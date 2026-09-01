package api

import (
	"net/http"

	"github.com/vm75/message-sync/internal/safelog"
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
		safelog.Error(s.logger, "whatsapp pair failed", "whatsapp_pair", err)
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
		safelog.Error(s.logger, "whatsapp cancel pair failed", "whatsapp_cancel_pair", err)
		WriteError(w, http.StatusInternalServerError, "failed to cancel pairing")
		return
	}
	_ = WriteJSON(w, http.StatusOK, map[string]string{"status": "unpaired"})
}

func (s *Server) handleWhatsAppLogout(w http.ResponseWriter, r *http.Request) {
	if s.whatsapp == nil {
		WriteError(w, http.StatusServiceUnavailable, "whatsapp service unavailable")
		return
	}
	if err := s.whatsapp.Logout(r.Context()); err != nil {
		safelog.Error(s.logger, "whatsapp logout failed", "whatsapp_logout", err)
		WriteError(w, http.StatusInternalServerError, "failed to logout whatsapp session")
		return
	}
	_ = WriteJSON(w, http.StatusOK, map[string]string{"status": "unpaired"})
}

func (s *Server) handleWhatsAppGroups(w http.ResponseWriter, r *http.Request) {
	if s.whatsapp == nil {
		WriteError(w, http.StatusServiceUnavailable, "whatsapp service unavailable")
		return
	}
	groups, err := s.whatsapp.GetJoinedGroups(r.Context())
	if err != nil {
		safelog.Error(s.logger, "get joined groups failed", "whatsapp_groups", err)
		WriteError(w, http.StatusInternalServerError, "failed to get joined groups")
		return
	}
	if groups == nil {
		groups = []WhatsAppGroup{}
	}
	_ = WriteJSON(w, http.StatusOK, groups)
}
