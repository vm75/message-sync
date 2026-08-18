package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vm75/message-sync/internal/config"
)

func TestConfigAuthRequired(t *testing.T) {
	db := setupTestDB(t)
	srv := setupTestServer(t, db)

	endpoints := []struct {
		method string
		url    string
		body   string
	}{
		{http.MethodGet, "/api/config", ""},
		{http.MethodPut, "/api/config", `{"usernameMode":"hash"}`},
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

func TestConfigGetAndUpdate(t *testing.T) {
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

	// 1. GET /api/config initial values
	{
		req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
		req.Header.Set("Authorization", authHeader)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/config status = %d, want 200", rec.Code)
		}
		var cfg GlobalConfigDTO
		if err := json.NewDecoder(rec.Body).Decode(&cfg); err != nil {
			t.Fatalf("decode config: %v", err)
		}
		if cfg.UsernameMode != config.UsernameModePushName {
			t.Errorf("default usernameMode = %q, want push_name", cfg.UsernameMode)
		}
		if cfg.Media.MaxSizeMB != 100 {
			t.Errorf("default media MaxSizeMB = %d, want 100", cfg.Media.MaxSizeMB)
		}
		if cfg.Storage.MessageRetentionDays != 90 {
			t.Errorf("default retention days = %d, want 90", cfg.Storage.MessageRetentionDays)
		}
	}

	// 2. PUT validation errors
	invalidCases := []struct {
		name string
		body string
	}{
		{"invalid username mode", `{"usernameMode":"invalid"}`},
		{"negative media maxSizeMB", `{"media":{"enabled":true,"maxSizeMB":0}}`},
		{"negative recovery maxAgeHours", `{"recovery":{"enabled":true,"maxAgeHours":-1,"maxMessagesPerGroup":100}}`},
		{"negative recovery maxMessagesPerGroup", `{"recovery":{"enabled":true,"maxAgeHours":24,"maxMessagesPerGroup":0}}`},
		{"negative storage retention", `{"storage":{"messageRetentionDays":0}}`},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader([]byte(tc.body)))
			req.Header.Set("Authorization", authHeader)
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("PUT /api/config expected 400 for %s, got %d", tc.name, rec.Code)
			}
		})
	}

	// 3. PUT valid update
	{
		updateBody := `{
			"usernameMode": "hash",
			"media": {
				"enabled": false,
				"maxSizeMB": 50
			},
			"recovery": {
				"enabled": true,
				"maxAgeHours": 12,
				"maxMessagesPerGroup": 150
			},
			"storage": {
				"messageRetentionDays": 30
			}
		}`
		req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader([]byte(updateBody)))
		req.Header.Set("Authorization", authHeader)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT /api/config status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
		}
		var updated GlobalConfigDTO
		if err := json.NewDecoder(rec.Body).Decode(&updated); err != nil {
			t.Fatalf("decode updated config: %v", err)
		}
		if updated.UsernameMode != config.UsernameModeHash {
			t.Errorf("got usernameMode %q, want hash", updated.UsernameMode)
		}
		if updated.Media.MaxSizeMB != 50 || updated.Media.Enabled != false {
			t.Errorf("got media %+v", updated.Media)
		}
		if updated.Recovery.MaxAgeHours != 12 || updated.Recovery.MaxMessagesPerGroup != 150 {
			t.Errorf("got recovery %+v", updated.Recovery)
		}
		if updated.Storage.MessageRetentionDays != 30 {
			t.Errorf("got storage %+v", updated.Storage)
		}
		if configChanges != 1 {
			t.Fatalf("expected 1 config change notification, got %d", configChanges)
		}

		// 4. Verify GET returns updated values
		reqGet := httptest.NewRequest(http.MethodGet, "/api/config", nil)
		reqGet.Header.Set("Authorization", authHeader)
		recGet := httptest.NewRecorder()
		srv.Handler().ServeHTTP(recGet, reqGet)
		if recGet.Code != http.StatusOK {
			t.Fatalf("GET /api/config status = %d, want 200", recGet.Code)
		}
		var loaded GlobalConfigDTO
		if err := json.NewDecoder(recGet.Body).Decode(&loaded); err != nil {
			t.Fatalf("decode config: %v", err)
		}
		if loaded.UsernameMode != config.UsernameModeHash || loaded.Media.MaxSizeMB != 50 || loaded.Storage.MessageRetentionDays != 30 {
			t.Fatalf("loaded config mismatch: %+v", loaded)
		}
	}
}
