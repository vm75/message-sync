package whatsapp

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"go.mau.fi/whatsmeow"
)

func TestDisablePlaintextPersistence(t *testing.T) {
	client := &whatsmeow.Client{
		EnableDecryptedEventBuffer: true,
		UseRetryMessageStore:       true,
	}
	disablePlaintextPersistence(client)
	if client.EnableDecryptedEventBuffer {
		t.Fatal("decrypted event buffer must be disabled")
	}
	if client.UseRetryMessageStore {
		t.Fatal("retry plaintext message store must be disabled")
	}
}

func TestOpenSessionStoreCreatesRestrictedWhatsAppDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "whatsapp.db")
	container, device, err := openSessionStore(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if device == nil {
		_ = container.Close()
		t.Fatal("device store is nil")
	}
	if err := container.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("whatsapp.db mode = %o, want 600", got)
	}
}

func TestAdapterUnauthenticatedStartupDoesNotBlock(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "whatsapp.db")

	secret := []byte("0123456789abcdef0123456789abcdef")
	hasher, err := identity.New(secret)
	if err != nil {
		t.Fatal(err)
	}

	opts := Options{
		DatabasePath: dbPath,
		GroupJIDs: map[string]string{
			"g1": "123456789@g.us",
		},
		Hasher:           hasher,
		UsernameMode:     config.UsernameModePushName,
		Logger:           slog.Default(),
		EnableTerminalQR: false,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	adapter, err := Open(ctx, opts)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer adapter.Close()

	status := adapter.Status(context.Background())
	if status.Status != "unpaired" {
		t.Fatalf("expected status unpaired, got %s", status.Status)
	}
	if status.IsLoggedIn || status.IsConnected {
		t.Fatalf("expected not logged in and not connected: %+v", status)
	}
}

func TestAdapterConsumeQREventsAndLifecycle(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))

	adapter := &Adapter{
		logger: logger,
	}

	// 1. Initial status
	status := adapter.Status(context.Background())
	if status.Status != "unpaired" {
		t.Fatalf("expected unpaired status, got %s", status.Status)
	}

	qrChan := make(chan whatsmeow.QRChannelItem, 5)
	qrCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go adapter.consumeQR(qrCtx, qrChan)

	// 2. Send "code" event
	qrChan <- whatsmeow.QRChannelItem{
		Event:   "code",
		Code:    "2@sensitive-qr-pairing-payload",
		Timeout: 30 * time.Second,
	}

	// Wait for code to be processed
	for i := 0; i < 50; i++ {
		status = adapter.Status(context.Background())
		if status.Status == "pairing" && status.QRCode == "2@sensitive-qr-pairing-payload" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status.Status != "pairing" || status.QRCode != "2@sensitive-qr-pairing-payload" {
		t.Fatalf("expected pairing with QR code, got %+v", status)
	}

	// Verify sensitive QR payload is NOT logged
	logged := logBuf.String()
	if strings.Contains(logged, "2@sensitive-qr-pairing-payload") {
		t.Fatalf("log leaked raw QR code: %s", logged)
	}
	if !strings.Contains(logged, "whatsapp_qr_generated") {
		t.Fatalf("expected safe log whatsapp_qr_generated: %s", logged)
	}

	// 3. Send "success" event
	qrChan <- whatsmeow.QRChannelItem{
		Event: "success",
	}
	for i := 0; i < 50; i++ {
		status = adapter.Status(context.Background())
		if status.Status == "unpaired" && status.QRCode == "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(logBuf.String(), "whatsapp_pairing_success") {
		t.Fatalf("expected safe log whatsapp_pairing_success: %s", logBuf.String())
	}

	// 4. Test CancelPair
	if err := adapter.CancelPair(context.Background()); err != nil {
		t.Fatalf("CancelPair failed: %v", err)
	}
	if !strings.Contains(logBuf.String(), "whatsapp_pairing_cancelled") {
		t.Fatalf("expected safe log whatsapp_pairing_cancelled: %s", logBuf.String())
	}
}
