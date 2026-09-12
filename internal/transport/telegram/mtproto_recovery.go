package telegram

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/vm75/message-sync/internal/transport"
)

const mtprotoHistoryPageSize = 100

var _ transport.RecoverySource = (*MTProtoAdapter)(nil)

type mtprotoHistoryPage struct {
	Messages []*tg.Message
	Entities tg.Entities
	More     bool
}

type mtprotoHistoryClient interface {
	History(context.Context, mtprotoPeerState, int, int, int) (mtprotoHistoryPage, error)
}

func (a *MTProtoAdapter) mtprotoRecoveryStreamKey(endpoint transport.EndpointID) string {
	return "telegram:mtproto:" + a.connectionID + ":" + string(endpoint)
}

func (a *MTProtoAdapter) RecoveryStream(endpoint transport.EndpointID) (string, bool) {
	if a == nil || a.live == nil || endpoint == "" {
		return "", false
	}
	a.live.mu.RLock()
	n := a.live.normalizer
	a.live.mu.RUnlock()
	if n == nil {
		return "", false
	}
	if _, ok := n.chatID(endpoint); !ok {
		return "", false
	}
	return a.mtprotoRecoveryStreamKey(endpoint), true
}

func (a *MTProtoAdapter) RecoveryStreams() []string {
	if a == nil || a.live == nil {
		return nil
	}
	a.live.mu.RLock()
	n := a.live.normalizer
	a.live.mu.RUnlock()
	if n == nil {
		return nil
	}
	aliases := make([]transport.EndpointID, 0, len(n.endpoints))
	for _, alias := range n.endpoints {
		aliases = append(aliases, alias)
	}
	sort.Slice(aliases, func(i, j int) bool { return aliases[i] < aliases[j] })
	out := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		out = append(out, a.mtprotoRecoveryStreamKey(alias))
	}
	return out
}

func (a *MTProtoAdapter) RecoverySignals() <-chan struct{} {
	if a == nil {
		return nil
	}
	return a.recoverySignals
}

func (a *MTProtoAdapter) signalMTProtoRecovery() {
	if a == nil || a.recoverySignals == nil {
		return
	}
	select {
	case a.recoverySignals <- struct{}{}:
	default:
	}
}

func (a *MTProtoAdapter) recoveryEndpoint(stream string) (transport.EndpointID, bool) {
	prefix := "telegram:mtproto:" + a.connectionID + ":"
	if !strings.HasPrefix(stream, prefix) {
		return "", false
	}
	alias := transport.EndpointID(strings.TrimPrefix(stream, prefix))
	_, ok := a.RecoveryStream(alias)
	return alias, ok
}

func (a *MTProtoAdapter) Recover(ctx context.Context, request transport.RecoveryRequest, emit func(context.Context, transport.Incoming) error) error {
	if a == nil || emit == nil {
		return errors.New("Telegram MTProto recovery is not initialized")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	endpoint, ok := a.recoveryEndpoint(strings.TrimSpace(request.Cursor.StreamKey))
	if !ok {
		return errors.New("Telegram MTProto recovery stream is not configured")
	}
	if request.MaxEvents <= 0 {
		return nil
	}
	maxEvents := request.MaxEvents
	if maxEvents > 1000 {
		maxEvents = 1000
	}
	peer, liveClient, err := a.resolveMTProtoPeer(ctx, endpoint)
	if err != nil {
		return errors.New("Telegram MTProto history is unavailable")
	}
	history, ok := liveClient.(mtprotoHistoryClient)
	if !ok {
		return errors.New("Telegram MTProto history is unavailable")
	}

	cutoff := time.Time{}
	if request.MaxAge > 0 {
		cutoff = time.Now().UTC().Add(-request.MaxAge)
	}
	cursor := int(request.Cursor.Position)
	offsetID := 0
	candidates := make([]transport.Incoming, 0, maxEvents)
	seen := make(map[int]struct{})
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := history.History(ctx, peer, cursor, offsetID, mtprotoHistoryPageSize)
		if err != nil {
			return errors.New("recover Telegram MTProto history")
		}
		if len(page.Messages) == 0 {
			break
		}
		reachedCutoff := false
		oldestID := 0
		for _, msg := range page.Messages {
			if msg == nil || msg.ID <= cursor {
				continue
			}
			if oldestID == 0 || msg.ID < oldestID {
				oldestID = msg.ID
			}
			if _, exists := seen[msg.ID]; exists {
				continue
			}
			seen[msg.ID] = struct{}{}
			ts := time.Unix(int64(msg.Date), 0).UTC()
			if !cutoff.IsZero() && ts.Before(cutoff) {
				reachedCutoff = true
				continue
			}
			incoming, ok := a.recoveredMTProtoIncoming(ctx, page.Entities, msg)
			if !ok {
				continue
			}
			incoming.Checkpoint = transport.Checkpoint{StreamKey: request.Cursor.StreamKey, Position: int64(msg.ID), EventTimestamp: incoming.Timestamp, Valid: true}
			candidates = append(candidates, incoming)
		}
		if reachedCutoff || !page.More || oldestID <= cursor || oldestID == offsetID {
			break
		}
		offsetID = oldestID
		// With no age bound and no prior cursor, do not turn a bounded request into an account export.
		if request.MaxAge <= 0 && cursor == 0 && len(candidates) >= maxEvents {
			break
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Timestamp.Equal(candidates[j].Timestamp) {
			return candidates[i].Checkpoint.Position < candidates[j].Checkpoint.Position
		}
		return candidates[i].Timestamp.Before(candidates[j].Timestamp)
	})
	if len(candidates) > maxEvents {
		candidates = candidates[:maxEvents]
	}
	for _, incoming := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := emit(ctx, incoming); err != nil {
			return err
		}
	}
	return nil
}

func (a *MTProtoAdapter) recoveredMTProtoIncoming(ctx context.Context, entities tg.Entities, msg *tg.Message) (transport.Incoming, bool) {
	if a == nil || a.live == nil || msg == nil {
		return transport.Incoming{}, false
	}
	peer, ok := mtprotoPeerFromEntities(msg.PeerID, entities)
	if !ok {
		return transport.Incoming{}, false
	}
	if peer.Kind == "channel" && peer.AccessHash == 0 {
		a.live.mu.RLock()
		cached := a.live.peers[peer.RemoteID]
		a.live.mu.RUnlock()
		peer.AccessHash = cached.AccessHash
	}
	if a.live.cachePeer(peer) {
		_ = a.persistMTProtoPeers(ctx)
	}
	normalizer, selfID, mediaEnabled, mediaMax := a.live.stateForNormalize()
	if normalizer == nil {
		return transport.Incoming{}, false
	}
	media, hasMedia := mtprotoMediaDescriptor(msg)
	synthetic, ok := mtprotoSyntheticMessage(msg, entities, selfID, hasMedia)
	if !ok {
		return transport.Incoming{}, false
	}
	incoming, ok := normalizer.NormalizeMessage(synthetic, 0)
	if !ok {
		return transport.Incoming{}, false
	}
	incoming.FromSelf = msg.Out
	topicID, _ := mtprotoTopicAndReply(msg)
	if incoming.ChildScope != nil && topicID > 0 {
		incoming.ChildScope.Label = a.live.topicLabel(peer.RemoteID, topicID)
	}
	if hasMedia {
		if !mediaEnabled {
			if strings.TrimSpace(incoming.Text) == "" {
				return transport.Incoming{}, false
			}
			incoming.Kind = "text"
			incoming.MediaLoader = nil
		} else {
			incoming.Kind = media.Kind
			ref := media
			incoming.MediaLoader = func(loadCtx context.Context) ([]byte, error) {
				if mediaMax > 0 && ref.Size > mediaMax {
					return nil, errMTProtoMediaTooLarge
				}
				return a.downloadMTProtoMedia(loadCtx, ref, mediaMax)
			}
		}
	}
	a.live.rememberMessage(incoming.Endpoint, msg.ID)
	return incoming, true
}

func (c *gotdAuthClient) History(ctx context.Context, peer mtprotoPeerState, minID, offsetID, limit int) (mtprotoHistoryPage, error) {
	input, err := peer.input()
	if err != nil {
		return mtprotoHistoryPage{}, err
	}
	if limit <= 0 || limit > mtprotoHistoryPageSize {
		limit = mtprotoHistoryPageSize
	}
	result, err := tg.NewClient(c.client).MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: input, OffsetID: offsetID, Limit: limit, MinID: minID})
	if err != nil {
		return mtprotoHistoryPage{}, err
	}
	modified, ok := result.AsModified()
	if !ok {
		return mtprotoHistoryPage{}, nil
	}
	entities := tg.Entities{Users: map[int64]*tg.User{}, Chats: map[int64]*tg.Chat{}, Channels: map[int64]*tg.Channel{}}
	for _, u := range modified.GetUsers() {
		if v, ok := u.(*tg.User); ok {
			entities.Users[v.ID] = v
		}
	}
	for _, ch := range modified.GetChats() {
		switch v := ch.(type) {
		case *tg.Chat:
			entities.Chats[v.ID] = v
		case *tg.Channel:
			entities.Channels[v.ID] = v
		}
	}
	messages := make([]*tg.Message, 0, len(modified.GetMessages()))
	for _, class := range modified.GetMessages() {
		if msg, ok := class.(*tg.Message); ok {
			messages = append(messages, msg)
		}
	}
	return mtprotoHistoryPage{Messages: messages, Entities: entities, More: len(modified.GetMessages()) >= limit}, nil
}

func mtprotoMessageID(s string) int { v, _ := strconv.Atoi(strings.TrimSpace(s)); return v }
