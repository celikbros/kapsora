// Package benefitpg implements the benefit repository with sqlc. RLS hides other
// tenants' rows, so an id from another tenant is simply not found.
package benefitpg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// Repository is stateless; every method takes the caller's transaction.
type Repository struct{}

// New returns the repository.
func New() *Repository { return &Repository{} }

var _ application.Repository = (*Repository)(nil)

// SQLSTATE codes mapped to application errors.
const (
	uniqueViolation     = "23505"
	exclusionViolation  = "23P01"
	checkViolation      = "23514"
	integrityConstraint = "23000" // raised by benefit.tg_plan_version_guard
)

// Constraint names from migrations 000004, 000005 and 000015.
const (
	constraintProgramCode   = "uq_program_code_tenant"
	constraintPlanCode      = "uq_plan_code_program"
	constraintVersionNo     = "uq_plan_version_no"
	constraintVersionPeriod = "ex_published_plan_version_period"
	constraintEnrollPeriod  = "ex_enrollment_period"
	constraintMakerChecker  = "ck_plan_version_maker_checker"
)

// GetProgramType implements application.Repository.
func (Repository) GetProgramType(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, code string) (application.CatalogRow, error) {
	row, err := sqlcgen.New(tx).GetProgramType(ctx, sqlcgen.GetProgramTypeParams{TenantID: tenantID, Code: code})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.CatalogRow{}, application.ErrCatalogEntryNotFound
	}
	if err != nil {
		return application.CatalogRow{}, fmt.Errorf("benefit: program type: %w", err)
	}
	return application.CatalogRow{Code: row.Code, DisplayName: row.DisplayName, Status: row.Status}, nil
}

// GetOrganization implements application.Repository.
func (Repository) GetOrganization(ctx context.Context, tx pgx.Tx, tenantID, organizationID uuid.UUID) (application.OrganizationRow, error) {
	row, err := sqlcgen.New(tx).GetBenefitOrganization(ctx, sqlcgen.GetBenefitOrganizationParams{TenantID: tenantID, ID: organizationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.OrganizationRow{}, application.ErrNotFound
	}
	if err != nil {
		return application.OrganizationRow{}, fmt.Errorf("benefit: organization: %w", err)
	}
	return application.OrganizationRow{
		ID: row.ID, Role: row.RelationshipRole, Status: row.Status, DisplayName: row.DisplayName,
	}, nil
}

// CreateProgram implements application.Repository.
func (Repository) CreateProgram(ctx context.Context, tx pgx.Tx, in application.NewProgramRow) (uuid.UUID, error) {
	row, err := sqlcgen.New(tx).CreateProgram(ctx, sqlcgen.CreateProgramParams{
		TenantID: in.TenantID, SponsorOrganizationID: in.SponsorOrganizationID,
		PayerOrganizationID: in.PayerOrganizationID, Code: in.Code, Name: in.Name,
		ProgramType: in.ProgramType, ValidFrom: date(in.ValidFrom), ValidTo: date(in.ValidTo),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == constraintProgramCode {
			return uuid.Nil, application.ErrProgramCodeTaken
		}
		return uuid.Nil, fmt.Errorf("benefit: create program: %w", err)
	}
	return row.ID, nil
}

// GetProgram implements application.Repository.
func (Repository) GetProgram(ctx context.Context, tx pgx.Tx, tenantID, programID uuid.UUID) (application.ProgramRow, error) {
	r, err := sqlcgen.New(tx).GetProgram(ctx, sqlcgen.GetProgramParams{TenantID: tenantID, ID: programID})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ProgramRow{}, application.ErrProgramNotFound
	}
	if err != nil {
		return application.ProgramRow{}, fmt.Errorf("benefit: get program: %w", err)
	}
	return application.ProgramRow{
		ID: r.ID, Code: r.Code, Name: r.Name, ProgramType: r.ProgramType, Status: r.Status,
		SponsorOrganizationID: r.SponsorTenantOrganizationID, PayerOrganizationID: r.PayerTenantOrganizationID,
		SponsorDisplayName: r.SponsorDisplayName, PayerDisplayName: r.PayerDisplayName,
		ValidFrom: datePtr(r.ValidFrom), ValidTo: datePtr(r.ValidTo),
		PlanCount: r.PlanCount, CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}, nil
}

// ListPrograms implements application.Repository.
func (Repository) ListPrograms(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q application.ProgramListQuery) ([]application.ProgramRow, error) {
	params := sqlcgen.ListProgramsParams{
		TenantID: tenantID, Status: optString(q.Status), Q: optString(q.Pattern),
		PageSize: int32(q.PageSize), //nolint:gosec // page size is clamped by httpx.ClampLimit
	}
	if q.After != nil {
		params.CursorCreatedAt = &q.After.CreatedAt
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListPrograms(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("benefit: list programs: %w", err)
	}
	out := make([]application.ProgramRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.ProgramRow{
			ID: r.ID, Code: r.Code, Name: r.Name, ProgramType: r.ProgramType, Status: r.Status,
			SponsorOrganizationID: r.SponsorTenantOrganizationID, PayerOrganizationID: r.PayerTenantOrganizationID,
			SponsorDisplayName: r.SponsorDisplayName, PayerDisplayName: r.PayerDisplayName,
			ValidFrom: datePtr(r.ValidFrom), ValidTo: datePtr(r.ValidTo),
			PlanCount: r.PlanCount, CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
		})
	}
	return out, nil
}

// UpdateProgram implements application.Repository.
func (Repository) UpdateProgram(ctx context.Context, tx pgx.Tx, tenantID, programID uuid.UUID, in application.ProgramUpdateRow) error {
	_, err := sqlcgen.New(tx).UpdateProgram(ctx, sqlcgen.UpdateProgramParams{
		TenantID: tenantID, ID: programID, RowVersion: in.Expected,
		Name: in.Name, Status: in.Status, ValidFrom: date(in.ValidFrom), ValidTo: date(in.ValidTo),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrVersionMismatch
	}
	if err != nil {
		return fmt.Errorf("benefit: update program: %w", err)
	}
	return nil
}

// CreatePlan implements application.Repository.
func (Repository) CreatePlan(ctx context.Context, tx pgx.Tx, in application.NewPlanRow) (uuid.UUID, error) {
	row, err := sqlcgen.New(tx).CreatePlan(ctx, sqlcgen.CreatePlanParams{
		TenantID: in.TenantID, ProgramID: in.ProgramID, Code: in.Code, Name: in.Name,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == constraintPlanCode {
			return uuid.Nil, application.ErrPlanCodeTaken
		}
		return uuid.Nil, fmt.Errorf("benefit: create plan: %w", err)
	}
	return row.ID, nil
}

// GetPlan implements application.Repository.
func (Repository) GetPlan(ctx context.Context, tx pgx.Tx, tenantID, planID uuid.UUID) (application.PlanRow, error) {
	r, err := sqlcgen.New(tx).GetPlan(ctx, sqlcgen.GetPlanParams{TenantID: tenantID, ID: planID})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.PlanRow{}, application.ErrPlanNotFound
	}
	if err != nil {
		return application.PlanRow{}, fmt.Errorf("benefit: get plan: %w", err)
	}
	return application.PlanRow{
		ID: r.ID, ProgramID: r.ProgramID, Code: r.Code, Name: r.Name, Status: r.Status,
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}, nil
}

// ListPlans implements application.Repository.
func (Repository) ListPlans(ctx context.Context, tx pgx.Tx, tenantID, programID uuid.UUID) ([]application.PlanRow, error) {
	rows, err := sqlcgen.New(tx).ListPlans(ctx, sqlcgen.ListPlansParams{TenantID: tenantID, ProgramID: programID})
	if err != nil {
		return nil, fmt.Errorf("benefit: list plans: %w", err)
	}
	out := make([]application.PlanRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.PlanRow{
			ID: r.ID, ProgramID: r.ProgramID, Code: r.Code, Name: r.Name, Status: r.Status,
			CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
		})
	}
	return out, nil
}

// UpdatePlan implements application.Repository.
func (Repository) UpdatePlan(ctx context.Context, tx pgx.Tx, tenantID, planID uuid.UUID, in application.PlanUpdateRow) error {
	_, err := sqlcgen.New(tx).UpdatePlan(ctx, sqlcgen.UpdatePlanParams{
		TenantID: tenantID, ID: planID, RowVersion: in.Expected, Name: in.Name, Status: in.Status,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrVersionMismatch
	}
	if err != nil {
		return fmt.Errorf("benefit: update plan: %w", err)
	}
	return nil
}

// PersonExists implements application.Repository.
func (Repository) PersonExists(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID) (bool, error) {
	ok, err := sqlcgen.New(tx).PersonExists(ctx, sqlcgen.PersonExistsParams{TenantID: tenantID, ID: personID})
	if err != nil {
		return false, fmt.Errorf("benefit: person exists: %w", err)
	}
	return ok, nil
}

func date(t *time.Time) pgtype.Date {
	if t == nil {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: *t, Valid: true}
}

func dateValue(d pgtype.Date) time.Time {
	if !d.Valid {
		return time.Time{}
	}
	return d.Time
}

func datePtr(d pgtype.Date) *time.Time {
	if !d.Valid || d.InfinityModifier != pgtype.Finite {
		return nil
	}
	t := d.Time
	return &t
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func uuidPtr(n uuid.NullUUID) *uuid.UUID {
	if !n.Valid {
		return nil
	}
	id := n.UUID
	return &id
}

func nullUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}
