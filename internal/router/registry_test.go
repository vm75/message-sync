package router

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	waitForSent(t, dc, 1)
	waitForSent(t, wa, 1)
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
	waitForSent(t, wa, 2)
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
	if err := firstRouter.Handle(ctx, incoming); err != nil {
		t.Fatalf("first Handle error = %v", err)
	}
	waitForSent(t, firstDC, 1)
	waitForSent(t, firstWA, 1)
	if len(firstDC.sent) != 1 || firstDC.sent[0].outgoing.Endpoint != "discord" {
		t.Fatalf("first Discord sends = %#v, want persisted discord copy", firstDC.sent)
	}
	if len(firstWA.sent) != 1 {
		t.Fatalf("first WhatsApp sends = %#v, want independent wa2 delivery", firstWA.sent)
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
	// Both destination copies were persisted independently before the restart.
	if len(secondDC.sent) != 0 {
		t.Fatalf("restart resent already-persisted Discord copy: %#v", secondDC.sent)
	}
	if len(secondWA.sent) != 0 {
		t.Fatalf("restart resent already-persisted WhatsApp copy: %#v", secondWA.sent)
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
			ID:        "mesh",
			Endpoints: []string{"wa1", "discord", "wa2"},
		}},
		Identity: config.Identity{UsernameMode: config.UsernameModePushName},
	}
}

type failingOutboundAdapter struct {
	mu       sync.Mutex
	err      error
	attempts []transport.Outgoing
}

func (f *failingOutboundAdapter) Send(_ context.Context, outgoing transport.Outgoing) (transport.MessageRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts = append(f.attempts, outgoing)
	return transport.MessageRef{}, f.err
}

func (f *failingOutboundAdapter) attemptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.attempts)
}

func (f *failingOutboundAdapter) React(context.Context, transport.Reaction) error { return f.err }
func (f *failingOutboundAdapter) Edit(context.Context, transport.MessageRef, string) error {
	return f.err
}
func (f *failingOutboundAdapter) Delete(context.Context, transport.MessageRef) error { return f.err }

func TestAdapterRegistryThreeTransportFanout(t *testing.T) {
	ctx := context.Background()
	syncStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syncStore.Close() })

	cfg := threeTransportConfig()
	wa := &fakeSender{}
	dc := &fakeSender{}
	tg := &fakeSender{}
	registry, err := NewAdapterRegistry(cfg, map[config.Transport]OutboundAdapter{
		config.TransportWhatsApp: wa,
		config.TransportDiscord:  dc,
		config.TransportTelegram: tg,
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(cfg, syncStore, registry)
	if err != nil {
		t.Fatal(err)
	}

	if err := r.Handle(ctx, testIncoming("wa", "wa-source")); err != nil {
		t.Fatal(err)
	}
	waitForSent(t, dc, 1)
	waitForSent(t, tg, 1)
	if len(dc.sent) != 1 || dc.sent[0].outgoing.Endpoint != "discord" {
		t.Fatalf("WhatsApp -> Discord sends = %#v", dc.sent)
	}
	if len(tg.sent) != 1 || tg.sent[0].outgoing.Endpoint != "telegram" {
		t.Fatalf("WhatsApp -> Telegram sends = %#v", tg.sent)
	}

	wa.sent = nil
	dc.sent = nil
	tg.sent = nil
	if err := r.Handle(ctx, testIncoming("telegram", "telegram-source")); err != nil {
		t.Fatal(err)
	}
	waitForSent(t, wa, 1)
	waitForSent(t, dc, 1)
	if len(wa.sent) != 1 || wa.sent[0].outgoing.Endpoint != "wa" {
		t.Fatalf("Telegram -> WhatsApp sends = %#v", wa.sent)
	}
	if len(dc.sent) != 1 || dc.sent[0].outgoing.Endpoint != "discord" {
		t.Fatalf("Telegram -> Discord sends = %#v", dc.sent)
	}
	if len(tg.sent) != 0 {
		t.Fatalf("Telegram source looped back through Telegram adapter: %#v", tg.sent)
	}

	wa.sent = nil
	dc.sent = nil
	tg.sent = nil
	if err := r.Handle(ctx, testIncoming("discord", "discord-source")); err != nil {
		t.Fatal(err)
	}
	waitForSent(t, wa, 1)
	waitForSent(t, tg, 1)
	if len(wa.sent) != 1 || wa.sent[0].outgoing.Endpoint != "wa" {
		t.Fatalf("Discord -> WhatsApp sends = %#v", wa.sent)
	}
	if len(tg.sent) != 1 || tg.sent[0].outgoing.Endpoint != "telegram" {
		t.Fatalf("Discord -> Telegram sends = %#v", tg.sent)
	}
}

func TestAdapterRegistryThreeTransportPartialFailureRetriesOnlyMissingCopy(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "sync.db")
	firstStore, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}

	cfg := threeTransportConfig()
	firstWA := &fakeSender{}
	firstDC := &fakeSender{}
	sendFailure := errors.New("simulated Telegram send failure")
	firstTG := &failingOutboundAdapter{err: sendFailure}
	firstRegistry, err := NewAdapterRegistry(cfg, map[config.Transport]OutboundAdapter{
		config.TransportWhatsApp: firstWA,
		config.TransportDiscord:  firstDC,
		config.TransportTelegram: firstTG,
	})
	if err != nil {
		_ = firstStore.Close()
		t.Fatal(err)
	}
	firstRouter, err := New(cfg, firstStore, firstRegistry)
	if err != nil {
		_ = firstStore.Close()
		t.Fatal(err)
	}

	incoming := testIncoming("wa", "restart-source")
	if err := firstRouter.Handle(ctx, incoming); err != nil {
		_ = firstStore.Close()
		t.Fatalf("first Handle error = %v", err)
	}
	waitForSent(t, firstDC, 1)
	deadline := time.After(time.Second)
	for firstTG.attemptCount() < 4 {
		select {
		case <-deadline:
			_ = firstStore.Close()
			t.Fatal("Telegram lane did not execute")
		case <-time.After(time.Millisecond):
		}
	}
	if len(firstDC.sent) != 1 || firstDC.sent[0].outgoing.Endpoint != "discord" {
		_ = firstStore.Close()
		t.Fatalf("first Discord sends = %#v, want one successful persisted copy", firstDC.sent)
	}
	if firstTG.attemptCount() != 4 {
		_ = firstStore.Close()
		t.Fatalf("first Telegram attempts = %d, want bounded retries", firstTG.attemptCount())
	}
	if err := firstStore.Close(); err != nil {
		t.Fatal(err)
	}

	secondStore, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondStore.Close() })

	secondWA := &fakeSender{}
	secondDC := &fakeSender{}
	secondTG := &fakeSender{}
	secondRegistry, err := NewAdapterRegistry(cfg, map[config.Transport]OutboundAdapter{
		config.TransportWhatsApp: secondWA,
		config.TransportDiscord:  secondDC,
		config.TransportTelegram: secondTG,
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := New(cfg, secondStore, secondRegistry)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Handle(ctx, incoming); err != nil {
		t.Fatal(err)
	}
	waitForSent(t, secondTG, 1)
	if len(secondDC.sent) != 0 {
		t.Fatalf("restart resent already-persisted Discord copy: %#v", secondDC.sent)
	}
	if len(secondTG.sent) != 1 || secondTG.sent[0].outgoing.Endpoint != "telegram" {
		t.Fatalf("restart Telegram sends = %#v, want only missing Telegram copy", secondTG.sent)
	}
	if len(secondWA.sent) != 0 {
		t.Fatalf("restart unexpectedly sent to source WhatsApp adapter: %#v", secondWA.sent)
	}
}

func TestAdapterRegistryTelegramMessageIDIsOnlyRemoteCopyIdentity(t *testing.T) {
	ctx := context.Background()
	syncStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syncStore.Close() })

	cfg := threeTransportConfig()
	registry, err := NewAdapterRegistry(cfg, map[config.Transport]OutboundAdapter{
		config.TransportWhatsApp: &fakeSender{},
		config.TransportDiscord:  &fakeSender{},
		config.TransportTelegram: &fakeSender{},
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(cfg, syncStore, registry)
	if err != nil {
		t.Fatal(err)
	}

	const telegramMessageID = "987654321"
	incoming := testIncoming("telegram", telegramMessageID)
	if err := r.Handle(ctx, incoming); err != nil {
		t.Fatal(err)
	}
	canonicalID, err := syncStore.CanonicalForRemote(ctx, "telegram", telegramMessageID)
	if err != nil {
		t.Fatal(err)
	}
	if canonicalID == telegramMessageID || canonicalID == "" {
		t.Fatalf("canonical ID = %q, must be independent of Telegram message ID", canonicalID)
	}
}

func TestAdapterRegistryRuntimeReloadAddsTelegramRoutingAlias(t *testing.T) {
	initial := mixedTransportConfig()
	wa := &fakeSender{}
	dc := &fakeSender{}
	tg := &fakeSender{}
	registry, err := NewAdapterRegistry(initial, map[config.Transport]OutboundAdapter{
		config.TransportWhatsApp: wa,
		config.TransportDiscord:  dc,
		config.TransportTelegram: tg,
	})
	if err != nil {
		t.Fatal(err)
	}

	updated := threeTransportConfig()
	if err := registry.UpdateConfig(updated); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Send(context.Background(), transport.Outgoing{
		Endpoint: "telegram",
		Kind:     "text",
		Text:     "transient test body",
	}); err != nil {
		t.Fatal(err)
	}
	if len(tg.sent) != 1 || tg.sent[0].outgoing.Endpoint != "telegram" {
		t.Fatalf("runtime reload did not route Telegram alias to Telegram adapter: %#v", tg.sent)
	}
}

func TestAdapterRegistryUnavailableTelegramErrorDoesNotLeakRemoteID(t *testing.T) {
	cfg := threeTransportConfig()
	remoteID := cfg.Endpoints["telegram"].RemoteID
	_, err := NewAdapterRegistry(cfg, map[config.Transport]OutboundAdapter{
		config.TransportWhatsApp: &fakeSender{},
		config.TransportDiscord:  &fakeSender{},
	})
	if err == nil {
		t.Fatal("expected unavailable Telegram adapter error")
	}
	if !strings.Contains(err.Error(), "telegram") {
		t.Fatalf("error = %q, want safe transport classification", err)
	}
	if strings.Contains(err.Error(), remoteID) {
		t.Fatalf("error leaked Telegram remote ID: %q", err)
	}
}

func threeTransportConfig() *config.Config {
	return &config.Config{
		Endpoints: map[string]config.Endpoint{
			"wa":       {Transport: config.TransportWhatsApp, RemoteID: "111@g.us"},
			"discord":  {Transport: config.TransportDiscord, RemoteID: "123456789012345678"},
			"telegram": {Transport: config.TransportTelegram, RemoteID: "-1001234567890"},
		},
		SyncSets: []config.SyncSet{{
			ID:        "mesh",
			Endpoints: []string{"wa", "discord", "telegram"},
		}},
		Identity: config.Identity{UsernameMode: config.UsernameModePushName},
	}
}
