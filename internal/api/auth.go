package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/vm75/message-sync/internal/safelog"
	"golang.org/x/crypto/bcrypt"
)

const (
	defaultSessionTTL = 24 * time.Hour
	minPasswordLength = 8
	maxPasswordLength = 72
)

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{1,63}$`)

type Principal struct{ ID, Role, Username string }
type principalContextKey struct{}

func principalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalContextKey{}).(Principal)
	return p, ok
}

type loginFailure struct {
	count int
	until time.Time
}
type SessionManager struct {
	db       *sql.DB
	tokenTTL time.Duration
	mu       sync.Mutex
	failed   map[string]loginFailure
}

func NewSessionManager(db *sql.DB, ttl time.Duration) (*SessionManager, error) {
	if db == nil {
		return nil, errors.New("control database is required")
	}
	if ttl == 0 {
		ttl = defaultSessionTTL
	}
	if ttl < time.Second {
		return nil, errors.New("session TTL must be at least one second")
	}
	return &SessionManager{db: db, tokenTTL: ttl, failed: make(map[string]loginFailure)}, nil
}

func randomOpaqueID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate opaque ID: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func newBearerToken() (string, string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(token))
	return token, base64.RawURLEncoding.EncodeToString(h[:]), nil
}

func normalizeUsername(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
func validUsername(s string) bool       { return usernamePattern.MatchString(s) }

func (sm *SessionManager) createSession(ctx context.Context, userID string) (string, error) {
	token, hash, err := newBearerToken()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	_, err = sm.db.ExecContext(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at,created_at,last_seen_at) VALUES(?,?,?,?,?)`, hash, userID, now.Add(sm.tokenTTL).UnixMilli(), now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return "", fmt.Errorf("save session: %w", err)
	}
	return token, nil
}

// CreateToken is retained for internal package tests and small API fixtures.
// It still creates a hashed, database-backed session; it is not an alternate
// token format or authentication path.
func (sm *SessionManager) CreateToken() (string, error) {
	var userID string
	if err := sm.db.QueryRow(`SELECT id FROM users WHERE active = 1 ORDER BY created_at LIMIT 1`).Scan(&userID); err != nil {
		return "", err
	}
	return sm.createSession(context.Background(), userID)
}

func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func (sm *SessionManager) principal(ctx context.Context, token string) (Principal, error) {
	if token == "" {
		return Principal{}, sql.ErrNoRows
	}
	var p Principal
	var expires int64
	err := sm.db.QueryRowContext(ctx, `SELECT u.id,u.role,u.username,s.expires_at FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=? AND s.revoked_at IS NULL AND u.active=1`, tokenHash(token)).Scan(&p.ID, &p.Role, &p.Username, &expires)
	if err != nil || expires <= time.Now().UTC().UnixMilli() {
		return Principal{}, sql.ErrNoRows
	}
	_, _ = sm.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at=? WHERE token_hash=?`, time.Now().UTC().UnixMilli(), tokenHash(token))
	return p, nil
}

func (sm *SessionManager) revoke(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	_, err := sm.db.ExecContext(ctx, `UPDATE sessions SET revoked_at=? WHERE token_hash=?`, time.Now().UTC().UnixMilli(), tokenHash(token))
	return err
}

func (sm *SessionManager) revokeOtherSessions(ctx context.Context, userID, currentToken string) error {
	_, err := sm.db.ExecContext(ctx, `UPDATE sessions SET revoked_at=? WHERE user_id=? AND revoked_at IS NULL AND token_hash<>?`, time.Now().UTC().UnixMilli(), userID, tokenHash(currentToken))
	return err
}

type AuthStatusResponse struct {
	IsSetup bool `json:"isSetup"`
}
type AuthCredentialsRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type AuthTokenResponse struct {
	Token string    `json:"token"`
	User  Principal `json:"user"`
}

func (s *Server) controlAvailable(w http.ResponseWriter) bool {
	if s.controlDB == nil || s.sessions == nil {
		WriteError(w, http.StatusServiceUnavailable, "authentication unavailable")
		return false
	}
	return true
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	if !s.controlAvailable(w) {
		return
	}
	var count int
	if err := s.controlDB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		safelog.Error(s.logger, "query auth status failed", "auth_status", err)
		WriteError(w, http.StatusInternalServerError, "failed to check auth status")
		return
	}
	_ = WriteJSON(w, http.StatusOK, AuthStatusResponse{IsSetup: count > 0})
}

func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r.Context())
	if !ok { WriteError(w, http.StatusUnauthorized, "unauthorized"); return }
	_ = WriteJSON(w, http.StatusOK, p)
}

func (s *Server) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	if !s.controlAvailable(w) {
		return
	}
	var req AuthCredentialsRequest
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	username := normalizeUsername(req.Username)
	if !validUsername(username) {
		WriteError(w, http.StatusBadRequest, "invalid username")
		return
	}
	if len(req.Password) < minPasswordLength || len(req.Password) > maxPasswordLength {
		WriteError(w, http.StatusBadRequest, "invalid password")
		return
	}
	var count int
	if err := s.controlDB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		safelog.Error(s.logger, "check auth setup failed", "auth_setup_check", err)
		WriteError(w, http.StatusInternalServerError, "failed to check auth setup")
		return
	}
	if count != 0 {
		WriteError(w, http.StatusBadRequest, "account setup is already complete")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		safelog.Error(s.logger, "password hash failed", "auth_password_hash", err)
		WriteError(w, 500, "failed to process password")
		return
	}
	id, err := randomOpaqueID()
	if err != nil {
		safelog.Error(s.logger, "user ID generation failed", "auth_user_id", err)
		WriteError(w, 500, "failed to create account")
		return
	}
	now := time.Now().UTC().UnixMilli()
	tx, err := s.controlDB.BeginTx(r.Context(), nil)
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO users(id,username,password_hash,role,active,created_at,updated_at) VALUES(?,?,?,'admin',1,?,?)`, id, username, string(hash), now, now)
	}
	if err != nil {
		if tx != nil {
			_ = tx.Rollback()
		}
		safelog.Error(s.logger, "save initial account failed", "auth_setup_save", err)
		WriteError(w, 500, "failed to create account")
		return
	}
	if err = tx.Commit(); err != nil {
		safelog.Error(s.logger, "commit initial account failed", "auth_setup_commit", err)
		WriteError(w, 500, "failed to create account")
		return
	}
	s.issueSession(w, r, id, username, "admin")
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if !s.controlAvailable(w) {
		return
	}
	var req AuthCredentialsRequest
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, 400, "invalid request body")
		return
	}
	key := clientKey(r)
	if !s.allowLogin(key) {
		WriteError(w, 429, "try again later")
		return
	}
	username := normalizeUsername(req.Username)
	var p Principal
	var hash string
	err := s.controlDB.QueryRowContext(r.Context(), `SELECT id,username,role,password_hash FROM users WHERE username=? AND active=1`, username).Scan(&p.ID, &p.Username, &p.Role, &hash)
	if err != nil {
		hash = "$2a$10$7EqJtq98hPqEX7fNZaFWoOe0VdZK0VdJf7hQJmJj1L2R7F1T9D3mK"
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)) != nil || err != nil {
		s.recordLoginFailure(key)
		WriteError(w, 401, "invalid credentials")
		return
	}
	s.clearLoginFailure(key)
	s.issueSession(w, r, p.ID, p.Username, p.Role)
}

func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, id, username, role string) {
	token, err := s.sessions.createSession(r.Context(), id)
	if err != nil {
		safelog.Error(s.logger, "create session failed", "auth_session", err)
		WriteError(w, 500, "failed to create session")
		return
	}
	s.setSessionCookie(w, token, int(s.sessions.tokenTTL.Seconds()), r)
	_ = WriteJSON(w, 200, AuthTokenResponse{Token: token, User: Principal{ID: id, Username: username, Role: role}})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if !s.controlAvailable(w) {
		return
	}
	if err := s.sessions.revoke(r.Context(), s.extractToken(r)); err != nil {
		safelog.Error(s.logger, "revoke session failed", "auth_logout", err)
	}
	s.setSessionCookie(w, "", -1, r)
	_ = WriteJSON(w, 200, map[string]string{"status": "ok"})
}

type AuthChangePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

func (s *Server) handleAuthChangePassword(w http.ResponseWriter, r *http.Request) {
	if !s.controlAvailable(w) {
		return
	}
	p, ok := principalFromContext(r.Context())
	if !ok {
		WriteError(w, 401, "unauthorized")
		return
	}
	var req AuthChangePasswordRequest
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, 400, "invalid request body")
		return
	}
	if req.CurrentPassword == "" || len(req.NewPassword) < minPasswordLength || len(req.NewPassword) > maxPasswordLength {
		WriteError(w, 400, "invalid password")
		return
	}
	var oldHash string
	if err := s.controlDB.QueryRowContext(r.Context(), `SELECT password_hash FROM users WHERE id=? AND active=1`, p.ID).Scan(&oldHash); err != nil || bcrypt.CompareHashAndPassword([]byte(oldHash), []byte(req.CurrentPassword)) != nil {
		WriteError(w, 401, "invalid current password")
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		safelog.Error(s.logger, "password hash failed", "auth_password_hash", err)
		WriteError(w, 500, "failed to process new password")
		return
	}
	if _, err = s.controlDB.ExecContext(r.Context(), `UPDATE users SET password_hash=?,updated_at=? WHERE id=?`, string(newHash), time.Now().UTC().UnixMilli(), p.ID); err != nil {
		safelog.Error(s.logger, "update password failed", "auth_password_update", err)
		WriteError(w, 500, "failed to update password")
		return
	}
	if err = s.sessions.revokeOtherSessions(r.Context(), p.ID, s.extractToken(r)); err != nil {
		safelog.Error(s.logger, "revoke other sessions failed", "auth_password_revoke", err)
		WriteError(w, 500, "failed to update password")
		return
	}
	_ = WriteJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, maxAge int, r *http.Request) {
	secure := r != nil && (r.TLS != nil || strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https"))
	http.SetCookie(w, &http.Cookie{Name: "session", Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/auth/status" || r.URL.Path == "/api/auth/setup" || r.URL.Path == "/api/auth/login" || r.URL.Path == "/api/auth/invite/redeem" || r.URL.Path == "/api/auth/reset-password" {
			next.ServeHTTP(w, r)
			return
		}
		if s.sessions == nil {
			WriteError(w, 401, "unauthorized")
			return
		}
		p, err := s.sessions.principal(r.Context(), s.extractToken(r))
		if err != nil {
			WriteError(w, 401, "unauthorized")
			return
		}
		if requiresAdmin(r.URL.Path) && p.Role != "admin" {
			WriteError(w, http.StatusForbidden, "forbidden")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, p)))
	})
}

func requiresAdmin(path string) bool {
	return path == "/api/users" || strings.HasPrefix(path, "/api/users/") || path == "/api/audit"
}

func (s *Server) extractToken(r *http.Request) string {
	if parts := strings.SplitN(r.Header.Get("Authorization"), " ", 2); len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		if token := strings.TrimSpace(parts[1]); token != "" {
			return token
		}
	}
	if c, err := r.Cookie("session"); err == nil {
		return c.Value
	}
	return ""
}
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
func (s *Server) allowLogin(key string) bool {
	s.sessions.mu.Lock()
	defer s.sessions.mu.Unlock()
	f := s.sessions.failed[key]
	return f.until.IsZero() || time.Now().After(f.until)
}
func (s *Server) recordLoginFailure(key string) {
	s.sessions.mu.Lock()
	defer s.sessions.mu.Unlock()
	f := s.sessions.failed[key]
	f.count++
	if f.count >= 5 {
		f.until = time.Now().Add(time.Minute)
		f.count = 0
	}
	s.sessions.failed[key] = f
}
func (s *Server) clearLoginFailure(key string) {
	s.sessions.mu.Lock()
	defer s.sessions.mu.Unlock()
	delete(s.sessions.failed, key)
}
