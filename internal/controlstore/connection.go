package controlstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var connectionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

const (
	TelegramIntegrationModeBot     = "bot"
	TelegramIntegrationModeMTProto = "mtproto"
	DiscordIntegrationModeManaged  = "managed"
	DiscordIntegrationModeWebhook  = "webhook"
)

func NormalizeIntegrationMode(transportName, mode string) string {
	transportName = strings.ToLower(strings.TrimSpace(transportName))
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch transportName {
	case "telegram":
		if mode == "" {
			return TelegramIntegrationModeBot
		}
		return mode
	case "discord":
		if mode == "" {
			return DiscordIntegrationModeManaged
		}
		return mode
	default:
		return ""
	}
}

func ValidateIntegrationMode(transportName, mode string) error {
	transportName = strings.ToLower(strings.TrimSpace(transportName))
	switch transportName {
	case "telegram":
		switch NormalizeIntegrationMode(transportName, mode) {
		case TelegramIntegrationModeBot, TelegramIntegrationModeMTProto:
			return nil
		default:
			return fmt.Errorf("invalid telegram integration mode %q: must be bot or mtproto", strings.TrimSpace(mode))
		}
	case "discord":
		switch NormalizeIntegrationMode(transportName, mode) {
		case DiscordIntegrationModeManaged, DiscordIntegrationModeWebhook:
			return nil
		default:
			return fmt.Errorf("invalid discord integration mode %q: must be managed or webhook", strings.TrimSpace(mode))
		}
	default:
		if strings.TrimSpace(mode) != "" {
			return fmt.Errorf("integration mode is only supported for discord and telegram connections")
		}
		return nil
	}
}

func ValidateConnectionID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("connection id is required")
	}
	if !connectionIDPattern.MatchString(id) {
		return fmt.Errorf("connection id %q must match %s", id, connectionIDPattern.String())
	}
	return nil
}

func NewConnectionID() (string, error) {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", err
	}
	return "conn_" + hex.EncodeToString(randomBytes[:]), nil
}

type Connection struct {
	ID                   string  `json:"id"`
	Transport            string  `json:"transport"`
	IntegrationMode      string  `json:"integrationMode,omitempty"`
	Label                string  `json:"label"`
	Enabled              bool    `json:"enabled"`
	EncryptedCredential  []byte  `json:"-"`
	CredentialNonce      []byte  `json:"-"`
	CredentialKeyVersion int     `json:"credentialKeyVersion"`
	CreatedBy            *string `json:"createdBy,omitempty"`
	CreatedAt            int64   `json:"createdAt"`
	UpdatedAt            int64   `json:"updatedAt"`
}

func (c Connection) Validate() error {
	if err := ValidateConnectionID(c.ID); err != nil {
		return err
	}
	if err := ValidateIntegrationMode(c.Transport, c.IntegrationMode); err != nil {
		return err
	}
	switch c.Transport {
	case "whatsapp":
		if len(c.EncryptedCredential) > 0 || len(c.CredentialNonce) > 0 {
			return errors.New("whatsapp connections must not store credentials")
		}
	case "discord", "telegram":
		if len(c.EncryptedCredential) == 0 || len(c.CredentialNonce) == 0 {
			return fmt.Errorf("%s connections require encrypted credentials", c.Transport)
		}
	default:
		return fmt.Errorf("invalid transport %q: must be whatsapp, discord, or telegram", c.Transport)
	}
	if strings.TrimSpace(c.Label) == "" {
		return errors.New("connection label is required")
	}
	if c.CredentialKeyVersion < 1 {
		return errors.New("credential key version must be at least 1")
	}
	return nil
}

func (s *Store) CreateConnection(ctx context.Context, conn Connection) error {
	if s == nil || s.db == nil {
		return errors.New("control database is required")
	}
	if conn.CredentialKeyVersion == 0 {
		conn.CredentialKeyVersion = CurrentKeyVersion
	}
	if err := ValidateIntegrationMode(conn.Transport, conn.IntegrationMode); err != nil {
		return err
	}
	conn.IntegrationMode = NormalizeIntegrationMode(conn.Transport, conn.IntegrationMode)
	if err := conn.Validate(); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	if conn.CreatedAt == 0 {
		conn.CreatedAt = now
	}
	if conn.UpdatedAt == 0 {
		conn.UpdatedAt = now
	}

	var encCred, nonce any
	if len(conn.EncryptedCredential) > 0 {
		encCred = conn.EncryptedCredential
	}
	if len(conn.CredentialNonce) > 0 {
		nonce = conn.CredentialNonce
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO transport_connections (id, transport, integration_mode, label, enabled, encrypted_credential, credential_nonce, credential_key_version, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, conn.ID, conn.Transport, conn.IntegrationMode, conn.Label, conn.Enabled, encCred, nonce, conn.CredentialKeyVersion, conn.CreatedBy, conn.CreatedAt, conn.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert connection: %w", err)
	}
	return nil
}

func (s *Store) GetConnection(ctx context.Context, id string) (*Connection, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("control database is required")
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, transport, integration_mode, label, enabled, encrypted_credential, credential_nonce, credential_key_version, created_by, created_at, updated_at
		FROM transport_connections
		WHERE id = ?
	`, id)
	var conn Connection
	var encCred, nonce []byte
	var createdBy sql.NullString
	if err := row.Scan(&conn.ID, &conn.Transport, &conn.IntegrationMode, &conn.Label, &conn.Enabled, &encCred, &nonce, &conn.CredentialKeyVersion, &createdBy, &conn.CreatedAt, &conn.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("connection not found")
		}
		return nil, fmt.Errorf("query connection: %w", err)
	}
	conn.EncryptedCredential = encCred
	conn.CredentialNonce = nonce
	conn.IntegrationMode = NormalizeIntegrationMode(conn.Transport, conn.IntegrationMode)
	if createdBy.Valid {
		conn.CreatedBy = &createdBy.String
	}
	return &conn, nil
}

func (s *Store) ListConnections(ctx context.Context) ([]Connection, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("control database is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, transport, integration_mode, label, enabled, encrypted_credential, credential_nonce, credential_key_version, created_by, created_at, updated_at
		FROM transport_connections
		ORDER BY id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query connections: %w", err)
	}
	defer rows.Close()

	var conns []Connection
	for rows.Next() {
		var conn Connection
		var encCred, nonce []byte
		var createdBy sql.NullString
		if err := rows.Scan(&conn.ID, &conn.Transport, &conn.IntegrationMode, &conn.Label, &conn.Enabled, &encCred, &nonce, &conn.CredentialKeyVersion, &createdBy, &conn.CreatedAt, &conn.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan connection: %w", err)
		}
		conn.EncryptedCredential = encCred
		conn.CredentialNonce = nonce
		conn.IntegrationMode = NormalizeIntegrationMode(conn.Transport, conn.IntegrationMode)
		if createdBy.Valid {
			conn.CreatedBy = &createdBy.String
		}
		conns = append(conns, conn)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate connections: %w", err)
	}
	return conns, nil
}

func (s *Store) DeleteConnection(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return errors.New("control database is required")
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM transport_connections WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete connection: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		return errors.New("connection not found")
	}
	return nil
}

func (s *Store) UpdateConnection(ctx context.Context, conn Connection) error {
	if s == nil || s.db == nil {
		return errors.New("control database is required")
	}
	if err := ValidateIntegrationMode(conn.Transport, conn.IntegrationMode); err != nil {
		return err
	}
	conn.IntegrationMode = NormalizeIntegrationMode(conn.Transport, conn.IntegrationMode)
	if err := conn.Validate(); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	conn.UpdatedAt = now

	var encCred, nonce any
	if len(conn.EncryptedCredential) > 0 {
		encCred = conn.EncryptedCredential
	}
	if len(conn.CredentialNonce) > 0 {
		nonce = conn.CredentialNonce
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE transport_connections
		SET integration_mode = ?, label = ?, enabled = ?, encrypted_credential = ?, credential_nonce = ?, credential_key_version = ?, updated_at = ?
		WHERE id = ?
	`, conn.IntegrationMode, conn.Label, conn.Enabled, encCred, nonce, conn.CredentialKeyVersion, conn.UpdatedAt, conn.ID)
	if err != nil {
		return fmt.Errorf("update connection: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		return errors.New("connection not found")
	}
	return nil
}
