package whatsapp

import (
	"errors"

	"github.com/vm75/message-sync/internal/transport"
	"go.mau.fi/whatsmeow"
)

func classifyWhatsAppFailure(err error) error {
	if err == nil {
		return nil
	}
	class := transport.FailureTransient
	switch {
	case errors.Is(err, whatsmeow.ErrGroupNotFound):
		class = transport.FailureDestinationMissing
	case errors.Is(err, whatsmeow.ErrNotInGroup), errors.Is(err, whatsmeow.ErrNotLoggedIn):
		class = transport.FailurePermissionDenied
	}
	return transport.NewFailure(class, 0, err)
}
