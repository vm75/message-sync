package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/vm75/message-sync/internal/controlstore"
	telegram "github.com/vm75/message-sync/internal/transport/telegram"
)

func (s *Server) handleTelegramTopicDiscovery(w http.ResponseWriter, r *http.Request) {
	if s.controlDB == nil {
		WriteError(w, http.StatusServiceUnavailable, "control database unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	remoteID := strings.TrimSpace(r.URL.Query().Get("remoteId"))
	if id == "" || remoteID == "" {
		WriteError(w, http.StatusBadRequest, "connection id and remoteId are required")
		return
	}
	var transportName, mode string
	var enabled bool
	err := s.controlDB.QueryRowContext(r.Context(), `SELECT transport, integration_mode, enabled FROM transport_connections WHERE id = ?`, id).Scan(&transportName, &mode, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "connection not found")
		return
	}
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}
	if transportName != "telegram" {
		WriteError(w, http.StatusBadRequest, "not a Telegram connection")
		return
	}
	if !enabled {
		WriteError(w, http.StatusBadRequest, "connection is disabled")
		return
	}
	if controlstore.NormalizeIntegrationMode(transportName, mode) != controlstore.TelegramIntegrationModeMTProto {
		WriteError(w, http.StatusConflict, "full Telegram topic discovery is not supported by this connection")
		return
	}
	service, ok := s.connections.(telegramTopicDiscoveryService)
	if !ok || service == nil {
		WriteError(w, http.StatusServiceUnavailable, "Telegram topic discovery is unavailable")
		return
	}
	topics, err := service.TelegramTopicDiscovery(r.Context(), id, remoteID)
	if err != nil {
		if errors.Is(err, telegram.ErrUnsupportedTarget) {
			WriteError(w, http.StatusBadRequest, "Telegram target is not a supported forum group")
			return
		}
		WriteError(w, http.StatusServiceUnavailable, "Telegram topic discovery is unavailable")
		return
	}
	if topics == nil {
		topics = []telegram.DiscoveredTopic{}
	}
	_ = WriteJSON(w, http.StatusOK, topics)
}
