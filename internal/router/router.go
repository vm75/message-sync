package router

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/delivery"
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

type pollMeta struct {
	Question string
	Options  []string
	Hashes   []string
}

type Router struct {
	store        *store.Store
	sender       sender
	lanes        *delivery.Manager
	routes       map[transport.EndpointID][]transport.EndpointID
	usernameMode config.UsernameMode
	aggTrigger   string
	knownCopies  map[copyKey]string
	pollCache    map[string]pollMeta
	newCanonical func() (string, error)
	afterPersist func(transport.EndpointID) error
	mu           sync.RWMutex
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
	if !cfg.Identity.UsernameMode.IsValid() {
		return nil, errors.New("username mode must be push_name or hash")
	}

	routes := make(map[transport.EndpointID][]transport.EndpointID, len(cfg.Endpoints))
	endpoints := make([]transport.EndpointID, 0, len(cfg.Endpoints))
	for alias := range cfg.Endpoints {
		endpoints = append(endpoints, transport.EndpointID(alias))
	}
	for _, set := range cfg.SyncSets {
		members := make([]transport.EndpointID, 0, len(set.Endpoints))
		for _, alias := range set.Endpoints {
			members = append(members, transport.EndpointID(alias))
		}
		for _, member := range members {
			routes[member] = members
		}
	}

	lanes, err := delivery.New(context.Background(), 32, endpoints)
	if err != nil {
		return nil, fmt.Errorf("create delivery lanes: %w", err)
	}
	return &Router{
		store:        syncStore,
		sender:       transportSender,
		lanes:        lanes,
		routes:       routes,
		usernameMode: cfg.Identity.UsernameMode,
		aggTrigger:   cfg.Polls.AggregationTrigger,
		knownCopies:  make(map[copyKey]string),
		pollCache:    make(map[string]pollMeta),
		newCanonical: newCanonicalID,
	}, nil
}

func (r *Router) UpdateConfig(cfg *config.Config) error {
	if cfg == nil {
		return errors.New("config is required")
	}
	if !cfg.Identity.UsernameMode.IsValid() {
		return errors.New("username mode must be push_name or hash")
	}

	routes := make(map[transport.EndpointID][]transport.EndpointID, len(cfg.Endpoints))
	for _, set := range cfg.SyncSets {
		members := make([]transport.EndpointID, 0, len(set.Endpoints))
		for _, alias := range set.Endpoints {
			members = append(members, transport.EndpointID(alias))
		}
		for _, member := range members {
			routes[member] = members
		}
	}
	endpoints := make([]transport.EndpointID, 0, len(cfg.Endpoints))
	for alias := range cfg.Endpoints {
		endpoints = append(endpoints, transport.EndpointID(alias))
	}
	if err := r.lanes.Update(endpoints); err != nil {
		return fmt.Errorf("update delivery lanes: %w", err)
	}
	r.mu.Lock()
	r.routes = routes
	r.usernameMode = cfg.Identity.UsernameMode
	r.aggTrigger = cfg.Polls.AggregationTrigger
	r.mu.Unlock()
	return nil
}

func (r *Router) Close() {
	if r == nil || r.lanes == nil {
		return
	}
	r.lanes.Close()
}

func (r *Router) Handle(ctx context.Context, incoming transport.Incoming) error {
	if incoming.Kind == "other" {
		return nil
	}
	r.mu.RLock()
	members, configured := r.routes[incoming.Endpoint]
	r.mu.RUnlock()
	if !configured {
		return nil
	}
	if strings.TrimSpace(incoming.RemoteID) == "" {
		return errors.New("incoming remote message id is required")
	}

	if incoming.Kind == "poll_vote" {
		if incoming.ReplyTo == nil || incoming.ReplyTo.RemoteMessageID == "" {
			return nil
		}
		targetCanonical, err := r.store.CanonicalForRemote(ctx, string(incoming.Endpoint), incoming.ReplyTo.RemoteMessageID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil // Target unknown
			}
			return fmt.Errorf("resolve poll vote target: %w", err)
		}

		isTombstoned, err := r.store.IsTombstoned(ctx, targetCanonical)
		if err != nil {
			return fmt.Errorf("check poll vote target tombstone: %w", err)
		}
		if isTombstoned {
			return nil
		}

		if err := r.store.RecordPollVote(ctx, targetCanonical, string(incoming.Endpoint), incoming.Sender.OpaqueID, incoming.PollOptionHashes, incoming.Timestamp); err != nil {
			return fmt.Errorf("record poll vote: %w", err)
		}

		_ = r.store.PutRecoveryCursor(ctx, store.RecoveryCursor{
			EndpointID:       string(incoming.Endpoint),
			RemoteMessageID:  incoming.RemoteID,
			MessageTimestamp: incoming.Timestamp,
			UpdatedAt:        time.Now().UTC(),
		})
		return nil
	}

	triggerPhrase := r.aggTrigger
	if triggerPhrase == "" {
		triggerPhrase = "aggregate-response"
	}

	if strings.TrimSpace(incoming.Text) == triggerPhrase && incoming.ReplyTo != nil && incoming.ReplyTo.RemoteMessageID != "" {
		targetCanonical, err := r.store.CanonicalForRemote(ctx, string(incoming.Endpoint), incoming.ReplyTo.RemoteMessageID)
		if err == nil && targetCanonical != "" {
			isPoll, err := r.store.IsPoll(ctx, targetCanonical)
			if err == nil && isPoll {
				return r.handlePollAggregation(ctx, incoming, targetCanonical, members)
			}
		}
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
			emoji := strings.TrimSpace(incoming.Text)
			echoed, err := r.store.CheckAndClearSuppressedReaction(ctx, string(incoming.Endpoint), incoming.ReplyTo.RemoteMessageID, emoji)
			if err != nil {
				return fmt.Errorf("check suppressed reaction: %w", err)
			}
			if echoed {
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
		if r.getUsernameMode() == config.UsernameModePushName {
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
			if err := r.store.RecordSuppressedReaction(ctx, string(destination), targetCopy.RemoteMessageID, emoji, time.Now().UTC()); err != nil {
				return fmt.Errorf("record suppressed reaction: %w", err)
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

	if incoming.Kind == "poll" {
		optionHashes := make([]string, len(incoming.PollOptions))
		for i, opt := range incoming.PollOptions {
			h := sha256.Sum256([]byte(opt))
			optionHashes[i] = hex.EncodeToString(h[:])
		}
		if created {
			if err := r.store.SavePollOptions(ctx, canonicalID, optionHashes); err != nil {
				return fmt.Errorf("save poll options: %w", err)
			}
		}
		r.mu.Lock()
		r.pollCache[canonicalID] = pollMeta{
			Question: incoming.Text,
			Options:  incoming.PollOptions,
			Hashes:   optionHashes,
		}
		r.mu.Unlock()
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
			r.mu.Lock()
			r.knownCopies[copyKey{endpoint: destination, remoteID: copy.RemoteMessageID}] = canonicalID
			r.mu.Unlock()
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

		if err := r.enqueueCreate(ctx, canonicalID, incoming, destination, forwardedText, outgoingReplyTo, mediaBytes); err != nil {
			return err
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

func (r *Router) enqueueCreate(ctx context.Context, canonicalID string, incoming transport.Incoming, destination transport.EndpointID, forwardedText string, replyTo *transport.MessageRef, mediaBytes []byte) error {
	now := time.Now().UTC()
	operation := store.DeliveryOperation{
		CanonicalID: canonicalID, EndpointID: string(destination), OperationKind: "create", OperationRevision: 1,
		State: store.DeliveryQueued, CreatedAt: now, UpdatedAt: now,
	}
	if err := r.store.UpsertDeliveryOperation(ctx, operation); err != nil {
		return fmt.Errorf("queue delivery operation: %w", err)
	}
	job := func(jobCtx context.Context, attempt int) error {
		tombstoned, err := r.store.IsTombstoned(jobCtx, canonicalID)
		if err != nil {
			return err
		}
		if tombstoned {
			_ = r.store.DeleteDeliveryOperation(jobCtx, operation)
			return nil
		}
		if err := r.store.BeginDeliveryAttempt(jobCtx, operation, time.Now().UTC()); err != nil {
			return err
		}
		finishFailure := func(err error) error {
			failure := transport.Classify(err)
			if !failure.Retryable || attempt >= delivery.DefaultRetryPolicy().MaxAttempts || jobCtx.Err() != nil {
				stateErr := error(nil)
				if failure.Retryable && jobCtx.Err() != nil || failure.Retryable && attempt >= delivery.DefaultRetryPolicy().MaxAttempts {
					stateErr = r.store.MarkDeliveryOperationAwaitingReplay(jobCtx, operation, time.Now().UTC())
				} else {
					stateErr = r.store.MarkDeliveryOperationFailed(jobCtx, operation, string(failure.Class), time.Now().UTC())
				}
				if stateErr != nil {
					return stateErr
				}
				return nil
			}
			return err
		}
		if len(mediaBytes) > 0 && (incoming.Kind == "audio" || incoming.Kind == "sticker") {
			if _, err := r.sender.Send(jobCtx, transport.Outgoing{
				Endpoint: destination, OriginEndpoint: incoming.Endpoint, Sender: incoming.Sender,
				SourceText: incoming.Text, AttributionOnly: true,
				ReplyFallback: incoming.ReplyTo != nil && replyTo == nil, Kind: "text", Text: forwardedText,
				ReplyTo: replyTo, QuotedText: incoming.QuotedText,
			}); err != nil {
				return finishFailure(err)
			}
		}
		ref, err := r.sender.Send(jobCtx, transport.Outgoing{
			Endpoint: destination, OriginEndpoint: incoming.Endpoint, Sender: incoming.Sender,
			SourceText: incoming.Text, ReplyFallback: incoming.ReplyTo != nil && replyTo == nil,
			Kind: incoming.Kind, Text: forwardedText, Mentions: incoming.Mentions,
			MediaBytes: mediaBytes, ReplyTo: replyTo, QuotedText: incoming.QuotedText,
			PollOptions: incoming.PollOptions, PollSelectableCount: incoming.PollSelectableCount,
		})
		if err != nil {
			return finishFailure(err)
		}
		if ref.Endpoint != destination || strings.TrimSpace(ref.RemoteMessageID) == "" {
			return finishFailure(errors.New("transport returned invalid destination message reference"))
		}
		if err := r.store.AddMessageCopy(jobCtx, store.MessageCopy{
			CanonicalID: canonicalID, EndpointID: string(destination), RemoteMessageID: ref.RemoteMessageID,
			CreatedAt: time.Now().UTC(), FromSelf: true,
		}); err != nil {
			return finishFailure(err)
		}
		r.mu.Lock()
		r.knownCopies[copyKey{endpoint: destination, remoteID: ref.RemoteMessageID}] = canonicalID
		r.mu.Unlock()
		_ = r.store.DeleteDeliveryOperation(jobCtx, operation)
		if r.afterPersist != nil {
			_ = r.afterPersist(destination)
		}
		return nil
	}
	if err := r.lanes.EnqueueRetry(ctx, destination, job); err != nil {
		_ = r.store.MarkDeliveryOperationAwaitingReplay(ctx, operation, time.Now().UTC())
		return fmt.Errorf("enqueue destination delivery: %w", err)
	}
	return nil
}

func (r *Router) handlePollAggregation(ctx context.Context, incoming transport.Incoming, canonicalID string, members []transport.EndpointID) error {
	optionHashes, err := r.store.GetPollOptions(ctx, canonicalID)
	if err != nil {
		return fmt.Errorf("get poll options: %w", err)
	}
	counts, err := r.store.GetPollVoteCounts(ctx, canonicalID)
	if err != nil {
		return fmt.Errorf("get poll vote counts: %w", err)
	}

	r.mu.RLock()
	meta, hasMeta := r.pollCache[canonicalID]
	r.mu.RUnlock()

	var totalVotes int
	for _, cnt := range counts {
		totalVotes += cnt
	}

	var sb strings.Builder
	if hasMeta && meta.Question != "" {
		sb.WriteString(fmt.Sprintf("📊 Aggregated Poll Results: %s\n\n", meta.Question))
	} else {
		sb.WriteString("📊 Aggregated Poll Results\n\n")
	}

	for i, optHash := range optionHashes {
		label := fmt.Sprintf("Option %d", i+1)
		if hasMeta && i < len(meta.Options) {
			label = meta.Options[i]
		}
		cnt := counts[optHash]
		pct := 0
		if totalVotes > 0 {
			pct = (cnt * 100) / totalVotes
		}
		sb.WriteString(fmt.Sprintf("• %s: %d vote(s) (%d%%)\n", label, cnt, pct))
	}
	sb.WriteString(fmt.Sprintf("\nTotal votes: %d", totalVotes))
	summaryText := sb.String()

	for _, destination := range members {
		var outgoingReplyTo *transport.MessageRef
		if targetCopy, err := r.store.MessageCopyForEndpoint(ctx, canonicalID, string(destination)); err == nil {
			outgoingReplyTo = &transport.MessageRef{
				Endpoint:        destination,
				RemoteMessageID: targetCopy.RemoteMessageID,
				IsTargetFromMe:  targetCopy.FromSelf,
			}
		}

		if _, err := r.sender.Send(ctx, transport.Outgoing{
			Endpoint: destination,
			Kind:     "text",
			Text:     summaryText,
			ReplyTo:  outgoingReplyTo,
		}); err != nil {
			return fmt.Errorf("send aggregated poll results: %w", err)
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
	r.mu.RLock()
	if canonicalID, ok := r.knownCopies[key]; ok {
		r.mu.RUnlock()
		return canonicalID, false, nil
	}
	r.mu.RUnlock()

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
	r.mu.Lock()
	r.knownCopies[key] = canonicalID
	r.mu.Unlock()
	return canonicalID, created, nil
}

func (r *Router) getUsernameMode() config.UsernameMode {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.usernameMode
}

func (r *Router) forwardedText(incoming transport.Incoming) (string, error) {
	username := incoming.Sender.OpaqueID
	if r.getUsernameMode() == config.UsernameModePushName {
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
	return fmt.Sprintf("*_%s/%s_*: %s", incoming.Endpoint, username, incoming.Text), nil
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
