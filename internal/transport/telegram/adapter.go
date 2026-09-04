package telegram

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
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
	ConnectionID           string
	Token                  string
	ChatIDs                map[string]string
	Hasher                 *identity.Hasher
	UsernameMode           config.UsernameMode
	Logger                 *slog.Logger
	MediaEnabled           bool
	MediaMaxBytes          uint64
	InitialUpdateID        int64
	clientFactory          botClientFactory
	httpClient             *http.Client
	retryWait              func(context.Context, time.Duration) error
	MigrateEndpoint        func(context.Context, transport.EndpointID, string, string) error
	ResolvePollEndpoint    func(context.Context, string) (transport.EndpointID, bool)
	ObserveChildScopeLabel transport.ChildScopeLabelObserver
}

type Adapter struct {
	connectionID string
	client       botClient
	normalizer   *Normalizer
	hasher       *identity.Hasher
	events       chan transport.Incoming
	logger       *slog.Logger
	botUserID    int64
	token        string
	httpClient   *http.Client

	mediaEnabled           bool
	mediaMaxBytes          uint64
	retryWait              func(context.Context, time.Duration) error
	migrateEndpoint        func(context.Context, transport.EndpointID, string, string) error
	resolvePollEndpoint    func(context.Context, string) (transport.EndpointID, bool)
	observeChildScopeLabel transport.ChildScopeLabelObserver
	messageKinds           map[messageKindKey]string

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

	token := strings.TrimSpace(opts.Token)
	if token == "" {
		return nil, errors.New("Telegram bot token is required")
	}
	if err := config.ValidateConnectionID(opts.ConnectionID); err != nil {
		return nil, err
	}
	normalizer, err := NewNormalizerWithConnection(opts.ChatIDs, opts.Hasher, opts.UsernameMode, opts.ConnectionID)
	if err != nil {
		return nil, err
	}

	pollCtx, pollCancel := context.WithCancel(ctx)
	httpClient := opts.httpClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	adapter := &Adapter{
		connectionID:           strings.TrimSpace(opts.ConnectionID),
		normalizer:             normalizer,
		hasher:                 opts.Hasher,
		events:                 make(chan transport.Incoming, eventBufferSize),
		logger:                 opts.Logger,
		token:                  token,
		httpClient:             httpClient,
		mediaEnabled:           opts.MediaEnabled,
		mediaMaxBytes:          opts.MediaMaxBytes,
		retryWait:              opts.retryWait,
		migrateEndpoint:        opts.MigrateEndpoint,
		resolvePollEndpoint:    opts.ResolvePollEndpoint,
		observeChildScopeLabel: opts.ObserveChildScopeLabel,
		messageKinds:           make(map[messageKindKey]string),
		observed:               make(map[int64]observedChatEntry),
		polling:                true,
		pollCancel:             pollCancel,
	}

	factory := opts.clientFactory
	var client botClient
	if factory == nil {
		client, err = newBotClient(token, adapter.handleUpdate, safeTelegramErrorsHandler(opts.Logger), opts.InitialUpdateID, httpClient)
	} else {
		client, err = factory(token, adapter.handleUpdate, safeTelegramErrorsHandler(opts.Logger))
	}
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

func newBotClient(token string, handler telegrambot.HandlerFunc, errorsHandler telegrambot.ErrorsHandler, initialUpdateID int64, httpClient *http.Client) (botClient, error) {
	options := []telegrambot.Option{
		telegrambot.WithDefaultHandler(handler),
		telegrambot.WithErrorsHandler(errorsHandler),
		telegrambot.WithAllowedUpdates(telegrambot.AllowedUpdates{
			models.AllowedUpdateMessage,
			models.AllowedUpdateEditedMessage,
			models.AllowedUpdateMessageReaction,
			models.AllowedUpdatePoll,
		}),
		telegrambot.WithNotAsyncHandlers(),
	}
	if initialUpdateID > 0 {
		options = append(options, telegrambot.WithInitialOffset(initialUpdateID))
	}
	if httpClient != nil {
		options = append(options, telegrambot.WithHTTPClient(time.Minute, httpClient))
	}
	return telegrambot.New(token, options...)
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

func (a *Adapter) ConnectionID() string {
	if a == nil {
		return ""
	}
	return a.connectionID
}

func (a *Adapter) checkpointStreamKey() string {
	if a == nil {
		return "telegram:"
	}
	return "telegram:" + a.connectionID
}

func (a *Adapter) pollProviderNamespace() string {
	if a == nil {
		return "telegram:"
	}
	return "telegram:" + a.connectionID
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
			if endpoint.ConnectionID != a.connectionID {
				continue
			}
			chatIDs[alias] = endpoint.RemoteID
		}
	}
	normalizer, err := NewNormalizerWithConnection(chatIDs, a.hasher, cfg.Identity.UsernameMode, a.connectionID)
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

func (a *Adapter) handleUpdate(ctx context.Context, _ *telegrambot.Bot, update *models.Update) {
	if a == nil || update == nil || !a.acceptUpdateID(update.ID) {
		return
	}
	a.observeUpdate(update)
	checkpoint := transport.Checkpoint{StreamKey: a.checkpointStreamKey(), Position: update.ID, EventTimestamp: telegramUpdateTimestamp(update), Valid: update.ID > 0}

	a.mu.RLock()
	normalizer := a.normalizer
	botUserID := a.botUserID
	a.mu.RUnlock()
	if normalizer == nil {
		a.emit(transport.Incoming{Kind: "other", Checkpoint: checkpoint})
		return
	}

	var (
		incoming transport.Incoming
		ok       bool
	)
	switch {
	case update.Message != nil:
		a.observeForumTopicLabel(ctx, normalizer, update.Message)
		if a.handleTelegramMigration(ctx, normalizer, update.Message) {
			a.emit(transport.Incoming{Kind: "other", Checkpoint: checkpoint})
			return
		}
		incoming, ok = normalizer.NormalizeMessage(update.Message, botUserID)
		if ok {
			incoming, ok = a.withTelegramMedia(incoming, update.Message)
		} else {
			a.logIgnoredTelegramMessage(normalizer, update.Message, botUserID)
		}
	case update.EditedMessage != nil:
		a.observeForumTopicLabel(ctx, normalizer, update.EditedMessage)
		incoming, ok = normalizer.NormalizeEditedMessage(update.EditedMessage, botUserID)
	case update.MessageReaction != nil:
		incoming, ok = normalizer.NormalizeReaction(update.MessageReaction, botUserID)
		if a.logger != nil {
			reason := "accepted"
			if !ok {
				reason = "normalization_rejected"
			}
			a.logger.Info("Telegram reaction update received", "event", "telegram_reaction_update", "result", reason)
		}
	case update.Poll != nil:
		if a.resolvePollEndpoint == nil {
			return
		}
		endpoint, resolved := a.resolvePollEndpoint(context.Background(), update.Poll.ID)
		if !resolved {
			return
		}
		counts := make(map[int]int, len(update.Poll.Options))
		for index, option := range update.Poll.Options {
			counts[index] = option.VoterCount
		}
		incoming = transport.Incoming{Endpoint: endpoint, RemoteID: update.Poll.ID, Kind: "poll_snapshot", PollSnapshot: counts, PollProvider: a.pollProviderNamespace(), PollProviderReference: update.Poll.ID, Timestamp: time.Now().UTC()}
		ok = true
	default:
		a.emit(transport.Incoming{Kind: "other", Checkpoint: checkpoint})
		return
	}
	if !ok {
		a.emit(transport.Incoming{Kind: "other", Checkpoint: checkpoint})
		return
	}
	incoming.Checkpoint = checkpoint
	if !incoming.Timestamp.IsZero() {
		incoming.Checkpoint.EventTimestamp = incoming.Timestamp
	}
	a.emit(incoming)
}

func (a *Adapter) observeForumTopicLabel(ctx context.Context, normalizer *Normalizer, message *models.Message) {
	if a == nil || normalizer == nil || message == nil || message.Chat.Type != "supergroup" || message.MessageThreadID <= 0 || a.observeChildScopeLabel == nil {
		return
	}
	var label string
	switch {
	case message.ForumTopicCreated != nil:
		label = message.ForumTopicCreated.Name
	case message.ForumTopicEdited != nil:
		label = message.ForumTopicEdited.Name
	default:
		return
	}
	if strings.TrimSpace(label) == "" {
		return
	}
	endpoint, ok := normalizer.endpoint(message.Chat.ID)
	if !ok {
		return
	}
	a.observeChildScopeLabel(ctx, endpoint, transport.ChildScope{
		Kind: transport.ScopeKindTelegramTopic, RemoteID: strconv.Itoa(message.MessageThreadID), Label: label,
	})
}

func telegramUpdateTimestamp(update *models.Update) time.Time {
	if update == nil {
		return time.Time{}
	}
	var date int
	switch {
	case update.Message != nil:
		date = update.Message.Date
	case update.EditedMessage != nil:
		date = update.EditedMessage.Date
	}
	if date == 0 {
		return time.Time{}
	}
	return time.Unix(int64(date), 0).UTC()
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
