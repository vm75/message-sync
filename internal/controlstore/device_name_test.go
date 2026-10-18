package controlstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestWhatsAppDeviceNameUpgradeAndImportOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(schemaSQL, "    whatsapp_device_name TEXT NOT NULL DEFAULT '',\n", "", 1)
	if old == schemaSQL {
		t.Fatal("fixture did not remove device name")
	}
	if _, err := db.ExecContext(ctx, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transport_connections(id,transport,label,enabled,created_at,updated_at) VALUES('wa','whatsapp','Synthetic',1,1,1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		st, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		name := "Old linked name"
		if i == 1 {
			name = "Other global name"
		}
		if err := st.ImportWhatsAppDeviceNames(ctx, name); err != nil {
			t.Fatal(err)
		}
		conn, err := st.GetConnection(ctx, "wa")
		if err != nil {
			t.Fatal(err)
		}
		if conn.WhatsAppDeviceName != "Old linked name" {
			t.Fatalf("name changed across import: %q", conn.WhatsAppDeviceName)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
