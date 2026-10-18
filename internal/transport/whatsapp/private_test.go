package whatsapp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/identity"
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
	return whatsmeow.SendResponse{ID: "private-out"}, nil
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
	result, err := a.SendPrivate(ctx, inbox, "anonymous body")
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) != 1 || fake.destinations[0] != pn || fake.sent[0].GetProtocolMessage() != nil || fake.sent[0].GetConversation() != "anonymous body" {
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
	a.handleEvent(&events.DeleteForMe{ChatJID: pn, MessageID: "private-out", IsFromMe: true})
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
