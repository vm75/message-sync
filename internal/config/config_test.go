package config

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vm75/message-sync/internal/store"
)

func validConfig() Config {
	return Config{
		Groups: map[string]Group{
			"a": {JID: "1@g.us"},
			"b": {JID: "2@g.us"},
		},
		SyncSets: []SyncSet{{ID: "mesh", Groups: []string{"a", "b"}}},
		Identity: Identity{UsernameMode: UsernameModeHash},
		Media:    Media{MaxSizeMB: 100},
		Recovery: Recovery{MaxAgeHours: 24, MaxMessagesPerGroup: 200},
		Storage:  Storage{MessageRetentionDays: 90},
	}
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "sync.db")
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestValidateAcceptsSimpleMesh(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateUsernameModeEnum(t *testing.T) {
	cfg := validConfig()
	cfg.Identity.UsernameMode = UsernameModePushName
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with push_name failed: %v", err)
	}

	cfg.Identity.UsernameMode = UsernameModeHash
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with hash failed: %v", err)
	}

	cfg.Identity.UsernameMode = "invalid_mode"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected error for invalid usernameMode")
	}

	cfg.Identity.UsernameMode = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected error for empty usernameMode")
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

func TestSaveAndLoadFromSQLite(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	cfg := validConfig()
	cfg.Identity.UsernameMode = UsernameModePushName
	if err := Save(ctx, st.DB(), &cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := Load(ctx, st.DB())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if loaded.Identity.UsernameMode != UsernameModePushName {
		t.Errorf("got usernameMode %q, want %q", loaded.Identity.UsernameMode, UsernameModePushName)
	}
	if len(loaded.Groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(loaded.Groups))
	}
	if loaded.Groups["a"].JID != "1@g.us" || loaded.Groups["b"].JID != "2@g.us" {
		t.Errorf("loaded groups mismatch: %+v", loaded.Groups)
	}
	if len(loaded.SyncSets) != 1 || loaded.SyncSets[0].ID != "mesh" {
		t.Fatalf("loaded sync sets mismatch: %+v", loaded.SyncSets)
	}
}

func TestLoadDefaultsWhenEmpty(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	// Initially empty groups will fail validation in Load, but let's insert groups and check defaults
	_, err := st.DB().ExecContext(ctx, `INSERT INTO sync_sets (id) VALUES ('set1')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DB().ExecContext(ctx, `INSERT INTO groups (alias, jid, sync_set_id) VALUES ('g1', '1@g.us', 'set1'), ('g2', '2@g.us', 'set1')`)
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(ctx, st.DB())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if loaded.Identity.UsernameMode != UsernameModePushName {
		t.Errorf("default usernameMode = %q, want push_name", loaded.Identity.UsernameMode)
	}
	if loaded.Media.MaxSizeMB != 100 {
		t.Errorf("default media MaxSizeMB = %d, want 100", loaded.Media.MaxSizeMB)
	}
	if loaded.Storage.MessageRetentionDays != 90 {
		t.Errorf("default retention days = %d, want 90", loaded.Storage.MessageRetentionDays)
	}
	if loaded.WhatsAppCleanup.Enabled {
		t.Errorf("default whatsappCleanup.enabled = true, want false")
	}
	if loaded.WhatsAppCleanup.RetentionDays != 30 {
		t.Errorf("default whatsappCleanup.retentionDays = %d, want 30", loaded.WhatsAppCleanup.RetentionDays)
	}
}

func TestWhatsAppCleanupValidationAndPersistence(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	cfg := validConfig()
	cfg.WhatsAppCleanup.Enabled = true
	cfg.WhatsAppCleanup.RetentionDays = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for zero retention days when enabled")
	}

	cfg.WhatsAppCleanup.RetentionDays = 14
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}

	if err := Save(ctx, st.DB(), &cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := Load(ctx, st.DB())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if !loaded.WhatsAppCleanup.Enabled {
		t.Errorf("loaded whatsappCleanup.enabled = false, want true")
	}
	if loaded.WhatsAppCleanup.RetentionDays != 14 {
		t.Errorf("loaded whatsappCleanup.retentionDays = %d, want 14", loaded.WhatsAppCleanup.RetentionDays)
	}
}
