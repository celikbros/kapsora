// Package billingpg implements the invoice repository with sqlc. It is stateless: every
// method takes the caller's tenant-bound transaction, so RLS is active for every statement
// and nothing here can read another tenant's invoices.
//
// The provider boundary lives here rather than above: every read takes the caller's scope and
// hands it to SQL, so an invoice outside it is genuinely not returned. That is what lets the
// application layer answer 404 without ever having held the row.
//
// What this package deliberately does not do is decide what a caller may see. Every read
// returns the whole row — including the claim's line description, which is possibly clinical —
// and the projection is applied one layer up. A repository that filtered as well would be a
// second rule about clinical visibility, and two rules is how one of them ends up wrong.
package billingpg

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/celikbros/kapsora/internal/billing/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// The constraint names the repository turns into named errors, so a caller reads a refusal
// rather than a PostgreSQL string.
const (
	// constraintNumber is the uniqueness rule of 11.12: one number per provider per fiscal
	// year, counting only the invoices that are still live.
	constraintNumber = "uq_billing_invoice_number"
	// constraintLiveClaim is "one claim, one live invoice". The service looks first and
	// refuses by name; this is the half that holds when two callers look at the same moment.
	constraintLiveClaim = "uq_billing_invoice_claim_live"
	sqlStateUnique      = "23505"
)

// Repository implements application.Repository.
type Repository struct{}

// New returns the repository.
func New() *Repository { return &Repository{} }

var _ application.Repository = (*Repository)(nil)

// CreateInvoice implements application.Repository.
func (Repository) CreateInvoice(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewInvoiceRow,
) (application.InvoiceRecord, error) {
	row, err := sqlcgen.New(tx).CreateInvoice(ctx, sqlcgen.CreateInvoiceParams{
		TenantID: tenantID, ProviderOrganizationID: in.ProviderOrganizationID,
		PayerOrganizationID: optUUID(in.PayerOrganizationID), Source: in.Source,
		InvoiceNumber: in.InvoiceNumber, InvoiceDate: dateParam(in.InvoiceDate),
		FiscalYear:        int32(in.FiscalYear), //nolint:gosec // a year is bounded by the CHECK on the column
		ProviderTaxIDHash: in.ProviderTaxIDHash, CurrencyCode: in.CurrencyCode,
		LineExtensionAmount: in.LineExtensionAmount, TaxAmount: in.TaxAmount,
		PayableAmount: in.PayableAmount, VatRate: in.VatRate, DomainCode: in.DomainCode,
		SupersedesInvoiceID: optUUID(in.SupersedesInvoiceID),
		DocumentID:          optUUID(in.DocumentID), Notes: in.Notes,
		ActorID: optUUID(in.ActorID),
	})
	if isUniqueViolation(err, constraintNumber) {
		return application.InvoiceRecord{}, application.ErrNumberTaken
	}
	if err != nil {
		return application.InvoiceRecord{}, fmt.Errorf("billing: create invoice: %w", err)
	}
	return invoiceOf(createdInvoiceRow(row)), nil
}

// GetInvoice implements application.Repository.
func (Repository) GetInvoice(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.InvoiceRecord, error) {
	row, err := sqlcgen.New(tx).GetInvoice(ctx, sqlcgen.GetInvoiceParams{
		TenantID: tenantID, ID: id, ScopeIds: scopeIDs(scope),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.InvoiceRecord{}, application.ErrInvoiceNotFound
	}
	if err != nil {
		return application.InvoiceRecord{}, fmt.Errorf("billing: get invoice: %w", err)
	}
	return invoiceOf(getInvoiceRow(row)), nil
}

// LockInvoice implements application.Repository.
func (Repository) LockInvoice(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.InvoiceRecord, error) {
	row, err := sqlcgen.New(tx).LockInvoice(ctx, sqlcgen.LockInvoiceParams{
		TenantID: tenantID, ID: id, ScopeIds: scopeIDs(scope),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.InvoiceRecord{}, application.ErrInvoiceNotFound
	}
	if err != nil {
		return application.InvoiceRecord{}, fmt.Errorf("billing: lock invoice: %w", err)
	}
	return invoiceOf(lockInvoiceRow(row)), nil
}

// ListInvoices implements application.Repository.
func (Repository) ListInvoices(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.InvoiceQuery,
) ([]application.InvoiceSummaryRecord, error) {
	params := sqlcgen.ListInvoicesParams{
		TenantID: tenantID, ScopeIds: scopeIDs(q.Scope),
		ProviderOrganizationID: optUUID(q.ProviderOrganizationID),
		Status:                 q.Status, FiscalYear: optInt32(q.FiscalYear),
		DateFrom: optDate(q.DateFrom), DateTo: optDate(q.DateTo),
		BatchID:  optUUID(q.BatchID),
		PageSize: int32(q.PageSize), //nolint:gosec // the page size is clamped before it reaches here
	}
	if q.After != nil {
		after := q.After.CreatedAt
		params.AfterCreatedAt = &after
		params.AfterID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListInvoices(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("billing: list invoices: %w", err)
	}
	out := make([]application.InvoiceSummaryRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, summaryOf(listInvoicesRow(row)))
	}
	return out, nil
}

// ListChain implements application.Repository.
func (Repository) ListChain(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
) ([]application.InvoiceSummaryRecord, error) {
	rows, err := sqlcgen.New(tx).ListInvoiceChain(ctx, sqlcgen.ListInvoiceChainParams{
		TenantID: tenantID, ID: id,
	})
	if err != nil {
		return nil, fmt.Errorf("billing: list invoice chain: %w", err)
	}
	out := make([]application.InvoiceSummaryRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, summaryOf(chainRow(row)))
	}
	return out, nil
}

// UpdateInvoiceDraft implements application.Repository.
func (Repository) UpdateInvoiceDraft(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	in application.UpdateInvoiceRow, expected int64,
) (bool, error) {
	affected, err := sqlcgen.New(tx).UpdateInvoiceDraft(ctx, sqlcgen.UpdateInvoiceDraftParams{
		TenantID: tenantID, ID: id, PayerOrganizationID: optUUID(in.PayerOrganizationID),
		InvoiceNumber: in.InvoiceNumber, InvoiceDate: dateParam(in.InvoiceDate),
		FiscalYear:   int32(in.FiscalYear), //nolint:gosec // a year is bounded by the CHECK on the column
		CurrencyCode: in.CurrencyCode, LineExtensionAmount: in.LineExtensionAmount,
		TaxAmount: in.TaxAmount, PayableAmount: in.PayableAmount, VatRate: in.VatRate,
		DomainCode: in.DomainCode, DocumentID: optUUID(in.DocumentID), Notes: in.Notes,
		ActorID: optUUID(in.ActorID), ExpectedRowVersion: expected,
	})
	if isUniqueViolation(err, constraintNumber) {
		return false, application.ErrNumberTaken
	}
	if err != nil {
		return false, fmt.Errorf("billing: update invoice draft: %w", err)
	}
	return affected == 1, nil
}

// SetStatus implements application.Repository.
func (Repository) SetStatus(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	move application.StatusMove, expected int64,
) (bool, error) {
	affected, err := sqlcgen.New(tx).SetInvoiceStatus(ctx, sqlcgen.SetInvoiceStatusParams{
		TenantID: tenantID, ID: id, Status: move.Status, SubmittedAt: move.SubmittedAt,
		FromStatuses: move.FromStatuses, ActorID: optUUID(move.ActorID),
		ExpectedRowVersion: expected,
	})
	if err != nil {
		return false, fmt.Errorf("billing: set invoice status: %w", err)
	}
	return affected == 1, nil
}

// SetSupersededBy implements application.Repository.
func (Repository) SetSupersededBy(ctx context.Context, tx pgx.Tx, tenantID, id,
	successor uuid.UUID, actorID *uuid.UUID,
) (bool, error) {
	affected, err := sqlcgen.New(tx).SetInvoiceSupersededBy(ctx,
		sqlcgen.SetInvoiceSupersededByParams{
			TenantID: tenantID, ID: id,
			SupersededByInvoiceID: uuid.NullUUID{UUID: successor, Valid: true},
			ActorID:               optUUID(actorID),
		})
	if err != nil {
		return false, fmt.Errorf("billing: set superseded by: %w", err)
	}
	return affected == 1, nil
}

// ListAllocations implements application.Repository.
func (Repository) ListAllocations(ctx context.Context, tx pgx.Tx, tenantID, invoiceID uuid.UUID,
) ([]application.AllocationRecord, error) {
	rows, err := sqlcgen.New(tx).ListInvoiceAllocations(ctx,
		sqlcgen.ListInvoiceAllocationsParams{TenantID: tenantID, InvoiceID: invoiceID})
	if err != nil {
		return nil, fmt.Errorf("billing: list invoice allocations: %w", err)
	}
	out := make([]application.AllocationRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, allocationOf(row))
	}
	return out, nil
}

// DeleteAllocations implements application.Repository.
func (Repository) DeleteAllocations(ctx context.Context, tx pgx.Tx, tenantID, invoiceID uuid.UUID) error {
	if err := sqlcgen.New(tx).DeleteInvoiceAllocations(ctx,
		sqlcgen.DeleteInvoiceAllocationsParams{TenantID: tenantID, InvoiceID: invoiceID}); err != nil {
		return fmt.Errorf("billing: delete invoice allocations: %w", err)
	}
	return nil
}

// CreateAllocation implements application.Repository.
func (Repository) CreateAllocation(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewAllocationRow,
) error {
	err := sqlcgen.New(tx).CreateInvoiceAllocation(ctx, sqlcgen.CreateInvoiceAllocationParams{
		TenantID: tenantID, InvoiceID: in.InvoiceID, ClaimID: in.ClaimID,
		ClaimVersionNo:  int32(in.ClaimVersionNo), //nolint:gosec // a version number is bounded by the versions somebody made
		AllocatedAmount: in.AllocatedAmount, CurrencyCode: in.CurrencyCode,
		ClaimStatusBefore: in.ClaimStatusBefore, ActorID: optUUID(in.ActorID),
	})
	if isUniqueViolation(err, constraintLiveClaim) {
		// One claim, one live invoice. The service looked first and found none; this is
		// another caller having written one in between, and the honest answer names the rule
		// rather than the index.
		return &application.AllocationError{
			Kind: application.AllocationClaimAlreadyInvoiced, ClaimID: in.ClaimID,
		}
	}
	if err != nil {
		return fmt.Errorf("billing: create invoice allocation: %w", err)
	}
	return nil
}

// FindByNumber implements application.Repository.
func (Repository) FindByNumber(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.NumberQuery,
) (application.NumberHolder, error) {
	row, err := sqlcgen.New(tx).FindInvoiceByNumber(ctx, sqlcgen.FindInvoiceByNumberParams{
		TenantID: tenantID, ProviderOrganizationID: q.ProviderOrganizationID,
		FiscalYear:    int32(q.FiscalYear), //nolint:gosec // a year is bounded by the CHECK on the column
		InvoiceNumber: q.InvoiceNumber, ExcludingID: q.ExcludingID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.NumberHolder{}, nil
	}
	if err != nil {
		return application.NumberHolder{}, fmt.Errorf("billing: find invoice by number: %w", err)
	}
	return application.NumberHolder{Found: true, ID: row.ID, Status: row.Status}, nil
}

// ProviderTaxIdentity implements application.Repository.
func (Repository) ProviderTaxIdentity(ctx context.Context, tx pgx.Tx, tenantID,
	providerID uuid.UUID,
) (application.TaxIdentity, error) {
	row, err := sqlcgen.New(tx).GetInvoiceProviderTaxIdentity(ctx,
		sqlcgen.GetInvoiceProviderTaxIdentityParams{TenantID: tenantID, ID: providerID})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.TaxIdentity{}, nil
	}
	if err != nil {
		return application.TaxIdentity{}, fmt.Errorf("billing: provider tax identity: %w", err)
	}
	return application.TaxIdentity{
		Found: true, DisplayName: row.DisplayName, TaxNumberHash: row.TaxNumberHash,
		RelationshipRole: row.RelationshipRole, Status: row.Status,
		IsProvider: row.RelationshipRole == "PROVIDER" &&
			(row.Status == "PENDING" || row.Status == "ACTIVE"),
	}, nil
}

// InvoiceableClaims implements application.Repository.
func (Repository) InvoiceableClaims(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	ids []uuid.UUID,
) ([]application.ClaimCandidate, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := sqlcgen.New(tx).ListInvoiceableClaims(ctx, sqlcgen.ListInvoiceableClaimsParams{
		TenantID: tenantID, ClaimIds: ids,
	})
	if err != nil {
		return nil, fmt.Errorf("billing: list invoiceable claims: %w", err)
	}
	out := make([]application.ClaimCandidate, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.ClaimCandidate{
			ClaimID: row.ClaimID, Reference: row.Reference, Status: row.Status,
			CurrentVersionNo:       int(row.CurrentVersionNo),
			ProviderOrganizationID: row.ProviderOrganizationID,
			CurrencyCode:           row.CurrencyCode, ApprovedTotal: row.ApprovedTotal,
			LiveInvoiceID: row.LiveInvoiceID,
		})
	}
	return out, nil
}

// DocumentIsClean implements application.Repository.
func (Repository) DocumentIsClean(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID,
) (bool, error) {
	n, err := sqlcgen.New(tx).CountCleanInvoiceDocuments(ctx,
		sqlcgen.CountCleanInvoiceDocumentsParams{TenantID: tenantID, ID: documentID})
	if err != nil {
		return false, fmt.Errorf("billing: count clean invoice documents: %w", err)
	}
	return n > 0, nil
}

// LinkDocument implements application.Repository.
func (Repository) LinkDocument(ctx context.Context, tx pgx.Tx, tenantID, invoiceID,
	documentID uuid.UUID, actorID *uuid.UUID,
) error {
	if err := sqlcgen.New(tx).LinkInvoiceDocument(ctx, sqlcgen.LinkInvoiceDocumentParams{
		TenantID: tenantID, ObjectID: documentID, AggregateID: invoiceID,
		ActorID: optUUID(actorID),
	}); err != nil {
		return fmt.Errorf("billing: link invoice document: %w", err)
	}
	return nil
}

// isUniqueViolation reports whether err is a unique violation of the named constraint.
func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == sqlStateUnique &&
		pgErr.ConstraintName == constraint
}

// scopeIDs turns "no restriction" into the empty array the queries read as "no restriction".
// A restricted caller with no organization is restricted to nothing, and the empty non-nil
// slice below would be indistinguishable from "unrestricted" — so it is answered with a uuid
// nobody has instead.
func scopeIDs(scope application.Scope) []uuid.UUID {
	if !scope.Restricted() {
		return []uuid.UUID{}
	}
	if len(scope.OrganizationIDs) == 0 {
		return []uuid.UUID{uuid.Nil}
	}
	return scope.OrganizationIDs
}

func optUUID(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}

func optInt32(v *int) *int32 {
	if v == nil {
		return nil
	}
	n := int32(*v) //nolint:gosec // a fiscal year is bounded by the filter's own validation
	return &n
}
