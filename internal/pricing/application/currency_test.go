package application_test

import (
	"testing"

	"github.com/celikbros/kapsora/internal/pricing/application"
)

// TestMinorUnitsHasOneDocumentedDefault: the currency's scale is where the single rounding
// of a quote happens, so it is looked up in one place with a stated fallback rather than
// assumed at each call site.
func TestMinorUnitsHasOneDocumentedDefault(t *testing.T) {
	for _, code := range []string{"TRY", "try", " EUR ", "USD", "GBP", "CHF"} {
		if got := application.MinorUnits(code); got != 2 {
			t.Fatalf("MinorUnits(%q) = %d, want 2", code, got)
		}
	}
	// An unknown or empty code falls back rather than failing the quote: a currency
	// nobody configured is a configuration gap, not a reason to refuse a number.
	for _, code := range []string{"", "XXX", "NOK"} {
		if got := application.MinorUnits(code); got != application.DefaultMinorUnits {
			t.Fatalf("MinorUnits(%q) = %d, want the default %d",
				code, got, application.DefaultMinorUnits)
		}
	}
}
