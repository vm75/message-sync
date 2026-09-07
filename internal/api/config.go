package api

import (
	"net/http"
	"strings"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/safelog"
)

type GlobalConfigDTO struct {
	UsernameMode            config.UsernameMode            `json:"usernameMode"`
	Media                   config.Media                   `json:"media"`
	Recovery                config.Recovery                `json:"recovery"`
	Storage                 config.Storage                 `json:"storage"`
	Polls                   config.Polls                   `json:"polls"`
	WhatsAppCleanup         config.WhatsAppCleanup         `json:"whatsappCleanup"`
	LocalPrefix             string                         `json:"localPrefix"`
	WhatsAppDeviceName      string                         `json:"whatsappDeviceName"`
	ChildContextDisplayMode config.ChildContextDisplayMode `json:"childContextDisplayMode"`
}

type UpdateConfigRequest struct {
	Identity                *config.Identity                `json:"identity,omitempty"`
	UsernameMode            *config.UsernameMode            `json:"usernameMode,omitempty"`
	Media                   *config.Media                   `json:"media,omitempty"`
	Recovery                *config.Recovery                `json:"recovery,omitempty"`
	Storage                 *config.Storage                 `json:"storage,omitempty"`
	Polls                   *config.Polls                   `json:"polls,omitempty"`
	WhatsAppCleanup         *config.WhatsAppCleanup         `json:"whatsappCleanup,omitempty"`
	LocalPrefix             *string                         `json:"localPrefix,omitempty"`
	WhatsAppDeviceName      *string                         `json:"whatsappDeviceName,omitempty"`
	ChildContextDisplayMode *config.ChildContextDisplayMode `json:"childContextDisplayMode,omitempty"`
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	cfg, err := config.LoadRaw(r.Context(), s.db)
	if err != nil {
		safelog.Error(s.logger, "load config failed", "config_load", err)
		WriteError(w, http.StatusInternalServerError, "failed to load config")
		return
	}

	dto := GlobalConfigDTO{
		UsernameMode:            cfg.Identity.UsernameMode,
		Media:                   cfg.Media,
		Recovery:                cfg.Recovery,
		Storage:                 cfg.Storage,
		Polls:                   cfg.Polls,
		WhatsAppCleanup:         cfg.WhatsAppCleanup,
		LocalPrefix:             cfg.LocalPrefix,
		WhatsAppDeviceName:      cfg.WhatsAppDeviceName,
		ChildContextDisplayMode: cfg.ChildContextDisplayMode,
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
		safelog.Error(s.logger, "load current config failed", "config_load", err)
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

	polls := currentCfg.Polls
	if req.Polls != nil {
		polls = *req.Polls
	}
	polls.AggregationTrigger = strings.Join(strings.Fields(polls.AggregationTrigger), " ")
	if polls.AggregationTrigger == "" {
		polls.AggregationTrigger = "aggregate-response"
	}
	if err := config.ValidateAggregationTrigger(polls.AggregationTrigger); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	whatsappCleanup := currentCfg.WhatsAppCleanup
	if req.WhatsAppCleanup != nil {
		whatsappCleanup = *req.WhatsAppCleanup
		if whatsappCleanup.RetentionDays < 1 {
			WriteError(w, http.StatusBadRequest, "whatsappCleanup.retentionDays must be positive")
			return
		}
	}
	localPrefix := currentCfg.LocalPrefix
	if req.LocalPrefix != nil {
		localPrefix = strings.Join(strings.Fields(*req.LocalPrefix), " ")
	}
	if err := config.ValidateLocalPrefix(localPrefix); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	whatsappDeviceName := currentCfg.WhatsAppDeviceName
	if req.WhatsAppDeviceName != nil {
		whatsappDeviceName = *req.WhatsAppDeviceName
	}
	if strings.TrimSpace(whatsappDeviceName) == "" {
		whatsappDeviceName = config.DefaultWhatsAppDeviceName
	}
	if err := config.ValidateWhatsAppDeviceName(whatsappDeviceName); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	childContextMode := currentCfg.ChildContextDisplayMode
	if req.ChildContextDisplayMode != nil {
		childContextMode = *req.ChildContextDisplayMode
	}
	if !childContextMode.IsValid() {
		WriteError(w, http.StatusBadRequest, "childContextDisplayMode must be opaque or friendly")
		return
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to update config")
		return
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO global_config (id, username_mode, media_enabled, media_max_size_mb, recovery_enabled, recovery_max_age_hours, recovery_max_messages_per_group, storage_message_retention_days, poll_aggregation_trigger, whatsapp_chat_cleanup_enabled, whatsapp_chat_retention_days, local_message_prefix, whatsapp_device_name, child_context_display_mode)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			username_mode = excluded.username_mode,
			media_enabled = excluded.media_enabled,
			media_max_size_mb = excluded.media_max_size_mb,
			recovery_enabled = excluded.recovery_enabled,
			recovery_max_age_hours = excluded.recovery_max_age_hours,
			recovery_max_messages_per_group = excluded.recovery_max_messages_per_group,
			storage_message_retention_days = excluded.storage_message_retention_days,
			poll_aggregation_trigger = excluded.poll_aggregation_trigger,
			whatsapp_chat_cleanup_enabled = excluded.whatsapp_chat_cleanup_enabled,
			whatsapp_chat_retention_days = excluded.whatsapp_chat_retention_days,
			local_message_prefix = excluded.local_message_prefix,
			whatsapp_device_name = excluded.whatsapp_device_name,
			child_context_display_mode = excluded.child_context_display_mode
	`, string(mode), media.Enabled, media.MaxSizeMB, recovery.Enabled, recovery.MaxAgeHours, recovery.MaxMessagesPerGroup, storage.MessageRetentionDays, polls.AggregationTrigger, whatsappCleanup.Enabled, whatsappCleanup.RetentionDays, localPrefix, whatsappDeviceName, string(childContextMode))
	if err != nil {
		safelog.Error(s.logger, "save global_config failed", "config_save", err)
		WriteError(w, http.StatusInternalServerError, "failed to update config")
		return
	}
	if currentCfg.ChildContextDisplayMode == config.ChildContextDisplayFriendly && childContextMode == config.ChildContextDisplayOpaque {
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM child_scope_labels`); err != nil {
			WriteError(w, http.StatusInternalServerError, "failed to clear child scope labels")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to update config")
		return
	}

	s.notifyConfigChange(r.Context())

	dto := GlobalConfigDTO{
		UsernameMode:            mode,
		Media:                   media,
		Recovery:                recovery,
		Storage:                 storage,
		Polls:                   polls,
		WhatsAppCleanup:         whatsappCleanup,
		LocalPrefix:             localPrefix,
		WhatsAppDeviceName:      whatsappDeviceName,
		ChildContextDisplayMode: childContextMode,
	}

	_ = WriteJSON(w, http.StatusOK, dto)
}
