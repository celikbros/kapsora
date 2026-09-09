package application

import (
	"context"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/billing/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// Replacing the set of claims an invoice covers.
//
// It is a replacement rather than an add-and-remove because that is what the screen is: a
// provider ticks claims off their earnings view until the total matches their document. An
// endpoint that added one link at a time would make "the invoice now covers exactly these"
// a sequence of requests, and a half-applied sequence is an invoice whose total is a
// coincidence.
//
// Every rule below is checked twice on purpose. Here, so the caller is told which claim and
// why; and in the database, by the deferred constraint trigger and the partial unique index,
// so a backfill, a psql session or a future command cannot get it wrong.

// AllocationInput is one line of the request.
type AllocationInput struct {
	ClaimID         uuid.UUID
	AllocatedAmount string
}

// maxAllocations caps one invoice's claim set. It matches the contract's `maxItems` and exists
// so a request cannot ask the deferred trigger to walk an unbounded number of claims at commit.
const maxAllocations = 500

// PutAllocations replaces the whole set of claim allocations of a DRAFT.
func (s *Service) PutAllocations(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID, in []AllocationInput, expected int64,
) (InvoiceView, error) {
	if len(in) > maxAllocations {
		return InvoiceView{}, fieldError("allocations", "LENGTH",
			"bir faturaya en fazla 500 dosya bağlanabilir")
	}

	var out InvoiceView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		record, err := s.repo.LockInvoice(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		if err := checkProviderScope(rc, record.ProviderOrganizationID); err != nil {
			return err
		}
		if !domain.CanAllocate(record.Status) {
			return ErrInvoiceFrozen
		}
		if record.RowVersion != expected {
			return ErrVersionMismatch
		}

		rows, err := s.resolveAllocations(ctx, tx, rc, record, in)
		if err != nil {
			return err
		}

		if err := s.repo.DeleteAllocations(ctx, tx, rc.TenantID, id); err != nil {
			return err
		}
		for _, row := range rows {
			if err := s.repo.CreateAllocation(ctx, tx, rc.TenantID, row); err != nil {
				return err
			}
		}
		// The row version has to move even though no column of the invoice changed: the ETag
		// a caller holds is a statement about the invoice *and what it covers*, and an
		// allocation set replaced under a stale If-Match is exactly the race this guards.
		ok, err := s.repo.SetStatus(ctx, tx, rc.TenantID, id, StatusMove{
			Status: domain.StatusDraft, FromStatuses: []string{domain.StatusDraft},
			ActorID: actorPtr(rc.Principal.ActorID),
		}, expected)
		if err != nil {
			return err
		}
		if !ok {
			return ErrVersionMismatch
		}

		if err := s.record(ctx, tx, rc, "invoice.allocations.put", id, map[string]any{
			"allocationCount": len(rows),
			"currencyCode":    record.CurrencyCode,
		}); err != nil {
			return err
		}

		updated, err := s.repo.GetInvoice(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		stored, err := s.repo.ListAllocations(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		out = buildView(updated, stored, projectionFor(rc))
		return nil
	})
	if err != nil {
		return InvoiceView{}, err
	}
	return out, nil
}

// resolveAllocations turns the request into rows, refusing anything the rules do not admit and
// naming the claim it refused.
//
// The claims are read once, for exactly the ids the caller sent, so a fifty-claim invoice is
// one query rather than fifty — and the figures every check uses are the figures of one
// consistent read rather than fifty reads a reviewer could have changed between.
func (s *Service) resolveAllocations(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	invoice InvoiceRecord, in []AllocationInput,
) ([]NewAllocationRow, error) {
	if len(in) == 0 {
		return nil, nil
	}

	ve := &domain.ValidationError{}
	seen := map[uuid.UUID]bool{}
	amounts := make(map[uuid.UUID]benefitdomain.Quantity, len(in))
	ids := make([]uuid.UUID, 0, len(in))
	for i, item := range in {
		if item.ClaimID == uuid.Nil {
			ve.Add(indexed(i, "claimId"), "REQUIRED", "hasar dosyası kimliği zorunlu")
			continue
		}
		if seen[item.ClaimID] {
			// The database would refuse it too (`uq_billing_invoice_claim`), but a caller told
			// "duplicate key" learns nothing: an invoice that named the same claim twice would
			// allocate to it twice and the ceiling would be checked against half the figure.
			ve.Add(indexed(i, "claimId"), "DUPLICATE", "aynı dosya bir faturada bir kez yer alır")
			continue
		}
		amount, err := benefitdomain.ParseQuantity(item.AllocatedAmount)
		if err != nil {
			ve.Add(indexed(i, "allocatedAmount"), "FORMAT", "kesin ondalık bir sayı olmalı")
			continue
		}
		if amount.IsNegative() {
			ve.Add(indexed(i, "allocatedAmount"), "RANGE", "negatif olamaz")
			continue
		}
		seen[item.ClaimID] = true
		amounts[item.ClaimID] = amount
		ids = append(ids, item.ClaimID)
	}
	if err := ve.OrNil(); err != nil {
		return nil, err
	}

	candidates, err := s.repo.InvoiceableClaims(ctx, tx, rc.TenantID, ids)
	if err != nil {
		return nil, err
	}
	byClaim := make(map[uuid.UUID]ClaimCandidate, len(candidates))
	for _, c := range candidates {
		byClaim[c.ClaimID] = c
	}

	out := make([]NewAllocationRow, 0, len(ids))
	for _, claimID := range ids {
		candidate, ok := byClaim[claimID]
		if !ok {
			// A claim of another tenant, or none at all. Both are the same refusal with
			// nothing to name: that a claim exists somewhere is not this caller's business.
			return nil, &AllocationError{
				Kind: AllocationClaimNotInvoiceable, ClaimID: claimID,
			}
		}
		if candidate.ProviderOrganizationID != invoice.ProviderOrganizationID {
			// A claim of another provider. It is refused as not invoiceable *by this invoice*
			// rather than as a scope violation, because the caller may well be allowed to see
			// it — it simply is not theirs to bill on this document.
			return nil, &AllocationError{
				Kind: AllocationClaimNotInvoiceable, ClaimID: claimID,
				ClaimReference: candidate.Reference, ClaimStatus: candidate.Status,
			}
		}
		if !domain.ClaimIsInvoiceable(candidate.Status) {
			return nil, &AllocationError{
				Kind: AllocationClaimNotInvoiceable, ClaimID: claimID,
				ClaimReference: candidate.Reference, ClaimStatus: candidate.Status,
			}
		}
		if candidate.LiveInvoiceID != uuid.Nil && candidate.LiveInvoiceID != invoice.ID {
			return nil, &AllocationError{
				Kind: AllocationClaimAlreadyInvoiced, ClaimID: claimID,
				ClaimReference: candidate.Reference, LiveInvoiceID: candidate.LiveInvoiceID,
			}
		}
		if candidate.CurrencyCode != invoice.CurrencyCode {
			return nil, &AllocationError{
				Kind: AllocationCurrency, ClaimID: claimID, ClaimReference: candidate.Reference,
				CurrencyCode: candidate.CurrencyCode, InvoiceCurrency: invoice.CurrencyCode,
			}
		}
		amount := amounts[claimID]
		approved := quantityOrZero(candidate.ApprovedTotal)
		if amount.Cmp(approved) > 0 {
			// The ceiling. Billing *less* of an approved claim than was approved is ordinary
			// — a provider collecting in instalments, or writing part of it off — and billing
			// more is the payer paying for something it never agreed to.
			return nil, &AllocationError{
				Kind: AllocationExceedsApproved, ClaimID: claimID,
				ClaimReference: candidate.Reference, Allocated: amount.String(),
				Approved: approved.String(),
			}
		}
		out = append(out, NewAllocationRow{
			InvoiceID: invoice.ID, ClaimID: claimID,
			ClaimVersionNo: candidate.CurrentVersionNo, AllocatedAmount: amount.String(),
			CurrencyCode: invoice.CurrencyCode, ClaimStatusBefore: candidate.Status,
			ActorID: actorPtr(rc.Principal.ActorID),
		})
	}
	return out, nil
}

// indexed names a field inside the allocation array the way the rest of the platform does, so
// a screen can put the error back beside the row the caller typed it on.
func indexed(i int, field string) string {
	return "allocations[" + strconv.Itoa(i) + "]." + field
}
