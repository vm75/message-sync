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
	React(context.Context, transport.Reaction) error
	Edit(context.Context, transport.MessageRef, string) error
	Delete(context.Context, transport.MessageRef) error
}

type copyKey struct {
	endpoint transport.EndpointID
	remoteID string
}

type sentReactionKey struct {
	endpoint transport.EndpointID
	remoteID string
	emoji    string
}

type Router struct {
	store         *store.Store
	sender        sender
	routes        map[transport.EndpointID][]transport.EndpointID
	usernameMode  string
	knownCopies   map[copyKey]string
	sentReactions map[sentReactionKey]struct{}
	newCanonical  func() (string, error)
	afterPersist  func(transport.EndpointID) error
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
		store:         syncStore,
		sender:        transportSender,
		routes:        routes,
		usernameMode:  cfg.Identity.UsernameMode,
		knownCopies:   make(map[copyKey]string),
		sentReactions: make(map[sentReactionKey]struct{}),
		newCanonical:  newCanonicalID,
	}, nil
}

func (r *Router) Handle(ctx context.Context, incoming transport.Incoming) error {
	if incoming.Kind == "other" {
		return nil
	}
	members, configured := r.routes[incoming.Endpoint]
	if !configured {
		return nil
	}
	if strings.TrimSpace(incoming.RemoteID) == "" {
		return errors.New("incoming remote message id is required")
	}

	if incoming.Kind == "delete" || incoming.Kind == "revoke" {
		if incoming.ReplyTo == nil || incoming.ReplyTo.RemoteMessageID == "" {
			return nil
		}
		targetCanonical, err := r.store.CanonicalForRemote(ctx, string(incoming.Endpoint), incoming.ReplyTo.RemoteMessageID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil // Target unknown
			}
			return fmt.Errorf("resolve delete target: %w", err)
		}

		if err := r.store.TombstoneCanonical(ctx, targetCanonical, incoming.Timestamp); err != nil {
			return fmt.Errorf("tombstone canonical: %w", err)
		}

		for _, destination := range members {
			if destination == incoming.Endpoint {
				continue
			}
			targetCopy, err := r.store.MessageCopyForEndpoint(ctx, targetCanonical, string(destination))
			if err != nil {
				continue
			}
			if err := r.sender.Delete(ctx, transport.MessageRef{
				Endpoint:        destination,
				RemoteMessageID: targetCopy.RemoteMessageID,
				IsTargetFromMe:  targetCopy.FromSelf,
			}); err != nil {
				return fmt.Errorf("send destination delete: %w", err)
			}
		}
		_ = r.store.PutRecoveryCursor(ctx, store.RecoveryCursor{
			EndpointID:       string(incoming.Endpoint),
			RemoteMessageID:  incoming.RemoteID,
			MessageTimestamp: incoming.Timestamp,
			UpdatedAt:        time.Now().UTC(),
		})
		return nil
	}

	if incoming.Kind == "edit" {
		if incoming.ReplyTo == nil || incoming.ReplyTo.RemoteMessageID == "" {
			return nil
		}
		targetCanonical, err := r.store.CanonicalForRemote(ctx, string(incoming.Endpoint), incoming.ReplyTo.RemoteMessageID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil // Target unknown
			}
			return fmt.Errorf("resolve edit target: %w", err)
		}

		isTombstoned, err := r.store.IsTombstoned(ctx, targetCanonical)
		if err != nil {
			return fmt.Errorf("check edit target tombstone: %w", err)
		}
		if isTombstoned {
			return nil // Cannot edit a deleted message
		}

		forwardedText, err := r.forwardedText(incoming)
		if err != nil {
			return err
		}

		for _, destination := range members {
			if destination == incoming.Endpoint {
				continue
			}
			targetCopy, err := r.store.MessageCopyForEndpoint(ctx, targetCanonical, string(destination))
			if err != nil {
				continue
			}
			if err := r.sender.Edit(ctx, transport.MessageRef{
				Endpoint:        destination,
				RemoteMessageID: targetCopy.RemoteMessageID,
				IsTargetFromMe:  targetCopy.FromSelf,
			}, forwardedText); err != nil {
				return fmt.Errorf("send destination edit: %w", err)
			}
		}
		_ = r.store.PutRecoveryCursor(ctx, store.RecoveryCursor{
			EndpointID:       string(incoming.Endpoint),
			RemoteMessageID:  incoming.RemoteID,
			MessageTimestamp: incoming.Timestamp,
			UpdatedAt:        time.Now().UTC(),
		})
		return nil
	}

	if incoming.Kind == "reaction" {
		if incoming.ReplyTo == nil || incoming.ReplyTo.RemoteMessageID == "" {
			return errors.New("reaction target is required")
		}
		if incoming.FromSelf {
			// The bridge is a linked device, so user reactions are also FromSelf.
			// Distinguish genuine user reactions from bridge echo by checking
			// whether we recently sent this exact reaction to this endpoint.
			key := sentReactionKey{
				endpoint: incoming.Endpoint,
				remoteID: incoming.ReplyTo.RemoteMessageID,
				emoji:    strings.TrimSpace(incoming.Text),
			}
			if _, echoed := r.sentReactions[key]; echoed {
				delete(r.sentReactions, key)
				return nil
			}
		}
		targetCanonical, err := r.store.CanonicalForRemote(ctx, string(incoming.Endpoint), incoming.ReplyTo.RemoteMessageID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil // Target unknown
			}
			return fmt.Errorf("resolve reaction target: %w", err)
		}

		isTombstoned, err := r.store.IsTombstoned(ctx, targetCanonical)
		if err != nil {
			return fmt.Errorf("check reaction target tombstone: %w", err)
		}
		if isTombstoned {
			return nil
		}

		emoji := strings.TrimSpace(incoming.Text)
		if emoji == "" {
			err = r.store.DeleteReaction(ctx, targetCanonical, string(incoming.Endpoint), incoming.Sender.OpaqueID)
		} else {
			err = r.store.UpsertReaction(ctx, store.Reaction{
				CanonicalID:      targetCanonical,
				SourceEndpointID: string(incoming.Endpoint),
				ActorHash:        incoming.Sender.OpaqueID,
				Emoji:            emoji,
				UpdatedAt:        incoming.Timestamp,
			})
		}
		if err != nil {
			return err
		}

		username := incoming.Sender.OpaqueID
		if r.usernameMode == "push_name" {
			displayName := normalizeDisplayName(incoming.Sender.DisplayName)
			phone := incoming.Sender.PhoneNumber
			if phone != "" && displayName != "" {
				username = fmt.Sprintf("%s (%s)", phone, displayName)
			} else if displayName != "" {
				username = displayName
			} else if phone != "" {
				username = phone
			}
		}
		fallbackText := fmt.Sprintf("%s/%s removed their reaction from a message", incoming.Endpoint, username)
		if emoji != "" {
			fallbackText = fmt.Sprintf("%s/%s reacted %s to a message", incoming.Endpoint, username, emoji)
		}

		for _, destination := range members {
			if destination == incoming.Endpoint {
				continue
			}
			targetCopy, err := r.store.MessageCopyForEndpoint(ctx, targetCanonical, string(destination))
			if err != nil {
				continue // Don't forward reaction if target copy missing
			}

			if err := r.sender.React(ctx, transport.Reaction{
				Endpoint:       destination,
				TargetRemoteID: targetCopy.RemoteMessageID,
				IsTargetFromMe: targetCopy.FromSelf,
				Emoji:          emoji,
				FallbackText:   fallbackText,
			}); err != nil {
				return fmt.Errorf("send reaction copy: %w", err)
			}
			r.sentReactions[sentReactionKey{
				endpoint: destination,
				remoteID: targetCopy.RemoteMessageID,
				emoji:    emoji,
			}] = struct{}{}
		}
		_ = r.store.PutRecoveryCursor(ctx, store.RecoveryCursor{
			EndpointID:       string(incoming.Endpoint),
			RemoteMessageID:  incoming.RemoteID,
			MessageTimestamp: incoming.Timestamp,
			UpdatedAt:        time.Now().UTC(),
		})
		return nil
	}

	canonicalID, created, err := r.resolveCanonical(ctx, incoming)
	if err != nil {
		return err
	}
	isTombstoned, err := r.store.IsTombstoned(ctx, canonicalID)
	if err != nil {
		return fmt.Errorf("check canonical tombstone: %w", err)
	}
	if isTombstoned {
		return nil
	}
	if incoming.FromSelf && !created {
		return nil
	}
	forwardedText, err := r.forwardedText(incoming)
	if err != nil {
		return err
	}

	var replyToCanonical string
	if incoming.ReplyTo != nil && incoming.ReplyTo.RemoteMessageID != "" {
		rc, err := r.store.CanonicalForRemote(ctx, string(incoming.Endpoint), incoming.ReplyTo.RemoteMessageID)
		if err == nil {
			replyToCanonical = rc
		}
	}

	var mediaBytes []byte
	if incoming.MediaLoader != nil {
		b, err := incoming.MediaLoader(ctx)
		if err != nil {
			return fmt.Errorf("load incoming media: %w", err)
		}
		mediaBytes = b
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

		var outgoingReplyTo *transport.MessageRef
		if replyToCanonical != "" {
			if targetCopy, err := r.store.MessageCopyForEndpoint(ctx, replyToCanonical, string(destination)); err == nil {
				outgoingReplyTo = &transport.MessageRef{
					Endpoint:        destination,
					RemoteMessageID: targetCopy.RemoteMessageID,
					IsTargetFromMe:  targetCopy.FromSelf,
				}
			}
		}

		if len(mediaBytes) > 0 && (incoming.Kind == "audio" || incoming.Kind == "sticker") {
			_, err := r.sender.Send(ctx, transport.Outgoing{
				Endpoint:   destination,
				Kind:       "text",
				Text:       forwardedText,
				ReplyTo:    outgoingReplyTo,
				QuotedText: incoming.QuotedText,
			})
			if err != nil {
				return fmt.Errorf("send companion attribution: %w", err)
			}
		}

		var outgoingText string
		if incoming.Kind == "text" || incoming.Kind == "image" || incoming.Kind == "video" || incoming.Kind == "document" {
			outgoingText = forwardedText
		}

		ref, err := r.sender.Send(ctx, transport.Outgoing{
			Endpoint:   destination,
			Kind:       incoming.Kind,
			Text:       outgoingText,
			MediaBytes: mediaBytes,
			ReplyTo:    outgoingReplyTo,
			QuotedText: incoming.QuotedText,
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
			FromSelf:        true,
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
	_ = r.store.PutRecoveryCursor(ctx, store.RecoveryCursor{
		EndpointID:       string(incoming.Endpoint),
		RemoteMessageID:  incoming.RemoteID,
		MessageTimestamp: incoming.Timestamp,
		UpdatedAt:        time.Now().UTC(),
	})
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
		FromSelf:        incoming.FromSelf,
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
		displayName := normalizeDisplayName(incoming.Sender.DisplayName)
		phone := incoming.Sender.PhoneNumber
		if phone != "" && displayName != "" {
			username = fmt.Sprintf("%s (%s)", phone, displayName)
		} else if displayName != "" {
			username = displayName
		} else if phone != "" {
			username = phone
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
