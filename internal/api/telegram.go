package api

import (
	"net/http"

	"github.com/vm75/message-sync/internal/safelog"
	telegram "github.com/vm75/message-sync/internal/transport/telegram"
)

func (s *Server) handleTelegramStatus(w http.ResponseWriter, r *http.Request) {
	if s.telegram == nil {
		status := telegram.AdminStatus{
			TokenConfigured:    false,
			Running:            false,
			Status:             "not_configured",
			Endpoints:          []telegram.EndpointReadiness{},
			PrivacyModeKnown:   false,
			VisibilityGuidance: telegram.VisibilityGuidance,
		}
		_ = WriteJSON(w, http.StatusOK, status)
		return
	}

	status := s.telegram.AdminStatus(r.Context())
	if status.Endpoints == nil {
		status.Endpoints = []telegram.EndpointReadiness{}
	}
	if status.VisibilityGuidance == "" {
		status.VisibilityGuidance = telegram.VisibilityGuidance
	}
	_ = WriteJSON(w, http.StatusOK, status)
}

func (s *Server) handleTelegramChats(w http.ResponseWriter, r *http.Request) {
	if s.telegram == nil {
		WriteError(w, http.StatusServiceUnavailable, "Telegram bot is not configured")
		return
	}

	status := s.telegram.AdminStatus(r.Context())
	if !status.Running {
		WriteError(w, http.StatusServiceUnavailable, "Telegram bot is configured but long polling is not running")
		return
	}

	chats, err := s.telegram.DiscoverChats(r.Context())
	if err != nil {
		safelog.Error(s.logger, "Telegram chat discovery failed", "telegram_discovery", err)
		WriteError(w, http.StatusInternalServerError, "failed to discover Telegram chats")
		return
	}
	if chats == nil {
		chats = []telegram.DiscoveredChat{}
	}
	_ = WriteJSON(w, http.StatusOK, chats)
}
