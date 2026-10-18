package telegram

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/router"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/testutil"
	"github.com/vm75/message-sync/internal/transport"
)

type telegramReferenceDelivery struct {
	outgoing transport.Outgoing
	text     string
}

type telegramReferenceSender struct {
	*Adapter
	api       *crossMentionAPI
	delivered chan telegramReferenceDelivery
}

func (s *telegramReferenceSender) Send(ctx context.Context, outgoing transport.Outgoing) (transport.MessageRef, error) {
	if outgoing.Endpoint != "tg" {
		s.delivered <- telegramReferenceDelivery{outgoing: outgoing, text: outgoing.Text}
		return transport.MessageRef{Endpoint: outgoing.Endpoint, RemoteMessageID: "forwarded"}, nil
	}
	ref, err := s.Adapter.Send(ctx, outgoing)
	if err == nil {
		s.delivered <- telegramReferenceDelivery{outgoing: outgoing, text: s.api.texts[len(s.api.texts)-1]}
	}
	return ref, err
}

func TestTelegramAliasAddressAfterRouterRestart(t *testing.T) {

	for _, observed := range []bool{false, true} {
		t.Run(fmt.Sprintf("native-observed=%t", observed), func(t *testing.T) {
			ctx := context.Background()
			db, err := store.Open(ctx, filepath.Join(t.TempDir(), "sync.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			cfg := &config.Config{
				Endpoints: map[string]config.Endpoint{"tg": {Transport: config.TransportTelegram, ConnectionID: "tg", RemoteID: "-1001234567890"}, "source": {Transport: config.TransportWhatsApp, ConnectionID: "wa", RemoteID: "111@g.us"}},
				SyncSets:  []config.SyncSet{{ID: "mesh", Endpoints: []string{"tg", "source"}}}}
			if err := config.Save(ctx, db.DB(), cfg); err != nil {
				t.Fatal(err)
			}
			newSender := func() *telegramReferenceSender {
				api := &crossMentionAPI{fakeTelegramAPI: &fakeTelegramAPI{}}
				adapter := newOutboundTestAdapter(t, api.fakeTelegramAPI)
				adapter.client = api
				return &telegramReferenceSender{Adapter: adapter, api: api, delivered: make(chan telegramReferenceDelivery, 2)}
			}
			receive := func(sender *telegramReferenceSender) telegramReferenceDelivery {
				select {
				case sent := <-sender.delivered:
					return sent
				case <-time.After(3 * time.Second):
					t.Fatal("timed out waiting for delivery")
					return telegramReferenceDelivery{}
				}
			}
			sender := newSender()
			testutil.SeedRouting(t, db.DB(), cfg)
			mesh, err := router.NewWithHasher(cfg, db, sender, sender.hasher)
			if err != nil {
				t.Fatal(err)
			}
			incoming, ok := sender.normalizer.NormalizeMessage(&models.Message{ID: 101, Date: 1700000000, Chat: models.Chat{ID: -1001234567890, Type: models.ChatTypeSupergroup}, From: &models.User{ID: 123, FirstName: "Alice"}, Text: "hello"}, 999)
			if !ok {
				t.Fatal("Telegram event rejected")
			}
			if err := mesh.Handle(ctx, incoming); err != nil {
				t.Fatal(err)
			}
			forwarded := receive(sender).text
			alias, err := db.AliasByActor(ctx, "mesh", incoming.Sender.OpaqueID)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(forwarded, "Alice ("+alias+")") {
				t.Fatalf("forwarded suffix visibility: %q", forwarded)
			}
			mesh.Close()

			sender = newSender() // Native IDs and names are deliberately absent after restart.
			if observed {
				sender.rememberCrossMentionParticipant("tg", incoming.Sender.OpaqueID, 123)
			}
			testutil.SeedRouting(t, db.DB(), cfg)
			mesh, err = router.NewWithHasher(cfg, db, sender, sender.hasher)
			if err != nil {
				t.Fatal(err)
			}
			defer mesh.Close()
			token := "@{" + alias + "}"
			sourceActor := sender.hasher.UserID("999@lid")
			if err := mesh.Handle(ctx, transport.Incoming{Endpoint: "source", RemoteID: "reference", Kind: "text", Sender: transport.Sender{OpaqueID: sourceActor, DisplayName: "Bob"}, Text: "Hi " + token, Timestamp: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			sent := receive(sender)
			if len(sent.outgoing.References) != 1 || sent.outgoing.References[0].ActorID != incoming.Sender.OpaqueID {
				t.Fatalf("code failed to resolve after restart: %+v", sent.outgoing.References)
			}
			author := "source/Bob (Pale Poppy)"
			if !strings.Contains(sent.text, author) {
				t.Fatalf("Telegram sender attribution = %q, want %q", sent.text, author)
			}
			if observed {
				if !strings.Contains(sent.text, `<a href="tg://user?id=123">@Amber Badger</a>`) || strings.Contains(sent.text, token) {
					t.Fatalf("native Telegram mention missing: %q", sent.text)
				}
			} else if !strings.Contains(sent.text, token) {
				t.Fatalf("cold native cache changed literal token: %q", sent.text)
			}
		})
	}

}
