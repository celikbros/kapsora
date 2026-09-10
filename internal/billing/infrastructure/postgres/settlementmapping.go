package billingpg

import (
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/billing/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// sqlc gives each query its own row struct even when the columns are identical, so the four
// settlement reads and the four reimbursement reads produce eight types that differ in name
// only. The two shapes below are what the two mappers work from, and the small converters are
// what keep each mapper single — the alternative is eight copies, which is eight places for a
// column to be forgotten.
//
// The reads differ in one field each. The header reads carry the provider's display name, the
// batch's reference and who decided the batch; the locking read carries none of them, because
// a command that is about to write does not need to render anything and three joins it does
// not need are three joins under a `FOR UPDATE`.

// settlementFields is the one shape the settlement mapper works from.
type settlementFields struct {
	ID                     uuid.UUID
	Reference              string
	BatchID                uuid.UUID
	VersionNo              int32
	ProviderOrganizationID uuid.UUID
	PayerOrganizationID    uuid.NullUUID
	CurrencyCode           string
	ApprovedAmount         string
	WithheldAmount         string
	PayableAmount          string
	PaidAmount             string
	DueDate                pgtype.Date
	SettlementMethod       string
	Status                 string
	ApprovedBy             uuid.NullUUID
	ApprovedAt             *time.Time
	CheckedBy              uuid.NullUUID
	PostingID              uuid.NullUUID
	CancelReasonCode       *string
	CreatedAt              time.Time
	RowVersion             int64
	ProviderName           string
	BatchReference         string
	BatchDecidedBy         uuid.NullUUID
}

func createdSettlementRow(r sqlcgen.CreateSettlementRow) settlementFields {
	return settlementFields{
		ID: r.ID, Reference: r.Reference, BatchID: r.BatchID, VersionNo: r.VersionNo,
		ProviderOrganizationID: r.ProviderOrganizationID,
		PayerOrganizationID:    r.PayerOrganizationID, CurrencyCode: r.CurrencyCode,
		ApprovedAmount: r.ApprovedAmount, WithheldAmount: r.WithheldAmount,
		PayableAmount: r.PayableAmount, PaidAmount: r.PaidAmount, DueDate: r.DueDate,
		SettlementMethod: r.SettlementMethod, Status: r.Status, ApprovedBy: r.ApprovedBy,
		ApprovedAt: r.ApprovedAt, CheckedBy: r.CheckedBy, PostingID: r.PostingID,
		CancelReasonCode: r.CancelReasonCode, CreatedAt: r.CreatedAt,
		RowVersion: r.RowVersion,
	}
}

func settlementRow(r sqlcgen.GetSettlementRow) settlementFields { return settlementFields(r) }

func listSettlementsRow(r sqlcgen.ListSettlementsRow) settlementFields {
	return settlementFields(r)
}

func lockSettlementRow(r sqlcgen.LockSettlementRow) settlementFields {
	return settlementFields{
		ID: r.ID, Reference: r.Reference, BatchID: r.BatchID, VersionNo: r.VersionNo,
		ProviderOrganizationID: r.ProviderOrganizationID,
		PayerOrganizationID:    r.PayerOrganizationID, CurrencyCode: r.CurrencyCode,
		ApprovedAmount: r.ApprovedAmount, WithheldAmount: r.WithheldAmount,
		PayableAmount: r.PayableAmount, PaidAmount: r.PaidAmount, DueDate: r.DueDate,
		SettlementMethod: r.SettlementMethod, Status: r.Status, ApprovedBy: r.ApprovedBy,
		ApprovedAt: r.ApprovedAt, CheckedBy: r.CheckedBy, PostingID: r.PostingID,
		CancelReasonCode: r.CancelReasonCode, CreatedAt: r.CreatedAt,
		RowVersion: r.RowVersion,
	}
}

// settlementOf maps one row onto the application record.
func settlementOf(r settlementFields) application.SettlementRecord {
	out := application.SettlementRecord{
		ID: r.ID, Reference: r.Reference, BatchID: r.BatchID,
		BatchReference: r.BatchReference, BatchDecidedBy: uuidPtr(r.BatchDecidedBy),
		VersionNo:              int(r.VersionNo),
		ProviderOrganizationID: r.ProviderOrganizationID, ProviderName: r.ProviderName,
		PayerOrganizationID: uuidPtr(r.PayerOrganizationID), CurrencyCode: r.CurrencyCode,
		ApprovedAmount: r.ApprovedAmount, WithheldAmount: r.WithheldAmount,
		PayableAmount: r.PayableAmount, PaidAmount: r.PaidAmount,
		DueDate: dateValue(r.DueDate), SettlementMethod: r.SettlementMethod,
		Status: r.Status, ApprovedBy: uuidPtr(r.ApprovedBy), ApprovedAt: r.ApprovedAt,
		CheckedBy: uuidPtr(r.CheckedBy), PostingID: uuidPtr(r.PostingID),
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
	if r.CancelReasonCode != nil {
		out.CancelReasonCode = *r.CancelReasonCode
	}
	return out
}

// reimbursementFields is the one shape the reimbursement mapper works from. There is no
// ciphertext on it: no query selects `bank_account_ref_enc` back, so there is nothing here to
// drop.
type reimbursementFields struct {
	ID                     uuid.UUID
	Reference              string
	PersonID               uuid.UUID
	EnrollmentID           uuid.UUID
	ServiceRequestID       uuid.UUID
	ClaimID                uuid.NullUUID
	ReceiptDocumentID      uuid.UUID
	ServiceDefinitionID    uuid.UUID
	ServiceDate            pgtype.Date
	ProviderOrganizationID uuid.UUID
	RequestedAmount        string
	ApprovedAmount         string
	CurrencyCode           string
	BankAccountMasked      string
	Status                 string
	DuplicateOfID          uuid.NullUUID
	DecisionReasonCode     *string
	DecidedBy              uuid.NullUUID
	DecidedAt              *time.Time
	SubmittedAt            *time.Time
	PaymentReference       *string
	PaidAt                 *time.Time
	CreatedAt              time.Time
	RowVersion             int64
	DuplicateOfReference   *string
}

func createdReimbursementRow(r sqlcgen.CreateReimbursementRow) reimbursementFields {
	return reimbursementFields{
		ID: r.ID, Reference: r.Reference, PersonID: r.PersonID,
		EnrollmentID: r.EnrollmentID, ServiceRequestID: r.ServiceRequestID,
		ClaimID: r.ClaimID, ReceiptDocumentID: r.ReceiptDocumentID,
		ServiceDefinitionID: r.ServiceDefinitionID, ServiceDate: r.ServiceDate,
		ProviderOrganizationID: r.ProviderOrganizationID,
		RequestedAmount:        r.RequestedAmount, ApprovedAmount: r.ApprovedAmount,
		CurrencyCode: r.CurrencyCode, BankAccountMasked: r.BankAccountMasked,
		Status: r.Status, DuplicateOfID: r.DuplicateOfID,
		DecisionReasonCode: r.DecisionReasonCode, DecidedBy: r.DecidedBy,
		DecidedAt: r.DecidedAt, SubmittedAt: r.SubmittedAt,
		PaymentReference: r.PaymentReference, PaidAt: r.PaidAt,
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
}

func getReimbursementRow(r sqlcgen.GetReimbursementRow) reimbursementFields {
	return reimbursementFields(r)
}

func listReimbursementsRow(r sqlcgen.ListReimbursementsRow) reimbursementFields {
	return reimbursementFields(r)
}

func lockReimbursementRow(r sqlcgen.LockReimbursementRow) reimbursementFields {
	return reimbursementFields{
		ID: r.ID, Reference: r.Reference, PersonID: r.PersonID,
		EnrollmentID: r.EnrollmentID, ServiceRequestID: r.ServiceRequestID,
		ClaimID: r.ClaimID, ReceiptDocumentID: r.ReceiptDocumentID,
		ServiceDefinitionID: r.ServiceDefinitionID, ServiceDate: r.ServiceDate,
		ProviderOrganizationID: r.ProviderOrganizationID,
		RequestedAmount:        r.RequestedAmount, ApprovedAmount: r.ApprovedAmount,
		CurrencyCode: r.CurrencyCode, BankAccountMasked: r.BankAccountMasked,
		Status: r.Status, DuplicateOfID: r.DuplicateOfID,
		DecisionReasonCode: r.DecisionReasonCode, DecidedBy: r.DecidedBy,
		DecidedAt: r.DecidedAt, SubmittedAt: r.SubmittedAt,
		PaymentReference: r.PaymentReference, PaidAt: r.PaidAt,
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
}

// reimbursementOf maps one row onto the application record.
func reimbursementOf(r reimbursementFields) application.ReimbursementRecord {
	out := application.ReimbursementRecord{
		ID: r.ID, Reference: r.Reference, PersonID: r.PersonID,
		EnrollmentID: r.EnrollmentID, ServiceRequestID: r.ServiceRequestID,
		ClaimID: uuidPtr(r.ClaimID), ReceiptDocumentID: r.ReceiptDocumentID,
		ServiceDefinitionID: r.ServiceDefinitionID, ServiceDate: dateValue(r.ServiceDate),
		ProviderOrganizationID: r.ProviderOrganizationID,
		RequestedAmount:        r.RequestedAmount, ApprovedAmount: r.ApprovedAmount,
		CurrencyCode: r.CurrencyCode, BankAccountMasked: r.BankAccountMasked,
		Status: r.Status, DuplicateOfID: uuidPtr(r.DuplicateOfID),
		DecidedBy: uuidPtr(r.DecidedBy), DecidedAt: r.DecidedAt,
		SubmittedAt: r.SubmittedAt, PaidAt: r.PaidAt,
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
	if r.DecisionReasonCode != nil {
		out.DecisionReasonCode = *r.DecisionReasonCode
	}
	if r.PaymentReference != nil {
		out.PaymentReference = *r.PaymentReference
	}
	if r.DuplicateOfReference != nil {
		out.DuplicateReference = *r.DuplicateOfReference
	}
	return out
}
