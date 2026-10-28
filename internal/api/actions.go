package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/clusterview/clusterview/internal/actions"
	"github.com/clusterview/clusterview/internal/auth"
)

// actionHeader must be on every action request. Browsers won't send a custom
// header cross-site without a CORS preflight, which this server never allows,
// so another site can't trigger actions through a visitor's browser.
const actionHeader = "X-Clusterview-Action"

func (s *Server) actionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/actions", s.actionsConfig)
	mux.HandleFunc("GET /api/v1/audit", s.auditLog)
	mux.HandleFunc("POST /api/v1/namespaces/{namespace}/pods/{pod}/restart", s.action(func(_ http.ResponseWriter, r *http.Request, actor string) (actions.Entry, error) {
		return s.actions.RestartPod(r.Context(), actor, r.PathValue("namespace"), r.PathValue("pod"))
	}))
	mux.HandleFunc("POST /api/v1/workloads/{kind}/{namespace}/{name}/restart", s.action(func(_ http.ResponseWriter, r *http.Request, actor string) (actions.Entry, error) {
		return s.actions.RolloutRestart(r.Context(), actor, r.PathValue("kind"), r.PathValue("namespace"), r.PathValue("name"))
	}))
	mux.HandleFunc("POST /api/v1/workloads/{kind}/{namespace}/{name}/scale", s.action(func(w http.ResponseWriter, r *http.Request, actor string) (actions.Entry, error) {
		var body struct {
			Replicas *int `json:"replicas"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil || body.Replicas == nil {
			return actions.Entry{}, actions.Refused{Reason: `Send {"replicas": <number>}.`}
		}
		return s.actions.Scale(r.Context(), actor, r.PathValue("kind"), r.PathValue("namespace"), r.PathValue("name"), *body.Replicas)
	}))
	mux.HandleFunc("POST /api/v1/workloads/CronJob/{namespace}/{name}/run", s.action(func(_ http.ResponseWriter, r *http.Request, actor string) (actions.Entry, error) {
		return s.actions.RunCronJob(r.Context(), actor, r.PathValue("namespace"), r.PathValue("name"))
	}))
}

func (s *Server) actionsConfig(w http.ResponseWriter, _ *http.Request) {
	if s.actions == nil {
		writeJSON(w, http.StatusOK, actions.Config{Namespaces: []string{}})
		return
	}
	writeJSON(w, http.StatusOK, s.actions.Config())
}

func (s *Server) auditLog(w http.ResponseWriter, _ *http.Request) {
	if s.actions == nil {
		writeJSON(w, http.StatusOK, []actions.Entry{})
		return
	}
	writeJSON(w, http.StatusOK, s.actions.Audit().List())
}

type actionFunc func(w http.ResponseWriter, r *http.Request, actor string) (actions.Entry, error)

func (s *Server) action(fn actionFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(actionHeader) == "" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing " + actionHeader + " header"})
			return
		}
		if s.actions == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no cluster connection"})
			return
		}
		user := auth.UserFrom(r.Context())
		if !user.CanAct() {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": user.Name + " can look but not change things. Ask to be added to auth.operators."})
			return
		}
		entry, err := fn(w, r, user.Name)
		var refused actions.Refused
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, entry)
		case errors.Is(err, actions.ErrDisabled), errors.Is(err, actions.ErrNamespace):
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		case errors.Is(err, actions.ErrNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		case errors.As(err, &refused):
			writeJSON(w, http.StatusConflict, map[string]string{"error": refused.Reason})
		default:
			s.log.Warn("action failed", "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		}
	}
}
