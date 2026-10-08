package integration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/recovery"
	"github.com/vm75/message-sync/internal/router"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/transport"
)

type fakeEditCall struct {
	ref  transport.MessageRef
	text string
}

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
	edits       []fakeEditCall
	deletes     []transport.MessageRef
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

func (f *fakeAdapter) Edit(_ context.Context, ref transport.MessageRef, text string) error {
	f.mu.Lock()
	f.sequence = append(f.sequence, "edit")
	f.edits = append(f.edits, fakeEditCall{ref: ref, text: text})
	f.mu.Unlock()
	return nil
}

func (f *fakeAdapter) Delete(_ context.Context, ref transport.MessageRef) error {
	f.mu.Lock()
	f.sequence = append(f.sequence, "delete")
	f.deletes = append(f.deletes, ref)
	f.mu.Unlock()
	return nil
}

func (f *fakeAdapter) editCalls() []fakeEditCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeEditCall(nil), f.edits...)
}

func (f *fakeAdapter) deleteCalls() []transport.MessageRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]transport.MessageRef(nil), f.deletes...)
}

func (f *fakeAdapter) waitForEdits(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		count := len(f.edits)
		f.mu.Unlock()
		if count >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t.Fatalf("%s edits = %d, want %d", f.name, len(f.edits), want)
}

func (f *fakeAdapter) waitForDeletes(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		count := len(f.deletes)
		f.mu.Unlock()
		if count >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t.Fatalf("%s deletes = %d, want %d", f.name, len(f.deletes), want)
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

func TestWhatsAppOriginatedEditPropagation(t *testing.T) {
	ctx := context.Background()
	syncStore, err := store.Open(ctx, t.TempDir()+"/sync.db")
	if err != nil {
		t.Fatal(err)
	}
	defer syncStore.Close()

	waA := &fakeAdapter{name: "wa-a"}
	dcB := &fakeAdapter{name: "dc-b"}
	waC := &fakeAdapter{name: "wa-c"}

	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"wa-a": {Transport: config.TransportWhatsApp, ConnectionID: "conn-wa-1", RemoteID: "111@g.us"},
			"dc-b": {Transport: config.TransportDiscord, ConnectionID: "conn-dc-1", RemoteID: "222"},
			"wa-c": {Transport: config.TransportWhatsApp, ConnectionID: "conn-wa-2", RemoteID: "333@g.us"},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Endpoints: []string{"wa-a", "dc-b", "wa-c"}}},
		Identity: config.Identity{UsernameMode: config.UsernameModePushName},
	}

	registry, err := router.NewAdapterRegistry(cfg, map[string]router.OutboundAdapter{
		"conn-wa-1": waA,
		"conn-dc-1": dcB,
		"conn-wa-2": waC,
	})
	if err != nil {
		t.Fatal(err)
	}
	mesh, err := router.New(cfg, syncStore, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer mesh.Close()

	coordinator, err := recovery.NewCoordinator(syncStore, mesh)
	if err != nil {
		t.Fatal(err)
	}

	origTimestamp := time.Unix(1_700_000_100, 0).UTC()
	origMsg := transport.Incoming{
		Endpoint:  "wa-a",
		RemoteID:  "wa-orig-100",
		Kind:      "text",
		Text:      "Initial message in WhatsApp Group A",
		Sender:    transport.Sender{DisplayName: "Alice", OpaqueID: "u_alice_123"},
		Timestamp: origTimestamp,
		Checkpoint: transport.Checkpoint{
			StreamKey:      "wa-a",
			Position:       origTimestamp.UnixMilli(),
			EventTimestamp: origTimestamp,
			Valid:          true,
		},
	}

	// 1 & 2. Send in WhatsApp group A; verify synchronization to Discord B and WhatsApp C.
	outcome, err := coordinator.Handle(ctx, origMsg)
	if err != nil {
		t.Fatalf("handle original create: %v", err)
	}
	if !outcome.Accepted {
		t.Fatalf("original message not accepted: %+v", outcome)
	}
	dcB.waitFor(t, 1)
	waC.waitFor(t, 1)

	_, dcBSuccesses, dcBSeq := dcB.snapshot()
	_, waCSuccesses, waCSeq := waC.snapshot()
	if dcBSuccesses != 1 || waCSuccesses != 1 {
		t.Fatalf("expected 1 create each, got dc-b: %d, wa-c: %d", dcBSuccesses, waCSuccesses)
	}
	if len(dcBSeq) != 1 || dcBSeq[0] != "create" || len(waCSeq) != 1 || waCSeq[0] != "create" {
		t.Fatalf("unexpected sequences: dcB=%v, waC=%v", dcBSeq, waCSeq)
	}

	// 3. Edit original message in WhatsApp group A.
	// Notification ID != original message ID; timestamp equal to original timestamp.
	editMsg := transport.Incoming{
		Endpoint:  "wa-a",
		RemoteID:  "wa-edit-notif-200",
		Kind:      "edit",
		Text:      "Edited message in WhatsApp Group A",
		Sender:    transport.Sender{DisplayName: "Alice", OpaqueID: "u_alice_123"},
		ReplyTo:   &transport.MessageRef{Endpoint: "wa-a", RemoteMessageID: "wa-orig-100"},
		Timestamp: origTimestamp,
	}

	editOutcome, err := coordinator.Handle(ctx, editMsg)
	if err != nil {
		t.Fatalf("handle edit: %v", err)
	}
	if editOutcome.NoOp {
		t.Fatalf("edit was incorrectly treated as no-op: %+v", editOutcome)
	}

	// 4. Confirm synchronized messages in B and C are updated in place.
	dcB.waitForEdits(t, 1)
	waC.waitForEdits(t, 1)

	dcBEdits := dcB.editCalls()
	waCEdits := waC.editCalls()
	if len(dcBEdits) != 1 || len(waCEdits) != 1 {
		t.Fatalf("expected 1 edit each, got dc-b: %d, wa-c: %d", len(dcBEdits), len(waCEdits))
	}

	if dcBEdits[0].ref.RemoteMessageID != "dc-b-copy" {
		t.Errorf("dc-b edit target = %q, want %q", dcBEdits[0].ref.RemoteMessageID, "dc-b-copy")
	}
	if !strings.Contains(dcBEdits[0].text, "Edited message in WhatsApp Group A") {
		t.Errorf("dc-b edit text missing content: %q", dcBEdits[0].text)
	}

	if waCEdits[0].ref.RemoteMessageID != "wa-c-copy" {
		t.Errorf("wa-c edit target = %q, want %q", waCEdits[0].ref.RemoteMessageID, "wa-c-copy")
	}
	if !strings.Contains(waCEdits[0].text, "Edited message in WhatsApp Group A") {
		t.Errorf("wa-c edit text missing content: %q", waCEdits[0].text)
	}

	// 5. Confirm NO duplicate messages are created.
	dcBAttempts, dcBSuccesses, _ := dcB.snapshot()
	waCAttempts, waCSuccesses, _ := waC.snapshot()
	if dcBSuccesses != 1 || waCSuccesses != 1 {
		t.Fatalf("creates changed after edit! dc-b: %d, wa-c: %d (expected 1 each)", dcBSuccesses, waCSuccesses)
	}
	if dcBAttempts != 1 || waCAttempts != 1 {
		t.Fatalf("create attempts changed after edit! dc-b: %d, wa-c: %d (expected 1 each)", dcBAttempts, waCAttempts)
	}

	// 6. Duplicate / replayed edit event.
	if _, err := coordinator.Handle(ctx, editMsg); err != nil {
		t.Fatalf("duplicate edit error: %v", err)
	}
	dcB.waitForEdits(t, 2)
	waC.waitForEdits(t, 2)
	_, dcBSuccesses, _ = dcB.snapshot()
	_, waCSuccesses, _ = waC.snapshot()
	if dcBSuccesses != 1 || waCSuccesses != 1 {
		t.Fatalf("creates increased on duplicate edit! dc-b: %d, wa-c: %d", dcBSuccesses, waCSuccesses)
	}

	// 7. Self-originated WhatsApp edit.
	selfEditMsg := transport.Incoming{
		Endpoint:  "wa-a",
		RemoteID:  "wa-self-edit-300",
		Kind:      "edit",
		FromSelf:  true,
		Text:      "Self-edited message in WhatsApp Group A",
		Sender:    transport.Sender{DisplayName: "Alice", OpaqueID: "u_alice_123"},
		ReplyTo:   &transport.MessageRef{Endpoint: "wa-a", RemoteMessageID: "wa-orig-100"},
		Timestamp: origTimestamp,
	}
	if _, err := coordinator.Handle(ctx, selfEditMsg); err != nil {
		t.Fatalf("self edit error: %v", err)
	}
	dcB.waitForEdits(t, 3)
	waC.waitForEdits(t, 3)
	if !strings.Contains(dcB.editCalls()[2].text, "Self-edited message") {
		t.Errorf("self edit text missing in dc-b: %q", dcB.editCalls()[2].text)
	}
	if !strings.Contains(waC.editCalls()[2].text, "Self-edited message") {
		t.Errorf("self edit text missing in wa-c: %q", waC.editCalls()[2].text)
	}

	// 8. Discord -> WhatsApp edit propagation still works.
	dcEditMsg := transport.Incoming{
		Endpoint:  "dc-b",
		RemoteID:  "dc-edit-400",
		Kind:      "edit",
		Text:      "Edit originated from Discord",
		Sender:    transport.Sender{DisplayName: "Bob", OpaqueID: "u_bob_456"},
		ReplyTo:   &transport.MessageRef{Endpoint: "dc-b", RemoteMessageID: "dc-b-copy"},
		Timestamp: time.Now().UTC(),
	}
	if _, err := coordinator.Handle(ctx, dcEditMsg); err != nil {
		t.Fatalf("dc edit error: %v", err)
	}
	waA.waitForEdits(t, 1)
	waC.waitForEdits(t, 4)
	if waA.editCalls()[0].ref.RemoteMessageID != "wa-orig-100" {
		t.Errorf("wa-a edit target = %q, want %q", waA.editCalls()[0].ref.RemoteMessageID, "wa-orig-100")
	}
	if !strings.Contains(waA.editCalls()[0].text, "Edit originated from Discord") {
		t.Errorf("wa-a edit text missing content: %q", waA.editCalls()[0].text)
	}

	// 9. Message deletion propagation still works.
	delMsg := transport.Incoming{
		Endpoint:  "wa-a",
		RemoteID:  "wa-del-500",
		Kind:      "delete",
		ReplyTo:   &transport.MessageRef{Endpoint: "wa-a", RemoteMessageID: "wa-orig-100"},
		Timestamp: time.Now().UTC(),
	}
	if _, err := coordinator.Handle(ctx, delMsg); err != nil {
		t.Fatalf("delete error: %v", err)
	}
	dcB.waitForDeletes(t, 1)
	waC.waitForDeletes(t, 1)
	if dcB.deleteCalls()[0].RemoteMessageID != "dc-b-copy" {
		t.Errorf("dc-b delete target = %q, want %q", dcB.deleteCalls()[0].RemoteMessageID, "dc-b-copy")
	}
	if waC.deleteCalls()[0].RemoteMessageID != "wa-c-copy" {
		t.Errorf("wa-c delete target = %q, want %q", waC.deleteCalls()[0].RemoteMessageID, "wa-c-copy")
	}
}
