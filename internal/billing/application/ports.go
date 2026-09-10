// Package application implements the invoice use cases: recording the header a provider
// entered, replacing the set of claims it covers, submitting it against the tenant's
// tolerance, withdrawing it, and reading the chain a correction belongs to. Transactions are
// opened here with db.WithTenantTx, so a write, its audit row and the event it owes commit
// together and RLS is bound for every statement.
//
// Four things this package never does.
//
// It never computes an invoice. Every figure is what the provider typed — the tax, the rate,
// the total — and the only arithmetic performed here is the two comparisons the package
// exists for: that the header adds up, and that the allocations add up to it within the
// tenant's tolerance. A figure this platform derived would be this platform's opinion about
// somebody else's fiscal document.
//
// It never edits a submitted invoice. Every write command's precondition is a status the
// database also checks, and a correction opens a new invoice that supersedes the old one — so
// what the provider billed in March still says what it said.
//
// It never touches the plaintext of a VKN. The provider's tax identity reaches an invoice as
// the blind index the directory already computed, and there is no path in this package by
// which the number itself could be read, logged or rendered.
//
// And it never moves a claim by hand. `INVOICED` and the way back are the claim module's
// transitions, reached through a port, inside this package's transaction.
package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	healthapp "github.com/celikbros/kapsora/internal/health/application"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Permissions guarding invoice work. Both are in the catalogue from migration 000008; this
// package adds none. They live here rather than in the transport because who may raise an
// invoice is a business rule, not a routing detail.
const (
	PermissionRead   = "invoice.read"
	PermissionManage = "invoice.manage"
)

// PermissionClinicalRead is WP-I5-01's, named again here only so this package does not have
// to import the health package to read a permission string.
const PermissionClinicalRead = healthapp.PermissionClinicalRead

// ScopeOrganization is the iam.access_grant.scope_type of provider-side roles (v1.2 6.3).
const ScopeOrganization = "ORGANIZATION"

// Projection is WP-I5-01's, re-exported so a caller of this package does not need to know
// which module owns the clinical visibility rules — only that there is one.
type Projection = healthapp.Projection

// The two projections, under this package's names.
const (
	ProjectionClinical  = healthapp.ProjectionClinical
	ProjectionFinancial = healthapp.ProjectionFinancial
)

// Errors mapped by the transport layer to problem codes.
var (
	ErrInvoiceNotFound = errors.New("billing: invoice not found")
	// ErrInvoiceFrozen is the freeze the whole package exists for: an invoice that has left
	// DRAFT is not editable, and a correction is a new invoice in a chain.
	ErrInvoiceFrozen = errors.New("billing: the invoice has left DRAFT and is frozen")
	// ErrTransitionInvalid is a command run from a status it cannot run from.
	ErrTransitionInvalid = errors.New("billing: the invoice is not in a state this command can run from")
	// ErrNumberTaken is the uniqueness rule of 11.12: one number per provider per fiscal
	// year, counting only the invoices that are still live.
	ErrNumberTaken = errors.New("billing: the invoice number is already used in this fiscal year")
	// ErrProviderTaxIDMissing refuses an invoice against a provider organization that carries
	// no VKN. The answer is a boolean: the number is envelope encrypted and its blind index
	// is a hash, and neither belongs in a response.
	ErrProviderTaxIDMissing = errors.New("billing: the provider organization has no tax identity")
	ErrProviderUnknown      = errors.New("billing: the organization is not a provider of this tenant")
	ErrProviderScope        = errors.New("billing: the caller is not scoped to this provider")
	// ErrDocumentUnusable is an image that is not a clean document object of this tenant.
	ErrDocumentUnusable = errors.New("billing: the invoice image is not a usable document")
	// ErrImageRequired is the tenant's `billing.invoice_requires_image` refusing a submit.
	ErrImageRequired = errors.New("billing: the tenant requires an invoice image")
	// ErrNothingAllocated refuses a submit of an invoice that covers no claim at all. An
	// invoice allocating nothing is a number with a total on it and no answer to "for what".
	ErrNothingAllocated = errors.New("billing: the invoice allocates to no claim")
	// ErrNotSupersedable is a correction naming an invoice that is not in a state a
	// correction may replace.
	ErrNotSupersedable = errors.New("billing: the invoice cannot be superseded")
	ErrVersionMismatch = errors.New("billing: row version does not match If-Match")
)

// AllocationError is one refused allocation, naming the claim rather than the row number: a
// provider looking at their own earnings screen knows their claims by reference, and "row 3"
// is a thing only the request body knows about.
type AllocationError struct {
	// Kind is which rule refused it.
	Kind AllocationErrorKind
	// ClaimID and ClaimReference name the claim. The reference is empty when the claim is not
	// this tenant's at all, which is the one case there is nothing to name.
	ClaimID        uuid.UUID
	ClaimReference string
	// ClaimStatus is what the claim actually is, for CLAIM_NOT_INVOICEABLE.
	ClaimStatus string
	// Allocated and Approved are the two figures of an exceeded ceiling, exact decimals.
	Allocated string
	Approved  string
	// CurrencyCode and InvoiceCurrency are the two of a currency mismatch.
	CurrencyCode    string
	InvoiceCurrency string
	// LiveInvoiceID is the invoice a claim is already sitting on.
	LiveInvoiceID uuid.UUID
}

// AllocationErrorKind is which of the allocation rules refused a row.
type AllocationErrorKind string

// The four ways an allocation is refused.
const (
	// AllocationExceedsApproved is the ceiling: a provider may bill less of an approved claim
	// than was approved, and never more.
	AllocationExceedsApproved AllocationErrorKind = "ALLOCATION_EXCEEDS_APPROVED"
	// AllocationClaimNotInvoiceable is a claim nobody approved, or one of another provider.
	AllocationClaimNotInvoiceable AllocationErrorKind = "CLAIM_NOT_INVOICEABLE"
	// AllocationClaimAlreadyInvoiced is the one-claim-one-live-invoice rule.
	AllocationClaimAlreadyInvoiced AllocationErrorKind = "CLAIM_ALREADY_INVOICED"
	// AllocationCurrency is an allocation denominated differently from the invoice.
	AllocationCurrency AllocationErrorKind = "ALLOCATION_CURRENCY"
)

func (e *AllocationError) Error() string {
	return "billing: allocation refused: " + string(e.Kind)
}

// MismatchError is the submit gate refusing an invoice whose allocations do not add up.
//
// It carries both figures and the difference rather than a message, because "the totals do
// not match" is not something a provider can act on and "you are 12.50 short of 1,340.00" is.
type MismatchError struct {
	PayableAmount   string
	AllocationTotal string
	// Difference is payable minus allocated, signed: negative means the invoice allocates
	// more than it bills.
	Difference string
	Tolerance  string
}

func (e *MismatchError) Error() string { return "billing: allocations do not match the invoice" }

// Scope is the caller's provider boundary. A nil slice means "no restriction"; an empty
// non-nil slice restricts the caller to nothing, which is the safe reading of a grant that
// names no organization.
type Scope struct {
	OrganizationIDs []uuid.UUID
}

// Restricted reports whether the caller is bound to a set of organizations.
func (s Scope) Restricted() bool { return s.OrganizationIDs != nil }

// scopeOf reads the caller's organization grants. A tenant-wide actor has none.
func scopeOf(rc identity.RequestContext) Scope {
	var ids []uuid.UUID
	for _, s := range rc.Scopes {
		if s.Type != ScopeOrganization {
			continue
		}
		if ids == nil {
			ids = []uuid.UUID{}
		}
		if s.ID.Valid {
			ids = append(ids, s.ID.UUID)
		}
	}
	return Scope{OrganizationIDs: ids}
}

// InvoiceRecord is one invoice header as the repository reads it. Every money field is exact
// decimal text and nothing here is ever a float.
type InvoiceRecord struct {
	ID                     uuid.UUID
	ProviderOrganizationID uuid.UUID
	ProviderName           string
	PayerOrganizationID    *uuid.UUID
	Source                 string
	EDocumentID            *uuid.UUID
	InvoiceNumber          string
	InvoiceDate            time.Time
	FiscalYear             int
	CurrencyCode           string
	LineExtensionAmount    string
	TaxAmount              string
	PayableAmount          string
	VatRate                *string
	DomainCode             string
	Status                 string
	SupersedesInvoiceID    *uuid.UUID
	SupersededByInvoiceID  *uuid.UUID
	SubmittedAt            *time.Time
	DocumentID             *uuid.UUID
	BatchID                *uuid.UUID
	Notes                  *string
	CreatedAt              time.Time
	RowVersion             int64
}

// AllocationRecord is one claim link with the claim's own figures beside it.
type AllocationRecord struct {
	ClaimID         uuid.UUID
	ClaimReference  string
	ClaimVersionNo  int
	AllocatedAmount string
	CurrencyCode    string
	// ApprovedTotal is what the payer approved for the claim, recomputed by the same SQL
	// function the deferred ceiling trigger applies.
	ApprovedTotal string
	ClaimStatus   string
	// ClaimStatusBefore is what the claim was when it was allocated, so releasing it puts it
	// back where it came from rather than where a recomputation guessed.
	ClaimStatusBefore string
	Active            bool
	// ClaimDescription is the provider's own words on the claim's lines. It is possibly
	// clinical and the financial projection clears it.
	ClaimDescription *string
}

// InvoiceSummaryRecord is one row of a list: the header plus what it allocates in total.
type InvoiceSummaryRecord struct {
	InvoiceRecord
	AllocationTotal string
	AllocationCount int
}

// InvoiceView is one invoice with its allocations, already projected. There is no way to
// build one except through the service, and nothing downstream re-reads the unprojected row.
type InvoiceView struct {
	Projection  Projection
	Invoice     InvoiceRecord
	Allocations []AllocationRecord
	// AllocationTotal is summed here, in Go, in exact decimals, from the active rows — the
	// same sum the submit gate compares against the payable amount.
	AllocationTotal string
	// AllocationDifference is payable minus allocated, signed. Zero on an invoice that adds
	// up; a draft screen shows it so the gap is visible before the submit is refused.
	AllocationDifference string
}

// InvoicePage is one page of invoices.
type InvoicePage struct {
	Items      []InvoiceSummaryRecord
	NextCursor string
}

// NewInvoiceRow is the header as it is written.
type NewInvoiceRow struct {
	ProviderOrganizationID uuid.UUID
	PayerOrganizationID    *uuid.UUID
	Source                 string
	InvoiceNumber          string
	InvoiceDate            time.Time
	FiscalYear             int
	// ProviderTaxIDHash is the VKN's blind index, copied from the directory. It is never the
	// number, and no code path in this package can turn it back into one.
	ProviderTaxIDHash   []byte
	CurrencyCode        string
	LineExtensionAmount string
	TaxAmount           string
	PayableAmount       string
	VatRate             *string
	DomainCode          string
	SupersedesInvoiceID *uuid.UUID
	DocumentID          *uuid.UUID
	Notes               *string
	ActorID             *uuid.UUID
}

// UpdateInvoiceRow is the header of a draft as it is rewritten. Every column the command owns
// is written, so clearing an image or a note is expressible and a stale value can never
// survive an edit.
type UpdateInvoiceRow struct {
	PayerOrganizationID *uuid.UUID
	InvoiceNumber       string
	InvoiceDate         time.Time
	FiscalYear          int
	CurrencyCode        string
	LineExtensionAmount string
	TaxAmount           string
	PayableAmount       string
	VatRate             *string
	DomainCode          string
	DocumentID          *uuid.UUID
	Notes               *string
	ActorID             *uuid.UUID
}

// StatusMove is one transition, with its whole precondition.
type StatusMove struct {
	Status       string
	FromStatuses []string
	SubmittedAt  *time.Time
	ActorID      *uuid.UUID
}

// NewAllocationRow is one claim link as it is written.
type NewAllocationRow struct {
	InvoiceID         uuid.UUID
	ClaimID           uuid.UUID
	ClaimVersionNo    int
	AllocatedAmount   string
	CurrencyCode      string
	ClaimStatusBefore string
	ActorID           *uuid.UUID
}

// InvoiceQuery is the list filter, already validated.
type InvoiceQuery struct {
	Scope                  Scope
	ProviderOrganizationID *uuid.UUID
	Status                 *string
	FiscalYear             *int
	DateFrom               *time.Time
	DateTo                 *time.Time
	BatchID                *uuid.UUID
	After                  *httpx.Cursor
	PageSize               int
}

// TaxIdentity is what the directory answers about a provider: its display name, whether it is
// a provider of this tenant at all, and its VKN blind index.
type TaxIdentity struct {
	Found            bool
	IsProvider       bool
	DisplayName      string
	TaxNumberHash    []byte
	RelationshipRole string
	Status           string
}

// NumberQuery asks who is holding an invoice number. `ExcludingID` is the invoice being
// edited, so a patch that leaves the number alone does not find itself.
type NumberQuery struct {
	ProviderOrganizationID uuid.UUID
	FiscalYear             int
	InvoiceNumber          string
	ExcludingID            uuid.UUID
}

// NumberHolder is the answer: the invoice holding the number, and what state it is in.
type NumberHolder struct {
	Found  bool
	ID     uuid.UUID
	Status string
}

// ClaimCandidate is one claim an allocation may name, as the repository reads it.
type ClaimCandidate struct {
	ClaimID                uuid.UUID
	Reference              string
	Status                 string
	CurrentVersionNo       int
	ProviderOrganizationID uuid.UUID
	CurrencyCode           string
	ApprovedTotal          string
	// LiveInvoiceID is the invoice this claim already sits on, or uuid.Nil. It is answered
	// rather than filtered out, so a refusal can name the document instead of saying that a
	// claim the caller can see in their own earnings view does not exist.
	LiveInvoiceID uuid.UUID
}

// Repository is the persistence port.
type Repository interface {
	CreateInvoice(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewInvoiceRow) (InvoiceRecord, error)
	GetInvoice(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (InvoiceRecord, error)
	LockInvoice(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (InvoiceRecord, error)
	ListInvoices(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q InvoiceQuery) ([]InvoiceSummaryRecord, error)
	ListChain(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) ([]InvoiceSummaryRecord, error)
	UpdateInvoiceDraft(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in UpdateInvoiceRow, expected int64) (bool, error)
	SetStatus(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, move StatusMove, expected int64) (bool, error)
	// SetSupersededBy cancels the old invoice and points it at its successor, in one
	// statement, at the moment the successor is submitted.
	SetSupersededBy(ctx context.Context, tx pgx.Tx, tenantID, id, successor uuid.UUID, actorID *uuid.UUID) (bool, error)
	ListAllocations(ctx context.Context, tx pgx.Tx, tenantID, invoiceID uuid.UUID) ([]AllocationRecord, error)
	DeleteAllocations(ctx context.Context, tx pgx.Tx, tenantID, invoiceID uuid.UUID) error
	CreateAllocation(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewAllocationRow) error
	// FindByNumber answers which invoice, if any, is already holding this number in this
	// provider's fiscal year. It is the half the unique index cannot answer: a returned
	// invoice's number may be reused, but only by the invoice that supersedes it.
	FindByNumber(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q NumberQuery) (NumberHolder, error)
	ProviderTaxIdentity(ctx context.Context, tx pgx.Tx, tenantID, providerID uuid.UUID) (TaxIdentity, error)
	InvoiceableClaims(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, ids []uuid.UUID) ([]ClaimCandidate, error)
	DocumentIsClean(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) (bool, error)
	LinkDocument(ctx context.Context, tx pgx.Tx, tenantID, invoiceID, documentID uuid.UUID, actorID *uuid.UUID) error
}

// ClaimsPort is the claim module's own transitions, narrowed to the two an invoice causes.
// It runs inside this package's transaction, so a claim that moved to INVOICED and an invoice
// that failed to submit is a state no reader ever observes.
//
// It is a port rather than a query because the transition is a business rule the claim module
// owns: this package decides *when* a claim goes onto an invoice, and the claim module decides
// *whether* it may.
type ClaimsPort interface {
	// MarkInvoiced moves approved claims onto an invoice.
	MarkInvoiced(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, actorID *uuid.UUID,
		claims []uuid.UUID) error
	// ReleaseFromInvoice puts each claim back to the status it had when it was allocated.
	ReleaseFromInvoice(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, actorID *uuid.UUID,
		releases []ClaimRelease) error
	// Cut writes one WP-I7-01 CUT adjustment per claim, for a payer's cut on an invoice. The
	// shares are computed here, exactly; whether a claim may carry one is the claim module's
	// answer.
	Cut(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, actorID *uuid.UUID,
		cuts []ClaimCut) ([]ClaimAdjustment, error)
	// Reverse takes back the adjustments a changed decision wrote. The amount is read off the
	// row being reversed rather than supplied, so a decision changed twice cannot give the
	// money back twice.
	Reverse(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, actorID *uuid.UUID,
		reversals []ClaimReversal) ([]ClaimAdjustment, error)
	// CloseUnpaid finishes the claims of an invoice the payer rejected.
	CloseUnpaid(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, actorID *uuid.UUID,
		claims []uuid.UUID) error
	// ReopenUnpaid puts them back on the invoice when the rejection is withdrawn.
	ReopenUnpaid(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, actorID *uuid.UUID,
		claims []uuid.UUID) error
	// CutReasons is the closed list a cut adjustment's reason has to be in. It is asked for
	// rather than copied, so this package cannot end up offering a reviewer a code the claim
	// ledger would refuse.
	CutReasons() []string
}

// ClaimRelease is one claim going back where it came from.
type ClaimRelease struct {
	ClaimID uuid.UUID
	// Status is what the claim was when it was allocated — APPROVED or PARTIALLY_APPROVED.
	Status string
}

// NoClaims is the default ClaimsPort. It refuses rather than pretending: a process wired with
// no claim service must not be able to submit an invoice, because an invoice submitted
// against claims that never moved to INVOICED is an invoice whose claims can be billed again.
type NoClaims struct{}

// MarkInvoiced implements ClaimsPort.
func (NoClaims) MarkInvoiced(context.Context, pgx.Tx, uuid.UUID, *uuid.UUID, []uuid.UUID) error {
	return errors.New("billing: no claim service is wired; an invoice cannot be submitted")
}

// ReleaseFromInvoice implements ClaimsPort.
func (NoClaims) ReleaseFromInvoice(context.Context, pgx.Tx, uuid.UUID, *uuid.UUID, []ClaimRelease) error {
	return errors.New("billing: no claim service is wired; an invoice cannot be withdrawn")
}

// Cut implements ClaimsPort.
func (NoClaims) Cut(context.Context, pgx.Tx, uuid.UUID, *uuid.UUID, []ClaimCut) ([]ClaimAdjustment, error) {
	return nil, errors.New("billing: no claim service is wired; an invoice cannot be cut")
}

// Reverse implements ClaimsPort.
func (NoClaims) Reverse(context.Context, pgx.Tx, uuid.UUID, *uuid.UUID, []ClaimReversal) ([]ClaimAdjustment, error) {
	return nil, errors.New("billing: no claim service is wired; a cut cannot be taken back")
}

// CloseUnpaid implements ClaimsPort.
func (NoClaims) CloseUnpaid(context.Context, pgx.Tx, uuid.UUID, *uuid.UUID, []uuid.UUID) error {
	return errors.New("billing: no claim service is wired; an invoice cannot be rejected")
}

// ReopenUnpaid implements ClaimsPort.
func (NoClaims) ReopenUnpaid(context.Context, pgx.Tx, uuid.UUID, *uuid.UUID, []uuid.UUID) error {
	return errors.New("billing: no claim service is wired; a rejection cannot be withdrawn")
}

// CutReasons implements ClaimsPort. A process with no claim service offers no reason at all,
// which is what refuses every cut before it is written.
func (NoClaims) CutReasons() []string { return nil }
