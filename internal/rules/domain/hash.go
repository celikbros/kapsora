package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ContentVersion is the version tag of the canonical form below. It is part of the hashed
// document, so a future change to the serialisation cannot silently produce the same hash
// for a different reading of the same rules.
const ContentVersion = 1

// Content is everything a published rule set version froze: its period, the variables it
// declared and every rule it holds. It is assembled by the application layer from the
// stored rows and hashed here.
type Content struct {
	ValidFrom   *time.Time
	ValidTo     *time.Time
	InputSchema map[string]string
	Rules       []RuleContent
}

// RuleContent is one rule as the hash sees it.
type RuleContent struct {
	Code              string
	Name              string
	Priority          int
	Condition         string
	Actions           []ActionInput
	ExplanationCode   string
	ExplanationParams map[string]any
	StopOnMatch       bool
	Active            bool
}

// CanonicalContent returns the byte string a version's content hash is taken over:
//
//   - object keys are sorted (encoding/json sorts map keys),
//   - rules are sorted by priority and then by code,
//   - the priority is written as a decimal string, so no float reaches JSON,
//   - dates are ISO days and absent values are null.
//
// Two readings of the same stored version therefore produce identical bytes, whatever
// order the rows arrived in.
func CanonicalContent(c Content) ([]byte, error) {
	rules := append([]RuleContent(nil), c.Rules...)
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].Priority != rules[j].Priority {
			return rules[i].Priority < rules[j].Priority
		}
		return rules[i].Code < rules[j].Code
	})
	ruleDocs := make([]any, 0, len(rules))
	for _, r := range rules {
		actionDocs := make([]any, 0, len(r.Actions))
		for _, a := range r.Actions {
			actionDocs = append(actionDocs, map[string]any{
				"type":    a.Type,
				"payload": emptyObject(a.Payload),
			})
		}
		ruleDocs = append(ruleDocs, map[string]any{
			"code":              r.Code,
			"name":              r.Name,
			"priority":          fmt.Sprintf("%d", r.Priority),
			"condition":         r.Condition,
			"actions":           actionDocs,
			"explanationCode":   r.ExplanationCode,
			"explanationParams": emptyObject(r.ExplanationParams),
			"stopOnMatch":       r.StopOnMatch,
			"active":            r.Active,
		})
	}
	schema := make(map[string]any, len(c.InputSchema))
	for name, kind := range c.InputSchema {
		schema[name] = kind
	}
	doc := map[string]any{
		"schema":      ContentVersion,
		"validFrom":   nullableDate(c.ValidFrom),
		"validTo":     nullableDate(c.ValidTo),
		"inputSchema": schema,
		"rules":       ruleDocs,
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("rules: canonical content: %w", err)
	}
	return out, nil
}

// ContentHash is the SHA-256 of CanonicalContent as lower-case hex, which is what
// rule_set_version.content_hash stores and what the API returns.
func ContentHash(c Content) (string, error) {
	canonical, err := CanonicalContent(c)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// InputHash is the SHA-256 of the canonical JSON of one evaluation input. encoding/json
// sorts object keys, so the same input twice produces the same 32 bytes whatever order it
// was built in — which is what lets two evaluations be compared without keeping the input
// itself.
func InputHash(input map[string]any) ([]byte, error) {
	canonical, err := json.Marshal(emptyObject(input))
	if err != nil {
		return nil, fmt.Errorf("rules: input hash: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return sum[:], nil
}

// deniedKeyParts are the key name fragments that never reach an evaluation snapshot,
// whatever the value looks like. The value-shape rules below would already drop a person's
// name; this list says so explicitly, so a reader of the code does not have to derive the
// intent from a regular expression. Both languages appear because the input document is
// keyed by whoever authored the rule set, and they write in Turkish; the Turkish entries
// are prefixes, so their inflected forms are caught by the same fragment.
var deniedKeyParts = []string{
	"name", "isim", "soyad", "tckn", "vkn", "identity", "identifier", "kimlik",
	"passport", "pasaport", "email", "eposta", "phone", "telefon", "gsm", "iban",
	"address", "adre",
}

var (
	digitsOnly = regexp.MustCompile(`^[0-9]+$`)
	// snapshotCode is what an audit trail may keep as a free-standing string: an
	// upper-case code, never a sentence and never a person's name.
	snapshotCode = regexp.MustCompile(`^[A-Z][A-Z0-9_.:-]{0,63}$`)
)

const (
	// maxSnapshotDepth bounds the walk; a rule input is a small document, and anything
	// deeper is either a mistake or an attempt to smuggle something past the filter.
	maxSnapshotDepth = 8
	// minIdentityDigits is where a bare run of digits stops being a quantity and starts
	// looking like an identity, a card or a telephone number. A TCKN is 11 digits, a VKN
	// 10; a session count, an age or a day count is never that long.
	minIdentityDigits = 10
)

// Snapshot reduces an evaluation input to what rules.evaluation.input_snapshot may hold:
// ids, dates, codes and quantities, and nothing else. Everything that is not recognisably
// one of those is dropped rather than masked, because a masked value still tells a reader
// that the field was there and how long it was.
//
// This is the reason the column exists at all: an audit trail has to be readable years
// later by whoever is arguing about the decision, and a trail that itself needs protecting
// is a trail nobody will be allowed to read.
func Snapshot(input map[string]any) map[string]any {
	out, _ := snapshotMap(input, 0)
	return out
}

func snapshotMap(in map[string]any, depth int) (map[string]any, bool) {
	if depth > maxSnapshotDepth {
		return nil, false
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		if deniedKey(key) {
			continue
		}
		kept, ok := snapshotValue(value, depth+1)
		if !ok {
			continue
		}
		out[key] = kept
	}
	return out, true
}

func snapshotValue(value any, depth int) (any, bool) {
	if depth > maxSnapshotDepth {
		return nil, false
	}
	switch v := value.(type) {
	case nil:
		return nil, false
	case bool:
		return v, true
	case string:
		return snapshotString(v)
	case time.Time:
		return v.UTC().Format(time.RFC3339), true
	case json.Number:
		return snapshotString(v.String())
	case float64:
		return snapshotNumber(v)
	case int:
		return snapshotNumber(float64(v))
	case int32:
		return snapshotNumber(float64(v))
	case int64:
		return snapshotNumber(float64(v))
	case map[string]any:
		return snapshotMap(v, depth)
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			kept, ok := snapshotValue(item, depth+1)
			if !ok {
				continue
			}
			out = append(out, kept)
		}
		return out, true
	default:
		return nil, false
	}
}

// snapshotNumber keeps a quantity and drops anything long enough to be an identity. A
// number is written back as a decimal string so the snapshot carries no float.
func snapshotNumber(f float64) (any, bool) {
	s := decimalOf(f)
	if isIdentityLike(s) {
		return nil, false
	}
	return s, true
}

func snapshotString(s string) (any, bool) {
	if s == "" {
		return nil, false
	}
	if isIdentityLike(s) {
		return nil, false
	}
	if _, err := uuid.Parse(s); err == nil {
		return s, true
	}
	if _, err := time.Parse(time.DateOnly, s); err == nil {
		return s, true
	}
	if _, err := time.Parse(time.RFC3339, s); err == nil {
		return s, true
	}
	if Decimal(s) {
		return s, true
	}
	if snapshotCode.MatchString(s) {
		return s, true
	}
	return nil, false
}

// deniedKey reports a field name that never reaches the snapshot whatever it holds.
func deniedKey(key string) bool {
	lower := strings.ToLower(key)
	for _, part := range deniedKeyParts {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}

// isIdentityLike reports a bare run of ten or more digits, with or without separators —
// an identity number, a card number, a telephone number. None of those belongs in an
// audit snapshot, and none of them is a quantity anybody needs to read back.
func isIdentityLike(s string) bool {
	trimmed := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '.', '/':
			return -1
		}
		return r
	}, s)
	return digitsOnly.MatchString(trimmed) && len(trimmed) >= minIdentityDigits
}

// decimalOf renders a float as the shortest exact decimal, without an exponent, so the
// snapshot never carries scientific notation a reader has to decode.
func decimalOf(f float64) string {
	s := fmt.Sprintf("%.6f", f)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

func emptyObject(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func nullableDate(t *time.Time) any {
	if t == nil {
		return nil
	}
	return DateOnly(*t).Format(time.DateOnly)
}
