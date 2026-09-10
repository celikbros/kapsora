package application

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/report/domain"
)

// DefaultCurrency is what a statement is read in when the caller names none. It is the currency
// every figure in this platform defaults to in its own schema, so a caller that omits it gets the
// column the database would have written.
const DefaultCurrency = "TRY"

// ProviderStatement answers the numbers both sides argue about: what the provider billed, what
// the payer decided, what was settled, what was actually paid, and what is still open.
//
// **The totals are one query.** They are not the invoice rows added up, and they are not the
// settlement rows added up: they are ten sums PostgreSQL computed over exact decimals in a single
// statement, and the rows below them are the evidence rather than the source. That matters
// because the rows are capped — a statement page shows at most `MaxStatementRows` of them — and a
// total assembled from a capped list would be a total that is quietly wrong for the busiest
// provider on the system.
//
// Who may read it: the payer side under `report.read`, and the provider itself for its own
// organization. A provider-scoped caller asking for somebody else's statement is refused rather
// than answered with an empty one, because it named an organization on purpose and is entitled to
// know its grants do not reach it.
func (s *Service) ProviderStatement(ctx context.Context, rc identity.RequestContext,
	providerID uuid.UUID, from, to *time.Time, currency string,
) (Statement, error) {
	if err := domain.ValidateStatementRequest(from, to, currency); err != nil {
		return Statement{}, err
	}
	if currency == "" {
		currency = DefaultCurrency
	}
	if !scopeOf(rc).Allows(providerID) {
		return Statement{}, ErrProviderScope
	}

	q := StatementQuery{
		ProviderOrganizationID: providerID,
		PeriodFrom:             from.UTC(), PeriodTo: to.UTC(),
		CurrencyCode: currency, RowLimit: domain.MaxStatementRows,
	}
	out := Statement{
		ProviderOrganizationID: providerID,
		PeriodFrom:             q.PeriodFrom, PeriodTo: q.PeriodTo, CurrencyCode: currency,
	}
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		totals, err := s.repo.StatementTotals(ctx, tx, rc.TenantID, q)
		if err != nil {
			return err
		}
		out.Totals = totals
		if out.Invoices, err = s.repo.StatementInvoices(ctx, tx, rc.TenantID, q); err != nil {
			return err
		}
		if out.Settlements, err = s.repo.StatementSettlements(ctx, tx, rc.TenantID, q); err != nil {
			return err
		}
		// The statement is a read of one provider's money over a period, which is exactly the
		// kind of read a dispute later asks who performed. The detail carries counts and codes
		// and no amount: what was owed is in the record already, and an audit detail is not a
		// second copy of the ledger.
		return s.record(ctx, tx, rc, "report.statement.read", "PROVIDER_STATEMENT", providerID,
			map[string]any{
				"currency_code":    currency,
				"invoice_count":    totals.InvoiceCount,
				"settlement_count": totals.SettlementCount,
				"period_days":      int(q.PeriodTo.Sub(q.PeriodFrom).Hours()/24) + 1,
			})
	})
	if err != nil {
		return Statement{}, err
	}
	return out, nil
}
