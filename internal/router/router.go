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
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/transport"
)

type sender interface {
	Send(context.Context, transport.Outgoing) (transport.MessageRef, error)
	React(context.Context, transport.Reaction) error
	Edit(context.Context, transport.MessageRef, string) error
	Delete(context.Context, transport.MessageRef) error
}

type renderedEditor interface {
	EditRendered(context.Context, transport.MessageRef, string) error
}

// PollPresentationStore persists the human-readable part of a poll outside
// the privacy-preserving routing database.
type PollPresentationStore interface {
	SavePollPresentation(context.Context, string, string, []string) error
	LoadPollPresentation(context.Context, string) (string, []string, error)
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
	childContextMode   config.ChildContextDisplayMode
	aggTrigger         string
	localPrefix        string
	knownCopies        map[copyKey]string
	pollPresentation   map[string]pollPresentation
	pollAggregations   map[string][]transport.MessageRef
	pendingEdits       map[mutationKey]pendingEdit
	pendingResultEdits map[mutationKey]pendingEdit
	pendingReactions   map[mutationKey]map[string]pendingReaction
	nextRevision       int64
	newCanonical       func() (string, error)
	afterPersist       func(transport.EndpointID) error
	mu                 sync.RWMutex
	pollResultMu       sync.Mutex
	scopeHasher        *identity.Hasher
	presentationStore  PollPresentationStore
}

type Outcome struct {
	Accepted      bool
	NoOp          bool
	SafeToAdvance bool
	Pending       bool
	Ambiguous     bool
}

func New(cfg *config.Config, syncStore *store.Store, transportSender sender) (*Router, error) {
	return newRouter(cfg, syncStore, transportSender, nil, nil)
}

func NewWithHasher(cfg *config.Config, syncStore *store.Store, transportSender sender, hasher *identity.Hasher) (*Router, error) {
	if hasher == nil {
		return nil, errors.New("identity hasher is required")
	}
	return newRouter(cfg, syncStore, transportSender, hasher, nil)
}

func NewWithHasherAndPollPresentationStore(cfg *config.Config, syncStore *store.Store, transportSender sender, hasher *identity.Hasher, presentationStore PollPresentationStore) (*Router, error) {
	if hasher == nil {
		return nil, errors.New("identity hasher is required")
	}
	return newRouter(cfg, syncStore, transportSender, hasher, presentationStore)
}

func newRouter(cfg *config.Config, syncStore *store.Store, transportSender sender, hasher *identity.Hasher, presentationStore PollPresentationStore) (*Router, error) {
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
		childContextMode:   cfg.ChildContextDisplayMode,
		aggTrigger:         cfg.Polls.AggregationTrigger,
		localPrefix:        cfg.LocalPrefix,
		knownCopies:        make(map[copyKey]string),
		pollPresentation:   make(map[string]pollPresentation),
		pollAggregations:   make(map[string][]transport.MessageRef),
		pendingEdits:       make(map[mutationKey]pendingEdit),
		pendingResultEdits: make(map[mutationKey]pendingEdit),
		pendingReactions:   make(map[mutationKey]map[string]pendingReaction),
		newCanonical:       newCanonicalID,
		scopeHasher:        hasher,
		presentationStore:  presentationStore,
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
	r.childContextMode = cfg.ChildContextDisplayMode
	r.aggTrigger = cfg.Polls.AggregationTrigger
	r.localPrefix = cfg.LocalPrefix
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
			} else {
				// Failed operations are terminal history, not active lane work.
				// Keep the lane healthy when the transport has no pending work;
				// the failure count remains available in the ledger summary.
				status.LaneState = "healthy"
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
	if incoming.Kind == "other" {
		return Outcome{Accepted: true, NoOp: true, SafeToAdvance: true}, nil
	}
	pending, err := r.store.PendingDeliveryForRemote(ctx, string(incoming.Endpoint), incoming.RemoteID)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Accepted: true, SafeToAdvance: !pending, Pending: pending}, nil
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
	// Existing copies, including bridge echoes, must continue through the
	// normal idempotence/lifecycle path and must not be newly classified local.
	if _, err := r.store.CanonicalForRemote(ctx, string(incoming.Endpoint), incoming.RemoteID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check existing canonical message: %w", err)
	}
	if suppressed, err := r.store.IsSuppressedLocalMessage(ctx, string(incoming.Endpoint), incoming.RemoteID); err != nil {
		return fmt.Errorf("check local message suppression: %w", err)
	} else if suppressed {
		return nil
	}
	r.mu.RLock()
	localPrefix := r.localPrefix
	r.mu.RUnlock()
	if incoming.Kind != "edit" && incoming.Kind != "delete" && incoming.Kind != "revoke" && incoming.Kind != "reaction" && matchLocalPrefix(incoming.Text, localPrefix) {
		if err := r.store.SuppressedLocalMessage(ctx, string(incoming.Endpoint), incoming.RemoteID, incoming.Timestamp); err != nil {
			return fmt.Errorf("suppress local message: %w", err)
		}
		return nil
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

	r.mu.RLock()
	aggTrigger := r.aggTrigger
	r.mu.RUnlock()

	if matchAggregationTrigger(incoming.Text, aggTrigger) && incoming.ReplyTo != nil && incoming.ReplyTo.RemoteMessageID != "" {
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
		r.mu.Lock()
		delete(r.pollAggregations, targetCanonical)
		for key := range r.pendingResultEdits {
			if strings.HasPrefix(key.canonical, targetCanonical+"\x00") {
				delete(r.pendingResultEdits, key)
			}
		}
		r.mu.Unlock()

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
				ref := r.destinationMessageRef(jobCtx, targetCanonical, destination, targetCopy)
				return r.sender.Delete(jobCtx, ref)
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

		forwardedText, err := r.forwardedText(ctx, incoming)
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
				ref := r.destinationMessageRef(jobCtx, targetCanonical, destination, targetCopy)
				return r.edit(jobCtx, ref, forwardedText)
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

		username := senderPresentationUsername(incoming.Sender, r.getUsernameMode())
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
				ref := r.destinationMessageRef(jobCtx, targetCanonical, destination, targetCopy)
				if err := r.sender.React(jobCtx, transport.Reaction{Endpoint: destination, TargetRemoteID: ref.RemoteMessageID, IsTargetFromMe: ref.IsTargetFromMe, ChildScope: ref.ChildScope, Emoji: emoji, FallbackText: fallbackText}); err != nil {
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
	// A self-origin poll may already have a canonical copy after a restart.
	// Refresh the transient presentation before the idempotence return so live
	// result companions retain the question and option labels.
	if incoming.Kind == "poll" {
		if err := r.rememberPollPresentation(ctx, canonicalID, incoming); err != nil {
			return fmt.Errorf("persist poll presentation: %w", err)
		}
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
		if err := r.rememberPollPresentation(ctx, canonicalID, incoming); err != nil {
			return fmt.Errorf("persist poll presentation: %w", err)
		}
	}

	forwardedText, err := r.forwardedText(ctx, incoming)
	if err != nil {
		return err
	}

	var replyToCanonical string
	if incoming.ReplyTo != nil && incoming.ReplyTo.RemoteMessageID != "" {
		rc, err := r.store.CanonicalForRemote(ctx, string(incoming.Endpoint), incoming.ReplyTo.RemoteMessageID)
		if err == nil {
			replyToCanonical = rc
		} else if errors.Is(err, sql.ErrNoRows) {
			// A local-only source has no canonical copy. Its quoted payload is
			// transient ingress data and must not become a fallback attribution
			// on a bridged reply.
			suppressed, suppressionErr := r.store.IsSuppressedLocalMessage(ctx, string(incoming.Endpoint), incoming.ReplyTo.RemoteMessageID)
			if suppressionErr != nil {
				return fmt.Errorf("check suppressed reply target: %w", suppressionErr)
			}
			if suppressed {
				incoming.QuotedText = ""
			}
		}
	}
	// A reply carries the complete child-scope lineage of its target. The
	// source endpoint's live scope, when supplied, wins over the inherited
	// value; other endpoint scopes remain independent.
	if replyToCanonical != "" {
		inherited, err := r.store.CanonicalScopes(ctx, replyToCanonical)
		if err != nil {
			return fmt.Errorf("load replied-to child scopes: %w", err)
		}
		for _, scope := range inherited {
			if scope.EndpointID == string(incoming.Endpoint) && incoming.ChildScope != nil {
				continue
			}
			if err := r.store.UpsertCanonicalScope(ctx, store.CanonicalScope{
				CanonicalID: canonicalID, EndpointID: scope.EndpointID, ScopeKind: scope.ScopeKind, RemoteScopeID: scope.RemoteScopeID, CreatedAt: scope.CreatedAt,
			}); err != nil {
				return fmt.Errorf("inherit child scope: %w", err)
			}
		}
	}
	if incoming.ChildScope != nil && strings.TrimSpace(incoming.ChildScope.RemoteID) != "" {
		if err := r.store.UpsertCanonicalScope(ctx, store.CanonicalScope{
			CanonicalID: canonicalID, EndpointID: string(incoming.Endpoint), ScopeKind: string(incoming.ChildScope.Kind), RemoteScopeID: incoming.ChildScope.RemoteID, CreatedAt: incoming.Timestamp,
		}); err != nil {
			return fmt.Errorf("persist incoming child scope: %w", err)
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
		var childScope *transport.ChildScope
		if scope, err := r.store.CanonicalScope(ctx, canonicalID, string(destination)); err == nil {
			childScope = &transport.ChildScope{Kind: transport.ScopeKind(scope.ScopeKind), RemoteID: scope.RemoteScopeID}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("look up destination child scope: %w", err)
		}
		if outgoingReplyTo != nil {
			if scope, err := r.store.CanonicalScope(ctx, replyToCanonical, string(destination)); err == nil {
				outgoingReplyTo.ChildScope = &transport.ChildScope{Kind: transport.ScopeKind(scope.ScopeKind), RemoteID: scope.RemoteScopeID}
			} else if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("look up reply child scope: %w", err)
			}
		}
		destinationText := forwardedText
		if scopes, err := r.store.CanonicalScopes(ctx, canonicalID); err == nil {
			destinationText = r.withChildContextHeaders(incoming, destination, scopes, forwardedText)
		} else {
			return fmt.Errorf("load presentation child scopes: %w", err)
		}

		if err := r.enqueueCreate(ctx, canonicalID, incoming, destination, destinationText, outgoingReplyTo, childScope, mediaBytes); err != nil {
			return err
		}
	}
	if incoming.Kind == "poll" {
		r.updatePollResults(ctx, canonicalID, members)
	}
	return nil
}

func (r *Router) rememberPollPresentation(ctx context.Context, canonicalID string, incoming transport.Incoming) error {
	if strings.TrimSpace(incoming.Text) == "" || len(incoming.PollOptions) == 0 {
		return nil
	}
	presentation := pollPresentation{Question: incoming.Text, Options: append([]string(nil), incoming.PollOptions...)}
	r.mu.Lock()
	r.pollPresentation[canonicalID] = presentation
	r.mu.Unlock()
	if r.presentationStore != nil {
		if err := r.presentationStore.SavePollPresentation(ctx, canonicalID, presentation.Question, presentation.Options); err != nil {
			return err
		}
	}
	return nil
}

func (r *Router) edit(ctx context.Context, ref transport.MessageRef, text string) error {
	if r.getChildContextMode() == config.ChildContextDisplayFriendly {
		if editor, ok := r.sender.(renderedEditor); ok {
			return editor.EditRendered(ctx, ref, text)
		}
	}
	return r.sender.Edit(ctx, ref, text)
}

func (r *Router) destinationMessageRef(ctx context.Context, canonicalID string, endpoint transport.EndpointID, copy store.MessageCopy) transport.MessageRef {
	ref := transport.MessageRef{Endpoint: endpoint, RemoteMessageID: copy.RemoteMessageID, IsTargetFromMe: copy.FromSelf}
	ref.ChildScope = r.childScope(ctx, canonicalID, endpoint)
	return ref
}

func (r *Router) childScope(ctx context.Context, canonicalID string, endpoint transport.EndpointID) *transport.ChildScope {
	scope, err := r.store.CanonicalScope(ctx, canonicalID, string(endpoint))
	if err != nil {
		return nil
	}
	return &transport.ChildScope{Kind: transport.ScopeKind(scope.ScopeKind), RemoteID: scope.RemoteScopeID}
}

func (r *Router) companionMessageRef(ctx context.Context, canonicalID string, endpoint transport.EndpointID, remoteID string) transport.MessageRef {
	return transport.MessageRef{Endpoint: endpoint, RemoteMessageID: remoteID, IsTargetFromMe: true, ChildScope: r.childScope(ctx, canonicalID, endpoint)}
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
		if err := r.sender.Delete(jobCtx, r.companionMessageRef(jobCtx, canonicalID, endpoint, companion.RemoteMessageID)); err != nil {
			return err
		}
		return r.store.DeletePollResultCompanion(context.Background(), canonicalID, string(endpoint))
	}, nil)
}

func (r *Router) enqueueCreate(ctx context.Context, canonicalID string, incoming transport.Incoming, destination transport.EndpointID, forwardedText string, replyTo *transport.MessageRef, childScope *transport.ChildScope, mediaBytes []byte) error {
	senderLabel, err := r.senderLabel(ctx, incoming)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	operation := store.DeliveryOperation{
		CanonicalID: canonicalID, EndpointID: string(destination), OperationKind: "create", OperationRevision: 1,
		State: store.DeliveryQueued, CreatedAt: now, UpdatedAt: now,
	}
	if existing, err := r.store.DeliveryOperation(ctx, operation); err == nil && (existing.State == store.DeliveryQueued || existing.State == store.DeliveryRetrying) {
		return nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("look up delivery operation: %w", err)
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
					SenderLabel: senderLabel,
					SourceText:  incoming.Text, AttributionOnly: true,
					ReplyFallback: incoming.ReplyTo != nil && replyTo == nil, Kind: "text", Text: forwardedText,
					RenderedText:    childAwareRenderedText(r.getChildContextMode(), incoming.ChildScope, forwardedText),
					PollAttribution: r.childAwarePollAttribution(jobCtx, r.getChildContextMode(), incoming, incoming.ChildScope),
					ReplyTo:         replyTo, ChildScope: childScope, QuotedText: incoming.QuotedText,
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
				SenderLabel: senderLabel,
				SourceText:  incoming.Text, ReplyFallback: incoming.ReplyTo != nil && replyTo == nil,
				Kind: incoming.Kind, Text: forwardedText, Mentions: incoming.Mentions,
				RenderedText:    childAwareRenderedText(r.getChildContextMode(), incoming.ChildScope, forwardedText),
				PollAttribution: r.childAwarePollAttribution(jobCtx, r.getChildContextMode(), incoming, incoming.ChildScope),
				MediaBytes:      mediaBytes, ReplyTo: replyTo, ChildScope: childScope, QuotedText: incoming.QuotedText,
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

func childAwareRenderedText(mode config.ChildContextDisplayMode, childScope *transport.ChildScope, text string) string {
	// Preserve legacy adapter rendering for opaque root messages. A child source
	// must use the router-rendered text so group/child/user survives all adapters.
	if mode == config.ChildContextDisplayFriendly || childScope != nil {
		return text
	}
	return ""
}

func (r *Router) childAwarePollAttribution(ctx context.Context, mode config.ChildContextDisplayMode, incoming transport.Incoming, childScope *transport.ChildScope) string {
	// Preserve legacy opaque root-poll behavior. Child polls always carry the
	// same source child attribution as ordinary child messages.
	if mode != config.ChildContextDisplayFriendly && childScope == nil {
		return ""
	}
	label, err := r.senderLabel(ctx, incoming)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("*_%s_*:", label)
}

func (r *Router) renderPollResults(ctx context.Context, canonicalID string) (string, error) {
	aggregate, err := r.readPollAggregate(ctx, canonicalID)
	if err != nil {
		return "", err
	}
	r.mu.RLock()
	presentation, hasPresentation := r.pollPresentation[canonicalID]
	r.mu.RUnlock()
	if !hasPresentation && r.presentationStore != nil {
		question, options, err := r.presentationStore.LoadPollPresentation(ctx, canonicalID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("load poll presentation: %w", err)
		}
		if err == nil && strings.TrimSpace(question) != "" && len(options) > 0 {
			presentation = pollPresentation{Question: question, Options: options}
			hasPresentation = true
			r.mu.Lock()
			r.pollPresentation[canonicalID] = presentation
			r.mu.Unlock()
		}
	}

	var builder strings.Builder
	builder.WriteString("📊 ***LIVE POLL RESULTS ACROSS ALL GROUPS***\n❓ ")
	if hasPresentation && presentation.Question != "" {
		builder.WriteString(presentation.Question)
	}
	builder.WriteString("\n\n**Options**")
	for _, option := range aggregate.Options {
		label := fmt.Sprintf("Option %d", option.Index+1)
		if hasPresentation && option.Index < len(presentation.Options) && presentation.Options[option.Index] != "" {
			label = presentation.Options[option.Index]
		}
		fmt.Fprintf(&builder, "\n○ *%s* — %d votes", label, aggregate.Counts[option.Index])
	}
	return builder.String(), nil
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
	ref, err := r.sender.Send(ctx, transport.Outgoing{Endpoint: endpoint, Kind: "text", Text: text, ChildScope: r.childScope(ctx, canonicalID, endpoint)})
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
			return r.sender.Edit(jobCtx, r.companionMessageRef(jobCtx, canonicalID, endpoint, companion.RemoteMessageID), text)
		}, func() {
			r.mu.Lock()
			if current, ok := r.pendingResultEdits[key]; ok && current.revision == revision {
				delete(r.pendingResultEdits, key)
			}
			r.mu.Unlock()
		})
	}
	r.updatePollAggregations(ctx, canonicalID, text)
}

func (r *Router) updatePollAggregations(ctx context.Context, canonicalID, text string) {
	r.mu.RLock()
	results := append([]transport.MessageRef(nil), r.pollAggregations[canonicalID]...)
	r.mu.RUnlock()
	for _, ref := range results {
		key := mutationKey{canonical: canonicalID + "\x00" + ref.RemoteMessageID, endpoint: ref.Endpoint}
		revision := r.nextMutationRevision()
		r.mu.Lock()
		r.pendingResultEdits[key] = pendingEdit{revision: revision, text: text}
		r.mu.Unlock()
		op := store.DeliveryOperation{CanonicalID: canonicalID, EndpointID: string(ref.Endpoint), OperationKind: "poll_aggregation_edit", OperationRevision: revision, State: store.DeliveryQueued, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		_ = r.enqueueMutation(ctx, op, func() bool { return r.pendingResultEditCurrent(key, revision) }, func(jobCtx context.Context) error {
			return r.sender.Edit(jobCtx, ref, text)
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
				return r.edit(jobCtx, transport.MessageRef{Endpoint: destination, RemoteMessageID: copy.RemoteMessageID, IsTargetFromMe: copy.FromSelf}, edit.text)
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
	summaryText, err := r.renderPollResults(ctx, canonicalID)
	if err != nil {
		return fmt.Errorf("render aggregated poll results: %w", err)
	}

	for _, destination := range members {
		var outgoingReplyTo *transport.MessageRef
		if targetCopy, err := r.store.MessageCopyForEndpoint(ctx, canonicalID, string(destination)); err == nil {
			outgoingReplyTo = &transport.MessageRef{
				Endpoint:        destination,
				RemoteMessageID: targetCopy.RemoteMessageID,
				IsTargetFromMe:  targetCopy.FromSelf,
			}
			if scope := r.childScope(ctx, canonicalID, destination); scope != nil {
				outgoingReplyTo.ChildScope = scope
			}
		}

		ref, err := r.sender.Send(ctx, transport.Outgoing{
			Endpoint: destination,
			Kind:     "text",
			Text:     summaryText,
			ReplyTo:  outgoingReplyTo, ChildScope: r.childScope(ctx, canonicalID, destination),
		})
		if err != nil {
			return fmt.Errorf("send aggregated poll results: %w", err)
		}
		if ref.Endpoint != destination || strings.TrimSpace(ref.RemoteMessageID) == "" {
			return errors.New("aggregated poll result transport returned invalid message reference")
		}
		ref.ChildScope = r.childScope(ctx, canonicalID, destination)
		r.mu.Lock()
		r.pollAggregations[canonicalID] = append(r.pollAggregations[canonicalID], ref)
		r.mu.Unlock()
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

func (r *Router) forwardedText(ctx context.Context, incoming transport.Incoming) (string, error) {
	label, err := r.senderLabel(ctx, incoming)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("*_%s_*: %s", label, incoming.Text), nil
}

func (r *Router) senderLabel(ctx context.Context, incoming transport.Incoming) (string, error) {
	username := senderPresentationUsername(incoming.Sender, r.getUsernameMode())
	if strings.TrimSpace(username) == "" {
		return "", errors.New("incoming sender identity is required")
	}
	prefix := string(incoming.Endpoint)
	if incoming.ChildScope != nil {
		label := normalizeDisplayName(incoming.ChildScope.Label)
		// childContextDisplayMode controls label persistence, not visible syntax.
		// Opaque mode may use a live label transiently but must not store it.
		if label == "" && r.getChildContextMode() == config.ChildContextDisplayFriendly {
			if stored, err := r.store.ChildScopeLabel(ctx, string(incoming.Endpoint), string(incoming.ChildScope.Kind), incoming.ChildScope.RemoteID); err == nil {
				label = normalizeDisplayName(stored.DisplayName)
			}
		}
		if label == "" {
			switch incoming.ChildScope.Kind {
			case transport.ScopeKindDiscordThread:
				label = "thread"
			case transport.ScopeKindTelegramTopic:
				label = "topic"
			}
		}
		if label != "" {
			prefix += "/" + escapePresentation(label)
		}
	}
	return prefix + "/" + username, nil
}

func (r *Router) getChildContextMode() config.ChildContextDisplayMode {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.childContextMode
}

func (r *Router) withChildContextHeaders(incoming transport.Incoming, destination transport.EndpointID, scopes []store.CanonicalScope, text string) string {
	// Opaque ChildScope IDs are routing-only. Never expose them in forwarded text.
	return text
}

func senderPresentationUsername(sender transport.Sender, mode config.UsernameMode) string {
	if mode == config.UsernameModePushName {
		if displayName := normalizeDisplayName(sender.DisplayName); displayName != "" {
			return displayName
		}
		if sender.PhoneNumber != "" {
			return sender.PhoneNumber
		}
	}
	return sender.OpaqueID
}

func normalizeDisplayName(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func escapePresentation(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	for _, char := range []string{"*", "_", "`", "[", "]", "(", ")"} {
		value = strings.ReplaceAll(value, char, `\`+char)
	}
	return value
}

func newCanonicalID() (string, error) {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", err
	}
	return "c_" + hex.EncodeToString(randomBytes[:]), nil
}

func matchLocalPrefix(text string, localPrefixConfig string) bool {
	prefixes := strings.Fields(localPrefixConfig)
	for _, prefix := range prefixes {
		if prefix != "" && strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

func matchAggregationTrigger(text string, aggTriggerConfig string) bool {
	triggers := strings.Fields(aggTriggerConfig)
	if len(triggers) == 0 {
		triggers = []string{"aggregate-response"}
	}
	trimmed := strings.TrimSpace(text)
	for _, trigger := range triggers {
		if trimmed == trigger {
			return true
		}
	}
	return false
}
