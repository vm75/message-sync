package router

import (
	"context"
	"strings"
	"testing"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/transport"
)

func TestCrossGroupReferencesAddressKnownActorOnly(t *testing.T) {
	ctx := context.Background()
	r, db, sender := newTestRouter(t, config.UsernameModePushName)
	_, err := db.DB().ExecContext(ctx, `
		INSERT INTO sync_sets(id) VALUES('mesh');
		INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id)
		VALUES ('c1g1','whatsapp','wa','111@g.us','mesh'),
		       ('c1g2','whatsapp','wa','222@g.us','mesh'),
		       ('c1g3','whatsapp','wa','333@g.us','mesh');`)
	if err != nil {
		t.Fatal(err)
	}
	first := testIncoming("c1g2", "first")
	first.Sender.OpaqueID = "u_abcdefghij"
	if err := r.Handle(ctx, first); err != nil {
		t.Fatal(err)
	}
	waitForSent(t, sender, 2)
	observed, err := db.ParticipantByActor(ctx, "c1g2", first.Sender.OpaqueID)
	if err != nil || observed.Handle != "P1" {
		t.Fatalf("observed = %+v, err=%v", observed, err)
	}
	reply := testIncoming("c1g3", "second")
	reply.Sender.OpaqueID = "u_234567abcd"
	reply.Text = "Hello @{c1g2/P1}, I can help"
	if err := r.Handle(ctx, reply); err != nil {
		t.Fatal(err)
	}
	waitForSent(t, sender, 4)
	sender.mu.Lock()
	defer sender.mu.Unlock()
	found := false
	for _, item := range sender.sent {
		if item.outgoing.OriginEndpoint != "c1g3" || item.outgoing.Endpoint != "c1g2" {
			continue
		}
		found = true
		refs := item.outgoing.References
		if len(refs) != 1 || refs[0].ActorID != first.Sender.OpaqueID || refs[0].Endpoint != transport.EndpointID("c1g2") {
			t.Fatalf("resolved reference = %+v", refs)
		}
		if !strings.Contains(item.outgoing.Text, "@{c1g2/P1}") {
			t.Fatalf("text lost reference: %q", item.outgoing.Text)
		}
	}
	if !found {
		t.Fatal("message to addressed group was not delivered")
	}
}

func TestCrossReferenceParserRejectsEscapesAndUnconfiguredTargets(t *testing.T) {
	ctx := context.Background()
	r, db, _ := newTestRouter(t, config.UsernameModeHash)
	_, err := db.DB().ExecContext(ctx, `
		INSERT INTO sync_sets(id) VALUES('mesh');
		INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id)
		VALUES ('c1g2','whatsapp','wa','222@g.us','mesh'),
		       ('other','discord','dc','987654','mesh');`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ObserveParticipant(ctx, "c1g2", "u_abcdefghij"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ObserveParticipant(ctx, "other", "u_234567abcd"); err != nil {
		t.Fatal(err)
	}
	refs, err := r.crossReferences(ctx, "c1g1", `\\@{c1g2/P1} @{c1g2/P9} @{other/P1} @{c1g2/P1}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].Token != "@{c1g2/P1}" {
		t.Fatalf("unexpected resolutions: %+v", refs)
	}
}
