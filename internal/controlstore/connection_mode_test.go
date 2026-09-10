package controlstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestTransportConnectionIntegrationModeMigrationDefaultsTelegramToBot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE transport_connections (
		id TEXT PRIMARY KEY,
		transport TEXT NOT NULL,
		label TEXT NOT NULL,
		enabled BOOLEAN NOT NULL DEFAULT 1,
		encrypted_credential BLOB,
		credential_nonce BLOB,
		credential_key_version INTEGER NOT NULL DEFAULT 1,
		created_by TEXT,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transport_connections(id,transport,label,enabled,encrypted_credential,credential_nonce,created_at,updated_at) VALUES('legacy','telegram','Legacy bot',1,x'01',x'02',1,1)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	conn, err := store.GetConnection(context.Background(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if conn.IntegrationMode != TelegramIntegrationModeBot {
		t.Fatalf("legacy Telegram mode = %q, want bot", conn.IntegrationMode)
	}
}

func TestValidateIntegrationMode(t *testing.T) {
	for _, tc := range []struct {
		transport string
		mode      string
		wantErr   bool
	}{
		{"telegram", "", false},
		{"telegram", "bot", false},
		{"telegram", "mtproto", false},
		{"telegram", "other", true},
		{"discord", "", false},
		{"discord", "bot", true},
	} {
		if err := ValidateIntegrationMode(tc.transport, tc.mode); (err != nil) != tc.wantErr {
			t.Errorf("ValidateIntegrationMode(%q,%q) err=%v wantErr=%v", tc.transport, tc.mode, err, tc.wantErr)
		}
	}
}
