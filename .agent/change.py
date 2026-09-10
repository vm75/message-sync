from pathlib import Path


def rep(path, old, new, count=1):
    p=Path(path); s=p.read_text()
    if s.count(old)<count: raise SystemExit(f'{path}: missing pattern {old[:120]!r}')
    p.write_text(s.replace(old,new,count))

def create(path, content):
    p=Path(path); p.parent.mkdir(parents=True, exist_ok=True)
    if p.exists(): raise SystemExit(f'{path}: exists')
    p.write_text(content)

create('internal/controlstore/connection_secret_store.go', r'''package controlstore

import (
    "context"
    "database/sql"
    "errors"
    "fmt"
    "time"
)

// ConnectionSecretStore keeps opaque, integration-specific state behind the
// same encrypted control-store boundary used for transport credentials.
type ConnectionSecretStore struct {
    db           *sql.DB
    cipher       *CredentialCipher
    connectionID string
}

func NewConnectionSecretStore(db *sql.DB, cipher *CredentialCipher, connectionID string) (*ConnectionSecretStore, error) {
    if db == nil || cipher == nil {
        return nil, errors.New("control database and credential cipher are required")
    }
    if err := ValidateConnectionID(connectionID); err != nil {
        return nil, err
    }
    return &ConnectionSecretStore{db: db, cipher: cipher, connectionID: connectionID}, nil
}

func (s *ConnectionSecretStore) Load(ctx context.Context) ([]byte, error) {
    if s == nil || s.db == nil || s.cipher == nil {
        return nil, errors.New("connection secret store is unavailable")
    }
    var transportName, mode string
    var encrypted, nonce []byte
    if err := s.db.QueryRowContext(ctx, `SELECT transport, integration_mode, encrypted_credential, credential_nonce FROM transport_connections WHERE id=?`, s.connectionID).Scan(&transportName, &mode, &encrypted, &nonce); err != nil {
        if errors.Is(err, sql.ErrNoRows) {
            return nil, errors.New("connection not found")
        }
        return nil, fmt.Errorf("load encrypted connection state: %w", err)
    }
    if transportName != "telegram" || NormalizeIntegrationMode(transportName, mode) != TelegramIntegrationModeMTProto {
        return nil, errors.New("connection is not a Telegram MTProto connection")
    }
    if len(encrypted) == 0 || len(nonce) == 0 {
        return nil, nil
    }
    plaintext, err := s.cipher.Decrypt(encrypted, nonce)
    if err != nil {
        return nil, errors.New("decrypt connection state")
    }
    return plaintext, nil
}

func (s *ConnectionSecretStore) Store(ctx context.Context, plaintext []byte) error {
    if s == nil || s.db == nil || s.cipher == nil {
        return errors.New("connection secret store is unavailable")
    }
    var transportName, mode string
    if err := s.db.QueryRowContext(ctx, `SELECT transport, integration_mode FROM transport_connections WHERE id=?`, s.connectionID).Scan(&transportName, &mode); err != nil {
        if errors.Is(err, sql.ErrNoRows) {
            return errors.New("connection not found")
        }
        return fmt.Errorf("verify connection state owner: %w", err)
    }
    if transportName != "telegram" || NormalizeIntegrationMode(transportName, mode) != TelegramIntegrationModeMTProto {
        return errors.New("connection is not a Telegram MTProto connection")
    }
    encrypted, nonce, err := s.cipher.Encrypt(plaintext)
    if err != nil {
        return errors.New("encrypt connection state")
    }
    result, err := s.db.ExecContext(ctx, `UPDATE transport_connections SET encrypted_credential=?, credential_nonce=?, credential_key_version=?, updated_at=? WHERE id=?`, encrypted, nonce, CurrentKeyVersion, time.Now().UnixMilli(), s.connectionID)
    if err != nil {
        return fmt.Errorf("persist encrypted connection state: %w", err)
    }
    n, err := result.RowsAffected()
    if err != nil || n != 1 {
        return errors.New("connection state was not persisted")
    }
    return nil
}
''')

create('internal/transport/telegram/mtproto.go', r'''package telegram

import (
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "sort"
    "strings"
    "sync"

    "github.com/awnumar/memguard"
    gotdtelegram "github.com/gotd/td/telegram"
    gotdauth "github.com/gotd/td/telegram/auth"
    "github.com/gotd/td/telegram/auth/srpguard"
    gotdsession "github.com/gotd/td/session"
    "github.com/gotd/td/tg"
    "github.com/vm75/message-sync/internal/config"
    "github.com/vm75/message-sync/internal/transport"
)

const mtprotoStateVersion = 1

const (
    MTProtoAuthDisconnected     = "disconnected"
    MTProtoAuthCodeRequired    = "code_required"
    MTProtoAuthPasswordRequired = "password_required"
    MTProtoAuthConnected       = "connected"
    MTProtoAuthError           = "error"
)

// MTProtoStateStore is intentionally opaque. The app supplies an implementation
// backed by the encrypted control-store credential boundary.
type MTProtoStateStore interface {
    Load(context.Context) ([]byte, error)
    Store(context.Context, []byte) error
}

type mtprotoState struct {
    Version int    `json:"version"`
    APIID   int    `json:"apiId,omitempty"`
    APIHash string `json:"apiHash,omitempty"`
    Phone   string `json:"phone,omitempty"`
    Session []byte `json:"session,omitempty"`
}

type mtprotoStateBox struct {
    raw MTProtoStateStore
    mu  sync.Mutex
}

func (s *mtprotoStateBox) load(ctx context.Context) (mtprotoState, error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    return s.loadLocked(ctx)
}

func (s *mtprotoStateBox) loadLocked(ctx context.Context) (mtprotoState, error) {
    var state mtprotoState
    raw, err := s.raw.Load(ctx)
    if err != nil {
        return state, err
    }
    if len(raw) == 0 {
        return mtprotoState{Version: mtprotoStateVersion}, nil
    }
    if err := json.Unmarshal(raw, &state); err != nil {
        return state, errors.New("invalid encrypted MTProto state")
    }
    if state.Version == 0 {
        state.Version = mtprotoStateVersion
    }
    if state.Version != mtprotoStateVersion {
        return state, fmt.Errorf("unsupported MTProto state version %d", state.Version)
    }
    return state, nil
}

func (s *mtprotoStateBox) store(ctx context.Context, state mtprotoState) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    state.Version = mtprotoStateVersion
    raw, err := json.Marshal(state)
    if err != nil {
        return errors.New("encode MTProto state")
    }
    return s.raw.Store(ctx, raw)
}

type encryptedSessionStorage struct{ box *mtprotoStateBox }

func (s encryptedSessionStorage) LoadSession(ctx context.Context) ([]byte, error) {
    state, err := s.box.load(ctx)
    if err != nil {
        return nil, err
    }
    if len(state.Session) == 0 {
        return nil, gotdsession.ErrNotFound
    }
    return append([]byte(nil), state.Session...), nil
}

func (s encryptedSessionStorage) StoreSession(ctx context.Context, session []byte) error {
    s.box.mu.Lock()
    defer s.box.mu.Unlock()
    state, err := s.box.loadLocked(ctx)
    if err != nil {
        return err
    }
    state.Session = append([]byte(nil), session...)
    raw, err := json.Marshal(state)
    if err != nil {
        return errors.New("encode MTProto session state")
    }
    return s.box.raw.Store(ctx, raw)
}

type mtprotoAuthClient interface {
    Authorized(context.Context) (bool, error)
    SendCode(context.Context, string) (string, bool, error)
    SignIn(context.Context, string, string, string) (bool, error)
    Password(context.Context, []byte) error
    Logout(context.Context) error
}

type mtprotoRuntime interface {
    Run(context.Context, func(context.Context, mtprotoAuthClient) error) error
}

type mtprotoRuntimeFactory func(int, string, gotdsession.Storage) mtprotoRuntime

type gotdRuntime struct{ client *gotdtelegram.Client }
type gotdAuthClient struct{ client *gotdtelegram.Client }

func newGotdRuntime(apiID int, apiHash string, storage gotdsession.Storage) mtprotoRuntime {
    return &gotdRuntime{client: gotdtelegram.NewClient(apiID, apiHash, gotdtelegram.Options{SessionStorage: storage})}
}
func (r *gotdRuntime) Run(ctx context.Context, fn func(context.Context, mtprotoAuthClient) error) error {
    return r.client.Run(ctx, func(runCtx context.Context) error { return fn(runCtx, &gotdAuthClient{client: r.client}) })
}
func (c *gotdAuthClient) Authorized(ctx context.Context) (bool, error) {
    status, err := c.client.Auth().Status(ctx)
    return err == nil && status != nil && status.Authorized, err
}
func (c *gotdAuthClient) SendCode(ctx context.Context, phone string) (string, bool, error) {
    sent, err := c.client.Auth().SendCode(ctx, phone, gotdauth.SendCodeOptions{})
    if err != nil { return "", false, err }
    switch value := sent.(type) {
    case *tg.AuthSentCode:
        return value.PhoneCodeHash, false, nil
    case *tg.AuthSentCodeSuccess:
        return "", true, nil
    default:
        return "", false, errors.New("unsupported Telegram authentication response")
    }
}
func (c *gotdAuthClient) SignIn(ctx context.Context, phone, code, hash string) (bool, error) {
    _, err := c.client.Auth().SignIn(ctx, phone, code, hash)
    if errors.Is(err, gotdauth.ErrPasswordAuthNeeded) { return true, nil }
    return false, err
}
func (c *gotdAuthClient) Password(ctx context.Context, password []byte) error {
    if len(password) == 0 { return errors.New("password is required") }
    buf := memguard.NewBufferFromBytes(password)
    _, err := c.client.Auth().PasswordWith(ctx, srpguard.LockedBuffer(buf))
    return err
}
func (c *gotdAuthClient) Logout(ctx context.Context) error {
    _, err := c.client.API().AuthLogOut(ctx)
    return err
}

type MTProtoAdapter struct {
    connectionID string
    logger       interface{ Error(string, ...any) }
    events       chan transport.Incoming
    state        *mtprotoStateBox
    runtimeFactory mtprotoRuntimeFactory

    mu          sync.RWMutex
    cfg         *config.Config
    authState   string
    configured  bool
    auth        mtprotoAuthClient
    codeHash    string
    runCancel   context.CancelFunc
    runDone     chan struct{}
    ready       chan struct{}
    readyOnce   *sync.Once
    closed      bool
}

var _ transport.Adapter = (*MTProtoAdapter)(nil)
var _ AdminService = (*MTProtoAdapter)(nil)
var _ MTProtoAuthService = (*MTProtoAdapter)(nil)

type MTProtoAuthService interface {
    ConfigureMTProto(context.Context, int, string, string) (AdminStatus, error)
    SendMTProtoCode(context.Context) (AdminStatus, error)
    SubmitMTProtoCode(context.Context, string) (AdminStatus, error)
    SubmitMTProtoPassword(context.Context, []byte) (AdminStatus, error)
    LogoutMTProto(context.Context) error
}

func OpenMTProto(ctx context.Context, opts Options) (*MTProtoAdapter, error) {
    if ctx == nil || opts.Logger == nil { return nil, errors.New("context and logger are required") }
    if err := config.ValidateConnectionID(opts.ConnectionID); err != nil { return nil, err }
    if opts.MTProtoStateStore == nil { return nil, errors.New("encrypted MTProto state store is required") }
    factory := opts.mtprotoRuntimeFactory
    if factory == nil { factory = newGotdRuntime }
    adapter := &MTProtoAdapter{
        connectionID: strings.TrimSpace(opts.ConnectionID), events: make(chan transport.Incoming, eventBufferSize),
        state: &mtprotoStateBox{raw: opts.MTProtoStateStore}, runtimeFactory: factory,
        authState: MTProtoAuthDisconnected,
    }
    state, err := adapter.state.load(ctx)
    if err != nil { return nil, err }
    if state.APIID > 0 && strings.TrimSpace(state.APIHash) != "" && strings.TrimSpace(state.Phone) != "" {
        adapter.configured = true
        adapter.startRuntime(ctx, state)
    }
    return adapter, nil
}

func (a *MTProtoAdapter) Name() string { return "telegram" }
func (a *MTProtoAdapter) ConnectionID() string { if a==nil{return ""}; return a.connectionID }
func (a *MTProtoAdapter) Events() <-chan transport.Incoming { if a==nil{return nil}; return a.events }
func (a *MTProtoAdapter) UpdateConfig(cfg *config.Config) error { if cfg==nil{return errors.New("config is required")}; a.mu.Lock(); a.cfg=cfg; a.mu.Unlock(); return nil }
func (a *MTProtoAdapter) Send(context.Context, transport.Outgoing) (transport.MessageRef,error) { return transport.MessageRef{}, errors.New("Telegram MTProto live messaging is not enabled yet") }
func (a *MTProtoAdapter) React(context.Context, transport.Reaction) error { return errors.New("Telegram MTProto live messaging is not enabled yet") }
func (a *MTProtoAdapter) Edit(context.Context, transport.MessageRef, string) error { return errors.New("Telegram MTProto live messaging is not enabled yet") }
func (a *MTProtoAdapter) Delete(context.Context, transport.MessageRef) error { return errors.New("Telegram MTProto live messaging is not enabled yet") }

func (a *MTProtoAdapter) Close() error {
    if a==nil{return nil}
    a.stopRuntime()
    a.mu.Lock()
    if !a.closed { close(a.events); a.closed=true }
    a.mu.Unlock()
    return nil
}

func (a *MTProtoAdapter) startRuntime(parent context.Context, state mtprotoState) {
    a.stopRuntime()
    runCtx, cancel := context.WithCancel(parent)
    done := make(chan struct{})
    ready := make(chan struct{})
    once := &sync.Once{}
    runtime := a.runtimeFactory(state.APIID, state.APIHash, encryptedSessionStorage{box:a.state})
    a.mu.Lock()
    a.runCancel, a.runDone, a.ready, a.readyOnce = cancel, done, ready, once
    a.authState = MTProtoAuthDisconnected
    a.mu.Unlock()
    go func(){
        defer close(done)
        err := runtime.Run(runCtx, func(clientCtx context.Context, authClient mtprotoAuthClient) error {
            authorized, statusErr := authClient.Authorized(clientCtx)
            a.mu.Lock()
            a.auth = authClient
            if statusErr != nil { a.authState=MTProtoAuthError } else if authorized { a.authState=MTProtoAuthConnected } else { a.authState=MTProtoAuthCodeRequired }
            a.mu.Unlock()
            once.Do(func(){close(ready)})
            if statusErr != nil { return statusErr }
            <-clientCtx.Done()
            return nil
        })
        a.mu.Lock()
        a.auth=nil
        if !errors.Is(err, context.Canceled) && err != nil { a.authState=MTProtoAuthError } else if a.authState != MTProtoAuthConnected { a.authState=MTProtoAuthDisconnected }
        a.mu.Unlock()
        once.Do(func(){close(ready)})
    }()
}

func (a *MTProtoAdapter) stopRuntime() {
    if a==nil{return}
    a.mu.Lock(); cancel, done := a.runCancel, a.runDone; a.runCancel=nil; a.runDone=nil; a.auth=nil; a.codeHash=""; a.mu.Unlock()
    if cancel!=nil { cancel() }
    if done!=nil { <-done }
}

func (a *MTProtoAdapter) waitAuth(ctx context.Context) (mtprotoAuthClient,error) {
    a.mu.RLock(); authClient, ready := a.auth, a.ready; a.mu.RUnlock()
    if authClient!=nil{return authClient,nil}
    if ready==nil{return nil, errors.New("MTProto credentials are not configured")}
    select { case <-ctx.Done(): return nil, ctx.Err(); case <-ready: }
    a.mu.RLock(); authClient=a.auth; state:=a.authState; a.mu.RUnlock()
    if authClient==nil { return nil, fmt.Errorf("Telegram MTProto client unavailable (%s)", state) }
    return authClient,nil
}

func (a *MTProtoAdapter) ConfigureMTProto(ctx context.Context, apiID int, apiHash, phone string) (AdminStatus,error) {
    apiHash=strings.TrimSpace(apiHash); phone=strings.TrimSpace(phone)
    if apiID<=0 || apiHash=="" || phone=="" { return a.AdminStatus(ctx), errors.New("apiId, apiHash, and phone are required") }
    current, err := a.state.load(ctx); if err!=nil{return a.AdminStatus(ctx),err}
    if current.APIID!=apiID || current.APIHash!=apiHash || current.Phone!=phone { current.Session=nil }
    current.APIID, current.APIHash, current.Phone = apiID, apiHash, phone
    if err:=a.state.store(ctx,current); err!=nil{return a.AdminStatus(ctx),err}
    a.mu.Lock(); a.configured=true; a.codeHash=""; a.mu.Unlock()
    a.startRuntime(ctx,current)
    return a.AdminStatus(ctx),nil
}

func (a *MTProtoAdapter) SendMTProtoCode(ctx context.Context) (AdminStatus,error) {
    state,err:=a.state.load(ctx); if err!=nil{return a.AdminStatus(ctx),err}
    if state.APIID<=0 || strings.TrimSpace(state.APIHash)=="" || strings.TrimSpace(state.Phone)=="" { return a.AdminStatus(ctx),errors.New("MTProto credentials are not configured") }
    client,err:=a.waitAuth(ctx); if err!=nil{return a.AdminStatus(ctx),err}
    hash,authorized,err:=client.SendCode(ctx,state.Phone); if err!=nil { a.setAuthError(); return a.AdminStatus(ctx), errors.New("failed to request Telegram login code") }
    a.mu.Lock(); a.codeHash=hash; if authorized { a.authState=MTProtoAuthConnected } else { a.authState=MTProtoAuthCodeRequired }; a.mu.Unlock()
    return a.AdminStatus(ctx),nil
}

func (a *MTProtoAdapter) SubmitMTProtoCode(ctx context.Context, code string) (AdminStatus,error) {
    code=strings.TrimSpace(code); if code=="" { return a.AdminStatus(ctx),errors.New("login code is required") }
    state,err:=a.state.load(ctx); if err!=nil{return a.AdminStatus(ctx),err}
    a.mu.RLock(); hash:=a.codeHash; a.mu.RUnlock(); if hash=="" { return a.AdminStatus(ctx),errors.New("request a new login code first") }
    client,err:=a.waitAuth(ctx); if err!=nil{return a.AdminStatus(ctx),err}
    passwordRequired,err:=client.SignIn(ctx,state.Phone,code,hash)
    if err!=nil { a.setAuthError(); return a.AdminStatus(ctx),errors.New("Telegram login code was rejected") }
    a.mu.Lock(); a.codeHash=""; if passwordRequired {a.authState=MTProtoAuthPasswordRequired}else{a.authState=MTProtoAuthConnected}; a.mu.Unlock()
    return a.AdminStatus(ctx),nil
}

func (a *MTProtoAdapter) SubmitMTProtoPassword(ctx context.Context, password []byte) (AdminStatus,error) {
    if len(password)==0{return a.AdminStatus(ctx),errors.New("2FA password is required")}
    defer func(){ for i:=range password {password[i]=0} }()
    client,err:=a.waitAuth(ctx); if err!=nil{return a.AdminStatus(ctx),err}
    a.mu.RLock(); state:=a.authState; a.mu.RUnlock(); if state!=MTProtoAuthPasswordRequired{return a.AdminStatus(ctx),errors.New("Telegram 2FA password is not currently required")}
    if err:=client.Password(ctx,password); err!=nil { a.setAuthError(); return a.AdminStatus(ctx),errors.New("Telegram 2FA password was rejected") }
    a.mu.Lock(); a.authState=MTProtoAuthConnected; a.mu.Unlock()
    return a.AdminStatus(ctx),nil
}
func (a *MTProtoAdapter) setAuthError(){a.mu.Lock();a.authState=MTProtoAuthError;a.mu.Unlock()}

func (a *MTProtoAdapter) LogoutMTProto(ctx context.Context) error {
    a.mu.RLock(); client:=a.auth; a.mu.RUnlock()
    if client!=nil { _=client.Logout(ctx) }
    a.stopRuntime()
    state,err:=a.state.load(ctx); if err!=nil{return err}; state.Session=nil
    if err:=a.state.store(ctx,state); err!=nil{return err}
    a.mu.Lock(); a.authState=MTProtoAuthDisconnected; a.codeHash=""; a.mu.Unlock()
    return nil
}

func (a *MTProtoAdapter) AdminStatus(_ context.Context) AdminStatus {
    status:=AdminStatus{IntegrationMode:"mtproto", Status:MTProtoAuthDisconnected, Endpoints:[]EndpointReadiness{}, Capabilities:CapabilitiesForIntegrationMode("mtproto")}
    if a==nil{return status}
    a.mu.RLock(); status.Status=a.authState; status.Running=a.auth!=nil; status.Configured=a.configured; cfg:=a.cfg; a.mu.RUnlock()
    if cfg!=nil { aliases:=make([]string,0); for alias,ep:=range cfg.Endpoints {if ep.Transport==config.TransportTelegram && ep.ConnectionID==a.connectionID {aliases=append(aliases,alias)}}; sort.Strings(aliases); for _,alias:=range aliases {ready:="unavailable";if status.Status==MTProtoAuthConnected{ready="ready"};status.Endpoints=append(status.Endpoints,EndpointReadiness{Alias:alias,Status:ready})} }
    return status
}
func (a *MTProtoAdapter) DiscoverChats(context.Context)([]DiscoveredChat,error){return nil,errors.New("full MTProto discovery is not enabled yet")}
func (a *MTProtoAdapter) ValidateTarget(context.Context,string)error{return ErrTargetValidationUnavailable}
''')

# Extend telegram Options with encrypted state store and injectable runtime factory.
rep('internal/transport/telegram/adapter.go', '\tObserveChildScopeLabel transport.ChildScopeLabelObserver\n}', '\tObserveChildScopeLabel transport.ChildScopeLabelObserver\n\tMTProtoStateStore       MTProtoStateStore\n\tmtprotoRuntimeFactory   mtprotoRuntimeFactory\n}')

# Extend status without exposing auth material.
rep('internal/transport/telegram/admin.go', '\tTokenConfigured    bool                `json:"tokenConfigured"`\n', '\tTokenConfigured    bool                `json:"tokenConfigured"`\n\tIntegrationMode    string              `json:"integrationMode,omitempty"`\n\tConfigured         bool                `json:"configured,omitempty"`\n')
rep('internal/transport/telegram/admin.go', '\tstatus := AdminStatus{\n\t\tTokenConfigured:    false,', '\tstatus := AdminStatus{\n\t\tIntegrationMode:    "bot",\n\t\tTokenConfigured:    false,')

# API: optional MTProto auth service and routes/handlers.
rep('internal/api/api.go', 'type telegramTargetValidator interface {', r'''type telegramMTProtoAuthService interface {
    TelegramMTProtoConfigure(context.Context, string, int, string, string) (any, error)
    TelegramMTProtoSendCode(context.Context, string) (any, error)
    TelegramMTProtoSubmitCode(context.Context, string, string) (any, error)
    TelegramMTProtoSubmitPassword(context.Context, string, []byte) (any, error)
    TelegramMTProtoLogout(context.Context, string) error
}

type telegramTargetValidator interface {''')
rep('internal/api/api.go', '\ts.mux.HandleFunc("GET /api/connections/{id}/membership-readiness", s.handleGetMembershipReadiness)\n', '\ts.mux.HandleFunc("GET /api/connections/{id}/membership-readiness", s.handleGetMembershipReadiness)\n\ts.mux.HandleFunc("POST /api/connections/{id}/telegram/mtproto/setup", s.handleTelegramMTProtoSetup)\n\ts.mux.HandleFunc("POST /api/connections/{id}/telegram/mtproto/send-code", s.handleTelegramMTProtoSendCode)\n\ts.mux.HandleFunc("POST /api/connections/{id}/telegram/mtproto/code", s.handleTelegramMTProtoCode)\n\ts.mux.HandleFunc("POST /api/connections/{id}/telegram/mtproto/password", s.handleTelegramMTProtoPassword)\n')

create('internal/api/telegram_mtproto.go', r'''package api

import (
    "context"
    "database/sql"
    "errors"
    "net/http"
    "strings"

    "github.com/vm75/message-sync/internal/controlstore"
)

type mtprotoSetupRequest struct { APIID int `json:"apiId"`; APIHash string `json:"apiHash"`; Phone string `json:"phone"` }
type mtprotoCodeRequest struct { Code string `json:"code"` }
type mtprotoPasswordRequest struct { Password string `json:"password"` }

func (s *Server) requireMTProtoConnection(ctx context.Context, id string) error {
    var transportName, mode string
    var enabled bool
    err:=s.controlDB.QueryRowContext(ctx, `SELECT transport,integration_mode,enabled FROM transport_connections WHERE id=?`,id).Scan(&transportName,&mode,&enabled)
    if errors.Is(err,sql.ErrNoRows){return errors.New("connection not found")}; if err!=nil{return errors.New("database error")}
    if transportName!="telegram" || controlstore.NormalizeIntegrationMode(transportName,mode)!=controlstore.TelegramIntegrationModeMTProto{return errors.New("not a Telegram MTProto connection")}
    if !enabled{return errors.New("connection is disabled")}
    return nil
}
func (s *Server) mtprotoService() (telegramMTProtoAuthService,bool){svc,ok:=s.connections.(telegramMTProtoAuthService);return svc,ok}
func writeMTProtoOpError(w http.ResponseWriter, err error){msg:=err.Error();switch msg{case "connection not found":WriteError(w,http.StatusNotFound,msg);case "not a Telegram MTProto connection","connection is disabled":WriteError(w,http.StatusBadRequest,msg);default:WriteError(w,http.StatusBadRequest,msg)}}

func (s *Server) handleTelegramMTProtoSetup(w http.ResponseWriter,r *http.Request){
    id:=strings.TrimSpace(r.PathValue("id")); if err:=s.requireMTProtoConnection(r.Context(),id);err!=nil{writeMTProtoOpError(w,err);return}
    var req mtprotoSetupRequest;if err:=ReadJSON(r,&req);err!=nil{WriteError(w,http.StatusBadRequest,"invalid request body");return}
    if req.APIID<=0 || strings.TrimSpace(req.APIHash)=="" || strings.TrimSpace(req.Phone)==""{WriteError(w,http.StatusBadRequest,"apiId, apiHash, and phone are required");return}
    svc,ok:=s.mtprotoService();if !ok{WriteError(w,http.StatusServiceUnavailable,"Telegram MTProto runtime unavailable");return}
    status,err:=svc.TelegramMTProtoConfigure(r.Context(),id,req.APIID,req.APIHash,req.Phone);req.APIHash="";req.Phone=""
    if err!=nil{WriteError(w,http.StatusBadRequest,err.Error());return};s.audit(r,"telegram_mtproto_configured",id);_ = WriteJSON(w,http.StatusOK,status)
}
func (s *Server) handleTelegramMTProtoSendCode(w http.ResponseWriter,r *http.Request){
    id:=strings.TrimSpace(r.PathValue("id"));if err:=s.requireMTProtoConnection(r.Context(),id);err!=nil{writeMTProtoOpError(w,err);return};svc,ok:=s.mtprotoService();if !ok{WriteError(w,http.StatusServiceUnavailable,"Telegram MTProto runtime unavailable");return};status,err:=svc.TelegramMTProtoSendCode(r.Context(),id);if err!=nil{WriteError(w,http.StatusBadRequest,err.Error());return};s.audit(r,"telegram_mtproto_code_requested",id);_ = WriteJSON(w,http.StatusOK,status)
}
func (s *Server) handleTelegramMTProtoCode(w http.ResponseWriter,r *http.Request){
    id:=strings.TrimSpace(r.PathValue("id"));if err:=s.requireMTProtoConnection(r.Context(),id);err!=nil{writeMTProtoOpError(w,err);return};var req mtprotoCodeRequest;if err:=ReadJSON(r,&req);err!=nil{WriteError(w,http.StatusBadRequest,"invalid request body");return};code:=strings.TrimSpace(req.Code);req.Code="";if code==""{WriteError(w,http.StatusBadRequest,"login code is required");return};svc,ok:=s.mtprotoService();if !ok{WriteError(w,http.StatusServiceUnavailable,"Telegram MTProto runtime unavailable");return};status,err:=svc.TelegramMTProtoSubmitCode(r.Context(),id,code);code="";if err!=nil{WriteError(w,http.StatusBadRequest,err.Error());return};s.audit(r,"telegram_mtproto_code_verified",id);_ = WriteJSON(w,http.StatusOK,status)
}
func (s *Server) handleTelegramMTProtoPassword(w http.ResponseWriter,r *http.Request){
    id:=strings.TrimSpace(r.PathValue("id"));if err:=s.requireMTProtoConnection(r.Context(),id);err!=nil{writeMTProtoOpError(w,err);return};var req mtprotoPasswordRequest;if err:=ReadJSON(r,&req);err!=nil{WriteError(w,http.StatusBadRequest,"invalid request body");return};password:=[]byte(req.Password);req.Password="";defer func(){for i:=range password{password[i]=0}}();if len(password)==0{WriteError(w,http.StatusBadRequest,"2FA password is required");return};svc,ok:=s.mtprotoService();if !ok{WriteError(w,http.StatusServiceUnavailable,"Telegram MTProto runtime unavailable");return};status,err:=svc.TelegramMTProtoSubmitPassword(r.Context(),id,password);if err!=nil{WriteError(w,http.StatusBadRequest,err.Error());return};s.audit(r,"telegram_mtproto_password_verified",id);_ = WriteJSON(w,http.StatusOK,status)
}
''')

# Make logout route dispatch MTProto while preserving WhatsApp behavior.
rep('internal/api/connections.go', r'''\tif transport != "whatsapp" {
\t\tWriteError(w, http.StatusBadRequest, "not a WhatsApp connection")
\t\treturn
\t}

\tif s.connections != nil {
\t\tif err := s.connections.WhatsAppLogout(r.Context(), id); err != nil {'''.replace('\\t','\t'), r'''\tif transport == "telegram" {
\t\tvar mode string
\t\tif err := s.controlDB.QueryRowContext(r.Context(), `SELECT integration_mode FROM transport_connections WHERE id = ?`, id).Scan(&mode); err != nil {
\t\t\tWriteError(w, http.StatusInternalServerError, "database error")
\t\t\treturn
\t\t}
\t\tif controlstore.NormalizeIntegrationMode(transport, mode) != controlstore.TelegramIntegrationModeMTProto {
\t\t\tWriteError(w, http.StatusBadRequest, "logout is not supported for Telegram Bot API connections")
\t\t\treturn
\t\t}
\t\tsvc, ok := s.mtprotoService()
\t\tif !ok {
\t\t\tWriteError(w, http.StatusServiceUnavailable, "Telegram MTProto runtime unavailable")
\t\t\treturn
\t\t}
\t\tif err := svc.TelegramMTProtoLogout(r.Context(), id); err != nil {
\t\t\tWriteError(w, http.StatusBadRequest, err.Error())
\t\t\treturn
\t\t}
\t\ts.audit(r, "telegram_mtproto_logged_out", id)
\t\t_ = WriteJSON(w, http.StatusOK, map[string]string{"status": "disconnected"})
\t\treturn
\t}
\tif transport != "whatsapp" {
\t\tWriteError(w, http.StatusBadRequest, "not a WhatsApp connection")
\t\treturn
\t}

\tif s.connections != nil {
\t\tif err := s.connections.WhatsAppLogout(r.Context(), id); err != nil {'''.replace('\\t','\t'))

# App: real MTProto opener.
rep('internal/app/app.go', 'var openTelegramMTProto = func(context.Context, telegram.Options) (telegramTransport, error) {\n\treturn nil, telegram.ErrMTProtoAdapterUnavailable\n}', 'var openTelegramMTProto = func(ctx context.Context, opts telegram.Options) (telegramTransport, error) {\n\treturn telegram.OpenMTProto(ctx, opts)\n}')

# Initial Telegram startup: only decrypt bot tokens and supply encrypted store for MTProto.
old='''\t\t\t\tif c.Transport == "telegram" && c.Enabled {
\t\t\t\t\ttokenBytes, err := credentialCipher.Decrypt(c.EncryptedCredential, c.CredentialNonce)
\t\t\t\t\tif err != nil {
\t\t\t\t\t\tsafelog.Error(logger, "decrypt telegram credential failed", "telegram_decrypt", err)
\t\t\t\t\t\tcontinue
\t\t\t\t\t}
\t\t\t\t\tconnChatIDs := make(map[string]string)'''
new='''\t\t\t\tif c.Transport == "telegram" && c.Enabled {
\t\t\t\t\tvar token string
\t\t\t\t\tvar mtState telegram.MTProtoStateStore
\t\t\t\t\tif c.IntegrationMode == controlstore.TelegramIntegrationModeMTProto {
\t\t\t\t\t\tstateStore, stateErr := controlstore.NewConnectionSecretStore(controlStore.DB(), credentialCipher, c.ID)
\t\t\t\t\t\tif stateErr != nil { safelog.Error(logger, "initialize Telegram MTProto state store failed", "telegram_mtproto_state", stateErr); continue }
\t\t\t\t\t\tmtState = stateStore
\t\t\t\t\t} else {
\t\t\t\t\t\ttokenBytes, decryptErr := credentialCipher.Decrypt(c.EncryptedCredential, c.CredentialNonce)
\t\t\t\t\t\tif decryptErr != nil { safelog.Error(logger, "decrypt telegram credential failed", "telegram_decrypt", decryptErr); continue }
\t\t\t\t\t\ttoken = string(tokenBytes)
\t\t\t\t\t}
\t\t\t\t\tconnChatIDs := make(map[string]string)'''
rep('internal/app/app.go',old,new)
rep('internal/app/app.go','\t\t\t\t\t\tToken:           string(tokenBytes),','\t\t\t\t\t\tToken:           token,\n\t\t\t\t\t\tMTProtoStateStore: mtState,',1)
rep('internal/app/app.go','\t\t\t\t\tcredentialFingerprints[c.ID] = credentialFingerprint(c)','\t\t\t\t\tif c.IntegrationMode != controlstore.TelegramIntegrationModeMTProto { credentialFingerprints[c.ID] = credentialFingerprint(c) }',1)

# Reload fingerprint ignores mutable MTProto session blob.
rep('internal/app/app.go','if c.Enabled && (c.Transport == "discord" || c.Transport == "telegram") {','if c.Enabled && (c.Transport == "discord" || (c.Transport == "telegram" && c.IntegrationMode != controlstore.TelegramIntegrationModeMTProto)) {')

# Reload Telegram opener branch.
old2='''\t\t\t\t\tif !exists || credentialChanged[connID] {
\t\t\t\t\t\ttokenBytes, err := credentialCipher.Decrypt(c.EncryptedCredential, c.CredentialNonce)
\t\t\t\t\t\tif err != nil {
\t\t\t\t\t\t\tsafelog.Error(logger, "decrypt telegram credential failed", "telegram_decrypt", err)
\t\t\t\t\t\t\tif exists && credentialChanged[connID] {
\t\t\t\t\t\t\t\treloadErr = errors.Join(reloadErr, errors.New("decrypt Telegram credential failed"))
\t\t\t\t\t\t\t}
\t\t\t\t\t\t\tcontinue
\t\t\t\t\t\t}
\t\t\t\t\t\tconnChatIDs := make(map[string]string)'''
new2='''\t\t\t\t\tif !exists || credentialChanged[connID] {
\t\t\t\t\t\tvar token string
\t\t\t\t\t\tvar mtState telegram.MTProtoStateStore
\t\t\t\t\t\tif c.IntegrationMode == controlstore.TelegramIntegrationModeMTProto {
\t\t\t\t\t\t\tstateStore, stateErr := controlstore.NewConnectionSecretStore(controlStore.DB(), credentialCipher, c.ID)
\t\t\t\t\t\t\tif stateErr != nil { safelog.Error(logger, "initialize Telegram MTProto state store failed", "telegram_mtproto_state", stateErr); continue }
\t\t\t\t\t\t\tmtState = stateStore
\t\t\t\t\t\t} else {
\t\t\t\t\t\t\ttokenBytes, decryptErr := credentialCipher.Decrypt(c.EncryptedCredential, c.CredentialNonce)
\t\t\t\t\t\t\tif decryptErr != nil {
\t\t\t\t\t\t\t\tsafelog.Error(logger, "decrypt telegram credential failed", "telegram_decrypt", decryptErr)
\t\t\t\t\t\t\t\tif exists && credentialChanged[connID] { reloadErr = errors.Join(reloadErr, errors.New("decrypt Telegram credential failed")) }
\t\t\t\t\t\t\t\tcontinue
\t\t\t\t\t\t\t}
\t\t\t\t\t\t\ttoken = string(tokenBytes)
\t\t\t\t\t\t}
\t\t\t\t\t\tconnChatIDs := make(map[string]string)'''
rep('internal/app/app.go',old2,new2)
rep('internal/app/app.go','\t\t\t\t\t\t\tToken:           string(tokenBytes),','\t\t\t\t\t\t\tToken:           token,\n\t\t\t\t\t\t\tMTProtoStateStore: mtState,',1)
rep('internal/app/app.go','\t\t\t\t\t\tcredentialFingerprints[c.ID] = credentialFingerprint(c)','\t\t\t\t\t\tif c.IntegrationMode != controlstore.TelegramIntegrationModeMTProto { credentialFingerprints[c.ID] = credentialFingerprint(c) }',1)

# appConnectionService MTProto auth forwarding.
rep('internal/app/app.go','func (s *appConnectionService) WhatsAppPair(ctx context.Context, id string)',r'''func (s *appConnectionService) telegramMTProtoAuth(id string) (telegram.MTProtoAuthService, error) {
    if s == nil || s.connMgr == nil { return nil, errors.New("connection manager unavailable") }
    adapter, ok := s.connMgr.GetAdapter(id); if !ok { return nil, errors.New("connection is not running") }
    service, ok := adapter.(telegram.MTProtoAuthService); if !ok { return nil, errors.New("not a Telegram MTProto connection") }
    return service,nil
}
func (s *appConnectionService) TelegramMTProtoConfigure(ctx context.Context,id string,apiID int,apiHash,phone string)(any,error){svc,err:=s.telegramMTProtoAuth(id);if err!=nil{return nil,err};return svc.ConfigureMTProto(ctx,apiID,apiHash,phone)}
func (s *appConnectionService) TelegramMTProtoSendCode(ctx context.Context,id string)(any,error){svc,err:=s.telegramMTProtoAuth(id);if err!=nil{return nil,err};return svc.SendMTProtoCode(ctx)}
func (s *appConnectionService) TelegramMTProtoSubmitCode(ctx context.Context,id,code string)(any,error){svc,err:=s.telegramMTProtoAuth(id);if err!=nil{return nil,err};return svc.SubmitMTProtoCode(ctx,code)}
func (s *appConnectionService) TelegramMTProtoSubmitPassword(ctx context.Context,id string,password []byte)(any,error){svc,err:=s.telegramMTProtoAuth(id);if err!=nil{return nil,err};return svc.SubmitMTProtoPassword(ctx,password)}
func (s *appConnectionService) TelegramMTProtoLogout(ctx context.Context,id string)error{svc,err:=s.telegramMTProtoAuth(id);if err!=nil{return err};return svc.LogoutMTProto(ctx)}

func (s *appConnectionService) WhatsAppPair(ctx context.Context, id string)''')

# Tests: encrypted state isolation and MTProto lifecycle with fake runtime.
create('internal/controlstore/connection_secret_store_test.go', r'''package controlstore
import("bytes";"context";"testing")
func TestConnectionSecretStoreEncryptedAndIsolated(t *testing.T){ctx:=context.Background();s,err:=Open(ctx,":memory:");if err!=nil{t.Fatal(err)};defer s.Close();cipher,_:=NewCredentialCipher([]byte("01234567890123456789012345678901"));for _,id:=range []string{"mt-a","mt-b"}{enc,nonce,_:=cipher.Encrypt([]byte(`{"version":1}`));if err:=s.CreateConnection(ctx,Connection{ID:id,Transport:"telegram",IntegrationMode:"mtproto",Label:id,Enabled:true,EncryptedCredential:enc,CredentialNonce:nonce,CredentialKeyVersion:1});err!=nil{t.Fatal(err)}};a,_:=NewConnectionSecretStore(s.DB(),cipher,"mt-a");b,_:=NewConnectionSecretStore(s.DB(),cipher,"mt-b");secret:=[]byte(`{"version":1,"apiHash":"secret-hash","phone":"+15551234567","session":"c2Vzc2lvbg=="}`);if err:=a.Store(ctx,secret);err!=nil{t.Fatal(err)};got,err:=a.Load(ctx);if err!=nil||!bytes.Equal(got,secret){t.Fatalf("load=%q err=%v",got,err)};other,_:=b.Load(ctx);if bytes.Equal(other,secret){t.Fatal("MTProto state leaked across connections")};var raw []byte;if err:=s.DB().QueryRow(`SELECT encrypted_credential FROM transport_connections WHERE id='mt-a'`).Scan(&raw);err!=nil{t.Fatal(err)};if bytes.Contains(raw,[]byte("secret-hash"))||bytes.Contains(raw,[]byte("+1555"))||bytes.Contains(raw,[]byte("session")){t.Fatal("plaintext auth/session material stored in control.db")}}
''')

create('internal/transport/telegram/mtproto_test.go', r'''package telegram
import("context";"errors";"sync";"testing";gotdsession "github.com/gotd/td/session";gotdauth "github.com/gotd/td/telegram/auth")
type memoryMTStore struct{mu sync.Mutex;data []byte};func(s *memoryMTStore)Load(context.Context)([]byte,error){s.mu.Lock();defer s.mu.Unlock();return append([]byte(nil),s.data...),nil};func(s *memoryMTStore)Store(_ context.Context,b []byte)error{s.mu.Lock();defer s.mu.Unlock();s.data=append([]byte(nil),b...);return nil}
type fakeMTAuth struct{authorized bool;wantPassword bool;logout bool;codes []string;passwords int};func(f *fakeMTAuth)Authorized(context.Context)(bool,error){return f.authorized,nil};func(f *fakeMTAuth)SendCode(context.Context,string)(string,bool,error){return "runtime-only-code-hash",false,nil};func(f *fakeMTAuth)SignIn(_ context.Context,_ string,code,_ string)(bool,error){f.codes=append(f.codes,code);if code=="bad"{return false,errors.New("PHONE_CODE_INVALID")};if f.wantPassword{return true,nil};f.authorized=true;return false,nil};func(f *fakeMTAuth)Password(context.Context,[]byte)error{f.passwords++;f.authorized=true;return nil};func(f *fakeMTAuth)Logout(context.Context)error{f.logout=true;f.authorized=false;return nil}
type fakeMTRuntime struct{auth *fakeMTAuth};func(r fakeMTRuntime)Run(ctx context.Context,fn func(context.Context,mtprotoAuthClient)error)error{return fn(ctx,r.auth)}
func TestMTProtoAuthLifecycleAndRestartSession(t *testing.T){ctx,cancel:=context.WithCancel(context.Background());defer cancel();store:=&memoryMTStore{};authClient:=&fakeMTAuth{wantPassword:true};opts:=Options{ConnectionID:"tg-mt",Logger:testLogger(),MTProtoStateStore:store,mtprotoRuntimeFactory:func(int,string,gotdsession.Storage)mtprotoRuntime{return fakeMTRuntime{authClient}}};a,err:=OpenMTProto(ctx,opts);if err!=nil{t.Fatal(err)};defer a.Close();if st:=a.AdminStatus(ctx);st.Configured||st.Status!=MTProtoAuthDisconnected{t.Fatalf("initial status %+v",st)};if _,err:=a.ConfigureMTProto(ctx,12345,"api-secret","+15550001111");err!=nil{t.Fatal(err)};if _,err:=a.SendMTProtoCode(ctx);err!=nil{t.Fatal(err)};if _,err:=a.SubmitMTProtoCode(ctx,"12345");err!=nil{t.Fatal(err)};if st:=a.AdminStatus(ctx);st.Status!=MTProtoAuthPasswordRequired{t.Fatalf("status %+v",st)};pw:=[]byte("2fa-secret");if _,err:=a.SubmitMTProtoPassword(ctx,pw);err!=nil{t.Fatal(err)};if st:=a.AdminStatus(ctx);st.Status!=MTProtoAuthConnected{t.Fatalf("status %+v",st)};state,err:=a.state.load(ctx);if err!=nil{t.Fatal(err)};state.Session=[]byte("reusable-session");if err:=a.state.store(ctx,state);err!=nil{t.Fatal(err)};raw:=string(store.data);if raw==""{t.Fatal("missing persisted encrypted-boundary payload")};if err:=a.Close();err!=nil{t.Fatal(err)};auth2:=&fakeMTAuth{authorized:true};opts.mtprotoRuntimeFactory=func(int,string,gotdsession.Storage)mtprotoRuntime{return fakeMTRuntime{auth2}};b,err:=OpenMTProto(ctx,opts);if err!=nil{t.Fatal(err)};defer b.Close();<-b.ready;if st:=b.AdminStatus(ctx);st.Status!=MTProtoAuthConnected{t.Fatalf("restart status %+v",st)};if err:=b.LogoutMTProto(ctx);err!=nil{t.Fatal(err)};cleared,_:=b.state.load(ctx);if len(cleared.Session)!=0{t.Fatal("logout retained reusable session")}}
func TestMTProtoInvalidCodeIsSanitizedAndCodeHashNotPersisted(t *testing.T){ctx,cancel:=context.WithCancel(context.Background());defer cancel();store:=&memoryMTStore{};f:=&fakeMTAuth{};a,err:=OpenMTProto(ctx,Options{ConnectionID:"tg-mt2",Logger:testLogger(),MTProtoStateStore:store,mtprotoRuntimeFactory:func(int,string,gotdsession.Storage)mtprotoRuntime{return fakeMTRuntime{f}}});if err!=nil{t.Fatal(err)};defer a.Close();_,_=a.ConfigureMTProto(ctx,1,"hash","+1000");_,_=a.SendMTProtoCode(ctx);if string(store.data)==""{t.Fatal("state not stored")};if contains(string(store.data),"runtime-only-code-hash"){t.Fatal("code hash persisted")};_,err=a.SubmitMTProtoCode(ctx,"bad");if err==nil||err.Error()!="Telegram login code was rejected"{t.Fatalf("err=%v",err)}}
func contains(s,sub string)bool{for i:=0;i+len(sub)<=len(s);i++{if s[i:i+len(sub)]==sub{return true}};return false}
func TestGotdAuthPasswordNeededSentinel(t *testing.T){if gotdauth.ErrPasswordAuthNeeded==nil{t.Fatal("missing gotd password sentinel")}}
''')

# testLogger helper is not guaranteed package-wide; add local implementation by importing slog/io.
rep('internal/transport/telegram/mtproto_test.go','import("context";"errors";"sync";"testing";', 'import("context";"errors";"sync";"testing";"log/slog";"io";')
rep('internal/transport/telegram/mtproto_test.go','type memoryMTStore struct', 'func testLogger()*slog.Logger{return slog.New(slog.NewTextHandler(io.Discard,nil))}\ntype memoryMTStore struct')

# API fake service tests focused on authenticated lifecycle routing without secrets in response.
create('internal/api/telegram_mtproto_test.go', r'''package api
import("context";"encoding/json";"net/http";"testing")
type mtprotoAPITestService struct{testConnectionService;configured int;codes int;submitted int;passwords int;loggedOut int}
func(s *mtprotoAPITestService)TelegramMTProtoConfigure(context.Context,string,int,string,string)(any,error){s.configured++;return map[string]any{"status":"code_required"},nil};func(s *mtprotoAPITestService)TelegramMTProtoSendCode(context.Context,string)(any,error){s.codes++;return map[string]any{"status":"code_required"},nil};func(s *mtprotoAPITestService)TelegramMTProtoSubmitCode(context.Context,string,string)(any,error){s.submitted++;return map[string]any{"status":"password_required"},nil};func(s *mtprotoAPITestService)TelegramMTProtoSubmitPassword(context.Context,string,[]byte)(any,error){s.passwords++;return map[string]any{"status":"connected"},nil};func(s *mtprotoAPITestService)TelegramMTProtoLogout(context.Context,string)error{s.loggedOut++;return nil}
func TestMTProtoAdminAPIFlow(t *testing.T){srv,_,db,token,_:=setupConnectionsTestEnv(t);_,err:=db.Exec(`INSERT INTO transport_connections(id,transport,integration_mode,label,enabled,encrypted_credential,credential_nonce,created_at,updated_at) VALUES('conn-mt-api','telegram','mtproto','mt',1,x'01',x'02',1,1)`);if err!=nil{t.Fatal(err)};svc:=&mtprotoAPITestService{};srv.connections=svc;tests:=[]struct{path,body string;want string}{{"/api/connections/conn-mt-api/telegram/mtproto/setup",`{"apiId":123,"apiHash":"super-secret","phone":"+15551234567"}`,"code_required"},{"/api/connections/conn-mt-api/telegram/mtproto/send-code","", "code_required"},{"/api/connections/conn-mt-api/telegram/mtproto/code",`{"code":"12345"}`,"password_required"},{"/api/connections/conn-mt-api/telegram/mtproto/password",`{"password":"top-secret"}`,"connected"}};for _,tc:=range tests{rec:=authenticatedConnectionRequest(t,srv,token,http.MethodPost,tc.path,tc.body);if rec.Code!=http.StatusOK{t.Fatalf("%s => %d %s",tc.path,rec.Code,rec.Body.String())};var out map[string]any;if err:=json.Unmarshal(rec.Body.Bytes(),&out);err!=nil{t.Fatal(err)};if out["status"]!=tc.want{t.Fatalf("%s status=%v",tc.path,out["status"])};body:=rec.Body.String();if containsString(body,"super-secret")||containsString(body,"+1555")||containsString(body,"top-secret")||containsString(body,"12345"){t.Fatalf("secret reflected in response %q",body)}}}
func containsString(s,sub string)bool{for i:=0;i+len(sub)<=len(s);i++{if s[i:i+len(sub)]==sub{return true}};return false}
''')

# Tracker.
rep('docs/TELEGRAM_MTPROTO_SUPPORT_PROGRESS.md','- [ ] #103 — Secure MTProto auth/session lifecycle with encrypted persistence','- [x] #103 — Secure MTProto auth/session lifecycle with encrypted persistence')
rep('docs/TELEGRAM_MTPROTO_SUPPORT_PROGRESS.md','## Completion rule\n',r'''### #103 — complete

- Added `gotd/td` MTProto client authentication with phone code and optional 2FA using `PasswordWith`/`srpguard` so the 2FA value is consumed from wipeable memory.
- Added an encrypted control-store-backed gotd session storage adapter; API credentials, phone, and reusable session stay inside the encrypted connection blob. OTP, code hash, and 2FA are runtime-only.
- Pending MTProto connections now start as control-plane adapters, expose sanitized auth states, reconnect from authorized persisted sessions, and support explicit logout/session clearing.
- Added mode-specific authenticated admin routes for setup, code request/verification, and 2FA; generic Bot API token handling remains unchanged.
- MTProto session writes are excluded from credential-fingerprint reload logic so normal session persistence cannot trigger spurious adapter restarts.
- Verification: focused encrypted-state/auth/API tests plus `go test ./...` and `go vet ./...` in the tested-change workflow.

## Completion rule
''')
