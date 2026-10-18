package whatsapp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/controlstore"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/privatedm"
	"github.com/vm75/message-sync/internal/router"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/testutil"
	"github.com/vm75/message-sync/internal/transport"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waE2E"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

type privateWAClient struct {
	group        *types.GroupInfo
	sent         []*waE2E.Message
	destinations []types.JID
	patches      []appstate.PatchInfo
}

type privateLIDStore struct{ waStore.LIDStore }

func (*privateLIDStore) GetPNForLID(context.Context, types.JID) (types.JID, error) {
	return types.EmptyJID, nil
}
func (*privateLIDStore) GetLIDForPN(context.Context, types.JID) (types.JID, error) {
	return types.EmptyJID, nil
}

func (f *privateWAClient) IsLoggedIn() bool { return true }
func (f *privateWAClient) GetGroupInfo(context.Context, types.JID) (*types.GroupInfo, error) {
	return f.group, nil
}
func (f *privateWAClient) SendMessage(_ context.Context, j types.JID, m *waE2E.Message, _ ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	f.sent = append(f.sent, m)
	f.destinations = append(f.destinations, j)
	return whatsmeow.SendResponse{ID: types.MessageID(fmt.Sprintf("private-out-%d", len(f.sent)))}, nil
}
func (f *privateWAClient) SendAppState(_ context.Context, p appstate.PatchInfo) error {
	f.patches = append(f.patches, p)
	return nil
}

func TestPrivateWhatsAppCreatesRepliesAndStrictLocalCleanup(t *testing.T) {
	hasher, _ := identity.New([]byte("01234567890123456789012345678901"))
	pn := types.NewJID("15550000111", types.DefaultUserServer)
	lid := types.NewJID("999999", types.HiddenUserServer)
	fake := &privateWAClient{group: &types.GroupInfo{Participants: []types.GroupParticipant{{JID: lid, LID: lid, PhoneNumber: pn}}}}
	own := types.NewJID("15550000222", types.DefaultUserServer)
	a := &Adapter{connectionID: "owner", client: &whatsmeow.Client{Store: &waStore.Device{ID: &own, LIDs: &privateLIDStore{}}}, privateClient: fake, normalizer: &Normalizer{hasher: hasher}, targets: map[transport.EndpointID]types.JID{"group": types.NewJID("111", types.GroupServer)}, privateEvents: make(chan transport.PrivateIncoming, 10)}
	ctx := context.Background()
	actor := hasher.UserID(lid.String())
	inbox, ok := a.ResolvePrivateInbox(ctx, "group", actor)
	if !ok || string(inbox.Token) != pn.String() || !a.VerifyPrivateInboxMember(ctx, "group", actor, inbox) {
		t.Fatal("verified PN/LID private target was not resolved")
	}
	fake.group.Participants[0].PhoneNumber = types.EmptyJID
	if _, ok := a.ResolvePrivateInbox(ctx, "group", actor); ok {
		t.Fatal("LID-only roster exposed an unverified DM destination")
	}
	fake.group.Participants[0].PhoneNumber = pn
	evt := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: pn, Sender: pn, SenderAlt: lid}, ID: "private-in", Timestamp: time.Now()}, Message: &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("reply body"), ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("private-bot-parent")}}}}
	a.handleEvent(evt)
	var incoming transport.PrivateIncoming
	select {
	case incoming = <-a.PrivateEvents():
	default:
		t.Fatal("private create not emitted")
	}
	if incoming.ActorID != actor || incoming.ReplyToMessageID != "private-bot-parent" || incoming.Text != "reply body" {
		t.Fatalf("native private input=%+v", incoming)
	}
	if incoming.Inbox.ChatID != inbox.ChatID {
		t.Fatal("PN ingress and verified roster did not share canonical private identity")
	}
	lidReply := *evt
	lidReply.Info.Chat = lid
	lidReply.Info.Sender = lid
	lidReply.Info.SenderAlt = pn
	lidReply.Info.ID = "lid-private-in"
	a.handleEvent(&lidReply)
	select {
	case in := <-a.PrivateEvents():
		if in.Inbox.ChatID != inbox.ChatID || string(in.Inbox.Token) == string(inbox.Token) {
			t.Fatal("LID native reply failed canonical chat correlation")
		}
		matchedPN := false
		for _, alias := range in.ActorAliases {
			matchedPN = matchedPN || alias == hasher.UserID(pn.String())
		}
		if !matchedPN {
			t.Fatal("trusted historical PN actor proof lost")
		}
	default:
		t.Fatal("LID private reply missing")
	}
	result, err := a.SendPrivate(ctx, inbox, "DM from group/Amber Badger\nbody", transport.PrivateSendOptions{Attribution: "group/Amber Badger", ReplyToMessageID: "prior"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) != 1 || fake.destinations[0] != pn || fake.sent[0].GetProtocolMessage() != nil || fake.sent[0].GetExtendedTextMessage().GetText() != "DM from *_group/Amber Badger_*\nbody" || fake.sent[0].GetExtendedTextMessage().GetContextInfo().GetStanzaID() != "prior" {
		t.Fatal("private send used wrong destination or a lifecycle operation")
	}
	for _, copy := range []transport.PrivateCopy{incoming.LocalCopy, result.LocalCopy} {
		if err := a.DeletePrivateCopyForMe(ctx, copy); err != nil {
			t.Fatal(err)
		}
	}
	if len(fake.sent) != 1 || len(fake.patches) != 2 {
		t.Fatal("local cleanup sent a message/revoke instead of AppState")
	}
	for i, patch := range fake.patches {
		if patch.Type != appstate.WAPatchRegularHigh || len(patch.Mutations) != 1 {
			t.Fatal("unexpected cleanup patch")
		}
		mutation := patch.Mutations[0]
		if mutation.Index[0] != appstate.IndexDeleteMessageForMe || mutation.Index[1] != pn.String() || mutation.Index[4] != "0" || mutation.Value.GetDeleteMessageForMeAction() == nil || mutation.Value.GetDeleteChatAction() != nil || mutation.Value.GetClearChatAction() != nil {
			t.Fatal("cleanup was not a per-message Delete for Me")
		}
		want := "0"
		if i == 1 {
			want = "1"
		}
		if mutation.Index[3] != want {
			t.Fatal("inbound/outbound cleanup ownership lost")
		}
	}
	bad, _ := json.Marshal(privateCopyKey{Chat: "111@g.us", Message: "group-message", Timestamp: time.Now().Unix()})
	if a.DeletePrivateCopyForMe(ctx, transport.PrivateCopy{ConnectionID: "owner", Transport: "whatsapp", OwnerID: a.privateOwnerID(), Token: bad}) == nil {
		t.Fatal("group message accepted for private cleanup")
	}
	for _, edit := range []*events.Message{{Info: evt.Info, IsEdit: true, Message: &waE2E.Message{Conversation: proto.String("edited /dm")}}, {Info: evt.Info, Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_REVOKE.Enum()}}}} {
		a.handleEvent(edit)
	}
	echo := *evt
	echo.Info.IsFromMe = true
	a.handleEvent(&echo)
	a.handleEvent(&events.DeleteForMe{ChatJID: pn, MessageID: "private-out-1", IsFromMe: true})
	select {
	case <-a.PrivateEvents():
		t.Fatal("edit, revoke or cleanup echo became private input")
	default:
	}
	otherAccount := types.NewJID("15550000333", types.DefaultUserServer)
	a.client.Store.ID = &otherAccount
	if _, err := a.SendPrivate(ctx, inbox, "wrong account"); err == nil {
		t.Fatal("inbox silently moved to a different authenticated account")
	}
	if a.DeletePrivateCopyForMe(ctx, result.LocalCopy) == nil || len(fake.patches) != 2 {
		t.Fatal("old account cleanup was applied to new account")
	}
}

func TestGroupWhatsAppDMCommandKeepsGroupSemantics(t *testing.T) {
	hasher, _ := identity.New([]byte("01234567890123456789012345678901"))
	normalizer, _ := NewNormalizer(map[string]string{"group": "111@g.us"}, hasher)
	text := "/dm @{Amber Badger} group message"
	event := &events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: types.NewJID("111", types.GroupServer), Sender: types.NewJID("42", types.HiddenUserServer), IsGroup: true}, ID: "group-command", Timestamp: time.Now()}, Message: &waE2E.Message{Conversation: &text}}
	in, ok := normalizer.NormalizeMessage(event, true, 1024, nil, nil, nil, nil)
	if !ok || in.Endpoint != "group" || in.Kind != "text" || in.Text != text {
		t.Fatal("group /dm was intercepted or changed")
	}
}

// Exercise provider ingress and the real relay together: group traffic has
// already assigned the LID alias, but the private event contains only a PN.
func TestPrivateWhatsAppPNOnlyCommandUsesVerifiedRosterIdentity(t *testing.T) {
	ctx := context.Background()
	secret := []byte("01234567890123456789012345678901")
	hasher, _ := identity.New(secret)
	pn := types.NewJID("15550000111", types.DefaultUserServer)
	lid := types.NewJID("999999", types.HiddenUserServer)
	otherPN := types.NewJID("15550000333", types.DefaultUserServer)
	otherLID := types.NewJID("888888", types.HiddenUserServer)
	own := types.NewJID("15550000222", types.DefaultUserServer)
	settings := config.DefaultSyncSetSettings()
	settings.PrivateDMRelayEnabled = true
	cfg := &config.Config{PrivateDMRelayEnabled: true, Endpoints: map[string]config.Endpoint{
		"group": {ConnectionID: "owner", Transport: config.TransportWhatsApp, RemoteID: "111@g.us"},
	}, SyncSets: []config.SyncSet{{ID: "mesh", Endpoints: []string{"group"}, Settings: &settings}}}
	syncDB, err := store.Open(ctx, filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer syncDB.Close()
	testutil.SeedRouting(t, syncDB.DB(), cfg)
	var address string
	for _, jid := range []types.JID{lid, otherLID} {
		actor := hasher.UserID(jid.String())
		if _, err := syncDB.ObserveParticipant(ctx, "group", actor); err != nil {
			t.Fatal(err)
		}
		alias, err := syncDB.ObserveSyncSetActor(ctx, "mesh", actor)
		if err != nil {
			t.Fatal(err)
		}
		address = alias.Alias
	}
	control, err := controlstore.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	state, err := controlstore.NewPrivateDMStore(control.DB(), secret)
	if err != nil {
		t.Fatal(err)
	}
	fake := &privateWAClient{group: &types.GroupInfo{Participants: []types.GroupParticipant{
		{JID: lid, LID: lid, PhoneNumber: pn}, {JID: otherLID, LID: otherLID, PhoneNumber: otherPN},
	}}}
	a := &Adapter{connectionID: "owner", client: &whatsmeow.Client{Store: &waStore.Device{ID: &own, LIDs: &privateLIDStore{}}}, privateClient: fake, normalizer: &Normalizer{hasher: hasher}, targets: map[transport.EndpointID]types.JID{"group": types.NewJID("111", types.GroupServer)}, endpointSettings: map[string]config.SyncSetSettings{"group": settings}, privateEvents: make(chan transport.PrivateIncoming, 10)}
	mesh, err := router.NewWithHasher(cfg, syncDB, a, hasher)
	if err != nil {
		t.Fatal(err)
	}
	defer mesh.Close()
	relay := &privatedm.Relay{Sync: syncDB, State: state, Runtime: a, Resolver: mesh, Hasher: hasher, LoadConfig: func(context.Context) (*config.Config, error) { return cfg, nil }}
	a.handlePrivateMessage(&events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: pn, Sender: pn}, ID: "pn-command", Timestamp: time.Now()}, Message: &waE2E.Message{Conversation: proto.String("/dm @{" + address + "} hello")}})
	select {
	case in := <-a.PrivateEvents():
		if err := relay.Handle(ctx, in); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("private command not emitted")
	}
	if len(fake.sent) != 1 || fake.destinations[0] != otherPN || !strings.Contains(fake.sent[0].GetConversation(), "hello") && !strings.Contains(fake.sent[0].GetExtendedTextMessage().GetText(), "hello") {
		text := ""
		if len(fake.sent) > 0 {
			text = fake.sent[0].GetConversation()
		}
		t.Fatalf("PN-only command rejected instead of delivered: %s", text)
	}
	// Responses may also arrive PN-only. Preserve canonical inbox ownership
	// and reference the original author message on each hop.
	for _, turn := range []struct {
		jid             types.JID
		id, reply, text string
		destination     types.JID
		index           int
	}{
		{otherPN, "pn-reply", "private-out-1", "answer", pn, 1},
		{pn, "pn-third", "private-out-2", "next", otherPN, 2},
	} {
		a.handlePrivateMessage(&events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: turn.jid, Sender: turn.jid}, ID: types.MessageID(turn.id), Timestamp: time.Now()}, Message: &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String(turn.text), ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String(turn.reply)}}}})
		select {
		case in := <-a.PrivateEvents():
			if err := relay.Handle(ctx, in); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("PN-only response not emitted")
		}
		wantParent := "pn-command"
		if turn.index == 2 {
			wantParent = "pn-reply"
		}
		if len(fake.sent) != turn.index+1 || fake.destinations[turn.index] != turn.destination || fake.sent[turn.index].GetExtendedTextMessage().GetContextInfo().GetStanzaID() != wantParent {
			t.Fatal("PN-only response lost the native reply chain")
		}
	}
	var actors int
	if err := syncDB.DB().QueryRow("SELECT COUNT(*) FROM sync_set_actors").Scan(&actors); err != nil || actors != 2 {
		t.Fatalf("private traffic enrolled an actor: %d %v", actors, err)
	}

}

func TestPrivateWhatsAppRosterIdentityRequiresEnabledUniquePair(t *testing.T) {
	pn := types.NewJID("15550000111", types.DefaultUserServer)
	lid := types.NewJID("999999", types.HiddenUserServer)
	otherLID := types.NewJID("888888", types.HiddenUserServer)
	for _, tt := range []struct {
		name         string
		sender       types.JID
		participants []types.GroupParticipant
		disabled     bool
		want         types.JID
		ambiguous    bool
	}{
		{"PN-to-LID", pn, []types.GroupParticipant{{JID: lid, LID: lid, PhoneNumber: pn}}, false, lid, false},
		{"LID-to-PN", lid, []types.GroupParticipant{{JID: lid, LID: lid, PhoneNumber: pn}}, false, pn, false},
		{"PN-primary", pn, []types.GroupParticipant{{JID: pn, LID: lid}}, false, lid, false},
		{"disabled", pn, []types.GroupParticipant{{JID: lid, LID: lid, PhoneNumber: pn}}, true, types.EmptyJID, false},
		{"unrelated-PN", types.NewJID("15550000999", types.DefaultUserServer), []types.GroupParticipant{{JID: lid, LID: lid, PhoneNumber: pn}}, false, types.EmptyJID, false},
		{"no-PN-proof", pn, []types.GroupParticipant{{JID: lid, LID: lid}}, false, types.EmptyJID, false},
		{"no-LID-proof", pn, []types.GroupParticipant{{JID: pn, PhoneNumber: pn}}, false, types.EmptyJID, false},
		{"ambiguous-PN", pn, []types.GroupParticipant{{JID: lid, LID: lid, PhoneNumber: pn}, {JID: otherLID, LID: otherLID, PhoneNumber: pn}}, false, types.EmptyJID, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			settings := config.DefaultSyncSetSettings()
			settings.PrivateDMRelayEnabled = !tt.disabled
			a := &Adapter{privateClient: &privateWAClient{group: &types.GroupInfo{Participants: tt.participants}}, targets: map[transport.EndpointID]types.JID{"group": types.NewJID("111", types.GroupServer)}, endpointSettings: map[string]config.SyncSetSettings{"group": settings}}
			got, err := a.resolvePrivateAltJID(context.Background(), tt.sender)
			if got != tt.want || (err != nil) != tt.ambiguous {
				t.Fatalf("identity=%v error=%v", got, err)
			}
		})
	}
}
