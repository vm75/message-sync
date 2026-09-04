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
	body := `{"applicantInstructions":"Answer briefly","reviewerGuidance":"Check the evidence","evidenceRequired":true,"customFields":[{"key":"company","label":"Company","type":"text","required":true,"maxLength":120,"position":0}]}`
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
	if !out.EvidenceRequired || len(out.CustomFields) != 1 || out.CustomFields[0].Key != "company" {
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
	cfg := MembershipConfig{CustomFields: []MembershipCustomField{{Key: "company", Label: "Company", Type: "text", Required: true, MaxLength: 8, Position: 0}}}
	answers, err := validateMembershipAnswers(cfg, `{"company":"Acme"}`)
	if err != nil || answers["company"] != "Acme" {
		t.Fatalf("answers=%v err=%v", answers, err)
	}
	for _, raw := range []string{`{}`, `{"unknown":"x"}`, `{"company":"too-longer"}`} {
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
