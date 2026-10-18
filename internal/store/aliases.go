package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/vm75/message-sync/internal/identity"
	"modernc.org/sqlite"
)

var ErrAliasExhausted = errors.New("alias ordinal namespace exhausted")

type AliasRecord struct {
	SyncSetID string
	ActorID   string
	Ordinal   int64
	Alias     string
}

type aliasScanner interface {
	Scan(...any) error
}

func scanAlias(row aliasScanner) (AliasRecord, error) {
	var record AliasRecord
	if err := row.Scan(&record.SyncSetID, &record.ActorID, &record.Ordinal, &record.Alias); err != nil {
		return AliasRecord{}, err
	}
	expected, err := identity.SyncSetAlias(record.Ordinal)
	if err != nil || expected != record.Alias || !actorPattern.MatchString(record.ActorID) || strings.TrimSpace(record.SyncSetID) == "" {
		return AliasRecord{}, errors.New("inconsistent alias assignment")
	}
	return record, nil
}

const aliasColumns = `sync_set_id, actor_id, ordinal, alias`

// ObserveSyncSetActor atomically assigns one permanent alias. BEGIN IMMEDIATE
// acquires the SQLite writer lock before the read, including across processes.
func (s *Store) ObserveSyncSetActor(ctx context.Context, setID, actorID string) (AliasRecord, error) {
	if s == nil || s.db == nil {
		return AliasRecord{}, errors.New("sync store is required")
	}
	if setID == "" || strings.TrimSpace(setID) != setID || !actorPattern.MatchString(actorID) {
		return AliasRecord{}, errors.New("invalid alias identity")
	}
	lookup := s.db.QueryRowContext(ctx, `SELECT `+aliasColumns+` FROM sync_set_actors WHERE sync_set_id=? AND actor_id=?`, setID, actorID)
	if record, err := scanAlias(lookup); !errors.Is(err, sql.ErrNoRows) {
		return record, err
	}
	allocationCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		record, err := s.allocateAlias(allocationCtx, setID, actorID)
		if err == nil {
			return record, nil
		}
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || (sqliteErr.Code()&255 != 5 && sqliteErr.Code()&255 != 6 && sqliteErr.Code() != 2067 && sqliteErr.Code() != 1555) {
			return AliasRecord{}, fmt.Errorf("allocate actor alias: %w", err)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-allocationCtx.Done():
			timer.Stop()
			return AliasRecord{}, fmt.Errorf("allocate actor alias: %w", allocationCtx.Err())
		case <-timer.C:
		}
	}
}

func (s *Store) allocateAlias(ctx context.Context, setID, actorID string) (AliasRecord, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return AliasRecord{}, err
	}
	defer conn.Close()
	// SQLite's busy handler can outlive a short Go context. Poll the writer lock
	// in small intervals, restoring this connection's original policy afterward.
	var busyTimeout int
	if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		return AliasRecord{}, err
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA busy_timeout=25`); err != nil {
		return AliasRecord{}, err
	}
	defer conn.ExecContext(context.Background(), fmt.Sprintf("PRAGMA busy_timeout=%d", busyTimeout))
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return AliasRecord{}, err
	}
	// A competing writer may already have assigned this actor.
	if record, err := scanAlias(conn.QueryRowContext(ctx, `SELECT `+aliasColumns+` FROM sync_set_actors WHERE sync_set_id=? AND actor_id=?`, setID, actorID)); !errors.Is(err, sql.ErrNoRows) {
		return record, err
	}
	var ordinal int64
	var exhausted bool
	if err := conn.QueryRowContext(ctx, `SELECT next_ordinal, exhausted FROM alias_sequence WHERE singleton=1`).Scan(&ordinal, &exhausted); err != nil {
		return AliasRecord{}, err
	}
	if exhausted {
		return AliasRecord{}, ErrAliasExhausted
	}
	alias, err := identity.SyncSetAlias(ordinal)
	if err != nil {
		return AliasRecord{}, err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO sync_set_actors(sync_set_id, actor_id, ordinal, alias) VALUES(?,?,?,?)`, setID, actorID, ordinal, alias); err != nil {
		return AliasRecord{}, err
	}
	if ordinal == math.MaxInt64 {
		_, err = conn.ExecContext(ctx, `UPDATE alias_sequence SET exhausted=1 WHERE singleton=1`)
	} else {
		_, err = conn.ExecContext(ctx, `UPDATE alias_sequence SET next_ordinal=? WHERE singleton=1`, ordinal+1)
	}
	if err != nil {
		return AliasRecord{}, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return AliasRecord{}, err
	}
	return AliasRecord{SyncSetID: setID, ActorID: actorID, Ordinal: ordinal, Alias: alias}, nil
}

func (s *Store) AliasByActor(ctx context.Context, setID, actorID string) (string, error) {
	if s == nil || s.db == nil {
		return "", errors.New("sync store is required")
	}
	record, err := scanAlias(s.db.QueryRowContext(ctx, `SELECT `+aliasColumns+` FROM sync_set_actors WHERE sync_set_id=? AND actor_id=?`, setID, actorID))
	return record.Alias, err
}

func (s *Store) ActorByAlias(ctx context.Context, alias string) (AliasRecord, error) {
	if s == nil || s.db == nil {
		return AliasRecord{}, errors.New("sync store is required")
	}
	if !identity.ValidAlias(alias) {
		return AliasRecord{}, sql.ErrNoRows
	}
	return scanAlias(s.db.QueryRowContext(ctx, `SELECT `+aliasColumns+` FROM sync_set_actors WHERE alias=? COLLATE NOCASE`, alias))
}

func (s *Store) ActorInSyncSetByAlias(ctx context.Context, setID, alias string) (AliasRecord, error) {
	record, err := s.ActorByAlias(ctx, alias)
	if err != nil {
		return AliasRecord{}, err
	}
	if record.SyncSetID != setID {
		return AliasRecord{}, sql.ErrNoRows
	}
	return record, nil
}

// ActorEndpoints lists historical observations only. Callers must independently
// verify current membership, target permissions and private-inbox ownership.
func (s *Store) ActorEndpoints(ctx context.Context, setID, actorID string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("sync store is required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT p.endpoint_id FROM participant_addresses p JOIN endpoints e ON e.alias=p.endpoint_id WHERE e.sync_set_id=? AND p.actor_id=? ORDER BY p.endpoint_id`, setID, actorID)
	if err != nil {
		return nil, fmt.Errorf("find actor endpoints: %w", err)
	}
	defer rows.Close()
	var endpoints []string
	for rows.Next() {
		var endpoint string
		if err := rows.Scan(&endpoint); err != nil {
			return nil, err
		}
		endpoints = append(endpoints, endpoint)
	}
	return endpoints, rows.Err()
}
