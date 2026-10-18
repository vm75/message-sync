package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestParticipantHandlesStableAndEndpointScoped(t *testing.T) {
	ctx := context.Background()
	s, _ := openTestStore(t)
	_, err := s.DB().ExecContext(ctx, `INSERT INTO sync_sets(id) VALUES('mesh');
	INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id)
	VALUES ('group1','whatsapp','wa','111@g.us','mesh'),
	       ('group2','whatsapp','wa','222@g.us','mesh');`)
	if err != nil {
		t.Fatal(err)
	}
	a1, err := s.ObserveParticipant(ctx, "group1", "u_abcdefghij")
	if err != nil || a1.Handle != "P1" {
		t.Fatalf("first address = %+v, err=%v", a1, err)
	}
	repeated, err := s.ObserveParticipant(ctx, "group1", "u_abcdefghij")
	if err != nil || repeated != a1 {
		t.Fatalf("repeat allocated different address: %+v, err=%v", repeated, err)
	}
	a2, err := s.ObserveParticipant(ctx, "group1", "u_234567abcd")
	if err != nil || a2.Handle != "P2" {
		t.Fatalf("second address = %+v, err=%v", a2, err)
	}
	other, err := s.ObserveParticipant(ctx, "group2", "u_abcdefghij")
	if err != nil || other.Handle != "P1" {
		t.Fatalf("second group should start at P1: %+v, err=%v", other, err)
	}
	byHandle, err := s.ParticipantByHandle(ctx, "group1", "P1")
	if err != nil || byHandle != a1 {
		t.Fatalf("handle lookup = %+v, err=%v", byHandle, err)
	}
	byActor, err := s.ParticipantByActor(ctx, "group2", "u_abcdefghij")
	if err != nil || byActor != other {
		t.Fatalf("actor lookup = %+v, err=%v", byActor, err)
	}
	if _, err := s.ParticipantByHandle(ctx, "group1", "P01"); err == nil {
		t.Fatal("accepted a noncanonical handle")
	}
	if _, err := s.ParticipantByHandle(ctx, "group1", "P3"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown handle resolved: %v", err)
	}
	// Address migrations follow endpoint alias renames, not group titles.
	if _, err := s.DB().ExecContext(ctx, `UPDATE endpoints SET alias='renamed' WHERE alias='group1'`); err != nil {
		t.Fatal(err)
	}
	renamed, err := s.ParticipantByHandle(ctx, "renamed", "P1")
	if err != nil || renamed.ActorID != a1.ActorID {
		t.Fatalf("renamed endpoint lost handle: %+v, %v", renamed, err)
	}
	// Provider IDs and usernames have no columns in this table.
	rows, err := s.DB().QueryContext(ctx, `PRAGMA table_info(participant_addresses)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(name, "phone") || strings.Contains(name, "native") || strings.Contains(name, "username") {
			t.Fatalf("unexpected PII column: %q", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
