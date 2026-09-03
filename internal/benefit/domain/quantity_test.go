package domain_test

import (
	"errors"
	"testing"

	"github.com/celikbros/kapsora/internal/benefit/domain"
)

func TestQuantityParsingAndRendering(t *testing.T) {
	cases := []struct{ in, want string }{
		{"0", "0"},
		{"0.000000", "0"},
		{"-0.000000", "0"},
		{"100", "100"},
		{"100.000000", "100"},     // the padded form PostgreSQL renders
		{"1500.500000", "1500.5"}, // trailing zeros dropped again
		{"0.000001", "0.000001"},  // the smallest representable step
		{"-2.250000", "-2.25"},
		{"00012.3400", "12.34"},
		{"99999999999999.999999", "99999999999999.999999"},
	}
	for _, c := range cases {
		q, err := domain.ParseQuantity(c.in)
		if err != nil {
			t.Fatalf("ParseQuantity(%q): %v", c.in, err)
		}
		if got := q.String(); got != c.want {
			t.Fatalf("ParseQuantity(%q).String() = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestQuantityRejectsValuesOutsideTheColumn(t *testing.T) {
	for _, bad := range []string{"", " ", "abc", "1.2.3", "1e6", "0.0000001", "+1"} {
		if _, err := domain.ParseQuantity(bad); err == nil {
			t.Fatalf("ParseQuantity(%q) accepted", bad)
		}
	}
	// numeric(20,6) holds at most 20 significant digits.
	if _, err := domain.ParseQuantity("100000000000000.000000"); !errors.Is(err, domain.ErrQuantityRange) {
		t.Fatalf("ParseQuantity(10^14) error = %v, want ErrQuantityRange", err)
	}
}

func TestQuantityArithmeticIsExact(t *testing.T) {
	// The classic float trap: 0.1 + 0.2 must be exactly 0.3, and repeated subtraction of
	// a sixth-decimal step must land on zero, not on a residue.
	a, b := domain.MustQuantity("0.1"), domain.MustQuantity("0.2")
	if got := a.Add(b).String(); got != "0.3" {
		t.Fatalf("0.1 + 0.2 = %s, want 0.3", got)
	}
	balance := domain.MustQuantity("1")
	step := domain.MustQuantity("0.000001")
	for i := 0; i < 1_000_000; i++ {
		balance = balance.Sub(step)
	}
	if !balance.IsZero() {
		t.Fatalf("1 - 10^6 * 0.000001 = %s, want 0", balance)
	}
	if got := domain.MustQuantity("5").Sub(domain.MustQuantity("7")).String(); got != "-2" {
		t.Fatalf("5 - 7 = %s, want -2", got)
	}
	if got := domain.MustQuantity("2.5").Neg().String(); got != "-2.5" {
		t.Fatalf("Neg(2.5) = %s, want -2.5", got)
	}
}

func TestQuantityComparison(t *testing.T) {
	small, large := domain.MustQuantity("1.000001"), domain.MustQuantity("1.000002")
	if small.Cmp(large) >= 0 || large.Cmp(small) <= 0 || small.Cmp(small) != 0 {
		t.Fatalf("Cmp does not order the two values")
	}
	if small.Min(large).Cmp(small) != 0 || large.Min(small).Cmp(small) != 0 {
		t.Fatalf("Min returned the larger value")
	}
	var zero domain.Quantity // the zero value must behave as 0 without a parse
	if !zero.IsZero() || zero.Sign() != 0 || zero.String() != "0" {
		t.Fatalf("zero Quantity = %q", zero.String())
	}
	if got := zero.Add(domain.MustQuantity("3")).String(); got != "3" {
		t.Fatalf("0 + 3 = %s", got)
	}
	if !domain.MustQuantity("-1").IsNegative() || !domain.MustQuantity("1").IsPositive() {
		t.Fatalf("sign predicates disagree with Sign")
	}
}

func TestLikePatternEscapesUserWildcards(t *testing.T) {
	if got := domain.LikePattern("  "); got != "" {
		t.Fatalf("LikePattern(blank) = %q, want empty", got)
	}
	if got := domain.LikePattern(" ACME "); got != "%ACME%" {
		t.Fatalf("LikePattern = %q", got)
	}
	// A user typing wildcards searches for those characters, not for everything.
	if got := domain.LikePattern(`100%_a\b`); got != `%100\%\_a\\b%` {
		t.Fatalf("LikePattern = %q", got)
	}
}
