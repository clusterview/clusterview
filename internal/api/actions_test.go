package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/clusterview/clusterview/internal/actions"
	"github.com/clusterview/clusterview/internal/auth"
)

func post(t *testing.T, url, body string, header bool) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if header {
		req.Header.Set(actionHeader, "1")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func actionServer(cfg actions.Config) *httptest.Server {
	cs := fake.NewClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api"},
		Spec:       appsv1.DeploymentSpec{Replicas: new(int32)},
	})
	log := slog.New(slog.DiscardHandler)
	r := actions.NewRunner(cfg, cs, nil, log)
	return httptest.NewServer(New(nil, nil, r, nil, testUI, log).Handler())
}

func TestActionRoutes(t *testing.T) {
	srv := actionServer(actions.Config{Enabled: true})
	defer srv.Close()

	if code, _ := post(t, srv.URL+"/api/v1/workloads/Deployment/shop/api/restart", "", false); code != http.StatusForbidden {
		t.Errorf("without the action header: %d", code)
	}
	if code, body := post(t, srv.URL+"/api/v1/workloads/Deployment/shop/api/restart", "", true); code != 200 || body["ok"] != true {
		t.Errorf("restart: %d %v", code, body)
	}
	if code, body := post(t, srv.URL+"/api/v1/workloads/Deployment/shop/api/scale", `{"replicas": 3}`, true); code != 200 || body["detail"] != "from 0 to 3 replicas" {
		t.Errorf("scale: %d %v", code, body)
	}
	if code, _ := post(t, srv.URL+"/api/v1/workloads/Deployment/shop/api/scale", `{}`, true); code != http.StatusConflict {
		t.Errorf("scale without replicas: %d", code)
	}
	if code, _ := post(t, srv.URL+"/api/v1/workloads/Deployment/shop/nope/restart", "", true); code != http.StatusNotFound {
		t.Errorf("missing: %d", code)
	}

	// A malformed request never reaches the cluster, so it isn't audited.
	_, audit := get(t, srv.URL+"/api/v1/audit")
	var entries []actions.Entry
	if err := json.Unmarshal([]byte(audit), &entries); err != nil || len(entries) != 3 || entries[0].Target != "Deployment/shop/nope" {
		t.Errorf("audit: %s", audit)
	}
}

func TestActionsOff(t *testing.T) {
	srv := actionServer(actions.Config{})
	defer srv.Close()
	if code, body := post(t, srv.URL+"/api/v1/workloads/Deployment/shop/api/restart", "", true); code != http.StatusForbidden || !strings.Contains(body["error"].(string), "switched off") {
		t.Errorf("disabled: %d %v", code, body)
	}
	_, cfg := get(t, srv.URL+"/api/v1/actions")
	if !strings.Contains(cfg, `"enabled":false`) || !strings.Contains(cfg, `"maxReplicas":20`) {
		t.Errorf("config: %s", cfg)
	}
}

func TestViewersCantAct(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	au, _ := auth.New(context.Background(), auth.Config{Mode: auth.ModeProxy, Operators: []string{"ops@example.com"}}, log)
	r := actions.NewRunner(actions.Config{Enabled: true}, fake.NewClientset(), nil, log)
	srv := httptest.NewServer(New(nil, nil, r, au, testUI, log).Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/workloads/Deployment/shop/api/restart", nil)
	req.Header.Set(actionHeader, "1")
	req.Header.Set("X-Forwarded-Email", "dev@example.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "can look but not change things") {
		t.Errorf("viewer: %d %s", resp.StatusCode, body)
	}
}
