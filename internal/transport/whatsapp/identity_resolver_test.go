package whatsapp

import (
	"context"
	"testing"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	waCommon "go.mau.fi/whatsmeow/proto/waCommon"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestCanonicalSenderIdentity_Priority(t *testing.T) {
	ctx := context.Background()
	pnJID := types.NewJID("15551234567", types.DefaultUserServer)
	lidJID := types.NewJID("100012345678901", types.HiddenUserServer)
	hostedLID := types.NewJID("100098765432100", types.HostedLIDServer)

	mockResolver := func(expectedTarget, returnAlt types.JID) JIDResolver {
		return func(ctx context.Context, jid types.JID) (types.JID, error) {
			if jid.ToNonAD().String() == expectedTarget.ToNonAD().String() {
				return returnAlt, nil
			}
			return types.EmptyJID, nil
		}
	}

	t.Run("primary sender is LID", func(t *testing.T) {
		canonical, phone := CanonicalSenderIdentity(ctx, lidJID, pnJID, nil)
		if canonical.String() != lidJID.String() {
			t.Fatalf("expected canonical %s, got %s", lidJID, canonical)
		}
		if phone != "15551234567" {
			t.Fatalf("expected phone 15551234567, got %s", phone)
		}
	})

	t.Run("primary sender is hosted LID", func(t *testing.T) {
		canonical, phone := CanonicalSenderIdentity(ctx, hostedLID, pnJID, nil)
		expected := types.NewJID(hostedLID.User, types.HiddenUserServer)
		if canonical.String() != expected.String() {
			t.Fatalf("expected canonical %s, got %s", expected, canonical)
		}
		if phone != "15551234567" {
			t.Fatalf("expected phone 15551234567, got %s", phone)
		}
	})

	t.Run("alternate sender is LID", func(t *testing.T) {
		canonical, phone := CanonicalSenderIdentity(ctx, pnJID, lidJID, nil)
		if canonical.String() != lidJID.String() {
			t.Fatalf("expected canonical %s, got %s", lidJID, canonical)
		}
		if phone != "15551234567" {
			t.Fatalf("expected phone 15551234567, got %s", phone)
		}
	})

	t.Run("sender is PN and resolver maps to LID", func(t *testing.T) {
		canonical, phone := CanonicalSenderIdentity(ctx, pnJID, types.EmptyJID, mockResolver(pnJID, lidJID))
		if canonical.String() != lidJID.String() {
			t.Fatalf("expected canonical %s, got %s", lidJID, canonical)
		}
		if phone != "15551234567" {
			t.Fatalf("expected phone 15551234567, got %s", phone)
		}
	})

	t.Run("fallback to PN when no LID available", func(t *testing.T) {
		canonical, phone := CanonicalSenderIdentity(ctx, pnJID, types.EmptyJID, nil)
		if canonical.String() != pnJID.String() {
			t.Fatalf("expected canonical %s, got %s", pnJID, canonical)
		}
		if phone != "15551234567" {
			t.Fatalf("expected phone 15551234567, got %s", phone)
		}
	})

	t.Run("LID-only sender never treats LID numeric string as phone", func(t *testing.T) {
		canonical, phone := CanonicalSenderIdentity(ctx, lidJID, types.EmptyJID, nil)
		if canonical.String() != lidJID.String() {
			t.Fatalf("expected canonical %s, got %s", lidJID, canonical)
		}
		if phone != "" {
			t.Fatalf("LID user component must not be treated as a phone number, got %q", phone)
		}
	})

	t.Run("LID sender with resolver resolving to PN", func(t *testing.T) {
		canonical, phone := CanonicalSenderIdentity(ctx, lidJID, types.EmptyJID, mockResolver(lidJID, pnJID))
		if canonical.String() != lidJID.String() {
			t.Fatalf("expected canonical %s, got %s", lidJID, canonical)
		}
		if phone != "15551234567" {
			t.Fatalf("expected phone 15551234567 from resolver, got %s", phone)
		}
	})
}

func TestNormalizeMessage_OpaqueIDConsistencyAcrossRepresentations(t *testing.T) {
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	normalizer, err := NewNormalizer(map[string]string{"c1g1": "123456789@g.us"}, hasher, config.UsernameModePushName)
	if err != nil {
		t.Fatal(err)
	}

	chatJID := types.NewJID("123456789", types.GroupServer)
	pnJID := types.NewJID("15551234567", types.DefaultUserServer)
	lidJID := types.NewJID("100012345678901", types.HiddenUserServer)
	now := time.Now().UTC().Truncate(time.Second)
	body := "hello from WhatsApp"

	resolver := func(ctx context.Context, jid types.JID) (types.JID, error) {
		if jid.ToNonAD().String() == pnJID.ToNonAD().String() {
			return lidJID, nil
		}
		if jid.ToNonAD().String() == lidJID.ToNonAD().String() {
			return pnJID, nil
		}
		return types.EmptyJID, nil
	}

	// 1. Inbound message where Sender is LID and SenderAlt is PN
	evt1 := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:      chatJID,
				Sender:    lidJID,
				SenderAlt: pnJID,
				IsGroup:   true,
			},
			ID:        "msg-1",
			PushName:  "Alice",
			Timestamp: now,
		},
		Message: &waE2E.Message{Conversation: &body},
	}
	norm1, ok1 := normalizer.NormalizeMessage(evt1, true, 1024*1024, nil, nil, nil, resolver)
	if !ok1 {
		t.Fatal("evt1 failed normalization")
	}

	// 2. Inbound message where Sender is PN and SenderAlt is LID
	evt2 := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:      chatJID,
				Sender:    pnJID,
				SenderAlt: lidJID,
				IsGroup:   true,
			},
			ID:        "msg-2",
			PushName:  "Alice",
			Timestamp: now,
		},
		Message: &waE2E.Message{Conversation: &body},
	}
	norm2, ok2 := normalizer.NormalizeMessage(evt2, true, 1024*1024, nil, nil, nil, resolver)
	if !ok2 {
		t.Fatal("evt2 failed normalization")
	}

	// 3. Inbound message where Sender is PN, SenderAlt is empty, but resolver resolves LID
	evt3 := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:    chatJID,
				Sender:  pnJID,
				IsGroup: true,
			},
			ID:        "msg-3",
			PushName:  "Alice",
			Timestamp: now,
		},
		Message: &waE2E.Message{Conversation: &body},
	}
	norm3, ok3 := normalizer.NormalizeMessage(evt3, true, 1024*1024, nil, nil, nil, resolver)
	if !ok3 {
		t.Fatal("evt3 failed normalization")
	}

	// Verify all 3 produce the exact same OpaqueID
	if norm1.Sender.OpaqueID == "" {
		t.Fatal("OpaqueID must not be empty")
	}
	if norm1.Sender.OpaqueID != norm2.Sender.OpaqueID {
		t.Fatalf("OpaqueID mismatch between LID-primary (%s) and PN-primary (%s)", norm1.Sender.OpaqueID, norm2.Sender.OpaqueID)
	}
	if norm1.Sender.OpaqueID != norm3.Sender.OpaqueID {
		t.Fatalf("OpaqueID mismatch between LID-primary (%s) and resolved LID (%s)", norm1.Sender.OpaqueID, norm3.Sender.OpaqueID)
	}

	// Verify PhoneNumber is correctly populated
	if norm1.Sender.PhoneNumber != "15551234567" || norm2.Sender.PhoneNumber != "15551234567" || norm3.Sender.PhoneNumber != "15551234567" {
		t.Fatalf("unexpected phone numbers: norm1=%q norm2=%q norm3=%q", norm1.Sender.PhoneNumber, norm2.Sender.PhoneNumber, norm3.Sender.PhoneNumber)
	}

	// 4. Distinct user produces distinct OpaqueID
	pnJIDOther := types.NewJID("15559876543", types.DefaultUserServer)
	lidJIDOther := types.NewJID("100099999999999", types.HiddenUserServer)
	evtOther := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:      chatJID,
				Sender:    lidJIDOther,
				SenderAlt: pnJIDOther,
				IsGroup:   true,
			},
			ID:        "msg-other",
			PushName:  "Bob",
			Timestamp: now,
		},
		Message: &waE2E.Message{Conversation: &body},
	}
	normOther, okOther := normalizer.NormalizeMessage(evtOther, true, 1024*1024, nil, nil, nil, resolver)
	if !okOther {
		t.Fatal("evtOther failed normalization")
	}
	if normOther.Sender.OpaqueID == norm1.Sender.OpaqueID {
		t.Fatal("distinct users must have distinct OpaqueIDs")
	}

	// 5. LID-only sender without PN mapping produces empty PhoneNumber
	lidOnly := types.NewJID("100055555555555", types.HiddenUserServer)
	evtLIDOnly := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:    chatJID,
				Sender:  lidOnly,
				IsGroup: true,
			},
			ID:        "msg-lid-only",
			PushName:  "Anonymous",
			Timestamp: now,
		},
		Message: &waE2E.Message{Conversation: &body},
	}
	normLIDOnly, okLIDOnly := normalizer.NormalizeMessage(evtLIDOnly, true, 1024*1024, nil, nil, nil, resolver)
	if !okLIDOnly {
		t.Fatal("evtLIDOnly failed normalization")
	}
	if normLIDOnly.Sender.PhoneNumber != "" {
		t.Fatalf("LID-only user component must never be treated as phone number, got: %q", normLIDOnly.Sender.PhoneNumber)
	}
}

func TestNormalizePollVote_OpaqueIDConsistency(t *testing.T) {
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	normalizer, err := NewNormalizer(map[string]string{"c1g1": "123456789@g.us"}, hasher, config.UsernameModePushName)
	if err != nil {
		t.Fatal(err)
	}

	chatJID := types.NewJID("123456789", types.GroupServer)
	pnJID := types.NewJID("15551234567", types.DefaultUserServer)
	lidJID := types.NewJID("100012345678901", types.HiddenUserServer)
	now := time.Now().UTC().Truncate(time.Second)

	mockDecryptor := func(ctx context.Context, msg *events.Message) (*waE2E.PollVoteMessage, error) {
		return &waE2E.PollVoteMessage{
			SelectedOptions: [][]byte{[]byte("\x01\x02\x03\x04")},
		}, nil
	}

	resolver := func(ctx context.Context, jid types.JID) (types.JID, error) {
		if jid.ToNonAD().String() == pnJID.ToNonAD().String() {
			return lidJID, nil
		}
		return types.EmptyJID, nil
	}

	votePN := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:      chatJID,
				Sender:    pnJID,
				SenderAlt: lidJID,
				IsGroup:   true,
			},
			ID:        "vote-pn",
			PushName:  "Alice",
			Timestamp: now,
		},
		Message: &waE2E.Message{
			PollUpdateMessage: &waE2E.PollUpdateMessage{
				PollCreationMessageKey: &waCommon.MessageKey{ID: protoPtr("target-poll")},
			},
		},
	}

	voteLID := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:      chatJID,
				Sender:    lidJID,
				SenderAlt: pnJID,
				IsGroup:   true,
			},
			ID:        "vote-lid",
			PushName:  "Alice",
			Timestamp: now,
		},
		Message: &waE2E.Message{
			PollUpdateMessage: &waE2E.PollUpdateMessage{
				PollCreationMessageKey: &waCommon.MessageKey{ID: protoPtr("target-poll")},
			},
		},
	}

	normPN, ok1 := normalizer.NormalizeMessage(votePN, true, 1024*1024, nil, mockDecryptor, nil, resolver)
	if !ok1 {
		t.Fatal("votePN normalization failed")
	}
	normLID, ok2 := normalizer.NormalizeMessage(voteLID, true, 1024*1024, nil, mockDecryptor, nil, resolver)
	if !ok2 {
		t.Fatal("voteLID normalization failed")
	}

	if normPN.Sender.OpaqueID != normLID.Sender.OpaqueID {
		t.Fatalf("poll vote OpaqueID mismatch: PN=%s, LID=%s", normPN.Sender.OpaqueID, normLID.Sender.OpaqueID)
	}
}

func TestParticipantMatches_SeparatePhoneAndIdentity(t *testing.T) {
	ctx := context.Background()
	pnJID := types.NewJID("15551234567", types.DefaultUserServer)
	lidJID := types.NewJID("100012345678901", types.HiddenUserServer)

	resolver := func(ctx context.Context, jid types.JID) (types.JID, error) {
		if jid.ToNonAD().String() == pnJID.ToNonAD().String() {
			return lidJID, nil
		}
		if jid.ToNonAD().String() == lidJID.ToNonAD().String() {
			return pnJID, nil
		}
		return types.EmptyJID, nil
	}

	p := types.GroupParticipant{
		JID:         lidJID,
		LID:         lidJID,
		PhoneNumber: pnJID,
	}

	// 1. Phone matching via ParticipantMatchesPhone
	if !ParticipantMatchesPhone(ctx, p, "+15551234567", nil) {
		t.Fatal("expected match by +phone")
	}
	if !ParticipantMatchesPhone(ctx, p, "15551234567", nil) {
		t.Fatal("expected match by phone")
	}

	// 2. Identity matching via ParticipantMatchesIdentity (full JIDs)
	if !ParticipantMatchesIdentity(ctx, p, lidJID, nil) {
		t.Fatal("expected match by LID JID")
	}
	if !ParticipantMatchesIdentity(ctx, p, pnJID, nil) {
		t.Fatal("expected match by PN JID")
	}

	// 3. Regression test: submitted phone digits exactly equal to LID numeric user value
	// must NOT match unless a real PN mapping exists.
	lidDigits := "100012345678901"
	pLIDOnly := types.GroupParticipant{
		JID: types.NewJID(lidDigits, types.HiddenUserServer),
		LID: types.NewJID(lidDigits, types.HiddenUserServer),
	}
	if ParticipantMatchesPhone(ctx, pLIDOnly, lidDigits, nil) {
		t.Fatal("submitted phone digits matching LID user must not match without real PN mapping")
	}
	if ParticipantMatchesPhone(ctx, pLIDOnly, "+"+lidDigits, nil) {
		t.Fatal("submitted +phone digits matching LID user must not match without real PN mapping")
	}

	// But if resolver maps submitted phone to participant's LID, it matches
	pnForLIDDigits := types.NewJID(lidDigits, types.DefaultUserServer)
	validResolver := func(ctx context.Context, jid types.JID) (types.JID, error) {
		if jid.ToNonAD().String() == pnForLIDDigits.ToNonAD().String() {
			return pLIDOnly.LID, nil
		}
		return types.EmptyJID, nil
	}
	if !ParticipantMatchesPhone(ctx, pLIDOnly, lidDigits, validResolver) {
		t.Fatal("submitted phone mapped to LID via resolver should match")
	}

	// 4. Participant has ONLY LID, but resolver maps it to PN
	if !ParticipantMatchesPhone(ctx, pLIDOnly, "15551234567", resolver) {
		t.Fatal("expected resolver to map LID participant to target phone")
	}
	if !ParticipantMatchesPhone(ctx, pLIDOnly, "+15551234567", resolver) {
		t.Fatal("expected resolver to map LID participant to target +phone")
	}

	// 5. Participant has ONLY PN, but resolver maps target LID to PN
	pPNOnly := types.GroupParticipant{
		JID:         pnJID,
		PhoneNumber: pnJID,
	}
	if !ParticipantMatchesIdentity(ctx, pPNOnly, lidJID, resolver) {
		t.Fatal("expected resolver to map target LID to PN participant")
	}
}

func TestExtractMentions_FullJIDPreservation(t *testing.T) {
	pnJID := types.NewJID("15551234567", types.DefaultUserServer)
	lidJID := types.NewJID("100012345678901", types.HiddenUserServer)

	resolver := func(ctx context.Context, jid types.JID) (types.JID, error) {
		if jid.ToNonAD().String() == pnJID.ToNonAD().String() {
			return lidJID, nil
		}
		return types.EmptyJID, nil
	}

	mockContactGetter := func(jid types.JID) types.ContactInfo {
		if jid.ToNonAD().String() == lidJID.ToNonAD().String() {
			return types.ContactInfo{
				Found:    true,
				PushName: "Alice",
			}
		}
		return types.ContactInfo{Found: false}
	}

	mentions := extractMentions([]string{pnJID.String()}, mockContactGetter, resolver)
	if len(mentions) != 1 {
		t.Fatalf("expected 1 mention, got %d", len(mentions))
	}
	// Verify full JID is preserved rather than just user part
	if mentions[0].RemoteID != pnJID.ToNonAD().String() {
		t.Fatalf("expected mention RemoteID to preserve normalized full JID %s, got %s", pnJID.ToNonAD().String(), mentions[0].RemoteID)
	}
	if mentions[0].Name != "Alice" {
		t.Fatalf("expected mention Name to be Alice via resolved LID contact info, got %s", mentions[0].Name)
	}
}

func TestNormalizeMessage_MentionTextPreservation(t *testing.T) {
	hasher, err := identity.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	normalizer, err := NewNormalizer(map[string]string{"c1g1": "123456789@g.us"}, hasher, config.UsernameModePushName)
	if err != nil {
		t.Fatal(err)
	}

	chatJID := types.NewJID("123456789", types.GroupServer)
	senderJID := types.NewJID("15550000000", types.DefaultUserServer)
	mentionedPN := types.NewJID("15551234567", types.DefaultUserServer)
	body := "hello @15551234567 welcome"

	mockContactGetter := func(jid types.JID) types.ContactInfo {
		return types.ContactInfo{Found: true, PushName: "Bob"}
	}

	evt := &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{
				Chat:    chatJID,
				Sender:  senderJID,
				IsGroup: true,
			},
			ID:        "msg-mention",
			PushName:  "Alice",
			Timestamp: time.Now(),
		},
		Message: &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text: &body,
				ContextInfo: &waE2E.ContextInfo{
					MentionedJID: []string{mentionedPN.String()},
				},
			},
		},
	}

	norm, ok := normalizer.NormalizeMessage(evt, true, 1024*1024, nil, nil, mockContactGetter, nil)
	if !ok {
		t.Fatal("normalization failed")
	}
	if len(norm.Mentions) != 1 {
		t.Fatalf("expected 1 mention, got %d", len(norm.Mentions))
	}
	if norm.Mentions[0].RemoteID != mentionedPN.ToNonAD().String() {
		t.Fatalf("expected mention RemoteID to be %s, got %s", mentionedPN.ToNonAD().String(), norm.Mentions[0].RemoteID)
	}
	expectedText := "hello @" + mentionedPN.ToNonAD().String() + " welcome"
	if norm.Text != expectedText {
		t.Fatalf("expected text %q, got %q", expectedText, norm.Text)
	}
}
