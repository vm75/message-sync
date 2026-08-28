package discord

import (
	"context"
	"time"

	"github.com/bwmarrin/discordgo"
)

func (a *Adapter) handleMessageUpdate(session *discordgo.Session, event *discordgo.MessageUpdate) {
	if a == nil || event == nil || event.Message == nil {
		return
	}
	a.mu.RLock()
	normalizer := a.normalizer
	webhooks := a.webhook
	api := a.api
	a.mu.RUnlock()
	if normalizer == nil {
		return
	}

	message := event.Message
	if message.Author == nil && api != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		fetched, err := api.ChannelMessage(
			message.ChannelID,
			message.ID,
			discordgo.WithContext(ctx),
			discordgo.WithRetryOnRatelimit(true),
		)
		if err != nil || fetched == nil {
			if a.logger != nil {
				a.logger.Warn("Discord message update could not be resolved",
					"event", "discord_update_unresolved",
					"error_kind", "message_fetch",
				)
			}
			return
		}
		message = fetched
	}

	routeChannelID := configuredIngressChannelID(session, normalizer, message.ChannelID)
	if routeChannelID == "" {
		return
	}
	incoming, ok := normalizer.NormalizeUpdate(routeDiscordMessage(message, routeChannelID), discordBotUserID(session), webhooks)
	if !ok {
		return
	}
	a.emit(incoming)
}

func (a *Adapter) handleMessageDelete(session *discordgo.Session, event *discordgo.MessageDelete) {
	if a == nil || event == nil || event.Message == nil {
		return
	}
	if a.consumeSuppressedDelete(event.ChannelID, event.ID) {
		return
	}
	a.mu.RLock()
	normalizer := a.normalizer
	a.mu.RUnlock()
	if normalizer == nil {
		return
	}
	routeChannelID := configuredIngressChannelID(session, normalizer, event.ChannelID)
	if routeChannelID == "" {
		return
	}
	routed := *event
	routed.Message = routeDiscordMessage(event.Message, routeChannelID)
	incoming, ok := normalizer.NormalizeDelete(&routed)
	if !ok {
		return
	}
	a.emit(incoming)
}

func (a *Adapter) handleMessageReactionAdd(session *discordgo.Session, event *discordgo.MessageReactionAdd) {
	if a == nil || event == nil || event.MessageReaction == nil {
		return
	}
	a.mu.RLock()
	normalizer := a.normalizer
	a.mu.RUnlock()
	if normalizer == nil {
		return
	}
	routeChannelID := configuredIngressChannelID(session, normalizer, event.ChannelID)
	if routeChannelID == "" {
		return
	}
	routedReaction := *event.MessageReaction
	routedReaction.ChannelID = routeChannelID
	incoming, ok := normalizer.NormalizeReaction(&routedReaction, discordBotUserID(session), false)
	if !ok {
		return
	}
	a.emit(incoming)
}

func (a *Adapter) handleMessageReactionRemove(session *discordgo.Session, event *discordgo.MessageReactionRemove) {
	if a == nil || event == nil || event.MessageReaction == nil {
		return
	}
	a.mu.RLock()
	normalizer := a.normalizer
	a.mu.RUnlock()
	if normalizer == nil {
		return
	}
	routeChannelID := configuredIngressChannelID(session, normalizer, event.ChannelID)
	if routeChannelID == "" {
		return
	}
	routedReaction := *event.MessageReaction
	routedReaction.ChannelID = routeChannelID
	incoming, ok := normalizer.NormalizeReaction(&routedReaction, discordBotUserID(session), true)
	if !ok {
		return
	}
	a.emit(incoming)
}

func discordBotUserID(session *discordgo.Session) string {
	if session == nil || session.State == nil || session.State.User == nil {
		return ""
	}
	return session.State.User.ID
}

func (a *Adapter) markSuppressedDelete(channelID, messageID string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.suppressedDeletes[channelID+"\x00"+messageID] = struct{}{}
	a.mu.Unlock()
}

func (a *Adapter) consumeSuppressedDelete(channelID, messageID string) bool {
	if a == nil {
		return false
	}
	key := channelID + "\x00" + messageID
	a.mu.Lock()
	_, ok := a.suppressedDeletes[key]
	if ok {
		delete(a.suppressedDeletes, key)
	}
	a.mu.Unlock()
	return ok
}
