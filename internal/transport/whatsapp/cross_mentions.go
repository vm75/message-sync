package whatsapp

import (
	"context"
	"strings"

	"github.com/vm75/message-sync/internal/transport"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

// nativeCrossReferences resolves actor IDs against current group membership.
// LID-only native notification protects phone numbers. Other WhatsApp groups
// on the same connection may display a transient contact name, but it is never
// stored or used as routing identity. Unresolvable handles stay literal.
func (a *Adapter) nativeCrossReferences(ctx context.Context, target types.JID, outgoing transport.Outgoing, info *waE2E.ContextInfo) (string, *waE2E.ContextInfo) {
	if len(outgoing.References) == 0 || a == nil || a.client == nil {
		return outgoing.Text, info
	}
	a.mu.Lock()
	normalizer := a.normalizer
	a.mu.Unlock()
	if normalizer == nil || normalizer.hasher == nil {
		return outgoing.Text, info
	}

	// Per-delivery lookup, never persistent native identity mapping.
	byEndpoint := make(map[transport.EndpointID]map[string]types.JID)
	resolveGroup := func(endpoint transport.EndpointID, jid types.JID) map[string]types.JID {
		if known, ok := byEndpoint[endpoint]; ok {
			return known
		}
		known := make(map[string]types.JID)
		byEndpoint[endpoint] = known
		group, err := a.client.GetGroupInfo(ctx, jid)
		if err != nil || group == nil {
			return known
		}
		for _, participant := range group.Participants {
			identity, _ := CanonicalSenderIdentity(ctx, participant.JID, participant.LID, a.resolveAltJID)
			if !identity.IsEmpty() {
				known[normalizer.hasher.UserID(identity.String())] = identity
			}
		}
		return known
	}
	destinationMembers := resolveGroup(outgoing.Endpoint, target)
	seen := make(map[string]bool)
	text := transport.RewriteCrossReferences(outgoing.Text, outgoing.References, func(ref transport.CrossReference) (string, bool) {
		handle := strings.TrimPrefix(strings.TrimSuffix(ref.Token, "}"), "@{"+string(ref.Endpoint)+"/")
		identity, exists := destinationMembers[ref.ActorID]
		if exists && identity.Server == types.HiddenUserServer {
			if info == nil {
				info = &waE2E.ContextInfo{}
			}
			if !seen[identity.String()] {
				info.MentionedJID = append(info.MentionedJID, identity.String())
				seen[identity.String()] = true
			}
			return "@" + handle, true
		}

		// Non-native fallback may show a current contact label in a different,
		// non-anonymized WhatsApp group, but cannot infer cross-connection users.
		a.mu.Lock()
		sourceGroup, owned := a.targets[ref.Endpoint]
		a.mu.Unlock()
		if !owned {
			return "", false
		}
		sourceIdentity := resolveGroup(ref.Endpoint, sourceGroup)[ref.ActorID]
		if sourceIdentity.IsEmpty() {
			return "", false
		}
		contact := a.getContactInfo(sourceIdentity)
		name := strings.Join(strings.Fields(contact.PushName), " ")
		if name == "" {
			name = strings.Join(strings.Fields(contact.FullName), " ")
		}
		if name == "" {
			name = strings.Join(strings.Fields(contact.BusinessName), " ")
		}
		if name == "" || strings.Trim(name, "+0123456789 -()") == "" {
			return "", false
		}
		runes := []rune(name)
		if len(runes) > 64 {
			name = string(runes[:64])
		}
		// Keep the label clearly textual, and prevent names from introducing
		// another structured bridge reference.
		name = strings.NewReplacer("{", "(", "}", ")", "@", "").Replace(name)
		return "@{" + string(ref.Endpoint) + "/" + name + "}", true
	})
	return text, info
}
