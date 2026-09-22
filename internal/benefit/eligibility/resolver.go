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
// PlanCode and PlanName are carried for one reason: when the check has to answer
// ENROLLMENT_MULTIPLE it must name the plans well enough for a desk to choose between
// them, and an id is not a name.
type Enrollment struct {
	ID        uuid.UUID
	PlanID    uuid.UUID
	PlanCode  string
	PlanName  string
	ProgramID uuid.UUID
	Status    string
	ValidFrom time.Time
	ValidTo   *time.Time
}

// EnrollmentCandidate is one of the enrollments an ENROLLMENT_MULTIPLE answer was torn
// between. It is the minimum a desk needs to ask the question again naming one of them:
// a provider may not list a person's enrollments, and this is not a list — it is the
// choice the check itself already had to look at.
type EnrollmentCandidate struct {
	EnrollmentID uuid.UUID
	PlanCode     string
	PlanName     string
	ValidFrom    time.Time
	ValidTo      *time.Time
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

// Item is one requested service line. ServiceDefinitionID is what the mapping of the
// resolved plan version is looked up by; EntitlementCode is the older hint taken from the
// request context, which still answers for a service nobody has mapped yet.
//
// AlreadyHeld is entitlement this line has *already* reserved on the account it draws from,
// in the entitlement's own unit. It is added back before the balance is compared, so a
// request that took its hold before it was raised is not refused for spending what it is
// holding. It is the zero value everywhere except WP-I6-02's booking confirmation.
type Item struct {
	Index               int
	ServiceDefinitionID uuid.UUID
	EntitlementCode     string
	Quantity            domain.Quantity
	// AlreadyHeld is what this line has already reserved on the entitlement it draws from.
	AlreadyHeld domain.Quantity
}

// Mapping is one row of benefit.service_entitlement_mapping as the resolution reads it:
// which entitlement a service draws from, and how much of it one unit of the service
// draws. The factor is exact, like every other quantity here.
type Mapping struct {
	EntitlementCode string
	UnitFactor      domain.Quantity
}

// Input is everything Resolve reads. The caller loads it in one transaction.
type Input struct {
	ServiceDate time.Time
	// ProgramID restricts the enrollment search; uuid.Nil means "any program".
	ProgramID uuid.UUID
	// EnrollmentID answers a caller who was told ENROLLMENT_MULTIPLE and has chosen. It
	// narrows the search to one of the enrollments the check itself found; an id that is
	// not among them selects nothing, which lands on ENROLLMENT_NONE rather than on
	// somebody else's plan.
	EnrollmentID uuid.UUID
	Person       Person
	Memberships  []Membership
	Enrollments  []Enrollment
	// PlanVersion is nil when no version was published on the service date.
	PlanVersion *PlanVersion
	Accounts    []Account
	// Mappings is the service → entitlement mapping of the resolved plan version, keyed
	// by service definition. A line whose service is in here is judged against that
	// entitlement's balance; a line whose service is not is SERVICE_MAPPING_PENDING
	// unless the caller hinted a code.
	Mappings map[uuid.UUID]Mapping
	Items    []Item
}

// ItemResult is the verdict on one requested line.
type ItemResult struct {
	// Matched account metadata stays internal; pricing must distinguish money from
	// service quantities and pool lines against the exact account the resolver chose.
	AccountID         uuid.UUID
	UnitType          string
	DrawQuantity      domain.Quantity
	AllowOverdraft    bool
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
	// EnrollmentCandidates is filled only when ENROLLMENT_MULTIPLE was raised. It is
	// empty otherwise, so the presence of the field is itself the answer to "was there a
	// choice to make".
	EnrollmentCandidates []EnrollmentCandidate
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
	return selectEnrollments(enrollments, programID, uuid.Nil, day, statusActive)
}

// SelectEnrollmentsFor is SelectEnrollments narrowed to the enrollment the caller chose.
// uuid.Nil means "no choice was made" and behaves exactly as SelectEnrollments does.
func SelectEnrollmentsFor(enrollments []Enrollment, programID, enrollmentID uuid.UUID, day time.Time) []Enrollment {
	return selectEnrollments(enrollments, programID, enrollmentID, day, statusActive)
}

func selectEnrollments(enrollments []Enrollment, programID, enrollmentID uuid.UUID, day time.Time, status string) []Enrollment {
	out := make([]Enrollment, 0, len(enrollments))
	for _, e := range enrollments {
		if e.Status != status || !ActiveOn(e.ValidFrom, e.ValidTo, day) {
			continue
		}
		if programID != uuid.Nil && e.ProgramID != programID {
			continue
		}
		if enrollmentID != uuid.Nil && e.ID != enrollmentID {
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

	// 3. An enrollment covers the service date, restricted to the requested program and,
	// when the caller has already been told there was a choice, to the one they picked.
	active := SelectEnrollmentsFor(in.Enrollments, in.ProgramID, in.EnrollmentID, day)
	if len(active) == 0 {
		if len(selectEnrollments(in.Enrollments, in.ProgramID, in.EnrollmentID, day, statusSuspended)) > 0 {
			return blocked(res, in.Items, CodeEnrollmentSuspended, OutcomeIneligible)
		}
		return blocked(res, in.Items, CodeEnrollmentNone, OutcomeIneligible)
	}
	res.EnrollmentID = active[0].ID
	review := false
	if len(active) > 1 {
		res.Explanations = append(res.Explanations, explain(CodeEnrollmentMultiple))
		// Naming the choice is the point: a desk told "there is more than one plan" and
		// nothing else can only guess, and a provider is not allowed to list a person's
		// enrollments to find out.
		res.EnrollmentCandidates = candidates(active)
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
		result := resolveItem(item, accounts, in.Mappings)
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

// resolveItem compares one requested line with the balance of the entitlement behind it.
//
// The mapping of the resolved plan version decides first: a service that is mapped is
// judged against its entitlement's balance, and the requested quantity is multiplied by
// the mapping's unit factor, because one session of a service may draw two units of the
// entitlement. The request context's entitlement code is the fallback, for the plans
// nobody has mapped yet. A line with neither — and a line whose entitlement no account in
// the plan version carries — is not judged at all: it is REVIEW_REQUIRED with
// SERVICE_MAPPING_PENDING, which says the configuration is missing rather than pretending
// the answer is no.
func resolveItem(item Item, accounts map[string]Account, mappings map[uuid.UUID]Mapping) ItemResult {
	code, drawn := item.EntitlementCode, item.Quantity
	if mapping, ok := mappings[item.ServiceDefinitionID]; ok && mapping.EntitlementCode != "" {
		code = mapping.EntitlementCode
		if mapping.UnitFactor.IsPositive() {
			drawn = item.Quantity.Mul(mapping.UnitFactor)
		}
	}
	out := ItemResult{
		Index: item.Index, EntitlementCode: code,
		RequestedQuantity: item.Quantity, Explanations: []Explanation{},
	}
	account, ok := accounts[code]
	if code == "" || !ok {
		out.Outcome = ItemReviewRequired
		out.Explanations = append(out.Explanations, explain(CodeServiceMappingPending))
		return out
	}
	out.AccountID = account.ID
	out.UnitType = account.UnitType
	out.DrawQuantity = drawn
	out.AllowOverdraft = account.AllowOverdraft
	available := account.Available
	// The balance reported is the one the account really carries; the balance *compared*
	// adds back whatever this very line already holds.
	//
	// Without that, a request that reserved its entitlement before it was raised would be
	// judged against a balance it had itself drawn down, and would be refused for spending
	// what it is holding. WP-I6-02's booking is exactly that shape: the room and the nights
	// are taken at the hold, minutes before the reservation request reaches this gate, and a
	// member whose plan covers precisely the stay they held would be told they are
	// ineligible for it. AlreadyHeld is zero for every other caller, so nothing else moves.
	effective := available.Add(item.AlreadyHeld)
	out.AvailableQuantity = &available
	switch {
	case drawn.Cmp(effective) <= 0:
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

// candidates renders the enrollments an ENROLLMENT_MULTIPLE answer was torn between, in
// the order SelectEnrollments already put them in, so two identical checks name them the
// same way round.
func candidates(active []Enrollment) []EnrollmentCandidate {
	out := make([]EnrollmentCandidate, 0, len(active))
	for _, e := range active {
		out = append(out, EnrollmentCandidate{
			EnrollmentID: e.ID, PlanCode: e.PlanCode, PlanName: e.PlanName,
			ValidFrom: domain.DateOnly(e.ValidFrom), ValidTo: e.ValidTo,
		})
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
