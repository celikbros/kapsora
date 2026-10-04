package identitypg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/audit"
	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// DirectoryRepository performs permission correlation and membership reads in one
// tenant-bound transaction. The projected columns are the response allowlist.
type DirectoryRepository struct {
	pool  *pgxpool.Pool
	audit audit.Recorder
}

func NewDirectoryRepository(pool *pgxpool.Pool, recorder ...audit.Recorder) *DirectoryRepository {
	selected := audit.Recorder(auditpg.New())
	if len(recorder) > 0 && recorder[0] != nil {
		selected = recorder[0]
	}
	return &DirectoryRepository{pool: pool, audit: selected}
}

var _ application.DirectoryRepository = (*DirectoryRepository)(nil)

func authorizeDirectory(ctx context.Context, q *sqlcgen.Queries, rc identity.RequestContext) error {
	if rc.App != identity.AppAny && rc.App != identity.AppBackoffice {
		return identity.ErrPermissionDenied
	}
	allowed, err := q.CanReadTenantUsers(ctx, sqlcgen.CanReadTenantUsersParams{
		TenantID: rc.TenantID, ID: rc.MembershipID, ActorID: rc.Principal.ActorID,
	})
	if err != nil {
		return fmt.Errorf("identity: check tenant directory permission: %w", err)
	}
	if !allowed {
		return identity.ErrPermissionDenied
	}
	return nil
}

// sqlc sees the nullable upper(range) expression as any; pgx returns text or nil.
func periodEnd(value any) *string {
	if text, ok := value.(string); ok {
		return nullableStatus(text)
	}
	return nil
}

func fromListRow(row sqlcgen.ListTenantUsersRow) application.DirectoryMembership {
	return application.DirectoryMembership{
		ID: row.ID, DisplayName: row.DisplayName, ActorType: row.ActorType,
		ActorStatus: row.ActorStatus, MembershipStatus: row.MembershipStatus,
		ValidFrom: periodEnd(row.ValidFrom), ValidTo: periodEnd(row.ValidTo),
		ValidityEmpty: row.ValidityEmpty, CreatedAt: row.CreatedAt,
		RowVersion: row.RowVersion,
	}
}

func fromDetailRow(row sqlcgen.GetTenantUserRow) application.DirectoryMembership {
	return application.DirectoryMembership{
		ID: row.ID, DisplayName: row.DisplayName, ActorType: row.ActorType,
		ActorStatus: row.ActorStatus, MembershipStatus: row.MembershipStatus,
		ValidFrom: periodEnd(row.ValidFrom), ValidTo: periodEnd(row.ValidTo),
		ValidityEmpty: row.ValidityEmpty, CreatedAt: row.CreatedAt,
		RowVersion: row.RowVersion,
	}
}

func (r *DirectoryRepository) List(ctx context.Context, rc identity.RequestContext, filter application.DirectoryFilter) ([]application.DirectoryMembership, error) {
	if filter.Limit < 1 || filter.Limit > httpx.MaxPageSize+1 {
		return nil, fmt.Errorf("identity: invalid directory page limit")
	}
	out := make([]application.DirectoryMembership, 0)
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if err := authorizeDirectory(ctx, q, rc); err != nil {
			return err
		}
		var afterAt *time.Time
		var afterID uuid.NullUUID
		if filter.After != nil {
			afterAt, afterID = &filter.After.CreatedAt, uuid.NullUUID{UUID: filter.After.ID, Valid: true}
		}
		rows, err := q.ListTenantUsers(ctx, sqlcgen.ListTenantUsersParams{
			TenantID: rc.TenantID, MembershipStatus: nullableStatus(filter.Status),
			AfterAt: afterAt, AfterID: afterID, PageLimit: int32(filter.Limit), //nolint:gosec // checked against 1..MaxPageSize+1 above
		})
		if err != nil {
			return fmt.Errorf("identity: list tenant memberships: %w", err)
		}
		for _, row := range rows {
			out = append(out, fromListRow(row))
		}
		return nil
	})
	return out, err
}

func nullableStatus(status string) *string {
	if status == "" {
		return nil
	}
	return &status
}

func (r *DirectoryRepository) Get(ctx context.Context, rc identity.RequestContext, membershipID uuid.UUID) (application.DirectoryDetail, error) {
	out := application.DirectoryDetail{AssignedRoles: make([]application.AssignedRole, 0)}
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if err := authorizeDirectory(ctx, q, rc); err != nil {
			return err
		}
		var err error
		out, err = loadDirectoryDetail(ctx, q, rc.TenantID, membershipID)
		return err
	})
	return out, err
}

func loadDirectoryDetail(ctx context.Context, q *sqlcgen.Queries, tenantID, membershipID uuid.UUID) (application.DirectoryDetail, error) {
	out := application.DirectoryDetail{AssignedRoles: make([]application.AssignedRole, 0)}
	member, err := q.GetTenantUser(ctx, sqlcgen.GetTenantUserParams{TenantID: tenantID, ID: membershipID})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, application.ErrMembershipNotFound
	}
	if err != nil {
		return out, fmt.Errorf("identity: get tenant membership: %w", err)
	}
	out.Membership = fromDetailRow(member)
	roles, err := q.ListTenantUserRoles(ctx, sqlcgen.ListTenantUserRolesParams{TenantID: tenantID, TenantMembershipID: membershipID})
	if err != nil {
		return out, fmt.Errorf("identity: list assigned roles: %w", err)
	}
	for _, role := range roles {
		out.AssignedRoles = append(out.AssignedRoles, application.AssignedRole{
			Code: role.Code, Name: role.Name, System: role.IsSystemRole,
			ScopeType: role.ScopeType, ValidFrom: periodEnd(role.ValidFrom), ValidTo: periodEnd(role.ValidTo),
			ValidityEmpty: role.ValidityEmpty,
		})
	}
	return out, nil
}

func authorizeManageDirectory(ctx context.Context, q *sqlcgen.Queries, rc identity.RequestContext) error {
	if rc.App != identity.AppAny && rc.App != identity.AppBackoffice {
		return identity.ErrPermissionDenied
	}
	allowed, err := q.CanManageTenantUsers(ctx, sqlcgen.CanManageTenantUsersParams{
		TenantID: rc.TenantID, ID: rc.MembershipID, ActorID: rc.Principal.ActorID,
	})
	if err != nil {
		return fmt.Errorf("identity: check tenant user management: %w", err)
	}
	if !allowed {
		return identity.ErrPermissionDenied
	}
	return nil
}

func (r *DirectoryRepository) AuthorizeManage(ctx context.Context, rc identity.RequestContext) error {
	return db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		return authorizeManageDirectory(ctx, sqlcgen.New(tx), rc)
	})
}

func (r *DirectoryRepository) Suspend(ctx context.Context, rc identity.RequestContext, membershipID uuid.UUID, expectedVersion int64, reasonCode string) (application.DirectoryDetail, error) {
	out := application.DirectoryDetail{}
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if _, err := q.LockTenantForUserManagement(ctx, rc.TenantID); err != nil {
			return fmt.Errorf("identity: lock tenant management: %w", err)
		}
		if err := authorizeManageDirectory(ctx, q, rc); err != nil {
			return err
		}
		target, err := q.LockTenantUserForSuspension(ctx, sqlcgen.LockTenantUserForSuspensionParams{TenantID: rc.TenantID, ID: membershipID})
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrMembershipNotFound
		}
		if err != nil {
			return fmt.Errorf("identity: lock tenant membership: %w", err)
		}
		if target.ActorID == rc.Principal.ActorID {
			return application.ErrDirectorySelfSuspension
		}
		if target.MembershipStatus != "ACTIVE" {
			return application.ErrDirectoryStateConflict
		}
		if target.RowVersion != expectedVersion {
			return application.ErrDirectoryVersionConflict
		}
		usable, err := q.IsUsableTenantUserManager(ctx, sqlcgen.IsUsableTenantUserManagerParams{TenantID: rc.TenantID, ID: membershipID})
		if err != nil {
			return fmt.Errorf("identity: check target management access: %w", err)
		}
		if usable {
			others, err := q.CountOtherUsableTenantUserManagers(ctx, sqlcgen.CountOtherUsableTenantUserManagersParams{TenantID: rc.TenantID, ID: membershipID})
			if err != nil {
				return fmt.Errorf("identity: count other tenant managers: %w", err)
			}
			if others < 1 {
				return application.ErrDirectoryLastManager
			}
		}
		if _, err := q.SuspendTenantUser(ctx, sqlcgen.SuspendTenantUserParams{TenantID: rc.TenantID, ID: membershipID, RowVersion: expectedVersion}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return application.ErrDirectoryVersionConflict
			}
			return fmt.Errorf("identity: suspend tenant membership: %w", err)
		}
		out, err = loadDirectoryDetail(ctx, q, rc.TenantID, membershipID)
		if err != nil {
			return err
		}
		return r.audit.Record(ctx, tx, audit.Event{
			TenantID:     uuid.NullUUID{UUID: rc.TenantID, Valid: true},
			ActorID:      uuid.NullUUID{UUID: rc.Principal.ActorID, Valid: true},
			MembershipID: uuid.NullUUID{UUID: rc.MembershipID, Valid: true},
			Category:     audit.CategoryAdmin, ActionCode: "tenant_membership.suspend",
			ResourceType: "tenant_membership", ResourceID: uuid.NullUUID{UUID: membershipID, Valid: true},
			Outcome: audit.OutcomeSuccess, ReasonCode: reasonCode,
			Detail: map[string]any{"old_status": "ACTIVE", "new_status": "SUSPENDED"},
		})
	})
	return out, err
}
