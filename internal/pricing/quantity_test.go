package pricing_test

import (
	"testing"

	"github.com/celikbros/kapsora/internal/pricing"
)

func quantityLine(t *testing.T, amount, available, required string) pricing.Item {
	t.Helper()
	i := line(t, fixed(t, amount), available)
	i.AccountKey = "session-account"
	i.QuantityCover = &pricing.QuantityCover{Required: m(t, required)}
	return i
}

func TestQuantityCoverDoesNotCapMoney(t *testing.T) {
	for _, method := range []pricing.Method{pricing.MethodFixed, pricing.MethodUnit, pricing.MethodPercentOfList, pricing.MethodFormula} {
		t.Run(string(method), func(t *testing.T) {
			i := quantityLine(t, "400", "20", "1")
			i.Price.Method = method
			i.Price.Percent = m(t, "50")
			i.Requested = m(t, "800")
			i.Price.FormulaKnownAmount = ptr(m(t, "400"))
			i.Price.ShareMethod = pricing.SharePercent
			i.Price.SharePercent = m(t, "20")
			i.Adjustments = []pricing.Adjustment{{Kind: pricing.AdjustmentLimit, Value: m(t, "300")}}
			got := pricing.Calculate([]pricing.Item{i}, minorUnits).Items[0]
			if got.Payer.String() != "300" || got.Member.String() != "100" || got.Outcome != pricing.OutcomeQuoted {
				t.Fatalf("quantity became a monetary cap or bypassed share/rule: %+v", got)
			}
		})
	}
}

func TestQuantityPoolDrawsUnitsNotPayerAmount(t *testing.T) {
	items := []pricing.Item{
		quantityLine(t, "400", "2", "1"),
		quantityLine(t, "800", "2", "1"),
		quantityLine(t, "100", "2", "1"),
	}
	got := pricing.Calculate(items, minorUnits)
	if got.Items[0].Payer.String() != "400" || got.Items[1].Payer.String() != "800" || got.Items[2].Payer.String() != "0" {
		t.Fatalf("wrong quantity pool: %+v", got.Items)
	}
	if got.Items[2].Outcome != pricing.OutcomeNotEligible || !has(got.Items[2].Explanations, pricing.ExplanationBalanceShort) {
		t.Fatal("quantity shortage must refuse the line and explain why")
	}
	for _, l := range got.Items {
		if l.Payer.Add(l.Member).Cmp(l.Contract) != 0 {
			t.Fatal("payer + member differs from contract")
		}
	}
}

func TestQuantityCoverBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name, available, required string
		overdraft, eligible       bool
		want                      pricing.Outcome
	}{
		{"empty", "0", "1", false, true, pricing.OutcomeNotEligible},
		{"fraction exact", "0.5", "0.5", false, true, pricing.OutcomeQuoted},
		{"fraction short", "0.499999", "0.5", false, true, pricing.OutcomeNotEligible},
		{"overdraft", "0", "1", true, true, pricing.OutcomeQuoted},
		{"inactive member", "20", "1", true, false, pricing.OutcomeNotEligible},
	} {
		t.Run(tt.name, func(t *testing.T) {
			i := quantityLine(t, "400", tt.available, tt.required)
			i.QuantityCover.AllowOverdraft = tt.overdraft
			i.Eligible = tt.eligible
			got := pricing.Calculate([]pricing.Item{i}, minorUnits).Items[0]
			if got.Outcome != tt.want {
				t.Fatalf("outcome=%s, want %s", got.Outcome, tt.want)
			}
		})
	}
}

func TestUndecidedQuantityLineDoesNotSpendPool(t *testing.T) {
	for _, review := range []bool{true, false} {
		first := quantityLine(t, "500", "1", "1")
		if review {
			first.Adjustments = []pricing.Adjustment{{Kind: pricing.AdjustmentReview}}
		} else {
			first.Eligible = false
		}
		second := quantityLine(t, "400", "1", "1")
		got := pricing.Calculate([]pricing.Item{first, second}, minorUnits)
		if got.Items[1].Payer.String() != "400" {
			t.Fatal("unapproved line spent another line's quantity")
		}
	}
}
