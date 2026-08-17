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

func TestOpenMigratesSchemaAndEnablesForeignKeys(t *testing.T) {
	store, _ := openTestStore(t)
	var enabled int
	if err := store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled != 1 {
		t.Fatalf("foreign_keys = %d, want 1", enabled)
	}
	var version int
	if err := store.db.QueryRow(`SELECT CAST(value AS INTEGER) FROM schema_meta WHERE key='schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != SchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, SchemaVersion)
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
	forbidden := []string{"jid", "phone", "name", "body", "caption", "media", "filename", "url"}
	rows, err := store.db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	rows.Close()
	for _, table := range tables {
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
			for _, token := range forbidden {
				if strings.Contains(lower, token) {
					t.Fatalf("PII/content-shaped column %q in table %q", name, table)
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
