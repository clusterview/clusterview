package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	typedcore "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"

	"github.com/clusterview/clusterview/internal/auth"
	"github.com/clusterview/clusterview/internal/cluster"
	"github.com/clusterview/clusterview/internal/kube"
)

// Keep informer watches deterministic while sending log requests through a real
// HTTP transport, including cancellation and streaming response bodies.
type logClient struct {
	kubernetes.Interface
	logs typedcore.CoreV1Interface
}

func (c logClient) IsWatchListSemanticsUnSupported() bool { return true }

func (c logClient) CoreV1() typedcore.CoreV1Interface {
	return logCore{CoreV1Interface: c.Interface.CoreV1(), logs: c.logs}
}

type logCore struct {
	typedcore.CoreV1Interface
	logs typedcore.CoreV1Interface
}

func (c logCore) Pods(ns string) typedcore.PodInterface {
	return logPods{PodInterface: c.CoreV1Interface.Pods(ns), logs: c.logs.Pods(ns)}
}

type logPods struct {
	typedcore.PodInterface
	logs typedcore.PodInterface
}

func (p logPods) GetLogs(name string, opts *corev1.PodLogOptions) *rest.Request {
	return p.logs.GetLogs(name, opts)
}

func apiLogPod(name string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: name, UID: types.UID(name), OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "api", Controller: new(true)}}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "example/api:1"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
}

func serviceLogServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *fake.Clientset) {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	transport, err := kubernetes.NewForConfig(&rest.Config{Host: upstream.URL, QPS: 100, Burst: 100})
	if err != nil {
		t.Fatal(err)
	}
	cs := fake.NewClientset(&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api"}, Spec: appsv1.StatefulSetSpec{Replicas: new(int32(2))}}, apiLogPod("api-0"), apiLogPod("api-1"))
	client := &kube.Client{Clientset: logClient{Interface: cs, logs: transport.CoreV1()}, Metrics: metricsfake.NewSimpleClientset()}
	logger := slog.New(slog.DiscardHandler)
	cache := cluster.NewCache(client, cluster.Options{Namespaces: []string{"shop"}}, logger)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	if !cache.Start(ctx) {
		t.Fatal("cache did not sync")
	}
	au, err := auth.New(ctx, auth.Config{Mode: auth.ModeProxy}, logger)
	if err != nil {
		t.Fatal(err)
	}
	server := New(client, cache, nil, au, testUI, logger)
	server.SetBasePath("/dashboard")
	srv := httptest.NewServer(server.Handler())
	t.Cleanup(srv.Close)
	return srv, cs
}

func viewerRequest(ctx context.Context, t *testing.T, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Forwarded-Email", "dev@example.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestServiceLogsRoutes(t *testing.T) {
	var requests atomic.Int32
	srv, _ := serviceLogServer(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = fmt.Fprint(w, "2026-10-29T12:00:00Z ready\n")
	})
	for _, route := range []string{"/api/v1/services/logs", "/api/v1/services/logs/follow"} {
		if code, _ := get(t, srv.URL+"/dashboard"+route); code != http.StatusUnauthorized {
			t.Errorf("unauthenticated %s: %d", route, code)
		}
	}
	for _, tc := range []struct {
		query string
		code  int
	}{
		{"", 400}, {"?serviceId=StatefulSet/other/api", 404}, {"?serviceId=StatefulSet/shop/api&tailLines=0", 400}, {"?serviceId=StatefulSet/shop/api&sinceTime=no", 400}, {"?serviceId=StatefulSet/shop/api&container=missing", 404},
	} {
		resp := viewerRequest(t.Context(), t, srv.URL+"/dashboard/api/v1/services/logs"+tc.query)
		_ = resp.Body.Close()
		if resp.StatusCode != tc.code {
			t.Errorf("%s: %d, want %d", tc.query, resp.StatusCode, tc.code)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid or out-of-scope requests reached Kubernetes logs")
	}
	resp := viewerRequest(t.Context(), t, srv.URL+"/dashboard/api/v1/services/logs?serviceId=StatefulSet/shop/api")
	defer func() { _ = resp.Body.Close() }()
	var logs cluster.ServiceLogs
	if err := json.NewDecoder(resp.Body).Decode(&logs); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || len(logs.Lines) != 2 || logs.Lines[0].Pod != "api-0" || logs.Lines[1].Pod != "api-1" || logs.Lines[0].Container != "app" {
		t.Fatalf("aggregate response: %d %+v", resp.StatusCode, logs)
	}
	previous := viewerRequest(t.Context(), t, srv.URL+"/dashboard/api/v1/services/logs/follow?serviceId=StatefulSet/shop/api&previous=true")
	_ = previous.Body.Close()
	if previous.StatusCode != 400 {
		t.Errorf("follow previous: %d", previous.StatusCode)
	}
}

func TestFollowServiceLogsReplacementAndCancellation(t *testing.T) {
	var active atomic.Int32
	srv, cs := serviceLogServer(t, func(w http.ResponseWriter, r *http.Request) {
		active.Add(1)
		defer active.Add(-1)
		if r.URL.Query().Get("follow") != "true" {
			t.Error("expected follow request")
		}
		_, _ = fmt.Fprintf(w, "2026-10-29T12:00:00Z %s\n", r.URL.Path)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	resp := viewerRequest(ctx, t, srv.URL+"/dashboard/api/v1/services/logs/follow?serviceId=StatefulSet/shop/api")
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal("expected SSE")
	}
	lines := make(chan cluster.ServiceLogLine, 10)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(resp.Body)
		event := ""
		for scanner.Scan() {
			text := scanner.Text()
			if strings.HasPrefix(text, "event: ") {
				event = strings.TrimPrefix(text, "event: ")
			}
			if event == "line" && strings.HasPrefix(text, "data: ") {
				var line cluster.ServiceLogLine
				if json.Unmarshal([]byte(strings.TrimPrefix(text, "data: ")), &line) == nil {
					select {
					case lines <- line:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case line := <-lines:
			seen[line.Pod] = true
		case <-ctx.Done():
			t.Fatal("did not receive both pods")
		}
	}
	if err := cs.CoreV1().Pods("shop").Delete(ctx, "api-0", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CoreV1().Pods("shop").Create(ctx, apiLogPod("api-2"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for !seen["api-2"] {
		select {
		case line := <-lines:
			seen[line.Pod] = true
		case <-ctx.Done():
			t.Fatal("replacement pod never joined stream")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("browser stream did not stop")
	}
	deadline := time.Now().Add(2 * time.Second)
	for active.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if active.Load() != 0 {
		t.Fatalf("%d Kubernetes streams leaked", active.Load())
	}
}
