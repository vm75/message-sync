package connection

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/recovery"
	"github.com/vm75/message-sync/internal/router"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/transport"
)

type fakeAdapter struct {
	mu            sync.Mutex
	events        chan transport.Incoming
	sent          []transport.Outgoing
	closed        bool
	updatedConfig *config.Config

	// RecoverySource fields
	streams []string
	signals chan struct{}
	recLock sync.Mutex
	recRuns int
}

func newFakeAdapter() *fakeAdapter {
	return &fakeAdapter{
		events:  make(chan transport.Incoming, 10),
		signals: make(chan struct{}, 10),
	}
}

func (f *fakeAdapter) Events() <-chan transport.Incoming { return f.events }

func (f *fakeAdapter) Send(_ context.Context, outgoing transport.Outgoing) (transport.MessageRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, outgoing)
	return transport.MessageRef{
		Endpoint:        outgoing.Endpoint,
		RemoteMessageID: fmt.Sprintf("msg-%d", len(f.sent)),
		ChildScope:      outgoing.ChildScope,
	}, nil
}

func (f *fakeAdapter) React(context.Context, transport.Reaction) error          { return nil }
func (f *fakeAdapter) Edit(context.Context, transport.MessageRef, string) error { return nil }
func (f *fakeAdapter) Delete(context.Context, transport.MessageRef) error       { return nil }

func (f *fakeAdapter) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.events)
	}
	return nil
}

func (f *fakeAdapter) UpdateConfig(cfg *config.Config) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updatedConfig = cfg
	return nil
}

func (f *fakeAdapter) RecoveryStreams() []string {
	f.recLock.Lock()
	defer f.recLock.Unlock()
	return f.streams
}

func (f *fakeAdapter) Recover(_ context.Context, _ transport.RecoveryRequest, _ func(context.Context, transport.Incoming) error) error {
	f.recLock.Lock()
	defer f.recLock.Unlock()
	f.recRuns++
	return nil
}

func (f *fakeAdapter) RecoverySignals() <-chan struct{} {
	return f.signals
}

func TestManagerMultiAdapterSameTransportIngressAndDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"dc-1": {Transport: config.TransportDiscord, ConnectionID: "conn-dc-1", RemoteID: "111"},
			"dc-2": {Transport: config.TransportDiscord, ConnectionID: "conn-dc-2", RemoteID: "222"},
		},
	}

	registry, err := router.NewAdapterRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(ctx, nil, registry, nil)
	defer mgr.Close()

	ad1 := newFakeAdapter()
	ad2 := newFakeAdapter()

	if err := mgr.Register(ctx, "conn-dc-1", config.TransportDiscord, ad1); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Register(ctx, "conn-dc-2", config.TransportDiscord, ad2); err != nil {
		t.Fatal(err)
	}

	// Ingress from both adapters
	scope1 := &transport.ChildScope{Kind: transport.ScopeKindDiscordThread, RemoteID: "thread-1", Label: "Thread 1"}
	scope2 := &transport.ChildScope{Kind: transport.ScopeKindTelegramTopic, RemoteID: "topic-2", Label: "Topic 2"}

	ad1.events <- transport.Incoming{Endpoint: "dc-1", RemoteID: "m1", ChildScope: scope1}
	ad2.events <- transport.Incoming{Endpoint: "dc-2", RemoteID: "m2", ChildScope: scope2}

	received := make(map[transport.EndpointID]transport.Incoming)
	for i := 0; i < 2; i++ {
		select {
		case ev := <-mgr.Events():
			received[ev.Endpoint] = ev
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for event")
		}
	}

	ev1, ok1 := received["dc-1"]
	if !ok1 || ev1.ChildScope == nil || ev1.ChildScope.RemoteID != "thread-1" {
		t.Fatalf("unexpected dc-1 event: %+v", ev1)
	}
	ev2, ok2 := received["dc-2"]
	if !ok2 || ev2.ChildScope == nil || ev2.ChildScope.RemoteID != "topic-2" {
		t.Fatalf("unexpected dc-2 event: %+v", ev2)
	}

	// Outbound dispatch to both endpoints
	ref1, err := registry.Send(ctx, transport.Outgoing{Endpoint: "dc-1", ChildScope: scope1})
	if err != nil {
		t.Fatal(err)
	}
	if ref1.ChildScope == nil || ref1.ChildScope.RemoteID != "thread-1" {
		t.Fatalf("unexpected ref1 child scope: %+v", ref1.ChildScope)
	}

	ref2, err := registry.Send(ctx, transport.Outgoing{Endpoint: "dc-2", ChildScope: scope2})
	if err != nil {
		t.Fatal(err)
	}
	if ref2.ChildScope == nil || ref2.ChildScope.RemoteID != "topic-2" {
		t.Fatalf("unexpected ref2 child scope: %+v", ref2.ChildScope)
	}

	if len(ad1.sent) != 1 || len(ad2.sent) != 1 {
		t.Fatalf("expected each adapter to receive 1 send: ad1=%d, ad2=%d", len(ad1.sent), len(ad2.sent))
	}
}

func TestManagerStopConnectionIsolation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"ep-1": {Transport: config.TransportDiscord, ConnectionID: "conn-1", RemoteID: "111"},
			"ep-2": {Transport: config.TransportDiscord, ConnectionID: "conn-2", RemoteID: "222"},
		},
	}

	registry, err := router.NewAdapterRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(ctx, nil, registry, nil)
	defer mgr.Close()

	ad1 := newFakeAdapter()
	ad2 := newFakeAdapter()

	_ = mgr.Register(ctx, "conn-1", config.TransportDiscord, ad1)
	_ = mgr.Register(ctx, "conn-2", config.TransportDiscord, ad2)

	// Stop conn-1
	if err := mgr.Stop("conn-1"); err != nil {
		t.Fatal(err)
	}

	st, ok := mgr.Status("conn-1")
	if !ok || st.State != StateStopped {
		t.Fatalf("expected conn-1 StateStopped, got %+v", st)
	}

	// Dispatch to conn-1 should fail with safe unavailable error
	_, err = registry.Send(ctx, transport.Outgoing{Endpoint: "ep-1"})
	if err == nil {
		t.Fatal("expected error dispatching to stopped connection")
	}

	// conn-2 continues working normally
	ad2.events <- transport.Incoming{Endpoint: "ep-2", RemoteID: "live-2"}
	select {
	case ev := <-mgr.Events():
		if ev.Endpoint != "ep-2" {
			t.Fatalf("expected ev from ep-2, got: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event on conn-2")
	}

	_, err = registry.Send(ctx, transport.Outgoing{Endpoint: "ep-2"})
	if err != nil {
		t.Fatalf("conn-2 dispatch error: %v", err)
	}
}

func TestManagerAtomicRestartReplace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"ep-1": {Transport: config.TransportTelegram, ConnectionID: "conn-1", RemoteID: "111"},
		},
	}

	registry, err := router.NewAdapterRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(ctx, nil, registry, nil)
	defer mgr.Close()

	ad1 := newFakeAdapter()
	_ = mgr.Register(ctx, "conn-1", config.TransportTelegram, ad1)

	ad2 := newFakeAdapter()
	if err := mgr.Restart(ctx, "conn-1", ad2); err != nil {
		t.Fatal(err)
	}

	// ad1 should be closed
	ad1.mu.Lock()
	closed1 := ad1.closed
	ad1.mu.Unlock()
	if !closed1 {
		t.Fatal("expected ad1 to be closed after restart")
	}

	// Outbound goes to ad2
	_, err = registry.Send(ctx, transport.Outgoing{Endpoint: "ep-1", Text: "hello new adapter"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ad2.sent) != 1 || ad2.sent[0].Text != "hello new adapter" {
		t.Fatalf("expected ad2 to receive sent message, got: %+v", ad2.sent)
	}

	// Ingress flows from ad2
	ad2.events <- transport.Incoming{Endpoint: "ep-1", RemoteID: "from-ad2"}
	select {
	case ev := <-mgr.Events():
		if ev.RemoteID != "from-ad2" {
			t.Fatalf("expected from-ad2, got %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event from ad2")
	}
}

func TestManagerDynamicRecoveryRegistration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	syncStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer syncStore.Close()

	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"wa-1": {Transport: config.TransportWhatsApp, ConnectionID: "conn-wa-1", RemoteID: "111@g.us"},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Endpoints: []string{"wa-1"}}},
		Identity: config.Identity{UsernameMode: config.UsernameModeHash},
	}

	registry, err := router.NewAdapterRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	mesh, err := router.New(cfg, syncStore, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer mesh.Close()

	coord, err := recovery.NewCoordinator(syncStore, mesh)
	if err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(ctx, nil, registry, coord)
	defer mgr.Close()

	ad := newFakeAdapter()
	ad.streams = []string{"stream-wa-1"}

	if err := mgr.Register(ctx, "conn-wa-1", config.TransportWhatsApp, ad); err != nil {
		t.Fatal(err)
	}

	// Initial recovery run
	for i := 0; i < 50; i++ {
		ad.recLock.Lock()
		runs := ad.recRuns
		ad.recLock.Unlock()
		if runs >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	ad.recLock.Lock()
	if ad.recRuns < 1 {
		ad.recLock.Unlock()
		t.Fatalf("expected at least 1 recovery run on register, got %d", ad.recRuns)
	}
	ad.recLock.Unlock()

	// Signal reconnect
	ad.signals <- struct{}{}
	for i := 0; i < 50; i++ {
		ad.recLock.Lock()
		runs := ad.recRuns
		ad.recLock.Unlock()
		if runs >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	ad.recLock.Lock()
	if ad.recRuns < 2 {
		ad.recLock.Unlock()
		t.Fatalf("expected 2 recovery runs after signal, got %d", ad.recRuns)
	}
	ad.recLock.Unlock()

	// Stop connection
	if err := mgr.Stop("conn-wa-1"); err != nil {
		t.Fatal(err)
	}

	// New signals should not trigger recovery
	ad.signals <- struct{}{}
	time.Sleep(50 * time.Millisecond)
	ad.recLock.Lock()
	if ad.recRuns != 2 {
		ad.recLock.Unlock()
		t.Fatalf("recovery ran after connection stopped: runs=%d", ad.recRuns)
	}
	ad.recLock.Unlock()
}

func TestManagerConcurrentOperations(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"ep-1": {Transport: config.TransportDiscord, ConnectionID: "conn-1", RemoteID: "111"},
			"ep-2": {Transport: config.TransportTelegram, ConnectionID: "conn-2", RemoteID: "222"},
		},
	}

	registry, err := router.NewAdapterRegistry(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	mgr := NewManager(ctx, nil, registry, nil)

	ad1 := newFakeAdapter()
	ad2 := newFakeAdapter()

	_ = mgr.Register(ctx, "conn-1", config.TransportDiscord, ad1)
	_ = mgr.Register(ctx, "conn-2", config.TransportTelegram, ad2)

	var wg sync.WaitGroup

	// Reader draining manager events
	doneDraining := make(chan struct{})
	go func() {
		for range mgr.Events() {
		}
		close(doneDraining)
	}()

	// Producer 1
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			select {
			case ad1.events <- transport.Incoming{Endpoint: "ep-1", RemoteID: fmt.Sprintf("m-%d", i)}:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Producer 2
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			select {
			case ad2.events <- transport.Incoming{Endpoint: "ep-2", RemoteID: fmt.Sprintf("m-%d", i)}:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Config updater
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_ = mgr.UpdateConfig(cfg)
			time.Sleep(2 * time.Millisecond)
		}
	}()

	// Status reader
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_ = mgr.ListStatuses()
			time.Sleep(2 * time.Millisecond)
		}
	}()

	wg.Wait()
	_ = mgr.Close()
	cancel()
}
