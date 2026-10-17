package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/vm75/message-sync/internal/transport"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

type privateCopyKey struct {
	Chat      string
	Message   string
	FromMe    bool
	Timestamp int64
}

type privateWhatsAppClient interface {
	IsLoggedIn() bool
	GetGroupInfo(context.Context, types.JID) (*types.GroupInfo, error)
	SendMessage(context.Context, types.JID, *waE2E.Message, ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error)
	SendAppState(context.Context, appstate.PatchInfo) error
}

func (a *Adapter) privateAPI() privateWhatsAppClient {
	if a.privateClient != nil {
		return a.privateClient
	}
	if a.client != nil {
		return a.client
	}
	return nil
}

func privateUserJID(jid types.JID) bool {
	return jid.User != "" && (jid.Server == types.DefaultUserServer || jid.Server == types.HiddenUserServer || jid.Server == types.HostedLIDServer)
}

func (a *Adapter) PrivateEvents() <-chan transport.PrivateIncoming { return a.privateEvents }

func (a *Adapter) privateOwnerID() string {
	a.mu.Lock()
	normalizer, client := a.normalizer, a.client
	a.mu.Unlock()
	if normalizer == nil || normalizer.hasher == nil || client == nil || client.Store == nil || client.Store.GetJID().IsEmpty() {
		return ""
	}
	return normalizer.hasher.ActorID("private-dm-owner", "whatsapp:"+client.Store.GetJID().ToNonAD().String())
}

func (a *Adapter) handlePrivateMessage(evt *events.Message) {
	if evt == nil || evt.Message == nil || evt.Info.IsGroup || evt.Info.IsFromMe || !privateUserJID(evt.Info.Chat) || evt.Info.ID == "" {
		return
	}
	kind, text, _, _ := normalizedPayload(evt.Message)
	if isWhatsAppEdit(evt) || kind == "edit" || kind == "delete" || kind == "revoke" || kind == "reaction" || evt.Message.GetProtocolMessage() != nil || evt.Message.GetSecretEncryptedMessage() != nil {
		return
	}
	if kind != "text" {
		kind = "unsupported"
	}
	a.mu.Lock()
	normalizer := a.normalizer
	a.mu.Unlock()
	if normalizer == nil || normalizer.hasher == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sender := evt.Info.Sender
	if sender.IsEmpty() {
		sender = evt.Info.Chat
	}
	canonical, phone := CanonicalSenderIdentity(ctx, sender, evt.Info.SenderAlt, a.resolveAltJID)
	if !privateUserJID(canonical) {
		return
	}
	chatJID := normalizeWhatsAppIdentityJID(evt.Info.Chat)
	if chatJID != normalizeWhatsAppIdentityJID(sender) && chatJID != normalizeWhatsAppIdentityJID(evt.Info.SenderAlt) {
		chatActor, _ := CanonicalSenderIdentity(ctx, chatJID, types.EmptyJID, a.resolveAltJID)
		if chatActor != canonical {
			return
		}
	}
	chat := normalizeWhatsAppIdentityJID(evt.Info.Chat).String()
	in := transport.PrivateIncoming{ConnectionID: a.connectionID, Transport: "whatsapp", Inbox: transport.PrivateInbox{ConnectionID: a.connectionID, Transport: "whatsapp", OwnerID: a.privateOwnerID(), ChatID: canonical.String(), Token: []byte(chat)}, ActorID: normalizer.hasher.UserID(canonical.String()), RemoteMessageID: string(evt.Info.ID), Text: text, Kind: kind, Timestamp: evt.Info.Timestamp}
	for _, jid := range []types.JID{sender, evt.Info.SenderAlt, canonical} {
		jid = normalizeWhatsAppIdentityJID(jid)
		if privateUserJID(jid) {
			in.ActorAliases = append(in.ActorAliases, normalizer.hasher.UserID(jid.String()))
		}
	}
	if phone != "" {
		in.ActorAliases = append(in.ActorAliases, normalizer.hasher.UserID(types.NewJID(phone, types.DefaultUserServer).String()))
	}
	if info := getContextInfo(evt.Message); info != nil {
		in.ReplyToMessageID = info.GetStanzaID()
	}
	token, _ := json.Marshal(privateCopyKey{Chat: chat, Message: string(evt.Info.ID), Timestamp: evt.Info.Timestamp.Unix()})
	in.LocalCopy = transport.PrivateCopy{ConnectionID: a.connectionID, Transport: "whatsapp", OwnerID: a.privateOwnerID(), Token: token}
	select {
	case a.privateEvents <- in:
	default:
	}
}

func (a *Adapter) privateMember(ctx context.Context, endpoint transport.EndpointID, actor string) (types.GroupParticipant, bool) {
	a.mu.Lock()
	client, target, normalizer := a.privateAPI(), a.targets[endpoint], a.normalizer
	a.mu.Unlock()
	if client == nil || normalizer == nil || !client.IsLoggedIn() || target.IsEmpty() {
		return types.GroupParticipant{}, false
	}
	group, err := client.GetGroupInfo(ctx, target)
	if err != nil || group == nil {
		return types.GroupParticipant{}, false
	}
	for _, p := range group.Participants {
		canonical, phone := CanonicalSenderIdentity(ctx, p.JID, p.LID, a.resolveAltJID)
		pn := types.EmptyJID
		if phone != "" {
			pn = types.NewJID(phone, types.DefaultUserServer)
		}
		for _, jid := range []types.JID{canonical, p.JID, p.LID, p.PhoneNumber, pn} {
			jid = normalizeWhatsAppIdentityJID(jid)
			if !jid.IsEmpty() && normalizer.hasher.UserID(jid.String()) == actor {
				return p, true
			}
		}
	}
	return types.GroupParticipant{}, false
}

func (a *Adapter) VerifyPrivateMember(ctx context.Context, endpoint transport.EndpointID, actor string) bool {
	_, ok := a.privateMember(ctx, endpoint, actor)
	return ok
}

func (a *Adapter) VerifyPrivateInboxMember(ctx context.Context, endpoint transport.EndpointID, actor string, inbox transport.PrivateInbox) bool {
	if inbox.ConnectionID != a.connectionID || inbox.Transport != "whatsapp" || inbox.OwnerID == "" || inbox.OwnerID != a.privateOwnerID() {
		return false
	}
	jid, err := types.ParseJID(string(inbox.Token))
	jid = normalizeWhatsAppIdentityJID(jid)
	if err != nil || !privateUserJID(jid) {
		return false
	}
	p, ok := a.privateMember(ctx, endpoint, actor)
	if !ok {
		return false
	}
	canonical, phone := CanonicalSenderIdentity(ctx, p.JID, p.LID, a.resolveAltJID)
	if inbox.ChatID != canonical.String() {
		return false
	}
	pn := types.EmptyJID
	if phone != "" {
		pn = types.NewJID(phone, types.DefaultUserServer)
	}
	for _, candidate := range []types.JID{canonical, p.JID, p.LID, p.PhoneNumber, pn} {
		if normalizeWhatsAppIdentityJID(candidate) == normalizeWhatsAppIdentityJID(jid) {
			return true
		}
	}
	return false
}

func (a *Adapter) ResolvePrivateInbox(ctx context.Context, endpoint transport.EndpointID, actor string) (transport.PrivateInbox, bool) {
	p, ok := a.privateMember(ctx, endpoint, actor)
	if !ok {
		return transport.PrivateInbox{}, false
	}
	// Only a PN from the current verified roster establishes a new direct
	// destination. A group-observed LID alone is never treated as a phone.
	for _, jid := range []types.JID{p.PhoneNumber, p.JID} {
		jid = jid.ToNonAD()
		if jid.Server == types.DefaultUserServer && jid.User != "" {
			canonical, _ := CanonicalSenderIdentity(ctx, p.JID, p.LID, a.resolveAltJID)
			return transport.PrivateInbox{ConnectionID: a.connectionID, Transport: "whatsapp", OwnerID: a.privateOwnerID(), ChatID: canonical.String(), Token: []byte(jid.String())}, true
		}
	}
	return transport.PrivateInbox{}, false
}

func (a *Adapter) SendPrivate(ctx context.Context, inbox transport.PrivateInbox, text string) (transport.PrivateSendResult, error) {
	client := a.privateAPI()
	if inbox.ConnectionID != a.connectionID || inbox.Transport != "whatsapp" || inbox.OwnerID == "" || inbox.OwnerID != a.privateOwnerID() || client == nil || !client.IsLoggedIn() {
		return transport.PrivateSendResult{}, errors.New("private WhatsApp connection unavailable")
	}
	jid, err := types.ParseJID(string(inbox.Token))
	jid = normalizeWhatsAppIdentityJID(jid)
	if err != nil || !privateUserJID(jid) || strings.ContainsAny(string(inbox.Token), "\r\n\x00") {
		return transport.PrivateSendResult{}, errors.New("invalid private WhatsApp inbox")
	}
	result, err := client.SendMessage(ctx, jid, &waE2E.Message{Conversation: proto.String(text)})
	if err != nil {
		return transport.PrivateSendResult{}, classifyWhatsAppFailure(err)
	}
	stamp := result.Timestamp
	if stamp.IsZero() {
		stamp = time.Now()
	}
	token, _ := json.Marshal(privateCopyKey{Chat: jid.String(), Message: string(result.ID), FromMe: true, Timestamp: stamp.Unix()})
	return transport.PrivateSendResult{RemoteMessageID: string(result.ID), LocalCopy: transport.PrivateCopy{ConnectionID: a.connectionID, Transport: "whatsapp", OwnerID: a.privateOwnerID(), Token: token}}, nil
}

// BuildPrivateDeleteForMePatch can affect only one message in a private chat.
// It uses the pinned whatsmeow account-local AppState operation, never a
// protocol REVOKE, SendMessage, clearChat, or deleteChat operation.
func BuildPrivateDeleteForMePatch(key privateCopyKey) (appstate.PatchInfo, error) {
	jid, err := types.ParseJID(key.Chat)
	jid = normalizeWhatsAppIdentityJID(jid)
	if err != nil || !privateUserJID(jid) || key.Message == "" || key.Timestamp <= 0 {
		return appstate.PatchInfo{}, errors.New("invalid private local-copy key")
	}
	fromMe := "0"
	if key.FromMe {
		fromMe = "1"
	}
	return appstate.PatchInfo{Type: appstate.WAPatchRegularHigh, Mutations: []appstate.MutationInfo{{Index: []string{appstate.IndexDeleteMessageForMe, jid.String(), key.Message, fromMe, "0"}, Version: 3, Value: &waSyncAction.SyncActionValue{DeleteMessageForMeAction: &waSyncAction.DeleteMessageForMeAction{DeleteMedia: proto.Bool(false), MessageTimestamp: proto.Int64(key.Timestamp)}}}}}, nil
}

func (a *Adapter) DeletePrivateCopyForMe(ctx context.Context, copy transport.PrivateCopy) error {
	client := a.privateAPI()
	if copy.ConnectionID != a.connectionID || copy.Transport != "whatsapp" || copy.OwnerID == "" || copy.OwnerID != a.privateOwnerID() || client == nil || !client.IsLoggedIn() {
		return errors.New("private local cleanup unavailable")
	}
	var key privateCopyKey
	if err := json.Unmarshal(copy.Token, &key); err != nil {
		return errors.New("invalid private local-copy key")
	}
	patch, err := BuildPrivateDeleteForMePatch(key)
	if err != nil {
		return err
	}
	if err := client.SendAppState(ctx, patch); err != nil {
		return errors.New("private local cleanup failed")
	}
	return nil
}
