package whatsapp

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestGroupParticipantMatchesPhone(t *testing.T) {
	phone := "15551234567"
	if !groupParticipantMatchesPhone(types.GroupParticipant{JID: types.NewJID(phone, types.DefaultUserServer)}, "+"+phone) {
		t.Fatal("phone JID should match submitted phone")
	}
	if !groupParticipantMatchesPhone(types.GroupParticipant{
		JID:         types.NewJID("1234567890", types.HiddenUserServer),
		PhoneNumber: types.NewJID(phone, types.DefaultUserServer),
	}, phone) {
		t.Fatal("LID-primary participant should match its phone-number JID")
	}
	if groupParticipantMatchesPhone(types.GroupParticipant{JID: types.NewJID("1234567890", types.HiddenUserServer)}, phone) {
		t.Fatal("LID-only participant must not be treated as a phone match")
	}

	// Regression test for issue #101: submitted phone digits exactly equal to LID numeric user value
	// must NOT match unless a real PN mapping exists.
	lidDigits := "1234567890"
	lidParticipant := types.GroupParticipant{
		JID: types.NewJID(lidDigits, types.HiddenUserServer),
		LID: types.NewJID(lidDigits, types.HiddenUserServer),
	}
	if groupParticipantMatchesPhone(lidParticipant, lidDigits) {
		t.Fatal("submitted phone digits matching LID user must not match without real PN mapping")
	}
	if groupParticipantMatchesPhone(lidParticipant, "+"+lidDigits) {
		t.Fatal("submitted +phone digits matching LID user must not match without real PN mapping")
	}
}
