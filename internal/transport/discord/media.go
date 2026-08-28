package discord

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/transport"
)

func (a *Adapter) withDiscordMedia(incoming transport.Incoming, message *discordgo.Message) (transport.Incoming, bool) {
	if a == nil || message == nil || len(message.Attachments) == 0 {
		return incoming, true
	}
	a.mu.RLock()
	enabled := a.mediaEnabled
	maxBytes := a.mediaMaxBytes
	a.mu.RUnlock()

	if !enabled {
		// Preserve a textual caption when media forwarding is disabled, but do
		// not synthesize a canonical message for an attachment-only event.
		return incoming, strings.TrimSpace(incoming.Text) != ""
	}

	attachment := message.Attachments[0]
	if attachment == nil || strings.TrimSpace(attachment.URL) == "" {
		return incoming, strings.TrimSpace(incoming.Text) != ""
	}
	kind := discordAttachmentKind(attachment.ContentType)
	url := attachment.URL
	size := attachment.Size

	incoming.Kind = kind
	incoming.MediaLoader = func(ctx context.Context) ([]byte, error) {
		if maxBytes > 0 && size > 0 && uint64(size) > maxBytes {
			return nil, errors.New("Discord attachment exceeds configured size limit")
		}
		return downloadDiscordAttachment(ctx, url, maxBytes)
	}
	return incoming, true
}

func discordAttachmentKind(contentType string) string {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	switch {
	case strings.HasPrefix(contentType, "image/"):
		return "image"
	case strings.HasPrefix(contentType, "video/"):
		return "video"
	case strings.HasPrefix(contentType, "audio/"):
		return "audio"
	default:
		return "document"
	}
}

func downloadDiscordAttachment(ctx context.Context, url string, maxBytes uint64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errors.New("prepare Discord attachment download")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, errors.New("download Discord attachment")
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, errors.New("download Discord attachment")
	}
	if maxBytes > 0 && response.ContentLength > int64(maxBytes) {
		return nil, errors.New("Discord attachment exceeds configured size limit")
	}

	reader := io.Reader(response.Body)
	if maxBytes > 0 {
		reader = io.LimitReader(response.Body, int64(maxBytes)+1)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, errors.New("read Discord attachment")
	}
	if maxBytes > 0 && uint64(len(data)) > maxBytes {
		return nil, errors.New("Discord attachment exceeds configured size limit")
	}
	return data, nil
}
