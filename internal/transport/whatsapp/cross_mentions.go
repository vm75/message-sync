package whatsapp

import (
	"context"
	"time"

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

// ResolveParticipant rebuilds a name lookup using provider-owned contact
// metadata and the referenced group's current roster. It never reads the
// protocol database directly or persists the returned contact names.
func (a *Adapter) ResolveParticipant(ctx context.Context, endpoint transport.EndpointID, label string, actors []string) (string, bool) {
	if a == nil {
		return "", false
	}
	a.mu.Lock()
	client := a.client
	target, owned := a.targets[endpoint]
	a.mu.Unlock()
	if client == nil || !owned {
		return "", false
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return a.resolveParticipantName(lookupCtx, target, label, actors, client.GetGroupInfo, func(jid types.JID) types.ContactInfo {
		if client.Store == nil || client.Store.Contacts == nil {
			return types.ContactInfo{}
		}
		if contact, err := client.Store.Contacts.GetContact(lookupCtx, jid); err == nil && contact.Found {
			return contact
		}
		if alt, err := client.Store.GetAltJID(lookupCtx, jid); err == nil && !alt.IsEmpty() {
			if contact, err := client.Store.Contacts.GetContact(lookupCtx, alt); err == nil {
				return contact
			}
		}
		return types.ContactInfo{}
	})
}

func (a *Adapter) resolveParticipantName(ctx context.Context, groupID types.JID, label string, actors []string, getGroupInfo func(context.Context, types.JID) (*types.GroupInfo, error), getContact ContactGetter) (string, bool) {
	a.mu.Lock()
	normalizer := a.normalizer
	a.mu.Unlock()
	if normalizer == nil || normalizer.hasher == nil || getGroupInfo == nil || getContact == nil || len(actors) == 0 {
		return "", false
	}
	group, err := getGroupInfo(ctx, groupID)
	if err != nil || group == nil {
		return "", false
	}
	known := make(map[string]bool, len(actors))
	for _, actor := range actors {
		known[actor] = true
	}
	label = transport.NormalizeParticipantLabel(label)
	if label == "" {
		return "", false
	}
	matched := ""
	for _, participant := range group.Participants {
		canonical, _ := CanonicalSenderIdentity(ctx, participant.JID, participant.LID, a.resolveAltJID)
		var actor string
		for _, jid := range []types.JID{canonical, participant.JID, participant.LID, participant.PhoneNumber} {
			if jid = normalizeWhatsAppIdentityJID(jid); jid.IsEmpty() {
				continue
			}
			if candidate := normalizer.hasher.UserID(jid.String()); known[candidate] {
				actor = candidate
				break
			}
		}
		if actor == "" {
			continue
		}
		matches := false
		for _, jid := range []types.JID{canonical, participant.JID, participant.PhoneNumber} {
			if jid.IsEmpty() {
				continue
			}
			contact := getContact(jid)
			for _, name := range []string{contact.PushName, contact.FullName, contact.BusinessName} {
				if transport.NormalizeParticipantLabel(name) == label {
					matches = true
				}
			}
		}
		if !matches {
			continue
		}
		if matched != "" && matched != actor {
			return "", false
		}
		matched = actor
	}
	return matched, matched != ""
}
