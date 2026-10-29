package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/clusterview/clusterview/internal/cluster"
)

// heartbeat keeps idle event streams open through proxies that close quiet
// connections.
const heartbeat = 20 * time.Second

// needsCache answers 503 until the cluster has been read once.
func (s *Server) needsCache(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cache == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no cluster connection"})
			return
		}
		if !s.cache.Ready() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "syncing with the cluster"})
			return
		}
		h(w, r)
	}
}

func (s *Server) snapshot(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.cache.Snapshot())
}

func (s *Server) nodes(w http.ResponseWriter, _ *http.Request) {
	if !s.cache.NodesEnabled() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "The nodes view is off (scope.nodes in the Helm values, --nodes on the command line)."})
		return
	}
	writeJSON(w, http.StatusOK, s.cache.Nodes())
}

// events streams a "version" event whenever the cluster changes. The browser
// then fetches whatever view it's showing.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	sse, ok := startSSE(w)
	if !ok {
		return
	}
	updates, stop := s.cache.Subscribe()
	defer stop()

	_ = sse.send("version", s.cache.Version())
	beat := time.NewTicker(heartbeat)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case v := <-updates:
			if sse.send("version", v) != nil {
				return
			}
		case <-beat.C:
			if sse.comment("ping") != nil {
				return
			}
		}
	}
}

func (s *Server) podLogs(w http.ResponseWriter, r *http.Request) {
	pod, req, ok := s.logRequest(w, r)
	if !ok {
		return
	}
	logs, err := cluster.ReadLogs(r.Context(), s.cache.Clientset(), pod, req)
	if err != nil {
		s.logError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

// followLogs streams "line" events as the container writes them, then "end"
// when the container stops or the pod goes away.
func (s *Server) followLogs(w http.ResponseWriter, r *http.Request) {
	pod, req, ok := s.logRequest(w, r)
	if !ok {
		return
	}
	sse, ok := startSSE(w)
	if !ok {
		return
	}
	err := cluster.FollowLogs(r.Context(), s.cache.Clientset(), pod, req, func(l cluster.LogLine) error {
		return sse.send("line", l)
	})
	if err != nil && r.Context().Err() == nil {
		s.log.Warn("following log stopped", "pod", pod.Name, "err", err)
		_ = sse.send("end", map[string]string{"error": err.Error()})
		return
	}
	_ = sse.send("end", map[string]string{})
}

func (s *Server) logRequest(w http.ResponseWriter, r *http.Request) (*corev1.Pod, cluster.LogRequest, bool) {
	req, ok := parseLogRequest(w, r)
	if !ok {
		return nil, req, false
	}
	pod, err := s.cache.Pod(r.PathValue("namespace"), r.PathValue("pod"))
	if err != nil {
		s.logError(w, err)
		return nil, req, false
	}
	return pod, req, true
}

func parseLogRequest(w http.ResponseWriter, r *http.Request) (cluster.LogRequest, bool) {
	q := r.URL.Query()
	req := cluster.LogRequest{Container: q.Get("container"), Previous: q.Get("previous") == "true", TailLines: 500}
	if v := q.Get("tailLines"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 || n > 10000 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tailLines must be 1–10000"})
			return req, false
		}
		req.TailLines = n
	}
	if v := q.Get("sinceTime"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sinceTime must be RFC 3339"})
			return req, false
		}
		req.SinceTime = &t
	}
	return req, true
}

func (s *Server) logError(w http.ResponseWriter, err error) {
	if errors.Is(err, cluster.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pod or container not found"})
		return
	}
	s.log.Warn("reading log failed", "err", err)
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
}

type sseWriter struct {
	w http.ResponseWriter
	f http.Flusher
}

func startSSE(w http.ResponseWriter) (*sseWriter, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // nginx: don't buffer the stream
	w.WriteHeader(http.StatusOK)
	f.Flush()
	return &sseWriter{w: w, f: f}, true
}

func (s *sseWriter) send(event string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}

func (s *sseWriter) comment(text string) error {
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}
