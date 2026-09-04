package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

func TestTelegramTokenInjectionFromOptions(t *testing.T) {
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("missing token returns error", func(t *testing.T) {
		_, err := Open(context.Background(), Options{
			ChatIDs:      map[string]string{"tg": "-1001234567890"},
			Hasher:       hasher,
			UsernameMode: config.UsernameModeHash,
			Logger:       slog.Default(),
			Token:        "",
		})
		if err == nil || !strings.Contains(err.Error(), "Telegram bot token is required") {
			t.Fatalf("expected token required error, got: %v", err)
		}
	})

	t.Run("injected token passed to factory and adapter", func(t *testing.T) {
		var capturedToken string
		factory := func(token string, handler telegrambot.HandlerFunc, errorsHandler telegrambot.ErrorsHandler) (botClient, error) {
			capturedToken = token
			return &fakeBotClient{id: 12345, started: make(chan struct{}, 1)}, nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		adapter, err := Open(ctx, Options{
			ConnectionID:  "conn-tg-1",
			Token:         "12345:injected-secret-token",
			ChatIDs:       map[string]string{"tg": "-1001234567890"},
			Hasher:        hasher,
			UsernameMode:  config.UsernameModeHash,
			Logger:        slog.Default(),
			clientFactory: factory,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer adapter.Close()

		if capturedToken != "12345:injected-secret-token" {
			t.Fatalf("factory received token %q, want %q", capturedToken, "12345:injected-secret-token")
		}
		if adapter.ConnectionID() != "conn-tg-1" {
			t.Fatalf("adapter connection ID = %q, want %q", adapter.ConnectionID(), "conn-tg-1")
		}
	})
}

func TestTelegramAdapterEndpointOwnership(t *testing.T) {
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}

	adapter := &Adapter{
		connectionID: "conn-tg-1",
		hasher:       hasher,
	}

	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"tg-owned": {
				Transport:    config.TransportTelegram,
				ConnectionID: "conn-tg-1",
				RemoteID:     "-1001234567890",
			},
			"tg-other": {
				Transport:    config.TransportTelegram,
				ConnectionID: "conn-tg-2",
				RemoteID:     "-1009876543210",
			},
		},
		Identity: config.Identity{UsernameMode: config.UsernameModeHash},
	}

	if err := adapter.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}

	adapter.mu.RLock()
	defer adapter.mu.RUnlock()
	if len(adapter.normalizer.endpoints) != 1 {
		t.Fatalf("expected 1 endpoint for conn-tg-1, got: %d", len(adapter.normalizer.endpoints))
	}
	if _, ok := adapter.normalizer.endpoints[-1001234567890]; !ok {
		t.Fatal("expected -1001234567890 to be configured on adapter")
	}
	if _, ok := adapter.normalizer.endpoints[-1009876543210]; ok {
		t.Fatal("expected -1009876543210 to be excluded from adapter")
	}
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
		connectionID: "conn-tg-test",
		normalizer:   normalizer,
		events:       make(chan transport.Incoming, 4),
		botUserID:    testBotUserID,
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
		if incoming.Kind != "other" || incoming.Checkpoint.Position != 11 {
			t.Fatalf("ignored Telegram update = %#v", incoming)
		}
	default:
		t.Fatal("ignored Telegram update did not emit an accepted checkpoint")
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

func TestHandleUpdateEmitsTelegramReactionLifecycle(t *testing.T) {
	normalizer := testNormalizer(t, config.UsernameModeHash)
	adapter := &Adapter{
		connectionID: "conn-tg-test",
		normalizer:   normalizer,
		events:       make(chan transport.Incoming, 4),
		botUserID:    testBotUserID,
	}

	tests := []struct {
		name  string
		new   []models.ReactionType
		want  string
		start int64
	}{
		{name: "add", new: []models.ReactionType{{Type: models.ReactionTypeTypeEmoji, ReactionTypeEmoji: &models.ReactionTypeEmoji{Type: models.ReactionTypeTypeEmoji, Emoji: "👍"}}}, want: "👍", start: 20},
		{name: "remove", new: nil, want: "", start: 21},
		{name: "change", new: []models.ReactionType{{Type: models.ReactionTypeTypeEmoji, ReactionTypeEmoji: &models.ReactionTypeEmoji{Type: models.ReactionTypeTypeEmoji, Emoji: "❤️"}}}, want: "❤️", start: 22},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter.handleUpdate(context.Background(), nil, &models.Update{ID: tt.start, MessageReaction: &models.MessageReactionUpdated{
				Chat: models.Chat{ID: testGroupID, Type: models.ChatTypeSupergroup}, MessageID: 101,
				User: &models.User{ID: 54321}, Date: 1_700_000_020, NewReaction: tt.new,
			}})
			select {
			case incoming := <-adapter.events:
				if incoming.Kind != "reaction" || incoming.Endpoint != "team-telegram" || incoming.RemoteID != "101" || incoming.Text != tt.want {
					t.Fatalf("normalized reaction = %#v, want endpoint team-telegram, remote 101, text %q", incoming, tt.want)
				}
				if incoming.ReplyTo == nil || incoming.ReplyTo.RemoteMessageID != "101" {
					t.Fatalf("reaction target = %#v, want remote 101", incoming.ReplyTo)
				}
				if incoming.Checkpoint.StreamKey != "telegram:conn-tg-test" || incoming.Checkpoint.Position != tt.start {
					t.Fatalf("reaction checkpoint = %#v", incoming.Checkpoint)
				}
			default:
				t.Fatal("reaction update did not reach the shared ingress channel")
			}
		})
	}
}

func TestHandleUpdateObservesTelegramForumTopicLabelsWithoutRoutingServiceMessages(t *testing.T) {
	normalizer := testNormalizer(t, config.UsernameModeHash)
	type observedLabel struct {
		endpoint transport.EndpointID
		scope    transport.ChildScope
	}
	labels := make(chan observedLabel, 4)
	adapter := &Adapter{
		normalizer: normalizer,
		events:     make(chan transport.Incoming, 4),
		botUserID:  testBotUserID,
		observeChildScopeLabel: func(_ context.Context, endpoint transport.EndpointID, scope transport.ChildScope) {
			labels <- observedLabel{endpoint: endpoint, scope: scope}
		},
	}

	created := testMessage(testSupergroupID, models.ChatTypeSupergroup)
	created.Text = ""
	created.MessageThreadID = 777
	created.ForumTopicCreated = &models.ForumTopicCreated{Name: "  Project\n Alpha  "}
	adapter.handleUpdate(context.Background(), nil, &models.Update{ID: 30, Message: created})
	edited := testMessage(testSupergroupID, models.ChatTypeSupergroup)
	edited.Text = ""
	edited.MessageThreadID = 777
	edited.ForumTopicEdited = &models.ForumTopicEdited{Name: "Renamed"}
	adapter.handleUpdate(context.Background(), nil, &models.Update{ID: 31, Message: edited})
	emptyEdit := testMessage(testSupergroupID, models.ChatTypeSupergroup)
	emptyEdit.Text = ""
	emptyEdit.MessageThreadID = 777
	emptyEdit.ForumTopicEdited = &models.ForumTopicEdited{}
	adapter.handleUpdate(context.Background(), nil, &models.Update{ID: 32, Message: emptyEdit})
	unconfigured := testMessage(-1009999999999, models.ChatTypeSupergroup)
	unconfigured.Text = ""
	unconfigured.MessageThreadID = 777
	unconfigured.ForumTopicCreated = &models.ForumTopicCreated{Name: "Not stored"}
	adapter.handleUpdate(context.Background(), nil, &models.Update{ID: 33, Message: unconfigured})

	for _, want := range []string{"super-telegram:  Project\n Alpha  ", "super-telegram:Renamed"} {
		select {
		case got := <-labels:
			if string(got.endpoint)+":"+got.scope.Label != want || got.scope.RemoteID != "777" || got.scope.Kind != transport.ScopeKindTelegramTopic {
				t.Fatalf("observed label = %#v, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("did not observe label %q", want)
		}
	}
	if len(adapter.events) != 4 {
		t.Fatalf("service messages should only emit checkpoints, got %d events", len(adapter.events))
	}
	for i := 0; i < 4; i++ {
		incoming := <-adapter.events
		if incoming.Kind != "other" {
			t.Fatalf("service message was routed as %#v", incoming)
		}
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

func TestNewBotClientStartsAtAcceptedUpdatePlusOne(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var gotOffset string
	client, err := newBotClient("12345:test-token", func(context.Context, *telegrambot.Bot, *models.Update) {}, func(error) {}, 42, &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if strings.HasSuffix(request.URL.Path, "/getMe") {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"id":12345}}`)), Header: make(http.Header)}, nil
			}
			if !strings.HasSuffix(request.URL.Path, "/getUpdates") {
				return nil, fmt.Errorf("unexpected Telegram method %s", request.URL.Path)
			}
			if err := request.ParseMultipartForm(1024); err != nil {
				return nil, err
			}
			gotOffset = request.FormValue("offset")
			cancel()
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":[]}`)), Header: make(http.Header)}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	client.Start(ctx)
	if gotOffset != "43" {
		t.Fatalf("first Telegram getUpdates offset = %q, want 43", gotOffset)
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
	case incoming := <-adapter.events:
		if incoming.Kind != "other" || incoming.Checkpoint.Position != 1 {
			t.Fatalf("bridge bot update checkpoint = %#v", incoming)
		}
	default:
		t.Fatal("bridge bot update did not emit an accepted checkpoint")
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
	if f.started != nil {
		f.once.Do(func() { close(f.started) })
	}
	<-ctx.Done()
	if f.stopped != nil {
		close(f.stopped)
	}
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
		ConnectionID: "conn-tg-test",
		Token:        "12345:test-token",
		ChatIDs: map[string]string{
			"team-telegram": strconv.FormatInt(testGroupID, 10),
		},
		Hasher:          hasher,
		UsernameMode:    config.UsernameModePushName,
		InitialUpdateID: 12,
		Logger:          slog.New(slog.NewJSONHandler(&logBuf, nil)),
		clientFactory:   factory,
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
		ConnectionID: "conn-tg-test",
		Token:        "12345:test-token",
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
		if incoming.Kind != "other" || incoming.Checkpoint.Position != 1 {
			t.Fatalf("migration checkpoint = %#v", incoming)
		}
	default:
		t.Fatal("migration update did not emit an accepted checkpoint")
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
	select {
	case incoming := <-adapter.events:
		if incoming.Kind != "other" || incoming.Checkpoint.Position != 2 {
			t.Fatalf("migration replay checkpoint = %#v", incoming)
		}
	default:
		t.Fatal("migration replay did not emit an accepted checkpoint")
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
		if incoming.Kind != "other" || incoming.Checkpoint.Position != 1 {
			t.Fatalf("unsupported service checkpoint = %#v", incoming)
		}
	default:
		t.Fatal("unsupported service update did not emit an accepted checkpoint")
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
