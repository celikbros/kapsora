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

// AdmissionServiceCode is the catalogue definition an admission is requested against. It is
// found by code rather than named by the caller for the same reason WP-I4-02 maps a line to
// an entitlement by code: which service a ward night is booked as is the tenant's
// configuration, and a provider that could choose it could book a night as a session.
const AdmissionServiceCode = "INPATIENT_DAY"

// The tenant settings that bound the admission window (migration 000033's comment). They
// are keys of platform.tenant_setting rather than columns, so a tenant that has never
// thought about the window needs no row at all.
const (
	SettingBackdateDays = "health.inpatient.backdate_days"
	SettingFutureDays   = "health.inpatient.future_days"
)

// Errors mapped by the transport layer to problem codes.
var (
	ErrStayNotFound      = errors.New("health: inpatient stay not found")
	ErrExtensionNotFound = errors.New("health: stay extension not found")
	// ErrStayAlreadyOpen is the one-open-stay rule of v1.2 10.4 step 3, as the service
	// states it. The partial unique index states it again, and the repository turns that
	// index's violation into this same error: two callers arriving together are refused by
	// the database, and both of them are told the same thing.
	ErrStayAlreadyOpen = errors.New("health: this case already has an open stay at this provider")
	// ErrStayExtensionPending is the one-undecided-extension rule of v1.2 10.4 step 6.
	ErrStayExtensionPending = errors.New("health: an earlier extension has not been decided")
	// ErrAdmissionOutOfWindow is an admission dated outside the tenant's backdating and
	// future-dating window.
	ErrAdmissionOutOfWindow = errors.New("health: the admission date is outside the allowed window")
	// ErrStayTransitionInvalid refuses a command the stay's status does not have.
	ErrStayTransitionInvalid = errors.New("health: the stay is not in a state for this command")
	// ErrStayCaseClosed refuses an admission into a case somebody has already closed.
	ErrStayCaseClosed = errors.New("health: the case is closed")
	// ErrSegmentOverlap is the exclusion constraint answering. It reaches the caller when
	// the database refused a set the service's own overlap check passed, which is the case
	// the constraint exists for.
	ErrSegmentOverlap = errors.New("health: two segments of this stay claim the same hours")
	// ErrAdmissionServiceUnknown is a tenant with no admission service in its catalogue.
	// It is a refusal rather than a silent fallback: booking an admission against whatever
	// definition happened to be first would reserve the wrong entitlement.
	ErrAdmissionServiceUnknown = errors.New("health: the tenant has no inpatient admission service")
	// ErrAdmissionDiagnosisMismatch is a diagnosis borrowed from another person's case.
	ErrAdmissionDiagnosisMismatch = errors.New("health: the admission diagnosis belongs to another case")
	// ErrStayNotDischarged refuses a reconciliation of a stay that has not ended.
	ErrStayNotDischarged = errors.New("health: the stay has not been discharged")
)

// StayRecord is one health.inpatient_stay row, as the database holds it, with the two facts
// that come from its case: whose it is and how sensitive it is. Every read returns the whole
// row; the projection drops what the caller may not have.
type StayRecord struct {
	ID                      uuid.UUID
	CaseID                  uuid.UUID
	PersonID                uuid.UUID
	ProviderOrganizationID  uuid.UUID
	LocationID              *uuid.UUID
	AttendingPractitionerID *uuid.UUID
	AdmissionAt             time.Time
	EstimatedDays           int
	ExpectedDischargeAt     time.Time
	DischargeAt             *time.Time
	Status                  string
	ServiceRequestID        uuid.UUID
	AuthorizationID         *uuid.UUID
	// AdmissionDiagnosisID is clinical: that an admission has a recorded diagnosis at all
	// is a fact about the patient, so the financial projection clears it.
	AdmissionDiagnosisID *uuid.UUID
	// The reconciliation, as the exact decimal text the numeric columns hold. An empty
	// string is a NULL column: "not authorized yet" and "authorized for nothing" are
	// different answers and a zero would collapse them.
	AuthorizedDays    string
	ActualDays        string
	ReleasedDays      string
	OverAuthorization bool
	CancelReasonCode  *string
	CreatedAt         time.Time
	RowVersion        int64
	// CaseSensitivity is what the projection decides on. It is cleared in both projections
	// before a record leaves the service: it is a fact of the case, not of the stay.
	CaseSensitivity string
}

// StayExtensionRecord is one health.stay_extension row.
type StayExtensionRecord struct {
	ID             uuid.UUID
	StayID         uuid.UUID
	SequenceNo     int
	AdditionalDays int
	ReasonCode     string
	// ReasonText is why the doctor wants more days, in the doctor's words. It is clinical
	// and is nil once the financial projection has been applied.
	ReasonText       *string
	ServiceRequestID uuid.UUID
	AuthorizationID  *uuid.UUID
	Status           string
	CreatedAt        time.Time
	RowVersion       int64
}

// StaySegmentRecord is one health.stay_segment row. Nothing here is cleared by the
// financial projection: where somebody slept and for how long is what a claim is priced
// from, and a claims reviewer who could not see an intensive care night could not check the
// bill for one.
type StaySegmentRecord struct {
	ID          uuid.UUID
	StayID      uuid.UUID
	SegmentType string
	StartsAt    time.Time
	EndsAt      *time.Time
	RoomCode    *string
	BedCode     *string
	CreatedAt   time.Time
	RowVersion  int64
}

// StayCaseRecord is the case an admission is opened against, with everything the create
// command has to check about it.
type StayCaseRecord struct {
	ID                     uuid.UUID
	PersonID               uuid.UUID
	ProgramID              uuid.UUID
	EnrollmentID           uuid.UUID
	CaseType               string
	ProviderOrganizationID *uuid.UUID
	Status                 string
	Sensitivity            string
}

// AdmissionServiceRecord is the catalogue definition an admission is booked as.
type AdmissionServiceRecord struct {
	ID               uuid.UUID
	Code             string
	DefaultUnitType  string
	RequiresProvider bool
	Active           bool
}

// NewStayRow is the insert payload of a stay. There is no status: a stay is always created
// REQUESTED, and a field a caller could set to AUTHORIZED would be a way past the reviewer.
type NewStayRow struct {
	CaseID                  uuid.UUID
	ProviderOrganizationID  uuid.UUID
	LocationID              *uuid.UUID
	AttendingPractitionerID *uuid.UUID
	AdmissionAt             time.Time
	EstimatedDays           int
	ExpectedDischargeAt     time.Time
	ServiceRequestID        uuid.UUID
	AdmissionDiagnosisID    *uuid.UUID
	ActorID                 *uuid.UUID
}

// NewStayExtensionRow is the insert payload of an extension.
type NewStayExtensionRow struct {
	StayID           uuid.UUID
	SequenceNo       int
	AdditionalDays   int
	ReasonCode       string
	ReasonText       *string
	ServiceRequestID uuid.UUID
	ActorID          *uuid.UUID
}

// NewSegmentRow is one line of a segment set replacement.
type NewSegmentRow struct {
	StayID      uuid.UUID
	SegmentType string
	StartsAt    time.Time
	EndsAt      *time.Time
	RoomCode    *string
	BedCode     *string
	ActorID     *uuid.UUID
}

// DischargeRow is the whole reconciliation, written in one statement so a stay is never
// momentarily discharged without its figures.
type DischargeRow struct {
	DischargeAt       time.Time
	ActualDays        string
	ReleasedDays      string
	OverAuthorization bool
	ActorID           *uuid.UUID
	Expected          int64
}

// StayQuery is the repository-level stay filter.
type StayQuery struct {
	Scope                  Scope
	CaseID                 *uuid.UUID
	PersonID               *uuid.UUID
	ProviderOrganizationID *uuid.UUID
	Status                 string
	AdmittedFrom           *time.Time
	AdmittedTo             *time.Time
	After                  *httpx.Cursor
	PageSize               int
}

// StayRepository is the persistence port of the inpatient stay; every method runs inside the
// caller's transaction, which db.WithTenantTx has already bound to the tenant so RLS is
// active. The provider boundary is a repository concern too: the scope is passed down rather
// than checked above, so a stay outside it is genuinely not there rather than fetched and
// then hidden.
//
// Two methods deliberately take no scope. LockStayByRequest and LockExtensionByRequest are
// the outbox subscriber's: it is the worker acting for the tenant rather than a person
// acting for a provider, and a stay it could not see is a stay whose decision would silently
// do nothing.
type StayRepository interface {
	// CreateStay reports ErrStayAlreadyOpen when the partial unique index refuses a second
	// open stay, so the constraint rather than a preceding count is what decides.
	CreateStay(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewStayRow) (StayRecord, error)
	GetStay(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (StayRecord, error)
	// LockStay reads the row FOR UPDATE, so two commands on one stay serialise.
	LockStay(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (StayRecord, error)
	LockStayByRequest(ctx context.Context, tx pgx.Tx, tenantID, requestID uuid.UUID) (StayRecord, error)
	ListStays(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q StayQuery) ([]StayRecord, error)
	CountOpenStays(ctx context.Context, tx pgx.Tx, tenantID, caseID uuid.UUID) (int, error)

	// AuthorizeStay and RejectStay are the two writes the outbox subscriber makes. Both
	// name REQUESTED in their own predicate and report whether they moved anything, so a
	// redelivered decision finds nothing to do rather than reauthorizing a stay that has
	// since been admitted.
	AuthorizeStay(ctx context.Context, tx pgx.Tx, tenantID, id, authorizationID uuid.UUID,
		authorizedDays string, actorID *uuid.UUID) (bool, error)
	RejectStay(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, actorID *uuid.UUID) (bool, error)
	AdmitStay(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, actorID *uuid.UUID) (bool, error)
	AddAuthorizedDays(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, additionalDays string,
		expectedDischargeAt time.Time, actorID *uuid.UUID) (bool, error)
	DischargeStay(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in DischargeRow) error
	CancelStay(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, reasonCode string,
		actorID *uuid.UUID, expected int64) error

	CreateExtension(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewStayExtensionRow) (StayExtensionRecord, error)
	ListExtensions(ctx context.Context, tx pgx.Tx, tenantID, stayID uuid.UUID) ([]StayExtensionRecord, error)
	LockExtensionByRequest(ctx context.Context, tx pgx.Tx, tenantID, requestID uuid.UUID) (StayExtensionRecord, error)
	NextExtensionSequence(ctx context.Context, tx pgx.Tx, tenantID, stayID uuid.UUID) (int, error)
	CountPendingExtensions(ctx context.Context, tx pgx.Tx, tenantID, stayID uuid.UUID) (int, error)
	ApproveExtension(ctx context.Context, tx pgx.Tx, tenantID, id, authorizationID uuid.UUID,
		actorID *uuid.UUID) (bool, error)
	RejectExtension(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, actorID *uuid.UUID) (bool, error)
	CancelExtensions(ctx context.Context, tx pgx.Tx, tenantID, stayID uuid.UUID, actorID *uuid.UUID) (int, error)

	// ReplaceSegments deletes the stay's whole segment set and writes the new one. It
	// reports ErrSegmentOverlap when the exclusion constraint refuses the set.
	ReplaceSegments(ctx context.Context, tx pgx.Tx, tenantID, stayID uuid.UUID, rows []NewSegmentRow) error
	ListSegments(ctx context.Context, tx pgx.Tx, tenantID, stayID uuid.UUID) ([]StaySegmentRecord, error)
	EndOpenSegments(ctx context.Context, tx pgx.Tx, tenantID, stayID uuid.UUID, endsAt time.Time,
		actorID *uuid.UUID) (int, error)

	GetStayCase(ctx context.Context, tx pgx.Tx, tenantID, caseID uuid.UUID, scope Scope) (StayCaseRecord, error)
	GetAdmissionService(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, code string) (AdmissionServiceRecord, error)
	// GetDiagnosisCase answers which case a diagnosis belongs to, which is the one thing
	// the create command has to know about the admission diagnosis it was handed.
	GetDiagnosisCase(ctx context.Context, tx pgx.Tx, tenantID, diagnosisID uuid.UUID) (uuid.UUID, error)
	// WindowSetting reads one of the two admission window keys. An absent key is not an
	// error: the caller falls back to the documented default.
	WindowSetting(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, key string) (int, bool, error)
}

// StayRequestInput is what the preauthorization behind an admission asks for.
type StayRequestInput struct {
	PersonID               uuid.UUID
	EnrollmentID           uuid.UUID
	ProviderOrganizationID uuid.UUID
	ServiceDefinitionID    uuid.UUID
	UnitType               string
	// Days is the requested quantity: an admission is asked for in days, and the
	// authorization that follows promises days.
	Days int
	// ServiceDate is the admission day; it is what the eligibility gate reads balances on.
	ServiceDate time.Time
	// RequestedStartAt and RequestedEndAt are the admission window the request carries, so
	// a reviewer opening the request page sees when the person is expected in and out.
	RequestedStartAt time.Time
	RequestedEndAt   time.Time
}

// StayRequestRef is the request a stay hangs off, as this package needs it: the id to store
// and the status the gate left it in.
type StayRequestRef struct {
	ID        uuid.UUID
	Reference string
	Status    string
}

// RequestPort creates and submits the PREAUTHORIZATION request an admission is asked for
// with. It is a port rather than a direct call so that this package cannot grow its own
// copy of the submit gate: the document requirement, the eligibility evaluation and the
// rule trace are WP-I4-01's, and a stay that decided any of them itself would be a second
// gate that could disagree with the one the reviewer reads.
//
// It takes the caller's transaction, so the stay, its request and the request's evaluations
// are one atomic fact. A stay that exists with no request, or a request raised for a stay
// that rolled back, is a state that never exists.
type RequestPort interface {
	CreatePreauthorization(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
		in StayRequestInput) (StayRequestRef, error)
}

// StayAuthorizationInput is a hold this package asks WP-I4-02 for.
type StayAuthorizationInput struct {
	RequestID uuid.UUID
	ValidFrom time.Time
	ValidTo   time.Time
	// IdempotencyKey is derived from the stay or the extension rather than from a clock, so
	// a redelivered outbox event finds the authorization the first delivery created rather
	// than reserving the same entitlement twice.
	IdempotencyKey string
}

// StayAuthorizationRef is the hold, as this package needs it.
type StayAuthorizationRef struct {
	ID uuid.UUID
	// ApprovedDays is what the authorization actually promised, which is not always what
	// the stay asked for: a reviewer may approve four of the five days requested, and the
	// reconciliation has to compare against what was promised.
	ApprovedDays string
	ValidTo      time.Time
	RowVersion   int64
}

// StayReleaseInput gives back days that were reserved and never used.
type StayReleaseInput struct {
	TenantID        uuid.UUID
	ActorID         uuid.UUID
	AuthorizationID uuid.UUID
	// Days is the ceiling, not the amount: the port releases what is still outstanding up
	// to this, so a hold something else has already consumed is not released twice.
	Days string
	// ReasonCode names the movement in the ledger, and is part of its idempotency key, so
	// a discharge and a cancellation of the same line are two movements and either of them
	// run twice is still one.
	ReasonCode string
}

// AuthorizationPort is WP-I4-02, narrowed to the three things an admission does with a
// hold: take one when the request is approved, move its end when an extension is approved,
// and give back what was never used at discharge.
//
// Create and Extend open transactions of their own, because they are the authorization
// module's own commands with their own audit rows and ledger movements. Release takes the
// caller's transaction: the discharge and the release it owes have to commit together, or a
// stay would end with entitlement still held for days nobody spent.
type AuthorizationPort interface {
	CreateForRequest(ctx context.Context, rc identity.RequestContext,
		in StayAuthorizationInput) (StayAuthorizationRef, error)
	ExtendValidity(ctx context.Context, rc identity.RequestContext, authorizationID uuid.UUID,
		validTo time.Time, reasonCode string) error
	ReleaseUnused(ctx context.Context, tx pgx.Tx, in StayReleaseInput) (string, error)
}

// NoRequests and NoAuthorizations are the defaults of the two ports above. A process wired
// with neither can read stays and can create none, which is the honest behaviour of a
// deployment that has not been given the request and authorization services — and is what
// the expiry-only scheduler process actually is.
type NoRequests struct{}

// CreatePreauthorization implements RequestPort.
func (NoRequests) CreatePreauthorization(context.Context, pgx.Tx, identity.RequestContext,
	StayRequestInput,
) (StayRequestRef, error) {
	return StayRequestRef{}, errors.New("health: this process cannot raise a preauthorization request")
}

// NoAuthorizations is the default AuthorizationPort.
type NoAuthorizations struct{}

// CreateForRequest implements AuthorizationPort.
func (NoAuthorizations) CreateForRequest(context.Context, identity.RequestContext,
	StayAuthorizationInput,
) (StayAuthorizationRef, error) {
	return StayAuthorizationRef{}, errors.New("health: this process cannot create an authorization")
}

// ExtendValidity implements AuthorizationPort.
func (NoAuthorizations) ExtendValidity(context.Context, identity.RequestContext, uuid.UUID,
	time.Time, string,
) error {
	return errors.New("health: this process cannot extend an authorization")
}

// ReleaseUnused implements AuthorizationPort.
func (NoAuthorizations) ReleaseUnused(context.Context, pgx.Tx, StayReleaseInput) (string, error) {
	return "", errors.New("health: this process cannot release an authorization")
}
