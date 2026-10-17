package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateDMGateUpgradeDefaultsOffAndPreservesRouting(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sync.db")
	old := strings.Replace(schemaSQL, ",\n    private_dm_relay_enabled BOOLEAN NOT NULL DEFAULT 0 CHECK (private_dm_relay_enabled IN (0, 1))", "", 1)
	if old == schemaSQL {
		t.Fatal("upgrade fixture did not remove new gate")
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, old); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `UPDATE global_config SET username_mode='hash',storage_message_retention_days=37 WHERE id=1; INSERT INTO sync_sets(id) VALUES('mesh'); INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id) VALUES('a','discord','owner','123','mesh'); INSERT INTO participant_addresses(endpoint_id,actor_id,ordinal) VALUES('a','u_abcdefghij',1)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	for attempt := 0; attempt < 2; attempt++ {
		db, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		var enabled bool
		var retention, participants int
		var mode string
		if err := db.DB().QueryRow(`SELECT private_dm_relay_enabled,username_mode,storage_message_retention_days FROM global_config`).Scan(&enabled, &mode, &retention); err != nil {
			t.Fatal(err)
		}
		if err := db.DB().QueryRow(`SELECT COUNT(*) FROM participant_addresses WHERE actor_id='u_abcdefghij'`).Scan(&participants); err != nil {
			t.Fatal(err)
		}
		if enabled != (attempt == 1) || mode != "hash" || retention != 37 || participants != 1 {
			t.Fatal("upgrade changed existing routing state or enabled private messaging")
		}
		if _, err := db.DB().Exec(`UPDATE global_config SET private_dm_relay_enabled=1 WHERE id=1`); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
}
