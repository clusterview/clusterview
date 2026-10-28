package cluster

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var now = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func controller(kind, name string, uid types.UID) []metav1.OwnerReference {
	return []metav1.OwnerReference{{Kind: kind, Name: name, UID: uid, Controller: ptr(true)}}
}

func deployment(ns, name string, replicas, ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr(replicas),
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "ghcr.io/acme/api:1.4.2"}}}},
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: ready, AvailableReplicas: ready, UpdatedReplicas: replicas},
	}
}

func replicaSet(ns, name, deploy string, uid types.UID) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Namespace: ns, Name: name, UID: uid, OwnerReferences: controller("Deployment", deploy, "d-"+uid),
	}}
}

type podOpt func(*corev1.Pod)

func pod(ns, name string, owner []metav1.OwnerReference, opts ...podOpt) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, OwnerReferences: owner},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		Status: corev1.PodStatus{
			Phase:             corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true}},
		},
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

func waiting(reason string, restarts int32) podOpt {
	return func(p *corev1.Pod) {
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name: "app", RestartCount: restarts,
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}},
		}}
	}
}

func find(t *testing.T, s Snapshot, id string) Workload {
	t.Helper()
	for _, w := range s.Workloads {
		if w.ID == id {
			return w
		}
	}
	t.Fatalf("workload %s not in snapshot", id)
	return Workload{}
}

func TestReplicaStatus(t *testing.T) {
	cases := []struct {
		desired, ready int
		want           Status
	}{
		{0, 0, StatusIdle},
		{3, 3, StatusHealthy},
		{3, 4, StatusHealthy},
		{3, 1, StatusDegraded},
		{3, 0, StatusDown},
	}
	for _, c := range cases {
		if got := replicaStatus(c.desired, c.ready); got != c.want {
			t.Errorf("replicaStatus(%d, %d) = %s, want %s", c.desired, c.ready, got, c.want)
		}
	}
}

func TestPodsFollowOwnerChain(t *testing.T) {
	objs := Objects{
		Deployments: []*appsv1.Deployment{deployment("shop", "api", 2, 1)},
		ReplicaSets: []*appsv1.ReplicaSet{replicaSet("shop", "api-7d9c8b6f5", "api", "rs1")},
		StatefulSets: []*appsv1.StatefulSet{{
			ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "rabbitmq"},
			Spec:       appsv1.StatefulSetSpec{Replicas: ptr(int32(1))},
			Status:     appsv1.StatefulSetStatus{ReadyReplicas: 1},
		}},
		Pods: []*corev1.Pod{
			pod("shop", "api-7d9c8b6f5-aaaaa", controller("ReplicaSet", "api-7d9c8b6f5", "rs1")),
			pod("shop", "api-7d9c8b6f5-bbbbb", controller("ReplicaSet", "api-7d9c8b6f5", "rs1"), waiting("CrashLoopBackOff", 7)),
			pod("shop", "rabbitmq-server-0", controller("StatefulSet", "rabbitmq", "ss1")),
			pod("shop", "loner", nil),
		},
	}
	s := BuildSnapshot(objs, 3, now)

	api := find(t, s, "Deployment/shop/api")
	if api.Status != StatusDegraded || api.Desired != 2 || api.Ready != 1 || len(api.Pods) != 2 {
		t.Fatalf("api: %+v", api)
	}
	crash := api.Pods[1]
	if crash.Reason != "CrashLoopBackOff" || !crash.Stuck || crash.Restarts != 7 || crash.ReadyContainers != "0/1" {
		t.Errorf("crashing pod: %+v", crash)
	}
	if api.ImageTag != "1.4.2" {
		t.Errorf("image tag %q", api.ImageTag)
	}
	rmq := find(t, s, "StatefulSet/shop/rabbitmq")
	if rmq.Status != StatusHealthy || len(rmq.Pods) != 1 {
		t.Errorf("rabbitmq: %+v", rmq)
	}
	// The pod nothing owns is a workload of its own.
	if loner := find(t, s, "Pod/shop/loner"); loner.Status != StatusHealthy {
		t.Errorf("loner: %+v", loner)
	}
	if s.Summary.Pods != 4 || s.Summary.NeedAttention != 1 || s.Summary.Restarts != 7 || s.Summary.Workloads != 3 || s.Summary.Services != 3 {
		t.Errorf("summary: %+v", s.Summary)
	}
	if s.Version != 3 {
		t.Errorf("version %d", s.Version)
	}
}

func TestTerminatingPodsLeaveTheWorkload(t *testing.T) {
	gone := pod("a", "web-1", controller("DaemonSet", "web", "x"))
	gone.DeletionTimestamp = &metav1.Time{Time: now}
	s := BuildSnapshot(Objects{
		DaemonSets: []*appsv1.DaemonSet{{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "web"},
			Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 1, NumberReady: 1}}},
		Pods: []*corev1.Pod{gone},
	}, 1, now)
	if w := find(t, s, "DaemonSet/a/web"); len(w.Pods) != 0 || s.Summary.Pods != 0 {
		t.Fatalf("terminating pod counted: %+v", w)
	}
}

func TestPodReasons(t *testing.T) {
	initFailing := pod("a", "p", nil, func(p *corev1.Pod) {
		p.Status.Phase = corev1.PodPending
		p.Status.InitContainerStatuses = []corev1.ContainerStatus{{
			Name:  "migrate",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		}}
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name:  "app",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}},
		}}
	})
	unschedulable := pod("a", "p", nil, func(p *corev1.Pod) {
		p.Status.Phase = corev1.PodPending
		p.Status.ContainerStatuses = nil
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable"}}
	})
	oom := pod("a", "p", nil, func(p *corev1.Pod) {
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name:  "app",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled"}},
		}}
	})
	completed := pod("a", "p", nil, func(p *corev1.Pod) {
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name:  "app",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Completed"}},
		}}
	})
	creating := pod("a", "p", nil, waiting("ContainerCreating", 0))

	cases := []struct {
		name   string
		pod    *corev1.Pod
		reason string
		stuck  bool
	}{
		{"failing init container wins", initFailing, "Init:CrashLoopBackOff", true},
		{"pending pod uses its condition", unschedulable, "Unschedulable", false},
		{"OOM kill", oom, "OOMKilled", true},
		{"Completed isn't a problem", completed, "", false},
		{"still starting", creating, "ContainerCreating", false},
	}
	for _, c := range cases {
		s := podState(c.pod)
		if s.Reason != c.reason || s.Stuck != c.stuck {
			t.Errorf("%s: reason %q stuck %v", c.name, s.Reason, s.Stuck)
		}
	}
	if s := podState(unschedulable); s.Ready || s.ReadyContainers != "0/0" {
		t.Errorf("a pod without containers isn't ready: %+v", s)
	}
}

func TestAutoscaling(t *testing.T) {
	hpa := func(name string, labels map[string]string, kind, target string) *autoscalingv2.HorizontalPodAutoscaler {
		return &autoscalingv2.HorizontalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: name, Labels: labels},
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
				ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{Kind: kind, Name: target},
				MinReplicas:    ptr(int32(2)), MaxReplicas: 10,
			},
			Status: autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 3},
		}
	}
	hpas := []*autoscalingv2.HorizontalPodAutoscaler{
		hpa("keda-hpa-worker", nil, "Deployment", "worker"),
		hpa("api", nil, "Deployment", "api"),
		hpa("labelled", map[string]string{"scaledobject.keda.sh/name": "x"}, "StatefulSet", "db"),
	}
	if a := autoscalingFor("Deployment", "a", "worker", hpas); a == nil || a.ManagedBy != "keda" || a.Min != 2 || a.Max != 10 || a.Current != 3 {
		t.Errorf("worker: %+v", a)
	}
	if a := autoscalingFor("Deployment", "a", "api", hpas); a == nil || a.ManagedBy != "hpa" {
		t.Errorf("api: %+v", a)
	}
	if a := autoscalingFor("StatefulSet", "a", "db", hpas); a == nil || a.ManagedBy != "keda" {
		t.Errorf("db: %+v", a)
	}
	if a := autoscalingFor("Deployment", "other-ns", "api", hpas); a != nil {
		t.Errorf("matched across namespaces: %+v", a)
	}
}

func TestJobs(t *testing.T) {
	job := func(name string, st batchv1.JobStatus, owner []metav1.OwnerReference) *batchv1.Job {
		return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: name, UID: types.UID(name), OwnerReferences: owner}, Status: st}
	}
	jobPod := func(name string, opts ...podOpt) *corev1.Pod {
		return pod("a", name+"-x", controller("Job", name, types.UID(name)), opts...)
	}
	objs := Objects{
		Jobs: []*batchv1.Job{
			job("done", batchv1.JobStatus{Succeeded: 1}, nil),
			job("running", batchv1.JobStatus{Active: 1}, nil),
			job("bad-image", batchv1.JobStatus{Active: 1}, nil),
			job("retrying", batchv1.JobStatus{Active: 1, Failed: 2}, nil),
			job("failed", batchv1.JobStatus{Failed: 6}, nil),
			job("nightly-29123", batchv1.JobStatus{Succeeded: 1}, controller("CronJob", "nightly", "cj")),
		},
		Pods: []*corev1.Pod{
			jobPod("running"),
			jobPod("bad-image", waiting("ImagePullBackOff", 0)),
			jobPod("retrying"),
		},
	}
	s := BuildSnapshot(objs, 1, now)
	want := map[string]Status{
		"Job/a/done": StatusHealthy, "Job/a/running": StatusHealthy, "Job/a/bad-image": StatusDown,
		"Job/a/retrying": StatusDegraded, "Job/a/failed": StatusDown,
	}
	for id, st := range want {
		if got := find(t, s, id).Status; got != st {
			t.Errorf("%s: %s, want %s", id, got, st)
		}
	}
	if len(s.Workloads) != len(want) {
		t.Errorf("a CronJob's Job shouldn't be its own workload (the CronJob isn't in this test): %d workloads", len(s.Workloads))
	}
	if m := find(t, s, "Job/a/failed").Message; m != "6 failed attempt(s)" {
		t.Errorf("message %q", m)
	}
}

func TestImageTag(t *testing.T) {
	cases := map[string]string{
		"nginx":                      "latest",
		"nginx:1.27":                 "1.27",
		"localhost:5000/team/app":    "latest",
		"localhost:5000/team/app:v2": "v2",
		"ghcr.io/a/b@sha256:0123456789abcdef0123": "sha256:0123456789ab",
		"": "",
	}
	for in, want := range cases {
		if got := imageTag(in); got != want {
			t.Errorf("imageTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFailingCondition(t *testing.T) {
	d := deployment("a", "api", 1, 0)
	d.Status.Conditions = []appsv1.DeploymentCondition{
		{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue, Message: "fine"},
		{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Message: `ReplicaSet "api-1" has timed out progressing.`},
	}
	s := BuildSnapshot(Objects{Deployments: []*appsv1.Deployment{d}}, 1, now)
	if m := s.Workloads[0].Message; m != `ReplicaSet "api-1" has timed out progressing.` {
		t.Errorf("message %q", m)
	}
}
