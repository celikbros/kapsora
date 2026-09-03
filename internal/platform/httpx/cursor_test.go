package httpx

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCursorRoundTripAndTamperDetection(t *testing.T) {
	codec, err := NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	want := Cursor{CreatedAt: time.Date(2026, 9, 3, 10, 30, 0, 123456789, time.UTC), ID: uuid.New()}
	raw := codec.Encode(want)

	got, ok, err := codec.Decode(raw)
	if err != nil || !ok {
		t.Fatalf("decode: ok=%v err=%v", ok, err)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) || got.ID != want.ID {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
	if _, ok, err := codec.Decode(""); ok || err != nil {
		t.Fatalf("empty cursor must mean first page: ok=%v err=%v", ok, err)
	}

	// Flip one character of the payload: the signature no longer matches.
	tampered := []byte(raw)
	if tampered[3] == 'A' {
		tampered[3] = 'B'
	} else {
		tampered[3] = 'A'
	}
	if _, _, err := codec.Decode(string(tampered)); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("tampered cursor accepted: %v", err)
	}
	for name, bad := range map[string]string{
		"garbage":   "not-a-cursor",
		"truncated": raw[:len(raw)-4],
		"too long":  raw + "AAAA",
		"padded":    raw + "=",
	} {
		if _, _, err := codec.Decode(bad); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}

	// A cursor from another deployment (different key) is foreign.
	other, _ := NewCursorCodec([]byte("fedcba9876543210fedcba9876543210"))
	if _, _, err := other.Decode(raw); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("foreign cursor accepted: %v", err)
	}
	if _, err := NewCursorCodec([]byte("short")); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestClampLimit(t *testing.T) {
	for in, want := range map[int]int{0: 50, -5: 50, 1: 1, 200: 200, 201: 200, 999: 200} {
		if got := ClampLimit(in); got != want {
			t.Errorf("ClampLimit(%d) = %d, want %d", in, got, want)
		}
	}
}
