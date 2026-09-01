package telegram

import (
	"errors"
	"testing"

	telegrambot "github.com/go-telegram/bot"
	"github.com/vm75/message-sync/internal/transport"
)

func TestClassifyTelegramFailure(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		class transport.FailureClass
	}{
		{"transient", errors.New("network failure"), transport.FailureTransient},
		{"rate limited", &telegrambot.TooManyRequestsError{RetryAfter: 2}, transport.FailureRateLimited},
		{"forbidden", telegrambot.ErrorForbidden, transport.FailurePermissionDenied},
		{"missing", telegrambot.ErrorNotFound, transport.FailureDestinationMissing},
		{"bad request", telegrambot.ErrorBadRequest, transport.FailurePayloadRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := transport.Classify(classifyTelegramFailure(tt.err))
			if got.Class != tt.class {
				t.Fatalf("class = %q, want %q", got.Class, tt.class)
			}
			if got.Error() == tt.err.Error() {
				t.Fatal("classification exposed provider error")
			}
		})
	}
}
