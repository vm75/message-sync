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
}
