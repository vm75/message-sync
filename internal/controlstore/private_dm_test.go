package controlstore

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/transport"
)

func TestPrivateDMControlUpgradePreservesExistingAccounts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	old, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.DB().Exec(`INSERT INTO users(id,username,password_hash,role,active,created_at,updated_at) VALUES('existing','existing','hash','admin',1,1,1)`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"private_dm_reply_mappings", "private_dm_seen_messages", "private_dm_cleanup_candidates", "private_dm_inboxes"} {
		if _, err := raw.Exec(`DROP TABLE ` + table); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()
	for attempt := 0; attempt < 2; attempt++ {
		db, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		var count int
		if err := db.DB().QueryRow(`SELECT COUNT(*) FROM users WHERE id='existing'`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("account lost during upgrade: %d %v", count, err)
		}
		for _, table := range []string{"private_dm_reply_mappings", "private_dm_seen_messages", "private_dm_cleanup_candidates", "private_dm_inboxes"} {
			if err := db.DB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
				t.Fatalf("private schema not initialized empty: %s %v", table, err)
			}
		}
		db.Close()
	}
}

func TestPrivateDMMappingEncryptedBoundToInboxAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	secret := []byte("01234567890123456789012345678901")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	state, err := NewPrivateDMStore(db.DB(), secret)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	inbox := transport.PrivateInbox{ConnectionID: "conn-one", Transport: "discord", ChatID: "sensitive-private-chat-id", Token: []byte("sensitive-private-chat-id")}
	mapping := PrivateDMMapping{ConversationID: "opaque", SyncSetID: "mesh", Author: PrivateDMSide{Endpoint: "group-a", ActorID: "u_abcdefghij", Inbox: transport.PrivateInbox{ConnectionID: "conn-two", Transport: "whatsapp", Token: []byte("sensitive-user@s.whatsapp.net")}}, Recipient: PrivateDMSide{Endpoint: "group-b", ActorID: "u_0123456789", Inbox: inbox}}
	if err := state.PutReply(ctx, "sensitive-native-message", mapping, now); err != nil {
		t.Fatal(err)
	}
	if err := state.ObserveInbox(ctx, "u_0123456789", inbox, now); err != nil {
		t.Fatal(err)
	}
	var cipher, nonce []byte
	var lookup string
	if err := db.DB().QueryRow(`SELECT lookup_key,ciphertext,nonce FROM private_dm_reply_mappings`).Scan(&lookup, &cipher, &nonce); err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{"sensitive-private-chat-id", "sensitive-user@s.whatsapp.net", "sensitive-native-message", "group-a"} {
		if bytes.Contains(cipher, []byte(sensitive)) || bytes.Contains([]byte(lookup), []byte(sensitive)) {
			t.Fatal("plaintext routing data in persistent private mapping")
		}
	}
	if len(nonce) == 0 {
		t.Fatal("mapping was not encrypted")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	state, _ = NewPrivateDMStore(db.DB(), secret)
	if got, ok, err := state.Inbox(ctx, inbox.ConnectionID, inbox.Transport, "u_0123456789", now.Add(time.Hour)); err != nil || !ok || got.ChatID != inbox.ChatID {
		t.Fatal("encrypted operational inbox did not survive restart")
	}
	if _, ok, err := state.Inbox(ctx, inbox.ConnectionID, inbox.Transport, "u_0123456789", now.Add(24*time.Hour)); err != nil || ok {
		t.Fatal("operational inbox did not expire")
	}
	got, ok, err := state.Reply(ctx, inbox, "sensitive-native-message", now.Add(time.Hour))
	if err != nil || !ok || got.Author.Inbox.ConnectionID != "conn-two" {
		t.Fatalf("restart mapping failed: %v %v", ok, err)
	}
	alternate := inbox
	alternate.Token = []byte("verified-alternate-native-send-address")
	if _, ok, err := state.Reply(ctx, alternate, "sensitive-native-message", now); err != nil || !ok {
		t.Fatal("canonical chat lookup incorrectly depended on a reversible send address")
	}
	for _, wrong := range []transport.PrivateInbox{{ConnectionID: "other", Transport: inbox.Transport, Token: inbox.Token}, {ConnectionID: inbox.ConnectionID, Transport: inbox.Transport, ChatID: "other-chat", Token: []byte("other-chat")}, {ConnectionID: inbox.ConnectionID, Transport: "telegram", Token: inbox.Token}} {
		if _, ok, err := state.Reply(ctx, wrong, "sensitive-native-message", now); err != nil || ok {
			t.Fatal("mapping accepted a different inbox")
		}
	}
	if _, ok, err := state.Reply(ctx, inbox, "sensitive-native-message", now.Add(24*time.Hour)); err != nil || ok {
		t.Fatal("expired mapping accepted")
	}
	if err := state.Prune(ctx, now.Add(24*time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	var count int
	db.DB().QueryRow(`SELECT COUNT(*) FROM private_dm_reply_mappings`).Scan(&count)
	if count != 0 {
		t.Fatal("expired mappings retained")
	}
}

func TestPrivateCleanupBoundedAndUnsupportedProvidersNotQueued(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	state, _ := NewPrivateDMStore(db.DB(), []byte("01234567890123456789012345678901"))
	now := time.Now().UTC()
	for _, provider := range []string{"whatsapp", "discord", "telegram"} {
		if err := state.QueueCleanup(ctx, transport.PrivateCopy{ConnectionID: "owner", Transport: provider, Token: []byte("private-message-key")}, now, now); err != nil {
			t.Fatal(err)
		}
	}
	for attempt := 0; attempt < 3; attempt++ {
		due, err := state.DueCleanup(ctx, now, 100)
		if err != nil || len(due) != 1 || due[0].Attempts != attempt || due[0].Copy.Transport != "whatsapp" {
			t.Fatalf("attempt %d: %+v %v", attempt, due, err)
		}
		if claimed, err := state.BeginCleanup(ctx, due[0], now); err != nil || !claimed {
			t.Fatal("cleanup was not durably claimed")
		}
		if err := state.FinishCleanup(ctx, due[0], false); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Hour)
	}
	due, err := state.DueCleanup(ctx, now, 100)
	if err != nil || len(due) != 0 {
		t.Fatal("unbounded failed cleanup retries")
	}
}

func TestPrivateCleanupCrashesCannotResetAttempts(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	state, _ := NewPrivateDMStore(db.DB(), []byte("01234567890123456789012345678901"))
	now := time.Now()
	if err := state.QueueCleanup(ctx, transport.PrivateCopy{ConnectionID: "owner", Transport: "whatsapp", Token: []byte("feature-private-copy")}, now, now); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		due, err := state.DueCleanup(ctx, now, 100)
		if err != nil || len(due) != 1 || due[0].Attempts != attempt {
			t.Fatal("durable cleanup attempt state lost")
		}
		if claimed, err := state.BeginCleanup(ctx, due[0], now); err != nil || !claimed {
			t.Fatal("cleanup claim failed")
		}
		// Simulate a process crash or context timeout before FinishCleanup.
		now = now.Add(time.Hour)
	}
	if due, err := state.DueCleanup(ctx, now, 100); err != nil || len(due) != 0 {
		t.Fatal("crash caused an unbounded retry")
	}
	if err := state.Prune(ctx, now.Add(72*time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	var count int
	db.DB().QueryRow(`SELECT COUNT(*) FROM private_dm_cleanup_candidates`).Scan(&count)
	if count != 0 {
		t.Fatal("abandoned cleanup metadata did not expire")
	}
}
