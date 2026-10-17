package router

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
)

func TestDiscriminatorPresentation(t *testing.T) {
	ctx := context.Background()
	actor := "u_abcdefghij"
	for _, show := range []bool{false, true} {
		for _, mode := range []config.UsernameMode{config.UsernameModePushName, config.UsernameModeHash} {
			for _, anonymized := range []bool{false, true} {
				t.Run(fmt.Sprintf("show=%t/mode=%s/anonymous=%t", show, mode, anonymized), func(t *testing.T) {
					r := &Router{usernameMode: mode, showDiscriminator: show}
					incoming := transport.Incoming{Endpoint: "source", Sender: transport.Sender{OpaqueID: actor, DisplayName: "Alice", PhoneNumber: "15551234567"}, Text: "hello"}
					sender, _, _, _ := r.presentationForTarget(incoming, routeTarget{Endpoint: "destination", AnonymizeToDiscord: anonymized})
					name := "Alice"
					if anonymized {
						name = identity.PseudonymName(actor)
					}
					if mode == config.UsernameModeHash {
						name = actor
					}
					if show {
						name += " " + identity.Discriminator(actor)
					}
					if got := senderPresentationUsername(sender, mode); got != name {
						t.Fatalf("name = %q, want %q", got, name)
					}
					for _, child := range []*transport.ChildScope{nil, {Kind: transport.ScopeKindTelegramTopic, RemoteID: "123", Label: "Travel"}} {
						incoming.ChildScope = child
						label := "source/" + name
						if child != nil {
							label = "source/Travel/" + name
						}
						text, err := r.forwardedTextFor(ctx, incoming, sender)
						if err != nil || text != "*_"+label+"_*: hello" {
							t.Fatalf("render = %q, %v", text, err)
						}
						poll := r.childAwarePollAttributionFor(ctx, config.ChildContextDisplayFriendly, incoming, child, sender)
						if !strings.Contains(poll, name) {
							t.Fatalf("poll attribution lost name: %q", poll)
						}
					}
					if anonymized && sender.PhoneNumber != "" {
						t.Fatal("anonymized phone leaked")
					}
					if incoming.Sender.DisplayName != "Alice" || incoming.Sender.Discriminator != "" {
						t.Fatal("ingress name was mutated")
					}
				})
			}
		}
	}
}

func TestDiscriminatorReferencesRejectAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		name, first, second, label string
		ordinal                    int
		displayName                string
	}{
		{name: "suffix collision", first: "u_aaaaaaazsi", second: "u_aaaaaaazta", label: "LN9XR"},
		{name: "name collision", first: "u_abcdefghij", second: "u_234567abcd", label: "7WCUX", displayName: "7WCUX"},
		{name: "handle collision", first: "u_aaaaaaadjq", second: "u_234567abcd", label: "P6798", ordinal: 6798},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r, db, fake := newTestRouter(t, config.UsernameModePushName)
			if _, err := db.DB().ExecContext(ctx, `INSERT INTO sync_sets(id) VALUES('mesh'); INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id) VALUES ('c1g1','whatsapp','wa','111@g.us','mesh')`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ObserveParticipant(ctx, "c1g1", tc.first); err != nil {
				t.Fatal(err)
			}
			resolver := &referenceResolverSender{fakeSender: fake, actor: tc.first}
			r.sender = resolver
			token := "@{c1g1/" + tc.label + "}"
			refs, err := r.crossReferences(ctx, "c1g2", token)
			if err != nil || len(refs) != 1 || refs[0].ActorID != tc.first {
				t.Fatalf("unique code failed: %+v, %v", refs, err)
			}
			if _, err := db.ObserveParticipant(ctx, "c1g1", tc.second); err != nil {
				t.Fatal(err)
			}
			if tc.ordinal != 0 {
				if _, err := db.DB().ExecContext(ctx, `UPDATE participant_addresses SET ordinal=? WHERE endpoint_id='c1g1' AND actor_id=?`, tc.ordinal, tc.second); err != nil {
					t.Fatal(err)
				}
			}
			if tc.displayName != "" {
				r.observeReferenceName("c1g1", tc.second, tc.displayName)
			}

			for _, token := range []string{token, strings.ToLower(token)} {
				refs, err = r.crossReferences(ctx, "c1g2", token)
				if err != nil || len(refs) != 0 || resolver.calls != 0 {
					t.Fatalf("ambiguous code resolved: %+v, %v (provider calls=%d)", refs, err, resolver.calls)
				}
				if got := transport.RewriteCrossReferences(token, refs, nil); got != token {
					t.Fatalf("ambiguous token changed: %q", got)
				}
			}
		})
	}
}

func TestDiscriminatorReferencesAreScopedAndEscapeAware(t *testing.T) {
	ctx := context.Background()
	r, db, _ := newTestRouter(t, config.UsernameModePushName)
	if _, err := db.DB().ExecContext(ctx, `INSERT INTO sync_sets(id) VALUES('mesh'); INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id) VALUES ('c1g1','whatsapp','wa','111@g.us','mesh'), ('outside','whatsapp','wa','999@g.us','mesh')`); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"c1g1", "outside"} {
		if _, err := db.ObserveParticipant(ctx, endpoint, "u_abcdefghij"); err != nil {
			t.Fatal(err)
		}
	}
	input := `\@{c1g1/7WCUX} @{outside/7WCUX} @{c1g1/7wcux} @{c1g1/7WCUX}`
	refs, err := r.crossReferences(ctx, "c1g2", input)
	if err != nil || len(refs) != 2 {
		t.Fatalf("scoped references = %+v, %v", refs, err)
	}
	for _, ref := range refs {
		if ref.ActorID != "u_abcdefghij" || ref.Endpoint != "c1g1" {
			t.Fatalf("wrong recipient: %+v", ref)
		}
	}
}

func TestDiscriminatorDisplayReloadDoesNotChangeAddressing(t *testing.T) {
	ctx := context.Background()
	r, db, fake := newTestRouter(t, config.UsernameModePushName)
	if _, err := db.DB().ExecContext(ctx, `INSERT INTO sync_sets(id) VALUES('mesh'); INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id) VALUES ('c1g1','whatsapp','wa','111@g.us','mesh'), ('c1g2','whatsapp','wa','222@g.us','mesh'), ('c1g3','whatsapp','wa','333@g.us','mesh')`); err != nil {
		t.Fatal(err)
	}
	for index, show := range []bool{false, true, false} {
		cfg := testConfig(config.UsernameModePushName)
		cfg.ShowDiscriminator = show
		if err := r.UpdateConfig(cfg); err != nil {
			t.Fatal(err)
		}
		incoming := testIncoming("c1g1", fmt.Sprintf("reload-%d", index))
		if err := r.Handle(ctx, incoming); err != nil {
			t.Fatal(err)
		}
		waitForSent(t, fake, (index+1)*2)
		fake.mu.Lock()
		for _, sent := range fake.sent[index*2:] {
			expected := "*_c1g1/Alice"
			if show {
				expected += " 7WCUX"
			}
			expected += "_*: hello"
			if sent.outgoing.Text != expected {
				t.Errorf("reloaded text=%q want=%q", sent.outgoing.Text, expected)
			}
		}
		fake.mu.Unlock()
		refs, err := r.crossReferences(ctx, "c1g2", "Hi @{c1g1/7WCUX}")
		if err != nil || len(refs) != 1 || refs[0].ActorID != incoming.Sender.OpaqueID {
			t.Fatalf("display setting changed addressing: %+v, %v", refs, err)
		}
	}
}

func TestScopedPresentationAndHistoricalCodeLengths(t *testing.T) {
	ctx := context.Background()
	r, db, _ := newTestRouter(t, config.UsernameModePushName)
	cfg := testConfig(config.UsernameModePushName)
	first, second := config.DefaultSyncSetSettings(), config.DefaultSyncSetSettings()
	first.ShowDiscriminator = true
	first.CodeLength = 8
	second.ShowDiscriminator = true
	second.CodeLength = 10
	second.UsernameMode = config.UsernameModeHash
	cfg.Endpoints["other1"] = config.Endpoint{Transport: config.TransportWhatsApp, ConnectionID: "conn-wa-1", RemoteID: "444@g.us"}
	cfg.Endpoints["other2"] = config.Endpoint{Transport: config.TransportWhatsApp, ConnectionID: "conn-wa-1", RemoteID: "555@g.us"}
	cfg.SyncSets[0].Settings = &first
	cfg.SyncSets = append(cfg.SyncSets, config.SyncSet{ID: "other", Endpoints: []string{"other1", "other2"}, Settings: &second})
	if err := r.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	actor := "u_abcdefghij"
	for _, tc := range []struct {
		ep     transport.EndpointID
		length int
		mode   config.UsernameMode
	}{{"c1g1", 8, config.UsernameModePushName}, {"other1", 10, config.UsernameModeHash}} {
		incoming := transport.Incoming{Endpoint: tc.ep, Sender: transport.Sender{OpaqueID: actor, DisplayName: "Alice"}, Text: "hello"}
		sender, _, _, _ := r.presentationForTarget(incoming, routeTarget{Endpoint: "destination"})
		base := "Alice"
		if tc.mode == config.UsernameModeHash {
			base = actor
		}
		want := base + " " + identity.DiscriminatorForLength(actor, tc.length)
		if got := senderPresentationUsername(sender, tc.mode); got != want {
			t.Fatalf("%s: %q want %q", tc.ep, got, want)
		}
	}
	if _, err := db.DB().Exec(`INSERT INTO sync_sets(id) VALUES('mesh'); INSERT INTO endpoints(alias,transport,connection_id,remote_id,sync_set_id) VALUES('c1g1','whatsapp','wa','111@g.us','mesh')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ObserveParticipant(ctx, "c1g1", actor); err != nil {
		t.Fatal(err)
	}
	for _, length := range []int{5, 7, 8, 10} {
		for _, label := range []string{identity.DiscriminatorForLength(actor, length), identity.PseudonymForLength(actor, length)} {
			refs, err := r.crossReferences(ctx, "c1g2", "@{c1g1/"+label+"}")
			if err != nil || len(refs) != 1 || refs[0].ActorID != actor {
				t.Fatalf("historical length %d label %q failed: %+v %v", length, label, refs, err)
			}
		}
	}
	first.ShowDiscriminator = false
	first.CodeLength = 6
	if err := r.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if r.getShowDiscriminator("c1g1") || !r.getShowDiscriminator("other1") {
		t.Fatal("reload leaked display policy between sets")
	}
}
