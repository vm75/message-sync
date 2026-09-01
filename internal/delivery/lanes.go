package delivery

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/vm75/message-sync/internal/transport"
)

var (
	ErrFull        = errors.New("delivery lane is full")
	ErrUnavailable = errors.New("delivery endpoint is unavailable")
)

type Job func(context.Context)
type RetryJob func(context.Context, int) error

type RetryPolicy struct {
	MaxAttempts int
	MaxWindow   time.Duration
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Wait        func(context.Context, time.Duration) error
	OnExhausted func(context.Context, error)
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: 4,
		MaxWindow:   10 * time.Second,
		BaseDelay:   10 * time.Millisecond,
		MaxDelay:    160 * time.Millisecond,
		Wait:        wait,
		OnExhausted: func(context.Context, error) {},
	}
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

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
			for {
				select {
				case job := <-l.jobs:
					if job != nil {
						job(l.ctx)
					}
				default:
					return
				}
			}
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
	policy   RetryPolicy
}

func New(ctx context.Context, capacity int, endpoints []transport.EndpointID) (*Manager, error) {
	if ctx == nil {
		return nil, errors.New("delivery context is required")
	}
	if capacity <= 0 {
		return nil, errors.New("delivery lane capacity must be positive")
	}
	m := &Manager{parent: ctx, capacity: capacity, lanes: make(map[transport.EndpointID]*lane), policy: DefaultRetryPolicy()}
	if err := m.Update(endpoints); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) SetRetryPolicy(policy RetryPolicy) error {
	if policy.MaxAttempts <= 0 || policy.MaxWindow <= 0 || policy.BaseDelay <= 0 || policy.MaxDelay < policy.BaseDelay || policy.Wait == nil || policy.OnExhausted == nil {
		return errors.New("invalid delivery retry policy")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("delivery manager is closed")
	}
	m.policy = policy
	return nil
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

func (m *Manager) EnqueueRetry(ctx context.Context, endpoint transport.EndpointID, job RetryJob) error {
	if job == nil {
		return errors.New("delivery retry job is required")
	}
	m.mu.Lock()
	policy := m.policy
	m.mu.Unlock()
	return m.Enqueue(ctx, endpoint, func(jobCtx context.Context) {
		started := time.Now()
		for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
			err := job(jobCtx, attempt)
			if err == nil {
				return
			}
			failure := transport.Classify(err)
			if !failure.Retryable || attempt == policy.MaxAttempts || time.Since(started) >= policy.MaxWindow {
				policy.OnExhausted(jobCtx, err)
				return
			}
			delay := failure.RetryAfter
			if delay <= 0 {
				delay = policy.BaseDelay
				for i := 1; i < attempt; i++ {
					delay *= 2
					if delay >= policy.MaxDelay {
						delay = policy.MaxDelay
						break
					}
				}
			}
			if delay > policy.MaxDelay {
				delay = policy.MaxDelay
			}
			if err := policy.Wait(jobCtx, delay); err != nil {
				policy.OnExhausted(jobCtx, err)
				return
			}
		}
	})
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
