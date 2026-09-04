package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/controlstore"
	"github.com/vm75/message-sync/internal/store"
)

func setupAuthServer(t *testing.T, ttl time.Duration) (*Server, *sql.DB, *sql.DB) {
	t.Helper()
	syncStore, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	controlStore, err := controlstore.Open(context.Background(), filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syncStore.Close(); controlStore.Close() })
	srv := NewServer(Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DB: syncStore.DB(), ControlDB: controlStore.DB(), SessionTTL: ttl})
	return srv, syncStore.DB(), controlStore.DB()
}

// setupTestDB/setupTestServer are shared fixtures for the API package tests.
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st.DB()
}

func setupTestServer(t *testing.T, db *sql.DB) *Server {
	t.Helper()
	cs, err := controlstore.Open(context.Background(), filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	now := time.Now().UnixMilli()
	if _, err := cs.DB().Exec(`INSERT INTO users(id,username,password_hash,role,active,created_at,updated_at) VALUES ('fixture','fixture','$2a$10$7EqJtq98hPqEX7fNZaFWoOe0VdZK0VdJf7hQJmJj1L2R7F1T9D3mK','admin',1,?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.DB().Exec(`INSERT INTO transport_connections (id, transport, label, enabled, encrypted_credential, credential_nonce, created_at, updated_at) VALUES
		('conn-wa-1', 'whatsapp', 'wa', 1, NULL, NULL, ?, ?),
		('conn-dc-1', 'discord', 'dc', 1, X'0102', X'0304', ?, ?),
		('conn-tg-1', 'telegram', 'tg', 1, X'0506', X'0708', ?, ?)`, now, now, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	return NewServer(Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DB: db, ControlDB: cs.DB(), SessionTTL: time.Hour, Connections: testConnectionService{tg: &fakeTelegramAdminService{}}})
}

func request(t *testing.T, srv *Server, method, path string, body any, token string) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func tokenFrom(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out AuthTokenResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Token
}

func TestAuthSetupLoginAndServerSideSessions(t *testing.T) {
	srv, syncDB, controlDB := setupAuthServer(t, time.Hour)
	if rec := request(t, srv, http.MethodGet, "/api/auth/status", nil, ""); rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}
	setup := request(t, srv, http.MethodPost, "/api/auth/setup", AuthCredentialsRequest{Username: "First.Admin", Password: "password-one"}, "")
	if setup.Code != 200 {
		t.Fatalf("setup=%d %s", setup.Code, setup.Body.String())
	}
	first := tokenFrom(t, setup)
	if _, err := syncDB.Exec("SELECT 1 FROM global_config WHERE admin_password_hash IS NOT NULL"); err == nil {
		t.Fatal("legacy password field remains")
	}
	var count int
	if err := controlDB.QueryRow("SELECT COUNT(*) FROM users WHERE role='admin'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("admin count=%d err=%v", count, err)
	}
	if rec := request(t, srv, http.MethodPost, "/api/auth/setup", AuthCredentialsRequest{Username: "second", Password: "password-two"}, ""); rec.Code != 400 {
		t.Fatalf("duplicate setup=%d", rec.Code)
	}
	login := request(t, srv, http.MethodPost, "/api/auth/login", AuthCredentialsRequest{Username: "FIRST.ADMIN", Password: "password-one"}, "")
	if login.Code != 200 {
		t.Fatalf("login=%d", login.Code)
	}
	second := tokenFrom(t, login)
	if first == second {
		t.Fatal("sessions are not independent")
	}
	if rec := request(t, srv, http.MethodGet, "/api/config", nil, first); rec.Code != 200 {
		t.Fatalf("authenticated API=%d", rec.Code)
	}
	if rec := request(t, srv, http.MethodPost, "/api/auth/logout", nil, first); rec.Code != 200 {
		t.Fatalf("logout=%d", rec.Code)
	}
	if rec := request(t, srv, http.MethodGet, "/api/config", nil, first); rec.Code != 401 {
		t.Fatalf("revoked session=%d", rec.Code)
	}
	if rec := request(t, srv, http.MethodGet, "/api/config", nil, second); rec.Code != 200 {
		t.Fatalf("other session affected=%d", rec.Code)
	}
}

func TestAuthPasswordChangeRevokesOtherSessionsAndDeactivation(t *testing.T) {
	srv, _, controlDB := setupAuthServer(t, time.Hour)
	first := tokenFrom(t, request(t, srv, http.MethodPost, "/api/auth/setup", AuthCredentialsRequest{Username: "admin", Password: "password-one"}, ""))
	second := tokenFrom(t, request(t, srv, http.MethodPost, "/api/auth/login", AuthCredentialsRequest{Username: "admin", Password: "password-one"}, ""))
	if rec := request(t, srv, http.MethodPost, "/api/auth/change-password", AuthChangePasswordRequest{CurrentPassword: "password-one", NewPassword: "password-new"}, first); rec.Code != 200 {
		t.Fatalf("change=%d", rec.Code)
	}
	if rec := request(t, srv, http.MethodGet, "/api/config", nil, second); rec.Code != 401 {
		t.Fatalf("other session after password change=%d", rec.Code)
	}
	if rec := request(t, srv, http.MethodPost, "/api/auth/login", AuthCredentialsRequest{Username: "admin", Password: "password-new"}, ""); rec.Code != 200 {
		t.Fatalf("new password login=%d", rec.Code)
	}
	var userID string
	if err := controlDB.QueryRow("SELECT id FROM users WHERE username='admin'").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := controlDB.Exec("UPDATE users SET active=0 WHERE id=?", userID); err != nil {
		t.Fatal(err)
	}
	if rec := request(t, srv, http.MethodGet, "/api/config", nil, first); rec.Code != 401 {
		t.Fatalf("deactivated session=%d", rec.Code)
	}
}

func TestAuthMalformedExpiredAndSecretFreeLogs(t *testing.T) {
	srv, _, _ := setupAuthServer(t, 1*time.Second)
	if rec := request(t, srv, http.MethodGet, "/api/config", nil, "not-a-token"); rec.Code != 401 {
		t.Fatalf("malformed=%d", rec.Code)
	}
	logs := new(bytes.Buffer)
	srv.logger = slog.New(slog.NewTextHandler(logs, nil))
	if rec := request(t, srv, http.MethodPost, "/api/auth/login", AuthCredentialsRequest{Username: "missing", Password: "secret-value"}, ""); rec.Code != 401 {
		t.Fatalf("missing login=%d", rec.Code)
	}
	if strings.Contains(logs.String(), "secret-value") || strings.Contains(logs.String(), "missing") {
		t.Fatalf("sensitive login data logged: %s", logs.String())
	}
}

func TestRBACInvitesDeactivationAndAudit(t *testing.T) {
	srv, _, controlDB := setupAuthServer(t, time.Hour)
	admin := tokenFrom(t, request(t, srv, http.MethodPost, "/api/auth/setup", AuthCredentialsRequest{Username: "admin", Password: "password-one"}, ""))
	invite := request(t, srv, http.MethodPost, "/api/users/invites", InviteRequest{Role: "operator", TTLHours: 1}, admin)
	if invite.Code != http.StatusCreated {
		t.Fatalf("invite=%d", invite.Code)
	}
	var inviteResponse SecretTokenResponse
	_ = json.NewDecoder(invite.Body).Decode(&inviteResponse)
	operatorResponse := request(t, srv, http.MethodPost, "/api/auth/invite/redeem", RedeemInviteRequest{Token: inviteResponse.Token, Username: "operator", Password: "password-two"}, "")
	if operatorResponse.Code != http.StatusOK {
		t.Fatalf("redeem=%d", operatorResponse.Code)
	}
	operator := tokenFrom(t, operatorResponse)
	if rec := request(t, srv, http.MethodGet, "/api/users", nil, operator); rec.Code != http.StatusForbidden {
		t.Fatalf("operator users=%d", rec.Code)
	}
	var operatorID string
	if err := controlDB.QueryRow("SELECT id FROM users WHERE username='operator'").Scan(&operatorID); err != nil {
		t.Fatal(err)
	}
	if rec := request(t, srv, http.MethodPost, "/api/users/"+operatorID+"/active", SetActiveRequest{Active: false}, admin); rec.Code != http.StatusOK {
		t.Fatalf("deactivate=%d", rec.Code)
	}
	if rec := request(t, srv, http.MethodGet, "/api/config", nil, operator); rec.Code != http.StatusUnauthorized {
		t.Fatalf("deactivated operator=%d", rec.Code)
	}
	if rec := request(t, srv, http.MethodPost, "/api/users/"+"missing"+"/active", SetActiveRequest{Active: false}, admin); rec.Code != http.StatusNotFound {
		t.Fatalf("missing user=%d", rec.Code)
	}
	if rec := request(t, srv, http.MethodPost, "/api/users/"+"x"+"/active", SetActiveRequest{Active: false}, admin); rec.Code != http.StatusNotFound {
		t.Fatalf("last admin/missing=%d", rec.Code)
	}
	if rec := request(t, srv, http.MethodGet, "/api/audit", nil, admin); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "invite_created") {
		t.Fatalf("audit=%d %s", rec.Code, rec.Body.String())
	}
}
