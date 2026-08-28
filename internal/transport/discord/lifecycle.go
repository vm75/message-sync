package discord

import (
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/transport"
)

func (n *Normalizer) NormalizeUpdate(message *discordgo.Message, botUserID string, webhooks ManagedWebhookChecker) (transport.Incoming, bool) {
	incoming, ok := n.NormalizeMessage(&discordgo.MessageCreate{Message: message}, botUserID, webhooks)
	if !ok {
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

func (n *Normalizer) NormalizeDelete(event *discordgo.MessageDelete) (transport.Incoming, bool) {
	if n == nil || event == nil || event.Message == nil {
		return transport.Incoming{}, false
	}
	message := event.Message
	if strings.TrimSpace(message.GuildID) == "" {
		return transport.Incoming{}, false
	}
	endpoint, ok := n.endpoints[strings.TrimSpace(message.ChannelID)]
	if !ok || strings.TrimSpace(message.ID) == "" {
		return transport.Incoming{}, false
	}
	return transport.Incoming{
		Endpoint: endpoint,
		RemoteID: strings.TrimSpace(message.ID),
		Kind:     "delete",
		ReplyTo: &transport.MessageRef{
			Endpoint:        endpoint,
			RemoteMessageID: strings.TrimSpace(message.ID),
		},
		Timestamp: time.Now().UTC(),
	}, true
}

func (n *Normalizer) NormalizeReaction(reaction *discordgo.MessageReaction, botUserID string, removed bool) (transport.Incoming, bool) {
	if n == nil || reaction == nil {
		return transport.Incoming{}, false
	}
	if strings.TrimSpace(reaction.GuildID) == "" {
		return transport.Incoming{}, false
	}
	endpoint, ok := n.endpoints[strings.TrimSpace(reaction.ChannelID)]
	if !ok || strings.TrimSpace(reaction.MessageID) == "" || strings.TrimSpace(reaction.UserID) == "" {
		return transport.Incoming{}, false
	}

	actorID := strings.TrimSpace(reaction.UserID)
	emoji := strings.TrimSpace(reaction.Emoji.APIName())
	if !removed && emoji == "" {
		return transport.Incoming{}, false
	}
	if removed {
		emoji = ""
	}

	return transport.Incoming{
		Endpoint: endpoint,
		RemoteID: strings.TrimSpace(reaction.MessageID),
		Sender: transport.Sender{
			OpaqueID: n.hasher.UserID("discord:" + actorID),
		},
		FromSelf: actorID == strings.TrimSpace(botUserID),
		Kind:     "reaction",
		Text:     emoji,
		ReplyTo: &transport.MessageRef{
			Endpoint:        endpoint,
			RemoteMessageID: strings.TrimSpace(reaction.MessageID),
		},
		Timestamp: time.Now().UTC(),
	}, true
}
