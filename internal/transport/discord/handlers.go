package discord

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/transport"
)

type pollActorKey struct{ channel, message, actor string }

func (a *Adapter) handlePollVoteAdd(session *discordgo.Session, event *discordgo.MessagePollVoteAdd) {
	a.handlePollVote(session, event.ChannelID, event.MessageID, event.UserID, event.AnswerID, true)
}

func (a *Adapter) handlePollVoteRemove(session *discordgo.Session, event *discordgo.MessagePollVoteRemove) {
	a.handlePollVote(session, event.ChannelID, event.MessageID, event.UserID, event.AnswerID, false)
}

func (a *Adapter) handlePollVote(session *discordgo.Session, channelID, messageID, userID string, answerID int, added bool) {
	if a == nil || session == nil || strings.TrimSpace(channelID) == "" || strings.TrimSpace(messageID) == "" || strings.TrimSpace(userID) == "" || strings.TrimSpace(userID) == discordBotUserID(session) {
		return
	}
	a.mu.RLock()
	normalizer, api, hasher := a.normalizer, a.api, a.hasher
	a.mu.RUnlock()
	routeChannelID := configuredIngressChannelID(session, normalizer, channelID)
	_, ok := normalizer.endpointFor(routeChannelID)
	if !ok || hasher == nil || api == nil {
		return
	}
	poll, err := api.ChannelMessage(channelID, messageID, discordgo.WithRetryOnRatelimit(true))
	if err != nil || poll == nil || poll.Poll == nil {
		return
	}
	optionIndex, ok := discordPollAnswerIndex(poll.Poll, answerID)
	if !ok {
		return
	}
	key := pollActorKey{channel: channelID, message: messageID, actor: userID}
	a.mu.Lock()
	selected := a.pollSelections[key]
	if selected == nil {
		selected = make(map[int]struct{})
		a.pollSelections[key] = selected
	}
	if !added {
		delete(selected, optionIndex)
	} else {
		selected[optionIndex] = struct{}{}
	}
	indexes := make([]int, 0, len(selected))
	for index := range selected {
		indexes = append(indexes, index)
	}
	a.mu.Unlock()
	incoming, ok := normalizer.NormalizePollVote(routeChannelID, messageID, userID, indexes, hasher)
	if ok {
		incoming.ChildScope = discordChildScope(session, channelID, routeChannelID)
		a.emit(incoming)
	}
}

func discordPollAnswerIndex(poll *discordgo.Poll, answerID int) (int, bool) {
	if poll == nil {
		return 0, false
	}
	for index, answer := range poll.Answers {
		if answer.AnswerID == answerID {
			return index, true
		}
	}
	return 0, false
}

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
	incoming.ChildScope = discordChildScope(session, message.ChannelID, routeChannelID)
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

func (a *Adapter) handleMessageDeleteBulk(session *discordgo.Session, event *discordgo.MessageDeleteBulk) {
	if a == nil || event == nil {
		return
	}
	if len(event.Messages) == 0 {
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
	for _, messageID := range event.Messages {
		incoming, ok := normalizer.NormalizeDelete(&discordgo.MessageDelete{Message: &discordgo.Message{
			ID:        messageID,
			ChannelID: routeChannelID,
		}})
		if !ok {
			continue
		}
		a.emit(incoming)
	}
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
	incoming.ChildScope = discordChildScope(session, event.ChannelID, routeChannelID)
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
	incoming.ChildScope = discordChildScope(session, event.ChannelID, routeChannelID)
	a.emit(incoming)
}

func addCheckpoint(incoming *transport.Incoming) {
	if incoming == nil {
		return
	}
	position, err := strconv.ParseInt(incoming.RemoteID, 10, 64)
	if err != nil || position <= 0 {
		return
	}
	incoming.Checkpoint = transport.Checkpoint{
		StreamKey: string(incoming.Endpoint), Position: position,
		EventTimestamp: incoming.Timestamp, Valid: true,
	}
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
