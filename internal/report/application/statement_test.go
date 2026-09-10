package application_test

import (
	"errors"
	"testing"

	benefit "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/report/application"
)

// **The statement's totals are the database's, to the kuruş.**
//
// Two comparisons, and both matter. The first adds the rows the endpoint returned and checks the
// totals against them, which is what a person reading the page would do. The second asks
// PostgreSQL the same question independently, through the admin pool with the whole application
// bypassed — so a service that started summing the rows in Go and dropped one of them fails here
// even though its own page still adds up.
func TestStatementTotalsAreTheDatabasesToTheKurus(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := f.ctx()
	defer cancel()

	batch := f.seedBatch(t, f.provider, statementFrom.AddDate(0, 0, 9), "3500.25", "3400.25", "100")
	f.seedInvoice(t, f.provider, statementFrom.AddDate(0, 0, 2), "1000", &batch)
	f.seedInvoice(t, f.provider, statementFrom.AddDate(0, 0, 5), "2000", &batch)
	f.seedInvoice(t, f.provider, statementFrom.AddDate(0, 0, 8), "500.25", &batch)
	// Another provider's invoice on the same day, and one of this provider's outside the period.
	// Both are traps: a statement that leaked either would be a figure the provider disputes.
	other := f.seedBatch(t, f.rival, statementFrom.AddDate(0, 0, 9), "9999", "9999", "0")
	f.seedInvoice(t, f.rival, statementFrom.AddDate(0, 0, 5), "9999", &other)
	f.seedInvoice(t, f.provider, statementFrom.AddDate(0, -1, 0), "777", nil)

	f.seedSettlement(t, f.provider, batch, statementFrom.AddDate(0, 0, 20), "3000", "1200",
		"PARTIALLY_PAID")
	second := f.seedBatch(t, f.provider, statementFrom.AddDate(0, 0, 11), "500", "500", "0")
	f.seedSettlement(t, f.provider, second, statementFrom.AddDate(0, 0, 25), "500", "500", "PAID")

	statement, err := f.reports.ProviderStatement(ctx, f.readerRC(), f.provider,
		&statementFrom, &statementTo, "TRY")
	if err != nil {
		t.Fatalf("statement: %v", err)
	}

	// The rows, added up here.
	rowInvoiced := benefit.MustQuantity("0")
	for _, invoice := range statement.Invoices {
		rowInvoiced = rowInvoiced.Add(benefit.MustQuantity(invoice.PayableAmount))
	}
	rowSettled := benefit.MustQuantity("0")
	rowPaid := benefit.MustQuantity("0")
	for _, settlement := range statement.Settlements {
		rowSettled = rowSettled.Add(benefit.MustQuantity(settlement.PayableAmount))
		rowPaid = rowPaid.Add(benefit.MustQuantity(settlement.PaidAmount))
	}
	if got := statement.Totals.InvoicedTotal; !equalAmount(got, rowInvoiced.String()) {
		t.Errorf("invoicedTotal = %s, the rows add up to %s", got, rowInvoiced)
	}
	if got := statement.Totals.SettledTotal; !equalAmount(got, rowSettled.String()) {
		t.Errorf("settledTotal = %s, the rows add up to %s", got, rowSettled)
	}
	if got := statement.Totals.PaidTotal; !equalAmount(got, rowPaid.String()) {
		t.Errorf("paidTotal = %s, the rows add up to %s", got, rowPaid)
	}

	// And the same question asked of PostgreSQL with the application bypassed.
	wantInvoiced := f.scalar(t, `
		SELECT trim_scale(COALESCE(sum(payable_amount), 0))::text FROM billing.invoice
		 WHERE tenant_id = $1 AND provider_organization_id = $2 AND currency_code = 'TRY'
		   AND invoice_date BETWEEN $3::date AND $4::date AND status NOT IN ('DRAFT','CANCELLED')`,
		f.tenant, f.provider, statementFrom, statementTo)
	if statement.Totals.InvoicedTotal != wantInvoiced {
		t.Errorf("invoicedTotal = %s, the database says %s",
			statement.Totals.InvoicedTotal, wantInvoiced)
	}
	if statement.Totals.InvoicedTotal != "3500.25" {
		t.Errorf("invoicedTotal = %s, want exactly 3500.25", statement.Totals.InvoicedTotal)
	}
	if statement.Totals.OpenBalance != "1800" {
		t.Errorf("openBalance = %s, want exactly 1800", statement.Totals.OpenBalance)
	}
	if statement.Totals.InvoiceCount != 3 || statement.Totals.SettlementCount != 2 {
		t.Errorf("counts = %d invoices / %d settlements, want 3 / 2",
			statement.Totals.InvoiceCount, statement.Totals.SettlementCount)
	}
	if statement.Totals.ProviderName == "" {
		t.Error("the statement names the provider by id and not by name")
	}
}

// A provider reads its own statement and nobody else's. It is 403 rather than an empty page,
// because the caller named an organization on purpose and is entitled to know its grants do not
// reach it.
func TestAProviderReadsOnlyItsOwnStatement(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := f.ctx()
	defer cancel()

	batch := f.seedBatch(t, f.provider, statementFrom.AddDate(0, 0, 9), "1000", "1000", "0")
	f.seedInvoice(t, f.provider, statementFrom.AddDate(0, 0, 2), "1000", &batch)

	if _, err := f.reports.ProviderStatement(ctx, f.providerRC(f.provider), f.provider,
		&statementFrom, &statementTo, "TRY"); err != nil {
		t.Fatalf("a provider could not read its own statement: %v", err)
	}
	_, err := f.reports.ProviderStatement(ctx, f.providerRC(f.rival), f.provider,
		&statementFrom, &statementTo, "TRY")
	if !errors.Is(err, application.ErrProviderScope) {
		t.Fatalf("a rival read somebody else's statement: %v", err)
	}
}

// Reading a statement is an audited act: it is one provider's money over a period, and a dispute
// later asks who looked at it.
func TestStatementIsAudited(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := f.ctx()
	defer cancel()

	if _, err := f.reports.ProviderStatement(ctx, f.readerRC(), f.provider,
		&statementFrom, &statementTo, "TRY"); err != nil {
		t.Fatalf("statement: %v", err)
	}
	if n := f.count(t, `
		SELECT count(*) FROM audit.event
		 WHERE tenant_id = $1 AND action_code = 'report.statement.read'
		   AND resource_id = $2`, f.tenant, f.provider); n != 1 {
		t.Errorf("the statement wrote %d audit rows, want exactly one", n)
	}
}

// equalAmount compares two exact decimals by value rather than by spelling, so "1800" and
// "1800.000000" are one figure.
func equalAmount(a, b string) bool {
	return mustQuantity(a).Cmp(mustQuantity(b)) == 0
}

// mustQuantity is the exact-decimal type this platform adds money with. It is used here and never
// in the package under test: the point of these tests is that the server did the arithmetic.
func mustQuantity(raw string) benefit.Quantity { return benefit.MustQuantity(raw) }
