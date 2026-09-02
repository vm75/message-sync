package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/safelog"
)

type EndpointDTO struct {
	Alias     string           `json:"alias"`
	Transport config.Transport `json:"transport"`
	RemoteID  string           `json:"remoteId"`
	SyncSetID *string          `json:"syncSetId,omitempty"`
}

type CreateEndpointRequest struct {
	Alias     string           `json:"alias"`
	Transport config.Transport `json:"transport"`
	RemoteID  string           `json:"remoteId"`
	SyncSetID *string          `json:"syncSetId,omitempty"`
}

type UpdateEndpointRequest struct {
	Alias     string           `json:"alias,omitempty"`
	Transport config.Transport `json:"transport"`
	RemoteID  string           `json:"remoteId"`
	SyncSetID *string          `json:"syncSetId,omitempty"`
}

func (s *Server) handleListEndpoints(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	rows, err := s.db.QueryContext(r.Context(), `SELECT alias, transport, remote_id, sync_set_id FROM endpoints ORDER BY alias ASC`)
	if err != nil {
		safelog.Error(s.logger, "query endpoints failed", "endpoint_list", err)
		WriteError(w, http.StatusInternalServerError, "failed to query endpoints")
		return
	}
	defer rows.Close()

	endpoints := make([]EndpointDTO, 0)
	for rows.Next() {
		var dto EndpointDTO
		var transportName string
		var syncSetID sql.NullString
		if err := rows.Scan(&dto.Alias, &transportName, &dto.RemoteID, &syncSetID); err != nil {
			safelog.Error(s.logger, "scan endpoint failed", "endpoint_list", err)
			WriteError(w, http.StatusInternalServerError, "failed to read endpoints")
			return
		}
		dto.Transport = config.Transport(transportName)
		if syncSetID.Valid && strings.TrimSpace(syncSetID.String) != "" {
			value := syncSetID.String
			dto.SyncSetID = &value
		}
		endpoints = append(endpoints, dto)
	}
	if err := rows.Err(); err != nil {
		safelog.Error(s.logger, "iterate endpoints failed", "endpoint_list", err)
		WriteError(w, http.StatusInternalServerError, "failed to iterate endpoints")
		return
	}

	_ = WriteJSON(w, http.StatusOK, endpoints)
}

func (s *Server) handleGetEndpoint(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	alias := strings.TrimSpace(r.PathValue("alias"))
	if alias == "" {
		WriteError(w, http.StatusBadRequest, "endpoint alias is required")
		return
	}

	var dto EndpointDTO
	var transportName string
	var syncSetID sql.NullString
	err := s.db.QueryRowContext(r.Context(),
		`SELECT alias, transport, remote_id, sync_set_id FROM endpoints WHERE alias = ?`,
		alias,
	).Scan(&dto.Alias, &transportName, &dto.RemoteID, &syncSetID)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "endpoint not found")
		return
	}
	if err != nil {
		safelog.Error(s.logger, "query endpoint failed", "endpoint_get", err)
		WriteError(w, http.StatusInternalServerError, "failed to query endpoint")
		return
	}

	dto.Transport = config.Transport(transportName)
	if syncSetID.Valid && strings.TrimSpace(syncSetID.String) != "" {
		value := syncSetID.String
		dto.SyncSetID = &value
	}
	_ = WriteJSON(w, http.StatusOK, dto)
}

func (s *Server) handleCreateEndpoint(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	var req CreateEndpointRequest
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Alias = strings.TrimSpace(req.Alias)
	req.Transport = config.Transport(strings.TrimSpace(string(req.Transport)))
	req.RemoteID = strings.TrimSpace(req.RemoteID)

	if err := config.ValidateAlias(req.Alias); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := config.ValidateEndpointRemoteID(req.Transport, req.RemoteID); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	var existing string
	err := s.db.QueryRowContext(r.Context(), `SELECT alias FROM endpoints WHERE alias = ?`, req.Alias).Scan(&existing)
	if err == nil {
		WriteError(w, http.StatusBadRequest, "endpoint alias already exists")
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		safelog.Error(s.logger, "check endpoint alias failed", "endpoint_create", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}

	err = s.db.QueryRowContext(r.Context(),
		`SELECT alias FROM endpoints WHERE transport = ? AND remote_id = ?`,
		string(req.Transport), req.RemoteID,
	).Scan(&existing)
	if err == nil {
		WriteError(w, http.StatusBadRequest, "an endpoint is already configured for this transport target")
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		safelog.Error(s.logger, "check endpoint target failed", "endpoint_create", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}

	syncSetValue, responseSyncSet, ok := s.endpointSyncSetValue(w, r, req.SyncSetID, "endpoint_create")
	if !ok {
		return
	}

	_, err = s.db.ExecContext(r.Context(),
		`INSERT INTO endpoints (alias, transport, remote_id, sync_set_id) VALUES (?, ?, ?, ?)`,
		req.Alias, string(req.Transport), req.RemoteID, syncSetValue,
	)
	if err != nil {
		safelog.Error(s.logger, "insert endpoint failed", "endpoint_create", err)
		WriteError(w, http.StatusInternalServerError, "failed to create endpoint")
		return
	}

	s.notifyConfigChange(r.Context())
	_ = WriteJSON(w, http.StatusCreated, EndpointDTO{
		Alias:     req.Alias,
		Transport: req.Transport,
		RemoteID:  req.RemoteID,
		SyncSetID: responseSyncSet,
	})
}

func (s *Server) handleUpdateEndpoint(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	alias := strings.TrimSpace(r.PathValue("alias"))
	if alias == "" {
		WriteError(w, http.StatusBadRequest, "endpoint alias is required")
		return
	}

	var req UpdateEndpointRequest
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	newAlias := alias
	if req.Alias != "" && strings.TrimSpace(req.Alias) != alias {
		newAlias = strings.TrimSpace(req.Alias)
		if err := config.ValidateAlias(newAlias); err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	req.Transport = config.Transport(strings.TrimSpace(string(req.Transport)))
	req.RemoteID = strings.TrimSpace(req.RemoteID)
	if err := config.ValidateEndpointRemoteID(req.Transport, req.RemoteID); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	var existing string
	err := s.db.QueryRowContext(r.Context(), `SELECT alias FROM endpoints WHERE alias = ?`, alias).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "endpoint not found")
		return
	}
	if err != nil {
		safelog.Error(s.logger, "check endpoint existence failed", "endpoint_update", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}

	if newAlias != alias {
		err = s.db.QueryRowContext(r.Context(), `SELECT alias FROM endpoints WHERE alias = ?`, newAlias).Scan(&existing)
		if err == nil {
			WriteError(w, http.StatusBadRequest, "endpoint alias already exists")
			return
		}
		if !errors.Is(err, sql.ErrNoRows) {
			safelog.Error(s.logger, "check endpoint alias uniqueness failed", "endpoint_update", err)
			WriteError(w, http.StatusInternalServerError, "database error")
			return
		}
	}

	err = s.db.QueryRowContext(r.Context(),
		`SELECT alias FROM endpoints WHERE transport = ? AND remote_id = ? AND alias != ?`,
		string(req.Transport), req.RemoteID, alias,
	).Scan(&existing)
	if err == nil {
		WriteError(w, http.StatusBadRequest, "an endpoint is already configured for this transport target")
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		safelog.Error(s.logger, "check endpoint target failed", "endpoint_update", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}

	syncSetValue, responseSyncSet, ok := s.endpointSyncSetValue(w, r, req.SyncSetID, "endpoint_update")
	if !ok {
		return
	}

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		safelog.Error(s.logger, "begin tx failed", "endpoint_update", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(r.Context(),
		`UPDATE endpoints SET alias = ?, transport = ?, remote_id = ?, sync_set_id = ? WHERE alias = ?`,
		newAlias, string(req.Transport), req.RemoteID, syncSetValue, alias,
	)
	if err != nil {
		safelog.Error(s.logger, "update endpoint failed", "endpoint_update", err)
		WriteError(w, http.StatusInternalServerError, "failed to update endpoint")
		return
	}

	if newAlias != alias {
		if _, err := tx.ExecContext(r.Context(), `UPDATE message_copies SET endpoint_id = ? WHERE endpoint_id = ?`, newAlias, alias); err != nil {
			safelog.Error(s.logger, "update message copies endpoint failed", "endpoint_update", err)
			WriteError(w, http.StatusInternalServerError, "failed to update endpoint")
			return
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE reactions SET source_endpoint_id = ? WHERE source_endpoint_id = ?`, newAlias, alias); err != nil {
			safelog.Error(s.logger, "update reactions endpoint failed", "endpoint_update", err)
			WriteError(w, http.StatusInternalServerError, "failed to update endpoint")
			return
		}
	}

	if err := tx.Commit(); err != nil {
		safelog.Error(s.logger, "commit tx failed", "endpoint_update", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}

	if newAlias != alias && s.controlDB != nil {
		_, _ = s.controlDB.ExecContext(r.Context(), `UPDATE verification_pipelines SET endpoint_alias = ? WHERE endpoint_alias = ?`, newAlias, alias)
	}

	s.notifyConfigChange(r.Context())
	_ = WriteJSON(w, http.StatusOK, EndpointDTO{
		Alias:     newAlias,
		Transport: req.Transport,
		RemoteID:  req.RemoteID,
		SyncSetID: responseSyncSet,
	})
}

func (s *Server) handleDeleteEndpoint(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	alias := strings.TrimSpace(r.PathValue("alias"))
	if alias == "" {
		WriteError(w, http.StatusBadRequest, "endpoint alias is required")
		return
	}

	res, err := s.db.ExecContext(r.Context(), `DELETE FROM endpoints WHERE alias = ?`, alias)
	if err != nil {
		safelog.Error(s.logger, "delete endpoint failed", "endpoint_delete", err)
		WriteError(w, http.StatusInternalServerError, "failed to delete endpoint")
		return
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		safelog.Error(s.logger, "read endpoint delete result failed", "endpoint_delete", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}
	if rowsAffected == 0 {
		WriteError(w, http.StatusNotFound, "endpoint not found")
		return
	}

	s.notifyConfigChange(r.Context())
	_ = WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) endpointSyncSetValue(w http.ResponseWriter, r *http.Request, requested *string, operation string) (any, *string, bool) {
	if requested == nil || strings.TrimSpace(*requested) == "" {
		return nil, nil, true
	}

	setID := strings.TrimSpace(*requested)
	var existing string
	err := s.db.QueryRowContext(r.Context(), `SELECT id FROM sync_sets WHERE id = ?`, setID).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusBadRequest, "sync set does not exist")
		return nil, nil, false
	}
	if err != nil {
		safelog.Error(s.logger, "check endpoint sync set failed", operation, err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return nil, nil, false
	}
	return setID, &setID, true
}
