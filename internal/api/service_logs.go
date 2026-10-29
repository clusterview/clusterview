package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/clusterview/clusterview/internal/cluster"
)

func (s *Server) serviceLogRequest(w http.ResponseWriter, r *http.Request) (string, []*corev1.Pod, cluster.LogRequest, bool) {
	req, ok := parseLogRequest(w, r)
	if !ok {
		return "", nil, req, false
	}
	id := r.URL.Query().Get("serviceId")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "serviceId is required"})
		return id, nil, req, false
	}
	pods, err := s.cache.ServicePods(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "service not found in the watched namespaces"})
		return id, nil, req, false
	}
	return id, pods, req, true
}

func (s *Server) serviceLogs(w http.ResponseWriter, r *http.Request) {
	_, pods, req, ok := s.serviceLogRequest(w, r)
	if !ok {
		return
	}
	logs, err := cluster.ReadServiceLogs(r.Context(), s.cache.Clientset(), pods, req)
	if err != nil {
		s.logError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

type serviceLogEvent struct {
	line   *cluster.ServiceLogLine
	notice string
}

// followServiceLogs multiplexes container streams into one browser connection.
// Only this goroutine writes SSE; workers respect backpressure and cancellation.
func (s *Server) followServiceLogs(w http.ResponseWriter, r *http.Request) {
	updates, stop := s.cache.Subscribe()
	defer stop()
	id, pods, req, ok := s.serviceLogRequest(w, r)
	if !ok {
		return
	}
	if req.Previous {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "previous logs cannot be followed"})
		return
	}
	metadata, err := cluster.ServiceLogSources(pods, req.Container)
	if err != nil {
		s.logError(w, err)
		return
	}
	sse, ok := startSSE(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	messages := make(chan serviceLogEvent)
	streams := map[string]context.CancelFunc{}
	permits := make(chan struct{}, cluster.MaxServiceLogSources)
	start := func(source cluster.LogSource) {
		child, stopStream := context.WithCancel(ctx)
		streams[source.Key()] = stopStream
		wg.Go(func() {
			select {
			case permits <- struct{}{}:
				defer func() { <-permits }()
			case <-child.Done():
				return
			}
			err := cluster.FollowSourceLogs(child, s.cache.Clientset(), source, req, func(line cluster.ServiceLogLine) error {
				select {
				case messages <- serviceLogEvent{line: &line}:
					return nil
				case <-child.Done():
					return child.Err()
				}
			})
			if err != nil && child.Err() == nil {
				select {
				case messages <- serviceLogEvent{notice: fmt.Sprintf("%s/%s: %v", source.Pod, source.Container, err)}:
				case <-child.Done():
				}
			}
		})
	}
	reconcile := func(next cluster.ServiceLogs) error {
		wanted := map[string]bool{}
		for _, source := range next.Sources {
			wanted[source.Key()] = true
		}
		for key, stopStream := range streams {
			if !wanted[key] {
				stopStream()
				delete(streams, key)
			}
		}
		for _, source := range next.Sources {
			if _, exists := streams[source.Key()]; !exists {
				start(source)
			}
		}
		return sse.send("sources", next)
	}
	if reconcile(metadata) != nil {
		return
	}
	beat := time.NewTicker(heartbeat)
	defer beat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-updates:
			pods, err := s.cache.ServicePods(id)
			if err != nil {
				_ = sse.send("end", map[string]string{"error": "service no longer exists in the watched namespaces"})
				return
			}
			next, err := cluster.ServiceLogSources(pods, req.Container)
			if err != nil {
				_ = sse.send("end", map[string]string{"error": "container no longer exists"})
				return
			}
			if slices.EqualFunc(metadata.Sources, next.Sources, func(a, b cluster.LogSource) bool { return a.Key() == b.Key() }) && slices.Equal(metadata.Containers, next.Containers) && metadata.TotalSources == next.TotalSources {
				continue
			}
			metadata = next
			if reconcile(next) != nil {
				return
			}
		case event := <-messages:
			if event.line != nil {
				err = sse.send("line", event.line)
			} else {
				err = sse.send("notice", map[string]string{"message": event.notice})
			}
			if err != nil {
				return
			}
		case <-beat.C:
			if sse.comment("ping") != nil {
				return
			}
		}
	}
}
