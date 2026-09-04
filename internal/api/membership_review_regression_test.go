package api

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type captureMembershipMailer struct {
	values []string
}

func (m *captureMembershipMailer) Send(_ context.Context, _, _, value string) error {
	m.values = append(m.values, value)
	return nil
}

func TestSyncSetMembershipConfigSurvivesUpdateAndIsDeletedWithSyncSet(t *testing.T) {
	db := setupTestDB(t)
	if _, err := db.Exec(`INSERT INTO sync_sets(id) VALUES ('community'); INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id) VALUES ('group','whatsapp','conn-wa-1','1@g.us','community')`); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(Options{DB: db})
	now := time.Now().UnixMilli()
	if _, err := srv.controlDB.Exec(`INSERT INTO membership_configs(sync_set_id,applicant_instructions,evidence_instructions,reviewer_guidance,evidence_required,updated_by_user_id,created_at,updated_at) VALUES ('community','keep me','','',0,'embedded-fixture',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	token, err := srv.sessions.CreateToken()
	if err != nil {
		t.Fatal(err)
	}

	update := httptest.NewRequest(http.MethodPut, "/api/sync-sets/community", bytes.NewBufferString(`{"endpoints":["group"]}`))
	update.Header.Set("Authorization", "Bearer "+token)
	updateRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(updateRec, update)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", updateRec.Code, updateRec.Body.String())
	}
	var instructions string
	if err := srv.controlDB.QueryRow(`SELECT applicant_instructions FROM membership_configs WHERE sync_set_id='community'`).Scan(&instructions); err != nil || instructions != "keep me" {
		t.Fatalf("membership config lost after sync-set update: instructions=%q err=%v", instructions, err)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/sync-sets/community", nil)
	deleteReq.Header.Set("Authorization", "Bearer "+token)
	deleteRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	var count int
	if err := srv.controlDB.QueryRow(`SELECT COUNT(*) FROM membership_configs WHERE sync_set_id='community'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale membership config remains after sync-set delete: count=%d err=%v", count, err)
	}
}

func TestOutstandingMembershipRequestBlocksPipelineAndEndpointRetarget(t *testing.T) {
	db := setupTestDB(t)
	if _, err := db.Exec(`
		INSERT INTO sync_sets(id) VALUES ('community');
		INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id) VALUES
			('group-a','whatsapp','conn-wa-1','1@g.us','community'),
			('group-b','whatsapp','conn-wa-1','2@g.us','community')
	`); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(Options{DB: db})
	now := time.Now().UnixMilli()
	if _, err := srv.controlDB.Exec(`
		INSERT INTO verification_pipelines(id,public_token,label,target_transport,endpoint_alias,enabled,creator_user_id,created_at,updated_at) VALUES ('pipeline','token','Community','whatsapp','group-a',1,'embedded-fixture',?,?);
		INSERT INTO membership_requests(id,pipeline_id,status,applicant_work_email,applicant_whatsapp_phone,verification_state,created_at,updated_at) VALUES ('request','pipeline','pending_admin','applicant@example.com','+15551234567','verified',?,?)
	`, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	token, err := srv.sessions.CreateToken()
	if err != nil {
		t.Fatal(err)
	}
	auth := "Bearer " + token

	pipelineReq := httptest.NewRequest(http.MethodPut, "/api/verification/pipelines/pipeline", bytes.NewBufferString(`{"label":"Community","transport":"whatsapp","endpointAlias":"group-b","discordRoleId":"","enabled":true}`))
	pipelineReq.Header.Set("Authorization", auth)
	pipelineRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(pipelineRec, pipelineReq)
	if pipelineRec.Code != http.StatusConflict {
		t.Fatalf("pipeline retarget status=%d body=%s", pipelineRec.Code, pipelineRec.Body.String())
	}

	endpointReq := httptest.NewRequest(http.MethodPut, "/api/endpoints/group-a", bytes.NewBufferString(`{"transport":"whatsapp","connectionId":"conn-wa-1","remoteId":"9@g.us","syncSetId":"community"}`))
	endpointReq.Header.Set("Authorization", auth)
	endpointRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(endpointRec, endpointReq)
	if endpointRec.Code != http.StatusConflict {
		t.Fatalf("endpoint retarget status=%d body=%s", endpointRec.Code, endpointRec.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/endpoints/group-a", nil)
	deleteReq.Header.Set("Authorization", auth)
	deleteRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusConflict {
		t.Fatalf("endpoint delete status=%d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
}

func TestMembershipJoinURLIsAbsolute(t *testing.T) {
	t.Setenv("VERIFICATION_PUBLIC_BASE_URL", "")
	req := httptest.NewRequest(http.MethodPost, "https://bridge.example/api/verification/requests/request/fulfill", nil)
	got, err := membershipJoinURL(req, "opaque-token")
	if err != nil || got != "https://bridge.example/api/verification/join/opaque-token" {
		t.Fatalf("join URL=%q err=%v", got, err)
	}

	t.Setenv("VERIFICATION_PUBLIC_BASE_URL", "https://public.example/membership/")
	got, err = membershipJoinURL(req, "opaque-token")
	if err != nil || got != "https://public.example/membership/api/verification/join/opaque-token" {
		t.Fatalf("configured join URL=%q err=%v", got, err)
	}
}

func TestReconciliationDoesNotReissueOrReemailJoinToken(t *testing.T) {
	t.Setenv("VERIFICATION_PUBLIC_BASE_URL", "")
	db := setupTestDB(t)
	if _, err := db.Exec(`INSERT INTO sync_sets(id) VALUES ('community'); INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id) VALUES ('group','whatsapp','conn-wa-1','1@g.us','community')`); err != nil {
		t.Fatal(err)
	}
	mailer := &captureMembershipMailer{}
	srv := NewServer(Options{DB: db, Mailer: mailer})
	now := time.Now().UnixMilli()
	if _, err := srv.controlDB.Exec(`
		INSERT INTO verification_pipelines(id,public_token,label,target_transport,endpoint_alias,enabled,creator_user_id,created_at,updated_at) VALUES ('pipeline','token','Community','whatsapp','group',1,'embedded-fixture',?,?);
		INSERT INTO membership_requests(id,pipeline_id,status,applicant_work_email,applicant_whatsapp_phone,verification_state,fulfillment_state,fulfillment_failure_class,created_at,updated_at) VALUES ('request','pipeline','approved','applicant@example.com','+15551234567','verified','action_pending','invite_pending',?,?)
	`, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	httpReq := httptest.NewRequest(http.MethodPost, "https://bridge.example/api/verification/requests/request/fulfill", nil)
	if failureClass := srv.sendMembershipJoinInvite(httpReq, "request", "applicant@example.com", "Community"); failureClass != "" {
		t.Fatalf("invite send failed: %s", failureClass)
	}
	if len(mailer.values) != 1 || !strings.HasPrefix(mailer.values[0], "https://bridge.example/api/verification/join/") {
		t.Fatalf("unexpected sent values: %#v", mailer.values)
	}
	var originalHash string
	if err := srv.controlDB.QueryRow(`SELECT token_hash FROM membership_join_tokens WHERE membership_request_id='request' AND revoked_at IS NULL`).Scan(&originalHash); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	srv.ReconcileMembership(ctx)
	if ctx.Err() != nil {
		t.Fatalf("reconciliation did not complete: %v", ctx.Err())
	}
	if len(mailer.values) != 1 {
		t.Fatalf("reconciliation resent invite: %#v", mailer.values)
	}
	var tokenCount int
	if err := srv.controlDB.QueryRow(`SELECT COUNT(*) FROM membership_join_tokens WHERE membership_request_id='request'`).Scan(&tokenCount); err != nil || tokenCount != 1 {
		t.Fatalf("token count=%d err=%v", tokenCount, err)
	}
	var afterHash string
	var revoked sql.NullInt64
	if err := srv.controlDB.QueryRow(`SELECT token_hash,revoked_at FROM membership_join_tokens WHERE membership_request_id='request'`).Scan(&afterHash, &revoked); err != nil {
		t.Fatal(err)
	}
	if afterHash != originalHash || revoked.Valid {
		t.Fatalf("reconciliation rotated/revoked token: before=%q after=%q revoked=%v", originalHash, afterHash, revoked)
	}
}

func TestTerminalDecisionQueuesFailedEvidencePurgeForRetry(t *testing.T) {
	db := setupTestDB(t)
	evidenceDir := t.TempDir()
	srv := NewServer(Options{DB: db, EvidenceDir: evidenceDir})
	now := time.Now().UnixMilli()
	if _, err := srv.controlDB.Exec(`
		INSERT INTO verification_pipelines(id,public_token,label,target_transport,endpoint_alias,enabled,creator_user_id,created_at,updated_at) VALUES ('pipeline','token','Community','whatsapp','group',1,'embedded-fixture',?,?);
		INSERT INTO membership_requests(id,pipeline_id,status,applicant_work_email,verification_state,evidence_reference,evidence_metadata,created_at,updated_at) VALUES ('request','pipeline','pending_admin','applicant@example.com','verified','evidence-ref','application/pdf',?,?)
	`, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	blockedPath := filepath.Join(evidenceDir, "evidence-ref")
	if err := os.Mkdir(blockedPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blockedPath, "child"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := srv.sessions.CreateToken()
	if err != nil {
		t.Fatal(err)
	}
	decision := httptest.NewRequest(http.MethodPost, "/api/verification/requests/request/decision", bytes.NewBufferString(`{"action":"reject"}`))
	decision.Header.Set("Authorization", "Bearer "+token)
	decisionRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(decisionRec, decision)
	if decisionRec.Code != http.StatusOK {
		t.Fatalf("decision status=%d body=%s", decisionRec.Code, decisionRec.Body.String())
	}
	var ref sql.NullString
	if err := srv.controlDB.QueryRow(`SELECT evidence_reference FROM membership_requests WHERE id='request'`).Scan(&ref); err != nil || ref.Valid {
		t.Fatalf("evidence reference was not cleared atomically: ref=%v err=%v", ref, err)
	}
	var queued int
	if err := srv.controlDB.QueryRow(`SELECT COUNT(*) FROM evidence_cleanup_queue WHERE evidence_reference='evidence-ref'`).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("cleanup queue count=%d err=%v", queued, err)
	}
	if _, err := os.Stat(blockedPath); err != nil {
		t.Fatalf("expected failed unlink artifact to remain for retry: %v", err)
	}

	if err := os.Remove(filepath.Join(blockedPath, "child")); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.controlDB.Exec(`UPDATE evidence_cleanup_queue SET next_attempt_at=0 WHERE evidence_reference='evidence-ref'`); err != nil {
		t.Fatal(err)
	}
	srv.cleanupMembershipEvidence(context.Background(), 25)
	if _, err := os.Stat(blockedPath); !os.IsNotExist(err) {
		t.Fatalf("queued evidence was not removed on retry: %v", err)
	}
	if err := srv.controlDB.QueryRow(`SELECT COUNT(*) FROM evidence_cleanup_queue WHERE evidence_reference='evidence-ref'`).Scan(&queued); err != nil || queued != 0 {
		t.Fatalf("cleanup queue not cleared after success: count=%d err=%v", queued, err)
	}
}
