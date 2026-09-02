package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"github.com/vm75/message-sync/internal/verification"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var verificationPhonePattern = regexp.MustCompile(`^\+?[0-9]{7,20}$`)
var verificationDiscordIDPattern = regexp.MustCompile(`^[0-9]{16,22}$`)

type PipelineInput struct {
	Label         string `json:"label"`
	Transport     string `json:"transport"`
	EndpointAlias string `json:"endpointAlias"`
	DiscordRoleID string `json:"discordRoleId"`
	Enabled       bool   `json:"enabled"`
}
type PipelineView struct {
	ID            string `json:"id"`
	PublicToken   string `json:"publicToken,omitempty"`
	Label         string `json:"label"`
	Transport     string `json:"transport"`
	EndpointAlias string `json:"endpointAlias,omitempty"`
	DiscordRoleID string `json:"discordRoleId,omitempty"`
	Enabled       bool   `json:"enabled"`
}

func (s *Server) validatePipeline(r *http.Request, in PipelineInput) error {
	if strings.TrimSpace(in.Label) == "" || (in.Transport != "whatsapp" && in.Transport != "discord") || in.EndpointAlias == "" {
		return errors.New("invalid pipeline")
	}
	var transport string
	if err := s.db.QueryRowContext(r.Context(), `SELECT transport FROM endpoints WHERE alias = ?`, in.EndpointAlias).Scan(&transport); err != nil || transport != in.Transport {
		return errors.New("pipeline endpoint is unavailable")
	}
	return nil
}

func (s *Server) handleListPipelines(w http.ResponseWriter, r *http.Request) {
	rows, err := s.controlDB.QueryContext(r.Context(), `SELECT id,public_token,label,target_transport,endpoint_alias,COALESCE(discord_role_id,''),enabled FROM verification_pipelines ORDER BY created_at`)
	if err != nil {
		WriteError(w, 500, "failed to list pipelines")
		return
	}
	defer rows.Close()
	out := []PipelineView{}
	for rows.Next() {
		var p PipelineView
		if err := rows.Scan(&p.ID, &p.PublicToken, &p.Label, &p.Transport, &p.EndpointAlias, &p.DiscordRoleID, &p.Enabled); err != nil {
			WriteError(w, 500, "failed to list pipelines")
			return
		}
		out = append(out, p)
	}
	_ = WriteJSON(w, 200, out)
}

func (s *Server) handleCreatePipeline(w http.ResponseWriter, r *http.Request) {
	var in PipelineInput
	if ReadJSON(r, &in) != nil || s.validatePipeline(r, in) != nil {
		WriteError(w, 400, "invalid pipeline")
		return
	}
	id, err := randomOpaqueID()
	if err != nil {
		WriteError(w, 500, "failed to create pipeline")
		return
	}
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		WriteError(w, 500, "failed to create pipeline")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	p, _ := principalFromContext(r.Context())
	now := time.Now().UnixMilli()
	_, err = s.controlDB.ExecContext(r.Context(), `INSERT INTO verification_pipelines(id,public_token,label,target_transport,endpoint_alias,discord_role_id,enabled,creator_user_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, token, in.Label, in.Transport, in.EndpointAlias, in.DiscordRoleID, in.Enabled, p.ID, now, now)
	if err != nil {
		WriteError(w, 500, "failed to create pipeline")
		return
	}
	_ = WriteJSON(w, 201, PipelineView{ID: id, PublicToken: token, Label: in.Label, Transport: in.Transport, EndpointAlias: in.EndpointAlias, DiscordRoleID: in.DiscordRoleID, Enabled: in.Enabled})
}

func (s *Server) handleUpdatePipeline(w http.ResponseWriter, r *http.Request) {
	var in PipelineInput
	if ReadJSON(r, &in) != nil || s.validatePipeline(r, in) != nil {
		WriteError(w, 400, "invalid pipeline")
		return
	}
	res, err := s.controlDB.ExecContext(r.Context(), `UPDATE verification_pipelines SET label=?,target_transport=?,endpoint_alias=?,discord_role_id=?,enabled=?,updated_at=? WHERE id=?`, in.Label, in.Transport, in.EndpointAlias, in.DiscordRoleID, in.Enabled, time.Now().UnixMilli(), r.PathValue("id"))
	if err != nil {
		WriteError(w, 500, "failed to update pipeline")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		WriteError(w, 404, "pipeline not found")
		return
	}
	_ = WriteJSON(w, 200, map[string]string{"status": "ok"})
}
func (s *Server) handleDeletePipeline(w http.ResponseWriter, r *http.Request) {
	res, err := s.controlDB.ExecContext(r.Context(), `DELETE FROM verification_pipelines WHERE id=?`, r.PathValue("id"))
	if err != nil {
		WriteError(w, 500, "failed to delete pipeline")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		WriteError(w, 404, "pipeline not found")
		return
	}
	_ = WriteJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) pipelineByToken(r *http.Request) (PipelineView, bool) {
	var p PipelineView
	err := s.controlDB.QueryRowContext(r.Context(), `SELECT id,public_token,label,target_transport,endpoint_alias,COALESCE(discord_role_id,''),enabled FROM verification_pipelines WHERE public_token=?`, r.PathValue("token")).Scan(&p.ID, &p.PublicToken, &p.Label, &p.Transport, &p.EndpointAlias, &p.DiscordRoleID, &p.Enabled)
	return p, err == nil && p.Enabled
}
func (s *Server) handlePublicPipeline(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pipelineByToken(r)
	if !ok {
		WriteError(w, 404, "verification page not found")
		return
	}
	p.ID = ""
	p.PublicToken = ""
	p.EndpointAlias = ""
	p.DiscordRoleID = ""
	_ = WriteJSON(w, 200, p)
}

func (s *Server) handlePublicIntake(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pipelineByToken(r)
	if !ok {
		WriteError(w, 404, "verification page not found")
		return
	}
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		WriteError(w, 400, "invalid application")
		return
	}
	email := strings.TrimSpace(r.FormValue("workEmail"))
	identity := strings.TrimSpace(r.FormValue("identity"))
	if !strings.Contains(email, "@") || len(email) > 320 || (p.Transport == "whatsapp" && !verificationPhonePattern.MatchString(identity)) || (p.Transport == "discord" && !verificationDiscordIDPattern.MatchString(identity)) {
		WriteError(w, 400, "invalid application")
		return
	}
	var existing int
	if err := s.controlDB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM membership_requests WHERE pipeline_id = ? AND applicant_work_email = ? AND status IN ('pending_email','pending')`, p.ID, email).Scan(&existing); err == nil && existing > 0 {
		_ = WriteJSON(w, http.StatusAccepted, map[string]string{"status": "received"})
		return
	}
	ref := ""
	if f, h, err := r.FormFile("evidence"); err == nil {
		defer f.Close()
		if h.Size > 5<<20 {
			WriteError(w, 400, "evidence too large")
			return
		}
		head := make([]byte, 512)
		n, _ := io.ReadFull(f, head)
		kind := http.DetectContentType(head[:n])
		if kind != "application/pdf" && !strings.HasPrefix(kind, "image/") {
			WriteError(w, 400, "unsupported evidence type")
			return
		}
		if s.evidenceDir == "" {
			WriteError(w, 500, "evidence storage unavailable")
			return
		}
		if err = os.MkdirAll(s.evidenceDir, 0700); err != nil {
			WriteError(w, 500, "evidence storage unavailable")
			return
		}
		name, _ := randomOpaqueID()
		ref = name
		path := filepath.Join(s.evidenceDir, name)
		out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			WriteError(w, 500, "evidence storage unavailable")
			return
		}
		_, err = out.Write(head[:n])
		if err == nil {
			_, err = io.Copy(out, io.LimitReader(f, 5<<20-int64(n)))
		}
		out.Close()
		if err != nil {
			_ = os.Remove(path)
			WriteError(w, 400, "invalid evidence")
			return
		}
	}
	id, _ := randomOpaqueID()
	now := time.Now().UnixMilli()
	waID, dcID := "", ""
	if p.Transport == "whatsapp" {
		waID = identity
	} else {
		dcID = identity
	}
	_, err := s.controlDB.ExecContext(r.Context(), `INSERT INTO membership_requests(id,pipeline_id,status,applicant_work_email,applicant_whatsapp_phone,applicant_discord_user_id,linkedin_url,evidence_reference,verification_state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, p.ID, "pending_email", email, waID, dcID, strings.TrimSpace(r.FormValue("linkedinURL")), ref, "pending", now, now)
	if err != nil {
		if ref != "" {
			_ = os.Remove(filepath.Join(s.evidenceDir, ref))
		}
		WriteError(w, 400, "application could not be accepted")
		return
	}
	challenge, challengeHash, challengeErr := newBearerToken()
	if challengeErr != nil {
		_, _ = s.controlDB.ExecContext(r.Context(), `DELETE FROM membership_requests WHERE id = ?`, id)
		if ref != "" {
			_ = os.Remove(filepath.Join(s.evidenceDir, ref))
		}
		WriteError(w, 500, "application could not be accepted")
		return
	}
	_, challengeErr = s.controlDB.ExecContext(r.Context(), `INSERT INTO email_challenges(id,membership_request_id,token_hash,expires_at) VALUES(?,?,?,?)`, id+"-email", id, challengeHash, time.Now().Add(24*time.Hour).UnixMilli())
	if challengeErr != nil {
		_, _ = s.controlDB.ExecContext(r.Context(), `DELETE FROM membership_requests WHERE id = ?`, id)
		if ref != "" {
			_ = os.Remove(filepath.Join(s.evidenceDir, ref))
		}
		WriteError(w, 500, "application could not be accepted")
		return
	}
	assessment := verification.Assess(email, r.FormValue("linkedinURL"), ref != "")
	_, _ = s.controlDB.ExecContext(r.Context(), `INSERT INTO verification_assessments(id,membership_request_id,assessment_kind,state,result_code,confidence,detail,created_at,updated_at) VALUES(?,?,?,'complete',?,?,?, ?,?)`, id+"-deterministic", id, "deterministic", assessment.Result, nil, assessment.DomainClass, now, now)
	if s.mailer != nil {
		_ = s.mailer.Send(r.Context(), email, p.Label, challenge)
	}
	if s.analyzer != nil && ref != "" {
		if evidence, readErr := os.ReadFile(filepath.Join(s.evidenceDir, ref)); readErr == nil {
			_, _ = s.controlDB.ExecContext(r.Context(), `INSERT INTO verification_assessments(id,membership_request_id,assessment_kind,state,created_at,updated_at) VALUES(?,?,?,'pending',?,?) ON CONFLICT(membership_request_id,assessment_kind) DO NOTHING`, id+"-openrouter", id, "openrouter", now, now)
			verification.AnalyzeAsync(context.Background(), s.analyzer, verification.AnalysisInput{EvidenceType: "application/octet-stream", Evidence: evidence}, func(result verification.AnalysisResult) {
				_, _ = s.controlDB.ExecContext(context.Background(), `UPDATE verification_assessments SET state=?,result_code=?,detail=?,updated_at=? WHERE membership_request_id=? AND assessment_kind='openrouter'`, result.Status, result.Confidence, result.Assessment, time.Now().UnixMilli(), id)
			})
		}
	}
	_ = WriteJSON(w, 202, map[string]string{"status": "received"})
}

func (s *Server) handleDeleteMembershipRequest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var ref string
	if err := s.controlDB.QueryRowContext(r.Context(), `SELECT evidence_reference FROM membership_requests WHERE id=?`, id).Scan(&ref); err != nil {
		WriteError(w, 404, "request not found")
		return
	}
	if ref != "" && s.evidenceDir != "" {
		_ = os.Remove(filepath.Join(s.evidenceDir, ref))
	}
	if _, err := s.controlDB.ExecContext(r.Context(), `DELETE FROM membership_requests WHERE id=?`, id); err != nil {
		WriteError(w, 500, "failed to delete request")
		return
	}
	_ = WriteJSON(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) handleVerifyEmail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RequestID string `json:"requestId"`
		Challenge string `json:"challenge"`
	}
	if ReadJSON(r, &req) != nil || req.RequestID == "" || req.Challenge == "" {
		WriteError(w, 400, "invalid verification")
		return
	}
	var hash string
	var expires int64
	var attempts int
	err := s.controlDB.QueryRowContext(r.Context(), `SELECT token_hash,expires_at,attempt_count FROM email_challenges WHERE membership_request_id=? AND verified_at IS NULL`, req.RequestID).Scan(&hash, &expires, &attempts)
	if err != nil || expires <= time.Now().UnixMilli() || attempts >= 5 || tokenHash(req.Challenge) != hash {
		if err == nil {
			_, _ = s.controlDB.ExecContext(r.Context(), `UPDATE email_challenges SET attempt_count=attempt_count+1 WHERE membership_request_id=?`, req.RequestID)
		}
		WriteError(w, 401, "invalid verification")
		return
	}
	tx, err := s.controlDB.BeginTx(r.Context(), nil)
	if err != nil {
		WriteError(w, 500, "verification unavailable")
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `UPDATE email_challenges SET verified_at=? WHERE membership_request_id=? AND verified_at IS NULL`, time.Now().UnixMilli(), req.RequestID); err != nil {
		WriteError(w, 500, "verification unavailable")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE membership_requests SET status='pending',verification_state='verified',updated_at=? WHERE id=? AND status='pending_email'`, time.Now().UnixMilli(), req.RequestID); err != nil {
		WriteError(w, 500, "verification unavailable")
		return
	}
	if err = tx.Commit(); err != nil {
		WriteError(w, 500, "verification unavailable")
		return
	}
	_ = WriteJSON(w, 200, map[string]string{"status": "verified"})
}

func (s *Server) handleResendEmail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RequestID string `json:"requestId"`
	}
	if ReadJSON(r, &req) != nil || req.RequestID == "" {
		WriteError(w, 400, "invalid request")
		return
	}
	var email, label string
	if err := s.controlDB.QueryRowContext(r.Context(), `SELECT r.applicant_work_email,p.label FROM membership_requests r JOIN verification_pipelines p ON p.id=r.pipeline_id WHERE r.id=? AND r.status='pending_email'`, req.RequestID).Scan(&email, &label); err != nil {
		WriteError(w, 202, "if eligible, a new challenge will be sent")
		return
	}
	challenge, hash, err := newBearerToken()
	if err != nil {
		WriteError(w, 202, "if eligible, a new challenge will be sent")
		return
	}
	now := time.Now().UnixMilli()
	_, _ = s.controlDB.ExecContext(r.Context(), `UPDATE email_challenges SET token_hash=?,expires_at=?,attempt_count=0,verified_at=NULL WHERE membership_request_id=?`, hash, time.Now().Add(24*time.Hour).UnixMilli(), req.RequestID)
	if s.mailer != nil {
		_ = s.mailer.Send(r.Context(), email, label, challenge)
	}
	_ = now
	_ = WriteJSON(w, 202, map[string]string{"status": "accepted"})
}
