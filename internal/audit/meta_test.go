package audit

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestSanitizeDetailKeepsSafeValuesOnly(t *testing.T) {
	id := uuid.New()
	in := map[string]any{
		"organization_id": id,
		"line_count":      3,
		"amount_minor":    int64(1250),
		"ratio":           0.5,
		"approved":        true,
		"reason_code":     "LIMIT",
		"nested":          map[string]any{"x": 1},
		"list":            []string{"a"},
		"too_long":        strings.Repeat("x", MaxDetailString+1),
		"BadKey":          "x",
		"tckn":            "12345678901",
		"customer_email":  "a@b.c",
		"api_token":       "t",
		"display_name":    "Ada",
		"nullable_id":     uuid.NullUUID{},
		"present_id":      uuid.NullUUID{UUID: id, Valid: true},
	}
	out := SanitizeDetail(in)

	for _, want := range []string{"organization_id", "line_count", "amount_minor", "ratio", "approved", "reason_code", "present_id"} {
		if _, ok := out[want]; !ok {
			t.Errorf("expected key %q to be kept", want)
		}
	}
	for _, drop := range []string{"nested", "list", "too_long", "BadKey", "tckn", "customer_email", "api_token", "display_name", "nullable_id"} {
		if _, ok := out[drop]; ok {
			t.Errorf("expected key %q to be dropped", drop)
		}
	}
	if out["organization_id"] != id.String() {
		t.Errorf("uuid should be stored as string, got %v", out["organization_id"])
	}
	if SanitizeDetail(nil) == nil {
		t.Errorf("nil input must yield an empty map, not nil")
	}
}
