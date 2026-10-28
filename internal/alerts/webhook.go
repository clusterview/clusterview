package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

func message(e Event) string {
	state := "Needs attention"
	if e.State == "resolved" {
		state = "Recovered"
	}
	text := fmt.Sprintf("%s: %s/%s on %s\n%d/%d replicas ready", state, e.Service.Namespace, e.Service.Name, e.Cluster, e.Service.Ready, e.Service.Desired)
	for _, p := range e.Problems {
		text += "\n" + p.Title
	}
	if e.DashboardURL != "" {
		text += "\n" + e.DashboardURL
	}
	// Slack's plain-text section accepts at most 3000 characters.
	runes := []rune(text)
	if len(runes) > 2900 {
		text = string(runes[:2900]) + "…"
	}
	return text
}

func (m *Monitor) payload(e Event) any {
	switch m.cfg.Format {
	case "slack":
		return map[string]any{
			"text": e.Text, "mrkdwn": false, "unfurl_links": false, "unfurl_media": false,
			"blocks": []any{map[string]any{
				"type": "section", "text": map[string]any{"type": "plain_text", "text": e.Text},
			}},
		}
	case "teams":
		return map[string]any{
			"type": "message",
			"attachments": []any{map[string]any{
				"contentType": "application/vnd.microsoft.card.adaptive",
				"content": map[string]any{
					"$schema": "https://adaptivecards.io/schemas/adaptive-card.json", "type": "AdaptiveCard", "version": "1.2",
					"body": []any{map[string]any{"type": "TextBlock", "text": e.Text, "wrap": true}},
				},
			}},
		}
	default:
		return e
	}
}

func (m *Monitor) send(ctx context.Context, e Event) error {
	body, err := json.Marshal(m.payload(e))
	if err != nil {
		return errors.New("couldn't encode the alert")
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, m.cfg.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return errors.New("couldn't create the webhook request")
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("User-Agent", "clusterview-alerts")
	r.Header.Set("Idempotency-Key", e.ID)
	resp, err := m.http.Do(r)
	if err != nil {
		// net/http errors contain the entire URL, including a webhook's token.
		return errors.New("couldn't reach the alert webhook (check the destination and network)")
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &deliveryError{status: resp.StatusCode, retryAfter: retryAfter(resp.Header.Get("Retry-After"))}
	}
	return nil
}

type deliveryError struct {
	status     int
	retryAfter time.Duration
}

func (e *deliveryError) Error() string {
	return fmt.Sprintf("alert webhook returned HTTP %d %s", e.status, http.StatusText(e.status))
}

func retryAfter(value string) time.Duration {
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(min(seconds, 3600)) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		return min(max(time.Until(at), 0), time.Hour)
	}
	return 0
}
