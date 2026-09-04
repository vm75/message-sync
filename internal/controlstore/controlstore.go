// Package controlstore owns the sensitive control-plane database. It is kept
// separate from store so routing persistence can remain PII-free.
package controlstore

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

type Store struct{ db *sql.DB }

// PruneRetention removes only expired/terminal control-plane artifacts in a
// bounded transaction. Evidence references are queued durably before their
// membership rows are deleted, and are also returned so the caller may attempt
// immediate unlinking from the private evidence directory.
func (s *Store) PruneRetention(ctx context.Context, now time.Time, age time.Duration, batch int) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("control database is required")
	}
	if batch <= 0 {
		batch = 100
	}
	cutoff := now.Add(-age).UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin control retention: %w", err)
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM email_challenges WHERE rowid IN (SELECT rowid FROM email_challenges WHERE expires_at < ? LIMIT ?)`,
		`DELETE FROM membership_join_tokens WHERE rowid IN (SELECT rowid FROM membership_join_tokens WHERE expires_at < ? OR revoked_at IS NOT NULL OR consumed_at IS NOT NULL LIMIT ?)`,
		`DELETE FROM user_invites WHERE rowid IN (SELECT rowid FROM user_invites WHERE expires_at < ? OR consumed_at IS NOT NULL LIMIT ?)`,
		`DELETE FROM password_reset_tokens WHERE rowid IN (SELECT rowid FROM password_reset_tokens WHERE expires_at < ? OR consumed_at IS NOT NULL LIMIT ?)`,
		`DELETE FROM sessions WHERE rowid IN (SELECT rowid FROM sessions WHERE expires_at < ? OR revoked_at IS NOT NULL LIMIT ?)`,
	} {
		if _, err := tx.ExecContext(ctx, q, now.UnixMilli(), batch); err != nil {
			return nil, fmt.Errorf("prune control artifacts: %w", err)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT evidence_reference FROM membership_requests WHERE status IN ('rejected','fulfilled','cancelled','expired') AND updated_at < ? AND rowid IN (SELECT rowid FROM membership_requests WHERE status IN ('rejected','fulfilled','cancelled','expired') AND updated_at < ? LIMIT ?)`, cutoff, cutoff, batch)
	if err != nil {
		return nil, fmt.Errorf("select expired membership evidence: %w", err)
	}
	var refs []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan expired membership evidence: %w", err)
		}
		if ref != "" {
			refs = append(refs, ref)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate expired membership evidence: %w", err)
	}
	rows.Close()
	for _, ref := range refs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO evidence_cleanup_queue(evidence_reference,created_at,next_attempt_at) VALUES(?,?,?) ON CONFLICT(evidence_reference) DO UPDATE SET next_attempt_at=MIN(evidence_cleanup_queue.next_attempt_at,excluded.next_attempt_at)`, ref, now.UnixMilli(), now.UnixMilli()); err != nil {
			return nil, fmt.Errorf("queue membership evidence cleanup: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM membership_requests WHERE status IN ('rejected','fulfilled','cancelled','expired') AND updated_at < ? AND rowid IN (SELECT rowid FROM membership_requests WHERE status IN ('rejected','fulfilled','cancelled','expired') AND updated_at < ? LIMIT ?)`, cutoff, cutoff, batch); err != nil {
		return nil, fmt.Errorf("prune membership requests: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit control retention: %w", err)
	}
	return refs, nil
}

func Open(ctx context.Context, path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("control database path is required")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create control database directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open control database: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("configure control database: %w", err)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("begin control schema initialization: %w", err)
	}
	defer tx.Rollback()
	for _, statement := range strings.Split(schemaSQL, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("initialize control schema: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("commit control schema initialization: %w", err)
	}
	if path != ":memory:" {
		if err := os.Chmod(path, 0o600); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("secure control database permissions: %w", err)
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) DB() *sql.DB {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	_, _ = s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	_, _ = s.db.Exec("PRAGMA optimize")
	return s.db.Close()
}
