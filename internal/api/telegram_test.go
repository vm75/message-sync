package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	telegram "github.com/vm75/message-sync/internal/transport/telegram"
)

type fakeTelegramAdminService struct {
	status      telegram.AdminStatus
	chats       []telegram.DiscoveredChat
	discoverErr error
}

func (f *fakeTelegramAdminService) AdminStatus(context.Context) telegram.AdminStatus {
	return f.status
}

func (f *fakeTelegramAdminService) DiscoverChats(context.Context) ([]telegram.DiscoveredChat, error) {
	return f.chats, f.discoverErr
}

func authenticatedTelegramRequest(t *testing.T, srv *Server, method, path string) *http.Request {
	t.Helper()
	token, err := srv.sessions.CreateToken()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

func TestTelegramAdminEndpointsRequireAuthentication(t *testing.T) {
	srv := NewServer(Options{
		Secret:      []byte("01234567890123456789012345678901"),
		Connections: testConnectionService{tg: &fakeTelegramAdminService{}},
	})
	for _, path := range []string{"/api/connections/conn-tg-1/status", "/api/connections/conn-tg-1/discovery"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("GET %s status=%d, want 401", path, rec.Code)
		}
	}
}

func TestTelegramStatusMissingTokenIsSafe(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_BOT_TOKEN_FILE", "")
	srv := NewServer(Options{Secret: []byte("01234567890123456789012345678901")})

	statusRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(statusRec, authenticatedTelegramRequest(t, srv, http.MethodGet, "/api/connections/conn-tg-1/status"))
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200: %s", statusRec.Code, statusRec.Body.String())
	}
	var status map[string]any
	if err := json.NewDecoder(statusRec.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status["status"] != "stopped" && status["status"] != "not_configured" {
		t.Fatalf("unexpected missing-token status: %+v", status)
	}

	chatsRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(chatsRec, authenticatedTelegramRequest(t, srv, http.MethodGet, "/api/connections/conn-tg-1/discovery"))
	if strings.Contains(chatsRec.Body.String(), "botToken") {
		t.Fatalf("missing-token response exposed credential field: %s", chatsRec.Body.String())
	}
}

func TestTelegramStatusAndDiscoveryExposeTransientSelectionMetadata(t *testing.T) {
	privacyModeEnabled := false
	service := &fakeTelegramAdminService{
		status: telegram.AdminStatus{
			TokenConfigured:    true,
			Running:            true,
			Status:             "running",
			Endpoints:          []telegram.EndpointReadiness{{Alias: "tg-ready", Status: "ready"}},
			PrivacyModeKnown:   true,
			PrivacyModeEnabled: &privacyModeEnabled,
			VisibilityGuidance: telegram.VisibilityGuidance,
		},
		chats: []telegram.DiscoveredChat{{
			ChatID:   "-1001234567890",
			Title:    "Transient Team",
			Username: "transient_team",
			Type:     "supergroup",
		}},
	}
	srv := NewServer(Options{
		Secret:      []byte("01234567890123456789012345678901"),
		Connections: testConnectionService{tg: service},
	})

	statusRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(statusRec, authenticatedTelegramRequest(t, srv, http.MethodGet, "/api/connections/conn-tg-1/status"))
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status=%d: %s", statusRec.Code, statusRec.Body.String())
	}
	for _, forbidden := range []string{"botToken", "tokenValue", "rawUpdate"} {
		if strings.Contains(statusRec.Body.String(), forbidden) {
			t.Fatalf("status exposed forbidden field %q: %s", forbidden, statusRec.Body.String())
		}
	}

	if !strings.Contains(statusRec.Body.String(), `"privacyModeEnabled":false`) {
		t.Fatalf("status did not expose safe derived privacy-mode state: %s", statusRec.Body.String())
	}

	chatsRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(chatsRec, authenticatedTelegramRequest(t, srv, http.MethodGet, "/api/connections/conn-tg-1/discovery"))
	if chatsRec.Code != http.StatusOK {
		t.Fatalf("chats=%d: %s", chatsRec.Code, chatsRec.Body.String())
	}
	var chats []telegram.DiscoveredChat
	if err := json.NewDecoder(chatsRec.Body).Decode(&chats); err != nil {
		t.Fatal(err)
	}
	if len(chats) != 1 || chats[0].Title != "Transient Team" || chats[0].Username != "transient_team" {
		t.Fatalf("unexpected transient chats: %+v", chats)
	}
}

func TestTelegramDiscoveryEndpointCreationPersistsOnlyOpaqueChatID(t *testing.T) {
	db := setupTestDB(t)
	if _, err := db.Exec(`INSERT INTO sync_sets (id) VALUES ('mesh')`); err != nil {
		t.Fatal(err)
	}
	service := &fakeTelegramAdminService{
		status: telegram.AdminStatus{
			TokenConfigured:    true,
			Running:            true,
			Status:             "running",
			Endpoints:          []telegram.EndpointReadiness{},
			VisibilityGuidance: telegram.VisibilityGuidance,
		},
		chats: []telegram.DiscoveredChat{{
			ChatID:   "-1001234567890",
			Title:    "Private Human-Readable Team Name",
			Username: "private_human_name",
			Type:     "supergroup",
		}},
	}
	configChanges := 0
	srv := NewServer(Options{
		DB:          db,
		Secret:      []byte("01234567890123456789012345678901"),
		Connections: testConnectionService{tg: service},
		OnConfigChange: func(context.Context) error {
			configChanges++
			return nil
		},
	})
	token, err := srv.sessions.CreateToken()
	if err != nil {
		t.Fatal(err)
	}

	discoveryReq := httptest.NewRequest(http.MethodGet, "/api/connections/conn-tg-1/discovery", nil)
	discoveryReq.Header.Set("Authorization", "Bearer "+token)
	discoveryRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(discoveryRec, discoveryReq)
	if discoveryRec.Code != http.StatusOK {
		t.Fatalf("discovery status=%d: %s", discoveryRec.Code, discoveryRec.Body.String())
	}

	createReq := httptest.NewRequest(http.MethodPost, "/api/endpoints", strings.NewReader(
		`{"alias":"tg_route_01","transport":"telegram","connectionId":"conn-tg-1","remoteId":"-1001234567890","syncSetId":"mesh"}`,
	))
	createReq.Header.Set("Authorization", "Bearer "+token)
	createRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status=%d: %s", createRec.Code, createRec.Body.String())
	}
	if configChanges != 1 {
		t.Fatalf("config reload count=%d, want 1", configChanges)
	}

	var alias, transportName, remoteID string
	var syncSetID *string
	if err := db.QueryRow(`SELECT alias, transport, remote_id, sync_set_id FROM endpoints WHERE alias = 'tg_route_01'`).Scan(&alias, &transportName, &remoteID, &syncSetID); err != nil {
		t.Fatal(err)
	}
	if alias != "tg_route_01" || transportName != "telegram" || remoteID != "-1001234567890" || syncSetID == nil || *syncSetID != "mesh" {
		t.Fatalf("unexpected persisted endpoint: alias=%q transport=%q remote=%q sync=%v", alias, transportName, remoteID, syncSetID)
	}

	var endpointSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='endpoints'`).Scan(&endpointSQL); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Private Human-Readable Team Name", "private_human_name", "chat_title", "chat_username"} {
		if strings.Contains(endpointSQL, forbidden) {
			t.Fatalf("endpoint persistence schema contains transient Telegram metadata %q: %s", forbidden, endpointSQL)
		}
	}
}

func TestTelegramDiscoveryErrorsAreSafeLogged(t *testing.T) {
	var logs bytes.Buffer
	service := &fakeTelegramAdminService{
		status:      telegram.AdminStatus{TokenConfigured: true, Running: true, Status: "running"},
		discoverErr: errors.New("chat -100999 private-title secret-token"),
	}
	srv := NewServer(Options{
		Secret:      []byte("01234567890123456789012345678901"),
		Connections: testConnectionService{tg: service},
		Logger:      slog.New(slog.NewTextHandler(&logs, nil)),
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, authenticatedTelegramRequest(t, srv, http.MethodGet, "/api/connections/conn-tg-1/discovery"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500", rec.Code)
	}
	for _, forbidden := range []string{"-100999", "private-title", "secret-token"} {
		if strings.Contains(logs.String(), forbidden) {
			t.Fatalf("logs exposed Telegram discovery data %q: %s", forbidden, logs.String())
		}
	}
}
