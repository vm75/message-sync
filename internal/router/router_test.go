package router

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/controlstore"
	"github.com/vm75/message-sync/internal/identity"
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
	deleted    []transport.MessageRef
	reacted    []transport.Reaction
	next       map[transport.EndpointID]int
	sendErrors map[transport.EndpointID][]error
	sendError  func(transport.Outgoing) error
	attempts   int
}

func (f *fakeSender) Send(_ context.Context, outgoing transport.Outgoing) (transport.MessageRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.next == nil {
		f.next = make(map[transport.EndpointID]int)
	}
	f.next[outgoing.Endpoint]++
	f.attempts++
	if f.sendError != nil {
		if err := f.sendError(outgoing); err != nil {
			return transport.MessageRef{}, err
		}
	}
	if queued := f.sendErrors[outgoing.Endpoint]; len(queued) > 0 {
		err := queued[0]
		f.sendErrors[outgoing.Endpoint] = queued[1:]
		return transport.MessageRef{}, err
	}
	ref := transport.MessageRef{
		Endpoint:        outgoing.Endpoint,
		RemoteMessageID: string(outgoing.Endpoint) + "-sent-" + string(rune('0'+f.next[outgoing.Endpoint])),
	}
	f.sent = append(f.sent, sentMessage{outgoing: outgoing, ref: ref})
	return ref, nil
}

func TestAmbiguousCreateIsNotBlindlyRetried(t *testing.T) {
	r, syncStore, fake := newTestRouter(t, config.UsernameModeHash)
	fake.sendErrors = map[transport.EndpointID][]error{
		"c1g2": {transport.NewFailureWithCertainty(transport.FailureTransient, 0, transport.SendUnknown, errors.New("provider accepted but response was lost"))},
	}
	if err := r.Handle(context.Background(), testIncoming("c1g1", "ambiguous-source")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	fake.mu.Lock()
	attempts := fake.attempts
	fake.mu.Unlock()
	if attempts != 2 {
		t.Fatalf("attempts = %d, want source fan-out attempts only", attempts)
	}
	canonical, err := syncStore.CanonicalForRemote(context.Background(), "c1g1", "ambiguous-source")
	if err != nil {
		t.Fatal(err)
	}
	step, err := syncStore.CreateStep(context.Background(), canonical, "c1g2", 1, "primary")
	if err != nil {
		t.Fatal(err)
	}
	if step.State != "ambiguous" {
		t.Fatalf("primary step state = %q, want ambiguous", step.State)
	}
}

func TestLocalPrefixSuppressesBeforeCanonicalizationAndMedia(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.db")
	syncStore, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer syncStore.Close()
	fake := &fakeSender{}
	cfg := testConfig(config.UsernameModeHash)
	cfg.LocalPrefix = "!local"
	r, err := New(cfg, syncStore, fake)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	loaded := false
	incoming := testIncoming("c1g1", "local-1")
	incoming.Text = "!local keep this here"
	incoming.MediaLoader = func(context.Context) ([]byte, error) {
		loaded = true
		return []byte("must not load"), nil
	}
	if err := r.Handle(context.Background(), incoming); err != nil {
		t.Fatal(err)
	}
	if loaded || len(fake.sent) != 0 {
		t.Fatalf("local message loaded media or fanned out: loaded=%v sends=%d", loaded, len(fake.sent))
	}
	suppressed, err := syncStore.IsSuppressedLocalMessage(context.Background(), "c1g1", "local-1")
	if err != nil || !suppressed {
		t.Fatalf("suppressed marker = %v, err=%v", suppressed, err)
	}
	if _, err := syncStore.CanonicalForRemote(context.Background(), "c1g1", "local-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("local message created canonical state: %v", err)
	}
	reply := testIncoming("c1g1", "reply-1")
	reply.Text = "ordinary reply"
	reply.ReplyTo = &transport.MessageRef{Endpoint: "c1g1", RemoteMessageID: "local-1"}
	reply.QuotedText = "SECRET LOCAL CONTENT"
	if err := r.Handle(context.Background(), reply); err != nil {
		t.Fatal(err)
	}
	waitForSent(t, fake, 2)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, message := range fake.sent {
		if message.outgoing.QuotedText != "" || strings.Contains(message.outgoing.Text, "SECRET LOCAL CONTENT") {
			t.Fatal("local-only quoted content leaked into bridged reply")
		}
	}
}

func TestMultipleLocalPrefixesSuppression(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.db")
	syncStore, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer syncStore.Close()
	fake := &fakeSender{}
	cfg := testConfig(config.UsernameModeHash)
	cfg.LocalPrefix = "!local #local [local] //"
	r, err := New(cfg, syncStore, fake)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	testCases := []struct {
		text       string
		suppressed bool
	}{
		{"!local message", true},
		{"#local message", true},
		{"[local] message", true},
		{"// message", true},
		{"normal message", false},
		{"local without prefix", false},
	}

	for i, tc := range testCases {
		fake.mu.Lock()
		fake.sent = nil
		fake.mu.Unlock()

		remoteID := fmt.Sprintf("msg-%d", i)
		incoming := testIncoming("c1g1", remoteID)
		incoming.Text = tc.text

		if err := r.Handle(context.Background(), incoming); err != nil {
			t.Fatal(err)
		}

		suppressed, err := syncStore.IsSuppressedLocalMessage(context.Background(), "c1g1", remoteID)
		if err != nil {
			t.Fatal(err)
		}
		if suppressed != tc.suppressed {
			t.Fatalf("for text %q: got suppressed=%v, want %v", tc.text, suppressed, tc.suppressed)
		}
		if tc.suppressed {
			fake.mu.Lock()
			sentCount := len(fake.sent)
			fake.mu.Unlock()
			if sentCount != 0 {
				t.Fatalf("for suppressed text %q: got %d sends, want 0", tc.text, sentCount)
			}
		}
	}
}

func TestReplyLineageRetainsMultipleChildScopesAndOmitsNativeScopeHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.db")
	syncStore, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer syncStore.Close()
	fake := &fakeSender{}
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewWithHasher(testConfig(config.UsernameModeHash), syncStore, fake, hasher)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	first := testIncoming("c1g1", "thread-message")
	first.ChildScope = &transport.ChildScope{Kind: transport.ScopeKindDiscordThread, RemoteID: "123456789012345678", Label: "Backend API"}
	if err := r.Handle(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	waitForSent(t, fake, 2)
	reply := testIncoming("c1g2", "topic-reply")
	reply.Text = "reply from topic"
	reply.ChildScope = &transport.ChildScope{Kind: transport.ScopeKindTelegramTopic, RemoteID: "77"}
	reply.ReplyTo = &transport.MessageRef{Endpoint: "c1g2", RemoteMessageID: "c1g2-sent-1"}
	if err := r.Handle(context.Background(), reply); err != nil {
		t.Fatal(err)
	}
	waitForSent(t, fake, 4)
	canonical, err := syncStore.CanonicalForRemote(context.Background(), "c1g2", "topic-reply")
	if err != nil {
		t.Fatal(err)
	}
	scopes, err := syncStore.CanonicalScopes(context.Background(), canonical)
	if err != nil || len(scopes) != 2 {
		t.Fatalf("reply scopes = %#v, err=%v; want two endpoint scopes", scopes, err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	var sawNative, sawFlat bool
	for _, sent := range fake.sent[2:] {
		if sent.outgoing.Endpoint == "c1g1" {
			sawNative = sent.outgoing.ChildScope != nil && sent.outgoing.ChildScope.Kind == transport.ScopeKindDiscordThread && sent.outgoing.ChildScope.RemoteID == "123456789012345678"
		}
		if sent.outgoing.Endpoint == "c1g3" {
			sawFlat = strings.Contains(sent.outgoing.Text, "[contexts ") && strings.Contains(sent.outgoing.Text, hasher.ScopeToken("c1g1\x00discord_thread\x00123456789012345678"))
		}
	}
	if !sawNative || !sawFlat {
		t.Fatalf("scoped reply sends = %#v, native=%v flat=%v", fake.sent[2:], sawNative, sawFlat)
	}
}

func TestMediaCompanionIsNotRepeatedWhenPrimaryRetries(t *testing.T) {
	r, _, fake := newTestRouter(t, config.UsernameModeHash)
	failed := false
	fake.sendError = func(outgoing transport.Outgoing) error {
		if outgoing.Endpoint == "c1g2" && !outgoing.AttributionOnly && !failed {
			failed = true
			return transport.NewFailure(transport.FailureTransient, 0, errors.New("primary not accepted"))
		}
		return nil
	}
	incoming := testIncoming("c1g1", "media-source")
	incoming.Kind = "sticker"
	incoming.MediaLoader = func(context.Context) ([]byte, error) { return []byte("media"), nil }
	if err := r.Handle(context.Background(), incoming); err != nil {
		t.Fatal(err)
	}
	waitForSent(t, fake, 4)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	var companions, primary int
	for _, sent := range fake.sent {
		if sent.outgoing.AttributionOnly {
			companions++
		} else if sent.outgoing.Kind == "sticker" {
			primary++
		}
	}
	if companions != 2 || primary != 2 {
		t.Fatalf("companion=%d primary=%d, want one each destination", companions, primary)
	}
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
		if sent.outgoing.Text != "*_c1g2/Alice Example_*: hello" {
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

func TestFriendlyAttributionUsesSourceChildLabelAndGenericFallback(t *testing.T) {
	ctx := context.Background()
	syncStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer syncStore.Close()
	cfg := testConfig(config.UsernameModeHash)
	cfg.ChildContextDisplayMode = config.ChildContextDisplayFriendly
	for alias, endpoint := range cfg.Endpoints {
		endpoint.ConnectionID = "conn-1"
		cfg.Endpoints[alias] = endpoint
	}
	if err := config.Save(ctx, syncStore.DB(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := syncStore.UpsertChildScopeLabel(ctx, "c1g1", string(transport.ScopeKindDiscordThread), "thread-1", "Dinner * Plans"); err != nil {
		t.Fatal(err)
	}
	fake := &fakeSender{}
	r, err := New(cfg, syncStore, fake)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	root, err := r.forwardedText(ctx, testIncoming("c1g1", "root"))
	if err != nil || root != "*_c1g1/u_abcdefghij_*: hello" {
		t.Fatalf("friendly root = %q, err=%v", root, err)
	}
	known := testIncoming("c1g1", "known")
	known.ChildScope = &transport.ChildScope{Kind: transport.ScopeKindDiscordThread, RemoteID: "thread-1"}
	knownText, err := r.forwardedText(ctx, known)
	if err != nil || knownText != "*_c1g1:Dinner \\* Plans/u_abcdefghij_*: hello" {
		t.Fatalf("friendly persisted child = %q, err=%v", knownText, err)
	}
	unknown := testIncoming("c1g1", "unknown")
	unknown.ChildScope = &transport.ChildScope{Kind: transport.ScopeKindTelegramTopic, RemoteID: "topic-1"}
	unknownText, err := r.forwardedText(ctx, unknown)
	if err != nil || unknownText != "*_c1g1:topic/u_abcdefghij_*: hello" {
		t.Fatalf("friendly unknown child = %q, err=%v", unknownText, err)
	}
	if err := r.Handle(ctx, known); err != nil {
		t.Fatal(err)
	}
	waitForSent(t, fake, 2)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, sent := range fake.sent {
		if sent.outgoing.RenderedText != knownText || strings.Contains(sent.outgoing.RenderedText, "[contexts") {
			t.Fatalf("router did not propagate friendly rendered text: %#v", sent.outgoing)
		}
		if sent.outgoing.PollAttribution != "*_c1g1:Dinner \\* Plans/u_abcdefghij_*:" {
			t.Fatalf("router did not propagate structured poll attribution: %#v", sent.outgoing)
		}
	}
}

func TestFriendlyPollAttributionPrefersDisplayNameOverPhone(t *testing.T) {
	r, syncStore, _ := newTestRouter(t, config.UsernameModePushName)
	defer r.Close()
	defer syncStore.Close()

	incoming := transport.Incoming{
		Endpoint:   "wg2",
		Sender:     transport.Sender{DisplayName: "Display Name", PhoneNumber: "test-phone", OpaqueID: "u_hash"},
		ChildScope: &transport.ChildScope{Kind: transport.ScopeKindTelegramTopic, RemoteID: "topic-1", Label: "Plans"},
	}
	got := r.friendlyPollAttribution(context.Background(), config.ChildContextDisplayFriendly, incoming, incoming.ChildScope)
	if got != "*_wg2:Plans/Display Name_*:" {
		t.Fatalf("poll attribution = %q, want display name without phone", got)
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

func TestDeliveryStatusTreatsTerminalFailuresAsHistory(t *testing.T) {
	r, syncStore, _ := newTestRouter(t, config.UsernameModeHash)
	defer r.Close()
	now := time.Now().UTC().UnixMilli()
	if _, err := syncStore.DB().Exec(`
		INSERT INTO canonical_messages(canonical_id, created_at) VALUES ('canon-history', ?);
		INSERT INTO delivery_operations(canonical_id, endpoint_id, operation_kind, operation_revision, state, failure_class, created_at, updated_at)
		VALUES ('canon-history', 'c1g1', 'create', 1, 'failed', 'unsupported', ?, ?)
	`, now, now, now); err != nil {
		t.Fatal(err)
	}
	statuses, err := r.DeliveryStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range statuses {
		if status.EndpointID == "c1g1" {
			if status.LaneState != "healthy" || status.Failed != 1 || status.FailureClass != "unsupported" {
				t.Fatalf("terminal failure affected live state: %#v", status)
			}
			return
		}
	}
	t.Fatal("missing c1g1 delivery status")
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
	wantResult := "📊 ***LIVE POLL RESULTS ACROSS ALL GROUPS***\n❓ What is your favorite pet?\n\n**Options**\n○ *Dog* — 0 votes\n○ *Cat* — 0 votes\n○ *Parrot* — 0 votes"
	if resultText != wantResult {
		t.Fatalf("unexpected exact live result layout: got %q, want %q", resultText, wantResult)
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
		if !strings.Contains(s.outgoing.Text, "○ *Pizza* — 2 votes") {
			t.Fatalf("expected Pizza 2 votes, got: %s", s.outgoing.Text)
		}
		if !strings.Contains(s.outgoing.Text, "○ *Sushi* — 1 votes") {
			t.Fatalf("expected Sushi 1 vote, got: %s", s.outgoing.Text)
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

	// Trigger responses are live results too: a later vote must update both
	// the bridge-owned companions and the responses sent by the trigger.
	waitForMutations(t, fake, 3, 0, 0)
	fake.mu.Lock()
	fake.edited = nil
	fake.mu.Unlock()
	if err := r.Handle(ctx, transport.Incoming{
		Endpoint: "c1g1", RemoteID: "vote-after-trigger", Kind: "poll_vote",
		Sender:           transport.Sender{OpaqueID: "u_ghijklmnop"},
		ReplyTo:          &transport.MessageRef{Endpoint: "c1g1", RemoteMessageID: "poll-msg-1"},
		PollOptionHashes: []string{hPizza}, Timestamp: time.Unix(1_700_000_050, 0).UTC(),
	}); err != nil {
		t.Fatalf("Handle post-trigger vote error: %v", err)
	}
	waitForMutations(t, fake, 6, 0, 0)
	fake.mu.Lock()
	defer fake.mu.Unlock()
	updated := 0
	for _, edit := range fake.edited {
		if strings.Contains(edit.text, "○ *Pizza* — 3 votes") {
			updated++
		}
	}
	if updated != 6 {
		t.Fatalf("post-trigger updated results = %d, want 6 companion and trigger messages", updated)
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
	control, err := controlstore.Open(context.Background(), filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	cipher, err := controlstore.NewCredentialCipher([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	presentationStore, err := controlstore.NewPollPresentationStore(control.DB(), cipher)
	if err != nil {
		t.Fatal(err)
	}
	hasher, err := identity.New([]byte("12345678901234567890123456789012"))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeSender{}
	ctx := context.Background()

	r1, err := NewWithHasherAndPollPresentationStore(cfg, syncStore, fake, hasher, presentationStore)
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
	r2, err := NewWithHasherAndPollPresentationStore(cfg, syncStore, fake, hasher, presentationStore)
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
		if !strings.Contains(s.outgoing.Text, "❓ Question before restart") || !strings.Contains(s.outgoing.Text, "○ *Option Alpha* — 1 votes") {
			t.Fatalf("expected persisted poll presentation with Alpha 1 vote, got: %s", s.outgoing.Text)
		}
		if !strings.Contains(s.outgoing.Text, "○ *Option Beta* — 0 votes") {
			t.Fatalf("expected persisted Beta label with 0 votes, got: %s", s.outgoing.Text)
		}
	}
}

func TestRouterSelfPollReplayRestoresPresentationAfterRestart(t *testing.T) {
	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"c1g1": {Transport: config.TransportWhatsApp, RemoteID: "111@g.us"},
			"c1g2": {Transport: config.TransportWhatsApp, RemoteID: "222@g.us"},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Endpoints: []string{"c1g1", "c1g2"}}},
		Identity: config.Identity{UsernameMode: config.UsernameModePushName},
	}
	syncStore, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syncStore.Close() })
	fake := &fakeSender{}
	ctx := context.Background()
	poll := transport.Incoming{
		Endpoint: "c1g1", RemoteID: "poll-msg-1", Kind: "poll",
		Sender:   transport.Sender{OpaqueID: "u_abcdefghij"},
		FromSelf: true, Text: "Where should we eat?", PollOptions: []string{"Pizza", "Sushi"},
		PollSelectableCount: 1, Timestamp: time.Unix(1_700_000_000, 0).UTC(),
	}
	r1, err := New(cfg, syncStore, fake)
	if err != nil {
		t.Fatal(err)
	}
	if err := r1.Handle(ctx, poll); err != nil {
		t.Fatalf("initial poll: %v", err)
	}
	r1.Close()
	fake.sent = nil

	r2, err := New(cfg, syncStore, fake)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	if err := r2.Handle(ctx, poll); err != nil {
		t.Fatalf("replayed poll: %v", err)
	}
	if err := r2.Handle(ctx, transport.Incoming{
		Endpoint: "c1g1", RemoteID: "aggregate-trigger", Kind: "text", Text: "aggregate-response",
		ReplyTo: &transport.MessageRef{Endpoint: "c1g1", RemoteMessageID: "poll-msg-1"}, Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("aggregation trigger: %v", err)
	}

	if len(fake.sent) != 2 {
		t.Fatalf("summary messages = %d, want 2", len(fake.sent))
	}
	for _, sent := range fake.sent {
		if !strings.Contains(sent.outgoing.Text, "❓ Where should we eat?") ||
			!strings.Contains(sent.outgoing.Text, "○ *Pizza* — 0 votes") ||
			!strings.Contains(sent.outgoing.Text, "○ *Sushi* — 0 votes") {
			t.Fatalf("summary lost poll presentation: %q", sent.outgoing.Text)
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

func TestRouterPollAggregationMultipleTriggers(t *testing.T) {
	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"c1g1": {Transport: config.TransportWhatsApp, RemoteID: "111@g.us"},
			"c1g2": {Transport: config.TransportWhatsApp, RemoteID: "222@g.us"},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Endpoints: []string{"c1g1", "c1g2"}}},
		Identity: config.Identity{UsernameMode: config.UsernameModePushName},
		Polls:    config.Polls{AggregationTrigger: "aggregate-response /poll-results #agg"},
	}
	path := filepath.Join(t.TempDir(), "sync.db")
	syncStore, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syncStore.Close() })
	fake := &fakeSender{}
	ctx := context.Background()

	r, err := New(cfg, syncStore, fake)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	pollInc := transport.Incoming{
		Endpoint: "c1g1",
		RemoteID: "poll-multi-trig",
		Sender: transport.Sender{
			DisplayName: "Alice",
			PhoneNumber: "15551234567",
			OpaqueID:    "u_abcdefghij",
		},
		Kind:                "poll",
		Text:                "Multi trigger poll?",
		PollOptions:         []string{"Option 1", "Option 2"},
		PollSelectableCount: 1,
		Timestamp:           time.Unix(1_700_000_000, 0).UTC(),
	}
	if err := r.Handle(ctx, pollInc); err != nil {
		t.Fatalf("Handle poll creation error: %v", err)
	}

	// Test each configured trigger
	triggers := []string{"aggregate-response", "/poll-results", "#agg"}
	for _, tr := range triggers {
		fake.mu.Lock()
		fake.sent = nil
		fake.mu.Unlock()

		aggTrigger := transport.Incoming{
			Endpoint: "c1g1",
			RemoteID: "agg-trig-" + tr,
			Sender: transport.Sender{
				DisplayName: "Bob",
				PhoneNumber: "15559876543",
				OpaqueID:    "u_cdefghijkl",
			},
			Kind:      "text",
			Text:      tr,
			ReplyTo:   &transport.MessageRef{Endpoint: "c1g1", RemoteMessageID: "poll-multi-trig"},
			Timestamp: time.Unix(1_700_000_040, 0).UTC(),
		}
		if err := r.Handle(ctx, aggTrigger); err != nil {
			t.Fatalf("Handle aggregation error for trigger %q: %v", tr, err)
		}

		fake.mu.Lock()
		sentCount := len(fake.sent)
		fake.mu.Unlock()
		if sentCount != 2 {
			t.Fatalf("expected 2 summary messages for trigger %q, got %d", tr, sentCount)
		}
	}
}

func cryptoSHA256(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

func TestSenderPresentationUsernamePrefersDisplayNameAndFallsBackSafely(t *testing.T) {
	sender := transport.Sender{
		DisplayName: "  Alice   Example  ",
		PhoneNumber: "15551234567",
		OpaqueID:    "u_abcdefghij",
	}

	if got := senderPresentationUsername(sender, config.UsernameModePushName); got != "Alice Example" {
		t.Fatalf("push-name presentation = %q, want display name without phone number", got)
	}

	sender.DisplayName = ""
	if got := senderPresentationUsername(sender, config.UsernameModePushName); got != "15551234567" {
		t.Fatalf("push-name presentation without display name = %q, want phone fallback", got)
	}

	sender.DisplayName = "Alice Example"
	if got := senderPresentationUsername(sender, config.UsernameModeHash); got != "u_abcdefghij" {
		t.Fatalf("hash presentation = %q, want opaque ID", got)
	}
}
