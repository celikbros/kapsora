package reportpg

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
	"github.com/celikbros/kapsora/internal/report/application"
)

// sqlc gives each query its own row struct even when the columns are identical, so the three
// export reads and the three run reads produce six types that differ in name only. The two
// shapes below are what the two mappers work from, and the small converters are what keep each
// mapper single — the alternative is six copies, which is six places for a column to be
// forgotten.

// exportFields is the one shape the export mapper works from. Every export read selects the same
// columns in the same order, so the conversions below are conversions rather than copies.
type exportFields = sqlcgen.GetExportRow

func createdExportRow(r sqlcgen.CreateExportRow) exportFields { return exportFields(r) }

func lockExportRow(r sqlcgen.LockExportRow) exportFields { return exportFields(r) }

func listExportRow(r sqlcgen.ListExportsRow) exportFields { return exportFields(r) }

// exportFrom renders one report.export row. The parameters are decoded here rather than carried
// as bytes, because everything above this line reads them as a map and a second decoder
// somewhere else would be a second chance to disagree about what an empty filter set is.
func exportFrom(r exportFields) (application.Export, error) {
	parameters := map[string]any{}
	if len(r.Parameters) > 0 {
		if err := json.Unmarshal(r.Parameters, &parameters); err != nil {
			return application.Export{}, fmt.Errorf("report: decode export parameters: %w", err)
		}
	}
	out := application.Export{
		ID: r.ID, Kind: r.Kind, Format: r.Format, Status: r.Status, Parameters: parameters,
		ProviderOrganizationID: uuidPtr(r.ProviderOrganizationID),
		PeriodFrom:             optDateValue(r.PeriodFrom), PeriodTo: optDateValue(r.PeriodTo),
		DocumentID:  uuidPtr(r.DocumentID),
		RowCount:    int(r.RowCount),
		RequestedBy: r.RequestedBy, RequestedAt: r.RequestedAt, ExpiresAt: r.ExpiresAt,
		Watermark: r.Watermark, DownloadCount: int(r.DownloadCount),
		FailureCode: r.FailureCode, CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
	if r.CurrencyCode != nil {
		out.CurrencyCode = *r.CurrencyCode
	}
	return out, nil
}

// runFields is the one shape the reconciliation mapper works from.
type runFields = sqlcgen.GetReconciliationRunRow

func listRunRow(r sqlcgen.ListReconciliationRunsRow) runFields { return runFields(r) }

// createdRunRow is the one conversion that is not a conversion: the insert's RETURNING clause
// carries no provider name, because there is nothing to join against inside an INSERT and the
// caller has the id it just wrote.
func createdRunRow(r sqlcgen.CreateReconciliationRunRow) runFields {
	return runFields{
		ID: r.ID, Scope: r.Scope, ProviderOrganizationID: r.ProviderOrganizationID,
		PeriodFrom: r.PeriodFrom, PeriodTo: r.PeriodTo, RunNo: r.RunNo,
		CurrencyCode:  r.CurrencyCode,
		InvoicedTotal: r.InvoicedTotal, ApprovedTotal: r.ApprovedTotal, CutTotal: r.CutTotal,
		ReturnedTotal: r.ReturnedTotal, RejectedTotal: r.RejectedTotal,
		SettledTotal: r.SettledTotal, PaidTotal: r.PaidTotal, OpenTotal: r.OpenTotal,
		ErpTotal: r.ErpTotal, Difference: r.Difference, DifferenceCount: r.DifferenceCount,
		Differences: r.Differences, Status: r.Status, FailureCode: r.FailureCode,
		RanAt: r.RanAt, CreatedAt: r.CreatedAt,
	}
}

// runFrom renders one billing.reconciliation_run row.
func runFrom(r runFields) (application.ReconciliationRun, error) {
	differences, err := decodeDifferences(r.Differences)
	if err != nil {
		return application.ReconciliationRun{}, err
	}
	return application.ReconciliationRun{
		ID: r.ID, Scope: r.Scope,
		ProviderOrganizationID: uuidPtr(r.ProviderOrganizationID), ProviderName: r.ProviderName,
		PeriodFrom: dateValue(r.PeriodFrom), PeriodTo: dateValue(r.PeriodTo),
		RunNo: int(r.RunNo), CurrencyCode: r.CurrencyCode,
		InvoicedTotal: r.InvoicedTotal, ApprovedTotal: r.ApprovedTotal, CutTotal: r.CutTotal,
		ReturnedTotal: r.ReturnedTotal, RejectedTotal: r.RejectedTotal,
		SettledTotal: r.SettledTotal, PaidTotal: r.PaidTotal, OpenTotal: r.OpenTotal,
		ERPTotal: r.ErpTotal, Difference: r.Difference, DifferenceCount: int(r.DifferenceCount),
		Differences: differences, Status: r.Status, FailureCode: r.FailureCode,
		RanAt: r.RanAt, CreatedAt: r.CreatedAt,
	}, nil
}

// differenceWire is what one difference looks like inside the run's jsonb column: the reference a
// person finds on their own screen, the two figures and the kind. **No identifier.** The column is
// read by somebody looking for the settlement, and a uuid is not how they will find it.
type differenceWire struct {
	Reference  string `json:"reference"`
	DueDate    string `json:"dueDate"`
	Status     string `json:"status"`
	Expected   string `json:"expected"`
	Actual     string `json:"actual"`
	Difference string `json:"difference"`
	Kind       string `json:"kind"`
}

func encodeDifferences(list []application.ReconciliationDifference) ([]byte, error) {
	wire := make([]differenceWire, 0, len(list))
	for _, d := range list {
		wire = append(wire, differenceWire{
			Reference: d.SettlementReference, DueDate: d.DueDate.Format(time.DateOnly),
			Status: d.Status, Expected: d.ExpectedAmount, Actual: d.ActualAmount,
			Difference: d.DifferenceAmount, Kind: d.Kind,
		})
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("report: encode reconciliation differences: %w", err)
	}
	return encoded, nil
}

func decodeDifferences(raw []byte) ([]application.ReconciliationDifference, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var wire []differenceWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("report: decode reconciliation differences: %w", err)
	}
	out := make([]application.ReconciliationDifference, 0, len(wire))
	for _, d := range wire {
		due, _ := time.Parse(time.DateOnly, d.DueDate)
		out = append(out, application.ReconciliationDifference{
			SettlementReference: d.Reference, DueDate: due, Status: d.Status,
			ExpectedAmount: d.Expected, ActualAmount: d.Actual,
			DifferenceAmount: d.Difference, Kind: d.Kind,
		})
	}
	return out, nil
}

func dateParam(t time.Time) pgtype.Date {
	if t.IsZero() {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: t, Valid: true}
}

func optDate(t *time.Time) pgtype.Date {
	if t == nil {
		return pgtype.Date{}
	}
	return dateParam(*t)
}

func dateValue(d pgtype.Date) time.Time {
	if !d.Valid {
		return time.Time{}
	}
	return d.Time
}

func optDateValue(d pgtype.Date) *time.Time {
	if !d.Valid {
		return nil
	}
	value := d.Time
	return &value
}

func uuidPtr(id uuid.NullUUID) *uuid.UUID {
	if !id.Valid {
		return nil
	}
	value := id.UUID
	return &value
}

func optUUID(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}

func emptyToNil(v string) *string {
	if v == "" {
		return nil
	}
	value := v
	return &value
}

// anyTime reads a column sqlc typed as `interface{}`. An aggregate over no rows is NULL, and sqlc
// cannot know that `min(x)` of a NOT NULL column is nullable — so these five dashboard columns
// arrive untyped and are narrowed here, in one place, rather than by five type assertions
// scattered through the repository.
func anyTime(v any) *time.Time {
	switch value := v.(type) {
	case nil:
		return nil
	case time.Time:
		if value.IsZero() {
			return nil
		}
		out := value
		return &out
	case *time.Time:
		return value
	default:
		return nil
	}
}
