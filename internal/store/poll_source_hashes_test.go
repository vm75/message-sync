package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"
)

func TestPollSourceHashesSurviveRestartAndCascade(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sync.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	hash := func(text string) string {
		sum := sha256.Sum256([]byte(text))
		return hex.EncodeToString(sum[:])
	}
	original, projected := hash("With @123"), hash("With @Amber Badger")
	if err := s.CreateCanonical(ctx, "canon-poll-source", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePollOptions(ctx, "canon-poll-source", []string{projected}); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePollSourceOptionHashes(ctx, "canon-poll-source", "source", []string{original}); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePollSourceOptionHashes(ctx, "canon-poll-source", "source", []string{"message content"}); err == nil {
		t.Fatal("accepted content as an option hash")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for endpoint, option := range map[string]string{"source": original, "destination": projected} {
		if err := s.RecordPollVote(ctx, "canon-poll-source", endpoint, "u_abcdefghij", []string{option}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	counts, err := s.GetPollAggregateCounts(ctx, "canon-poll-source")
	if err != nil || counts[0] != 2 {
		t.Fatalf("votes after restart: %v %v", counts, err)
	}
	if err := s.RecordPollVote(ctx, "canon-poll-source", "source", "u_abcdefghij", []string{projected}, time.Now()); err == nil {
		t.Fatal("source accepted a hash from a different poll presentation")
	}
	if _, err := s.db.Exec(`DELETE FROM canonical_messages WHERE canonical_id='canon-poll-source'`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM poll_source_option_hashes`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("hash mapping did not cascade: %d %v", count, err)
	}
}
