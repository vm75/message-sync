package telegram

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

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
	connectionID string
	endpoints    map[int64]transport.EndpointID
	hasher       *identity.Hasher
	usernameMode config.UsernameMode
}

func NewNormalizer(chatIDs map[string]string, hasher *identity.Hasher, usernameMode config.UsernameMode) (*Normalizer, error) {
	return NewNormalizerWithConnection(chatIDs, hasher, usernameMode, "")
}

func NewNormalizerWithConnection(chatIDs map[string]string, hasher *identity.Hasher, usernameMode config.UsernameMode, connectionID string) (*Normalizer, error) {
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
		connectionID: strings.TrimSpace(connectionID),
		endpoints:    endpoints,
		hasher:       hasher,
		usernameMode: usernameMode,
	}, nil
}

func (n *Normalizer) chatID(endpoint transport.EndpointID) (int64, bool) {
	if n == nil {
		return 0, false
	}
	for chatID, alias := range n.endpoints {
		if alias == endpoint {
			return chatID, true
		}
	}
	return 0, false
}

func (n *Normalizer) endpoint(chatID int64) (transport.EndpointID, bool) {
	if n == nil {
		return "", false
	}
	endpoint, ok := n.endpoints[chatID]
	return endpoint, ok
}

func (n *Normalizer) withChatMigration(endpoint transport.EndpointID, oldChatID, newChatID int64) (*Normalizer, error) {
	if n == nil {
		return nil, errors.New("Telegram normalizer is not initialized")
	}
	if existing, ok := n.endpoints[newChatID]; ok && existing != endpoint {
		return nil, errors.New("Telegram migration target is already configured")
	}
	if existing, ok := n.endpoints[oldChatID]; !ok {
		if migrated, already := n.endpoints[newChatID]; already && migrated == endpoint {
			return n, nil
		}
		return nil, errors.New("Telegram migration source is not configured")
	} else if existing != endpoint {
		return nil, errors.New("Telegram migration source does not match endpoint")
	}

	endpoints := make(map[int64]transport.EndpointID, len(n.endpoints))
	for chatID, alias := range n.endpoints {
		endpoints[chatID] = alias
	}
	delete(endpoints, oldChatID)
	endpoints[newChatID] = endpoint
	return &Normalizer{
		connectionID: n.connectionID,
		endpoints:    endpoints,
		hasher:       n.hasher,
		usernameMode: n.usernameMode,
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

	if telegramSensitivePayload(msg) {
		return transport.Incoming{}, false
	}

	text, entities := telegramTextPayload(msg)
	kind := "text"
	var pollOptions []string
	selectableCount := 0
	providerReference := ""
	if msg.Poll != nil {
		if telegramPollRepresentable(msg.Poll) {
			kind = "poll"
			text = msg.Poll.Question
			text, _ = normalizeTelegramMentions(text, msg.Poll.QuestionEntities, n.hasher, n.usernameMode)
			for _, option := range msg.Poll.Options {
				pollOptions = append(pollOptions, option.Text)
			}
			if msg.Poll.AllowsMultipleAnswers {
				selectableCount = len(pollOptions)
			} else {
				selectableCount = 1
			}
			providerReference = msg.Poll.ID
		} else {
			var ok bool
			text, ok = telegramPollMessageText(msg.Poll, n.hasher, n.usernameMode)
			if !ok {
				return transport.Incoming{}, false
			}
		}
		entities = nil
	}
	text, mentions := normalizeTelegramMentions(text, entities, n.hasher, n.usernameMode)
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
		if reply.Poll != nil {
			quotedText, _ = telegramPollMessageText(reply.Poll, n.hasher, n.usernameMode)
		} else {
			var quotedEntities []models.MessageEntity
			quotedText, quotedEntities = telegramTextPayload(reply)
			quotedText, _ = normalizeTelegramMentions(quotedText, quotedEntities, n.hasher, n.usernameMode)
		}
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
		FromSelf:    false,
		Kind:        kind,
		Text:        text,
		PollOptions: pollOptions, PollSelectableCount: selectableCount,
		PollProvider: func() string {
			if providerReference != "" {
				if n != nil && n.connectionID != "" {
					return "telegram:" + n.connectionID
				}
				return "telegram"
			}
			return ""
		}(),
		PollProviderReference: providerReference,
		PollSourceUnavailable: providerReference != "",
		Mentions:              mentions,
		ReplyTo:               replyTo,
		ChildScope: func() *transport.ChildScope {
			if msg.MessageThreadID > 0 {
				return &transport.ChildScope{Kind: transport.ScopeKindTelegramTopic, RemoteID: strconv.Itoa(msg.MessageThreadID)}
			}
			return nil
		}(),
		QuotedText: quotedText,
		Timestamp:  timestamp,
	}, true
}

func telegramPollRepresentable(poll *models.Poll) bool {
	if poll == nil || poll.Type == "quiz" || utf8.RuneCountInString(strings.TrimSpace(poll.Question)) < 1 || utf8.RuneCountInString(poll.Question) > 300 || len(poll.Options) < 2 || len(poll.Options) > 10 {
		return false
	}
	for _, option := range poll.Options {
		if utf8.RuneCountInString(option.Text) < 1 || utf8.RuneCountInString(option.Text) > 100 {
			return false
		}
	}
	return true
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

type telegramMentionReplacement struct {
	start int
	end   int
	text  string
}

func normalizeTelegramMentions(content string, entities []models.MessageEntity, hasher *identity.Hasher, usernameMode config.UsernameMode) (string, []transport.Mention) {
	if len(entities) == 0 || hasher == nil {
		return content, nil
	}

	units := utf16.Encode([]rune(content))
	replacements := make([]telegramMentionReplacement, 0, len(entities))
	mentions := make([]transport.Mention, 0, len(entities))
	for _, entity := range entities {
		start := entity.Offset
		end := entity.Offset + entity.Length
		if start < 0 || entity.Length <= 0 || end > len(units) {
			continue
		}

		switch entity.Type {
		case models.MessageEntityTypeTextMention:
			if entity.User == nil || entity.User.ID == 0 {
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
			mentions = append(mentions, transport.Mention{RemoteID: opaqueID, Name: name})
			replacements = append(replacements, telegramMentionReplacement{
				start: start,
				end:   end,
				text:  "@" + name,
			})
		case models.MessageEntityTypeMention:
			// Username-only entities carry no stable user ID. Replace the
			// username with a generic transient label rather than forwarding
			// or persisting remote identity.
			replacements = append(replacements, telegramMentionReplacement{
				start: start,
				end:   end,
				text:  "@mention",
			})
		}
	}

	sort.SliceStable(replacements, func(i, j int) bool {
		return replacements[i].start > replacements[j].start
	})
	for _, replacement := range replacements {
		replacementUnits := utf16.Encode([]rune(replacement.text))
		next := make([]uint16, 0, len(units)-(replacement.end-replacement.start)+len(replacementUnits))
		next = append(next, units[:replacement.start]...)
		next = append(next, replacementUnits...)
		next = append(next, units[replacement.end:]...)
		units = next
	}
	content = string(utf16.Decode(units))
	if len(mentions) == 0 {
		return content, nil
	}
	return content, mentions
}
