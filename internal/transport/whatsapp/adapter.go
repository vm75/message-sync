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
	"github.com/vm75/message-sync/internal/api"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/safelog"
	"github.com/vm75/message-sync/internal/transport"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
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
	EnableTerminalQR bool
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
	enableTerminalQR bool
	qrCancel         context.CancelFunc
	pairingActive    bool
	currentQRCode    string
	currentQRExpires time.Time
	pairingCodeChan  chan string
	mu               sync.Mutex
	closeOnce        sync.Once
	closeErr         error
	mediaEnabled     bool
	mediaMaxBytes    uint64
	recoveryEnabled  bool
	recoveryMaxAge   time.Duration
	recoveryMaxCount int
	pcache           *participantCache
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
		enableTerminalQR: opts.EnableTerminalQR,
		mediaEnabled:     opts.MediaEnabled,
		mediaMaxBytes:    opts.MediaMaxBytes,
		recoveryEnabled:  opts.RecoveryEnabled,
		recoveryMaxAge:   opts.RecoveryMaxAge,
		recoveryMaxCount: opts.RecoveryMaxCount,
		pcache:           newParticipantCache(1024),
	}
	client.AddEventHandler(adapter.handleEvent)

	if device.ID != nil {
		if err := client.ConnectContext(ctx); err != nil {
			_ = adapter.Close()
			return nil, fmt.Errorf("connect WhatsApp transport: %w", err)
		}
	} else if opts.EnableTerminalQR {
		if _, err := adapter.Pair(ctx); err != nil {
			_ = adapter.Close()
			return nil, fmt.Errorf("start WhatsApp terminal pairing: %w", err)
		}
	}

	return adapter, nil
}

func (a *Adapter) getContactInfo(jid types.JID) types.ContactInfo {
	if a.client != nil && a.client.Store != nil && a.client.Store.Contacts != nil {
		if info, err := a.client.Store.Contacts.GetContact(context.Background(), jid); err == nil {
			return info
		}
	}
	return types.ContactInfo{}
}

func (a *Adapter) Events() <-chan transport.Incoming {
	return a.events
}

func (a *Adapter) Send(ctx context.Context, outgoing transport.Outgoing) (transport.MessageRef, error) {
	if a == nil || a.client == nil {
		return transport.MessageRef{}, errors.New("WhatsApp transport is not initialized")
	}
	a.mu.Lock()
	target, ok := a.targets[outgoing.Endpoint]
	mediaEnabled := a.mediaEnabled
	a.mu.Unlock()
	if !ok {
		return transport.MessageRef{}, fmt.Errorf("unknown WhatsApp endpoint %q", outgoing.Endpoint)
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
	}

	var contextInfo *waE2E.ContextInfo
	if outgoing.ReplyTo != nil {
		var participant string
		var useNative bool

		if outgoing.ReplyTo.IsTargetFromMe {
			participant = a.client.Store.ID.ToNonAD().String()
			useNative = true
		} else if jid, ok := a.pcache.Get(outgoing.ReplyTo.RemoteMessageID); ok {
			participant = jid
			useNative = true
		}

		if useNative {
			quote := strings.TrimSpace(outgoing.QuotedText)
			if quote == "" {
				quote = "message"
			}
			contextInfo = &waE2E.ContextInfo{
				StanzaID:    proto.String(outgoing.ReplyTo.RemoteMessageID),
				Participant: proto.String(participant),
				QuotedMessage: &waE2E.Message{
					Conversation: proto.String(quote),
				},
			}
		} else {
			// Fallback: prepend quoted text when original participant is unknown
			quote := strings.TrimSpace(outgoing.QuotedText)
			if quote == "" {
				quote = "message"
			}
			outgoing.Text = fmt.Sprintf("> %s\n\n%s", quote, outgoing.Text)
		}
	}

	if len(outgoing.Mentions) > 0 {
		groupInfo, _ := a.client.GetGroupInfo(ctx, target)
		isParticipant := func(user string) bool {
			if groupInfo == nil {
				return false
			}
			for _, p := range groupInfo.Participants {
				if p.JID.User == user {
					return true
				}
			}
			return false
		}

		seenMentions := make(map[string]bool)
		for _, m := range outgoing.Mentions {
			if seenMentions[m.RemoteID] {
				continue
			}
			seenMentions[m.RemoteID] = true

			var replacement string
			if isParticipant(m.RemoteID) {
				// Format as a plain-text mention (non-clickable) to avoid WhatsApp revealing the phone number.
				replacement = fmt.Sprintf("@%s", m.Name)
			} else {
				if outgoing.OriginEndpoint != "" {
					replacement = fmt.Sprintf("<%s/%s>", outgoing.OriginEndpoint, m.Name)
				} else {
					replacement = fmt.Sprintf("<%s>", m.Name)
				}
			}
			outgoing.Text = strings.ReplaceAll(outgoing.Text, "@"+m.RemoteID, replacement)
			outgoing.QuotedText = strings.ReplaceAll(outgoing.QuotedText, "@"+m.RemoteID, replacement)
		}
	}

	var msg *waE2E.Message
	if outgoing.Kind == "poll" {
		if outgoing.Text == "" {
			return transport.MessageRef{}, errors.New("outgoing poll question is required")
		}
		if len(outgoing.PollOptions) == 0 {
			return transport.MessageRef{}, errors.New("outgoing poll options are required")
		}
		msg = a.client.BuildPollCreation(outgoing.Text, outgoing.PollOptions, outgoing.PollSelectableCount)
		if contextInfo != nil && msg.PollCreationMessage != nil {
			msg.PollCreationMessage.ContextInfo = contextInfo
		}
	} else if outgoing.Kind == "text" || outgoing.Kind == "other" {
		if outgoing.Text == "" {
			return transport.MessageRef{}, errors.New("outgoing text is required")
		}
		text := outgoing.Text
		if contextInfo != nil {
			msg = &waE2E.Message{
				ExtendedTextMessage: &waE2E.ExtendedTextMessage{
					Text:        &text,
					ContextInfo: contextInfo,
				},
			}
		} else {
			msg = &waE2E.Message{
				Conversation: &text,
			}
		}
	} else {
		if !mediaEnabled {
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

		msg = &waE2E.Message{}
		if err := populateMediaMessage(msg, outgoing, uploadResp, contextInfo); err != nil {
			return transport.MessageRef{}, err
		}
	}

	response, err := a.client.SendMessage(ctx, target, msg)
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
	a.mu.Lock()
	target, ok := a.targets[r.Endpoint]
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown WhatsApp endpoint %q", r.Endpoint)
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	var participant *string
	if !r.IsTargetFromMe {
		if jid, ok := a.pcache.Get(r.TargetRemoteID); ok {
			participant = proto.String(jid)
		} else {
			// Cache miss, we drop the reaction to avoid PII violations
			return nil
		}
	}

	reactionMsg := &waE2E.Message{
		ReactionMessage: &waE2E.ReactionMessage{
			Key: &waCommon.MessageKey{
				RemoteJID:   proto.String(target.String()),
				FromMe:      proto.Bool(r.IsTargetFromMe),
				ID:          proto.String(r.TargetRemoteID),
				Participant: participant,
			},
			Text:              proto.String(r.Emoji),
			SenderTimestampMS: proto.Int64(time.Now().UnixMilli()),
		},
	}
	_, err := a.client.SendMessage(ctx, target, reactionMsg)
	if err != nil {
		return fmt.Errorf("send WhatsApp native reaction: %w", err)
	}
	return nil
}

func (a *Adapter) Edit(ctx context.Context, ref transport.MessageRef, text string) error {
	if a == nil || a.client == nil {
		return errors.New("WhatsApp transport is not initialized")
	}
	a.mu.Lock()
	target, ok := a.targets[ref.Endpoint]
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown WhatsApp endpoint %q", ref.Endpoint)
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
	a.mu.Lock()
	target, ok := a.targets[ref.Endpoint]
	a.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown WhatsApp endpoint %q", ref.Endpoint)
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

// BuildClearChatPatch creates an AppState PatchInfo for clearing messages in a chat up to the given cutoff.
func BuildClearChatPatch(target types.JID, cutoff time.Time, deleteMedia bool) appstate.PatchInfo {
	if cutoff.IsZero() {
		cutoff = time.Now().UTC()
	}
	action := &waSyncAction.ClearChatAction{
		MessageRange: &waSyncAction.SyncActionMessageRange{
			LastMessageTimestamp: proto.Int64(cutoff.Unix()),
		},
	}
	deleteMediaInt := "0"
	if deleteMedia {
		deleteMediaInt = "1"
	}
	return appstate.PatchInfo{
		Type: appstate.WAPatchRegularHigh,
		Mutations: []appstate.MutationInfo{{
			Index:   []string{appstate.IndexClearChat, target.String(), "0", deleteMediaInt},
			Version: 6,
			Value: &waSyncAction.SyncActionValue{
				ClearChatAction: action,
			},
		}},
	}
}

// ClearChatOlderThan dispatches an AppState clearChat mutation to clear messages on the sync account
// up to the given cutoff timestamp for a specific group.
func (a *Adapter) ClearChatOlderThan(ctx context.Context, target types.JID, cutoff time.Time, deleteMedia bool) error {
	if a == nil || a.client == nil {
		return errors.New("whatsapp client is not initialized")
	}
	if !a.client.IsLoggedIn() {
		return errors.New("whatsapp client is not logged in")
	}
	patch := BuildClearChatPatch(target, cutoff, deleteMedia)
	return a.client.SendAppState(ctx, patch)
}

// ClearSyncSetChats clears messages older than the cutoff timestamp for the given group JIDs.
// It returns the number of groups successfully cleared.
func (a *Adapter) ClearSyncSetChats(ctx context.Context, groupJIDs []types.JID, cutoff time.Time) (int, error) {
	if a == nil || a.client == nil {
		return 0, errors.New("whatsapp client is not initialized")
	}
	if !a.client.IsLoggedIn() {
		return 0, errors.New("whatsapp client is not logged in")
	}
	clearedCount := 0
	var firstErr error
	for _, jid := range groupJIDs {
		if err := a.ClearChatOlderThan(ctx, jid, cutoff, true); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			safelog.Error(a.logger, "failed to clear chat for group", "clear_chat", err)
			continue
		}
		clearedCount++
	}
	return clearedCount, firstErr
}

// IsLoggedIn returns whether the underlying WhatsApp client is currently authenticated.
func (a *Adapter) IsLoggedIn() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.client != nil && a.client.IsLoggedIn()
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
		a.mu.Lock()
		if a.qrCancel != nil {
			a.qrCancel()
			a.qrCancel = nil
		}
		a.pairingActive = false
		a.currentQRCode = ""
		a.pairingCodeChan = nil
		a.mu.Unlock()

		if a.client != nil {
			a.client.Disconnect()
		}
		if a.container != nil {
			a.closeErr = a.container.Close()
		}
	})
	return a.closeErr
}

func (a *Adapter) Status(ctx context.Context) api.WhatsAppStatus {
	if a == nil {
		return api.WhatsAppStatus{
			Status:      "unpaired",
			IsLoggedIn:  false,
			IsConnected: false,
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	var isLoggedIn, isConnected bool
	if a.client != nil {
		isLoggedIn = a.client.IsLoggedIn()
		isConnected = a.client.IsConnected()
	}

	var state string
	if isLoggedIn && isConnected {
		state = "connected"
	} else if isLoggedIn && !isConnected {
		state = "disconnected"
	} else if a.pairingActive {
		state = "pairing"
	} else {
		state = "unpaired"
	}

	res := api.WhatsAppStatus{
		Status:      state,
		IsLoggedIn:  isLoggedIn,
		IsConnected: isConnected,
	}
	if a.pairingActive && a.currentQRCode != "" && time.Now().Before(a.currentQRExpires) {
		res.QRCode = a.currentQRCode
	}
	return res
}

func (a *Adapter) Pair(ctx context.Context) (api.WhatsAppPairResponse, error) {
	if a == nil || a.client == nil {
		return api.WhatsAppPairResponse{}, errors.New("WhatsApp transport is not initialized")
	}

	a.mu.Lock()
	if a.client.IsLoggedIn() {
		a.mu.Unlock()
		return api.WhatsAppPairResponse{
			Status:     "connected",
			IsLoggedIn: true,
		}, nil
	}

	// If already pairing and current QR code is still valid, return it
	if a.pairingActive && a.currentQRCode != "" && time.Now().Before(a.currentQRExpires) {
		code := a.currentQRCode
		remaining := int(time.Until(a.currentQRExpires).Seconds())
		if remaining < 1 {
			remaining = 1
		}
		a.mu.Unlock()
		return api.WhatsAppPairResponse{
			Status:         "pairing",
			QRCode:         code,
			TimeoutSeconds: remaining,
		}, nil
	}

	// Cancel existing pairing if any
	if a.qrCancel != nil {
		a.qrCancel()
		a.qrCancel = nil
	}
	if a.client.IsConnected() {
		a.client.Disconnect()
	}

	qrCtx, cancel := context.WithCancel(context.Background())
	a.qrCancel = cancel
	a.pairingActive = true
	a.currentQRCode = ""
	codeChan := make(chan string, 1)
	a.pairingCodeChan = codeChan

	qrChan, qrErr := a.client.GetQRChannel(qrCtx)
	if qrErr != nil {
		cancel()
		a.pairingActive = false
		a.pairingCodeChan = nil
		a.mu.Unlock()
		return api.WhatsAppPairResponse{}, fmt.Errorf("prepare WhatsApp QR pairing: %w", qrErr)
	}

	go a.consumeQR(qrCtx, qrChan)

	if err := a.client.ConnectContext(qrCtx); err != nil {
		cancel()
		a.pairingActive = false
		a.pairingCodeChan = nil
		a.mu.Unlock()
		return api.WhatsAppPairResponse{}, fmt.Errorf("connect WhatsApp transport: %w", err)
	}
	a.mu.Unlock()

	select {
	case code := <-codeChan:
		a.mu.Lock()
		timeout := int(time.Until(a.currentQRExpires).Seconds())
		if timeout < 1 {
			timeout = 20
		}
		a.mu.Unlock()
		return api.WhatsAppPairResponse{
			Status:         "pairing",
			QRCode:         code,
			TimeoutSeconds: timeout,
		}, nil
	case <-ctx.Done():
		return api.WhatsAppPairResponse{}, ctx.Err()
	case <-time.After(15 * time.Second):
		return api.WhatsAppPairResponse{}, errors.New("timeout waiting for WhatsApp QR code")
	}
}

func (a *Adapter) CancelPair(ctx context.Context) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.qrCancel != nil {
		a.qrCancel()
		a.qrCancel = nil
	}
	a.pairingActive = false
	a.currentQRCode = ""
	a.pairingCodeChan = nil
	if a.client != nil && !a.client.IsLoggedIn() && a.client.IsConnected() {
		a.client.Disconnect()
	}
	a.logger.Info("WhatsApp pairing cancelled", "event", "whatsapp_pairing_cancelled")
	return nil
}

func (a *Adapter) Logout(ctx context.Context) error {
	if a == nil {
		return errors.New("WhatsApp adapter is not initialized")
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.qrCancel != nil {
		a.qrCancel()
		a.qrCancel = nil
	}
	a.pairingActive = false
	a.currentQRCode = ""
	a.pairingCodeChan = nil

	if a.client != nil {
		if a.client.IsLoggedIn() {
			if err := a.client.Logout(ctx); err != nil {
				a.logger.Warn("WhatsApp server logout failed, forcing disconnect and session deletion", "error", err.Error())
				a.client.Disconnect()
				if a.client.Store != nil {
					_ = a.client.Store.Delete(ctx)
				}
			}
		} else if a.client.IsConnected() {
			a.client.Disconnect()
		}
	}

	if a.container != nil {
		device, err := a.container.GetFirstDevice(ctx)
		if err != nil {
			return fmt.Errorf("reset WhatsApp session device: %w", err)
		}
		newClient := whatsmeow.NewClient(device, nil)
		disablePlaintextPersistence(newClient)
		newClient.AddEventHandler(a.handleEvent)
		a.client = newClient
	}

	a.logger.Info("WhatsApp session logged out", "event", "whatsapp_logged_out")
	return nil
}

func (a *Adapter) GetJoinedGroups(ctx context.Context) ([]api.WhatsAppGroup, error) {
	if a == nil {
		return nil, errors.New("WhatsApp adapter is not initialized")
	}
	a.mu.Lock()
	client := a.client
	a.mu.Unlock()

	if client == nil {
		return nil, errors.New("WhatsApp client is not initialized")
	}
	if !client.IsLoggedIn() || !client.IsConnected() {
		return nil, errors.New("WhatsApp client is not connected")
	}

	groups, err := client.GetJoinedGroups(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]api.WhatsAppGroup, 0, len(groups))
	for _, g := range groups {
		if g == nil {
			continue
		}
		result = append(result, api.WhatsAppGroup{
			JID:  g.JID.String(),
			Name: g.Name,
		})
	}
	return result, nil
}

func (a *Adapter) UpdateConfig(cfg *config.Config) error {
	if a == nil || cfg == nil {
		return nil
	}
	groupJIDs := make(map[string]string)
	for alias, endpoint := range cfg.Endpoints {
		if endpoint.Transport == config.TransportWhatsApp {
			groupJIDs[alias] = endpoint.RemoteID
		}
	}

	a.mu.Lock()
	var hasher *identity.Hasher
	if a.normalizer != nil {
		hasher = a.normalizer.hasher
	}
	a.mu.Unlock()

	normalizer, err := NewNormalizer(groupJIDs, hasher, cfg.Identity.UsernameMode)
	if err != nil {
		return fmt.Errorf("create normalizer for updated config: %w", err)
	}
	targets, err := outgoingTargets(groupJIDs)
	if err != nil {
		return fmt.Errorf("resolve targets for updated config: %w", err)
	}

	a.mu.Lock()
	a.normalizer = normalizer
	a.targets = targets
	a.mediaEnabled = cfg.Media.Enabled
	a.mediaMaxBytes = uint64(cfg.Media.MaxSizeMB) * 1024 * 1024
	a.recoveryEnabled = cfg.Recovery.Enabled
	a.recoveryMaxAge = time.Duration(cfg.Recovery.MaxAgeHours) * time.Hour
	a.recoveryMaxCount = cfg.Recovery.MaxMessagesPerGroup
	a.mu.Unlock()

	return nil
}

func (a *Adapter) handleEvent(raw any) {
	switch evt := raw.(type) {
	case *events.Message:
		a.mu.Lock()
		normalizer := a.normalizer
		mediaEnabled := a.mediaEnabled
		mediaMaxBytes := a.mediaMaxBytes
		recoveryMaxAge := a.recoveryMaxAge
		client := a.client
		a.mu.Unlock()

		if recoveryMaxAge > 0 && !evt.Info.Timestamp.IsZero() && time.Since(evt.Info.Timestamp) > recoveryMaxAge {
			return
		}
		if normalizer == nil || client == nil {
			return
		}
		if !evt.Info.MessageSource.Sender.IsEmpty() {
			a.pcache.Add(evt.Info.ID, evt.Info.MessageSource.Sender.ToNonAD().String())
		}
		incoming, ok := normalizer.NormalizeMessage(evt, mediaEnabled, mediaMaxBytes, client.Download, client.DecryptPollVote, a.getContactInfo)
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
		a.mu.Lock()
		normalizer := a.normalizer
		mediaEnabled := a.mediaEnabled
		mediaMaxBytes := a.mediaMaxBytes
		recoveryEnabled := a.recoveryEnabled
		recoveryMaxAge := a.recoveryMaxAge
		recoveryMaxCount := a.recoveryMaxCount
		client := a.client
		a.mu.Unlock()

		if !recoveryEnabled || evt.Data == nil || normalizer == nil || client == nil {
			return
		}
		for _, conv := range evt.Data.GetConversations() {
			chatJID, err := types.ParseJID(conv.GetID())
			if err != nil || chatJID.Server != types.GroupServer {
				continue
			}
			chatJID = chatJID.ToNonAD()
			if _, ok := normalizer.endpoints[chatJID.String()]; !ok {
				continue
			}
			count := 0
			for _, historyMsg := range conv.GetMessages() {
				if recoveryMaxCount > 0 && count >= recoveryMaxCount {
					break
				}
				webMsg := historyMsg.GetMessage()
				if webMsg == nil {
					continue
				}
				parsed, err := client.ParseWebMessage(chatJID, webMsg)
				if err != nil || parsed == nil {
					continue
				}
				if recoveryMaxAge > 0 && !parsed.Info.Timestamp.IsZero() && time.Since(parsed.Info.Timestamp) > recoveryMaxAge {
					continue
				}
				if !parsed.Info.MessageSource.Sender.IsEmpty() {
					a.pcache.Add(parsed.Info.ID, parsed.Info.MessageSource.Sender.ToNonAD().String())
				}
				incoming, ok := normalizer.NormalizeMessage(parsed, mediaEnabled, mediaMaxBytes, client.Download, client.DecryptPollVote, a.getContactInfo)
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
	case *events.LoggedOut:
		a.logger.Warn("WhatsApp logged out", "event", "whatsapp_logged_out")
	}
}

func (a *Adapter) consumeQR(qrCtx context.Context, items <-chan whatsmeow.QRChannelItem) {
	for {
		select {
		case <-qrCtx.Done():
			a.mu.Lock()
			a.pairingActive = false
			a.currentQRCode = ""
			a.pairingCodeChan = nil
			a.mu.Unlock()
			return
		case item, ok := <-items:
			if !ok {
				a.mu.Lock()
				a.pairingActive = false
				a.currentQRCode = ""
				a.pairingCodeChan = nil
				a.mu.Unlock()
				return
			}
			switch item.Event {
			case whatsmeow.QRChannelEventCode:
				a.mu.Lock()
				a.currentQRCode = item.Code
				timeout := item.Timeout
				if timeout <= 0 {
					timeout = 20 * time.Second
				}
				a.currentQRExpires = time.Now().Add(timeout)
				a.pairingActive = true
				if a.pairingCodeChan != nil {
					select {
					case a.pairingCodeChan <- item.Code:
					default:
					}
				}
				a.mu.Unlock()
				a.logger.Info("WhatsApp QR code generated", "event", "whatsapp_qr_generated")
				if a.enableTerminalQR && a.qrOut != nil {
					fmt.Fprintln(a.qrOut, "\nWhatsApp pairing required. In WhatsApp open Linked devices, choose Link a device, and scan this QR code:")
					qrterminal.GenerateHalfBlock(item.Code, qrterminal.L, a.qrOut)
				}
			case whatsmeow.QRChannelSuccess.Event:
				a.mu.Lock()
				a.pairingActive = false
				a.currentQRCode = ""
				a.pairingCodeChan = nil
				a.mu.Unlock()
				a.logger.Info("WhatsApp pairing complete", "event", "whatsapp_pairing_success")
				if a.enableTerminalQR && a.qrOut != nil {
					fmt.Fprintln(a.qrOut, "WhatsApp pairing complete. The linked session is stored in whatsapp.db.")
				}
			case "timeout":
				a.mu.Lock()
				a.pairingActive = false
				a.currentQRCode = ""
				a.pairingCodeChan = nil
				a.mu.Unlock()
				a.logger.Warn("WhatsApp QR pairing timed out", "event", "whatsapp_pairing_timeout")
			case "error":
				a.mu.Lock()
				a.pairingActive = false
				a.currentQRCode = ""
				a.pairingCodeChan = nil
				a.mu.Unlock()
				safelog.Error(a.logger, "WhatsApp QR pairing failed", "whatsapp_pairing", item.Error)
			default:
				a.mu.Lock()
				a.pairingActive = false
				a.currentQRCode = ""
				a.pairingCodeChan = nil
				a.mu.Unlock()
				a.logger.Error("WhatsApp QR pairing failed",
					"event", "whatsapp_pairing_failed",
					"reason", item.Event,
				)
			}
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
