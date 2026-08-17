package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveOrCreateCanonicalIsPersistentAndIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sync.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	source := MessageCopy{
		EndpointID:      "c1g1",
		RemoteMessageID: "source-remote-id",
		CreatedAt:       time.Unix(1_700_000_000, 0).UTC(),
	}

	canonicalID, created, err := s.ResolveOrCreateCanonical(ctx, "c_first", source)
	if err != nil {
		t.Fatal(err)
	}
	if !created || canonicalID != "c_first" {
		t.Fatalf("first resolution = (%q, %v), want (c_first, true)", canonicalID, created)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	canonicalID, created, err = s.ResolveOrCreateCanonical(ctx, "c_second", source)
	if err != nil {
		t.Fatal(err)
	}
	if created || canonicalID != "c_first" {
		t.Fatalf("restart resolution = (%q, %v), want (c_first, false)", canonicalID, created)
	}
}
