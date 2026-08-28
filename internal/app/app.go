package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vm75/message-sync/internal/api"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/router"
	"github.com/vm75/message-sync/internal/safelog"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/transport"
	discord "github.com/vm75/message-sync/internal/transport/discord"
	whatsapp "github.com/vm75/message-sync/internal/transport/whatsapp"
	"go.mau.fi/whatsmeow/types"
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
	UpdateConfig(*config.Config) error
}

var openWhatsApp = func(ctx context.Context, opts whatsapp.Options) (whatsappTransport, error) {
	return whatsapp.Open(ctx, opts)
}

type discordTransport interface {
	Events() <-chan transport.Incoming
	Send(context.Context, transport.Outgoing) (transport.MessageRef, error)
	React(context.Context, transport.Reaction) error
	Edit(context.Context, transport.MessageRef, string) error
	Delete(context.Context, transport.MessageRef) error
	Close() error
	UpdateConfig(*config.Config) error
}

var openDiscord = func(ctx context.Context, opts discord.Options) (discordTransport, error) {
	return discord.Open(ctx, opts)
}

// Run supervises persistence, the WhatsApp adapter, the HTTP API server, and
// the single ordered router worker.
func Run(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
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

	if cfg == nil {
		loadedCfg, err := config.LoadRaw(ctx, syncStore.DB())
		if err != nil {
			return fmt.Errorf("load config from sync database: %w", err)
		}
		cfg = loadedCfg
	}

	groupJIDs := make(map[string]string)
	discordChannelIDs := make(map[string]string)
	for alias, endpoint := range cfg.Endpoints {
		switch endpoint.Transport {
		case config.TransportWhatsApp:
			groupJIDs[alias] = endpoint.RemoteID
		case config.TransportDiscord:
			discordChannelIDs[alias] = endpoint.RemoteID
		}
	}

	wa, err := openWhatsApp(ctx, whatsapp.Options{
		DatabasePath:     filepath.Join(dataDir, WhatsAppDBName),
		GroupJIDs:        groupJIDs,
		Hasher:           hasher,
		UsernameMode:     cfg.Identity.UsernameMode,
		Logger:           logger,
		QROut:            os.Stdout,
		EnableTerminalQR: false,
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

	var dc discordTransport
	if len(discordChannelIDs) > 0 {
		token, err := discord.LoadBotToken()
		if err != nil {
			return err
		}
		dc, err = openDiscord(ctx, discord.Options{
			Token:         token,
			ChannelIDs:    discordChannelIDs,
			Hasher:        hasher,
			UsernameMode:  cfg.Identity.UsernameMode,
			Logger:        logger,
			MediaEnabled:  cfg.Media.Enabled,
			MediaMaxBytes: uint64(cfg.Media.MaxSizeMB) * 1024 * 1024,
		})
		if err != nil {
			return fmt.Errorf("start Discord transport: %w", err)
		}
		defer dc.Close()
	}

	var waService api.WhatsAppService
	if s, ok := wa.(api.WhatsAppService); ok {
		waService = s
	}

	adapters := map[config.Transport]router.OutboundAdapter{
		config.TransportWhatsApp: wa,
	}
	if dc != nil {
		adapters[config.TransportDiscord] = dc
	}
	adapterRegistry, err := router.NewAdapterRegistry(cfg, adapters)
	if err != nil {
		return fmt.Errorf("create transport adapter registry: %w", err)
	}

	mesh, err := router.New(cfg, syncStore, adapterRegistry)
	if err != nil {
		return fmt.Errorf("create canonical router: %w", err)
	}

	onConfigChange := func(updateCtx context.Context) error {
		updatedCfg, err := config.LoadRaw(updateCtx, syncStore.DB())
		if err != nil {
			return fmt.Errorf("reload config: %w", err)
		}
		if err := adapterRegistry.UpdateConfig(updatedCfg); err != nil {
			return fmt.Errorf("update transport adapter registry: %w", err)
		}
		if err := mesh.UpdateConfig(updatedCfg); err != nil {
			return fmt.Errorf("update router config: %w", err)
		}
		if err := wa.UpdateConfig(updatedCfg); err != nil {
			return fmt.Errorf("update whatsapp config: %w", err)
		}
		if dc != nil {
			if err := dc.UpdateConfig(updatedCfg); err != nil {
				return fmt.Errorf("update Discord config: %w", err)
			}
		}
		logger.Info("configuration reloaded",
			"endpoints", len(updatedCfg.Endpoints),
			"sync_sets", len(updatedCfg.SyncSets),
			"username_mode", string(updatedCfg.Identity.UsernameMode),
		)
		return nil
	}

	apiAddr := strings.TrimSpace(os.Getenv("API_ADDR"))
	if apiAddr == "" {
		port := strings.TrimSpace(os.Getenv("PORT"))
		if port == "" {
			port = "8080"
		}
		if !strings.HasPrefix(port, ":") {
			apiAddr = ":" + port
		} else {
			apiAddr = port
		}
	}
	apiServer := api.NewServer(api.Options{
		Addr:           apiAddr,
		Logger:         logger,
		DB:             syncStore.DB(),
		Secret:         []byte(secret),
		WhatsApp:       waService,
		OnConfigChange: onConfigChange,
	})
	if err := apiServer.Start(); err != nil {
		return fmt.Errorf("start api server: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = apiServer.Shutdown(shutdownCtx)
	}()

	syncDBPath := filepath.Join(dataDir, SyncDBName)
	if deleted, err := syncStore.PruneRetention(ctx, cfg.Storage.MessageRetentionDays, 1000); err != nil {
		safelog.Error(logger, "initial retention prune failed", "retention_prune", err)
	} else if deleted > 0 {
		logger.Info("retention prune completed", "deleted_messages", deleted)
	}

	runWhatsAppChatCleanup(ctx, logger, syncStore.DB(), wa)

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

	logger.Info("message-sync started",
		"endpoints", len(cfg.Endpoints),
		"sync_sets", len(cfg.SyncSets),
		"sync_schema", store.SchemaVersion,
	)

	pruneTicker := time.NewTicker(24 * time.Hour)
	defer pruneTicker.Stop()

	whatsAppEvents := wa.Events()
	var discordEvents <-chan transport.Incoming
	if dc != nil {
		discordEvents = dc.Events()
	}

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
			runWhatsAppChatCleanup(ctx, logger, syncStore.DB(), wa)
			if metrics, err := syncStore.Metrics(ctx, syncDBPath); err == nil {
				logger.Info("storage metrics",
					"canonical_messages", metrics.CanonicalMessages,
					"message_copies", metrics.MessageCopies,
					"reactions", metrics.Reactions,
					"sync_db_bytes", metrics.DatabaseSizeBytes,
				)
			}
		case incoming, ok := <-whatsAppEvents:
			if !ok {
				return errors.New("WhatsApp event stream closed")
			}
			if err := mesh.Handle(ctx, incoming); err != nil {
				safelog.Error(logger, "message routing failed", "route_message", err)
				continue
			}
			logger.Info("message routed",
				"event", "message_routed",
				"endpoint", string(incoming.Endpoint),
				"kind", incoming.Kind,
			)
		case incoming, ok := <-discordEvents:
			if !ok {
				return errors.New("Discord event stream closed")
			}
			if err := mesh.Handle(ctx, incoming); err != nil {
				safelog.Error(logger, "message routing failed", "route_message", err)
				continue
			}
			logger.Info("message routed",
				"event", "message_routed",
				"endpoint", string(incoming.Endpoint),
				"kind", incoming.Kind,
			)
		}
	}
}

func runWhatsAppChatCleanup(ctx context.Context, logger *slog.Logger, db *sql.DB, wa any) {
	cleaner, ok := wa.(interface {
		IsLoggedIn() bool
		ClearSyncSetChats(context.Context, []types.JID, time.Time) (int, error)
	})
	if !ok || cleaner == nil || !cleaner.IsLoggedIn() {
		return
	}
	cfg, err := config.LoadRaw(ctx, db)
	if err != nil {
		safelog.Error(logger, "failed to load config for whatsapp cleanup", "whatsapp_cleanup", err)
		return
	}
	if !cfg.WhatsAppCleanup.Enabled {
		return
	}
	retentionDays := cfg.WhatsAppCleanup.RetentionDays
	if retentionDays < 1 {
		retentionDays = 30
	}

	// Extract distinct group JIDs configured in sync-sets
	syncSetGroupAliases := make(map[string]struct{})
	for _, set := range cfg.SyncSets {
		for _, alias := range set.Groups {
			syncSetGroupAliases[alias] = struct{}{}
		}
	}
	groupJIDMap := make(map[string]types.JID)
	for alias := range syncSetGroupAliases {
		if endpoint, ok := cfg.Endpoints[alias]; ok && endpoint.Transport == config.TransportWhatsApp && strings.TrimSpace(endpoint.RemoteID) != "" {
			parsedJID, err := types.ParseJID(endpoint.RemoteID)
			if err == nil {
				groupJIDMap[parsedJID.String()] = parsedJID
			}
		}
	}
	if len(groupJIDMap) == 0 {
		return
	}

	jids := make([]types.JID, 0, len(groupJIDMap))
	for _, jid := range groupJIDMap {
		jids = append(jids, jid)
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
	cleared, err := cleaner.ClearSyncSetChats(ctx, jids, cutoff)
	if err != nil {
		safelog.Error(logger, "whatsapp chat cleanup had errors", "whatsapp_cleanup", err)
	}
	if cleared > 0 {
		logger.Info("whatsapp chat cleanup completed",
			"groups_cleared", cleared,
			"retention_days", retentionDays,
		)
	}
}
