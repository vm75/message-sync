package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/delivery"
	"github.com/vm75/message-sync/internal/store"
)

type fakeDeliveryStatus struct {
	statuses []delivery.EndpointStatus
}

func (f fakeDeliveryStatus) DeliveryStatus(context.Context) ([]delivery.EndpointStatus, error) {
	return f.statuses, nil
}

func TestDeliveryStatusRequiresAuthAndIsContentFree(t *testing.T) {
	syncStore, _ := store.Open(context.Background(), filepath.Join(t.TempDir(), "sync.db"))
	defer syncStore.Close()
	_, err := syncStore.DB().Exec(`INSERT INTO endpoints(alias, transport, remote_id) VALUES ('alpha', 'telegram', '-123456')`)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(Options{
		DB:     syncStore.DB(),
		Secret: []byte("01234567890123456789012345678901"),
		Delivery: fakeDeliveryStatus{statuses: []delivery.EndpointStatus{{
			EndpointID: "alpha", QueueDepth: 2, QueueCapacity: 32, LaneState: "retrying",
			Retrying: 1, Failed: 1, OldestActiveAge: 4 * time.Second, FailureClass: "rate_limited",
		}}},
	})

	unauthorized := httptest.NewRecorder()
	srv.Handler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/delivery/status", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	token, err := srv.sessions.CreateToken()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/delivery/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, forbidden := range []string{"-123456", "canonical-secret", "checkpoint", "message body", "raw provider error", "bot-token"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response contains forbidden value %q: %s", forbidden, body)
		}
	}
	for _, expected := range []string{"\"alpha\"", "\"retrying\"", "\"rate_limited\"", "\"oldestActiveAgeSeconds\":4"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("response missing %q: %s", expected, body)
		}
	}
}

func TestDeliveryStatusEmptyEndpointList(t *testing.T) {
	syncStore, _ := store.Open(context.Background(), filepath.Join(t.TempDir(), "sync.db"))
	defer syncStore.Close()
	srv := NewServer(Options{DB: syncStore.DB(), Delivery: fakeDeliveryStatus{}})
	token, err := srv.sessions.CreateToken()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/delivery/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"endpoints\":[]}\n" {
		t.Fatalf("empty response = %d %q", rec.Code, rec.Body.String())
	}
}
