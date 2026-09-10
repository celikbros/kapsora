// Package application implements the reporting use cases: the provider statement, the daily
// reconciliation, the operations dashboard, and the exports a person is allowed to take out of
// the system. Transactions are opened here with db.WithTenantTx, so a write and its audit row
// commit together and RLS is bound for every statement.
//
// Four things this package never does.
//
// **It never adds up money in Go.** Every figure it hands back was summed by PostgreSQL over
// exact decimals in one query. There is no loop in this package that accumulates an amount, and
// there is no float anywhere near one: an amount is a canonical decimal string from the moment
// it leaves the database until the moment it is written into JSON or a CSV cell.
//
// **It never updates a reconciliation run.** The run is append-only in the schema, so there is
// no repository method that would accept one — a correction is a second run of the same period,
// and both stay.
//
// **It never renders a file on an API request.** `createExport` queues; the worker renders. A
// download hands back a short-lived URL into the document store, exactly as WP-I4-04's own
// download does, and the API carries no bytes.
//
// **It never lets an export leave without a stamp and an audit row.** The watermark is on every
// row of every file, and every download writes an `audit.access_event`. Both are in one place
// each, which is what makes them true of every export rather than of the ones somebody
// remembered.
package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/objectstore"
)

// Permissions guarding this package. `report.read` is in the catalogue from migration 000008 and
// is what every figure here is read under; `report.export` and `report.export.sensitive` are
// added by migration 000047. They live here rather than in the transport because who may take a
// list of claim line descriptions out of the building is a business rule, not a routing detail.
const (
	PermissionRead            = "report.read"
	PermissionExport          = "report.export"
	PermissionExportSensitive = "report.export.sensitive"
)

// ScopeOrganization is the iam.access_grant.scope_type of provider-side roles (v1.2 6.3). It is
// this package's provider boundary: an actor granted an organization reads that organization's
// statement and its own reconciliation runs, and no other provider's.
const ScopeOrganization = "ORGANIZATION"

// ExportRequestedEvent asks the worker to render a queued export. The payload carries nothing but
// ids: the worker loads the export row itself, and an event that carried the filters would be an
// event that could disagree with the row.
const ExportRequestedEvent = "report.export.requested"

// Errors mapped by the transport layer to problem codes.
var (
	ErrExportNotFound = errors.New("report: export not found")
	ErrRunNotFound    = errors.New("report: reconciliation run not found")
	// ErrProviderScope is a caller asking for a provider it is not granted. It is 403 rather
	// than 404 because the caller named an organization on purpose and is entitled to know
	// that its own grants do not reach it.
	ErrProviderScope = errors.New("report: the caller may not read that provider")
	// ErrExportNotReady refuses a download of an export the worker has not finished. QUEUED and
	// RUNNING are a wait; FAILED is not, and the caller is told which.
	ErrExportNotReady = errors.New("report: the export is not ready")
	ErrExportFailed   = errors.New("report: the export could not be produced")
	// ErrExportExpired refuses a download after the TTL. The file may still exist for a few
	// hours until the nightly sweep removes it; the refusal does not depend on that, because
	// "may I have this" and "is it still on disk" are different questions.
	ErrExportExpired = errors.New("report: the export has expired")
	// ErrExportSensitive is the CLAIMS kind without the sensitive grant.
	ErrExportSensitive = errors.New("report: the export kind needs report.export.sensitive")
	// ErrTooManyRows is a request that would render a file larger than this platform produces.
	ErrTooManyRows = errors.New("report: the export would carry too many rows")
	// ErrDocumentUnavailable is an export whose document is gone: retention purged it, or a
	// legal hold sweep removed the bytes. The row survives its file, so this is answerable.
	ErrDocumentUnavailable = errors.New("report: the export file is no longer available")
)

// Scope is the caller's provider boundary. A nil slice means "no restriction"; an empty non-nil
// slice restricts the caller to nothing, which is the safe reading of a grant that names no
// organization.
type Scope struct {
	OrganizationIDs []uuid.UUID
}

// Restricted reports whether the caller is bound to a set of organizations.
func (s Scope) Restricted() bool { return s.OrganizationIDs != nil }

// Allows reports whether an organization is inside the boundary.
func (s Scope) Allows(id uuid.UUID) bool {
	if !s.Restricted() {
		return true
	}
	for _, own := range s.OrganizationIDs {
		if own == id {
			return true
		}
	}
	return false
}

// IDs renders the boundary the way the queries take it: an empty array means no restriction, so a
// caller that is restricted to nothing is given one impossible id rather than an empty array that
// the SQL would read as "everything". A grant that names no organization reaches no organization.
func (s Scope) IDs() []uuid.UUID {
	if !s.Restricted() {
		return []uuid.UUID{}
	}
	if len(s.OrganizationIDs) == 0 {
		return []uuid.UUID{uuid.Nil}
	}
	return s.OrganizationIDs
}

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

// ---------------------------------------------------------------------------
// The provider statement
// ---------------------------------------------------------------------------

// StatementQuery is one provider's statement for one period in one currency.
type StatementQuery struct {
	ProviderOrganizationID uuid.UUID
	PeriodFrom             time.Time
	PeriodTo               time.Time
	CurrencyCode           string
	RowLimit               int
}

// StatementTotals is every figure of the statement, and every one of them is a sum PostgreSQL
// computed in one query. They are strings because they are exact decimals: a money value that
// went through a float on its way here would be a money value nobody could reconcile.
type StatementTotals struct {
	ProviderName    string
	InvoicedTotal   string
	ApprovedTotal   string
	CutTotal        string
	ReturnedTotal   string
	RejectedTotal   string
	SettledTotal    string
	PaidTotal       string
	OpenBalance     string
	InvoiceCount    int64
	SettlementCount int64
}

// StatementInvoice is one invoice line of the statement.
type StatementInvoice struct {
	ID                   uuid.UUID
	InvoiceNumber        string
	InvoiceDate          time.Time
	Status               string
	CurrencyCode         string
	PayableAmount        string
	TaxAmount            string
	BatchID              *uuid.UUID
	BatchReference       *string
	BatchStatus          *string
	BatchDecision        *string
	ApprovedAmount       string
	SettlementID         *uuid.UUID
	SettlementReference  *string
	DueDate              *time.Time
	SettlementPaidAmount string
}

// StatementSettlement is one settlement line of the statement.
type StatementSettlement struct {
	ID             uuid.UUID
	Reference      string
	BatchID        uuid.UUID
	BatchReference string
	DueDate        time.Time
	Status         string
	CurrencyCode   string
	ApprovedAmount string
	WithheldAmount string
	PayableAmount  string
	PaidAmount     string
	OpenAmount     string
	PaymentCount   int64
	LastPaidAt     *time.Time
}

// Statement is the whole answer: the totals, and the rows they were computed from.
type Statement struct {
	ProviderOrganizationID uuid.UUID
	PeriodFrom             time.Time
	PeriodTo               time.Time
	CurrencyCode           string
	Totals                 StatementTotals
	Invoices               []StatementInvoice
	Settlements            []StatementSettlement
}

// ---------------------------------------------------------------------------
// Reconciliation
// ---------------------------------------------------------------------------

// RunQuery is the scope of one run's arithmetic: a tenant, a currency, a period, and either one
// provider or all of them.
type RunQuery struct {
	ProviderOrganizationID *uuid.UUID
	CurrencyCode           string
	PeriodFrom             time.Time
	PeriodTo               time.Time
}

// ReconciliationTotals is what one run compared, summed by PostgreSQL in one query.
type ReconciliationTotals struct {
	InvoicedTotal string
	ApprovedTotal string
	CutTotal      string
	ReturnedTotal string
	RejectedTotal string
	SettledTotal  string
	PaidTotal     string
	// OpenTotal is settled minus paid, subtracted by PostgreSQL. It is carried rather than
	// worked out here because it is an exact decimal and the database is about to check it
	// against the other two.
	OpenTotal       string
	SettlementCount int64
}

// ReconciliationDifference is one settlement the run disagreed with.
type ReconciliationDifference struct {
	SettlementID        uuid.UUID
	SettlementReference string `json:"reference"`
	DueDate             time.Time
	Status              string
	ExpectedAmount      string
	ActualAmount        string
	DifferenceAmount    string
	Kind                string
}

// NewRun is the insert payload of one run. Every amount is an exact decimal string, and the
// database checks the arithmetic: `OpenTotal` has to be settled minus paid, and `Difference` has
// to be settled minus the ERP's figure or the paid one.
type NewRun struct {
	Scope                  string
	ProviderOrganizationID *uuid.UUID
	PeriodFrom             time.Time
	PeriodTo               time.Time
	RunNo                  int
	CurrencyCode           string
	Totals                 ReconciliationTotals
	ERPTotal               *string
	Difference             string
	Differences            []ReconciliationDifference
	Status                 string
	FailureCode            *string
	RanAt                  time.Time
	ActorID                *uuid.UUID
}

// ReconciliationRun is one billing.reconciliation_run row as this module reads it.
type ReconciliationRun struct {
	ID                     uuid.UUID
	Scope                  string
	ProviderOrganizationID *uuid.UUID
	ProviderName           *string
	PeriodFrom             time.Time
	PeriodTo               time.Time
	RunNo                  int
	CurrencyCode           string
	InvoicedTotal          string
	ApprovedTotal          string
	CutTotal               string
	ReturnedTotal          string
	RejectedTotal          string
	SettledTotal           string
	PaidTotal              string
	OpenTotal              string
	// ERPTotal is empty until M9. It is a string rather than a pointer for the same reason
	// every other amount here is one: absent and zero are told apart by the empty string, and
	// no amount is ever the empty string.
	ERPTotal        string
	Difference      string
	DifferenceCount int
	Differences     []ReconciliationDifference
	Status          string
	FailureCode     *string
	RanAt           time.Time
	CreatedAt       time.Time
}

// RunFilter is the API-level list request for runs.
type RunFilter struct {
	Cursor                 string
	Limit                  int
	Scope                  string
	Status                 string
	ProviderOrganizationID *uuid.UUID
	PeriodFrom             *time.Time
	PeriodTo               *time.Time
}

// RunQueryOptions is the repository-level list request.
type RunQueryOptions struct {
	Scope                  Scope
	RunScope               string
	Status                 string
	ProviderOrganizationID *uuid.UUID
	PeriodFrom             *time.Time
	PeriodTo               *time.Time
	After                  *httpx.Cursor
	PageSize               int
}

// RunPage is one keyset page of runs.
type RunPage struct {
	Items      []ReconciliationRun
	NextCursor string
}

// ReconcileReport is what one sweep did. `Runs` and `Differing` are separate because "nothing
// was reconciled" and "everything differed" are different answers an operator has to be able to
// tell apart.
type ReconcileReport struct {
	Tenants    int
	Runs       int
	Differing  int
	Reconciled int64
	WorkItems  int
}

// ---------------------------------------------------------------------------
// The dashboard
// ---------------------------------------------------------------------------

// StatusFigure is one row of the claims-by-status figure.
type StatusFigure struct {
	Status        string
	ClaimCount    int64
	ApprovedTotal string
}

// AgingFigure is one aging bucket.
type AgingFigure struct {
	Bucket        string
	ClaimCount    int64
	ApprovedTotal string
}

// BatchFigure is the icmals awaiting a decision.
type BatchFigure struct {
	BatchCount        int64
	SubmittedTotal    string
	OldestSubmittedAt *time.Time
	OldestSLADueAt    *time.Time
}

// SettlementFigure is what is due this week and what is already overdue.
type SettlementFigure struct {
	DueSoonCount int64
	DueSoonTotal string
	OverdueCount int64
	OverdueTotal string
}

// ReimbursementFigure is the members waiting for an answer.
type ReimbursementFigure struct {
	ReimbursementCount int64
	RequestedTotal     string
	OldestSubmittedAt  *time.Time
}

// WorkItemFigure is the work past its SLA.
type WorkItemFigure struct {
	ItemCount   int64
	OldestDueAt *time.Time
}

// Dashboard is every figure of the operations screen, each one a query of its own.
type Dashboard struct {
	AsOf             time.Time
	ClaimsByStatus   []StatusFigure
	ClaimAging       []AgingFigure
	Batches          BatchFigure
	Settlements      SettlementFigure
	Reimbursements   ReimbursementFigure
	WorkItemsPastSLA WorkItemFigure
}

// ---------------------------------------------------------------------------
// Exports
// ---------------------------------------------------------------------------

// NewExportInput is what a caller asks for. The scope is typed and the filters are not: which
// provider, which period and which currency are fields, and `Parameters` is the remainder — which
// may not carry an identifier, here or in the database.
type NewExportInput struct {
	Kind                   string
	Format                 string
	ProviderOrganizationID *uuid.UUID
	PeriodFrom             *time.Time
	PeriodTo               *time.Time
	CurrencyCode           string
	Parameters             map[string]any
}

// NewExportRow is the insert payload of a queued export. The id is chosen by the caller because
// the watermark carries it: a stamp naming an id the row does not have would be a stamp nothing
// can be traced back to.
type NewExportRow struct {
	ID                     uuid.UUID
	Kind                   string
	Format                 string
	Parameters             []byte
	ProviderOrganizationID *uuid.UUID
	PeriodFrom             *time.Time
	PeriodTo               *time.Time
	CurrencyCode           *string
	RequestedBy            uuid.UUID
	RequestedAt            time.Time
	ExpiresAt              time.Time
	Watermark              string
}

// Export is one report.export row.
type Export struct {
	ID                     uuid.UUID
	Kind                   string
	Format                 string
	Status                 string
	Parameters             map[string]any
	ProviderOrganizationID *uuid.UUID
	PeriodFrom             *time.Time
	PeriodTo               *time.Time
	CurrencyCode           string
	DocumentID             *uuid.UUID
	RowCount               int
	RequestedBy            uuid.UUID
	RequestedAt            time.Time
	ExpiresAt              time.Time
	Watermark              string
	DownloadCount          int
	FailureCode            *string
	CreatedAt              time.Time
	RowVersion             int64
}

// Expired reports whether this export may still be downloaded at the given moment. It is the one
// place that answer is computed, so a screen that offers the button and a server that refuses it
// cannot disagree.
func (e Export) Expired(at time.Time) bool { return !at.Before(e.ExpiresAt) }

// ExportFilter is the API-level export list request.
type ExportFilter struct {
	Cursor string
	Limit  int
	Kind   string
	Status string
	// Mine narrows the list to the caller's own exports. It is the default in the transport:
	// an export is a file with somebody's name stamped on every row of it.
	Mine bool
}

// ExportQueryOptions is the repository-level export list request.
type ExportQueryOptions struct {
	RequestedBy *uuid.UUID
	Kind        string
	Status      string
	After       *httpx.Cursor
	PageSize    int
}

// ExportPage is one keyset page of exports.
type ExportPage struct {
	Items      []Export
	NextCursor string
}

// ExportTable is what the worker renders: the column names and the rows, all as text, in the
// order they go into the file. It carries no watermark column — that is added by the renderer,
// once, for every row, so that a query somebody adds later cannot forget it.
type ExportTable struct {
	Columns []string
	Rows    [][]string
}

// ExportRowQuery is the scope the worker reproduces from the export row.
type ExportRowQuery struct {
	Kind                   string
	ProviderOrganizationID *uuid.UUID
	PeriodFrom             *time.Time
	PeriodTo               *time.Time
	CurrencyCode           string
	Scope                  Scope
	RowLimit               int
}

// ExpiringExport is one row of the nightly sweep's worklist.
type ExpiringExport struct {
	ID          uuid.UUID
	Kind        string
	DocumentID  *uuid.UUID
	RequestedBy uuid.UUID
	ExpiresAt   time.Time
}

// ExpiryReport is what one expiry sweep did.
type ExpiryReport struct {
	Tenants int
	Expired int
	// Held counts exports whose file a legal hold covers. The row is still marked EXPIRED —
	// it stopped being downloadable when it said it would — and the bytes stay, which is what
	// a legal hold means.
	Held int
}

// RenderedFile is one finished export on its way into the document store.
type RenderedFile struct {
	Filename       string
	ContentType    string
	Classification string
	Body           []byte
	// OwnerOrganizationID is the provider an export belongs to when it has one. A tenant-wide
	// export has none, exactly like a tenant-wide document.
	OwnerOrganizationID *uuid.UUID
	// RetainFor is the export's TTL, carried into the document link so a retention sweep and
	// this module's own expiry agree about how long the file lives.
	RetainFor time.Duration
	ExportID  uuid.UUID
	Kind      string
}

// ---------------------------------------------------------------------------
// Ports
// ---------------------------------------------------------------------------

// Repository is the persistence port; every method runs inside the caller's transaction, which
// db.WithTenantTx has already bound to the tenant so RLS is active. The provider boundary is a
// repository concern too: the scope is passed down rather than checked above, so a row outside it
// is genuinely not there rather than fetched and then hidden.
type Repository interface {
	StatementTotals(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q StatementQuery) (StatementTotals, error)
	StatementInvoices(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q StatementQuery) ([]StatementInvoice, error)
	StatementSettlements(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q StatementQuery) ([]StatementSettlement, error)

	ReconciliationCurrencies(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, from, to time.Time) ([]string, error)
	ReconciliationProviders(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, currency string, from, to time.Time) ([]uuid.UUID, error)
	ReconciliationTotals(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q RunQuery) (ReconciliationTotals, error)
	ReconciliationDifferences(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q RunQuery, asOf time.Time, limit int) ([]ReconciliationDifference, error)
	NextRunNo(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope string, provider *uuid.UUID, from, to time.Time, currency string) (int, error)
	// CreateRun is the only write. There is deliberately no UpdateRun and no DeleteRun: the
	// table is append-only in the schema, and a method that existed here would be a method
	// somebody eventually called.
	CreateRun(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewRun) (ReconciliationRun, error)
	GetRun(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (ReconciliationRun, error)
	ListRuns(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q RunQueryOptions) ([]ReconciliationRun, error)
	// MarkSettlementsReconciled is the run's own act on somebody else's table. Its predicate
	// names PAID and equality, and `ck_billing_settlement_reconciled` is underneath it, so it
	// cannot mark a settlement that is a kuruş short however it is called.
	MarkSettlementsReconciled(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q RunQuery, actorID *uuid.UUID) (int64, error)

	ClaimsByStatus(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope Scope) ([]StatusFigure, error)
	ClaimAging(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope Scope, asOf time.Time) ([]AgingFigure, error)
	BatchesAwaitingReview(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope Scope, queueCode string) (BatchFigure, error)
	SettlementsDue(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, scope Scope, asOf time.Time) (SettlementFigure, error)
	ReimbursementsAwaitingDecision(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (ReimbursementFigure, error)
	WorkItemsPastSLA(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, asOf time.Time) (WorkItemFigure, error)

	CreateExport(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewExportRow) (Export, error)
	GetExport(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, requestedBy *uuid.UUID) (Export, error)
	LockExport(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (Export, error)
	ListExports(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q ExportQueryOptions) ([]Export, error)
	MarkExportRunning(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (bool, error)
	MarkExportReady(ctx context.Context, tx pgx.Tx, tenantID, id, documentID uuid.UUID, rowCount int) (bool, error)
	MarkExportFailed(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, failureCode string) (bool, error)
	CountExportDownload(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (int, error)
	ListExpiredExports(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, asOf time.Time, limit int) ([]ExpiringExport, error)
	MarkExportExpired(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (bool, error)
	// ExportIdentity reads the two names the watermark carries.
	ExportIdentity(ctx context.Context, tx pgx.Tx, tenantID, actorID uuid.UUID) (tenantCode, requesterName string, err error)
	// ExportRows renders one kind's rows. The switch on the kind is here rather than in the
	// service because what a kind *is* is the query behind it.
	ExportRows(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q ExportRowQuery) (ExportTable, error)

	// ActiveTenants lists the tenants the reconciliation and the expiry sweep walk.
	// platform.tenant carries no RLS, so it is read outside a tenant transaction like the
	// other cross-tenant jobs.
	ActiveTenants(ctx context.Context, tx pgx.Tx) ([]uuid.UUID, error)
}

// Documents is WP-I4-04 behind the narrow door this module needs: put a rendered file in, take a
// short-lived link out, and remove the bytes when the export stops being downloadable.
//
// It is a port rather than a direct call for the ordinary reason — the document service opens
// transactions of its own — and for one that matters here: the download it performs writes the
// document's own access event, and this module writes the export's. Two events for one act, and
// each of them answers a question the other cannot.
type Documents interface {
	// Store writes a file the platform itself produced. It is the worker's call and nobody
	// else's: an API request never holds a file body.
	Store(ctx context.Context, tenantID uuid.UUID, in RenderedFile) (uuid.UUID, error)
	// Download answers a presigned GET and writes the document's access event.
	Download(ctx context.Context, rc identity.RequestContext, documentID uuid.UUID,
		purposeCode, reasonText string) (objectstore.PresignedURL, error)
	// Purge removes the bytes of an expired export, unless a legal hold covers them. It reports
	// false when a hold kept the file, which the sweep counts rather than swallows.
	Purge(ctx context.Context, tenantID, documentID uuid.UUID) (bool, error)
}

// RaiseWorkItem is one work item this module asks WP-I4-03 for.
type RaiseWorkItem struct {
	QueueCode     string
	AggregateType string
	AggregateID   uuid.UUID
	Title         string
	ActorID       *uuid.UUID
}

// WorkItems is the work queue port, written from this side of the boundary rather than as a call
// into the worklist service, because that service opens a transaction of its own: a work item
// that committed while the run it belongs to rolled back would be work nobody can explain.
type WorkItems interface {
	Raise(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in RaiseWorkItem) error
}

// noWorkItems is the default for a process that raises none. It refuses rather than doing
// nothing quietly: a reconciliation that found differences and raised no work item would be a
// difference nobody ever sees.
type noWorkItems struct{}

func (noWorkItems) Raise(context.Context, pgx.Tx, uuid.UUID, RaiseWorkItem) error {
	return errors.New("report: this process has no work item port")
}

// noDocuments is the default for a process that stores no file. Every method refuses, so a bug
// that tried to render an export in the API would say so rather than quietly working.
type noDocuments struct{}

func (noDocuments) Store(context.Context, uuid.UUID, RenderedFile) (uuid.UUID, error) {
	return uuid.Nil, errors.New("report: this process has no document store")
}

func (noDocuments) Download(context.Context, identity.RequestContext, uuid.UUID, string, string) (objectstore.PresignedURL, error) {
	return objectstore.PresignedURL{}, errors.New("report: this process has no document store")
}

func (noDocuments) Purge(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return false, errors.New("report: this process has no document store")
}
