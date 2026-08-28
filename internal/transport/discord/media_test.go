package discord

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/transport"
)

func TestDiscordAttachmentLoaderStreamsInMemoryAndEnforcesLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("media-bytes"))
	}))
	defer server.Close()

	tempDir := t.TempDir()
	before, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatal(err)
	}

	adapter := &Adapter{mediaEnabled: true, mediaMaxBytes: 32}
	incoming, ok := adapter.withDiscordMedia(
		transport.Incoming{Kind: "text", Text: "caption"},
		&discordgo.Message{Attachments: []*discordgo.MessageAttachment{{
			URL:         server.URL + "/private-cdn-path",
			ContentType: "image/png",
			Size:        len("media-bytes"),
		}}},
	)
	if !ok {
		t.Fatal("media event was dropped")
	}
	if incoming.Kind != "image" || incoming.MediaLoader == nil {
		t.Fatalf("unexpected normalized media: kind=%q loader=%v", incoming.Kind, incoming.MediaLoader != nil)
	}
	data, err := incoming.MediaLoader(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "media-bytes" {
		t.Fatalf("media = %q", data)
	}

	after, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatal("Discord media forwarding created a persistent artifact")
	}

	adapter.mediaMaxBytes = 4
	incoming, ok = adapter.withDiscordMedia(
		transport.Incoming{Kind: "text"},
		&discordgo.Message{Attachments: []*discordgo.MessageAttachment{{
			URL:         server.URL,
			ContentType: "application/pdf",
			Size:        len("media-bytes"),
		}}},
	)
	if !ok || incoming.MediaLoader == nil {
		t.Fatal("oversized media should surface through a bounded loader")
	}
	if _, err := incoming.MediaLoader(context.Background()); err == nil {
		t.Fatal("expected oversized attachment rejection")
	}
}

func TestDiscordMediaDisabledPreservesCaptionOnly(t *testing.T) {
	adapter := &Adapter{mediaEnabled: false}
	message := &discordgo.Message{Attachments: []*discordgo.MessageAttachment{{
		URL:         "https://example.invalid/private",
		ContentType: "audio/ogg",
		Size:        12,
	}}}

	caption, ok := adapter.withDiscordMedia(transport.Incoming{Kind: "text", Text: "caption"}, message)
	if !ok || caption.Kind != "text" || caption.MediaLoader != nil {
		t.Fatalf("caption handling = %#v accepted=%v", caption, ok)
	}
	if _, ok := adapter.withDiscordMedia(transport.Incoming{Kind: "text"}, message); ok {
		t.Fatal("attachment-only event should be dropped while media forwarding is disabled")
	}
}

func TestDiscordAttachmentKind(t *testing.T) {
	cases := map[string]string{
		"image/png":               "image",
		"video/mp4":               "video",
		"audio/ogg; codecs=opus":  "audio",
		"application/pdf":         "document",
		"":                        "document",
	}
	for contentType, want := range cases {
		if got := discordAttachmentKind(contentType); got != want {
			t.Fatalf("kind(%q) = %q, want %q", contentType, got, want)
		}
	}
}
