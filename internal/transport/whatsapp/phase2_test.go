package whatsapp

import (
	"testing"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestOutgoingTargetsUseConfiguredAliases(t *testing.T) {
	targets, err := outgoingTargets(map[string]string{
		"c1g1": "123456789@g.us",
		"c1g2": "987654321@g.us",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets["c1g1"].String() != "123456789@g.us" || targets["c1g2"].String() != "987654321@g.us" {
		t.Fatalf("unexpected outgoing targets: %#v", targets)
	}
}

func TestNormalizerMarksBridgeOriginMessage(t *testing.T) {
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	normalizer, err := NewNormalizer(map[string]string{"c1g1": "123456789@g.us"}, hasher, config.UsernameModeHash)
	if err != nil {
		t.Fatal(err)
	}
	body := "bridge copy"
	incoming, ok := normalizer.NormalizeMessage(&events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:     types.NewJID("123456789", types.GroupServer),
				Sender:   types.NewJID("15551234567", types.DefaultUserServer),
				IsGroup:  true,
				IsFromMe: true,
			},
			ID: "bridge-copy-id",
		},
		Message: &waE2E.Message{Conversation: &body},
	}, true, 100*1024*1024, nil, nil, nil)
	if !ok {
		t.Fatal("bridge-origin group message was not normalized")
	}
	if !incoming.FromSelf {
		t.Fatal("bridge-origin message was not marked FromSelf")
	}
}
