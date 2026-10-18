package config

import (
	"context"
	"github.com/vm75/message-sync/internal/store"
	"path/filepath"
	"testing"
)

func TestEndpointAnonymityPersistence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sync.db")
	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := validConfig()
	for alias, ep := range cfg.Endpoints {
		ep.AnonymizeIncoming = true
		ep.AnonymizeOutgoing = ep.Transport == TransportWhatsApp
		cfg.Endpoints[alias] = ep
	}
	if err := Save(ctx, db.DB(), &cfg); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	loaded, err := Load(ctx, db.DB())
	if err != nil {
		t.Fatal(err)
	}
	for alias, want := range cfg.Endpoints {
		if got := loaded.Endpoints[alias]; got != want {
			t.Fatalf("%s: %+v want %+v", alias, got, want)
		}
	}
	// An unrelated settings save must retain both flags and physical endpoint rows.
	loaded.SyncSets[0].Settings.Media.Enabled = false
	if err := Save(ctx, db.DB(), loaded); err != nil {
		t.Fatal(err)
	}
	loaded, err = Load(ctx, db.DB())
	if err != nil {
		t.Fatal(err)
	}
	for alias, want := range cfg.Endpoints {
		if got := loaded.Endpoints[alias]; got != want {
			t.Fatalf("settings save changed %s: %+v", alias, got)
		}
	}
}
