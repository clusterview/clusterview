package diagnose

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/clusterview/clusterview/internal/ai"
	"github.com/clusterview/clusterview/internal/cluster"
)

type fakeSource struct {
	snap cluster.Snapshot
	pods map[string]*corev1.Pod
	cs   kubernetes.Interface
}

func (f fakeSource) Snapshot() cluster.Snapshot      { return f.snap }
func (f fakeSource) Clientset() kubernetes.Interface { return f.cs }
func (f fakeSource) Pod(ns, name string) (*corev1.Pod, error) {
	if p, ok := f.pods[ns+"/"+name]; ok {
		return p, nil
	}
	return nil, cluster.ErrNotFound
}

type fakeModel struct {
	asked []ai.Request
	reply string
}

func (m *fakeModel) Name() string { return "fake" }
func (m *fakeModel) JSON(_ context.Context, r ai.Request) ([]byte, error) {
	m.asked = append(m.asked, r)
	return []byte(m.reply), nil
}

// crashingShop is a Deployment whose one pod crash-loops, with a database
// password in its environment.
func crashingShop() fakeSource {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "payments"},
		Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(1)), Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "ghcr.io/acme/payments:3.2"}}},
		}},
	}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Namespace: "shop", Name: "payments-7c9f", UID: "rs-1",
		OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "payments", Controller: ptr(true)}},
	}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "payments-7c9f-x2", OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "payments-7c9f", UID: "rs-1", Controller: ptr(true)}}},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app", Image: "ghcr.io/acme/payments:3.2",
			Env: []corev1.EnvVar{
				{Name: "DB_HOST", Value: "orders-db.data"},
				{Name: "DB_PASSWORD", Value: "hunter2hunter2"},
				{Name: "PROVIDER_KEY", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "payments"}, Key: "provider-key"}}},
			},
		}}},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
			Name: "app", RestartCount: 7,
			State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}},
		}}},
	}
	snap := cluster.BuildSnapshot(cluster.Objects{Deployments: []*appsv1.Deployment{dep}, ReplicaSets: []*appsv1.ReplicaSet{rs}, Pods: []*corev1.Pod{pod}}, 1, time.Now())
	return fakeSource{snap: snap, pods: map[string]*corev1.Pod{"shop/payments-7c9f-x2": pod}, cs: fake.NewClientset(pod)}
}

func ptr[T any](v T) *T { return &v }

func TestFindErrorSendsEvidenceWithoutSecrets(t *testing.T) {
	src := crashingShop()
	model := &fakeModel{reply: `{"summary":"It crashes on start.","cause":"Bad key.","evidence":["CrashLoopBackOff"],"fix":["Fix the key"],"confidence":"high"}`}
	d := New(model, src)

	got, err := d.FindError(context.Background(), "Deployment/shop/payments", false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary != "It crashes on start." || got.Model != "fake" || len(got.Fix) != 1 {
		t.Errorf("unexpected diagnosis: %+v", got)
	}
	sent := model.asked[0].User
	for _, want := range []string{"CrashLoopBackOff", "7 restarts", "exit code 1", `env DB_HOST = "orders-db.data"`, "from Secret payments, key provider-key", "<log>"} {
		if !strings.Contains(sent, want) {
			t.Errorf("evidence lacks %q:\n%s", want, sent)
		}
	}
	if strings.Contains(sent, "hunter2") {
		t.Errorf("a secret value was sent:\n%s", sent)
	}
	if got.Sent == "" || !strings.Contains(sent, got.Sent) {
		t.Errorf("Sent should be exactly the evidence given to the model")
	}

	// Asked again soon: the same answer, no second call.
	if _, err := d.FindError(context.Background(), "Deployment/shop/payments", false); err != nil || len(model.asked) != 1 {
		t.Errorf("a recent answer should be reused: %d calls, %v", len(model.asked), err)
	}
	if _, err := d.FindError(context.Background(), "Deployment/shop/payments", true); err != nil || len(model.asked) != 2 {
		t.Errorf("fresh should ask again: %d calls, %v", len(model.asked), err)
	}
}

func TestFindErrorUnknownService(t *testing.T) {
	d := New(&fakeModel{}, crashingShop())
	if _, err := d.FindError(context.Background(), "Deployment/shop/nope", false); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}
