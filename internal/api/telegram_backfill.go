package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/vm75/message-sync/internal/controlstore"
)

type telegramBackfillRequest struct {
	Endpoint    string `json:"endpoint"`
	MaxEvents   int    `json:"maxEvents"`
	MaxAgeHours int    `json:"maxAgeHours"`
}

func (s *Server) handleTelegramHistoricalBackfill(w http.ResponseWriter, r *http.Request) {
	if s.controlDB == nil {
		WriteError(w, http.StatusServiceUnavailable, "control database unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		WriteError(w, http.StatusBadRequest, "connection id is required")
		return
	}
	var transportName, mode string
	var enabled bool
	err := s.controlDB.QueryRowContext(r.Context(), `SELECT transport, integration_mode, enabled FROM transport_connections WHERE id=?`, id).Scan(&transportName, &mode, &enabled)
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
		WriteError(w, http.StatusConflict, "historical backfill is not supported by this connection")
		return
	}
	var req telegramBackfillRequest
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	if req.Endpoint == "" || req.MaxEvents <= 0 || req.MaxEvents > 1000 || req.MaxAgeHours <= 0 || req.MaxAgeHours > 24*30 {
		WriteError(w, http.StatusBadRequest, "endpoint and bounded maxEvents/maxAgeHours are required")
		return
	}
	svc, ok := s.connections.(telegramBackfillService)
	if !ok || svc == nil {
		WriteError(w, http.StatusServiceUnavailable, "Telegram historical backfill is unavailable")
		return
	}
	if err := svc.TelegramHistoricalBackfill(r.Context(), id, req.Endpoint, req.MaxEvents, time.Duration(req.MaxAgeHours)*time.Hour); err != nil {
		WriteError(w, http.StatusBadRequest, "Telegram historical backfill failed")
		return
	}
	s.audit(r, "telegram_mtproto_backfill", id)
	_ = WriteJSON(w, http.StatusOK, map[string]string{"status": "completed"})
}
