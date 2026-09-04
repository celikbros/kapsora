package domain_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/rules/domain"
)

// TestSnapshotKeepsIdsDatesAndQuantities and drops everything else. The application test
// checks the bytes that reach the column; this one pins the rule itself, value by value,
// so a future change to the filter fails here rather than in a database test whose failure
// is harder to read.
func TestSnapshotKeepsIdsDatesAndQuantities(t *testing.T) {
	id := uuid.NewString()
	kept := map[string]any{
		"claimId":      id,
		"serviceDate":  "2026-03-15",
		"createdAt":    "2026-03-15T09:30:00Z",
		"amount":       "1250.75",
		"sessionCount": 4.0,
		"age":          38.0,
		"covered":      true,
		"serviceCode":  "PHYSIO_SESSION",
		"planCode":     "GOLD-2026",
	}
	dropped := map[string]any{
		"memberName":     "Ayşe Yılmaz",
		"holderFullName": "Ayşe Yılmaz",
		"tckn":           "10000000146",
		"nationalNumber": "10000000146",
		"bareIdentity":   10000000146.0,
		"cardNumber":     "4111 1111 1111 1111",
		"phone":          "0532 123 45 67",
		"note":           "hasta üçüncü katta yatıyor",
		"emptyString":    "",
		"absent":         nil,
	}
	input := map[string]any{}
	for k, v := range kept {
		input[k] = v
	}
	for k, v := range dropped {
		input[k] = v
	}

	out := domain.Snapshot(input)
	for key, want := range kept {
		got, present := out[key]
		if !present {
			t.Errorf("snapshot dropped %s, which an auditor needs", key)
			continue
		}
		if s, ok := want.(string); ok && got != s {
			t.Errorf("snapshot changed %s: %v, want %v", key, got, want)
		}
	}
	for key := range dropped {
		if _, present := out[key]; present {
			t.Errorf("snapshot kept %s = %v", key, out[key])
		}
	}
}

// TestSnapshotRecursesAndStillFilters: nesting must not be a way past the filter.
func TestSnapshotRecursesAndStillFilters(t *testing.T) {
	out := domain.Snapshot(map[string]any{
		"claim": map[string]any{
			"amount":  "100",
			"patient": map[string]any{"memberName": "Ayşe", "personId": uuid.NewString()},
			"lines": []any{
				map[string]any{"serviceCode": "PHYSIO_SESSION", "note": "sol diz"},
			},
		},
	})
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for _, forbidden := range []string{"Ayşe", "memberName", "sol diz"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("nested snapshot %s contains %q", encoded, forbidden)
		}
	}
	if !strings.Contains(string(encoded), "PHYSIO_SESSION") {
		t.Fatalf("nested snapshot %s lost the service code", encoded)
	}
}

// TestContentHashIsStableAndOrderIndependent: two readings of the same stored version have
// to hash identically, whatever order the rows arrived in, or the hash proves nothing.
func TestContentHashIsStableAndOrderIndependent(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rules := []domain.RuleContent{
		{Code: "SECOND", Name: "İkinci", Priority: 20, Condition: "true", ExplanationCode: "B", Active: true},
		{Code: "FIRST", Name: "Birinci", Priority: 10, Condition: "false", ExplanationCode: "A", Active: true,
			Actions: []domain.ActionInput{{Type: "REJECT", Payload: map[string]any{"reasonCode": "NO"}}}},
	}
	content := domain.Content{
		ValidFrom: &from, InputSchema: map[string]string{"claim": "map", "serviceDate": "timestamp"},
		Rules: rules,
	}
	first, err := domain.ContentHash(content)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	reversed := domain.Content{
		ValidFrom: &from, InputSchema: map[string]string{"serviceDate": "timestamp", "claim": "map"},
		Rules: []domain.RuleContent{rules[1], rules[0]},
	}
	second, err := domain.ContentHash(reversed)
	if err != nil {
		t.Fatalf("hash again: %v", err)
	}
	if first != second {
		t.Fatalf("row order changed the hash: %s vs %s", first, second)
	}
	if len(first) != 64 {
		t.Fatalf("hash is %d characters, want 64", len(first))
	}

	// A change anybody would call a different rule set changes the hash.
	changed := content
	changed.Rules = append([]domain.RuleContent(nil), rules...)
	changed.Rules[0].Condition = "1 == 1"
	third, err := domain.ContentHash(changed)
	if err != nil {
		t.Fatalf("hash changed content: %v", err)
	}
	if third == first {
		t.Fatal("a changed condition hashed identically")
	}
}

// TestInputHashIsOrderIndependent, which is what makes two evaluations comparable without
// keeping the input itself.
func TestInputHashIsOrderIndependent(t *testing.T) {
	a, err := domain.InputHash(map[string]any{"b": "2", "a": "1"})
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	b, err := domain.InputHash(map[string]any{"a": "1", "b": "2"})
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if string(a) != string(b) {
		t.Fatal("the same input built in another order hashed differently")
	}
	if len(a) != 32 {
		t.Fatalf("hash is %d bytes, want 32", len(a))
	}
	c, err := domain.InputHash(map[string]any{"a": "1", "b": "3"})
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if string(a) == string(c) {
		t.Fatal("two different inputs hashed identically")
	}
}
