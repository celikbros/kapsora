package application

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/billing/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/outbox"
)

// The two commands that move an invoice out of DRAFT, and the one event they publish.

// SubmittedEvent is the outbox type a submitted invoice publishes. The batch of WP-I7-03 and
// the accounting projection of M8 are its consumers; neither of them exists yet, and the event
// is published now because a consumer added later can be replayed and a fact never recorded
// cannot be recovered.
const SubmittedEvent = "invoice.submitted"

// SubmitInvoice sends a draft to the payer.
//
// Three gates, in the order a provider can act on them: does the total add up, is the image
// there, and are the claims still invoiceable. Then the transition, the claims, the freeze and
// the event — all in one transaction, so an invoice that is SUBMITTED and claims that never
// moved is a state no reader ever observes.
func (s *Service) SubmitInvoice(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	expected int64,
) (InvoiceView, error) {
	var out InvoiceView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		record, err := s.repo.LockInvoice(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		if err := checkProviderScope(rc, record.ProviderOrganizationID); err != nil {
			return err
		}
		if !domain.CanSubmit(record.Status) {
			return ErrTransitionInvalid
		}
		if record.RowVersion != expected {
			return ErrVersionMismatch
		}

		rows, err := s.repo.ListAllocations(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return ErrNothingAllocated
		}

		values, err := s.settingsFor(ctx, tx, rc.TenantID)
		if err != nil {
			return err
		}

		// Gate one: the sum. It is added up here, in Go, in exact decimals, from the rows
		// themselves — the same arithmetic the detail response shows, so the figure a screen
		// displayed and the figure this gate applied are produced by one implementation.
		total := zero()
		for _, row := range rows {
			total = total.Add(quantityOrZero(row.AllocatedAmount))
		}
		payable := quantityOrZero(record.PayableAmount)
		tolerance := quantityOrZero(values.AllocationTolerance)
		ok, difference := domain.WithinTolerance(payable, total, tolerance)
		if !ok {
			return &MismatchError{
				PayableAmount: payable.String(), AllocationTotal: total.String(),
				Difference: difference.String(), Tolerance: tolerance.String(),
			}
		}

		// Gate two: the image, when the tenant asks for one.
		if values.InvoiceRequiresImage {
			if record.DocumentID == nil {
				return ErrImageRequired
			}
			if err := s.checkDocument(ctx, tx, rc.TenantID, *record.DocumentID); err != nil {
				return err
			}
		}

		// Gate three: every allocated claim is still what it was when it was allocated. A
		// claim a reviewer reopened while the draft sat on somebody's screen is refused by
		// name rather than being billed for a decision that no longer stands.
		claimIDs := make([]uuid.UUID, 0, len(rows))
		for _, row := range rows {
			claimIDs = append(claimIDs, row.ClaimID)
		}
		candidates, err := s.repo.InvoiceableClaims(ctx, tx, rc.TenantID, claimIDs)
		if err != nil {
			return err
		}
		byClaim := make(map[uuid.UUID]ClaimCandidate, len(candidates))
		for _, c := range candidates {
			byClaim[c.ClaimID] = c
		}
		for _, row := range rows {
			candidate, found := byClaim[row.ClaimID]
			if !found || !domain.ClaimIsInvoiceable(candidate.Status) {
				return &AllocationError{
					Kind: AllocationClaimNotInvoiceable, ClaimID: row.ClaimID,
					ClaimReference: row.ClaimReference, ClaimStatus: candidate.Status,
				}
			}
			if allocated := quantityOrZero(row.AllocatedAmount); allocated.Cmp(
				quantityOrZero(candidate.ApprovedTotal)) > 0 {
				return &AllocationError{
					Kind: AllocationExceedsApproved, ClaimID: row.ClaimID,
					ClaimReference: row.ClaimReference, Allocated: allocated.String(),
					Approved: candidate.ApprovedTotal,
				}
			}
		}

		now := s.now().UTC()
		moved, err := s.repo.SetStatus(ctx, tx, rc.TenantID, id, StatusMove{
			Status: domain.StatusSubmitted, FromStatuses: []string{domain.StatusDraft},
			SubmittedAt: &now, ActorID: actorPtr(rc.Principal.ActorID),
		}, expected)
		if err != nil {
			return err
		}
		if !moved {
			return ErrVersionMismatch
		}

		// The claims move onto the invoice through the claim module's own command. From this
		// moment the money is on a document somebody is collecting, and the earnings view
		// stops offering these claims.
		if err := s.claims.MarkInvoiced(ctx, tx, rc.TenantID,
			actorPtr(rc.Principal.ActorID), claimIDs); err != nil {
			return err
		}

		// The correction takes effect now and not when it was drafted: a correction somebody
		// abandoned must not have cancelled the document it was going to replace.
		if record.SupersedesInvoiceID != nil {
			superseded, err := s.repo.SetSupersededBy(ctx, tx, rc.TenantID,
				*record.SupersedesInvoiceID, id, actorPtr(rc.Principal.ActorID))
			if err != nil {
				return err
			}
			if !superseded {
				return ErrNotSupersedable
			}
		}

		if err := s.publishSubmitted(ctx, tx, rc, record, total, len(rows)); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "invoice.submit", id, map[string]any{
			"payable_amount":   payable.String(),
			"allocation_total": total.String(),
			"difference":       difference.String(),
			"tolerance":        tolerance.String(),
			"allocation_count": len(rows),
			"currency_code":    record.CurrencyCode,
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

// publishSubmitted writes the outbox row a submitted invoice owes, inside the command's own
// transaction.
//
// The payload is identifiers, a currency, two exact decimals and a count. There is no invoice
// number and no tax identity in it: an outbox payload is read by every consumer, including
// ones written later, and a provider's fiscal document number is not something to broadcast.
func (s *Service) publishSubmitted(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record InvoiceRecord, total benefitdomain.Quantity, allocations int,
) error {
	payload := map[string]any{
		"invoiceId":              record.ID,
		"providerOrganizationId": record.ProviderOrganizationID,
		"fiscal_year":            record.FiscalYear,
		"currency_code":          record.CurrencyCode,
		"payable_amount":         record.PayableAmount,
		"allocation_total":       total.String(),
		"allocation_count":       allocations,
		"domainCode":             record.DomainCode,
	}
	if record.SupersedesInvoiceID != nil {
		payload["supersedes_invoice_id"] = *record.SupersedesInvoiceID
	}
	_, _, err := outbox.Publish(ctx, tx, outbox.Event{
		TenantID:      nullUUID(rc.TenantID),
		AggregateType: domain.AggregateInvoice, AggregateID: record.ID, Type: SubmittedEvent,
		Payload: payload,
		// The invoice's own id: one invoice is submitted once, and a redelivered command that
		// moved nothing publishes no second event either.
		DeduplicationKey: record.ID.String(),
	})
	return err
}

// CancelInvoice withdraws a DRAFT or a RETURNED invoice and releases the claims it held.
//
// Nothing is deleted. The claim links stay on the record and go inactive — the invoice's own
// status trigger does that, in the database, so a returned invoice released by WP-I7-03's
// reviewer and a draft withdrawn here behave the same way — and the claims go back to the
// status they carried when they were allocated.
func (s *Service) CancelInvoice(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	expected int64,
) (InvoiceView, error) {
	var out InvoiceView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		record, err := s.repo.LockInvoice(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		if err := checkProviderScope(rc, record.ProviderOrganizationID); err != nil {
			return err
		}
		if !domain.CanCancel(record.Status) {
			return ErrTransitionInvalid
		}
		if record.RowVersion != expected {
			return ErrVersionMismatch
		}

		rows, err := s.repo.ListAllocations(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}

		moved, err := s.repo.SetStatus(ctx, tx, rc.TenantID, id, StatusMove{
			Status:       domain.StatusCancelled,
			FromStatuses: []string{domain.StatusDraft, domain.StatusReturned},
			ActorID:      actorPtr(rc.Principal.ActorID),
		}, expected)
		if err != nil {
			return err
		}
		if !moved {
			return ErrVersionMismatch
		}

		// Which claims go back. It is not "the rows that were active": a returned invoice's
		// rows were already released by the status trigger, and its claims are still INVOICED
		// until somebody puts them back -- so a cancellation after a return has real work to
		// do. What it must not do is drag back a claim that has meanwhile gone onto the
		// correction and been submitted there, so the claims are re-read and a claim now
		// living on a *different* live invoice is left alone.
		//
		// `ReleaseFromInvoice` skips anything that is not INVOICED, so a claim a settlement has
		// already moved on is left where it is rather than dragged backwards.
		releases, err := s.releasesFor(ctx, tx, rc.TenantID, record.ID, rows)
		if err != nil {
			return err
		}
		if len(releases) > 0 {
			if err := s.claims.ReleaseFromInvoice(ctx, tx, rc.TenantID,
				actorPtr(rc.Principal.ActorID), releases); err != nil {
				return err
			}
		}

		if err := s.record(ctx, tx, rc, "invoice.cancel", id, map[string]any{
			"from_status":     record.Status,
			"released_claims": len(releases),
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

// releasesFor decides which of an invoice's claims go back to the status they were allocated
// at. A claim that has since been put on another live invoice belongs to that document now and
// is not this cancellation's to move.
func (s *Service) releasesFor(ctx context.Context, tx pgx.Tx, tenantID, invoiceID uuid.UUID,
	rows []AllocationRecord,
) ([]ClaimRelease, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ClaimID)
	}
	candidates, err := s.repo.InvoiceableClaims(ctx, tx, tenantID, ids)
	if err != nil {
		return nil, err
	}
	live := make(map[uuid.UUID]uuid.UUID, len(candidates))
	for _, c := range candidates {
		live[c.ClaimID] = c.LiveInvoiceID
	}
	out := make([]ClaimRelease, 0, len(rows))
	for _, row := range rows {
		if other, ok := live[row.ClaimID]; ok && other != uuid.Nil && other != invoiceID {
			continue
		}
		out = append(out, ClaimRelease{ClaimID: row.ClaimID, Status: row.ClaimStatusBefore})
	}
	return out, nil
}
