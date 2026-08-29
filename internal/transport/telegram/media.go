package telegram

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	telegrambot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/vm75/message-sync/internal/transport"
)

const telegramHostedDownloadMax = 20 * 1024 * 1024

type telegramMedia struct {
	kind   string
	fileID string
	size   uint64
}

func hasTelegramMedia(message *models.Message) bool {
	_, ok := telegramMediaDescriptor(message)
	return ok
}

func telegramMediaDescriptor(message *models.Message) (telegramMedia, bool) {
	if message == nil {
		return telegramMedia{}, false
	}
	if message.Sticker != nil && strings.TrimSpace(message.Sticker.FileID) != "" {
		return telegramMedia{kind: "sticker", fileID: message.Sticker.FileID, size: positiveSize(int64(message.Sticker.FileSize))}, true
	}
	if len(message.Photo) > 0 {
		photo := message.Photo[len(message.Photo)-1]
		if strings.TrimSpace(photo.FileID) != "" {
			return telegramMedia{kind: "image", fileID: photo.FileID, size: positiveSize(int64(photo.FileSize))}, true
		}
	}
	if message.Video != nil && strings.TrimSpace(message.Video.FileID) != "" {
		return telegramMedia{kind: "video", fileID: message.Video.FileID, size: positiveSize(message.Video.FileSize)}, true
	}
	if message.Voice != nil && strings.TrimSpace(message.Voice.FileID) != "" {
		return telegramMedia{kind: "audio", fileID: message.Voice.FileID, size: positiveSize(message.Voice.FileSize)}, true
	}
	if message.Audio != nil && strings.TrimSpace(message.Audio.FileID) != "" {
		return telegramMedia{kind: "audio", fileID: message.Audio.FileID, size: positiveSize(message.Audio.FileSize)}, true
	}
	if message.Document != nil && strings.TrimSpace(message.Document.FileID) != "" {
		return telegramMedia{kind: "document", fileID: message.Document.FileID, size: positiveSize(message.Document.FileSize)}, true
	}
	return telegramMedia{}, false
}

func positiveSize(size int64) uint64 {
	if size <= 0 {
		return 0
	}
	return uint64(size)
}

func (a *Adapter) withTelegramMedia(incoming transport.Incoming, message *models.Message) (transport.Incoming, bool) {
	media, ok := telegramMediaDescriptor(message)
	if !ok {
		return incoming, true
	}

	a.mu.RLock()
	enabled := a.mediaEnabled
	configuredMax := a.mediaMaxBytes
	a.mu.RUnlock()
	if !enabled {
		// Preserve a caption as an ordinary text message when media forwarding
		// is disabled, but do not synthesize an empty canonical message.
		return incoming, strings.TrimSpace(incoming.Text) != ""
	}

	maxBytes := telegramHostedDownloadMax
	if configuredMax > 0 && configuredMax < maxBytes {
		maxBytes = configuredMax
	}
	incoming.Kind = media.kind
	incoming.MediaLoader = func(ctx context.Context) ([]byte, error) {
		if media.size > 0 && media.size > maxBytes {
			return nil, errors.New("Telegram media exceeds configured or hosted Bot API download limit")
		}
		return a.downloadTelegramMedia(ctx, media.fileID, maxBytes)
	}
	return incoming, true
}

func (a *Adapter) downloadTelegramMedia(ctx context.Context, fileID string, maxBytes uint64) ([]byte, error) {
	if a == nil {
		return nil, errors.New("Telegram transport is not initialized")
	}
	api, ok := a.client.(telegramAPI)
	if !ok || api == nil {
		return nil, errors.New("Telegram Bot API is unavailable")
	}
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return nil, errors.New("Telegram media file id is unavailable")
	}

	var file *models.File
	if err := a.callWithRetry(ctx, func() error {
		var callErr error
		file, callErr = api.GetFile(ctx, &telegrambot.GetFileParams{FileID: fileID})
		return callErr
	}); err != nil {
		return nil, errors.New("get Telegram media file")
	}
	if file == nil || strings.TrimSpace(file.FilePath) == "" {
		return nil, errors.New("Telegram media file path is unavailable")
	}
	if maxBytes > 0 && file.FileSize > 0 && uint64(file.FileSize) > maxBytes {
		return nil, errors.New("Telegram media exceeds configured or hosted Bot API download limit")
	}

	a.mu.RLock()
	token := a.token
	httpClient := a.httpClient
	a.mu.RUnlock()
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("Telegram media download credential is unavailable")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	fileURL := "https://api.telegram.org/file/bot" + token + "/" + strings.TrimPrefix(file.FilePath, "/")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, errors.New("prepare Telegram media download")
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, errors.New("download Telegram media")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, errors.New("download Telegram media")
	}
	if maxBytes > 0 && response.ContentLength > int64(maxBytes) {
		return nil, errors.New("Telegram media exceeds configured or hosted Bot API download limit")
	}

	reader := io.Reader(response.Body)
	if maxBytes > 0 {
		reader = io.LimitReader(response.Body, int64(maxBytes)+1)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, errors.New("read Telegram media")
	}
	if maxBytes > 0 && uint64(len(data)) > maxBytes {
		return nil, errors.New("Telegram media exceeds configured or hosted Bot API download limit")
	}
	return data, nil
}
