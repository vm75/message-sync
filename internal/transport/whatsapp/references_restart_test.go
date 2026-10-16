package whatsapp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/router"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/transport"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

type restartWASent struct {
	outgoing transport.Outgoing
	text     string
	info     *waE2E.ContextInfo
}

type restartWASender struct {
	adapter  *Adapter
	groups   map[transport.EndpointID]types.JID
	roster   func(context.Context, types.JID) (*types.GroupInfo, error)
	contacts ContactGetter
	mu       sync.Mutex
	sent     []restartWASent
}

func (s *restartWASender) Send(ctx context.Context, outgoing transport.Outgoing) (transport.MessageRef, error) {
	text, info := s.adapter.nativeCrossReferences(ctx, s.groups[outgoing.Endpoint], outgoing, nil, s.roster)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, restartWASent{outgoing: outgoing, text: text, info: info})
	return transport.MessageRef{Endpoint: outgoing.Endpoint, RemoteMessageID: fmt.Sprintf("copy-%d", len(s.sent))}, nil
}
func (s *restartWASender) ResolveParticipant(ctx context.Context, endpoint transport.EndpointID, label string, actors []string) (string, bool) {
	return s.adapter.resolveParticipantName(ctx, s.groups[endpoint], label, actors, s.roster, s.contacts)
}
func (*restartWASender) Edit(context.Context, transport.MessageRef, string) error { return nil }
func (*restartWASender) Delete(context.Context, transport.MessageRef) error       { return nil }
func (*restartWASender) React(context.Context, transport.Reaction) error          { return nil }
func (s *restartWASender) snapshot(t *testing.T, want int) []restartWASent {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		got := append([]restartWASent(nil), s.sent...)
		s.mu.Unlock()
		if len(got) >= want {
			return got
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for routed WhatsApp messages")
	return nil
}

func TestForwardedWhatsAppNameCanBeAddressedAfterRestart(t *testing.T) {
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
			cfg := &config.Config{Endpoints: map[string]config.Endpoint{}, SyncSets: []config.SyncSet{{ID: "mesh", Endpoints: []string{"wg1", "wg2", "wg3"}, AnonymizeToDiscord: anonymized}}, Identity: config.Identity{UsernameMode: config.UsernameModePushName}}
			groupIDs := map[string]string{"wg1": "111@g.us", "wg2": "222@g.us", "wg3": "333@g.us"}
			groups := make(map[transport.EndpointID]types.JID)
			for alias, raw := range groupIDs {
				cfg.Endpoints[alias] = config.Endpoint{Transport: config.TransportWhatsApp, ConnectionID: "wa", RemoteID: raw}
				groups[transport.EndpointID(alias)], _ = types.ParseJID(raw)
			}
			if err := config.Save(ctx, db.DB(), cfg); err != nil {
				t.Fatal(err)
			}
			normalizer, err := NewNormalizer(groupIDs, hasher, config.UsernameModePushName)
			if err != nil {
				t.Fatal(err)
			}
			lid := types.NewJID("987654321012345", types.HiddenUserServer)
			newSender := func() *restartWASender {
				return &restartWASender{adapter: &Adapter{normalizer: normalizer}, groups: groups,
					roster: func(_ context.Context, group types.JID) (*types.GroupInfo, error) {
						if group == groups["wg2"] {
							return &types.GroupInfo{Participants: []types.GroupParticipant{{JID: lid}}}, nil
						}
						return &types.GroupInfo{}, nil
					},
					contacts: func(jid types.JID) types.ContactInfo {
						if jid == lid {
							return types.ContactInfo{Found: true, PushName: "Alice"}
						}
						return types.ContactInfo{}
					},
				}
			}
			sender := newSender()
			registry, err := router.NewAdapterRegistry(cfg, map[string]router.OutboundAdapter{"wa": sender})
			if err != nil {
				t.Fatal(err)
			}
			mesh, err := router.NewWithHasher(cfg, db, registry, hasher)
			if err != nil {
				t.Fatal(err)
			}
			incoming, ok := normalizer.NormalizeMessage(&events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: groups["wg2"], Sender: lid, IsGroup: true}, ID: "source-wa", PushName: "Alice", Timestamp: time.Now().UTC()}, Message: &waE2E.Message{Conversation: proto.String("hello")}}, false, 0, nil, nil, nil, nil)
			if !ok {
				t.Fatal("source WhatsApp message was rejected")
			}
			if err := mesh.Handle(ctx, incoming); err != nil {
				t.Fatal(err)
			}
			for _, sent := range sender.snapshot(t, 2) {
				if sent.text != "*_wg2/Alice_*: hello" {
					t.Fatalf("forwarded name = %q", sent.text)
				}
			}
			mesh.Close()
			pseudonym := identity.Pseudonym(incoming.Sender.OpaqueID)
			for i, label := range []string{"Alice", pseudonym, strings.Join(strings.Fields(pseudonym)[:2], " "), "P1"} {
				sender = newSender()
				if err := registry.RegisterAdapter("wa", sender); err != nil {
					t.Fatal(err)
				}
				mesh, err = router.NewWithHasher(cfg, db, registry, hasher)
				if err != nil {
					t.Fatal(err)
				}
				token := "@{wg2/" + label + "}"
				if err := mesh.Handle(ctx, transport.Incoming{Endpoint: "wg1", RemoteID: fmt.Sprintf("reference-%d", i), Kind: "text", Sender: transport.Sender{OpaqueID: hasher.UserID("999@lid"), DisplayName: "Sender"}, Text: "Hi " + token, Timestamp: time.Now().UTC()}); err != nil {
					t.Fatal(err)
				}
				for _, sent := range sender.snapshot(t, 2) {
					if sent.outgoing.Endpoint == "wg2" {
						if !strings.HasSuffix(sent.text, "Hi @"+lid.User) || len(sent.info.GetMentionedJID()) != 1 || sent.info.GetMentionedJID()[0] != lid.String() {
							t.Errorf("old %q reference did not become a WhatsApp chip: %q (%v)", label, sent.text, sent.info.GetMentionedJID())
						}
					} else if !strings.HasSuffix(sent.text, "Hi "+token) || len(sent.info.GetMentionedJID()) != 0 {
						t.Errorf("nonmember group changed posted reference: %q", sent.text)
					}
				}
				mesh.Close()
			}
		})
	}
}
