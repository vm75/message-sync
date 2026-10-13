package whatsapp

import (
	"context"
	"strings"

	"github.com/vm75/message-sync/internal/transport"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

// nativeCrossReferences resolves actor IDs against current destination group
// membership. It never treats a displayed name or LID's numeric component as
// a phone number, and never sends a PN-based mention that can expose a number.
// On any uncertainty the explicit textual reference remains intact.
func (a *Adapter) nativeCrossReferences(ctx context.Context, target types.JID, outgoing transport.Outgoing, info *waE2E.ContextInfo) (string, *waE2E.ContextInfo) {
	if len(outgoing.References) == 0 || a == nil || a.client == nil {
		return outgoing.Text, info
	}
	a.mu.Lock()
	var hasher = a.normalizer
	a.mu.Unlock()
	if hasher == nil || hasher.hasher == nil {
		return outgoing.Text, info
	}
	group, err := a.client.GetGroupInfo(ctx, target)
	if err != nil || group == nil {
		return outgoing.Text, info
	}
	resolved := make(map[string]types.JID)
	for _, p := range group.Participants {
		identity, _ := CanonicalSenderIdentity(ctx, p.JID, p.LID, a.resolveAltJID)
		if identity.IsEmpty() || identity.Server != types.HiddenUserServer {
			continue
		}
		resolved[hasher.hasher.UserID(identity.String())] = identity
	}
	seen := make(map[string]bool)
	text := transport.RewriteCrossReferences(outgoing.Text, outgoing.References, func(ref transport.CrossReference) (string, bool) {
		jid, ok := resolved[ref.ActorID]
		if !ok || jid.IsEmpty() {
			return "", false
		}
		if info == nil {
			info = &waE2E.ContextInfo{}
		}
		if !seen[jid.String()] {
			info.MentionedJID = append(info.MentionedJID, jid.String())
			seen[jid.String()] = true
		}
		// Handles, not phone numbers, are visible in message text.
		return "@" + strings.TrimPrefix(strings.TrimSuffix(ref.Token, "}"), "@{"+string(ref.Endpoint)+"/"), true
	})
	return text, info
}
