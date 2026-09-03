// Package eligibility answers one question deterministically: may this person use this
// benefit on this day, with which plan version and which balances, and why not.
//
// Resolve is a pure function over already-loaded data (WP-I2-04 section 2.1): it never
// touches the database, so every explanation code and every outcome is table-testable.
// Service loads the data in one transaction, calls Resolve, stores the immutable
// evaluation snapshot and writes the access audit event.
package eligibility

import (
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/benefit/domain"
)

// Explanation codes (WP-I2-04 section 2.1). They are stable, machine-readable and part
// of the stored evaluation snapshot; the message beside them is for people.
const (
	CodePersonNotFound          = "PERSON_NOT_FOUND"
	CodePersonInactive          = "PERSON_INACTIVE"
	CodeMembershipNone          = "MEMBERSHIP_NONE"
	CodeMembershipSuspended     = "MEMBERSHIP_SUSPENDED"
	CodeEnrollmentNone          = "ENROLLMENT_NONE"
	CodeEnrollmentSuspended     = "ENROLLMENT_SUSPENDED"
	CodeEnrollmentMultiple      = "ENROLLMENT_MULTIPLE"
	CodePlanVersionNone         = "PLAN_VERSION_NONE"
	CodeBalanceInsufficient     = "BALANCE_INSUFFICIENT"
	CodeBalanceOverdraftAllowed = "BALANCE_OVERDRAFT_ALLOWED"
	CodeServiceMappingPending   = "SERVICE_MAPPING_PENDING"
)

// Severities of the contract's explanation objects.
const (
	SeverityInfo    = "INFO"
	SeverityWarning = "WARNING"
	SeverityError   = "ERROR"
)

// Request-level outcomes (contract EligibilityCheckResult.outcome).
const (
	OutcomeEligible          = "ELIGIBLE"
	OutcomePartiallyEligible = "PARTIALLY_ELIGIBLE"
	OutcomeIneligible        = "INELIGIBLE"
	OutcomeReviewRequired    = "REVIEW_REQUIRED"
	OutcomeMissingData       = "MISSING_DATA"
)

// Item outcomes (contract EligibilityCheckResult.items[].outcome).
const (
	ItemEligible       = "ELIGIBLE"
	ItemIneligible     = "INELIGIBLE"
	ItemReviewRequired = "REVIEW_REQUIRED"

	// Statuses of party.sponsor_membership and benefit.enrollment (migrations 000003,
	// 000004) this resolution distinguishes.
	statusActive    = "ACTIVE"
	statusSuspended = "SUSPENDED"
)

// severities fixes the severity of every code, so two evaluations of the same situation
// never differ in wording or weight.
var severities = map[string]string{
	CodePersonNotFound:          SeverityError,
	CodePersonInactive:          SeverityError,
	CodeMembershipNone:          SeverityError,
	CodeMembershipSuspended:     SeverityError,
	CodeEnrollmentNone:          SeverityError,
	CodeEnrollmentSuspended:     SeverityError,
	CodeEnrollmentMultiple:      SeverityWarning,
	CodePlanVersionNone:         SeverityError,
	CodeBalanceInsufficient:     SeverityError,
	CodeBalanceOverdraftAllowed: SeverityWarning,
	CodeServiceMappingPending:   SeverityInfo,
}

// messages are the user-facing texts (handbook section 3: Turkish for people, English
// for code).
var messages = map[string]string{
	CodePersonNotFound:          "Hak sahibi bulunamadı",
	CodePersonInactive:          "Hak sahibi kaydı aktif değil",
	CodeMembershipNone:          "Hizmet tarihinde geçerli sponsor üyeliği yok",
	CodeMembershipSuspended:     "Sponsor üyeliği askıya alınmış",
	CodeEnrollmentNone:          "Hizmet tarihinde geçerli plan kaydı yok",
	CodeEnrollmentSuspended:     "Plan kaydı askıya alınmış",
	CodeEnrollmentMultiple:      "Hizmet tarihinde birden fazla geçerli plan kaydı var",
	CodePlanVersionNone:         "Hizmet tarihinde yayınlanmış plan sürümü yok",
	CodeBalanceInsufficient:     "Hak bakiyesi yetersiz",
	CodeBalanceOverdraftAllowed: "Bakiye yetersiz, plan eksi bakiyeye izin veriyor",
	CodeServiceMappingPending:   "Hizmet tanımı bir hak koduna eşlenemedi",
}

// Explanation is one reason behind an outcome.
type Explanation struct {
	Code     string
	Message  string
	Severity string
}

// explain builds the explanation of a code; an unknown code degrades to an ERROR with
// the code as its own message rather than panicking on a caller's typo.
func explain(code string) Explanation {
	message, ok := messages[code]
	if !ok {
		message = code
	}
	severity, ok := severities[code]
	if !ok {
		severity = SeverityError
	}
	return Explanation{Code: code, Message: message, Severity: severity}
}

// Person is the part of party.person the resolution needs: no name, no identifier.
type Person struct {
	ID     uuid.UUID
	Found  bool
	Status string
}

// Membership is one party.sponsor_membership period.
type Membership struct {
	ID        uuid.UUID
	Status    string
	ValidFrom time.Time
	ValidTo   *time.Time
}

// Enrollment is one benefit.enrollment period with the plan and program it belongs to.
type Enrollment struct {
	ID        uuid.UUID
	PlanID    uuid.UUID
	ProgramID uuid.UUID
	Status    string
	ValidFrom time.Time
	ValidTo   *time.Time
}

// PlanVersion is the published configuration the check resolved for the service date.
type PlanVersion struct {
	ID uuid.UUID
}

// Account is one entitlement account the person can spend from on the service date.
type Account struct {
	ID              uuid.UUID
	EntitlementCode string
	UnitType        string
	Available       domain.Quantity
	AllowOverdraft  bool
	// Shared is true when the account was reached through a principal membership.
	Shared bool
}

// Item is one requested service line. EntitlementCode is the hint taken from the
// request context; it is empty until the service catalogue lands in I3.
type Item struct {
	Index           int
	EntitlementCode string
	Quantity        domain.Quantity
}

// Input is everything Resolve reads. The caller loads it in one transaction.
type Input struct {
	ServiceDate time.Time
	// ProgramID restricts the enrollment search; uuid.Nil means "any program".
	ProgramID   uuid.UUID
	Person      Person
	Memberships []Membership
	Enrollments []Enrollment
	// PlanVersion is nil when no version was published on the service date.
	PlanVersion *PlanVersion
	Accounts    []Account
	Items       []Item
}

// ItemResult is the verdict on one requested line.
type ItemResult struct {
	Index             int
	EntitlementCode   string
	Outcome           string
	RequestedQuantity domain.Quantity
	// AvailableQuantity is nil when no entitlement account could be matched.
	AvailableQuantity *domain.Quantity
	Explanations      []Explanation
}

// Balance is one entitlement balance reported alongside the outcome.
type Balance struct {
	EntitlementCode string
	Available       domain.Quantity
	Unit            string
}

// Result is the deterministic answer: the outcome, the versions it was taken against and
// every reason behind it.
type Result struct {
	Outcome       string
	Eligible      bool
	EnrollmentID  uuid.UUID
	PlanVersionID uuid.UUID
	Explanations  []Explanation
	Items         []ItemResult
	Balances      []Balance
}

// ActiveOn reports whether a half-open [from, to) period contains the day; an absent
// upper bound is open ended. Both bounds are compared day-wise, never clock-wise.
func ActiveOn(from time.Time, to *time.Time, day time.Time) bool {
	day = domain.DateOnly(day)
	if day.Before(domain.DateOnly(from)) {
		return false
	}
	if to != nil && !day.Before(domain.DateOnly(*to)) {
		return false
	}
	return true
}

// SelectEnrollments returns the enrollments that are ACTIVE on the day and, when a
// program is given, belong to it. The order is deterministic - latest start first, then
// id - so the same data always picks the same enrollment. The application service calls
// it before Resolve to know which plan's version to resolve; Resolve derives the same
// selection from the same slice, so the two can never disagree.
func SelectEnrollments(enrollments []Enrollment, programID uuid.UUID, day time.Time) []Enrollment {
	return selectEnrollments(enrollments, programID, day, statusActive)
}

func selectEnrollments(enrollments []Enrollment, programID uuid.UUID, day time.Time, status string) []Enrollment {
	out := make([]Enrollment, 0, len(enrollments))
	for _, e := range enrollments {
		if e.Status != status || !ActiveOn(e.ValidFrom, e.ValidTo, day) {
			continue
		}
		if programID != uuid.Nil && e.ProgramID != programID {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].ValidFrom.Equal(out[j].ValidFrom) {
			return out[i].ValidFrom.After(out[j].ValidFrom)
		}
		return out[i].ID.String() < out[j].ID.String()
	})
	return out
}

// Resolve runs the six steps of WP-I2-04 section 2.1 over loaded data. It is pure: the
// same input always produces the same result, which is what makes the stored evaluation
// snapshot reproducible in a dispute.
func Resolve(in Input) Result {
	day := domain.DateOnly(in.ServiceDate)
	res := Result{
		Explanations: []Explanation{},
		Items:        make([]ItemResult, 0, len(in.Items)),
		Balances:     balances(in.Accounts),
	}

	// 1. The person exists and is usable.
	if !in.Person.Found {
		return blocked(res, in.Items, CodePersonNotFound, OutcomeMissingData)
	}
	if in.Person.Status != statusActive {
		return blocked(res, in.Items, CodePersonInactive, OutcomeIneligible)
	}

	// 2. A sponsor membership covers the service date.
	if !hasMembership(in.Memberships, statusActive, day) {
		if hasMembership(in.Memberships, statusSuspended, day) {
			return blocked(res, in.Items, CodeMembershipSuspended, OutcomeIneligible)
		}
		return blocked(res, in.Items, CodeMembershipNone, OutcomeMissingData)
	}

	// 3. An enrollment covers the service date, restricted to the requested program.
	active := SelectEnrollments(in.Enrollments, in.ProgramID, day)
	if len(active) == 0 {
		if len(selectEnrollments(in.Enrollments, in.ProgramID, day, statusSuspended)) > 0 {
			return blocked(res, in.Items, CodeEnrollmentSuspended, OutcomeIneligible)
		}
		return blocked(res, in.Items, CodeEnrollmentNone, OutcomeIneligible)
	}
	res.EnrollmentID = active[0].ID
	review := false
	if len(active) > 1 {
		res.Explanations = append(res.Explanations, explain(CodeEnrollmentMultiple))
		review = true
	}

	// 4. A plan version was published on the service date.
	if in.PlanVersion == nil {
		return blocked(res, in.Items, CodePlanVersionNone, OutcomeIneligible)
	}
	res.PlanVersionID = in.PlanVersion.ID

	// 5. Every requested line is compared with the balance behind its entitlement code.
	accounts := accountsByCode(in.Accounts)
	eligible, needsReview := 0, 0
	for _, item := range in.Items {
		result := resolveItem(item, accounts)
		switch result.Outcome {
		case ItemEligible:
			eligible++
		case ItemReviewRequired:
			needsReview++
		}
		res.Items = append(res.Items, result)
	}

	// 6. The request-level outcome. A review code anywhere wins over a partial answer:
	// a counter must not be told "half eligible" when something could not be judged.
	res.Outcome = outcomeOf(len(in.Items), eligible, needsReview, review)
	res.Eligible = res.Outcome == OutcomeEligible
	res.Explanations = append(res.Explanations, itemExplanations(res.Items, res.Explanations)...)
	return res
}

// outcomeOf maps the item verdicts onto the request-level outcome.
func outcomeOf(items, eligible, review int, enrollmentReview bool) string {
	switch {
	case enrollmentReview || review > 0:
		return OutcomeReviewRequired
	case items == 0 || eligible == items:
		return OutcomeEligible
	case eligible > 0:
		return OutcomePartiallyEligible
	default:
		return OutcomeIneligible
	}
}

// resolveItem compares one requested quantity with the balance of its entitlement code.
// Without a code - or with a code no account in the plan version carries - the line is
// not judged at all: it is REVIEW_REQUIRED with SERVICE_MAPPING_PENDING until the
// service catalogue of I3 can map serviceDefinitionId onto an entitlement.
func resolveItem(item Item, accounts map[string]Account) ItemResult {
	out := ItemResult{
		Index: item.Index, EntitlementCode: item.EntitlementCode,
		RequestedQuantity: item.Quantity, Explanations: []Explanation{},
	}
	account, ok := accounts[item.EntitlementCode]
	if item.EntitlementCode == "" || !ok {
		out.Outcome = ItemReviewRequired
		out.Explanations = append(out.Explanations, explain(CodeServiceMappingPending))
		return out
	}
	available := account.Available
	out.AvailableQuantity = &available
	switch {
	case item.Quantity.Cmp(available) <= 0:
		out.Outcome = ItemEligible
	case account.AllowOverdraft:
		out.Outcome = ItemEligible
		out.Explanations = append(out.Explanations, explain(CodeBalanceOverdraftAllowed))
	default:
		out.Outcome = ItemIneligible
		out.Explanations = append(out.Explanations, explain(CodeBalanceInsufficient))
	}
	return out
}

// blocked ends the resolution at a person-level failure: the code is reported once at
// the top and echoed on every requested line, so no item is ever left unexplained.
func blocked(res Result, items []Item, code, outcome string) Result {
	explanation := explain(code)
	res.Outcome = outcome
	res.Eligible = false
	res.Explanations = append(res.Explanations, explanation)
	itemOutcome := ItemIneligible
	if outcome == OutcomeMissingData || outcome == OutcomeReviewRequired {
		itemOutcome = ItemReviewRequired
	}
	res.Items = make([]ItemResult, 0, len(items))
	for _, item := range items {
		res.Items = append(res.Items, ItemResult{
			Index: item.Index, EntitlementCode: item.EntitlementCode, Outcome: itemOutcome,
			RequestedQuantity: item.Quantity, Explanations: []Explanation{explanation},
		})
	}
	return res
}

// itemExplanations lifts the distinct item codes to the top level, so a reader of the
// summary sees why the request is not simply eligible without walking the lines.
func itemExplanations(items []ItemResult, existing []Explanation) []Explanation {
	seen := make(map[string]struct{}, len(existing))
	for _, e := range existing {
		seen[e.Code] = struct{}{}
	}
	out := []Explanation{}
	for _, item := range items {
		for _, e := range item.Explanations {
			if _, ok := seen[e.Code]; ok {
				continue
			}
			seen[e.Code] = struct{}{}
			out = append(out, e)
		}
	}
	return out
}

// hasMembership reports whether a membership of the status covers the day.
func hasMembership(memberships []Membership, status string, day time.Time) bool {
	for _, m := range memberships {
		if m.Status == status && ActiveOn(m.ValidFrom, m.ValidTo, day) {
			return true
		}
	}
	return false
}

// accountsByCode picks one account per entitlement code. A dependant reaches the
// family-shared account of their principal, and a person enrolled in two plans can hold
// the same code twice; the largest available balance wins, with the person's own account
// ahead of a shared one and the account id as the last tie-breaker, so the choice is
// deterministic.
func accountsByCode(accounts []Account) map[string]Account {
	out := make(map[string]Account, len(accounts))
	for _, a := range accounts {
		best, ok := out[a.EntitlementCode]
		if !ok || betterAccount(a, best) {
			out[a.EntitlementCode] = a
		}
	}
	return out
}

func betterAccount(candidate, current Account) bool {
	if cmp := candidate.Available.Cmp(current.Available); cmp != 0 {
		return cmp > 0
	}
	if candidate.Shared != current.Shared {
		return !candidate.Shared
	}
	return candidate.ID.String() < current.ID.String()
}

// balances reports every account the person can spend from, family-shared ones included,
// in a stable order.
func balances(accounts []Account) []Balance {
	out := make([]Balance, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, Balance{EntitlementCode: a.EntitlementCode, Available: a.Available, Unit: a.UnitType})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].EntitlementCode != out[j].EntitlementCode {
			return out[i].EntitlementCode < out[j].EntitlementCode
		}
		return out[i].Available.Cmp(out[j].Available) > 0
	})
	return out
}
