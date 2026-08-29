package telegram

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/vm75/message-sync/internal/transport"
)

const observedChatLimit = 128

const VisibilityGuidance = "Telegram does not expose Bot Privacy Mode state through the Bot API. Disable Bot Privacy Mode or grant appropriate bot admin visibility, then send a group message so the chat can be observed."

type EndpointReadiness struct {
	Alias  string `json:"alias"`
	Status string `json:"status"`
}

type AdminStatus struct {
	TokenConfigured    bool                `json:"tokenConfigured"`
	Running            bool                `json:"running"`
	Status             string              `json:"status"`
	Endpoints          []EndpointReadiness `json:"endpoints"`
	PrivacyModeKnown   bool                `json:"privacyModeKnown"`
	VisibilityGuidance string              `json:"visibilityGuidance"`
}

type DiscoveredChat struct {
	ChatID   string `json:"chatId"`
	Title    string `json:"title,omitempty"`
	Username string `json:"username,omitempty"`
	Type     string `json:"type"`
}

type AdminService interface {
	AdminStatus(context.Context) AdminStatus
	DiscoverChats(context.Context) ([]DiscoveredChat, error)
}

type observedChatEntry struct {
	chat DiscoveredChat
	seen uint64
}

func (a *Adapter) AdminStatus(_ context.Context) AdminStatus {
	status := AdminStatus{
		TokenConfigured:    BotTokenConfigured(),
		Status:             "not_configured",
		Endpoints:          []EndpointReadiness{},
		PrivacyModeKnown:   false,
		VisibilityGuidance: VisibilityGuidance,
	}
	if a == nil {
		if status.TokenConfigured {
			status.Status = "stopped"
		}
		return status
	}

	a.mu.RLock()
	status.TokenConfigured = true
	status.Running = a.polling
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
var _ = transport.EndpointID("")
