package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/accommodation/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/outbox"
)

// The request statuses this subscriber acts on. A partially approved reservation is a
// reservation approved for fewer nights, which is still an approval: the reviewer said yes
// to four of the five nights and the authorization holds four.
const (
	requestApproved          = "APPROVED"
	requestPartiallyApproved = "PARTIALLY_APPROVED"
	requestRejected          = "REJECTED"
	requestPendingReview     = "PENDING_REVIEW"
	requestPendingDocument   = "PENDING_DOCUMENT"
)

// voucherValidityGrace is how long after check-out a booking's voucher stays usable. It is
// a day rather than zero because a guest checking out on the morning of the fifth is still
// presenting a voucher on the fifth, and a promise that ended at midnight would refuse them
// at the desk.
const voucherValidityGrace = 24 * time.Hour

// decidedPayload is the part of WP-I4-01's `service_request.decided` payload this package
// reads. Everything else in it is somebody else's business, and a consumer that
// unmarshalled the whole thing would be a consumer that broke when a field was added.
type decidedPayload struct {
	ServiceRequestID uuid.UUID `json:"serviceRequestId"`
	Status           string    `json:"status"`
}

// HandleServiceRequestDecided is the seam: a reviewer decides a reservation request on the
// request page, and the booking behind it follows without the reviewer knowing a booking
// exists.
//
// It is a subscription rather than a call from WP-I4-01 for two reasons. The reviewer's
// decision must not be able to fail because this package is slow, down, or has a bug in it.
// And WP-I4-01 must not have to know that bookings exist at all.
//
// **It is idempotent, and not by trying to be.** The outbox delivers at least once, so every
// write below is guarded by a predicate rather than by a flag: the confirmation names HOLD
// or PENDING_APPROVAL, the cancellation names the same two, and the authorization is created
// under a key derived from the booking rather than from a clock. Above all, the
// authorization **adopts** the reservation the hold already took rather than reserving the
// nights again -- so a redelivery cannot draw the member's plan down twice, and neither can
// the first delivery.
//
// An event about a request no booking hangs off is not an error and is not a warning: most
// requests are not bookings, and this handler is one of several that each recognise their own.
func (s *Service) HandleServiceRequestDecided(ctx context.Context, d outbox.Delivery) error {
	if s.bookings == nil {
		return nil
	}
	if !d.TenantID.Valid {
		return outbox.Permanent(errors.New("accommodation: a decided request event carries no tenant"))
	}
	var payload decidedPayload
	if err := json.Unmarshal(d.Payload, &payload); err != nil {
		return outbox.Permanent(fmt.Errorf("accommodation: decode decided request event: %w", err))
	}
	if payload.ServiceRequestID == uuid.Nil {
		return outbox.Permanent(errors.New("accommodation: a decided request event names no request"))
	}
	rc := systemContext(d.TenantID.UUID)

	record, found, err := s.bookingOfRequest(ctx, rc, payload.ServiceRequestID)
	if err != nil || !found {
		return err
	}
	switch payload.Status {
	case requestRejected:
		return s.refuseBooking(ctx, rc, record)
	case requestApproved, requestPartiallyApproved:
		return s.approveBooking(ctx, rc, record)
	default:
		// A return, a cancellation, or a request still waiting on a document. The booking
		// is still holding its room and still waiting for somebody to answer.
		return nil
	}
}

// systemContext is the caller this handler acts as: the tenant, and nobody in particular.
// There is no actor because there is no person -- the reviewer's decision is already
// recorded on the request, and attributing the booking's move to them would say they moved
// something they have never seen.
//
// It holds no organization scope, which is what lets it find a booking whichever provider
// runs the hotel. A worker bound to one provider would leave every other property's
// bookings in HOLD until they expired.
func systemContext(tenantID uuid.UUID) identity.RequestContext {
	return identity.RequestContext{TenantID: tenantID}
}

// bookingOfRequest finds the booking a decision belongs to, or reports that there is none.
// It reads without locking, because the writes that follow take their own locks and one of
// the steps between is an authorization, which opens a transaction of its own and must not
// be waited on with a row lock held.
func (s *Service) bookingOfRequest(ctx context.Context, rc identity.RequestContext,
	requestID uuid.UUID,
) (BookingRecord, bool, error) {
	var out BookingRecord
	found := false
	err := db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: rc.TenantID},
		func(ctx context.Context, tx pgx.Tx) error {
			record, err := s.bookings.GetBookingByRequest(ctx, tx, rc.TenantID, requestID)
			switch {
			case errors.Is(err, ErrBookingNotFound):
				return nil
			case err != nil:
				return err
			}
			out, found = record, true
			return nil
		})
	return out, found, err
}

// approveBooking is the whole of "the reservation was approved", in the order that survives
// a redelivery.
//
// The authorization is taken first, in the authorization module's own transaction, under a
// key derived from the booking. It **adopts** the reservation the hold placed: the
// authorization's line points at that same row and its deadline is moved out to the end of
// the stay, so the plan is drawn down exactly once for exactly one stay. A second delivery
// finds the authorization the first one created and adopts the same hold again, which
// changes nothing.
//
// The booking is moved second, under its own lock and under the inventory lock order, with
// the status predicate doing the work: HOLD or PENDING_APPROVAL becomes CONFIRMED, and
// anything else means somebody has already been here.
func (s *Service) approveBooking(ctx context.Context, rc identity.RequestContext, record BookingRecord) error {
	if !domain.InList(record.Status, domain.HeldBookingStatuses) {
		// A redelivery of a decision already applied, or a booking somebody released while
		// the event sat in the queue. Either way there is nothing to do, and doing it
		// again would undo whatever moved it.
		return nil
	}
	if record.ServiceRequestID == nil {
		return nil
	}

	validTo := domain.Day(record.CheckOut).Add(voucherValidityGrace)
	snapshot, err := decodeQuoteSnapshot(record.QuoteSnapshot)
	if err != nil {
		return err
	}
	hold, err := s.auths.CreateForRequest(ctx, rc, BookingAuthorizationInput{
		RequestID: *record.ServiceRequestID,
		ValidFrom: domain.Day(record.CheckIn), ValidTo: validTo,
		IdempotencyKey:            bookingAuthorizationKey(record.ID),
		AdoptReservationID:        record.EntitlementReservationID,
		AdoptReservationExpiresAt: &validTo,
		MemberAmount:              snapshot.MemberAmount,
	})
	if err != nil {
		return fmt.Errorf("accommodation: authorize booking %s: %w", record.ID, err)
	}

	return s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.bookings.LockBooking(ctx, tx, rc.TenantID, record.ID)
		if err != nil {
			return err
		}
		if !domain.InList(current.Status, domain.HeldBookingStatuses) {
			return nil
		}
		room, err := s.bookings.RoomTypeBookingContext(ctx, tx, rc.TenantID, current.RoomTypeID, nil)
		if err != nil {
			return err
		}
		policy, err := s.policySnapshot(ctx, tx, rc, current, room.PropertyTimezone)
		if err != nil {
			return err
		}
		// The nights, in stay_date order and under the same lock every hold takes. The
		// room stops being held and starts being taken in one statement, so no transaction
		// can observe it as neither.
		if _, err := s.bookings.LockInventoryNights(ctx, tx, rc.TenantID, current.RoomTypeID,
			current.CheckIn, current.LastNight()); err != nil {
			return err
		}
		if err := s.bookings.ConfirmNights(ctx, tx, rc.TenantID, current.RoomTypeID,
			current.CheckIn, current.LastNight()); err != nil {
			return err
		}
		confirmedAt := s.now().UTC()
		moved, err := s.bookings.ConfirmBookingRow(ctx, tx, rc.TenantID, current.ID, hold.ID,
			confirmedAt, policy, uuid.Nil)
		if err != nil {
			return err
		}
		if !moved {
			return nil
		}
		current.Status = domain.BookingConfirmed
		current.ConfirmedAt = &confirmedAt
		current.AuthorizationID = &hold.ID

		// The voucher exists from the moment the stay does, so the property can be told a
		// booking has proof behind it and a later reissue has something to rotate. The
		// plaintext generated here is deliberately dropped on the floor: this is a
		// background worker with nobody to hand a token to, and a token kept anywhere for
		// somebody to collect later would be a usable voucher sitting in storage, which is
		// the one thing this system does not do. The member picks up a token they can use
		// through the booking's own voucher command, which rotates this digest out.
		issued, err := s.auths.IssueVoucher(ctx, tx, rc, BookingVoucherInput{
			AuthorizationID: hold.ID, ValidFrom: domain.Day(current.CheckIn), ValidTo: validTo,
		})
		if err != nil {
			return err
		}
		if err := s.bookings.SetBookingVoucher(ctx, tx, rc.TenantID, current.ID, issued.ID, uuid.Nil); err != nil {
			return err
		}
		current.VoucherID = &issued.ID

		if err := s.record(ctx, tx, rc, ActionBookingConfirm, ResourceBooking, current.ID,
			map[string]any{
				"reference": current.Reference, "authorization": hold.ID.String(),
				"approved_nights": hold.ApprovedNights, "source": "SERVICE_REQUEST_DECISION",
			}); err != nil {
			return err
		}
		return s.notifyBookingConfirmed(ctx, tx, rc.TenantID, current, room, snapshot)
	})
}

// refuseBooking gives back the room and the nights a refused reservation was holding, once.
//
// The release is guarded the same way every other write here is: the status predicate on
// the cancellation, and the ledger's own idempotency key on the movement. A second delivery
// finds a CANCELLED booking, changes nothing, and returns success -- which is what an
// at-least-once queue needs a handler to do.
func (s *Service) refuseBooking(ctx context.Context, rc identity.RequestContext, record BookingRecord) error {
	if !domain.InList(record.Status, domain.HeldBookingStatuses) {
		return nil
	}
	return s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.bookings.LockBooking(ctx, tx, rc.TenantID, record.ID)
		if err != nil {
			return err
		}
		if !domain.InList(current.Status, domain.HeldBookingStatuses) {
			return nil
		}
		if err := s.giveBackRoom(ctx, tx, rc, current, reasonRequestRefused); err != nil {
			return err
		}
		moved, err := s.bookings.CancelBookingRow(ctx, tx, rc.TenantID, current.ID, s.now().UTC(),
			domain.CancelReasonRequestRejected, uuid.Nil)
		if err != nil || !moved {
			return err
		}
		if err := s.record(ctx, tx, rc, ActionBookingCancel, ResourceBooking, current.ID,
			map[string]any{
				"reference": current.Reference, "reason_code": domain.CancelReasonRequestRejected,
				"source": "SERVICE_REQUEST_DECISION",
			}); err != nil {
			return err
		}
		return s.notifyBookingCancelled(ctx, tx, rc.TenantID, current,
			domain.CancelReasonRequestRejected, "0")
	})
}

// bookingAuthorizationKey is the idempotency key the hold is created under. It is derived
// from the booking rather than from a clock, so replaying a decision can never take a second
// authorization -- and, with the adoption above, can never take a second reservation either.
func bookingAuthorizationKey(bookingID uuid.UUID) string { return "booking:" + bookingID.String() }

// BookingDecidedHandler is the handler kapsora-worker registers for the decided-request
// event. It is a method value rather than the method itself so cmd/worker names the event
// and the handler in one line, the way every other subscription there reads.
func (s *Service) BookingDecidedHandler() outbox.HandlerFunc {
	return s.HandleServiceRequestDecided
}

// IssueBookingVoucher hands the member the code they show at the desk, and returns the
// plaintext exactly once.
//
// It is a rotation and not a read. There is no command anywhere in this system that reads a
// voucher back out, because there is nothing to read: `service.voucher` holds a SHA-256
// digest and a masked tail, and the plaintext exists only in the response body of the
// command that generated it. So a member who lost their code does not fetch it again -- they
// are given a new one, and the old digest stops working in the same transaction.
//
// The route this is served on carries no Idempotency-Key, and that is not an oversight: the
// middleware persists response bodies for replay, and this response is the one place in the
// system a usable token exists. A replayed reissue mints a new token; the previous one stops
// working; nobody ends up holding two.
func (s *Service) IssueBookingVoucher(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID,
) (IssuedBookingVoucher, error) {
	if s.bookings == nil {
		return IssuedBookingVoucher{}, ErrBookingNotFound
	}
	var out IssuedBookingVoucher
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		record, err := s.bookings.GetBooking(ctx, tx, rc.TenantID, id, personBoundary(rc), scopeOf(rc))
		if err != nil {
			return err
		}
		if record.Status != domain.BookingConfirmed && record.Status != domain.BookingCheckedIn {
			return ErrBookingTransitionInvalid
		}
		if record.AuthorizationID == nil {
			return ErrVoucherNotAvailable
		}
		issued, err := s.auths.IssueVoucher(ctx, tx, rc, BookingVoucherInput{
			AuthorizationID: *record.AuthorizationID,
			ValidFrom:       domain.Day(record.CheckIn),
			ValidTo:         domain.Day(record.CheckOut).Add(voucherValidityGrace),
			Replace:         true, RevokeReasonCode: voucherReissueReason,
		})
		if err != nil {
			return err
		}
		if err := s.bookings.SetBookingVoucher(ctx, tx, rc.TenantID, record.ID, issued.ID,
			rc.Principal.ActorID); err != nil {
			return err
		}
		// The audit detail names the voucher row and nothing that resembles the token.
		// audit.SanitizeDetail would drop a key containing "token" anyway; not writing one
		// is the half that does not depend on it.
		if err := s.record(ctx, tx, rc, ActionBookingVoucherIssue, ResourceBooking, record.ID,
			map[string]any{"reference": record.Reference, "voucher_id": issued.ID.String()}); err != nil {
			return err
		}
		out = issued
		return nil
	})
	if err != nil {
		return IssuedBookingVoucher{}, err
	}
	return out, nil
}
