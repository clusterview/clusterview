// Package api serves clusterview's HTTP API and the web UI.
package api

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/clusterview/clusterview/internal/actions"
	"github.com/clusterview/clusterview/internal/auth"
	"github.com/clusterview/clusterview/internal/cluster"
	"github.com/clusterview/clusterview/internal/diagnose"
	"github.com/clusterview/clusterview/internal/kube"
	"github.com/clusterview/clusterview/internal/version"
)

// Server routes API calls and serves the built UI for everything else.
type Server struct {
	kube    *kube.Client
	cache   *cluster.Cache
	actions *actions.Runner
	auth    *auth.Authenticator
	ui      fs.FS
	log     *slog.Logger

	diagnoser *diagnose.Diagnoser
	aiInfo    AIInfo
	// basePath is where the dashboard is served behind a shared host, such
	// as "/clusterview"; empty at the root.
	basePath string
}

// SetBasePath serves the dashboard under a path, such as "/clusterview", for
// an ingress that shares its host with other apps. Call before Handler.
func (s *Server) SetBasePath(p string) { s.basePath = p }

// New returns a server. k, c and a are nil when no cluster could be reached;
// the UI still loads and reports that. A nil authenticator means no login.
func New(k *kube.Client, c *cluster.Cache, a *actions.Runner, au *auth.Authenticator, ui fs.FS, log *slog.Logger) *Server {
	if au == nil {
		au, _ = auth.New(context.Background(), auth.Config{Mode: auth.ModeNone}, log)
	}
	return &Server{kube: k, cache: c, actions: a, auth: au, ui: ui, log: log}
}

// Handler returns the server's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /api/v1/version", s.version)
	mux.HandleFunc("GET /api/v1/cluster", s.cluster)
	mux.HandleFunc("GET /api/v1/snapshot", s.needsCache(s.snapshot))
	mux.HandleFunc("GET /api/v1/nodes", s.needsCache(s.nodes))
	mux.HandleFunc("GET /api/v1/events", s.needsCache(s.events))
	mux.HandleFunc("GET /api/v1/namespaces/{namespace}/pods/{pod}/logs", s.needsCache(s.podLogs))
	mux.HandleFunc("GET /api/v1/namespaces/{namespace}/pods/{pod}/logs/follow", s.needsCache(s.followLogs))
	mux.HandleFunc("GET /api/v1/services/logs", s.needsCache(s.serviceLogs))
	mux.HandleFunc("GET /api/v1/services/logs/follow", s.needsCache(s.followServiceLogs))

	s.actionRoutes(mux)
	s.aiRoutes(mux)
	s.auth.Routes(mux)
	mux.HandleFunc("GET /api/v1/me", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, auth.UserFrom(r.Context()))
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	})
	mux.Handle("/", spa(s.ui))
	return underPath(s.basePath, s.auth.Middleware(mux))
}

// underPath serves h under base, removing base from each request so routes
// and the UI work as at the root. The health checks also answer at the
// root, where the kubelet's probes ask.
func underPath(base string, h http.Handler) http.Handler {
	if base == "" {
		return h
	}
	inner := http.StripPrefix(base, h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := r.URL.Path; {
		case strings.HasPrefix(p, base+"/"):
			inner.ServeHTTP(w, r)
		case p == base:
			// The UI loads its files relative to the page, so the page needs
			// its trailing slash.
			target := base + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently) //nolint:gosec // G710: our own fixed path
		case p == "/healthz" || p == "/readyz":
			h.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

// readyz is ready once the first full read of the cluster is in memory.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.cache != nil && !s.cache.Ready() {
		http.Error(w, "syncing with the cluster", http.StatusServiceUnavailable)
		return
	}
	s.healthz(w, r)
}

func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"version": version.Version,
		"commit":  version.Commit,
	})
}

// ClusterInfo says whether clusterview can reach the cluster, and how.
type ClusterInfo struct {
	Connected     bool   `json:"connected"`
	Mode          string `json:"mode,omitempty"`
	Context       string `json:"context,omitempty"`
	ServerVersion string `json:"serverVersion,omitempty"`
	Error         string `json:"error,omitempty"`
}

func (s *Server) cluster(w http.ResponseWriter, _ *http.Request) {
	if s.kube == nil {
		writeJSON(w, http.StatusOK, ClusterInfo{Error: "no cluster configured"})
		return
	}
	info := ClusterInfo{Mode: string(s.kube.Mode), Context: s.kube.Context}
	v, err := s.kube.Clientset.Discovery().ServerVersion()
	if err != nil {
		s.log.Warn("cluster unreachable", "err", err)
		info.Error = err.Error()
		writeJSON(w, http.StatusOK, info)
		return
	}
	info.Connected = true
	info.ServerVersion = v.GitVersion
	writeJSON(w, http.StatusOK, info)
}

// spa serves files from the built UI and falls back to index.html, so routes
// the browser owns still load the app.
func spa(ui fs.FS) http.Handler {
	files := http.FileServerFS(ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name != "" {
			if st, err := fs.Stat(ui, name); err == nil && !st.IsDir() {
				files.ServeHTTP(w, r)
				return
			}
		}
		index, err := fs.ReadFile(ui, "index.html")
		if err != nil {
			http.Error(w, "UI not built. Run `make web` first.", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
