package benefitpg

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// NextVersionNo implements application.Repository.
func (Repository) NextVersionNo(ctx context.Context, tx pgx.Tx, tenantID, planID uuid.UUID) (int, error) {
	n, err := sqlcgen.New(tx).NextPlanVersionNo(ctx, sqlcgen.NextPlanVersionNoParams{TenantID: tenantID, PlanID: planID})
	if err != nil {
		return 0, fmt.Errorf("benefit: next version number: %w", err)
	}
	return int(n), nil
}

// CreatePlanVersion implements application.Repository.
func (Repository) CreatePlanVersion(ctx context.Context, tx pgx.Tx, in application.NewPlanVersionRow) (uuid.UUID, error) {
	id, err := sqlcgen.New(tx).CreatePlanVersion(ctx, sqlcgen.CreatePlanVersionParams{
		TenantID: in.TenantID, PlanID: in.PlanID, ActorID: nullUUID(in.ActorID),
		VersionNo: int32(in.VersionNo), //nolint:gosec // version numbers come from the database counter
		ValidFrom: date(in.ValidFrom), ValidTo: date(in.ValidTo), Notes: in.Notes,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == constraintVersionNo {
			return uuid.Nil, application.ErrVersionNumberTaken
		}
		return uuid.Nil, fmt.Errorf("benefit: create plan version: %w", err)
	}
	return id, nil
}

// GetPlanVersion implements application.Repository.
func (Repository) GetPlanVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (application.PlanVersionRow, error) {
	r, err := sqlcgen.New(tx).GetPlanVersion(ctx, sqlcgen.GetPlanVersionParams{TenantID: tenantID, ID: versionID})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.PlanVersionRow{}, application.ErrPlanVersionNotFound
	}
	if err != nil {
		return application.PlanVersionRow{}, fmt.Errorf("benefit: get plan version: %w", err)
	}
	return application.PlanVersionRow{
		ID: r.ID, PlanID: r.PlanID, VersionNo: int(r.VersionNo), Status: r.Status,
		ValidFrom: datePtr(r.ValidFrom), ValidTo: datePtr(r.ValidTo),
		ConfigurationHash: r.ConfigurationHash,
		PublishedAt:       r.PublishedAt, PublishedBy: uuidPtr(r.PublishedBy),
		SubmittedAt: r.SubmittedAt, SubmittedBy: uuidPtr(r.SubmittedBy),
		ReviewComment: r.ReviewComment, RetireReasonCode: r.RetireReasonCode,
		RetireReasonText: r.RetireReasonText, Notes: r.Notes,
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}, nil
}

// LockPlanVersion implements application.Repository.
func (Repository) LockPlanVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (application.PlanVersionRow, error) {
	r, err := sqlcgen.New(tx).GetPlanVersionForUpdate(ctx, sqlcgen.GetPlanVersionForUpdateParams{TenantID: tenantID, ID: versionID})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.PlanVersionRow{}, application.ErrPlanVersionNotFound
	}
	if err != nil {
		return application.PlanVersionRow{}, fmt.Errorf("benefit: lock plan version: %w", err)
	}
	return application.PlanVersionRow{
		ID: r.ID, PlanID: r.PlanID, VersionNo: int(r.VersionNo), Status: r.Status,
		ValidFrom: datePtr(r.ValidFrom), ValidTo: datePtr(r.ValidTo),
		ConfigurationHash: r.ConfigurationHash,
		PublishedAt:       r.PublishedAt, PublishedBy: uuidPtr(r.PublishedBy),
		SubmittedAt: r.SubmittedAt, SubmittedBy: uuidPtr(r.SubmittedBy),
		ReviewComment: r.ReviewComment, RetireReasonCode: r.RetireReasonCode,
		RetireReasonText: r.RetireReasonText, Notes: r.Notes,
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}, nil
}

// ListPlanVersions implements application.Repository.
func (Repository) ListPlanVersions(ctx context.Context, tx pgx.Tx, tenantID, planID uuid.UUID) ([]application.PlanVersionRow, error) {
	rows, err := sqlcgen.New(tx).ListPlanVersions(ctx, sqlcgen.ListPlanVersionsParams{TenantID: tenantID, PlanID: planID})
	if err != nil {
		return nil, fmt.Errorf("benefit: list plan versions: %w", err)
	}
	out := make([]application.PlanVersionRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.PlanVersionRow{
			ID: r.ID, PlanID: r.PlanID, VersionNo: int(r.VersionNo), Status: r.Status,
			ValidFrom: datePtr(r.ValidFrom), ValidTo: datePtr(r.ValidTo),
			ConfigurationHash: r.ConfigurationHash,
			PublishedAt:       r.PublishedAt, PublishedBy: uuidPtr(r.PublishedBy),
			SubmittedAt: r.SubmittedAt, SubmittedBy: uuidPtr(r.SubmittedBy),
			ReviewComment: r.ReviewComment, RetireReasonCode: r.RetireReasonCode,
			RetireReasonText: r.RetireReasonText, Notes: r.Notes,
			CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
		})
	}
	return out, nil
}

// UpdatePlanVersionDraft implements application.Repository.
func (Repository) UpdatePlanVersionDraft(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, in application.PlanVersionDraftRow) error {
	n, err := sqlcgen.New(tx).UpdatePlanVersionDraft(ctx, sqlcgen.UpdatePlanVersionDraftParams{
		TenantID: tenantID, ID: versionID,
		ValidFrom: date(in.ValidFrom), ValidTo: date(in.ValidTo), Notes: in.Notes,
	})
	return versionWriteResult(n, err, "update plan version")
}

// TouchPlanVersion implements application.Repository.
func (Repository) TouchPlanVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) error {
	n, err := sqlcgen.New(tx).TouchPlanVersion(ctx, sqlcgen.TouchPlanVersionParams{TenantID: tenantID, ID: versionID})
	return versionWriteResult(n, err, "touch plan version")
}

// SubmitPlanVersion implements application.Repository.
func (Repository) SubmitPlanVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, in application.SubmitRow) error {
	n, err := sqlcgen.New(tx).SubmitPlanVersion(ctx, sqlcgen.SubmitPlanVersionParams{
		TenantID: tenantID, ID: versionID, ActorID: nullUUID(in.ActorID), ReviewComment: in.Comment,
	})
	return versionWriteResult(n, err, "submit plan version")
}

// PublishPlanVersion implements application.Repository. The exclusion constraint on
// published periods and the maker-checker CHECK are the database's own guards.
func (Repository) PublishPlanVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, in application.PublishRow) error {
	n, err := sqlcgen.New(tx).PublishPlanVersion(ctx, sqlcgen.PublishPlanVersionParams{
		TenantID: tenantID, ID: versionID, ActorID: nullUUID(in.ActorID),
		ConfigurationHash: in.ConfigurationHash, ReviewComment: in.Comment,
	})
	return versionWriteResult(n, err, "publish plan version")
}

// RetirePlanVersion implements application.Repository.
func (Repository) RetirePlanVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, in application.RetireRow) error {
	n, err := sqlcgen.New(tx).RetirePlanVersion(ctx, sqlcgen.RetirePlanVersionParams{
		TenantID: tenantID, ID: versionID, ReasonCode: &in.ReasonCode, ReasonText: in.ReasonText,
	})
	return versionWriteResult(n, err, "retire plan version")
}

// versionWriteResult maps the database guards of benefit.plan_version. The service holds
// the row lock and has already checked status and row version, so zero affected rows can
// only mean a concurrent state change.
func versionWriteResult(affected int64, err error, what string) error {
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch {
			case pgErr.Code == exclusionViolation && pgErr.ConstraintName == constraintVersionPeriod:
				return application.ErrVersionOverlap
			case pgErr.Code == checkViolation && pgErr.ConstraintName == constraintMakerChecker:
				return application.ErrMakerCheckerSame
			case pgErr.Code == integrityConstraint:
				return application.ErrVersionImmutable
			}
		}
		return fmt.Errorf("benefit: %s: %w", what, err)
	}
	if affected == 0 {
		return application.ErrVersionTransition
	}
	return nil
}

// ListDefinitions implements application.Repository.
func (Repository) ListDefinitions(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) ([]application.DefinitionRow, error) {
	rows, err := sqlcgen.New(tx).ListEntitlementDefinitions(ctx, sqlcgen.ListEntitlementDefinitionsParams{
		TenantID: tenantID, PlanVersionID: versionID,
	})
	if err != nil {
		return nil, fmt.Errorf("benefit: list entitlement definitions: %w", err)
	}
	out := make([]application.DefinitionRow, 0, len(rows))
	for _, r := range rows {
		spec := domain.EntitlementDefinition{
			Code: r.Code, Name: r.Name, UnitType: r.UnitType, PeriodType: r.PeriodType,
			InitialQuantity: domain.TrimDecimal(r.InitialQuantity), AllowOverdraft: r.AllowOverdraft,
			RolloverPolicy: r.RolloverPolicy, FamilyShared: r.FamilyShared,
		}
		if r.CurrencyCode != nil {
			spec.CurrencyCode = *r.CurrencyCode
		}
		if r.PeriodLength != nil {
			n := int(*r.PeriodLength)
			spec.PeriodLength = &n
		}
		if r.RolloverCap != "" {
			spec.RolloverCap = domain.TrimDecimal(r.RolloverCap)
		}
		out = append(out, application.DefinitionRow{ID: r.ID, Status: r.Status, Definition: spec})
	}
	return out, nil
}

// DeleteDefinitions implements application.Repository.
func (Repository) DeleteDefinitions(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (int64, error) {
	n, err := sqlcgen.New(tx).DeleteEntitlementDefinitions(ctx, sqlcgen.DeleteEntitlementDefinitionsParams{
		TenantID: tenantID, PlanVersionID: versionID,
	})
	if err != nil {
		return 0, fmt.Errorf("benefit: delete entitlement definitions: %w", err)
	}
	return n, nil
}

// CreateDefinition implements application.Repository.
func (Repository) CreateDefinition(ctx context.Context, tx pgx.Tx, in application.NewDefinitionRow) (uuid.UUID, error) {
	d := in.Definition
	params := sqlcgen.CreateEntitlementDefinitionParams{
		TenantID: in.TenantID, PlanVersionID: in.PlanVersionID, Code: d.Code, Name: d.Name,
		UnitType: d.UnitType, CurrencyCode: optString(d.CurrencyCode), PeriodType: d.PeriodType,
		InitialQuantity: d.InitialQuantity, AllowOverdraft: d.AllowOverdraft,
		RolloverPolicy: d.RolloverPolicy, RolloverCap: optString(d.RolloverCap),
		FamilyShared: d.FamilyShared,
	}
	if d.PeriodLength != nil {
		n := int32(*d.PeriodLength) //nolint:gosec // validated as a small positive day count
		params.PeriodLength = &n
	}
	id, err := sqlcgen.New(tx).CreateEntitlementDefinition(ctx, params)
	if err != nil {
		return uuid.Nil, fmt.Errorf("benefit: create entitlement definition: %w", err)
	}
	return id, nil
}
