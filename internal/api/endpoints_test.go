package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEndpointsAuthRequired(t *testing.T) {
	db := setupTestDB(t)
	srv := setupTestServer(t, db)

	cases := []struct {
		method string
		url    string
		body   string
	}{
		{http.MethodGet, "/api/endpoints", ""},
		{http.MethodGet, "/api/endpoints/ops", ""},
		{http.MethodPost, "/api/endpoints", `{"alias":"ops","transport":"discord","remoteId":"123456789012345678"}`},
		{http.MethodPut, "/api/endpoints/ops", `{"transport":"discord","remoteId":"123456789012345678"}`},
		{http.MethodDelete, "/api/endpoints/ops", ""},
	}

	for _, tc := range cases {
		var req *http.Request
		if tc.body == "" {
			req = httptest.NewRequest(tc.method, tc.url, nil)
		} else {
			req = httptest.NewRequest(tc.method, tc.url, strings.NewReader(tc.body))
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s status = %d, want 401", tc.method, tc.url, rec.Code)
		}
	}
}

func TestEndpointsCRUDMixedTransportsAndReload(t *testing.T) {
	db := setupTestDB(t)
	configChanges := 0
	srv := NewServer(Options{
		DB:     db,
		Secret: []byte("01234567890123456789012345678901"),
		OnConfigChange: func(context.Context) error {
			configChanges++
			return nil
		},
	})
	token, err := srv.sessions.CreateToken()
	if err != nil {
		t.Fatal(err)
	}
	authHeader := "Bearer " + token

	if _, err := db.Exec(`INSERT INTO sync_sets (id) VALUES ('mesh')`); err != nil {
		t.Fatal(err)
	}

	invalid := []string{
		`{"alias":"","transport":"discord","remoteId":"123456789012345678"}`,
		`{"alias":"d1","transport":"unknown","remoteId":"123456789012345678"}`,
		`{"alias":"d1","transport":"discord","remoteId":"not-a-channel"}`,
		`{"alias":"w1","transport":"whatsapp","remoteId":"not-a-jid"}`,
		`{"alias":"d1","transport":"discord","remoteId":"123456789012345678","syncSetId":"missing"}`,
		`{"alias":"d1","transport":"discord","remoteId":"123456789012345678","botToken":"secret"}`,
	}
	for _, body := range invalid {
		req := httptest.NewRequest(http.MethodPost, "/api/endpoints", strings.NewReader(body))
		req.Header.Set("Authorization", authHeader)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST invalid endpoint status = %d, want 400; body=%s response=%s", rec.Code, body, rec.Body.String())
		}
	}
	if configChanges != 0 {
		t.Fatalf("failed mutations triggered %d config reloads, want 0", configChanges)
	}

	create := func(body string) EndpointDTO {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/endpoints", strings.NewReader(body))
		req.Header.Set("Authorization", authHeader)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("POST /api/endpoints status = %d, want 201: %s", rec.Code, rec.Body.String())
		}
		var dto EndpointDTO
		if err := json.NewDecoder(rec.Body).Decode(&dto); err != nil {
			t.Fatal(err)
		}
		return dto
	}

	wa := create(`{"alias":"w1","transport":"whatsapp","remoteId":"12345@g.us","syncSetId":"mesh"}`)
	if wa.Alias != "w1" || wa.Transport != "whatsapp" || wa.RemoteID != "12345@g.us" || wa.SyncSetID == nil || *wa.SyncSetID != "mesh" {
		t.Fatalf("unexpected WhatsApp endpoint: %+v", wa)
	}
	discord := create(`{"alias":"d1","transport":"discord","remoteId":"123456789012345678","syncSetId":"mesh"}`)
	if discord.Alias != "d1" || discord.Transport != "discord" || discord.RemoteID != "123456789012345678" || discord.SyncSetID == nil || *discord.SyncSetID != "mesh" {
		t.Fatalf("unexpected Discord endpoint: %+v", discord)
	}
	if configChanges != 2 {
		t.Fatalf("config reloads after create = %d, want 2", configChanges)
	}

	reqDup := httptest.NewRequest(http.MethodPost, "/api/endpoints", strings.NewReader(
		`{"alias":"d2","transport":"discord","remoteId":"123456789012345678"}`,
	))
	reqDup.Header.Set("Authorization", authHeader)
	recDup := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recDup, reqDup)
	if recDup.Code != http.StatusBadRequest {
		t.Fatalf("duplicate Discord target status = %d, want 400", recDup.Code)
	}
	if strings.Contains(recDup.Body.String(), "123456789012345678") {
		t.Fatal("duplicate target error exposed remote id")
	}

	reqList := httptest.NewRequest(http.MethodGet, "/api/endpoints", nil)
	reqList.Header.Set("Authorization", authHeader)
	recList := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recList, reqList)
	if recList.Code != http.StatusOK {
		t.Fatalf("GET /api/endpoints status = %d, want 200", recList.Code)
	}
	var endpoints []EndpointDTO
	if err := json.NewDecoder(recList.Body).Decode(&endpoints); err != nil {
		t.Fatal(err)
	}
	if len(endpoints) != 2 || endpoints[0].Alias != "d1" || endpoints[1].Alias != "w1" {
		t.Fatalf("unexpected endpoint list: %+v", endpoints)
	}

	reqGet := httptest.NewRequest(http.MethodGet, "/api/endpoints/d1", nil)
	reqGet.Header.Set("Authorization", authHeader)
	recGet := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recGet, reqGet)
	if recGet.Code != http.StatusOK {
		t.Fatalf("GET endpoint status = %d, want 200", recGet.Code)
	}
	var got EndpointDTO
	if err := json.NewDecoder(recGet.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Transport != "discord" || got.RemoteID != "123456789012345678" {
		t.Fatalf("unexpected endpoint: %+v", got)
	}

	reqUpdate := httptest.NewRequest(http.MethodPut, "/api/endpoints/d1", strings.NewReader(
		`{"alias":"d1","transport":"discord","remoteId":"223456789012345678","syncSetId":"mesh"}`,
	))
	reqUpdate.Header.Set("Authorization", authHeader)
	recUpdate := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recUpdate, reqUpdate)
	if recUpdate.Code != http.StatusOK {
		t.Fatalf("PUT endpoint status = %d, want 200: %s", recUpdate.Code, recUpdate.Body.String())
	}
	if configChanges != 3 {
		t.Fatalf("config reloads after update = %d, want 3", configChanges)
	}

	reqDelete := httptest.NewRequest(http.MethodDelete, "/api/endpoints/w1", nil)
	reqDelete.Header.Set("Authorization", authHeader)
	recDelete := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recDelete, reqDelete)
	if recDelete.Code != http.StatusOK {
		t.Fatalf("DELETE endpoint status = %d, want 200", recDelete.Code)
	}
	if configChanges != 4 {
		t.Fatalf("config reloads after delete = %d, want 4", configChanges)
	}

	reqMissing := httptest.NewRequest(http.MethodGet, "/api/endpoints/w1", nil)
	reqMissing.Header.Set("Authorization", authHeader)
	recMissing := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recMissing, reqMissing)
	if recMissing.Code != http.StatusNotFound {
		t.Fatalf("GET deleted endpoint status = %d, want 404", recMissing.Code)
	}
}

func TestTelegramEndpointCRUD(t *testing.T) {
	db := setupTestDB(t)
	srv := setupTestServer(t, db)
	token, err := srv.sessions.CreateToken()
	if err != nil {
		t.Fatal(err)
	}
	authHeader := "Bearer " + token

	if _, err := db.Exec(`INSERT INTO sync_sets (id) VALUES ('mesh')`); err != nil {
		t.Fatal(err)
	}

	invalid := httptest.NewRequest(http.MethodPost, "/api/endpoints", strings.NewReader(
		`{"alias":"tg1","transport":"telegram","remoteId":"123456789","syncSetId":"mesh"}`,
	))
	invalid.Header.Set("Authorization", authHeader)
	invalidRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(invalidRec, invalid)
	if invalidRec.Code != http.StatusBadRequest {
		t.Fatalf("positive Telegram private chat id status = %d, want 400", invalidRec.Code)
	}

	create := httptest.NewRequest(http.MethodPost, "/api/endpoints", strings.NewReader(
		`{"alias":"tg1","transport":"telegram","remoteId":"-1001234567890","syncSetId":"mesh"}`,
	))
	create.Header.Set("Authorization", authHeader)
	createRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(createRec, create)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("POST Telegram endpoint status = %d, want 201: %s", createRec.Code, createRec.Body.String())
	}
	var created EndpointDTO
	if err := json.NewDecoder(createRec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Alias != "tg1" || created.Transport != "telegram" || created.RemoteID != "-1001234567890" || created.SyncSetID == nil || *created.SyncSetID != "mesh" {
		t.Fatalf("unexpected Telegram endpoint: %+v", created)
	}

	get := httptest.NewRequest(http.MethodGet, "/api/endpoints/tg1", nil)
	get.Header.Set("Authorization", authHeader)
	getRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(getRec, get)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET Telegram endpoint status = %d, want 200", getRec.Code)
	}
	var got EndpointDTO
	if err := json.NewDecoder(getRec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Transport != "telegram" || got.RemoteID != "-1001234567890" {
		t.Fatalf("unexpected Telegram endpoint read: %+v", got)
	}

	list := httptest.NewRequest(http.MethodGet, "/api/endpoints", nil)
	list.Header.Set("Authorization", authHeader)
	listRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(listRec, list)
	if listRec.Code != http.StatusOK {
		t.Fatalf("GET endpoint list status = %d, want 200", listRec.Code)
	}
	var endpoints []EndpointDTO
	if err := json.NewDecoder(listRec.Body).Decode(&endpoints); err != nil {
		t.Fatal(err)
	}
	if len(endpoints) != 1 || endpoints[0].Alias != "tg1" || endpoints[0].Transport != "telegram" {
		t.Fatalf("unexpected endpoint list: %+v", endpoints)
	}

	update := httptest.NewRequest(http.MethodPut, "/api/endpoints/tg1", strings.NewReader(
		`{"alias":"tg1","transport":"telegram","remoteId":"-1009876543210","syncSetId":"mesh"}`,
	))
	update.Header.Set("Authorization", authHeader)
	updateRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(updateRec, update)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("PUT Telegram endpoint status = %d, want 200: %s", updateRec.Code, updateRec.Body.String())
	}
	var updated EndpointDTO
	if err := json.NewDecoder(updateRec.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.RemoteID != "-1009876543210" || updated.Transport != "telegram" {
		t.Fatalf("unexpected updated Telegram endpoint: %+v", updated)
	}

	remove := httptest.NewRequest(http.MethodDelete, "/api/endpoints/tg1", nil)
	remove.Header.Set("Authorization", authHeader)
	removeRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(removeRec, remove)
	if removeRec.Code != http.StatusOK {
		t.Fatalf("DELETE Telegram endpoint status = %d, want 200", removeRec.Code)
	}

	missing := httptest.NewRequest(http.MethodGet, "/api/endpoints/tg1", nil)
	missing.Header.Set("Authorization", authHeader)
	missingRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(missingRec, missing)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("GET deleted Telegram endpoint status = %d, want 404", missingRec.Code)
	}
}

func TestEndpointErrorsDoNotLogRemoteIDs(t *testing.T) {
	db := setupTestDB(t)
	var logs bytes.Buffer
	srv := NewServer(Options{
		DB:     db,
		Secret: []byte("01234567890123456789012345678901"),
		Logger: slog.New(slog.NewTextHandler(&logs, nil)),
	})
	token, err := srv.sessions.CreateToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	const remoteID = "123456789012345678"
	req := httptest.NewRequest(http.MethodPost, "/api/endpoints", strings.NewReader(
		`{"alias":"d1","transport":"discord","remoteId":"`+remoteID+`"}`,
	))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST with closed database status = %d, want 500", rec.Code)
	}
	if strings.Contains(logs.String(), remoteID) {
		t.Fatalf("logs exposed remote id: %s", logs.String())
	}
}

func TestEndpointsRenameAlias(t *testing.T) {
	db := setupTestDB(t)
	srv := setupTestServer(t, db)
	token, err := srv.sessions.CreateToken()
	if err != nil {
		t.Fatal(err)
	}
	authHeader := "Bearer " + token

	if _, err := db.Exec(`INSERT INTO sync_sets (id) VALUES ('set1')`); err != nil {
		t.Fatal(err)
	}

	// Create endpoint "old_alias"
	reqCreate := httptest.NewRequest(http.MethodPost, "/api/endpoints", strings.NewReader(
		`{"alias":"old_alias","transport":"whatsapp","remoteId":"11111@g.us","syncSetId":"set1"}`,
	))
	reqCreate.Header.Set("Authorization", authHeader)
	recCreate := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recCreate, reqCreate)
	if recCreate.Code != http.StatusCreated {
		t.Fatalf("POST /api/endpoints status = %d, want 201: %s", recCreate.Code, recCreate.Body.String())
	}

	// Insert message_copies referencing "old_alias"
	if _, err := db.Exec(`INSERT INTO canonical_messages (canonical_id, created_at) VALUES ('c1', 1000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO message_copies (canonical_id, endpoint_id, remote_message_id, created_at) VALUES ('c1', 'old_alias', 'rm1', 1000)`); err != nil {
		t.Fatal(err)
	}

	// Rename "old_alias" -> "new_alias"
	reqUpdate := httptest.NewRequest(http.MethodPut, "/api/endpoints/old_alias", strings.NewReader(
		`{"alias":"new_alias","transport":"whatsapp","remoteId":"11111@g.us","syncSetId":"set1"}`,
	))
	reqUpdate.Header.Set("Authorization", authHeader)
	recUpdate := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recUpdate, reqUpdate)
	if recUpdate.Code != http.StatusOK {
		t.Fatalf("PUT /api/endpoints/old_alias status = %d, want 200: %s", recUpdate.Code, recUpdate.Body.String())
	}

	// Verify old_alias is gone and new_alias exists
	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM endpoints WHERE alias = 'old_alias'`).Scan(&count)
	if count != 0 {
		t.Fatalf("old_alias still exists")
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM endpoints WHERE alias = 'new_alias'`).Scan(&count)
	if count != 1 {
		t.Fatalf("new_alias does not exist")
	}

	// Verify message_copies was updated to new_alias
	var copyEndpoint string
	_ = db.QueryRow(`SELECT endpoint_id FROM message_copies WHERE canonical_id = 'c1'`).Scan(&copyEndpoint)
	if copyEndpoint != "new_alias" {
		t.Fatalf("message_copies endpoint_id = %s, want new_alias", copyEndpoint)
	}
}
