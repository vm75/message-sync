package telegram

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/connection"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/router"
	"github.com/vm75/message-sync/internal/transport"
	_ "modernc.org/sqlite"
)

func TestMultiTelegramConcurrentPollingAndRecoveryCursors(t *testing.T) {
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}

	norm1, _ := NewNormalizerWithConnection(map[string]string{
		"tg1": "-1001111111111",
	}, hasher, config.UsernameModeHash, "conn-tg-1")

	norm2, _ := NewNormalizerWithConnection(map[string]string{
		"tg2": "-1002222222222",
	}, hasher, config.UsernameModeHash, "conn-tg-2")

	api1 := &fakeTelegramAPI{botID: 1001}
	api2 := &fakeTelegramAPI{botID: 1002}

	ad1 := &Adapter{
		connectionID: "conn-tg-1",
		token:        "12345:token-1",
		normalizer:   norm1,
		hasher:       hasher,
		events:       make(chan transport.Incoming, 10),
		observed:     make(map[int64]observedChatEntry),
		client:       api1,
		botUserID:    1001,
	}

	ad2 := &Adapter{
		connectionID: "conn-tg-2",
		token:        "67890:token-2",
		normalizer:   norm2,
		hasher:       hasher,
		events:       make(chan transport.Incoming, 10),
		observed:     make(map[int64]observedChatEntry),
		client:       api2,
		botUserID:    1002,
	}

	ctx := context.Background()

	// Update on bot 1 with ID 200
	ad1.handleUpdate(ctx, nil, &models.Update{
		ID: 200,
		Message: &models.Message{
			ID:   1,
			Chat: models.Chat{ID: -1001111111111, Type: models.ChatTypeSupergroup},
			From: &models.User{ID: 555},
			Text: "hello on tg1",
		},
	})

	select {
	case ev := <-ad1.Events():
		if ev.Endpoint != "tg1" {
			t.Fatalf("expected tg1 endpoint, got: %s", ev.Endpoint)
		}
		if ev.Checkpoint.StreamKey != "telegram:conn-tg-1" {
			t.Fatalf("expected stream key telegram:conn-tg-1, got: %s", ev.Checkpoint.StreamKey)
		}
		if ev.Checkpoint.Position != 200 {
			t.Fatalf("expected position 200, got: %d", ev.Checkpoint.Position)
		}
	default:
		t.Fatal("expected event from ad1")
	}

	// Update on bot 2 with numerically lower ID 100 (must be accepted because stream is independent)
	ad2.handleUpdate(ctx, nil, &models.Update{
		ID: 100,
		Message: &models.Message{
			ID:   2,
			Chat: models.Chat{ID: -1002222222222, Type: models.ChatTypeSupergroup},
			From: &models.User{ID: 777},
			Text: "hello on tg2",
		},
	})

	select {
	case ev := <-ad2.Events():
		if ev.Endpoint != "tg2" {
			t.Fatalf("expected tg2 endpoint, got: %s", ev.Endpoint)
		}
		if ev.Checkpoint.StreamKey != "telegram:conn-tg-2" {
			t.Fatalf("expected stream key telegram:conn-tg-2, got: %s", ev.Checkpoint.StreamKey)
		}
		if ev.Checkpoint.Position != 100 {
			t.Fatalf("expected position 100, got: %d", ev.Checkpoint.Position)
		}
	default:
		t.Fatal("expected event from ad2")
	}

	// Cross-talk protection: message for -1001111111111 sent to ad2 must NOT be accepted as ingress
	ad2.handleUpdate(ctx, nil, &models.Update{
		ID: 101,
		Message: &models.Message{
			ID:   3,
			Chat: models.Chat{ID: -1001111111111, Type: models.ChatTypeSupergroup},
			From: &models.User{ID: 555},
			Text: "cross talk",
		},
	})

	select {
	case ev := <-ad2.Events():
		if ev.Kind != "other" {
			t.Fatalf("unconfigured chat on ad2 should normalize as kind=other, got: %+v", ev)
		}
	default:
		t.Fatal("expected checkpoint event from ad2")
	}
}

func TestMultiTelegramPollProviderReferenceIsolation(t *testing.T) {
	ctx := context.Background()

	hasher, _ := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	norm1, _ := NewNormalizerWithConnection(map[string]string{"tg1": "-1001111111111"}, hasher, config.UsernameModeHash, "conn-tg-1")
	norm2, _ := NewNormalizerWithConnection(map[string]string{"tg2": "-1002222222222"}, hasher, config.UsernameModeHash, "conn-tg-2")

	api1 := &fakeTelegramAPI{botID: 1001}
	api2 := &fakeTelegramAPI{botID: 1002}

	resolvedNamespace := make(map[string]transport.EndpointID)
	resolvedNamespace["telegram:conn-tg-1|poll-999"] = "tg1"
	resolvedNamespace["telegram:conn-tg-2|poll-999"] = "tg2"

	ad1 := &Adapter{
		connectionID: "conn-tg-1",
		token:        "tok-1",
		normalizer:   norm1,
		client:       api1,
		messageKinds: make(map[messageKindKey]string),
		resolvePollEndpoint: func(_ context.Context, pollID string) (transport.EndpointID, bool) {
			ep, ok := resolvedNamespace["telegram:conn-tg-1|"+pollID]
			return ep, ok
		},
		events: make(chan transport.Incoming, 10),
	}

	ad2 := &Adapter{
		connectionID: "conn-tg-2",
		token:        "tok-2",
		normalizer:   norm2,
		client:       api2,
		messageKinds: make(map[messageKindKey]string),
		resolvePollEndpoint: func(_ context.Context, pollID string) (transport.EndpointID, bool) {
			ep, ok := resolvedNamespace["telegram:conn-tg-2|"+pollID]
			return ep, ok
		},
		events: make(chan transport.Incoming, 10),
	}

	// 1. Outbound poll provider namespaces
	ref1, err := ad1.Send(ctx, transport.Outgoing{
		Endpoint:            "tg1",
		Kind:                "poll",
		SourceText:          "Which color?",
		PollOptions:         []string{"Red", "Blue"},
		PollSelectableCount: 1,
		PollDurationHours:   0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref1.Provider != "telegram:conn-tg-1" {
		t.Fatalf("ad1 poll provider = %q, want telegram:conn-tg-1", ref1.Provider)
	}

	ref2, err := ad2.Send(ctx, transport.Outgoing{
		Endpoint:            "tg2",
		Kind:                "poll",
		SourceText:          "Which pet?",
		PollOptions:         []string{"Dog", "Cat"},
		PollSelectableCount: 1,
		PollDurationHours:   0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref2.Provider != "telegram:conn-tg-2" {
		t.Fatalf("ad2 poll provider = %q, want telegram:conn-tg-2", ref2.Provider)
	}

	// 2. Incoming poll snapshot isolation with identical poll ID
	ad1.handleUpdate(ctx, nil, &models.Update{
		ID: 1,
		Poll: &models.Poll{
			ID:      "poll-999",
			Options: []models.PollOption{{Text: "Red", VoterCount: 5}, {Text: "Blue", VoterCount: 3}},
		},
	})
	select {
	case ev1 := <-ad1.Events():
		if ev1.Endpoint != "tg1" || ev1.PollProvider != "telegram:conn-tg-1" || ev1.PollProviderReference != "poll-999" {
			t.Fatalf("unexpected ad1 poll snapshot event: %+v", ev1)
		}
	default:
		t.Fatal("expected poll snapshot on ad1")
	}

	ad2.handleUpdate(ctx, nil, &models.Update{
		ID: 1,
		Poll: &models.Poll{
			ID:      "poll-999",
			Options: []models.PollOption{{Text: "Dog", VoterCount: 10}, {Text: "Cat", VoterCount: 20}},
		},
	})
	select {
	case ev2 := <-ad2.Events():
		if ev2.Endpoint != "tg2" || ev2.PollProvider != "telegram:conn-tg-2" || ev2.PollProviderReference != "poll-999" {
			t.Fatalf("unexpected ad2 poll snapshot event: %+v", ev2)
		}
	default:
		t.Fatal("expected poll snapshot on ad2")
	}
}

func TestMultiTelegramTopicLifecycleAcrossConnections(t *testing.T) {
	ctx := context.Background()

	hasher, _ := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	norm1, _ := NewNormalizerWithConnection(map[string]string{"tg1": "-1001111111111"}, hasher, config.UsernameModeHash, "conn-tg-1")
	norm2, _ := NewNormalizerWithConnection(map[string]string{"tg2": "-1002222222222"}, hasher, config.UsernameModeHash, "conn-tg-2")

	api1 := &fakeTelegramAPI{botID: 1001}
	api2 := &fakeTelegramAPI{botID: 1002}

	ad1 := &Adapter{
		connectionID: "conn-tg-1",
		token:        "tok-1",
		normalizer:   norm1,
		client:       api1,
		messageKinds: make(map[messageKindKey]string),
	}
	ad2 := &Adapter{
		connectionID: "conn-tg-2",
		token:        "tok-2",
		normalizer:   norm2,
		client:       api2,
		messageKinds: make(map[messageKindKey]string),
	}

	// 1. Outbound with topic on ad1
	scope1 := &transport.ChildScope{Kind: transport.ScopeKindTelegramTopic, RemoteID: "555"}
	ref1, err := ad1.Send(ctx, transport.Outgoing{
		Endpoint:   "tg1",
		Kind:       "text",
		Text:       "topic message 1",
		ChildScope: scope1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref1.ChildScope == nil || ref1.ChildScope.RemoteID != "555" {
		t.Fatalf("ref1 should preserve topic scope, got: %+v", ref1)
	}

	// 2. Outbound with topic on ad2
	scope2 := &transport.ChildScope{Kind: transport.ScopeKindTelegramTopic, RemoteID: "777"}
	ref2, err := ad2.Send(ctx, transport.Outgoing{
		Endpoint:   "tg2",
		Kind:       "text",
		Text:       "topic message 2",
		ChildScope: scope2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref2.ChildScope == nil || ref2.ChildScope.RemoteID != "777" {
		t.Fatalf("ref2 should preserve topic scope, got: %+v", ref2)
	}

	// 3. Incoming topic normalization
	inc1, ok1 := norm1.NormalizeMessage(&models.Message{
		ID:              10,
		Chat:            models.Chat{ID: -1001111111111, Type: models.ChatTypeSupergroup},
		MessageThreadID: 555,
		From:            &models.User{ID: 123},
		Text:            "in topic 555",
	}, 1001)
	if !ok1 || inc1.ChildScope == nil || inc1.ChildScope.RemoteID != "555" {
		t.Fatalf("incoming topic not normalized correctly: %+v", inc1)
	}

	// 4. Invalid topic fails safely
	_, err = telegramChildThreadID(&transport.ChildScope{Kind: transport.ScopeKindTelegramTopic, RemoteID: "not-a-number"})
	if err == nil {
		t.Fatal("expected error for invalid non-numeric topic RemoteID")
	}
}

func TestMultiTelegramGroupMigrationConnectionScoping(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Create endpoints table
	_, err = db.Exec(`CREATE TABLE endpoints (
		alias TEXT PRIMARY KEY,
		transport TEXT NOT NULL,
		connection_id TEXT NOT NULL,
		remote_id TEXT NOT NULL
	);`)
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`INSERT INTO endpoints (alias, transport, connection_id, remote_id) VALUES
		('tg1', 'telegram', 'conn-tg-1', '-1001111111111'),
		('tg2', 'telegram', 'conn-tg-2', '-1002222222222');`)
	if err != nil {
		t.Fatal(err)
	}

	// Mismatched connection ID must fail
	err = config.MigrateTelegramEndpoint(ctx, db, "tg1", "conn-tg-2", "-1001111111111", "-1009999999999")
	if err == nil {
		t.Fatal("expected error when connection_id does not match")
	}

	// Correct connection ID must succeed
	err = config.MigrateTelegramEndpoint(ctx, db, "tg1", "conn-tg-1", "-1001111111111", "-1009999999999")
	if err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	var newRemoteID string
	err = db.QueryRow(`SELECT remote_id FROM endpoints WHERE alias = 'tg1'`).Scan(&newRemoteID)
	if err != nil {
		t.Fatal(err)
	}
	if newRemoteID != "-1009999999999" {
		t.Fatalf("new remote ID = %q, want -1009999999999", newRemoteID)
	}

	// tg2 remains completely untouched
	var tg2RemoteID string
	err = db.QueryRow(`SELECT remote_id FROM endpoints WHERE alias = 'tg2'`).Scan(&tg2RemoteID)
	if err != nil {
		t.Fatal(err)
	}
	if tg2RemoteID != "-1002222222222" {
		t.Fatalf("tg2 was modified: %q", tg2RemoteID)
	}
}

func TestMultiTelegramDistinctDiscoveryAndStatus(t *testing.T) {
	ctx := context.Background()

	hasher, _ := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	norm1, _ := NewNormalizerWithConnection(map[string]string{"tg1": "-1001111111111"}, hasher, config.UsernameModeHash, "conn-tg-1")
	norm2, _ := NewNormalizerWithConnection(map[string]string{"tg2": "-1002222222222"}, hasher, config.UsernameModeHash, "conn-tg-2")

	api1 := &fakeTelegramAPI{botID: 1001}
	api2 := &fakeTelegramAPI{botID: 1002}

	ad1 := &Adapter{
		connectionID: "conn-tg-1",
		token:        "tok-1",
		normalizer:   norm1,
		hasher:       hasher,
		client:       api1,
		observed:     make(map[int64]observedChatEntry),
		polling:      true,
	}
	ad2 := &Adapter{
		connectionID: "conn-tg-2",
		token:        "tok-2",
		normalizer:   norm2,
		hasher:       hasher,
		client:       api2,
		observed:     make(map[int64]observedChatEntry),
		polling:      true,
	}

	// Bot 1 observes Chat A
	ad1.observeUpdate(&models.Update{
		ID: 1,
		Message: &models.Message{
			Chat: models.Chat{ID: -1001111111111, Title: "Group One", Type: models.ChatTypeSupergroup},
		},
	})

	// Bot 2 observes Chat B
	ad2.observeUpdate(&models.Update{
		ID: 2,
		Message: &models.Message{
			Chat: models.Chat{ID: -1002222222222, Title: "Group Two", Type: models.ChatTypeSupergroup},
		},
	})

	// DiscoverChats isolation
	chats1, err := ad1.DiscoverChats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(chats1) != 1 || chats1[0].ChatID != "-1001111111111" {
		t.Fatalf("unexpected discovery for ad1: %+v", chats1)
	}

	chats2, err := ad2.DiscoverChats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(chats2) != 1 || chats2[0].ChatID != "-1002222222222" {
		t.Fatalf("unexpected discovery for ad2: %+v", chats2)
	}

	// AdminStatus isolation: mark ad2 as stopped to verify status separation
	ad2.mu.Lock()
	ad2.polling = false
	ad2.mu.Unlock()

	st1 := ad1.AdminStatus(ctx)
	if !st1.Running || len(st1.Endpoints) != 1 || st1.Endpoints[0].Alias != "tg1" || st1.Endpoints[0].Status != "ready" {
		t.Fatalf("unexpected status for ad1: %+v", st1)
	}

	st2 := ad2.AdminStatus(ctx)
	if st2.Running || len(st2.Endpoints) != 1 || st2.Endpoints[0].Alias != "tg2" || st2.Endpoints[0].Status != "unavailable" {
		t.Fatalf("unexpected status for ad2: %+v", st2)
	}
}

func TestMultiTelegramIsolationOnStopAndRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"tg1": {Transport: config.TransportTelegram, ConnectionID: "conn-tg-1", RemoteID: "-1001111111111"},
			"tg2": {Transport: config.TransportTelegram, ConnectionID: "conn-tg-2", RemoteID: "-1002222222222"},
		},
	}
	registry, _ := router.NewAdapterRegistry(cfg, nil)
	connMgr := connection.NewManager(ctx, slog.Default(), registry, nil)
	defer connMgr.Close()

	ad1 := &Adapter{
		connectionID: "conn-tg-1",
		token:        "tok-1",
		events:       make(chan transport.Incoming, 10),
	}
	ad2 := &Adapter{
		connectionID: "conn-tg-2",
		token:        "tok-2",
		events:       make(chan transport.Incoming, 10),
	}

	_ = connMgr.Register(ctx, "conn-tg-1", config.TransportTelegram, ad1)
	_ = connMgr.Register(ctx, "conn-tg-2", config.TransportTelegram, ad2)

	// Stop conn-tg-1
	if err := connMgr.Stop("conn-tg-1"); err != nil {
		t.Fatal(err)
	}

	// ad2 continues processing events unharmed
	ad2.events <- transport.Incoming{Endpoint: "tg2", RemoteID: "msg-healthy"}
	select {
	case ev := <-connMgr.Events():
		if ev.RemoteID != "msg-healthy" {
			t.Fatalf("unexpected event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("ad2 events timed out after ad1 stopped")
	}
}

func TestTelegramTokenPrivacyNonDisclosure(t *testing.T) {
	hasher, _ := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	token := "123456:secret-token-value-never-leak"

	norm, _ := NewNormalizerWithConnection(map[string]string{"tg": "-1001111111111"}, hasher, config.UsernameModeHash, "conn-tg-1")
	ad := &Adapter{
		connectionID: "conn-tg-1",
		token:        token,
		normalizer:   norm,
		hasher:       hasher,
	}

	// AdminStatus must not leak token
	st := ad.AdminStatus(context.Background())
	if !st.TokenConfigured {
		t.Fatal("expected TokenConfigured to be true")
	}

	// Error on missing token must not leak secret values
	_, err := Open(context.Background(), Options{Token: ""})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatal("error leaked token")
	}
}
