package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
)

type mtprotoAPITestService struct {
	testConnectionService
	configured int
	codes      int
	submitted  int
	passwords  int
	loggedOut  int
}

func (s *mtprotoAPITestService) TelegramMTProtoConfigure(context.Context, string, int, string, string) (any, error) {
	s.configured++
	return map[string]any{"status": "code_required"}, nil
}
func (s *mtprotoAPITestService) TelegramMTProtoSendCode(context.Context, string) (any, error) {
	s.codes++
	return map[string]any{"status": "code_required"}, nil
}
func (s *mtprotoAPITestService) TelegramMTProtoSubmitCode(context.Context, string, string) (any, error) {
	s.submitted++
	return map[string]any{"status": "password_required"}, nil
}
func (s *mtprotoAPITestService) TelegramMTProtoSubmitPassword(context.Context, string, []byte) (any, error) {
	s.passwords++
	return map[string]any{"status": "connected"}, nil
}
func (s *mtprotoAPITestService) TelegramMTProtoLogout(context.Context, string) error {
	s.loggedOut++
	return nil
}
func TestMTProtoAdminAPIFlow(t *testing.T) {
	srv, _, db, token, _ := setupConnectionsTestEnv(t)
	var logs bytes.Buffer
	srv.logger = slog.New(slog.NewTextHandler(&logs, nil))
	_, err := db.Exec(`INSERT INTO transport_connections(id,transport,integration_mode,label,enabled,encrypted_credential,credential_nonce,created_at,updated_at) VALUES('conn-mt-api','telegram','mtproto','mt',1,x'01',x'02',1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	svc := &mtprotoAPITestService{}
	srv.connections = svc
	tests := []struct {
		path, body string
		want       string
	}{{"/api/connections/conn-mt-api/telegram/mtproto/setup", `{"apiId":123,"apiHash":"super-secret","phone":"+15551234567"}`, "code_required"}, {"/api/connections/conn-mt-api/telegram/mtproto/send-code", "", "code_required"}, {"/api/connections/conn-mt-api/telegram/mtproto/code", `{"code":"12345"}`, "password_required"}, {"/api/connections/conn-mt-api/telegram/mtproto/password", `{"password":"top-secret"}`, "connected"}}
	for _, tc := range tests {
		rec := authenticatedConnectionRequest(t, srv, token, http.MethodPost, tc.path, tc.body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s => %d %s", tc.path, rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out["status"] != tc.want {
			t.Fatalf("%s status=%v", tc.path, out["status"])
		}
		body := rec.Body.String()
		if containsString(body, "super-secret") || containsString(body, "+1555") || containsString(body, "top-secret") || containsString(body, "12345") {
			t.Fatalf("secret reflected in response %q", body)
		}
	}
	logText := logs.String()
	for _, secret := range []string{"super-secret", "+15551234567", "top-secret", "12345"} {
		if containsString(logText, secret) {
			t.Fatalf("MTProto auth secret leaked to logs: %q", secret)
		}
	}
}
func containsString(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
