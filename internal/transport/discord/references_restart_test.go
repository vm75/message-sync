package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/router"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/testutil"
	"github.com/vm75/message-sync/internal/transport"
)

func TestForwardedDiscordNameCanBeAddressedAfterRestart(t *testing.T) {

	for _, anonymized := range []bool{false, true} {
		t.Run(fmt.Sprintf("anonymized=%t", anonymized), func(t *testing.T) {
			ctx := context.Background()
			hasher, err := identity.New([]byte("01234567890123456789012345678901"))
			if err != nil {
				t.Fatal(err)
			}
			db, err := store.Open(ctx, filepath.Join(t.TempDir(), "sync.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			cfg := &config.Config{

				Endpoints: map[string]config.Endpoint{
					"dg1": {Transport: config.TransportDiscord, ConnectionID: "dc", RemoteID: testChannelID},
					"wg1": {Transport: config.TransportWhatsApp, ConnectionID: "wa", RemoteID: "111@g.us"},
				},
				SyncSets: []config.SyncSet{{ID: "mesh", Endpoints: []string{"dg1", "wg1"}, AnonymizeToDiscord: anonymized}},
			}
			if err := config.Save(ctx, db.DB(), cfg); err != nil {
				t.Fatal(err)
			}
			api := &e2eDiscordAPI{}
			webhook := &managedWebhookClient{api: api, hooks: map[string]managedWebhookCredential{testChannelID: {id: "hook", token: "test-token"}}}
			wa := &e2eWhatsAppOutbound{}
			newAdapter := func() *Adapter {
				normalizer, err := NewNormalizer(map[string]string{"dg1": testChannelID}, hasher)
				if err != nil {
					t.Fatal(err)
				}
				session, err := discordgo.New("Bot test-token")
				if err != nil {
					t.Fatal(err)
				}
				session.Client = &http.Client{Transport: mentionRoundTripper(func(req *http.Request) (*http.Response, error) {
					member := &discordgo.Member{User: &discordgo.User{ID: testAuthorID, Username: "vm", GlobalName: "vm"}}
					var value any
					switch req.URL.Path {
					case "/api/v9/channels/" + testChannelID:
						value = &discordgo.Channel{ID: testChannelID, GuildID: testGuildID}
					case "/api/v9/guilds/" + testGuildID + "/members", "/api/v9/guilds/" + testGuildID + "/members/search":
						value = []*discordgo.Member{member}
					case "/api/v9/guilds/" + testGuildID + "/members/" + testAuthorID:
						value = member
					case "/api/v9/guilds/" + testGuildID:
						value = &discordgo.Guild{ID: testGuildID, Roles: []*discordgo.Role{{ID: testGuildID, Permissions: discordgo.PermissionViewChannel}}}
					default:
						t.Fatalf("unexpected provider operation: %s", req.URL.Path)
					}
					body, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
				})}
				return &Adapter{session: session, api: api, hasher: hasher, normalizer: normalizer, webhook: webhook, targets: map[transport.EndpointID]string{"dg1": testChannelID}, events: make(chan transport.Incoming, 1)}
			}
			adapter := newAdapter()
			registry, err := router.NewAdapterRegistry(cfg, map[string]router.OutboundAdapter{"dc": adapter, "wa": wa})
			if err != nil {
				t.Fatal(err)
			}
			testutil.SeedRouting(t, db.DB(), cfg)
			mesh, err := router.NewWithHasher(cfg, db, registry, hasher)
			if err != nil {
				t.Fatal(err)
			}
			adapter.handleMessageCreate(adapter.session, &discordgo.MessageCreate{Message: &discordgo.Message{ID: "source-discord", ChannelID: testChannelID, GuildID: testGuildID, Author: &discordgo.User{ID: testAuthorID, Username: "vm", GlobalName: "vm"}, Content: "hello", Timestamp: time.Now().UTC()}})
			incoming := <-adapter.events
			if err := mesh.Handle(ctx, incoming); err != nil {
				t.Fatal(err)
			}
			waitForWebhookCount(t, wa.sentCount, 1, "WhatsApp forwards")
			wa.mu.Lock()
			forwarded := wa.sent[0].Text
			wa.mu.Unlock()
			expected := "*_dg1/vm (Amber Badger)"
			expected += "_*: hello"
			if forwarded != expected {
				t.Fatalf("visible Discord author = %q", forwarded)
			}
			mesh.Close()
			alias, err := db.AliasByActor(ctx, "mesh", incoming.Sender.OpaqueID)
			if err != nil {
				t.Fatal(err)
			}
			for i, label := range []string{alias, strings.ToLower(alias)} {
				if err := registry.RegisterAdapter("dc", newAdapter()); err != nil {
					t.Fatal(err)
				}
				testutil.SeedRouting(t, db.DB(), cfg)
				mesh, err = router.NewWithHasher(cfg, db, registry, hasher)
				if err != nil {
					t.Fatal(err)
				}
				if err := mesh.Handle(ctx, transport.Incoming{Endpoint: "wg1", RemoteID: fmt.Sprintf("reference-from-whatsapp-%d", i), Kind: "text", Sender: transport.Sender{OpaqueID: hasher.UserID("999@lid"), DisplayName: "Sender"}, Text: "Hi @{" + label + "}", Timestamp: time.Now().UTC()}); err != nil {
					t.Fatal(err)
				}
				waitForWebhookSends(t, api, i+1)
				_, _, messages, _, _, _, _ := api.snapshot()
				message := messages[i]
				senderName := "Sender (Pale Poppy)"
				if anonymized {
					senderName = "Pale Poppy"
				}
				if message.Username != "wg1/"+senderName {
					t.Errorf("webhook sender = %q, want %q", message.Username, "wg1/"+senderName)
				}

				if message.Content != "Hi <@"+testAuthorID+">" || len(message.AllowedUserIDs) != 1 || message.AllowedUserIDs[0] != testAuthorID {
					t.Errorf("old forwarded label %q did not become a chip: %+v", label, message)
				}
				mesh.Close()
			}
		})
	}

}
