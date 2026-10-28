package actions

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

var ctx = context.Background()

func newRunner(cfg Config, objs ...runtime.Object) (*Runner, *fake.Clientset) {
	cs := fake.NewClientset(objs...)
	r := NewRunner(cfg, cs, nil, slog.New(slog.DiscardHandler))
	r.now = func() time.Time { return time.Date(2026, 10, 25, 9, 30, 0, 0, time.UTC) }
	return r, cs
}

func owned(kind, name string) []metav1.OwnerReference {
	return []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: kind, Name: name, UID: "owner-uid", Controller: ptr(true)}}
}

func on() Config { return Config{Enabled: true} }

func TestLimits(t *testing.T) {
	r, _ := newRunner(Config{})
	if _, err := r.RestartPod(ctx, "ann", "shop", "web-1"); !errors.Is(err, ErrDisabled) {
		t.Errorf("disabled: %v", err)
	}
	r, _ = newRunner(Config{Enabled: true, Namespaces: []string{"shop"}})
	if _, err := r.RestartPod(ctx, "ann", "kube-system", "coredns-1"); !errors.Is(err, ErrNamespace) {
		t.Errorf("namespace: %v", err)
	}
	if len(r.Audit().List()) != 0 {
		t.Error("nothing was attempted, so nothing should be audited")
	}
	if _, err := r.RestartPod(ctx, "ann", "shop", "gone"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing pod: %v", err)
	}
}

func TestRestartPod(t *testing.T) {
	pod := func(name string, owners []metav1.OwnerReference) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: name, UID: types.UID("uid-" + name), OwnerReferences: owners}}
	}
	r, cs := newRunner(on(),
		pod("web-1", owned("ReplicaSet", "web-5d8f")),
		pod("loner", nil),
		pod("kube-apiserver-n1", []metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: "n1", Controller: ptr(true)}}),
	)

	e, err := r.RestartPod(ctx, "ann@example.com", "shop", "web-1")
	if err != nil || !e.OK || e.Target != "Pod/shop/web-1" || e.Actor != "ann@example.com" {
		t.Fatalf("restart: %+v %v", e, err)
	}
	if _, err := cs.CoreV1().Pods("shop").Get(ctx, "web-1", metav1.GetOptions{}); err == nil {
		t.Error("pod should be deleted")
	}
	evs, _ := cs.CoreV1().Events("shop").List(ctx, metav1.ListOptions{})
	if len(evs.Items) != 1 || evs.Items[0].InvolvedObject.Kind != "ReplicaSet" || !strings.Contains(evs.Items[0].Message, "by ann@example.com via clusterview") {
		t.Errorf("event on the owner: %+v", evs.Items)
	}

	var refused Refused
	if _, err := r.RestartPod(ctx, "ann", "shop", "loner"); !errors.As(err, &refused) {
		t.Errorf("bare pod: %v", err)
	}
	if _, err := r.RestartPod(ctx, "ann", "shop", "kube-apiserver-n1"); !errors.As(err, &refused) || !strings.Contains(refused.Reason, "static pod") {
		t.Errorf("static pod: %v", err)
	}
	if got := r.Audit().List(); len(got) != 3 || got[0].OK || !got[2].OK {
		t.Errorf("audit (newest first, refusals included): %+v", got)
	}
}

func TestRolloutRestart(t *testing.T) {
	paused := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "paused"}, Spec: appsv1.DeploymentSpec{Paused: true}}
	r, cs := newRunner(on(),
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api"}},
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "db"}},
		paused,
	)
	if _, err := r.RolloutRestart(ctx, "ann", "Deployment", "shop", "api"); err != nil {
		t.Fatal(err)
	}
	d, _ := cs.AppsV1().Deployments("shop").Get(ctx, "api", metav1.GetOptions{})
	a := d.Spec.Template.Annotations
	if a["kubectl.kubernetes.io/restartedAt"] != "2026-10-25T09:30:00Z" || a["clusterview.io/restarted-by"] != "ann" {
		t.Errorf("annotations: %v", a)
	}
	if _, err := r.RolloutRestart(ctx, "ann", "StatefulSet", "shop", "db"); err != nil {
		t.Errorf("statefulset: %v", err)
	}
	var refused Refused
	if _, err := r.RolloutRestart(ctx, "ann", "Deployment", "shop", "paused"); !errors.As(err, &refused) {
		t.Errorf("paused: %v", err)
	}
	if _, err := r.RolloutRestart(ctx, "ann", "Job", "shop", "x"); !errors.As(err, &refused) {
		t.Errorf("job: %v", err)
	}
}

func TestScale(t *testing.T) {
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "worker"},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: "Deployment", Name: "worker"},
			MinReplicas:    ptr(int32(2)), MaxReplicas: 10,
		},
	}
	r, cs := newRunner(Config{Enabled: true, MaxReplicas: 5},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api"}, Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(2))}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "worker"}},
		hpa,
	)
	e, err := r.Scale(ctx, "ann", "Deployment", "shop", "api", 4)
	if err != nil || e.Detail != "from 2 to 4 replicas" {
		t.Fatalf("scale: %+v %v", e, err)
	}
	if d, _ := cs.AppsV1().Deployments("shop").Get(ctx, "api", metav1.GetOptions{}); *d.Spec.Replicas != 4 {
		t.Errorf("replicas %d", *d.Spec.Replicas)
	}
	var refused Refused
	if _, err := r.Scale(ctx, "ann", "Deployment", "shop", "api", 6); !errors.As(err, &refused) {
		t.Errorf("over the limit: %v", err)
	}
	if _, err := r.Scale(ctx, "ann", "Deployment", "shop", "worker", 3); !errors.As(err, &refused) || !strings.Contains(refused.Reason, "between 2 and 10") {
		t.Errorf("autoscaled: %v", err)
	}
	if _, err := r.Scale(ctx, "ann", "DaemonSet", "shop", "x", 1); !errors.As(err, &refused) {
		t.Errorf("daemonset: %v", err)
	}
}

func TestRunCronJob(t *testing.T) {
	cj := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ops", Name: "backup", UID: "cj-uid"},
		Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"team": "data"}},
			Spec:       batchv1.JobSpec{BackoffLimit: ptr(int32(2))},
		}},
	}
	r, cs := newRunner(on(), cj)
	e, err := r.RunCronJob(ctx, "ann", "ops", "backup")
	if err != nil {
		t.Fatal(err)
	}
	jobs, _ := cs.BatchV1().Jobs("ops").List(ctx, metav1.ListOptions{})
	if len(jobs.Items) != 1 {
		t.Fatalf("jobs: %+v", jobs.Items)
	}
	j := jobs.Items[0]
	if !strings.HasPrefix(j.Name, "backup-manual-") || e.Detail != "started Job "+j.Name {
		t.Errorf("name %q, detail %q", j.Name, e.Detail)
	}
	if ref := metav1.GetControllerOf(&j); ref == nil || ref.Kind != "CronJob" || ref.UID != "cj-uid" {
		t.Errorf("owner: %+v", j.OwnerReferences)
	}
	if j.Labels["team"] != "data" || j.Annotations["cronjob.kubernetes.io/instantiate"] != "manual" || *j.Spec.BackoffLimit != 2 {
		t.Errorf("job: %+v", j)
	}
}

func TestManualJobName(t *testing.T) {
	long := strings.Repeat("a", 70)
	if n := manualJobName(long, time.Unix(1792900000, 0)); len(n) != 63 || !strings.Contains(n, "-manual-") {
		t.Errorf("%q (%d)", n, len(n))
	}
}

func TestAuditKeepsTheNewest(t *testing.T) {
	a := NewAudit(fake.NewClientset(), slog.New(slog.DiscardHandler))
	for i := range auditSize + 5 {
		a.record(ctx, Entry{Detail: string(rune('a' + i%26)), OK: false}, corev1.ObjectReference{})
	}
	if got := a.List(); len(got) != auditSize {
		t.Errorf("kept %d", len(got))
	}
}
