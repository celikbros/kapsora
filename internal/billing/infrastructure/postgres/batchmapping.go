package billingpg

import (
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/billing/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// sqlc gives each query its own row struct even when the columns are identical, so the four
// batch reads produce four types that differ in name only. `batchRow` is the one shape the
// mapper below works from, and the small converters are what keep the mapper single — the
// alternative is four copies of it, which is four places for a column to be forgotten.
//
// The reads differ in one field: the header reads carry the provider's display name and the
// locking read does not, because a command that is about to write does not need to render
// anything and a join it does not need is a join under a `FOR UPDATE`.
type batchRow struct {
	ID                     uuid.UUID
	Reference              string
	ProviderOrganizationID uuid.UUID
	PayerOrganizationID    uuid.NullUUID
	DomainCode             string
	CurrencyCode           string
	PeriodFrom             pgtype.Date
	PeriodTo               pgtype.Date
	Status                 string
	SubmittedAt            *time.Time
	SubmittedBy            uuid.NullUUID
	DecidedAt              *time.Time
	DecidedBy              uuid.NullUUID
	InvoiceCount           int32
	SubmittedTotal         string
	ApprovedTotal          string
	CutTotal               string
	ReturnedTotal          string
	RejectedTotal          string
	CreatedAt              time.Time
	RowVersion             int64
	ProviderName           string
}

func createdBatchRow(r sqlcgen.CreateBatchRow) batchRow {
	return batchRow{
		ID: r.ID, Reference: r.Reference,
		ProviderOrganizationID: r.ProviderOrganizationID,
		PayerOrganizationID:    r.PayerOrganizationID, DomainCode: r.DomainCode,
		CurrencyCode: r.CurrencyCode, PeriodFrom: r.PeriodFrom, PeriodTo: r.PeriodTo,
		Status: r.Status, SubmittedAt: r.SubmittedAt, SubmittedBy: r.SubmittedBy,
		DecidedAt: r.DecidedAt, DecidedBy: r.DecidedBy, InvoiceCount: r.InvoiceCount,
		SubmittedTotal: r.SubmittedTotal, ApprovedTotal: r.ApprovedTotal,
		CutTotal: r.CutTotal, ReturnedTotal: r.ReturnedTotal, RejectedTotal: r.RejectedTotal,
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
}

func getBatchRow(r sqlcgen.GetBatchRow) batchRow { return batchRow(r) }

func listBatchesRow(r sqlcgen.ListBatchesRow) batchRow { return batchRow(r) }

func lockBatchRow(r sqlcgen.LockBatchRow) batchRow {
	return batchRow{
		ID: r.ID, Reference: r.Reference,
		ProviderOrganizationID: r.ProviderOrganizationID,
		PayerOrganizationID:    r.PayerOrganizationID, DomainCode: r.DomainCode,
		CurrencyCode: r.CurrencyCode, PeriodFrom: r.PeriodFrom, PeriodTo: r.PeriodTo,
		Status: r.Status, SubmittedAt: r.SubmittedAt, SubmittedBy: r.SubmittedBy,
		DecidedAt: r.DecidedAt, DecidedBy: r.DecidedBy, InvoiceCount: r.InvoiceCount,
		SubmittedTotal: r.SubmittedTotal, ApprovedTotal: r.ApprovedTotal,
		CutTotal: r.CutTotal, ReturnedTotal: r.ReturnedTotal, RejectedTotal: r.RejectedTotal,
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
}

// batchOf maps one row onto the application record.
func batchOf(r batchRow) application.BatchRecord {
	return application.BatchRecord{
		ID: r.ID, Reference: r.Reference,
		ProviderOrganizationID: r.ProviderOrganizationID, ProviderName: r.ProviderName,
		PayerOrganizationID: uuidPtr(r.PayerOrganizationID), DomainCode: r.DomainCode,
		CurrencyCode: r.CurrencyCode,
		PeriodFrom:   dateValue(r.PeriodFrom), PeriodTo: dateValue(r.PeriodTo),
		Status: r.Status, SubmittedAt: r.SubmittedAt, SubmittedBy: uuidPtr(r.SubmittedBy),
		DecidedAt: r.DecidedAt, DecidedBy: uuidPtr(r.DecidedBy),
		InvoiceCount:   int(r.InvoiceCount),
		SubmittedTotal: r.SubmittedTotal, ApprovedTotal: r.ApprovedTotal,
		CutTotal: r.CutTotal, ReturnedTotal: r.ReturnedTotal, RejectedTotal: r.RejectedTotal,
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
}
