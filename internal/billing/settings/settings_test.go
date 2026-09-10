package settings_test

import (
	"testing"

	"github.com/celikbros/kapsora/internal/billing/settings"
)

// The two tenant settings the submit gate reads, with no database.
//
// The point of the file is the fallback: a tenant that configured nothing gets the documented
// answer, and a tenant that configured nonsense gets the documented answer too. A setting
// nobody can interpret is a setting nobody set — and the alternative, honouring it, would mean
// an invoice gate that silently stopped checking anything.

func TestDefaultsWhenTheTenantHasSetNothing(t *testing.T) {
	got := settings.FromMap(map[string]string{})
	if got.AllocationTolerance != settings.DefaultAllocationTolerance {
		t.Errorf("tolerance = %s, want %s", got.AllocationTolerance,
			settings.DefaultAllocationTolerance)
	}
	if !got.InvoiceRequiresImage {
		t.Error("a tenant that has configured nothing should still require an invoice image")
	}
}

func TestTenantValuesWin(t *testing.T) {
	got := settings.FromMap(map[string]string{
		settings.KeyAllocationTolerance:  "0.50",
		settings.KeyInvoiceRequiresImage: "false",
	})
	if got.AllocationTolerance != "0.5" {
		t.Errorf("tolerance = %s, want the canonical 0.5", got.AllocationTolerance)
	}
	if got.InvoiceRequiresImage {
		t.Error("a tenant that turned the image off should not be asked for one")
	}
}

// TestZeroToleranceIsHonoured is the one value that must not be treated as "unset". A tenant
// that typed zero has said "exactly", and falling back to a kuruş would let through the one
// invoice they configured the setting to catch.
func TestZeroToleranceIsHonoured(t *testing.T) {
	if got := settings.FromMap(map[string]string{
		settings.KeyAllocationTolerance: "0",
	}); got.AllocationTolerance != "0" {
		t.Errorf("tolerance = %s, want 0", got.AllocationTolerance)
	}
}

// TestNonsenseFallsBackToTheDefault walks every way a value can be unusable. Each of them is
// one line of `FromMap`, and a table is how a reader checks that none was forgotten.
func TestNonsenseFallsBackToTheDefault(t *testing.T) {
	for name, raw := range map[string]string{
		"not a number":  "çok",
		"negative":      "-1",
		"absurdly big":  "100000",
		"empty":         "",
		"a JSON object": `{"amount":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			got := settings.FromMap(map[string]string{settings.KeyAllocationTolerance: raw})
			if got.AllocationTolerance != settings.DefaultAllocationTolerance {
				t.Errorf("tolerance = %s, want the default %s", got.AllocationTolerance,
					settings.DefaultAllocationTolerance)
			}
		})
	}
	for name, raw := range map[string]string{
		"not a boolean": "belki",
		"empty":         "",
		"a number":      "2",
	} {
		t.Run("image/"+name, func(t *testing.T) {
			got := settings.FromMap(map[string]string{settings.KeyInvoiceRequiresImage: raw})
			if !got.InvoiceRequiresImage {
				t.Error("an uninterpretable flag should leave the safe default in place")
			}
		})
	}
}

// TestKeysAreTheOnesTheLoaderAsksFor keeps the exported list and the constants in step: a key
// added to the package and forgotten in `Keys` would be a setting a tenant could configure and
// the loader would never read.
func TestKeysAreTheOnesTheLoaderAsksFor(t *testing.T) {
	want := map[string]bool{
		settings.KeyAllocationTolerance:    true,
		settings.KeyInvoiceRequiresImage:   true,
		settings.KeyBatchMinInvoices:       true,
		settings.KeyBatchMaxInvoices:       true,
		settings.KeyBatchDecisionThreshold: true,
		// WP-I7-04's two.
		settings.KeySettlementCheckerThreshold: true,
		settings.KeyReimbursementWindowDays:    true,
	}
	if len(settings.Keys) != len(want) {
		t.Fatalf("Keys has %d entries, want %d", len(settings.Keys), len(want))
	}
	for _, key := range settings.Keys {
		if !want[key] {
			t.Errorf("Keys carries an unknown key %q", key)
		}
	}
}

// TestBatchSettingsFallBackToTheDocumentedDefaults is the icmal's half of the same rule the
// tolerance follows: a value nobody can interpret is a value nobody set (WP-I7-03 section 2.1).
func TestBatchSettingsFallBackToTheDocumentedDefaults(t *testing.T) {
	defaults := settings.Defaults()
	cases := []struct {
		name string
		raw  map[string]string
		want settings.Values
	}{
		{
			name: "a tenant that configured nothing",
			raw:  nil,
			want: defaults,
		},
		{
			name: "the tenant's own bounds and threshold",
			raw: map[string]string{
				settings.KeyBatchMinInvoices:       "5",
				settings.KeyBatchMaxInvoices:       "50",
				settings.KeyBatchDecisionThreshold: "250000.00",
			},
			want: settings.Values{
				AllocationTolerance:  defaults.AllocationTolerance,
				InvoiceRequiresImage: defaults.InvoiceRequiresImage,
				BatchMinInvoices:     5, BatchMaxInvoices: 50,
				// Canonicalised, so two tenants who typed "250000" and "250000.00" compare
				// the same way against an exact decimal total.
				BatchDecisionThreshold: "250000",
			},
		},
		{
			name: "a minimum above the maximum is a batch nobody could ever submit",
			raw: map[string]string{
				settings.KeyBatchMinInvoices: "40",
				settings.KeyBatchMaxInvoices: "10",
			},
			want: defaults,
		},
		{
			name: "nonsense, a zero and a negative threshold",
			raw: map[string]string{
				settings.KeyBatchMinInvoices:       "yarım",
				settings.KeyBatchMaxInvoices:       "0",
				settings.KeyBatchDecisionThreshold: "-1",
			},
			want: defaults,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := settings.FromMap(tc.raw)
			if got.BatchMinInvoices != tc.want.BatchMinInvoices ||
				got.BatchMaxInvoices != tc.want.BatchMaxInvoices ||
				got.BatchDecisionThreshold != tc.want.BatchDecisionThreshold {
				t.Fatalf("got min %d max %d threshold %s, want %d/%d/%s",
					got.BatchMinInvoices, got.BatchMaxInvoices, got.BatchDecisionThreshold,
					tc.want.BatchMinInvoices, tc.want.BatchMaxInvoices,
					tc.want.BatchDecisionThreshold)
			}
		})
	}
}
