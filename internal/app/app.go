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
	"github.com/vm75/message-sync/internal/controlstore"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/recovery"
	"github.com/vm75/message-sync/internal/router"
	"github.com/vm75/message-sync/internal/safelog"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/transport"
	discord "github.com/vm75/message-sync/internal/transport/discord"
	telegram "github.com/vm75/message-sync/internal/transport/telegram"
	whatsapp "github.com/vm75/message-sync/internal/transport/whatsapp"
	"github.com/vm75/message-sync/internal/verification"
	"go.mau.fi/whatsmeow/types"
)

const (
	WhatsAppDBName = "whatsapp.db"
	SyncDBName     = "sync.db"
	ControlDBName  = "control.db"
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
	discord.AdminService
}

var openDiscord = func(ctx context.Context, opts discord.Options) (discordTransport, error) {
	return discord.Open(ctx, opts)
}

type telegramTransport interface {
	Events() <-chan transport.Incoming
	Send(context.Context, transport.Outgoing) (transport.MessageRef, error)
	React(context.Context, transport.Reaction) error
	Edit(context.Context, transport.MessageRef, string) error
	Delete(context.Context, transport.MessageRef) error
	Close() error
	UpdateConfig(*config.Config) error
	telegram.AdminService
}

var openTelegram = func(ctx context.Context, opts telegram.Options) (telegramTransport, error) {
	return telegram.Open(ctx, opts)
}

// Run supervises persistence, the transport adapters, the HTTP API server, and
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
	controlStore, err := controlstore.Open(ctx, filepath.Join(dataDir, ControlDBName))
	if err != nil {
		return fmt.Errorf("open control store: %w", err)
	}
	defer controlStore.Close()

	if cfg == nil {
		loadedCfg, err := config.LoadRaw(ctx, syncStore.DB())
		if err != nil {
			return fmt.Errorf("load config from sync database: %w", err)
		}
		cfg = loadedCfg
	}

	groupJIDs := make(map[string]string)
	discordChannelIDs := make(map[string]string)
	telegramChatIDs := make(map[string]string)
	for alias, endpoint := range cfg.Endpoints {
		switch endpoint.Transport {
		case config.TransportWhatsApp:
			groupJIDs[alias] = endpoint.RemoteID
		case config.TransportDiscord:
			discordChannelIDs[alias] = endpoint.RemoteID
		case config.TransportTelegram:
			telegramChatIDs[alias] = endpoint.RemoteID
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
	if len(discordChannelIDs) > 0 || discord.BotTokenConfigured() {
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

	var tg telegramTransport
	if len(telegramChatIDs) > 0 || telegram.BotTokenConfigured() {
		initialUpdateID, err := telegramInitialUpdateID(ctx, syncStore)
		if err != nil {
			return fmt.Errorf("load Telegram recovery cursor: %w", err)
		}
		tg, err = openTelegram(ctx, telegram.Options{
			ChatIDs:         telegramChatIDs,
			Hasher:          hasher,
			UsernameMode:    cfg.Identity.UsernameMode,
			Logger:          logger,
			MediaEnabled:    cfg.Media.Enabled,
			MediaMaxBytes:   uint64(cfg.Media.MaxSizeMB) * 1024 * 1024,
			InitialUpdateID: initialUpdateID,
			MigrateEndpoint: func(migrationCtx context.Context, endpoint transport.EndpointID, oldRemoteID, newRemoteID string) error {
				return config.MigrateTelegramEndpoint(migrationCtx, syncStore.DB(), string(endpoint), oldRemoteID, newRemoteID)
			},
			ResolvePollEndpoint: func(resolveCtx context.Context, pollID string) (transport.EndpointID, bool) {
				endpoint, resolveErr := syncStore.PollEndpointForProviderRef(resolveCtx, "telegram", pollID)
				if resolveErr != nil {
					return "", false
				}
				return transport.EndpointID(endpoint), true
			},
		})
		if err != nil {
			return fmt.Errorf("start Telegram transport: %w", err)
		}
		defer tg.Close()
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
	if tg != nil {
		adapters[config.TransportTelegram] = tg
	}
	adapterRegistry, err := router.NewAdapterRegistry(cfg, adapters)
	if err != nil {
		return fmt.Errorf("create transport adapter registry: %w", err)
	}

	mesh, err := router.New(cfg, syncStore, adapterRegistry)
	if err != nil {
		return fmt.Errorf("create canonical router: %w", err)
	}
	defer mesh.Close()
	recoveryCoordinator, err := recovery.NewCoordinator(syncStore, mesh)
	if err != nil {
		return fmt.Errorf("create recovery coordinator: %w", err)
	}
	var recoverySources []transport.RecoverySource
	for _, adapter := range []any{wa, dc, tg} {
		if source, ok := adapter.(transport.RecoverySource); ok {
			recoverySources = append(recoverySources, source)
		}
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
		if tg != nil {
			if err := tg.UpdateConfig(updatedCfg); err != nil {
				return fmt.Errorf("update Telegram config: %w", err)
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
		ControlDB:      controlStore.DB(),
		Secret:         []byte(secret),
		WhatsApp:       waService,
		Discord:        dc,
		Telegram:       tg,
		Delivery:       mesh,
		OnConfigChange: onConfigChange,
		EvidenceDir:    filepath.Join(dataDir, "membership-evidence"),
		Mailer:         verification.NewResendMailerFromEnv(),
		Analyzer:       verification.NewOpenRouterFromEnv(),
	})
	if err := apiServer.Start(); err != nil {
		return fmt.Errorf("start api server: %w", err)
	}
	recoveryCoordinator.Start(ctx, recoverySources)
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
	pruneControl := func() {
		refs, err := controlStore.PruneRetention(ctx, time.Now().UTC(), 30*24*time.Hour, 100)
		if err != nil {
			safelog.Error(logger, "control retention prune failed", "control_retention", err)
			return
		}
		for _, ref := range refs {
			if filepath.Base(ref) == ref {
				_ = os.Remove(filepath.Join(dataDir, "membership-evidence", ref))
			}
		}
	}
	pruneControl()

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
	)

	pruneTicker := time.NewTicker(24 * time.Hour)
	defer pruneTicker.Stop()

	whatsAppEvents := wa.Events()
	var discordEvents <-chan transport.Incoming
	if dc != nil {
		discordEvents = dc.Events()
	}
	var telegramEvents <-chan transport.Incoming
	if tg != nil {
		telegramEvents = tg.Events()
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
			pruneControl()
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
			if _, err := recoveryCoordinator.Handle(ctx, incoming); err != nil {
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
			if _, err := recoveryCoordinator.Handle(ctx, incoming); err != nil {
				safelog.Error(logger, "message routing failed", "route_message", err)
				continue
			}
			logger.Info("message routed",
				"event", "message_routed",
				"endpoint", string(incoming.Endpoint),
				"kind", incoming.Kind,
			)
		case incoming, ok := <-telegramEvents:
			if !ok {
				return errors.New("Telegram event stream closed")
			}
			if _, err := recoveryCoordinator.Handle(ctx, incoming); err != nil {
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

func telegramInitialUpdateID(ctx context.Context, syncStore *store.Store) (int64, error) {
	if syncStore == nil {
		return 0, nil
	}
	cursor, err := syncStore.RecoveryCursor(ctx, "telegram")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return cursor.Position, nil
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
		for _, alias := range set.Endpoints {
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
