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
)

func TestRunCreatesOnlyApplicationDatabaseAndLogsNoProtocolPII(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	secret := "0123456789abcdef0123456789abcdef"
	t.Setenv("IDENTITY_SECRET", secret)

	cfg := &config.Config{
		Groups: map[string]config.Group{
			"c1g1": {JID: "group-one@g.us"},
			"c1g2": {JID: "group-two@g.us"},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Groups: []string{"c1g1", "c1g2"}}},
		Identity: config.Identity{UsernameMode: "hash"},
		Media:    config.Media{MaxSizeMB: 100},
		Recovery: config.Recovery{MaxAgeHours: 24, MaxMessagesPerGroup: 200},
		Storage:  config.Storage{MessageRetentionDays: 90},
	}

	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, nil))
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, cfg, logger) }()

	syncPath := filepath.Join(dataDir, SyncDBName)
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(syncPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(syncPath); err != nil {
		cancel()
		t.Fatalf("sync.db missing: %v", err)
	}
	cancel()
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, WhatsAppDBName)); !os.IsNotExist(err) {
		t.Fatalf("phase 0 must not create whatsapp.db, stat err = %v", err)
	}

	forbiddenValues := []string{secret, "group-one", "group-two", "@g.us"}
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
