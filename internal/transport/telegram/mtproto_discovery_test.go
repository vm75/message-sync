package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	gotdsession "github.com/gotd/td/session"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
)

type fakeMTDiscoveryAuth struct {
	*fakeMTLiveAuth
	topics    map[string][]mtprotoTopic
	topicsErr error
}

func (f *fakeMTDiscoveryAuth) ListForumTopics(_ context.Context, peer mtprotoPeerState) ([]mtprotoTopic, error) {
	if f.topicsErr != nil {
		return nil, f.topicsErr
	}
	return append([]mtprotoTopic(nil), f.topics[peer.RemoteID]...), nil
}

func newMTProtoDiscoveryAdapter(t *testing.T, connectionID string, auth *fakeMTDiscoveryAuth) (*MTProtoAdapter, *memoryMTStore) {
	t.Helper()
	auth.authorized = true
	raw, err := json.Marshal(mtprotoState{Version: mtprotoStateVersion, APIID: 1, APIHash: "hash", Phone: "+1000", Session: []byte("session")})
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryMTStore{data: raw}
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := OpenMTProto(context.Background(), Options{
		ConnectionID: connectionID, Logger: testLogger(), MTProtoStateStore: store,
		Hasher: hasher, UsernameMode: config.UsernameModePushName,
		mtprotoRuntimeFactory: func(int, string, gotdsession.Storage) mtprotoRuntime { return fakeMTLiveRuntime{auth: auth} },
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-adapter.ready:
	case <-time.After(time.Second):
		t.Fatal("adapter not ready")
	}
	return adapter, store
}

func TestMTProtoFullDiscoveryValidationAndPeerPersistence(t *testing.T) {
	auth := &fakeMTDiscoveryAuth{fakeMTLiveAuth: &fakeMTLiveAuth{groups: []mtprotoGroup{
		{Peer: mtprotoPeerState{RemoteID: "-7", Kind: "chat", ID: 7}, Title: "Basic"},
		{Peer: mtprotoPeerState{RemoteID: "-1000000000042", Kind: "channel", ID: 42, AccessHash: 99}, Title: "Forum", Username: "forum", Forum: true},
	}}}
	adapter, store := newMTProtoDiscoveryAdapter(t, "mt-discovery", auth)
	defer adapter.Close()

	chats, err := adapter.DiscoverChats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 2 || chats[0].ChatID != "-1000000000042" || chats[0].Type != "supergroup" || !chats[0].Forum || chats[1].Type != "group" {
		t.Fatalf("unexpected discovery: %+v", chats)
	}
	if err := adapter.ValidateTarget(context.Background(), "-7"); err != nil {
		t.Fatalf("valid group rejected: %v", err)
	}
	if !errors.Is(adapter.ValidateTarget(context.Background(), "-999"), ErrUnsupportedTarget) {
		t.Fatal("unknown group was accepted")
	}
	if !errors.Is(adapter.ValidateTarget(context.Background(), "123"), ErrUnsupportedTarget) {
		t.Fatal("positive/private target was accepted")
	}

	var state mtprotoState
	if err := json.Unmarshal(store.data, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Peers) != 2 || state.Peers["-1000000000042"].AccessHash != 99 {
		t.Fatalf("peer cache not persisted: %+v", state.Peers)
	}
}

func TestMTProtoForumTopicDiscoveryIncludesGeneralWithoutInventedLabel(t *testing.T) {
	remote := "-1000000000042"
	auth := &fakeMTDiscoveryAuth{
		fakeMTLiveAuth: &fakeMTLiveAuth{groups: []mtprotoGroup{{Peer: mtprotoPeerState{RemoteID: remote, Kind: "channel", ID: 42, AccessHash: 99}, Title: "Forum", Forum: true}}},
		topics:         map[string][]mtprotoTopic{remote: {{ID: 17, Title: "Plans"}}},
	}
	adapter, _ := newMTProtoDiscoveryAdapter(t, "mt-topics", auth)
	defer adapter.Close()
	topics, err := adapter.DiscoverTopics(context.Background(), remote)
	if err != nil {
		t.Fatal(err)
	}
	if len(topics) != 2 || topics[0].RemoteID != "1" || !topics[0].General || topics[0].Label != "" || topics[1].RemoteID != "17" || topics[1].Label != "Plans" {
		t.Fatalf("unexpected topics: %+v", topics)
	}
}

func TestMTProtoDiscoveryConnectionIsolation(t *testing.T) {
	one := &fakeMTDiscoveryAuth{fakeMTLiveAuth: &fakeMTLiveAuth{groups: []mtprotoGroup{{Peer: mtprotoPeerState{RemoteID: "-11", Kind: "chat", ID: 11}, Title: "One"}}}}
	two := &fakeMTDiscoveryAuth{fakeMTLiveAuth: &fakeMTLiveAuth{groups: []mtprotoGroup{{Peer: mtprotoPeerState{RemoteID: "-22", Kind: "chat", ID: 22}, Title: "Two"}}}}
	a, _ := newMTProtoDiscoveryAdapter(t, "mt-one", one)
	defer a.Close()
	b, _ := newMTProtoDiscoveryAdapter(t, "mt-two", two)
	defer b.Close()
	ac, _ := a.DiscoverChats(context.Background())
	bc, _ := b.DiscoverChats(context.Background())
	if len(ac) != 1 || ac[0].ChatID != "-11" || len(bc) != 1 || bc[0].ChatID != "-22" {
		t.Fatalf("discovery leaked: a=%+v b=%+v", ac, bc)
	}
}
