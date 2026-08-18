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
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/safelog"
	"github.com/vm75/message-sync/internal/transport"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	waStore "go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)

const eventBufferSize = 128

type Options struct {
	DatabasePath     string
	GroupJIDs        map[string]string
	Hasher           *identity.Hasher
	UsernameMode     config.UsernameMode
	Logger           *slog.Logger
	QROut            io.Writer
	MediaEnabled     bool
	MediaMaxBytes    uint64
	RecoveryEnabled  bool
	RecoveryMaxAge   time.Duration
	RecoveryMaxCount int
}

type Adapter struct {
	client           *whatsmeow.Client
	container        *sqlstore.Container
	normalizer       *Normalizer
	targets          map[transport.EndpointID]types.JID
	events           chan transport.Incoming
	logger           *slog.Logger
	qrOut            io.Writer
	qrCancel         context.CancelFunc
	closeOnce        sync.Once
	closeErr         error
	mediaEnabled     bool
	mediaMaxBytes    uint64
	recoveryEnabled  bool
	recoveryMaxAge   time.Duration
	recoveryMaxCount int
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
		client:           client,
		container:        container,
		normalizer:       normalizer,
		targets:          targets,
		events:           make(chan transport.Incoming, eventBufferSize),
		logger:           opts.Logger,
		qrOut:            opts.QROut,
		mediaEnabled:     opts.MediaEnabled,
		mediaMaxBytes:    opts.MediaMaxBytes,
		recoveryEnabled:  opts.RecoveryEnabled,
		recoveryMaxAge:   opts.RecoveryMaxAge,
		recoveryMaxCount: opts.RecoveryMaxCount,
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
	target, ok := a.targets[outgoing.Endpoint]
	if !ok {
		return transport.MessageRef{}, errors.New("unknown WhatsApp endpoint")
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
	}

	var contextInfo *waE2E.ContextInfo
	if outgoing.ReplyTo != nil {
		if outgoing.ReplyTo.IsTargetFromMe {
			participant := a.client.Store.ID.ToNonAD().String()
			contextInfo = &waE2E.ContextInfo{
				StanzaID:    proto.String(outgoing.ReplyTo.RemoteMessageID),
				Participant: proto.String(participant),
			}
		} else {
			// Fallback: prepend quoted text
			quote := strings.TrimSpace(outgoing.QuotedText)
			if quote == "" {
				quote = "message"
			}
			outgoing.Text = fmt.Sprintf("> %s\n\n%s", quote, outgoing.Text)
		}
	}

	var msg waE2E.Message
	if outgoing.Kind == "text" || outgoing.Kind == "other" {
		if outgoing.Text == "" {
			return transport.MessageRef{}, errors.New("outgoing text is required")
		}
		text := outgoing.Text
		if contextInfo != nil {
			msg.ExtendedTextMessage = &waE2E.ExtendedTextMessage{
				Text:        &text,
				ContextInfo: contextInfo,
			}
		} else {
			msg.Conversation = &text
		}
	} else {
		if !a.mediaEnabled {
			return transport.MessageRef{}, errors.New("WhatsApp sender supports text only when media is disabled")
		}
		if len(outgoing.MediaBytes) == 0 {
			return transport.MessageRef{}, errors.New("outgoing media bytes are required")
		}
		mediaType, err := getMediaType(outgoing.Kind)
		if err != nil {
			return transport.MessageRef{}, err
		}

		uploadResp, err := a.client.Upload(ctx, outgoing.MediaBytes, mediaType)
		if err != nil {
			return transport.MessageRef{}, fmt.Errorf("upload WhatsApp media: %w", err)
		}

		if err := populateMediaMessage(&msg, outgoing, uploadResp, contextInfo); err != nil {
			return transport.MessageRef{}, err
		}
	}

	response, err := a.client.SendMessage(ctx, target, &msg)
	if err != nil {
		return transport.MessageRef{}, fmt.Errorf("send WhatsApp %s: %w", outgoing.Kind, err)
	}
	return transport.MessageRef{
		Endpoint:        outgoing.Endpoint,
		RemoteMessageID: string(response.ID),
	}, nil
}

func (a *Adapter) React(ctx context.Context, r transport.Reaction) error {
	if a == nil || a.client == nil {
		return errors.New("WhatsApp transport is not initialized")
	}
	target, ok := a.targets[r.Endpoint]
	if !ok {
		return errors.New("unknown WhatsApp endpoint")
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	if r.IsTargetFromMe {
		msg := a.client.BuildReaction(target, a.client.Store.ID.ToNonAD(), r.TargetRemoteID, r.Emoji)
		_, err := a.client.SendMessage(ctx, target, msg)
		if err != nil {
			return fmt.Errorf("send WhatsApp native reaction: %w", err)
		}
		return nil
	}

	if r.FallbackText != "" {
		_, err := a.client.SendMessage(ctx, target, &waE2E.Message{Conversation: proto.String(r.FallbackText)})
		if err != nil {
			return fmt.Errorf("send WhatsApp reaction fallback: %w", err)
		}
	}
	return nil
}

func (a *Adapter) Edit(ctx context.Context, ref transport.MessageRef, text string) error {
	if a == nil || a.client == nil {
		return errors.New("WhatsApp transport is not initialized")
	}
	target, ok := a.targets[ref.Endpoint]
	if !ok {
		return errors.New("unknown WhatsApp endpoint")
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	editContent := &waE2E.Message{
		Conversation: proto.String(text),
	}
	editMsg := a.client.BuildEdit(target, types.MessageID(ref.RemoteMessageID), editContent)
	_, err := a.client.SendMessage(ctx, target, editMsg)
	if err != nil {
		return fmt.Errorf("send WhatsApp edit: %w", err)
	}
	return nil
}

func (a *Adapter) Delete(ctx context.Context, ref transport.MessageRef) error {
	if a == nil || a.client == nil {
		return errors.New("WhatsApp transport is not initialized")
	}
	target, ok := a.targets[ref.Endpoint]
	if !ok {
		return errors.New("unknown WhatsApp endpoint")
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	revokeMsg := a.client.BuildRevoke(target, types.EmptyJID, types.MessageID(ref.RemoteMessageID))
	_, err := a.client.SendMessage(ctx, target, revokeMsg)
	if err != nil {
		return fmt.Errorf("send WhatsApp delete: %w", err)
	}
	return nil
}

func getMediaType(kind string) (whatsmeow.MediaType, error) {
	switch kind {
	case "image", "sticker":
		return whatsmeow.MediaImage, nil
	case "video":
		return whatsmeow.MediaVideo, nil
	case "document":
		return whatsmeow.MediaDocument, nil
	case "audio":
		return whatsmeow.MediaAudio, nil
	default:
		return "", fmt.Errorf("unsupported media kind: %s", kind)
	}
}

func populateMediaMessage(msg *waE2E.Message, outgoing transport.Outgoing, uploadResp whatsmeow.UploadResponse, contextInfo *waE2E.ContextInfo) error {
	var caption *string
	if outgoing.Text != "" {
		caption = proto.String(outgoing.Text)
	}

	switch outgoing.Kind {
	case "image":
		msg.ImageMessage = &waE2E.ImageMessage{
			Caption:       caption,
			Mimetype:      proto.String("image/jpeg"), // Best-effort fallback; real mime depends on content, but whatsmeow often infers it internally or clients ignore it.
			URL:           &uploadResp.URL,
			DirectPath:    &uploadResp.DirectPath,
			MediaKey:      uploadResp.MediaKey,
			FileEncSHA256: uploadResp.FileEncSHA256,
			FileSHA256:    uploadResp.FileSHA256,
			FileLength:    &uploadResp.FileLength,
			ContextInfo:   contextInfo,
		}
	case "video":
		msg.VideoMessage = &waE2E.VideoMessage{
			Caption:       caption,
			Mimetype:      proto.String("video/mp4"),
			URL:           &uploadResp.URL,
			DirectPath:    &uploadResp.DirectPath,
			MediaKey:      uploadResp.MediaKey,
			FileEncSHA256: uploadResp.FileEncSHA256,
			FileSHA256:    uploadResp.FileSHA256,
			FileLength:    &uploadResp.FileLength,
			ContextInfo:   contextInfo,
		}
	case "document":
		msg.DocumentMessage = &waE2E.DocumentMessage{
			Caption:       caption,
			Mimetype:      proto.String("application/octet-stream"),
			FileName:      proto.String("document"),
			URL:           &uploadResp.URL,
			DirectPath:    &uploadResp.DirectPath,
			MediaKey:      uploadResp.MediaKey,
			FileEncSHA256: uploadResp.FileEncSHA256,
			FileSHA256:    uploadResp.FileSHA256,
			FileLength:    &uploadResp.FileLength,
			ContextInfo:   contextInfo,
		}
	case "audio":
		msg.AudioMessage = &waE2E.AudioMessage{
			Mimetype:      proto.String("audio/ogg; codecs=opus"),
			URL:           &uploadResp.URL,
			DirectPath:    &uploadResp.DirectPath,
			MediaKey:      uploadResp.MediaKey,
			FileEncSHA256: uploadResp.FileEncSHA256,
			FileSHA256:    uploadResp.FileSHA256,
			FileLength:    &uploadResp.FileLength,
			ContextInfo:   contextInfo,
		}
	case "sticker":
		msg.StickerMessage = &waE2E.StickerMessage{
			Mimetype:      proto.String("image/webp"),
			URL:           &uploadResp.URL,
			DirectPath:    &uploadResp.DirectPath,
			MediaKey:      uploadResp.MediaKey,
			FileEncSHA256: uploadResp.FileEncSHA256,
			FileSHA256:    uploadResp.FileSHA256,
			FileLength:    &uploadResp.FileLength,
			ContextInfo:   contextInfo,
		}
	default:
		return fmt.Errorf("unsupported media population for kind: %s", outgoing.Kind)
	}
	return nil
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
		if a.recoveryMaxAge > 0 && !evt.Info.Timestamp.IsZero() && time.Since(evt.Info.Timestamp) > a.recoveryMaxAge {
			return
		}
		incoming, ok := a.normalizer.NormalizeMessage(evt, a.mediaEnabled, a.mediaMaxBytes, a.client.Download)
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
	case *events.HistorySync:
		if !a.recoveryEnabled || evt.Data == nil {
			return
		}
		for _, conv := range evt.Data.GetConversations() {
			chatJID, err := types.ParseJID(conv.GetID())
			if err != nil || chatJID.Server != types.GroupServer {
				continue
			}
			chatJID = chatJID.ToNonAD()
			if _, ok := a.normalizer.endpoints[chatJID.String()]; !ok {
				continue
			}
			count := 0
			for _, historyMsg := range conv.GetMessages() {
				if a.recoveryMaxCount > 0 && count >= a.recoveryMaxCount {
					break
				}
				webMsg := historyMsg.GetMessage()
				if webMsg == nil {
					continue
				}
				parsed, err := a.client.ParseWebMessage(chatJID, webMsg)
				if err != nil || parsed == nil {
					continue
				}
				if a.recoveryMaxAge > 0 && !parsed.Info.Timestamp.IsZero() && time.Since(parsed.Info.Timestamp) > a.recoveryMaxAge {
					continue
				}
				incoming, ok := a.normalizer.NormalizeMessage(parsed, a.mediaEnabled, a.mediaMaxBytes, a.client.Download)
				if !ok {
					continue
				}
				select {
				case a.events <- incoming:
					count++
				default:
					a.logger.Warn("WhatsApp ingress queue full",
						"event", "message_dropped",
						"endpoint", string(incoming.Endpoint),
						"kind", incoming.Kind,
					)
				}
			}
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
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
	} {
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
