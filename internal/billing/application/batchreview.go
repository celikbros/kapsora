package application

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/billing/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/outbox"
)

// The payer's side of the icmal: one decision per invoice, and the moment the whole batch is
// closed off.
//
// Two rules run through everything below.
//
// **A decision is never edited.** Changing one takes back what the previous one did — the CUT
// adjustments are reversed rather than rewritten, a return puts the invoice and its claims back
// where the batch had them, a rejection is undone the same way — and only then is the new
// decision applied. The row that carries the decision holds the last answer; the ledger holds
// every one of them.
//
// **The totals are the server's.** Nothing a caller sends decides what a batch approved: the
// four totals are recomputed from the decisions at the moment the batch is decided, and
// `ck_billing_batch_totals` refuses the write unless they add up to the submitted total.

// outcomeBatchDecided is the work item outcome a decided batch closes its review with.
const outcomeBatchDecided = "DECIDED"

// ReviewBatchInvoiceInput is the reviewBatchInvoice command.
type ReviewBatchInvoiceInput struct {
	Decision string
	// ApprovedAmount is meaningful only on a CUT; the other three decisions derive it from
	// what was submitted.
	ApprovedAmount string
	ReasonCode     string
	ReasonText     *string
}

// ReviewBatchInvoice records the payer's answer to one invoice in the batch.
//
// Taking the first decision is what moves the batch to UNDER_REVIEW and claims the work item:
// the payer's reviewer has opened the icmal and is working through it. It happens in the same
// transaction as the decision, so a batch that is UNDER_REVIEW with nothing decided and a
// decision on a batch still marked SUBMITTED are both states no reader ever observes.
func (s *Service) ReviewBatchInvoice(ctx context.Context, rc identity.RequestContext,
	batchID, invoiceID uuid.UUID, in ReviewBatchInvoiceInput, expected int64,
) (BatchView, error) {
	var out BatchView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.requireBatches(); err != nil {
			return err
		}
		record, err := s.batches.LockBatch(ctx, tx, rc.TenantID, batchID, scopeOf(rc))
		if err != nil {
			return err
		}
		if err := checkProviderScope(rc, record.ProviderOrganizationID); err != nil {
			return err
		}
		if !domain.CanReviewBatch(record.Status) {
			return ErrBatchTransitionInvalid
		}
		if record.RowVersion != expected {
			return ErrVersionMismatch
		}

		member, err := s.batches.LockBatchInvoice(ctx, tx, rc.TenantID, record.ID, invoiceID)
		if err != nil {
			return err
		}
		submitted := quantityOrZero(member.SubmittedAmount)
		decision, approved, reasonCode, reasonText, err := domain.ValidateBatchDecision(
			domain.BatchDecisionInput{
				Decision: in.Decision, ApprovedAmount: in.ApprovedAmount,
				ReasonCode: in.ReasonCode, ReasonText: in.ReasonText,
			}, submitted)
		if err != nil {
			return err
		}
		// A cut writes an adjustment on every claim the invoice covers, and an adjustment's
		// reason is a closed list the claim module owns. It is checked here, before anything is
		// written, so a reviewer is told which codes they may use rather than watching the
		// command fail halfway through the claims.
		if decision == domain.DecisionCut && !s.knownCutReason(reasonCode) {
			return fieldError("reasonCode", "ENUM",
				"kesinti gerekçesi tanımlı kesinti kodlarından biri olmalı")
		}

		// The batch comes in front of a person now. Both halves are one fact and both are in
		// this transaction: the status, and the work item that says who is doing it.
		if record.Status == domain.BatchSubmitted {
			moved, err := s.batches.StartBatchReview(ctx, tx, rc.TenantID, record.ID,
				actorPtr(rc.Principal.ActorID))
			if err != nil {
				return err
			}
			if !moved {
				return ErrBatchTransitionInvalid
			}
			if err := s.workItems.Claim(ctx, tx, rc.TenantID, domain.AggregateBatch, record.ID,
				rc.Principal.ActorID, actorPtr(rc.Principal.ActorID)); err != nil {
				return err
			}
		}

		// Take back whatever the last decision did, then apply this one. Never an edit: the
		// money a changed decision moved is reversed on the record, so "who cut this and who
		// put it back" is answerable from the ledger rather than from a diff nobody kept.
		if err := s.undoBatchDecision(ctx, tx, rc, member); err != nil {
			return err
		}
		if err := s.applyBatchDecision(ctx, tx, rc, member, decision, submitted, approved,
			reasonCode, reasonText); err != nil {
			return err
		}

		now := s.now().UTC()
		row := BatchDecisionRow{
			Decision: decision, ApprovedAmount: approved.String(),
			ReasonText: reasonText, DecidedBy: rc.Principal.ActorID, DecidedAt: now,
		}
		if reasonCode != "" {
			row.ReasonCode = &reasonCode
		}
		written, err := s.batches.SetBatchInvoiceDecision(ctx, tx, rc.TenantID, member.ID, row)
		if err != nil {
			return err
		}
		if !written {
			return ErrBatchInvoiceNotFound
		}

		if err := s.recordBatch(ctx, tx, rc, "batch.invoice.review", record.ID, map[string]any{
			"reference":         record.Reference,
			"invoice_id":        member.InvoiceID.String(),
			"decision":          decision,
			"previous_decision": member.Decision,
			"submitted_amount":  submitted.String(),
			"approved_amount":   approved.String(),
			"cut_amount":        submitted.Sub(approved).String(),
			"reason_code":       reasonCode,
			"currency_code":     record.CurrencyCode,
		}); err != nil {
			return err
		}
		out, err = s.loadBatch(ctx, tx, rc, record.ID)
		return err
	})
	if err != nil {
		return BatchView{}, err
	}
	return out, nil
}

// knownCutReason asks the claim module which reasons a cut may carry. The list is answered
// through the port rather than copied here, so this package cannot end up offering a reviewer a
// code the ledger would refuse.
func (s *Service) knownCutReason(code string) bool {
	for _, known := range s.claims.CutReasons() {
		if known == code {
			return true
		}
	}
	return false
}

// undoBatchDecision takes back what the member's current decision did, if it did anything.
//
// It is the whole of "a changed decision reverses rather than edits". Every branch is the exact
// inverse of the matching branch of applyBatchDecision, and the pairing is why they sit next to
// each other.
func (s *Service) undoBatchDecision(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	member BatchInvoiceRecord,
) error {
	actor := actorPtr(rc.Principal.ActorID)
	switch member.Decision {
	case "", domain.DecisionApprove:
		// An approval moved nothing: the invoice stays IN_BATCH until the batch is decided.
		return nil

	case domain.DecisionCut:
		links, err := s.batches.ListLiveBatchAdjustments(ctx, tx, rc.TenantID, member.ID)
		if err != nil {
			return err
		}
		if len(links) == 0 {
			return nil
		}
		reversals := make([]ClaimReversal, 0, len(links))
		for _, link := range links {
			reversals = append(reversals, ClaimReversal{
				ClaimID: link.ClaimID, AdjustmentID: link.AdjustmentID,
			})
		}
		written, err := s.claims.Reverse(ctx, tx, rc.TenantID, actor, reversals)
		if err != nil {
			return err
		}
		if len(written) != len(links) {
			return ErrBatchTotalsMismatch
		}
		for i, link := range links {
			if err := s.batches.MarkBatchAdjustmentReversed(ctx, tx, rc.TenantID, link.ID,
				written[i].AdjustmentID); err != nil {
				return err
			}
		}
		return nil

	case domain.DecisionReturn:
		// The invoice comes back into the batch, and its claims come back onto the invoice.
		// The status trigger of migration 000044 reactivates the claim links as it goes, which
		// is also what refuses the undo when one of those claims has meanwhile been put on a
		// correction: that claim belongs to the other document now.
		if err := s.restoreInBatch(ctx, tx, rc, member, domain.StatusReturned); err != nil {
			return err
		}
		claims, err := s.claimsOfInvoice(ctx, tx, rc.TenantID, member.InvoiceID, false)
		if err != nil {
			return err
		}
		if len(claims) == 0 {
			return nil
		}
		return s.claims.MarkInvoiced(ctx, tx, rc.TenantID, actor, claimIDs(claims))

	case domain.DecisionReject:
		if err := s.restoreInBatch(ctx, tx, rc, member, domain.StatusRejected); err != nil {
			return err
		}
		claims, err := s.claimsOfInvoice(ctx, tx, rc.TenantID, member.InvoiceID, false)
		if err != nil {
			return err
		}
		if len(claims) == 0 {
			return nil
		}
		return s.claims.ReopenUnpaid(ctx, tx, rc.TenantID, actor, claimIDs(claims))
	}
	return nil
}

// applyBatchDecision performs what the new decision means for the invoice and its claims.
func (s *Service) applyBatchDecision(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	member BatchInvoiceRecord, decision string, submitted, approved benefitdomain.Quantity,
	reasonCode string, reasonText *string,
) error {
	actor := actorPtr(rc.Principal.ActorID)
	switch decision {
	case domain.DecisionApprove:
		// Nothing moves yet. The invoice becomes APPROVED when the whole batch is decided, so
		// a reviewer who changes their mind halfway through has not already told the provider
		// that a document was accepted.
		return nil

	case domain.DecisionCut:
		return s.writeCutAdjustments(ctx, tx, rc, member, submitted.Sub(approved), reasonCode,
			reasonText)

	case domain.DecisionReturn:
		// The invoice goes back for correction and stops holding its claims. Setting the
		// status is what releases the links — the trigger of migration 000044 — and the claims
		// then go back to the status they carried when they were allocated, which is what lets
		// the provider put them on the corrected invoice.
		moved, err := s.batches.SetInvoiceReviewStatus(ctx, tx, rc.TenantID, member.InvoiceID,
			domain.StatusReturned, []string{domain.StatusInBatch}, actor)
		if err != nil {
			return err
		}
		if !moved {
			return ErrBatchTransitionInvalid
		}
		rows, err := s.claimsOfInvoice(ctx, tx, rc.TenantID, member.InvoiceID, false)
		if err != nil {
			return err
		}
		releases := make([]ClaimRelease, 0, len(rows))
		for _, row := range rows {
			releases = append(releases, ClaimRelease{
				ClaimID: row.ClaimID, Status: row.ClaimStatusBefore,
			})
		}
		if len(releases) == 0 {
			return nil
		}
		return s.claims.ReleaseFromInvoice(ctx, tx, rc.TenantID, actor, releases)

	case domain.DecisionReject:
		// The document is refused and so is everything on it. The claims are finished rather
		// than freed: nobody is going to be paid for them, and leaving them approved would
		// leave a provider's earnings view counting money that will never arrive.
		moved, err := s.batches.SetInvoiceReviewStatus(ctx, tx, rc.TenantID, member.InvoiceID,
			domain.StatusRejected, []string{domain.StatusInBatch}, actor)
		if err != nil {
			return err
		}
		if !moved {
			return ErrBatchTransitionInvalid
		}
		rows, err := s.claimsOfInvoice(ctx, tx, rc.TenantID, member.InvoiceID, false)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		return s.claims.CloseUnpaid(ctx, tx, rc.TenantID, actor, claimIDs(rows))
	}
	return nil
}

// writeCutAdjustments spreads one cut across the claims the invoice was collecting.
//
// The split is proportional to what each claim was allocated, exact, and the rounding
// difference lands on the largest allocation — `domain.SplitProportional` is the whole of it,
// and the property that matters is that the shares sum to the cut with no remainder anywhere.
//
// A cut on an invoice that allocates nothing is refused rather than silently written as a
// decision that moved no money: an invoice with no claims under it is not something this
// package can have produced, and cutting it would put a figure on a batch that no claim
// carries.
func (s *Service) writeCutAdjustments(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, member BatchInvoiceRecord, cut benefitdomain.Quantity,
	reasonCode string, reasonText *string,
) error {
	rows, err := s.claimsOfInvoice(ctx, tx, rc.TenantID, member.InvoiceID, true)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return ErrNothingAllocated
	}
	weights := make([]benefitdomain.Quantity, 0, len(rows))
	for _, row := range rows {
		weights = append(weights, quantityOrZero(row.AllocatedAmount))
	}
	shares, err := domain.SplitProportional(cut, weights)
	if err != nil {
		return err
	}
	cuts := make([]ClaimCut, 0, len(rows))
	for i, row := range rows {
		cuts = append(cuts, ClaimCut{
			ClaimID: row.ClaimID, Amount: shares[i].String(),
			ReasonCode: reasonCode, ReasonText: reasonText,
		})
	}
	written, err := s.claims.Cut(ctx, tx, rc.TenantID, actorPtr(rc.Principal.ActorID), cuts)
	if err != nil {
		return err
	}
	for _, adjustment := range written {
		if err := s.batches.CreateBatchAdjustmentLink(ctx, tx, rc.TenantID,
			NewBatchAdjustmentLink{
				BatchInvoiceID: member.ID, ClaimID: adjustment.ClaimID,
				AdjustmentID: adjustment.AdjustmentID, Amount: adjustment.Amount,
				ActorID: actorPtr(rc.Principal.ActorID),
			}); err != nil {
			return err
		}
	}
	return nil
}

// restoreInBatch puts an invoice a decision had moved back into the batch it came from.
func (s *Service) restoreInBatch(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	member BatchInvoiceRecord, from string,
) error {
	moved, err := s.batches.SetInvoiceReviewStatus(ctx, tx, rc.TenantID, member.InvoiceID,
		domain.StatusInBatch, []string{from}, actorPtr(rc.Principal.ActorID))
	if err != nil {
		return err
	}
	if !moved {
		return ErrBatchTransitionInvalid
	}
	return nil
}

// claimsOfInvoice reads the claim links of one invoice. `activeOnly` is what a cut is spread
// over — a released link is not money anybody is collecting — and the whole set is what a
// restore has to put back, because the links a return released are exactly the inactive ones.
func (s *Service) claimsOfInvoice(ctx context.Context, tx pgx.Tx, tenantID, invoiceID uuid.UUID,
	activeOnly bool,
) ([]AllocationRecord, error) {
	rows, err := s.repo.ListAllocations(ctx, tx, tenantID, invoiceID)
	if err != nil {
		return nil, err
	}
	if !activeOnly {
		return rows, nil
	}
	out := make([]AllocationRecord, 0, len(rows))
	for _, row := range rows {
		if row.Active {
			out = append(out, row)
		}
	}
	return out, nil
}

func claimIDs(rows []AllocationRecord) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ClaimID)
	}
	return out
}

// DecideBatch closes the icmal off: every invoice answered, the totals recomputed, the work
// item completed and the provider told.
//
// Two people, at least. The submitter never decides — that is WP-I4-03 section 2.4's rule and
// it holds at every amount — and above the tenant's threshold the person who took the last
// decision may not be the one who closes the batch either. The second rule is deliberately
// about the *last decider* rather than about any decider: a reviewer who worked through fifty
// invoices should not be blocked by having touched one of them, and what the threshold buys is
// a second pair of eyes on the final answer.
func (s *Service) DecideBatch(ctx context.Context, rc identity.RequestContext,
	batchID uuid.UUID, expected int64,
) (BatchView, error) {
	var out BatchView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.requireBatches(); err != nil {
			return err
		}
		record, err := s.batches.LockBatch(ctx, tx, rc.TenantID, batchID, scopeOf(rc))
		if err != nil {
			return err
		}
		if err := checkProviderScope(rc, record.ProviderOrganizationID); err != nil {
			return err
		}
		if !domain.CanDecideBatch(record.Status) {
			return ErrBatchTransitionInvalid
		}
		if record.RowVersion != expected {
			return ErrVersionMismatch
		}
		if record.SubmittedBy != nil && *record.SubmittedBy == rc.Principal.ActorID {
			return ErrBatchSubmitterCannotDecide
		}

		members, err := s.batches.ListBatchInvoices(ctx, tx, rc.TenantID, record.ID)
		if err != nil {
			return err
		}
		decided := make([]domain.DecidedRow, 0, len(members))
		for _, member := range members {
			if !member.Decided() {
				return ErrBatchNotFullyDecided
			}
			decided = append(decided, domain.DecidedRow{
				Decision:  member.Decision,
				Submitted: quantityOrZero(member.SubmittedAmount),
				Approved:  quantityOrZero(member.ApprovedAmount),
			})
		}
		totals := domain.DecidedTotals(decided)
		if !totals.Reconciles() ||
			totals.Submitted.Cmp(quantityOrZero(record.SubmittedTotal)) != 0 {
			return ErrBatchTotalsMismatch
		}

		values, err := s.settingsFor(ctx, tx, rc.TenantID)
		if err != nil {
			return err
		}
		if totals.Approved.Cmp(quantityOrZero(values.BatchDecisionThreshold)) > 0 {
			if last := lastDecider(members); last != nil && *last == rc.Principal.ActorID {
				return ErrBatchSecondReviewerRequired
			}
		}

		// Where each decision leaves its invoice. A return and a rejection already moved theirs
		// when they were taken; an approval and a cut move now, so that a reviewer who changed
		// their mind halfway through never told a provider a document had been accepted.
		for _, member := range members {
			status := ""
			switch member.Decision {
			case domain.DecisionApprove:
				status = domain.StatusApproved
			case domain.DecisionCut:
				status = domain.StatusPartiallyApproved
			}
			if status == "" {
				continue
			}
			moved, err := s.batches.SetInvoiceReviewStatus(ctx, tx, rc.TenantID,
				member.InvoiceID, status, []string{domain.StatusInBatch},
				actorPtr(rc.Principal.ActorID))
			if err != nil {
				return err
			}
			if !moved {
				return ErrBatchTransitionInvalid
			}
		}

		now := s.now().UTC()
		closed, err := s.batches.DecideBatch(ctx, tx, rc.TenantID, record.ID, DecideBatchRow{
			DecidedAt: now, DecidedBy: rc.Principal.ActorID,
			ApprovedTotal: totals.Approved.String(), CutTotal: totals.Cut.String(),
			ReturnedTotal: totals.Returned.String(), RejectedTotal: totals.Rejected.String(),
			ActorID: actorPtr(rc.Principal.ActorID),
		}, expected)
		if err != nil {
			return err
		}
		if !closed {
			return ErrVersionMismatch
		}

		if err := s.workItems.Complete(ctx, tx, rc.TenantID, domain.AggregateBatch, record.ID,
			outcomeBatchDecided, actorPtr(rc.Principal.ActorID)); err != nil {
			return err
		}
		if err := s.publishBatchDecided(ctx, tx, rc, record, totals, members); err != nil {
			return err
		}
		if err := s.notifyBatchDecided(ctx, tx, rc, record, totals, now); err != nil {
			return err
		}
		if err := s.recordBatch(ctx, tx, rc, "batch.decide", record.ID, map[string]any{
			"reference":       record.Reference,
			"invoice_count":   len(members),
			"submitted_total": totals.Submitted.String(),
			"approved_total":  totals.Approved.String(),
			"cut_total":       totals.Cut.String(),
			"returned_total":  totals.Returned.String(),
			"rejected_total":  totals.Rejected.String(),
			"currency_code":   record.CurrencyCode,
		}); err != nil {
			return err
		}
		out, err = s.loadBatch(ctx, tx, rc, record.ID)
		return err
	})
	if err != nil {
		return BatchView{}, err
	}
	return out, nil
}

// lastDecider is who took the most recent decision on the batch, or nil when nobody has.
//
// Ties on the timestamp fall to the later row in the members' own order, which is the order the
// decisions were written in; two decisions in the same microsecond are one person's anyway.
func lastDecider(members []BatchInvoiceRecord) *uuid.UUID {
	var latest *time.Time
	var actor *uuid.UUID
	for i := range members {
		member := members[i]
		if member.DecidedAt == nil || member.DecidedBy == nil {
			continue
		}
		if latest == nil || !member.DecidedAt.Before(*latest) {
			latest = member.DecidedAt
			actor = member.DecidedBy
		}
	}
	return actor
}

// GetBatchSummary answers the totals and the per-decision counts, for both sides.
//
// The counts are computed from the members rather than stored, because they are a view of the
// decisions and not a fact of their own: a stored count is a second place for the same truth
// and therefore a second place for it to be wrong.
func (s *Service) GetBatchSummary(ctx context.Context, rc identity.RequestContext,
	batchID uuid.UUID,
) (BatchSummaryView, error) {
	var out BatchSummaryView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.requireBatches(); err != nil {
			return err
		}
		view, err := s.loadBatch(ctx, tx, rc, batchID)
		if err != nil {
			return err
		}
		out = summariseBatch(view)
		return nil
	})
	if err != nil {
		return BatchSummaryView{}, err
	}
	return out, nil
}

// summariseBatch groups the members by decision, in the fixed order of the four words so that
// two calls answer the same shape and a screen can draw a stable set of columns.
func summariseBatch(view BatchView) BatchSummaryView {
	out := BatchSummaryView{Batch: view.Batch, PendingTotal: zero().String()}
	type bucket struct {
		count     int
		submitted benefitdomain.Quantity
		approved  benefitdomain.Quantity
	}
	buckets := make(map[string]*bucket, len(domain.BatchDecisions))
	for _, decision := range domain.BatchDecisions {
		buckets[decision] = &bucket{submitted: zero(), approved: zero()}
	}
	pending := zero()
	for _, member := range view.Invoices {
		submitted := quantityOrZero(member.SubmittedAmount)
		if !member.Decided() {
			out.PendingCount++
			pending = pending.Add(submitted)
			continue
		}
		b, ok := buckets[member.Decision]
		if !ok {
			continue
		}
		b.count++
		b.submitted = b.submitted.Add(submitted)
		b.approved = b.approved.Add(quantityOrZero(member.ApprovedAmount))
	}
	out.PendingTotal = pending.String()
	out.Decisions = make([]BatchDecisionCount, 0, len(domain.BatchDecisions))
	for _, decision := range domain.BatchDecisions {
		b := buckets[decision]
		out.Decisions = append(out.Decisions, BatchDecisionCount{
			Decision: decision, Count: b.count,
			SubmittedTotal: b.submitted.String(), ApprovedTotal: b.approved.String(),
		})
	}
	return out
}

// publishBatchDecided writes the outbox row a decided icmal owes. WP-I7-04's settlement opens
// on it, which is why the payload carries the four totals rather than only the fact.
//
// Each return and each rejection travels with its reason *code* and never its reason text: a
// code is what a settlement and a report can count, and the free prose a reviewer typed is read
// from the batch itself by somebody who is allowed to see it.
func (s *Service) publishBatchDecided(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record BatchRecord, totals domain.BatchTotals, members []BatchInvoiceRecord,
) error {
	returned := make([]map[string]any, 0)
	for _, member := range members {
		if member.Decision != domain.DecisionReturn && member.Decision != domain.DecisionReject {
			continue
		}
		returned = append(returned, map[string]any{
			"invoiceId":  member.InvoiceID,
			"decision":   member.Decision,
			"reasonCode": member.ReasonCode,
			"amount":     member.SubmittedAmount,
		})
	}
	payload := map[string]any{
		"batchId":                record.ID,
		"reference":              record.Reference,
		"providerOrganizationId": record.ProviderOrganizationID,
		"currencyCode":           record.CurrencyCode,
		"domainCode":             record.DomainCode,
		"invoiceCount":           len(members),
		"submittedTotal":         totals.Submitted.String(),
		"approvedTotal":          totals.Approved.String(),
		"cutTotal":               totals.Cut.String(),
		"returnedTotal":          totals.Returned.String(),
		"rejectedTotal":          totals.Rejected.String(),
		"returnedInvoices":       returned,
	}
	if record.PayerOrganizationID != nil {
		payload["payerOrganizationId"] = *record.PayerOrganizationID
	}
	_, _, err := outbox.Publish(ctx, tx, outbox.Event{
		TenantID:      nullUUID(rc.TenantID),
		AggregateType: domain.AggregateBatch, AggregateID: record.ID,
		Type: BatchDecidedEvent, Payload: payload,
		// The batch's own id: one batch is decided once, and a redelivered command that moved
		// nothing publishes no second event either.
		DeduplicationKey: record.ID.String(),
	})
	return err
}
