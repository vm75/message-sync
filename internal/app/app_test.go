package app

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/transport"
	whatsapp "github.com/vm75/message-sync/internal/transport/whatsapp"
)

type fakeWhatsAppTransport struct {
	events chan transport.Incoming
}

func (f *fakeWhatsAppTransport) Events() <-chan transport.Incoming { return f.events }
func (f *fakeWhatsAppTransport) Close() error                      { return nil }

func TestRunKeepsProtocolPIIOutOfApplicationDatabaseAndLogs(t *testing.T) {
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
		Identity: config.Identity{UsernameMode: "hash"},
		Media:    config.Media{MaxSizeMB: 100},
		Recovery: config.Recovery{MaxAgeHours: 24, MaxMessagesPerGroup: 200},
		Storage:  config.Storage{MessageRetentionDays: 90},
	}

	originalOpen := openWhatsApp
	defer func() { openWhatsApp = originalOpen }()
	openWhatsApp = func(_ context.Context, opts whatsapp.Options) (whatsappTransport, error) {
		if err := os.WriteFile(opts.DatabasePath, []byte("sensitive protocol state: 123456789@g.us 15551234567 Alice Example private body"), 0o600); err != nil {
			return nil, err
		}
		events := make(chan transport.Incoming, 1)
		events <- transport.Incoming{
			Endpoint: "c1g1",
			RemoteID: "opaque-remote-id",
			Sender: transport.Sender{
				DisplayName: "Alice Example",
				OpaqueID:    "u_abcdefghij",
			},
			Kind: "text",
			Text: "private body",
		}
		return &fakeWhatsAppTransport{events: events}, nil
	}

	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, nil))
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, cfg, logger) }()

	syncPath := filepath.Join(dataDir, SyncDBName)
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(syncPath); err == nil && strings.Contains(out.String(), "message_received") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(syncPath); err != nil {
		cancel()
		t.Fatalf("sync.db missing: %v", err)
	}
	if !strings.Contains(out.String(), "message_received") {
		cancel()
		t.Fatal("normalized event was not observed by the application")
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
		"opaque-remote-id",
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
}
