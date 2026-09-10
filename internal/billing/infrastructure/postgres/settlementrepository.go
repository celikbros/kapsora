package billingpg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/billing/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// The settlement's repository (WP-I7-04). It is stateless in the same way the invoice's and the
// icmal's are: every method takes the caller's tenant-bound transaction, so RLS is active for
// every statement and the provider boundary is applied in SQL rather than after the read.
//
// One thing is worth stating here rather than only in the migration: the envelope of
// `bank_account_ref_enc` is written by `CreateReimbursement` and is selected back by nothing.
// There is no method on this repository that returns it, so there is no mapper that could
// forget to drop it.

// The constraint names this repository turns into named errors, so a caller reads a refusal
// rather than a PostgreSQL string.
const (
	// constraintSettlementReference is the reference's uniqueness inside the tenant. It never
	// reaches a caller: the create retries with a new random tail.
	constraintSettlementReference = "uq_billing_settlement_reference"
	// constraintReimbursementReference is the same, for a reimbursement.
	constraintReimbursementReference = "uq_billing_reimbursement_reference"
	// constraintLiveSettlement is "one live settlement per batch". The outbox handler looks
	// first and stops; this is the half that holds when two deliveries look at the same moment.
	constraintLiveSettlement = "uq_billing_settlement_live_batch"
	// constraintPaymentReference is "one external reference per provider": the same transfer
	// entered twice is how a settlement comes to look paid when half of it was not.
	constraintPaymentReference = "uq_billing_payment_record_reference"
	// constraintLiveReceipt is "one live claim per receipt". The duplicate check looks first
	// and names the earlier request; this is what holds under a race.
	constraintLiveReceipt = "uq_billing_reimbursement_live_receipt"
	// constraintReimbursementRequest is "one reimbursement per request".
	constraintReimbursementRequest = "uq_billing_reimbursement_request"
)

// SettlementRepository implements application.SettlementRepository.
type SettlementRepository struct{}

// NewSettlementRepository returns the repository.
func NewSettlementRepository() *SettlementRepository { return &SettlementRepository{} }

var _ application.SettlementRepository = (*SettlementRepository)(nil)

// CreateSettlement implements application.SettlementRepository.
func (SettlementRepository) CreateSettlement(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewSettlementRow,
) (application.SettlementRecord, error) {
	row, err := sqlcgen.New(tx).CreateSettlement(ctx, sqlcgen.CreateSettlementParams{
		TenantID: tenantID, Reference: in.Reference, BatchID: in.BatchID,
		VersionNo:              int32(in.VersionNo), //nolint:gosec // bounded by the batch's own history
		ProviderOrganizationID: in.ProviderOrganizationID,
		PayerOrganizationID:    optUUID(in.PayerOrganizationID),
		CurrencyCode:           in.CurrencyCode, ApprovedAmount: in.ApprovedAmount,
		WithheldAmount: in.WithheldAmount, PayableAmount: in.PayableAmount,
		DueDate: dateParam(in.DueDate), SettlementMethod: in.SettlementMethod,
		Status: in.Status, ActorID: optUUID(in.ActorID),
	})
	switch {
	case isUniqueViolation(err, constraintSettlementReference):
		return application.SettlementRecord{}, application.ErrSettlementReferenceTaken
	case isUniqueViolation(err, constraintLiveSettlement):
		// A second `batch.decided` delivery got past the look-first read. The handler treats
		// this as the guarantee working rather than as a failure.
		return application.SettlementRecord{}, application.ErrSettlementTransitionInvalid
	case err != nil:
		return application.SettlementRecord{}, fmt.Errorf("billing: create settlement: %w", err)
	}
	return settlementOf(createdSettlementRow(row)), nil
}

// GetSettlement implements application.SettlementRepository.
func (SettlementRepository) GetSettlement(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.SettlementRecord, error) {
	row, err := sqlcgen.New(tx).GetSettlement(ctx, sqlcgen.GetSettlementParams{
		TenantID: tenantID, ID: id, ScopeIds: scopeIDs(scope),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.SettlementRecord{}, application.ErrSettlementNotFound
	}
	if err != nil {
		return application.SettlementRecord{}, fmt.Errorf("billing: get settlement: %w", err)
	}
	return settlementOf(settlementRow(row)), nil
}

// LockSettlement implements application.SettlementRepository.
func (SettlementRepository) LockSettlement(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.SettlementRecord, error) {
	row, err := sqlcgen.New(tx).LockSettlement(ctx, sqlcgen.LockSettlementParams{
		TenantID: tenantID, ID: id, ScopeIds: scopeIDs(scope),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.SettlementRecord{}, application.ErrSettlementNotFound
	}
	if err != nil {
		return application.SettlementRecord{}, fmt.Errorf("billing: lock settlement: %w", err)
	}
	return settlementOf(lockSettlementRow(row)), nil
}

// ListSettlements implements application.SettlementRepository.
func (SettlementRepository) ListSettlements(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.SettlementQuery,
) ([]application.SettlementRecord, error) {
	params := sqlcgen.ListSettlementsParams{
		TenantID: tenantID, ScopeIds: scopeIDs(q.Scope),
		ProviderOrganizationID: optUUID(q.ProviderOrganizationID),
		PayerOrganizationID:    optUUID(q.PayerOrganizationID),
		BatchID:                optUUID(q.BatchID),
		Status:                 q.Status, CurrencyCode: q.CurrencyCode,
		DueFrom: optDate(q.DueFrom), DueTo: optDate(q.DueTo),
		PageSize: int32(q.PageSize), //nolint:gosec // clamped by httpx.ClampLimit
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.AfterCreatedAt = &at
		params.AfterID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListSettlements(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("billing: list settlements: %w", err)
	}
	out := make([]application.SettlementRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, settlementOf(listSettlementsRow(row)))
	}
	return out, nil
}

// FindLiveSettlementForBatch implements application.SettlementRepository.
func (SettlementRepository) FindLiveSettlementForBatch(ctx context.Context, tx pgx.Tx,
	tenantID, batchID uuid.UUID,
) (uuid.UUID, bool, error) {
	row, err := sqlcgen.New(tx).FindLiveSettlementForBatch(ctx,
		sqlcgen.FindLiveSettlementForBatchParams{TenantID: tenantID, BatchID: batchID})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("billing: find live settlement: %w", err)
	}
	return row.ID, true, nil
}

// NextSettlementVersion implements application.SettlementRepository.
func (SettlementRepository) NextSettlementVersion(ctx context.Context, tx pgx.Tx,
	tenantID, batchID uuid.UUID,
) (int, error) {
	next, err := sqlcgen.New(tx).NextSettlementVersion(ctx,
		sqlcgen.NextSettlementVersionParams{TenantID: tenantID, BatchID: batchID})
	if err != nil {
		return 0, fmt.Errorf("billing: next settlement version: %w", err)
	}
	return int(next), nil
}

// ApproveSettlement implements application.SettlementRepository.
func (SettlementRepository) ApproveSettlement(ctx context.Context, tx pgx.Tx, tenantID,
	id uuid.UUID, in application.ApproveSettlementRow, expected int64,
) (bool, error) {
	at := in.ApprovedAt
	n, err := sqlcgen.New(tx).ApproveSettlement(ctx, sqlcgen.ApproveSettlementParams{
		TenantID: tenantID, ID: id,
		ApprovedBy: uuid.NullUUID{UUID: in.ApprovedBy, Valid: in.ApprovedBy != uuid.Nil},
		ApprovedAt: &at, CheckedBy: optUUID(in.CheckedBy), ActorID: optUUID(in.ActorID),
		ExpectedRowVersion: expected,
	})
	if err != nil {
		return false, fmt.Errorf("billing: approve settlement: %w", err)
	}
	return n == 1, nil
}

// SetSettlementStatus implements application.SettlementRepository.
func (SettlementRepository) SetSettlementStatus(ctx context.Context, tx pgx.Tx, tenantID,
	id uuid.UUID, status string, from []string, actorID *uuid.UUID,
) (bool, error) {
	n, err := sqlcgen.New(tx).SetSettlementStatus(ctx, sqlcgen.SetSettlementStatusParams{
		TenantID: tenantID, ID: id, Status: status, FromStatuses: from,
		ActorID: optUUID(actorID),
	})
	if err != nil {
		return false, fmt.Errorf("billing: set settlement status: %w", err)
	}
	return n == 1, nil
}

// CancelSettlement implements application.SettlementRepository.
func (SettlementRepository) CancelSettlement(ctx context.Context, tx pgx.Tx, tenantID,
	id uuid.UUID, reasonCode string, reasonText *string, actorID *uuid.UUID, expected int64,
) (bool, error) {
	n, err := sqlcgen.New(tx).CancelSettlement(ctx, sqlcgen.CancelSettlementParams{
		TenantID: tenantID, ID: id, ReasonCode: &reasonCode, ReasonText: reasonText,
		ActorID: optUUID(actorID), ExpectedRowVersion: expected,
	})
	if err != nil {
		return false, fmt.Errorf("billing: cancel settlement: %w", err)
	}
	return n == 1, nil
}

// SetSettlementPaidAmount implements application.SettlementRepository.
func (SettlementRepository) SetSettlementPaidAmount(ctx context.Context, tx pgx.Tx, tenantID,
	id uuid.UUID, paid, status string, actorID *uuid.UUID,
) error {
	if _, err := sqlcgen.New(tx).SetSettlementPaidAmount(ctx,
		sqlcgen.SetSettlementPaidAmountParams{
			TenantID: tenantID, ID: id, PaidAmount: paid, Status: status,
			ActorID: optUUID(actorID),
		}); err != nil {
		return fmt.Errorf("billing: set settlement paid amount: %w", err)
	}
	return nil
}

// BatchForSettlement implements application.SettlementRepository.
func (SettlementRepository) BatchForSettlement(ctx context.Context, tx pgx.Tx,
	tenantID, batchID uuid.UUID,
) (application.SettlementBatch, error) {
	row, err := sqlcgen.New(tx).GetBatchForSettlement(ctx, sqlcgen.GetBatchForSettlementParams{
		TenantID: tenantID, ID: batchID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.SettlementBatch{}, application.ErrBatchNotFound
	}
	if err != nil {
		return application.SettlementBatch{}, fmt.Errorf("billing: batch for settlement: %w", err)
	}
	return application.SettlementBatch{
		ID: row.ID, Reference: row.Reference,
		ProviderOrganizationID: row.ProviderOrganizationID, ProviderName: row.ProviderName,
		ProviderProfileID:   uuidPtr(row.ProviderProfileID),
		PayerOrganizationID: uuidPtr(row.PayerOrganizationID),
		CurrencyCode:        row.CurrencyCode, DomainCode: row.DomainCode, Status: row.Status,
		DecidedAt: row.DecidedAt, DecidedBy: uuidPtr(row.DecidedBy),
		ApprovedTotal: row.ApprovedTotal,
	}, nil
}

// PaymentTermFor implements application.SettlementRepository.
func (SettlementRepository) PaymentTermFor(ctx context.Context, tx pgx.Tx, tenantID,
	providerProfileID uuid.UUID, payerOrganizationID *uuid.UUID, asOf time.Time,
) (application.PaymentTerm, error) {
	row, err := sqlcgen.New(tx).GetPaymentTermForProvider(ctx,
		sqlcgen.GetPaymentTermForProviderParams{
			TenantID: tenantID, ProviderProfileID: providerProfileID,
			PayerOrganizationID: optUUID(payerOrganizationID), AsOf: dateParam(asOf),
		})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.PaymentTerm{}, nil
	}
	if err != nil {
		return application.PaymentTerm{}, fmt.Errorf("billing: payment term: %w", err)
	}
	return application.PaymentTerm{
		Found: true, DueDays: int(row.DueDays), SettlementMethod: row.SettlementMethod,
		ContractID: row.ContractID,
	}, nil
}

// ListOpenRecoveries implements application.SettlementRepository.
func (SettlementRepository) ListOpenRecoveries(ctx context.Context, tx pgx.Tx, tenantID,
	providerOrganizationID uuid.UUID,
) ([]application.OpenRecovery, error) {
	rows, err := sqlcgen.New(tx).ListOpenRecoveries(ctx, sqlcgen.ListOpenRecoveriesParams{
		TenantID: tenantID, ProviderOrganizationID: providerOrganizationID,
	})
	if err != nil {
		return nil, fmt.Errorf("billing: list open recoveries: %w", err)
	}
	out := make([]application.OpenRecovery, 0, len(rows))
	for _, row := range rows {
		recovery := application.OpenRecovery{
			AdjustmentID: row.AdjustmentID, ClaimID: row.ClaimID, Amount: row.Amount,
			CreatedAt: row.CreatedAt,
		}
		recovery.ReasonCode = row.ReasonCode
		out = append(out, recovery)
	}
	return out, nil
}

// CreateSettlementRecovery implements application.SettlementRepository.
func (SettlementRepository) CreateSettlementRecovery(ctx context.Context, tx pgx.Tx,
	tenantID, settlementID, claimID, adjustmentID uuid.UUID, amount string, actorID *uuid.UUID,
) error {
	if err := sqlcgen.New(tx).CreateSettlementRecovery(ctx,
		sqlcgen.CreateSettlementRecoveryParams{
			TenantID: tenantID, SettlementID: settlementID, ClaimID: claimID,
			AdjustmentID: adjustmentID, Amount: amount, ActorID: optUUID(actorID),
		}); err != nil {
		return fmt.Errorf("billing: create settlement recovery: %w", err)
	}
	return nil
}

// ListSettlementRecoveries implements application.SettlementRepository.
func (SettlementRepository) ListSettlementRecoveries(ctx context.Context, tx pgx.Tx,
	tenantID, settlementID uuid.UUID,
) ([]application.SettlementRecoveryRecord, error) {
	rows, err := sqlcgen.New(tx).ListSettlementRecoveries(ctx,
		sqlcgen.ListSettlementRecoveriesParams{TenantID: tenantID, SettlementID: settlementID})
	if err != nil {
		return nil, fmt.Errorf("billing: list settlement recoveries: %w", err)
	}
	out := make([]application.SettlementRecoveryRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.SettlementRecoveryRecord{
			ID: row.ID, ClaimID: row.ClaimID, AdjustmentID: row.AdjustmentID,
			Amount: row.Amount, CreatedAt: row.CreatedAt,
		})
	}
	return out, nil
}

// DeleteSettlementRecoveries implements application.SettlementRepository.
func (SettlementRepository) DeleteSettlementRecoveries(ctx context.Context, tx pgx.Tx,
	tenantID, settlementID uuid.UUID,
) error {
	if _, err := sqlcgen.New(tx).DeleteSettlementRecoveries(ctx,
		sqlcgen.DeleteSettlementRecoveriesParams{
			TenantID: tenantID, SettlementID: settlementID,
		}); err != nil {
		return fmt.Errorf("billing: delete settlement recoveries: %w", err)
	}
	return nil
}

// CreatePaymentRecord implements application.SettlementRepository.
func (SettlementRepository) CreatePaymentRecord(ctx context.Context, tx pgx.Tx,
	tenantID uuid.UUID, in application.NewPaymentRecordRow,
) (application.PaymentRecordRecord, error) {
	row, err := sqlcgen.New(tx).CreatePaymentRecord(ctx, sqlcgen.CreatePaymentRecordParams{
		TenantID: tenantID, SettlementID: in.SettlementID,
		ProviderOrganizationID: in.ProviderOrganizationID,
		ExternalReference:      in.ExternalReference, Amount: in.Amount,
		CurrencyCode: in.CurrencyCode, PaidAt: in.PaidAt, Source: in.Source,
		RecordedBy: optUUID(in.RecordedBy), Notes: in.Notes, ActorID: optUUID(in.ActorID),
	})
	if isUniqueViolation(err, constraintPaymentReference) {
		return application.PaymentRecordRecord{}, application.ErrPaymentReferenceTaken
	}
	if err != nil {
		return application.PaymentRecordRecord{}, fmt.Errorf("billing: create payment record: %w", err)
	}
	return application.PaymentRecordRecord{
		ID: row.ID, SettlementID: row.SettlementID,
		ProviderOrganizationID: row.ProviderOrganizationID,
		ExternalReference:      row.ExternalReference, Amount: row.Amount,
		CurrencyCode: row.CurrencyCode, PaidAt: row.PaidAt, Source: row.Source,
		Status: row.Status, RecordedBy: uuidPtr(row.RecordedBy), Notes: row.Notes,
		CreatedAt: row.CreatedAt, RowVersion: row.RowVersion,
	}, nil
}

// ListPaymentRecords implements application.SettlementRepository.
func (SettlementRepository) ListPaymentRecords(ctx context.Context, tx pgx.Tx,
	tenantID, settlementID uuid.UUID, scope application.Scope,
) ([]application.PaymentRecordRecord, error) {
	rows, err := sqlcgen.New(tx).ListPaymentRecords(ctx, sqlcgen.ListPaymentRecordsParams{
		TenantID: tenantID, SettlementID: settlementID, ScopeIds: scopeIDs(scope),
	})
	if err != nil {
		return nil, fmt.Errorf("billing: list payment records: %w", err)
	}
	out := make([]application.PaymentRecordRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.PaymentRecordRecord{
			ID: row.ID, SettlementID: row.SettlementID,
			ProviderOrganizationID: row.ProviderOrganizationID,
			ExternalReference:      row.ExternalReference, Amount: row.Amount,
			CurrencyCode: row.CurrencyCode, PaidAt: row.PaidAt, Source: row.Source,
			Status: row.Status, RecordedBy: uuidPtr(row.RecordedBy), Notes: row.Notes,
			CreatedAt: row.CreatedAt, RowVersion: row.RowVersion,
		})
	}
	return out, nil
}

// SumLivePaymentRecords implements application.SettlementRepository.
func (SettlementRepository) SumLivePaymentRecords(ctx context.Context, tx pgx.Tx,
	tenantID, settlementID uuid.UUID,
) (string, error) {
	total, err := sqlcgen.New(tx).SumLivePaymentRecords(ctx,
		sqlcgen.SumLivePaymentRecordsParams{TenantID: tenantID, SettlementID: settlementID})
	if err != nil {
		return "", fmt.Errorf("billing: sum payment records: %w", err)
	}
	return total, nil
}

// CreateReimbursement implements application.SettlementRepository.
func (SettlementRepository) CreateReimbursement(ctx context.Context, tx pgx.Tx,
	tenantID uuid.UUID, in application.NewReimbursementRow,
) (application.ReimbursementRecord, error) {
	row, err := sqlcgen.New(tx).CreateReimbursement(ctx, sqlcgen.CreateReimbursementParams{
		TenantID: tenantID, Reference: in.Reference, PersonID: in.PersonID,
		EnrollmentID: in.EnrollmentID, ServiceRequestID: in.ServiceRequestID,
		ReceiptDocumentID: in.ReceiptDocumentID, ReceiptSha256: in.ReceiptSHA256,
		ServiceDefinitionID: in.ServiceDefinitionID, ServiceDate: dateParam(in.ServiceDate),
		ProviderOrganizationID: in.ProviderOrganizationID,
		RequestedAmount:        in.RequestedAmount, CurrencyCode: in.CurrencyCode,
		BankAccountRefEnc: in.BankAccountRefEnc, BankAccountMasked: in.BankAccountMasked,
		ActorID: optUUID(in.ActorID),
	})
	switch {
	case isUniqueViolation(err, constraintReimbursementReference):
		return application.ReimbursementRecord{}, application.ErrReimbursementReferenceTaken
	case isUniqueViolation(err, constraintLiveReceipt),
		isUniqueViolation(err, constraintReimbursementRequest):
		// The duplicate check looked first and named the earlier request; this is the half
		// that holds when two submissions look at the same moment.
		return application.ReimbursementRecord{}, application.ErrReimbursementDuplicate
	case err != nil:
		return application.ReimbursementRecord{}, fmt.Errorf("billing: create reimbursement: %w", err)
	}
	return reimbursementOf(createdReimbursementRow(row)), nil
}

// GetReimbursement implements application.SettlementRepository.
func (SettlementRepository) GetReimbursement(ctx context.Context, tx pgx.Tx, tenantID,
	id uuid.UUID, personID *uuid.UUID,
) (application.ReimbursementRecord, error) {
	row, err := sqlcgen.New(tx).GetReimbursement(ctx, sqlcgen.GetReimbursementParams{
		TenantID: tenantID, ID: id, PersonID: optUUID(personID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ReimbursementRecord{}, application.ErrReimbursementNotFound
	}
	if err != nil {
		return application.ReimbursementRecord{}, fmt.Errorf("billing: get reimbursement: %w", err)
	}
	return reimbursementOf(getReimbursementRow(row)), nil
}

// LockReimbursement implements application.SettlementRepository.
func (SettlementRepository) LockReimbursement(ctx context.Context, tx pgx.Tx, tenantID,
	id uuid.UUID, personID *uuid.UUID,
) (application.ReimbursementRecord, error) {
	row, err := sqlcgen.New(tx).LockReimbursement(ctx, sqlcgen.LockReimbursementParams{
		TenantID: tenantID, ID: id, PersonID: optUUID(personID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ReimbursementRecord{}, application.ErrReimbursementNotFound
	}
	if err != nil {
		return application.ReimbursementRecord{}, fmt.Errorf("billing: lock reimbursement: %w", err)
	}
	return reimbursementOf(lockReimbursementRow(row)), nil
}

// ListReimbursements implements application.SettlementRepository.
func (SettlementRepository) ListReimbursements(ctx context.Context, tx pgx.Tx,
	tenantID uuid.UUID, q application.ReimbursementQuery,
) ([]application.ReimbursementRecord, error) {
	params := sqlcgen.ListReimbursementsParams{
		TenantID: tenantID, PersonID: optUUID(q.PersonID), Status: q.Status,
		DateFrom: optDate(q.DateFrom), DateTo: optDate(q.DateTo),
		PageSize: int32(q.PageSize), //nolint:gosec // clamped by httpx.ClampLimit
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.AfterCreatedAt = &at
		params.AfterID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListReimbursements(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("billing: list reimbursements: %w", err)
	}
	out := make([]application.ReimbursementRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, reimbursementOf(listReimbursementsRow(row)))
	}
	return out, nil
}

// SubmitReimbursement implements application.SettlementRepository.
func (SettlementRepository) SubmitReimbursement(ctx context.Context, tx pgx.Tx, tenantID,
	id uuid.UUID, submittedAt time.Time, actorID *uuid.UUID, expected int64,
) (bool, error) {
	at := submittedAt
	n, err := sqlcgen.New(tx).SubmitReimbursement(ctx, sqlcgen.SubmitReimbursementParams{
		TenantID: tenantID, ID: id, SubmittedAt: &at, ActorID: optUUID(actorID),
		ExpectedRowVersion: expected,
	})
	if err != nil {
		return false, fmt.Errorf("billing: submit reimbursement: %w", err)
	}
	return n == 1, nil
}

// DecideReimbursement implements application.SettlementRepository.
func (SettlementRepository) DecideReimbursement(ctx context.Context, tx pgx.Tx, tenantID,
	id uuid.UUID, in application.DecideReimbursementRow, expected int64,
) (bool, error) {
	at := in.DecidedAt
	n, err := sqlcgen.New(tx).DecideReimbursement(ctx, sqlcgen.DecideReimbursementParams{
		TenantID: tenantID, ID: id, Status: in.Status, ApprovedAmount: in.ApprovedAmount,
		ReasonCode: in.ReasonCode, ReasonText: in.ReasonText,
		DuplicateOfID: optUUID(in.DuplicateOfID), ClaimID: optUUID(in.ClaimID),
		DecidedBy: uuid.NullUUID{UUID: in.DecidedBy, Valid: in.DecidedBy != uuid.Nil},
		DecidedAt: &at, ActorID: optUUID(in.ActorID), ExpectedRowVersion: expected,
	})
	if err != nil {
		return false, fmt.Errorf("billing: decide reimbursement: %w", err)
	}
	return n == 1, nil
}

// SetReimbursementStatus implements application.SettlementRepository.
func (SettlementRepository) SetReimbursementStatus(ctx context.Context, tx pgx.Tx, tenantID,
	id uuid.UUID, status string, from []string, actorID *uuid.UUID,
) (bool, error) {
	n, err := sqlcgen.New(tx).SetReimbursementStatus(ctx, sqlcgen.SetReimbursementStatusParams{
		TenantID: tenantID, ID: id, Status: status, FromStatuses: from,
		ActorID: optUUID(actorID),
	})
	if err != nil {
		return false, fmt.Errorf("billing: set reimbursement status: %w", err)
	}
	return n == 1, nil
}

// SetReimbursementPaid implements application.SettlementRepository.
func (SettlementRepository) SetReimbursementPaid(ctx context.Context, tx pgx.Tx, tenantID,
	id uuid.UUID, reference string, paidAt time.Time, actorID *uuid.UUID,
) (bool, error) {
	at := paidAt
	n, err := sqlcgen.New(tx).SetReimbursementPaid(ctx, sqlcgen.SetReimbursementPaidParams{
		TenantID: tenantID, ID: id, PaymentReference: &reference, PaidAt: &at,
		ActorID: optUUID(actorID),
	})
	if err != nil {
		return false, fmt.Errorf("billing: set reimbursement paid: %w", err)
	}
	return n == 1, nil
}

// FindReimbursementDuplicate implements application.SettlementRepository.
func (SettlementRepository) FindReimbursementDuplicate(ctx context.Context, tx pgx.Tx,
	tenantID uuid.UUID, probe application.DuplicateProbe,
) (application.DuplicateMatch, error) {
	row, err := sqlcgen.New(tx).FindReimbursementDuplicate(ctx,
		sqlcgen.FindReimbursementDuplicateParams{
			TenantID: tenantID, PersonID: probe.PersonID, ExcludeID: probe.ExcludeID,
			ReceiptSha256: probe.ReceiptSHA256, WindowFrom: probe.WindowFrom,
			ProviderOrganizationID: probe.ProviderOrganizationID,
			ServiceDate:            dateParam(probe.ServiceDate),
			RequestedAmount:        probe.RequestedAmount,
		})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.DuplicateMatch{}, nil
	}
	if err != nil {
		return application.DuplicateMatch{}, fmt.Errorf("billing: find reimbursement duplicate: %w", err)
	}
	return application.DuplicateMatch{
		Found: true, ID: row.ID, Reference: row.Reference,
		ServiceDate: dateValue(row.ServiceDate), ByReceipt: row.ByReceipt,
	}, nil
}

// ReimbursementRequest implements application.SettlementRepository.
func (SettlementRepository) ReimbursementRequest(ctx context.Context, tx pgx.Tx, tenantID,
	requestID, documentID uuid.UUID,
) (application.ReimbursementRequest, error) {
	row, err := sqlcgen.New(tx).GetReimbursementRequest(ctx,
		sqlcgen.GetReimbursementRequestParams{
			TenantID: tenantID, ServiceRequestID: requestID, DocumentID: documentID,
		})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ReimbursementRequest{}, nil
	}
	if err != nil {
		return application.ReimbursementRequest{}, fmt.Errorf("billing: reimbursement request: %w", err)
	}
	return application.ReimbursementRequest{
		Found: true, ServiceRequestID: row.ServiceRequestID, PersonID: row.PersonID,
		EnrollmentID: row.EnrollmentID, ProgramID: row.ProgramID,
		ServiceDate:            dateValue(row.ServiceDate),
		ProviderOrganizationID: uuidPtr(row.ProviderTenantOrganizationID),
		RequestType:            row.RequestType, Status: row.Status,
		ServiceDefinitionID: row.ServiceDefinitionID,
		// A clean document whose bytes retention has already purged is not a receipt anybody
		// can look at, so it is not a receipt this package will accept.
		ReceiptClean:  row.ScanStatus == scanClean && row.PurgedAt == nil,
		ReceiptSHA256: row.Sha256,
	}, nil
}

// scanClean is `document.object.scan_status` for a file the scanner passed.
const scanClean = "CLEAN"

// ReimbursementCeiling implements application.SettlementRepository. An empty string means the
// contract sets no ceiling for this service, which is ordinary.
func (SettlementRepository) ReimbursementCeiling(ctx context.Context, tx pgx.Tx, tenantID,
	serviceDefinitionID uuid.UUID, providerProfileID *uuid.UUID, asOf time.Time,
) (string, error) {
	row, err := sqlcgen.New(tx).GetReimbursementCeiling(ctx,
		sqlcgen.GetReimbursementCeilingParams{
			TenantID:            tenantID,
			ServiceDefinitionID: uuid.NullUUID{UUID: serviceDefinitionID, Valid: true},
			ProviderProfileID:   optUUID(providerProfileID), AsOf: dateParam(asOf),
		})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("billing: reimbursement ceiling: %w", err)
	}
	return row.MaxAmount, nil
}

// EnrollmentCoverage implements application.SettlementRepository.
func (SettlementRepository) EnrollmentCoverage(ctx context.Context, tx pgx.Tx, tenantID,
	enrollmentID uuid.UUID, on time.Time,
) (application.EnrollmentCoverage, error) {
	row, err := sqlcgen.New(tx).GetEnrollmentCoverage(ctx, sqlcgen.GetEnrollmentCoverageParams{
		TenantID: tenantID, ID: enrollmentID, ServiceDate: dateParam(on),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.EnrollmentCoverage{}, nil
	}
	if err != nil {
		return application.EnrollmentCoverage{}, fmt.Errorf("billing: enrollment coverage: %w", err)
	}
	return application.EnrollmentCoverage{
		Found: true, Status: row.Status, CoversDate: row.CoversDate, PersonID: row.PersonID,
	}, nil
}

// ProviderProfileOf implements application.SettlementRepository.
func (SettlementRepository) ProviderProfileOf(ctx context.Context, tx pgx.Tx, tenantID,
	organizationID uuid.UUID,
) (uuid.UUID, bool, error) {
	row, err := sqlcgen.New(tx).GetProviderProfileForOrganization(ctx,
		sqlcgen.GetProviderProfileForOrganizationParams{
			TenantID: tenantID, TenantOrganizationID: organizationID,
		})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("billing: provider profile: %w", err)
	}
	return row.ProviderProfileID, true, nil
}
