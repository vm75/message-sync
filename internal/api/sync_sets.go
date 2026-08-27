package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/safelog"
)

type SyncSetDTO struct {
	ID     string   `json:"id"`
	Groups []string `json:"groups"`
}

type CreateSyncSetRequest struct {
	ID     string   `json:"id"`
	Groups []string `json:"groups"`
}

type UpdateSyncSetRequest struct {
	ID     string   `json:"id,omitempty"`
	Groups []string `json:"groups"`
}

func (s *Server) handleListSyncSets(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	setRows, err := s.db.QueryContext(r.Context(), `SELECT id FROM sync_sets ORDER BY id ASC`)
	if err != nil {
		safelog.Error(s.logger, "query sync_sets failed", "sync_set_api", err)
		WriteError(w, http.StatusInternalServerError, "failed to query sync sets")
		return
	}

	setIDs := make([]string, 0)
	for setRows.Next() {
		var id string
		if err := setRows.Scan(&id); err != nil {
			setRows.Close()
			safelog.Error(s.logger, "scan sync_set failed", "sync_set_api", err)
			WriteError(w, http.StatusInternalServerError, "failed to read sync set")
			return
		}
		setIDs = append(setIDs, id)
	}
	if err := setRows.Err(); err != nil {
		setRows.Close()
		safelog.Error(s.logger, "iterate sync_sets failed", "sync_set_api", err)
		WriteError(w, http.StatusInternalServerError, "failed to iterate sync sets")
		return
	}
	setRows.Close()

	groupRows, err := s.db.QueryContext(r.Context(), `SELECT alias, sync_set_id FROM endpoints WHERE sync_set_id IS NOT NULL ORDER BY alias ASC`)
	if err != nil {
		safelog.Error(s.logger, "query groups for sync_sets failed", "sync_set_api", err)
		WriteError(w, http.StatusInternalServerError, "failed to query groups")
		return
	}
	defer groupRows.Close()

	syncSetGroups := make(map[string][]string)
	for groupRows.Next() {
		var alias, syncSetID string
		if err := groupRows.Scan(&alias, &syncSetID); err != nil {
			safelog.Error(s.logger, "scan group alias failed", "sync_set_api", err)
			WriteError(w, http.StatusInternalServerError, "failed to read group memberships")
			return
		}
		syncSetGroups[syncSetID] = append(syncSetGroups[syncSetID], alias)
	}
	if err := groupRows.Err(); err != nil {
		safelog.Error(s.logger, "iterate groups failed", "sync_set_api", err)
		WriteError(w, http.StatusInternalServerError, "failed to iterate groups")
		return
	}

	syncSets := make([]SyncSetDTO, 0, len(setIDs))
	for _, id := range setIDs {
		groups := syncSetGroups[id]
		if groups == nil {
			groups = []string{}
		}
		syncSets = append(syncSets, SyncSetDTO{
			ID:     id,
			Groups: groups,
		})
	}

	_ = WriteJSON(w, http.StatusOK, syncSets)
}

func (s *Server) handleGetSyncSet(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		WriteError(w, http.StatusBadRequest, "sync set id is required")
		return
	}

	var exists string
	err := s.db.QueryRowContext(r.Context(), `SELECT id FROM sync_sets WHERE id = ?`, id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "sync set not found")
		return
	} else if err != nil {
		safelog.Error(s.logger, "query sync set failed", "sync_set_api", err)
		WriteError(w, http.StatusInternalServerError, "failed to query sync set")
		return
	}

	rows, err := s.db.QueryContext(r.Context(), `SELECT alias FROM endpoints WHERE sync_set_id = ? ORDER BY alias ASC`, id)
	if err != nil {
		safelog.Error(s.logger, "query sync set groups failed", "sync_set_api", err)
		WriteError(w, http.StatusInternalServerError, "failed to query sync set groups")
		return
	}
	defer rows.Close()

	groups := make([]string, 0)
	for rows.Next() {
		var alias string
		if err := rows.Scan(&alias); err != nil {
			safelog.Error(s.logger, "scan sync set group failed", "sync_set_api", err)
			WriteError(w, http.StatusInternalServerError, "failed to read group")
			return
		}
		groups = append(groups, alias)
	}

	_ = WriteJSON(w, http.StatusOK, SyncSetDTO{
		ID:     id,
		Groups: groups,
	})
}

func (s *Server) handleCreateSyncSet(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	var req CreateSyncSetRequest
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.ID = strings.TrimSpace(req.ID)
	if err := config.ValidateSyncSetID(req.ID); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Check if sync_set id already exists
	var existing string
	err := s.db.QueryRowContext(r.Context(), `SELECT id FROM sync_sets WHERE id = ?`, req.ID).Scan(&existing)
	if err == nil {
		WriteError(w, http.StatusBadRequest, "sync set already exists")
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		safelog.Error(s.logger, "check existing sync set failed", "sync_set_api", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}

	// Validate groups in request
	if err := s.validateSyncSetGroups(r, req.ID, req.Groups, true); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		safelog.Error(s.logger, "begin tx failed", "sync_set_api", err)
		WriteError(w, http.StatusInternalServerError, "database transaction error")
		return
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(r.Context(), `INSERT INTO sync_sets (id) VALUES (?)`, req.ID); err != nil {
		safelog.Error(s.logger, "insert sync set failed", "sync_set_api", err)
		WriteError(w, http.StatusInternalServerError, "failed to insert sync set")
		return
	}

	for _, alias := range req.Groups {
		alias = strings.TrimSpace(alias)
		if _, err := tx.ExecContext(r.Context(), `UPDATE endpoints SET sync_set_id = ? WHERE alias = ?`, req.ID, alias); err != nil {
			safelog.Error(s.logger, "assign group to sync set failed", "sync_set_api", err)
			WriteError(w, http.StatusInternalServerError, "failed to assign groups")
			return
		}
	}

	if err := tx.Commit(); err != nil {
		safelog.Error(s.logger, "commit tx failed", "sync_set_api", err)
		WriteError(w, http.StatusInternalServerError, "failed to commit transaction")
		return
	}

	s.notifyConfigChange(r.Context())

	groups := req.Groups
	if groups == nil {
		groups = []string{}
	}
	_ = WriteJSON(w, http.StatusCreated, SyncSetDTO{
		ID:     req.ID,
		Groups: groups,
	})
}

func (s *Server) handleUpdateSyncSet(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		WriteError(w, http.StatusBadRequest, "sync set id is required")
		return
	}

	var req UpdateSyncSetRequest
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.ID != "" && req.ID != id {
		WriteError(w, http.StatusBadRequest, "sync set id in body does not match path")
		return
	}

	// Check if sync_set id exists
	var existing string
	err := s.db.QueryRowContext(r.Context(), `SELECT id FROM sync_sets WHERE id = ?`, id).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "sync set not found")
		return
	} else if err != nil {
		safelog.Error(s.logger, "check sync set existence failed", "sync_set_api", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}

	// Validate groups
	if err := s.validateSyncSetGroups(r, id, req.Groups, false); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		safelog.Error(s.logger, "begin tx failed", "sync_set_api", err)
		WriteError(w, http.StatusInternalServerError, "database transaction error")
		return
	}
	defer tx.Rollback()

	// Clear current members
	if _, err := tx.ExecContext(r.Context(), `UPDATE endpoints SET sync_set_id = NULL WHERE sync_set_id = ?`, id); err != nil {
		safelog.Error(s.logger, "clear old sync set memberships failed", "sync_set_api", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}

	// Assign new members
	for _, alias := range req.Groups {
		alias = strings.TrimSpace(alias)
		if _, err := tx.ExecContext(r.Context(), `UPDATE endpoints SET sync_set_id = ? WHERE alias = ?`, id, alias); err != nil {
			safelog.Error(s.logger, "assign group to sync set failed", "sync_set_api", err)
			WriteError(w, http.StatusInternalServerError, "failed to assign groups")
			return
		}
	}

	if err := tx.Commit(); err != nil {
		safelog.Error(s.logger, "commit tx failed", "sync_set_api", err)
		WriteError(w, http.StatusInternalServerError, "failed to commit transaction")
		return
	}

	s.notifyConfigChange(r.Context())

	groups := req.Groups
	if groups == nil {
		groups = []string{}
	}
	_ = WriteJSON(w, http.StatusOK, SyncSetDTO{
		ID:     id,
		Groups: groups,
	})
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

	// Unassign groups
	if _, err := tx.ExecContext(r.Context(), `UPDATE endpoints SET sync_set_id = NULL WHERE sync_set_id = ?`, id); err != nil {
		safelog.Error(s.logger, "unassign sync set groups failed", "sync_set_api", err)
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

	s.notifyConfigChange(r.Context())
	_ = WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) validateSyncSetGroups(r *http.Request, currentSetID string, groups []string, isNew bool) error {
	seen := make(map[string]struct{}, len(groups))
	for _, alias := range groups {
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
