package safelog

import (
	"context"
	"errors"
	"log/slog"
	"os"
)

// Error records only a stable error class. Callers must never pass err.Error(),
// protocol objects, identifiers, names, message text, or media into application logs.
func Error(logger *slog.Logger, message, operation string, err error) {
	logger.Error(message,
		"operation", operation,
		"error_kind", errorKind(err),
	)
}

func errorKind(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, os.ErrNotExist):
		return "not_found"
	case errors.Is(err, os.ErrPermission):
		return "permission"
	default:
		return "internal"
	}
}
