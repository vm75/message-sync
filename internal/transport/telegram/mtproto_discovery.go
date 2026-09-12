package telegram

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/gotd/td/tg"
)

const mtprotoForumTopicPageSize = 100

type mtprotoTopic struct {
	ID      int
	Title   string
	General bool
}

type mtprotoTopicClient interface {
	ListForumTopics(context.Context, mtprotoPeerState) ([]mtprotoTopic, error)
}

var _ TopicDiscoveryService = (*MTProtoAdapter)(nil)

func (a *MTProtoAdapter) discoveryClient(ctx context.Context) (mtprotoLiveClient, error) {
	if a == nil || a.live == nil {
		return nil, errors.New("Telegram MTProto transport is not initialized")
	}
	auth, err := a.waitAuth(ctx)
	if err != nil {
		return nil, err
	}
	client, ok := auth.(mtprotoLiveClient)
	if !ok {
		return nil, errors.New("Telegram MTProto discovery is unavailable")
	}
	return client, nil
}

func (a *MTProtoAdapter) discoverMTProtoGroups(ctx context.Context) ([]mtprotoGroup, error) {
	client, err := a.discoveryClient(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := client.ListGroups(ctx)
	if err != nil {
		return nil, errors.New("discover Telegram MTProto groups")
	}
	if a.live.replacePeers(groups) {
		if err := a.persistMTProtoPeers(ctx); err != nil {
			return nil, err
		}
	}
	return groups, nil
}

func (a *MTProtoAdapter) discoverMTProtoChats(ctx context.Context) ([]DiscoveredChat, error) {
	groups, err := a.discoverMTProtoGroups(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]DiscoveredChat, 0, len(groups))
	for _, group := range groups {
		typ := "group"
		if group.Peer.Kind == "channel" {
			typ = "supergroup"
		}
		out = append(out, DiscoveredChat{
			ChatID:   strings.TrimSpace(group.Peer.RemoteID),
			Title:    strings.TrimSpace(group.Title),
			Username: strings.TrimSpace(group.Username),
			Type:     typ,
			Forum:    group.Forum,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChatID < out[j].ChatID })
	return out, nil
}

func (a *MTProtoAdapter) validateMTProtoTarget(ctx context.Context, remoteID string) error {
	remoteID = strings.TrimSpace(remoteID)
	parsed, err := strconv.ParseInt(remoteID, 10, 64)
	if err != nil || parsed >= 0 {
		return ErrUnsupportedTarget
	}
	groups, err := a.discoverMTProtoGroups(ctx)
	if err != nil {
		return ErrTargetValidationUnavailable
	}
	for _, group := range groups {
		if strings.TrimSpace(group.Peer.RemoteID) == remoteID {
			return nil
		}
	}
	return ErrUnsupportedTarget
}

func normalizeMTProtoTopics(topics []mtprotoTopic) []mtprotoTopic {
	byID := make(map[int]mtprotoTopic, len(topics)+1)
	for _, topic := range topics {
		if topic.ID <= 0 {
			continue
		}
		topic.Title = strings.TrimSpace(topic.Title)
		topic.General = topic.ID == 1
		byID[topic.ID] = topic
	}
	if _, ok := byID[1]; !ok {
		byID[1] = mtprotoTopic{ID: 1, General: true}
	}
	out := make([]mtprotoTopic, 0, len(byID))
	for _, topic := range byID {
		out = append(out, topic)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (a *MTProtoAdapter) DiscoverTopics(ctx context.Context, remoteID string) ([]DiscoveredTopic, error) {
	remoteID = strings.TrimSpace(remoteID)
	groups, err := a.discoverMTProtoGroups(ctx)
	if err != nil {
		return nil, err
	}
	var target *mtprotoGroup
	for i := range groups {
		if groups[i].Peer.RemoteID == remoteID {
			target = &groups[i]
			break
		}
	}
	if target == nil || !target.Forum || target.Peer.Kind != "channel" {
		return nil, ErrUnsupportedTarget
	}
	auth, err := a.waitAuth(ctx)
	if err != nil {
		return nil, err
	}
	client, ok := auth.(mtprotoTopicClient)
	if !ok {
		return nil, errors.New("full Telegram topic discovery is unavailable")
	}
	topics, err := client.ListForumTopics(ctx, target.Peer)
	if err != nil {
		return nil, errors.New("discover Telegram forum topics")
	}
	topics = normalizeMTProtoTopics(topics)
	if a.live != nil {
		a.live.replaceTopicLabels(target.Peer.RemoteID, topics)
	}
	out := make([]DiscoveredTopic, 0, len(topics))
	for _, topic := range topics {
		out = append(out, DiscoveredTopic{
			RemoteID: strconv.Itoa(topic.ID),
			Label:    strings.TrimSpace(topic.Title),
			General:  topic.General,
		})
	}
	return out, nil
}

func (c *gotdAuthClient) ListForumTopics(ctx context.Context, peer mtprotoPeerState) ([]mtprotoTopic, error) {
	input, err := peer.input()
	if err != nil {
		return nil, err
	}
	raw := tg.NewClient(c.client)
	offsetDate, offsetID, offsetTopic := 0, 0, 0
	topics := make([]mtprotoTopic, 0)
	seen := make(map[int]struct{})
	for {
		result, err := raw.MessagesGetForumTopics(ctx, &tg.MessagesGetForumTopicsRequest{
			Peer: input, OffsetDate: offsetDate, OffsetID: offsetID, OffsetTopic: offsetTopic, Limit: mtprotoForumTopicPageSize,
		})
		if err != nil {
			return nil, err
		}
		messageDates := make(map[int]int, len(result.Messages))
		for _, message := range result.Messages {
			if dated, ok := message.(interface {
				GetID() int
				GetDate() int
			}); ok {
				messageDates[dated.GetID()] = dated.GetDate()
			}
		}
		var last *tg.ForumTopic
		added := 0
		for _, class := range result.Topics {
			topic, ok := class.(*tg.ForumTopic)
			if !ok || topic.ID <= 0 {
				continue
			}
			last = topic
			if _, exists := seen[topic.ID]; exists {
				continue
			}
			seen[topic.ID] = struct{}{}
			topics = append(topics, mtprotoTopic{ID: topic.ID, Title: topic.Title, General: topic.ID == 1})
			added++
		}
		if last == nil || len(topics) >= result.Count || added == 0 {
			break
		}
		nextTopic := last.ID
		nextID := last.TopMessage
		nextDate := last.Date
		if !result.OrderByCreateDate {
			if date := messageDates[last.TopMessage]; date > 0 {
				nextDate = date
			}
		}
		if nextTopic == offsetTopic && nextID == offsetID && nextDate == offsetDate {
			break
		}
		offsetTopic, offsetID, offsetDate = nextTopic, nextID, nextDate
	}
	return normalizeMTProtoTopics(topics), nil
}
