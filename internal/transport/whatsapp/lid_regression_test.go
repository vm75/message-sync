package whatsapp

import (
	"context"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
	waCommon "go.mau.fi/whatsmeow/proto/waCommon"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestNormalizeWhatsAppIdentityJIDCanonicalizesHostedLID(t *testing.T) {
	hosted := types.NewJID("100012345678901", types.HostedLIDServer)
	got := normalizeWhatsAppIdentityJID(hosted)
	want := types.NewJID("100012345678901", types.HiddenUserServer)
	if got.String() != want.String() {
		t.Fatalf("normalized hosted LID = %q, want %q", got.String(), want.String())
	}
}

func TestCanonicalSenderIdentityTreatsHostedLIDAndLIDAsSameActor(t *testing.T) {
	ctx := context.Background()
	pn := types.NewJID("15551234567", types.DefaultUserServer)
	lid := types.NewJID("100012345678901", types.HiddenUserServer)
	hosted := types.NewJID("100012345678901", types.HostedLIDServer)

	canonicalLID, phoneLID := CanonicalSenderIdentity(ctx, lid, pn, nil)
	canonicalHosted, phoneHosted := CanonicalSenderIdentity(ctx, hosted, pn, nil)
	if canonicalLID.String() != canonicalHosted.String() {
		t.Fatalf("canonical identities differ: lid=%q hosted=%q", canonicalLID.String(), canonicalHosted.String())
	}
	if canonicalHosted.Server != types.HiddenUserServer {
		t.Fatalf("hosted LID canonical server = %q, want %q", canonicalHosted.Server, types.HiddenUserServer)
	}
	if phoneLID != pn.User || phoneHosted != pn.User {
		t.Fatalf("unexpected phones: lid=%q hosted=%q", phoneLID, phoneHosted)
	}
}

func TestNormalizeMessageHostedLIDAndLIDProduceSameOpaqueID(t *testing.T) {
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	normalizer, err := NewNormalizer(map[string]string{"c1g1": "123456789@g.us"}, hasher, config.UsernameModePushName)
	if err != nil {
		t.Fatal(err)
	}

	chat := types.NewJID("123456789", types.GroupServer)
	pn := types.NewJID("15551234567", types.DefaultUserServer)
	lid := types.NewJID("100012345678901", types.HiddenUserServer)
	hosted := types.NewJID("100012345678901", types.HostedLIDServer)
	body := "hello"
	now := time.Now().UTC()

	makeEvent := func(id string, sender types.JID) *events.Message {
		return &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{
					Chat:      chat,
					Sender:    sender,
					SenderAlt: pn,
					IsGroup:   true,
				},
				ID:        types.MessageID(id),
				PushName:  "Alice",
				Timestamp: now,
			},
			Message: &waE2E.Message{Conversation: &body},
		}
	}

	fromLID, ok := normalizer.NormalizeMessage(makeEvent("lid", lid), true, 1024, nil, nil, nil, nil)
	if !ok {
		t.Fatal("LID message did not normalize")
	}
	fromHosted, ok := normalizer.NormalizeMessage(makeEvent("hosted", hosted), true, 1024, nil, nil, nil, nil)
	if !ok {
		t.Fatal("hosted LID message did not normalize")
	}
	if fromLID.Sender.OpaqueID != fromHosted.Sender.OpaqueID {
		t.Fatalf("OpaqueID differs for @lid and @hosted.lid: %q vs %q", fromLID.Sender.OpaqueID, fromHosted.Sender.OpaqueID)
	}
}

func TestParticipantIdentityTreatsHostedLIDAsCanonicalLID(t *testing.T) {
	lid := types.NewJID("100012345678901", types.HiddenUserServer)
	hosted := types.NewJID("100012345678901", types.HostedLIDServer)
	participant := types.GroupParticipant{JID: lid, LID: lid}
	if !ParticipantMatchesIdentity(context.Background(), participant, hosted, nil) {
		t.Fatal("hosted LID should match the same canonical @lid participant")
	}
}

func TestParticipantPhoneMatchNormalizesHostedLIDBeforeResolver(t *testing.T) {
	pn := types.NewJID("15551234567", types.DefaultUserServer)
	lid := types.NewJID("100012345678901", types.HiddenUserServer)
	hosted := types.NewJID("100012345678901", types.HostedLIDServer)
	participant := types.GroupParticipant{JID: hosted, LID: hosted}

	resolver := func(_ context.Context, jid types.JID) (types.JID, error) {
		if jid.String() == lid.String() {
			return pn, nil
		}
		return types.EmptyJID, nil
	}
	if !ParticipantMatchesPhone(context.Background(), participant, pn.User, resolver) {
		t.Fatal("hosted LID participant should resolve through canonical @lid to its PN")
	}
}

func TestRecoveredMessageUsesSameCanonicalIdentityAsLiveMessage(t *testing.T) {
	// History recovery passes ParseWebMessage output through the same NormalizeMessage
	// path as live ingress. Model the parsed recovered event here and verify that a
	// hosted-LID representation cannot create a second actor identity.
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	normalizer, err := NewNormalizer(map[string]string{"c1g1": "123456789@g.us"}, hasher, config.UsernameModePushName)
	if err != nil {
		t.Fatal(err)
	}
	chat := types.NewJID("123456789", types.GroupServer)
	pn := types.NewJID("15551234567", types.DefaultUserServer)
	lid := types.NewJID("100012345678901", types.HiddenUserServer)
	hosted := types.NewJID("100012345678901", types.HostedLIDServer)
	body := "recovered"
	now := time.Now().UTC()

	makeEvent := func(id string, sender types.JID) *events.Message {
		return &events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: chat, Sender: sender, SenderAlt: pn, IsGroup: true},
				ID:            types.MessageID(id),
				Timestamp:     now,
			},
			Message: &waE2E.Message{Conversation: &body},
		}
	}

	live, ok := normalizer.NormalizeMessage(makeEvent("live", lid), true, 1024, nil, nil, nil, nil)
	if !ok {
		t.Fatal("live message did not normalize")
	}
	recovered, ok := normalizer.NormalizeMessage(makeEvent("history", hosted), true, 1024, nil, nil, nil, nil)
	if !ok {
		t.Fatal("recovered message did not normalize")
	}
	if live.Sender.OpaqueID != recovered.Sender.OpaqueID {
		t.Fatalf("history recovery changed actor identity: live=%q recovered=%q", live.Sender.OpaqueID, recovered.Sender.OpaqueID)
	}
}

func TestLIDReplyAndReactionNormalizeWithCanonicalActorIdentity(t *testing.T) {
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	normalizer, err := NewNormalizer(map[string]string{"c1g1": "123456789@g.us"}, hasher, config.UsernameModePushName)
	if err != nil {
		t.Fatal(err)
	}
	chat := types.NewJID("123456789", types.GroupServer)
	pn := types.NewJID("15551234567", types.DefaultUserServer)
	lid := types.NewJID("100012345678901", types.HiddenUserServer)
	hosted := types.NewJID("100012345678901", types.HostedLIDServer)
	now := time.Now().UTC()

	replyText := "reply"
	quotedText := "original"
	stanzaID := "original-message"
	reply := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: lid, SenderAlt: pn, IsGroup: true},
			ID:            "reply-message",
			Timestamp:     now,
		},
		Message: &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: &replyText,
			ContextInfo: &waE2E.ContextInfo{
				StanzaID:      &stanzaID,
				QuotedMessage: &waE2E.Message{Conversation: &quotedText},
			},
		}},
	}
	normalizedReply, ok := normalizer.NormalizeMessage(reply, true, 1024, nil, nil, nil, nil)
	if !ok || normalizedReply.ReplyTo == nil || normalizedReply.ReplyTo.RemoteMessageID != stanzaID {
		t.Fatalf("LID reply did not preserve reply target: %+v", normalizedReply.ReplyTo)
	}

	reactionTarget := "original-message"
	reactionText := "👍"
	reaction := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: hosted, SenderAlt: pn, IsGroup: true},
			ID:            "reaction-message",
			Timestamp:     now,
		},
		Message: &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{
			Key:  &waCommon.MessageKey{ID: &reactionTarget},
			Text: &reactionText,
		}},
	}
	normalizedReaction, ok := normalizer.NormalizeMessage(reaction, true, 1024, nil, nil, nil, nil)
	if !ok || normalizedReaction.Kind != "reaction" || normalizedReaction.ReplyTo == nil || normalizedReaction.ReplyTo.RemoteMessageID != reactionTarget {
		t.Fatalf("hosted-LID reaction did not preserve target: kind=%q reply=%+v", normalizedReaction.Kind, normalizedReaction.ReplyTo)
	}
	if normalizedReply.Sender.OpaqueID != normalizedReaction.Sender.OpaqueID {
		t.Fatalf("reply/reaction actor identity differs across LID addressing: reply=%q reaction=%q", normalizedReply.Sender.OpaqueID, normalizedReaction.Sender.OpaqueID)
	}
}

func TestRevokeSenderPreservesExactLIDProviderJID(t *testing.T) {
	cache := newParticipantCache(4)
	hosted := types.NewJID("100012345678901", types.HostedLIDServer)
	cache.Add("message-id", hosted.String())

	sender, err := revokeSender(transport.MessageRef{RemoteMessageID: "message-id"}, cache)
	if err != nil {
		t.Fatal(err)
	}
	if sender.String() != hosted.String() {
		t.Fatalf("revoke sender = %q, want exact cached provider JID %q", sender.String(), hosted.String())
	}
}
