package controlstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneRetentionKeepsFreshPollPresentationAndRemovesExpired(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	cipher, err := NewCredentialCipher([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	presentations, err := NewPollPresentationStore(s.DB(), cipher)
	if err != nil {
		t.Fatal(err)
	}

	if err := presentations.SavePollPresentation(ctx, "fresh-poll", "Fresh question", []string{"A", "B"}); err != nil {
		t.Fatal(err)
	}
	if err := presentations.SavePollPresentation(ctx, "expired-poll", "Expired question", []string{"C", "D"}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	freshUpdatedAt := now.Add(-time.Hour).UnixMilli()
	expiredUpdatedAt := now.Add(-48 * time.Hour).UnixMilli()
	if _, err := s.DB().ExecContext(ctx, `UPDATE poll_presentations SET created_at=?, updated_at=? WHERE canonical_id=?`, freshUpdatedAt, freshUpdatedAt, "fresh-poll"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `UPDATE poll_presentations SET created_at=?, updated_at=? WHERE canonical_id=?`, expiredUpdatedAt, expiredUpdatedAt, "expired-poll"); err != nil {
		t.Fatal(err)
	}

	if _, err := s.PruneRetention(ctx, now, 24*time.Hour, 100); err != nil {
		t.Fatal(err)
	}

	var freshCount int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM poll_presentations WHERE canonical_id=?`, "fresh-poll").Scan(&freshCount); err != nil {
		t.Fatal(err)
	}
	if freshCount != 1 {
		t.Fatalf("fresh poll presentation count = %d, want 1", freshCount)
	}

	var expiredCount int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM poll_presentations WHERE canonical_id=?`, "expired-poll").Scan(&expiredCount); err != nil {
		t.Fatal(err)
	}
	if expiredCount != 0 {
		t.Fatalf("expired poll presentation count = %d, want 0", expiredCount)
	}

	question, options, err := presentations.LoadPollPresentation(ctx, "fresh-poll")
	if err != nil {
		t.Fatal(err)
	}
	if question != "Fresh question" || len(options) != 2 || options[0] != "A" || options[1] != "B" {
		t.Fatalf("fresh poll presentation = %q, %#v", question, options)
	}
}

func TestFreshPollPresentationSurvivesReopenAndStartupPrune(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	secret := []byte("01234567890123456789012345678901")

	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := NewCredentialCipher(secret)
	if err != nil {
		t.Fatal(err)
	}
	presentations, err := NewPollPresentationStore(first.DB(), cipher)
	if err != nil {
		t.Fatal(err)
	}
	if err := presentations.SavePollPresentation(ctx, "restart-poll", "Restart question", []string{"One", "Two"}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.PruneRetention(ctx, time.Now().UTC(), 30*24*time.Hour, 100); err != nil {
		t.Fatal(err)
	}

	presentations, err = NewPollPresentationStore(reopened.DB(), cipher)
	if err != nil {
		t.Fatal(err)
	}
	question, options, err := presentations.LoadPollPresentation(ctx, "restart-poll")
	if err != nil {
		t.Fatal(err)
	}
	if question != "Restart question" || len(options) != 2 || options[0] != "One" || options[1] != "Two" {
		t.Fatalf("reopened poll presentation = %q, %#v", question, options)
	}
}
