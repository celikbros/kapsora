package pricing_test

import (
	"testing"

	"github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/pricing"
)

const minorUnits = 2 // TRY, and every other currency the pilot will see

func m(t *testing.T, raw string) pricing.Money {
	t.Helper()
	v, err := domain.ParseQuantity(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return v
}

func ptr(v pricing.Money) *pricing.Money { return &v }

func fixed(t *testing.T, amount string) *pricing.Price {
	t.Helper()
	return &pricing.Price{Method: pricing.MethodFixed, Amount: m(t, amount), ShareMethod: pricing.ShareNone}
}

func line(t *testing.T, price *pricing.Price, available string) pricing.Item {
	t.Helper()
	return pricing.Item{
		LineNo: 1, Quantity: m(t, "1"), Price: price,
		Available: m(t, available), Eligible: true,
	}
}

func has(explanations []pricing.Explanation, code string) bool {
	for _, e := range explanations {
		if e.Code == code {
			return true
		}
	}
	return false
}

func TestFixedPriceFullyCovered(t *testing.T) {
	res := pricing.Calculate([]pricing.Item{line(t, fixed(t, "500"), "1000")}, minorUnits)
	got := res.Items[0]
	if got.Contract.String() != "500" || got.Payer.String() != "500" || got.Member.String() != "0" {
		t.Fatalf("contract %s payer %s member %s", got.Contract, got.Payer, got.Member)
	}
	if got.Outcome != pricing.OutcomeQuoted {
		t.Fatalf("outcome = %s", got.Outcome)
	}
}

// The worked example from the specification.
func TestBalanceOf300AgainstA500ServiceWithATwentyPerCentShare(t *testing.T) {
	price := fixed(t, "500")
	price.ShareMethod = pricing.SharePercent
	price.SharePercent = m(t, "20")

	res := pricing.Calculate([]pricing.Item{line(t, price, "300")}, minorUnits)
	got := res.Items[0]

	// 500 contract, member carries 20 % = 100, so the plan would carry 400. The balance
	// is 300, so the payer pays 300 and the member is left with 200.
	if got.Contract.String() != "500" {
		t.Fatalf("contract = %s, want 500", got.Contract)
	}
	if got.Covered.String() != "400" {
		t.Fatalf("covered = %s, want 400", got.Covered)
	}
	if got.Payer.String() != "300" {
		t.Fatalf("payer = %s, want 300", got.Payer)
	}
	if got.Member.String() != "200" {
		t.Fatalf("member = %s, want 200", got.Member)
	}
	if !has(got.Explanations, pricing.ExplanationMemberShare) ||
		!has(got.Explanations, pricing.ExplanationBalanceShort) {
		t.Fatalf("explanations = %+v", got.Explanations)
	}
	if got.Outcome != pricing.OutcomePartial {
		t.Fatalf("outcome = %s, want PARTIAL", got.Outcome)
	}
}

// The member share is a co-payment, not a surcharge: the two halves must always add up to
// the contract amount, or somebody is being billed twice.
func TestPayerAndMemberAlwaysSumToTheContractAmount(t *testing.T) {
	cases := []struct{ contract, share, available string }{
		{"500", "20", "300"},
		{"100.005", "33.333333", "10.001"},
		{"0.01", "50", "0"},
		{"999999.994999", "17.5", "500000.005"},
		{"33.33", "10", "9999"},
	}
	for _, c := range cases {
		price := fixed(t, c.contract)
		price.ShareMethod = pricing.SharePercent
		price.SharePercent = m(t, c.share)
		res := pricing.Calculate([]pricing.Item{line(t, price, c.available)}, minorUnits)
		got := res.Items[0]
		if got.Payer.Add(got.Member).Cmp(got.Contract) != 0 {
			t.Errorf("contract %s: payer %s + member %s != %s",
				c.contract, got.Payer, got.Member, got.Contract)
		}
	}
}

func TestUnitPriceMultipliesByQuantity(t *testing.T) {
	price := &pricing.Price{Method: pricing.MethodUnit, Amount: m(t, "12.5"), ShareMethod: pricing.ShareNone}
	item := line(t, price, "1000")
	item.Quantity = m(t, "4")
	res := pricing.Calculate([]pricing.Item{item}, minorUnits)
	if got := res.Items[0].Contract.String(); got != "50" {
		t.Fatalf("contract = %s, want 50", got)
	}
}

func TestPercentOfTheRequestedAmount(t *testing.T) {
	price := &pricing.Price{Method: pricing.MethodPercentOfList, Percent: m(t, "80"), ShareMethod: pricing.ShareNone}
	item := line(t, price, "1000")
	item.Requested = m(t, "250")
	res := pricing.Calculate([]pricing.Item{item}, minorUnits)
	if got := res.Items[0].Contract.String(); got != "200" {
		t.Fatalf("contract = %s, want 200", got)
	}
}

func TestMinimumAndMaximumClamp(t *testing.T) {
	low := fixed(t, "10")
	low.MinAmount = ptr(m(t, "50"))
	res := pricing.Calculate([]pricing.Item{line(t, low, "1000")}, minorUnits)
	if res.Items[0].Contract.String() != "50" || !has(res.Items[0].Explanations, pricing.ExplanationClampedToMin) {
		t.Fatalf("min clamp: %+v", res.Items[0])
	}

	high := fixed(t, "900")
	high.MaxAmount = ptr(m(t, "400"))
	res = pricing.Calculate([]pricing.Item{line(t, high, "1000")}, minorUnits)
	if res.Items[0].Contract.String() != "400" || !has(res.Items[0].Explanations, pricing.ExplanationClampedToMax) {
		t.Fatalf("max clamp: %+v", res.Items[0])
	}
}

// An unknown formula key must never quietly fall back to a raw amount: the number would
// look like a price and be nobody's decision.
func TestAnUnknownFormulaIsAnErrorNotAFallback(t *testing.T) {
	price := &pricing.Price{Method: pricing.MethodFormula, Amount: m(t, "750"), ShareMethod: pricing.ShareNone}
	res := pricing.Calculate([]pricing.Item{line(t, price, "1000")}, minorUnits)
	got := res.Items[0]
	if got.Contract.Sign() != 0 {
		t.Fatalf("contract = %s, want 0", got.Contract)
	}
	if !has(got.Explanations, pricing.ExplanationFormulaUnknown) {
		t.Fatalf("explanations = %+v", got.Explanations)
	}
}

func TestAnAmbiguousPriceCarriesNoMemberFigure(t *testing.T) {
	item := pricing.Item{LineNo: 1, Quantity: m(t, "1"), Eligible: true,
		NoPriceReason: pricing.ExplanationPriceAmbiguous}
	res := pricing.Calculate([]pricing.Item{item}, minorUnits)

	if res.Outcome != pricing.OutcomeReviewRequired {
		t.Fatalf("outcome = %s", res.Outcome)
	}
	if !res.Payer.IsZero() || !res.Member.IsZero() {
		t.Fatalf("payer %s member %s: a quote nobody can act on must show no figure",
			res.Payer, res.Member)
	}
	if !has(res.Items[0].Explanations, pricing.ExplanationPriceAmbiguous) {
		t.Fatalf("explanations = %+v", res.Items[0].Explanations)
	}
}

func TestOneUnpriceableLineMakesTheWholeQuoteUnusable(t *testing.T) {
	good := line(t, fixed(t, "100"), "1000")
	bad := pricing.Item{LineNo: 2, Quantity: m(t, "1"), Eligible: true,
		NoPriceReason: pricing.ExplanationPriceNotFound}
	res := pricing.Calculate([]pricing.Item{good, bad}, minorUnits)
	if res.Outcome != pricing.OutcomeReviewRequired {
		t.Fatalf("outcome = %s", res.Outcome)
	}
}

func TestAnIneligiblePersonStillGetsAPriceButNoCover(t *testing.T) {
	item := line(t, fixed(t, "500"), "1000")
	item.Eligible = false
	res := pricing.Calculate([]pricing.Item{item}, minorUnits)
	got := res.Items[0]

	if got.Contract.String() != "500" {
		t.Fatalf("contract = %s: the price is still the price", got.Contract)
	}
	if !got.Payer.IsZero() || got.Member.String() != "500" {
		t.Fatalf("payer %s member %s: the member may still pay privately", got.Payer, got.Member)
	}
	if got.Outcome != pricing.OutcomeNotEligible || !has(got.Explanations, pricing.ExplanationNotEligible) {
		t.Fatalf("line = %+v", got)
	}
}

func TestARuleLimitCapsWhatThePlanCarries(t *testing.T) {
	item := line(t, fixed(t, "500"), "1000")
	item.Adjustments = []pricing.Adjustment{
		{RuleCode: "ANNUAL_CAP", Kind: pricing.AdjustmentLimit, Value: m(t, "150")},
	}
	res := pricing.Calculate([]pricing.Item{item}, minorUnits)
	got := res.Items[0]
	if got.Payer.String() != "150" || got.Member.String() != "350" {
		t.Fatalf("payer %s member %s", got.Payer, got.Member)
	}
	for _, e := range got.Explanations {
		if e.Code == pricing.ExplanationLimitApplied && e.Source != "ANNUAL_CAP" {
			t.Fatalf("the limit must name the rule that set it: %+v", e)
		}
	}
}

// A rule may reduce what the plan carries. It may not raise it above the agreed price.
func TestARuleCannotRaiseTheCoverAboveTheContractAmount(t *testing.T) {
	item := line(t, fixed(t, "500"), "10000")
	item.Adjustments = []pricing.Adjustment{
		{RuleCode: "GENEROUS", Kind: pricing.AdjustmentAmount, Value: m(t, "5000")},
	}
	res := pricing.Calculate([]pricing.Item{item}, minorUnits)
	if got := res.Items[0].Payer.String(); got != "500" {
		t.Fatalf("payer = %s, want 500", got)
	}
}

func TestARuleAskingForReviewSuppressesTheFigures(t *testing.T) {
	item := line(t, fixed(t, "500"), "1000")
	item.Adjustments = []pricing.Adjustment{
		{RuleCode: "SECOND_OPINION", Kind: pricing.AdjustmentReview},
	}
	res := pricing.Calculate([]pricing.Item{item}, minorUnits)
	if res.Outcome != pricing.OutcomeReviewRequired {
		t.Fatalf("outcome = %s", res.Outcome)
	}
	if !res.Payer.IsZero() || !res.Member.IsZero() {
		t.Fatalf("payer %s member %s", res.Payer, res.Member)
	}
}

// Rounding happens once, at the end. Rounding each step instead would give a different
// total, which is the whole reason the rule exists.
func TestRoundingHappensOnceAtTheEnd(t *testing.T) {
	price := fixed(t, "10.005")
	price.ShareMethod = pricing.SharePercent
	price.SharePercent = m(t, "33.333333")

	items := make([]pricing.Item, 0, 3)
	for i := 1; i <= 3; i++ {
		item := line(t, price, "10000")
		item.LineNo = i
		items = append(items, item)
	}
	res := pricing.Calculate(items, minorUnits)

	// Each line rounds to 10.01 and three of them total 30.03.
	if res.Contract.String() != "30.03" {
		t.Fatalf("contract total = %s, want 30.03", res.Contract)
	}
	if res.Payer.Add(res.Member).Cmp(res.Contract) != 0 {
		t.Fatalf("payer %s + member %s != %s", res.Payer, res.Member, res.Contract)
	}
	if !has(res.Items[0].Explanations, pricing.ExplanationRounded) {
		t.Fatal("a rounded line must say so")
	}
}

func TestZeroBalanceLeavesTheWholePriceWithTheMember(t *testing.T) {
	res := pricing.Calculate([]pricing.Item{line(t, fixed(t, "250"), "0")}, minorUnits)
	got := res.Items[0]
	if !got.Payer.IsZero() || got.Member.String() != "250" {
		t.Fatalf("payer %s member %s", got.Payer, got.Member)
	}
	if got.Outcome != pricing.OutcomePartial {
		t.Fatalf("outcome = %s", got.Outcome)
	}
}

func TestAFixedShareNeverExceedsThePrice(t *testing.T) {
	price := fixed(t, "40")
	price.ShareMethod = pricing.ShareFixed
	price.ShareAmount = m(t, "100")
	res := pricing.Calculate([]pricing.Item{line(t, price, "1000")}, minorUnits)
	got := res.Items[0]
	if got.Member.String() != "40" || !got.Payer.IsZero() {
		t.Fatalf("payer %s member %s: a share larger than the price is the whole price",
			got.Payer, got.Member)
	}
}

func TestAnEmptyQuoteIsQuoted(t *testing.T) {
	res := pricing.Calculate(nil, minorUnits)
	if res.Outcome != pricing.OutcomeQuoted || !res.Contract.IsZero() {
		t.Fatalf("res = %+v", res)
	}
}

// Two services drawing on one entitlement account share its balance. Quoting each of them
// the whole balance would tell a member they owe nothing for 600 TRY of services against a
// 300 TRY account.
func TestTwoLinesOnOneAccountShareItsBalance(t *testing.T) {
	first := line(t, fixed(t, "300"), "300")
	first.AccountKey = "PHYSIO"
	second := line(t, fixed(t, "300"), "300")
	second.LineNo = 2
	second.AccountKey = "PHYSIO"

	res := pricing.Calculate([]pricing.Item{first, second}, minorUnits)

	if res.Items[0].Payer.String() != "300" || !res.Items[0].Member.IsZero() {
		t.Fatalf("first line: payer %s member %s", res.Items[0].Payer, res.Items[0].Member)
	}
	if !res.Items[1].Payer.IsZero() || res.Items[1].Member.String() != "300" {
		t.Fatalf("second line: payer %s member %s, want the balance already spent",
			res.Items[1].Payer, res.Items[1].Member)
	}
	if res.Payer.String() != "300" || res.Member.String() != "300" {
		t.Fatalf("totals: payer %s member %s", res.Payer, res.Member)
	}
}

func TestLinesOnDifferentAccountsKeepTheirOwnBalances(t *testing.T) {
	first := line(t, fixed(t, "300"), "300")
	first.AccountKey = "PHYSIO"
	second := line(t, fixed(t, "300"), "300")
	second.LineNo = 2
	second.AccountKey = "DENTAL"

	res := pricing.Calculate([]pricing.Item{first, second}, minorUnits)
	if res.Payer.String() != "600" || !res.Member.IsZero() {
		t.Fatalf("payer %s member %s", res.Payer, res.Member)
	}
}

// A line with no account key keeps the balance it was given, which is what a single-line
// quote and every fixture without an account relies on.
func TestLinesWithoutAnAccountKeyAreIndependent(t *testing.T) {
	first := line(t, fixed(t, "300"), "300")
	second := line(t, fixed(t, "300"), "300")
	second.LineNo = 2

	res := pricing.Calculate([]pricing.Item{first, second}, minorUnits)
	if res.Payer.String() != "600" {
		t.Fatalf("payer = %s, want 600", res.Payer)
	}
}
