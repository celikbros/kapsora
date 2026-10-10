package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
)

// BookingPenaltyInput settles only the original, proven NIGHT booking hold. PolicyPenalty
// is the frozen charge quantity; the actual entitlement draw is capped by approval.
type BookingPenaltyInput struct {
	BookingCancellationEvidenceInput
	EvaluationID, EnrollmentID               uuid.UUID
	ActorID                                  uuid.UUID
	PolicyPenalty                            benefitdomain.Quantity
	ConsumeKey, ConsumeReason, ReleaseReason string
}

type BookingPenaltyResult struct {
	ConsumedService, ReleasedService benefitdomain.Quantity
	ConsumedUnits, ReleasedUnits     benefitdomain.Quantity
}

var ErrBookingPenaltyEvidence = errors.New("authorization: booking penalty requires an open, consistent original NIGHT hold")

type bookingNightProofReader interface {
	BookingNightProvenance(ctx context.Context, tx pgx.Tx, tenantID, evaluationID,
		reservationID, serviceDefinitionID, personID, enrollmentID, bookingID, accountID uuid.UUID,
		factor benefitdomain.Quantity) (bool, error)
}

// SettleBookingPenalty owns the authorization, item and reservation locks throughout
// the terminal movement. It never changes generic Consume's over-consumption rule.
func (s *Service) SettleBookingPenalty(ctx context.Context, tx pgx.Tx, in BookingPenaltyInput) (BookingPenaltyResult, error) {
	e, err := s.BookingCancellationEvidence(ctx, tx, in.BookingCancellationEvidenceInput)
	if err != nil {
		return BookingPenaltyResult{}, err
	}
	if e == nil || in.PolicyPenalty.IsNegative() || in.EvaluationID == uuid.Nil ||
		in.EnrollmentID == uuid.Nil ||
		in.ConsumeReason == "" || in.ReleaseReason == "" || in.ConsumeKey == "" {
		return BookingPenaltyResult{}, ErrBookingPenaltyEvidence
	}
	proofReader, ok := s.repo.(bookingNightProofReader)
	if !ok {
		return BookingPenaltyResult{}, ErrBookingPenaltyEvidence
	}
	proven, err := proofReader.BookingNightProvenance(ctx, tx, in.TenantID, in.EvaluationID,
		in.ReservationID, in.ServiceDefinitionID, in.PersonID, in.EnrollmentID, in.BookingID,
		e.Reservation.AccountID, e.UnitFactor)
	if err != nil {
		return BookingPenaltyResult{}, err
	}
	if !proven {
		return BookingPenaltyResult{}, ErrBookingPenaltyEvidence
	}
	// Prior legitimate consumption can leave no approved remainder. A consumed hold
	// then proves a terminal zero effect; no idempotent movement is fabricated.
	if !e.Remaining.IsPositive() {
		if e.Reservation.Status != ledger.ReservationConsumed || e.Reservation.Remaining().IsPositive() {
			return BookingPenaltyResult{}, ErrBookingPenaltyEvidence
		}
		return BookingPenaltyResult{ConsumedService: benefitdomain.ZeroQuantity(),
			ReleasedService: benefitdomain.ZeroQuantity(), ConsumedUnits: benefitdomain.ZeroQuantity(),
			ReleasedUnits: benefitdomain.ZeroQuantity()}, nil
	}
	if !e.Reservation.Open() && !(e.Reservation.Status == ledger.ReservationConsumed &&
		!e.Reservation.Remaining().IsPositive()) {
		return BookingPenaltyResult{}, ErrBookingPenaltyEvidence
	}
	items, err := s.repo.ListAuthorizationItems(ctx, tx, in.TenantID, in.AuthorizationID, true)
	if err != nil {
		return BookingPenaltyResult{}, err
	}
	if len(items) != 1 {
		return BookingPenaltyResult{}, ErrBookingPenaltyEvidence
	}
	item := items[0]
	consumed := in.PolicyPenalty.Min(e.Remaining)
	released := e.Remaining.Sub(consumed)
	before := e.Reservation
	if consumed.IsPositive() {
		draw := e.Consumed.Add(consumed).Mul(e.UnitFactor).Sub(e.Consumed.Mul(e.UnitFactor))
		if draw.IsNegative() {
			return BookingPenaltyResult{}, ErrBookingPenaltyEvidence
		}
		if draw.IsPositive() {
			if _, err = s.ledger.Consume(ctx, tx, ledger.MovementInput{TenantID: in.TenantID,
				ReservationID: before.ID, Quantity: draw, Key: in.ConsumeKey,
				ReasonCode: in.ConsumeReason, ActorID: in.ActorID}); err != nil {
				return BookingPenaltyResult{}, fmt.Errorf("authorization: booking penalty consume: %w", err)
			}
		}
		if err = s.repo.AddAuthorizationItemConsumption(ctx, tx, in.TenantID, item.ID, consumed); err != nil {
			return BookingPenaltyResult{}, err
		}
		if err = s.repo.ApplyConsumption(ctx, tx, in.TenantID, in.AuthorizationID, consumed, actorPtr(in.ActorID)); err != nil {
			return BookingPenaltyResult{}, err
		}
	}
	if released.IsPositive() {
		// The original booking reservation is the only release target; ReleaseUnused's
		// broad multi-line and replay semantics are intentionally not used here.
		current, readErr := s.ledger.ReadReservation(ctx, tx, in.TenantID, before.ID)
		if readErr != nil {
			return BookingPenaltyResult{}, readErr
		}
		if current.Remaining().IsPositive() {
			if !current.Open() {
				return BookingPenaltyResult{}, ErrBookingPenaltyEvidence
			}
			if _, err = s.ledger.Release(ctx, tx, ledger.MovementInput{TenantID: in.TenantID,
				ReservationID: before.ID, Quantity: current.Remaining(),
				Key: releaseKey(item.ID, in.ReleaseReason), ReasonCode: in.ReleaseReason,
				ActorID: in.ActorID}); err != nil {
				return BookingPenaltyResult{}, fmt.Errorf("authorization: booking penalty release: %w", err)
			}
		}
	}
	after, err := s.ledger.ReadReservation(ctx, tx, in.TenantID, before.ID)
	if err != nil {
		return BookingPenaltyResult{}, err
	}
	updated, err := s.repo.ListAuthorizationItems(ctx, tx, in.TenantID, in.AuthorizationID, true)
	if err != nil {
		return BookingPenaltyResult{}, err
	}
	if len(updated) != 1 || updated[0].ID != item.ID {
		return BookingPenaltyResult{}, ErrBookingPenaltyEvidence
	}
	itemConsumed, err := benefitdomain.ParseQuantity(updated[0].ConsumedQuantity)
	if err != nil || itemConsumed.Sub(e.Consumed).Cmp(consumed) != 0 {
		return BookingPenaltyResult{}, ErrBookingPenaltyEvidence
	}
	result := BookingPenaltyResult{ConsumedService: consumed, ReleasedService: released,
		ConsumedUnits: after.Consumed.Sub(before.Consumed), ReleasedUnits: after.Released.Sub(before.Released)}
	if after.Remaining().IsPositive() || result.ConsumedUnits.IsNegative() || result.ReleasedUnits.IsNegative() ||
		result.ConsumedUnits.Add(result.ReleasedUnits).Cmp(before.Remaining()) != 0 ||
		result.ConsumedUnits.Cmp(e.Consumed.Add(consumed).Mul(e.UnitFactor).Sub(e.Consumed.Mul(e.UnitFactor))) != 0 {
		return BookingPenaltyResult{}, ErrBookingPenaltyEvidence
	}
	return result, nil
}
