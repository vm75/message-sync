package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/router"
	"github.com/vm75/message-sync/internal/safelog"
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
	Send(context.Context, transport.Outgoing) (transport.MessageRef, error)
	React(context.Context, transport.Reaction) error
	Edit(context.Context, transport.MessageRef, string) error
	Delete(context.Context, transport.MessageRef) error
	Close() error
}

var openWhatsApp = func(ctx context.Context, opts whatsapp.Options) (whatsappTransport, error) {
	return whatsapp.Open(ctx, opts)
}

// Run supervises persistence, the WhatsApp adapter, and the single ordered
// router worker. Message bodies and participant identity are transient.
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
		DatabasePath:     filepath.Join(dataDir, WhatsAppDBName),
		GroupJIDs:        groupJIDs,
		Hasher:           hasher,
		UsernameMode:     cfg.Identity.UsernameMode,
		Logger:           logger,
		QROut:            os.Stdout,
		MediaEnabled:     cfg.Media.Enabled,
		MediaMaxBytes:    uint64(cfg.Media.MaxSizeMB) * 1024 * 1024,
		RecoveryEnabled:  cfg.Recovery.Enabled,
		RecoveryMaxAge:   time.Duration(cfg.Recovery.MaxAgeHours) * time.Hour,
		RecoveryMaxCount: cfg.Recovery.MaxMessagesPerGroup,
	})
	if err != nil {
		return fmt.Errorf("start WhatsApp transport: %w", err)
	}
	defer wa.Close()

	syncDBPath := filepath.Join(dataDir, SyncDBName)
	if deleted, err := syncStore.PruneRetention(ctx, cfg.Storage.MessageRetentionDays, 1000); err != nil {
		safelog.Error(logger, "initial retention prune failed", "retention_prune", err)
	} else if deleted > 0 {
		logger.Info("retention prune completed", "deleted_messages", deleted)
	}

	if metrics, err := syncStore.Metrics(ctx, syncDBPath); err != nil {
		safelog.Error(logger, "fetch storage metrics failed", "storage_metrics", err)
	} else {
		logger.Info("storage metrics",
			"canonical_messages", metrics.CanonicalMessages,
			"message_copies", metrics.MessageCopies,
			"reactions", metrics.Reactions,
			"sync_db_bytes", metrics.DatabaseSizeBytes,
		)
	}

	mesh, err := router.New(cfg, syncStore, wa)
	if err != nil {
		return fmt.Errorf("create canonical router: %w", err)
	}

	logger.Info("message-sync started",
		"groups", len(cfg.Groups),
		"sync_sets", len(cfg.SyncSets),
		"sync_schema", store.SchemaVersion,
	)

	pruneTicker := time.NewTicker(24 * time.Hour)
	defer pruneTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("message-sync stopping")
			return nil
		case <-pruneTicker.C:
			if deleted, err := syncStore.PruneRetention(ctx, cfg.Storage.MessageRetentionDays, 1000); err != nil {
				safelog.Error(logger, "periodic retention prune failed", "retention_prune", err)
			} else if deleted > 0 {
				logger.Info("retention prune completed", "deleted_messages", deleted)
			}
			if metrics, err := syncStore.Metrics(ctx, syncDBPath); err == nil {
				logger.Info("storage metrics",
					"canonical_messages", metrics.CanonicalMessages,
					"message_copies", metrics.MessageCopies,
					"reactions", metrics.Reactions,
					"sync_db_bytes", metrics.DatabaseSizeBytes,
				)
			}
		case incoming, ok := <-wa.Events():
			if !ok {
				return errors.New("WhatsApp event stream closed")
			}
			if err := mesh.Handle(ctx, incoming); err != nil {
				safelog.Error(logger, "message routing failed", "route_message", err)
				continue
			}
			logger.Info("WhatsApp message routed",
				"event", "message_routed",
				"endpoint", string(incoming.Endpoint),
				"kind", incoming.Kind,
			)
		}
	}
}
