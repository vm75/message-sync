package transport

import (
	"errors"
	"testing"
	"time"
)

func TestClassifyFailureContract(t *testing.T) {
	cause := errors.New("provider response must stay private")
	tests := []struct {
		name       string
		err        error
		class      FailureClass
		retryable  bool
		retryAfter time.Duration
	}{
		{"unknown", cause, FailureTransient, true, 0},
		{"rate limited", NewFailure(FailureRateLimited, 3*time.Second, cause), FailureRateLimited, true, 3 * time.Second},
		{"permission", NewFailure(FailurePermissionDenied, 0, cause), FailurePermissionDenied, false, 0},
		{"payload", NewFailure(FailurePayloadRejected, 0, cause), FailurePayloadRejected, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.err)
			if got.Class != tt.class || got.Retryable != tt.retryable || got.RetryAfter != tt.retryAfter {
				t.Fatalf("failure = %+v, want class=%q retryable=%v retry_after=%s", got, tt.class, tt.retryable, tt.retryAfter)
			}
			if got.Error() == cause.Error() {
				t.Fatal("failure exposed raw cause")
			}
			if tt.err != nil && !errors.Is(tt.err, cause) {
				t.Fatal("failure did not preserve in-process cause")
			}
		})
	}
}

func TestNewFailureSanitizesClassAndDelay(t *testing.T) {
	cause := errors.New("private")
	failure := Classify(NewFailure(FailureClass("provider-secret"), -time.Second, cause))
	if failure.Class != FailureTransient || !failure.Retryable || failure.RetryAfter != 0 {
		t.Fatalf("failure = %+v, want sanitized transient failure", failure)
	}
}
