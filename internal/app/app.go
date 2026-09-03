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
	"sync"
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

	whatsappAdapters := make(map[string]whatsappTransport)
	if controlStore != nil {
		allConns, err := controlStore.ListConnections(ctx)
		if err == nil {
			for _, c := range allConns {
				if c.Transport == "whatsapp" && c.Enabled {
					connGroupJIDs := make(map[string]string)
					for alias, ep := range cfg.Endpoints {
						if ep.Transport == config.TransportWhatsApp && (ep.ConnectionID == c.ID || (c.ID == "conn-wa-1" && ep.ConnectionID == "")) {
							connGroupJIDs[alias] = ep.RemoteID
						}
					}
					dbPath, err := whatsapp.ProtocolDBPath(dataDir, c.ID)
					if err != nil {
						safelog.Error(logger, "invalid whatsapp db path", "whatsapp_path", err)
						continue
					}
					waInst, err := openWhatsApp(ctx, whatsapp.Options{
						ConnectionID:     c.ID,
						DatabasePath:     dbPath,
						GroupJIDs:        connGroupJIDs,
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
						safelog.Error(logger, "start WhatsApp transport failed", "whatsapp_start", err)
						continue
					}
					whatsappAdapters[c.ID] = waInst
					defer waInst.Close()
				}
			}
		}
	}

	if len(whatsappAdapters) == 0 && len(groupJIDs) > 0 {
		dbPath, _ := whatsapp.ProtocolDBPath(dataDir, "conn-wa-1")
		waInst, err := openWhatsApp(ctx, whatsapp.Options{
			ConnectionID:     "conn-wa-1",
			DatabasePath:     dbPath,
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
		whatsappAdapters["conn-wa-1"] = waInst
		defer waInst.Close()
	}

	var wa whatsappTransport
	for _, w := range whatsappAdapters {
		wa = w
		break
	}

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

	telegramAdapters := make(map[string]telegramTransport)
	if controlStore != nil && credentialCipher != nil {
		allConns, err := controlStore.ListConnections(ctx)
		if err == nil {
			for _, c := range allConns {
				if c.Transport == "telegram" && c.Enabled {
					tokenBytes, err := credentialCipher.Decrypt(c.EncryptedCredential, c.CredentialNonce)
					if err != nil {
						safelog.Error(logger, "decrypt telegram credential failed", "telegram_decrypt", err)
						continue
					}
					connChatIDs := make(map[string]string)
					for alias, ep := range cfg.Endpoints {
						if ep.Transport == config.TransportTelegram && ep.ConnectionID == c.ID {
							connChatIDs[alias] = ep.RemoteID
						}
					}
					initialUpdateID, err := telegramInitialUpdateID(ctx, syncStore, c.ID)
					if err != nil {
						safelog.Error(logger, "load Telegram recovery cursor failed", "telegram_cursor", err)
						continue
					}
					connID := c.ID
					tgInst, err := openTelegram(ctx, telegram.Options{
						ConnectionID:    connID,
						Token:           string(tokenBytes),
						ChatIDs:         connChatIDs,
						Hasher:          hasher,
						UsernameMode:    cfg.Identity.UsernameMode,
						Logger:          logger,
						MediaEnabled:    cfg.Media.Enabled,
						MediaMaxBytes:   uint64(cfg.Media.MaxSizeMB) * 1024 * 1024,
						InitialUpdateID: initialUpdateID,
						MigrateEndpoint: func(migrationCtx context.Context, endpoint transport.EndpointID, oldRemoteID, newRemoteID string) error {
							return config.MigrateTelegramEndpoint(migrationCtx, syncStore.DB(), string(endpoint), connID, oldRemoteID, newRemoteID)
						},
						ResolvePollEndpoint: func(resolveCtx context.Context, pollID string) (transport.EndpointID, bool) {
							endpoint, resolveErr := syncStore.PollEndpointForProviderRef(resolveCtx, "telegram:"+connID, pollID)
							if resolveErr != nil {
								endpoint, resolveErr = syncStore.PollEndpointForProviderRef(resolveCtx, "telegram", pollID)
							}
							if resolveErr != nil {
								return "", false
							}
							return transport.EndpointID(endpoint), true
						},
					})
					if err != nil {
						safelog.Error(logger, "start Telegram transport failed", "telegram_start", err)
						continue
					}
					telegramAdapters[c.ID] = tgInst
					defer tgInst.Close()
				}
			}
		}
	}

	if len(telegramChatIDs) > 0 && len(telegramAdapters) == 0 {
		return errors.New("Telegram bot token is not configured")
	}

	var tg telegramTransport
	for _, ta := range telegramAdapters {
		tg = ta
		break
	}

	var currentCfgMu sync.RWMutex
	currentCfg := cfg

	waService := &appWhatsAppService{
		getAdapters: func() map[string]whatsappTransport {
			return whatsappAdapters
		},
		getEndpoints: func() map[string]config.Endpoint {
			currentCfgMu.RLock()
			defer currentCfgMu.RUnlock()
			return currentCfg.Endpoints
		},
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
		for connID, wa := range whatsappAdapters {
			if err := connMgr.Register(ctx, connID, config.TransportWhatsApp, wa); err == nil {
				registered[connID] = true
			}
		}
		for connID, da := range discordAdapters {
			if err := connMgr.Register(ctx, connID, config.TransportDiscord, da); err == nil {
				registered[connID] = true
			}
		}
		for connID, ta := range telegramAdapters {
			if err := connMgr.Register(ctx, connID, config.TransportTelegram, ta); err == nil {
				registered[connID] = true
			}
		}
		if wa != nil && !registered["conn-wa-1"] {
			_ = connMgr.Register(ctx, "conn-wa-1", config.TransportWhatsApp, wa)
		}
	}
	registerActiveConnections(cfg)

	onConfigChange := func(updateCtx context.Context) error {
		updatedCfg, err := config.LoadRaw(updateCtx, syncStore.DB())
		if err != nil {
			return fmt.Errorf("reload config: %w", err)
		}
		currentCfgMu.Lock()
		currentCfg = updatedCfg
		currentCfgMu.Unlock()
		if controlStore != nil && credentialCipher != nil {
			allConns, err := controlStore.ListConnections(updateCtx)
			if err == nil {
				enabledWhatsApp := make(map[string]controlstore.Connection)
				enabledDiscord := make(map[string]controlstore.Connection)
				enabledTelegram := make(map[string]controlstore.Connection)
				for _, c := range allConns {
					if c.Enabled {
						switch c.Transport {
						case "whatsapp":
							enabledWhatsApp[c.ID] = c
						case "discord":
							enabledDiscord[c.ID] = c
						case "telegram":
							enabledTelegram[c.ID] = c
						}
					}
				}
				for connID := range whatsappAdapters {
					if _, ok := enabledWhatsApp[connID]; !ok {
						_ = connMgr.Stop(connID)
						delete(whatsappAdapters, connID)
					}
				}
				for connID, c := range enabledWhatsApp {
					if _, exists := whatsappAdapters[connID]; !exists {
						connGroupJIDs := make(map[string]string)
						for alias, ep := range updatedCfg.Endpoints {
							if ep.Transport == config.TransportWhatsApp && (ep.ConnectionID == c.ID || (c.ID == "conn-wa-1" && ep.ConnectionID == "")) {
								connGroupJIDs[alias] = ep.RemoteID
							}
						}
						dbPath, err := whatsapp.ProtocolDBPath(dataDir, c.ID)
						if err != nil {
							safelog.Error(logger, "invalid whatsapp db path", "whatsapp_path", err)
							continue
						}
						waInst, err := openWhatsApp(ctx, whatsapp.Options{
							ConnectionID:     c.ID,
							DatabasePath:     dbPath,
							GroupJIDs:        connGroupJIDs,
							Hasher:           hasher,
							UsernameMode:     updatedCfg.Identity.UsernameMode,
							Logger:           logger,
							QROut:            os.Stdout,
							EnableTerminalQR: false,
							MediaEnabled:     updatedCfg.Media.Enabled,
							MediaMaxBytes:    uint64(updatedCfg.Media.MaxSizeMB) * 1024 * 1024,
							RecoveryEnabled:  updatedCfg.Recovery.Enabled,
							RecoveryMaxAge:   time.Duration(updatedCfg.Recovery.MaxAgeHours) * time.Hour,
							RecoveryMaxCount: updatedCfg.Recovery.MaxMessagesPerGroup,
							DeviceName:       updatedCfg.WhatsAppDeviceName,
						})
						if err != nil {
							safelog.Error(logger, "start WhatsApp transport failed", "whatsapp_start", err)
							continue
						}
						whatsappAdapters[c.ID] = waInst
						_ = connMgr.Register(ctx, c.ID, config.TransportWhatsApp, waInst)
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

				for connID := range telegramAdapters {
					if _, ok := enabledTelegram[connID]; !ok {
						_ = connMgr.Stop(connID)
						delete(telegramAdapters, connID)
					}
				}
				for connID, c := range enabledTelegram {
					if _, exists := telegramAdapters[connID]; !exists {
						tokenBytes, err := credentialCipher.Decrypt(c.EncryptedCredential, c.CredentialNonce)
						if err != nil {
							safelog.Error(logger, "decrypt telegram credential failed", "telegram_decrypt", err)
							continue
						}
						connChatIDs := make(map[string]string)
						for alias, ep := range updatedCfg.Endpoints {
							if ep.Transport == config.TransportTelegram && ep.ConnectionID == c.ID {
								connChatIDs[alias] = ep.RemoteID
							}
						}
						initialUpdateID, err := telegramInitialUpdateID(ctx, syncStore, c.ID)
						if err != nil {
							safelog.Error(logger, "load Telegram recovery cursor failed", "telegram_cursor", err)
							continue
						}
						cid := c.ID
						tgInst, err := openTelegram(ctx, telegram.Options{
							ConnectionID:    cid,
							Token:           string(tokenBytes),
							ChatIDs:         connChatIDs,
							Hasher:          hasher,
							UsernameMode:    updatedCfg.Identity.UsernameMode,
							Logger:          logger,
							MediaEnabled:    updatedCfg.Media.Enabled,
							MediaMaxBytes:   uint64(updatedCfg.Media.MaxSizeMB) * 1024 * 1024,
							InitialUpdateID: initialUpdateID,
							MigrateEndpoint: func(migrationCtx context.Context, endpoint transport.EndpointID, oldRemoteID, newRemoteID string) error {
								return config.MigrateTelegramEndpoint(migrationCtx, syncStore.DB(), string(endpoint), cid, oldRemoteID, newRemoteID)
							},
							ResolvePollEndpoint: func(resolveCtx context.Context, pollID string) (transport.EndpointID, bool) {
								endpoint, resolveErr := syncStore.PollEndpointForProviderRef(resolveCtx, "telegram:"+cid, pollID)
								if resolveErr != nil {
									endpoint, resolveErr = syncStore.PollEndpointForProviderRef(resolveCtx, "telegram", pollID)
								}
								if resolveErr != nil {
									return "", false
								}
								return transport.EndpointID(endpoint), true
							},
						})
						if err != nil {
							safelog.Error(logger, "start Telegram transport failed", "telegram_start", err)
							continue
						}
						telegramAdapters[c.ID] = tgInst
						_ = connMgr.Register(ctx, c.ID, config.TransportTelegram, tgInst)
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
		Addr:             apiAddr,
		Logger:           logger,
		DB:               syncStore.DB(),
		ControlDB:        controlStore.DB(),
		Secret:           []byte(secret),
		CredentialCipher: credentialCipher,
		Connections:      &appConnectionService{connMgr: connMgr, dataDir: dataDir},
		WhatsApp:         waService,
		Discord:          dc,
		Telegram:         tg,
		Delivery:         mesh,
		OnConfigChange:   onConfigChange,
		EvidenceDir:      filepath.Join(dataDir, "membership-evidence"),
		Mailer:           verification.NewResendMailerFromEnv(),
		Analyzer:         verification.NewOpenRouterFromEnv(),
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

	runWhatsAppChatCleanup(ctx, logger, syncStore.DB(), whatsappAdapters)

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
			runWhatsAppChatCleanup(ctx, logger, syncStore.DB(), whatsappAdapters)
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

func telegramInitialUpdateID(ctx context.Context, syncStore *store.Store, connectionID string) (int64, error) {
	if syncStore == nil {
		return 0, nil
	}
	streamKey := "telegram"
	if connectionID != "" {
		streamKey = "telegram:" + connectionID
	}
	cursor, err := syncStore.RecoveryCursor(ctx, streamKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return cursor.Position, nil
}

func runWhatsAppChatCleanup(ctx context.Context, logger *slog.Logger, db *sql.DB, wa any) {
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
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)

	adapters := make(map[string]any)
	if m, ok := wa.(map[string]whatsappTransport); ok {
		for k, v := range m {
			adapters[k] = v
		}
	} else if wa != nil {
		adapters["conn-wa-1"] = wa
	}

	syncSetGroupAliases := make(map[string]struct{})
	for _, set := range cfg.SyncSets {
		for _, alias := range set.Endpoints {
			syncSetGroupAliases[alias] = struct{}{}
		}
	}

	for connID, a := range adapters {
		cleaner, ok := a.(interface {
			IsLoggedIn() bool
			ClearSyncSetChats(context.Context, []types.JID, time.Time) (int, error)
		})
		if !ok || cleaner == nil || !cleaner.IsLoggedIn() {
			continue
		}

		connJIDs := make(map[string]types.JID)
		for alias := range syncSetGroupAliases {
			if endpoint, ok := cfg.Endpoints[alias]; ok && endpoint.Transport == config.TransportWhatsApp {
				if endpoint.ConnectionID == connID || (connID == "conn-wa-1" && endpoint.ConnectionID == "") {
					if strings.TrimSpace(endpoint.RemoteID) != "" {
						parsedJID, err := types.ParseJID(endpoint.RemoteID)
						if err == nil {
							connJIDs[parsedJID.String()] = parsedJID
						}
					}
				}
			}
		}

		if len(connJIDs) == 0 {
			continue
		}

		jids := make([]types.JID, 0, len(connJIDs))
		for _, jid := range connJIDs {
			jids = append(jids, jid)
		}

		cleared, err := cleaner.ClearSyncSetChats(ctx, jids, cutoff)
		if err != nil {
			safelog.Error(logger, "whatsapp chat cleanup had errors", "whatsapp_cleanup", err)
		}
		if cleared > 0 {
			logger.Info("whatsapp chat cleanup completed",
				"connection_id", connID,
				"groups_cleared", cleared,
				"retention_days", retentionDays,
			)
		}
	}
}

type appWhatsAppService struct {
	getAdapters  func() map[string]whatsappTransport
	getEndpoints func() map[string]config.Endpoint
}

func (s *appWhatsAppService) primary() api.WhatsAppService {
	if s == nil || s.getAdapters == nil {
		return nil
	}
	adapters := s.getAdapters()
	if a, ok := adapters["conn-wa-1"].(api.WhatsAppService); ok {
		return a
	}
	for _, raw := range adapters {
		if a, ok := raw.(api.WhatsAppService); ok {
			return a
		}
	}
	return nil
}

func (s *appWhatsAppService) Status(ctx context.Context) api.WhatsAppStatus {
	p := s.primary()
	if p == nil {
		return api.WhatsAppStatus{Status: "unpaired"}
	}
	return p.Status(ctx)
}

func (s *appWhatsAppService) Pair(ctx context.Context) (api.WhatsAppPairResponse, error) {
	p := s.primary()
	if p == nil {
		return api.WhatsAppPairResponse{}, errors.New("WhatsApp service unavailable")
	}
	return p.Pair(ctx)
}

func (s *appWhatsAppService) CancelPair(ctx context.Context) error {
	p := s.primary()
	if p == nil {
		return nil
	}
	return p.CancelPair(ctx)
}

func (s *appWhatsAppService) Logout(ctx context.Context) error {
	p := s.primary()
	if p == nil {
		return errors.New("WhatsApp service unavailable")
	}
	return p.Logout(ctx)
}

func (s *appWhatsAppService) GetJoinedGroups(ctx context.Context) ([]api.WhatsAppGroup, error) {
	p := s.primary()
	if p == nil {
		return []api.WhatsAppGroup{}, nil
	}
	return p.GetJoinedGroups(ctx)
}

func (s *appWhatsAppService) findAdmin(alias string) (verification.WhatsAppAdmin, error) {
	if s == nil || s.getAdapters == nil {
		return nil, verification.ErrDestinationMissing
	}
	adapters := s.getAdapters()
	for _, raw := range adapters {
		if hasEp, ok := raw.(interface{ HasEndpoint(string) bool }); ok && hasEp.HasEndpoint(alias) {
			if admin, ok := raw.(verification.WhatsAppAdmin); ok {
				return admin, nil
			}
		}
	}
	if s.getEndpoints != nil {
		if ep, ok := s.getEndpoints()[alias]; ok && ep.Transport == config.TransportWhatsApp {
			connID := ep.ConnectionID
			if connID == "" {
				connID = "conn-wa-1"
			}
			if raw, ok := adapters[connID]; ok {
				if admin, ok := raw.(verification.WhatsAppAdmin); ok {
					return admin, nil
				}
			}
		}
	}
	if p, ok := s.primary().(verification.WhatsAppAdmin); ok {
		return p, nil
	}
	return nil, verification.ErrDestinationMissing
}

func (s *appWhatsAppService) AddParticipant(ctx context.Context, alias, phone string) error {
	admin, err := s.findAdmin(alias)
	if err != nil {
		return err
	}
	return admin.AddParticipant(ctx, alias, phone)
}

func (s *appWhatsAppService) InviteLink(ctx context.Context, alias string) (string, error) {
	admin, err := s.findAdmin(alias)
	if err != nil {
		return "", err
	}
	return admin.InviteLink(ctx, alias)
}

func (s *appWhatsAppService) IsMember(ctx context.Context, alias, phone string) (bool, error) {
	admin, err := s.findAdmin(alias)
	if err != nil {
		return false, err
	}
	return admin.IsMember(ctx, alias, phone)
}

func (s *appWhatsAppService) RotateInviteLink(ctx context.Context, alias string) error {
	admin, err := s.findAdmin(alias)
	if err != nil {
		return err
	}
	return admin.RotateInviteLink(ctx, alias)
}

type appConnectionService struct {
	connMgr *connection.Manager
	dataDir string
}

func (s *appConnectionService) ConnectionStatus(ctx context.Context, id string) (any, error) {
	if s == nil || s.connMgr == nil {
		return map[string]any{"id": id, "status": "stopped"}, nil
	}
	adapter, ok := s.connMgr.GetAdapter(id)
	if !ok {
		return map[string]any{"id": id, "status": "stopped"}, nil
	}
	if wa, ok := adapter.(interface {
		Status(context.Context) api.WhatsAppStatus
	}); ok {
		return wa.Status(ctx), nil
	}
	if dc, ok := adapter.(discord.AdminService); ok {
		return dc.AdminStatus(ctx), nil
	}
	if tg, ok := adapter.(telegram.AdminService); ok {
		return tg.AdminStatus(ctx), nil
	}
	return map[string]any{"id": id, "status": "running"}, nil
}

func (s *appConnectionService) ConnectionDiscovery(ctx context.Context, id string) (any, error) {
	if s == nil || s.connMgr == nil {
		return nil, errors.New("connection manager unavailable")
	}
	adapter, ok := s.connMgr.GetAdapter(id)
	if !ok {
		return nil, errors.New("connection is not running")
	}
	if wa, ok := adapter.(interface {
		GetJoinedGroups(context.Context) ([]api.WhatsAppGroup, error)
	}); ok {
		return wa.GetJoinedGroups(ctx)
	}
	if dc, ok := adapter.(discord.AdminService); ok {
		return dc.DiscoverChannels(ctx)
	}
	if tg, ok := adapter.(telegram.AdminService); ok {
		return tg.DiscoverChats(ctx)
	}
	return nil, errors.New("discovery not supported for this transport")
}

func (s *appConnectionService) WhatsAppPair(ctx context.Context, id string) (api.WhatsAppPairResponse, error) {
	if s == nil || s.connMgr == nil {
		return api.WhatsAppPairResponse{}, errors.New("connection manager unavailable")
	}
	adapter, ok := s.connMgr.GetAdapter(id)
	if !ok {
		return api.WhatsAppPairResponse{}, errors.New("connection is not running")
	}
	if wa, ok := adapter.(interface {
		Pair(context.Context) (api.WhatsAppPairResponse, error)
	}); ok {
		return wa.Pair(ctx)
	}
	return api.WhatsAppPairResponse{}, errors.New("pairing not supported for this connection")
}

func (s *appConnectionService) WhatsAppCancelPair(ctx context.Context, id string) error {
	if s == nil || s.connMgr == nil {
		return nil
	}
	adapter, ok := s.connMgr.GetAdapter(id)
	if !ok {
		return nil
	}
	if wa, ok := adapter.(interface{ CancelPair(context.Context) error }); ok {
		return wa.CancelPair(ctx)
	}
	return nil
}

func (s *appConnectionService) WhatsAppLogout(ctx context.Context, id string) error {
	if s == nil || s.connMgr == nil {
		return nil
	}
	adapter, ok := s.connMgr.GetAdapter(id)
	if !ok {
		return nil
	}
	if wa, ok := adapter.(interface{ Logout(context.Context) error }); ok {
		return wa.Logout(ctx)
	}
	return nil
}

func (s *appConnectionService) StopConnection(id string) error {
	if s != nil && s.connMgr != nil {
		_ = s.connMgr.Stop(id)
	}
	if s != nil && s.dataDir != "" {
		_ = whatsapp.RemoveProtocolDB(s.dataDir, id)
	}
	return nil
}
