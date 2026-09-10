package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// The icmal's vocabulary: what a batch is, what one decision on one invoice is, and the two
// ports the review reaches other modules through (WP-I7-03).

// Permissions guarding batch work. All three are in the catalogue from migration 000008 and
// all three are already granted by the role templates: `batch.create` and `batch.submit` to
// the provider's billing clerk, `batch.review` to the payer's financial reviewer. This package
// adds none — a package that invented a permission here would invent one nobody holds.
const (
	PermissionBatchCreate = "batch.create"
	PermissionBatchSubmit = "batch.submit"
	PermissionBatchReview = "batch.review"
)

// Errors mapped by the transport layer to problem codes.
var (
	ErrBatchNotFound = errors.New("billing: batch not found")
	// ErrBatchFrozen is the rule the whole package exists for: a submitted batch's membership
	// and amounts never change.
	ErrBatchFrozen = errors.New("billing: the batch has left DRAFT and its membership is frozen")
	// ErrBatchTransitionInvalid is a command run from a status it cannot run from.
	ErrBatchTransitionInvalid = errors.New("billing: the batch is not in a state this command can run from")
	// ErrBatchInvoiceNotFound is an invoice this batch does not carry.
	ErrBatchInvoiceNotFound = errors.New("billing: the invoice is not in this batch")
	// ErrBatchNotFullyDecided refuses closing a batch somebody has not finished reviewing.
	// It is not a validation error: nothing about the request is wrong, and the answer is to
	// go on reviewing.
	ErrBatchNotFullyDecided = errors.New("billing: every invoice in the batch has to be decided first")
	// ErrBatchSubmitterCannotDecide is the maker-checker rule of WP-I4-03 section 2.4: the
	// person who sent the batch is never the person who answers it. It is a separate refusal
	// from a permission denial because the caller is allowed to review — just not this one.
	ErrBatchSubmitterCannotDecide = errors.New("billing: the submitter of a batch may not decide it")
	// ErrBatchSecondReviewerRequired is the other half, above the tenant's threshold: the
	// person who took the last decision may not be the one who closes the batch.
	ErrBatchSecondReviewerRequired = errors.New("billing: a batch above the threshold needs a second reviewer")
	// ErrBatchTotalsMismatch is the reconciliation refusing to write. It is a bug rather than
	// a user error and is answered as one; it exists so that the arithmetic is refused here as
	// well as by `ck_billing_batch_totals`.
	ErrBatchTotalsMismatch = errors.New("billing: the decided totals do not add up to the submitted total")
)

// BatchSizeError is the tenant's min/max refusing a submit, carrying all three figures: a
// provider told "too many invoices" and not how many too many has been told nothing.
type BatchSizeError struct {
	Count int
	Min   int
	Max   int
}

func (e *BatchSizeError) Error() string {
	return "billing: the batch size is outside the tenant's bounds"
}

// ErrBatchSize lets callers detect a BatchSizeError with errors.Is.
var ErrBatchSize = errors.New("billing: batch size out of range")

// Is lets errors.Is(err, ErrBatchSize) match a *BatchSizeError.
func (e *BatchSizeError) Is(target error) bool { return target == ErrBatchSize }

// MixedBatchError is `putBatchInvoices` refusing an invoice that does not belong with the
// others. It names the field, both values and the invoice, because "mixed batch" on its own is
// not something a provider can act on and "invoice KPS2026000041 is in EUR, the batch is in
// TRY" is.
type MixedBatchError struct {
	// Field is the request-shaped name of what disagreed: `currencyCode`, `domainCode`,
	// `payerOrganizationId`, `providerOrganizationId` or `status`.
	Field         string
	InvoiceID     uuid.UUID
	InvoiceNumber string
	ExpectedValue string
	ActualValue   string
}

func (e *MixedBatchError) Error() string { return "billing: the batch would mix " + e.Field }

// ErrBatchMixed lets callers detect a MixedBatchError with errors.Is.
var ErrBatchMixed = errors.New("billing: the batch would mix invoices that do not belong together")

// Is lets errors.Is(err, ErrBatchMixed) match a *MixedBatchError.
func (e *MixedBatchError) Is(target error) bool { return target == ErrBatchMixed }

// InvoiceBatchedError is the one-live-batch-per-invoice rule, naming the icmal the invoice is
// already in rather than saying that an invoice the provider can see does not exist.
type InvoiceBatchedError struct {
	InvoiceID     uuid.UUID
	InvoiceNumber string
	LiveBatchID   uuid.UUID
}

func (e *InvoiceBatchedError) Error() string {
	return "billing: the invoice is already in a live batch"
}

// ErrInvoiceBatched lets callers detect an InvoiceBatchedError with errors.Is.
var ErrInvoiceBatched = errors.New("billing: the invoice is already in a live batch")

// Is lets errors.Is(err, ErrInvoiceBatched) match an *InvoiceBatchedError.
func (e *InvoiceBatchedError) Is(target error) bool { return target == ErrInvoiceBatched }

// BatchRecord is one icmal header as the repository reads it. Every money field is exact
// decimal text and nothing here is ever a float.
type BatchRecord struct {
	ID                     uuid.UUID
	Reference              string
	ProviderOrganizationID uuid.UUID
	ProviderName           string
	PayerOrganizationID    *uuid.UUID
	DomainCode             string
	CurrencyCode           string
	PeriodFrom             time.Time
	PeriodTo               time.Time
	Status                 string
	SubmittedAt            *time.Time
	SubmittedBy            *uuid.UUID
	DecidedAt              *time.Time
	DecidedBy              *uuid.UUID
	InvoiceCount           int
	SubmittedTotal         string
	ApprovedTotal          string
	CutTotal               string
	ReturnedTotal          string
	RejectedTotal          string
	CreatedAt              time.Time
	RowVersion             int64
}

// BatchInvoiceRecord is one invoice in one batch, with the payer's answer to it and enough of
// the invoice itself for a reviewer to read the row without a second request.
type BatchInvoiceRecord struct {
	ID              uuid.UUID
	InvoiceID       uuid.UUID
	InvoiceNumber   string
	InvoiceDate     time.Time
	InvoiceStatus   string
	CurrencyCode    string
	PayableAmount   string
	SubmittedAmount string
	// Decision and ApprovedAmount are empty on a member nobody has answered yet.
	Decision             string
	ApprovedAmount       string
	ReasonCode           string
	ReasonText           *string
	DecidedBy            *uuid.UUID
	DecidedByDisplayName *string
	DecidedAt            *time.Time
	Active               bool
	CreatedAt            time.Time
}

// Decided reports whether the payer has answered this invoice.
func (r BatchInvoiceRecord) Decided() bool { return r.Decision != "" }

// BatchView is one batch with its invoices.
type BatchView struct {
	Batch    BatchRecord
	Invoices []BatchInvoiceRecord
}

// BatchPage is one page of batches.
type BatchPage struct {
	Items      []BatchRecord
	NextCursor string
}

// BatchDecisionCount is one word of the four, with how many invoices carry it and what they
// add up to.
type BatchDecisionCount struct {
	Decision       string
	Count          int
	SubmittedTotal string
	ApprovedTotal  string
}

// BatchSummaryView is what both sides read: the totals, the counts per decision, and how much
// is still waiting for an answer.
type BatchSummaryView struct {
	Batch BatchRecord
	// PendingCount is how many invoices nobody has answered yet, and PendingTotal what they
	// are worth. A reviewer's screen is a progress bar and this is the numerator.
	PendingCount int
	PendingTotal string
	Decisions    []BatchDecisionCount
}

// NewBatchRow is the header as it is written.
type NewBatchRow struct {
	Reference              string
	ProviderOrganizationID uuid.UUID
	PayerOrganizationID    *uuid.UUID
	DomainCode             string
	CurrencyCode           string
	PeriodFrom             time.Time
	PeriodTo               time.Time
	ActorID                *uuid.UUID
}

// NewBatchInvoiceRow is one membership row as it is written.
type NewBatchInvoiceRow struct {
	BatchID         uuid.UUID
	InvoiceID       uuid.UUID
	SubmittedAmount string
	ActorID         *uuid.UUID
}

// SubmitBatchRow is everything the submit writes onto the header at once.
type SubmitBatchRow struct {
	SubmittedAt    time.Time
	SubmittedBy    uuid.UUID
	InvoiceCount   int
	SubmittedTotal string
	ActorID        *uuid.UUID
}

// DecideBatchRow is the four totals and who closed the batch.
type DecideBatchRow struct {
	DecidedAt     time.Time
	DecidedBy     uuid.UUID
	ApprovedTotal string
	CutTotal      string
	ReturnedTotal string
	RejectedTotal string
	ActorID       *uuid.UUID
}

// BatchDecisionRow is one decision as it is written. It replaces whatever was there: a changed
// decision is the same row with the last answer on it, and what stops that being an edit of
// history is that the money it moved is reversed rather than rewritten.
type BatchDecisionRow struct {
	Decision       string
	ApprovedAmount string
	ReasonCode     *string
	ReasonText     *string
	DecidedBy      uuid.UUID
	DecidedAt      time.Time
}

// BatchQuery is the list filter, already validated.
type BatchQuery struct {
	Scope                  Scope
	ProviderOrganizationID *uuid.UUID
	PayerOrganizationID    *uuid.UUID
	Status                 *string
	DomainCode             *string
	CurrencyCode           *string
	DateFrom               *time.Time
	DateTo                 *time.Time
	After                  *httpx.Cursor
	PageSize               int
}

// BatchCandidate is one invoice a `putBatchInvoices` may name, as the repository reads it.
type BatchCandidate struct {
	InvoiceID              uuid.UUID
	ProviderOrganizationID uuid.UUID
	PayerOrganizationID    *uuid.UUID
	Status                 string
	CurrencyCode           string
	DomainCode             string
	InvoiceNumber          string
	InvoiceDate            time.Time
	PayableAmount          string
	// LiveBatchID is the batch this invoice already sits in, or uuid.Nil.
	LiveBatchID uuid.UUID
}

// NewBatchAdjustmentLink records which `claim.adjustment` row a CUT wrote.
type NewBatchAdjustmentLink struct {
	BatchInvoiceID uuid.UUID
	ClaimID        uuid.UUID
	AdjustmentID   uuid.UUID
	Amount         string
	ActorID        *uuid.UUID
}

// BatchAdjustmentLink is one such row, read back so a changed decision can take it away.
type BatchAdjustmentLink struct {
	ID           uuid.UUID
	ClaimID      uuid.UUID
	AdjustmentID uuid.UUID
	Amount       string
}

// BatchRepository is the persistence port of the icmal. It is a second interface rather than
// more methods on Repository so that a test double for the invoice does not have to grow
// fourteen methods it never calls.
type BatchRepository interface {
	CreateBatch(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewBatchRow) (BatchRecord, error)
	GetBatch(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (BatchRecord, error)
	LockBatch(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (BatchRecord, error)
	ListBatches(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q BatchQuery) ([]BatchRecord, error)
	SubmitBatch(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in SubmitBatchRow, expected int64) (bool, error)
	// StartBatchReview is SUBMITTED to UNDER_REVIEW, and it is the first decision's side
	// effect rather than a command of its own.
	StartBatchReview(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, actorID *uuid.UUID) (bool, error)
	DecideBatch(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in DecideBatchRow, expected int64) (bool, error)
	// TouchDraftBatch moves the row version of a DRAFT batch whose membership was replaced.
	TouchDraftBatch(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, actorID *uuid.UUID, expected int64) (bool, error)
	DeleteBatchInvoices(ctx context.Context, tx pgx.Tx, tenantID, batchID uuid.UUID) error
	CreateBatchInvoice(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewBatchInvoiceRow) error
	ListBatchInvoices(ctx context.Context, tx pgx.Tx, tenantID, batchID uuid.UUID) ([]BatchInvoiceRecord, error)
	LockBatchInvoice(ctx context.Context, tx pgx.Tx, tenantID, batchID, invoiceID uuid.UUID) (BatchInvoiceRecord, error)
	SetBatchInvoiceDecision(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in BatchDecisionRow) (bool, error)
	BatchCandidates(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, ids []uuid.UUID) ([]BatchCandidate, error)
	// SetInvoiceBatch moves one SUBMITTED invoice into the batch it was put in.
	SetInvoiceBatch(ctx context.Context, tx pgx.Tx, tenantID, invoiceID, batchID uuid.UUID, actorID *uuid.UUID) (bool, error)
	// SetInvoiceReviewStatus is where one decision leaves the invoice.
	SetInvoiceReviewStatus(ctx context.Context, tx pgx.Tx, tenantID, invoiceID uuid.UUID, status string, from []string, actorID *uuid.UUID) (bool, error)
	CreateBatchAdjustmentLink(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewBatchAdjustmentLink) error
	ListLiveBatchAdjustments(ctx context.Context, tx pgx.Tx, tenantID, batchInvoiceID uuid.UUID) ([]BatchAdjustmentLink, error)
	MarkBatchAdjustmentReversed(ctx context.Context, tx pgx.Tx, tenantID, id, reversalID uuid.UUID) error
}

// ClaimCut is one claim's share of a cut the payer applied to an invoice, in this package's
// vocabulary rather than the claim module's.
type ClaimCut struct {
	ClaimID    uuid.UUID
	Amount     string
	ReasonCode string
	ReasonText *string
}

// ClaimReversal names one adjustment to take back. The amount is deliberately absent: it is
// read off the row being reversed, which is what makes a reversal exact.
type ClaimReversal struct {
	ClaimID      uuid.UUID
	AdjustmentID uuid.UUID
	ReasonText   *string
}

// ClaimAdjustment is one row that was written, named so this package can find it again.
type ClaimAdjustment struct {
	ClaimID      uuid.UUID
	AdjustmentID uuid.UUID
	Amount       string
}

// RaiseWorkItem is one piece of work handed to WP-I4-03's queue.
type RaiseWorkItem struct {
	QueueCode     string
	AggregateType string
	AggregateID   uuid.UUID
	Title         string
	ActorID       *uuid.UUID
}

// WorkItemPort is the worklist seen from the icmal: raise the review, take it when somebody
// starts, and close it when the batch is decided.
//
// It is a port rather than a call into the worklist service because that service opens a
// transaction of its own: a work item that committed while the submit it belongs to rolled
// back would be work nobody can explain, and a decided batch whose item stayed open would be a
// queue full of work already done.
type WorkItemPort interface {
	Raise(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in RaiseWorkItem) error
	// Claim assigns the open item of an aggregate to the person who has just started work on
	// it. An item somebody else already holds is left alone.
	Claim(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, aggregateType string,
		aggregateID, assignee uuid.UUID, actorID *uuid.UUID) error
	Complete(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, aggregateType string,
		aggregateID uuid.UUID, outcomeCode string, actorID *uuid.UUID) error
}

// NoWorkItems is the default WorkItemPort: it raises nothing and refuses nothing.
//
// Unlike NoClaims, silence is the right answer here. A tenant that has configured no review
// queue has not configured one, refusing the submit would not make anybody watch a queue, and
// a provider told "your icmal cannot be sent because the payer has no work queue" is a
// provider told about somebody else's configuration.
type NoWorkItems struct{}

// Raise implements WorkItemPort.
func (NoWorkItems) Raise(context.Context, pgx.Tx, uuid.UUID, RaiseWorkItem) error { return nil }

// Claim implements WorkItemPort.
func (NoWorkItems) Claim(context.Context, pgx.Tx, uuid.UUID, string, uuid.UUID, uuid.UUID,
	*uuid.UUID,
) error {
	return nil
}

// Complete implements WorkItemPort.
func (NoWorkItems) Complete(context.Context, pgx.Tx, uuid.UUID, string, uuid.UUID, string,
	*uuid.UUID,
) error {
	return nil
}
