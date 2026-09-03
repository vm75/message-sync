package discord

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/connection"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/router"
	"github.com/vm75/message-sync/internal/transport"
)

type threadedFakeWebhook struct {
	fakeChannelWebhook
	threadExecutes []string
	threadEdits    []string
	threadDeletes  []string
	readinessMap   map[string]WebhookStatus
}

func (t *threadedFakeWebhook) ExecuteInThread(_ context.Context, channelID, threadID string, message WebhookMessage) (string, error) {
	t.threadExecutes = append(t.threadExecutes, channelID+"|"+threadID+"|"+message.Content)
	t.nextID++
	return fmt.Sprintf("threaded-copy-%d", t.nextID), nil
}

func (t *threadedFakeWebhook) EditInThread(_ context.Context, channelID, threadID, messageID, content string) error {
	t.threadEdits = append(t.threadEdits, channelID+"|"+threadID+"|"+messageID+"|"+content)
	return nil
}

func (t *threadedFakeWebhook) DeleteInThread(_ context.Context, channelID, threadID, messageID string) error {
	t.threadDeletes = append(t.threadDeletes, channelID+"|"+threadID+"|"+messageID)
	return nil
}

func (t *threadedFakeWebhook) Readiness(channelID string) WebhookStatus {
	if t.readinessMap != nil {
		if st, ok := t.readinessMap[channelID]; ok {
			return st
		}
	}
	return WebhookStatusReady
}

type fakeAdminAPI struct {
	guilds   []*discordgo.UserGuild
	channels map[string][]*discordgo.Channel
}

func (f *fakeAdminAPI) UserGuilds(_ int, _, _ string, _ bool, _ ...discordgo.RequestOption) ([]*discordgo.UserGuild, error) {
	return f.guilds, nil
}

func (f *fakeAdminAPI) GuildChannels(guildID string, _ ...discordgo.RequestOption) ([]*discordgo.Channel, error) {
	return f.channels[guildID], nil
}

func TestMultiDiscordConnectionsConcurrentOutboundAndIngress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"d1": {Transport: config.TransportDiscord, ConnectionID: "conn-dc-1", RemoteID: "100000000000000001"},
			"d2": {Transport: config.TransportDiscord, ConnectionID: "conn-dc-1", RemoteID: "100000000000000002"},
			"d3": {Transport: config.TransportDiscord, ConnectionID: "conn-dc-2", RemoteID: "100000000000000003"},
		},
		SyncSets: []config.SyncSet{
			{ID: "mesh", Endpoints: []string{"d1", "d2", "d3"}},
		},
		Identity: config.Identity{UsernameMode: config.UsernameModeHash},
	}

	norm1, _ := NewNormalizer(map[string]string{
		"d1": "100000000000000001",
		"d2": "100000000000000002",
	}, hasher, config.UsernameModeHash)
	wh1 := &threadedFakeWebhook{fakeChannelWebhook: fakeChannelWebhook{managed: make(map[string]string)}}
	ad1 := &Adapter{
		connectionID:      "conn-dc-1",
		normalizer:        norm1,
		hasher:            hasher,
		webhook:           wh1,
		targets:           map[transport.EndpointID]string{"d1": "100000000000000001", "d2": "100000000000000002"},
		events:            make(chan transport.Incoming, 10),
		reactionState:     make(map[reactionKey]string),
		suppressedDeletes: make(map[string]struct{}),
		historyStatus:     make(map[string]HistoryStatus),
	}

	norm2, _ := NewNormalizer(map[string]string{
		"d3": "100000000000000003",
	}, hasher, config.UsernameModeHash)
	wh2 := &threadedFakeWebhook{fakeChannelWebhook: fakeChannelWebhook{managed: make(map[string]string)}}
	ad2 := &Adapter{
		connectionID:      "conn-dc-2",
		normalizer:        norm2,
		hasher:            hasher,
		webhook:           wh2,
		targets:           map[transport.EndpointID]string{"d3": "100000000000000003"},
		events:            make(chan transport.Incoming, 10),
		reactionState:     make(map[reactionKey]string),
		suppressedDeletes: make(map[string]struct{}),
		historyStatus:     make(map[string]HistoryStatus),
	}

	registry, err := router.NewAdapterRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	connMgr := connection.NewManager(ctx, slog.Default(), registry, nil)
	defer connMgr.Close()

	if err := connMgr.Register(ctx, "conn-dc-1", config.TransportDiscord, ad1); err != nil {
		t.Fatal(err)
	}
	if err := connMgr.Register(ctx, "conn-dc-2", config.TransportDiscord, ad2); err != nil {
		t.Fatal(err)
	}

	// 1. Outbound dispatch verification
	_, err = registry.Send(ctx, transport.Outgoing{Endpoint: "d1", Text: "msg for d1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(wh1.executed) != 1 || wh1.channels[0] != "100000000000000001" {
		t.Fatalf("expected d1 to route through wh1, got channels: %+v", wh1.channels)
	}
	if len(wh2.executed) != 0 {
		t.Fatal("wh2 should not have received message for d1")
	}

	_, err = registry.Send(ctx, transport.Outgoing{Endpoint: "d3", Text: "msg for d3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(wh2.executed) != 1 || wh2.channels[0] != "100000000000000003" {
		t.Fatalf("expected d3 to route through wh2, got channels: %+v", wh2.channels)
	}
	if len(wh1.executed) != 1 {
		t.Fatal("wh1 should not have received message for d3")
	}

	// 2. Ingress isolation:
	// Event from d1 emitted on ad1 should reach connMgr.Events()
	ad1.events <- transport.Incoming{Endpoint: "d1", RemoteID: "m-d1"}
	select {
	case ev := <-connMgr.Events():
		if ev.Endpoint != "d1" || ev.RemoteID != "m-d1" {
			t.Fatalf("unexpected event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for d1 event")
	}

	// Event for channel 1 sent to ad2's normalizer should be ignored
	_, ok := norm2.NormalizeMessage(&discordgo.MessageCreate{
		Message: &discordgo.Message{
			ID:        "msg-unowned",
			ChannelID: "100000000000000001", // owned by conn-dc-1, NOT conn-dc-2
			Content:   "intruder message",
			Author:    &discordgo.User{ID: "user-123"},
		},
	}, "bot-2", wh2)
	if ok {
		t.Fatal("ad2 normalizer must reject unowned channel 100000000000000001")
	}
}

func TestMultiDiscordDistinctDiscoveryAndStatus(t *testing.T) {
	ctx := context.Background()

	wh1 := &threadedFakeWebhook{readinessMap: map[string]WebhookStatus{"100000000000000001": WebhookStatusReady, "100000000000000002": WebhookStatusMissingPermission}}
	api1 := &fakeAdminAPI{
		guilds: []*discordgo.UserGuild{{ID: "guild-1", Name: "Guild One"}},
		channels: map[string][]*discordgo.Channel{
			"guild-1": {
				{ID: "100000000000000001", Name: "chan-1", Type: discordgo.ChannelTypeGuildText},
				{ID: "100000000000000002", Name: "chan-2", Type: discordgo.ChannelTypeGuildText},
			},
		},
	}
	ad1 := &Adapter{
		connectionID:  "conn-dc-1",
		adminAPI:      api1,
		webhook:       wh1,
		targets:       map[transport.EndpointID]string{"d1": "100000000000000001", "d2": "100000000000000002"},
		connected:     true,
		historyStatus: map[string]HistoryStatus{"d1": HistoryStatusReady, "d2": HistoryStatusUnknown},
	}

	wh2 := &threadedFakeWebhook{readinessMap: map[string]WebhookStatus{"100000000000000003": WebhookStatusReady}}
	api2 := &fakeAdminAPI{
		guilds: []*discordgo.UserGuild{{ID: "guild-2", Name: "Guild Two"}},
		channels: map[string][]*discordgo.Channel{
			"guild-2": {
				{ID: "100000000000000003", Name: "chan-3", Type: discordgo.ChannelTypeGuildText},
			},
		},
	}
	ad2 := &Adapter{
		connectionID:  "conn-dc-2",
		adminAPI:      api2,
		webhook:       wh2,
		targets:       map[transport.EndpointID]string{"d3": "100000000000000003"},
		connected:     true,
		historyStatus: map[string]HistoryStatus{"d3": HistoryStatusUnavailable},
	}

	// Distinct discovery
	chans1, err := ad1.DiscoverChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(chans1) != 2 || chans1[0].GuildID != "guild-1" {
		t.Fatalf("unexpected discovery for ad1: %+v", chans1)
	}

	chans2, err := ad2.DiscoverChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(chans2) != 1 || chans2[0].GuildID != "guild-2" {
		t.Fatalf("unexpected discovery for ad2: %+v", chans2)
	}

	// Disconnect ad2 to test distinct status reporting
	ad2.connected = false
	if _, err := ad2.DiscoverChannels(ctx); err == nil {
		t.Fatal("expected error discovering channels when ad2 is disconnected")
	}

	// Distinct status
	st1 := ad1.AdminStatus(ctx)
	if !st1.Connected || len(st1.Webhooks) != 2 || len(st1.History) != 2 {
		t.Fatalf("unexpected status for ad1: %+v", st1)
	}

	st2 := ad2.AdminStatus(ctx)
	if st2.Connected || len(st2.Webhooks) != 1 || len(st2.History) != 1 {
		t.Fatalf("unexpected status for ad2: %+v", st2)
	}
	if st2.Webhooks[0].Alias != "d3" || st2.History[0].Alias != "d3" {
		t.Fatalf("ad2 status should only list d3: %+v", st2)
	}
}

type fakeWebhookAPIWithUsers struct {
	mu           sync.Mutex
	webhooks     map[string][]*discordgo.Webhook
	createdCount int
}

func (f *fakeWebhookAPIWithUsers) ChannelWebhooks(channelID string, _ ...discordgo.RequestOption) ([]*discordgo.Webhook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.webhooks[channelID], nil
}

func (f *fakeWebhookAPIWithUsers) WebhookCreate(channelID, name, avatar string, _ ...discordgo.RequestOption) (*discordgo.Webhook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createdCount++
	wh := &discordgo.Webhook{
		ID:        fmt.Sprintf("wh-created-%d", f.createdCount),
		Name:      name,
		ChannelID: channelID,
		Token:     fmt.Sprintf("tok-%d", f.createdCount),
	}
	f.webhooks[channelID] = append(f.webhooks[channelID], wh)
	return wh, nil
}

func (f *fakeWebhookAPIWithUsers) WebhookExecute(string, string, bool, *discordgo.WebhookParams, ...discordgo.RequestOption) (*discordgo.Message, error) {
	return &discordgo.Message{ID: "sent"}, nil
}
func (f *fakeWebhookAPIWithUsers) WebhookMessageEdit(string, string, string, *discordgo.WebhookEdit, ...discordgo.RequestOption) (*discordgo.Message, error) {
	return &discordgo.Message{ID: "edited"}, nil
}
func (f *fakeWebhookAPIWithUsers) WebhookMessageDelete(string, string, string, ...discordgo.RequestOption) error {
	return nil
}

func TestMultiDiscordSameManagedWebhookNameIsolation(t *testing.T) {
	ctx := context.Background()

	// Channel has existing webhook created by bot-user-1
	sharedAPI := &fakeWebhookAPIWithUsers{
		webhooks: map[string][]*discordgo.Webhook{
			"chan-shared": {
				{
					ID:        "wh-bot-1",
					Name:      managedWebhookName,
					ChannelID: "chan-shared",
					Token:     "tok-bot-1",
					Type:      discordgo.WebhookTypeIncoming,
					User:      &discordgo.User{ID: "bot-user-1"},
				},
			},
		},
	}

	// Client 1 with bot-user-1
	client1 := newManagedWebhookClient(nil)
	client1.api = sharedAPI
	client1.botUserID = func() string { return "bot-user-1" }

	if err := client1.Prepare(ctx, []string{"chan-shared"}); err != nil {
		t.Fatal(err)
	}
	cred1, ok1 := client1.credential("chan-shared")
	if !ok1 || cred1.id != "wh-bot-1" || cred1.token != "tok-bot-1" {
		t.Fatalf("client1 should reuse its own webhook: %+v", cred1)
	}

	// Client 2 with bot-user-2 must NOT reuse bot-user-1's webhook!
	client2 := newManagedWebhookClient(nil)
	client2.api = sharedAPI
	client2.botUserID = func() string { return "bot-user-2" }

	if err := client2.Prepare(ctx, []string{"chan-shared"}); err != nil {
		t.Fatal(err)
	}
	cred2, ok2 := client2.credential("chan-shared")
	if !ok2 || cred2.id == "wh-bot-1" || cred2.token == "tok-bot-1" {
		t.Fatalf("client2 must not reuse bot-user-1 webhook: %+v", cred2)
	}
	if cred2.id != "wh-created-1" {
		t.Fatalf("client2 should have created its own webhook: %+v", cred2)
	}
}

func TestMultiDiscordThreadLifecycleAcrossConnections(t *testing.T) {
	ctx := context.Background()
	wh1 := &threadedFakeWebhook{fakeChannelWebhook: fakeChannelWebhook{managed: make(map[string]string)}}
	wh2 := &threadedFakeWebhook{fakeChannelWebhook: fakeChannelWebhook{managed: make(map[string]string)}}

	ad1 := &Adapter{
		connectionID:      "conn-dc-1",
		webhook:           wh1,
		targets:           map[transport.EndpointID]string{"d1": "chan-1"},
		reactionState:     make(map[reactionKey]string),
		suppressedDeletes: make(map[string]struct{}),
	}
	ad2 := &Adapter{
		connectionID:      "conn-dc-2",
		webhook:           wh2,
		targets:           map[transport.EndpointID]string{"d3": "chan-3"},
		reactionState:     make(map[reactionKey]string),
		suppressedDeletes: make(map[string]struct{}),
	}

	scope1 := &transport.ChildScope{Kind: transport.ScopeKindDiscordThread, RemoteID: "thread-991"}
	scope2 := &transport.ChildScope{Kind: transport.ScopeKindDiscordThread, RemoteID: "thread-992"}

	// Send in thread via ad1
	ref1, err := ad1.Send(ctx, transport.Outgoing{
		Endpoint:   "d1",
		Text:       "thread msg 1",
		ChildScope: scope1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref1.ChildScope == nil || ref1.ChildScope.RemoteID != "thread-991" {
		t.Fatalf("unexpected child scope ref1: %+v", ref1)
	}
	if len(wh1.threadExecutes) != 1 || wh1.threadExecutes[0] != "chan-1|thread-991|thread msg 1" {
		t.Fatalf("unexpected wh1 thread executes: %+v", wh1.threadExecutes)
	}

	// Send in thread via ad2
	ref2, err := ad2.Send(ctx, transport.Outgoing{
		Endpoint:   "d3",
		Text:       "thread msg 2",
		ChildScope: scope2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref2.ChildScope == nil || ref2.ChildScope.RemoteID != "thread-992" {
		t.Fatalf("unexpected child scope ref2: %+v", ref2)
	}
	if len(wh2.threadExecutes) != 1 || wh2.threadExecutes[0] != "chan-3|thread-992|thread msg 2" {
		t.Fatalf("unexpected wh2 thread executes: %+v", wh2.threadExecutes)
	}

	// Edit and delete in thread via ad1
	if err := ad1.Edit(ctx, ref1, "edited thread 1"); err != nil {
		t.Fatal(err)
	}
	if len(wh1.threadEdits) != 1 {
		t.Fatalf("expected 1 thread edit on wh1, got: %+v", wh1.threadEdits)
	}

	if err := ad1.Delete(ctx, ref1); err != nil {
		t.Fatal(err)
	}
	if len(wh1.threadDeletes) != 1 {
		t.Fatalf("expected 1 thread delete on wh1, got: %+v", wh1.threadDeletes)
	}
}

func TestMultiDiscordIsolationOnDisconnectAndRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"d1": {Transport: config.TransportDiscord, ConnectionID: "conn-dc-1", RemoteID: "100000000000000001"},
			"d2": {Transport: config.TransportDiscord, ConnectionID: "conn-dc-2", RemoteID: "100000000000000002"},
		},
	}
	registry, _ := router.NewAdapterRegistry(cfg, nil)
	connMgr := connection.NewManager(ctx, slog.Default(), registry, nil)
	defer connMgr.Close()

	sig1 := make(chan struct{}, 5)
	sig2 := make(chan struct{}, 5)

	ad1 := &Adapter{
		connectionID:    "conn-dc-1",
		targets:         map[transport.EndpointID]string{"d1": "100000000000000001"},
		events:          make(chan transport.Incoming, 10),
		recoverySignals: sig1,
		webhook:         &fakeChannelWebhook{managed: make(map[string]string)},
	}
	ad2 := &Adapter{
		connectionID:    "conn-dc-2",
		targets:         map[transport.EndpointID]string{"d2": "100000000000000002"},
		events:          make(chan transport.Incoming, 10),
		recoverySignals: sig2,
		webhook:         &fakeChannelWebhook{managed: make(map[string]string)},
	}

	_ = connMgr.Register(ctx, "conn-dc-1", config.TransportDiscord, ad1)
	_ = connMgr.Register(ctx, "conn-dc-2", config.TransportDiscord, ad2)

	// Disconnect / stop conn-dc-1
	if err := connMgr.Stop("conn-dc-1"); err != nil {
		t.Fatal(err)
	}

	// Reconnect signal on ad1
	ad1.signalRecovery()
	select {
	case <-sig1:
	default:
		t.Fatal("ad1 signalRecovery should send to sig1")
	}

	// ad2 should have received NO recovery signals
	select {
	case <-sig2:
		t.Fatal("ad2 received spurious recovery signal")
	default:
	}

	// ad2 continues processing events
	ad2.events <- transport.Incoming{Endpoint: "d2", RemoteID: "msg-healthy"}
	select {
	case ev := <-connMgr.Events():
		if ev.RemoteID != "msg-healthy" {
			t.Fatalf("unexpected event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("ad2 events timed out after ad1 stopped")
	}

	// Outbound to d2 works
	_, err := registry.Send(ctx, transport.Outgoing{Endpoint: "d2", Text: "live"})
	if err != nil {
		t.Fatalf("ad2 outbound failed: %v", err)
	}
}

func TestMultiDiscordEndpointReassignment(t *testing.T) {
	hasher, _ := identity.New([]byte("0123456789abcdef0123456789abcdef"))

	wh1 := &threadedFakeWebhook{fakeChannelWebhook: fakeChannelWebhook{managed: make(map[string]string)}}
	wh2 := &threadedFakeWebhook{fakeChannelWebhook: fakeChannelWebhook{managed: make(map[string]string)}}

	ad1 := &Adapter{
		connectionID:  "conn-dc-1",
		hasher:        hasher,
		webhook:       wh1,
		targets:       map[transport.EndpointID]string{"d1": "100000000000000001"},
		historyStatus: make(map[string]HistoryStatus),
	}
	ad2 := &Adapter{
		connectionID:  "conn-dc-2",
		hasher:        hasher,
		webhook:       wh2,
		targets:       map[transport.EndpointID]string{"d2": "100000000000000002"},
		historyStatus: make(map[string]HistoryStatus),
	}

	// Reassign d1 from conn-dc-1 to conn-dc-2
	newCfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"d1": {Transport: config.TransportDiscord, ConnectionID: "conn-dc-2", RemoteID: "100000000000000001"},
			"d2": {Transport: config.TransportDiscord, ConnectionID: "conn-dc-2", RemoteID: "100000000000000002"},
		},
		Identity: config.Identity{UsernameMode: config.UsernameModeHash},
	}

	if err := ad1.UpdateConfig(newCfg); err != nil {
		t.Fatal(err)
	}
	if err := ad2.UpdateConfig(newCfg); err != nil {
		t.Fatal(err)
	}

	ad1.mu.RLock()
	if len(ad1.targets) != 0 {
		t.Fatalf("ad1 should have 0 targets after d1 reassigned, got: %+v", ad1.targets)
	}
	ad1.mu.RUnlock()

	ad2.mu.RLock()
	if len(ad2.targets) != 2 || ad2.targets["d1"] != "100000000000000001" || ad2.targets["d2"] != "100000000000000002" {
		t.Fatalf("ad2 should own both d1 and d2, got: %+v", ad2.targets)
	}
	ad2.mu.RUnlock()
}
