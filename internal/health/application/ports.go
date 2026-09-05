// Package application implements the health case use cases: opening a case, recording an
// encounter, replacing an encounter's diagnoses, closing a case, and reading any of it —
// and, above all, deciding which half of a record the caller reading it is shown.
// Transactions are opened here with db.WithTenantTx, so a write, its audit row and the
// access event it owes commit together and RLS is bound for every statement.
//
// Three things this package never does.
//
// It never hands a clinical field to a caller who has not earned it. The projection is
// applied in exactly one function, on the record, before anything maps it to the wire; a
// screen dropping fields would be a second rule that could disagree, and a screen is not
// where a data protection guarantee belongs.
//
// It never writes a sensitivity a caller chose. `diagnosis.sensitive` is read from the code
// value's own category in the catalogue and `health_case.sensitivity` is recomputed from
// the diagnoses that were actually stored. There is no parameter anywhere below that would
// accept either from outside.
//
// And it never serves the clinical projection of a sensitive case silently. Either the
// caller stated why, and the reason is on the access event, or the read did not happen and
// the refusal is on the access event instead.
package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Permissions guarding health work. health.case.read, health.case.manage and
// health.clinical.read are in the catalogue from migration 000008; health.sensitive.read is
// added by migration 000031. They live here rather than in the transport because who may
// see a diagnosis is a business rule, not a routing detail.
const (
	PermissionCaseRead      = "health.case.read"
	PermissionCaseManage    = "health.case.manage"
	PermissionClinicalRead  = "health.clinical.read"
	PermissionSensitiveRead = "health.sensitive.read"
	// PermissionAuditRead guards the access log: who looked at a person's clinical data is
	// itself a record only an auditor reads.
	PermissionAuditRead = "audit.read"
)

// ScopeOrganization is the iam.access_grant.scope_type of provider-side roles (v1.2 6.3).
const ScopeOrganization = "ORGANIZATION"

// Errors mapped by the transport layer to problem codes.
var (
	ErrCaseNotFound      = errors.New("health: health case not found")
	ErrEncounterNotFound = errors.New("health: encounter not found")
	// ErrCaseClosed refuses a second close and any write into a finished case.
	ErrCaseClosed = errors.New("health: the case is already closed")
	// ErrEncounterOpen refuses a close over an encounter nobody ended.
	ErrEncounterOpen = errors.New("health: the case has an encounter that has not ended")
	// ErrStayOpen refuses a close over an inpatient stay that is still running. The stay
	// itself belongs to WP-I5-03; here the port exists and answers "none open".
	ErrStayOpen        = errors.New("health: the case has an open inpatient stay")
	ErrVersionMismatch = errors.New("health: row version does not match If-Match")
	// ErrClinicalReadRequired is the refusal listEncounterDiagnoses owes a caller that may
	// read cases but not clinical detail. It is deliberately not an empty list: an empty
	// list would tell the caller the encounter has no diagnosis, which is a clinical fact.
	ErrClinicalReadRequired = errors.New("health: reading a diagnosis needs health.clinical.read")
	// ErrAccessPurposeRequired is a sensitive case read without a stated purpose.
	ErrAccessPurposeRequired = errors.New("health: reading a sensitive case needs a stated purpose")
	ErrEnrollmentMismatch    = errors.New("health: the enrollment does not belong to this person or program")
	ErrProviderScope         = errors.New("health: the caller is not scoped to this provider")
	ErrProviderUnknown       = errors.New("health: the organization is not a provider of this tenant")
	// ErrRequestNotEligible is a case opened from a request that is not an episode of care:
	// the wrong request type, another person's, or one with no HEALTH-domain line on it.
	ErrRequestNotEligible = errors.New("health: the service request cannot open a health case")
	ErrRequestNotFound    = errors.New("health: the service request was not found")
	ErrCodeValueUnknown   = errors.New("health: a diagnosis code was not found in the catalogue")
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

// CaseRecord is one health.health_case row, as the database holds it. Every read returns
// the whole row including Sensitivity; the projection drops what the caller may not have.
type CaseRecord struct {
	ID                     uuid.UUID
	PersonID               uuid.UUID
	ProgramID              uuid.UUID
	EnrollmentID           uuid.UUID
	CaseType               string
	ProviderOrganizationID *uuid.UUID
	OpenedAt               time.Time
	ClosedAt               *time.Time
	Status                 string
	// Sensitivity is "" once the financial projection has been applied: the record no
	// longer carries the answer, rather than carrying it and relying on a mapper to hide it.
	Sensitivity      string
	ServiceRequestID *uuid.UUID
	CreatedAt        time.Time
	RowVersion       int64
}

// EncounterRecord is one health.encounter row.
type EncounterRecord struct {
	ID             uuid.UUID
	CaseID         uuid.UUID
	EncounterType  string
	StartedAt      time.Time
	EndedAt        *time.Time
	LocationID     *uuid.UUID
	PractitionerID *uuid.UUID
	// BranchCode and NotesClinical are nil once the financial projection has been applied.
	BranchCode    *string
	NotesClinical *string
	CreatedAt     time.Time
	RowVersion    int64
}

// DiagnosisRecord is one health.diagnosis row with the catalogue code it names.
type DiagnosisRecord struct {
	ID             uuid.UUID
	EncounterID    uuid.UUID
	CodeSystemID   uuid.UUID
	CodeSystemCode string
	CodeValueID    uuid.UUID
	Code           string
	Display        string
	DiagnosisType  string
	Sensitive      bool
	RecordedAt     time.Time
	RecordedBy     *uuid.UUID
}

// NewCaseRow is the insert payload of a case. There is no sensitivity: a case starts
// STANDARD and becomes what its diagnoses make it.
type NewCaseRow struct {
	PersonID               uuid.UUID
	ProgramID              uuid.UUID
	EnrollmentID           uuid.UUID
	CaseType               string
	ProviderOrganizationID *uuid.UUID
	OpenedAt               time.Time
	ServiceRequestID       *uuid.UUID
	ActorID                *uuid.UUID
}

// NewEncounterRow is the insert payload of an encounter.
type NewEncounterRow struct {
	CaseID         uuid.UUID
	EncounterType  string
	StartedAt      time.Time
	EndedAt        *time.Time
	LocationID     *uuid.UUID
	PractitionerID *uuid.UUID
	BranchCode     *string
	NotesClinical  *string
	ActorID        *uuid.UUID
}

// NewDiagnosisRow is one line of a diagnosis set replacement. Sensitive is filled in by the
// service from the catalogue, never from the caller.
type NewDiagnosisRow struct {
	EncounterID   uuid.UUID
	CodeSystemID  uuid.UUID
	CodeValueID   uuid.UUID
	DiagnosisType string
	Sensitive     bool
	RecordedAt    time.Time
	ActorID       *uuid.UUID
}

// CaseQuery is the repository-level case filter.
type CaseQuery struct {
	Scope                  Scope
	PersonID               *uuid.UUID
	ProgramID              *uuid.UUID
	ProviderOrganizationID *uuid.UUID
	Status                 string
	CaseType               string
	OpenedFrom             *time.Time
	OpenedTo               *time.Time
	After                  *httpx.Cursor
	PageSize               int
}

// EnrollmentRecord is what the create command needs to check that a case's person, program
// and enrollment belong together.
type EnrollmentRecord struct {
	ID        uuid.UUID
	PlanID    uuid.UUID
	ProgramID uuid.UUID
	Status    string
	PersonID  uuid.UUID
}

// ServiceRequestRecord is the request a case may be opened from, with the one thing this
// module decides about it: whether any of its lines names a HEALTH-domain service.
type ServiceRequestRecord struct {
	ID               uuid.UUID
	PersonID         uuid.UUID
	ProgramID        uuid.UUID
	EnrollmentID     uuid.UUID
	RequestType      string
	HasHealthService bool
}

// CodeValueRecord is one catalogue code a diagnosis names, with the sensitivity its own
// category gives it.
type CodeValueRecord struct {
	ID             uuid.UUID
	CodeSystemID   uuid.UUID
	CodeSystemCode string
	Code           string
	Display        string
	Active         bool
	Sensitive      bool
}

// AccessEventRecord is one audit.access_event row of the health access log.
type AccessEventRecord struct {
	ID           uuid.UUID
	OccurredAt   time.Time
	ActorID      uuid.UUID
	MembershipID *uuid.UUID
	PersonID     *uuid.UUID
	ResourceType string
	ResourceID   *uuid.UUID
	AccessType   string
	PurposeCode  *string
	ReasonText   *string
	Outcome      string
}

// AccessEventQuery is the repository-level access log filter.
type AccessEventQuery struct {
	PersonID *uuid.UUID
	After    *httpx.Cursor
	PageSize int
}

// Repository is the persistence port; every method runs inside the caller's transaction,
// which db.WithTenantTx has already bound to the tenant so RLS is active. The provider
// boundary is a repository concern too: the scope is passed down rather than checked above,
// so a case outside it is genuinely not there rather than fetched and then hidden.
type Repository interface {
	CreateCase(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewCaseRow) (CaseRecord, error)
	GetCase(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (CaseRecord, error)
	// LockCase reads the row FOR UPDATE, so two commands on one case serialise.
	LockCase(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (CaseRecord, error)
	ListCases(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q CaseQuery) ([]CaseRecord, error)
	// CloseCase carries the whole precondition in its predicate: OPEN and the row version
	// the caller read. It reports ErrVersionMismatch when nothing matched.
	CloseCase(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, closedAt time.Time,
		actorID *uuid.UUID, expected int64) error
	// SetCaseSensitivity writes the value recomputed from the stored diagnoses.
	SetCaseSensitivity(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
		sensitivity string, actorID *uuid.UUID) error
	CountOpenEncounters(ctx context.Context, tx pgx.Tx, tenantID, caseID uuid.UUID) (int, error)

	CreateEncounter(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewEncounterRow) (EncounterRecord, error)
	GetEncounter(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (EncounterRecord, error)
	ListCaseEncounters(ctx context.Context, tx pgx.Tx, tenantID, caseID uuid.UUID) ([]EncounterRecord, error)

	ListDiagnoses(ctx context.Context, tx pgx.Tx, tenantID, encounterID uuid.UUID) ([]DiagnosisRecord, error)
	// ReplaceDiagnoses deletes the encounter's whole diagnosis set and writes the new one;
	// the set is the unit, and nothing hangs off a diagnosis id that would be worth keeping.
	ReplaceDiagnoses(ctx context.Context, tx pgx.Tx, tenantID, encounterID uuid.UUID, rows []NewDiagnosisRow) error
	CaseHasSensitiveDiagnosis(ctx context.Context, tx pgx.Tx, tenantID, caseID uuid.UUID) (bool, error)
	// ResolveCodeValues reads the catalogue codes a diagnosis set names, with the
	// sensitivity each one's own category gives it.
	ResolveCodeValues(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, ids []uuid.UUID) ([]CodeValueRecord, error)

	GetEnrollment(ctx context.Context, tx pgx.Tx, tenantID, enrollmentID uuid.UUID) (EnrollmentRecord, error)
	GetServiceRequest(ctx context.Context, tx pgx.Tx, tenantID, requestID uuid.UUID) (ServiceRequestRecord, error)
	ProviderOrganizationExists(ctx context.Context, tx pgx.Tx, tenantID, orgID uuid.UUID) (bool, error)
	// AccessPurposeExists checks a purpose against health.clinical_access_purpose. The
	// domain repeats the list so a malformed header is a field error without a round trip;
	// this is the authority, so a purpose seeded later works without a Go release.
	AccessPurposeExists(ctx context.Context, tx pgx.Tx, purpose string) (bool, error)

	ListAccessEvents(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q AccessEventQuery) ([]AccessEventRecord, error)
}

// StayPort answers whether a case still has an inpatient stay running. WP-I5-03 owns the
// stay and will implement it over its own tables; until then NoOpenStays answers "none",
// which is the only answer a schema with no stays in it could honestly give.
type StayPort interface {
	OpenStays(ctx context.Context, tx pgx.Tx, tenantID, caseID uuid.UUID) (int, error)
}

// NoOpenStays is the default StayPort.
type NoOpenStays struct{}

// OpenStays implements StayPort.
func (NoOpenStays) OpenStays(context.Context, pgx.Tx, uuid.UUID, uuid.UUID) (int, error) {
	return 0, nil
}
