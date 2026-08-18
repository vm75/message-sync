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

const SchemaVersion = 6

var (
	//go:embed schema.sql
	schemaSQL string

	endpointPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	actorPattern    = regexp.MustCompile(`^u_[a-z2-7]{10}$`)
)

type Store struct {
	db *sql.DB
}

func (s *Store) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

type MessageCopy struct {
	CanonicalID     string
	EndpointID      string
	RemoteMessageID string
	CreatedAt       time.Time
	FromSelf        bool
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

type StorageMetrics struct {
	CanonicalMessages int64
	MessageCopies     int64
	Reactions         int64
	DatabaseSizeBytes int64
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

	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
	} {
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
	if version == 1 {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE message_copies ADD COLUMN from_self BOOLEAN NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("migrate schema v1 to v2: add from_self: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schema_meta SET value = '2' WHERE key = 'schema_version'`); err != nil {
			return fmt.Errorf("migrate schema v1 to v2: update version: %w", err)
		}
		version = 2
	}
	if version == 2 {
		// Retroactively mark destination copies as from_self=1.
		// For each canonical, the earliest copy is the source; all others
		// were sent by the bridge account.
		if _, err := tx.ExecContext(ctx, `
			UPDATE message_copies SET from_self = 1
			WHERE rowid NOT IN (
				SELECT MIN(rowid) FROM message_copies GROUP BY canonical_id
			)`); err != nil {
			return fmt.Errorf("migrate schema v2 to v3: fix from_self: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schema_meta SET value = '3' WHERE key = 'schema_version'`); err != nil {
			return fmt.Errorf("migrate schema v2 to v3: update version: %w", err)
		}
		version = 3
	}
	if version == 3 {
		if _, err := tx.ExecContext(ctx, `UPDATE schema_meta SET value = '4' WHERE key = 'schema_version'`); err != nil {
			return fmt.Errorf("migrate schema v3 to v4: update version: %w", err)
		}
		version = 4
	}
	if version == 4 {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE global_config ADD COLUMN admin_password_hash TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("migrate schema v4 to v5: add admin_password_hash: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schema_meta SET value = '5' WHERE key = 'schema_version'`); err != nil {
			return fmt.Errorf("migrate schema v4 to v5: update version: %w", err)
		}
		version = 5
	}
	if version == 5 {
		if _, err := tx.ExecContext(ctx, `
			CREATE TABLE IF NOT EXISTS poll_options (
				canonical_id TEXT NOT NULL REFERENCES canonical_messages(canonical_id) ON DELETE CASCADE,
				option_index INTEGER NOT NULL,
				option_hash TEXT NOT NULL,
				PRIMARY KEY (canonical_id, option_index)
			);
			CREATE INDEX IF NOT EXISTS idx_poll_options_canonical ON poll_options(canonical_id);

			CREATE TABLE IF NOT EXISTS poll_votes (
				canonical_id TEXT NOT NULL REFERENCES canonical_messages(canonical_id) ON DELETE CASCADE,
				endpoint_id TEXT NOT NULL,
				actor_hash TEXT NOT NULL,
				option_hash TEXT NOT NULL,
				updated_at INTEGER NOT NULL,
				PRIMARY KEY (canonical_id, endpoint_id, actor_hash, option_hash)
			);
			CREATE INDEX IF NOT EXISTS idx_poll_votes_canonical ON poll_votes(canonical_id);
		`); err != nil {
			return fmt.Errorf("migrate schema v5 to v6: add poll tables: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schema_meta SET value = '6' WHERE key = 'schema_version'`); err != nil {
			return fmt.Errorf("migrate schema v5 to v6: update version: %w", err)
		}
		version = 6
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
	_, _ = s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	_, _ = s.db.Exec("PRAGMA optimize")
	return s.db.Close()
}

// PruneOlderThan deletes canonical messages created before the given cutoff timestamp in bounded batches.
// Cascades to associated message_copies and reactions.
func (s *Store) PruneOlderThan(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("sync store is required")
	}
	if batchSize <= 0 {
		batchSize = 1000
	}
	cutoffMillis := unixMillis(cutoff)
	var totalDeleted int64

	for {
		if err := ctx.Err(); err != nil {
			return totalDeleted, err
		}

		res, err := s.db.ExecContext(ctx, `
			DELETE FROM canonical_messages
			WHERE canonical_id IN (
				SELECT canonical_id FROM canonical_messages
				WHERE created_at < ?
				ORDER BY created_at ASC
				LIMIT ?
			)`, cutoffMillis, batchSize)
		if err != nil {
			return totalDeleted, wrapDB("prune retention", err)
		}

		affected, err := res.RowsAffected()
		if err != nil {
			return totalDeleted, fmt.Errorf("prune retention rows affected: %w", err)
		}
		totalDeleted += affected
		if affected < int64(batchSize) {
			break
		}
	}
	return totalDeleted, nil
}

// PruneRetention deletes canonical messages older than retentionDays.
func (s *Store) PruneRetention(ctx context.Context, retentionDays int, batchSize int) (int64, error) {
	if retentionDays <= 0 {
		return 0, errors.New("retention days must be positive")
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
	return s.PruneOlderThan(ctx, cutoff, batchSize)
}

// Metrics computes non-sensitive storage size metrics.
func (s *Store) Metrics(ctx context.Context, dbPath string) (StorageMetrics, error) {
	if s == nil || s.db == nil {
		return StorageMetrics{}, errors.New("sync store is required")
	}
	var metrics StorageMetrics
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM canonical_messages`).Scan(&metrics.CanonicalMessages); err != nil {
		return StorageMetrics{}, wrapDB("count canonical messages", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM message_copies`).Scan(&metrics.MessageCopies); err != nil {
		return StorageMetrics{}, wrapDB("count message copies", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM reactions`).Scan(&metrics.Reactions); err != nil {
		return StorageMetrics{}, wrapDB("count reactions", err)
	}
	if dbPath != "" {
		if fi, err := os.Stat(dbPath); err == nil {
			metrics.DatabaseSizeBytes = fi.Size()
		}
	}
	return metrics, nil
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

func (s *Store) IsTombstoned(ctx context.Context, canonicalID string) (bool, error) {
	if err := requireOpaque("canonical id", canonicalID); err != nil {
		return false, err
	}
	var tombstonedAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT tombstoned_at FROM canonical_messages WHERE canonical_id = ?`, canonicalID).Scan(&tombstonedAt)
	if err != nil {
		return false, wrapDB("check tombstone", err)
	}
	return tombstonedAt.Valid, nil
}

func (s *Store) AddMessageCopy(ctx context.Context, copy MessageCopy) error {
	if err := validateCopy(copy); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO message_copies(canonical_id, endpoint_id, remote_message_id, created_at, from_self) VALUES (?, ?, ?, ?, ?)`,
		copy.CanonicalID, copy.EndpointID, copy.RemoteMessageID, unixMillis(copy.CreatedAt), copy.FromSelf,
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
		`SELECT canonical_id, endpoint_id, remote_message_id, created_at, from_self FROM message_copies WHERE canonical_id = ? AND endpoint_id = ?`,
		canonicalID, endpointID,
	).Scan(&copy.CanonicalID, &copy.EndpointID, &copy.RemoteMessageID, &createdAt, &copy.FromSelf)
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

func (s *Store) SavePollOptions(ctx context.Context, canonicalID string, optionHashes []string) error {
	if err := requireOpaque("canonical id", canonicalID); err != nil {
		return err
	}
	if len(optionHashes) == 0 {
		return errors.New("poll options are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapDB("begin save poll options", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `INSERT INTO poll_options(canonical_id, option_index, option_hash) VALUES (?, ?, ?)`)
	if err != nil {
		return wrapDB("prepare save poll option", err)
	}
	defer stmt.Close()

	for i, hash := range optionHashes {
		if strings.TrimSpace(hash) == "" {
			return errors.New("option hash is required")
		}
		if _, err := stmt.ExecContext(ctx, canonicalID, i, hash); err != nil {
			return wrapDB("insert poll option", err)
		}
	}
	return tx.Commit()
}

func (s *Store) GetPollOptions(ctx context.Context, canonicalID string) ([]string, error) {
	if err := requireOpaque("canonical id", canonicalID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT option_hash FROM poll_options WHERE canonical_id = ? ORDER BY option_index ASC`, canonicalID)
	if err != nil {
		return nil, wrapDB("get poll options", err)
	}
	defer rows.Close()

	var hashes []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, wrapDB("scan poll option", err)
		}
		hashes = append(hashes, h)
	}
	return hashes, rows.Err()
}

func (s *Store) IsPoll(ctx context.Context, canonicalID string) (bool, error) {
	if err := requireOpaque("canonical id", canonicalID); err != nil {
		return false, err
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM poll_options WHERE canonical_id = ?`, canonicalID).Scan(&count)
	if err != nil {
		return false, wrapDB("check is poll", err)
	}
	return count > 0, nil
}

func (s *Store) RecordPollVote(ctx context.Context, canonicalID, endpointID, actorHash string, optionHashes []string, updatedAt time.Time) error {
	if err := requireOpaque("canonical id", canonicalID); err != nil {
		return err
	}
	if err := validateEndpoint(endpointID); err != nil {
		return err
	}
	if !actorPattern.MatchString(actorHash) {
		return errors.New("actor hash must be an HMAC-derived user id")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapDB("begin record poll vote", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM poll_votes WHERE canonical_id = ? AND endpoint_id = ? AND actor_hash = ?`, canonicalID, endpointID, actorHash); err != nil {
		return wrapDB("delete old poll votes", err)
	}

	if len(optionHashes) > 0 {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO poll_votes(canonical_id, endpoint_id, actor_hash, option_hash, updated_at) VALUES (?, ?, ?, ?, ?)`)
		if err != nil {
			return wrapDB("prepare insert poll vote", err)
		}
		defer stmt.Close()

		for _, optHash := range optionHashes {
			if strings.TrimSpace(optHash) == "" {
				continue
			}
			if _, err := stmt.ExecContext(ctx, canonicalID, endpointID, actorHash, optHash, unixMillis(updatedAt)); err != nil {
				return wrapDB("insert poll vote", err)
			}
		}
	}

	return tx.Commit()
}

func (s *Store) GetPollVoteCounts(ctx context.Context, canonicalID string) (map[string]int, error) {
	if err := requireOpaque("canonical id", canonicalID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT option_hash, COUNT(*) FROM poll_votes WHERE canonical_id = ? GROUP BY option_hash`, canonicalID)
	if err != nil {
		return nil, wrapDB("get poll vote counts", err)
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var optHash string
		var count int
		if err := rows.Scan(&optHash, &count); err != nil {
			return nil, wrapDB("scan poll vote count", err)
		}
		counts[optHash] = count
	}
	return counts, rows.Err()
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
