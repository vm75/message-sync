package telegram

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	telegrambot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/vm75/message-sync/internal/transport"
)

func (a *Adapter) PrivateEvents() <-chan transport.PrivateIncoming { return a.privateEvents }

type privateInboxObservation struct {
	ChatID     int64
	ObservedAt time.Time
}

func (a *Adapter) privateOwnerID() string {
	a.mu.RLock()
	client, hasher := a.client, a.hasher
	a.mu.RUnlock()
	if client == nil || hasher == nil || client.ID() <= 0 {
		return ""
	}
	return hasher.ActorID("private-dm-owner", "telegram:"+strconv.FormatInt(client.ID(), 10))
}

func (a *Adapter) handlePrivateMessage(message *models.Message) {
	if message == nil || message.Chat.Type != models.ChatTypePrivate || message.From == nil || message.From.IsBot || message.From.ID <= 0 || message.Chat.ID != message.From.ID || message.ID <= 0 {
		return
	}
	now := time.Now()
	stamp := time.Unix(int64(message.Date), 0)
	if message.Date <= 0 || now.Sub(stamp) >= 24*time.Hour || stamp.After(now.Add(5*time.Minute)) {
		return
	}
	a.mu.Lock()
	if !a.privateDMEnabled || a.hasher == nil {
		a.mu.Unlock()
		return
	}
	actor := telegramActorID(a.hasher, message.From.ID)
	if a.privateInboxes == nil {
		a.privateInboxes = make(map[string]privateInboxObservation)
	}
	for actor, observation := range a.privateInboxes {
		if !observation.ObservedAt.Add(24 * time.Hour).After(now) {
			delete(a.privateInboxes, actor)
		}
	}
	if len(a.privateInboxes) < 10000 || a.privateInboxes[actor].ChatID != 0 {
		a.privateInboxes[actor] = privateInboxObservation{ChatID: message.Chat.ID, ObservedAt: now}
	}
	a.mu.Unlock()
	text, _ := normalizeTelegramMentions(message.Text, message.Entities, a.hasher)
	in := transport.PrivateIncoming{DisplayName: strings.TrimSpace(message.From.FirstName + " " + message.From.LastName), ConnectionID: a.connectionID, Transport: "telegram", Inbox: transport.PrivateInbox{ConnectionID: a.connectionID, Transport: "telegram", OwnerID: a.privateOwnerID(), ChatID: strconv.FormatInt(message.Chat.ID, 10), Token: []byte(strconv.FormatInt(message.Chat.ID, 10))}, ActorID: actor, RemoteMessageID: strconv.Itoa(message.ID), Kind: "text", Text: text, Timestamp: time.Unix(int64(message.Date), 0)}
	if message.Text == "" || message.Photo != nil || message.Document != nil || message.Video != nil || message.Audio != nil || message.Voice != nil || message.Sticker != nil || message.Poll != nil {
		in.Kind = "unsupported"
	}
	if reply := message.ReplyToMessage; reply != nil && reply.Chat.ID == message.Chat.ID {
		in.ReplyToMessageID = strconv.Itoa(reply.ID)
	}
	select {
	case a.privateEvents <- in:
	default:
	}
}

func (a *Adapter) privateMemberUser(ctx context.Context, endpoint transport.EndpointID, actor string) (int64, bool) {
	a.mu.RLock()
	userID := a.observedParticipants[endpoint][actor]
	if userID <= 0 {
		userID = a.privateInboxes[actor].ChatID
	}
	enabled := a.privateDMEnabled
	a.mu.RUnlock()
	chatID, api, _, _, ok := a.outboundState(endpoint)
	if !enabled || !ok || userID <= 0 {
		return 0, false
	}
	checker, ok := api.(interface {
		GetChatMember(context.Context, *telegrambot.GetChatMemberParams) (*models.ChatMember, error)
	})
	if !ok {
		return 0, false
	}
	member, err := checker.GetChatMember(ctx, &telegrambot.GetChatMemberParams{ChatID: chatID, UserID: userID})
	if err != nil || member == nil {
		return 0, false
	}
	var user *models.User
	switch {
	case member.Owner != nil:
		user = member.Owner.User
	case member.Administrator != nil:
		user = &member.Administrator.User
	case member.Member != nil:
		user = member.Member.User
	case member.Restricted != nil && member.Restricted.IsMember:
		user = member.Restricted.User
	}
	return userID, user != nil && user.ID == userID
}

func (a *Adapter) VerifyPrivateMember(ctx context.Context, endpoint transport.EndpointID, actor string) bool {
	_, ok := a.privateMemberUser(ctx, endpoint, actor)
	return ok
}

func (a *Adapter) VerifyPrivateInboxMember(ctx context.Context, endpoint transport.EndpointID, actor string, inbox transport.PrivateInbox) bool {
	if inbox.ConnectionID != a.connectionID || inbox.Transport != "telegram" || inbox.OwnerID == "" || inbox.OwnerID != a.privateOwnerID() || a.hasher == nil {
		return false
	}
	userID, err := strconv.ParseInt(string(inbox.Token), 10, 64)
	if err != nil || userID <= 0 || inbox.ChatID != string(inbox.Token) || telegramActorID(a.hasher, userID) != actor {
		return false
	}
	a.rememberCrossMentionParticipant(endpoint, actor, userID)
	return a.VerifyPrivateMember(ctx, endpoint, actor)
}

func (a *Adapter) ResolvePrivateInbox(ctx context.Context, endpoint transport.EndpointID, actor string) (transport.PrivateInbox, bool) {
	userID, ok := a.privateMemberUser(ctx, endpoint, actor)
	a.mu.RLock()
	observation := a.privateInboxes[actor]
	chatID := observation.ChatID
	a.mu.RUnlock()
	if !ok || chatID != userID || !observation.ObservedAt.Add(24*time.Hour).After(time.Now()) {
		return transport.PrivateInbox{}, false
	}
	return transport.PrivateInbox{ConnectionID: a.connectionID, Transport: "telegram", OwnerID: a.privateOwnerID(), ChatID: strconv.FormatInt(chatID, 10), Token: []byte(strconv.FormatInt(chatID, 10))}, true
}

func (a *Adapter) SendPrivate(ctx context.Context, inbox transport.PrivateInbox, text string, options ...transport.PrivateSendOptions) (transport.PrivateSendResult, error) {
	a.mu.RLock()
	client, enabled := a.client, a.privateDMEnabled
	a.mu.RUnlock()
	if !enabled || inbox.ConnectionID != a.connectionID || inbox.Transport != "telegram" || inbox.OwnerID == "" || inbox.OwnerID != a.privateOwnerID() {
		return transport.PrivateSendResult{}, errors.New("private Telegram connection unavailable")
	}
	chatID, err := strconv.ParseInt(string(inbox.Token), 10, 64)
	if err != nil || chatID <= 0 || inbox.ChatID != string(inbox.Token) {
		return transport.PrivateSendResult{}, errors.New("invalid private Telegram inbox")
	}
	api, ok := client.(interface {
		SendMessage(context.Context, *telegrambot.SendMessageParams) (*models.Message, error)
	})
	if !ok {
		return transport.PrivateSendResult{}, errors.New("private Telegram client unavailable")
	}
	// A single send attempt with explicit attribution entities and no previews.
	disablePreviews := true
	params := &telegrambot.SendMessageParams{ChatID: chatID, Text: text, LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: &disablePreviews}}
	if len(options) > 0 {
		o := options[0]
		if o.Attribution != "" {
			length := len(utf16.Encode([]rune(o.Attribution)))
			params.Entities = []models.MessageEntity{{Type: "bold", Offset: 8, Length: length}, {Type: "italic", Offset: 8, Length: length}}
		}
		if o.ReplyToMessageID != "" {
			id, err := strconv.Atoi(o.ReplyToMessageID)
			if err != nil || id <= 0 {
				return transport.PrivateSendResult{}, errors.New("invalid private reply ID")
			}
			params.ReplyParameters = &models.ReplyParameters{MessageID: id, AllowSendingWithoutReply: true}
		}
	}
	message, err := api.SendMessage(ctx, params)
	if err != nil {
		return transport.PrivateSendResult{}, classifyTelegramFailure(err)
	}
	if message == nil || message.ID <= 0 {
		return transport.PrivateSendResult{}, errors.New("private Telegram send unconfirmed")
	}
	return transport.PrivateSendResult{RemoteMessageID: strconv.Itoa(message.ID)}, nil
}

func (a *Adapter) DeletePrivateCopyForMe(context.Context, transport.PrivateCopy) error {
	// Bot API deleteMessage deletes the participant-visible copy too.
	return errors.New("private local cleanup unsupported")
}
