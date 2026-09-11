package telegram

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	telegrambotmodels "github.com/go-telegram/bot/models"
	gotdcrypto "github.com/gotd/td/crypto"
	gotdsession "github.com/gotd/td/session"
	gotdtelegram "github.com/gotd/td/telegram"
	gotddownloader "github.com/gotd/td/telegram/downloader"
	gotdmessage "github.com/gotd/td/telegram/message"
	gotdunpack "github.com/gotd/td/telegram/message/unpack"
	gotddialogs "github.com/gotd/td/telegram/query/dialogs"
	gotduploader "github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
)

const (
	mtprotoChannelRemoteBase = int64(1000000000000)
	mtprotoMessageCacheLimit = 8192
	mtprotoReactionPageSize  = 100
	mtprotoReactionMaxPeers  = 1000
)

var errMTProtoMediaTooLarge = errors.New("Telegram MTProto media exceeds configured size limit")

type mtprotoPeerState struct {
	RemoteID   string `json:"remoteId"`
	Kind       string `json:"kind"`
	ID         int64  `json:"id"`
	AccessHash int64  `json:"accessHash,omitempty"`
}

func (p mtprotoPeerState) input() (tg.InputPeerClass, error) {
	switch p.Kind {
	case "chat":
		if p.ID <= 0 {
			return nil, errors.New("invalid Telegram MTProto chat peer")
		}
		return &tg.InputPeerChat{ChatID: p.ID}, nil
	case "channel":
		if p.ID <= 0 || p.AccessHash == 0 {
			return nil, errors.New("Telegram MTProto channel peer is unresolved")
		}
		return &tg.InputPeerChannel{ChannelID: p.ID, AccessHash: p.AccessHash}, nil
	default:
		return nil, errors.New("unsupported Telegram MTProto peer")
	}
}

type mtprotoGroup struct {
	Peer     mtprotoPeerState
	Title    string
	Username string
	Forum    bool
}

type mtprotoMediaRef struct {
	Kind     string
	Location tg.InputFileLocationClass
	Size     uint64
}

type mtprotoReactionState struct {
	UserID      int64
	Emoji       string
	DisplayName string
	Date        int
	My          bool
}

type mtprotoLiveClient interface {
	SelfID(context.Context) (int64, error)
	ListGroups(context.Context) ([]mtprotoGroup, error)
	SendText(context.Context, mtprotoPeerState, string, int, int) (int, error)
	SendMedia(context.Context, mtprotoPeerState, string, []byte, string, int, int) (int, error)
	React(context.Context, mtprotoPeerState, int, string) error
	Edit(context.Context, mtprotoPeerState, int, string) error
	Delete(context.Context, mtprotoPeerState, int) error
	DownloadMedia(context.Context, mtprotoMediaRef, uint64) ([]byte, error)
	ReactionSnapshot(context.Context, mtprotoPeerState, int) ([]mtprotoReactionState, error)
}

type mtprotoLiveState struct {
	mu sync.RWMutex

	connectionID  string
	hasher        *identity.Hasher
	usernameMode  config.UsernameMode
	normalizer    *Normalizer
	mediaEnabled  bool
	mediaMaxBytes uint64

	selfID int64
	peers  map[string]mtprotoPeerState

	messageEndpoints map[int]transport.EndpointID
	messageOrder     []int
	reactions        map[string]map[int64]mtprotoReactionState
	pendingSends     map[string]int
	pendingMutations map[string]int
}

func newMTProtoLiveState(opts Options) (*mtprotoLiveState, error) {
	l := &mtprotoLiveState{
		connectionID:     strings.TrimSpace(opts.ConnectionID),
		hasher:           opts.Hasher,
		usernameMode:     opts.UsernameMode,
		mediaEnabled:     opts.MediaEnabled,
		mediaMaxBytes:    opts.MediaMaxBytes,
		peers:            make(map[string]mtprotoPeerState),
		messageEndpoints: make(map[int]transport.EndpointID),
		reactions:        make(map[string]map[int64]mtprotoReactionState),
		pendingSends:     make(map[string]int),
		pendingMutations: make(map[string]int),
	}
	if len(opts.ChatIDs) > 0 {
		if opts.Hasher == nil {
			return nil, errors.New("identity hasher is required for Telegram MTProto endpoints")
		}
		if !opts.UsernameMode.IsValid() {
			return nil, errors.New("username mode must be push_name or hash")
		}
		normalizer, err := NewNormalizerWithConnection(opts.ChatIDs, opts.Hasher, opts.UsernameMode, opts.ConnectionID)
		if err != nil {
			return nil, err
		}
		l.normalizer = normalizer
	}
	return l, nil
}

func (l *mtprotoLiveState) updateConfig(cfg *config.Config, connectionID string) error {
	if l == nil {
		return errors.New("Telegram MTProto live state is unavailable")
	}
	if cfg == nil {
		return errors.New("config is required")
	}
	chatIDs := make(map[string]string)
	for alias, endpoint := range cfg.Endpoints {
		if endpoint.Transport == config.TransportTelegram && endpoint.ConnectionID == connectionID {
			chatIDs[alias] = endpoint.RemoteID
		}
	}
	if len(chatIDs) > 0 && l.hasher == nil {
		return errors.New("identity hasher is required for Telegram MTProto endpoints")
	}
	var normalizer *Normalizer
	var err error
	if l.hasher != nil {
		normalizer, err = NewNormalizerWithConnection(chatIDs, l.hasher, cfg.Identity.UsernameMode, connectionID)
		if err != nil {
			return err
		}
	}
	l.mu.Lock()
	l.normalizer = normalizer
	l.usernameMode = cfg.Identity.UsernameMode
	l.mediaEnabled = cfg.Media.Enabled
	l.mediaMaxBytes = uint64(cfg.Media.MaxSizeMB) * 1024 * 1024
	l.mu.Unlock()
	return nil
}

func (l *mtprotoLiveState) loadPeers(peers map[string]mtprotoPeerState) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.peers == nil {
		l.peers = make(map[string]mtprotoPeerState)
	}
	for remoteID, peer := range peers {
		if strings.TrimSpace(remoteID) == "" || peer.ID <= 0 {
			continue
		}
		peer.RemoteID = remoteID
		l.peers[remoteID] = peer
	}
}

func (l *mtprotoLiveState) peerSnapshot() map[string]mtprotoPeerState {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make(map[string]mtprotoPeerState, len(l.peers))
	for k, v := range l.peers {
		out[k] = v
	}
	return out
}

func (l *mtprotoLiveState) replacePeers(groups []mtprotoGroup) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	next := make(map[string]mtprotoPeerState, len(groups))
	for _, group := range groups {
		if group.Peer.RemoteID != "" {
			next[group.Peer.RemoteID] = group.Peer
		}
	}
	changed := len(next) != len(l.peers)
	if !changed {
		for k, v := range next {
			if l.peers[k] != v {
				changed = true
				break
			}
		}
	}
	if changed {
		l.peers = next
	}
	return changed
}

func (l *mtprotoLiveState) cachePeer(peer mtprotoPeerState) bool {
	if l == nil || peer.RemoteID == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	old, ok := l.peers[peer.RemoteID]
	if ok && old == peer {
		return false
	}
	l.peers[peer.RemoteID] = peer
	return true
}

func (l *mtprotoLiveState) setSelfID(id int64) {
	l.mu.Lock()
	l.selfID = id
	l.mu.Unlock()
}

func (l *mtprotoLiveState) stateForNormalize() (*Normalizer, int64, bool, uint64) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.normalizer, l.selfID, l.mediaEnabled, l.mediaMaxBytes
}

func (l *mtprotoLiveState) rememberMessage(endpoint transport.EndpointID, id int) {
	if l == nil || endpoint == "" || id <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.messageEndpoints[id]; !ok {
		l.messageOrder = append(l.messageOrder, id)
	}
	l.messageEndpoints[id] = endpoint
	for len(l.messageOrder) > mtprotoMessageCacheLimit {
		old := l.messageOrder[0]
		l.messageOrder = l.messageOrder[1:]
		delete(l.messageEndpoints, old)
	}
}

func (l *mtprotoLiveState) endpointForMessage(id int) (transport.EndpointID, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	ep, ok := l.messageEndpoints[id]
	return ep, ok
}

func mtprotoPendingSendKey(endpoint transport.EndpointID, kind string, topic int, text string) string {
	return string(endpoint) + "\x00" + strings.TrimSpace(kind) + "\x00" + strconv.Itoa(topic) + "\x00" + text
}

func (l *mtprotoLiveState) markPendingSend(endpoint transport.EndpointID, kind string, topic int, text string) func(bool) {
	key := mtprotoPendingSendKey(endpoint, kind, topic, text)
	l.mu.Lock()
	l.pendingSends[key]++
	l.mu.Unlock()
	return func(success bool) {
		if success {
			return
		}
		l.mu.Lock()
		if l.pendingSends[key] <= 1 {
			delete(l.pendingSends, key)
		} else {
			l.pendingSends[key]--
		}
		l.mu.Unlock()
	}
}

func (l *mtprotoLiveState) consumePendingSend(endpoint transport.EndpointID, kind string, topic int, text string) bool {
	key := mtprotoPendingSendKey(endpoint, kind, topic, text)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pendingSends[key] <= 0 {
		return false
	}
	if l.pendingSends[key] == 1 {
		delete(l.pendingSends, key)
	} else {
		l.pendingSends[key]--
	}
	return true
}

func mtprotoMutationKey(kind string, endpoint transport.EndpointID, messageID int) string {
	return kind + "\x00" + string(endpoint) + "\x00" + strconv.Itoa(messageID)
}

func (l *mtprotoLiveState) markPendingMutation(kind string, endpoint transport.EndpointID, messageID int) func(bool) {
	key := mtprotoMutationKey(kind, endpoint, messageID)
	l.mu.Lock()
	l.pendingMutations[key]++
	l.mu.Unlock()
	return func(success bool) {
		if success {
			return
		}
		l.mu.Lock()
		if l.pendingMutations[key] <= 1 {
			delete(l.pendingMutations, key)
		} else {
			l.pendingMutations[key]--
		}
		l.mu.Unlock()
	}
}

func (l *mtprotoLiveState) consumePendingMutation(kind string, endpoint transport.EndpointID, messageID int) bool {
	key := mtprotoMutationKey(kind, endpoint, messageID)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pendingMutations[key] <= 0 {
		return false
	}
	if l.pendingMutations[key] == 1 {
		delete(l.pendingMutations, key)
	} else {
		l.pendingMutations[key]--
	}
	return true
}

func (a *MTProtoAdapter) initializeMTProtoLive(ctx context.Context, auth mtprotoAuthClient) {
	if a == nil || a.live == nil {
		return
	}
	client, ok := auth.(mtprotoLiveClient)
	if !ok {
		return
	}
	if selfID, err := client.SelfID(ctx); err == nil && selfID > 0 {
		a.live.setSelfID(selfID)
	}
	groups, err := client.ListGroups(ctx)
	if err != nil {
		return
	}
	if a.live.replacePeers(groups) {
		_ = a.persistMTProtoPeers(ctx)
	}
	a.signalMTProtoRecovery()
}

func (a *MTProtoAdapter) persistMTProtoPeers(ctx context.Context) error {
	if a == nil || a.state == nil || a.live == nil {
		return nil
	}
	peers := a.live.peerSnapshot()
	return a.state.update(ctx, func(state *mtprotoState) {
		state.Peers = peers
	})
}

func (a *MTProtoAdapter) refreshMTProtoPeers(ctx context.Context, client mtprotoLiveClient) error {
	groups, err := client.ListGroups(ctx)
	if err != nil {
		return errors.New("refresh Telegram MTProto peers")
	}
	if a.live.replacePeers(groups) {
		if err := a.persistMTProtoPeers(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (a *MTProtoAdapter) resolveMTProtoPeer(ctx context.Context, endpoint transport.EndpointID) (mtprotoPeerState, mtprotoLiveClient, error) {
	if a == nil || a.live == nil {
		return mtprotoPeerState{}, nil, errors.New("Telegram MTProto transport is not initialized")
	}
	auth, err := a.waitAuth(ctx)
	if err != nil {
		return mtprotoPeerState{}, nil, err
	}
	client, ok := auth.(mtprotoLiveClient)
	if !ok {
		return mtprotoPeerState{}, nil, errors.New("Telegram MTProto live client is unavailable")
	}
	a.live.mu.RLock()
	normalizer := a.live.normalizer
	a.live.mu.RUnlock()
	if normalizer == nil {
		return mtprotoPeerState{}, nil, errors.New("Telegram MTProto endpoint configuration is unavailable")
	}
	remote, ok := normalizer.chatID(endpoint)
	if !ok {
		return mtprotoPeerState{}, nil, errors.New("unknown Telegram endpoint")
	}
	remoteID := strconv.FormatInt(remote, 10)
	a.live.mu.RLock()
	peer, found := a.live.peers[remoteID]
	a.live.mu.RUnlock()
	if found {
		if _, inputErr := peer.input(); inputErr == nil {
			return peer, client, nil
		}
	}
	if peer, ok := mtprotoBasicPeerFromRemote(remote); ok {
		a.live.cachePeer(peer)
		return peer, client, nil
	}
	if err := a.refreshMTProtoPeers(ctx, client); err != nil {
		return mtprotoPeerState{}, nil, errors.New("Telegram MTProto peer is unresolved")
	}
	a.live.mu.RLock()
	peer, found = a.live.peers[remoteID]
	a.live.mu.RUnlock()
	if !found {
		return mtprotoPeerState{}, nil, errors.New("Telegram MTProto peer is unresolved")
	}
	if _, err := peer.input(); err != nil {
		return mtprotoPeerState{}, nil, err
	}
	return peer, client, nil
}

func mtprotoBasicPeerFromRemote(remote int64) (mtprotoPeerState, bool) {
	if remote >= 0 || remote <= -mtprotoChannelRemoteBase {
		return mtprotoPeerState{}, false
	}
	id := -remote
	return mtprotoPeerState{RemoteID: strconv.FormatInt(remote, 10), Kind: "chat", ID: id}, true
}

func mtprotoRemoteIDFromPeer(peer tg.PeerClass) (int64, string, bool) {
	switch p := peer.(type) {
	case *tg.PeerChat:
		if p.ChatID <= 0 {
			return 0, "", false
		}
		remote := -p.ChatID
		return remote, "chat", true
	case *tg.PeerChannel:
		if p.ChannelID <= 0 {
			return 0, "", false
		}
		remote := -(mtprotoChannelRemoteBase + p.ChannelID)
		return remote, "channel", true
	default:
		return 0, "", false
	}
}

func mtprotoPeerFromEntities(peer tg.PeerClass, entities tg.Entities) (mtprotoPeerState, bool) {
	remote, kind, ok := mtprotoRemoteIDFromPeer(peer)
	if !ok {
		return mtprotoPeerState{}, false
	}
	state := mtprotoPeerState{RemoteID: strconv.FormatInt(remote, 10), Kind: kind}
	switch p := peer.(type) {
	case *tg.PeerChat:
		state.ID = p.ChatID
	case *tg.PeerChannel:
		state.ID = p.ChannelID
		if channel := entities.Channels[p.ChannelID]; channel != nil {
			state.AccessHash = channel.AccessHash
		}
	}
	return state, true
}

func mtprotoTopicAndReply(msg *tg.Message) (topicID, replyID int) {
	if msg == nil {
		return 0, 0
	}
	header, ok := msg.ReplyTo.(*tg.MessageReplyHeader)
	if !ok || header == nil {
		return 0, 0
	}
	replyID = header.ReplyToMsgID
	topicID = header.ReplyToTopID
	if topicID == 0 && header.ForumTopic {
		topicID = header.ReplyToMsgID
	}
	return topicID, replyID
}

func mtprotoUserFromMessage(msg *tg.Message, entities tg.Entities, selfID int64) (*telegrambotmodels.User, bool) {
	if msg == nil {
		return nil, false
	}
	var id int64
	if peer, ok := msg.FromID.(*tg.PeerUser); ok {
		id = peer.UserID
	} else if msg.Out {
		id = selfID
	}
	if id <= 0 {
		return nil, false
	}
	out := &telegrambotmodels.User{ID: id}
	if user := entities.Users[id]; user != nil {
		out.FirstName = user.FirstName
		out.LastName = user.LastName
		out.Username = user.Username
	}
	return out, true
}

func mtprotoSyntheticMessage(msg *tg.Message, entities tg.Entities, selfID int64, media bool) (*telegrambotmodels.Message, bool) {
	if msg == nil || msg.ID <= 0 {
		return nil, false
	}
	remote, kind, ok := mtprotoRemoteIDFromPeer(msg.PeerID)
	if !ok {
		return nil, false
	}
	from, ok := mtprotoUserFromMessage(msg, entities, selfID)
	if !ok {
		return nil, false
	}
	chatType := telegrambotmodels.ChatTypeGroup
	if kind == "channel" {
		chatType = telegrambotmodels.ChatTypeSupergroup
	}
	synthetic := &telegrambotmodels.Message{
		ID:   msg.ID,
		Date: msg.Date,
		Chat: telegrambotmodels.Chat{ID: remote, Type: chatType},
		From: from,
		Text: msg.Message,
	}
	topicID, replyID := mtprotoTopicAndReply(msg)
	if topicID > 0 {
		synthetic.IsTopicMessage = true
		synthetic.MessageThreadID = topicID
	}
	if replyID > 0 {
		synthetic.ReplyToMessage = &telegrambotmodels.Message{
			ID:   replyID,
			Chat: telegrambotmodels.Chat{ID: remote, Type: chatType},
		}
	}
	if pollMedia, pollOK := msg.Media.(*tg.MessageMediaPoll); pollOK {
		poll, ok := mtprotoSyntheticPoll(pollMedia)
		if !ok {
			return nil, false
		}
		synthetic.Poll = poll
	} else if media {
		synthetic.Document = &telegrambotmodels.Document{FileID: "mtproto"}
	}
	return synthetic, true
}

func mtprotoMediaDescriptor(msg *tg.Message) (mtprotoMediaRef, bool) {
	if msg == nil || msg.Media == nil {
		return mtprotoMediaRef{}, false
	}
	switch media := msg.Media.(type) {
	case *tg.MessageMediaPhoto:
		photo, ok := media.Photo.(*tg.Photo)
		if !ok || photo == nil || len(photo.Sizes) == 0 {
			return mtprotoMediaRef{}, false
		}
		thumb := photo.Sizes[len(photo.Sizes)-1].GetType()
		if strings.TrimSpace(thumb) == "" {
			return mtprotoMediaRef{}, false
		}
		return mtprotoMediaRef{
			Kind: "image",
			Location: &tg.InputPhotoFileLocation{
				ID: photo.ID, AccessHash: photo.AccessHash, FileReference: photo.FileReference, ThumbSize: thumb,
			},
		}, true
	case *tg.MessageMediaDocument:
		doc, ok := media.Document.(*tg.Document)
		if !ok || doc == nil {
			return mtprotoMediaRef{}, false
		}
		kind := "document"
		for _, attr := range doc.Attributes {
			switch attr.(type) {
			case *tg.DocumentAttributeSticker:
				kind = "sticker"
			case *tg.DocumentAttributeVideo:
				kind = "video"
			case *tg.DocumentAttributeAudio:
				kind = "audio"
			}
		}
		if kind == "document" && strings.EqualFold(strings.TrimSpace(doc.MimeType), "video/mp4") {
			for _, attr := range doc.Attributes {
				if _, animated := attr.(*tg.DocumentAttributeAnimated); animated {
					kind = "video"
					break
				}
			}
		}
		size := uint64(0)
		if doc.Size > 0 {
			size = uint64(doc.Size)
		}
		return mtprotoMediaRef{
			Kind: kind,
			Size: size,
			Location: &tg.InputDocumentFileLocation{
				ID: doc.ID, AccessHash: doc.AccessHash, FileReference: doc.FileReference,
			},
		}, true
	default:
		return mtprotoMediaRef{}, false
	}
}

func (a *MTProtoAdapter) normalizeMTProtoMessage(ctx context.Context, entities tg.Entities, msg *tg.Message, edit bool) {
	if a == nil || a.live == nil || msg == nil {
		return
	}
	peer, peerOK := mtprotoPeerFromEntities(msg.PeerID, entities)
	if !peerOK {
		return // private DMs and unsupported peers are intentionally ignored.
	}
	if peer.Kind == "channel" && peer.AccessHash == 0 {
		a.live.mu.RLock()
		cached := a.live.peers[peer.RemoteID]
		a.live.mu.RUnlock()
		if cached.AccessHash != 0 {
			peer.AccessHash = cached.AccessHash
		}
	}
	if a.live.cachePeer(peer) {
		_ = a.persistMTProtoPeers(ctx)
	}
	normalizer, selfID, mediaEnabled, mediaMax := a.live.stateForNormalize()
	if normalizer == nil {
		return
	}
	media, hasMedia := mtprotoMediaDescriptor(msg)
	synthetic, ok := mtprotoSyntheticMessage(msg, entities, selfID, hasMedia)
	if !ok {
		return
	}
	var incoming transport.Incoming
	if edit {
		incoming, ok = normalizer.NormalizeEditedMessage(synthetic, 0)
	} else {
		incoming, ok = normalizer.NormalizeMessage(synthetic, 0)
	}
	if !ok {
		return
	}
	incoming.FromSelf = msg.Out
	if hasMedia && !edit {
		if !mediaEnabled {
			if strings.TrimSpace(incoming.Text) == "" {
				return
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
	topicID := 0
	if incoming.ChildScope != nil {
		topicID, _ = strconv.Atoi(incoming.ChildScope.RemoteID)
	}
	if !edit && incoming.Kind == "poll" {
		if pollMedia, pollOK := msg.Media.(*tg.MessageMediaPoll); pollOK {
			_ = a.rememberMTProtoPoll(ctx, pollMedia.Poll.ID, mtprotoPollState{
				RemoteID: peer.RemoteID, MessageID: msg.ID, TopicID: topicID, OptionKeys: mtprotoPollOptionKeys(pollMedia.Poll),
			})
		}
	}
	if incoming.FromSelf && !edit && a.live.consumePendingSend(incoming.Endpoint, incoming.Kind, topicID, incoming.Text) {
		return
	}
	if edit && a.live.consumePendingMutation("edit", incoming.Endpoint, msg.ID) {
		return
	}
	a.live.rememberMessage(incoming.Endpoint, msg.ID)
	if !edit {
		incoming.Checkpoint = transport.Checkpoint{
			StreamKey: a.mtprotoRecoveryStreamKey(incoming.Endpoint), Position: int64(msg.ID), EventTimestamp: incoming.Timestamp, Valid: true,
		}
	}
	a.emitMTProto(incoming)
}

func (a *MTProtoAdapter) emitMTProto(incoming transport.Incoming) {
	if a == nil || a.events == nil {
		return
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return
	}
	select {
	case a.events <- incoming:
	default:
		// Match the existing Telegram adapter's bounded ingress behavior without
		// logging message content or raw provider updates.
	}
}

func (a *MTProtoAdapter) handleMTProtoUpdates(ctx context.Context, updates tg.UpdatesClass) error {
	if a == nil || updates == nil {
		return nil
	}
	dispatcher := tg.NewUpdateDispatcher()
	dispatcher.OnNewMessage(func(handlerCtx context.Context, entities tg.Entities, update *tg.UpdateNewMessage) error {
		if msg, ok := update.Message.(*tg.Message); ok {
			a.normalizeMTProtoMessage(handlerCtx, entities, msg, false)
		}
		return nil
	})
	dispatcher.OnNewChannelMessage(func(handlerCtx context.Context, entities tg.Entities, update *tg.UpdateNewChannelMessage) error {
		if msg, ok := update.Message.(*tg.Message); ok {
			a.normalizeMTProtoMessage(handlerCtx, entities, msg, false)
		}
		return nil
	})
	dispatcher.OnEditMessage(func(handlerCtx context.Context, entities tg.Entities, update *tg.UpdateEditMessage) error {
		if msg, ok := update.Message.(*tg.Message); ok {
			a.normalizeMTProtoMessage(handlerCtx, entities, msg, true)
		}
		return nil
	})
	dispatcher.OnEditChannelMessage(func(handlerCtx context.Context, entities tg.Entities, update *tg.UpdateEditChannelMessage) error {
		if msg, ok := update.Message.(*tg.Message); ok {
			a.normalizeMTProtoMessage(handlerCtx, entities, msg, true)
		}
		return nil
	})
	dispatcher.OnDeleteMessages(func(handlerCtx context.Context, _ tg.Entities, update *tg.UpdateDeleteMessages) error {
		for _, id := range update.Messages {
			endpoint, ok := a.live.endpointForMessage(id)
			if !ok || a.live.consumePendingMutation("delete", endpoint, id) {
				continue
			}
			now := time.Now().UTC()
			a.emitMTProto(transport.Incoming{
				Endpoint: endpoint, RemoteID: strconv.Itoa(id), Kind: "delete", Timestamp: now,
				ReplyTo: &transport.MessageRef{Endpoint: endpoint, RemoteMessageID: strconv.Itoa(id)},
			})
		}
		return nil
	})
	dispatcher.OnDeleteChannelMessages(func(handlerCtx context.Context, _ tg.Entities, update *tg.UpdateDeleteChannelMessages) error {
		remote := -(mtprotoChannelRemoteBase + update.ChannelID)
		a.live.mu.RLock()
		normalizer := a.live.normalizer
		a.live.mu.RUnlock()
		if normalizer == nil {
			return nil
		}
		endpoint, ok := normalizer.endpoint(remote)
		if !ok {
			return nil
		}
		for _, id := range update.Messages {
			if a.live.consumePendingMutation("delete", endpoint, id) {
				continue
			}
			now := time.Now().UTC()
			a.emitMTProto(transport.Incoming{
				Endpoint: endpoint, RemoteID: strconv.Itoa(id), Kind: "delete", Timestamp: now,
				ReplyTo: &transport.MessageRef{Endpoint: endpoint, RemoteMessageID: strconv.Itoa(id)},
			})
		}
		return nil
	})
	dispatcher.OnMessageReactions(func(handlerCtx context.Context, _ tg.Entities, update *tg.UpdateMessageReactions) error {
		a.handleMTProtoReactionUpdate(handlerCtx, update)
		return nil
	})
	dispatcher.OnMessagePoll(func(handlerCtx context.Context, _ tg.Entities, update *tg.UpdateMessagePoll) error {
		a.handleMTProtoPollUpdate(handlerCtx, update)
		return nil
	})
	return dispatcher.Handle(ctx, updates)
}

func (a *MTProtoAdapter) handleMTProtoReactionUpdate(ctx context.Context, update *tg.UpdateMessageReactions) {
	if a == nil || a.live == nil || update == nil || update.MsgID <= 0 {
		return
	}
	remote, _, ok := mtprotoRemoteIDFromPeer(update.Peer)
	if !ok {
		return
	}
	a.live.mu.RLock()
	normalizer := a.live.normalizer
	selfID := a.live.selfID
	a.live.mu.RUnlock()
	if normalizer == nil {
		return
	}
	endpoint, ok := normalizer.endpoint(remote)
	if !ok {
		return
	}
	peer, client, err := a.resolveMTProtoPeer(ctx, endpoint)
	if err != nil {
		return
	}
	snapshot, err := client.ReactionSnapshot(ctx, peer, update.MsgID)
	if err != nil {
		return
	}
	key := string(endpoint) + "\x00" + strconv.Itoa(update.MsgID)
	events, hadBaseline := a.live.replaceReactionSnapshot(key, snapshot)
	if !hadBaseline {
		latest, found := latestMTProtoReaction(update.Reactions.RecentReactions)
		if !found {
			return
		}
		for _, current := range snapshot {
			if current.UserID == latest.UserID && current.Emoji == latest.Emoji {
				events = []mtprotoReactionState{current}
				break
			}
		}
	}
	topicID := update.TopMsgID
	for _, event := range events {
		if event.UserID <= 0 {
			continue
		}
		timestamp := time.Now().UTC()
		if event.Date > 0 {
			timestamp = time.Unix(int64(event.Date), 0).UTC()
		}
		var child *transport.ChildScope
		if topicID > 0 {
			child = &transport.ChildScope{Kind: transport.ScopeKindTelegramTopic, RemoteID: strconv.Itoa(topicID)}
		}
		displayName := ""
		if a.live.usernameMode == config.UsernameModePushName {
			displayName = event.DisplayName
		}
		a.emitMTProto(transport.Incoming{
			Endpoint:   endpoint,
			RemoteID:   strconv.Itoa(update.MsgID),
			Sender:     transport.Sender{DisplayName: displayName, OpaqueID: telegramActorID(a.live.hasher, event.UserID)},
			FromSelf:   event.My || event.UserID == selfID,
			Kind:       "reaction",
			Text:       event.Emoji,
			ReplyTo:    &transport.MessageRef{Endpoint: endpoint, RemoteMessageID: strconv.Itoa(update.MsgID), ChildScope: child},
			ChildScope: child,
			Timestamp:  timestamp,
		})
	}
}

func (l *mtprotoLiveState) replaceReactionSnapshot(key string, snapshot []mtprotoReactionState) ([]mtprotoReactionState, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	old, had := l.reactions[key]
	next := make(map[int64]mtprotoReactionState, len(snapshot))
	for _, item := range snapshot {
		if item.UserID > 0 {
			next[item.UserID] = item
		}
	}
	l.reactions[key] = next
	if !had {
		return nil, false
	}
	var events []mtprotoReactionState
	for id, current := range next {
		previous, ok := old[id]
		if !ok || previous.Emoji != current.Emoji {
			events = append(events, current)
		}
	}
	for id, previous := range old {
		if _, ok := next[id]; !ok {
			previous.Emoji = ""
			previous.Date = 0
			events = append(events, previous)
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].UserID < events[j].UserID })
	return events, true
}

func latestMTProtoReaction(recent []tg.MessagePeerReaction) (mtprotoReactionState, bool) {
	var best mtprotoReactionState
	found := false
	for _, item := range recent {
		peer, ok := item.PeerID.(*tg.PeerUser)
		if !ok || peer.UserID <= 0 {
			continue
		}
		emoji, ok := mtprotoReactionEmoji(item.Reaction)
		if !ok {
			continue
		}
		if !found || item.Date >= best.Date {
			best = mtprotoReactionState{UserID: peer.UserID, Emoji: emoji, Date: item.Date, My: item.My}
			found = true
		}
	}
	return best, found
}

func mtprotoReactionEmoji(reaction tg.ReactionClass) (string, bool) {
	switch r := reaction.(type) {
	case *tg.ReactionEmoji:
		emoji := strings.TrimSpace(r.Emoticon)
		return emoji, emoji != ""
	default:
		return "", false
	}
}

func (a *MTProtoAdapter) downloadMTProtoMedia(ctx context.Context, ref mtprotoMediaRef, max uint64) ([]byte, error) {
	auth, err := a.waitAuth(ctx)
	if err != nil {
		return nil, err
	}
	client, ok := auth.(mtprotoLiveClient)
	if !ok {
		return nil, errors.New("Telegram MTProto live client is unavailable")
	}
	data, err := client.DownloadMedia(ctx, ref, max)
	if err != nil {
		if errors.Is(err, errMTProtoMediaTooLarge) {
			return nil, errMTProtoMediaTooLarge
		}
		return nil, errors.New("download Telegram MTProto media")
	}
	return data, nil
}

func (a *MTProtoAdapter) sendMTProto(ctx context.Context, outgoing transport.Outgoing) (transport.MessageRef, error) {
	if a == nil || a.live == nil {
		return transport.MessageRef{}, errors.New("Telegram MTProto transport is not initialized")
	}
	if ctx == nil {
		return transport.MessageRef{}, errors.New("context is required")
	}
	if outgoing.AttributionOnly {
		return transport.MessageRef{Endpoint: outgoing.Endpoint}, nil
	}
	peer, client, err := a.resolveMTProtoPeer(ctx, outgoing.Endpoint)
	if err != nil {
		return transport.MessageRef{}, err
	}
	topicID, err := telegramChildThreadID(outgoing.ChildScope)
	if err != nil {
		return transport.MessageRef{}, err
	}
	replyID := 0
	if outgoing.ReplyTo != nil && strings.TrimSpace(outgoing.ReplyTo.RemoteMessageID) != "" {
		replyID, err = strconv.Atoi(strings.TrimSpace(outgoing.ReplyTo.RemoteMessageID))
		if err != nil || replyID <= 0 {
			return transport.MessageRef{}, errors.New("invalid Telegram reply message id")
		}
	}
	kind := strings.TrimSpace(outgoing.Kind)
	if kind == "" {
		kind = "text"
	}
	if kind == "poll" {
		pollClient, supported := client.(mtprotoPollClient)
		if supported {
			question, options, selectable, duration, native := mtprotoNativePoll(outgoing)
			if native {
				done := a.live.markPendingSend(outgoing.Endpoint, "poll", topicID, question)
				result, sendErr := pollClient.SendPoll(ctx, peer, question, options, selectable, duration, replyID, topicID)
				done(sendErr == nil)
				if sendErr != nil {
					return transport.MessageRef{}, errors.New("send Telegram MTProto poll")
				}
				if result.MessageID <= 0 || result.PollID == 0 {
					return transport.MessageRef{}, errors.New("Telegram MTProto poll response was incomplete")
				}
				_ = a.rememberMTProtoPoll(ctx, result.PollID, mtprotoPollState{
					RemoteID: peer.RemoteID, MessageID: result.MessageID, TopicID: topicID, OptionKeys: result.OptionKeys,
				})
				a.live.rememberMessage(outgoing.Endpoint, result.MessageID)
				return transport.MessageRef{
					Endpoint: outgoing.Endpoint, RemoteMessageID: strconv.Itoa(result.MessageID), IsTargetFromMe: true,
					Provider: a.mtprotoPollProviderNamespace(), ProviderReference: strconv.FormatInt(result.PollID, 10), ChildScope: outgoing.ChildScope,
				}, nil
			}
		}
		pollText, pollErr := telegramPollText(outgoing.SourceText, outgoing.PollOptions, outgoing.PollSelectableCount)
		if pollErr != nil {
			return transport.MessageRef{}, pollErr
		}
		outgoing.SourceText = pollText
		if outgoing.RenderedText != "" {
			if attribution := strings.TrimSpace(outgoing.PollAttribution); attribution != "" {
				outgoing.RenderedText = attribution + " " + pollText
			} else {
				outgoing.RenderedText = pollText
			}
		}
		kind = "text"
	}
	content := telegramOutgoingText(outgoing)
	if outgoing.ReplyFallback {
		content = telegramReplyFallback(outgoing.OriginEndpoint, outgoing.QuotedText, content)
	}
	var id int
	switch kind {
	case "text":
		content = truncateTelegramText(content, telegramTextLimit)
		if strings.TrimSpace(content) == "" {
			return transport.MessageRef{}, errors.New("outgoing Telegram text is required")
		}
		done := a.live.markPendingSend(outgoing.Endpoint, kind, topicID, content)
		id, err = client.SendText(ctx, peer, content, replyID, topicID)
		done(err == nil)
	case "image", "video", "audio", "document", "sticker":
		a.live.mu.RLock()
		enabled, maxBytes := a.live.mediaEnabled, a.live.mediaMaxBytes
		a.live.mu.RUnlock()
		if !enabled {
			return transport.MessageRef{}, errors.New("Telegram media forwarding is disabled")
		}
		if len(outgoing.MediaBytes) == 0 {
			return transport.MessageRef{}, errors.New("outgoing Telegram media bytes are required")
		}
		if maxBytes > 0 && uint64(len(outgoing.MediaBytes)) > maxBytes {
			return transport.MessageRef{}, errMTProtoMediaTooLarge
		}
		if kind == "sticker" {
			if _, stickerErr := telegramStickerFilename(outgoing.MediaBytes); stickerErr != nil {
				return transport.MessageRef{}, stickerErr
			}
		}
		content = truncateTelegramText(content, telegramCaptionLimit)
		done := a.live.markPendingSend(outgoing.Endpoint, kind, topicID, content)
		id, err = client.SendMedia(ctx, peer, kind, outgoing.MediaBytes, content, replyID, topicID)
		done(err == nil)
	default:
		return transport.MessageRef{}, errors.New("unsupported Telegram MTProto outgoing message kind")
	}
	if err != nil {
		return transport.MessageRef{}, errors.New("send Telegram MTProto message")
	}
	if id <= 0 {
		return transport.MessageRef{}, errors.New("Telegram MTProto returned an invalid message id")
	}
	a.live.rememberMessage(outgoing.Endpoint, id)
	return transport.MessageRef{
		Endpoint: outgoing.Endpoint, RemoteMessageID: strconv.Itoa(id), IsTargetFromMe: true, ChildScope: outgoing.ChildScope,
	}, nil
}

func (a *MTProtoAdapter) reactMTProto(ctx context.Context, reaction transport.Reaction) error {
	peer, client, err := a.resolveMTProtoPeer(ctx, reaction.Endpoint)
	if err != nil {
		return err
	}
	id, err := strconv.Atoi(strings.TrimSpace(reaction.TargetRemoteID))
	if err != nil || id <= 0 {
		return errors.New("invalid Telegram reaction target")
	}
	if err := client.React(ctx, peer, id, strings.TrimSpace(reaction.Emoji)); err != nil {
		return errors.New("send Telegram MTProto reaction")
	}
	return nil
}

func (a *MTProtoAdapter) editMTProto(ctx context.Context, ref transport.MessageRef, text string) error {
	peer, client, err := a.resolveMTProtoPeer(ctx, ref.Endpoint)
	if err != nil {
		return err
	}
	id, err := strconv.Atoi(strings.TrimSpace(ref.RemoteMessageID))
	if err != nil || id <= 0 {
		return errors.New("invalid Telegram edit target")
	}
	text = truncateTelegramText(text, telegramTextLimit)
	if strings.TrimSpace(text) == "" {
		return errors.New("Telegram edit text is required")
	}
	done := a.live.markPendingMutation("edit", ref.Endpoint, id)
	err = client.Edit(ctx, peer, id, text)
	done(err == nil)
	if err != nil {
		return errors.New("edit Telegram MTProto message")
	}
	return nil
}

func (a *MTProtoAdapter) deleteMTProto(ctx context.Context, ref transport.MessageRef) error {
	peer, client, err := a.resolveMTProtoPeer(ctx, ref.Endpoint)
	if err != nil {
		return err
	}
	id, err := strconv.Atoi(strings.TrimSpace(ref.RemoteMessageID))
	if err != nil || id <= 0 {
		return errors.New("invalid Telegram delete target")
	}
	done := a.live.markPendingMutation("delete", ref.Endpoint, id)
	err = client.Delete(ctx, peer, id)
	done(err == nil)
	if err != nil {
		return errors.New("delete Telegram MTProto message")
	}
	return nil
}

func newGotdRuntimeWithUpdates(apiID int, apiHash string, storage gotdsession.Storage, handler gotdtelegram.UpdateHandler) mtprotoRuntime {
	return &gotdRuntime{client: gotdtelegram.NewClient(apiID, apiHash, gotdtelegram.Options{
		SessionStorage: storage,
		UpdateHandler:  handler,
	})}
}

func (c *gotdAuthClient) SelfID(ctx context.Context) (int64, error) {
	status, err := c.client.Auth().Status(ctx)
	if err != nil || status == nil || !status.Authorized || status.User == nil || status.User.ID <= 0 {
		if err != nil {
			return 0, err
		}
		return 0, errors.New("Telegram MTProto account is not authorized")
	}
	return status.User.ID, nil
}

func (c *gotdAuthClient) ListGroups(ctx context.Context) ([]mtprotoGroup, error) {
	raw := tg.NewClient(c.client)
	iter := gotddialogs.NewQueryBuilder(raw).GetDialogs().BatchSize(100).Iter()
	groups := make([]mtprotoGroup, 0)
	for iter.Next(ctx) {
		elem := iter.Value()
		switch peer := elem.Peer.(type) {
		case *tg.InputPeerChat:
			chat, ok := elem.Entities.Chat(peer.ChatID)
			if !ok || chat == nil || chat.Deactivated || chat.Left {
				continue
			}
			groups = append(groups, mtprotoGroup{
				Peer: mtprotoPeerState{
					RemoteID: strconv.FormatInt(-peer.ChatID, 10), Kind: "chat", ID: peer.ChatID,
				},
				Title: chat.Title,
			})
		case *tg.InputPeerChannel:
			channel, ok := elem.Entities.Channel(peer.ChannelID)
			if !ok || channel == nil || channel.Left || !(channel.Megagroup || channel.Gigagroup || channel.Forum) {
				continue
			}
			groups = append(groups, mtprotoGroup{
				Peer: mtprotoPeerState{
					RemoteID: strconv.FormatInt(-(mtprotoChannelRemoteBase + peer.ChannelID), 10),
					Kind:     "channel", ID: peer.ChannelID, AccessHash: peer.AccessHash,
				},
				Title: channel.Title, Username: channel.Username, Forum: channel.Forum,
			})
		}
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Peer.RemoteID < groups[j].Peer.RemoteID })
	return groups, nil
}

func mtprotoRandomID() (int64, error) {
	id, err := gotdcrypto.RandInt64(gotdcrypto.DefaultRand())
	if err != nil {
		return 0, err
	}
	if id == 0 {
		return 1, nil
	}
	return id, nil
}

func mtprotoReply(replyID, topicID int) tg.InputReplyToClass {
	if replyID > 0 {
		reply := &tg.InputReplyToMessage{ReplyToMsgID: replyID}
		if topicID > 1 && replyID != topicID {
			reply.TopMsgID = topicID
		}
		return reply
	}
	if topicID > 0 {
		return &tg.InputReplyToMessage{ReplyToMsgID: topicID}
	}
	return nil
}

func (c *gotdAuthClient) SendText(ctx context.Context, peer mtprotoPeerState, text string, replyID, topicID int) (int, error) {
	input, err := peer.input()
	if err != nil {
		return 0, err
	}
	randomID, err := mtprotoRandomID()
	if err != nil {
		return 0, err
	}
	updates, err := tg.NewClient(c.client).MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
		Peer: input, ReplyTo: mtprotoReply(replyID, topicID), Message: text, RandomID: randomID,
	})
	return gotdunpack.MessageID(updates, err)
}

func mtprotoUploadName(kind string, data []byte) string {
	switch kind {
	case "image":
		return "image.jpg"
	case "video":
		return "video.mp4"
	case "audio":
		return telegramAudioFilename(data)
	case "sticker":
		if name, err := telegramStickerFilename(data); err == nil {
			return name
		}
		return "sticker.bin"
	default:
		return "document.bin"
	}
}

func (c *gotdAuthClient) SendMedia(ctx context.Context, peer mtprotoPeerState, kind string, data []byte, caption string, replyID, topicID int) (int, error) {
	input, err := peer.input()
	if err != nil {
		return 0, err
	}
	raw := tg.NewClient(c.client)
	name := mtprotoUploadName(kind, data)
	file, err := gotduploader.NewUploader(raw).FromBytes(ctx, name, data)
	if err != nil {
		return 0, err
	}
	var media tg.InputMediaClass
	if kind == "image" {
		media = &tg.InputMediaUploadedPhoto{File: file}
	} else {
		mime := http.DetectContentType(data)
		media = &tg.InputMediaUploadedDocument{
			File:     file,
			MimeType: mime,
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeFilename{FileName: name},
			},
		}
	}
	randomID, err := mtprotoRandomID()
	if err != nil {
		return 0, err
	}
	updates, err := raw.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
		Peer: input, ReplyTo: mtprotoReply(replyID, topicID), Media: media, Message: caption, RandomID: randomID,
	})
	return gotdunpack.MessageID(updates, err)
}

func (c *gotdAuthClient) React(ctx context.Context, peer mtprotoPeerState, id int, emoji string) error {
	input, err := peer.input()
	if err != nil {
		return err
	}
	var reactions []tg.ReactionClass
	if emoji != "" {
		reactions = []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: emoji}}
	}
	_, err = tg.NewClient(c.client).MessagesSendReaction(ctx, &tg.MessagesSendReactionRequest{
		Peer: input, MsgID: id, Reaction: reactions,
	})
	return err
}

func (c *gotdAuthClient) Edit(ctx context.Context, peer mtprotoPeerState, id int, text string) error {
	input, err := peer.input()
	if err != nil {
		return err
	}
	_, err = tg.NewClient(c.client).MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{
		Peer: input, ID: id, Message: text,
	})
	return err
}

func (c *gotdAuthClient) Delete(ctx context.Context, peer mtprotoPeerState, id int) error {
	input, err := peer.input()
	if err != nil {
		return err
	}
	_, err = gotdmessage.NewSender(tg.NewClient(c.client)).To(input).Revoke().Messages(ctx, id)
	return err
}

type mtprotoLimitWriter struct {
	buf bytes.Buffer
	max uint64
}

func (w *mtprotoLimitWriter) Write(p []byte) (int, error) {
	if w.max > 0 && uint64(w.buf.Len()+len(p)) > w.max {
		return 0, errMTProtoMediaTooLarge
	}
	return w.buf.Write(p)
}

func (c *gotdAuthClient) DownloadMedia(ctx context.Context, ref mtprotoMediaRef, max uint64) ([]byte, error) {
	if ref.Location == nil {
		return nil, errors.New("Telegram MTProto media location is unavailable")
	}
	if max > 0 && ref.Size > max {
		return nil, errMTProtoMediaTooLarge
	}
	writer := &mtprotoLimitWriter{max: max}
	_, err := gotddownloader.NewDownloader().Download(tg.NewClient(c.client), ref.Location).Stream(ctx, writer)
	if err != nil {
		if errors.Is(err, errMTProtoMediaTooLarge) {
			return nil, errMTProtoMediaTooLarge
		}
		return nil, err
	}
	return append([]byte(nil), writer.buf.Bytes()...), nil
}

func (c *gotdAuthClient) ReactionSnapshot(ctx context.Context, peer mtprotoPeerState, id int) ([]mtprotoReactionState, error) {
	input, err := peer.input()
	if err != nil {
		return nil, err
	}
	raw := tg.NewClient(c.client)
	offset := ""
	out := make([]mtprotoReactionState, 0)
	for len(out) < mtprotoReactionMaxPeers {
		result, err := raw.MessagesGetMessageReactionsList(ctx, &tg.MessagesGetMessageReactionsListRequest{
			Peer: input, ID: id, Offset: offset, Limit: mtprotoReactionPageSize,
		})
		if err != nil {
			return nil, err
		}
		users := make(map[int64]*tg.User)
		for _, class := range result.Users {
			if user, ok := class.(*tg.User); ok {
				users[user.ID] = user
			}
		}
		for _, item := range result.Reactions {
			peerUser, ok := item.PeerID.(*tg.PeerUser)
			if !ok || peerUser.UserID <= 0 {
				continue
			}
			emoji, ok := mtprotoReactionEmoji(item.Reaction)
			if !ok {
				continue
			}
			display := ""
			if user := users[peerUser.UserID]; user != nil {
				display = strings.TrimSpace(strings.Join(strings.Fields(user.FirstName+" "+user.LastName), " "))
				if display == "" {
					display = strings.TrimSpace(user.Username)
				}
			}
			out = append(out, mtprotoReactionState{
				UserID: peerUser.UserID, Emoji: emoji, DisplayName: display, Date: item.Date, My: item.My,
			})
			if len(out) >= mtprotoReactionMaxPeers {
				break
			}
		}
		next, ok := result.GetNextOffset()
		if !ok || strings.TrimSpace(next) == "" || next == offset {
			break
		}
		offset = next
	}
	return out, nil
}
