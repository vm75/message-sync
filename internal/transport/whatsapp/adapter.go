package whatsapp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mdp/qrterminal/v3"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/safelog"
	"github.com/vm75/message-sync/internal/transport"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	_ "modernc.org/sqlite"
)

const eventBufferSize = 128

type Options struct {
	DatabasePath string
	GroupJIDs    map[string]string
	Hasher       *identity.Hasher
	UsernameMode string
	Logger       *slog.Logger
	QROut        io.Writer
}

type Adapter struct {
	client     *whatsmeow.Client
	container  *sqlstore.Container
	normalizer *Normalizer
	targets    map[transport.EndpointID]types.JID
	events     chan transport.Incoming
	logger     *slog.Logger
	qrOut      io.Writer
	qrCancel   context.CancelFunc
	closeOnce  sync.Once
	closeErr   error
}

func Open(ctx context.Context, opts Options) (*Adapter, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	if opts.Logger == nil {
		return nil, errors.New("logger is required")
	}
	if opts.QROut == nil {
		opts.QROut = io.Discard
	}

	normalizer, err := NewNormalizer(opts.GroupJIDs, opts.Hasher, opts.UsernameMode)
	if err != nil {
		return nil, err
	}
	targets, err := outgoingTargets(opts.GroupJIDs)
	if err != nil {
		return nil, err
	}
	container, device, err := openSessionStore(ctx, opts.DatabasePath)
	if err != nil {
		return nil, err
	}

	client := whatsmeow.NewClient(device, nil)
	disablePlaintextPersistence(client)

	adapter := &Adapter{
		client:     client,
		container:  container,
		normalizer: normalizer,
		targets:    targets,
		events:     make(chan transport.Incoming, eventBufferSize),
		logger:     opts.Logger,
		qrOut:      opts.QROut,
	}
	client.AddEventHandler(adapter.handleEvent)

	if device.ID == nil {
		qrCtx, cancel := context.WithCancel(ctx)
		qrChan, qrErr := client.GetQRChannel(qrCtx)
		if qrErr != nil {
			cancel()
			_ = container.Close()
			return nil, fmt.Errorf("prepare WhatsApp QR pairing: %w", qrErr)
		}
		adapter.qrCancel = cancel
		go adapter.consumeQR(qrChan)
	}

	if err := client.ConnectContext(ctx); err != nil {
		_ = adapter.Close()
		return nil, fmt.Errorf("connect WhatsApp transport: %w", err)
	}
	return adapter, nil
}

func (a *Adapter) Events() <-chan transport.Incoming {
	return a.events
}

func (a *Adapter) Send(ctx context.Context, outgoing transport.Outgoing) (transport.MessageRef, error) {
	if a == nil || a.client == nil {
		return transport.MessageRef{}, errors.New("WhatsApp transport is not initialized")
	}
	if outgoing.Kind != "text" || outgoing.Media != nil || outgoing.ReplyTo != nil {
		return transport.MessageRef{}, errors.New("WhatsApp Phase 2 sender supports plain text only")
	}
	target, ok := a.targets[outgoing.Endpoint]
	if !ok {
		return transport.MessageRef{}, errors.New("unknown WhatsApp endpoint")
	}
	if outgoing.Text == "" {
		return transport.MessageRef{}, errors.New("outgoing text is required")
	}

	text := outgoing.Text
	response, err := a.client.SendMessage(ctx, target, &waE2E.Message{Conversation: &text})
	if err != nil {
		return transport.MessageRef{}, fmt.Errorf("send WhatsApp text: %w", err)
	}
	return transport.MessageRef{
		Endpoint:        outgoing.Endpoint,
		RemoteMessageID: string(response.ID),
	}, nil
}

func (a *Adapter) Close() error {
	if a == nil {
		return nil
	}
	a.closeOnce.Do(func() {
		if a.qrCancel != nil {
			a.qrCancel()
		}
		if a.client != nil {
			a.client.Disconnect()
		}
		if a.container != nil {
			a.closeErr = a.container.Close()
		}
	})
	return a.closeErr
}

func (a *Adapter) handleEvent(raw any) {
	switch evt := raw.(type) {
	case *events.Message:
		incoming, ok := a.normalizer.NormalizeMessage(evt)
		if !ok {
			return
		}
		select {
		case a.events <- incoming:
		default:
			a.logger.Warn("WhatsApp ingress queue full",
				"event", "message_dropped",
				"endpoint", string(incoming.Endpoint),
				"kind", incoming.Kind,
			)
		}
	case *events.Connected:
		a.logger.Info("WhatsApp connected", "event", "whatsapp_connected")
	case *events.Disconnected:
		a.logger.Warn("WhatsApp disconnected", "event", "whatsapp_disconnected")
	}
}

func (a *Adapter) consumeQR(items <-chan whatsmeow.QRChannelItem) {
	for item := range items {
		switch item.Event {
		case "code":
			fmt.Fprintln(a.qrOut, "\nWhatsApp pairing required. In WhatsApp open Linked devices, choose Link a device, and scan this QR code:")
			qrterminal.GenerateHalfBlock(item.Code, qrterminal.L, a.qrOut)
		case "success":
			fmt.Fprintln(a.qrOut, "WhatsApp pairing complete. The linked session is stored in whatsapp.db.")
		case "timeout":
			a.logger.Warn("WhatsApp QR pairing timed out", "event", "whatsapp_pairing_timeout")
		case "error":
			safelog.Error(a.logger, "WhatsApp QR pairing failed", "whatsapp_pairing", item.Error)
		case "err-client-outdated", "err-unexpected-state", "err-scanned-without-multidevice":
			a.logger.Error("WhatsApp QR pairing failed",
				"event", "whatsapp_pairing_failed",
				"reason", item.Event,
			)
		}
	}
}

func outgoingTargets(groupJIDs map[string]string) (map[transport.EndpointID]types.JID, error) {
	targets := make(map[transport.EndpointID]types.JID, len(groupJIDs))
	seen := make(map[string]struct{}, len(groupJIDs))
	for alias, rawJID := range groupJIDs {
		jid, err := types.ParseJID(strings.TrimSpace(rawJID))
		if err != nil || jid.Server != types.GroupServer || jid.User == "" {
			return nil, fmt.Errorf("group %q has invalid WhatsApp group JID", alias)
		}
		jid = jid.ToNonAD()
		if _, duplicate := seen[jid.String()]; duplicate {
			return nil, fmt.Errorf("group %q duplicates a configured WhatsApp group", alias)
		}
		seen[jid.String()] = struct{}{}
		targets[transport.EndpointID(alias)] = jid
	}
	return targets, nil
}

func disablePlaintextPersistence(client *whatsmeow.Client) {
	client.EnableDecryptedEventBuffer = false
	client.UseRetryMessageStore = false
}

func openSessionStore(ctx context.Context, rawPath string) (*sqlstore.Container, *waStore.Device, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return nil, nil, errors.New("WhatsApp database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, fmt.Errorf("create WhatsApp database directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, nil, fmt.Errorf("open WhatsApp database: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA foreign_keys = ON", "PRAGMA busy_timeout = 5000"} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			_ = db.Close()
			return nil, nil, fmt.Errorf("configure WhatsApp database: %w", err)
		}
	}

	container := sqlstore.NewWithDB(db, "sqlite3", nil)
	if err := container.Upgrade(ctx); err != nil {
		_ = container.Close()
		return nil, nil, fmt.Errorf("migrate WhatsApp database: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = container.Close()
		return nil, nil, fmt.Errorf("secure WhatsApp database permissions: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		_ = container.Close()
		return nil, nil, fmt.Errorf("load WhatsApp session: %w", err)
	}
	return container, device, nil
}
