// Package domain holds the session rules: token generation, hashing, expiry, idle
// timeout and the step-up window. It depends on nothing but the standard library and the
// identity port types, so every rule is unit-testable with an injected clock.
package domain

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/celikbros/kapsora/internal/identity"
)

// TokenBytes is the entropy of a session id (v1.2 section 19.2: unguessable identifiers).
const TokenBytes = 32

// NewToken returns base64url-encoded random bytes with no padding.
func NewToken() (string, error) {
	b := make([]byte, TokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("identity: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// TokenHash is what the database stores; the plaintext token exists only in the cookie.
func TokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// DeriveCSRFToken produces the per-session CSRF token. It is not stored: given the
// session id and the server signing key it is reproducible, and an attacker who cannot
// read the HttpOnly session cookie cannot compute it.
func DeriveCSRFToken(signingKey []byte, sessionID string) string {
	mac := hmac.New(sha256.New, signingKey)
	mac.Write([]byte("kapsora-csrf-v1|"))
	mac.Write([]byte(sessionID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// CSRFTokenMatches compares in constant time.
func CSRFTokenMatches(expected, presented string) bool {
	return subtle.ConstantTimeCompare([]byte(expected), []byte(presented)) == 1
}

// Policy holds the session timing rules (v1.2 section 18.2).
type Policy struct {
	IdleTimeout      time.Duration // no request for this long ends the session
	AbsoluteLifetime time.Duration // hard limit regardless of activity
	StepUpWindow     time.Duration // how long a step-up stays valid
	TouchInterval    time.Duration // minimum gap between last_seen_at writes
}

// DefaultPolicy returns the documented defaults.
func DefaultPolicy() Policy {
	return Policy{
		IdleTimeout:      30 * time.Minute,
		AbsoluteLifetime: 8 * time.Hour,
		StepUpWindow:     10 * time.Minute,
		TouchInterval:    time.Minute,
	}
}

// Validate rejects nonsensical configuration at startup.
func (p Policy) Validate() error {
	switch {
	case p.IdleTimeout < time.Minute:
		return errors.New("identity: idle timeout must be at least one minute")
	case p.AbsoluteLifetime < p.IdleTimeout:
		return errors.New("identity: absolute lifetime must not be shorter than the idle timeout")
	case p.StepUpWindow < time.Minute || p.StepUpWindow > time.Hour:
		return errors.New("identity: step-up window must be between one minute and one hour")
	case p.TouchInterval <= 0 || p.TouchInterval > p.IdleTimeout:
		return errors.New("identity: touch interval must be positive and shorter than the idle timeout")
	}
	return nil
}

// Status is the result of evaluating a loaded session against the clock.
type Status string

const (
	StatusActive  Status = "ACTIVE"
	StatusIdle    Status = "IDLE_TIMEOUT"
	StatusExpired Status = "EXPIRED"
)

// Evaluate reports whether the session may still be used.
func (p Policy) Evaluate(s identity.Session, now time.Time) Status {
	if !now.Before(s.ExpiresAt) {
		return StatusExpired
	}
	if now.Sub(s.LastSeenAt) > p.IdleTimeout {
		return StatusIdle
	}
	return StatusActive
}

// NeedsTouch limits last_seen_at writes to one per TouchInterval.
func (p Policy) NeedsTouch(s identity.Session, now time.Time) bool {
	return now.Sub(s.LastSeenAt) >= p.TouchInterval
}

// StepUpValid reports whether a step-up performed earlier is still in force.
func (p Policy) StepUpValid(s identity.Session, now time.Time) bool {
	return !s.StepUpUntil.IsZero() && now.Before(s.StepUpUntil)
}

// ExpiresAt is the absolute end of a session created now.
func (p Policy) ExpiresAt(now time.Time) time.Time { return now.Add(p.AbsoluteLifetime) }

// StepUpUntil is the end of the step-up window starting now.
func (p Policy) StepUpUntil(now time.Time) time.Time { return now.Add(p.StepUpWindow) }
