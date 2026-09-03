package eligibility_test

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/eligibility"
)

// serviceDate is the day every table case is evaluated on.
var serviceDate = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

var (
	programID     = uuid.MustParse("00000000-0000-4000-8000-000000000001")
	otherProgram  = uuid.MustParse("00000000-0000-4000-8000-000000000002")
	planID        = uuid.MustParse("00000000-0000-4000-8000-000000000003")
	enrollmentA   = uuid.MustParse("00000000-0000-4000-8000-00000000000a")
	enrollmentB   = uuid.MustParse("00000000-0000-4000-8000-00000000000b")
	planVersionID = uuid.MustParse("00000000-0000-4000-8000-00000000000c")
	accountDental = uuid.MustParse("00000000-0000-4000-8000-00000000000d")
	accountShared = uuid.MustParse("00000000-0000-4000-8000-00000000000e")
)

func day(s string) time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return d
}

func dayPtr(s string) *time.Time {
	d := day(s)
	return &d
}

// base is a person who is eligible: active, enrolled, with a published plan version and
// a DENTAL account holding 100 units. Every case mutates one thing.
func base() eligibility.Input {
	return eligibility.Input{
		ServiceDate: serviceDate,
		Person:      eligibility.Person{ID: uuid.New(), Found: true, Status: "ACTIVE"},
		Memberships: []eligibility.Membership{
			{ID: uuid.New(), Status: "ACTIVE", ValidFrom: day("2026-01-01")},
		},
		Enrollments: []eligibility.Enrollment{
			{ID: enrollmentA, PlanID: planID, ProgramID: programID, Status: "ACTIVE", ValidFrom: day("2026-01-01")},
		},
		PlanVersion: &eligibility.PlanVersion{ID: planVersionID},
		Accounts: []eligibility.Account{
			{ID: accountDental, EntitlementCode: "DENTAL", UnitType: "COUNT", Available: domain.MustQuantity("100")},
		},
		Items: []eligibility.Item{
			{Index: 0, EntitlementCode: "DENTAL", Quantity: domain.MustQuantity("10")},
		},
	}
}

// codesOf collects the explanation codes of a result: the top level already carries the
// distinct item codes, so this is the full set of reasons.
func codesOf(r eligibility.Result) []string {
	out := make([]string, 0, len(r.Explanations))
	for _, e := range r.Explanations {
		out = append(out, e.Code)
	}
	sort.Strings(out)
	return out
}

func itemOutcomes(r eligibility.Result) []string {
	out := make([]string, 0, len(r.Items))
	for _, item := range r.Items {
		out = append(out, item.Outcome)
	}
	return out
}

func TestResolveOutcomesAndExplanations(t *testing.T) {
	cases := []struct {
		name         string
		mutate       func(in *eligibility.Input)
		wantOutcome  string
		wantCodes    []string
		wantItems    []string
		wantEnroll   uuid.UUID
		wantVersion  uuid.UUID
		wantEligible bool
	}{
		{
			name:        "eligible",
			mutate:      func(*eligibility.Input) {},
			wantOutcome: eligibility.OutcomeEligible,
			wantCodes:   []string{},
			wantItems:   []string{eligibility.ItemEligible},
			wantEnroll:  enrollmentA, wantVersion: planVersionID, wantEligible: true,
		},
		{
			name:        "person not found",
			mutate:      func(in *eligibility.Input) { in.Person = eligibility.Person{ID: in.Person.ID} },
			wantOutcome: eligibility.OutcomeMissingData,
			wantCodes:   []string{eligibility.CodePersonNotFound},
			wantItems:   []string{eligibility.ItemReviewRequired},
		},
		{
			name:        "person inactive",
			mutate:      func(in *eligibility.Input) { in.Person.Status = "INACTIVE" },
			wantOutcome: eligibility.OutcomeIneligible,
			wantCodes:   []string{eligibility.CodePersonInactive},
			wantItems:   []string{eligibility.ItemIneligible},
		},
		{
			name:        "no membership at all",
			mutate:      func(in *eligibility.Input) { in.Memberships = nil },
			wantOutcome: eligibility.OutcomeMissingData,
			wantCodes:   []string{eligibility.CodeMembershipNone},
			wantItems:   []string{eligibility.ItemReviewRequired},
		},
		{
			name: "membership ended the day before the service date",
			mutate: func(in *eligibility.Input) {
				in.Memberships[0].ValidTo = dayPtr("2026-06-01")
			},
			wantOutcome: eligibility.OutcomeMissingData,
			wantCodes:   []string{eligibility.CodeMembershipNone},
			wantItems:   []string{eligibility.ItemReviewRequired},
		},
		{
			name:        "membership suspended",
			mutate:      func(in *eligibility.Input) { in.Memberships[0].Status = "SUSPENDED" },
			wantOutcome: eligibility.OutcomeIneligible,
			wantCodes:   []string{eligibility.CodeMembershipSuspended},
			wantItems:   []string{eligibility.ItemIneligible},
		},
		{
			name:        "no enrollment",
			mutate:      func(in *eligibility.Input) { in.Enrollments = nil },
			wantOutcome: eligibility.OutcomeIneligible,
			wantCodes:   []string{eligibility.CodeEnrollmentNone},
			wantItems:   []string{eligibility.ItemIneligible},
		},
		{
			name: "enrollment of another program is not used when a program is requested",
			mutate: func(in *eligibility.Input) {
				in.ProgramID = otherProgram
			},
			wantOutcome: eligibility.OutcomeIneligible,
			wantCodes:   []string{eligibility.CodeEnrollmentNone},
			wantItems:   []string{eligibility.ItemIneligible},
		},
		{
			name:        "enrollment suspended",
			mutate:      func(in *eligibility.Input) { in.Enrollments[0].Status = "SUSPENDED" },
			wantOutcome: eligibility.OutcomeIneligible,
			wantCodes:   []string{eligibility.CodeEnrollmentSuspended},
			wantItems:   []string{eligibility.ItemIneligible},
		},
		{
			name: "two active enrollments need a human",
			mutate: func(in *eligibility.Input) {
				in.Enrollments = append(in.Enrollments, eligibility.Enrollment{
					ID: enrollmentB, PlanID: planID, ProgramID: programID,
					Status: "ACTIVE", ValidFrom: day("2026-03-01"),
				})
			},
			wantOutcome: eligibility.OutcomeReviewRequired,
			wantCodes:   []string{eligibility.CodeEnrollmentMultiple},
			wantItems:   []string{eligibility.ItemEligible},
			// The latest start wins, deterministically.
			wantEnroll: enrollmentB, wantVersion: planVersionID,
		},
		{
			name:        "no published plan version",
			mutate:      func(in *eligibility.Input) { in.PlanVersion = nil },
			wantOutcome: eligibility.OutcomeIneligible,
			wantCodes:   []string{eligibility.CodePlanVersionNone},
			wantItems:   []string{eligibility.ItemIneligible},
			wantEnroll:  enrollmentA,
		},
		{
			name: "balance insufficient",
			mutate: func(in *eligibility.Input) {
				in.Items[0].Quantity = domain.MustQuantity("120")
			},
			wantOutcome: eligibility.OutcomeIneligible,
			wantCodes:   []string{eligibility.CodeBalanceInsufficient},
			wantItems:   []string{eligibility.ItemIneligible},
			wantEnroll:  enrollmentA, wantVersion: planVersionID,
		},
		{
			name: "exactly the remaining balance is still eligible",
			mutate: func(in *eligibility.Input) {
				in.Items[0].Quantity = domain.MustQuantity("100")
			},
			wantOutcome: eligibility.OutcomeEligible,
			wantCodes:   []string{},
			wantItems:   []string{eligibility.ItemEligible},
			wantEnroll:  enrollmentA, wantVersion: planVersionID, wantEligible: true,
		},
		{
			name: "overdraft turns an insufficient balance into a warning",
			mutate: func(in *eligibility.Input) {
				in.Accounts[0].AllowOverdraft = true
				in.Items[0].Quantity = domain.MustQuantity("120")
			},
			wantOutcome: eligibility.OutcomeEligible,
			wantCodes:   []string{eligibility.CodeBalanceOverdraftAllowed},
			wantItems:   []string{eligibility.ItemEligible},
			wantEnroll:  enrollmentA, wantVersion: planVersionID, wantEligible: true,
		},
		{
			name: "item without an entitlement hint waits for the service catalogue",
			mutate: func(in *eligibility.Input) {
				in.Items[0].EntitlementCode = ""
			},
			wantOutcome: eligibility.OutcomeReviewRequired,
			wantCodes:   []string{eligibility.CodeServiceMappingPending},
			wantItems:   []string{eligibility.ItemReviewRequired},
			wantEnroll:  enrollmentA, wantVersion: planVersionID,
		},
		{
			name: "hint that matches no account waits for the service catalogue",
			mutate: func(in *eligibility.Input) {
				in.Items[0].EntitlementCode = "PHYSIO"
			},
			wantOutcome: eligibility.OutcomeReviewRequired,
			wantCodes:   []string{eligibility.CodeServiceMappingPending},
			wantItems:   []string{eligibility.ItemReviewRequired},
			wantEnroll:  enrollmentA, wantVersion: planVersionID,
		},
		{
			name: "one line covered, one not",
			mutate: func(in *eligibility.Input) {
				in.Accounts = append(in.Accounts, eligibility.Account{
					ID: accountShared, EntitlementCode: "OPTIC", UnitType: "COUNT",
					Available: domain.MustQuantity("1"),
				})
				in.Items = append(in.Items, eligibility.Item{
					Index: 1, EntitlementCode: "OPTIC", Quantity: domain.MustQuantity("2"),
				})
			},
			wantOutcome: eligibility.OutcomePartiallyEligible,
			wantCodes:   []string{eligibility.CodeBalanceInsufficient},
			wantItems:   []string{eligibility.ItemEligible, eligibility.ItemIneligible},
			wantEnroll:  enrollmentA, wantVersion: planVersionID,
		},
		{
			name: "a review line outweighs a partial answer",
			mutate: func(in *eligibility.Input) {
				in.Items = append(in.Items, eligibility.Item{
					Index: 1, Quantity: domain.MustQuantity("1"),
				})
			},
			wantOutcome: eligibility.OutcomeReviewRequired,
			wantCodes:   []string{eligibility.CodeServiceMappingPending},
			wantItems:   []string{eligibility.ItemEligible, eligibility.ItemReviewRequired},
			wantEnroll:  enrollmentA, wantVersion: planVersionID,
		},
		{
			name: "a family-shared account of the principal covers the dependant",
			mutate: func(in *eligibility.Input) {
				in.Accounts = []eligibility.Account{{
					ID: accountShared, EntitlementCode: "DENTAL", UnitType: "COUNT",
					Available: domain.MustQuantity("50"), Shared: true,
				}}
			},
			wantOutcome: eligibility.OutcomeEligible,
			wantCodes:   []string{},
			wantItems:   []string{eligibility.ItemEligible},
			wantEnroll:  enrollmentA, wantVersion: planVersionID, wantEligible: true,
		},
		{
			name: "no requested line: the person-level answer stands on its own",
			mutate: func(in *eligibility.Input) {
				in.Items = nil
			},
			wantOutcome: eligibility.OutcomeEligible,
			wantCodes:   []string{},
			wantItems:   []string{},
			wantEnroll:  enrollmentA, wantVersion: planVersionID, wantEligible: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			tc.mutate(&in)
			got := eligibility.Resolve(in)

			if got.Outcome != tc.wantOutcome {
				t.Fatalf("outcome = %s, want %s", got.Outcome, tc.wantOutcome)
			}
			if got.Eligible != tc.wantEligible {
				t.Fatalf("eligible = %v, want %v", got.Eligible, tc.wantEligible)
			}
			if codes := codesOf(got); strings.Join(codes, ",") != strings.Join(tc.wantCodes, ",") {
				t.Fatalf("codes = %v, want %v", codes, tc.wantCodes)
			}
			if outcomes := itemOutcomes(got); strings.Join(outcomes, ",") != strings.Join(tc.wantItems, ",") {
				t.Fatalf("item outcomes = %v, want %v", outcomes, tc.wantItems)
			}
			if got.EnrollmentID != tc.wantEnroll {
				t.Fatalf("enrollment = %s, want %s", got.EnrollmentID, tc.wantEnroll)
			}
			if got.PlanVersionID != tc.wantVersion {
				t.Fatalf("plan version = %s, want %s", got.PlanVersionID, tc.wantVersion)
			}
			// Every non-eligible line carries at least one code (acceptance criterion).
			for _, item := range got.Items {
				if item.Outcome != eligibility.ItemEligible && len(item.Explanations) == 0 {
					t.Fatalf("item %d is %s without an explanation", item.Index, item.Outcome)
				}
			}
		})
	}
}

// TestResolveEverySeverityIsSet guards the severity table: an explanation without a
// severity would break the contract's enum.
func TestResolveEverySeverityIsSet(t *testing.T) {
	in := base()
	in.Enrollments = append(in.Enrollments, eligibility.Enrollment{
		ID: enrollmentB, PlanID: planID, ProgramID: programID, Status: "ACTIVE", ValidFrom: day("2026-03-01"),
	})
	in.Items = append(in.Items,
		eligibility.Item{Index: 1, Quantity: domain.MustQuantity("1")},
		eligibility.Item{Index: 2, EntitlementCode: "DENTAL", Quantity: domain.MustQuantity("500")},
	)
	got := eligibility.Resolve(in)

	want := map[string]string{
		eligibility.CodeEnrollmentMultiple:    eligibility.SeverityWarning,
		eligibility.CodeServiceMappingPending: eligibility.SeverityInfo,
		eligibility.CodeBalanceInsufficient:   eligibility.SeverityError,
	}
	seen := map[string]string{}
	for _, e := range got.Explanations {
		if e.Message == "" || e.Severity == "" {
			t.Fatalf("explanation %+v is incomplete", e)
		}
		seen[e.Code] = e.Severity
	}
	for code, severity := range want {
		if seen[code] != severity {
			t.Fatalf("severity of %s = %q, want %q", code, seen[code], severity)
		}
	}
}

// TestResolveIsDeterministic is the property the stored snapshot depends on: the same
// input always produces the same answer, whatever order the rows arrived in.
func TestResolveIsDeterministic(t *testing.T) {
	in := base()
	in.Enrollments = []eligibility.Enrollment{
		{ID: enrollmentA, PlanID: planID, ProgramID: programID, Status: "ACTIVE", ValidFrom: day("2026-01-01")},
		{ID: enrollmentB, PlanID: planID, ProgramID: programID, Status: "ACTIVE", ValidFrom: day("2026-03-01")},
	}
	reversed := base()
	reversed.Enrollments = []eligibility.Enrollment{in.Enrollments[1], in.Enrollments[0]}

	first, second := eligibility.Resolve(in), eligibility.Resolve(reversed)
	if first.EnrollmentID != second.EnrollmentID || first.Outcome != second.Outcome {
		t.Fatalf("row order changed the answer: %+v vs %+v", first, second)
	}
}

// TestSelectEnrollmentsBoundaries pins the half-open period rule the whole module uses.
func TestSelectEnrollmentsBoundaries(t *testing.T) {
	enrollments := []eligibility.Enrollment{{
		ID: enrollmentA, PlanID: planID, ProgramID: programID, Status: "ACTIVE",
		ValidFrom: day("2026-01-01"), ValidTo: dayPtr("2026-07-01"),
	}}
	cases := []struct {
		date string
		want int
	}{
		{"2025-12-31", 0},
		{"2026-01-01", 1}, // inclusive lower bound
		{"2026-06-30", 1},
		{"2026-07-01", 0}, // exclusive upper bound
	}
	for _, tc := range cases {
		if got := len(eligibility.SelectEnrollments(enrollments, uuid.Nil, day(tc.date))); got != tc.want {
			t.Fatalf("%s: %d active enrollments, want %d", tc.date, got, tc.want)
		}
	}
}
