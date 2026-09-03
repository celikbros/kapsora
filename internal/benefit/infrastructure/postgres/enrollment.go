package benefitpg

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// GetMembership implements application.Repository.
func (Repository) GetMembership(ctx context.Context, tx pgx.Tx, tenantID, membershipID uuid.UUID) (application.MembershipRow, error) {
	r, err := sqlcgen.New(tx).GetEnrollmentMembership(ctx, sqlcgen.GetEnrollmentMembershipParams{TenantID: tenantID, ID: membershipID})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.MembershipRow{}, application.ErrNotFound
	}
	if err != nil {
		return application.MembershipRow{}, fmt.Errorf("benefit: get membership: %w", err)
	}
	return application.MembershipRow{
		ID: r.ID, PersonID: r.PersonID, Status: r.Status,
		ValidFrom: datePtr(r.ValidFrom), ValidTo: datePtr(r.ValidTo),
	}, nil
}

// CreateEnrollment implements application.Repository; the exclusion constraint on the
// validity period becomes ErrEnrollmentOverlap.
func (Repository) CreateEnrollment(ctx context.Context, tx pgx.Tx, in application.NewEnrollmentRow) (uuid.UUID, error) {
	row, err := sqlcgen.New(tx).CreateEnrollment(ctx, sqlcgen.CreateEnrollmentParams{
		TenantID: in.TenantID, SponsorMembershipID: in.SponsorMembershipID, PlanID: in.PlanID,
		Status: in.Status, ValidFrom: date(&in.ValidFrom), ValidTo: date(in.ValidTo),
		EnrollmentReason: in.EnrollmentReason,
	})
	if err != nil {
		return uuid.Nil, enrollmentError(err, "create enrollment")
	}
	return row.ID, nil
}

// GetEnrollment implements application.Repository.
func (Repository) GetEnrollment(ctx context.Context, tx pgx.Tx, tenantID, enrollmentID uuid.UUID) (application.EnrollmentRow, error) {
	r, err := sqlcgen.New(tx).GetEnrollment(ctx, sqlcgen.GetEnrollmentParams{TenantID: tenantID, ID: enrollmentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.EnrollmentRow{}, application.ErrEnrollmentNotFound
	}
	if err != nil {
		return application.EnrollmentRow{}, fmt.Errorf("benefit: get enrollment: %w", err)
	}
	return application.EnrollmentRow{
		ID: r.ID, PersonID: r.PersonID, SponsorMembershipID: r.SponsorMembershipID,
		PlanID: r.PlanID, ProgramID: r.ProgramID, PlanCode: r.PlanCode, Status: r.Status,
		ValidFrom: dateValue(r.ValidFrom), ValidTo: datePtr(r.ValidTo),
		EnrollmentReason: r.EnrollmentReason, SourceSystem: r.SourceSystem,
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}, nil
}

// ListPersonEnrollments implements application.Repository.
func (Repository) ListPersonEnrollments(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID) ([]application.EnrollmentRow, error) {
	rows, err := sqlcgen.New(tx).ListPersonEnrollments(ctx, sqlcgen.ListPersonEnrollmentsParams{TenantID: tenantID, PersonID: personID})
	if err != nil {
		return nil, fmt.Errorf("benefit: list person enrollments: %w", err)
	}
	out := make([]application.EnrollmentRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.EnrollmentRow{
			ID: r.ID, PersonID: r.PersonID, SponsorMembershipID: r.SponsorMembershipID,
			PlanID: r.PlanID, ProgramID: r.ProgramID, PlanCode: r.PlanCode, Status: r.Status,
			ValidFrom: dateValue(r.ValidFrom), ValidTo: datePtr(r.ValidTo),
			EnrollmentReason: r.EnrollmentReason, SourceSystem: r.SourceSystem,
			CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
		})
	}
	return out, nil
}

// ListEnrollments implements application.Repository.
func (Repository) ListEnrollments(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q application.EnrollmentListQuery) ([]application.EnrollmentRow, error) {
	params := sqlcgen.ListEnrollmentsParams{
		TenantID: tenantID, PlanID: nullUUID(q.PlanID), Status: optString(q.Status),
		PageSize: int32(q.PageSize), //nolint:gosec // page size is clamped by httpx.ClampLimit
	}
	if q.After != nil {
		params.CursorCreatedAt = &q.After.CreatedAt
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListEnrollments(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("benefit: list enrollments: %w", err)
	}
	out := make([]application.EnrollmentRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.EnrollmentRow{
			ID: r.ID, PersonID: r.PersonID, SponsorMembershipID: r.SponsorMembershipID,
			PlanID: r.PlanID, ProgramID: r.ProgramID, PlanCode: r.PlanCode, Status: r.Status,
			ValidFrom: dateValue(r.ValidFrom), ValidTo: datePtr(r.ValidTo),
			EnrollmentReason: r.EnrollmentReason, SourceSystem: r.SourceSystem,
			CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
		})
	}
	return out, nil
}

// UpdateEnrollment implements application.Repository.
func (Repository) UpdateEnrollment(ctx context.Context, tx pgx.Tx, tenantID, enrollmentID uuid.UUID, in application.EnrollmentUpdateRow) error {
	_, err := sqlcgen.New(tx).UpdateEnrollment(ctx, sqlcgen.UpdateEnrollmentParams{
		TenantID: tenantID, ID: enrollmentID, RowVersion: in.Expected,
		Status: in.Status, ValidTo: date(in.ValidTo),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrVersionMismatch
	}
	if err != nil {
		return enrollmentError(err, "update enrollment")
	}
	return nil
}

func enrollmentError(err error, what string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == exclusionViolation && pgErr.ConstraintName == constraintEnrollPeriod {
		return application.ErrEnrollmentOverlap
	}
	return fmt.Errorf("benefit: %s: %w", what, err)
}
