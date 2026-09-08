package controlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// PollPresentationStore stores encrypted poll question and option labels.
// canonicalID is opaque routing state; the presentation payload is never
// written to the database in plaintext.
type PollPresentationStore struct {
	db     *sql.DB
	cipher *CredentialCipher
}

type pollPresentationPayload struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
}

func NewPollPresentationStore(db *sql.DB, cipher *CredentialCipher) (*PollPresentationStore, error) {
	if db == nil {
		return nil, errors.New("control database is required")
	}
	if cipher == nil {
		return nil, errors.New("credential cipher is required")
	}
	return &PollPresentationStore{db: db, cipher: cipher}, nil
}

func (s *PollPresentationStore) SavePollPresentation(ctx context.Context, canonicalID, question string, options []string) error {
	if s == nil || s.db == nil || s.cipher == nil {
		return errors.New("poll presentation store is not initialized")
	}
	payload, err := json.Marshal(pollPresentationPayload{Question: question, Options: options})
	if err != nil {
		return fmt.Errorf("encode poll presentation: %w", err)
	}
	ciphertext, nonce, err := s.cipher.Encrypt(payload)
	if err != nil {
		return fmt.Errorf("encrypt poll presentation: %w", err)
	}
	now := time.Now().UTC().UnixMilli()
	_, err = s.db.ExecContext(ctx, `INSERT INTO poll_presentations(canonical_id,ciphertext,nonce,created_at,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(canonical_id) DO UPDATE SET ciphertext=excluded.ciphertext,nonce=excluded.nonce,updated_at=excluded.updated_at`, canonicalID, ciphertext, nonce, now, now)
	if err != nil {
		return fmt.Errorf("save poll presentation: %w", err)
	}
	return nil
}

func (s *PollPresentationStore) LoadPollPresentation(ctx context.Context, canonicalID string) (string, []string, error) {
	if s == nil || s.db == nil || s.cipher == nil {
		return "", nil, errors.New("poll presentation store is not initialized")
	}
	var ciphertext, nonce []byte
	if err := s.db.QueryRowContext(ctx, `SELECT ciphertext,nonce FROM poll_presentations WHERE canonical_id=?`, canonicalID).Scan(&ciphertext, &nonce); err != nil {
		return "", nil, err
	}
	payload, err := s.cipher.Decrypt(ciphertext, nonce)
	if err != nil {
		return "", nil, fmt.Errorf("decrypt poll presentation: %w", err)
	}
	var presentation pollPresentationPayload
	if err := json.Unmarshal(payload, &presentation); err != nil {
		return "", nil, fmt.Errorf("decode poll presentation: %w", err)
	}
	return presentation.Question, append([]string(nil), presentation.Options...), nil
}
