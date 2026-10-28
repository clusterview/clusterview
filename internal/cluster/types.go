// Package cluster watches a Kubernetes cluster and turns what it sees into
// the dashboard's view: services made of workloads, their pods and health,
// nodes, and logs.
//
// The build functions (BuildSnapshot, BuildNodes) are pure, so they are tested
// on hand-made objects. Cache does the watching.
package cluster

import (
	"strings"
	"time"
)

// Status is a workload's health at a glance.
type Status string

// Workload statuses, from best to worst.
const (
	StatusHealthy  Status = "healthy"  // every wanted replica is ready
	StatusIdle     Status = "idle"     // scaled to zero on purpose
	StatusDegraded Status = "degraded" // some replicas ready, not all
	StatusDown     Status = "down"     // nothing ready, or a Job that failed
	StatusUnknown  Status = "unknown"
)

// Kind is the Kubernetes object behind a workload.
type Kind string

// Workload kinds.
const (
	KindDeployment  Kind = "Deployment"
	KindStatefulSet Kind = "StatefulSet"
	KindDaemonSet   Kind = "DaemonSet"
	KindJob         Kind = "Job"
	KindCronJob     Kind = "CronJob"
	// KindReplicaSet is a ReplicaSet no Deployment owns, such as one an Argo
	// Rollout runs.
	KindReplicaSet Kind = "ReplicaSet"
	// KindPod is a pod no workload owns: a bare pod, a control-plane static
	// pod, or one an operator runs directly (CloudNativePG does).
	KindPod Kind = "Pod"
)

// Snapshot is every service and workload in the cluster at one moment.
type Snapshot struct {
	Version   uint64     `json:"version"`
	FetchedAt time.Time  `json:"fetchedAt"`
	Summary   Summary    `json:"summary"`
	Services  []Service  `json:"services"`
	Links     []Link     `json:"links"`
	Workloads []Workload `json:"workloads"`
}

// Summary counts what needs attention. NeedAttention counts services.
type Summary struct {
	Services       int `json:"services"`
	Workloads      int `json:"workloads"`
	NeedAttention  int `json:"needAttention"`
	Pods           int `json:"pods"`
	ReadyPods      int `json:"readyPods"`
	Restarts       int `json:"restarts"`
	NamespaceCount int `json:"namespaces"`
}

// Service is one box on the map: a workload, or every workload and pod one
// operator-managed resource runs (a RabbitmqCluster's StatefulSet, a
// CloudNativePG Cluster's pods).
type Service struct {
	// ID is the workload's ID, or "<group>/<Kind>/<namespace>/<name>" for an
	// operator-managed resource.
	ID        string `json:"id"`
	Namespace string `json:"namespace"`
	// Name is the Kubernetes name; Title is what people call it.
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	// DescriptionSource says where the words came from: "annotation",
	// "operator" or "image". Empty when nothing describes it yet.
	DescriptionSource string   `json:"descriptionSource,omitempty"`
	Category          Category `json:"category"`
	App               string   `json:"app,omitempty"`
	ManagedBy         *Owner   `json:"managedBy,omitempty"`
	Status            Status   `json:"status"`
	Ready             int      `json:"ready"`
	Desired           int      `json:"desired"`
	Workloads         []string `json:"workloads"`
	// Problems is every member's problems, critical first.
	Problems []Problem `json:"problems"`
	// Addresses are the Kubernetes Services that reach it; Entrypoints the
	// Ingress rules that lead to it.
	Addresses   []Address    `json:"addresses"`
	Entrypoints []Entrypoint `json:"entrypoints"`
}

// Severity says how bad a problem is.
type Severity string

// Severities.
const (
	SeverityCritical Severity = "critical" // it isn't working
	SeverityWarning  Severity = "warning"  // it works, but something is off
)

// Problem is something wrong with a workload, in plain words, with the
// evidence Kubernetes gave.
type Problem struct {
	// Kind is a stable key, such as "crash-loop" or "image-pull".
	Kind     string   `json:"kind"`
	Severity Severity `json:"severity"`
	// Title says what's wrong in plain words: "Can't download its image".
	Title string `json:"title"`
	// Detail is the evidence, mostly in Kubernetes' own words.
	Detail string `json:"detail,omitempty"`
	// Pods are the affected pods, when the problem is with pods.
	Pods []string `json:"pods,omitempty"`
}

// Event is a recent Kubernetes warning about a workload or one of its pods.
type Event struct {
	Reason   string    `json:"reason"`
	Message  string    `json:"message"`
	Object   string    `json:"object"` // "Pod/web-1"
	Count    int       `json:"count"`
	LastSeen time.Time `json:"lastSeen"`
}

// Category is a rough kind of service, for icons and grouping.
type Category string

// Categories.
const (
	CategoryApp        Category = "app"
	CategoryDatabase   Category = "database"
	CategoryQueue      Category = "queue"
	CategoryCache      Category = "cache"
	CategorySearch     Category = "search"
	CategoryStorage    Category = "storage"
	CategoryIngress    Category = "ingress"
	CategoryNetwork    Category = "network"
	CategoryDNS        Category = "dns"
	CategoryMonitoring Category = "monitoring"
	CategorySecurity   Category = "security"
	CategoryPlatform   Category = "platform"
	CategoryJob        Category = "job"
)

// Owner is the custom resource whose operator runs a workload, read from the
// workload's owner reference.
type Owner struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
}

// Group is the owner's API group, such as "rabbitmq.com".
func (o Owner) Group() string {
	if g, _, ok := strings.Cut(o.APIVersion, "/"); ok {
		return g
	}
	return ""
}

// Workload is one Deployment, StatefulSet, DaemonSet, Job, CronJob, or a pod
// or ReplicaSet nothing else owns.
type Workload struct {
	// ID is "<kind>/<namespace>/<name>", stable across updates.
	ID        string    `json:"id"`
	Kind      Kind      `json:"kind"`
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Status    Status    `json:"status"`
	Desired   int       `json:"desired"`
	Ready     int       `json:"ready"`
	Available int       `json:"available"`
	Updated   int       `json:"updated"`
	Image     string    `json:"image,omitempty"`
	ImageTag  string    `json:"imageTag,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	// Message is the first failing condition, in Kubernetes' words.
	Message     string       `json:"message,omitempty"`
	Autoscaling *Autoscaling `json:"autoscaling,omitempty"`
	// Owner is set when an operator's custom resource runs this workload.
	Owner *Owner `json:"owner,omitempty"`
	// App groups services on the map: the app.kubernetes.io/part-of or
	// instance (Helm release) label.
	App string `json:"app,omitempty"`
	// Schedule, Suspended and LastRun are set for CronJobs.
	Schedule  string     `json:"schedule,omitempty"`
	Suspended bool       `json:"suspended,omitempty"`
	LastRun   *time.Time `json:"lastRun,omitempty"`
	Pods      []PodState `json:"pods"`
	Problems  []Problem  `json:"problems"`
	// Events are recent warnings about the workload or its pods, newest first.
	Events []Event `json:"events"`

	labels      map[string]string
	annotations map[string]string
	env         []envVar
}

// Autoscaling is the HPA (or KEDA's generated HPA) that sizes a workload.
type Autoscaling struct {
	Min       int    `json:"min"`
	Max       int    `json:"max"`
	Current   int    `json:"current"`
	ManagedBy string `json:"managedBy"` // "hpa" or "keda"
}

// PodState is one pod, reduced to what explains its health.
type PodState struct {
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	Node            string `json:"node,omitempty"`
	Phase           string `json:"phase"`
	Ready           bool   `json:"ready"`
	ReadyContainers string `json:"readyContainers"`
	Restarts        int    `json:"restarts"`
	// Reason is why it isn't running normally: CrashLoopBackOff,
	// Init:ImagePullBackOff, Unschedulable…
	Reason    string     `json:"reason,omitempty"`
	Stuck     bool       `json:"stuck"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
}

// Nodes is the cluster's machines and what runs on them.
type Nodes struct {
	FetchedAt time.Time `json:"fetchedAt"`
	// Metrics is false when metrics-server isn't installed; usage is then empty.
	Metrics bool   `json:"metrics"`
	Nodes   []Node `json:"nodes"`
	// Unscheduled pods are waiting for a node.
	Unscheduled []NodePod `json:"unscheduled"`
}

// Node is one machine.
type Node struct {
	Name            string    `json:"name"`
	Ready           bool      `json:"ready"`
	Unschedulable   bool      `json:"unschedulable"`
	Pressure        []string  `json:"pressure"`
	InstanceType    string    `json:"instanceType,omitempty"`
	Zone            string    `json:"zone,omitempty"`
	Pool            string    `json:"pool,omitempty"`
	KubeletVersion  string    `json:"kubeletVersion,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	CPUAllocatable  int64     `json:"cpuAllocatableMillicores"`
	MemAllocatable  int64     `json:"memoryAllocatableBytes"`
	PodsAllocatable int64     `json:"podsAllocatable"`
	CPURequested    int64     `json:"cpuRequestedMillicores"`
	MemRequested    int64     `json:"memoryRequestedBytes"`
	CPUUsage        *int64    `json:"cpuUsageMillicores,omitempty"`
	MemUsage        *int64    `json:"memoryUsageBytes,omitempty"`
	Pods            []NodePod `json:"pods"`
}

// NodePod is a pod as seen from its node.
type NodePod struct {
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	WorkloadID  string `json:"workloadId,omitempty"`
	Workload    string `json:"workload"`
	Phase       string `json:"phase"`
	Ready       bool   `json:"ready"`
	Terminating bool   `json:"terminating"`
	Reason      string `json:"reason,omitempty"`
	Restarts    int    `json:"restarts"`
	CPURequest  int64  `json:"cpuRequestMillicores"`
	MemRequest  int64  `json:"memoryRequestBytes"`
	CPUUsage    *int64 `json:"cpuUsageMillicores,omitempty"`
	MemUsage    *int64 `json:"memoryUsageBytes,omitempty"`
}

// PodLogs is one container's recent log.
type PodLogs struct {
	Namespace  string    `json:"namespace"`
	Pod        string    `json:"pod"`
	Container  string    `json:"container"`
	Containers []string  `json:"containers"`
	Previous   bool      `json:"previous"`
	Lines      []LogLine `json:"lines"`
	// Notice explains an empty log, such as "previous terminated container
	// not found".
	Notice string `json:"notice,omitempty"`
}

// LogLine is one line, with Kubernetes' timestamp in UTC when it had one.
type LogLine struct {
	Timestamp string `json:"timestamp,omitempty"`
	Text      string `json:"text"`
}
