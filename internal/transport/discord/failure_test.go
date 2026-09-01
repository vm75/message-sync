package discord

import (
	"errors"
	"net/http"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/vm75/message-sync/internal/transport"
)

func TestClassifyDiscordFailure(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		class transport.FailureClass
	}{
		{"transient", errors.New("network failure"), transport.FailureTransient},
		{"forbidden", &discordgo.RESTError{Response: &http.Response{StatusCode: http.StatusForbidden}}, transport.FailurePermissionDenied},
		{"missing", &discordgo.RESTError{Response: &http.Response{StatusCode: http.StatusNotFound}}, transport.FailureDestinationMissing},
		{"bad request", &discordgo.RESTError{Response: &http.Response{StatusCode: http.StatusBadRequest}}, transport.FailurePayloadRejected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := transport.Classify(classifyDiscordFailure(tt.err))
			if got.Class != tt.class {
				t.Fatalf("class = %q, want %q", got.Class, tt.class)
			}
			if got.Error() == tt.err.Error() {
				t.Fatal("classification exposed provider error")
			}
		})
	}
}
