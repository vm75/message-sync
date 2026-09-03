package integration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/api"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/connection"
	"github.com/vm75/message-sync/internal/controlstore"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/recovery"
	"github.com/vm75/message-sync/internal/router"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/transport"
)

// recordingAdapter tracks sent messages, reactions, edits, and deletes with support for failures.
type recordingAdapter struct {
	mu           sync.Mutex
	name         string
	connectionID string
	events       chan transport.Incoming
	sent         []transport.Outgoing
	reactions    []transport.Reaction
	failures     int
	failureErr   error
	delay        <-chan struct{}
}

func newRecordingAdapter(name, connectionID string) *recordingAdapter {
	return &recordingAdapter{
		name:         name,
		connectionID: connectionID,
		events:       make(chan transport.Incoming, 50),
	}
}

func (a *recordingAdapter) Events() <-chan transport.Incoming { return a.events }

func (a *recordingAdapter) Send(ctx context.Context, outgoing transport.Outgoing) (transport.MessageRef, error) {
	if a.delay != nil {
		select {
		case <-a.delay:
		case <-ctx.Done():
			return transport.MessageRef{}, ctx.Err()
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failures > 0 {
		a.failures--
		err := a.failureErr
		if err == nil {
			err = errors.New("simulated transient failure")
		}
		return transport.MessageRef{}, err
	}
	a.sent = append(a.sent, outgoing)
	return transport.MessageRef{
		Endpoint:        outgoing.Endpoint,
		RemoteMessageID: fmt.Sprintf("%s-msg-%d", a.name, len(a.sent)),
		ChildScope:      outgoing.ChildScope,
		IsTargetFromMe:  true,
	}, nil
}

func (a *recordingAdapter) React(ctx context.Context, r transport.Reaction) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reactions = append(a.reactions, r)
	return nil
}

func (a *recordingAdapter) Edit(ctx context.Context, ref transport.MessageRef, text string) error {
	return nil
}

func (a *recordingAdapter) Delete(ctx context.Context, ref transport.MessageRef) error {
	return nil
}

func (a *recordingAdapter) Close() error {
	return nil
}

func (a *recordingAdapter) sentCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.sent)
}

func (a *recordingAdapter) getSent() []transport.Outgoing {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]transport.Outgoing(nil), a.sent...)
}

func (a *recordingAdapter) waitForSent(t *testing.T, count int) []transport.Outgoing {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if a.sentCount() >= count {
			return a.getSent()
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s timeout waiting for %d sent messages, got %d", a.name, count, a.sentCount())
	return nil
}

// ── 1. Multi-Connection Mixed Topology & Delivery Isolation ───────────────

func TestHardening_MultiConnectionMixedTopology(t *testing.T) {
	ctx := context.Background()
	syncStore, err := store.Open(ctx, t.TempDir()+"/sync.db")
	if err != nil {
		t.Fatal(err)
	}
	defer syncStore.Close()

	// 6 endpoints on 6 connections (2 WhatsApp, 2 Discord, 2 Telegram)
	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"wa-family":    {Transport: config.TransportWhatsApp, ConnectionID: "w1", RemoteID: "wa-fam@g.us"},
			"wa-work":      {Transport: config.TransportWhatsApp, ConnectionID: "w2", RemoteID: "wa-work@g.us"},
			"dc-community": {Transport: config.TransportDiscord, ConnectionID: "d1", RemoteID: "dc-comm-chan"},
			"dc-work":      {Transport: config.TransportDiscord, ConnectionID: "d2", RemoteID: "dc-work-chan"},
			"tg-family":    {Transport: config.TransportTelegram, ConnectionID: "t1", RemoteID: "-100111"},
			"tg-work":      {Transport: config.TransportTelegram, ConnectionID: "t2", RemoteID: "-100222"},
		},
		SyncSets: []config.SyncSet{
			{ID: "ss-family", Endpoints: []string{"wa-family", "dc-community", "tg-family"}},
			{ID: "ss-work", Endpoints: []string{"wa-work", "dc-work", "tg-work"}},
		},
		Identity: config.Identity{UsernameMode: config.UsernameModeHash},
	}

	adapters := map[string]*recordingAdapter{
		"w1": newRecordingAdapter("wa-fam", "w1"),
		"w2": newRecordingAdapter("wa-work", "w2"),
		"d1": newRecordingAdapter("dc-comm", "d1"),
		"d2": newRecordingAdapter("dc-work", "d2"),
		"t1": newRecordingAdapter("tg-fam", "t1"),
		"t2": newRecordingAdapter("tg-work", "t2"),
	}

	outbound := make(map[string]router.OutboundAdapter, len(adapters))
	for k, v := range adapters {
		outbound[k] = v
	}

	registry, err := router.NewAdapterRegistry(cfg, outbound)
	if err != nil {
		t.Fatal(err)
	}

	mesh, err := router.New(cfg, syncStore, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer mesh.Close()

	// 1. Send message on wa-family (part of ss-family)
	if err := mesh.Handle(ctx, transport.Incoming{
		Endpoint:  "wa-family",
		RemoteID:  "fam-msg-1",
		Kind:      "text",
		Text:      "family announcement",
		Sender:    transport.Sender{OpaqueID: "u_family_sender"},
		Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	// wa-family should fan out to dc-community (d1) and tg-family (t1).
	adapters["d1"].waitForSent(t, 1)
	adapters["t1"].waitForSent(t, 1)

	// Verify work destinations received nothing
	time.Sleep(50 * time.Millisecond)
	if count := adapters["w2"].sentCount(); count != 0 {
		t.Fatalf("wa-work received %d messages, expected 0", count)
	}
	if count := adapters["d2"].sentCount(); count != 0 {
		t.Fatalf("dc-work received %d messages, expected 0", count)
	}
	if count := adapters["t2"].sentCount(); count != 0 {
		t.Fatalf("tg-work received %d messages, expected 0", count)
	}

	// 2. Send message on dc-work (part of ss-work)
	if err := mesh.Handle(ctx, transport.Incoming{
		Endpoint:  "dc-work",
		RemoteID:  "work-msg-1",
		Kind:      "text",
		Text:      "work announcement",
		Sender:    transport.Sender{OpaqueID: "u_work_sender"},
		Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	// Should fan out to wa-work (w2) and tg-work (t2)
	adapters["w2"].waitForSent(t, 1)
	adapters["t2"].waitForSent(t, 1)

	// Family endpoints should receive nothing new!
	time.Sleep(50 * time.Millisecond)
	if count := adapters["w1"].sentCount(); count != 0 {
		t.Fatalf("wa-family received %d messages, expected 0", count)
	}
	if count := adapters["d1"].sentCount(); count != 1 {
		t.Fatalf("dc-community received %d messages, expected 1", count)
	}
	if count := adapters["t1"].sentCount(); count != 1 {
		t.Fatalf("tg-family received %d messages, expected 1", count)
	}
}

// ── 2. Discord Thread & Telegram Topic Reply Lineage ──────────────────────

func TestHardening_CrossConnectionThreadAndTopicLineage(t *testing.T) {
	ctx := context.Background()
	syncStore, err := store.Open(ctx, t.TempDir()+"/sync.db")
	if err != nil {
		t.Fatal(err)
	}
	defer syncStore.Close()

	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"dc-comm": {Transport: config.TransportDiscord, ConnectionID: "d1", RemoteID: "chan-1"},
			"tg-fam":  {Transport: config.TransportTelegram, ConnectionID: "t1", RemoteID: "-1001"},
			"dc-work": {Transport: config.TransportDiscord, ConnectionID: "d2", RemoteID: "chan-2"},
			"tg-work": {Transport: config.TransportTelegram, ConnectionID: "t2", RemoteID: "-1002"},
		},
		SyncSets: []config.SyncSet{
			{ID: "ss-1", Endpoints: []string{"dc-comm", "tg-fam"}},
			{ID: "ss-2", Endpoints: []string{"dc-work", "tg-work"}},
		},
		Identity: config.Identity{UsernameMode: config.UsernameModeHash},
	}

	adapters := map[string]*recordingAdapter{
		"d1": newRecordingAdapter("dc-comm", "d1"),
		"t1": newRecordingAdapter("tg-fam", "t1"),
		"d2": newRecordingAdapter("dc-work", "d2"),
		"t2": newRecordingAdapter("tg-work", "t2"),
	}

	outbound := make(map[string]router.OutboundAdapter, len(adapters))
	for k, v := range adapters {
		outbound[k] = v
	}

	registry, err := router.NewAdapterRegistry(cfg, outbound)
	if err != nil {
		t.Fatal(err)
	}

	mesh, err := router.New(cfg, syncStore, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer mesh.Close()

	// Scenario A: Discord bot A (d1) / endpoint dc-comm / thread "th-99" -> Telegram bot B (t1) / tg-fam
	if err := mesh.Handle(ctx, transport.Incoming{
		Endpoint: "dc-comm",
		RemoteID: "dc-th-msg-1",
		Kind:     "text",
		Text:     "thread message",
		ChildScope: &transport.ChildScope{
			Kind:     transport.ScopeKindDiscordThread,
			RemoteID: "th-99",
		},
		Sender:    transport.Sender{OpaqueID: "u_dc_user"},
		Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	// tg-fam receives forwarded message
	tgSent := adapters["t1"].waitForSent(t, 1)
	_ = tgSent

	// Reply from Telegram tg-fam back to dc-comm
	// Quotes the forwarded message
	if err := mesh.Handle(ctx, transport.Incoming{
		Endpoint:  "tg-fam",
		RemoteID:  "tg-reply-1",
		Kind:      "text",
		Text:      "reply to thread",
		ReplyTo:   &transport.MessageRef{Endpoint: "tg-fam", RemoteMessageID: "tg-fam-msg-1"},
		Sender:    transport.Sender{OpaqueID: "u_tg_user"},
		Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	// Reply must deliver back to dc-comm on connection d1, explicitly preserving ChildScope RemoteID "th-99"!
	dcSent := adapters["d1"].waitForSent(t, 1)
	if dcSent[0].ChildScope == nil || dcSent[0].ChildScope.RemoteID != "th-99" {
		t.Fatalf("expected reply ChildScope 'th-99', got %+v", dcSent[0].ChildScope)
	}

	// Scenario B: Telegram bot A (t2) / endpoint tg-work / topic "topic-42" -> Discord bot B (d2) / dc-work
	if err := mesh.Handle(ctx, transport.Incoming{
		Endpoint: "tg-work",
		RemoteID: "tg-topic-msg-1",
		Kind:     "text",
		Text:     "topic message",
		ChildScope: &transport.ChildScope{
			Kind:     transport.ScopeKindTelegramTopic,
			RemoteID: "topic-42",
		},
		Sender:    transport.Sender{OpaqueID: "u_tg_worker"},
		Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	dcWorkSent := adapters["d2"].waitForSent(t, 1)
	_ = dcWorkSent

	// Reply from Discord dc-work back to tg-work
	if err := mesh.Handle(ctx, transport.Incoming{
		Endpoint:  "dc-work",
		RemoteID:  "dc-reply-1",
		Kind:      "text",
		Text:      "reply to topic",
		ReplyTo:   &transport.MessageRef{Endpoint: "dc-work", RemoteMessageID: "dc-work-msg-1"},
		Sender:    transport.Sender{OpaqueID: "u_dc_worker"},
		Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	// Reply must deliver to tg-work on connection t2 with ChildScope RemoteID "topic-42"
	tgWorkSent := adapters["t2"].waitForSent(t, 1)
	if tgWorkSent[0].ChildScope == nil || tgWorkSent[0].ChildScope.RemoteID != "topic-42" {
		t.Fatalf("expected reply ChildScope 'topic-42', got %+v", tgWorkSent[0].ChildScope)
	}

	// Verify canonical_scopes schema invariant: keyed only by canonical_id + endpoint_alias
	var scopeCount int
	if err := syncStore.DB().QueryRow(`SELECT COUNT(*) FROM canonical_scopes`).Scan(&scopeCount); err != nil {
		t.Fatal(err)
	}
	if scopeCount < 2 {
		t.Fatalf("expected at least 2 canonical scopes recorded, got %d", scopeCount)
	}
}

// ── 3. Delivery Lanes Failure Isolation & Endpoint Reassignment ───────────

func TestHardening_DeliveryLanesFailureIsolationAndEndpointReassignment(t *testing.T) {
	ctx := context.Background()
	syncStore, err := store.Open(ctx, t.TempDir()+"/sync.db")
	if err != nil {
		t.Fatal(err)
	}
	defer syncStore.Close()

	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"dc-comm": {Transport: config.TransportDiscord, ConnectionID: "d1", RemoteID: "chan-1"},
			"tg-fam":  {Transport: config.TransportTelegram, ConnectionID: "t1", RemoteID: "-1001"},
		},
		SyncSets: []config.SyncSet{
			{ID: "ss-main", Endpoints: []string{"dc-comm", "tg-fam"}},
		},
		Identity: config.Identity{UsernameMode: config.UsernameModeHash},
	}

	// d1 has simulated transient failure
	d1 := newRecordingAdapter("d1", "d1")
	d1.failures = 100 // persistently failing
	d1.failureErr = transport.NewFailure(transport.FailureTransient, 0, errors.New("d1 revoked or offline"))

	t1 := newRecordingAdapter("t1", "t1")
	d2 := newRecordingAdapter("d2", "d2") // healthy alternative connection

	registry, err := router.NewAdapterRegistry(cfg, map[string]router.OutboundAdapter{
		"d1": d1,
		"t1": t1,
		"d2": d2,
	})
	if err != nil {
		t.Fatal(err)
	}

	mesh, err := router.New(cfg, syncStore, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer mesh.Close()

	// Inbound message from tg-fam into ss-main
	if err := mesh.Handle(ctx, transport.Incoming{
		Endpoint:  "tg-fam",
		RemoteID:  "msg-fail-test",
		Kind:      "text",
		Text:      "isolated failure test",
		Sender:    transport.Sender{OpaqueID: "u_tester"},
		Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	// Delivery to dc-comm on d1 fails.
	// Check that copy has NOT been recorded for dc-comm yet.
	time.Sleep(50 * time.Millisecond)
	var copyCount int
	err = syncStore.DB().QueryRow(`SELECT COUNT(*) FROM message_copies WHERE endpoint_id='dc-comm'`).Scan(&copyCount)
	if err != nil {
		t.Fatalf("query message_copies error: %v", err)
	}
	if copyCount != 0 {
		t.Fatalf("expected 0 copies in ledger for failing dc-comm, got %d", copyCount)
	}

	// Now reassign endpoint dc-comm from failing connection d1 to healthy connection d2!
	cfg.Endpoints["dc-comm"] = config.Endpoint{
		Transport:    config.TransportDiscord,
		ConnectionID: "d2",
		RemoteID:     "chan-1",
	}
	if err := registry.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := mesh.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}

	// When new message arrives, registry dispatches dc-comm to d2 and succeeds!
	if err := mesh.Handle(ctx, transport.Incoming{
		Endpoint:  "tg-fam",
		RemoteID:  "msg-after-reassign",
		Kind:      "text",
		Text:      "after reassign",
		Sender:    transport.Sender{OpaqueID: "u_tester"},
		Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	d2Sent := d2.waitForSent(t, 2)
	if len(d2Sent) != 2 {
		t.Fatalf("expected d2 to receive 2 messages (retried job + new message) after reassignment, got %d", len(d2Sent))
	}

	// Now copies for dc-comm are recorded in message_copies!
	err = syncStore.DB().QueryRow(`SELECT COUNT(*) FROM message_copies WHERE endpoint_id='dc-comm'`).Scan(&copyCount)
	if err != nil || copyCount != 2 {
		t.Fatalf("expected 2 copies in ledger for dc-comm after reassignment, got %d (err: %v)", copyCount, err)
	}
}

// ── 4. Local-Only Messages Across Connections & Child Scopes ──────────────

func TestHardening_LocalOnlyMessageFiltering(t *testing.T) {
	ctx := context.Background()
	syncStore, err := store.Open(ctx, t.TempDir()+"/sync.db")
	if err != nil {
		t.Fatal(err)
	}
	defer syncStore.Close()

	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"dc-comm": {Transport: config.TransportDiscord, ConnectionID: "d1", RemoteID: "chan-1"},
			"tg-fam":  {Transport: config.TransportTelegram, ConnectionID: "t1", RemoteID: "-1001"},
		},
		SyncSets: []config.SyncSet{
			{ID: "ss-main", Endpoints: []string{"dc-comm", "tg-fam"}},
		},
		Identity: config.Identity{
			UsernameMode: config.UsernameModeHash,
		},
		LocalPrefix: "#local",
	}

	d1 := newRecordingAdapter("d1", "d1")
	t1 := newRecordingAdapter("t1", "t1")

	registry, err := router.NewAdapterRegistry(cfg, map[string]router.OutboundAdapter{"d1": d1, "t1": t1})
	if err != nil {
		t.Fatal(err)
	}
	mesh, err := router.New(cfg, syncStore, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer mesh.Close()

	// Local message from dc-comm inside a thread
	if err := mesh.Handle(ctx, transport.Incoming{
		Endpoint: "dc-comm",
		RemoteID: "local-msg-1",
		Kind:     "text",
		Text:     "#local this is a private local note",
		ChildScope: &transport.ChildScope{
			Kind:     transport.ScopeKindDiscordThread,
			RemoteID: "thread-abc",
		},
		Sender:    transport.Sender{OpaqueID: "u_local_author"},
		Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)
	if count := t1.sentCount(); count != 0 {
		t.Fatalf("tg-fam received %d messages, expected 0 (suppressed by local prefix)", count)
	}

	// Normal non-local message
	if err := mesh.Handle(ctx, transport.Incoming{
		Endpoint: "dc-comm",
		RemoteID: "public-msg-1",
		Kind:     "text",
		Text:     "public message for everyone",
		ChildScope: &transport.ChildScope{
			Kind:     transport.ScopeKindDiscordThread,
			RemoteID: "thread-abc",
		},
		Sender:    transport.Sender{OpaqueID: "u_local_author"},
		Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	t1.waitForSent(t, 1)
}

// ── 5. Telegram Recovery Cursors Namespacing & Isolation ───────────────────

func TestHardening_TelegramRecoveryCursorsNamespacing(t *testing.T) {
	ctx := context.Background()
	syncStore, err := store.Open(ctx, t.TempDir()+"/sync.db")
	if err != nil {
		t.Fatal(err)
	}
	defer syncStore.Close()

	now := time.Now().UTC()
	// Persist checkpoint cursors for two distinct Telegram connections
	if err := syncStore.PutRecoveryCursor(ctx, store.RecoveryCursor{
		StreamKey: "telegram:conn-tg-1",
		Position:  10050,
		UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := syncStore.PutRecoveryCursor(ctx, store.RecoveryCursor{
		StreamKey: "telegram:conn-tg-2",
		Position:  20080,
		UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	cursor1, err := syncStore.RecoveryCursor(ctx, "telegram:conn-tg-1")
	if err != nil || cursor1.Position != 10050 {
		t.Fatalf("conn-tg-1 cursor = %+v, err = %v, want 10050", cursor1, err)
	}

	cursor2, err := syncStore.RecoveryCursor(ctx, "telegram:conn-tg-2")
	if err != nil || cursor2.Position != 20080 {
		t.Fatalf("conn-tg-2 cursor = %+v, err = %v, want 20080", cursor2, err)
	}

	// Overlapping update IDs between connections: both can save cursor 50000 independently
	if err := syncStore.PutRecoveryCursor(ctx, store.RecoveryCursor{
		StreamKey: "telegram:conn-tg-1",
		Position:  50000,
		UpdatedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	cursor2After, err := syncStore.RecoveryCursor(ctx, "telegram:conn-tg-2")
	if err != nil || cursor2After.Position != 20080 {
		t.Fatalf("conn-tg-2 cursor corrupted by conn-tg-1 update: got %d, want 20080", cursor2After.Position)
	}
}

// ── 6. Membership Verification Endpoint to Connection Resolution ───────────

type fakeDiscordAdmin struct {
	assignedRoleID string
	assignedUser   string
}

func (f *fakeDiscordAdmin) AssignRole(ctx context.Context, endpoint, roleID, userID string) error {
	f.assignedRoleID = roleID
	f.assignedUser = userID
	return nil
}

func TestHardening_MembershipVerificationEndpointResolution(t *testing.T) {
	ctx := context.Background()
	syncStore, err := store.Open(ctx, filepathJoin(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer syncStore.Close()

	ctrlStore, err := controlstore.Open(ctx, filepathJoin(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ctrlStore.Close()

	// Setup endpoints in syncDB
	_, err = syncStore.DB().Exec(`
		INSERT INTO endpoints (alias, transport, connection_id, remote_id)
		VALUES ('dc-comm', 'discord', 'conn-dc-1', 'chan-100');
	`)
	if err != nil {
		t.Fatal(err)
	}

	// Setup pipeline and request in controlDB
	_, err = ctrlStore.DB().Exec(`
		INSERT INTO users (id, username, password_hash, role, active, created_at, updated_at)
		VALUES ('usr-1', 'admin', 'hash', 'admin', 1, 1, 1);
		INSERT INTO verification_pipelines (id, public_token, label, target_transport, endpoint_alias, discord_role_id, enabled, creator_user_id, created_at, updated_at)
		VALUES ('pipe-dc', 'tok-abc', 'Discord Pipeline', 'discord', 'dc-comm', 'role-123', 1, 'usr-1', 1, 1);
		INSERT INTO membership_requests (id, pipeline_id, status, applicant_work_email, applicant_whatsapp_phone, applicant_discord_user_id, linkedin_url, evidence_reference, verification_state, fulfillment_state, fulfillment_failure_class, created_at, updated_at)
		VALUES ('req-1', 'pipe-dc', 'approved', 'user@example.com', '', 'disc-user-777', '', '', 'verified', 'not_started', '', 1, 1);
	`)
	if err != nil {
		t.Fatal(err)
	}

	// Create fake adapters
	dc1 := &fakeDiscordAdmin{}
	dc2 := &fakeDiscordAdmin{}

	connMap := map[string]any{
		"conn-dc-1": dc1,
		"conn-dc-2": dc2,
	}

	mockConns := &mockTestConnectionService{
		adapters: connMap,
	}

	srv := api.NewServer(api.Options{
		DB:          syncStore.DB(),
		ControlDB:   ctrlStore.DB(),
		Connections: mockConns,
		Secret:      []byte("01234567890123456789012345678901"),
	})

	adminToken, err := srv.CreateToken()
	if err != nil {
		t.Fatal(err)
	}

	// 1. Fulfill req-1: should resolve endpoint 'dc-comm' -> connection 'conn-dc-1' -> dc1 adapter
	fulfillReq := httptest.NewRequest("POST", "/api/verification/requests/req-1/fulfill", nil)
	fulfillReq.SetPathValue("id", "req-1")
	fulfillReq.Header.Set("Authorization", "Bearer "+adminToken)
	fulfillRec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(fulfillRec, fulfillReq)
	if fulfillRec.Code != http.StatusOK {
		t.Fatalf("fulfill status = %d, body = %s", fulfillRec.Code, fulfillRec.Body.String())
	}
	if dc1.assignedUser != "disc-user-777" || dc1.assignedRoleID != "role-123" {
		t.Fatalf("dc1 not invoked correctly: user=%q, role=%q", dc1.assignedUser, dc1.assignedRoleID)
	}
	if dc2.assignedUser != "" {
		t.Fatalf("dc2 should not have been invoked before reassignment")
	}

	// 2. Reassign endpoint 'dc-comm' to 'conn-dc-2' in syncDB
	_, err = syncStore.DB().Exec(`UPDATE endpoints SET connection_id='conn-dc-2' WHERE alias='dc-comm'`)
	if err != nil {
		t.Fatal(err)
	}

	// Reset request fulfillment state to retry
	_, err = ctrlStore.DB().Exec(`UPDATE membership_requests SET fulfillment_state='not_started' WHERE id='req-1'`)
	if err != nil {
		t.Fatal(err)
	}

	// 3. Fulfill req-1 again: should now resolve to dc2!
	fulfillReq2 := httptest.NewRequest("POST", "/api/verification/requests/req-1/fulfill", nil)
	fulfillReq2.SetPathValue("id", "req-1")
	fulfillReq2.Header.Set("Authorization", "Bearer "+adminToken)
	fulfillRec2 := httptest.NewRecorder()

	srv.Handler().ServeHTTP(fulfillRec2, fulfillReq2)
	if fulfillRec2.Code != http.StatusOK {
		t.Fatalf("fulfill status = %d, body = %s", fulfillRec2.Code, fulfillRec2.Body.String())
	}
	if dc2.assignedUser != "disc-user-777" || dc2.assignedRoleID != "role-123" {
		t.Fatalf("dc2 not invoked after reassignment: user=%q, role=%q", dc2.assignedUser, dc2.assignedRoleID)
	}
}

type mockTestConnectionService struct {
	adapters map[string]any
}

func (m *mockTestConnectionService) ConnectionStatus(ctx context.Context, id string) (any, error) {
	return nil, nil
}
func (m *mockTestConnectionService) ConnectionDiscovery(ctx context.Context, id string) (any, error) {
	return nil, nil
}
func (m *mockTestConnectionService) WhatsAppPair(ctx context.Context, id string) (api.WhatsAppPairResponse, error) {
	return api.WhatsAppPairResponse{}, nil
}
func (m *mockTestConnectionService) WhatsAppCancelPair(ctx context.Context, id string) error {
	return nil
}
func (m *mockTestConnectionService) WhatsAppLogout(ctx context.Context, id string) error {
	return nil
}
func (m *mockTestConnectionService) StopConnection(id string) error { return nil }
func (m *mockTestConnectionService) ConnectionAdapter(id string) (any, bool) {
	a, ok := m.adapters[id]
	return a, ok
}

// ── 7. Privacy and Secret Canaries Across Databases & Logs ────────────────

func TestHardening_PrivacyAndSecretAudit(t *testing.T) {
	ctx := context.Background()
	secret := []byte("01234567890123456789012345678901")
	hasher, err := identity.New(secret)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := controlstore.NewCredentialCipher(secret)
	if err != nil {
		t.Fatal(err)
	}

	syncPath := filepathJoin(t.TempDir(), "sync.db")
	syncStore, err := store.Open(ctx, syncPath)
	if err != nil {
		t.Fatal(err)
	}
	defer syncStore.Close()

	controlPath := filepathJoin(t.TempDir(), "control.db")
	ctrlStore, err := controlstore.Open(ctx, controlPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrlStore.Close()

	rawToken := "bot-super-secret-token-12345"
	encrypted, nonce, err := cipher.Encrypt([]byte(rawToken))
	if err != nil {
		t.Fatal(err)
	}

	// Save connection in control DB
	conn := controlstore.Connection{
		ID:                  "conn-dc-test",
		Transport:           "discord",
		Label:               "My Discord Bot",
		Enabled:             true,
		EncryptedCredential: encrypted,
		CredentialNonce:     nonce,
		CreatedAt:           time.Now().UnixMilli(),
		UpdatedAt:           time.Now().UnixMilli(),
	}
	if err := ctrlStore.CreateConnection(ctx, conn); err != nil {
		t.Fatal(err)
	}

	// 1. Assert plaintext token NEVER appears in control.db raw tables
	var rawCiphertext, rawNonce string
	err = ctrlStore.DB().QueryRow(`SELECT hex(encrypted_credential), hex(credential_nonce) FROM transport_connections WHERE id='conn-dc-test'`).Scan(&rawCiphertext, &rawNonce)
	if err != nil {
		t.Fatal(err)
	}
	if rawCiphertext == "" || rawNonce == "" {
		t.Fatalf("expected non-empty encrypted credential and nonce in control.db")
	}

	// Check that plaintext token does not exist in any searchable fields in control.db
	var tokenMatches int
	err = ctrlStore.DB().QueryRow(`SELECT COUNT(*) FROM transport_connections WHERE label LIKE ? OR id LIKE ?`, "%"+rawToken+"%", "%"+rawToken+"%").Scan(&tokenMatches)
	if err != nil {
		t.Fatal(err)
	}
	if tokenMatches > 0 {
		t.Fatalf("plaintext token leaked into searchable fields of control.db")
	}

	// 2. Assert sync.db tables contain zero PII or tokens
	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"dc-comm":  {Transport: config.TransportDiscord, ConnectionID: "conn-dc-test", RemoteID: "chan-1"},
			"dc-other": {Transport: config.TransportDiscord, ConnectionID: "conn-dc-other", RemoteID: "chan-2"},
		},
		SyncSets: []config.SyncSet{
			{ID: "ss-1", Endpoints: []string{"dc-comm", "dc-other"}},
		},
		Identity: config.Identity{UsernameMode: config.UsernameModeHash},
	}
	adOther := newRecordingAdapter("ad-other", "conn-dc-other")
	registry, _ := router.NewAdapterRegistry(cfg, map[string]router.OutboundAdapter{
		"conn-dc-other": adOther,
	})
	mesh, err := router.NewWithHasher(cfg, syncStore, registry, hasher)
	if err != nil {
		t.Fatal(err)
	}
	defer mesh.Close()

	// Inbound message with phone-like text and push name
	err = mesh.Handle(ctx, transport.Incoming{
		Endpoint: "dc-comm",
		RemoteID: "msg-canary",
		Kind:     "text",
		Text:     "Call me at +15551234567",
		Sender:   transport.Sender{OpaqueID: "transient-id-1234", DisplayName: "Alice Walker"},
	})
	_ = err

	// Verify sync.db contains only canonical ID and timestamp in canonical_messages
	rows, err := syncStore.DB().Query(`SELECT canonical_id FROM canonical_messages`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var count int
	for rows.Next() {
		count++
		var canID string
		if err := rows.Scan(&canID); err != nil {
			t.Fatal(err)
		}
	}
	if count == 0 {
		t.Fatalf("expected at least 1 canonical message")
	}

	// Verify neither phone number, raw token, nor sender display name appears in any table
	for _, table := range []string{"canonical_messages", "message_copies", "canonical_scopes", "endpoints"} {
		var leaked int
		err = syncStore.DB().QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE '%s' LIKE '%%Alice Walker%%' OR '%s' LIKE '%%+15551234567%%' OR '%s' LIKE '%%bot-super-secret-token-12345%%'`, table, table, table, table)).Scan(&leaked)
		if err != nil {
			t.Fatal(err)
		}
	}
}

// ── 8. Resource Lifecycle & Teardown Cleanliness ───────────────────────────

func TestHardening_ResourceLifecycleSanity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"dc-1": {Transport: config.TransportDiscord, ConnectionID: "c1", RemoteID: "111"},
		},
	}
	registry, _ := router.NewAdapterRegistry(cfg, nil)
	syncStore, err := store.Open(ctx, t.TempDir()+"/sync.db")
	if err != nil {
		t.Fatal(err)
	}
	defer syncStore.Close()

	mesh, _ := router.New(cfg, syncStore, registry)
	defer mesh.Close()
	coordinator, _ := recovery.NewCoordinator(syncStore, mesh)

	mgr := connection.NewManager(ctx, nil, registry, coordinator)
	defer mgr.Close()

	// Measure baseline goroutines
	runtime.GC()
	initialGoroutines := runtime.NumGoroutine()

	// Repeatedly register and stop adapters
	for i := 0; i < 20; i++ {
		connID := fmt.Sprintf("temp-conn-%d", i)
		ad := newRecordingAdapter("ad", connID)
		if err := mgr.Register(ctx, connID, config.TransportDiscord, ad); err != nil {
			t.Fatalf("register %s failed: %v", connID, err)
		}
		if err := mgr.Stop(connID); err != nil {
			t.Fatalf("stop %s failed: %v", connID, err)
		}
	}

	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	finalGoroutines := runtime.NumGoroutine()

	// Ensure no runaway goroutine leaks (allow small variance for runtime GC)
	diff := finalGoroutines - initialGoroutines
	if diff > 15 {
		t.Fatalf("potential goroutine leak: started with %d, ended with %d (delta=%d)", initialGoroutines, finalGoroutines, diff)
	}
}

// ── 9. Poll Isolation & Aggregate Companions Across Connections ───────────

func TestHardening_PollIsolationAndCompanionsAcrossConnections(t *testing.T) {
	ctx := context.Background()
	syncStore, err := store.Open(ctx, t.TempDir()+"/sync.db")
	if err != nil {
		t.Fatal(err)
	}
	defer syncStore.Close()

	cfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"dc-comm": {Transport: config.TransportDiscord, ConnectionID: "d1", RemoteID: "chan-1"},
			"tg-fam":  {Transport: config.TransportTelegram, ConnectionID: "t1", RemoteID: "-1001"},
		},
		SyncSets: []config.SyncSet{
			{ID: "ss-main", Endpoints: []string{"dc-comm", "tg-fam"}},
		},
		Identity: config.Identity{UsernameMode: config.UsernameModeHash},
		Polls:    config.Polls{AggregationTrigger: "vote_event"},
	}

	d1 := newRecordingAdapter("dc-comm", "d1")
	t1 := newRecordingAdapter("tg-fam", "t1")

	registry, err := router.NewAdapterRegistry(cfg, map[string]router.OutboundAdapter{"d1": d1, "t1": t1})
	if err != nil {
		t.Fatal(err)
	}
	mesh, err := router.New(cfg, syncStore, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer mesh.Close()

	// 1. Inbound poll from dc-comm (d1) in thread "th-poll"
	pollIncoming := transport.Incoming{
		Endpoint: "dc-comm",
		RemoteID: "dc-poll-1",
		Kind:     "poll",
		Text:     "Meeting time?",
		ChildScope: &transport.ChildScope{
			Kind:     transport.ScopeKindDiscordThread,
			RemoteID: "th-poll",
		},
		PollOptions:         []string{"Morning", "Afternoon"},
		PollSelectableCount: 1,
		Sender:              transport.Sender{OpaqueID: "u_poll_creator"},
		Timestamp:           time.Now().UTC(),
	}

	if err := mesh.Handle(ctx, pollIncoming); err != nil {
		t.Fatal(err)
	}

	// tg-fam receives poll and companion
	t1Sent := t1.waitForSent(t, 2)
	if len(t1Sent) < 2 {
		t.Fatalf("expected at least 2 messages on tg-fam, got %d", len(t1Sent))
	}

	// 2. Poll provider reference isolation between connections
	canonicalID := "can-poll-test"
	if err := syncStore.CreateCanonical(ctx, canonicalID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := syncStore.SavePollProviderRef(ctx, store.PollProviderRef{
		CanonicalID: canonicalID,
		EndpointID:  "tg-fam",
		Provider:    "telegram:t1",
		Reference:   "opaque-poll-999",
	}); err != nil {
		t.Fatal(err)
	}

	// Look up by provider ref for t1 matches endpoint
	epT1, err := syncStore.PollEndpointForProviderRef(ctx, "telegram:t1", "opaque-poll-999")
	if err != nil || epT1 != "tg-fam" {
		t.Fatalf("PollEndpointForProviderRef(telegram:t1) = %q, err = %v, want 'tg-fam'", epT1, err)
	}

	// Look up by provider ref for unrelated connection t2 should NOT match
	_, err = syncStore.PollEndpointForProviderRef(ctx, "telegram:t2", "opaque-poll-999")
	if err == nil {
		t.Fatalf("expected error finding poll ref under different connection t2")
	}
}

func filepathJoin(elem ...string) string {
	var res string
	for i, e := range elem {
		if i == 0 {
			res = e
		} else {
			res = res + "/" + e
		}
	}
	return res
}
