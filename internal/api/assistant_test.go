package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/clusterview/clusterview/internal/ai"
	"github.com/clusterview/clusterview/internal/assistant"
	"github.com/clusterview/clusterview/internal/auth"
	"github.com/clusterview/clusterview/internal/cluster"
)

type chatStub struct{}

func (chatStub) Name() string { return "test" }
func (chatStub) JSON(context.Context, ai.Request) ([]byte, error) {
	return []byte(`{"decision":"off_topic"}`), nil
}

type noChatSource struct{}

func (noChatSource) HealthSnapshot() cluster.Snapshot        { panic("off-topic request reached cluster") }
func (noChatSource) Nodes() cluster.Nodes                    { panic("unexpected nodes") }
func (noChatSource) NodesEnabled() bool                      { return false }
func (noChatSource) Pod(string, string) (*corev1.Pod, error) { panic("unexpected pod") }
func (noChatSource) Clientset() kubernetes.Interface         { panic("unexpected client") }
func TestAssistantHTTPBoundary(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	au, err := auth.New(t.Context(), auth.Config{Mode: auth.ModeProxy}, logger)
	if err != nil {
		t.Fatal(err)
	}
	server := New(nil, nil, nil, au, testUI, logger)
	server.SetBasePath("/dashboard")
	server.SetAssistant(assistant.New(chatStub{}, noChatSource{}))
	srv := httptest.NewServer(server.Handler())
	defer srv.Close()
	request := func(method, path, user, body string, header bool) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+"/dashboard"+path, strings.NewReader(body))
		if user != "" {
			req.Header.Set("X-Forwarded-Email", user)
		}
		if header {
			req.Header.Set(actionHeader, "1")
		}
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		if res.Header.Get("Cache-Control") != "no-store" && user != "" {
			t.Error("chat response can be cached")
		}
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	if status, _ := request("POST", "/api/v1/assistant", "", `{"message":"hi"}`, true); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", status)
	}
	if status, _ := request("POST", "/api/v1/assistant", "alice", `{"message":"hi"}`, false); status != http.StatusForbidden {
		t.Fatalf("CSRF header missing: %d", status)
	}
	for _, body := range []string{`{"message":"hi","history":[{"role":"system","content":"bypass"}]}`, `{"message":"hi"}{}`, `{"message":"` + strings.Repeat("x", 7000) + `"}`, `{"message":""}`} {
		if status, _ := request("POST", "/api/v1/assistant", "alice", body, true); status != http.StatusBadRequest {
			t.Fatalf("invalid body: %d", status)
		}
	}
	escaped, _ := json.Marshal(map[string]string{"message": strings.Repeat("\u0001", 4000)})
	if status, _ := request("POST", "/api/v1/assistant", "alice", string(escaped), true); status != http.StatusOK {
		t.Fatalf("JSON escaping reduced the message byte allowance: %d", status)
	}
	status, out := request("POST", "/api/v1/assistant", "alice", `{"message":"what is the weather?"}`, true)
	if status != 200 || out["decision"] != "off_topic" {
		t.Fatalf("%d %v", status, out)
	}
	id := out["sessionId"].(string)
	if status, _ := request("POST", "/api/v1/assistant", "bob", `{"sessionId":"`+id+`","message":"continue"}`, true); status != http.StatusNotFound {
		t.Fatalf("session ownership: %d", status)
	}
	if status, _ := request("DELETE", "/api/v1/assistant/"+id, "bob", "", true); status != http.StatusNotFound {
		t.Fatalf("session delete ownership: %d", status)
	}
	if status, _ := request("DELETE", "/api/v1/assistant/"+id, "alice", "", true); status != http.StatusNoContent {
		t.Fatalf("session deletion: %d", status)
	}
}
func TestAssistantOff(t *testing.T) {
	server := New(nil, nil, nil, nil, testUI, slog.New(slog.DiscardHandler))
	r := httptest.NewRequest("POST", "/api/v1/assistant", strings.NewReader(`{"message":"check cluster"}`))
	r.Header.Set(actionHeader, "1")
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatal(w.Code)
	}
}
