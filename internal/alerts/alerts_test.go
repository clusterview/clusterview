package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/clusterview/clusterview/internal/cluster"
)

type source struct {
	ready       bool
	services    []cluster.Service
	readyChecks chan struct{}
}

func (s *source) Ready() bool {
	if s.readyChecks != nil {
		select {
		case s.readyChecks <- struct{}{}:
		default:
		}
	}
	return s.ready
}
func (s *source) HealthSnapshot() cluster.Snapshot {
	return cluster.Snapshot{Services: s.services}
}

type receiver struct {
	mu       sync.Mutex
	events   []Event
	statuses []int
	retry    string
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var e Event
	_ = json.NewDecoder(req.Body).Decode(&e)
	r.events = append(r.events, e)
	if req.Method != http.MethodPost || req.Header.Get("Content-Type") != "application/json" || req.Header.Get("Idempotency-Key") != e.ID {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if r.retry != "" {
		w.Header().Set("Retry-After", r.retry)
	}
	status := http.StatusNoContent
	if len(r.statuses) > 0 {
		status, r.statuses = r.statuses[0], r.statuses[1:]
	}
	w.WriteHeader(status)
}

func (r *receiver) got() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event{}, r.events...)
}

func setup(t *testing.T, r *receiver) (*Monitor, *source) {
	t.Helper()
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	src := &source{ready: true}
	m, err := New(Config{WebhookURL: srv.URL, For: 5 * time.Minute, Cluster: "test-cluster"}, src, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return m, src
}

// checkAt advances both observation and delivery time without sleeping.
func checkAt(ctx context.Context, m *Monitor, now time.Time) {
	m.now = func() time.Time { return now }
	m.check(ctx, now)
}

func service(status cluster.Status) cluster.Service {
	return cluster.Service{ID: "Deployment/shop/orders", Namespace: "shop", Name: "orders", Title: "Orders", Status: status, Ready: 0, Desired: 2}
}

func TestAlertsWaitDeduplicateResolveAndRearm(t *testing.T) {
	r := &receiver{}
	m, src := setup(t, r)
	src.services = []cluster.Service{service(cluster.StatusDown)}
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	checkAt(ctx, m, now)
	checkAt(ctx, m, now.Add(5*time.Minute-time.Second))
	if len(r.got()) != 0 {
		t.Fatal("notified before the delay elapsed")
	}
	checkAt(ctx, m, now.Add(5*time.Minute))
	checkAt(ctx, m, now.Add(20*time.Minute))
	got := r.got()
	if len(got) != 1 || got[0].State != "firing" || got[0].Cluster != "test-cluster" || !got[0].StartedAt.Equal(now) {
		t.Fatalf("expected one firing notification: %+v", got)
	}
	src.services[0].Status = cluster.StatusUnknown
	checkAt(ctx, m, now.Add(21*time.Minute))
	if len(r.got()) != 1 {
		t.Fatal("unknown health isn't recovery")
	}
	src.services[0].Status, src.services[0].Ready = cluster.StatusHealthy, 2
	checkAt(ctx, m, now.Add(22*time.Minute))
	checkAt(ctx, m, now.Add(23*time.Minute))
	got = r.got()
	if len(got) != 2 || got[1].State != "resolved" || !strings.Contains(got[1].Text, "Recovered") || got[1].ID == got[0].ID {
		t.Fatalf("expected one distinct recovery: %+v", got)
	}
	src.services[0] = service(cluster.StatusDegraded)
	checkAt(ctx, m, now.Add(24*time.Minute))
	checkAt(ctx, m, now.Add(29*time.Minute))
	got = r.got()
	if len(got) != 3 || got[2].ID == got[0].ID || got[2].State != "firing" {
		t.Fatalf("expected a new incident: %+v", got)
	}
}

func TestTransientFailuresAndUnknownResetDelay(t *testing.T) {
	r := &receiver{}
	m, src := setup(t, r)
	ctx, now := context.Background(), time.Now().UTC()
	for _, status := range []cluster.Status{cluster.StatusHealthy, cluster.StatusIdle, cluster.StatusUnknown} {
		src.services = []cluster.Service{service(cluster.StatusDown)}
		checkAt(ctx, m, now)
		src.services[0].Status = status
		checkAt(ctx, m, now.Add(4*time.Minute))
		src.services[0].Status = cluster.StatusDown
		checkAt(ctx, m, now.Add(5*time.Minute))
		checkAt(ctx, m, now.Add(9*time.Minute))
		if len(r.got()) != 0 {
			t.Fatalf("a brief failure followed by %s should reset the delay", status)
		}
		src.services = nil
		checkAt(ctx, m, now.Add(10*time.Minute))
		now = now.Add(20 * time.Minute)
	}
}

func TestCriticalProblemsAlertButWarningsDoNot(t *testing.T) {
	r := &receiver{}
	m, src := setup(t, r)
	ctx, now := context.Background(), time.Now().UTC()
	s := service(cluster.StatusHealthy)
	s.Problems = []cluster.Problem{{Kind: "restarted", Severity: cluster.SeverityWarning, Title: "Restarted recently", Detail: "secret raw detail"}}
	src.services = []cluster.Service{s}
	checkAt(ctx, m, now)
	checkAt(ctx, m, now.Add(10*time.Minute))
	if len(r.got()) != 0 {
		t.Fatal("a warning alone shouldn't alert")
	}
	src.services[0].Problems = append(s.Problems, cluster.Problem{Kind: "crash-loop", Severity: cluster.SeverityCritical, Title: "Keeps crashing", Detail: "password=never-send-this"})
	checkAt(ctx, m, now.Add(11*time.Minute))
	checkAt(ctx, m, now.Add(16*time.Minute))
	got := r.got()
	if len(got) != 1 || len(got[0].Problems) != 1 || got[0].Problems[0].Kind != "crash-loop" {
		t.Fatalf("expected just the critical problem: %+v", got)
	}
	raw, _ := json.Marshal(got[0])
	if strings.Contains(string(raw), "never-send-this") || strings.Contains(string(raw), "secret raw detail") {
		t.Fatal("raw Kubernetes detail must not be sent")
	}
}

func TestRemovedServicesAreNotCalledRecovered(t *testing.T) {
	r := &receiver{}
	m, src := setup(t, r)
	ctx, now := context.Background(), time.Now().UTC()
	src.services = []cluster.Service{service(cluster.StatusDown)}
	checkAt(ctx, m, now)
	checkAt(ctx, m, now.Add(5*time.Minute))
	src.services = nil
	checkAt(ctx, m, now.Add(6*time.Minute))
	if len(r.got()) != 1 || len(m.incidents) != 0 {
		t.Fatal("removal should clear the incident without claiming recovery")
	}
	src.services = []cluster.Service{service(cluster.StatusDown)}
	checkAt(ctx, m, now.Add(7*time.Minute))
	if len(r.got()) != 1 {
		t.Fatal("a reappearing service must wait through the delay again")
	}
}

func TestFailedDeliveryRetriesWithoutLosingRecovery(t *testing.T) {
	r := &receiver{statuses: []int{503, 204, 503, 204}}
	m, src := setup(t, r)
	ctx, now := context.Background(), time.Now().UTC()
	src.services = []cluster.Service{service(cluster.StatusDown)}
	checkAt(ctx, m, now)
	checkAt(ctx, m, now.Add(5*time.Minute))
	checkAt(ctx, m, now.Add(5*time.Minute+time.Second))
	if len(r.got()) != 1 {
		t.Fatal("failed delivery should back off")
	}
	checkAt(ctx, m, now.Add(5*time.Minute+pollEvery))
	src.services[0].Status = cluster.StatusIdle
	checkAt(ctx, m, now.Add(6*time.Minute))
	checkAt(ctx, m, now.Add(6*time.Minute+pollEvery))
	checkAt(ctx, m, now.Add(7*time.Minute))
	got := r.got()
	if len(got) != 4 || got[0].ID != got[1].ID || got[2].ID != got[3].ID || got[2].State != "resolved" || len(m.incidents) != 0 {
		t.Fatalf("both firing and recovery should retry with stable IDs: %+v", got)
	}
}

func TestRecoveryBeforeSuccessfulDeliveryIsQuiet(t *testing.T) {
	r := &receiver{statuses: []int{503}}
	m, src := setup(t, r)
	ctx, now := context.Background(), time.Now().UTC()
	src.services = []cluster.Service{service(cluster.StatusDown)}
	checkAt(ctx, m, now)
	checkAt(ctx, m, now.Add(5*time.Minute))
	src.services[0].Status = cluster.StatusHealthy
	checkAt(ctx, m, now.Add(6*time.Minute))
	if len(r.got()) != 1 || len(m.incidents) != 0 {
		t.Fatal("don't send recovery for an alert the destination never accepted")
	}
}

func TestRateLimitCoolsDownWholeDestination(t *testing.T) {
	r := &receiver{statuses: []int{429}, retry: "120"}
	m, src := setup(t, r)
	ctx, now := context.Background(), time.Now().UTC()
	a, b := service(cluster.StatusDown), service(cluster.StatusDown)
	b.ID, b.Namespace = "Deployment/other/orders", "other"
	src.services = []cluster.Service{a, b}
	checkAt(ctx, m, now)
	checkAt(ctx, m, now.Add(5*time.Minute))
	checkAt(ctx, m, now.Add(6*time.Minute))
	if len(r.got()) != 1 {
		t.Fatal("Retry-After should delay all services sharing the destination")
	}
	checkAt(ctx, m, now.Add(7*time.Minute))
	got := r.got()
	if len(got) != 3 || got[1].Service.Namespace != "shop" || got[2].Service.Namespace != "other" {
		t.Fatalf("both scoped service identities should be delivered after cooldown: %+v", got)
	}
}

func TestConfig(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if m, err := New(Config{}, nil, log); m != nil || err != nil {
		t.Fatalf("alerts should be off by default: %v, %v", m, err)
	}
	for _, cfg := range []Config{
		{WebhookURL: "file:///secret", For: time.Minute},
		{WebhookURL: "https://user:password@example.com", For: time.Minute},
		{WebhookURL: "https://example.com/#fragment", For: time.Minute},
		{WebhookURL: "https://example.com/", For: -time.Minute},
		{WebhookURL: "https://example.com/", For: time.Minute, Format: "unknown"},
		{WebhookURL: "https://example.com/", For: time.Minute, DashboardURL: "javascript:alert(1)"},
	} {
		if _, err := New(cfg, nil, log); err == nil {
			t.Errorf("invalid config accepted: format=%s, duration=%s", cfg.Format, cfg.For)
		}
	}
}

func TestPayloadFormats(t *testing.T) {
	e := Event{ID: "id", State: "firing", Text: "Orders is down"}
	for _, format := range []string{"webhook", "slack", "teams"} {
		t.Run(format, func(t *testing.T) {
			var got map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&got)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()
			m, _ := New(Config{WebhookURL: srv.URL, For: time.Minute, Format: format}, nil, nil)
			if err := m.send(context.Background(), e); err != nil {
				t.Fatal(err)
			}
			switch format {
			case "webhook":
				if got["state"] != "firing" || got["id"] != "id" {
					t.Fatalf("custom webhook needs structured event: %+v", got)
				}
			case "slack":
				block := got["blocks"].([]any)[0].(map[string]any)
				if got["text"] != e.Text || block["text"].(map[string]any)["type"] != "plain_text" || got["mrkdwn"] != false {
					t.Fatalf("Slack should use plain text: %+v", got)
				}
			case "teams":
				card := got["attachments"].([]any)[0].(map[string]any)
				if got["type"] != "message" || card["contentType"] != "application/vnd.microsoft.card.adaptive" || card["content"].(map[string]any)["type"] != "AdaptiveCard" {
					t.Fatalf("Teams needs an Adaptive Card envelope: %+v", got)
				}
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDeliveryErrorsDoNotExposeWebhookSecrets(t *testing.T) {
	var logs bytes.Buffer
	m, _ := New(Config{WebhookURL: "https://example.com/secret-path?token=private", For: time.Minute}, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	m.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport error containing secret-path and token=private")
	})
	i := &incident{}
	m.deliver(context.Background(), Event{ID: "event"}, i)
	if strings.Contains(logs.String(), "secret-path") || strings.Contains(logs.String(), "private") {
		t.Fatalf("credentials leaked in logs: %s", logs.String())
	}
	m.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("token=private"))}, nil
	})
	err := m.send(context.Background(), Event{})
	if err == nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("response bodies must not reach logs: %v", err)
	}
}

func TestRedirectDoesNotForwardPayload(t *testing.T) {
	var mu sync.Mutex
	forwarded := false
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		mu.Lock()
		forwarded = true
		mu.Unlock()
	}))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	m, _ := New(Config{WebhookURL: redirect.URL, For: time.Minute}, nil, nil)
	if err := m.send(context.Background(), Event{}); err == nil {
		t.Fatal("redirect must be reported as failed delivery")
	}
	mu.Lock()
	defer mu.Unlock()
	if forwarded {
		t.Fatal("redirect forwarded the alert to another destination")
	}
}

func TestRunWaitsForInitialSyncAndStopsOnCancellation(t *testing.T) {
	r := &receiver{}
	m, src := setup(t, r)
	src.ready = false
	src.readyChecks = make(chan struct{}, 1)
	src.services = []cluster.Service{service(cluster.StatusDown)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	select {
	case <-src.readyChecks:
	case <-time.After(time.Second):
		t.Fatal("monitor didn't check cache readiness")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("monitor didn't stop on cancellation")
	}
	if len(m.incidents) != 0 || len(r.got()) != 0 {
		t.Fatal("monitor must ignore unsynced snapshots")
	}
}

func TestRetryAfterBoundsAndHTTPDate(t *testing.T) {
	for value, want := range map[string]time.Duration{
		"120": 2 * time.Minute, "999999": time.Hour, "0": 0, "-1": 0, "garbage": 0,
	} {
		if got := retryAfter(value); got != want {
			t.Errorf("Retry-After %q: got %s, want %s", value, got, want)
		}
	}
	future := time.Now().UTC().Add(2 * time.Minute).Format(http.TimeFormat)
	if got := retryAfter(future); got < 119*time.Second || got > 120*time.Second {
		t.Errorf("HTTP-date Retry-After should delay about two minutes, got %s", got)
	}
	if got := retryAfter(time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)); got != 0 {
		t.Errorf("past Retry-After should not delay, got %s", got)
	}
}
