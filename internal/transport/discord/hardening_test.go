package discord

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/transport"
)

func requestRetriesOnRateLimit(options []discordgo.RequestOption) bool {
	req, _ := http.NewRequest(http.MethodGet, "https://discord.invalid", nil)
	cfg := &discordgo.RequestConfig{Request: req}
	for _, option := range options {
		option(cfg)
	}
	return cfg.ShouldRetryOnRateLimit
}

type retryDiscordAPI struct {
	replyRetry  bool
	addRetry    bool
	removeRetry bool
}

func (f *retryDiscordAPI) ChannelMessages(string, int, string, string, string, ...discordgo.RequestOption) ([]*discordgo.Message, error) {
	return nil, nil
}

func (f *retryDiscordAPI) ChannelMessage(string, string, ...discordgo.RequestOption) (*discordgo.Message, error) {
	return nil, nil
}

func (f *retryDiscordAPI) ChannelMessageSendComplex(_ string, _ *discordgo.MessageSend, options ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.replyRetry = requestRetriesOnRateLimit(options)
	return &discordgo.Message{ID: "reply-marker"}, nil
}

func (f *retryDiscordAPI) MessageReactionAdd(_, _, _ string, options ...discordgo.RequestOption) error {
	f.addRetry = requestRetriesOnRateLimit(options)
	return nil
}

func (f *retryDiscordAPI) MessageReactionRemove(_, _, _, _ string, options ...discordgo.RequestOption) error {
	f.removeRetry = requestRetriesOnRateLimit(options)
	return nil
}

type retryWebhookAPI struct {
	channels    map[string][]*discordgo.Webhook
	listRetry   bool
	createRetry bool
	execRetry   bool
	editRetry   bool
	deleteRetry bool
}

func (f *retryWebhookAPI) ChannelWebhooks(channelID string, options ...discordgo.RequestOption) ([]*discordgo.Webhook, error) {
	f.listRetry = requestRetriesOnRateLimit(options)
	return f.channels[channelID], nil
}

func (f *retryWebhookAPI) WebhookCreate(channelID, name, _ string, options ...discordgo.RequestOption) (*discordgo.Webhook, error) {
	f.createRetry = requestRetriesOnRateLimit(options)
	webhook := &discordgo.Webhook{
		ID:        "managed-" + channelID,
		Type:      discordgo.WebhookTypeIncoming,
		ChannelID: channelID,
		User:      &discordgo.User{ID: "bridge-bot"},
		Name:      name,
		Token:     "transient-token",
	}
	f.channels[channelID] = append(f.channels[channelID], webhook)
	return webhook, nil
}

func (f *retryWebhookAPI) WebhookExecute(_ string, _ string, _ bool, _ *discordgo.WebhookParams, options ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.execRetry = requestRetriesOnRateLimit(options)
	return &discordgo.Message{ID: "created-message"}, nil
}

func (f *retryWebhookAPI) WebhookMessageEdit(_, _, messageID string, _ *discordgo.WebhookEdit, options ...discordgo.RequestOption) (*discordgo.Message, error) {
	f.editRetry = requestRetriesOnRateLimit(options)
	return &discordgo.Message{ID: messageID}, nil
}

func (f *retryWebhookAPI) WebhookMessageDelete(_, _, _ string, options ...discordgo.RequestOption) error {
	f.deleteRetry = requestRetriesOnRateLimit(options)
	return nil
}

type retryAdminAPI struct {
	guildRetry   bool
	channelRetry bool
}

func (f *retryAdminAPI) UserGuilds(_ int, _, _ string, _ bool, options ...discordgo.RequestOption) ([]*discordgo.UserGuild, error) {
	f.guildRetry = requestRetriesOnRateLimit(options)
	return []*discordgo.UserGuild{{ID: "guild-id", Name: "Transient Guild"}}, nil
}

func (f *retryAdminAPI) GuildChannels(_ string, options ...discordgo.RequestOption) ([]*discordgo.Channel, error) {
	f.channelRetry = requestRetriesOnRateLimit(options)
	return []*discordgo.Channel{{ID: testChannelID, Name: "transient-channel", Type: discordgo.ChannelTypeGuildText}}, nil
}

func TestDiscordRESTOperationsEnableRateLimitRetry(t *testing.T) {
	ctx := context.Background()
	api := &retryDiscordAPI{}
	adapter := &Adapter{
		api:               api,
		targets:           map[transport.EndpointID]string{"discord": testChannelID},
		reactionState:     make(map[reactionKey]string),
		suppressedDeletes: make(map[string]struct{}),
	}
	if err := sendNativeReplyMarker(ctx, api, testChannelID, "target-message"); err != nil {
		t.Fatal(err)
	}
	if err := adapter.React(ctx, transport.Reaction{Endpoint: "discord", TargetRemoteID: "target-message", Emoji: "👍"}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.React(ctx, transport.Reaction{Endpoint: "discord", TargetRemoteID: "target-message", Emoji: "🎉"}); err != nil {
		t.Fatal(err)
	}
	if !api.replyRetry || !api.addRetry || !api.removeRetry {
		t.Fatalf("Discord reply/reaction rate-limit retry flags: reply=%v add=%v remove=%v", api.replyRetry, api.addRetry, api.removeRetry)
	}

	webhookAPI := &retryWebhookAPI{channels: make(map[string][]*discordgo.Webhook)}
	manager := &managedWebhookClient{
		api:       webhookAPI,
		botUserID: func() string { return "bridge-bot" },
		hooks:     make(map[string]managedWebhookCredential),
		states:    make(map[string]WebhookStatus),
	}
	if err := manager.Prepare(ctx, []string{testChannelID}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Execute(ctx, testChannelID, WebhookMessage{Username: "Alice", Content: "body"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Edit(ctx, testChannelID, "created-message", "edited"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Delete(ctx, testChannelID, "created-message"); err != nil {
		t.Fatal(err)
	}
	if !webhookAPI.listRetry || !webhookAPI.createRetry || !webhookAPI.execRetry || !webhookAPI.editRetry || !webhookAPI.deleteRetry {
		t.Fatalf("Discord webhook rate-limit retry flags: list=%v create=%v execute=%v edit=%v delete=%v", webhookAPI.listRetry, webhookAPI.createRetry, webhookAPI.execRetry, webhookAPI.editRetry, webhookAPI.deleteRetry)
	}

	adminAPI := &retryAdminAPI{}
	adminAdapter := &Adapter{adminAPI: adminAPI, connected: true}
	if _, err := adminAdapter.DiscoverChannels(ctx); err != nil {
		t.Fatal(err)
	}
	if !adminAPI.guildRetry || !adminAPI.channelRetry {
		t.Fatalf("Discord discovery rate-limit retry flags: guilds=%v channels=%v", adminAPI.guildRetry, adminAPI.channelRetry)
	}
}

func TestDiscordGatewayConnectionStateTracksDisconnectAndReconnectSafely(t *testing.T) {
	var logs strings.Builder
	adapter := &Adapter{
		connected: true,
		logger:    slog.New(slog.NewTextHandler(&logs, nil)),
	}

	adapter.handleDisconnect(nil, &discordgo.Disconnect{})
	if got := adapter.AdminStatus(context.Background()); got.Connected || got.Status != "disconnected" {
		t.Fatalf("status after disconnect = %+v", got)
	}

	adapter.handleResumed(nil, &discordgo.Resumed{Trace: []string{"PRIVATE_RECONNECT_TRACE"}})
	if got := adapter.AdminStatus(context.Background()); !got.Connected || got.Status != "connected" {
		t.Fatalf("status after resume = %+v", got)
	}

	adapter.handleDisconnect(nil, &discordgo.Disconnect{})
	adapter.handleReady(nil, &discordgo.Ready{User: &discordgo.User{ID: "PRIVATE_USER_ID", Username: "PRIVATE_USERNAME"}})
	if got := adapter.AdminStatus(context.Background()); !got.Connected || got.Status != "connected" {
		t.Fatalf("status after ready = %+v", got)
	}

	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	if got := adapter.AdminStatus(context.Background()); got.Connected || got.Status != "disconnected" {
		t.Fatalf("status after close = %+v", got)
	}

	logged := logs.String()
	for _, want := range []string{"discord_disconnected", "discord_reconnected", "discord_connected"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("safe connection log missing %q: %s", want, logged)
		}
	}
	for _, forbidden := range []string{"PRIVATE_RECONNECT_TRACE", "PRIVATE_USER_ID", "PRIVATE_USERNAME"} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("gateway lifecycle log leaked raw event field %q: %s", forbidden, logged)
		}
	}
}
