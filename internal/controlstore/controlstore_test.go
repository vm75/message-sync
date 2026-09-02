package controlstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenCreatesSensitiveSchemaAndRestrictsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var foreignKeys int
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}
	for _, table := range []string{"users", "sessions", "user_invites", "audit_events", "verification_pipelines", "membership_requests", "email_challenges", "verification_assessments"} {
		var count int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("missing table %q", table)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("control.db permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestControlSchemaConstraintsAndNoRoutingSchemaMix(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.db.Exec(`INSERT INTO users(id, username, password_hash, role, created_at, updated_at) VALUES ('u1', 'Admin', 'hash', 'admin', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO users(id, username, password_hash, role, created_at, updated_at) VALUES ('u2', 'admin', 'hash', 'operator', 1, 1)`); err == nil {
		t.Fatal("case-insensitive username uniqueness was not enforced")
	}
	if _, err := s.db.Exec(`INSERT INTO sessions(token_hash, user_id, expires_at, created_at, last_seen_at) VALUES ('token-hash', 'u1', 2, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO sessions(token_hash, user_id, expires_at, created_at, last_seen_at) VALUES ('token-hash', 'u1', 2, 1, 1)`); err == nil {
		t.Fatal("session token hash uniqueness was not enforced")
	}
	var routingTables int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('canonical_messages', 'message_copies', 'endpoints')").Scan(&routingTables); err != nil {
		t.Fatal(err)
	}
	if routingTables != 0 {
		t.Fatal("control database contains routing tables")
	}
}

func TestPruneRetentionRemovesBoundedTerminalStateAndReturnsEvidence(t *testing.T) {
	s, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO users(id,username,password_hash,role,created_at,updated_at) VALUES('u','operator','hash','operator',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO verification_pipelines(id,public_token,label,target_transport,endpoint_alias,creator_user_id,created_at,updated_at) VALUES('p','token','test','whatsapp','alias','u',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour).UnixMilli()
	if _, err := s.db.Exec(`INSERT INTO membership_requests(id,pipeline_id,status,applicant_work_email,verification_state,evidence_reference,created_at,updated_at) VALUES('r','p','rejected','private@example.test','failed','opaque-evidence',?,?)`, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO email_challenges(id,membership_request_id,token_hash,expires_at) VALUES('c','r','challenge-hash',?)`, old); err != nil {
		t.Fatal(err)
	}
	refs, err := s.PruneRetention(context.Background(), time.Now(), 24*time.Hour, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0] != "opaque-evidence" {
		t.Fatalf("evidence refs = %v", refs)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM membership_requests`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("terminal request count = %d, want 0", count)
	}
}
