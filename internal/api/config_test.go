package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/vm75/message-sync/internal/config"
)

func TestGlobalPrivateDMRelayToggle(t *testing.T) {
	db := setupTestDB(t)
	srv := setupTestServer(t, db)
	token, err := srv.sessions.CreateToken()
	if err != nil {
		t.Fatal(err)
	}
	unauthorized := httptest.NewRecorder()
	srv.Handler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized GET status = %d", unauthorized.Code)
	}
	get := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	get.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, get)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"privateDMRelayEnabled":false`) {
		t.Fatalf("initial global setting response = %d %s", response.Code, response.Body.String())
	}
	update := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewBufferString(`{"privateDMRelayEnabled":true}`))
	update.Header.Set("Authorization", "Bearer "+token)
	updated := httptest.NewRecorder()
	srv.Handler().ServeHTTP(updated, update)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"privateDMRelayEnabled":true`) {
		t.Fatalf("update response = %d %s", updated.Code, updated.Body.String())
	}
	var enabled bool
	if err := db.QueryRow(`SELECT private_dm_relay_enabled FROM global_config WHERE id=1`).Scan(&enabled); err != nil || !enabled {
		t.Fatalf("persisted global setting = %t, error = %v", enabled, err)
	}
	invalid := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewBufferString(`{"usernameMode":"hash"}`))
	invalid.Header.Set("Authorization", "Bearer "+token)
	invalidResponse := httptest.NewRecorder()
	srv.Handler().ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("legacy setting payload status = %d", invalidResponse.Code)
	}
}

func TestPrivateDMEnablementRequiresAdministrator(t *testing.T) {
	db := setupTestDB(t)
	srv := setupTestServer(t, db)
	admin, err := srv.sessions.CreateToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.controlDB.Exec(`INSERT INTO users(id,username,password_hash,role,active,created_at,updated_at) VALUES('operator','operator','hash','operator',1,1,1)`); err != nil {
		t.Fatal(err)
	}
	operator, err := srv.sessions.createSession(context.Background(), "operator")
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if rec := request(t, srv, method, "/api/config", map[string]bool{"privateDMRelayEnabled": true}, operator); rec.Code != http.StatusForbidden {
			t.Fatalf("operator global %s=%d", method, rec.Code)
		}
	}
	create := map[string]any{"id": "private-set", "endpoints": []string{}, "settings": map[string]bool{"privateDMRelayEnabled": true}}
	if rec := request(t, srv, http.MethodPost, "/api/sync-sets", create, operator); rec.Code != http.StatusForbidden {
		t.Fatalf("operator enabled new set: %d", rec.Code)
	}
	if rec := request(t, srv, http.MethodPost, "/api/sync-sets", create, admin); rec.Code != http.StatusCreated {
		t.Fatalf("administrator create=%d %s", rec.Code, rec.Body.String())
	}
	if rec := request(t, srv, http.MethodPut, "/api/sync-sets/private-set", map[string]any{"settings": map[string]bool{"privateDMRelayEnabled": false}}, operator); rec.Code != http.StatusForbidden {
		t.Fatalf("operator changed set gate=%d", rec.Code)
	}
	if rec := request(t, srv, http.MethodPut, "/api/sync-sets/private-set", map[string]any{"settings": map[string]int{"codeLength": 7}}, operator); rec.Code != http.StatusOK {
		t.Fatalf("unrelated operator settings behavior changed=%d", rec.Code)
	}
}

func TestSyncSetSettingsIsolationAndPartialUpdates(t *testing.T) {
	db := setupTestDB(t)
	srv := setupTestServer(t, db)
	token, err := srv.sessions.CreateToken()
	if err != nil {
		t.Fatal(err)
	}
	send := func(method, path, body string, status int) SyncSetDTO {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
		}
		var dto SyncSetDTO
		if status < 300 {
			if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
				t.Fatal(err)
			}
		}
		return dto
	}
	first := send("POST", "/api/sync-sets", `{"id":"work","anonymizeToDiscord":true}`, 201)
	if !reflect.DeepEqual(*first.Settings, config.DefaultSyncSetSettings()) {
		t.Fatalf("defaults: %+v", first.Settings)
	}
	family := send("POST", "/api/sync-sets", `{"id":"family","settings":{"codeLength":10,"storage":{"messageRetentionDays":50}}}`, 201)
	changed := send("PUT", "/api/sync-sets/work", `{"settings":{"codeLength":7,"membershipReviewEnabled":true,"showDiscriminator":true,"usernameMode":"hash","media":{"enabled":false},"childContextDisplayMode":"friendly","recovery":{"enabled":false},"storage":{"messageRetentionDays":12},"polls":{"aggregationTrigger":"/votes"},"whatsappCleanup":{"enabled":true,"retentionDays":8},"localPrefix":"!local"}}`, 200)
	if !changed.AnonymizeToDiscord || changed.Settings.CodeLength != 7 || changed.Settings.Media.Enabled || changed.Settings.Media.MaxSizeMB != 100 {
		t.Fatalf("partial merge lost fields: %+v", changed)
	}
	send("PUT", "/api/sync-sets/work", `{"settings":{"showDiscriminator":false}}`, 200)
	loaded := send("GET", "/api/sync-sets/family", "", 200)
	if !reflect.DeepEqual(family, loaded) {
		t.Fatalf("another set changed: %+v != %+v", family, loaded)
	}
	loaded = send("GET", "/api/sync-sets/work", "", 200)
	expected := *changed.Settings
	expected.ShowDiscriminator = false
	if !reflect.DeepEqual(expected, *loaded.Settings) {
		t.Fatalf("settings not preserved: %+v", loaded.Settings)
	}
	for _, body := range []string{`{"settings":{"codeLength":4}}`, `{"settings":{"codeLength":11}}`, `{"settings":{"codeLength":6.5}}`, `{"settings":null}`, `{"settings":{"unknown":true}}`, `{"settings":{"usernameMode":"invalid"}}`, `{"settings":{"media":{"maxSizeMB":0}}}`} {
		send("PUT", "/api/sync-sets/work", body, 400)
	}
	loaded = send("GET", "/api/sync-sets/work", "", 200)
	if !reflect.DeepEqual(expected, *loaded.Settings) {
		t.Fatal("invalid update mutated persisted settings")
	}
}

func TestSyncSetSaveReportsReloadFailure(t *testing.T) {
	db := setupTestDB(t)
	srv := NewServer(Options{DB: db, Secret: []byte("01234567890123456789012345678901"), OnConfigChange: func(context.Context) error { return errors.New("reload failed") }})
	token, err := srv.sessions.CreateToken()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/sync-sets", bytes.NewBufferString(`{"id":"work"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 500 {
		t.Fatalf("reload failure returned %d", rec.Code)
	}
	var result struct {
		Persisted bool   `json:"persisted"`
		SyncSetID string `json:"syncSetId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Persisted || result.SyncSetID != "work" {
		t.Fatalf("retry metadata missing: %+v", result)
	}
}
