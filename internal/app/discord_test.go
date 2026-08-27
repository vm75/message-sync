package app

import (
	"context"
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
	events chan transport.Incoming
	mu     sync.Mutex
	closed bool
}

func (f *fakeDiscordTransport) Events() <-chan transport.Incoming { return f.events }

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
		t.Fatalf("Discord channel filter = %+v", gotDiscordOptions.ChannelIDs)
	}
	if gotDiscordOptions.UsernameMode != config.UsernameModeHash || gotDiscordOptions.Hasher == nil {
		cancel()
		t.Fatalf("Discord privacy options were not initialized: %+v", gotDiscordOptions)
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

