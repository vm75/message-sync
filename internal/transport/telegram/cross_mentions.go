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

func (a *Adapter) verifiedCrossMention(ctx context.Context, api telegramAPI, chatID int64, ref transport.CrossReference, endpoint transport.EndpointID, anonymized bool) (string, bool) {
	if a == nil {
		return "", false
	}
	a.mu.RLock()
	userID := a.observedParticipants[endpoint][ref.ActorID]
	if userID <= 0 {
		for _, participants := range a.observedParticipants {
			if userID = participants[ref.ActorID]; userID > 0 {
				break
			}
		}
	}
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
	if err != nil || member == nil {
		return "", false
	}
	var memberUser *models.User
	switch {
	case member.Owner != nil:
		memberUser = member.Owner.User
	case member.Administrator != nil:
		memberUser = &member.Administrator.User
	case member.Member != nil:
		memberUser = member.Member.User
	case member.Restricted != nil && member.Restricted.IsMember:
		memberUser = member.Restricted.User
	}
	if memberUser == nil || memberUser.ID != userID {
		return "", false
	}
	// This inline mention refers to a verified user without exposing the
	// raw Telegram identifier as visible text.
	// Transient names are HTML-escaped by renderCrossMentions. The generated
	// pseudonym is ASCII-safe and remains private across destinations.
	label := transport.ReferenceDisplayLabel(ref, anonymized)
	return "<a href=\"tg://user?id=" + strconv.FormatInt(userID, 10) + "\">@" + label + "</a>", true
}

func (a *Adapter) renderCrossMentions(ctx context.Context, api telegramAPI, chatID int64, outgoing transport.Outgoing, content string, mode models.ParseMode) (string, models.ParseMode) {
	if !strings.Contains(content, "@{") {
		return content, mode
	}
	// The text may already be formatted HTML, otherwise escape it before
	// introducing verified native HTML links. Escaping also applies to the
	// transient reference labels: names must never become markup.
	escaped := content
	if mode != models.ParseModeHTML {
		escaped = html.EscapeString(content)
	}
	refs := make([]transport.CrossReference, len(outgoing.References))
	copy(refs, outgoing.References)
	for i := range refs {
		refs[i].Token = html.EscapeString(refs[i].Token)
		refs[i].Name = html.EscapeString(refs[i].Name)
	}
	rendered := transport.RewriteCrossReferences(escaped, refs, func(ref transport.CrossReference) (string, bool) {
		return a.verifiedCrossMention(ctx, api, chatID, ref, outgoing.Endpoint, outgoing.ReferenceAnonymized)
	})
	if rendered == escaped {
		return content, mode
	}
	return rendered, models.ParseModeHTML
}
