// Package testutil provides fixtures for tests using synthetic provider targets.
package testutil

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/vm75/message-sync/internal/config"
)

// SeedRouting provisions ownership for fake-adapter tests without validating
// their deliberately synthetic provider targets. It does not allocate aliases.
func SeedRouting(t testing.TB, db *sql.DB, cfg *config.Config) {
	t.Helper()
	for _, set := range cfg.SyncSets {
		settings, err := json.Marshal(cfg.SettingsForSet(set))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO sync_sets(id,settings_json) VALUES(?,?) ON CONFLICT(id) DO NOTHING`, set.ID, string(settings)); err != nil {
			t.Fatal(err)
		}
		for _, alias := range set.Endpoints {
			ep := cfg.Endpoints[alias]
			if _, err := db.Exec(`INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id) VALUES(?,?,?,?,?) ON CONFLICT(alias) DO UPDATE SET sync_set_id=excluded.sync_set_id`, alias, ep.Transport, ep.ConnectionID, ep.RemoteID, set.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
}
