package telegram

import (
	"context"
	"testing"
	"time"

	telegrambot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/gotd/td/tg"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
)

type privateTelegramAPI struct {
	*crossMentionAPI
	sent *telegrambot.SendMessageParams
}

func (f *privateTelegramAPI) SendMessage(ctx context.Context, p *telegrambot.SendMessageParams) (*models.Message, error) {
	f.sent = p
	return f.fakeTelegramAPI.SendMessage(ctx, p)
}

func TestPrivateTelegramStartReachabilityReplyAndNoDelete(t *testing.T) {
	hasher, _ := identity.New([]byte("01234567890123456789012345678901"))
	normalizer, _ := NewNormalizerWithConnection(map[string]string{"group": "-123"}, hasher, config.UsernameModePushName, "owner")
	api := &privateTelegramAPI{crossMentionAPI: &crossMentionAPI{fakeTelegramAPI: &fakeTelegramAPI{}}}
	a := &Adapter{connectionID: "owner", privateDMEnabled: true, privateEvents: make(chan transport.PrivateIncoming, 5), events: make(chan transport.Incoming, 10), hasher: hasher, normalizer: normalizer, client: api}
	actor := telegramActorID(hasher, 123)
	a.rememberCrossMentionParticipant("group", actor, 123)
	if _, ok := a.ResolvePrivateInbox(context.Background(), "group", actor); ok {
		t.Fatal("bot initiated a DM before private bootstrap")
	}
	message := &models.Message{ID: 1, Date: int(time.Now().Unix()), Chat: models.Chat{ID: 123, Type: models.ChatTypePrivate}, From: &models.User{ID: 123}, Text: "/start"}
	a.handleUpdate(context.Background(), nil, &models.Update{ID: 1, Message: message})
	in := <-a.PrivateEvents()
	if in.Text != "/start" || in.Kind != "text" {
		t.Fatal("/start was not routed to private help")
	}
	inbox, ok := a.ResolvePrivateInbox(context.Background(), "group", actor)
	if !ok || string(inbox.Token) != "123" {
		t.Fatal("started bot was not privately reachable")
	}
	result, err := a.SendPrivate(context.Background(), inbox, "anonymous body")
	if err != nil || result.RemoteMessageID == "" {
		t.Fatal("private Bot API send failed")
	}
	if api.sent.ChatID != int64(123) || api.sent.ParseMode != "" || api.sent.LinkPreviewOptions == nil || api.sent.LinkPreviewOptions.IsDisabled == nil || !*api.sent.LinkPreviewOptions.IsDisabled || len(api.sent.Entities) != 0 {
		t.Fatal("private message used group delivery, markup, pings or previews")
	}
	reply := *message
	reply.ID = 2
	reply.Text = "answer"
	reply.ReplyToMessage = &models.Message{ID: 101, Chat: message.Chat}
	a.handleUpdate(context.Background(), nil, &models.Update{ID: 2, Message: &reply})
	if in := <-a.PrivateEvents(); in.ReplyToMessageID != "101" {
		t.Fatal("structured native reply ID lost")
	}
	a.handleUpdate(context.Background(), nil, &models.Update{ID: 3, EditedMessage: &reply})
	select {
	case <-a.PrivateEvents():
		t.Fatal("private edited command was reprocessed")
	default:
	}
	if a.DeletePrivateCopyForMe(context.Background(), transport.PrivateCopy{Transport: "telegram"}) == nil || api.deleted != nil {
		t.Fatal("Bot API cleanup deleted shared message")
	}
	if _, ok := any(&MTProtoAdapter{}).(transport.PrivateEventsSource); ok {
		t.Fatal("non-managed MTProto personal chats were intercepted")
	}
	observation := a.privateInboxes[actor]
	observation.ObservedAt = time.Now().Add(-24 * time.Hour)
	a.privateInboxes[actor] = observation
	if _, ok := a.ResolvePrivateInbox(context.Background(), "group", actor); ok {
		t.Fatal("expired in-memory Bot API inbox bypassed re-observation TTL")
	}
	for len(a.events) > 0 {
		event := <-a.events
		if event.Endpoint != "" || event.Text != "" || event.Kind != "other" {
			t.Fatal("private content entered group ingress")
		}
	}
}

func TestGroupTelegramDMCommandKeepsGroupSemantics(t *testing.T) {
	hasher, _ := identity.New([]byte("01234567890123456789012345678901"))
	normalizer, _ := NewNormalizerWithConnection(map[string]string{"group": "-123"}, hasher, config.UsernameModeHash, "owner")
	text := "/dm @{other/P1} group message"
	in, ok := normalizer.NormalizeMessage(&models.Message{ID: 1, Date: int(time.Now().Unix()), Chat: models.Chat{ID: -123, Type: models.ChatTypeGroup}, From: &models.User{ID: 123}, Text: text}, 999)
	if !ok || in.Endpoint != "group" || in.Kind != "text" || in.Text != text {
		t.Fatal("group /dm was intercepted or changed")
	}
}

func TestUnmanagedMTProtoPersonalChatsRemainUntouched(t *testing.T) {
	live := &fakeMTLiveAuth{groups: []mtprotoGroup{{Peer: mtprotoPeerState{RemoteID: "-77", Kind: "chat", ID: 77}, Title: "Group"}}}
	adapter, _ := newMTProtoLiveAdapter(t, "mt-private", "-77", live)
	defer adapter.Close()
	for i, text := range []string{"personal hello", "/help", "/dm @{tg/P1} text"} {
		adapter.normalizeMTProtoMessage(context.Background(), tg.Entities{Users: map[int64]*tg.User{7: {ID: 7, FirstName: "Private"}}}, &tg.Message{ID: i + 1, PeerID: &tg.PeerUser{UserID: 7}, FromID: &tg.PeerUser{UserID: 7}, Date: int(time.Now().Unix()), Message: text}, false)
	}
	select {
	case <-adapter.Events():
		t.Fatal("personal MTProto message entered group routing")
	default:
	}
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.lastText != "" || live.deletedID != 0 || live.editText != "" {
		t.Fatal("personal MTProto chat was auto-answered or changed")
	}
}
