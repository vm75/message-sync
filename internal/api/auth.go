package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/vm75/message-sync/internal/safelog"
	"golang.org/x/crypto/bcrypt"
)

const (
	defaultSessionTTL = 24 * time.Hour
	minPasswordLength = 8
	maxPasswordLength = 72
)

type sessionClaims struct {
	Subject   string `json:"sub"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
	Nonce     string `json:"nonce"`
}

type SessionManager struct {
	secret   []byte
	tokenTTL time.Duration
}

func NewSessionManager(secret []byte, ttl time.Duration) (*SessionManager, error) {
	if len(secret) == 0 {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, fmt.Errorf("generate random session secret: %w", err)
		}
	} else if len(secret) < 32 {
		return nil, errors.New("session secret must be at least 32 bytes")
	}

	if ttl == 0 {
		ttl = defaultSessionTTL
	}

	secretCopy := append([]byte(nil), secret...)
	return &SessionManager{
		secret:   secretCopy,
		tokenTTL: ttl,
	}, nil
}

func (sm *SessionManager) CreateToken() (string, error) {
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", fmt.Errorf("generate session nonce: %w", err)
	}

	now := time.Now().UTC()
	claims := sessionClaims{
		Subject:   "admin",
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(sm.tokenTTL).Unix(),
		Nonce:     hex.EncodeToString(nonceBytes),
	}

	payloadBytes, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal session claims: %w", err)
	}

	sig := sm.sign(payloadBytes)
	token := base64.RawURLEncoding.EncodeToString(payloadBytes) + "." + base64.RawURLEncoding.EncodeToString(sig)
	return token, nil
}

func (sm *SessionManager) ValidateToken(tokenStr string) bool {
	parts := strings.Split(tokenStr, ".")
	if len(parts) != 2 {
		return false
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}

	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}

	expectedSig := sm.sign(payloadBytes)
	if !hmac.Equal(sigBytes, expectedSig) {
		return false
	}

	var claims sessionClaims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return false
	}

	now := time.Now().UTC().Unix()
	if claims.Subject != "admin" {
		return false
	}
	if claims.ExpiresAt < now {
		return false
	}
	// Allow 60 seconds clock skew in the future for IssuedAt
	if claims.IssuedAt > now+60 {
		return false
	}

	return true
}

func (sm *SessionManager) sign(data []byte) []byte {
	mac := hmac.New(sha256.New, sm.secret)
	mac.Write(data)
	return mac.Sum(nil)
}

type AuthStatusResponse struct {
	IsSetup bool `json:"isSetup"`
}

type AuthPasswordRequest struct {
	Password string `json:"password"`
}

type AuthTokenResponse struct {
	Token string `json:"token"`
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	var hash string
	err := s.db.QueryRowContext(r.Context(), `SELECT admin_password_hash FROM global_config WHERE id = 1`).Scan(&hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		safelog.Error(s.logger, "query auth status failed", "auth_status", err)
		WriteError(w, http.StatusInternalServerError, "failed to check auth status")
		return
	}

	isSetup := hash != ""
	_ = WriteJSON(w, http.StatusOK, AuthStatusResponse{IsSetup: isSetup})
}

func (s *Server) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	var req AuthPasswordRequest
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(req.Password) < minPasswordLength || len(req.Password) > maxPasswordLength {
		WriteError(w, http.StatusBadRequest, fmt.Sprintf("password must be between %d and %d characters", minPasswordLength, maxPasswordLength))
		return
	}

	var existingHash string
	err := s.db.QueryRowContext(r.Context(), `SELECT admin_password_hash FROM global_config WHERE id = 1`).Scan(&existingHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		safelog.Error(s.logger, "check existing password failed", "auth_setup_check", err)
		WriteError(w, http.StatusInternalServerError, "failed to check existing auth setup")
		return
	}
	if existingHash != "" {
		WriteError(w, http.StatusBadRequest, "admin password already configured")
		return
	}

	hashBytes, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		safelog.Error(s.logger, "password hash failed", "auth_password_hash", err)
		WriteError(w, http.StatusInternalServerError, "failed to process password")
		return
	}

	_, err = s.db.ExecContext(r.Context(), `
		INSERT INTO global_config (id, admin_password_hash) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET admin_password_hash = excluded.admin_password_hash
	`, string(hashBytes))
	if err != nil {
		safelog.Error(s.logger, "save password hash failed", "auth_password_save", err)
		WriteError(w, http.StatusInternalServerError, "failed to save password")
		return
	}

	s.logger.Info("admin password configured successfully")

	token, err := s.sessions.CreateToken()
	if err != nil {
		safelog.Error(s.logger, "generate session token failed", "auth_session", err)
		WriteError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	s.setSessionCookie(w, token, int(s.sessions.tokenTTL.Seconds()))
	_ = WriteJSON(w, http.StatusOK, AuthTokenResponse{Token: token})
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	var req AuthPasswordRequest
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Password == "" {
		WriteError(w, http.StatusBadRequest, "password is required")
		return
	}

	var storedHash string
	err := s.db.QueryRowContext(r.Context(), `SELECT admin_password_hash FROM global_config WHERE id = 1`).Scan(&storedHash)
	if errors.Is(err, sql.ErrNoRows) || storedHash == "" {
		WriteError(w, http.StatusBadRequest, "admin password not configured")
		return
	}
	if err != nil {
		safelog.Error(s.logger, "fetch password hash failed", "auth_login_lookup", err)
		WriteError(w, http.StatusInternalServerError, "authentication failed")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(req.Password)); err != nil {
		s.logger.Warn("admin login attempt rejected")
		WriteError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	s.logger.Info("admin login successful")

	token, err := s.sessions.CreateToken()
	if err != nil {
		safelog.Error(s.logger, "generate session token failed", "auth_session", err)
		WriteError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	s.setSessionCookie(w, token, int(s.sessions.tokenTTL.Seconds()))
	_ = WriteJSON(w, http.StatusOK, AuthTokenResponse{Token: token})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	s.setSessionCookie(w, "", -1)
	_ = WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type AuthChangePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

func (s *Server) handleAuthChangePassword(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		WriteError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	var req AuthChangePasswordRequest
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.CurrentPassword == "" {
		WriteError(w, http.StatusBadRequest, "current password is required")
		return
	}

	if len(req.NewPassword) < minPasswordLength || len(req.NewPassword) > maxPasswordLength {
		WriteError(w, http.StatusBadRequest, fmt.Sprintf("new password must be between %d and %d characters", minPasswordLength, maxPasswordLength))
		return
	}

	var storedHash string
	err := s.db.QueryRowContext(r.Context(), `SELECT admin_password_hash FROM global_config WHERE id = 1`).Scan(&storedHash)
	if errors.Is(err, sql.ErrNoRows) || storedHash == "" {
		WriteError(w, http.StatusBadRequest, "admin password not configured")
		return
	}
	if err != nil {
		safelog.Error(s.logger, "fetch password hash failed", "auth_password_lookup", err)
		WriteError(w, http.StatusInternalServerError, "failed to verify password")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(req.CurrentPassword)); err != nil {
		s.logger.Warn("change password rejected: invalid current password")
		WriteError(w, http.StatusUnauthorized, "invalid current password")
		return
	}

	newHashBytes, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		safelog.Error(s.logger, "password hash failed", "auth_password_hash", err)
		WriteError(w, http.StatusInternalServerError, "failed to process new password")
		return
	}

	_, err = s.db.ExecContext(r.Context(), `UPDATE global_config SET admin_password_hash = ? WHERE id = 1`, string(newHashBytes))
	if err != nil {
		safelog.Error(s.logger, "update password hash failed", "auth_password_update", err)
		WriteError(w, http.StatusInternalServerError, "failed to update password")
		return
	}

	s.logger.Info("admin password updated successfully")
	_ = WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	})
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Only intercept /api/* routes
		if !strings.HasPrefix(path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		// Public authentication routes
		if path == "/api/auth/status" || path == "/api/auth/setup" || path == "/api/auth/login" || path == "/api/auth/logout" {
			next.ServeHTTP(w, r)
			return
		}

		// Check session authentication
		token := s.extractToken(r)
		if token == "" || s.sessions == nil || !s.sessions.ValidateToken(token) {
			WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) extractToken(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			token := strings.TrimSpace(parts[1])
			if token != "" {
				return token
			}
		}
	}

	if cookie, err := r.Cookie("session"); err == nil && cookie.Value != "" {
		return cookie.Value
	}
	if cookie, err := r.Cookie("token"); err == nil && cookie.Value != "" {
		return cookie.Value
	}

	return ""
}
