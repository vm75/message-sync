package api

import (
	"net/http"
)

func (s *Server) handleWhatsAppStatus(w http.ResponseWriter, r *http.Request) {
	if s.whatsapp == nil {
		WriteError(w, http.StatusServiceUnavailable, "whatsapp service unavailable")
		return
	}
	status := s.whatsapp.Status(r.Context())
	_ = WriteJSON(w, http.StatusOK, status)
}

func (s *Server) handleWhatsAppPair(w http.ResponseWriter, r *http.Request) {
	if s.whatsapp == nil {
		WriteError(w, http.StatusServiceUnavailable, "whatsapp service unavailable")
		return
	}
	resp, err := s.whatsapp.Pair(r.Context())
	if err != nil {
		s.logger.Error("whatsapp pair failed", "error", err.Error())
		WriteError(w, http.StatusInternalServerError, "failed to initiate pairing")
		return
	}
	_ = WriteJSON(w, http.StatusOK, resp)
}

func (s *Server) handleWhatsAppCancelPair(w http.ResponseWriter, r *http.Request) {
	if s.whatsapp == nil {
		WriteError(w, http.StatusServiceUnavailable, "whatsapp service unavailable")
		return
	}
	if err := s.whatsapp.CancelPair(r.Context()); err != nil {
		s.logger.Error("whatsapp cancel pair failed", "error", err.Error())
		WriteError(w, http.StatusInternalServerError, "failed to cancel pairing")
		return
	}
	_ = WriteJSON(w, http.StatusOK, map[string]string{"status": "unpaired"})
}
