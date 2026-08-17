package config

import "testing"

func TestValidateAcceptsSimpleMesh(t *testing.T) {
	cfg := Config{
		Groups: map[string]Group{
			"a": {JID: "1@g.us"},
			"b": {JID: "2@g.us"},
		},
		SyncSets: []SyncSet{{ID: "mesh", Groups: []string{"a", "b"}}},
		Identity: Identity{UsernameMode: "hash"},
		Media: Media{MaxSizeMB: 100},
		Storage: Storage{MessageRetentionDays: 90},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsGroupInMultipleSets(t *testing.T) {
	cfg := Config{
		Groups: map[string]Group{"a": {JID: "1@g.us"}, "b": {JID: "2@g.us"}, "c": {JID: "3@g.us"}},
		SyncSets: []SyncSet{{ID: "one", Groups: []string{"a", "b"}}, {ID: "two", Groups: []string{"a", "c"}}},
		Identity: Identity{UsernameMode: "hash"}, Media: Media{MaxSizeMB: 100}, Storage: Storage{MessageRetentionDays: 90},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected error")
	}
}
