package domain_test

import (
	"errors"
	"testing"

	"github.com/celikbros/kapsora/internal/contract/domain"
)

// nightsTerms is the smallest set of terms that is valid, so every case below differs from
// a passing one in exactly the field it is testing.
func nightsTerms() domain.LodgingTermsInput {
	nights := 1
	return domain.LodgingTermsInput{
		FreeCancellationHoursBefore: 48,
		PenaltyKind:                 domain.PenaltyNightsKind,
		PenaltyNights:               &nights,
		NoShowPercent:               "100",
		MinNights:                   1,
	}
}

func percentTerms() domain.LodgingTermsInput {
	in := nightsTerms()
	in.PenaltyKind = domain.PenaltyPercentKind
	in.PenaltyNights = nil
	in.PenaltyPercent = "42.5"
	return in
}

func intPtr(n int) *int { return &n }

// fieldsOf pulls the field codes out of a refusal, so a test can say which field was
// refused rather than only that something was.
func fieldsOf(t *testing.T, err error) map[string]string {
	t.Helper()
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("not a validation error: %v", err)
	}
	out := map[string]string{}
	for _, f := range ve.Fields {
		out[f.Field] = f.Code
	}
	return out
}

func TestValidateLodgingTermsAcceptsBothPenaltyKinds(t *testing.T) {
	for name, in := range map[string]domain.LodgingTermsInput{
		"nights":  nightsTerms(),
		"percent": percentTerms(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := domain.ValidateLodgingTerms(in); err != nil {
				t.Fatalf("valid terms refused: %v", err)
			}
		})
	}

	// The whole optional half, filled in.
	full := percentTerms()
	full.FreeCancellationHoursBefore = 0
	full.HoldMinutes = intPtr(30)
	full.MinNights = 2
	full.MaxNights = intPtr(14)
	full.ChildFreeUnderAge = intPtr(6)
	full.NoShowPercent = "0"
	if err := domain.ValidateLodgingTerms(full); err != nil {
		t.Fatalf("fully specified terms refused: %v", err)
	}
}

// TestValidateLodgingTermsRefusesTwoPenaltiesOrNone is the rule the column CHECK repeats:
// the kind names one number and the other must be absent. Both halves are asserted,
// because a validator that only checked "at least one" would let a row through that says
// PERCENT and carries nights, and the fee would then depend on which column the reader
// happened to look at.
func TestValidateLodgingTermsRefusesTwoPenaltiesOrNone(t *testing.T) {
	both := nightsTerms()
	both.PenaltyPercent = "10"
	if got := fieldsOf(t, domain.ValidateLodgingTerms(both)); got["penaltyPercent"] != "FORBIDDEN" {
		t.Fatalf("NIGHTS with a percentage: %+v", got)
	}

	bothOther := percentTerms()
	bothOther.PenaltyNights = intPtr(2)
	if got := fieldsOf(t, domain.ValidateLodgingTerms(bothOther)); got["penaltyNights"] != "FORBIDDEN" {
		t.Fatalf("PERCENT with nights: %+v", got)
	}

	noneNights := nightsTerms()
	noneNights.PenaltyNights = nil
	if got := fieldsOf(t, domain.ValidateLodgingTerms(noneNights)); got["penaltyNights"] != "REQUIRED" {
		t.Fatalf("NIGHTS with no nights: %+v", got)
	}

	nonePercent := percentTerms()
	nonePercent.PenaltyPercent = ""
	if got := fieldsOf(t, domain.ValidateLodgingTerms(nonePercent)); got["penaltyPercent"] != "REQUIRED" {
		t.Fatalf("PERCENT with no percentage: %+v", got)
	}

	unknown := nightsTerms()
	unknown.PenaltyKind = "FIXED"
	if got := fieldsOf(t, domain.ValidateLodgingTerms(unknown)); got["penaltyKind"] != "ENUM" {
		t.Fatalf("an invented penalty kind: %+v", got)
	}
}

func TestValidateLodgingTermsBoundsEveryNumber(t *testing.T) {
	cases := []struct {
		name   string
		field  string
		code   string
		mutate func(*domain.LodgingTermsInput)
	}{
		{"a free window longer than a year", "freeCancellationHoursBefore", "RANGE",
			func(in *domain.LodgingTermsInput) { in.FreeCancellationHoursBefore = 8761 }},
		{"a negative free window", "freeCancellationHoursBefore", "RANGE",
			func(in *domain.LodgingTermsInput) { in.FreeCancellationHoursBefore = -1 }},
		{"more penalty nights than a year", "penaltyNights", "RANGE",
			func(in *domain.LodgingTermsInput) { in.PenaltyNights = intPtr(366) }},
		{"a no-show over a hundred percent", "noShowPercent", "RANGE",
			func(in *domain.LodgingTermsInput) { in.NoShowPercent = "100.01" }},
		{"a hold of no minutes", "holdMinutes", "RANGE",
			func(in *domain.LodgingTermsInput) { in.HoldMinutes = intPtr(0) }},
		{"a hold longer than a day", "holdMinutes", "RANGE",
			func(in *domain.LodgingTermsInput) { in.HoldMinutes = intPtr(1441) }},
		{"a stay of no nights", "minNights", "RANGE",
			func(in *domain.LodgingTermsInput) { in.MinNights = 0 }},
		{"a maximum below the minimum", "maxNights", "RANGE",
			func(in *domain.LodgingTermsInput) { in.MinNights = 5; in.MaxNights = intPtr(4) }},
		{"a child older than eighteen", "childFreeUnderAge", "RANGE",
			func(in *domain.LodgingTermsInput) { in.ChildFreeUnderAge = intPtr(19) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := nightsTerms()
			tc.mutate(&in)
			got := fieldsOf(t, domain.ValidateLodgingTerms(in))
			if got[tc.field] != tc.code {
				t.Fatalf("%s: fields = %+v, want %s=%s", tc.name, got, tc.field, tc.code)
			}
		})
	}
}

// TestValidateLodgingTermsKeepsPercentagesExact is the "money is never a float" rule at
// this boundary. numeric(7,4) keeps four decimals; a fifth would be silently rounded on
// the way into the column, and a fee the contract desk never typed is a fee nobody agreed
// to. A missing no-show percentage is refused outright, because "not stated" would leave
// the one number a no-show is charged by up to whoever reads the row.
func TestValidateLodgingTermsKeepsPercentagesExact(t *testing.T) {
	tooPrecise := percentTerms()
	tooPrecise.PenaltyPercent = "42.56789"
	if got := fieldsOf(t, domain.ValidateLodgingTerms(tooPrecise)); got["penaltyPercent"] != "SCALE" {
		t.Fatalf("five decimals accepted: %+v", got)
	}
	okPrecise := percentTerms()
	okPrecise.PenaltyPercent = "42.5678"
	if err := domain.ValidateLodgingTerms(okPrecise); err != nil {
		t.Fatalf("four decimals refused: %v", err)
	}

	notANumber := percentTerms()
	notANumber.NoShowPercent = "yüzde yüz"
	if got := fieldsOf(t, domain.ValidateLodgingTerms(notANumber)); got["noShowPercent"] != "FORMAT" {
		t.Fatalf("a word accepted as a percentage: %+v", got)
	}

	missing := percentTerms()
	missing.NoShowPercent = ""
	if got := fieldsOf(t, domain.ValidateLodgingTerms(missing)); got["noShowPercent"] != "REQUIRED" {
		t.Fatalf("a missing no-show percentage: %+v", got)
	}

	negative := percentTerms()
	negative.NoShowPercent = "-1"
	if got := fieldsOf(t, domain.ValidateLodgingTerms(negative)); got["noShowPercent"] == "" {
		t.Fatalf("a negative no-show percentage accepted: %+v", got)
	}
}
