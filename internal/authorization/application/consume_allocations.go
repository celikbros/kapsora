package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/authorization/domain"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
)

// ConsumptionAllocation is a frozen claim-line draw, measured in service units.
type ConsumptionAllocation struct {
	AuthorizationID uuid.UUID
	Quantity        benefitdomain.Quantity
	Key             string
}

type ConsumeAllocationsInput struct {
	TenantID            uuid.UUID
	ActorID             uuid.UUID
	ServiceDefinitionID uuid.UUID
	Quantity            benefitdomain.Quantity
	ReasonCode          string
	Allocations         []ConsumptionAllocation
}

type ConsumeAllocationsResult struct {
	OverConsumed bool
	Remaining    benefitdomain.Quantity
	Consumed     benefitdomain.Quantity
	Draws        []ConsumptionAllocation
}

var (
	ErrInvalidAllocations = errors.New("authorization: invalid consumption allocations")
	ErrAllocationReplay   = errors.New("authorization: inconsistent or reversed consumption replay")
)

type preparedAllocation struct {
	allocation ConsumptionAllocation
	item       AuthorizationItemRecord
	draw       benefitdomain.Quantity
}

// ConsumeAllocations checks every hold before moving any entitlement. The caller owns the
// surrounding tenant transaction and stores its immutable claim receipt in that transaction.
func (s *Service) ConsumeAllocations(ctx context.Context, tx pgx.Tx, in ConsumeAllocationsInput) (out ConsumeAllocationsResult, err error) {
	if in.ReasonCode == "" {
		return out, ErrConsumeNoReason
	}
	if in.TenantID == uuid.Nil || in.ServiceDefinitionID == uuid.Nil || !in.Quantity.IsPositive() || len(in.Allocations) == 0 {
		return out, ErrInvalidAllocations
	}
	ordered := append([]ConsumptionAllocation(nil), in.Allocations...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].AuthorizationID.String() < ordered[j].AuthorizationID.String() })
	seenKeys := make(map[string]bool, len(ordered))
	total := benefitdomain.ZeroQuantity()
	for i, a := range ordered {
		if a.AuthorizationID == uuid.Nil || !a.Quantity.IsPositive() || seenKeys[a.Key] || !validAllocationKey(a.Key, a.AuthorizationID) {
			return out, ErrInvalidAllocations
		}
		if i > 0 && ordered[i-1].AuthorizationID == a.AuthorizationID {
			return out, ErrInvalidAllocations
		}
		seenKeys[a.Key] = true
		total = total.Add(a.Quantity)
	}
	if total.Cmp(in.Quantity) != 0 {
		return out, ErrInvalidAllocations
	}

	prepared := make([]preparedAllocation, 0, len(ordered))
	replayCount := 0
	for _, a := range ordered {
		auth, lockErr := s.repo.LockAuthorization(ctx, tx, in.TenantID, a.AuthorizationID, Scope{})
		if lockErr != nil {
			return out, lockErr
		}
		items, listErr := s.repo.ListAuthorizationItems(ctx, tx, in.TenantID, a.AuthorizationID, true)
		if listErr != nil {
			return out, listErr
		}
		var item *AuthorizationItemRecord
		for i := range items {
			if items[i].ServiceDefinitionID == in.ServiceDefinitionID {
				item = &items[i]
				break
			}
		}
		if item == nil || item.ReservationID == nil {
			return out, ErrItemNotInAuthorization
		}
		previous, found, findErr := s.repo.FindConsumption(ctx, tx, in.TenantID, a.AuthorizationID, *item.ReservationID, a.Key, in.ReasonCode)
		if findErr != nil {
			return out, findErr
		}
		if found {
			if previous.Reversed || !previous.Quantity.IsPositive() {
				return out, ErrAllocationReplay
			}
			factor, factorErr := benefitdomain.ParseQuantity(item.EntitlementUnitFactor)
			if factorErr != nil {
				return out, factorErr
			}
			// The movement stores entitlement units. Cumulative rounding can change
			// this delta by one micro-unit; a larger difference disproves replay.
			difference := previous.Quantity.Sub(a.Quantity.Mul(factor))
			if difference.IsNegative() {
				difference = difference.Neg()
			}
			if difference.Cmp(benefitdomain.MustQuantity("0.000001")) > 0 {
				return out, ErrAllocationReplay
			}
			replayCount++
			prepared = append(prepared, preparedAllocation{allocation: a, item: *item})
			continue
		}
		if !domain.Open(auth.Status) {
			return out, ErrAuthorizationNotActive
		}
		remaining, remainErr := remainderOf(*item)
		if remainErr != nil {
			return out, remainErr
		}
		out.Remaining = out.Remaining.Add(remaining)
		draw, drawErr := entitlementConsumption(*item, a.Quantity)
		if drawErr != nil {
			return out, drawErr
		}
		reserved, reservedErr := s.repo.ReservationRemaining(ctx, tx, in.TenantID, *item.ReservationID)
		if reservedErr != nil {
			return out, reservedErr
		}
		if a.Quantity.Cmp(remaining) > 0 || draw.Cmp(reserved) > 0 || !draw.IsPositive() {
			out.OverConsumed = true
		}
		prepared = append(prepared, preparedAllocation{allocation: a, item: *item, draw: draw})
	}
	if replayCount != 0 {
		if replayCount != len(ordered) {
			return ConsumeAllocationsResult{}, ErrAllocationReplay
		}
		// The ledger stores entitlement units, not the original service-unit split.
		// The claim's frozen allocation rows are its authoritative replay receipt.
		return ConsumeAllocationsResult{Consumed: in.Quantity, Draws: append([]ConsumptionAllocation(nil), in.Allocations...)}, nil
	}
	if out.OverConsumed {
		return out, nil
	}

	// A write failure must not leave the caller's transaction with only the first hold
	// spent. A savepoint makes the operation atomic even if the caller handles the error.
	const savepoint = "authorization_consume_allocations"
	if _, err = tx.Exec(ctx, "SAVEPOINT "+savepoint); err != nil {
		return ConsumeAllocationsResult{}, err
	}
	defer func() {
		if err != nil {
			_, rollbackErr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT "+savepoint)
			if rollbackErr != nil {
				err = errors.Join(err, rollbackErr)
			}
		}
		_, releaseErr := tx.Exec(ctx, "RELEASE SAVEPOINT "+savepoint)
		if releaseErr != nil {
			err = errors.Join(err, releaseErr)
		}
	}()
	for _, p := range prepared {
		_, err = s.ledger.Consume(ctx, tx, ledger.MovementInput{TenantID: in.TenantID, ReservationID: *p.item.ReservationID, Quantity: p.draw, Key: p.allocation.Key, ReasonCode: in.ReasonCode, ActorID: in.ActorID})
		if err != nil {
			return ConsumeAllocationsResult{}, fmt.Errorf("authorization: consume allocation: %w", err)
		}
		if err = s.repo.AddAuthorizationItemConsumption(ctx, tx, in.TenantID, p.item.ID, p.allocation.Quantity); err != nil {
			return ConsumeAllocationsResult{}, err
		}
		if err = s.repo.ApplyConsumption(ctx, tx, in.TenantID, p.allocation.AuthorizationID, p.allocation.Quantity, actorPtr(in.ActorID)); err != nil {
			return ConsumeAllocationsResult{}, err
		}
	}
	out.Consumed = in.Quantity
	out.Draws = append([]ConsumptionAllocation(nil), in.Allocations...)
	return out, nil
}

func validAllocationKey(key string, authorizationID uuid.UUID) bool {
	const prefix = "claim-line:"
	const separator = ":authorization:"
	if !strings.HasPrefix(key, prefix) {
		return false
	}
	line, suffix, ok := strings.Cut(strings.TrimPrefix(key, prefix), separator)
	if !ok || suffix != authorizationID.String() {
		return false
	}
	id, err := uuid.Parse(line)
	return err == nil && id != uuid.Nil && line == id.String()
}
