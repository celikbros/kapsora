package billingpg

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/billing/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// The icmal's repository (WP-I7-03). It is stateless in the same way the invoice's is: every
// method takes the caller's tenant-bound transaction, so RLS is active for every statement and
// the provider boundary is applied in SQL rather than after the read.

// The constraint names this repository turns into named errors, so a caller reads a refusal
// rather than a PostgreSQL string.
const (
	// constraintBatchReference is the reference's uniqueness inside the tenant. It never
	// reaches a caller: the create retries with a new random tail.
	constraintBatchReference = "uq_billing_batch_reference"
	// constraintLiveBatchInvoice is "one invoice, one live batch". The service looks first and
	// refuses by name; this is the half that holds when two callers look at the same moment.
	constraintLiveBatchInvoice = "uq_billing_batch_invoice_live"
	// constraintLiveClaimInvoice is migration 000044's "one claim, one live invoice". It is
	// named here because putting a returned invoice back into its batch reactivates its claim
	// links, and a claim that has meanwhile gone onto a correction belongs to that document.
	constraintLiveClaimInvoice = "uq_billing_invoice_claim_live"
)

// BatchRepository implements application.BatchRepository.
type BatchRepository struct{}

// NewBatchRepository returns the repository.
func NewBatchRepository() *BatchRepository { return &BatchRepository{} }

var _ application.BatchRepository = (*BatchRepository)(nil)

// CreateBatch implements application.BatchRepository.
func (BatchRepository) CreateBatch(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewBatchRow,
) (application.BatchRecord, error) {
	row, err := sqlcgen.New(tx).CreateBatch(ctx, sqlcgen.CreateBatchParams{
		TenantID: tenantID, Reference: in.Reference,
		ProviderOrganizationID: in.ProviderOrganizationID,
		PayerOrganizationID:    optUUID(in.PayerOrganizationID),
		DomainCode:             in.DomainCode, CurrencyCode: in.CurrencyCode,
		PeriodFrom: dateParam(in.PeriodFrom), PeriodTo: dateParam(in.PeriodTo),
		ActorID: optUUID(in.ActorID),
	})
	if isUniqueViolation(err, constraintBatchReference) {
		return application.BatchRecord{}, application.ErrBatchReferenceTaken
	}
	if err != nil {
		return application.BatchRecord{}, fmt.Errorf("billing: create batch: %w", err)
	}
	return batchOf(createdBatchRow(row)), nil
}

// GetBatch implements application.BatchRepository.
func (BatchRepository) GetBatch(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.BatchRecord, error) {
	row, err := sqlcgen.New(tx).GetBatch(ctx, sqlcgen.GetBatchParams{
		TenantID: tenantID, ID: id, ScopeIds: scopeIDs(scope),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.BatchRecord{}, application.ErrBatchNotFound
	}
	if err != nil {
		return application.BatchRecord{}, fmt.Errorf("billing: get batch: %w", err)
	}
	return batchOf(getBatchRow(row)), nil
}

// LockBatch implements application.BatchRepository.
func (BatchRepository) LockBatch(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.BatchRecord, error) {
	row, err := sqlcgen.New(tx).LockBatch(ctx, sqlcgen.LockBatchParams{
		TenantID: tenantID, ID: id, ScopeIds: scopeIDs(scope),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.BatchRecord{}, application.ErrBatchNotFound
	}
	if err != nil {
		return application.BatchRecord{}, fmt.Errorf("billing: lock batch: %w", err)
	}
	return batchOf(lockBatchRow(row)), nil
}

// ListBatches implements application.BatchRepository.
func (BatchRepository) ListBatches(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.BatchQuery,
) ([]application.BatchRecord, error) {
	params := sqlcgen.ListBatchesParams{
		TenantID: tenantID, ScopeIds: scopeIDs(q.Scope),
		ProviderOrganizationID: optUUID(q.ProviderOrganizationID),
		PayerOrganizationID:    optUUID(q.PayerOrganizationID),
		Status:                 q.Status, DomainCode: q.DomainCode, CurrencyCode: q.CurrencyCode,
		DateFrom: optDate(q.DateFrom), DateTo: optDate(q.DateTo),
		PageSize: int32(q.PageSize), //nolint:gosec // the page size is clamped before it reaches here
	}
	if q.After != nil {
		after := q.After.CreatedAt
		params.AfterCreatedAt = &after
		params.AfterID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListBatches(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("billing: list batches: %w", err)
	}
	out := make([]application.BatchRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, batchOf(listBatchesRow(row)))
	}
	return out, nil
}

// SubmitBatch implements application.BatchRepository.
func (BatchRepository) SubmitBatch(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	in application.SubmitBatchRow, expected int64,
) (bool, error) {
	submittedAt := in.SubmittedAt
	affected, err := sqlcgen.New(tx).SubmitBatch(ctx, sqlcgen.SubmitBatchParams{
		TenantID: tenantID, ID: id, SubmittedAt: &submittedAt,
		SubmittedBy:    uuid.NullUUID{UUID: in.SubmittedBy, Valid: in.SubmittedBy != uuid.Nil},
		InvoiceCount:   int32(in.InvoiceCount), //nolint:gosec // the count is bounded by the tenant's maximum
		SubmittedTotal: in.SubmittedTotal, ActorID: optUUID(in.ActorID),
		ExpectedRowVersion: expected,
	})
	if err != nil {
		return false, fmt.Errorf("billing: submit batch: %w", err)
	}
	return affected == 1, nil
}

// StartBatchReview implements application.BatchRepository.
func (BatchRepository) StartBatchReview(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	actorID *uuid.UUID,
) (bool, error) {
	affected, err := sqlcgen.New(tx).StartBatchReview(ctx, sqlcgen.StartBatchReviewParams{
		TenantID: tenantID, ID: id, ActorID: optUUID(actorID),
	})
	if err != nil {
		return false, fmt.Errorf("billing: start batch review: %w", err)
	}
	return affected == 1, nil
}

// DecideBatch implements application.BatchRepository.
func (BatchRepository) DecideBatch(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	in application.DecideBatchRow, expected int64,
) (bool, error) {
	decidedAt := in.DecidedAt
	affected, err := sqlcgen.New(tx).DecideBatch(ctx, sqlcgen.DecideBatchParams{
		TenantID: tenantID, ID: id, DecidedAt: &decidedAt,
		DecidedBy:     uuid.NullUUID{UUID: in.DecidedBy, Valid: in.DecidedBy != uuid.Nil},
		ApprovedTotal: in.ApprovedTotal, CutTotal: in.CutTotal,
		ReturnedTotal: in.ReturnedTotal, RejectedTotal: in.RejectedTotal,
		ActorID: optUUID(in.ActorID), ExpectedRowVersion: expected,
	})
	if err != nil {
		return false, fmt.Errorf("billing: decide batch: %w", err)
	}
	return affected == 1, nil
}

// TouchDraftBatch implements application.BatchRepository.
func (BatchRepository) TouchDraftBatch(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	actorID *uuid.UUID, expected int64,
) (bool, error) {
	affected, err := sqlcgen.New(tx).TouchDraftBatch(ctx, sqlcgen.TouchDraftBatchParams{
		TenantID: tenantID, ID: id, ActorID: optUUID(actorID), ExpectedRowVersion: expected,
	})
	if err != nil {
		return false, fmt.Errorf("billing: touch draft batch: %w", err)
	}
	return affected == 1, nil
}

// DeleteBatchInvoices implements application.BatchRepository.
func (BatchRepository) DeleteBatchInvoices(ctx context.Context, tx pgx.Tx,
	tenantID, batchID uuid.UUID,
) error {
	if err := sqlcgen.New(tx).DeleteBatchInvoices(ctx, sqlcgen.DeleteBatchInvoicesParams{
		TenantID: tenantID, BatchID: batchID,
	}); err != nil {
		return fmt.Errorf("billing: delete batch invoices: %w", err)
	}
	return nil
}

// CreateBatchInvoice implements application.BatchRepository.
func (BatchRepository) CreateBatchInvoice(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewBatchInvoiceRow,
) error {
	err := sqlcgen.New(tx).CreateBatchInvoice(ctx, sqlcgen.CreateBatchInvoiceParams{
		TenantID: tenantID, BatchID: in.BatchID, InvoiceID: in.InvoiceID,
		SubmittedAmount: in.SubmittedAmount, ActorID: optUUID(in.ActorID),
	})
	if isUniqueViolation(err, constraintLiveBatchInvoice) {
		// One invoice, one live batch. The service looked first and found none; this is
		// another caller having written one in between, and the honest answer names the rule
		// rather than the index.
		return &application.InvoiceBatchedError{InvoiceID: in.InvoiceID}
	}
	if err != nil {
		return fmt.Errorf("billing: create batch invoice: %w", err)
	}
	return nil
}

// ListBatchInvoices implements application.BatchRepository.
func (BatchRepository) ListBatchInvoices(ctx context.Context, tx pgx.Tx,
	tenantID, batchID uuid.UUID,
) ([]application.BatchInvoiceRecord, error) {
	rows, err := sqlcgen.New(tx).ListBatchInvoices(ctx, sqlcgen.ListBatchInvoicesParams{
		TenantID: tenantID, BatchID: batchID,
	})
	if err != nil {
		return nil, fmt.Errorf("billing: list batch invoices: %w", err)
	}
	out := make([]application.BatchInvoiceRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.BatchInvoiceRecord{
			ID: row.ID, InvoiceID: row.InvoiceID, InvoiceNumber: row.InvoiceNumber,
			InvoiceDate: dateValue(row.InvoiceDate), InvoiceStatus: row.InvoiceStatus,
			CurrencyCode: row.CurrencyCode, PayableAmount: row.PayableAmount,
			SubmittedAmount: row.SubmittedAmount, Decision: row.Decision,
			ApprovedAmount: row.ApprovedAmount, ReasonCode: row.ReasonCode,
			ReasonText: row.ReasonText, DecidedBy: uuidPtr(row.DecidedBy),
			DecidedByDisplayName: row.DecidedByDisplayName, DecidedAt: row.DecidedAt,
			Active: row.Active, CreatedAt: row.CreatedAt,
		})
	}
	return out, nil
}

// LockBatchInvoice implements application.BatchRepository.
func (BatchRepository) LockBatchInvoice(ctx context.Context, tx pgx.Tx,
	tenantID, batchID, invoiceID uuid.UUID,
) (application.BatchInvoiceRecord, error) {
	row, err := sqlcgen.New(tx).LockBatchInvoice(ctx, sqlcgen.LockBatchInvoiceParams{
		TenantID: tenantID, BatchID: batchID, InvoiceID: invoiceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.BatchInvoiceRecord{}, application.ErrBatchInvoiceNotFound
	}
	if err != nil {
		return application.BatchInvoiceRecord{}, fmt.Errorf("billing: lock batch invoice: %w", err)
	}
	return application.BatchInvoiceRecord{
		ID: row.ID, InvoiceID: row.InvoiceID, SubmittedAmount: row.SubmittedAmount,
		Decision: row.Decision, ApprovedAmount: row.ApprovedAmount,
		ReasonCode: row.ReasonCode, ReasonText: row.ReasonText,
		DecidedBy: uuidPtr(row.DecidedBy), DecidedAt: row.DecidedAt,
		Active: row.Active, CreatedAt: row.CreatedAt,
	}, nil
}

// SetBatchInvoiceDecision implements application.BatchRepository.
func (BatchRepository) SetBatchInvoiceDecision(ctx context.Context, tx pgx.Tx,
	tenantID, id uuid.UUID, in application.BatchDecisionRow,
) (bool, error) {
	decision := in.Decision
	decidedAt := in.DecidedAt
	affected, err := sqlcgen.New(tx).SetBatchInvoiceDecision(ctx,
		sqlcgen.SetBatchInvoiceDecisionParams{
			TenantID: tenantID, ID: id, Decision: &decision,
			ApprovedAmount: in.ApprovedAmount, ReasonCode: in.ReasonCode,
			ReasonText: in.ReasonText,
			DecidedBy:  uuid.NullUUID{UUID: in.DecidedBy, Valid: in.DecidedBy != uuid.Nil},
			DecidedAt:  &decidedAt,
		})
	if err != nil {
		return false, fmt.Errorf("billing: set batch invoice decision: %w", err)
	}
	return affected == 1, nil
}

// BatchCandidates implements application.BatchRepository.
func (BatchRepository) BatchCandidates(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	ids []uuid.UUID,
) ([]application.BatchCandidate, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := sqlcgen.New(tx).ListBatchCandidateInvoices(ctx,
		sqlcgen.ListBatchCandidateInvoicesParams{TenantID: tenantID, InvoiceIds: ids})
	if err != nil {
		return nil, fmt.Errorf("billing: list batch candidate invoices: %w", err)
	}
	out := make([]application.BatchCandidate, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.BatchCandidate{
			InvoiceID: row.ID, ProviderOrganizationID: row.ProviderOrganizationID,
			PayerOrganizationID: uuidPtr(row.PayerOrganizationID), Status: row.Status,
			CurrencyCode: row.CurrencyCode, DomainCode: row.DomainCode,
			InvoiceNumber: row.InvoiceNumber, InvoiceDate: dateValue(row.InvoiceDate),
			PayableAmount: row.PayableAmount, LiveBatchID: row.LiveBatchID,
		})
	}
	return out, nil
}

// SetInvoiceBatch implements application.BatchRepository.
func (BatchRepository) SetInvoiceBatch(ctx context.Context, tx pgx.Tx,
	tenantID, invoiceID, batchID uuid.UUID, actorID *uuid.UUID,
) (bool, error) {
	affected, err := sqlcgen.New(tx).SetInvoiceBatch(ctx, sqlcgen.SetInvoiceBatchParams{
		TenantID: tenantID, ID: invoiceID,
		BatchID: uuid.NullUUID{UUID: batchID, Valid: true}, ActorID: optUUID(actorID),
	})
	if err != nil {
		return false, fmt.Errorf("billing: set invoice batch: %w", err)
	}
	return affected == 1, nil
}

// SetInvoiceReviewStatus implements application.BatchRepository.
//
// The one refusal it translates is the claim link the status change reactivates: putting a
// returned invoice back into its batch republishes its claims, and a claim that has meanwhile
// been put on the correction belongs to that document now.
func (BatchRepository) SetInvoiceReviewStatus(ctx context.Context, tx pgx.Tx,
	tenantID, invoiceID uuid.UUID, status string, from []string, actorID *uuid.UUID,
) (bool, error) {
	affected, err := sqlcgen.New(tx).SetInvoiceReviewStatus(ctx,
		sqlcgen.SetInvoiceReviewStatusParams{
			TenantID: tenantID, ID: invoiceID, Status: status, FromStatuses: from,
			ActorID: optUUID(actorID),
		})
	if isUniqueViolation(err, constraintLiveClaimInvoice) {
		return false, &application.AllocationError{
			Kind: application.AllocationClaimAlreadyInvoiced,
		}
	}
	// Putting a returned invoice back into its icmal puts its number back into the live index,
	// and the correction the provider raised in the meantime may be holding it. That is a
	// refusal a reviewer can act on rather than an error nobody can read.
	if isUniqueViolation(err, constraintNumber) {
		return false, application.ErrNumberTaken
	}
	if err != nil {
		return false, fmt.Errorf("billing: set invoice review status: %w", err)
	}
	return affected == 1, nil
}

// CreateBatchAdjustmentLink implements application.BatchRepository.
func (BatchRepository) CreateBatchAdjustmentLink(ctx context.Context, tx pgx.Tx,
	tenantID uuid.UUID, in application.NewBatchAdjustmentLink,
) error {
	if err := sqlcgen.New(tx).CreateBatchInvoiceAdjustment(ctx,
		sqlcgen.CreateBatchInvoiceAdjustmentParams{
			TenantID: tenantID, BatchInvoiceID: in.BatchInvoiceID, ClaimID: in.ClaimID,
			AdjustmentID: in.AdjustmentID, Amount: in.Amount, ActorID: optUUID(in.ActorID),
		}); err != nil {
		return fmt.Errorf("billing: create batch adjustment link: %w", err)
	}
	return nil
}

// ListLiveBatchAdjustments implements application.BatchRepository.
func (BatchRepository) ListLiveBatchAdjustments(ctx context.Context, tx pgx.Tx,
	tenantID, batchInvoiceID uuid.UUID,
) ([]application.BatchAdjustmentLink, error) {
	rows, err := sqlcgen.New(tx).ListLiveBatchInvoiceAdjustments(ctx,
		sqlcgen.ListLiveBatchInvoiceAdjustmentsParams{
			TenantID: tenantID, BatchInvoiceID: batchInvoiceID,
		})
	if err != nil {
		return nil, fmt.Errorf("billing: list live batch adjustments: %w", err)
	}
	out := make([]application.BatchAdjustmentLink, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.BatchAdjustmentLink{
			ID: row.ID, ClaimID: row.ClaimID, AdjustmentID: row.AdjustmentID,
			Amount: row.Amount,
		})
	}
	return out, nil
}

// MarkBatchAdjustmentReversed implements application.BatchRepository.
func (BatchRepository) MarkBatchAdjustmentReversed(ctx context.Context, tx pgx.Tx,
	tenantID, id, reversalID uuid.UUID,
) error {
	if err := sqlcgen.New(tx).SetBatchInvoiceAdjustmentReversed(ctx,
		sqlcgen.SetBatchInvoiceAdjustmentReversedParams{
			TenantID: tenantID, ID: id,
			ReversedByAdjustmentID: uuid.NullUUID{UUID: reversalID, Valid: true},
		}); err != nil {
		return fmt.Errorf("billing: mark batch adjustment reversed: %w", err)
	}
	return nil
}
