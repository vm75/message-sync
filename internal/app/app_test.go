package app

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/transport"
	whatsapp "github.com/vm75/message-sync/internal/transport/whatsapp"
)

type fakeWhatsAppTransport struct {
	events chan transport.Incoming
	mu     sync.Mutex
	sent   []transport.Outgoing
}

func (f *fakeWhatsAppTransport) Events() <-chan transport.Incoming { return f.events }
func (f *fakeWhatsAppTransport) Close() error                      { return nil }
func (f *fakeWhatsAppTransport) Send(_ context.Context, outgoing transport.Outgoing) (transport.MessageRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, outgoing)
	return transport.MessageRef{
		Endpoint:        outgoing.Endpoint,
		RemoteMessageID: "sent-" + string(outgoing.Endpoint),
	}, nil
}

func (f *fakeWhatsAppTransport) React(ctx context.Context, r transport.Reaction) error {
	return nil
}

func (f *fakeWhatsAppTransport) Edit(ctx context.Context, ref transport.MessageRef, text string) error {
	return nil
}

func (f *fakeWhatsAppTransport) Delete(ctx context.Context, ref transport.MessageRef) error {
	return nil
}

func TestRunRoutesWithoutPersistingProtocolPIIContentOrParticipantIdentity(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	secret := "0123456789abcdef0123456789abcdef"
	t.Setenv("IDENTITY_SECRET", secret)

	cfg := &config.Config{
		Groups: map[string]config.Group{
			"c1g1": {JID: "123456789@g.us"},
			"c1g2": {JID: "987654321@g.us"},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Groups: []string{"c1g1", "c1g2"}}},
		Identity: config.Identity{UsernameMode: "push_name"},
		Media:    config.Media{MaxSizeMB: 100},
		Recovery: config.Recovery{MaxAgeHours: 24, MaxMessagesPerGroup: 200},
		Storage:  config.Storage{MessageRetentionDays: 90},
	}

	originalOpen := openWhatsApp
	defer func() { openWhatsApp = originalOpen }()
	fake := &fakeWhatsAppTransport{}
	openWhatsApp = func(_ context.Context, opts whatsapp.Options) (whatsappTransport, error) {
		if err := os.WriteFile(opts.DatabasePath, []byte("sensitive protocol state: 123456789@g.us 15551234567 Alice Example private body"), 0o600); err != nil {
			return nil, err
		}
		fake.events = make(chan transport.Incoming, 1)
		fake.events <- transport.Incoming{
			Endpoint: "c1g1",
			RemoteID: "opaque-remote-id",
			Sender: transport.Sender{
				DisplayName: "Alice Example",
				OpaqueID:    "u_abcdefghij",
			},
			Kind: "text",
			Text: "private body",
		}
		return fake, nil
	}

	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, nil))
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, cfg, logger) }()

	syncPath := filepath.Join(dataDir, SyncDBName)
	for i := 0; i < 100; i++ {
		fake.mu.Lock()
		sentCount := len(fake.sent)
		fake.mu.Unlock()
		if _, err := os.Stat(syncPath); err == nil && strings.Contains(out.String(), "message_routed") && sentCount == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(syncPath); err != nil {
		cancel()
		t.Fatalf("sync.db missing: %v", err)
	}
	if !strings.Contains(out.String(), "message_routed") {
		cancel()
		t.Fatal("normalized event was not routed by the application")
	}

	fake.mu.Lock()
	if len(fake.sent) != 1 {
		fake.mu.Unlock()
		cancel()
		t.Fatalf("fan-out sent %d messages, want 1", len(fake.sent))
	}
	forwarded := fake.sent[0]
	fake.mu.Unlock()
	if forwarded.Endpoint != "c1g2" || forwarded.Text != "c1g1/Alice Example: private body" {
		cancel()
		t.Fatalf("unexpected forwarded message: %+v", forwarded)
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, WhatsAppDBName)); err != nil {
		t.Fatalf("whatsapp.db missing: %v", err)
	}

	forbiddenValues := []string{
		secret,
		"123456789@g.us",
		"987654321@g.us",
		"15551234567",
		"Alice Example",
		"private body",
		"u_abcdefghij",
	}
	logged := out.String()
	for _, forbidden := range forbiddenValues {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("log leaked forbidden value %q: %s", forbidden, logged)
		}
	}

	databaseBytes, err := os.ReadFile(syncPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range forbiddenValues {
		if strings.Contains(string(databaseBytes), forbidden) {
			t.Fatalf("sync.db leaked forbidden value %q", forbidden)
		}
	}
	for _, required := range []string{"c1g1", "c1g2", "opaque-remote-id", "sent-c1g2"} {
		if !strings.Contains(string(databaseBytes), required) {
			t.Fatalf("sync.db missing expected opaque routing value %q", required)
		}
	}
}

func TestRunStartupRetentionPruneAndMetricsLogging(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	secret := "0123456789abcdef0123456789abcdef"
	t.Setenv("IDENTITY_SECRET", secret)

	cfg := &config.Config{
		Groups: map[string]config.Group{
			"c1g1": {JID: "123456789@g.us"},
			"c1g2": {JID: "987654321@g.us"},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Groups: []string{"c1g1", "c1g2"}}},
		Identity: config.Identity{UsernameMode: "push_name"},
		Media:    config.Media{MaxSizeMB: 100},
		Recovery: config.Recovery{MaxAgeHours: 24, MaxMessagesPerGroup: 200},
		Storage:  config.Storage{MessageRetentionDays: 90},
	}

	// Pre-create sync.db with an expired canonical message (100 days old)
	syncPath := filepath.Join(dataDir, SyncDBName)
	preStore, err := store.Open(context.Background(), syncPath)
	if err != nil {
		t.Fatal(err)
	}
	expiredTime := time.Now().UTC().AddDate(0, 0, -100)
	if err := preStore.CreateCanonical(context.Background(), "expired-canonical", expiredTime); err != nil {
		t.Fatal(err)
	}
	if err := preStore.AddMessageCopy(context.Background(), store.MessageCopy{
		CanonicalID:     "expired-canonical",
		EndpointID:      "c1g1",
		RemoteMessageID: "expired-remote-1",
		CreatedAt:       expiredTime,
	}); err != nil {
		t.Fatal(err)
	}
	if err := preStore.Close(); err != nil {
		t.Fatal(err)
	}

	originalOpen := openWhatsApp
	defer func() { openWhatsApp = originalOpen }()
	fake := &fakeWhatsAppTransport{events: make(chan transport.Incoming)}
	openWhatsApp = func(_ context.Context, opts whatsapp.Options) (whatsappTransport, error) {
		return fake, nil
	}

	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, nil))
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, cfg, logger)
	}()

	// Wait briefly for startup retention prune and metrics logging
	for i := 0; i < 50; i++ {
		if strings.Contains(out.String(), "retention prune completed") && strings.Contains(out.String(), "storage metrics") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Run() returned error on graceful shutdown: %v", err)
	}

	logged := out.String()
	if !strings.Contains(logged, "retention prune completed") {
		t.Fatalf("expected log to contain retention prune completed: %s", logged)
	}
	if !strings.Contains(logged, "storage metrics") {
		t.Fatalf("expected log to contain storage metrics: %s", logged)
	}
	if !strings.Contains(logged, "message-sync stopping") {
		t.Fatalf("expected log to contain message-sync stopping: %s", logged)
	}

	// Verify that the expired message was deleted from sync.db during startup prune
	verifyStore, err := store.Open(context.Background(), syncPath)
	if err != nil {
		t.Fatal(err)
	}
	defer verifyStore.Close()
	metrics, err := verifyStore.Metrics(context.Background(), syncPath)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.CanonicalMessages != 0 || metrics.MessageCopies != 0 {
		t.Fatalf("expected 0 canonical messages after startup prune, got %+v", metrics)
	}
}
