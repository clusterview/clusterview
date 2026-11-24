package cluster

import (
	"math"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

func healthFixture(now time.Time) (Objects, []metricsv1beta1.PodMetrics) {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "shop"}, Spec: corev1.PodSpec{Containers: []corev1.Container{
		{Name: "app", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi")}}},
		{Name: "sidecar", Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi")}}},
	}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true}, {Name: "sidecar", Ready: true}}, StartTime: &metav1.Time{Time: now.Add(-time.Hour)}, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	m := metricsv1beta1.PodMetrics{ObjectMeta: metav1.ObjectMeta{Name: p.Name, Namespace: p.Namespace}, Timestamp: metav1.Time{Time: now}, Containers: []metricsv1beta1.ContainerMetrics{
		{Name: "app", Usage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("980m"), corev1.ResourceMemory: resource.MustParse("980Mi")}},
		{Name: "sidecar", Usage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("10Mi")}},
	}}
	return Objects{Pods: []*corev1.Pod{p}}, []metricsv1beta1.PodMetrics{m}
}

func TestResourceHealthUsesWorstContainerAndPreservesReadiness(t *testing.T) {
	now := time.Now().UTC()
	objs, metrics := healthFixture(now)
	s := BuildSnapshot(objs, 1, now)
	enrichHealth(&s, objs, metrics, TelemetryData{}, HealthPolicy{}, now)
	h := s.Services[0].Health
	if h == nil || h.State != HealthCritical || h.Memory.Percent == nil || *h.Memory.Percent < 95 || h.Memory.Subject != "api-1/app" {
		t.Fatalf("worst container lost: %+v", h)
	}
	if h.CPU.State != HealthWarning {
		t.Fatalf("high CPU without throttling must only warn: %+v", h.CPU)
	}
	if s.Services[0].Status != StatusHealthy || s.Summary.NeedAttention != 1 {
		t.Fatalf("resource pressure shouldn't change availability: %+v", s.Services[0])
	}
	if len(s.Workloads[0].Pods[0].Resources) != 2 {
		t.Fatal("missing container measurements")
	}
	extra := TelemetryData{Throttling: map[string]TelemetrySample{"shop/api-1/app": {Value: 60, Time: now}}}
	s = BuildSnapshot(objs, 2, now)
	enrichHealth(&s, objs, metrics, extra, HealthPolicy{}, now)
	if s.Services[0].Health.CPU.State != HealthCritical {
		t.Fatal("throttled CPU not escalated")
	}
}

func TestResourceHealthMissingStaleAndReplacedPods(t *testing.T) {
	now := time.Now().UTC()
	for _, mode := range []string{"missing", "stale", "future", "before-pod-start", "before-container-start", "missing-container", "no-limit"} {
		t.Run(mode, func(t *testing.T) {
			objs, metrics := healthFixture(now)
			switch mode {
			case "missing":
				metrics = nil
			case "stale":
				metrics[0].Timestamp.Time = now.Add(-3 * time.Minute)
			case "future":
				metrics[0].Timestamp.Time = now.Add(time.Minute)
			case "before-pod-start":
				objs.Pods[0].Status.StartTime = &metav1.Time{Time: now.Add(time.Second)}
			case "before-container-start":
				objs.Pods[0].Status.ContainerStatuses[0].State.Running = &corev1.ContainerStateRunning{StartedAt: metav1.Time{Time: now.Add(time.Second)}}
			case "missing-container":
				metrics[0].Containers = metrics[0].Containers[:1]
			case "no-limit":
				objs.Pods[0].Spec.Containers[0].Resources.Limits = nil
			}
			// Keep available measurements below warning to isolate incompleteness.
			for i := range metrics {
				for j := range metrics[i].Containers {
					metrics[i].Containers[j].Usage = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("10Mi")}
				}
			}
			s := BuildSnapshot(objs, 1, now)
			enrichHealth(&s, objs, metrics, TelemetryData{}, HealthPolicy{}, now)
			if s.Services[0].Health.State != HealthUnknown {
				t.Fatalf("incomplete evidence became healthy: %+v", s.Services[0].Health)
			}
			if s.Services[0].Health.Memory.Percent != nil {
				t.Fatal("unknown measurement has a misleading percentage")
			}
		})
	}
}

func TestVolumeHealthUsesActualClaimsAndFreshCapacity(t *testing.T) {
	now := time.Now().UTC()
	objs, metrics := healthFixture(now)
	objs.Pods[0].Spec.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "orders-data"}}}}
	extra := TelemetryData{Volumes: map[string]VolumeSample{
		"shop/orders-data":  {Used: TelemetrySample{Value: 96, Time: now}, Capacity: TelemetrySample{Value: 100, Time: now}},
		"other/secret-data": {Used: TelemetrySample{Value: 100, Time: now}, Capacity: TelemetrySample{Value: 100, Time: now}},
	}}
	s := BuildSnapshot(objs, 1, now)
	enrichHealth(&s, objs, metrics, extra, HealthPolicy{}, now)
	disk := s.Services[0].Health.Disk
	if disk == nil || disk.State != HealthCritical || disk.Subject != "shop/orders-data" {
		t.Fatalf("wrong volume: %+v", disk)
	}
	extra.Volumes["shop/orders-data"] = VolumeSample{Used: TelemetrySample{Value: 96, Time: now}, Capacity: TelemetrySample{Value: 100, Time: now.Add(-3 * time.Minute)}}
	s = BuildSnapshot(objs, 2, now)
	enrichHealth(&s, objs, metrics, extra, HealthPolicy{}, now)
	if s.Services[0].Health.Disk.State != HealthUnknown {
		t.Fatal("stale capacity used as a denominator")
	}
}

func TestQueueHealthEvaluatesFlowNotJustDepth(t *testing.T) {
	now := time.Now().UTC()
	stale := now.Add(-3 * time.Minute)
	n := func(v float64) *float64 { return &v }
	base := QueueHealth{QueueTarget: QueueTarget{ServiceID: "Pod/shop/api-1", Job: "rabbitmq", VHost: "/", Queue: "orders", WarningMessages: 1000, CriticalMessages: 10000}, Ready: n(20000), Unacked: n(0), Consumers: n(3), PublishRate: n(100), ProcessRate: n(200), ObservedAt: &now}
	for _, tc := range []struct {
		name string
		edit func(*QueueHealth)
		want HealthState
	}{
		{"large but draining", func(*QueueHealth) {}, HealthHealthy},
		{"growing", func(q *QueueHealth) { q.Ready = n(2000); q.PublishRate = n(300) }, HealthWarning},
		{"large growing", func(q *QueueHealth) { q.PublishRate = n(300) }, HealthCritical},
		{"no consumers", func(q *QueueHealth) { q.Ready = n(2); q.Consumers = n(0) }, HealthCritical},
		{"unacked stuck", func(q *QueueHealth) { q.Ready = n(0); q.Unacked = n(2000); q.ProcessRate = n(0) }, HealthWarning},
		{"missing rate", func(q *QueueHealth) { q.ProcessRate = nil }, HealthUnknown},
		{"stale", func(q *QueueHealth) { q.ObservedAt = &stale }, HealthUnknown},
		{"pull consumer allowed", func(q *QueueHealth) { q.Consumers = n(0); q.AllowNoConsumers = true }, HealthHealthy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := base
			tc.edit(&q)
			got := EvaluateQueue(q, now)
			if got.State != tc.want {
				t.Fatalf("got %s: %s", got.State, got.Reason)
			}
		})
	}
}

func TestHealthHistoryIsBoundedAndImmutable(t *testing.T) {
	c := &Cache{}
	now := time.Now().UTC()
	usage, pct := 1.0, 20.0
	s := Snapshot{Services: []Service{{ID: "Pod/shop/api", Health: &ServiceHealth{CPU: ResourceReading{Usage: &usage, Percent: &pct, ObservedAt: &now, Subject: "api/app"}}}}}
	for i := 0; i < 45; i++ {
		at := now.Add(time.Duration(i) * time.Second)
		s.Services[0].Health.CPU.ObservedAt = &at
		c.recordHealthHistory(&s)
	}
	first := s.Services[0].Health.CPU.History
	if len(first) != 40 {
		t.Fatalf("unbounded history: %d", len(first))
	}
	at := now.Add(time.Minute)
	s.Services[0].Health.CPU.ObservedAt = &at
	c.recordHealthHistory(&s)
	if !first[0].Time.Equal(now.Add(5 * time.Second)) {
		t.Fatal("published history mutated")
	}
}

func TestQueueAssociationStaysWithinDiscoveredServices(t *testing.T) {
	now := time.Now().UTC()
	objs, metrics := healthFixture(now)
	q := QueueHealth{QueueTarget: QueueTarget{ServiceID: "Pod/other/api-1", Queue: "private"}}
	s := BuildSnapshot(objs, 1, now)
	enrichHealth(&s, objs, metrics, TelemetryData{Queues: []QueueHealth{q}}, HealthPolicy{}, now)
	if len(s.Services[0].Health.Queues) > 0 || strings.Contains(s.Services[0].Health.Memory.Reason, "private") {
		t.Fatal("out-of-scope queue leaked")
	}
}

func TestReadingRejectsNonFiniteTelemetry(t *testing.T) {
	r := reading(math.Inf(1), 100, "bytes", "prometheus", "disk", time.Now(), HealthPolicy{85, 95})
	if r.State != HealthUnknown || r.Percent != nil {
		t.Fatal("non-finite reading accepted")
	}
}

func TestThrottlingExcludesPreviousContainerRateWindow(t *testing.T) {
	now := time.Now().UTC()
	objs, metrics := healthFixture(now)
	objs.Pods[0].Status.ContainerStatuses[0].State.Running = &corev1.ContainerStateRunning{StartedAt: metav1.Time{Time: now.Add(-time.Minute)}}
	extra := TelemetryData{Throttling: map[string]TelemetrySample{"shop/api-1/app": {Value: 90, Time: now}}}
	s := BuildSnapshot(objs, 1, now)
	enrichHealth(&s, objs, metrics, extra, HealthPolicy{}, now)
	if s.Services[0].Health.CPU.State == HealthCritical || s.Workloads[0].Pods[0].Resources[0].Throttling != nil {
		t.Fatal("prior container's rate window used for restarted container")
	}
}

func TestCPUUsageCannotProveThrottlingRecovery(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name    string
		samples map[string]TelemetrySample
		want    HealthState
	}{
		{"throttled", map[string]TelemetrySample{"shop/api-1/app": {Value: 60, Time: now}, "shop/api-1/sidecar": {Value: 0, Time: now}}, HealthCritical},
		{"source unavailable", map[string]TelemetrySample{}, HealthUnknown},
		{"container measurement missing", map[string]TelemetrySample{"shop/api-1/sidecar": {Value: 0, Time: now}}, HealthUnknown},
		{"stale throttling", map[string]TelemetrySample{"shop/api-1/app": {Value: 0, Time: now.Add(-3 * time.Minute)}, "shop/api-1/sidecar": {Value: 0, Time: now}}, HealthUnknown},
		{"fresh recovery", map[string]TelemetrySample{"shop/api-1/app": {Value: 0, Time: now}, "shop/api-1/sidecar": {Value: 0, Time: now}}, HealthHealthy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objs, metrics := healthFixture(now)
			for i := range metrics[0].Containers {
				metrics[0].Containers[i].Usage = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("10Mi")}
			}
			s := BuildSnapshot(objs, 1, now)
			enrichHealth(&s, objs, metrics, TelemetryData{Throttling: tc.samples}, HealthPolicy{}, now)
			h := s.Services[0].Health
			if h.Checks["resource-cpu"] != HealthHealthy || h.Checks["resource-cpu-throttling"] != tc.want {
				t.Fatalf("usage and throttling evidence conflated: %+v", h.Checks)
			}
			if h.CPU.State != tc.want {
				t.Fatalf("CPU meter conceals throttling state: got %s, want %s", h.CPU.State, tc.want)
			}
			if tc.want == HealthCritical {
				if len(s.Services[0].Problems) != 1 || s.Services[0].Problems[0].Kind != "resource-cpu-throttling" {
					t.Fatalf("throttling incident has no independent identity: %+v", s.Services[0].Problems)
				}
			} else if len(s.Services[0].Problems) != 0 {
				t.Fatalf("unobserved problem fabricated: %+v", s.Services[0].Problems)
			}
		})
	}
}

func TestQueueCheckKeyIncludesStableBrokerIdentity(t *testing.T) {
	a := QueueTarget{Job: "rabbitmq", VHost: "/", Queue: "orders", BrokerLabels: map[string]string{"service": "events", "cluster": "prod"}}
	b := a
	b.BrokerLabels = map[string]string{"cluster": "prod", "service": "events"}
	if QueueCheckKey(a) != QueueCheckKey(b) {
		t.Fatal("broker label iteration changed identity")
	}
	b.BrokerLabels = map[string]string{"service": "other", "cluster": "prod"}
	if QueueCheckKey(a) == QueueCheckKey(b) {
		t.Fatal("different brokers share a recovery check")
	}
}

func TestCPUMeterIdentifiesThrottledContainerIndependentlyOfUsage(t *testing.T) {
	now := time.Now().UTC()
	objs, metrics := healthFixture(now)
	extra := TelemetryData{Throttling: map[string]TelemetrySample{"shop/api-1/app": {Value: 0, Time: now}, "shop/api-1/sidecar": {Value: 60, Time: now}}}
	s := BuildSnapshot(objs, 1, now)
	enrichHealth(&s, objs, metrics, extra, HealthPolicy{}, now)
	h := s.Services[0].Health
	if h.CPU.Subject != "api-1/sidecar" || h.CPU.Usage == nil || *h.CPU.Usage != 10 || h.CPU.State != HealthCritical {
		t.Fatalf("throttling attributed to a different container: %+v", h.CPU)
	}
	if h.Checks["resource-cpu"] != HealthWarning || h.Checks["resource-cpu-throttling"] != HealthCritical {
		t.Fatalf("independent CPU conditions lost: %+v", h.Checks)
	}
}

func TestResourceWarningDoesNotMaskIncompleteRecoveryEvidence(t *testing.T) {
	now := time.Now().UTC()
	for _, metric := range []string{"cpu-throttling", "memory", "disk"} {
		t.Run(metric, func(t *testing.T) {
			objs, metrics := healthFixture(now)
			for i := range metrics[0].Containers {
				metrics[0].Containers[i].Usage = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("10Mi")}
			}
			extra := TelemetryData{}
			switch metric {
			case "cpu-throttling":
				extra.Throttling = map[string]TelemetrySample{"shop/api-1/app": {Value: 60, Time: now}, "shop/api-1/sidecar": {Value: 30, Time: now}}
			case "memory":
				metrics[0].Containers[0].Usage[corev1.ResourceMemory] = resource.MustParse("980Mi")
				metrics[0].Containers[1].Usage[corev1.ResourceMemory] = resource.MustParse("900Mi")
			case "disk":
				for _, claim := range []string{"a", "b"} {
					objs.Pods[0].Spec.Volumes = append(objs.Pods[0].Spec.Volumes, corev1.Volume{Name: claim, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: claim}}})
				}
				extra.Volumes = map[string]VolumeSample{
					"shop/a": {Used: TelemetrySample{Value: 96, Time: now}, Capacity: TelemetrySample{Value: 100, Time: now}},
					"shop/b": {Used: TelemetrySample{Value: 90, Time: now}, Capacity: TelemetrySample{Value: 100, Time: now}},
				}
			}
			observe := func() (HealthState, HealthState) {
				s := BuildSnapshot(objs, 1, now)
				enrichHealth(&s, objs, metrics, extra, HealthPolicy{}, now)
				h := s.Services[0].Health
				display := h.CPU.State
				if metric == "memory" {
					display = h.Memory.State
				}
				if metric == "disk" {
					display = h.Disk.State
				}
				return display, h.Checks["resource-"+metric]
			}
			if display, check := observe(); display != HealthCritical || check != HealthCritical {
				t.Fatalf("initial critical evidence missing: display %s, recovery %s", display, check)
			}
			app := metrics[0].Containers[0]
			switch metric {
			case "cpu-throttling":
				delete(extra.Throttling, "shop/api-1/app")
			case "memory":
				metrics[0].Containers = metrics[0].Containers[1:]
			case "disk":
				delete(extra.Volumes, "shop/a")
			}
			if display, check := observe(); display != HealthWarning || check != HealthUnknown {
				t.Fatalf("warning hid missing critical evidence: display %s, recovery %s", display, check)
			}
			// A fresh observation below critical permits a critical-only incident to
			// recover, while B's observed warning stays visible.
			switch metric {
			case "cpu-throttling":
				extra.Throttling["shop/api-1/app"] = TelemetrySample{Value: 0, Time: now}
			case "memory":
				app.Usage[corev1.ResourceMemory] = resource.MustParse("10Mi")
				metrics[0].Containers = append([]metricsv1beta1.ContainerMetrics{app}, metrics[0].Containers...)
			case "disk":
				extra.Volumes["shop/a"] = VolumeSample{Used: TelemetrySample{Value: 10, Time: now}, Capacity: TelemetrySample{Value: 100, Time: now}}
			}
			if display, check := observe(); display != HealthWarning || check != HealthWarning {
				t.Fatalf("complete observed improvement lost: display %s, recovery %s", display, check)
			}
		})
	}
}
