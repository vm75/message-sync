package telegram

import (
	"context"
	"errors"
	gotdsession "github.com/gotd/td/session"
	gotdauth "github.com/gotd/td/telegram/auth"
	"io"
	"log/slog"
	"sync"
	"testing"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type memoryMTStore struct {
	mu   sync.Mutex
	data []byte
}

func (s *memoryMTStore) Load(context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.data...), nil
}
func (s *memoryMTStore) Store(_ context.Context, b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = append([]byte(nil), b...)
	return nil
}

type fakeMTAuth struct {
	authorized   bool
	wantPassword bool
	logout       bool
	codes        []string
	passwords    int
}

func (f *fakeMTAuth) Authorized(context.Context) (bool, error) { return f.authorized, nil }
func (f *fakeMTAuth) SendCode(context.Context, string) (string, bool, error) {
	return "runtime-only-code-hash", false, nil
}
func (f *fakeMTAuth) SignIn(_ context.Context, _ string, code, _ string) (bool, error) {
	f.codes = append(f.codes, code)
	if code == "bad" {
		return false, errors.New("PHONE_CODE_INVALID")
	}
	if f.wantPassword {
		return true, nil
	}
	f.authorized = true
	return false, nil
}
func (f *fakeMTAuth) Password(context.Context, []byte) error {
	f.passwords++
	f.authorized = true
	return nil
}
func (f *fakeMTAuth) Logout(context.Context) error { f.logout = true; f.authorized = false; return nil }

type fakeMTRuntime struct{ auth *fakeMTAuth }

func (r fakeMTRuntime) Run(ctx context.Context, fn func(context.Context, mtprotoAuthClient) error) error {
	return fn(ctx, r.auth)
}
func TestMTProtoAuthLifecycleAndRestartSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &memoryMTStore{}
	authClient := &fakeMTAuth{wantPassword: true}
	opts := Options{ConnectionID: "tg-mt", Logger: testLogger(), MTProtoStateStore: store, mtprotoRuntimeFactory: func(int, string, gotdsession.Storage) mtprotoRuntime { return fakeMTRuntime{authClient} }}
	a, err := OpenMTProto(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if st := a.AdminStatus(ctx); st.Configured || st.Status != MTProtoAuthDisconnected {
		t.Fatalf("initial status %+v", st)
	}
	if _, err := a.ConfigureMTProto(ctx, 12345, "api-secret", "+15550001111"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SendMTProtoCode(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SubmitMTProtoCode(ctx, "12345"); err != nil {
		t.Fatal(err)
	}
	if st := a.AdminStatus(ctx); st.Status != MTProtoAuthPasswordRequired {
		t.Fatalf("status %+v", st)
	}
	pw := []byte("2fa-secret")
	if _, err := a.SubmitMTProtoPassword(ctx, pw); err != nil {
		t.Fatal(err)
	}
	if st := a.AdminStatus(ctx); st.Status != MTProtoAuthConnected {
		t.Fatalf("status %+v", st)
	}
	state, err := a.state.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.Session = []byte("reusable-session")
	if err := a.state.store(ctx, state); err != nil {
		t.Fatal(err)
	}
	raw := string(store.data)
	if raw == "" {
		t.Fatal("missing persisted encrypted-boundary payload")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	auth2 := &fakeMTAuth{authorized: true}
	opts.mtprotoRuntimeFactory = func(int, string, gotdsession.Storage) mtprotoRuntime { return fakeMTRuntime{auth2} }
	b, err := OpenMTProto(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	<-b.ready
	if st := b.AdminStatus(ctx); st.Status != MTProtoAuthConnected {
		t.Fatalf("restart status %+v", st)
	}
	if err := b.LogoutMTProto(ctx); err != nil {
		t.Fatal(err)
	}
	cleared, _ := b.state.load(ctx)
	if len(cleared.Session) != 0 {
		t.Fatal("logout retained reusable session")
	}
}
func TestMTProtoConfigureRuntimeOutlivesRequestContext(t *testing.T) {
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()
	store := &memoryMTStore{}
	f := &fakeMTAuth{}
	a, err := OpenMTProto(appCtx, Options{ConnectionID: "tg-lifecycle", Logger: testLogger(), MTProtoStateStore: store, mtprotoRuntimeFactory: func(int, string, gotdsession.Storage) mtprotoRuntime { return fakeMTRuntime{f} }})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	reqCtx, reqCancel := context.WithCancel(context.Background())
	if _, err := a.ConfigureMTProto(reqCtx, 1, "hash", "+1000"); err != nil {
		t.Fatal(err)
	}
	reqCancel()
	if _, err := a.SendMTProtoCode(context.Background()); err != nil {
		t.Fatalf("runtime stopped with request context: %v", err)
	}
}

func TestMTProtoInvalidCodeIsSanitizedAndCodeHashNotPersisted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &memoryMTStore{}
	f := &fakeMTAuth{}
	a, err := OpenMTProto(ctx, Options{ConnectionID: "tg-mt2", Logger: testLogger(), MTProtoStateStore: store, mtprotoRuntimeFactory: func(int, string, gotdsession.Storage) mtprotoRuntime { return fakeMTRuntime{f} }})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	_, _ = a.ConfigureMTProto(ctx, 1, "hash", "+1000")
	_, _ = a.SendMTProtoCode(ctx)
	if string(store.data) == "" {
		t.Fatal("state not stored")
	}
	if contains(string(store.data), "runtime-only-code-hash") {
		t.Fatal("code hash persisted")
	}
	_, err = a.SubmitMTProtoCode(ctx, "bad")
	if err == nil || err.Error() != "Telegram login code was rejected" {
		t.Fatalf("err=%v", err)
	}
}
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
func TestGotdAuthPasswordNeededSentinel(t *testing.T) {
	if gotdauth.ErrPasswordAuthNeeded == nil {
		t.Fatal("missing gotd password sentinel")
	}
}
