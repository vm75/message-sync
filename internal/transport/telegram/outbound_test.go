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

	methods        []string
	texts          []string
	caption        string
	filename       string
	media          []byte
	replyID        int
	reactions      []*telegrambot.SetMessageReactionParams
	editText       *telegrambot.EditMessageTextParams
	editCaption    *telegrambot.EditMessageCaptionParams
	deleted        *telegrambot.DeleteMessageParams
	getFile        *models.File
	messageErrs    []error
	deleteErr      error
	getFileErr     error
	reactionErr    error
	editTextErr    error
	editCaptionErr error
}

func (f *fakeTelegramAPI) Start(context.Context) {}
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
	if params.ReplyParameters != nil {
		f.replyID = params.ReplyParameters.MessageID
	}
	if err := f.popMessageErr(); err != nil {
		return nil, err
	}
	return f.nextMessage(), nil
}

func captureUpload(file models.InputFile) (string, []byte) {
	upload, ok := file.(*models.InputFileUpload)
	if !ok || upload == nil {
		return "", nil
	}
	data, _ := io.ReadAll(upload.Data)
	return upload.Filename, data
}

func (f *fakeTelegramAPI) captureMedia(method string, file models.InputFile, caption string, reply *models.ReplyParameters) (*models.Message, error) {
	f.methods = append(f.methods, method)
	f.filename, f.media = captureUpload(file)
	f.caption = caption
	if reply != nil {
		f.replyID = reply.MessageID
	}
	return f.nextMessage(), nil
}

func (f *fakeTelegramAPI) SendPhoto(_ context.Context, params *telegrambot.SendPhotoParams) (*models.Message, error) {
	return f.captureMedia("photo", params.Photo, params.Caption, params.ReplyParameters)
}
func (f *fakeTelegramAPI) SendVideo(_ context.Context, params *telegrambot.SendVideoParams) (*models.Message, error) {
	return f.captureMedia("video", params.Video, params.Caption, params.ReplyParameters)
}
func (f *fakeTelegramAPI) SendAudio(_ context.Context, params *telegrambot.SendAudioParams) (*models.Message, error) {
	return f.captureMedia("audio", params.Audio, params.Caption, params.ReplyParameters)
}
func (f *fakeTelegramAPI) SendVoice(_ context.Context, params *telegrambot.SendVoiceParams) (*models.Message, error) {
	return f.captureMedia("voice", params.Voice, params.Caption, params.ReplyParameters)
}
func (f *fakeTelegramAPI) SendDocument(_ context.Context, params *telegrambot.SendDocumentParams) (*models.Message, error) {
	return f.captureMedia("document", params.Document, params.Caption, params.ReplyParameters)
}
func (f *fakeTelegramAPI) SendSticker(_ context.Context, params *telegrambot.SendStickerParams) (*models.Message, error) {
	return f.captureMedia("sticker", params.Sticker, "", params.ReplyParameters)
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
