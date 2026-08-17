package config

import (
	"os"
	"path/filepath"
	"testing"
)

func validConfig() Config {
	return Config{
		Groups: map[string]Group{
			"a": {JID: "1@g.us"},
			"b": {JID: "2@g.us"},
		},
		SyncSets: []SyncSet{{ID: "mesh", Groups: []string{"a", "b"}}},
		Identity: Identity{UsernameMode: "hash"},
		Media:    Media{MaxSizeMB: 100},
		Recovery: Recovery{MaxAgeHours: 24, MaxMessagesPerGroup: 200},
		Storage:  Storage{MessageRetentionDays: 90},
	}
}

func TestValidateAcceptsSimpleMesh(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsGroupInMultipleSets(t *testing.T) {
	cfg := validConfig()
	cfg.Groups["c"] = Group{JID: "3@g.us"}
	cfg.SyncSets = append(cfg.SyncSets, SyncSet{ID: "two", Groups: []string{"a", "c"}})
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected error")
	}
}

func TestValidateRejectsUnsafeAliasAndUnassignedGroup(t *testing.T) {
	cfg := validConfig()
	cfg.Groups["15551234567@s.whatsapp.net"] = Group{JID: "3@g.us"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected unsafe alias error")
	}

	cfg = validConfig()
	cfg.Groups["c"] = Group{JID: "3@g.us"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected unassigned group error")
	}
}

func TestLoadDefaultsAndRejectsTrailingJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{"groups":{"a":{"jid":"1@g.us"},"b":{"jid":"2@g.us"}},"syncSets":[{"id":"mesh","groups":["a","b"]}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Identity.UsernameMode != "push_name" || cfg.Media.MaxSizeMB != 100 || cfg.Storage.MessageRetentionDays != 90 {
		t.Fatalf("defaults not applied: %+v", cfg)
	}

	if err := os.WriteFile(path, []byte(body+` {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() expected trailing JSON error")
	}
}
