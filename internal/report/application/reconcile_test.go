package application_test

import (
	"testing"
	"time"

	"github.com/celikbros/kapsora/internal/report/application"
	"github.com/celikbros/kapsora/internal/report/domain"
)

// A day on which one settlement was paid in full and one was not. It is the ordinary shape of a
// reconciliation: the run has to mark the first, notice the second, and say so once.
func (f *fixture) seedDifferingDay(t *testing.T) (paid, short string) {
	t.Helper()
	full := f.seedBatch(t, f.provider, runDay, "1000", "1000", "0")
	f.seedSettlement(t, f.provider, full, runDay, "1000", "1000", "PAID")
	partial := f.seedBatch(t, f.provider, runDay, "2000", "2000", "0")
	f.seedSettlement(t, f.provider, partial, runDay, "2000", "500", "PARTIALLY_PAID")
	return "1000", "500"
}

// **The run compares, marks and raises — once each.**
//
// Four properties in one pass, because they are four halves of one transaction and testing them
// apart would be testing something the service never does:
//
//   - the totals are the database's sums of the day;
//   - a settlement whose paid amount equals its payable amount is RECONCILED afterwards, and one
//     that is short is not;
//   - a differing run raises exactly one work item;
//   - the difference names the settlement by reference and carries both figures.
func TestReconciliationMarksWhatIsPaidAndRaisesOneItem(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := f.ctx()
	defer cancel()
	f.seedDifferingDay(t)

	report, err := f.reports.Reconcile(ctx, runDay)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	// One TENANT run and one PROVIDER run, both differing.
	if report.Runs != 2 || report.Differing != 2 {
		t.Fatalf("wrote %d runs of which %d differ, want 2 and 2", report.Runs, report.Differing)
	}
	if report.Reconciled != 1 {
		t.Errorf("marked %d settlements RECONCILED, want exactly the one that was paid in full",
			report.Reconciled)
	}

	page, err := f.reports.ListReconciliationRuns(ctx, f.readerRC(), application.RunFilter{})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("listed %d runs, want 2", len(page.Items))
	}
	for _, run := range page.Items {
		if run.Status != domain.RunDifferences {
			t.Errorf("%s run is %s, want DIFFERENCES", run.Scope, run.Status)
		}
		if run.SettledTotal != "3000" || run.PaidTotal != "1500" {
			t.Errorf("%s run settled/paid = %s/%s, want 3000/1500",
				run.Scope, run.SettledTotal, run.PaidTotal)
		}
		if run.OpenTotal != "1500" || run.Difference != "1500" {
			t.Errorf("%s run open/difference = %s/%s, want 1500/1500",
				run.Scope, run.OpenTotal, run.Difference)
		}
		if run.ERPTotal != "" {
			t.Errorf("%s run carries an ERP total (%s) before M9", run.Scope, run.ERPTotal)
		}
		if run.DifferenceCount != 1 || len(run.Differences) != 1 {
			t.Fatalf("%s run recorded %d differences, want the one that is short",
				run.Scope, run.DifferenceCount)
		}
		d := run.Differences[0]
		if d.Kind != domain.DifferenceUnderpaid {
			t.Errorf("difference kind = %s, want UNDERPAID", d.Kind)
		}
		if d.ExpectedAmount != "2000" || d.ActualAmount != "500" {
			t.Errorf("difference expected/actual = %s/%s, want 2000/500",
				d.ExpectedAmount, d.ActualAmount)
		}
		if d.SettlementReference == "" {
			t.Error("the difference names no settlement reference")
		}
		// **No identifier in the run's own column.** A person reads it looking for the row on
		// their own screen, and a uuid is not how they will find it.
		if d.SettlementID.String() != "00000000-0000-0000-0000-000000000000" {
			t.Errorf("the stored difference carries a settlement id (%s)", d.SettlementID)
		}
	}

	// The settlements: one marked by the run, one left alone.
	if n := f.count(t, `SELECT count(*) FROM billing.settlement
	                     WHERE tenant_id = $1 AND status = 'RECONCILED'`, f.tenant); n != 1 {
		t.Errorf("%d settlements are RECONCILED, want exactly the fully paid one", n)
	}
	if n := f.count(t, `SELECT count(*) FROM billing.settlement
	                     WHERE tenant_id = $1 AND status = 'RECONCILED'
	                       AND paid_amount <> payable_amount`, f.tenant); n != 0 {
		t.Errorf("%d settlements are RECONCILED while short", n)
	}

	// **Exactly one work item per differing run**, and not one per difference.
	for _, run := range page.Items {
		if n := f.count(t, `SELECT count(*) FROM workflow.work_item
		                     WHERE tenant_id = $1 AND aggregate_type = $2 AND aggregate_id = $3`,
			f.tenant, domain.AggregateReconciliationRun, run.ID); n != 1 {
			t.Errorf("%s run raised %d work items, want exactly one", run.Scope, n)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM workflow.work_item
	                     WHERE tenant_id = $1 AND aggregate_type = $2`,
		f.tenant, domain.AggregateReconciliationRun); n != 2 {
		t.Errorf("the sweep raised %d work items altogether, want one per differing run", n)
	}
}

// A day on which everything due was paid balances, marks both settlements and raises nothing.
func TestABalancedDayRaisesNothing(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := f.ctx()
	defer cancel()

	first := f.seedBatch(t, f.provider, runDay, "1000", "1000", "0")
	f.seedSettlement(t, f.provider, first, runDay, "1000", "1000", "PAID")
	second := f.seedBatch(t, f.provider, runDay, "250.50", "250.50", "0")
	f.seedSettlement(t, f.provider, second, runDay, "250.50", "250.50", "PAID")

	report, err := f.reports.Reconcile(ctx, runDay)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if report.Differing != 0 || report.WorkItems != 0 {
		t.Errorf("a balanced day produced %d differing runs and %d work items",
			report.Differing, report.WorkItems)
	}
	if report.Reconciled != 2 {
		t.Errorf("marked %d settlements, want both", report.Reconciled)
	}
	page, err := f.reports.ListReconciliationRuns(ctx, f.readerRC(), application.RunFilter{})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	for _, run := range page.Items {
		if run.Status != domain.RunBalanced || run.DifferenceCount != 0 {
			t.Errorf("%s run is %s with %d differences, want BALANCED with none",
				run.Scope, run.Status, run.DifferenceCount)
		}
		if run.OpenTotal != "0" || run.Difference != "0" {
			t.Errorf("%s run open/difference = %s/%s on a balanced day",
				run.Scope, run.OpenTotal, run.Difference)
		}
	}
}

// A settlement that is not yet due and not yet paid is not a difference. Without this the job
// would raise a work item every night for every invoice in the system that has not fallen due.
func TestASettlementNotYetDueIsNotADifference(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := f.ctx()
	defer cancel()

	future := fixtureNow.AddDate(0, 0, 30).Truncate(24 * time.Hour)
	batch := f.seedBatch(t, f.provider, future, "5000", "5000", "0")
	f.seedSettlement(t, f.provider, batch, future, "5000", "0", "APPROVED")

	report, err := f.reports.Reconcile(ctx, future)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if report.Differing != 0 {
		t.Errorf("a settlement due in the future produced %d differing runs", report.Differing)
	}
}

// **A run is written, never rewritten.** A second sweep of the same day is run number two and
// both stay: "we looked again and it balanced" is part of the record, and a run somebody could
// correct afterwards would be a record of the argument's outcome rather than of the argument.
func TestASecondSweepInsertsRatherThanUpdates(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := f.ctx()
	defer cancel()
	f.seedDifferingDay(t)

	if _, err := f.reports.Reconcile(ctx, runDay); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	before := f.scalar(t, `
		SELECT string_agg(id::text || ':' || run_no::text || ':' || status, ',' ORDER BY id)
		  FROM billing.reconciliation_run WHERE tenant_id = $1`, f.tenant)

	if _, err := f.reports.Reconcile(ctx, runDay); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM billing.reconciliation_run WHERE tenant_id = $1`,
		f.tenant); n != 4 {
		t.Fatalf("the table holds %d runs after two sweeps, want 4", n)
	}
	after := f.scalar(t, `
		SELECT string_agg(id::text || ':' || run_no::text || ':' || status, ',' ORDER BY id)
		  FROM billing.reconciliation_run WHERE tenant_id = $1 AND run_no = 1`, f.tenant)
	if after != before {
		t.Errorf("the first sweep's runs changed:\n before %s\n after  %s", before, after)
	}
	if n := f.count(t, `SELECT count(*) FROM billing.reconciliation_run
	                     WHERE tenant_id = $1 AND run_no = 2`, f.tenant); n != 2 {
		t.Errorf("the second sweep wrote %d runs numbered two, want 2", n)
	}
	// The second sweep finds nothing left to mark: RECONCILED is a predicate, not a decision.
	if n := f.count(t, `SELECT count(*) FROM billing.settlement
	                     WHERE tenant_id = $1 AND status = 'RECONCILED'`, f.tenant); n != 1 {
		t.Errorf("%d settlements are RECONCILED after two sweeps, want 1", n)
	}
}

// The tenant-wide run's figures are exactly the sum of its providers'. It is the property that
// makes the two scopes worth having: the first says whether the day balanced, and the second says
// which provider is the reason it did not.
func TestTheTenantRunIsTheSumOfTheProviderRuns(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := f.ctx()
	defer cancel()

	mine := f.seedBatch(t, f.provider, runDay, "1000", "1000", "0")
	f.seedSettlement(t, f.provider, mine, runDay, "1000", "1000", "PAID")
	theirs := f.seedBatch(t, f.rival, runDay, "400.40", "400.40", "0")
	f.seedSettlement(t, f.rival, theirs, runDay, "400.40", "100.40", "PARTIALLY_PAID")

	if _, err := f.reports.Reconcile(ctx, runDay); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	page, err := f.reports.ListReconciliationRuns(ctx, f.readerRC(), application.RunFilter{})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	var tenantRun application.ReconciliationRun
	providerSettled := "0"
	providerPaid := "0"
	for _, run := range page.Items {
		if run.Scope == domain.ScopeTenantWide {
			tenantRun = run
			continue
		}
		providerSettled = addAmount(providerSettled, run.SettledTotal)
		providerPaid = addAmount(providerPaid, run.PaidTotal)
	}
	if !equalAmount(tenantRun.SettledTotal, providerSettled) {
		t.Errorf("tenant settled = %s, providers add up to %s",
			tenantRun.SettledTotal, providerSettled)
	}
	if !equalAmount(tenantRun.PaidTotal, providerPaid) {
		t.Errorf("tenant paid = %s, providers add up to %s", tenantRun.PaidTotal, providerPaid)
	}
	if tenantRun.SettledTotal != "1400.4" {
		t.Errorf("tenant settled = %s, want exactly 1400.4", tenantRun.SettledTotal)
	}
}

// A provider-scoped caller reads its own PROVIDER runs and never the tenant-wide ones: a TENANT
// run is every provider's figures added together.
func TestAProviderReadsOnlyItsOwnRuns(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := f.ctx()
	defer cancel()

	mine := f.seedBatch(t, f.provider, runDay, "1000", "1000", "0")
	f.seedSettlement(t, f.provider, mine, runDay, "1000", "1000", "PAID")
	theirs := f.seedBatch(t, f.rival, runDay, "500", "500", "0")
	f.seedSettlement(t, f.rival, theirs, runDay, "500", "500", "PAID")

	if _, err := f.reports.Reconcile(ctx, runDay); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	page, err := f.reports.ListReconciliationRuns(ctx, f.providerRC(f.provider),
		application.RunFilter{})
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("a provider saw %d runs, want only its own", len(page.Items))
	}
	if page.Items[0].Scope != domain.ScopeProvider ||
		page.Items[0].ProviderOrganizationID == nil ||
		*page.Items[0].ProviderOrganizationID != f.provider {
		t.Errorf("a provider saw a run that is not its own: %+v", page.Items[0])
	}
}

// addAmount adds two exact decimals the way the database would.
func addAmount(a, b string) string {
	return mustQuantity(a).Add(mustQuantity(b)).String()
}
