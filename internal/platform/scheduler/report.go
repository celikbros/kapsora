package scheduler

import (
	"context"
	"time"

	reportapp "github.com/celikbros/kapsora/internal/report/application"
)

// BillingReconcile compares yesterday against itself, every day, for every tenant (WP-I7-05
// section 2.3).
//
// It runs on a daily slot rather than at a wall-clock hour, because this scheduler has intervals
// and not cron expressions: the slot is midnight UTC, which is the small hours in every tenant's
// own zone and well after the last person has stopped entering payment records. The one thing
// that matters about the timing is that the day being reconciled is over, and it is.
//
// The sweep is idempotent, and not by trying to be. Every run is an insert into an append-only
// table, so a second pass of the same day writes run number two rather than editing run number
// one — "we looked again and it balanced" is part of the record. `RECONCILED` is written by a
// predicate that names PAID and equality, with a CHECK underneath it, so a second pass finds
// nothing left to mark. And the work item a differing run raises is written inside that run's own
// transaction, which is what makes "exactly one work item per run" true rather than likely.
func BillingReconcile(svc *reportapp.Service) Job {
	return Job{
		Code:  "billing.reconcile",
		Every: 24 * time.Hour,
		Run: func(ctx context.Context) (Metrics, error) {
			// Yesterday. A run of today would be a run of a day people are still working in,
			// and every settlement due this afternoon would be reported as underpaid.
			yesterday := time.Now().UTC().AddDate(0, 0, -1)
			report, err := svc.Reconcile(ctx, yesterday)
			return Metrics{
				"tenants": report.Tenants, "runs": report.Runs,
				"differing": report.Differing, "reconciled": report.Reconciled,
				"work_items": report.WorkItems,
			}, err
		},
	}
}

// ReportExportExpire removes the file of every export past its TTL and marks the row EXPIRED
// (WP-I7-05 section 2.5).
//
// Hourly rather than nightly, and the difference matters: the TTL is a promise about when a link
// stops working, and a tenant that configured four hours would otherwise keep its files for
// twenty. The refusal at download time does not depend on this job — `downloadExport` compares
// the clock — so this is the sweep that makes the bytes go, not the one that makes the promise.
//
// The row outlives the file, exactly as a purged document's row outlives its bytes: "this export
// existed, carried this many rows and stopped being downloadable on this day" is an answer
// somebody will need. A file under legal hold keeps its bytes and its row is marked all the same,
// and the sweep counts those separately, because "nothing expired" and "everything was held" are
// different answers.
func ReportExportExpire(svc *reportapp.Service) Job {
	return Job{
		Code:  "report.export_expire",
		Every: time.Hour,
		Run: func(ctx context.Context) (Metrics, error) {
			report, err := svc.ExpireExports(ctx, time.Now().UTC())
			return Metrics{
				"tenants": report.Tenants, "expired": report.Expired, "held": report.Held,
			}, err
		},
	}
}
