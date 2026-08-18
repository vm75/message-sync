package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/api"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/transport"
	whatsapp "github.com/vm75/message-sync/internal/transport/whatsapp"
)

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

type fakeWhatsAppTransport struct {
	events      chan transport.Incoming
	mu          sync.Mutex
	sent        []transport.Outgoing
	sentCount   int
	status      api.WhatsAppStatus
	pairResp    api.WhatsAppPairResponse
	pairCalled  int
	cancelCalls int
}

func (f *fakeWhatsAppTransport) Events() <-chan transport.Incoming { return f.events }
func (f *fakeWhatsAppTransport) Close() error                      { return nil }
func (f *fakeWhatsAppTransport) Send(_ context.Context, outgoing transport.Outgoing) (transport.MessageRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sentCount++
	f.sent = append(f.sent, outgoing)
	return transport.MessageRef{
		Endpoint:        outgoing.Endpoint,
		RemoteMessageID: fmt.Sprintf("sent-%s-%d", outgoing.Endpoint, f.sentCount),
	}, nil
}

func (f *fakeWhatsAppTransport) React(ctx context.Context, r transport.Reaction) error {
	return nil
}

func (f *fakeWhatsAppTransport) Edit(ctx context.Context, ref transport.MessageRef, text string) error {
	return nil
}

func (f *fakeWhatsAppTransport) Delete(ctx context.Context, ref transport.MessageRef) error {
	return nil
}

func (f *fakeWhatsAppTransport) Status(ctx context.Context) api.WhatsAppStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status.Status == "" {
		return api.WhatsAppStatus{Status: "unpaired"}
	}
	return f.status
}

func (f *fakeWhatsAppTransport) Pair(ctx context.Context) (api.WhatsAppPairResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pairCalled++
	if f.pairResp.Status == "" {
		return api.WhatsAppPairResponse{Status: "pairing", QRCode: "mock-qr", TimeoutSeconds: 20}, nil
	}
	return f.pairResp, nil
}

func (f *fakeWhatsAppTransport) CancelPair(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCalls++
	return nil
}

func TestRunRoutesWithoutPersistingProtocolPIIContentOrParticipantIdentity(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv("API_ADDR", "127.0.0.1:0")
	secret := "0123456789abcdef0123456789abcdef"
	t.Setenv("IDENTITY_SECRET", secret)

	cfg := &config.Config{
		Groups: map[string]config.Group{
			"c1g1": {JID: "123456789@g.us"},
			"c1g2": {JID: "987654321@g.us"},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Groups: []string{"c1g1", "c1g2"}}},
		Identity: config.Identity{UsernameMode: config.UsernameModePushName},
		Media:    config.Media{MaxSizeMB: 100},
		Recovery: config.Recovery{MaxAgeHours: 24, MaxMessagesPerGroup: 200},
		Storage:  config.Storage{MessageRetentionDays: 90},
	}

	originalOpen := openWhatsApp
	defer func() { openWhatsApp = originalOpen }()
	fake := &fakeWhatsAppTransport{}
	openWhatsApp = func(_ context.Context, opts whatsapp.Options) (whatsappTransport, error) {
		if err := os.WriteFile(opts.DatabasePath, []byte("sensitive protocol state: 123456789@g.us 15551234567 Alice Example private body"), 0o600); err != nil {
			return nil, err
		}
		fake.events = make(chan transport.Incoming, 1)
		fake.events <- transport.Incoming{
			Endpoint: "c1g1",
			RemoteID: "opaque-remote-id",
			Sender: transport.Sender{
				DisplayName: "Alice Example",
				OpaqueID:    "u_abcdefghij",
			},
			Kind: "text",
			Text: "private body",
		}
		return fake, nil
	}

	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, nil))
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, cfg, logger) }()

	syncPath := filepath.Join(dataDir, SyncDBName)
	for i := 0; i < 100; i++ {
		fake.mu.Lock()
		sentCount := len(fake.sent)
		fake.mu.Unlock()
		if _, err := os.Stat(syncPath); err == nil && strings.Contains(out.String(), "message_routed") && sentCount == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(syncPath); err != nil {
		cancel()
		t.Fatalf("sync.db missing: %v", err)
	}
	if !strings.Contains(out.String(), "message_routed") {
		cancel()
		t.Fatal("normalized event was not routed by the application")
	}

	fake.mu.Lock()
	if len(fake.sent) != 1 {
		fake.mu.Unlock()
		cancel()
		t.Fatalf("fan-out sent %d messages, want 1", len(fake.sent))
	}
	forwarded := fake.sent[0]
	fake.mu.Unlock()
	if forwarded.Endpoint != "c1g2" || forwarded.Text != "c1g1/Alice Example: private body" {
		cancel()
		t.Fatalf("unexpected forwarded message: %+v", forwarded)
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, WhatsAppDBName)); err != nil {
		t.Fatalf("whatsapp.db missing: %v", err)
	}

	forbiddenValues := []string{
		secret,
		"123456789@g.us",
		"987654321@g.us",
		"15551234567",
		"Alice Example",
		"private body",
		"u_abcdefghij",
	}
	logged := out.String()
	for _, forbidden := range forbiddenValues {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("log leaked forbidden value %q: %s", forbidden, logged)
		}
	}

	databaseBytes, err := os.ReadFile(syncPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range forbiddenValues {
		if strings.Contains(string(databaseBytes), forbidden) {
			t.Fatalf("sync.db leaked forbidden value %q", forbidden)
		}
	}
	for _, required := range []string{"c1g1", "c1g2", "opaque-remote-id", "sent-c1g2"} {
		if !strings.Contains(string(databaseBytes), required) {
			t.Fatalf("sync.db missing expected opaque routing value %q", required)
		}
	}
}

func TestRunStartupRetentionPruneAndMetricsLogging(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv("API_ADDR", "127.0.0.1:0")
	secret := "0123456789abcdef0123456789abcdef"
	t.Setenv("IDENTITY_SECRET", secret)

	cfg := &config.Config{
		Groups: map[string]config.Group{
			"c1g1": {JID: "123456789@g.us"},
			"c1g2": {JID: "987654321@g.us"},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Groups: []string{"c1g1", "c1g2"}}},
		Identity: config.Identity{UsernameMode: config.UsernameModePushName},
		Media:    config.Media{MaxSizeMB: 100},
		Recovery: config.Recovery{MaxAgeHours: 24, MaxMessagesPerGroup: 200},
		Storage:  config.Storage{MessageRetentionDays: 90},
	}

	// Pre-create sync.db with an expired canonical message (100 days old)
	syncPath := filepath.Join(dataDir, SyncDBName)
	preStore, err := store.Open(context.Background(), syncPath)
	if err != nil {
		t.Fatal(err)
	}
	expiredTime := time.Now().UTC().AddDate(0, 0, -100)
	if err := preStore.CreateCanonical(context.Background(), "expired-canonical", expiredTime); err != nil {
		t.Fatal(err)
	}
	if err := preStore.AddMessageCopy(context.Background(), store.MessageCopy{
		CanonicalID:     "expired-canonical",
		EndpointID:      "c1g1",
		RemoteMessageID: "expired-remote-1",
		CreatedAt:       expiredTime,
	}); err != nil {
		t.Fatal(err)
	}
	if err := preStore.Close(); err != nil {
		t.Fatal(err)
	}

	originalOpen := openWhatsApp
	defer func() { openWhatsApp = originalOpen }()
	fake := &fakeWhatsAppTransport{events: make(chan transport.Incoming)}
	openWhatsApp = func(_ context.Context, opts whatsapp.Options) (whatsappTransport, error) {
		return fake, nil
	}

	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, nil))
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, cfg, logger)
	}()

	// Wait briefly for startup retention prune and metrics logging
	for i := 0; i < 50; i++ {
		if strings.Contains(out.String(), "retention prune completed") && strings.Contains(out.String(), "storage metrics") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Run() returned error on graceful shutdown: %v", err)
	}

	logged := out.String()
	if !strings.Contains(logged, "retention prune completed") {
		t.Fatalf("expected log to contain retention prune completed: %s", logged)
	}
	if !strings.Contains(logged, "storage metrics") {
		t.Fatalf("expected log to contain storage metrics: %s", logged)
	}
	if !strings.Contains(logged, "message-sync stopping") {
		t.Fatalf("expected log to contain message-sync stopping: %s", logged)
	}

	// Verify that the expired message was deleted from sync.db during startup prune
	verifyStore, err := store.Open(context.Background(), syncPath)
	if err != nil {
		t.Fatal(err)
	}
	defer verifyStore.Close()
	metrics, err := verifyStore.Metrics(context.Background(), syncPath)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.CanonicalMessages != 0 || metrics.MessageCopies != 0 {
		t.Fatalf("expected 0 canonical messages after startup prune, got %+v", metrics)
	}
}

func TestRunLoadsConfigFromSyncDB(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv("API_ADDR", "127.0.0.1:0")
	secret := "0123456789abcdef0123456789abcdef"
	t.Setenv("IDENTITY_SECRET", secret)

	syncPath := filepath.Join(dataDir, SyncDBName)
	st, err := store.Open(context.Background(), syncPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Groups: map[string]config.Group{
			"c1g1": {JID: "123456789@g.us"},
			"c1g2": {JID: "987654321@g.us"},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Groups: []string{"c1g1", "c1g2"}}},
		Identity: config.Identity{UsernameMode: config.UsernameModePushName},
		Media:    config.Media{MaxSizeMB: 100},
		Recovery: config.Recovery{MaxAgeHours: 24, MaxMessagesPerGroup: 200},
		Storage:  config.Storage{MessageRetentionDays: 90},
	}
	if err := config.Save(context.Background(), st.DB(), cfg); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	originalOpen := openWhatsApp
	defer func() { openWhatsApp = originalOpen }()
	fake := &fakeWhatsAppTransport{events: make(chan transport.Incoming)}
	openWhatsApp = func(_ context.Context, opts whatsapp.Options) (whatsappTransport, error) {
		return fake, nil
	}

	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, nil))
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		// Pass nil cfg so Run loads from sync.db
		errCh <- Run(ctx, nil, logger)
	}()

	for i := 0; i < 50; i++ {
		if strings.Contains(out.String(), "message-sync started") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}

	if !strings.Contains(out.String(), "message-sync started") {
		t.Fatalf("expected log to contain message-sync started: %s", out.String())
	}
}

func TestRunWhatsAppAPIIntegration(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	apiAddr := "127.0.0.1:18099"
	t.Setenv("API_ADDR", apiAddr)
	secret := "0123456789abcdef0123456789abcdef"
	t.Setenv("IDENTITY_SECRET", secret)

	syncPath := filepath.Join(dataDir, SyncDBName)
	st, err := store.Open(context.Background(), syncPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Groups: map[string]config.Group{
			"c1g1": {JID: "123456789@g.us"},
			"c1g2": {JID: "987654321@g.us"},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Groups: []string{"c1g1", "c1g2"}}},
		Identity: config.Identity{UsernameMode: config.UsernameModePushName},
		Media:    config.Media{MaxSizeMB: 100},
		Recovery: config.Recovery{MaxAgeHours: 24, MaxMessagesPerGroup: 200},
		Storage:  config.Storage{MessageRetentionDays: 90},
	}
	if err := config.Save(context.Background(), st.DB(), cfg); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	originalOpen := openWhatsApp
	defer func() { openWhatsApp = originalOpen }()
	fake := &fakeWhatsAppTransport{
		events: make(chan transport.Incoming),
		status: api.WhatsAppStatus{
			Status:      "unpaired",
			IsLoggedIn:  false,
			IsConnected: false,
		},
		pairResp: api.WhatsAppPairResponse{
			Status:         "pairing",
			QRCode:         "2@test-qr-code",
			TimeoutSeconds: 25,
		},
	}
	openWhatsApp = func(_ context.Context, opts whatsapp.Options) (whatsappTransport, error) {
		return fake, nil
	}

	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, cfg, logger)
	}()

	// Wait for server to start
	client := &http.Client{Timeout: 2 * time.Second}
	var token string
	for i := 0; i < 50; i++ {
		resp, err := client.Post(fmt.Sprintf("http://%s/api/auth/setup", apiAddr), "application/json", strings.NewReader(`{"password":"testadminpassword123"}`))
		if err == nil {
			var tokenResp struct {
				Token string `json:"token"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&tokenResp)
			resp.Body.Close()
			if tokenResp.Token != "" {
				token = tokenResp.Token
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if token == "" {
		t.Fatal("failed to setup auth on running api server")
	}

	// 1. GET /api/whatsapp/status
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/api/whatsapp/status", apiAddr), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /api/whatsapp/status failed: %v", err)
	}
	var statusResp api.WhatsAppStatus
	_ = json.NewDecoder(resp.Body).Decode(&statusResp)
	resp.Body.Close()
	if statusResp.Status != "unpaired" || statusResp.IsLoggedIn {
		t.Fatalf("unexpected status response: %+v", statusResp)
	}

	// 2. POST /api/whatsapp/pair
	req, _ = http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/api/whatsapp/pair", apiAddr), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("POST /api/whatsapp/pair failed: %v", err)
	}
	var pairResp api.WhatsAppPairResponse
	_ = json.NewDecoder(resp.Body).Decode(&pairResp)
	resp.Body.Close()
	if pairResp.Status != "pairing" || pairResp.QRCode != "2@test-qr-code" || pairResp.TimeoutSeconds != 25 {
		t.Fatalf("unexpected pair response: %+v", pairResp)
	}

	// 3. DELETE /api/whatsapp/pair
	req, _ = http.NewRequest(http.MethodDelete, fmt.Sprintf("http://%s/api/whatsapp/pair", apiAddr), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("DELETE /api/whatsapp/pair failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from DELETE, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	if fake.cancelCalls != 1 {
		t.Fatalf("expected 1 cancel call, got %d", fake.cancelCalls)
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
}

func TestRunDynamicConfigUpdateViaAPI(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv("API_ADDR", "127.0.0.1:0")
	secret := "0123456789abcdef0123456789abcdef"
	t.Setenv("IDENTITY_SECRET", secret)

	cfg := &config.Config{
		Groups: map[string]config.Group{
			"c1g1": {JID: "123456789@g.us"},
			"c1g2": {JID: "987654321@g.us"},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Groups: []string{"c1g1", "c1g2"}}},
		Identity: config.Identity{UsernameMode: config.UsernameModePushName},
		Media:    config.Media{MaxSizeMB: 100},
		Recovery: config.Recovery{MaxAgeHours: 24, MaxMessagesPerGroup: 200},
		Storage:  config.Storage{MessageRetentionDays: 90},
	}

	syncPath := filepath.Join(dataDir, SyncDBName)
	st, err := store.Open(context.Background(), syncPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(context.Background(), st.DB(), cfg); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	fake := &fakeWhatsAppTransport{
		events: make(chan transport.Incoming, 10),
	}

	origOpen := openWhatsApp
	openWhatsApp = func(ctx context.Context, opts whatsapp.Options) (whatsappTransport, error) {
		return fake, nil
	}
	defer func() { openWhatsApp = origOpen }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var logBuf safeBuffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, cfg, logger)
	}()

	var apiAddr string
	for i := 0; i < 50; i++ {
		logs := logBuf.String()
		if strings.Contains(logs, "api server listening") {
			for _, line := range strings.Split(logs, "\n") {
				if strings.Contains(line, "api server listening") && strings.Contains(line, "addr=") {
					parts := strings.Split(line, "addr=")
					if len(parts) > 1 {
						apiAddr = strings.Trim(strings.Fields(parts[1])[0], "\"")
						break
					}
				}
			}
			if apiAddr != "" {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if apiAddr == "" {
		t.Fatal("failed to find api server listening address in logs")
	}

	client := &http.Client{Timeout: 2 * time.Second}
	var token string
	for i := 0; i < 50; i++ {
		resp, err := client.Post(fmt.Sprintf("http://%s/api/auth/setup", apiAddr), "application/json", strings.NewReader(`{"password":"testadminpassword123"}`))
		if err == nil {
			var tokenResp struct {
				Token string `json:"token"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&tokenResp)
			resp.Body.Close()
			if tokenResp.Token != "" {
				token = tokenResp.Token
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if token == "" {
		t.Fatal("failed to setup auth on running api server")
	}

	// 1. Initial message routing: c1g1 -> only c1g2 (push name)
	fake.events <- transport.Incoming{
		Endpoint: "c1g1",
		RemoteID: "msg-1",
		Sender: transport.Sender{
			DisplayName: "Alice",
			PhoneNumber: "15551234567",
			OpaqueID:    "u_alice1234",
		},
		Kind:      "text",
		Text:      "Initial message",
		Timestamp: time.Now().UTC(),
	}

	time.Sleep(100 * time.Millisecond)
	fake.mu.Lock()
	if len(fake.sent) != 1 || fake.sent[0].Endpoint != "c1g2" {
		t.Fatalf("expected initial message to route only to c1g2, got %+v", fake.sent)
	}
	if !strings.Contains(fake.sent[0].Text, "Alice") {
		t.Fatalf("expected push name Alice in forwarded text, got %q", fake.sent[0].Text)
	}
	fake.sent = nil
	fake.mu.Unlock()

	// 2. Add group c1g3 via POST /api/groups
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/api/groups", apiAddr), strings.NewReader(`{"alias":"c1g3","jid":"333333333@g.us"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/groups failed: err=%v, code=%d", err, resp.StatusCode)
	}
	resp.Body.Close()

	// 3. Update sync-set mesh via PUT /api/sync-sets/mesh to include [c1g1, c1g2, c1g3]
	req, _ = http.NewRequest(http.MethodPut, fmt.Sprintf("http://%s/api/sync-sets/mesh", apiAddr), strings.NewReader(`{"groups":["c1g1","c1g2","c1g3"]}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/sync-sets/mesh failed: err=%v, code=%d", err, resp.StatusCode)
	}
	resp.Body.Close()

	// 4. Update global config via PUT /api/config to set usernameMode to "hash"
	req, _ = http.NewRequest(http.MethodPut, fmt.Sprintf("http://%s/api/config", apiAddr), strings.NewReader(`{"usernameMode":"hash"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/config failed: err=%v, code=%d", err, resp.StatusCode)
	}
	resp.Body.Close()

	// 5. Send message on c1g1 -> should now route to BOTH c1g2 and c1g3 with hash format
	fake.events <- transport.Incoming{
		Endpoint: "c1g1",
		RemoteID: "msg-2",
		Sender: transport.Sender{
			DisplayName: "Alice",
			PhoneNumber: "15551234567",
			OpaqueID:    "u_alice1234",
		},
		Kind:      "text",
		Text:      "Updated dynamic routing message",
		Timestamp: time.Now().UTC(),
	}

	time.Sleep(100 * time.Millisecond)
	fake.mu.Lock()
	if len(fake.sent) != 2 {
		t.Fatalf("expected 2 forwarded messages after dynamic update, got %d", len(fake.sent))
	}
	endpoints := map[transport.EndpointID]bool{
		fake.sent[0].Endpoint: true,
		fake.sent[1].Endpoint: true,
	}
	if !endpoints["c1g2"] || !endpoints["c1g3"] {
		t.Fatalf("expected messages to c1g2 and c1g3, got %+v", fake.sent)
	}
	if strings.Contains(fake.sent[0].Text, "Alice") {
		t.Fatalf("expected hash mode without push name Alice, got %q", fake.sent[0].Text)
	}
	fake.mu.Unlock()

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
}

func TestApp_WebUIServing(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("DATA_DIR", dataDir)
	t.Setenv("API_ADDR", "127.0.0.1:0")
	secret := "0123456789abcdef0123456789abcdef"
	t.Setenv("IDENTITY_SECRET", secret)

	cfg := &config.Config{
		Groups: map[string]config.Group{
			"c1g1": {JID: "123456789@g.us"},
			"c1g2": {JID: "987654321@g.us"},
		},
		SyncSets: []config.SyncSet{{ID: "mesh", Groups: []string{"c1g1", "c1g2"}}},
		Identity: config.Identity{UsernameMode: config.UsernameModePushName},
		Media:    config.Media{MaxSizeMB: 100},
		Recovery: config.Recovery{MaxAgeHours: 24, MaxMessagesPerGroup: 200},
		Storage:  config.Storage{MessageRetentionDays: 90},
	}

	syncPath := filepath.Join(dataDir, SyncDBName)
	st, err := store.Open(context.Background(), syncPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(context.Background(), st.DB(), cfg); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()

	fake := &fakeWhatsAppTransport{
		events: make(chan transport.Incoming, 10),
	}

	origOpen := openWhatsApp
	openWhatsApp = func(ctx context.Context, opts whatsapp.Options) (whatsappTransport, error) {
		return fake, nil
	}
	defer func() { openWhatsApp = origOpen }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var logBuf safeBuffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, cfg, logger)
	}()

	var apiAddr string
	for i := 0; i < 50; i++ {
		logs := logBuf.String()
		if strings.Contains(logs, "api server listening") {
			for _, line := range strings.Split(logs, "\n") {
				if strings.Contains(line, "api server listening") && strings.Contains(line, "addr=") {
					parts := strings.Split(line, "addr=")
					if len(parts) > 1 {
						apiAddr = strings.Trim(strings.Fields(parts[1])[0], "\"")
						break
					}
				}
			}
			if apiAddr != "" {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if apiAddr == "" {
		t.Fatal("failed to find api server listening address in logs")
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://%s/", apiAddr))
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from UI, got %d", resp.StatusCode)
	}

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	if !strings.Contains(buf.String(), "Message Sync • Admin Console") {
		t.Errorf("expected HTML title in response, got: %s", buf.String())
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Run() returned error: %v", err)
	}
}
