package whatsapp

import (
	"context"
	"strings"

	"go.mau.fi/whatsmeow/types"
)

// JIDResolver resolves an alternate JID (LID <-> PN) for a given JID.
type JIDResolver func(ctx context.Context, jid types.JID) (types.JID, error)

// normalizeWhatsAppIdentityJID removes device addressing and folds hosted LIDs
// into the canonical @lid namespace used by whatsmeow's LID mapping store.
// @hosted.lid and @lid are addressing forms of the same opaque WhatsApp identity
// and must not become distinct application actors.
func normalizeWhatsAppIdentityJID(jid types.JID) types.JID {
	normalized := jid.ToNonAD()
	if normalized.Server == types.HostedLIDServer {
		normalized.Server = types.HiddenUserServer
	}
	return normalized
}

func isLIDJID(jid types.JID) bool {
	return normalizeWhatsAppIdentityJID(jid).Server == types.HiddenUserServer
}

// CanonicalSenderIdentity returns the canonical identity JID and phone number
// for an inbound WhatsApp message sender according to priority:
// 1. If the primary sender is a LID, use normalized canonical @lid.
// 2. Else if the alternate sender is a LID, use normalized canonical @lid.
// 3. Else if the sender is PN and whatsmeow can resolve a LID for it, use that normalized LID.
// 4. Otherwise fall back to the normalized PN JID.
//
// Phone number is populated only when an actual PN identity is available (from primary,
// alternate, or resolved PN). An LID user component is never used as a phone number.
func CanonicalSenderIdentity(ctx context.Context, sender, senderAlt types.JID, resolver JIDResolver) (canonical types.JID, phone string) {
	s := normalizeWhatsAppIdentityJID(sender)
	sAlt := normalizeWhatsAppIdentityJID(senderAlt)

	// 1. If primary sender is LID, use normalized LID.
	if s.Server == types.HiddenUserServer {
		canonical = s
	} else if sAlt.Server == types.HiddenUserServer {
		// 2. Else if alternate sender is LID, use normalized alternate LID.
		canonical = sAlt
	} else if s.Server == types.DefaultUserServer && resolver != nil {
		// 3. Else if sender is PN and whatsmeow can resolve a LID for it, use that normalized LID.
		if alt, err := resolver(ctx, s); err == nil && !alt.IsEmpty() {
			altNormalized := normalizeWhatsAppIdentityJID(alt)
			if altNormalized.Server == types.HiddenUserServer {
				canonical = altNormalized
			}
		}
	}

	// 4. Otherwise fall back to the normalized PN JID.
	if canonical.IsEmpty() {
		canonical = s
	}

	// Determine phone number only from an actual PN identity.
	if s.Server == types.DefaultUserServer {
		phone = s.User
	} else if sAlt.Server == types.DefaultUserServer {
		phone = sAlt.User
	} else if resolver != nil {
		target := s
		if target.Server != types.HiddenUserServer {
			target = sAlt
		}
		if target.Server == types.HiddenUserServer {
			if alt, err := resolver(ctx, target); err == nil && !alt.IsEmpty() {
				altNormalized := normalizeWhatsAppIdentityJID(alt)
				if altNormalized.Server == types.DefaultUserServer {
					phone = altNormalized.User
				}
			}
		}
	}

	return canonical, phone
}

// ParticipantMatchesPhone checks whether a WhatsApp group participant matches a
// submitted phone number. A submitted phone number must only match a real PN identity:
//   - PhoneNumber with @s.whatsapp.net
//   - Primary JID with @s.whatsapp.net
//   - A trusted PN resolved through whatsmeow's PN/LID mapping (or resolved LID matching participant's LID)
//
// An LID user component (the numeric .User part of @lid) is NEVER treated as a phone number.
func ParticipantMatchesPhone(ctx context.Context, p types.GroupParticipant, phone string, resolver JIDResolver) bool {
	cleanPhone := strings.TrimPrefix(strings.TrimSpace(phone), "+")
	if cleanPhone == "" {
		return false
	}

	participantPhone := normalizeWhatsAppIdentityJID(p.PhoneNumber)
	participantPrimary := normalizeWhatsAppIdentityJID(p.JID)
	participantLID := normalizeWhatsAppIdentityJID(p.LID)

	// 1. Direct match against genuine PN fields on the participant.
	if participantPhone.Server == types.DefaultUserServer && participantPhone.User == cleanPhone {
		return true
	}
	if participantPrimary.Server == types.DefaultUserServer && participantPrimary.User == cleanPhone {
		return true
	}

	// Never compare cleanPhone against a LID user component.
	if resolver == nil {
		return false
	}

	// 2. Resolve submitted phone number (as PN JID) to canonical LID, and compare
	// against the participant's LID identities.
	submittedPN := types.NewJID(cleanPhone, types.DefaultUserServer)
	if altLID, err := resolver(ctx, submittedPN); err == nil && !altLID.IsEmpty() {
		altNormalized := normalizeWhatsAppIdentityJID(altLID)
		if altNormalized.Server == types.HiddenUserServer {
			if !participantLID.IsEmpty() && participantLID.String() == altNormalized.String() {
				return true
			}
			if !participantPrimary.IsEmpty() && participantPrimary.Server == types.HiddenUserServer && participantPrimary.String() == altNormalized.String() {
				return true
			}
		}
	}

	// 3. Resolve participant LIDs to PN, and compare the resolved PN against cleanPhone.
	lidFields := []types.JID{}
	if !participantLID.IsEmpty() && participantLID.Server == types.HiddenUserServer {
		lidFields = append(lidFields, participantLID)
	}
	if !participantPrimary.IsEmpty() && participantPrimary.Server == types.HiddenUserServer {
		lidFields = append(lidFields, participantPrimary)
	}
	for _, lid := range lidFields {
		if altPN, err := resolver(ctx, lid); err == nil && !altPN.IsEmpty() {
			altNormalized := normalizeWhatsAppIdentityJID(altPN)
			if altNormalized.Server == types.DefaultUserServer && altNormalized.User == cleanPhone {
				return true
			}
		}
	}

	return false
}

// ParticipantMatchesIdentity checks whether a WhatsApp group participant matches a
// provider identity JID (PN or LID), using explicit PN/LID-aware resolution.
func ParticipantMatchesIdentity(ctx context.Context, p types.GroupParticipant, target types.JID, resolver JIDResolver) bool {
	if target.IsEmpty() {
		return false
	}
	targetNormalized := normalizeWhatsAppIdentityJID(target)
	fields := []types.JID{
		normalizeWhatsAppIdentityJID(p.JID),
		normalizeWhatsAppIdentityJID(p.LID),
		normalizeWhatsAppIdentityJID(p.PhoneNumber),
	}

	// 1. Direct match on canonical normalized JID.
	for _, f := range fields {
		if !f.IsEmpty() && f.String() == targetNormalized.String() {
			return true
		}
	}

	if resolver == nil {
		return false
	}

	// 2. Resolve target to alternate JID and compare with participant fields.
	if alt, err := resolver(ctx, targetNormalized); err == nil && !alt.IsEmpty() {
		altNormalized := normalizeWhatsAppIdentityJID(alt)
		for _, f := range fields {
			if !f.IsEmpty() && f.String() == altNormalized.String() {
				return true
			}
		}
	}

	// 3. Resolve participant fields and compare with target.
	for _, f := range fields {
		if !f.IsEmpty() {
			if alt, err := resolver(ctx, f); err == nil && !alt.IsEmpty() {
				if normalizeWhatsAppIdentityJID(alt).String() == targetNormalized.String() {
					return true
				}
			}
		}
	}

	return false
}

// ParticipantMatches is a convenience helper that matches a participant against a
// target string, delegating to ParticipantMatchesIdentity when target is a full JID
// or ParticipantMatchesPhone when target is a phone number.
func ParticipantMatches(ctx context.Context, p types.GroupParticipant, target string, resolver JIDResolver) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	if strings.Contains(target, "@") {
		if jid, err := types.ParseJID(target); err == nil && !jid.IsEmpty() {
			return ParticipantMatchesIdentity(ctx, p, jid, resolver)
		}
		return false
	}
	return ParticipantMatchesPhone(ctx, p, target, resolver)
}
