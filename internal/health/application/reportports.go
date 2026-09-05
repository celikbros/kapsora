package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Permissions guarding the treatment report (migration 000008). They are already in the
// catalogue and already granted — manage to PROVIDER_STAFF, review to MEDICAL_REVIEWER —
// so this package adds none.
const (
	PermissionReportManage = "health.medical_report.manage"
	PermissionReportReview = "health.medical_report.review"
)

// MedicalReviewQueueCode is the queue a submitted report waits in. A module raises work by
// the queue's code rather than by an id, because the id belongs to a row an operator
// created and the module cannot know it.
const MedicalReviewQueueCode = "MEDICAL_REVIEW"

// Errors mapped by the transport layer to problem codes.
var (
	ErrReportNotFound = errors.New("health: medical report not found")
	// ErrReportImmutable is the whole point of the package: a report that has left DRAFT
	// is what the reviewer will see or already saw, and editing it would rewrite the thing
	// a claim is leaning on. A correction is a new version.
	ErrReportImmutable = errors.New("health: a decided medical report cannot be edited")
	// ErrReportTransitionInvalid is a lifecycle command given in a state that does not
	// allow it — approving a draft, submitting a rejected report, cancelling one a
	// reviewer has already picked up.
	ErrReportTransitionInvalid = errors.New("health: the medical report cannot move that way")
	// ErrReportServiceRequired refuses a submission with no service line. A report that
	// names no service is a reviewer asked to approve nothing in particular.
	ErrReportServiceRequired = errors.New("health: the report has no service line")
	// ErrReportDocumentRequired refuses a submission with no scanned-clean report file. A
	// link to an object still in quarantine is not a document a reviewer can open.
	ErrReportDocumentRequired = errors.New("health: the report has no clean document")
	// ErrReportSupersedesInvalid is a correction naming a report that cannot be corrected:
	// one nobody has decided, one of another person, or one whose chain already carries a
	// later version.
	ErrReportSupersedesInvalid = errors.New("health: that report cannot be superseded")
	// ErrReportChainApproved is the partial unique index refusing a second approved version
	// of one chain. It is unreachable through the ordinary path — the approval supersedes
	// its predecessor in the same transaction — and exists so a race is an answer rather
	// than a constraint violation in a log.
	ErrReportChainApproved = errors.New("health: the report chain already has an approved version")
	// ErrReportServiceUnknown is a service line naming a definition that is not in this
	// tenant's catalogue.
	ErrReportServiceUnknown = errors.New("health: a service line names an unknown service definition")
	// ErrReportCaseMismatch is a report attached to a case that belongs to somebody else.
	ErrReportCaseMismatch = errors.New("health: the case belongs to another person")
	ErrReportCaseNotFound = errors.New("health: the case named by the report was not found")
)

// The reason codes ReportCoverage answers a refusal with. They are codes rather than
// sentences because WP-I5-04 puts them on a claim's explanation trace, and a trace nobody
// can count is a trace nobody can report on.
const (
	// CoverageOK is the answer when the report covers the service on the day.
	CoverageOK = "COVERED"
	// CoverageNotApproved is a report no reviewer approved, or one a later version replaced.
	CoverageNotApproved = "REPORT_NOT_APPROVED"
	// CoverageOutOfWindow is a service date outside [valid_from, valid_to].
	CoverageOutOfWindow = "REPORT_OUT_OF_WINDOW"
	// CoverageServiceNotCovered is a service the report does not name.
	CoverageServiceNotCovered = "SERVICE_NOT_IN_REPORT"
)

// ReportRecord is one health.medical_report row, as the database holds it. Every read
// returns the whole row including the clinical columns; the projection drops what the
// caller may not have, before anything maps it to the wire.
type ReportRecord struct {
	ID                 uuid.UUID
	PersonID           uuid.UUID
	CaseID             *uuid.UUID
	Reference          string
	VersionNo          int
	RootReportID       uuid.UUID
	SupersedesReportID *uuid.UUID
	// ReportType and ReportSubtype are "" and nil once the financial projection has been
	// applied: "Rapor türü: ONKOLOJI_TEDAVI" is a diagnosis anybody can read off a list.
	ReportType                    string
	ReportSubtype                 *string
	IssuingPractitionerID         *uuid.UUID
	IssuingProviderOrganizationID *uuid.UUID
	IssuedAt                      time.Time
	ValidFrom                     time.Time
	ValidTo                       time.Time
	Status                        string
	// ClinicalSummary and ReviewComment are the two free-text clinical columns. Both are
	// nil once the financial projection has been applied.
	ClinicalSummary  *string
	ReviewComment    *string
	RejectReasonCode *string
	ReviewedBy       *uuid.UUID
	ReviewedAt       *time.Time
	SubmittedAt      *time.Time
	SubmittedBy      *uuid.UUID
	CreatedAt        time.Time
	RowVersion       int64
	// CaseSensitivity is the sensitivity of the case the report hangs off, or STANDARD for
	// a report written outside one. It is what `decide` decides on and it never reaches the
	// wire: the projection clears it like any other clinical field.
	CaseSensitivity string
}

// ReportServiceRecord is one health.medical_report_service row. The amounts are exact
// decimals as strings the whole way down; nothing here is ever a float.
type ReportServiceRecord struct {
	ID                  uuid.UUID
	ReportID            uuid.UUID
	ServiceDefinitionID uuid.UUID
	ServiceCode         string
	ServiceName         string
	CoveredQuantity     *string
	CoveredAmount       *string
	CurrencyCode        *string
	// Notes is nil once the financial projection has been applied: the line a doctor writes
	// about this service for this person is clinical, whatever the service is.
	Notes      *string
	RowVersion int64
}

// ReportDocumentRecord is one document.link row of a report, with what the object store
// knows about the file behind it. The whole slice is empty in the financial projection: a
// document list naming "PSIKIYATRI_RAPORU" is a diagnosis on a filename.
type ReportDocumentRecord struct {
	ID                 uuid.UUID
	ObjectID           uuid.UUID
	DocumentTypeCode   string
	Purpose            *string
	RequiredPermission *string
	OriginalFilename   string
	ContentType        string
	ScanStatus         string
	Classification     string
	CreatedAt          time.Time
}

// ReportUsageRecord is one health.medical_report_usage row: which request, authorization or
// claim leaned on this version of the report, and when.
type ReportUsageRecord struct {
	ID         uuid.UUID
	ReportID   uuid.UUID
	UsedByType string
	UsedByID   uuid.UUID
	UsedAt     time.Time
}

// NewReportRow is the insert payload of a report. There is no status: a report starts DRAFT
// and moves only through the lifecycle commands.
type NewReportRow struct {
	ID                            uuid.UUID
	PersonID                      uuid.UUID
	CaseID                        *uuid.UUID
	Reference                     string
	VersionNo                     int
	RootReportID                  uuid.UUID
	SupersedesReportID            *uuid.UUID
	ReportType                    string
	ReportSubtype                 *string
	IssuingPractitionerID         *uuid.UUID
	IssuingProviderOrganizationID *uuid.UUID
	IssuedAt                      time.Time
	ValidFrom                     time.Time
	ValidTo                       time.Time
	ClinicalSummary               *string
	ActorID                       *uuid.UUID
}

// PatchReportRow is the header a draft accepts. Every field is sent every time: a patch that
// merged would make "clear the subtype" unexpressible.
type PatchReportRow struct {
	CaseID                        *uuid.UUID
	ReportType                    string
	ReportSubtype                 *string
	IssuingPractitionerID         *uuid.UUID
	IssuingProviderOrganizationID *uuid.UUID
	IssuedAt                      time.Time
	ValidFrom                     time.Time
	ValidTo                       time.Time
	ClinicalSummary               *string
	ActorID                       *uuid.UUID
}

// NewReportServiceRow is one line of a service set replacement.
type NewReportServiceRow struct {
	ServiceDefinitionID uuid.UUID
	CoveredQuantity     *string
	CoveredAmount       *string
	CurrencyCode        *string
	Notes               *string
	ActorID             *uuid.UUID
}

// ReportDecisionRow is what a reviewer's answer writes.
type ReportDecisionRow struct {
	ReviewComment    *string
	RejectReasonCode string
	ReviewedAt       time.Time
	ReviewedBy       uuid.UUID
	Expected         int64
}

// NewReportUsageRow is one usage. `UsedAt` is the moment the coverage was asserted, which
// is the moment the claim leaned on the report rather than the moment a job wrote a row.
type NewReportUsageRow struct {
	ReportID   uuid.UUID
	UsedByType string
	UsedByID   uuid.UUID
	UsedAt     time.Time
	ActorID    *uuid.UUID
}

// ReportQuery is the repository-level report filter.
type ReportQuery struct {
	Scope                  Scope
	PersonID               *uuid.UUID
	CaseID                 *uuid.UUID
	RootReportID           *uuid.UUID
	ProviderOrganizationID *uuid.UUID
	Status                 string
	ReportType             string
	ValidOn                *time.Time
	After                  *httpx.Cursor
	PageSize               int
}

// UsageQuery is the repository-level usage filter.
type UsageQuery struct {
	ReportID uuid.UUID
	After    *httpx.Cursor
	PageSize int
}

// ServiceDefinitionRecord is the one thing this module has to know about a catalogue entry
// a report line names: that it exists in this tenant, and what it is called.
type ServiceDefinitionRecord struct {
	ID     uuid.UUID
	Code   string
	Name   string
	Active bool
}

// ReportRepository is the persistence port of the treatment report. Like Repository it runs
// every method inside the caller's transaction, so a write, its audit row, the work item it
// raises and the notification it publishes commit together.
type ReportRepository interface {
	CreateReport(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewReportRow) (ReportRecord, error)
	GetReport(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (ReportRecord, error)
	// LockReport reads the row FOR UPDATE, so two commands on one report serialise.
	LockReport(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (ReportRecord, error)
	ListReports(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q ReportQuery) ([]ReportRecord, error)
	ListReportChain(ctx context.Context, tx pgx.Tx, tenantID, rootID uuid.UUID) ([]ReportRecord, error)
	// PatchReportDraft carries the whole precondition in its predicate: still a draft, still
	// at the version the caller read. It reports ErrVersionMismatch when nothing matched.
	PatchReportDraft(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in PatchReportRow, expected int64) error
	// TouchReportDraft moves the ETag of a draft whose lines were replaced.
	TouchReportDraft(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, actorID *uuid.UUID, expected int64) error

	MarkReportSubmitted(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
		submittedAt time.Time, actorID *uuid.UUID, expected int64) error
	// MarkReportUnderReview takes no expected version: the transition is also raised by the
	// worklist when a reviewer claims the item, and a claim has read the work item rather
	// than the report. It reports whether it moved anything, so claiming twice is one move.
	MarkReportUnderReview(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, actorID *uuid.UUID) (bool, error)
	MarkReportApproved(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in ReportDecisionRow) error
	MarkReportRejected(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in ReportDecisionRow) error
	MarkReportCancelled(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, actorID *uuid.UUID, expected int64) error
	// SupersedeReport moves the predecessor out of APPROVED and changes nothing else. It
	// runs in the successor's own transaction.
	SupersedeReport(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (bool, error)

	ListExpirableReports(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, asOf time.Time, limit int) ([]uuid.UUID, error)
	MarkReportExpired(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (bool, error)

	ListReportServices(ctx context.Context, tx pgx.Tx, tenantID, reportID uuid.UUID) ([]ReportServiceRecord, error)
	// ReplaceReportServices deletes the report's whole service set and writes the new one;
	// the set is the unit, and nothing hangs off a line id that would be worth keeping.
	ReplaceReportServices(ctx context.Context, tx pgx.Tx, tenantID, reportID uuid.UUID, rows []NewReportServiceRow) error
	CountReportServices(ctx context.Context, tx pgx.Tx, tenantID, reportID uuid.UUID) (int, error)

	ListReportDocuments(ctx context.Context, tx pgx.Tx, tenantID, reportID uuid.UUID) ([]ReportDocumentRecord, error)
	// CountCleanReportDocuments is the submit gate's second half: how many linked documents
	// of the report's own type the scanner has cleared.
	CountCleanReportDocuments(ctx context.Context, tx pgx.Tx, tenantID, reportID uuid.UUID, reportType string) (int, error)

	CreateReportUsage(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewReportUsageRow) (ReportUsageRecord, error)
	ListReportUsages(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q UsageQuery) ([]ReportUsageRecord, error)
	// ReportCoverageRow reads one report and, if it has one, its line for the service asked
	// about. It decides nothing: the service turns the three facts into an answer.
	ReportCoverageRow(ctx context.Context, tx pgx.Tx, tenantID, reportID, serviceDefinitionID uuid.UUID) (CoverageRow, error)

	GetReportServiceDefinition(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (ServiceDefinitionRecord, error)
	GetReportCase(ctx context.Context, tx pgx.Tx, tenantID, caseID uuid.UUID) (CaseSummary, error)
	ActiveTenants(ctx context.Context, tx pgx.Tx) ([]uuid.UUID, error)
}

// CaseSummary is the little a report has to know about the case it hangs off: whose it is,
// and how sensitive it is.
type CaseSummary struct {
	ID          uuid.UUID
	PersonID    uuid.UUID
	Status      string
	Sensitivity string
}

// CoverageRow is one report and, when it has one, its line for the service asked about.
type CoverageRow struct {
	ReportID        uuid.UUID
	Reference       string
	VersionNo       int
	RootReportID    uuid.UUID
	PersonID        uuid.UUID
	Status          string
	ValidFrom       time.Time
	ValidTo         time.Time
	HasServiceLine  bool
	CoveredQuantity *string
	CoveredAmount   *string
	CurrencyCode    *string
}

// CoverageRequest is one question WP-I5-04 asks: may this claim lean on this report for
// this service on this day, and who is asking.
//
// The work package writes the port as ReportCoverage(ctx, tx, reportID, serviceDefinitionID,
// date). Three things had to be added and none of them could be derived: the tenant, because
// the transaction is bound to one and the port is not a method on it; and the pair naming
// what is using the report, because "every call writes a usage row" and a usage row that
// could not say what used it would be a trace of nothing.
type CoverageRequest struct {
	TenantID            uuid.UUID
	ReportID            uuid.UUID
	ServiceDefinitionID uuid.UUID
	ServiceDate         time.Time
	UsedByType          string
	UsedByID            uuid.UUID
	ActorID             *uuid.UUID
}

// Coverage is the answer, with the version the caller may quote afterwards.
type Coverage struct {
	Usable     bool
	ReasonCode string
	ReportID   uuid.UUID
	Reference  string
	VersionNo  int
	// UsageID is the row written for a usable answer, and nil for a refusal: nothing used
	// a report it was not allowed to use.
	UsageID         *uuid.UUID
	CoveredQuantity *string
	CoveredAmount   *string
	CurrencyCode    *string
}

// WorkItemPort raises the work a submitted report is. It is a port rather than a call into
// the worklist service because a work item has to be written in the report command's own
// transaction: a report submitted with no item raised, or an item raised for a submission
// that rolled back, is work nobody can explain.
//
// The title it is given carries the report's reference and nothing else. A queue is a list
// people read across a room, and "Tıbbi rapor · onkoloji" on it would be a diagnosis on a
// wall.
type WorkItemPort interface {
	Raise(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in RaiseWorkItem) error
}

// RaiseWorkItem is one piece of work.
type RaiseWorkItem struct {
	QueueCode     string
	AggregateType string
	AggregateID   uuid.UUID
	Title         string
	ActorID       *uuid.UUID
}

// NoWorkItems is the default WorkItemPort: it raises nothing. A deployment that has not
// configured a medical review queue is a deployment where nobody is watching, and refusing
// the submission would not make anybody watch.
type NoWorkItems struct{}

// Raise implements WorkItemPort.
func (NoWorkItems) Raise(context.Context, pgx.Tx, uuid.UUID, RaiseWorkItem) error { return nil }
