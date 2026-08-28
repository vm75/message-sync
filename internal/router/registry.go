package router

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/transport"
)

// OutboundAdapter is the transport-owned outbound boundary used by the
// canonical router. Endpoint aliases remain the only addressing keys visible
// here; transport-specific remote target IDs stay inside adapter configuration.
type OutboundAdapter interface {
	Send(context.Context, transport.Outgoing) (transport.MessageRef, error)
	React(context.Context, transport.Reaction) error
	Edit(context.Context, transport.MessageRef, string) error
	Delete(context.Context, transport.MessageRef) error
}

// AdapterRegistry dispatches outbound operations to the adapter responsible
// for each configured endpoint transport.
type AdapterRegistry struct {
	mu        sync.RWMutex
	adapters  map[config.Transport]OutboundAdapter
	endpoints map[transport.EndpointID]config.Transport
}

func NewAdapterRegistry(cfg *config.Config, adapters map[config.Transport]OutboundAdapter) (*AdapterRegistry, error) {
	if cfg == nil {
		return nil, errors.New("config is required")
	}
	if len(adapters) == 0 {
		return nil, errors.New("at least one transport adapter is required")
	}

	copied := make(map[config.Transport]OutboundAdapter, len(adapters))
	for transportType, adapter := range adapters {
		if !transportType.IsValid() {
			return nil, errors.New("transport adapter registry contains unknown transport")
		}
		if adapter == nil {
			return nil, fmt.Errorf("transport adapter %q is unavailable", transportType)
		}
		copied[transportType] = adapter
	}

	registry := &AdapterRegistry{adapters: copied}
	if err := registry.UpdateConfig(cfg); err != nil {
		return nil, err
	}
	return registry, nil
}

func (r *AdapterRegistry) UpdateConfig(cfg *config.Config) error {
	if r == nil {
		return errors.New("transport adapter registry is not initialized")
	}
	if cfg == nil {
		return errors.New("config is required")
	}

	endpoints := make(map[transport.EndpointID]config.Transport, len(cfg.Endpoints))
	for alias, endpoint := range cfg.Endpoints {
		if !endpoint.Transport.IsValid() {
			return errors.New("configured endpoint has unknown transport")
		}
		if _, ok := r.adapters[endpoint.Transport]; !ok {
			return fmt.Errorf("transport adapter %q is unavailable", endpoint.Transport)
		}
		endpoints[transport.EndpointID(alias)] = endpoint.Transport
	}

	r.mu.Lock()
	r.endpoints = endpoints
	r.mu.Unlock()
	return nil
}

func (r *AdapterRegistry) adapterFor(endpoint transport.EndpointID) (OutboundAdapter, error) {
	if r == nil {
		return nil, errors.New("transport adapter registry is not initialized")
	}

	r.mu.RLock()
	transportType, ok := r.endpoints[endpoint]
	if !ok {
		r.mu.RUnlock()
		return nil, errors.New("destination endpoint is not configured")
	}
	adapter := r.adapters[transportType]
	r.mu.RUnlock()
	if adapter == nil {
		return nil, fmt.Errorf("transport adapter %q is unavailable", transportType)
	}
	return adapter, nil
}

func (r *AdapterRegistry) Send(ctx context.Context, outgoing transport.Outgoing) (transport.MessageRef, error) {
	adapter, err := r.adapterFor(outgoing.Endpoint)
	if err != nil {
		return transport.MessageRef{}, err
	}
	return adapter.Send(ctx, outgoing)
}

func (r *AdapterRegistry) React(ctx context.Context, reaction transport.Reaction) error {
	adapter, err := r.adapterFor(reaction.Endpoint)
	if err != nil {
		return err
	}
	return adapter.React(ctx, reaction)
}

func (r *AdapterRegistry) Edit(ctx context.Context, ref transport.MessageRef, text string) error {
	adapter, err := r.adapterFor(ref.Endpoint)
	if err != nil {
		return err
	}
	return adapter.Edit(ctx, ref, text)
}

func (r *AdapterRegistry) Delete(ctx context.Context, ref transport.MessageRef) error {
	adapter, err := r.adapterFor(ref.Endpoint)
	if err != nil {
		return err
	}
	return adapter.Delete(ctx, ref)
}
