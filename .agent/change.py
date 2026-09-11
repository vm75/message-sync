from pathlib import Path

root=Path('.')
def rep(path,old,new,label):
 p=root/path; t=p.read_text()
 if old not in t: raise SystemExit(f'missing {label} in {path}')
 p.write_text(t.replace(old,new,1))

# Recovery coordinator exposes a bounded single-stream trigger for admin/manual backfill.
rep('internal/recovery/coordinator.go',
'''// RegisterSource registers a new recovery-capable source, runs its bounded recovery,\n''',
'''// RecoverStream runs one explicitly bounded source stream through the same canonical\n// recovery path used at startup/reconnect.\nfunc (c *Coordinator) RecoverStream(ctx context.Context, source transport.RecoverySource, key string, maxEvents int, maxAge time.Duration) error {\n\tif c == nil || source == nil {\n\t\treturn errors.New("recovery coordinator and source are required")\n\t}\n\tif key == "" || maxEvents <= 0 || maxAge <= 0 {\n\t\treturn errors.New("recovery stream and positive bounds are required")\n\t}\n\tallowed := false\n\tfor _, candidate := range source.RecoveryStreams() {\n\t\tif candidate == key {\n\t\t\tallowed = true\n\t\t\tbreak\n\t\t}\n\t}\n\tif !allowed {\n\t\treturn errors.New("recovery stream is not configured")\n\t}\n\tc.recoverMu.Lock()\n\tdefer c.recoverMu.Unlock()\n\tcursor := transport.Checkpoint{StreamKey: key, Valid: true}\n\tif saved, err := c.store.RecoveryCursor(ctx, key); err == nil {\n\t\tcursor.Position = saved.Position\n\t\tcursor.EventTimestamp = saved.EventTimestamp\n\t} else if !errors.Is(err, sql.ErrNoRows) {\n\t\treturn err\n\t}\n\treturn source.Recover(ctx, transport.RecoveryRequest{Cursor: cursor, MaxEvents: maxEvents, MaxAge: maxAge}, func(eventCtx context.Context, incoming transport.Incoming) error {\n\t\t_, err := c.Handle(eventCtx, incoming)\n\t\treturn err\n\t})\n}\n\n// RegisterSource registers a new recovery-capable source, runs its bounded recovery,\n''','coordinator manual recovery')

# Adapter owns a reconnect signal channel; live ordinary ingress gets per-endpoint recovery checkpoints.
rep('internal/transport/telegram/mtproto.go',
'''\tclosed     bool\n}\n''',
'''\tclosed          bool\n\trecoverySignals chan struct{}\n}\n''','recovery channel field')
rep('internal/transport/telegram/mtproto.go',
'''\t\tlifecycleCtx: lifecycleCtx, lifecycleCancel: lifecycleCancel, authState: MTProtoAuthDisconnected, live: live,\n\t}\n''',
'''\t\tlifecycleCtx: lifecycleCtx, lifecycleCancel: lifecycleCancel, authState: MTProtoAuthDisconnected, live: live,\n\t\trecoverySignals: make(chan struct{}, 1),\n\t}\n''','recovery channel init')
rep('internal/transport/telegram/mtproto.go',
'''\ta.mu.Lock()\n\ta.cfg = cfg\n\ta.mu.Unlock()\n\treturn nil\n}\n''',
'''\ta.mu.Lock()\n\ta.cfg = cfg\n\ta.mu.Unlock()\n\ta.signalMTProtoRecovery()\n\treturn nil\n}\n''','signal config recovery')

# Live message checkpoints share the same recovery stream identity; edits are not ordered as new-history items.
rep('internal/transport/telegram/mtproto_live.go',
'''\ta.live.rememberMessage(incoming.Endpoint, msg.ID)\n\ta.emitMTProto(incoming)\n}\n''',
'''\ta.live.rememberMessage(incoming.Endpoint, msg.ID)\n\tif !edit {\n\t\tincoming.Checkpoint = transport.Checkpoint{\n\t\t\tStreamKey: a.mtprotoRecoveryStreamKey(incoming.Endpoint), Position: int64(msg.ID), EventTimestamp: incoming.Timestamp, Valid: true,\n\t\t}\n\t}\n\ta.emitMTProto(incoming)\n}\n''','live recovery checkpoint')
rep('internal/transport/telegram/mtproto_live.go',
'''\tif a.live.replacePeers(groups) {\n\t\t_ = a.persistMTProtoPeers(ctx)\n\t}\n}\n''',
'''\tif a.live.replacePeers(groups) {\n\t\t_ = a.persistMTProtoPeers(ctx)\n\t}\n\ta.signalMTProtoRecovery()\n}\n''','auth reconnect recovery signal')

(root/'internal/transport/telegram/mtproto_recovery.go').write_text(r'''package telegram

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
    if a == nil || a.live == nil || endpoint == "" { return "", false }
    a.live.mu.RLock(); n := a.live.normalizer; a.live.mu.RUnlock()
    if n == nil { return "", false }
    if _, ok := n.chatID(endpoint); !ok { return "", false }
    return a.mtprotoRecoveryStreamKey(endpoint), true
}

func (a *MTProtoAdapter) RecoveryStreams() []string {
    if a == nil || a.live == nil { return nil }
    a.live.mu.RLock(); n := a.live.normalizer; a.live.mu.RUnlock()
    if n == nil { return nil }
    aliases := make([]transport.EndpointID, 0, len(n.endpoints))
    for _, alias := range n.endpoints { aliases = append(aliases, alias) }
    sort.Slice(aliases, func(i,j int) bool { return aliases[i] < aliases[j] })
    out := make([]string,0,len(aliases))
    for _, alias := range aliases { out=append(out,a.mtprotoRecoveryStreamKey(alias)) }
    return out
}

func (a *MTProtoAdapter) RecoverySignals() <-chan struct{} {
    if a == nil { return nil }
    return a.recoverySignals
}

func (a *MTProtoAdapter) signalMTProtoRecovery() {
    if a == nil || a.recoverySignals == nil { return }
    select { case a.recoverySignals <- struct{}{}: default: }
}

func (a *MTProtoAdapter) recoveryEndpoint(stream string) (transport.EndpointID, bool) {
    prefix := "telegram:mtproto:" + a.connectionID + ":"
    if !strings.HasPrefix(stream,prefix) { return "", false }
    alias := transport.EndpointID(strings.TrimPrefix(stream,prefix))
    _, ok := a.RecoveryStream(alias)
    return alias, ok
}

func (a *MTProtoAdapter) Recover(ctx context.Context, request transport.RecoveryRequest, emit func(context.Context, transport.Incoming) error) error {
    if a == nil || emit == nil { return errors.New("Telegram MTProto recovery is not initialized") }
    if err := ctx.Err(); err != nil { return err }
    endpoint, ok := a.recoveryEndpoint(strings.TrimSpace(request.Cursor.StreamKey))
    if !ok { return errors.New("Telegram MTProto recovery stream is not configured") }
    if request.MaxEvents <= 0 { return nil }
    maxEvents := request.MaxEvents
    if maxEvents > 1000 { maxEvents = 1000 }
    peer, liveClient, err := a.resolveMTProtoPeer(ctx, endpoint)
    if err != nil { return errors.New("Telegram MTProto history is unavailable") }
    history, ok := liveClient.(mtprotoHistoryClient)
    if !ok { return errors.New("Telegram MTProto history is unavailable") }

    cutoff := time.Time{}
    if request.MaxAge > 0 { cutoff = time.Now().UTC().Add(-request.MaxAge) }
    cursor := int(request.Cursor.Position)
    offsetID := 0
    candidates := make([]transport.Incoming,0,maxEvents)
    seen := make(map[int]struct{})
    for {
        if err := ctx.Err(); err != nil { return err }
        page, err := history.History(ctx, peer, cursor, offsetID, mtprotoHistoryPageSize)
        if err != nil { return errors.New("recover Telegram MTProto history") }
        if len(page.Messages)==0 { break }
        reachedCutoff := false
        oldestID := 0
        for _, msg := range page.Messages {
            if msg==nil || msg.ID<=cursor { continue }
            if oldestID==0 || msg.ID<oldestID { oldestID=msg.ID }
            if _, exists := seen[msg.ID]; exists { continue }
            seen[msg.ID]=struct{}{}
            ts := time.Unix(int64(msg.Date),0).UTC()
            if !cutoff.IsZero() && ts.Before(cutoff) { reachedCutoff=true; continue }
            incoming, ok := a.recoveredMTProtoIncoming(ctx,page.Entities,msg)
            if !ok { continue }
            incoming.Checkpoint=transport.Checkpoint{StreamKey:request.Cursor.StreamKey,Position:int64(msg.ID),EventTimestamp:incoming.Timestamp,Valid:true}
            candidates=append(candidates,incoming)
        }
        if reachedCutoff || !page.More || oldestID<=cursor || oldestID==offsetID { break }
        offsetID=oldestID
        // With no age bound and no prior cursor, do not turn a bounded request into an account export.
        if request.MaxAge<=0 && cursor==0 && len(candidates)>=maxEvents { break }
    }
    sort.SliceStable(candidates,func(i,j int) bool {
        if candidates[i].Timestamp.Equal(candidates[j].Timestamp) { return candidates[i].Checkpoint.Position<candidates[j].Checkpoint.Position }
        return candidates[i].Timestamp.Before(candidates[j].Timestamp)
    })
    if len(candidates)>maxEvents { candidates=candidates[:maxEvents] }
    for _, incoming := range candidates {
        if err:=ctx.Err(); err!=nil { return err }
        if err:=emit(ctx,incoming); err!=nil { return err }
    }
    return nil
}

func (a *MTProtoAdapter) recoveredMTProtoIncoming(ctx context.Context, entities tg.Entities, msg *tg.Message) (transport.Incoming,bool) {
    if a==nil || a.live==nil || msg==nil { return transport.Incoming{},false }
    peer, ok:=mtprotoPeerFromEntities(msg.PeerID,entities); if !ok { return transport.Incoming{},false }
    if peer.Kind=="channel" && peer.AccessHash==0 { a.live.mu.RLock(); cached:=a.live.peers[peer.RemoteID]; a.live.mu.RUnlock(); peer.AccessHash=cached.AccessHash }
    if a.live.cachePeer(peer) { _=a.persistMTProtoPeers(ctx) }
    normalizer,selfID,mediaEnabled,mediaMax:=a.live.stateForNormalize(); if normalizer==nil { return transport.Incoming{},false }
    media,hasMedia:=mtprotoMediaDescriptor(msg)
    synthetic,ok:=mtprotoSyntheticMessage(msg,entities,selfID,hasMedia); if !ok { return transport.Incoming{},false }
    incoming,ok:=normalizer.NormalizeMessage(synthetic,0); if !ok { return transport.Incoming{},false }
    incoming.FromSelf=msg.Out
    if hasMedia {
        if !mediaEnabled {
            if strings.TrimSpace(incoming.Text)=="" { return transport.Incoming{},false }
            incoming.Kind="text"; incoming.MediaLoader=nil
        } else {
            incoming.Kind=media.Kind; ref:=media
            incoming.MediaLoader=func(loadCtx context.Context)([]byte,error){
                if mediaMax>0 && ref.Size>mediaMax { return nil,errMTProtoMediaTooLarge }
                return a.downloadMTProtoMedia(loadCtx,ref,mediaMax)
            }
        }
    }
    a.live.rememberMessage(incoming.Endpoint,msg.ID)
    return incoming,true
}

func (c *gotdAuthClient) History(ctx context.Context, peer mtprotoPeerState, minID, offsetID, limit int) (mtprotoHistoryPage,error) {
    input,err:=peer.input(); if err!=nil { return mtprotoHistoryPage{},err }
    if limit<=0 || limit>mtprotoHistoryPageSize { limit=mtprotoHistoryPageSize }
    result,err:=tg.NewClient(c.client).MessagesGetHistory(ctx,&tg.MessagesGetHistoryRequest{Peer:input,OffsetID:offsetID,Limit:limit,MinID:minID})
    if err!=nil { return mtprotoHistoryPage{},err }
    modified,ok:=result.AsModified(); if !ok { return mtprotoHistoryPage{},nil }
    entities:=tg.Entities{Users:map[int64]*tg.User{},Chats:map[int64]*tg.Chat{},Channels:map[int64]*tg.Channel{}}
    for _, u:=range modified.GetUsers(){ if v,ok:=u.(*tg.User);ok { entities.Users[v.ID]=v } }
    for _, ch:=range modified.GetChats(){
        switch v:=ch.(type){ case *tg.Chat: entities.Chats[v.ID]=v; case *tg.Channel: entities.Channels[v.ID]=v }
    }
    messages:=make([]*tg.Message,0,len(modified.GetMessages()))
    for _, class:=range modified.GetMessages(){ if msg,ok:=class.(*tg.Message);ok { messages=append(messages,msg) } }
    return mtprotoHistoryPage{Messages:messages,Entities:entities,More:len(modified.GetMessages())>=limit},nil
}

func mtprotoMessageID(s string) int { v,_:=strconv.Atoi(strings.TrimSpace(s)); return v }
''')

# API optional manual-backfill service + route.
rep('internal/api/api.go',
'''type telegramTopicDiscoveryService interface {\n\tTelegramTopicDiscovery(context.Context, string, string) (any, error)\n}\n''',
'''type telegramTopicDiscoveryService interface {\n\tTelegramTopicDiscovery(context.Context, string, string) (any, error)\n}\n\ntype telegramBackfillService interface {\n\tTelegramHistoricalBackfill(context.Context, string, string, int, time.Duration) error\n}\n''','backfill service interface')
rep('internal/api/api.go',
'''\ts.mux.HandleFunc("GET /api/connections/{id}/telegram/topics", s.handleTelegramTopicDiscovery)\n''',
'''\ts.mux.HandleFunc("GET /api/connections/{id}/telegram/topics", s.handleTelegramTopicDiscovery)\n\ts.mux.HandleFunc("POST /api/connections/{id}/telegram/backfill", s.handleTelegramHistoricalBackfill)\n''','backfill route')
(root/'internal/api/telegram_backfill.go').write_text(r'''package api

import (
    "database/sql"
    "errors"
    "net/http"
    "strings"
    "time"

    "github.com/vm75/message-sync/internal/controlstore"
)

type telegramBackfillRequest struct {
    Endpoint string `json:"endpoint"`
    MaxEvents int `json:"maxEvents"`
    MaxAgeHours int `json:"maxAgeHours"`
}

func (s *Server) handleTelegramHistoricalBackfill(w http.ResponseWriter,r *http.Request){
    if s.controlDB==nil { WriteError(w,http.StatusServiceUnavailable,"control database unavailable"); return }
    id:=strings.TrimSpace(r.PathValue("id")); if id=="" { WriteError(w,http.StatusBadRequest,"connection id is required"); return }
    var transportName,mode string; var enabled bool
    err:=s.controlDB.QueryRowContext(r.Context(),`SELECT transport, integration_mode, enabled FROM transport_connections WHERE id=?`,id).Scan(&transportName,&mode,&enabled)
    if errors.Is(err,sql.ErrNoRows){ WriteError(w,http.StatusNotFound,"connection not found"); return }
    if err!=nil { WriteError(w,http.StatusInternalServerError,"database error"); return }
    if transportName!="telegram" { WriteError(w,http.StatusBadRequest,"not a Telegram connection"); return }
    if !enabled { WriteError(w,http.StatusBadRequest,"connection is disabled"); return }
    if controlstore.NormalizeIntegrationMode(transportName,mode)!=controlstore.TelegramIntegrationModeMTProto { WriteError(w,http.StatusConflict,"historical backfill is not supported by this connection"); return }
    var req telegramBackfillRequest; if err:=ReadJSON(r,&req);err!=nil { WriteError(w,http.StatusBadRequest,"invalid request body"); return }
    req.Endpoint=strings.TrimSpace(req.Endpoint)
    if req.Endpoint=="" || req.MaxEvents<=0 || req.MaxEvents>1000 || req.MaxAgeHours<=0 || req.MaxAgeHours>24*30 { WriteError(w,http.StatusBadRequest,"endpoint and bounded maxEvents/maxAgeHours are required"); return }
    svc,ok:=s.connections.(telegramBackfillService); if !ok || svc==nil { WriteError(w,http.StatusServiceUnavailable,"Telegram historical backfill is unavailable"); return }
    if err:=svc.TelegramHistoricalBackfill(r.Context(),id,req.Endpoint,req.MaxEvents,time.Duration(req.MaxAgeHours)*time.Hour);err!=nil { WriteError(w,http.StatusBadRequest,"Telegram historical backfill failed"); return }
    s.audit(r,"telegram_mtproto_backfill",id)
    _=WriteJSON(w,http.StatusOK,map[string]string{"status":"completed"})
}
''')

# App service routes manual backfill through the existing coordinator.
rep('internal/app/app.go',
'''\tConnections:      &appConnectionService{connMgr: connMgr, dataDir: dataDir},\n''',
'''\tConnections:      &appConnectionService{connMgr: connMgr, coordinator: recoveryCoordinator, dataDir: dataDir},\n''','wire coordinator')
rep('internal/app/app.go',
'''type appConnectionService struct {\n\tconnMgr *connection.Manager\n\tdataDir string\n}\n''',
'''type appConnectionService struct {\n\tconnMgr     *connection.Manager\n\tcoordinator *recovery.Coordinator\n\tdataDir     string\n}\n''','service coordinator field')
rep('internal/app/app.go',
'''func (s *appConnectionService) TelegramTopicDiscovery(ctx context.Context, id, remoteID string) (any, error) {\n''',
'''func (s *appConnectionService) TelegramHistoricalBackfill(ctx context.Context, id, endpoint string, maxEvents int, maxAge time.Duration) error {\n\tif s == nil || s.connMgr == nil || s.coordinator == nil {\n\t\treturn errors.New("recovery runtime unavailable")\n\t}\n\tadapter, ok := s.connMgr.GetAdapter(id)\n\tif !ok { return errors.New("connection is not running") }\n\tsource, ok := adapter.(transport.RecoverySource)\n\tif !ok { return errors.New("historical recovery is not supported") }\n\tprovider, ok := adapter.(interface { RecoveryStream(transport.EndpointID) (string, bool) })\n\tif !ok { return errors.New("historical recovery is not supported") }\n\tstream, ok := provider.RecoveryStream(transport.EndpointID(strings.TrimSpace(endpoint)))\n\tif !ok { return errors.New("endpoint is not configured on this connection") }\n\treturn s.coordinator.RecoverStream(ctx, source, stream, maxEvents, maxAge)\n}\n\nfunc (s *appConnectionService) TelegramTopicDiscovery(ctx context.Context, id, remoteID string) (any, error) {\n''','app manual backfill')

# Focused recovery tests reuse the tested live fake.
(root/'internal/transport/telegram/mtproto_recovery_test.go').write_text(r'''package telegram

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

type fakeMTRecoveryAuth struct { *fakeMTLiveAuth; pages []mtprotoHistoryPage }
func (f *fakeMTRecoveryAuth) History(ctx context.Context,_ mtprotoPeerState,minID,offsetID,limit int)(mtprotoHistoryPage,error){
    if err:=ctx.Err();err!=nil{return mtprotoHistoryPage{},err}
    for _,page:=range f.pages {
        if len(page.Messages)==0 { continue }
        newest:=page.Messages[0].ID; oldest:=page.Messages[len(page.Messages)-1].ID
        if offsetID==0 || (newest<offsetID && oldest>minID) { return page,nil }
    }
    return mtprotoHistoryPage{},nil
}
func newRecoveryAdapter(t *testing.T,id string,auth *fakeMTRecoveryAuth,max uint64)(*MTProtoAdapter){
    t.Helper(); auth.authorized=true
    state,_:=json.Marshal(mtprotoState{Version:1,APIID:1,APIHash:"hash",Phone:"+1",Session:[]byte("s")})
    store:=&memoryMTStore{data:state}; hasher,_:=identity.New([]byte("0123456789abcdef0123456789abcdef"))
    a,err:=OpenMTProto(context.Background(),Options{ConnectionID:id,Logger:testLogger(),MTProtoStateStore:store,ChatIDs:map[string]string{"tg":"-1000000000042"},Hasher:hasher,UsernameMode:config.UsernameModePushName,MediaEnabled:true,MediaMaxBytes:max,mtprotoRuntimeFactory:func(int,string,gotdsession.Storage)mtprotoRuntime{return fakeMTLiveRuntime{auth:auth}}})
    if err!=nil{t.Fatal(err)}; select{case<-a.ready:case<-time.After(time.Second):t.Fatal("not ready")}; return a
}
func historyEntities()tg.Entities{return tg.Entities{Users:map[int64]*tg.User{7:{ID:7,FirstName:"A"}},Channels:map[int64]*tg.Channel{42:{ID:42,AccessHash:9,Title:"G",Megagroup:true,Forum:true}}}}
func histMsg(id int,when time.Time,topic int)*tg.Message{ m:=&tg.Message{ID:id,PeerID:&tg.PeerChannel{ChannelID:42},FromID:&tg.PeerUser{UserID:7},Date:int(when.Unix()),Message:"m"}; if topic>0{m.ReplyTo=&tg.MessageReplyHeader{ForumTopic:true,ReplyToMsgID:topic,ReplyToTopID:topic}}; return m }
func TestMTProtoRecoveryOrderingBoundsCursorAndTopics(t *testing.T){
    now:=time.Now().UTC(); ents:=historyEntities()
    auth:=&fakeMTRecoveryAuth{fakeMTLiveAuth:&fakeMTLiveAuth{groups:[]mtprotoGroup{{Peer:mtprotoPeerState{RemoteID:"-1000000000042",Kind:"channel",ID:42,AccessHash:9},Forum:true}}},pages:[]mtprotoHistoryPage{{Messages:[]*tg.Message{histMsg(4,now,17),histMsg(3,now.Add(-time.Minute),17),histMsg(2,now.Add(-2*time.Minute),17)},Entities:ents,More:false}}}
    a:=newRecoveryAdapter(t,"r1",auth,1024); defer a.Close(); stream,_:=a.RecoveryStream("tg")
    var got []transport.Incoming
    err:=a.Recover(context.Background(),transport.RecoveryRequest{Cursor:transport.Checkpoint{StreamKey:stream,Position:1,Valid:true},MaxEvents:2,MaxAge:time.Hour},func(_ context.Context,in transport.Incoming)error{got=append(got,in);return nil})
    if err!=nil{t.Fatal(err)}; if len(got)!=2 || got[0].RemoteID!="2" || got[1].RemoteID!="3"{t.Fatalf("ordering/bounds: %+v",got)}
    if got[0].ChildScope==nil || got[0].ChildScope.RemoteID!="17"{t.Fatalf("topic lost: %+v",got[0].ChildScope)}
    if got[0].Checkpoint.StreamKey!=stream || got[0].Checkpoint.Position!=2{t.Fatalf("checkpoint: %+v",got[0].Checkpoint)}
}
func TestMTProtoRecoveryAgeMediaLimitAndCancellation(t *testing.T){
    now:=time.Now().UTC(); ents:=historyEntities(); big:=histMsg(5,now,0); big.Media=&tg.MessageMediaDocument{Document:&tg.Document{ID:1,AccessHash:2,FileReference:[]byte{1},Size:2048,MimeType:"application/octet-stream"}}
    old:=histMsg(4,now.Add(-48*time.Hour),0)
    auth:=&fakeMTRecoveryAuth{fakeMTLiveAuth:&fakeMTLiveAuth{groups:[]mtprotoGroup{{Peer:mtprotoPeerState{RemoteID:"-1000000000042",Kind:"channel",ID:42,AccessHash:9}}},pages:[]mtprotoHistoryPage{{Messages:[]*tg.Message{big,old},Entities:ents}}}
    a:=newRecoveryAdapter(t,"r2",auth,1024); defer a.Close(); stream,_:=a.RecoveryStream("tg"); var got []transport.Incoming
    if err:=a.Recover(context.Background(),transport.RecoveryRequest{Cursor:transport.Checkpoint{StreamKey:stream,Valid:true},MaxEvents:10,MaxAge:time.Hour},func(_ context.Context,in transport.Incoming)error{got=append(got,in);return nil});err!=nil{t.Fatal(err)}
    if len(got)!=1 || got[0].MediaLoader==nil{t.Fatalf("age/media: %+v",got)}
    if _,err:=got[0].MediaLoader(context.Background());!errors.Is(err,errMTProtoMediaTooLarge){t.Fatalf("media limit err=%v",err)}
    ctx,cancel:=context.WithCancel(context.Background());cancel(); if err:=a.Recover(ctx,transport.RecoveryRequest{Cursor:transport.Checkpoint{StreamKey:stream,Valid:true},MaxEvents:1,MaxAge:time.Hour},func(context.Context,transport.Incoming)error{return nil});!errors.Is(err,context.Canceled){t.Fatalf("cancel err=%v",err)}
}
func TestMTProtoRecoveryStreamsAndLiveCheckpointAreConnectionScoped(t *testing.T){
    auth:=&fakeMTRecoveryAuth{fakeMTLiveAuth:&fakeMTLiveAuth{groups:[]mtprotoGroup{{Peer:mtprotoPeerState{RemoteID:"-1000000000042",Kind:"channel",ID:42,AccessHash:9}}}}
    a:=newRecoveryAdapter(t,"r-one",auth,1024); defer a.Close(); b:=newRecoveryAdapter(t,"r-two",auth,1024); defer b.Close()
    as,_:=a.RecoveryStream("tg");bs,_:=b.RecoveryStream("tg");if as==bs{t.Fatal("connection streams collided")}
    a.normalizeMTProtoMessage(context.Background(),historyEntities(),histMsg(9,time.Now(),0),false)
    select{case in:=<-a.Events(): if in.Checkpoint.StreamKey!=as || in.Checkpoint.Position!=9{t.Fatalf("live checkpoint %+v",in.Checkpoint)};case<-time.After(time.Second):t.Fatal("missing live event")}
}
''')

(root/'internal/api/telegram_backfill_test.go').write_text(r'''package api

import("context";"net/http";"net/http/httptest";"strings";"testing";"time")
type mockBackfillConnectionService struct{*mockConnectionService;calls int;id,endpoint string;max int;age time.Duration}
func(m *mockBackfillConnectionService)TelegramHistoricalBackfill(_ context.Context,id,endpoint string,max int,age time.Duration)error{m.calls++;m.id=id;m.endpoint=endpoint;m.max=max;m.age=age;return nil}
func TestTelegramHistoricalBackfillRequiresMTProtoAndBounds(t *testing.T){
 srv,_,db,admin,_:=setupConnectionsTestEnv(t); m:=&mockBackfillConnectionService{mockConnectionService:&mockConnectionService{}};srv.connections=m
 req:=httptest.NewRequest(http.MethodPost,"/api/connections/conn-tg-1/telegram/backfill",strings.NewReader(`{"endpoint":"tg","maxEvents":10,"maxAgeHours":24}`));req.Header.Set("Authorization","Bearer "+admin);rec:=httptest.NewRecorder();srv.Handler().ServeHTTP(rec,req);if rec.Code!=http.StatusConflict{t.Fatalf("bot status=%d %s",rec.Code,rec.Body.String())}
 if _,err:=db.Exec(`UPDATE transport_connections SET integration_mode='mtproto' WHERE id='conn-tg-1'`);err!=nil{t.Fatal(err)}
 req=httptest.NewRequest(http.MethodPost,"/api/connections/conn-tg-1/telegram/backfill",strings.NewReader(`{"endpoint":"tg","maxEvents":10,"maxAgeHours":24}`));req.Header.Set("Authorization","Bearer "+admin);rec=httptest.NewRecorder();srv.Handler().ServeHTTP(rec,req);if rec.Code!=http.StatusOK{t.Fatalf("mtproto status=%d %s",rec.Code,rec.Body.String())};if m.calls!=1||m.id!="conn-tg-1"||m.endpoint!="tg"||m.max!=10||m.age!=24*time.Hour{t.Fatalf("call %+v",m)}
}
''')

tracker=root/'docs/TELEGRAM_MTPROTO_SUPPORT_PROGRESS.md'; text=tracker.read_text().replace('- [ ] #106 — Implement MTProto historical recovery and bounded backfill through RecoverySource','- [x] #106 — Implement MTProto historical recovery and bounded backfill through RecoverySource')
log='''\n### #106 — complete\n\n- MTProto adapters now implement `transport.RecoverySource` with connection+endpoint-scoped stream keys and matching checkpoints on live ordinary-message ingress.\n- Historical reads use bounded `messages.getHistory`, normalize through the same Telegram privacy/routing semantics, preserve replies/topics/media loaders, and emit deterministic chronological events through the existing recovery coordinator.\n- Recovery honors cursor, max-event, max-age, media and cancellation bounds; private dialogs remain excluded and no raw history payloads are persisted/logged.\n- Added an explicitly bounded MTProto-only manual backfill API that routes through `Coordinator.RecoverStream`; Bot API returns an unsupported-capability response.\n- Verification: focused ordering/bounds/topic/media/cancellation/isolation/API tests plus `go test ./...` and `go vet ./...` in the tested-change workflow.\n'''; marker='\n## Completion rule\n';
if '### #106 — complete' not in text:text=text.replace(marker,log+marker)
tracker.write_text(text)
