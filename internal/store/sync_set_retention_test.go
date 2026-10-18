package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScopedSettingsUpgradePreservesPoliciesAndOwnership(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sync.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(schemaSQL, "    settings_json TEXT,\n", "", 1)
	old = strings.Replace(old, "    sync_set_id TEXT,\n", "", 1)
	if strings.Contains(old, "settings_json") {
		t.Fatal("fixture retained settings_json")
	}
	if _, err = db.ExecContext(ctx, old); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE global_config SET username_mode='hash',media_enabled=0,media_max_size_mb=23,recovery_enabled=0,recovery_max_age_hours=72,recovery_max_messages_per_group=345,storage_message_retention_days=37,poll_aggregation_trigger='/votes',whatsapp_chat_cleanup_enabled=1,whatsapp_chat_retention_days=13,local_message_prefix='!local',child_context_display_mode='friendly';
 INSERT INTO sync_sets(id) VALUES ('work'),('family');
 INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id) VALUES ('a','whatsapp','conn-wa','1@g.us','work'),('b','discord','conn-dc','123','family');
 INSERT INTO canonical_messages(canonical_id,created_at) VALUES ('owned',1),('ambiguous',1);
 INSERT INTO message_copies(canonical_id,endpoint_id,remote_message_id,created_at) VALUES ('owned','a','1',1),('ambiguous','a','2',1),('ambiguous','b','2',1);`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		st, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		for _, set := range []string{"work", "family"} {
			var raw string
			if err := st.db.QueryRow(`SELECT settings_json FROM sync_sets WHERE id=?`, set).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(raw), &got); err != nil {
				t.Fatal(err)
			}
			if got["codeLength"] != nil || got["usernameMode"] != nil || got["childContextDisplayMode"] != "friendly" || got["showDiscriminator"] != nil || got["localPrefix"] != "!local" {
				t.Fatalf("upgrade/reopen settings: %s", raw)
			}
			for key, want := range map[string]map[string]any{
				"media": {"enabled": false, "maxSizeMB": float64(23)}, "recovery": {"enabled": false, "maxAgeHours": float64(72), "maxMessagesPerGroup": float64(345)}, "storage": {"messageRetentionDays": float64(37)}, "polls": {"aggregationTrigger": "/votes"}, "whatsappCleanup": {"enabled": true, "retentionDays": float64(13)},
			} {
				actual := got[key].(map[string]any)
				for field, v := range want {
					if actual[field] != v {
						t.Fatalf("%s.%s=%v want %v", key, field, actual[field], v)
					}
				}
			}
		}
		var owner string
		if err := st.db.QueryRow(`SELECT sync_set_id FROM canonical_messages WHERE canonical_id='owned'`).Scan(&owner); err != nil || owner != "work" {
			t.Fatalf("ownership %s %v", owner, err)
		}
		var ambiguous sql.NullString
		if err := st.db.QueryRow(`SELECT sync_set_id FROM canonical_messages WHERE canonical_id='ambiguous'`).Scan(&ambiguous); err != nil || ambiguous.Valid {
			t.Fatalf("ambiguous ownership %v %v", ambiguous, err)
		}
		if _, err := st.db.Exec(`UPDATE sync_sets SET settings_json=json_set(settings_json,'$.codeLength',9) WHERE id='work'; UPDATE global_config SET storage_message_retention_days=1;`); err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSyncSetRetentionIsolationAndOrphanCleanup(t *testing.T) {
	st, _ := openTestStore(t)
	ctx := context.Background()
	if _, err := st.db.Exec(`INSERT INTO sync_sets(id) VALUES ('short'),('long'); INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id) VALUES ('a','whatsapp','conn-wa','1@g.us','short'),('b','discord','conn-dc','123','long')`); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id, owner string
		days      int
	}{{"short-old", "short", 20}, {"long-old", "long", 20}, {"recent", "short", 1}, {"orphan", "gone", 100}} {
		stamp := time.Now().AddDate(0, 0, -item.days).UnixMilli()
		if _, err := st.db.Exec(`INSERT INTO canonical_messages(canonical_id,sync_set_id,created_at) VALUES(?,?,?)`, item.id, item.owner, stamp); err != nil {
			t.Fatal(err)
		}
	}
	// Move source endpoint after message creation: canonical ownership stays original.
	if _, err := st.db.Exec(`UPDATE endpoints SET sync_set_id='long' WHERE alias='a'`); err != nil {
		t.Fatal(err)
	}
	for _, ep := range []string{"a", "b", "deleted"} {
		for _, days := range []int{1, 100} {
			stamp := time.Now().AddDate(0, 0, -days).UnixMilli()
			if _, err := st.db.Exec(`INSERT INTO suppressed_local_messages(endpoint_id,remote_message_id,created_at) VALUES(?,?,?)`, ep, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			if _, err := st.db.Exec(`INSERT INTO suppressed_reactions(endpoint_id,remote_message_id,emoji,created_at) VALUES(?,?,'👍',?)`, ep, stamp, stamp); err != nil {
				t.Fatal(err)
			}
		}
	}
	deleted, err := st.PruneSyncSetRetention(ctx, map[string]int{"short": 7, "long": 60}, 1)
	if err != nil || deleted != 2 {
		t.Fatalf("prune=%d err=%v", deleted, err)
	}
	for _, table := range []string{"canonical_messages", "suppressed_local_messages", "suppressed_reactions"} {
		var count int
		if err := st.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		want := 3
		if table == "canonical_messages" {
			want = 2
		}
		if count != want {
			t.Fatalf("%s count=%d want %d", table, count, want)
		}
	}
}
