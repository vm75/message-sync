package whatsapp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
