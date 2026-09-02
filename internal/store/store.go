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
	StreamKey      string
	Position       int64
	EventTimestamp time.Time
	UpdatedAt      time.Time
}

const (
	DeliveryQueued         = "queued"
	DeliveryRetrying       = "retrying"
	DeliveryAwaitingReplay = "awaiting_replay"
	DeliveryFailed         = "failed"
)

type DeliveryOperation struct {
	CanonicalID       string
	EndpointID        string
	OperationKind     string
	OperationRevision int64
	State             string
	AttemptCount      int
	NextAttemptAt     time.Time
	FailureClass      string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type DeliverySummary struct {
	EndpointID      string
	Queued          int64
	Retrying        int64
	AwaitingReplay  int64
	Failed          int64
	OldestActiveAge time.Duration
	FailureClass    string
}

type StorageMetrics struct {
	CanonicalMessages int64
	MessageCopies     int64
	Reactions         int64
	DatabaseSizeBytes int64
}

// PollOption is the provider-neutral option metadata for a canonical poll.
// WhatsAppHash is only a boundary-resolution value and may be empty for other
// transports.
type PollOption struct {
	Index        int
	WhatsAppHash string
}

type PollProviderRef struct {
	CanonicalID string
	EndpointID  string
	Provider    string
	Reference   string
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
	if err := initialize(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := markDeliveryOperationsAwaitingReplay(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("secure sync database permissions: %w", err)
	}
	return &Store{db: db}, nil
}

func markDeliveryOperationsAwaitingReplay(ctx context.Context, db *sql.DB) (int64, error) {
	result, err := db.ExecContext(ctx, `
		UPDATE delivery_operations
		SET state = ?, updated_at = ?
		WHERE state IN (?, ?)`, DeliveryAwaitingReplay, time.Now().UTC().UnixMilli(), DeliveryQueued, DeliveryRetrying)
	if err != nil {
		return 0, fmt.Errorf("mark delivery operations awaiting replay: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count delivery operations awaiting replay: %w", err)
	}
	return count, nil
}

func initialize(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin sync schema initialization: %w", err)
	}
	defer tx.Rollback()

	for _, statement := range strings.Split(schemaSQL, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize sync schema: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sync schema initialization: %w", err)
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

	if _, err := s.db.ExecContext(ctx, `DELETE FROM suppressed_reactions WHERE created_at < ?`, cutoffMillis); err != nil {
		return 0, wrapDB("prune suppressed reactions", err)
	}

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
		`INSERT INTO message_copies(canonical_id, endpoint_id, remote_message_id, created_at, from_self) VALUES (?, ?, ?, ?, ?)`,
		candidateCanonicalID, source.EndpointID, source.RemoteMessageID, unixMillis(createdAt), source.FromSelf,
	); err != nil {
		return "", false, fmt.Errorf("add source message copy: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", false, fmt.Errorf("commit canonical resolution: %w", err)
	}
	return candidateCanonicalID, true, nil
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
	if !endpointPattern.MatchString(cursor.StreamKey) {
		return errors.New("recovery stream key is required")
	}
	if cursor.Position < 0 {
		return errors.New("recovery cursor position must not be negative")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO recovery_cursors(stream_key, position, event_timestamp, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(stream_key) DO UPDATE SET
			position = excluded.position,
			event_timestamp = excluded.event_timestamp,
			updated_at = excluded.updated_at
		WHERE excluded.position > recovery_cursors.position
	`, cursor.StreamKey, cursor.Position, nullableMillis(cursor.EventTimestamp), unixMillis(cursor.UpdatedAt))
	return wrapDB("put recovery cursor", err)
}

func (s *Store) RecoveryCursor(ctx context.Context, streamKey string) (RecoveryCursor, error) {
	if !endpointPattern.MatchString(streamKey) {
		return RecoveryCursor{}, errors.New("recovery stream key is required")
	}
	var cursor RecoveryCursor
	var eventTimestamp sql.NullInt64
	var updated int64
	err := s.db.QueryRowContext(ctx,
		`SELECT stream_key, position, event_timestamp, updated_at FROM recovery_cursors WHERE stream_key = ?`, streamKey,
	).Scan(&cursor.StreamKey, &cursor.Position, &eventTimestamp, &updated)
	if err != nil {
		return RecoveryCursor{}, wrapDB("get recovery cursor", err)
	}
	if eventTimestamp.Valid {
		cursor.EventTimestamp = fromUnixMillis(eventTimestamp.Int64)
	}
	cursor.UpdatedAt = fromUnixMillis(updated)
	return cursor, nil
}

// MarkDeliveryOperationsAwaitingReplay makes payload-dependent work safe after
// restart. Payloads are intentionally not stored, so queued work cannot resume
// without a source replay.
func (s *Store) MarkDeliveryOperationsAwaitingReplay(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("sync store is required")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE delivery_operations
		SET state = ?, updated_at = ?
		WHERE state IN (?, ?)`, DeliveryAwaitingReplay, unixMillis(time.Now().UTC()), DeliveryQueued, DeliveryRetrying)
	if err != nil {
		return 0, wrapDB("mark delivery operations awaiting replay", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, wrapDB("count delivery operations awaiting replay", err)
	}
	return count, nil
}

func (s *Store) UpsertDeliveryOperation(ctx context.Context, operation DeliveryOperation) error {
	if err := validateDeliveryOperation(operation); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO delivery_operations(
			canonical_id, endpoint_id, operation_kind, operation_revision,
			state, attempt_count, next_attempt_at, failure_class, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(canonical_id, endpoint_id, operation_kind, operation_revision) DO NOTHING`,
		operation.CanonicalID, operation.EndpointID, operation.OperationKind, operation.OperationRevision,
		operation.State, operation.AttemptCount, nullableMillis(operation.NextAttemptAt), nullableString(operation.FailureClass),
		unixMillis(operation.CreatedAt), unixMillis(operation.UpdatedAt))
	return wrapDB("upsert delivery operation", err)
}

// ClaimDeliveryOperation advances a queued operation to retrying and increments
// its attempt count atomically. It returns false when another worker claimed it.
func (s *Store) ClaimDeliveryOperation(ctx context.Context, operation DeliveryOperation, now time.Time) (bool, error) {
	if err := validateDeliveryIdentity(operation); err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE delivery_operations
		SET state = ?, attempt_count = attempt_count + 1, updated_at = ?
		WHERE canonical_id = ? AND endpoint_id = ? AND operation_kind = ? AND operation_revision = ?
		  AND state = ?`, DeliveryRetrying, unixMillis(now), operation.CanonicalID, operation.EndpointID,
		operation.OperationKind, operation.OperationRevision, DeliveryQueued)
	if err != nil {
		return false, wrapDB("claim delivery operation", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, wrapDB("count claimed delivery operation", err)
	}
	return count == 1, nil
}

// BeginDeliveryAttempt records an attempt without changing the operation's
// identity or retaining any payload.
func (s *Store) BeginDeliveryAttempt(ctx context.Context, operation DeliveryOperation, now time.Time) error {
	if err := validateDeliveryIdentity(operation); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE delivery_operations
		SET state = ?, attempt_count = attempt_count + 1, updated_at = ?
		WHERE canonical_id = ? AND endpoint_id = ? AND operation_kind = ? AND operation_revision = ?`,
		DeliveryRetrying, unixMillis(now), operation.CanonicalID, operation.EndpointID, operation.OperationKind, operation.OperationRevision)
	return wrapDB("begin delivery attempt", err)
}

func (s *Store) SetDeliveryOperationState(ctx context.Context, operation DeliveryOperation, state string, nextAttemptAt time.Time, failureClass string, updatedAt time.Time) error {
	if err := validateDeliveryIdentity(operation); err != nil {
		return err
	}
	if !validDeliveryState(state) {
		return errors.New("invalid delivery operation state")
	}
	if failureClass != "" && !validDeliveryFailureClass(failureClass) {
		return errors.New("invalid delivery failure class")
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE delivery_operations
		SET state = ?, next_attempt_at = ?, failure_class = ?, updated_at = ?
		WHERE canonical_id = ? AND endpoint_id = ? AND operation_kind = ? AND operation_revision = ?`,
		state, nullableMillis(nextAttemptAt), nullableString(failureClass), unixMillis(updatedAt), operation.CanonicalID,
		operation.EndpointID, operation.OperationKind, operation.OperationRevision)
	return wrapDB("set delivery operation state", err)
}

func (s *Store) MarkDeliveryOperationAwaitingReplay(ctx context.Context, operation DeliveryOperation, updatedAt time.Time) error {
	return s.SetDeliveryOperationState(ctx, operation, DeliveryAwaitingReplay, time.Time{}, "", updatedAt)
}

func (s *Store) MarkDeliveryOperationFailed(ctx context.Context, operation DeliveryOperation, failureClass string, updatedAt time.Time) error {
	return s.SetDeliveryOperationState(ctx, operation, DeliveryFailed, time.Time{}, failureClass, updatedAt)
}

func (s *Store) DeleteDeliveryOperation(ctx context.Context, operation DeliveryOperation) error {
	if err := validateDeliveryIdentity(operation); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM delivery_operations
		WHERE canonical_id = ? AND endpoint_id = ? AND operation_kind = ? AND operation_revision = ?`,
		operation.CanonicalID, operation.EndpointID, operation.OperationKind, operation.OperationRevision)
	return wrapDB("delete delivery operation", err)
}

func (s *Store) DeliverySummaries(ctx context.Context, now time.Time) (map[string]DeliverySummary, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("sync store is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT endpoint_id, state, updated_at, failure_class
		FROM delivery_operations
		ORDER BY endpoint_id ASC, updated_at ASC`)
	if err != nil {
		return nil, wrapDB("query delivery summaries", err)
	}
	defer rows.Close()
	result := make(map[string]DeliverySummary)
	for rows.Next() {
		var endpointID, state string
		var updatedAt int64
		var failureClass sql.NullString
		if err := rows.Scan(&endpointID, &state, &updatedAt, &failureClass); err != nil {
			return nil, wrapDB("scan delivery summary", err)
		}
		summary := result[endpointID]
		summary.EndpointID = endpointID
		switch state {
		case DeliveryQueued:
			summary.Queued++
		case DeliveryRetrying:
			summary.Retrying++
		case DeliveryAwaitingReplay:
			summary.AwaitingReplay++
		case DeliveryFailed:
			summary.Failed++
		}
		if state != DeliveryFailed {
			age := now.Sub(fromUnixMillis(updatedAt))
			if age < 0 {
				age = 0
			}
			if age > summary.OldestActiveAge {
				summary.OldestActiveAge = age
			}
		}
		if state == DeliveryFailed && failureClass.Valid {
			summary.FailureClass = failureClass.String
		}
		result[endpointID] = summary
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("iterate delivery summaries", err)
	}
	return result, nil
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

// SavePollOptionMetadata stores canonical option order and optional WhatsApp
// hashes. It is intentionally separate from provider-specific poll models.
func (s *Store) SavePollOptionMetadata(ctx context.Context, canonicalID string, options []PollOption) error {
	if err := requireOpaque("canonical id", canonicalID); err != nil {
		return err
	}
	if len(options) == 0 {
		return errors.New("poll options are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapDB("begin save poll option metadata", err)
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO poll_options(canonical_id, option_index, option_hash) VALUES (?, ?, ?)`)
	if err != nil {
		return wrapDB("prepare save poll option metadata", err)
	}
	defer stmt.Close()
	for expected, option := range options {
		if option.Index != expected || option.Index < 0 || strings.ContainsAny(option.WhatsAppHash, "\r\n\x00") {
			return errors.New("poll option indexes must be contiguous and non-negative")
		}
		if _, err := stmt.ExecContext(ctx, canonicalID, option.Index, nullableString(option.WhatsAppHash)); err != nil {
			return wrapDB("insert poll option metadata", err)
		}
	}
	return tx.Commit()
}

func (s *Store) GetPollOptionMetadata(ctx context.Context, canonicalID string) ([]PollOption, error) {
	if err := requireOpaque("canonical id", canonicalID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT option_index, COALESCE(option_hash, '') FROM poll_options WHERE canonical_id = ? ORDER BY option_index ASC`, canonicalID)
	if err != nil {
		return nil, wrapDB("get poll option metadata", err)
	}
	defer rows.Close()
	var options []PollOption
	for rows.Next() {
		var option PollOption
		if err := rows.Scan(&option.Index, &option.WhatsAppHash); err != nil {
			return nil, wrapDB("scan poll option metadata", err)
		}
		options = append(options, option)
	}
	return options, rows.Err()
}

func (s *Store) PollOptionIndexForWhatsAppHash(ctx context.Context, canonicalID, optionHash string) (int, error) {
	if err := requireOpaque("canonical id", canonicalID); err != nil {
		return 0, err
	}
	if strings.TrimSpace(optionHash) == "" {
		return 0, errors.New("option hash is required")
	}
	var index int
	err := s.db.QueryRowContext(ctx, `SELECT option_index FROM poll_options WHERE canonical_id = ? AND option_hash = ?`, canonicalID, optionHash).Scan(&index)
	if err != nil {
		return 0, wrapDB("resolve WhatsApp poll option", err)
	}
	return index, nil
}

func (s *Store) GetPollOptions(ctx context.Context, canonicalID string) ([]string, error) {
	if err := requireOpaque("canonical id", canonicalID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(option_hash, '') FROM poll_options WHERE canonical_id = ? ORDER BY option_index ASC`, canonicalID)
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

// RecordSuppressedReaction records a reaction that was sent by the bridge to prevent echoing.
func (s *Store) RecordSuppressedReaction(ctx context.Context, endpointID, remoteMessageID, emoji string, createdAt time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("sync store is required")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO suppressed_reactions(endpoint_id, remote_message_id, emoji, created_at) VALUES (?, ?, ?, ?)`,
		endpointID, remoteMessageID, emoji, unixMillis(createdAt),
	)
	return wrapDB("record suppressed reaction", err)
}

// CheckAndClearSuppressedReaction checks if a reaction echo suppression marker exists, and deletes it atomically.
// Returns true if the reaction was found and deleted (meaning it should be suppressed).
func (s *Store) CheckAndClearSuppressedReaction(ctx context.Context, endpointID, remoteMessageID, emoji string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("sync store is required")
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM suppressed_reactions WHERE endpoint_id = ? AND remote_message_id = ? AND emoji = ?`,
		endpointID, remoteMessageID, emoji,
	)
	if err != nil {
		return false, wrapDB("check suppressed reaction", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("check suppressed reaction rows affected: %w", err)
	}
	return affected > 0, nil
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

	indexes := make([]int, 0, len(optionHashes))
	for _, optionHash := range optionHashes {
		index, err := s.PollOptionIndexForWhatsAppHash(ctx, canonicalID, optionHash)
		if err != nil {
			return err
		}
		indexes = append(indexes, index)
	}
	return s.ReplacePollActorSelections(ctx, canonicalID, endpointID, actorHash, indexes, updatedAt)
}

// ReplacePollActorSelections atomically replaces one actor's complete
// selection set and makes the endpoint's contribution actor-backed.
func (s *Store) ReplacePollActorSelections(ctx context.Context, canonicalID, endpointID, actorHash string, optionIndexes []int, updatedAt time.Time) error {
	if err := requireOpaque("canonical id", canonicalID); err != nil {
		return err
	}
	if err := validateEndpoint(endpointID); err != nil {
		return err
	}
	if !actorPattern.MatchString(actorHash) {
		return errors.New("actor hash must be an HMAC-derived user id")
	}
	if err := validatePollIndexes(optionIndexes); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapDB("begin replace poll actor selections", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `INSERT INTO poll_endpoint_sources(canonical_id, endpoint_id, source_kind) VALUES (?, ?, 'actor') ON CONFLICT(canonical_id, endpoint_id) DO UPDATE SET source_kind = 'actor'`, canonicalID, endpointID); err != nil {
		return wrapDB("set actor poll source", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM poll_actor_selections WHERE canonical_id = ? AND endpoint_id = ? AND actor_hash = ?`, canonicalID, endpointID, actorHash); err != nil {
		return wrapDB("delete old poll actor selections", err)
	}
	if len(optionIndexes) > 0 {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO poll_actor_selections(canonical_id, endpoint_id, actor_hash, option_index, updated_at) VALUES (?, ?, ?, ?, ?)`)
		if err != nil {
			return wrapDB("prepare insert poll actor selection", err)
		}
		defer stmt.Close()
		for _, optionIndex := range optionIndexes {
			if _, err := stmt.ExecContext(ctx, canonicalID, endpointID, actorHash, optionIndex, unixMillis(updatedAt)); err != nil {
				return wrapDB("insert poll actor selection", err)
			}
		}
	}
	return tx.Commit()
}

func validatePollIndexes(indexes []int) error {
	seen := make(map[int]struct{}, len(indexes))
	for _, index := range indexes {
		if index < 0 {
			return errors.New("poll option index must not be negative")
		}
		if _, ok := seen[index]; ok {
			return errors.New("poll option indexes must be unique")
		}
		seen[index] = struct{}{}
	}
	return nil
}

// ReplacePollEndpointSnapshot atomically replaces an endpoint's absolute
// aggregate and makes that endpoint snapshot-backed.
func (s *Store) ReplacePollEndpointSnapshot(ctx context.Context, canonicalID, endpointID string, counts map[int]int, updatedAt time.Time) error {
	if err := requireOpaque("canonical id", canonicalID); err != nil {
		return err
	}
	if err := validateEndpoint(endpointID); err != nil {
		return err
	}
	for index, count := range counts {
		if index < 0 || count < 0 {
			return errors.New("poll snapshot indexes and counts must not be negative")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapDB("begin replace poll endpoint snapshot", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO poll_endpoint_sources(canonical_id, endpoint_id, source_kind) VALUES (?, ?, 'snapshot') ON CONFLICT(canonical_id, endpoint_id) DO UPDATE SET source_kind = 'snapshot'`, canonicalID, endpointID); err != nil {
		return wrapDB("set snapshot poll source", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM poll_endpoint_snapshots WHERE canonical_id = ? AND endpoint_id = ?`, canonicalID, endpointID); err != nil {
		return wrapDB("delete old poll endpoint snapshot", err)
	}
	for index, count := range counts {
		if _, err := tx.ExecContext(ctx, `INSERT INTO poll_endpoint_snapshots(canonical_id, endpoint_id, option_index, option_count, updated_at) VALUES (?, ?, ?, ?, ?)`, canonicalID, endpointID, index, count, unixMillis(updatedAt)); err != nil {
			return wrapDB("insert poll endpoint snapshot", err)
		}
	}
	return tx.Commit()
}

func (s *Store) GetPollAggregateCounts(ctx context.Context, canonicalID string) (map[int]int, error) {
	if err := requireOpaque("canonical id", canonicalID); err != nil {
		return nil, err
	}
	counts := make(map[int]int)
	rows, err := s.db.QueryContext(ctx, `
		SELECT option_index, SUM(count) FROM (
			SELECT s.option_index, COUNT(*) AS count
			FROM poll_endpoint_sources p
			JOIN poll_actor_selections s ON s.canonical_id = p.canonical_id AND s.endpoint_id = p.endpoint_id
			WHERE p.canonical_id = ? AND p.source_kind = 'actor'
			GROUP BY s.option_index
			UNION ALL
			SELECT s.option_index, s.option_count AS count
			FROM poll_endpoint_sources p
			JOIN poll_endpoint_snapshots s ON s.canonical_id = p.canonical_id AND s.endpoint_id = p.endpoint_id
			WHERE p.canonical_id = ? AND p.source_kind = 'snapshot'
		) GROUP BY option_index`, canonicalID, canonicalID)
	if err != nil {
		return nil, wrapDB("get poll aggregate counts", err)
	}
	defer rows.Close()
	for rows.Next() {
		var index, count int
		if err := rows.Scan(&index, &count); err != nil {
			return nil, wrapDB("scan poll aggregate count", err)
		}
		counts[index] = count
	}
	return counts, rows.Err()
}

// GetPollVoteCounts retains the old hash-shaped read API for existing callers;
// new code should use GetPollAggregateCounts.
func (s *Store) GetPollVoteCounts(ctx context.Context, canonicalID string) (map[string]int, error) {
	options, err := s.GetPollOptionMetadata(ctx, canonicalID)
	if err != nil {
		return nil, err
	}
	indexes, err := s.GetPollAggregateCounts(ctx, canonicalID)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int)
	for _, option := range options {
		if option.WhatsAppHash != "" {
			counts[option.WhatsAppHash] = indexes[option.Index]
		}
	}
	return counts, nil
}

func (s *Store) SavePollProviderRef(ctx context.Context, ref PollProviderRef) error {
	if err := requireOpaque("canonical id", ref.CanonicalID); err != nil {
		return err
	}
	if err := validateEndpoint(ref.EndpointID); err != nil {
		return err
	}
	if strings.TrimSpace(ref.Provider) == "" || strings.ContainsAny(ref.Provider, "\r\n\x00") {
		return errors.New("poll provider is required")
	}
	if err := requireOpaque("poll provider reference", ref.Reference); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO poll_provider_refs(canonical_id, endpoint_id, provider_kind, provider_ref) VALUES (?, ?, ?, ?) ON CONFLICT(canonical_id, endpoint_id, provider_kind) DO UPDATE SET provider_ref = excluded.provider_ref`, ref.CanonicalID, ref.EndpointID, ref.Provider, ref.Reference)
	return wrapDB("save poll provider reference", err)
}

func (s *Store) PollCanonicalForProviderRef(ctx context.Context, endpointID, provider, reference string) (string, error) {
	if err := validateEndpoint(endpointID); err != nil {
		return "", err
	}
	if strings.TrimSpace(provider) == "" {
		return "", errors.New("poll provider is required")
	}
	if err := requireOpaque("poll provider reference", reference); err != nil {
		return "", err
	}
	var canonicalID string
	err := s.db.QueryRowContext(ctx, `SELECT canonical_id FROM poll_provider_refs WHERE endpoint_id = ? AND provider_kind = ? AND provider_ref = ?`, endpointID, provider, reference).Scan(&canonicalID)
	if err != nil {
		return "", wrapDB("resolve poll provider reference", err)
	}
	return canonicalID, nil
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

func validateDeliveryIdentity(operation DeliveryOperation) error {
	if err := requireOpaque("canonical id", operation.CanonicalID); err != nil {
		return err
	}
	if err := validateEndpoint(operation.EndpointID); err != nil {
		return err
	}
	if strings.TrimSpace(operation.OperationKind) == "" || strings.ContainsAny(operation.OperationKind, "\r\n\x00") {
		return errors.New("delivery operation kind is required")
	}
	if operation.OperationRevision < 0 {
		return errors.New("delivery operation revision must not be negative")
	}
	return nil
}

func validateDeliveryOperation(operation DeliveryOperation) error {
	if err := validateDeliveryIdentity(operation); err != nil {
		return err
	}
	if !validDeliveryState(operation.State) {
		return errors.New("invalid delivery operation state")
	}
	if operation.AttemptCount < 0 {
		return errors.New("delivery attempt count must not be negative")
	}
	if operation.FailureClass != "" && !validDeliveryFailureClass(operation.FailureClass) {
		return errors.New("invalid delivery failure class")
	}
	return nil
}

func validDeliveryState(state string) bool {
	return state == DeliveryQueued || state == DeliveryRetrying || state == DeliveryAwaitingReplay || state == DeliveryFailed
}

func validDeliveryFailureClass(class string) bool {
	switch class {
	case "transient", "rate_limited", "permission_denied", "destination_missing", "payload_rejected", "unsupported":
		return true
	default:
		return false
	}
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
