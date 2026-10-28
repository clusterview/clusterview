package cluster

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

func requests(cpu, mem string) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{Requests: corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem),
	}}
}

func TestPodRequests(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	cases := []struct {
		name     string
		spec     corev1.PodSpec
		cpu, mem int64
	}{
		{
			name: "main containers add up",
			spec: corev1.PodSpec{Containers: []corev1.Container{{Resources: requests("250m", "128Mi")}, {Resources: requests("1", "1Gi")}}},
			cpu:  1250, mem: 128<<20 + 1<<30,
		},
		{
			name: "a big init container sets the peak",
			spec: corev1.PodSpec{
				InitContainers: []corev1.Container{{Resources: requests("2", "64Mi")}},
				Containers:     []corev1.Container{{Resources: requests("500m", "256Mi")}},
			},
			cpu: 2000, mem: 256 << 20,
		},
		{
			name: "sidecars run alongside the main containers",
			spec: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{Resources: requests("100m", "32Mi"), RestartPolicy: &always},
					{Resources: requests("300m", "16Mi")},
				},
				Containers: []corev1.Container{{Resources: requests("200m", "64Mi")}},
			},
			// init peak: sidecar 100 + plain init 300 = 400 > main 200 + sidecar 100
			cpu: 400, mem: 96 << 20,
		},
		{
			name: "overhead is added",
			spec: corev1.PodSpec{
				Containers: []corev1.Container{{Resources: requests("100m", "10Mi")}},
				Overhead:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m")},
			},
			cpu: 150, mem: 10 << 20,
		},
	}
	for _, c := range cases {
		cpu, mem := podRequests(&c.spec)
		if cpu != c.cpu || mem != c.mem {
			t.Errorf("%s: got %dm %d bytes, want %dm %d bytes", c.name, cpu, mem, c.cpu, c.mem)
		}
	}
}

func TestWorkloadName(t *testing.T) {
	labelled := pod("a", "x", nil)
	labelled.Labels = map[string]string{"app.kubernetes.io/name": "payments"}
	fromRS := pod("a", "x", controller("ReplicaSet", "api-gateway-7d9c8b6f5", "u"))
	controllerNamed := pod("a", "x", controller("ReplicaSet", "controller", "u"))
	fromSTS := pod("a", "x", controller("StatefulSet", "rabbitmq-server", "u"))
	cases := map[*corev1.Pod]string{
		labelled:                "payments",
		fromRS:                  "api-gateway",
		controllerNamed:         "controller", // has vowels, so it isn't a template hash
		fromSTS:                 "rabbitmq-server",
		pod("a", "lonely", nil): "lonely",
	}
	for p, want := range cases {
		if got := workloadName(p); got != want {
			t.Errorf("%s: got %q, want %q", p.Name, got, want)
		}
	}
}

func TestBuildNodes(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "ip-10-0-1-5", Labels: map[string]string{
			"node.kubernetes.io/instance-type": "m7g.large",
			"topology.kubernetes.io/zone":      "us-west-1a",
			"karpenter.sh/nodepool":            "general",
		}},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("1930m"), corev1.ResourceMemory: resource.MustParse("7Gi"), corev1.ResourcePods: resource.MustParse("29"),
			},
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
				{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue},
				{Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse},
			},
		},
	}
	api := pod("shop", "api-7d9c8b6f5-aaaaa", controller("ReplicaSet", "api-7d9c8b6f5", "rs1"))
	api.Spec.NodeName = "ip-10-0-1-5"
	api.Spec.Containers[0].Resources = requests("500m", "512Mi")
	leaving := pod("shop", "old-1", nil)
	leaving.Spec.NodeName = "ip-10-0-1-5"
	leaving.DeletionTimestamp = &metav1.Time{Time: now}
	waitingPod := pod("shop", "big", nil, func(p *corev1.Pod) { p.Status.Phase = corev1.PodPending })

	got := BuildNodes(NodeObjects{
		Nodes:       []*corev1.Node{node},
		Pods:        []*corev1.Pod{api, leaving, waitingPod},
		ReplicaSets: []*appsv1.ReplicaSet{replicaSet("shop", "api-7d9c8b6f5", "api", "rs1")},
		NodeMetrics: []metricsv1beta1.NodeMetrics{{
			ObjectMeta: metav1.ObjectMeta{Name: "ip-10-0-1-5"},
			Usage:      corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("123456789n"), corev1.ResourceMemory: resource.MustParse("2Gi")},
		}},
		PodMetrics: []metricsv1beta1.PodMetrics{{
			ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "api-7d9c8b6f5-aaaaa"},
			Containers: []metricsv1beta1.ContainerMetrics{{Usage: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("20m"), corev1.ResourceMemory: resource.MustParse("100Mi")}}},
		}},
		HasMetrics: true,
	}, now)

	if len(got.Nodes) != 1 || len(got.Unscheduled) != 1 || !got.Metrics {
		t.Fatalf("got %+v", got)
	}
	n := got.Nodes[0]
	if !n.Ready || n.InstanceType != "m7g.large" || n.Zone != "us-west-1a" || n.Pool != "general" {
		t.Errorf("node labels: %+v", n)
	}
	if len(n.Pressure) != 1 || n.Pressure[0] != "MemoryPressure" {
		t.Errorf("pressure %v", n.Pressure)
	}
	if n.CPUAllocatable != 1930 || n.PodsAllocatable != 29 || n.CPURequested != 500 || n.MemRequested != 512<<20 {
		t.Errorf("figures: %+v", n)
	}
	if n.CPUUsage == nil || *n.CPUUsage != 124 { // MilliValue rounds up
		t.Errorf("node cpu usage %v", n.CPUUsage)
	}
	if len(n.Pods) != 2 {
		t.Fatalf("a terminating pod still holds its slot: %d pods", len(n.Pods))
	}
	first := n.Pods[0]
	if first.WorkloadID != "Deployment/shop/api" || first.Workload != "api" || first.CPUUsage == nil || *first.CPUUsage != 20 {
		t.Errorf("api pod: %+v", first)
	}
	if n.Pods[1].Reason != "Terminating" {
		t.Errorf("leaving pod: %+v", n.Pods[1])
	}
}

func TestBuildNodesWithoutMetrics(t *testing.T) {
	got := BuildNodes(NodeObjects{Nodes: []*corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "n"}}}}, now)
	if got.Metrics || got.Nodes[0].CPUUsage != nil || got.Nodes[0].Pods == nil {
		t.Errorf("got %+v", got)
	}
}
