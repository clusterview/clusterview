package cluster

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

const healthMaxAge = 2 * time.Minute

func freshHealth(t, now time.Time) bool {
	return !t.IsZero() && now.Sub(t) <= healthMaxAge && t.Sub(now) <= 30*time.Second
}

func healthRank(s HealthState) int {
	switch s {
	case HealthCritical:
		return 3
	case HealthWarning:
		return 2
	case HealthUnknown:
		return 1
	default:
		return 0
	}
}

func unknownReading(unit, source, reason string) ResourceReading {
	return ResourceReading{State: HealthUnknown, Unit: unit, Source: source, Reason: reason}
}

func reading(usage, limit float64, unit, source, subject string, at time.Time, policy HealthPolicy) ResourceReading {
	if math.IsNaN(usage) || math.IsInf(usage, 0) || math.IsNaN(limit) || math.IsInf(limit, 0) || usage < 0 {
		return unknownReading(unit, source, "Invalid measurement")
	}
	r := ResourceReading{State: HealthHealthy, Usage: &usage, Limit: &limit, Unit: unit, Source: source, Subject: subject, ObservedAt: &at, Reason: "Within configured thresholds"}
	if limit <= 0 {
		r.State = HealthUnknown
		r.Limit = nil
		r.Reason = "No limit configured; percentage unavailable"
		return r
	}
	pct := usage / limit * 100
	r.Percent = &pct
	if pct >= policy.CriticalPercent {
		r.State = HealthCritical
		r.Reason = "At or above the critical threshold"
	} else if pct >= policy.WarningPercent {
		r.State = HealthWarning
		r.Reason = "Approaching its limit"
	}
	return r
}

// worstReading preserves missing data even when other containers are healthy,
// while still surfacing a known critical or warning observation.
func worstReading(all []ResourceReading, empty ResourceReading) ResourceReading {
	if len(all) == 0 {
		return empty
	}
	best := all[0]
	for _, r := range all[1:] {
		if healthRank(r.State) > healthRank(best.State) || (r.State == best.State && r.Percent != nil && (best.Percent == nil || *r.Percent > *best.Percent)) {
			best = r
		}
	}
	return best
}

// recoveryState requires every current subject to have an observation. Display
// severity can still show a known problem while another subject is unknown.
func recoveryState(all []ResourceReading) HealthState {
	if len(all) == 0 {
		return HealthUnknown
	}
	state := HealthHealthy
	for _, r := range all {
		if r.State == HealthUnknown || r.State == "" {
			return HealthUnknown
		}
		if healthRank(r.State) > healthRank(state) {
			state = r.State
		}
	}
	return state
}

// QueueCheckKey is stable even when queue names contain separators.
func QueueCheckKey(q QueueTarget) string {
	key := "queue:" + strconv.Quote(q.Job) + ":" + strconv.Quote(q.VHost) + ":" + strconv.Quote(q.Queue)
	keys := make([]string, 0, len(q.BrokerLabels))
	for label := range q.BrokerLabels {
		keys = append(keys, label)
	}
	slices.Sort(keys)
	for _, label := range keys {
		key += ":" + strconv.Quote(label) + "=" + strconv.Quote(q.BrokerLabels[label])
	}
	return key
}

// EvaluateQueue distinguishes a draining burst from stalled or growing work.
// Missing rates and missing consumers are never silently interpreted as zero.
func EvaluateQueue(q QueueHealth, now time.Time) QueueHealth {
	q.State = HealthUnknown
	q.Reason = "Queue metrics missing or stale"
	if q.ObservedAt == nil || !freshHealth(*q.ObservedAt, now) {
		return q
	}
	if q.Ready == nil || q.Unacked == nil {
		return q
	}
	total := *q.Ready + *q.Unacked
	warn, critical := q.WarningMessages, q.CriticalMessages
	if warn <= 0 {
		warn = 1000
	}
	if critical <= 0 {
		critical = 10000
	}
	switch {
	case *q.Ready > 0 && q.Consumers != nil && *q.Consumers == 0 && !q.AllowNoConsumers:
		q.State = HealthCritical
		q.Reason = "Messages waiting with no consumers"
	case q.ProcessRate != nil && *q.ProcessRate <= 0 && total >= float64(critical):
		q.State = HealthCritical
		q.Reason = "Large backlog with no processing"
	case q.ProcessRate != nil && q.PublishRate != nil && *q.PublishRate > *q.ProcessRate && total >= float64(critical):
		q.State = HealthCritical
		q.Reason = "Large backlog continues to grow"
	case q.ProcessRate != nil && *q.ProcessRate <= 0 && total >= float64(warn):
		q.State = HealthWarning
		q.Reason = "Messages are not being processed"
	case q.ProcessRate != nil && q.PublishRate != nil && *q.PublishRate > *q.ProcessRate && total >= float64(warn):
		q.State = HealthWarning
		q.Reason = "Incoming messages exceed processing"
	case q.ProcessRate == nil || q.PublishRate == nil || q.Consumers == nil:
		return q
	default:
		q.State = HealthHealthy
		q.Reason = "Message flow within configured thresholds"
		if total > 0 && *q.ProcessRate > *q.PublishRate {
			q.Reason = "Backlog is draining"
		}
	}
	return q
}

// enrichHealth attaches resource evidence without changing Kubernetes readiness.
// All associations use the already-scoped snapshot and its actual pod volumes.
func enrichHealth(s *Snapshot, objs Objects, metrics []metricsv1beta1.PodMetrics, extra TelemetryData, policy HealthPolicy, now time.Time) {
	if policy.WarningPercent == 0 {
		policy.WarningPercent = 85
	}
	if policy.CriticalPercent == 0 {
		policy.CriticalPercent = 95
	}
	raw := map[string]*corev1.Pod{}
	for _, p := range objs.Pods {
		raw[p.Namespace+"/"+p.Name] = p
	}
	met := map[string]metricsv1beta1.PodMetrics{}
	for _, m := range metrics {
		if freshHealth(m.Timestamp.Time, now) {
			met[m.Namespace+"/"+m.Name] = m
		}
	}
	work := map[string]*Workload{}
	for i := range s.Workloads {
		w := &s.Workloads[i]
		work[w.ID] = w
		if w.Kind == KindJob || w.Kind == KindCronJob {
			continue
		}
		for pi := range w.Pods {
			ps := &w.Pods[pi]
			p := raw[ps.Namespace+"/"+ps.Name]
			if p == nil || p.Status.Phase != corev1.PodRunning {
				continue
			}
			m, ok := met[p.Namespace+"/"+p.Name]
			if p.Status.StartTime != nil && m.Timestamp.Before(p.Status.StartTime) {
				ok = false
			}
			usage := map[string]corev1.ResourceList{}
			if ok {
				for _, c := range m.Containers {
					usage[c.Name] = c.Usage
				}
			}
			started := map[string]time.Time{}
			for _, status := range append(slices.Clone(p.Status.ContainerStatuses), p.Status.InitContainerStatuses...) {
				if status.State.Running != nil {
					started[status.Name] = status.State.Running.StartedAt.Time
				}
			}
			containers := slices.Clone(p.Spec.Containers)
			for _, c := range p.Spec.InitContainers {
				if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
					containers = append(containers, c)
				}
			}
			for _, c := range containers {
				subject := p.Name + "/" + c.Name
				cpu := unknownReading("millicores", "metrics-server", "CPU metrics missing or stale")
				mem := unknownReading("bytes", "metrics-server", "Memory metrics missing or stale")
				cpu.Subject, mem.Subject = subject, subject
				if u, found := usage[c.Name]; found && !m.Timestamp.Time.Before(started[c.Name]) {
					if v, exists := u[corev1.ResourceCPU]; exists {
						cpu = reading(float64(v.MilliValue()), float64(c.Resources.Limits.Cpu().MilliValue()), "millicores", "metrics-server", subject, m.Timestamp.Time, policy)
					}
					if v, exists := u[corev1.ResourceMemory]; exists {
						mem = reading(float64(v.Value()), float64(c.Resources.Limits.Memory().Value()), "bytes", "metrics-server", subject, m.Timestamp.Time, policy)
					}
				}
				var throttle *ResourceReading
				if t, found := extra.Throttling[p.Namespace+"/"+subject]; found && freshHealth(t.Time, now) && !t.Time.Before(started[c.Name].Add(5*time.Minute)) && (p.Status.StartTime == nil || !t.Time.Before(p.Status.StartTime.Add(5*time.Minute))) && t.Value >= 0 && !math.IsNaN(t.Value) && !math.IsInf(t.Value, 0) {
					tr := reading(t.Value, 100, "percent", "prometheus", subject, t.Time, HealthPolicy{WarningPercent: 25, CriticalPercent: 50})
					throttle = &tr
				}
				// Busy CPUs can be useful work. Saturation alone is warning-level;
				// critical CPU health requires evidence of throttling too.
				if cpu.State == HealthCritical {
					cpu.State = HealthWarning
					cpu.Reason = "CPU close to its limit"
				}
				ps.Resources = append(ps.Resources, ContainerHealth{Name: c.Name, CPU: cpu, Memory: mem, Throttling: throttle})
			}
		}
	}
	s.Summary.NeedAttention = 0
	for i := range s.Services {
		svc := &s.Services[i]
		var cpus, mems, disks, throttles []ResourceReading
		active := false
		seenVolumes := map[string]bool{}
		for _, id := range svc.Workloads {
			w := work[id]
			if w == nil || w.Kind == KindJob || w.Kind == KindCronJob {
				continue
			}
			active = active || w.Desired > 0 || len(w.Pods) > 0
			for _, ps := range w.Pods {
				for _, c := range ps.Resources {
					cpus = append(cpus, c.CPU)
					mems = append(mems, c.Memory)
					tr := unknownReading("percent", "prometheus", "CPU throttling metrics missing or stale")
					tr.Subject = ps.Name + "/" + c.Name
					if c.Throttling != nil {
						tr = *c.Throttling
					}
					throttles = append(throttles, tr)
				}
				p := raw[ps.Namespace+"/"+ps.Name]
				if p == nil {
					continue
				}
				for _, v := range p.Spec.Volumes {
					if v.PersistentVolumeClaim == nil {
						continue
					}
					key := p.Namespace + "/" + v.PersistentVolumeClaim.ClaimName
					if seenVolumes[key] {
						continue
					}
					seenVolumes[key] = true
					r := unknownReading("bytes", "prometheus", "Volume usage unavailable; connect Prometheus with kubelet volume metrics")
					r.Subject = key
					if d, ok := extra.Volumes[key]; ok && freshHealth(d.Used.Time, now) && freshHealth(d.Capacity.Time, now) && d.Used.Value >= 0 && d.Capacity.Value > 0 {
						at := d.Used.Time
						if d.Capacity.Time.Before(at) {
							at = d.Capacity.Time
						}
						r = reading(d.Used.Value, d.Capacity.Value, "bytes", "prometheus", key, at, policy)
					}
					disks = append(disks, r)
				}
			}
		}
		if active {
			h := &ServiceHealth{
				CPU:    worstReading(cpus, unknownReading("millicores", "metrics-server", "No current container metrics")),
				Memory: worstReading(mems, unknownReading("bytes", "metrics-server", "No current container metrics")),
				Checks: map[string]HealthState{
					"resource-cpu":            recoveryState(cpus),
					"resource-memory":         recoveryState(mems),
					"resource-cpu-throttling": recoveryState(throttles),
				},
			}
			if len(disks) > 0 {
				d := worstReading(disks, ResourceReading{})
				h.Disk = &d
				h.Checks["resource-disk"] = recoveryState(disks)
			}
			resources := []struct {
				key, label string
				r          ResourceReading
			}{{"resource-cpu", "CPU", h.CPU}, {"resource-memory", "Memory", h.Memory}}
			// Usage and throttling are independent evidence. A low usage sample
			// cannot prove recovery from an incident caused by throttling.
			throttle := worstReading(throttles, unknownReading("percent", "prometheus", "No current throttling metrics"))
			if extra.Throttling != nil {
				resources = append(resources, struct {
					key, label string
					r          ResourceReading
				}{"resource-cpu-throttling", "CPU throttling", throttle})
			}
			if h.Disk != nil {
				resources = append(resources, struct {
					key, label string
					r          ResourceReading
				}{"resource-disk", "Disk", *h.Disk})
			}
			h.State = HealthHealthy
			for _, item := range resources {
				r := item.r
				if healthRank(r.State) > healthRank(h.State) {
					h.State = r.State
				}
				if r.State == HealthWarning || r.State == HealthCritical {
					sev := SeverityWarning
					if r.State == HealthCritical {
						sev = SeverityCritical
					}
					detail := r.Reason + ". " + r.Subject
					if r.Percent != nil {
						detail = fmt.Sprintf("%.1f%% of limit/capacity. %s", *r.Percent, detail)
					}
					svc.Problems = append(svc.Problems, Problem{Kind: item.key, Severity: sev, Title: item.label + ": " + r.Reason, Detail: detail})
				}
			}
			// Keep the compact CPU meter useful while retaining separate check
			// identities for alert recovery and independently reported problems.
			if extra.Throttling != nil && healthRank(throttle.State) > healthRank(h.CPU.State) {
				for _, cpu := range cpus {
					if cpu.Subject == throttle.Subject {
						h.CPU = cpu
						break
					}
				}
				h.CPU.State = throttle.State
				h.CPU.Reason = throttle.Reason
				if throttle.State == HealthWarning || throttle.State == HealthCritical {
					h.CPU.Reason = "CPU is being throttled"
				}
			}
			for _, q := range extra.Queues {
				if q.ServiceID != svc.ID {
					continue
				}
				q = EvaluateQueue(q, now)
				h.Queues = append(h.Queues, q)
				h.Checks[QueueCheckKey(q.QueueTarget)] = q.State
				if healthRank(q.State) > healthRank(h.State) {
					h.State = q.State
				}
				if q.State == HealthWarning || q.State == HealthCritical {
					sev := SeverityWarning
					if q.State == HealthCritical {
						sev = SeverityCritical
					}
					svc.Problems = append(svc.Problems, Problem{Kind: QueueCheckKey(q.QueueTarget), Severity: sev, Title: q.Queue + ": " + q.Reason, Detail: "Virtual host " + q.VHost + "; Prometheus job " + q.Job})
				}
			}
			svc.Health = h
			svc.Problems = mergeProblems(svc.Problems)
		}
		if svc.Status == StatusDown || svc.Status == StatusDegraded || len(svc.Problems) > 0 {
			s.Summary.NeedAttention++
		}
	}
}
