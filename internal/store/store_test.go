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

func TestCanonicalScopesArePerEndpointAndIdempotent(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.CreateCanonical(ctx, "canon-scope", time.Unix(1700000000, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []CanonicalScope{
		{CanonicalID: "canon-scope", EndpointID: "endpoint1", ScopeKind: "discord_thread", RemoteScopeID: "thread-1"},
		{CanonicalID: "canon-scope", EndpointID: "endpoint2", ScopeKind: "telegram_topic", RemoteScopeID: "topic-2"},
		{CanonicalID: "canon-scope", EndpointID: "endpoint1", ScopeKind: "discord_thread", RemoteScopeID: "thread-1-replayed"},
	} {
		if err := store.UpsertCanonicalScope(ctx, scope); err != nil {
			t.Fatal(err)
		}
	}
	scopes, err := store.CanonicalScopes(ctx, "canon-scope")
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 2 {
		t.Fatalf("scope count = %d, want 2", len(scopes))
	}
	scope, err := store.CanonicalScope(ctx, "canon-scope", "endpoint1")
	if err != nil {
		t.Fatal(err)
	}
	if scope.RemoteScopeID != "thread-1-replayed" {
		t.Fatalf("endpoint1 scope = %q, want replayed scope", scope.RemoteScopeID)
	}
	rows, err := store.db.Query(`SELECT canonical_id, endpoint_id, remote_scope_id FROM canonical_scopes`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var canonical, endpoint, scopeID string
		if err := rows.Scan(&canonical, &endpoint, &scopeID); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(canonical+endpoint+scopeID, "body") {
			t.Fatal("scope table unexpectedly contains content")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestChildScopeLabelsAreEndpointScopedAndCascading(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if _, err := store.db.Exec(`INSERT INTO sync_sets(id) VALUES ('mesh'); INSERT INTO endpoints(alias, transport, connection_id, remote_id, sync_set_id) VALUES ('e1','discord','dc-1','1','mesh'),('e2','telegram','tg-1','-1001','mesh')`); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertChildScopeLabel(ctx, "e1", "discord_thread", "same", "  Dinner\n Plans  "); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertChildScopeLabel(ctx, "e2", "telegram_topic", "same", "Travel"); err != nil {
		t.Fatal(err)
	}
	got, err := store.ChildScopeLabel(ctx, "e1", "discord_thread", "same")
	if err != nil || got.DisplayName != "Dinner Plans" {
		t.Fatalf("label = %+v, err=%v", got, err)
	}
	if _, err := store.ChildScopeLabel(ctx, "e1", "telegram_topic", "same"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("scope kinds collided: %v", err)
	}
	if err := store.UpsertChildScopeLabel(ctx, "e1", "discord_thread", "same", "Renamed"); err != nil {
		t.Fatal(err)
	}
	got, err = store.ChildScopeLabel(ctx, "e1", "discord_thread", "same")
	if err != nil || got.DisplayName != "Renamed" {
		t.Fatalf("updated label = %+v, err=%v", got, err)
	}
	if _, err := store.db.Exec(`DELETE FROM endpoints WHERE alias='e1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ChildScopeLabel(ctx, "e1", "discord_thread", "same"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("endpoint deletion left label: %v", err)
	}
}

func TestDeliveryLedgerSchemaIsContentFree(t *testing.T) {
	store, _ := openTestStore(t)

	rows, err := store.db.Query(`PRAGMA table_info(delivery_operations)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
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
	want := map[string]bool{
		"canonical_id": true, "endpoint_id": true, "operation_kind": true,
		"operation_revision": true, "state": true, "attempt_count": true,
		"next_attempt_at": true, "failure_class": true, "created_at": true,
		"updated_at": true,
	}
	if len(columns) != len(want) {
		t.Fatalf("delivery ledger columns = %v, want exactly %v", columns, want)
	}
	for name := range want {
		if !columns[name] {
			t.Fatalf("delivery ledger missing column %q", name)
		}
	}
	for name := range columns {
		lower := strings.ToLower(name)
		for _, forbidden := range []string{"body", "text", "media", "phone", "jid", "name", "token", "secret", "error", "url"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("content, identity, secret, or raw error column %q", name)
			}
		}
	}
}

func TestDeliveryLedgerUpsertIsIdempotent(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	operation := DeliveryOperation{
		CanonicalID: "canon-delivery", EndpointID: "endpoint1", OperationKind: "send", OperationRevision: 1,
		State: DeliveryQueued, AttemptCount: 0, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateCanonical(ctx, operation.CanonicalID, now); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDeliveryOperation(ctx, operation); err != nil {
		t.Fatal(err)
	}
	operation.State = DeliveryFailed
	operation.AttemptCount = 4
	operation.FailureClass = "transient"
	if err := store.UpsertDeliveryOperation(ctx, operation); err != nil {
		t.Fatal(err)
	}
	var count int
	var state string
	var attempts int
	if err := store.db.QueryRow(`SELECT COUNT(*), MAX(state), MAX(attempt_count) FROM delivery_operations`).Scan(&count, &state, &attempts); err != nil {
		t.Fatal(err)
	}
	if count != 1 || state != DeliveryQueued || attempts != 0 {
		t.Fatalf("ledger row = count %d, state %q, attempts %d; want one unchanged queued row", count, state, attempts)
	}
}

func TestDeliveryLedgerRestartAwaitsReplayOnlyActiveWork(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sync.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	for i, state := range []string{DeliveryQueued, DeliveryRetrying, DeliveryFailed} {
		canonicalID := "canon-restart-" + string(rune('a'+i))
		if err := store.CreateCanonical(ctx, canonicalID, now); err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertDeliveryOperation(ctx, DeliveryOperation{
			CanonicalID: canonicalID, EndpointID: "endpoint1", OperationKind: "send", OperationRevision: 1,
			State: state, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.db.Query(`SELECT canonical_id, state FROM delivery_operations ORDER BY canonical_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := make(map[string]string)
	for rows.Next() {
		var canonicalID, state string
		if err := rows.Scan(&canonicalID, &state); err != nil {
			t.Fatal(err)
		}
		got[canonicalID] = state
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got["canon-restart-a"] != DeliveryAwaitingReplay || got["canon-restart-b"] != DeliveryAwaitingReplay || got["canon-restart-c"] != DeliveryFailed {
		t.Fatalf("restart states = %v", got)
	}
}

func TestDeliveryLedgerStateAndSummaryLifecycle(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	operation := DeliveryOperation{
		CanonicalID: "canon-lifecycle", EndpointID: "endpoint1", OperationKind: "send", OperationRevision: 1,
		State: DeliveryQueued, CreatedAt: now.Add(-10 * time.Minute), UpdatedAt: now.Add(-10 * time.Minute),
	}
	if err := store.CreateCanonical(ctx, operation.CanonicalID, now); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDeliveryOperation(ctx, operation); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimDeliveryOperation(ctx, operation, now.Add(-5*time.Minute))
	if err != nil || !claimed {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	if err := store.MarkDeliveryOperationFailed(ctx, operation, "permission_denied", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteDeliveryOperation(ctx, operation); err != nil {
		t.Fatal(err)
	}

	for i, state := range []string{DeliveryQueued, DeliveryRetrying, DeliveryAwaitingReplay, DeliveryFailed} {
		canonicalID := "canon-summary-" + string(rune('a'+i))
		if err := store.CreateCanonical(ctx, canonicalID, now); err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertDeliveryOperation(ctx, DeliveryOperation{
			CanonicalID: canonicalID, EndpointID: "endpoint1", OperationKind: "send", OperationRevision: 1,
			State: state, CreatedAt: now, UpdatedAt: now.Add(-time.Duration(i+1) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	summaries, err := store.DeliverySummaries(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	summary := summaries["endpoint1"]
	if summary.Queued != 1 || summary.Retrying != 1 || summary.AwaitingReplay != 1 || summary.Failed != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.OldestActiveAge != 3*time.Minute {
		t.Fatalf("oldest active age = %s, want 3m", summary.OldestActiveAge)
	}
}

func TestEndpointSchemaEnforcesAliasAndTransportRemoteUniqueness(t *testing.T) {
	store, _ := openTestStore(t)

	if _, err := store.db.Exec(`INSERT INTO sync_sets(id) VALUES ('mesh')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO endpoints(alias, transport, connection_id, remote_id, sync_set_id) VALUES ('a', 'whatsapp', 'conn-wa-1', '1@g.us', 'mesh')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO endpoints(alias, transport, connection_id, remote_id, sync_set_id) VALUES ('a', 'discord', 'conn-dc-1', '123', 'mesh')`); err == nil {
		t.Fatal("expected duplicate alias to be rejected")
	}
	if _, err := store.db.Exec(`INSERT INTO endpoints(alias, transport, connection_id, remote_id, sync_set_id) VALUES ('b', 'whatsapp', 'conn-wa-1', '1@g.us', 'mesh')`); err == nil {
		t.Fatal("expected duplicate WhatsApp remote target to be rejected")
	}
	if _, err := store.db.Exec(`INSERT INTO endpoints(alias, transport, connection_id, remote_id, sync_set_id) VALUES ('b', 'discord', 'conn-dc-1', '1@g.us', 'mesh')`); err != nil {
		t.Fatalf("same opaque remote id on another transport should be allowed: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO endpoints(alias, transport, connection_id, remote_id, sync_set_id) VALUES ('t', 'telegram', 'conn-tg-1', '-1001234567890', 'mesh')`); err != nil {
		t.Fatalf("Telegram endpoint should be allowed by schema: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO endpoints(alias, transport, connection_id, remote_id, sync_set_id) VALUES ('x', 'unknown', 'conn-x', 'opaque', 'mesh')`); err == nil {
		t.Fatal("expected unknown transport to be rejected by schema")
	}
	// Missing connection_id is rejected by NOT NULL constraint
	if _, err := store.db.Exec(`INSERT INTO endpoints(alias, transport, remote_id, sync_set_id) VALUES ('c', 'whatsapp', '3@g.us', 'mesh')`); err == nil {
		t.Fatal("expected missing connection_id to be rejected by NOT NULL constraint")
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

	cursor := RecoveryCursor{StreamKey: "telegram", Position: 42, EventTimestamp: now, UpdatedAt: now.Add(time.Minute)}
	if err := store.PutRecoveryCursor(ctx, cursor); err != nil {
		t.Fatal(err)
	}
	got, err := store.RecoveryCursor(ctx, "telegram")
	if err != nil || got.Position != cursor.Position || !got.EventTimestamp.Equal(now) {
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

func TestProviderNeutralPollStateUsesOneEndpointContribution(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	if err := store.CreateCanonical(ctx, "canon-poll-state", now); err != nil {
		t.Fatal(err)
	}
	if err := store.SavePollOptionMetadata(ctx, "canon-poll-state", []PollOption{
		{Index: 0, WhatsAppHash: "wa-zero"}, {Index: 1}, {Index: 2, WhatsAppHash: "wa-two"},
	}); err != nil {
		t.Fatal(err)
	}
	index, err := store.PollOptionIndexForWhatsAppHash(ctx, "canon-poll-state", "wa-two")
	if err != nil || index != 2 {
		t.Fatalf("WhatsApp hash index = %d, %v; want 2", index, err)
	}
	if err := store.ReplacePollActorSelections(ctx, "canon-poll-state", "endpoint1", "u_abcdefghij", []int{0, 2}, now); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplacePollEndpointSnapshot(ctx, "canon-poll-state", "endpoint2", map[int]int{0: 4, 1: 3}, now); err != nil {
		t.Fatal(err)
	}
	counts, err := store.GetPollAggregateCounts(ctx, "canon-poll-state")
	if err != nil {
		t.Fatal(err)
	}
	if counts[0] != 5 || counts[1] != 3 || counts[2] != 1 {
		t.Fatalf("aggregate counts = %#v, want option 0=5, 1=3, 2=1", counts)
	}
	// Replacing the endpoint contribution changes its authoritative path and
	// must not leave the old snapshot contributing as well.
	if err := store.ReplacePollActorSelections(ctx, "canon-poll-state", "endpoint2", "u_klmnopqrst", []int{1}, now); err != nil {
		t.Fatal(err)
	}
	counts, err = store.GetPollAggregateCounts(ctx, "canon-poll-state")
	if err != nil {
		t.Fatal(err)
	}
	if counts[0] != 1 || counts[1] != 1 || counts[2] != 1 {
		t.Fatalf("exclusive aggregate counts = %#v, want option 0=1, 1=1, 2=1", counts)
	}
	if err := store.MarkPollEndpointUnavailable(ctx, "canon-poll-state", "endpoint3"); err != nil {
		t.Fatal(err)
	}
	partial, err := store.HasUnavailablePollEndpoint(ctx, "canon-poll-state")
	if err != nil || !partial {
		t.Fatalf("unavailable endpoint marker = %v, %v", partial, err)
	}
}

func TestPollProviderReferenceIsOpaqueAndRestartSafe(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.CreateCanonical(ctx, "canon-poll-ref", time.Now()); err != nil {
		t.Fatal(err)
	}
	want := PollProviderRef{CanonicalID: "canon-poll-ref", EndpointID: "endpoint1", Provider: "telegram", Reference: "opaque-poll-123"}
	if err := store.SavePollProviderRef(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := store.PollCanonicalForProviderRef(ctx, want.EndpointID, want.Provider, want.Reference)
	if err != nil || got != want.CanonicalID {
		t.Fatalf("resolved canonical = %q, %v; want %q", got, err, want.CanonicalID)
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
