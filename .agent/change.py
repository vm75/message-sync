from pathlib import Path
import runpy

runpy.run_path('.agent/base.py', run_name='__main__')
Path('.agent/base.py').unlink(missing_ok=True)

Path('internal/transport/telegram/mtproto_recovery_test.go').write_text(r'''package telegram

import (
    "context"
    "encoding/json"
    "errors"
    "testing"
    "time"

    gotdsession "github.com/gotd/td/session"
    "github.com/gotd/td/tg"
    "github.com/vm75/message-sync/internal/config"
    "github.com/vm75/message-sync/internal/identity"
    "github.com/vm75/message-sync/internal/transport"
)

type fakeMTRecoveryAuth struct {
    *fakeMTLiveAuth
    pages []mtprotoHistoryPage
}

func (f *fakeMTRecoveryAuth) History(ctx context.Context, _ mtprotoPeerState, minID, offsetID, limit int) (mtprotoHistoryPage, error) {
    if err := ctx.Err(); err != nil {
        return mtprotoHistoryPage{}, err
    }
    for _, page := range f.pages {
        if len(page.Messages) == 0 {
            continue
        }
        newest := page.Messages[0].ID
        oldest := page.Messages[len(page.Messages)-1].ID
        if offsetID == 0 || (newest < offsetID && oldest > minID) {
            return page, nil
        }
    }
    return mtprotoHistoryPage{}, nil
}

func newRecoveryAdapter(t *testing.T, id string, auth *fakeMTRecoveryAuth, max uint64) *MTProtoAdapter {
    t.Helper()
    auth.authorized = true
    state, err := json.Marshal(mtprotoState{Version: 1, APIID: 1, APIHash: "hash", Phone: "+1", Session: []byte("s")})
    if err != nil { t.Fatal(err) }
    store := &memoryMTStore{data: state}
    hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
    if err != nil { t.Fatal(err) }
    a, err := OpenMTProto(context.Background(), Options{
        ConnectionID: id, Logger: testLogger(), MTProtoStateStore: store,
        ChatIDs: map[string]string{"tg": "-1000000000042"}, Hasher: hasher,
        UsernameMode: config.UsernameModePushName, MediaEnabled: true, MediaMaxBytes: max,
        mtprotoRuntimeFactory: func(int, string, gotdsession.Storage) mtprotoRuntime {
            return fakeMTLiveRuntime{auth: auth}
        },
    })
    if err != nil { t.Fatal(err) }
    select {
    case <-a.ready:
    case <-time.After(time.Second): t.Fatal("not ready")
    }
    return a
}

func historyEntities() tg.Entities {
    return tg.Entities{
        Users: map[int64]*tg.User{7: {ID: 7, FirstName: "A"}},
        Channels: map[int64]*tg.Channel{42: {ID: 42, AccessHash: 9, Title: "G", Megagroup: true, Forum: true}},
    }
}

func histMsg(id int, when time.Time, topic int) *tg.Message {
    m := &tg.Message{ID: id, PeerID: &tg.PeerChannel{ChannelID: 42}, FromID: &tg.PeerUser{UserID: 7}, Date: int(when.Unix()), Message: "m"}
    if topic > 0 {
        m.ReplyTo = &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: topic, ReplyToTopID: topic}
    }
    return m
}

func TestMTProtoRecoveryOrderingBoundsCursorAndTopics(t *testing.T) {
    now := time.Now().UTC()
    ents := historyEntities()
    auth := &fakeMTRecoveryAuth{
        fakeMTLiveAuth: &fakeMTLiveAuth{groups: []mtprotoGroup{{Peer: mtprotoPeerState{RemoteID: "-1000000000042", Kind: "channel", ID: 42, AccessHash: 9}, Forum: true}}},
        pages: []mtprotoHistoryPage{{Messages: []*tg.Message{histMsg(4, now, 17), histMsg(3, now.Add(-time.Minute), 17), histMsg(2, now.Add(-2*time.Minute), 17)}, Entities: ents, More: false}},
    }
    a := newRecoveryAdapter(t, "r1", auth, 1024)
    defer a.Close()
    stream, _ := a.RecoveryStream("tg")
    var got []transport.Incoming
    err := a.Recover(context.Background(), transport.RecoveryRequest{Cursor: transport.Checkpoint{StreamKey: stream, Position: 1, Valid: true}, MaxEvents: 2, MaxAge: time.Hour}, func(_ context.Context, in transport.Incoming) error {
        got = append(got, in)
        return nil
    })
    if err != nil { t.Fatal(err) }
    if len(got) != 2 || got[0].RemoteID != "2" || got[1].RemoteID != "3" { t.Fatalf("ordering/bounds: %+v", got) }
    if got[0].ChildScope == nil || got[0].ChildScope.RemoteID != "17" { t.Fatalf("topic lost: %+v", got[0].ChildScope) }
    if got[0].Checkpoint.StreamKey != stream || got[0].Checkpoint.Position != 2 { t.Fatalf("checkpoint: %+v", got[0].Checkpoint) }
}

func TestMTProtoRecoveryAgeMediaLimitAndCancellation(t *testing.T) {
    now := time.Now().UTC()
    ents := historyEntities()
    big := histMsg(5, now, 0)
    big.Media = &tg.MessageMediaDocument{Document: &tg.Document{ID: 1, AccessHash: 2, FileReference: []byte{1}, Size: 2048, MimeType: "application/octet-stream"}}
    old := histMsg(4, now.Add(-48*time.Hour), 0)
    auth := &fakeMTRecoveryAuth{
        fakeMTLiveAuth: &fakeMTLiveAuth{groups: []mtprotoGroup{{Peer: mtprotoPeerState{RemoteID: "-1000000000042", Kind: "channel", ID: 42, AccessHash: 9}}}},
        pages: []mtprotoHistoryPage{{Messages: []*tg.Message{big, old}, Entities: ents}},
    }
    a := newRecoveryAdapter(t, "r2", auth, 1024)
    defer a.Close()
    stream, _ := a.RecoveryStream("tg")
    var got []transport.Incoming
    if err := a.Recover(context.Background(), transport.RecoveryRequest{Cursor: transport.Checkpoint{StreamKey: stream, Valid: true}, MaxEvents: 10, MaxAge: time.Hour}, func(_ context.Context, in transport.Incoming) error {
        got = append(got, in)
        return nil
    }); err != nil { t.Fatal(err) }
    if len(got) != 1 || got[0].MediaLoader == nil { t.Fatalf("age/media: %+v", got) }
    if _, err := got[0].MediaLoader(context.Background()); !errors.Is(err, errMTProtoMediaTooLarge) { t.Fatalf("media limit err=%v", err) }
    ctx, cancel := context.WithCancel(context.Background())
    cancel()
    if err := a.Recover(ctx, transport.RecoveryRequest{Cursor: transport.Checkpoint{StreamKey: stream, Valid: true}, MaxEvents: 1, MaxAge: time.Hour}, func(context.Context, transport.Incoming) error { return nil }); !errors.Is(err, context.Canceled) { t.Fatalf("cancel err=%v", err) }
}

func TestMTProtoRecoveryStreamsAndLiveCheckpointAreConnectionScoped(t *testing.T) {
    auth := &fakeMTRecoveryAuth{fakeMTLiveAuth: &fakeMTLiveAuth{groups: []mtprotoGroup{{Peer: mtprotoPeerState{RemoteID: "-1000000000042", Kind: "channel", ID: 42, AccessHash: 9}}}}}
    a := newRecoveryAdapter(t, "r-one", auth, 1024)
    defer a.Close()
    b := newRecoveryAdapter(t, "r-two", auth, 1024)
    defer b.Close()
    as, _ := a.RecoveryStream("tg")
    bs, _ := b.RecoveryStream("tg")
    if as == bs { t.Fatal("connection streams collided") }
    a.normalizeMTProtoMessage(context.Background(), historyEntities(), histMsg(9, time.Now(), 0), false)
    select {
    case in := <-a.Events():
        if in.Checkpoint.StreamKey != as || in.Checkpoint.Position != 9 { t.Fatalf("live checkpoint %+v", in.Checkpoint) }
    case <-time.After(time.Second):
        t.Fatal("missing live event")
    }
}
''')
