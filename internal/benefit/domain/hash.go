package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// ConfigurationVersion is the version tag of the canonical form below. It is part of the
// hashed document, so a future change to the serialisation cannot silently produce the
// same hash for a different reading of the same configuration.
const ConfigurationVersion = 1

// CanonicalConfiguration returns the byte string a plan version's configuration hash is
// taken over (WP-I2-02 section 3):
//
//   - object keys are sorted (encoding/json sorts map keys),
//   - definitions are sorted by code,
//   - every numeric value is a string with trailing zeros trimmed,
//   - the validity bounds are ISO dates, absent bounds are null.
//
// Two readings of the same stored configuration therefore produce identical bytes,
// whatever order the rows arrived in.
func CanonicalConfiguration(validFrom, validTo *time.Time, defs []EntitlementDefinition) ([]byte, error) {
	sorted := make([]EntitlementDefinition, len(defs))
	copy(sorted, defs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Code < sorted[j].Code })

	items := make([]any, 0, len(sorted))
	for _, d := range sorted {
		item := map[string]any{
			"code":            d.Code,
			"name":            d.Name,
			"unitType":        d.UnitType,
			"periodType":      d.PeriodType,
			"initialQuantity": TrimDecimal(d.InitialQuantity),
			"allowOverdraft":  d.AllowOverdraft,
			"rolloverPolicy":  d.RolloverPolicy,
			"familyShared":    d.FamilyShared,
			"currencyCode":    nullableString(d.CurrencyCode),
			"rolloverCap":     nullableDecimal(d.RolloverCap),
			"periodLength":    nullableInt(d.PeriodLength),
		}
		items = append(items, item)
	}
	doc := map[string]any{
		"schema":      ConfigurationVersion,
		"validFrom":   nullableDate(validFrom),
		"validTo":     nullableDate(validTo),
		"definitions": items,
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("benefit: canonical configuration: %w", err)
	}
	return out, nil
}

// ConfigurationHash is the SHA-256 of CanonicalConfiguration, stored as bytea and shown
// to clients as lower-case hex.
func ConfigurationHash(validFrom, validTo *time.Time, defs []EntitlementDefinition) ([]byte, error) {
	canonical, err := CanonicalConfiguration(validFrom, validTo, defs)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	return sum[:], nil
}

// HexHash renders a stored configuration hash for the API; an empty hash becomes "".
func HexHash(sum []byte) string {
	if len(sum) == 0 {
		return ""
	}
	return hex.EncodeToString(sum)
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableDecimal(s string) any {
	if s == "" {
		return nil
	}
	return TrimDecimal(s)
}

func nullableInt(v *int) any {
	if v == nil {
		return nil
	}
	// Numeric values are strings in the canonical form, so no float ever reaches JSON.
	return fmt.Sprintf("%d", *v)
}

func nullableDate(t *time.Time) any {
	if t == nil {
		return nil
	}
	return DateOnly(*t).Format(time.DateOnly)
}
