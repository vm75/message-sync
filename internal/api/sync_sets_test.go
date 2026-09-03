package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSyncSetsAuthRequired(t *testing.T) {
	db := setupTestDB(t)
	srv := setupTestServer(t, db)

	endpoints := []struct {
		method string
		url    string
		body   string
	}{
		{http.MethodGet, "/api/sync-sets", ""},
		{http.MethodGet, "/api/sync-sets/mesh", ""},
		{http.MethodPost, "/api/sync-sets", `{"id":"mesh","endpoints":["g1","d1"]}`},
		{http.MethodPut, "/api/sync-sets/mesh", `{"endpoints":["g1","d1"]}`},
		{http.MethodDelete, "/api/sync-sets/mesh", ""},
	}

	for _, ep := range endpoints {
		var req *http.Request
		if ep.body != "" {
			req = httptest.NewRequest(ep.method, ep.url, bytes.NewReader([]byte(ep.body)))
		} else {
			req = httptest.NewRequest(ep.method, ep.url, nil)
		}
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s expected 401 Unauthorized, got %d", ep.method, ep.url, rec.Code)
		}
	}
}

func TestSyncSetsCRUDAndValidation(t *testing.T) {
	db := setupTestDB(t)
	configChanges := 0
	secret := []byte("01234567890123456789012345678901")
	srv := NewServer(Options{
		DB:     db,
		Secret: secret,
		OnConfigChange: func(ctx context.Context) error {
			configChanges++
			return nil
		},
	})

	token, err := srv.sessions.CreateToken()
	if err != nil {
		t.Fatalf("create token failed: %v", err)
	}
	authHeader := "Bearer " + token

	// Create mixed-transport endpoints: g1 and g3 are WhatsApp, d1 is Discord, t1 is Telegram.
	_, err = db.Exec(`
		INSERT INTO endpoints (alias, transport, connection_id, remote_id) VALUES
			('g1', 'whatsapp', 'conn-wa-1', '1@g.us'),
			('d1', 'discord', 'conn-dc-1', '123456789012345678'),
			('t1', 'telegram', 'conn-tg-1', '-1001234567890'),
			('g3', 'whatsapp', 'conn-wa-1', '3@g.us')
	`)
	if err != nil {
		t.Fatalf("insert test endpoints: %v", err)
	}

	// 1. Initial GET /api/sync-sets should be empty
	{
		req := httptest.NewRequest(http.MethodGet, "/api/sync-sets", nil)
		req.Header.Set("Authorization", authHeader)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/sync-sets status = %d, want 200", rec.Code)
		}
		var sets []SyncSetDTO
		if err := json.NewDecoder(rec.Body).Decode(&sets); err != nil {
			t.Fatalf("decode sync sets: %v", err)
		}
		if len(sets) != 0 {
			t.Fatalf("expected empty sync sets, got %d", len(sets))
		}
	}

	// 2. POST validation failures
	invalidCases := []struct {
		name string
		body string
	}{
		{"empty id", `{"id":"","endpoints":["g1","d1"]}`},
		{"invalid id characters", `{"id":"set 1!","endpoints":["g1","d1"]}`},
		{"unknown endpoint", `{"id":"s1","endpoints":["g1","unknown_endpoint"]}`},
		{"duplicate endpoint in request", `{"id":"s1","endpoints":["g1","g1"]}`},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/sync-sets", bytes.NewReader([]byte(tc.body)))
			req.Header.Set("Authorization", authHeader)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("POST expected 400 for %s, got %d", tc.name, rec.Code)
			}
		})
	}

	// 3. POST valid creation of set1 with WhatsApp + Discord + Telegram endpoints.
	{
		body := `{"id":"set1","endpoints":["g1","d1","t1"]}`
		req := httptest.NewRequest(http.MethodPost, "/api/sync-sets", bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", authHeader)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("POST /api/sync-sets status = %d, want 201 (body: %s)", rec.Code, rec.Body.String())
		}
		var created SyncSetDTO
		if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
			t.Fatalf("decode created sync set: %v", err)
		}
		if created.ID != "set1" || len(created.Endpoints) != 3 {
			t.Fatalf("created sync set mismatch: %+v", created)
		}
		if configChanges != 1 {
			t.Fatalf("expected 1 config change, got %d", configChanges)
		}
	}

	// 4. POST duplicate sync set ID
	{
		body := `{"id":"set1","endpoints":["g3"]}`
		req := httptest.NewRequest(http.MethodPost, "/api/sync-sets", bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", authHeader)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST duplicate set ID status = %d, want 400", rec.Code)
		}
	}

	// 5. POST overlapping endpoint assignment (g1 already belongs to set1)
	{
		body := `{"id":"set2","endpoints":["g1","g3"]}`
		req := httptest.NewRequest(http.MethodPost, "/api/sync-sets", bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", authHeader)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST overlapping endpoint status = %d, want 400", rec.Code)
		}
	}

	// 6. GET /api/sync-sets/{id}
	{
		req := httptest.NewRequest(http.MethodGet, "/api/sync-sets/set1", nil)
		req.Header.Set("Authorization", authHeader)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/sync-sets/set1 status = %d, want 200", rec.Code)
		}
		var s SyncSetDTO
		if err := json.NewDecoder(rec.Body).Decode(&s); err != nil {
			t.Fatalf("decode sync set: %v", err)
		}
		if s.ID != "set1" || len(s.Endpoints) != 3 {
			t.Fatalf("sync set mismatch: %+v", s)
		}

		// GET non-existent
		reqNotFound := httptest.NewRequest(http.MethodGet, "/api/sync-sets/nonexistent", nil)
		reqNotFound.Header.Set("Authorization", authHeader)
		recNotFound := httptest.NewRecorder()
		srv.Handler().ServeHTTP(recNotFound, reqNotFound)
		if recNotFound.Code != http.StatusNotFound {
			t.Fatalf("GET non-existent status = %d, want 404", recNotFound.Code)
		}
	}

	// 7. PUT /api/sync-sets/{id}
	{
		// Update set1 to keep Discord + Telegram and replace the WhatsApp endpoint.
		body := `{"endpoints":["d1","t1","g3"]}`
		req := httptest.NewRequest(http.MethodPut, "/api/sync-sets/set1", bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", authHeader)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT /api/sync-sets/set1 status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		var updated SyncSetDTO
		if err := json.NewDecoder(rec.Body).Decode(&updated); err != nil {
			t.Fatalf("decode updated sync set: %v", err)
		}
		if updated.ID != "set1" || len(updated.Endpoints) != 3 {
			t.Fatalf("updated sync set mismatch: %+v", updated)
		}

		// Verify g1 is now unassigned
		var g1Set *string
		err := db.QueryRow(`SELECT sync_set_id FROM endpoints WHERE alias = 'g1'`).Scan(&g1Set)
		if err != nil {
			t.Fatalf("query g1: %v", err)
		}
		if g1Set != nil {
			t.Fatalf("expected g1 sync_set_id to be NULL, got %v", *g1Set)
		}

		// PUT non-existent sync set
		reqNotFound := httptest.NewRequest(http.MethodPut, "/api/sync-sets/missing", bytes.NewReader([]byte(`{"endpoints":["g1"]}`)))
		reqNotFound.Header.Set("Authorization", authHeader)
		recNotFound := httptest.NewRecorder()
		srv.Handler().ServeHTTP(recNotFound, reqNotFound)
		if recNotFound.Code != http.StatusNotFound {
			t.Fatalf("PUT non-existent sync set status = %d, want 404", recNotFound.Code)
		}

		// PUT invalid endpoint
		reqInvalid := httptest.NewRequest(http.MethodPut, "/api/sync-sets/set1", bytes.NewReader([]byte(`{"endpoints":["unknown_endpoint"]}`)))
		reqInvalid.Header.Set("Authorization", authHeader)
		recInvalid := httptest.NewRecorder()
		srv.Handler().ServeHTTP(recInvalid, reqInvalid)
		if recInvalid.Code != http.StatusBadRequest {
			t.Fatalf("PUT invalid endpoint status = %d, want 400", recInvalid.Code)
		}
	}

	// 8. DELETE /api/sync-sets/{id}
	{
		req := httptest.NewRequest(http.MethodDelete, "/api/sync-sets/set1", nil)
		req.Header.Set("Authorization", authHeader)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("DELETE /api/sync-sets/set1 status = %d, want 200", rec.Code)
		}

		// Verify sync_set is deleted
		reqGet := httptest.NewRequest(http.MethodGet, "/api/sync-sets/set1", nil)
		reqGet.Header.Set("Authorization", authHeader)
		recGet := httptest.NewRecorder()
		srv.Handler().ServeHTTP(recGet, reqGet)
		if recGet.Code != http.StatusNotFound {
			t.Fatalf("GET deleted sync set status = %d, want 404", recGet.Code)
		}

		// Verify all mixed-transport endpoints are now unassigned.
		var d1Set, t1Set, g3Set *string
		_ = db.QueryRow(`SELECT sync_set_id FROM endpoints WHERE alias = 'd1'`).Scan(&d1Set)
		_ = db.QueryRow(`SELECT sync_set_id FROM endpoints WHERE alias = 't1'`).Scan(&t1Set)
		_ = db.QueryRow(`SELECT sync_set_id FROM endpoints WHERE alias = 'g3'`).Scan(&g3Set)
		if d1Set != nil || t1Set != nil || g3Set != nil {
			t.Fatalf("expected endpoints to be unassigned after sync set delete, got d1=%v t1=%v g3=%v", d1Set, t1Set, g3Set)
		}

		// DELETE non-existent
		reqNotFound := httptest.NewRequest(http.MethodDelete, "/api/sync-sets/set1", nil)
		reqNotFound.Header.Set("Authorization", authHeader)
		recNotFound := httptest.NewRecorder()
		srv.Handler().ServeHTTP(recNotFound, reqNotFound)
		if recNotFound.Code != http.StatusNotFound {
			t.Fatalf("DELETE non-existent sync set status = %d, want 404", recNotFound.Code)
		}
	}
}

func TestSyncSetRejectsLegacyGroupsField(t *testing.T) {
	db := setupTestDB(t)
	srv := setupTestServer(t, db)
	token, err := srv.sessions.CreateToken()
	if err != nil {
		t.Fatal(err)
	}

	for _, method := range []string{http.MethodPost, http.MethodPut} {
		url := "/api/sync-sets"
		if method == http.MethodPut {
			url = "/api/sync-sets/mesh"
		}
		req := httptest.NewRequest(method, url, bytes.NewReader([]byte(`{"id":"mesh","groups":["a","b"]}`)))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s legacy groups field status = %d, want 400", method, rec.Code)
		}
	}
}
