package cluster

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// MaxServiceLogSources bounds the Kubernetes requests made by one log viewer.
const MaxServiceLogSources = 20

const serviceLogLines = 5000

// LogSource identifies the pod and container that produced a line.
type LogSource struct {
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
	pod       *corev1.Pod
	restarts  int32
	started   string
}

// Key changes when a pod is replaced or its container restarts.
func (s LogSource) Key() string {
	return fmt.Sprintf("%s/%s/%s/%s/%d/%s", s.pod.UID, s.Namespace, s.Pod, s.Container, s.restarts, s.started)
}

// ServiceLogLine preserves the source when several containers share a view.
type ServiceLogLine struct {
	LogLine
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
}

// ServiceLogs is a bounded, timestamp-ordered read across a service's pods.
type ServiceLogs struct {
	Sources      []LogSource      `json:"sources"`
	Containers   []string         `json:"containers"`
	TotalSources int              `json:"totalSources"`
	Lines        []ServiceLogLine `json:"lines"`
	Notices      []string         `json:"notices"`
}

// ServicePods resolves through the scoped cache, never an arbitrary selector
// supplied by the browser. Operator-managed services can include several workloads.
func (c *Cache) ServicePods(id string) ([]*corev1.Pod, error) {
	snapshot := c.HealthSnapshot()
	var workloadIDs []string
	for _, s := range snapshot.Services {
		if s.ID == id {
			workloadIDs = s.Workloads
			break
		}
	}
	if workloadIDs == nil {
		return nil, ErrNotFound
	}
	pods := []*corev1.Pod{}
	seen := map[string]bool{}
	for _, w := range snapshot.Workloads {
		if !slices.Contains(workloadIDs, w.ID) {
			continue
		}
		for _, ref := range w.Pods {
			key := ref.Namespace + "/" + ref.Name
			if seen[key] {
				continue
			}
			seen[key] = true
			if pod, err := c.Pod(ref.Namespace, ref.Name); err == nil {
				pods = append(pods, pod)
			}
		}
	}
	return pods, nil
}

// ServiceLogSources lists containers deterministically and makes truncation
// explicit. Init containers are included so failed migrations are visible too.
func ServiceLogSources(pods []*corev1.Pod, container string) (ServiceLogs, error) {
	out := ServiceLogs{Sources: []LogSource{}, Containers: []string{}, Lines: []ServiceLogLine{}, Notices: []string{}}
	for _, pod := range pods {
		containers := append(slices.Clone(pod.Spec.Containers), pod.Spec.InitContainers...)
		statuses := append(slices.Clone(pod.Status.ContainerStatuses), pod.Status.InitContainerStatuses...)
		for _, ct := range containers {
			if !slices.Contains(out.Containers, ct.Name) {
				out.Containers = append(out.Containers, ct.Name)
			}
			if container != "" && container != ct.Name {
				continue
			}
			source := LogSource{Namespace: pod.Namespace, Pod: pod.Name, Container: ct.Name, pod: pod}
			for _, status := range statuses {
				if status.Name == ct.Name {
					source.restarts = status.RestartCount
					if status.State.Running != nil {
						source.started = status.State.Running.StartedAt.String()
					} else if status.State.Terminated != nil {
						source.started = status.State.Terminated.StartedAt.String()
					}
				}
			}
			out.Sources = append(out.Sources, source)
		}
	}
	if container != "" && len(pods) > 0 && len(out.Sources) == 0 {
		return out, ErrNotFound
	}
	slices.Sort(out.Containers)
	slices.SortFunc(out.Sources, func(a, b LogSource) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Pod, b.Pod), cmp.Compare(a.Container, b.Container))
	})
	out.TotalSources = len(out.Sources)
	if len(out.Sources) > MaxServiceLogSources {
		out.Sources = out.Sources[:MaxServiceLogSources]
		out.Notices = append(out.Notices, fmt.Sprintf("Showing %d of %d containers. Choose a pod or container to narrow the view.", MaxServiceLogSources, out.TotalSources))
	}
	return out, nil
}

// ReadServiceLogs reads at most four containers concurrently, sharing a 4 MiB
// history budget. A missing/unstarted container doesn't hide other pods' logs.
func ReadServiceLogs(ctx context.Context, cs kubernetes.Interface, pods []*corev1.Pod, req LogRequest) (ServiceLogs, error) {
	out, err := ServiceLogSources(pods, req.Container)
	if err != nil || len(out.Sources) == 0 {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req.LimitBytes = (4 << 20) / int64(len(out.Sources))
	results := make([]PodLogs, len(out.Sources))
	errors := make([]error, len(out.Sources))
	jobs := make(chan int, len(out.Sources))
	for i := range out.Sources {
		jobs <- i
	}
	close(jobs)
	var wg sync.WaitGroup
	for range min(4, len(out.Sources)) {
		wg.Go(func() {
			for i := range jobs {
				source := out.Sources[i]
				request := req
				request.Container = source.Container
				results[i], errors[i] = ReadLogs(ctx, cs, source.pod, request)
			}
		})
	}
	wg.Wait()
	for i, result := range results {
		source := out.Sources[i]
		notice := result.Notice
		if errors[i] != nil {
			notice = errors[i].Error()
		}
		if notice != "" {
			out.Notices = append(out.Notices, source.Pod+"/"+source.Container+": "+notice)
		}
		for _, line := range result.Lines {
			out.Lines = append(out.Lines, source.line(line))
		}
	}
	slices.SortStableFunc(out.Lines, func(a, b ServiceLogLine) int { return cmp.Compare(a.Timestamp, b.Timestamp) })
	if len(out.Lines) > serviceLogLines {
		out.Lines = out.Lines[len(out.Lines)-serviceLogLines:]
		out.Notices = append(out.Notices, "Showing the most recent 5000 lines.")
	}
	return out, nil
}

func (s LogSource) line(line LogLine) ServiceLogLine {
	return ServiceLogLine{LogLine: line, Namespace: s.Namespace, Pod: s.Pod, Container: s.Container}
}

// FollowSourceLogs tags every line without changing the existing pod-log API.
func FollowSourceLogs(ctx context.Context, cs kubernetes.Interface, source LogSource, req LogRequest, emit func(ServiceLogLine) error) error {
	req.Container = source.Container
	return FollowLogs(ctx, cs, source.pod, req, func(line LogLine) error { return emit(source.line(line)) })
}
