package router

import (
	"context"
	"regexp"
	"strings"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/transport"
)

// Native @mentions are left to their respective providers. Only the explicit
// bridge namespace is parsed as a cross-group reference. A leading backslash
// escapes the bridge syntax and is never treated as a notification request.
var crossReferencePattern = regexp.MustCompile(`@\{([A-Za-z0-9][A-Za-z0-9_-]{0,63})/(P[1-9][0-9]*)\}`)

const maxCrossReferences = 32

// crossReferences resolves only participants in endpoints that share the
// sender's sync set. Unknown, escaped, or unauthorized tokens remain literal.
func (r *Router) crossReferences(ctx context.Context, source transport.EndpointID, body string) ([]transport.CrossReference, error) {
	if len(body) == 0 {
		return nil, nil
	}
	r.mu.RLock()
	routes := append([]routeTarget(nil), r.routes[source]...)
	r.mu.RUnlock()
	allowed := make(map[string]bool, len(routes))
	for _, route := range routes {
		allowed[string(route.Endpoint)] = true
	}
	var references []transport.CrossReference
	for _, loc := range crossReferencePattern.FindAllStringSubmatchIndex(body, -1) {
		if len(references) >= maxCrossReferences {
			break
		}
		backslashes := 0
		for j := loc[0] - 1; j >= 0 && body[j] == '\\'; j-- {
			backslashes++
		}
		if backslashes%2 == 1 {
			continue
		}
		endpoint := body[loc[2]:loc[3]]
		handle := body[loc[4]:loc[5]]
		if !allowed[endpoint] {
			continue
		}
		address, err := r.store.ParticipantByHandle(ctx, endpoint, handle)
		if err != nil {
			// Never guess identities from display names or forward internal IDs.
			// A database error may prevent an address from resolving, but a
			// failed notification must not suppress a normal message.
			continue
		}
		references = append(references, transport.CrossReference{
			Token: body[loc[0]:loc[1]], Endpoint: transport.EndpointID(address.Endpoint), ActorID: address.ActorID,
		})
	}
	return references, nil
}

// allReferenceTokens are intentionally idempotent and content-free in storage:
// only messages being actively delivered carry the resolved references.
func hasCrossReference(refs []transport.CrossReference, token string, endpoint transport.EndpointID) bool {
	for _, ref := range refs {
		if ref.Token == token && ref.Endpoint == endpoint {
			return true
		}
	}
	return false
}

func bridgeHandleFromLabel(label string) string {
	return strings.TrimSpace(label)
}

// addressedSender makes compact address handles available in anonymized
// destinations without exposing an identity or depending on a display name.
// Existing pseudonym handling remains the fallback when the actor has not
// been observed or the presentation mode is opaque/hash-based.
func (r *Router) addressedSender(ctx context.Context, incoming transport.Incoming, target routeTarget, sender transport.Sender) transport.Sender {
	if !target.AnonymizeToDiscord || r.getUsernameMode() != config.UsernameModePushName {
		return sender
	}
	address, err := r.store.ParticipantByActor(ctx, string(incoming.Endpoint), incoming.Sender.OpaqueID)
	if err == nil {
		sender.DisplayName = address.Handle
		sender.PhoneNumber = ""
	}
	return sender
}
