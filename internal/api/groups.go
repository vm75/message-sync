package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/safelog"
)

type GroupDTO struct {
	Alias     string  `json:"alias"`
	JID       string  `json:"jid"`
	SyncSetID *string `json:"syncSetId,omitempty"`
}

type CreateGroupRequest struct {
	Alias     string  `json:"alias"`
	JID       string  `json:"jid"`
	SyncSetID *string `json:"syncSetId,omitempty"`
}

type UpdateGroupRequest struct {
	Alias     string  `json:"alias,omitempty"`
	JID       string  `json:"jid"`
	SyncSetID *string `json:"syncSetId,omitempty"`
}

func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	rows, err := s.db.QueryContext(r.Context(), `SELECT alias, remote_id, sync_set_id FROM endpoints WHERE transport = 'whatsapp' ORDER BY alias ASC`)
	if err != nil {
		safelog.Error(s.logger, "query groups failed", "legacy_group_api", err)
		WriteError(w, http.StatusInternalServerError, "failed to query groups")
		return
	}
	defer rows.Close()

	groups := make([]GroupDTO, 0)
	for rows.Next() {
		var alias, jid string
		var syncSetID sql.NullString
		if err := rows.Scan(&alias, &jid, &syncSetID); err != nil {
			safelog.Error(s.logger, "scan group failed", "legacy_group_api", err)
			WriteError(w, http.StatusInternalServerError, "failed to read groups")
			return
		}
		dto := GroupDTO{
			Alias: alias,
			JID:   jid,
		}
		if syncSetID.Valid && strings.TrimSpace(syncSetID.String) != "" {
			val := syncSetID.String
			dto.SyncSetID = &val
		}
		groups = append(groups, dto)
	}
	if err := rows.Err(); err != nil {
		safelog.Error(s.logger, "iterate groups failed", "legacy_group_api", err)
		WriteError(w, http.StatusInternalServerError, "failed to iterate groups")
		return
	}

	_ = WriteJSON(w, http.StatusOK, groups)
}

func (s *Server) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	alias := strings.TrimSpace(r.PathValue("alias"))
	if alias == "" {
		WriteError(w, http.StatusBadRequest, "group alias is required")
		return
	}

	var jid string
	var syncSetID sql.NullString
	err := s.db.QueryRowContext(r.Context(), `SELECT remote_id, sync_set_id FROM endpoints WHERE alias = ? AND transport = 'whatsapp'`, alias).Scan(&jid, &syncSetID)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "group not found")
		return
	}
	if err != nil {
		safelog.Error(s.logger, "query group failed", "legacy_group_api", err)
		WriteError(w, http.StatusInternalServerError, "failed to query group")
		return
	}

	dto := GroupDTO{
		Alias: alias,
		JID:   jid,
	}
	if syncSetID.Valid && strings.TrimSpace(syncSetID.String) != "" {
		val := syncSetID.String
		dto.SyncSetID = &val
	}

	_ = WriteJSON(w, http.StatusOK, dto)
}

func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	var req CreateGroupRequest
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Alias = strings.TrimSpace(req.Alias)
	req.JID = strings.TrimSpace(req.JID)

	if err := config.ValidateAlias(req.Alias); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := config.ValidateGroupJID(req.JID); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	var existing string
	err := s.db.QueryRowContext(r.Context(), `SELECT alias FROM endpoints WHERE alias = ?`, req.Alias).Scan(&existing)
	if err == nil {
		WriteError(w, http.StatusBadRequest, "group alias already exists")
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		safelog.Error(s.logger, "check existing group failed", "legacy_group_api", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}

	var existingAliasForJID string
	err = s.db.QueryRowContext(r.Context(), `SELECT alias FROM endpoints WHERE transport = 'whatsapp' AND remote_id = ?`, req.JID).Scan(&existingAliasForJID)
	if err == nil {
		WriteError(w, http.StatusBadRequest, "a group alias is already defined for this WhatsApp group")
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		safelog.Error(s.logger, "check existing group JID failed", "legacy_group_api", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}

	var syncSetVal any
	if req.SyncSetID != nil && strings.TrimSpace(*req.SyncSetID) != "" {
		setID := strings.TrimSpace(*req.SyncSetID)
		var exists string
		err := s.db.QueryRowContext(r.Context(), `SELECT id FROM sync_sets WHERE id = ?`, setID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			WriteError(w, http.StatusBadRequest, "sync set does not exist")
			return
		} else if err != nil {
			safelog.Error(s.logger, "check sync set failed", "legacy_group_api", err)
			WriteError(w, http.StatusInternalServerError, "database error")
			return
		}
		syncSetVal = setID
	}

	_, err = s.db.ExecContext(r.Context(), `INSERT INTO endpoints (alias, transport, remote_id, sync_set_id) VALUES (?, 'whatsapp', ?, ?)`, req.Alias, req.JID, syncSetVal)
	if err != nil {
		safelog.Error(s.logger, "insert group failed", "legacy_group_api", err)
		WriteError(w, http.StatusInternalServerError, "failed to create group")
		return
	}

	s.notifyConfigChange(r.Context())

	dto := GroupDTO{
		Alias: req.Alias,
		JID:   req.JID,
	}
	if syncSetVal != nil {
		val := syncSetVal.(string)
		dto.SyncSetID = &val
	}

	_ = WriteJSON(w, http.StatusCreated, dto)
}

func (s *Server) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	alias := strings.TrimSpace(r.PathValue("alias"))
	if alias == "" {
		WriteError(w, http.StatusBadRequest, "group alias is required")
		return
	}

	var req UpdateGroupRequest
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Alias != "" && req.Alias != alias {
		WriteError(w, http.StatusBadRequest, "group alias in body does not match path")
		return
	}

	req.JID = strings.TrimSpace(req.JID)
	if err := config.ValidateGroupJID(req.JID); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	var existing string
	err := s.db.QueryRowContext(r.Context(), `SELECT alias FROM endpoints WHERE alias = ? AND transport = 'whatsapp'`, alias).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "group not found")
		return
	} else if err != nil {
		safelog.Error(s.logger, "check group existence failed", "legacy_group_api", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}

	var otherAlias string
	err = s.db.QueryRowContext(r.Context(), `SELECT alias FROM endpoints WHERE transport = 'whatsapp' AND remote_id = ? AND alias != ?`, req.JID, alias).Scan(&otherAlias)
	if err == nil {
		WriteError(w, http.StatusBadRequest, "a group alias is already defined for this WhatsApp group")
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		safelog.Error(s.logger, "check other group JID failed", "legacy_group_api", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}

	var syncSetVal any
	if req.SyncSetID != nil && strings.TrimSpace(*req.SyncSetID) != "" {
		setID := strings.TrimSpace(*req.SyncSetID)
		var exists string
		err := s.db.QueryRowContext(r.Context(), `SELECT id FROM sync_sets WHERE id = ?`, setID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			WriteError(w, http.StatusBadRequest, "sync set does not exist")
			return
		} else if err != nil {
			safelog.Error(s.logger, "check sync set failed", "legacy_group_api", err)
			WriteError(w, http.StatusInternalServerError, "database error")
			return
		}
		syncSetVal = setID
	}

	_, err = s.db.ExecContext(r.Context(), `UPDATE endpoints SET remote_id = ?, sync_set_id = ? WHERE alias = ? AND transport = 'whatsapp'`, req.JID, syncSetVal, alias)
	if err != nil {
		safelog.Error(s.logger, "update group failed", "legacy_group_api", err)
		WriteError(w, http.StatusInternalServerError, "failed to update group")
		return
	}

	s.notifyConfigChange(r.Context())

	dto := GroupDTO{
		Alias: alias,
		JID:   req.JID,
	}
	if syncSetVal != nil {
		val := syncSetVal.(string)
		dto.SyncSetID = &val
	}

	_ = WriteJSON(w, http.StatusOK, dto)
}

func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	alias := strings.TrimSpace(r.PathValue("alias"))
	if alias == "" {
		WriteError(w, http.StatusBadRequest, "group alias is required")
		return
	}

	res, err := s.db.ExecContext(r.Context(), `DELETE FROM endpoints WHERE alias = ? AND transport = 'whatsapp'`, alias)
	if err != nil {
		safelog.Error(s.logger, "delete group failed", "legacy_group_api", err)
		WriteError(w, http.StatusInternalServerError, "failed to delete group")
		return
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		safelog.Error(s.logger, "get rows affected failed", "legacy_group_api", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}
	if rowsAffected == 0 {
		WriteError(w, http.StatusNotFound, "group not found")
		return
	}

	s.notifyConfigChange(r.Context())
	_ = WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
