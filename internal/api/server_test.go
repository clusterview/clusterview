package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/clusterview/clusterview/internal/kube"
)

var testUI = fstest.MapFS{
	"index.html":    {Data: []byte("<html>app</html>")},
	"assets/app.js": {Data: []byte("console.log(1)")},
}

func newTestServer(k *kube.Client) *httptest.Server {
	return httptest.NewServer(New(k, nil, nil, nil, testUI, slog.New(slog.DiscardHandler)).Handler())
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestHealthz(t *testing.T) {
	srv := newTestServer(nil)
	defer srv.Close()
	if code, body := get(t, srv.URL+"/healthz"); code != 200 || body != "ok" {
		t.Fatalf("got %d %q", code, body)
	}
}

func TestClusterConnected(t *testing.T) {
	cs := fake.NewClientset()
	cs.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{GitVersion: "v1.34.0"}
	srv := newTestServer(&kube.Client{Clientset: cs, Mode: kube.InCluster})
	defer srv.Close()

	code, body := get(t, srv.URL+"/api/v1/cluster")
	var info ClusterInfo
	if err := json.Unmarshal([]byte(body), &info); err != nil || code != 200 {
		t.Fatalf("got %d %q (%v)", code, body, err)
	}
	if !info.Connected || info.ServerVersion != "v1.34.0" || info.Mode != "in-cluster" {
		t.Fatalf("unexpected %+v", info)
	}
}

func TestClusterNotConfigured(t *testing.T) {
	srv := newTestServer(nil)
	defer srv.Close()
	_, body := get(t, srv.URL+"/api/v1/cluster")
	var info ClusterInfo
	_ = json.Unmarshal([]byte(body), &info)
	if info.Connected || info.Error == "" {
		t.Fatalf("unexpected %+v", info)
	}
}

func TestUIRoutes(t *testing.T) {
	srv := newTestServer(nil)
	defer srv.Close()

	cases := []struct {
		path, want string
		code       int
	}{
		{"/", "<html>app</html>", 200},
		{"/services/payments", "<html>app</html>", 200}, // browser route falls back to the app
		{"/assets", "<html>app</html>", 200},            // directories are never listed
		{"/assets/app.js", "console.log(1)", 200},
	}
	for _, c := range cases {
		if code, body := get(t, srv.URL+c.path); code != c.code || body != c.want {
			t.Errorf("%s: got %d %q", c.path, code, body)
		}
	}
	if code, _ := get(t, srv.URL+"/api/v1/nope"); code != 404 {
		t.Errorf("unknown API route: got %d, want 404", code)
	}
}
