package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/authorization/domain"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
)

// ConsumptionRecord identifies an actual movement, not an attempted over-consumption.
type ConsumptionRecord struct {
	ID       uuid.UUID
	Quantity benefitdomain.Quantity
	Reversed bool
}

// UndoConsumption returns a submitted claim line's draw to its original hold when the
// reviewer returns the statement for correction. The immutable CONSUME remains and a
// linked REVERSE records its undo. Corrected lines consume afresh on resubmission.
// Closed/expired promises stay closed; their restored amount is released immediately.
// The caller must supply the original frozen line, within the same transaction as return.
func (s *Service) UndoConsumption(ctx context.Context, tx pgx.Tx, in ConsumeInput) error {
	if in.Key == "" || in.ReasonCode != "CLAIM" || !in.Quantity.IsPositive() {
		return ErrConsumeNoReason
	}
	auth, err := s.repo.LockAuthorization(ctx, tx, in.TenantID, in.AuthorizationID, Scope{})
	if err != nil {
		return err
	}
	items, err := s.repo.ListAuthorizationItems(ctx, tx, in.TenantID, in.AuthorizationID, true)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.ServiceDefinitionID != in.ServiceDefinitionID || item.ReservationID == nil {
			continue
		}
		original, found, err := s.repo.FindConsumption(ctx, tx, in.TenantID, in.AuthorizationID, *item.ReservationID, in.Key, in.ReasonCode)
		if err != nil {
			return err
		}
		if !found || original.Reversed {
			return nil
		}
		consumed, err := benefitdomain.ParseQuantity(item.ConsumedQuantity)
		if err != nil {
			return err
		}
		if in.Quantity.Cmp(consumed) > 0 {
			return fmt.Errorf("authorization: correction exceeds original consumption")
		}
		if _, err = s.ledger.Reverse(ctx, tx, ledger.ReverseInput{TenantID: in.TenantID, LedgerEntry: original.ID, Key: "claim-return:" + in.Key, ReasonCode: "CLAIM_RETURN", ActorID: in.ActorID}); err != nil {
			return err
		}
		if err = s.repo.AddAuthorizationItemConsumption(ctx, tx, in.TenantID, item.ID, in.Quantity.Neg()); err != nil {
			return err
		}
		if err = s.repo.RestoreConsumption(ctx, tx, in.TenantID, in.AuthorizationID, in.Quantity, s.now(), actorPtr(in.ActorID)); err != nil {
			return err
		}

		if (!domain.Open(auth.Status) && auth.Status != "USED") || !auth.ValidTo.After(s.now()) {
			// Expiry may have elapsed before the scheduler ran. Release every remaining hold,
			// including the restored draw, before leaving the authorization terminal.
			for _, heldItem := range items {
				if heldItem.ReservationID == nil {
					continue
				}
				held, err := s.repo.ReservationRemaining(ctx, tx, in.TenantID, *heldItem.ReservationID)
				if err != nil {
					return err
				}
				if !held.IsPositive() {
					continue
				}
				_, err = s.ledger.Release(ctx, tx, ledger.MovementInput{TenantID: in.TenantID, ReservationID: *heldItem.ReservationID, Quantity: held, Key: "claim-return-release:" + original.ID.String() + ":" + heldItem.ID.String(), ReasonCode: "CLAIM_RETURN_CLOSED", ActorID: in.ActorID})
				if err != nil {
					return err
				}
			}
		}

		return nil
	}
	return nil
}
