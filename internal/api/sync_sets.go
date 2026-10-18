package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/safelog"
)

type SyncSetDTO = config.SyncSet

type CreateSyncSetRequest struct {
	ID                 string          `json:"id"`
	Endpoints          []string        `json:"endpoints"`
	AnonymizeToDiscord bool            `json:"anonymizeToDiscord"`
	Settings           json.RawMessage `json:"settings,omitempty"`
}

type UpdateSyncSetRequest struct {
	ID                 string          `json:"id,omitempty"`
	Endpoints          *[]string       `json:"endpoints,omitempty"`
	AnonymizeToDiscord *bool           `json:"anonymizeToDiscord,omitempty"`
	Settings           json.RawMessage `json:"settings,omitempty"`
}

func (s *Server) handleListSyncSets(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, 503, "database unavailable")
		return
	}
	cfg, err := config.LoadRaw(r.Context(), s.db)
	if err != nil {
		WriteError(w, 500, "failed to read sync sets")
		return
	}
	_ = WriteJSON(w, http.StatusOK, cfg.SyncSets)
}

func (s *Server) handleGetSyncSet(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, 503, "database unavailable")
		return
	}
	cfg, err := config.LoadRaw(r.Context(), s.db)
	if err != nil {
		WriteError(w, 500, "failed to read sync sets")
		return
	}
	for _, set := range cfg.SyncSets {
		if set.ID == r.PathValue("id") {
			_ = WriteJSON(w, 200, set)
			return
		}
	}
	WriteError(w, 404, "sync set not found")
}

func (s *Server) handleCreateSyncSet(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, 503, "database unavailable")
		return
	}
	var req CreateSyncSetRequest
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, 400, "invalid request body")
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	if err := config.ValidateSyncSetID(req.ID); err != nil {
		WriteError(w, 400, err.Error())
		return
	}
	var existing string
	err := s.db.QueryRowContext(r.Context(), `SELECT id FROM sync_sets WHERE id=?`, req.ID).Scan(&existing)
	if err == nil {
		WriteError(w, 400, "sync set already exists")
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		WriteError(w, 500, "database error")
		return
	}
	if err := s.validateSyncSetEndpoints(r, req.ID, req.Endpoints, true); err != nil {
		WriteError(w, 400, err.Error())
		return
	}
	settings, err := mergeSyncSetSettings(config.DefaultSyncSetSettings(), req.Settings)
	if err != nil {
		WriteError(w, 400, err.Error())
		return
	}
	set := config.SyncSet{ID: req.ID, Endpoints: req.Endpoints, AnonymizeToDiscord: req.AnonymizeToDiscord, Settings: &settings}
	if settings.PrivateDMRelayEnabled {
		if principal, ok := principalFromContext(r.Context()); !ok || principal.Role != "admin" {
			WriteError(w, http.StatusForbidden, "private DM relay enablement requires an administrator")
			return
		}
	}
	if set.Endpoints == nil {
		set.Endpoints = []string{}
	}
	if !s.saveSyncSet(w, r, set, true) {
		return
	}
	_ = WriteJSON(w, http.StatusCreated, set)
}

func (s *Server) handleUpdateSyncSet(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, 503, "database unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	var req UpdateSyncSetRequest
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, 400, "invalid request body")
		return
	}
	if req.ID != "" && req.ID != id {
		WriteError(w, 400, "sync set id in body does not match path")
		return
	}
	cfg, err := config.LoadRaw(r.Context(), s.db)
	if err != nil {
		WriteError(w, 500, "failed to read sync sets")
		return
	}
	var current *config.SyncSet
	for index := range cfg.SyncSets {
		if cfg.SyncSets[index].ID == id {
			current = &cfg.SyncSets[index]
			break
		}
	}
	if current == nil {
		WriteError(w, 404, "sync set not found")
		return
	}
	set := *current
	if req.Endpoints != nil {
		set.Endpoints = *req.Endpoints
	}
	if req.AnonymizeToDiscord != nil {
		set.AnonymizeToDiscord = *req.AnonymizeToDiscord
	}
	if err := s.validateSyncSetEndpoints(r, id, set.Endpoints, false); err != nil {
		WriteError(w, 400, err.Error())
		return
	}
	settings, err := mergeSyncSetSettings(cfg.SettingsForSet(set), req.Settings)
	if err != nil {
		WriteError(w, 400, err.Error())
		return
	}
	if settings.PrivateDMRelayEnabled != cfg.SettingsForSet(*current).PrivateDMRelayEnabled {
		if principal, ok := principalFromContext(r.Context()); !ok || principal.Role != "admin" {
			WriteError(w, http.StatusForbidden, "private DM relay enablement requires an administrator")
			return
		}
	}
	set.Settings = &settings
	if !s.saveSyncSet(w, r, set, false) {
		return
	}
	_ = WriteJSON(w, http.StatusOK, set)
}

func (s *Server) saveSyncSet(w http.ResponseWriter, r *http.Request, set config.SyncSet, create bool) bool {
	payload, err := json.Marshal(set.Settings)
	if err != nil {
		WriteError(w, 500, "encode settings failed")
		return false
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		WriteError(w, 500, "database transaction error")
		return false
	}
	defer tx.Rollback()
	if create {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO sync_sets(id,anonymize_to_discord,settings_json) VALUES(?,?,?)`, set.ID, set.AnonymizeToDiscord, string(payload))
	} else {
		_, err = tx.ExecContext(r.Context(), `UPDATE sync_sets SET anonymize_to_discord=?,settings_json=? WHERE id=?`, set.AnonymizeToDiscord, string(payload), set.ID)
	}
	if err != nil {
		WriteError(w, 500, "failed to save sync set")
		return false
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE endpoints SET sync_set_id=NULL WHERE sync_set_id=?`, set.ID); err != nil {
		WriteError(w, 500, "failed to update memberships")
		return false
	}
	for _, alias := range set.Endpoints {
		if _, err = tx.ExecContext(r.Context(), `UPDATE endpoints SET sync_set_id=? WHERE alias=?`, set.ID, alias); err != nil {
			WriteError(w, 500, "failed to assign endpoint")
			return false
		}
	}
	if set.Settings.ChildContextDisplayMode == config.ChildContextDisplayOpaque {
		if _, err = tx.ExecContext(r.Context(), `DELETE FROM child_scope_labels WHERE endpoint_id IN (SELECT alias FROM endpoints WHERE sync_set_id=?)`, set.ID); err != nil {
			WriteError(w, 500, "failed to clear child labels")
			return false
		}
	}
	if err = tx.Commit(); err != nil {
		WriteError(w, 500, "failed to commit sync set")
		return false
	}
	if err := s.notifyConfigChange(r.Context()); err != nil {
		_ = WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"error":     "settings saved but runtime reload failed; retry Save",
			"persisted": true,
			"syncSetId": set.ID,
		})
		return false
	}
	return true
}

func (s *Server) handleDeleteSyncSet(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		WriteError(w, http.StatusBadRequest, "sync set id is required")
		return
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		safelog.Error(s.logger, "begin tx failed", "sync_set_api", err)
		WriteError(w, http.StatusInternalServerError, "database transaction error")
		return
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(r.Context(), `DELETE FROM endpoints WHERE sync_set_id = ?`, id); err != nil {
		safelog.Error(s.logger, "delete sync set endpoints failed", "sync_set_api", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}

	res, err := tx.ExecContext(r.Context(), `DELETE FROM sync_sets WHERE id = ?`, id)
	if err != nil {
		safelog.Error(s.logger, "delete sync set failed", "sync_set_api", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		safelog.Error(s.logger, "get rows affected failed", "sync_set_api", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}
	if rowsAffected == 0 {
		WriteError(w, http.StatusNotFound, "sync set not found")
		return
	}

	if err := tx.Commit(); err != nil {
		safelog.Error(s.logger, "commit delete sync set tx failed", "sync_set_api", err)
		WriteError(w, http.StatusInternalServerError, "failed to commit transaction")
		return
	}

	if s.controlDB != nil {
		if _, err := s.controlDB.ExecContext(r.Context(), `DELETE FROM membership_configs WHERE sync_set_id=?`, id); err != nil {
			safelog.Error(s.logger, "delete sync set membership config failed", "sync_set_api", err)
			WriteError(w, http.StatusInternalServerError, "failed to delete membership configuration")
			return
		}
	}

	s.notifyConfigChange(r.Context())
	_ = WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) validateSyncSetEndpoints(r *http.Request, currentSetID string, endpoints []string, isNew bool) error {
	seen := make(map[string]struct{}, len(endpoints))
	for _, alias := range endpoints {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			return errors.New("endpoint alias cannot be empty")
		}
		if _, dup := seen[alias]; dup {
			return fmt.Errorf("duplicate endpoint %q in sync set", alias)
		}
		seen[alias] = struct{}{}

		var assignedSet sql.NullString
		err := s.db.QueryRowContext(r.Context(), `SELECT sync_set_id FROM endpoints WHERE alias = ?`, alias).Scan(&assignedSet)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("endpoint %q does not exist", alias)
		} else if err != nil {
			return fmt.Errorf("query endpoint %q: %w", alias, err)
		}

		if assignedSet.Valid && strings.TrimSpace(assignedSet.String) != "" {
			if isNew || assignedSet.String != currentSetID {
				return fmt.Errorf("endpoint %q is already assigned to sync set %q", alias, assignedSet.String)
			}
		}
	}
	return nil
}
