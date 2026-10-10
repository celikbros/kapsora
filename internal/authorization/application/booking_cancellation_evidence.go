package application

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/authorization/domain"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
)

// BookingCancellationEvidence is a locked, single-line view of the original booking
// hold. A nil result means that exact reporting cannot be proven; it does not alter
// the already established cancellation settlement rule.
type BookingCancellationEvidence struct {
	Approved    benefitdomain.Quantity
	Consumed    benefitdomain.Quantity
	Remaining   benefitdomain.Quantity
	UnitFactor  benefitdomain.Quantity
	Reservation ledger.Reservation
}

type BookingCancellationEvidenceInput struct {
	TenantID, AuthorizationID, RequestID, PersonID uuid.UUID
	BookingID, ReservationID, ServiceDefinitionID  uuid.UUID
}

// BookingCancellationEvidence locks authorization, item and reservation in the same
// order as Consume. The caller's transaction retains those locks through settlement,
// so before/after reservation deltas cannot include somebody else's movement.
func (s *Service) BookingCancellationEvidence(ctx context.Context, tx pgx.Tx,
	in BookingCancellationEvidenceInput,
) (*BookingCancellationEvidence, error) {
	if in.TenantID == uuid.Nil || in.AuthorizationID == uuid.Nil ||
		in.RequestID == uuid.Nil || in.PersonID == uuid.Nil || in.BookingID == uuid.Nil ||
		in.ReservationID == uuid.Nil || in.ServiceDefinitionID == uuid.Nil {
		return nil, nil
	}
	auth, err := s.repo.LockAuthorization(ctx, tx, in.TenantID, in.AuthorizationID, Scope{})
	if errors.Is(err, ErrAuthorizationNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !domain.Open(auth.Status) || auth.RequestID != in.RequestID {
		return nil, nil
	}
	request, err := s.repo.GetRequest(ctx, tx, in.TenantID, auth.RequestID, Scope{})
	if errors.Is(err, ErrRequestNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if request.PersonID != in.PersonID {
		return nil, nil
	}
	items, err := s.repo.ListAuthorizationItems(ctx, tx, in.TenantID, auth.ID, true)
	if err != nil {
		return nil, err
	}
	if len(items) != 1 || items[0].ServiceDefinitionID != in.ServiceDefinitionID ||
		items[0].ReservationID == nil || *items[0].ReservationID != in.ReservationID {
		return nil, nil
	}
	item := items[0]
	approved, approvedErr := benefitdomain.ParseQuantity(item.ApprovedQuantity)
	consumed, consumedErr := benefitdomain.ParseQuantity(item.ConsumedQuantity)
	factor, factorErr := benefitdomain.ParseQuantity(item.EntitlementUnitFactor)
	remaining, remainingErr := remainderOf(item)
	if approvedErr != nil || consumedErr != nil || factorErr != nil || remainingErr != nil ||
		!approved.IsPositive() || !factor.IsPositive() || consumed.IsNegative() ||
		consumed.Cmp(approved) > 0 || approved.Sub(consumed).Cmp(remaining) != 0 {
		return nil, nil
	}
	reservation, err := s.ledger.LockReservation(ctx, tx, in.TenantID, in.ReservationID)
	if errors.Is(err, ledger.ErrReservationNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if reservation.ReferenceType != ledger.ReferenceBooking || reservation.ReferenceID != in.BookingID ||
		reservation.Remaining().Cmp(approved.Mul(factor).Sub(consumed.Mul(factor))) != 0 {
		return nil, nil
	}
	return &BookingCancellationEvidence{Approved: approved, Consumed: consumed,
		Remaining: remaining, UnitFactor: factor, Reservation: reservation}, nil
}
