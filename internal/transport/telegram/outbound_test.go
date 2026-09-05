package telegram

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	telegrambot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
	"github.com/vm75/message-sync/internal/transport"
)

type fakeTelegramAPI struct {
	botID int64
	next  int

	methods          []string
	texts            []string
	textParseMode    models.ParseMode
	polls            []*telegrambot.SendPollParams
	caption          string
	captionParseMode models.ParseMode
	filename         string
	media            []byte
	replyID          int
	reactions        []*telegrambot.SetMessageReactionParams
	editText         *telegrambot.EditMessageTextParams
	editCaption      *telegrambot.EditMessageCaptionParams
	deleted          *telegrambot.DeleteMessageParams
	getFile          *models.File
	messageErrs      []error
	deleteErr        error
	getFileErr       error
	reactionErr      error
	editTextErr      error
	editCaptionErr   error
}

func (f *fakeTelegramAPI) Start(context.Context) {}
func (f *fakeTelegramAPI) GetMe(context.Context) (*models.User, error) {
	return &models.User{CanReadAllGroupMessages: true}, nil
}
func (f *fakeTelegramAPI) ID() int64 {
	if f.botID == 0 {
		return 999
	}
	return f.botID
}

func (f *fakeTelegramAPI) nextMessage() *models.Message {
	f.next++
	return &models.Message{ID: 100 + f.next}
}

func (f *fakeTelegramAPI) popMessageErr() error {
	if len(f.messageErrs) == 0 {
		return nil
	}
	err := f.messageErrs[0]
	f.messageErrs = f.messageErrs[1:]
	return err
}

func (f *fakeTelegramAPI) SendMessage(_ context.Context, params *telegrambot.SendMessageParams) (*models.Message, error) {
	f.methods = append(f.methods, "message")
	f.texts = append(f.texts, params.Text)
	f.textParseMode = params.ParseMode
	if params.ReplyParameters != nil {
		f.replyID = params.ReplyParameters.MessageID
	}
	if err := f.popMessageErr(); err != nil {
		return nil, err
	}
	return f.nextMessage(), nil
}

func (f *fakeTelegramAPI) SendPoll(_ context.Context, params *telegrambot.SendPollParams) (*models.Message, error) {
	f.methods = append(f.methods, "poll")
	f.polls = append(f.polls, params)
	if err := f.popMessageErr(); err != nil {
		return nil, err
	}
	message := f.nextMessage()
	message.Poll = &models.Poll{ID: "opaque-telegram-poll", Options: []models.PollOption{{Text: "one"}, {Text: "two"}}}
	return message, nil
}

func captureUpload(file models.InputFile) (string, []byte) {
	upload, ok := file.(*models.InputFileUpload)
	if !ok || upload == nil {
		return "", nil
	}
	data, _ := io.ReadAll(upload.Data)
	return upload.Filename, data
}

func (f *fakeTelegramAPI) captureMedia(method string, file models.InputFile, caption string, parseMode models.ParseMode, reply *models.ReplyParameters) (*models.Message, error) {
	f.methods = append(f.methods, method)
	f.filename, f.media = captureUpload(file)
	f.caption = caption
	f.captionParseMode = parseMode
	if reply != nil {
		f.replyID = reply.MessageID
	}
	return f.nextMessage(), nil
}

func (f *fakeTelegramAPI) SendPhoto(_ context.Context, params *telegrambot.SendPhotoParams) (*models.Message, error) {
	return f.captureMedia("photo", params.Photo, params.Caption, params.ParseMode, params.ReplyParameters)
}
func (f *fakeTelegramAPI) SendVideo(_ context.Context, params *telegrambot.SendVideoParams) (*models.Message, error) {
	return f.captureMedia("video", params.Video, params.Caption, params.ParseMode, params.ReplyParameters)
}
func (f *fakeTelegramAPI) SendAudio(_ context.Context, params *telegrambot.SendAudioParams) (*models.Message, error) {
	return f.captureMedia("audio", params.Audio, params.Caption, params.ParseMode, params.ReplyParameters)
}
func (f *fakeTelegramAPI) SendVoice(_ context.Context, params *telegrambot.SendVoiceParams) (*models.Message, error) {
	return f.captureMedia("voice", params.Voice, params.Caption, params.ParseMode, params.ReplyParameters)
}
func (f *fakeTelegramAPI) SendDocument(_ context.Context, params *telegrambot.SendDocumentParams) (*models.Message, error) {
	return f.captureMedia("document", params.Document, params.Caption, params.ParseMode, params.ReplyParameters)
}
func (f *fakeTelegramAPI) SendSticker(_ context.Context, params *telegrambot.SendStickerParams) (*models.Message, error) {
	return f.captureMedia("sticker", params.Sticker, "", "", params.ReplyParameters)
}
func (f *fakeTelegramAPI) SetMessageReaction(_ context.Context, params *telegrambot.SetMessageReactionParams) (bool, error) {
	f.reactions = append(f.reactions, params)
	if f.reactionErr != nil {
		return false, f.reactionErr
	}
	return true, nil
}
func (f *fakeTelegramAPI) EditMessageText(_ context.Context, params *telegrambot.EditMessageTextParams) (*models.Message, error) {
	f.editText = params
	if f.editTextErr != nil {
		return nil, f.editTextErr
	}
	return f.nextMessage(), nil
}
func (f *fakeTelegramAPI) EditMessageCaption(_ context.Context, params *telegrambot.EditMessageCaptionParams) (*models.Message, error) {
	f.editCaption = params
	if f.editCaptionErr != nil {
		return nil, f.editCaptionErr
	}
	return f.nextMessage(), nil
}
func (f *fakeTelegramAPI) DeleteMessage(_ context.Context, params *telegrambot.DeleteMessageParams) (bool, error) {
	f.deleted = params
	if f.deleteErr != nil {
		return false, f.deleteErr
	}
	return true, nil
}
func (f *fakeTelegramAPI) GetFile(_ context.Context, _ *telegrambot.GetFileParams) (*models.File, error) {
	if f.getFileErr != nil {
		return nil, f.getFileErr
	}
	return f.getFile, nil
}

func newOutboundTestAdapter(t *testing.T, api *fakeTelegramAPI) *Adapter {
	t.Helper()
	hasher, err := identity.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	normalizer, err := NewNormalizer(map[string]string{"tg": "-1001234567890"}, hasher, config.UsernameModePushName)
	if err != nil {
		t.Fatal(err)
	}
	return &Adapter{
		connectionID:  "conn-tg-test",
		client:        api,
		normalizer:    normalizer,
		hasher:        hasher,
		mediaEnabled:  true,
		mediaMaxBytes: telegramGeneralUploadMax,
		messageKinds:  make(map[messageKindKey]string),
	}
}

func TestSendUsesTransientSenderNamesAndHashFallback(t *testing.T) {
	api := &fakeTelegramAPI{}
	adapter := newOutboundTestAdapter(t, api)

	cases := []struct {
		name   string
		sender transport.Sender
		want   string
	}{
		{name: "first transient name", sender: transport.Sender{DisplayName: " Vidhya ", OpaqueID: "u_firsthash"}, want: "Vidhya: hello"},
		{name: "second transient name", sender: transport.Sender{DisplayName: "Alex Chen", OpaqueID: "u_secondhash"}, want: "Alex Chen: hello"},
		{name: "hash fallback", sender: transport.Sender{OpaqueID: "u_abcd1234"}, want: "u_abcd1234: hello"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api.texts = nil
			_, err := adapter.Send(context.Background(), transport.Outgoing{
				Endpoint:   "tg",
				Sender:     tc.sender,
				SourceText: "hello",
				Kind:       "text",
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(api.texts) != 1 || api.texts[0] != tc.want {
				t.Fatalf("sent text = %q, want %q", api.texts, tc.want)
			}
		})
	}
}

func TestSendUsesCentralFriendlyRendering(t *testing.T) {
	api := &fakeTelegramAPI{}
	adapter := newOutboundTestAdapter(t, api)
	_, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint: "tg", OriginEndpoint: "tg", Sender: transport.Sender{DisplayName: "Alice"},
		SourceText: "Flights booked", RenderedText: "*_family:Travel/Alice_*: Flights booked", Kind: "text",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(api.texts) != 1 || api.texts[0] != "<b><i>family:Travel/Alice</i></b>: Flights booked" || api.textParseMode != models.ParseModeHTML {
		t.Fatalf("Telegram adapter discarded friendly rendering: %q", api.texts)
	}
}

func TestSendUsesNativeReplyAndPrivacySafeFallback(t *testing.T) {
	api := &fakeTelegramAPI{}
	adapter := newOutboundTestAdapter(t, api)
	_, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint:   "tg",
		Sender:     transport.Sender{DisplayName: "Alice", OpaqueID: "u_hash"},
		SourceText: "native reply",
		Kind:       "text",
		ReplyTo:    &transport.MessageRef{Endpoint: "tg", RemoteMessageID: "42"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if api.replyID != 42 {
		t.Fatalf("reply id = %d, want 42", api.replyID)
	}

	api.replyID = 0
	api.texts = nil
	_, err = adapter.Send(context.Background(), transport.Outgoing{
		Endpoint:       "tg",
		OriginEndpoint: "wa",
		Sender:         transport.Sender{OpaqueID: "u_hash"},
		SourceText:     "fallback reply",
		Kind:           "text",
		ReplyFallback:  true,
		QuotedText:     "quoted source body",
	})
	if err != nil {
		t.Fatal(err)
	}
	if api.replyID != 0 || len(api.texts) != 1 || !strings.Contains(api.texts[0], "↳ wa: quoted source body") || !strings.Contains(api.texts[0], "wa/u_hash: fallback reply") {
		t.Fatalf("unexpected fallback send: reply=%d text=%q", api.replyID, api.texts)
	}
}

func TestSendPrefixesCrossEndpointOriginForAllTelegramTextPaths(t *testing.T) {
	api := &fakeTelegramAPI{}
	adapter := newOutboundTestAdapter(t, api)
	outgoing := transport.Outgoing{
		Endpoint: "tg", OriginEndpoint: "wa",
		Sender:     transport.Sender{DisplayName: "Alice", OpaqueID: "u_hash"},
		SourceText: "hello", Kind: "text",
	}
	if _, err := adapter.Send(context.Background(), outgoing); err != nil {
		t.Fatal(err)
	}
	if got := api.texts[len(api.texts)-1]; got != "wa/Alice: hello" {
		t.Fatalf("cross-endpoint Telegram text = %q, want %q", got, "wa/Alice: hello")
	}

	api.texts = nil
	if _, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint: "tg", OriginEndpoint: "wa", Sender: outgoing.Sender,
		SourceText: "caption", Kind: "image", MediaBytes: []byte("image"),
	}); err != nil {
		t.Fatal(err)
	}
	if api.caption != "wa/Alice: caption" {
		t.Fatalf("cross-endpoint Telegram media caption = %q, want %q", api.caption, "wa/Alice: caption")
	}
}

func TestSendMediaUsesGenericNamesAndHostedLimits(t *testing.T) {
	api := &fakeTelegramAPI{}
	adapter := newOutboundTestAdapter(t, api)
	_, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint:   "tg",
		Sender:     transport.Sender{DisplayName: "Alice", OpaqueID: "u_hash"},
		SourceText: "caption",
		Kind:       "document",
		MediaBytes: []byte("MEDIA_SENTINEL"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if api.filename != "document.bin" || string(api.media) != "MEDIA_SENTINEL" || api.caption != "Alice: caption" {
		t.Fatalf("document send = filename %q media %q caption %q", api.filename, api.media, api.caption)
	}

	adapter.mediaMaxBytes = 3
	_, err = adapter.Send(context.Background(), transport.Outgoing{Endpoint: "tg", Kind: "image", MediaBytes: []byte("1234")})
	if err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversize error = %v", err)
	}
}

func TestSendMediaMappingsUseHostedBotAPIForms(t *testing.T) {
	cases := []struct {
		name         string
		kind         string
		data         []byte
		wantMethod   string
		wantFilename string
	}{
		{name: "image", kind: "image", data: []byte("image-bytes"), wantMethod: "photo", wantFilename: "image.jpg"},
		{name: "video", kind: "video", data: []byte("video-bytes"), wantMethod: "video", wantFilename: "video.mp4"},
		{name: "audio", kind: "audio", data: append([]byte("ID3"), []byte("audio-bytes")...), wantMethod: "audio", wantFilename: "audio.mp3"},
		{name: "voice", kind: "audio", data: append([]byte("OggS"), []byte("voice-bytes")...), wantMethod: "voice", wantFilename: "voice.ogg"},
		{name: "sticker", kind: "sticker", data: append([]byte("RIFF0000WEBP"), []byte("sticker-bytes")...), wantMethod: "sticker", wantFilename: "sticker.webp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeTelegramAPI{}
			adapter := newOutboundTestAdapter(t, api)
			_, err := adapter.Send(context.Background(), transport.Outgoing{
				Endpoint:   "tg",
				Sender:     transport.Sender{DisplayName: "Alice", OpaqueID: "u_hash"},
				SourceText: "caption",
				Kind:       tc.kind,
				MediaBytes: tc.data,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(api.methods) == 0 || api.methods[0] != tc.wantMethod {
				t.Fatalf("methods = %v, want first method %q", api.methods, tc.wantMethod)
			}
			if api.filename != tc.wantFilename {
				t.Fatalf("filename = %q, want %q", api.filename, tc.wantFilename)
			}
			if strings.Contains(api.filename, "source") || strings.Contains(api.filename, "private") {
				t.Fatalf("source filename leaked into Telegram upload name %q", api.filename)
			}
			if string(api.media) != string(tc.data) {
				t.Fatal("media bytes were not forwarded transiently")
			}
			if tc.kind != "sticker" && api.caption != "Alice: caption" {
				t.Fatalf("caption = %q, want transient sender attribution", api.caption)
			}
			if tc.kind == "sticker" {
				if len(api.methods) < 2 || api.methods[1] != "message" {
					t.Fatalf("sticker attribution companion methods = %v", api.methods)
				}
				if len(api.texts) != 1 || api.texts[0] != "Alice: caption" {
					t.Fatalf("sticker companion attribution = %q", api.texts)
				}
			}
		})
	}
}

func TestReactionEditDeleteAndRateLimitRetry(t *testing.T) {
	api := &fakeTelegramAPI{}
	adapter := newOutboundTestAdapter(t, api)
	ref, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint: "tg", Sender: transport.Sender{DisplayName: "Alice", OpaqueID: "u_hash"}, SourceText: "original", Kind: "text",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := adapter.React(context.Background(), transport.Reaction{Endpoint: "tg", TargetRemoteID: ref.RemoteMessageID, Emoji: "👍"}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.React(context.Background(), transport.Reaction{Endpoint: "tg", TargetRemoteID: ref.RemoteMessageID}); err != nil {
		t.Fatal(err)
	}
	if len(api.reactions) != 2 || len(api.reactions[0].Reaction) != 1 || len(api.reactions[1].Reaction) != 0 {
		t.Fatalf("reaction calls = %#v", api.reactions)
	}

	if err := adapter.Edit(context.Background(), ref, "*_wa/15551234567 (Alice)_*: changed"); err != nil {
		t.Fatal(err)
	}
	if api.editText == nil || api.editText.Text != "Alice: changed" {
		t.Fatalf("edit text = %#v", api.editText)
	}

	api.deleteErr = fmt.Errorf("%w, message to delete not found", telegrambot.ErrorBadRequest)
	if err := adapter.Delete(context.Background(), ref); err != nil {
		t.Fatalf("idempotent delete error = %v", err)
	}

	api.deleteErr = nil
	api.messageErrs = []error{&telegrambot.TooManyRequestsError{Message: "rate limited", RetryAfter: 2}, nil}
	var waits []time.Duration
	adapter.retryWait = func(_ context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		return nil
	}
	if _, err := adapter.Send(context.Background(), transport.Outgoing{Endpoint: "tg", Sender: transport.Sender{OpaqueID: "u_hash"}, SourceText: "retry", Kind: "text"}); err != nil {
		t.Fatal(err)
	}
	if len(waits) != 1 || waits[0] != 2*time.Second {
		t.Fatalf("retry waits = %v, want [2s]", waits)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestTelegramMediaLoaderIsTransientAndBounded(t *testing.T) {
	api := &fakeTelegramAPI{getFile: &models.File{FilePath: "photos/file.bin", FileSize: 14}}
	adapter := newOutboundTestAdapter(t, api)
	adapter.token = "TOKEN_SENTINEL"
	adapter.mediaMaxBytes = 1024
	adapter.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if !strings.Contains(request.URL.Path, "TOKEN_SENTINEL") {
			t.Fatalf("download URL did not use in-memory token")
		}
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(bytes.NewReader([]byte("MEDIA_SENTINEL"))),
			ContentLength: int64(len("MEDIA_SENTINEL")),
			Header:        make(http.Header),
		}, nil
	})}

	message := &models.Message{
		ID:    77,
		Date:  1_700_000_000,
		Chat:  models.Chat{ID: -1001234567890, Type: models.ChatTypeSupergroup},
		From:  &models.User{ID: 12345, FirstName: "Transient", LastName: "Sender"},
		Photo: []models.PhotoSize{{FileID: "FILE_ID_SENTINEL", FileSize: 14}},
	}
	incoming, ok := adapter.normalizer.NormalizeMessage(message, api.ID())
	if !ok {
		t.Fatal("media-only Telegram message was rejected")
	}
	incoming, ok = adapter.withTelegramMedia(incoming, message)
	if !ok || incoming.Kind != "image" || incoming.MediaLoader == nil {
		t.Fatalf("media incoming = %#v ok=%v", incoming, ok)
	}
	data, err := incoming.MediaLoader(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "MEDIA_SENTINEL" {
		t.Fatalf("media bytes = %q", data)
	}
	if incoming.RemoteID != "77" || strings.Contains(fmt.Sprintf("%+v", incoming), "FILE_ID_SENTINEL") {
		t.Fatalf("raw Telegram file id crossed the canonical boundary: %#v", incoming)
	}

	api.getFile = &models.File{FilePath: "photos/file.bin", FileSize: telegramHostedDownloadMax + 1}
	_, err = adapter.downloadTelegramMedia(context.Background(), "FILE_ID_SENTINEL", telegramHostedDownloadMax)
	if err == nil || strings.Contains(err.Error(), "FILE_ID_SENTINEL") || strings.Contains(err.Error(), "TOKEN_SENTINEL") {
		t.Fatalf("unsafe or missing oversize error: %v", err)
	}
}

func TestNormalizerLifecycleEvents(t *testing.T) {
	api := &fakeTelegramAPI{}
	adapter := newOutboundTestAdapter(t, api)
	n := adapter.normalizer

	edit, ok := n.NormalizeEditedMessage(&models.Message{
		ID: 88, Date: 1_700_000_010,
		Chat: models.Chat{ID: -1001234567890, Type: models.ChatTypeGroup},
		From: &models.User{ID: 12345, FirstName: "Edit", LastName: "Sender"},
		Text: "changed",
	}, api.ID())
	if !ok || edit.Kind != "edit" || edit.ReplyTo == nil || edit.ReplyTo.RemoteMessageID != "88" {
		t.Fatalf("edit normalization = %#v ok=%v", edit, ok)
	}

	reaction, ok := n.NormalizeReaction(&models.MessageReactionUpdated{
		Chat:      models.Chat{ID: -1001234567890, Type: models.ChatTypeSupergroup},
		MessageID: 88,
		User:      &models.User{ID: 54321, FirstName: "Reaction", LastName: "Sender"},
		Date:      1_700_000_020,
		NewReaction: []models.ReactionType{{
			Type:              models.ReactionTypeTypeEmoji,
			ReactionTypeEmoji: &models.ReactionTypeEmoji{Type: models.ReactionTypeTypeEmoji, Emoji: "❤️"},
		}},
	}, api.ID())
	if !ok || reaction.Kind != "reaction" || reaction.Text != "❤️" || reaction.Sender.OpaqueID == "" || strings.Contains(reaction.Sender.OpaqueID, "54321") {
		t.Fatalf("reaction normalization = %#v ok=%v", reaction, ok)
	}

	_, ok = n.NormalizeReaction(&models.MessageReactionUpdated{
		Chat: models.Chat{ID: 12345, Type: models.ChatTypePrivate}, MessageID: 88, User: &models.User{ID: 54321},
	}, api.ID())
	if ok {
		t.Fatal("private-chat reaction was accepted")
	}
}

var _ botClient = (*fakeTelegramAPI)(nil)
var _ telegramAPI = (*fakeTelegramAPI)(nil)

func TestSendPollUsesDeterministicTextFallback(t *testing.T) {
	api := &fakeTelegramAPI{}
	adapter := newOutboundTestAdapter(t, api)

	_, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint:            "tg",
		Sender:              transport.Sender{DisplayName: "Alice", OpaqueID: "u_hash"},
		SourceText:          "Lunch?",
		Kind:                "poll",
		PollOptions:         []string{"Idli", "Dosa", "Rice", "Roti", "Tea", "Coffee", "Soup", "Fruit", "Cake", "Bread", "Water"},
		PollSelectableCount: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "Alice: Poll: Lunch?\n1. Idli\n2. Dosa\n3. Rice\n4. Roti\n5. Tea\n6. Coffee\n7. Soup\n8. Fruit\n9. Cake\n10. Bread\n11. Water\nChoose up to 2 options."
	if len(api.methods) != 1 || api.methods[0] != "message" {
		t.Fatalf("Telegram poll fallback methods = %v", api.methods)
	}
	if len(api.texts) != 1 || api.texts[0] != want {
		t.Fatalf("Telegram poll fallback text = %q, want %q", api.texts, want)
	}
}

func TestSendRepresentablePollUsesBotAPIPollAndReturnsOpaqueReference(t *testing.T) {
	api := &fakeTelegramAPI{}
	adapter := newOutboundTestAdapter(t, api)
	ref, err := adapter.Send(context.Background(), transport.Outgoing{Endpoint: "tg", SourceText: "Lunch?", Kind: "poll", PollOptions: []string{"Idli", "Dosa"}, PollSelectableCount: 1, PollAttribution: "*_family:Travel/Alice_*:"})
	if err != nil {
		t.Fatal(err)
	}
	if len(api.polls) != 1 || ref.Provider != "telegram:conn-tg-test" || ref.ProviderReference != "opaque-telegram-poll" {
		t.Fatalf("native Telegram poll = ref %#v polls=%d", ref, len(api.polls))
	}
	if api.polls[0].AllowsMultipleAnswers || api.polls[0].IsAnonymous == nil || !*api.polls[0].IsAnonymous {
		t.Fatalf("unexpected Telegram poll semantics: %#v", api.polls[0])
	}
	if api.polls[0].Question != "Lunch?" || api.polls[0].Description != "<b><i>family:Travel/Alice</i></b>:" {
		t.Fatalf("native Telegram poll presentation = %#v", api.polls[0])
	}
	if api.polls[0].DescriptionParseMode != models.ParseModeHTML {
		t.Fatalf("native Telegram poll description parse mode = %q, want HTML", api.polls[0].DescriptionParseMode)
	}
}

func TestTelegramNativePollPresentationKeepsSourceAttributionAndTopicOpaque(t *testing.T) {
	tests := []struct {
		name       string
		pollAttr   string
		childScope *transport.ChildScope
		wantThread int
		wantDesc   string
		wantMode   models.ParseMode
	}{
		{name: "friendly root", pollAttr: "*_family/Alice_*:", wantDesc: "<b><i>family/Alice</i></b>:", wantMode: models.ParseModeHTML},
		{name: "friendly topic", pollAttr: "*_family:Plans/Alice_*:", childScope: &transport.ChildScope{Kind: transport.ScopeKindTelegramTopic, RemoteID: "77"}, wantThread: 77, wantDesc: "<b><i>family:Plans/Alice</i></b>:", wantMode: models.ParseModeHTML},
		{name: "opaque topic", childScope: &transport.ChildScope{Kind: transport.ScopeKindTelegramTopic, RemoteID: "77"}, wantThread: 77},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeTelegramAPI{}
			adapter := newOutboundTestAdapter(t, api)
			_, err := adapter.Send(context.Background(), transport.Outgoing{
				Endpoint: "tg", SourceText: "Lunch?", Kind: "poll", ChildScope: tc.childScope,
				PollAttribution: tc.pollAttr, PollOptions: []string{"Idli", "Dosa"}, PollSelectableCount: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(api.polls) != 1 || api.polls[0].MessageThreadID != tc.wantThread {
				t.Fatalf("native Telegram poll placement = %#v, want thread %d", api.polls, tc.wantThread)
			}
			if api.polls[0].Question != "Lunch?" || api.polls[0].Description != tc.wantDesc || api.polls[0].DescriptionParseMode != tc.wantMode || api.polls[0].Options[0].Text != "Idli" {
				t.Fatalf("native Telegram poll presentation = %#v", api.polls[0])
			}
		})
	}
}

func TestTelegramNativePollRetryReusesSingleRenderedAttribution(t *testing.T) {
	api := &fakeTelegramAPI{messageErrs: []error{&telegrambot.TooManyRequestsError{Message: "rate limited", RetryAfter: 1}, nil}}
	adapter := newOutboundTestAdapter(t, api)
	adapter.retryWait = func(context.Context, time.Duration) error { return nil }
	_, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint: "tg", SourceText: "Lunch?", Kind: "poll", PollAttribution: "*_family:Plans/Alice_*:",
		PollOptions: []string{"Idli", "Dosa"}, PollSelectableCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(api.polls) != 2 || api.polls[0].Description != api.polls[1].Description || api.polls[1].Description != "<b><i>family:Plans/Alice</i></b>:" || api.polls[1].DescriptionParseMode != models.ParseModeHTML {
		t.Fatalf("poll retry attribution = %#v", api.polls)
	}
}

func TestTelegramNativePollAcceptsWhatsAppUnlimitedSemantics(t *testing.T) {
	api := &fakeTelegramAPI{}
	adapter := newOutboundTestAdapter(t, api)
	_, err := adapter.Send(context.Background(), transport.Outgoing{
		Endpoint: "tg", SourceText: "Choose any", Kind: "poll",
		PollOptions: []string{"One", "Two"}, PollSelectableCount: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(api.polls) != 1 || !api.polls[0].AllowsMultipleAnswers {
		t.Fatalf("WhatsApp unlimited poll did not use native Telegram poll: %#v", api.polls)
	}
}

func TestTelegramPresentationConvertsCanonicalAggregateHeading(t *testing.T) {
	got, mode := telegramPresentation("📊 ***LIVE POLL RESULTS ACROSS ALL GROUPS***\n❓ Question & choice\n\n**Options**\n○ *Option* — 0 votes")
	want := "📊 <b><i>LIVE POLL RESULTS ACROSS ALL GROUPS</i></b>\n❓ Question &amp; choice\n\n<b>Options</b>\n○ <i>Option</i> — 0 votes"
	if got != want || mode != models.ParseModeHTML {
		t.Fatalf("aggregate presentation = %q, %q; want %q, HTML", got, mode, want)
	}
}
