package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/celikbros/kapsora/internal/identity"
)

func TestNewTokenIsRandomAndLongEnough(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if len(tok) < 43 {
			t.Fatalf("token %q too short for %d bytes of entropy", tok, TokenBytes)
		}
		if strings.ContainsAny(tok, "+/=") {
			t.Fatalf("token %q is not base64url without padding", tok)
		}
		if seen[tok] {
			t.Fatalf("token repeated: %s", tok)
		}
		seen[tok] = true
	}
	if len(TokenHash("abc")) != 32 {
		t.Fatalf("hash length")
	}
}

func TestDeriveCSRFTokenIsStableSecretAndComparedSafely(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	a := DeriveCSRFToken(key, "session-one")
	if a != DeriveCSRFToken(key, "session-one") {
		t.Fatal("derivation must be deterministic")
	}
	if a == DeriveCSRFToken(key, "session-two") {
		t.Fatal("different sessions must get different tokens")
	}
	if a == DeriveCSRFToken([]byte("another-key-another-key-anotherk"), "session-one") {
		t.Fatal("different keys must get different tokens")
	}
	if strings.Contains(a, "session-one") {
		t.Fatal("token must not reveal the session id")
	}
	if !CSRFTokenMatches(a, a) || CSRFTokenMatches(a, a+"x") || CSRFTokenMatches(a, "") {
		t.Fatal("constant-time comparison is wrong")
	}
}

func TestPolicyValidate(t *testing.T) {
	if err := DefaultPolicy().Validate(); err != nil {
		t.Fatalf("defaults must be valid: %v", err)
	}
	base := DefaultPolicy()
	bad := map[string]Policy{
		"short idle":       {IdleTimeout: time.Second, AbsoluteLifetime: time.Hour, StepUpWindow: base.StepUpWindow, TouchInterval: time.Minute},
		"absolute < idle":  {IdleTimeout: time.Hour, AbsoluteLifetime: time.Minute, StepUpWindow: base.StepUpWindow, TouchInterval: time.Minute},
		"step-up too long": {IdleTimeout: base.IdleTimeout, AbsoluteLifetime: base.AbsoluteLifetime, StepUpWindow: 2 * time.Hour, TouchInterval: time.Minute},
		"touch too long":   {IdleTimeout: base.IdleTimeout, AbsoluteLifetime: base.AbsoluteLifetime, StepUpWindow: base.StepUpWindow, TouchInterval: time.Hour},
	}
	for name, p := range bad {
		if err := p.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestEvaluateIdleAbsoluteAndStepUp(t *testing.T) {
	p := DefaultPolicy()
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	fresh := identity.Session{
		CreatedAt:  now.Add(-time.Hour),
		LastSeenAt: now.Add(-10 * time.Second),
		ExpiresAt:  now.Add(7 * time.Hour),
	}

	if got := p.Evaluate(fresh, now); got != StatusActive {
		t.Fatalf("fresh session = %s", got)
	}
	idle := fresh
	idle.LastSeenAt = now.Add(-31 * time.Minute)
	if got := p.Evaluate(idle, now); got != StatusIdle {
		t.Fatalf("idle session = %s", got)
	}
	expired := fresh
	expired.ExpiresAt = now.Add(-time.Second)
	if got := p.Evaluate(expired, now); got != StatusExpired {
		t.Fatalf("expired session = %s", got)
	}
	// Absolute expiry wins over recent activity.
	both := fresh
	both.ExpiresAt = now
	both.LastSeenAt = now
	if got := p.Evaluate(both, now); got != StatusExpired {
		t.Fatalf("absolute expiry must win: %s", got)
	}

	if p.NeedsTouch(fresh, now) {
		t.Fatal("a session seen ten seconds ago must not be written again")
	}
	if !p.NeedsTouch(identity.Session{LastSeenAt: now.Add(-p.TouchInterval)}, now) {
		t.Fatal("exactly one touch interval must trigger a write")
	}
	if !p.NeedsTouch(idle, now) {
		t.Fatal("a stale last_seen_at must be touched")
	}

	if p.StepUpValid(fresh, now) {
		t.Fatal("no step-up recorded")
	}
	stepped := fresh
	stepped.StepUpUntil = now.Add(time.Minute)
	if !p.StepUpValid(stepped, now) {
		t.Fatal("step-up within the window must be valid")
	}
	stepped.StepUpUntil = now.Add(-time.Second)
	if p.StepUpValid(stepped, now) {
		t.Fatal("expired step-up must not be valid")
	}
}
