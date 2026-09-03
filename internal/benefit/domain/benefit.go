// Package domain holds the benefit rules: the status machines of programs, plans, plan
// versions and enrollments, the shape of an entitlement definition, exact decimal
// handling for quantities, and the canonical serialisation the configuration hash is
// taken over. It depends on nothing outside the standard library, so every rule is
// unit-testable.
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
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
	return fmt.Sprintf("benefit: %d validation error(s)", len(e.Fields))
}

// Add appends one field error.
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
var ErrValidation = errors.New("benefit: validation failed")

// Is lets errors.Is(err, ErrValidation) match a *ValidationError.
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

// Program statuses (migration 000004).
const (
	ProgramDraft     = "DRAFT"
	ProgramActive    = "ACTIVE"
	ProgramSuspended = "SUSPENDED"
	ProgramClosed    = "CLOSED"
)

// Plan statuses.
const (
	PlanDraft   = "DRAFT"
	PlanActive  = "ACTIVE"
	PlanRetired = "RETIRED"
)

// Plan version statuses.
const (
	VersionDraft       = "DRAFT"
	VersionUnderReview = "UNDER_REVIEW"
	VersionPublished   = "PUBLISHED"
	VersionRetired     = "RETIRED"
)

// Enrollment statuses.
const (
	EnrollmentPending   = "PENDING"
	EnrollmentActive    = "ACTIVE"
	EnrollmentSuspended = "SUSPENDED"
	EnrollmentEnded     = "ENDED"
)

// Status sets accepted on the wire.
var (
	ProgramStatuses        = []string{ProgramDraft, ProgramActive, ProgramSuspended, ProgramClosed}
	ProgramUpdateStatuses  = []string{ProgramActive, ProgramSuspended, ProgramClosed}
	PlanUpdateStatuses     = []string{PlanActive, PlanRetired}
	EnrollmentStatuses     = []string{EnrollmentPending, EnrollmentActive, EnrollmentSuspended, EnrollmentEnded}
	EnrollmentCreate       = []string{EnrollmentPending, EnrollmentActive}
	EnrollmentUpdates      = []string{EnrollmentActive, EnrollmentSuspended, EnrollmentEnded}
	SponsorRoles           = []string{"SPONSOR", "PAYER"}
	UnitTypes              = []string{"MONEY", "COUNT", "NIGHT", "SESSION", "HOUR", "KILOMETER", "POINT"}
	PeriodTypes            = []string{"CALENDAR_YEAR", "PLAN_YEAR", "ROLLING_DAYS", "LIFETIME", "CUSTOM"}
	RolloverPolicies       = []string{"NONE", "FULL", "CAPPED"}
	programTransitions     = map[string][]string{ProgramDraft: {ProgramActive}, ProgramActive: {ProgramSuspended, ProgramClosed}, ProgramSuspended: {ProgramActive, ProgramClosed}}
	planTransitions        = map[string][]string{PlanDraft: {PlanActive}, PlanActive: {PlanRetired}}
	enrollmentTransitions  = map[string][]string{EnrollmentPending: {EnrollmentActive, EnrollmentEnded}, EnrollmentActive: {EnrollmentSuspended, EnrollmentEnded}, EnrollmentSuspended: {EnrollmentActive, EnrollmentEnded}}
	programCodePattern     = regexp.MustCompile(`^[A-Z][A-Z0-9_-]{1,39}$`)
	programTypePattern     = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
	entitlementCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
	currencyPattern        = regexp.MustCompile(`^[A-Z]{3}$`)
	decimalPattern         = regexp.MustCompile(`^-?[0-9]{1,20}(\.[0-9]{1,20})?$`)
)

// Length limits mirror the OpenAPI contract.
const (
	MinNameLength    = 2
	MaxNameLength    = 200
	MaxNotesLength   = 2000
	MaxCommentLength = 1000
	MaxReasonLength  = 200
	MaxDefinitions   = 100
	// MaxScale is the scale of numeric(20,6); MaxPrecision its total digit count.
	MaxScale     = 6
	MaxPrecision = 20
)

// Contains reports whether value is in set.
func Contains(set []string, value string) bool {
	for _, s := range set {
		if s == value {
			return true
		}
	}
	return false
}

// ProgramTransitionAllowed reports whether a program may move from -> to. Staying in the
// same status is always allowed so a merge-patch may repeat the current value.
func ProgramTransitionAllowed(from, to string) bool { return transition(programTransitions, from, to) }

// PlanTransitionAllowed reports whether a plan may move from -> to.
func PlanTransitionAllowed(from, to string) bool { return transition(planTransitions, from, to) }

// EnrollmentTransitionAllowed reports whether an enrollment may move from -> to.
func EnrollmentTransitionAllowed(from, to string) bool {
	return transition(enrollmentTransitions, from, to)
}

func transition(table map[string][]string, from, to string) bool {
	if from == to {
		return true
	}
	return Contains(table[from], to)
}

// ValidateProgramCode checks the tenant-unique program code.
func ValidateProgramCode(ve *ValidationError, code string) {
	if !programCodePattern.MatchString(code) {
		ve.Add("code", "FORMAT", "büyük harf, rakam, _ ve - ile 2-40 karakter olmalı")
	}
}

// ValidatePlanCode checks the program-unique plan code (same shape as a program code).
func ValidatePlanCode(ve *ValidationError, code string) {
	if !programCodePattern.MatchString(code) {
		ve.Add("code", "FORMAT", "büyük harf, rakam, _ ve - ile 2-40 karakter olmalı")
	}
}

// ValidateProgramType checks the catalog code format before the catalog lookup.
func ValidateProgramType(ve *ValidationError, code string) {
	if !programTypePattern.MatchString(code) {
		ve.Add("programType", "FORMAT", "geçersiz program türü kodu")
	}
}

// ValidateName checks a display name against the contract length limits.
func ValidateName(ve *ValidationError, field, value string) {
	l := utf8.RuneCountInString(strings.TrimSpace(value))
	if l < MinNameLength || l > MaxNameLength {
		ve.Add(field, "LENGTH", fmt.Sprintf("%d-%d karakter olmalı", MinNameLength, MaxNameLength))
	}
}

// ValidateText checks an optional free-text field against a maximum rune count.
func ValidateText(ve *ValidationError, field, value string, maxRunes int) {
	if utf8.RuneCountInString(value) > maxRunes {
		ve.Add(field, "LENGTH", fmt.Sprintf("en fazla %d karakter olmalı", maxRunes))
	}
}

// ValidatePeriod enforces a non-empty, ordered validity period. Both bounds are optional
// (an unbounded daterange); when both are present the end must be after the start
// because daterange uses '[)' and an empty range would defeat the exclusion constraints.
func ValidatePeriod(ve *ValidationError, prefix string, from, to *time.Time) {
	if from != nil && to != nil && !to.After(*from) {
		ve.Add(prefix+"To", "RANGE", "bitiş tarihi başlangıçtan sonra olmalı")
	}
}

// DateOnly strips the clock from a contract date so comparisons stay day-based.
func DateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// CoversDate reports whether [from, to) contains day; nil bounds are unbounded.
func CoversDate(from, to *time.Time, day time.Time) bool {
	day = DateOnly(day)
	if from != nil && day.Before(DateOnly(*from)) {
		return false
	}
	if to != nil && !day.Before(DateOnly(*to)) {
		return false
	}
	return true
}

// EntitlementDefinition is one row of a plan version configuration. Quantities are exact
// decimal text (never a float); an empty RolloverCap means "not set".
type EntitlementDefinition struct {
	Code            string
	Name            string
	UnitType        string
	CurrencyCode    string
	PeriodType      string
	PeriodLength    *int
	InitialQuantity string
	AllowOverdraft  bool
	RolloverPolicy  string
	RolloverCap     string
	FamilyShared    bool
}

// ValidateDefinitions applies the contract rules and the CHECK constraints of migration
// 000005 up front, so the caller gets field errors instead of a database error. Field
// paths follow the request body: items[i].<property>.
func ValidateDefinitions(defs []EntitlementDefinition) error {
	ve := &ValidationError{}
	if len(defs) > MaxDefinitions {
		ve.Add("items", "LENGTH", fmt.Sprintf("en fazla %d tanım olabilir", MaxDefinitions))
		return ve.OrNil()
	}
	seen := make(map[string]struct{}, len(defs))
	for i := range defs {
		d := &defs[i]
		at := func(name string) string { return fmt.Sprintf("items[%d].%s", i, name) }
		if !entitlementCodePattern.MatchString(d.Code) {
			ve.Add(at("code"), "FORMAT", "büyük harf, rakam ve _ ile 2-64 karakter olmalı")
		} else if _, dup := seen[d.Code]; dup {
			ve.Add(at("code"), "DUPLICATE", "aynı kod bu sürümde birden fazla kez var")
		} else {
			seen[d.Code] = struct{}{}
		}
		ValidateName(ve, at("name"), d.Name)
		if !Contains(UnitTypes, d.UnitType) {
			ve.Add(at("unitType"), "ENUM", "geçersiz birim türü")
		}
		if !Contains(PeriodTypes, d.PeriodType) {
			ve.Add(at("periodType"), "ENUM", "geçersiz dönem türü")
		}
		if d.RolloverPolicy == "" {
			d.RolloverPolicy = "NONE"
		}
		if !Contains(RolloverPolicies, d.RolloverPolicy) {
			ve.Add(at("rolloverPolicy"), "ENUM", "geçersiz devir politikası")
		}
		// ck_entitlement_currency
		switch {
		case d.UnitType == "MONEY" && d.CurrencyCode == "":
			ve.Add(at("currencyCode"), "REQUIRED", "MONEY birimi için para birimi zorunlu")
		case d.UnitType != "MONEY" && d.CurrencyCode != "":
			ve.Add(at("currencyCode"), "FORBIDDEN", "para birimi yalnızca MONEY biriminde verilir")
		case d.CurrencyCode != "" && !currencyPattern.MatchString(d.CurrencyCode):
			ve.Add(at("currencyCode"), "FORMAT", "ISO-4217 üç harfli kod olmalı")
		}
		// ck_entitlement_period_length
		switch {
		case d.PeriodType == "ROLLING_DAYS" && (d.PeriodLength == nil || *d.PeriodLength <= 0):
			ve.Add(at("periodLength"), "REQUIRED", "ROLLING_DAYS için gün sayısı zorunlu")
		case d.PeriodType != "ROLLING_DAYS" && d.PeriodLength != nil:
			ve.Add(at("periodLength"), "FORBIDDEN", "gün sayısı yalnızca ROLLING_DAYS için verilir")
		}
		// ck_entitlement_rollover_cap
		switch {
		case d.RolloverPolicy == "CAPPED" && d.RolloverCap == "":
			ve.Add(at("rolloverCap"), "REQUIRED", "CAPPED devir için üst sınır zorunlu")
		case d.RolloverPolicy != "CAPPED" && d.RolloverCap != "":
			ve.Add(at("rolloverCap"), "FORBIDDEN", "üst sınır yalnızca CAPPED devirde verilir")
		}
		quantity(ve, at("initialQuantity"), &d.InitialQuantity, true)
		quantity(ve, at("rolloverCap"), &d.RolloverCap, false)
	}
	return ve.OrNil()
}

// quantity normalises a decimal in place and reports a field error when it is not a
// non-negative numeric(20,6) value. Empty is accepted when the value is optional.
func quantity(ve *ValidationError, field string, value *string, required bool) {
	raw := strings.TrimSpace(*value)
	if raw == "" {
		if required {
			ve.Add(field, "REQUIRED", "zorunlu alan")
		}
		*value = ""
		return
	}
	normalized, err := NormalizeDecimal(raw)
	if err != nil {
		ve.Add(field, "FORMAT", err.Error())
		return
	}
	if strings.HasPrefix(normalized, "-") {
		ve.Add(field, "RANGE", "negatif olamaz")
		return
	}
	*value = normalized
}

// ErrDecimalFormat is the reason a quantity was rejected.
var ErrDecimalFormat = errors.New("ondalık sayı biçimi geçersiz")

// NormalizeDecimal returns the canonical form of an exact decimal: no leading zeros
// beyond one, no trailing fractional zeros, no trailing decimal point, "0" for zero. It
// enforces the numeric(20,6) envelope of the entitlement columns. Money never travels
// through a float in this codebase, so the value stays text end to end.
func NormalizeDecimal(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" || !decimalPattern.MatchString(s) {
		return "", ErrDecimalFormat
	}
	negative := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intPart, fracPart, _ := strings.Cut(s, ".")
	intPart = strings.TrimLeft(intPart, "0")
	if intPart == "" {
		intPart = "0"
	}
	fracPart = strings.TrimRight(fracPart, "0")
	if len(fracPart) > MaxScale {
		return "", fmt.Errorf("en fazla %d ondalık basamak olabilir", MaxScale)
	}
	if len(intPart)+len(fracPart) > MaxPrecision {
		return "", fmt.Errorf("en fazla %d basamak olabilir", MaxPrecision)
	}
	out := intPart
	if fracPart != "" {
		out += "." + fracPart
	}
	if negative && out != "0" {
		out = "-" + out
	}
	return out, nil
}

// TrimDecimal is NormalizeDecimal for values read back from PostgreSQL, which renders
// numeric(20,6) with all six fractional digits. An unparsable value is returned as is so
// a read never fails on data the database accepted.
func TrimDecimal(raw string) string {
	if out, err := NormalizeDecimal(raw); err == nil {
		return out
	}
	return raw
}

// likeEscaper neutralises the LIKE wildcards a user may type in a list filter; the SQL
// side uses PostgreSQL's default backslash escape character.
var likeEscaper = strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)

// LikePattern wraps a trimmed search term in the contains-pattern the ILIKE filters
// expect, with the user's own wildcards escaped so "%" searches for a literal percent
// sign instead of matching every row. An empty term means "no filter".
func LikePattern(q string) string {
	trimmed := strings.TrimSpace(q)
	if trimmed == "" {
		return ""
	}
	return "%" + likeEscaper.Replace(trimmed) + "%"
}
