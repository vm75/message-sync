package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGroupsAuthRequired(t *testing.T) {
	db := setupTestDB(t)
	srv := setupTestServer(t, db)

	endpoints := []struct {
		method string
		url    string
		body   string
	}{
		{http.MethodGet, "/api/groups", ""},
		{http.MethodGet, "/api/groups/ops", ""},
		{http.MethodPost, "/api/groups", `{"alias":"ops","jid":"123@g.us"}`},
		{http.MethodPut, "/api/groups/ops", `{"jid":"123@g.us"}`},
		{http.MethodDelete, "/api/groups/ops", ""},
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

func TestGroupsCRUDAndValidation(t *testing.T) {
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

	// 1. Initial GET /api/groups should be empty
	{
		req := httptest.NewRequest(http.MethodGet, "/api/groups", nil)
		req.Header.Set("Authorization", authHeader)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/groups status = %d, want 200", rec.Code)
		}
		var groups []GroupDTO
		if err := json.NewDecoder(rec.Body).Decode(&groups); err != nil {
			t.Fatalf("decode groups: %v", err)
		}
		if len(groups) != 0 {
			t.Fatalf("expected empty groups, got %d", len(groups))
		}
	}

	// 2. POST /api/groups validation failures
	invalidCases := []struct {
		name string
		body string
	}{
		{"empty alias", `{"alias":"","jid":"123@g.us"}`},
		{"invalid alias characters", `{"alias":"ops@whatsapp","jid":"123@g.us"}`},
		{"alias too long", `{"alias":"abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz0123456789","jid":"123@g.us"}`},
		{"empty jid", `{"alias":"ops","jid":""}`},
		{"invalid jid format", `{"alias":"ops","jid":"12345"}`},
		{"non-existent syncSetId", `{"alias":"ops","jid":"123@g.us","syncSetId":"missing_set"}`},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/groups", bytes.NewReader([]byte(tc.body)))
			req.Header.Set("Authorization", authHeader)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("POST /api/groups expected 400 for %s, got %d (body: %s)", tc.name, rec.Code, rec.Body.String())
			}
		})
	}

	// 3. Create a sync set directly in DB for testing syncSetId assignment
	_, err = db.Exec(`INSERT INTO sync_sets (id) VALUES ('set1')`)
	if err != nil {
		t.Fatalf("insert sync set: %v", err)
	}

	// 4. POST /api/groups valid creation
	{
		body := `{"alias":"g1","jid":"12345@g.us","syncSetId":"set1"}`
		req := httptest.NewRequest(http.MethodPost, "/api/groups", bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", authHeader)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("POST /api/groups status = %d, want 201 (body: %s)", rec.Code, rec.Body.String())
		}
		var created GroupDTO
		if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
			t.Fatalf("decode created group: %v", err)
		}
		if created.Alias != "g1" || created.JID != "12345@g.us" || created.SyncSetID == nil || *created.SyncSetID != "set1" {
			t.Fatalf("created group mismatch: %+v", created)
		}
		if configChanges != 1 {
			t.Fatalf("expected 1 config change notification, got %d", configChanges)
		}
	}

	// 5. POST /api/groups duplicate alias
	{
		body := `{"alias":"g1","jid":"99999@g.us"}`
		req := httptest.NewRequest(http.MethodPost, "/api/groups", bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", authHeader)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST duplicate group status = %d, want 400", rec.Code)
		}
	}

	// 6. GET /api/groups/{alias}
	{
		req := httptest.NewRequest(http.MethodGet, "/api/groups/g1", nil)
		req.Header.Set("Authorization", authHeader)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/groups/g1 status = %d, want 200", rec.Code)
		}
		var g GroupDTO
		if err := json.NewDecoder(rec.Body).Decode(&g); err != nil {
			t.Fatalf("decode group: %v", err)
		}
		if g.Alias != "g1" || g.JID != "12345@g.us" {
			t.Fatalf("group mismatch: %+v", g)
		}

		// Non-existent group
		reqNotFound := httptest.NewRequest(http.MethodGet, "/api/groups/nonexistent", nil)
		reqNotFound.Header.Set("Authorization", authHeader)
		recNotFound := httptest.NewRecorder()
		srv.Handler().ServeHTTP(recNotFound, reqNotFound)
		if recNotFound.Code != http.StatusNotFound {
			t.Fatalf("GET non-existent group status = %d, want 404", recNotFound.Code)
		}
	}

	// 7. PUT /api/groups/{alias}
	{
		// Update with valid JID and unassign sync set
		body := `{"jid":"54321@g.us","syncSetId":null}`
		req := httptest.NewRequest(http.MethodPut, "/api/groups/g1", bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", authHeader)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT /api/groups/g1 status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		var updated GroupDTO
		if err := json.NewDecoder(rec.Body).Decode(&updated); err != nil {
			t.Fatalf("decode updated group: %v", err)
		}
		if updated.JID != "54321@g.us" || updated.SyncSetID != nil {
			t.Fatalf("updated group mismatch: %+v", updated)
		}

		// Update non-existent group
		reqNotFound := httptest.NewRequest(http.MethodPut, "/api/groups/g2", bytes.NewReader([]byte(`{"jid":"123@g.us"}`)))
		reqNotFound.Header.Set("Authorization", authHeader)
		recNotFound := httptest.NewRecorder()
		srv.Handler().ServeHTTP(recNotFound, reqNotFound)
		if recNotFound.Code != http.StatusNotFound {
			t.Fatalf("PUT non-existent group status = %d, want 404", recNotFound.Code)
		}

		// Update with invalid JID
		reqInvalid := httptest.NewRequest(http.MethodPut, "/api/groups/g1", bytes.NewReader([]byte(`{"jid":"invalid-jid"}`)))
		reqInvalid.Header.Set("Authorization", authHeader)
		recInvalid := httptest.NewRecorder()
		srv.Handler().ServeHTTP(recInvalid, reqInvalid)
		if recInvalid.Code != http.StatusBadRequest {
			t.Fatalf("PUT invalid jid status = %d, want 400", recInvalid.Code)
		}
	}

	// 8. DELETE /api/groups/{alias}
	{
		req := httptest.NewRequest(http.MethodDelete, "/api/groups/g1", nil)
		req.Header.Set("Authorization", authHeader)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("DELETE /api/groups/g1 status = %d, want 200", rec.Code)
		}

		// Verify it was deleted
		reqGet := httptest.NewRequest(http.MethodGet, "/api/groups/g1", nil)
		reqGet.Header.Set("Authorization", authHeader)
		recGet := httptest.NewRecorder()
		srv.Handler().ServeHTTP(recGet, reqGet)
		if recGet.Code != http.StatusNotFound {
			t.Fatalf("GET deleted group status = %d, want 404", recGet.Code)
		}

		// DELETE non-existent
		reqNotFound := httptest.NewRequest(http.MethodDelete, "/api/groups/g1", nil)
		reqNotFound.Header.Set("Authorization", authHeader)
		recNotFound := httptest.NewRecorder()
		srv.Handler().ServeHTTP(recNotFound, reqNotFound)
		if recNotFound.Code != http.StatusNotFound {
			t.Fatalf("DELETE non-existent group status = %d, want 404", recNotFound.Code)
		}
	}
}
