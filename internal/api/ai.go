package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/clusterview/clusterview/internal/diagnose"
)

// AIInfo tells the UI whether Find error is available, and with what.
type AIInfo struct {
	Enabled  bool   `json:"enabled"`
	Model    string `json:"model,omitempty"`
	Describe bool   `json:"describe"`
}

// SetAI turns on Find error. Call before Handler.
func (s *Server) SetAI(d *diagnose.Diagnoser, info AIInfo) {
	s.diagnoser = d
	s.aiInfo = info
}

func (s *Server) aiRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/ai", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.aiInfo)
	})
	mux.HandleFunc("POST /api/v1/diagnose", s.findError)
}

// findError asks the model why a service is broken. It spends money on the
// configured key, so like actions it needs the custom header that a
// cross-site form or script can't send.
func (s *Server) findError(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(actionHeader) == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing " + actionHeader + " header"})
		return
	}
	if s.diagnoser == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "AI isn't set up: set ai.provider and a key (see the README)."})
		return
	}
	var body struct {
		ServiceID string `json:"serviceId"`
		Fresh     bool   `json:"fresh"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil || body.ServiceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `Send {"serviceId": "<id>"}.`})
		return
	}
	d, err := s.diagnoser.FindError(r.Context(), body.ServiceID, body.Fresh)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, d)
	case errors.Is(err, diagnose.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "That service is gone."})
	case errors.Is(err, diagnose.ErrBusy):
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
	default:
		s.log.Warn("find error failed", "service", body.ServiceID, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
	}
}
