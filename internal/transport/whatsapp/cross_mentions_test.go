package whatsapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
	"go.mau.fi/whatsmeow"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

func TestWhatsAppReferencesFallbackWithoutConnectedClient(t *testing.T) {
	adapter := &Adapter{}
	for _, tc := range []struct {
		anonymized bool
		want       string
	}{
		{anonymized: true, want: "*_other/Sender_*: Hi @{g1/P1}"},
		{anonymized: false, want: "*_other/Sender_*: Hi @{g1/P1}"},
	} {
		outgoing := transport.Outgoing{
			Endpoint: "g2", OriginEndpoint: "other", Kind: "text",
			Text:                "*_other/Sender_*: Hi @{g1/P1}",
			References:          []transport.CrossReference{{Token: "@{g1/P1}", Endpoint: "g1", ActorID: "u_abcdefghij", Handle: "P1", Name: "Alice", Pseudonym: "Rust Ocean Q7M5K"}},
			ReferenceAnonymized: tc.anonymized,
		}
		rendered, info := adapter.nativeCrossReferences(context.Background(), types.NewJID("98765", types.GroupServer), outgoing, nil, nil)
		if rendered != tc.want || info != nil {
			t.Errorf("WhatsApp fallback = %q (context=%v), want %q", rendered, info, tc.want)
		}
	}
}

func TestWhatsAppCrossReferenceMediaCaption(t *testing.T) {
	hasher, err := identity.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &Adapter{normalizer: &Normalizer{hasher: hasher}}
	lid := types.NewJID("987654321012345", types.HiddenUserServer)
	for _, kind := range []string{"image", "video", "document"} {
		t.Run(kind, func(t *testing.T) {
			outgoing := transport.Outgoing{Endpoint: "g2", Kind: kind, Text: "Hi @{g1/Alice}", References: []transport.CrossReference{{Token: "@{g1/Alice}", Endpoint: "g1", ActorID: hasher.UserID(lid.String())}}}
			rendered, info := adapter.nativeCrossReferences(context.Background(), types.NewJID("222", types.GroupServer), outgoing, nil, func(context.Context, types.JID) (*types.GroupInfo, error) {
				return &types.GroupInfo{Participants: []types.GroupParticipant{{JID: lid}}}, nil
			})
			outgoing.Text = rendered
			message := &waE2E.Message{}
			if err := populateMediaMessage(message, outgoing, whatsmeow.UploadResponse{}, info); err != nil {
				t.Fatal(err)
			}
			var caption string
			switch kind {
			case "image":
				caption, info = message.GetImageMessage().GetCaption(), message.GetImageMessage().GetContextInfo()
			case "video":
				caption, info = message.GetVideoMessage().GetCaption(), message.GetVideoMessage().GetContextInfo()
			case "document":
				caption, info = message.GetDocumentMessage().GetCaption(), message.GetDocumentMessage().GetContextInfo()
			}
			if caption != "Hi @"+lid.User || len(info.GetMentionedJID()) != 1 || info.GetMentionedJID()[0] != lid.String() {
				t.Fatalf("native caption = %q, mentions = %v", caption, info.GetMentionedJID())
			}
		})
	}
}

func TestWhatsAppNativeCrossReferences(t *testing.T) {
	hasher, err := identity.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &Adapter{normalizer: &Normalizer{hasher: hasher}}
	lid := types.NewJID("987654321012345", types.HiddenUserServer)
	pn := types.NewJID("15551234567", types.DefaultUserServer)
	member := types.GroupParticipant{JID: pn, LID: lid, PhoneNumber: pn}
	target := types.NewJID("222", types.GroupServer)
	for _, tc := range []struct {
		name         string
		destination  transport.EndpointID
		actor        types.JID
		participants []types.GroupParticipant
		lookupErr    error
		native       bool
	}{
		{name: "addressed group", destination: "g1", actor: lid, participants: []types.GroupParticipant{member}, native: true},
		{name: "shared group", destination: "g2", actor: lid, participants: []types.GroupParticipant{member}, native: true},
		{name: "previous PN actor", destination: "g2", actor: pn, participants: []types.GroupParticipant{member}, native: true},
		{name: "absent member", destination: "g2", actor: lid},
		{name: "PN only cannot tag", destination: "g1", actor: pn, participants: []types.GroupParticipant{{JID: pn}}},
		{name: "lookup unavailable", destination: "g1", actor: lid, lookupErr: errors.New("unavailable")},
	} {
		for _, label := range []string{"Alice Smith", identity.Pseudonym(hasher.UserID(tc.actor.String())), "P1"} {
			t.Run(tc.name+"/"+label, func(t *testing.T) {
				token := "@{g1/" + label + "}"
				body := "*_source/Sender_*: Hi " + token + ", " + token + ` and \` + token
				stanza := "reply-marker"
				info := &waE2E.ContextInfo{StanzaID: &stanza}
				outgoing := transport.Outgoing{Endpoint: tc.destination, Kind: "text", Text: body,
					References: []transport.CrossReference{{Token: token, Endpoint: "g1", ActorID: hasher.UserID(tc.actor.String()), Name: "Alice Smith", Handle: "P1"}}}
				got, gotInfo := adapter.nativeCrossReferences(context.Background(), target, outgoing, info, func(_ context.Context, jid types.JID) (*types.GroupInfo, error) {
					if jid != target {
						t.Fatalf("looked up a group other than the destination: %v", jid)
					}
					return &types.GroupInfo{Participants: tc.participants}, tc.lookupErr
				})
				want := body
				if tc.native {
					want = "*_source/Sender_*: Hi @" + lid.User + ", @" + lid.User + ` and \` + token
					if len(gotInfo.GetMentionedJID()) != 1 || gotInfo.GetMentionedJID()[0] != lid.String() {
						t.Fatalf("native mention metadata = %v", gotInfo.GetMentionedJID())
					}
				} else if len(gotInfo.GetMentionedJID()) != 0 {
					t.Fatalf("fallback unexpectedly tagged: %v", gotInfo.GetMentionedJID())
				}
				if got != want || gotInfo.GetStanzaID() != stanza || strings.Contains(got, pn.User) {
					t.Fatalf("rendered = %q, want %q; reply = %q", got, want, gotInfo.GetStanzaID())
				}
			})
		}
	}
}

func TestResolveWhatsAppParticipantNameAfterRestart(t *testing.T) {
	hasher, err := identity.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	first := types.NewJID("111111111111111", types.HiddenUserServer)
	second := types.NewJID("222222222222222", types.HiddenUserServer)
	actor := hasher.UserID(first.String())
	otherActor := hasher.UserID(second.String())
	adapter := &Adapter{normalizer: &Normalizer{hasher: hasher}}
	for _, tc := range []struct {
		name      string
		label     string
		actors    []string
		members   []types.GroupParticipant
		lookupErr error
		want      bool
	}{
		{name: "push name", label: "Alice", actors: []string{actor}, members: []types.GroupParticipant{{JID: first}}, want: true},
		{name: "contact alias", label: "Alice Smith", actors: []string{actor}, members: []types.GroupParticipant{{JID: first}}, want: true},
		{name: "normalized alias", label: " ALICE   SMITH ", actors: []string{actor}, members: []types.GroupParticipant{{JID: first}}, want: true},
		{name: "unregistered user", label: "Alice", actors: []string{otherActor}, members: []types.GroupParticipant{{JID: first}}},
		{name: "ambiguous name", label: "Alice", actors: []string{actor, otherActor}, members: []types.GroupParticipant{{JID: first}, {JID: second}}},
		{name: "absent member", label: "Alice", actors: []string{actor}},
		{name: "lookup failure", label: "Alice", actors: []string{actor}, lookupErr: errors.New("unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, found := adapter.resolveParticipantName(context.Background(), types.NewJID("333", types.GroupServer), tc.label, tc.actors, func(context.Context, types.JID) (*types.GroupInfo, error) {
				return &types.GroupInfo{Participants: tc.members}, tc.lookupErr
			}, func(types.JID) types.ContactInfo {
				return types.ContactInfo{Found: true, PushName: "Alice", FullName: "Alice Smith"}
			})
			if found != tc.want || tc.want && got != actor || !tc.want && got != "" {
				t.Fatalf("resolved %q = %q, %t; want found=%t", tc.label, got, found, tc.want)
			}
		})
	}
}
