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

// ParticipantMatches checks whether a WhatsApp group participant matches a target
// string (which may be a phone number without '+', a user component, or a JID string).
func ParticipantMatches(ctx context.Context, p types.GroupParticipant, target string, resolver JIDResolver) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	cleanTarget := strings.TrimPrefix(target, "+")

	// 1. Direct JID string match (full string match against any participant field)
	fields := []types.JID{p.JID, p.LID, p.PhoneNumber}
	for _, f := range fields {
		if !f.IsEmpty() && (f.String() == target || f.ToNonAD().String() == target) {
			return true
		}
	}

	// 2. Direct Phone match against genuine PN fields (p.PhoneNumber or p.JID if PN)
	pnFields := []types.JID{p.PhoneNumber}
	if p.JID.Server == types.DefaultUserServer {
		pnFields = append(pnFields, p.JID)
	}
	for _, f := range pnFields {
		if !f.IsEmpty() {
			nonAD := f.ToNonAD()
			if nonAD.User == cleanTarget || nonAD.User == target {
				return true
			}
		}
	}

	// 3. Direct LID user match (only if target is not an explicit phone number starting with '+')
	if !strings.HasPrefix(target, "+") {
		lidFields := []types.JID{p.LID}
		if p.JID.Server == types.HiddenUserServer || p.JID.Server == types.HostedLIDServer {
			lidFields = append(lidFields, p.JID)
		}
		for _, f := range lidFields {
			if !f.IsEmpty() {
				nonAD := f.ToNonAD()
				if target == nonAD.User {
					return true
				}
			}
		}
	}

	if resolver == nil {
		return false
	}

	// 4. Resolve target to alternate JID
	var targetJID types.JID
	if strings.Contains(target, "@") {
		targetJID, _ = types.ParseJID(target)
	} else if strings.HasPrefix(target, "+") || p.PhoneNumber.User == cleanTarget {
		targetJID = types.NewJID(cleanTarget, types.DefaultUserServer)
	} else {
		targetJID = types.NewJID(cleanTarget, types.DefaultUserServer)
	}

	if !targetJID.IsEmpty() {
		if alt, err := resolver(ctx, targetJID); err == nil && !alt.IsEmpty() {
			altNonAD := alt.ToNonAD()
			for _, f := range fields {
				if !f.IsEmpty() {
					nonAD := f.ToNonAD()
					if nonAD.String() == altNonAD.String() || (nonAD.Server == altNonAD.Server && nonAD.User == altNonAD.User) {
						return true
					}
				}
			}
		}
		if !strings.Contains(target, "@") {
			lidTarget := types.NewJID(cleanTarget, types.HiddenUserServer)
			if alt, err := resolver(ctx, lidTarget); err == nil && !alt.IsEmpty() {
				altNonAD := alt.ToNonAD()
				for _, f := range fields {
					if !f.IsEmpty() {
						nonAD := f.ToNonAD()
						if nonAD.String() == altNonAD.String() || (nonAD.Server == altNonAD.Server && nonAD.User == altNonAD.User) {
							return true
						}
					}
				}
			}
		}
	}

	// 5. Resolve participant JIDs to compare with target
	for _, f := range fields {
		if f.IsEmpty() {
			continue
		}
		if alt, err := resolver(ctx, f); err == nil && !alt.IsEmpty() {
			altNonAD := alt.ToNonAD()
			if altNonAD.String() == target {
				return true
			}
			if altNonAD.Server == types.DefaultUserServer && (altNonAD.User == cleanTarget || altNonAD.User == target) {
				return true
			}
			if (altNonAD.Server == types.HiddenUserServer || altNonAD.Server == types.HostedLIDServer) && !strings.HasPrefix(target, "+") && altNonAD.User == target {
				return true
			}
		}
	}

	return false
}
