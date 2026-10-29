package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestGeminiAndCompletionBudget(t *testing.T) {
	var budget int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("key not forwarded to configured provider")
		}
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.Unmarshal(body["max_tokens"], &budget)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	m, err := New(Config{Provider: ProviderGemini, APIKey: "test-key", Model: "configured-model", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if m.Name() != "Gemini · configured-model" {
		t.Fatal(m.Name())
	}
	if _, err := m.JSON(t.Context(), Request{MaxTokens: 1024, Schema: map[string]any{"type": "object"}}); err != nil {
		t.Fatal(err)
	}
	if budget != 1024 {
		t.Fatal("output budget not sent")
	}
	defaultModel, err := New(Config{Provider: ProviderGemini, APIKey: "test-key", Model: "configured-model"})
	if err != nil {
		t.Fatal(err)
	}
	if defaultModel.(*openAIModel).baseURL != "https://generativelanguage.googleapis.com/v1beta/openai" {
		t.Fatal("wrong Gemini endpoint")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOfficialOpenAICompletionBudget(t *testing.T) {
	m := newOpenAI(Config{Model: "configured-model", APIKey: "test-key"})
	m.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if string(body["max_completion_tokens"]) != "1024" || body["max_tokens"] != nil {
			t.Error("wrong token budget parameter for official endpoint")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{}"},"finish_reason":"stop"}]}`))}, nil
	})
	if _, err := m.JSON(t.Context(), Request{MaxTokens: 1024, Schema: map[string]any{"type": "object"}}); err != nil {
		t.Fatal(err)
	}
}

func TestMissingKeyIsDistinctFromOtherConfigurationErrors(t *testing.T) {
	for _, provider := range []string{ProviderAnthropic, ProviderOpenAI, ProviderGemini} {
		model, err := New(Config{Provider: provider})
		if model != nil || !errors.Is(err, ErrMissingKey) {
			t.Errorf("%s: expected missing-key error, got %v", provider, err)
		}
	}
	for _, cfg := range []Config{{Provider: "unknown"}, {Provider: ProviderOpenAI, APIKey: "test-key"}, {Provider: ProviderGemini, APIKey: "test-key"}} {
		if _, err := New(cfg); err == nil || errors.Is(err, ErrMissingKey) {
			t.Errorf("other configuration error classified as missing key: %v", err)
		}
	}
}
