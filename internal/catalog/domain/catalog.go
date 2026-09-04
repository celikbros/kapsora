// Package domain holds the catalog rules: the shape of a service category tree, of a
// service definition, of an external code system and of the mapping between a definition
// and the codes other parties report it under. It depends on nothing outside the standard
// library, so every rule is unit-testable without a database.
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
	return fmt.Sprintf("catalog: %d validation error(s)", len(e.Fields))
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
var ErrValidation = errors.New("catalog: validation failed")

// Is lets errors.Is(err, ErrValidation) match a *ValidationError.
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

// ErrCategoryCycle is raised when a parent assignment would close a loop in the tree.
var ErrCategoryCycle = errors.New("catalog: category parent would close a cycle")

// ErrMappingOverlap is raised when the submitted mapping set contradicts itself; the
// same condition against rows already stored is caught by the database exclusion
// constraint and mapped to the identical problem code.
var ErrMappingOverlap = errors.New("catalog: code mapping periods overlap")

// MaxCategoryDepth caps how deep the category tree may grow, so a picker stays navigable
// (WP-I3-01 section 2.1). The root counts as level one.
const MaxCategoryDepth = 6

// MaxImportRows is the largest code value batch one import call accepts.
const MaxImportRows = 5000

// Service domains (migration 000006 ck_service_category domain_code).
var ServiceDomains = []string{
	"GENERIC", "HEALTH", "ACCOMMODATION", "ASSISTANCE", "EDUCATION",
	"SPORT", "TRANSPORT", "CARE", "OTHER",
}

// FulfillmentModes are the ways a definition is delivered (migration 000006).
var FulfillmentModes = []string{
	"APPOINTMENT", "RESERVATION", "WORK_ORDER", "MEMBERSHIP",
	"SESSION", "VOUCHER", "REIMBURSEMENT", "DIRECT",
}

// UnitTypes are the units a definition is counted in (migration 000006).
var UnitTypes = []string{"MONEY", "COUNT", "NIGHT", "SESSION", "HOUR", "KILOMETER", "POINT"}

// CodeSystemAuthorities are the publishers a code system may name (migration 000019).
var CodeSystemAuthorities = []string{"SGK", "SB", "WHO", "TENANT", "OTHER"}

// CodeSystemStatuses are the lifecycle states of a code system (migration 000019).
var CodeSystemStatuses = []string{"ACTIVE", "INACTIVE"}

// Code shapes; the database repeats every one of them as a CHECK constraint.
var (
	catalogCodePattern    = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
	codeSystemCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,39}$`)
)

// Contains reports whether list holds v.
func Contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// NewCategory is the validated create command for a service category.
type NewCategory struct {
	Code     string
	Name     string
	Domain   string
	ParentID string // empty means a root category
	Active   bool
}

// ValidateNewCategory checks the fields the database would otherwise reject with a raw
// constraint error.
func ValidateNewCategory(in NewCategory) error {
	ve := &ValidationError{}
	validateCatalogCode(ve, "code", in.Code)
	validateName(ve, in.Name)
	if !Contains(ServiceDomains, in.Domain) {
		ve.Add("domain", "ENUM", "geçersiz hizmet alanı")
	}
	return ve.OrNil()
}

// CategoryPatch is a merge-patch of a category; nil leaves a field unchanged.
type CategoryPatch struct {
	Name            *string
	ParentID        *string
	ClearParent     bool
	Active          *bool
	ExpectedVersion int64
}

// ValidateCategoryPatch checks the fields a merge-patch may carry.
func ValidateCategoryPatch(p CategoryPatch) error {
	ve := &ValidationError{}
	if p.Name != nil {
		validateName(ve, *p.Name)
	}
	return ve.OrNil()
}

// ValidateCategoryPlacement decides whether a category may sit under a parent.
//
// ancestors is the chain from the proposed parent up to its root, the parent first; it is
// empty for a root category. subtreeHeight is how many levels hang below the category
// being placed, the category itself counting as one, so a leaf and a brand new category
// both pass 1. A cycle is a 409 because the request is well formed but contradicts the
// stored tree; too much depth is a field error on parentId.
func ValidateCategoryPlacement(subjectID string, ancestors []string, subtreeHeight int) error {
	if subjectID != "" {
		for _, id := range ancestors {
			if id == subjectID {
				return ErrCategoryCycle
			}
		}
	}
	if subtreeHeight < 1 {
		subtreeHeight = 1
	}
	if len(ancestors)+subtreeHeight > MaxCategoryDepth {
		ve := &ValidationError{}
		ve.Add("parentId", "DEPTH_EXCEEDED",
			fmt.Sprintf("kategori ağacı en fazla %d seviye olabilir", MaxCategoryDepth))
		return ve
	}
	return nil
}

// NewDefinition is the validated create command for a service definition.
type NewDefinition struct {
	CategoryID       string
	Code             string
	Name             string
	Description      string
	FulfillmentMode  string
	DefaultUnitType  string
	RequiresProvider bool
	Active           bool
}

// ValidateNewDefinition checks the fields of a create command.
func ValidateNewDefinition(in NewDefinition) error {
	ve := &ValidationError{}
	validateCatalogCode(ve, "code", in.Code)
	validateName(ve, in.Name)
	validateDescription(ve, "description", in.Description)
	if !Contains(FulfillmentModes, in.FulfillmentMode) {
		ve.Add("fulfillmentMode", "ENUM", "geçersiz karşılama biçimi")
	}
	if !Contains(UnitTypes, in.DefaultUnitType) {
		ve.Add("defaultUnitType", "ENUM", "geçersiz birim türü")
	}
	return ve.OrNil()
}

// DefinitionPatch is a merge-patch of a service definition.
type DefinitionPatch struct {
	CategoryID       *string
	Name             *string
	Description      *string
	ClearDescription bool
	FulfillmentMode  *string
	DefaultUnitType  *string
	RequiresProvider *bool
	Active           *bool
	ExpectedVersion  int64
}

// ValidateDefinitionPatch checks the fields a merge-patch may carry.
func ValidateDefinitionPatch(p DefinitionPatch) error {
	ve := &ValidationError{}
	if p.Name != nil {
		validateName(ve, *p.Name)
	}
	if p.Description != nil {
		validateDescription(ve, "description", *p.Description)
	}
	if p.FulfillmentMode != nil && !Contains(FulfillmentModes, *p.FulfillmentMode) {
		ve.Add("fulfillmentMode", "ENUM", "geçersiz karşılama biçimi")
	}
	if p.DefaultUnitType != nil && !Contains(UnitTypes, *p.DefaultUnitType) {
		ve.Add("defaultUnitType", "ENUM", "geçersiz birim türü")
	}
	return ve.OrNil()
}

// NewCodeSystem is the validated create command for a code system edition.
type NewCodeSystem struct {
	Code      string
	Name      string
	Version   string
	Authority string
	Licensed  bool
	ValidFrom time.Time
	ValidTo   *time.Time
}

// ValidateNewCodeSystem checks the fields of a create command.
func ValidateNewCodeSystem(in NewCodeSystem) error {
	ve := &ValidationError{}
	if !codeSystemCodePattern.MatchString(in.Code) {
		ve.Add("code", "FORMAT", "2-40 karakter, büyük harf ve rakamlarla A-Z ile başlamalı")
	}
	validateName(ve, in.Name)
	if n := utf8.RuneCountInString(strings.TrimSpace(in.Version)); n < 1 || n > 32 {
		ve.Add("version", "LENGTH", "1-32 karakter olmalı")
	}
	if !Contains(CodeSystemAuthorities, in.Authority) {
		ve.Add("authority", "ENUM", "geçersiz otorite")
	}
	if in.ValidFrom.IsZero() {
		ve.Add("validFrom", "REQUIRED", "zorunlu alan")
	}
	validatePeriod(ve, "validTo", in.ValidFrom, in.ValidTo)
	return ve.OrNil()
}

// CodeSystemPatch is a merge-patch of a code system.
type CodeSystemPatch struct {
	Name            *string
	Authority       *string
	Licensed        *bool
	Status          *string
	ValidTo         *time.Time
	ClearValidTo    bool
	ExpectedVersion int64
}

// ValidateCodeSystemPatch checks the fields a merge-patch may carry; the period is
// re-checked against the stored validFrom by the caller.
func ValidateCodeSystemPatch(p CodeSystemPatch) error {
	ve := &ValidationError{}
	if p.Name != nil {
		validateName(ve, *p.Name)
	}
	if p.Authority != nil && !Contains(CodeSystemAuthorities, *p.Authority) {
		ve.Add("authority", "ENUM", "geçersiz otorite")
	}
	if p.Status != nil && !Contains(CodeSystemStatuses, *p.Status) {
		ve.Add("status", "ENUM", "geçersiz durum")
	}
	return ve.OrNil()
}

// CodeValueInput is one row of an import batch.
type CodeValueInput struct {
	Code       string
	Display    string
	ParentCode string
	ValidFrom  time.Time
	ValidTo    *time.Time
	Active     bool
	Attributes []byte // canonical JSON object, "{}" when the caller sent none
}

// ValidateImportBatch checks the size of the batch and every row in it. Field paths name
// the array index so the caller can point at the offending row, and the whole batch is
// rejected together: an import is all-or-nothing.
func ValidateImportBatch(items []CodeValueInput) error {
	ve := &ValidationError{}
	switch {
	case len(items) == 0:
		ve.Add("items", "REQUIRED", "en az bir satır gerekli")
		return ve
	case len(items) > MaxImportRows:
		ve.Add("items", "MAX_ITEMS", fmt.Sprintf("bir çağrıda en fazla %d satır aktarılabilir", MaxImportRows))
		return ve
	}
	seen := make(map[string]int, len(items))
	for i, row := range items {
		path := fmt.Sprintf("items[%d]", i)
		if n := utf8.RuneCountInString(row.Code); n < 1 || n > 64 {
			ve.Add(path+".code", "LENGTH", "1-64 karakter olmalı")
		}
		if n := utf8.RuneCountInString(strings.TrimSpace(row.Display)); n < 1 || n > 500 {
			ve.Add(path+".display", "LENGTH", "1-500 karakter olmalı")
		}
		if n := utf8.RuneCountInString(row.ParentCode); n > 64 {
			ve.Add(path+".parentCode", "LENGTH", "en fazla 64 karakter olmalı")
		}
		if row.ValidFrom.IsZero() {
			ve.Add(path+".validFrom", "REQUIRED", "zorunlu alan")
		}
		validatePeriod(ve, path+".validTo", row.ValidFrom, row.ValidTo)
		key := row.Code + "|" + row.ValidFrom.Format(time.DateOnly)
		if first, dup := seen[key]; dup {
			ve.Add(path+".code", "DUPLICATE",
				fmt.Sprintf("aynı kod ve başlangıç tarihi %d. satırda da var", first))
			continue
		}
		seen[key] = i
	}
	return ve.OrNil()
}

// MappingInput is one row of a mapping replacement.
type MappingInput struct {
	CodeSystemID string
	Code         string
	ValidFrom    time.Time
	ValidTo      *time.Time
	Primary      bool
}

// ValidateMappingSet checks the submitted set against itself: a period must be well
// formed, two rows for the same system and code may not overlap, and two primary rows for
// the same system may not overlap. The identical rules against rows already stored are
// enforced by the exclusion constraints of migration 000019; this check exists so a
// self-contradicting payload is refused with the same problem code instead of a partial
// write being rolled back.
func ValidateMappingSet(items []MappingInput) error {
	ve := &ValidationError{}
	for i, row := range items {
		path := fmt.Sprintf("items[%d]", i)
		if row.CodeSystemID == "" {
			ve.Add(path+".codeSystemId", "REQUIRED", "zorunlu alan")
		}
		if n := utf8.RuneCountInString(row.Code); n < 1 || n > 64 {
			ve.Add(path+".code", "LENGTH", "1-64 karakter olmalı")
		}
		if row.ValidFrom.IsZero() {
			ve.Add(path+".validFrom", "REQUIRED", "zorunlu alan")
		}
		validatePeriod(ve, path+".validTo", row.ValidFrom, row.ValidTo)
	}
	if err := ve.OrNil(); err != nil {
		return err
	}
	for i := range items {
		for j := i + 1; j < len(items); j++ {
			a, b := items[i], items[j]
			if a.CodeSystemID != b.CodeSystemID {
				continue
			}
			if !PeriodsOverlap(a.ValidFrom, a.ValidTo, b.ValidFrom, b.ValidTo) {
				continue
			}
			if a.Code == b.Code || (a.Primary && b.Primary) {
				return ErrMappingOverlap
			}
		}
	}
	return nil
}

// PeriodsOverlap reports whether the half-open day ranges [aFrom, aTo) and [bFrom, bTo)
// share a day; a nil end means the period is still open. It mirrors the
// daterange(valid_from, valid_to, '[)') && daterange(...) test of the exclusion
// constraints, so Go and PostgreSQL answer the same question.
func PeriodsOverlap(aFrom time.Time, aTo *time.Time, bFrom time.Time, bTo *time.Time) bool {
	aStart, bStart := DateOnly(aFrom), DateOnly(bFrom)
	if aTo != nil && !DateOnly(*aTo).After(bStart) {
		return false
	}
	if bTo != nil && !DateOnly(*bTo).After(aStart) {
		return false
	}
	return true
}

// DateOnly strips the clock so period comparisons stay day-based.
func DateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// likeEscaper makes the caller's own wildcards literal inside an ILIKE pattern.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// LikePattern wraps a trimmed search term in the contains-pattern the ILIKE filters
// expect. An empty term means "no filter".
func LikePattern(q string) string {
	trimmed := strings.TrimSpace(q)
	if trimmed == "" {
		return ""
	}
	return "%" + likeEscaper.Replace(trimmed) + "%"
}

// ValidateSearchTerm checks a free-text filter before it reaches the database.
func ValidateSearchTerm(field, q string) error {
	if q == "" {
		return nil
	}
	if n := utf8.RuneCountInString(q); n < 2 || n > 120 {
		ve := &ValidationError{}
		ve.Add(field, "LENGTH", "2-120 karakter olmalı")
		return ve
	}
	return nil
}

func validateCatalogCode(ve *ValidationError, field, code string) {
	if !catalogCodePattern.MatchString(code) {
		ve.Add(field, "FORMAT", "2-64 karakter, A-Z ile başlamalı, büyük harf, rakam ve alt çizgi")
	}
}

// validateName checks the display name every catalog resource carries.
func validateName(ve *ValidationError, name string) {
	if n := utf8.RuneCountInString(strings.TrimSpace(name)); n < 2 || n > 200 {
		ve.Add("name", "LENGTH", "2-200 karakter olmalı")
	}
}

func validateDescription(ve *ValidationError, field, text string) {
	if utf8.RuneCountInString(text) > 2000 {
		ve.Add(field, "LENGTH", "en fazla 2000 karakter olmalı")
	}
}

func validatePeriod(ve *ValidationError, field string, from time.Time, to *time.Time) {
	if to == nil || from.IsZero() {
		return
	}
	if !DateOnly(*to).After(DateOnly(from)) {
		ve.Add(field, "PERIOD", "bitiş tarihi başlangıçtan sonra olmalı")
	}
}
