package whatsapp

import (
	"context"
	"strings"

	"github.com/vm75/message-sync/internal/transport"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

// nativeCrossReferences ALWAYS renders readable cross-group references, even
// if the native mention is unavailable. Only an explicitly addressed group
// may receive native metadata, and only for a live participant LID.
func (a *Adapter) nativeCrossReferences(ctx context.Context, target types.JID, outgoing transport.Outgoing, info *waE2E.ContextInfo) (string, *waE2E.ContextInfo) {
	fallback := func() (string, *waE2E.ContextInfo) {
		return transport.FormatCrossReferences(outgoing.Text, outgoing.References, outgoing.ReferenceAnonymized, nil), info
	}
	if a == nil || a.client == nil || len(outgoing.References) == 0 || outgoing.Kind != "text" {
		return fallback()
	}
	a.mu.Lock()
	normalizer := a.normalizer
	a.mu.Unlock()
	if normalizer == nil || normalizer.hasher == nil {
		return fallback()
	}

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
			canonical, _ := CanonicalSenderIdentity(ctx, participant.JID, participant.LID, a.resolveAltJID)
			if !canonical.IsEmpty() {
				known[normalizer.hasher.UserID(canonical.String())] = canonical
			}
		}
		return known
	}
	// The destination roster is used only to verify a real native mention.
	destinationMembers := resolveGroup(outgoing.Endpoint, target)
	seen := make(map[string]bool)
	text := transport.FormatCrossReferences(outgoing.Text, outgoing.References, outgoing.ReferenceAnonymized, func(ref transport.CrossReference) (string, bool) {
		if ref.Endpoint == outgoing.Endpoint {
			identity, exists := destinationMembers[ref.ActorID]
			if exists && identity.Server == types.HiddenUserServer {
				if info == nil {
					info = &waE2E.ContextInfo{}
				}
				if !seen[identity.String()] {
					info.MentionedJID = append(info.MentionedJID, identity.String())
					seen[identity.String()] = true
				}
				return "@" + transport.ReferenceDisplayLabel(ref, outgoing.ReferenceAnonymized), true
			}
			return "", false
		}
		// A WhatsApp contact name may be used only for non-native display
		// in another group on the same connection. It never selects a user.
		if outgoing.ReferenceAnonymized || strings.TrimSpace(ref.Name) != "" {
			return "", false
		}
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
		if name == "" || len([]rune(name)) > 96 || strings.ContainsAny(name, "@{}<>\r\n") {
			return "", false
		}
		return "@" + string(ref.Endpoint) + "/" + name, true
	})
	return text, info
}
