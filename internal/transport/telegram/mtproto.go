package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/awnumar/memguard"
	gotdsession "github.com/gotd/td/session"
	gotdtelegram "github.com/gotd/td/telegram"
	gotdauth "github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/srpguard"
	"github.com/gotd/td/tg"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/transport"
)

const mtprotoStateVersion = 1

const (
	MTProtoAuthDisconnected     = "disconnected"
	MTProtoAuthCodeRequired     = "code_required"
	MTProtoAuthPasswordRequired = "password_required"
	MTProtoAuthConnected        = "connected"
	MTProtoAuthError            = "error"
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
	if err != nil {
		return "", false, err
	}
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
	if errors.Is(err, gotdauth.ErrPasswordAuthNeeded) {
		return true, nil
	}
	return false, err
}
func (c *gotdAuthClient) Password(ctx context.Context, password []byte) error {
	if len(password) == 0 {
		return errors.New("password is required")
	}
	buf := memguard.NewBufferFromBytes(password)
	_, err := c.client.Auth().PasswordWith(ctx, srpguard.LockedBuffer(buf))
	return err
}
func (c *gotdAuthClient) Logout(ctx context.Context) error {
	_, err := c.client.API().AuthLogOut(ctx)
	return err
}

type MTProtoAdapter struct {
	connectionID    string
	logger          interface{ Error(string, ...any) }
	events          chan transport.Incoming
	state           *mtprotoStateBox
	runtimeFactory  mtprotoRuntimeFactory
	lifecycleCtx    context.Context
	lifecycleCancel context.CancelFunc

	mu         sync.RWMutex
	cfg        *config.Config
	authState  string
	configured bool
	auth       mtprotoAuthClient
	codeHash   string
	runCancel  context.CancelFunc
	runDone    chan struct{}
	ready      chan struct{}
	readyOnce  *sync.Once
	closed     bool
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
	if ctx == nil || opts.Logger == nil {
		return nil, errors.New("context and logger are required")
	}
	if err := config.ValidateConnectionID(opts.ConnectionID); err != nil {
		return nil, err
	}
	if opts.MTProtoStateStore == nil {
		return nil, errors.New("encrypted MTProto state store is required")
	}
	factory := opts.mtprotoRuntimeFactory
	if factory == nil {
		factory = newGotdRuntime
	}
	lifecycleCtx, lifecycleCancel := context.WithCancel(ctx)
	adapter := &MTProtoAdapter{
		connectionID: strings.TrimSpace(opts.ConnectionID), events: make(chan transport.Incoming, eventBufferSize),
		state: &mtprotoStateBox{raw: opts.MTProtoStateStore}, runtimeFactory: factory,
		lifecycleCtx: lifecycleCtx, lifecycleCancel: lifecycleCancel, authState: MTProtoAuthDisconnected,
	}
	state, err := adapter.state.load(ctx)
	if err != nil {
		return nil, err
	}
	if state.APIID > 0 && strings.TrimSpace(state.APIHash) != "" && strings.TrimSpace(state.Phone) != "" {
		adapter.configured = true
		adapter.startRuntime(lifecycleCtx, state)
	}
	return adapter, nil
}

func (a *MTProtoAdapter) Name() string { return "telegram" }
func (a *MTProtoAdapter) ConnectionID() string {
	if a == nil {
		return ""
	}
	return a.connectionID
}
func (a *MTProtoAdapter) Events() <-chan transport.Incoming {
	if a == nil {
		return nil
	}
	return a.events
}
func (a *MTProtoAdapter) UpdateConfig(cfg *config.Config) error {
	if cfg == nil {
		return errors.New("config is required")
	}
	a.mu.Lock()
	a.cfg = cfg
	a.mu.Unlock()
	return nil
}
func (a *MTProtoAdapter) Send(context.Context, transport.Outgoing) (transport.MessageRef, error) {
	return transport.MessageRef{}, errors.New("Telegram MTProto live messaging is not enabled yet")
}
func (a *MTProtoAdapter) React(context.Context, transport.Reaction) error {
	return errors.New("Telegram MTProto live messaging is not enabled yet")
}
func (a *MTProtoAdapter) Edit(context.Context, transport.MessageRef, string) error {
	return errors.New("Telegram MTProto live messaging is not enabled yet")
}
func (a *MTProtoAdapter) Delete(context.Context, transport.MessageRef) error {
	return errors.New("Telegram MTProto live messaging is not enabled yet")
}

func (a *MTProtoAdapter) Close() error {
	if a == nil {
		return nil
	}
	if a.lifecycleCancel != nil {
		a.lifecycleCancel()
	}
	a.stopRuntime()
	a.mu.Lock()
	if !a.closed {
		close(a.events)
		a.closed = true
	}
	a.mu.Unlock()
	return nil
}

func (a *MTProtoAdapter) startRuntime(parent context.Context, state mtprotoState) {
	a.stopRuntime()
	runCtx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	ready := make(chan struct{})
	once := &sync.Once{}
	runtime := a.runtimeFactory(state.APIID, state.APIHash, encryptedSessionStorage{box: a.state})
	a.mu.Lock()
	a.runCancel, a.runDone, a.ready, a.readyOnce = cancel, done, ready, once
	a.authState = MTProtoAuthDisconnected
	a.mu.Unlock()
	go func() {
		defer close(done)
		err := runtime.Run(runCtx, func(clientCtx context.Context, authClient mtprotoAuthClient) error {
			authorized, statusErr := authClient.Authorized(clientCtx)
			a.mu.Lock()
			a.auth = authClient
			if statusErr != nil {
				a.authState = MTProtoAuthError
			} else if authorized {
				a.authState = MTProtoAuthConnected
			} else {
				a.authState = MTProtoAuthCodeRequired
			}
			a.mu.Unlock()
			once.Do(func() { close(ready) })
			if statusErr != nil {
				return statusErr
			}
			<-clientCtx.Done()
			return nil
		})
		a.mu.Lock()
		a.auth = nil
		if !errors.Is(err, context.Canceled) && err != nil {
			a.authState = MTProtoAuthError
		} else if a.authState != MTProtoAuthConnected {
			a.authState = MTProtoAuthDisconnected
		}
		a.mu.Unlock()
		once.Do(func() { close(ready) })
	}()
}

func (a *MTProtoAdapter) stopRuntime() {
	if a == nil {
		return
	}
	a.mu.Lock()
	cancel, done := a.runCancel, a.runDone
	a.runCancel = nil
	a.runDone = nil
	a.auth = nil
	a.codeHash = ""
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (a *MTProtoAdapter) waitAuth(ctx context.Context) (mtprotoAuthClient, error) {
	a.mu.RLock()
	authClient, ready := a.auth, a.ready
	a.mu.RUnlock()
	if authClient != nil {
		return authClient, nil
	}
	if ready == nil {
		return nil, errors.New("MTProto credentials are not configured")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-ready:
	}
	a.mu.RLock()
	authClient = a.auth
	state := a.authState
	a.mu.RUnlock()
	if authClient == nil {
		return nil, fmt.Errorf("Telegram MTProto client unavailable (%s)", state)
	}
	return authClient, nil
}

func (a *MTProtoAdapter) ConfigureMTProto(ctx context.Context, apiID int, apiHash, phone string) (AdminStatus, error) {
	apiHash = strings.TrimSpace(apiHash)
	phone = strings.TrimSpace(phone)
	if apiID <= 0 || apiHash == "" || phone == "" {
		return a.AdminStatus(ctx), errors.New("apiId, apiHash, and phone are required")
	}
	current, err := a.state.load(ctx)
	if err != nil {
		return a.AdminStatus(ctx), err
	}
	if current.APIID != apiID || current.APIHash != apiHash || current.Phone != phone {
		current.Session = nil
	}
	current.APIID, current.APIHash, current.Phone = apiID, apiHash, phone
	if err := a.state.store(ctx, current); err != nil {
		return a.AdminStatus(ctx), err
	}
	a.mu.Lock()
	a.configured = true
	a.codeHash = ""
	a.mu.Unlock()
	a.startRuntime(a.lifecycleCtx, current)
	return a.AdminStatus(ctx), nil
}

func (a *MTProtoAdapter) SendMTProtoCode(ctx context.Context) (AdminStatus, error) {
	state, err := a.state.load(ctx)
	if err != nil {
		return a.AdminStatus(ctx), err
	}
	if state.APIID <= 0 || strings.TrimSpace(state.APIHash) == "" || strings.TrimSpace(state.Phone) == "" {
		return a.AdminStatus(ctx), errors.New("MTProto credentials are not configured")
	}
	client, err := a.waitAuth(ctx)
	if err != nil {
		return a.AdminStatus(ctx), err
	}
	hash, authorized, err := client.SendCode(ctx, state.Phone)
	if err != nil {
		a.setAuthError()
		return a.AdminStatus(ctx), errors.New("failed to request Telegram login code")
	}
	a.mu.Lock()
	a.codeHash = hash
	if authorized {
		a.authState = MTProtoAuthConnected
	} else {
		a.authState = MTProtoAuthCodeRequired
	}
	a.mu.Unlock()
	return a.AdminStatus(ctx), nil
}

func (a *MTProtoAdapter) SubmitMTProtoCode(ctx context.Context, code string) (AdminStatus, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return a.AdminStatus(ctx), errors.New("login code is required")
	}
	state, err := a.state.load(ctx)
	if err != nil {
		return a.AdminStatus(ctx), err
	}
	a.mu.RLock()
	hash := a.codeHash
	a.mu.RUnlock()
	if hash == "" {
		return a.AdminStatus(ctx), errors.New("request a new login code first")
	}
	client, err := a.waitAuth(ctx)
	if err != nil {
		return a.AdminStatus(ctx), err
	}
	passwordRequired, err := client.SignIn(ctx, state.Phone, code, hash)
	if err != nil {
		a.setAuthError()
		return a.AdminStatus(ctx), errors.New("Telegram login code was rejected")
	}
	a.mu.Lock()
	a.codeHash = ""
	if passwordRequired {
		a.authState = MTProtoAuthPasswordRequired
	} else {
		a.authState = MTProtoAuthConnected
	}
	a.mu.Unlock()
	return a.AdminStatus(ctx), nil
}

func (a *MTProtoAdapter) SubmitMTProtoPassword(ctx context.Context, password []byte) (AdminStatus, error) {
	if len(password) == 0 {
		return a.AdminStatus(ctx), errors.New("2FA password is required")
	}
	defer func() {
		for i := range password {
			password[i] = 0
		}
	}()
	client, err := a.waitAuth(ctx)
	if err != nil {
		return a.AdminStatus(ctx), err
	}
	a.mu.RLock()
	state := a.authState
	a.mu.RUnlock()
	if state != MTProtoAuthPasswordRequired {
		return a.AdminStatus(ctx), errors.New("Telegram 2FA password is not currently required")
	}
	if err := client.Password(ctx, password); err != nil {
		a.setAuthError()
		return a.AdminStatus(ctx), errors.New("Telegram 2FA password was rejected")
	}
	a.mu.Lock()
	a.authState = MTProtoAuthConnected
	a.mu.Unlock()
	return a.AdminStatus(ctx), nil
}
func (a *MTProtoAdapter) setAuthError() { a.mu.Lock(); a.authState = MTProtoAuthError; a.mu.Unlock() }

func (a *MTProtoAdapter) LogoutMTProto(ctx context.Context) error {
	a.mu.RLock()
	client := a.auth
	a.mu.RUnlock()
	if client != nil {
		_ = client.Logout(ctx)
	}
	a.stopRuntime()
	state, err := a.state.load(ctx)
	if err != nil {
		return err
	}
	state.Session = nil
	if err := a.state.store(ctx, state); err != nil {
		return err
	}
	a.mu.Lock()
	a.authState = MTProtoAuthDisconnected
	a.codeHash = ""
	a.mu.Unlock()
	return nil
}

func (a *MTProtoAdapter) AdminStatus(_ context.Context) AdminStatus {
	status := AdminStatus{IntegrationMode: "mtproto", Status: MTProtoAuthDisconnected, Endpoints: []EndpointReadiness{}, Capabilities: CapabilitiesForIntegrationMode("mtproto")}
	if a == nil {
		return status
	}
	a.mu.RLock()
	status.Status = a.authState
	status.Running = a.auth != nil
	status.Configured = a.configured
	cfg := a.cfg
	a.mu.RUnlock()
	if cfg != nil {
		aliases := make([]string, 0)
		for alias, ep := range cfg.Endpoints {
			if ep.Transport == config.TransportTelegram && ep.ConnectionID == a.connectionID {
				aliases = append(aliases, alias)
			}
		}
		sort.Strings(aliases)
		for _, alias := range aliases {
			ready := "unavailable"
			if status.Status == MTProtoAuthConnected {
				ready = "ready"
			}
			status.Endpoints = append(status.Endpoints, EndpointReadiness{Alias: alias, Status: ready})
		}
	}
	return status
}
func (a *MTProtoAdapter) DiscoverChats(context.Context) ([]DiscoveredChat, error) {
	return nil, errors.New("full MTProto discovery is not enabled yet")
}
func (a *MTProtoAdapter) ValidateTarget(context.Context, string) error {
	return ErrTargetValidationUnavailable
}
