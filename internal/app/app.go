package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/transport"
	whatsapp "github.com/vm75/message-sync/internal/transport/whatsapp"
)

const (
	WhatsAppDBName = "whatsapp.db"
	SyncDBName     = "sync.db"
)

type whatsappTransport interface {
	Events() <-chan transport.Incoming
	Close() error
}

var openWhatsApp = func(ctx context.Context, opts whatsapp.Options) (whatsappTransport, error) {
	return whatsapp.Open(ctx, opts)
}

// Run supervises application persistence and the Phase 1 WhatsApp transport.
// Raw WhatsApp protocol identity and content must be normalized before crossing
// the transport boundary. whatsapp.db remains an isolated protocol-state store.
func Run(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
	if cfg == nil {
		return errors.New("config is required")
	}
	if logger == nil {
		return errors.New("logger is required")
	}

	secret := strings.TrimSpace(os.Getenv("IDENTITY_SECRET"))
	hasher, err := identity.New([]byte(secret))
	if err != nil {
		return err
	}

	dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if dataDir == "" {
		dataDir = "/data"
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}

	syncStore, err := store.Open(ctx, filepath.Join(dataDir, SyncDBName))
	if err != nil {
		return err
	}
	defer syncStore.Close()

	groupJIDs := make(map[string]string, len(cfg.Groups))
	for alias, group := range cfg.Groups {
		groupJIDs[alias] = group.JID
	}

	wa, err := openWhatsApp(ctx, whatsapp.Options{
		DatabasePath: filepath.Join(dataDir, WhatsAppDBName),
		GroupJIDs:    groupJIDs,
		Hasher:       hasher,
		UsernameMode: cfg.Identity.UsernameMode,
		Logger:       logger,
		QROut:        os.Stdout,
	})
	if err != nil {
		return fmt.Errorf("start WhatsApp transport: %w", err)
	}
	defer wa.Close()

	logger.Info("message-sync started",
		"groups", len(cfg.Groups),
		"sync_sets", len(cfg.SyncSets),
		"sync_schema", store.SchemaVersion,
		"phase", 1,
	)

	for {
		select {
		case <-ctx.Done():
			logger.Info("message-sync stopping")
			return nil
		case incoming, ok := <-wa.Events():
			if !ok {
				return errors.New("WhatsApp event stream closed")
			}
			logger.Info("WhatsApp message normalized",
				"event", "message_received",
				"endpoint", string(incoming.Endpoint),
				"kind", incoming.Kind,
			)
		}
	}
}
