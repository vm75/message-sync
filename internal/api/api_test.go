package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthEndpoint(t *testing.T) {
	srv := NewServer(Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	contentType := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("expected Content-Type application/json, got %s", contentType)
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if resp["status"] != "ok" {
		t.Fatalf("expected status 'ok', got %q", resp["status"])
	}
}

func TestWriteJSONAndWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, http.StatusBadRequest, "invalid parameter")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	var errResp ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil {
		t.Fatal(err)
	}
	if errResp.Error != "invalid parameter" {
		t.Fatalf("expected 'invalid parameter', got %q", errResp.Error)
	}
}

func TestReadJSON(t *testing.T) {
	type sample struct {
		Name string `json:"name"`
	}

	// Valid JSON
	body := strings.NewReader(`{"name":"test"}`)
	req := httptest.NewRequest(http.MethodPost, "/test", body)
	var s sample
	if err := ReadJSON(req, &s); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if s.Name != "test" {
		t.Fatalf("expected name 'test', got %q", s.Name)
	}

	// Trailing JSON
	body = strings.NewReader(`{"name":"test"} {"extra":1}`)
	req = httptest.NewRequest(http.MethodPost, "/test", body)
	if err := ReadJSON(req, &s); err == nil {
		t.Fatal("expected trailing JSON error")
	}

	// Unknown field
	body = strings.NewReader(`{"name":"test","unknown":true}`)
	req = httptest.NewRequest(http.MethodPost, "/test", body)
	if err := ReadJSON(req, &s); err == nil {
		t.Fatal("expected unknown field error")
	}
}

func TestServerLifecycle(t *testing.T) {
	srv := NewServer(Options{
		Addr:   "127.0.0.1:0",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	addr := srv.listener.Addr().String()
	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("GET /health failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}
}
