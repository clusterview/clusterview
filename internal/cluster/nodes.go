package cluster

import (
	"cmp"
	"regexp"
	"slices"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

// NodeObjects is what BuildNodes needs: nodes, every pod, and metrics-server's
// figures when it's installed (nil when it isn't).
type NodeObjects struct {
	Nodes []*corev1.Node
	Pods  []*corev1.Pod
	// ReplicaSets and Jobs link each pod to its workload.
	ReplicaSets []*appsv1.ReplicaSet
	Jobs        []*batchv1.Job
	NodeMetrics []metricsv1beta1.NodeMetrics
	PodMetrics  []metricsv1beta1.PodMetrics
	HasMetrics  bool
}

// Where managed offerings and autoscalers write a node's pool name.
var poolLabels = []string{
	"eks.amazonaws.com/nodegroup",
	"karpenter.sh/nodepool",
	"alpha.eksctl.io/nodegroup-name",
	"cloud.google.com/gke-nodepool",
	"kubernetes.azure.com/agentpool",
	"agentpool",
}

var pressureConditions = []corev1.NodeConditionType{
	corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure, corev1.NodeNetworkUnavailable,
}

// A ReplicaSet is named <deployment>-<pod-template-hash>, and the hash uses
// Kubernetes' vowel-free alphabet.
var templateHash = regexp.MustCompile(`-[bcdfghjklmnpqrstvwxz2456789]{6,10}$`)

// BuildNodes lays out every node with its pods and how full it is.
func BuildNodes(objs NodeObjects, now time.Time) Nodes {
	own := newOwners(objs.ReplicaSets, objs.Jobs)

	type key struct{ ns, name string }
	podUsage := map[key][2]int64{}
	for _, m := range objs.PodMetrics {
		var cpu, mem int64
		for _, c := range m.Containers {
			cpu += c.Usage.Cpu().MilliValue()
			mem += c.Usage.Memory().Value()
		}
		podUsage[key{m.Namespace, m.Name}] = [2]int64{cpu, mem}
	}
	nodeUsage := map[string][2]int64{}
	for _, m := range objs.NodeMetrics {
		nodeUsage[m.Name] = [2]int64{m.Usage.Cpu().MilliValue(), m.Usage.Memory().Value()}
	}

	byNode := map[string][]NodePod{}
	unscheduled := []NodePod{}
	for _, p := range objs.Pods {
		// A terminating pod stays: until its grace period ends it still holds
		// its requests and a pod slot, which is often why a replacement waits.
		np := nodePod(p, own)
		if u, ok := podUsage[key{p.Namespace, p.Name}]; ok {
			np.CPUUsage, np.MemUsage = &u[0], &u[1]
		}
		switch {
		case p.Spec.NodeName != "":
			byNode[p.Spec.NodeName] = append(byNode[p.Spec.NodeName], np)
		case np.Phase == string(corev1.PodPending) && !np.Terminating:
			unscheduled = append(unscheduled, np)
		}
	}

	podOrder := func(a, b NodePod) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	}
	nodes := make([]Node, 0, len(objs.Nodes))
	for _, n := range objs.Nodes {
		pods := byNode[n.Name]
		if pods == nil {
			pods = []NodePod{}
		}
		slices.SortFunc(pods, podOrder)
		conds := map[corev1.NodeConditionType]corev1.ConditionStatus{}
		for _, c := range n.Status.Conditions {
			conds[c.Type] = c.Status
		}
		pressure := []string{}
		for _, c := range pressureConditions {
			if conds[c] == corev1.ConditionTrue {
				pressure = append(pressure, string(c))
			}
		}
		alloc := n.Status.Allocatable
		node := Node{
			Name:            n.Name,
			Ready:           conds[corev1.NodeReady] == corev1.ConditionTrue,
			Unschedulable:   n.Spec.Unschedulable,
			Pressure:        pressure,
			InstanceType:    firstLabel(n.Labels, "node.kubernetes.io/instance-type", "beta.kubernetes.io/instance-type"),
			Zone:            firstLabel(n.Labels, "topology.kubernetes.io/zone", "failure-domain.beta.kubernetes.io/zone"),
			Pool:            firstLabel(n.Labels, poolLabels...),
			KubeletVersion:  n.Status.NodeInfo.KubeletVersion,
			CreatedAt:       n.CreationTimestamp.Time,
			CPUAllocatable:  alloc.Cpu().MilliValue(),
			MemAllocatable:  alloc.Memory().Value(),
			PodsAllocatable: alloc.Pods().Value(),
			Pods:            pods,
		}
		for _, p := range pods {
			node.CPURequested += p.CPURequest
			node.MemRequested += p.MemRequest
		}
		if u, ok := nodeUsage[n.Name]; ok {
			node.CPUUsage, node.MemUsage = &u[0], &u[1]
		}
		nodes = append(nodes, node)
	}
	slices.SortFunc(nodes, func(a, b Node) int { return cmp.Compare(a.Name, b.Name) })
	slices.SortFunc(unscheduled, podOrder)

	return Nodes{FetchedAt: now, Metrics: objs.HasMetrics, Nodes: nodes, Unscheduled: unscheduled}
}

func nodePod(p *corev1.Pod, own owners) NodePod {
	s := podState(p)
	cpu, mem := podRequests(&p.Spec)
	np := NodePod{
		Name:        p.Name,
		Namespace:   p.Namespace,
		WorkloadID:  own.workloadOf(p),
		Workload:    workloadName(p),
		Phase:       s.Phase,
		Ready:       s.Ready,
		Terminating: p.DeletionTimestamp != nil,
		Reason:      s.Reason,
		Restarts:    s.Restarts,
		CPURequest:  cpu,
		MemRequest:  mem,
	}
	if np.Terminating {
		np.Reason = "Terminating"
	}
	return np
}

// workloadName is a short human name for whatever runs the pod.
func workloadName(p *corev1.Pod) string {
	for _, k := range []string{"app.kubernetes.io/name", "app", "k8s-app", "app.kubernetes.io/component"} {
		if v := p.Labels[k]; v != "" {
			return v
		}
	}
	if len(p.OwnerReferences) > 0 && p.OwnerReferences[0].Name != "" {
		o := p.OwnerReferences[0]
		if o.Kind == "ReplicaSet" {
			return templateHash.ReplaceAllString(o.Name, "")
		}
		return o.Name
	}
	return p.Name
}

// podRequests is what the scheduler reserves for a pod.
//
// Init containers run one at a time, except sidecars (restartPolicy: Always),
// which start in order and keep running alongside everything after them. So
// the reservation is the larger of: the main containers plus every sidecar, or
// the peak while the init containers run (each plain init container plus the
// sidecars already started before it). Pod overhead is added on top.
func podRequests(spec *corev1.PodSpec) (cpu, mem int64) {
	of := func(c corev1.Container) (int64, int64) {
		return c.Resources.Requests.Cpu().MilliValue(), c.Resources.Requests.Memory().Value()
	}
	var sideCPU, sideMem, initCPU, initMem int64
	for _, c := range spec.InitContainers {
		ccpu, cmem := of(c)
		if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
			sideCPU += ccpu
			sideMem += cmem
			initCPU = max(initCPU, sideCPU)
			initMem = max(initMem, sideMem)
		} else {
			initCPU = max(initCPU, sideCPU+ccpu)
			initMem = max(initMem, sideMem+cmem)
		}
	}
	var mainCPU, mainMem int64
	for _, c := range spec.Containers {
		ccpu, cmem := of(c)
		mainCPU += ccpu
		mainMem += cmem
	}
	cpu = max(mainCPU+sideCPU, initCPU)
	mem = max(mainMem+sideMem, initMem)
	if o := spec.Overhead; o != nil {
		cpu += quantityOr(o, corev1.ResourceCPU).MilliValue()
		mem += quantityOr(o, corev1.ResourceMemory).Value()
	}
	return cpu, mem
}

func quantityOr(list corev1.ResourceList, name corev1.ResourceName) *resource.Quantity {
	if q, ok := list[name]; ok {
		return &q
	}
	return resource.NewQuantity(0, resource.DecimalSI)
}

func firstLabel(labels map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := labels[k]; v != "" {
			return v
		}
	}
	return ""
}
