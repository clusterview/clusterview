package cluster

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

// eventWindow is how far back warnings count. Kubernetes keeps events for an
// hour by default anyway.
const eventWindow = time.Hour

// maxEvents caps the warnings listed per workload.
const maxEvents = 10

// diagnosis is what problem-finding needs beyond the pod itself.
type diagnosis struct {
	now       time.Time
	pvcs      map[string]*corev1.PersistentVolumeClaim // "<ns>/<name>"
	nodeReady map[string]bool                          // absent: unknown, assumed fine
	podEvents map[string][]*corev1.Event               // "<ns>/<pod>"
	ownEvents map[string][]*corev1.Event               // workload ID
}

func newDiagnosis(objs Objects, now time.Time) diagnosis {
	d := diagnosis{
		now:       now,
		pvcs:      map[string]*corev1.PersistentVolumeClaim{},
		nodeReady: map[string]bool{},
		podEvents: map[string][]*corev1.Event{},
		ownEvents: map[string][]*corev1.Event{},
	}
	for _, c := range objs.PVCs {
		d.pvcs[c.Namespace+"/"+c.Name] = c
	}
	for _, n := range objs.Nodes {
		ready := false
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeReady {
				ready = c.Status == corev1.ConditionTrue
			}
		}
		d.nodeReady[n.Name] = ready
	}
	rsToWorkload := map[string]string{}
	for _, r := range objs.ReplicaSets {
		id := WorkloadID(KindReplicaSet, r.Namespace, r.Name)
		for _, ref := range r.OwnerReferences {
			if ref.Controller != nil && *ref.Controller && ref.Kind == "Deployment" {
				id = WorkloadID(KindDeployment, r.Namespace, ref.Name)
			}
		}
		rsToWorkload[r.Namespace+"/"+r.Name] = id
	}
	for _, e := range objs.Events {
		if e.Type != corev1.EventTypeWarning || now.Sub(eventTime(e)) > eventWindow {
			continue
		}
		o := e.InvolvedObject
		switch o.Kind {
		case "Pod":
			d.podEvents[o.Namespace+"/"+o.Name] = append(d.podEvents[o.Namespace+"/"+o.Name], e)
		case "ReplicaSet":
			if id, ok := rsToWorkload[o.Namespace+"/"+o.Name]; ok {
				d.ownEvents[id] = append(d.ownEvents[id], e)
			}
		case "Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob":
			id := WorkloadID(Kind(o.Kind), o.Namespace, o.Name)
			d.ownEvents[id] = append(d.ownEvents[id], e)
		}
	}
	return d
}

func eventTime(e *corev1.Event) time.Time {
	switch {
	case !e.LastTimestamp.IsZero():
		return e.LastTimestamp.Time
	case e.Series != nil && !e.Series.LastObservedTime.IsZero():
		return e.Series.LastObservedTime.Time
	case !e.EventTime.IsZero():
		return e.EventTime.Time
	}
	return e.CreationTimestamp.Time
}

// workloadEvents lists recent warnings about a workload and its pods, newest
// first.
func (d diagnosis) workloadEvents(id string, pods []*corev1.Pod) []Event {
	var raw []*corev1.Event
	raw = append(raw, d.ownEvents[id]...)
	for _, p := range pods {
		raw = append(raw, d.podEvents[p.Namespace+"/"+p.Name]...)
	}
	slices.SortFunc(raw, func(a, b *corev1.Event) int { return eventTime(b).Compare(eventTime(a)) })
	out := []Event{}
	for _, e := range raw[:min(len(raw), maxEvents)] {
		out = append(out, Event{
			Reason:   e.Reason,
			Message:  strings.TrimSpace(e.Message),
			Object:   e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name,
			Count:    int(max(e.Count, 1)),
			LastSeen: eventTime(e),
		})
	}
	return out
}

// podProblems explains what's wrong with one pod, if anything.
func (d diagnosis) podProblems(p *corev1.Pod) []Problem {
	if p.DeletionTimestamp != nil {
		return nil
	}
	one := func(kind string, sev Severity, title, detail string) []Problem {
		return []Problem{{Kind: kind, Severity: sev, Title: title, Detail: detail, Pods: []string{p.Name}}}
	}
	if p.Status.Phase == corev1.PodFailed && p.Status.Reason == "Evicted" {
		return one("evicted", SeverityWarning, "Evicted from its node", p.Status.Message)
	}
	if n := p.Spec.NodeName; n != "" {
		if ready, known := d.nodeReady[n]; known && !ready {
			return one("node-down", SeverityCritical, "Its node is down", fmt.Sprintf("Node %s isn't ready.", n))
		}
	}

	limits := map[string]string{}
	for _, c := range append(slices.Clone(p.Spec.InitContainers), p.Spec.Containers...) {
		if q, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
			limits[c.Name] = q.String()
		}
	}
	for _, c := range p.Status.InitContainerStatuses {
		if pr := containerProblem(c, limits[c.Name]); pr != nil {
			pr.Kind = "init-" + pr.Kind
			pr.Detail = fmt.Sprintf("Setup step %q: %s", c.Name, pr.Detail)
			pr.Pods = []string{p.Name}
			return []Problem{*pr}
		}
	}
	var out []Problem
	for _, c := range p.Status.ContainerStatuses {
		if pr := containerProblem(c, limits[c.Name]); pr != nil {
			pr.Pods = []string{p.Name}
			out = append(out, *pr)
		}
	}
	if len(out) > 0 {
		return out
	}

	if p.Status.Phase == corev1.PodPending {
		for _, v := range p.Spec.Volumes {
			if v.PersistentVolumeClaim == nil {
				continue
			}
			pvc := d.pvcs[p.Namespace+"/"+v.PersistentVolumeClaim.ClaimName]
			switch {
			case pvc == nil:
				return one("storage", SeverityCritical, "Storage not ready",
					fmt.Sprintf("Volume claim %s doesn't exist.", v.PersistentVolumeClaim.ClaimName))
			case pvc.Status.Phase == corev1.ClaimPending:
				return one("storage", SeverityCritical, "Storage not ready",
					fmt.Sprintf("Volume claim %s is still waiting for a volume.", pvc.Name))
			}
		}
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
				return one("unschedulable", SeverityCritical, "Waiting for a node", c.Message)
			}
		}
	}

	if p.Status.Phase == corev1.PodRunning && !podReady(p) {
		for _, e := range d.podEvents[p.Namespace+"/"+p.Name] {
			if e.Reason == "Unhealthy" {
				return one("probe", SeverityCritical, "Failing health checks", strings.TrimSpace(e.Message))
			}
		}
	}

	// Running fine now, but it restarted in the last hour.
	for _, c := range p.Status.ContainerStatuses {
		last := c.LastTerminationState.Terminated
		if c.RestartCount == 0 || last == nil || d.now.Sub(last.FinishedAt.Time) > eventWindow {
			continue
		}
		if last.Reason == "OOMKilled" {
			return one("oom-recent", SeverityWarning, "Ran out of memory recently",
				fmt.Sprintf("Killed for going over its memory limit%s at %s; restarted %d times.", limitText(limits[c.Name]), last.FinishedAt.UTC().Format("15:04"), c.RestartCount))
		}
		return one("restarted", SeverityWarning, "Restarted recently",
			fmt.Sprintf("Exited with code %d (%s) at %s; restarted %d times.", last.ExitCode, cmp.Or(last.Reason, "Error"), last.FinishedAt.UTC().Format("15:04"), c.RestartCount))
	}
	return nil
}

// containerProblem explains a container that isn't running properly.
func containerProblem(c corev1.ContainerStatus, memLimit string) *Problem {
	w := c.State.Waiting
	now, last := c.State.Terminated, c.LastTerminationState.Terminated
	oom := (now != nil && now.Reason == "OOMKilled") || (last != nil && last.Reason == "OOMKilled")
	reason := ""
	if w != nil {
		reason = w.Reason
	}
	switch {
	case reason == "ImagePullBackOff" || reason == "ErrImagePull" || reason == "InvalidImageName" || reason == "ErrImageNeverPull":
		return &Problem{Kind: "image-pull", Severity: SeverityCritical, Title: "Can't download its image",
			Detail: fmt.Sprintf("%s: %s", c.Image, cmp.Or(firstLine(w.Message), reason))}
	case reason == "CreateContainerConfigError":
		return &Problem{Kind: "config", Severity: SeverityCritical, Title: "Missing configuration", Detail: firstLine(w.Message)}
	case reason == "CreateContainerError" || reason == "RunContainerError":
		return &Problem{Kind: "start-failed", Severity: SeverityCritical, Title: "Can't start", Detail: firstLine(w.Message)}
	case oom && (reason == "CrashLoopBackOff" || now != nil):
		return &Problem{Kind: "oom", Severity: SeverityCritical, Title: "Running out of memory",
			Detail: fmt.Sprintf("Killed for going over its memory limit%s; restarted %d times.", limitText(memLimit), c.RestartCount)}
	case reason == "CrashLoopBackOff", now != nil && now.ExitCode != 0 && c.RestartCount > 0:
		// Between crashes a container is briefly Terminated rather than in
		// CrashLoopBackOff; with restarts behind it, it's the same problem.
		exit := cmp.Or(now, last)
		detail := fmt.Sprintf("Restarted %d times.", c.RestartCount)
		if exit != nil {
			detail = fmt.Sprintf("Exits with code %d (%s); restarted %d times. Its logs show why.", exit.ExitCode, cmp.Or(exit.Reason, "Error"), c.RestartCount)
		}
		return &Problem{Kind: "crash-loop", Severity: SeverityCritical, Title: "Keeps crashing", Detail: detail}
	case now != nil && now.ExitCode != 0:
		return &Problem{Kind: "exited", Severity: SeverityCritical, Title: "Stopped with an error",
			Detail: fmt.Sprintf("Exited with code %d (%s).", now.ExitCode, cmp.Or(now.Reason, "Error"))}
	}
	return nil
}

// ownProblems are the workload's problems that aren't about one pod.
func (d diagnosis) ownProblems(w *Workload, deploy *appsv1.Deployment, job *batchv1.Job) []Problem {
	var out []Problem
	if deploy != nil {
		for _, c := range deploy.Status.Conditions {
			switch {
			case c.Type == appsv1.DeploymentProgressing && c.Status == corev1.ConditionFalse && c.Reason == "ProgressDeadlineExceeded":
				out = append(out, Problem{Kind: "rollout-stuck", Severity: SeverityCritical, Title: "Update stuck", Detail: c.Message})
			case c.Type == appsv1.DeploymentReplicaFailure && c.Status == corev1.ConditionTrue:
				out = append(out, Problem{Kind: "cant-create-pods", Severity: SeverityCritical, Title: "Can't create pods", Detail: c.Message})
			}
		}
	}
	if job != nil {
		for _, c := range job.Status.Conditions {
			if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
				title := "Failed"
				if w.Kind == KindCronJob {
					title = "Last run failed"
				}
				out = append(out, Problem{Kind: "job-failed", Severity: SeverityCritical, Title: title, Detail: c.Message})
			}
		}
	}
	for _, e := range d.ownEvents[w.ID] {
		if e.Reason != "FailedCreate" || slices.ContainsFunc(out, func(p Problem) bool { return p.Kind == "cant-create-pods" }) {
			continue
		}
		// A workload that's running fine got past it; still worth a look.
		if w.Status == StatusHealthy || w.Status == StatusIdle {
			out = append(out, Problem{Kind: "cant-create-pods", Severity: SeverityWarning, Title: "Had trouble creating pods", Detail: firstLine(e.Message)})
		} else {
			out = append(out, Problem{Kind: "cant-create-pods", Severity: SeverityCritical, Title: "Can't create pods", Detail: firstLine(e.Message)})
		}
	}
	if a := w.Autoscaling; a != nil && a.Max > a.Min && a.Current >= a.Max {
		out = append(out, Problem{Kind: "scale-limit", Severity: SeverityWarning, Title: "At its scaling limit",
			Detail: fmt.Sprintf("Running %d replicas, the most its autoscaler allows.", a.Current)})
	}
	return out
}

// fallbackProblem makes sure a workload that isn't running says so, even when
// nothing more specific was found.
func fallbackProblem(w *Workload) []Problem {
	switch w.Status {
	case StatusDown:
		return []Problem{{Kind: "not-running", Severity: SeverityCritical, Title: "Not running",
			Detail: fmt.Sprintf("%d of %d ready.", w.Ready, w.Desired)}}
	case StatusDegraded:
		return []Problem{{Kind: "partly-running", Severity: SeverityWarning, Title: "Partly running",
			Detail: fmt.Sprintf("%d of %d ready.", w.Ready, w.Desired)}}
	}
	return nil
}

// mergeProblems folds problems of the same kind together (one "Keeps
// crashing" listing three pods) and puts critical ones first.
func mergeProblems(list []Problem) []Problem {
	out := []Problem{}
	at := map[string]int{}
	for _, p := range list {
		if i, ok := at[p.Kind]; ok {
			for _, name := range p.Pods {
				if !slices.Contains(out[i].Pods, name) {
					out[i].Pods = append(out[i].Pods, name)
				}
			}
			continue
		}
		at[p.Kind] = len(out)
		p.Pods = slices.Clone(p.Pods)
		out = append(out, p)
	}
	slices.SortStableFunc(out, func(a, b Problem) int {
		return cmp.Compare(severityRank(a.Severity), severityRank(b.Severity))
	})
	return out
}

func severityRank(s Severity) int {
	if s == SeverityCritical {
		return 0
	}
	return 1
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func limitText(limit string) string {
	if limit == "" {
		return ""
	}
	return " of " + limit
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
