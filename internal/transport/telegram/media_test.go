package telegram

import (
	"testing"

	"github.com/go-telegram/bot/models"
)

func TestTelegramMediaDescriptorFormatMappings(t *testing.T) {
	tests := []struct {
		name     string
		message  *models.Message
		wantKind string
	}{
		{
			name:     "sticker",
			message:  &models.Message{Sticker: &models.Sticker{FileID: "sticker-file"}},
			wantKind: "sticker",
		},
		{
			name:     "voice note",
			message:  &models.Message{Voice: &models.Voice{FileID: "voice-file"}},
			wantKind: "audio",
		},
		{
			name:     "mp4 animation",
			message:  &models.Message{Animation: &models.Animation{FileID: "animation-file", MimeType: "video/mp4"}},
			wantKind: "video",
		},
		{
			name:     "non-mp4 animation",
			message:  &models.Message{Animation: &models.Animation{FileID: "animation-file", MimeType: "image/gif"}},
			wantKind: "document",
		},
		{
			name:     "video note",
			message:  &models.Message{VideoNote: &models.VideoNote{FileID: "video-note-file"}},
			wantKind: "video",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			media, ok := telegramMediaDescriptor(tc.message)
			if !ok {
				t.Fatal("Telegram media payload was not recognized")
			}
			if media.kind != tc.wantKind {
				t.Fatalf("Telegram media kind = %q, want %q", media.kind, tc.wantKind)
			}
		})
	}
}
