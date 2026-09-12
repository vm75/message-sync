package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/vm75/message-sync/internal/controlstore"
)

type mtprotoSetupRequest struct {
	APIID   int    `json:"apiId"`
	APIHash string `json:"apiHash"`
	Phone   string `json:"phone"`
}
type mtprotoCodeRequest struct {
	Code string `json:"code"`
}
type mtprotoPasswordRequest struct {
	Password string `json:"password"`
}

func (s *Server) requireMTProtoConnection(ctx context.Context, id string) error {
	var transportName, mode string
	var enabled bool
	err := s.controlDB.QueryRowContext(ctx, `SELECT transport,integration_mode,enabled FROM transport_connections WHERE id=?`, id).Scan(&transportName, &mode, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("connection not found")
	}
	if err != nil {
		return errors.New("database error")
	}
	if transportName != "telegram" || controlstore.NormalizeIntegrationMode(transportName, mode) != controlstore.TelegramIntegrationModeMTProto {
		return errors.New("not a Telegram MTProto connection")
	}
	if !enabled {
		return errors.New("connection is disabled")
	}
	return nil
}
func (s *Server) mtprotoService() (telegramMTProtoAuthService, bool) {
	svc, ok := s.connections.(telegramMTProtoAuthService)
	return svc, ok
}
func writeMTProtoOpError(w http.ResponseWriter, err error) {
	msg := err.Error()
	switch msg {
	case "connection not found":
		WriteError(w, http.StatusNotFound, msg)
	case "not a Telegram MTProto connection", "connection is disabled":
		WriteError(w, http.StatusBadRequest, msg)
	default:
		WriteError(w, http.StatusBadRequest, msg)
	}
}

func (s *Server) handleTelegramMTProtoSetup(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := s.requireMTProtoConnection(r.Context(), id); err != nil {
		writeMTProtoOpError(w, err)
		return
	}
	var req mtprotoSetupRequest
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.APIID <= 0 || strings.TrimSpace(req.APIHash) == "" || strings.TrimSpace(req.Phone) == "" {
		WriteError(w, http.StatusBadRequest, "apiId, apiHash, and phone are required")
		return
	}
	svc, ok := s.mtprotoService()
	if !ok {
		WriteError(w, http.StatusServiceUnavailable, "Telegram MTProto runtime unavailable")
		return
	}
	status, err := svc.TelegramMTProtoConfigure(r.Context(), id, req.APIID, req.APIHash, req.Phone)
	req.APIHash = ""
	req.Phone = ""
	if err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "telegram_mtproto_configured", id)
	_ = WriteJSON(w, http.StatusOK, status)
}
func (s *Server) handleTelegramMTProtoSendCode(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := s.requireMTProtoConnection(r.Context(), id); err != nil {
		writeMTProtoOpError(w, err)
		return
	}
	svc, ok := s.mtprotoService()
	if !ok {
		WriteError(w, http.StatusServiceUnavailable, "Telegram MTProto runtime unavailable")
		return
	}
	status, err := svc.TelegramMTProtoSendCode(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "telegram_mtproto_code_requested", id)
	_ = WriteJSON(w, http.StatusOK, status)
}
func (s *Server) handleTelegramMTProtoCode(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := s.requireMTProtoConnection(r.Context(), id); err != nil {
		writeMTProtoOpError(w, err)
		return
	}
	var req mtprotoCodeRequest
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	code := strings.TrimSpace(req.Code)
	req.Code = ""
	if code == "" {
		WriteError(w, http.StatusBadRequest, "login code is required")
		return
	}
	svc, ok := s.mtprotoService()
	if !ok {
		WriteError(w, http.StatusServiceUnavailable, "Telegram MTProto runtime unavailable")
		return
	}
	status, err := svc.TelegramMTProtoSubmitCode(r.Context(), id, code)
	code = ""
	if err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "telegram_mtproto_code_verified", id)
	_ = WriteJSON(w, http.StatusOK, status)
}
func (s *Server) handleTelegramMTProtoPassword(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if err := s.requireMTProtoConnection(r.Context(), id); err != nil {
		writeMTProtoOpError(w, err)
		return
	}
	var req mtprotoPasswordRequest
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	password := []byte(req.Password)
	req.Password = ""
	defer func() {
		for i := range password {
			password[i] = 0
		}
	}()
	if len(password) == 0 {
		WriteError(w, http.StatusBadRequest, "2FA password is required")
		return
	}
	svc, ok := s.mtprotoService()
	if !ok {
		WriteError(w, http.StatusServiceUnavailable, "Telegram MTProto runtime unavailable")
		return
	}
	status, err := svc.TelegramMTProtoSubmitPassword(r.Context(), id, password)
	if err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "telegram_mtproto_password_verified", id)
	_ = WriteJSON(w, http.StatusOK, status)
}
