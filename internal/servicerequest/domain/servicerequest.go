// Package domain holds what a service request may look like and how it may move: the
// closed lists the schema repeats as CHECK constraints, the transition table that is the
// only way through the lifecycle, and the validation of everything a caller may send.
// It depends on nothing outside the standard library and the exact-decimal type of the
// benefit module, so every rule here is unit-testable without a database.
//
// The one thing this package deliberately does not have is a way to write a status. A
// request moves by command — submit, return, reject, approve, partiallyApprove, cancel —
// and each command has its own precondition, its own permission and its own reason. A
// field a caller could set to "APPROVED" would make all three optional.
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	benefit "github.com/celikbros/kapsora/internal/benefit/domain"
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
	return fmt.Sprintf("servicerequest: %d validation error(s)", len(e.Fields))
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
var ErrValidation = errors.New("servicerequest: validation failed")

// Is lets errors.Is(err, ErrValidation) match a *ValidationError.
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

// Request statuses (migration 000006).
const (
	StatusDraft             = "DRAFT"
	StatusSubmitted         = "SUBMITTED"
	StatusEligibilityFailed = "ELIGIBILITY_FAILED"
	StatusPendingDocument   = "PENDING_DOCUMENT"
	StatusPendingReview     = "PENDING_REVIEW"
	StatusApproved          = "APPROVED"
	StatusPartiallyApproved = "PARTIALLY_APPROVED"
	StatusRejected          = "REJECTED"
	StatusCancelled         = "CANCELLED"
	StatusExpired           = "EXPIRED"
	StatusClosed            = "CLOSED"
)

// Version statuses (migration 000006).
const (
	VersionDraft      = "DRAFT"
	VersionSubmitted  = "SUBMITTED"
	VersionSuperseded = "SUPERSEDED"
)

// Item statuses (migration 000006).
const (
	ItemRequested         = "REQUESTED"
	ItemApproved          = "APPROVED"
	ItemPartiallyApproved = "PARTIALLY_APPROVED"
	ItemRejected          = "REJECTED"
	ItemCancelled         = "CANCELLED"
)

// Commands. Each one is a transition with its own precondition and permission; the
// transition code is what the workflow.status_event row carries.
const (
	CommandSubmit           = "SUBMIT"
	CommandReturn           = "RETURN"
	CommandReject           = "REJECT"
	CommandApprove          = "APPROVE"
	CommandPartiallyApprove = "PARTIALLY_APPROVE"
	CommandCancel           = "CANCEL"
	// CommandGate is the automatic move the submit gate makes on its way out of
	// SUBMITTED. It has no endpoint: nobody decides it, the evaluation does.
	CommandGate = "GATE"
)

// Closed lists the database repeats as CHECK constraints.
var (
	RequestTypes = []string{"DIRECT_SERVICE", "PREAUTHORIZATION", "RESERVATION", "REIMBURSEMENT"}
	Channels     = []string{
		"BACKOFFICE", "PROVIDER_PORTAL", "MEMBER_PORTAL", "API", "BATCH_IMPORT", "CALL_CENTER",
	}
	UnitTypes = []string{"MONEY", "COUNT", "NIGHT", "SESSION", "HOUR", "KILOMETER", "POINT"}
	Statuses  = []string{
		StatusDraft, StatusSubmitted, StatusEligibilityFailed, StatusPendingDocument,
		StatusPendingReview, StatusApproved, StatusPartiallyApproved, StatusRejected,
		StatusCancelled, StatusExpired, StatusClosed,
	}
	// DecisionItemStatuses are the line outcomes a reviewer may record. CANCELLED is not
	// among them: a line is cancelled with the request, never on its own.
	DecisionItemStatuses = []string{ItemApproved, ItemPartiallyApproved, ItemRejected}
	// RequiresProviderTypes are the request types that cannot be raised without naming a
	// provider: a preauthorization and a reservation are both promises made to somebody.
	RequiresProviderTypes = []string{"PREAUTHORIZATION", "RESERVATION"}
)

// Limits mirroring the column CHECKs and the OpenAPI schema.
const (
	// MaxItems bounds one line set; the OpenAPI schema repeats it.
	MaxItems = 100
	// MaxReasonText bounds the free-text half of a reason.
	MaxReasonText = 1000
)

var (
	reasonCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_.:-]{1,79}$`)
	currencyPattern   = regexp.MustCompile(`^[A-Z]{3}$`)
)

// transitions is the whole lifecycle: which command may be given in which state and where
// it lands. Nothing outside this table is a legal move, and there is no entry that leads
// out of REJECTED, CANCELLED, EXPIRED or CLOSED — a finished request stays finished.
var transitions = map[string]map[string]string{
	CommandSubmit: {StatusDraft: StatusSubmitted},
	CommandReturn: {
		StatusPendingReview:   StatusDraft,
		StatusPendingDocument: StatusDraft,
	},
	CommandReject:           {StatusPendingReview: StatusRejected},
	CommandApprove:          {StatusPendingReview: StatusApproved},
	CommandPartiallyApprove: {StatusPendingReview: StatusPartiallyApproved},
	CommandCancel: {
		StatusDraft:             StatusCancelled,
		StatusSubmitted:         StatusCancelled,
		StatusPendingReview:     StatusCancelled,
		StatusPendingDocument:   StatusCancelled,
		StatusEligibilityFailed: StatusCancelled,
	},
}

// Target reports where a command leads from a status, and whether it is a legal move at
// all. Callers answer REQUEST_TRANSITION_INVALID when ok is false.
func Target(command, from string) (to string, ok bool) {
	byStatus, known := transitions[command]
	if !known {
		return "", false
	}
	to, ok = byStatus[from]
	return to, ok
}

// GateOutcomes are the four states the submit gate may leave a request in.
var GateOutcomes = []string{
	StatusEligibilityFailed, StatusPendingDocument, StatusPendingReview, StatusApproved,
}

// Editable reports whether the request's draft version may still be written to.
func Editable(status string) bool { return status == StatusDraft }

// DateOnly strips the clock from a day-valued field.
func DateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// ItemInput is one requested line as a caller sent it. The two decimals arrive as text
// and stay exact all the way to the numeric column.
type ItemInput struct {
	ServiceDefinitionID string
	RequestedQuantity   string
	UnitType            string
	RequestedAmount     string
	CurrencyCode        string
}

// DecisionItem is what a reviewer decided about one line.
type DecisionItem struct {
	LineNo             int
	Status             string
	ApprovedQuantity   string
	ApprovedAmount     string
	DecisionReasonCode string
}

// ValidateItems checks a whole line set. Line numbers are assigned by the service in the
// order the caller sent them, so nothing here validates them: the caller does not choose
// them and could not be wrong about them.
func ValidateItems(items []ItemInput) error {
	ve := &ValidationError{}
	switch {
	case len(items) == 0:
		ve.Add("items", "REQUIRED", "en az bir kalem gerekli")
		return ve.OrNil()
	case len(items) > MaxItems:
		ve.Add("items", "RANGE", "en fazla 100 kalem gönderilebilir")
		return ve.OrNil()
	}
	for i, item := range items {
		path := fmt.Sprintf("items[%d]", i)
		if strings.TrimSpace(item.ServiceDefinitionID) == "" {
			ve.Add(path+".serviceDefinitionId", "REQUIRED", "hizmet tanımı zorunlu")
		}
		quantity, err := benefit.ParseQuantity(item.RequestedQuantity)
		switch {
		case err != nil:
			ve.Add(path+".requestedQuantity", "FORMAT", "kesin ondalık bir sayı olmalı")
		case !quantity.IsPositive():
			ve.Add(path+".requestedQuantity", "RANGE", "miktar sıfırdan büyük olmalı")
		}
		requireOneOf(ve, path+".unitType", item.UnitType, UnitTypes)
		validateMoney(ve, path, item.RequestedAmount, item.CurrencyCode)
	}
	return ve.OrNil()
}

// validateMoney checks the optional amount of a line. The schema pairs them: an amount
// without a currency is a number nobody can invoice, and a currency without an amount says
// nothing at all.
func validateMoney(ve *ValidationError, path, amount, currency string) {
	amount, currency = strings.TrimSpace(amount), strings.TrimSpace(currency)
	if amount == "" && currency == "" {
		return
	}
	if amount == "" {
		ve.Add(path+".requestedAmount", "REQUIRED", "para birimi verildiyse tutar da gerekli")
		return
	}
	value, err := benefit.ParseQuantity(amount)
	switch {
	case err != nil:
		ve.Add(path+".requestedAmount", "FORMAT", "kesin ondalık bir sayı olmalı")
	case value.IsNegative():
		ve.Add(path+".requestedAmount", "RANGE", "tutar negatif olamaz")
	}
	if !currencyPattern.MatchString(currency) {
		ve.Add(path+".currencyCode", "FORMAT", "ISO 4217 üç harfli kod olmalı")
	}
}

// NewRequest is the create command.
type NewRequest struct {
	RequestType      string
	ServiceDate      time.Time
	RequestedStartAt *time.Time
	RequestedEndAt   *time.Time
	Channel          string
	Items            []ItemInput
}

// ValidateNewRequest checks a create command. Whether the ids it names exist, and whether
// they belong together, is the application layer's business: only it can read them.
func ValidateNewRequest(in NewRequest) error {
	ve := &ValidationError{}
	requireOneOf(ve, "requestType", in.RequestType, RequestTypes)
	requireOneOf(ve, "channel", in.Channel, Channels)
	if in.ServiceDate.IsZero() {
		ve.Add("serviceDate", "REQUIRED", "hizmet tarihi zorunlu")
	}
	validateWindow(ve, in.RequestedStartAt, in.RequestedEndAt)
	if err := ValidateItems(in.Items); err != nil {
		appendFields(ve, err)
	}
	return ve.OrNil()
}

// RequestPatch is the merge-patch of a draft header.
type RequestPatch struct {
	ProviderOrganizationID    *string
	ClearProviderOrganization bool
	ServiceDate               *time.Time
	RequestedStartAt          *time.Time
	ClearRequestedStartAt     bool
	RequestedEndAt            *time.Time
	ClearRequestedEndAt       bool
}

// ValidatePatch checks a merge-patch of the draft header against the values it will
// produce, which is why the caller passes the merged window rather than the patch's own.
func ValidatePatch(start, end *time.Time) error {
	ve := &ValidationError{}
	validateWindow(ve, start, end)
	return ve.OrNil()
}

func validateWindow(ve *ValidationError, start, end *time.Time) {
	if start != nil && end != nil && !end.After(*start) {
		ve.Add("requestedEndAt", "RANGE", "bitiş zamanı başlangıçtan sonra olmalı")
	}
	if start == nil && end != nil {
		ve.Add("requestedStartAt", "REQUIRED", "bitiş zamanı verildiyse başlangıç da gerekli")
	}
}

// ValidateReason checks the reason a command carries. Every transition owes one: a request
// that came back without a reason is what makes a member give up rather than correct it.
func ValidateReason(reasonCode string, reasonText *string) error {
	ve := &ValidationError{}
	if !reasonCodePattern.MatchString(reasonCode) {
		ve.Add("reasonCode", "FORMAT", "büyük harf, rakam ve alt çizgi; 2-80 karakter")
	}
	if reasonText != nil && utf8.RuneCountInString(*reasonText) > MaxReasonText {
		ve.Add("reasonText", "LENGTH", "en fazla 1000 karakter")
	}
	return ve.OrNil()
}

// ValidateComment checks the optional comment of submit.
func ValidateComment(comment *string) error {
	if comment == nil || utf8.RuneCountInString(*comment) <= MaxReasonText {
		return nil
	}
	ve := &ValidationError{}
	ve.Add("comment", "LENGTH", "en fazla 1000 karakter")
	return ve
}

// ValidateDecision checks a review decision. `partial` asks for the stricter reading:
// a partial approval has to name lines and at least one of them has to be something other
// than a full approval, because a partial approval that approved everything would leave a
// member reading a word that does not match the numbers.
func ValidateDecision(reasonCode string, reasonText *string, items []DecisionItem, partial bool) error {
	ve := &ValidationError{}
	if err := ValidateReason(reasonCode, reasonText); err != nil {
		appendFields(ve, err)
	}
	if len(items) > MaxItems {
		ve.Add("items", "RANGE", "en fazla 100 kalem kararı gönderilebilir")
		return ve.OrNil()
	}
	seen := make(map[int]int, len(items))
	reduced := false
	for i, item := range items {
		path := fmt.Sprintf("items[%d]", i)
		if item.LineNo < 1 {
			ve.Add(path+".lineNo", "RANGE", "satır numarası birden başlar")
		} else if first, dup := seen[item.LineNo]; dup {
			ve.Add(path+".lineNo", "DUPLICATE",
				fmt.Sprintf("bu satır items[%d] içinde de var", first))
		} else {
			seen[item.LineNo] = i
		}
		requireOneOf(ve, path+".status", item.Status, DecisionItemStatuses)
		if item.Status != ItemApproved {
			reduced = true
		}
		validateDecimal(ve, path+".approvedQuantity", item.ApprovedQuantity)
		validateDecimal(ve, path+".approvedAmount", item.ApprovedAmount)
		if item.DecisionReasonCode != "" && !reasonCodePattern.MatchString(item.DecisionReasonCode) {
			ve.Add(path+".decisionReasonCode", "FORMAT",
				"büyük harf, rakam ve alt çizgi; 2-80 karakter")
		}
	}
	if partial {
		switch {
		case len(items) == 0:
			ve.Add("items", "REQUIRED", "kısmi onayda kalem kararları zorunlu")
		case !reduced:
			ve.Add("items", "NOT_PARTIAL",
				"her kalem tam onaylandıysa bu kısmi onay değil; onay komutunu kullanın")
		}
	}
	return ve.OrNil()
}

func validateDecimal(ve *ValidationError, field, raw string) {
	if strings.TrimSpace(raw) == "" {
		return
	}
	value, err := benefit.ParseQuantity(raw)
	switch {
	case err != nil:
		ve.Add(field, "FORMAT", "kesin ondalık bir sayı olmalı")
	case value.IsNegative():
		ve.Add(field, "RANGE", "negatif olamaz")
	}
}

// ValidateStatusFilter checks the list filter's status against the closed list, so an
// unknown value is a field error rather than a silently empty page.
func ValidateStatusFilter(status string) error {
	if status == "" {
		return nil
	}
	ve := &ValidationError{}
	requireOneOf(ve, "status", status, Statuses)
	return ve.OrNil()
}

// ValidateChannelFilter is ValidateStatusFilter for the channel.
func ValidateChannelFilter(channel string) error {
	if channel == "" {
		return nil
	}
	ve := &ValidationError{}
	requireOneOf(ve, "channel", channel, Channels)
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

// appendFields folds one validation error into another, so a command that validates two
// things reports both rather than only the first that failed.
func appendFields(ve *ValidationError, err error) {
	var other *ValidationError
	if errors.As(err, &other) {
		ve.Fields = append(ve.Fields, other.Fields...)
	}
}
