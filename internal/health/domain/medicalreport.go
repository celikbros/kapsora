package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// AggregateMedicalReport is the resource type a report's audit rows, access events, work
// items and document links carry. It is one constant because those four have to agree: a
// document linked under one spelling and looked for under another is a document the submit
// gate cannot see.
const AggregateMedicalReport = "MEDICAL_REPORT"

// DocumentTypeMedicalReport is the generic document type code the submit gate accepts
// besides the report's own type. A provider that tags the file with the report's type has
// said which report it is; one that tags it MEDICAL_REPORT has said it is the report.
const DocumentTypeMedicalReport = "MEDICAL_REPORT"

// Report statuses (migration 000032).
//
// StatusSuperseded is not in the work package's own enumeration and is here deliberately.
// The chain may hold at most one APPROVED version, so approving a correction has to move
// version 1 out of APPROVED; deleting it, rewriting it or refusing the correction each
// destroy what the package exists to protect. SUPERSEDED is the word WP-I4-01 already uses
// for a version a later one replaced, and it moves the status and nothing else.
const (
	ReportStatusDraft       = "DRAFT"
	ReportStatusSubmitted   = "SUBMITTED"
	ReportStatusUnderReview = "UNDER_REVIEW"
	ReportStatusApproved    = "APPROVED"
	ReportStatusRejected    = "REJECTED"
	ReportStatusCancelled   = "CANCELLED"
	ReportStatusExpired     = "EXPIRED"
	ReportStatusSuperseded  = "SUPERSEDED"
)

// ReportStatuses is the closed list the schema repeats as a CHECK constraint.
var ReportStatuses = []string{
	ReportStatusDraft, ReportStatusSubmitted, ReportStatusUnderReview, ReportStatusApproved,
	ReportStatusRejected, ReportStatusCancelled, ReportStatusExpired, ReportStatusSuperseded,
}

// ReportUsedByTypes are the three things that may lean on a report.
var ReportUsedByTypes = []string{"SERVICE_REQUEST", "AUTHORIZATION", "CLAIM"}

// The lifecycle commands. They are named rather than spelled at each call site so the
// transition table below is the only place the lifecycle is written down.
const (
	ReportCommandSubmit      = "SUBMIT"
	ReportCommandStartReview = "START_REVIEW"
	ReportCommandApprove     = "APPROVE"
	ReportCommandReject      = "REJECT"
	ReportCommandCancel      = "CANCEL"
	ReportCommandExpire      = "EXPIRE"
	ReportCommandSupersede   = "SUPERSEDE"
)

// reportTransitions is the whole lifecycle: which statuses each command may act on and
// where it lands. Nothing else in this module decides whether a command is allowed, so a
// state machine that would let a rejected report be approved cannot be written by accident.
//
// EXPIRE and SUPERSEDE are in the table although no caller may name them: the scheduler and
// the approval of a correction are the only two things that raise them, and having them
// here means the whole set of ways a report may move is readable in one place.
var reportTransitions = map[string]struct {
	from   []string
	target string
}{
	ReportCommandSubmit:      {from: []string{ReportStatusDraft}, target: ReportStatusSubmitted},
	ReportCommandStartReview: {from: []string{ReportStatusSubmitted}, target: ReportStatusUnderReview},
	ReportCommandApprove:     {from: []string{ReportStatusUnderReview}, target: ReportStatusApproved},
	ReportCommandReject:      {from: []string{ReportStatusUnderReview}, target: ReportStatusRejected},
	// A provider may take back a report nobody has started reading. Once a reviewer has
	// picked it up it is theirs to decide, and a withdrawal at that point would be the
	// provider deciding instead.
	ReportCommandCancel:    {from: []string{ReportStatusDraft, ReportStatusSubmitted}, target: ReportStatusCancelled},
	ReportCommandExpire:    {from: []string{ReportStatusApproved}, target: ReportStatusExpired},
	ReportCommandSupersede: {from: []string{ReportStatusApproved}, target: ReportStatusSuperseded},
}

// ReportTarget reports where a command takes a report from the status it is in, and whether
// it may be given at all.
func ReportTarget(command, status string) (string, bool) {
	t, ok := reportTransitions[command]
	if !ok || !Contains(t.from, status) {
		return "", false
	}
	return t.target, true
}

// ReportFrozen reports whether a report has been decided and may no longer be edited. It is
// the one predicate `patchMedicalReportDraft` and `putMedicalReportServices` refuse on, so
// "an approved report is never edited" is one sentence of code rather than four.
//
// A cancelled or expired report is frozen too. Neither was decided by a reviewer, but
// editing one back into life would be a second way to produce a report nobody submitted.
func ReportFrozen(status string) bool {
	return status != ReportStatusDraft
}

// ReportDecided reports whether a reviewer has answered. A decided version is what a
// correction supersedes: there is nothing to correct about a draft, and a report still
// waiting for a reviewer is corrected by editing it.
func ReportDecided(status string) bool {
	switch status {
	case ReportStatusApproved, ReportStatusRejected, ReportStatusSuperseded:
		return true
	default:
		return false
	}
}

// Limits mirroring the column CHECKs and the OpenAPI schema.
const (
	// MaxReportServices bounds one service set.
	MaxReportServices = 50
	// MaxReportSummary bounds the two free-text clinical columns of a report.
	MaxReportSummary = 4000
	// MaxReportLineNotes bounds a service line's note.
	MaxReportLineNotes = 1000
)

var (
	reportCodePattern     = regexp.MustCompile(`^[A-Z][A-Z0-9_.-]{0,63}$`)
	reportReasonPattern   = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
	reportCurrencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
	// reportDecimalPattern is numeric(20,6) written out: at most fourteen digits before the
	// point and six after it. It is checked here rather than only in the column so a caller
	// is told which line is wrong rather than handed a constraint violation.
	reportDecimalPattern = regexp.MustCompile(`^[0-9]{1,14}(\.[0-9]{1,6})?$`)
)

// NewReport is the create command's own fields, without the ids the application layer has
// to read the database to check.
type NewReport struct {
	ReportType    string
	ReportSubtype *string
	IssuedAt      *time.Time
	ValidFrom     *time.Time
	ValidTo       *time.Time
	Summary       *string
	// Correction is true when the command names a report to supersede. A correction takes
	// its type and its dates from the caller like any other version, so nothing below
	// changes; the flag exists so the message on a missing field can say which command
	// the caller was actually giving.
	Correction bool
	Now        time.Time
}

// ValidateNewReport checks a create or patch command. Whether the ids it names exist, and
// whether they belong together, is the application layer's business: only it can read them.
func ValidateNewReport(in NewReport) error {
	ve := &ValidationError{}
	validateReportCode(ve, "reportType", in.ReportType, true)
	if in.ReportSubtype != nil {
		validateReportCode(ve, "reportSubtype", strings.TrimSpace(*in.ReportSubtype), false)
	}
	switch {
	case in.IssuedAt == nil:
		ve.Add("issuedAt", "REQUIRED", "rapor tarihi zorunlu")
	case in.IssuedAt.After(in.Now):
		ve.Add("issuedAt", "RANGE", "rapor tarihi gelecekte olamaz")
	}
	if in.ValidFrom == nil {
		ve.Add("validFrom", "REQUIRED", "geçerlilik başlangıcı zorunlu")
	}
	if in.ValidTo == nil {
		ve.Add("validTo", "REQUIRED", "geçerlilik bitişi zorunlu")
	}
	if in.ValidFrom != nil && in.ValidTo != nil && in.ValidTo.Before(*in.ValidFrom) {
		ve.Add("validTo", "RANGE", "geçerlilik bitişi başlangıçtan önce olamaz")
	}
	if in.Summary != nil && utf8.RuneCountInString(*in.Summary) > MaxReportSummary {
		ve.Add("clinicalSummary", "LENGTH", "en fazla 4000 karakter")
	}
	return ve.OrNil()
}

func validateReportCode(ve *ValidationError, field, value string, required bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		if required {
			ve.Add(field, "REQUIRED", "kod zorunlu")
		}
		return
	}
	if !reportCodePattern.MatchString(trimmed) {
		ve.Add(field, "FORMAT", "büyük harf, rakam, nokta, alt çizgi ve tire; 1-64 karakter")
	}
}

// ReportServiceInput is one line of a service set replacement.
type ReportServiceInput struct {
	ServiceDefinitionID string
	CoveredQuantity     *string
	CoveredAmount       *string
	CurrencyCode        *string
	Notes               *string
}

// ValidateReportServices checks a whole service set. The set is the unit — the whole set is
// replaced at once — so a single invalid line refuses the whole replacement rather than
// leaving the report half rewritten.
func ValidateReportServices(items []ReportServiceInput) error {
	ve := &ValidationError{}
	if len(items) > MaxReportServices {
		ve.Add("items", "RANGE", "en fazla 50 hizmet satırı gönderilebilir")
		return ve.OrNil()
	}
	seen := make(map[string]int, len(items))
	for i, item := range items {
		path := fmt.Sprintf("items[%d]", i)
		id := strings.TrimSpace(item.ServiceDefinitionID)
		if id == "" {
			ve.Add(path+".serviceDefinitionId", "REQUIRED", "hizmet tanımı zorunlu")
		} else if first, dup := seen[id]; dup {
			ve.Add(path+".serviceDefinitionId", "DUPLICATE",
				fmt.Sprintf("bu hizmet items[%d] içinde de var", first))
		} else {
			seen[id] = i
		}
		validateReportDecimal(ve, path+".coveredQuantity", item.CoveredQuantity)
		validateReportDecimal(ve, path+".coveredAmount", item.CoveredAmount)
		currency := trimOrEmpty(item.CurrencyCode)
		if currency != "" && !reportCurrencyPattern.MatchString(currency) {
			ve.Add(path+".currencyCode", "FORMAT", "üç büyük harfli para birimi kodu olmalı")
		}
		// An amount without a currency is a number, not money. The column says so too; the
		// caller is told which line rather than handed a constraint violation.
		if trimOrEmpty(item.CoveredAmount) != "" && currency == "" {
			ve.Add(path+".currencyCode", "REQUIRED", "tutar verildiğinde para birimi zorunlu")
		}
		if item.Notes != nil && utf8.RuneCountInString(*item.Notes) > MaxReportLineNotes {
			ve.Add(path+".notes", "LENGTH", "en fazla 1000 karakter")
		}
	}
	return ve.OrNil()
}

func validateReportDecimal(ve *ValidationError, field string, raw *string) {
	value := trimOrEmpty(raw)
	if value == "" {
		return
	}
	if !reportDecimalPattern.MatchString(value) {
		ve.Add(field, "FORMAT", "en fazla 14 tam ve 6 ondalık basamaklı pozitif bir sayı olmalı")
		return
	}
	if strings.Trim(strings.ReplaceAll(value, ".", ""), "0") == "" {
		ve.Add(field, "RANGE", "sıfırdan büyük olmalı")
	}
}

// ValidateReportDecision checks the comment and the reason code a reviewer sends. The
// comment is clinical text and is bounded like the summary; the reason code is a code
// because a rejection nobody can count is a rejection nobody can improve on.
func ValidateReportDecision(comment *string, reasonCode string, reasonRequired bool) error {
	ve := &ValidationError{}
	if comment != nil && utf8.RuneCountInString(*comment) > MaxReportSummary {
		ve.Add("reviewComment", "LENGTH", "en fazla 4000 karakter")
	}
	code := strings.TrimSpace(reasonCode)
	switch {
	case code == "":
		if reasonRequired {
			ve.Add("rejectReasonCode", "REQUIRED", "ret gerekçe kodu zorunlu")
		}
	case !reportReasonPattern.MatchString(code):
		ve.Add("rejectReasonCode", "FORMAT", "büyük harf, rakam ve alt çizgi; 2-64 karakter")
	}
	return ve.OrNil()
}

// ValidateReportStatusFilter checks the list filter's status against the closed list, so an
// unknown value is a field error rather than a silently empty page.
func ValidateReportStatusFilter(status string) error {
	if status == "" {
		return nil
	}
	ve := &ValidationError{}
	requireOneOf(ve, "status", status, ReportStatuses)
	return ve.OrNil()
}

// ValidateReportUsedByType checks the kind of thing a coverage call says is using a report.
func ValidateReportUsedByType(usedByType string) error {
	ve := &ValidationError{}
	requireOneOf(ve, "usedByType", usedByType, ReportUsedByTypes)
	return ve.OrNil()
}

func trimOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}
