package transport

import (
	"errors"
	"time"
)

// FailureClass is the only failure detail that may cross the transport
// boundary into routing, persistence, logs, or APIs.
type FailureClass string

const (
	FailureTransient          FailureClass = "transient"
	FailureRateLimited        FailureClass = "rate_limited"
	FailurePermissionDenied   FailureClass = "permission_denied"
	FailureDestinationMissing FailureClass = "destination_missing"
	FailurePayloadRejected    FailureClass = "payload_rejected"
	FailureUnsupported        FailureClass = "unsupported"
)

// Failure preserves the provider cause for in-process errors.Is/errors.As
// checks while exposing only safe operational fields to callers.
type Failure struct {
	Class      FailureClass
	Retryable  bool
	RetryAfter time.Duration
	cause      error
}

func (f *Failure) Error() string {
	if f == nil {
		return string(FailureTransient)
	}
	return string(f.Class)
}

func (f *Failure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.cause
}

func validFailureClass(class FailureClass) bool {
	switch class {
	case FailureTransient, FailureRateLimited, FailurePermissionDenied,
		FailureDestinationMissing, FailurePayloadRejected, FailureUnsupported:
		return true
	default:
		return false
	}
}

func classRetryable(class FailureClass) bool {
	return class == FailureTransient || class == FailureRateLimited
}

// NewFailure classifies an error without exposing its provider-specific text.
// A nil cause returns nil so it can be used directly around provider calls.
func NewFailure(class FailureClass, retryAfter time.Duration, cause error) error {
	if cause == nil {
		return nil
	}
	if !validFailureClass(class) {
		class = FailureTransient
	}
	if retryAfter < 0 {
		retryAfter = 0
	}
	return &Failure{Class: class, Retryable: classRetryable(class), RetryAfter: retryAfter, cause: cause}
}

// Classify returns a safe view of any error. Unknown errors conservatively
// become retryable transient failures.
func Classify(err error) Failure {
	if err == nil {
		return Failure{}
	}
	var failure *Failure
	if errors.As(err, &failure) && failure != nil {
		return *failure
	}
	return Failure{Class: FailureTransient, Retryable: true, cause: err}
}
