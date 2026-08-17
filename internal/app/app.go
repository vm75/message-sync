package app

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
)

// Run is the MVP process supervisor. WhatsApp connectivity, SQLite repositories,
// and routing are added phase-by-phase; this scaffold validates all startup
// invariants and remains alive until shutdown.
func Run(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
	if logger == nil {
		return errors.New("logger is required")
	}

	secret := os.Getenv("IDENTITY_SECRET")
	if _, err := identity.New([]byte(secret)); err != nil {
		return err
	}

	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "/data"
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}

	logger.Info("message-sync started",
		"groups", len(cfg.Groups),
		"sync_sets", len(cfg.SyncSets),
		"data_dir", filepath.Clean(dataDir),
	)
	logger.Info("WhatsApp transport is not enabled in the foundation scaffold")

	<-ctx.Done()
	logger.Info("message-sync stopping")
	return nil
}
