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
// for each configured endpoint's owning connection.
type AdapterRegistry struct {
	mu        sync.RWMutex
	adapters  map[string]OutboundAdapter
	endpoints map[transport.EndpointID]string
}

func NewAdapterRegistry(cfg *config.Config, adapters map[string]OutboundAdapter) (*AdapterRegistry, error) {
	if cfg == nil {
		return nil, errors.New("config is required")
	}

	copied := make(map[string]OutboundAdapter, len(adapters))
	for connID, adapter := range adapters {
		if err := config.ValidateConnectionID(connID); err != nil {
			return nil, err
		}
		if adapter == nil {
			return nil, fmt.Errorf("connection adapter %q is unavailable", connID)
		}
		copied[connID] = adapter
	}

	registry := &AdapterRegistry{
		adapters:  copied,
		endpoints: make(map[transport.EndpointID]string),
	}
	if err := registry.UpdateConfig(cfg); err != nil {
		return nil, err
	}
	return registry, nil
}

func (r *AdapterRegistry) RegisterAdapter(connectionID string, adapter OutboundAdapter) error {
	if r == nil {
		return errors.New("transport adapter registry is not initialized")
	}
	if err := config.ValidateConnectionID(connectionID); err != nil {
		return err
	}
	if adapter == nil {
		return errors.New("adapter is required")
	}
	r.mu.Lock()
	r.adapters[connectionID] = adapter
	r.mu.Unlock()
	return nil
}

func (r *AdapterRegistry) UnregisterAdapter(connectionID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.adapters, connectionID)
	r.mu.Unlock()
}

func (r *AdapterRegistry) UpdateConfig(cfg *config.Config) error {
	if r == nil {
		return errors.New("transport adapter registry is not initialized")
	}
	if cfg == nil {
		return errors.New("config is required")
	}

	endpoints := make(map[transport.EndpointID]string, len(cfg.Endpoints))
	for alias, endpoint := range cfg.Endpoints {
		if !endpoint.Transport.IsValid() {
			return errors.New("configured endpoint has unknown transport")
		}
		if err := config.ValidateConnectionID(endpoint.ConnectionID); err != nil {
			return err
		}
		endpoints[transport.EndpointID(alias)] = endpoint.ConnectionID
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
	connID, ok := r.endpoints[endpoint]
	if !ok {
		r.mu.RUnlock()
		return nil, transport.NewFailure(transport.FailureDestinationMissing, 0, errors.New("destination endpoint is not configured"))
	}
	adapter := r.adapters[connID]
	r.mu.RUnlock()
	if adapter == nil {
		return nil, transport.NewFailure(transport.FailureTransient, 0, fmt.Errorf("connection %q is stopped or unavailable", connID))
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
