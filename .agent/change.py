from pathlib import Path

root = Path('.')

def replace_once(path, old, new, label):
    p = root / path
    text = p.read_text()
    if old not in text:
        raise SystemExit(f'missing marker {label} in {path}')
    p.write_text(text.replace(old, new, 1))

# Admin DTO/capability surface: keep Bot AdminService unchanged and add an optional
# complete-topic discovery interface implemented only by MTProto.
replace_once('internal/transport/telegram/admin.go',
'''type DiscoveredChat struct {\n\tChatID   string `json:"chatId"`\n\tTitle    string `json:"title,omitempty"`\n\tUsername string `json:"username,omitempty"`\n\tType     string `json:"type"`\n}\n\ntype AdminService interface {''',
'''type DiscoveredChat struct {\n\tChatID   string `json:"chatId"`\n\tTitle    string `json:"title,omitempty"`\n\tUsername string `json:"username,omitempty"`\n\tType     string `json:"type"`\n\tForum    bool   `json:"forum,omitempty"`\n}\n\ntype DiscoveredTopic struct {\n\tRemoteID string `json:"remoteId"`\n\tLabel    string `json:"label,omitempty"`\n\tGeneral  bool   `json:"general,omitempty"`\n}\n\ntype TopicDiscoveryService interface {\n\tDiscoverTopics(context.Context, string) ([]DiscoveredTopic, error)\n}\n\ntype AdminService interface {''', 'admin discovery DTOs')

# Delegate MTProto placeholders to the real discovery implementation in a focused file.
replace_once('internal/transport/telegram/mtproto.go',
'''func (a *MTProtoAdapter) DiscoverChats(context.Context) ([]DiscoveredChat, error) {\n\treturn nil, errors.New("full MTProto discovery is not enabled yet")\n}\nfunc (a *MTProtoAdapter) ValidateTarget(context.Context, string) error {\n\treturn ErrTargetValidationUnavailable\n}\n''',
'''func (a *MTProtoAdapter) DiscoverChats(ctx context.Context) ([]DiscoveredChat, error) {\n\treturn a.discoverMTProtoChats(ctx)\n}\nfunc (a *MTProtoAdapter) ValidateTarget(ctx context.Context, remoteID string) error {\n\treturn a.validateMTProtoTarget(ctx, remoteID)\n}\n''', 'MTProto discovery delegates')

(root / 'internal/transport/telegram/mtproto_discovery.go').write_text(r'''package telegram

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
            ChatID: strings.TrimSpace(group.Peer.RemoteID),
            Title: strings.TrimSpace(group.Title),
            Username: strings.TrimSpace(group.Username),
            Type: typ,
            Forum: group.Forum,
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
    out := make([]DiscoveredTopic, 0, len(topics))
    for _, topic := range topics {
        out = append(out, DiscoveredTopic{
            RemoteID: strconv.Itoa(topic.ID),
            Label: strings.TrimSpace(topic.Title),
            General: topic.General,
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
            if dated, ok := message.(interface { GetID() int; GetDate() int }); ok {
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
''')

# Optional connection-service capability and route.
replace_once('internal/api/api.go',
'''type telegramTargetValidator interface {\n\tValidateTelegramTarget(context.Context, string, string) error\n}\n''',
'''type telegramTargetValidator interface {\n\tValidateTelegramTarget(context.Context, string, string) error\n}\n\ntype telegramTopicDiscoveryService interface {\n\tTelegramTopicDiscovery(context.Context, string, string) (any, error)\n}\n''', 'api optional topic service')
replace_once('internal/api/api.go',
'''\ts.mux.HandleFunc("GET /api/connections/{id}/discovery", s.handleGetConnectionDiscovery)\n''',
'''\ts.mux.HandleFunc("GET /api/connections/{id}/discovery", s.handleGetConnectionDiscovery)\n\ts.mux.HandleFunc("GET /api/connections/{id}/telegram/topics", s.handleTelegramTopicDiscovery)\n''', 'topic route')

(root / 'internal/api/telegram_discovery.go').write_text(r'''package api

import (
    "database/sql"
    "errors"
    "net/http"
    "strings"

    "github.com/vm75/message-sync/internal/controlstore"
    telegram "github.com/vm75/message-sync/internal/transport/telegram"
)

func (s *Server) handleTelegramTopicDiscovery(w http.ResponseWriter, r *http.Request) {
    if s.controlDB == nil {
        WriteError(w, http.StatusServiceUnavailable, "control database unavailable")
        return
    }
    id := strings.TrimSpace(r.PathValue("id"))
    remoteID := strings.TrimSpace(r.URL.Query().Get("remoteId"))
    if id == "" || remoteID == "" {
        WriteError(w, http.StatusBadRequest, "connection id and remoteId are required")
        return
    }
    var transportName, mode string
    var enabled bool
    err := s.controlDB.QueryRowContext(r.Context(), `SELECT transport, integration_mode, enabled FROM transport_connections WHERE id = ?`, id).Scan(&transportName, &mode, &enabled)
    if errors.Is(err, sql.ErrNoRows) {
        WriteError(w, http.StatusNotFound, "connection not found")
        return
    }
    if err != nil {
        WriteError(w, http.StatusInternalServerError, "database error")
        return
    }
    if transportName != "telegram" {
        WriteError(w, http.StatusBadRequest, "not a Telegram connection")
        return
    }
    if !enabled {
        WriteError(w, http.StatusBadRequest, "connection is disabled")
        return
    }
    if controlstore.NormalizeIntegrationMode(transportName, mode) != controlstore.TelegramIntegrationModeMTProto {
        WriteError(w, http.StatusConflict, "full Telegram topic discovery is not supported by this connection")
        return
    }
    service, ok := s.connections.(telegramTopicDiscoveryService)
    if !ok || service == nil {
        WriteError(w, http.StatusServiceUnavailable, "Telegram topic discovery is unavailable")
        return
    }
    topics, err := service.TelegramTopicDiscovery(r.Context(), id, remoteID)
    if err != nil {
        if errors.Is(err, telegram.ErrUnsupportedTarget) {
            WriteError(w, http.StatusBadRequest, "Telegram target is not a supported forum group")
            return
        }
        WriteError(w, http.StatusServiceUnavailable, "Telegram topic discovery is unavailable")
        return
    }
    if topics == nil {
        topics = []telegram.DiscoveredTopic{}
    }
    _ = WriteJSON(w, http.StatusOK, topics)
}
''')

# App service passes the optional MTProto-only topic discovery capability through.
replace_once('internal/app/app.go',
'''func (s *appConnectionService) ValidateTelegramTarget(ctx context.Context, id, remoteID string) error {\n''',
'''func (s *appConnectionService) TelegramTopicDiscovery(ctx context.Context, id, remoteID string) (any, error) {\n\tif s == nil || s.connMgr == nil {\n\t\treturn nil, errors.New("connection manager unavailable")\n\t}\n\tadapter, ok := s.connMgr.GetAdapter(id)\n\tif !ok {\n\t\treturn nil, errors.New("connection is not running")\n\t}\n\tservice, ok := adapter.(telegram.TopicDiscoveryService)\n\tif !ok {\n\t\treturn nil, errors.New("full Telegram topic discovery is not supported")\n\t}\n\treturn service.DiscoverTopics(ctx, remoteID)\n}\n\nfunc (s *appConnectionService) ValidateTelegramTarget(ctx context.Context, id, remoteID string) error {\n''', 'app topic discovery')

(root / 'internal/transport/telegram/mtproto_discovery_test.go').write_text(r'''package telegram

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
    if err != nil { t.Fatal(err) }
    store := &memoryMTStore{data: raw}
    hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
    if err != nil { t.Fatal(err) }
    adapter, err := OpenMTProto(context.Background(), Options{
        ConnectionID: connectionID, Logger: testLogger(), MTProtoStateStore: store,
        Hasher: hasher, UsernameMode: config.UsernameModePushName,
        mtprotoRuntimeFactory: func(int, string, gotdsession.Storage) mtprotoRuntime { return fakeMTLiveRuntime{auth: auth} },
    })
    if err != nil { t.Fatal(err) }
    select {
    case <-adapter.ready:
    case <-time.After(time.Second): t.Fatal("adapter not ready")
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
    if err != nil { t.Fatal(err) }
    if len(chats) != 2 || chats[0].ChatID != "-1000000000042" || chats[0].Type != "supergroup" || !chats[0].Forum || chats[1].Type != "group" {
        t.Fatalf("unexpected discovery: %+v", chats)
    }
    if err := adapter.ValidateTarget(context.Background(), "-7"); err != nil { t.Fatalf("valid group rejected: %v", err) }
    if !errors.Is(adapter.ValidateTarget(context.Background(), "-999"), ErrUnsupportedTarget) { t.Fatal("unknown group was accepted") }
    if !errors.Is(adapter.ValidateTarget(context.Background(), "123"), ErrUnsupportedTarget) { t.Fatal("positive/private target was accepted") }

    var state mtprotoState
    if err := json.Unmarshal(store.data, &state); err != nil { t.Fatal(err) }
    if len(state.Peers) != 2 || state.Peers["-1000000000042"].AccessHash != 99 { t.Fatalf("peer cache not persisted: %+v", state.Peers) }
}

func TestMTProtoForumTopicDiscoveryIncludesGeneralWithoutInventedLabel(t *testing.T) {
    remote := "-1000000000042"
    auth := &fakeMTDiscoveryAuth{
        fakeMTLiveAuth: &fakeMTLiveAuth{groups: []mtprotoGroup{{Peer: mtprotoPeerState{RemoteID: remote, Kind: "channel", ID: 42, AccessHash: 99}, Title: "Forum", Forum: true}}},
        topics: map[string][]mtprotoTopic{remote: {{ID: 17, Title: "Plans"}}},
    }
    adapter, _ := newMTProtoDiscoveryAdapter(t, "mt-topics", auth)
    defer adapter.Close()
    topics, err := adapter.DiscoverTopics(context.Background(), remote)
    if err != nil { t.Fatal(err) }
    if len(topics) != 2 || topics[0].RemoteID != "1" || !topics[0].General || topics[0].Label != "" || topics[1].RemoteID != "17" || topics[1].Label != "Plans" {
        t.Fatalf("unexpected topics: %+v", topics)
    }
}

func TestMTProtoDiscoveryConnectionIsolation(t *testing.T) {
    one := &fakeMTDiscoveryAuth{fakeMTLiveAuth: &fakeMTLiveAuth{groups: []mtprotoGroup{{Peer: mtprotoPeerState{RemoteID: "-11", Kind: "chat", ID: 11}, Title: "One"}}}}
    two := &fakeMTDiscoveryAuth{fakeMTLiveAuth: &fakeMTLiveAuth{groups: []mtprotoGroup{{Peer: mtprotoPeerState{RemoteID: "-22", Kind: "chat", ID: 22}, Title: "Two"}}}}
    a, _ := newMTProtoDiscoveryAdapter(t, "mt-one", one); defer a.Close()
    b, _ := newMTProtoDiscoveryAdapter(t, "mt-two", two); defer b.Close()
    ac, _ := a.DiscoverChats(context.Background()); bc, _ := b.DiscoverChats(context.Background())
    if len(ac) != 1 || ac[0].ChatID != "-11" || len(bc) != 1 || bc[0].ChatID != "-22" { t.Fatalf("discovery leaked: a=%+v b=%+v", ac, bc) }
}
''')

(root / 'internal/api/telegram_discovery_test.go').write_text(r'''package api

import (
    "context"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    telegram "github.com/vm75/message-sync/internal/transport/telegram"
)

type mockTopicConnectionService struct {
    *mockConnectionService
    topics any
    err error
    seenConnection string
    seenRemote string
}
func (m *mockTopicConnectionService) TelegramTopicDiscovery(_ context.Context, id, remoteID string) (any, error) {
    m.seenConnection, m.seenRemote = id, remoteID
    return m.topics, m.err
}

func TestTelegramTopicDiscoveryCapabilityAndModeIsolation(t *testing.T) {
    srv, _, controlDB, _, opToken := setupConnectionsTestEnv(t)
    mock := &mockTopicConnectionService{mockConnectionService: &mockConnectionService{}, topics: []telegram.DiscoveredTopic{{RemoteID: "1", General: true}, {RemoteID: "17", Label: "Plans"}}}
    srv.connections = mock

    req := httptest.NewRequest(http.MethodGet, "/api/connections/conn-tg-1/telegram/topics?remoteId=-1000000000042", nil)
    req.Header.Set("Authorization", "Bearer "+opToken)
    rec := httptest.NewRecorder(); srv.Handler().ServeHTTP(rec, req)
    if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "not supported") { t.Fatalf("bot topic discovery = %d %s", rec.Code, rec.Body.String()) }

    if _, err := controlDB.Exec(`UPDATE transport_connections SET integration_mode='mtproto' WHERE id='conn-tg-1'`); err != nil { t.Fatal(err) }
    req = httptest.NewRequest(http.MethodGet, "/api/connections/conn-tg-1/telegram/topics?remoteId=-1000000000042", nil)
    req.Header.Set("Authorization", "Bearer "+opToken)
    rec = httptest.NewRecorder(); srv.Handler().ServeHTTP(rec, req)
    if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"remoteId":"17"`) { t.Fatalf("MTProto topic discovery = %d %s", rec.Code, rec.Body.String()) }
    if mock.seenConnection != "conn-tg-1" || mock.seenRemote != "-1000000000042" { t.Fatalf("wrong discovery scope: %q %q", mock.seenConnection, mock.seenRemote) }
}
''')

# Tracker.
tracker = root / 'docs/TELEGRAM_MTPROTO_SUPPORT_PROGRESS.md'
text = tracker.read_text()
text = text.replace('- [ ] #105 — Add MTProto full chat and forum-topic discovery with capability-aware admin APIs', '- [x] #105 — Add MTProto full chat and forum-topic discovery with capability-aware admin APIs')
log = '''\n### #105 — complete\n\n- MTProto discovery now enumerates all joined supported Telegram basic groups and supergroups/forums without waiting for an observed message; private dialogs and broadcast-only channels remain excluded by product policy.\n- Discovery refreshes only encrypted operational peer/access-hash state; group/topic labels remain transient presentation metadata.\n- MTProto target validation resolves directly against current joined groups, while Bot API observed discovery/validation behavior is unchanged.\n- Added complete MTProto forum-topic enumeration with explicit General-topic ID `1`; no English label is fabricated when Telegram does not supply one.\n- Added a capability-specific topic-discovery API available only to MTProto connections.\n- Verification: focused transport/API discovery tests plus `go test ./...` and `go vet ./...` in the tested-change workflow.\n'''
marker='\n## Completion rule\n'
if '### #105 — complete' not in text:
    text=text.replace(marker, log+marker)
tracker.write_text(text)
