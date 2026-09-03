package discord

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/transport"
)

var _ transport.RecoverySource = (*Adapter)(nil)

type discordThreadRecoveryAPI interface {
	ThreadsActive(string, ...discordgo.RequestOption) (*discordgo.ThreadsList, error)
	ThreadsArchived(string, *time.Time, int, ...discordgo.RequestOption) (*discordgo.ThreadsList, error)
}

// RecoveryStreams returns one safe stream key per configured Discord endpoint.
func (a *Adapter) RecoveryStreams() []string {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	streams := make([]string, 0, len(a.targets))
	for alias := range a.targets {
		streams = append(streams, string(alias))
	}
	a.mu.RUnlock()
	sort.Strings(streams)
	return streams
}

func (a *Adapter) RecoverySignals() <-chan struct{} {
	if a == nil {
		return nil
	}
	return a.recoverySignals
}

// Recover reads only the bounded channel history after the accepted cursor.
// Discord returns history newest-first, so each page is reversed before the
// normalized events are delivered to the coordinator.
func (a *Adapter) Recover(ctx context.Context, request transport.RecoveryRequest, emit func(context.Context, transport.Incoming) error) error {
	if a == nil || emit == nil {
		return errors.New("Discord recovery is not initialized")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	alias := strings.TrimSpace(request.Cursor.StreamKey)
	if alias == "" {
		return errors.New("Discord recovery stream is required")
	}

	a.mu.RLock()
	channelID := a.targets[transport.EndpointID(alias)]
	api := a.api
	normalizer := a.normalizer
	webhooks := a.webhook
	a.mu.RUnlock()
	if strings.TrimSpace(channelID) == "" || api == nil || normalizer == nil {
		a.setHistoryStatus(alias, HistoryStatusUnavailable)
		return errors.New("Discord recovery is unavailable")
	}

	maxEvents := request.MaxEvents
	if maxEvents <= 0 {
		return nil
	}
	if maxEvents > 200 {
		maxEvents = 200
	}
	limit := maxEvents
	if limit > 100 {
		limit = 100
	}
	cutoff := time.Time{}
	if request.MaxAge > 0 {
		cutoff = time.Now().UTC().Add(-request.MaxAge)
	}
	a.setHistoryStatus(alias, HistoryStatusReady)

	afterID := ""
	if request.Cursor.Position > 0 {
		afterID = strconv.FormatInt(request.Cursor.Position, 10)
	}
	emitted := 0
	for emitted < maxEvents {
		if err := ctx.Err(); err != nil {
			return err
		}
		pageSize := limit
		if remaining := maxEvents - emitted; remaining < pageSize {
			pageSize = remaining
		}
		messages, err := api.ChannelMessages(channelID, pageSize, "", afterID, "", discordgo.WithContext(ctx), discordgo.WithRetryOnRatelimit(true))
		if err != nil {
			failure := classifyDiscordFailure(err)
			if isDiscordForbidden(err) {
				a.setHistoryStatus(alias, HistoryStatusMissingPermission)
			}
			return failure
		}
		if len(messages) == 0 {
			break
		}
		sort.SliceStable(messages, func(i, j int) bool {
			return discordMessagePosition(messages[i]) < discordMessagePosition(messages[j])
		})
		progress := false
		for _, message := range messages {
			if message == nil || strings.TrimSpace(message.ID) == "" {
				continue
			}
			position := discordMessagePosition(message)
			if position <= request.Cursor.Position {
				continue
			}
			if cutoff.IsZero() == false && discordMessageTime(message).Before(cutoff) {
				continue
			}
			progress = true
			routed := routeDiscordMessage(message, channelID)
			var incoming transport.Incoming
			var ok bool
			if message.EditedTimestamp != nil {
				incoming, ok = normalizer.NormalizeUpdate(routed, discordBotUserID(a.session), webhooks)
			} else {
				incoming, ok = normalizer.NormalizeMessage(&discordgo.MessageCreate{Message: routed}, discordBotUserID(a.session), webhooks)
			}
			if !ok {
				continue
			}
			incoming, ok = a.withDiscordMedia(incoming, message)
			if !ok {
				continue
			}
			addCheckpoint(&incoming)
			if err := emit(ctx, incoming); err != nil {
				return err
			}
			emitted++
			if emitted >= maxEvents {
				break
			}
		}
		if emitted >= maxEvents || len(messages) < pageSize || !progress {
			break
		}
		last := messages[len(messages)-1]
		if last == nil || discordMessagePosition(last) <= 0 {
			break
		}
		afterID = strings.TrimSpace(last.ID)
	}
	if emitted < maxEvents {
		if threadAPI, ok := api.(discordThreadRecoveryAPI); ok {
			for _, listRequest := range []func() (*discordgo.ThreadsList, error){
				func() (*discordgo.ThreadsList, error) {
					return threadAPI.ThreadsActive(channelID, discordgo.WithContext(ctx), discordgo.WithRetryOnRatelimit(true))
				},
				func() (*discordgo.ThreadsList, error) {
					return threadAPI.ThreadsArchived(channelID, nil, 100, discordgo.WithContext(ctx), discordgo.WithRetryOnRatelimit(true))
				},
			} {
				threads, err := listRequest()
				if err != nil {
					if isDiscordForbidden(err) {
						a.setHistoryStatus(alias, HistoryStatusMissingPermission)
						return classifyDiscordFailure(err)
					}
					continue
				}
				if threads == nil {
					continue
				}
				sort.SliceStable(threads.Threads, func(i, j int) bool {
					return discordChannelPosition(threads.Threads[i]) < discordChannelPosition(threads.Threads[j])
				})
				for _, thread := range threads.Threads {
					if emitted >= maxEvents || thread == nil || strings.TrimSpace(thread.ID) == "" {
						break
					}
					messages, err := api.ChannelMessages(thread.ID, maxEvents-emitted, "", afterID, "", discordgo.WithContext(ctx), discordgo.WithRetryOnRatelimit(true))
					if err != nil {
						continue
					}
					sort.SliceStable(messages, func(i, j int) bool { return discordMessagePosition(messages[i]) < discordMessagePosition(messages[j]) })
					for _, message := range messages {
						if emitted >= maxEvents || message == nil || discordMessagePosition(message) <= request.Cursor.Position {
							continue
						}
						if !cutoff.IsZero() && discordMessageTime(message).Before(cutoff) {
							continue
						}
						routed := routeDiscordMessage(message, channelID)
						incoming, ok := normalizer.NormalizeMessage(&discordgo.MessageCreate{Message: routed}, discordBotUserID(a.session), webhooks)
						if !ok {
							continue
						}
						incoming.ChildScope = &transport.ChildScope{Kind: transport.ScopeKindDiscordThread, RemoteID: thread.ID, Label: strings.TrimSpace(thread.Name)}
						incoming, ok = a.withDiscordMedia(incoming, message)
						if !ok {
							continue
						}
						addCheckpoint(&incoming)
						if err := emit(ctx, incoming); err != nil {
							return err
						}
						emitted++
					}
				}
			}
		}
	}
	return nil
}

func discordMessagePosition(message *discordgo.Message) int64 {
	if message == nil {
		return 0
	}
	position, err := strconv.ParseInt(strings.TrimSpace(message.ID), 10, 64)
	if err != nil {
		return 0
	}
	return position
}

func discordChannelPosition(channel *discordgo.Channel) int64 {
	if channel == nil {
		return 0
	}
	position, err := strconv.ParseInt(strings.TrimSpace(channel.ID), 10, 64)
	if err != nil {
		return 0
	}
	return position
}

func discordMessageTime(message *discordgo.Message) time.Time {
	if message == nil {
		return time.Time{}
	}
	if !message.Timestamp.IsZero() {
		return message.Timestamp
	}
	if timestamp, err := discordgo.SnowflakeTimestamp(message.ID); err == nil {
		return timestamp
	}
	return time.Time{}
}

func (a *Adapter) setHistoryStatus(alias string, status HistoryStatus) {
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.historyStatus == nil {
		a.historyStatus = make(map[string]HistoryStatus)
	}
	a.historyStatus[alias] = status
	a.mu.Unlock()
}
