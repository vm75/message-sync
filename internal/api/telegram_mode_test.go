package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vm75/message-sync/internal/controlstore"
	telegram "github.com/vm75/message-sync/internal/transport/telegram"
)

func authenticatedConnectionRequest(t *testing.T, srv *Server, token, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestTelegramConnectionIntegrationModesAndCapabilities(t *testing.T) {
	srv, _, controlDB, adminToken, _ := setupConnectionsTestEnv(t)

	legacy := authenticatedConnectionRequest(t, srv, adminToken, http.MethodGet, "/api/connections/conn-tg-1", "")
	if legacy.Code != http.StatusOK {
		t.Fatalf("get legacy Telegram connection: %d %s", legacy.Code, legacy.Body.String())
	}
	var legacyDTO ConnectionDTO
	if err := json.Unmarshal(legacy.Body.Bytes(), &legacyDTO); err != nil {
		t.Fatal(err)
	}
	if legacyDTO.IntegrationMode != controlstore.TelegramIntegrationModeBot {
		t.Fatalf("legacy mode=%q want bot", legacyDTO.IntegrationMode)
	}
	if legacyDTO.Capabilities == nil || legacyDTO.Capabilities.ChatDiscovery != telegram.DiscoveryObserved || legacyDTO.Capabilities.HistoryRecovery {
		t.Fatalf("legacy capabilities=%+v", legacyDTO.Capabilities)
	}

	mt := authenticatedConnectionRequest(t, srv, adminToken, http.MethodPost, "/api/connections", `{"id":"conn-tg-mt","transport":"telegram","integrationMode":"mtproto","label":"phone account"}`)
	if mt.Code != http.StatusCreated {
		t.Fatalf("create MTProto connection: %d %s", mt.Code, mt.Body.String())
	}
	var mtDTO ConnectionDTO
	if err := json.Unmarshal(mt.Body.Bytes(), &mtDTO); err != nil {
		t.Fatal(err)
	}
	if mtDTO.IntegrationMode != controlstore.TelegramIntegrationModeMTProto || mtDTO.Capabilities == nil || mtDTO.Capabilities.ChatDiscovery != telegram.DiscoveryFull || !mtDTO.Capabilities.HistoryRecovery || mtDTO.Capabilities.PrivacyModeStatus {
		t.Fatalf("MTProto DTO=%+v", mtDTO)
	}
	var storedMode string
	var ciphertext, nonce []byte
	if err := controlDB.QueryRow(`SELECT integration_mode, encrypted_credential, credential_nonce FROM transport_connections WHERE id='conn-tg-mt'`).Scan(&storedMode, &ciphertext, &nonce); err != nil {
		t.Fatal(err)
	}
	if storedMode != controlstore.TelegramIntegrationModeMTProto || len(ciphertext) == 0 || len(nonce) == 0 {
		t.Fatalf("stored MTProto foundation state mode=%q ciphertext=%d nonce=%d", storedMode, len(ciphertext), len(nonce))
	}

	withToken := authenticatedConnectionRequest(t, srv, adminToken, http.MethodPost, "/api/connections", `{"id":"conn-tg-bad","transport":"telegram","integrationMode":"mtproto","label":"bad","token":"bot-secret"}`)
	if withToken.Code != http.StatusBadRequest {
		t.Fatalf("MTProto connection with bot token status=%d body=%s", withToken.Code, withToken.Body.String())
	}
	invalid := authenticatedConnectionRequest(t, srv, adminToken, http.MethodPost, "/api/connections", `{"id":"conn-tg-invalid","transport":"telegram","integrationMode":"hybrid","label":"bad"}`)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid integration mode status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	nonTelegram := authenticatedConnectionRequest(t, srv, adminToken, http.MethodPost, "/api/connections", `{"id":"conn-dc-mode","transport":"discord","integrationMode":"bot","label":"bad","token":"x"}`)
	if nonTelegram.Code != http.StatusBadRequest {
		t.Fatalf("non-Telegram integration mode status=%d body=%s", nonTelegram.Code, nonTelegram.Body.String())
	}
}
