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
)

// ConsumeInput is one line of somebody else's aggregate drawing on a hold, inside that
// caller's own transaction.
type ConsumeInput struct {
	TenantID uuid.UUID
	ActorID  uuid.UUID
	// AuthorizationID names the promise. It is required: a line that draws on nothing has
	// nothing to over-consume and never reaches this call.
	AuthorizationID uuid.UUID
	// ServiceDefinitionID is what the line delivered. The authorization's own line for that
	// service is the one that is drawn down; there is at most one, because an authorization
	// approves a service once.
	ServiceDefinitionID uuid.UUID
	Quantity            benefitdomain.Quantity
	// Key makes the movement idempotent. It is derived from the thing consuming — a claim
	// line — rather than from a clock, so a redelivered command consumes once.
	Key        string
	ReasonCode string
}

// ConsumeResult is what the hold said, whether or not anything moved.
type ConsumeResult struct {
	// Matched is false when the authorization approved no line for this service. That is
	// not an error: a claim may carry a line the preauthorization never covered, and the
	// caller decides what to do about it.
	Matched bool
	// Remaining is what the line still held *before* this call, which is the figure an
	// over-consumption exception has to quote.
	Remaining benefitdomain.Quantity
	// Consumed is what actually moved. Zero on an over-consumption, because nothing moved.
	Consumed benefitdomain.Quantity
	// OverConsumed is true when the quantity asked for is more than the line still holds.
	// **Nothing is written in that case.** An over-consumption is an exception a person
	// looks at, never a silent consume of what happened to be left: a hold for four
	// sessions drawn on for six is either a mistake or a fraud, and taking four of them
	// quietly would hide both.
	OverConsumed bool
}

// ErrConsumeNoReason refuses a consume with no reason code. The reason is part of the
// movement's idempotency key, so an empty one would make two different consumptions collide.
var ErrConsumeNoReason = errors.New("authorization: a consume needs a reason code")

// Consume draws one line's quantity out of the hold its service took, inside the caller's
// transaction, and reports exactly what it did.
//
// It exists because a claim (WP-I5-04) settles against an authorization line by line, and it
// has to do so in the claim's own transaction: a claim that was submitted while the
// entitlement was not yet spent is a promise two claims could both draw on. `Consume` on a
// fulfilment cannot serve that — a fulfilment consumes what a provider recorded delivering,
// and a claim consumes what the payer is being billed for, and the two are decided by
// different people at different moments.
//
// The over-consumption rule is the whole reason this returns a result rather than an error.
// A caller that was told "no" would have to guess whether to raise an exception line or to
// refuse the claim; the result says which line, how much was left and that nothing moved, so
// the exception the claim writes can quote a figure.
func (s *Service) Consume(ctx context.Context, tx pgx.Tx, in ConsumeInput) (ConsumeResult, error) {
	if in.ReasonCode == "" {
		return ConsumeResult{}, ErrConsumeNoReason
	}
	if !in.Quantity.IsPositive() {
		return ConsumeResult{}, nil
	}
	authorization, err := s.repo.GetAuthorization(ctx, tx, in.TenantID, in.AuthorizationID, Scope{})
	if err != nil {
		return ConsumeResult{}, err
	}
	if !domain.Open(authorization.Status) {
		return ConsumeResult{}, ErrAuthorizationNotActive
	}
	// FOR UPDATE, so the remainder computed here is one nobody else can change before this
	// transaction commits.
	items, err := s.repo.ListAuthorizationItems(ctx, tx, in.TenantID, in.AuthorizationID, true)
	if err != nil {
		return ConsumeResult{}, err
	}
	for _, item := range items {
		if item.ServiceDefinitionID != in.ServiceDefinitionID {
			continue
		}
		remaining, err := remainderOf(item)
		if err != nil {
			return ConsumeResult{}, err
		}
		out := ConsumeResult{Matched: true, Remaining: remaining, Consumed: benefitdomain.ZeroQuantity()}
		if in.Quantity.Cmp(remaining) > 0 {
			// Nothing is written. The claim raises an exception line and a person decides.
			out.OverConsumed = true
			return out, nil
		}
		if item.ReservationID == nil {
			return ConsumeResult{}, ErrAccountNotFound
		}
		draw, err := entitlementConsumption(item, in.Quantity)
		if err != nil {
			return ConsumeResult{}, err
		}
		if _, err := s.ledger.Consume(ctx, tx, ledger.MovementInput{
			TenantID: in.TenantID, ReservationID: *item.ReservationID, Quantity: draw,
			Key: in.Key, ReasonCode: in.ReasonCode, ActorID: in.ActorID,
		}); err != nil {
			if errors.Is(err, ledger.ErrIdempotentReplay) {
				// The first delivery of this consume already happened. The entitlement is
				// where it belongs and the counters have already moved with it.
				out.Consumed = in.Quantity
				return out, nil
			}
			return ConsumeResult{}, fmt.Errorf("authorization: consume: %w", err)
		}
		if err := s.repo.AddAuthorizationItemConsumption(ctx, tx, in.TenantID, item.ID, in.Quantity); err != nil {
			return ConsumeResult{}, err
		}
		if err := s.repo.ApplyConsumption(ctx, tx, in.TenantID, in.AuthorizationID, in.Quantity,
			actorPtr(in.ActorID)); err != nil {
			return ConsumeResult{}, err
		}
		out.Consumed = in.Quantity
		return out, nil
	}
	return ConsumeResult{Matched: false, Remaining: benefitdomain.ZeroQuantity(),
		Consumed: benefitdomain.ZeroQuantity()}, nil
}
