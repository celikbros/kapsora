package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/celikbros/kapsora/internal/billing/application"
	"github.com/celikbros/kapsora/internal/billing/domain"
	"github.com/celikbros/kapsora/internal/billing/settings"
	claimapp "github.com/celikbros/kapsora/internal/claim/application"
	"github.com/celikbros/kapsora/internal/identity"
)

// TestCreateRefusesAProviderWithNoTaxIdentity is the first gate of the create command.
//
// The refusal is a boolean about a missing number and never the number itself: the VKN is
// envelope-encrypted and its blind index is a hash, and neither belongs in a response, a log or
// an audit detail.
func TestCreateRefusesAProviderWithNoTaxIdentity(t *testing.T) {
	f := newFixture(t)
	rc := f.financialRC()

	in := f.header("NOTAX1", "100")
	in.ProviderOrganizationID = f.other // seeded without a tax number
	_, err := f.invoices.CreateInvoice(context.Background(), rc, in)
	if !errors.Is(err, application.ErrProviderTaxIDMissing) {
		t.Fatalf("create against a provider with no VKN = %v, want ErrProviderTaxIDMissing", err)
	}

	// And a kurum that is not a provider of this tenant at all is a different refusal, because
	// it is a different thing to fix.
	in.ProviderOrganizationID = f.sponsor
	_, err = f.invoices.CreateInvoice(context.Background(), rc, in)
	if !errors.Is(err, application.ErrProviderUnknown) {
		t.Fatalf("create against a sponsor = %v, want ErrProviderUnknown", err)
	}
}

// TestNumberIsTakenInsideTheFiscalYear is the uniqueness rule of 11.12 as the service reports
// it. The schema test proves the index; this proves the caller is told which rule refused them.
func TestNumberIsTakenInsideTheFiscalYear(t *testing.T) {
	f := newFixture(t)
	rc := f.financialRC()
	first := f.createInvoice(t, rc, "KPS2026000041", "100")

	_, err := f.invoices.CreateInvoice(context.Background(), rc, f.header("KPS2026000041", "200"))
	if !errors.Is(err, application.ErrNumberTaken) {
		t.Fatalf("a reused number = %v, want ErrNumberTaken", err)
	}

	// Withdrawing the first invoice frees the number, which is how a provider whose own books
	// already carry it puts it on the correction.
	if _, err := f.invoices.CancelInvoice(context.Background(), rc, first.Invoice.ID,
		first.Invoice.RowVersion); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := f.invoices.CreateInvoice(context.Background(), rc,
		f.header("KPS2026000041", "200")); err != nil {
		t.Errorf("a cancelled invoice did not free its number: %v", err)
	}
}

// TestOnlyTheCorrectionMayReuseAReturnedNumber is section 2.2's rule about numbering: a
// different number is the normal case, and the same number is allowed only for the superseded
// chain.
//
// The unique index cannot say this on its own — "who may reuse it" is a fact about another row
// — so the service answers it, and the index underneath still refuses two live documents
// sharing one however they were written.
//
// **Two returned invoices, not one.** With a single returned invoice in the world, "the
// correction may reuse the number" and "any correction may reuse any returned number" are the
// same sentence, and a check that only asked whether the caller was correcting *something*
// would pass. X and Y below are what tells the two apart: a correction of X carrying Y's number
// is refused, and it is refused for the reason a provider would care about — Y's number is Y's.
func TestOnlyTheCorrectionMayReuseAReturnedNumber(t *testing.T) {
	f := newFixture(t)
	rc := f.financialRC()

	x := f.returnedInvoice(t, rc, "KPS2026000077", "CLM-20260301-NUM00001", "500")
	y := f.returnedInvoice(t, rc, "KPS2026000088", "CLM-20260301-NUM00002", "400")

	// An unrelated invoice may not pick a returned number up, even though the index would now
	// allow the row: the returned invoice is still the document that number belongs to.
	_, err := f.invoices.CreateInvoice(context.Background(), rc, f.header("KPS2026000077", "400"))
	if !errors.Is(err, application.ErrNumberTaken) {
		t.Fatalf("an unrelated invoice reused a returned number: %v", err)
	}

	// **And neither may a correction of somebody else's document.** Correcting X does not
	// entitle this invoice to Y's number; Y is still returned, and its number is still Y's.
	before := f.invoiceCount(t)
	wrong := f.header("KPS2026000088", "500")
	wrong.SupersedesInvoiceID = &x.Invoice.ID
	_, err = f.invoices.CreateInvoice(context.Background(), rc, wrong)
	if !errors.Is(err, application.ErrNumberTaken) {
		t.Fatalf("a correction of X took Y's number: %v", err)
	}
	// Nothing was written. A refused create that still left a draft behind would be a draft
	// nobody asked for, holding a number nobody could then use.
	if after := f.invoiceCount(t); after != before {
		t.Errorf("the refused create wrote %d row(s)", after-before)
	}
	if still, err := f.invoices.GetInvoice(context.Background(), rc, y.Invoice.ID); err != nil {
		t.Fatalf("read Y: %v", err)
	} else if still.Invoice.Status != domain.StatusReturned {
		t.Errorf("Y is %s after the refusal, want RETURNED", still.Invoice.Status)
	}

	// The correction of X may carry X's own number.
	in := f.header("KPS2026000077", "500")
	in.SupersedesInvoiceID = &x.Invoice.ID
	correction, err := f.invoices.CreateInvoice(context.Background(), rc, in)
	if err != nil {
		t.Fatalf("the correction could not carry the number it is correcting: %v", err)
	}
	if correction.Invoice.InvoiceNumber != "KPS2026000077" {
		t.Errorf("number = %s, want the one the provider's books carry",
			correction.Invoice.InvoiceNumber)
	}
	// And once it is submitted, X is cancelled and there is one live holder again.
	if _, err := f.invoices.SubmitInvoice(context.Background(), rc, correction.Invoice.ID,
		correction.Invoice.RowVersion); err != nil {
		t.Fatalf("submit the correction: %v", err)
	}
	old, err := f.invoices.GetInvoice(context.Background(), rc, x.Invoice.ID)
	if err != nil {
		t.Fatalf("read the superseded invoice: %v", err)
	}
	if old.Invoice.Status != domain.StatusCancelled {
		t.Errorf("the superseded invoice is %s, want CANCELLED", old.Invoice.Status)
	}
}

// TestAllocationRefusalsNameTheClaim walks the four ways an allocation is refused. Each of them
// is also a database rule; what is asserted here is that the caller is told *which claim* and
// *by how much*, because "constraint violated" is not something a billing clerk can act on.
func TestAllocationRefusalsNameTheClaim(t *testing.T) {
	f := newFixture(t)
	rc := f.financialRC()
	view := f.createInvoice(t, rc, "ALLOC1", "1000")
	approved := f.approvedClaim(t, "CLM-20260301-ALLOC001", "600")

	// Above the ceiling.
	_, err := f.invoices.PutAllocations(context.Background(), rc, view.Invoice.ID,
		[]application.AllocationInput{{ClaimID: approved, AllocatedAmount: "600.01"}},
		view.Invoice.RowVersion)
	var allocationErr *application.AllocationError
	if !errors.As(err, &allocationErr) ||
		allocationErr.Kind != application.AllocationExceedsApproved {
		t.Fatalf("an allocation above the approved total = %v, want ALLOCATION_EXCEEDS_APPROVED", err)
	}
	if allocationErr.ClaimID != approved || allocationErr.ClaimReference == "" {
		t.Errorf("the refusal names claim %v ref %q, want the claim it refused",
			allocationErr.ClaimID, allocationErr.ClaimReference)
	}
	if allocationErr.Allocated != "600.01" || allocationErr.Approved != "600" {
		t.Errorf("the refusal carries %s of %s, want both figures",
			allocationErr.Allocated, allocationErr.Approved)
	}

	// A claim nobody approved.
	undecided := f.claimFor(t, f.provider, "CLM-20260301-ALLOC002", "SUBMITTED", "100", nil)
	_, err = f.invoices.PutAllocations(context.Background(), rc, view.Invoice.ID,
		[]application.AllocationInput{{ClaimID: undecided, AllocatedAmount: "100"}},
		view.Invoice.RowVersion)
	if !errors.As(err, &allocationErr) ||
		allocationErr.Kind != application.AllocationClaimNotInvoiceable {
		t.Fatalf("an undecided claim = %v, want CLAIM_NOT_INVOICEABLE", err)
	}
	if allocationErr.ClaimStatus != "SUBMITTED" {
		t.Errorf("the refusal reports status %q, want SUBMITTED", allocationErr.ClaimStatus)
	}

	// A claim of another provider, which the caller may well be allowed to see and simply may
	// not bill on this document.
	foreign := f.claimFor(t, f.other, "CLM-20260301-ALLOC003", "APPROVED", "100", nil)
	_, err = f.invoices.PutAllocations(context.Background(), rc, view.Invoice.ID,
		[]application.AllocationInput{{ClaimID: foreign, AllocatedAmount: "100"}},
		view.Invoice.RowVersion)
	if !errors.As(err, &allocationErr) ||
		allocationErr.Kind != application.AllocationClaimNotInvoiceable {
		t.Fatalf("another provider's claim = %v, want CLAIM_NOT_INVOICEABLE", err)
	}

	// The same claim twice on one document: refused by name, because the ceiling would
	// otherwise be checked against half the figure.
	_, err = f.invoices.PutAllocations(context.Background(), rc, view.Invoice.ID,
		[]application.AllocationInput{
			{ClaimID: approved, AllocatedAmount: "300"},
			{ClaimID: approved, AllocatedAmount: "300"},
		}, view.Invoice.RowVersion)
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("the same claim twice = %v, want a validation error", err)
	}

	// Billing less than was approved is ordinary and has to work.
	after := f.allocate(t, rc, view, approved, "500")
	if after.AllocationTotal != "500" || after.AllocationDifference != "500" {
		t.Errorf("total/difference = %s/%s, want 500/500",
			after.AllocationTotal, after.AllocationDifference)
	}
}

// TestOneClaimSitsOnOneLiveInvoiceThroughTheService is the rule that stops a provider being
// paid twice, refused with the invoice the claim is already on rather than with an index name.
func TestOneClaimSitsOnOneLiveInvoiceThroughTheService(t *testing.T) {
	f := newFixture(t)
	rc := f.financialRC()
	first := f.createInvoice(t, rc, "LIVE1", "600")
	second := f.createInvoice(t, rc, "LIVE2", "600")
	claim := f.approvedClaim(t, "CLM-20260301-LIVE0001", "600")
	f.allocate(t, rc, first, claim, "600")

	_, err := f.invoices.PutAllocations(context.Background(), rc, second.Invoice.ID,
		[]application.AllocationInput{{ClaimID: claim, AllocatedAmount: "600"}},
		second.Invoice.RowVersion)
	var allocationErr *application.AllocationError
	if !errors.As(err, &allocationErr) ||
		allocationErr.Kind != application.AllocationClaimAlreadyInvoiced {
		t.Fatalf("a claim on two invoices = %v, want CLAIM_ALREADY_INVOICED", err)
	}
	if allocationErr.LiveInvoiceID != first.Invoice.ID {
		t.Errorf("the refusal names invoice %v, want the one it is already on %v",
			allocationErr.LiveInvoiceID, first.Invoice.ID)
	}
}

// TestSubmitGateIsTheTenantsTolerance is section 3's third requirement.
//
// Three states of one invoice: outside the tolerance, inside it, and outside a tolerance the
// tenant tightened. The figures are chosen so that a kuruş decides the answer in every case.
func TestSubmitGateIsTheTenantsTolerance(t *testing.T) {
	f := newFixture(t)
	rc := f.financialRC()
	claim := f.approvedClaim(t, "CLM-20260301-TOL00001", "1000")

	// Two kuruş short of the payable amount, with the default tolerance of one.
	view := f.createInvoice(t, rc, "TOL1", "1000")
	view = f.allocate(t, rc, view, claim, "999.98")
	_, err := f.invoices.SubmitInvoice(context.Background(), rc, view.Invoice.ID,
		view.Invoice.RowVersion)
	var mismatch *application.MismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("a mismatch beyond the tolerance = %v, want ALLOCATION_MISMATCH", err)
	}
	if mismatch.PayableAmount != "1000" || mismatch.AllocationTotal != "999.98" ||
		mismatch.Difference != "0.02" || mismatch.Tolerance != settings.DefaultAllocationTolerance {
		t.Errorf("the refusal carries payable=%s allocated=%s difference=%s tolerance=%s; "+
			"want 1000/999.98/0.02/%s", mismatch.PayableAmount, mismatch.AllocationTotal,
			mismatch.Difference, mismatch.Tolerance, settings.DefaultAllocationTolerance)
	}

	// One kuruş short is inside the default tolerance, and goes through.
	view = f.allocate(t, rc, view, claim, "999.99")
	submitted, err := f.invoices.SubmitInvoice(context.Background(), rc, view.Invoice.ID,
		view.Invoice.RowVersion)
	if err != nil {
		t.Fatalf("a mismatch inside the tolerance was refused: %v", err)
	}
	if submitted.Invoice.Status != domain.StatusSubmitted || submitted.Invoice.SubmittedAt == nil {
		t.Fatalf("status = %s submittedAt = %v, want SUBMITTED with a moment",
			submitted.Invoice.Status, submitted.Invoice.SubmittedAt)
	}

	// The tolerance is the tenant's. With it set to zero, the same kuruş is refused.
	f.setSetting(settings.KeyAllocationTolerance, `"0"`)
	other := f.approvedClaim(t, "CLM-20260301-TOL00002", "1000")
	strict := f.createInvoice(t, rc, "TOL2", "1000")
	strict = f.allocate(t, rc, strict, other, "999.99")
	_, err = f.invoices.SubmitInvoice(context.Background(), rc, strict.Invoice.ID,
		strict.Invoice.RowVersion)
	if !errors.As(err, &mismatch) {
		t.Fatalf("a kuruş under a zero tolerance = %v, want ALLOCATION_MISMATCH", err)
	}
	if mismatch.Tolerance != "0" {
		t.Errorf("tolerance reported as %s, want the tenant's 0", mismatch.Tolerance)
	}

	// And with the tenant's tolerance widened, an invoice a lira out goes through — which is
	// what makes the figure a setting rather than a constant.
	f.setSetting(settings.KeyAllocationTolerance, `"1.00"`)
	if _, err := f.invoices.SubmitInvoice(context.Background(), rc, strict.Invoice.ID,
		strict.Invoice.RowVersion); err != nil {
		t.Errorf("a kuruş under a one-lira tolerance was refused: %v", err)
	}
}

// TestSubmitRequiresTheImageWhenTheTenantAsksForOne is the second gate.
func TestSubmitRequiresTheImageWhenTheTenantAsksForOne(t *testing.T) {
	f := newFixture(t)
	rc := f.financialRC()
	claim := f.approvedClaim(t, "CLM-20260301-IMG00001", "500")

	in := f.header("IMG1", "500")
	in.DocumentID = nil
	view, err := f.invoices.CreateInvoice(context.Background(), rc, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	view = f.allocate(t, rc, view, claim, "500")
	_, err = f.invoices.SubmitInvoice(context.Background(), rc, view.Invoice.ID,
		view.Invoice.RowVersion)
	if !errors.Is(err, application.ErrImageRequired) {
		t.Fatalf("submit with no image = %v, want ErrImageRequired", err)
	}

	// A tenant whose providers integrate by API can turn it off.
	f.setSetting(settings.KeyInvoiceRequiresImage, `false`)
	if _, err := f.invoices.SubmitInvoice(context.Background(), rc, view.Invoice.ID,
		view.Invoice.RowVersion); err != nil {
		t.Errorf("submit with the image requirement turned off was refused: %v", err)
	}
}

// TestSubmitMovesTheClaimsAndFreezesTheInvoice is section 3's fourth requirement.
func TestSubmitMovesTheClaimsAndFreezesTheInvoice(t *testing.T) {
	f := newFixture(t)
	rc := f.financialRC()
	first := f.approvedClaim(t, "CLM-20260301-FRZ00001", "600")
	second := f.approvedClaim(t, "CLM-20260301-FRZ00002", "400")
	view := f.createInvoice(t, rc, "FREEZE1", "1000")
	out, err := f.invoices.PutAllocations(context.Background(), rc, view.Invoice.ID,
		[]application.AllocationInput{
			{ClaimID: first, AllocatedAmount: "600"},
			{ClaimID: second, AllocatedAmount: "400"},
		}, view.Invoice.RowVersion)
	if err != nil {
		t.Fatalf("put allocations: %v", err)
	}
	if out.AllocationTotal != "1000" || out.AllocationDifference != "0" {
		t.Fatalf("total/difference = %s/%s, want 1000/0",
			out.AllocationTotal, out.AllocationDifference)
	}

	submitted, err := f.invoices.SubmitInvoice(context.Background(), rc, out.Invoice.ID,
		out.Invoice.RowVersion)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	// The claims are on a document somebody is collecting.
	if got := f.claimStatus(t, first); got != "INVOICED" {
		t.Errorf("first claim = %s, want INVOICED", got)
	}
	if got := f.claimStatus(t, second); got != "INVOICED" {
		t.Errorf("second claim = %s, want INVOICED", got)
	}

	// The header is frozen.
	number := "FREEZE2"
	_, err = f.invoices.PatchInvoiceDraft(context.Background(), rc, submitted.Invoice.ID,
		application.PatchInvoiceInput{InvoiceNumber: &number}, submitted.Invoice.RowVersion)
	if !errors.Is(err, application.ErrInvoiceFrozen) {
		t.Errorf("editing a submitted invoice = %v, want ErrInvoiceFrozen", err)
	}

	// The links are frozen.
	_, err = f.invoices.PutAllocations(context.Background(), rc, submitted.Invoice.ID,
		[]application.AllocationInput{{ClaimID: first, AllocatedAmount: "1"}},
		submitted.Invoice.RowVersion)
	if !errors.Is(err, application.ErrInvoiceFrozen) {
		t.Errorf("reallocating a submitted invoice = %v, want ErrInvoiceFrozen", err)
	}

	// And it cannot be withdrawn: it is the payer's business now.
	_, err = f.invoices.CancelInvoice(context.Background(), rc, submitted.Invoice.ID,
		submitted.Invoice.RowVersion)
	if !errors.Is(err, application.ErrTransitionInvalid) {
		t.Errorf("cancelling a submitted invoice = %v, want ErrTransitionInvalid", err)
	}

	// Submitting it a second time is refused rather than silently repeated. The command is not
	// idempotent by nature; the Idempotency-Key middleware is what makes a *replay* answer the
	// same body, and this is what makes a genuine second attempt an error.
	_, err = f.invoices.SubmitInvoice(context.Background(), rc, submitted.Invoice.ID,
		submitted.Invoice.RowVersion)
	if !errors.Is(err, application.ErrTransitionInvalid) {
		t.Errorf("a second submit = %v, want ErrTransitionInvalid", err)
	}
}

// TestSubmitPublishesTheEventOnce checks the outbox row the batch of WP-I7-03 and the
// accounting projection of M8 will consume. It carries identifiers and figures and no invoice
// number: an outbox payload is read by every consumer, including ones written later.
func TestSubmitPublishesTheEventOnce(t *testing.T) {
	f := newFixture(t)
	rc := f.financialRC()
	claim := f.approvedClaim(t, "CLM-20260301-EVT00001", "500")
	view := f.createInvoice(t, rc, "EVENT1", "500")
	view = f.allocate(t, rc, view, claim, "500")
	if _, err := f.invoices.SubmitInvoice(context.Background(), rc, view.Invoice.ID,
		view.Invoice.RowVersion); err != nil {
		t.Fatalf("submit: %v", err)
	}

	ctx, cancel := f.h.Ctx()
	defer cancel()
	var n int
	var payload string
	if err := f.h.Admin.QueryRow(ctx, `
		SELECT count(*) OVER (), payload_json::text
		  FROM system.outbox_event
		 WHERE tenant_id = $1 AND event_type = $2 AND aggregate_id = $3`,
		f.tenant, application.SubmittedEvent, view.Invoice.ID).Scan(&n, &payload); err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	if n != 1 {
		t.Fatalf("the submit published %d events, want exactly one", n)
	}
	if strings.Contains(payload, "EVENT1") {
		t.Errorf("the outbox payload carries the invoice number: %s", payload)
	}
}

// TestSupersedeChain is section 3's fifth requirement.
//
// A returned invoice, its correction, and the moment the old one is actually cancelled: when
// the correction is submitted, not when it is drafted. A correction somebody abandoned must not
// have cancelled the document it was going to replace.
func TestSupersedeChain(t *testing.T) {
	f := newFixture(t)
	rc := f.financialRC()
	claim := f.approvedClaim(t, "CLM-20260301-SUP00001", "1000")

	original := f.createInvoice(t, rc, "SUP1", "1000")
	original = f.allocate(t, rc, original, claim, "1000")
	submitted, err := f.invoices.SubmitInvoice(context.Background(), rc, original.Invoice.ID,
		original.Invoice.RowVersion)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	f.returnInvoice(submitted.Invoice.ID)

	// The correction: the new draft starts with the old header's allocations.
	in := f.header("SUP2", "900")
	in.SupersedesInvoiceID = &submitted.Invoice.ID
	correction, err := f.invoices.CreateInvoice(context.Background(), rc, in)
	if err != nil {
		t.Fatalf("create the correction: %v", err)
	}
	if len(correction.Allocations) != 1 || correction.Allocations[0].ClaimID != claim {
		t.Fatalf("the correction carries %d allocations, want the old invoice's one",
			len(correction.Allocations))
	}

	// The old invoice is still RETURNED. Nothing has been cancelled yet.
	before, err := f.invoices.GetInvoice(context.Background(), rc, submitted.Invoice.ID)
	if err != nil {
		t.Fatalf("read the returned invoice: %v", err)
	}
	if before.Invoice.Status != domain.StatusReturned {
		t.Fatalf("the superseded invoice is %s before the correction was submitted, want RETURNED",
			before.Invoice.Status)
	}
	if before.Invoice.SupersededByInvoiceID != nil {
		t.Error("the superseded invoice already names its successor before it was submitted")
	}

	// Correct the figure and submit.
	corrected := f.allocate(t, rc, correction, claim, "900")
	final, err := f.invoices.SubmitInvoice(context.Background(), rc, corrected.Invoice.ID,
		corrected.Invoice.RowVersion)
	if err != nil {
		t.Fatalf("submit the correction: %v", err)
	}
	if final.Invoice.SupersedesInvoiceID == nil ||
		*final.Invoice.SupersedesInvoiceID != submitted.Invoice.ID {
		t.Errorf("the correction does not name what it replaced")
	}

	after, err := f.invoices.GetInvoice(context.Background(), rc, submitted.Invoice.ID)
	if err != nil {
		t.Fatalf("read the superseded invoice: %v", err)
	}
	if after.Invoice.Status != domain.StatusCancelled {
		t.Errorf("the superseded invoice is %s, want CANCELLED", after.Invoice.Status)
	}
	if after.Invoice.SupersededByInvoiceID == nil ||
		*after.Invoice.SupersededByInvoiceID != final.Invoice.ID {
		t.Errorf("the superseded invoice does not name its successor")
	}

	// The chain reads in order, and reads the same from either end.
	fromOld, err := f.invoices.ListChain(context.Background(), rc, submitted.Invoice.ID)
	if err != nil {
		t.Fatalf("chain from the old invoice: %v", err)
	}
	fromNew, err := f.invoices.ListChain(context.Background(), rc, final.Invoice.ID)
	if err != nil {
		t.Fatalf("chain from the correction: %v", err)
	}
	for name, chain := range map[string][]application.InvoiceSummaryRecord{
		"from the old invoice": fromOld, "from the correction": fromNew,
	} {
		if len(chain) != 2 {
			t.Fatalf("%s: chain has %d rows, want 2", name, len(chain))
		}
		if chain[0].ID != submitted.Invoice.ID || chain[1].ID != final.Invoice.ID {
			t.Errorf("%s: chain is out of order", name)
		}
	}
}

// TestCancelReleasesTheClaims proves the way back: a withdrawn draft gives its claims up, the
// rows stay on the record marked inactive, and the claims are offered again.
func TestCancelReleasesTheClaims(t *testing.T) {
	f := newFixture(t)
	rc := f.financialRC()
	claim := f.approvedClaim(t, "CLM-20260301-CAN00001", "500")
	view := f.createInvoice(t, rc, "CANCEL1", "500")
	view = f.allocate(t, rc, view, claim, "500")
	submitted, err := f.invoices.SubmitInvoice(context.Background(), rc, view.Invoice.ID,
		view.Invoice.RowVersion)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := f.claimStatus(t, claim); got != "INVOICED" {
		t.Fatalf("claim = %s, want INVOICED", got)
	}

	// WP-I7-03's reviewer returns it; the provider withdraws it.
	f.returnInvoiceWithoutReleasingClaims(submitted.Invoice.ID)
	returned, err := f.invoices.GetInvoice(context.Background(), rc, submitted.Invoice.ID)
	if err != nil {
		t.Fatalf("read the returned invoice: %v", err)
	}
	// The return already released the link, in the database.
	if len(returned.Allocations) != 1 || returned.Allocations[0].Active {
		t.Fatalf("the returned invoice is still holding its claim")
	}

	cancelled, err := f.invoices.CancelInvoice(context.Background(), rc, returned.Invoice.ID,
		returned.Invoice.RowVersion)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.Invoice.Status != domain.StatusCancelled {
		t.Errorf("status = %s, want CANCELLED", cancelled.Invoice.Status)
	}
	// Nothing was deleted: "which claims did this cancelled invoice cover" is a question a
	// dispute asks.
	if len(cancelled.Allocations) != 1 {
		t.Errorf("the cancelled invoice carries %d allocations, want its record kept",
			len(cancelled.Allocations))
	}
	// And the claim is free again. This is the case the release would be easiest to get wrong
	// in: the link was already inactive when the cancellation ran, so a release that only
	// looked at active rows would have left the claim INVOICED for ever -- billable by nobody
	// and collectable by nobody.
	if got := f.claimStatus(t, claim); got != "APPROVED" {
		t.Errorf("claim = %s after the cancellation, want APPROVED", got)
	}
}

// TestCancellingADraftPutsTheClaimBack is the other half of the release: a draft's claims were
// never moved, so there is nothing to put back, and the claim is offered again immediately.
func TestCancellingADraftPutsTheClaimBack(t *testing.T) {
	f := newFixture(t)
	rc := f.financialRC()
	claim := f.approvedClaim(t, "CLM-20260301-DRF00001", "500")
	view := f.createInvoice(t, rc, "DRAFT1", "500")
	view = f.allocate(t, rc, view, claim, "500")

	if _, err := f.invoices.CancelInvoice(context.Background(), rc, view.Invoice.ID,
		view.Invoice.RowVersion); err != nil {
		t.Fatalf("cancel the draft: %v", err)
	}
	if got := f.claimStatus(t, claim); got != "APPROVED" {
		t.Errorf("claim = %s, want APPROVED", got)
	}
	// And it can be put on another invoice.
	second := f.createInvoice(t, rc, "DRAFT2", "500")
	f.allocate(t, rc, second, claim, "500")
}

// TestEarningsStopOfferingAnAllocatedClaim is the change WP-I7-02 makes to WP-I7-01's earnings
// view, and it is the one an implementation is most likely to get wrong.
//
// A claim on a *draft* invoice is still APPROVED. Keying "invoiceable" off the status alone —
// which is what WP-I7-01 had to do, because the link table did not exist — would offer it a
// second time, and the provider would build a second invoice around money already on a
// document.
func TestEarningsStopOfferingAnAllocatedClaim(t *testing.T) {
	f := newFixture(t)
	rc := f.financialRC()
	free := f.approvedClaim(t, "CLM-20260301-ERN00001", "600")
	allocated := f.approvedClaim(t, "CLM-20260301-ERN00002", "400")

	claimRC := f.claimRC()
	before := f.earnings(t, claimRC)
	if before.InvoiceableTotal != "1000" || len(before.InvoiceableClaimIDs) != 2 {
		t.Fatalf("before: invoiceable = %s over %d claims, want 1000 over 2",
			before.InvoiceableTotal, len(before.InvoiceableClaimIDs))
	}

	view := f.createInvoice(t, rc, "EARN1", "400")
	f.allocate(t, rc, view, allocated, "400")

	after := f.earnings(t, claimRC)
	if after.ApprovedTotal != "1000" {
		t.Errorf("approved total = %s, want 1000 — allocating changes what is collectable, "+
			"not what was earned", after.ApprovedTotal)
	}
	if after.InvoiceableTotal != "600" {
		t.Errorf("invoiceable total = %s, want only the claim nobody has put on a document (600)",
			after.InvoiceableTotal)
	}
	if len(after.InvoiceableClaimIDs) != 1 || after.InvoiceableClaimIDs[0] != free {
		t.Errorf("invoiceable claim ids = %v, want only the free claim %v",
			after.InvoiceableClaimIDs, free)
	}

	// Withdrawing the draft offers it again.
	current, err := f.invoices.GetInvoice(context.Background(), rc, view.Invoice.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := f.invoices.CancelInvoice(context.Background(), rc, current.Invoice.ID,
		current.Invoice.RowVersion); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	restored := f.earnings(t, claimRC)
	if restored.InvoiceableTotal != "1000" {
		t.Errorf("after the cancellation invoiceable = %s, want 1000 again",
			restored.InvoiceableTotal)
	}
}

// claimRC is the caller the earnings view is asked as: a payer-side reader of claims.
func (f *fixture) claimRC() identity.RequestContext {
	rc := f.financialRC()
	rc.Permissions = map[string]struct{}{claimapp.PermissionRead: {}}
	return rc
}

// earnings asks WP-I7-01's endpoint for this provider's TRY bucket.
func (f *fixture) earnings(t *testing.T, rc identity.RequestContext) claimapp.EarningsCurrency {
	t.Helper()
	out, err := f.claims.ProviderEarnings(context.Background(), rc, f.provider,
		claimapp.EarningsFilter{})
	if err != nil {
		t.Fatalf("provider earnings: %v", err)
	}
	for _, bucket := range out.Currencies {
		if bucket.CurrencyCode == "TRY" {
			return bucket
		}
	}
	t.Fatalf("earnings carry no TRY bucket; currencies = %d", len(out.Currencies))
	return claimapp.EarningsCurrency{}
}
