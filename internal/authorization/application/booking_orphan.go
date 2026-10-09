package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/authorization/domain"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	"github.com/celikbros/kapsora/internal/identity"
)

const bookingHandoffAbortReason = "BOOKING_HANDOFF_ABORTED"

var ErrBookingOrphanProvenance = errors.New("authorization: booking orphan provenance mismatch")

// BookingOrphanInput identifies one booking's original promise. The caller must already
// hold that terminal booking's row lock in tx. ExpectedAuthorizationID may be nil on a
// fresh outbox delivery after the worker died before returning from CreateForRequest.
type BookingOrphanInput struct {
	BookingID               uuid.UUID
	RequestID               uuid.UUID
	PersonID                uuid.UUID
	ReservationID           uuid.UUID
	ServiceDefinitionID     uuid.UUID
	AccountID               uuid.UUID
	ExpectedAuthorizationID uuid.UUID
	UnitFactor              string
	ReservedUnits           string
	IdempotencyKey          string
}

// RetireBookingOrphanInTx withdraws only the unused promise proven to have adopted the
// caller's released booking reservation. It never makes a ledger movement.
func (s *Service) RetireBookingOrphanInTx(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, in BookingOrphanInput,
) error {
	if in.BookingID == uuid.Nil || in.RequestID == uuid.Nil || in.PersonID == uuid.Nil ||
		in.ReservationID == uuid.Nil || in.ServiceDefinitionID == uuid.Nil ||
		in.IdempotencyKey != "booking:"+in.BookingID.String() {
		return ErrBookingOrphanProvenance
	}
	keyed, err := s.repo.GetAuthorizationByKey(ctx, tx, rc.TenantID, in.IdempotencyKey)
	if errors.Is(err, ErrAuthorizationNotFound) {
		if in.ExpectedAuthorizationID != uuid.Nil {
			return ErrBookingOrphanProvenance
		}
		return nil
	}
	if err != nil {
		return err
	}
	if in.ExpectedAuthorizationID != uuid.Nil && keyed.ID != in.ExpectedAuthorizationID {
		return ErrBookingOrphanProvenance
	}
	current, err := s.repo.LockAuthorization(ctx, tx, rc.TenantID, keyed.ID, Scope{})
	if err != nil {
		return err
	}
	if current.RequestID != in.RequestID || current.ID != keyed.ID {
		return ErrBookingOrphanProvenance
	}
	request, err := s.repo.GetRequest(ctx, tx, rc.TenantID, current.RequestID, Scope{})
	if err != nil {
		return err
	}
	if request.PersonID != in.PersonID {
		return ErrBookingOrphanProvenance
	}
	items, err := s.repo.ListAuthorizationItems(ctx, tx, rc.TenantID, current.ID, true)
	if err != nil {
		return err
	}
	if len(items) != 1 || items[0].ServiceDefinitionID != in.ServiceDefinitionID ||
		items[0].ReservationID == nil || *items[0].ReservationID != in.ReservationID {
		return ErrBookingOrphanProvenance
	}
	item := items[0]
	zero := benefitdomain.ZeroQuantity()
	consumedHeader, headerErr := benefitdomain.ParseQuantity(current.ConsumedTotal)
	consumedItem, itemErr := benefitdomain.ParseQuantity(item.ConsumedQuantity)
	if headerErr != nil || itemErr != nil || consumedHeader.Cmp(zero) != 0 || consumedItem.Cmp(zero) != 0 {
		return ErrBookingOrphanProvenance
	}
	if in.UnitFactor != "" {
		factor, expectedErr := benefitdomain.ParseQuantity(in.UnitFactor)
		stored, storedErr := benefitdomain.ParseQuantity(item.EntitlementUnitFactor)
		if expectedErr != nil || storedErr != nil || !factor.IsPositive() || factor.Cmp(stored) != 0 {
			return ErrBookingOrphanProvenance
		}
	}
	evidence, err := s.repo.BookingOrphanEvidence(ctx, tx, rc.TenantID, current.ID, in.ReservationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrBookingOrphanProvenance
	}
	if err != nil {
		return err
	}
	quantity, quantityErr := benefitdomain.ParseQuantity(evidence.Quantity)
	released, releasedErr := benefitdomain.ParseQuantity(evidence.ReleasedQuantity)
	consumedReservation, consumedErr := benefitdomain.ParseQuantity(evidence.ConsumedQuantity)
	if quantityErr != nil || releasedErr != nil || consumedErr != nil ||
		evidence.ReferenceType != ledger.ReferenceBooking || evidence.ReferenceID != in.BookingID ||
		(in.AccountID != uuid.Nil && evidence.AccountID != in.AccountID) ||
		!quantity.IsPositive() || consumedReservation.Cmp(zero) != 0 ||
		quantity.Sub(released).Cmp(zero) != 0 ||
		(evidence.Status != ledger.ReservationReleased && evidence.Status != ledger.ReservationExpired) ||
		evidence.BookingLinks != 0 || evidence.Fulfilments != 0 ||
		evidence.RedeemedVouchers != 0 || evidence.ConsumptionMovements != 0 {
		return ErrBookingOrphanProvenance
	}
	if in.ReservedUnits != "" {
		expected, expectedErr := benefitdomain.ParseQuantity(in.ReservedUnits)
		if expectedErr != nil || expected.Cmp(quantity) != 0 {
			return ErrBookingOrphanProvenance
		}
	}
	if current.Status == domain.StatusCancelled {
		return nil
	}
	if current.Status != domain.StatusActive {
		return fmt.Errorf("%w: authorization status %s", ErrBookingOrphanProvenance, current.Status)
	}
	if err := s.repo.CancelAuthorization(ctx, tx, rc.TenantID, current.ID,
		bookingHandoffAbortReason, actorPtr(rc.Principal.ActorID), current.RowVersion); err != nil {
		return err
	}
	if err := s.revokeLiveVouchers(ctx, tx, rc, current.ID, bookingHandoffAbortReason); err != nil {
		return err
	}
	return s.record(ctx, tx, rc, "authorization.cancel", "authorization", current.ID,
		map[string]any{"reason_code": bookingHandoffAbortReason, "released_total": "0",
			"source": "BOOKING_HANDOFF"})
}
