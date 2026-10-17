package router

import (
	"context"
	"regexp"
	"strings"

	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
)

// Explicit bridge syntax is separate from native provider mentions. The
// endpoint is a configured alias, and a label is either a stable Pn handle
// or a recently observed unambiguous display name/pseudonym.
var crossReferencePattern = regexp.MustCompile(`@\{([A-Za-z0-9][A-Za-z0-9_-]{0,63})/([^{}\r\n]{1,96})\}`)
var discriminatorLabel = regexp.MustCompile(`(?i)^[23456789ABCDEFGHJKLMNPQRSTUVWXYZ]{5,10}$`)
var stableReferenceHandle = regexp.MustCompile(`^P[1-9][0-9]*$`)

const maxCrossReferences = 32

func normalizedReferenceName(name string) string {
	return transport.NormalizeParticipantLabel(name)
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

// observedReferenceActor scans the persisted, per-endpoint HMAC identities,
// making deterministic pseudonym addressing work across server restarts.
// Real names are transient; they never enter the participant registry.
// The two-word stem (for example "Rust Ocean") is accepted only when unique.
func (r *Router) observedReferenceActor(ctx context.Context, endpoint transport.EndpointID, label string) (string, error) {
	normalized := normalizedReferenceName(label)
	if normalized == "" {
		return "", nil
	}
	addresses, err := r.store.ParticipantsForEndpoint(ctx, string(endpoint))
	if err != nil {
		return "", err
	}
	r.mu.RLock()
	var matched string
	isCode := discriminatorLabel.MatchString(label)
	aliasStem, aliasCode := "", ""
	hasAliasCode := false
	if split := strings.LastIndex(strings.TrimSpace(label), " "); split >= 0 {
		aliasStem = normalizedReferenceName(label[:split])
		aliasCode = label[split+1:]
		hasAliasCode = true
	} else {
		hasAliasCode = false
	}
	hasAliasCode = hasAliasCode && discriminatorLabel.MatchString(aliasCode)
	for _, address := range addresses {
		alias := identity.Pseudonym(address.ActorID)
		shortAlias := identity.PseudonymName(address.ActorID)
		if label != address.Handle && !(isCode && strings.EqualFold(label, address.Handle)) &&
			!(isCode && normalized == strings.ToLower(identity.DiscriminatorForLength(address.ActorID, len(label)))) &&
			!(hasAliasCode && aliasStem == normalizedReferenceName(shortAlias) && strings.EqualFold(aliasCode, identity.DiscriminatorForLength(address.ActorID, len(aliasCode)))) &&
			normalized != normalizedReferenceName(r.referenceNames[endpoint][address.ActorID]) &&
			normalized != normalizedReferenceName(alias) &&
			normalized != normalizedReferenceName(shortAlias) &&
			normalized != strings.ToLower(address.ActorID) {
			continue
		}
		if matched != "" && matched != address.ActorID {
			r.mu.RUnlock()
			return "", nil // ambiguous: never guess the native recipient
		}
		matched = address.ActorID
	}
	r.mu.RUnlock()
	if matched != "" || len(addresses) == 0 || stableReferenceHandle.MatchString(label) {
		return matched, nil
	}
	resolver, ok := r.sender.(transport.ParticipantResolver)
	if !ok {
		return "", nil
	}
	actors := make([]string, 0, len(addresses))
	for _, address := range addresses {
		actors = append(actors, address.ActorID)
	}
	actor, found := resolver.ResolveParticipant(ctx, endpoint, label, actors)
	if !found {
		return "", nil
	}
	for _, known := range actors {
		if actor == known {
			r.observeReferenceName(endpoint, actor, label)
			return actor, nil
		}
	}
	return "", nil
}

// crossReferences resolves identities only from the sender's sync set.
// It does not edit the original text. Providers preserve each token unless
// they can verify destination membership and render a native mention.
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
		if actor, err := r.observedReferenceActor(ctx, transport.EndpointID(endpoint), label); err == nil && actor != "" {
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
			Name:      r.observedReferenceName(transport.EndpointID(endpoint), addressActor),
			Pseudonym: identity.Pseudonym(addressActor),
		})
	}
	return references, nil
}
