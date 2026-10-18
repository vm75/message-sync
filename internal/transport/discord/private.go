package discord

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/transport"
)

type privateInboxKey struct {
	Channel string
	User    string
}

func (a *Adapter) PrivateEvents() <-chan transport.PrivateIncoming { return a.privateEvents }
func (a *Adapter) PrivateDMEnabled() bool                          { return a.privateDMEnabled }

func (a *Adapter) privateOwnerID() string {
	if a.hasher == nil {
		return ""
	}
	id := discordBotUserID(a.session)
	if id == "" {
		return ""
	}
	return a.hasher.ActorID("private-dm-owner", "discord:"+id)
}

func (a *Adapter) handlePrivateMessage(session *discordgo.Session, message *discordgo.Message) bool {
	if session == nil || message == nil || message.GuildID != "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	channel, err := session.Channel(message.ChannelID, discordgo.WithContext(ctx), discordgo.WithRestRetries(0), discordgo.WithRetryOnRatelimit(false))
	if err != nil || channel == nil || channel.Type != discordgo.ChannelTypeDM || channel.GuildID != "" {
		return false
	}
	if !a.privateDMEnabled || a.hasher == nil || message.Author == nil || message.Author.Bot || message.Author.ID == discordBotUserID(session) || len(channel.Recipients) != 1 || channel.Recipients[0].ID != message.Author.ID {
		return true
	}
	token, _ := json.Marshal(privateInboxKey{Channel: channel.ID, User: message.Author.ID})
	in := transport.PrivateIncoming{DisplayName: message.Author.GlobalName, ConnectionID: a.connectionID, Transport: "discord", Inbox: transport.PrivateInbox{ConnectionID: a.connectionID, Transport: "discord", OwnerID: a.privateOwnerID(), ChatID: channel.ID, Token: token}, ActorID: a.hasher.UserID("discord:" + message.Author.ID), RemoteMessageID: message.ID, Text: message.Content, Kind: "text", Timestamp: time.Time(message.Timestamp)}
	if in.DisplayName == "" {
		in.DisplayName = message.Author.Username
	}
	if len(message.Attachments) > 0 || len(message.StickerItems) > 0 || message.Poll != nil {
		in.Kind = "unsupported"
	}
	if ref := message.MessageReference; ref != nil && ref.GuildID == "" && (ref.ChannelID == "" || ref.ChannelID == channel.ID) {
		in.ReplyToMessageID = ref.MessageID
	}
	select {
	case a.privateEvents <- in:
	default:
	}
	return true
}

func (a *Adapter) VerifyPrivateMember(ctx context.Context, endpoint transport.EndpointID, actor string) bool {
	a.mu.RLock()
	channel := a.targets[endpoint]
	a.mu.RUnlock()
	if channel == "" || !a.privateDMEnabled {
		return false
	}
	_, ok := a.nativeCrossMention(ctx, transport.CrossReference{ActorID: actor}, endpoint, channel)
	return ok
}

func (a *Adapter) VerifyPrivateInboxMember(ctx context.Context, endpoint transport.EndpointID, actor string, inbox transport.PrivateInbox) bool {
	if inbox.ConnectionID != a.connectionID || inbox.Transport != "discord" || inbox.OwnerID == "" || inbox.OwnerID != a.privateOwnerID() || a.hasher == nil {
		return false
	}
	var key privateInboxKey
	if json.Unmarshal(inbox.Token, &key) != nil || key.Channel == "" || key.Channel != inbox.ChatID || key.User == "" || a.hasher.UserID("discord:"+key.User) != actor {
		return false
	}
	a.rememberCrossMentionParticipant(endpoint, actor, key.User)
	return a.VerifyPrivateMember(ctx, endpoint, actor)
}

func (a *Adapter) ResolvePrivateInbox(ctx context.Context, endpoint transport.EndpointID, actor string) (transport.PrivateInbox, bool) {
	a.mu.RLock()
	channelID, session := a.targets[endpoint], a.session
	a.mu.RUnlock()
	if session == nil || !a.privateDMEnabled || channelID == "" {
		return transport.PrivateInbox{}, false
	}
	user, ok := a.nativeCrossMention(ctx, transport.CrossReference{ActorID: actor}, endpoint, channelID)
	if !ok {
		return transport.PrivateInbox{}, false
	}
	channel, err := session.UserChannelCreate(user, discordgo.WithContext(ctx), discordgo.WithRestRetries(0), discordgo.WithRetryOnRatelimit(false))
	if err != nil || channel == nil || channel.Type != discordgo.ChannelTypeDM {
		return transport.PrivateInbox{}, false
	}
	token, _ := json.Marshal(privateInboxKey{Channel: channel.ID, User: user})
	return transport.PrivateInbox{ConnectionID: a.connectionID, Transport: "discord", OwnerID: a.privateOwnerID(), ChatID: channel.ID, Token: token}, true
}

func (a *Adapter) SendPrivate(ctx context.Context, inbox transport.PrivateInbox, text string, options ...transport.PrivateSendOptions) (transport.PrivateSendResult, error) {
	if inbox.ConnectionID != a.connectionID || inbox.Transport != "discord" || inbox.OwnerID == "" || inbox.OwnerID != a.privateOwnerID() || !a.privateDMEnabled || a.session == nil {
		return transport.PrivateSendResult{}, errors.New("private Discord connection unavailable")
	}
	var key privateInboxKey
	if err := json.Unmarshal(inbox.Token, &key); err != nil || key.User == "" || key.Channel == "" || key.Channel != inbox.ChatID {
		return transport.PrivateSendResult{}, errors.New("invalid private Discord inbox")
	}
	channel, err := a.session.UserChannelCreate(key.User, discordgo.WithContext(ctx), discordgo.WithRestRetries(0), discordgo.WithRetryOnRatelimit(false))
	if err != nil || channel == nil || channel.Type != discordgo.ChannelTypeDM || channel.ID != key.Channel {
		return transport.PrivateSendResult{}, transport.NewFailure(transport.FailureDestinationMissing, 0, errors.New("private Discord inbox unavailable"))
	}
	var reference *discordgo.MessageReference
	if len(options) > 0 {
		o := options[0]
		if o.Attribution != "" {
			text = strings.Replace(text, "DM from "+o.Attribution, "DM from ***"+strings.NewReplacer("*", "", "_", "", "\n", " ").Replace(o.Attribution)+"***", 1)
		}
		if o.ReplyToMessageID != "" {
			fail := false
			reference = &discordgo.MessageReference{MessageID: o.ReplyToMessageID, ChannelID: channel.ID, FailIfNotExists: &fail}
		}
	}
	message, err := a.session.ChannelMessageSendComplex(channel.ID, &discordgo.MessageSend{Content: text, Reference: reference, AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}, Flags: discordgo.MessageFlagsSuppressEmbeds}, discordgo.WithContext(ctx), discordgo.WithRestRetries(0), discordgo.WithRetryOnRatelimit(false))
	if err != nil {
		return transport.PrivateSendResult{}, classifyDiscordFailure(err)
	}
	if message == nil || message.ID == "" {
		return transport.PrivateSendResult{}, errors.New("private Discord send unconfirmed")
	}
	return transport.PrivateSendResult{RemoteMessageID: message.ID}, nil
}

func (a *Adapter) DeletePrivateCopyForMe(context.Context, transport.PrivateCopy) error {
	// Message deletion changes the shared DM. Account-local deletion is absent.
	return errors.New("private local cleanup unsupported")
}
