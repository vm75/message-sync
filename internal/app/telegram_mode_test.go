package app

import (
	"context"
	"errors"
	"testing"

	"github.com/vm75/message-sync/internal/controlstore"
	telegram "github.com/vm75/message-sync/internal/transport/telegram"
)

func TestOpenTelegramForIntegrationModeSelectsExactlyOneOpener(t *testing.T) {
	originalBot := openTelegram
	originalMTProto := openTelegramMTProto
	t.Cleanup(func() {
		openTelegram = originalBot
		openTelegramMTProto = originalMTProto
	})
	botErr := errors.New("bot selected")
	mtprotoErr := errors.New("mtproto selected")
	openTelegram = func(context.Context, telegram.Options) (telegramTransport, error) { return nil, botErr }
	openTelegramMTProto = func(context.Context, telegram.Options) (telegramTransport, error) { return nil, mtprotoErr }

	if _, err := openTelegramForIntegrationMode(context.Background(), "", telegram.Options{}); !errors.Is(err, botErr) {
		t.Fatalf("legacy/empty mode selected %v, want bot opener", err)
	}
	if _, err := openTelegramForIntegrationMode(context.Background(), controlstore.TelegramIntegrationModeBot, telegram.Options{}); !errors.Is(err, botErr) {
		t.Fatalf("bot mode selected %v, want bot opener", err)
	}
	if _, err := openTelegramForIntegrationMode(context.Background(), controlstore.TelegramIntegrationModeMTProto, telegram.Options{}); !errors.Is(err, mtprotoErr) {
		t.Fatalf("mtproto mode selected %v, want MTProto opener", err)
	}
}
