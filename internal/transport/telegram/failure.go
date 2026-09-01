package telegram

import (
	"errors"
	"time"

	telegrambot "github.com/go-telegram/bot"
	"github.com/vm75/message-sync/internal/transport"
)

func classifyTelegramFailure(err error) error {
	if err == nil {
		return nil
	}
	var rateLimit *telegrambot.TooManyRequestsError
	if errors.As(err, &rateLimit) {
		return transport.NewFailure(transport.FailureRateLimited, time.Duration(rateLimit.RetryAfter)*time.Second, err)
	}
	switch {
	case errors.Is(err, telegrambot.ErrorForbidden), errors.Is(err, telegrambot.ErrorUnauthorized):
		return transport.NewFailure(transport.FailurePermissionDenied, 0, err)
	case errors.Is(err, telegrambot.ErrorNotFound):
		return transport.NewFailure(transport.FailureDestinationMissing, 0, err)
	case errors.Is(err, telegrambot.ErrorBadRequest):
		return transport.NewFailure(transport.FailurePayloadRejected, 0, err)
	default:
		return transport.NewFailure(transport.FailureTransient, 0, err)
	}
}
