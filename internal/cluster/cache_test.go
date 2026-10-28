package cluster

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"

	"github.com/clusterview/clusterview/internal/kube"
)

func TestCacheSeesChangesLive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := fake.NewClientset(deployment("shop", "api", 1, 1))
	c := NewCache(&kube.Client{Clientset: cs, Metrics: metricsfake.NewSimpleClientset()}, Options{Nodes: true}, slog.New(slog.DiscardHandler))
	if !c.Start(ctx) {
		t.Fatal("cache never synced")
	}
	if len(c.Snapshot().Workloads) != 1 {
		t.Fatalf("initial snapshot: %+v", c.Snapshot())
	}

	updates, stop := c.Subscribe()
	defer stop()
	start := time.Now()
	_, err := cs.AppsV1().Deployments("shop").Create(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "worker"},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case v := <-updates:
			s := c.Snapshot()
			if s.Version == v && len(s.Workloads) == 2 {
				t.Logf("change seen after %v", time.Since(start))
				return
			}
		case <-deadline:
			t.Fatalf("new deployment not seen within 2s: %+v", c.Snapshot().Workloads)
		}
	}
}

func TestCacheScopedToNamespaces(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs := fake.NewClientset(deployment("shop", "api", 1, 1), deployment("ops", "backup", 1, 1), deployment("other", "x", 1, 1))
	c := NewCache(&kube.Client{Clientset: cs, Metrics: metricsfake.NewSimpleClientset()},
		Options{Namespaces: []string{"shop", "ops"}}, slog.New(slog.DiscardHandler))
	if !c.Start(ctx) {
		t.Fatal("cache never synced")
	}
	got := map[string]bool{}
	for _, w := range c.Snapshot().Workloads {
		got[w.ID] = true
	}
	if len(got) != 2 || !got["Deployment/shop/api"] || !got["Deployment/ops/backup"] {
		t.Errorf("workloads: %v", got)
	}
	if c.NodesEnabled() {
		t.Error("nodes weren't asked for")
	}
	if _, err := c.Pod("other", "anything"); err == nil {
		t.Error("a pod outside the scope can't be found")
	}
}

func TestHealthAndDashboardSnapshotsStaySeparate(t *testing.T) {
	for _, first := range []string{"health", "dashboard"} {
		t.Run(first+" first", func(t *testing.T) {
			c := NewCache(&kube.Client{Clientset: fake.NewClientset()}, Options{Namespaces: []string{"shop"}}, slog.New(slog.DiscardHandler))
			// Populate the scoped informer store without starting background
			// watches, so cache versions change only when this test asks.
			if err := c.scopes[0].factory.Apps().V1().Deployments().Informer().GetStore().Add(deployment("shop", "api", 1, 0)); err != nil {
				t.Fatal(err)
			}
			c.SetGuesser(&fakeGuesser{known: map[string]Guess{
				"acme/api": {Description: "Guessed API description", Category: CategoryDatabase},
			}})
			if first == "health" {
				c.HealthSnapshot()
			} else {
				c.Snapshot()
			}
			check := func() {
				health, dashboard := c.HealthSnapshot(), c.Snapshot()
				if len(health.Services) != 1 || len(dashboard.Services) != 1 {
					t.Error("both snapshots must include the scoped service")
					return
				}
				h, d := health.Services[0], dashboard.Services[0]
				if h.DescriptionSource == "ai" || h.Category != CategoryApp {
					t.Errorf("dashboard enrichment leaked into health snapshot: %+v", h)
				}
				if d.DescriptionSource != "ai" || d.Description != "Guessed API description" || d.Category != CategoryDatabase {
					t.Errorf("dashboard descriptions lost: %+v", d)
				}
				if h.Status != d.Status || h.ID != d.ID || h.Ready != d.Ready || h.Desired != d.Desired {
					t.Error("health and identity must match in both snapshots")
				}
			}
			check()
			c.version.Add(1) // Both cached views must rebuild after a change.
			var readers sync.WaitGroup
			for range 8 {
				readers.Go(check)
			}
			readers.Wait()
			check()
		})
	}
}
