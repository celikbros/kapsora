// Package application implements the service request use cases: opening a draft, editing
// it, submitting it through the eligibility and rule gate, and the five review commands
// that move it afterwards. Transactions are opened here with db.WithTenantTx, so a write,
// its status event and its audit row commit together and RLS is bound for every statement.
//
// Two things this package never does. It never writes a status a caller chose: every move
// is a named command with its own precondition, permission and reason, and the repository
// has no statement that would accept one. And it never touches the entitlement ledger:
// submitting a request decides nothing about balances, and reserving entitlement belongs
// to the authorization that comes after this one.
package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/eligibility"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/rules/engine"
)

// Permissions guarding request work (migration 000008). They live here rather than in the
// transport because who may decide a request is a business rule, not a routing detail.
const (
	PermissionRead   = "service_request.read"
	PermissionCreate = "service_request.create"
	PermissionSubmit = "service_request.submit"
	PermissionReview = "service_request.review"
	PermissionCancel = "service_request.cancel"
)

// ScopeOrganization is the iam.access_grant.scope_type of provider-side roles (v1.2 6.3).
const ScopeOrganization = "ORGANIZATION"

// Errors mapped by the transport layer to problem codes.
var (
	ErrRequestNotFound    = errors.New("servicerequest: request not found")
	ErrVersionNotFound    = errors.New("servicerequest: request version not found")
	ErrDraftNotFound      = errors.New("servicerequest: request has no draft version")
	ErrVersionImmutable   = errors.New("servicerequest: a submitted version cannot be changed")
	ErrTransitionInvalid  = errors.New("servicerequest: this transition is not allowed")
	ErrVersionMismatch    = errors.New("servicerequest: row version does not match If-Match")
	ErrEnrollmentMismatch = errors.New("servicerequest: the enrollment does not belong to this person or program")
	ErrProviderScope      = errors.New("servicerequest: the caller is not scoped to this provider")
	ErrReferenceCollision = errors.New("servicerequest: could not allocate a free request reference")
)

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

// RequestRecord is one service.service_request row.
type RequestRecord struct {
	ID                      uuid.UUID
	Reference               string
	RequestType             string
	PersonID                uuid.UUID
	ProgramID               uuid.UUID
	EnrollmentID            uuid.UUID
	ProviderOrganizationID  *uuid.UUID
	ServiceDate             time.Time
	RequestedStartAt        *time.Time
	RequestedEndAt          *time.Time
	Channel                 string
	Status                  string
	CurrentVersionNo        int
	SupersedesRequestID     *uuid.UUID
	EligibilityEvaluationID *uuid.UUID
	RuleEvaluationID        *uuid.UUID
	// RequiredDocumentTypes is nil when the rules have not been asked yet and an empty
	// slice when they were asked and required nothing; the two are different statements.
	RequiredDocumentTypes []string
	ReturnReasonCode      *string
	RejectReasonCode      *string
	ReviewComment         *string
	SubmittedAt           *time.Time
	ClosedAt              *time.Time
	CreatedAt             time.Time
	RowVersion            int64
}

// NewRequestRow is the insert payload of a request header.
type NewRequestRow struct {
	Reference              string
	RequestType            string
	PersonID               uuid.UUID
	ProgramID              uuid.UUID
	EnrollmentID           uuid.UUID
	ProviderOrganizationID *uuid.UUID
	ServiceDate            time.Time
	RequestedStartAt       *time.Time
	RequestedEndAt         *time.Time
	Channel                string
	SupersedesRequestID    *uuid.UUID
	ActorID                *uuid.UUID
}

// DraftHeaderRow is the merge-patch result written back to a draft request.
type DraftHeaderRow struct {
	ProviderOrganizationID *uuid.UUID
	ServiceDate            time.Time
	RequestedStartAt       *time.Time
	RequestedEndAt         *time.Time
	ActorID                *uuid.UUID
}

// SubmitRow is the single write the submit gate makes to the header.
type SubmitRow struct {
	Status                  string
	SubmittedAt             time.Time
	EligibilityEvaluationID *uuid.UUID
	RuleEvaluationID        *uuid.UUID
	RequiredDocumentTypes   []string
	ReviewComment           *string
	ActorID                 *uuid.UUID
}

// ReturnRow sends a request back to the requester with the version it is corrected in.
type ReturnRow struct {
	CurrentVersionNo int
	ReasonCode       string
	ReviewComment    *string
	ActorID          *uuid.UUID
}

// RejectRow closes a request as refused.
type RejectRow struct {
	ReasonCode    string
	ReviewComment *string
	DecidedAt     time.Time
	ActorID       *uuid.UUID
}

// ApproveRow records an approval or a partial approval; the two differ only in the status.
type ApproveRow struct {
	Status        string
	ReviewComment *string
	ActorID       *uuid.UUID
}

// CancelRow withdraws a request that has not been decided.
type CancelRow struct {
	CancelledAt time.Time
	ActorID     *uuid.UUID
}

// RequestQuery is the repository-level request filter.
type RequestQuery struct {
	Scope                  Scope
	Status                 string
	PersonID               *uuid.UUID
	ProgramID              *uuid.UUID
	ProviderOrganizationID *uuid.UUID
	Channel                string
	ServiceDateFrom        *time.Time
	ServiceDateTo          *time.Time
	CreatedFrom            *time.Time
	CreatedTo              *time.Time
	After                  *httpx.Cursor
	PageSize               int
}

// VersionRecord is one service.service_request_version row.
type VersionRecord struct {
	ID               uuid.UUID
	ServiceRequestID uuid.UUID
	VersionNo        int
	Status           string
	// Snapshot is the frozen document of a submitted version and nil for a draft.
	Snapshot         []byte
	SubmittedAt      *time.Time
	SubmittedBy      *uuid.UUID
	ReturnedAt       *time.Time
	ReturnedBy       *uuid.UUID
	ReturnReasonCode *string
	ReturnReasonText *string
	CreatedAt        time.Time
}

// NewVersionRow opens a version. The return fields are set only when the version exists
// because the previous one was sent back, which is the only version anybody may write
// them to: migration 000006 freezes a submitted version byte for byte.
type NewVersionRow struct {
	ServiceRequestID uuid.UUID
	VersionNo        int
	ReturnedAt       *time.Time
	ReturnedBy       *uuid.UUID
	ReturnReasonCode *string
	ReturnReasonText *string
	ActorID          *uuid.UUID
}

// FreezeRow is the submit-time write that turns a draft version into the record of what
// was actually asked for.
type FreezeRow struct {
	Snapshot    []byte
	SubmittedAt time.Time
	ActorID     *uuid.UUID
}

// ItemRecord is one service.service_request_item row. Every decimal is carried as the
// exact text the numeric column holds.
type ItemRecord struct {
	ID                  uuid.UUID
	VersionID           uuid.UUID
	LineNo              int
	ServiceDefinitionID uuid.UUID
	RequestedQuantity   string
	UnitType            string
	RequestedAmount     *string
	CurrencyCode        *string
	Status              string
	ApprovedQuantity    *string
	ApprovedAmount      *string
	DecisionReasonCode  *string
}

// NewItemRow is one line of a set replacement.
type NewItemRow struct {
	LineNo              int
	ServiceDefinitionID uuid.UUID
	RequestedQuantity   benefitdomain.Quantity
	UnitType            string
	RequestedAmount     *benefitdomain.Quantity
	CurrencyCode        *string
}

// ItemDecisionRow is the line-level outcome a reviewer recorded.
type ItemDecisionRow struct {
	LineNo             int
	Status             string
	ApprovedQuantity   *benefitdomain.Quantity
	ApprovedAmount     *benefitdomain.Quantity
	DecisionReasonCode *string
}

// StatusEventRow is one append-only workflow.status_event row.
type StatusEventRow struct {
	AggregateID    uuid.UUID
	FromStatus     string
	ToStatus       string
	TransitionCode string
	ReasonCode     *string
	ReasonText     *string
	ActorID        *uuid.UUID
	Metadata       map[string]any
}

// StatusEventRecord is a stored transition, read back for the history.
type StatusEventRecord struct {
	ID             uuid.UUID
	FromStatus     *string
	ToStatus       string
	TransitionCode string
	ReasonCode     *string
	ReasonText     *string
	OccurredAt     time.Time
	ActorID        *uuid.UUID
}

// EnrollmentRecord is what the create command needs to check that a request's person,
// program and enrollment belong together.
type EnrollmentRecord struct {
	ID        uuid.UUID
	PlanID    uuid.UUID
	ProgramID uuid.UUID
	Status    string
	PersonID  uuid.UUID
	ValidFrom time.Time
	ValidTo   *time.Time
}

// ServiceDefinitionRecord is one catalog row a line points at.
type ServiceDefinitionRecord struct {
	ID               uuid.UUID
	Code             string
	DefaultUnitType  string
	RequiresProvider bool
	Active           bool
}

// EligibilityInput is everything the pure resolver reads, loaded in one transaction.
type EligibilityInput struct {
	Person      eligibility.Person
	Memberships []eligibility.Membership
	Enrollments []eligibility.Enrollment
	PlanVersion *eligibility.PlanVersion
	Accounts    []eligibility.Account
}

// NewEligibilityEvaluationRow is the append-only evaluation the submit gate stores, so the
// answer the gate was given can be read again long after the balances have moved on.
type NewEligibilityEvaluationRow struct {
	ID              uuid.UUID
	PersonID        uuid.UUID
	ProgramID       *uuid.UUID
	EnrollmentID    *uuid.UUID
	PlanVersionID   *uuid.UUID
	ProviderOrgID   *uuid.UUID
	ServiceDate     time.Time
	Outcome         string
	RequestHash     []byte
	RequestSnapshot []byte
	ResultSnapshot  []byte
	EvaluatedAt     time.Time
	EvaluatedBy     *uuid.UUID
}

// RuleVersion is one published rule set version with the rules under it in evaluation
// order, compiled by the caller.
type RuleVersion struct {
	ID          uuid.UUID
	RuleSetID   uuid.UUID
	RuleSetCode string
	Purpose     string
	VersionNo   int
	InputSchema map[string]string
	Rules       []engine.Rule
}

// NewRuleEvaluationRow is the append-only rule evaluation header.
type NewRuleEvaluationRow struct {
	SubjectID        uuid.UUID
	RuleSetVersionID uuid.UUID
	InputHash        []byte
	InputSnapshot    []byte
	Outcome          string
	DurationMs       int
	EvaluatedBy      *uuid.UUID
}

// RuleEvaluationResultRow is one line of the stored rule trace.
type RuleEvaluationResultRow struct {
	Sequence        int
	RuleID          *uuid.UUID
	RuleCode        string
	Matched         bool
	ActionType      *string
	ActionPayload   map[string]any
	ExplanationCode string
	Severity        string
}

// Repository is the persistence port; every method runs inside the caller's transaction,
// which db.WithTenantTx has already bound to the tenant so RLS is active. The provider
// boundary is a repository concern too: the scope is passed down rather than checked above,
// so a request outside it is genuinely not there rather than fetched and then hidden.
type Repository interface {
	CreateRequest(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewRequestRow) (RequestRecord, error)
	GetRequest(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (RequestRecord, error)
	// LockRequest reads the row FOR UPDATE, so two commands on one request serialise.
	LockRequest(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (RequestRecord, error)
	ListRequests(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q RequestQuery) ([]RequestRecord, error)
	UpdateDraftHeader(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in DraftHeaderRow, expected int64) error
	// TouchRequest bumps row_version without changing a business field, so replacing the
	// lines invalidates the ETag the caller holds for the request.
	TouchRequest(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, actorID *uuid.UUID) error

	MarkSubmitted(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in SubmitRow, expected int64) error
	MarkReturned(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in ReturnRow, expected int64) error
	MarkRejected(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in RejectRow, expected int64) error
	MarkApproved(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in ApproveRow, expected int64) error
	MarkCancelled(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in CancelRow, expected int64) error

	CreateVersion(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewVersionRow) (VersionRecord, error)
	GetDraftVersion(ctx context.Context, tx pgx.Tx, tenantID, requestID uuid.UUID) (VersionRecord, error)
	GetVersionByNo(ctx context.Context, tx pgx.Tx, tenantID, requestID uuid.UUID, versionNo int) (VersionRecord, error)
	ListVersions(ctx context.Context, tx pgx.Tx, tenantID, requestID uuid.UUID) ([]VersionRecord, error)
	FreezeVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, in FreezeRow) error
	SupersedeVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) error

	ListItems(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) ([]ItemRecord, error)
	// ReplaceItems deletes the draft's whole line set and writes the new one; a line
	// number identifies a line only inside one version, so nothing hangs off an item id
	// that would be worth preserving.
	ReplaceItems(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, rows []NewItemRow) error
	DecideItems(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, rows []ItemDecisionRow) error

	AppendStatusEvent(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in StatusEventRow) error
	ListStatusEvents(ctx context.Context, tx pgx.Tx, tenantID, requestID uuid.UUID) ([]StatusEventRecord, error)

	GetEnrollment(ctx context.Context, tx pgx.Tx, tenantID, enrollmentID uuid.UUID) (EnrollmentRecord, error)
	ProviderOrganizationExists(ctx context.Context, tx pgx.Tx, tenantID, orgID uuid.UUID) (bool, error)
	ListServiceDefinitions(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, ids []uuid.UUID) ([]ServiceDefinitionRecord, error)

	// LoadEligibility reads the person, the memberships, the enrollments, the plan version
	// published on the service date and the already-open entitlement accounts. It writes
	// nothing and opens nothing: opening an account posts a GRANT movement, and the ledger
	// has to be exactly as this package found it.
	LoadEligibility(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID,
		programID *uuid.UUID, serviceDate time.Time) (EligibilityInput, error)
	CreateEligibilityEvaluation(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
		in NewEligibilityEvaluationRow) error

	// ListRuleVersions returns every published version of the tenant's rule sets that
	// serves one of the purposes and covers the service date.
	ListRuleVersions(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
		purposes []string, serviceDate time.Time) ([]RuleVersion, error)
	CreateRuleEvaluation(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
		in NewRuleEvaluationRow, results []RuleEvaluationResultRow) (uuid.UUID, error)

	// ReviewRequired answers whether a program's requests still need a person to look at
	// them once nothing has objected. It is configuration rather than a hard-coded rule,
	// and "not configured" means a person reviews it.
	ReviewRequired(ctx context.Context, tx pgx.Tx, tenantID, programID uuid.UUID) (bool, error)
}
