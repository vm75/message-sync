package whatsapp

import (
	"errors"
	"testing"

	"github.com/vm75/message-sync/internal/transport"
	"go.mau.fi/whatsmeow"
)

func TestClassifyWhatsAppFailure(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		class transport.FailureClass
	}{
		{"transient", errors.New("network failure"), transport.FailureTransient},
		{"missing", whatsmeow.ErrGroupNotFound, transport.FailureDestinationMissing},
		{"permission", whatsmeow.ErrNotInGroup, transport.FailurePermissionDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := transport.Classify(classifyWhatsAppFailure(tt.err))
			if got.Class != tt.class {
				t.Fatalf("class = %q, want %q", got.Class, tt.class)
			}
			if got.Error() == tt.err.Error() {
				t.Fatal("classification exposed provider error")
			}
		})
	}
}
