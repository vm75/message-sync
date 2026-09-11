package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	gotdsession "github.com/gotd/td/session"
	"github.com/gotd/td/tg"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
)

type fakeMTLiveAuth struct {
	fakeMTAuth
	mu sync.Mutex

	groups    []mtprotoGroup
	groupsErr error
	nextID    int

	lastPeer    mtprotoPeerState
	lastKind    string
	lastText    string
	lastReplyID int
	lastTopicID int
	lastMedia   []byte

	reactionEmoji string
	editText      string
	deletedID     int
	download      []byte
	snapshot      []mtprotoReactionState
}

func (f *fakeMTLiveAuth) SelfID(context.Context) (int64, error) { return 999, nil }
func (f *fakeMTLiveAuth) ListGroups(context.Context) ([]mtprotoGroup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.groupsErr != nil {
		return nil, f.groupsErr
	}
	return append([]mtprotoGroup(nil), f.groups...), nil
}
func (f *fakeMTLiveAuth) SendText(_ context.Context, peer mtprotoPeerState, text string, replyID, topicID int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastPeer, f.lastKind, f.lastText, f.lastReplyID, f.lastTopicID = peer, "text", text, replyID, topicID
	if f.nextID == 0 {
		f.nextID = 100
	}
	return f.nextID, nil
}
func (f *fakeMTLiveAuth) SendMedia(_ context.Context, peer mtprotoPeerState, kind string, data []byte, text string, replyID, topicID int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastPeer, f.lastKind, f.lastText, f.lastReplyID, f.lastTopicID = peer, kind, text, replyID, topicID
	f.lastMedia = append([]byte(nil), data...)
	if f.nextID == 0 {
		f.nextID = 101
	}
	return f.nextID, nil
}
func (f *fakeMTLiveAuth) React(_ context.Context, peer mtprotoPeerState, _ int, emoji string) error {
	f.mu.Lock()
	f.lastPeer, f.reactionEmoji = peer, emoji
	f.mu.Unlock()
	return nil
}
func (f *fakeMTLiveAuth) Edit(_ context.Context, peer mtprotoPeerState, _ int, text string) error {
	f.mu.Lock()
	f.lastPeer, f.editText = peer, text
	f.mu.Unlock()
	return nil
}
func (f *fakeMTLiveAuth) Delete(_ context.Context, peer mtprotoPeerState, id int) error {
	f.mu.Lock()
	f.lastPeer, f.deletedID = peer, id
	f.mu.Unlock()
	return nil
}
func (f *fakeMTLiveAuth) DownloadMedia(_ context.Context, _ mtprotoMediaRef, max uint64) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if max > 0 && uint64(len(f.download)) > max {
		return nil, errMTProtoMediaTooLarge
	}
	return append([]byte(nil), f.download...), nil
}
func (f *fakeMTLiveAuth) ReactionSnapshot(context.Context, mtprotoPeerState, int) ([]mtprotoReactionState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mtprotoReactionState(nil), f.snapshot...), nil
}

type fakeMTLiveRuntime struct{ auth mtprotoAuthClient }

func (r fakeMTLiveRuntime) Run(ctx context.Context, fn func(context.Context, mtprotoAuthClient) error) error {
	return fn(ctx, r.auth)
}

func newMTProtoLiveAdapter(t *testing.T, connectionID string, remoteID string, live *fakeMTLiveAuth) (*MTProtoAdapter, *memoryMTStore) {
	t.Helper()
	live.authorized = true
	stateBytes, err := json.Marshal(mtprotoState{
		Version: mtprotoStateVersion, APIID: 1, APIHash: "hash", Phone: "+1000", Session: []byte("session"),
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryMTStore{data: stateBytes}
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	adapter, err := OpenMTProto(ctx, Options{
		ConnectionID:      connectionID,
		Logger:            testLogger(),
		MTProtoStateStore: store,
		ChatIDs:           map[string]string{"tg": remoteID},
		Hasher:            hasher,
		UsernameMode:      config.UsernameModePushName,
		MediaEnabled:      true,
		MediaMaxBytes:     1024 * 1024,
		mtprotoRuntimeFactory: func(int, string, gotdsession.Storage) mtprotoRuntime {
			return fakeMTLiveRuntime{auth: live}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-adapter.ready:
	case <-time.After(time.Second):
		t.Fatal("MTProto adapter did not become ready")
	}
	return adapter, store
}

func TestMTProtoLiveIngressTextReplyTopicAndSelfSuppression(t *testing.T) {
	remote := -(mtprotoChannelRemoteBase + int64(99))
	live := &fakeMTLiveAuth{groups: []mtprotoGroup{{
		Peer:  mtprotoPeerState{RemoteID: "-1000000000099", Kind: "channel", ID: 99, AccessHash: 777},
		Title: "Group", Forum: true,
	}}}
	adapter, _ := newMTProtoLiveAdapter(t, "mt-live", "-1000000000099", live)
	defer adapter.Close()

	entities := tg.Entities{
		Users:    map[int64]*tg.User{7: &tg.User{ID: 7, FirstName: "Alice"}},
		Channels: map[int64]*tg.Channel{99: &tg.Channel{ID: 99, AccessHash: 777, Title: "Group", Megagroup: true, Forum: true}},
	}
	msg := &tg.Message{
		ID: 20, PeerID: &tg.PeerChannel{ChannelID: 99}, FromID: &tg.PeerUser{UserID: 7},
		Date: int(time.Now().Unix()), Message: "hello",
		ReplyTo: &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 18, ReplyToTopID: 10},
	}
	adapter.normalizeMTProtoMessage(context.Background(), entities, msg, false)
	select {
	case incoming := <-adapter.Events():
		if incoming.Endpoint != "tg" || incoming.Text != "hello" || incoming.Kind != "text" {
			t.Fatalf("unexpected incoming: %+v", incoming)
		}
		if incoming.ChildScope == nil || incoming.ChildScope.Kind != transport.ScopeKindTelegramTopic || incoming.ChildScope.RemoteID != "10" {
			t.Fatalf("unexpected child scope: %+v", incoming.ChildScope)
		}
		if incoming.ReplyTo == nil || incoming.ReplyTo.RemoteMessageID != "18" {
			t.Fatalf("unexpected reply: %+v", incoming.ReplyTo)
		}
		if incoming.Sender.DisplayName != "Alice" || incoming.Sender.OpaqueID == "" {
			t.Fatalf("unexpected sender: %+v", incoming.Sender)
		}
	case <-time.After(time.Second):
		t.Fatal("missing MTProto ingress")
	}

	// A bridge-originated send is tracked before the RPC so its MTProto outgoing
	// update cannot race the router's message-copy persistence and re-enter sync.
	live.nextID = 21
	_, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint: "tg", Kind: "text", Text: "bridge echo", SourceText: "bridge echo",
		ChildScope: &transport.ChildScope{Kind: transport.ScopeKindTelegramTopic, RemoteID: "10"},
	})
	if err != nil {
		t.Fatal(err)
	}
	self := &tg.Message{
		ID: 21, Out: true, PeerID: &tg.PeerChannel{ChannelID: 99}, FromID: &tg.PeerUser{UserID: 999},
		Date: int(time.Now().Unix()), Message: "bridge echo",
		ReplyTo: &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 10, ReplyToTopID: 10},
	}
	adapter.normalizeMTProtoMessage(context.Background(), entities, self, false)
	select {
	case got := <-adapter.Events():
		t.Fatalf("bridge self echo was not suppressed: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
	if remote != -1000000000099 {
		t.Fatalf("test remote formula changed: %d", remote)
	}
}

func TestMTProtoLiveOutboundReplyMediaMutationsAndPeerPersistence(t *testing.T) {
	live := &fakeMTLiveAuth{groups: []mtprotoGroup{{
		Peer:  mtprotoPeerState{RemoteID: "-1000000000042", Kind: "channel", ID: 42, AccessHash: 4242},
		Title: "Persistent",
	}}}
	adapter, store := newMTProtoLiveAdapter(t, "mt-persist", "-1000000000042", live)

	ref, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint: "tg", Kind: "text", Text: "reply", SourceText: "reply",
		ReplyTo:    &transport.MessageRef{Endpoint: "tg", RemoteMessageID: "8"},
		ChildScope: &transport.ChildScope{Kind: transport.ScopeKindTelegramTopic, RemoteID: "5"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref.RemoteMessageID == "" || live.lastPeer.AccessHash != 4242 || live.lastReplyID != 8 || live.lastTopicID != 5 {
		t.Fatalf("outbound routing not preserved: ref=%+v peer=%+v reply=%d topic=%d", ref, live.lastPeer, live.lastReplyID, live.lastTopicID)
	}

	live.nextID = 102
	if _, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint: "tg", Kind: "image", Text: "caption", SourceText: "caption", MediaBytes: []byte{0xff, 0xd8, 0xff},
	}); err != nil {
		t.Fatal(err)
	}
	if live.lastKind != "image" || len(live.lastMedia) == 0 {
		t.Fatalf("media send not delegated: kind=%q bytes=%d", live.lastKind, len(live.lastMedia))
	}
	if err := adapter.React(context.Background(), transport.Reaction{Endpoint: "tg", TargetRemoteID: "100", Emoji: "👍"}); err != nil {
		t.Fatal(err)
	}
	if live.reactionEmoji != "👍" {
		t.Fatalf("reaction=%q", live.reactionEmoji)
	}
	if err := adapter.Edit(context.Background(), transport.MessageRef{Endpoint: "tg", RemoteMessageID: "100"}, "edited"); err != nil {
		t.Fatal(err)
	}
	if live.editText != "edited" {
		t.Fatalf("edit=%q", live.editText)
	}
	if err := adapter.Delete(context.Background(), transport.MessageRef{Endpoint: "tg", RemoteMessageID: "100"}); err != nil {
		t.Fatal(err)
	}
	if live.deletedID != 100 {
		t.Fatalf("deleted id=%d", live.deletedID)
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}

	// Restart with dialog refresh unavailable. The encrypted state must still
	// contain the access hash needed for an ordinary configured send.
	live2 := &fakeMTLiveAuth{groupsErr: errors.New("offline refresh"), nextID: 200}
	hasher, _ := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	restarted, err := OpenMTProto(context.Background(), Options{
		ConnectionID: "mt-persist", Logger: testLogger(), MTProtoStateStore: store,
		ChatIDs: map[string]string{"tg": "-1000000000042"}, Hasher: hasher, UsernameMode: config.UsernameModePushName,
		MediaEnabled: true, MediaMaxBytes: 1024 * 1024,
		mtprotoRuntimeFactory: func(int, string, gotdsession.Storage) mtprotoRuntime {
			live2.authorized = true
			return fakeMTLiveRuntime{auth: live2}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	<-restarted.ready
	if _, err := restarted.Send(context.Background(), transport.Outgoing{Endpoint: "tg", Text: "after restart", SourceText: "after restart"}); err != nil {
		t.Fatalf("send after restart lost peer access hash: %v", err)
	}
	if live2.lastPeer.AccessHash != 4242 {
		t.Fatalf("restart access hash=%d", live2.lastPeer.AccessHash)
	}
}

func TestMTProtoLiveIngressEditDeleteMediaReactionAndIsolation(t *testing.T) {
	live := &fakeMTLiveAuth{groups: []mtprotoGroup{{
		Peer:  mtprotoPeerState{RemoteID: "-77", Kind: "chat", ID: 77},
		Title: "Basic",
	}}, download: []byte("media")}
	adapter, _ := newMTProtoLiveAdapter(t, "mt-a", "-77", live)
	defer adapter.Close()

	entities := tg.Entities{
		Users: map[int64]*tg.User{4: &tg.User{ID: 4, FirstName: "Bob"}},
		Chats: map[int64]*tg.Chat{77: &tg.Chat{ID: 77, Title: "Basic"}},
	}
	edit := &tg.Message{ID: 9, PeerID: &tg.PeerChat{ChatID: 77}, FromID: &tg.PeerUser{UserID: 4}, Date: int(time.Now().Unix()), Message: "changed"}
	adapter.normalizeMTProtoMessage(context.Background(), entities, edit, true)
	incoming := <-adapter.Events()
	if incoming.Kind != "edit" || incoming.ReplyTo == nil || incoming.ReplyTo.RemoteMessageID != "9" {
		t.Fatalf("edit normalize: %+v", incoming)
	}

	adapter.live.rememberMessage("tg", 9)
	err := adapter.handleMTProtoUpdates(context.Background(), &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateDeleteMessages{Messages: []int{9}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	incoming = <-adapter.Events()
	if incoming.Kind != "delete" || incoming.ReplyTo == nil || incoming.ReplyTo.RemoteMessageID != "9" {
		t.Fatalf("delete normalize: %+v", incoming)
	}

	// Reaction snapshots are kept only in memory. After a baseline, a changed
	// actor reaction produces the canonical reaction event.
	live.snapshot = []mtprotoReactionState{{UserID: 4, Emoji: "👍", DisplayName: "Bob", Date: int(time.Now().Unix())}}
	adapter.handleMTProtoReactionUpdate(context.Background(), &tg.UpdateMessageReactions{
		Peer: &tg.PeerChat{ChatID: 77}, MsgID: 12, Reactions: tg.MessageReactions{},
	})
	live.snapshot = []mtprotoReactionState{{UserID: 4, Emoji: "❤️", DisplayName: "Bob", Date: int(time.Now().Unix())}}
	adapter.handleMTProtoReactionUpdate(context.Background(), &tg.UpdateMessageReactions{
		Peer: &tg.PeerChat{ChatID: 77}, MsgID: 12, Reactions: tg.MessageReactions{},
	})
	incoming = <-adapter.Events()
	if incoming.Kind != "reaction" || incoming.Text != "❤️" || incoming.Sender.OpaqueID == "" {
		t.Fatalf("reaction normalize: %+v", incoming)
	}

	// Peer and message state is per adapter/connection.
	liveB := &fakeMTLiveAuth{groups: []mtprotoGroup{{Peer: mtprotoPeerState{RemoteID: "-88", Kind: "chat", ID: 88}}}}
	other, _ := newMTProtoLiveAdapter(t, "mt-b", "-88", liveB)
	defer other.Close()
	if _, ok := other.live.endpointForMessage(9); ok {
		t.Fatal("message cache leaked across MTProto connections")
	}
	if _, ok := other.live.peerSnapshot()["-77"]; ok {
		t.Fatal("peer cache leaked across MTProto connections")
	}
}

func TestMTProtoMediaDescriptorAndConfiguredLimit(t *testing.T) {
	doc := &tg.Document{
		ID: 1, AccessHash: 2, FileReference: []byte{3}, Size: 2048, MimeType: "audio/ogg",
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{Voice: true}},
	}
	ref, ok := mtprotoMediaDescriptor(&tg.Message{Media: &tg.MessageMediaDocument{Document: doc}})
	if !ok || ref.Kind != "audio" || ref.Size != 2048 {
		t.Fatalf("media descriptor=%+v ok=%v", ref, ok)
	}
	live := &fakeMTLiveAuth{groups: []mtprotoGroup{{Peer: mtprotoPeerState{RemoteID: "-5", Kind: "chat", ID: 5}}}, download: make([]byte, 32)}
	adapter, _ := newMTProtoLiveAdapter(t, "mt-media", "-5", live)
	defer adapter.Close()
	if _, err := live.DownloadMedia(context.Background(), ref, 16); !errors.Is(err, errMTProtoMediaTooLarge) {
		t.Fatalf("expected size limit, got %v", err)
	}
}
