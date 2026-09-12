package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type mockBackfillConnectionService struct {
	*mockConnectionService
	calls        int
	id, endpoint string
	max          int
	age          time.Duration
}

func (m *mockBackfillConnectionService) TelegramHistoricalBackfill(_ context.Context, id, endpoint string, max int, age time.Duration) error {
	m.calls++
	m.id = id
	m.endpoint = endpoint
	m.max = max
	m.age = age
	return nil
}
func TestTelegramHistoricalBackfillRequiresMTProtoAndBounds(t *testing.T) {
	srv, _, db, admin, _ := setupConnectionsTestEnv(t)
	m := &mockBackfillConnectionService{mockConnectionService: &mockConnectionService{}}
	srv.connections = m
	req := httptest.NewRequest(http.MethodPost, "/api/connections/conn-tg-1/telegram/backfill", strings.NewReader(`{"endpoint":"tg","maxEvents":10,"maxAgeHours":24}`))
	req.Header.Set("Authorization", "Bearer "+admin)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("bot status=%d %s", rec.Code, rec.Body.String())
	}
	if _, err := db.Exec(`UPDATE transport_connections SET integration_mode='mtproto' WHERE id='conn-tg-1'`); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/connections/conn-tg-1/telegram/backfill", strings.NewReader(`{"endpoint":"tg","maxEvents":10,"maxAgeHours":24}`))
	req.Header.Set("Authorization", "Bearer "+admin)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("mtproto status=%d %s", rec.Code, rec.Body.String())
	}
	if m.calls != 1 || m.id != "conn-tg-1" || m.endpoint != "tg" || m.max != 10 || m.age != 24*time.Hour {
		t.Fatalf("call %+v", m)
	}
}
