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

	"github.com/vm75/message-sync/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "sync.db")
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("failed to open test store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st.DB()
}

func setupTestServer(t *testing.T, db *sql.DB) *Server {
	t.Helper()
	secret := []byte("01234567890123456789012345678901") // 32 bytes
	srv := NewServer(Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:     db,
		Secret: secret,
	})
	return srv
}

func TestSessionManager(t *testing.T) {
	secret := []byte("01234567890123456789012345678901")
	sm, err := NewSessionManager(secret, time.Hour)
	if err != nil {
		t.Fatalf("unexpected error creating session manager: %v", err)
	}

	// Token creation & validation
	token, err := sm.CreateToken()
	if err != nil {
		t.Fatalf("unexpected error creating token: %v", err)
	}
	if !sm.ValidateToken(token) {
		t.Fatal("expected valid token to pass validation")
	}

	// Secret too short
	if _, err := NewSessionManager([]byte("short"), time.Hour); err == nil {
		t.Fatal("expected error for secret < 32 bytes")
	}

	// Random secret fallback when nil/empty
	smRandom, err := NewSessionManager(nil, time.Hour)
	if err != nil {
		t.Fatalf("unexpected error with nil secret: %v", err)
	}
	rToken, err := smRandom.CreateToken()
	if err != nil {
		t.Fatalf("unexpected error creating token with random secret: %v", err)
	}
	if !smRandom.ValidateToken(rToken) {
		t.Fatal("expected random-secret token to pass validation")
	}
	if sm.ValidateToken(rToken) {
		t.Fatal("token signed by different secret should fail validation")
	}

	// Tampered token payload
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		t.Fatalf("unexpected token format: %s", token)
	}
	tamperedPayload := parts[0] + "xyz"
	if sm.ValidateToken(tamperedPayload + "." + parts[1]) {
		t.Fatal("expected tampered payload to fail validation")
	}

	// Tampered signature
	tamperedSig := parts[1] + "xyz"
	if sm.ValidateToken(parts[0] + "." + tamperedSig) {
		t.Fatal("expected tampered signature to fail validation")
	}

	// Malformed tokens
	if sm.ValidateToken("invalid-token") {
		t.Fatal("expected malformed token to fail validation")
	}
	if sm.ValidateToken("a.b.c") {
		t.Fatal("expected 3-part token to fail validation")
	}

	// Expired token
	smExpired, _ := NewSessionManager(secret, -1*time.Second)
	expToken, err := smExpired.CreateToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sm.ValidateToken(expToken) {
		t.Fatal("expected expired token to fail validation")
	}
}

func TestAuthStatusEndpoint(t *testing.T) {
	db := setupTestDB(t)
	srv := setupTestServer(t, db)

	// Initially not setup
	req := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	var resp AuthStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp.IsSetup {
		t.Fatal("expected IsSetup to be false initially")
	}

	// Set password hash in database
	hash, err := bcrypt.GenerateFromPassword([]byte("supersecret123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE global_config SET admin_password_hash = ? WHERE id = 1`, string(hash)); err != nil {
		t.Fatal(err)
	}

	// Check status again
	req = httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if !resp.IsSetup {
		t.Fatal("expected IsSetup to be true after setting password")
	}
}

func TestAuthSetupEndpoint(t *testing.T) {
	db := setupTestDB(t)
	srv := setupTestServer(t, db)

	// Short password (< 8 chars)
	body, _ := json.Marshal(AuthPasswordRequest{Password: "short"})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for short password, got %d", rec.Code)
	}

	// Long password (> 72 chars)
	body, _ = json.Marshal(AuthPasswordRequest{Password: strings.Repeat("a", 73)})
	req = httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for long password, got %d", rec.Code)
	}

	// Valid setup
	body, _ = json.Marshal(AuthPasswordRequest{Password: "validpassword123"})
	req = httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid setup, got %d (body: %s)", rec.Code, rec.Body.String())
	}
	var tokenResp AuthTokenResponse
	if err := json.NewDecoder(rec.Body).Decode(&tokenResp); err != nil {
		t.Fatalf("failed to decode token response: %v", err)
	}
	if tokenResp.Token == "" {
		t.Fatal("expected non-empty token")
	}

	// Check cookie was set
	cookies := rec.Result().Cookies()
	var foundCookie bool
	for _, c := range cookies {
		if c.Name == "session" && c.Value == tokenResp.Token {
			foundCookie = true
			if !c.HttpOnly {
				t.Error("expected HttpOnly cookie")
			}
		}
	}
	if !foundCookie {
		t.Fatal("expected session cookie with token value")
	}

	// Check DB has hash
	var storedHash string
	if err := db.QueryRow(`SELECT admin_password_hash FROM global_config WHERE id = 1`).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte("validpassword123")); err != nil {
		t.Fatalf("stored hash does not match password: %v", err)
	}

	// Duplicate setup attempt should fail
	body, _ = json.Marshal(AuthPasswordRequest{Password: "anotherpassword123"})
	req = httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for duplicate setup, got %d", rec.Code)
	}
}

func TestAuthLoginAndLogoutEndpoints(t *testing.T) {
	db := setupTestDB(t)
	srv := setupTestServer(t, db)

	// Attempt login before setup
	body, _ := json.Marshal(AuthPasswordRequest{Password: "adminpassword123"})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for login before setup, got %d", rec.Code)
	}

	// Setup password
	reqSetup := httptest.NewRequest(http.MethodPost, "/api/auth/setup", bytes.NewReader(body))
	recSetup := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recSetup, reqSetup)
	if recSetup.Code != http.StatusOK {
		t.Fatalf("setup failed: %d", recSetup.Code)
	}

	// Wrong password login
	wrongBody, _ := json.Marshal(AuthPasswordRequest{Password: "wrongpassword"})
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(wrongBody))
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong password, got %d", rec.Code)
	}

	// Correct password login
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for correct login, got %d", rec.Code)
	}
	var tokenResp AuthTokenResponse
	if err := json.NewDecoder(rec.Body).Decode(&tokenResp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if tokenResp.Token == "" {
		t.Fatal("expected non-empty token")
	}

	// Logout
	reqLogout := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	recLogout := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recLogout, reqLogout)
	if recLogout.Code != http.StatusOK {
		t.Fatalf("expected 200 for logout, got %d", recLogout.Code)
	}
	cookies := recLogout.Result().Cookies()
	var cleared bool
	for _, c := range cookies {
		if c.Name == "session" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("expected session cookie to be cleared on logout")
	}
}

func TestAuthMiddleware(t *testing.T) {
	db := setupTestDB(t)
	srv := setupTestServer(t, db)

	// Add a protected test endpoint to mux
	srv.mux.HandleFunc("GET /api/test/protected", func(w http.ResponseWriter, r *http.Request) {
		_ = WriteJSON(w, http.StatusOK, map[string]string{"secret_data": "ok"})
	})

	// 1. Health endpoint should always pass without auth
	reqHealth := httptest.NewRequest(http.MethodGet, "/health", nil)
	recHealth := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recHealth, reqHealth)
	if recHealth.Code != http.StatusOK {
		t.Fatalf("expected 200 for /health, got %d", recHealth.Code)
	}

	// 2. Auth public endpoints should pass without auth
	reqStatus := httptest.NewRequest(http.MethodGet, "/api/auth/status", nil)
	recStatus := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recStatus, reqStatus)
	if recStatus.Code != http.StatusOK {
		t.Fatalf("expected 200 for /api/auth/status, got %d", recStatus.Code)
	}

	// 3. Protected endpoint without token should return 401
	reqProt := httptest.NewRequest(http.MethodGet, "/api/test/protected", nil)
	recProt := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recProt, reqProt)
	if recProt.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated protected route, got %d", recProt.Code)
	}

	// 4. Protected endpoint with invalid token should return 401
	reqInvalid := httptest.NewRequest(http.MethodGet, "/api/test/protected", nil)
	reqInvalid.Header.Set("Authorization", "Bearer invalid-token")
	recInvalid := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recInvalid, reqInvalid)
	if recInvalid.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid token, got %d", recInvalid.Code)
	}

	// Generate a valid token
	token, err := srv.sessions.CreateToken()
	if err != nil {
		t.Fatalf("unexpected error creating token: %v", err)
	}

	// 5. Protected endpoint with Bearer header should succeed
	reqBearer := httptest.NewRequest(http.MethodGet, "/api/test/protected", nil)
	reqBearer.Header.Set("Authorization", "Bearer "+token)
	recBearer := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recBearer, reqBearer)
	if recBearer.Code != http.StatusOK {
		t.Fatalf("expected 200 with Bearer token, got %d", recBearer.Code)
	}

	// 6. Protected endpoint with session cookie should succeed
	reqCookie := httptest.NewRequest(http.MethodGet, "/api/test/protected", nil)
	reqCookie.AddCookie(&http.Cookie{Name: "session", Value: token})
	recCookie := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recCookie, reqCookie)
	if recCookie.Code != http.StatusOK {
		t.Fatalf("expected 200 with session cookie, got %d", recCookie.Code)
	}

	// 7. Protected endpoint with token cookie should succeed
	reqTokenCookie := httptest.NewRequest(http.MethodGet, "/api/test/protected", nil)
	reqTokenCookie.AddCookie(&http.Cookie{Name: "token", Value: token})
	recTokenCookie := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recTokenCookie, reqTokenCookie)
	if recTokenCookie.Code != http.StatusOK {
		t.Fatalf("expected 200 with token cookie, got %d", recTokenCookie.Code)
	}
}
