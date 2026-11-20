package monitoring

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/clusterview/clusterview/internal/cluster"
)

func vector(w http.ResponseWriter, value string, labels map[string]string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": []any{map[string]any{"metric": labels, "value": []any{float64(time.Now().UnixMilli()) / 1000, value}}}}})
}

func TestPrometheusReadAssociatesLabelsAndSeparatesMissingFromZero(t *testing.T) {
	var mu sync.Mutex
	var expressions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prom/api/v1/query" || r.Header.Get("Authorization") != "Bearer private-token" {
			t.Error("missing base path or token")
		}
		expr := r.URL.Query().Get("query")
		mu.Lock()
		expressions = append(expressions, expr)
		mu.Unlock()
		if !strings.Contains(expr, `cluster="production"`) || !strings.Contains(expr, "timestamp(") {
			t.Error("query missing cluster or scrape freshness filter")
		}
		switch {
		case strings.Contains(expr, "kubelet_volume_stats_used_bytes"):
			vector(w, "96", map[string]string{"namespace": "shop", "persistentvolumeclaim": "data"})
		case strings.Contains(expr, "kubelet_volume_stats_capacity_bytes"):
			vector(w, "100", map[string]string{"namespace": "shop", "persistentvolumeclaim": "data"})
		case strings.Contains(expr, "container_cpu_cfs"):
			vector(w, "60", map[string]string{"namespace": "shop", "pod": "api-1", "container": "app"})
		case strings.Contains(expr, "queue_messages_ready"):
			vector(w, "12000", nil)
		case strings.Contains(expr, "queue_messages_unacked"):
			vector(w, "0", nil)
		case strings.Contains(expr, "queue_consumers"):
			vector(w, "0", nil)
		case strings.Contains(expr, "messages_acked_total"):
			vector(w, "0", nil)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	cfg := Config{PrometheusURL: srv.URL + "/prom", MatchLabels: map[string]string{"cluster": "production"}, RabbitMQ: []cluster.QueueTarget{{ServiceID: "Pod/shop/rabbit", Job: "rabbit-job", VHost: "/", Queue: `orders"test`}}}
	c, err := New(cfg, "private-token", []string{"shop"})
	if err != nil {
		t.Fatal(err)
	}
	got := c.Read(context.Background())
	if got.Volumes["shop/data"].Used.Value != 96 || got.Throttling["shop/api-1/app"].Value != 60 {
		t.Fatalf("lost volume or container association: %+v", got)
	}
	q := got.Queues[0]
	if q.Consumers == nil || *q.Consumers != 0 || q.ProcessRate == nil || *q.ProcessRate != 0 || q.PublishRate != nil {
		t.Fatalf("zero conflated with missing: %+v", q)
	}
	if evaluated := cluster.EvaluateQueue(q, time.Now()); evaluated.State != cluster.HealthCritical {
		t.Fatalf("queue failure not detected: %+v", evaluated)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, expr := range expressions {
		if strings.Contains(expr, "kubelet_") || strings.Contains(expr, "container_cpu_") {
			if !strings.Contains(expr, `namespace=~"shop"`) {
				t.Fatal("resource query not namespace-scoped")
			}
		} else if !strings.Contains(expr, `job="rabbit-job"`) || !strings.Contains(expr, `queue="orders\"test"`) {
			t.Fatalf("queue selector not safely quoted: %s", expr)
		}
	}
}

func TestPrometheusDoesNotFollowRedirectsOrLeakCredentials(t *testing.T) {
	contacted := false
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { contacted = true }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer source.Close()
	c, err := New(Config{PrometheusURL: source.URL}, "credential", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.query(context.Background(), "up")
	if err == nil || strings.Contains(err.Error(), source.URL) || strings.Contains(err.Error(), "credential") || contacted {
		t.Fatal("redirect followed or sensitive error exposed")
	}
}

func TestPrometheusRejectsIncompleteAndInvalidSamples(t *testing.T) {
	for _, mode := range []string{"nan", "infinity", "negative", "stale", "partial", "error", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				switch mode {
				case "nan":
					vector(w, "NaN", nil)
				case "infinity":
					vector(w, "+Inf", nil)
				case "negative":
					vector(w, "-1", nil)
				case "stale":
					_, _ = fmt.Fprintf(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[%d,"1"]}]}}`, time.Now().Add(-3*time.Minute).Unix())
				case "partial":
					_, _ = fmt.Fprint(w, `{"status":"success","warnings":["partial response"],"data":{"resultType":"vector","result":[]}}`)
				case "error":
					_, _ = fmt.Fprint(w, `{"status":"error","error":"secret internal details"}`)
				default:
					_, _ = fmt.Fprint(w, `{`)
				}
			}))
			defer srv.Close()
			c, _ := New(Config{PrometheusURL: srv.URL}, "", nil)
			rows, err := c.query(context.Background(), "up")
			if len(rows) > 0 {
				t.Fatalf("invalid sample accepted: %+v", rows)
			}
			if mode == "partial" || mode == "error" || mode == "malformed" {
				if err == nil {
					t.Fatal("incomplete/error response accepted")
				}
			}
		})
	}
}

func TestMonitoringConfigurationValidation(t *testing.T) {
	for _, raw := range []string{`{"resources":{"warningPercent":96,"criticalPercent":95}}`, `{"resources":{"warningPercent":-1}}`, `{"promethusURL":"http://example"}`, `{} {}`} {
		if _, err := Parse(raw); err == nil {
			t.Fatalf("invalid JSON accepted: %s", raw)
		}
	}
	cfg, err := Parse("")
	if err != nil || cfg.Resources.WarningPercent != 85 || cfg.Resources.CriticalPercent != 95 {
		t.Fatal("missing safe defaults")
	}
	for _, cfg := range []Config{
		{PrometheusURL: "http://user:secret@example"},
		{PrometheusURL: "https://example?token=secret"},
		{PrometheusURL: "https://example", MatchLabels: map[string]string{"__name__": "up"}},
		{PrometheusURL: "https://example", RabbitMQ: []cluster.QueueTarget{{ServiceID: "Pod/other/rabbit", Job: "broker", VHost: "/", Queue: "orders"}}},
		{RabbitMQ: []cluster.QueueTarget{{}}},
	} {
		if _, err := New(cfg, "", []string{"shop"}); err == nil {
			t.Fatalf("invalid config accepted: %+v", cfg)
		}
	}
}

func TestAutoAckQueueUsesDeliveryAndGetRates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expr := r.URL.Query().Get("query")
		switch {
		case strings.Contains(expr, "messages_delivered_total"):
			vector(w, "20", nil)
		case strings.Contains(expr, "queue_get_total"):
			vector(w, "3", nil)
		default:
			vector(w, "0", nil)
		}
	}))
	defer srv.Close()
	c, err := New(Config{PrometheusURL: srv.URL, RabbitMQ: []cluster.QueueTarget{{ServiceID: "Pod/shop/rabbit", Job: "broker", VHost: "/", Queue: "orders", AckMode: "auto"}}}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := c.Read(context.Background())
	if got.Queues[0].ProcessRate == nil || *got.Queues[0].ProcessRate != 23 {
		t.Fatalf("auto-ack processing wrong: %+v", got.Queues[0])
	}
}

func TestQueueQueriesIsolateNamespaceAndBrokerBeforeAggregation(t *testing.T) {
	type broker struct {
		namespace, service                 string
		ready, consumers, published, acked float64
	}
	for _, tc := range []struct {
		name       string
		namespaces []string
		targets    []cluster.QueueTarget
		brokers    []broker
		want       []cluster.HealthState
	}{
		{"one watched namespace", []string{"shop"}, []cluster.QueueTarget{{ServiceID: "StatefulSet/shop/events", Job: "rabbitmq", VHost: "/", Queue: "orders"}}, []broker{{"shop", "events", 20, 0, 30, 0}, {"other", "events", 50000, 5, 0, 1000}}, []cluster.HealthState{cluster.HealthCritical}},
		{"all namespaces", nil, []cluster.QueueTarget{{ServiceID: "StatefulSet/shop/events", Job: "rabbitmq", VHost: "/", Queue: "orders"}, {ServiceID: "StatefulSet/other/events", Job: "rabbitmq", VHost: "/", Queue: "orders"}}, []broker{{"shop", "events", 20, 0, 30, 0}, {"other", "events", 50000, 5, 0, 1000}}, []cluster.HealthState{cluster.HealthCritical, cluster.HealthHealthy}},
		{"same namespace brokers", []string{"shop"}, []cluster.QueueTarget{{ServiceID: "StatefulSet/shop/events", Job: "rabbitmq", VHost: "/", Queue: "orders", BrokerLabels: map[string]string{"service": `events"a`}}, {ServiceID: "StatefulSet/shop/other", Job: "rabbitmq", VHost: "/", Queue: "orders", BrokerLabels: map[string]string{"service": "events-b"}}}, []broker{{"shop", `events"a`, 20, 0, 30, 0}, {"shop", "events-b", 50000, 5, 0, 1000}}, []cluster.HealthState{cluster.HealthCritical, cluster.HealthHealthy}},
		{"namespace label absent", []string{"shop"}, []cluster.QueueTarget{{ServiceID: "StatefulSet/shop/events", Job: "rabbitmq", VHost: "/", Queue: "orders"}}, []broker{{"", "events", 20, 0, 30, 0}}, []cluster.HealthState{cluster.HealthUnknown}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				expr := r.URL.Query().Get("query")
				if !strings.Contains(expr, "rabbitmq_detailed_") {
					vector(w, "0", nil)
					return
				}
				// Emulate aggregation over matching raw broker series. Missing selectors
				// deliberately admit foreign series, reproducing the contamination bug.
				var sum float64
				matches := 0
				for _, b := range tc.brokers {
					if strings.Contains(expr, "namespace=") && !strings.Contains(expr, "namespace="+strconv.Quote(b.namespace)) {
						continue
					}
					if strings.Contains(expr, "service=") && !strings.Contains(expr, "service="+strconv.Quote(b.service)) {
						continue
					}
					matches++
					switch {
					case strings.Contains(expr, "queue_messages_ready"):
						sum += b.ready
					case strings.Contains(expr, "queue_consumers"):
						sum += b.consumers
					case strings.Contains(expr, "messages_published_total"):
						sum += b.published
					case strings.Contains(expr, "messages_acked_total"):
						sum += b.acked
					}
				}
				if matches == 0 {
					_, _ = fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[]}}`)
					return
				}
				vector(w, strconv.FormatFloat(sum, 'f', -1, 64), nil)
			}))
			defer srv.Close()
			client, err := New(Config{PrometheusURL: srv.URL, RabbitMQ: tc.targets}, "", tc.namespaces)
			if err != nil {
				t.Fatal(err)
			}
			got := client.Read(context.Background())
			for i, want := range tc.want {
				q := cluster.EvaluateQueue(got.Queues[i], time.Now())
				if q.State != want {
					t.Fatalf("target %s contaminated: got %s, want %s; %+v", q.ServiceID, q.State, want, q)
				}
				if want == cluster.HealthCritical && (q.Ready == nil || *q.Ready != 20 || q.Consumers == nil || *q.Consumers != 0 || q.PublishRate == nil || *q.PublishRate != 30 || q.ProcessRate == nil || *q.ProcessRate != 0) {
					t.Fatalf("foreign broker measurements included: %+v", q)
				}
			}
		})
	}
}

func TestBrokerLabelsCannotOverrideTargetOrClusterScope(t *testing.T) {
	for _, labels := range []map[string]string{
		{"namespace": "other"}, {"job": "other"}, {"vhost": "other"}, {"queue": "other"}, {"__name__": "up"}, {"bad-label": "bad"}, {"cluster": "other"},
	} {
		cfg := Config{PrometheusURL: "http://prometheus", MatchLabels: map[string]string{"cluster": "prod"}, RabbitMQ: []cluster.QueueTarget{{ServiceID: "StatefulSet/shop/events", Job: "rabbitmq", VHost: "/", Queue: "orders", BrokerLabels: labels}}}
		if _, err := New(cfg, "", []string{"shop"}); err == nil {
			t.Fatalf("unsafe broker labels accepted: %+v", labels)
		}
	}
}
