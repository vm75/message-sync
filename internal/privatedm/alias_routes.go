package privatedm

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/transport"
)

func (r *Relay) aliasFailure(ctx context.Context, inbox transport.PrivateInbox, lookupErr error) error {
	replyErr := r.help(ctx, inbox, "Delivery unavailable. Check the address and your shared sync set.")
	if errors.Is(lookupErr, sql.ErrNoRows) {
		return replyErr
	}
	// Keep the response identical while allowing safe operational logging of an
	// infrastructure/integrity failure rather than treating it as an absent actor.
	return errors.Join(errors.New("read private alias assignment"), lookupErr, replyErr)
}

func findSetByID(cfg *config.Config, id string) (config.SyncSet, bool) {
	for _, set := range cfg.SyncSets {
		if set.ID == id {
			return set, true
		}
	}
	return config.SyncSet{}, false
}

// recipientRoute accepts one verified inbox, possibly reached through several
// equivalent group endpoints. Historical observations never authorize delivery.
func (r *Relay) recipientRoute(ctx context.Context, cfg *config.Config, actor store.AliasRecord, now time.Time) (string, transport.PrivateInbox, bool, error) {
	endpoints, err := r.Sync.ActorEndpoints(ctx, actor.SyncSetID, actor.ActorID)
	if err != nil {
		return "", transport.PrivateInbox{}, false, err
	}
	var selected string
	var inbox transport.PrivateInbox
	for _, endpoint := range endpoints {
		set, ok := findSet(cfg, endpoint)
		ep, exists := cfg.Endpoints[endpoint]
		if !ok || !exists || set.ID != actor.SyncSetID || !cfg.PrivateDMConnectionEnabled(ep.ConnectionID) {
			continue
		}
		candidate, found := r.Runtime.ResolvePrivateInbox(ctx, transport.EndpointID(endpoint), actor.ActorID)
		if !found && ep.Transport == config.TransportTelegram {
			candidate, found, err = r.State.Inbox(ctx, ep.ConnectionID, string(ep.Transport), actor.ActorID, now)
			if err != nil {
				return "", transport.PrivateInbox{}, false, err
			}
		}
		if !found || candidate.ConnectionID != ep.ConnectionID || candidate.Transport != string(ep.Transport) || !r.Runtime.VerifyPrivateInboxMember(ctx, transport.EndpointID(endpoint), actor.ActorID, candidate) {
			continue
		}
		if selected != "" && !sameInbox(inbox, candidate) {
			return "", transport.PrivateInbox{}, false, nil
		}
		if selected == "" {
			selected, inbox = endpoint, candidate
		}
	}
	return selected, inbox, selected != "", nil
}
