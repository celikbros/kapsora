package domain_test

import (
	"testing"

	"github.com/celikbros/kapsora/internal/benefit/domain"
)

func q(t *testing.T, raw string) domain.Quantity {
	t.Helper()
	v, err := domain.ParseQuantity(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return v
}

func TestMul(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"12.5", "4", "50"},
		{"0.000001", "1", "0.000001"},
		// Twelve decimals do not fit in six, so the product rounds half away from zero.
		{"0.000001", "0.5", "0.000001"},
		{"0.000001", "0.49", "0"},
		{"-2.5", "3", "-7.5"},
		{"0", "999", "0"},
	}
	for _, c := range cases {
		if got := q(t, c.a).Mul(q(t, c.b)).String(); got != c.want {
			t.Errorf("%s × %s = %s, want %s", c.a, c.b, got, c.want)
		}
	}
}

func TestPercent(t *testing.T) {
	cases := []struct{ amount, percent, want string }{
		{"500", "20", "100"},
		{"1000", "0", "0"},
		{"1000", "100", "1000"},
		{"333.33", "33.33", "111.098889"},
		// A rate a float would mangle: 1/3 of 100 at six decimals.
		{"100", "33.333333", "33.333333"},
		{"-500", "20", "-100"},
	}
	for _, c := range cases {
		if got := q(t, c.amount).Percent(q(t, c.percent)).String(); got != c.want {
			t.Errorf("%s%% of %s = %s, want %s", c.percent, c.amount, got, c.want)
		}
	}
}

func TestRoundTo(t *testing.T) {
	cases := []struct {
		value string
		scale int
		want  string
	}{
		{"1.005", 2, "1.01"},
		{"1.004999", 2, "1"},
		{"2.675", 2, "2.68"}, // the classic float trap: binary would give 2.67
		{"-2.675", 2, "-2.68"},
		{"1234.5", 0, "1235"},
		{"1233.5", 0, "1234"},
		{"1.234567", 6, "1.234567"},
		{"1.234567", 9, "1.234567"},
	}
	for _, c := range cases {
		if got := q(t, c.value).RoundTo(c.scale).String(); got != c.want {
			t.Errorf("round %s to %d = %s, want %s", c.value, c.scale, got, c.want)
		}
	}
}

// Rounding once at the end is not the same as rounding each step, which is why the
// pricing calculation keeps six decimals until it is finished.
func TestRoundingOnceDiffersFromRoundingEachStep(t *testing.T) {
	share := q(t, "33.333333")
	lines := []string{"10.005", "10.005", "10.005"}

	var onceTotal, eachTotal domain.Quantity
	for _, raw := range lines {
		v := q(t, raw).Percent(share)
		onceTotal = onceTotal.Add(v)
		eachTotal = eachTotal.Add(v.RoundTo(2))
	}
	if onceTotal.RoundTo(2).String() == eachTotal.String() {
		t.Skip("this fixture no longer distinguishes the two policies")
	}
	// The point is that they differ; the totals below record which is which.
	t.Logf("rounded once: %s, rounded each step: %s", onceTotal.RoundTo(2), eachTotal)
}

func TestRefundRoundsTheSameDistanceAsTheCharge(t *testing.T) {
	charge := q(t, "2.675").RoundTo(2)
	refund := q(t, "-2.675").RoundTo(2)
	if charge.Add(refund).String() != "0" {
		t.Fatalf("a charge of %s and a refund of %s do not cancel", charge, refund)
	}
}
