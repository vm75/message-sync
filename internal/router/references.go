package router

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/transport"
)

const maxCrossReferences = 32

// ResolveParticipantReference performs global address lookup only. The private
// relay must independently authorize the sender and both set-local inboxes.
func (r *Router) ResolveParticipantReference(ctx context.Context, token string) (store.AliasRecord, error) {
	alias, ok := identity.ParseAliasToken(token)
	if !ok {
		return store.AliasRecord{}, sql.ErrNoRows
	}
	return r.store.ActorByAlias(ctx, alias)
}

// crossReferences preserves authored text and resolves only in the source set.
func (r *Router) crossReferences(ctx context.Context, source transport.EndpointID, body string) ([]transport.CrossReference, error) {
	return r.referencesForSet(ctx, r.syncSetFor(source), body)
}

func (r *Router) referencesForSet(ctx context.Context, setID, body string) ([]transport.CrossReference, error) {
	if setID == "" {
		return nil, nil
	}
	var refs []transport.CrossReference
	for _, loc := range identity.AliasTokenSpans(body) {
		if len(refs) >= maxCrossReferences {
			break
		}
		alias, _ := identity.ParseAliasToken(body[loc[0]:loc[1]])
		record, err := r.store.ActorInSyncSetByAlias(ctx, setID, alias)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("resolve alias reference: %w", err)
		}
		refs = append(refs, transport.CrossReference{Token: body[loc[0]:loc[1]], ActorID: record.ActorID, Alias: record.Alias})
	}
	return refs, nil
}
