// Package application implements the benefit use cases: programs and plans, versioned
// plan configurations published through maker-checker, entitlement definitions and
// enrollments. Transactions are opened here with db.WithTenantTx so a change, its audit
// row and its outbox event commit together.
package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Errors mapped by the transport layer to problem+json codes.
var (
	ErrNotFound             = errors.New("benefit: resource not found")
	ErrProgramNotFound      = errors.New("benefit: program not found")
	ErrPlanNotFound         = errors.New("benefit: plan not found")
	ErrPlanVersionNotFound  = errors.New("benefit: plan version not found")
	ErrEnrollmentNotFound   = errors.New("benefit: enrollment not found")
	ErrProgramCodeTaken     = errors.New("benefit: program code already used in this tenant")
	ErrPlanCodeTaken        = errors.New("benefit: plan code already used in this program")
	ErrProgramTransition    = errors.New("benefit: program status transition not allowed")
	ErrPlanTransition       = errors.New("benefit: plan status transition not allowed")
	ErrVersionTransition    = errors.New("benefit: plan version status transition not allowed")
	ErrVersionImmutable     = errors.New("benefit: published plan version is immutable")
	ErrVersionOverlap       = errors.New("benefit: published plan version period overlaps another")
	ErrVersionNumberTaken   = errors.New("benefit: plan version number already exists")
	ErrEnrollmentOverlap    = errors.New("benefit: enrollment period overlaps an existing one")
	ErrEnrollmentTransition = errors.New("benefit: enrollment status transition not allowed")
	ErrVersionMismatch      = errors.New("benefit: row version does not match If-Match")
	ErrMakerCheckerSame     = errors.New("benefit: the publisher must differ from the submitter")
	ErrCatalogEntryNotFound = errors.New("benefit: catalog entry not found")
	// ErrNoPublishedVersion is returned by ResolvePlanVersion when the plan has no
	// PUBLISHED version covering the requested date.
	ErrNoPublishedVersion = errors.New("benefit: no published plan version for the given date")
)

// CatalogRow is one benefit.program_type row.
type CatalogRow struct {
	Code        string
	DisplayName string
	Status      string
}

// OrganizationRow is the tenant's relationship with a sponsor or payer organization.
type OrganizationRow struct {
	ID          uuid.UUID
	Role        string
	Status      string
	DisplayName string
}

// ProgramRow is benefit.program as stored, with the two organization display names and
// the number of plans underneath.
type ProgramRow struct {
	ID                    uuid.UUID
	Code                  string
	Name                  string
	ProgramType           string
	Status                string
	SponsorOrganizationID uuid.UUID
	PayerOrganizationID   uuid.UUID
	SponsorDisplayName    string
	PayerDisplayName      string
	ValidFrom             *time.Time
	ValidTo               *time.Time
	PlanCount             int64
	CreatedAt             time.Time
	RowVersion            int64
}

// NewProgramRow is the insert payload of benefit.program.
type NewProgramRow struct {
	TenantID              uuid.UUID
	SponsorOrganizationID uuid.UUID
	PayerOrganizationID   uuid.UUID
	Code                  string
	Name                  string
	ProgramType           string
	ValidFrom             *time.Time
	ValidTo               *time.Time
}

// ProgramUpdateRow is the full new state of a program after a merge-patch.
type ProgramUpdateRow struct {
	Name      string
	Status    string
	ValidFrom *time.Time
	ValidTo   *time.Time
	Expected  int64
}

// ProgramListQuery is the repository-level program filter.
type ProgramListQuery struct {
	Status   string
	Pattern  string
	After    *httpx.Cursor
	PageSize int
}

// PlanRow is benefit.plan as stored.
type PlanRow struct {
	ID         uuid.UUID
	ProgramID  uuid.UUID
	Code       string
	Name       string
	Status     string
	CreatedAt  time.Time
	RowVersion int64
}

// NewPlanRow is the insert payload of benefit.plan.
type NewPlanRow struct {
	TenantID  uuid.UUID
	ProgramID uuid.UUID
	Code      string
	Name      string
}

// PlanUpdateRow is the full new state of a plan after a merge-patch.
type PlanUpdateRow struct {
	Name     string
	Status   string
	Expected int64
}

// PlanVersionRow is benefit.plan_version as stored. RowVersion is the row_version
// counter migration 000016 added; platform.tg_touch_row owns it, and it is exposed as the
// contract's rowVersion and ETag.
type PlanVersionRow struct {
	ID                uuid.UUID
	PlanID            uuid.UUID
	VersionNo         int
	Status            string
	ValidFrom         *time.Time
	ValidTo           *time.Time
	ConfigurationHash []byte
	PublishedAt       *time.Time
	PublishedBy       *uuid.UUID
	SubmittedAt       *time.Time
	SubmittedBy       *uuid.UUID
	ReviewComment     *string
	RetireReasonCode  *string
	RetireReasonText  *string
	Notes             *string
	CreatedAt         time.Time
	RowVersion        int64
}

// NewPlanVersionRow is the insert payload of benefit.plan_version.
type NewPlanVersionRow struct {
	TenantID  uuid.UUID
	PlanID    uuid.UUID
	ActorID   uuid.UUID
	VersionNo int
	ValidFrom *time.Time
	ValidTo   *time.Time
	Notes     *string
}

// PlanVersionDraftRow is the full new state of a draft version after a merge-patch.
type PlanVersionDraftRow struct {
	ValidFrom *time.Time
	ValidTo   *time.Time
	Notes     *string
}

// SubmitRow, PublishRow and RetireRow are the three review commands.
type (
	// SubmitRow moves a draft to UNDER_REVIEW.
	SubmitRow struct {
		ActorID uuid.UUID
		Comment *string
	}
	// PublishRow freezes the configuration hash on a version under review.
	PublishRow struct {
		ActorID           uuid.UUID
		ConfigurationHash []byte
		Comment           *string
	}
	// RetireRow closes a published version with a reason.
	RetireRow struct {
		ReasonCode string
		ReasonText *string
	}
)

// DefinitionRow is one benefit.entitlement_definition row.
type DefinitionRow struct {
	ID         uuid.UUID
	Status     string
	Definition domain.EntitlementDefinition
}

// NewDefinitionRow is the insert payload of benefit.entitlement_definition.
type NewDefinitionRow struct {
	TenantID      uuid.UUID
	PlanVersionID uuid.UUID
	Definition    domain.EntitlementDefinition
}

// MembershipRow is the part of party.sponsor_membership an enrollment needs.
type MembershipRow struct {
	ID        uuid.UUID
	PersonID  uuid.UUID
	Status    string
	ValidFrom *time.Time
	ValidTo   *time.Time
}

// EnrollmentRow is benefit.enrollment joined with its membership and plan.
type EnrollmentRow struct {
	ID                  uuid.UUID
	PersonID            uuid.UUID
	SponsorMembershipID uuid.UUID
	PlanID              uuid.UUID
	ProgramID           uuid.UUID
	PlanCode            string
	Status              string
	ValidFrom           time.Time
	ValidTo             *time.Time
	EnrollmentReason    *string
	SourceSystem        *string
	CreatedAt           time.Time
	RowVersion          int64
}

// NewEnrollmentRow is the insert payload of benefit.enrollment.
type NewEnrollmentRow struct {
	TenantID            uuid.UUID
	SponsorMembershipID uuid.UUID
	PlanID              uuid.UUID
	Status              string
	ValidFrom           time.Time
	ValidTo             *time.Time
	EnrollmentReason    *string
}

// EnrollmentUpdateRow is the full new state of an enrollment after a merge-patch.
type EnrollmentUpdateRow struct {
	Status   string
	ValidTo  *time.Time
	Expected int64
}

// DefinitionCode is one entitlement definition of a version, reduced to what a mapping
// needs: the id it points at and the code it is named by.
type DefinitionCode struct {
	ID       uuid.UUID
	Code     string
	UnitType string
}

// EnrollmentListQuery is the repository-level enrollment filter.
type EnrollmentListQuery struct {
	PlanID   uuid.UUID
	Status   string
	After    *httpx.Cursor
	PageSize int
}

// Repository is the persistence port; every method runs inside the caller's transaction.
type Repository interface {
	GetProgramType(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, code string) (CatalogRow, error)
	GetOrganization(ctx context.Context, tx pgx.Tx, tenantID, organizationID uuid.UUID) (OrganizationRow, error)

	CreateProgram(ctx context.Context, tx pgx.Tx, in NewProgramRow) (uuid.UUID, error)
	GetProgram(ctx context.Context, tx pgx.Tx, tenantID, programID uuid.UUID) (ProgramRow, error)
	ListPrograms(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q ProgramListQuery) ([]ProgramRow, error)
	UpdateProgram(ctx context.Context, tx pgx.Tx, tenantID, programID uuid.UUID, in ProgramUpdateRow) error

	CreatePlan(ctx context.Context, tx pgx.Tx, in NewPlanRow) (uuid.UUID, error)
	GetPlan(ctx context.Context, tx pgx.Tx, tenantID, planID uuid.UUID) (PlanRow, error)
	ListPlans(ctx context.Context, tx pgx.Tx, tenantID, programID uuid.UUID) ([]PlanRow, error)
	UpdatePlan(ctx context.Context, tx pgx.Tx, tenantID, planID uuid.UUID, in PlanUpdateRow) error

	NextVersionNo(ctx context.Context, tx pgx.Tx, tenantID, planID uuid.UUID) (int, error)
	CreatePlanVersion(ctx context.Context, tx pgx.Tx, in NewPlanVersionRow) (uuid.UUID, error)
	GetPlanVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (PlanVersionRow, error)
	// LockPlanVersion reads the row FOR UPDATE so a state command can compare the
	// concurrency token and write without a lost update.
	LockPlanVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (PlanVersionRow, error)
	ListPlanVersions(ctx context.Context, tx pgx.Tx, tenantID, planID uuid.UUID) ([]PlanVersionRow, error)
	UpdatePlanVersionDraft(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, in PlanVersionDraftRow) error
	TouchPlanVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) error
	SubmitPlanVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, in SubmitRow) error
	PublishPlanVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, in PublishRow) error
	RetirePlanVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, in RetireRow) error

	ListDefinitions(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) ([]DefinitionRow, error)
	DeleteDefinitions(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (int64, error)
	CreateDefinition(ctx context.Context, tx pgx.Tx, in NewDefinitionRow) (uuid.UUID, error)

	// The service → entitlement mapping of a plan version (WP-I5-05). ListMappings and
	// DeleteMappings are the two halves of a set replacement; ListMappableDefinitions is
	// what an entitlement code is resolved against, so a code from another version is a
	// field error rather than a foreign key violation.
	ListMappings(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) ([]MappingRow, error)
	DeleteMappings(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (int64, error)
	CreateMapping(ctx context.Context, tx pgx.Tx, in NewMappingRow) (uuid.UUID, error)
	ListMappableDefinitions(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) ([]DefinitionCode, error)
	ListServiceDefinitionsByID(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, ids []uuid.UUID) ([]ServiceDefinitionRow, error)

	// PersonExists answers the 404 of the person-scoped enrollment routes without
	// reading any personal column.
	PersonExists(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID) (bool, error)
	GetMembership(ctx context.Context, tx pgx.Tx, tenantID, membershipID uuid.UUID) (MembershipRow, error)
	CreateEnrollment(ctx context.Context, tx pgx.Tx, in NewEnrollmentRow) (uuid.UUID, error)
	GetEnrollment(ctx context.Context, tx pgx.Tx, tenantID, enrollmentID uuid.UUID) (EnrollmentRow, error)
	ListPersonEnrollments(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID) ([]EnrollmentRow, error)
	ListEnrollments(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q EnrollmentListQuery) ([]EnrollmentRow, error)
	UpdateEnrollment(ctx context.Context, tx pgx.Tx, tenantID, enrollmentID uuid.UUID, in EnrollmentUpdateRow) error
}
