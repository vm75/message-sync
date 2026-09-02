package discord

import (
	"context"
	"errors"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/transport"
	"github.com/vm75/message-sync/internal/verification"
)

func (a *Adapter) AssignRole(ctx context.Context, alias, roleID, userID string) error {
	if a == nil || a.session == nil {
		return verification.ErrDestinationMissing
	}
	a.mu.RLock()
	channelID := a.targets[transport.EndpointID(alias)]
	a.mu.RUnlock()
	if channelID == "" || strings.TrimSpace(roleID) == "" || strings.TrimSpace(userID) == "" {
		return verification.ErrDestinationMissing
	}
	channel, err := a.session.State.Channel(channelID)
	if err != nil || channel == nil {
		return verification.ErrDestinationMissing
	}
	if channel.GuildID == "" {
		return errors.New("Discord channel has no guild")
	}
	return a.session.GuildMemberRoleAdd(channel.GuildID, userID, roleID, discordgo.WithContext(ctx), discordgo.WithRetryOnRatelimit(true))
}
