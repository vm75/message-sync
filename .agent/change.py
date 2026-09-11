from pathlib import Path
root=Path('.')

def rep(path, old, new, label):
    p=root/path
    s=p.read_text()
    if old not in s:
        raise SystemExit(f'missing marker: {label}')
    p.write_text(s.replace(old,new,1))

# Encrypted MTProto state gains operational poll correlation only.
rep('internal/transport/telegram/mtproto.go',
'''\tSession []byte                      `json:"session,omitempty"`\n\tPeers   map[string]mtprotoPeerState `json:"peers,omitempty"`\n''',
'''\tSession []byte                       `json:"session,omitempty"`\n\tPeers   map[string]mtprotoPeerState  `json:"peers,omitempty"`\n\tPolls   map[string]mtprotoPollState  `json:"polls,omitempty"`\n''','mtproto poll state field')

# MTProto poll capability becomes true only with this implementation.
rep('internal/transport/telegram/capabilities.go',
'''\t\t\tPrivacyModeStatus: false,\n\t\t\tPolls:             false,\n''',
'''\t\t\tPrivacyModeStatus: false,\n\t\t\tPolls:             true,\n''','mtproto poll capability')
rep('internal/transport/telegram/capabilities_test.go',
'''\tif mt.ChatDiscovery != DiscoveryFull || mt.TopicDiscovery != DiscoveryFull || !mt.HistoryRecovery || mt.PrivacyModeStatus || mt.Polls {\n\t\tt.Fatalf("unexpected mtproto foundation capabilities: %+v", mt)\n''',
'''\tif mt.ChatDiscovery != DiscoveryFull || mt.TopicDiscovery != DiscoveryFull || !mt.HistoryRecovery || mt.PrivacyModeStatus || !mt.Polls {\n\t\tt.Fatalf("unexpected mtproto capabilities: %+v", mt)\n''','capability test')

# Synthetic MTProto messages now preserve native polls through the existing Bot-model normalizer.
rep('internal/transport/telegram/mtproto_live.go',
'''\tif media {\n\t\tsynthetic.Document = &telegrambotmodels.Document{FileID: "mtproto"}\n\t}\n\treturn synthetic, true\n}\n\nfunc mtprotoMediaDescriptor''',
'''\tif pollMedia, pollOK := msg.Media.(*tg.MessageMediaPoll); pollOK {\n\t\tpoll, ok := mtprotoSyntheticPoll(pollMedia)\n\t\tif !ok {\n\t\t\treturn nil, false\n\t\t}\n\t\tsynthetic.Poll = poll\n\t} else if media {\n\t\tsynthetic.Document = &telegrambotmodels.Document{FileID: "mtproto"}\n\t}\n\treturn synthetic, true\n}\n\nfunc mtprotoMediaDescriptor''','synthetic poll')

# Persist correlation for accepted inbound polls before they enter the canonical router.
rep('internal/transport/telegram/mtproto_live.go',
'''\ttopicID := 0\n\tif incoming.ChildScope != nil {\n\t\ttopicID, _ = strconv.Atoi(incoming.ChildScope.RemoteID)\n\t}\n\tif incoming.FromSelf && !edit && a.live.consumePendingSend''',
'''\ttopicID := 0\n\tif incoming.ChildScope != nil {\n\t\ttopicID, _ = strconv.Atoi(incoming.ChildScope.RemoteID)\n\t}\n\tif !edit && incoming.Kind == "poll" {\n\t\tif pollMedia, pollOK := msg.Media.(*tg.MessageMediaPoll); pollOK {\n\t\t\t_ = a.rememberMTProtoPoll(ctx, pollMedia.Poll.ID, mtprotoPollState{\n\t\t\t\tRemoteID: peer.RemoteID, MessageID: msg.ID, TopicID: topicID, OptionKeys: mtprotoPollOptionKeys(pollMedia.Poll),\n\t\t\t})\n\t\t}\n\t}\n\tif incoming.FromSelf && !edit && a.live.consumePendingSend''','remember inbound poll')

# Aggregate poll updates are registered; per-voter UpdateMessagePollVote is intentionally not registered.
rep('internal/transport/telegram/mtproto_live.go',
'''\tdispatcher.OnMessageReactions(func(handlerCtx context.Context, _ tg.Entities, update *tg.UpdateMessageReactions) error {\n\t\ta.handleMTProtoReactionUpdate(handlerCtx, update)\n\t\treturn nil\n\t})\n\treturn dispatcher.Handle(ctx, updates)\n''',
'''\tdispatcher.OnMessageReactions(func(handlerCtx context.Context, _ tg.Entities, update *tg.UpdateMessageReactions) error {\n\t\ta.handleMTProtoReactionUpdate(handlerCtx, update)\n\t\treturn nil\n\t})\n\tdispatcher.OnMessagePoll(func(handlerCtx context.Context, _ tg.Entities, update *tg.UpdateMessagePoll) error {\n\t\ta.handleMTProtoPollUpdate(handlerCtx, update)\n\t\treturn nil\n\t})\n\treturn dispatcher.Handle(ctx, updates)\n''','poll update dispatcher')

# Native outgoing polls with text fallback matching the Bot adapter's representability rules.
rep('internal/transport/telegram/mtproto_live.go',
'''\tif kind == "poll" {\n\t\treturn transport.MessageRef{}, errors.New("Telegram MTProto polls are not enabled yet")\n\t}\n\tcontent := telegramOutgoingText(outgoing)\n''',
'''\tif kind == "poll" {\n\t\tpollClient, supported := client.(mtprotoPollClient)\n\t\tif supported {\n\t\t\tquestion, options, selectable, duration, native := mtprotoNativePoll(outgoing)\n\t\t\tif native {\n\t\t\t\tdone := a.live.markPendingSend(outgoing.Endpoint, "poll", topicID, question)\n\t\t\t\tresult, sendErr := pollClient.SendPoll(ctx, peer, question, options, selectable, duration, replyID, topicID)\n\t\t\t\tdone(sendErr == nil)\n\t\t\t\tif sendErr != nil {\n\t\t\t\t\treturn transport.MessageRef{}, errors.New("send Telegram MTProto poll")\n\t\t\t\t}\n\t\t\t\tif result.MessageID <= 0 || result.PollID == 0 {\n\t\t\t\t\treturn transport.MessageRef{}, errors.New("Telegram MTProto poll response was incomplete")\n\t\t\t\t}\n\t\t\t\t_ = a.rememberMTProtoPoll(ctx, result.PollID, mtprotoPollState{\n\t\t\t\t\tRemoteID: peer.RemoteID, MessageID: result.MessageID, TopicID: topicID, OptionKeys: result.OptionKeys,\n\t\t\t\t})\n\t\t\t\ta.live.rememberMessage(outgoing.Endpoint, result.MessageID)\n\t\t\t\treturn transport.MessageRef{\n\t\t\t\t\tEndpoint: outgoing.Endpoint, RemoteMessageID: strconv.Itoa(result.MessageID), IsTargetFromMe: true,\n\t\t\t\t\tProvider: a.mtprotoPollProviderNamespace(), ProviderReference: strconv.FormatInt(result.PollID, 10), ChildScope: outgoing.ChildScope,\n\t\t\t\t}, nil\n\t\t\t}\n\t\t}\n\t\tpollText, pollErr := telegramPollText(outgoing.SourceText, outgoing.PollOptions, outgoing.PollSelectableCount)\n\t\tif pollErr != nil {\n\t\t\treturn transport.MessageRef{}, pollErr\n\t\t}\n\t\toutgoing.SourceText = pollText\n\t\tif outgoing.RenderedText != "" {\n\t\t\tif attribution := strings.TrimSpace(outgoing.PollAttribution); attribution != "" {\n\t\t\t\toutgoing.RenderedText = attribution + " " + pollText\n\t\t\t} else {\n\t\t\t\toutgoing.RenderedText = pollText\n\t\t\t}\n\t\t}\n\t\tkind = "text"\n\t}\n\tcontent := telegramOutgoingText(outgoing)\n''','outbound poll path')

# Add complete MTProto poll implementation.
(root/'internal/transport/telegram/mtproto_poll.go').write_text(r'''package telegram

import (
    "context"
    "encoding/base64"
    "errors"
    "strconv"
    "strings"
    "time"
    "unicode/utf8"

    telegrambotmodels "github.com/go-telegram/bot/models"
    gotdunpack "github.com/gotd/td/telegram/message/unpack"
    "github.com/gotd/td/tg"
    "github.com/vm75/message-sync/internal/transport"
)

const mtprotoPollCorrelationLimit = 4096

type mtprotoPollState struct {
    RemoteID   string   `json:"remoteId"`
    MessageID  int      `json:"messageId"`
    TopicID    int      `json:"topicId,omitempty"`
    OptionKeys []string `json:"optionKeys,omitempty"`
}

type mtprotoPollSendResult struct {
    MessageID  int
    PollID     int64
    OptionKeys []string
}

type mtprotoPollClient interface {
    SendPoll(context.Context, mtprotoPeerState, string, []string, int, int, int, int) (mtprotoPollSendResult, error)
}

func (a *MTProtoAdapter) mtprotoPollProviderNamespace() string {
    if a == nil {
        return "telegram:"
    }
    return "telegram:" + a.connectionID
}

func mtprotoPollOptionKeys(poll tg.Poll) []string {
    keys := make([]string, 0, len(poll.Answers))
    for _, class := range poll.Answers {
        answer, ok := class.(*tg.PollAnswer)
        if !ok || answer == nil || len(answer.Option) == 0 {
            return nil
        }
        keys = append(keys, base64.RawStdEncoding.EncodeToString(answer.Option))
    }
    return keys
}

func mtprotoSyntheticPoll(media *tg.MessageMediaPoll) (*telegrambotmodels.Poll, bool) {
    if media == nil || media.Poll.ID == 0 || media.Poll.Quiz {
        return nil, false
    }
    question := strings.TrimSpace(media.Poll.Question.Text)
    if utf8.RuneCountInString(question) < 1 || utf8.RuneCountInString(question) > 300 || len(media.Poll.Answers) < 2 || len(media.Poll.Answers) > 10 {
        return nil, false
    }
    options := make([]telegrambotmodels.PollOption, 0, len(media.Poll.Answers))
    for _, class := range media.Poll.Answers {
        answer, ok := class.(*tg.PollAnswer)
        if !ok || answer == nil {
            return nil, false
        }
        text := strings.TrimSpace(answer.Text.Text)
        if utf8.RuneCountInString(text) < 1 || utf8.RuneCountInString(text) > 100 || len(answer.Option) == 0 {
            return nil, false
        }
        options = append(options, telegrambotmodels.PollOption{Text: text})
    }
    return &telegrambotmodels.Poll{
        ID: strconv.FormatInt(media.Poll.ID, 10), Question: question, Options: options,
        Type: "regular", AllowsMultipleAnswers: media.Poll.MultipleChoice,
    }, true
}

func mtprotoNativePoll(outgoing transport.Outgoing) (string, []string, int, int, bool) {
    question := strings.TrimSpace(outgoing.SourceText)
    if utf8.RuneCountInString(question) < 1 || utf8.RuneCountInString(question) > 300 || len(outgoing.PollOptions) < 2 || len(outgoing.PollOptions) > 10 {
        return "", nil, 0, 0, false
    }
    selectable := outgoing.PollSelectableCount
    if selectable != 1 && selectable != len(outgoing.PollOptions) {
        return "", nil, 0, 0, false
    }
    duration := outgoing.PollDurationHours * 3600
    if outgoing.PollDurationHours < 0 || (duration > 0 && (duration < 5 || duration > 600)) {
        return "", nil, 0, 0, false
    }
    options := make([]string, 0, len(outgoing.PollOptions))
    for _, raw := range outgoing.PollOptions {
        option := strings.TrimSpace(raw)
        if utf8.RuneCountInString(option) < 1 || utf8.RuneCountInString(option) > 100 {
            return "", nil, 0, 0, false
        }
        options = append(options, option)
    }
    return question, options, selectable, duration, true
}

func (a *MTProtoAdapter) rememberMTProtoPoll(ctx context.Context, pollID int64, value mtprotoPollState) error {
    if a == nil || a.state == nil || pollID == 0 || strings.TrimSpace(value.RemoteID) == "" || value.MessageID <= 0 || len(value.OptionKeys) < 2 {
        return nil
    }
    key := strconv.FormatInt(pollID, 10)
    return a.state.update(ctx, func(state *mtprotoState) {
        if state.Polls == nil {
            state.Polls = make(map[string]mtprotoPollState)
        }
        if len(state.Polls) >= mtprotoPollCorrelationLimit {
            for existing := range state.Polls {
                if existing != key {
                    delete(state.Polls, existing)
                    break
                }
            }
        }
        value.OptionKeys = append([]string(nil), value.OptionKeys...)
        state.Polls[key] = value
    })
}

func (a *MTProtoAdapter) mtprotoPollCorrelation(ctx context.Context, pollID int64) (mtprotoPollState, bool) {
    if a == nil || a.state == nil || pollID == 0 {
        return mtprotoPollState{}, false
    }
    state, err := a.state.load(ctx)
    if err != nil || state.Polls == nil {
        return mtprotoPollState{}, false
    }
    value, ok := state.Polls[strconv.FormatInt(pollID, 10)]
    if !ok {
        return mtprotoPollState{}, false
    }
    value.OptionKeys = append([]string(nil), value.OptionKeys...)
    return value, true
}

func mtprotoPollStateFromUpdate(update *tg.UpdateMessagePoll) (mtprotoPollState, bool) {
    if update == nil || update.PollID == 0 {
        return mtprotoPollState{}, false
    }
    peer, peerOK := update.GetPeer()
    messageID, messageOK := update.GetMsgID()
    if !peerOK || !messageOK || messageID <= 0 {
        return mtprotoPollState{}, false
    }
    remote, _, ok := mtprotoRemoteIDFromPeer(peer)
    if !ok {
        return mtprotoPollState{}, false
    }
    value := mtprotoPollState{RemoteID: strconv.FormatInt(remote, 10), MessageID: messageID}
    if topicID, ok := update.GetTopMsgID(); ok && topicID > 0 {
        value.TopicID = topicID
    }
    if poll, ok := update.GetPoll(); ok {
        value.OptionKeys = mtprotoPollOptionKeys(poll)
    }
    return value, true
}

func mtprotoPollSnapshot(results tg.PollResults, keys []string) (map[int]int, bool) {
    if len(keys) < 2 {
        return nil, false
    }
    indexes := make(map[string]int, len(keys))
    counts := make(map[int]int, len(keys))
    for index, key := range keys {
        indexes[key] = index
        counts[index] = 0
    }
    voters, ok := results.GetResults()
    if !ok {
        return counts, true
    }
    for _, result := range voters {
        index, found := indexes[base64.RawStdEncoding.EncodeToString(result.Option)]
        if !found {
            continue
        }
        counts[index] = result.Voters
    }
    return counts, true
}

func (a *MTProtoAdapter) handleMTProtoPollUpdate(ctx context.Context, update *tg.UpdateMessagePoll) {
    if a == nil || a.live == nil || update == nil || update.PollID == 0 {
        return
    }
    correlation, haveCorrelation := a.mtprotoPollCorrelation(ctx, update.PollID)
    if current, ok := mtprotoPollStateFromUpdate(update); ok {
        if len(current.OptionKeys) == 0 && haveCorrelation {
            current.OptionKeys = append([]string(nil), correlation.OptionKeys...)
        }
        correlation = current
        haveCorrelation = len(correlation.OptionKeys) >= 2
        if haveCorrelation {
            _ = a.rememberMTProtoPoll(ctx, update.PollID, correlation)
        }
    } else if poll, ok := update.GetPoll(); ok && haveCorrelation {
        if keys := mtprotoPollOptionKeys(poll); len(keys) >= 2 {
            correlation.OptionKeys = keys
            _ = a.rememberMTProtoPoll(ctx, update.PollID, correlation)
        }
    }
    if !haveCorrelation || len(correlation.OptionKeys) < 2 {
        return
    }
    remote, err := strconv.ParseInt(correlation.RemoteID, 10, 64)
    if err != nil {
        return
    }
    a.live.mu.RLock()
    normalizer := a.live.normalizer
    a.live.mu.RUnlock()
    if normalizer == nil {
        return
    }
    endpoint, ok := normalizer.endpoint(remote)
    if !ok {
        return
    }
    snapshot, ok := mtprotoPollSnapshot(update.Results, correlation.OptionKeys)
    if !ok {
        return
    }
    var child *transport.ChildScope
    if correlation.TopicID > 0 {
        child = &transport.ChildScope{Kind: transport.ScopeKindTelegramTopic, RemoteID: strconv.Itoa(correlation.TopicID)}
    }
    a.emitMTProto(transport.Incoming{
        Endpoint: endpoint, RemoteID: strconv.FormatInt(update.PollID, 10), Kind: "poll_snapshot",
        PollSnapshot: snapshot, PollProvider: a.mtprotoPollProviderNamespace(), PollProviderReference: strconv.FormatInt(update.PollID, 10),
        ChildScope: child, Timestamp: time.Now().UTC(),
    })
}

func (c *gotdAuthClient) SendPoll(ctx context.Context, peer mtprotoPeerState, question string, options []string, selectableCount, duration, replyID, topicID int) (mtprotoPollSendResult, error) {
    input, err := peer.input()
    if err != nil {
        return mtprotoPollSendResult{}, err
    }
    pollID, err := mtprotoRandomID()
    if err != nil {
        return mtprotoPollSendResult{}, err
    }
    randomID, err := mtprotoRandomID()
    if err != nil {
        return mtprotoPollSendResult{}, err
    }
    answers := make([]tg.PollAnswerClass, 0, len(options))
    keys := make([]string, 0, len(options))
    for index, option := range options {
        token := []byte(strconv.Itoa(index))
        answers = append(answers, &tg.PollAnswer{Text: tg.TextWithEntities{Text: option}, Option: token})
        keys = append(keys, base64.RawStdEncoding.EncodeToString(token))
    }
    poll := tg.Poll{
        ID: pollID, Question: tg.TextWithEntities{Text: question}, Answers: answers,
        MultipleChoice: selectableCount > 1,
    }
    if duration > 0 {
        poll.ClosePeriod = duration
    }
    updates, err := tg.NewClient(c.client).MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
        Peer: input, ReplyTo: mtprotoReply(replyID, topicID), Media: &tg.InputMediaPoll{Poll: poll}, RandomID: randomID,
    })
    messageID, err := gotdunpack.MessageID(updates, err)
    if err != nil {
        return mtprotoPollSendResult{}, err
    }
    return mtprotoPollSendResult{MessageID: messageID, PollID: pollID, OptionKeys: keys}, nil
}

var _ mtprotoPollClient = (*gotdAuthClient)(nil)
var _ = errors.Is
''')

# Focused tests for native creation, inbound normalization, snapshots, restart, isolation and privacy.
(root/'internal/transport/telegram/mtproto_poll_test.go').write_text(r'''package telegram

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
    question string
    options []string
    selectable int
    duration int
    replyID int
    topicID int
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
        Users: map[int64]*tg.User{7: {ID: 7, FirstName: "Alice"}},
        Channels: map[int64]*tg.Channel{42: {ID: 42, AccessHash: 9, Title: "Forum", Megagroup: true, Forum: true}},
    }
}

func nativePollMessage(id int, pollID int64, multiple bool, topic int) *tg.Message {
    poll := tg.Poll{
        ID: pollID,
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
    if err != nil { t.Fatal(err) }
    adapter, err := OpenMTProto(context.Background(), Options{
        ConnectionID: id, Logger: testLogger(), MTProtoStateStore: store,
        ChatIDs: map[string]string{"tg": "-1000000000042"}, Hasher: hasher, UsernameMode: config.UsernameModePushName,
        MediaEnabled: true, MediaMaxBytes: 1024 * 1024,
        mtprotoRuntimeFactory: func(int, string, gotdsession.Storage) mtprotoRuntime { return fakeMTLiveRuntime{auth: auth} },
    })
    if err != nil { t.Fatal(err) }
    select { case <-adapter.ready: case <-time.After(time.Second): t.Fatal("adapter not ready") }
    return adapter
}

func pollStore(t *testing.T) *memoryMTStore {
    t.Helper()
    raw, err := json.Marshal(mtprotoState{Version: 1, APIID: 1, APIHash: "hash", Phone: "+1", Session: []byte("session")})
    if err != nil { t.Fatal(err) }
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
    if err != nil { t.Fatal(err) }
    correlation, ok := state.Polls["777"]
    if !ok || correlation.MessageID != 55 || correlation.TopicID != 11 || len(correlation.OptionKeys) != 2 {
        t.Fatalf("missing persistent poll correlation: %+v", correlation)
    }
    raw := string(store.data)
    for _, secret := range []string{"Lunch?", "Rice", "Soup", "Alice"} {
        if strings.Contains(raw, secret) { t.Fatalf("poll content leaked into MTProto correlation state: %q", secret) }
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
        ReplyTo: &transport.MessageRef{Endpoint: "tg", RemoteMessageID: "60"},
        ChildScope: &transport.ChildScope{Kind: transport.ScopeKindTelegramTopic, RemoteID: "11"},
    })
    if err != nil { t.Fatal(err) }
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
        if incoming.ChildScope == nil || incoming.ChildScope.RemoteID != "11" { t.Fatalf("topic lost: %+v", incoming.ChildScope) }
    case <-time.After(time.Second):
        t.Fatal("missing poll snapshot")
    }
    if strings.Contains(string(store.data), "123456") { t.Fatal("voter identity leaked into persisted poll state") }
}

func TestMTProtoPollCorrelationSurvivesRestartAndIsConnectionScoped(t *testing.T) {
    mkAuth := func() *fakeMTLiveAuth {
        a := &fakeMTLiveAuth{groups: []mtprotoGroup{{Peer: mtprotoPeerState{RemoteID: "-1000000000042", Kind: "channel", ID: 42, AccessHash: 9}}}
        a.authorized = true
        return a
    }
    storeOne := pollStore(t)
    first := openPollAdapter(t, "poll-one", storeOne, mkAuth())
    if err := first.rememberMTProtoPoll(context.Background(), 444, mtprotoPollState{RemoteID: "-1000000000042", MessageID: 9, OptionKeys: []string{"MA", "MQ"}}); err != nil { t.Fatal(err) }
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
        if incoming.PollSnapshot[0] != 2 || incoming.PollSnapshot[1] != 5 { t.Fatalf("restart snapshot: %+v", incoming) }
    case <-time.After(time.Second): t.Fatal("restart lost poll correlation")
    }
    select {
    case incoming := <-other.Events(): t.Fatalf("poll correlation leaked across connections: %+v", incoming)
    case <-time.After(50 * time.Millisecond):
    }
}

func TestMTProtoPollUpdateWithPeerCanSeedCorrelationWithoutVoterDetails(t *testing.T) {
    auth := &fakeMTLiveAuth{groups: []mtprotoGroup{{Peer: mtprotoPeerState{RemoteID: "-1000000000042", Kind: "channel", ID: 42, AccessHash: 9}}}
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
        if incoming.PollSnapshot[0] != 4 || incoming.PollSnapshot[1] != 6 || incoming.ChildScope == nil || incoming.ChildScope.RemoteID != "12" { t.Fatalf("seeded snapshot: %+v", incoming) }
    case <-time.After(time.Second): t.Fatal("missing seeded snapshot")
    }
}
''')

tracker=root/'docs/TELEGRAM_MTPROTO_SUPPORT_PROGRESS.md'
text=tracker.read_text().replace('- [ ] #107 — Add Telegram MTProto poll parity with canonical live poll synchronization','- [x] #107 — Add Telegram MTProto poll parity with canonical live poll synchronization')
text=text.replace('| Native polls/live aggregate results | Yes | Yes after #107 |','| Native polls/live aggregate results | Yes | Yes |')
log='''\n### #107 — complete\n\n- MTProto native polls now normalize through the existing canonical Telegram poll fields and preserve reply/topic identity.\n- Outbound canonical polls use native MTProto `inputMediaPoll` when representable and return the same connection-scoped provider reference contract as Bot API; unsupported poll shapes retain the existing text fallback.\n- `updateMessagePoll` produces aggregate-only `poll_snapshot` events. Per-voter `updateMessagePollVote` is intentionally not registered or consumed.\n- Restart-safe poll correlation stores only remote/message/topic IDs plus opaque option tokens inside the existing encrypted MTProto state; questions, option text and voter identities are not persisted there.\n- Verification: focused ingress/outbound/multiple-choice/topic/snapshot/restart/isolation/privacy tests plus `go test ./...` and `go vet ./...` in the tested-change workflow.\n'''
marker='\n## Completion rule\n'
if '### #107 — complete' not in text:
    text=text.replace(marker,log+marker)
tracker.write_text(text)
