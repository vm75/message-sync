package app

import (
	"context"
	"crypto/sha256"
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
	SyncDBName    = "sync.db"
	ControlDBName = "control.db"
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

func credentialFingerprint(c controlstore.Connection) [32]byte {
	h := sha256.New()
	_, _ = h.Write(c.EncryptedCredential)
	_, _ = h.Write(c.CredentialNonce)
	var fingerprint [32]byte
	copy(fingerprint[:], h.Sum(nil))
	return fingerprint
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

	observeChildScopeLabel := transport.ChildScopeLabelObserver(func(observeCtx context.Context, endpoint transport.EndpointID, scope transport.ChildScope) {
		if strings.TrimSpace(scope.Label) == "" {
			return
		}
		currentCfg, loadErr := config.LoadRaw(observeCtx, syncStore.DB())
		if loadErr != nil {
			safelog.Error(logger, "load child scope display mode failed", "child_scope_mode", loadErr)
			return
		}
		if currentCfg.ChildContextDisplayMode != config.ChildContextDisplayFriendly {
			return
		}
		if upsertErr := syncStore.UpsertChildScopeLabel(observeCtx, string(endpoint), string(scope.Kind), scope.RemoteID, scope.Label); upsertErr != nil {
			safelog.Error(logger, "persist child scope label failed", "child_scope_label", upsertErr)
		}
	})

	telegramChatIDs := make(map[string]string)
	for alias, endpoint := range cfg.Endpoints {
		switch endpoint.Transport {
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
						if ep.Transport == config.TransportWhatsApp && ep.ConnectionID == c.ID {
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

	var credentialCipher *controlstore.CredentialCipher
	if len(secret) >= 32 {
		credentialCipher, _ = controlstore.NewCredentialCipher([]byte(secret))
	}

	discordAdapters := make(map[string]discordTransport)
	credentialFingerprints := make(map[string][32]byte)
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
					credentialFingerprints[c.ID] = credentialFingerprint(c)
					defer dcInst.Close()
				}
			}
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
								return "", false
							}
							return transport.EndpointID(endpoint), true
						},
						ObserveChildScopeLabel: observeChildScopeLabel,
					})
					if err != nil {
						safelog.Error(logger, "start Telegram transport failed", "telegram_start", err)
						continue
					}
					telegramAdapters[c.ID] = tgInst
					credentialFingerprints[c.ID] = credentialFingerprint(c)
					defer tgInst.Close()
				}
			}
		}
	}

	if len(telegramChatIDs) > 0 && len(telegramAdapters) == 0 {
		return errors.New("Telegram bot token is not configured")
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
	}
	registerActiveConnections(cfg)

	onConfigChange := func(updateCtx context.Context) error {
		updatedCfg, err := config.LoadRaw(updateCtx, syncStore.DB())
		if err != nil {
			return fmt.Errorf("reload config: %w", err)
		}
		var reloadErr error
		if controlStore != nil && credentialCipher != nil {
			allConns, err := controlStore.ListConnections(updateCtx)
			if err == nil {
				credentialChanged := make(map[string]bool)
				for _, c := range allConns {
					if c.Enabled && (c.Transport == "discord" || c.Transport == "telegram") {
						previous, known := credentialFingerprints[c.ID]
						credentialChanged[c.ID] = known && previous != credentialFingerprint(c)
					}
				}
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
							if ep.Transport == config.TransportWhatsApp && ep.ConnectionID == c.ID {
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
					_, exists := discordAdapters[connID]
					if !exists || credentialChanged[connID] {
						tokenBytes, err := credentialCipher.Decrypt(c.EncryptedCredential, c.CredentialNonce)
						if err != nil {
							safelog.Error(logger, "decrypt discord credential failed", "discord_decrypt", err)
							if exists && credentialChanged[connID] {
								reloadErr = errors.Join(reloadErr, errors.New("decrypt Discord credential failed"))
							}
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
							if exists && credentialChanged[connID] {
								reloadErr = errors.Join(reloadErr, errors.New("start Discord transport failed"))
							}
							continue
						}
						var runtimeErr error
						if exists {
							runtimeErr = connMgr.Restart(ctx, c.ID, dcInst)
						} else {
							runtimeErr = connMgr.Register(ctx, c.ID, config.TransportDiscord, dcInst)
						}
						if runtimeErr != nil {
							safelog.Error(logger, "replace Discord transport failed", "discord_replace", runtimeErr)
							_ = dcInst.Close()
							reloadErr = errors.Join(reloadErr, errors.New("replace Discord transport failed"))
							continue
						}
						discordAdapters[c.ID] = dcInst
						credentialFingerprints[c.ID] = credentialFingerprint(c)
					}
				}

				for connID := range telegramAdapters {
					if _, ok := enabledTelegram[connID]; !ok {
						_ = connMgr.Stop(connID)
						delete(telegramAdapters, connID)
					}
				}
				for connID, c := range enabledTelegram {
					_, exists := telegramAdapters[connID]
					if !exists || credentialChanged[connID] {
						tokenBytes, err := credentialCipher.Decrypt(c.EncryptedCredential, c.CredentialNonce)
						if err != nil {
							safelog.Error(logger, "decrypt telegram credential failed", "telegram_decrypt", err)
							if exists && credentialChanged[connID] {
								reloadErr = errors.Join(reloadErr, errors.New("decrypt Telegram credential failed"))
							}
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
									return "", false
								}
								return transport.EndpointID(endpoint), true
							},
							ObserveChildScopeLabel: observeChildScopeLabel,
						})
						if err != nil {
							safelog.Error(logger, "start Telegram transport failed", "telegram_start", err)
							if exists && credentialChanged[connID] {
								reloadErr = errors.Join(reloadErr, errors.New("start Telegram transport failed"))
							}
							continue
						}
						var runtimeErr error
						if exists {
							runtimeErr = connMgr.Restart(ctx, c.ID, tgInst)
						} else {
							runtimeErr = connMgr.Register(ctx, c.ID, config.TransportTelegram, tgInst)
						}
						if runtimeErr != nil {
							safelog.Error(logger, "replace Telegram transport failed", "telegram_replace", runtimeErr)
							_ = tgInst.Close()
							reloadErr = errors.Join(reloadErr, errors.New("replace Telegram transport failed"))
							continue
						}
						telegramAdapters[c.ID] = tgInst
						credentialFingerprints[c.ID] = credentialFingerprint(c)
					}
				}
			}
		}
		if reloadErr != nil {
			return fmt.Errorf("reload transport connections: %w", reloadErr)
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
			if incoming.ChildScope != nil {
				observeChildScopeLabel(ctx, incoming.Endpoint, *incoming.ChildScope)
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
	streamKey := "telegram:" + connectionID
	cursor, err := syncStore.RecoveryCursor(ctx, streamKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return cursor.Position, nil
}

func runWhatsAppChatCleanup(ctx context.Context, logger *slog.Logger, db *sql.DB, wa map[string]whatsappTransport) {
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

	adapters := make(map[string]any, len(wa))
	for k, v := range wa {
		adapters[k] = v
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
				if endpoint.ConnectionID == connID {
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

func (s *appConnectionService) ConnectionAdapter(id string) (any, bool) {
	if s == nil || s.connMgr == nil {
		return nil, false
	}
	return s.connMgr.GetAdapter(id)
}
