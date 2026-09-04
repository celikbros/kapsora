package domain

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestHashAndVerifyPassword(t *testing.T) {
	p := DefaultPasswordParams()
	hash, err := HashPassword("correct horse battery staple", p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected encoding: %s", hash)
	}
	if strings.Contains(hash, "correct") {
		t.Fatal("hash must not contain the password")
	}

	ok, rehash, err := VerifyPassword("correct horse battery staple", hash, p)
	if err != nil || !ok || rehash {
		t.Fatalf("verify: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	ok, _, err = VerifyPassword("wrong horse battery staple", hash, p)
	if err != nil || ok {
		t.Fatalf("wrong password accepted: ok=%v err=%v", ok, err)
	}

	// Two hashes of the same password differ (random salt).
	other, _ := HashPassword("correct horse battery staple", p)
	if other == hash {
		t.Fatal("salt is not random")
	}
}

func TestVerifyPasswordDetectsWeakerParametersAndBadHashes(t *testing.T) {
	weak := PasswordParams{MemoryKiB: 8192, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	hash, err := HashPassword("correct horse battery staple", weak)
	if err != nil {
		t.Fatal(err)
	}
	ok, rehash, err := VerifyPassword("correct horse battery staple", hash, DefaultPasswordParams())
	if err != nil || !ok || !rehash {
		t.Fatalf("expected a valid password needing a rehash: ok=%v rehash=%v err=%v", ok, rehash, err)
	}

	for name, bad := range map[string]string{
		"empty":        "",
		"not argon2id": "$argon2i$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA",
		"short":        "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA",
		"bad version":  "$argon2id$v=16$m=19456,t=2,p=1$c2FsdA$aGFzaA",
		"bad params":   "$argon2id$v=19$m=abc,t=2,p=1$c2FsdA$aGFzaA",
		"bad base64":   "$argon2id$v=19$m=19456,t=2,p=1$!!!$aGFzaA",
	} {
		if _, _, err := VerifyPassword("x", bad, DefaultPasswordParams()); !errors.Is(err, ErrInvalidHash) {
			t.Errorf("%s: err = %v, want ErrInvalidHash", name, err)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	if err := ValidatePassword("correct horse battery staple", "ahmet@example.com"); err != nil {
		t.Fatalf("good password rejected: %v", err)
	}
	cases := map[string]struct {
		password, username string
		want               error
	}{
		"too short":       {"short12", "", ErrPasswordTooShort},
		"too long":        {strings.Repeat("a", MaxPasswordSizeBytes+1), "", ErrPasswordTooLong},
		"contains user":   {"ahmet-guclu-parola", "ahmet@example.com", ErrPasswordTooCommon},
		"short user part": {"abc-guclu-parola-x", "abc@example.com", nil}, // < 4 chars is not checked
	}
	for name, c := range cases {
		if err := ValidatePassword(c.password, c.username); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
	// An over-long password must never reach the hasher.
	if _, err := HashPassword(strings.Repeat("a", MaxPasswordSizeBytes+1), DefaultPasswordParams()); !errors.Is(err, ErrPasswordTooLong) {
		t.Errorf("hashing an over-long password: %v", err)
	}
}

// The property under test is a security one: an unknown username must cost about as much
// as a known one, or the response time itself answers "does this account exist?".
//
// It is measured as the fastest of several runs rather than a single sample. A single
// sample measures whatever else the machine was doing — one preemption in the middle of
// either side skews the ratio by more than the property ever would — while the minimum is
// the run that came closest to uncontended, which is the number the comparison is about.
func TestBurnPasswordTimeCostsRoughlyTheSameAsAVerification(t *testing.T) {
	hash, _ := HashPassword("correct horse battery staple", DefaultPasswordParams())

	fastest := func(run func()) time.Duration {
		best := time.Duration(math.MaxInt64)
		for i := 0; i < 5; i++ {
			start := time.Now()
			run()
			if d := time.Since(start); d < best {
				best = d
			}
		}
		return best
	}

	real := fastest(func() {
		_, _, _ = VerifyPassword("wrong password entirely", hash, DefaultPasswordParams())
	})
	dummy := fastest(func() { BurnPasswordTime("wrong password entirely") })

	if real <= 0 || dummy <= 0 {
		t.Fatal("measurements are degenerate")
	}
	ratio := float64(dummy) / float64(real)
	if ratio < 0.25 || ratio > 4 {
		t.Fatalf("unknown-user path takes %s versus %s for a real verification", dummy, real)
	}
}

func TestTurkishPassphraseIsNotPenalised(t *testing.T) {
	// 20 characters, more than 20 bytes: accepted because the cap counts bytes.
	pass := "çilekli düğün pastası"
	if len(pass) <= utf8.RuneCountInString(pass) {
		t.Fatal("fixture is not multi-byte")
	}
	if err := ValidatePassword(pass, "ahmet@example.com"); err != nil {
		t.Fatalf("Turkish passphrase rejected: %v", err)
	}
	hash, err := HashPassword(pass, DefaultPasswordParams())
	if err != nil {
		t.Fatal(err)
	}
	if ok, _, err := VerifyPassword(pass, hash, DefaultPasswordParams()); err != nil || !ok {
		t.Fatalf("verify: ok=%v err=%v", ok, err)
	}
}

func TestNormalizeUsername(t *testing.T) {
	for in, want := range map[string]string{
		"  Ahmet@Example.COM ": "ahmet@example.com",
		"ADMIN.A":              "admin.a",
		"":                     "",
	} {
		if got := NormalizeUsername(in); got != want {
			t.Errorf("NormalizeUsername(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLockout(t *testing.T) {
	l := DefaultLockout()
	if err := l.Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	for _, bad := range []Lockout{{MaxFailedAttempts: 1, Duration: time.Hour}, {MaxFailedAttempts: 10, Duration: time.Second}} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%+v should be rejected", bad)
		}
	}

	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	if got := l.LockedUntil(l.MaxFailedAttempts-1, now); !got.IsZero() {
		t.Fatalf("below the threshold there is no lock, got %v", got)
	}
	until := l.LockedUntil(l.MaxFailedAttempts, now)
	if !until.Equal(now.Add(l.Duration)) {
		t.Fatalf("lock end = %v", until)
	}
	if !l.IsLocked(until, now) || l.IsLocked(until, until) || l.IsLocked(time.Time{}, now) {
		t.Fatal("IsLocked boundaries are wrong")
	}
}
