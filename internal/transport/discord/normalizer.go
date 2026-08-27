package discord

import (
	"errors"
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
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
				quotedText = msg.ReferencedMessage.Content
			}
		}
	}

	mentions := make([]transport.Mention, 0, len(msg.Mentions))
	for _, mentioned := range msg.Mentions {
		if mentioned == nil || strings.TrimSpace(mentioned.ID) == "" {
			continue
		}
		mentions = append(mentions, transport.Mention{
			RemoteID: strings.TrimSpace(mentioned.ID),
			Name:     transientUserDisplayName(mentioned),
		})
	}
	if len(mentions) == 0 {
		mentions = nil
	}

	return transport.Incoming{
		Endpoint: endpoint,
		RemoteID: strings.TrimSpace(msg.ID),
		Sender: transport.Sender{
			DisplayName: displayName,
			OpaqueID:    n.hasher.UserID("discord:" + authorID),
		},
		Kind:       "text",
		Text:       msg.Content,
		Mentions:   mentions,
		ReplyTo:    replyTo,
		QuotedText: quotedText,
		Timestamp:  msg.Timestamp,
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
