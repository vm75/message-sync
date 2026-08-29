package telegram

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	telegrambot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/safelog"
	"github.com/vm75/message-sync/internal/transport"
)

const eventBufferSize = 128

type botClient interface {
	Start(context.Context)
	ID() int64
	GetMe(context.Context) (*models.User, error)
}

type botClientFactory func(string, telegrambot.HandlerFunc, telegrambot.ErrorsHandler) (botClient, error)

type Options struct {
	ChatIDs       map[string]string
	Hasher        *identity.Hasher
	UsernameMode  config.UsernameMode
	Logger        *slog.Logger
	MediaEnabled  bool
	MediaMaxBytes uint64
	clientFactory botClientFactory
	httpClient    *http.Client
	retryWait     func(context.Context, time.Duration) error
}

type Adapter struct {
	client     botClient
	normalizer *Normalizer
	hasher     *identity.Hasher
	events     chan transport.Incoming
	logger     *slog.Logger
	botUserID  int64
	token      string
	httpClient *http.Client

	mediaEnabled  bool
	mediaMaxBytes uint64
	retryWait     func(context.Context, time.Duration) error
	messageKinds  map[messageKindKey]string

	mu                 sync.RWMutex
	lastUpdateID       int64
	haveUpdateID       bool
	observed           map[int64]observedChatEntry
	observeSeq         uint64
	polling            bool
	privacyModeKnown   bool
	privacyModeEnabled bool

	pollCancel context.CancelFunc
	pollWG     sync.WaitGroup
	closeOnce  sync.Once
}

var _ transport.Adapter = (*Adapter)(nil)

func Open(ctx context.Context, opts Options) (*Adapter, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	if opts.Logger == nil {
		return nil, errors.New("logger is required")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	token, err := LoadBotToken()
	if err != nil {
		return nil, err
	}
	normalizer, err := NewNormalizer(opts.ChatIDs, opts.Hasher, opts.UsernameMode)
	if err != nil {
		return nil, err
	}

	pollCtx, pollCancel := context.WithCancel(ctx)
	httpClient := opts.httpClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	adapter := &Adapter{
		normalizer:    normalizer,
		hasher:        opts.Hasher,
		events:        make(chan transport.Incoming, eventBufferSize),
		logger:        opts.Logger,
		token:         token,
		httpClient:    httpClient,
		mediaEnabled:  opts.MediaEnabled,
		mediaMaxBytes: opts.MediaMaxBytes,
		retryWait:     opts.retryWait,
		messageKinds:  make(map[messageKindKey]string),
		observed:      make(map[int64]observedChatEntry),
		polling:       true,
		pollCancel:    pollCancel,
	}

	factory := opts.clientFactory
	if factory == nil {
		factory = newBotClient
	}
	client, err := factory(token, adapter.handleUpdate, safeTelegramErrorsHandler(opts.Logger))
	if err != nil {
		pollCancel()
		safelog.Error(opts.Logger, "Telegram Bot API initialization failed", "telegram_init", err)
		return nil, errors.New("initialize Telegram Bot API client")
	}
	if client == nil || client.ID() == 0 {
		pollCancel()
		return nil, errors.New("Telegram Bot API client has invalid bot identity")
	}
	adapter.client = client
	adapter.botUserID = client.ID()

	adapter.pollWG.Add(1)
	go func() {
		defer adapter.pollWG.Done()
		opts.Logger.Info("Telegram long polling started", "event", "telegram_poll_started")
		client.Start(pollCtx)
		adapter.mu.Lock()
		adapter.polling = false
		adapter.mu.Unlock()
		opts.Logger.Info("Telegram long polling stopped", "event", "telegram_poll_stopped")
	}()

	return adapter, nil
}

func newBotClient(token string, handler telegrambot.HandlerFunc, errorsHandler telegrambot.ErrorsHandler) (botClient, error) {
	return telegrambot.New(token,
		telegrambot.WithDefaultHandler(handler),
		telegrambot.WithErrorsHandler(errorsHandler),
		telegrambot.WithAllowedUpdates(telegrambot.AllowedUpdates{
			models.AllowedUpdateMessage,
			models.AllowedUpdateEditedMessage,
			models.AllowedUpdateMessageReaction,
		}),
		telegrambot.WithNotAsyncHandlers(),
	)
}

func safeTelegramErrorsHandler(logger *slog.Logger) telegrambot.ErrorsHandler {
	return func(err error) {
		if logger == nil {
			return
		}
		safelog.Error(logger, "Telegram Bot API client error", "telegram_client", err)
	}
}

func (a *Adapter) Name() string {
	return "telegram"
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
		if a.pollCancel != nil {
			a.pollCancel()
		}
		a.pollWG.Wait()
	})
	return nil
}

func (a *Adapter) UpdateConfig(cfg *config.Config) error {
	if a == nil {
		return errors.New("Telegram transport is not initialized")
	}
	if cfg == nil {
		return errors.New("config is required")
	}

	chatIDs := make(map[string]string)
	for alias, endpoint := range cfg.Endpoints {
		if endpoint.Transport == config.TransportTelegram {
			chatIDs[alias] = endpoint.RemoteID
		}
	}
	normalizer, err := NewNormalizer(chatIDs, a.hasher, cfg.Identity.UsernameMode)
	if err != nil {
		return err
	}

	a.mu.Lock()
	a.normalizer = normalizer
	a.mediaEnabled = cfg.Media.Enabled
	a.mediaMaxBytes = uint64(cfg.Media.MaxSizeMB) * 1024 * 1024
	a.mu.Unlock()
	return nil
}

func (a *Adapter) handleUpdate(_ context.Context, _ *telegrambot.Bot, update *models.Update) {
	if a == nil || update == nil || !a.acceptUpdateID(update.ID) {
		return
	}
	a.observeUpdate(update)

	a.mu.RLock()
	normalizer := a.normalizer
	botUserID := a.botUserID
	a.mu.RUnlock()
	if normalizer == nil {
		return
	}

	var (
		incoming transport.Incoming
		ok       bool
	)
	switch {
	case update.Message != nil:
		incoming, ok = normalizer.NormalizeMessage(update.Message, botUserID)
		if ok {
			incoming, ok = a.withTelegramMedia(incoming, update.Message)
		}
	case update.EditedMessage != nil:
		incoming, ok = normalizer.NormalizeEditedMessage(update.EditedMessage, botUserID)
	case update.MessageReaction != nil:
		incoming, ok = normalizer.NormalizeReaction(update.MessageReaction, botUserID)
	default:
		return
	}
	if !ok {
		return
	}
	a.emit(incoming)
}

func (a *Adapter) acceptUpdateID(updateID int64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.haveUpdateID && updateID <= a.lastUpdateID {
		return false
	}
	a.lastUpdateID = updateID
	a.haveUpdateID = true
	return true
}

func (a *Adapter) emit(incoming transport.Incoming) {
	if a == nil || a.events == nil {
		return
	}
	select {
	case a.events <- incoming:
	default:
		if a.logger != nil {
			a.logger.Warn("Telegram ingress dropped",
				"event", "telegram_ingress_dropped",
				"endpoint", string(incoming.Endpoint),
				"kind", incoming.Kind,
				"reason", "buffer_full",
			)
		}
	}
}

// BotTokenConfigured reports whether a supported deployment credential source
// is present. LoadBotToken rejects ambiguous configuration when both sources
// are set. The token itself never enters application configuration or persistence.
func BotTokenConfigured() bool {
	return strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")) != "" ||
		strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN_FILE")) != ""
}

func LoadBotToken() (string, error) {
	token := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN"))
	secretFile := strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN_FILE"))
	if token != "" && secretFile != "" {
		return "", errors.New("configure only one Telegram bot token source")
	}
	if token != "" {
		return token, nil
	}
	if secretFile == "" {
		return "", errors.New("Telegram bot token is not configured")
	}

	data, err := os.ReadFile(secretFile)
	if err != nil {
		return "", errors.New("read Telegram bot token secret")
	}
	token = strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("Telegram bot token secret is empty")
	}
	return token, nil
}
