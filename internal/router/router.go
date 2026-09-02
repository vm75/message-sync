package router

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
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

type pollPresentation struct {
	Question string
	Options  []string
}

type pollAggregate struct {
	Options []store.PollOption
	Counts  map[int]int
	Partial bool
}

type mutationKey struct {
	canonical string
	endpoint  transport.EndpointID
}

type pendingEdit struct {
	revision int64
	text     string
}

type pendingReaction struct {
	revision int64
	actor    string
	emoji    string
	fallback string
}

type Router struct {
	store              *store.Store
	sender             sender
	lanes              *delivery.Manager
	routes             map[transport.EndpointID][]transport.EndpointID
	usernameMode       config.UsernameMode
	aggTrigger         string
	knownCopies        map[copyKey]string
	pollPresentation   map[string]pollPresentation
	pendingEdits       map[mutationKey]pendingEdit
	pendingResultEdits map[mutationKey]pendingEdit
	pendingReactions   map[mutationKey]map[string]pendingReaction
	nextRevision       int64
	newCanonical       func() (string, error)
	afterPersist       func(transport.EndpointID) error
	mu                 sync.RWMutex
	pollResultMu       sync.Mutex
}

type Outcome struct {
	Accepted bool
	NoOp     bool
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
		store:              syncStore,
		sender:             transportSender,
		lanes:              lanes,
		routes:             routes,
		usernameMode:       cfg.Identity.UsernameMode,
		aggTrigger:         cfg.Polls.AggregationTrigger,
		knownCopies:        make(map[copyKey]string),
		pollPresentation:   make(map[string]pollPresentation),
		pendingEdits:       make(map[mutationKey]pendingEdit),
		pendingResultEdits: make(map[mutationKey]pendingEdit),
		pendingReactions:   make(map[mutationKey]map[string]pendingReaction),
		newCanonical:       newCanonicalID,
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

// DeliveryStatus returns the single content-free operational read model used
// by administration. The router joins its in-memory lanes with the SQLite
// ledger so live queue state and durable retry state cannot drift in the API.
func (r *Router) DeliveryStatus(ctx context.Context) ([]delivery.EndpointStatus, error) {
	if r == nil || r.store == nil || r.lanes == nil {
		return nil, errors.New("delivery status is unavailable")
	}
	now := time.Now().UTC()
	ledger, err := r.store.DeliverySummaries(ctx, now)
	if err != nil {
		return nil, err
	}
	lanes := r.lanes.Status()
	result := make([]delivery.EndpointStatus, 0, len(lanes))
	for endpoint, status := range lanes {
		if summary, ok := ledger[string(endpoint)]; ok {
			status.Queued = summary.Queued
			status.Retrying = summary.Retrying
			status.AwaitingReplay = summary.AwaitingReplay
			status.Failed = summary.Failed
			status.OldestActiveAge = summary.OldestActiveAge
			status.FailureClass = summary.FailureClass
			if summary.AwaitingReplay > 0 {
				status.LaneState = store.DeliveryAwaitingReplay
			} else if summary.Retrying > 0 {
				status.LaneState = store.DeliveryRetrying
			} else if summary.Queued > 0 {
				status.LaneState = store.DeliveryQueued
			} else if summary.Failed > 0 {
				status.LaneState = store.DeliveryFailed
			}
		}
		result = append(result, status)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].EndpointID < result[j].EndpointID })
	return result, nil
}

// HandleEvent processes live and recovered normalized events through the same
// canonical path and reports whether the accepted boundary was reached.
func (r *Router) HandleEvent(ctx context.Context, incoming transport.Incoming) (Outcome, error) {
	err := r.Handle(ctx, incoming)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Accepted: true, NoOp: incoming.Kind == "other"}, nil
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
	if incoming.Kind == "poll_snapshot" {
		canonicalID, err := r.store.PollCanonicalForProviderRef(ctx, string(incoming.Endpoint), incoming.PollProvider, incoming.PollProviderReference)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("resolve poll snapshot target: %w", err)
		}
		tombstoned, err := r.store.IsTombstoned(ctx, canonicalID)
		if err != nil {
			return fmt.Errorf("check poll snapshot tombstone: %w", err)
		}
		if tombstoned {
			return nil
		}
		if err := r.store.ReplacePollEndpointSnapshot(ctx, canonicalID, string(incoming.Endpoint), incoming.PollSnapshot, incoming.Timestamp); err != nil {
			return fmt.Errorf("record poll snapshot: %w", err)
		}
		r.updatePollResults(ctx, canonicalID, members)
		return nil
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

		var recordErr error
		if incoming.PollOptionIndexes != nil {
			recordErr = r.store.ReplacePollActorSelections(ctx, targetCanonical, string(incoming.Endpoint), incoming.Sender.OpaqueID, incoming.PollOptionIndexes, incoming.Timestamp)
		} else {
			recordErr = r.store.RecordPollVote(ctx, targetCanonical, string(incoming.Endpoint), incoming.Sender.OpaqueID, incoming.PollOptionHashes, incoming.Timestamp)
		}
		if recordErr != nil {
			return fmt.Errorf("record poll vote: %w", recordErr)
		}
		r.updatePollResults(ctx, targetCanonical, members)
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
			if err := r.enqueuePollResultCompanionDelete(ctx, targetCanonical, destination); err != nil {
				return fmt.Errorf("delete poll result companion: %w", err)
			}
			if destination == incoming.Endpoint {
				continue
			}
			targetCopy, err := r.store.MessageCopyForEndpoint(ctx, targetCanonical, string(destination))
			if err != nil {
				r.mu.Lock()
				delete(r.pendingEdits, mutationKey{canonical: targetCanonical, endpoint: destination})
				delete(r.pendingReactions, mutationKey{canonical: targetCanonical, endpoint: destination})
				r.mu.Unlock()
				continue
			}
			op := store.DeliveryOperation{CanonicalID: targetCanonical, EndpointID: string(destination), OperationKind: "delete", OperationRevision: r.nextMutationRevision(), State: store.DeliveryQueued, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
			if err := r.enqueueMutation(ctx, op, func() bool { return true }, func(jobCtx context.Context) error {
				return r.sender.Delete(jobCtx, transport.MessageRef{Endpoint: destination, RemoteMessageID: targetCopy.RemoteMessageID, IsTargetFromMe: targetCopy.FromSelf})
			}, nil); err != nil {
				return fmt.Errorf("send destination delete: %w", err)
			}
		}
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
				key := mutationKey{canonical: targetCanonical, endpoint: destination}
				r.mu.Lock()
				r.pendingEdits[key] = pendingEdit{revision: r.nextRevision + 1, text: forwardedText}
				r.nextRevision++
				r.mu.Unlock()
				continue
			}
			key := mutationKey{canonical: targetCanonical, endpoint: destination}
			revision := r.nextMutationRevision()
			r.mu.Lock()
			r.pendingEdits[key] = pendingEdit{revision: revision, text: forwardedText}
			r.mu.Unlock()
			op := store.DeliveryOperation{CanonicalID: targetCanonical, EndpointID: string(destination), OperationKind: "edit", OperationRevision: revision, State: store.DeliveryQueued, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
			if err := r.enqueueMutation(ctx, op, func() bool { return r.pendingEditCurrent(key, revision) }, func(jobCtx context.Context) error {
				return r.sender.Edit(jobCtx, transport.MessageRef{Endpoint: destination, RemoteMessageID: targetCopy.RemoteMessageID, IsTargetFromMe: targetCopy.FromSelf}, forwardedText)
			}, func() {
				r.mu.Lock()
				if current, ok := r.pendingEdits[key]; ok && current.revision == revision {
					delete(r.pendingEdits, key)
				}
				r.mu.Unlock()
			}); err != nil {
				return fmt.Errorf("send destination edit: %w", err)
			}
		}
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
			key := mutationKey{canonical: targetCanonical, endpoint: destination}
			revision := r.nextMutationRevision()
			pending := pendingReaction{revision: revision, actor: incoming.Sender.OpaqueID, emoji: emoji, fallback: fallbackText}
			if err != nil {
				r.mu.Lock()
				if r.pendingReactions[key] == nil {
					r.pendingReactions[key] = make(map[string]pendingReaction)
				}
				r.pendingReactions[key][pending.actor] = pending
				r.mu.Unlock()
				continue
			}
			r.mu.Lock()
			if r.pendingReactions[key] == nil {
				r.pendingReactions[key] = make(map[string]pendingReaction)
			}
			r.pendingReactions[key][pending.actor] = pending
			r.mu.Unlock()
			op := store.DeliveryOperation{CanonicalID: targetCanonical, EndpointID: string(destination), OperationKind: "reaction", OperationRevision: revision, State: store.DeliveryQueued, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
			if err := r.enqueueMutation(ctx, op, func() bool { return r.pendingReactionCurrent(key, pending.actor, revision) }, func(jobCtx context.Context) error {
				if err := r.sender.React(jobCtx, transport.Reaction{Endpoint: destination, TargetRemoteID: targetCopy.RemoteMessageID, IsTargetFromMe: targetCopy.FromSelf, Emoji: emoji, FallbackText: fallbackText}); err != nil {
					return err
				}
				return r.store.RecordSuppressedReaction(context.Background(), string(destination), targetCopy.RemoteMessageID, emoji, time.Now().UTC())
			}, func() {
				r.mu.Lock()
				if current, ok := r.pendingReactions[key][pending.actor]; ok && current.revision == revision {
					delete(r.pendingReactions[key], pending.actor)
				}
				r.mu.Unlock()
			}); err != nil {
				return fmt.Errorf("send reaction copy: %w", err)
			}
		}
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
			if incoming.PollProvider != "" && incoming.PollProviderReference != "" {
				if err := r.store.SavePollProviderRef(ctx, store.PollProviderRef{CanonicalID: canonicalID, EndpointID: string(incoming.Endpoint), Provider: incoming.PollProvider, Reference: incoming.PollProviderReference}); err != nil {
					return fmt.Errorf("save source poll provider reference: %w", err)
				}
				if incoming.PollSourceUnavailable {
					if err := r.store.MarkPollEndpointUnavailable(ctx, canonicalID, string(incoming.Endpoint)); err != nil {
						return fmt.Errorf("mark source poll unavailable: %w", err)
					}
				}
			}
		}
		r.mu.Lock()
		r.pollPresentation[canonicalID] = pollPresentation{
			Question: incoming.Text,
			Options:  incoming.PollOptions,
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
	if incoming.Kind == "poll" {
		r.updatePollResults(ctx, canonicalID, members)
	}
	return nil
}

func (r *Router) enqueuePollResultCompanionDelete(ctx context.Context, canonicalID string, endpoint transport.EndpointID) error {
	companion, err := r.store.PollResultCompanion(ctx, canonicalID, string(endpoint))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	operation := store.DeliveryOperation{
		CanonicalID: canonicalID, EndpointID: string(endpoint), OperationKind: "poll_result_delete",
		OperationRevision: r.nextMutationRevision(), State: store.DeliveryQueued,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	return r.enqueueMutation(ctx, operation, func() bool { return true }, func(jobCtx context.Context) error {
		if err := r.sender.Delete(jobCtx, transport.MessageRef{Endpoint: endpoint, RemoteMessageID: companion.RemoteMessageID, IsTargetFromMe: true}); err != nil {
			return err
		}
		return r.store.DeletePollResultCompanion(context.Background(), canonicalID, string(endpoint))
	}, nil)
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
	for _, kind := range []string{"primary", "companion"} {
		if kind == "companion" && !(len(mediaBytes) > 0 && (incoming.Kind == "audio" || incoming.Kind == "sticker")) {
			continue
		}
		if err := r.store.UpsertCreateStep(ctx, store.CreateStep{CanonicalID: canonicalID, EndpointID: string(destination), OperationRevision: 1, StepKind: kind, State: "pending", CreatedAt: now, UpdatedAt: now}); err != nil {
			return fmt.Errorf("queue create step: %w", err)
		}
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
		stateCtx := jobCtx
		if stateCtx.Err() != nil {
			stateCtx = context.Background()
		}
		if err := r.store.BeginDeliveryAttempt(stateCtx, operation, time.Now().UTC()); err != nil {
			return err
		}
		finishFailure := func(err error) error {
			failure := transport.Classify(err)
			// A provider request may have been accepted even when it returned an
			// error. The persisted step is the durable no-blind-retry boundary.
			if failure.Certainty == transport.SendUnknown {
				_ = r.store.MarkDeliveryOperationAwaitingReplay(context.Background(), operation, time.Now().UTC())
				return nil
			}
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
			step, stepErr := r.store.CreateStep(jobCtx, canonicalID, string(destination), 1, "companion")
			if stepErr != nil {
				return finishFailure(stepErr)
			}
			if step.State == "ambiguous" {
				return nil
			}
			if step.State != "complete" {
				if _, err := r.sender.Send(jobCtx, transport.Outgoing{
					Endpoint: destination, OriginEndpoint: incoming.Endpoint, Sender: incoming.Sender,
					SourceText: incoming.Text, AttributionOnly: true,
					ReplyFallback: incoming.ReplyTo != nil && replyTo == nil, Kind: "text", Text: forwardedText,
					ReplyTo: replyTo, QuotedText: incoming.QuotedText,
				}); err != nil {
					if transport.Classify(err).Certainty == transport.SendUnknown {
						_ = r.store.CompleteCreateStep(context.Background(), step, "", true, time.Now().UTC())
					}
					return finishFailure(err)
				}
				if err := r.store.CompleteCreateStep(jobCtx, step, "", false, time.Now().UTC()); err != nil {
					return finishFailure(err)
				}
			}
		}
		primary, stepErr := r.store.CreateStep(jobCtx, canonicalID, string(destination), 1, "primary")
		if stepErr != nil {
			return finishFailure(stepErr)
		}
		var ref transport.MessageRef
		if primary.State == "ambiguous" {
			return nil
		}
		if primary.State == "complete" {
			ref = transport.MessageRef{Endpoint: destination, RemoteMessageID: primary.RemoteMessageID, IsTargetFromMe: true}
		} else {
			ref, err = r.sender.Send(jobCtx, transport.Outgoing{
				Endpoint: destination, OriginEndpoint: incoming.Endpoint, Sender: incoming.Sender,
				SourceText: incoming.Text, ReplyFallback: incoming.ReplyTo != nil && replyTo == nil,
				Kind: incoming.Kind, Text: forwardedText, Mentions: incoming.Mentions,
				MediaBytes: mediaBytes, ReplyTo: replyTo, QuotedText: incoming.QuotedText,
				PollOptions: incoming.PollOptions, PollSelectableCount: incoming.PollSelectableCount,
				PollDurationHours: incoming.PollDurationHours,
			})
			if err != nil {
				if transport.Classify(err).Certainty == transport.SendUnknown {
					_ = r.store.CompleteCreateStep(context.Background(), primary, "", true, time.Now().UTC())
				}
				return finishFailure(err)
			}
			if ref.Endpoint != destination || strings.TrimSpace(ref.RemoteMessageID) == "" {
				return finishFailure(errors.New("transport returned invalid destination message reference"))
			}
			if primary.State != "complete" {
				if err := r.store.CompleteCreateStep(jobCtx, primary, ref.RemoteMessageID, false, time.Now().UTC()); err != nil {
					return finishFailure(err)
				}
			}
		}
		tombstoned, err = r.store.IsTombstoned(jobCtx, canonicalID)
		if err != nil {
			return finishFailure(err)
		}
		if tombstoned {
			_ = r.sender.Delete(jobCtx, ref)
			_ = r.store.DeleteDeliveryOperation(context.Background(), operation)
			return nil
		}
		if err := r.store.AddMessageCopy(jobCtx, store.MessageCopy{
			CanonicalID: canonicalID, EndpointID: string(destination), RemoteMessageID: ref.RemoteMessageID,
			CreatedAt: time.Now().UTC(), FromSelf: true,
		}); err != nil {
			return finishFailure(err)
		}
		if incoming.Kind == "poll" && ref.Provider != "" && ref.ProviderReference != "" {
			if err := r.store.SavePollProviderRef(jobCtx, store.PollProviderRef{CanonicalID: canonicalID, EndpointID: string(destination), Provider: ref.Provider, Reference: ref.ProviderReference}); err != nil {
				return finishFailure(err)
			}
		}
		r.mu.Lock()
		r.knownCopies[copyKey{endpoint: destination, remoteID: ref.RemoteMessageID}] = canonicalID
		r.mu.Unlock()
		if incoming.Kind == "poll" {
			_ = r.ensurePollResultCompanion(jobCtx, canonicalID, destination)
		}
		_ = r.store.DeleteDeliveryOperation(jobCtx, operation)
		if r.afterPersist != nil {
			_ = r.afterPersist(destination)
		}
		r.flushPendingMutations(context.Background(), canonicalID, destination)
		return nil
	}
	if err := r.lanes.EnqueueRetry(ctx, destination, job); err != nil {
		_ = r.store.MarkDeliveryOperationAwaitingReplay(ctx, operation, time.Now().UTC())
		return fmt.Errorf("enqueue destination delivery: %w", err)
	}
	return nil
}

func (r *Router) renderPollResults(ctx context.Context, canonicalID string) (string, error) {
	aggregate, err := r.readPollAggregate(ctx, canonicalID)
	if err != nil {
		return "", err
	}
	r.mu.RLock()
	presentation, hasPresentation := r.pollPresentation[canonicalID]
	r.mu.RUnlock()

	var builder strings.Builder
	builder.WriteString("***Aggregated anonymised live results***\n")
	if hasPresentation && presentation.Question != "" {
		builder.WriteString(presentation.Question)
		builder.WriteString("\n")
	}
	if aggregate.Partial {
		builder.WriteString("Partial/unavailable source\n")
	}
	for _, option := range aggregate.Options {
		label := fmt.Sprintf("Option %d", option.Index+1)
		if hasPresentation && option.Index < len(presentation.Options) && presentation.Options[option.Index] != "" {
			label = presentation.Options[option.Index]
		}
		fmt.Fprintf(&builder, "%s — %d\n", label, aggregate.Counts[option.Index])
	}
	return strings.TrimSuffix(builder.String(), "\n"), nil
}

func (r *Router) readPollAggregate(ctx context.Context, canonicalID string) (pollAggregate, error) {
	options, err := r.store.GetPollOptionMetadata(ctx, canonicalID)
	if err != nil {
		return pollAggregate{}, err
	}
	counts, err := r.store.GetPollAggregateCounts(ctx, canonicalID)
	if err != nil {
		return pollAggregate{}, err
	}
	partial, err := r.store.HasUnavailablePollEndpoint(ctx, canonicalID)
	if err != nil {
		return pollAggregate{}, err
	}
	return pollAggregate{Options: options, Counts: counts, Partial: partial}, nil
}

func (r *Router) ensurePollResultCompanion(ctx context.Context, canonicalID string, endpoint transport.EndpointID) error {
	r.pollResultMu.Lock()
	defer r.pollResultMu.Unlock()
	if _, err := r.store.PollResultCompanion(ctx, canonicalID, string(endpoint)); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	text, err := r.renderPollResults(ctx, canonicalID)
	if err != nil {
		return err
	}
	ref, err := r.sender.Send(ctx, transport.Outgoing{Endpoint: endpoint, Kind: "text", Text: text})
	if err != nil {
		return err
	}
	if ref.Endpoint != endpoint || strings.TrimSpace(ref.RemoteMessageID) == "" {
		return errors.New("poll result transport returned invalid message reference")
	}
	return r.store.SavePollResultCompanion(ctx, store.PollResultCompanion{CanonicalID: canonicalID, EndpointID: string(endpoint), RemoteMessageID: ref.RemoteMessageID})
}

func (r *Router) updatePollResults(ctx context.Context, canonicalID string, members []transport.EndpointID) {
	if tombstoned, err := r.store.IsTombstoned(ctx, canonicalID); err != nil || tombstoned {
		return
	}
	text, err := r.renderPollResults(ctx, canonicalID)
	if err != nil {
		return
	}
	for _, endpoint := range members {
		companion, err := r.store.PollResultCompanion(ctx, canonicalID, string(endpoint))
		if errors.Is(err, sql.ErrNoRows) {
			if _, copyErr := r.store.MessageCopyForEndpoint(ctx, canonicalID, string(endpoint)); copyErr != nil {
				continue
			}
			if createErr := r.ensurePollResultCompanion(ctx, canonicalID, endpoint); createErr != nil {
				continue
			}
			companion, err = r.store.PollResultCompanion(ctx, canonicalID, string(endpoint))
		}
		if err != nil {
			continue
		}
		key := mutationKey{canonical: canonicalID, endpoint: endpoint}
		revision := r.nextMutationRevision()
		r.mu.Lock()
		r.pendingResultEdits[key] = pendingEdit{revision: revision, text: text}
		r.mu.Unlock()
		op := store.DeliveryOperation{CanonicalID: canonicalID, EndpointID: string(endpoint), OperationKind: "poll_result_edit", OperationRevision: revision, State: store.DeliveryQueued, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		_ = r.enqueueMutation(ctx, op, func() bool { return r.pendingResultEditCurrent(key, revision) }, func(jobCtx context.Context) error {
			return r.sender.Edit(jobCtx, transport.MessageRef{Endpoint: endpoint, RemoteMessageID: companion.RemoteMessageID, IsTargetFromMe: true}, text)
		}, func() {
			r.mu.Lock()
			if current, ok := r.pendingResultEdits[key]; ok && current.revision == revision {
				delete(r.pendingResultEdits, key)
			}
			r.mu.Unlock()
		})
	}
}

func (r *Router) pendingResultEditCurrent(key mutationKey, revision int64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	edit, ok := r.pendingResultEdits[key]
	return ok && edit.revision == revision
}

func (r *Router) enqueueMutation(ctx context.Context, operation store.DeliveryOperation, current func() bool, execute func(context.Context) error, succeeded func()) error {
	if err := r.store.UpsertDeliveryOperation(ctx, operation); err != nil {
		return fmt.Errorf("queue delivery operation: %w", err)
	}
	job := func(jobCtx context.Context, attempt int) error {
		if !current() {
			_ = r.store.DeleteDeliveryOperation(context.Background(), operation)
			return nil
		}
		stateCtx := jobCtx
		if stateCtx.Err() != nil {
			stateCtx = context.Background()
		}
		if err := r.store.BeginDeliveryAttempt(stateCtx, operation, time.Now().UTC()); err != nil {
			return err
		}
		if err := execute(jobCtx); err != nil {
			failure := transport.Classify(err)
			if !failure.Retryable {
				_ = r.store.MarkDeliveryOperationFailed(context.Background(), operation, string(failure.Class), time.Now().UTC())
				return nil
			}
			if attempt >= delivery.DefaultRetryPolicy().MaxAttempts || jobCtx.Err() != nil {
				_ = r.store.MarkDeliveryOperationAwaitingReplay(context.Background(), operation, time.Now().UTC())
				return nil
			}
			return err
		}
		_ = r.store.DeleteDeliveryOperation(context.Background(), operation)
		if succeeded != nil {
			succeeded()
		}
		return nil
	}
	if err := r.lanes.EnqueueRetry(ctx, transport.EndpointID(operation.EndpointID), job); err != nil {
		_ = r.store.MarkDeliveryOperationAwaitingReplay(context.Background(), operation, time.Now().UTC())
		return fmt.Errorf("enqueue destination mutation: %w", err)
	}
	return nil
}

func (r *Router) nextMutationRevision() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextRevision++
	return r.nextRevision
}

func (r *Router) flushPendingMutations(ctx context.Context, canonicalID string, destination transport.EndpointID) {
	key := mutationKey{canonical: canonicalID, endpoint: destination}
	r.mu.Lock()
	edit, hasEdit := r.pendingEdits[key]
	reactions := r.pendingReactions[key]
	r.mu.Unlock()
	if hasEdit {
		copy, err := r.store.MessageCopyForEndpoint(ctx, canonicalID, string(destination))
		if err == nil {
			op := store.DeliveryOperation{CanonicalID: canonicalID, EndpointID: string(destination), OperationKind: "edit", OperationRevision: edit.revision, State: store.DeliveryQueued, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
			_ = r.enqueueMutation(ctx, op, func() bool { return r.pendingEditCurrent(key, edit.revision) }, func(jobCtx context.Context) error {
				return r.sender.Edit(jobCtx, transport.MessageRef{Endpoint: destination, RemoteMessageID: copy.RemoteMessageID, IsTargetFromMe: copy.FromSelf}, edit.text)
			}, func() {
				r.mu.Lock()
				if current, ok := r.pendingEdits[key]; ok && current.revision == edit.revision {
					delete(r.pendingEdits, key)
				}
				r.mu.Unlock()
			})
		}
	}
	for _, reaction := range reactions {
		copy, err := r.store.MessageCopyForEndpoint(ctx, canonicalID, string(destination))
		if err != nil {
			continue
		}
		op := store.DeliveryOperation{CanonicalID: canonicalID, EndpointID: string(destination), OperationKind: "reaction", OperationRevision: reaction.revision, State: store.DeliveryQueued, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		current := reaction
		_ = r.enqueueMutation(ctx, op, func() bool { return r.pendingReactionCurrent(key, current.actor, current.revision) }, func(jobCtx context.Context) error {
			err := r.sender.React(jobCtx, transport.Reaction{Endpoint: destination, TargetRemoteID: copy.RemoteMessageID, IsTargetFromMe: copy.FromSelf, Emoji: current.emoji, FallbackText: current.fallback})
			if err == nil {
				err = r.store.RecordSuppressedReaction(context.Background(), string(destination), copy.RemoteMessageID, current.emoji, time.Now().UTC())
			}
			return err
		}, func() {
			r.mu.Lock()
			if pending, ok := r.pendingReactions[key][current.actor]; ok && pending.revision == current.revision {
				delete(r.pendingReactions[key], current.actor)
			}
			r.mu.Unlock()
		})
	}
}

func (r *Router) pendingEditCurrent(key mutationKey, revision int64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	edit, ok := r.pendingEdits[key]
	return ok && edit.revision == revision
}

func (r *Router) pendingReactionCurrent(key mutationKey, actor string, revision int64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reaction, ok := r.pendingReactions[key][actor]
	return ok && reaction.revision == revision
}

func (r *Router) handlePollAggregation(ctx context.Context, incoming transport.Incoming, canonicalID string, members []transport.EndpointID) error {
	aggregate, err := r.readPollAggregate(ctx, canonicalID)
	if err != nil {
		return fmt.Errorf("get poll aggregate: %w", err)
	}

	r.mu.RLock()
	meta, hasMeta := r.pollPresentation[canonicalID]
	r.mu.RUnlock()

	var totalVotes int
	for _, cnt := range aggregate.Counts {
		totalVotes += cnt
	}

	var sb strings.Builder
	if hasMeta && meta.Question != "" {
		sb.WriteString(fmt.Sprintf("📊 Aggregated Poll Results: %s\n\n", meta.Question))
	} else {
		sb.WriteString("📊 Aggregated Poll Results\n\n")
	}

	for i, option := range aggregate.Options {
		label := fmt.Sprintf("Option %d", i+1)
		if hasMeta && i < len(meta.Options) {
			label = meta.Options[i]
		}
		cnt := aggregate.Counts[option.Index]
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
