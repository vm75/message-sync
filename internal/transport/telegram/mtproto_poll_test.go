package telegram

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	gotdsession "github.com/gotd/td/session"
	"github.com/gotd/td/tg"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
)

type fakeMTPollAuth struct {
	*fakeMTLiveAuth
	pollResult mtprotoPollSendResult
	question   string
	options    []string
	selectable int
	duration   int
	replyID    int
	topicID    int
}

func (f *fakeMTPollAuth) SendPoll(_ context.Context, _ mtprotoPeerState, question string, options []string, selectable, duration, replyID, topicID int) (mtprotoPollSendResult, error) {
	f.question = question
	f.options = append([]string(nil), options...)
	f.selectable, f.duration, f.replyID, f.topicID = selectable, duration, replyID, topicID
	if f.pollResult.MessageID == 0 {
		f.pollResult = mtprotoPollSendResult{MessageID: 70, PollID: 9001, OptionKeys: []string{"MA", "MQ"}}
	}
	return f.pollResult, nil
}

func pollEntities() tg.Entities {
	return tg.Entities{
		Users:    map[int64]*tg.User{7: {ID: 7, FirstName: "Alice"}},
		Channels: map[int64]*tg.Channel{42: {ID: 42, AccessHash: 9, Title: "Forum", Megagroup: true, Forum: true}},
	}
}

func nativePollMessage(id int, pollID int64, multiple bool, topic int) *tg.Message {
	poll := tg.Poll{
		ID:       pollID,
		Question: tg.TextWithEntities{Text: "Lunch?"},
		Answers: []tg.PollAnswerClass{
			&tg.PollAnswer{Text: tg.TextWithEntities{Text: "Rice"}, Option: []byte("0")},
			&tg.PollAnswer{Text: tg.TextWithEntities{Text: "Soup"}, Option: []byte("1")},
		},
		MultipleChoice: multiple,
	}
	message := &tg.Message{
		ID: id, PeerID: &tg.PeerChannel{ChannelID: 42}, FromID: &tg.PeerUser{UserID: 7},
		Date: int(time.Now().Unix()), Media: &tg.MessageMediaPoll{Poll: poll},
	}
	if topic > 0 {
		message.ReplyTo = &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: topic, ReplyToTopID: topic}
	}
	return message
}

func openPollAdapter(t *testing.T, id string, store *memoryMTStore, auth mtprotoAuthClient) *MTProtoAdapter {
	t.Helper()
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := OpenMTProto(context.Background(), Options{
		ConnectionID: id, Logger: testLogger(), MTProtoStateStore: store,
		ChatIDs: map[string]string{"tg": "-1000000000042"}, Hasher: hasher, UsernameMode: config.UsernameModePushName,
		MediaEnabled: true, MediaMaxBytes: 1024 * 1024,
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
	return adapter
}

func pollStore(t *testing.T) *memoryMTStore {
	t.Helper()
	raw, err := json.Marshal(mtprotoState{Version: 1, APIID: 1, APIHash: "hash", Phone: "+1", Session: []byte("session")})
	if err != nil {
		t.Fatal(err)
	}
	return &memoryMTStore{data: raw}
}

func TestMTProtoNativePollIngressAndPersistentCorrelation(t *testing.T) {
	live := &fakeMTLiveAuth{groups: []mtprotoGroup{{Peer: mtprotoPeerState{RemoteID: "-1000000000042", Kind: "channel", ID: 42, AccessHash: 9}, Forum: true}}}
	live.authorized = true
	store := pollStore(t)
	adapter := openPollAdapter(t, "poll-in", store, live)
	defer adapter.Close()

	adapter.normalizeMTProtoMessage(context.Background(), pollEntities(), nativePollMessage(55, 777, true, 11), false)
	select {
	case incoming := <-adapter.Events():
		if incoming.Kind != "poll" || incoming.Text != "Lunch?" || len(incoming.PollOptions) != 2 || incoming.PollOptions[0] != "Rice" || incoming.PollSelectableCount != 2 {
			t.Fatalf("unexpected poll ingress: %+v", incoming)
		}
		if incoming.PollProvider != "telegram:poll-in" || incoming.PollProviderReference != "777" || incoming.ChildScope == nil || incoming.ChildScope.RemoteID != "11" {
			t.Fatalf("unexpected poll correlation: %+v", incoming)
		}
	case <-time.After(time.Second):
		t.Fatal("missing poll ingress")
	}
	state, err := adapter.state.load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	correlation, ok := state.Polls["777"]
	if !ok || correlation.MessageID != 55 || correlation.TopicID != 11 || len(correlation.OptionKeys) != 2 {
		t.Fatalf("missing persistent poll correlation: %+v", correlation)
	}
	raw := string(store.data)
	for _, secret := range []string{"Lunch?", "Rice", "Soup", "Alice"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("poll content leaked into MTProto correlation state: %q", secret)
		}
	}
}

func TestMTProtoNativePollOutboundAndAggregateSnapshot(t *testing.T) {
	base := &fakeMTLiveAuth{groups: []mtprotoGroup{{Peer: mtprotoPeerState{RemoteID: "-1000000000042", Kind: "channel", ID: 42, AccessHash: 9}, Forum: true}}}
	base.authorized = true
	auth := &fakeMTPollAuth{fakeMTLiveAuth: base, pollResult: mtprotoPollSendResult{MessageID: 70, PollID: 9001, OptionKeys: []string{"MA", "MQ"}}}
	store := pollStore(t)
	adapter := openPollAdapter(t, "poll-out", store, auth)
	defer adapter.Close()

	ref, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint: "tg", Kind: "poll", SourceText: "Lunch?", PollOptions: []string{"Rice", "Soup"}, PollSelectableCount: 2,
		ReplyTo:    &transport.MessageRef{Endpoint: "tg", RemoteMessageID: "60"},
		ChildScope: &transport.ChildScope{Kind: transport.ScopeKindTelegramTopic, RemoteID: "11"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref.RemoteMessageID != "70" || ref.Provider != "telegram:poll-out" || ref.ProviderReference != "9001" {
		t.Fatalf("unexpected poll ref: %+v", ref)
	}
	if auth.question != "Lunch?" || len(auth.options) != 2 || auth.selectable != 2 || auth.replyID != 60 || auth.topicID != 11 {
		t.Fatalf("unexpected native poll call: %+v", auth)
	}

	adapter.handleMTProtoPollUpdate(context.Background(), &tg.UpdateMessagePoll{
		PollID: 9001,
		Results: tg.PollResults{Results: []tg.PollAnswerVoters{
			{Option: []byte("0"), Voters: 3, RecentVoters: []tg.PeerClass{&tg.PeerUser{UserID: 123456}}},
			{Option: []byte("1"), Voters: 1},
		}},
	})
	select {
	case incoming := <-adapter.Events():
		if incoming.Kind != "poll_snapshot" || incoming.PollSnapshot[0] != 3 || incoming.PollSnapshot[1] != 1 || incoming.PollProvider != "telegram:poll-out" || incoming.PollProviderReference != "9001" {
			t.Fatalf("unexpected snapshot: %+v", incoming)
		}
		if incoming.ChildScope == nil || incoming.ChildScope.RemoteID != "11" {
			t.Fatalf("topic lost: %+v", incoming.ChildScope)
		}
	case <-time.After(time.Second):
		t.Fatal("missing poll snapshot")
	}
	if strings.Contains(string(store.data), "123456") {
		t.Fatal("voter identity leaked into persisted poll state")
	}
}

func TestMTProtoPollCorrelationSurvivesRestartAndIsConnectionScoped(t *testing.T) {
	mkAuth := func() *fakeMTLiveAuth {
		a := &fakeMTLiveAuth{groups: []mtprotoGroup{{Peer: mtprotoPeerState{RemoteID: "-1000000000042", Kind: "channel", ID: 42, AccessHash: 9}}}}
		a.authorized = true
		return a
	}
	storeOne := pollStore(t)
	first := openPollAdapter(t, "poll-one", storeOne, mkAuth())
	if err := first.rememberMTProtoPoll(context.Background(), 444, mtprotoPollState{RemoteID: "-1000000000042", MessageID: 9, OptionKeys: []string{"MA", "MQ"}}); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()

	restarted := openPollAdapter(t, "poll-one", storeOne, mkAuth())
	defer restarted.Close()
	other := openPollAdapter(t, "poll-two", pollStore(t), mkAuth())
	defer other.Close()
	update := &tg.UpdateMessagePoll{PollID: 444, Results: tg.PollResults{Results: []tg.PollAnswerVoters{{Option: []byte("0"), Voters: 2}, {Option: []byte("1"), Voters: 5}}}}
	restarted.handleMTProtoPollUpdate(context.Background(), update)
	other.handleMTProtoPollUpdate(context.Background(), update)
	select {
	case incoming := <-restarted.Events():
		if incoming.PollSnapshot[0] != 2 || incoming.PollSnapshot[1] != 5 {
			t.Fatalf("restart snapshot: %+v", incoming)
		}
	case <-time.After(time.Second):
		t.Fatal("restart lost poll correlation")
	}
	select {
	case incoming := <-other.Events():
		t.Fatalf("poll correlation leaked across connections: %+v", incoming)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestMTProtoPollUpdateWithPeerCanSeedCorrelationWithoutVoterDetails(t *testing.T) {
	auth := &fakeMTLiveAuth{groups: []mtprotoGroup{{Peer: mtprotoPeerState{RemoteID: "-1000000000042", Kind: "channel", ID: 42, AccessHash: 9}}}}
	auth.authorized = true
	adapter := openPollAdapter(t, "poll-seed", pollStore(t), auth)
	defer adapter.Close()
	poll := nativePollMessage(8, 333, false, 0).Media.(*tg.MessageMediaPoll).Poll
	update := &tg.UpdateMessagePoll{PollID: 333, Results: tg.PollResults{Results: []tg.PollAnswerVoters{{Option: []byte("0"), Voters: 4}, {Option: []byte("1"), Voters: 6}}}}
	update.SetPeer(&tg.PeerChannel{ChannelID: 42})
	update.SetMsgID(8)
	update.SetTopMsgID(12)
	update.SetPoll(poll)
	adapter.handleMTProtoPollUpdate(context.Background(), update)
	select {
	case incoming := <-adapter.Events():
		if incoming.PollSnapshot[0] != 4 || incoming.PollSnapshot[1] != 6 || incoming.ChildScope == nil || incoming.ChildScope.RemoteID != "12" {
			t.Fatalf("seeded snapshot: %+v", incoming)
		}
	case <-time.After(time.Second):
		t.Fatal("missing seeded snapshot")
	}
}
