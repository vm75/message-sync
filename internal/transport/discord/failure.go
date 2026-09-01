package discord

import (
	"errors"
	"net/http"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/transport"
)

func classifyDiscordFailure(err error) error {
	if err == nil {
		return nil
	}
	var rateLimit *discordgo.RateLimitError
	if errors.As(err, &rateLimit) && rateLimit != nil && rateLimit.RateLimit != nil {
		return transport.NewFailure(transport.FailureRateLimited, rateLimit.RetryAfter, err)
	}
	var restErr *discordgo.RESTError
	if errors.As(err, &restErr) && restErr != nil && restErr.Response != nil {
		class := transport.FailureTransient
		switch restErr.Response.StatusCode {
		case http.StatusForbidden, http.StatusUnauthorized:
			class = transport.FailurePermissionDenied
		case http.StatusNotFound:
			class = transport.FailureDestinationMissing
		case http.StatusBadRequest:
			class = transport.FailurePayloadRejected
		case http.StatusTooManyRequests:
			class = transport.FailureRateLimited
		}
		return transport.NewFailure(class, 0, err)
	}
	return transport.NewFailure(transport.FailureTransient, 0, err)
}
