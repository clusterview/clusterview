// Package actions changes things in the cluster on a person's behalf:
// restarting a pod, a rolling restart, scaling, running a CronJob now. Each
// action is checked against the configured limits and recorded in the audit
// log. Nothing here deletes a workload or reads a Secret.
package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// Config limits what actions may do.
type Config struct {
	Enabled bool `json:"enabled"`
	// Namespaces, when set, are the only ones actions may touch.
	Namespaces []string `json:"namespaces"`
	// MaxReplicas caps scaling.
	MaxReplicas int `json:"maxReplicas"`
}

// Errors the API maps to status codes.
var (
	ErrDisabled  = errors.New("actions are switched off; set actions.enabled in the Helm values")
	ErrNamespace = errors.New("actions aren't allowed in this namespace")
	ErrNotFound  = errors.New("not found")
)

// Refused is an action that's allowed in general but wouldn't make sense
// here, with the reason in plain words.
type Refused struct{ Reason string }

func (r Refused) Error() string { return r.Reason }

// Runner carries out actions.
type Runner struct {
	cfg   Config
	cs    kubernetes.Interface
	audit *Audit
	now   func() time.Time
}

// NewRunner returns a Runner. Audit may be shared with the API to list
// entries.
func NewRunner(cfg Config, cs kubernetes.Interface, audit *Audit, log *slog.Logger) *Runner {
	if cfg.MaxReplicas <= 0 {
		cfg.MaxReplicas = 20
	}
	if cfg.Namespaces == nil {
		cfg.Namespaces = []string{}
	}
	if audit == nil {
		audit = NewAudit(cs, log)
	}
	return &Runner{cfg: cfg, cs: cs, audit: audit, now: time.Now}
}

// Config returns the limits in force.
func (r *Runner) Config() Config { return r.cfg }

// Audit returns the audit log.
func (r *Runner) Audit() *Audit { return r.audit }

func (r *Runner) allowed(namespace string) error {
	if !r.cfg.Enabled {
		return ErrDisabled
	}
	if len(r.cfg.Namespaces) > 0 && !slices.Contains(r.cfg.Namespaces, namespace) {
		return ErrNamespace
	}
	return nil
}

// do checks the namespace, runs fn and records the outcome. Refusals and
// failures are recorded too; a disallowed namespace isn't, since nothing was
// attempted.
func (r *Runner) do(ctx context.Context, actor, action, kind, ns, name string, fn func() (detail string, obj corev1.ObjectReference, err error)) (Entry, error) {
	if err := r.allowed(ns); err != nil {
		return Entry{}, err
	}
	detail, obj, err := fn()
	if apierrors.IsNotFound(err) {
		err = ErrNotFound
	}
	e := Entry{
		Time: r.now().UTC(), Actor: actor, Action: action,
		Target: fmt.Sprintf("%s/%s/%s", kind, ns, name), Detail: detail, OK: err == nil,
	}
	if err != nil {
		e.Error = err.Error()
	}
	r.audit.record(ctx, e, obj)
	return e, err
}

// RestartPod deletes a pod so its owner replaces it.
func (r *Runner) RestartPod(ctx context.Context, actor, ns, name string) (Entry, error) {
	return r.do(ctx, actor, "Restarted pod", "Pod", ns, name, func() (string, corev1.ObjectReference, error) {
		pod, err := r.cs.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", corev1.ObjectReference{}, err
		}
		ref := metav1.GetControllerOf(pod)
		switch {
		case ref == nil:
			return "", corev1.ObjectReference{}, Refused{"Nothing manages this pod, so it wouldn't come back after a restart."}
		case ref.Kind == "Node":
			return "", corev1.ObjectReference{}, Refused{"This is a static pod run by its node; restart it on the node."}
		}
		uid := pod.UID
		err = r.cs.CoreV1().Pods(ns).Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
		// The event goes on the owner: the pod itself is on its way out.
		obj := corev1.ObjectReference{APIVersion: ref.APIVersion, Kind: ref.Kind, Namespace: ns, Name: ref.Name, UID: ref.UID}
		return fmt.Sprintf("%s will replace it", ref.Kind), obj, err
	})
}

// RolloutRestart replaces every pod of a Deployment, StatefulSet or
// DaemonSet, one batch at a time, as kubectl rollout restart does.
func (r *Runner) RolloutRestart(ctx context.Context, actor, kind, ns, name string) (Entry, error) {
	return r.do(ctx, actor, "Restarted", kind, ns, name, func() (string, corev1.ObjectReference, error) {
		patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{
			"annotations": map[string]string{
				"kubectl.kubernetes.io/restartedAt": r.now().UTC().Format(time.RFC3339),
				"clusterview.io/restarted-by":       actor,
			},
		}}}})
		apps := r.cs.AppsV1()
		var uid types.UID
		var err error
		switch kind {
		case "Deployment":
			d, gerr := apps.Deployments(ns).Get(ctx, name, metav1.GetOptions{})
			if gerr != nil {
				return "", corev1.ObjectReference{}, gerr
			}
			if d.Spec.Paused {
				return "", corev1.ObjectReference{}, Refused{"Its rollout is paused; resume it first."}
			}
			d, err = apps.Deployments(ns).Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
			if err == nil {
				uid = d.UID
			}
		case "StatefulSet":
			s, perr := apps.StatefulSets(ns).Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
			if err = perr; err == nil {
				uid = s.UID
			}
		case "DaemonSet":
			s, perr := apps.DaemonSets(ns).Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
			if err = perr; err == nil {
				uid = s.UID
			}
		default:
			return "", corev1.ObjectReference{}, Refused{fmt.Sprintf("A %s can't be restarted this way.", kind)}
		}
		return "every pod is being replaced", corev1.ObjectReference{APIVersion: "apps/v1", Kind: kind, Namespace: ns, Name: name, UID: uid}, err
	})
}

// Scale sets a Deployment's or StatefulSet's replica count, unless an
// autoscaler owns it.
func (r *Runner) Scale(ctx context.Context, actor, kind, ns, name string, replicas int) (Entry, error) {
	return r.do(ctx, actor, "Scaled", kind, ns, name, func() (string, corev1.ObjectReference, error) {
		if replicas < 0 || replicas > r.cfg.MaxReplicas {
			return "", corev1.ObjectReference{}, Refused{fmt.Sprintf("Replicas must be between 0 and %d.", r.cfg.MaxReplicas)}
		}
		if kind != "Deployment" && kind != "StatefulSet" {
			return "", corev1.ObjectReference{}, Refused{fmt.Sprintf("A %s can't be scaled.", kind)}
		}
		hpas, err := r.cs.AutoscalingV2().HorizontalPodAutoscalers(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return "", corev1.ObjectReference{}, err
		}
		for _, h := range hpas.Items {
			if t := h.Spec.ScaleTargetRef; t.Kind == kind && t.Name == name {
				return "", corev1.ObjectReference{}, Refused{fmt.Sprintf("Its autoscaler (%s) sets the replica count, between %d and %d.",
					h.Name, ptrOr(h.Spec.MinReplicas, 1), h.Spec.MaxReplicas)}
			}
		}
		apps := r.cs.AppsV1()
		var from int32
		var uid types.UID
		switch kind {
		case "Deployment":
			d, err := apps.Deployments(ns).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return "", corev1.ObjectReference{}, err
			}
			from, uid = ptrOr(d.Spec.Replicas, 1), d.UID
		case "StatefulSet":
			s, err := apps.StatefulSets(ns).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return "", corev1.ObjectReference{}, err
			}
			from, uid = ptrOr(s.Spec.Replicas, 1), s.UID
		}
		patch := []byte(`{"spec":{"replicas":` + strconv.Itoa(replicas) + `}}`)
		if kind == "Deployment" {
			_, err = apps.Deployments(ns).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
		} else {
			_, err = apps.StatefulSets(ns).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
		}
		return fmt.Sprintf("from %d to %d replicas", from, replicas), corev1.ObjectReference{APIVersion: "apps/v1", Kind: kind, Namespace: ns, Name: name, UID: uid}, err
	})
}

// RunCronJob starts a CronJob's job now, as kubectl create job --from does.
func (r *Runner) RunCronJob(ctx context.Context, actor, ns, name string) (Entry, error) {
	return r.do(ctx, actor, "Ran now", "CronJob", ns, name, func() (string, corev1.ObjectReference, error) {
		cj, err := r.cs.BatchV1().CronJobs(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", corev1.ObjectReference{}, err
		}
		jobName := manualJobName(cj.Name, r.now())
		annotations := map[string]string{"cronjob.kubernetes.io/instantiate": "manual", "clusterview.io/run-by": actor}
		for k, v := range cj.Spec.JobTemplate.Annotations {
			annotations[k] = v
		}
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name: jobName, Namespace: ns,
				Labels:      cj.Spec.JobTemplate.Labels,
				Annotations: annotations,
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "batch/v1", Kind: "CronJob", Name: cj.Name, UID: cj.UID,
					Controller: ptr(true), BlockOwnerDeletion: ptr(true),
				}},
			},
			Spec: cj.Spec.JobTemplate.Spec,
		}
		_, err = r.cs.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{})
		return "started Job " + jobName, corev1.ObjectReference{APIVersion: "batch/v1", Kind: "CronJob", Namespace: ns, Name: name, UID: cj.UID}, err
	})
}

// manualJobName fits Kubernetes' 63-character limit for names used as labels.
func manualJobName(cronJob string, t time.Time) string {
	suffix := "-manual-" + strconv.FormatInt(t.Unix(), 36)
	if len(cronJob)+len(suffix) > 63 {
		cronJob = cronJob[:63-len(suffix)]
	}
	return cronJob + suffix
}

func ptr[T any](v T) *T { return &v }

func ptrOr[T any](p *T, fallback T) T {
	if p == nil {
		return fallback
	}
	return *p
}
