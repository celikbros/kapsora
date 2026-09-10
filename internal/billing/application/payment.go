package application

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/billing/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// The payment record: a finance fact with an external reference (WP-I7-04 section 2.3).
//
// KAPSORA transfers no money (v1.2 4.3). Somebody outside this system moved it, and this is
// what they said about it: a bank reference, an amount and a date. Two rules run through the
// file.
//
// **The sum of the records never exceeds the settlement.** The settlement row is taken with
// `SELECT ... FOR UPDATE` before anything is read, so two clerks entering a record at the same
// moment serialise here rather than each seeing a stale sum; the deferred constraint trigger of
// migration 000046 is what holds when neither of them went through this service at all.
//
// **Nothing is deleted.** A record entered wrongly becomes DISPUTED and stays on the table with
// its reference, because "the bank says it sent this and we say it did not" is exactly the
// conversation the row exists to support.

// CreatePaymentRecordInput is the createPaymentRecord command.
type CreatePaymentRecordInput struct {
	ExternalReference string
	Amount            string
	CurrencyCode      string
	PaidAt            time.Time
	Source            string
	Notes             *string
}

// CreatePaymentRecord enters one payment against a settlement and moves the settlement's own
// figure and status to match.
//
// A record that would take the sum past `payable_amount` is refused with
// `PAYMENT_EXCEEDS_SETTLEMENT` carrying the remainder — what a clerk may still enter — rather
// than only the fact that this one was too much.
func (s *Service) CreatePaymentRecord(ctx context.Context, rc identity.RequestContext,
	settlementID uuid.UUID, in CreatePaymentRecordInput,
) (SettlementView, error) {
	ve := &domain.ValidationError{}
	if !domain.ValidExternalReference(in.ExternalReference) {
		ve.Add("externalReference", "FORMAT",
			"banka referansı harf, rakam ve . _ / - karakterlerinden oluşmalı")
	}
	amount, err := benefitdomain.ParseQuantity(in.Amount)
	switch {
	case err != nil:
		ve.Add("amount", "FORMAT", "tutar ondalık sayı olmalı")
	case !amount.IsPositive():
		ve.Add("amount", "RANGE", "tutar sıfırdan büyük olmalı")
	}
	source := in.Source
	if source == "" {
		source = domain.PaymentSourceManual
	}
	if !domain.ValidPaymentSource(source) {
		ve.Add("source", "ENUM", "kaynak MANUAL ya da ERP olmalı")
	}
	if in.PaidAt.IsZero() {
		ve.Add("paidAt", "REQUIRED", "ödeme tarihi zorunlu")
	}
	if err := ve.OrNil(); err != nil {
		return SettlementView{}, err
	}

	var out SettlementView
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.requireSettlements(); err != nil {
			return err
		}
		// The lock is the whole concurrency story. Everything below reads a figure and then
		// writes one derived from it, and two callers doing that at once is exactly how a
		// settlement comes to be paid twice.
		record, err := s.settlements.LockSettlement(ctx, tx, rc.TenantID, settlementID,
			scopeOf(rc))
		if err != nil {
			return err
		}
		if err := checkProviderScope(rc, record.ProviderOrganizationID); err != nil {
			return err
		}
		if !domain.CanRecordPayment(record.Status) {
			return ErrPaymentNotAllowed
		}
		if in.CurrencyCode != "" && in.CurrencyCode != record.CurrencyCode {
			return fieldError("currencyCode", "MISMATCH",
				"ödeme para birimi mutabakatın para birimiyle aynı olmalı")
		}

		payable := quantityOrZero(record.PayableAmount)
		paid := quantityOrZero(record.PaidAmount)
		next := paid.Add(amount)
		if next.Cmp(payable) > 0 {
			return &PaymentExceedsError{
				SettlementID: record.ID, Amount: amount.String(),
				PaidAmount: paid.String(), PayableAmount: payable.String(),
				Remainder: payable.Sub(paid).String(), CurrencyCode: record.CurrencyCode,
			}
		}

		payment, err := s.settlements.CreatePaymentRecord(ctx, tx, rc.TenantID,
			NewPaymentRecordRow{
				SettlementID:           record.ID,
				ProviderOrganizationID: record.ProviderOrganizationID,
				ExternalReference:      in.ExternalReference, Amount: amount.String(),
				CurrencyCode: record.CurrencyCode, PaidAt: in.PaidAt.UTC(), Source: source,
				RecordedBy: actorPtr(rc.Principal.ActorID), Notes: in.Notes,
				ActorID: actorPtr(rc.Principal.ActorID),
			})
		if err != nil {
			return err
		}

		// The stored figure is recomputed from the rows rather than incremented, so it is the
		// sum of what is actually there and not the sum of what this service believes it put
		// there. The deferred trigger computes the same sum at commit and refuses the write
		// when the two disagree.
		total, err := s.settlements.SumLivePaymentRecords(ctx, tx, rc.TenantID, record.ID)
		if err != nil {
			return err
		}
		status := domain.PaidStatusFor(quantityOrZero(total), payable, record.Status)
		if err := s.settlements.SetSettlementPaidAmount(ctx, tx, rc.TenantID, record.ID,
			quantityOrZero(total).String(), status,
			actorPtr(rc.Principal.ActorID)); err != nil {
			return err
		}

		if err := s.notifyPaymentRecorded(ctx, tx, rc, record, payment, status); err != nil {
			return err
		}
		if err := s.recordSettlement(ctx, tx, rc, "settlement.payment.record", record.ID,
			map[string]any{
				"reference":          record.Reference,
				"payment_record_id":  payment.ID.String(),
				"external_reference": payment.ExternalReference,
				"amount":             payment.Amount,
				"paid_amount":        quantityOrZero(total).String(),
				"payable_amount":     record.PayableAmount,
				"settlement_status":  status,
				"source":             payment.Source,
				"currency_code":      record.CurrencyCode,
			}); err != nil {
			return err
		}
		out, err = s.loadSettlement(ctx, tx, rc, record.ID)
		return err
	})
	if err != nil {
		return SettlementView{}, err
	}
	return out, nil
}

// ListPaymentRecords answers the records entered against one settlement, oldest first.
//
// It reads the settlement first rather than the records directly, so that a caller who may not
// see the settlement is told the settlement does not exist rather than being handed an empty
// list they might read as "nothing has been paid".
func (s *Service) ListPaymentRecords(ctx context.Context, rc identity.RequestContext,
	settlementID uuid.UUID,
) ([]PaymentRecordRecord, error) {
	var out []PaymentRecordRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.requireSettlements(); err != nil {
			return err
		}
		record, err := s.settlements.GetSettlement(ctx, tx, rc.TenantID, settlementID,
			scopeOf(rc))
		if err != nil {
			return err
		}
		rows, err := s.settlements.ListPaymentRecords(ctx, tx, rc.TenantID, record.ID,
			scopeOf(rc))
		if err != nil {
			return err
		}
		if rows == nil {
			rows = []PaymentRecordRecord{}
		}
		out = rows
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
