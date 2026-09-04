package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/celikbros/kapsora/internal/workflow/domain"
)

// TestTransitionsAreTheOnlyWayThrough: finished work stays finished, and an item the clock
// gave up on is not resurrected by somebody picking it up.
func TestTransitionsAreTheOnlyWayThrough(t *testing.T) {
	for _, tc := range []struct {
		command, from, to string
		ok                bool
	}{
		{domain.CommandClaim, domain.StatusOpen, domain.StatusClaimed, true},
		{domain.CommandClaim, domain.StatusClaimed, "", false},
		{domain.CommandClaim, domain.StatusEscalated, "", false},
		{domain.CommandClaim, domain.StatusCompleted, "", false},
		{domain.CommandRelease, domain.StatusClaimed, domain.StatusOpen, true},
		{domain.CommandRelease, domain.StatusOpen, "", false},
		{domain.CommandReassign, domain.StatusOpen, domain.StatusClaimed, true},
		{domain.CommandReassign, domain.StatusClaimed, domain.StatusClaimed, true},
		{domain.CommandReassign, domain.StatusCancelled, "", false},
		{domain.CommandComplete, domain.StatusClaimed, domain.StatusCompleted, true},
		{domain.CommandComplete, domain.StatusOpen, "", false},
		// The escalation job is not a command anybody may give.
		{domain.CommandEscalate, domain.StatusOpen, "", false},
	} {
		to, ok := domain.Target(tc.command, tc.from)
		if ok != tc.ok || to != tc.to {
			t.Fatalf("%s from %s = (%q, %t), want (%q, %t)", tc.command, tc.from, to, ok, tc.to, tc.ok)
		}
	}
}

// TestDueAtIsTheClockAnItemWasGiven states the arithmetic in Go so a stored due date has
// something to be measured against.
func TestDueAtIsTheClockAnItemWasGiven(t *testing.T) {
	created := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	if due := domain.DueAt(created, nil); due != nil {
		t.Fatalf("a queue with no SLA gives no due date, got %s", due)
	}
	minutes := 90
	due := domain.DueAt(created, &minutes)
	if due == nil || !due.Equal(created.Add(90*time.Minute)) {
		t.Fatalf("dueAt = %v, want %s", due, created.Add(90*time.Minute))
	}
}

// TestPolicySetRefusesOverlapAndNamesBothEntries: the exclusion constraint refuses the same
// thing one pair at a time and names a constraint; this names the field a caller sent.
func TestPolicySetRefusesOverlapAndNamesBothEntries(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mid := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	base := domain.Policy{ScopeCode: "STANDARD", RequiredApproverCount: 1, ValidFrom: from}

	overlapping := domain.PolicySet{ActionCode: "service_request.approve", Policies: []domain.Policy{
		base, {ScopeCode: "STANDARD", RequiredApproverCount: 2, ValidFrom: mid},
	}}
	if err := overlapping.Validate(); err == nil {
		t.Fatal("an open-ended policy overlaps everything after it")
	}

	closed := domain.PolicySet{ActionCode: "service_request.approve", Policies: []domain.Policy{
		{ScopeCode: "STANDARD", RequiredApproverCount: 1, ValidFrom: from, ValidTo: &mid},
		{ScopeCode: "STANDARD", RequiredApproverCount: 2, ValidFrom: mid},
	}}
	if err := closed.Validate(); err != nil {
		t.Fatalf("two periods that meet but do not overlap must be allowed: %v", err)
	}

	// A different scope on the same day is how one action carries more than one band.
	scoped := domain.PolicySet{ActionCode: "service_request.approve", Policies: []domain.Policy{
		base, {ScopeCode: "HIGH_VALUE", RequiredApproverCount: 3, ValidFrom: from},
	}}
	if err := scoped.Validate(); err != nil {
		t.Fatalf("two scopes on the same day must be allowed: %v", err)
	}
}

// TestPolicyAmountsMustBeExactDecimals: a band edge that is not a decimal is refused here
// rather than arriving at the numeric column as something PostgreSQL has to guess about.
func TestPolicyAmountsMustBeExactDecimals(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	set := domain.PolicySet{ActionCode: "service_request.approve", Policies: []domain.Policy{{
		ScopeCode: "STANDARD", MinAmount: "1e3", MaxAmount: "-5",
		RequiredApproverCount: 1, ValidFrom: from,
	}}}
	err := set.Validate()
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("err = %v, want a validation error", err)
	}
	fields := fieldCodes(t, err)
	if fields["policies[0].minAmount"] != "FORMAT" {
		t.Fatalf("minAmount code = %q, want FORMAT", fields["policies[0].minAmount"])
	}
	if fields["policies[0].maxAmount"] != "RANGE" {
		t.Fatalf("maxAmount code = %q, want RANGE", fields["policies[0].maxAmount"])
	}

	// An inverted band is caught too, and named on the edge the caller can move.
	inverted := domain.PolicySet{ActionCode: "service_request.approve", Policies: []domain.Policy{{
		ScopeCode: "STANDARD", MinAmount: "1000", MaxAmount: "10",
		RequiredApproverCount: 1, ValidFrom: from,
	}}}
	if fieldCodes(t, inverted.Validate())["policies[0].maxAmount"] != "RANGE" {
		t.Fatal("a band whose top is below its floor must be refused")
	}
}

// TestQueueValidationHoldsTheColumnRules so a caller is told which field is wrong rather
// than being handed a constraint name.
func TestQueueValidationHoldsTheColumnRules(t *testing.T) {
	tooLong := 525601
	queue := domain.NewQueue{
		Code: "lower case", Name: strings.Repeat("x", 201), DomainCode: "NOWHERE",
		AssignmentPolicy: "MAGIC", SLAMinutes: &tooLong,
	}
	fields := fieldCodes(t, queue.Validate())
	for field, code := range map[string]string{
		"code": "FORMAT", "name": "LENGTH", "domainCode": "ENUM",
		"assignmentPolicy": "ENUM", "slaMinutes": "RANGE",
	} {
		if fields[field] != code {
			t.Fatalf("%s code = %q, want %q", field, fields[field], code)
		}
	}

	ok := domain.NewQueue{
		Code: "MEDICAL_REVIEW", Name: "Tıbbi inceleme", DomainCode: "HEALTH",
		AssignmentPolicy: "MANUAL",
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("a well-formed queue must be accepted: %v", err)
	}
}

// TestCommentAndOutcomeValidation: an item closed with no outcome reports nothing, and a
// comment written for nobody in particular has no rule to be judged against.
func TestCommentAndOutcomeValidation(t *testing.T) {
	if err := (domain.NewComment{Visibility: "EVERYBODY", Body: "x"}).Validate(); err == nil {
		t.Fatal("a visibility outside the closed list must be refused")
	}
	if err := (domain.NewComment{Visibility: "INTERNAL", Body: "   "}).Validate(); err == nil {
		t.Fatal("a comment with nothing in it must be refused")
	}
	if err := (domain.NewComment{Visibility: "PROVIDER", Body: "Rapor eksik"}).Validate(); err != nil {
		t.Fatalf("a well-formed comment must be accepted: %v", err)
	}
	if err := domain.ValidateOutcome(""); err == nil {
		t.Fatal("an outcome is required")
	}
	if err := domain.ValidateOutcome("APPROVED"); err != nil {
		t.Fatalf("a well-formed outcome must be accepted: %v", err)
	}
	// A reason is optional on the worklist commands, but a code that is given has to be
	// one a report can group by.
	if err := domain.ValidateReason("reason", "", ""); err != nil {
		t.Fatalf("no reason at all must be accepted: %v", err)
	}
	if err := domain.ValidateReason("reason", "lower", ""); err == nil {
		t.Fatal("a malformed reason code must be refused")
	}
}

func fieldCodes(t *testing.T, err error) map[string]string {
	t.Helper()
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want a validation error", err)
	}
	out := map[string]string{}
	for _, f := range ve.Fields {
		out[f.Field] = f.Code
	}
	return out
}
