package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	telegrambot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/vm75/message-sync/internal/transport"
)

const (
	telegramTextLimit          = 4096
	telegramCaptionLimit       = 1024
	telegramPhotoUploadMax     = 10 * 1024 * 1024
	telegramGeneralUploadMax   = 50 * 1024 * 1024
	telegramStickerUploadMax   = 512 * 1024
	telegramMaxAPIAttempts     = 3
	telegramMaxRateLimitWait   = 30 * time.Second
	telegramDefaultRetryDelay  = 250 * time.Millisecond
	telegramReplyFallbackLimit = 400
)

type telegramAPI interface {
	SendMessage(context.Context, *telegrambot.SendMessageParams) (*models.Message, error)
	SendPhoto(context.Context, *telegrambot.SendPhotoParams) (*models.Message, error)
	SendVideo(context.Context, *telegrambot.SendVideoParams) (*models.Message, error)
	SendAudio(context.Context, *telegrambot.SendAudioParams) (*models.Message, error)
	SendVoice(context.Context, *telegrambot.SendVoiceParams) (*models.Message, error)
	SendDocument(context.Context, *telegrambot.SendDocumentParams) (*models.Message, error)
	SendSticker(context.Context, *telegrambot.SendStickerParams) (*models.Message, error)
	SetMessageReaction(context.Context, *telegrambot.SetMessageReactionParams) (bool, error)
	EditMessageText(context.Context, *telegrambot.EditMessageTextParams) (*models.Message, error)
	EditMessageCaption(context.Context, *telegrambot.EditMessageCaptionParams) (*models.Message, error)
	DeleteMessage(context.Context, *telegrambot.DeleteMessageParams) (bool, error)
	GetFile(context.Context, *telegrambot.GetFileParams) (*models.File, error)
}

type messageKindKey struct {
	endpoint transport.EndpointID
	message  string
}

func (a *Adapter) Send(ctx context.Context, outgoing transport.Outgoing) (transport.MessageRef, error) {
	if a == nil {
		return transport.MessageRef{}, errors.New("Telegram transport is not initialized")
	}
	if ctx == nil {
		return transport.MessageRef{}, errors.New("context is required")
	}
	if outgoing.AttributionOnly {
		// Telegram captions cover images/video/audio/documents. Stickers have no
		// caption, so the normal sticker send emits its own transient companion.
		return transport.MessageRef{Endpoint: outgoing.Endpoint}, nil
	}

	chatID, api, mediaEnabled, mediaMaxBytes, ok := a.outboundState(outgoing.Endpoint)
	if !ok {
		return transport.MessageRef{}, errors.New("unknown Telegram endpoint")
	}
	if api == nil {
		return transport.MessageRef{}, errors.New("Telegram Bot API is unavailable")
	}

	reply, err := telegramReplyParameters(outgoing.ReplyTo)
	if err != nil {
		return transport.MessageRef{}, err
	}
	content := telegramOutgoingText(outgoing)
	if outgoing.ReplyFallback {
		content = telegramReplyFallback(outgoing.OriginEndpoint, outgoing.QuotedText, content)
	}

	kind := strings.TrimSpace(outgoing.Kind)
	if kind == "" {
		kind = "text"
	}

	var message *models.Message
	switch kind {
	case "text":
		content = truncateTelegramText(content, telegramTextLimit)
		if strings.TrimSpace(content) == "" {
			return transport.MessageRef{}, errors.New("outgoing Telegram text is required")
		}
		err = a.callWithRetry(ctx, func() error {
			var callErr error
			message, callErr = api.SendMessage(ctx, &telegrambot.SendMessageParams{
				ChatID:          chatID,
				Text:            content,
				ReplyParameters: reply,
			})
			return callErr
		})
		if err != nil {
			return transport.MessageRef{}, errors.New("send Telegram text")
		}
	case "image", "video", "audio", "document", "sticker":
		if !mediaEnabled {
			return transport.MessageRef{}, errors.New("Telegram media forwarding is disabled")
		}
		if len(outgoing.MediaBytes) == 0 {
			return transport.MessageRef{}, errors.New("outgoing Telegram media bytes are required")
		}
		limit := telegramUploadLimit(kind, mediaMaxBytes)
		if limit > 0 && uint64(len(outgoing.MediaBytes)) > limit {
			return transport.MessageRef{}, errors.New("Telegram media exceeds configured or hosted Bot API size limit")
		}
		message, err = a.sendTelegramMedia(ctx, api, chatID, kind, outgoing.MediaBytes, truncateTelegramText(content, telegramCaptionLimit), reply)
		if err != nil {
			return transport.MessageRef{}, err
		}
	default:
		return transport.MessageRef{}, errors.New("unsupported Telegram outgoing message kind")
	}

	if message == nil || message.ID <= 0 {
		return transport.MessageRef{}, errors.New("Telegram Bot API returned an invalid message id")
	}
	remoteID := strconv.Itoa(message.ID)
	a.rememberMessageKind(outgoing.Endpoint, remoteID, kind)
	return transport.MessageRef{
		Endpoint:        outgoing.Endpoint,
		RemoteMessageID: remoteID,
		IsTargetFromMe:  true,
	}, nil
}

func (a *Adapter) sendTelegramMedia(ctx context.Context, api telegramAPI, chatID int64, kind string, data []byte, caption string, reply *models.ReplyParameters) (*models.Message, error) {
	var (
		message *models.Message
		err     error
	)

	switch kind {
	case "image":
		err = a.callWithRetry(ctx, func() error {
			var callErr error
			message, callErr = api.SendPhoto(ctx, &telegrambot.SendPhotoParams{
				ChatID: chatID,
				Photo: &models.InputFileUpload{
					Filename: "image.jpg",
					Data:     bytes.NewReader(data),
				},
				Caption:         caption,
				ReplyParameters: reply,
			})
			return callErr
		})
	case "video":
		err = a.callWithRetry(ctx, func() error {
			var callErr error
			message, callErr = api.SendVideo(ctx, &telegrambot.SendVideoParams{
				ChatID: chatID,
				Video: &models.InputFileUpload{
					Filename: "video.mp4",
					Data:     bytes.NewReader(data),
				},
				Caption:         caption,
				ReplyParameters: reply,
			})
			return callErr
		})
	case "audio":
		if isOggAudio(data) {
			err = a.callWithRetry(ctx, func() error {
				var callErr error
				message, callErr = api.SendVoice(ctx, &telegrambot.SendVoiceParams{
					ChatID: chatID,
					Voice: &models.InputFileUpload{
						Filename: "voice.ogg",
						Data:     bytes.NewReader(data),
					},
					Caption:         caption,
					ReplyParameters: reply,
				})
				return callErr
			})
		} else {
			err = a.callWithRetry(ctx, func() error {
				var callErr error
				message, callErr = api.SendAudio(ctx, &telegrambot.SendAudioParams{
					ChatID: chatID,
					Audio: &models.InputFileUpload{
						Filename: telegramAudioFilename(data),
						Data:     bytes.NewReader(data),
					},
					Caption:         caption,
					ReplyParameters: reply,
				})
				return callErr
			})
		}
	case "document":
		err = a.callWithRetry(ctx, func() error {
			var callErr error
			message, callErr = api.SendDocument(ctx, &telegrambot.SendDocumentParams{
				ChatID: chatID,
				Document: &models.InputFileUpload{
					Filename: "document.bin",
					Data:     bytes.NewReader(data),
				},
				Caption:         caption,
				ReplyParameters: reply,
			})
			return callErr
		})
	case "sticker":
		filename, filenameErr := telegramStickerFilename(data)
		if filenameErr != nil {
			return nil, filenameErr
		}
		if formatLimit := telegramStickerFormatLimit(filename); uint64(len(data)) > formatLimit {
			return nil, errors.New("Telegram sticker exceeds hosted Bot API format size limit")
		}
		err = a.callWithRetry(ctx, func() error {
			var callErr error
			message, callErr = api.SendSticker(ctx, &telegrambot.SendStickerParams{
				ChatID: chatID,
				Sticker: &models.InputFileUpload{
					Filename: filename,
					Data:     bytes.NewReader(data),
				},
				ReplyParameters: reply,
			})
			return callErr
		})
		if err == nil {
			// Telegram stickers have no caption. Emit attribution only after the
			// sticker succeeds so a failed sticker cannot leave a duplicate
			// companion on router retry. A companion failure does not invalidate
			// the already-created canonical destination copy.
			companion := strings.TrimSpace(caption)
			if companion == "" {
				companion = "[sticker]"
			} else if strings.HasSuffix(companion, ":") {
				companion += " [sticker]"
			}
			companion = truncateTelegramText(companion, telegramTextLimit)
			_ = a.callWithRetry(ctx, func() error {
				_, sendErr := api.SendMessage(ctx, &telegrambot.SendMessageParams{
					ChatID: chatID,
					Text:   companion,
				})
				return sendErr
			})
		}
	}
	if err != nil {
		return nil, errors.New("send Telegram media")
	}
	return message, nil
}

func (a *Adapter) React(ctx context.Context, reaction transport.Reaction) error {
	if a == nil {
		return errors.New("Telegram transport is not initialized")
	}
	if ctx == nil {
		return errors.New("context is required")
	}
	chatID, api, _, _, ok := a.outboundState(reaction.Endpoint)
	if !ok {
		return errors.New("unknown Telegram endpoint")
	}
	if api == nil {
		return errors.New("Telegram Bot API is unavailable")
	}
	messageID, err := telegramMessageID(reaction.TargetRemoteID)
	if err != nil {
		return errors.New("Telegram reaction target is invalid")
	}

	var reactions []models.ReactionType
	emoji := strings.TrimSpace(reaction.Emoji)
	if emoji != "" {
		if strings.ContainsAny(emoji, " \t\r\n") || utf8.RuneCountInString(emoji) > 16 {
			return errors.New("unsupported Telegram reaction value")
		}
		reactions = []models.ReactionType{{
			Type: models.ReactionTypeTypeEmoji,
			ReactionTypeEmoji: &models.ReactionTypeEmoji{
				Type:  models.ReactionTypeTypeEmoji,
				Emoji: emoji,
			},
		}}
	}

	var applied bool
	err = a.callWithRetry(ctx, func() error {
		var callErr error
		applied, callErr = api.SetMessageReaction(ctx, &telegrambot.SetMessageReactionParams{
			ChatID:    chatID,
			MessageID: messageID,
			Reaction:  reactions,
		})
		return callErr
	})
	if err != nil || !applied {
		return errors.New("set Telegram reaction")
	}
	return nil
}

func (a *Adapter) Edit(ctx context.Context, ref transport.MessageRef, text string) error {
	if a == nil {
		return errors.New("Telegram transport is not initialized")
	}
	if ctx == nil {
		return errors.New("context is required")
	}
	chatID, api, _, _, ok := a.outboundState(ref.Endpoint)
	if !ok {
		return errors.New("unknown Telegram endpoint")
	}
	if api == nil {
		return errors.New("Telegram Bot API is unavailable")
	}
	messageID, err := telegramMessageID(ref.RemoteMessageID)
	if err != nil {
		return errors.New("Telegram edit target is invalid")
	}
	content := telegramEditContent(text)
	if strings.TrimSpace(content) == "" {
		return errors.New("outgoing Telegram edit text is required")
	}

	kind, known := a.messageKind(ref.Endpoint, ref.RemoteMessageID)
	if known && kind == "sticker" {
		return errors.New("Telegram sticker messages do not support text edits")
	}
	if known && kind != "text" {
		return a.editTelegramCaption(ctx, api, chatID, messageID, content)
	}
	if known {
		return a.editTelegramText(ctx, api, chatID, messageID, content)
	}

	// Message kind is intentionally not persisted. After restart, try the
	// text form first and fall back to caption only for a Bot API bad request.
	err = a.editTelegramTextRaw(ctx, api, chatID, messageID, content)
	if err == nil || isTelegramNotModified(err) {
		return nil
	}
	if !errors.Is(err, telegrambot.ErrorBadRequest) {
		return errors.New("edit Telegram message")
	}
	err = a.editTelegramCaptionRaw(ctx, api, chatID, messageID, content)
	if err == nil || isTelegramNotModified(err) {
		return nil
	}
	return errors.New("edit Telegram message")
}

func (a *Adapter) editTelegramText(ctx context.Context, api telegramAPI, chatID int64, messageID int, content string) error {
	err := a.editTelegramTextRaw(ctx, api, chatID, messageID, content)
	if err == nil || isTelegramNotModified(err) {
		return nil
	}
	return errors.New("edit Telegram text")
}

func (a *Adapter) editTelegramTextRaw(ctx context.Context, api telegramAPI, chatID int64, messageID int, content string) error {
	content = truncateTelegramText(content, telegramTextLimit)
	return a.callWithRetry(ctx, func() error {
		_, err := api.EditMessageText(ctx, &telegrambot.EditMessageTextParams{
			ChatID:    chatID,
			MessageID: messageID,
			Text:      content,
		})
		return err
	})
}

