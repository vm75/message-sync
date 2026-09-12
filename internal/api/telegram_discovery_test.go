package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	telegram "github.com/vm75/message-sync/internal/transport/telegram"
)

type mockTopicConnectionService struct {
	*mockConnectionService
	topics         any
	err            error
	seenConnection string
	seenRemote     string
}

func (m *mockTopicConnectionService) TelegramTopicDiscovery(_ context.Context, id, remoteID string) (any, error) {
	m.seenConnection, m.seenRemote = id, remoteID
	return m.topics, m.err
}

func TestTelegramTopicDiscoveryCapabilityAndModeIsolation(t *testing.T) {
	srv, _, controlDB, _, opToken := setupConnectionsTestEnv(t)
	mock := &mockTopicConnectionService{mockConnectionService: &mockConnectionService{}, topics: []telegram.DiscoveredTopic{{RemoteID: "1", General: true}, {RemoteID: "17", Label: "Plans"}}}
	srv.connections = mock

	req := httptest.NewRequest(http.MethodGet, "/api/connections/conn-tg-1/telegram/topics?remoteId=-1000000000042", nil)
	req.Header.Set("Authorization", "Bearer "+opToken)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "not supported") {
		t.Fatalf("bot topic discovery = %d %s", rec.Code, rec.Body.String())
	}

	if _, err := controlDB.Exec(`UPDATE transport_connections SET integration_mode='mtproto' WHERE id='conn-tg-1'`); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/connections/conn-tg-1/telegram/topics?remoteId=-1000000000042", nil)
	req.Header.Set("Authorization", "Bearer "+opToken)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"remoteId":"17"`) {
		t.Fatalf("MTProto topic discovery = %d %s", rec.Code, rec.Body.String())
	}
	if mock.seenConnection != "conn-tg-1" || mock.seenRemote != "-1000000000042" {
		t.Fatalf("wrong discovery scope: %q %q", mock.seenConnection, mock.seenRemote)
	}
}
