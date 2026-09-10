// Package reportpg implements the report module's persistence port on the sqlc-generated
// queries of db/queries/report.sql.
//
// It is a mapper and nothing else. Every sum this package hands upward was computed by
// PostgreSQL, every amount travels as canonical decimal text, and there is not one arithmetic
// operator in this file. That is the point of the seam: a repository that added two amounts
// together would be a repository that decided what a figure means.
package reportpg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
	"github.com/celikbros/kapsora/internal/report/application"
	"github.com/celikbros/kapsora/internal/report/domain"
)

// Repository implements application.Repository.
type Repository struct{}

// New returns the repository.
func New() *Repository { return &Repository{} }

var _ application.Repository = (*Repository)(nil)

// ---------------------------------------------------------------------------
// The provider statement
// ---------------------------------------------------------------------------

// StatementTotals implements application.Repository.
func (Repository) StatementTotals(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.StatementQuery,
) (application.StatementTotals, error) {
	row, err := sqlcgen.New(tx).GetProviderStatementTotals(ctx,
		sqlcgen.GetProviderStatementTotalsParams{
			TenantID: tenantID, ProviderOrganizationID: q.ProviderOrganizationID,
			CurrencyCode: q.CurrencyCode,
			PeriodFrom:   dateParam(q.PeriodFrom), PeriodTo: dateParam(q.PeriodTo),
		})
	if err != nil {
		return application.StatementTotals{}, fmt.Errorf("report: statement totals: %w", err)
	}
	out := application.StatementTotals{
		InvoicedTotal: row.InvoicedTotal, ApprovedTotal: row.ApprovedTotal,
		CutTotal: row.CutTotal, ReturnedTotal: row.ReturnedTotal,
		RejectedTotal: row.RejectedTotal, SettledTotal: row.SettledTotal,
		PaidTotal: row.PaidTotal, OpenBalance: row.OpenBalance,
		InvoiceCount: row.InvoiceCount, SettlementCount: row.SettlementCount,
	}
	out.ProviderName = row.ProviderName
	return out, nil
}

// StatementInvoices implements application.Repository.
func (Repository) StatementInvoices(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.StatementQuery,
) ([]application.StatementInvoice, error) {
	rows, err := sqlcgen.New(tx).ListProviderStatementInvoices(ctx,
		sqlcgen.ListProviderStatementInvoicesParams{
			TenantID: tenantID, ProviderOrganizationID: q.ProviderOrganizationID,
			CurrencyCode: q.CurrencyCode,
			PeriodFrom:   dateParam(q.PeriodFrom), PeriodTo: dateParam(q.PeriodTo),
			RowLimit: int32(q.RowLimit), //nolint:gosec // bounded by domain.MaxStatementRows
		})
	if err != nil {
		return nil, fmt.Errorf("report: statement invoices: %w", err)
	}
	out := make([]application.StatementInvoice, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.StatementInvoice{
			ID: r.ID, InvoiceNumber: r.InvoiceNumber, InvoiceDate: dateValue(r.InvoiceDate),
			Status: r.Status, CurrencyCode: r.CurrencyCode,
			PayableAmount: r.PayableAmount, TaxAmount: r.TaxAmount,
			BatchID: uuidPtr(r.BatchID), BatchReference: r.BatchReference,
			BatchStatus: r.BatchStatus, BatchDecision: r.BatchDecision,
			ApprovedAmount: r.ApprovedAmount, SettlementID: uuidPtr(r.SettlementID),
			SettlementReference: r.SettlementReference, DueDate: optDateValue(r.DueDate),
			SettlementPaidAmount: r.SettlementPaidAmount,
		})
	}
	return out, nil
}

// StatementSettlements implements application.Repository.
func (Repository) StatementSettlements(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.StatementQuery,
) ([]application.StatementSettlement, error) {
	rows, err := sqlcgen.New(tx).ListProviderStatementSettlements(ctx,
		sqlcgen.ListProviderStatementSettlementsParams{
			TenantID: tenantID, ProviderOrganizationID: q.ProviderOrganizationID,
			CurrencyCode: q.CurrencyCode,
			PeriodFrom:   dateParam(q.PeriodFrom), PeriodTo: dateParam(q.PeriodTo),
			RowLimit: int32(q.RowLimit), //nolint:gosec // bounded by domain.MaxStatementRows
		})
	if err != nil {
		return nil, fmt.Errorf("report: statement settlements: %w", err)
	}
	out := make([]application.StatementSettlement, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.StatementSettlement{
			ID: r.ID, Reference: r.Reference, BatchID: r.BatchID,
			BatchReference: r.BatchReference, DueDate: dateValue(r.DueDate), Status: r.Status,
			CurrencyCode: r.CurrencyCode, ApprovedAmount: r.ApprovedAmount,
			WithheldAmount: r.WithheldAmount, PayableAmount: r.PayableAmount,
			PaidAmount: r.PaidAmount, OpenAmount: r.OpenAmount,
			PaymentCount: r.PaymentCount, LastPaidAt: anyTime(r.LastPaidAt),
		})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Reconciliation
// ---------------------------------------------------------------------------

// ReconciliationCurrencies implements application.Repository.
func (Repository) ReconciliationCurrencies(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	from, to time.Time,
) ([]string, error) {
	rows, err := sqlcgen.New(tx).ListReconciliationCurrencies(ctx,
		sqlcgen.ListReconciliationCurrenciesParams{
			TenantID: tenantID, PeriodFrom: dateParam(from), PeriodTo: dateParam(to),
		})
	if err != nil {
		return nil, fmt.Errorf("report: reconciliation currencies: %w", err)
	}
	return rows, nil
}

// ReconciliationProviders implements application.Repository.
func (Repository) ReconciliationProviders(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	currency string, from, to time.Time,
) ([]uuid.UUID, error) {
	rows, err := sqlcgen.New(tx).ListReconciliationProviders(ctx,
		sqlcgen.ListReconciliationProvidersParams{
			TenantID: tenantID, CurrencyCode: currency,
			PeriodFrom: dateParam(from), PeriodTo: dateParam(to),
		})
	if err != nil {
		return nil, fmt.Errorf("report: reconciliation providers: %w", err)
	}
	return rows, nil
}

// ReconciliationTotals implements application.Repository.
func (Repository) ReconciliationTotals(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.RunQuery,
) (application.ReconciliationTotals, error) {
	row, err := sqlcgen.New(tx).ComputeReconciliationTotals(ctx,
		sqlcgen.ComputeReconciliationTotalsParams{
			TenantID: tenantID, CurrencyCode: q.CurrencyCode,
			ProviderOrganizationID: optUUID(q.ProviderOrganizationID),
			PeriodFrom:             dateParam(q.PeriodFrom), PeriodTo: dateParam(q.PeriodTo),
		})
	if err != nil {
		return application.ReconciliationTotals{}, fmt.Errorf("report: reconciliation totals: %w", err)
	}
	return application.ReconciliationTotals{
		InvoicedTotal: row.InvoicedTotal, ApprovedTotal: row.ApprovedTotal,
		CutTotal: row.CutTotal, ReturnedTotal: row.ReturnedTotal,
		RejectedTotal: row.RejectedTotal, SettledTotal: row.SettledTotal,
		PaidTotal: row.PaidTotal, OpenTotal: row.OpenTotal,
		SettlementCount: row.SettlementCount,
	}, nil
}

// ReconciliationDifferences implements application.Repository.
func (Repository) ReconciliationDifferences(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.RunQuery, asOf time.Time, limit int,
) ([]application.ReconciliationDifference, error) {
	rows, err := sqlcgen.New(tx).ListReconciliationDifferences(ctx,
		sqlcgen.ListReconciliationDifferencesParams{
			TenantID: tenantID, CurrencyCode: q.CurrencyCode,
			ProviderOrganizationID: optUUID(q.ProviderOrganizationID),
			PeriodFrom:             dateParam(q.PeriodFrom), PeriodTo: dateParam(q.PeriodTo),
			AsOf:     dateParam(asOf),
			RowLimit: int32(limit), //nolint:gosec // bounded by domain.MaxDifferences
		})
	if err != nil {
		return nil, fmt.Errorf("report: reconciliation differences: %w", err)
	}
	out := make([]application.ReconciliationDifference, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.ReconciliationDifference{
			SettlementID: r.ID, SettlementReference: r.Reference,
			DueDate: dateValue(r.DueDate), Status: r.Status,
			ExpectedAmount: r.ExpectedAmount, ActualAmount: r.ActualAmount,
			DifferenceAmount: r.DifferenceAmount, Kind: r.DifferenceKind,
		})
	}
	return out, nil
}

// NextRunNo implements application.Repository.
func (Repository) NextRunNo(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope string,
	provider *uuid.UUID, from, to time.Time, currency string,
) (int, error) {
	n, err := sqlcgen.New(tx).NextReconciliationRunNo(ctx, sqlcgen.NextReconciliationRunNoParams{
		TenantID: tenantID, Scope: scope, ProviderOrganizationID: optUUID(provider),
		PeriodFrom: dateParam(from), PeriodTo: dateParam(to), CurrencyCode: currency,
	})
	if err != nil {
		return 0, fmt.Errorf("report: next run number: %w", err)
	}
	return int(n), nil
}

// CreateRun implements application.Repository.
func (Repository) CreateRun(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewRun,
) (application.ReconciliationRun, error) {
	differences, err := encodeDifferences(in.Differences)
	if err != nil {
		return application.ReconciliationRun{}, err
	}
	row, err := sqlcgen.New(tx).CreateReconciliationRun(ctx,
		sqlcgen.CreateReconciliationRunParams{
			TenantID: tenantID, Scope: in.Scope,
			ProviderOrganizationID: optUUID(in.ProviderOrganizationID),
			PeriodFrom:             dateParam(in.PeriodFrom), PeriodTo: dateParam(in.PeriodTo),
			RunNo:           int32(in.RunNo), //nolint:gosec // a run number is small by construction
			CurrencyCode:    in.CurrencyCode,
			InvoicedTotal:   in.Totals.InvoicedTotal,
			ApprovedTotal:   in.Totals.ApprovedTotal,
			CutTotal:        in.Totals.CutTotal,
			ReturnedTotal:   in.Totals.ReturnedTotal,
			RejectedTotal:   in.Totals.RejectedTotal,
			SettledTotal:    in.Totals.SettledTotal,
			PaidTotal:       in.Totals.PaidTotal,
			OpenTotal:       in.Totals.OpenTotal,
			ErpTotal:        in.ERPTotal,
			Difference:      in.Difference,
			DifferenceCount: int32(len(in.Differences)), //nolint:gosec // bounded by domain.MaxDifferences
			Differences:     differences,
			Status:          in.Status, FailureCode: in.FailureCode,
			RanAt: in.RanAt, ActorID: optUUID(in.ActorID),
		})
	if err != nil {
		return application.ReconciliationRun{}, fmt.Errorf("report: create reconciliation run: %w", err)
	}
	return runFrom(createdRunRow(row))
}

// GetRun implements application.Repository.
func (Repository) GetRun(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.ReconciliationRun, error) {
	row, err := sqlcgen.New(tx).GetReconciliationRun(ctx, sqlcgen.GetReconciliationRunParams{
		TenantID: tenantID, ID: id, ScopeIds: scope.IDs(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ReconciliationRun{}, application.ErrRunNotFound
	}
	if err != nil {
		return application.ReconciliationRun{}, fmt.Errorf("report: read reconciliation run: %w", err)
	}
	return runFrom(row)
}

// ListRuns implements application.Repository.
func (Repository) ListRuns(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.RunQueryOptions,
) ([]application.ReconciliationRun, error) {
	params := sqlcgen.ListReconciliationRunsParams{
		TenantID: tenantID, ScopeIds: q.Scope.IDs(),
		Scope: emptyToNil(q.RunScope), Status: emptyToNil(q.Status),
		ProviderOrganizationID: optUUID(q.ProviderOrganizationID),
		PeriodFrom:             optDate(q.PeriodFrom), PeriodTo: optDate(q.PeriodTo),
		PageSize: int32(q.PageSize), //nolint:gosec // clamped by httpx.ClampLimit
	}
	if q.After != nil {
		params.AfterCreatedAt = &q.After.CreatedAt
		params.AfterID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListReconciliationRuns(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("report: list reconciliation runs: %w", err)
	}
	out := make([]application.ReconciliationRun, 0, len(rows))
	for _, r := range rows {
		run, err := runFrom(listRunRow(r))
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, nil
}

// MarkSettlementsReconciled implements application.Repository.
func (Repository) MarkSettlementsReconciled(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.RunQuery, actorID *uuid.UUID,
) (int64, error) {
	n, err := sqlcgen.New(tx).MarkSettlementsReconciled(ctx,
		sqlcgen.MarkSettlementsReconciledParams{
			TenantID: tenantID, CurrencyCode: q.CurrencyCode,
			ProviderOrganizationID: optUUID(q.ProviderOrganizationID),
			PeriodFrom:             dateParam(q.PeriodFrom), PeriodTo: dateParam(q.PeriodTo),
			ActorID: optUUID(actorID),
		})
	if err != nil {
		return 0, fmt.Errorf("report: mark settlements reconciled: %w", err)
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// The dashboard
// ---------------------------------------------------------------------------

// ClaimsByStatus implements application.Repository.
func (Repository) ClaimsByStatus(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	scope application.Scope,
) ([]application.StatusFigure, error) {
	rows, err := sqlcgen.New(tx).DashboardClaimsByStatus(ctx,
		sqlcgen.DashboardClaimsByStatusParams{TenantID: tenantID, ScopeIds: scope.IDs()})
	if err != nil {
		return nil, fmt.Errorf("report: dashboard claims: %w", err)
	}
	out := make([]application.StatusFigure, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.StatusFigure{
			Status: r.Status, ClaimCount: r.ClaimCount, ApprovedTotal: r.ApprovedTotal,
		})
	}
	return out, nil
}

// ClaimAging implements application.Repository.
func (Repository) ClaimAging(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	scope application.Scope, asOf time.Time,
) ([]application.AgingFigure, error) {
	rows, err := sqlcgen.New(tx).DashboardClaimAging(ctx, sqlcgen.DashboardClaimAgingParams{
		TenantID: tenantID, ScopeIds: scope.IDs(), AsOf: asOf,
	})
	if err != nil {
		return nil, fmt.Errorf("report: dashboard aging: %w", err)
	}
	// The empty buckets are filled in here rather than left out, so a screen draws four columns
	// on a quiet morning and four columns on a busy one. The order is the domain's.
	found := make(map[string]application.AgingFigure, len(rows))
	for _, r := range rows {
		found[r.Bucket] = application.AgingFigure{
			Bucket: r.Bucket, ClaimCount: r.ClaimCount, ApprovedTotal: r.ApprovedTotal,
		}
	}
	out := make([]application.AgingFigure, 0, len(domain.AgingBuckets))
	for _, bucket := range domain.AgingBuckets {
		figure, ok := found[bucket]
		if !ok {
			figure = application.AgingFigure{Bucket: bucket, ClaimCount: 0, ApprovedTotal: "0"}
		}
		out = append(out, figure)
	}
	return out, nil
}

// BatchesAwaitingReview implements application.Repository.
func (Repository) BatchesAwaitingReview(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	scope application.Scope, queueCode string,
) (application.BatchFigure, error) {
	row, err := sqlcgen.New(tx).DashboardBatchesAwaitingReview(ctx,
		sqlcgen.DashboardBatchesAwaitingReviewParams{
			TenantID: tenantID, ReviewQueueCode: queueCode, ScopeIds: scope.IDs(),
		})
	if err != nil {
		return application.BatchFigure{}, fmt.Errorf("report: dashboard batches: %w", err)
	}
	return application.BatchFigure{
		BatchCount: row.BatchCount, SubmittedTotal: row.SubmittedTotal,
		OldestSubmittedAt: anyTime(row.OldestSubmittedAt),
		OldestSLADueAt:    anyTime(row.OldestSlaDueAt),
	}, nil
}

// SettlementsDue implements application.Repository.
func (Repository) SettlementsDue(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	scope application.Scope, asOf time.Time,
) (application.SettlementFigure, error) {
	row, err := sqlcgen.New(tx).DashboardSettlements(ctx, sqlcgen.DashboardSettlementsParams{
		TenantID: tenantID, AsOf: dateParam(asOf), ScopeIds: scope.IDs(),
	})
	if err != nil {
		return application.SettlementFigure{}, fmt.Errorf("report: dashboard settlements: %w", err)
	}
	return application.SettlementFigure{
		DueSoonCount: row.DueSoonCount, DueSoonTotal: row.DueSoonTotal,
		OverdueCount: row.OverdueCount, OverdueTotal: row.OverdueTotal,
	}, nil
}

// ReimbursementsAwaitingDecision implements application.Repository.
func (Repository) ReimbursementsAwaitingDecision(ctx context.Context, tx pgx.Tx,
	tenantID uuid.UUID,
) (application.ReimbursementFigure, error) {
	row, err := sqlcgen.New(tx).DashboardReimbursementsAwaitingDecision(ctx, tenantID)
	if err != nil {
		return application.ReimbursementFigure{}, fmt.Errorf("report: dashboard reimbursements: %w", err)
	}
	return application.ReimbursementFigure{
		ReimbursementCount: row.ReimbursementCount, RequestedTotal: row.RequestedTotal,
		OldestSubmittedAt: anyTime(row.OldestSubmittedAt),
	}, nil
}

// WorkItemsPastSLA implements application.Repository.
func (Repository) WorkItemsPastSLA(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	asOf time.Time,
) (application.WorkItemFigure, error) {
	row, err := sqlcgen.New(tx).DashboardWorkItemsPastSla(ctx,
		sqlcgen.DashboardWorkItemsPastSlaParams{TenantID: tenantID, AsOf: &asOf})
	if err != nil {
		return application.WorkItemFigure{}, fmt.Errorf("report: dashboard work items: %w", err)
	}
	return application.WorkItemFigure{
		ItemCount: row.ItemCount, OldestDueAt: anyTime(row.OldestDueAt),
	}, nil
}

// ---------------------------------------------------------------------------
// Exports
// ---------------------------------------------------------------------------

// CreateExport implements application.Repository.
func (Repository) CreateExport(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewExportRow,
) (application.Export, error) {
	row, err := sqlcgen.New(tx).CreateExport(ctx, sqlcgen.CreateExportParams{
		ID:       in.ID,
		TenantID: tenantID, Kind: in.Kind, Parameters: in.Parameters, Format: in.Format,
		ProviderOrganizationID: optUUID(in.ProviderOrganizationID),
		PeriodFrom:             optDate(in.PeriodFrom), PeriodTo: optDate(in.PeriodTo),
		CurrencyCode: in.CurrencyCode,
		RequestedBy:  in.RequestedBy, RequestedAt: in.RequestedAt,
		ExpiresAt: in.ExpiresAt, Watermark: in.Watermark,
	})
	if err != nil {
		return application.Export{}, fmt.Errorf("report: create export: %w", err)
	}
	return exportFrom(createdExportRow(row))
}

// GetExport implements application.Repository.
func (Repository) GetExport(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	requestedBy *uuid.UUID,
) (application.Export, error) {
	row, err := sqlcgen.New(tx).GetExport(ctx, sqlcgen.GetExportParams{
		TenantID: tenantID, ID: id, RequestedBy: optUUID(requestedBy),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Export{}, application.ErrExportNotFound
	}
	if err != nil {
		return application.Export{}, fmt.Errorf("report: read export: %w", err)
	}
	return exportFrom(row)
}

// LockExport implements application.Repository.
func (Repository) LockExport(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
) (application.Export, error) {
	row, err := sqlcgen.New(tx).LockExport(ctx, sqlcgen.LockExportParams{
		TenantID: tenantID, ID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Export{}, application.ErrExportNotFound
	}
	if err != nil {
		return application.Export{}, fmt.Errorf("report: lock export: %w", err)
	}
	return exportFrom(lockExportRow(row))
}

// ListExports implements application.Repository.
func (Repository) ListExports(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.ExportQueryOptions,
) ([]application.Export, error) {
	params := sqlcgen.ListExportsParams{
		TenantID: tenantID, RequestedBy: optUUID(q.RequestedBy),
		Kind: emptyToNil(q.Kind), Status: emptyToNil(q.Status),
		PageSize: int32(q.PageSize), //nolint:gosec // clamped by httpx.ClampLimit
	}
	if q.After != nil {
		params.AfterCreatedAt = &q.After.CreatedAt
		params.AfterID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListExports(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("report: list exports: %w", err)
	}
	out := make([]application.Export, 0, len(rows))
	for _, r := range rows {
		export, err := exportFrom(listExportRow(r))
		if err != nil {
			return nil, err
		}
		out = append(out, export)
	}
	return out, nil
}

// MarkExportRunning implements application.Repository.
func (Repository) MarkExportRunning(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
) (bool, error) {
	n, err := sqlcgen.New(tx).MarkExportRunning(ctx, sqlcgen.MarkExportRunningParams{
		TenantID: tenantID, ID: id,
	})
	if err != nil {
		return false, fmt.Errorf("report: mark export running: %w", err)
	}
	return n == 1, nil
}

// MarkExportReady implements application.Repository.
func (Repository) MarkExportReady(ctx context.Context, tx pgx.Tx, tenantID, id,
	documentID uuid.UUID, rowCount int,
) (bool, error) {
	n, err := sqlcgen.New(tx).MarkExportReady(ctx, sqlcgen.MarkExportReadyParams{
		TenantID: tenantID, ID: id,
		DocumentID: uuid.NullUUID{UUID: documentID, Valid: true},
		RowCount:   int32(rowCount), //nolint:gosec // bounded by domain.MaxExportRows
	})
	if err != nil {
		return false, fmt.Errorf("report: mark export ready: %w", err)
	}
	return n == 1, nil
}

// MarkExportFailed implements application.Repository.
func (Repository) MarkExportFailed(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	failureCode string,
) (bool, error) {
	code := failureCode
	n, err := sqlcgen.New(tx).MarkExportFailed(ctx, sqlcgen.MarkExportFailedParams{
		TenantID: tenantID, ID: id, FailureCode: &code,
	})
	if err != nil {
		return false, fmt.Errorf("report: mark export failed: %w", err)
	}
	return n == 1, nil
}

// CountExportDownload implements application.Repository.
func (Repository) CountExportDownload(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
) (int, error) {
	n, err := sqlcgen.New(tx).CountExportDownload(ctx, sqlcgen.CountExportDownloadParams{
		TenantID: tenantID, ID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, application.ErrExportNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("report: count export download: %w", err)
	}
	return int(n), nil
}

// ListExpiredExports implements application.Repository.
func (Repository) ListExpiredExports(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	asOf time.Time, limit int,
) ([]application.ExpiringExport, error) {
	rows, err := sqlcgen.New(tx).ListExpiredExports(ctx, sqlcgen.ListExpiredExportsParams{
		TenantID: tenantID, AsOf: asOf,
		RowLimit: int32(limit), //nolint:gosec // a sweep limit the caller chose
	})
	if err != nil {
		return nil, fmt.Errorf("report: list expired exports: %w", err)
	}
	out := make([]application.ExpiringExport, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.ExpiringExport{
			ID: r.ID, Kind: r.Kind, DocumentID: uuidPtr(r.DocumentID),
			RequestedBy: r.RequestedBy, ExpiresAt: r.ExpiresAt,
		})
	}
	return out, nil
}

// MarkExportExpired implements application.Repository.
func (Repository) MarkExportExpired(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
) (bool, error) {
	n, err := sqlcgen.New(tx).MarkExportExpired(ctx, sqlcgen.MarkExportExpiredParams{
		TenantID: tenantID, ID: id,
	})
	if err != nil {
		return false, fmt.Errorf("report: mark export expired: %w", err)
	}
	return n == 1, nil
}

// ExportIdentity implements application.Repository.
func (Repository) ExportIdentity(ctx context.Context, tx pgx.Tx, tenantID, actorID uuid.UUID,
) (string, string, error) {
	row, err := sqlcgen.New(tx).GetExportIdentity(ctx, sqlcgen.GetExportIdentityParams{
		TenantID: tenantID, ActorID: actorID,
	})
	if err != nil {
		return "", "", fmt.Errorf("report: read export identity: %w", err)
	}
	return row.TenantCode, row.RequesterName, nil
}

// ActiveTenants implements application.Repository.
func (Repository) ActiveTenants(ctx context.Context, tx pgx.Tx) ([]uuid.UUID, error) {
	rows, err := sqlcgen.New(tx).ActiveTenantsForReports(ctx)
	if err != nil {
		return nil, fmt.Errorf("report: list tenants: %w", err)
	}
	return rows, nil
}
