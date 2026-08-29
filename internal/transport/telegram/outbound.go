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
