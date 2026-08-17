package whatsapp

import (
	"strings"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/identity"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestNormalizeConfiguredGroupMessage(t *testing.T) {
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	normalizer, err := NewNormalizer(map[string]string{
		"c1g1": "123456789@g.us",
	}, hasher, "push_name")
	if err != nil {
		t.Fatal(err)
	}

	body := "private message body"
	timestamp := time.Unix(1_700_000_000, 0)
	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:    types.NewJID("123456789", types.GroupServer),
				Sender:  types.NewJID("15551234567", types.DefaultUserServer),
				IsGroup: true,
			},
			ID:        types.MessageID("opaque-remote-id"),
			PushName:  "Alice Example",
			Timestamp: timestamp,
		},
		Message: &waE2E.Message{Conversation: &body},
	}

	incoming, ok := normalizer.NormalizeMessage(evt)
	if !ok {
		t.Fatal("configured group message was ignored")
	}
	if incoming.Endpoint != "c1g1" || incoming.RemoteID != "opaque-remote-id" || incoming.Kind != "text" {
		t.Fatalf("unexpected normalized metadata: %+v", incoming)
	}
	if incoming.Text != body || incoming.Sender.DisplayName != "Alice Example" || !incoming.Timestamp.Equal(timestamp) {
		t.Fatalf("transient fields not preserved: %+v", incoming)
	}
	if incoming.Sender.OpaqueID == "" || strings.Contains(incoming.Sender.OpaqueID, "15551234567") || strings.Contains(incoming.Sender.OpaqueID, "@") {
		t.Fatalf("sender was not normalized to an opaque HMAC identity: %q", incoming.Sender.OpaqueID)
	}
}

func TestNormalizeIgnoresDMAndUnconfiguredGroup(t *testing.T) {
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	normalizer, err := NewNormalizer(map[string]string{"c1g1": "123456789@g.us"}, hasher, "hash")
	if err != nil {
		t.Fatal(err)
	}
	body := "secret"
	sender := types.NewJID("15551234567", types.DefaultUserServer)

	cases := []struct {
		name string
		info types.MessageInfo
	}{
		{
			name: "dm",
			info: types.MessageInfo{MessageSource: types.MessageSource{
				Chat: types.NewJID("15557654321", types.DefaultUserServer), Sender: sender, IsGroup: false,
			}, ID: "dm-id"},
		},
		{
			name: "unconfigured group",
			info: types.MessageInfo{MessageSource: types.MessageSource{
				Chat: types.NewJID("987654321", types.GroupServer), Sender: sender, IsGroup: true,
			}, ID: "group-id"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := normalizer.NormalizeMessage(&events.Message{Info: tc.info, Message: &waE2E.Message{Conversation: &body}}); ok {
				t.Fatalf("unexpected normalized event: %+v", got)
			}
		})
	}
}

func TestHashModeDropsPushName(t *testing.T) {
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	normalizer, err := NewNormalizer(map[string]string{"c1g1": "123456789@g.us"}, hasher, "hash")
	if err != nil {
		t.Fatal(err)
	}
	body := "secret"
	incoming, ok := normalizer.NormalizeMessage(&events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat: types.NewJID("123456789", types.GroupServer), Sender: types.NewJID("15551234567", types.DefaultUserServer), IsGroup: true,
			},
			ID:       "id",
			PushName: "Must Not Escape",
		},
		Message: &waE2E.Message{Conversation: &body},
	})
	if !ok {
		t.Fatal("message was ignored")
	}
	if incoming.Sender.DisplayName != "" {
		t.Fatalf("hash mode retained push name: %q", incoming.Sender.DisplayName)
	}
}
