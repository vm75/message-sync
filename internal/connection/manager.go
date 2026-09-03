package connection

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/recovery"
	"github.com/vm75/message-sync/internal/router"
	"github.com/vm75/message-sync/internal/safelog"
	"github.com/vm75/message-sync/internal/transport"
)

// State represents the lifecycle state of a connection.
type State string

const (
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopped  State = "stopped"
	StateError    State = "error"
)

// Status provides a safe read-only snapshot of a connection's runtime status.
type Status struct {
	ID        string           `json:"id"`
	Transport config.Transport `json:"transport"`
	State     State            `json:"state"`
	Error     string           `json:"error,omitempty"`
}

// Adapter represents an active transport connection runtime instance.
type Adapter interface {
	router.OutboundAdapter
	Events() <-chan transport.Incoming
}

// Manager owns active connection adapter instances by opaque connection ID.
type Manager struct {
	mu          sync.RWMutex
	ctx         context.Context
	cancel      context.CancelFunc
	logger      *slog.Logger
	registry    *router.AdapterRegistry
	coordinator *recovery.Coordinator
	events      chan transport.Incoming
	entries     map[string]*entry
	closed      bool
}

type entry struct {
	id        string
	transport config.Transport
	adapter   Adapter
	state     State
	errStr    string
	cancel    context.CancelFunc
}

// NewManager creates a dynamic ConnectionManager.
func NewManager(ctx context.Context, logger *slog.Logger, registry *router.AdapterRegistry, coordinator *recovery.Coordinator) *Manager {
	managerCtx, cancel := context.WithCancel(ctx)
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		ctx:         managerCtx,
		cancel:      cancel,
		logger:      logger,
		registry:    registry,
		coordinator: coordinator,
		events:      make(chan transport.Incoming, 100),
		entries:     make(map[string]*entry),
	}
}

// Events returns the unified shared ingress channel fed by all active connections.
func (m *Manager) Events() <-chan transport.Incoming {
	return m.events
}

// Register registers and starts an adapter for the given connection ID.
func (m *Manager) Register(ctx context.Context, id string, transportType config.Transport, adapter Adapter) error {
	if id == "" {
		return errors.New("connection id is required")
	}
	if adapter == nil {
		return errors.New("adapter is required")
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("connection manager is closed")
	}
	if existing, ok := m.entries[id]; ok && existing.state == StateRunning {
		m.mu.Unlock()
		return fmt.Errorf("connection %q is already running", id)
	}

	connCtx, connCancel := context.WithCancel(m.ctx)
	e := &entry{
		id:        id,
		transport: transportType,
		adapter:   adapter,
		state:     StateRunning,
		cancel:    connCancel,
	}
	m.entries[id] = e
	m.mu.Unlock()

	if m.registry != nil {
		if err := m.registry.RegisterAdapter(id, adapter); err != nil {
			connCancel()
			m.mu.Lock()
			delete(m.entries, id)
			m.mu.Unlock()
			return fmt.Errorf("register outbound adapter for %q: %w", id, err)
		}
	}

	if m.coordinator != nil {
		if rec, ok := adapter.(transport.RecoverySource); ok {
			m.coordinator.RegisterSource(connCtx, rec)
		}
	}

	go m.forwardEvents(connCtx, id, adapter.Events())
	return nil
}

// Stop stops and unregisters a connection without disrupting other connections.
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	e, ok := m.entries[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("connection %q not found", id)
	}
	if e.state == StateStopped {
		m.mu.Unlock()
		return nil
	}
	e.state = StateStopped
	cancel := e.cancel
	adapter := e.adapter
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if m.coordinator != nil {
		if rec, ok := adapter.(transport.RecoverySource); ok {
			m.coordinator.UnregisterSource(rec)
		}
	}
	if m.registry != nil {
		m.registry.UnregisterAdapter(id)
	}
	if closer, ok := adapter.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil {
			safelog.Error(m.logger, "close connection adapter error", "close_adapter", err)
		}
	}
	return nil
}

// Restart atomically replaces an active connection's adapter instance.
func (m *Manager) Restart(ctx context.Context, id string, newAdapter Adapter) error {
	if id == "" {
		return errors.New("connection id is required")
	}
	if newAdapter == nil {
		return errors.New("new adapter is required")
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("connection manager is closed")
	}
	oldEntry, ok := m.entries[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("connection %q not found", id)
	}

	oldCancel := oldEntry.cancel
	oldAdapter := oldEntry.adapter
	transportType := oldEntry.transport

	newConnCtx, newConnCancel := context.WithCancel(m.ctx)
	oldEntry.adapter = newAdapter
	oldEntry.state = StateRunning
	oldEntry.errStr = ""
	oldEntry.cancel = newConnCancel
	m.mu.Unlock()

	// Stop previous forwarder and recovery
	if oldCancel != nil {
		oldCancel()
	}
	if m.coordinator != nil {
		if rec, ok := oldAdapter.(transport.RecoverySource); ok {
			m.coordinator.UnregisterSource(rec)
		}
	}
	if closer, ok := oldAdapter.(interface{ Close() error }); ok {
		_ = closer.Close()
	}

	// Register new adapter in registry and coordinator
	if m.registry != nil {
		_ = m.registry.RegisterAdapter(id, newAdapter)
	}
	if m.coordinator != nil {
		if rec, ok := newAdapter.(transport.RecoverySource); ok {
			m.coordinator.RegisterSource(newConnCtx, rec)
		}
	}

	_ = transportType
	go m.forwardEvents(newConnCtx, id, newAdapter.Events())
	return nil
}

// GetAdapter retrieves the active adapter for a connection ID.
func (m *Manager) GetAdapter(id string) (Adapter, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[id]
	if !ok || e.state != StateRunning {
		return nil, false
	}
	return e.adapter, true
}

// Status returns the current status of a connection.
func (m *Manager) Status(id string) (Status, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.entries[id]
	if !ok {
		return Status{}, false
	}
	return Status{
		ID:        e.id,
		Transport: e.transport,
		State:     e.state,
		Error:     e.errStr,
	}, true
}

// ListStatuses returns all connection statuses sorted by ID.
func (m *Manager) ListStatuses() []Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	res := make([]Status, 0, len(m.entries))
	for _, e := range m.entries {
		res = append(res, Status{
			ID:        e.id,
			Transport: e.transport,
			State:     e.state,
			Error:     e.errStr,
		})
	}
	sort.Slice(res, func(i, j int) bool { return res[i].ID < res[j].ID })
	return res
}

// UpdateConfig distributes configuration updates to the registry and active adapters.
func (m *Manager) UpdateConfig(cfg *config.Config) error {
	if cfg == nil {
		return errors.New("config is required")
	}
	if m.registry != nil {
		if err := m.registry.UpdateConfig(cfg); err != nil {
			return fmt.Errorf("update adapter registry: %w", err)
		}
	}

	m.mu.RLock()
	adapters := make([]Adapter, 0, len(m.entries))
	for _, e := range m.entries {
		if e.state == StateRunning && e.adapter != nil {
			adapters = append(adapters, e.adapter)
		}
	}
	m.mu.RUnlock()

	for _, a := range adapters {
		if updatable, ok := a.(interface{ UpdateConfig(*config.Config) error }); ok {
			if err := updatable.UpdateConfig(cfg); err != nil {
				safelog.Error(m.logger, "adapter config update error", "update_config", err)
			}
		}
	}
	return nil
}

func (m *Manager) forwardEvents(connCtx context.Context, connID string, stream <-chan transport.Incoming) {
	if stream == nil {
		return
	}
	for {
		select {
		case <-connCtx.Done():
			return
		case <-m.ctx.Done():
			return
		case ev, ok := <-stream:
			if !ok {
				m.mu.Lock()
				if e, exists := m.entries[connID]; exists && e.state == StateRunning {
					e.state = StateStopped
					e.errStr = "adapter event stream closed"
				}
				m.mu.Unlock()
				return
			}
			select {
			case m.events <- ev:
			case <-connCtx.Done():
				return
			case <-m.ctx.Done():
				return
			}
		}
	}
}

// Close stops all connections and releases manager resources.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.cancel()

	var closers []interface{ Close() error }
	for _, e := range m.entries {
		if e.state == StateStopped {
			continue
		}
		e.state = StateStopped
		if e.cancel != nil {
			e.cancel()
		}
		if closer, ok := e.adapter.(interface{ Close() error }); ok {
			closers = append(closers, closer)
		}
	}
	m.mu.Unlock()

	for _, closer := range closers {
		_ = closer.Close()
	}
	return nil
}
