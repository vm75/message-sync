package transport

import (
	"errors"
	"time"
)

// FailureClass is the only failure detail that may cross the transport
// boundary into routing, persistence, logs, or APIs.
type FailureClass string

// SendCertainty describes what is known about a provider create attempt.
// DefinitelyNotSent is safe to retry; Unknown is never blindly retried.
type SendCertainty uint8

const (
	SendDefinitelyNotSent SendCertainty = iota
	SendAccepted
	SendUnknown
)

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
	Certainty  SendCertainty
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
	return NewFailureWithCertainty(class, retryAfter, SendDefinitelyNotSent, cause)
}

// NewFailureWithCertainty preserves the existing safe failure class while
// allowing create callers to avoid a blind retry after an ambiguous request.
func NewFailureWithCertainty(class FailureClass, retryAfter time.Duration, certainty SendCertainty, cause error) error {
	if cause == nil {
		return nil
	}
	if !validFailureClass(class) {
		class = FailureTransient
	}
	if retryAfter < 0 {
		retryAfter = 0
	}
	if certainty > SendUnknown {
		certainty = SendUnknown
	}
	return &Failure{Class: class, Retryable: classRetryable(class), RetryAfter: retryAfter, Certainty: certainty, cause: cause}
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
	// Unclassified errors are retained as the legacy pre-acceptance contract.
	// Provider adapters must use NewFailureWithCertainty for API outcomes whose
	// acceptance cannot be known.
	return Failure{Class: FailureTransient, Retryable: true, Certainty: SendDefinitelyNotSent, cause: err}
}
