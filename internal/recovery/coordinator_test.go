package recovery

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/router"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/transport"
)

func recoveryConfig() *config.Config {
	return &config.Config{
		Endpoints: map[string]config.Endpoint{
			"one": {Transport: config.TransportWhatsApp, RemoteID: "111@g.us"},
			"two": {Transport: config.TransportWhatsApp, RemoteID: "222@g.us"},
		},
		SyncSets: []config.SyncSet{{ID: "set", Endpoints: []string{"one", "two"}}},
		Identity: config.Identity{UsernameMode: config.UsernameModeHash},
	}
}

type recoverySender struct{ fail bool }

func (s *recoverySender) Send(context.Context, transport.Outgoing) (transport.MessageRef, error) {
	if s.fail {
		return transport.MessageRef{}, errors.New("send failed")
	}
	return transport.MessageRef{Endpoint: "two", RemoteMessageID: "remote"}, nil
}
func (*recoverySender) React(context.Context, transport.Reaction) error          { return nil }
func (*recoverySender) Edit(context.Context, transport.MessageRef, string) error { return nil }
func (*recoverySender) Delete(context.Context, transport.MessageRef) error       { return nil }

func recoveryEvent(position int64, remote string) transport.Incoming {
	return transport.Incoming{
		Endpoint: "one", RemoteID: remote, Kind: "text", Text: "transient",
		Sender: transport.Sender{OpaqueID: "u_abcdefghij"}, Timestamp: time.Unix(1700000000+position, 0).UTC(),
		Checkpoint: transport.Checkpoint{StreamKey: "one", Position: position, EventTimestamp: time.Unix(1700000000+position, 0).UTC(), Valid: true},
	}
}

func TestCoordinatorAdvancesAcceptedCheckpointAndDeduplicatesReplay(t *testing.T) {
	ctx := context.Background()
	syncStore, err := store.Open(ctx, t.TempDir()+"/sync.db")
	if err != nil {
		t.Fatal(err)
	}
	defer syncStore.Close()
	canonicalRouter, err := router.New(recoveryConfig(), syncStore, &recoverySender{})
	if err != nil {
		t.Fatal(err)
	}
	defer canonicalRouter.Close()
	coordinator, err := NewCoordinator(syncStore, canonicalRouter)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Handle(ctx, recoveryEvent(1, "first")); err != nil {
		t.Fatal(err)
	}
	cursor, err := syncStore.RecoveryCursor(ctx, "one")
	if err != nil || cursor.Position != 1 {
		t.Fatalf("cursor = %+v, err = %v", cursor, err)
	}
	if _, err := coordinator.Handle(ctx, recoveryEvent(1, "first")); err != nil {
		t.Fatal(err)
	}
	cursor, err = syncStore.RecoveryCursor(ctx, "one")
	if err != nil || cursor.Position != 1 {
		t.Fatalf("duplicate changed cursor = %+v, err = %v", cursor, err)
	}
}

func TestCoordinatorDoesNotAdvanceCheckpointForIgnoredEvent(t *testing.T) {
	ctx := context.Background()
	syncStore, err := store.Open(ctx, t.TempDir()+"/sync.db")
	if err != nil {
		t.Fatal(err)
	}
	defer syncStore.Close()
	canonicalRouter, err := router.New(recoveryConfig(), syncStore, &recoverySender{})
	if err != nil {
		t.Fatal(err)
	}
	defer canonicalRouter.Close()
	coordinator, err := NewCoordinator(syncStore, canonicalRouter)
	if err != nil {
		t.Fatal(err)
	}

	ignored := recoveryEvent(7, "ignored")
	ignored.Kind = "other"
	if _, err := coordinator.Handle(ctx, ignored); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Handle(ctx, recoveryEvent(7, "real-message")); err != nil {
		t.Fatal(err)
	}
	metrics, err := syncStore.Metrics(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if metrics.CanonicalMessages != 1 || metrics.MessageCopies != 1 {
		t.Fatalf("ignored event blocked real message: %+v", metrics)
	}
}

func TestCoordinatorGapStaysBlockedUntilFailedPositionIsAccepted(t *testing.T) {
	ctx := context.Background()
	syncStore, err := store.Open(ctx, t.TempDir()+"/sync.db")
	if err != nil {
		t.Fatal(err)
	}
	defer syncStore.Close()
	canonicalRouter, err := router.New(recoveryConfig(), syncStore, &recoverySender{})
	if err != nil {
		t.Fatal(err)
	}
	defer canonicalRouter.Close()
	coordinator, err := NewCoordinator(syncStore, canonicalRouter)
	if err != nil {
		t.Fatal(err)
	}
	failed := recoveryEvent(1, "")
	if _, err := coordinator.Handle(ctx, failed); err == nil {
		t.Fatal("invalid event unexpectedly accepted")
	}
	if _, err := coordinator.Handle(ctx, recoveryEvent(2, "second")); err != nil {
		t.Fatal(err)
	}
	if _, err := syncStore.RecoveryCursor(ctx, "one"); err == nil {
		t.Fatal("cursor advanced across failed position")
	}
	if _, err := coordinator.Handle(ctx, recoveryEvent(1, "first")); err != nil {
		t.Fatal(err)
	}
	cursor, err := syncStore.RecoveryCursor(ctx, "one")
	if err != nil || cursor.Position != 2 {
		t.Fatalf("cursor after gap repair = %+v, err = %v", cursor, err)
	}
}

type testRecoverySource struct {
	signals           chan struct{}
	started           chan struct{}
	active, maxActive int
	mu                sync.Mutex
}

func (s *testRecoverySource) RecoveryStreams() []string        { return []string{"one"} }
func (s *testRecoverySource) RecoverySignals() <-chan struct{} { return s.signals }
func (s *testRecoverySource) Recover(ctx context.Context, request transport.RecoveryRequest, emit func(context.Context, transport.Incoming) error) error {
	if request.MaxEvents != DefaultMaxEvents || request.MaxAge != DefaultMaxAge || !request.Cursor.Valid {
		return errors.New("unexpected recovery bounds")
	}
	s.mu.Lock()
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	s.mu.Unlock()
	select {
	case s.started <- struct{}{}:
	default:
	}
	defer func() { s.mu.Lock(); s.active--; s.mu.Unlock() }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(10 * time.Millisecond):
		return nil
	}
}

func TestCoordinatorRecoveryIsSingleFlightAndCancellable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	syncStore, err := store.Open(ctx, t.TempDir()+"/sync.db")
	if err != nil {
		t.Fatal(err)
	}
	defer syncStore.Close()
	canonicalRouter, err := router.New(recoveryConfig(), syncStore, &recoverySender{})
	if err != nil {
		t.Fatal(err)
	}
	defer canonicalRouter.Close()
	coordinator, err := NewCoordinator(syncStore, canonicalRouter)
	if err != nil {
		t.Fatal(err)
	}
	source := &testRecoverySource{signals: make(chan struct{}, 2), started: make(chan struct{}, 2)}
	go coordinator.Recover(ctx, []transport.RecoverySource{source})
	go coordinator.Recover(ctx, []transport.RecoverySource{source})
	select {
	case <-source.started:
	case <-time.After(time.Second):
		t.Fatal("recovery did not start")
	}
	cancel()
	time.Sleep(20 * time.Millisecond)
	source.mu.Lock()
	maxActive := source.maxActive
	source.mu.Unlock()
	if maxActive != 1 {
		t.Fatalf("recovery ran concurrently, max active = %d", maxActive)
	}
}
