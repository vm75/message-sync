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
	"github.com/vm75/message-sync/internal/connection"
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
		DeviceName:       cfg.WhatsAppDeviceName,
	})
	if err != nil {
		return fmt.Errorf("start WhatsApp transport: %w", err)
	}
	defer wa.Close()

	var credentialCipher *controlstore.CredentialCipher
	if len(secret) >= 32 {
		credentialCipher, _ = controlstore.NewCredentialCipher([]byte(secret))
	}

	discordAdapters := make(map[string]discordTransport)
	if controlStore != nil && credentialCipher != nil {
		allConns, err := controlStore.ListConnections(ctx)
		if err == nil {
			for _, c := range allConns {
				if c.Transport == "discord" && c.Enabled {
					tokenBytes, err := credentialCipher.Decrypt(c.EncryptedCredential, c.CredentialNonce)
					if err != nil {
						safelog.Error(logger, "decrypt discord credential failed", "discord_decrypt", err)
						continue
					}
					connChannelIDs := make(map[string]string)
					for alias, ep := range cfg.Endpoints {
						if ep.Transport == config.TransportDiscord && ep.ConnectionID == c.ID {
							connChannelIDs[alias] = ep.RemoteID
						}
					}
					dcInst, err := openDiscord(ctx, discord.Options{
						ConnectionID:  c.ID,
						Token:         string(tokenBytes),
						ChannelIDs:    connChannelIDs,
						Hasher:        hasher,
						UsernameMode:  cfg.Identity.UsernameMode,
						Logger:        logger,
						MediaEnabled:  cfg.Media.Enabled,
						MediaMaxBytes: uint64(cfg.Media.MaxSizeMB) * 1024 * 1024,
					})
					if err != nil {
						safelog.Error(logger, "start Discord transport failed", "discord_start", err)
						continue
					}
					discordAdapters[c.ID] = dcInst
					defer dcInst.Close()
				}
			}
		}
	}

	var dc discord.AdminService
	for _, da := range discordAdapters {
		if s, ok := da.(discord.AdminService); ok {
			dc = s
			break
		}
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

	adapterRegistry, err := router.NewAdapterRegistry(cfg, nil)
	if err != nil {
		return fmt.Errorf("create transport adapter registry: %w", err)
	}

	mesh, err := router.NewWithHasher(cfg, syncStore, adapterRegistry, hasher)
	if err != nil {
		return fmt.Errorf("create canonical router: %w", err)
	}
	defer mesh.Close()
	recoveryCoordinator, err := recovery.NewCoordinator(syncStore, mesh)
	if err != nil {
		return fmt.Errorf("create recovery coordinator: %w", err)
	}

	connMgr := connection.NewManager(ctx, logger, adapterRegistry, recoveryCoordinator)
	defer connMgr.Close()

	registerActiveConnections := func(c *config.Config) {
		registered := make(map[string]bool)
		for connID, da := range discordAdapters {
			if err := connMgr.Register(ctx, connID, config.TransportDiscord, da); err == nil {
				registered[connID] = true
			}
		}
		for _, ep := range c.Endpoints {
			if registered[ep.ConnectionID] {
				continue
			}
			switch ep.Transport {
			case config.TransportWhatsApp:
				if wa != nil {
					if err := connMgr.Register(ctx, ep.ConnectionID, config.TransportWhatsApp, wa); err == nil {
						registered[ep.ConnectionID] = true
					}
				}
			case config.TransportTelegram:
				if tg != nil {
					if err := connMgr.Register(ctx, ep.ConnectionID, config.TransportTelegram, tg); err == nil {
						registered[ep.ConnectionID] = true
					}
				}
			}
		}
		if wa != nil && !registered["conn-wa-1"] {
			_ = connMgr.Register(ctx, "conn-wa-1", config.TransportWhatsApp, wa)
		}
		if tg != nil && !registered["conn-tg-1"] {
			_ = connMgr.Register(ctx, "conn-tg-1", config.TransportTelegram, tg)
		}
	}
	registerActiveConnections(cfg)

	onConfigChange := func(updateCtx context.Context) error {
		updatedCfg, err := config.LoadRaw(updateCtx, syncStore.DB())
		if err != nil {
			return fmt.Errorf("reload config: %w", err)
		}
		if controlStore != nil && credentialCipher != nil {
			allConns, err := controlStore.ListConnections(updateCtx)
			if err == nil {
				enabledDiscord := make(map[string]controlstore.Connection)
				for _, c := range allConns {
					if c.Transport == "discord" && c.Enabled {
						enabledDiscord[c.ID] = c
					}
				}
				for connID := range discordAdapters {
					if _, ok := enabledDiscord[connID]; !ok {
						_ = connMgr.Stop(connID)
						delete(discordAdapters, connID)
					}
				}
				for connID, c := range enabledDiscord {
					if _, exists := discordAdapters[connID]; !exists {
						tokenBytes, err := credentialCipher.Decrypt(c.EncryptedCredential, c.CredentialNonce)
						if err != nil {
							safelog.Error(logger, "decrypt discord credential failed", "discord_decrypt", err)
							continue
						}
						connChannelIDs := make(map[string]string)
						for alias, ep := range updatedCfg.Endpoints {
							if ep.Transport == config.TransportDiscord && ep.ConnectionID == c.ID {
								connChannelIDs[alias] = ep.RemoteID
							}
						}
						dcInst, err := openDiscord(ctx, discord.Options{
							ConnectionID:  c.ID,
							Token:         string(tokenBytes),
							ChannelIDs:    connChannelIDs,
							Hasher:        hasher,
							UsernameMode:  updatedCfg.Identity.UsernameMode,
							Logger:        logger,
							MediaEnabled:  updatedCfg.Media.Enabled,
							MediaMaxBytes: uint64(updatedCfg.Media.MaxSizeMB) * 1024 * 1024,
						})
						if err != nil {
							safelog.Error(logger, "start Discord transport failed", "discord_start", err)
							continue
						}
						discordAdapters[c.ID] = dcInst
						_ = connMgr.Register(ctx, c.ID, config.TransportDiscord, dcInst)
					}
				}
			}
		}
		registerActiveConnections(updatedCfg)
		if err := connMgr.UpdateConfig(updatedCfg); err != nil {
			return fmt.Errorf("update connection manager: %w", err)
		}
		if err := mesh.UpdateConfig(updatedCfg); err != nil {
			return fmt.Errorf("update router config: %w", err)
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
		case incoming, ok := <-connMgr.Events():
			if !ok {
				return errors.New("connection manager event stream closed")
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
