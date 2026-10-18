package config

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestSettingsSavePreservesAliasAndParticipantRows(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	cfg := validConfig()
	cfg.ChildContextDisplayMode = ChildContextDisplayFriendly
	if err := Save(ctx, s.DB(), &cfg); err != nil {
		t.Fatal(err)
	}
	actor := "u_abcdefghij"
	alias, err := s.ObserveSyncSetActor(ctx, "mesh", actor)
	if err != nil {
		t.Fatal(err)
	}
	participant, err := s.ObserveParticipant(ctx, "a", actor)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertChildScopeLabel(ctx, "a", "telegram_topic", "topic", "Planning"); err != nil {
		t.Fatal(err)
	}
	// A delete/reinsert implementation must fail even if it later restores data.
	if _, err := s.DB().Exec(`CREATE TRIGGER protect_sets BEFORE DELETE ON sync_sets BEGIN SELECT RAISE(ABORT,'unexpected set deletion'); END; CREATE TRIGGER protect_endpoints BEFORE DELETE ON endpoints BEGIN SELECT RAISE(ABORT,'unexpected endpoint deletion'); END;`); err != nil {
		t.Fatal(err)
	}
	cfg.SyncSets[0].AnonymizeToDiscord = true
	cfg.LocalPrefix = "!local"
	if err := Save(ctx, s.DB(), &cfg); err != nil {
		t.Fatal(err)
	}
	got, err := s.ActorByAlias(ctx, alias.Alias)
	if err != nil || got != alias {
		t.Fatalf("lost alias: %+v %v", got, err)
	}
	address, err := s.ParticipantByActor(ctx, "a", actor)
	if err != nil || address != participant {
		t.Fatalf("lost participant: %+v %v", address, err)
	}
	var next int64
	if err := s.DB().QueryRow(`SELECT next_ordinal FROM alias_sequence`).Scan(&next); err != nil || next != 2 {
		t.Fatalf("changed sequence: %d %v", next, err)
	}
	if _, err := s.ChildScopeLabel(ctx, "a", "telegram_topic", "topic"); err != nil {
		t.Fatal(err)
	}
	var violations int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign keys: %d %v", violations, err)
	}
}

func TestRoutingSaveRetiresOnlyChangedBindingsAndKeepsAlias(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	cfg := validConfig()
	if err := Save(ctx, s.DB(), &cfg); err != nil {
		t.Fatal(err)
	}
	actor := "u_abcdefghij"
	alias, err := s.ObserveSyncSetActor(ctx, "mesh", actor)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"a", "b"} {
		if _, err := s.ObserveParticipant(ctx, endpoint, actor); err != nil {
			t.Fatal(err)
		}
	}
	ep := cfg.Endpoints["a"]
	ep.RemoteID = "3@g.us"
	cfg.Endpoints["a"] = ep
	if err := Save(ctx, s.DB(), &cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ParticipantByActor(ctx, "a", actor); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale participant retained: %v", err)
	}
	if _, err := s.ParticipantByActor(ctx, "b", actor); err != nil {
		t.Fatalf("unchanged binding lost: %v", err)
	}
	got, err := s.ActorByAlias(ctx, alias.Alias)
	if err != nil || got != alias {
		t.Fatalf("rebind lost alias: %+v %v", got, err)
	}
	// Swapping two targets remains an atomic supported config change.
	a, b := cfg.Endpoints["a"], cfg.Endpoints["b"]
	a.RemoteID, b.RemoteID = b.RemoteID, a.RemoteID
	cfg.Endpoints["a"], cfg.Endpoints["b"] = a, b
	if err := Save(ctx, s.DB(), &cfg); err != nil {
		t.Fatal(err)
	}
}

func TestRoutingSaveFailureRollsBackDependentState(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	cfg := validConfig()
	if err := Save(ctx, s.DB(), &cfg); err != nil {
		t.Fatal(err)
	}
	actor := "u_abcdefghij"
	if _, err := s.ObserveParticipant(ctx, "a", actor); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`CREATE TRIGGER reject_set BEFORE UPDATE ON sync_sets BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	ep := cfg.Endpoints["a"]
	ep.RemoteID = "3@g.us"
	cfg.Endpoints["a"] = ep
	if err := Save(ctx, s.DB(), &cfg); err == nil {
		t.Fatal("expected save failure")
	}
	if _, err := s.ParticipantByActor(ctx, "a", actor); err != nil {
		t.Fatalf("rollback lost participant: %v", err)
	}
}
