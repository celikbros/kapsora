package httpx

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Keyset cursors (v1.2 section 17.3). A cursor names the last row of a page by
// (created_at, id); it is opaque to clients and signed so a tampered or hand-built cursor
// is rejected instead of steering the query. Layout before base64url:
//
//	8 bytes unix nanoseconds (big endian) | 16 bytes uuid | 32 bytes HMAC-SHA256
const cursorSize = 8 + 16 + 32

// ErrInvalidCursor is returned for malformed, tampered or foreign cursors.
var ErrInvalidCursor = errors.New("httpx: invalid cursor")

// Cursor is the decoded keyset position.
type Cursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// CursorCodec encodes and verifies cursors with one server-side key.
type CursorCodec struct {
	key []byte
}

// NewCursorCodec returns a codec; the key must be at least 16 bytes.
func NewCursorCodec(key []byte) (*CursorCodec, error) {
	if len(key) < 16 {
		return nil, errors.New("httpx: cursor key must be at least 16 bytes")
	}
	return &CursorCodec{key: append([]byte(nil), key...)}, nil
}

// Encode returns the opaque cursor for c.
func (c *CursorCodec) Encode(cur Cursor) string {
	buf := make([]byte, 0, cursorSize)
	buf = binary.BigEndian.AppendUint64(buf, uint64(cur.CreatedAt.UnixNano()))
	buf = append(buf, cur.ID[:]...)
	buf = append(buf, c.sign(buf)...)
	return base64.RawURLEncoding.EncodeToString(buf)
}

// Decode verifies and parses an opaque cursor. An empty string is the first page.
func (c *CursorCodec) Decode(raw string) (Cursor, bool, error) {
	if raw == "" {
		return Cursor{}, false, nil
	}
	buf, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(buf) != cursorSize {
		return Cursor{}, false, ErrInvalidCursor
	}
	payload, mac := buf[:24], buf[24:]
	if !hmac.Equal(mac, c.sign(payload)) {
		return Cursor{}, false, ErrInvalidCursor
	}
	nanos := int64(binary.BigEndian.Uint64(payload[:8])) //nolint:gosec // round-trips the value written by Encode
	id, err := uuid.FromBytes(payload[8:24])
	if err != nil {
		return Cursor{}, false, ErrInvalidCursor
	}
	return Cursor{CreatedAt: time.Unix(0, nanos).UTC(), ID: id}, true, nil
}

func (c *CursorCodec) sign(payload []byte) []byte {
	mac := hmac.New(sha256.New, c.key)
	mac.Write([]byte("kapsora-cursor-v1|"))
	mac.Write(payload)
	return mac.Sum(nil)
}

// Pagination limits (v1.2 section 17.3).
const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// ClampLimit applies the default and maximum page size.
func ClampLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultPageSize
	case limit > MaxPageSize:
		return MaxPageSize
	default:
		return limit
	}
}
