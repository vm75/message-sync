package router

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/store"
	"github.com/vm75/message-sync/internal/transport"
)

func TestAdapterRegistryMixedTransportFanout(t *testing.T) {
	ctx := context.Background()
	syncStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syncStore.Close() })

	cfg := mixedTransportConfig()
	wa := &fakeSender{}
	dc := &fakeSender{}
	registry, err := NewAdapterRegistry(cfg, map[config.Transport]OutboundAdapter{
		config.TransportWhatsApp: wa,
		config.TransportDiscord:  dc,
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(cfg, syncStore, registry)
	if err != nil {
		t.Fatal(err)
	}

	if err := r.Handle(ctx, testIncoming("wa1", "wa-source")); err != nil {
		t.Fatal(err)
	}
	if len(dc.sent) != 1 || dc.sent[0].outgoing.Endpoint != "discord" {
		t.Fatalf("WA -> Discord sends = %#v, want one Discord destination", dc.sent)
	}
	if len(wa.sent) != 1 || wa.sent[0].outgoing.Endpoint != "wa2" {
		t.Fatalf("WA -> WA sends = %#v, want one wa2 destination", wa.sent)
	}

	wa.sent = nil
	dc.sent = nil
	if err := r.Handle(ctx, testIncoming("discord", "discord-source")); err != nil {
		t.Fatal(err)
	}
	if len(dc.sent) != 0 {
		t.Fatalf("Discord source looped back through Discord adapter: %#v", dc.sent)
	}
	if len(wa.sent) != 2 {
		t.Fatalf("Discord -> WA sends = %#v, want two WhatsApp destinations", wa.sent)
	}
	got := map[transport.EndpointID]bool{}
	for _, sent := range wa.sent {
		got[sent.outgoing.Endpoint] = true
	}
	if !got["wa1"] || !got["wa2"] {
		t.Fatalf("Discord -> WA destinations = %#v, want wa1 and wa2", got)
	}
}

func TestAdapterRegistryPartialFanoutRestartRetriesOnlyMissingTransport(t *testing.T) {
	ctx := context.Background()
	syncStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syncStore.Close() })

	cfg := mixedTransportConfig()
	firstWA := &fakeSender{}
	firstDC := &fakeSender{}
	firstRegistry, err := NewAdapterRegistry(cfg, map[config.Transport]OutboundAdapter{
		config.TransportWhatsApp: firstWA,
		config.TransportDiscord:  firstDC,
	})
	if err != nil {
		t.Fatal(err)
	}
	firstRouter, err := New(cfg, syncStore, firstRegistry)
	if err != nil {
		t.Fatal(err)
	}
	crash := errors.New("simulated crash")
	firstRouter.afterPersist = func(endpoint transport.EndpointID) error {
		if endpoint == "discord" {
			return crash
		}
		return nil
	}

	incoming := testIncoming("wa1", "restart-source")
	if err := firstRouter.Handle(ctx, incoming); !errors.Is(err, crash) {
		t.Fatalf("first Handle error = %v, want simulated crash", err)
	}
	if len(firstDC.sent) != 1 || firstDC.sent[0].outgoing.Endpoint != "discord" {
		t.Fatalf("first Discord sends = %#v, want persisted discord copy", firstDC.sent)
	}
	if len(firstWA.sent) != 0 {
		t.Fatalf("first WhatsApp sends = %#v, want crash before wa2", firstWA.sent)
	}

	secondWA := &fakeSender{}
	secondDC := &fakeSender{}
	secondRegistry, err := NewAdapterRegistry(cfg, map[config.Transport]OutboundAdapter{
		config.TransportWhatsApp: secondWA,
		config.TransportDiscord:  secondDC,
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := New(cfg, syncStore, secondRegistry)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Handle(ctx, incoming); err != nil {
		t.Fatal(err)
	}
	if len(secondDC.sent) != 0 {
		t.Fatalf("restart resent already-persisted Discord copy: %#v", secondDC.sent)
	}
	if len(secondWA.sent) != 1 || secondWA.sent[0].outgoing.Endpoint != "wa2" {
		t.Fatalf("restart WhatsApp sends = %#v, want only missing wa2", secondWA.sent)
	}
}

func TestAdapterRegistryUnavailableTransportErrorDoesNotLeakRemoteID(t *testing.T) {
	cfg := mixedTransportConfig()
	remoteID := cfg.Endpoints["discord"].RemoteID
	_, err := NewAdapterRegistry(cfg, map[config.Transport]OutboundAdapter{
		config.TransportWhatsApp: &fakeSender{},
	})
	if err == nil {
		t.Fatal("expected unavailable Discord adapter error")
	}
	if !strings.Contains(err.Error(), "discord") {
		t.Fatalf("error = %q, want safe transport classification", err)
	}
	if strings.Contains(err.Error(), remoteID) {
		t.Fatalf("error leaked Discord remote ID: %q", err)
	}
}

func mixedTransportConfig() *config.Config {
	return &config.Config{
		Endpoints: map[string]config.Endpoint{
			"wa1":     {Transport: config.TransportWhatsApp, RemoteID: "111@g.us"},
			"discord": {Transport: config.TransportDiscord, RemoteID: "123456789012345678"},
			"wa2":     {Transport: config.TransportWhatsApp, RemoteID: "222@g.us"},
		},
		SyncSets: []config.SyncSet{{
			ID:     "mesh",
			Groups: []string{"wa1", "discord", "wa2"},
		}},
		Identity: config.Identity{UsernameMode: config.UsernameModePushName},
	}
}
