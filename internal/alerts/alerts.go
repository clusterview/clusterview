// Package alerts notifies a webhook when a service stays broken and when it
// recovers. It uses the dashboard's scoped cache, without extra cluster access.
package alerts

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/clusterview/clusterview/internal/cluster"
)

const pollEvery = 15 * time.Second

// Config selects one destination. An empty WebhookURL turns alerts off.
type Config struct {
	// WebhookURL is a secret: read it from the environment, never a flag.
	WebhookURL   string
	Format       string // webhook (structured JSON), slack, or teams
	For          time.Duration
	Cluster      string
	DashboardURL string
}

// Source is the same scoped view the dashboard uses.
type Source interface {
	Ready() bool
	// HealthSnapshot must not enqueue AI descriptions.
	HealthSnapshot() cluster.Snapshot
}

// Service contains only identity and health, never logs or raw event messages.
type Service struct {
	ID        string         `json:"id"`
	Namespace string         `json:"namespace"`
	Name      string         `json:"name"`
	Title     string         `json:"title"`
	Status    cluster.Status `json:"status"`
	Ready     int            `json:"ready"`
	Desired   int            `json:"desired"`
}

// Problem is a detected problem's kind and plain title, without its detail.
type Problem struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
}

// Event is the custom webhook payload. Retries of an event keep the same ID.
type Event struct {
	ID           string    `json:"id"`
	State        string    `json:"state"` // firing or resolved
	Cluster      string    `json:"cluster"`
	Service      Service   `json:"service"`
	Problems     []Problem `json:"problems"`
	StartedAt    time.Time `json:"startedAt"`
	ObservedAt   time.Time `json:"observedAt"`
	DashboardURL string    `json:"dashboardUrl,omitempty"`
	Text         string    `json:"text"`
}

type incident struct {
	since       time.Time
	notified    bool
	failures    int
	nextAttempt time.Time
}

// Monitor tracks incidents in memory. Run it once per process.
type Monitor struct {
	cfg       Config
	src       Source
	log       *slog.Logger
	http      *http.Client
	now       func() time.Time
	incidents map[string]*incident
	notBefore time.Time // destination-wide cooldown after rate limiting
}

// New validates settings without contacting the destination. When off it
// returns nil, so the dashboard needs no alert destination to work.
func New(cfg Config, src Source, log *slog.Logger) (*Monitor, error) {
	if cfg.WebhookURL == "" {
		return nil, nil
	}
	if !validURL(cfg.WebhookURL) {
		return nil, fmt.Errorf("alerts webhook must be an http(s) URL without user information or a fragment")
	}
	if cfg.DashboardURL != "" && !validURL(cfg.DashboardURL) {
		return nil, fmt.Errorf("alerts dashboard URL must be an http(s) URL without user information or a fragment")
	}
	if cfg.For <= 0 {
		return nil, fmt.Errorf("alerts-for must be a positive duration, such as 5m")
	}
	if cfg.Format == "" {
		cfg.Format = "webhook"
	}
	if cfg.Format != "webhook" && cfg.Format != "slack" && cfg.Format != "teams" {
		return nil, fmt.Errorf("alerts format must be webhook, slack or teams")
	}
	if cfg.Cluster == "" {
		cfg.Cluster = "cluster"
	}
	return &Monitor{
		cfg: cfg, src: src, log: log, now: time.Now, incidents: map[string]*incident{},
		http: &http.Client{
			Timeout: 10 * time.Second,
			// A redirect must not forward a message to a different destination.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func validURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}

// Run samples health until shutdown. No notifications leave the process
// before its first complete read of the watched namespaces.
func (m *Monitor) Run(ctx context.Context) {
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if m.src.Ready() {
			m.check(ctx, m.now().UTC())
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func broken(s cluster.Service) bool {
	if s.Status == cluster.StatusDown || s.Status == cluster.StatusDegraded {
		return true
	}
	for _, p := range s.Problems {
		if p.Severity == cluster.SeverityCritical {
			return true
		}
	}
	return false
}

func (m *Monitor) check(ctx context.Context, now time.Time) {
	seen := map[string]bool{}
	for _, s := range m.src.HealthSnapshot().Services {
		if ctx.Err() != nil {
			return
		}
		seen[s.ID] = true
		i := m.incidents[s.ID]
		if broken(s) {
			if i == nil {
				m.incidents[s.ID] = &incident{since: now}
				continue
			}
			if !i.notified && now.Sub(i.since) >= m.cfg.For && !now.Before(i.nextAttempt) && !now.Before(m.notBefore) {
				if m.deliver(ctx, m.event(s, i, "firing", now), i) {
					i.notified = true
				}
			}
			continue
		}
		if i == nil {
			continue
		}
		// Unknown is not evidence of recovery. It also breaks the continuous
		// failure window for an incident we haven't notified about yet.
		if !i.notified {
			delete(m.incidents, s.ID)
		} else if (s.Status == cluster.StatusHealthy || s.Status == cluster.StatusIdle) && !now.Before(i.nextAttempt) && !now.Before(m.notBefore) {
			if m.deliver(ctx, m.event(s, i, "resolved", now), i) {
				delete(m.incidents, s.ID)
			}
		}
	}
	for id := range m.incidents {
		if !seen[id] {
			// Deletion isn't recovery. Forget it without claiming it's healthy.
			delete(m.incidents, id)
		}
	}
}

func (m *Monitor) deliver(ctx context.Context, e Event, i *incident) bool {
	err := m.send(ctx, e)
	// Sequential deliveries can take longer than a retry delay. Start both
	// cooldowns when this attempt finishes, not when the snapshot was read.
	finishedAt := m.now()
	if err == nil {
		i.failures, i.nextAttempt = 0, time.Time{}
		m.log.Info("alert delivered", "service", e.Service.ID, "state", e.State, "event", e.ID)
		return true
	}
	i.failures = min(i.failures+1, 6)
	delay := pollEvery
	for n := 1; n < i.failures && delay < 5*time.Minute; n++ {
		delay *= 2
	}
	delay = min(delay, 5*time.Minute)
	var rejected *deliveryError
	if errors.As(err, &rejected) {
		delay = max(delay, rejected.retryAfter)
		if rejected.status == http.StatusTooManyRequests {
			m.notBefore = finishedAt.Add(delay)
		}
	}
	i.nextAttempt = finishedAt.Add(delay)
	// send returns sanitized errors; webhook URLs and response bodies can
	// contain credentials and must never be logged.
	m.log.Warn("alert delivery failed; will retry", "service", e.Service.ID, "state", e.State, "retryIn", delay, "err", err)
	return false
}

func (m *Monitor) event(s cluster.Service, i *incident, state string, now time.Time) Event {
	id := sha256.Sum256([]byte(s.ID + "\n" + i.since.Format(time.RFC3339Nano) + "\n" + state))
	e := Event{
		ID: fmt.Sprintf("%x", id[:16]), State: state, Cluster: m.cfg.Cluster,
		Service:  Service{ID: s.ID, Namespace: s.Namespace, Name: s.Name, Title: s.Title, Status: s.Status, Ready: s.Ready, Desired: s.Desired},
		Problems: []Problem{}, StartedAt: i.since, ObservedAt: now, DashboardURL: m.cfg.DashboardURL,
	}
	for _, p := range s.Problems {
		if p.Severity == cluster.SeverityCritical {
			e.Problems = append(e.Problems, Problem{Kind: p.Kind, Title: p.Title})
		}
	}
	e.Text = message(e)
	return e
}
