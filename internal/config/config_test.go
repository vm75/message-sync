package config

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vm75/message-sync/internal/store"
)

func validConfig() Config {
	return Config{
		Endpoints: map[string]Endpoint{
			"a": {Transport: TransportWhatsApp, RemoteID: "1@g.us"},
			"b": {Transport: TransportWhatsApp, RemoteID: "2@g.us"},
		},
		SyncSets: []SyncSet{{ID: "mesh", Endpoints: []string{"a", "b"}}},
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
	cfg.Endpoints["c"] = Endpoint{Transport: TransportWhatsApp, RemoteID: "3@g.us"}
	cfg.SyncSets = append(cfg.SyncSets, SyncSet{ID: "two", Endpoints: []string{"a", "c"}})
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected error")
	}
}

func TestValidateRejectsUnsafeAliasAndUnassignedGroup(t *testing.T) {
	cfg := validConfig()
	cfg.Endpoints["15551234567@s.whatsapp.net"] = Endpoint{Transport: TransportWhatsApp, RemoteID: "3@g.us"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected unsafe alias error")
	}

	cfg = validConfig()
	cfg.Endpoints["c"] = Endpoint{Transport: TransportWhatsApp, RemoteID: "3@g.us"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected unassigned group error")
	}
}

func TestValidateTransportAwareEndpoints(t *testing.T) {
	cfg := validConfig()
	cfg.Endpoints["b"] = Endpoint{Transport: TransportDiscord, RemoteID: "123456789012345678"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with Discord endpoint failed: %v", err)
	}

	cfg = validConfig()
	cfg.Endpoints["b"] = Endpoint{Transport: TransportTelegram, RemoteID: "-1001234567890"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with Telegram endpoint failed: %v", err)
	}

	cfg = validConfig()
	cfg.Endpoints["b"] = Endpoint{Transport: TransportTelegram, RemoteID: "-123456789"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with Telegram basic-group endpoint failed: %v", err)
	}

	cfg = validConfig()
	cfg.Endpoints["b"] = Endpoint{Transport: "unknown", RemoteID: "opaque"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected error for unknown transport")
	}

	cfg = validConfig()
	cfg.Endpoints["b"] = Endpoint{Transport: TransportTelegram, RemoteID: "123456789"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected error for positive Telegram private chat id")
	}

	cfg = validConfig()
	cfg.Endpoints["b"] = Endpoint{Transport: TransportTelegram, RemoteID: "-0"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected error for invalid Telegram chat id")
	}

	cfg = validConfig()
	cfg.Endpoints["b"] = Endpoint{Transport: TransportTelegram, RemoteID: "-99999999999999999999"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected error for overflowing Telegram chat id")
	}

	cfg = validConfig()
	cfg.Endpoints["b"] = Endpoint{Transport: TransportDiscord, RemoteID: " "}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected error for empty remote id")
	}

	cfg = validConfig()
	cfg.Endpoints["b"] = Endpoint{Transport: TransportDiscord, RemoteID: "not-a-channel"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected error for invalid Discord channel id")
	}

	cfg = validConfig()
	cfg.Endpoints["b"] = Endpoint{Transport: TransportDiscord, RemoteID: "99999999999999999999"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected error for overflowing Discord channel id")
	}

	cfg = validConfig()
	cfg.Endpoints["b"] = Endpoint{Transport: TransportWhatsApp, RemoteID: "1@g.us"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected error for duplicate WhatsApp remote target")
	}
}

func TestValidateRejectsInvalidSyncSetAndDuplicateMembership(t *testing.T) {
	cfg := validConfig()
	cfg.SyncSets[0].ID = "unsafe set"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected invalid sync set id error")
	}

	cfg = validConfig()
	cfg.SyncSets[0].Endpoints = []string{"a", "a"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected duplicate endpoint membership error")
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
	if len(loaded.Endpoints) != 2 {
		t.Fatalf("got %d endpoints, want 2", len(loaded.Endpoints))
	}
	if loaded.Endpoints["a"].Transport != TransportWhatsApp || loaded.Endpoints["b"].Transport != TransportWhatsApp ||
		loaded.Endpoints["a"].RemoteID != "1@g.us" || loaded.Endpoints["b"].RemoteID != "2@g.us" {
		t.Errorf("loaded endpoints mismatch: %+v", loaded.Endpoints)
	}
	if len(loaded.SyncSets) != 1 || loaded.SyncSets[0].ID != "mesh" {
		t.Fatalf("loaded sync sets mismatch: %+v", loaded.SyncSets)
	}
}

func TestSaveAndLoadThreeTransportSyncSet(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	cfg := validConfig()
	cfg.Endpoints["b"] = Endpoint{Transport: TransportDiscord, RemoteID: "123456789012345678"}
	cfg.Endpoints["c"] = Endpoint{Transport: TransportTelegram, RemoteID: "-1001234567890"}
	cfg.SyncSets[0].Endpoints = []string{"a", "b", "c"}

	if err := Save(ctx, st.DB(), &cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := Load(ctx, st.DB())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(loaded.Endpoints) != 3 {
		t.Fatalf("got %d endpoints, want 3", len(loaded.Endpoints))
	}
	if got := loaded.Endpoints["a"]; got.Transport != TransportWhatsApp || got.RemoteID != "1@g.us" {
		t.Fatalf("loaded WhatsApp endpoint = %+v", got)
	}
	if got := loaded.Endpoints["b"]; got.Transport != TransportDiscord || got.RemoteID != "123456789012345678" {
		t.Fatalf("loaded Discord endpoint = %+v", got)
	}
	if got := loaded.Endpoints["c"]; got.Transport != TransportTelegram || got.RemoteID != "-1001234567890" {
		t.Fatalf("loaded Telegram endpoint = %+v", got)
	}
	if len(loaded.SyncSets) != 1 || loaded.SyncSets[0].ID != "mesh" || len(loaded.SyncSets[0].Endpoints) != 3 {
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
	_, err = st.DB().ExecContext(ctx, `INSERT INTO endpoints (alias, transport, remote_id, sync_set_id) VALUES ('g1', 'whatsapp', '1@g.us', 'set1'), ('g2', 'whatsapp', '2@g.us', 'set1')`)
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

func TestMigrateTelegramEndpointPreservesAliasAndSyncSetAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "sync.db")
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}

	cfg := validConfig()
	const oldRemoteID = "-123456789"
	const newRemoteID = "-1009876543210"
	cfg.Endpoints["b"] = Endpoint{Transport: TransportTelegram, RemoteID: oldRemoteID}
	if err := Save(ctx, st.DB(), &cfg); err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	if err := MigrateTelegramEndpoint(ctx, st.DB(), "b", oldRemoteID, newRemoteID); err != nil {
		_ = st.Close()
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	loaded, err := Load(ctx, reopened.DB())
	if err != nil {
		t.Fatal(err)
	}
	endpoint := loaded.Endpoints["b"]
	if endpoint.Transport != TransportTelegram || endpoint.RemoteID != newRemoteID {
		t.Fatalf("migrated Telegram endpoint = %+v", endpoint)
	}
	if len(loaded.SyncSets) != 1 || loaded.SyncSets[0].ID != "mesh" {
		t.Fatalf("sync-set changed across Telegram migration: %+v", loaded.SyncSets)
	}
	foundAlias := false
	for _, alias := range loaded.SyncSets[0].Endpoints {
		if alias == "b" {
			foundAlias = true
		}
	}
	if !foundAlias {
		t.Fatalf("Telegram endpoint alias lost sync-set membership: %+v", loaded.SyncSets[0])
	}

	// Replayed migration events are idempotent after restart.
	if err := MigrateTelegramEndpoint(ctx, reopened.DB(), "b", oldRemoteID, newRemoteID); err != nil {
		t.Fatalf("replayed Telegram migration failed: %v", err)
	}
}
