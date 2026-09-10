package application

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/report/domain"
)

// Reconcile runs the daily comparison for one day: one TENANT run per currency that moved, and
// one PROVIDER run for every provider with a settlement due that day.
//
// Three properties make this safe to run twice, on two schedulers, on a day it has already run:
//
//   - **every run is an insert.** The table is append-only, so a second sweep of the same day
//     writes run number two rather than editing run number one, and both stay. "We looked again
//     and it balanced" is part of the record;
//   - **`RECONCILED` is a predicate, not a decision.** The statement names PAID and equality, and
//     the CHECK is underneath it, so a second pass finds nothing left to mark rather than marking
//     something twice;
//   - **each run is one transaction.** The run, its work item and the settlements it marked commit
//     together or not at all, which is what makes "a difference produces exactly one work item"
//     true rather than likely.
//
// A tenant whose sweep fails does not stop the others: the error is logged with the tenant and the
// sweep carries on, because one tenant's misconfigured work queue is not a reason nobody else's
// numbers get reconciled.
func (s *Service) Reconcile(ctx context.Context, day time.Time) (ReconcileReport, error) {
	period := day.UTC().Truncate(24 * time.Hour)
	tenants, err := s.activeTenants(ctx)
	if err != nil {
		return ReconcileReport{}, err
	}

	report := ReconcileReport{Tenants: len(tenants)}
	for _, tenantID := range tenants {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		tenantReport, err := s.reconcileTenant(ctx, tenantID, period)
		report.Runs += tenantReport.Runs
		report.Differing += tenantReport.Differing
		report.Reconciled += tenantReport.Reconciled
		report.WorkItems += tenantReport.WorkItems
		if err != nil {
			s.logger.Error("report: reconciliation failed for a tenant",
				"tenant_id", tenantID, "period", period.Format(time.DateOnly), "error", err)
		}
	}
	return report, nil
}

func (s *Service) activeTenants(ctx context.Context) ([]uuid.UUID, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tenants, err := s.repo.ActiveTenants(ctx, tx)
	if err != nil {
		return nil, err
	}
	return tenants, tx.Commit(ctx)
}

// reconcileTenant runs one tenant's day. The currencies and the providers are read first, in a
// transaction of their own, so that the list of runs to make is decided once rather than growing
// underneath the loop that is making them.
func (s *Service) reconcileTenant(ctx context.Context, tenantID uuid.UUID, period time.Time,
) (ReconcileReport, error) {
	var currencies []string
	if err := s.withSystemTx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		currencies, err = s.repo.ReconciliationCurrencies(ctx, tx, tenantID, period, period)
		return err
	}); err != nil {
		return ReconcileReport{}, err
	}

	report := ReconcileReport{}
	for _, currency := range currencies {
		// The tenant-wide run first: it is the figure somebody looks at, and the provider runs
		// below it are the answer to "which one is the reason".
		if err := s.runOne(ctx, tenantID, nil, currency, period, &report); err != nil {
			return report, err
		}
		var providers []uuid.UUID
		if err := s.withSystemTx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			providers, err = s.repo.ReconciliationProviders(ctx, tx, tenantID, currency, period, period)
			return err
		}); err != nil {
			return report, err
		}
		for _, provider := range providers {
			if err := s.runOne(ctx, tenantID, &provider, currency, period, &report); err != nil {
				return report, err
			}
		}
	}
	return report, nil
}

// runOne writes one run and everything that follows from it, in one transaction.
func (s *Service) runOne(ctx context.Context, tenantID uuid.UUID, provider *uuid.UUID,
	currency string, period time.Time, report *ReconcileReport,
) error {
	q := RunQuery{
		ProviderOrganizationID: provider, CurrencyCode: currency,
		PeriodFrom: period, PeriodTo: period,
	}
	scope := domain.ScopeTenantWide
	if provider != nil {
		scope = domain.ScopeProvider
	}
	ranAt := s.now().UTC()

	return s.withSystemTx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		totals, err := s.repo.ReconciliationTotals(ctx, tx, tenantID, q)
		if err != nil {
			return err
		}
		differences, err := s.repo.ReconciliationDifferences(ctx, tx, tenantID, q, ranAt,
			domain.MaxDifferences)
		if err != nil {
			return err
		}

		status := domain.RunBalanced
		if len(differences) > 0 {
			status = domain.RunDifferences
		}
		runNo, err := s.repo.NextRunNo(ctx, tx, tenantID, scope, provider, period, period, currency)
		if err != nil {
			return err
		}
		run, err := s.repo.CreateRun(ctx, tx, tenantID, NewRun{
			Scope: scope, ProviderOrganizationID: provider,
			PeriodFrom: period, PeriodTo: period, RunNo: runNo, CurrencyCode: currency,
			Totals: totals,
			// The ERP has not spoken and will not until M9, so the difference this run records
			// is settled minus paid — which is what `OpenTotal` already is, summed by
			// PostgreSQL. Nothing is subtracted here. When M9 supplies a figure, the difference
			// becomes settled minus that one and it will be subtracted where the other sums
			// are: in SQL.
			ERPTotal: nil, Difference: totals.OpenTotal,
			Differences: differences, Status: status, RanAt: ranAt,
		})
		if err != nil {
			return err
		}
		report.Runs++

		// **Exactly one work item per differing run.** It is raised here, inside the run's own
		// transaction, so it cannot exist without the run and the run cannot exist without it.
		if status == domain.RunDifferences {
			report.Differing++
			if err := s.workItems.Raise(ctx, tx, tenantID, RaiseWorkItem{
				QueueCode:     domain.QueueReconciliationDifference,
				AggregateType: domain.AggregateReconciliationRun,
				AggregateID:   run.ID,
				Title:         reconciliationWorkItemTitle(run),
			}); err != nil {
				return err
			}
			report.WorkItems++
		}

		// **The run marks what it found settled.** A settlement is RECONCILED here and nowhere
		// else in this codebase.
		marked, err := s.repo.MarkSettlementsReconciled(ctx, tx, tenantID, q, nil)
		if err != nil {
			return err
		}
		report.Reconciled += marked

		return s.recordSystem(ctx, tx, tenantID, "billing.reconciliation.run",
			domain.AggregateReconciliationRun, run.ID, map[string]any{
				"scope":            scope,
				"currency_code":    currency,
				"run_no":           runNo,
				"status":           status,
				"difference_count": len(differences),
				"settlement_count": totals.SettlementCount,
				"reconciled_count": marked,
				"period":           period.Format(time.DateOnly),
			})
	})
}

// reconciliationWorkItemTitle is what a person sees in the finance queue. It names the period, the
// currency and how many settlements disagreed, because "reconciliation difference" on its own is a
// title that tells whoever picks it up nothing they did not already know from the queue's name.
func reconciliationWorkItemTitle(run ReconciliationRun) string {
	return fmt.Sprintf("Mutabakat farkı: %s %s, %d kayıt",
		run.PeriodFrom.Format(time.DateOnly), run.CurrencyCode, run.DifferenceCount)
}

// GetReconciliationRun reads one run. A run outside the caller's provider scope is not found
// rather than refused: that it exists at all is somebody else's business.
func (s *Service) GetReconciliationRun(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID,
) (ReconciliationRun, error) {
	var out ReconciliationRun
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = s.repo.GetRun(ctx, tx, rc.TenantID, id, scopeOf(rc))
		return err
	})
	if err != nil {
		return ReconciliationRun{}, err
	}
	return out, nil
}

// ListReconciliationRuns pages the tenant's runs, newest first.
func (s *Service) ListReconciliationRuns(ctx context.Context, rc identity.RequestContext,
	f RunFilter,
) (RunPage, error) {
	after, pageSize, err := s.paging(f.Cursor, f.Limit)
	if err != nil {
		return RunPage{}, err
	}
	if f.Scope != "" && !domain.ValidScope(f.Scope) {
		return RunPage{}, fieldError("scope", "ENUM", "geçerli bir mutabakat kapsamı olmalı")
	}
	if f.Status != "" && !domain.ValidRunStatus(f.Status) {
		return RunPage{}, fieldError("status", "ENUM", "geçerli bir mutabakat durumu olmalı")
	}
	scope := scopeOf(rc)
	if f.ProviderOrganizationID != nil && !scope.Allows(*f.ProviderOrganizationID) {
		return RunPage{}, ErrProviderScope
	}

	var page RunPage
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := s.repo.ListRuns(ctx, tx, rc.TenantID, RunQueryOptions{
			Scope: scope, RunScope: f.Scope, Status: f.Status,
			ProviderOrganizationID: f.ProviderOrganizationID,
			PeriodFrom:             f.PeriodFrom, PeriodTo: f.PeriodTo,
			After: after, PageSize: pageSize + 1,
		})
		if err != nil {
			return err
		}
		if len(rows) > pageSize {
			rows = rows[:pageSize]
			page.NextCursor = s.cursors.Encode(runCursor(rows[len(rows)-1]))
		}
		page.Items = rows
		return nil
	})
	if err != nil {
		return RunPage{}, err
	}
	return page, nil
}
