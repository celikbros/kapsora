package outbox

import (
	"errors"
	"testing"
	"time"
)

func TestBackoffDoublesWithJitterAndCap(t *testing.T) {
	noJitter := func() float64 { return 0.5 }
	base, maxDelay := 5*time.Second, time.Hour

	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second}
	for i, w := range want {
		if got := Backoff(i+1, KindTransient, base, maxDelay, noJitter); got != w {
			t.Errorf("attempt %d: got %s want %s", i+1, got, w)
		}
	}
	if got := Backoff(30, KindTransient, base, maxDelay, noJitter); got != maxDelay {
		t.Errorf("cap: got %s want %s", got, maxDelay)
	}
	if got := Backoff(1, KindRateLimited, base, maxDelay, noJitter); got != 20*time.Second {
		t.Errorf("rate limited first delay: got %s want 20s", got)
	}

	low := Backoff(1, KindTransient, base, maxDelay, func() float64 { return 0 })
	high := Backoff(1, KindTransient, base, maxDelay, func() float64 { return 1 })
	if low != 4*time.Second || high != 6*time.Second {
		t.Errorf("jitter bounds: low=%s high=%s", low, high)
	}
}

func TestErrorKinds(t *testing.T) {
	base := errors.New("boom")
	cases := map[Kind]error{
		KindTransient:   Transient(base),
		KindRateLimited: RateLimited(base),
		KindPermanent:   Permanent(base),
		KindSecurity:    Security(base),
	}
	for kind, err := range cases {
		if KindOf(err) != kind {
			t.Errorf("kind of %v = %v, want %v", err, KindOf(err), kind)
		}
		if !errors.Is(err, base) {
			t.Errorf("wrapped error must unwrap to the cause")
		}
	}
	if KindOf(base) != KindTransient {
		t.Errorf("plain errors default to transient")
	}
	if Transient(nil) != nil {
		t.Errorf("wrapping nil must stay nil")
	}
}
