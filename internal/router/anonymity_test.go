package router

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/transport"
)

func TestEndpointDirectionalAnonymity(t *testing.T) {
	for _, sourceTransport := range []config.Transport{config.TransportWhatsApp, config.TransportDiscord, config.TransportTelegram} {
		for _, incoming := range []bool{false, true} {
			for _, outgoing := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/in=%v/out=%v", sourceTransport, incoming, outgoing), func(t *testing.T) {
					ctx := context.Background()
					db, err := store.Open(ctx, filepath.Join(t.TempDir(), "sync.db"))
					if err != nil {
						t.Fatal(err)
					}
					defer db.Close()
					cfg := &config.Config{Endpoints: map[string]config.Endpoint{
						"source":   {Transport: sourceTransport, RemoteID: "source", AnonymizeIncoming: incoming},
						"wa":       {Transport: config.TransportWhatsApp, RemoteID: "wa", AnonymizeOutgoing: outgoing},
						"dc":       {Transport: config.TransportDiscord, RemoteID: "dc", AnonymizeOutgoing: outgoing},
						"tg":       {Transport: config.TransportTelegram, RemoteID: "tg", AnonymizeOutgoing: outgoing},
						"ordinary": {Transport: config.TransportTelegram, RemoteID: "ordinary"},
					}, SyncSets: []config.SyncSet{{ID: "mesh", Endpoints: []string{"source", "wa", "dc", "tg", "ordinary"}}}}
					persistRouterConfig(t, db, cfg)
					h, _ := identity.New([]byte("0123456789abcdef0123456789abcdef"))
					fake := &fakeSender{}
					r, err := NewWithHasher(cfg, db, fake, h)
					if err != nil {
						t.Fatal(err)
					}
					defer r.Close()
					in := transport.Incoming{Endpoint: "source", RemoteID: "message", Kind: "text", Text: "hello @12345", Sender: transport.Sender{OpaqueID: h.UserID("author"), DisplayName: "Alice", PhoneNumber: "+15551234"}, Mentions: []transport.Mention{{RemoteID: "12345", Name: "Bob"}}, QuotedText: "*_source/Carol_*: quoted", Timestamp: time.Now().UTC()}
					if err := r.Handle(ctx, in); err != nil {
						t.Fatal(err)
					}
					waitForSent(t, fake, 4)
					for _, sent := range fake.sent {
						out := sent.outgoing
						want := incoming || (out.Endpoint != "ordinary" && outgoing)
						if out.Sender.Anonymized != want || out.ReferenceAnonymized != want {
							t.Fatalf("%s anonymity=%v references=%v want=%v", out.Endpoint, out.Sender.Anonymized, out.ReferenceAnonymized, want)
						}
						if want {
							if out.Sender.DisplayName != out.Sender.Alias || out.Sender.PhoneNumber != "" || strings.Contains(out.Text, "Alice") || strings.Contains(out.QuotedText, "Carol") || out.Mentions[0].Name == "Bob" {
								t.Fatalf("%s leaked presentation: %+v", out.Endpoint, out)
							}
						} else if out.Sender.DisplayName != "Alice" || !strings.Contains(out.Text, "Alice") {
							t.Fatalf("%s lost ordinary presentation", out.Endpoint)
						}
					}
					mutation := in
					mutation.Kind = "edit"
					mutation.RemoteID = "edit"
					mutation.ReplyTo = &transport.MessageRef{Endpoint: "source", RemoteMessageID: "message"}
					mutation.Text = "edited"
					if err := r.Handle(ctx, mutation); err != nil {
						t.Fatal(err)
					}
					waitForMutations(t, fake, 4, 0, 0)
					for _, edit := range fake.edited {
						want := incoming || (edit.ref.Endpoint != "ordinary" && outgoing)
						if strings.Contains(edit.text, "Alice") == want {
							t.Fatalf("%s edit anonymity wrong: %s", edit.ref.Endpoint, edit.text)
						}
					}
					mutation.Kind = "reaction"
					mutation.RemoteID = "reaction"
					mutation.Text = "👍"
					if err := r.Handle(ctx, mutation); err != nil {
						t.Fatal(err)
					}
					waitForMutations(t, fake, 0, 4, 0)
					for _, reaction := range fake.reacted {
						want := incoming || (reaction.Endpoint != "ordinary" && outgoing)
						if strings.Contains(reaction.FallbackText, "Alice") == want {
							t.Fatalf("%s reaction anonymity wrong: %s", reaction.Endpoint, reaction.FallbackText)
						}
					}
					// Reload both flags off and verify a subsequent message uses ordinary presentation.
					for alias, ep := range cfg.Endpoints {
						ep.AnonymizeIncoming = false
						ep.AnonymizeOutgoing = false
						cfg.Endpoints[alias] = ep
					}
					if err := r.UpdateConfig(cfg); err != nil {
						t.Fatal(err)
					}
					in.RemoteID = "after-reload"
					if err := r.Handle(ctx, in); err != nil {
						t.Fatal(err)
					}
					waitForSent(t, fake, 8)
					for _, sent := range fake.sent[4:] {
						if sent.outgoing.Sender.Anonymized {
							t.Fatal("reload retained anonymity")
						}
					}
				})
			}
		}
	}
}

func TestDirectionalAnonymityRequiresHasher(t *testing.T) {
	for _, incoming := range []bool{false, true} {
		t.Run(fmt.Sprintf("incoming=%v", incoming), func(t *testing.T) {
			ctx := context.Background()
			db, err := store.Open(ctx, filepath.Join(t.TempDir(), "sync.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			cfg := &config.Config{Endpoints: map[string]config.Endpoint{"wa": {Transport: config.TransportWhatsApp, RemoteID: "1@g.us"}}, SyncSets: []config.SyncSet{{ID: "mesh", Endpoints: []string{"wa"}}}}
			r, err := New(cfg, db, &fakeSender{})
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			ep := cfg.Endpoints["wa"]
			ep.AnonymizeIncoming = incoming
			ep.AnonymizeOutgoing = !incoming
			cfg.Endpoints["wa"] = ep
			if err := r.UpdateConfig(cfg); err == nil {
				t.Fatal("reload accepted anonymity without hasher")
			}
			if other, err := New(cfg, db, &fakeSender{}); err == nil {
				other.Close()
				t.Fatal("constructor accepted anonymity without hasher")
			}
		})
	}
}
