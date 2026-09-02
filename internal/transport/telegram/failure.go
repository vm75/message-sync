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
		return transport.NewFailureWithCertainty(transport.FailureRateLimited, time.Duration(rateLimit.RetryAfter)*time.Second, transport.SendUnknown, err)
	}
	switch {
	case errors.Is(err, telegrambot.ErrorForbidden), errors.Is(err, telegrambot.ErrorUnauthorized):
		return transport.NewFailureWithCertainty(transport.FailurePermissionDenied, 0, transport.SendUnknown, err)
	case errors.Is(err, telegrambot.ErrorNotFound):
		return transport.NewFailureWithCertainty(transport.FailureDestinationMissing, 0, transport.SendUnknown, err)
	case errors.Is(err, telegrambot.ErrorBadRequest):
		return transport.NewFailureWithCertainty(transport.FailurePayloadRejected, 0, transport.SendUnknown, err)
	default:
		return transport.NewFailureWithCertainty(transport.FailureTransient, 0, transport.SendUnknown, err)
	}
}
