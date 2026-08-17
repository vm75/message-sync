package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ResolveOrCreateCanonical atomically resolves an existing source copy or
// creates a new canonical message and its source copy in one transaction.
func (s *Store) ResolveOrCreateCanonical(ctx context.Context, candidateCanonicalID string, source MessageCopy) (string, bool, error) {
	if s == nil || s.db == nil {
		return "", false, errors.New("sync store is required")
	}
	if err := requireOpaque("canonical id", candidateCanonicalID); err != nil {
		return "", false, err
	}
	if err := validateEndpoint(source.EndpointID); err != nil {
		return "", false, err
	}
	if err := requireOpaque("remote message id", source.RemoteMessageID); err != nil {
		return "", false, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, fmt.Errorf("begin canonical resolution: %w", err)
	}
	defer tx.Rollback()

	var existing string
	err = tx.QueryRowContext(ctx,
		`SELECT canonical_id FROM message_copies WHERE endpoint_id = ? AND remote_message_id = ?`,
		source.EndpointID, source.RemoteMessageID,
	).Scan(&existing)
	if err == nil {
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("resolve source canonical: %w", err)
	}

	createdAt := source.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO canonical_messages(canonical_id, created_at) VALUES (?, ?)`,
		candidateCanonicalID, unixMillis(createdAt),
	); err != nil {
		return "", false, fmt.Errorf("create canonical message: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO message_copies(canonical_id, endpoint_id, remote_message_id, created_at) VALUES (?, ?, ?, ?)`,
		candidateCanonicalID, source.EndpointID, source.RemoteMessageID, unixMillis(createdAt),
	); err != nil {
		return "", false, fmt.Errorf("add source message copy: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", false, fmt.Errorf("commit canonical resolution: %w", err)
	}
	return candidateCanonicalID, true, nil
}
