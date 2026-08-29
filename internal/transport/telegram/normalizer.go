package telegram

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
)

// Normalizer owns the Telegram ingress privacy boundary. Raw Telegram user IDs,
// usernames, and chat metadata must not cross this boundary. Only configured
// chat IDs are mapped to safe endpoint aliases, and user IDs are immediately
// replaced by HMAC-derived actor IDs.
type Normalizer struct {
	endpoints    map[int64]transport.EndpointID
	hasher       *identity.Hasher
	usernameMode config.UsernameMode
}

func NewNormalizer(chatIDs map[string]string, hasher *identity.Hasher, usernameMode config.UsernameMode) (*Normalizer, error) {
	if hasher == nil {
		return nil, errors.New("identity hasher is required")
	}
	if !usernameMode.IsValid() {
		return nil, errors.New("username mode must be push_name or hash")
	}

	endpoints := make(map[int64]transport.EndpointID, len(chatIDs))
	for alias, rawChatID := range chatIDs {
		if err := config.ValidateAlias(alias); err != nil {
			return nil, err
		}
		chatID := strings.TrimSpace(rawChatID)
		if err := config.ValidateEndpointRemoteID(config.TransportTelegram, chatID); err != nil {
			return nil, fmt.Errorf("endpoint %q: %w", alias, err)
		}
		parsed, err := strconv.ParseInt(chatID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("endpoint %q has invalid Telegram chat ID", alias)
		}
		if _, duplicate := endpoints[parsed]; duplicate {
			return nil, fmt.Errorf("endpoint %q duplicates a configured Telegram chat", alias)
		}
		endpoints[parsed] = transport.EndpointID(alias)
	}

	return &Normalizer{
		endpoints:    endpoints,
		hasher:       hasher,
		usernameMode: usernameMode,
	}, nil
}

func (n *Normalizer) NormalizeMessage(msg *models.Message, botUserID int64) (transport.Incoming, bool) {
	if n == nil || msg == nil {
		return transport.Incoming{}, false
	}
	if msg.Chat.Type != models.ChatTypeGroup && msg.Chat.Type != models.ChatTypeSupergroup {
		return transport.Incoming{}, false
	}
	endpoint, configured := n.endpoints[msg.Chat.ID]
	if !configured || msg.ID <= 0 || msg.From == nil || msg.From.ID == 0 {
		return transport.Incoming{}, false
	}
	if botUserID != 0 && msg.From.ID == botUserID {
		return transport.Incoming{}, false
	}

	text, entities := telegramTextPayload(msg)
	if strings.TrimSpace(text) == "" && !hasTelegramMedia(msg) {
		// Service-only messages and unsupported payloads are intentionally
		// ignored. Supported media may legitimately have no caption.
		return transport.Incoming{}, false
	}

	displayName := ""
	if n.usernameMode == config.UsernameModePushName {
		displayName = transientTelegramDisplayName(msg.From)
	}

	var replyTo *transport.MessageRef
	var quotedText string
	if reply := msg.ReplyToMessage; reply != nil && reply.ID > 0 && reply.Chat.ID == msg.Chat.ID {
		replyTo = &transport.MessageRef{
			Endpoint:        endpoint,
			RemoteMessageID: strconv.Itoa(reply.ID),
		}
		quotedText, _ = telegramTextPayload(reply)
	}

	timestamp := time.Unix(int64(msg.Date), 0).UTC()
	if msg.Date == 0 {
		timestamp = time.Time{}
	}

	return transport.Incoming{
		Endpoint: endpoint,
		RemoteID: strconv.Itoa(msg.ID),
		Sender: transport.Sender{
			DisplayName: displayName,
			OpaqueID:    telegramActorID(n.hasher, msg.From.ID),
		},
		FromSelf:   false,
		Kind:       "text",
		Text:       text,
		Mentions:   normalizeTelegramMentions(entities, n.hasher, n.usernameMode),
		ReplyTo:    replyTo,
		QuotedText: quotedText,
		Timestamp:  timestamp,
	}, true
}

func (n *Normalizer) NormalizeEditedMessage(msg *models.Message, botUserID int64) (transport.Incoming, bool) {
	incoming, ok := n.NormalizeMessage(msg, botUserID)
	if !ok || strings.TrimSpace(incoming.Text) == "" {
		return transport.Incoming{}, false
	}
	incoming.Kind = "edit"
	incoming.ReplyTo = &transport.MessageRef{
		Endpoint:        incoming.Endpoint,
		RemoteMessageID: incoming.RemoteID,
	}
	incoming.QuotedText = ""
	incoming.MediaLoader = nil
	return incoming, true
}

func (n *Normalizer) NormalizeReaction(update *models.MessageReactionUpdated, botUserID int64) (transport.Incoming, bool) {
	if n == nil || update == nil {
		return transport.Incoming{}, false
	}
	if update.Chat.Type != models.ChatTypeGroup && update.Chat.Type != models.ChatTypeSupergroup {
		return transport.Incoming{}, false
	}
	endpoint, configured := n.endpoints[update.Chat.ID]
	if !configured || update.MessageID <= 0 || update.User == nil || update.User.ID == 0 {
		return transport.Incoming{}, false
	}
	if botUserID != 0 && update.User.ID == botUserID {
		return transport.Incoming{}, false
	}

	emoji := ""
	switch len(update.NewReaction) {
	case 0:
		// Empty reaction list represents removal.
	case 1:
		reaction := update.NewReaction[0]
		if reaction.Type != models.ReactionTypeTypeEmoji || reaction.ReactionTypeEmoji == nil {
			return transport.Incoming{}, false
		}
		emoji = strings.TrimSpace(reaction.ReactionTypeEmoji.Emoji)
		if emoji == "" {
			return transport.Incoming{}, false
		}
	default:
		// Canonical reaction state currently represents one reaction per actor.
		// Multiple/custom/paid Telegram reactions are therefore unsupported.
		return transport.Incoming{}, false
	}

	displayName := ""
	if n.usernameMode == config.UsernameModePushName {
		displayName = transientTelegramDisplayName(update.User)
	}
	timestamp := time.Unix(int64(update.Date), 0).UTC()
	if update.Date == 0 {
		timestamp = time.Time{}
	}
	remoteID := strconv.Itoa(update.MessageID)
	return transport.Incoming{
		Endpoint: endpoint,
		RemoteID: remoteID,
		Sender: transport.Sender{
			DisplayName: displayName,
			OpaqueID:    telegramActorID(n.hasher, update.User.ID),
		},
		Kind: "reaction",
		Text: emoji,
		ReplyTo: &transport.MessageRef{
			Endpoint:        endpoint,
			RemoteMessageID: remoteID,
		},
		Timestamp: timestamp,
	}, true
}

func telegramTextPayload(msg *models.Message) (string, []models.MessageEntity) {
	if msg == nil {
		return "", nil
	}
	if msg.Text != "" {
		return msg.Text, msg.Entities
	}
	if msg.Caption != "" {
		return msg.Caption, msg.CaptionEntities
	}
	return "", nil
}

func transientTelegramDisplayName(user *models.User) string {
	if user == nil {
		return ""
	}
	if name := strings.Join(strings.Fields(strings.TrimSpace(user.FirstName+" "+user.LastName)), " "); name != "" {
		return name
	}
	return strings.TrimSpace(user.Username)
}

func telegramActorID(hasher *identity.Hasher, userID int64) string {
	if hasher == nil || userID == 0 {
		return ""
	}
	return hasher.UserID("telegram:" + strconv.FormatInt(userID, 10))
}

func normalizeTelegramMentions(entities []models.MessageEntity, hasher *identity.Hasher, usernameMode config.UsernameMode) []transport.Mention {
	if len(entities) == 0 || hasher == nil {
		return nil
	}

	mentions := make([]transport.Mention, 0, len(entities))
	for _, entity := range entities {
		// Username-only @mentions do not expose a stable Telegram user ID in
		// the Bot API message entity, so they stay as transient message text.
		if entity.Type != models.MessageEntityTypeTextMention || entity.User == nil || entity.User.ID == 0 {
			continue
		}
		opaqueID := telegramActorID(hasher, entity.User.ID)
		if opaqueID == "" {
			continue
		}
		name := opaqueID
		if usernameMode == config.UsernameModePushName {
			if displayName := transientTelegramDisplayName(entity.User); displayName != "" {
				name = displayName
			}
		}
		mentions = append(mentions, transport.Mention{
			RemoteID: opaqueID,
			Name:     name,
		})
	}
	if len(mentions) == 0 {
		return nil
	}
	return mentions
}
