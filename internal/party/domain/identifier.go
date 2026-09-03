package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	organization "github.com/celikbros/kapsora/internal/organization/domain"
)

// Identifier type codes with type-specific validation or masking. Every other code is a
// tenant-defined catalog row and gets the generic treatment.
const (
	TypeTCKN       = "TCKN"
	TypePassport   = "PASSPORT"
	TypeMemberNo   = "MEMBER_NO"
	TypeEmployeeNo = "EMPLOYEE_NO"
)

// Uniqueness scopes of party.identifier_type (D5).
const (
	ScopeTenant  = "TENANT"
	ScopeSponsor = "SPONSOR"
	ScopeNone    = "NONE"
)

// Status values shared by the three party catalogs.
const (
	StatusActive   = "ACTIVE"
	StatusInactive = "INACTIVE"
)

// MaxIdentifiers is the contract limit on identifiers per request.
const MaxIdentifiers = 10

// ErrIdentifierInvalid is returned when a value fails its type's rules. It never carries
// the value itself.
var ErrIdentifierInvalid = errors.New("party: identifier fails validation")

var (
	identifierSeparators = strings.NewReplacer(" ", "", "\t", "", "-", "", "\u00a0", "")
	identifierPattern    = regexp.MustCompile(`^[0-9A-Z][0-9A-Z._/]{0,79}$`)
	typeCodePattern      = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
)

// ValidTypeCode reports whether a catalog code has the shape the database enforces.
func ValidTypeCode(code string) bool { return typeCodePattern.MatchString(code) }

// NormalizeIdentifier trims, removes spaces and dashes and upper-cases. Blind indexes,
// validation and masking all run on this form (WP-I2-01 section 3.2).
func NormalizeIdentifier(raw string) string {
	return strings.ToUpper(identifierSeparators.Replace(strings.TrimSpace(raw)))
}

// ValidateIdentifier checks a normalized value for its type. TCKN carries the Turkish
// national identity checksum; every other type is only shape-checked.
func ValidateIdentifier(typeCode, normalized string) error {
	if typeCode == TypeTCKN {
		if err := organization.ValidateTCKN(normalized); err != nil {
			return fmt.Errorf("%w: %s", ErrIdentifierInvalid, typeCode)
		}
		return nil
	}
	if !identifierPattern.MatchString(normalized) {
		return fmt.Errorf("%w: %s", ErrIdentifierInvalid, typeCode)
	}
	return nil
}

// MaskIdentifier is the only representation of an identifier that may leave the process:
// TCKN keeps three leading and two trailing digits, member and employee numbers their
// last three characters, passports their first two, everything else nothing at all.
func MaskIdentifier(typeCode, normalized string) string {
	n := len(normalized)
	switch {
	case typeCode == TypeTCKN && n == 11:
		return organization.Mask(organization.IdentifierTCKN, normalized)
	case typeCode == TypeMemberNo || typeCode == TypeEmployeeNo:
		if n > 3 {
			return strings.Repeat("*", n-3) + normalized[n-3:]
		}
		return strings.Repeat("*", n)
	case typeCode == TypePassport:
		if n > 2 {
			return normalized[:2] + strings.Repeat("*", n-2)
		}
		return strings.Repeat("*", n)
	default:
		return "**"
	}
}

// BlindIndexInput is the string handed to crypto.BlindIndexer: the type qualifies the
// value so a member number can never collide with a passport number.
func BlindIndexInput(typeCode, normalized string) string { return typeCode + ":" + normalized }

// ScopeKey implements identifier_type.uniqueness_scope (D5). TENANT is the empty string,
// SPONSOR the sponsor's tenant_organization id and NONE the identifier row's own id, which
// never collides. ok is false when a SPONSOR-scoped identifier has no sponsor to bind to.
func ScopeKey(scope, sponsorID, rowID string) (key string, ok bool) {
	switch scope {
	case ScopeSponsor:
		if sponsorID == "" {
			return "", false
		}
		return sponsorID, true
	case ScopeNone:
		return rowID, true
	default:
		return "", true
	}
}
