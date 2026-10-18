package whatsapp

import (
	"context"

	"github.com/vm75/message-sync/internal/transport"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

// nativeCrossReferences replaces references only for verified destination
// members. The wire text and mention metadata must address the same LID.
func (a *Adapter) nativeCrossReferences(ctx context.Context, target types.JID, outgoing transport.Outgoing, info *waE2E.ContextInfo, getGroupInfo func(context.Context, types.JID) (*types.GroupInfo, error)) (string, *waE2E.ContextInfo) {
	if a == nil || getGroupInfo == nil || len(outgoing.References) == 0 {
		return outgoing.Text, info
	}
	switch outgoing.Kind {
	case "text", "other", "image", "video", "document":
	default:
		return outgoing.Text, info
	}
	a.mu.Lock()
	normalizer := a.normalizer
	a.mu.Unlock()
	if normalizer == nil || normalizer.hasher == nil {
		return outgoing.Text, info
	}
	group, err := getGroupInfo(ctx, target)
	if err != nil || group == nil {
		return outgoing.Text, info
	}
	destinationMembers := make(map[string]types.JID)
	for _, participant := range group.Participants {
		canonical, _ := CanonicalSenderIdentity(ctx, participant.JID, participant.LID, a.resolveAltJID)
		if canonical.Server != types.HiddenUserServer || canonical.User == "" {
			continue
		}
		// An actor may have first been observed before its LID was available.
		// Match trusted roster identities, but emit only the verified LID.
		for _, jid := range []types.JID{canonical, participant.JID, participant.LID, participant.PhoneNumber} {
			jid = normalizeWhatsAppIdentityJID(jid)
			if !jid.IsEmpty() {
				destinationMembers[normalizer.hasher.UserID(jid.String())] = canonical
			}
		}
	}
	seen := make(map[string]bool)
	for _, jid := range info.GetMentionedJID() {
		seen[jid] = true
	}
	text := transport.RewriteCrossReferences(outgoing.Text, outgoing.References, func(ref transport.CrossReference) (string, bool) {
		identity, exists := destinationMembers[ref.ActorID]
		if !exists {
			return "", false
		}
		if info == nil {
			info = &waE2E.ContextInfo{}
		}
		if !seen[identity.String()] {
			info.MentionedJID = append(info.MentionedJID, identity.String())
			seen[identity.String()] = true
		}
		return "@" + identity.User, true
	})
	return text, info
}
