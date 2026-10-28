// Package diagnose explains why a service is broken: it gathers what
// Kubernetes knows about it, removes secrets, and asks the model.
package diagnose

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/clusterview/clusterview/internal/ai"
	"github.com/clusterview/clusterview/internal/cluster"
)

// Limits on what one diagnosis sends, so a chatty service can't make it
// slow or expensive.
const (
	maxPods       = 4  // pods described
	maxLogPods    = 2  // pods whose logs are read
	logTail       = 80 // lines per log
	maxEvents     = 15
	maxValueChars = 160
	maxLogLine    = 400
)

// Source is what the evidence is read from: the cache in production.
type Source interface {
	Snapshot() cluster.Snapshot
	Pod(namespace, name string) (*corev1.Pod, error)
	Clientset() kubernetes.Interface
}

// gather writes everything known about a service as plain text for the
// model, then removes secrets from it.
func gather(ctx context.Context, src Source, snap cluster.Snapshot, svc cluster.Service) (string, int) {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	workloads := members(snap, svc)
	w("Service: %s (Kubernetes name %s) in namespace %s\n", svc.Title, svc.Name, svc.Namespace)
	if svc.ManagedBy != nil {
		w("Run by an operator through %s %s (%s)\n", svc.ManagedBy.Kind, svc.ManagedBy.Name, svc.ManagedBy.APIVersion)
	}
	w("Status: %s, %d of %d ready\n", svc.Status, svc.Ready, svc.Desired)

	if len(svc.Problems) > 0 {
		w("\n## Problems clusterview detected\n")
		for _, p := range svc.Problems {
			w("- [%s] %s", p.Severity, p.Title)
			if p.Detail != "" {
				w(": %s", p.Detail)
			}
			if len(p.Pods) > 0 {
				w(" (pods: %s)", strings.Join(p.Pods, ", "))
			}
			w("\n")
		}
	}

	w("\n## Workloads\n")
	var pods []cluster.PodState
	var events []cluster.Event
	for _, wl := range workloads {
		w("- %s %s: %s, desired %d, ready %d, available %d, updated %d", wl.Kind, wl.Name, wl.Status, wl.Desired, wl.Ready, wl.Available, wl.Updated)
		if wl.Image != "" {
			w(", image %s", wl.Image)
		}
		if wl.Schedule != "" {
			w(", schedule %q", wl.Schedule)
			if wl.Suspended {
				w(" (suspended)")
			}
		}
		if wl.Autoscaling != nil {
			w(", autoscaled %d–%d by %s", wl.Autoscaling.Min, wl.Autoscaling.Max, wl.Autoscaling.ManagedBy)
		}
		w("\n")
		if wl.Message != "" {
			w("  Kubernetes says: %s\n", wl.Message)
		}
		pods = append(pods, wl.Pods...)
		events = append(events, wl.Events...)
	}

	chosen := pickPods(pods)
	if len(chosen) > 0 {
		w("\n## Pods (%d of %d, the unhealthy ones first)\n", len(chosen), len(pods))
	}
	var objects []*corev1.Pod
	for _, ps := range chosen {
		w("- %s: phase %s, ready %s", ps.Name, ps.Phase, ps.ReadyContainers)
		if ps.Reason != "" {
			w(", %s", ps.Reason)
		}
		w(", %d restarts", ps.Restarts)
		if ps.Node != "" {
			w(", on node %s", ps.Node)
		}
		w("\n")
		pod, err := src.Pod(ps.Namespace, ps.Name)
		if err != nil {
			continue
		}
		objects = append(objects, pod)
		writeContainerStates(&b, pod)
	}

	if len(objects) > 0 {
		w("\n## Spec of %s\n", objects[0].Name)
		writeSpec(&b, objects[0])
	}

	if len(events) > 0 {
		slices.SortFunc(events, func(a, b cluster.Event) int { return b.LastSeen.Compare(a.LastSeen) })
		w("\n## Recent Kubernetes warnings, newest first\n")
		for _, e := range events[:min(len(events), maxEvents)] {
			w("- %s on %s, %d× (last %s ago): %s\n", e.Reason, e.Object, e.Count, since(e.LastSeen), e.Message)
		}
	}

	for _, pod := range objects[:min(len(objects), maxLogPods)] {
		writeLogs(ctx, &b, src.Clientset(), pod)
	}
	return ai.Redact(b.String())
}

// members are the service's workloads.
func members(snap cluster.Snapshot, svc cluster.Service) []cluster.Workload {
	byID := make(map[string]cluster.Workload, len(snap.Workloads))
	for _, wl := range snap.Workloads {
		byID[wl.ID] = wl
	}
	var out []cluster.Workload
	for _, id := range svc.Workloads {
		if wl, ok := byID[id]; ok {
			out = append(out, wl)
		}
	}
	return out
}

// pickPods puts stuck pods first, then those not ready, then the rest.
func pickPods(pods []cluster.PodState) []cluster.PodState {
	rank := func(p cluster.PodState) int {
		switch {
		case p.Stuck:
			return 0
		case !p.Ready:
			return 1
		}
		return 2
	}
	sorted := slices.Clone(pods)
	slices.SortStableFunc(sorted, func(a, b cluster.PodState) int { return rank(a) - rank(b) })
	return sorted[:min(len(sorted), maxPods)]
}

func writeContainerStates(b *strings.Builder, pod *corev1.Pod) {
	statuses := append(slices.Clone(pod.Status.InitContainerStatuses), pod.Status.ContainerStatuses...)
	for _, cs := range statuses {
		fmt.Fprintf(b, "  container %s: ready %t, %d restarts", cs.Name, cs.Ready, cs.RestartCount)
		switch s := cs.State; {
		case s.Waiting != nil:
			fmt.Fprintf(b, ", waiting: %s", s.Waiting.Reason)
			if s.Waiting.Message != "" {
				fmt.Fprintf(b, " (%s)", oneLine(s.Waiting.Message, maxLogLine))
			}
		case s.Terminated != nil:
			fmt.Fprintf(b, ", terminated: %s, exit code %d", s.Terminated.Reason, s.Terminated.ExitCode)
		case s.Running != nil:
			fmt.Fprintf(b, ", running since %s", s.Running.StartedAt.UTC().Format(time.RFC3339))
		}
		if t := cs.LastTerminationState.Terminated; t != nil {
			fmt.Fprintf(b, "; last run ended: %s, exit code %d", t.Reason, t.ExitCode)
			if t.Signal != 0 {
				fmt.Fprintf(b, ", signal %d", t.Signal)
			}
			if t.Message != "" {
				fmt.Fprintf(b, " (%s)", oneLine(t.Message, maxLogLine))
			}
			if !t.StartedAt.IsZero() && !t.FinishedAt.IsZero() {
				fmt.Fprintf(b, ", after running %s", t.FinishedAt.Sub(t.StartedAt.Time).Round(time.Second))
			}
		}
		b.WriteString("\n")
	}
	for _, c := range pod.Status.Conditions {
		if c.Status != corev1.ConditionTrue && c.Message != "" {
			fmt.Fprintf(b, "  condition %s is %s: %s\n", c.Type, c.Status, oneLine(c.Message, maxLogLine))
		}
	}
}

// writeSpec summarises the pod's containers: what they run, their limits,
// probes, and where their configuration comes from. Values of variables
// with secret-sounding names are left out entirely.
func writeSpec(b *strings.Builder, pod *corev1.Pod) {
	containers := append(slices.Clone(pod.Spec.InitContainers), pod.Spec.Containers...)
	for i, c := range containers {
		kind := "container"
		if i < len(pod.Spec.InitContainers) {
			kind = "init container"
		}
		fmt.Fprintf(b, "- %s %s: image %s\n", kind, c.Name, c.Image)
		if len(c.Command) > 0 {
			fmt.Fprintf(b, "  command: %s\n", oneLine(strings.Join(c.Command, " "), maxLogLine))
		}
		if len(c.Args) > 0 {
			fmt.Fprintf(b, "  args: %s\n", oneLine(strings.Join(c.Args, " "), maxLogLine))
		}
		var ports []string
		for _, p := range c.Ports {
			ports = append(ports, fmt.Sprintf("%d/%s", p.ContainerPort, p.Protocol))
		}
		if len(ports) > 0 {
			fmt.Fprintf(b, "  ports: %s\n", strings.Join(ports, ", "))
		}
		if r := resources(c.Resources); r != "" {
			fmt.Fprintf(b, "  resources: %s\n", r)
		}
		for _, p := range []struct {
			name  string
			probe *corev1.Probe
		}{{"startup", c.StartupProbe}, {"liveness", c.LivenessProbe}, {"readiness", c.ReadinessProbe}} {
			if p.probe != nil {
				fmt.Fprintf(b, "  %s probe: %s\n", p.name, probe(p.probe))
			}
		}
		for _, e := range c.Env {
			fmt.Fprintf(b, "  env %s = %s\n", e.Name, envValue(e))
		}
		for _, f := range c.EnvFrom {
			switch {
			case f.ConfigMapRef != nil:
				fmt.Fprintf(b, "  env from ConfigMap %s%s\n", f.ConfigMapRef.Name, optional(f.ConfigMapRef.Optional))
			case f.SecretRef != nil:
				fmt.Fprintf(b, "  env from Secret %s%s\n", f.SecretRef.Name, optional(f.SecretRef.Optional))
			}
		}
		for _, m := range c.VolumeMounts {
			ro := ""
			if m.ReadOnly {
				ro = " (read-only)"
			}
			fmt.Fprintf(b, "  mounts %s at %s%s\n", volumeSource(pod, m.Name), m.MountPath, ro)
		}
	}
	if pod.Spec.ServiceAccountName != "" {
		fmt.Fprintf(b, "- service account: %s\n", pod.Spec.ServiceAccountName)
	}
	if len(pod.Spec.NodeSelector) > 0 {
		fmt.Fprintf(b, "- node selector: %v\n", pod.Spec.NodeSelector)
	}
	if len(pod.Spec.ImagePullSecrets) > 0 {
		var names []string
		for _, s := range pod.Spec.ImagePullSecrets {
			names = append(names, s.Name)
		}
		fmt.Fprintf(b, "- image pull secrets: %s\n", strings.Join(names, ", "))
	}
}

func resources(r corev1.ResourceRequirements) string {
	var parts []string
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		if q, ok := r.Requests[name]; ok {
			parts = append(parts, fmt.Sprintf("%s request %s", name, q.String()))
		}
		if q, ok := r.Limits[name]; ok {
			parts = append(parts, fmt.Sprintf("%s limit %s", name, q.String()))
		}
	}
	return strings.Join(parts, ", ")
}

func probe(p *corev1.Probe) string {
	var what string
	switch h := p.ProbeHandler; {
	case h.HTTPGet != nil:
		what = fmt.Sprintf("HTTP GET %s on port %s", h.HTTPGet.Path, h.HTTPGet.Port.String())
	case h.TCPSocket != nil:
		what = fmt.Sprintf("TCP port %s", h.TCPSocket.Port.String())
	case h.Exec != nil:
		what = "runs " + oneLine(strings.Join(h.Exec.Command, " "), maxValueChars)
	case h.GRPC != nil:
		what = fmt.Sprintf("gRPC port %d", h.GRPC.Port)
	}
	return fmt.Sprintf("%s, starts after %ds, every %ds, times out after %ds, fails after %d tries",
		what, p.InitialDelaySeconds, max(p.PeriodSeconds, 10), max(p.TimeoutSeconds, 1), max(p.FailureThreshold, 3))
}

func envValue(e corev1.EnvVar) string {
	if f := e.ValueFrom; f != nil {
		switch {
		case f.SecretKeyRef != nil:
			return fmt.Sprintf("from Secret %s, key %s%s", f.SecretKeyRef.Name, f.SecretKeyRef.Key, optional(f.SecretKeyRef.Optional))
		case f.ConfigMapKeyRef != nil:
			return fmt.Sprintf("from ConfigMap %s, key %s%s", f.ConfigMapKeyRef.Name, f.ConfigMapKeyRef.Key, optional(f.ConfigMapKeyRef.Optional))
		case f.FieldRef != nil:
			return "from the pod's " + f.FieldRef.FieldPath
		case f.ResourceFieldRef != nil:
			return "from the container's " + f.ResourceFieldRef.Resource
		}
		return "from elsewhere"
	}
	if ai.SecretName(e.Name) {
		return "(set; value hidden)"
	}
	return fmt.Sprintf("%q", oneLine(e.Value, maxValueChars))
}

func optional(o *bool) string {
	if o != nil && *o {
		return " (optional)"
	}
	return ""
}

func volumeSource(pod *corev1.Pod, name string) string {
	for _, v := range pod.Spec.Volumes {
		if v.Name != name {
			continue
		}
		switch s := v.VolumeSource; {
		case s.ConfigMap != nil:
			return "ConfigMap " + s.ConfigMap.Name
		case s.Secret != nil:
			return "Secret " + s.Secret.SecretName
		case s.PersistentVolumeClaim != nil:
			return "volume claim " + s.PersistentVolumeClaim.ClaimName
		case s.EmptyDir != nil:
			return "empty dir " + name
		case s.Projected != nil:
			return "projected volume " + name
		case s.HostPath != nil:
			return "host path " + s.HostPath.Path
		}
		return "volume " + name
	}
	return "volume " + name
}

// writeLogs adds the end of the log of the container most likely to explain
// the pod: from before its last restart too, when it has restarted.
func writeLogs(ctx context.Context, b *strings.Builder, cs kubernetes.Interface, pod *corev1.Pod) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, previous := range []bool{true, false} {
		logs, err := cluster.ReadLogs(ctx, cs, pod, cluster.LogRequest{Previous: previous, TailLines: logTail})
		if err != nil || (len(logs.Lines) == 0 && previous) {
			continue
		}
		which := "now"
		if previous {
			which = "before its last restart"
		}
		fmt.Fprintf(b, "\n## Log of %s, container %s, %s (last %d lines)\n", pod.Name, logs.Container, which, logTail)
		if len(logs.Lines) == 0 {
			note := logs.Notice
			if note == "" {
				note = "empty"
			}
			fmt.Fprintf(b, "(%s)\n", note)
			continue
		}
		b.WriteString("<log>\n")
		for _, l := range logs.Lines {
			b.WriteString(oneLine(l.Text, maxLogLine))
			b.WriteString("\n")
		}
		b.WriteString("</log>\n")
	}
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func since(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	if d < time.Minute {
		return d.String()
	}
	return d.Round(time.Minute).String()
}
