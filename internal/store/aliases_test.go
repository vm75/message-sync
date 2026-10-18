package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/identity"
)

func TestAliasesSurviveRestartAndNeverReuseDeletedSet(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sync.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO sync_sets(id) VALUES ('work'),('family'); INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id) VALUES('a','discord','dc','123','work')`); err != nil {
		t.Fatal(err)
	}
	first, err := s.ObserveSyncSetActor(ctx, "work", "u_abcdefghij")
	if err != nil || first.Alias != "Amber Badger" {
		t.Fatalf("first=%+v %v", first, err)
	}
	again, err := s.ObserveSyncSetActor(ctx, "work", first.ActorID)
	if err != nil || again != first {
		t.Fatalf("repeat=%+v %v", again, err)
	}
	other, err := s.ObserveSyncSetActor(ctx, "family", first.ActorID)
	if err != nil || other.Alias == first.Alias {
		t.Fatalf("other=%+v %v", other, err)
	}
	if _, err := s.ObserveParticipant(ctx, "a", first.ActorID); err != nil {
		t.Fatal(err)
	}
	endpoints, err := s.ActorEndpoints(ctx, "work", first.ActorID)
	if err != nil || len(endpoints) != 1 || endpoints[0] != "a" {
		t.Fatalf("endpoints=%v %v", endpoints, err)
	}
	if _, err := s.db.Exec(`DELETE FROM endpoints WHERE alias='a'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	resolved, err := s.ActorByAlias(ctx, strings.ToLower(first.Alias))
	if err != nil || resolved != first {
		t.Fatalf("restart lookup=%+v %v", resolved, err)
	}
	if _, err := s.ActorInSyncSetByAlias(ctx, "family", first.Alias); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-set lookup=%v", err)
	}
	if _, err := s.db.Exec(`DELETE FROM sync_sets WHERE id='work'; INSERT INTO sync_sets(id) VALUES('new')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActorByAlias(ctx, first.Alias); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted alias resolved: %v", err)
	}
	third, err := s.ObserveSyncSetActor(ctx, "new", first.ActorID)
	if err != nil || third.Ordinal <= other.Ordinal || third.Alias == first.Alias {
		t.Fatalf("reused ordinal: %+v %v", third, err)
	}
}

func TestAliasAllocationConcurrentConnections(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sync.db")
	a, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := a.db.Exec(`INSERT INTO sync_sets(id) VALUES('mesh')`); err != nil {
		t.Fatal(err)
	}
	hasher, _ := identity.New([]byte(strings.Repeat("k", 32)))
	var wg sync.WaitGroup
	for i := 0; i < 48; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st := []*Store{a, b}[i%2]
			actor := hasher.UserID(fmt.Sprintf("actor-%d", i%12))
			first, err := st.ObserveSyncSetActor(ctx, "mesh", actor)
			if err != nil {
				t.Errorf("allocate: %v", err)
				return
			}
			second, err := st.ObserveSyncSetActor(ctx, "mesh", actor)
			if err != nil || second != first {
				t.Errorf("unstable: %+v %+v %v", first, second, err)
			}
		}(i)
	}
	wg.Wait()
	var count, next int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM sync_set_actors`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow(`SELECT next_ordinal FROM alias_sequence`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if count != 12 || next != 13 {
		t.Fatalf("count=%d next=%d", count, next)
	}
}

func TestAliasAllocationRollsBackAndExhaustionIsPermanent(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO sync_sets(id) VALUES('mesh'); CREATE TRIGGER fail_alias BEFORE UPDATE ON alias_sequence BEGIN SELECT RAISE(ABORT,'injected'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ObserveSyncSetActor(ctx, "mesh", "u_abcdefghij"); err == nil {
		t.Fatal("expected counter failure")
	}
	if _, err := s.AliasByActor(ctx, "mesh", "u_abcdefghij"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("partial assignment: %v", err)
	}
	var next int64
	if err := s.db.QueryRow(`SELECT next_ordinal FROM alias_sequence`).Scan(&next); err != nil || next != 1 {
		t.Fatalf("consumed ordinal %d %v", next, err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER fail_alias`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ObserveSyncSetActor(ctx, "missing", "u_abcdefghij"); err == nil {
		t.Fatal("accepted missing set")
	}
	if _, err := s.db.Exec(`UPDATE alias_sequence SET next_ordinal=?`, math.MaxInt64); err != nil {
		t.Fatal(err)
	}
	last, err := s.ObserveSyncSetActor(ctx, "mesh", "u_abcdefghij")
	if err != nil || last.Ordinal != math.MaxInt64 {
		t.Fatalf("last ordinal=%+v %v", last, err)
	}
	if _, err := s.ObserveSyncSetActor(ctx, "mesh", "u_234567abcd"); !errors.Is(err, ErrAliasExhausted) {
		t.Fatalf("exhaustion=%v", err)
	}
	if _, err := s.db.Exec(`DELETE FROM sync_set_actors`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ObserveSyncSetActor(ctx, "mesh", "u_234567abcd"); !errors.Is(err, ErrAliasExhausted) {
		t.Fatalf("reused exhausted ordinal: %v", err)
	}
}

func TestAliasAllocationHonorsWriterCancellation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sync.db")
	a, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := a.db.Exec(`INSERT INTO sync_sets(id) VALUES('mesh'); BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer a.db.Exec(`ROLLBACK`)
	deadline, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := b.ObserveSyncSetActor(deadline, "mesh", "u_abcdefghij"); err == nil {
		t.Fatal("allocated while writer was locked")
	}
	if time.Since(started) > time.Second {
		t.Fatal("writer busy handler ignored the caller's cancellation")
	}
}

func TestAliasCaseInsensitiveSQLUniquenessAndLookupFailure(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO sync_sets(id) VALUES('one'),('two')`); err != nil {
		t.Fatal(err)
	}
	first, err := s.ObserveSyncSetActor(ctx, "one", "u_abcdefghij")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO sync_set_actors(sync_set_id,actor_id,ordinal,alias) VALUES('two','u_234567abcd',2,?)`, strings.ToLower(first.Alias)); err == nil {
		t.Fatal("SQL accepted a case-insensitive alias collision")
	}
	if _, err := s.db.Exec(`UPDATE sync_set_actors SET alias='Unassigned Label' WHERE sync_set_id='one'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AliasByActor(ctx, "one", first.ActorID); err == nil || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("inconsistent assignment treated as absence: %v", err)
	}
}

func TestAliasOrderUpgradePreservesAssignmentsAndRetiredOrdinals(t *testing.T) {
	for _, ordering := range []string{"sequential", "scrambled"} {
		for _, deleted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/deleted=%v", ordering, deleted), func(t *testing.T) {
				ctx := context.Background()
				path := filepath.Join(t.TempDir(), "sync.db")
				s, err := Open(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				generate := identity.SyncSetAlias
				if ordering == "sequential" {
					generate = identity.SequentialSyncSetAlias
				}
				// Seed the last schema without a pinned ordering, using issued addresses.
				alias, _ := generate(2)
				if _, err = s.db.Exec("INSERT INTO sync_sets(id) VALUES('mesh'); DROP TABLE alias_order; UPDATE alias_sequence SET next_ordinal=3"); err != nil {
					t.Fatal(err)
				}
				if !deleted {
					if _, err = s.db.Exec("INSERT INTO sync_set_actors(sync_set_id,actor_id,ordinal,alias) VALUES('mesh','u_abcdefghij',2,?)", alias); err != nil {
						t.Fatal(err)
					}
				}
				if err = s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = Open(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				if !deleted {
					got, err := s.AliasByActor(ctx, "mesh", "u_abcdefghij")
					if err != nil || got != alias {
						t.Fatalf("issued address changed: %q %v", got, err)
					}
				}
				// With no surviving evidence, keep the original ordering to protect
				// previously retired addresses. Once recorded, that choice is permanent.
				if deleted {
					generate = identity.SequentialSyncSetAlias
				}
				want, _ := generate(3)
				got, err := s.ObserveSyncSetActor(ctx, "mesh", "u_234567abcd")
				if err != nil || got.Ordinal != 3 || got.Alias != want {
					t.Fatalf("allocation changed: %+v %v want %s", got, err, want)
				}
				if err = s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = Open(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				again, err := s.ObserveSyncSetActor(ctx, "mesh", "u_234567abcd")
				if err != nil || again != got {
					t.Fatalf("restart changed address: %+v %v", again, err)
				}
			})
		}
	}
}

func TestPinnedAliasOrderSurvivesAllAssignmentsDeleted(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sync.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("INSERT INTO sync_sets(id) VALUES('mesh')"); err != nil {
		t.Fatal(err)
	}
	first, err := s.ObserveSyncSetActor(ctx, "mesh", "u_abcdefghij")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DELETE FROM sync_sets"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.db.Exec("INSERT INTO sync_sets(id) VALUES('mesh')"); err != nil {
		t.Fatal(err)
	}
	second, err := s.ObserveSyncSetActor(ctx, "mesh", "u_234567abcd")
	want, _ := identity.SyncSetAlias(2)
	if err != nil || second.Ordinal != 2 || second.Alias != want || second.Alias == first.Alias {
		t.Fatalf("deleted assignments changed pinned ordering: %+v %v", second, err)
	}
}
