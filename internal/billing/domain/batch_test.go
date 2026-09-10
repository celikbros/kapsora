package domain_test

import (
	"errors"
	"testing"
	"time"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/billing/domain"
)

// The icmal's arithmetic, without a database anywhere near it.
//
// Two properties are worth more than every other test in this package put together: a cut's
// shares sum to the cut exactly, and the four decided totals sum to the submitted total exactly.
// Both are asserted here on values chosen so that a rounding mistake cannot hide.

func q(t *testing.T, raw string) benefitdomain.Quantity {
	t.Helper()
	value, err := benefitdomain.ParseQuantity(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return value
}

func weights(t *testing.T, raw ...string) []benefitdomain.Quantity {
	t.Helper()
	out := make([]benefitdomain.Quantity, 0, len(raw))
	for _, value := range raw {
		out = append(out, q(t, value))
	}
	return out
}

// TestSplitProportionalIsExact is the property a CUT rests on.
func TestSplitProportionalIsExact(t *testing.T) {
	cases := []struct {
		name    string
		total   string
		weights []string
		want    []string
	}{
		{
			name:  "an even split needs no remainder",
			total: "100", weights: []string{"500", "300", "200"},
			want: []string{"50", "30", "20"},
		},
		{
			name: "the remainder lands on the largest allocation",
			// 100 × 1000/1501, 100 × 500/1501 and 100 × 1/1501, truncated, leave two
			// micro-units over.
			total: "100", weights: []string{"1000", "500", "1"},
			want: []string{"66.622253", "33.311125", "0.066622"},
		},
		{
			name:  "the largest is found wherever it sits",
			total: "100", weights: []string{"1", "500", "1000"},
			want: []string{"0.066622", "33.311125", "66.622253"},
		},
		{
			name:  "thirds, which no decimal scale can divide",
			total: "100", weights: []string{"1", "1", "1"},
			want: []string{"33.333334", "33.333333", "33.333333"},
		},
		{
			name:  "a single micro-unit goes somewhere rather than nowhere",
			total: "0.000001", weights: []string{"5", "5"},
			want: []string{"0.000001", "0"},
		},
		{
			name:  "nothing to spread is nothing on every claim",
			total: "0", weights: []string{"7", "3"},
			want: []string{"0", "0"},
		},
		{
			name:  "allocations of nothing put the whole cut somewhere findable",
			total: "10", weights: []string{"0", "0"},
			want: []string{"10", "0"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shares, err := domain.SplitProportional(q(t, tc.total), weights(t, tc.weights...))
			if err != nil {
				t.Fatalf("split: %v", err)
			}
			if len(shares) != len(tc.want) {
				t.Fatalf("got %d shares, want %d", len(shares), len(tc.want))
			}
			sum := benefitdomain.ZeroQuantity()
			for i, share := range shares {
				if share.String() != tc.want[i] {
					t.Errorf("share %d = %s, want %s", i, share.String(), tc.want[i])
				}
				sum = sum.Add(share)
			}
			// The property, restated on every case: the shares are the total, exactly.
			if sum.Cmp(q(t, tc.total)) != 0 {
				t.Fatalf("the shares sum to %s, want exactly %s", sum.String(), tc.total)
			}
		})
	}
}

// TestSplitProportionalRefusesWhatItCannotDivide keeps the two impossible inputs impossible.
func TestSplitProportionalRefusesWhatItCannotDivide(t *testing.T) {
	if _, err := domain.SplitProportional(q(t, "10"), nil); err == nil {
		t.Error("a cut was spread across no allocations")
	}
	if _, err := domain.SplitProportional(q(t, "10"), weights(t, "5", "-1")); err == nil {
		t.Error("a negative allocation was accepted as a weight")
	}
}

// TestDecidedTotalsReconcile is the equation `ck_billing_batch_totals` holds, checked here so a
// service is refused before the database refuses it.
func TestDecidedTotalsReconcile(t *testing.T) {
	rows := []domain.DecidedRow{
		{Decision: domain.DecisionApprove, Submitted: q(t, "1000"), Approved: q(t, "1000")},
		{Decision: domain.DecisionCut, Submitted: q(t, "800"), Approved: q(t, "612.34")},
		{Decision: domain.DecisionReturn, Submitted: q(t, "400"), Approved: q(t, "0")},
		{Decision: domain.DecisionReject, Submitted: q(t, "200"), Approved: q(t, "0")},
	}
	totals := domain.DecidedTotals(rows)
	if totals.Submitted.String() != "2400" {
		t.Fatalf("submitted = %s, want 2400", totals.Submitted.String())
	}
	if totals.Approved.String() != "1612.34" {
		t.Errorf("approved = %s, want 1612.34", totals.Approved.String())
	}
	if totals.Cut.String() != "187.66" {
		t.Errorf("cut = %s, want 187.66", totals.Cut.String())
	}
	if totals.Returned.String() != "400" || totals.Rejected.String() != "200" {
		t.Errorf("returned/rejected = %s/%s", totals.Returned.String(), totals.Rejected.String())
	}
	if !totals.Reconciles() {
		t.Fatal("the four totals do not add up to the submitted total")
	}

	// And the check is a check: a set that does not reconcile says so.
	broken := domain.BatchTotals{
		Submitted: q(t, "100"), Approved: q(t, "90"), Cut: q(t, "5"),
		Returned: benefitdomain.ZeroQuantity(), Rejected: benefitdomain.ZeroQuantity(),
	}
	if broken.Reconciles() {
		t.Fatal("totals that are five short reconciled")
	}
}

// TestValidateBatchDecisionTiesTheAmountToTheWord is the CHECK the database also holds, stated
// so the caller is told which field.
func TestValidateBatchDecisionTiesTheAmountToTheWord(t *testing.T) {
	submitted := q(t, "1000")

	// An approval is the submitted amount, exactly, whatever the caller sent.
	decision, approved, reason, _, err := domain.ValidateBatchDecision(domain.BatchDecisionInput{
		Decision: domain.DecisionApprove, ApprovedAmount: "1",
	}, submitted)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if decision != domain.DecisionApprove || approved.String() != "1000" || reason != "" {
		t.Fatalf("approve gave %s/%s/%q", decision, approved.String(), reason)
	}

	// A return and a rejection approve nothing and need a reason.
	for _, word := range []string{domain.DecisionReturn, domain.DecisionReject} {
		_, amount, code, _, err := domain.ValidateBatchDecision(domain.BatchDecisionInput{
			Decision: word, ReasonCode: "NOT_COVERED",
		}, submitted)
		if err != nil {
			t.Fatalf("%s: %v", word, err)
		}
		if !amount.IsZero() || code != "NOT_COVERED" {
			t.Errorf("%s gave %s/%s", word, amount.String(), code)
		}
		if _, _, _, _, err := domain.ValidateBatchDecision(domain.BatchDecisionInput{
			Decision: word,
		}, submitted); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("%s was accepted with no reason", word)
		}
	}

	// A cut is strictly between nothing and everything. Either end is a different decision
	// with a different word, and calling it a cut would hide what a provider disputes.
	for _, amount := range []string{"0", "1000", "1500", "-5", "kesinti"} {
		if _, _, _, _, err := domain.ValidateBatchDecision(domain.BatchDecisionInput{
			Decision: domain.DecisionCut, ApprovedAmount: amount, ReasonCode: "CONTRACT_TERMS",
		}, submitted); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("a cut to %q was accepted", amount)
		}
	}
	_, cut, _, _, err := domain.ValidateBatchDecision(domain.BatchDecisionInput{
		Decision: domain.DecisionCut, ApprovedAmount: "999.999999",
		ReasonCode: "CONTRACT_TERMS",
	}, submitted)
	if err != nil {
		t.Fatalf("a cut of one micro-unit was refused: %v", err)
	}
	if cut.String() != "999.999999" {
		t.Fatalf("the cut approved %s", cut.String())
	}

	// And a word the lifecycle does not have is refused before anything else is looked at.
	if _, _, _, _, err := domain.ValidateBatchDecision(domain.BatchDecisionInput{
		Decision: "MAYBE",
	}, submitted); !errors.Is(err, domain.ErrValidation) {
		t.Error("an unknown decision was accepted")
	}
}

// TestValidateNewBatchFillsTheDefaultsAndRefusesABackwardsPeriod.
func TestValidateNewBatchFillsTheDefaultsAndRefusesABackwardsPeriod(t *testing.T) {
	from := time.Date(2026, 3, 1, 13, 45, 0, 0, time.UTC)
	to := time.Date(2026, 3, 31, 22, 0, 0, 0, time.UTC)
	out, err := domain.ValidateNewBatch(domain.NewBatchInput{
		Period: domain.BatchPeriod{From: from, To: to},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if out.CurrencyCode != domain.DefaultCurrency || out.DomainCode != domain.DefaultDomainCode {
		t.Errorf("defaults = %s/%s", out.CurrencyCode, out.DomainCode)
	}
	// The clock is stripped: a period "from the 1st" is the 1st whatever timezone the caller's
	// clock was in.
	if !out.Period.From.Equal(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("period from = %s", out.Period.From)
	}

	if _, err := domain.ValidateNewBatch(domain.NewBatchInput{
		Period: domain.BatchPeriod{From: to, To: from},
	}); !errors.Is(err, domain.ErrValidation) {
		t.Error("a period that ends before it starts was accepted")
	}
}

// TestNewBatchReferenceMatchesTheColumn keeps the generator and `ck_billing_batch_reference` in
// step: a reference the database refuses would be an icmal nobody could open.
func TestNewBatchReferenceMatchesTheColumn(t *testing.T) {
	now := time.Date(2026, 3, 20, 9, 0, 0, 0, time.UTC)
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		reference, err := domain.NewBatchReference(now)
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if len(reference) != len("IC-202603-ABCDEFGH") {
			t.Fatalf("reference %q is the wrong length", reference)
		}
		if reference[:10] != "IC-202603-" {
			t.Fatalf("reference %q does not carry the month", reference)
		}
		for _, r := range reference[10:] {
			if (r < 'A' || r > 'Z') && (r < '2' || r > '7') {
				t.Fatalf("reference %q carries %q, which the column CHECK refuses",
					reference, r)
			}
		}
		seen[reference] = true
	}
	if len(seen) != 64 {
		t.Fatalf("64 references produced %d distinct values", len(seen))
	}
}
