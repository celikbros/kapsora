// Package domain holds the claim's own rules: the lifecycle, what a line has to look like,
// what a decision has to add up to, and the two review stages.
//
// Nothing here reads a database or a clock it was not given. Two things in particular are
// decided here rather than in the service, because they are the two the whole package rests
// on and a rule kept in a service is a rule the next command can forget:
//
//   - **the freeze** — `Target` refuses every write command on a claim whose current version
//     has been submitted, so "a submitted version is never edited" is a transition table
//     rather than an `if` somebody has to remember to write;
//   - **the split** — `ValidateDecision` refuses a decision whose payer and member halves do
//     not add up to the approved amount, exactly, in exact decimals. The database CHECKs it
//     too; this is so the caller is told which line and which field rather than being handed
//     a constraint name.
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
)

// FieldError is one rejected field, as the transport renders it.
type FieldError struct {
	Field   string
	Code    string
	Message string
}

// ValidationError collects field errors so a caller is told everything that is wrong at
// once rather than one thing per round trip.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("claim: %d validation error(s)", len(e.Fields))
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
var ErrValidation = errors.New("claim: validation failed")

// Is lets errors.Is(err, ErrValidation) match a *ValidationError.
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

// AggregateClaim is the resource type the audit and access rows of a claim carry.
const AggregateClaim = "CLAIM"

// The claim lifecycle (v1.2 12.5). The last three are declared because the lifecycle is one
// list and a list with a hole in it is a list nobody can read; M7 owns the commands that
// reach them and nothing in this package puts a claim into one.
const (
	StatusDraft             = "DRAFT"
	StatusSubmitted         = "SUBMITTED"
	StatusAutoAdjudicated   = "AUTO_ADJUDICATED"
	StatusPendingMedical    = "PENDING_MEDICAL"
	StatusPendingFinancial  = "PENDING_FINANCIAL"
	StatusReturned          = "RETURNED"
	StatusPartiallyApproved = "PARTIALLY_APPROVED"
	StatusApproved          = "APPROVED"
	StatusRejected          = "REJECTED"
	StatusInvoiced          = "INVOICED"
	StatusBatched           = "BATCHED"
	StatusSettled           = "SETTLED"
	StatusCancelled         = "CANCELLED"
)

// Statuses is the whole list, in lifecycle order, for a filter's validation.
var Statuses = []string{
	StatusDraft, StatusSubmitted, StatusAutoAdjudicated, StatusPendingMedical,
	StatusPendingFinancial, StatusReturned, StatusPartiallyApproved, StatusApproved,
	StatusRejected, StatusInvoiced, StatusBatched, StatusSettled, StatusCancelled,
}

// Version statuses.
const (
	VersionDraft      = "DRAFT"
	VersionSubmitted  = "SUBMITTED"
	VersionSuperseded = "SUPERSEDED"
)

// The four things that may be decided about a line. CUT and PARTIALLY_APPROVED are two words
// rather than one because a provider reads them differently and disputes them differently: a
// cut is money the payer took off a line it accepted, and a partial approval is fewer units
// than were claimed.
const (
	DecisionApproved          = "APPROVED"
	DecisionPartiallyApproved = "PARTIALLY_APPROVED"
	DecisionRejected          = "REJECTED"
	DecisionCut               = "CUT"
)

// Decisions is the closed list.
var Decisions = []string{DecisionApproved, DecisionPartiallyApproved, DecisionRejected, DecisionCut}

// The three stages a decision can be taken at. AUTO means the rules decided and nobody
// looked, which is why an AUTO decision never carries an actor.
const (
	StageAuto      = "AUTO"
	StageMedical   = "MEDICAL"
	StageFinancial = "FINANCIAL"
)

// The channels a claim may arrive on. It is the same word list a service request's channel
// uses: "how did this reach us" is one question, and two vocabularies for it would be two
// answers a report has to reconcile.
var Channels = []string{
	"BACKOFFICE", "PROVIDER_PORTAL", "MEMBER_PORTAL", "API", "BATCH_IMPORT", "CALL_CENTER",
}

// DefaultChannel is what a claim raised through the product's own provider portal carries.
const DefaultChannel = "PROVIDER_PORTAL"

// DomainHealth is the only domain WP-I5-04 writes. The column exists for M6 and M7.
const DomainHealth = "HEALTH"

// The commands that move a claim.
const (
	CommandSubmit  = "SUBMIT"
	CommandDecide  = "DECIDE"
	CommandApprove = "APPROVE"
	CommandReject  = "REJECT"
	CommandReturn  = "RETURN"
	CommandCancel  = "CANCEL"
	// CommandEdit is not a transition — it lands where it started — but it is in the table
	// because the freeze is exactly "which statuses may be edited", and stating it here
	// keeps it beside every other precondition rather than inside a service.
	CommandEdit = "EDIT"
)

// transitions is the whole lifecycle: for each command, the statuses it may run from.
//
// The freeze lives in CommandEdit's row and nowhere else. A claim whose current version has
// been submitted is not DRAFT or RETURNED, so `patchClaimDraft` and `putClaimLines` find no
// target and the caller is answered 409 CLAIM_VERSION_FROZEN.
var transitions = map[string][]string{
	CommandEdit:   {StatusDraft, StatusReturned},
	CommandSubmit: {StatusDraft, StatusReturned},
	CommandDecide: {StatusPendingMedical, StatusPendingFinancial},
	// A claim the pipeline adjudicated on its own is already decided; approve is for the
	// two review statuses. AUTO_ADJUDICATED is here because a deployment whose approval
	// policy sends everything to a person still lands there for a moment.
	CommandApprove: {StatusAutoAdjudicated, StatusPendingMedical, StatusPendingFinancial},
	CommandReject:  {StatusAutoAdjudicated, StatusPendingMedical, StatusPendingFinancial},
	CommandReturn:  {StatusAutoAdjudicated, StatusPendingMedical, StatusPendingFinancial},
	// Anything the payer has not yet answered may be withdrawn. A decided claim may not:
	// cancelling one would be an accounting entry rather than a withdrawal.
	CommandCancel: {
		StatusDraft, StatusReturned, StatusSubmitted, StatusPendingMedical,
		StatusPendingFinancial,
	},
}

// Allowed reports whether a command may run from a status.
func Allowed(command, from string) bool {
	for _, s := range transitions[command] {
		if s == from {
			return true
		}
	}
	return false
}

// From returns the statuses a command may run from, for a repository predicate.
func From(command string) []string {
	out := make([]string, len(transitions[command]))
	copy(out, transitions[command])
	return out
}

// StageFor is the stage a claim waiting in this status is decided at. It is derived from the
// status rather than chosen by the caller, which is what makes medical review precede
// financial: a financial reviewer reaching a claim in PENDING_MEDICAL is told the stage does
// not match, rather than quietly writing a FINANCIAL decision on a clinical question.
func StageFor(status string) (string, bool) {
	switch status {
	case StatusPendingMedical:
		return StageMedical, true
	case StatusPendingFinancial:
		return StageFinancial, true
	default:
		return "", false
	}
}

// Closed reports whether a status is one the claim stops moving in. It is the same list the
// `ck_claim_closed` CHECK asserts `closed_at` against.
func Closed(status string) bool {
	switch status {
	case StatusRejected, StatusCancelled, StatusSettled:
		return true
	default:
		return false
	}
}

// Decided reports whether the claim has an answer an invoice could be raised from.
func Decided(status string) bool {
	return status == StatusApproved || status == StatusPartiallyApproved
}

// Patterns mirroring the column CHECKs, so a malformed value is a field error rather than a
// constraint name.
var (
	reasonCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_.:-]{1,79}$`)
	unitTypePattern   = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,31}$`)
	currencyPattern   = regexp.MustCompile(`^[A-Z]{3}$`)
	referencePattern  = regexp.MustCompile(`^[A-Z][A-Z0-9-]{3,63}$`)
)

// Limits mirroring the column CHECKs and the OpenAPI schema.
const (
	MaxReasonText    = 1000
	MaxReviewComment = 2000
	MaxDescription   = 1000
	MaxLines         = 500
)

// ValidReference reports whether a generated reference matches the column CHECK. It exists
// so the generator is tested rather than trusted.
func ValidReference(reference string) bool { return referencePattern.MatchString(reference) }

// NewLine is one line as it arrives, before anything has been looked up.
type NewLine struct {
	LineNo              int
	ServiceDefinitionID string
	UnitType            string
	Quantity            string
	UnitAmount          *string
	LineAmount          string
	CurrencyCode        *string
	Description         *string
}

// ValidateLines checks a whole line set: the numbering, the decimals, the currency and the
// free text. The set is validated together rather than line by line because the numbering is
// a property of the set — two lines numbered 2 is not two problems, it is one.
func ValidateLines(lines []NewLine) error {
	ve := &ValidationError{}
	switch {
	case len(lines) == 0:
		ve.Add("lines", "REQUIRED", "en az bir kalem gerekli")
		return ve.OrNil()
	case len(lines) > MaxLines:
		ve.Add("lines", "LENGTH", fmt.Sprintf("en fazla %d kalem", MaxLines))
		return ve.OrNil()
	}
	seen := make(map[int]bool, len(lines))
	currency := ""
	for i, line := range lines {
		path := fmt.Sprintf("lines[%d]", i)
		switch {
		case line.LineNo < 1:
			ve.Add(path+".lineNo", "RANGE", "satır numarası birden başlar")
		case seen[line.LineNo]:
			ve.Add(path+".lineNo", "DUPLICATE", "aynı satır numarası iki kez verilemez")
		default:
			seen[line.LineNo] = true
		}
		if !unitTypePattern.MatchString(line.UnitType) {
			ve.Add(path+".unitType", "FORMAT", "büyük harfle başlayan bir birim kodu olmalı")
		}
		quantity, err := benefitdomain.ParseQuantity(line.Quantity)
		switch {
		case err != nil:
			ve.Add(path+".quantity", "FORMAT", "kesin ondalık bir sayı olmalı")
		case !quantity.IsPositive():
			ve.Add(path+".quantity", "RANGE", "miktar sıfırdan büyük olmalı")
		}
		amount, err := benefitdomain.ParseQuantity(line.LineAmount)
		switch {
		case err != nil:
			ve.Add(path+".lineAmount", "FORMAT", "kesin ondalık bir sayı olmalı")
		case amount.IsNegative():
			ve.Add(path+".lineAmount", "RANGE", "tutar negatif olamaz")
		}
		if line.UnitAmount != nil {
			unit, err := benefitdomain.ParseQuantity(*line.UnitAmount)
			switch {
			case err != nil:
				ve.Add(path+".unitAmount", "FORMAT", "kesin ondalık bir sayı olmalı")
			case unit.IsNegative():
				ve.Add(path+".unitAmount", "RANGE", "birim tutarı negatif olamaz")
			}
		}
		if line.CurrencyCode != nil && *line.CurrencyCode != "" {
			if !currencyPattern.MatchString(*line.CurrencyCode) {
				ve.Add(path+".currencyCode", "FORMAT", "üç harfli para birimi kodu olmalı")
			} else if currency == "" {
				currency = *line.CurrencyCode
			} else if currency != *line.CurrencyCode {
				// One claim, one currency. The mixed case is refused at the door rather
				// than left to become an invoice readiness blocker nobody can fix without
				// a new version.
				ve.Add(path+".currencyCode", "CONFLICT", "bir hasar dosyasındaki tüm kalemler aynı para biriminde olmalı")
			}
		}
		if line.Description != nil && len([]rune(*line.Description)) > MaxDescription {
			ve.Add(path+".description", "LENGTH", fmt.Sprintf("en fazla %d karakter", MaxDescription))
		}
	}
	return ve.OrNil()
}

// Period is the claim's service window.
type Period struct {
	From time.Time
	To   time.Time
}

// ValidatePeriod refuses a window that runs backwards.
func ValidatePeriod(p Period) error {
	ve := &ValidationError{}
	switch {
	case p.From.IsZero():
		ve.Add("serviceDateFrom", "REQUIRED", "hizmet başlangıç tarihi zorunlu")
	case p.To.IsZero():
		ve.Add("serviceDateTo", "REQUIRED", "hizmet bitiş tarihi zorunlu")
	case p.To.Before(p.From):
		ve.Add("serviceDateTo", "RANGE", "bitiş tarihi başlangıçtan önce olamaz")
	}
	return ve.OrNil()
}

// ValidateChannel refuses a channel outside the word list.
func ValidateChannel(channel string) error {
	if channel == "" {
		return nil
	}
	for _, c := range Channels {
		if c == channel {
			return nil
		}
	}
	ve := &ValidationError{}
	ve.Add("channel", "ENUM", "geçerli değerler: "+strings.Join(Channels, ", "))
	return ve.OrNil()
}

// ValidateStatusFilter refuses a status filter outside the lifecycle.
func ValidateStatusFilter(status string) error {
	if status == "" {
		return nil
	}
	for _, s := range Statuses {
		if s == status {
			return nil
		}
	}
	ve := &ValidationError{}
	ve.Add("status", "ENUM", "geçerli değerler: "+strings.Join(Statuses, ", "))
	return ve.OrNil()
}

// ValidateReason checks a reason code and its optional text against the column CHECKs.
func ValidateReason(field, code string, text *string) error {
	ve := &ValidationError{}
	if !reasonCodePattern.MatchString(code) {
		ve.Add(field, "FORMAT", "büyük harfle başlayan bir gerekçe kodu olmalı")
	}
	if text != nil && len([]rune(*text)) > MaxReasonText {
		ve.Add(field+"Text", "LENGTH", fmt.Sprintf("en fazla %d karakter", MaxReasonText))
	}
	return ve.OrNil()
}

// ValidateReviewComment bounds the reviewer's comment on the claim as a whole.
func ValidateReviewComment(comment *string) error {
	if comment == nil || len([]rune(*comment)) <= MaxReviewComment {
		return nil
	}
	ve := &ValidationError{}
	ve.Add("reviewComment", "LENGTH", fmt.Sprintf("en fazla %d karakter", MaxReviewComment))
	return ve.OrNil()
}

// Decision is one per-line decision as it arrives.
type Decision struct {
	LineNo           int
	Decision         string
	ApprovedQuantity string
	ApprovedAmount   string
	PayerAmount      string
	MemberAmount     string
	ReasonCode       string
	ReasonText       *string
}

// ValidateDecisions checks a whole decision set: the decision words, the exactness of every
// figure, and the split.
//
// **The split is the point.** `payerAmount + memberAmount` has to be `approvedAmount`
// exactly, in exact decimals. It is a database CHECK as well, and this is the copy that tells
// the caller which line and which field: a reviewer who typed 600 and 401 against 1000 should
// read "these do not add up" rather than a constraint name.
func ValidateDecisions(decisions []Decision) error {
	ve := &ValidationError{}
	switch {
	case len(decisions) == 0:
		ve.Add("decisions", "REQUIRED", "en az bir satır kararı gerekli")
		return ve.OrNil()
	case len(decisions) > MaxLines:
		ve.Add("decisions", "LENGTH", fmt.Sprintf("en fazla %d karar", MaxLines))
		return ve.OrNil()
	}
	seen := make(map[int]bool, len(decisions))
	for i, d := range decisions {
		path := fmt.Sprintf("decisions[%d]", i)
		switch {
		case d.LineNo < 1:
			ve.Add(path+".lineNo", "RANGE", "satır numarası birden başlar")
		case seen[d.LineNo]:
			ve.Add(path+".lineNo", "DUPLICATE", "aynı satır iki kez karara bağlanamaz")
		default:
			seen[d.LineNo] = true
		}
		known := false
		for _, k := range Decisions {
			if k == d.Decision {
				known = true
				break
			}
		}
		if !known {
			ve.Add(path+".decision", "ENUM", "geçerli değerler: "+strings.Join(Decisions, ", "))
		}
		if !reasonCodePattern.MatchString(d.ReasonCode) {
			ve.Add(path+".reasonCode", "FORMAT", "büyük harfle başlayan bir gerekçe kodu olmalı")
		}
		if d.ReasonText != nil && len([]rune(*d.ReasonText)) > MaxReasonText {
			ve.Add(path+".reasonText", "LENGTH", fmt.Sprintf("en fazla %d karakter", MaxReasonText))
		}
		quantity, qErr := benefitdomain.ParseQuantity(d.ApprovedQuantity)
		if qErr != nil || quantity.IsNegative() {
			ve.Add(path+".approvedQuantity", "FORMAT", "kesin ondalık ve negatif olmayan bir sayı olmalı")
		}
		approved, aErr := benefitdomain.ParseQuantity(d.ApprovedAmount)
		if aErr != nil || approved.IsNegative() {
			ve.Add(path+".approvedAmount", "FORMAT", "kesin ondalık ve negatif olmayan bir sayı olmalı")
		}
		payer, pErr := benefitdomain.ParseQuantity(d.PayerAmount)
		if pErr != nil || payer.IsNegative() {
			ve.Add(path+".payerAmount", "FORMAT", "kesin ondalık ve negatif olmayan bir sayı olmalı")
		}
		member, mErr := benefitdomain.ParseQuantity(d.MemberAmount)
		if mErr != nil || member.IsNegative() {
			ve.Add(path+".memberAmount", "FORMAT", "kesin ondalık ve negatif olmayan bir sayı olmalı")
		}
		if aErr != nil || pErr != nil || mErr != nil {
			continue
		}
		if payer.Add(member).Cmp(approved) != 0 {
			ve.Add(path+".memberAmount", "SPLIT",
				"ödeyici ve hak sahibi payları onaylanan tutara eşit olmalı")
		}
		if d.Decision == DecisionRejected && (approved.IsPositive() || quantity.IsPositive()) {
			ve.Add(path+".approvedAmount", "CONFLICT", "reddedilen satırda onaylanan tutar sıfır olmalı")
		}
	}
	return ve.OrNil()
}

// DateOnly drops the time of day, which is what a service date is.
func DateOnly(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
