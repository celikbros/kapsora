package outbox

import "errors"

// Kind classifies a handler failure (v1.2 section 23.2).
type Kind int

const (
	// KindTransient retries with exponential backoff; the default for plain errors.
	KindTransient Kind = iota
	// KindRateLimited retries like transient but starts from a longer delay.
	KindRateLimited
	// KindPermanent dead-letters immediately (validation errors, unknown aggregate).
	KindPermanent
	// KindSecurity dead-letters immediately and is logged at error level.
	KindSecurity
)

// String returns the stable code stored in last_error_code.
func (k Kind) String() string {
	switch k {
	case KindRateLimited:
		return "RATE_LIMITED"
	case KindPermanent:
		return "PERMANENT"
	case KindSecurity:
		return "SECURITY"
	default:
		return "TRANSIENT"
	}
}

type kindError struct {
	kind Kind
	err  error
}

func (e *kindError) Error() string { return e.kind.String() + ": " + e.err.Error() }
func (e *kindError) Unwrap() error { return e.err }

// Transient wraps err as retryable.
func Transient(err error) error { return wrap(KindTransient, err) }

// RateLimited wraps err as retryable after a longer delay.
func RateLimited(err error) error { return wrap(KindRateLimited, err) }

// Permanent wraps err as non-retryable.
func Permanent(err error) error { return wrap(KindPermanent, err) }

// Security wraps err as non-retryable and security relevant.
func Security(err error) error { return wrap(KindSecurity, err) }

func wrap(kind Kind, err error) error {
	if err == nil {
		return nil
	}
	return &kindError{kind: kind, err: err}
}

// KindOf returns the classification of err; unclassified errors are transient.
func KindOf(err error) Kind {
	var ke *kindError
	if errors.As(err, &ke) {
		return ke.kind
	}
	return KindTransient
}
