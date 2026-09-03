package domain

import (
	"errors"
	"fmt"
	"regexp"
	"unicode/utf8"
)

// Closed enumerations from migration 000002.
var (
	OrganizationKinds   = []string{"BANK", "INSURER", "SPONSOR", "PROVIDER", "VENDOR", "PUBLIC_BODY", "OTHER"}
	RelationshipRoles   = []string{"PAYER", "SPONSOR", "PROVIDER", "VENDOR", "PARTNER"}
	RelationshipUpdates = []string{"ACTIVE", "SUSPENDED", "TERMINATED"} // PENDING is never set by hand
)

var (
	countryPattern    = regexp.MustCompile(`^[A-Z]{2}$`)
	tenantCodePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,79}$`)
)

// Length limits mirror the OpenAPI contract.
const (
	MinNameLength        = 2
	MaxLegalNameLength   = 300
	MaxDisplayNameLength = 200
	MaxIdentifiers       = 10
)

// FieldError names one invalid request field; Field uses the JSON path of the contract.
type FieldError struct {
	Field   string
	Code    string
	Message string
}

// ValidationError aggregates field errors for a 422 response.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("organization: %d validation error(s)", len(e.Fields))
}

func (e *ValidationError) add(field, code, message string) {
	e.Fields = append(e.Fields, FieldError{Field: field, Code: code, Message: message})
}

// ErrValidation lets callers detect a ValidationError with errors.As without importing
// the concrete type everywhere.
var ErrValidation = errors.New("organization: validation failed")

// Identifier is one registry identifier as submitted, after normalisation.
type Identifier struct {
	Type    IdentifierType
	Value   string
	Primary bool
}

// NewOrganization is a validated create command.
type NewOrganization struct {
	LegalName        string
	DisplayName      string
	OrganizationKind string
	RelationshipRole string
	CountryCode      string
	TenantCode       string
	Identifiers      []Identifier
}

// TaxNumber returns the single VKN/TCKN identifier, if any.
func (n NewOrganization) TaxNumber() (Identifier, bool) {
	for _, id := range n.Identifiers {
		if id.Type.IsTaxNumber() {
			return id, true
		}
	}
	return Identifier{}, false
}

// OtherIdentifiers returns the identifiers stored in the public identifier table.
func (n NewOrganization) OtherIdentifiers() []Identifier {
	out := make([]Identifier, 0, len(n.Identifiers))
	for _, id := range n.Identifiers {
		if !id.Type.IsTaxNumber() {
			out = append(out, id)
		}
	}
	return out
}

// ValidateNew normalises and validates a create command in place. It reports every
// problem at once so the client can fix the whole form in one round trip.
func ValidateNew(n *NewOrganization) error {
	ve := &ValidationError{}
	if l := utf8.RuneCountInString(n.LegalName); l < MinNameLength || l > MaxLegalNameLength {
		ve.add("legalName", "LENGTH", fmt.Sprintf("%d-%d karakter olmalı", MinNameLength, MaxLegalNameLength))
	}
	if l := utf8.RuneCountInString(n.DisplayName); l < MinNameLength || l > MaxDisplayNameLength {
		ve.add("displayName", "LENGTH", fmt.Sprintf("%d-%d karakter olmalı", MinNameLength, MaxDisplayNameLength))
	}
	if !contains(OrganizationKinds, n.OrganizationKind) {
		ve.add("organizationKind", "ENUM", "geçersiz kurum türü")
	}
	if !contains(RelationshipRoles, n.RelationshipRole) {
		ve.add("relationshipRole", "ENUM", "geçersiz ilişki rolü")
	}
	if n.CountryCode == "" {
		n.CountryCode = "TR"
	}
	if !countryPattern.MatchString(n.CountryCode) {
		ve.add("countryCode", "FORMAT", "iki harfli ülke kodu olmalı")
	}
	if n.TenantCode != "" && !tenantCodePattern.MatchString(n.TenantCode) {
		ve.add("tenantCode", "FORMAT", "harf, rakam, '.', '_' ve '-' ile 1-80 karakter olmalı")
	}
	if len(n.Identifiers) == 0 || len(n.Identifiers) > MaxIdentifiers {
		ve.add("identifiers", "LENGTH", fmt.Sprintf("1-%d tanımlayıcı verilmeli", MaxIdentifiers))
	}

	taxNumbers := 0
	for i := range n.Identifiers {
		id := &n.Identifiers[i]
		field := fmt.Sprintf("identifiers[%d].value", i)
		if !id.Type.Valid() {
			ve.add(fmt.Sprintf("identifiers[%d].type", i), "ENUM", "geçersiz tanımlayıcı türü")
			continue
		}
		id.Value = Normalize(id.Type, id.Value)
		if id.Type.IsTaxNumber() {
			taxNumbers++
		}
		if err := Validate(id.Type, id.Value); err != nil {
			ve.add(field, "IDENTIFIER_INVALID", "tanımlayıcı doğrulanamadı")
		}
	}
	switch {
	case taxNumbers > 1:
		ve.add("identifiers", "IDENTIFIER_CONFLICT", "yalnız bir VKN veya TCKN verilebilir")
	case taxNumbers == 0 && n.CountryCode == "TR" && len(n.Identifiers) > 0:
		ve.add("identifiers", "IDENTIFIER_REQUIRED", "Türkiye için VKN veya TCKN zorunludur")
	}
	if len(ve.Fields) > 0 {
		return ve
	}
	return nil
}

// ValidateUpdate checks the fields of a merge-patch. Nil pointers mean "unchanged".
func ValidateUpdate(displayName, relationshipStatus, tenantCode *string) error {
	ve := &ValidationError{}
	if displayName != nil {
		if l := utf8.RuneCountInString(*displayName); l < MinNameLength || l > MaxDisplayNameLength {
			ve.add("displayName", "LENGTH", fmt.Sprintf("%d-%d karakter olmalı", MinNameLength, MaxDisplayNameLength))
		}
	}
	if relationshipStatus != nil && !contains(RelationshipUpdates, *relationshipStatus) {
		ve.add("relationshipStatus", "ENUM", "ACTIVE, SUSPENDED veya TERMINATED olmalı")
	}
	if tenantCode != nil && !tenantCodePattern.MatchString(*tenantCode) {
		ve.add("tenantCode", "FORMAT", "harf, rakam, '.', '_' ve '-' ile 1-80 karakter olmalı")
	}
	if len(ve.Fields) > 0 {
		return ve
	}
	return nil
}

// Is lets errors.Is(err, ErrValidation) match a *ValidationError.
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
