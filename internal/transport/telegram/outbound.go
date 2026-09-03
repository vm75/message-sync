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
	SendPoll(context.Context, *telegrambot.SendPollParams) (*models.Message, error)
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
	threadID, err := telegramChildThreadID(outgoing.ChildScope)
	if err != nil {
		return transport.MessageRef{}, err
	}

	reply, err := telegramReplyParameters(outgoing.ReplyTo)
	if err != nil {
		return transport.MessageRef{}, err
	}

	kind := strings.TrimSpace(outgoing.Kind)
	var message *models.Message
	if kind == "" {
		kind = "text"
	}
	if kind == "poll" {
		if params, ok := telegramNativePoll(chatID, outgoing.SourceText, outgoing.PollOptions, outgoing.PollSelectableCount, outgoing.PollDurationHours, reply); ok {
			params.MessageThreadID = threadID
			err = a.callWithRetry(ctx, func() error {
				var callErr error
				message, callErr = api.SendPoll(ctx, params)
				return callErr
			})
			if err != nil {
				return transport.MessageRef{}, errors.New("send Telegram poll")
			}
			if message == nil || message.ID <= 0 || message.Poll == nil || strings.TrimSpace(message.Poll.ID) == "" {
				return transport.MessageRef{}, errors.New("Telegram poll response was incomplete")
			}
			remoteID := strconv.Itoa(message.ID)
			a.rememberMessageKind(outgoing.Endpoint, remoteID, "poll")
			return transport.MessageRef{
				Endpoint:          outgoing.Endpoint,
				RemoteMessageID:   remoteID,
				IsTargetFromMe:    true,
				Provider:          a.pollProviderNamespace(),
				ProviderReference: message.Poll.ID,
				ChildScope:        outgoing.ChildScope,
			}, nil
		}
		pollText, pollErr := telegramPollText(outgoing.SourceText, outgoing.PollOptions, outgoing.PollSelectableCount)
		if pollErr != nil {
			return transport.MessageRef{}, pollErr
		}
		outgoing.SourceText = pollText
		kind = "text"
	}

	content := telegramOutgoingText(outgoing)
	if outgoing.ReplyFallback {
		content = telegramReplyFallback(outgoing.OriginEndpoint, outgoing.QuotedText, content)
	}

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
				MessageThreadID: threadID,
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
		message, err = a.sendTelegramMedia(ctx, api, chatID, kind, outgoing.MediaBytes, truncateTelegramText(content, telegramCaptionLimit), reply, threadID)
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
		ChildScope:      outgoing.ChildScope,
	}, nil
}

func telegramNativePoll(chatID int64, question string, options []string, selectableCount, durationHours int, reply *models.ReplyParameters) (*telegrambot.SendPollParams, bool) {
	question = strings.TrimSpace(question)
	if utf8.RuneCountInString(question) < 1 || utf8.RuneCountInString(question) > 300 || len(options) < 2 || len(options) > 10 || (selectableCount != 1 && selectableCount != len(options)) || durationHours < 0 {
		return nil, false
	}
	if durationHours > 0 && (durationHours*3600 < 5 || durationHours*3600 > 600) {
		return nil, false
	}
	input := make([]models.InputPollOption, 0, len(options))
	for _, option := range options {
		option = strings.TrimSpace(option)
		if utf8.RuneCountInString(option) < 1 || utf8.RuneCountInString(option) > 100 {
			return nil, false
		}
		input = append(input, models.InputPollOption{Text: option})
	}
	params := &telegrambot.SendPollParams{ChatID: chatID, Question: question, Options: input, IsAnonymous: boolPtr(true), AllowsMultipleAnswers: selectableCount > 1, ReplyParameters: reply}
	if durationHours > 0 {
		params.OpenPeriod = durationHours * 3600
	}
	return params, true
}

func boolPtr(value bool) *bool { return &value }

func (a *Adapter) sendTelegramMedia(ctx context.Context, api telegramAPI, chatID int64, kind string, data []byte, caption string, reply *models.ReplyParameters, threadID int) (*models.Message, error) {
	var (
		message *models.Message
		err     error
	)

	switch kind {
	case "image":
		err = a.callWithRetry(ctx, func() error {
			var callErr error
			message, callErr = api.SendPhoto(ctx, &telegrambot.SendPhotoParams{
				ChatID:          chatID,
				MessageThreadID: threadID,
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
				ChatID:          chatID,
				MessageThreadID: threadID,
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
					ChatID:          chatID,
					MessageThreadID: threadID,
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
					ChatID:          chatID,
					MessageThreadID: threadID,
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
				ChatID:          chatID,
				MessageThreadID: threadID,
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
				ChatID:          chatID,
				MessageThreadID: threadID,
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
					ChatID:          chatID,
					MessageThreadID: threadID,
					Text:            companion,
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

func (a *Adapter) editTelegramCaption(ctx context.Context, api telegramAPI, chatID int64, messageID int, content string) error {
	err := a.editTelegramCaptionRaw(ctx, api, chatID, messageID, content)
	if err == nil || isTelegramNotModified(err) {
		return nil
	}
	return errors.New("edit Telegram caption")
}

func (a *Adapter) editTelegramCaptionRaw(ctx context.Context, api telegramAPI, chatID int64, messageID int, content string) error {
	content = truncateTelegramText(content, telegramCaptionLimit)
	return a.callWithRetry(ctx, func() error {
		_, err := api.EditMessageCaption(ctx, &telegrambot.EditMessageCaptionParams{
			ChatID:    chatID,
			MessageID: messageID,
			Caption:   content,
		})
		return err
	})
}

func (a *Adapter) Delete(ctx context.Context, ref transport.MessageRef) error {
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
		return errors.New("Telegram delete target is invalid")
	}

	var deleted bool
	var lastDeleteErr error
	err = a.callWithRetry(ctx, func() error {
		var callErr error
		deleted, callErr = api.DeleteMessage(ctx, &telegrambot.DeleteMessageParams{
			ChatID:    chatID,
			MessageID: messageID,
		})
		lastDeleteErr = callErr
		return callErr
	})
	if err != nil {
		if isTelegramDeleteMissing(lastDeleteErr) {
			a.forgetMessageKind(ref.Endpoint, ref.RemoteMessageID)
			return nil
		}
		return err
	}
	if !deleted {
		return errors.New("delete Telegram message")
	}
	a.forgetMessageKind(ref.Endpoint, ref.RemoteMessageID)
	return nil
}

func (a *Adapter) outboundState(endpoint transport.EndpointID) (int64, telegramAPI, bool, uint64, bool) {
	a.mu.RLock()
	normalizer := a.normalizer
	mediaEnabled := a.mediaEnabled
	mediaMaxBytes := a.mediaMaxBytes
	client := a.client
	a.mu.RUnlock()
	if normalizer == nil {
		return 0, nil, mediaEnabled, mediaMaxBytes, false
	}
	chatID, ok := normalizer.chatID(endpoint)
	if !ok {
		return 0, nil, mediaEnabled, mediaMaxBytes, false
	}
	api, _ := client.(telegramAPI)
	return chatID, api, mediaEnabled, mediaMaxBytes, true
}

func (a *Adapter) callWithRetry(ctx context.Context, call func() error) error {
	for attempt := 0; attempt < telegramMaxAPIAttempts; attempt++ {
		err := call()
		if err == nil {
			return nil
		}
		var rateLimit *telegrambot.TooManyRequestsError
		if !errors.As(err, &rateLimit) || attempt == telegramMaxAPIAttempts-1 {
			return classifyTelegramFailure(err)
		}

		delay := time.Duration(rateLimit.RetryAfter) * time.Second
		if delay <= 0 {
			delay = telegramDefaultRetryDelay * time.Duration(1<<attempt)
		}
		if delay > telegramMaxRateLimitWait {
			return classifyTelegramFailure(err)
		}
		wait := a.retryWait
		if wait == nil {
			wait = waitTelegramRetry
		}
		if err := wait(ctx, delay); err != nil {
			return classifyTelegramFailure(err)
		}
	}
	return classifyTelegramFailure(errors.New("telegram API retry exhausted"))
}

func waitTelegramRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func telegramReplyParameters(ref *transport.MessageRef) (*models.ReplyParameters, error) {
	if ref == nil {
		return nil, nil
	}
	messageID, err := telegramMessageID(ref.RemoteMessageID)
	if err != nil {
		return nil, errors.New("Telegram reply target is invalid")
	}
	return &models.ReplyParameters{MessageID: messageID}, nil
}

func telegramChildThreadID(scope *transport.ChildScope) (int, error) {
	if scope == nil || strings.TrimSpace(scope.RemoteID) == "" {
		return 0, nil
	}
	threadID, err := strconv.Atoi(strings.TrimSpace(scope.RemoteID))
	if err != nil || threadID <= 0 {
		return 0, errors.New("Telegram child scope is invalid")
	}
	return threadID, nil
}

func telegramMessageID(value string) (int, error) {
	value = strings.TrimSpace(value)
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, errors.New("invalid Telegram message id")
	}
	return parsed, nil
}

func telegramOutgoingText(outgoing transport.Outgoing) string {
	label := telegramSenderLabel(outgoing.Sender)
	source := sanitizeTelegramMentions(outgoing.SourceText, outgoing.Mentions)
	if strings.TrimSpace(source) == "" && label == "" {
		return outgoing.Text
	}
	if label == "" {
		return source
	}
	if source == "" {
		return label + ":"
	}
	return label + ": " + source
}

func telegramSenderLabel(sender transport.Sender) string {
	label := sanitizeTelegramAttribution(sender.DisplayName)
	if label == "" {
		label = sanitizeTelegramAttribution(sender.OpaqueID)
	}
	return label
}

func sanitizeTelegramAttribution(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

func sanitizeTelegramMentions(content string, mentions []transport.Mention) string {
	for _, mention := range mentions {
		remoteID := strings.TrimSpace(mention.RemoteID)
		if remoteID == "" {
			continue
		}
		name := sanitizeTelegramAttribution(mention.Name)
		if name == "" || name == remoteID {
			name = "participant"
		}
		content = strings.ReplaceAll(content, "@"+remoteID, "@"+name)
	}
	return content
}

func telegramReplyFallback(origin transport.EndpointID, quotedText, content string) string {
	quote := strings.TrimSpace(quotedText)
	if quote == "" {
		quote = "message"
	}
	quote = strings.ReplaceAll(quote, "\r", " ")
	quote = strings.ReplaceAll(quote, "\n", " ")
	quote = truncateTelegramText(quote, telegramReplyFallbackLimit)
	label := string(origin)
	if label == "" {
		label = "source"
	}
	if content == "" {
		return fmt.Sprintf("reply to %s: %s", label, quote)
	}
	return fmt.Sprintf("reply to %s: %s\n\n%s", label, quote, content)
}

func telegramEditContent(text string) string {
	if !strings.HasPrefix(text, "*_") {
		return text
	}
	marker := strings.Index(text, "_*: ")
	if marker < 0 {
		return text
	}
	meta := text[2:marker]
	slash := strings.Index(meta, "/")
	if slash < 0 || slash == len(meta)-1 {
		return text[marker+4:]
	}
	label := strings.TrimSpace(meta[slash+1:])
	if open := strings.LastIndex(label, " ("); open >= 0 && strings.HasSuffix(label, ")") {
		label = strings.TrimSpace(label[open+2 : len(label)-1])
	}
	if looksLikePhoneNumber(label) {
		label = ""
	} else {
		label = sanitizeTelegramAttribution(label)
	}
	body := text[marker+4:]
	if label == "" {
		return body
	}
	return label + ": " + body
}

func looksLikePhoneNumber(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "u_") {
		return false
	}
	digits := 0
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case strings.ContainsRune("+-.() ", r):
		default:
			return false
		}
	}
	return digits >= 6
}

func truncateTelegramText(value string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	if limit == 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}

func telegramUploadLimit(kind string, configured uint64) uint64 {
	var hosted uint64 = telegramGeneralUploadMax
	switch kind {
	case "image":
		hosted = telegramPhotoUploadMax
	case "sticker":
		hosted = telegramStickerUploadMax
	}
	if configured > 0 && configured < hosted {
		return configured
	}
	return hosted
}

func telegramAudioFilename(data []byte) string {
	switch {
	case len(data) >= 3 && string(data[:3]) == "ID3":
		return "audio.mp3"
	case len(data) >= 12 && string(data[4:8]) == "ftyp":
		return "audio.m4a"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE":
		return "audio.wav"
	default:
		return "audio.bin"
	}
}

func isOggAudio(data []byte) bool {
	return len(data) >= 4 && string(data[:4]) == "OggS"
}

func telegramStickerFilename(data []byte) (string, error) {
	switch {
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "sticker.webp", nil
	case len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b:
		return "sticker.tgs", nil
	case len(data) >= 4 && bytes.Equal(data[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}):
		return "sticker.webm", nil
	default:
		return "", errors.New("unsupported Telegram sticker representation")
	}
}

func telegramStickerFormatLimit(filename string) uint64 {
	switch filename {
	case "sticker.tgs":
		return 64 * 1024
	case "sticker.webm":
		return 256 * 1024
	default:
		return telegramStickerUploadMax
	}
}

func isTelegramNotModified(err error) bool {
	return errors.Is(err, telegrambot.ErrorBadRequest) && strings.Contains(strings.ToLower(err.Error()), "message is not modified")
}

func isTelegramDeleteMissing(err error) bool {
	if errors.Is(err, telegrambot.ErrorNotFound) {
		return true
	}
	return errors.Is(err, telegrambot.ErrorBadRequest) && strings.Contains(strings.ToLower(err.Error()), "message to delete not found")
}

func (a *Adapter) rememberMessageKind(endpoint transport.EndpointID, remoteID, kind string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.messageKinds == nil {
		a.messageKinds = make(map[messageKindKey]string)
	}
	a.messageKinds[messageKindKey{endpoint: endpoint, message: remoteID}] = kind
}

func (a *Adapter) messageKind(endpoint transport.EndpointID, remoteID string) (string, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	kind, ok := a.messageKinds[messageKindKey{endpoint: endpoint, message: remoteID}]
	return kind, ok
}

func (a *Adapter) forgetMessageKind(endpoint transport.EndpointID, remoteID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.messageKinds, messageKindKey{endpoint: endpoint, message: remoteID})
}
