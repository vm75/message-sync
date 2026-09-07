package controlstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneRetentionRemovesOrphanEvidenceAndKeepsActiveEvidence(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	evidenceDir := filepath.Join(root, "membership-evidence")
	if err := os.MkdirAll(evidenceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	activeRef := "active-evidence"
	orphanRef := "orphan-evidence"
	for _, ref := range []string{activeRef, orphanRef} {
		path := filepath.Join(evidenceDir, ref)
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Hour)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC()
	nowMS := now.UnixMilli()
	if _, err := store.db.Exec(`INSERT INTO users(id,username,password_hash,role,active,created_at,updated_at) VALUES ('admin','admin','hash','admin',1,?,?)`, nowMS, nowMS); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO verification_pipelines(id,public_token,label,target_transport,endpoint_alias,enabled,creator_user_id,created_at,updated_at) VALUES ('pipeline','token','Community','whatsapp','group-a',1,'admin',?,?)`, nowMS, nowMS); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO membership_requests(id,pipeline_id,status,applicant_work_email,verification_state,fulfillment_state,evidence_reference,created_at,updated_at) VALUES ('request','pipeline','pending_admin','applicant@example.com','verified','not_started',?,?,?)`, activeRef, nowMS, nowMS); err != nil {
		t.Fatal(err)
	}

	if _, err := store.PruneRetention(ctx, now, 30*24*time.Hour, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(evidenceDir, activeRef)); err != nil {
		t.Fatalf("active evidence was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(evidenceDir, orphanRef)); !os.IsNotExist(err) {
		t.Fatalf("orphan evidence was not removed: %v", err)
	}
}

func TestPruneRetentionPrunesSucceededApprovedRequests(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Now().UTC()
	old := now.Add(-31 * 24 * time.Hour).UnixMilli()
	nowMS := now.UnixMilli()
	if _, err := store.db.Exec(`INSERT INTO users(id,username,password_hash,role,active,created_at,updated_at) VALUES ('admin','admin','hash','admin',1,?,?)`, nowMS, nowMS); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO verification_pipelines(id,public_token,label,target_transport,endpoint_alias,enabled,creator_user_id,created_at,updated_at) VALUES ('pipeline','token','Community','whatsapp','group-a',1,'admin',?,?)`, nowMS, nowMS); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO membership_requests(id,pipeline_id,status,applicant_work_email,verification_state,fulfillment_state,created_at,updated_at) VALUES ('request','pipeline','approved','applicant@example.com','verified','succeeded',?,?)`, old, old); err != nil {
		t.Fatal(err)
	}

	if _, err := store.PruneRetention(ctx, now, 30*24*time.Hour, 100); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM membership_requests WHERE id='request'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("completed approved request was not pruned: count=%d", count)
	}
}
