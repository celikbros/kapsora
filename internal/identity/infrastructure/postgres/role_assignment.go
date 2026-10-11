package identitypg

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/audit"
	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/identity/domain"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

type RoleAssignmentRepository struct {
	pool        *pgxpool.Pool
	audit       audit.Recorder
	idleTimeout time.Duration
}

func NewRoleAssignmentRepository(pool *pgxpool.Pool, recorder ...audit.Recorder) *RoleAssignmentRepository {
	r := &RoleAssignmentRepository{pool: pool, audit: auditpg.New(), idleTimeout: domain.DefaultPolicy().IdleTimeout}
	if len(recorder) > 0 && recorder[0] != nil {
		r.audit = recorder[0]
	}
	return r
}

func (r *RoleAssignmentRepository) WithIdleTimeout(timeout time.Duration) *RoleAssignmentRepository {
	if timeout > 0 {
		r.idleTimeout = timeout
	}
	return r
}

var _ application.RoleAssignmentRepository = (*RoleAssignmentRepository)(nil)

func authorizeRoleAssignment(ctx context.Context, q *sqlcgen.Queries, rc identity.RequestContext) error {
	if rc.App != identity.AppAny && rc.App != identity.AppBackoffice {
		return identity.ErrPermissionDenied
	}
	allowed, err := q.CanManageTenantRoles(ctx, sqlcgen.CanManageTenantRolesParams{
		TenantID: rc.TenantID, ID: rc.MembershipID, ActorID: rc.Principal.ActorID,
	})
	if err != nil {
		return fmt.Errorf("identity: check tenant role management: %w", err)
	}
	if !allowed {
		return identity.ErrPermissionDenied
	}
	return nil
}

func (r *RoleAssignmentRepository) Authorize(ctx context.Context, rc identity.RequestContext) error {
	return db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if err := r.checkSession(ctx, q, rc, false); err != nil {
			return err
		}
		return authorizeRoleAssignment(ctx, q, rc)
	})
}

func (r *RoleAssignmentRepository) AuthorizeCommand(ctx context.Context, rc identity.RequestContext) error {
	return db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if err := r.checkSession(ctx, q, rc, true); err != nil {
			return err
		}
		return authorizeRoleAssignment(ctx, q, rc)
	})
}

func (r *RoleAssignmentRepository) checkSession(ctx context.Context, q *sqlcgen.Queries, rc identity.RequestContext, requireStepUp bool) error {
	if rc.SessionID == "" {
		return identity.ErrUnauthenticated
	}
	row, err := q.LockRoleAssignmentSession(ctx, domain.TokenHash(rc.SessionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.ErrUnauthenticated
	}
	if err != nil {
		return fmt.Errorf("identity: recheck role command session: %w", err)
	}
	now, err := q.RoleAssignmentNow(ctx)
	if err != nil {
		return fmt.Errorf("identity: read role command session time: %w", err)
	}
	if row.RevokedAt != nil || row.ActorID != rc.Principal.ActorID || !now.Before(row.ExpiresAt) || now.Sub(row.LastSeenAt) > r.idleTimeout {
		return identity.ErrUnauthenticated
	}
	if !row.ActiveTenantID.Valid || row.ActiveTenantID.UUID != rc.TenantID {
		return identity.ErrPermissionDenied
	}
	if requireStepUp && (row.StepUpUntil == nil || !now.Before(*row.StepUpUntil)) {
		return identity.ErrStepUpRequired
	}
	return nil
}

func validateRoleConfiguration(ctx context.Context, q *sqlcgen.Queries, tenantID, roleID uuid.UUID, code string, system bool) (application.RoleAssignmentOption, bool, error) {
	scope, supported := application.SupportedRoleScope(code)
	if !supported || !system {
		return application.RoleAssignmentOption{}, false, nil
	}
	tpl, ok := application.RoleTemplateByCode(code)
	if !ok || tpl.Scope != scope {
		return application.RoleAssignmentOption{}, false, nil
	}
	rows, err := q.ListRoleAssignmentPermissions(ctx, sqlcgen.ListRoleAssignmentPermissionsParams{TenantID: tenantID, RoleID: roleID})
	if err != nil {
		return application.RoleAssignmentOption{}, false, fmt.Errorf("identity: read role permissions: %w", err)
	}
	if len(rows) == 0 || len(rows) != len(tpl.Permissions) {
		return application.RoleAssignmentOption{}, false, nil
	}
	expected := append([]string(nil), tpl.Permissions...)
	sort.Strings(expected)
	codes := make([]string, 0, len(rows))
	sensitive := false
	for i, row := range rows {
		if row.PermissionCode != expected[i] || row.Sensitivity == "PRIVILEGED" ||
			(row.Sensitivity != "NORMAL" && row.Sensitivity != "SENSITIVE") {
			return application.RoleAssignmentOption{}, false, nil
		}
		codes = append(codes, row.PermissionCode)
		sensitive = sensitive || row.Sensitivity == "SENSITIVE"
	}
	description := tpl.Description
	switch code {
	case "AUDITOR":
		description = "Raporları ve denetim kayıtlarını inceler; hassas raporları dışa aktarabilir."
	case "FINANCIAL_REVIEWER":
		description = "Fiyat, fatura ve claim mali incelemesi yapar; hassas satır ayrıntılarını içeren raporları dışa aktarabilir."
	}
	return application.RoleAssignmentOption{Code: code, Name: tpl.Name, Description: description,
		ScopeType: scope, PermissionCodes: codes, HasSensitivePermissions: sensitive}, true, nil
}

func (r *RoleAssignmentRepository) Options(ctx context.Context, rc identity.RequestContext) ([]application.RoleAssignmentOption, error) {
	items := make([]application.RoleAssignmentOption, 0)
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if err := authorizeRoleAssignment(ctx, q, rc); err != nil {
			return err
		}
		for code := range map[string]struct{}{
			"PROGRAM_MANAGER": {}, "CONTRACT_MANAGER": {}, "RULE_AUTHOR": {}, "MEDICAL_REVIEWER": {},
			"FINANCIAL_REVIEWER": {}, "AUDITOR": {}, "SPONSOR_HR": {},
			"PROVIDER_ADMIN": {}, "PROVIDER_STAFF": {}, "PROVIDER_BILLING": {}, "PROVIDER_RESERVATION": {},
		} {
			role, err := q.GetRoleAssignmentCandidate(ctx, sqlcgen.GetRoleAssignmentCandidateParams{TenantID: rc.TenantID, Code: code})
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return fmt.Errorf("identity: read role candidate: %w", err)
			}
			option, valid, err := validateRoleConfiguration(ctx, q, rc.TenantID, role.ID, code, role.IsSystemRole)
			if err != nil {
				return err
			}
			if valid {
				items = append(items, option)
			}
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Code < items[j].Code })
		return nil
	})
	return items, err
}

func (r *RoleAssignmentRepository) Organizations(ctx context.Context, rc identity.RequestContext, after *httpx.Cursor, limit int) ([]application.RoleAssignmentOrganization, error) {
	items := make([]application.RoleAssignmentOrganization, 0)
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if err := authorizeRoleAssignment(ctx, q, rc); err != nil {
			return err
		}
		params := sqlcgen.ListRoleAssignmentOrganizationsParams{TenantID: rc.TenantID, PageLimit: int32(limit)} //nolint:gosec // bounded by service
		if after != nil {
			params.AfterAt = &after.CreatedAt
			params.AfterID = uuid.NullUUID{UUID: after.ID, Valid: true}
		}
		rows, err := q.ListRoleAssignmentOrganizations(ctx, params)
		if err != nil {
			return fmt.Errorf("identity: list eligible provider relationships: %w", err)
		}
		for _, row := range rows {
			items = append(items, application.RoleAssignmentOrganization{ID: row.ID, CreatedAt: row.CreatedAt, DisplayName: row.DisplayName, TenantCode: row.TenantCode})
		}
		return nil
	})
	return items, err
}

func roleAssignmentTargetState(actorID, callerID uuid.UUID, status string, valid any, actorType, actorStatus string) error {
	if actorID == callerID {
		return application.ErrRoleAssignmentSelf
	}
	if status != "ACTIVE" || actorType != "HUMAN" || actorStatus != "ACTIVE" || valid != true {
		return application.ErrRoleMembershipConflict
	}
	return nil
}

func boolValue(v any) bool { b, ok := v.(bool); return ok && b }
func stringValue(v any) *string {
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}
func uuidValue(v any) *uuid.UUID {
	switch value := v.(type) {
	case uuid.UUID:
		return &value
	case uuid.NullUUID:
		if value.Valid {
			return &value.UUID
		}
	case string:
		id, err := uuid.Parse(value)
		if err == nil {
			return &id
		}
	case []byte:
		id, err := uuid.ParseBytes(value)
		if err == nil {
			return &id
		}
	}
	return nil
}
func refusal(code string) *string { return &code }

func grantView(id uuid.UUID, roleCode, roleName string, system bool, scope string, orgID, orgName, from, to any, empty bool, createdAt time.Time) application.TenantRoleGrant {
	return application.TenantRoleGrant{ID: id, RoleCode: roleCode, RoleName: roleName, IsSystemRole: system,
		ScopeType: scope, OrganizationRelationshipID: uuidValue(orgID), OrganizationDisplayName: stringValue(orgName),
		ValidFrom: periodEnd(from), ValidTo: periodEnd(to), ValidityEmpty: empty, CreatedAt: createdAt,
		RevocationRefusalCode: refusal("ROLE_ASSIGNMENT_UNSUPPORTED")}
}

func (r *RoleAssignmentRepository) Grants(ctx context.Context, rc identity.RequestContext, membershipID uuid.UUID, after *httpx.Cursor, limit int) (application.RoleGrantList, error) {
	out := application.RoleGrantList{MembershipID: membershipID, Items: make([]application.TenantRoleGrant, 0)}
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if err := authorizeRoleAssignment(ctx, q, rc); err != nil {
			return err
		}
		target, err := q.LockRoleAssignmentTargetRead(ctx, sqlcgen.LockRoleAssignmentTargetReadParams{TenantID: rc.TenantID, ID: membershipID})
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrRoleAssignmentNotFound
		}
		if err != nil {
			return fmt.Errorf("identity: read role assignment target: %w", err)
		}
		out.MembershipRowVersion = target.RowVersion
		stateErr := roleAssignmentTargetState(target.ActorID, rc.Principal.ActorID, target.MembershipStatus, target.MembershipValid, target.ActorType, target.ActorStatus)
		if stateErr != nil {
			if errors.Is(stateErr, application.ErrRoleAssignmentSelf) {
				out.AssignmentRefusalCode = refusal("SELF_ROLE_CHANGE_FORBIDDEN")
			} else {
				out.AssignmentRefusalCode = refusal("MEMBERSHIP_STATE_CONFLICT")
			}
		} else {
			blocked, err := q.HasCurrentFutureOrPersonGrant(ctx, sqlcgen.HasCurrentFutureOrPersonGrantParams{TenantID: rc.TenantID, TenantMembershipID: membershipID})
			if err != nil {
				return fmt.Errorf("identity: check target access: %w", err)
			}
			if blocked {
				out.AssignmentRefusalCode = refusal("EXISTING_ACCESS_CONFLICT")
			} else {
				out.CanAssign = true
			}
		}
		count, err := q.CountCurrentFutureGrants(ctx, sqlcgen.CountCurrentFutureGrantsParams{TenantID: rc.TenantID, TenantMembershipID: membershipID})
		if err != nil {
			return fmt.Errorf("identity: count target grants: %w", err)
		}
		person, err := q.HasPersonGrantHistory(ctx, sqlcgen.HasPersonGrantHistoryParams{TenantID: rc.TenantID, TenantMembershipID: membershipID})
		if err != nil {
			return fmt.Errorf("identity: check person grant history: %w", err)
		}
		params := sqlcgen.ListTenantRoleGrantHistoryParams{TenantID: rc.TenantID, MembershipID: membershipID, PageLimit: int32(limit)} //nolint:gosec // bounded by service
		if after != nil {
			params.AfterAt = &after.CreatedAt
			params.AfterID = uuid.NullUUID{UUID: after.ID, Valid: true}
		}
		rows, err := q.ListTenantRoleGrantHistory(ctx, params)
		if err != nil {
			return fmt.Errorf("identity: read grant history: %w", err)
		}
		for _, row := range rows {
			view := grantView(row.ID, row.RoleCode, row.RoleName, row.IsSystemRole, row.ScopeType,
				row.OrganizationRelationshipID, row.OrganizationDisplayName, row.ValidFrom, row.ValidTo, row.ValidityEmpty, row.CreatedAt)
			scope, supported := application.SupportedRoleScope(row.RoleCode)
			if !supported || scope != row.ScopeType || (scope == application.ScopeOrganization && view.OrganizationRelationshipID == nil) {
				view.OrganizationRelationshipID, view.OrganizationDisplayName = nil, nil
			} else {
				_, configured, err := validateRoleConfiguration(ctx, q, rc.TenantID, row.RoleID, row.RoleCode, row.IsSystemRole)
				if err != nil {
					return err
				}
				if !configured {
					view.OrganizationRelationshipID, view.OrganizationDisplayName = nil, nil
				} else if stateErr == nil && boolValue(row.CurrentGrant) && count == 1 && !person {
					view.CanRevoke, view.RevocationRefusalCode = true, nil
				}
			}
			if !view.CanRevoke && stateErr != nil {
				if errors.Is(stateErr, application.ErrRoleAssignmentSelf) {
					view.RevocationRefusalCode = refusal("SELF_ROLE_CHANGE_FORBIDDEN")
				} else {
					view.RevocationRefusalCode = refusal("MEMBERSHIP_STATE_CONFLICT")
				}
			} else if !view.CanRevoke && !boolValue(row.CurrentGrant) {
				view.RevocationRefusalCode = refusal("GRANT_STATE_CONFLICT")
			} else if !view.CanRevoke && (count != 1 || person) {
				view.RevocationRefusalCode = refusal("EXISTING_ACCESS_CONFLICT")
			}
			out.Items = append(out.Items, view)
		}
		return nil
	})
	return out, err
}

func lockRoleAssignmentTenant(ctx context.Context, q *sqlcgen.Queries, tenantID uuid.UUID) error {
	if _, err := q.LockTenantForUserManagement(ctx, tenantID); err != nil {
		return fmt.Errorf("identity: lock tenant for role assignment: %w", err)
	}
	return nil
}

func lockRoleAssignmentCaller(ctx context.Context, q *sqlcgen.Queries, actorID uuid.UUID) error {
	actor, err := q.LockRoleAssignmentActor(ctx, actorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.ErrPermissionDenied
	}
	if err != nil {
		return fmt.Errorf("identity: lock role manager actor: %w", err)
	}
	if actor.Status != "ACTIVE" {
		return identity.ErrPermissionDenied
	}
	return nil
}

func (r *RoleAssignmentRepository) Assign(ctx context.Context, rc identity.RequestContext, membershipID uuid.UUID, expectedVersion int64, in application.AssignRoleGrantInput) (application.RoleGrantResult, error) {
	out := application.RoleGrantResult{MembershipID: membershipID}
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if err := lockRoleAssignmentTenant(ctx, q, rc.TenantID); err != nil {
			return err
		}
		if err := r.checkSession(ctx, q, rc, true); err != nil {
			return err
		}
		if err := lockRoleAssignmentCaller(ctx, q, rc.Principal.ActorID); err != nil {
			return err
		}
		if err := authorizeRoleAssignment(ctx, q, rc); err != nil {
			return err
		}
		target, err := q.LockRoleAssignmentTarget(ctx, sqlcgen.LockRoleAssignmentTargetParams{TenantID: rc.TenantID, ID: membershipID})
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrRoleAssignmentNotFound
		}
		if err != nil {
			return fmt.Errorf("identity: lock target membership: %w", err)
		}
		if err := roleAssignmentTargetState(target.ActorID, rc.Principal.ActorID, target.MembershipStatus, target.MembershipValid, target.ActorType, target.ActorStatus); err != nil {
			return err
		}
		if target.RowVersion != expectedVersion {
			return application.ErrRoleAssignmentVersion
		}
		role, err := q.GetRoleAssignmentCandidate(ctx, sqlcgen.GetRoleAssignmentCandidateParams{TenantID: rc.TenantID, Code: in.RoleCode})
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrRoleConfigurationUnsupported
		}
		if err != nil {
			return fmt.Errorf("identity: find role candidate: %w", err)
		}
		lockedRole, err := q.LockRoleAssignmentCandidate(ctx, sqlcgen.LockRoleAssignmentCandidateParams{TenantID: rc.TenantID, ID: role.ID})
		if err != nil {
			return fmt.Errorf("identity: lock role candidate: %w", err)
		}
		_, configured, err := validateRoleConfiguration(ctx, q, rc.TenantID, lockedRole.ID, lockedRole.Code, lockedRole.IsSystemRole)
		if err != nil {
			return err
		}
		if !configured {
			return application.ErrRoleConfigurationUnsupported
		}
		blocked, err := q.HasCurrentFutureOrPersonGrant(ctx, sqlcgen.HasCurrentFutureOrPersonGrantParams{TenantID: rc.TenantID, TenantMembershipID: membershipID})
		if err != nil {
			return fmt.Errorf("identity: check existing target access: %w", err)
		}
		if blocked {
			return application.ErrExistingAccessConflict
		}
		var orgName *string
		if in.OrganizationRelationshipID.Valid {
			org, err := q.LockEligibleRoleAssignmentOrganization(ctx, sqlcgen.LockEligibleRoleAssignmentOrganizationParams{TenantID: rc.TenantID, ID: in.OrganizationRelationshipID.UUID})
			if errors.Is(err, pgx.ErrNoRows) {
				return application.ErrRoleAssignmentNotFound
			}
			if err != nil {
				return fmt.Errorf("identity: lock provider relationship: %w", err)
			}
			orgName = &org.DisplayName
		}
		if err := r.checkSession(ctx, q, rc, true); err != nil {
			return err
		}
		if err := lockRoleAssignmentCaller(ctx, q, rc.Principal.ActorID); err != nil {
			return err
		}
		if err := authorizeRoleAssignment(ctx, q, rc); err != nil {
			return err
		}
		at, err := q.RoleAssignmentNow(ctx)
		if err != nil {
			return fmt.Errorf("identity: read assignment timestamp: %w", err)
		}
		id, err := q.InsertTenantRoleGrant(ctx, sqlcgen.InsertTenantRoleGrantParams{TenantID: rc.TenantID,
			TenantMembershipID: membershipID, RoleID: lockedRole.ID, ScopeType: in.ScopeType,
			ScopeID: in.OrganizationRelationshipID, Column6: at,
			GrantedBy: uuid.NullUUID{UUID: rc.Principal.ActorID, Valid: true}})
		if err != nil {
			return fmt.Errorf("identity: insert role grant: %w", err)
		}
		version, err := q.TouchRoleAssignmentMembership(ctx, sqlcgen.TouchRoleAssignmentMembershipParams{TenantID: rc.TenantID, ID: membershipID, RowVersion: expectedVersion})
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrRoleAssignmentVersion
		}
		if err != nil {
			return fmt.Errorf("identity: touch role assignment membership: %w", err)
		}
		out.MembershipRowVersion = version
		grant, err := q.GetTenantRoleGrant(ctx, sqlcgen.GetTenantRoleGrantParams{TenantID: rc.TenantID, TenantMembershipID: membershipID, ID: id})
		if err != nil {
			return fmt.Errorf("identity: read assigned grant: %w", err)
		}
		out.Grant = grantView(grant.ID, grant.RoleCode, grant.RoleName, grant.IsSystemRole, grant.ScopeType,
			grant.OrganizationRelationshipID, grant.OrganizationDisplayName, grant.ValidFrom, grant.ValidTo, grant.ValidityEmpty, grant.CreatedAt)
		out.Grant.CanRevoke, out.Grant.RevocationRefusalCode = true, nil
		return r.audit.Record(ctx, tx, audit.Event{TenantID: uuid.NullUUID{UUID: rc.TenantID, Valid: true},
			ActorID: uuid.NullUUID{UUID: rc.Principal.ActorID, Valid: true}, MembershipID: uuid.NullUUID{UUID: rc.MembershipID, Valid: true},
			Category: audit.CategoryAdmin, ActionCode: "access_grant.assign", ResourceType: "access_grant",
			ResourceID: uuid.NullUUID{UUID: id, Valid: true}, Outcome: audit.OutcomeSuccess, ReasonCode: in.ReasonCode,
			Detail: map[string]any{"target_membership_id": membershipID.String(), "grant_id": id.String(), "role_code": lockedRole.Code,
				"scope_type": in.ScopeType, "organization_relationship_id": nullableAuditUUID(in.OrganizationRelationshipID),
				"prior_validity": nil, "new_valid_from": at.UTC().Format(time.RFC3339Nano), "new_valid_to": nil,
				"reason_code": in.ReasonCode, "organization_display_name_present": orgName != nil}})
	})
	return out, err
}

func nullableAuditUUID(id uuid.NullUUID) any {
	if id.Valid {
		return id.UUID.String()
	}
	return nil
}

func (r *RoleAssignmentRepository) Revoke(ctx context.Context, rc identity.RequestContext, membershipID, grantID uuid.UUID, expectedVersion int64, reasonCode string) (application.RoleGrantResult, error) {
	out := application.RoleGrantResult{MembershipID: membershipID}
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if err := lockRoleAssignmentTenant(ctx, q, rc.TenantID); err != nil {
			return err
		}
		if err := r.checkSession(ctx, q, rc, true); err != nil {
			return err
		}
		if err := authorizeRoleAssignment(ctx, q, rc); err != nil {
			return err
		}
		target, err := q.LockRoleAssignmentTarget(ctx, sqlcgen.LockRoleAssignmentTargetParams{TenantID: rc.TenantID, ID: membershipID})
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrRoleAssignmentNotFound
		}
		if err != nil {
			return fmt.Errorf("identity: lock target membership: %w", err)
		}
		if err := roleAssignmentTargetState(target.ActorID, rc.Principal.ActorID, target.MembershipStatus, target.MembershipValid, target.ActorType, target.ActorStatus); err != nil {
			return err
		}
		if target.RowVersion != expectedVersion {
			return application.ErrRoleAssignmentVersion
		}
		roleID, err := q.GetTenantRoleGrantRoleID(ctx, sqlcgen.GetTenantRoleGrantRoleIDParams{TenantID: rc.TenantID, TenantMembershipID: membershipID, ID: grantID})
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrRoleAssignmentNotFound
		}
		if err != nil {
			return fmt.Errorf("identity: find grant role: %w", err)
		}
		role, err := q.LockRoleAssignmentCandidate(ctx, sqlcgen.LockRoleAssignmentCandidateParams{TenantID: rc.TenantID, ID: roleID})
		if err != nil {
			return fmt.Errorf("identity: lock grant role: %w", err)
		}
		grant, err := q.LockTenantRoleGrant(ctx, sqlcgen.LockTenantRoleGrantParams{TenantID: rc.TenantID, TenantMembershipID: membershipID, ID: grantID})
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrRoleAssignmentNotFound
		}
		if err != nil {
			return fmt.Errorf("identity: lock role grant: %w", err)
		}
		if !boolValue(grant.CurrentGrant) || grant.ValidityEmpty {
			return application.ErrRoleGrantConflict
		}
		scope, supported := application.SupportedRoleScope(role.Code)
		if !supported || scope != grant.ScopeType || (scope == application.ScopeOrganization && !grant.ScopeID.Valid) ||
			(scope == application.ScopeTenant && grant.ScopeID.Valid) {
			return application.ErrRoleAssignmentUnsupported
		}
		_, configured, err := validateRoleConfiguration(ctx, q, rc.TenantID, role.ID, role.Code, role.IsSystemRole)
		if err != nil {
			return err
		}
		if !configured {
			return application.ErrRoleConfigurationUnsupported
		}
		count, err := q.CountCurrentFutureGrants(ctx, sqlcgen.CountCurrentFutureGrantsParams{TenantID: rc.TenantID, TenantMembershipID: membershipID})
		if err != nil {
			return fmt.Errorf("identity: count target grants: %w", err)
		}
		person, err := q.HasPersonGrantHistory(ctx, sqlcgen.HasPersonGrantHistoryParams{TenantID: rc.TenantID, TenantMembershipID: membershipID})
		if err != nil {
			return fmt.Errorf("identity: check person grant history: %w", err)
		}
		if count != 1 || person {
			return application.ErrExistingAccessConflict
		}
		beforeUser, err := q.CountCurrentTenantManagersByPermission(ctx, sqlcgen.CountCurrentTenantManagersByPermissionParams{TenantID: rc.TenantID, PermissionCode: "identity.user.manage"})
		if err != nil {
			return fmt.Errorf("identity: count tenant user managers: %w", err)
		}
		beforeRole, err := q.CountCurrentTenantManagersByPermission(ctx, sqlcgen.CountCurrentTenantManagersByPermissionParams{TenantID: rc.TenantID, PermissionCode: "identity.role.manage"})
		if err != nil {
			return fmt.Errorf("identity: count tenant role managers: %w", err)
		}
		prior, err := q.GetTenantRoleGrant(ctx, sqlcgen.GetTenantRoleGrantParams{TenantID: rc.TenantID, TenantMembershipID: membershipID, ID: grantID})
		if err != nil {
			return fmt.Errorf("identity: read grant before revocation: %w", err)
		}
		if scope == application.ScopeOrganization && uuidValue(prior.OrganizationRelationshipID) == nil {
			return application.ErrRoleAssignmentUnsupported
		}
		if err := r.checkSession(ctx, q, rc, true); err != nil {
			return err
		}
		if err := authorizeRoleAssignment(ctx, q, rc); err != nil {
			return err
		}
		at, err := q.RoleAssignmentNow(ctx)
		if err != nil {
			return fmt.Errorf("identity: read revocation timestamp: %w", err)
		}
		n, err := q.EndTenantRoleGrant(ctx, sqlcgen.EndTenantRoleGrantParams{TenantID: rc.TenantID, TenantMembershipID: membershipID, ID: grantID, Column4: at})
		if err != nil {
			return fmt.Errorf("identity: end role grant: %w", err)
		}
		if n != 1 {
			return application.ErrRoleGrantConflict
		}
		if beforeUser > 0 {
			after, err := q.CountCurrentTenantManagersByPermission(ctx, sqlcgen.CountCurrentTenantManagersByPermissionParams{TenantID: rc.TenantID, PermissionCode: "identity.user.manage"})
			if err != nil {
				return fmt.Errorf("identity: recount tenant user managers: %w", err)
			}
			if after < 1 {
				return application.ErrDirectoryLastManager
			}
		}
		if beforeRole > 0 {
			after, err := q.CountCurrentTenantManagersByPermission(ctx, sqlcgen.CountCurrentTenantManagersByPermissionParams{TenantID: rc.TenantID, PermissionCode: "identity.role.manage"})
			if err != nil {
				return fmt.Errorf("identity: recount tenant role managers: %w", err)
			}
			if after < 1 {
				return application.ErrLastTenantRoleManager
			}
		}
		version, err := q.TouchRoleAssignmentMembership(ctx, sqlcgen.TouchRoleAssignmentMembershipParams{TenantID: rc.TenantID, ID: membershipID, RowVersion: expectedVersion})
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrRoleAssignmentVersion
		}
		if err != nil {
			return fmt.Errorf("identity: touch revoked membership: %w", err)
		}
		out.MembershipRowVersion = version
		ended, err := q.GetTenantRoleGrant(ctx, sqlcgen.GetTenantRoleGrantParams{TenantID: rc.TenantID, TenantMembershipID: membershipID, ID: grantID})
		if err != nil {
			return fmt.Errorf("identity: read ended grant: %w", err)
		}
		out.Grant = grantView(ended.ID, ended.RoleCode, ended.RoleName, ended.IsSystemRole, ended.ScopeType,
			ended.OrganizationRelationshipID, ended.OrganizationDisplayName, ended.ValidFrom, ended.ValidTo, ended.ValidityEmpty, ended.CreatedAt)
		out.Grant.RevocationRefusalCode = refusal("GRANT_STATE_CONFLICT")
		return r.audit.Record(ctx, tx, audit.Event{TenantID: uuid.NullUUID{UUID: rc.TenantID, Valid: true},
			ActorID: uuid.NullUUID{UUID: rc.Principal.ActorID, Valid: true}, MembershipID: uuid.NullUUID{UUID: rc.MembershipID, Valid: true},
			Category: audit.CategoryAdmin, ActionCode: "access_grant.revoke", ResourceType: "access_grant",
			ResourceID: uuid.NullUUID{UUID: grantID, Valid: true}, Outcome: audit.OutcomeSuccess, ReasonCode: reasonCode,
			Detail: map[string]any{"target_membership_id": membershipID.String(), "grant_id": grantID.String(), "role_code": role.Code,
				"scope_type": grant.ScopeType, "organization_relationship_id": nullableAuditUUID(grant.ScopeID),
				"prior_valid_from": prior.ValidFrom, "prior_valid_to": prior.ValidTo,
				"new_valid_from": ended.ValidFrom, "new_valid_to": at.UTC().Format(time.RFC3339Nano), "reason_code": reasonCode}})
	})
	return out, err
}
