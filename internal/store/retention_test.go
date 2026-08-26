package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestPruneRetentionAndCascadingDeletes(t *testing.T) {
	store, path := openTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	oldTime := now.AddDate(0, 0, -100)   // 100 days old
	recentTime := now.AddDate(0, 0, -10) // 10 days old

	// Insert 5 old canonical messages with copies and reactions
	for i := 0; i < 5; i++ {
		cid := fmt.Sprintf("old-canon-%d", i)
		if err := store.CreateCanonical(ctx, cid, oldTime); err != nil {
			t.Fatal(err)
		}
		if err := store.AddMessageCopy(ctx, MessageCopy{
			CanonicalID:     cid,
			EndpointID:      "c1g1",
			RemoteMessageID: fmt.Sprintf("old-remote-1-%d", i),
			CreatedAt:       oldTime,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.AddMessageCopy(ctx, MessageCopy{
			CanonicalID:     cid,
			EndpointID:      "c1g2",
			RemoteMessageID: fmt.Sprintf("old-remote-2-%d", i),
			CreatedAt:       oldTime,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertReaction(ctx, Reaction{
			CanonicalID:      cid,
			SourceEndpointID: "c1g1",
			ActorHash:        "u_abcdefghij",
			Emoji:            "👍",
			UpdatedAt:        oldTime,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Insert 2 recent canonical messages with copies and reactions
	for i := 0; i < 2; i++ {
		cid := fmt.Sprintf("recent-canon-%d", i)
		if err := store.CreateCanonical(ctx, cid, recentTime); err != nil {
			t.Fatal(err)
		}
		if err := store.AddMessageCopy(ctx, MessageCopy{
			CanonicalID:     cid,
			EndpointID:      "c1g1",
			RemoteMessageID: fmt.Sprintf("recent-remote-1-%d", i),
			CreatedAt:       recentTime,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.AddMessageCopy(ctx, MessageCopy{
			CanonicalID:     cid,
			EndpointID:      "c1g2",
			RemoteMessageID: fmt.Sprintf("recent-remote-2-%d", i),
			CreatedAt:       recentTime,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertReaction(ctx, Reaction{
			CanonicalID:      cid,
			SourceEndpointID: "c1g1",
			ActorHash:        "u_abcdefghij",
			Emoji:            "❤️",
			UpdatedAt:        recentTime,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Verify metrics before pruning
	metricsBefore, err := store.Metrics(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if metricsBefore.CanonicalMessages != 7 {
		t.Fatalf("canonical count before = %d, want 7", metricsBefore.CanonicalMessages)
	}
	if metricsBefore.MessageCopies != 14 {
		t.Fatalf("copies count before = %d, want 14", metricsBefore.MessageCopies)
	}
	if metricsBefore.Reactions != 7 {
		t.Fatalf("reactions count before = %d, want 7", metricsBefore.Reactions)
	}
	if metricsBefore.DatabaseSizeBytes <= 0 {
		t.Fatalf("database size bytes = %d, want > 0", metricsBefore.DatabaseSizeBytes)
	}

	// Prune with retention 90 days in batches of 2 (testing batching behavior)
	deleted, err := store.PruneRetention(ctx, 90, 2)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 5 {
		t.Fatalf("deleted %d canonical messages, want 5", deleted)
	}

	// Verify metrics after pruning
	metricsAfter, err := store.Metrics(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if metricsAfter.CanonicalMessages != 2 {
		t.Fatalf("canonical count after = %d, want 2", metricsAfter.CanonicalMessages)
	}
	if metricsAfter.MessageCopies != 4 {
		t.Fatalf("copies count after = %d, want 4", metricsAfter.MessageCopies)
	}
	if metricsAfter.Reactions != 2 {
		t.Fatalf("reactions count after = %d, want 2", metricsAfter.Reactions)
	}

	// Verify that the recent messages are still accessible and old ones are gone
	for i := 0; i < 2; i++ {
		cid := fmt.Sprintf("recent-canon-%d", i)
		found, err := store.CanonicalForRemote(ctx, "c1g1", fmt.Sprintf("recent-remote-1-%d", i))
		if err != nil || found != cid {
			t.Fatalf("recent message lookup failed: %v", err)
		}
	}
	for i := 0; i < 5; i++ {
		_, err := store.CanonicalForRemote(ctx, "c1g1", fmt.Sprintf("old-remote-1-%d", i))
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("expected sql.ErrNoRows for pruned old message, got %v", err)
		}
	}
}

func TestPruneValidation(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()

	if _, err := store.PruneRetention(ctx, 0, 10); err == nil {
		t.Fatal("expected error for zero retention days")
	}
	if _, err := store.PruneRetention(ctx, -5, 10); err == nil {
		t.Fatal("expected error for negative retention days")
	}
}

func TestStoreCloseExecutesCheckpoint(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()

	if err := store.CreateCanonical(ctx, "canon-test", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}
