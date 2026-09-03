package discord

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
)

func TestDiscordGatewayIntentsIncludeMessageReactions(t *testing.T) {
	intents := discordGatewayIntents()
	if intents&discordgo.IntentsGuildMessages == 0 {
		t.Fatal("guild message intent is missing")
	}
	if intents&discordgo.IntentsGuildMessageReactions == 0 {
		t.Fatal("guild message reactions intent is missing")
	}
	if intents&discordgo.IntentsGuilds == 0 {
		t.Fatal("guilds intent is missing")
	}
	if intents&discordgo.IntentsMessageContent == 0 {
		t.Fatal("message content intent is missing")
	}
	if intents&discordgo.IntentGuildMessagePolls == 0 {
		t.Fatal("guild poll intent is missing")
	}
	if intents&discordgo.IntentDirectMessagePolls != 0 {
		t.Fatal("DM poll intent must remain disabled")
	}
}

func TestDiscordTokenInjectionFromOptions(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()

	// Missing token returns error
	_, err := Open(ctx, Options{
		ConnectionID: "conn-dc-1",
		Token:        "",
		Logger:       logger,
	})
	if err == nil {
		t.Fatal("expected error when token is empty")
	}

	// Token with spaces is rejected if empty after trim
	_, err = Open(ctx, Options{
		ConnectionID: "conn-dc-1",
		Token:        "   ",
		Logger:       logger,
	})
	if err == nil {
		t.Fatal("expected error when token is whitespace")
	}
}

func TestDiscordAdapterEndpointOwnership(t *testing.T) {
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	ad := &Adapter{
		connectionID: "conn-dc-1",
		hasher:       hasher,
		targets:      make(map[transport.EndpointID]string),
	}

	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"d1": {Transport: config.TransportDiscord, ConnectionID: "conn-dc-1", RemoteID: "123456789012345671"},
			"d2": {Transport: config.TransportDiscord, ConnectionID: "conn-dc-1", RemoteID: "123456789012345672"},
			"d3": {Transport: config.TransportDiscord, ConnectionID: "conn-dc-2", RemoteID: "123456789012345673"},
			"wa": {Transport: config.TransportWhatsApp, ConnectionID: "conn-wa-1", RemoteID: "111@g.us"},
		},
		Identity: config.Identity{UsernameMode: config.UsernameModeHash},
	}

	if err := ad.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}

	ad.mu.RLock()
	defer ad.mu.RUnlock()
	if len(ad.targets) != 2 {
		t.Fatalf("expected exactly 2 targets for conn-dc-1, got: %d", len(ad.targets))
	}
	if ad.targets["d1"] != "123456789012345671" || ad.targets["d2"] != "123456789012345672" {
		t.Fatalf("unexpected targets for conn-dc-1: %+v", ad.targets)
	}
	if _, ok := ad.targets["d3"]; ok {
		t.Fatal("conn-dc-1 should not own d3 (owned by conn-dc-2)")
	}
}

func TestDiscordGoLoggerDropsRawProtocolErrorText(t *testing.T) {
	originalLogger := discordgo.Logger
	defer func() { discordgo.Logger = originalLogger }()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	installSafeDiscordLogger(logger)

	discordgo.Logger(discordgo.LogError, 0, "raw error %s", "private-user-id private-message-body")
	logged := logBuf.String()
	if !strings.Contains(logged, "discord_client_error") || !strings.Contains(logged, "error_kind") {
		t.Fatalf("safe Discord client error classification was not emitted: %s", logged)
	}
	if strings.Contains(logged, "private-user-id") || strings.Contains(logged, "private-message-body") || strings.Contains(logged, "raw error") {
		t.Fatal("Discord client logger leaked raw protocol error text")
	}
}

func TestAdapterEmitsOnlySafeStructuredFieldsWhenBufferFull(t *testing.T) {
	var logBuf bytes.Buffer
	adapter := &Adapter{
		events: make(chan transport.Incoming, 1),
		logger: slog.New(slog.NewJSONHandler(&logBuf, nil)),
	}
	adapter.events <- transport.Incoming{Endpoint: "already-buffered"}

	adapter.emit(transport.Incoming{
		Endpoint: "team-discord",
		RemoteID: "raw-discord-message-id",
		Sender: transport.Sender{
			DisplayName: "Private Discord Name",
			OpaqueID:    "u_privatehash",
		},
		Kind: "text",
		Text: "private Discord message body",
	})

	logged := logBuf.String()
	for _, required := range []string{"discord_ingress_dropped", "team-discord", "text", "buffer_full"} {
		if !strings.Contains(logged, required) {
			t.Fatalf("safe log is missing %q: %s", required, logged)
		}
	}
	for _, forbidden := range []string{"raw-discord-message-id", "Private Discord Name", "u_privatehash", "private Discord message body"} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("safe log leaked %q: %s", forbidden, logged)
		}
	}
}

func TestAdapterUpdateConfigUsesOnlyDiscordEndpoints(t *testing.T) {
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &Adapter{
		hasher: hasher,
		events: make(chan transport.Incoming, 1),
		logger: slog.Default(),
	}
	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"wa":      {Transport: config.TransportWhatsApp, RemoteID: "123456789@g.us"},
			"discord": {Transport: config.TransportDiscord, RemoteID: testChannelID},
		},
		Identity: config.Identity{UsernameMode: config.UsernameModeHash},
	}

	if err := adapter.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	discordEvent := testMessage()
	incoming, ok := adapter.normalizer.NormalizeMessage(discordEvent, "", nil)
	if !ok || incoming.Endpoint != "discord" {
		t.Fatalf("Discord endpoint was not installed; accepted=%v", ok)
	}

	waRemoteAsChannel := testMessage()
	waRemoteAsChannel.ChannelID = "123456789"
	if _, ok := adapter.normalizer.NormalizeMessage(waRemoteAsChannel, "", nil); ok {
		t.Fatal("WhatsApp endpoint was incorrectly installed as Discord target")
	}
}

func TestAdapterContract(t *testing.T) {
	var adapter transport.Adapter = &Adapter{}
	if adapter.Name() != "discord" {
		t.Fatalf("Name() = %q", adapter.Name())
	}
}

func TestHandleMessageCreateDropsBridgeBotWithoutLoggingProtocolData(t *testing.T) {
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	normalizer, err := NewNormalizer(map[string]string{"team-discord": testChannelID}, hasher, config.UsernameModePushName)
	if err != nil {
		t.Fatal(err)
	}

	var logBuf bytes.Buffer
	adapter := &Adapter{
		normalizer: normalizer,
		events:     make(chan transport.Incoming, 1),
		logger:     slog.New(slog.NewJSONHandler(&logBuf, nil)),
	}
	state := discordgo.NewState()
	state.User = &discordgo.User{ID: testAuthorID}
	session := &discordgo.Session{State: state}
	adapter.handleMessageCreate(session, testMessage())

	select {
	case <-adapter.events:
		t.Fatal("bridge bot message re-entered Discord ingress")
	default:
	}
	if logBuf.Len() != 0 {
		t.Fatalf("filtered bridge bot event produced a log: %s", logBuf.String())
	}
}

func TestConfiguredIngressChannelFlattensThreadsToParentAlias(t *testing.T) {
	normalizer := testNormalizer(t, config.UsernameModeHash)
	state := discordgo.NewState()
	if err := state.GuildAdd(&discordgo.Guild{ID: testGuildID}); err != nil {
		t.Fatal(err)
	}
	threadID := "133456789012345678"
	if err := state.ChannelAdd(&discordgo.Channel{
		ID:       threadID,
		GuildID:  testGuildID,
		ParentID: testChannelID,
		Type:     discordgo.ChannelTypeGuildPublicThread,
	}); err != nil {
		t.Fatal(err)
	}
	session := &discordgo.Session{State: state}

	routeChannelID := configuredIngressChannelID(session, normalizer, threadID)
	if routeChannelID != testChannelID {
		t.Fatalf("thread route = %q, want configured parent %q", routeChannelID, testChannelID)
	}

	evt := testMessage()
	evt.ChannelID = threadID
	evt.MessageReference = &discordgo.MessageReference{
		MessageID: "143456789012345678",
		ChannelID: threadID,
		GuildID:   testGuildID,
	}
	routed := routeDiscordMessage(evt.Message, routeChannelID)
	incoming, ok := normalizer.NormalizeMessage(&discordgo.MessageCreate{Message: routed}, "", nil)
	if !ok {
		t.Fatal("thread message was not accepted through configured parent")
	}
	if incoming.Endpoint != "team-discord" {
		t.Fatalf("endpoint = %q, want parent alias", incoming.Endpoint)
	}
	if incoming.ReplyTo == nil || incoming.ReplyTo.Endpoint != "team-discord" {
		t.Fatalf("thread reply did not retain parent alias: %#v", incoming.ReplyTo)
	}
	if len(normalizer.endpoints) != 1 {
		t.Fatalf("thread ingress mutated configured endpoints: %#v", normalizer.endpoints)
	}
	if _, exists := normalizer.endpoints[threadID]; exists {
		t.Fatal("thread id became a persisted/configured endpoint identity")
	}
}

func TestConfiguredIngressChannelRejectsUnconfiguredThreadParent(t *testing.T) {
	normalizer := testNormalizer(t, config.UsernameModeHash)
	state := discordgo.NewState()
	if err := state.GuildAdd(&discordgo.Guild{ID: testGuildID}); err != nil {
		t.Fatal(err)
	}
	threadID := "153456789012345678"
	if err := state.ChannelAdd(&discordgo.Channel{
		ID:       threadID,
		GuildID:  testGuildID,
		ParentID: "999999999999999999",
		Type:     discordgo.ChannelTypeGuildPrivateThread,
	}); err != nil {
		t.Fatal(err)
	}
	if got := configuredIngressChannelID(&discordgo.Session{State: state}, normalizer, threadID); got != "" {
		t.Fatalf("unconfigured thread parent unexpectedly routed as %q", got)
	}
}
