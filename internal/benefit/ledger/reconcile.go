package ledger

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// ReconcileBatchSize bounds one reconciliation pass over a tenant.
const ReconcileBatchSize = 500

// Drift is one account whose materialised balances no longer equal the sum of its ledger
// deltas. It can only come from a write that bypassed this package (a manual UPDATE, a
// restore, a bug), which is exactly what the daily job is there to catch.
type Drift struct {
	AccountID    uuid.UUID
	EnrollmentID uuid.UUID
	Status       string
	Account      Balances
	Ledger       Balances
}

// Reconcile compares every account of the tenant with the sum of its ledger movements
// and freezes the ones that disagree. Freezing stops further spending (Reserve and
// Consume answer ErrAccountFrozen) while leaving the account readable and adjustable, so
// an operator can repair it with a maker-checker adjustment.
func (l *Ledger) Reconcile(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, batch int) ([]Drift, error) {
	if batch <= 0 || batch > ReconcileBatchSize {
		batch = ReconcileBatchSize
	}
	rows, err := sqlcgen.New(tx).ListEntitlementAccountDrift(ctx, sqlcgen.ListEntitlementAccountDriftParams{
		TenantID: tenantID, PageSize: int32(batch),
	})
	if err != nil {
		return nil, fmt.Errorf("benefit: list account drift: %w", err)
	}
	out := make([]Drift, 0, len(rows))
	for _, r := range rows {
		account, err := parseBalances(r.AccountTotal, r.AccountAvailable, r.AccountReserved,
			r.AccountConsumed, r.AccountExpired)
		if err != nil {
			return nil, err
		}
		ledger, err := parseBalances(r.LedgerTotal, r.LedgerAvailable, r.LedgerReserved,
			r.LedgerConsumed, r.LedgerExpired)
		if err != nil {
			return nil, err
		}
		if err := freezeAccount(ctx, tx, tenantID, r.ID); err != nil {
			return nil, err
		}
		out = append(out, Drift{
			AccountID: r.ID, EnrollmentID: r.EnrollmentID, Status: r.Status,
			Account: account, Ledger: ledger,
		})
	}
	return out, nil
}

// CountAccounts reports how many accounts the tenant has; the job publishes it as a
// metric so a reconciliation that suddenly examines nothing is visible.
func (l *Ledger) CountAccounts(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (int64, error) {
	n, err := sqlcgen.New(tx).CountEntitlementAccounts(ctx, tenantID)
	if err != nil {
		return 0, fmt.Errorf("benefit: count entitlement accounts: %w", err)
	}
	return n, nil
}

func freezeAccount(ctx context.Context, tx pgx.Tx, tenantID, accountID uuid.UUID) error {
	if _, err := sqlcgen.New(tx).SetEntitlementAccountStatus(ctx, sqlcgen.SetEntitlementAccountStatusParams{
		TenantID: tenantID, ID: accountID, Status: AccountFrozen,
	}); err != nil {
		return fmt.Errorf("benefit: freeze entitlement account: %w", err)
	}
	return nil
}

// Detail renders a drift as the audit and outbox payload: identifiers and exact decimal
// text only, never a personal field.
func (d Drift) Detail() map[string]any {
	return map[string]any{
		"account_id":        d.AccountID,
		"enrollment_id":     d.EnrollmentID,
		"account_status":    d.Status,
		"account_total":     d.Account.Total.String(),
		"account_available": d.Account.Available.String(),
		"account_reserved":  d.Account.Reserved.String(),
		"account_consumed":  d.Account.Consumed.String(),
		"account_expired":   d.Account.Expired.String(),
		"ledger_total":      d.Ledger.Total.String(),
		"ledger_available":  d.Ledger.Available.String(),
		"ledger_reserved":   d.Ledger.Reserved.String(),
		"ledger_consumed":   d.Ledger.Consumed.String(),
		"ledger_expired":    d.Ledger.Expired.String(),
	}
}
