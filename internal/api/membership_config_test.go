package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/verification"
)

func TestMembershipConfigIsBoundedToSyncSet(t *testing.T) {
	db := setupTestDB(t)
	if _, err := db.Exec(`INSERT INTO sync_sets(id) VALUES ('community'); INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id) VALUES ('group','whatsapp','conn-wa-1','1@g.us','community')`); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(Options{DB: db})
	token, err := srv.sessions.CreateToken()
	if err != nil {
		t.Fatal(err)
	}
	body := `{"applicantInstructions":"Answer briefly","evidenceInstructions":"Upload proof","reviewerGuidance":"Check the evidence","evidenceRequired":true,"customFields":[{"key":"company","label":"Company","type":"text","required":true,"maxLength":120,"position":0},{"key":"status","label":"Status","type":"select","options":["New","Existing"],"position":1}]}`
	req := httptest.NewRequest(http.MethodPut, "/api/sync-sets/community/membership", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", rec.Code, rec.Body.String())
	}
	get := httptest.NewRequest(http.MethodGet, "/api/sync-sets/community/membership", nil)
	get.Header.Set("Authorization", "Bearer "+token)
	got := httptest.NewRecorder()
	srv.Handler().ServeHTTP(got, get)
	if got.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", got.Code, got.Body.String())
	}
	var out MembershipConfig
	if err := json.NewDecoder(got.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.EvidenceRequired || out.EvidenceInstructions != "Upload proof" || len(out.CustomFields) != 2 || out.CustomFields[1].Options[1] != "Existing" {
		t.Fatalf("config=%+v", out)
	}

	bad := httptest.NewRequest(http.MethodPut, "/api/sync-sets/community/membership", bytes.NewBufferString(`{"customFields":[{"key":"bad key","label":"Bad","type":"text"}]}`))
	bad.Header.Set("Authorization", "Bearer "+token)
	badRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(badRec, bad)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid field status=%d", badRec.Code)
	}
}

func TestMembershipAnswersAreStrictAndSnapshotSafe(t *testing.T) {
	cfg := MembershipConfig{CustomFields: []MembershipCustomField{{Key: "company", Label: "Company", Type: "text", Required: true, MaxLength: 8, Position: 0}, {Key: "status", Label: "Status", Type: "select", Options: []string{"New", "Existing"}, MaxLength: 20, Position: 1}, {Key: "terms", Label: "Terms", Type: "checkbox", Required: true, Position: 2}}}
	answers, err := validateMembershipAnswers(cfg, `{"company":"Acme"}`)
	if err == nil {
		t.Fatal("missing select/checkbox answers unexpectedly accepted")
	}
	answers, err = validateMembershipAnswers(cfg, `{"company":"Acme","status":"Existing","terms":true}`)
	if err != nil || answers["company"] != "Acme" {
		t.Fatalf("answers=%v err=%v", answers, err)
	}
	for _, raw := range []string{`{}`, `{"unknown":"x","status":"Existing","terms":true}`, `{"company":"too-longer","status":"Existing","terms":true}`, `{"company":"Acme","status":"Other","terms":true}`, `{"company":"Acme","status":"Existing","terms":"yes"}`} {
		if _, err := validateMembershipAnswers(cfg, raw); err == nil {
			t.Fatalf("answers %s unexpectedly accepted", raw)
		}
	}
	snapshot, err := marshalMembershipSnapshot(cfg)
	if err != nil || snapshot == "" {
		t.Fatalf("snapshot=%q err=%v", snapshot, err)
	}
}

type captureAnalyzer struct {
	inputs chan verification.AnalysisInput
}

func (a *captureAnalyzer) Analyze(_ context.Context, in verification.AnalysisInput) (verification.AnalysisResult, error) {
	a.inputs <- in
	return verification.AnalysisResult{Status: "complete", Confidence: "LOW", Assessment: "advisory"}, nil
}

func TestEvidenceMIMEReachesAnalyzerAndFinalDecisionPurgesIt(t *testing.T) {
	db := setupTestDB(t)
	if _, err := db.Exec(`INSERT INTO sync_sets(id) VALUES ('community'); INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id) VALUES ('group','whatsapp','conn-wa-1','1@g.us','community')`); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(Options{DB: db, EvidenceDir: t.TempDir(), Analyzer: &captureAnalyzer{inputs: make(chan verification.AnalysisInput, 1)}})
	if _, err := srv.controlDB.Exec(`INSERT INTO verification_pipelines(id,public_token,label,target_transport,endpoint_alias,enabled,creator_user_id,created_at,updated_at) VALUES ('pipeline','token','Community','whatsapp','group',1,'embedded-fixture',?,?)`, time.Now().UnixMilli(), time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("workEmail", "applicant@example.com")
	_ = writer.WriteField("identity", "+15551234567")
	part, err := writer.CreateFormFile("evidence", "proof.pdf")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(part, "%PDF-1.7 evidence")
	_ = writer.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/verification/token", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("intake status=%d body=%s", rec.Code, rec.Body.String())
	}
	analyzer := srv.analyzer.(*captureAnalyzer)
	select {
	case input := <-analyzer.inputs:
		if input.EvidenceType != "application/pdf" {
			t.Fatalf("evidence type=%q", input.EvidenceType)
		}
	case <-time.After(time.Second):
		t.Fatal("analyzer did not receive evidence")
	}
	var id, ref string
	if err := srv.controlDB.QueryRow(`SELECT id,evidence_reference FROM membership_requests WHERE pipeline_id='pipeline'`).Scan(&id, &ref); err != nil {
		t.Fatal(err)
	}
	if ref == "" {
		t.Fatal("missing evidence reference")
	}
	if _, err := srv.controlDB.Exec(`UPDATE membership_requests SET status='pending_admin' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	token, err := srv.sessions.CreateToken()
	if err != nil {
		t.Fatal(err)
	}
	decision := httptest.NewRequest(http.MethodPost, "/api/verification/requests/"+id+"/decision", bytes.NewBufferString(`{"action":"reject"}`))
	decision.Header.Set("Authorization", "Bearer "+token)
	decisionRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(decisionRec, decision)
	if decisionRec.Code != http.StatusOK {
		t.Fatalf("decision status=%d body=%s", decisionRec.Code, decisionRec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(srv.evidenceDir, ref)); !os.IsNotExist(err) {
		t.Fatalf("evidence still exists: %v", err)
	}
	var cleared sql.NullString
	if err := srv.controlDB.QueryRow(`SELECT evidence_reference FROM membership_requests WHERE id=?`, id).Scan(&cleared); err != nil {
		t.Fatal(err)
	}
	if cleared.Valid {
		t.Fatalf("evidence reference retained: %q", cleared.String)
	}
}

func TestMembershipJoinTokenIsHashedAndBoundToRequest(t *testing.T) {
	srv := NewServer(Options{})
	if _, err := srv.controlDB.Exec(`INSERT INTO verification_pipelines(id,public_token,label,target_transport,endpoint_alias,creator_user_id,created_at,updated_at) VALUES ('pipeline','token','Community','whatsapp','group','embedded-fixture',?,?); INSERT INTO membership_requests(id,pipeline_id,status,applicant_work_email,verification_state,created_at,updated_at) VALUES ('request','pipeline','approved','applicant@example.com','verified',?,?)`, time.Now().UnixMilli(), time.Now().UnixMilli(), time.Now().UnixMilli(), time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	plain, err := srv.issueMembershipJoinToken(context.Background(), "request")
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := srv.controlDB.QueryRow(`SELECT token_hash FROM membership_join_tokens WHERE membership_request_id='request'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == plain || stored == "" || tokenHash(plain) != stored {
		t.Fatalf("token storage is not hashed: %q", stored)
	}
}
