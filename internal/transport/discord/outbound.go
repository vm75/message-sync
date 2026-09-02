package discord

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/transport"
)

const (
	maxWebhookUsernameRunes = 80
	replyMarkerText         = "↪"
)

type discordAPI interface {
	ChannelMessages(channelID string, limit int, beforeID, afterID, aroundID string, options ...discordgo.RequestOption) ([]*discordgo.Message, error)
	ChannelMessage(channelID, messageID string, options ...discordgo.RequestOption) (*discordgo.Message, error)
	ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend, options ...discordgo.RequestOption) (*discordgo.Message, error)
	MessageReactionAdd(channelID, messageID, emojiID string, options ...discordgo.RequestOption) error
	MessageReactionRemove(channelID, messageID, emojiID, userID string, options ...discordgo.RequestOption) error
}

// discordChannelAPI is implemented by discordgo.Session. It is kept
// separate from discordAPI so small outbound test doubles do not need to
// implement the channel lookup used only for reply links.
type discordChannelAPI interface {
	Channel(channelID string, options ...discordgo.RequestOption) (*discordgo.Channel, error)
}

type reactionKey struct {
	endpoint transport.EndpointID
	message  string
}

func (a *Adapter) Send(ctx context.Context, outgoing transport.Outgoing) (transport.MessageRef, error) {
	if a == nil {
		return transport.MessageRef{}, errors.New("Discord transport is not initialized")
	}
	if outgoing.AttributionOnly {
		// WhatsApp needs a separate attribution message for media types that
		// cannot carry a caption. Discord webhook usernames already provide
		// that attribution, so no extra Discord message is emitted.
		return transport.MessageRef{Endpoint: outgoing.Endpoint}, nil
	}

	channelID, webhook, mediaEnabled, mediaMaxBytes, _, ok := a.outboundState(outgoing.Endpoint)
	if !ok {
		return transport.MessageRef{}, errors.New("unknown Discord endpoint")
	}
	if webhook == nil {
		return transport.MessageRef{}, errors.New("managed Discord webhook is unavailable")
	}

	username := sanitizeWebhookUsername(outgoing.Sender.DisplayName)
	if username == "" {
		username = sanitizeWebhookUsername(outgoing.Sender.OpaqueID)
	}
	if username == "" {
		username = "message-sync"
	}

	content := outgoing.SourceText
	if content == "" && outgoing.Sender.DisplayName == "" && outgoing.Sender.OpaqueID == "" {
		content = outgoing.Text
	}
	if outgoing.ReplyFallback {
		content = discordReplyFallback(outgoing.OriginEndpoint, outgoing.QuotedText, content)
	}
	content = sanitizeOutgoingMentions(content, outgoing.Mentions)
	if outgoing.Kind == "poll" {
		var err error
		content, err = discordPollText(content, outgoing.PollOptions, outgoing.PollSelectableCount)
		if err != nil {
			return transport.MessageRef{}, err
		}
	}

	var file *WebhookFile
	if len(outgoing.MediaBytes) > 0 {
		if !mediaEnabled {
			return transport.MessageRef{}, errors.New("Discord media forwarding is disabled")
		}
		if mediaMaxBytes > 0 && uint64(len(outgoing.MediaBytes)) > mediaMaxBytes {
			return transport.MessageRef{}, errors.New("Discord media exceeds configured size limit")
		}
		var err error
		file, err = discordWebhookFile(outgoing.Kind, outgoing.MediaBytes)
		if err != nil {
			return transport.MessageRef{}, err
		}
	} else if isDiscordMediaKind(outgoing.Kind) {
		return transport.MessageRef{}, errors.New("outgoing Discord media bytes are required")
	}

	if outgoing.ReplyTo != nil {
		if link := a.replyLink(channelID, outgoing.ReplyTo.RemoteMessageID); link != "" {
			content = fmt.Sprintf("[%s](%s)\n\n%s", discordReplyLinkLabel(outgoing.OriginEndpoint, outgoing.QuotedText), link, content)
		} else {
			content = discordReplyFallback(outgoing.OriginEndpoint, outgoing.QuotedText, content)
		}
	}

	remoteID, err := webhook.Execute(ctx, channelID, WebhookMessage{
		Username: username,
		Content:  content,
		File:     file,
	})
	if err != nil {
		return transport.MessageRef{}, err
	}
	if strings.TrimSpace(remoteID) == "" {
		return transport.MessageRef{}, errors.New("Discord webhook returned an empty message id")
	}
	return transport.MessageRef{
		Endpoint:        outgoing.Endpoint,
		RemoteMessageID: strings.TrimSpace(remoteID),
		IsTargetFromMe:  true,
	}, nil
}

func (a *Adapter) replyLink(channelID, messageID string) string {
	if a == nil {
		return ""
	}
	a.mu.RLock()
	session := a.session
	api := a.api
	a.mu.RUnlock()
	if session != nil && session.State != nil {
		if channel, err := session.State.Channel(strings.TrimSpace(channelID)); err == nil && channel != nil {
			if link := discordMessageLink(channel.GuildID, channel.ID, messageID); link != "" {
				return link
			}
		}
	}

	lookup, ok := api.(discordChannelAPI)
	if !ok {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	channel, err := lookup.Channel(strings.TrimSpace(channelID), discordgo.WithContext(ctx), discordgo.WithRetryOnRatelimit(true))
	if err != nil || channel == nil {
		return ""
	}
	return discordMessageLink(channel.GuildID, channel.ID, messageID)
}

func discordMessageLink(guildID, channelID, messageID string) string {
	guildID = strings.TrimSpace(guildID)
	channelID = strings.TrimSpace(channelID)
	messageID = strings.TrimSpace(messageID)
	if guildID == "" || channelID == "" || messageID == "" {
		return ""
	}
	return fmt.Sprintf("https://discord.com/channels/%s/%s/%s", guildID, channelID, messageID)
}

func discordReplyLinkLabel(origin transport.EndpointID, quotedText string) string {
	label := "message"
	if line := strings.TrimSpace(strings.SplitN(strings.ReplaceAll(quotedText, "\r", ""), "\n", 2)[0]); line != "" {
		label = line
		if strings.HasPrefix(label, "*_") {
			label = strings.TrimPrefix(label, "*_")
			label = strings.Replace(label, "_*: ", ": ", 1)
		}
	}
	label = strings.Join(strings.Fields(label), " ")
	label = strings.NewReplacer("[", "(", "]", ")").Replace(label)
	if len([]rune(label)) > 400 {
		label = string([]rune(label)[:400]) + "…"
	}
	if origin == "" {
		origin = "source"
	}
	return fmt.Sprintf("↪ reply to %s: %s", origin, label)
}

func (a *Adapter) React(ctx context.Context, reaction transport.Reaction) error {
	if a == nil {
		return errors.New("Discord transport is not initialized")
	}
	channelID, _, _, _, api, ok := a.outboundState(reaction.Endpoint)
	if !ok {
		return errors.New("unknown Discord endpoint")
	}
	if api == nil {
		return errors.New("Discord reaction API is unavailable")
	}
	messageID := strings.TrimSpace(reaction.TargetRemoteID)
	if messageID == "" {
		return errors.New("Discord reaction target is required")
	}

	key := reactionKey{endpoint: reaction.Endpoint, message: messageID}
	emoji := strings.TrimSpace(reaction.Emoji)

	a.mu.Lock()
	previous := a.reactionState[key]
	a.mu.Unlock()

	if previous != "" && previous != emoji {
		if err := api.MessageReactionRemove(
			channelID,
			messageID,
			previous,
			"@me",
			discordgo.WithContext(ctx),
			discordgo.WithRetryOnRatelimit(true),
		); err != nil && !isDiscordNotFound(err) {
			return classifyDiscordFailure(err)
		}
		a.mu.Lock()
		delete(a.reactionState, key)
		a.mu.Unlock()
	}

	if emoji == "" {
		return nil
	}
	if previous == emoji {
		return nil
	}
	if err := api.MessageReactionAdd(
		channelID,
		messageID,
		emoji,
		discordgo.WithContext(ctx),
		discordgo.WithRetryOnRatelimit(true),
	); err != nil {
		return classifyDiscordFailure(err)
	}
	a.mu.Lock()
	a.reactionState[key] = emoji
	a.mu.Unlock()
	return nil
}

func (a *Adapter) Edit(ctx context.Context, ref transport.MessageRef, text string) error {
	if a == nil {
		return errors.New("Discord transport is not initialized")
	}
	channelID, webhook, _, _, _, ok := a.outboundState(ref.Endpoint)
	if !ok {
		return errors.New("unknown Discord endpoint")
	}
	if webhook == nil {
		return errors.New("managed Discord webhook is unavailable")
	}
	messageID := strings.TrimSpace(ref.RemoteMessageID)
	if messageID == "" {
		return errors.New("Discord edit target is required")
	}

	if err := webhook.Edit(ctx, channelID, messageID, sourceBodyFromForwarded(text)); err != nil {
		return err
	}
	return nil
}

func (a *Adapter) Delete(ctx context.Context, ref transport.MessageRef) error {
	if a == nil {
		return errors.New("Discord transport is not initialized")
	}
	channelID, webhook, _, _, _, ok := a.outboundState(ref.Endpoint)
	if !ok {
		return errors.New("unknown Discord endpoint")
	}
	if webhook == nil {
		return errors.New("managed Discord webhook is unavailable")
	}
	messageID := strings.TrimSpace(ref.RemoteMessageID)
	if messageID == "" {
		return errors.New("Discord delete target is required")
	}

	a.markSuppressedDelete(channelID, messageID)
	if err := webhook.Delete(ctx, channelID, messageID); err != nil {
		a.consumeSuppressedDelete(channelID, messageID)
		return err
	}
	return nil
}

func (a *Adapter) outboundState(endpoint transport.EndpointID) (string, ChannelWebhook, bool, uint64, discordAPI, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	channelID, ok := a.targets[endpoint]
	return channelID, a.webhook, a.mediaEnabled, a.mediaMaxBytes, a.api, ok
}

func sendNativeReplyMarker(ctx context.Context, api discordAPI, channelID, messageID string) error {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return errors.New("Discord reply target is required")
	}
	failIfMissing := false
	_, err := api.ChannelMessageSendComplex(
		channelID,
		&discordgo.MessageSend{
			Content: replyMarkerText,
			Reference: &discordgo.MessageReference{
				MessageID:       messageID,
				ChannelID:       channelID,
				FailIfNotExists: &failIfMissing,
			},
		},
		discordgo.WithContext(ctx),
		discordgo.WithRetryOnRatelimit(true),
	)
	if err != nil {
		return classifyDiscordFailure(err)
	}
	return nil
}

func sanitizeWebhookUsername(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return ""
	}
	if utf8.RuneCountInString(value) <= maxWebhookUsernameRunes {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:maxWebhookUsernameRunes]))
}

func discordReplyFallback(origin transport.EndpointID, quotedText, content string) string {
	quote := strings.TrimSpace(quotedText)
	if quote == "" {
		quote = "message"
	}
	quote = strings.ReplaceAll(quote, "\r", " ")
	quote = strings.ReplaceAll(quote, "\n", "\n> ")
	if len([]rune(quote)) > 400 {
		quote = string([]rune(quote)[:400]) + "…"
	}
	label := string(origin)
	if label == "" {
		label = "source"
	}
	if content == "" {
		return fmt.Sprintf("> reply to %s: %s", label, quote)
	}
	return fmt.Sprintf("> reply to %s: %s\n\n%s", label, quote, content)
}

func sourceBodyFromForwarded(text string) string {
	if !strings.HasPrefix(text, "*_") {
		return text
	}
	if index := strings.Index(text, "_*: "); index >= 0 {
		return text[index+4:]
	}
	return text
}

func discordWebhookFile(kind string, data []byte) (*WebhookFile, error) {
	file := &WebhookFile{Data: data}
	switch kind {
	case "image":
		file.Name = "image.jpg"
		file.ContentType = "image/jpeg"
	case "video":
		file.Name = "video.mp4"
		file.ContentType = "video/mp4"
	case "audio":
		file.Name = "audio.ogg"
		file.ContentType = "audio/ogg"
	case "document":
		file.Name = "document.bin"
		file.ContentType = "application/octet-stream"
	case "sticker":
		file.Name = "sticker.webp"
		file.ContentType = "image/webp"
	default:
		return nil, errors.New("unsupported Discord media kind")
	}
	return file, nil
}

func isDiscordMediaKind(kind string) bool {
	switch kind {
	case "image", "video", "audio", "document", "sticker":
		return true
	default:
		return false
	}
}

func sanitizeOutgoingMentions(content string, mentions []transport.Mention) string {
	for _, mention := range mentions {
		remoteID := strings.TrimSpace(mention.RemoteID)
		if remoteID == "" {
			continue
		}
		name := strings.Join(strings.Fields(mention.Name), " ")
		if name == "" || name == remoteID {
			name = "participant"
		}
		content = strings.ReplaceAll(content, "@"+remoteID, "@"+name)
	}
	return content
}

func discordPollText(question string, options []string, selectableCount int) (string, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return "", errors.New("outgoing Discord poll question is required")
	}
	if len(options) == 0 {
		return "", errors.New("outgoing Discord poll options are required")
	}

	var builder strings.Builder
	builder.WriteString("Poll: ")
	builder.WriteString(question)
	for i, option := range options {
		builder.WriteString("\n")
		builder.WriteString(fmt.Sprintf("%d. %s", i+1, strings.TrimSpace(option)))
	}
	if selectableCount > 1 {
		builder.WriteString(fmt.Sprintf("\nChoose up to %d options.", selectableCount))
	} else {
		builder.WriteString("\nChoose one option.")
	}
	return builder.String(), nil
}
