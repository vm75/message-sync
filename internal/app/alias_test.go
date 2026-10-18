package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/store"
)

// Run normally receives configuration loaded from the same routing database.
func seedRoutingConfig(t *testing.T, dataDir string, cfg *config.Config) {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(dataDir, SyncDBName))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(cfg.Endpoints) < 2 || len(cfg.SyncSets) == 0 {
		return
	}
	if err := config.Save(context.Background(), s.DB(), cfg); err != nil {
		t.Fatal(err)
	}
}
