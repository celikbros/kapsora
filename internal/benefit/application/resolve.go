package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// ResolvePlanVersion returns the PUBLISHED plan version whose validity period contains
// asOf, or ErrNoPublishedVersion. The period is half-open: the lower bound is inclusive,
// the upper bound is exclusive, so a version valid [2026-01-01, 2027-01-01) answers for
// 2026-01-01 and 2026-12-31 but not for 2027-01-01. At most one version can match: the
// exclusion constraint ex_published_plan_version_period forbids overlapping published
// periods within a plan.
//
// Later packages (eligibility, entitlement accounts, decisions) call this to snapshot the
// configuration that was valid on the service date. It takes the caller's transaction so
// the snapshot is consistent with the rest of that transaction, and it reads the
// generated queries directly for the same reason internal/platform/outbox does: it is a
// free function shared across modules, not a method on a wired service.
func ResolvePlanVersion(ctx context.Context, tx pgx.Tx, tenantID, planID uuid.UUID, asOf time.Time) (PlanVersionRow, error) {
	row, err := sqlcgen.New(tx).ResolvePlanVersion(ctx, sqlcgen.ResolvePlanVersionParams{
		TenantID: tenantID,
		PlanID:   planID,
		AsOf:     pgtype.Date{Time: domain.DateOnly(asOf), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return PlanVersionRow{}, ErrNoPublishedVersion
	}
	if err != nil {
		return PlanVersionRow{}, fmt.Errorf("benefit: resolve plan version: %w", err)
	}
	return PlanVersionRow{
		ID: row.ID, PlanID: row.PlanID, VersionNo: int(row.VersionNo), Status: row.Status,
		ValidFrom: optDate(row.ValidFrom), ValidTo: optDate(row.ValidTo),
		ConfigurationHash: row.ConfigurationHash,
		PublishedAt:       row.PublishedAt, PublishedBy: optUUID(row.PublishedBy),
		SubmittedAt: row.SubmittedAt, SubmittedBy: optUUID(row.SubmittedBy),
		ReviewComment: row.ReviewComment, RetireReasonCode: row.RetireReasonCode,
		RetireReasonText: row.RetireReasonText, Notes: row.Notes,
		CreatedAt: row.CreatedAt, RowVersion: row.RowVersion,
	}, nil
}

func optDate(d pgtype.Date) *time.Time {
	if !d.Valid || d.InfinityModifier != pgtype.Finite {
		return nil
	}
	t := d.Time
	return &t
}

func optUUID(n uuid.NullUUID) *uuid.UUID {
	if !n.Valid {
		return nil
	}
	id := n.UUID
	return &id
}
