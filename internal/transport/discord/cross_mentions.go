package discord

import (
	"context"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/transport"
)

// Remember provider IDs only in memory after configured ingress/recovery or
// an authorized roster lookup. No reversible native ID enters the router DB.
func (a *Adapter) rememberCrossMentionParticipant(endpoint transport.EndpointID, actorID, nativeID string) {
	if a == nil || actorID == "" || nativeID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.observedMembers == nil {
		a.observedMembers = make(map[transport.EndpointID]map[string]string)
	}
	if a.observedMembers[endpoint] == nil {
		a.observedMembers[endpoint] = make(map[string]string)
	}
	a.observedMembers[endpoint][actorID] = nativeID
}

// Resolve native mentions only for a currently verifiable channel member.
// Unavailable identities, missing permissions and departed users stay as text.
func (a *Adapter) nativeCrossMention(ctx context.Context, ref transport.CrossReference, endpoint transport.EndpointID, channelID string) (string, bool) {
	if a == nil {
		return "", false
	}
	a.mu.RLock()
	userID := a.observedMembers[endpoint][ref.ActorID]
	if userID == "" {
		for _, members := range a.observedMembers {
			if userID = members[ref.ActorID]; userID != "" {
				break
			}
		}
	}
	session := a.session
	a.mu.RUnlock()
	if session == nil {
		return "", false
	}
	channel, err := session.Channel(channelID, discordgo.WithContext(ctx))
	if err != nil || channel == nil || strings.TrimSpace(channel.GuildID) == "" {
		return "", false
	}
	if userID == "" {
		userID = a.crossMentionRosterUser(ctx, session, endpoint, channel.GuildID, ref.ActorID)
		if userID == "" {
			return "", false
		}
	}
	member, err := session.GuildMember(channel.GuildID, userID, discordgo.WithContext(ctx))
	if err != nil || member == nil || member.User == nil || member.User.ID != userID {
		return "", false
	}
	permissionChannel := channel
	if channel.IsThread() {
		permissionChannel, err = session.Channel(channel.ParentID, discordgo.WithContext(ctx))
		if err != nil || permissionChannel == nil || permissionChannel.GuildID != channel.GuildID {
			return "", false
		}
		if channel.Type == discordgo.ChannelTypeGuildPrivateThread {
			threadMember, err := session.ThreadMember(channelID, userID, false, discordgo.WithContext(ctx))
			if err != nil || threadMember == nil || threadMember.UserID != userID {
				return "", false
			}
		}
	}
	guild, err := session.Guild(channel.GuildID, discordgo.WithContext(ctx))
	if err != nil || guild == nil {
		return "", false
	}
	// Use DiscordGo's permission calculation with freshly fetched data, so a
	// stale gateway cache cannot authorize a ping after access was revoked.
	state := discordgo.NewState()
	guild.Members = []*discordgo.Member{member}
	guild.Channels = []*discordgo.Channel{permissionChannel}
	if err := state.GuildAdd(guild); err != nil {
		return "", false
	}
	permissions, err := state.UserChannelPermissions(userID, permissionChannel.ID)
	if err != nil || permissions&discordgo.PermissionViewChannel == 0 {
		return "", false
	}
	return userID, true
}

// Rebuild a missing volatile binding by comparing the authorized HMAC actor
// with current Discord identities. Display names never select the recipient.
func (a *Adapter) crossMentionRosterUser(ctx context.Context, session *discordgo.Session, endpoint transport.EndpointID, guildID, actorID string) string {
	a.mu.RLock()
	hasher := a.hasher
	a.mu.RUnlock()
	if hasher == nil || actorID == "" {
		return ""
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	const pageSize = 1000
	for after := ""; lookupCtx.Err() == nil; {
		members, err := session.GuildMembers(guildID, after, pageSize, discordgo.WithContext(lookupCtx))
		if err != nil || len(members) == 0 {
			return ""
		}
		lastID := ""
		for _, member := range members {
			if member == nil || member.User == nil || member.User.ID == "" {
				continue
			}
			lastID = member.User.ID
			if hasher.UserID("discord:"+lastID) == actorID {
				a.rememberCrossMentionParticipant(endpoint, actorID, lastID)
				return lastID
			}
		}
		if len(members) < pageSize || lastID == "" || lastID == after {
			return ""
		}
		after = lastID
	}
	return ""
}
