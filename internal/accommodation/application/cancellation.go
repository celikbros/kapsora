package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/accommodation/domain"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	"github.com/celikbros/kapsora/internal/identity"
)

// reasonCodeShape is ck_cancellation_reason, spelled here so a caller gets a field error
// naming what is wrong instead of a constraint violation nobody can read.
var reasonCodeShape = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)

// CancellationView is the answer both the preview and the command give: what it costs, what
// comes back, and -- once it has happened -- the row that records it.
type CancellationView struct {
	Booking BookingView
	Quote   CancellationQuote
	// Record is the written cancellation. It is nil on a preview, which is exactly the
	// difference between the two: one of them changed nothing.
	Record *CancellationRecord
}

// PreviewCancellation answers what a cancellation now would cost, without changing anything.
//
// It is a command in the API and a read here, and that shape is deliberate: the member is
// shown the fee before they agree to it, and the figures they are shown are computed by the
// same function the cancellation itself calls, on the same frozen document. A preview that
// estimated and a command that decided would be two answers to one question, and the member
// would find out which one was real from an invoice.
func (s *Service) PreviewCancellation(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID,
) (CancellationView, error) {
	if s.bookings == nil {
		return CancellationView{}, ErrBookingNotFound
	}
	var out CancellationView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		record, err := s.bookings.GetBooking(ctx, tx, rc.TenantID, id, personBoundary(rc), scopeOf(rc))
		if err != nil {
			return err
		}
		if err := cancellable(record); err != nil {
			return err
		}
		view, err := s.loadBooking(ctx, tx, rc.TenantID, record)
		if err != nil {
			return err
		}
		quote, err := s.quoteCancellation(record, view.Nights)
		if err != nil {
			return err
		}
		out = CancellationView{Booking: view, Quote: quote}
		return nil
	})
	if err != nil {
		return CancellationView{}, err
	}
	return out, nil
}

// quoteCancellation prices a cancellation of one booking against the policy that booking
// froze.
//
// A booking still waiting on a reviewer has agreed nothing and therefore owes nothing, and
// it has no frozen policy either -- which is the same fact seen from the column. Everything
// else is judged by QuoteCancellation, which reads the snapshot and nothing else.
func (s *Service) quoteCancellation(record BookingRecord, nights []BookingNightRecord) (
	CancellationQuote, error,
) {
	snapshot, err := decodeQuoteSnapshot(record.QuoteSnapshot)
	if err != nil {
		return CancellationQuote{}, err
	}
	currency := snapshot.CurrencyCode
	if currency == "" {
		currency = defaultCurrency
	}
	if record.Status == domain.BookingPendingApproval {
		return CancellationQuote{
			Free: true, FeeAmount: "0", PayerFee: "0", MemberFee: "0",
			ReleasedNights: snapshot.CoveredNights, CurrencyCode: currency,
		}, nil
	}
	policy, err := DecodeLodgingPolicy(record.PolicySnapshot)
	if err != nil {
		return CancellationQuote{}, err
	}
	return QuoteCancellation(policy, snapshot, nights, record.Nights, s.now().UTC(), record.CheckIn)
}

// cancellable answers whether this booking is one a cancellation may act on at all.
//
// A hold is given back with releaseHold and not cancelled: nothing was agreed, so there is
// nothing to charge and nothing to record. A guest who has arrived checks out. Everything
// else has already ended.
func cancellable(record BookingRecord) error {
	switch record.Status {
	case domain.BookingConfirmed, domain.BookingPendingApproval:
		return nil
	case domain.BookingCheckedIn, domain.BookingCompleted:
		return ErrCancellationTooLate
	default:
		return ErrBookingTransitionInvalid
	}
}

// CancelBooking calls off a stay somebody agreed to, and settles what that costs.
//
// One transaction, in an order that is not interchangeable. The inventory is given back
// under the same `stay_date` lock order every hold takes. The plan's **penalty is consumed
// before the remainder is released**, because both draw on the same reservation and a
// release that ran first would leave nothing to consume -- the ledger would hand everything
// back, and the only surviving trace that the plan paid for a room nobody slept in would be
// a money figure on a row. The money fee itself is recorded and charged nowhere: KAPSORA
// holds no card (v1.2 10.6 step 6), and M7's settlement is what turns this row into an
// invoice line.
//
// **Running it twice is one cancellation.** The status predicate on the booking's own update
// names CONFIRMED and PENDING_APPROVAL, so a second attempt moves no row and stops before
// anything is written; `uq_cancellation_booking` is underneath it if that predicate were
// ever removed; and both ledger movements carry keys derived from the booking rather than
// from a clock, so even a replay that got past both would be the same movement rather than a
// second one.
func (s *Service) CancelBooking(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	reasonCode string,
) (CancellationView, error) {
	if s.bookings == nil {
		return CancellationView{}, ErrBookingNotFound
	}
	if reasonCode == "" {
		reasonCode = CancelReasonMember
	}
	if !reasonCodeShape.MatchString(reasonCode) {
		return CancellationView{}, fieldError("reasonCode", "FORMAT",
			"büyük harf, rakam ve alt çizgiden oluşan bir kod olmalı")
	}

	// The reservation request is withdrawn before the booking's transaction is opened,
	// exactly as the authorization is taken before it on the approval path: WP-I4-01's
	// cancel is its own command with its own transaction, and calling it with this
	// booking's rows locked would hold one pooled connection while waiting for another.
	if err := s.withdrawPendingRequest(ctx, rc, id, reasonCode); err != nil {
		return CancellationView{}, err
	}

	var out CancellationView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		view, err := s.cancel(ctx, tx, rc, id, reasonCode)
		out = view
		return err
	})
	if err != nil {
		return CancellationView{}, err
	}
	return out, nil
}

// withdrawPendingRequest cancels the reservation request of a booking still waiting on a
// reviewer. A booking in any other status has nothing to withdraw.
func (s *Service) withdrawPendingRequest(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID, reasonCode string,
) error {
	var requestID *uuid.UUID
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		record, err := s.bookings.GetBooking(ctx, tx, rc.TenantID, id, personBoundary(rc), scopeOf(rc))
		if err != nil {
			return err
		}
		if err := cancellable(record); err != nil {
			return err
		}
		if record.Status == domain.BookingPendingApproval {
			requestID = record.ServiceRequestID
		}
		return nil
	})
	if err != nil || requestID == nil {
		return err
	}
	return s.requests.CancelReservation(ctx, rc, *requestID, reasonCode)
}

func (s *Service) cancel(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	id uuid.UUID, reasonCode string,
) (CancellationView, error) {
	if _, err := s.bookings.GetBooking(ctx, tx, rc.TenantID, id, personBoundary(rc), scopeOf(rc)); err != nil {
		return CancellationView{}, err
	}
	record, err := s.bookings.LockBooking(ctx, tx, rc.TenantID, id)
	if err != nil {
		return CancellationView{}, err
	}
	if err := cancellable(record); err != nil {
		return CancellationView{}, err
	}
	nights, err := s.bookings.ListBookingNights(ctx, tx, rc.TenantID, record.ID)
	if err != nil {
		return CancellationView{}, err
	}
	quote, err := s.quoteCancellation(record, nights)
	if err != nil {
		return CancellationView{}, err
	}
	snapshot, err := decodeQuoteSnapshot(record.QuoteSnapshot)
	if err != nil {
		return CancellationView{}, err
	}

	if err := s.releaseCancelledInventory(ctx, tx, rc, record); err != nil {
		return CancellationView{}, err
	}

	// The status write is the gate on everything below. It names the two statuses a
	// cancellation may act on, so a replayed command finds no row and gives nothing back a
	// second time.
	cancelledAt := s.now().UTC()
	moved, err := s.bookings.CancelConfirmedBookingRow(ctx, tx, rc.TenantID, record.ID,
		cancelledAt, reasonCode, rc.Principal.ActorID)
	if err != nil {
		return CancellationView{}, err
	}
	if !moved {
		return CancellationView{}, ErrBookingTransitionInvalid
	}

	if err := s.settleCancellation(ctx, tx, rc, record, snapshot, quote); err != nil {
		return CancellationView{}, err
	}

	policy := record.PolicySnapshot
	if len(policy) == 0 {
		// A booking cancelled while it was still waiting on a reviewer has no frozen policy
		// and owes nothing. The row still has to say what it was judged by, and an empty
		// object records exactly that: nothing had been agreed yet.
		policy = json.RawMessage(`{}`)
	}
	written, err := s.bookings.CreateCancellation(ctx, tx, rc.TenantID, NewCancellationRow{
		BookingID: record.ID, CancelledAt: cancelledAt, CancelledBy: rc.Principal.ActorID,
		ReasonCode: reasonCode, PolicySnapshot: policy, Free: quote.Free,
		PenaltyNights: quote.PenaltyNights, ReleasedNights: quote.ReleasedNights,
		FeeAmount: quote.FeeAmount, PayerFee: quote.PayerFee, MemberFee: quote.MemberFee,
		CurrencyCode: quote.CurrencyCode,
	})
	if err != nil {
		return CancellationView{}, err
	}

	if err := s.record(ctx, tx, rc, ActionBookingCancel, ResourceBooking, record.ID, map[string]any{
		"reference": record.Reference, "reason_code": reasonCode, "free": quote.Free,
		"penalty_nights": quote.PenaltyNights, "released_nights": quote.ReleasedNights,
		"fee_amount": quote.FeeAmount, "currency_code": quote.CurrencyCode,
	}); err != nil {
		return CancellationView{}, err
	}
	if err := s.notifyBookingCancelled(ctx, tx, rc.TenantID, record, reasonCode,
		quote.FeeAmount); err != nil {
		return CancellationView{}, err
	}
	// The billing side hears about every cancellation, free or not. `free` travels in the
	// payload because "this one cost nothing" is a fact a subscriber has to be able to
	// observe: an event that simply never arrived is indistinguishable from an event that
	// was lost.
	if err := s.publishCancelled(ctx, tx, rc.TenantID, record, reasonCode, quote.Free); err != nil {
		return CancellationView{}, err
	}

	cancelled := record
	cancelled.Status = domain.BookingCancelled
	cancelled.CancelledAt = &cancelledAt
	cancelled.CancelReasonCode = &reasonCode
	view, err := s.loadBooking(ctx, tx, rc.TenantID, cancelled)
	if err != nil {
		return CancellationView{}, err
	}
	return CancellationView{Booking: view, Quote: quote, Record: &written}, nil
}

// releaseCancelledInventory gives the room back under the same `stay_date` lock order every
// hold takes.
//
// Which counter moves depends on what the booking was. A stay that was agreed is counted in
// `confirmed`; one still waiting on a reviewer is counted in `held`. Decrementing the wrong
// one would leave the allotment right in total and wrong on every night, and the row's own
// `held + confirmed <= capacity` would not notice.
func (s *Service) releaseCancelledInventory(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, record BookingRecord,
) error {
	if _, err := s.bookings.LockInventoryNights(ctx, tx, rc.TenantID, record.RoomTypeID,
		record.CheckIn, record.LastNight()); err != nil {
		return err
	}
	if domain.InList(record.Status, domain.HeldBookingStatuses) {
		return s.bookings.AddHeld(ctx, tx, rc.TenantID, record.RoomTypeID,
			record.CheckIn, record.LastNight(), -1)
	}
	return s.bookings.AddConfirmed(ctx, tx, rc.TenantID, record.RoomTypeID,
		record.CheckIn, record.LastNight(), -1)
}

// settleCancellation moves the plan: the penalty is spent and the rest is given back.
//
// The order is the whole of it. Both movements draw on the same reservation, so consuming
// first and releasing second is what makes "the plan paid for the room the member did not
// use" true in the ledger as well as on the invoice. Releasing first would hand everything
// back and leave the consume with nothing to take, which is precisely the disagreement
// between the ledger and the settlement this system exists to make impossible.
func (s *Service) settleCancellation(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record BookingRecord, snapshot QuoteSnapshot, quote CancellationQuote,
) error {
	if record.AuthorizationID == nil {
		// A booking still waiting on a reviewer holds its nights on the hold's own
		// reservation rather than on an authorization. Giving that back is keyed by the
		// booking, so a cancellation and a later expiry sweep of the same one are one
		// release.
		return s.releaseReservationOnly(ctx, tx, rc, record, ReasonCancellationRelease)
	}
	penalty := quote.EntitlementPenalty(snapshot.CoveredNights)
	if penalty > 0 {
		if _, err := s.auths.Consume(ctx, tx, BookingConsumeInput{
			TenantID: rc.TenantID, ActorID: rc.Principal.ActorID,
			AuthorizationID:     *record.AuthorizationID,
			ServiceDefinitionID: snapshot.ServiceDefinitionID,
			Nights:              fmt.Sprintf("%d", penalty),
			Key:                 cancellationPenaltyKey(record.ID), ReasonCode: ReasonCancellationPenalty,
		}); err != nil {
			return err
		}
	}
	if quote.ReleasedNights > 0 {
		if _, err := s.auths.ReleaseUnused(ctx, tx, BookingReleaseInput{
			TenantID: rc.TenantID, ActorID: rc.Principal.ActorID,
			AuthorizationID: *record.AuthorizationID,
			Nights:          fmt.Sprintf("%d", quote.ReleasedNights),
			ReasonCode:      ReasonCancellationRelease,
		}); err != nil {
			return err
		}
	}
	// The code that would have opened the room stops working in the same transaction the
	// stay stops existing in. A cancelled booking whose voucher still redeems is a room a
	// guest can walk into with a token this system believes in.
	return s.auths.RevokeVouchers(ctx, tx, rc, *record.AuthorizationID, ReasonVoucherCancelled)
}

// releaseReservationOnly is giveBackRoom's ledger half, without its inventory half.
//
// It exists because a cancellation has already moved the counters itself -- `confirmed` for
// a stay that was agreed, `held` for one still pending -- and giveBackRoom only knows how to
// move `held`. The key and the benign refusals are the same, so the two paths give the
// entitlement back exactly once between them.
func (s *Service) releaseReservationOnly(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, record BookingRecord, reason string,
) error {
	if record.EntitlementReservationID == nil {
		return nil
	}
	quantity, err := benefitdomain.ParseQuantity(fmt.Sprintf("%d", record.Nights))
	if err != nil {
		return fmt.Errorf("accommodation: night quantity: %w", err)
	}
	_, err = s.ledger.Release(ctx, tx, ledger.MovementInput{
		TenantID: rc.TenantID, ReservationID: *record.EntitlementReservationID,
		Quantity: quantity, Key: bookingReleaseKey(record.ID, reason),
		ReasonCode: reason, ActorID: rc.Principal.ActorID,
	})
	switch {
	case errors.Is(err, ledger.ErrIdempotentReplay), errors.Is(err, ledger.ErrReservationClosed),
		errors.Is(err, ledger.ErrQuantityRemainder):
		// The entitlement is already where it belongs. Refusing here would leave a booking
		// nobody can cancel.
		return nil
	default:
		return err
	}
}

// The ledger idempotency keys of the two penalties and of the stay itself. All three are
// derived from the booking rather than from a clock, so a replayed command moves the ledger
// once; and all three differ from each other, so a booking that was cancelled, one that was
// a no-show and one that was slept in are three movements rather than one that the others
// silently swallow.
func cancellationPenaltyKey(bookingID uuid.UUID) string {
	return "booking-cancel-penalty:" + bookingID.String()
}

func noShowPenaltyKey(bookingID uuid.UUID) string {
	return "booking-no-show-penalty:" + bookingID.String()
}
