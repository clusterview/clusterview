package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServedUnderAPath(t *testing.T) {
	s := New(nil, nil, nil, nil, testUI, slog.New(slog.DiscardHandler))
	s.SetBasePath("/clusterview")
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	for _, c := range []struct {
		path     string
		code     int
		location string
	}{
		{"/clusterview", http.StatusMovedPermanently, "/clusterview/"},
		{"/clusterview?select=x", http.StatusMovedPermanently, "/clusterview/?select=x"},
		{"/clusterview/", http.StatusOK, ""},
		{"/clusterview/assets/app.js", http.StatusOK, ""},
		{"/clusterview/api/v1/version", http.StatusOK, ""},
		{"/healthz", http.StatusOK, ""}, // the kubelet's probes ask at the root
		{"/readyz", http.StatusOK, ""},
		{"/", http.StatusNotFound, ""}, // the root belongs to whatever shares the host
		{"/api/v1/version", http.StatusNotFound, ""},
		{"/clusterviewer/", http.StatusNotFound, ""},
	} {
		resp, err := noFollow.Get(srv.URL + c.path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != c.code || resp.Header.Get("Location") != c.location {
			t.Errorf("%s: %d %q, want %d %q", c.path, resp.StatusCode, resp.Header.Get("Location"), c.code, c.location)
		}
	}
}
