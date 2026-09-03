package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/transport"
	discord "github.com/vm75/message-sync/internal/transport/discord"
	telegram "github.com/vm75/message-sync/internal/transport/telegram"
	whatsapp "github.com/vm75/message-sync/internal/transport/whatsapp"
)

type fakeTelegramTransport struct {
	events         chan transport.Incoming
	mu             sync.Mutex
	closed         bool
	sent           []transport.Outgoing
	sentCount      int
	updatedConfigs []*config.Config
}

func (f *fakeTelegramTransport) Events() <-chan transport.Incoming { return f.events }

func (f *fakeTelegramTransport) Send(_ context.Context, outgoing transport.Outgoing) (transport.MessageRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sentCount++
	f.sent = append(f.sent, outgoing)
	return transport.MessageRef{
		Endpoint:        outgoing.Endpoint,
		RemoteMessageID: fmt.Sprintf("telegram-sent-%d", f.sentCount),
		IsTargetFromMe:  true,
	}, nil
}

func (f *fakeTelegramTransport) React(context.Context, transport.Reaction) error { return nil }
func (f *fakeTelegramTransport) Edit(context.Context, transport.MessageRef, string) error {
	return nil
}
func (f *fakeTelegramTransport) Delete(context.Context, transport.MessageRef) error { return nil }

func (f *fakeTelegramTransport) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeTelegramTransport) UpdateConfig(cfg *config.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updatedConfigs = append(f.updatedConfigs, cfg)
	return nil
}

func (f *fakeTelegramTransport) AdminStatus(context.Context) telegram.AdminStatus {
	return telegram.AdminStatus{
		TokenConfigured:    true,
		Running:            true,
		Status:             "running",
		Endpoints:          []telegram.EndpointReadiness{},
		PrivacyModeKnown:   false,
		VisibilityGuidance: telegram.VisibilityGuidance,
	}
}

func (f *fakeTelegramTransport) DiscoverChats(context.Context) ([]telegram.DiscoveredChat, error) {
	return []telegram.DiscoveredChat{}, nil
}

func TestRunRoutesAllThreeTransportIngressThroughOneRouter(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	seedTestConnections(t, dataDir)
	t.Setenv("API_ADDR", "127.0.0.1:0")
	t.Setenv("IDENTITY_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("DISCORD_BOT_TOKEN", "test-discord-token")
	t.Setenv("DISCORD_BOT_TOKEN_FILE", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "12345:test-telegram-token")
	t.Setenv("TELEGRAM_BOT_TOKEN_FILE", "")

	const telegramRemoteID = "-1001234567890"
	const discordRemoteID = "123456789012345678"
	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"wa":       {Transport: config.TransportWhatsApp, ConnectionID: "conn-wa-1", RemoteID: "123456789@g.us"},
			"discord":  {Transport: config.TransportDiscord, ConnectionID: "conn-dc-1", RemoteID: discordRemoteID},
			"telegram": {Transport: config.TransportTelegram, ConnectionID: "conn-tg-1", RemoteID: telegramRemoteID},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Endpoints: []string{"wa", "discord", "telegram"}}},
		Identity: config.Identity{UsernameMode: config.UsernameModeHash},
		Media:    config.Media{Enabled: true, MaxSizeMB: 100},
		Recovery: config.Recovery{MaxAgeHours: 24, MaxMessagesPerGroup: 200},
		Storage:  config.Storage{MessageRetentionDays: 90},
	}

	wa := &fakeWhatsAppTransport{events: make(chan transport.Incoming, 4)}
	dc := &fakeDiscordTransport{events: make(chan transport.Incoming, 4)}
	tg := &fakeTelegramTransport{events: make(chan transport.Incoming, 4)}

	originalOpenWhatsApp := openWhatsApp
	originalOpenDiscord := openDiscord
	originalOpenTelegram := openTelegram
	defer func() {
		openWhatsApp = originalOpenWhatsApp
		openDiscord = originalOpenDiscord
		openTelegram = originalOpenTelegram
	}()

	openWhatsApp = func(context.Context, whatsapp.Options) (whatsappTransport, error) {
		return wa, nil
	}
	openDiscord = func(context.Context, discord.Options) (discordTransport, error) {
		return dc, nil
	}
	openedTelegram := make(chan telegram.Options, 1)
	openTelegram = func(_ context.Context, opts telegram.Options) (telegramTransport, error) {
		openedTelegram <- opts
		return tg, nil
	}

	var logBuf safeBuffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, cfg, logger) }()

	select {
	case opts := <-openedTelegram:
		if len(opts.ChatIDs) != 1 || opts.ChatIDs["telegram"] != telegramRemoteID {
			cancel()
			t.Fatalf("Telegram chat filter was not initialized correctly: %+v", opts.ChatIDs)
		}
		if opts.Hasher == nil || opts.UsernameMode != config.UsernameModeHash {
			cancel()
			t.Fatal("Telegram privacy options were not initialized")
		}
		if !opts.MediaEnabled || opts.MediaMaxBytes != 100*1024*1024 {
			cancel()
			t.Fatalf("Telegram media options were not initialized: enabled=%v max=%d", opts.MediaEnabled, opts.MediaMaxBytes)
		}
		if opts.MigrateEndpoint == nil {
			cancel()
			t.Fatal("Telegram migration persistence callback was not initialized")
		}
	case <-time.After(time.Second):
		cancel()
		t.Fatal("Telegram adapter was not opened")
	}

	waitStarted := func() {
		t.Helper()
		for i := 0; i < 100 && !strings.Contains(logBuf.String(), "message-sync started"); i++ {
			time.Sleep(10 * time.Millisecond)
		}
		if !strings.Contains(logBuf.String(), "message-sync started") {
			cancel()
			t.Fatalf("application did not start: %s", logBuf.String())
		}
	}
	waitStarted()

	wa.events <- transport.Incoming{
		Endpoint:  "wa",
		RemoteID:  "wa-source-message",
		Sender:    transport.Sender{OpaqueID: "u_waactor0001"},
		Kind:      "text",
		Text:      "from whatsapp",
		Timestamp: time.Unix(1_700_000_000, 0).UTC(),
	}
	waitForCount(t, func() int {
		tg.mu.Lock()
		defer tg.mu.Unlock()
		return len(tg.sent)
	}, 1)
	waitForCount(t, func() int {
		dc.mu.Lock()
		defer dc.mu.Unlock()
		return len(dc.sent)
	}, 1)

	tg.events <- transport.Incoming{
		Endpoint:  "telegram",
		RemoteID:  "telegram-source-message",
		Sender:    transport.Sender{OpaqueID: "u_tgactor0001", DisplayName: "private Telegram display sentinel"},
		Kind:      "text",
		Text:      "from telegram",
		Timestamp: time.Unix(1_700_000_001, 0).UTC(),
	}
	waitForCount(t, func() int {
		wa.mu.Lock()
		defer wa.mu.Unlock()
		return len(wa.sent)
	}, 1)
	waitForCount(t, func() int {
		dc.mu.Lock()
		defer dc.mu.Unlock()
		return len(dc.sent)
	}, 2)

	dc.mu.Lock()
	telegramToDiscord := dc.sent[1]
	dc.mu.Unlock()
	if telegramToDiscord.Sender.DisplayName != "private Telegram display sentinel" || telegramToDiscord.Sender.OpaqueID != "u_tgactor0001" {
		cancel()
		t.Fatalf("Telegram sender attribution was not preserved transiently for Discord rendering: %+v", telegramToDiscord.Sender)
	}
	wa.mu.Lock()
	telegramToWhatsApp := wa.sent[0]
	wa.mu.Unlock()
	if telegramToWhatsApp.Sender.DisplayName != "private Telegram display sentinel" || telegramToWhatsApp.Sender.OpaqueID != "u_tgactor0001" {
		cancel()
		t.Fatalf("Telegram sender attribution was not preserved transiently for WhatsApp rendering: %+v", telegramToWhatsApp.Sender)
	}

	dc.events <- transport.Incoming{
		Endpoint:  "discord",
		RemoteID:  "discord-source-message",
		Sender:    transport.Sender{OpaqueID: "u_dcactor0001"},
		Kind:      "text",
		Text:      "from discord",
		Timestamp: time.Unix(1_700_000_002, 0).UTC(),
	}
	waitForCount(t, func() int {
		tg.mu.Lock()
		defer tg.mu.Unlock()
		return len(tg.sent)
	}, 2)
	waitForCount(t, func() int {
		wa.mu.Lock()
		defer wa.mu.Unlock()
		return len(wa.sent)
	}, 2)

	if strings.Contains(logBuf.String(), telegramRemoteID) ||
		strings.Contains(logBuf.String(), discordRemoteID) ||
		strings.Contains(logBuf.String(), "12345:test-telegram-token") ||
		strings.Contains(logBuf.String(), "private Telegram display sentinel") {
		cancel()
		t.Fatalf("application logs leaked transport credential or remote ID: %s", logBuf.String())
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	tg.mu.Lock()
	telegramClosed := tg.closed
	tg.mu.Unlock()
	if !telegramClosed {
		t.Fatal("Telegram adapter was not closed with application lifecycle")
	}
}

func TestRunRequiresTelegramCredentialOnlyWhenRuntimeIsConfigured(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("API_ADDR", "127.0.0.1:0")
	t.Setenv("IDENTITY_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("DISCORD_BOT_TOKEN", "")
	t.Setenv("DISCORD_BOT_TOKEN_FILE", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_BOT_TOKEN_FILE", "")

	originalOpenWhatsApp := openWhatsApp
	defer func() { openWhatsApp = originalOpenWhatsApp }()
	wa := &fakeWhatsAppTransport{events: make(chan transport.Incoming)}
	openWhatsApp = func(context.Context, whatsapp.Options) (whatsappTransport, error) {
		return wa, nil
	}

	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"wa":       {Transport: config.TransportWhatsApp, ConnectionID: "conn-wa-1", RemoteID: "123456789@g.us"},
			"telegram": {Transport: config.TransportTelegram, ConnectionID: "conn-tg-1", RemoteID: "-1001234567890"},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Endpoints: []string{"wa", "telegram"}}},
		Identity: config.Identity{UsernameMode: config.UsernameModeHash},
		Media:    config.Media{MaxSizeMB: 100},
		Recovery: config.Recovery{MaxAgeHours: 24, MaxMessagesPerGroup: 200},
		Storage:  config.Storage{MessageRetentionDays: 90},
	}

	logger := slog.New(slog.NewTextHandler(&safeBuffer{}, nil))
	err := Run(context.Background(), cfg, logger)
	if err == nil {
		t.Fatal("expected configured Telegram runtime to require a credential")
	}
	if !strings.Contains(err.Error(), "Telegram bot token is not configured") {
		t.Fatalf("unexpected Telegram credential error: %v", err)
	}
	if strings.Contains(err.Error(), "-1001234567890") {
		t.Fatalf("Telegram credential error leaked remote chat ID: %v", err)
	}
}

func TestRunDoesNotStartTelegramWhenDisabled(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	seedTestConnections(t, dataDir)
	t.Setenv("API_ADDR", "127.0.0.1:0")
	t.Setenv("IDENTITY_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("DISCORD_BOT_TOKEN", "")
	t.Setenv("DISCORD_BOT_TOKEN_FILE", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_BOT_TOKEN_FILE", "")

	originalOpenWhatsApp := openWhatsApp
	originalOpenTelegram := openTelegram
	defer func() {
		openWhatsApp = originalOpenWhatsApp
		openTelegram = originalOpenTelegram
	}()

	wa := &fakeWhatsAppTransport{events: make(chan transport.Incoming)}
	openWhatsApp = func(context.Context, whatsapp.Options) (whatsappTransport, error) {
		return wa, nil
	}
	openTelegram = func(context.Context, telegram.Options) (telegramTransport, error) {
		return nil, errors.New("Telegram adapter should not be opened")
	}

	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"wa": {Transport: config.TransportWhatsApp, ConnectionID: "conn-wa-1", RemoteID: "123456789@g.us"},
		},
		Identity: config.Identity{UsernameMode: config.UsernameModeHash},
		Media:    config.Media{MaxSizeMB: 100},
		Recovery: config.Recovery{MaxAgeHours: 24, MaxMessagesPerGroup: 200},
		Storage:  config.Storage{MessageRetentionDays: 90},
	}

	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(&safeBuffer{}, nil))
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, cfg, logger) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Telegram-disabled deployment failed: %v", err)
	}
}

func waitForCount(t *testing.T, count func() int, want int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if count() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("count = %d, want %d", count(), want)
}

func TestRunRuntimeReloadAddsFirstTelegramEndpoint(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv("API_ADDR", "127.0.0.1:0")
	t.Setenv("IDENTITY_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("DISCORD_BOT_TOKEN", "")
	t.Setenv("DISCORD_BOT_TOKEN_FILE", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "12345:test-reload-token")
	t.Setenv("TELEGRAM_BOT_TOKEN_FILE", "")
	seedTestConnections(t, dataDir)

	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"wa1": {Transport: config.TransportWhatsApp, ConnectionID: "conn-wa-1", RemoteID: "123456789@g.us"},
			"wa2": {Transport: config.TransportWhatsApp, ConnectionID: "conn-wa-1", RemoteID: "987654321@g.us"},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Endpoints: []string{"wa1", "wa2"}}},
		Identity: config.Identity{UsernameMode: config.UsernameModeHash},
		Media:    config.Media{MaxSizeMB: 100},
		Recovery: config.Recovery{MaxAgeHours: 24, MaxMessagesPerGroup: 200},
		Storage:  config.Storage{MessageRetentionDays: 90},
	}

	st, err := store.Open(context.Background(), filepath.Join(dataDir, SyncDBName))
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(context.Background(), st.DB(), cfg); err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	wa := &fakeWhatsAppTransport{events: make(chan transport.Incoming, 2)}
	tg := &fakeTelegramTransport{events: make(chan transport.Incoming, 2)}

	originalOpenWhatsApp := openWhatsApp
	originalOpenTelegram := openTelegram
	defer func() {
		openWhatsApp = originalOpenWhatsApp
		openTelegram = originalOpenTelegram
	}()
	openWhatsApp = func(context.Context, whatsapp.Options) (whatsappTransport, error) {
		return wa, nil
	}
	openTelegram = func(context.Context, telegram.Options) (telegramTransport, error) {
		return tg, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var logBuf safeBuffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, cfg, logger)
	}()

	var apiAddr string
	for i := 0; i < 50; i++ {
		for _, line := range strings.Split(logBuf.String(), "\n") {
			if strings.Contains(line, "api server listening") && strings.Contains(line, "addr=") {
				parts := strings.Split(line, "addr=")
				if len(parts) > 1 {
					apiAddr = strings.Trim(strings.Fields(parts[1])[0], "\"")
					break
				}
			}
		}
		if apiAddr != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if apiAddr == "" {
		cancel()
		t.Fatalf("failed to find API address in logs: %s", logBuf.String())
	}

	client := &http.Client{Timeout: 2 * time.Second}
	var token string
	for i := 0; i < 50; i++ {
		resp, requestErr := client.Post(
			fmt.Sprintf("http://%s/api/auth/setup", apiAddr),
			"application/json",
			strings.NewReader(`{"username":"admin","password":"testadminpassword123"}`),
		)
		if requestErr == nil {
			var tokenResp struct {
				Token string `json:"token"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&tokenResp)
			resp.Body.Close()
			if tokenResp.Token != "" {
				token = tokenResp.Token
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if token == "" {
		cancel()
		t.Fatal("failed to set up admin authentication")
	}

	const telegramRemoteID = "-1001234567890"
	req, err := http.NewRequest(
		http.MethodPost,
		fmt.Sprintf("http://%s/api/endpoints", apiAddr),
		strings.NewReader(`{"alias":"telegram","transport":"telegram","connectionId":"conn-tg-1","remoteId":"-1001234567890","syncSetId":"mesh"}`),
	)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("POST Telegram endpoint failed: %v", err)
	}
	statusCode := resp.StatusCode
	resp.Body.Close()
	if statusCode != http.StatusCreated {
		cancel()
		t.Fatalf("POST Telegram endpoint status = %d, want 201", statusCode)
	}

	for i := 0; i < 100; i++ {
		tg.mu.Lock()
		updated := len(tg.updatedConfigs) > 0
		var endpoint config.Endpoint
		var ok bool
		if updated {
			endpoint, ok = tg.updatedConfigs[len(tg.updatedConfigs)-1].Endpoints["telegram"]
		}
		tg.mu.Unlock()
		if updated && ok && endpoint.Transport == config.TransportTelegram && endpoint.RemoteID == telegramRemoteID {
			break
		}
		if i == 99 {
			cancel()
			t.Fatal("runtime config reload did not update Telegram adapter targets")
		}
		time.Sleep(10 * time.Millisecond)
	}

	wa.events <- transport.Incoming{
		Endpoint:  "wa1",
		RemoteID:  "wa-after-reload",
		Sender:    transport.Sender{OpaqueID: "u_waactor0001"},
		Kind:      "text",
		Text:      "after reload",
		Timestamp: time.Unix(1_700_000_100, 0).UTC(),
	}
	waitForCount(t, func() int {
		tg.mu.Lock()
		defer tg.mu.Unlock()
		return len(tg.sent)
	}, 1)
	tg.mu.Lock()
	if tg.sent[0].Endpoint != "telegram" {
		got := tg.sent[0].Endpoint
		tg.mu.Unlock()
		cancel()
		t.Fatalf("post-reload destination = %q, want telegram", got)
	}
	tg.mu.Unlock()

	if strings.Contains(logBuf.String(), telegramRemoteID) ||
		strings.Contains(logBuf.String(), "12345:test-reload-token") {
		cancel()
		t.Fatalf("runtime reload logs leaked Telegram credential or remote ID: %s", logBuf.String())
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}
