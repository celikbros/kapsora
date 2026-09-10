package reportpg

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
	"github.com/celikbros/kapsora/internal/report/application"
	"github.com/celikbros/kapsora/internal/report/domain"
)

// The column names of each kind, in the order they go into the file. They are Turkish because the
// people who open these files are, and they are here rather than in the SQL because a column name
// is a label rather than a fact about the data.
//
// The watermark column is deliberately not in any of these lists. It is added by the renderer,
// once, for every row of every kind, so that a column somebody adds to one of these queries later
// cannot arrive unstamped.
var exportColumns = map[string][]string{
	domain.KindSettlements: {
		"Referans", "Sağlayıcı", "İcmal", "Para Birimi", "Vade", "Durum",
		"Onaylanan", "Mahsup", "Ödenecek", "Ödenen", "Açık",
	},
	domain.KindBatch: {
		"Referans", "Sağlayıcı", "Para Birimi", "Dönem Başı", "Dönem Sonu", "Durum",
		"Fatura Adedi", "Gönderilen", "Onaylanan", "Kesinti", "İade", "Ret",
	},
	domain.KindProviderStatement: {
		"Fatura No", "Fatura Tarihi", "Durum", "Para Birimi", "Tutar",
		"İcmal", "Karar", "Onaylanan", "Ödeme Referansı", "Ödenen",
	},
	domain.KindReconciliation: {
		"Kapsam", "Sağlayıcı", "Dönem Başı", "Dönem Sonu", "Sıra", "Para Birimi", "Durum",
		"Fark Adedi", "Settlement Toplamı", "Ödenen", "Açık", "Fark",
	},
	domain.KindClaims: {
		"Dosya", "Sağlayıcı", "Durum", "Alan", "Hizmet Başı", "Hizmet Sonu",
		"Açıklama", "Hizmet Kodu", "Satır Tutarı", "Onaylanan", "Para Birimi",
	},
}

// ExportRows implements application.Repository. The switch on the kind is here rather than in the
// service because what a kind *is* is the query behind it: five kinds, five statements, one shape
// of answer.
func (Repository) ExportRows(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.ExportRowQuery,
) (application.ExportTable, error) {
	columns, ok := exportColumns[q.Kind]
	if !ok {
		return application.ExportTable{}, fmt.Errorf("report: no export rows for kind %q", q.Kind)
	}
	limit := int32(q.RowLimit) //nolint:gosec // bounded by domain.MaxExportRows
	queries := sqlcgen.New(tx)

	var (
		rows [][]string
		err  error
	)
	switch q.Kind {
	case domain.KindSettlements:
		rows, err = settlementExportRows(ctx, queries, tenantID, q, limit)
	case domain.KindBatch:
		rows, err = batchExportRows(ctx, queries, tenantID, q, limit)
	case domain.KindProviderStatement:
		rows, err = statementExportRows(ctx, queries, tenantID, q, limit)
	case domain.KindReconciliation:
		rows, err = reconciliationExportRows(ctx, queries, tenantID, q, limit)
	case domain.KindClaims:
		rows, err = claimExportRows(ctx, queries, tenantID, q, limit)
	default:
		return application.ExportTable{}, fmt.Errorf("report: no export rows for kind %q", q.Kind)
	}
	if err != nil {
		return application.ExportTable{}, err
	}
	return application.ExportTable{Columns: columns, Rows: rows}, nil
}

func settlementExportRows(ctx context.Context, q *sqlcgen.Queries, tenantID uuid.UUID,
	in application.ExportRowQuery, limit int32,
) ([][]string, error) {
	rows, err := q.ExportSettlementRows(ctx, sqlcgen.ExportSettlementRowsParams{
		TenantID: tenantID, ProviderOrganizationID: optUUID(in.ProviderOrganizationID),
		CurrencyCode: emptyToNil(in.CurrencyCode),
		PeriodFrom:   optDate(in.PeriodFrom), PeriodTo: optDate(in.PeriodTo),
		ScopeIds: in.Scope.IDs(), RowLimit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("report: export settlements: %w", err)
	}
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, []string{
			r.Reference, r.ProviderName, r.BatchReference, r.CurrencyCode,
			day(r.DueDate), r.Status,
			r.ApprovedAmount, r.WithheldAmount, r.PayableAmount, r.PaidAmount, r.OpenAmount,
		})
	}
	return out, nil
}

func batchExportRows(ctx context.Context, q *sqlcgen.Queries, tenantID uuid.UUID,
	in application.ExportRowQuery, limit int32,
) ([][]string, error) {
	rows, err := q.ExportBatchRows(ctx, sqlcgen.ExportBatchRowsParams{
		TenantID: tenantID, ProviderOrganizationID: optUUID(in.ProviderOrganizationID),
		CurrencyCode: emptyToNil(in.CurrencyCode),
		PeriodFrom:   optDate(in.PeriodFrom), PeriodTo: optDate(in.PeriodTo),
		ScopeIds: in.Scope.IDs(), RowLimit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("report: export batches: %w", err)
	}
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, []string{
			r.Reference, r.ProviderName, r.CurrencyCode,
			day(r.PeriodFrom), day(r.PeriodTo), r.Status,
			fmt.Sprint(r.InvoiceCount),
			r.SubmittedTotal, r.ApprovedTotal, r.CutTotal, r.ReturnedTotal, r.RejectedTotal,
		})
	}
	return out, nil
}

// statementExportRows renders one provider's statement. The provider and the period are required
// by `ck_report_export_statement_scope`, so a row that reached the worker without them is a row
// the database would not have accepted — the guard below is what turns that into an error rather
// than a nil dereference.
func statementExportRows(ctx context.Context, q *sqlcgen.Queries, tenantID uuid.UUID,
	in application.ExportRowQuery, limit int32,
) ([][]string, error) {
	if in.ProviderOrganizationID == nil || in.PeriodFrom == nil || in.PeriodTo == nil {
		return nil, fmt.Errorf("report: a statement export needs a provider and a period")
	}
	rows, err := q.ExportStatementRows(ctx, sqlcgen.ExportStatementRowsParams{
		TenantID: tenantID, ProviderOrganizationID: *in.ProviderOrganizationID,
		CurrencyCode: emptyToNil(in.CurrencyCode),
		PeriodFrom:   optDate(in.PeriodFrom), PeriodTo: optDate(in.PeriodTo),
		RowLimit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("report: export statement: %w", err)
	}
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, []string{
			r.InvoiceNumber, day(r.InvoiceDate), r.Status, r.CurrencyCode, r.PayableAmount,
			r.BatchReference, r.BatchDecision, r.ApprovedAmount,
			r.SettlementReference, r.SettlementPaidAmount,
		})
	}
	return out, nil
}

func reconciliationExportRows(ctx context.Context, q *sqlcgen.Queries, tenantID uuid.UUID,
	in application.ExportRowQuery, limit int32,
) ([][]string, error) {
	rows, err := q.ExportReconciliationRows(ctx, sqlcgen.ExportReconciliationRowsParams{
		TenantID: tenantID, ProviderOrganizationID: optUUID(in.ProviderOrganizationID),
		CurrencyCode: emptyToNil(in.CurrencyCode),
		PeriodFrom:   optDate(in.PeriodFrom), PeriodTo: optDate(in.PeriodTo),
		ScopeIds: in.Scope.IDs(), RowLimit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("report: export reconciliation runs: %w", err)
	}
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, []string{
			r.Scope, r.ProviderName, day(r.PeriodFrom), day(r.PeriodTo),
			fmt.Sprint(r.RunNo), r.CurrencyCode, r.Status, fmt.Sprint(r.DifferenceCount),
			r.SettledTotal, r.PaidTotal, r.OpenTotal, r.Difference,
		})
	}
	return out, nil
}

// claimExportRows is the only one whose rows carry prose. `report.export.sensitive` is what stands
// between a caller and this function, and it is checked in the service where the kind is chosen —
// which is the only moment anybody asks for this.
func claimExportRows(ctx context.Context, q *sqlcgen.Queries, tenantID uuid.UUID,
	in application.ExportRowQuery, limit int32,
) ([][]string, error) {
	rows, err := q.ExportClaimRows(ctx, sqlcgen.ExportClaimRowsParams{
		TenantID: tenantID, ProviderOrganizationID: optUUID(in.ProviderOrganizationID),
		PeriodFrom: optDate(in.PeriodFrom), PeriodTo: optDate(in.PeriodTo),
		ScopeIds: in.Scope.IDs(), RowLimit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("report: export claims: %w", err)
	}
	out := make([][]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, []string{
			r.Reference, r.ProviderName, r.Status, r.DomainCode,
			day(r.ServiceDateFrom), day(r.ServiceDateTo),
			r.Description, r.ServiceCode, r.LineAmount, r.ApprovedAmount, r.CurrencyCode,
		})
	}
	return out, nil
}

// day renders a date column for a file. An absent date is an empty cell rather than a zero date,
// because "0001-01-01" in a spreadsheet is a number somebody will eventually try to explain.
func day(d pgtype.Date) string {
	if !d.Valid {
		return ""
	}
	return d.Time.Format(time.DateOnly)
}
