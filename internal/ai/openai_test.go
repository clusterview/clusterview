package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIFallsBackToJSONMode(t *testing.T) {
	var formats []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ResponseFormat struct {
				Type string `json:"type"`
			} `json:"response_format"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		formats = append(formats, body.ResponseFormat.Type)
		if r.Header.Get("Authorization") != "" {
			t.Errorf("no key set, yet sent %q", r.Header.Get("Authorization"))
		}
		if body.ResponseFormat.Type == "json_schema" {
			http.Error(w, `{"error":{"message":"json_schema unsupported"}}`, http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Sure! ` + "```json\\n{\\\"ok\\\": true}\\n```" + `"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	m, err := New(Config{Provider: "openai", Model: "llama3.3", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.JSON(context.Background(), Request{System: "s", User: "u", SchemaName: "x", Schema: map[string]any{"type": "object"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"ok": true}` {
		t.Errorf("got %s", got)
	}
	if len(formats) != 2 || formats[0] != "json_schema" || formats[1] != "json_object" {
		t.Errorf("formats tried: %v", formats)
	}
	if m.Name() != "llama3.3" {
		t.Errorf("a local model is named by itself: %q", m.Name())
	}
}

func TestOpenAIRetriesOnce(t *testing.T) {
	retryWait = 0
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, `{"error":{"message":"overloaded"}}`, http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	m, _ := New(Config{Provider: "openai", Model: "m", BaseURL: srv.URL})
	if _, err := m.JSON(context.Background(), Request{Schema: map[string]any{}}); err != nil || calls != 2 {
		t.Errorf("a 503 should be tried again once: %d calls, %v", calls, err)
	}
}

func TestNewChecksConfig(t *testing.T) {
	if m, err := New(Config{}); m != nil || err != nil {
		t.Errorf("no provider means off: %v %v", m, err)
	}
	for _, c := range []Config{
		{Provider: "anthropic"},
		{Provider: "openai", APIKey: "k"},
		{Provider: "openai", Model: "gpt"},
		{Provider: "gemini", APIKey: "k"},
	} {
		if _, err := New(c); err == nil {
			t.Errorf("%+v should be refused", c)
		}
	}
	m, err := New(Config{Provider: "anthropic", APIKey: "k"})
	if err != nil || m.Name() != "Anthropic · "+DefaultAnthropicModel {
		t.Errorf("anthropic defaults: %v %v", m, err)
	}
}
