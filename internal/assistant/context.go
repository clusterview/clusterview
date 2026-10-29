package assistant

import (
	"cmp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/clusterview/clusterview/internal/ai"
	"github.com/clusterview/clusterview/internal/cluster"
)

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "… [truncated]"
}
func makeEvidence(id, label string, data any) Evidence {
	raw, _ := ai.Redact(encode(data))
	label, _ = ai.Redact(label)
	return Evidence{ID: id, Label: label, Data: clip(raw, 12<<10)}
}
func briefService(s cluster.Service) map[string]any {
	problems := []map[string]string{}
	for _, p := range s.Problems[:min(3, len(s.Problems))] {
		problems = append(problems, map[string]string{"kind": p.Kind, "detail": clip(p.Detail, 350)})
	}
	return map[string]any{"id": s.ID, "namespace": s.Namespace, "name": s.Name, "status": s.Status, "ready": s.Ready, "desired": s.Desired, "problems": problems}
}
func overview(s cluster.Snapshot, src Source) any {
	services := slices.Clone(s.Services)
	rank := func(s cluster.Service) int {
		if s.Status == cluster.StatusDown {
			return 0
		}
		if len(s.Problems) > 0 {
			return 1
		}
		return 2
	}
	slices.SortStableFunc(services, func(a, b cluster.Service) int { return cmp.Compare(rank(a), rank(b)) })
	briefs := []map[string]any{}
	for _, svc := range services[:min(len(services), 80)] {
		briefs = append(briefs, briefService(svc))
	}
	nodes := []map[string]any{}
	if src.NodesEnabled() {
		data := src.Nodes()
		for _, node := range data.Nodes[:min(len(data.Nodes), 40)] {
			nodes = append(nodes, map[string]any{"name": node.Name, "ready": node.Ready, "pressure": node.Pressure, "unschedulable": node.Unschedulable, "pods": len(node.Pods)})
		}
	}
	return struct {
		SnapshotAt   any             `json:"snapshotAt"`
		Summary      cluster.Summary `json:"summary"`
		NodesEnabled bool            `json:"nodesEnabled"`
		Nodes        any             `json:"nodes"`
		Note         string          `json:"note"`
		Services     any             `json:"services"`
	}{s.FetchedAt, s.Summary, src.NodesEnabled(), nodes, "Bounded overview; unhealthy services first, at most 80 services and 40 nodes. Use find_services for omitted services and service for connections. Links describe configuration, not measured traffic. Node data requires scope.nodes; usage requires metrics-server.", briefs}
}
func serviceDetails(s cluster.Snapshot, id string) (any, error) {
	for _, svc := range s.Services {
		if svc.ID != id {
			continue
		}
		workloads := []cluster.Workload{}
		for _, w := range s.Workloads {
			if !slices.Contains(svc.Workloads, w.ID) {
				continue
			}
			w.Pods = slices.Clone(w.Pods)
			slices.SortStableFunc(w.Pods, func(a, b cluster.PodState) int {
				if a.Ready == b.Ready {
					return 0
				}
				if !a.Ready {
					return -1
				}
				return 1
			})
			w.Pods = w.Pods[:min(15, len(w.Pods))]
			w.Events = slices.Clone(w.Events[:min(8, len(w.Events))])
			w.Problems = nil
			w.Message = clip(w.Message, 600)
			for i := range w.Events {
				w.Events[i].Message = clip(w.Events[i].Message, 600)
			}
			workloads = append(workloads, w)
			if len(workloads) == 5 {
				break
			}
		}
		links := []cluster.Link{}
		for _, l := range s.Links {
			if l.From == id || l.To == id {
				l.Via = clip(l.Via, 200)
				l.Evidence = clip(l.Evidence, 200)
				links = append(links, l)
				if len(links) == 30 {
					break
				}
			}
		}
		return map[string]any{"service": briefService(svc), "entrypoints": svc.Entrypoints[:min(8, len(svc.Entrypoints))], "workloads": workloads, "connections": links, "limits": "At most 5 workloads, 15 pods and 8 events per workload, 30 direct connections. Event messages clipped to 600 characters. No logs fetched."}, nil
	}
	return nil, cluster.ErrNotFound
}
func findServices(s cluster.Snapshot, query string) any {
	results := []map[string]any{}
	total := 0
	for _, svc := range s.Services {
		if strings.Contains(strings.ToLower(svc.ID+" "+svc.Title), strings.ToLower(query)) {
			total++
			if len(results) < 30 {
				results = append(results, briefService(svc))
			}
		}
	}
	return map[string]any{"matches": total, "services": results, "truncated": total > len(results)}
}
