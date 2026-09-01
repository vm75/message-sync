package delivery

import (
	"context"
	"errors"
	"sync"

	"github.com/vm75/message-sync/internal/transport"
)

var (
	ErrFull        = errors.New("delivery lane is full")
	ErrUnavailable = errors.New("delivery endpoint is unavailable")
)

type Job func(context.Context)

type lane struct {
	jobs chan Job
	ctx  context.Context
	stop context.CancelFunc
	wg   sync.WaitGroup
}

func newLane(parent context.Context, capacity int) *lane {
	ctx, cancel := context.WithCancel(parent)
	l := &lane{jobs: make(chan Job, capacity), ctx: ctx, stop: cancel}
	l.wg.Add(1)
	go l.run()
	return l
}

func (l *lane) run() {
	defer l.wg.Done()
	for {
		select {
		case <-l.ctx.Done():
			return
		case job := <-l.jobs:
			if job != nil {
				job(l.ctx)
			}
		}
	}
}

func (l *lane) close() {
	l.stop()
	l.wg.Wait()
}

type Manager struct {
	mu       sync.Mutex
	parent   context.Context
	capacity int
	lanes    map[transport.EndpointID]*lane
	closed   bool
}

func New(ctx context.Context, capacity int, endpoints []transport.EndpointID) (*Manager, error) {
	if ctx == nil {
		return nil, errors.New("delivery context is required")
	}
	if capacity <= 0 {
		return nil, errors.New("delivery lane capacity must be positive")
	}
	m := &Manager{parent: ctx, capacity: capacity, lanes: make(map[transport.EndpointID]*lane)}
	if err := m.Update(endpoints); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Update(endpoints []transport.EndpointID) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("delivery manager is closed")
	}
	wanted := make(map[transport.EndpointID]struct{}, len(endpoints))
	for _, endpoint := range endpoints {
		if endpoint == "" {
			m.mu.Unlock()
			return errors.New("delivery endpoint is required")
		}
		wanted[endpoint] = struct{}{}
	}
	var removed []*lane
	for endpoint, current := range m.lanes {
		if _, ok := wanted[endpoint]; !ok {
			delete(m.lanes, endpoint)
			removed = append(removed, current)
		}
	}
	for endpoint := range wanted {
		if _, ok := m.lanes[endpoint]; !ok {
			m.lanes[endpoint] = newLane(m.parent, m.capacity)
		}
	}
	m.mu.Unlock()
	for _, current := range removed {
		current.close()
	}
	return nil
}

func (m *Manager) Enqueue(ctx context.Context, endpoint transport.EndpointID, job Job) error {
	if ctx == nil || job == nil {
		return errors.New("delivery job and context are required")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrUnavailable
	}
	l, ok := m.lanes[endpoint]
	if !ok {
		m.mu.Unlock()
		return ErrUnavailable
	}
	select {
	case l.jobs <- job:
		m.mu.Unlock()
		return nil
	default:
		m.mu.Unlock()
		return ErrFull
	}
}

func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	lanes := make([]*lane, 0, len(m.lanes))
	for endpoint, l := range m.lanes {
		delete(m.lanes, endpoint)
		lanes = append(lanes, l)
	}
	m.mu.Unlock()
	for _, l := range lanes {
		l.close()
	}
}
