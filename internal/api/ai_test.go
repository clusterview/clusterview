package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFindErrorWithoutAI(t *testing.T) {
	srv := httptest.NewServer(New(nil, nil, nil, nil, testUI, slog.New(slog.DiscardHandler)).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/ai")
	if err != nil {
		t.Fatal(err)
	}
	var info AIInfo
	_ = json.NewDecoder(resp.Body).Decode(&info)
	_ = resp.Body.Close()
	if info.Enabled {
		t.Errorf("AI should be off: %+v", info)
	}

	if code, _ := post(t, srv.URL+"/api/v1/diagnose", `{"serviceId":"Deployment/shop/api"}`, false); code != http.StatusForbidden {
		t.Errorf("without the action header: %d", code)
	}
	if code, body := post(t, srv.URL+"/api/v1/diagnose", `{"serviceId":"Deployment/shop/api"}`, true); code != http.StatusNotFound || body["error"] == "" {
		t.Errorf("with AI off: %d %v", code, body)
	}
}
