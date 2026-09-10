package application_test

import (
	"testing"
	"time"

	"github.com/celikbros/kapsora/internal/report/application"
	"github.com/celikbros/kapsora/internal/report/domain"
)

// **Every count on the dashboard equals the list it links to.**
//
// The lists are asked of PostgreSQL through the admin pool, with the whole application bypassed
// and with exactly the filter the response carries — the same statuses, the same dates. A figure
// that stopped agreeing with its own filter would be a number a person clicks through and does not
// recognise, which is the one way a dashboard becomes worse than no dashboard.
func TestDashboardCountsEqualTheListsTheyLinkTo(t *testing.T) { //nolint:funlen // one seeded world, six figures
	f := newFixture(t)
	ctx, cancel := f.ctx()
	defer cancel()

	// Claims: two waiting, one already approved, and one of another provider.
	f.seedClaim(t, f.provider, "PENDING_FINANCIAL", "300", "Muayene", fixtureNow.AddDate(0, 0, -1))
	f.seedClaim(t, f.provider, "PENDING_MEDICAL", "700", "Tetkik", fixtureNow.AddDate(0, 0, -10))
	f.seedClaim(t, f.provider, "APPROVED", "500", "Kontrol", fixtureNow.AddDate(0, 0, -40))
	f.seedClaim(t, f.rival, "PENDING_FINANCIAL", "900", "Başka", fixtureNow.AddDate(0, 0, -3))

	// Icmals: one waiting for a decision, one already decided.
	f.h.AdminExec(`
		INSERT INTO billing.batch (tenant_id, reference, provider_organization_id,
		                           payer_organization_id, currency_code, period_from, period_to,
		                           status, submitted_at, submitted_by, invoice_count,
		                           submitted_total)
		VALUES ($1, $2, $3, $4, 'TRY', '2026-03-01', '2026-03-31', 'SUBMITTED',
		        clock_timestamp(), $5, 2, 4000)`,
		f.tenant, batchReference(), f.provider, f.payer, f.actor)
	f.seedBatch(t, f.provider, runDay, "1000", "1000", "0")

	// Settlements: one due inside the week, one already overdue, one paid in full.
	soon := f.seedBatch(t, f.provider, runDay, "800", "800", "0")
	f.seedSettlement(t, f.provider, soon, fixtureNow.AddDate(0, 0, 3), "800", "0", "APPROVED")
	late := f.seedBatch(t, f.provider, runDay, "600", "600", "0")
	f.seedSettlement(t, f.provider, late, fixtureNow.AddDate(0, 0, -5), "600", "100",
		"PARTIALLY_PAID")
	done := f.seedBatch(t, f.provider, runDay, "400", "400", "0")
	f.seedSettlement(t, f.provider, done, fixtureNow.AddDate(0, 0, -2), "400", "400", "PAID")

	dashboard, err := f.reports.Dashboard(ctx, f.readerRC())
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}

	// Claims by status, each against its own list.
	for _, figure := range dashboard.ClaimsByStatus {
		want := f.count(t, `SELECT count(*) FROM claim.claim
		                     WHERE tenant_id = $1 AND status = $2`, f.tenant, figure.Status)
		if int(figure.ClaimCount) != want {
			t.Errorf("claims in %s = %d, the list holds %d", figure.Status, figure.ClaimCount, want)
		}
	}
	if n := statusCount(dashboard.ClaimsByStatus, "PENDING_FINANCIAL"); n != 2 {
		t.Errorf("PENDING_FINANCIAL = %d, want both providers' claims", n)
	}
	// A closed claim is not on the operator's desk, and the figure says so.
	if n := statusCount(dashboard.ClaimsByStatus, "APPROVED"); n != 1 {
		t.Errorf("APPROVED = %d, want 1", n)
	}

	// The aging buckets: every one is drawn, even the empty ones, and each one equals its list.
	if len(dashboard.ClaimAging) != len(domain.AgingBuckets) {
		t.Fatalf("the aging figure has %d buckets, want all %d drawn",
			len(dashboard.ClaimAging), len(domain.AgingBuckets))
	}
	openTotal := int64(0)
	for _, figure := range dashboard.ClaimAging {
		openTotal += figure.ClaimCount
	}
	wantOpen := f.count(t, `SELECT count(*) FROM claim.claim
	                         WHERE tenant_id = $1 AND status IN ('SUBMITTED','AUTO_ADJUDICATED',
	                                                             'PENDING_MEDICAL','PENDING_FINANCIAL',
	                                                             'RETURNED')`, f.tenant)
	if int(openTotal) != wantOpen {
		t.Errorf("the aging buckets hold %d claims, the open list holds %d", openTotal, wantOpen)
	}
	if bucketCount(dashboard.ClaimAging, domain.BucketDayZeroToOne) != 1 {
		t.Error("the claim raised yesterday is not in the first bucket")
	}
	if bucketCount(dashboard.ClaimAging, domain.BucketDayEightToThirty) != 1 {
		t.Error("the claim raised ten days ago is not in the third bucket")
	}

	// The icmals awaiting a decision.
	wantBatches := f.count(t, `SELECT count(*) FROM billing.batch
	                            WHERE tenant_id = $1 AND status IN ('SUBMITTED','UNDER_REVIEW')`,
		f.tenant)
	if int(dashboard.Batches.BatchCount) != wantBatches || wantBatches != 1 {
		t.Errorf("batches awaiting review = %d, the list holds %d",
			dashboard.Batches.BatchCount, wantBatches)
	}
	if dashboard.Batches.SubmittedTotal != "4000" {
		t.Errorf("submitted total = %s, want exactly 4000", dashboard.Batches.SubmittedTotal)
	}
	if dashboard.Batches.OldestSubmittedAt == nil {
		t.Error("the icmal figure says nothing about how long the oldest has waited")
	}

	// The settlements, both arms against their own dates.
	if dashboard.Settlements.DueSoonCount != 1 || dashboard.Settlements.DueSoonTotal != "800" {
		t.Errorf("due this week = %d / %s, want 1 / 800",
			dashboard.Settlements.DueSoonCount, dashboard.Settlements.DueSoonTotal)
	}
	if dashboard.Settlements.OverdueCount != 1 || dashboard.Settlements.OverdueTotal != "500" {
		t.Errorf("overdue = %d / %s, want 1 / 500 (what is still open, not what is payable)",
			dashboard.Settlements.OverdueCount, dashboard.Settlements.OverdueTotal)
	}
	wantOverdue := f.count(t, `SELECT count(*) FROM billing.settlement
	                            WHERE tenant_id = $1 AND status IN ('APPROVED','POSTED','PARTIALLY_PAID')
	                              AND paid_amount < payable_amount AND due_date < $2::date`,
		f.tenant, fixtureNow)
	if int(dashboard.Settlements.OverdueCount) != wantOverdue {
		t.Errorf("overdue = %d, the list holds %d", dashboard.Settlements.OverdueCount, wantOverdue)
	}

	// And the moment every figure was computed against, which is what the aging and the week are
	// measured from.
	if !dashboard.AsOf.Equal(fixtureNow) {
		t.Errorf("asOf = %s, want the pinned clock", dashboard.AsOf)
	}
}

// A provider-scoped caller sees its own rows and nobody else's. The boundary is applied in SQL, so
// a rival's claim is not counted rather than counted and then hidden.
func TestDashboardIsBoundedByTheCallersProviderScope(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := f.ctx()
	defer cancel()

	f.seedClaim(t, f.provider, "PENDING_FINANCIAL", "300", "Muayene", fixtureNow.AddDate(0, 0, -1))
	f.seedClaim(t, f.rival, "PENDING_FINANCIAL", "900", "Başka", fixtureNow.AddDate(0, 0, -1))

	mine, err := f.reports.Dashboard(ctx, f.providerRC(f.provider))
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	if n := statusCount(mine.ClaimsByStatus, "PENDING_FINANCIAL"); n != 1 {
		t.Errorf("a provider saw %d pending claims, want only its own", n)
	}
	everyone, err := f.reports.Dashboard(ctx, f.readerRC())
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	if n := statusCount(everyone.ClaimsByStatus, "PENDING_FINANCIAL"); n != 2 {
		t.Errorf("the payer saw %d pending claims, want both", n)
	}
}

// A work item past its SLA is counted; one whose clock has not run out is not.
func TestDashboardCountsWorkPastItsSLA(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := f.ctx()
	defer cancel()

	f.h.AdminExec(`
		INSERT INTO workflow.work_item (tenant_id, queue_id, aggregate_type, aggregate_id, title,
		                                due_at, sla_minutes_snapshot, status)
		VALUES ($1, $2, 'BATCH', $3, 'Geciken iş', $4, 1440, 'OPEN')`,
		f.tenant, f.queue, f.provider, fixtureNow.Add(-2*time.Hour))
	f.h.AdminExec(`
		INSERT INTO workflow.work_item (tenant_id, queue_id, aggregate_type, aggregate_id, title,
		                                due_at, sla_minutes_snapshot, status)
		VALUES ($1, $2, 'BATCH', $3, 'Zamanında iş', $4, 1440, 'OPEN')`,
		f.tenant, f.queue, f.provider, fixtureNow.Add(48*time.Hour))

	dashboard, err := f.reports.Dashboard(ctx, f.readerRC())
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	if dashboard.WorkItemsPastSLA.ItemCount != 1 {
		t.Errorf("work past SLA = %d, want the one whose clock ran out",
			dashboard.WorkItemsPastSLA.ItemCount)
	}
	if dashboard.WorkItemsPastSLA.OldestDueAt == nil {
		t.Error("the SLA figure says nothing about how late the oldest is")
	}
}

func statusCount(figures []application.StatusFigure, status string) int64 {
	for _, f := range figures {
		if f.Status == status {
			return f.ClaimCount
		}
	}
	return 0
}

func bucketCount(figures []application.AgingFigure, bucket string) int64 {
	for _, f := range figures {
		if f.Bucket == bucket {
			return f.ClaimCount
		}
	}
	return 0
}
