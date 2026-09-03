package whatsapp_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport/whatsapp"
	"github.com/vm75/message-sync/internal/verification"
)

func TestProtocolDBPath_ValidationAndPermissions(t *testing.T) {
	tempDir := t.TempDir()

	// Valid connection IDs
	for _, id := range []string{"conn-wa-1", "conn-wa-2", "wa_backup", "test-123"} {
		path, err := whatsapp.ProtocolDBPath(tempDir, id)
		if err != nil {
			t.Fatalf("expected valid path for %q, got error: %v", id, err)
		}
		expected := filepath.Join(tempDir, "whatsapp", id+".db")
		if path != expected {
			t.Fatalf("expected path %q, got %q", expected, path)
		}
	}

	// Invalid connection IDs (path traversal, invalid chars, empty)
	for _, invalid := range []string{"", "../foo", "conn/1", "conn;rm", "wa space", "@conn", "-conn"} {
		_, err := whatsapp.ProtocolDBPath(tempDir, invalid)
		if err == nil {
			t.Fatalf("expected error for invalid connection ID %q, got nil", invalid)
		}
	}
}

func TestProtocolDB_IsolationAndRemoval(t *testing.T) {
	tempDir := t.TempDir()

	dbPath1, err := whatsapp.ProtocolDBPath(tempDir, "conn-wa-1")
	if err != nil {
		t.Fatal(err)
	}
	dbPath2, err := whatsapp.ProtocolDBPath(tempDir, "conn-wa-2")
	if err != nil {
		t.Fatal(err)
	}

	// Ensure directory with 0700 permissions
	dir := filepath.Dir(dbPath1)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("directory perm = %o, want 0700", dirInfo.Mode().Perm())
	}

	// Create mock files including sidecar WAL/SHM files
	if err := os.WriteFile(dbPath1, []byte("state-conn-1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath1+"-wal", []byte("wal-conn-1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath2, []byte("state-conn-2"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Remove connection 1 protocol DB
	if err := whatsapp.RemoveProtocolDB(tempDir, "conn-wa-1"); err != nil {
		t.Fatalf("remove protocol DB failed: %v", err)
	}

	// Connection 1 DB and WAL must be removed
	if _, err := os.Stat(dbPath1); !os.IsNotExist(err) {
		t.Fatalf("conn-1 db still exists after removal")
	}
	if _, err := os.Stat(dbPath1 + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("conn-1 db-wal still exists after removal")
	}

	// Connection 2 DB must remain intact
	data2, err := os.ReadFile(dbPath2)
	if err != nil {
		t.Fatalf("conn-2 db was deleted or cannot be read: %v", err)
	}
	if string(data2) != "state-conn-2" {
		t.Fatalf("conn-2 data corrupted, got %q", string(data2))
	}
}

func TestAdapter_EndpointOwnershipAndFiltering(t *testing.T) {
	tempDir := t.TempDir()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	secret := strings.Repeat("a", 32)
	hasher, err := identity.New([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}

	dbPath1, _ := whatsapp.ProtocolDBPath(tempDir, "conn-wa-1")
	dbPath2, _ := whatsapp.ProtocolDBPath(tempDir, "conn-wa-2")

	a1, err := whatsapp.Open(context.Background(), whatsapp.Options{
		ConnectionID: "conn-wa-1",
		DatabasePath: dbPath1,
		GroupJIDs: map[string]string{
			"g1": "111111111@g.us",
		},
		Hasher:           hasher,
		UsernameMode:     config.UsernameModeHash,
		Logger:           logger,
		EnableTerminalQR: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a1.Close()

	a2, err := whatsapp.Open(context.Background(), whatsapp.Options{
		ConnectionID: "conn-wa-2",
		DatabasePath: dbPath2,
		GroupJIDs: map[string]string{
			"g2": "222222222@g.us",
		},
		Hasher:           hasher,
		UsernameMode:     config.UsernameModeHash,
		Logger:           logger,
		EnableTerminalQR: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Close()

	if a1.ConnectionID() != "conn-wa-1" {
		t.Fatalf("a1 connectionID = %q, want conn-wa-1", a1.ConnectionID())
	}
	if a2.ConnectionID() != "conn-wa-2" {
		t.Fatalf("a2 connectionID = %q, want conn-wa-2", a2.ConnectionID())
	}

	if !a1.HasEndpoint("g1") || a1.HasEndpoint("g2") {
		t.Fatalf("a1 endpoint ownership incorrect")
	}
	if !a2.HasEndpoint("g2") || a2.HasEndpoint("g1") {
		t.Fatalf("a2 endpoint ownership incorrect")
	}

	// Update configuration with mixed endpoints across connections
	newCfg := &config.Config{
		Endpoints: map[string]config.Endpoint{
			"g1": {
				Transport:    config.TransportWhatsApp,
				ConnectionID: "conn-wa-1",
				RemoteID:     "111111111@g.us",
			},
			"g2": {
				Transport:    config.TransportWhatsApp,
				ConnectionID: "conn-wa-2",
				RemoteID:     "222222222@g.us",
			},
			"g3": {
				Transport:    config.TransportWhatsApp,
				ConnectionID: "conn-wa-1",
				RemoteID:     "333333333@g.us",
			},
		},
		Identity: config.Identity{UsernameMode: config.UsernameModeHash},
	}

	if err := a1.UpdateConfig(newCfg); err != nil {
		t.Fatal(err)
	}
	if err := a2.UpdateConfig(newCfg); err != nil {
		t.Fatal(err)
	}

	// a1 should now have g1 and g3, but NOT g2
	if !a1.HasEndpoint("g1") || !a1.HasEndpoint("g3") || a1.HasEndpoint("g2") {
		t.Fatalf("a1 updated endpoints incorrect")
	}
	// a2 should now have only g2
	if !a2.HasEndpoint("g2") || a2.HasEndpoint("g1") || a2.HasEndpoint("g3") {
		t.Fatalf("a2 updated endpoints incorrect")
	}
}

func TestAdapter_GlobalPairingSerializationAndBusyConflict(t *testing.T) {
	tempDir := t.TempDir()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	secret := strings.Repeat("b", 32)
	hasher, _ := identity.New([]byte(secret))

	dbPath1, _ := whatsapp.ProtocolDBPath(tempDir, "conn-wa-1")
	dbPath2, _ := whatsapp.ProtocolDBPath(tempDir, "conn-wa-2")

	a1, err := whatsapp.Open(context.Background(), whatsapp.Options{
		ConnectionID: "conn-wa-1",
		DatabasePath: dbPath1,
		Hasher:       hasher,
		UsernameMode: config.UsernameModeHash,
		Logger:       logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a1.Close()

	a2, err := whatsapp.Open(context.Background(), whatsapp.Options{
		ConnectionID: "conn-wa-2",
		DatabasePath: dbPath2,
		Hasher:       hasher,
		UsernameMode: config.UsernameModeHash,
		Logger:       logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Close()

	// Both initially unauthenticated/unpaired
	st1 := a1.Status(context.Background())
	if st1.Status != "unpaired" {
		t.Fatalf("a1 status = %q, want unpaired", st1.Status)
	}
	st2 := a2.Status(context.Background())
	if st2.Status != "unpaired" {
		t.Fatalf("a2 status = %q, want unpaired", st2.Status)
	}

	ctxPair1, cancelPair1 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelPair1()

	go func() {
		_, _ = a1.Pair(ctxPair1)
	}()

	time.Sleep(20 * time.Millisecond)

	_, err2 := a2.Pair(context.Background())
	if err2 != whatsapp.ErrPairingBusy {
		if err2 == nil || !strings.Contains(err2.Error(), "already in progress") {
			t.Logf("Pair error on a2 was: %v (expected ErrPairingBusy)", err2)
		}
	}

	_ = a1.CancelPair(context.Background())

	ctxPair2, cancelPair2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelPair2()
	_, errAfterCancel := a2.Pair(ctxPair2)
	if errAfterCancel == whatsapp.ErrPairingBusy {
		t.Fatalf("a2 should not get ErrPairingBusy after a1 cancelled pairing")
	}
	_ = a2.CancelPair(context.Background())
}

func TestMultiAdmin_MembershipVerificationRouting(t *testing.T) {
	tempDir := t.TempDir()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	secret := strings.Repeat("c", 32)
	hasher, _ := identity.New([]byte(secret))

	dbPath1, _ := whatsapp.ProtocolDBPath(tempDir, "conn-wa-1")
	dbPath2, _ := whatsapp.ProtocolDBPath(tempDir, "conn-wa-2")

	a1, err := whatsapp.Open(context.Background(), whatsapp.Options{
		ConnectionID: "conn-wa-1",
		DatabasePath: dbPath1,
		GroupJIDs: map[string]string{
			"team-a": "111111111@g.us",
		},
		Hasher:       hasher,
		UsernameMode: config.UsernameModeHash,
		Logger:       logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a1.Close()

	a2, err := whatsapp.Open(context.Background(), whatsapp.Options{
		ConnectionID: "conn-wa-2",
		DatabasePath: dbPath2,
		GroupJIDs: map[string]string{
			"team-b": "222222222@g.us",
		},
		Hasher:       hasher,
		UsernameMode: config.UsernameModeHash,
		Logger:       logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Close()

	multiAdmin := whatsapp.NewMultiAdmin(func() []verification.WhatsAppAdmin {
		return []verification.WhatsAppAdmin{a1, a2}
	})

	errUnknown := multiAdmin.AddParticipant(context.Background(), "unknown-alias", "+1234567890")
	if errUnknown != verification.ErrDestinationMissing {
		t.Fatalf("expected ErrDestinationMissing for unknown alias, got: %v", errUnknown)
	}

	errA := multiAdmin.AddParticipant(context.Background(), "team-a", "+1234567890")
	if errA == verification.ErrDestinationMissing {
		t.Fatalf("team-a should have been routed to a1, got ErrDestinationMissing")
	}

	errB := multiAdmin.AddParticipant(context.Background(), "team-b", "+1234567890")
	if errB == verification.ErrDestinationMissing {
		t.Fatalf("team-b should have been routed to a2, got ErrDestinationMissing")
	}
}
