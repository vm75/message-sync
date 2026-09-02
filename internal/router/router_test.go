package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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
	mu     sync.Mutex
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
	f.mu.Lock()
	defer f.mu.Unlock()
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

func waitForSent(t *testing.T, f *fakeSender, want int) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		f.mu.Lock()
		got := len(f.sent)
		f.mu.Unlock()
		if got >= want {
			return
		}
		select {
		case <-deadline:
			f.mu.Lock()
			for _, sent := range f.sent {
				t.Logf("timeout sent endpoint=%s kind=%s", sent.outgoing.Endpoint, sent.outgoing.Kind)
			}
			f.mu.Unlock()
			t.Fatalf("sent %d messages, want at least %d", got, want)
		case <-time.After(time.Millisecond):
		}
	}
}

func waitForMutations(t *testing.T, f *fakeSender, edits, reactions, deletes int) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		f.mu.Lock()
		ready := len(f.edited) >= edits && len(f.reacted) >= reactions && len(f.deleted) >= deletes
		f.mu.Unlock()
		if ready {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("mutation counts did not reach edits=%d reactions=%d deletes=%d", edits, reactions, deletes)
		case <-time.After(time.Millisecond):
		}
	}
}

func (f *fakeSender) React(_ context.Context, r transport.Reaction) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reacted = append(f.reacted, r)
	return nil
}

func (f *fakeSender) Edit(_ context.Context, ref transport.MessageRef, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edited = append(f.edited, struct {
		ref  transport.MessageRef
		text string
	}{ref: ref, text: text})
	return nil
}

func (f *fakeSender) Delete(_ context.Context, ref transport.MessageRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	waitForSent(t, fake, 2)

	if len(fake.sent) != 2 {
		t.Fatalf("sent %d messages, want 2", len(fake.sent))
	}
	gotEndpoints := []transport.EndpointID{fake.sent[0].outgoing.Endpoint, fake.sent[1].outgoing.Endpoint}
	wantEndpoints := map[transport.EndpointID]bool{"c1g1": true, "c1g3": true}
	gotSet := map[transport.EndpointID]bool{gotEndpoints[0]: true, gotEndpoints[1]: true}
	if !reflect.DeepEqual(gotSet, wantEndpoints) {
		t.Fatalf("destinations = %v, want set %v", gotEndpoints, wantEndpoints)
	}
	for _, sent := range fake.sent {
		if sent.outgoing.Text != "*_c1g2/15551234567 (Alice Example)_*: hello" {
			t.Fatalf("forwarded text = %q", sent.outgoing.Text)
		}
		if sent.outgoing.SourceText != "hello" || sent.outgoing.Sender.DisplayName != "  Alice   Example  " {
			t.Fatalf("transient sender/source metadata was not preserved: %#v", sent.outgoing)
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
	waitForSent(t, fake, 2)

	if len(fake.sent) != 2 {
		t.Fatalf("sent %d messages, want 2", len(fake.sent))
	}
	for _, sent := range fake.sent {
		if sent.outgoing.Text != "*_c1g2/15551234567_*: hello" {
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
		if sent.outgoing.Text != "*_c1g1/u_abcdefghij_*: hello" {
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
	waitForSent(t, fake, 2)
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
	waitForSent(t, fake, 2)
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

	if err := r.Handle(ctx, incoming); err != nil {
		t.Fatalf("first handle error = %v", err)
	}
	waitForSent(t, first, 2)
	waitForSent(t, first, 2)
	if len(first.sent) != 2 {
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
	waitForSent(t, second, 0)
	if len(second.sent) != 0 {
		t.Fatalf("restart resent already-persisted copies = %#v", second.sent)
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
	incoming.Sender.DisplayName = "Discord Member Sentinel"
	incoming.Text = "PRIVACY_SENTINEL_BODY Discord Guild Sentinel"
	incoming.QuotedText = "Discord Channel Sentinel"
	incoming.Mentions = []transport.Mention{{
		RemoteID: "discord-transient-member-id",
		Name:     "Discord Mention Sentinel",
	}}
	incoming.Kind = "document"
	incoming.MediaLoader = func(context.Context) ([]byte, error) {
		return []byte("TELEGRAM_MEDIA_SENTINEL"), nil
	}

	if err := r.Handle(ctx, incoming); err != nil {
		t.Fatal(err)
	}
	waitForSent(t, first, 2)
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
	for _, forbidden := range []string{
		"Discord Member Sentinel",
		"Discord Guild Sentinel",
		"Discord Channel Sentinel",
		"Discord Mention Sentinel",
		"discord-transient-member-id",
		"PRIVACY_SENTINEL_BODY",
		"TELEGRAM_MEDIA_SENTINEL",
		"u_abcdefghij",
	} {
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
	r, _, fake := newTestRouter(t, config.UsernameModePushName)

	// 1. Send original text message
	orig := testIncoming("c1g1", "orig-msg-1")
	orig.Text = "original message text"
	if err := r.Handle(ctx, orig); err != nil {
		t.Fatal(err)
	}
	waitForSent(t, fake, 2)
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
	waitForMutations(t, fake, 2, 0, 0)

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
	waitForSent(t, fake, 2)
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
	waitForMutations(t, fake, 0, 0, 2)

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
	r, _, fake := newTestRouter(t, config.UsernameModePushName)

	// 1. Ingest original message in c1g1
	orig := testIncoming("c1g1", "orig-msg-reaction")
	orig.Text = "original message"
	if err := r.Handle(ctx, orig); err != nil {
		t.Fatal(err)
	}
	waitForSent(t, fake, 2)
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
	waitForMutations(t, fake, 0, 2, 0)

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
	waitForMutations(t, fake, 0, 2, 0)
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
	waitForSent(t, fake, 2)
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
	waitForSent(t, fake, 2)

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

func TestReplyFallbackFlagWhenDestinationCopyMissing(t *testing.T) {
	ctx := context.Background()
	r, _, fake := newTestRouter(t, config.UsernameModePushName)

	reply := testIncoming("c1g1", "reply-with-missing-target")
	reply.Sender.DisplayName = "Alice"
	reply.Text = "new reply body"
	reply.ReplyTo = &transport.MessageRef{
		Endpoint:        "c1g1",
		RemoteMessageID: "unknown-source-target",
	}
	reply.QuotedText = "quoted source body"

	if err := r.Handle(ctx, reply); err != nil {
		t.Fatal(err)
	}
	waitForSent(t, fake, 2)
	if len(fake.sent) != 2 {
		t.Fatalf("reply sent %d destination copies, want 2", len(fake.sent))
	}
	for _, sent := range fake.sent {
		if sent.outgoing.ReplyTo != nil {
			t.Fatalf("missing destination target unexpectedly resolved in %s", sent.outgoing.Endpoint)
		}
		if !sent.outgoing.ReplyFallback {
			t.Fatalf("missing destination target did not set ReplyFallback in %s", sent.outgoing.Endpoint)
		}
		if sent.outgoing.QuotedText != "quoted source body" {
			t.Fatalf("quoted fallback text = %q", sent.outgoing.QuotedText)
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
	t.Cleanup(r.Close)
	return r, syncStore, fake
}

func testConfig(usernameMode config.UsernameMode) *config.Config {
	return &config.Config{
		Endpoints: map[string]config.Endpoint{
			"c1g1": {Transport: config.TransportWhatsApp, RemoteID: "111@g.us"},
			"c1g2": {Transport: config.TransportWhatsApp, RemoteID: "222@g.us"},
			"c1g3": {Transport: config.TransportWhatsApp, RemoteID: "333@g.us"},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Endpoints: []string{"c1g1", "c1g2", "c1g3"}}},
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

func TestRouterUpdateConfig(t *testing.T) {
	r, _, fake := newTestRouter(t, config.UsernameModePushName)
	ctx := context.Background()

	// Initial route: c1g1 forwards to c1g2, c1g3
	inc := testIncoming("c1g1", "msg1")
	if err := r.Handle(ctx, inc); err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	waitForSent(t, fake, 2)
	if len(fake.sent) != 2 {
		t.Fatalf("expected 2 forwarded messages, got %d", len(fake.sent))
	}
	if !strings.Contains(fake.sent[0].outgoing.Text, "Alice") {
		t.Fatalf("expected push name in forwarded text, got %q", fake.sent[0].outgoing.Text)
	}

	// Update config to switch to hash mode and remove c1g3 from sync set
	newCfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"c1g1": {Transport: config.TransportWhatsApp, RemoteID: "111@g.us"},
			"c1g2": {Transport: config.TransportWhatsApp, RemoteID: "222@g.us"},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Endpoints: []string{"c1g1", "c1g2"}}},
		Identity: config.Identity{UsernameMode: config.UsernameModeHash},
	}
	if err := r.UpdateConfig(newCfg); err != nil {
		t.Fatalf("UpdateConfig error = %v", err)
	}

	fake.sent = nil
	inc2 := testIncoming("c1g1", "msg2")
	if err := r.Handle(ctx, inc2); err != nil {
		t.Fatalf("Handle error = %v", err)
	}
	waitForSent(t, fake, 1)
	// Now should only forward to c1g2 (1 message)
	if len(fake.sent) != 1 {
		t.Fatalf("expected 1 forwarded message after config update, got %d", len(fake.sent))
	}
	if fake.sent[0].outgoing.Endpoint != "c1g2" {
		t.Fatalf("expected forwarded to c1g2, got %s", fake.sent[0].outgoing.Endpoint)
	}
	// Username mode is hash, so no "Alice"
	if strings.Contains(fake.sent[0].outgoing.Text, "Alice") {
		t.Fatalf("expected hash mode without push name, got %q", fake.sent[0].outgoing.Text)
	}
}

func TestRouterPollCreationFanOut(t *testing.T) {
	r, store, fake := newTestRouter(t, config.UsernameModePushName)
	ctx := context.Background()

	inc := transport.Incoming{
		Endpoint: "c1g1",
		RemoteID: "poll-orig-1",
		Sender: transport.Sender{
			DisplayName: "Alice",
			PhoneNumber: "15551234567",
			OpaqueID:    "u_abcdefghij",
		},
		Kind:                "poll",
		Text:                "What is your favorite pet?",
		PollOptions:         []string{"Dog", "Cat", "Parrot"},
		PollSelectableCount: 1,
		Timestamp:           time.Unix(1_700_000_000, 0).UTC(),
	}

	if err := r.Handle(ctx, inc); err != nil {
		t.Fatalf("Handle poll error: %v", err)
	}
	waitForSent(t, fake, 5)

	if len(fake.sent) != 5 {
		for _, sent := range fake.sent {
			t.Logf("sent endpoint=%s kind=%s text=%q", sent.outgoing.Endpoint, sent.outgoing.Kind, sent.outgoing.Text)
		}
		t.Fatalf("expected 2 poll copies and 3 result companions, got %d", len(fake.sent))
	}

	pollCopies := 0
	for _, s := range fake.sent {
		if s.outgoing.Kind == "text" {
			continue
		}
		pollCopies++
		if s.outgoing.Kind != "poll" {
			t.Fatalf("expected Kind 'poll', got %q", s.outgoing.Kind)
		}
		if len(s.outgoing.PollOptions) != 3 || s.outgoing.PollOptions[0] != "Dog" || s.outgoing.PollOptions[1] != "Cat" || s.outgoing.PollOptions[2] != "Parrot" {
			t.Fatalf("unexpected PollOptions: %+v", s.outgoing.PollOptions)
		}
		if s.outgoing.PollSelectableCount != 1 {
			t.Fatalf("expected PollSelectableCount 1, got %d", s.outgoing.PollSelectableCount)
		}
	}
	if pollCopies != 2 {
		t.Fatalf("poll copies = %d, want 2", pollCopies)
	}
	var resultText string
	for _, s := range fake.sent {
		if s.outgoing.Kind == "text" {
			resultText = s.outgoing.Text
			break
		}
	}
	if !strings.HasPrefix(resultText, "***Aggregated anonymised live results***\nWhat is your favorite pet?\n") {
		t.Fatalf("unexpected live result heading/question: %q", resultText)
	}
	if !strings.Contains(resultText, "Dog — 0") || !strings.Contains(resultText, "Cat — 0") || !strings.Contains(resultText, "Parrot — 0") {
		t.Fatalf("expected anonymised option results, got %q", resultText)
	}

	canonicalID, err := store.CanonicalForRemote(ctx, "c1g1", "poll-orig-1")
	if err != nil {
		t.Fatalf("failed to find canonical: %v", err)
	}
	isPoll, err := store.IsPoll(ctx, canonicalID)
	if err != nil || !isPoll {
		t.Fatalf("expected isPoll=true, got %v (err: %v)", isPoll, err)
	}
	var companions int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM poll_result_companions WHERE canonical_id = ?`, canonicalID).Scan(&companions); err != nil {
		t.Fatal(err)
	}
	if companions != 3 {
		t.Fatalf("result companions = %d, want one per endpoint", companions)
	}

	if err := r.Handle(ctx, transport.Incoming{
		Endpoint:  "c1g1",
		RemoteID:  "poll-orig-1-delete",
		Kind:      "delete",
		ReplyTo:   &transport.MessageRef{Endpoint: "c1g1", RemoteMessageID: "poll-orig-1"},
		Timestamp: time.Unix(1_700_000_100, 0).UTC(),
	}); err != nil {
		t.Fatalf("Handle poll delete error: %v", err)
	}
	waitForMutations(t, fake, 0, 0, 5)
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM poll_result_companions WHERE canonical_id = ?`, canonicalID).Scan(&companions); err != nil {
		t.Fatal(err)
	}
	if companions != 0 {
		t.Fatalf("result companions after poll delete = %d, want 0", companions)
	}
}

func TestRouterPollVoteTrackingAndAggregation(t *testing.T) {
	r, _, fake := newTestRouter(t, config.UsernameModePushName)
	ctx := context.Background()

	// 1. Create a poll in c1g1
	pollInc := transport.Incoming{
		Endpoint: "c1g1",
		RemoteID: "poll-msg-1",
		Sender: transport.Sender{
			DisplayName: "Alice",
			PhoneNumber: "15551234567",
			OpaqueID:    "u_abcdefghij",
		},
		Kind:                "poll",
		Text:                "Lunch choice?",
		PollOptions:         []string{"Pizza", "Sushi"},
		PollSelectableCount: 1,
		Timestamp:           time.Unix(1_700_000_000, 0).UTC(),
	}
	if err := r.Handle(ctx, pollInc); err != nil {
		t.Fatalf("Handle poll creation error: %v", err)
	}
	waitForSent(t, fake, 2)

	// Option hashes
	hPizza := hex.EncodeToString(cryptoSHA256("Pizza"))
	hSushi := hex.EncodeToString(cryptoSHA256("Sushi"))

	// 2. User in c1g1 votes for Pizza
	vote1 := transport.Incoming{
		Endpoint: "c1g1",
		RemoteID: "vote-1",
		Sender: transport.Sender{
			OpaqueID: "u_bcdefghijk",
		},
		Kind:             "poll_vote",
		ReplyTo:          &transport.MessageRef{Endpoint: "c1g1", RemoteMessageID: "poll-msg-1"},
		PollOptionHashes: []string{hPizza},
		Timestamp:        time.Unix(1_700_000_010, 0).UTC(),
	}
	if err := r.Handle(ctx, vote1); err != nil {
		t.Fatalf("Handle vote 1 error: %v", err)
	}

	// 3. User in c1g2 votes for Sushi (replying to c1g2-sent-1)
	vote2 := transport.Incoming{
		Endpoint: "c1g2",
		RemoteID: "vote-2",
		Sender: transport.Sender{
			OpaqueID: "u_cdefghijkl",
		},
		Kind:             "poll_vote",
		ReplyTo:          &transport.MessageRef{Endpoint: "c1g2", RemoteMessageID: "c1g2-sent-1"},
		PollOptionHashes: []string{hSushi},
		Timestamp:        time.Unix(1_700_000_020, 0).UTC(),
	}
	if err := r.Handle(ctx, vote2); err != nil {
		t.Fatalf("Handle vote 2 error: %v", err)
	}

	// 4. User in c1g3 votes for Pizza (replying to c1g3-sent-1)
	vote3 := transport.Incoming{
		Endpoint: "c1g3",
		RemoteID: "vote-3",
		Sender: transport.Sender{
			OpaqueID: "u_defghijklm",
		},
		Kind:             "poll_vote",
		ReplyTo:          &transport.MessageRef{Endpoint: "c1g3", RemoteMessageID: "c1g3-sent-1"},
		PollOptionHashes: []string{hPizza},
		Timestamp:        time.Unix(1_700_000_030, 0).UTC(),
	}
	if err := r.Handle(ctx, vote3); err != nil {
		t.Fatalf("Handle vote 3 error: %v", err)
	}

	// Clear fake.sent before triggering aggregation
	fake.sent = nil

	// 5. Trigger aggregation by replying "aggregate-response" to c1g2-sent-1
	aggTrigger := transport.Incoming{
		Endpoint: "c1g2",
		RemoteID: "agg-trigger-msg",
		Sender: transport.Sender{
			DisplayName: "Bob",
			PhoneNumber: "15559876543",
			OpaqueID:    "u_efghijklmn",
		},
		Kind:      "text",
		Text:      "aggregate-response",
		ReplyTo:   &transport.MessageRef{Endpoint: "c1g2", RemoteMessageID: "c1g2-sent-1"},
		Timestamp: time.Unix(1_700_000_040, 0).UTC(),
	}
	if err := r.Handle(ctx, aggTrigger); err != nil {
		t.Fatalf("Handle aggregation error: %v", err)
	}

	// Aggregated summary should be sent to all 3 groups
	if len(fake.sent) != 3 {
		t.Fatalf("expected 3 aggregation messages, got %d", len(fake.sent))
	}

	for _, s := range fake.sent {
		if s.outgoing.Kind != "text" {
			t.Fatalf("expected summary kind text, got %q", s.outgoing.Kind)
		}
		if !strings.Contains(s.outgoing.Text, "Lunch choice?") {
			t.Fatalf("expected question in summary, got: %s", s.outgoing.Text)
		}
		if !strings.Contains(s.outgoing.Text, "Pizza: 2 vote(s) (66%)") {
			t.Fatalf("expected Pizza 2 votes (66%%), got: %s", s.outgoing.Text)
		}
		if !strings.Contains(s.outgoing.Text, "Sushi: 1 vote(s) (33%)") {
			t.Fatalf("expected Sushi 1 vote (33%%), got: %s", s.outgoing.Text)
		}
		if !strings.Contains(s.outgoing.Text, "Total votes: 3") {
			t.Fatalf("expected Total votes: 3, got: %s", s.outgoing.Text)
		}
		// In c1g1, reply should be to poll-msg-1
		if s.outgoing.Endpoint == "c1g1" {
			if s.outgoing.ReplyTo == nil || s.outgoing.ReplyTo.RemoteMessageID != "poll-msg-1" {
				t.Fatalf("expected c1g1 replyTo poll-msg-1, got %+v", s.outgoing.ReplyTo)
			}
		}
		// In c1g2, reply should be to c1g2-sent-1
		if s.outgoing.Endpoint == "c1g2" {
			if s.outgoing.ReplyTo == nil || s.outgoing.ReplyTo.RemoteMessageID != "c1g2-sent-1" {
				t.Fatalf("expected c1g2 replyTo c1g2-sent-1, got %+v", s.outgoing.ReplyTo)
			}
		}
		// In c1g3, reply should be to c1g3-sent-1
		if s.outgoing.Endpoint == "c1g3" {
			if s.outgoing.ReplyTo == nil || s.outgoing.ReplyTo.RemoteMessageID != "c1g3-sent-1" {
				t.Fatalf("expected c1g3 replyTo c1g3-sent-1, got %+v", s.outgoing.ReplyTo)
			}
		}
	}
}

func TestRouterPollAggregationAfterRestart(t *testing.T) {
	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"c1g1": {Transport: config.TransportWhatsApp, RemoteID: "111@g.us"},
			"c1g2": {Transport: config.TransportWhatsApp, RemoteID: "222@g.us"},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Endpoints: []string{"c1g1", "c1g2"}}},
		Identity: config.Identity{UsernameMode: config.UsernameModePushName},
	}
	path := filepath.Join(t.TempDir(), "sync.db")
	syncStore, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syncStore.Close() })
	fake := &fakeSender{}
	ctx := context.Background()

	r1, err := New(cfg, syncStore, fake)
	if err != nil {
		t.Fatal(err)
	}

	pollInc := transport.Incoming{
		Endpoint: "c1g1",
		RemoteID: "poll-msg-1",
		Sender: transport.Sender{
			DisplayName: "Alice",
			PhoneNumber: "15551234567",
			OpaqueID:    "u_abcdefghij",
		},
		Kind:                "poll",
		Text:                "Question before restart",
		PollOptions:         []string{"Option Alpha", "Option Beta"},
		PollSelectableCount: 1,
		Timestamp:           time.Unix(1_700_000_000, 0).UTC(),
	}
	if err := r1.Handle(ctx, pollInc); err != nil {
		t.Fatalf("Handle poll creation error: %v", err)
	}
	// Delivery is lane-owned; finish the first router before simulating a restart.
	r1.Close()

	hAlpha := hex.EncodeToString(cryptoSHA256("Option Alpha"))

	vote1 := transport.Incoming{
		Endpoint: "c1g1",
		RemoteID: "vote-1",
		Sender: transport.Sender{
			OpaqueID: "u_bcdefghijk",
		},
		Kind:             "poll_vote",
		ReplyTo:          &transport.MessageRef{Endpoint: "c1g1", RemoteMessageID: "poll-msg-1"},
		PollOptionHashes: []string{hAlpha},
		Timestamp:        time.Unix(1_700_000_010, 0).UTC(),
	}
	if err := r1.Handle(ctx, vote1); err != nil {
		t.Fatalf("Handle vote 1 error: %v", err)
	}

	// Simulate restart by instantiating a new Router instance with empty memory cache
	fake.sent = nil
	r2, err := New(cfg, syncStore, fake)
	if err != nil {
		t.Fatal(err)
	}

	aggTrigger := transport.Incoming{
		Endpoint: "c1g1",
		RemoteID: "agg-trigger-msg",
		Sender: transport.Sender{
			DisplayName: "Bob",
			PhoneNumber: "15559876543",
			OpaqueID:    "u_cdefghijkl",
		},
		Kind:      "text",
		Text:      "aggregate-response",
		ReplyTo:   &transport.MessageRef{Endpoint: "c1g1", RemoteMessageID: "poll-msg-1"},
		Timestamp: time.Unix(1_700_000_040, 0).UTC(),
	}
	if err := r2.Handle(ctx, aggTrigger); err != nil {
		t.Fatalf("Handle aggregation error after restart: %v", err)
	}

	if len(fake.sent) != 2 {
		t.Fatalf("expected 2 summary messages, got %d", len(fake.sent))
	}

	for _, s := range fake.sent {
		if !strings.Contains(s.outgoing.Text, "Option 1: 1 vote(s) (100%)") {
			t.Fatalf("expected Option 1 fallback label with 1 vote (100%%), got: %s", s.outgoing.Text)
		}
		if !strings.Contains(s.outgoing.Text, "Option 2: 0 vote(s) (0%)") {
			t.Fatalf("expected Option 2 fallback label with 0 votes, got: %s", s.outgoing.Text)
		}
		if !strings.Contains(s.outgoing.Text, "Total votes: 1") {
			t.Fatalf("expected Total votes: 1, got: %s", s.outgoing.Text)
		}
	}
}

func TestRouterTelegramPollSnapshotUsesCanonicalAggregate(t *testing.T) {
	r, syncStore, fake := newTestRouter(t, config.UsernameModeHash)
	ctx := context.Background()
	if err := r.Handle(ctx, transport.Incoming{Endpoint: "c1g1", RemoteID: "poll-snapshot-source", Sender: transport.Sender{OpaqueID: "u_abcdefghij"}, Kind: "poll", Text: "Question", PollOptions: []string{"A", "B"}, PollSelectableCount: 1, Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	waitForSent(t, fake, 2)
	canonicalID, err := syncStore.CanonicalForRemote(ctx, "c1g1", "poll-snapshot-source")
	if err != nil {
		t.Fatal(err)
	}
	if err := syncStore.SavePollProviderRef(ctx, store.PollProviderRef{CanonicalID: canonicalID, EndpointID: "c1g2", Provider: "telegram", Reference: "opaque-poll"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Handle(ctx, transport.Incoming{Endpoint: "c1g2", RemoteID: "opaque-poll", Kind: "poll_snapshot", PollProvider: "telegram", PollProviderReference: "opaque-poll", PollSnapshot: map[int]int{0: 4, 1: 2}, Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	counts, err := syncStore.GetPollAggregateCounts(ctx, canonicalID)
	if err != nil {
		t.Fatal(err)
	}
	if counts[0] != 4 || counts[1] != 2 {
		t.Fatalf("snapshot counts = %#v", counts)
	}
}

func cryptoSHA256(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}
