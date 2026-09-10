package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/claim/domain"
	"github.com/celikbros/kapsora/internal/platform/outbox"
)

// The transitions the payer's icmal decision causes, and the only commands in this package
// besides MarkInvoiced and ReleaseFromInvoice that run inside somebody else's transaction.
//
// They live here rather than in the billing module for the reason `invoiced.go` gives: what an
// adjustment on a claim may be is this module's rule, and WP-I7-03 owns only *when* it
// happens. A billing package that wrote `claim.adjustment` rows itself would be a second
// implementation of the split, the reversal chain and the currency — and the second
// implementation is the one that would eventually disagree.
//
// Neither opens a transaction. They are given the caller's, so a cut that was written and a
// decision that failed to commit is a state no reader ever observes.
//
// Neither writes an audit row either. The audit of an icmal decision is the batch's — one row
// naming the actor, the reason and both amounts — and a second row per claim would be fifty
// rows saying the same thing in a log somebody has to read.

// InvoiceCut is one claim's share of a cut the payer applied to an invoice.
//
// The amount is the caller's, and that is deliberate: WP-I7-03 divides the cut across the
// invoice's claims in proportion to their allocations, exactly, and this package must not
// second-guess a split that has already been made to add up.
type InvoiceCut struct {
	ClaimID uuid.UUID
	// Amount is what comes off this claim, an exact decimal, never negative.
	Amount     string
	ReasonCode string
	ReasonText *string
}

// InvoiceReversal names one adjustment to take back.
type InvoiceReversal struct {
	ClaimID uuid.UUID
	// AdjustmentID is the row being reversed. The amount is read off it rather than supplied,
	// which is what makes "a reversal restores the total exactly" a property of the code.
	AdjustmentID uuid.UUID
	ReasonCode   string
	ReasonText   *string
}

// AdjustmentRef is one row that was written, named so the caller can find it again.
type AdjustmentRef struct {
	ClaimID      uuid.UUID
	AdjustmentID uuid.UUID
	// Amount is the row's own amount as it was written, exact decimal text. A reversal's is
	// negative, which is what it means.
	Amount string
}

// CutReasonCodes is the closed list a cut adjustment's reason has to be in. It is answered by
// the service rather than read from the domain by the caller, so a module that only knows this
// one through a port does not have to import the claim's vocabulary to offer a reviewer a
// choice — and cannot end up with a stale copy of the list.
func (s *Service) CutReasonCodes() []string { return domain.CutReasons }

// CutAcrossClaims writes one CUT adjustment per claim, inside the caller's transaction.
//
// Every claim has to take its share. A decision that cut four of five claims would leave the
// fifth billing a figure the payer has refused, so a claim that cannot take its share fails the
// whole command and the decision is not recorded.
//
// The claim does not have to be `Decided` here, and that is the difference from
// `CreateAdjustment`: by the time an icmal is reviewed the claims are INVOICED, which is the
// status the invoice put them in. What is still required is that they are on a document at all
// — a cut against a draft is somebody editing a statement nobody has answered yet.
func (s *Service) CutAcrossClaims(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	actorID *uuid.UUID, cuts []InvoiceCut,
) ([]AdjustmentRef, error) {
	if actorID == nil {
		// `ck_claim_adjustment_actor`: only the two system sources may have no person behind
		// them, and a reviewer's cut is never one of them.
		return nil, fieldError("actor", "REQUIRED", "kesintiyi kaydeden kullanıcı belirlenemedi")
	}
	out := make([]AdjustmentRef, 0, len(cuts))
	for _, cut := range cuts {
		amount, err := quantityOf(cut.Amount, "amount")
		if err != nil {
			return nil, err
		}
		if amount.IsNegative() {
			return nil, fieldError("amount", "RANGE", "kesinti tutarı negatif olamaz")
		}
		if amount.IsZero() {
			// A share that rounded to nothing is not an adjustment. Writing a zero row would
			// put a line in a provider's ledger that says the payer took nothing off them.
			continue
		}
		if !domain.KnownCutReason(cut.ReasonCode) {
			return nil, fieldError("reasonCode", "ENUM", "tanımlı bir kesinti gerekçesi olmalı")
		}
		if cut.ReasonText != nil && len([]rune(*cut.ReasonText)) > domain.MaxReasonText {
			return nil, fieldError("reasonText", "LENGTH", "en fazla 1000 karakter")
		}

		record, version, currency, err := s.claimForAdjustment(ctx, tx, tenantID, cut.ClaimID)
		if err != nil {
			return nil, err
		}
		written, err := s.repo.CreateAdjustment(ctx, tx, tenantID, NewAdjustmentRow{
			ClaimID: record.ID, VersionNo: version.VersionNo,
			AdjustmentType: domain.AdjustmentCut, Amount: amount.String(),
			// The whole of a reviewer's cut is the payer's own money: it is what the payer
			// took off what it would have paid the provider, and the member's share of it is
			// nothing. `ck_claim_adjustment_split` holds the two halves to the total.
			PayerAmount: amount.String(), MemberAmount: zero().String(),
			CurrencyCode: currency, ReasonCode: cut.ReasonCode,
			ReasonText: trimmedPtr(cut.ReasonText),
			SourceType: domain.AdjustmentSourceReview, ActorID: actorID,
		})
		if err != nil {
			return nil, err
		}
		if err := s.publishAdjustedRow(ctx, tx, tenantID, record.ID, written); err != nil {
			return nil, err
		}
		out = append(out, AdjustmentRef{
			ClaimID: record.ID, AdjustmentID: written.ID, Amount: written.Amount,
		})
	}
	return out, nil
}

// ReverseInvoiceCuts takes back the adjustments an earlier decision wrote.
//
// Nothing about the amount comes from the caller: the row being reversed is read and negated,
// which is what makes "a changed decision puts the claim back exactly where it was" a property
// of the code rather than of arithmetic somebody did in a form. `uq_claim_adjustment_reversal`
// refuses a second reversal of one row, so a decision changed twice cannot give the money back
// twice.
func (s *Service) ReverseInvoiceCuts(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	actorID *uuid.UUID, reversals []InvoiceReversal,
) ([]AdjustmentRef, error) {
	if actorID == nil {
		return nil, fieldError("actor", "REQUIRED", "düzeltmeyi geri alan kullanıcı belirlenemedi")
	}
	out := make([]AdjustmentRef, 0, len(reversals))
	for _, reversal := range reversals {
		reasonCode := reversal.ReasonCode
		if reasonCode == "" {
			reasonCode = domain.ReasonReviewReversed
		}
		if !domain.KnownAdjustmentReason(reasonCode) {
			return nil, fieldError("reasonCode", "ENUM", "tanımlı bir düzeltme gerekçesi olmalı")
		}
		original, err := s.repo.GetAdjustment(ctx, tx, tenantID, reversal.AdjustmentID)
		if err != nil {
			return nil, err
		}
		if original.ClaimID != reversal.ClaimID {
			return nil, ErrAdjustmentNotFound
		}
		if original.AdjustmentType == domain.AdjustmentReversal {
			return nil, ErrAdjustmentNotReversible
		}
		record, version, _, err := s.claimForAdjustment(ctx, tx, tenantID, original.ClaimID)
		if err != nil {
			return nil, err
		}
		amount, err := quantityOf(original.Amount, "amount")
		if err != nil {
			return nil, err
		}
		payer, err := quantityOf(original.PayerAmount, "payerAmount")
		if err != nil {
			return nil, err
		}
		member, err := quantityOf(original.MemberAmount, "memberAmount")
		if err != nil {
			return nil, err
		}
		written, err := s.repo.CreateAdjustment(ctx, tx, tenantID, NewAdjustmentRow{
			ClaimID: record.ID, VersionNo: version.VersionNo,
			// The line travels with it, so a reversed line-level cut is still about that line.
			ClaimLineID:    original.ClaimLineID,
			AdjustmentType: domain.AdjustmentReversal,
			Amount:         zero().Sub(amount).String(),
			PayerAmount:    zero().Sub(payer).String(),
			MemberAmount:   zero().Sub(member).String(),
			CurrencyCode:   original.CurrencyCode, ReasonCode: reasonCode,
			ReasonText:           trimmedPtr(reversal.ReasonText),
			SourceType:           domain.AdjustmentSourceManual,
			ReversesAdjustmentID: &original.ID, ActorID: actorID,
		})
		if err != nil {
			return nil, err
		}
		if err := s.publishAdjustedRow(ctx, tx, tenantID, record.ID, written); err != nil {
			return nil, err
		}
		out = append(out, AdjustmentRef{
			ClaimID: record.ID, AdjustmentID: written.ID, Amount: written.Amount,
		})
	}
	return out, nil
}

// CloseUnpaid finishes the claims of an invoice the payer rejected.
//
// Every claim has to move, for the reason MarkInvoiced gives: a rejection that closed four of
// five claims would leave the fifth sitting on a rejected document, and the provider's earnings
// view would go on counting it as money in flight.
func (s *Service) CloseUnpaid(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	actorID *uuid.UUID, claims []uuid.UUID,
) error {
	for _, id := range claims {
		moved, err := s.repo.SetInvoiceStatus(ctx, tx, tenantID, id,
			domain.StatusClosedUnpaid, []string{domain.StatusInvoiced}, actorID)
		if err != nil {
			return err
		}
		if !moved {
			return fmt.Errorf("%w: %s", ErrTransitionInvalid, id)
		}
	}
	return nil
}

// ReopenUnpaid puts them back on the invoice when the rejection is withdrawn.
//
// It is the exact inverse of CloseUnpaid and exists for one reason: a decision may be changed
// while the batch is under review, and a rejection somebody took back must leave the claims
// where the invoice put them rather than where the rejection left them.
func (s *Service) ReopenUnpaid(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	actorID *uuid.UUID, claims []uuid.UUID,
) error {
	for _, id := range claims {
		moved, err := s.repo.SetInvoiceStatus(ctx, tx, tenantID, id,
			domain.StatusInvoiced, []string{domain.StatusClosedUnpaid}, actorID)
		if err != nil {
			return err
		}
		if !moved {
			return fmt.Errorf("%w: %s", ErrTransitionInvalid, id)
		}
	}
	return nil
}

// claimForAdjustment reads the three things an adjustment row needs about its claim: the claim
// itself, its current version and the currency its lines are denominated in.
func (s *Service) claimForAdjustment(ctx context.Context, tx pgx.Tx, tenantID, claimID uuid.UUID,
) (ClaimRecord, VersionRecord, string, error) {
	record, err := s.repo.LockClaim(ctx, tx, tenantID, claimID, Scope{})
	if err != nil {
		return ClaimRecord{}, VersionRecord{}, "", err
	}
	version, err := s.repo.GetVersion(ctx, tx, tenantID, claimID, record.CurrentVersionNo)
	if err != nil {
		return ClaimRecord{}, VersionRecord{}, "", err
	}
	lines, err := s.repo.ListLines(ctx, tx, tenantID, version.ID)
	if err != nil {
		return ClaimRecord{}, VersionRecord{}, "", err
	}
	return record, version, currencyOf(lines), nil
}

// publishAdjustedRow writes the `claim.adjusted` outbox row an adjustment owes when the command
// that caused it belongs to another module and there is no request context to read a tenant off.
//
// The payload is identical to the one `CreateAdjustment` publishes, because it is the same
// fact: money moved on a decided claim. A consumer must not be able to tell which command
// wrote the row.
func (s *Service) publishAdjustedRow(ctx context.Context, tx pgx.Tx, tenantID, claimID uuid.UUID,
	row AdjustmentRecord,
) error {
	_, _, err := outbox.Publish(ctx, tx, outbox.Event{
		TenantID:      nullUUID(tenantID),
		AggregateType: adjustedAggregate, AggregateID: claimID, Type: AdjustedEvent,
		Payload: map[string]any{
			"claimId":        claimID,
			"adjustmentId":   row.ID,
			"adjustmentType": row.AdjustmentType,
			"amount":         row.Amount,
			"payerAmount":    row.PayerAmount,
			"currencyCode":   row.CurrencyCode,
			"reasonCode":     row.ReasonCode,
			"versionNo":      row.VersionNo,
		},
		DeduplicationKey: row.ID.String(),
	})
	return err
}
