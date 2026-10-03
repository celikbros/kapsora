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

// ReleaseUnusedInput asks for part of a hold back, inside somebody else's transaction.
type ReleaseUnusedInput struct {
	TenantID        uuid.UUID
	ActorID         uuid.UUID
	AuthorizationID uuid.UUID
	// Quantity is a ceiling rather than an amount: the release gives back what is still
	// outstanding up to this, so a caller that asks for more than the authorization holds
	// gets what it holds rather than an error, and a line something has already consumed
	// is not released twice.
	Quantity benefitdomain.Quantity
	// ReasonCode names the movement in the ledger and is part of its idempotency key, so a
	// discharge and a cancellation of the same line are two distinct movements and either
	// of them run twice is still one.
	ReasonCode string
}

// ReleaseUnused gives back entitlement an authorization reserved and nobody used, inside the
// caller's transaction, and reports exactly how much it gave back.
//
// It exists because an inpatient stay (WP-I5-03) authorized for five days and discharged
// after three has to release two — and it has to release them in the discharge's own
// transaction, because a stay that ended while the entitlement was still held is a member
// unable to use a benefit they have already paid for. `Cancel` cannot serve that: it
// releases everything and withdraws the promise, and a discharge is the promise having been
// kept.
//
// The idempotency key is derived from the line and the reason rather than from a clock, so
// running the same discharge twice releases once. That is the property WP-I5-03's "running
// discharge twice releases nothing twice" leans on, and it is a property of this key rather
// than of any flag the caller has to remember to check.
func (s *Service) ReleaseUnused(ctx context.Context, tx pgx.Tx, in ReleaseUnusedInput) (benefitdomain.Quantity, error) {
	if in.ReasonCode == "" {
		return benefitdomain.Quantity{}, errors.New("authorization: a release needs a reason code")
	}
	released := benefitdomain.ZeroQuantity()
	if !in.Quantity.IsPositive() {
		return released, nil
	}
	// The lines are taken FOR UPDATE, so the remainder computed here is one nobody else
	// can change before this transaction commits.
	items, err := s.repo.ListAuthorizationItems(ctx, tx, in.TenantID, in.AuthorizationID, true)
	if err != nil {
		return benefitdomain.Quantity{}, err
	}
	budget := in.Quantity
	for _, item := range items {
		if !budget.IsPositive() || item.ReservationID == nil {
			continue
		}
		remaining, err := remainderOf(item)
		if err != nil {
			return benefitdomain.Quantity{}, err
		}
		amount := remaining.Min(budget)
		if !amount.IsPositive() {
			continue
		}
		draw, err := entitlementRelease(item, amount)
		if err != nil {
			return benefitdomain.Quantity{}, err
		}
		_, err = s.ledger.Release(ctx, tx, ledger.MovementInput{
			TenantID: in.TenantID, ReservationID: *item.ReservationID, Quantity: draw,
			Key: releaseKey(item.ID, in.ReasonCode), ReasonCode: in.ReasonCode,
			ActorID: in.ActorID,
		})
		switch {
		case errors.Is(err, ledger.ErrIdempotentReplay), errors.Is(err, ledger.ErrReservationClosed):
			// The first delivery of this release already happened, or the hold is
			// closed. Either way the entitlement is where it belongs and nothing more is
			// owed; reporting an error here would make a redelivered discharge fail
			// forever.
			continue
		case errors.Is(err, ledger.ErrQuantityRemainder):
			// The ledger's own remainder is smaller than the line's, which means
			// something has released part of this hold under a different reason. What is
			// left is not this caller's to take.
			continue
		case err != nil:
			return benefitdomain.Quantity{}, fmt.Errorf("authorization: release unused: %w", err)
		}
		released = released.Add(amount)
		budget = budget.Sub(amount)
	}
	return released, nil
}
