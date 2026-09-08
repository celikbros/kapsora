package application

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/claim/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/outbox"
)

// AdjustedEvent is the outbox event every adjustment publishes. It is what a settlement, a
// report and M7's invoice hang off: money moved on a decided claim, and the figure the
// provider was going to be paid is no longer the figure the lines add up to.
const AdjustedEvent = "claim.adjusted"

// adjustedAggregate is the aggregate type the event names.
const adjustedAggregate = "claim"

// AdjustmentInput is the createClaimAdjustment command.
//
// **A reversal supplies no figures.** It names the adjustment it takes back and a reason, and
// the amount and the split are read off the row it reverses. That is not a convenience: it is
// what makes "a reversal restores the approved total exactly" a property of the code rather
// than of whoever typed the numbers, and it is why a reversal cannot be a reversal for a
// slightly different amount.
type AdjustmentInput struct {
	AdjustmentType string
	Amount         string
	PayerAmount    string
	MemberAmount   string
	CurrencyCode   string
	ReasonCode     string
	ReasonText     *string
	// LineNo names a line of the claim's current version, for a line-level adjustment. Nil
	// is the claim-level case and is ordinary: a recovery of an overpayment is about the
	// claim, not about one of its lines.
	LineNo *int
	// ReversesAdjustmentID makes this a REVERSAL of that row and nothing else.
	ReversesAdjustmentID *uuid.UUID
}

// AdjustmentView is one adjustment with the claim's totals as they stand after it.
type AdjustmentView struct {
	Adjustment AdjustmentRecord
	Readiness  InvoiceReadiness
}

// CreateAdjustment records money that moved on a decided claim, with its split, its reason and
// — when it is one — the line it belongs to.
//
// Three things are refused here and each of them is refused underneath as well, which is the
// pairing this codebase uses everywhere: the service answers with a sentence naming the field,
// and the database answers whatever reaches the table.
//
//   - the split. `payerAmount + memberAmount` is `amount`, exactly, in exact decimals
//     (`ck_claim_adjustment_split`);
//   - the reversal chain. One reversal per adjustment (`uq_claim_adjustment_reversal`), and
//     never a reversal of a reversal (`tg_claim_adjustment_links`);
//   - the currency. One claim is denominated once, and an adjustment in another currency
//     would be a total nobody can add up.
//
// The claim has to have been decided. An adjustment on a draft is not a cut — it is somebody
// editing a statement nobody has answered yet, and the way to change a draft is to change the
// draft.
func (s *Service) CreateAdjustment(ctx context.Context, rc identity.RequestContext,
	claimID uuid.UUID, in AdjustmentInput,
) (AdjustmentView, error) {
	var out AdjustmentView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		record, err := s.repo.LockClaim(ctx, tx, rc.TenantID, claimID, scopeOf(rc))
		if err != nil {
			return err
		}
		if !domain.Decided(record.Status) {
			return ErrNotDecided
		}
		version, err := s.repo.GetVersion(ctx, tx, rc.TenantID, claimID, record.CurrentVersionNo)
		if err != nil {
			return err
		}
		lines, err := s.repo.ListLines(ctx, tx, rc.TenantID, version.ID)
		if err != nil {
			return err
		}
		currency := currencyOf(lines)

		row, err := s.adjustmentRow(ctx, tx, rc, record, version, lines, currency, in)
		if err != nil {
			return err
		}
		written, err := s.repo.CreateAdjustment(ctx, tx, rc.TenantID, row)
		if err != nil {
			return err
		}
		out.Adjustment = written

		if err := s.record(ctx, tx, rc, "claim.adjust", claimID, map[string]any{
			"reference": record.Reference, "version_no": row.VersionNo,
			"adjustment_id": written.ID.String(), "adjustment_type": written.AdjustmentType,
			"amount": written.Amount, "payer_amount": written.PayerAmount,
			"currency_code": written.CurrencyCode, "reason_code": written.ReasonCode,
			"source_type": written.SourceType,
		}); err != nil {
			return err
		}
		if err := s.publishAdjusted(ctx, tx, rc, record, written); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return AdjustmentView{}, err
	}
	// The totals are answered by the readiness endpoint's own code, in its own transaction,
	// so the figure a caller reads back after a command is produced by the same function the
	// figure they read before it was. Two sums would be two answers.
	readiness, err := s.InvoiceReadiness(ctx, rc, claimID)
	if err != nil {
		return AdjustmentView{}, err
	}
	out.Readiness = readiness
	return out, nil
}

// adjustmentRow turns the command into the row, which is where the difference between a
// reversal and everything else lives.
func (s *Service) adjustmentRow(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record ClaimRecord, version VersionRecord, lines []LineRecord, currency string,
	in AdjustmentInput,
) (NewAdjustmentRow, error) {
	actor := actorPtr(rc.Principal.ActorID)
	if actor == nil {
		// `ck_claim_adjustment_actor`: only the two system sources may have no person behind
		// them, and this command is never one of them.
		return NewAdjustmentRow{}, fieldError("actor", "REQUIRED",
			"düzeltmeyi kaydeden kullanıcı belirlenemedi")
	}

	if in.ReversesAdjustmentID != nil {
		return s.reversalRow(ctx, tx, rc, record, version, in, actor)
	}

	if err := domain.ValidateAdjustment(domain.NewAdjustment{
		AdjustmentType: in.AdjustmentType, Amount: in.Amount, PayerAmount: in.PayerAmount,
		MemberAmount: in.MemberAmount, CurrencyCode: in.CurrencyCode,
		ReasonCode: in.ReasonCode, ReasonText: in.ReasonText,
	}); err != nil {
		return NewAdjustmentRow{}, err
	}
	if in.CurrencyCode != "" && in.CurrencyCode != currency {
		return NewAdjustmentRow{}, ErrAdjustmentCurrency
	}
	lineID, err := lineOf(lines, in.LineNo)
	if err != nil {
		return NewAdjustmentRow{}, err
	}
	source := domain.AdjustmentSourceReview
	if in.AdjustmentType == domain.AdjustmentRecovery {
		// A recovery is money coming back rather than a reviewer's cut, and a settlement
		// reading the ledger has to be able to tell the two apart without reading the type
		// twice.
		source = domain.AdjustmentSourceRecovery
	}
	return NewAdjustmentRow{
		ClaimID: record.ID, VersionNo: version.VersionNo, ClaimLineID: lineID,
		AdjustmentType: in.AdjustmentType, Amount: in.Amount,
		PayerAmount: in.PayerAmount, MemberAmount: in.MemberAmount,
		CurrencyCode: currency, ReasonCode: in.ReasonCode,
		ReasonText: trimmedPtr(in.ReasonText), SourceType: source, ActorID: actor,
	}, nil
}

// reversalRow reads the row being taken back and negates it.
//
// Nothing about the amount comes from the caller. A reversal that could carry its own figures
// would be an edit with a different name, and "the approved total goes back to exactly where
// it was" would depend on arithmetic somebody did in a form.
func (s *Service) reversalRow(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record ClaimRecord, version VersionRecord, in AdjustmentInput, actor *uuid.UUID,
) (NewAdjustmentRow, error) {
	if !domain.KnownAdjustmentReason(in.ReasonCode) {
		return NewAdjustmentRow{}, fieldError("reasonCode", "ENUM",
			"tanımlı bir düzeltme gerekçesi olmalı")
	}
	if in.ReasonText != nil && len([]rune(*in.ReasonText)) > domain.MaxReasonText {
		return NewAdjustmentRow{}, fieldError("reasonText", "LENGTH", "en fazla 1000 karakter")
	}
	original, err := s.repo.GetAdjustment(ctx, tx, rc.TenantID, *in.ReversesAdjustmentID)
	if err != nil {
		return NewAdjustmentRow{}, err
	}
	if original.ClaimID != record.ID {
		// A reversal of another claim's row would move money on a document nobody can find
		// it from. It is answered "not found" rather than "wrong claim": that such an
		// adjustment exists at all is somebody else's business.
		return NewAdjustmentRow{}, ErrAdjustmentNotFound
	}
	if original.AdjustmentType == domain.AdjustmentReversal {
		return NewAdjustmentRow{}, ErrAdjustmentNotReversible
	}
	existing, err := s.repo.ListAdjustments(ctx, tx, rc.TenantID, record.ID)
	if err != nil {
		return NewAdjustmentRow{}, err
	}
	for _, row := range existing {
		if row.ReversesAdjustmentID != nil && *row.ReversesAdjustmentID == original.ID {
			return NewAdjustmentRow{}, ErrAdjustmentReversed
		}
	}

	amount, err := quantityOf(original.Amount, "amount")
	if err != nil {
		return NewAdjustmentRow{}, err
	}
	payer, err := quantityOf(original.PayerAmount, "payerAmount")
	if err != nil {
		return NewAdjustmentRow{}, err
	}
	member, err := quantityOf(original.MemberAmount, "memberAmount")
	if err != nil {
		return NewAdjustmentRow{}, err
	}
	negate := func(q benefitdomain.Quantity) string { return zero().Sub(q).String() }
	return NewAdjustmentRow{
		ClaimID: record.ID, VersionNo: version.VersionNo,
		// The line travels with it, so a reversed line-level cut is still about that line.
		ClaimLineID:    original.ClaimLineID,
		AdjustmentType: domain.AdjustmentReversal,
		Amount:         negate(amount), PayerAmount: negate(payer), MemberAmount: negate(member),
		CurrencyCode: original.CurrencyCode, ReasonCode: in.ReasonCode,
		ReasonText: trimmedPtr(in.ReasonText), SourceType: domain.AdjustmentSourceManual,
		ReversesAdjustmentID: &original.ID, ActorID: actor,
	}, nil
}

// lineOf resolves an optional line number against the claim's current version.
func lineOf(lines []LineRecord, lineNo *int) (*uuid.UUID, error) {
	if lineNo == nil {
		return nil, nil
	}
	for _, line := range lines {
		if line.LineNo == *lineNo {
			id := line.ID
			return &id, nil
		}
	}
	return nil, ErrAdjustmentLine
}

// ListAdjustments returns the claim's ledger, oldest first, which is the order the reversal
// chain reads in: a reversal always comes after the row it takes back.
func (s *Service) ListAdjustments(ctx context.Context, rc identity.RequestContext,
	claimID uuid.UUID,
) ([]AdjustmentRecord, error) {
	var out []AdjustmentRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetClaim(ctx, tx, rc.TenantID, claimID, scopeOf(rc)); err != nil {
			return err
		}
		rows, err := s.repo.ListAdjustments(ctx, tx, rc.TenantID, claimID)
		out = rows
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// publishAdjusted writes the outbox row an adjustment owes, inside the command's own
// transaction. The payload is identifiers, a word and two exact decimals; there is no reason
// text in it, because an outbox payload is read by every consumer.
func (s *Service) publishAdjusted(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record ClaimRecord, row AdjustmentRecord,
) error {
	_, _, err := outbox.Publish(ctx, tx, outbox.Event{
		TenantID:      nullUUID(rc.TenantID),
		AggregateType: adjustedAggregate, AggregateID: record.ID, Type: AdjustedEvent,
		Payload: map[string]any{
			"claimId":        record.ID,
			"adjustmentId":   row.ID,
			"adjustmentType": row.AdjustmentType,
			"amount":         row.Amount,
			"payerAmount":    row.PayerAmount,
			"currencyCode":   row.CurrencyCode,
			"reasonCode":     row.ReasonCode,
			"versionNo":      row.VersionNo,
		},
		// The adjustment's own id: every row is one event, and a redelivered command that
		// wrote no second row publishes no second event either.
		DeduplicationKey: row.ID.String(),
	})
	return err
}
