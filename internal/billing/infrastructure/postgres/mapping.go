package billingpg

import (
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/billing/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// sqlc gives each query its own row struct even when the columns are identical, so the four
// invoice reads produce four types that differ in name only. `invoiceRow` is the one shape the
// mapper below works from, and the small converters are what keep the mapper single — the
// alternative is four copies of it, which is four places for a column to be forgotten.
//
// The two shapes differ in one field: the header reads carry the provider's display name and
// the locking read does not, because a command that is about to write does not need to render
// anything and a join it does not need is a join under a `FOR UPDATE`.
type invoiceRow struct {
	ID                     uuid.UUID
	ProviderOrganizationID uuid.UUID
	PayerOrganizationID    uuid.NullUUID
	Source                 string
	EdocumentID            uuid.NullUUID
	InvoiceNumber          string
	InvoiceDate            pgtype.Date
	FiscalYear             int32
	CurrencyCode           string
	LineExtensionAmount    string
	TaxAmount              string
	PayableAmount          string
	VatRate                string
	DomainCode             string
	Status                 string
	SupersedesInvoiceID    uuid.NullUUID
	SupersededByInvoiceID  uuid.NullUUID
	SubmittedAt            *time.Time
	DocumentID             uuid.NullUUID
	BatchID                uuid.NullUUID
	Notes                  *string
	CreatedAt              time.Time
	RowVersion             int64
	ProviderName           string
}

func createdInvoiceRow(r sqlcgen.CreateInvoiceRow) invoiceRow {
	return invoiceRow{
		ID: r.ID, ProviderOrganizationID: r.ProviderOrganizationID,
		PayerOrganizationID: r.PayerOrganizationID, Source: r.Source,
		EdocumentID: r.EdocumentID, InvoiceNumber: r.InvoiceNumber,
		InvoiceDate: r.InvoiceDate, FiscalYear: r.FiscalYear, CurrencyCode: r.CurrencyCode,
		LineExtensionAmount: r.LineExtensionAmount, TaxAmount: r.TaxAmount,
		PayableAmount: r.PayableAmount, VatRate: r.VatRate, DomainCode: r.DomainCode,
		Status: r.Status, SupersedesInvoiceID: r.SupersedesInvoiceID,
		SupersededByInvoiceID: r.SupersededByInvoiceID, SubmittedAt: r.SubmittedAt,
		DocumentID: r.DocumentID, BatchID: r.BatchID, Notes: r.Notes,
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
}

func getInvoiceRow(r sqlcgen.GetInvoiceRow) invoiceRow { return invoiceRow(r) }

func lockInvoiceRow(r sqlcgen.LockInvoiceRow) invoiceRow {
	return invoiceRow{
		ID: r.ID, ProviderOrganizationID: r.ProviderOrganizationID,
		PayerOrganizationID: r.PayerOrganizationID, Source: r.Source,
		EdocumentID: r.EdocumentID, InvoiceNumber: r.InvoiceNumber,
		InvoiceDate: r.InvoiceDate, FiscalYear: r.FiscalYear, CurrencyCode: r.CurrencyCode,
		LineExtensionAmount: r.LineExtensionAmount, TaxAmount: r.TaxAmount,
		PayableAmount: r.PayableAmount, VatRate: r.VatRate, DomainCode: r.DomainCode,
		Status: r.Status, SupersedesInvoiceID: r.SupersedesInvoiceID,
		SupersededByInvoiceID: r.SupersededByInvoiceID, SubmittedAt: r.SubmittedAt,
		DocumentID: r.DocumentID, BatchID: r.BatchID, Notes: r.Notes,
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
}

// summaryRow is the list shape: an invoice row plus the two figures the page carries.
type summaryRow struct {
	invoiceRow
	AllocationTotal string
	AllocationCount int32
}

func listInvoicesRow(r sqlcgen.ListInvoicesRow) summaryRow {
	return summaryRow{
		invoiceRow: invoiceRow{
			ID: r.ID, ProviderOrganizationID: r.ProviderOrganizationID,
			PayerOrganizationID: r.PayerOrganizationID, Source: r.Source,
			EdocumentID: r.EdocumentID, InvoiceNumber: r.InvoiceNumber,
			InvoiceDate: r.InvoiceDate, FiscalYear: r.FiscalYear, CurrencyCode: r.CurrencyCode,
			LineExtensionAmount: r.LineExtensionAmount, TaxAmount: r.TaxAmount,
			PayableAmount: r.PayableAmount, VatRate: r.VatRate, DomainCode: r.DomainCode,
			Status: r.Status, SupersedesInvoiceID: r.SupersedesInvoiceID,
			SupersededByInvoiceID: r.SupersededByInvoiceID, SubmittedAt: r.SubmittedAt,
			DocumentID: r.DocumentID, BatchID: r.BatchID, Notes: r.Notes,
			CreatedAt: r.CreatedAt, RowVersion: r.RowVersion, ProviderName: r.ProviderName,
		},
		AllocationTotal: r.AllocationTotal, AllocationCount: r.AllocationCount,
	}
}

func chainRow(r sqlcgen.ListInvoiceChainRow) summaryRow {
	return listInvoicesRow(sqlcgen.ListInvoicesRow(r))
}

// invoiceOf maps one row onto the application record.
func invoiceOf(r invoiceRow) application.InvoiceRecord {
	return application.InvoiceRecord{
		ID: r.ID, ProviderOrganizationID: r.ProviderOrganizationID,
		ProviderName: r.ProviderName, PayerOrganizationID: uuidPtr(r.PayerOrganizationID),
		Source: r.Source, EDocumentID: uuidPtr(r.EdocumentID),
		InvoiceNumber: r.InvoiceNumber, InvoiceDate: dateValue(r.InvoiceDate),
		FiscalYear: int(r.FiscalYear), CurrencyCode: r.CurrencyCode,
		LineExtensionAmount: r.LineExtensionAmount, TaxAmount: r.TaxAmount,
		PayableAmount: r.PayableAmount, VatRate: emptyToNil(r.VatRate),
		DomainCode: r.DomainCode, Status: r.Status,
		SupersedesInvoiceID:   uuidPtr(r.SupersedesInvoiceID),
		SupersededByInvoiceID: uuidPtr(r.SupersededByInvoiceID),
		SubmittedAt:           r.SubmittedAt, DocumentID: uuidPtr(r.DocumentID),
		BatchID: uuidPtr(r.BatchID), Notes: r.Notes,
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
}

func summaryOf(r summaryRow) application.InvoiceSummaryRecord {
	return application.InvoiceSummaryRecord{
		InvoiceRecord:   invoiceOf(r.invoiceRow),
		AllocationTotal: r.AllocationTotal,
		AllocationCount: int(r.AllocationCount),
	}
}

// allocationOf maps one claim link. The description is copied through unprojected: what a
// caller may see of it is decided one layer up, in one place, on the record.
func allocationOf(r sqlcgen.ListInvoiceAllocationsRow) application.AllocationRecord {
	return application.AllocationRecord{
		ClaimID: r.ClaimID, ClaimReference: r.ClaimReference,
		ClaimVersionNo: int(r.ClaimVersionNo), AllocatedAmount: r.AllocatedAmount,
		CurrencyCode: r.CurrencyCode, ApprovedTotal: r.ApprovedTotal,
		ClaimStatus: r.ClaimStatus, ClaimStatusBefore: r.ClaimStatusBefore,
		Active: r.Active, ClaimDescription: emptyToNil(r.ClaimDescription),
	}
}

func uuidPtr(v uuid.NullUUID) *uuid.UUID {
	if !v.Valid {
		return nil
	}
	id := v.UUID
	return &id
}

// emptyToNil turns the empty string the queries use for a NULL text or numeric back into the
// absence it was. The queries COALESCE rather than answering NULL because sqlc infers a cast
// expression as not-null, and a column that arrived as a pointer in one query and a string in
// the next would be two shapes for one fact.
func emptyToNil(v string) *string {
	if v == "" {
		return nil
	}
	value := v
	return &value
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
