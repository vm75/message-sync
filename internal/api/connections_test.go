package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	discord "github.com/vm75/message-sync/internal/transport/discord"
)

type mockConnectionService struct {
	statusResp    any
	statusErr     error
	discoveryResp any
	discoveryErr  error
	pairResp      WhatsAppPairResponse
	pairErr       error
	cancelErr     error
	logoutErr     error
	stoppedConns  []string
}

func (m *mockConnectionService) ConnectionStatus(ctx context.Context, id string) (any, error) {
	if m.statusErr != nil {
		return nil, m.statusErr
	}
	if m.statusResp != nil {
		return m.statusResp, nil
	}
	return map[string]any{"id": id, "status": "running"}, nil
}

func (m *mockConnectionService) ConnectionDiscovery(ctx context.Context, id string) (any, error) {
	if m.discoveryErr != nil {
		return nil, m.discoveryErr
	}
	return m.discoveryResp, nil
}

func (m *mockConnectionService) WhatsAppPair(ctx context.Context, id string) (WhatsAppPairResponse, error) {
	return m.pairResp, m.pairErr
}

func (m *mockConnectionService) WhatsAppCancelPair(ctx context.Context, id string) error {
	return m.cancelErr
}

func (m *mockConnectionService) WhatsAppLogout(ctx context.Context, id string) error {
	return m.logoutErr
}

func (m *mockConnectionService) StopConnection(id string) error {
	m.stoppedConns = append(m.stoppedConns, id)
	return nil
}

func (m *mockConnectionService) ConnectionAdapter(id string) (any, bool) {
	return nil, false
}

func setupConnectionsTestEnv(t *testing.T) (*Server, *sql.DB, *sql.DB, string, string) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := controlstore.Open(context.Background(), filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close(); _ = cs.Close() })

	secret := []byte("01234567890123456789012345678901")
	cipher, err := controlstore.NewCredentialCipher(secret)
	if err != nil {
		t.Fatal(err)
	}

	srv := NewServer(Options{
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:               st.DB(),
		ControlDB:        cs.DB(),
		Secret:           secret,
		CredentialCipher: cipher,
	})

	now := time.Now().UnixMilli()
	// Create admin and operator users
	_, err = cs.DB().Exec(`INSERT INTO users(id, username, password_hash, role, active, created_at, updated_at) VALUES
		('user-admin', 'admin', 'hash', 'admin', 1, ?, ?),
		('user-op', 'operator', 'hash', 'operator', 1, ?, ?)`,
		now, now, now, now,
	)
	if err != nil {
		t.Fatal(err)
	}

	// Create base connections
	dcEnc, dcNonce, _ := cipher.Encrypt([]byte("discord-token-123"))
	tgEnc, tgNonce, _ := cipher.Encrypt([]byte("telegram-token-456"))
	_, err = cs.DB().Exec(`INSERT INTO transport_connections (id, transport, label, enabled, encrypted_credential, credential_nonce, created_at, updated_at) VALUES
		('conn-wa-1', 'whatsapp', 'wa-primary', 1, NULL, NULL, ?, ?),
		('conn-dc-1', 'discord', 'dc-primary', 1, ?, ?, ?, ?),
		('conn-tg-1', 'telegram', 'tg-primary', 1, ?, ?, ?, ?)`,
		now, now, dcEnc, dcNonce, now, now, tgEnc, tgNonce, now, now,
	)
	if err != nil {
		t.Fatal(err)
	}

	// Generate tokens
	adminToken, err := srv.sessions.createSession(context.Background(), "user-admin")
	if err != nil {
		t.Fatal(err)
	}
	opToken, err := srv.sessions.createSession(context.Background(), "user-op")
	if err != nil {
		t.Fatal(err)
	}

	return srv, st.DB(), cs.DB(), adminToken, opToken
}

func TestConnections_AuthAndRBACMatrix(t *testing.T) {
	srv, _, _, adminToken, opToken := setupConnectionsTestEnv(t)

	mock := &mockConnectionService{
		statusResp:    map[string]any{"id": "conn-dc-1", "status": "connected"},
		discoveryResp: []any{},
	}
	srv.connections = mock

	// 1. Unauthenticated requests must return 401
	unauthCases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/connections", ""},
		{http.MethodPost, "/api/connections", `{"transport":"whatsapp","label":"wa2"}`},
		{http.MethodGet, "/api/connections/conn-dc-1", ""},
		{http.MethodPatch, "/api/connections/conn-dc-1", `{"label":"new"}`},
		{http.MethodDelete, "/api/connections/conn-dc-1", ""},
		{http.MethodGet, "/api/connections/conn-dc-1/status", ""},
		{http.MethodGet, "/api/connections/conn-dc-1/discovery", ""},
		{http.MethodPost, "/api/connections/conn-wa-1/pair", ""},
		{http.MethodDelete, "/api/connections/conn-wa-1/pair", ""},
		{http.MethodPost, "/api/connections/conn-wa-1/logout", ""},
	}
	for _, tc := range unauthCases {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("unauthenticated %s %s got status %d, want 401", tc.method, tc.path, rec.Code)
		}
	}

	// 2. Operator role checks:
	// Mutations are Forbidden (403)
	forbiddenCases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/connections", `{"transport":"whatsapp","label":"wa2"}`},
		{http.MethodPatch, "/api/connections/conn-dc-1", `{"label":"new"}`},
		{http.MethodPut, "/api/connections/conn-dc-1", `{"label":"new"}`},
		{http.MethodDelete, "/api/connections/conn-dc-1", ""},
		{http.MethodPost, "/api/connections/conn-wa-1/pair", ""},
		{http.MethodDelete, "/api/connections/conn-wa-1/pair", ""},
		{http.MethodPost, "/api/connections/conn-wa-1/logout", ""},
	}
	for _, tc := range forbiddenCases {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+opToken)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("operator %s %s got status %d, want 403", tc.method, tc.path, rec.Code)
		}
	}

	// Read operations are allowed for Operator (200 OK)
	allowedCases := []struct {
		path string
	}{
		{"/api/connections"},
		{"/api/connections/conn-dc-1"},
		{"/api/connections/conn-dc-1/status"},
		{"/api/connections/conn-dc-1/discovery"},
	}
	for _, tc := range allowedCases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+opToken)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("operator GET %s got status %d, want 200: %s", tc.path, rec.Code, rec.Body.String())
		}
	}

	// 3. Admin role can perform mutations
	adminReq := httptest.NewRequest(http.MethodPost, "/api/connections", strings.NewReader(`{"id":"conn-wa-2","transport":"whatsapp","label":"wa-secondary"}`))
	adminReq.Header.Set("Authorization", "Bearer "+adminToken)
	adminRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(adminRec, adminReq)
	if adminRec.Code != http.StatusCreated {
		t.Fatalf("admin create connection got status %d, want 201: %s", adminRec.Code, adminRec.Body.String())
	}
}

func TestConnections_CRUD_LifecycleAndAuditing(t *testing.T) {
	srv, _, controlDB, adminToken, _ := setupConnectionsTestEnv(t)

	// Create Discord connection with token
	createBody := `{"id":"conn-dc-2","transport":"discord","label":"Secondary Discord","token":"super-secret-bot-token"}`
	req := httptest.NewRequest(http.MethodPost, "/api/connections", strings.NewReader(createBody))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("create connection got %d: %s", rec.Code, rec.Body.String())
	}
	respBody := rec.Body.String()
	// CRITICAL PRIVACY: Token, ciphertext, nonce must NEVER be in response
	for _, secretField := range []string{"super-secret-bot-token", "token", "encrypted_credential", "nonce", "credentialNonce"} {
		if strings.Contains(respBody, secretField) {
			t.Fatalf("connection creation response leaked %q: %s", secretField, respBody)
		}
	}

	// Verify audit event connection_created
	var count int
	err := controlDB.QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'connection_created' AND target_id = 'conn-dc-2'`).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("expected audit event for connection_created, got err=%v count=%d", err, count)
	}

	// Disable connection
	disableReq := httptest.NewRequest(http.MethodPatch, "/api/connections/conn-dc-2", strings.NewReader(`{"enabled":false}`))
	disableReq.Header.Set("Authorization", "Bearer "+adminToken)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, disableReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable connection got %d: %s", rec.Code, rec.Body.String())
	}

	err = controlDB.QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'connection_disabled' AND target_id = 'conn-dc-2'`).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("expected audit event connection_disabled, count=%d", count)
	}

	// Enable connection
	enableReq := httptest.NewRequest(http.MethodPatch, "/api/connections/conn-dc-2", strings.NewReader(`{"enabled":true}`))
	enableReq.Header.Set("Authorization", "Bearer "+adminToken)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, enableReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable connection got %d: %s", rec.Code, rec.Body.String())
	}

	err = controlDB.QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'connection_enabled' AND target_id = 'conn-dc-2'`).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("expected audit event connection_enabled, count=%d", count)
	}

	// Replace token
	replaceReq := httptest.NewRequest(http.MethodPatch, "/api/connections/conn-dc-2", strings.NewReader(`{"token":"replacement-secret-token"}`))
	replaceReq.Header.Set("Authorization", "Bearer "+adminToken)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, replaceReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace token got %d: %s", rec.Code, rec.Body.String())
	}

	replaceBody := rec.Body.String()
	if strings.Contains(replaceBody, "replacement-secret-token") || strings.Contains(replaceBody, "encrypted") {
		t.Fatalf("token replacement leaked secret: %s", replaceBody)
	}

	err = controlDB.QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'connection_credential_replaced' AND target_id = 'conn-dc-2'`).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("expected audit event connection_credential_replaced, count=%d", count)
	}

	// Verify ciphertext in DB can be decrypted to new token
	var encBlob, nonceBlob []byte
	err = controlDB.QueryRow(`SELECT encrypted_credential, credential_nonce FROM transport_connections WHERE id = 'conn-dc-2'`).Scan(&encBlob, &nonceBlob)
	if err != nil {
		t.Fatalf("failed to query credentials: %v", err)
	}
	decrypted, err := srv.credentialCipher.Decrypt(encBlob, nonceBlob)
	if err != nil || string(decrypted) != "replacement-secret-token" {
		t.Fatalf("decryption failed or mismatched: %v, got %q", err, string(decrypted))
	}

	// GET connection must never expose secrets
	getReq := httptest.NewRequest(http.MethodGet, "/api/connections/conn-dc-2", nil)
	getReq.Header.Set("Authorization", "Bearer "+adminToken)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, getReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("get connection got %d", rec.Code)
	}
	for _, forbidden := range []string{"replacement-secret-token", "encrypted", "nonce"} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Fatalf("GET /api/connections/{id} exposed secret: %s", rec.Body.String())
		}
	}

	// Delete connection
	delReq := httptest.NewRequest(http.MethodDelete, "/api/connections/conn-dc-2", nil)
	delReq.Header.Set("Authorization", "Bearer "+adminToken)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, delReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete connection got %d: %s", rec.Code, rec.Body.String())
	}

	err = controlDB.QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'connection_deleted' AND target_id = 'conn-dc-2'`).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("expected audit event connection_deleted, count=%d", count)
	}
}

func TestConnections_CredentialReplacementRollsBackWhenRuntimeSwapFails(t *testing.T) {
	srv, _, controlDB, adminToken, _ := setupConnectionsTestEnv(t)
	srv.onConfigChange = func(context.Context) error {
		return errors.New("replacement adapter unavailable")
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/connections/conn-dc-1", strings.NewReader(`{"token":"replacement-secret-token"}`))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("replacement failure status = %d, want 503: %s", rec.Code, rec.Body.String())
	}

	var encBlob, nonceBlob []byte
	if err := controlDB.QueryRow(`SELECT encrypted_credential, credential_nonce FROM transport_connections WHERE id = 'conn-dc-1'`).Scan(&encBlob, &nonceBlob); err != nil {
		t.Fatal(err)
	}
	decrypted, err := srv.credentialCipher.Decrypt(encBlob, nonceBlob)
	if err != nil {
		t.Fatal(err)
	}
	if string(decrypted) != "discord-token-123" {
		t.Fatalf("failed replacement changed stored credential to %q", decrypted)
	}
}

func TestConnections_EndpointReassignmentAndDeletionRules(t *testing.T) {
	srv, syncDB, controlDB, adminToken, _ := setupConnectionsTestEnv(t)

	// Create a second Discord connection
	_, err := controlDB.Exec(`INSERT INTO transport_connections (id, transport, label, enabled, encrypted_credential, credential_nonce, created_at, updated_at) VALUES
		('conn-dc-2', 'discord', 'dc-secondary', 1, X'0102', X'0304', 1000, 1000),
		('conn-dc-disabled', 'discord', 'dc-disabled', 0, X'0102', X'0304', 1000, 1000)`)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := syncDB.Exec(`INSERT INTO sync_sets (id) VALUES ('sync-test')`); err != nil {
		t.Fatal(err)
	}

	// 1. Endpoint create with mismatched transport is rejected
	badTransportReq := httptest.NewRequest(http.MethodPost, "/api/endpoints", strings.NewReader(
		`{"alias":"ep-fail","transport":"whatsapp","connectionId":"conn-dc-1","remoteId":"120363012345678901@g.us","syncSetId":"sync-test"}`))
	badTransportReq.Header.Set("Authorization", "Bearer "+adminToken)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, badTransportReq)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for transport mismatch, got %d: %s", rec.Code, rec.Body.String())
	}

	// 2. Endpoint create under disabled connection is rejected
	disabledReq := httptest.NewRequest(http.MethodPost, "/api/endpoints", strings.NewReader(
		`{"alias":"ep-fail-disabled","transport":"discord","connectionId":"conn-dc-disabled","remoteId":"111111111111111111","syncSetId":"sync-test"}`))
	disabledReq.Header.Set("Authorization", "Bearer "+adminToken)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, disabledReq)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for disabled connection, got %d: %s", rec.Code, rec.Body.String())
	}

	// 3. Endpoint create with valid connection succeeds
	validReq := httptest.NewRequest(http.MethodPost, "/api/endpoints", strings.NewReader(
		`{"alias":"ep-dc-main","transport":"discord","connectionId":"conn-dc-1","remoteId":"111111111111111111","syncSetId":"sync-test"}`))
	validReq.Header.Set("Authorization", "Bearer "+adminToken)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, validReq)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create endpoint got %d: %s", rec.Code, rec.Body.String())
	}

	// Insert mock message copy and scope linked to alias
	_, err = syncDB.Exec(`INSERT INTO canonical_messages (canonical_id, created_at) VALUES ('msg-1', 1000)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = syncDB.Exec(`INSERT INTO message_copies (canonical_id, endpoint_id, remote_message_id, created_at) VALUES ('msg-1', 'ep-dc-main', 'rem-1', 1000)`)
	if err != nil {
		t.Fatal(err)
	}

	// 4. Deleting connection referenced by endpoints fails with 409 Conflict
	delConnReq := httptest.NewRequest(http.MethodDelete, "/api/connections/conn-dc-1", nil)
	delConnReq.Header.Set("Authorization", "Bearer "+adminToken)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, delConnReq)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict deleting referenced connection, got %d: %s", rec.Code, rec.Body.String())
	}

	// 5. Reassign endpoint to conn-dc-2
	reassignReq := httptest.NewRequest(http.MethodPut, "/api/endpoints/ep-dc-main", strings.NewReader(
		`{"transport":"discord","connectionId":"conn-dc-2","remoteId":"111111111111111111","syncSetId":"sync-test"}`))
	reassignReq.Header.Set("Authorization", "Bearer "+adminToken)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, reassignReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("reassign endpoint failed %d: %s", rec.Code, rec.Body.String())
	}

	// Verify endpoint's connection_id was updated to conn-dc-2, while alias and copies preserved
	var newConnID string
	err = syncDB.QueryRow(`SELECT connection_id FROM endpoints WHERE alias = 'ep-dc-main'`).Scan(&newConnID)
	if err != nil || newConnID != "conn-dc-2" {
		t.Fatalf("expected new connection_id conn-dc-2, got %q (err=%v)", newConnID, err)
	}

	var copyCount int
	err = syncDB.QueryRow(`SELECT count(*) FROM message_copies WHERE endpoint_id = 'ep-dc-main'`).Scan(&copyCount)
	if err != nil || copyCount != 1 {
		t.Fatalf("expected message copy preserved, got count=%d", copyCount)
	}

	// 6. Deleting conn-dc-1 now succeeds since it has no references
	delConnReq = httptest.NewRequest(http.MethodDelete, "/api/connections/conn-dc-1", nil)
	delConnReq.Header.Set("Authorization", "Bearer "+adminToken)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, delConnReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete unreferenced connection failed %d: %s", rec.Code, rec.Body.String())
	}
}

func TestConnections_WhatsAppPairConflictAndActions(t *testing.T) {
	srv, _, controlDB, adminToken, _ := setupConnectionsTestEnv(t)

	mock := &mockConnectionService{
		pairErr: errors.New("WhatsApp pairing already in progress on another connection"),
	}
	srv.connections = mock

	// Pair when busy returns 409 Conflict
	pairReq := httptest.NewRequest(http.MethodPost, "/api/connections/conn-wa-1/pair", nil)
	pairReq.Header.Set("Authorization", "Bearer "+adminToken)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, pairReq)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for pairing busy, got %d: %s", rec.Code, rec.Body.String())
	}

	// Pair success
	mock.pairErr = nil
	mock.pairResp = WhatsAppPairResponse{
		Status:         "pairing",
		QRCode:         "2@fake-qr",
		TimeoutSeconds: 20,
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, pairReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for pairing success, got %d: %s", rec.Code, rec.Body.String())
	}

	var count int
	err := controlDB.QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'whatsapp_pair_started' AND target_id = 'conn-wa-1'`).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("expected audit event whatsapp_pair_started, count=%d", count)
	}

	// Cancel pair
	cancelReq := httptest.NewRequest(http.MethodDelete, "/api/connections/conn-wa-1/pair", nil)
	cancelReq.Header.Set("Authorization", "Bearer "+adminToken)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, cancelReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel pair failed %d: %s", rec.Code, rec.Body.String())
	}

	err = controlDB.QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'whatsapp_pair_cancelled' AND target_id = 'conn-wa-1'`).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("expected audit event whatsapp_pair_cancelled, count=%d", count)
	}

	// Logout
	logoutReq := httptest.NewRequest(http.MethodPost, "/api/connections/conn-wa-1/logout", nil)
	logoutReq.Header.Set("Authorization", "Bearer "+adminToken)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, logoutReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout failed %d: %s", rec.Code, rec.Body.String())
	}

	err = controlDB.QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'whatsapp_logged_out' AND target_id = 'conn-wa-1'`).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("expected audit event whatsapp_logged_out, count=%d", count)
	}
}

func TestConnections_DiscoveryScopedByConnection(t *testing.T) {
	srv, _, _, adminToken, _ := setupConnectionsTestEnv(t)

	mock := &mockConnectionService{
		discoveryResp: []discord.DiscoveredChannel{
			{GuildID: "111", GuildName: "Discord Guild", ChannelID: "222", ChannelName: "chat"},
		},
	}
	srv.connections = mock

	req := httptest.NewRequest(http.MethodGet, "/api/connections/conn-dc-1/discovery", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("discovery got %d: %s", rec.Code, rec.Body.String())
	}
	var channels []discord.DiscoveredChannel
	if err := json.Unmarshal(rec.Body.Bytes(), &channels); err != nil {
		t.Fatalf("failed to decode discovery: %v", err)
	}
	if len(channels) != 1 || channels[0].ChannelName != "chat" {
		t.Fatalf("unexpected discovery response: %+v", channels)
	}
}

func TestConnections_TelegramIntegrationModeIsImmutableAndDeleteRemovesState(t *testing.T) {
	srv, _, db, adminToken, _ := setupConnectionsTestEnv(t)
	create := authenticatedConnectionRequest(t, srv, adminToken, http.MethodPost, "/api/connections", `{"id":"conn-mt-hard","transport":"telegram","integrationMode":"mtproto","label":"Phone Telegram"}`)
	if create.Code != http.StatusCreated {
		t.Fatalf("create MTProto: %d %s", create.Code, create.Body.String())
	}
	patch := authenticatedConnectionRequest(t, srv, adminToken, http.MethodPatch, "/api/connections/conn-mt-hard", `{"integrationMode":"bot"}`)
	if patch.Code != http.StatusConflict {
		t.Fatalf("mode change status=%d body=%s", patch.Code, patch.Body.String())
	}
	var mode string
	if err := db.QueryRow(`SELECT integration_mode FROM transport_connections WHERE id='conn-mt-hard'`).Scan(&mode); err != nil || mode != "mtproto" {
		t.Fatalf("mode=%q err=%v", mode, err)
	}
	del := authenticatedConnectionRequest(t, srv, adminToken, http.MethodDelete, "/api/connections/conn-mt-hard", "")
	if del.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", del.Code, del.Body.String())
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM transport_connections WHERE id='conn-mt-hard'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted MTProto state remains count=%d err=%v", count, err)
	}
}

func TestConnections_DiscordExplicitWebhookCredential(t *testing.T) {
	srv, _, controlDB, adminToken, _ := setupConnectionsTestEnv(t)
	body := `{"id":"conn-dc-hook","transport":"discord","integrationMode":"webhook","label":"Webhook Discord","token":"bot-secret","webhookUrl":"https://discord.com/api/webhooks/123456789/webhook-secret","channelId":"987654321012345678"}`
	req := httptest.NewRequest(http.MethodPost, "/api/connections", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create explicit webhook connection got %d: %s", rec.Code, rec.Body.String())
	}
	response := rec.Body.String()
	for _, forbidden := range []string{"bot-secret", "webhook-secret", "webhookUrl", "channelId"} {
		if strings.Contains(response, forbidden) {
			t.Fatalf("create response leaked Discord credential field %q: %s", forbidden, response)
		}
	}
	if !strings.Contains(response, `"integrationMode":"webhook"`) {
		t.Fatalf("response missing explicit webhook integration mode: %s", response)
	}

	var encrypted, nonce []byte
	if err := controlDB.QueryRow(`SELECT encrypted_credential, credential_nonce FROM transport_connections WHERE id = 'conn-dc-hook'`).Scan(&encrypted, &nonce); err != nil {
		t.Fatal(err)
	}
	raw, err := srv.credentialCipher.Decrypt(encrypted, nonce)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := discord.DecodeStoredCredential("webhook", raw)
	if err != nil {
		t.Fatal(err)
	}
	if credential.BotToken != "bot-secret" || credential.ChannelID != "987654321012345678" || !strings.Contains(credential.WebhookURL, "webhook-secret") {
		t.Fatalf("unexpected stored Discord webhook credential: %#v", credential)
	}

	patch := httptest.NewRequest(http.MethodPatch, "/api/connections/conn-dc-hook", strings.NewReader(`{"token":"replacement-bot-secret"}`))
	patch.Header.Set("Authorization", "Bearer "+adminToken)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, patch)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace bot token got %d: %s", rec.Code, rec.Body.String())
	}
	if err := controlDB.QueryRow(`SELECT encrypted_credential, credential_nonce FROM transport_connections WHERE id = 'conn-dc-hook'`).Scan(&encrypted, &nonce); err != nil {
		t.Fatal(err)
	}
	raw, err = srv.credentialCipher.Decrypt(encrypted, nonce)
	if err != nil {
		t.Fatal(err)
	}
	credential, err = discord.DecodeStoredCredential("webhook", raw)
	if err != nil {
		t.Fatal(err)
	}
	if credential.BotToken != "replacement-bot-secret" || credential.ChannelID != "987654321012345678" || !strings.Contains(credential.WebhookURL, "webhook-secret") {
		t.Fatalf("bot token replacement did not preserve explicit webhook binding: %#v", credential)
	}
}

func TestConnections_DiscordExplicitWebhookRequiresURLAndChannel(t *testing.T) {
	srv, _, _, adminToken, _ := setupConnectionsTestEnv(t)
	for _, body := range []string{
		`{"transport":"discord","integrationMode":"webhook","label":"bad","token":"bot-secret","channelId":"123456789"}`,
		`{"transport":"discord","integrationMode":"webhook","label":"bad","token":"bot-secret","webhookUrl":"https://discord.com/api/webhooks/123/token"}`,
		`{"transport":"discord","integrationMode":"webhook","label":"bad","token":"bot-secret","webhookUrl":"https://example.com/api/webhooks/123/token","channelId":"123456789"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/connections", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+adminToken)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid explicit webhook create got %d, want 400: %s", rec.Code, rec.Body.String())
		}
	}
}
