package cluster

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestParseLogLines(t *testing.T) {
	text := "2026-10-24T10:00:00.5Z started\n" +
		"2026-10-24T12:00:01.123456789+02:00 local clock\n" +
		"no timestamp here\n" +
		"2026-10-24T10:00:02Z\n"
	got := ParseLogLines(text)
	want := []LogLine{
		{Timestamp: "2026-10-24T10:00:00.500000000Z", Text: "started"},
		{Timestamp: "2026-10-24T10:00:01.123456789Z", Text: "local clock"},
		{Text: "no timestamp here"},
		{Text: "2026-10-24T10:00:02Z"}, // a bare stamp has no message
	}
	if len(got) != len(want) {
		t.Fatalf("got %d lines: %+v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	// Fixed width, so string order is time order.
	if got[0].Timestamp >= got[1].Timestamp {
		t.Errorf("stamps don't sort: %q vs %q", got[0].Timestamp, got[1].Timestamp)
	}
}

func TestContainerChoice(t *testing.T) {
	p := &corev1.Pod{
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "migrate"}},
			Containers:     []corev1.Container{{Name: "app"}, {Name: "proxy"}},
		},
		Status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{{
			Name:  "migrate",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1}},
		}}},
	}
	c, names, err := containerNames(p, "")
	if err != nil || c != "migrate" || len(names) != 3 || names[0] != "app" {
		t.Errorf("failed init container should be picked: %q %v %v", c, names, err)
	}

	p.Status.InitContainerStatuses[0].State.Terminated.ExitCode = 0
	if c, _, _ := containerNames(p, ""); c != "app" {
		t.Errorf("finished init container: picked %q", c)
	}
	if c, _, _ := containerNames(p, "proxy"); c != "proxy" {
		t.Errorf("explicit choice: picked %q", c)
	}
	if _, _, err := containerNames(p, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown container: %v", err)
	}
}

func TestReadLogs(t *testing.T) {
	p := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}}
	p.Namespace, p.Name = "a", "web-1"
	cs := fake.NewClientset(p)
	// The fake client answers every log read with "fake logs".
	got, err := ReadLogs(context.Background(), cs, p, LogRequest{TailLines: 10})
	if err != nil || got.Container != "app" || len(got.Lines) != 1 || got.Lines[0].Text != "fake logs" {
		t.Fatalf("got %+v, %v", got, err)
	}
}
