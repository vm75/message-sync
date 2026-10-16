package router

import (
	"context"
	"strings"
	"testing"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
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
	first.Sender.DisplayName = "Alice Smith"
	if err := r.Handle(ctx, first); err != nil {
		t.Fatal(err)
	}
	waitForSent(t, sender, 2)
	observed, err := db.ParticipantByActor(ctx, "c1g2", first.Sender.OpaqueID)
	if err != nil || observed.Handle != "P1" {
		t.Fatalf("observed = %+v, err=%v", observed, err)
	}
	pseudonym := identity.Pseudonym(first.Sender.OpaqueID)
	for index, label := range []string{"P1", "Alice Smith", pseudonym, strings.Join(strings.Fields(pseudonym)[:2], " ")} {
		reply := testIncoming("c1g3", label)
		reply.Sender.OpaqueID = "u_234567abcd"
		token := "@{c1g2/" + label + "}"
		reply.Text = "Hello " + token + ", I can help"
		if err := r.Handle(ctx, reply); err != nil {
			t.Fatal(err)
		}
		waitForSent(t, sender, 4+index*2)
		sender.mu.Lock()
		found := 0
		for _, item := range sender.sent {
			if item.outgoing.OriginEndpoint != "c1g3" || item.outgoing.SourceText != reply.Text {
				continue
			}
			found++
			refs := item.outgoing.References
			if len(refs) != 1 || refs[0].ActorID != first.Sender.OpaqueID || refs[0].Endpoint != transport.EndpointID("c1g2") || refs[0].Name != "Alice Smith" {
				t.Errorf("resolved reference for %q at %s = %+v", label, item.outgoing.Endpoint, refs)
			}
			if !strings.Contains(item.outgoing.Text, token) {
				t.Errorf("text lost posted reference: %q", item.outgoing.Text)
			}
		}
		sender.mu.Unlock()
		if found != 2 {
			t.Fatalf("reference %q reached %d destinations, want 2", label, found)
		}
	}
	// Audio/sticker attribution is a separate text send on WhatsApp, and
	// must carry the same authorized references as a caption or text post.
	media := testIncoming("c1g3", "audio-reference")
	media.Kind = "audio"
	media.Text = "Hi @{c1g2/Alice Smith}"
	media.MediaLoader = func(context.Context) ([]byte, error) { return []byte("media"), nil }
	if err := r.Handle(ctx, media); err != nil {
		t.Fatal(err)
	}
	waitForSent(t, sender, 14)
	sender.mu.Lock()
	defer sender.mu.Unlock()
	companions := 0
	for _, item := range sender.sent[10:] {
		if len(item.outgoing.References) != 1 || item.outgoing.References[0].ActorID != first.Sender.OpaqueID {
			t.Errorf("media reference lost on %s (companion=%t): %+v", item.outgoing.Endpoint, item.outgoing.AttributionOnly, item.outgoing.References)
		}
		if item.outgoing.AttributionOnly {
			companions++
		}
	}
	if companions != 2 {
		t.Fatalf("reference reached %d media companions, want 2", companions)
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
	refs, err := r.crossReferences(ctx, "c1g1", `\@{c1g2/P1} @{c1g2/P9} @{other/P1} @{c1g2/P1}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].Token != "@{c1g2/P1}" {
		t.Fatalf("unexpected resolutions: %+v", refs)
	}
}

func TestNamedAndPseudonymousCrossGroupAddresses(t *testing.T) {
	ctx := context.Background()
	r, db, _ := newTestRouter(t, config.UsernameModePushName)
	_, err := db.DB().ExecContext(ctx, `
		INSERT INTO sync_sets(id) VALUES('mesh');
		INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id)
		VALUES ('c1g1','whatsapp','wa','111@g.us','mesh'),
		       ('c1g2','whatsapp','wa','222@g.us','mesh');`)
	if err != nil {
		t.Fatal(err)
	}
	a1 := "u_abcdefghij"
	a2 := "u_234567abcd"
	if _, err := db.ObserveParticipant(ctx, "c1g1", a1); err != nil {
		t.Fatal(err)
	}
	r.observeReferenceName("c1g1", a1, "Alice Smith")
	pseudonym := identity.Pseudonym(a1)
	shortPseudonym := strings.Join(strings.Fields(pseudonym)[:2], " ")
	for _, input := range []string{
		"Hi @{c1g1/P1}",
		"Hi @{c1g1/Alice Smith}",
		"Hi @{c1g1/" + pseudonym + "}",
		"Hi @{c1g1/" + shortPseudonym + "}",
	} {
		refs, err := r.crossReferences(ctx, "c1g2", input)
		if err != nil || len(refs) != 1 || refs[0].ActorID != a1 ||
			refs[0].Handle != "P1" || refs[0].Name != "Alice Smith" || refs[0].Pseudonym != pseudonym {
			t.Fatalf("resolve %q: %+v (err=%v)", input, refs, err)
		}
		if got := transport.RewriteCrossReferences(input, refs, nil); got != input {
			t.Fatalf("fallback changed posted text %q: %q", input, got)
		}
	}
	if _, err := db.ObserveParticipant(ctx, "c1g1", a2); err != nil {
		t.Fatal(err)
	}
	r.observeReferenceName("c1g1", a2, "Alice Smith")
	refs, err := r.crossReferences(ctx, "c1g2", "Hello @{c1g1/Alice Smith}")
	if err != nil || len(refs) != 0 {
		t.Fatalf("ambiguous aliases must not resolve: %+v (%v)", refs, err)
	}
	refs, err = r.crossReferences(ctx, "c1g2", "Hello @{c1g1/P1}")
	if err != nil || len(refs) != 1 || refs[0].ActorID != a1 {
		t.Fatalf("stable handles must still resolve: %+v (%v)", refs, err)
	}

	// Deterministic pseudonyms, including their stable suffixes, can be
	// resolved after restart using persisted HMAC identities alone.
	r.mu.Lock()
	r.referenceNames = nil
	r.mu.Unlock()
	refs, err = r.crossReferences(ctx, "c1g2", "Hello @{c1g1/"+pseudonym+"}")
	if err != nil || len(refs) != 1 || refs[0].ActorID != a1 ||
		refs[0].Name != "" || refs[0].Pseudonym != pseudonym {
		t.Fatalf("pseudonym after restart: %+v (%v)", refs, err)
	}
	if got := transport.RewriteCrossReferences("Hello @{c1g1/"+pseudonym+"}", refs, nil); got != "Hello @{c1g1/"+pseudonym+"}" {
		t.Fatalf("pseudonym render after restart: %q", got)
	}
	refs, err = r.crossReferences(ctx, "c1g2", "Hello @{c1g1/Alice Smith}")
	if err != nil || len(refs) != 0 {
		t.Fatalf("real name unexpectedly survived restart: %+v (%v)", refs, err)
	}
}

type referenceResolverSender struct {
	*fakeSender
	actor string
	calls int
}

func (s *referenceResolverSender) ResolveParticipant(_ context.Context, _ transport.EndpointID, _ string, _ []string) (string, bool) {
	s.calls++
	return s.actor, s.actor != ""
}

func TestProviderNameResolutionCannotEscapeRegisteredGroupActors(t *testing.T) {
	ctx := context.Background()
	r, db, sender := newTestRouter(t, config.UsernameModePushName)
	if _, err := db.DB().ExecContext(ctx, `INSERT INTO sync_sets(id) VALUES('mesh'); INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id) VALUES ('c1g1','whatsapp','wa','111@g.us','mesh'), ('c1g2','whatsapp','wa','222@g.us','mesh')`); err != nil {
		t.Fatal(err)
	}
	actor := "u_abcdefghij"
	if _, err := db.ObserveParticipant(ctx, "c1g1", actor); err != nil {
		t.Fatal(err)
	}
	resolver := &referenceResolverSender{fakeSender: sender, actor: "u_234567abcd"}
	r.sender = resolver
	refs, err := r.crossReferences(ctx, "c1g2", "Hi @{c1g1/vm}")
	if err != nil || len(refs) != 0 || resolver.calls != 1 {
		t.Fatalf("out-of-group identity resolved: %+v, %v (calls=%d)", refs, err, resolver.calls)
	}
	resolver.actor = actor
	refs, err = r.crossReferences(ctx, "c1g2", "Hi @{c1g1/vm}")
	if err != nil || len(refs) != 1 || refs[0].ActorID != actor {
		t.Fatalf("registered provider lookup failed: %+v, %v", refs, err)
	}
	calls := resolver.calls
	refs, err = r.crossReferences(ctx, "c1g2", "Hi @{outside/vm}")
	if err != nil || len(refs) != 0 || resolver.calls != calls {
		t.Fatalf("unconfigured group reached provider lookup: %+v, %v", refs, err)
	}
}
