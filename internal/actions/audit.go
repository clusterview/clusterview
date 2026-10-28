package actions

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// auditSize is how many entries the in-memory log keeps.
const auditSize = 500

// Entry records one action someone took.
type Entry struct {
	Time   time.Time `json:"time"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	// Target is "<Kind>/<namespace>/<name>".
	Target string `json:"target"`
	Detail string `json:"detail,omitempty"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// Audit keeps a record of every action in three places: the process log
// (JSON, for log shipping), memory (for the dashboard), and a Kubernetes
// Event on the object acted on (for kubectl describe).
type Audit struct {
	cs  kubernetes.Interface
	log *slog.Logger

	mu      sync.Mutex
	entries []Entry
}

// NewAudit returns an empty audit log.
func NewAudit(cs kubernetes.Interface, log *slog.Logger) *Audit {
	return &Audit{cs: cs, log: log}
}

func (a *Audit) record(ctx context.Context, e Entry, obj corev1.ObjectReference) {
	a.mu.Lock()
	a.entries = append(a.entries, e)
	if len(a.entries) > auditSize {
		a.entries = slices.Delete(a.entries, 0, len(a.entries)-auditSize)
	}
	a.mu.Unlock()

	a.log.Info("audit", "actor", e.Actor, "action", e.Action, "target", e.Target, "detail", e.Detail, "ok", e.OK, "error", e.Error)
	if !e.OK || obj.Name == "" {
		return
	}
	msg := fmt.Sprintf("%s by %s via clusterview", e.Action, e.Actor)
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	now := metav1.NewTime(e.Time)
	ev := &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{GenerateName: obj.Name + ".clusterview-", Namespace: obj.Namespace},
		InvolvedObject: obj,
		Type:           corev1.EventTypeNormal,
		Reason:         "ClusterviewAction",
		Message:        msg,
		Source:         corev1.EventSource{Component: "clusterview"},
		FirstTimestamp: now,
		LastTimestamp:  now,
		Count:          1,
	}
	if _, err := a.cs.CoreV1().Events(obj.Namespace).Create(ctx, ev, metav1.CreateOptions{}); err != nil {
		a.log.Warn("couldn't record the action as a Kubernetes event", "err", err)
	}
}

// List returns the entries, newest first.
func (a *Audit) List() []Entry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := slices.Clone(a.entries)
	slices.Reverse(out)
	if out == nil {
		out = []Entry{}
	}
	return out
}
