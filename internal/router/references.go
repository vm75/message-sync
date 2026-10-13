package router

import (
	"context"
	"regexp"
	"strings"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
)

// Explicit bridge syntax is separate from native provider mentions. The
// endpoint is a configured alias, and a label is either a stable Pn handle
// or a recently observed unambiguous display name/pseudonym.
var crossReferencePattern = regexp.MustCompile(`@\{([A-Za-z0-9][A-Za-z0-9_-]{0,63})/([^{}\r\n]{1,96})\}`)
var stableReferenceHandle = regexp.MustCompile(`^P[1-9][0-9]*$`)

const maxCrossReferences = 32

func normalizedReferenceName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

// observeReferenceName retains at most one transient display name per known
// HMAC actor. The map is not persisted, logged, or used for identity linking
// between different endpoints; ambiguous names never resolve to a person.
func (r *Router) observeReferenceName(endpoint transport.EndpointID, actor, name string) {
	name = strings.Join(strings.Fields(name), " ")
	if len(name) == 0 || len([]rune(name)) > 96 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.referenceNames == nil {
		r.referenceNames = make(map[transport.EndpointID]map[string]string)
	}
	if r.referenceNames[endpoint] == nil {
		r.referenceNames[endpoint] = make(map[string]string)
	}
	r.referenceNames[endpoint][actor] = name
}

// observedReferenceName is presentation only: a change of display name never
// changes the stable participant handle or a resolved HMAC actor.
func (r *Router) observedReferenceName(endpoint transport.EndpointID, actor string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.referenceNames[endpoint][actor]
}

func (r *Router) observedReferenceActor(endpoint transport.EndpointID, label string) string {
	normalized := normalizedReferenceName(label)
	if normalized == "" {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var matched string
	for actor, name := range r.referenceNames[endpoint] {
		if normalized != normalizedReferenceName(name) && normalized != normalizedReferenceName(identity.Pseudonym(actor)) && normalized != strings.ToLower(actor) {
			continue
		}
		if matched != "" && matched != actor {
			return "" // ambiguity: never guess a native recipient
		}
		matched = actor
	}
	return matched
}

// crossReferences resolves identities only from the sender's sync set.
// It does not edit the original text. Every provider renders references,
// including unresolved ones, to a safe non-notifying textual fallback.
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
		label := strings.TrimSpace(body[loc[4]:loc[5]])
		if !allowed[endpoint] || label == "" {
			continue
		}

		var addressActor, addressHandle string
		if stableReferenceHandle.MatchString(label) {
			if address, err := r.store.ParticipantByHandle(ctx, endpoint, label); err == nil {
				addressActor, addressHandle = address.ActorID, address.Handle
			}
		} else if actor := r.observedReferenceActor(transport.EndpointID(endpoint), label); actor != "" {
			if address, err := r.store.ParticipantByActor(ctx, endpoint, actor); err == nil {
				addressActor, addressHandle = address.ActorID, address.Handle
			}
		}
		if addressActor == "" {
			continue
		}
		references = append(references, transport.CrossReference{
			Token: body[loc[0]:loc[1]], Endpoint: transport.EndpointID(endpoint),
			ActorID: addressActor, Handle: addressHandle,
			Name: r.observedReferenceName(transport.EndpointID(endpoint), addressActor),
		})
	}
	return references, nil
}

// addressedSender keeps the stable compact handle available to recipients in
// an anonymized Discord destination. The original sender identity and native
// notification target remain distinct from its visible label.
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
