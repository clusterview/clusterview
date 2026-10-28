package cluster

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Objects is what the cache has seen, as the build functions take it.
type Objects struct {
	Deployments  []*appsv1.Deployment
	ReplicaSets  []*appsv1.ReplicaSet
	StatefulSets []*appsv1.StatefulSet
	DaemonSets   []*appsv1.DaemonSet
	Jobs         []*batchv1.Job
	CronJobs     []*batchv1.CronJob
	Pods         []*corev1.Pod
	HPAs         []*autoscalingv2.HorizontalPodAutoscaler
	// Events (warnings only), PVCs and Nodes explain problems.
	Events []*corev1.Event
	PVCs   []*corev1.PersistentVolumeClaim
	Nodes  []*corev1.Node
	// KubeServices and Ingresses link services together.
	KubeServices []*corev1.Service
	Ingresses    []*networkingv1.Ingress
}

// stuckReasons are waiting/terminated reasons that mean a pod won't make
// progress on its own, as opposed to ContainerCreating on the way up.
var stuckReasons = map[string]bool{
	"CrashLoopBackOff":           true,
	"ImagePullBackOff":           true,
	"ErrImagePull":               true,
	"InvalidImageName":           true,
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
	"RunContainerError":          true,
	"OOMKilled":                  true,
	"Error":                      true,
}

const (
	labelPartOf   = "app.kubernetes.io/part-of"
	labelInstance = "app.kubernetes.io/instance"
	labelName     = "app.kubernetes.io/name"
)

// WorkloadID is the stable ID of a workload.
func WorkloadID(kind Kind, namespace, name string) string {
	return fmt.Sprintf("%s/%s/%s", kind, namespace, name)
}

// owners resolves a pod to the workload that runs it: Pod → ReplicaSet →
// Deployment, Pod → Job → CronJob, straight to a StatefulSet, DaemonSet or
// Job, and otherwise the pod is its own workload.
type owners struct {
	replicaSets map[types.UID]string // ReplicaSet → Deployment, or the ReplicaSet itself
	jobs        map[types.UID]string // Job → CronJob
}

func newOwners(rs []*appsv1.ReplicaSet, jobs []*batchv1.Job) owners {
	o := owners{replicaSets: make(map[types.UID]string, len(rs)), jobs: map[types.UID]string{}}
	for _, r := range rs {
		if ref := metav1.GetControllerOf(r); ref != nil && ref.Kind == "Deployment" {
			o.replicaSets[r.UID] = WorkloadID(KindDeployment, r.Namespace, ref.Name)
		} else {
			o.replicaSets[r.UID] = WorkloadID(KindReplicaSet, r.Namespace, r.Name)
		}
	}
	for _, j := range jobs {
		if ref := metav1.GetControllerOf(j); ref != nil && ref.Kind == "CronJob" {
			o.jobs[j.UID] = WorkloadID(KindCronJob, j.Namespace, ref.Name)
		}
	}
	return o
}

// workloadOf returns the ID of the workload that runs the pod.
func (o owners) workloadOf(pod *corev1.Pod) string {
	ref := metav1.GetControllerOf(pod)
	if ref == nil {
		return WorkloadID(KindPod, pod.Namespace, pod.Name)
	}
	switch ref.Kind {
	case "ReplicaSet":
		if id, ok := o.replicaSets[ref.UID]; ok {
			return id
		}
		return "" // the ReplicaSet isn't in the cache yet
	case "StatefulSet":
		return WorkloadID(KindStatefulSet, pod.Namespace, ref.Name)
	case "DaemonSet":
		return WorkloadID(KindDaemonSet, pod.Namespace, ref.Name)
	case "Job":
		if id, ok := o.jobs[ref.UID]; ok {
			return id
		}
		return WorkloadID(KindJob, pod.Namespace, ref.Name)
	}
	// A static pod (owned by its Node) or one an operator runs directly.
	return WorkloadID(KindPod, pod.Namespace, pod.Name)
}

// BuildSnapshot turns the cache's objects into the dashboard's services and
// workloads.
func BuildSnapshot(objs Objects, version uint64, now time.Time) Snapshot {
	own := newOwners(objs.ReplicaSets, objs.Jobs)
	podsByWorkload := map[string][]PodState{}
	rawPods := map[string][]*corev1.Pod{}
	var barePods []*corev1.Pod
	summary := Summary{}
	for _, p := range objs.Pods {
		if p.DeletionTimestamp != nil {
			continue
		}
		state := podState(p)
		summary.Pods++
		if state.Ready {
			summary.ReadyPods++
		}
		summary.Restarts += state.Restarts
		id := own.workloadOf(p)
		if id == "" {
			continue
		}
		podsByWorkload[id] = append(podsByWorkload[id], state)
		rawPods[id] = append(rawPods[id], p)
		if strings.HasPrefix(id, string(KindPod)+"/") {
			barePods = append(barePods, p)
		}
	}

	var workloads []Workload
	deployments := map[string]*appsv1.Deployment{}
	jobs := map[string]*batchv1.Job{} // a Job, or a CronJob's latest run
	for _, d := range objs.Deployments {
		w := newWorkload(KindDeployment, d.ObjectMeta, d.Spec.Template, podsByWorkload)
		w.Desired = int(ptrOr(d.Spec.Replicas, 1))
		w.Ready = int(d.Status.ReadyReplicas)
		w.Available = int(d.Status.AvailableReplicas)
		w.Updated = int(d.Status.UpdatedReplicas)
		w.Message = failingDeploymentCondition(d.Status.Conditions)
		w.Autoscaling = autoscalingFor("Deployment", d.Namespace, d.Name, objs.HPAs)
		w.Status = replicaStatus(w.Desired, w.Ready)
		deployments[w.ID] = d
		workloads = append(workloads, w)
	}
	for _, r := range objs.ReplicaSets {
		// Only ReplicaSets nothing built in manages, and only while they run
		// something; a Rollout keeps its old ones at zero.
		if ref := metav1.GetControllerOf(r); ref != nil && ref.Kind == "Deployment" {
			continue
		}
		id := WorkloadID(KindReplicaSet, r.Namespace, r.Name)
		if ptrOr(r.Spec.Replicas, 1) == 0 && len(podsByWorkload[id]) == 0 {
			continue
		}
		w := newWorkload(KindReplicaSet, r.ObjectMeta, r.Spec.Template, podsByWorkload)
		w.Desired = int(ptrOr(r.Spec.Replicas, 1))
		w.Ready = int(r.Status.ReadyReplicas)
		w.Available = int(r.Status.AvailableReplicas)
		w.Updated = w.Desired
		w.Status = replicaStatus(w.Desired, w.Ready)
		workloads = append(workloads, w)
	}
	for _, s := range objs.StatefulSets {
		w := newWorkload(KindStatefulSet, s.ObjectMeta, s.Spec.Template, podsByWorkload)
		w.Desired = int(ptrOr(s.Spec.Replicas, 1))
		w.Ready = int(s.Status.ReadyReplicas)
		w.Available = int(s.Status.AvailableReplicas)
		w.Updated = int(s.Status.UpdatedReplicas)
		w.Autoscaling = autoscalingFor("StatefulSet", s.Namespace, s.Name, objs.HPAs)
		w.Status = replicaStatus(w.Desired, w.Ready)
		workloads = append(workloads, w)
	}
	for _, ds := range objs.DaemonSets {
		w := newWorkload(KindDaemonSet, ds.ObjectMeta, ds.Spec.Template, podsByWorkload)
		w.Desired = int(ds.Status.DesiredNumberScheduled)
		w.Ready = int(ds.Status.NumberReady)
		w.Available = int(ds.Status.NumberAvailable)
		w.Updated = int(ds.Status.UpdatedNumberScheduled)
		w.Status = replicaStatus(w.Desired, w.Ready)
		workloads = append(workloads, w)
	}
	jobsByCronJob := map[string][]*batchv1.Job{}
	for _, j := range objs.Jobs {
		if id, ok := own.jobs[j.UID]; ok {
			jobsByCronJob[id] = append(jobsByCronJob[id], j)
			continue
		}
		w := jobWorkload(j, podsByWorkload)
		jobs[w.ID] = j
		workloads = append(workloads, w)
	}
	for _, cj := range objs.CronJobs {
		id := WorkloadID(KindCronJob, cj.Namespace, cj.Name)
		w, latest := cronJobWorkload(cj, jobsByCronJob[id], podsByWorkload)
		if latest != nil {
			jobs[id] = latest
		}
		workloads = append(workloads, w)
	}
	for _, p := range barePods {
		workloads = append(workloads, podWorkload(p, podsByWorkload))
	}

	diag := newDiagnosis(objs, now)
	namespaces := map[string]bool{}
	for i := range workloads {
		w := &workloads[i]
		namespaces[w.Namespace] = true
		w.Events = diag.workloadEvents(w.ID, rawPods[w.ID])
		var problems []Problem
		for _, p := range problemPods(w, rawPods[w.ID], jobs[w.ID]) {
			problems = append(problems, diag.podProblems(p)...)
		}
		problems = append(problems, diag.ownProblems(w, deployments[w.ID], jobs[w.ID])...)
		if len(problems) == 0 {
			problems = fallbackProblem(w)
		}
		w.Problems = mergeProblems(problems)
	}
	slices.SortFunc(workloads, func(a, b Workload) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name), cmp.Compare(a.Kind, b.Kind))
	})
	if workloads == nil {
		workloads = []Workload{}
	}
	services := BuildServices(workloads)
	links := linkServices(objs, services, workloads, rawPods)
	for _, s := range services {
		if s.Status == StatusDegraded || s.Status == StatusDown {
			summary.NeedAttention++
		}
	}
	summary.Services = len(services)
	summary.Workloads = len(workloads)
	summary.NamespaceCount = len(namespaces)
	return Snapshot{Version: version, FetchedAt: now, Summary: summary, Services: services, Links: links, Workloads: workloads}
}

func newWorkload(kind Kind, meta metav1.ObjectMeta, tmpl corev1.PodTemplateSpec, podsByWorkload map[string][]PodState) Workload {
	id := WorkloadID(kind, meta.Namespace, meta.Name)
	pods := slices.Clone(podsByWorkload[id])
	slices.SortFunc(pods, func(a, b PodState) int { return cmp.Compare(a.Name, b.Name) })
	if pods == nil {
		pods = []PodState{}
	}
	w := Workload{
		ID:          id,
		Kind:        kind,
		Namespace:   meta.Namespace,
		Name:        meta.Name,
		CreatedAt:   meta.CreationTimestamp.Time,
		Owner:       operatorOwner(meta.OwnerReferences),
		Pods:        pods,
		labels:      merged(tmpl.Labels, meta.Labels),
		annotations: merged(tmpl.Annotations, meta.Annotations),
		env:         envOf(tmpl.Spec),
	}
	w.App = cmp.Or(w.labels[labelPartOf], w.labels[labelInstance])
	if len(tmpl.Spec.Containers) > 0 {
		w.Image = tmpl.Spec.Containers[0].Image
		w.ImageTag = imageTag(w.Image)
	}
	return w
}

// merged returns base overlaid with top; top wins.
func merged(base, top map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(top))
	maps.Copy(out, base)
	maps.Copy(out, top)
	return out
}

// operatorOwner returns the controller reference when it points at a custom
// resource, which means an operator runs this object. Built-in owners
// (Deployments, Jobs, Nodes…) return nil.
func operatorOwner(refs []metav1.OwnerReference) *Owner {
	for _, r := range refs {
		if r.Controller == nil || !*r.Controller {
			continue
		}
		o := Owner{APIVersion: r.APIVersion, Kind: r.Kind, Name: r.Name}
		if g := o.Group(); g == "" || g == "apps" || g == "batch" || strings.HasSuffix(g, ".k8s.io") {
			return nil
		}
		return &o
	}
	return nil
}

func replicaStatus(desired, ready int) Status {
	switch {
	case desired == 0:
		return StatusIdle
	case ready >= desired:
		return StatusHealthy
	case ready == 0:
		return StatusDown
	default:
		return StatusDegraded
	}
}

func jobWorkload(j *batchv1.Job, podsByWorkload map[string][]PodState) Workload {
	w := newWorkload(KindJob, j.ObjectMeta, j.Spec.Template, podsByWorkload)
	applyJobStatus(&w, j, w.Pods)
	return w
}

// applyJobStatus sets a Job's health from its counters and pods.
func applyJobStatus(w *Workload, j *batchv1.Job, pods []PodState) {
	succeeded, failed, active := int(j.Status.Succeeded), int(j.Status.Failed), int(j.Status.Active)
	running, stuck := false, false
	for _, p := range pods {
		if p.Stuck {
			stuck = true
		} else if p.Phase == string(corev1.PodRunning) {
			running = true
		}
	}
	w.Desired = 1
	if succeeded > 0 || running {
		w.Ready = 1
	}
	w.Available, w.Updated = succeeded, succeeded
	if failed > 0 {
		w.Message = fmt.Sprintf("%d failed attempt(s)", failed)
	}
	// An active Job isn't a healthy one: Kubernetes keeps it active while its
	// pod is pending, pulling a bad image or crash-looping.
	switch {
	case succeeded > 0:
		w.Status = StatusHealthy
	case active > 0 && stuck:
		w.Status = StatusDown
	case active > 0 && running && failed == 0:
		w.Status = StatusHealthy
	case active > 0:
		w.Status = StatusDegraded // still starting, or retrying
	case failed > 0:
		w.Status = StatusDown
	default:
		w.Status = StatusUnknown
	}
}

// problemPods are the pods worth diagnosing. A Job's finished attempts only
// matter while it's failing, and a CronJob's only from its latest run.
func problemPods(w *Workload, pods []*corev1.Pod, job *batchv1.Job) []*corev1.Pod {
	if w.Kind != KindJob && w.Kind != KindCronJob {
		return pods
	}
	var out []*corev1.Pod
	for _, p := range pods {
		if w.Kind == KindCronJob && (job == nil || !strings.HasPrefix(p.Name, job.Name+"-")) {
			continue
		}
		finished := p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed
		if finished && w.Status != StatusDown {
			continue
		}
		out = append(out, p)
	}
	return out
}

// cronJobWorkload takes its health from the most recent run, which it also
// returns.
func cronJobWorkload(cj *batchv1.CronJob, jobs []*batchv1.Job, podsByWorkload map[string][]PodState) (Workload, *batchv1.Job) {
	w := newWorkload(KindCronJob, cj.ObjectMeta, cj.Spec.JobTemplate.Spec.Template, podsByWorkload)
	w.Schedule = cj.Spec.Schedule
	w.Suspended = ptrOr(cj.Spec.Suspend, false)
	if t := cj.Status.LastScheduleTime; t != nil {
		last := t.Time
		w.LastRun = &last
	}
	if len(jobs) == 0 {
		w.Status = StatusIdle // hasn't run yet
		return w, nil
	}
	latest := slices.MaxFunc(jobs, func(a, b *batchv1.Job) int {
		return a.CreationTimestamp.Compare(b.CreationTimestamp.Time)
	})
	var latestPods []PodState
	for _, p := range w.Pods {
		if strings.HasPrefix(p.Name, latest.Name+"-") {
			latestPods = append(latestPods, p)
		}
	}
	applyJobStatus(&w, latest, latestPods)
	// A run that's still starting isn't a problem for a CronJob: it runs
	// again and again, so judge it on failures only.
	if w.Status == StatusDegraded && latest.Status.Failed == 0 {
		w.Status, w.Ready = StatusHealthy, 1
	}
	if w.Status == StatusDown {
		w.Message = cmp.Or(w.Message, "Last run failed")
	}
	if w.Suspended && w.Status != StatusDown {
		w.Status = StatusIdle
	}
	return w, latest
}

// podWorkload is a pod nothing else manages, judged on its own.
func podWorkload(p *corev1.Pod, podsByWorkload map[string][]PodState) Workload {
	// A static pod's owner is its Node, so it gets no Owner; an operator's pod
	// points at its custom resource and does.
	w := newWorkload(KindPod, p.ObjectMeta, corev1.PodTemplateSpec{ObjectMeta: p.ObjectMeta, Spec: p.Spec}, podsByWorkload)
	w.Desired = 1
	state := podState(p)
	switch {
	case state.Ready:
		w.Ready, w.Available, w.Updated = 1, 1, 1
		w.Status = StatusHealthy
	case p.Status.Phase == corev1.PodSucceeded:
		w.Desired = 0
		w.Status = StatusIdle
	case p.Status.Phase == corev1.PodFailed || state.Stuck:
		w.Status = StatusDown
	default:
		w.Status = StatusDegraded
	}
	return w
}

// podState reduces a pod to what explains its health.
func podState(p *corev1.Pod) PodState {
	ready := 0
	restarts := 0
	for _, c := range p.Status.ContainerStatuses {
		if c.Ready {
			ready++
		}
		restarts += int(c.RestartCount)
	}
	// A failing init container leaves the main one at PodInitializing, which
	// hides the real problem, so it is reported first (kubectl's "Init:" form).
	reason := ""
	if r := containerReason(p.Status.InitContainerStatuses); r != "" {
		reason = "Init:" + r
	} else {
		reason = containerReason(p.Status.ContainerStatuses)
	}
	if reason == "" && p.Status.Phase == corev1.PodPending {
		for _, c := range p.Status.Conditions {
			if c.Status == corev1.ConditionFalse && c.Reason != "" {
				reason = c.Reason
				break
			}
		}
	}
	total := len(p.Status.ContainerStatuses)
	s := PodState{
		Name:            p.Name,
		Namespace:       p.Namespace,
		Node:            p.Spec.NodeName,
		Phase:           string(p.Status.Phase),
		Ready:           total > 0 && ready == total,
		ReadyContainers: fmt.Sprintf("%d/%d", ready, total),
		Restarts:        restarts,
		Reason:          reason,
		Stuck:           stuckReasons[strings.TrimPrefix(reason, "Init:")],
	}
	if s.Phase == "" {
		s.Phase = "Unknown"
	}
	if p.Status.StartTime != nil {
		t := p.Status.StartTime.Time
		s.StartedAt = &t
	}
	return s
}

func containerReason(statuses []corev1.ContainerStatus) string {
	for _, c := range statuses {
		if w := c.State.Waiting; w != nil && w.Reason != "" {
			return w.Reason
		}
		if t := c.State.Terminated; t != nil && t.Reason != "" && t.Reason != "Completed" {
			return t.Reason
		}
	}
	return ""
}

func failingDeploymentCondition(conds []appsv1.DeploymentCondition) string {
	for _, c := range conds {
		if c.Status == corev1.ConditionFalse && c.Message != "" {
			return c.Message
		}
	}
	return ""
}

func autoscalingFor(kind, namespace, name string, hpas []*autoscalingv2.HorizontalPodAutoscaler) *Autoscaling {
	for _, h := range hpas {
		t := h.Spec.ScaleTargetRef
		if h.Namespace != namespace || t.Kind != kind || t.Name != name {
			continue
		}
		managedBy := "hpa"
		if strings.HasPrefix(h.Name, "keda-hpa-") {
			managedBy = "keda"
		}
		for k := range h.Labels {
			if strings.HasPrefix(k, "scaledobject.keda.sh") {
				managedBy = "keda"
			}
		}
		return &Autoscaling{
			Min:       int(ptrOr(h.Spec.MinReplicas, 1)),
			Max:       int(h.Spec.MaxReplicas),
			Current:   int(h.Status.CurrentReplicas),
			ManagedBy: managedBy,
		}
	}
	return nil
}

// imageTag shortens an image reference to its tag, or its digest's first
// characters.
func imageTag(image string) string {
	if image == "" {
		return ""
	}
	if _, digest, ok := strings.Cut(image, "@"); ok {
		return digest[:min(len(digest), 19)]
	}
	last := image[strings.LastIndex(image, "/")+1:]
	if _, tag, ok := strings.Cut(last, ":"); ok {
		return tag
	}
	return "latest"
}

func ptrOr[T any](p *T, fallback T) T {
	if p == nil {
		return fallback
	}
	return *p
}
