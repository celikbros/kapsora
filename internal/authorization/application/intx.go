package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/authorization/domain"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// The commands of this module that another module runs inside its own transaction.
//
// They exist because a stay in a hotel is one fact made of several: a guest checks in and
// the voucher is spent in the same breath, and a guest checks out and the nights they used
// are consumed while the nights they did not are released. A caller that had to run those
// as separate transactions would have a window in which the booking says one thing and the
// entitlement another, and there is no way to close such a window afterwards.
//
// Every one of them is the body of a command this package already has, called with a
// transaction rather than opening one. None of them decides anything the outer command does
// not: the window a voucher is valid in is this module's, the over-consumption rule is this
// module's, and a caller that wanted a different answer would have to ask a different
// question.

// RedeemTokenInput is a voucher presented at a counter, redeemed inside the caller's
// transaction and without a fulfilment.
type RedeemTokenInput struct {
	// Token is the plaintext the member presented. It is hashed immediately and reaches no
	// column, no log line and no audit row.
	Token string
	// AuthorizationID is the promise the caller expects this token to belong to. A token
	// that belongs to a different authorization answers ErrVoucherNotFound and not "wrong
	// booking": a counter that could tell the two apart could be used to find out whether a
	// code exists at all.
	AuthorizationID uuid.UUID
	RedeemedAt      time.Time
}

// RedeemToken marks a voucher used inside the caller's transaction and records nothing else.
//
// It is separate from RedeemVoucher, which records a fulfilment in the same act, because the
// two callers mean different things. A counter that redeems a voucher *is* delivering the
// service, so a fulfilment belongs with it. A hotel desk taking a voucher at check-in is not:
// nobody has slept anywhere yet, and the fulfilment is written at check-out for the nights
// actually used. Recording one here would be a record of a delivery that has not happened,
// and it would have to be cancelled and rewritten by every early check-out.
//
// The window checked here is the voucher's own. Whether the guest is inside the property's
// check-in hours is the accommodation module's question, asked against the tenant's own
// settings and the building's zone, and this module has no opinion about it.
func (s *Service) RedeemToken(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	in RedeemTokenInput,
) (VoucherRecord, error) {
	if in.Token == "" {
		return VoucherRecord{}, fieldError("token", "REQUIRED", "kupon kodu zorunlu")
	}
	scope := scopeOf(rc)
	// The lookup argument is the digest and never the token: a statement log that kept
	// query parameters would otherwise be a list of vouchers somebody could spend.
	voucher, err := s.repo.LockVoucherByTokenHash(ctx, tx, rc.TenantID, domain.TokenHash(in.Token), scope)
	if err != nil {
		return VoucherRecord{}, err
	}
	if in.AuthorizationID != uuid.Nil && voucher.AuthorizationID != in.AuthorizationID {
		// A real token for somebody else's promise. It is the same answer an unknown token
		// gets, deliberately: "this code is valid but not for this booking" is a fact a
		// desk can use to confirm a code exists.
		return VoucherRecord{}, ErrVoucherNotFound
	}
	now := in.RedeemedAt
	if now.IsZero() {
		now = s.now().UTC()
	}
	if err := redeemable(voucher, now.UTC()); err != nil {
		return VoucherRecord{}, err
	}
	authorization, err := s.repo.LockAuthorization(ctx, tx, rc.TenantID, voucher.AuthorizationID, scope)
	if err != nil {
		return VoucherRecord{}, err
	}
	if !domain.Open(authorization.Status) {
		return VoucherRecord{}, ErrAuthorizationNotActive
	}
	redeemed, err := s.repo.MarkVoucherRedeemed(ctx, tx, rc.TenantID, voucher.ID, now.UTC(),
		actorPtr(rc.Principal.ActorID))
	if err != nil {
		return VoucherRecord{}, err
	}
	// The row was ISSUED when it was locked, so a write that changed nothing means somebody
	// else redeemed it between the lock and here -- which cannot happen with the lock held,
	// and is therefore worth refusing loudly rather than ignoring.
	if !redeemed {
		return VoucherRecord{}, ErrVoucherAlreadyRedeemed
	}
	// The audit detail names the voucher row and nothing that resembles the token.
	if err := s.record(ctx, tx, rc, "voucher.redeem", "voucher", voucher.ID, map[string]any{
		"authorization_id": authorization.ID.String(),
	}); err != nil {
		return VoucherRecord{}, err
	}
	voucher.Status = domain.VoucherRedeemed
	return voucher, nil
}

// RevokeVouchersInTx withdraws every live voucher of an authorization inside the caller's
// transaction, so a cancelled stay and the code that would have opened its room stop being
// true at the same instant. Every revocation is its own audit row.
func (s *Service) RevokeVouchersInTx(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	authorizationID uuid.UUID, reasonCode string,
) error {
	if reasonCode == "" {
		return errors.New("authorization: a revocation needs a reason code")
	}
	return s.revokeLiveVouchers(ctx, tx, rc, authorizationID, reasonCode)
}

// RecordAndCompleteInTx records what was delivered and consumes it, in the caller's
// transaction, as one act.
//
// Recording and completing are two commands everywhere else, and they are two on purpose:
// a provider records what happened at the counter and a back office decides the entitlement
// was spent on it, and keeping them apart is what lets a mistaken record be cancelled with
// no ledger reversal. A check-out is the one place where they are genuinely one moment --
// the guest has left, the nights are known, and there is nobody left to correct anything --
// so this runs both against the same locked lines rather than leaving a recorded fulfilment
// nothing will ever complete.
func (s *Service) RecordAndCompleteInTx(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	in NewFulfilmentInput,
) (FulfilmentRecord, benefitdomain.Quantity, error) {
	if err := domain.ValidateNewFulfilment(domain.NewFulfilment{
		PerformedAt: in.PerformedAt, Items: in.Items,
	}); err != nil {
		return FulfilmentRecord{}, benefitdomain.Quantity{}, err
	}
	scope := scopeOf(rc)
	authorization, err := s.repo.LockAuthorization(ctx, tx, rc.TenantID, in.AuthorizationID, scope)
	if err != nil {
		return FulfilmentRecord{}, benefitdomain.Quantity{}, err
	}
	record, err := s.recordFulfilment(ctx, tx, rc, authorization, in)
	if err != nil {
		return FulfilmentRecord{}, benefitdomain.Quantity{}, err
	}
	consumed, err := s.consume(ctx, tx, rc, record)
	if err != nil {
		return FulfilmentRecord{}, benefitdomain.Quantity{}, err
	}
	if err := s.repo.ApplyConsumption(ctx, tx, rc.TenantID, authorization.ID, consumed,
		actorPtr(rc.Principal.ActorID)); err != nil {
		return FulfilmentRecord{}, benefitdomain.Quantity{}, err
	}
	if err := s.repo.CompleteFulfilment(ctx, tx, rc.TenantID, record.ID, s.now().UTC(),
		record.RowVersion); err != nil {
		return FulfilmentRecord{}, benefitdomain.Quantity{}, err
	}
	if err := s.record(ctx, tx, rc, "fulfilment.complete", "fulfilment", record.ID, map[string]any{
		"authorization_id": authorization.ID.String(), "consumed_total": consumed.String(),
	}); err != nil {
		return FulfilmentRecord{}, benefitdomain.Quantity{}, err
	}
	return record, consumed, nil
}

// AuthorizationLine is one approved line of a hold as another module needs to read it: what
// was promised, what has been spent, and which service it was promised for.
type AuthorizationLine struct {
	ID                  uuid.UUID
	ServiceDefinitionID uuid.UUID
	Approved            benefitdomain.Quantity
	Consumed            benefitdomain.Quantity
	Remaining           benefitdomain.Quantity
}

// AuthorizationLines reads the lines of a hold inside the caller's transaction, FOR UPDATE,
// so a caller about to consume or release against them computes its arithmetic on numbers
// nobody else can move before it commits.
//
// It exists because "how much did this promise actually cover" is not a number any other
// module may guess. A booking asked for the nights its quote said the plan carries; a
// reviewer may have approved fewer; and a check-out that released `booked - stayed` rather
// than `approved - stayed` would hand back entitlement that was never held.
func (s *Service) AuthorizationLines(ctx context.Context, tx pgx.Tx, tenantID,
	authorizationID uuid.UUID,
) ([]AuthorizationLine, error) {
	items, err := s.repo.ListAuthorizationItems(ctx, tx, tenantID, authorizationID, true)
	if err != nil {
		return nil, err
	}
	out := make([]AuthorizationLine, 0, len(items))
	for _, item := range items {
		approved, err := benefitdomain.ParseQuantity(item.ApprovedQuantity)
		if err != nil {
			return nil, fmt.Errorf("authorization: line %s approved quantity: %w", item.ID, err)
		}
		remaining, err := remainderOf(item)
		if err != nil {
			return nil, err
		}
		out = append(out, AuthorizationLine{
			ID: item.ID, ServiceDefinitionID: item.ServiceDefinitionID,
			Approved: approved, Consumed: approved.Sub(remaining), Remaining: remaining,
		})
	}
	return out, nil
}
