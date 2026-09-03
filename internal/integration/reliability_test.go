package integration

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

type fakeAdapter struct {
	mu          sync.Mutex
	name        string
	sequence    []string
	delay       <-chan struct{}
	failures    int
	failure     error
	attempts    int
	successes   int
	lastMessage string
}

func (f *fakeAdapter) Send(ctx context.Context, outgoing transport.Outgoing) (transport.MessageRef, error) {
	if f.delay != nil {
		select {
		case <-f.delay:
		case <-ctx.Done():
			return transport.MessageRef{}, ctx.Err()
		}
	}
	f.mu.Lock()
	f.attempts++
	if f.failures > 0 {
		f.failures--
		failure := f.failure
		f.mu.Unlock()
		return transport.MessageRef{}, failure
	}
	f.successes++
	ref := transport.MessageRef{Endpoint: outgoing.Endpoint, RemoteMessageID: f.name + "-copy", IsTargetFromMe: true}
	f.sequence = append(f.sequence, "create")
	f.lastMessage = outgoing.Text
	f.mu.Unlock()
	return ref, nil
}

func (f *fakeAdapter) React(context.Context, transport.Reaction) error {
	f.mu.Lock()
	f.sequence = append(f.sequence, "reaction")
	f.mu.Unlock()
	return nil
}

func (f *fakeAdapter) Edit(context.Context, transport.MessageRef, string) error {
	f.mu.Lock()
	f.sequence = append(f.sequence, "edit")
	f.mu.Unlock()
	return nil
}

func (f *fakeAdapter) Delete(context.Context, transport.MessageRef) error {
	f.mu.Lock()
	f.sequence = append(f.sequence, "delete")
	f.mu.Unlock()
	return nil
}

func (f *fakeAdapter) snapshot() (attempts, successes int, sequence []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts, f.successes, append([]string(nil), f.sequence...)
}

func (f *fakeAdapter) waitFor(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, successes, _ := f.snapshot()
		if successes >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	attempts, successes, sequence := f.snapshot()
	t.Fatalf("%s successes = %d, want %d (attempts=%d sequence=%v)", f.name, successes, want, attempts, sequence)
}

func (f *fakeAdapter) Name() string { return f.name }

func reliabilityConfig() *config.Config {
	return &config.Config{
		Endpoints: map[string]config.Endpoint{
			"wa": {Transport: config.TransportWhatsApp, ConnectionID: "conn-wa-1", RemoteID: "111@g.us"},
			"dc": {Transport: config.TransportDiscord, ConnectionID: "conn-dc-1", RemoteID: "222"},
			"tg": {Transport: config.TransportTelegram, ConnectionID: "conn-tg-1", RemoteID: "-333"},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Endpoints: []string{"wa", "dc", "tg"}}},
		Identity: config.Identity{UsernameMode: config.UsernameModeHash},
	}
}

func TestThreeTransportHarnessIsolatesDeliveryAndPreservesLifecycleOrder(t *testing.T) {
	ctx := context.Background()
	syncStore, err := store.Open(ctx, t.TempDir()+"/sync.db")
	if err != nil {
		t.Fatal(err)
	}
	defer syncStore.Close()

	releaseSlow := make(chan struct{})
	wa := &fakeAdapter{name: "wa"}
	dc := &fakeAdapter{name: "dc", delay: releaseSlow}
	tg := &fakeAdapter{name: "tg"}
	registry, err := router.NewAdapterRegistry(reliabilityConfig(), map[string]router.OutboundAdapter{
		"conn-wa-1": wa,
		"conn-dc-1": dc,
		"conn-tg-1": tg,
	})
	if err != nil {
		t.Fatal(err)
	}
	mesh, err := router.New(reliabilityConfig(), syncStore, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer mesh.Close()

	if err := mesh.Handle(ctx, transport.Incoming{
		Endpoint: "wa", RemoteID: "wa-message-1", Kind: "text", Text: "canary body",
		Sender: transport.Sender{OpaqueID: "u_abcdefghij"}, Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	tg.waitFor(t, 1)
	close(releaseSlow)
	dc.waitFor(t, 1)
	if attempts, successes, _ := dc.snapshot(); attempts != 1 || successes != 1 {
		t.Fatalf("slow destination attempts/successes = %d/%d, want 1/1", attempts, successes)
	}

	// A transient provider failure is retried in its own destination lane.
	dc.mu.Lock()
	dc.failures = 1
	dc.failure = transport.NewFailure(transport.FailureTransient, 0, errors.New("private provider detail"))
	dc.mu.Unlock()
	if err := mesh.Handle(ctx, transport.Incoming{
		Endpoint: "wa", RemoteID: "wa-message-2", Kind: "text", Text: "second body",
		Sender: transport.Sender{OpaqueID: "u_abcdefghij"}, Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	dc.waitFor(t, 2)

	// Lifecycle operations are serialized behind the create on every copy.
	edit := transport.Incoming{Endpoint: "wa", RemoteID: "edit-1", Kind: "edit", Text: "edited body", Sender: transport.Sender{OpaqueID: "u_abcdefghij"}, ReplyTo: &transport.MessageRef{Endpoint: "wa", RemoteMessageID: "wa-message-1"}, Timestamp: time.Now().UTC()}
	if err := mesh.Handle(ctx, edit); err != nil {
		t.Fatal(err)
	}
	reaction := transport.Incoming{Endpoint: "wa", RemoteID: "reaction-1", Kind: "reaction", Text: "👍", Sender: transport.Sender{OpaqueID: "u_bcdefghijk"}, ReplyTo: &transport.MessageRef{Endpoint: "wa", RemoteMessageID: "wa-message-1"}, Timestamp: time.Now().UTC()}
	if err := mesh.Handle(ctx, reaction); err != nil {
		t.Fatal(err)
	}
	deleteEvent := transport.Incoming{Endpoint: "wa", RemoteID: "delete-1", Kind: "delete", Sender: transport.Sender{OpaqueID: "u_abcdefghij"}, ReplyTo: &transport.MessageRef{Endpoint: "wa", RemoteMessageID: "wa-message-1"}, Timestamp: time.Now().UTC()}
	if err := mesh.Handle(ctx, deleteEvent); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, _, sequence := tg.snapshot()
		if len(sequence) >= 5 && sequence[len(sequence)-1] == "delete" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	_, _, sequence := tg.snapshot()
	wantSequence := []string{"create", "edit", "reaction", "delete"}
	if len(sequence) < 4 || !equalStrings(sequence[len(sequence)-4:], wantSequence) {
		t.Fatalf("Telegram lifecycle sequence = %v, want suffix %v", sequence, wantSequence)
	}
}

func TestThreeTransportHarnessDoesNotBlindlyRetryAmbiguousCreate(t *testing.T) {
	ctx := context.Background()
	syncStore, err := store.Open(ctx, t.TempDir()+"/sync.db")
	if err != nil {
		t.Fatal(err)
	}
	defer syncStore.Close()
	wa := &fakeAdapter{name: "wa"}
	dc := &fakeAdapter{name: "dc", failures: 1, failure: transport.NewFailureWithCertainty(transport.FailureTransient, 0, transport.SendUnknown, errors.New("ambiguous provider result"))}
	tg := &fakeAdapter{name: "tg"}
	registry, err := router.NewAdapterRegistry(reliabilityConfig(), map[string]router.OutboundAdapter{
		"conn-wa-1": wa, "conn-dc-1": dc, "conn-tg-1": tg,
	})
	if err != nil {
		t.Fatal(err)
	}
	mesh, err := router.New(reliabilityConfig(), syncStore, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer mesh.Close()
	event := transport.Incoming{Endpoint: "wa", RemoteID: "ambiguous-message", Kind: "text", Text: "ambiguous canary", Sender: transport.Sender{OpaqueID: "u_abcdefghij"}, Timestamp: time.Now().UTC()}
	if err := mesh.Handle(ctx, event); err != nil {
		t.Fatal(err)
	}
	tg.waitFor(t, 1)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		attempts, _, _ := dc.snapshot()
		if attempts == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if attempts, _, _ := dc.snapshot(); attempts != 1 {
		t.Fatalf("ambiguous attempts=%d, want one", attempts)
	}
	if err := mesh.Handle(ctx, event); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if attempts, _, _ := dc.snapshot(); attempts != 1 {
		t.Fatalf("duplicate ambiguous retry attempts=%d, want one", attempts)
	}
	var canaries int
	if err := syncStore.DB().QueryRow(`SELECT COUNT(*) FROM canonical_messages`).Scan(&canaries); err != nil {
		t.Fatal(err)
	}
	if canaries != 1 {
		t.Fatalf("canonical messages=%d, want one", canaries)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
