package application_test

import (
	"context"
	"testing"

	"github.com/celikbros/kapsora/internal/billing/application"
)

// TestSponsorHRReadsTheInvoiceWithNoLineDescription is section 3's sixth requirement, and the
// acceptance criterion of §2.3.
//
// The line description of a health claim may carry clinical text — v1.2 §2.11 says so, and
// "sol diz artroskopi sonrası kontrol" is a diagnosis in a sentence. The sponsor's HR user
// reads the invoice's header, its totals and which of its members' claims it covers, and never
// the words the provider typed on a line.
//
// The test asserts both halves, because only one of them is a test. That the field is absent
// for HR proves nothing on its own — a field nobody ever populates is absent for everybody —
// so the same invoice is read by a caller who has earned the clinical projection and the
// description has to be there.
func TestSponsorHRReadsTheInvoiceWithNoLineDescription(t *testing.T) {
	f := newFixture(t)
	description := "Sol diz artroskopi sonrası kontrol"
	claim := f.claimFor(t, f.provider, "CLM-20260301-HR000001", "APPROVED", "750", &description)

	view := f.createInvoice(t, f.financialRC(), "HR1", "750")
	view = f.allocate(t, f.financialRC(), view, claim, "750")

	clinical, err := f.invoices.GetInvoice(context.Background(), f.clinicalRC(), view.Invoice.ID)
	if err != nil {
		t.Fatalf("clinical read: %v", err)
	}
	if clinical.Projection != application.ProjectionClinical {
		t.Fatalf("a caller holding health.clinical.read was served %s", clinical.Projection)
	}
	if len(clinical.Allocations) != 1 || clinical.Allocations[0].ClaimDescription == nil ||
		*clinical.Allocations[0].ClaimDescription != description {
		t.Fatalf("the clinical projection does not carry the line description; "+
			"the test below would pass for the wrong reason. got %#v", clinical.Allocations)
	}

	hr, err := f.invoices.GetInvoice(context.Background(), f.sponsorHRRC(), view.Invoice.ID)
	if err != nil {
		t.Fatalf("HR read: %v", err)
	}
	if hr.Projection != application.ProjectionFinancial {
		t.Fatalf("the sponsor's HR user was served %s", hr.Projection)
	}
	if len(hr.Allocations) != 1 {
		t.Fatalf("HR reads %d allocations, want the one the invoice covers", len(hr.Allocations))
	}
	if hr.Allocations[0].ClaimDescription != nil {
		t.Errorf("the sponsor's HR user was given the line description %q",
			*hr.Allocations[0].ClaimDescription)
	}

	// Everything a bill *is* survives the projection. An HR user who could not see the money
	// could do nothing with the record the projection exists to let them have.
	got := hr.Allocations[0]
	if got.ClaimReference == "" || got.AllocatedAmount != "750" || got.ApprovedTotal != "750" ||
		got.CurrencyCode != "TRY" || got.ClaimVersionNo != 1 {
		t.Errorf("the financial projection dropped something that is money: %#v", got)
	}
	if hr.Invoice.PayableAmount != "750" || hr.AllocationTotal != "750" ||
		hr.AllocationDifference != "0" {
		t.Errorf("the header totals do not survive the projection: %s/%s/%s",
			hr.Invoice.PayableAmount, hr.AllocationTotal, hr.AllocationDifference)
	}

	// The payer's financial reviewer is in the same position as HR here: it holds no clinical
	// grant either, and this is deliberate — a financial reviewer reconciling a bill has no
	// reason to read what a surgeon wrote about a knee.
	finance, err := f.invoices.GetInvoice(context.Background(), f.financialRC(), view.Invoice.ID)
	if err != nil {
		t.Fatalf("finance read: %v", err)
	}
	if finance.Allocations[0].ClaimDescription != nil {
		t.Error("the payer's financial reviewer was given the line description")
	}
}

// TestListRowsCarryTheAllocationTotalSummedInSQL pins the two implementations of one figure
// against each other. The list sums the allocations in SQL because a page of fifty invoices
// would otherwise be fifty extra reads; the detail and the submit gate sum them in Go. Two sums
// are two answers unless something makes them agree, and this is that something.
func TestListRowsCarryTheAllocationTotalSummedInSQL(t *testing.T) {
	f := newFixture(t)
	rc := f.financialRC()
	first := f.approvedClaim(t, "CLM-20260301-SUM00001", "449.99")
	second := f.approvedClaim(t, "CLM-20260301-SUM00002", "250.01")
	view := f.createInvoice(t, rc, "SUM1", "700")
	view, err := f.invoices.PutAllocations(context.Background(), rc, view.Invoice.ID,
		[]application.AllocationInput{
			{ClaimID: first, AllocatedAmount: "449.99"},
			{ClaimID: second, AllocatedAmount: "250.01"},
		}, view.Invoice.RowVersion)
	if err != nil {
		t.Fatalf("put allocations: %v", err)
	}
	if view.AllocationTotal != "700" {
		t.Fatalf("the detail sums %s, want 700", view.AllocationTotal)
	}

	page, err := f.invoices.ListInvoices(context.Background(), rc, application.InvoiceFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	for _, row := range page.Items {
		if row.ID != view.Invoice.ID {
			continue
		}
		found = true
		if row.AllocationTotal != view.AllocationTotal {
			t.Errorf("the list sums %s and the detail sums %s", row.AllocationTotal,
				view.AllocationTotal)
		}
		if row.AllocationCount != 2 {
			t.Errorf("allocation count = %d, want 2", row.AllocationCount)
		}
	}
	if !found {
		t.Fatal("the invoice is not on its own list")
	}
}

// TestProviderScopeBoundsTheList is the boundary a provider-scoped caller lives inside. It is
// applied in SQL rather than after the read, so an invoice outside it is genuinely not returned
// and 404 is honest rather than a filtered 200.
func TestProviderScopeBoundsTheList(t *testing.T) {
	f := newFixture(t)
	mine := f.createInvoice(t, f.financialRC(), "SCOPE1", "100")

	// The same tenant's other provider, raised by the payer's tenant-wide finance user.
	other := f.header("SCOPE2", "100")
	other.ProviderOrganizationID = f.other
	// It has no tax identity, so give it one: the point of this test is the scope, not the VKN.
	f.h.AdminExec(`
		UPDATE directory.organization o
		   SET tax_number_cipher = decode('00', 'hex'),
		       tax_number_hash   = sha256('9876543210'::bytea)
		  FROM directory.tenant_organization t
		 WHERE t.tenant_id = $1 AND t.id = $2 AND o.id = t.organization_id`,
		f.tenant, f.other)
	theirs, err := f.invoices.CreateInvoice(context.Background(), f.financialRC(), other)
	if err != nil {
		t.Fatalf("create the other provider's invoice: %v", err)
	}

	page, err := f.invoices.ListInvoices(context.Background(), f.providerRC(),
		application.InvoiceFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, row := range page.Items {
		if row.ID == theirs.Invoice.ID {
			t.Fatal("a provider-scoped caller was shown another provider's invoice")
		}
	}
	if len(page.Items) != 1 || page.Items[0].ID != mine.Invoice.ID {
		t.Fatalf("the provider sees %d invoices, want only its own", len(page.Items))
	}

	// And asking for it by id is a 404, not a 403: that an invoice exists at all is somebody
	// else's business.
	if _, err := f.invoices.GetInvoice(context.Background(), f.providerRC(),
		theirs.Invoice.ID); err == nil {
		t.Fatal("a provider-scoped caller read another provider's invoice")
	}
}
