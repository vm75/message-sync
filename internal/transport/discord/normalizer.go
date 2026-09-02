package discord

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
)

var (
	discordUserMentionPattern    = regexp.MustCompile(`<@!?([0-9]+)>`)
	discordRoleMentionPattern    = regexp.MustCompile(`<@&[0-9]+>`)
	discordChannelMentionPattern = regexp.MustCompile(`<#[0-9]+>`)
)

// ManagedWebhookChecker identifies bridge-owned channel webhooks transiently.
// Implementations must not persist webhook credentials, URLs, or identity in sync.db.
type ManagedWebhookChecker interface {
	IsManagedWebhook(channelID, webhookID string) bool
}

// Normalizer owns the Discord ingress privacy boundary. Discord identifiers and
// display names remain transient and only configured channel IDs are mapped to
// safe application aliases.
type Normalizer struct {
	endpoints    map[string]transport.EndpointID
	hasher       *identity.Hasher
	usernameMode config.UsernameMode
}

func (n *Normalizer) endpointFor(channelID string) (transport.EndpointID, bool) {
	if n == nil {
		return "", false
	}
	endpoint, ok := n.endpoints[strings.TrimSpace(channelID)]
	return endpoint, ok
}

func (n *Normalizer) NormalizePollVote(channelID, messageID, userID string, indexes []int, hasher *identity.Hasher) (transport.Incoming, bool) {
	endpoint, ok := n.endpointFor(channelID)
	if !ok || strings.TrimSpace(messageID) == "" || strings.TrimSpace(userID) == "" || hasher == nil {
		return transport.Incoming{}, false
	}
	return transport.Incoming{
		Endpoint: endpoint, RemoteID: strings.TrimSpace(messageID),
		Sender: transport.Sender{OpaqueID: hasher.UserID("discord:" + strings.TrimSpace(userID))},
		Kind:   "poll_vote", ReplyTo: &transport.MessageRef{Endpoint: endpoint, RemoteMessageID: strings.TrimSpace(messageID)},
		PollOptionIndexes: indexes, Timestamp: time.Now().UTC(),
	}, true
}

func NewNormalizer(channelIDs map[string]string, hasher *identity.Hasher, usernameMode config.UsernameMode) (*Normalizer, error) {
	if hasher == nil {
		return nil, errors.New("identity hasher is required")
	}
	if !usernameMode.IsValid() {
		return nil, errors.New("username mode must be push_name or hash")
	}

	endpoints := make(map[string]transport.EndpointID, len(channelIDs))
	for alias, rawChannelID := range channelIDs {
		if err := config.ValidateAlias(alias); err != nil {
			return nil, err
		}
		channelID := strings.TrimSpace(rawChannelID)
		if err := config.ValidateEndpointRemoteID(config.TransportDiscord, channelID); err != nil {
			return nil, fmt.Errorf("endpoint %q: %w", alias, err)
		}
		if _, duplicate := endpoints[channelID]; duplicate {
			return nil, fmt.Errorf("endpoint %q duplicates a configured Discord channel", alias)
		}
		endpoints[channelID] = transport.EndpointID(alias)
	}
	return &Normalizer{
		endpoints:    endpoints,
		hasher:       hasher,
		usernameMode: usernameMode,
	}, nil
}

func (n *Normalizer) NormalizeMessage(evt *discordgo.MessageCreate, botUserID string, webhooks ManagedWebhookChecker) (transport.Incoming, bool) {
	if n == nil || evt == nil || evt.Message == nil {
		return transport.Incoming{}, false
	}
	msg := evt.Message

	// GuildID is empty for DMs. Discord DMs are never bridge ingress.
	if strings.TrimSpace(msg.GuildID) == "" {
		return transport.Incoming{}, false
	}
	if !discordMessageSupported(msg) {
		return transport.Incoming{}, false
	}
	endpoint, configured := n.endpoints[strings.TrimSpace(msg.ChannelID)]
	if !configured || strings.TrimSpace(msg.ID) == "" || msg.Author == nil || strings.TrimSpace(msg.Author.ID) == "" {
		return transport.Incoming{}, false
	}

	authorID := strings.TrimSpace(msg.Author.ID)
	if strings.TrimSpace(botUserID) != "" && authorID == strings.TrimSpace(botUserID) {
		return transport.Incoming{}, false
	}
	if strings.TrimSpace(msg.WebhookID) != "" && webhooks != nil && webhooks.IsManagedWebhook(msg.ChannelID, msg.WebhookID) {
		return transport.Incoming{}, false
	}

	displayName := ""
	if n.usernameMode == config.UsernameModePushName {
		displayName = transientDisplayName(msg)
	}

	var replyTo *transport.MessageRef
	var quotedText string
	if ref := msg.MessageReference; ref != nil && strings.TrimSpace(ref.MessageID) != "" {
		channelID := strings.TrimSpace(ref.ChannelID)
		if channelID == "" {
			channelID = strings.TrimSpace(msg.ChannelID)
		}
		if replyEndpoint, ok := n.endpoints[channelID]; ok {
			replyTo = &transport.MessageRef{
				Endpoint:        replyEndpoint,
				RemoteMessageID: strings.TrimSpace(ref.MessageID),
			}
			if msg.ReferencedMessage != nil {
				quotedText = sanitizeDiscordMentions(msg.ReferencedMessage.Content, msg.ReferencedMessage.Mentions, n.hasher)
			}
		}
	}

	text := sanitizeDiscordMentions(msg.Content, msg.Mentions, n.hasher)
	kind := "text"
	var pollOptions []string
	selectableCount := 0
	durationHours := 0
	if msg.Poll != nil {
		kind = "poll"
		text = sanitizeDiscordMentions(msg.Poll.Question.Text, nil, n.hasher)
		for _, answer := range msg.Poll.Answers {
			if answer.Media == nil || strings.TrimSpace(answer.Media.Text) == "" {
				return transport.Incoming{}, false
			}
			pollOptions = append(pollOptions, answer.Media.Text)
		}
		if msg.Poll.AllowMultiselect {
			selectableCount = len(pollOptions)
		} else {
			selectableCount = 1
		}
		durationHours = msg.Poll.Duration
	}

	return transport.Incoming{
		Endpoint: endpoint,
		RemoteID: strings.TrimSpace(msg.ID),
		Sender: transport.Sender{
			DisplayName: displayName,
			OpaqueID:    n.hasher.UserID("discord:" + authorID),
		},
		Kind:                kind,
		Text:                text,
		PollOptions:         pollOptions,
		PollSelectableCount: selectableCount,
		PollDurationHours:   durationHours,
		ReplyTo:             replyTo,
		QuotedText:          quotedText,
		Timestamp:           msg.Timestamp,
	}, true
}

func transientDisplayName(msg *discordgo.Message) string {
	if msg == nil {
		return ""
	}
	if msg.Member != nil {
		if nick := strings.TrimSpace(msg.Member.Nick); nick != "" {
			return nick
		}
	}
	return transientUserDisplayName(msg.Author)
}

func transientUserDisplayName(user *discordgo.User) string {
	if user == nil {
		return ""
	}
	if name := strings.TrimSpace(user.GlobalName); name != "" {
		return name
	}
	return strings.TrimSpace(user.Username)
}

func discordMessageSupported(msg *discordgo.Message) bool {
	if msg == nil {
		return false
	}
	if msg.Poll != nil {
		return strings.TrimSpace(msg.Poll.Question.Text) != "" && len(msg.Poll.Answers) > 0
	}
	switch msg.Type {
	case discordgo.MessageTypeDefault, discordgo.MessageTypeReply, discordgo.MessageTypeThreadStarterMessage:
	default:
		return false
	}
	return strings.TrimSpace(msg.Content) != "" || len(msg.Attachments) > 0
}

func sanitizeDiscordMentions(content string, mentions []*discordgo.User, hasher *identity.Hasher) string {
	labels := make(map[string]string, len(mentions))
	for _, mentioned := range mentions {
		if mentioned == nil {
			continue
		}
		id := strings.TrimSpace(mentioned.ID)
		if id == "" {
			continue
		}
		label := strings.Join(strings.Fields(transientUserDisplayName(mentioned)), " ")
		if (label == "" || label == id) && hasher != nil {
			label = hasher.UserID("discord:" + id)
		}
		if label == "" {
			label = "user"
		}
		labels[id] = "@" + label
	}

	content = discordUserMentionPattern.ReplaceAllStringFunc(content, func(raw string) string {
		matches := discordUserMentionPattern.FindStringSubmatch(raw)
		if len(matches) != 2 {
			return "@user"
		}
		if label := labels[matches[1]]; label != "" {
			return label
		}
		if hasher != nil {
			return "@" + hasher.UserID("discord:"+matches[1])
		}
		return "@user"
	})
	content = discordRoleMentionPattern.ReplaceAllString(content, "@role")
	content = discordChannelMentionPattern.ReplaceAllString(content, "#channel")
	return content
}
