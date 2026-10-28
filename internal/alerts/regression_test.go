package alerts

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"

	"github.com/clusterview/clusterview/internal/cluster"
	"github.com/clusterview/clusterview/internal/kube"
)

func TestRetryCooldownsStartAtDeliveryCompletion(t *testing.T) {
	for _, status := range []int{0, http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			m, src := setup(t, &receiver{})
			s := service(cluster.StatusDown)
			src.services = []cluster.Service{s}
			// Earlier sequential deliveries have consumed 16 seconds since
			// this health snapshot was observed.
			observedAt := time.Now().Add(-16 * time.Second)
			i := &incident{since: observedAt.Add(-m.cfg.For)}
			m.incidents[s.ID] = i
			var completedAt time.Time
			m.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				completedAt = time.Now()
				if status == 0 {
					return nil, errors.New("delivery failed")
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": {"15"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
			})
			m.check(context.Background(), observedAt)
			if completedAt.IsZero() {
				t.Fatal("expected a delivery attempt")
			}
			want := completedAt.Add(15 * time.Second)
			if i.nextAttempt.Before(want) {
				t.Errorf("incident retry starts before delivery completes: got %s, want at least %s", i.nextAttempt, want)
			}
			if status == http.StatusTooManyRequests && m.notBefore.Before(want) {
				t.Errorf("destination cooldown starts before delivery completes: got %s, want at least %s", m.notBefore, want)
			}
		})
	}
}

func TestSequentialDeliveriesRespectResponseBasedCooldown(t *testing.T) {
	m, src := setup(t, &receiver{})
	for _, name := range []string{"first", "second", "third", "fourth"} {
		s := service(cluster.StatusDown)
		s.ID, s.Name = "Deployment/shop/"+name, name
		src.services = append(src.services, s)
	}
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	ctx := context.Background()
	m.check(ctx, now)
	now = now.Add(m.cfg.For)
	observedAt := now
	attempts := 0
	m.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		status := http.StatusNoContent
		if attempts <= 2 {
			now = now.Add(8 * time.Second)
		} else if attempts == 3 {
			status = http.StatusTooManyRequests
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": {"15"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	m.check(ctx, observedAt)
	want := now.Add(15 * time.Second)
	if !m.notBefore.Equal(want) || !m.incidents["Deployment/shop/third"].nextAttempt.Equal(want) {
		t.Fatalf("both cooldowns must start after the third response, got destination=%s incident=%s, want %s", m.notBefore, m.incidents["Deployment/shop/third"].nextAttempt, want)
	}
	m.check(ctx, now) // The ticker may already be waiting after slow deliveries.
	now = want.Add(-time.Nanosecond)
	m.check(ctx, now)
	if attempts != 3 {
		t.Fatalf("sent %d requests before the response-based cooldown expired, want 3", attempts)
	}
	now = want
	m.check(ctx, now)
	if attempts != 5 {
		t.Fatalf("pending services must resume when the cooldown expires, got %d attempts", attempts)
	}
}

type countingGuesser struct{ requests atomic.Int32 }

func (*countingGuesser) Guess(string) (cluster.Guess, bool) { return cluster.Guess{}, false }
func (g *countingGuesser) Want([]cluster.ImageHint)         { g.requests.Add(1) }

func TestAlertPollingDoesNotQueueAIDescriptions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	replicas := int32(1)
	cs := fake.NewClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "orders"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "orders", Image: "ghcr.io/acme/orders:1"}}}},
		},
	})
	log := slog.New(slog.DiscardHandler)
	cache := cluster.NewCache(&kube.Client{Clientset: cs, Metrics: metricsfake.NewSimpleClientset()}, cluster.Options{Namespaces: []string{"shop"}}, log)
	g := &countingGuesser{}
	cache.SetGuesser(g)
	if !cache.Start(ctx) {
		t.Fatal("cache never synced")
	}
	m, err := New(Config{WebhookURL: "http://localhost/unused", For: 5 * time.Minute}, cache, log)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	m.check(ctx, now)
	m.check(ctx, now.Add(time.Minute))
	if len(m.incidents) != 1 {
		t.Fatal("polling must still observe the failing service")
	}
	if got := g.requests.Load(); got != 0 {
		t.Fatalf("alert polling queued %d AI description requests", got)
	}
	// An actual dashboard read must retain its existing description behavior,
	// including when alert polling populated a cache first.
	cache.Snapshot()
	if g.requests.Load() == 0 {
		t.Fatal("dashboard snapshot no longer queues descriptions")
	}
}
