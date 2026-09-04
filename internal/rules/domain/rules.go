// Package domain holds the authoring rules of the rule engine: what a rule set, a version,
// a rule and a test case may look like, the status machine a version moves through, and
// the typed payload every action carries. It depends on nothing outside the standard
// library, the evaluator's closed action list and the exact-decimal type of the benefit
// module, so every rule here is unit-testable without a database.
//
// The evaluator itself lives in internal/rules/engine and is not touched from here: this
// package decides what may be written, the engine decides what a written rule does.
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	benefit "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/rules/engine"
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
	return fmt.Sprintf("rules: %d validation error(s)", len(e.Fields))
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
var ErrValidation = errors.New("rules: validation failed")

// Is lets errors.Is(err, ErrValidation) match a *ValidationError.
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

// Rule set statuses (migration 000022).
const (
	SetActive   = "ACTIVE"
	SetInactive = "INACTIVE"
)

// Rule set version statuses (migration 000022).
const (
	VersionDraft       = "DRAFT"
	VersionUnderReview = "UNDER_REVIEW"
	VersionPublished   = "PUBLISHED"
	VersionRetired     = "RETIRED"
)

// Closed lists the database repeats as CHECK constraints.
var (
	SetStatuses     = []string{SetActive, SetInactive}
	VersionStatuses = []string{VersionDraft, VersionUnderReview, VersionPublished, VersionRetired}
	DomainCodes     = []string{
		"GENERIC", "HEALTH", "ACCOMMODATION", "ASSISTANCE", "EDUCATION",
		"SPORT", "TRANSPORT", "CARE", "OTHER",
	}
	Purposes = []string{
		"ELIGIBILITY", "DOCUMENT", "PREAUTH", "LIMIT", "DUPLICATE",
		"DIAGNOSIS_SERVICE", "PRICE", "ADJUDICATION",
	}
	Outcomes = []string{
		string(engine.OutcomeApproved), string(engine.OutcomeRejected),
		string(engine.OutcomeReviewRequired), string(engine.OutcomePartiallyApproved),
	}
	// InputTypes is the CEL type vocabulary the evaluator's environment offers. It is
	// listed here so an unknown type is a field error at authoring time rather than a
	// compiler message the author has to decode.
	InputTypes = []string{"string", "int", "double", "bool", "timestamp", "duration", "map", "list"}
)

// Limits mirroring the column CHECKs of migration 000022.
const (
	MaxNotesLength       = 2000
	MaxCommentLength     = 1000
	MaxDescriptionLength = 500
	MaxConditionLength   = 8192
	MaxNameLength        = 200
	// MaxPriority keeps a priority inside the int32 the column stores while leaving room
	// for an author who numbers rules in hundreds.
	MaxPriority = 100000
	// MaxRules and MaxTestCases bound one set write; the OpenAPI schema repeats them.
	MaxRules     = 500
	MaxTestCases = 200
	// MaxInputVariables bounds the declared environment of one version.
	MaxInputVariables = 100
	// MaxActionsPerRule bounds the ordered action list of one rule.
	MaxActionsPerRule = 20
	// MaxExplanations bounds the expected explanation list of one test case.
	MaxExplanations = 100
)

var (
	setCodePattern   = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,39}$`)
	ruleCodePattern  = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
	variablePattern  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,63}$`)
	payloadCodeRegex = regexp.MustCompile(`^[A-Z][A-Z0-9_.:-]{0,63}$`)
)

// DateOnly strips the clock from a day-valued field.
func DateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// DayPtr is DateOnly through an optional value.
func DayPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	d := DateOnly(*t)
	return &d
}

// NewRuleSet is the create command of a rule set.
type NewRuleSet struct {
	Code       string
	Name       string
	DomainCode string
	Purpose    string
}

// RuleSetPatch is a merge-patch of a rule set.
type RuleSetPatch struct {
	Name            *string
	Status          *string
	ExpectedVersion int64
}

// VersionInput is the shape both create and patch validate a version against.
type VersionInput struct {
	ValidFrom   *time.Time
	ValidTo     *time.Time
	InputSchema map[string]string
	Notes       *string
}

// RuleInput is one authored rule of a set write.
type RuleInput struct {
	Code              string
	Name              string
	Priority          int
	Condition         string
	Actions           []ActionInput
	ExplanationCode   string
	ExplanationParams map[string]any
	StopOnMatch       bool
	Active            bool
}

// ActionInput is one action of a rule or of a test case expectation.
type ActionInput struct {
	Type    string
	Payload map[string]any
}

// TestCaseInput is one authored test case of a set write. ExpectedActions is nil when the
// case does not assert on actions at all, which is different from asserting that it
// produces none.
type TestCaseInput struct {
	Code                 string
	Description          *string
	Input                map[string]any
	ExpectedOutcome      string
	ExpectedExplanations []string
	ExpectedActions      []ActionInput
	AssertsActions       bool
}

// ValidateNewRuleSet checks a create command.
func ValidateNewRuleSet(in NewRuleSet) error {
	ve := &ValidationError{}
	if !setCodePattern.MatchString(in.Code) {
		ve.Add("code", "FORMAT", "büyük harf, rakam ve alt çizgi; 2-40 karakter")
	}
	validateName(ve, "name", in.Name)
	requireOneOf(ve, "domainCode", in.DomainCode, DomainCodes)
	requireOneOf(ve, "purpose", in.Purpose, Purposes)
	return ve.OrNil()
}

// ValidateRuleSetPatch checks a merge-patch of a rule set.
func ValidateRuleSetPatch(p RuleSetPatch) error {
	ve := &ValidationError{}
	if p.Name != nil {
		validateName(ve, "name", *p.Name)
	}
	if p.Status != nil {
		requireOneOf(ve, "status", *p.Status, SetStatuses)
	}
	return ve.OrNil()
}

// ValidateVersion checks the period, the declared input schema and the notes of a version.
func ValidateVersion(in VersionInput) error {
	ve := &ValidationError{}
	if in.ValidFrom != nil && in.ValidTo != nil && !in.ValidTo.After(*in.ValidFrom) {
		ve.Add("validTo", "RANGE", "bitiş tarihi başlangıçtan sonra olmalı")
	}
	validateInputSchema(ve, in.InputSchema)
	if in.Notes != nil && utf8.RuneCountInString(*in.Notes) > MaxNotesLength {
		ve.Add("notes", "LENGTH", "en fazla 2000 karakter")
	}
	return ve.OrNil()
}

// validateInputSchema checks the declared variables against the CEL environment the
// evaluator builds. A variable nobody can declare is refused here rather than surfacing
// later as a compiler message about an unknown type.
func validateInputSchema(ve *ValidationError, schema map[string]string) {
	if len(schema) > MaxInputVariables {
		ve.Add("inputSchema", "LENGTH", "en fazla 100 değişken tanımlanabilir")
		return
	}
	for name, kind := range schema {
		field := "inputSchema." + name
		if !variablePattern.MatchString(name) {
			ve.Add(field, "FORMAT", "değişken adı harf veya alt çizgiyle başlamalı")
		}
		requireOneOf(ve, field, kind, InputTypes)
	}
}

// ValidateRules checks a whole rule set write. It stops short of compiling: the condition
// is compiled by the application layer against the version's own input schema, because
// only it knows what was declared.
func ValidateRules(items []RuleInput) error {
	ve := &ValidationError{}
	if len(items) > MaxRules {
		ve.Add("items", "LENGTH", "en fazla 500 kural yazılabilir")
		return ve.OrNil()
	}
	codes := make(map[string]int, len(items))
	priorities := make(map[int]string, len(items))
	for i, r := range items {
		path := fmt.Sprintf("items[%d]", i)
		if !ruleCodePattern.MatchString(r.Code) {
			ve.Add(path+".code", "FORMAT", "büyük harf, rakam ve alt çizgi; 2-64 karakter")
		} else if first, dup := codes[r.Code]; dup {
			ve.Add(path+".code", "DUPLICATE",
				fmt.Sprintf("bu kod items[%d] içinde de var", first))
		} else {
			codes[r.Code] = i
		}
		validateName(ve, path+".name", r.Name)
		switch {
		case r.Priority < 1 || r.Priority > MaxPriority:
			ve.Add(path+".priority", "RANGE", "1 ile 100000 arasında olmalı")
		default:
			// Two rules may not share a priority: evaluation order has to be total and
			// reproducible, and "whatever the index returned" is not an order. The
			// database says the same thing over uq_rule_priority, inactive rules
			// included, so this check does not exempt them either: an inactive rule is
			// one somebody is about to switch back on.
			if other, dup := priorities[r.Priority]; dup {
				ve.Add(path+".priority", "DUPLICATE", "bu öncelik "+other+" kuralında da var")
			} else {
				priorities[r.Priority] = r.Code
			}
		}
		if n := utf8.RuneCountInString(r.Condition); n == 0 || n > MaxConditionLength {
			ve.Add(path+".condition", "LENGTH", "1 ile 8192 karakter arasında olmalı")
		}
		if !ruleCodePattern.MatchString(r.ExplanationCode) {
			ve.Add(path+".explanationCode", "FORMAT", "büyük harf, rakam ve alt çizgi; 2-64 karakter")
		}
		validateActions(ve, path+".actions", r.Actions)
	}
	return ve.OrNil()
}

// ValidateTestCases checks a whole test case set write.
func ValidateTestCases(items []TestCaseInput) error {
	ve := &ValidationError{}
	if len(items) > MaxTestCases {
		ve.Add("items", "LENGTH", "en fazla 200 test senaryosu yazılabilir")
		return ve.OrNil()
	}
	codes := make(map[string]int, len(items))
	for i, c := range items {
		path := fmt.Sprintf("items[%d]", i)
		if !ruleCodePattern.MatchString(c.Code) {
			ve.Add(path+".code", "FORMAT", "büyük harf, rakam ve alt çizgi; 2-64 karakter")
		} else if first, dup := codes[c.Code]; dup {
			ve.Add(path+".code", "DUPLICATE", fmt.Sprintf("bu kod items[%d] içinde de var", first))
		} else {
			codes[c.Code] = i
		}
		if c.Description != nil && utf8.RuneCountInString(*c.Description) > MaxDescriptionLength {
			ve.Add(path+".description", "LENGTH", "en fazla 500 karakter")
		}
		if c.Input == nil {
			ve.Add(path+".input", "REQUIRED", "girdi belgesi gerekli")
		}
		requireOneOf(ve, path+".expectedOutcome", c.ExpectedOutcome, Outcomes)
		if len(c.ExpectedExplanations) > MaxExplanations {
			ve.Add(path+".expectedExplanations", "LENGTH", "en fazla 100 açıklama kodu")
		}
		for j, code := range c.ExpectedExplanations {
			if !ruleCodePattern.MatchString(code) {
				ve.Add(fmt.Sprintf("%s.expectedExplanations[%d]", path, j), "FORMAT",
					"büyük harf, rakam ve alt çizgi; 2-64 karakter")
			}
		}
		if c.AssertsActions {
			validateExpectedActions(ve, path+".expectedActions", c.ExpectedActions)
		}
	}
	return ve.OrNil()
}

// ValidateReasonCode checks the retire command.
func ValidateReasonCode(reasonCode string, reasonText *string) error {
	ve := &ValidationError{}
	if !ruleCodePattern.MatchString(reasonCode) {
		ve.Add("reasonCode", "FORMAT", "büyük harf, rakam ve alt çizgi; 2-64 karakter")
	}
	if reasonText != nil && utf8.RuneCountInString(*reasonText) > MaxCommentLength {
		ve.Add("reasonText", "LENGTH", "en fazla 1000 karakter")
	}
	return ve.OrNil()
}

// ValidateComment checks the optional review comment of submit and publish.
func ValidateComment(comment *string) error {
	if comment == nil {
		return nil
	}
	if utf8.RuneCountInString(*comment) > MaxCommentLength {
		ve := &ValidationError{}
		ve.Add("comment", "LENGTH", "en fazla 1000 karakter")
		return ve
	}
	return nil
}

func validateName(ve *ValidationError, field, name string) {
	n := utf8.RuneCountInString(strings.TrimSpace(name))
	if n < 2 || n > MaxNameLength {
		ve.Add(field, "LENGTH", "2 ile 200 karakter arasında olmalı")
	}
}

func requireOneOf(ve *ValidationError, field, value string, allowed []string) {
	for _, a := range allowed {
		if value == a {
			return
		}
	}
	ve.Add(field, "ENUM", "geçerli değerler: "+strings.Join(allowed, ", "))
}

// likeEscaper makes the caller's own wildcards literal inside an ILIKE pattern.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// LikePattern wraps a trimmed search term in the contains-pattern the ILIKE filter
// expects. An empty term means "no filter".
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

// Decimal reports whether a value is an exact decimal string. Every numeric value inside
// an action payload crosses this boundary as text: an adjusted price and a reserved
// entitlement are money and quantity, and a float would silently round them.
func Decimal(raw string) bool {
	_, err := benefit.ParseQuantity(raw)
	return err == nil
}
