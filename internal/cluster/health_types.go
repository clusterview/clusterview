package cluster

import (
	"context"
	"time"
)

// HealthState describes an observed resource or application signal, separately
// from Kubernetes readiness. Unknown never means healthy.
type HealthState string

// Observed health states, from healthy to unknown or needing attention.
const (
	HealthHealthy  HealthState = "healthy"
	HealthWarning  HealthState = "warning"
	HealthCritical HealthState = "critical"
	HealthUnknown  HealthState = "unknown"
)

// HealthPolicy controls percentage-based resource warnings. Notifications also
// wait for alerts.for, so brief peaks don't send alerts.
type HealthPolicy struct {
	WarningPercent  float64 `json:"warningPercent"`
	CriticalPercent float64 `json:"criticalPercent"`
}

// HealthPoint is a bounded, in-memory history of a measured percentage.
type HealthPoint struct {
	Time  time.Time `json:"time"`
	Value float64   `json:"value"`
}

// ResourceReading includes its denominator and source; a missing limit or
// measurement leaves Percent absent rather than inventing a percentage.
type ResourceReading struct {
	State      HealthState   `json:"state"`
	Usage      *float64      `json:"usage,omitempty"`
	Limit      *float64      `json:"limit,omitempty"`
	Percent    *float64      `json:"percent,omitempty"`
	Unit       string        `json:"unit"`
	Source     string        `json:"source"`
	Reason     string        `json:"reason"`
	Subject    string        `json:"subject,omitempty"`
	ObservedAt *time.Time    `json:"observedAt,omitempty"`
	History    []HealthPoint `json:"history,omitempty"`
}

// ContainerHealth identifies the container responsible for a resource warning.
type ContainerHealth struct {
	Name       string           `json:"name"`
	CPU        ResourceReading  `json:"cpu"`
	Memory     ResourceReading  `json:"memory"`
	Throttling *ResourceReading `json:"throttling,omitempty"`
}

// ServiceHealth is the worst observed container/volume, not an average which
// could hide a single container approaching its limit.
type ServiceHealth struct {
	State  HealthState      `json:"state"`
	CPU    ResourceReading  `json:"cpu"`
	Memory ResourceReading  `json:"memory"`
	Disk   *ResourceReading `json:"disk,omitempty"`
	Queues []QueueHealth    `json:"queues,omitempty"`
	// Resource checks require complete evidence for recovery, even when a known
	// warning or critical reading takes priority in the displayed severity.
	Checks map[string]HealthState `json:"checks"`
}

// QueueTarget explicitly associates a Prometheus job and queue with a discovered
// service. This avoids guessing the cluster or service from a queue name.
type QueueTarget struct {
	ServiceID string `json:"serviceId"`
	Job       string `json:"job"`
	VHost     string `json:"vhost"`
	Queue     string `json:"queue"`
	// BrokerLabels selects one logical broker cluster within its namespace.
	BrokerLabels     map[string]string `json:"brokerLabels,omitempty"`
	WarningMessages  int64             `json:"warningMessages"`
	CriticalMessages int64             `json:"criticalMessages"`
	// Pull consumers and intentionally delayed queues can disable this check.
	AllowNoConsumers bool `json:"allowNoConsumers"`
	// auto uses automatic delivery/get counters instead of manual acknowledgements.
	AckMode string `json:"ackMode"`
}

// QueueHealth shows flow as well as depth. Nil values indicate missing data.
type QueueHealth struct {
	QueueTarget
	State       HealthState `json:"state"`
	Reason      string      `json:"reason"`
	Ready       *float64    `json:"ready,omitempty"`
	Unacked     *float64    `json:"unacked,omitempty"`
	Consumers   *float64    `json:"consumers,omitempty"`
	PublishRate *float64    `json:"publishRate,omitempty"`
	ProcessRate *float64    `json:"processRate,omitempty"`
	ObservedAt  *time.Time  `json:"observedAt,omitempty"`
}

// TelemetrySample is a timestamped number from an optional monitoring source.
type TelemetrySample struct {
	Value float64
	Time  time.Time
}

// VolumeSample is persistent-volume filesystem usage, not requested storage.
type VolumeSample struct {
	Used     TelemetrySample
	Capacity TelemetrySample
}

// TelemetryData is refreshed as a unit. A failed source clears its readings;
// callers must not reuse old observations as evidence of recovery.
type TelemetryData struct {
	Volumes    map[string]VolumeSample    // namespace/PVC
	Throttling map[string]TelemetrySample // namespace/pod/container
	Queues     []QueueHealth
}

// Telemetry reads optional disk, throttling and application metrics.
type Telemetry interface {
	Read(context.Context) TelemetryData
}
