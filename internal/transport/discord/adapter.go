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

var ErrOutboundNotImplemented = errors.New("Discord outbound delivery is not implemented")

type Options struct {
	Token           string
	ChannelIDs      map[string]string
	Hasher          *identity.Hasher
	UsernameMode    config.UsernameMode
	Logger          *slog.Logger
	Webhook         ChannelWebhook
}

type Adapter struct {
	session         *discordgo.Session
	normalizer      *Normalizer
	hasher          *identity.Hasher
	webhook         ChannelWebhook
	events          chan transport.Incoming
	logger          *slog.Logger

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

	// Request guild message events only. DMs are intentionally not subscribed,
	// and the normalizer still rejects any DM event defensively.
	session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsMessageContent

	// DiscordGo's default logger may include protocol identifiers and arbitrary
	// server error text. Replace it with a fixed-field classifier and keep only
	// warning/error events from the dependency.
	installSafeDiscordLogger(opts.Logger)
	session.LogLevel = discordgo.LogWarning

	adapter := &Adapter{
		session:         session,
		normalizer:      normalizer,
		hasher:          opts.Hasher,
		webhook:         opts.Webhook,
		events:          make(chan transport.Incoming, eventBufferSize),
		logger:          opts.Logger,
	}
	session.AddHandler(adapter.handleMessageCreate)

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

func (a *Adapter) Send(context.Context, transport.Outgoing) (transport.MessageRef, error) {
	return transport.MessageRef{}, ErrOutboundNotImplemented
}

func (a *Adapter) React(context.Context, transport.Reaction) error {
	return ErrOutboundNotImplemented
}

func (a *Adapter) Edit(context.Context, transport.MessageRef, string) error {
	return ErrOutboundNotImplemented
}

func (a *Adapter) Delete(context.Context, transport.MessageRef) error {
	return ErrOutboundNotImplemented
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

	a.mu.Lock()
	a.normalizer = normalizer
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
