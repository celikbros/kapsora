// Package domain holds what a health case, an encounter and a diagnosis may look like:
// the closed lists the schema repeats as CHECK constraints, and the validation of
// everything a caller may send. It depends on nothing outside the standard library, so
// every rule here is unit-testable without a database.
//
// Two things this package deliberately does not have.
//
// It has no way to set a case's sensitivity. Sensitivity is derived from the diagnoses the
// case actually carries, and a field a caller could set is a field a caller could clear.
//
// And it has no projection. Which half of a record a caller is shown depends on what that
// caller holds, which is a fact about the request rather than about the record, so the
// decision lives one layer up — in the application service, where the permissions are.
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/celikbros/kapsora/internal/audit"
)

// FieldError names one invalid field; Field uses the JSON path of the contract.
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
	return fmt.Sprintf("health: %d validation error(s)", len(e.Fields))
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
var ErrValidation = errors.New("health: validation failed")

// Is lets errors.Is(err, ErrValidation) match a *ValidationError.
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

// AggregateCase is the resource type the audit and access rows of a case carry.
const AggregateCase = "health_case"

// AggregateEncounter is the resource type of an encounter.
const AggregateEncounter = "health_encounter"

// Case statuses and types (migration 000031).
const (
	StatusOpen   = "OPEN"
	StatusClosed = "CLOSED"

	SensitivityStandard  = "STANDARD"
	SensitivitySensitive = "SENSITIVE"
)

// Diagnosis types (migration 000031).
const (
	DiagnosisPrimary   = "PRIMARY"
	DiagnosisSecondary = "SECONDARY"
	DiagnosisSuspected = "SUSPECTED"
)

// Closed lists the database repeats as CHECK constraints.
var (
	CaseTypes      = []string{"OUTPATIENT", "INPATIENT", "CHRONIC", "MATERNITY", "OTHER"}
	CaseStatuses   = []string{StatusOpen, StatusClosed}
	EncounterTypes = []string{"OUTPATIENT", "INPATIENT", "EMERGENCY", "TELEHEALTH"}
	DiagnosisTypes = []string{DiagnosisPrimary, DiagnosisSecondary, DiagnosisSuspected}
	// CaseRequestTypes are the request types a case may be opened from. A reservation and
	// a reimbursement are neither of them an episode of care.
	CaseRequestTypes = []string{"DIRECT_SERVICE", "PREAUTHORIZATION"}
)

// Limits mirroring the column CHECKs and the OpenAPI schema.
const (
	// MaxDiagnoses bounds one diagnosis set; the OpenAPI schema repeats it.
	MaxDiagnoses = 50
	// MaxNotes bounds the one free-text clinical column of the package.
	MaxNotes = 4000
	// MaxReasonText bounds the reason a close command carries. It is 200 rather than the
	// 1000 other modules allow because this reason is written to an audit detail, and
	// audit.SanitizeDetail drops a string longer than 200 silently — a value the caller
	// was told was accepted and that then vanishes is worse than one refused out loud.
	MaxReasonText = audit.MaxDetailString
	// MaxAccessReason bounds X-Access-Reason. It is short on purpose: a sentence of two
	// hundred characters is a reason, and anything longer is a place to put a case note.
	MaxAccessReason = audit.MaxDetailString
)

// AccessPurposes are the purpose codes health.clinical_access_purpose is seeded with. The
// list is repeated here so a malformed header is a field error rather than a database
// round trip, and the database is still the authority: the service checks it there too.
var AccessPurposes = []string{
	"TREATMENT", "PRE_AUTHORIZATION", "CLAIM_REVIEW", "MEDICAL_REVIEW", "AUDIT", "MEMBER_REQUEST",
}

// branchCodePattern mirrors ck_health_encounter_branch.
var branchCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_.-]{0,63}$`)

// NewCase is the create command.
type NewCase struct {
	CaseType string
	OpenedAt *time.Time
	// Now is the clock the "not in the future" check is made against.
	Now time.Time
}

// ValidateNewCase checks a create command. Whether the ids it names exist, and whether they
// belong together, is the application layer's business: only it can read them.
func ValidateNewCase(in NewCase) error {
	ve := &ValidationError{}
	requireOneOf(ve, "caseType", in.CaseType, CaseTypes)
	if in.OpenedAt != nil && in.OpenedAt.After(in.Now) {
		ve.Add("openedAt", "RANGE", "vaka açılış zamanı gelecekte olamaz")
	}
	return ve.OrNil()
}

// NewEncounter is the create command of an encounter.
type NewEncounter struct {
	EncounterType string
	StartedAt     time.Time
	EndedAt       *time.Time
	BranchCode    *string
	NotesClinical *string
}

// ValidateNewEncounter checks a create command.
func ValidateNewEncounter(in NewEncounter) error {
	ve := &ValidationError{}
	requireOneOf(ve, "encounterType", in.EncounterType, EncounterTypes)
	if in.StartedAt.IsZero() {
		ve.Add("startedAt", "REQUIRED", "başlangıç zamanı zorunlu")
	}
	if in.EndedAt != nil && in.EndedAt.Before(in.StartedAt) {
		ve.Add("endedAt", "RANGE", "bitiş zamanı başlangıçtan önce olamaz")
	}
	if in.BranchCode != nil && !branchCodePattern.MatchString(strings.TrimSpace(*in.BranchCode)) {
		ve.Add("branchCode", "FORMAT", "büyük harf, rakam, nokta ve tire; 1-64 karakter")
	}
	if in.NotesClinical != nil && utf8.RuneCountInString(*in.NotesClinical) > MaxNotes {
		ve.Add("notesClinical", "LENGTH", "en fazla 4000 karakter")
	}
	return ve.OrNil()
}

// DiagnosisInput is one line of a diagnosis set replacement. There is no `sensitive` field:
// the category the code belongs to decides that, and a caller that could assert it could
// assert a psychiatric diagnosis is an ordinary one.
type DiagnosisInput struct {
	CodeValueID   string
	DiagnosisType string
}

// ValidateDiagnoses checks a whole diagnosis set, including the one rule the partial unique
// index also enforces: at most one PRIMARY. Both halves exist on purpose — the caller is
// told which line is the second primary, and the database refuses it whatever writes it.
func ValidateDiagnoses(items []DiagnosisInput) error {
	ve := &ValidationError{}
	if len(items) > MaxDiagnoses {
		ve.Add("items", "RANGE", "en fazla 50 tanı gönderilebilir")
		return ve.OrNil()
	}
	primary := -1
	seen := make(map[string]int, len(items))
	for i, item := range items {
		path := fmt.Sprintf("items[%d]", i)
		if strings.TrimSpace(item.CodeValueID) == "" {
			ve.Add(path+".codeValueId", "REQUIRED", "tanı kodu zorunlu")
		} else if first, dup := seen[item.CodeValueID]; dup {
			ve.Add(path+".codeValueId", "DUPLICATE",
				fmt.Sprintf("bu tanı items[%d] içinde de var", first))
		} else {
			seen[item.CodeValueID] = i
		}
		requireOneOf(ve, path+".diagnosisType", item.DiagnosisType, DiagnosisTypes)
		if item.DiagnosisType != DiagnosisPrimary {
			continue
		}
		if primary >= 0 {
			ve.Add(path+".diagnosisType", "DUPLICATE_PRIMARY",
				fmt.Sprintf("bir encounter'da tek ana tanı olur; items[%d] zaten ana tanı", primary))
			continue
		}
		primary = i
	}
	return ve.OrNil()
}

// ValidateCloseReason checks the optional free text a close command carries.
func ValidateCloseReason(reason *string) error {
	if reason == nil || utf8.RuneCountInString(*reason) <= MaxReasonText {
		return nil
	}
	ve := &ValidationError{}
	ve.Add("reasonText", "LENGTH", "en fazla 1000 karakter")
	return ve
}

// ValidateAccessHeaders checks the purpose and reason a clinical read states. The field
// names are the header names, because that is where a caller would have to fix them.
func ValidateAccessHeaders(purpose, reason string) error {
	ve := &ValidationError{}
	if purpose != "" && !Contains(AccessPurposes, purpose) {
		ve.Add("X-Access-Purpose", "ENUM", "geçerli değerler: "+strings.Join(AccessPurposes, ", "))
	}
	if utf8.RuneCountInString(reason) > MaxAccessReason {
		ve.Add("X-Access-Reason", "LENGTH", "en fazla 200 karakter")
	}
	return ve.OrNil()
}

// ValidateCaseStatusFilter checks the list filter's status against the closed list, so an
// unknown value is a field error rather than a silently empty page.
func ValidateCaseStatusFilter(status string) error {
	if status == "" {
		return nil
	}
	ve := &ValidationError{}
	requireOneOf(ve, "status", status, CaseStatuses)
	return ve.OrNil()
}

// ValidateCaseTypeFilter is ValidateCaseStatusFilter for the case type.
func ValidateCaseTypeFilter(caseType string) error {
	if caseType == "" {
		return nil
	}
	ve := &ValidationError{}
	requireOneOf(ve, "caseType", caseType, CaseTypes)
	return ve.OrNil()
}

// Contains reports whether a closed list holds a value.
func Contains(set []string, value string) bool {
	for _, s := range set {
		if s == value {
			return true
		}
	}
	return false
}

func requireOneOf(ve *ValidationError, field, value string, allowed []string) {
	if Contains(allowed, value) {
		return
	}
	ve.Add(field, "ENUM", "geçerli değerler: "+strings.Join(allowed, ", "))
}
