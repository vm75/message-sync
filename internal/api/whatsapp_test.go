package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type mockWhatsAppService struct {
	mu          sync.Mutex
	status      WhatsAppStatus
	pairResp    WhatsAppPairResponse
	pairErr     error
	cancelErr   error
	logoutErr   error
	groups      []WhatsAppGroup
	groupsErr   error
	pairCalled  int
	cancelCalls int
	logoutCalls int
}

func (m *mockWhatsAppService) Status(_ context.Context) WhatsAppStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *mockWhatsAppService) Pair(_ context.Context) (WhatsAppPairResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pairCalled++
	if m.pairErr != nil {
		return WhatsAppPairResponse{}, m.pairErr
	}
	return m.pairResp, nil
}

func (m *mockWhatsAppService) CancelPair(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelCalls++
	return m.cancelErr
}

func (m *mockWhatsAppService) Logout(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logoutCalls++
	return m.logoutErr
}

func (m *mockWhatsAppService) GetJoinedGroups(_ context.Context) ([]WhatsAppGroup, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.groupsErr != nil {
		return nil, m.groupsErr
	}
	return m.groups, nil
}

func TestWhatsAppEndpoints_Unauthorized(t *testing.T) {
	mockWA := &mockWhatsAppService{
		status: WhatsAppStatus{Status: "unpaired"},
	}
	srv := NewServer(Options{
		Secret:   []byte("12345678901234567890123456789012"),
		WhatsApp: mockWA,
	})

	// Test GET /api/whatsapp/status without token
	req := httptest.NewRequest(http.MethodGet, "/api/whatsapp/status", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", rec.Code)
	}

	// Test POST /api/whatsapp/pair without token
	req = httptest.NewRequest(http.MethodPost, "/api/whatsapp/pair", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", rec.Code)
	}

	// Test DELETE /api/whatsapp/pair without token
	req = httptest.NewRequest(http.MethodDelete, "/api/whatsapp/pair", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", rec.Code)
	}

	// Test POST /api/whatsapp/logout without token
	req = httptest.NewRequest(http.MethodPost, "/api/whatsapp/logout", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", rec.Code)
	}
}

func TestWhatsAppEndpoints_Authorized(t *testing.T) {
	mockWA := &mockWhatsAppService{
		status: WhatsAppStatus{
			Status:      "unpaired",
			IsLoggedIn:  false,
			IsConnected: false,
		},
		pairResp: WhatsAppPairResponse{
			Status:         "pairing",
			QRCode:         "2@mock-qr-code",
			TimeoutSeconds: 20,
		},
	}
	srv := NewServer(Options{
		Secret:   []byte("12345678901234567890123456789012"),
		WhatsApp: mockWA,
	})

	token, err := srv.sessions.CreateToken()
	if err != nil {
		t.Fatalf("failed to create session token: %v", err)
	}

	// 1. GET /api/whatsapp/status (initial unpaired state)
	req := httptest.NewRequest(http.MethodGet, "/api/whatsapp/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	var status WhatsAppStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("failed to unmarshal status response: %v", err)
	}
	if status.Status != "unpaired" || status.IsLoggedIn || status.IsConnected {
		t.Fatalf("unexpected status: %+v", status)
	}

	// 2. POST /api/whatsapp/pair
	req = httptest.NewRequest(http.MethodPost, "/api/whatsapp/pair", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	var pairResp WhatsAppPairResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &pairResp); err != nil {
		t.Fatalf("failed to unmarshal pair response: %v", err)
	}
	if pairResp.Status != "pairing" || pairResp.QRCode != "2@mock-qr-code" || pairResp.TimeoutSeconds != 20 {
		t.Fatalf("unexpected pair response: %+v", pairResp)
	}

	// 3. DELETE /api/whatsapp/pair
	req = httptest.NewRequest(http.MethodDelete, "/api/whatsapp/pair", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	if mockWA.cancelCalls != 1 {
		t.Fatalf("expected cancel calls 1, got %d", mockWA.cancelCalls)
	}

	// 4. Update status to connected and verify
	mockWA.mu.Lock()
	mockWA.status = WhatsAppStatus{
		Status:      "connected",
		IsLoggedIn:  true,
		IsConnected: true,
	}
	mockWA.mu.Unlock()

	req = httptest.NewRequest(http.MethodGet, "/api/whatsapp/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("failed to unmarshal status: %v", err)
	}
	if status.Status != "connected" || !status.IsLoggedIn || !status.IsConnected {
		t.Fatalf("unexpected connected status: %+v", status)
	}
}

func TestWhatsAppEndpoints_ServiceUnavailable(t *testing.T) {
	srv := NewServer(Options{
		Secret:   []byte("12345678901234567890123456789012"),
		WhatsApp: nil,
	})
	token, _ := srv.sessions.CreateToken()

	req := httptest.NewRequest(http.MethodGet, "/api/whatsapp/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/whatsapp/pair", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/whatsapp/pair", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestWhatsAppEndpoints_PairError(t *testing.T) {
	mockWA := &mockWhatsAppService{
		pairErr: errors.New("network error"),
	}
	srv := NewServer(Options{
		Secret:   []byte("12345678901234567890123456789012"),
		WhatsApp: mockWA,
	})
	token, _ := srv.sessions.CreateToken()

	req := httptest.NewRequest(http.MethodPost, "/api/whatsapp/pair", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestWhatsAppEndpoints_GetJoinedGroups(t *testing.T) {
	mockWA := &mockWhatsAppService{
		groups: []WhatsAppGroup{
			{JID: "120363012345678901@g.us", Name: "Team Alpha"},
			{JID: "120363098765432100@g.us", Name: "Team Beta"},
		},
	}
	srv := NewServer(Options{
		Secret:   []byte("12345678901234567890123456789012"),
		WhatsApp: mockWA,
	})
	token, _ := srv.sessions.CreateToken()

	// Unauthorized test
	unauthReq := httptest.NewRequest(http.MethodGet, "/api/whatsapp/groups", nil)
	unauthRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", unauthRec.Code)
	}

	// Authorized success test
	req := httptest.NewRequest(http.MethodGet, "/api/whatsapp/groups", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	var res []WhatsAppGroup
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to unmarshal groups: %v", err)
	}
	if len(res) != 2 || res[0].Name != "Team Alpha" || res[1].JID != "120363098765432100@g.us" {
		t.Fatalf("unexpected groups response: %+v", res)
	}

	// Error test
	mockWA.mu.Lock()
	mockWA.groupsErr = errors.New("client disconnected")
	mockWA.mu.Unlock()

	req = httptest.NewRequest(http.MethodGet, "/api/whatsapp/groups", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestWhatsAppEndpoints_Logout(t *testing.T) {
	mockWA := &mockWhatsAppService{}
	srv := NewServer(Options{
		Secret:   []byte("12345678901234567890123456789012"),
		WhatsApp: mockWA,
	})
	token, _ := srv.sessions.CreateToken()

	// Successful logout
	req := httptest.NewRequest(http.MethodPost, "/api/whatsapp/logout", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	var res map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if res["status"] != "unpaired" {
		t.Fatalf("expected status 'unpaired', got %q", res["status"])
	}
	if mockWA.logoutCalls != 1 {
		t.Fatalf("expected 1 logout call, got %d", mockWA.logoutCalls)
	}

	// Error during logout
	mockWA.mu.Lock()
	mockWA.logoutErr = errors.New("logout failed")
	mockWA.mu.Unlock()

	req = httptest.NewRequest(http.MethodPost, "/api/whatsapp/logout", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}

	// Service unavailable
	srvNoWA := NewServer(Options{
		Secret: []byte("12345678901234567890123456789012"),
	})
	req = httptest.NewRequest(http.MethodPost, "/api/whatsapp/logout", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	srvNoWA.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for a token unknown to this server, got %d", rec.Code)
	}
}
