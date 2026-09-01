package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sync.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func TestOpenInitializesSchemaAndEnablesForeignKeys(t *testing.T) {
	store, _ := openTestStore(t)
	var enabled int
	if err := store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 {
		t.Fatalf("foreign_keys = %d, want 1", enabled)
	}
	var schemaMetaCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_meta'`).Scan(&schemaMetaCount); err != nil {
		t.Fatal(err)
	}
	if schemaMetaCount != 0 {
		t.Fatal("fresh schema unexpectedly contains schema metadata")
	}
}

func TestFreshSchemaCreatesTransportAwareEndpoints(t *testing.T) {
	store, _ := openTestStore(t)

	rows, err := store.db.Query(`PRAGMA table_info(endpoints)`)
	if err != nil {
		t.Fatal(err)
	}
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alias", "transport", "remote_id", "sync_set_id"} {
		if !columns[name] {
			t.Fatalf("endpoints missing column %q", name)
		}
	}

	var legacyCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='groups'`).Scan(&legacyCount); err != nil {
		t.Fatal(err)
	}
	if legacyCount != 0 {
		t.Fatal("fresh schema unexpectedly contains legacy groups table")
	}
}

func TestEndpointSchemaEnforcesAliasAndTransportRemoteUniqueness(t *testing.T) {
	store, _ := openTestStore(t)

	if _, err := store.db.Exec(`INSERT INTO sync_sets(id) VALUES ('mesh')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO endpoints(alias, transport, remote_id, sync_set_id) VALUES ('a', 'whatsapp', '1@g.us', 'mesh')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO endpoints(alias, transport, remote_id, sync_set_id) VALUES ('a', 'discord', '123', 'mesh')`); err == nil {
		t.Fatal("expected duplicate alias to be rejected")
	}
	if _, err := store.db.Exec(`INSERT INTO endpoints(alias, transport, remote_id, sync_set_id) VALUES ('b', 'whatsapp', '1@g.us', 'mesh')`); err == nil {
		t.Fatal("expected duplicate WhatsApp remote target to be rejected")
	}
	if _, err := store.db.Exec(`INSERT INTO endpoints(alias, transport, remote_id, sync_set_id) VALUES ('b', 'discord', '1@g.us', 'mesh')`); err != nil {
		t.Fatalf("same opaque remote id on another transport should be allowed: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO endpoints(alias, transport, remote_id, sync_set_id) VALUES ('t', 'telegram', '-1001234567890', 'mesh')`); err != nil {
		t.Fatalf("Telegram endpoint should be allowed by schema: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO endpoints(alias, transport, remote_id, sync_set_id) VALUES ('x', 'unknown', 'opaque', 'mesh')`); err == nil {
		t.Fatal("expected unknown transport to be rejected by schema")
	}
}

func TestMessageCopyUniquenessAndBidirectionalLookup(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	for _, id := range []string{"canon-a", "canon-b"} {
		if err := store.CreateCanonical(ctx, id, now); err != nil {
			t.Fatal(err)
		}
	}
	first := MessageCopy{CanonicalID: "canon-a", EndpointID: "c1g1", RemoteMessageID: "ABC123", CreatedAt: now}
	if err := store.AddMessageCopy(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.AddMessageCopy(ctx, MessageCopy{CanonicalID: "canon-a", EndpointID: "c1g1", RemoteMessageID: "OTHER", CreatedAt: now}); err == nil {
		t.Fatal("expected unique canonical/endpoint error")
	}
	if err := store.AddMessageCopy(ctx, MessageCopy{CanonicalID: "canon-b", EndpointID: "c1g1", RemoteMessageID: "ABC123", CreatedAt: now}); err == nil {
		t.Fatal("expected unique endpoint/remote id error")
	}
	if err := store.AddMessageCopy(ctx, MessageCopy{CanonicalID: "canon-a", EndpointID: "c1g2", RemoteMessageID: "XYZ789", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	canonical, err := store.CanonicalForRemote(ctx, "c1g1", "ABC123")
	if err != nil || canonical != "canon-a" {
		t.Fatalf("CanonicalForRemote() = %q, %v", canonical, err)
	}
	copy, err := store.MessageCopyForEndpoint(ctx, "canon-a", "c1g2")
	if err != nil || copy.RemoteMessageID != "XYZ789" {
		t.Fatalf("MessageCopyForEndpoint() = %+v, %v", copy, err)
	}
}

func TestForeignKeyAndSafeEndpointConstraints(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	err := store.AddMessageCopy(ctx, MessageCopy{CanonicalID: "missing", EndpointID: "c1g1", RemoteMessageID: "ABC"})
	if err == nil {
		t.Fatal("expected foreign key error")
	}
	if err := store.CreateCanonical(ctx, "canon", time.Now()); err != nil {
		t.Fatal(err)
	}
	err = store.AddMessageCopy(ctx, MessageCopy{CanonicalID: "canon", EndpointID: "15551234567@s.whatsapp.net", RemoteMessageID: "ABC"})
	if err == nil {
		t.Fatal("expected raw JID endpoint rejection")
	}
}

func TestReactionAndRecoveryRepositories(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	if err := store.CreateCanonical(ctx, "canon", now); err != nil {
		t.Fatal(err)
	}
	reaction := Reaction{CanonicalID: "canon", SourceEndpointID: "c1g1", ActorHash: "u_abcdefghij", Emoji: "👍", UpdatedAt: now}
	if err := store.UpsertReaction(ctx, reaction); err != nil {
		t.Fatal(err)
	}
	reaction.Emoji = "❤️"
	if err := store.UpsertReaction(ctx, reaction); err != nil {
		t.Fatal(err)
	}
	var emoji string
	if err := store.db.QueryRow(`SELECT emoji FROM reactions WHERE canonical_id='canon'`).Scan(&emoji); err != nil || emoji != "❤️" {
		t.Fatalf("emoji = %q, err = %v", emoji, err)
	}
	if err := store.DeleteReaction(ctx, "canon", "c1g1", "u_abcdefghij"); err != nil {
		t.Fatal(err)
	}

	cursor := RecoveryCursor{EndpointID: "c1g1", RemoteMessageID: "ABC", MessageTimestamp: now, UpdatedAt: now.Add(time.Minute)}
	if err := store.PutRecoveryCursor(ctx, cursor); err != nil {
		t.Fatal(err)
	}
	got, err := store.RecoveryCursor(ctx, "c1g1")
	if err != nil || got.RemoteMessageID != cursor.RemoteMessageID || !got.MessageTimestamp.Equal(now) {
		t.Fatalf("RecoveryCursor() = %+v, %v", got, err)
	}
}

func TestSyncSchemaHasNoPIIContentColumns(t *testing.T) {
	store, path := openTestStore(t)
	forbidden := []string{"phone", "name", "body", "caption", "media", "filename", "url"}
	routingTables := []string{"canonical_messages", "message_copies", "reactions", "recovery_cursors"}
	for _, table := range routingTables {
		cols, err := store.db.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			t.Fatal(err)
		}
		for cols.Next() {
			var cid, notNull, pk int
			var name, typ string
			var defaultValue any
			if err := cols.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
				t.Fatal(err)
			}
			lower := strings.ToLower(name)
			if lower == "jid" {
				t.Fatalf("participant JID column in routing table %q", table)
			}
			for _, token := range forbidden {
				if strings.Contains(lower, token) {
					t.Fatalf("PII/content-shaped column %q in routing table %q", name, table)
				}
			}
		}
		cols.Close()
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbiddenValue := range []string{"15551234567@s.whatsapp.net", "Alice Example", "secret message body"} {
		if strings.Contains(string(data), forbiddenValue) {
			t.Fatalf("sync.db contains forbidden value %q", forbiddenValue)
		}
	}
}

func TestMissingLookupPreservesSQLNotFound(t *testing.T) {
	store, _ := openTestStore(t)
	_, err := store.CanonicalForRemote(context.Background(), "c1g1", "missing")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("error = %v, want sql.ErrNoRows", err)
	}
}

func TestPollOptionsAndVotes(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	canonicalID := "canon-poll-1"

	if err := store.CreateCanonical(ctx, canonicalID, now); err != nil {
		t.Fatal(err)
	}

	isPoll, err := store.IsPoll(ctx, canonicalID)
	if err != nil {
		t.Fatal(err)
	}
	if isPoll {
		t.Fatal("expected false before saving options")
	}

	options := []string{"hash1", "hash2", "hash3"}
	if err := store.SavePollOptions(ctx, canonicalID, options); err != nil {
		t.Fatal(err)
	}

	isPoll, err = store.IsPoll(ctx, canonicalID)
	if err != nil {
		t.Fatal(err)
	}
	if !isPoll {
		t.Fatal("expected true after saving options")
	}

	retrieved, err := store.GetPollOptions(ctx, canonicalID)
	if err != nil {
		t.Fatal(err)
	}
	if len(retrieved) != 3 || retrieved[0] != "hash1" || retrieved[1] != "hash2" || retrieved[2] != "hash3" {
		t.Fatalf("unexpected options: %+v", retrieved)
	}

	actor1 := "u_abcdefghij"
	actor2 := "u_klmnopqrst"

	// Actor 1 votes for hash1
	if err := store.RecordPollVote(ctx, canonicalID, "endpoint1", actor1, []string{"hash1"}, now); err != nil {
		t.Fatal(err)
	}

	// Actor 2 votes for hash1 and hash2
	if err := store.RecordPollVote(ctx, canonicalID, "endpoint2", actor2, []string{"hash1", "hash2"}, now); err != nil {
		t.Fatal(err)
	}

	counts, err := store.GetPollVoteCounts(ctx, canonicalID)
	if err != nil {
		t.Fatal(err)
	}
	if counts["hash1"] != 2 || counts["hash2"] != 1 || counts["hash3"] != 0 {
		t.Fatalf("unexpected counts: %+v", counts)
	}

	// Actor 1 changes vote to hash3
	if err := store.RecordPollVote(ctx, canonicalID, "endpoint1", actor1, []string{"hash3"}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	counts, err = store.GetPollVoteCounts(ctx, canonicalID)
	if err != nil {
		t.Fatal(err)
	}
	if counts["hash1"] != 1 || counts["hash2"] != 1 || counts["hash3"] != 1 {
		t.Fatalf("unexpected updated counts: %+v", counts)
	}

	// Actor 2 removes all votes
	if err := store.RecordPollVote(ctx, canonicalID, "endpoint2", actor2, nil, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}

	counts, err = store.GetPollVoteCounts(ctx, canonicalID)
	if err != nil {
		t.Fatal(err)
	}
	if counts["hash1"] != 0 || counts["hash2"] != 0 || counts["hash3"] != 1 {
		t.Fatalf("unexpected counts after actor 2 cleared: %+v", counts)
	}
}

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

func TestSuppressedReactions(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()

	endpointID := "c1g1"
	remoteID := "msg-123"
	emoji := "👍"
	now := time.Now().UTC()

	// Check non-existent
	found, err := store.CheckAndClearSuppressedReaction(ctx, endpointID, remoteID, emoji)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected no suppressed reaction initially")
	}

	// Record
	if err := store.RecordSuppressedReaction(ctx, endpointID, remoteID, emoji, now); err != nil {
		t.Fatal(err)
	}

	// Check and clear
	found, err = store.CheckAndClearSuppressedReaction(ctx, endpointID, remoteID, emoji)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected suppressed reaction to be found")
	}

	// Check again (should be cleared)
	found, err = store.CheckAndClearSuppressedReaction(ctx, endpointID, remoteID, emoji)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected suppressed reaction to be cleared")
	}
}
