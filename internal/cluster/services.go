package cluster

import (
	"cmp"
	"slices"
)

// statusRank orders statuses from calm to alarming; a service shows its
// worst member.
var statusRank = map[Status]int{
	StatusIdle:     0,
	StatusHealthy:  1,
	StatusUnknown:  2,
	StatusDegraded: 3,
	StatusDown:     4,
}

// BuildServices turns workloads into the boxes the map shows.
//
//   - Workloads an operator runs (their owner is a custom resource) become one
//     service per resource: a RabbitmqCluster's StatefulSet, a CloudNativePG
//     Cluster's pods.
//   - A Job that shares its Helm release and app name with another workload
//     (a chart's install hooks, such as ingress-nginx's admission Jobs) joins
//     that workload's service instead of getting a box of its own.
//   - Every other workload is its own service.
func BuildServices(workloads []Workload) []Service {
	type group struct {
		svc     Service
		members []*Workload
	}
	groups := map[string]*group{}
	var order []string
	add := func(key string, w *Workload, svc func() Service) {
		g, ok := groups[key]
		if !ok {
			g = &group{svc: svc()}
			groups[key] = g
			order = append(order, key)
		}
		g.members = append(g.members, w)
	}

	// Siblings a chart's Jobs can join: non-Job workloads by release and name.
	siblings := map[[3]string]string{}
	for i := range workloads {
		w := &workloads[i]
		if w.Kind == KindJob || w.Owner != nil {
			continue
		}
		k := [3]string{w.Namespace, w.labels[labelInstance], w.labels[labelName]}
		if k[1] != "" && k[2] != "" {
			if _, taken := siblings[k]; !taken {
				siblings[k] = w.ID
			}
		}
	}

	for i := range workloads {
		w := &workloads[i]
		switch {
		case w.Owner != nil:
			key := w.Owner.Group() + "/" + w.Owner.Kind + "/" + w.Namespace + "/" + w.Owner.Name
			add(key, w, func() Service {
				return Service{ID: key, Namespace: w.Namespace, Name: w.Owner.Name, ManagedBy: w.Owner, App: w.App}
			})
		case w.Kind == KindJob && siblings[[3]string{w.Namespace, w.labels[labelInstance], w.labels[labelName]}] != "":
			sib := siblings[[3]string{w.Namespace, w.labels[labelInstance], w.labels[labelName]}]
			add(sib, w, func() Service { return Service{ID: sib} }) // filled in by the sibling below
		default:
			add(w.ID, w, func() Service { return Service{ID: w.ID} })
		}
	}

	services := make([]Service, 0, len(groups))
	for _, key := range order {
		g := groups[key]
		s := g.svc
		// A service named after a workload takes that workload's identity,
		// whichever member was added first.
		if s.ManagedBy == nil {
			for _, m := range g.members {
				if m.ID == key {
					s.Namespace, s.Name, s.App = m.Namespace, m.Name, m.App
				}
			}
		}
		finishService(&s, g.members)
		services = append(services, s)
	}
	slices.SortFunc(services, func(a, b Service) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.App, b.App), cmp.Compare(a.Name, b.Name))
	})
	return services
}

func finishService(s *Service, members []*Workload) {
	slices.SortFunc(members, func(a, b *Workload) int { return cmp.Compare(jobLast(a), jobLast(b)) })
	worst := -1
	allJobs := true
	for _, m := range members {
		s.Workloads = append(s.Workloads, m.ID)
		if r := statusRank[m.Status]; r > worst {
			worst, s.Status = r, m.Status
		}
		if m.Kind != KindJob && m.Kind != KindCronJob {
			allJobs = false
		}
	}
	var problems []Problem
	for _, m := range members {
		if allJobs || (m.Kind != KindJob && m.Kind != KindCronJob) {
			s.Ready += m.Ready
			s.Desired += m.Desired
		}
		problems = append(problems, m.Problems...)
	}
	s.Problems = mergeProblems(problems)
	s.Addresses, s.Entrypoints = []Address{}, []Entrypoint{}
	describe(s, members, allJobs)
}

// jobLast sorts a service's main workload before its helper Jobs.
func jobLast(w *Workload) string {
	if w.Kind == KindJob {
		return "1" + w.Name
	}
	return "0" + w.Name
}
