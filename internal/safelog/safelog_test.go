package safelog

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestErrorDoesNotLogRawErrorText(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, nil))
	sensitive := "raw-identity@example.invalid display-name secret-message"
	Error(logger, "transport operation failed", "receive", errors.New(sensitive))
	logged := out.String()
	if strings.Contains(logged, sensitive) || strings.Contains(logged, "raw-identity") || strings.Contains(logged, "display-name") {
		t.Fatalf("sensitive error text leaked: %s", logged)
	}
	if !strings.Contains(logged, `"error_kind":"internal"`) {
		t.Fatalf("safe error class missing: %s", logged)
	}
}
