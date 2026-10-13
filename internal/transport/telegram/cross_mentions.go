package telegram

import (
	"context"
	"html"
	"strconv"
	"strings"

	telegrambot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/vm75/message-sync/internal/transport"
)

// Telegram participant IDs are held only in memory after an actual observed
// message. No native IDs or usernames enter sync.db or its message ledger.
func (a *Adapter) rememberCrossMentionParticipant(endpoint transport.EndpointID, actorID string, userID int64) {
	if a == nil || actorID == "" || userID <= 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.observedParticipants == nil {
		a.observedParticipants = make(map[transport.EndpointID]map[string]int64)
	}
	if a.observedParticipants[endpoint] == nil {
		a.observedParticipants[endpoint] = make(map[string]int64)
	}
	a.observedParticipants[endpoint][actorID] = userID
}

func (a *Adapter) verifiedCrossMention(ctx context.Context, api telegramAPI, chatID int64, ref transport.CrossReference, endpoint transport.EndpointID) (string, bool) {
	if a == nil || ref.Endpoint != endpoint {
		return "", false
	}
	a.mu.RLock()
	userID := a.observedParticipants[endpoint][ref.ActorID]
	a.mu.RUnlock()
	if userID <= 0 {
		return "", false
	}
	checker, ok := api.(interface {
		GetChatMember(context.Context, *telegrambot.GetChatMemberParams) (*models.ChatMember, error)
	})
	if !ok {
		return "", false
	}
	member, err := checker.GetChatMember(ctx, &telegrambot.GetChatMemberParams{ChatID: chatID, UserID: userID})
	if err != nil || member == nil || member.Left != nil || member.Banned != nil {
		return "", false
	}
	if member.Restricted != nil && !member.Restricted.IsMember {
		return "", false
	}
	// This inline mention refers to a verified user without exposing the
	// raw Telegram identifier as visible text.
	handle := strings.TrimPrefix(strings.TrimSuffix(ref.Token, "}"), "@{"+string(ref.Endpoint)+"/")
	return "<a href=\"tg://user?id=" + strconv.FormatInt(userID, 10) + "\">" + html.EscapeString(handle) + "</a>", true
}

func (a *Adapter) renderCrossMentions(ctx context.Context, api telegramAPI, chatID int64, outgoing transport.Outgoing, content string, mode models.ParseMode) (string, models.ParseMode) {
	if len(outgoing.References) == 0 {
		return content, mode
	}
	// HTML markup may be inserted only after escaping the provider text.
	escaped := content
	if mode != models.ParseModeHTML {
		escaped = html.EscapeString(content)
	}
	rendered := transport.RewriteCrossReferences(escaped, outgoing.References, func(ref transport.CrossReference) (string, bool) {
		return a.verifiedCrossMention(ctx, api, chatID, ref, outgoing.Endpoint)
	})
	if rendered == escaped {
		return content, mode
	}
	return rendered, models.ParseModeHTML
}
