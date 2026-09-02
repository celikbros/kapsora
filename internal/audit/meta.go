package audit

import (
	"context"
	"encoding/json"
	"net/netip"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

// RequestMeta carries the per-request correlation data the recorder copies onto every
// audit row. The HTTP middleware in transport/http sets it; jobs may set it themselves.
type RequestMeta struct {
	RequestID     uuid.NullUUID
	TraceID       string
	SourceIP      netip.Addr // zero value when unknown
	UserAgentHash []byte
}

type metaKey struct{}

// WithRequestMeta attaches meta to ctx.
func WithRequestMeta(ctx context.Context, m RequestMeta) context.Context {
	return context.WithValue(ctx, metaKey{}, m)
}

// RequestMetaFrom returns the meta stored in ctx, or the zero value.
func RequestMetaFrom(ctx context.Context) RequestMeta {
	m, _ := ctx.Value(metaKey{}).(RequestMeta)
	return m
}

// MaxDetailString is the longest string value kept in detail_json.
const MaxDetailString = 200

var (
	detailKeyPattern   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	forbiddenFragments = []string{"tckn", "vkn", "identifier", "name", "email", "token", "secret", "password"}
)

// SanitizeDetail keeps only safe, flat, non-personal values: keys matching
// ^[a-z][a-z0-9_]*$ that contain none of the forbidden fragments, with string values of at
// most MaxDetailString characters, numbers, booleans and UUIDs. Everything else is dropped
// silently so an audit write never fails because of a careless caller.
func SanitizeDetail(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		if !detailKeyPattern.MatchString(key) || hasForbiddenFragment(key) {
			continue
		}
		switch v := value.(type) {
		case string:
			if len(v) <= MaxDetailString {
				out[key] = v
			}
		case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
			out[key] = v
		case uuid.UUID:
			out[key] = v.String()
		case uuid.NullUUID:
			if v.Valid {
				out[key] = v.UUID.String()
			}
		}
	}
	return out
}

func hasForbiddenFragment(key string) bool {
	lower := strings.ToLower(key)
	for _, f := range forbiddenFragments {
		if strings.Contains(lower, f) {
			return true
		}
	}
	return false
}
