package whatsapp

import (
	"context"
	"testing"

	"github.com/vm75/message-sync/internal/transport"
	"go.mau.fi/whatsmeow/types"
)

func TestWhatsAppReferencesFallbackWithoutConnectedClient(t *testing.T) {
	adapter := &Adapter{}
	for _, tc := range []struct {
		anonymized bool
		want       string
	}{
		{anonymized: true, want: "*_other/Sender_*: Hi @g1/P1"},
		{anonymized: false, want: "*_other/Sender_*: Hi @g1/Alice"},
	} {
		outgoing := transport.Outgoing{
			Endpoint: "g2", OriginEndpoint: "other", Kind: "text",
			Text:                "*_other/Sender_*: Hi @{g1/P1}",
			References:          []transport.CrossReference{{Token: "@{g1/P1}", Endpoint: "g1", ActorID: "u_abcdefghij", Handle: "P1", Name: "Alice"}},
			ReferenceAnonymized: tc.anonymized,
		}
		rendered, info := adapter.nativeCrossReferences(context.Background(), types.NewJID("98765", types.GroupServer), outgoing, nil)
		if rendered != tc.want || info != nil {
			t.Errorf("WhatsApp fallback = %q (context=%v), want %q", rendered, info, tc.want)
		}
	}
}
