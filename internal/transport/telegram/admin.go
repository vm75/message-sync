package telegram

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	telegrambot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/vm75/message-sync/internal/safelog"
)

const observedChatLimit = 128

const VisibilityGuidance = "Send a group message so the chat can be observed. If ordinary group messages are not visible, disable Bot Privacy Mode or grant appropriate bot administrator visibility."

var (
	ErrTargetValidationUnavailable = errors.New("Telegram target validation is unavailable")
	ErrUnsupportedTarget           = errors.New("Telegram target is not a supported group")
)

type EndpointReadiness struct {
	Alias  string `json:"alias"`
	Status string `json:"status"`
}

type AdminStatus struct {
	TokenConfigured    bool                `json:"tokenConfigured"`
	IntegrationMode    string              `json:"integrationMode,omitempty"`
	Configured         bool                `json:"configured,omitempty"`
	Running            bool                `json:"running"`
	Status             string              `json:"status"`
	Endpoints          []EndpointReadiness `json:"endpoints"`
	PrivacyModeKnown   bool                `json:"privacyModeKnown"`
	PrivacyModeEnabled *bool               `json:"privacyModeEnabled,omitempty"`
	VisibilityGuidance string              `json:"visibilityGuidance"`
	Capabilities       Capabilities        `json:"capabilities"`
}

type DiscoveredChat struct {
	ChatID   string `json:"chatId"`
	Title    string `json:"title,omitempty"`
	Username string `json:"username,omitempty"`
	Type     string `json:"type"`
	Forum    bool   `json:"forum,omitempty"`
}

type DiscoveredTopic struct {
	RemoteID string `json:"remoteId"`
	Label    string `json:"label,omitempty"`
	General  bool   `json:"general,omitempty"`
}

type TopicDiscoveryService interface {
	DiscoverTopics(context.Context, string) ([]DiscoveredTopic, error)
}

type AdminService interface {
	AdminStatus(context.Context) AdminStatus
	DiscoverChats(context.Context) ([]DiscoveredChat, error)
	ValidateTarget(context.Context, string) error
}

type observedChatEntry struct {
	chat DiscoveredChat
	seen uint64
}

func (a *Adapter) AdminStatus(ctx context.Context) AdminStatus {
	status := AdminStatus{
		IntegrationMode:    "bot",
		TokenConfigured:    false,
		Status:             "not_configured",
		Endpoints:          []EndpointReadiness{},
		PrivacyModeKnown:   false,
		VisibilityGuidance: VisibilityGuidance,
		Capabilities:       CapabilitiesForIntegrationMode("bot"),
	}
	if a == nil {
		return status
	}

	a.refreshPrivacyMode(ctx)

	a.mu.RLock()
	status.TokenConfigured = true
	status.Running = a.polling
	status.PrivacyModeKnown = a.privacyModeKnown
	if a.privacyModeKnown {
		enabled := a.privacyModeEnabled
		status.PrivacyModeEnabled = &enabled
	}
	aliases := make([]string, 0)
	if a.normalizer != nil {
		aliases = make([]string, 0, len(a.normalizer.endpoints))
		for _, alias := range a.normalizer.endpoints {
			aliases = append(aliases, string(alias))
		}
	}
	a.mu.RUnlock()

	if status.Running {
		status.Status = "running"
	} else {
		status.Status = "stopped"
	}
	sort.Strings(aliases)
	readiness := "unavailable"
	if status.Running {
		readiness = "ready"
	}
	for _, alias := range aliases {
		status.Endpoints = append(status.Endpoints, EndpointReadiness{Alias: alias, Status: readiness})
	}
	return status
}

func (a *Adapter) refreshPrivacyMode(ctx context.Context) {
	if a == nil {
		return
	}
	a.mu.RLock()
	client := a.client
	logger := a.logger
	a.mu.RUnlock()
	if client == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	botInfo, err := client.GetMe(probeCtx)
	cancel()
	if err != nil {
		a.mu.Lock()
		a.privacyModeKnown = false
		a.mu.Unlock()
		safelog.Error(logger, "Telegram Bot API status probe failed", "telegram_status_probe", err)
		return
	}
	if botInfo == nil {
		a.mu.Lock()
		a.privacyModeKnown = false
		a.mu.Unlock()
		return
	}

	a.mu.Lock()
	a.privacyModeKnown = true
	a.privacyModeEnabled = !botInfo.CanReadAllGroupMessages
	a.mu.Unlock()
}

func (a *Adapter) DiscoverChats(_ context.Context) ([]DiscoveredChat, error) {
	if a == nil {
		return nil, errors.New("Telegram transport is not initialized")
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.polling {
		return nil, errors.New("Telegram long polling is not running")
	}
	chats := make([]DiscoveredChat, 0, len(a.observed))
	for _, entry := range a.observed {
		chats = append(chats, entry.chat)
	}
	sort.Slice(chats, func(i, j int) bool {
		return chats[i].ChatID < chats[j].ChatID
	})
	return chats, nil
}

func (a *Adapter) ValidateTarget(ctx context.Context, remoteID string) error {
	if a == nil {
		return ErrTargetValidationUnavailable
	}
	remoteID = strings.TrimSpace(remoteID)
	if _, err := strconv.ParseInt(remoteID, 10, 64); err != nil {
		return ErrUnsupportedTarget
	}
	a.mu.RLock()
	client := a.client
	running := a.polling
	a.mu.RUnlock()
	if client == nil || !running {
		return ErrTargetValidationUnavailable
	}
	getter, ok := client.(interface {
		GetChat(context.Context, *telegrambot.GetChatParams) (*models.ChatFullInfo, error)
	})
	if !ok {
		return ErrTargetValidationUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	chat, err := getter.GetChat(probeCtx, &telegrambot.GetChatParams{ChatID: remoteID})
	if err != nil || chat == nil {
		return ErrTargetValidationUnavailable
	}
	if chat.Type != models.ChatTypeGroup && chat.Type != models.ChatTypeSupergroup {
		return ErrUnsupportedTarget
	}
	return nil
}

func (a *Adapter) observeUpdate(update *models.Update) {
	if a == nil || update == nil {
		return
	}
	switch {
	case update.Message != nil:
		a.observeChat(update.Message.Chat)
	case update.EditedMessage != nil:
		a.observeChat(update.EditedMessage.Chat)
	case update.MessageReaction != nil:
		a.observeChat(update.MessageReaction.Chat)
	}
}

func (a *Adapter) observeChat(chat models.Chat) {
	if a == nil || chat.ID == 0 {
		return
	}
	if chat.Type != models.ChatTypeGroup && chat.Type != models.ChatTypeSupergroup {
		return
	}
	entry := DiscoveredChat{
		ChatID:   strconv.FormatInt(chat.ID, 10),
		Title:    strings.TrimSpace(chat.Title),
		Username: strings.TrimSpace(chat.Username),
		Type:     string(chat.Type),
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.observed == nil {
		a.observed = make(map[int64]observedChatEntry)
	}
	a.observeSeq++
	if _, exists := a.observed[chat.ID]; !exists && len(a.observed) >= observedChatLimit {
		var oldestID int64
		var oldestSeq uint64
		first := true
		for id, cached := range a.observed {
			if first || cached.seen < oldestSeq {
				oldestID = id
				oldestSeq = cached.seen
				first = false
			}
		}
		delete(a.observed, oldestID)
	}
	a.observed[chat.ID] = observedChatEntry{chat: entry, seen: a.observeSeq}
}

var _ AdminService = (*Adapter)(nil)
