// Package domain holds what a work queue, a work item, a comment and an approval policy
// may look like and how an item may move: the closed lists the schema repeats as CHECK
// constraints, the transition table that is the only way through an item's life, and the
// validation of everything a caller may send. It depends on nothing outside the standard
// library and the exact-decimal type of the benefit module, so every rule here is
// unit-testable without a database.
//
// The one thing this package deliberately does not have is a way to compute a due date
// from a queue. A due date is taken once, from the queue the item was raised in, and
// stored; a helper that derived it on demand would be a helper somebody eventually calls
// twice, and the second answer would be the queue's clock rather than the item's.
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

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
	return fmt.Sprintf("workflow: %d validation error(s)", len(e.Fields))
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
var ErrValidation = errors.New("workflow: validation failed")

// Is lets errors.Is(err, ErrValidation) match a *ValidationError.
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

// Work item statuses (migration 000027).
const (
	StatusOpen      = "OPEN"
	StatusClaimed   = "CLAIMED"
	StatusCompleted = "COMPLETED"
	StatusCancelled = "CANCELLED"
	StatusEscalated = "ESCALATED"
)

// Commands. Each one is a transition with its own precondition and permission; the
// transition code is what the workflow.status_event row carries.
const (
	CommandClaim    = "CLAIM"
	CommandRelease  = "RELEASE"
	CommandReassign = "REASSIGN"
	CommandComplete = "COMPLETE"
	// CommandEscalate is the move the scheduler makes. It has no endpoint: nobody
	// decides it, the clock does.
	CommandEscalate = "ESCALATE"
)

// AggregateType is what a work item's status events are recorded under.
const AggregateType = "WORK_ITEM"

// Closed lists the database repeats as CHECK constraints.
var (
	Statuses = []string{
		StatusOpen, StatusClaimed, StatusCompleted, StatusCancelled, StatusEscalated,
	}
	AssignmentPolicies = []string{"MANUAL", "ROUND_ROBIN", "LEAST_LOADED"}
	DomainCodes        = []string{
		"GENERIC", "HEALTH", "ACCOMMODATION", "ASSISTANCE", "EDUCATION",
		"SPORT", "TRANSPORT", "CARE", "OTHER",
	}
	// Visibilities is who a comment was written for. The health package (M5) judges the
	// content of a PROVIDER or MEMBER comment against clinical rules; this package only
	// records which audience it was written for.
	Visibilities = []string{"INTERNAL", "PROVIDER", "MEMBER"}
)

// Limits mirroring the column CHECKs and the OpenAPI schema.
const (
	// MaxTitle bounds a work item's one-line description.
	MaxTitle = 200
	// MaxName bounds a queue's display name.
	MaxName = 200
	// MaxCommentBody bounds one comment.
	MaxCommentBody = 4000
	// MaxReasonText bounds the free-text half of a reason.
	MaxReasonText = 1000
	// MaxPolicies bounds one written approval policy set.
	MaxPolicies = 50
	// MaxRoleCodes bounds the roles one policy may require.
	MaxRoleCodes = 20
	// MinPriority and MaxPriority bound a work item's urgency; higher is more urgent, and
	// DefaultPriority is the middle the schema defaults to.
	MinPriority     = 1
	MaxPriority     = 1000
	DefaultPriority = 100
	// MaxSLAMinutes is one year, which is long enough for any queue that keeps a clock at
	// all and short enough that a mistyped value is refused rather than stored.
	MaxSLAMinutes = 525600
)

var (
	queueCodePattern  = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
	scopeCodePattern  = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	actionCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){0,3}$`)
	reasonCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_.:-]{1,79}$`)
	// outcomeCodePattern is what a completed item records as its decision. It is the same
	// shape as a reason code because it is one: "why is this item finished".
	outcomeCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_.:-]{1,79}$`)
)

// transitions is the whole life of a work item: which command may be given in which state
// and where it lands. Nothing outside this table is a legal move, and there is no entry
// that leads out of COMPLETED or CANCELLED — finished work stays finished.
//
// ESCALATED is a dead end for the commands too. An item the clock has given up on is
// handled by whoever owns the escalation queue, and letting a claim resurrect it would
// hide from the report the very fact the escalation exists to raise.
var transitions = map[string]map[string]string{
	CommandClaim:    {StatusOpen: StatusClaimed},
	CommandRelease:  {StatusClaimed: StatusOpen},
	CommandReassign: {StatusOpen: StatusClaimed, StatusClaimed: StatusClaimed},
	CommandComplete: {StatusClaimed: StatusCompleted},
}

// Target reports where a command leads from a status, and whether it is a legal move at
// all. Callers answer WORK_ITEM_TRANSITION_INVALID when ok is false.
func Target(command, from string) (to string, ok bool) {
	byStatus, known := transitions[command]
	if !known {
		return "", false
	}
	to, ok = byStatus[from]
	return to, ok
}

// Live reports whether an item is still waiting to be worked on. Only live items are ever
// escalated, and only live items appear on a worklist.
func Live(status string) bool { return status == StatusOpen || status == StatusClaimed }

// DateOnly strips the clock from a day-valued field.
func DateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// DueAt is what a queue's SLA makes of a moment. It exists so that the one place an item's
// clock is set states the arithmetic in Go as well as in SQL; the stored column is written
// by the insert, and this is what a test measures it against.
func DueAt(createdAt time.Time, slaMinutes *int) *time.Time {
	if slaMinutes == nil {
		return nil
	}
	due := createdAt.Add(time.Duration(*slaMinutes) * time.Minute)
	return &due
}

// NewQueue is the createWorkQueue command.
type NewQueue struct {
	Code              string
	Name              string
	DomainCode        string
	AssignmentPolicy  string
	SLAMinutes        *int
	EscalationQueueID string
	Active            bool
	// RequiredPermission is the permission this queue's work takes; empty means the default
	// the application fills in.
	RequiredPermission string
}

// Validate checks a new queue.
func (q NewQueue) Validate() error {
	ve := &ValidationError{}
	if !queueCodePattern.MatchString(strings.TrimSpace(q.Code)) {
		ve.Add("code", "FORMAT", "büyük harf, rakam ve alt çizgi; 2-64 karakter")
	}
	validateName(ve, "name", q.Name)
	requireOneOf(ve, "domainCode", q.DomainCode, DomainCodes)
	requireOneOf(ve, "assignmentPolicy", q.AssignmentPolicy, AssignmentPolicies)
	validateSLA(ve, "slaMinutes", q.SLAMinutes)
	if q.RequiredPermission != "" {
		validatePermissionCode(ve, "requiredPermission", q.RequiredPermission)
	}
	return ve.OrNil()
}

// QueuePatch is the merge-patch of a queue: a nil field is one the caller did not mention.
// The code and the domain are absent on purpose — they are what other rows already point
// at, and renaming them in place would silently repoint them.
type QueuePatch struct {
	Name             *string
	AssignmentPolicy *string
	// SLAMinutes is a two-level pointer because a merge patch distinguishes "not
	// mentioned" from "explicitly cleared", and clearing a queue's clock is a real thing
	// to want: it stops new items being given one.
	SLAMinutes        **int
	EscalationQueueID **string
	Active            *bool
	// RequiredPermission is the permission the queue's work takes. It is a plain pointer: it
	// cannot be cleared, because a queue everybody sees is not a state to patch into.
	RequiredPermission *string
}

// Validate checks a queue patch.
func (p QueuePatch) Validate() error {
	ve := &ValidationError{}
	if p.Name != nil {
		validateName(ve, "name", *p.Name)
	}
	if p.AssignmentPolicy != nil {
		requireOneOf(ve, "assignmentPolicy", *p.AssignmentPolicy, AssignmentPolicies)
	}
	if p.SLAMinutes != nil {
		validateSLA(ve, "slaMinutes", *p.SLAMinutes)
	}
	if p.RequiredPermission != nil {
		validatePermissionCode(ve, "requiredPermission", *p.RequiredPermission)
	}
	return ve.OrNil()
}

// NewItem is one piece of work raised into a queue. It has no HTTP route: items are raised
// by the module that produced the work, which is what keeps "why does this exist" a fact
// of that module rather than something a caller could invent.
type NewItem struct {
	QueueID       string
	AggregateType string
	AggregateID   string
	Title         string
	Priority      int
}

// Validate checks a raised item.
func (i NewItem) Validate() error {
	ve := &ValidationError{}
	if strings.TrimSpace(i.QueueID) == "" {
		ve.Add("queueId", "REQUIRED", "kuyruk zorunlu")
	}
	if !queueCodePattern.MatchString(strings.TrimSpace(i.AggregateType)) {
		ve.Add("aggregateType", "FORMAT", "büyük harf, rakam ve alt çizgi; 2-64 karakter")
	}
	if strings.TrimSpace(i.AggregateID) == "" {
		ve.Add("aggregateId", "REQUIRED", "kayıt kimliği zorunlu")
	}
	validateTitle(ve, "title", i.Title)
	if i.Priority < MinPriority || i.Priority > MaxPriority {
		ve.Add("priority", "RANGE", "1 ile 1000 arasında olmalı")
	}
	return ve.OrNil()
}

// NewComment is the addWorkItemComment command.
type NewComment struct {
	Visibility string
	Body       string
}

// Validate checks a comment.
func (c NewComment) Validate() error {
	ve := &ValidationError{}
	requireOneOf(ve, "visibility", c.Visibility, Visibilities)
	body := strings.TrimSpace(c.Body)
	switch {
	case body == "":
		ve.Add("body", "REQUIRED", "yorum metni zorunlu")
	case len([]rune(body)) > MaxCommentBody:
		ve.Add("body", "LENGTH", "en fazla 4000 karakter")
	}
	return ve.OrNil()
}

// Policy is one approval policy as a caller sent it. Both amounts arrive as exact decimal
// text and stay exact all the way to the numeric column: a band that had passed through a
// float would refuse an approval it should have allowed, one hundredth of a lira from the
// edge.
type Policy struct {
	ScopeCode             string
	MinAmount             string
	MaxAmount             string
	RequiredRoleCodes     []string
	RequiredApproverCount int
	ValidFrom             time.Time
	ValidTo               *time.Time
}

// PolicySet is the whole set written for one action by putApprovalPolicies.
type PolicySet struct {
	ActionCode string
	Policies   []Policy
}

// Validate checks a whole policy set, including the overlap the database also refuses.
// Catching it here means a caller is told which two entries collide rather than being told
// only that something did.
func (s PolicySet) Validate() error {
	ve := &ValidationError{}
	if !actionCodePattern.MatchString(strings.TrimSpace(s.ActionCode)) {
		ve.Add("actionCode", "FORMAT", "küçük harf ve nokta ile ayrılmış olmalı")
	}
	if len(s.Policies) > MaxPolicies {
		ve.Add("policies", "RANGE", "en fazla 50 politika gönderilebilir")
		return ve.OrNil()
	}
	for i, policy := range s.Policies {
		validatePolicy(ve, fmt.Sprintf("policies[%d]", i), policy)
	}
	s.validateOverlap(ve)
	return ve.OrNil()
}

// validateOverlap reports every pair in the set that shares a scope and a day. The
// exclusion constraint refuses the same thing, but it refuses one pair at a time and
// names a constraint rather than a field.
func (s PolicySet) validateOverlap(ve *ValidationError) {
	for i := range s.Policies {
		for j := i + 1; j < len(s.Policies); j++ {
			a, b := s.Policies[i], s.Policies[j]
			if a.ScopeCode != b.ScopeCode || !overlaps(a, b) {
				continue
			}
			ve.Add(fmt.Sprintf("policies[%d].validFrom", j), "OVERLAP",
				fmt.Sprintf("%d numaralı politika ile aynı kapsamda ve dönemde çakışıyor", i))
		}
	}
}

// overlaps compares two half-open day ranges, an open end meaning "for ever".
func overlaps(a, b Policy) bool {
	return (b.ValidTo == nil || a.ValidFrom.Before(*b.ValidTo)) &&
		(a.ValidTo == nil || b.ValidFrom.Before(*a.ValidTo))
}

func validatePolicy(ve *ValidationError, path string, p Policy) {
	if !scopeCodePattern.MatchString(strings.TrimSpace(p.ScopeCode)) {
		ve.Add(path+".scopeCode", "FORMAT", "büyük harf, rakam ve alt çizgi; 1-64 karakter")
	}
	minAmount := validateAmount(ve, path+".minAmount", p.MinAmount)
	maxAmount := validateAmount(ve, path+".maxAmount", p.MaxAmount)
	if minAmount != nil && maxAmount != nil && maxAmount.Cmp(*minAmount) < 0 {
		ve.Add(path+".maxAmount", "RANGE", "üst sınır alt sınırdan küçük olamaz")
	}
	if p.RequiredApproverCount < 1 {
		ve.Add(path+".requiredApproverCount", "RANGE", "en az bir onay gerekir")
	}
	if len(p.RequiredRoleCodes) > MaxRoleCodes {
		ve.Add(path+".requiredRoleCodes", "RANGE", "en fazla 20 rol verilebilir")
	}
	seen := map[string]bool{}
	for i, code := range p.RequiredRoleCodes {
		field := fmt.Sprintf("%s.requiredRoleCodes[%d]", path, i)
		if !queueCodePattern.MatchString(strings.TrimSpace(code)) {
			ve.Add(field, "FORMAT", "büyük harf, rakam ve alt çizgi; 2-64 karakter")
			continue
		}
		if seen[code] {
			ve.Add(field, "DUPLICATE", "aynı rol birden çok kez verilmiş")
		}
		seen[code] = true
	}
	if p.ValidFrom.IsZero() {
		ve.Add(path+".validFrom", "REQUIRED", "başlangıç tarihi zorunlu")
	}
	if p.ValidTo != nil && !p.ValidTo.After(p.ValidFrom) {
		ve.Add(path+".validTo", "RANGE", "bitiş tarihi başlangıçtan sonra olmalı")
	}
}

// validateAmount parses one optional band edge, returning nil when the caller left it open.
func validateAmount(ve *ValidationError, field, raw string) *benefit.Quantity {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	value, err := benefit.ParseQuantity(raw)
	switch {
	case err != nil:
		ve.Add(field, "FORMAT", "kesin ondalık bir sayı olmalı")
		return nil
	case value.IsNegative():
		ve.Add(field, "RANGE", "tutar negatif olamaz")
		return nil
	}
	return &value
}

// ValidateReason checks the reason a command carries. A reason code is optional on the
// worklist commands — taking a piece of work off somebody may be nothing more than a
// rota change — but a code that is given has to be one a report can group by.
func ValidateReason(field, code, text string) error {
	ve := &ValidationError{}
	code, text = strings.TrimSpace(code), strings.TrimSpace(text)
	if code != "" && !reasonCodePattern.MatchString(code) {
		ve.Add(field+"Code", "FORMAT", "büyük harf, rakam ve _ . : - ; 2-80 karakter")
	}
	if len([]rune(text)) > MaxReasonText {
		ve.Add(field+"Text", "LENGTH", "en fazla 1000 karakter")
	}
	return ve.OrNil()
}

// ValidateOutcome checks what a completed item records as its decision. It is required:
// an item closed with no outcome is a row nobody can report on, and "it was done" is not
// an answer to "what was decided".
func ValidateOutcome(code string) error {
	ve := &ValidationError{}
	if !outcomeCodePattern.MatchString(strings.TrimSpace(code)) {
		ve.Add("outcomeCode", "FORMAT", "büyük harf, rakam ve _ . : - ; 2-80 karakter")
	}
	return ve.OrNil()
}

func validateName(ve *ValidationError, field, value string) {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		ve.Add(field, "REQUIRED", "ad zorunlu")
	case len([]rune(value)) > MaxName:
		ve.Add(field, "LENGTH", "en fazla 200 karakter")
	}
}

func validateTitle(ve *ValidationError, field, value string) {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		ve.Add(field, "REQUIRED", "başlık zorunlu")
	case len([]rune(value)) > MaxTitle:
		ve.Add(field, "LENGTH", "en fazla 200 karakter")
	}
}

func validateSLA(ve *ValidationError, field string, minutes *int) {
	if minutes == nil {
		return
	}
	if *minutes < 1 || *minutes > MaxSLAMinutes {
		ve.Add(field, "RANGE", "1 dakika ile 1 yıl arasında olmalı")
	}
}

func requireOneOf(ve *ValidationError, field, value string, allowed []string) {
	for _, candidate := range allowed {
		if value == candidate {
			return
		}
	}
	ve.Add(field, "ENUM", "geçersiz değer: "+strings.Join(allowed, ", "))
}

// permissionCodePattern is iam.permission.code as migration 000002 spells it. A queue names
// the permission its work takes, and a name no permission has would make a queue nobody can
// see rather than a queue the right people see. An empty name is not checked here: it means
// "the default", and the application fills it in before the row is written.
var permissionCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,3}$`)

func validatePermissionCode(ve *ValidationError, field, code string) {
	if !permissionCodePattern.MatchString(strings.TrimSpace(code)) {
		ve.Add(field, "FORMAT", "yetki kodu biçimi geçersiz (ör. worklist.read)")
	}
}
