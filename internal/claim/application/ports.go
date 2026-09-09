// Package application implements the claim use cases: opening a draft, replacing its lines,
// submitting it through the pipeline, deciding it line by line at two stages, finishing it,
// correcting it as a new version, and answering whether an invoice could be raised from it.
// Transactions are opened here with db.WithTenantTx, so a write, its audit row, the access
// event it owes and the notification it publishes commit together and RLS is bound for every
// statement.
//
// Four things this package never does.
//
// It never edits a submitted version. Every write command's precondition is a status the
// database checks, and a correction opens version n+1 with the lines copied — so the decision
// that was made about version n is still readable and still says what it said.
//
// It never decides a line without saying who decided it and why. `claim.line_decision` is
// append-only and carries a reason code, a stage and — except at the AUTO stage, where the
// rules decided and nobody looked — an actor.
//
// It never re-implements arithmetic that lives somewhere else. The price comes from
// WP-I3-05's ladder, the rules from WP-I3-04's engine, the hold from WP-I4-02, the report
// coverage from WP-I5-02 and the projection from WP-I5-01's own `decide`. A second copy of
// any of them would be a second answer, and the second answer is the one that reaches a
// member's screen.
//
// And it never hands a clinical field to a caller who has not earned it. The projection is
// applied in one function, on the record, before anything maps it to the wire.
package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	healthapp "github.com/celikbros/kapsora/internal/health/application"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/pricing"
)

// Permissions guarding claim work. All six are in the catalogue: five from migration 000008
// and `claim.cancel` from 000034. They live here rather than in the transport because who may
// decide a claim on clinical grounds is a business rule, not a routing detail.
const (
	PermissionRead            = "claim.read"
	PermissionCreate          = "claim.create"
	PermissionSubmit          = "claim.submit"
	PermissionMedicalReview   = "claim.medical.review"
	PermissionFinancialReview = "claim.financial.review"
	PermissionCancel          = "claim.cancel"
)

// The clinical grants the projection asks about. They are WP-I5-01's, named again here only
// so this package does not have to import the health package to read a permission string.
const (
	PermissionClinicalRead  = healthapp.PermissionClinicalRead
	PermissionSensitiveRead = healthapp.PermissionSensitiveRead
)

// ScopeOrganization is the iam.access_grant.scope_type of provider-side roles (v1.2 6.3).
const ScopeOrganization = "ORGANIZATION"

// Projection and AccessRequest are WP-I5-01's, re-exported so a caller of this package does
// not need to know which module owns the clinical visibility rules — only that there is one.
type (
	// Projection is which half of a claim a caller has earned.
	Projection = healthapp.Projection
	// AccessRequest is why a caller is opening clinical data, as it arrived on the request.
	AccessRequest = healthapp.AccessRequest
)

// The two projections, under this package's names.
const (
	ProjectionClinical  = healthapp.ProjectionClinical
	ProjectionFinancial = healthapp.ProjectionFinancial
)

// Errors mapped by the transport layer to problem codes.
var (
	ErrClaimNotFound = errors.New("claim: claim not found")
	// ErrVersionFrozen is the freeze the whole package exists for: a claim whose current
	// version has been submitted is not editable, and a correction is a new version.
	ErrVersionFrozen = errors.New("claim: the current version has been submitted and is frozen")
	// ErrVersionNotFound is a version number this claim has never had.
	ErrVersionNotFound   = errors.New("claim: claim version not found")
	ErrVersionMismatch   = errors.New("claim: row version does not match If-Match")
	ErrTransitionInvalid = errors.New("claim: the claim is not in a state this command can run from")
	// ErrStageMismatch is a reviewer decising at the wrong stage — a financial reviewer
	// reaching a claim that is still in medical review. It is what makes medical review
	// precede financial rather than merely usually happen first.
	ErrStageMismatch = errors.New("claim: the claim is not waiting for this review stage")
	// ErrLineUndecided refuses an approval while a line of the current version still has no
	// decision. A claim approved with an undecided line is a claim nobody can invoice.
	ErrLineUndecided = errors.New("claim: a line of the current version has no decision")
	// ErrLineNotFound is a decision naming a line number this version does not have.
	ErrLineNotFound = errors.New("claim: the version has no such line")
	// ErrApprovalNotPermitted is the approval policy refusing this caller's stage.
	ErrApprovalNotPermitted = errors.New("claim: the approval policy does not admit this reviewer")
	// ErrNotDecided refuses an invoice readiness question about a claim nobody has answered.
	ErrNotDecided = errors.New("claim: the claim has not been decided")
	// ErrAdjustmentNotFound is a reversal naming an adjustment this claim does not have.
	ErrAdjustmentNotFound = errors.New("claim: adjustment not found")
	// ErrAdjustmentReversed refuses a second reversal of one adjustment. Two reversals would
	// give the money back twice, and the approved total would depend on how many times
	// somebody pressed the button.
	ErrAdjustmentReversed = errors.New("claim: the adjustment has already been reversed")
	// ErrAdjustmentNotReversible refuses a reversal of a reversal. It is not a redo: it is a
	// reader having to walk a chain of unknown length to find out what a claim is worth.
	ErrAdjustmentNotReversible = errors.New("claim: a reversal cannot itself be reversed")
	// ErrAdjustmentCurrency refuses an adjustment denominated differently from the claim.
	ErrAdjustmentCurrency = errors.New("claim: the adjustment is not in the claim's currency")
	// ErrAdjustmentLine is an adjustment naming a line of another claim, or of a version this
	// claim is no longer on.
	ErrAdjustmentLine = errors.New("claim: the line does not belong to this claim's current version")
	ErrProviderScope  = errors.New("claim: the caller is not scoped to this provider")
	// ErrBookingNotFound is a lodging event naming a stay this tenant does not have.
	ErrBookingNotFound = errors.New("claim: booking not found")
	// ErrClaimAlreadyRaised is the second delivery of a source event finding the claim the
	// first delivery made. It is not a failure — it is the guarantee working.
	ErrClaimAlreadyRaised = errors.New("claim: a live claim already exists for this source")
	ErrProviderUnknown    = errors.New("claim: the organization is not a provider of this tenant")
	ErrProviderNoProfile  = errors.New("claim: the provider organization has no provider profile")
	ErrEnrollmentMismatch = errors.New("claim: the enrollment does not belong to this person or program")
	ErrServiceUnknown     = errors.New("claim: a line names a service the catalogue does not have")
	ErrLineRequired       = errors.New("claim: a claim cannot be submitted with no line")
	// ErrReferenceCollision is the reference generator losing a race; the caller retries.
	ErrReferenceCollision = errors.New("claim: reference already exists")
	// ErrAccessPurposeRequired is a sensitive-case read without a stated purpose.
	ErrAccessPurposeRequired = healthapp.ErrAccessPurposeRequired
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

// ClaimRecord is one claim.claim row, as the database holds it. Every read returns the whole
// row including ReviewCommentMedical; the projection drops what the caller may not have.
type ClaimRecord struct {
	ID                     uuid.UUID
	Reference              string
	PersonID               uuid.UUID
	ProgramID              uuid.UUID
	EnrollmentID           uuid.UUID
	ProviderOrganizationID uuid.UUID
	DomainCode             string
	// SourceType and SourceID are what the claim came from: a health case, a booking or a
	// reimbursement request (migration 000043). They are nil together on a claim raised by
	// hand against nothing, which is an ordinary claim.
	SourceType       *string
	SourceID         *uuid.UUID
	CaseID           *uuid.UUID
	FulfilmentID     *uuid.UUID
	AuthorizationID  *uuid.UUID
	CurrentVersionNo int
	Status           string
	ServiceDateFrom  time.Time
	ServiceDateTo    time.Time
	Channel          string
	RejectReasonCode *string
	ReturnReasonCode *string
	// ReviewCommentMedical is nil once the financial projection has been applied.
	ReviewCommentMedical   *string
	ReviewCommentFinancial *string
	ClosedAt               *time.Time
	CreatedAt              time.Time
	RowVersion             int64
}

// VersionRecord is one claim.claim_version row.
type VersionRecord struct {
	ID               uuid.UUID
	ClaimID          uuid.UUID
	VersionNo        int
	Status           string
	SubmittedAt      *time.Time
	SubmittedBy      *uuid.UUID
	ReturnedAt       *time.Time
	ReturnedBy       *uuid.UUID
	ReturnReasonCode *string
	ReturnReasonText *string
	Snapshot         []byte
	CreatedAt        time.Time
	RowVersion       int64
}

// LineRecord is one claim.claim_line row with the catalogue code it names. Three of its
// fields are nil once the financial projection has been applied: DiagnosisID,
// MedicalReportID and Description.
type LineRecord struct {
	ID                  uuid.UUID
	VersionID           uuid.UUID
	LineNo              int
	ServiceDefinitionID uuid.UUID
	ServiceCode         *string
	UnitType            string
	Quantity            string
	UnitAmount          *string
	LineAmount          string
	CurrencyCode        string
	DiagnosisID         *uuid.UUID
	MedicalReportID     *uuid.UUID
	PractitionerID      *uuid.UUID
	Description         *string
	CreatedAt           time.Time
	RowVersion          int64
}

// DecisionRecord is one claim.line_decision row.
type DecisionRecord struct {
	ID                 uuid.UUID
	LineID             uuid.UUID
	DecidedInVersionNo int
	Decision           string
	ApprovedQuantity   string
	ApprovedAmount     string
	ContractAmount     *string
	PayerAmount        string
	MemberAmount       string
	ReasonCode         string
	// ReasonText is nil once the financial projection has been applied to a MEDICAL
	// decision: a medical reviewer's sentence is clinical whatever the figure beside it is.
	ReasonText *string
	DecidedBy  *uuid.UUID
	DecidedAt  time.Time
	Stage      string
}

// AdjustmentRecord is one claim.adjustment row.
//
// It is a ledger line: money that moved for a reason that is not a line decision, with the
// split it moved in, the line it belongs to when it belongs to one, and the row it came from
// when the system wrote it. Nothing here is ever edited — an adjustment taken back is a
// REVERSAL naming it, and both rows stay.
type AdjustmentRecord struct {
	ID             uuid.UUID
	ClaimID        uuid.UUID
	VersionNo      int
	ClaimLineID    *uuid.UUID
	AdjustmentType string
	Amount         string
	PayerAmount    string
	MemberAmount   string
	CurrencyCode   string
	ReasonCode     string
	ReasonText     *string
	SourceType     string
	SourceID       *uuid.UUID
	// ReversesAdjustmentID is what this row takes back. It is set exactly on a REVERSAL.
	ReversesAdjustmentID *uuid.UUID
	// CreatedBy is nil exactly for the two system sources: a cancellation or no-show fee is
	// written by an outbox handler and no person is behind it.
	CreatedBy *uuid.UUID
	CreatedAt time.Time
}

// NewClaimRow is the insert payload of a claim header.
type NewClaimRow struct {
	Reference              string
	PersonID               uuid.UUID
	ProgramID              uuid.UUID
	EnrollmentID           uuid.UUID
	ProviderOrganizationID uuid.UUID
	DomainCode             string
	SourceType             *string
	SourceID               *uuid.UUID
	CaseID                 *uuid.UUID
	FulfilmentID           *uuid.UUID
	AuthorizationID        *uuid.UUID
	ServiceDateFrom        time.Time
	ServiceDateTo          time.Time
	Channel                string
	ActorID                *uuid.UUID
}

// DraftRow is the header patch of a claim whose current version is still a draft.
type DraftRow struct {
	ServiceDateFrom time.Time
	ServiceDateTo   time.Time
	Channel         string
	CaseID          *uuid.UUID
	FulfilmentID    *uuid.UUID
	AuthorizationID *uuid.UUID
	ActorID         *uuid.UUID
}

// StatusRow is one move through the lifecycle. Every column the move owns is written, so a
// stale return reason can never survive a transition.
type StatusRow struct {
	Status           string
	CurrentVersionNo int
	RejectReasonCode *string
	ReturnReasonCode *string
	ClosedAt         *time.Time
	FromStatuses     []string
	ActorID          *uuid.UUID
}

// NewLineRow is the insert payload of one line.
type NewLineRow struct {
	VersionID           uuid.UUID
	LineNo              int
	ServiceDefinitionID uuid.UUID
	UnitType            string
	Quantity            string
	UnitAmount          *string
	LineAmount          string
	CurrencyCode        string
	DiagnosisID         *uuid.UUID
	MedicalReportID     *uuid.UUID
	PractitionerID      *uuid.UUID
	Description         *string
	ActorID             *uuid.UUID
}

// NewDecisionRow is the insert payload of one line decision.
type NewDecisionRow struct {
	LineID             uuid.UUID
	DecidedInVersionNo int
	Decision           string
	ApprovedQuantity   string
	ApprovedAmount     string
	ContractAmount     *string
	PayerAmount        string
	MemberAmount       string
	ReasonCode         string
	ReasonText         *string
	DecidedBy          *uuid.UUID
	DecidedAt          time.Time
	Stage              string
}

// NewAdjustmentRow is the insert payload of one adjustment.
type NewAdjustmentRow struct {
	ClaimID              uuid.UUID
	VersionNo            int
	ClaimLineID          *uuid.UUID
	AdjustmentType       string
	Amount               string
	PayerAmount          string
	MemberAmount         string
	CurrencyCode         string
	ReasonCode           string
	ReasonText           *string
	SourceType           string
	SourceID             *uuid.UUID
	ReversesAdjustmentID *uuid.UUID
	ActorID              *uuid.UUID
}

// FreezeRow is the snapshot a submit writes onto the version it froze.
type FreezeRow struct {
	Snapshot    []byte
	SubmittedAt time.Time
	ActorID     *uuid.UUID
}

// ReturnRow is what a return writes onto the version it sent back.
type ReturnRow struct {
	ReturnedAt time.Time
	ReasonCode string
	ReasonText *string
	ActorID    *uuid.UUID
}

// ClaimQuery is the repository-level claim filter.
type ClaimQuery struct {
	Scope                  Scope
	PersonID               *uuid.UUID
	CaseID                 *uuid.UUID
	ProviderOrganizationID *uuid.UUID
	Status                 string
	ServiceDateFrom        *time.Time
	ServiceDateTo          *time.Time
	After                  *httpx.Cursor
	PageSize               int
}

// EnrollmentRecord is what the create command needs to check that a claim's person, program
// and enrollment belong together.
type EnrollmentRecord struct {
	ID        uuid.UUID
	PlanID    uuid.UUID
	ProgramID uuid.UUID
	Status    string
	PersonID  uuid.UUID
}

// ServiceDefinitionRecord is one catalogue definition a line names.
type ServiceDefinitionRecord struct {
	ID              uuid.UUID
	Code            string
	Name            string
	DefaultUnitType string
	Active          bool
}

// ProviderProfileRecord is the provider profile behind a claim's provider organization. The
// pricing ladder is asked about a profile because that is what a contract is signed with.
type ProviderProfileRecord struct {
	ID                   uuid.UUID
	TenantOrganizationID uuid.UUID
	Status               string
}

// DuplicateRecord names the other claim a duplicate check found. The reference travels with
// it because "we think you have already billed this" is only actionable if the provider is
// told which one.
type DuplicateRecord struct {
	ClaimID   uuid.UUID
	Reference string
}

// BookingRecord is the stay a lodging claim is raised from, as much of it as the claim needs.
// It carries no guest name, no room number and no voucher: a claim is a bill, and the only
// person on it is the member the plan already names.
type BookingRecord struct {
	ID                     uuid.UUID
	Reference              string
	PersonID               uuid.UUID
	ProgramID              uuid.UUID
	EnrollmentID           uuid.UUID
	RoomTypeID             uuid.UUID
	ServiceDefinitionID    uuid.UUID
	ProviderOrganizationID uuid.UUID
	Status                 string
	CheckIn                time.Time
	CheckOut               time.Time
	Nights                 int
	// ActualNights is what the check-out counted on the property's own clock. It is nil
	// until a stay is checked out, which is the honest answer for a booking nobody has left
	// yet.
	ActualNights    *int
	OverBooking     bool
	AuthorizationID *uuid.UUID
	CheckedOutAt    *time.Time
	CancelledAt     *time.Time
	// CoveredNights is how many of the stay's nights the plan carries, read back from the
	// booking's own frozen quote. It is what decides which lines are already approved.
	CoveredNights int
}

// BookingNightRecord is one night of the stay at the amounts the booking froze. The claim
// copies all three and computes none of them.
type BookingNightRecord struct {
	StayDate     time.Time
	UnitAmount   string
	PayerAmount  string
	MemberAmount string
	CurrencyCode string
}

// BookingFeeRecord is a cancellation or no-show fee as its own row already carries it: the
// assessed amount and the split, which the claim line and the adjustment both copy.
type BookingFeeRecord struct {
	ID           uuid.UUID
	Status       string
	Free         bool
	Amount       string
	PayerAmount  string
	MemberAmount string
	CurrencyCode string
}

// EarningsQuery is the provider's earnings question at the repository level.
type EarningsQuery struct {
	ProviderOrganizationID uuid.UUID
	// From and To bound the moment the claim was decided, not the day the service was
	// delivered: what a provider earned in March is what was decided in March.
	From         *time.Time
	To           *time.Time
	CurrencyCode string
}

// EarningClaimRecord is one decided claim of a provider, with everything the earnings view
// adds up. Every figure is exact decimal text and is summed once, in the service.
type EarningClaimRecord struct {
	ClaimID               uuid.UUID
	Reference             string
	Status                string
	DomainCode            string
	CurrencyCode          string
	DecidedAt             time.Time
	LineTotal             string
	LinePayerTotal        string
	LineMemberTotal       string
	AdjustmentTotal       string
	AdjustmentPayerTotal  string
	AdjustmentMemberTotal string
	// OnLiveInvoice is whether the claim already sits on an invoice that is still live
	// (WP-I7-02). It is the half a status cannot answer: a claim allocated to a *draft*
	// invoice is still APPROVED, and offering it again as invoiceable is how the same money
	// ends up on two documents.
	OnLiveInvoice bool
}

// Repository is the persistence port; every method runs inside the caller's transaction,
// which db.WithTenantTx has already bound to the tenant so RLS is active. The provider
// boundary is a repository concern too: the scope is passed down rather than checked above,
// so a claim outside it is genuinely not there rather than fetched and then hidden.
type Repository interface {
	CreateClaim(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewClaimRow) (ClaimRecord, error)
	GetClaim(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (ClaimRecord, error)
	// LockClaim reads the row FOR UPDATE, so two commands on one claim serialise.
	LockClaim(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (ClaimRecord, error)
	ListClaims(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q ClaimQuery) ([]ClaimRecord, error)
	// UpdateDraft carries the freeze in its predicate: a claim whose current version has
	// been submitted matches no row, and the service answers 409 rather than editing it.
	UpdateDraft(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in DraftRow, expected int64) (bool, error)
	// TouchClaim moves the row_version without changing a field, so a line replacement
	// hands the caller a fresh ETag.
	TouchClaim(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, actorID *uuid.UUID, expected int64) (bool, error)
	SetStatus(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in StatusRow, expected int64) (bool, error)
	// SetInvoiceStatus is the transition WP-I7-02 causes: onto an invoice and back off one.
	// It carries no expected row version because the concurrency control of that move is the
	// invoice's If-Match and the row lock the invoice command already holds -- demanding a
	// claim's ETag as well would make an invoice covering fifty claims unsubmittable whenever
	// a reviewer had touched any one of them. `fromStatuses` is the whole precondition.
	SetInvoiceStatus(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, status string,
		fromStatuses []string, actorID *uuid.UUID) (bool, error)
	SetReviewComment(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, stage string,
		comment *string, actorID *uuid.UUID) error

	CreateVersion(ctx context.Context, tx pgx.Tx, tenantID, claimID uuid.UUID, versionNo int,
		actorID *uuid.UUID) (VersionRecord, error)
	GetDraftVersion(ctx context.Context, tx pgx.Tx, tenantID, claimID uuid.UUID) (VersionRecord, error)
	GetVersion(ctx context.Context, tx pgx.Tx, tenantID, claimID uuid.UUID, versionNo int) (VersionRecord, error)
	ListVersions(ctx context.Context, tx pgx.Tx, tenantID, claimID uuid.UUID) ([]VersionRecord, error)
	FreezeVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, in FreezeRow) (bool, error)
	SupersedeVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, in ReturnRow) (bool, error)

	ReplaceLines(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, rows []NewLineRow) error
	ListLines(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) ([]LineRecord, error)

	CreateDecision(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewDecisionRow) (DecisionRecord, error)
	// ListLatestDecisions returns the head of each line's append-only decision history.
	ListLatestDecisions(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) ([]DecisionRecord, error)
	CountDecisions(ctx context.Context, tx pgx.Tx, tenantID, lineID uuid.UUID) (int, error)

	CreateAdjustment(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewAdjustmentRow) (AdjustmentRecord, error)
	ListAdjustments(ctx context.Context, tx pgx.Tx, tenantID, claimID uuid.UUID) ([]AdjustmentRecord, error)
	GetAdjustment(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (AdjustmentRecord, error)

	// FindLiveClaimBySource is the first half of every source-driven handler's idempotency:
	// a claim already raised for this stay is the answer, and `uq_claim_live_booking` is the
	// half that holds when two deliveries look at the same moment.
	FindLiveClaimBySource(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, sourceType string,
		sourceID uuid.UUID) (uuid.UUID, bool, error)
	// BookingForClaim reads the stay a lodging claim is raised from. Every amount it brings
	// back was frozen by the booking; nothing in this package re-prices a night.
	BookingForClaim(ctx context.Context, tx pgx.Tx, tenantID, bookingID uuid.UUID) (BookingRecord, error)
	BookingNights(ctx context.Context, tx pgx.Tx, tenantID, bookingID uuid.UUID) ([]BookingNightRecord, error)
	BookingNoShow(ctx context.Context, tx pgx.Tx, tenantID, bookingID uuid.UUID) (BookingFeeRecord, bool, error)
	BookingCancellation(ctx context.Context, tx pgx.Tx, tenantID, bookingID uuid.UUID) (BookingFeeRecord, bool, error)

	// ProviderEarningClaims answers one row per decided claim of a provider, with the line
	// totals and the adjustment totals the earnings view adds up per currency.
	ProviderEarningClaims(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
		q EarningsQuery) ([]EarningClaimRecord, error)
	ProviderDisplayName(ctx context.Context, tx pgx.Tx, tenantID, organizationID uuid.UUID) (string, error)

	// FindDuplicate is the cross-check of section 2.2 step 3.
	FindDuplicate(ctx context.Context, tx pgx.Tx, tenantID, claimID, personID,
		serviceDefinitionID uuid.UUID, from, to time.Time) (DuplicateRecord, bool, error)
	ProviderHasTaxIdentity(ctx context.Context, tx pgx.Tx, tenantID, organizationID uuid.UUID) (bool, error)
	// CaseSensitivity is what decides the projection. A claim hanging off no case is
	// STANDARD, which is the only honest answer for a claim with no episode of care.
	CaseSensitivity(ctx context.Context, tx pgx.Tx, tenantID, caseID uuid.UUID) (string, error)
	CaseOverAuthorization(ctx context.Context, tx pgx.Tx, tenantID, caseID uuid.UUID) (bool, error)
	ProviderOrganizationExists(ctx context.Context, tx pgx.Tx, tenantID, orgID uuid.UUID) (bool, error)
	ProviderProfile(ctx context.Context, tx pgx.Tx, tenantID, orgID uuid.UUID) (ProviderProfileRecord, error)
	GetEnrollment(ctx context.Context, tx pgx.Tx, tenantID, enrollmentID uuid.UUID) (EnrollmentRecord, error)
	ResolveServiceDefinitions(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
		ids []uuid.UUID) ([]ServiceDefinitionRecord, error)
	// EntitlementCodes maps each service onto the entitlement it draws from, under the plan
	// version in force for this enrollment on the service date (WP-I5-05).
	EntitlementCodes(ctx context.Context, tx pgx.Tx, tenantID, enrollmentID uuid.UUID,
		serviceDate time.Time, ids []uuid.UUID) (map[uuid.UUID]string, error)
	// AccessPurposeExists checks a purpose against health.clinical_access_purpose, which is
	// the authority: a purpose seeded later has to work without a Go release.
	AccessPurposeExists(ctx context.Context, tx pgx.Tx, purpose string) (bool, error)
}

// PricingPort is WP-I3-05's ladder, called inside the submit transaction. It is a port rather
// than a direct dependency so a process that was never given a pricing service says so rather
// than pricing every line at zero.
type PricingPort interface {
	PriceLines(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in PricingRequest) (PricingResult, error)
}

// PricingRequest is one claim's worth of lines to be priced.
type PricingRequest struct {
	PersonID          uuid.UUID
	ProgramID         uuid.UUID
	ProviderProfileID uuid.UUID
	ServiceDate       time.Time
	Items             []PricingItem
}

// PricingItem is one line as the ladder sees it.
type PricingItem struct {
	ServiceDefinitionID uuid.UUID
	Quantity            benefitdomain.Quantity
	RequestedAmount     benefitdomain.Quantity
	// EntitlementCode names the balance this line draws on, resolved from WP-I5-05's
	// service-to-entitlement mapping. It is what caps the payer's share: a line whose
	// entitlement nobody named has no balance to be capped by, and the member would be told
	// they owe the whole bill.
	EntitlementCode string
}

// PricingResult is what the ladder answered, one LineResult per item in order.
type PricingResult struct {
	CurrencyCode string
	Lines        []pricing.LineResult
}

// NoPricing is the default PricingPort: it prices nothing and says so. Every line comes back
// REVIEW_REQUIRED with PRICE_NOT_FOUND, which sends the claim to a person — the honest
// behaviour of a deployment with no pricing service wired.
type NoPricing struct{}

// PriceLines implements PricingPort.
func (NoPricing) PriceLines(_ context.Context, _ pgx.Tx, _ uuid.UUID, in PricingRequest) (PricingResult, error) {
	out := PricingResult{CurrencyCode: "TRY", Lines: make([]pricing.LineResult, 0, len(in.Items))}
	for i := range in.Items {
		out.Lines = append(out.Lines, pricing.LineResult{
			LineNo: i + 1, Outcome: pricing.OutcomeReviewRequired,
			Explanations: []pricing.Explanation{
				{Code: pricing.ExplanationPriceNotFound, Severity: pricing.SeverityError},
			},
		})
	}
	return out, nil
}

// RulesPort evaluates the tenant's published ADJUDICATION rule set over one line.
//
// ADJUDICATION is the purpose the schema calls what the work package calls the CLAIM rule
// set: migration 000022's closed list has no `CLAIM`, and its own comment says an
// ADJUDICATION rule belongs to the claim. Naming a purpose that does not exist would mean no
// rule ever fired.
type RulesPort interface {
	EvaluateClaimLine(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, subjectID uuid.UUID,
		serviceDate time.Time, input map[string]any) (RuleOutcome, error)
}

// RuleAction is one action a matched rule asked for, with the part of its payload this
// package understands.
type RuleAction struct {
	Type     string
	RuleCode string
	// ReasonCode is REJECT's own reason, when it named one.
	ReasonCode string
	// Percent and Amount are PARTIAL_APPROVE's two shapes; exactly one is set.
	Percent string
	Amount  string
}

// RuleOutcome is what the rule sets said about one line.
type RuleOutcome struct {
	// EvaluatedVersions is how many published versions were consulted. Zero means the
	// tenant has published no ADJUDICATION rules at all, which is not an error: a tenant
	// that adjudicates by policy alone is an ordinary tenant.
	EvaluatedVersions int
	Actions           []RuleAction
}

// NoRules is the default RulesPort: no version, no action.
type NoRules struct{}

// EvaluateClaimLine implements RulesPort.
func (NoRules) EvaluateClaimLine(context.Context, pgx.Tx, uuid.UUID, uuid.UUID, time.Time,
	map[string]any,
) (RuleOutcome, error) {
	return RuleOutcome{}, nil
}

// AuthorizationPort is WP-I4-02's hold, as this package is allowed to touch it: draw a line's
// quantity down, and give back what a refused claim was holding. There is no method here that
// would let a claim decide anything about entitlement.
type AuthorizationPort interface {
	Consume(ctx context.Context, tx pgx.Tx, in ConsumeRequest) (ConsumeAnswer, error)
	ReleaseUnused(ctx context.Context, tx pgx.Tx, in ReleaseRequest) (benefitdomain.Quantity, error)
}

// ConsumeRequest is one claim line drawing on the claim's authorization.
type ConsumeRequest struct {
	TenantID            uuid.UUID
	ActorID             uuid.UUID
	AuthorizationID     uuid.UUID
	ServiceDefinitionID uuid.UUID
	Quantity            benefitdomain.Quantity
	// Key is derived from the claim line, so a redelivered submit consumes once.
	Key        string
	ReasonCode string
}

// ConsumeAnswer is what the hold said. OverConsumed means nothing moved: an over-consumption
// is an exception a person looks at, never a silent consume of what happened to be left.
type ConsumeAnswer struct {
	Matched      bool
	Remaining    benefitdomain.Quantity
	Consumed     benefitdomain.Quantity
	OverConsumed bool
}

// ReleaseRequest gives back what a refused claim was holding.
type ReleaseRequest struct {
	TenantID        uuid.UUID
	ActorID         uuid.UUID
	AuthorizationID uuid.UUID
	Quantity        benefitdomain.Quantity
	ReasonCode      string
}

// NoAuthorizations is the default AuthorizationPort: it consumes nothing and matches nothing,
// which is what a claim naming no authorization means anyway.
type NoAuthorizations struct{}

// Consume implements AuthorizationPort.
func (NoAuthorizations) Consume(context.Context, pgx.Tx, ConsumeRequest) (ConsumeAnswer, error) {
	return ConsumeAnswer{Remaining: benefitdomain.ZeroQuantity(), Consumed: benefitdomain.ZeroQuantity()}, nil
}

// ReleaseUnused implements AuthorizationPort.
func (NoAuthorizations) ReleaseUnused(context.Context, pgx.Tx, ReleaseRequest) (benefitdomain.Quantity, error) {
	return benefitdomain.ZeroQuantity(), nil
}

// ReportCoveragePort is WP-I5-02's own port, narrowed to what a claim asks it: may this claim
// lean on this report for this service on this day, and write the usage row that says it did.
type ReportCoveragePort interface {
	ReportCoverage(ctx context.Context, tx pgx.Tx, in healthapp.CoverageRequest) (healthapp.Coverage, error)
}

// NoReportCoverage is the default ReportCoveragePort. It answers "not usable" with a reason
// that names itself, so a claim whose line leans on a report goes to medical review rather
// than being settled against a report nobody checked.
type NoReportCoverage struct{}

// ReportCoverage implements ReportCoveragePort.
func (NoReportCoverage) ReportCoverage(context.Context, pgx.Tx, healthapp.CoverageRequest) (healthapp.Coverage, error) {
	return healthapp.Coverage{Usable: false, ReasonCode: "REPORT_COVERAGE_UNAVAILABLE"}, nil
}

// PolicyPort is WP-I4-03 section 2.4's approval policy: for this action, at this amount, on
// this day, which roles may approve and how many are needed.
type PolicyPort interface {
	Resolve(ctx context.Context, rc identity.RequestContext, in PolicyLookup) (PolicyAnswer, error)
}

// PolicyLookup is the question.
type PolicyLookup struct {
	ActionCode string
	Amount     string
	AsOf       time.Time
}

// PolicyAnswer is the band that matched. Found is false when no band covers the amount, which
// is not a refusal: a tenant that has configured no policy for this action has said nothing
// about it, and the pipeline auto-adjudicates.
type PolicyAnswer struct {
	Found                 bool
	RequiredRoleCodes     []string
	RequiredApproverCount int
}

// NoPolicy is the default PolicyPort: no band, ever.
type NoPolicy struct{}

// Resolve implements PolicyPort.
func (NoPolicy) Resolve(context.Context, identity.RequestContext, PolicyLookup) (PolicyAnswer, error) {
	return PolicyAnswer{}, nil
}

// WorkItemPort raises the work a routed claim is. It is a port rather than a call into the
// worklist service because a work item has to be written in the submit's own transaction: a
// claim routed with no item raised, or an item raised for a submit that rolled back, is work
// nobody can explain.
//
// The title it is given carries the claim's reference and nothing else. A queue is a list
// people read across a room, and a diagnosis on it would be a diagnosis on a wall.
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
// configured a review queue is a deployment where nobody is watching, and refusing the
// submission would not make anybody watch.
type NoWorkItems struct{}

// Raise implements WorkItemPort.
func (NoWorkItems) Raise(context.Context, pgx.Tx, uuid.UUID, RaiseWorkItem) error { return nil }
