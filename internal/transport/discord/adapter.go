package discord

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/safelog"
	"github.com/vm75/message-sync/internal/transport"
)

const eventBufferSize = 128

type Options struct {
	Token         string
	ChannelIDs    map[string]string
	Hasher        *identity.Hasher
	UsernameMode  config.UsernameMode
	Logger        *slog.Logger
	Webhook       ChannelWebhook
	MediaEnabled  bool
	MediaMaxBytes uint64
}

type Adapter struct {
	session    *discordgo.Session
	api        discordAPI
	normalizer *Normalizer
	hasher     *identity.Hasher
	webhook    ChannelWebhook
	targets    map[transport.EndpointID]string
	events     chan transport.Incoming
	logger     *slog.Logger

	mediaEnabled      bool
	mediaMaxBytes     uint64
	reactionState     map[reactionKey]string
	suppressedDeletes map[string]struct{}

	mu        sync.RWMutex
	closeOnce sync.Once
	closeErr  error
}

var _ transport.Adapter = (*Adapter)(nil)

func Open(ctx context.Context, opts Options) (*Adapter, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	if opts.Logger == nil {
		return nil, errors.New("logger is required")
	}
	token := strings.TrimSpace(opts.Token)
	if token == "" {
		return nil, errors.New("Discord bot token is required")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	normalizer, err := NewNormalizer(opts.ChannelIDs, opts.Hasher, opts.UsernameMode)
	if err != nil {
		return nil, err
	}
	session, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, errors.New("create Discord gateway client")
	}
	session.ShouldRetryOnRateLimit = true

	// Request guild message events only. DMs are intentionally not subscribed,
	// and the normalizer still rejects any DM event defensively.
	session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsMessageContent

	// DiscordGo's default logger may include protocol identifiers and arbitrary
	// server error text. Replace it with a fixed-field classifier and keep only
	// warning/error events from the dependency.
	installSafeDiscordLogger(opts.Logger)
	session.LogLevel = discordgo.LogWarning

	targets := discordTargets(opts.ChannelIDs)
	webhook := opts.Webhook
	if webhook == nil {
		webhook = newManagedWebhookClient(session)
	}
	adapter := &Adapter{
		session:            session,
		api:                session,
		normalizer:         normalizer,
		hasher:             opts.Hasher,
		webhook:            webhook,
		targets:            targets,
		events:             make(chan transport.Incoming, eventBufferSize),
		logger:             opts.Logger,
		mediaEnabled:       opts.MediaEnabled,
		mediaMaxBytes:      opts.MediaMaxBytes,
		reactionState:      make(map[reactionKey]string),
		suppressedDeletes:  make(map[string]struct{}),
	}
	session.AddHandler(adapter.handleMessageCreate)
	session.AddHandler(adapter.handleMessageUpdate)
	session.AddHandler(adapter.handleMessageDelete)
	session.AddHandler(adapter.handleMessageReactionAdd)
	session.AddHandler(adapter.handleMessageReactionRemove)

	if err := session.Open(); err != nil {
		safelog.Error(opts.Logger, "Discord gateway connection failed", "discord_connect", err)
		_ = session.Close()
		return nil, errors.New("connect Discord gateway")
	}
	select {
	case <-ctx.Done():
		_ = adapter.Close()
		return nil, ctx.Err()
	default:
	}

	if preparer, ok := webhook.(webhookPreparer); ok {
		if err := preparer.Prepare(ctx, discordChannelIDs(targets)); err != nil {
			opts.Logger.Error("Discord webhook preparation failed",
				"event", "discord_webhook_prepare_failed",
				"error_kind", "webhook",
			)
			_ = adapter.Close()
			return nil, errors.New("prepare Discord managed webhooks")
		}
	}

	opts.Logger.Info("Discord transport connected",
		"event", "discord_connected",
		"endpoints", len(opts.ChannelIDs),
	)
	return adapter, nil
}

func installSafeDiscordLogger(logger *slog.Logger) {
	discordgo.Logger = func(level, _ int, _ string, _ ...interface{}) {
		if logger == nil {
			return
		}
		switch level {
		case discordgo.LogError:
			logger.Error("Discord client error",
				"event", "discord_client_error",
				"error_kind", "client",
			)
		case discordgo.LogWarning:
			logger.Warn("Discord client warning",
				"event", "discord_client_warning",
				"error_kind", "client",
			)
		}
	}
}

func (a *Adapter) Name() string {
	return "discord"
}

func (a *Adapter) Events() <-chan transport.Incoming {
	if a == nil {
		return nil
	}
	return a.events
}

func (a *Adapter) Close() error {
	if a == nil {
		return nil
	}
	a.closeOnce.Do(func() {
		if a.session == nil {
			return
		}
		if err := a.session.Close(); err != nil {
			safelog.Error(a.logger, "Discord gateway close failed", "discord_close", err)
			a.closeErr = errors.New("close Discord gateway")
			return
		}
		if a.logger != nil {
			a.logger.Info("Discord transport disconnected", "event", "discord_disconnected")
		}
	})
	return a.closeErr
}

func (a *Adapter) UpdateConfig(cfg *config.Config) error {
	if a == nil {
		return errors.New("Discord transport is not initialized")
	}
	if cfg == nil {
		return errors.New("config is required")
	}

	channelIDs := make(map[string]string)
	for alias, endpoint := range cfg.Endpoints {
		if endpoint.Transport == config.TransportDiscord {
			channelIDs[alias] = endpoint.RemoteID
		}
	}
	normalizer, err := NewNormalizer(channelIDs, a.hasher, cfg.Identity.UsernameMode)
	if err != nil {
		return err
	}
	targets := discordTargets(channelIDs)
	a.mu.RLock()
	webhook := a.webhook
	a.mu.RUnlock()
	if preparer, ok := webhook.(webhookPreparer); ok {
		if err := preparer.Prepare(context.Background(), discordChannelIDs(targets)); err != nil {
			return errors.New("prepare Discord managed webhooks")
		}
	}
	maxBytes := uint64(0)
	if cfg.Media.MaxSizeMB > 0 {
		maxBytes = uint64(cfg.Media.MaxSizeMB) * 1024 * 1024
	}

	a.mu.Lock()
	a.normalizer = normalizer
	a.targets = targets
	a.mediaEnabled = cfg.Media.Enabled
	a.mediaMaxBytes = maxBytes
	a.mu.Unlock()
	return nil
}

func (a *Adapter) handleMessageCreate(session *discordgo.Session, evt *discordgo.MessageCreate) {
	if a == nil {
		return
	}
	a.mu.RLock()
	normalizer := a.normalizer
	webhooks := a.webhook
	a.mu.RUnlock()
	if normalizer == nil {
		return
	}

	botUserID := ""
	if session != nil && session.State != nil && session.State.User != nil {
		botUserID = session.State.User.ID
	}
	incoming, ok := normalizer.NormalizeMessage(evt, botUserID, webhooks)
	if !ok {
		return
	}
	incoming, ok = a.withDiscordMedia(incoming, evt.Message)
	if !ok {
		return
	}
	a.emit(incoming)
}

func (a *Adapter) emit(incoming transport.Incoming) {
	if a == nil || a.events == nil {
		return
	}
	select {
	case a.events <- incoming:
	default:
		if a.logger != nil {
			a.logger.Warn("Discord ingress dropped",
				"event", "discord_ingress_dropped",
				"endpoint", string(incoming.Endpoint),
				"kind", incoming.Kind,
				"reason", "buffer_full",
			)
		}
	}
}

// LoadBotToken reads the Discord bot credential only from environment or a
// mounted secret file. The token is never returned through config APIs or
// written to application persistence.
func LoadBotToken() (string, error) {
	token := strings.TrimSpace(os.Getenv("DISCORD_BOT_TOKEN"))
	secretFile := strings.TrimSpace(os.Getenv("DISCORD_BOT_TOKEN_FILE"))
	if token != "" && secretFile != "" {
		return "", errors.New("configure only one Discord bot token source")
	}
	if token != "" {
		return token, nil
	}
	if secretFile == "" {
		return "", errors.New("Discord bot token is not configured")
	}

	data, err := os.ReadFile(secretFile)
	if err != nil {
		return "", errors.New("read Discord bot token secret")
	}
	token = strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("Discord bot token secret is empty")
	}
	return token, nil
}

func discordTargets(channelIDs map[string]string) map[transport.EndpointID]string {
	targets := make(map[transport.EndpointID]string, len(channelIDs))
	for alias, channelID := range channelIDs {
		targets[transport.EndpointID(alias)] = strings.TrimSpace(channelID)
	}
	return targets
}

func discordChannelIDs(targets map[transport.EndpointID]string) []string {
	channelIDs := make([]string, 0, len(targets))
	for _, channelID := range targets {
		channelIDs = append(channelIDs, channelID)
	}
	return channelIDs
}
