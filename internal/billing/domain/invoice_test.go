package domain_test

import (
	"errors"
	"testing"
	"time"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/billing/domain"
)

// The invoice's own rules, with no database anywhere near them.
//
// Two of the tests below are the ones that matter, and both of them are about arithmetic
// nobody may round: the header adds up exactly, and the tolerance is applied to the magnitude
// of a difference rather than to its sign.

var invoiceDay = time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)

func header(lines, tax, payable string) domain.Header {
	return domain.Header{
		InvoiceNumber: "KPS2026000041", InvoiceDate: invoiceDay,
		LineExtensionAmount: lines, TaxAmount: tax, PayableAmount: payable,
	}
}

// TestHeaderSumIsExact is the first half of the package's arithmetic. The figures are chosen so
// that a float would get them wrong: 1000.10 + 200.02 is 1200.12 in exact decimals and
// 1200.1200000000001 in binary floating point, and a comparison that had gone through a float
// would reject a header a provider typed correctly.
func TestHeaderSumIsExact(t *testing.T) {
	if _, err := domain.ValidateHeader(header("1000.10", "200.02", "1200.12")); err != nil {
		t.Fatalf("an exact sum was refused: %v", err)
	}
	// One kuruş out is out. There is no tolerance on the header: the tolerance of this
	// package is about the *allocations*, and a document whose own two halves do not make its
	// total is a document somebody mistyped.
	_, err := domain.ValidateHeader(header("1000.10", "200.02", "1200.13"))
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a header a kuruş out was accepted: %v", err)
	}
	var ve *domain.ValidationError
	if !errors.As(err, &ve) || len(ve.Fields) != 1 || ve.Fields[0].Field != "payableAmount" {
		t.Fatalf("the sum error should name payableAmount; got %#v", err)
	}
	if ve.Fields[0].Code != "SUM" {
		t.Errorf("code = %s, want SUM", ve.Fields[0].Code)
	}
}

// TestHeaderCanonicalisesAmounts proves two tenants who typed the same money differently store
// the same string. Without it, "1000" and "1000.000000" would be two figures a reconciliation
// has to know are one.
func TestHeaderCanonicalisesAmounts(t *testing.T) {
	out, err := domain.ValidateHeader(header("1000.000000", "0", "1000"))
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if out.LineExtensionAmount != "1000" || out.TaxAmount != "0" || out.PayableAmount != "1000" {
		t.Errorf("amounts = %s/%s/%s, want the canonical 1000/0/1000",
			out.LineExtensionAmount, out.TaxAmount, out.PayableAmount)
	}
}

// TestHeaderDefaultsAndBounds walks the fields a caller may leave out and the ones it may get
// wrong. They are asserted together because each of them is one line of the same function and
// a table is how a reader checks that none of them was forgotten.
func TestHeaderDefaultsAndBounds(t *testing.T) {
	out, err := domain.ValidateHeader(header("100", "0", "100"))
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if out.CurrencyCode != domain.DefaultCurrency || out.DomainCode != domain.DefaultDomainCode {
		t.Errorf("defaults = %s/%s, want TRY/GENERIC", out.CurrencyCode, out.DomainCode)
	}

	for name, mutate := range map[string]func(h *domain.Header){
		"a number with a space in it": func(h *domain.Header) { h.InvoiceNumber = "KPS 2026" },
		"an empty number":             func(h *domain.Header) { h.InvoiceNumber = "" },
		"a two-letter currency":       func(h *domain.Header) { h.CurrencyCode = "TR" },
		"an unknown domain":           func(h *domain.Header) { h.DomainCode = "SPACE_TRAVEL" },
		"a negative line total":       func(h *domain.Header) { h.LineExtensionAmount = "-1" },
		"a rate above a hundred": func(h *domain.Header) {
			rate := "101"
			h.VatRate = &rate
		},
		"a date in the far future": func(h *domain.Header) {
			h.InvoiceDate = time.Date(2999, 1, 1, 0, 0, 0, 0, time.UTC)
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := header("100", "0", "100")
			mutate(&h)
			if _, err := domain.ValidateHeader(h); !errors.Is(err, domain.ErrValidation) {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
}

// TestFiscalYearIsTheDatesYear is half of the uniqueness rule of 11.12. The database CHECKs
// that the stored value is this one, so the two have to agree.
func TestFiscalYearIsTheDatesYear(t *testing.T) {
	if got := domain.FiscalYear(invoiceDay); got != 2026 {
		t.Errorf("fiscal year = %d, want 2026", got)
	}
	// The last moment of a year is still that year. A provider dating an invoice 31 December
	// and a platform that had rounded to the next year would have put it in the wrong
	// numbering sequence.
	last := time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)
	if got := domain.FiscalYear(last); got != 2026 {
		t.Errorf("fiscal year of 31 December = %d, want 2026", got)
	}
}

// TestWithinToleranceIsAboutMagnitude is the second half of the package's arithmetic.
//
// An invoice that allocates a lira too much is exactly as wrong as one that allocates a lira
// too little, so the tolerance is applied to the magnitude. The difference itself stays signed,
// because a screen has to be able to say "short" rather than "off by".
func TestWithinToleranceIsAboutMagnitude(t *testing.T) {
	q := benefitdomain.MustQuantity
	tolerance := q("0.01")

	for _, tc := range []struct {
		name           string
		payable        string
		allocated      string
		wantOK         bool
		wantDifference string
	}{
		{"exactly equal", "1340.00", "1340", true, "0"},
		{"a kuruş short, inside", "1340.00", "1339.99", true, "0.01"},
		{"a kuruş over, inside", "1340.00", "1340.01", true, "-0.01"},
		{"two kuruş short, outside", "1340.00", "1339.98", false, "0.02"},
		{"two kuruş over, outside", "1340.00", "1340.02", false, "-0.02"},
		{"wildly short", "1340.00", "0", false, "1340"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ok, difference := domain.WithinTolerance(q(tc.payable), q(tc.allocated), tolerance)
			if ok != tc.wantOK {
				t.Errorf("within = %t, want %t", ok, tc.wantOK)
			}
			if difference.String() != tc.wantDifference {
				t.Errorf("difference = %s, want %s", difference.String(), tc.wantDifference)
			}
		})
	}

	// A tolerance of zero is a tenant that has said "exactly". It has to actually mean it.
	if ok, _ := domain.WithinTolerance(q("100"), q("99.99"), q("0")); ok {
		t.Error("a zero tolerance accepted a kuruş of difference")
	}
}

// TestLifecycleGates is the freeze, spelled as a table. Every command in the package asks one
// of these four questions, and a status quietly joining one of the lists would be a submitted
// invoice somebody could edit.
func TestLifecycleGates(t *testing.T) {
	for _, status := range domain.Statuses {
		draft := status == domain.StatusDraft
		if domain.CanEdit(status) != draft {
			t.Errorf("CanEdit(%s) = %t, want %t", status, !draft, draft)
		}
		if domain.CanAllocate(status) != draft {
			t.Errorf("CanAllocate(%s) = %t, want %t", status, !draft, draft)
		}
		if domain.CanSubmit(status) != draft {
			t.Errorf("CanSubmit(%s) = %t, want %t", status, !draft, draft)
		}
		wantCancel := status == domain.StatusDraft || status == domain.StatusReturned
		if domain.CanCancel(status) != wantCancel {
			t.Errorf("CanCancel(%s) = %t, want %t", status, !wantCancel, wantCancel)
		}
		wantSupersede := status == domain.StatusReturned || status == domain.StatusRejected
		if domain.CanSupersede(status) != wantSupersede {
			t.Errorf("CanSupersede(%s) = %t, want %t", status, !wantSupersede, wantSupersede)
		}
	}
	// The one that would be easiest to get wrong by accident: a submitted invoice is in front
	// of a reviewer, and sliding a correction underneath it is not the answer to having sent
	// the wrong figures.
	if domain.CanSupersede(domain.StatusSubmitted) {
		t.Error("a SUBMITTED invoice may not be superseded")
	}
}

// TestClaimIsInvoiceable pins the pair the deferred trigger enforces. INVOICED in particular
// has to be absent: it is the status this package's own submit puts a claim into, and a claim
// that could be allocated from it would be a claim on two invoices.
func TestClaimIsInvoiceable(t *testing.T) {
	for status, want := range map[string]bool{
		"APPROVED": true, "PARTIALLY_APPROVED": true,
		"DRAFT": false, "SUBMITTED": false, "REJECTED": false, "CANCELLED": false,
		"INVOICED": false, "BATCHED": false, "SETTLED": false,
	} {
		if got := domain.ClaimIsInvoiceable(status); got != want {
			t.Errorf("ClaimIsInvoiceable(%s) = %t, want %t", status, got, want)
		}
	}
}
