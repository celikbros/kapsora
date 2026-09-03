package domain

import (
	"errors"
	"time"
)

// Lockout describes the brute-force protection applied per account. HTTP-level rate
// limiting (internal/platform/ratelimit) is the first line of defence; this is the second.
type Lockout struct {
	MaxFailedAttempts int
	Duration          time.Duration
}

// DefaultLockout locks an account for 15 minutes after 10 consecutive failures. The
// counter resets on the first successful login.
func DefaultLockout() Lockout {
	return Lockout{MaxFailedAttempts: 10, Duration: 15 * time.Minute}
}

var errInvalidLockout = errors.New("identity: lockout needs 3-100 attempts and a 1 minute to 24 hour duration")

// Validate rejects nonsensical lockout settings.
func (l Lockout) Validate() error {
	if l.MaxFailedAttempts < 3 || l.MaxFailedAttempts > 100 {
		return errInvalidLockout
	}
	if l.Duration < time.Minute || l.Duration > 24*time.Hour {
		return errInvalidLockout
	}
	return nil
}

// LockedUntil returns the instant the account stays locked until after a failure, or the
// zero time when the threshold has not been reached yet.
func (l Lockout) LockedUntil(failedAttempts int, now time.Time) time.Time {
	if failedAttempts < l.MaxFailedAttempts {
		return time.Time{}
	}
	return now.Add(l.Duration)
}

// IsLocked reports whether a stored lock is still in force.
func (l Lockout) IsLocked(lockedUntil, now time.Time) bool {
	return !lockedUntil.IsZero() && now.Before(lockedUntil)
}
