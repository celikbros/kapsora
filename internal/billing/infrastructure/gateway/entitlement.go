package billinggw

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	billingapp "github.com/celikbros/kapsora/internal/billing/application"
)

// The member's wallet seen from the settlement module (WP-I7-04 section 2.4).
//
// It exists so that the billing package does not know what an entitlement account is and the
// ledger does not know what a reimbursement is. What the billing package asks for is "take
// this much money off this person for this request"; what the ledger answers is a movement,
// conservation intact.
//
// **The draw-down is a reserve followed immediately by a consume, in one transaction.** The
// ledger has no direct-consume path and should not grow one for this: `Reserve` is where the
// no-double-spend check lives — the account row is locked, the availability is taken under the
// lock, and `ErrInsufficient` is raised there — and `Consume` is what moves the quantity from
// reserved to consumed. Doing both here means an approval that fails afterwards leaves neither,
// and an approval that commits leaves exactly one reserve and one consume against one hold,
// which is what `ck_entitlement_ledger_conservation` and the conservation test both check.
//
// Both movements carry keys derived from the reimbursement rather than from a clock, so a
// command replayed under the same Idempotency-Key finds the ledger's own replay answer instead
// of spending twice.

// Entitlements is the ledger seen from the settlement module.
type Entitlements struct{ ledger *ledger.Ledger }

// NewEntitlements wraps the ledger.
func NewEntitlements(l *ledger.Ledger) *Entitlements { return &Entitlements{ledger: l} }

var _ billingapp.EntitlementPort = (*Entitlements)(nil)

// Consume implements billingapp.EntitlementPort.
//
// The account is the member's MONEY account in the reimbursement's own currency, open on the
// service date. A member with none is `ENTITLEMENT_ACCOUNT_NOT_FOUND`, which is refused rather
// than paid: a reimbursement that drew down nothing would be money paid out of a wallet nobody
// debited, and that is exactly the mutation the acceptance criterion of this package names.
//
// A member with more than one — a plan with a general health purse and a separate optical one,
// both in lira — draws on the fullest. It is a deterministic choice rather than a good one:
// which purse a receipt belongs to is a question the service-entitlement mapping answers for
// services the plan maps, and for the ones it does not there is no better answer than "the one
// that can carry it". Ties fall to the lower account id so two runs agree.
func (e *Entitlements) Consume(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in billingapp.EntitlementConsumption,
) error {
	amount, err := benefitdomain.ParseQuantity(in.Amount)
	if err != nil {
		return fmt.Errorf("billing: reimbursement amount %q: %w", in.Amount, err)
	}
	if !amount.IsPositive() {
		// Approving nothing consumes nothing, and the caller does not reach here for a
		// rejection. A zero movement would be a ledger row that recorded no money moving.
		return nil
	}

	accounts, err := e.ledger.ResolveAccounts(ctx, tx, tenantID, in.PersonID, in.AsOf)
	if err != nil {
		return err
	}
	account, found := pickMoneyAccount(accounts, in.CurrencyCode)
	if !found {
		return billingapp.ErrEntitlementAccountNotFound
	}

	reservation, err := e.ledger.Reserve(ctx, tx, ledger.ReserveInput{
		TenantID: tenantID, AccountID: account.ID, Quantity: amount,
		ReferenceType: ledger.ReferenceServiceRequest, ReferenceID: in.ReferenceID,
		Key: in.Key + ":reserve", ReasonCode: in.ReasonCode, ActorID: in.ActorID,
	})
	switch {
	case errors.Is(err, ledger.ErrInsufficient):
		return billingapp.ErrEntitlementInsufficient
	case errors.Is(err, ledger.ErrIdempotentReplay):
		// The same approval, replayed. The hold is already there and the consume below is
		// replayed with it; neither writes a second movement.
	case err != nil:
		return err
	}

	if _, err := e.ledger.Consume(ctx, tx, ledger.MovementInput{
		TenantID: tenantID, ReservationID: reservation.ID, Quantity: amount,
		Key: in.Key + ":consume", ReasonCode: in.ReasonCode, ActorID: in.ActorID,
	}); err != nil && !errors.Is(err, ledger.ErrIdempotentReplay) {
		return err
	}
	return nil
}

// pickMoneyAccount is the choice above, in one place so a test can point at it.
func pickMoneyAccount(accounts []ledger.Account, currency string) (ledger.Account, bool) {
	var best ledger.Account
	found := false
	for _, account := range accounts {
		if account.Status != ledger.AccountOpen {
			continue
		}
		if account.Definition.UnitType != unitMoney {
			continue
		}
		if currency != "" && account.Definition.CurrencyCode != currency {
			continue
		}
		switch {
		case !found:
			best, found = account, true
		case account.Balances.Available.Cmp(best.Balances.Available) > 0:
			best = account
		case account.Balances.Available.Cmp(best.Balances.Available) == 0 &&
			account.ID.String() < best.ID.String():
			best = account
		}
	}
	return best, found
}

// unitMoney is `benefit.entitlement_definition.unit_type` for a purse denominated in money.
const unitMoney = "MONEY"
