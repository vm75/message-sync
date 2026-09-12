package controlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ConnectionSecretStore keeps opaque, integration-specific state behind the
// same encrypted control-store boundary used for transport credentials.
type ConnectionSecretStore struct {
	db           *sql.DB
	cipher       *CredentialCipher
	connectionID string
}

func NewConnectionSecretStore(db *sql.DB, cipher *CredentialCipher, connectionID string) (*ConnectionSecretStore, error) {
	if db == nil || cipher == nil {
		return nil, errors.New("control database and credential cipher are required")
	}
	if err := ValidateConnectionID(connectionID); err != nil {
		return nil, err
	}
	return &ConnectionSecretStore{db: db, cipher: cipher, connectionID: connectionID}, nil
}

func (s *ConnectionSecretStore) Load(ctx context.Context) ([]byte, error) {
	if s == nil || s.db == nil || s.cipher == nil {
		return nil, errors.New("connection secret store is unavailable")
	}
	var transportName, mode string
	var encrypted, nonce []byte
	if err := s.db.QueryRowContext(ctx, `SELECT transport, integration_mode, encrypted_credential, credential_nonce FROM transport_connections WHERE id=?`, s.connectionID).Scan(&transportName, &mode, &encrypted, &nonce); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("connection not found")
		}
		return nil, fmt.Errorf("load encrypted connection state: %w", err)
	}
	if transportName != "telegram" || NormalizeIntegrationMode(transportName, mode) != TelegramIntegrationModeMTProto {
		return nil, errors.New("connection is not a Telegram MTProto connection")
	}
	if len(encrypted) == 0 || len(nonce) == 0 {
		return nil, nil
	}
	plaintext, err := s.cipher.Decrypt(encrypted, nonce)
	if err != nil {
		return nil, errors.New("decrypt connection state")
	}
	return plaintext, nil
}

func (s *ConnectionSecretStore) Store(ctx context.Context, plaintext []byte) error {
	if s == nil || s.db == nil || s.cipher == nil {
		return errors.New("connection secret store is unavailable")
	}
	var transportName, mode string
	if err := s.db.QueryRowContext(ctx, `SELECT transport, integration_mode FROM transport_connections WHERE id=?`, s.connectionID).Scan(&transportName, &mode); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("connection not found")
		}
		return fmt.Errorf("verify connection state owner: %w", err)
	}
	if transportName != "telegram" || NormalizeIntegrationMode(transportName, mode) != TelegramIntegrationModeMTProto {
		return errors.New("connection is not a Telegram MTProto connection")
	}
	encrypted, nonce, err := s.cipher.Encrypt(plaintext)
	if err != nil {
		return errors.New("encrypt connection state")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE transport_connections SET encrypted_credential=?, credential_nonce=?, credential_key_version=?, updated_at=? WHERE id=?`, encrypted, nonce, CurrentKeyVersion, time.Now().UnixMilli(), s.connectionID)
	if err != nil {
		return fmt.Errorf("persist encrypted connection state: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return errors.New("connection state was not persisted")
	}
	return nil
}
