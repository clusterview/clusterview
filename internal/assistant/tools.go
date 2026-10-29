package assistant

import (
	"context"
	"errors"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/clusterview/clusterview/internal/cluster"
)

func (a *Assistant) runTool(ctx context.Context, snap cluster.Snapshot, t Tool, logs *int) (any, error) {
	if len(t.Target) > 512 || len(t.Namespace) > 253 || len(t.Container) > 253 {
		return nil, errors.New("invalid tool arguments")
	}
	switch t.Name {
	case "find_services":
		return findServices(snap, t.Target), nil
	case "service":
		return serviceDetails(snap, t.Target)
	case "node":
		if !a.src.NodesEnabled() {
			return nil, errors.New("node visibility is disabled (scope.nodes)")
		}
		data := a.src.Nodes()
		for _, node := range data.Nodes {
			if node.Name == t.Target {
				total := len(node.Pods)
				node.Pods = node.Pods[:min(40, total)]
				return map[string]any{"metricsAvailable": data.Metrics, "fetchedAt": data.FetchedAt, "node": node, "totalScopedPods": total, "note": "Pod placement is namespace-scoped; node capacity and usage are machine-wide. Pod list capped at 40; no historical metrics available."}, nil
			}
		}
		return nil, cluster.ErrNotFound
	case "pod", "logs":
		// Look up only cached, watched pods. Never accept an arbitrary API path.
		pod, err := a.src.Pod(t.Namespace, t.Target)
		if err != nil {
			return nil, cluster.ErrNotFound
		}
		if t.Name == "pod" {
			return podDetails(pod), nil
		}
		(*logs)++
		if *logs > 3 {
			return nil, errors.New("log budget reached (3 targeted tails per turn)")
		}
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		data, err := cluster.ReadLogs(ctx, a.src.Clientset(), pod, cluster.LogRequest{Container: t.Container, Previous: t.Previous, TailLines: 80, LimitBytes: 12 << 10})
		if err != nil {
			return nil, errors.New("pod logs unavailable; pod/container may have changed or access is denied")
		}
		return data, nil
	default:
		return nil, errors.New("tool is not allowed; only find_services, service, node, pod and logs are available")
	}
}
func podDetails(pod *corev1.Pod) any {
	containers := []map[string]any{}
	all := append(slices.Clone(pod.Spec.InitContainers), pod.Spec.Containers...)
	for _, c := range all[:min(10, len(all))] {
		envNames := []string{}
		for _, env := range c.Env[:min(30, len(c.Env))] {
			envNames = append(envNames, env.Name)
		}
		containers = append(containers, map[string]any{"name": c.Name, "image": c.Image, "resources": c.Resources, "ports": c.Ports, "environmentNamesOnly": envNames, "readiness": probeDetails(c.ReadinessProbe), "liveness": probeDetails(c.LivenessProbe), "startup": probeDetails(c.StartupProbe)})
	}
	statuses := append(slices.Clone(pod.Status.InitContainerStatuses), pod.Status.ContainerStatuses...)
	health := []map[string]any{}
	for _, s := range statuses[:min(10, len(statuses))] {
		item := map[string]any{"container": s.Name, "ready": s.Ready, "restarts": s.RestartCount}
		if s.State.Waiting != nil {
			item["waiting"] = s.State.Waiting.Reason
			item["message"] = clip(s.State.Waiting.Message, 500)
		}
		if s.State.Terminated != nil {
			item["exitCode"] = s.State.Terminated.ExitCode
			item["reason"] = s.State.Terminated.Reason
		}
		if s.LastTerminationState.Terminated != nil {
			item["lastExitCode"] = s.LastTerminationState.Terminated.ExitCode
			item["lastReason"] = s.LastTerminationState.Terminated.Reason
		}
		health = append(health, item)
	}
	return map[string]any{"namespace": pod.Namespace, "pod": pod.Name, "node": pod.Spec.NodeName, "phase": pod.Status.Phase, "containers": containers, "health": health, "note": "At most 10 containers; environment names only, no values, secrets, headers, commands, args or annotations."}
}
func probeDetails(p *corev1.Probe) any {
	if p == nil {
		return nil
	}
	out := map[string]any{"periodSeconds": p.PeriodSeconds, "timeoutSeconds": p.TimeoutSeconds, "failureThreshold": p.FailureThreshold, "initialDelaySeconds": p.InitialDelaySeconds}
	switch {
	case p.HTTPGet != nil:
		out["httpPath"] = clip(p.HTTPGet.Path, 200)
		out["port"] = p.HTTPGet.Port
	case p.TCPSocket != nil:
		out["tcpPort"] = p.TCPSocket.Port
	case p.GRPC != nil:
		out["grpcPort"] = p.GRPC.Port
	case p.Exec != nil:
		out["type"] = "exec (command omitted)"
	}
	return out
}
