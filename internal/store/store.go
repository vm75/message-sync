package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const SchemaVersion = 1

var (
	//go:embed schema.sql
	schemaSQL string

	endpointPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	actorPattern    = regexp.MustCompile(`^u_[a-z2-7]{10}$`)
)

type Store struct {
	db *sql.DB
}

type MessageCopy struct {
	CanonicalID     string
	EndpointID      string
	RemoteMessageID string
	CreatedAt       time.Time
}

type Reaction struct {
	CanonicalID      string
	SourceEndpointID string
	ActorHash        string
	Emoji            string
	UpdatedAt        time.Time
}

type RecoveryCursor struct {
	EndpointID       string
	RemoteMessageID  string
	MessageTimestamp time.Time
	UpdatedAt        time.Time
}

func Open(ctx context.Context, path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("sync database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create sync database directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sync database: %w", err)
	}
	db.SetMaxOpenConns(1)

	for _, pragma := range []string{"PRAGMA foreign_keys = ON", "PRAGMA busy_timeout = 5000"} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("configure sync database: %w", err)
		}
	}
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("secure sync database permissions: %w", err)
	}
	return &Store{db: db}, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin sync migration: %w", err)
	}
	defer tx.Rollback()

	for _, statement := range strings.Split(schemaSQL, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate sync database: %w", err)
		}
	}

	var version int
	if err := tx.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM schema_meta WHERE key = 'schema_version'`).Scan(&version); err != nil {
		return fmt.Errorf("read sync schema version: %w", err)
	}
	if version != SchemaVersion {
		return fmt.Errorf("unsupported sync schema version %d", version)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sync migration: %w", err)
	}
	return nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) CreateCanonical(ctx context.Context, canonicalID string, createdAt time.Time) error {
	if err := requireOpaque("canonical id", canonicalID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO canonical_messages(canonical_id, created_at) VALUES (?, ?)`,
		canonicalID, unixMillis(createdAt),
	)
	return wrapDB("create canonical message", err)
}

func (s *Store) TombstoneCanonical(ctx context.Context, canonicalID string, at time.Time) error {
	if err := requireOpaque("canonical id", canonicalID); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE canonical_messages SET tombstoned_at = ? WHERE canonical_id = ?`,
		unixMillis(at), canonicalID,
	)
	if err != nil {
		return fmt.Errorf("tombstone canonical message: %w", err)
	}
	return requireRow("tombstone canonical message", res)
}

func (s *Store) AddMessageCopy(ctx context.Context, copy MessageCopy) error {
	if err := validateCopy(copy); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO message_copies(canonical_id, endpoint_id, remote_message_id, created_at) VALUES (?, ?, ?, ?)`,
		copy.CanonicalID, copy.EndpointID, copy.RemoteMessageID, unixMillis(copy.CreatedAt),
	)
	return wrapDB("add message copy", err)
}

func (s *Store) CanonicalForRemote(ctx context.Context, endpointID, remoteMessageID string) (string, error) {
	if err := validateEndpoint(endpointID); err != nil {
		return "", err
	}
	if err := requireOpaque("remote message id", remoteMessageID); err != nil {
		return "", err
	}
	var canonicalID string
	err := s.db.QueryRowContext(ctx,
		`SELECT canonical_id FROM message_copies WHERE endpoint_id = ? AND remote_message_id = ?`,
		endpointID, remoteMessageID,
	).Scan(&canonicalID)
	if err != nil {
		return "", wrapDB("find canonical message", err)
	}
	return canonicalID, nil
}

func (s *Store) MessageCopyForEndpoint(ctx context.Context, canonicalID, endpointID string) (MessageCopy, error) {
	if err := requireOpaque("canonical id", canonicalID); err != nil {
		return MessageCopy{}, err
	}
	if err := validateEndpoint(endpointID); err != nil {
		return MessageCopy{}, err
	}
	var copy MessageCopy
	var createdAt int64
	err := s.db.QueryRowContext(ctx,
		`SELECT canonical_id, endpoint_id, remote_message_id, created_at FROM message_copies WHERE canonical_id = ? AND endpoint_id = ?`,
		canonicalID, endpointID,
	).Scan(&copy.CanonicalID, &copy.EndpointID, &copy.RemoteMessageID, &createdAt)
	if err != nil {
		return MessageCopy{}, wrapDB("find message copy", err)
	}
	copy.CreatedAt = fromUnixMillis(createdAt)
	return copy, nil
}

func (s *Store) UpsertReaction(ctx context.Context, reaction Reaction) error {
	if err := requireOpaque("canonical id", reaction.CanonicalID); err != nil {
		return err
	}
	if err := validateEndpoint(reaction.SourceEndpointID); err != nil {
		return err
	}
	if !actorPattern.MatchString(reaction.ActorHash) {
		return errors.New("actor hash must be an HMAC-derived user id")
	}
	if strings.TrimSpace(reaction.Emoji) == "" {
		return errors.New("reaction emoji is required")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO reactions(canonical_id, source_endpoint_id, actor_hash, emoji, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(canonical_id, source_endpoint_id, actor_hash)
		DO UPDATE SET emoji = excluded.emoji, updated_at = excluded.updated_at`,
		reaction.CanonicalID, reaction.SourceEndpointID, reaction.ActorHash, reaction.Emoji, unixMillis(reaction.UpdatedAt),
	)
	return wrapDB("upsert reaction", err)
}

func (s *Store) DeleteReaction(ctx context.Context, canonicalID, sourceEndpointID, actorHash string) error {
	if err := requireOpaque("canonical id", canonicalID); err != nil {
		return err
	}
	if err := validateEndpoint(sourceEndpointID); err != nil {
		return err
	}
	if !actorPattern.MatchString(actorHash) {
		return errors.New("actor hash must be an HMAC-derived user id")
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM reactions WHERE canonical_id = ? AND source_endpoint_id = ? AND actor_hash = ?`,
		canonicalID, sourceEndpointID, actorHash,
	)
	return wrapDB("delete reaction", err)
}

func (s *Store) PutRecoveryCursor(ctx context.Context, cursor RecoveryCursor) error {
	if err := validateEndpoint(cursor.EndpointID); err != nil {
		return err
	}
	if cursor.RemoteMessageID != "" {
		if err := requireOpaque("remote message id", cursor.RemoteMessageID); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO recovery_cursors(endpoint_id, remote_message_id, message_timestamp, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(endpoint_id) DO UPDATE SET
			remote_message_id = excluded.remote_message_id,
			message_timestamp = excluded.message_timestamp,
			updated_at = excluded.updated_at`,
		cursor.EndpointID, nullableString(cursor.RemoteMessageID), nullableMillis(cursor.MessageTimestamp), unixMillis(cursor.UpdatedAt),
	)
	return wrapDB("put recovery cursor", err)
}

func (s *Store) RecoveryCursor(ctx context.Context, endpointID string) (RecoveryCursor, error) {
	if err := validateEndpoint(endpointID); err != nil {
		return RecoveryCursor{}, err
	}
	var cursor RecoveryCursor
	var remote sql.NullString
	var messageTS sql.NullInt64
	var updated int64
	err := s.db.QueryRowContext(ctx,
		`SELECT endpoint_id, remote_message_id, message_timestamp, updated_at FROM recovery_cursors WHERE endpoint_id = ?`, endpointID,
	).Scan(&cursor.EndpointID, &remote, &messageTS, &updated)
	if err != nil {
		return RecoveryCursor{}, wrapDB("get recovery cursor", err)
	}
	if remote.Valid {
		cursor.RemoteMessageID = remote.String
	}
	if messageTS.Valid {
		cursor.MessageTimestamp = fromUnixMillis(messageTS.Int64)
	}
	cursor.UpdatedAt = fromUnixMillis(updated)
	return cursor, nil
}

func validateCopy(copy MessageCopy) error {
	if err := requireOpaque("canonical id", copy.CanonicalID); err != nil {
		return err
	}
	if err := validateEndpoint(copy.EndpointID); err != nil {
		return err
	}
	return requireOpaque("remote message id", copy.RemoteMessageID)
}

func validateEndpoint(endpointID string) error {
	if !endpointPattern.MatchString(endpointID) {
		return errors.New("endpoint id must be a configured alias")
	}
	return nil
}

func requireOpaque(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%s contains invalid control characters", name)
	}
	return nil
}

func wrapDB(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func requireRow(operation string, result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: rows affected: %w", operation, err)
	}
	if rows == 0 {
		return fmt.Errorf("%s: %w", operation, sql.ErrNoRows)
	}
	return nil
}

func unixMillis(value time.Time) int64 {
	if value.IsZero() {
		return time.Now().UTC().UnixMilli()
	}
	return value.UTC().UnixMilli()
}

func fromUnixMillis(value int64) time.Time {
	return time.UnixMilli(value).UTC()
}

func nullableMillis(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return unixMillis(value)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
