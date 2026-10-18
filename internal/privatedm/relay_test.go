package privatedm

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/controlstore"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/router"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/testutil"
	"github.com/vm75/message-sync/internal/transport"
)

type privateSend struct {
	inbox    transport.PrivateInbox
	text, id string
}
type fakeRuntime struct {
	inboxes    map[string]transport.PrivateInbox
	members    map[string]bool
	sent       []privateSend
	deleted    []transport.PrivateCopy
	fail       error
	groupCalls int
	options    []transport.PrivateSendOptions
}

func (f *fakeRuntime) VerifyPrivateMember(_ context.Context, e transport.EndpointID, a string) bool {
	return f.members[string(e)+a]
}
func (f *fakeRuntime) VerifyPrivateInboxMember(ctx context.Context, e transport.EndpointID, a string, inbox transport.PrivateInbox) bool {
	return f.VerifyPrivateMember(ctx, e, a) && sameInbox(f.inboxes[string(e)+a], inbox)
}
func (f *fakeRuntime) ResolvePrivateInbox(_ context.Context, e transport.EndpointID, a string) (transport.PrivateInbox, bool) {
	inbox, ok := f.inboxes[string(e)+a]
	return inbox, ok
}
func (f *fakeRuntime) SendPrivate(_ context.Context, in transport.PrivateInbox, text string, options ...transport.PrivateSendOptions) (transport.PrivateSendResult, error) {
	o := transport.PrivateSendOptions{}
	if len(options) > 0 {
		o = options[0]
	}
	f.options = append(f.options, o)
	id := fmt.Sprintf("bot-%d", len(f.sent)+1)
	f.sent = append(f.sent, privateSend{in, text, id})
	if f.fail != nil {
		return transport.PrivateSendResult{}, f.fail
	}
	result := transport.PrivateSendResult{RemoteMessageID: id}
	if in.Transport == "whatsapp" {
		result.LocalCopy = transport.PrivateCopy{ConnectionID: in.ConnectionID, Transport: in.Transport, OwnerID: in.OwnerID, Token: []byte(id)}
	}
	return result, nil
}
func (f *fakeRuntime) DeletePrivateCopyForMe(_ context.Context, c transport.PrivateCopy) error {
	f.deleted = append(f.deleted, c)
	return nil
}
func (f *fakeRuntime) Send(context.Context, transport.Outgoing) (transport.MessageRef, error) {
	f.groupCalls++
	return transport.MessageRef{}, nil
}
func (f *fakeRuntime) React(context.Context, transport.Reaction) error { f.groupCalls++; return nil }
func (f *fakeRuntime) Edit(context.Context, transport.MessageRef, string) error {
	f.groupCalls++
	return nil
}
func (f *fakeRuntime) Delete(context.Context, transport.MessageRef) error { f.groupCalls++; return nil }

func fixture(t *testing.T, from, to string) (*Relay, *fakeRuntime, *config.Config, *time.Time, string, string) {
	t.Helper()
	ctx := context.Background()
	sync, err := store.Open(ctx, filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sync.Close() })
	control, err := controlstore.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { control.Close() })
	secret := []byte("01234567890123456789012345678901")
	hasher, _ := identity.New(secret)
	alice, bob := hasher.UserID("private-alice"), hasher.UserID("private-bob")
	settings := config.DefaultSyncSetSettings()
	settings.PrivateDMRelayEnabled = true
	remote := func(platform string) string {
		switch platform {
		case "whatsapp":
			return "1@g.us"
		case "discord":
			return "123"
		default:
			return "-123"
		}
	}
	cfg := &config.Config{PrivateDMRelayEnabled: true, Endpoints: map[string]config.Endpoint{"a": {Transport: config.Transport(from), ConnectionID: "from", RemoteID: remote(from)}, "b": {Transport: config.Transport(to), ConnectionID: "to", RemoteID: strings.Replace(remote(to), "1", "2", 1)}}, SyncSets: []config.SyncSet{{ID: "mesh", Endpoints: []string{"b", "a"}, Settings: &settings}}}
	if err := config.Save(ctx, sync.DB(), cfg); err != nil {
		t.Fatal(err)
	}
	for _, ep := range []string{"a", "b"} {
		actor := map[string]string{"a": alice, "b": bob}[ep]
		if _, err := sync.ObserveParticipant(ctx, ep, actor); err != nil {
			t.Fatal(err)
		}
		if _, err := sync.ObserveSyncSetActor(ctx, "mesh", actor); err != nil {
			t.Fatal(err)
		}
	}
	f := &fakeRuntime{inboxes: map[string]transport.PrivateInbox{"a" + alice: {ConnectionID: "from", Transport: from, OwnerID: "owner-from", ChatID: "chat-from", Token: []byte("private-native-alice")}, "b" + bob: {ConnectionID: "to", Transport: to, OwnerID: "owner-to", ChatID: "chat-to", Token: []byte("private-native-bob")}}, members: map[string]bool{"a" + alice: true, "b" + bob: true}}
	testutil.SeedRouting(t, sync.DB(), cfg)
	mesh, err := router.NewWithHasher(cfg, sync, f, hasher)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mesh.Close)
	state, err := controlstore.NewPrivateDMStore(control.DB(), secret)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	r := &Relay{Sync: sync, State: state, Runtime: f, Resolver: mesh, Hasher: hasher, Now: func() time.Time { return now }, LoadConfig: func(context.Context) (*config.Config, error) { return cfg, nil }}
	return r, f, cfg, &now, alice, bob
}

func input(inbox transport.PrivateInbox, actor, id, text, reply string, now time.Time) transport.PrivateIncoming {
	return transport.PrivateIncoming{ConnectionID: inbox.ConnectionID, Transport: inbox.Transport, Inbox: inbox, ActorID: actor, RemoteMessageID: id, Text: text, ReplyToMessageID: reply, Kind: "text", Timestamp: now}
}

func TestAllPlatformsUseNativeRepliesAndOwningConnections(t *testing.T) {
	for _, from := range []string{"whatsapp", "discord", "telegram"} {
		for _, to := range []string{"whatsapp", "discord", "telegram"} {
			t.Run(from+"-"+to, func(t *testing.T) {
				r, f, _, now, alice, bob := fixture(t, from, to)
				ctx := context.Background()
				a, b := f.inboxes["a"+alice], f.inboxes["b"+bob]
				if err := r.Handle(ctx, input(a, alice, "a1", "/dm @{Pale Poppy} hello\nsecond line", "", *now)); err != nil {
					t.Fatal(err)
				}
				if len(f.sent) != 1 || f.sent[0].inbox.ConnectionID != "to" || !strings.HasPrefix(f.sent[0].text, "DM from a/Amber Badger\n") || !strings.Contains(f.sent[0].text, "hello\nsecond line") {
					t.Fatalf("initial relay=%+v", f.sent)
				}
				if err := r.Handle(ctx, input(b, bob, "b1", "answer", f.sent[0].id, *now)); err != nil {
					t.Fatal(err)
				}
				if len(f.sent) != 2 || f.sent[1].inbox.ConnectionID != "from" || !strings.HasPrefix(f.sent[1].text, "DM from b/Pale Poppy\n") {
					t.Fatalf("return relay=%+v", f.sent)
				}
				if err := r.Handle(ctx, input(a, alice, "a2", "next", f.sent[1].id, *now)); err != nil {
					t.Fatal(err)
				}
				if len(f.sent) != 3 || f.sent[2].inbox.ConnectionID != "to" {
					t.Fatal("third turn did not reach recorded peer")
				}
				if f.options[0].ReplyToMessageID != "" || f.options[1].ReplyToMessageID != "a1" || f.options[2].ReplyToMessageID != "b1" {
					t.Fatalf("reply chain: %+v", f.options)
				}
				var copies int
				if err := r.Sync.DB().QueryRow(`SELECT COUNT(*) FROM message_copies`).Scan(&copies); err != nil || copies != 0 || f.groupCalls != 0 {
					t.Fatalf("group traffic=%d copies=%d err=%v", f.groupCalls, copies, err)
				}
				if err := r.Handle(ctx, input(a, alice, "a2", "next", f.sent[1].id, *now)); err != nil {
					t.Fatal(err)
				}
				if len(f.sent) != 3 {
					t.Fatal("duplicate was relayed")
				}
			})
		}
	}
}

func TestInvalidPrivateInputsOnlySendHelp(t *testing.T) {
	for _, text := range []string{"hello", "@{Pale Poppy}", "/foo", "/dm", "/dm @{Pale Poppy}", "/dm @{Pale Poppy} ", "/dm @{Pale Poppy} @{Pale Poppy}", "/dm \\@{Pale Poppy} hello"} {
		t.Run(text, func(t *testing.T) {
			r, f, _, now, alice, _ := fixture(t, "discord", "whatsapp")
			a := f.inboxes["a"+alice]
			if err := r.Handle(context.Background(), input(a, alice, "in", text, "unknown", *now)); err != nil {
				t.Fatal(err)
			}
			if len(f.sent) != 1 || f.sent[0].inbox.ConnectionID != "from" || !strings.Contains(f.sent[0].text, "/help") {
				t.Fatalf("invalid input forwarded: %+v", f.sent)
			}
		})
	}
}

func TestReplyOwnershipExpiryAndMembershipChangesFailClosed(t *testing.T) {
	for _, mutation := range []string{"actor", "chat", "connection", "owner-account", "expired", "membership", "endpoint-reassigned", "set-disabled", "global-disabled", "endpoint-deleted"} {
		t.Run(mutation, func(t *testing.T) {
			r, f, cfg, now, alice, bob := fixture(t, "discord", "whatsapp")
			ctx := context.Background()
			a, b := f.inboxes["a"+alice], f.inboxes["b"+bob]
			if err := r.Handle(ctx, input(a, alice, "a1", "/dm @{Pale Poppy} first", "", *now)); err != nil {
				t.Fatal(err)
			}
			in := input(b, bob, "b1", "reply", f.sent[0].id, *now)
			switch mutation {
			case "actor":
				in.ActorID = alice
			case "chat":
				in.Inbox.Token = []byte("other-private-chat")
				in.Inbox.ChatID = "other-private-chat"
			case "connection":
				in.Inbox.ConnectionID = "other"
				in.ConnectionID = "other"
			case "owner-account":
				in.Inbox.OwnerID = "another-bot-account"
			case "expired":
				*now = now.Add(24 * time.Hour)
				in.Timestamp = *now
			case "membership":
				f.members["a"+alice] = false
			case "endpoint-reassigned":
				ep := cfg.Endpoints["a"]
				ep.RemoteID = "999"
				cfg.Endpoints["a"] = ep
			case "set-disabled":
				cfg.SyncSets[0].Settings.PrivateDMRelayEnabled = false
			case "global-disabled":
				cfg.PrivateDMRelayEnabled = false
			case "endpoint-deleted":
				delete(cfg.Endpoints, "a")
			}
			if err := r.Handle(ctx, in); err != nil {
				t.Fatal(err)
			}
			for _, sent := range f.sent[1:] {
				if strings.HasPrefix(sent.text, "DM from ") {
					t.Fatalf("invalid reply relayed: %s", mutation)
				}
			}
		})
	}
}

func TestLifecycleEventsIgnoredAndLocalCleanupDoesNotDeleteRecipientCopy(t *testing.T) {
	r, f, _, now, alice, bob := fixture(t, "discord", "whatsapp")
	ctx := context.Background()
	a := f.inboxes["a"+alice]
	initial := input(a, alice, "a1", "/dm @{Pale Poppy} original", "", *now)
	if err := r.Handle(ctx, initial); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"edit", "delete", "revoke"} {
		event := initial
		event.Kind = kind
		event.Text = "/dm @{Pale Poppy} changed"
		event.RemoteMessageID = kind
		if err := r.Handle(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.sent) != 1 || f.groupCalls != 0 {
		t.Fatal("private lifecycle event caused provider mutation")
	}
	if err := r.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.deleted) != 0 {
		t.Fatal("live bot reply copy cleaned too early")
	}
	*now = now.Add(24 * time.Hour)
	if err := r.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.deleted) != 1 || f.deleted[0].Transport != "whatsapp" {
		t.Fatalf("cleanup=%+v", f.deleted)
	}
	if !strings.Contains(f.sent[0].text, "original") {
		t.Fatal("recipient copy changed")
	}
	b := f.inboxes["b"+bob]
	if err := r.Handle(ctx, input(b, bob, "late", "still visible", f.sent[0].id, *now)); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 2 || strings.HasPrefix(f.sent[1].text, "DM from ") {
		t.Fatal("expired message routed")
	}
}

func TestCommandsOverrideRepliesAndSeparateConversations(t *testing.T) {
	r, f, _, now, alice, bob := fixture(t, "discord", "telegram")
	ctx := context.Background()
	a, b := f.inboxes["a"+alice], f.inboxes["b"+bob]
	for _, id := range []string{"one", "two"} {
		if err := r.Handle(ctx, input(a, alice, id, "/dm @{Pale Poppy} "+id, "", *now)); err != nil {
			t.Fatal(err)
		}
	}
	m1, ok, _ := r.State.Reply(ctx, b, f.sent[0].id, *now)
	if !ok {
		t.Fatal("first mapping absent")
	}
	m2, ok, _ := r.State.Reply(ctx, b, f.sent[1].id, *now)
	if !ok || m1.ConversationID == m2.ConversationID {
		t.Fatal("conversations merged")
	}
	if err := r.Handle(ctx, input(b, bob, "reply", "answer to first", f.sent[0].id, *now)); err != nil {
		t.Fatal(err)
	}
	back, ok, _ := r.State.Reply(ctx, a, f.sent[2].id, *now)
	if !ok || back.ConversationID != m1.ConversationID {
		t.Fatal("native reply crossed conversations")
	}
	if err := r.Handle(ctx, input(a, alice, "new-command", "/dm @{Pale Poppy} fresh", f.sent[2].id, *now)); err != nil {
		t.Fatal(err)
	}
	newConversation, ok, _ := r.State.Reply(ctx, b, f.sent[3].id, *now)
	if !ok || newConversation.ConversationID == m1.ConversationID {
		t.Fatal("explicit command lost to reply context")
	}
}

func TestPrivateAddressingUsesAssignedAliasesCaseInsensitively(t *testing.T) {
	for _, form := range []string{"canonical", "lowercase"} {
		t.Run(form, func(t *testing.T) {
			r, f, _, now, alice, bob := fixture(t, "discord", "telegram")
			label, err := r.Sync.AliasByActor(context.Background(), "mesh", bob)
			if err != nil {
				t.Fatal(err)
			}
			if form == "lowercase" {
				label = strings.ToLower(label)
			}
			if err := r.Handle(context.Background(), input(f.inboxes["a"+alice], alice, "one", "/dm @{"+label+"} body", "", *now)); err != nil {
				t.Fatal(err)
			}
			if len(f.sent) != 1 || f.sent[0].inbox.ConnectionID != "to" || !strings.HasPrefix(f.sent[0].text, "DM from a/Amber Badger\n") {
				t.Fatal("assigned alias did not resolve")
			}
		})
	}
}

func TestHelpRepliesAttachmentsAndPlainContinuationDoNotRelay(t *testing.T) {
	r, f, _, now, alice, _ := fixture(t, "telegram", "discord")
	ctx := context.Background()
	a := f.inboxes["a"+alice]
	for i, text := range []string{"/start", "/help", "hello"} {
		if err := r.Handle(ctx, input(a, alice, fmt.Sprint(i), text, "", *now)); err != nil {
			t.Fatal(err)
		}
	}
	attachment := input(a, alice, "attachment", "caption", "", *now)
	attachment.Kind = "unsupported"
	if err := r.Handle(ctx, attachment); err != nil {
		t.Fatal(err)
	}
	if err := r.Handle(ctx, input(a, alice, "reply-help", "answer", f.sent[0].id, *now)); err != nil {
		t.Fatal(err)
	}
	for _, s := range f.sent {
		if s.inbox.ConnectionID != "from" || strings.HasPrefix(s.text, "DM from ") {
			t.Fatal("help or invalid contextual input reached third party")
		}
	}
	if len(f.sent) != 5 {
		t.Fatalf("expected five help responses, got %d", len(f.sent))
	}
}

func TestPrivateBodySanitizationAndAmbiguousSendNoRetry(t *testing.T) {
	r, f, _, now, alice, _ := fixture(t, "telegram", "discord")
	ctx := context.Background()
	a := f.inboxes["a"+alice]
	if err := r.Handle(ctx, input(a, alice, "one", "/dm @{Pale Poppy} hello @everyone <@12345> 15551234567@s.whatsapp.net https://t.me/private_user", "", *now)); err != nil {
		t.Fatal(err)
	}
	for _, sensitive := range []string{"@everyone", "12345", "15551234567", "private_user", "private-native-alice"} {
		if strings.Contains(f.sent[0].text, sensitive) {
			t.Fatalf("private output leaked mention/identity %q", sensitive)
		}
	}
	f.fail = transport.NewFailureWithCertainty(transport.FailureTransient, 0, transport.SendUnknown, errors.New("sensitive-provider-error"))
	_ = r.Handle(ctx, input(a, alice, "ambiguous", "/dm @{Pale Poppy} next", "", *now))
	if len(f.sent) != 3 || f.sent[1].inbox.ConnectionID != "to" || f.sent[2].inbox.ConnectionID != "from" || !strings.Contains(f.sent[2].text, "could not be confirmed") {
		t.Fatal("ambiguous send retried or claimed delivery")
	}
	if strings.Contains(f.sent[2].text, "sensitive-provider-error") {
		t.Fatal("provider error exposed")
	}
}

func TestRateLimitAndUnrelatedWhatsAppChat(t *testing.T) {
	r, f, _, now, alice, _ := fixture(t, "whatsapp", "discord")
	ctx := context.Background()
	a := f.inboxes["a"+alice]
	unknown := input(a, "u_unknown123", "unrelated", "personal conversation", "", *now)
	if err := r.Handle(ctx, unknown); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 0 {
		t.Fatal("unrelated personal chat was auto-answered")
	}
	for i := 0; i < 12; i++ {
		if err := r.Handle(ctx, input(a, alice, fmt.Sprint(i), "/help", "", *now)); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.sent) != 10 {
		t.Fatalf("fixed per-sender limit failed: %d", len(f.sent))
	}
}

func TestTrustedPNLIDAliasesPreserveObservedActorAndCanonicalChat(t *testing.T) {
	r, f, _, now, alice, bob := fixture(t, "whatsapp", "whatsapp")
	ctx := context.Background()
	a, b := f.inboxes["a"+alice], f.inboxes["b"+bob]
	start := input(a, "u_newlid1234", "first", "/dm @{Pale Poppy} hello", "", *now)
	start.ActorAliases = []string{alice}
	if err := r.Handle(ctx, start); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 1 || f.sent[0].inbox.ConnectionID != "to" {
		t.Fatal("historical observed PN actor did not start relay")
	}
	nativeLID := b
	nativeLID.Token = []byte("verified-alternate-lid-address")
	reply := input(nativeLID, "u_otherlid12", "second", "reply", f.sent[0].id, *now)
	reply.ActorAliases = []string{bob}
	if err := r.Handle(ctx, reply); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 2 || f.sent[1].inbox.ConnectionID != "from" {
		t.Fatal("canonical native chat with trusted alias did not return to original participant")
	}
	mapping, ok, err := r.State.Reply(ctx, a, f.sent[1].id, *now)
	if err != nil || !ok || mapping.Author.ActorID != bob || mapping.Recipient.ActorID != alice {
		t.Fatal("native identity reconciliation replaced endpoint-scoped actor identities")
	}
}

func TestConcurrentPeersAndSourceAliasStayWithinTargetSyncSet(t *testing.T) {
	r, f, cfg, now, alice, bob := fixture(t, "discord", "whatsapp")
	ctx := context.Background()
	a, b := f.inboxes["a"+alice], f.inboxes["b"+bob]
	charlie := r.Hasher.UserID("third-private-participant")
	settings := config.DefaultSyncSetSettings()
	settings.PrivateDMRelayEnabled = true
	cfg.Endpoints["c"] = config.Endpoint{Transport: config.TransportWhatsApp, ConnectionID: "third", RemoteID: "3@g.us"}
	cfg.Endpoints["z"] = config.Endpoint{Transport: config.TransportDiscord, ConnectionID: "from", RemoteID: "999"}
	cfg.Endpoints["0"] = config.Endpoint{Transport: config.TransportDiscord, ConnectionID: "from", RemoteID: "888"}
	cfg.SyncSets[0].Endpoints = append(cfg.SyncSets[0].Endpoints, "c", "z")
	cfg.SyncSets = append(cfg.SyncSets, config.SyncSet{ID: "other", Endpoints: []string{"0"}, Settings: &settings})
	if _, err := r.Sync.DB().Exec(`INSERT INTO sync_sets(id) VALUES('other'); INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id) VALUES('c','whatsapp','third','3@g.us','mesh'),('z','discord','from','999','mesh'),('0','discord','from','888','other')`); err != nil {
		t.Fatal(err)
	}
	for ep, actor := range map[string]string{"c": charlie, "z": alice, "0": alice} {
		if _, err := r.Sync.ObserveParticipant(ctx, ep, actor); err != nil {
			t.Fatal(err)
		}
		set, _ := findSet(cfg, ep)
		if _, err := r.Sync.ObserveSyncSetActor(ctx, set.ID, actor); err != nil {
			t.Fatal(err)
		}
		f.members[ep+actor] = true
	}
	c := transport.PrivateInbox{ConnectionID: "third", Transport: "whatsapp", OwnerID: "owner-third", ChatID: "third-chat", Token: []byte("third-native-address")}
	f.inboxes["c"+charlie] = c
	f.inboxes["z"+alice] = a
	f.inboxes["0"+alice] = a
	if err := r.Resolver.(*router.Router).UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	for i, target := range []string{bob, charlie} {
		label, err := r.Sync.AliasByActor(ctx, "mesh", target)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Handle(ctx, input(a, alice, fmt.Sprint("start", i), "/dm @{"+label+"} hello", "", *now)); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.sent) != 2 {
		t.Fatal("two peer conversations did not start")
	}
	if !strings.HasPrefix(f.sent[0].text, "DM from a/Amber Badger\n") || !strings.HasPrefix(f.sent[1].text, "DM from a/Amber Badger\n") {
		t.Fatal("source alias was selected outside the target set or without sorting")
	}
	if err := r.Handle(ctx, input(b, bob, "b-reply", "B response", f.sent[0].id, *now)); err != nil {
		t.Fatal(err)
	}
	if err := r.Handle(ctx, input(c, charlie, "c-reply", "C response", f.sent[1].id, *now)); err != nil {
		t.Fatal(err)
	}
	if err := r.Handle(ctx, input(a, alice, "to-c", "answer C", f.sent[3].id, *now)); err != nil {
		t.Fatal(err)
	}
	if err := r.Handle(ctx, input(a, alice, "to-b", "answer B", f.sent[2].id, *now)); err != nil {
		t.Fatal(err)
	}
	want := []string{"to", "third", "from", "from", "third", "to"}
	if len(f.sent) != len(want) {
		t.Fatal("conversation did not complete")
	}
	for i, connection := range want {
		if f.sent[i].inbox.ConnectionID != connection {
			t.Fatalf("message %d crossed peer conversations", i)
		}
	}
}

func TestPrivateSenderPresentation(t *testing.T) {
	for _, anonymous := range []bool{false, true} {
		t.Run(fmt.Sprint(anonymous), func(t *testing.T) {
			r, f, cfg, now, alice, _ := fixture(t, "telegram", "discord")
			ep := cfg.Endpoints["a"]
			ep.AnonymizeIncoming = anonymous
			cfg.Endpoints["a"] = ep
			in := input(f.inboxes["a"+alice], alice, "named", "/dm @{Pale Poppy} hello", "", *now)
			in.DisplayName = "Alice"
			if err := r.Handle(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			want := "a/Alice (Amber Badger)"
			if anonymous {
				want = "a/Amber Badger"
			}
			if len(f.sent) != 1 || f.options[0].Attribution != want || !strings.HasPrefix(f.sent[0].text, "DM from "+want+"\n") {
				t.Fatalf("presentation: %+v %+v", f.sent, f.options)
			}
		})
	}
}
