package cluster

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ErrNotFound means the pod or container doesn't exist (any more).
var ErrNotFound = errors.New("not found")

// logLimitBytes caps one log read, so a chatty pod can't flood the browser.
const logLimitBytes = 2 << 20

// LogRequest picks which log to read.
type LogRequest struct {
	Container string
	// Previous reads the container's log from before its last restart.
	Previous  bool
	TailLines int64
	// SinceTime returns only lines from this moment on.
	SinceTime *time.Time
}

// containerNames returns a pod's containers, main ones first, and checks the
// requested one exists. An empty request picks the container most likely to
// explain the pod's state.
func containerNames(pod *corev1.Pod, requested string) (string, []string, error) {
	var names []string
	for _, c := range pod.Spec.Containers {
		names = append(names, c.Name)
	}
	if len(names) == 0 {
		return "", nil, ErrNotFound
	}
	for _, c := range pod.Spec.InitContainers {
		names = append(names, c.Name)
	}
	if requested != "" {
		if !slices.Contains(names, requested) {
			return "", nil, ErrNotFound
		}
		return requested, names, nil
	}
	return defaultContainer(pod), names, nil
}

// defaultContainer is an init container that hasn't finished cleanly, else
// the first main container. A pod in Init:CrashLoopBackOff has written
// nothing from its main container yet; the init container's log explains it.
func defaultContainer(pod *corev1.Pod) string {
	for _, s := range pod.Status.InitContainerStatuses {
		t := s.State.Terminated
		if (t == nil || t.ExitCode != 0) && s.Name != "" {
			return s.Name
		}
	}
	return pod.Spec.Containers[0].Name
}

func logOptions(container string, req LogRequest, follow bool) *corev1.PodLogOptions {
	opts := &corev1.PodLogOptions{
		Container:  container,
		Timestamps: true,
		Previous:   req.Previous,
		Follow:     follow,
	}
	if req.TailLines > 0 {
		opts.TailLines = &req.TailLines
	}
	if !follow {
		limit := int64(logLimitBytes)
		opts.LimitBytes = &limit
	}
	if req.SinceTime != nil {
		t := metav1.NewTime(*req.SinceTime)
		opts.SinceTime = &t
	}
	return opts
}

// ReadLogs returns one container's recent log.
func ReadLogs(ctx context.Context, cs kubernetes.Interface, pod *corev1.Pod, req LogRequest) (PodLogs, error) {
	container, names, err := containerNames(pod, req.Container)
	if err != nil {
		return PodLogs{}, err
	}
	out := PodLogs{
		Namespace: pod.Namespace, Pod: pod.Name, Container: container,
		Containers: names, Previous: req.Previous, Lines: []LogLine{},
	}
	raw, err := cs.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, logOptions(container, req, false)).DoRaw(ctx)
	if err != nil {
		switch {
		case apierrors.IsNotFound(err):
			return PodLogs{}, ErrNotFound
		case apierrors.IsBadRequest(err):
			// "previous terminated container not found", or a container that
			// is still being created.
			out.Notice = apiMessage(err, raw)
			return out, nil
		}
		return PodLogs{}, fmt.Errorf("reading log: %w", err)
	}
	out.Lines = ParseLogLines(string(raw))
	return out, nil
}

// FollowLogs streams new lines from one container until ctx ends, calling
// emit for each.
func FollowLogs(ctx context.Context, cs kubernetes.Interface, pod *corev1.Pod, req LogRequest, emit func(LogLine) error) error {
	container, _, err := containerNames(pod, req.Container)
	if err != nil {
		return err
	}
	stream, err := cs.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, logOptions(container, req, true)).Stream(ctx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ErrNotFound
		}
		return fmt.Errorf("following log: %w", err)
	}
	defer func() { _ = stream.Close() }()
	return scanLines(stream, emit)
}

func scanLines(r io.Reader, emit func(LogLine) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if err := emit(parseLogLine(sc.Text())); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

const utcLayout = "2006-01-02T15:04:05.000000000Z"

var stamp = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d{1,9})?(Z|[+-]\d\d:\d\d)$`)

// ParseLogLines splits "<RFC3339> <message>" lines as Kubernetes writes them
// with timestamps on.
func ParseLogLines(text string) []LogLine {
	lines := []LogLine{}
	for raw := range strings.Lines(text) {
		lines = append(lines, parseLogLine(strings.TrimRight(raw, "\r\n")))
	}
	return lines
}

func parseLogLine(raw string) LogLine {
	ts, rest, ok := strings.Cut(raw, " ")
	if ok {
		if utc := utcStamp(ts); utc != "" {
			return LogLine{Timestamp: utc, Text: rest}
		}
	}
	return LogLine{Text: raw}
}

// utcStamp rewrites a timestamp in UTC with a fixed nine-digit fraction. A
// node on local time writes an offset (+02:00), and the UI compares stamps as
// strings, so they all need the same zone and width.
func utcStamp(s string) string {
	if !stamp.MatchString(s) {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return ""
	}
	return t.UTC().Format(utcLayout)
}

// apiMessage is Kubernetes' explanation of a failed log read, taken from the
// response body (a Status object, or plain text) when there is one.
func apiMessage(err error, body []byte) string {
	var st metav1.Status
	if json.Unmarshal(body, &st) == nil && st.Message != "" {
		return st.Message
	}
	if text := strings.TrimSpace(string(body)); text != "" && !strings.HasPrefix(text, "{") {
		return text
	}
	var status apierrors.APIStatus
	if errors.As(err, &status) && status.Status().Message != "" {
		return status.Status().Message
	}
	return err.Error()
}
