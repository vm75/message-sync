package whatsapp

import (
	"context"
	"strings"

	"go.mau.fi/whatsmeow/types"
)

// JIDResolver resolves an alternate JID (LID <-> PN) for a given JID.
type JIDResolver func(ctx context.Context, jid types.JID) (types.JID, error)

// CanonicalSenderIdentity returns the canonical identity JID and phone number
// for an inbound WhatsApp message sender according to priority:
// 1. If the primary sender is a LID, use normalized LID.
// 2. Else if the alternate sender is a LID, use normalized alternate LID.
// 3. Else if the sender is PN and whatsmeow can resolve a LID for it, use that normalized LID.
// 4. Otherwise fall back to the normalized PN JID.
//
// Phone number is populated only when an actual PN identity is available (from primary,
// alternate, or resolved PN). An LID user component is never used as a phone number.
func CanonicalSenderIdentity(ctx context.Context, sender, senderAlt types.JID, resolver JIDResolver) (canonical types.JID, phone string) {
	s := sender.ToNonAD()
	sAlt := senderAlt.ToNonAD()

	// 1. If primary sender is LID, use normalized LID.
	if s.Server == types.HiddenUserServer || s.Server == types.HostedLIDServer {
		canonical = s
	} else if sAlt.Server == types.HiddenUserServer || sAlt.Server == types.HostedLIDServer {
		// 2. Else if alternate sender is LID, use normalized alternate LID.
		canonical = sAlt
	} else if s.Server == types.DefaultUserServer && resolver != nil {
		// 3. Else if sender is PN and whatsmeow can resolve a LID for it, use that normalized LID.
		if alt, err := resolver(ctx, s); err == nil && !alt.IsEmpty() {
			altNonAD := alt.ToNonAD()
			if altNonAD.Server == types.HiddenUserServer || altNonAD.Server == types.HostedLIDServer {
				canonical = altNonAD
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
		if target.Server != types.HiddenUserServer && target.Server != types.HostedLIDServer {
			target = sAlt
		}
		if target.Server == types.HiddenUserServer || target.Server == types.HostedLIDServer {
			if alt, err := resolver(ctx, target); err == nil && !alt.IsEmpty() {
				altNonAD := alt.ToNonAD()
				if altNonAD.Server == types.DefaultUserServer {
					phone = altNonAD.User
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

	// 1. Direct match against genuine PN fields on the participant
	if p.PhoneNumber.Server == types.DefaultUserServer && p.PhoneNumber.ToNonAD().User == cleanPhone {
		return true
	}
	if p.JID.Server == types.DefaultUserServer && p.JID.ToNonAD().User == cleanPhone {
		return true
	}

	// Never compare cleanPhone against p.LID.User or p.JID.User when they are LID servers.
	if resolver == nil {
		return false
	}

	// 2. Resolve submitted phone number (as PN JID) to LID, and compare against participant's LID
	submittedPN := types.NewJID(cleanPhone, types.DefaultUserServer)
	if altLID, err := resolver(ctx, submittedPN); err == nil && !altLID.IsEmpty() {
		altNonAD := altLID.ToNonAD()
		if altNonAD.Server == types.HiddenUserServer || altNonAD.Server == types.HostedLIDServer {
			if !p.LID.IsEmpty() && p.LID.ToNonAD().String() == altNonAD.String() {
				return true
			}
			if !p.JID.IsEmpty() && (p.JID.Server == types.HiddenUserServer || p.JID.Server == types.HostedLIDServer) && p.JID.ToNonAD().String() == altNonAD.String() {
				return true
			}
		}
	}

	// 3. Resolve participant's LID to PN, and compare resolved PN against cleanPhone
	lidFields := []types.JID{}
	if !p.LID.IsEmpty() && (p.LID.Server == types.HiddenUserServer || p.LID.Server == types.HostedLIDServer) {
		lidFields = append(lidFields, p.LID.ToNonAD())
	}
	if !p.JID.IsEmpty() && (p.JID.Server == types.HiddenUserServer || p.JID.Server == types.HostedLIDServer) {
		lidFields = append(lidFields, p.JID.ToNonAD())
	}
	for _, lid := range lidFields {
		if altPN, err := resolver(ctx, lid); err == nil && !altPN.IsEmpty() {
			altNonAD := altPN.ToNonAD()
			if altNonAD.Server == types.DefaultUserServer && altNonAD.User == cleanPhone {
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
	targetNonAD := target.ToNonAD()
	fields := []types.JID{p.JID, p.LID, p.PhoneNumber}

	// 1. Direct match on normalized JID
	for _, f := range fields {
		if !f.IsEmpty() && f.ToNonAD().String() == targetNonAD.String() {
			return true
		}
	}

	if resolver == nil {
		return false
	}

	// 2. Resolve target to alternate JID and compare with participant fields
	if alt, err := resolver(ctx, targetNonAD); err == nil && !alt.IsEmpty() {
		altNonAD := alt.ToNonAD()
		for _, f := range fields {
			if !f.IsEmpty() && f.ToNonAD().String() == altNonAD.String() {
				return true
			}
		}
	}

	// 3. Resolve participant fields and compare with target
	for _, f := range fields {
		if !f.IsEmpty() {
			if alt, err := resolver(ctx, f.ToNonAD()); err == nil && !alt.IsEmpty() {
				if alt.ToNonAD().String() == targetNonAD.String() {
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
