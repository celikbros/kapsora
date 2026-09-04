// Package domain holds the provider network rules: the status machine of a provider
// profile, the shape of a location, how a capability set is checked against itself, how a
// category capability resolves to the definitions it covers, and the handling of a
// practitioner's registration number. It depends on nothing outside the standard library
// and the identifier primitives of the organization directory, so every rule is
// unit-testable without a database.
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	organization "github.com/celikbros/kapsora/internal/organization/domain"
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
	return fmt.Sprintf("provider: %d validation error(s)", len(e.Fields))
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
var ErrValidation = errors.New("provider: validation failed")

// Is lets errors.Is(err, ErrValidation) match a *ValidationError.
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

// Conflicts the transport maps to named problem codes.
var (
	// ErrTransitionInvalid is raised when a status command is asked for a move the
	// machine does not allow, including any move out of TERMINATED.
	ErrTransitionInvalid = errors.New("provider: status transition not allowed")
	// ErrCapabilityOverlap is raised when the submitted capability set contradicts
	// itself; the same condition against rows already stored is caught by the exclusion
	// constraints of migration 000020 and mapped to the identical problem code.
	ErrCapabilityOverlap = errors.New("provider: capability periods overlap")
	// ErrAssignmentOverlap is the same rule for practitioner-location assignments.
	ErrAssignmentOverlap = errors.New("provider: practitioner assignment periods overlap")
)

// Provider profile statuses (migration 000020).
const (
	StatusPending    = "PENDING"
	StatusActive     = "ACTIVE"
	StatusSuspended  = "SUSPENDED"
	StatusTerminated = "TERMINATED"
)

// Provider location statuses (migration 000020).
const (
	LocationActive    = "ACTIVE"
	LocationSuspended = "SUSPENDED"
	LocationClosed    = "CLOSED"
)

// Practitioner statuses (migration 000020).
const (
	PractitionerActive    = "ACTIVE"
	PractitionerSuspended = "SUSPENDED"
	PractitionerEnded     = "ENDED"
)

// Closed lists the database repeats as CHECK constraints.
var (
	ProviderTypes = []string{
		"HOSPITAL", "CLINIC", "PHARMACY", "LABORATORY", "IMAGING",
		"HOTEL", "AGENCY", "TRANSPORT", "EDUCATION", "SPORT", "OTHER",
	}
	ProviderStatuses     = []string{StatusPending, StatusActive, StatusSuspended, StatusTerminated}
	LocationStatuses     = []string{LocationActive, LocationSuspended, LocationClosed}
	PractitionerStatuses = []string{PractitionerActive, PractitionerSuspended, PractitionerEnded}
	PractitionerRoles    = []string{"ATTENDING", "CONSULTANT", "TECHNICIAN", "ADMINISTRATIVE"}
	// RegistrationAuthorities are the bodies that issue a professional registration
	// number; OTHER covers a foreign or private register.
	RegistrationAuthorities = []string{"TTB", "SB", "TDB", "TEB", "OTHER"}
)

// providerTransitions is the whole status machine. A provider is activated when its
// contract starts, suspended and reactivated while it runs, and terminated once, at which
// point nothing moves any more: the contract history has to stay readable exactly as it
// ended.
var providerTransitions = map[string][]string{
	StatusPending:   {StatusActive, StatusTerminated},
	StatusActive:    {StatusSuspended, StatusTerminated},
	StatusSuspended: {StatusActive, StatusTerminated},
}

// TransitionAllowed reports whether a provider may move from one status to another.
// Unlike a merge-patch machine, repeating the current status is not a no-op here: the
// caller asked for a command, and a command that changes nothing is a mistake worth
// refusing.
func TransitionAllowed(from, to string) bool {
	for _, allowed := range providerTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// ValidateTransition turns a refused move into the conflict the transport reports as
// PROVIDER_TRANSITION_INVALID.
func ValidateTransition(from, to string) error {
	if !TransitionAllowed(from, to) {
		return fmt.Errorf("%w: %s -> %s", ErrTransitionInvalid, from, to)
	}
	return nil
}

// Code and text shapes; the database repeats every one of them as a CHECK constraint.
var (
	locationCodePattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,39}$`)
	countryPattern      = regexp.MustCompile(`^[A-Z]{2}$`)
	branchCodePattern   = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_.-]{0,63}$`)
	timezonePattern     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+_-]*(/[A-Za-z0-9+_-]+){0,2}$`)
)

// DefaultCountry and DefaultTimezone mirror the column defaults of migration 000020.
const (
	DefaultCountry  = "TR"
	DefaultTimezone = "Europe/Istanbul"
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

// NewProvider is the validated create command for a provider profile.
type NewProvider struct {
	TenantOrganizationID string
	ProviderType         string
	NetworkTier          string
	ContractedFrom       *time.Time
	ContractedTo         *time.Time
	Notes                string
}

// ValidateNewProvider checks the fields the database would otherwise reject with a raw
// constraint error.
func ValidateNewProvider(in NewProvider) error {
	ve := &ValidationError{}
	if in.TenantOrganizationID == "" {
		ve.Add("tenantOrganizationId", "REQUIRED", "zorunlu alan")
	}
	if !Contains(ProviderTypes, in.ProviderType) {
		ve.Add("providerType", "ENUM", "geçersiz sağlayıcı türü")
	}
	validateNetworkTier(ve, "networkTier", in.NetworkTier)
	validateText(ve, "notes", in.Notes, 2000)
	validateOptionalPeriod(ve, "contractedTo", in.ContractedFrom, in.ContractedTo)
	return ve.OrNil()
}

// ProviderPatch is a merge-patch of a provider profile; nil leaves a field unchanged and
// the Clear flags carry an explicit null. Status is absent: it moves through the commands.
type ProviderPatch struct {
	ProviderType        *string
	NetworkTier         *string
	ClearNetworkTier    bool
	ContractedFrom      *time.Time
	ClearContractedFrom bool
	ContractedTo        *time.Time
	ClearContractedTo   bool
	Notes               *string
	ClearNotes          bool
	ExpectedVersion     int64
}

// ValidateProviderPatch checks the fields a merge-patch may carry; the resulting contract
// period is re-checked against the stored row by the caller.
func ValidateProviderPatch(p ProviderPatch) error {
	ve := &ValidationError{}
	if p.ProviderType != nil && !Contains(ProviderTypes, *p.ProviderType) {
		ve.Add("providerType", "ENUM", "geçersiz sağlayıcı türü")
	}
	if p.NetworkTier != nil {
		validateNetworkTier(ve, "networkTier", *p.NetworkTier)
	}
	if p.Notes != nil {
		validateText(ve, "notes", *p.Notes, 2000)
	}
	return ve.OrNil()
}

// ValidateContractPeriod checks a resolved contract period, so a patch that only moves one
// end is still refused when the result would be inverted.
func ValidateContractPeriod(from, to *time.Time) error {
	ve := &ValidationError{}
	validateOptionalPeriod(ve, "contractedTo", from, to)
	return ve.OrNil()
}

// NewLocation is the validated create command for a provider location.
type NewLocation struct {
	Code        string
	Name        string
	AddressLine string
	District    string
	City        string
	CountryCode string
	PostalCode  string
	Latitude    *float64
	Longitude   *float64
	Timezone    string
	Phone       string
}

// ValidateNewLocation checks a create command and fills in the column defaults.
func ValidateNewLocation(in *NewLocation) error {
	ve := &ValidationError{}
	if !locationCodePattern.MatchString(in.Code) {
		ve.Add("code", "FORMAT", "1-40 karakter, büyük harf, rakam, _ ve - kullanılabilir")
	}
	validateName(ve, "name", in.Name)
	if in.CountryCode == "" {
		in.CountryCode = DefaultCountry
	}
	if in.Timezone == "" {
		in.Timezone = DefaultTimezone
	}
	validateLocationCommon(ve, in.AddressLine, in.District, in.City, in.CountryCode,
		in.PostalCode, in.Timezone, in.Phone)
	validateCoordinates(ve, in.Latitude, in.Longitude)
	return ve.OrNil()
}

// LocationPatch is a merge-patch of a location; the code is immutable.
type LocationPatch struct {
	Name             *string
	AddressLine      *string
	ClearAddressLine bool
	District         *string
	ClearDistrict    bool
	City             *string
	ClearCity        bool
	CountryCode      *string
	PostalCode       *string
	ClearPostalCode  bool
	Latitude         *float64
	ClearLatitude    bool
	Longitude        *float64
	ClearLongitude   bool
	Timezone         *string
	Phone            *string
	ClearPhone       bool
	Status           *string
	ExpectedVersion  int64
}

// ValidateLocationPatch checks the fields a merge-patch may carry.
func ValidateLocationPatch(p LocationPatch) error {
	ve := &ValidationError{}
	if p.Name != nil {
		validateName(ve, "name", *p.Name)
	}
	if p.AddressLine != nil {
		validateText(ve, "addressLine", *p.AddressLine, 500)
	}
	if p.District != nil {
		validateText(ve, "district", *p.District, 120)
	}
	if p.City != nil {
		validateText(ve, "city", *p.City, 120)
	}
	if p.CountryCode != nil && !countryPattern.MatchString(*p.CountryCode) {
		ve.Add("countryCode", "FORMAT", "ISO-3166-1 iki harfli kod olmalı")
	}
	if p.PostalCode != nil {
		validateText(ve, "postalCode", *p.PostalCode, 20)
	}
	if p.Timezone != nil && !timezonePattern.MatchString(*p.Timezone) {
		ve.Add("timezone", "FORMAT", "IANA saat dilimi olmalı")
	}
	if p.Phone != nil {
		validateText(ve, "phone", *p.Phone, 40)
	}
	if p.Status != nil && !Contains(LocationStatuses, *p.Status) {
		ve.Add("status", "ENUM", "geçersiz lokasyon durumu")
	}
	validateCoordinates(ve, p.Latitude, p.Longitude)
	return ve.OrNil()
}

// ValidateCoordinatePair refuses a half filled pair once a patch has been merged into the
// stored row; the database repeats the rule as ck_location_geo_pair.
func ValidateCoordinatePair(lat, lng *float64) error {
	ve := &ValidationError{}
	if (lat == nil) != (lng == nil) {
		ve.Add("latitude", "PAIR", "enlem ve boylam birlikte verilmeli")
	}
	return ve.OrNil()
}

// CapabilityInput is one row of a capability set replacement. Exactly one of the two
// target ids is set.
type CapabilityInput struct {
	ServiceDefinitionID string
	ServiceCategoryID   string
	ValidFrom           time.Time
	ValidTo             *time.Time
	Notes               string
}

// ValidateCapabilitySet checks the submitted set against itself: each row names exactly
// one target over a well formed period, and two rows for the same target may not overlap.
// The identical rules against rows already stored are enforced by the exclusion
// constraints of migration 000020; this check exists because a PUT replaces the whole set,
// so a self-contradicting payload would never reach the constraint.
func ValidateCapabilitySet(items []CapabilityInput) error {
	ve := &ValidationError{}
	for i, row := range items {
		path := fmt.Sprintf("items[%d]", i)
		switch {
		case row.ServiceDefinitionID == "" && row.ServiceCategoryID == "":
			ve.Add(path+".serviceDefinitionId", "REQUIRED", "hizmet tanımı veya kategori zorunlu")
		case row.ServiceDefinitionID != "" && row.ServiceCategoryID != "":
			ve.Add(path+".serviceCategoryId", "EXCLUSIVE", "yalnızca biri verilebilir")
		}
		if row.ValidFrom.IsZero() {
			ve.Add(path+".validFrom", "REQUIRED", "zorunlu alan")
		}
		validatePeriod(ve, path+".validTo", row.ValidFrom, row.ValidTo)
		validateText(ve, path+".notes", row.Notes, 1000)
	}
	if err := ve.OrNil(); err != nil {
		return err
	}
	for i := range items {
		for j := i + 1; j < len(items); j++ {
			a, b := items[i], items[j]
			same := (a.ServiceDefinitionID != "" && a.ServiceDefinitionID == b.ServiceDefinitionID) ||
				(a.ServiceCategoryID != "" && a.ServiceCategoryID == b.ServiceCategoryID)
			if same && PeriodsOverlap(a.ValidFrom, a.ValidTo, b.ValidFrom, b.ValidTo) {
				return ErrCapabilityOverlap
			}
		}
	}
	return nil
}

// AssignmentInput is one row of a practitioner-location replacement.
type AssignmentInput struct {
	LocationID string
	Role       string
	ValidFrom  time.Time
	ValidTo    *time.Time
}

// ValidateAssignmentSet is ValidateCapabilitySet for practitioner-location assignments:
// two spells at the same location in the same role may not overlap.
func ValidateAssignmentSet(items []AssignmentInput) error {
	ve := &ValidationError{}
	for i, row := range items {
		path := fmt.Sprintf("items[%d]", i)
		if row.LocationID == "" {
			ve.Add(path+".locationId", "REQUIRED", "zorunlu alan")
		}
		if !Contains(PractitionerRoles, row.Role) {
			ve.Add(path+".role", "ENUM", "geçersiz görev")
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
			if a.LocationID != b.LocationID || a.Role != b.Role {
				continue
			}
			if PeriodsOverlap(a.ValidFrom, a.ValidTo, b.ValidFrom, b.ValidTo) {
				return ErrAssignmentOverlap
			}
		}
	}
	return nil
}

// CapabilityScope names every catalog row a capability may point at to cover one service
// definition on a given day: the definition itself, or any category on the chain from the
// definition's own category up to the root.
type CapabilityScope struct {
	DefinitionID string
	CategoryIDs  []string
}

// ResolveCapabilityTargets builds that scope. This is what "a category capability covers
// definitions added under it later" means in practice: nothing is expanded when a
// capability is written, the chain is walked when the search runs, so a definition created
// today is covered by a category capability written last year. The chain arrives ordered
// from the definition's own category upwards; duplicates and empty ids are dropped so the
// result can be handed straight to an `= ANY(...)` filter.
func ResolveCapabilityTargets(definitionID string, categoryChain []string) CapabilityScope {
	scope := CapabilityScope{DefinitionID: definitionID, CategoryIDs: make([]string, 0, len(categoryChain))}
	seen := make(map[string]struct{}, len(categoryChain))
	for _, id := range categoryChain {
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		scope.CategoryIDs = append(scope.CategoryIDs, id)
	}
	return scope
}

// Empty reports whether the scope can match nothing, which happens when the service
// definition the caller named does not exist in this tenant.
func (s CapabilityScope) Empty() bool { return s.DefinitionID == "" || len(s.CategoryIDs) == 0 }

// NewPractitioner is the validated create command. RegistrationNumber is the plaintext the
// caller submitted; it is normalized here and never leaves the process again.
type NewPractitioner struct {
	PersonID              string
	FullName              string
	Title                 string
	BranchCode            string
	RegistrationAuthority string
	RegistrationNumber    string
	ValidFrom             *time.Time
	ValidTo               *time.Time
}

// ValidateNewPractitioner checks a create command. The registration number is validated on
// its normalized form and never appears in a message.
func ValidateNewPractitioner(in NewPractitioner) error {
	ve := &ValidationError{}
	validateName(ve, "fullName", in.FullName)
	validateText(ve, "title", in.Title, 120)
	validateBranchCode(ve, in.BranchCode)
	if !Contains(RegistrationAuthorities, in.RegistrationAuthority) {
		ve.Add("registrationAuthority", "ENUM", "geçersiz kayıt otoritesi")
	}
	if err := ValidateRegistrationNumber(NormalizeRegistrationNumber(in.RegistrationNumber)); err != nil {
		ve.Add("registrationNumber", "FORMAT", "sicil numarası doğrulanamadı")
	}
	validateOptionalPeriod(ve, "validTo", in.ValidFrom, in.ValidTo)
	return ve.OrNil()
}

// PractitionerPatch is a merge-patch of a practitioner; the registration authority and the
// registration number are absent because a registration is ended and re-registered rather
// than rewritten.
type PractitionerPatch struct {
	FullName        *string
	Title           *string
	ClearTitle      bool
	BranchCode      *string
	ClearBranchCode bool
	PersonID        *string
	ClearPersonID   bool
	ValidFrom       *time.Time
	ClearValidFrom  bool
	ValidTo         *time.Time
	ClearValidTo    bool
	Status          *string
	ExpectedVersion int64
}

// ValidatePractitionerPatch checks the fields a merge-patch may carry.
func ValidatePractitionerPatch(p PractitionerPatch) error {
	ve := &ValidationError{}
	if p.FullName != nil {
		validateName(ve, "fullName", *p.FullName)
	}
	if p.Title != nil {
		validateText(ve, "title", *p.Title, 120)
	}
	if p.BranchCode != nil {
		validateBranchCode(ve, *p.BranchCode)
	}
	if p.Status != nil && !Contains(PractitionerStatuses, *p.Status) {
		ve.Add("status", "ENUM", "geçersiz uygulayıcı durumu")
	}
	return ve.OrNil()
}

// ValidatePractitionerPeriod checks a resolved validity period after a patch is merged.
func ValidatePractitionerPeriod(from, to *time.Time) error {
	ve := &ValidationError{}
	validateOptionalPeriod(ve, "validTo", from, to)
	return ve.OrNil()
}

// registrationType selects the identifier primitives of the organization directory that
// fit a professional register number: upper-cased, separators stripped, letters and digits
// with a few punctuation marks, masked to its first two characters.
const registrationType = organization.IdentifierProviderRegistry

// NormalizeRegistrationNumber trims, strips spaces and dashes and upper-cases. The blind
// index, the validation and the mask all run on this form, so "12-345" and "12345" are the
// same registration everywhere.
func NormalizeRegistrationNumber(raw string) string {
	return organization.Normalize(registrationType, raw)
}

// ValidateRegistrationNumber checks a normalized number. It never carries the value.
func ValidateRegistrationNumber(normalized string) error {
	return organization.Validate(registrationType, normalized)
}

// MaskRegistrationNumber is the only representation of a registration number that may
// leave the process.
func MaskRegistrationNumber(normalized string) string {
	return organization.Mask(registrationType, normalized)
}

// RegistrationIndexInput is the string handed to crypto.BlindIndexer. The issuing
// authority qualifies the number, so the same digits registered by two different bodies
// can never collide in the index.
func RegistrationIndexInput(authority, normalized string) string {
	return authority + ":" + normalized
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

func validateName(ve *ValidationError, field, name string) {
	if n := utf8.RuneCountInString(strings.TrimSpace(name)); n < 2 || n > 200 {
		ve.Add(field, "LENGTH", "2-200 karakter olmalı")
	}
}

func validateText(ve *ValidationError, field, value string, maxLen int) {
	if utf8.RuneCountInString(value) > maxLen {
		ve.Add(field, "LENGTH", fmt.Sprintf("en fazla %d karakter olmalı", maxLen))
	}
}

func validateNetworkTier(ve *ValidationError, field, tier string) {
	if tier == "" {
		return
	}
	if n := utf8.RuneCountInString(tier); n < 1 || n > 32 {
		ve.Add(field, "LENGTH", "1-32 karakter olmalı")
	}
}

func validateBranchCode(ve *ValidationError, code string) {
	if code == "" {
		return
	}
	if !branchCodePattern.MatchString(code) {
		ve.Add("branchCode", "FORMAT", "1-64 karakter, büyük harf, rakam, _, . ve - kullanılabilir")
	}
}

func validateLocationCommon(ve *ValidationError, addressLine, district, city, country, postal, timezone, phone string) {
	validateText(ve, "addressLine", addressLine, 500)
	validateText(ve, "district", district, 120)
	validateText(ve, "city", city, 120)
	if !countryPattern.MatchString(country) {
		ve.Add("countryCode", "FORMAT", "ISO-3166-1 iki harfli kod olmalı")
	}
	validateText(ve, "postalCode", postal, 20)
	if !timezonePattern.MatchString(timezone) {
		ve.Add("timezone", "FORMAT", "IANA saat dilimi olmalı")
	}
	validateText(ve, "phone", phone, 40)
}

func validateCoordinates(ve *ValidationError, lat, lng *float64) {
	if lat != nil && (*lat < -90 || *lat > 90) {
		ve.Add("latitude", "RANGE", "-90 ile 90 arasında olmalı")
	}
	if lng != nil && (*lng < -180 || *lng > 180) {
		ve.Add("longitude", "RANGE", "-180 ile 180 arasında olmalı")
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

func validateOptionalPeriod(ve *ValidationError, field string, from, to *time.Time) {
	if from == nil || to == nil {
		return
	}
	validatePeriod(ve, field, *from, to)
}
