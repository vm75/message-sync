package discord

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/transport"
)

type WebhookStatus string

const (
	WebhookStatusReady             WebhookStatus = "ready"
	WebhookStatusMissingPermission WebhookStatus = "missing_permission"
	WebhookStatusUnavailable       WebhookStatus = "unavailable"
)

type HistoryStatus string

const (
	HistoryStatusUnknown           HistoryStatus = "unknown"
	HistoryStatusReady             HistoryStatus = "ready"
	HistoryStatusMissingPermission HistoryStatus = "missing_permission"
	HistoryStatusUnavailable       HistoryStatus = "unavailable"
)

type EndpointWebhookStatus struct {
	Alias  string        `json:"alias"`
	Status WebhookStatus `json:"status"`
}

type EndpointHistoryStatus struct {
	Alias  string        `json:"alias"`
	Status HistoryStatus `json:"status"`
}

type AdminStatus struct {
	Configured bool                    `json:"configured"`
	Connected  bool                    `json:"connected"`
	Status     string                  `json:"status"`
	Webhooks   []EndpointWebhookStatus `json:"webhooks"`
	History    []EndpointHistoryStatus `json:"history"`
}

type DiscoveredChannel struct {
	GuildID     string `json:"guildId"`
	GuildName   string `json:"guildName"`
	ChannelID   string `json:"channelId"`
	ChannelName string `json:"channelName"`
}

type AdminService interface {
	AdminStatus(context.Context) AdminStatus
	DiscoverChannels(context.Context) ([]DiscoveredChannel, error)
}

type discordAdminAPI interface {
	UserGuilds(limit int, beforeID, afterID string, withCounts bool, options ...discordgo.RequestOption) ([]*discordgo.UserGuild, error)
	GuildChannels(guildID string, options ...discordgo.RequestOption) ([]*discordgo.Channel, error)
}

func (a *Adapter) AdminStatus(_ context.Context) AdminStatus {
	status := AdminStatus{
		Configured: true,
		Status:     "disconnected",
		Webhooks:   []EndpointWebhookStatus{},
		History:    []EndpointHistoryStatus{},
	}
	if a == nil {
		status.Configured = false
		status.Status = "not_configured"
		return status
	}

	a.mu.RLock()
	status.Connected = a.connected
	targets := make(map[transport.EndpointID]string, len(a.targets))
	for alias, channelID := range a.targets {
		targets[alias] = channelID
	}
	webhook := a.webhook
	for alias, historyStatus := range a.historyStatus {
		status.History = append(status.History, EndpointHistoryStatus{Alias: alias, Status: historyStatus})
	}
	a.mu.RUnlock()

	if status.Connected {
		status.Status = "connected"
	}

	readiness, _ := webhook.(webhookReadinessProvider)
	for alias, channelID := range targets {
		webhookStatus := WebhookStatusUnavailable
		if readiness != nil {
			webhookStatus = readiness.Readiness(channelID)
		}
		status.Webhooks = append(status.Webhooks, EndpointWebhookStatus{
			Alias:  string(alias),
			Status: webhookStatus,
		})
	}
	sort.Slice(status.Webhooks, func(i, j int) bool {
		return status.Webhooks[i].Alias < status.Webhooks[j].Alias
	})
	sort.Slice(status.History, func(i, j int) bool {
		return status.History[i].Alias < status.History[j].Alias
	})
	return status
}

func (a *Adapter) DiscoverChannels(ctx context.Context) ([]DiscoveredChannel, error) {
	if a == nil {
		return nil, errors.New("Discord transport is not initialized")
	}

	a.mu.RLock()
	connected := a.connected
	api := a.adminAPI
	a.mu.RUnlock()
	if !connected {
		return nil, errors.New("Discord gateway is not connected")
	}
	if api == nil {
		return nil, errors.New("Discord discovery API is unavailable")
	}

	guilds, err := api.UserGuilds(
		200,
		"",
		"",
		false,
		discordgo.WithContext(ctx),
		discordgo.WithRetryOnRatelimit(true),
	)
	if err != nil {
		return nil, errors.New("list Discord guilds")
	}

	channels := make([]DiscoveredChannel, 0)
	for _, guild := range guilds {
		if guild == nil {
			continue
		}
		guildID := strings.TrimSpace(guild.ID)
		if guildID == "" {
			continue
		}
		guildChannels, err := api.GuildChannels(
			guildID,
			discordgo.WithContext(ctx),
			discordgo.WithRetryOnRatelimit(true),
		)
		if err != nil {
			return nil, errors.New("list Discord guild channels")
		}
		for _, channel := range guildChannels {
			if channel == nil {
				continue
			}
			if channel.Type != discordgo.ChannelTypeGuildText && channel.Type != discordgo.ChannelTypeGuildNews {
				continue
			}
			channelID := strings.TrimSpace(channel.ID)
			if channelID == "" {
				continue
			}
			channels = append(channels, DiscoveredChannel{
				GuildID:     guildID,
				GuildName:   guild.Name,
				ChannelID:   channelID,
				ChannelName: channel.Name,
			})
		}
	}

	sort.Slice(channels, func(i, j int) bool {
		if channels[i].GuildName != channels[j].GuildName {
			return channels[i].GuildName < channels[j].GuildName
		}
		if channels[i].ChannelName != channels[j].ChannelName {
			return channels[i].ChannelName < channels[j].ChannelName
		}
		if channels[i].GuildID != channels[j].GuildID {
			return channels[i].GuildID < channels[j].GuildID
		}
		return channels[i].ChannelID < channels[j].ChannelID
	})
	return channels, nil
}
