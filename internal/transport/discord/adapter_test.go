package discord

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
)

func TestLoadBotTokenFromEnvironment(t *testing.T) {
	t.Setenv("DISCORD_BOT_TOKEN_FILE", "")
	t.Setenv("DISCORD_BOT_TOKEN", "  environment-token  ")

	token, err := LoadBotToken()
	if err != nil {
		t.Fatal(err)
	}
	if token != "environment-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestLoadBotTokenFromSecretFile(t *testing.T) {
	t.Setenv("DISCORD_BOT_TOKEN", "")
	path := filepath.Join(t.TempDir(), "discord-token")
	if err := os.WriteFile(path, []byte("mounted-secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DISCORD_BOT_TOKEN_FILE", path)

	token, err := LoadBotToken()
	if err != nil {
		t.Fatal(err)
	}
	if token != "mounted-secret-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestLoadBotTokenRejectsMissingAndAmbiguousSources(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		t.Setenv("DISCORD_BOT_TOKEN", "")
		t.Setenv("DISCORD_BOT_TOKEN_FILE", "")
		if _, err := LoadBotToken(); err == nil {
			t.Fatal("expected missing token error")
		}
	})

	t.Run("both", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "discord-token")
		if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("DISCORD_BOT_TOKEN", "environment-token")
		t.Setenv("DISCORD_BOT_TOKEN_FILE", path)
		if _, err := LoadBotToken(); err == nil {
			t.Fatal("expected ambiguous token source error")
		}
	})
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
