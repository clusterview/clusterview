package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/clusterview/clusterview/internal/kube"
)

func logPod(name string, containers ...string) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: types.UID(name)}}
	for _, name := range containers {
		p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: name})
	}
	return p
}

func TestServiceLogSources(t *testing.T) {
	p := logPod("api-b", "app", "proxy")
	p.Spec.InitContainers = []corev1.Container{{Name: "migrate"}}
	got, err := ServiceLogSources([]*corev1.Pod{p, logPod("api-a", "app")}, "")
	if err != nil || len(got.Sources) != 4 || got.Sources[0].Pod != "api-a" || strings.Join(got.Containers, ",") != "app,migrate,proxy" {
		t.Fatalf("sources: %+v, %v", got, err)
	}
	selected, err := ServiceLogSources([]*corev1.Pod{p, logPod("api-a", "app")}, "proxy")
	if err != nil || len(selected.Sources) != 1 || selected.Sources[0].Container != "proxy" {
		t.Fatalf("container filter: %+v, %v", selected, err)
	}
	if _, err := ServiceLogSources([]*corev1.Pod{p}, "absent"); err == nil {
		t.Fatal("unknown container must be rejected")
	}
	data, _ := json.Marshal(got)
	if strings.Contains(string(data), "spec") || strings.Contains(string(data), "uid") {
		t.Fatal("internal pod objects must never be serialized")
	}
	var pods []*corev1.Pod
	for i := range MaxServiceLogSources + 5 {
		pods = append(pods, logPod(fmt.Sprintf("api-%02d", i), "app"))
	}
	limited, err := ServiceLogSources(pods, "")
	if err != nil || len(limited.Sources) != MaxServiceLogSources || limited.TotalSources != MaxServiceLogSources+5 || len(limited.Notices) != 1 {
		t.Fatalf("source limit must be explicit: %+v, %v", limited, err)
	}
}

func TestServiceLogSourceTracksRestarts(t *testing.T) {
	p := logPod("api", "app")
	first, _ := ServiceLogSources([]*corev1.Pod{p}, "")
	p = p.DeepCopy()
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", RestartCount: 1, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.Now()}}}}
	next, _ := ServiceLogSources([]*corev1.Pod{p}, "")
	if first.Sources[0].Key() == next.Sources[0].Key() {
		t.Fatal("container restart must replace its stream")
	}
	p.Status.ContainerStatuses[0].State.Terminated = &corev1.ContainerStateTerminated{StartedAt: p.Status.ContainerStatuses[0].State.Running.StartedAt}
	p.Status.ContainerStatuses[0].State.Running = nil
	finished, _ := ServiceLogSources([]*corev1.Pod{p}, "")
	if next.Sources[0].Key() != finished.Sources[0].Key() {
		t.Fatal("termination must not replay the same container's history")
	}
}

func TestReadServiceLogsOrdersAndKeepsPartialResults(t *testing.T) {
	var active, maxActive atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := maxActive.Load()
			if n <= old || maxActive.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		if r.URL.Query().Get("timestamps") != "true" || r.URL.Query().Get("limitBytes") == "" {
			t.Error("bounded, timestamped Kubernetes request required")
		}
		if strings.Contains(r.URL.Path, "missing") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, "container has not started")
			return
		}
		stamp := "2026-10-29T12:00:02Z"
		if strings.Contains(r.URL.Path, "api-a") {
			stamp = "2026-10-29T12:00:01Z"
		}
		_, _ = fmt.Fprintf(w, "%s hello\n", stamp)
	}))
	defer srv.Close()
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL, QPS: 100, Burst: 100})
	if err != nil {
		t.Fatal(err)
	}
	pods := []*corev1.Pod{logPod("api-b", "app"), logPod("api-a", "app"), logPod("missing", "app"), logPod("other", "app", "proxy", "sidecar")}
	got, err := ReadServiceLogs(t.Context(), cs, pods, LogRequest{TailLines: 100})
	if err != nil || len(got.Lines) != 5 || got.Lines[0].Pod != "api-a" || len(got.Notices) != 1 {
		t.Fatalf("merged logs: %+v, %v", got, err)
	}
	if maxActive.Load() > 4 {
		t.Errorf("too many simultaneous requests: %d", maxActive.Load())
	}
	if !strings.Contains(got.Notices[0], "missing/app") {
		t.Error("partial failure must identify its source")
	}
}

func TestServicePodsRespectScope(t *testing.T) {
	c := NewCache(&kube.Client{Clientset: fake.NewClientset()}, Options{Namespaces: []string{"shop"}}, slog.New(slog.DiscardHandler))
	p := logPod("api", "app")
	if err := c.scopes[0].factory.Core().V1().Pods().Informer().GetStore().Add(p); err != nil {
		t.Fatal(err)
	}
	pods, err := c.ServicePods("Pod/shop/api")
	if err != nil || len(pods) != 1 || pods[0].Name != "api" {
		t.Fatalf("scoped pod: %v, %v", pods, err)
	}
	if _, err := c.ServicePods("Pod/other/api"); err == nil {
		t.Fatal("out-of-scope service must not resolve")
	}
}

func TestFollowSourceLogsCancellation(t *testing.T) {
	opened := make(chan struct{})
	closed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(closed)
		_, _ = fmt.Fprint(w, "2026-10-29T12:00:00Z ready\n")
		w.(http.Flusher).Flush()
		close(opened)
		<-r.Context().Done()
	}))
	defer srv.Close()
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	sources, _ := ServiceLogSources([]*corev1.Pod{logPod("api", "app")}, "")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- FollowSourceLogs(ctx, cs, sources.Sources[0], LogRequest{TailLines: 5}, func(line ServiceLogLine) error {
			if line.Pod != "api" || line.Container != "app" || line.Text != "ready" {
				t.Errorf("untagged line: %+v", line)
			}
			return nil
		})
	}()
	<-opened
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("unexpected stream error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("log worker did not stop after cancellation")
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Kubernetes stream stayed open")
	}
}
