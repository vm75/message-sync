package api

import (
	"net/http"
	"time"

	"github.com/vm75/message-sync/internal/safelog"
	"golang.org/x/crypto/bcrypt"
)

type UserSummary struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	Active    bool   `json:"active"`
	CreatedAt int64  `json:"createdAt"`
}
type InviteRequest struct {
	Role     string `json:"role"`
	TTLHours int    `json:"ttlHours"`
}
type SecretTokenResponse struct {
	Token string `json:"token"`
}
type RedeemInviteRequest struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	Password string `json:"password"`
}
type SetActiveRequest struct {
	Active bool `json:"active"`
}
type AuditEvent struct {
	ID          string `json:"id"`
	ActorUserID string `json:"actorUserId,omitempty"`
	Action      string `json:"action"`
	TargetID    string `json:"targetId,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.controlDB.QueryContext(r.Context(), `SELECT id,username,role,active,created_at FROM users ORDER BY created_at,id`)
	if err != nil {
		safelog.Error(s.logger, "list users failed", "users_list", err)
		WriteError(w, 500, "failed to list users")
		return
	}
	defer rows.Close()
	users := make([]UserSummary, 0)
	for rows.Next() {
		var u UserSummary
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.Active, &u.CreatedAt); err != nil {
			WriteError(w, 500, "failed to list users")
			return
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		WriteError(w, 500, "failed to list users")
		return
	}
	_ = WriteJSON(w, 200, users)
}

func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFromContext(r.Context())
	var req InviteRequest
	if err := ReadJSON(r, &req); err != nil || (req.Role != "admin" && req.Role != "operator") {
		WriteError(w, 400, "invalid invite")
		return
	}
	if req.TTLHours <= 0 || req.TTLHours > 7*24 {
		req.TTLHours = 24
	}
	token, hash, err := newBearerToken()
	if err != nil {
		WriteError(w, 500, "failed to create invite")
		return
	}
	id, err := randomOpaqueID()
	if err != nil {
		WriteError(w, 500, "failed to create invite")
		return
	}
	now := time.Now().UTC()
	_, err = s.controlDB.ExecContext(r.Context(), `INSERT INTO user_invites(id,token_hash,target_role,expires_at,creator_user_id) VALUES(?,?,?,?,?)`, id, hash, req.Role, now.Add(time.Duration(req.TTLHours)*time.Hour).UnixMilli(), p.ID)
	if err != nil {
		safelog.Error(s.logger, "create invite failed", "invite_create", err)
		WriteError(w, 500, "failed to create invite")
		return
	}
	s.audit(r, "invite_created", id)
	_ = WriteJSON(w, 201, SecretTokenResponse{Token: token})
}

func (s *Server) handleInviteRedeem(w http.ResponseWriter, r *http.Request) {
	var req RedeemInviteRequest
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, 400, "invalid invite")
		return
	}
	req.Username = normalizeUsername(req.Username)
	if !validUsername(req.Username) || len(req.Password) < minPasswordLength || len(req.Password) > maxPasswordLength || req.Token == "" {
		WriteError(w, 400, "invalid invite")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		WriteError(w, 500, "failed to create account")
		return
	}
	id, err := randomOpaqueID()
	if err != nil {
		WriteError(w, 500, "failed to create account")
		return
	}
	now := time.Now().UTC()
	tx, err := s.controlDB.BeginTx(r.Context(), nil)
	if err != nil {
		WriteError(w, 500, "failed to redeem invite")
		return
	}
	defer tx.Rollback()
	var inviteID, role string
	var expires int64
	if err = tx.QueryRowContext(r.Context(), `SELECT id,target_role,expires_at FROM user_invites WHERE token_hash=? AND consumed_at IS NULL`, tokenHash(req.Token)).Scan(&inviteID, &role, &expires); err != nil || expires <= now.UnixMilli() {
		WriteError(w, 401, "invalid invite")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO users(id,username,password_hash,role,active,created_at,updated_at) VALUES(?,?,?, ?,1,?,?)`, id, req.Username, string(hash), role, now.UnixMilli(), now.UnixMilli()); err != nil {
		WriteError(w, 400, "unable to create account")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE user_invites SET consumed_at=? WHERE id=? AND consumed_at IS NULL`, now.UnixMilli(), inviteID); err != nil {
		WriteError(w, 500, "failed to redeem invite")
		return
	}
	if err = tx.Commit(); err != nil {
		WriteError(w, 500, "failed to redeem invite")
		return
	}
	s.issueSession(w, r, id, req.Username, role)
}

func (s *Server) handleSetUserActive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req SetActiveRequest
	if err := ReadJSON(r, &req); err != nil {
		WriteError(w, 400, "invalid request")
		return
	}
	p, _ := principalFromContext(r.Context())
	if !req.Active && id == p.ID {
		WriteError(w, 400, "cannot deactivate current account")
		return
	}
	tx, err := s.controlDB.BeginTx(r.Context(), nil)
	if err != nil {
		WriteError(w, 500, "failed to update account")
		return
	}
	defer tx.Rollback()
	var role string
	var active bool
	if err = tx.QueryRowContext(r.Context(), `SELECT role,active FROM users WHERE id=?`, id).Scan(&role, &active); err != nil {
		WriteError(w, 404, "account not found")
		return
	}
	if !req.Active && role == "admin" && active {
		var admins int
		_ = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM users WHERE role='admin' AND active=1`).Scan(&admins)
		if admins <= 1 {
			WriteError(w, 400, "cannot deactivate last active admin")
			return
		}
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE users SET active=?,updated_at=? WHERE id=?`, req.Active, time.Now().UTC().UnixMilli(), id); err != nil {
		WriteError(w, 500, "failed to update account")
		return
	}
	if err = tx.Commit(); err != nil {
		WriteError(w, 500, "failed to update account")
		return
	}
	s.audit(r, "user_activation_changed", id)
	_ = WriteJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleCreateResetToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var active bool
	if err := s.controlDB.QueryRowContext(r.Context(), `SELECT active FROM users WHERE id=?`, id).Scan(&active); err != nil {
		WriteError(w, 404, "account not found")
		return
	}
	if !active {
		WriteError(w, 400, "account is inactive")
		return
	}
	token, hash, err := newBearerToken()
	if err != nil {
		WriteError(w, 500, "failed to create reset token")
		return
	}
	now := time.Now().UTC()
	_, err = s.controlDB.ExecContext(r.Context(), `INSERT INTO password_reset_tokens(token_hash,user_id,expires_at,created_at) VALUES(?,?,?,?)`, hash, id, now.Add(time.Hour).UnixMilli(), now.UnixMilli())
	if err != nil {
		WriteError(w, 500, "failed to create reset token")
		return
	}
	s.audit(r, "password_reset_token_created", id)
	_ = WriteJSON(w, 201, SecretTokenResponse{Token: token})
}

func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := ReadJSON(r, &req); err != nil || req.Token == "" || len(req.Password) < minPasswordLength || len(req.Password) > maxPasswordLength {
		WriteError(w, 400, "invalid reset token")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		WriteError(w, 500, "failed to reset password")
		return
	}
	tx, err := s.controlDB.BeginTx(r.Context(), nil)
	if err != nil {
		WriteError(w, 500, "failed to reset password")
		return
	}
	defer tx.Rollback()
	var userID string
	var exp int64
	if err = tx.QueryRowContext(r.Context(), `SELECT user_id,expires_at FROM password_reset_tokens WHERE token_hash=? AND consumed_at IS NULL`, tokenHash(req.Token)).Scan(&userID, &exp); err != nil || exp <= time.Now().UnixMilli() {
		WriteError(w, 401, "invalid reset token")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE users SET password_hash=?,updated_at=? WHERE id=? AND active=1`, string(hash), time.Now().UnixMilli(), userID); err != nil {
		WriteError(w, 500, "failed to reset password")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE password_reset_tokens SET consumed_at=? WHERE token_hash=?`, time.Now().UnixMilli(), tokenHash(req.Token)); err != nil {
		WriteError(w, 500, "failed to reset password")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE sessions SET revoked_at=? WHERE user_id=? AND revoked_at IS NULL`, time.Now().UnixMilli(), userID); err != nil {
		WriteError(w, 500, "failed to reset password")
		return
	}
	if err = tx.Commit(); err != nil {
		WriteError(w, 500, "failed to reset password")
		return
	}
	_ = WriteJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) audit(r *http.Request, action, target string) {
	p, ok := principalFromContext(r.Context())
	if !ok {
		return
	}
	id, err := randomOpaqueID()
	if err != nil {
		return
	}
	_, _ = s.controlDB.ExecContext(r.Context(), `INSERT INTO audit_events(id,actor_user_id,action,target_id,created_at) VALUES(?,?,?,?,?)`, id, p.ID, action, target, time.Now().UnixMilli())
}
func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	rows, err := s.controlDB.QueryContext(r.Context(), `SELECT id,COALESCE(actor_user_id,''),action,COALESCE(target_id,''),created_at FROM audit_events ORDER BY created_at DESC,id DESC LIMIT 500`)
	if err != nil {
		WriteError(w, 500, "failed to list audit events")
		return
	}
	defer rows.Close()
	out := []AuditEvent{}
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(&e.ID, &e.ActorUserID, &e.Action, &e.TargetID, &e.CreatedAt); err != nil {
			WriteError(w, 500, "failed to list audit events")
			return
		}
		out = append(out, e)
	}
	_ = WriteJSON(w, 200, out)
}
