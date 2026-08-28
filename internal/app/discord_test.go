package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/transport"
	discord "github.com/vm75/message-sync/internal/transport/discord"
	whatsapp "github.com/vm75/message-sync/internal/transport/whatsapp"
)

type fakeDiscordTransport struct {
	events    chan transport.Incoming
	mu        sync.Mutex
	closed    bool
	sent      []transport.Outgoing
	sentCount int
}

func (f *fakeDiscordTransport) Events() <-chan transport.Incoming { return f.events }

func (f *fakeDiscordTransport) Send(_ context.Context, outgoing transport.Outgoing) (transport.MessageRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sentCount++
	f.sent = append(f.sent, outgoing)
	return transport.MessageRef{
		Endpoint:        outgoing.Endpoint,
		RemoteMessageID: fmt.Sprintf("discord-sent-%d", f.sentCount),
		IsTargetFromMe:  true,
	}, nil
}

func (f *fakeDiscordTransport) React(context.Context, transport.Reaction) error { return nil }
func (f *fakeDiscordTransport) Edit(context.Context, transport.MessageRef, string) error {
	return nil
}
func (f *fakeDiscordTransport) Delete(context.Context, transport.MessageRef) error { return nil }

func (f *fakeDiscordTransport) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeDiscordTransport) UpdateConfig(*config.Config) error { return nil }

func TestRunStartsAndStopsDiscordGatewayWhenConfigured(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("API_ADDR", "127.0.0.1:0")
	t.Setenv("IDENTITY_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("DISCORD_BOT_TOKEN", "test-discord-token")
	t.Setenv("DISCORD_BOT_TOKEN_FILE", "")

	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"wa":      {Transport: config.TransportWhatsApp, RemoteID: "123456789@g.us"},
			"discord": {Transport: config.TransportDiscord, RemoteID: "123456789012345678"},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Groups: []string{"wa", "discord"}}},
		Identity: config.Identity{UsernameMode: config.UsernameModeHash},
		Media:    config.Media{MaxSizeMB: 100},
		Recovery: config.Recovery{MaxAgeHours: 24, MaxMessagesPerGroup: 200},
		Storage:  config.Storage{MessageRetentionDays: 90},
	}

	wa := &fakeWhatsAppTransport{events: make(chan transport.Incoming, 1)}
	dc := &fakeDiscordTransport{events: make(chan transport.Incoming, 1)}

	originalOpenWhatsApp := openWhatsApp
	originalOpenDiscord := openDiscord
	defer func() {
		openWhatsApp = originalOpenWhatsApp
		openDiscord = originalOpenDiscord
	}()

	openWhatsApp = func(context.Context, whatsapp.Options) (whatsappTransport, error) {
		return wa, nil
	}

	openedDiscord := make(chan discord.Options, 1)
	openDiscord = func(_ context.Context, opts discord.Options) (discordTransport, error) {
		openedDiscord <- opts
		return dc, nil
	}

	var logBuf safeBuffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, cfg, logger) }()

	var gotDiscordOptions discord.Options
	select {
	case gotDiscordOptions = <-openedDiscord:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("Discord gateway was not opened")
	}
	for i := 0; i < 100 && !strings.Contains(logBuf.String(), "message-sync started"); i++ {
		time.Sleep(10 * time.Millisecond)
	}

	if gotDiscordOptions.Token != "test-discord-token" {
		cancel()
		t.Fatalf("Discord token was not passed from environment to adapter")
	}
	if gotDiscordOptions.ChannelIDs["discord"] != "123456789012345678" || len(gotDiscordOptions.ChannelIDs) != 1 {
		cancel()
		t.Fatal("Discord channel filter was not initialized correctly")
	}
	if gotDiscordOptions.UsernameMode != config.UsernameModeHash || gotDiscordOptions.Hasher == nil {
		cancel()
		t.Fatal("Discord privacy options were not initialized")
	}
	if !strings.Contains(logBuf.String(), "message-sync started") {
		cancel()
		t.Fatalf("application did not start with configured Discord gateway: %s", logBuf.String())
	}
	if strings.Contains(logBuf.String(), "test-discord-token") || strings.Contains(logBuf.String(), "123456789012345678") {
		cancel()
		t.Fatalf("application logs leaked Discord credential or remote channel ID: %s", logBuf.String())
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	dc.mu.Lock()
	closed := dc.closed
	dc.mu.Unlock()
	if !closed {
		t.Fatal("Discord gateway was not closed with application lifecycle")
	}
}


func TestRunRoutesWhatsAppAndDiscordIngressThroughOneRouter(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("API_ADDR", "127.0.0.1:0")
	t.Setenv("IDENTITY_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("DISCORD_BOT_TOKEN", "test-discord-token")
	t.Setenv("DISCORD_BOT_TOKEN_FILE", "")

	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"wa":      {Transport: config.TransportWhatsApp, RemoteID: "123456789@g.us"},
			"discord": {Transport: config.TransportDiscord, RemoteID: "123456789012345678"},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Groups: []string{"wa", "discord"}}},
		Identity: config.Identity{UsernameMode: config.UsernameModeHash},
		Media:    config.Media{MaxSizeMB: 100},
		Recovery: config.Recovery{MaxAgeHours: 24, MaxMessagesPerGroup: 200},
		Storage:  config.Storage{MessageRetentionDays: 90},
	}

	wa := &fakeWhatsAppTransport{events: make(chan transport.Incoming, 2)}
	dc := &fakeDiscordTransport{events: make(chan transport.Incoming, 2)}

	originalOpenWhatsApp := openWhatsApp
	originalOpenDiscord := openDiscord
	defer func() {
		openWhatsApp = originalOpenWhatsApp
		openDiscord = originalOpenDiscord
	}()

	openWhatsApp = func(context.Context, whatsapp.Options) (whatsappTransport, error) {
		return wa, nil
	}
	openDiscord = func(context.Context, discord.Options) (discordTransport, error) {
		return dc, nil
	}

	var logBuf safeBuffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, cfg, logger) }()

	for i := 0; i < 100 && !strings.Contains(logBuf.String(), "message-sync started"); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(logBuf.String(), "message-sync started") {
		cancel()
		t.Fatalf("application did not start: %s", logBuf.String())
	}

	wa.events <- transport.Incoming{
		Endpoint:  "wa",
		RemoteID:  "wa-source-message",
		Sender:    transport.Sender{OpaqueID: "u_waactor0001"},
		Kind:      "text",
		Text:      "from whatsapp",
		Timestamp: time.Unix(1_700_000_000, 0).UTC(),
	}
	for i := 0; i < 100; i++ {
		dc.mu.Lock()
		sent := len(dc.sent)
		dc.mu.Unlock()
		if sent == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	dc.mu.Lock()
	if len(dc.sent) != 1 || dc.sent[0].Endpoint != "discord" {
		got := append([]transport.Outgoing(nil), dc.sent...)
		dc.mu.Unlock()
		cancel()
		t.Fatalf("WhatsApp ingress was not dispatched to Discord adapter: %+v", got)
	}
	dc.mu.Unlock()

	dc.events <- transport.Incoming{
		Endpoint:  "discord",
		RemoteID:  "discord-source-message",
		Sender:    transport.Sender{OpaqueID: "u_dcactor0001"},
		Kind:      "text",
		Text:      "from discord",
		Timestamp: time.Unix(1_700_000_001, 0).UTC(),
	}
	for i := 0; i < 100; i++ {
		wa.mu.Lock()
		sent := len(wa.sent)
		wa.mu.Unlock()
		if sent == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	wa.mu.Lock()
	if len(wa.sent) != 1 || wa.sent[0].Endpoint != "wa" {
		got := append([]transport.Outgoing(nil), wa.sent...)
		wa.mu.Unlock()
		cancel()
		t.Fatalf("Discord ingress was not dispatched to WhatsApp adapter: %+v", got)
	}
	wa.mu.Unlock()

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}
