// Package domain holds the organization directory rules: registry identifier validation,
// masking and the relationship invariants. It depends on nothing outside the standard
// library so every rule is unit-testable.
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// IdentifierType mirrors the OpenAPI enum for organization identifiers.
type IdentifierType string

const (
	IdentifierVKN              IdentifierType = "VKN"
	IdentifierTCKN             IdentifierType = "TCKN"
	IdentifierMERSIS           IdentifierType = "MERSIS"
	IdentifierProviderRegistry IdentifierType = "PROVIDER_REGISTRY"
	IdentifierOther            IdentifierType = "OTHER"
)

// IsTaxNumber reports whether the type is stored encrypted on the organization row.
func (t IdentifierType) IsTaxNumber() bool { return t == IdentifierVKN || t == IdentifierTCKN }

// Valid reports whether the type is known.
func (t IdentifierType) Valid() bool {
	switch t {
	case IdentifierVKN, IdentifierTCKN, IdentifierMERSIS, IdentifierProviderRegistry, IdentifierOther:
		return true
	}
	return false
}

// Validation errors. They name the rule, never the value.
var (
	ErrIdentifierInvalid  = errors.New("organization: identifier fails validation")
	ErrIdentifierRequired = errors.New("organization: a VKN or TCKN is required")
	ErrIdentifierType     = errors.New("organization: unknown identifier type")
)

var (
	digitsOnly   = regexp.MustCompile(`^[0-9]+$`)
	otherPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,79}$`)
	spaceRemover = strings.NewReplacer(" ", "", "\t", "", "-", "")
)

// Normalize prepares a raw identifier for validation and hashing: trims, strips spaces and
// dashes, and upper-cases free-form values. Blind indexes are computed on this form.
func Normalize(t IdentifierType, raw string) string {
	v := strings.TrimSpace(raw)
	if t.IsTaxNumber() || t == IdentifierMERSIS {
		return spaceRemover.Replace(v)
	}
	return strings.ToUpper(spaceRemover.Replace(v))
}

// Validate checks a normalized identifier for its type.
func Validate(t IdentifierType, normalized string) error {
	switch t {
	case IdentifierVKN:
		return ValidateVKN(normalized)
	case IdentifierTCKN:
		return ValidateTCKN(normalized)
	case IdentifierMERSIS:
		if len(normalized) != 16 || !digitsOnly.MatchString(normalized) {
			return fmt.Errorf("%w: MERSIS must be 16 digits", ErrIdentifierInvalid)
		}
		return nil
	case IdentifierProviderRegistry, IdentifierOther:
		if !otherPattern.MatchString(normalized) {
			return fmt.Errorf("%w: value must be 1-80 characters of letters, digits, '.', '_', '/' or '-'", ErrIdentifierInvalid)
		}
		return nil
	default:
		return ErrIdentifierType
	}
}

// ValidateTCKN applies the Turkish national identity number checksum: 11 digits, first
// digit not zero, d10 = ((d1+d3+d5+d7+d9)*7 - (d2+d4+d6+d8)) mod 10, d11 = sum(d1..d10) mod 10.
func ValidateTCKN(v string) error {
	if len(v) != 11 || !digitsOnly.MatchString(v) {
		return fmt.Errorf("%w: TCKN must be 11 digits", ErrIdentifierInvalid)
	}
	d := digits(v)
	if d[0] == 0 {
		return fmt.Errorf("%w: TCKN cannot start with 0", ErrIdentifierInvalid)
	}
	odd := d[0] + d[2] + d[4] + d[6] + d[8]
	even := d[1] + d[3] + d[5] + d[7]
	d10 := ((odd*7-even)%10 + 10) % 10
	if d10 != d[9] {
		return fmt.Errorf("%w: TCKN checksum", ErrIdentifierInvalid)
	}
	sum := 0
	for i := 0; i < 10; i++ {
		sum += d[i]
	}
	if sum%10 != d[10] {
		return fmt.Errorf("%w: TCKN checksum", ErrIdentifierInvalid)
	}
	return nil
}

// ValidateVKN applies the Turkish tax number checksum over 10 digits.
func ValidateVKN(v string) error {
	if len(v) != 10 || !digitsOnly.MatchString(v) {
		return fmt.Errorf("%w: VKN must be 10 digits", ErrIdentifierInvalid)
	}
	d := digits(v)
	sum := 0
	for i := 1; i <= 9; i++ {
		tmp := (d[i-1] + 10 - i) % 10
		if tmp == 9 {
			sum += 9
		} else {
			sum += (tmp * (1 << (10 - i))) % 9
		}
	}
	check := (10 - sum%10) % 10
	if check != d[9] {
		return fmt.Errorf("%w: VKN checksum", ErrIdentifierInvalid)
	}
	return nil
}

// Mask hides the middle of an identifier for display and audit (WP-I1-03 section 2.5).
func Mask(t IdentifierType, normalized string) string {
	n := len(normalized)
	switch {
	case t == IdentifierVKN && n == 10:
		return normalized[:2] + strings.Repeat("*", 6) + normalized[8:]
	case t == IdentifierTCKN && n == 11:
		return normalized[:3] + strings.Repeat("*", 6) + normalized[9:]
	case t == IdentifierMERSIS && n >= 4:
		return strings.Repeat("*", n-4) + normalized[n-4:]
	case n > 2:
		return normalized[:2] + strings.Repeat("*", n-2)
	default:
		return strings.Repeat("*", n)
	}
}

// TaxNumberType infers VKN or TCKN from a stored (decrypted) tax number by length.
func TaxNumberType(normalized string) IdentifierType {
	if len(normalized) == 11 {
		return IdentifierTCKN
	}
	return IdentifierVKN
}

func digits(v string) []int {
	out := make([]int, len(v))
	for i := 0; i < len(v); i++ {
		out[i] = int(v[i] - '0')
	}
	return out
}
