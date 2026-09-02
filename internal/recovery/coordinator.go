package recovery

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/vm75/message-sync/internal/router"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/transport"
)

const (
	DefaultMaxEvents = 200
	DefaultMaxAge    = 24 * time.Hour
)

type Coordinator struct {
	store     *store.Store
	router    *router.Router
	mu        sync.Mutex
	recoverMu sync.Mutex
	streams   map[string]*streamState
}

type streamState struct {
	mu      sync.Mutex
	loaded  bool
	cursor  int64
	acked   map[int64]time.Time
	failed  map[int64]struct{}
	pending map[int64]time.Time
}

func NewCoordinator(syncStore *store.Store, canonicalRouter *router.Router) (*Coordinator, error) {
	if syncStore == nil || canonicalRouter == nil {
		return nil, errors.New("recovery store and router are required")
	}
	return &Coordinator{store: syncStore, router: canonicalRouter, streams: make(map[string]*streamState)}, nil
}

func (c *Coordinator) stream(key string) *streamState {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.streams[key]
	if state == nil {
		state = &streamState{acked: make(map[int64]time.Time), failed: make(map[int64]struct{}), pending: make(map[int64]time.Time)}
		c.streams[key] = state
	}
	return state
}

// Handle serializes acknowledgements per stream and routes every event through
// the canonical router. Events without ordering metadata remain live-only.
func (c *Coordinator) Handle(ctx context.Context, incoming transport.Incoming) (router.Outcome, error) {
	if c == nil || c.router == nil {
		return router.Outcome{}, errors.New("recovery coordinator is not initialized")
	}
	cp := incoming.Checkpoint
	if !cp.Valid || cp.StreamKey == "" {
		return c.router.HandleEvent(ctx, incoming)
	}
	state := c.stream(cp.StreamKey)
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.loaded {
		cursor, err := c.store.RecoveryCursor(ctx, cp.StreamKey)
		if err == nil {
			state.cursor = cursor.Position
		} else if !errors.Is(err, sql.ErrNoRows) {
			return router.Outcome{}, err
		}
		state.loaded = true
	}
	if cp.Position <= state.cursor {
		return router.Outcome{Accepted: true, NoOp: true}, nil
	}
	outcome, err := c.router.HandleEvent(ctx, incoming)
	if err != nil || !outcome.Accepted {
		state.failed[cp.Position] = struct{}{}
		return outcome, err
	}
	if outcome.NoOp || !outcome.SafeToAdvance {
		if !outcome.SafeToAdvance {
			state.pending[cp.Position] = cp.EventTimestamp
		}
		return outcome, nil
	}
	state.acked[cp.Position] = cp.EventTimestamp
	delete(state.failed, cp.Position)
	delete(state.pending, cp.Position)
	if err := c.advance(ctx, state, cp.StreamKey); err != nil {
		return router.Outcome{}, err
	}
	return outcome, nil
}

func (c *Coordinator) advance(ctx context.Context, state *streamState, key string) error {
	positions := make([]int64, 0, len(state.acked))
	for position := range state.acked {
		if position > state.cursor {
			positions = append(positions, position)
		}
	}
	sort.Slice(positions, func(i, j int) bool { return positions[i] < positions[j] })
	for _, position := range positions {
		for pending := range state.pending {
			if pending <= position {
				return nil
			}
		}
		for failed := range state.failed {
			if failed <= position {
				return nil
			}
		}
		timestamp := state.acked[position]
		if err := c.store.PutRecoveryCursor(ctx, store.RecoveryCursor{StreamKey: key, Position: position, EventTimestamp: timestamp, UpdatedAt: time.Now().UTC()}); err != nil {
			return err
		}
		state.cursor = position
		delete(state.acked, position)
	}
	return nil
}

// Recover runs optional adapter sources with fixed bounds. Sources normalize
// provider history; the callback returns each item to Handle.
func (c *Coordinator) Recover(ctx context.Context, sources []transport.RecoverySource) {
	if c == nil {
		return
	}
	c.recoverMu.Lock()
	defer c.recoverMu.Unlock()
	if _, err := c.store.MarkDeliveryOperationsAwaitingReplay(ctx); err != nil {
		return
	}
	var wg sync.WaitGroup
	for _, source := range sources {
		if source == nil {
			continue
		}
		wg.Add(1)
		go func(source transport.RecoverySource) {
			defer wg.Done()
			for _, key := range source.RecoveryStreams() {
				cursor := transport.Checkpoint{StreamKey: key, Valid: true}
				if saved, err := c.store.RecoveryCursor(ctx, key); err == nil {
					cursor.Position = saved.Position
					cursor.EventTimestamp = saved.EventTimestamp
				}
				_ = source.Recover(ctx, transport.RecoveryRequest{Cursor: cursor, MaxEvents: DefaultMaxEvents, MaxAge: DefaultMaxAge}, func(eventCtx context.Context, incoming transport.Incoming) error {
					_, err := c.Handle(eventCtx, incoming)
					return err
				})
			}
		}(source)
	}
	wg.Wait()
}

// Start launches startup recovery and listens for adapter reconnect signals.
// A single recovery run is active at a time across all sources.
func (c *Coordinator) Start(ctx context.Context, sources []transport.RecoverySource) {
	if c == nil || len(sources) == 0 {
		return
	}
	var runMu sync.Mutex
	run := func() { runMu.Lock(); defer runMu.Unlock(); c.Recover(ctx, sources) }
	go run()
	for _, source := range sources {
		if source == nil || source.RecoverySignals() == nil {
			continue
		}
		go func(source transport.RecoverySource) {
			for {
				select {
				case <-ctx.Done():
					return
				case _, ok := <-source.RecoverySignals():
					if !ok {
						return
					}
					go run()
				}
			}
		}(source)
	}
}
