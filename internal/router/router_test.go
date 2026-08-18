package router

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/transport"
)

type sentMessage struct {
	outgoing transport.Outgoing
	ref      transport.MessageRef
}

type fakeSender struct {
	sent   []sentMessage
	edited []struct {
		ref  transport.MessageRef
		text string
	}
	deleted []transport.MessageRef
	reacted []transport.Reaction
	next    map[transport.EndpointID]int
}

func (f *fakeSender) Send(_ context.Context, outgoing transport.Outgoing) (transport.MessageRef, error) {
	if f.next == nil {
		f.next = make(map[transport.EndpointID]int)
	}
	f.next[outgoing.Endpoint]++
	ref := transport.MessageRef{
		Endpoint:        outgoing.Endpoint,
		RemoteMessageID: string(outgoing.Endpoint) + "-sent-" + string(rune('0'+f.next[outgoing.Endpoint])),
	}
	f.sent = append(f.sent, sentMessage{outgoing: outgoing, ref: ref})
	return ref, nil
}

func (f *fakeSender) React(_ context.Context, r transport.Reaction) error {
	f.reacted = append(f.reacted, r)
	return nil
}

func (f *fakeSender) Edit(_ context.Context, ref transport.MessageRef, text string) error {
	f.edited = append(f.edited, struct {
		ref  transport.MessageRef
		text string
	}{ref: ref, text: text})
	return nil
}

func (f *fakeSender) Delete(_ context.Context, ref transport.MessageRef) error {
	f.deleted = append(f.deleted, ref)
	return nil
}

func TestTextFanoutUsesAliasAndPushName(t *testing.T) {
	ctx := context.Background()
	r, _, fake := newTestRouter(t, config.UsernameModePushName)

	incoming := testIncoming("c1g2", "source-1")
	incoming.Sender.DisplayName = "  Alice   Example  "
	if err := r.Handle(ctx, incoming); err != nil {
		t.Fatal(err)
	}

	if len(fake.sent) != 2 {
		t.Fatalf("sent %d messages, want 2", len(fake.sent))
	}
	gotEndpoints := []transport.EndpointID{fake.sent[0].outgoing.Endpoint, fake.sent[1].outgoing.Endpoint}
	wantEndpoints := []transport.EndpointID{"c1g1", "c1g3"}
	if !reflect.DeepEqual(gotEndpoints, wantEndpoints) {
		t.Fatalf("destinations = %v, want %v", gotEndpoints, wantEndpoints)
	}
	for _, sent := range fake.sent {
		if sent.outgoing.Text != "c1g2/15551234567 (Alice Example): hello" {
			t.Fatalf("forwarded text = %q", sent.outgoing.Text)
		}
		if strings.Contains(sent.outgoing.Text, "@g.us") {
			t.Fatalf("forwarded attribution exposed a JID: %q", sent.outgoing.Text)
		}
	}
}

func TestTextFanoutFallsBackToPhoneNumberWhenPushNameEmpty(t *testing.T) {
	ctx := context.Background()
	r, _, fake := newTestRouter(t, config.UsernameModePushName)

	incoming := testIncoming("c1g2", "source-1")
	incoming.Sender.DisplayName = "   " // Empty after trim
	if err := r.Handle(ctx, incoming); err != nil {
		t.Fatal(err)
	}

	if len(fake.sent) != 2 {
		t.Fatalf("sent %d messages, want 2", len(fake.sent))
	}
	for _, sent := range fake.sent {
		if sent.outgoing.Text != "c1g2/15551234567: hello" {
			t.Fatalf("forwarded text = %q", sent.outgoing.Text)
		}
	}
}

func TestHashAttributionNeverUsesPushName(t *testing.T) {
	ctx := context.Background()
	r, _, fake := newTestRouter(t, config.UsernameModeHash)
	incoming := testIncoming("c1g1", "source-1")
	incoming.Sender.DisplayName = "Alice Example"

	if err := r.Handle(ctx, incoming); err != nil {
		t.Fatal(err)
	}
	for _, sent := range fake.sent {
		if sent.outgoing.Text != "c1g1/u_abcdefghij: hello" {
			t.Fatalf("forwarded text = %q", sent.outgoing.Text)
		}
	}
}

func TestNewMessageFromBridgeAccountStillFansOut(t *testing.T) {
	ctx := context.Background()
	r, _, fake := newTestRouter(t, config.UsernameModeHash)
	incoming := testIncoming("c1g1", "manual-self-message")
	incoming.FromSelf = true

	if err := r.Handle(ctx, incoming); err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) != 2 {
		t.Fatalf("new self-origin message produced %d sends, want 2", len(fake.sent))
	}
}

func TestDuplicateAndBridgeEchoDoNotCreateCopies(t *testing.T) {
	ctx := context.Background()
	r, _, fake := newTestRouter(t, config.UsernameModePushName)
	incoming := testIncoming("c1g1", "source-1")

	if err := r.Handle(ctx, incoming); err != nil {
		t.Fatal(err)
	}
	if err := r.Handle(ctx, incoming); err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) != 2 {
		t.Fatalf("duplicate source produced %d sends, want 2 total", len(fake.sent))
	}

	echo := testIncoming(fake.sent[0].ref.Endpoint, fake.sent[0].ref.RemoteMessageID)
	echo.FromSelf = true
	echo.Text = fake.sent[0].outgoing.Text
	if err := r.Handle(ctx, echo); err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) != 2 {
		t.Fatalf("bridge echo produced extra sends: %d total", len(fake.sent))
	}
}

func TestCrashAfterPersistResumesOnlyMissingCopies(t *testing.T) {
	ctx := context.Background()
	r, syncStore, first := newTestRouter(t, config.UsernameModePushName)
	incoming := testIncoming("c1g1", "source-1")
	crash := errors.New("simulated crash")
	r.afterPersist = func(endpoint transport.EndpointID) error {
		if endpoint == "c1g2" {
			return crash
		}
		return nil
	}

	if err := r.Handle(ctx, incoming); !errors.Is(err, crash) {
		t.Fatalf("first handle error = %v, want simulated crash", err)
	}
	if len(first.sent) != 1 || first.sent[0].outgoing.Endpoint != "c1g2" {
		t.Fatalf("first run sends = %#v", first.sent)
	}

	second := &fakeSender{}
	restarted, err := New(testConfig("push_name"), syncStore, second)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Handle(ctx, incoming); err != nil {
		t.Fatal(err)
	}
	if len(second.sent) != 1 || second.sent[0].outgoing.Endpoint != "c1g3" {
		t.Fatalf("restart sends = %#v, want only c1g3", second.sent)
	}
}

func TestRestartUsesCanonicalMappingWithoutPersistingContentOrParticipant(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sync.db")
	syncStore, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	first := &fakeSender{}
	r, err := New(testConfig("push_name"), syncStore, first)
	if err != nil {
		t.Fatal(err)
	}
	incoming := testIncoming("c1g1", "source-privacy")
	incoming.Sender.DisplayName = "Privacy Sentinel Name"
	incoming.Text = "PRIVACY_SENTINEL_BODY"

	if err := r.Handle(ctx, incoming); err != nil {
		t.Fatal(err)
	}
	if len(first.sent) != 2 {
		t.Fatalf("first run sends = %d, want 2", len(first.sent))
	}
	if err := syncStore.Close(); err != nil {
		t.Fatal(err)
	}

	dbBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Privacy Sentinel Name", "PRIVACY_SENTINEL_BODY", "u_abcdefghij"} {
		if strings.Contains(string(dbBytes), forbidden) {
			t.Fatalf("sync.db persisted participant/content value %q", forbidden)
		}
	}

	reopened, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	second := &fakeSender{}
	restarted, err := New(testConfig("push_name"), reopened, second)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Handle(ctx, incoming); err != nil {
		t.Fatal(err)
	}
	if len(second.sent) != 0 {
		t.Fatalf("restart duplicated destination copies: %#v", second.sent)
	}
}

func TestEditPropagationToDestinationCopies(t *testing.T) {
	ctx := context.Background()
	r, syncStore, fake := newTestRouter(t, config.UsernameModePushName)

	// 1. Send original text message
	orig := testIncoming("c1g1", "orig-msg-1")
	orig.Text = "original message text"
	if err := r.Handle(ctx, orig); err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) != 2 {
		t.Fatalf("sent %d messages, want 2", len(fake.sent))
	}

	// 2. Send edit event
	editEvt := transport.Incoming{
		Endpoint: "c1g1",
		RemoteID: "edit-event-id",
		Sender: transport.Sender{
			DisplayName: "Alice",
			OpaqueID:    "u_abcdefghij",
		},
		Kind: "edit",
		Text: "edited message text",
		ReplyTo: &transport.MessageRef{
			Endpoint:        "c1g1",
			RemoteMessageID: "orig-msg-1",
		},
		Timestamp: time.Unix(1_700_000_100, 0).UTC(),
	}
	if err := r.Handle(ctx, editEvt); err != nil {
		t.Fatal(err)
	}

	if len(fake.edited) != 2 {
		t.Fatalf("edited %d messages, want 2", len(fake.edited))
	}
	for _, ed := range fake.edited {
		if !strings.Contains(ed.text, "edited message text") {
			t.Fatalf("unexpected edit text: %s", ed.text)
		}
	}

	// 3. Redelivery is idempotent
	if err := r.Handle(ctx, editEvt); err != nil {
		t.Fatal(err)
	}

	// 4. Verify recovery cursor updated
	cursor, err := syncStore.RecoveryCursor(ctx, "c1g1")
	if err != nil {
		t.Fatal(err)
	}
	if cursor.RemoteMessageID != "edit-event-id" {
		t.Fatalf("recovery cursor remoteID = %q, want edit-event-id", cursor.RemoteMessageID)
	}
}

func TestDeletePropagationAndTombstonePreventsResurrection(t *testing.T) {
	ctx := context.Background()
	r, syncStore, fake := newTestRouter(t, config.UsernameModePushName)

	// 1. Send original text message
	orig := testIncoming("c1g1", "orig-msg-2")
	orig.Text = "will be deleted"
	if err := r.Handle(ctx, orig); err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) != 2 {
		t.Fatalf("sent %d messages, want 2", len(fake.sent))
	}

	// 2. Send delete event
	delEvt := transport.Incoming{
		Endpoint: "c1g1",
		RemoteID: "del-event-id",
		Sender: transport.Sender{
			DisplayName: "Alice",
			OpaqueID:    "u_abcdefghij",
		},
		Kind: "delete",
		ReplyTo: &transport.MessageRef{
			Endpoint:        "c1g1",
			RemoteMessageID: "orig-msg-2",
		},
		Timestamp: time.Unix(1_700_000_200, 0).UTC(),
	}
	if err := r.Handle(ctx, delEvt); err != nil {
		t.Fatal(err)
	}

	if len(fake.deleted) != 2 {
		t.Fatalf("deleted %d messages, want 2", len(fake.deleted))
	}

	// 3. Verify canonical message is tombstoned in syncStore
	canonID, err := syncStore.CanonicalForRemote(ctx, "c1g1", "orig-msg-2")
	if err != nil {
		t.Fatal(err)
	}
	isTomb, err := syncStore.IsTombstoned(ctx, canonID)
	if err != nil || !isTomb {
		t.Fatalf("isTombstoned = %v, err = %v, want true", isTomb, err)
	}

	// 4. Attempting to edit a deleted message is ignored
	editAfterDel := transport.Incoming{
		Endpoint: "c1g1",
		RemoteID: "edit-after-del",
		Sender: transport.Sender{
			DisplayName: "Alice",
			OpaqueID:    "u_abcdefghij",
		},
		Kind: "edit",
		Text: "attempt edit after delete",
		ReplyTo: &transport.MessageRef{
			Endpoint:        "c1g1",
			RemoteMessageID: "orig-msg-2",
		},
	}
	fake.edited = nil
	if err := r.Handle(ctx, editAfterDel); err != nil {
		t.Fatal(err)
	}
	if len(fake.edited) != 0 {
		t.Fatalf("edited %d messages after delete, want 0", len(fake.edited))
	}

	// 5. Replaying / recovering the deleted message does not resurrect it
	sentBefore := len(fake.sent)
	if err := r.Handle(ctx, orig); err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) != sentBefore {
		t.Fatalf("deleted message was resurrected! sent count before=%d, after=%d", sentBefore, len(fake.sent))
	}
}

func TestReactionPropagationAndEchoSuppression(t *testing.T) {
	ctx := context.Background()
	r, syncStore, fake := newTestRouter(t, config.UsernameModePushName)

	// 1. Ingest original message in c1g1
	orig := testIncoming("c1g1", "orig-msg-reaction")
	orig.Text = "original message"
	if err := r.Handle(ctx, orig); err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) != 2 {
		t.Fatalf("sent %d messages, want 2", len(fake.sent))
	}

	// 2. Incoming reaction from another user in c1g1
	reaction := transport.Incoming{
		Endpoint: "c1g1",
		RemoteID: "reaction-event-1",
		Sender: transport.Sender{
			DisplayName: "Bob",
			OpaqueID:    "u_bcdefghijk",
		},
		Kind: "reaction",
		Text: "👍",
		ReplyTo: &transport.MessageRef{
			Endpoint:        "c1g1",
			RemoteMessageID: "orig-msg-reaction",
		},
		Timestamp: time.Unix(1_700_000_100, 0).UTC(),
	}
	if err := r.Handle(ctx, reaction); err != nil {
		t.Fatal(err)
	}

	if len(fake.reacted) != 2 {
		t.Fatalf("reacted %d times, want 2", len(fake.reacted))
	}
	for _, rct := range fake.reacted {
		if rct.Emoji != "👍" {
			t.Fatalf("reaction emoji = %q, want 👍", rct.Emoji)
		}
		if !rct.IsTargetFromMe {
			t.Fatalf("destination reaction IsTargetFromMe = false, want true")
		}
	}

	// 3. User reaction (FromSelf = true) originating from the paired bridge account on phone
	userReaction := transport.Incoming{
		Endpoint: "c1g1",
		RemoteID: "reaction-event-self",
		Sender: transport.Sender{
			DisplayName: "Myself",
			OpaqueID:    "u_cdefghijkl",
		},
		FromSelf: true,
		Kind:     "reaction",
		Text:     "❤️",
		ReplyTo: &transport.MessageRef{
			Endpoint:        "c1g1",
			RemoteMessageID: "orig-msg-reaction",
		},
		Timestamp: time.Unix(1_700_000_200, 0).UTC(),
	}
	fake.reacted = nil
	if err := r.Handle(ctx, userReaction); err != nil {
		t.Fatal(err)
	}
	if len(fake.reacted) != 2 {
		t.Fatalf("user self-reaction propagated %d times, want 2", len(fake.reacted))
	}

	// 4. Bridge echo arriving back for one of the destinations (c1g2) with FromSelf = true
	echoTargetID := fake.reacted[0].TargetRemoteID
	echoReaction := transport.Incoming{
		Endpoint: fake.reacted[0].Endpoint,
		RemoteID: "echo-reaction-event",
		FromSelf: true,
		Kind:     "reaction",
		Text:     "❤️",
		ReplyTo: &transport.MessageRef{
			Endpoint:        fake.reacted[0].Endpoint,
			RemoteMessageID: echoTargetID,
		},
		Timestamp: time.Unix(1_700_000_205, 0).UTC(),
	}
	fake.reacted = nil
	if err := r.Handle(ctx, echoReaction); err != nil {
		t.Fatal(err)
	}
	if len(fake.reacted) != 0 {
		t.Fatalf("reaction echo was not suppressed, produced %d reactions", len(fake.reacted))
	}

	// 5. Verify recovery cursor is maintained
	cursor, err := syncStore.RecoveryCursor(ctx, "c1g1")
	if err != nil {
		t.Fatal(err)
	}
	if cursor.RemoteMessageID != "reaction-event-self" {
		t.Fatalf("recovery cursor remoteID = %q, want reaction-event-self", cursor.RemoteMessageID)
	}
}

func TestNativeReplyDestinationTargetResolution(t *testing.T) {
	ctx := context.Background()
	r, _, fake := newTestRouter(t, config.UsernameModePushName)

	// 1. Send original text message in c1g1
	orig := testIncoming("c1g1", "orig-msg-reply")
	orig.Text = "original message to reply to"
	if err := r.Handle(ctx, orig); err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) != 2 {
		t.Fatalf("sent %d messages, want 2", len(fake.sent))
	}

	// 2. Incoming reply in c1g1 quoting the original message
	reply := transport.Incoming{
		Endpoint: "c1g1",
		RemoteID: "reply-msg-1",
		Sender: transport.Sender{
			DisplayName: "Bob",
			PhoneNumber: "15559876543",
			OpaqueID:    "u_bcdefghijk",
		},
		Kind: "text",
		Text: "reply to original",
		ReplyTo: &transport.MessageRef{
			Endpoint:        "c1g1",
			RemoteMessageID: "orig-msg-reply",
		},
		QuotedText: "original message to reply to",
		Timestamp:  time.Unix(1_700_000_300, 0).UTC(),
	}
	fake.sent = nil
	if err := r.Handle(ctx, reply); err != nil {
		t.Fatal(err)
	}

	if len(fake.sent) != 2 {
		t.Fatalf("reply sent %d destination copies, want 2", len(fake.sent))
	}
	for _, sent := range fake.sent {
		if sent.outgoing.ReplyTo == nil {
			t.Fatalf("outgoing reply in %s has nil ReplyTo", sent.outgoing.Endpoint)
		}
		if !sent.outgoing.ReplyTo.IsTargetFromMe {
			t.Fatalf("outgoing reply in %s has IsTargetFromMe = false, want true for destination copy", sent.outgoing.Endpoint)
		}
		if !strings.HasPrefix(sent.outgoing.ReplyTo.RemoteMessageID, string(sent.outgoing.Endpoint)+"-sent-") {
			t.Fatalf("outgoing reply in %s targeted wrong remote ID %s", sent.outgoing.Endpoint, sent.outgoing.ReplyTo.RemoteMessageID)
		}
	}
}

func newTestRouter(t *testing.T, usernameMode config.UsernameMode) (*Router, *store.Store, *fakeSender) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sync.db")
	syncStore, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syncStore.Close() })
	fake := &fakeSender{}
	r, err := New(testConfig(usernameMode), syncStore, fake)
	if err != nil {
		t.Fatal(err)
	}
	return r, syncStore, fake
}

func testConfig(usernameMode config.UsernameMode) *config.Config {
	return &config.Config{
		Groups: map[string]config.Group{
			"c1g1": {JID: "111@g.us"},
			"c1g2": {JID: "222@g.us"},
			"c1g3": {JID: "333@g.us"},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Groups: []string{"c1g1", "c1g2", "c1g3"}}},
		Identity: config.Identity{UsernameMode: usernameMode},
	}
}

func testIncoming(endpoint transport.EndpointID, remoteID string) transport.Incoming {
	return transport.Incoming{
		Endpoint: endpoint,
		RemoteID: remoteID,
		Sender: transport.Sender{
			DisplayName: "Alice",
			PhoneNumber: "15551234567",
			OpaqueID:    "u_abcdefghij",
		},
		Kind:      "text",
		Text:      "hello",
		Timestamp: time.Unix(1_700_000_000, 0).UTC(),
	}
}
