package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/clusterview/clusterview/internal/assistant"
	"github.com/clusterview/clusterview/internal/auth"
)

func chatOwner(r *http.Request) string {
	u := auth.UserFrom(r.Context())
	return string(u.Mode) + ":" + u.Name
}
func (s *Server) chatReady(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get(actionHeader) == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing " + actionHeader + " header"})
		return false
	}
	if s.assistant == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Cluster assistant is not configured. Set ai.provider, ai.model and the provider key on the server."})
		return false
	}
	return true
}
func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	if !s.chatReady(w, r) {
		return
	}
	var body assistant.Request
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&body) != nil || dec.Decode(&struct{}{}) != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Send a message (up to 4000 bytes), optional sessionId and selectedServiceId."})
		return
	}
	out, err := s.assistant.Chat(r.Context(), chatOwner(r), body)
	if err != nil {
		chatError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
func (s *Server) forgetChat(w http.ResponseWriter, r *http.Request) {
	if !s.chatReady(w, r) {
		return
	}
	if err := s.assistant.Forget(chatOwner(r), r.PathValue("session")); err != nil {
		chatError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func chatError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	message := "The assistant could not complete the investigation. Retry or narrow the question."
	switch {
	case errors.Is(err, assistant.ErrInput):
		status = http.StatusBadRequest
		message = "Enter a message of up to 4000 bytes."
	case errors.Is(err, assistant.ErrSession):
		status = http.StatusNotFound
		message = assistant.ErrSession.Error()
	case errors.Is(err, assistant.ErrBusy):
		status = http.StatusTooManyRequests
		message = assistant.ErrBusy.Error()
	}
	writeJSON(w, status, map[string]string{"error": message})
}
