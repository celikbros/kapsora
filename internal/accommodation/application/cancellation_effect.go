package application

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/accommodation/domain"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	"github.com/celikbros/kapsora/internal/identity"
)

// cancellationEvidence is limited to the booking's one original NIGHT reservation.
// A missing or ambiguous line leaves the existing cancellation path intact but cannot
// support a new exact-effect claim.
type cancellationEvidence struct {
	reservation ledger.Reservation
	consumed    benefitdomain.Quantity
	remaining   benefitdomain.Quantity
	factor      benefitdomain.Quantity
}

func (s *Service) cancellationEvidence(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record BookingRecord, snapshot QuoteSnapshot,
) (*cancellationEvidence, error) {
	if record.AuthorizationID == nil || record.ServiceRequestID == nil ||
		record.EntitlementReservationID == nil ||
		snapshot.Entitlement == nil || snapshot.Entitlement.Unit != domain.UnitNight {
		return nil, nil
	}
	evidence, err := s.auths.CancellationEvidence(ctx, tx, rc.TenantID,
		*record.AuthorizationID, *record.ServiceRequestID, record.PersonID,
		record.ID, *record.EntitlementReservationID, snapshot.ServiceDefinitionID)
	if err != nil {
		return nil, err
	}
	if evidence == nil {
		return nil, nil
	}
	if snapshot.NightConversion != nil &&
		(snapshot.NightConversion.AccountID != evidence.Reservation.AccountID ||
			snapshot.NightConversion.UnitFactor != evidence.UnitFactor.String()) {
		return nil, nil
	}
	return &cancellationEvidence{reservation: evidence.Reservation,
		consumed: evidence.Consumed, remaining: evidence.Remaining,
		factor: evidence.UnitFactor}, nil
}

func predictedCancellationEffect(e *cancellationEvidence, quote CancellationQuote,
	covered int,
) *EntitlementEffect {
	if e == nil {
		return nil
	}
	penalty := benefitdomain.MustQuantity(fmt.Sprintf("%d", quote.EntitlementPenalty(covered)))
	if penalty.Cmp(e.remaining) > 0 {
		// The command still refuses this frozen penalty. Preview retains its fee quote
		// and makes no claim that entitlement will move.
		return nil
	}
	released := e.remaining.Sub(penalty)
	ceiling := benefitdomain.MustQuantity(fmt.Sprintf("%d", quote.ReleasedNights))
	if released.Cmp(ceiling) > 0 {
		return nil
	}
	consumedUnits := e.consumed.Add(penalty).Mul(e.factor).Sub(e.consumed.Mul(e.factor))
	if consumedUnits.Cmp(e.reservation.Remaining()) > 0 {
		return nil
	}
	return &EntitlementEffect{
		ConsumedServiceNights: penalty.String(), ReleasedServiceNights: released.String(),
		ConsumedEntitlementUnits: consumedUnits.String(),
		ReleasedEntitlementUnits: e.reservation.Remaining().Sub(consumedUnits).String(),
	}
}

func wholeReleasedNights(effect *EntitlementEffect) (int, error) {
	quantity, err := benefitdomain.ParseQuantity(effect.ReleasedServiceNights)
	if err != nil {
		return 0, err
	}
	return wholeNights(quantity), nil
}
