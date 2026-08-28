package discord

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/router"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/transport"
)

type e2eDiscordAPI struct {
	channels     map[string][]*discordgo.Webhook
	createCalls  int
	executeCalls int
	nextMessage  int
	messages     []WebhookMessage
	replyTargets []string
	edits        []string
	deletes      []string
	reactions    []string
}

func (f *e2eDiscordAPI) ChannelWebhooks(channelID string, _ ...discordgo.RequestOption) ([]*discordgo.Webhook, error) {
	return f.channels[channelID], nil
}

func (f *e2eDiscordAPI) WebhookCreate(channelID, name, _ string, _ ...discordgo.RequestOption) (*discordgo.Webhook, error) {
	f.createCalls++
	webhook := &discordgo.Webhook{
		ID:        "managed-" + channelID,
		Type:      discordgo.WebhookTypeIncoming,
		ChannelID: channelID,
		User:      &discordgo.User{ID: "bridge-bot"},
		Name:      name,
		Token:     "PRIVATE_WEBHOOK_TOKEN_SENTINEL",
	}
	f.channels[channelID] = append(f.channels[channelID], webhook)
	return webhook, nil
}

func (f *e2eDiscordAPI) WebhookExecute(_ string, _ string, _ bool, data *discordgo.WebhookParams, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.executeCalls++
	f.nextMessage++
	message := WebhookMessage{Username: data.Username, Content: data.Content}
	if len(data.Files) > 0 && data.Files[0] != nil {
		message.File = &WebhookFile{Name: data.Files[0].Name, ContentType: data.Files[0].ContentType}
	}
	f.messages = append(f.messages, message)
	return &discordgo.Message{ID: fmt.Sprintf("discord-copy-%d", f.nextMessage)}, nil
}

func (f *e2eDiscordAPI) WebhookMessageEdit(_ string, _ string, messageID string, data *discordgo.WebhookEdit, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	content := ""
	if data != nil && data.Content != nil {
		content = *data.Content
	}
	f.edits = append(f.edits, messageID+"|"+content)
	return &discordgo.Message{ID: messageID}, nil
}

func (f *e2eDiscordAPI) WebhookMessageDelete(_ string, _ string, messageID string, _ ...discordgo.RequestOption) error {
	f.deletes = append(f.deletes, messageID)
	return nil
}

func (f *e2eDiscordAPI) ChannelMessage(_ string, _ string, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	return nil, nil
}

func (f *e2eDiscordAPI) ChannelMessageSendComplex(_ string, data *discordgo.MessageSend, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	if data != nil && data.Reference != nil {
		f.replyTargets = append(f.replyTargets, data.Reference.MessageID)
	}
	return &discordgo.Message{ID: "reply-marker"}, nil
}

func (f *e2eDiscordAPI) MessageReactionAdd(_ string, messageID, emojiID string, _ ...discordgo.RequestOption) error {
	f.reactions = append(f.reactions, messageID+"|"+emojiID)
	return nil
}

func (f *e2eDiscordAPI) MessageReactionRemove(_ string, messageID, emojiID, _ string, _ ...discordgo.RequestOption) error {
	f.reactions = append(f.reactions, messageID+"|-"+emojiID)
	return nil
}

type e2eWhatsAppOutbound struct {
	nextID    int
	sent      []transport.Outgoing
	edits     []transport.MessageRef
	deletes   []transport.MessageRef
	reactions []transport.Reaction
}

func (f *e2eWhatsAppOutbound) Send(_ context.Context, outgoing transport.Outgoing) (transport.MessageRef, error) {
	f.nextID++
	f.sent = append(f.sent, outgoing)
	return transport.MessageRef{Endpoint: outgoing.Endpoint, RemoteMessageID: fmt.Sprintf("wa-copy-%d", f.nextID), IsTargetFromMe: true}, nil
}

func (f *e2eWhatsAppOutbound) React(_ context.Context, reaction transport.Reaction) error {
	f.reactions = append(f.reactions, reaction)
	return nil
}

func (f *e2eWhatsAppOutbound) Edit(_ context.Context, ref transport.MessageRef, _ string) error {
	f.edits = append(f.edits, ref)
	return nil
}

func (f *e2eWhatsAppOutbound) Delete(_ context.Context, ref transport.MessageRef) error {
	f.deletes = append(f.deletes, ref)
	return nil
}

func TestMixedTransportWebhookSenderRenderingAndCanonicalLifecycle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sync.db")
	syncStore, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"wa-one":  {Transport: config.TransportWhatsApp, RemoteID: "111@g.us"},
			"discord": {Transport: config.TransportDiscord, RemoteID: testChannelID},
			"wa-two":  {Transport: config.TransportWhatsApp, RemoteID: "222@g.us"},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Groups: []string{"wa-one", "discord", "wa-two"}}},
		Identity: config.Identity{UsernameMode: config.UsernameModePushName},
	}

	discordAPI := &e2eDiscordAPI{channels: make(map[string][]*discordgo.Webhook)}
	webhook := &managedWebhookClient{
		api:       discordAPI,
		botUserID: func() string { return "bridge-bot" },
		hooks:     make(map[string]managedWebhookCredential),
		states:    make(map[string]WebhookStatus),
	}
	if err := webhook.Prepare(ctx, []string{testChannelID}); err != nil {
		t.Fatal(err)
	}
	if err := webhook.Prepare(ctx, []string{testChannelID}); err != nil {
		t.Fatal(err)
	}
	if discordAPI.createCalls != 1 {
		t.Fatalf("managed webhook create calls=%d, want exactly one shared channel webhook", discordAPI.createCalls)
	}

	discordAdapter := &Adapter{
		api:               discordAPI,
		webhook:           webhook,
		targets:           map[transport.EndpointID]string{"discord": testChannelID},
		reactionState:     make(map[reactionKey]string),
		suppressedDeletes: make(map[string]struct{}),
	}
	whatsAppAdapter := &e2eWhatsAppOutbound{}
	registry, err := router.NewAdapterRegistry(cfg, map[config.Transport]router.OutboundAdapter{
		config.TransportWhatsApp: whatsAppAdapter,
		config.TransportDiscord:  discordAdapter,
	})
	if err != nil {
		t.Fatal(err)
	}
	mesh, err := router.New(cfg, syncStore, registry)
	if err != nil {
		t.Fatal(err)
	}

	messages := []transport.Incoming{
		{
			Endpoint: "wa-one", RemoteID: "wa-source-1", Kind: "text",
			Sender: transport.Sender{DisplayName: "Vidhya Private", OpaqueID: "u_abcde23456"},
			Text:   "PRIVATE_BODY_ONE", Timestamp: time.Unix(1_700_000_000, 0).UTC(),
		},
		{
			Endpoint: "wa-one", RemoteID: "wa-source-2", Kind: "text",
			Sender: transport.Sender{DisplayName: "Ravi Private", OpaqueID: "u_bcdef23456"},
			Text:   "PRIVATE_BODY_TWO", Timestamp: time.Unix(1_700_000_001, 0).UTC(),
		},
		{
			Endpoint: "wa-one", RemoteID: "wa-source-3", Kind: "text",
			Sender: transport.Sender{OpaqueID: "u_cdefg23456"},
			Text:   "PRIVATE_BODY_FALLBACK", Timestamp: time.Unix(1_700_000_002, 0).UTC(),
		},
	}
	for _, incoming := range messages {
		if err := mesh.Handle(ctx, incoming); err != nil {
			t.Fatal(err)
		}
	}
	if discordAPI.executeCalls != 3 || len(discordAPI.messages) != 3 {
		t.Fatalf("Discord webhook sends=%d messages=%d, want 3", discordAPI.executeCalls, len(discordAPI.messages))
	}
	wantUsers := []string{"Vidhya Private", "Ravi Private", "u_cdefg23456"}
	for i, want := range wantUsers {
		if got := discordAPI.messages[i].Username; got != want {
			t.Fatalf("webhook username[%d]=%q, want %q", i, got, want)
		}
		if discordAPI.messages[i].Username == "message-sync" {
			t.Fatal("WhatsApp participant was rendered under one generic Discord bot identity")
		}
	}
	if discordAPI.messages[0].Username == discordAPI.messages[1].Username {
		t.Fatal("distinct WhatsApp participants did not render as distinct Discord APP usernames")
	}
	if discordAPI.messages[0].Content != "PRIVATE_BODY_ONE" || strings.Contains(discordAPI.messages[0].Content, "Vidhya Private") {
		t.Fatalf("sender identity was not kept separate from webhook message body: %#v", discordAPI.messages[0])
	}
	if discordAPI.createCalls != 1 {
		t.Fatal("per-user webhooks were created for WhatsApp participants")
	}

	if err := mesh.Handle(ctx, messages[0]); err != nil {
		t.Fatal(err)
	}
	if discordAPI.executeCalls != 3 {
		t.Fatalf("duplicate ingress created another Discord copy: sends=%d", discordAPI.executeCalls)
	}

	canonicalID, err := syncStore.CanonicalForRemote(ctx, "wa-one", "wa-source-1")
	if err != nil {
		t.Fatal(err)
	}
	discordCopy, err := syncStore.MessageCopyForEndpoint(ctx, canonicalID, "discord")
	if err != nil {
		t.Fatal(err)
	}
	if discordCopy.RemoteMessageID != "discord-copy-1" {
		t.Fatalf("canonical Discord copy=%q, want webhook-created message id", discordCopy.RemoteMessageID)
	}

	reply := transport.Incoming{
		Endpoint: "wa-one", RemoteID: "wa-reply-1", Kind: "text",
		Sender: transport.Sender{DisplayName: "Vidhya Private", OpaqueID: "u_abcde23456"},
		Text:   "PRIVATE_REPLY_BODY", QuotedText: "PRIVATE_BODY_ONE",
		ReplyTo:   &transport.MessageRef{Endpoint: "wa-one", RemoteMessageID: "wa-source-1"},
		Timestamp: time.Unix(1_700_000_003, 0).UTC(),
	}
	if err := mesh.Handle(ctx, reply); err != nil {
		t.Fatal(err)
	}
	if len(discordAPI.replyTargets) != 1 || discordAPI.replyTargets[0] != "discord-copy-1" {
		t.Fatalf("Discord reply target=%#v, want webhook-created canonical copy", discordAPI.replyTargets)
	}
	if got := discordAPI.messages[len(discordAPI.messages)-1].Username; got != "Vidhya Private" {
		t.Fatalf("reply lost sender-specific APP username: %q", got)
	}

	edit := transport.Incoming{
		Endpoint: "wa-one", RemoteID: "wa-edit-1", Kind: "edit",
		Sender:    transport.Sender{DisplayName: "Vidhya Private", OpaqueID: "u_abcde23456"},
		Text:      "PRIVATE_EDIT_BODY",
		ReplyTo:   &transport.MessageRef{Endpoint: "wa-one", RemoteMessageID: "wa-source-1"},
		Timestamp: time.Unix(1_700_000_004, 0).UTC(),
	}
	if err := mesh.Handle(ctx, edit); err != nil {
		t.Fatal(err)
	}
	if len(discordAPI.edits) != 1 || !strings.HasPrefix(discordAPI.edits[0], "discord-copy-1|") {
		t.Fatalf("Discord edit did not target webhook-created canonical copy: %#v", discordAPI.edits)
	}

	reaction := transport.Incoming{
		Endpoint: "wa-one", RemoteID: "wa-reaction-1", Kind: "reaction",
		Sender: transport.Sender{OpaqueID: "u_abcde23456"}, Text: "👍",
		ReplyTo:   &transport.MessageRef{Endpoint: "wa-one", RemoteMessageID: "wa-source-1"},
		Timestamp: time.Unix(1_700_000_005, 0).UTC(),
	}
	if err := mesh.Handle(ctx, reaction); err != nil {
		t.Fatal(err)
	}
	if len(discordAPI.reactions) != 1 || discordAPI.reactions[0] != "discord-copy-1|👍" {
		t.Fatalf("Discord reaction did not target webhook-created canonical copy: %#v", discordAPI.reactions)
	}

	deleteEvent := transport.Incoming{
		Endpoint: "wa-one", RemoteID: "wa-delete-1", Kind: "delete",
		ReplyTo:   &transport.MessageRef{Endpoint: "wa-one", RemoteMessageID: "wa-source-1"},
		Timestamp: time.Unix(1_700_000_006, 0).UTC(),
	}
	if err := mesh.Handle(ctx, deleteEvent); err != nil {
		t.Fatal(err)
	}
	if len(discordAPI.deletes) != 1 || discordAPI.deletes[0] != "discord-copy-1" {
		t.Fatalf("Discord delete did not target webhook-created canonical copy: %#v", discordAPI.deletes)
	}

	beforeDiscordIngress := len(whatsAppAdapter.sent)
	if err := mesh.Handle(ctx, transport.Incoming{
		Endpoint: "discord", RemoteID: "discord-source-external", Kind: "text",
		Sender: transport.Sender{OpaqueID: "u_defgh23456"}, Text: "PRIVATE_DISCORD_BODY",
		Timestamp: time.Unix(1_700_000_007, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if got := len(whatsAppAdapter.sent) - beforeDiscordIngress; got != 2 {
		t.Fatalf("Discord ingress fanout to WhatsApp endpoints=%d, want 2", got)
	}

	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	normalizer, err := NewNormalizer(map[string]string{"discord": testChannelID}, hasher, config.UsernameModeHash)
	if err != nil {
		t.Fatal(err)
	}
	managedEcho := testMessage()
	managedEcho.WebhookID = "managed-" + testChannelID
	managedEcho.Author = &discordgo.User{ID: "923456789012345678"}
	if _, ok := normalizer.NormalizeMessage(managedEcho, "bridge-bot", webhook); ok {
		t.Fatal("bridge-managed webhook copy re-entered Discord ingress")
	}

	if err := syncStore.Close(); err != nil {
		t.Fatal(err)
	}
	dbBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"Vidhya Private",
		"Ravi Private",
		"PRIVATE_BODY_ONE",
		"PRIVATE_BODY_TWO",
		"PRIVATE_BODY_FALLBACK",
		"PRIVATE_REPLY_BODY",
		"PRIVATE_EDIT_BODY",
		"PRIVATE_DISCORD_BODY",
		"PRIVATE_WEBHOOK_TOKEN_SENTINEL",
	} {
		if strings.Contains(string(dbBytes), forbidden) {
			t.Fatalf("sync.db persisted forbidden transient Discord/WhatsApp value %q", forbidden)
		}
	}
}
