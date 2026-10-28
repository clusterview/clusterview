package cluster

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func status(c corev1.ContainerStatus) podOpt {
	return func(p *corev1.Pod) {
		c.Name = "app"
		p.Status.ContainerStatuses = []corev1.ContainerStatus{c}
	}
}

func warning(kind, ns, name, reason, msg string, ago time.Duration) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Namespace: ns, Name: name + "." + reason},
		InvolvedObject: corev1.ObjectReference{Kind: kind, Namespace: ns, Name: name},
		Type:           corev1.EventTypeWarning,
		Reason:         reason,
		Message:        msg,
		Count:          3,
		LastTimestamp:  metav1.NewTime(now.Add(-ago)),
	}
}

// onePod builds a Deployment with one pod and returns the snapshot.
func onePod(t *testing.T, p *corev1.Pod, extra Objects) Snapshot {
	t.Helper()
	p.Namespace = "shop"
	p.OwnerReferences = controller("ReplicaSet", "api-7d9c8b6f5", "rs1")
	extra.Deployments = append(extra.Deployments, deployment("shop", "api", 1, 0))
	extra.ReplicaSets = append(extra.ReplicaSets, replicaSet("shop", "api-7d9c8b6f5", "api", "rs1"))
	extra.Pods = append(extra.Pods, p)
	return BuildSnapshot(extra, 1, now)
}

func firstProblem(t *testing.T, s Snapshot) Problem {
	t.Helper()
	w := find(t, s, "Deployment/shop/api")
	if len(w.Problems) == 0 {
		t.Fatalf("no problems found: %+v", w)
	}
	return w.Problems[0]
}

func TestContainerProblems(t *testing.T) {
	waitingWith := func(reason, msg string) corev1.ContainerStatus {
		return corev1.ContainerStatus{Image: "ghcr.io/acme/api:9.9", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: msg}}}
	}
	oom := corev1.ContainerStatus{
		RestartCount:         5,
		State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137}},
	}
	crash := corev1.ContainerStatus{
		RestartCount:         7,
		State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1}},
	}
	cases := []struct {
		name   string
		status corev1.ContainerStatus
		kind   string
		title  string
		detail string
	}{
		{"bad image", waitingWith("ImagePullBackOff", `Back-off pulling image "ghcr.io/acme/api:9.9": not found`), "image-pull", "Can't download its image", "ghcr.io/acme/api:9.9: Back-off pulling image"},
		{"missing secret", waitingWith("CreateContainerConfigError", `secret "db-password" not found`), "config", "Missing configuration", `secret "db-password" not found`},
		{"out of memory", oom, "oom", "Running out of memory", "memory limit of 256Mi; restarted 5 times"},
		{"crash loop", crash, "crash-loop", "Keeps crashing", "Exits with code 1 (Error); restarted 7 times"},
		{"between crashes", corev1.ContainerStatus{
			RestartCount: 2,
			State:        corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 3}},
		}, "crash-loop", "Keeps crashing", "Exits with code 3 (Error); restarted 2 times"},
	}
	for _, c := range cases {
		p := pod("shop", "api-7d9c8b6f5-aaaaa", nil, status(c.status))
		p.Spec.Containers[0].Resources.Limits = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")}
		got := firstProblem(t, onePod(t, p, Objects{}))
		if got.Kind != c.kind || got.Title != c.title || !strings.Contains(got.Detail, c.detail) || got.Severity != SeverityCritical {
			t.Errorf("%s: got %+v", c.name, got)
		}
		if len(got.Pods) != 1 || got.Pods[0] != "api-7d9c8b6f5-aaaaa" {
			t.Errorf("%s: pods %v", c.name, got.Pods)
		}
	}
}

func TestInitContainerProblem(t *testing.T) {
	p := pod("shop", "api-1", nil, func(p *corev1.Pod) {
		p.Status.Phase = corev1.PodPending
		p.Status.InitContainerStatuses = []corev1.ContainerStatus{{
			Name: "migrate", RestartCount: 3,
			State:                corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 2}},
		}}
	})
	got := firstProblem(t, onePod(t, p, Objects{}))
	if got.Kind != "init-crash-loop" || !strings.HasPrefix(got.Detail, `Setup step "migrate": Exits with code 2`) {
		t.Errorf("got %+v", got)
	}
}

func TestPendingPodProblems(t *testing.T) {
	pending := func(opts ...podOpt) *corev1.Pod {
		p := pod("shop", "api-1", nil, func(p *corev1.Pod) {
			p.Status.Phase = corev1.PodPending
			p.Status.ContainerStatuses = nil
			p.Status.Conditions = []corev1.PodCondition{{
				Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable",
				Message: "0/3 nodes are available: 3 Insufficient cpu.",
			}}
		})
		for _, o := range opts {
			o(p)
		}
		return p
	}
	if got := firstProblem(t, onePod(t, pending(), Objects{})); got.Kind != "unschedulable" || got.Detail != "0/3 nodes are available: 3 Insufficient cpu." {
		t.Errorf("unschedulable: %+v", got)
	}

	withClaim := pending(func(p *corev1.Pod) {
		p.Spec.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data-api-1"},
		}}}
	})
	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "data-api-1"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
	}
	if got := firstProblem(t, onePod(t, withClaim, Objects{PVCs: []*corev1.PersistentVolumeClaim{claim}})); got.Kind != "storage" || !strings.Contains(got.Detail, "data-api-1 is still waiting") {
		t.Errorf("pending claim: %+v", got)
	}
	if got := firstProblem(t, onePod(t, withClaim, Objects{})); got.Kind != "storage" || !strings.Contains(got.Detail, "doesn't exist") {
		t.Errorf("missing claim: %+v", got)
	}
}

func TestRunningPodProblems(t *testing.T) {
	notReady := pod("shop", "api-1", nil, func(p *corev1.Pod) {
		p.Status.ContainerStatuses[0].Ready = false
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
	})
	probe := warning("Pod", "shop", "api-1", "Unhealthy", "Readiness probe failed: HTTP probe failed with statuscode: 503", time.Minute)
	if got := firstProblem(t, onePod(t, notReady, Objects{Events: []*corev1.Event{probe}})); got.Kind != "probe" || !strings.Contains(got.Detail, "503") {
		t.Errorf("probe: %+v", got)
	}

	onDeadNode := pod("shop", "api-1", nil, func(p *corev1.Pod) { p.Spec.NodeName = "node-2" })
	deadNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-2"}, Status: corev1.NodeStatus{
		Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionUnknown}},
	}}
	if got := firstProblem(t, onePod(t, onDeadNode, Objects{Nodes: []*corev1.Node{deadNode}})); got.Kind != "node-down" {
		t.Errorf("node down: %+v", got)
	}

	restarted := func(ago time.Duration, reason string) *corev1.Pod {
		return pod("shop", "api-1", nil, func(p *corev1.Pod) {
			p.Status.ContainerStatuses[0].RestartCount = 2
			p.Status.ContainerStatuses[0].LastTerminationState.Terminated = &corev1.ContainerStateTerminated{
				Reason: reason, ExitCode: 137, FinishedAt: metav1.NewTime(now.Add(-ago)),
			}
		})
	}
	healthy := func(p *corev1.Pod) Snapshot { return onePod(t, p, Objects{}) }
	if got := firstProblem(t, healthy(restarted(10*time.Minute, "OOMKilled"))); got.Kind != "oom-recent" || got.Severity != SeverityWarning {
		t.Errorf("recent OOM: %+v", got)
	}
	if got := firstProblem(t, healthy(restarted(10*time.Minute, "Error"))); got.Kind != "restarted" {
		t.Errorf("recent restart: %+v", got)
	}
	// An old restart is history, not a problem; the Deployment (0 of 1 ready
	// in this fixture) falls back to "Not running".
	if got := firstProblem(t, healthy(restarted(3*time.Hour, "Error"))); got.Kind != "not-running" {
		t.Errorf("old restart: %+v", got)
	}

	evicted := pod("shop", "api-1", nil, func(p *corev1.Pod) {
		p.Status.Phase = corev1.PodFailed
		p.Status.Reason = "Evicted"
		p.Status.Message = "The node was low on resource: memory."
	})
	if got := firstProblem(t, onePod(t, evicted, Objects{})); got.Kind != "evicted" || got.Detail != "The node was low on resource: memory." {
		t.Errorf("evicted: %+v", got)
	}
}

func TestWorkloadProblems(t *testing.T) {
	d := deployment("shop", "api", 2, 1)
	d.Status.Conditions = []appsv1.DeploymentCondition{{
		Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded",
		Message: `ReplicaSet "api-2" has timed out progressing.`,
	}}
	quota := warning("ReplicaSet", "shop", "api-7d9c8b6f5", "FailedCreate", `Error creating: pods "api-x" is forbidden: exceeded quota: compute`, 5*time.Minute)
	s := BuildSnapshot(Objects{
		Deployments: []*appsv1.Deployment{d},
		ReplicaSets: []*appsv1.ReplicaSet{replicaSet("shop", "api-7d9c8b6f5", "api", "rs1")},
		Events:      []*corev1.Event{quota},
	}, 1, now)
	w := find(t, s, "Deployment/shop/api")
	kinds := []string{}
	for _, p := range w.Problems {
		kinds = append(kinds, p.Kind)
	}
	if strings.Join(kinds, ",") != "rollout-stuck,cant-create-pods" {
		t.Errorf("problems: %v", kinds)
	}
	if len(w.Events) != 1 || w.Events[0].Object != "ReplicaSet/api-7d9c8b6f5" || w.Events[0].Count != 3 {
		t.Errorf("events: %+v", w.Events)
	}

	// The same warning on a workload that's running fine is only a warning.
	ok := deployment("shop", "api", 1, 1)
	s = BuildSnapshot(Objects{
		Deployments: []*appsv1.Deployment{ok},
		ReplicaSets: []*appsv1.ReplicaSet{replicaSet("shop", "api-7d9c8b6f5", "api", "rs1")},
		Events:      []*corev1.Event{quota},
	}, 1, now)
	if p := find(t, s, "Deployment/shop/api").Problems; len(p) != 1 || p[0].Severity != SeverityWarning || p[0].Title != "Had trouble creating pods" {
		t.Errorf("healthy workload: %+v", p)
	}
}

func TestJobProblems(t *testing.T) {
	failed := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ops", Name: "migrate", UID: "j1"},
		Status: batchv1.JobStatus{Failed: 6, Conditions: []batchv1.JobCondition{{
			Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Message: "Job has reached the specified backoff limit",
		}}},
	}
	attempt := pod("ops", "migrate-abcde", controller("Job", "migrate", "j1"), func(p *corev1.Pod) {
		p.Status.Phase = corev1.PodFailed
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 3},
		}}}
	})
	s := BuildSnapshot(Objects{Jobs: []*batchv1.Job{failed}, Pods: []*corev1.Pod{attempt}}, 1, now)
	w := find(t, s, "Job/ops/migrate")
	if len(w.Problems) != 2 || w.Problems[0].Kind != "exited" || w.Problems[1].Kind != "job-failed" {
		t.Errorf("failed job: %+v", w.Problems)
	}

	// A CronJob whose latest run succeeded ignores an earlier failed run.
	cj := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Namespace: "ops", Name: "backup", UID: "cj"}}
	owned := func(name string, minutesAgo int, st batchv1.JobStatus) *batchv1.Job {
		return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
			Namespace: "ops", Name: name, UID: types.UID(name),
			CreationTimestamp: metav1.NewTime(now.Add(-time.Duration(minutesAgo) * time.Minute)),
			OwnerReferences:   []metav1.OwnerReference{{Kind: "CronJob", Name: "backup", UID: "cj", Controller: ptr(true)}},
		}, Status: st}
	}
	oldPod := pod("ops", "backup-1-xyz", controller("Job", "backup-1", "backup-1"), func(p *corev1.Pod) {
		p.Status.Phase = corev1.PodFailed
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{
			Terminated: &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1},
		}}}
	})
	s = BuildSnapshot(Objects{
		CronJobs: []*batchv1.CronJob{cj},
		Jobs:     []*batchv1.Job{owned("backup-1", 120, batchv1.JobStatus{Failed: 1}), owned("backup-2", 60, batchv1.JobStatus{Succeeded: 1})},
		Pods:     []*corev1.Pod{oldPod},
	}, 1, now)
	if w := find(t, s, "CronJob/ops/backup"); w.Status != StatusHealthy || len(w.Problems) != 0 {
		t.Errorf("cronjob: status %s problems %+v", w.Status, w.Problems)
	}
}

func TestProblemsMergeAcrossPodsAndServices(t *testing.T) {
	crashing := func(name string) *corev1.Pod {
		return pod("shop", name, controller("ReplicaSet", "api-7d9c8b6f5", "rs1"), waiting("CrashLoopBackOff", 4))
	}
	restartedOK := pod("shop", "api-7d9c8b6f5-ccccc", controller("ReplicaSet", "api-7d9c8b6f5", "rs1"), func(p *corev1.Pod) {
		p.Status.ContainerStatuses[0].RestartCount = 1
		p.Status.ContainerStatuses[0].LastTerminationState.Terminated = &corev1.ContainerStateTerminated{Reason: "Error", ExitCode: 1, FinishedAt: metav1.NewTime(now.Add(-time.Minute))}
	})
	s := BuildSnapshot(Objects{
		Deployments: []*appsv1.Deployment{deployment("shop", "api", 3, 1)},
		ReplicaSets: []*appsv1.ReplicaSet{replicaSet("shop", "api-7d9c8b6f5", "api", "rs1")},
		Pods:        []*corev1.Pod{restartedOK, crashing("api-7d9c8b6f5-aaaaa"), crashing("api-7d9c8b6f5-bbbbb")},
	}, 1, now)
	svc := findService(t, s, "Deployment/shop/api")
	if len(svc.Problems) != 2 {
		t.Fatalf("problems: %+v", svc.Problems)
	}
	if p := svc.Problems[0]; p.Kind != "crash-loop" || len(p.Pods) != 2 {
		t.Errorf("crash loop should list both pods first: %+v", p)
	}
	if p := svc.Problems[1]; p.Kind != "restarted" || p.Severity != SeverityWarning {
		t.Errorf("warning should come after: %+v", p)
	}
}

func TestEventsWindowAndOrder(t *testing.T) {
	p := pod("shop", "api-1", nil)
	s := onePod(t, p, Objects{Events: []*corev1.Event{
		warning("Pod", "shop", "api-1", "BackOff", "older", 30*time.Minute),
		warning("Pod", "shop", "api-1", "Unhealthy", "newer", time.Minute),
		warning("Pod", "shop", "api-1", "Failed", "too old", 2*time.Hour),
		warning("Pod", "shop", "someone-else", "Failed", "not ours", time.Minute),
	}})
	ev := find(t, s, "Deployment/shop/api").Events
	if len(ev) != 2 || ev[0].Message != "newer" || ev[1].Message != "older" {
		t.Errorf("events: %+v", ev)
	}
}
