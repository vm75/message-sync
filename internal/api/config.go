package api

import (
	"net/http"

	"github.com/vm75/message-sync/internal/config"
)

type GlobalConfigDTO struct {
	UsernameMode config.UsernameMode `json:"usernameMode"`
	Media        config.Media        `json:"media"`
	Recovery     config.Recovery     `json:"recovery"`
	Storage      config.Storage      `json:"storage"`
}

type UpdateConfigRequest struct {
	Identity     *config.Identity     `json:"identity,omitempty"`
	UsernameMode *config.UsernameMode `json:"usernameMode,omitempty"`
	Media        *config.Media        `json:"media,omitempty"`
	Recovery     *config.Recovery     `json:"recovery,omitempty"`
	Storage      *config.Storage      `json:"storage,omitempty"`
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	cfg, err := config.LoadRaw(r.Context(), s.db)
	if err != nil {
		s.logger.Error("load config failed", "error", err.Error())
		WriteError(w, http.StatusInternalServerError, "failed to load config")
		return
	}

	dto := GlobalConfigDTO{
		UsernameMode: cfg.Identity.UsernameMode,
		Media:        cfg.Media,
		Recovery:     cfg.Recovery,
		Storage:      cfg.Storage,
	}

	_ = WriteJSON(w, http.StatusOK, dto)
}

func (s *Server) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	var req UpdateConfigRequest
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	currentCfg, err := config.LoadRaw(r.Context(), s.db)
	if err != nil {
		s.logger.Error("load current config failed", "error", err.Error())
		WriteError(w, http.StatusInternalServerError, "failed to load current config")
		return
	}

	var mode config.UsernameMode
	if req.UsernameMode != nil && *req.UsernameMode != "" {
		mode = *req.UsernameMode
	} else if req.Identity != nil && req.Identity.UsernameMode != "" {
		mode = req.Identity.UsernameMode
	} else {
		mode = currentCfg.Identity.UsernameMode
	}

	if !mode.IsValid() {
		WriteError(w, http.StatusBadRequest, "usernameMode must be push_name or hash")
		return
	}

	media := currentCfg.Media
	if req.Media != nil {
		media = *req.Media
	}
	if media.MaxSizeMB < 1 {
		WriteError(w, http.StatusBadRequest, "media.maxSizeMB must be positive")
		return
	}

	recovery := currentCfg.Recovery
	if req.Recovery != nil {
		recovery = *req.Recovery
	}
	if recovery.MaxAgeHours < 1 {
		WriteError(w, http.StatusBadRequest, "recovery.maxAgeHours must be positive")
		return
	}
	if recovery.MaxMessagesPerGroup < 1 {
		WriteError(w, http.StatusBadRequest, "recovery.maxMessagesPerGroup must be positive")
		return
	}

	storage := currentCfg.Storage
	if req.Storage != nil {
		storage = *req.Storage
	}
	if storage.MessageRetentionDays < 1 {
		WriteError(w, http.StatusBadRequest, "storage.messageRetentionDays must be positive")
		return
	}

	_, err = s.db.ExecContext(r.Context(), `
		INSERT INTO global_config (id, username_mode, media_enabled, media_max_size_mb, recovery_enabled, recovery_max_age_hours, recovery_max_messages_per_group, storage_message_retention_days)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			username_mode = excluded.username_mode,
			media_enabled = excluded.media_enabled,
			media_max_size_mb = excluded.media_max_size_mb,
			recovery_enabled = excluded.recovery_enabled,
			recovery_max_age_hours = excluded.recovery_max_age_hours,
			recovery_max_messages_per_group = excluded.recovery_max_messages_per_group,
			storage_message_retention_days = excluded.storage_message_retention_days
	`, string(mode), media.Enabled, media.MaxSizeMB, recovery.Enabled, recovery.MaxAgeHours, recovery.MaxMessagesPerGroup, storage.MessageRetentionDays)
	if err != nil {
		s.logger.Error("save global_config failed", "error", err.Error())
		WriteError(w, http.StatusInternalServerError, "failed to update config")
		return
	}

	s.notifyConfigChange(r.Context())

	dto := GlobalConfigDTO{
		UsernameMode: mode,
		Media:        media,
		Recovery:     recovery,
		Storage:      storage,
	}

	_ = WriteJSON(w, http.StatusOK, dto)
}
