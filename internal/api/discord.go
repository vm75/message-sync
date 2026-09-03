package api

import (
	"net/http"

	"github.com/vm75/message-sync/internal/safelog"
	discord "github.com/vm75/message-sync/internal/transport/discord"
)

func (s *Server) handleDiscordStatus(w http.ResponseWriter, r *http.Request) {
	if s.discord == nil {
		_ = WriteJSON(w, http.StatusOK, discord.AdminStatus{
			Configured: false,
			Connected:  false,
			Status:     "not_configured",
			Webhooks:   []discord.EndpointWebhookStatus{},
		})
		return
	}

	status := s.discord.AdminStatus(r.Context())
	if status.Webhooks == nil {
		status.Webhooks = []discord.EndpointWebhookStatus{}
	}
	_ = WriteJSON(w, http.StatusOK, status)
}

func (s *Server) handleDiscordChannels(w http.ResponseWriter, r *http.Request) {
	if s.discord == nil {
		WriteError(w, http.StatusServiceUnavailable, "Discord bot is not configured")
		return
	}

	status := s.discord.AdminStatus(r.Context())
	if !status.Connected {
		WriteError(w, http.StatusServiceUnavailable, "Discord bot is configured but not connected")
		return
	}

	channels, err := s.discord.DiscoverChannels(r.Context())
	if err != nil {
		safelog.Error(s.logger, "Discord channel discovery failed", "discord_discovery", err)
		WriteError(w, http.StatusInternalServerError, "failed to discover Discord channels")
		return
	}
	if channels == nil {
		channels = []discord.DiscoveredChannel{}
	}
	_ = WriteJSON(w, http.StatusOK, channels)
}
