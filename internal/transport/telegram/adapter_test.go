package telegram

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	telegrambot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
)

func TestLoadBotTokenFromEnvironment(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN_FILE", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "  12345:environment-token  ")

	token, err := LoadBotToken()
	if err != nil {
		t.Fatal(err)
	}
	if token != "12345:environment-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestLoadBotTokenFromSecretFile(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	path := filepath.Join(t.TempDir(), "telegram-token")
	if err := os.WriteFile(path, []byte("12345:mounted-secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TELEGRAM_BOT_TOKEN_FILE", path)

	token, err := LoadBotToken()
	if err != nil {
		t.Fatal(err)
	}
	if token != "12345:mounted-secret-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestLoadBotTokenRejectsMissingAndAmbiguousSources(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		t.Setenv("TELEGRAM_BOT_TOKEN", "")
		t.Setenv("TELEGRAM_BOT_TOKEN_FILE", "")
		if _, err := LoadBotToken(); err == nil {
			t.Fatal("expected missing token error")
		}
	})

	t.Run("both", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "telegram-token")
		if err := os.WriteFile(path, []byte("12345:secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TELEGRAM_BOT_TOKEN", "12345:environment-token")
		t.Setenv("TELEGRAM_BOT_TOKEN_FILE", path)
		if _, err := LoadBotToken(); err == nil {
			t.Fatal("expected ambiguous token source error")
		}
	})
}

func TestTelegramErrorHandlerDropsRawProtocolErrorText(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	handler := safeTelegramErrorsHandler(logger)

	handler(errors.New("raw Telegram error private-user-id private-message-body"))
	logged := logBuf.String()
	if !strings.Contains(logged, "telegram_client") || !strings.Contains(logged, "error_kind") {
		t.Fatalf("safe Telegram error classification was not emitted: %s", logged)
	}
	for _, forbidden := range []string{"private-user-id", "private-message-body", "raw Telegram error"} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("Telegram error logger leaked %q: %s", forbidden, logged)
		}
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
		Endpoint: "team-telegram",
		RemoteID: "raw-telegram-message-id",
		Sender: transport.Sender{
			DisplayName: "Private Telegram Name",
			OpaqueID:    "u_privatehash",
		},
		Kind: "text",
		Text: "private Telegram message body",
	})

	logged := logBuf.String()
	for _, required := range []string{"telegram_ingress_dropped", "team-telegram", "text", "buffer_full"} {
		if !strings.Contains(logged, required) {
			t.Fatalf("safe log is missing %q: %s", required, logged)
		}
	}
	for _, forbidden := range []string{"raw-telegram-message-id", "Private Telegram Name", "u_privatehash", "private Telegram message body"} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("safe log leaked %q: %s", forbidden, logged)
		}
	}
}

func TestAdapterContract(t *testing.T) {
	var adapter transport.Adapter = &Adapter{}
	if adapter.Name() != "telegram" {
		t.Fatalf("Name() = %q", adapter.Name())
	}
	if _, err := adapter.Send(context.Background(), transport.Outgoing{}); err == nil {
		t.Fatal("unconfigured Telegram adapter unexpectedly accepted outbound send")
	}
}

func TestAdapterUpdateConfigUsesOnlyTelegramEndpoints(t *testing.T) {
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
			"wa":       {Transport: config.TransportWhatsApp, RemoteID: "123456789@g.us"},
			"discord":  {Transport: config.TransportDiscord, RemoteID: "123456789012345678"},
			"telegram": {Transport: config.TransportTelegram, RemoteID: strconv.FormatInt(testGroupID, 10)},
		},
		Identity: config.Identity{UsernameMode: config.UsernameModeHash},
	}

	if err := adapter.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	incoming, ok := adapter.normalizer.NormalizeMessage(testMessage(testGroupID, models.ChatTypeGroup), testBotUserID)
	if !ok || incoming.Endpoint != "telegram" {
		t.Fatalf("Telegram endpoint was not installed; accepted=%v incoming=%#v", ok, incoming)
	}
	if len(adapter.normalizer.endpoints) != 1 {
		t.Fatalf("non-Telegram endpoints were installed as Telegram targets: %#v", adapter.normalizer.endpoints)
	}
}

func TestHandleUpdateRejectsDuplicateAndOlderOffsets(t *testing.T) {
	normalizer := testNormalizer(t, config.UsernameModeHash)
	adapter := &Adapter{
		normalizer: normalizer,
		events:     make(chan transport.Incoming, 4),
		botUserID:  testBotUserID,
	}

	update := &models.Update{ID: 10, Message: testMessage(testGroupID, models.ChatTypeGroup)}
	adapter.handleUpdate(context.Background(), nil, update)
	adapter.handleUpdate(context.Background(), nil, update)

	ignored := &models.Update{ID: 11}
	adapter.handleUpdate(context.Background(), nil, ignored)
	older := &models.Update{ID: 10, Message: testMessage(testGroupID, models.ChatTypeGroup)}
	older.Message.ID = 102
	adapter.handleUpdate(context.Background(), nil, older)

	select {
	case incoming := <-adapter.events:
		if incoming.RemoteID != "101" {
			t.Fatalf("first normalized remote ID = %q", incoming.RemoteID)
		}
	default:
		t.Fatal("expected first Telegram ingress event")
	}
	select {
	case incoming := <-adapter.events:
		t.Fatalf("duplicate/older Telegram update was emitted: %#v", incoming)
	default:
	}
	if adapter.lastUpdateID != 11 {
		t.Fatalf("last update ID = %d, want 11", adapter.lastUpdateID)
	}
}

func TestPinnedBotClientLongPollReconnectUsesBackoff(t *testing.T) {
	var (
		mu           sync.Mutex
		requestTimes []time.Time
		requests     int
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handled := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/getUpdates") {
			t.Errorf("unexpected Telegram test API path %q", r.URL.Path)
			http.Error(w, "unexpected Telegram test API path", http.StatusNotFound)
			return
		}

		mu.Lock()
		requests++
		requestTimes = append(requestTimes, time.Now())
		attempt := requests
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if attempt == 1 {
			_, _ = w.Write([]byte(`{"ok":false,"error_code":500,"description":"temporary"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":2}]}`))
	}))
	defer server.Close()

	var handledOnce sync.Once
	client, err := telegrambot.New(
		"12345:test-token",
		telegrambot.WithSkipGetMe(),
		telegrambot.WithServerURL(server.URL),
		telegrambot.WithErrorsHandler(func(error) {}),
		telegrambot.WithDefaultHandler(func(context.Context, *telegrambot.Bot, *models.Update) {
			handledOnce.Do(func() {
				close(handled)
				cancel()
			})
		}),
		telegrambot.WithNotAsyncHandlers(),
	)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		client.Start(ctx)
		close(done)
	}()

	select {
	case <-handled:
	case <-time.After(2 * time.Second):
		t.Fatal("Telegram long polling did not recover after a transient getUpdates failure")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Telegram long polling did not stop after recovered handler canceled context")
	}

	mu.Lock()
	gotRequests := requests
	times := append([]time.Time(nil), requestTimes...)
	mu.Unlock()
	if gotRequests < 2 || len(times) < 2 {
		t.Fatalf("getUpdates requests = %d, want at least 2", gotRequests)
	}
	if delay := times[1].Sub(times[0]); delay < 90*time.Millisecond {
		t.Fatalf("getUpdates retry delay = %v, want bounded backoff instead of a busy loop", delay)
	}
}

func TestHandleUpdateDropsBridgeBotWithoutLoggingProtocolData(t *testing.T) {
	normalizer := testNormalizer(t, config.UsernameModePushName)
	var logBuf bytes.Buffer
	adapter := &Adapter{
		normalizer: normalizer,
		events:     make(chan transport.Incoming, 1),
		logger:     slog.New(slog.NewJSONHandler(&logBuf, nil)),
		botUserID:  testBotUserID,
	}
	msg := testMessage(testGroupID, models.ChatTypeGroup)
	msg.From.ID = testBotUserID
	msg.From.IsBot = true
	adapter.handleUpdate(context.Background(), nil, &models.Update{ID: 1, Message: msg})

	select {
	case <-adapter.events:
		t.Fatal("bridge bot message re-entered Telegram ingress")
	default:
	}
	if logBuf.Len() != 0 {
		t.Fatalf("filtered bridge bot update produced a log: %s", logBuf.String())
	}
}

type fakeBotClient struct {
	id       int64
	me       *models.User
	getMeErr error
	started  chan struct{}
	stopped  chan struct{}
	once     sync.Once
}

func (f *fakeBotClient) ID() int64 { return f.id }

func (f *fakeBotClient) GetMe(context.Context) (*models.User, error) {
	return f.me, f.getMeErr
}

func (f *fakeBotClient) Start(ctx context.Context) {
	f.once.Do(func() { close(f.started) })
	<-ctx.Done()
	close(f.stopped)
}

func TestOpenStartsAndStopsLongPollingWithContext(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN_FILE", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "12345:test-token")
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeBotClient{
		id:      testBotUserID,
		me:      &models.User{CanReadAllGroupMessages: true},
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
	var capturedHandler telegrambot.HandlerFunc
	var capturedErrors telegrambot.ErrorsHandler
	factory := func(token string, handler telegrambot.HandlerFunc, errorsHandler telegrambot.ErrorsHandler) (botClient, error) {
		if token != "12345:test-token" {
			t.Fatalf("factory token = %q", token)
		}
		capturedHandler = handler
		capturedErrors = errorsHandler
		return fake, nil
	}
	var logBuf bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	adapter, err := Open(ctx, Options{
		ChatIDs: map[string]string{
			"team-telegram": strconv.FormatInt(testGroupID, 10),
		},
		Hasher:        hasher,
		UsernameMode:  config.UsernameModePushName,
		Logger:        slog.New(slog.NewJSONHandler(&logBuf, nil)),
		clientFactory: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	if capturedHandler == nil || capturedErrors == nil {
		t.Fatal("Telegram client handlers were not installed")
	}

	status := adapter.AdminStatus(context.Background())
	if !status.PrivacyModeKnown || status.PrivacyModeEnabled == nil || *status.PrivacyModeEnabled {
		t.Fatalf("privacy mode status = %+v, want known and disabled", status)
	}

	select {
	case <-fake.started:
	case <-time.After(time.Second):
		t.Fatal("Telegram long polling did not start")
	}

	capturedHandler(context.Background(), nil, &models.Update{ID: 1, Message: testMessage(testGroupID, models.ChatTypeGroup)})
	select {
	case incoming := <-adapter.Events():
		if incoming.Endpoint != "team-telegram" {
			t.Fatalf("unexpected ingress endpoint: %q", incoming.Endpoint)
		}
	case <-time.After(time.Second):
		t.Fatal("Telegram handler did not emit normalized ingress")
	}

	cancel()
	select {
	case <-fake.stopped:
	case <-time.After(time.Second):
		t.Fatal("Telegram long polling did not stop after context cancellation")
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	logged := logBuf.String()
	if !strings.Contains(logged, "telegram_poll_started") || !strings.Contains(logged, "telegram_poll_stopped") {
		t.Fatalf("safe Telegram lifecycle logs missing: %s", logged)
	}
	if strings.Contains(logged, "12345:test-token") {
		t.Fatal("Telegram token leaked into lifecycle logs")
	}
}

func TestAdminStatusSanitizesPrivacyProbeError(t *testing.T) {
	var logBuf bytes.Buffer
	adapter := &Adapter{
		client: &fakeBotClient{
			id:       testBotUserID,
			getMeErr: errors.New("raw getMe failure bot-user=999 secret-value"),
		},
		logger:             slog.New(slog.NewJSONHandler(&logBuf, nil)),
		polling:            true,
		privacyModeKnown:   true,
		privacyModeEnabled: false,
	}

	status := adapter.AdminStatus(context.Background())
	if status.PrivacyModeKnown || status.PrivacyModeEnabled != nil {
		t.Fatalf("privacy mode unexpectedly known after failed probe: %+v", status)
	}
	logged := logBuf.String()
	if !strings.Contains(logged, "telegram_status_probe") {
		t.Fatalf("safe privacy probe event missing: %s", logged)
	}
	for _, forbidden := range []string{"bot-user=999", "secret-value"} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("privacy probe log leaked %q: %s", forbidden, logged)
		}
	}
}

func TestOpenSanitizesClientInitializationError(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN_FILE", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "12345:test-token")
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	_, err = Open(context.Background(), Options{
		ChatIDs: map[string]string{
			"team-telegram": strconv.FormatInt(testGroupID, 10),
		},
		Hasher:       hasher,
		UsernameMode: config.UsernameModeHash,
		Logger:       logger,
		clientFactory: func(string, telegrambot.HandlerFunc, telegrambot.ErrorsHandler) (botClient, error) {
			return nil, errors.New("raw api failure user-id=999 private-body")
		},
	})
	if err == nil {
		t.Fatal("expected Telegram initialization error")
	}
	if strings.Contains(err.Error(), "user-id=999") || strings.Contains(err.Error(), "private-body") {
		t.Fatalf("returned error leaked raw Telegram API data: %v", err)
	}
	logged := logBuf.String()
	if !strings.Contains(logged, "telegram_init") || strings.Contains(logged, "user-id=999") || strings.Contains(logged, "private-body") {
		t.Fatalf("Telegram initialization log was not safely classified: %s", logged)
	}
}

func TestHandleTelegramGroupMigrationPreservesAliasAndRoutesNewChat(t *testing.T) {
	normalizer := testNormalizer(t, config.UsernameModeHash)
	const newChatID int64 = -1009876543210
	var logBuf bytes.Buffer
	var (
		migrationCalls int
		gotEndpoint    transport.EndpointID
		gotOldRemoteID string
		gotNewRemoteID string
	)
	adapter := &Adapter{
		normalizer: normalizer,
		events:     make(chan transport.Incoming, 2),
		logger:     slog.New(slog.NewJSONHandler(&logBuf, nil)),
		botUserID:  testBotUserID,
		migrateEndpoint: func(_ context.Context, endpoint transport.EndpointID, oldRemoteID, newRemoteID string) error {
			migrationCalls++
			gotEndpoint = endpoint
			gotOldRemoteID = oldRemoteID
			gotNewRemoteID = newRemoteID
			return nil
		},
	}

	migration := testMessage(testGroupID, models.ChatTypeGroup)
	migration.Text = ""
	migration.From = nil
	migration.Chat.Title = "private old Telegram title"
	migration.MigrateToChatID = newChatID
	adapter.handleUpdate(context.Background(), nil, &models.Update{ID: 1, Message: migration})

	if migrationCalls != 1 || gotEndpoint != "team-telegram" ||
		gotOldRemoteID != strconv.FormatInt(testGroupID, 10) ||
		gotNewRemoteID != strconv.FormatInt(newChatID, 10) {
		t.Fatalf("migration callback = calls %d endpoint %q", migrationCalls, gotEndpoint)
	}
	select {
	case incoming := <-adapter.events:
		t.Fatalf("migration service message became canonical ingress: %#v", incoming)
	default:
	}

	// Telegram may also emit migrate_from_chat_id in the new supergroup.
	replay := testMessage(newChatID, models.ChatTypeSupergroup)
	replay.Text = ""
	replay.From = nil
	replay.MigrateFromChatID = testGroupID
	adapter.handleUpdate(context.Background(), nil, &models.Update{ID: 2, Message: replay})
	if migrationCalls != 1 {
		t.Fatalf("migration replay caused duplicate persistence update: %d", migrationCalls)
	}

	message := testMessage(newChatID, models.ChatTypeSupergroup)
	message.ID = 202
	adapter.handleUpdate(context.Background(), nil, &models.Update{ID: 3, Message: message})
	select {
	case incoming := <-adapter.events:
		if incoming.Endpoint != "team-telegram" || incoming.RemoteID != "202" {
			t.Fatalf("post-migration routing = %#v", incoming)
		}
	default:
		t.Fatal("new supergroup chat did not route through preserved alias")
	}

	adapter.mu.RLock()
	runtimeNormalizer := adapter.normalizer
	adapter.mu.RUnlock()
	if _, ok := runtimeNormalizer.endpoint(testGroupID); ok {
		t.Fatal("old Telegram chat id remained active after migration")
	}
	if endpoint, ok := runtimeNormalizer.endpoint(newChatID); !ok || endpoint != "team-telegram" {
		t.Fatalf("new Telegram chat did not preserve alias: endpoint=%q ok=%v", endpoint, ok)
	}

	logged := logBuf.String()
	for _, forbidden := range []string{
		strconv.FormatInt(testGroupID, 10),
		strconv.FormatInt(newChatID, 10),
		"private old Telegram title",
	} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("Telegram migration log leaked %q: %s", forbidden, logged)
		}
	}
}

func TestHandleUpdateLogsOnlySafeUnsupportedEventClass(t *testing.T) {
	normalizer := testNormalizer(t, config.UsernameModePushName)
	var logBuf bytes.Buffer
	adapter := &Adapter{
		normalizer: normalizer,
		events:     make(chan transport.Incoming, 1),
		logger:     slog.New(slog.NewJSONHandler(&logBuf, nil)),
		botUserID:  testBotUserID,
	}

	msg := testMessage(testGroupID, models.ChatTypeGroup)
	msg.Text = ""
	msg.Chat.Title = "private chat title sentinel"
	msg.From.FirstName = "private sender sentinel"
	msg.NewChatTitle = "private new title sentinel"
	adapter.handleUpdate(context.Background(), nil, &models.Update{ID: 1, Message: msg})

	select {
	case incoming := <-adapter.events:
		t.Fatalf("unsupported service message became ingress: %#v", incoming)
	default:
	}
	logged := logBuf.String()
	for _, required := range []string{"telegram_message_ignored", "team-telegram", "chat_metadata"} {
		if !strings.Contains(logged, required) {
			t.Fatalf("safe unsupported-event log missing %q: %s", required, logged)
		}
	}
	for _, forbidden := range []string{
		strconv.FormatInt(testGroupID, 10),
		"private chat title sentinel",
		"private sender sentinel",
		"private new title sentinel",
	} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("unsupported-event log leaked %q: %s", forbidden, logged)
		}
	}
}
