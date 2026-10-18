package privatedm

import (
	"context"
	"strings"
	"testing"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/router"
	"github.com/vm75/message-sync/internal/store"
)

func TestPrivateAliasRoutingDeduplicatesOnlyEquivalentInboxes(t *testing.T) {
	for _, distinct := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-inbox", true: "different-inboxes"}[distinct], func(t *testing.T) {
			r, f, cfg, now, alice, bob := fixture(t, "discord", "telegram")
			ctx := context.Background()
			ep := cfg.Endpoints["b"]
			ep.RemoteID = "-999"
			if distinct {
				ep.ConnectionID = "second-bot"
			}
			cfg.Endpoints["c"] = ep
			cfg.SyncSets[0].Endpoints = append(cfg.SyncSets[0].Endpoints, "c")
			if _, err := r.Sync.DB().Exec(`INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id) VALUES('c','telegram',?,'-999','mesh')`, ep.ConnectionID); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Sync.ObserveParticipant(ctx, "c", bob); err != nil {
				t.Fatal(err)
			}
			f.members["c"+bob] = true
			candidate := f.inboxes["b"+bob]
			if distinct {
				candidate.ConnectionID = ep.ConnectionID
				candidate.OwnerID = "second-owner"
			}
			f.inboxes["c"+bob] = candidate
			if err := r.Handle(ctx, input(f.inboxes["a"+alice], alice, "command", "/dm @{Pale Poppy} body", "", *now)); err != nil {
				t.Fatal(err)
			}
			if len(f.sent) != 1 {
				t.Fatalf("unexpected deliveries: %+v", f.sent)
			}
			if distinct {
				if strings.HasPrefix(f.sent[0].text, "DM from ") || f.sent[0].inbox.ConnectionID != "from" {
					t.Fatal("ambiguous inbox was selected")
				}
			} else if f.sent[0].inbox.ConnectionID != "to" || !strings.HasPrefix(f.sent[0].text, "DM from a/Amber Badger\n") {
				t.Fatal("equivalent routes failed")
			}
		})
	}
}

func TestPrivateAliasDoesNotBootstrapUnassignedSender(t *testing.T) {
	r, f, _, now, alice, _ := fixture(t, "discord", "telegram")
	ctx := context.Background()
	if _, err := r.Sync.DB().Exec(`DELETE FROM sync_set_actors WHERE actor_id=?`, alice); err != nil {
		t.Fatal(err)
	}
	if err := r.Handle(ctx, input(f.inboxes["a"+alice], alice, "command", "/dm @{Pale Poppy} body", "", *now)); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 1 || strings.HasPrefix(f.sent[0].text, "DM from ") {
		t.Fatal("private sender was enrolled")
	}
	var count int
	if err := r.Sync.DB().QueryRow(`SELECT COUNT(*) FROM sync_set_actors`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("unexpected alias allocation %d %v", count, err)
	}
}

func TestPrivateAliasGloballyResolvesButAuthorizesOwningSet(t *testing.T) {
	r, f, cfg, now, alice, bob := fixture(t, "discord", "telegram")
	ctx := context.Background()
	settings := config.DefaultSyncSetSettings()
	settings.PrivateDMRelayEnabled = true
	cfg.SyncSets = append(cfg.SyncSets, config.SyncSet{ID: "other", Endpoints: []string{"other-recipient"}, Settings: &settings})
	cfg.Endpoints["other-recipient"] = config.Endpoint{Transport: config.TransportTelegram, ConnectionID: "to", RemoteID: "-999"}
	if _, err := r.Sync.DB().Exec(`INSERT INTO sync_sets(id) VALUES('other'); INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id) VALUES('other-recipient','telegram','to','-999','other')`); err != nil {
		t.Fatal(err)
	}
	other, err := r.Sync.ObserveSyncSetActor(ctx, "other", bob)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Sync.ObserveParticipant(ctx, "other-recipient", bob); err != nil {
		t.Fatal(err)
	}
	f.members["other-recipient"+bob] = true
	f.inboxes["other-recipient"+bob] = f.inboxes["b"+bob]
	if err := r.Handle(ctx, input(f.inboxes["a"+alice], alice, "command", "/dm @{"+other.Alias+"} body", "", *now)); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 1 || f.sent[0].inbox.ConnectionID != "from" || strings.HasPrefix(f.sent[0].text, "DM from ") {
		t.Fatal("source membership in a different set authorized private delivery")
	}
	// Merely knowing the global alias grants no membership or inbox access.
}

func TestPrivateAliasLookupIntegrityErrorIsNotReportedAsAbsence(t *testing.T) {
	r, f, _, now, alice, bob := fixture(t, "discord", "telegram")
	ctx := context.Background()
	if _, err := r.Sync.DB().Exec(`UPDATE sync_set_actors SET alias='Azure Fox' WHERE actor_id=?`, bob); err != nil {
		t.Fatal(err)
	}
	inbox := f.inboxes["a"+alice]
	err := r.Handle(ctx, input(inbox, alice, "broken", "/dm @{Azure Fox} private body", "", *now))
	if err == nil || len(f.sent) != 1 || strings.Contains(f.sent[0].text, "private body") {
		t.Fatalf("integrity error was swallowed or delivered: %v %+v", err, f.sent)
	}
	if err := r.Handle(ctx, input(inbox, alice, "absent", "/dm @{Unknown Member} private body", "", *now)); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 2 || f.sent[0].text != f.sent[1].text {
		t.Fatal("private response disclosed alias existence/integrity")
	}
}

// An installation that issued aliases before the slot-order change must still
// be able to address its existing recipient without rewriting that address.
func TestPrivateDMExistingAssignedAddressAfterUpdate(t *testing.T) {
	r, f, cfg, now, alice, bob := fixture(t, "discord", "telegram")
	if _, err := r.Sync.DB().Exec("UPDATE sync_set_actors SET alias='Amber Bear' WHERE actor_id=?", bob); err != nil {
		t.Fatal(err)
	}
	// Simulate an upgrade from the database schema before ordering was pinned.
	var seq int
	var dbName, path string
	if err := r.Sync.DB().QueryRow("PRAGMA database_list").Scan(&seq, &dbName, &path); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Sync.DB().Exec("DROP TABLE IF EXISTS alias_order"); err != nil {
		t.Fatal(err)
	}
	if err := r.Sync.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	r.Sync = reopened
	mesh, err := router.NewWithHasher(cfg, reopened, f, r.Hasher)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mesh.Close)
	r.Resolver = mesh
	err = r.Handle(context.Background(), input(f.inboxes["a"+alice], alice, "existing-address", "/dm @{Amber Bear} hello", "", *now))
	if len(f.sent) != 1 || f.sent[0].inbox.ConnectionID != "to" || !strings.HasPrefix(f.sent[0].text, "DM from ") {
		t.Fatalf("existing address rejected: err=%v deliveries=%+v", err, f.sent)
	}
}
