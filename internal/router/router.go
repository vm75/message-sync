package router

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/transport"
)

type sender interface {
	Send(context.Context, transport.Outgoing) (transport.MessageRef, error)
}

type copyKey struct {
	endpoint transport.EndpointID
	remoteID string
}

type Router struct {
	store        *store.Store
	sender       sender
	routes       map[transport.EndpointID][]transport.EndpointID
	usernameMode string
	knownCopies  map[copyKey]string
	newCanonical func() (string, error)
	afterPersist func(transport.EndpointID) error
}

func New(cfg *config.Config, syncStore *store.Store, transportSender sender) (*Router, error) {
	if cfg == nil {
		return nil, errors.New("config is required")
	}
	if syncStore == nil {
		return nil, errors.New("sync store is required")
	}
	if transportSender == nil {
		return nil, errors.New("transport sender is required")
	}
	if cfg.Identity.UsernameMode != "push_name" && cfg.Identity.UsernameMode != "hash" {
		return nil, errors.New("username mode must be push_name or hash")
	}

	routes := make(map[transport.EndpointID][]transport.EndpointID, len(cfg.Groups))
	for _, set := range cfg.SyncSets {
		members := make([]transport.EndpointID, 0, len(set.Groups))
		for _, alias := range set.Groups {
			members = append(members, transport.EndpointID(alias))
		}
		for _, member := range members {
			routes[member] = members
		}
	}
	if len(routes) == 0 {
		return nil, errors.New("at least one sync route is required")
	}

	return &Router{
		store:        syncStore,
		sender:       transportSender,
		routes:       routes,
		usernameMode: cfg.Identity.UsernameMode,
		knownCopies:  make(map[copyKey]string),
		newCanonical: newCanonicalID,
	}, nil
}

func (r *Router) Handle(ctx context.Context, incoming transport.Incoming) error {
	if incoming.Kind != "text" {
		return nil
	}
	members, configured := r.routes[incoming.Endpoint]
	if !configured {
		return nil
	}
	if strings.TrimSpace(incoming.RemoteID) == "" {
		return errors.New("incoming remote message id is required")
	}

	canonicalID, created, err := r.resolveCanonical(ctx, incoming)
	if err != nil {
		return err
	}
	if incoming.FromSelf && !created {
		return nil
	}
	forwardedText, err := r.forwardedText(incoming)
	if err != nil {
		return err
	}

	for _, destination := range members {
		if destination == incoming.Endpoint {
			continue
		}
		copy, err := r.store.MessageCopyForEndpoint(ctx, canonicalID, string(destination))
		if err == nil {
			r.knownCopies[copyKey{endpoint: destination, remoteID: copy.RemoteMessageID}] = canonicalID
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("look up destination copy: %w", err)
		}

		ref, err := r.sender.Send(ctx, transport.Outgoing{
			Endpoint: destination,
			Kind:     "text",
			Text:     forwardedText,
		})
		if err != nil {
			return fmt.Errorf("send destination copy: %w", err)
		}
		if ref.Endpoint != destination || strings.TrimSpace(ref.RemoteMessageID) == "" {
			return errors.New("transport returned invalid destination message reference")
		}

		if err := r.store.AddMessageCopy(ctx, store.MessageCopy{
			CanonicalID:     canonicalID,
			EndpointID:      string(destination),
			RemoteMessageID: ref.RemoteMessageID,
			CreatedAt:       time.Now().UTC(),
		}); err != nil {
			return fmt.Errorf("persist destination copy: %w", err)
		}
		r.knownCopies[copyKey{endpoint: destination, remoteID: ref.RemoteMessageID}] = canonicalID
		if r.afterPersist != nil {
			if err := r.afterPersist(destination); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Router) resolveCanonical(ctx context.Context, incoming transport.Incoming) (string, bool, error) {
	key := copyKey{endpoint: incoming.Endpoint, remoteID: incoming.RemoteID}
	if canonicalID, ok := r.knownCopies[key]; ok {
		return canonicalID, false, nil
	}

	candidate, err := r.newCanonical()
	if err != nil {
		return "", false, fmt.Errorf("generate canonical id: %w", err)
	}
	canonicalID, created, err := r.store.ResolveOrCreateCanonical(ctx, candidate, store.MessageCopy{
		EndpointID:      string(incoming.Endpoint),
		RemoteMessageID: incoming.RemoteID,
		CreatedAt:       incoming.Timestamp,
	})
	if err != nil {
		return "", false, err
	}
	r.knownCopies[key] = canonicalID
	return canonicalID, created, nil
}

func (r *Router) forwardedText(incoming transport.Incoming) (string, error) {
	username := incoming.Sender.OpaqueID
	if r.usernameMode == "push_name" {
		if displayName := normalizeDisplayName(incoming.Sender.DisplayName); displayName != "" {
			username = displayName
		}
	}
	if strings.TrimSpace(username) == "" {
		return "", errors.New("incoming sender identity is required")
	}
	return fmt.Sprintf("%s/%s: %s", incoming.Endpoint, username, incoming.Text), nil
}

func normalizeDisplayName(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func newCanonicalID() (string, error) {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", err
	}
	return "c_" + hex.EncodeToString(randomBytes[:]), nil
}
