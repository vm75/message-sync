package discord

import (
	"context"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/transport"
)

// Remember provider IDs only in memory and only for users actually observed
// in a configured destination. No reversible native ID enters the router DB.
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
// Missing permissions, missing observation, and departed users stay as text.
func (a *Adapter) nativeCrossMention(ctx context.Context, ref transport.CrossReference, endpoint transport.EndpointID, channelID string) (string, bool) {
	if a == nil || ref.Endpoint != endpoint {
		return "", false
	}
	a.mu.RLock()
	userID := a.observedMembers[endpoint][ref.ActorID]
	session := a.session
	a.mu.RUnlock()
	if userID == "" || session == nil {
		return "", false
	}
	channel, err := session.Channel(channelID, discordgo.WithContext(ctx))
	if err != nil || channel == nil || strings.TrimSpace(channel.GuildID) == "" {
		return "", false
	}
	member, err := session.GuildMember(channel.GuildID, userID, discordgo.WithContext(ctx))
	if err != nil || member == nil {
		return "", false
	}
	return userID, true
}
