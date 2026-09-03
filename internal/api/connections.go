package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/vm75/message-sync/internal/controlstore"
	"github.com/vm75/message-sync/internal/safelog"
	"github.com/vm75/message-sync/internal/transport/discord"
	"github.com/vm75/message-sync/internal/transport/telegram"
)

type ConnectionDTO struct {
	ID        string `json:"id"`
	Transport string `json:"transport"`
	Label     string `json:"label"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

type CreateConnectionRequest struct {
	ID        string `json:"id,omitempty"`
	Transport string `json:"transport"`
	Label     string `json:"label"`
	Enabled   *bool  `json:"enabled,omitempty"`
	Token     string `json:"token,omitempty"`
}

type UpdateConnectionRequest struct {
	Label   *string `json:"label,omitempty"`
	Enabled *bool   `json:"enabled,omitempty"`
	Token   *string `json:"token,omitempty"`
}

func (s *Server) handleListConnections(w http.ResponseWriter, r *http.Request) {
	if s.controlDB == nil {
		WriteError(w, http.StatusServiceUnavailable, "control database unavailable")
		return
	}
	rows, err := s.controlDB.QueryContext(r.Context(), `
		SELECT id, transport, label, enabled, created_at, updated_at
		FROM transport_connections
		ORDER BY id ASC
	`)
	if err != nil {
		safelog.Error(s.logger, "query connections failed", "connection_list", err)
		WriteError(w, http.StatusInternalServerError, "failed to query connections")
		return
	}
	defer rows.Close()

	conns := make([]ConnectionDTO, 0)
	for rows.Next() {
		var dto ConnectionDTO
		if err := rows.Scan(&dto.ID, &dto.Transport, &dto.Label, &dto.Enabled, &dto.CreatedAt, &dto.UpdatedAt); err != nil {
			safelog.Error(s.logger, "scan connection failed", "connection_list", err)
			WriteError(w, http.StatusInternalServerError, "failed to scan connection")
			return
		}
		conns = append(conns, dto)
	}
	if err := rows.Err(); err != nil {
		safelog.Error(s.logger, "iterate connections failed", "connection_list", err)
		WriteError(w, http.StatusInternalServerError, "failed to iterate connections")
		return
	}
	_ = WriteJSON(w, http.StatusOK, conns)
}

func (s *Server) handleGetConnection(w http.ResponseWriter, r *http.Request) {
	if s.controlDB == nil {
		WriteError(w, http.StatusServiceUnavailable, "control database unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		WriteError(w, http.StatusBadRequest, "connection id is required")
		return
	}

	var dto ConnectionDTO
	err := s.controlDB.QueryRowContext(r.Context(), `
		SELECT id, transport, label, enabled, created_at, updated_at
		FROM transport_connections
		WHERE id = ?
	`, id).Scan(&dto.ID, &dto.Transport, &dto.Label, &dto.Enabled, &dto.CreatedAt, &dto.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "connection not found")
		return
	}
	if err != nil {
		safelog.Error(s.logger, "query connection failed", "connection_get", err)
		WriteError(w, http.StatusInternalServerError, "failed to query connection")
		return
	}
	_ = WriteJSON(w, http.StatusOK, dto)
}

func (s *Server) handleCreateConnection(w http.ResponseWriter, r *http.Request) {
	if s.controlDB == nil {
		WriteError(w, http.StatusServiceUnavailable, "control database unavailable")
		return
	}
	var req CreateConnectionRequest
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Transport = strings.TrimSpace(req.Transport)
	req.Label = strings.TrimSpace(req.Label)
	req.ID = strings.TrimSpace(req.ID)
	req.Token = strings.TrimSpace(req.Token)

	if req.Transport != "whatsapp" && req.Transport != "discord" && req.Transport != "telegram" {
		WriteError(w, http.StatusBadRequest, "invalid transport: must be whatsapp, discord, or telegram")
		return
	}
	if req.Label == "" {
		WriteError(w, http.StatusBadRequest, "connection label is required")
		return
	}

	if req.ID == "" {
		var genErr error
		req.ID, genErr = controlstore.NewConnectionID()
		if genErr != nil {
			WriteError(w, http.StatusInternalServerError, "failed to generate connection id")
			return
		}
	} else if err := controlstore.ValidateConnectionID(req.ID); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	var existing string
	err := s.controlDB.QueryRowContext(r.Context(), `SELECT id FROM transport_connections WHERE id = ?`, req.ID).Scan(&existing)
	if err == nil {
		WriteError(w, http.StatusConflict, "connection already exists")
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		safelog.Error(s.logger, "check connection conflict failed", "connection_create", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	var encCred, nonce []byte
	if req.Transport == "whatsapp" {
		if req.Token != "" {
			WriteError(w, http.StatusBadRequest, "whatsapp connections must not store credentials")
			return
		}
	} else {
		if req.Token == "" {
			WriteError(w, http.StatusBadRequest, req.Transport+" connections require a bot token")
			return
		}
		if s.credentialCipher == nil {
			WriteError(w, http.StatusInternalServerError, "credential cipher unavailable")
			return
		}
		var encErr error
		encCred, nonce, encErr = s.credentialCipher.Encrypt([]byte(req.Token))
		if encErr != nil {
			safelog.Error(s.logger, "encrypt connection credential failed", "connection_create", encErr)
			WriteError(w, http.StatusInternalServerError, "failed to secure credentials")
			return
		}
	}

	now := time.Now().UnixMilli()
	p, _ := principalFromContext(r.Context())
	var creator *string
	if p.ID != "" {
		creator = &p.ID
	}

	conn := controlstore.Connection{
		ID:                   req.ID,
		Transport:            req.Transport,
		Label:                req.Label,
		Enabled:              enabled,
		EncryptedCredential:  encCred,
		CredentialNonce:      nonce,
		CredentialKeyVersion: controlstore.CurrentKeyVersion,
		CreatedBy:            creator,
		CreatedAt:            now,
		UpdatedAt:            now,
	}

	if s.ownedControlStore != nil {
		if err := s.ownedControlStore.CreateConnection(r.Context(), conn); err != nil {
			safelog.Error(s.logger, "create connection failed", "connection_create", err)
			WriteError(w, http.StatusInternalServerError, "failed to create connection")
			return
		}
	} else {
		var encVal, nonceVal any
		if len(encCred) > 0 {
			encVal = encCred
		}
		if len(nonce) > 0 {
			nonceVal = nonce
		}
		_, err := s.controlDB.ExecContext(r.Context(), `
			INSERT INTO transport_connections (id, transport, label, enabled, encrypted_credential, credential_nonce, credential_key_version, created_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, conn.ID, conn.Transport, conn.Label, conn.Enabled, encVal, nonceVal, conn.CredentialKeyVersion, conn.CreatedBy, conn.CreatedAt, conn.UpdatedAt)
		if err != nil {
			safelog.Error(s.logger, "insert connection failed", "connection_create", err)
			WriteError(w, http.StatusInternalServerError, "failed to create connection")
			return
		}
	}

	s.audit(r, "connection_created", conn.ID)
	s.notifyConfigChange(r.Context())

	_ = WriteJSON(w, http.StatusCreated, ConnectionDTO{
		ID:        conn.ID,
		Transport: conn.Transport,
		Label:     conn.Label,
		Enabled:   conn.Enabled,
		CreatedAt: conn.CreatedAt,
		UpdatedAt: conn.UpdatedAt,
	})
}

func (s *Server) handleUpdateConnection(w http.ResponseWriter, r *http.Request) {
	if s.controlDB == nil {
		WriteError(w, http.StatusServiceUnavailable, "control database unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		WriteError(w, http.StatusBadRequest, "connection id is required")
		return
	}

	var req UpdateConnectionRequest
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var conn controlstore.Connection
	var encCred, nonce []byte
	var createdBy sql.NullString
	err := s.controlDB.QueryRowContext(r.Context(), `
		SELECT id, transport, label, enabled, encrypted_credential, credential_nonce, credential_key_version, created_by, created_at, updated_at
		FROM transport_connections
		WHERE id = ?
	`, id).Scan(&conn.ID, &conn.Transport, &conn.Label, &conn.Enabled, &encCred, &nonce, &conn.CredentialKeyVersion, &createdBy, &conn.CreatedAt, &conn.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "connection not found")
		return
	}
	if err != nil {
		safelog.Error(s.logger, "query connection failed", "connection_update", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}
	conn.EncryptedCredential = encCred
	conn.CredentialNonce = nonce
	if createdBy.Valid {
		conn.CreatedBy = &createdBy.String
	}

	if req.Label != nil {
		l := strings.TrimSpace(*req.Label)
		if l == "" {
			WriteError(w, http.StatusBadRequest, "connection label cannot be empty")
			return
		}
		conn.Label = l
	}

	if req.Enabled != nil && *req.Enabled != conn.Enabled {
		conn.Enabled = *req.Enabled
		if conn.Enabled {
			s.audit(r, "connection_enabled", id)
		} else {
			s.audit(r, "connection_disabled", id)
		}
	}

	if req.Token != nil {
		if conn.Transport == "whatsapp" {
			WriteError(w, http.StatusBadRequest, "whatsapp connections must not store credentials")
			return
		}
		tok := strings.TrimSpace(*req.Token)
		if tok == "" {
			WriteError(w, http.StatusBadRequest, "token cannot be empty")
			return
		}
		if s.credentialCipher == nil {
			WriteError(w, http.StatusInternalServerError, "credential cipher unavailable")
			return
		}
		newEnc, newNonce, encErr := s.credentialCipher.Encrypt([]byte(tok))
		if encErr != nil {
			safelog.Error(s.logger, "encrypt replacement credential failed", "connection_update", encErr)
			WriteError(w, http.StatusInternalServerError, "failed to secure credentials")
			return
		}
		conn.EncryptedCredential = newEnc
		conn.CredentialNonce = newNonce
		conn.CredentialKeyVersion = controlstore.CurrentKeyVersion
		s.audit(r, "connection_credential_replaced", id)
	}

	conn.UpdatedAt = time.Now().UnixMilli()

	var encVal, nonceVal any
	if len(conn.EncryptedCredential) > 0 {
		encVal = conn.EncryptedCredential
	}
	if len(conn.CredentialNonce) > 0 {
		nonceVal = conn.CredentialNonce
	}

	_, err = s.controlDB.ExecContext(r.Context(), `
		UPDATE transport_connections
		SET label = ?, enabled = ?, encrypted_credential = ?, credential_nonce = ?, credential_key_version = ?, updated_at = ?
		WHERE id = ?
	`, conn.Label, conn.Enabled, encVal, nonceVal, conn.CredentialKeyVersion, conn.UpdatedAt, conn.ID)
	if err != nil {
		safelog.Error(s.logger, "update connection failed", "connection_update", err)
		WriteError(w, http.StatusInternalServerError, "failed to update connection")
		return
	}

	s.notifyConfigChange(r.Context())

	_ = WriteJSON(w, http.StatusOK, ConnectionDTO{
		ID:        conn.ID,
		Transport: conn.Transport,
		Label:     conn.Label,
		Enabled:   conn.Enabled,
		CreatedAt: conn.CreatedAt,
		UpdatedAt: conn.UpdatedAt,
	})
}

func (s *Server) handleDeleteConnection(w http.ResponseWriter, r *http.Request) {
	if s.controlDB == nil {
		WriteError(w, http.StatusServiceUnavailable, "control database unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		WriteError(w, http.StatusBadRequest, "connection id is required")
		return
	}

	var transport string
	err := s.controlDB.QueryRowContext(r.Context(), `SELECT transport FROM transport_connections WHERE id = ?`, id).Scan(&transport)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "connection not found")
		return
	}
	if err != nil {
		safelog.Error(s.logger, "check connection existence failed", "connection_delete", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}

	// Deletion must fail with conflict if referenced by any endpoint in sync.db
	if s.db != nil {
		var refCount int
		err = s.db.QueryRowContext(r.Context(), `SELECT count(*) FROM endpoints WHERE connection_id = ?`, id).Scan(&refCount)
		if err == nil && refCount > 0 {
			WriteError(w, http.StatusConflict, "cannot delete connection referenced by endpoints")
			return
		}
	}

	// Stop runtime adapter
	if s.connections != nil {
		_ = s.connections.StopConnection(id)
	}

	_, err = s.controlDB.ExecContext(r.Context(), `DELETE FROM transport_connections WHERE id = ?`, id)
	if err != nil {
		safelog.Error(s.logger, "delete connection failed", "connection_delete", err)
		WriteError(w, http.StatusInternalServerError, "failed to delete connection")
		return
	}

	s.audit(r, "connection_deleted", id)
	s.notifyConfigChange(r.Context())

	_ = WriteJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleGetConnectionStatus(w http.ResponseWriter, r *http.Request) {
	if s.controlDB == nil {
		WriteError(w, http.StatusServiceUnavailable, "control database unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		WriteError(w, http.StatusBadRequest, "connection id is required")
		return
	}

	var conn ConnectionDTO
	err := s.controlDB.QueryRowContext(r.Context(), `
		SELECT id, transport, label, enabled, created_at, updated_at
		FROM transport_connections
		WHERE id = ?
	`, id).Scan(&conn.ID, &conn.Transport, &conn.Label, &conn.Enabled, &conn.CreatedAt, &conn.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "connection not found")
		return
	}
	if err != nil {
		safelog.Error(s.logger, "query connection failed", "connection_status", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}

	if !conn.Enabled {
		_ = WriteJSON(w, http.StatusOK, map[string]any{
			"id":        conn.ID,
			"transport": conn.Transport,
			"status":    "disabled",
			"enabled":   false,
		})
		return
	}

	if s.connections != nil {
		status, err := s.connections.ConnectionStatus(r.Context(), id)
		if err != nil {
			_ = WriteJSON(w, http.StatusOK, map[string]any{
				"id":        conn.ID,
				"transport": conn.Transport,
				"status":    "stopped",
				"enabled":   conn.Enabled,
			})
			return
		}
		_ = WriteJSON(w, http.StatusOK, status)
		return
	}

	_ = WriteJSON(w, http.StatusOK, map[string]any{
		"id":        conn.ID,
		"transport": conn.Transport,
		"status":    "stopped",
		"enabled":   conn.Enabled,
	})
}

func (s *Server) handleGetConnectionDiscovery(w http.ResponseWriter, r *http.Request) {
	if s.controlDB == nil {
		WriteError(w, http.StatusServiceUnavailable, "control database unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		WriteError(w, http.StatusBadRequest, "connection id is required")
		return
	}

	var conn ConnectionDTO
	err := s.controlDB.QueryRowContext(r.Context(), `
		SELECT id, transport, label, enabled, created_at, updated_at
		FROM transport_connections
		WHERE id = ?
	`, id).Scan(&conn.ID, &conn.Transport, &conn.Label, &conn.Enabled, &conn.CreatedAt, &conn.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "connection not found")
		return
	}
	if err != nil {
		safelog.Error(s.logger, "query connection failed", "connection_discovery", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}

	if !conn.Enabled {
		WriteError(w, http.StatusBadRequest, "connection is disabled")
		return
	}

	if s.connections != nil {
		targets, err := s.connections.ConnectionDiscovery(r.Context(), id)
		if err != nil {
			safelog.Error(s.logger, "connection discovery failed", "connection_discovery", err)
			WriteError(w, http.StatusInternalServerError, "failed to discover connection targets")
			return
		}
		if targets == nil {
			targets = []any{}
		}
		_ = WriteJSON(w, http.StatusOK, targets)
		return
	}

	_ = WriteJSON(w, http.StatusOK, []any{})
}

func (s *Server) handleWhatsAppConnectionPair(w http.ResponseWriter, r *http.Request) {
	if s.controlDB == nil {
		WriteError(w, http.StatusServiceUnavailable, "control database unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		WriteError(w, http.StatusBadRequest, "connection id is required")
		return
	}

	var transport string
	var enabled bool
	err := s.controlDB.QueryRowContext(r.Context(), `SELECT transport, enabled FROM transport_connections WHERE id = ?`, id).Scan(&transport, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "connection not found")
		return
	}
	if err != nil {
		safelog.Error(s.logger, "query connection failed", "whatsapp_pair", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}
	if transport != "whatsapp" {
		WriteError(w, http.StatusBadRequest, "not a WhatsApp connection")
		return
	}
	if !enabled {
		WriteError(w, http.StatusBadRequest, "connection is disabled")
		return
	}

	if s.connections == nil {
		WriteError(w, http.StatusServiceUnavailable, "connection runtime unavailable")
		return
	}

	resp, err := s.connections.WhatsAppPair(r.Context(), id)
	if err != nil {
		if strings.Contains(err.Error(), "already in progress") {
			WriteError(w, http.StatusConflict, "WhatsApp pairing already in progress on another connection")
			return
		}
		safelog.Error(s.logger, "whatsapp pair failed", "whatsapp_pair", err)
		WriteError(w, http.StatusInternalServerError, "failed to initiate pairing")
		return
	}

	s.audit(r, "whatsapp_pair_started", id)
	_ = WriteJSON(w, http.StatusOK, resp)
}

func (s *Server) handleWhatsAppConnectionCancelPair(w http.ResponseWriter, r *http.Request) {
	if s.controlDB == nil {
		WriteError(w, http.StatusServiceUnavailable, "control database unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		WriteError(w, http.StatusBadRequest, "connection id is required")
		return
	}

	var transport string
	err := s.controlDB.QueryRowContext(r.Context(), `SELECT transport FROM transport_connections WHERE id = ?`, id).Scan(&transport)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "connection not found")
		return
	}
	if err != nil {
		safelog.Error(s.logger, "query connection failed", "whatsapp_cancel_pair", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}
	if transport != "whatsapp" {
		WriteError(w, http.StatusBadRequest, "not a WhatsApp connection")
		return
	}

	if s.connections != nil {
		if err := s.connections.WhatsAppCancelPair(r.Context(), id); err != nil {
			safelog.Error(s.logger, "whatsapp cancel pair failed", "whatsapp_cancel_pair", err)
			WriteError(w, http.StatusInternalServerError, "failed to cancel pairing")
			return
		}
	}

	s.audit(r, "whatsapp_pair_cancelled", id)
	_ = WriteJSON(w, http.StatusOK, map[string]string{"status": "unpaired"})
}

func (s *Server) handleWhatsAppConnectionLogout(w http.ResponseWriter, r *http.Request) {
	if s.controlDB == nil {
		WriteError(w, http.StatusServiceUnavailable, "control database unavailable")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		WriteError(w, http.StatusBadRequest, "connection id is required")
		return
	}

	var transport string
	err := s.controlDB.QueryRowContext(r.Context(), `SELECT transport FROM transport_connections WHERE id = ?`, id).Scan(&transport)
	if errors.Is(err, sql.ErrNoRows) {
		WriteError(w, http.StatusNotFound, "connection not found")
		return
	}
	if err != nil {
		safelog.Error(s.logger, "query connection failed", "whatsapp_logout", err)
		WriteError(w, http.StatusInternalServerError, "database error")
		return
	}
	if transport != "whatsapp" {
		WriteError(w, http.StatusBadRequest, "not a WhatsApp connection")
		return
	}

	if s.connections != nil {
		if err := s.connections.WhatsAppLogout(r.Context(), id); err != nil {
			safelog.Error(s.logger, "whatsapp logout failed", "whatsapp_logout", err)
			WriteError(w, http.StatusInternalServerError, "failed to logout whatsapp connection")
			return
		}
	}

	s.audit(r, "whatsapp_logged_out", id)
	_ = WriteJSON(w, http.StatusOK, map[string]string{"status": "unpaired"})
}

type fallbackConnectionService struct {
	wa WhatsAppService
	dc discord.AdminService
	tg telegram.AdminService
}

func (s *fallbackConnectionService) ConnectionStatus(ctx context.Context, id string) (any, error) {
	if s.wa != nil && id == "conn-wa-1" {
		return s.wa.Status(ctx), nil
	}
	if s.dc != nil && id == "conn-dc-1" {
		return s.dc.AdminStatus(ctx), nil
	}
	if s.tg != nil && id == "conn-tg-1" {
		return s.tg.AdminStatus(ctx), nil
	}
	return map[string]any{"id": id, "status": "stopped"}, nil
}

func (s *fallbackConnectionService) ConnectionDiscovery(ctx context.Context, id string) (any, error) {
	if s.wa != nil && id == "conn-wa-1" {
		return s.wa.GetJoinedGroups(ctx)
	}
	if s.dc != nil && id == "conn-dc-1" {
		return s.dc.DiscoverChannels(ctx)
	}
	if s.tg != nil && id == "conn-tg-1" {
		return s.tg.DiscoverChats(ctx)
	}
	return nil, errors.New("discovery not supported for this connection")
}

func (s *fallbackConnectionService) WhatsAppPair(ctx context.Context, id string) (WhatsAppPairResponse, error) {
	if s.wa != nil && id == "conn-wa-1" {
		return s.wa.Pair(ctx)
	}
	return WhatsAppPairResponse{}, errors.New("pairing not supported for this connection")
}

func (s *fallbackConnectionService) WhatsAppCancelPair(ctx context.Context, id string) error {
	if s.wa != nil && id == "conn-wa-1" {
		return s.wa.CancelPair(ctx)
	}
	return nil
}

func (s *fallbackConnectionService) WhatsAppLogout(ctx context.Context, id string) error {
	if s.wa != nil && id == "conn-wa-1" {
		return s.wa.Logout(ctx)
	}
	return nil
}

func (s *fallbackConnectionService) StopConnection(id string) error {
	return nil
}

func (s *fallbackConnectionService) ConnectionAdapter(id string) (any, bool) {
	return nil, false
}
