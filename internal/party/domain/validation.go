// Package domain holds the person rules: Turkish-aware name folding, identifier
// normalisation, validation and masking, and the period invariants of relationships and
// sponsor memberships. It depends on nothing outside the standard library, golang.org/x/text
// and the shared Turkish identifier checksums, so every rule is unit-testable.
package domain

import (
	"errors"
	"fmt"
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
	return fmt.Sprintf("party: %d validation error(s)", len(e.Fields))
}

// Add appends one field error; the application layer uses it for catalog-dependent rules.
func (e *ValidationError) Add(field, code, message string) {
	e.Fields = append(e.Fields, FieldError{Field: field, Code: code, Message: message})
}

// Len reports how many field errors were collected.
func (e *ValidationError) Len() int { return len(e.Fields) }

// OrNil returns nil when nothing failed, so callers can `return ve.OrNil()`.
func (e *ValidationError) OrNil() error {
	if len(e.Fields) == 0 {
		return nil
	}
	return e
}

// ErrValidation lets callers detect a ValidationError with errors.Is.
var ErrValidation = errors.New("party: validation failed")

// Is lets errors.Is(err, ErrValidation) match a *ValidationError.
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

// Field error codes shared with the OpenAPI problem contract (WP-I2-01 section 3.3).
const (
	CodeIdentifierTypeUnknown  = "IDENTIFIER_TYPE_UNKNOWN"
	CodeIdentifierInvalid      = "IDENTIFIER_INVALID"
	CodeIdentifierScopeNeeded  = "IDENTIFIER_SCOPE_REQUIRED"
	CodePrincipalRequired      = "PRINCIPAL_REQUIRED"
	CodeIdentifierDuplicated   = "IDENTIFIER_DUPLICATE"
	CodeSponsorOrganizationBad = "SPONSOR_ORGANIZATION_INVALID"
)
