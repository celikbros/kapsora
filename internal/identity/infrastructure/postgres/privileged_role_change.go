package identitypg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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

type PrivilegedRoleChangeRepository struct {
	pool        *pgxpool.Pool
	audit       audit.Recorder
	idleTimeout time.Duration
}

func NewPrivilegedRoleChangeRepository(pool *pgxpool.Pool, recorder ...audit.Recorder) *PrivilegedRoleChangeRepository {
	r := &PrivilegedRoleChangeRepository{pool: pool, audit: auditpg.New(), idleTimeout: domain.DefaultPolicy().IdleTimeout}
	if len(recorder) > 0 && recorder[0] != nil {
		r.audit = recorder[0]
	}
	return r
}
func (r *PrivilegedRoleChangeRepository) WithIdleTimeout(timeout time.Duration) *PrivilegedRoleChangeRepository {
	if timeout > 0 {
		r.idleTimeout = timeout
	}
	return r
}

var _ application.PrivilegedRoleChangeRepository = (*PrivilegedRoleChangeRepository)(nil)

func roleChangeValidity(v pgtype.Range[pgtype.Timestamptz]) *application.RoleChangeValidity {
	if !v.Valid {
		return nil
	}
	out := &application.RoleChangeValidity{FromInclusive: v.LowerType == pgtype.Inclusive, ToInclusive: v.UpperType == pgtype.Inclusive}
	if v.LowerType != pgtype.Unbounded && v.Lower.Valid {
		s := v.Lower.Time.UTC().Format(time.RFC3339Nano)
		out.From = &s
	}
	if v.UpperType != pgtype.Unbounded && v.Upper.Valid {
		s := v.Upper.Time.UTC().Format(time.RFC3339Nano)
		out.To = &s
	}
	return out
}

func roleChangeAuditRange(detail map[string]any, prefix string, v pgtype.Range[pgtype.Timestamptz]) {
	validity := roleChangeValidity(v)
	if validity == nil {
		return
	}
	if validity.From != nil {
		detail[prefix+"_from"] = *validity.From
	}
	if validity.To != nil {
		detail[prefix+"_to"] = *validity.To
	}
	detail[prefix+"_from_inclusive"] = validity.FromInclusive
	detail[prefix+"_to_inclusive"] = validity.ToInclusive
}
func roleChangeUUID(v uuid.NullUUID) *uuid.UUID {
	if v.Valid {
		return &v.UUID
	}
	return nil
}

func roleChangeRequestView(row sqlcgen.IamRoleChangeRequest) (application.RoleChangeRequest, error) {
	var snapshot []application.RoleChangePermission
	if err := json.Unmarshal(row.PermissionSnapshot, &snapshot); err != nil {
		return application.RoleChangeRequest{}, fmt.Errorf("identity: decode role change snapshot: %w", err)
	}
	return application.RoleChangeRequest{
		ID: row.ID, Operation: row.Operation, Status: row.Status, TargetMembershipID: row.TargetMembershipID,
		MakerMembershipID: row.MakerMembershipID, RoleCode: row.RoleCode, ScopeType: row.ScopeType,
		PermissionSnapshot: snapshot, ConfigurationHash: hex.EncodeToString(row.ConfigurationHash),
		TargetMembershipVersion: row.TargetMembershipVersion, RevokeGrantID: roleChangeUUID(row.RevokeGrantID),
		RevokeValidity: roleChangeValidity(row.RevokeValidPeriod), ReasonCode: row.ReasonCode, CreatedAt: row.CreatedAt,
		RowVersion: row.RowVersion, DecidedAt: row.DecidedAt, DecidedByMembershipID: roleChangeUUID(row.DecidedByMembershipID),
		DecisionReasonCode: row.DecisionReasonCode, AppliedGrantID: roleChangeUUID(row.AppliedGrantID),
		AppliedValidity: roleChangeValidity(row.AppliedValidPeriod), AppliedMembershipVersion: row.AppliedMembershipVersion,
	}, nil
}

type roleChangeConfiguration struct {
	role     sqlcgen.LockRoleAssignmentCandidateRow
	snapshot []application.RoleChangePermission
	bytes    []byte
	hash     []byte
	option   application.PrivilegedRoleOption
}

func loadRoleChangeConfiguration(ctx context.Context, q *sqlcgen.Queries, tenantID uuid.UUID, role sqlcgen.LockRoleAssignmentCandidateRow) (roleChangeConfiguration, bool, error) {
	if !application.ProtectedPrivilegedRole(role.Code) || !role.IsSystemRole {
		return roleChangeConfiguration{}, false, nil
	}
	tpl, ok := application.RoleTemplateByCode(role.Code)
	if !ok || tpl.Scope != application.ScopeTenant {
		return roleChangeConfiguration{}, false, nil
	}
	rows, err := q.ListPrivilegedRolePermissions(ctx, sqlcgen.ListPrivilegedRolePermissionsParams{TenantID: tenantID, RoleID: role.ID})
	if err != nil {
		return roleChangeConfiguration{}, false, fmt.Errorf("identity: list privileged role permissions: %w", err)
	}
	if len(rows) == 0 || len(rows) != len(tpl.Permissions) {
		return roleChangeConfiguration{}, false, nil
	}
	expected := append([]string(nil), tpl.Permissions...)
	sort.Strings(expected)
	config := roleChangeConfiguration{role: role, snapshot: make([]application.RoleChangePermission, 0, len(rows))}
	hasPrivileged, hasSensitive := false, false
	for i, row := range rows {
		if row.PermissionCode != expected[i] {
			return roleChangeConfiguration{}, false, nil
		}
		if row.Sensitivity == "PRIVILEGED" {
			hasPrivileged = true
		}
		if row.Sensitivity != "NORMAL" {
			hasSensitive = true
		}
		if row.Sensitivity != "NORMAL" && row.Sensitivity != "SENSITIVE" && row.Sensitivity != "PRIVILEGED" {
			return roleChangeConfiguration{}, false, nil
		}
		config.snapshot = append(config.snapshot, application.RoleChangePermission{Code: row.PermissionCode, Sensitivity: row.Sensitivity})
	}
	if !hasPrivileged {
		return roleChangeConfiguration{}, false, nil
	}
	config.bytes, err = json.Marshal(config.snapshot)
	if err != nil {
		return roleChangeConfiguration{}, false, err
	}
	canonical, err := json.Marshal(struct {
		Version     int                                `json:"version"`
		RoleID      uuid.UUID                          `json:"roleId"`
		Code        string                             `json:"code"`
		System      bool                               `json:"system"`
		Scope       string                             `json:"scope"`
		Permissions []application.RoleChangePermission `json:"permissions"`
	}{1, role.ID, role.Code, true, application.ScopeTenant, config.snapshot})
	if err != nil {
		return roleChangeConfiguration{}, false, err
	}
	sum := sha256.Sum256(append([]byte("kapsora-role-change-config-v1\x00"), canonical...))
	config.hash = sum[:]
	config.option = application.PrivilegedRoleOption{Code: role.Code, Name: role.Name, Description: tpl.Description, ScopeType: application.ScopeTenant,
		PermissionCodes: expected, HasSensitivePermissions: hasSensitive, ConfigurationHash: hex.EncodeToString(config.hash), RequiresApproval: true}
	return config, true, nil
}

func (r *PrivilegedRoleChangeRepository) checkSessionAt(ctx context.Context, q *sqlcgen.Queries, rc identity.RequestContext, at time.Time, stepUp bool) error {
	if rc.SessionID == "" {
		return identity.ErrUnauthenticated
	}
	row, err := q.LockRoleAssignmentSession(ctx, domain.TokenHash(rc.SessionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.ErrUnauthenticated
	}
	if err != nil {
		return fmt.Errorf("identity: lock role change session: %w", err)
	}
	if row.RevokedAt != nil || row.ActorID != rc.Principal.ActorID || !at.Before(row.ExpiresAt) || at.Sub(row.LastSeenAt) > r.idleTimeout {
		return identity.ErrUnauthenticated
	}
	if !row.ActiveTenantID.Valid || row.ActiveTenantID.UUID != rc.TenantID {
		return identity.ErrPermissionDenied
	}
	if stepUp && (row.StepUpUntil == nil || !at.Before(*row.StepUpUntil)) {
		return identity.ErrStepUpRequired
	}
	return nil
}

func waitRoleChangeSession(ctx context.Context, q *sqlcgen.Queries, rc identity.RequestContext) error {
	if rc.SessionID == "" {
		return identity.ErrUnauthenticated
	}
	_, err := q.LockRoleAssignmentSession(ctx, domain.TokenHash(rc.SessionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return identity.ErrUnauthenticated
	}
	return err
}
func (r *PrivilegedRoleChangeRepository) authorizeAt(ctx context.Context, q *sqlcgen.Queries, rc identity.RequestContext, at time.Time, stepUp bool) error {
	if rc.App != identity.AppAny && rc.App != identity.AppBackoffice {
		return identity.ErrPermissionDenied
	}
	if stepUp && rc.ClientType != identity.ClientBrowser {
		return identity.ErrPermissionDenied
	}
	if err := r.checkSessionAt(ctx, q, rc, at, stepUp); err != nil {
		return err
	}
	allowed, err := q.RoleChangeCurrentAuthorityAt(ctx, sqlcgen.RoleChangeCurrentAuthorityAtParams{TenantID: rc.TenantID, ID: rc.MembershipID, ActorID: rc.Principal.ActorID, Column4: at})
	if err != nil {
		return fmt.Errorf("identity: check role change authority: %w", err)
	}
	if !allowed {
		return identity.ErrPermissionDenied
	}
	return nil
}
func (r *PrivilegedRoleChangeRepository) Authorize(ctx context.Context, rc identity.RequestContext, stepUp bool) error {
	return db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		at, err := q.RoleAssignmentNow(ctx)
		if err != nil {
			return err
		}
		return r.authorizeAt(ctx, q, rc, at, stepUp)
	})
}

func (r *PrivilegedRoleChangeRepository) Options(ctx context.Context, rc identity.RequestContext) ([]application.PrivilegedRoleOption, error) {
	items := make([]application.PrivilegedRoleOption, 0)
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if err := lockRoleAssignmentTenant(ctx, q, rc.TenantID); err != nil {
			return err
		}
		at, err := q.RoleAssignmentNow(ctx)
		if err != nil {
			return err
		}
		if err := r.authorizeAt(ctx, q, rc, at, false); err != nil {
			return err
		}
		roles, err := q.ListPrivilegedRoleCandidates(ctx, sqlcgen.ListPrivilegedRoleCandidatesParams{TenantID: rc.TenantID, Column2: application.PrivilegedRoleCodes()})
		if err != nil {
			return err
		}
		for _, role := range roles {
			config, ok, err := loadRoleChangeConfiguration(ctx, q, rc.TenantID, sqlcgen.LockRoleAssignmentCandidateRow(role))
			if err != nil {
				return err
			}
			if ok {
				items = append(items, config.option)
			}
		}
		return nil
	})
	return items, err
}

func (r *PrivilegedRoleChangeRepository) receipt(ctx context.Context, q *sqlcgen.Queries, rc identity.RequestContext, cmd application.RoleChangeCommand) (*application.RoleChangeResponse, error) {
	row, err := q.RoleChangeReceiptGet(ctx, sqlcgen.RoleChangeReceiptGetParams{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID, CommandCode: cmd.Code, KeyHash: cmd.KeyHash})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("identity: read role change receipt: %w", err)
	}
	if !bytes.Equal(row.RequestHash, cmd.RequestHash) {
		return nil, application.ErrRoleChangeKeyReused
	}
	return &application.RoleChangeResponse{Status: int(row.ResponseStatus), ETag: row.ResponseEtag, Body: row.ResponseBody, Replayed: true}, nil
}

func roleChangeSeparation(rc identity.RequestContext, row sqlcgen.IamRoleChangeRequest, code string) error {
	if code == "CANCEL" {
		if rc.Principal.ActorID != row.MakerActorID {
			return application.ErrRoleChangeCancelForbidden
		}
		return nil
	}
	if code == "APPROVE" || code == "REJECT" {
		if rc.Principal.ActorID == row.MakerActorID || rc.Principal.ActorID == row.TargetActorID {
			return application.ErrRoleChangeSameActor
		}
	}
	return nil
}

func (r *PrivilegedRoleChangeRepository) Preflight(ctx context.Context, rc identity.RequestContext, cmd application.RoleChangeCommand) (*application.RoleChangeResponse, error) {
	var out *application.RoleChangeResponse
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if err := lockRoleAssignmentTenant(ctx, q, rc.TenantID); err != nil {
			return err
		}
		if err := waitRoleChangeSession(ctx, q, rc); err != nil {
			return err
		}
		at, err := q.RoleAssignmentNow(ctx)
		if err != nil {
			return err
		}
		if err := r.authorizeAt(ctx, q, rc, at, true); err != nil {
			return err
		}
		if cmd.Code == "CREATE" {
			var actorID uuid.UUID
			err := tx.QueryRow(ctx, `SELECT actor_id FROM iam.tenant_membership WHERE tenant_id=$1 AND id=$2`, rc.TenantID, cmd.TargetMembershipID).Scan(&actorID)
			if errors.Is(err, pgx.ErrNoRows) {
				return application.ErrRoleAssignmentNotFound
			}
			if err != nil {
				return err
			}
			if actorID == rc.Principal.ActorID {
				return application.ErrRoleAssignmentSelf
			}
		} else {
			row, err := q.RoleChangeGet(ctx, sqlcgen.RoleChangeGetParams{TenantID: rc.TenantID, ID: cmd.RequestID})
			if errors.Is(err, pgx.ErrNoRows) {
				return application.ErrRoleAssignmentNotFound
			}
			if err != nil {
				return err
			}
			if err := roleChangeSeparation(rc, row, cmd.Code); err != nil {
				return err
			}
		}
		at, err = q.RoleAssignmentNow(ctx)
		if err != nil {
			return err
		}
		if err := r.authorizeAt(ctx, q, rc, at, true); err != nil {
			return err
		}
		out, err = r.receipt(ctx, q, rc, cmd)
		return err
	})
	if errors.Is(err, application.ErrRoleChangeSameActor) {
		err = r.recordRoleChangeSameActorDenial(ctx, rc, cmd, err)
	}
	return out, err
}

func (r *PrivilegedRoleChangeRepository) recordRoleChangeSameActorDenial(ctx context.Context, rc identity.RequestContext, cmd application.RoleChangeCommand, cause error) error {
	action := "role_change_request.approve"
	if cmd.Code == "REJECT" {
		action = "role_change_request.reject"
	}
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		return r.audit.Record(ctx, tx, audit.Event{TenantID: uuid.NullUUID{UUID: rc.TenantID, Valid: true}, ActorID: uuid.NullUUID{UUID: rc.Principal.ActorID, Valid: true}, MembershipID: uuid.NullUUID{UUID: rc.MembershipID, Valid: true}, Category: audit.CategorySecurity, ActionCode: action, ResourceType: "role_change_request", ResourceID: uuid.NullUUID{UUID: cmd.RequestID, Valid: true}, Outcome: audit.OutcomeDenied, ReasonCode: "MAKER_CHECKER_SAME_ACTOR", Detail: map[string]any{"request_id": cmd.RequestID.String()}})
	})
	if err != nil {
		return fmt.Errorf("identity: record same-actor role decision denial: %w", err)
	}
	return cause
}

func (r *PrivilegedRoleChangeRepository) Eligibility(ctx context.Context, rc identity.RequestContext, membershipID uuid.UUID) (application.RoleChangeEligibility, error) {
	out := application.RoleChangeEligibility{MembershipID: membershipID, RevokeGrantIDs: []uuid.UUID{}, CheckerAvailability: "NO_ELIGIBLE_CHECKER"}
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if err := lockRoleAssignmentTenant(ctx, q, rc.TenantID); err != nil {
			return err
		}
		if err := waitRoleChangeSession(ctx, q, rc); err != nil {
			return err
		}
		at, err := q.RoleAssignmentNow(ctx)
		if err != nil {
			return err
		}
		if err := r.authorizeAt(ctx, q, rc, at, false); err != nil {
			return err
		}
		target, err := q.LockRoleAssignmentTargetRead(ctx, sqlcgen.LockRoleAssignmentTargetReadParams{TenantID: rc.TenantID, ID: membershipID})
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrRoleAssignmentNotFound
		}
		if err != nil {
			return err
		}
		out.MembershipRowVersion = target.RowVersion
		if err := roleAssignmentTargetState(target.ActorID, rc.Principal.ActorID, target.MembershipStatus, target.MembershipValid, target.ActorType, target.ActorStatus); err != nil {
			out.AssignmentRefusalCode = roleChangeRefusal(err)
		} else {
			pending, err := q.RoleChangePendingForTarget(ctx, sqlcgen.RoleChangePendingForTargetParams{TenantID: rc.TenantID, TargetMembershipID: membershipID})
			if err != nil {
				return err
			}
			person, err := q.RoleChangeHasPersonHistory(ctx, sqlcgen.RoleChangeHasPersonHistoryParams{TenantID: rc.TenantID, TenantMembershipID: membershipID})
			if err != nil {
				return err
			}
			count, err := q.RoleChangeCountCurrentFutureGrantsAt(ctx, sqlcgen.RoleChangeCountCurrentFutureGrantsAtParams{TenantID: rc.TenantID, TenantMembershipID: membershipID, Column3: at})
			if err != nil {
				return err
			}
			switch {
			case pending:
				out.AssignmentRefusalCode = strptr("ROLE_CHANGE_PENDING_EXISTS")
			case person, count > 0:
				out.AssignmentRefusalCode = strptr("EXISTING_ACCESS_CONFLICT")
			default:
				out.CanRequestAssignment = true
			}
			if !pending && !person && count == 1 {
				rows, err := tx.Query(ctx, `SELECT g.id,r.id,r.code,r.name,r.is_system_role FROM iam.access_grant g JOIN iam.role r ON r.tenant_id=g.tenant_id AND r.id=g.role_id WHERE g.tenant_id=$1 AND g.tenant_membership_id=$2 AND g.scope_type='TENANT' AND g.scope_id IS NULL AND g.valid_period @> $3::timestamptz`, rc.TenantID, membershipID, at)
				if err != nil {
					return err
				}
				type candidate struct {
					grantID, roleID uuid.UUID
					code, name      string
					system          bool
				}
				candidates := make([]candidate, 0, 1)
				for rows.Next() {
					var c candidate
					if err := rows.Scan(&c.grantID, &c.roleID, &c.code, &c.name, &c.system); err != nil {
						rows.Close()
						return err
					}
					candidates = append(candidates, c)
				}
				if err := rows.Err(); err != nil {
					rows.Close()
					return err
				}
				rows.Close()
				for _, c := range candidates {
					_, valid, err := loadRoleChangeConfiguration(ctx, q, rc.TenantID, sqlcgen.LockRoleAssignmentCandidateRow{ID: c.roleID, Code: c.code, Name: c.name, IsSystemRole: c.system})
					if err != nil {
						return err
					}
					if valid {
						out.RevokeGrantIDs = append(out.RevokeGrantIDs, c.grantID)
					}
				}
			}
		}
		available, err := q.RoleChangeCheckerAvailable(ctx, sqlcgen.RoleChangeCheckerAvailableParams{TenantID: rc.TenantID, ID: rc.Principal.ActorID, ID_2: target.ActorID, Column4: at})
		if err != nil {
			return err
		}
		if available {
			out.CheckerAvailability = "AVAILABLE"
		}
		return nil
	})
	return out, err
}

func strptr(s string) *string { return &s }
func roleChangeRefusal(err error) *string {
	switch {
	case errors.Is(err, application.ErrRoleAssignmentSelf):
		return strptr("SELF_ROLE_CHANGE_FORBIDDEN")
	case errors.Is(err, application.ErrRoleMembershipConflict):
		return strptr("MEMBERSHIP_STATE_CONFLICT")
	case errors.Is(err, application.ErrExistingAccessConflict):
		return strptr("EXISTING_ACCESS_CONFLICT")
	case errors.Is(err, application.ErrRoleChangePendingExists):
		return strptr("ROLE_CHANGE_PENDING_EXISTS")
	case errors.Is(err, application.ErrRoleChangeSameActor):
		return strptr("MAKER_CHECKER_SAME_ACTOR")
	case errors.Is(err, application.ErrRoleChangeCancelForbidden):
		return strptr("ROLE_CHANGE_CANCEL_FORBIDDEN")
	case errors.Is(err, application.ErrRoleChangeNotPending):
		return strptr("ROLE_CHANGE_NOT_PENDING")
	default:
		return strptr("ROLE_CHANGE_TARGET_CHANGED")
	}
}

func (r *PrivilegedRoleChangeRepository) List(ctx context.Context, rc identity.RequestContext, status string, membershipID *uuid.UUID, after *httpx.Cursor, limit int) ([]application.RoleChangeRequestSummary, error) {
	out := make([]application.RoleChangeRequestSummary, 0)
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if err := lockRoleAssignmentTenant(ctx, q, rc.TenantID); err != nil {
			return err
		}
		at, err := q.RoleAssignmentNow(ctx)
		if err != nil {
			return err
		}
		if err := r.authorizeAt(ctx, q, rc, at, false); err != nil {
			return err
		}
		if limit < 1 || limit > 101 {
			return application.ErrRoleAssignmentInvalid
		}
		arg := sqlcgen.RoleChangeListParams{TenantID: rc.TenantID, Status: status, PageLimit: int32(limit)}
		if membershipID != nil {
			arg.MembershipID = uuid.NullUUID{UUID: *membershipID, Valid: true}
		}
		if after != nil {
			arg.AfterAt = &after.CreatedAt
			arg.AfterID = uuid.NullUUID{UUID: after.ID, Valid: true}
		}
		rows, err := q.RoleChangeList(ctx, arg)
		if err != nil {
			return err
		}
		for _, row := range rows {
			out = append(out, application.RoleChangeRequestSummary{ID: row.ID, Operation: row.Operation, Status: row.Status, TargetMembershipID: row.TargetMembershipID, MakerMembershipID: row.MakerMembershipID, RoleCode: row.RoleCode, ScopeType: row.ScopeType, ReasonCode: row.ReasonCode, CreatedAt: row.CreatedAt, RowVersion: row.RowVersion, DecidedAt: row.DecidedAt})
		}
		return nil
	})
	return out, err
}

func (r *PrivilegedRoleChangeRepository) Detail(ctx context.Context, rc identity.RequestContext, requestID uuid.UUID) (application.RoleChangeRequestDetail, error) {
	var out application.RoleChangeRequestDetail
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if err := lockRoleAssignmentTenant(ctx, q, rc.TenantID); err != nil {
			return err
		}
		at, err := q.RoleAssignmentNow(ctx)
		if err != nil {
			return err
		}
		if err := r.authorizeAt(ctx, q, rc, at, false); err != nil {
			return err
		}
		row, err := q.RoleChangeGet(ctx, sqlcgen.RoleChangeGetParams{TenantID: rc.TenantID, ID: requestID})
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrRoleAssignmentNotFound
		}
		if err != nil {
			return err
		}
		out.Request, err = roleChangeRequestView(row)
		if err != nil {
			return err
		}
		available, err := q.RoleChangeCheckerAvailable(ctx, sqlcgen.RoleChangeCheckerAvailableParams{TenantID: rc.TenantID, ID: row.MakerActorID, ID_2: row.TargetActorID, Column4: at})
		if err != nil {
			return err
		}
		out.CheckerAvailability = "NO_ELIGIBLE_CHECKER"
		if available {
			out.CheckerAvailability = "AVAILABLE"
		}
		if row.Status != "PENDING" {
			out.ApprovalRefusalCode = strptr("ROLE_CHANGE_NOT_PENDING")
			out.RejectionRefusalCode = strptr("ROLE_CHANGE_NOT_PENDING")
			out.CancellationRefusalCode = strptr("ROLE_CHANGE_NOT_PENDING")
			return nil
		}
		if rc.Principal.ActorID == row.MakerActorID || rc.Principal.ActorID == row.TargetActorID {
			out.ApprovalRefusalCode = strptr("MAKER_CHECKER_SAME_ACTOR")
			out.RejectionRefusalCode = strptr("MAKER_CHECKER_SAME_ACTOR")
		} else {
			out.ApprovalRefusalCode, err = roleChangeApprovalRefusal(ctx, q, rc, row, at)
			if err != nil {
				return err
			}
			out.CanApprove = out.ApprovalRefusalCode == nil
			out.CanReject = true
		}
		if rc.Principal.ActorID == row.MakerActorID {
			out.CanCancel = true
		} else {
			out.CancellationRefusalCode = strptr("ROLE_CHANGE_CANCEL_FORBIDDEN")
		}
		return nil
	})
	return out, err
}

func roleChangeApprovalRefusal(ctx context.Context, q *sqlcgen.Queries, rc identity.RequestContext, row sqlcgen.IamRoleChangeRequest, at time.Time) (*string, error) {
	makerOK, err := q.RoleChangeCurrentAuthorityAt(ctx, sqlcgen.RoleChangeCurrentAuthorityAtParams{TenantID: rc.TenantID, ID: row.MakerMembershipID, ActorID: row.MakerActorID, Column4: at})
	if err != nil {
		return nil, err
	}
	if !makerOK {
		return strptr("ROLE_CHANGE_MAKER_UNAUTHORIZED"), nil
	}
	target, err := q.RoleChangeLockMembership(ctx, sqlcgen.RoleChangeLockMembershipParams{TenantID: rc.TenantID, ID: row.TargetMembershipID, Column3: at})
	if errors.Is(err, pgx.ErrNoRows) {
		return strptr("ROLE_CHANGE_TARGET_CHANGED"), nil
	}
	if err != nil {
		return nil, err
	}
	if roleChangeMemberState(target, row.TargetActorID) != nil || target.RowVersion != row.TargetMembershipVersion {
		return strptr("ROLE_CHANGE_TARGET_CHANGED"), nil
	}
	role, err := q.LockRoleAssignmentCandidate(ctx, sqlcgen.LockRoleAssignmentCandidateParams{TenantID: rc.TenantID, ID: row.RoleID})
	if errors.Is(err, pgx.ErrNoRows) {
		return strptr("ROLE_CHANGE_CONFIGURATION_CHANGED"), nil
	}
	if err != nil {
		return nil, err
	}
	config, valid, err := loadRoleChangeConfiguration(ctx, q, rc.TenantID, role)
	if err != nil {
		return nil, err
	}
	if !valid || role.Code != row.RoleCode || !bytes.Equal(config.bytes, row.PermissionSnapshot) || !bytes.Equal(config.hash, row.ConfigurationHash) {
		return strptr("ROLE_CHANGE_CONFIGURATION_CHANGED"), nil
	}
	person, err := q.RoleChangeHasPersonHistory(ctx, sqlcgen.RoleChangeHasPersonHistoryParams{TenantID: rc.TenantID, TenantMembershipID: row.TargetMembershipID})
	if err != nil {
		return nil, err
	}
	count, err := q.RoleChangeCountCurrentFutureGrantsAt(ctx, sqlcgen.RoleChangeCountCurrentFutureGrantsAtParams{TenantID: rc.TenantID, TenantMembershipID: row.TargetMembershipID, Column3: at})
	if err != nil {
		return nil, err
	}
	if person || (row.Operation == "ASSIGN" && count != 0) || (row.Operation == "REVOKE" && count != 1) {
		return strptr("ROLE_CHANGE_TARGET_CHANGED"), nil
	}
	if row.Operation == "REVOKE" {
		grant, err := q.RoleChangeGetGrantForUpdate(ctx, sqlcgen.RoleChangeGetGrantForUpdateParams{TenantID: rc.TenantID, TenantMembershipID: row.TargetMembershipID, ID: row.RevokeGrantID.UUID, Column4: at})
		if errors.Is(err, pgx.ErrNoRows) {
			return strptr("ROLE_CHANGE_TARGET_CHANGED"), nil
		}
		if err != nil {
			return nil, err
		}
		if grant.RoleID != row.RoleID || grant.ScopeType != "TENANT" || grant.ScopeID.Valid || !boolValue(grant.CurrentGrant) || !roleChangeRangeEqual(grant.ValidPeriod, row.RevokeValidPeriod) {
			return strptr("ROLE_CHANGE_TARGET_CHANGED"), nil
		}
	}
	return nil, nil
}

func roleChangeLockActors(ctx context.Context, q *sqlcgen.Queries, ids ...uuid.UUID) error {
	unique := map[uuid.UUID]bool{}
	for _, id := range ids {
		unique[id] = true
	}
	ordered := make([]uuid.UUID, 0, len(unique))
	for id := range unique {
		ordered = append(ordered, id)
	}
	sort.Slice(ordered, func(i, j int) bool { return bytes.Compare(ordered[i][:], ordered[j][:]) < 0 })
	for _, id := range ordered {
		actor, err := q.LockRoleAssignmentActor(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrRoleChangeTargetChanged
		}
		if err != nil {
			return err
		}
		_ = actor // Status is checked only for actors whose current state matters to this action.
	}
	return nil
}
func roleChangeLockMembers(ctx context.Context, q *sqlcgen.Queries, tenantID uuid.UUID, at time.Time, ids ...uuid.UUID) (map[uuid.UUID]sqlcgen.RoleChangeLockMembershipRow, error) {
	unique := map[uuid.UUID]bool{}
	for _, id := range ids {
		unique[id] = true
	}
	ordered := make([]uuid.UUID, 0, len(unique))
	for id := range unique {
		ordered = append(ordered, id)
	}
	sort.Slice(ordered, func(i, j int) bool { return bytes.Compare(ordered[i][:], ordered[j][:]) < 0 })
	out := make(map[uuid.UUID]sqlcgen.RoleChangeLockMembershipRow, len(ordered))
	for _, id := range ordered {
		row, err := q.RoleChangeLockMembership(ctx, sqlcgen.RoleChangeLockMembershipParams{TenantID: tenantID, ID: id, Column3: at})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, application.ErrRoleAssignmentNotFound
		}
		if err != nil {
			return nil, err
		}
		out[id] = row
	}
	return out, nil
}
func roleChangeMemberState(row sqlcgen.RoleChangeLockMembershipRow, actorID uuid.UUID) error {
	if row.ActorID != actorID || row.ActorType != "HUMAN" || row.ActorStatus != "ACTIVE" || row.MembershipStatus != "ACTIVE" || !boolValue(row.MembershipValid) {
		return application.ErrRoleChangeTargetChanged
	}
	return nil
}
func roleChangeRangeEqual(a, b pgtype.Range[pgtype.Timestamptz]) bool {
	if a.Valid != b.Valid || a.LowerType != b.LowerType || a.UpperType != b.UpperType {
		return false
	}
	if a.Lower.Valid != b.Lower.Valid || a.Upper.Valid != b.Upper.Valid {
		return false
	}
	if a.Lower.Valid && !a.Lower.Time.Equal(b.Lower.Time) {
		return false
	}
	if a.Upper.Valid && !a.Upper.Time.Equal(b.Upper.Time) {
		return false
	}
	return true
}

func (r *PrivilegedRoleChangeRepository) Execute(ctx context.Context, rc identity.RequestContext, cmd application.RoleChangeCommand) (application.RoleChangeResponse, error) {
	var out application.RoleChangeResponse
	err := db.WithTenantTx(ctx, r.pool, db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		if err := lockRoleAssignmentTenant(ctx, q, rc.TenantID); err != nil {
			return err
		}
		// The session row is retained FOR SHARE through commit; its time predicates are
		// repeated after all other lock waits with one database wall-clock value.
		at, err := q.RoleAssignmentNow(ctx)
		if err != nil {
			return err
		}
		if err := r.authorizeAt(ctx, q, rc, at, true); err != nil {
			return err
		}
		var prior sqlcgen.IamRoleChangeRequest
		if cmd.Code != "CREATE" {
			prior, err = q.RoleChangeGet(ctx, sqlcgen.RoleChangeGetParams{TenantID: rc.TenantID, ID: cmd.RequestID})
			if errors.Is(err, pgx.ErrNoRows) {
				return application.ErrRoleAssignmentNotFound
			}
			if err != nil {
				return err
			}
			if err := roleChangeSeparation(rc, prior, cmd.Code); err != nil {
				return err
			}
		}
		at, err = q.RoleAssignmentNow(ctx)
		if err != nil {
			return err
		}
		if err := r.authorizeAt(ctx, q, rc, at, true); err != nil {
			return err
		}
		if replay, err := r.receipt(ctx, q, rc, cmd); err != nil {
			return err
		} else if replay != nil {
			out = *replay
			return nil
		}
		var row sqlcgen.IamRoleChangeRequest
		if cmd.Code == "CREATE" {
			row, err = r.create(ctx, tx, q, rc, cmd)
		} else {
			row, err = r.decide(ctx, tx, q, rc, cmd, prior)
		}
		if err != nil {
			return err
		}
		view, err := roleChangeRequestView(row)
		if err != nil {
			return err
		}
		result := application.RoleChangeCommandResult{Request: view, MembershipRowVersion: row.AppliedMembershipVersion}
		if row.Status == "APPROVED" {
			role, err := q.LockRoleAssignmentCandidate(ctx, sqlcgen.LockRoleAssignmentCandidateParams{TenantID: rc.TenantID, ID: row.RoleID})
			if err != nil {
				return err
			}
			valid := roleChangeValidity(row.AppliedValidPeriod)
			result.AppliedGrant = &application.RoleChangeAppliedGrant{ID: row.AppliedGrantID.UUID, RoleCode: row.RoleCode, RoleName: role.Name, IsSystemRole: role.IsSystemRole, ScopeType: application.ScopeTenant, ValidFrom: valid.From, ValidTo: valid.To, ValidityEmpty: false}
		}
		body, err := json.Marshal(result)
		if err != nil {
			return err
		}
		body = append(body, '\n')
		status := 200
		if cmd.Code == "CREATE" {
			status = 201
		}
		out = application.RoleChangeResponse{Status: status, ETag: fmt.Sprintf(`"%d"`, row.RowVersion), Body: body}
		if err := q.RoleChangeReceiptInsert(ctx, sqlcgen.RoleChangeReceiptInsertParams{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID, CommandCode: cmd.Code, KeyHash: cmd.KeyHash, RequestHash: cmd.RequestHash, RequestID: row.ID, ResponseStatus: int32(status), ResponseEtag: out.ETag, ResponseBody: body}); err != nil {
			return fmt.Errorf("identity: save role change receipt: %w", err)
		}
		return nil
	})
	if errors.Is(err, application.ErrRoleChangeSameActor) {
		err = r.recordRoleChangeSameActorDenial(ctx, rc, cmd, err)
	}
	return out, err
}

func (r *PrivilegedRoleChangeRepository) roleChangeAudit(ctx context.Context, tx pgx.Tx, rc identity.RequestContext, action, resource string, id uuid.UUID, reason string, detail map[string]any) error {
	return r.audit.Record(ctx, tx, audit.Event{TenantID: uuid.NullUUID{UUID: rc.TenantID, Valid: true}, ActorID: uuid.NullUUID{UUID: rc.Principal.ActorID, Valid: true}, MembershipID: uuid.NullUUID{UUID: rc.MembershipID, Valid: true}, Category: audit.CategoryAdmin, ActionCode: action, ResourceType: resource, ResourceID: uuid.NullUUID{UUID: id, Valid: true}, Outcome: audit.OutcomeSuccess, ReasonCode: reason, Detail: detail})
}

func (r *PrivilegedRoleChangeRepository) create(ctx context.Context, tx pgx.Tx, q *sqlcgen.Queries, rc identity.RequestContext, cmd application.RoleChangeCommand) (sqlcgen.IamRoleChangeRequest, error) {
	var zero sqlcgen.IamRoleChangeRequest
	var targetActor uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT actor_id FROM iam.tenant_membership WHERE tenant_id=$1 AND id=$2`, rc.TenantID, cmd.TargetMembershipID).Scan(&targetActor); errors.Is(err, pgx.ErrNoRows) {
		return zero, application.ErrRoleAssignmentNotFound
	} else if err != nil {
		return zero, err
	}
	if targetActor == rc.Principal.ActorID {
		return zero, application.ErrRoleAssignmentSelf
	}
	var roleID uuid.UUID
	if cmd.Operation == "ASSIGN" {
		candidate, err := q.GetRoleAssignmentCandidate(ctx, sqlcgen.GetRoleAssignmentCandidateParams{TenantID: rc.TenantID, Code: cmd.RoleCode})
		if errors.Is(err, pgx.ErrNoRows) {
			return zero, application.ErrRoleConfigurationUnsupported
		}
		if err != nil {
			return zero, err
		}
		roleID = candidate.ID
	} else {
		id, err := q.GetTenantRoleGrantRoleID(ctx, sqlcgen.GetTenantRoleGrantRoleIDParams{TenantID: rc.TenantID, TenantMembershipID: cmd.TargetMembershipID, ID: cmd.GrantID})
		if errors.Is(err, pgx.ErrNoRows) {
			return zero, application.ErrRoleAssignmentNotFound
		}
		if err != nil {
			return zero, err
		}
		roleID = id
	}
	if err := roleChangeLockActors(ctx, q, rc.Principal.ActorID, targetActor); err != nil {
		return zero, err
	}
	pre, err := q.RoleAssignmentNow(ctx)
	if err != nil {
		return zero, err
	}
	if _, err := roleChangeLockMembers(ctx, q, rc.TenantID, pre, rc.MembershipID, cmd.TargetMembershipID); err != nil {
		return zero, err
	}
	pending, err := q.RoleChangePendingForTarget(ctx, sqlcgen.RoleChangePendingForTargetParams{TenantID: rc.TenantID, TargetMembershipID: cmd.TargetMembershipID})
	if err != nil {
		return zero, err
	}
	if pending {
		return zero, application.ErrRoleChangePendingExists
	}
	role, err := q.LockRoleAssignmentCandidate(ctx, sqlcgen.LockRoleAssignmentCandidateParams{TenantID: rc.TenantID, ID: roleID})
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, application.ErrRoleConfigurationUnsupported
	}
	if err != nil {
		return zero, err
	}
	config, valid, err := loadRoleChangeConfiguration(ctx, q, rc.TenantID, role)
	if err != nil {
		return zero, err
	}
	if !valid {
		return zero, application.ErrRoleConfigurationUnsupported
	}
	if hex.EncodeToString(config.hash) != cmd.ConfigurationHash {
		return zero, application.ErrRoleChangeConfigurationChanged
	}
	var grant sqlcgen.RoleChangeGetGrantForUpdateRow
	if cmd.Operation == "REVOKE" {
		grant, err = q.RoleChangeGetGrantForUpdate(ctx, sqlcgen.RoleChangeGetGrantForUpdateParams{TenantID: rc.TenantID, TenantMembershipID: cmd.TargetMembershipID, ID: cmd.GrantID, Column4: pre})
		if errors.Is(err, pgx.ErrNoRows) {
			return zero, application.ErrRoleAssignmentNotFound
		}
		if err != nil {
			return zero, err
		}
	}
	at, err := q.RoleAssignmentNow(ctx)
	if err != nil {
		return zero, err
	}
	if err := r.authorizeAt(ctx, q, rc, at, true); err != nil {
		return zero, err
	}
	target, err := q.RoleChangeLockMembership(ctx, sqlcgen.RoleChangeLockMembershipParams{TenantID: rc.TenantID, ID: cmd.TargetMembershipID, Column3: at})
	if err != nil {
		return zero, err
	}
	if err := roleChangeMemberState(target, targetActor); err != nil {
		return zero, err
	}
	if target.RowVersion != cmd.Version {
		return zero, application.ErrRoleAssignmentVersion
	}
	person, err := q.RoleChangeHasPersonHistory(ctx, sqlcgen.RoleChangeHasPersonHistoryParams{TenantID: rc.TenantID, TenantMembershipID: cmd.TargetMembershipID})
	if err != nil {
		return zero, err
	}
	count, err := q.RoleChangeCountCurrentFutureGrantsAt(ctx, sqlcgen.RoleChangeCountCurrentFutureGrantsAtParams{TenantID: rc.TenantID, TenantMembershipID: cmd.TargetMembershipID, Column3: at})
	if err != nil {
		return zero, err
	}
	if person || (cmd.Operation == "ASSIGN" && count != 0) || (cmd.Operation == "REVOKE" && count != 1) {
		return zero, application.ErrExistingAccessConflict
	}
	// A proposal remains pending even when no eligible second manager exists yet.
	var revokeID uuid.NullUUID
	var revokePeriod pgtype.Range[pgtype.Timestamptz]
	if cmd.Operation == "REVOKE" {
		grant, err = q.RoleChangeGetGrantForUpdate(ctx, sqlcgen.RoleChangeGetGrantForUpdateParams{TenantID: rc.TenantID, TenantMembershipID: cmd.TargetMembershipID, ID: cmd.GrantID, Column4: at})
		if err != nil {
			return zero, err
		}
		if grant.RoleID != roleID || grant.ScopeType != "TENANT" || grant.ScopeID.Valid || !boolValue(grant.CurrentGrant) || !grant.ValidPeriod.Valid {
			return zero, application.ErrRoleGrantConflict
		}
		revokeID = uuid.NullUUID{UUID: cmd.GrantID, Valid: true}
		revokePeriod = grant.ValidPeriod
	}
	row, err := q.RoleChangeInsert(ctx, sqlcgen.RoleChangeInsertParams{TenantID: rc.TenantID, Operation: cmd.Operation, TargetMembershipID: cmd.TargetMembershipID, TargetActorID: targetActor, MakerMembershipID: rc.MembershipID, MakerActorID: rc.Principal.ActorID, RoleID: roleID, RoleCode: role.Code, PermissionSnapshot: config.bytes, ConfigurationHash: config.hash, TargetMembershipVersion: target.RowVersion, RevokeGrantID: revokeID, RevokeValidPeriod: revokePeriod, ReasonCode: cmd.ReasonCode})
	if err != nil {
		return zero, fmt.Errorf("identity: insert role change proposal: %w", err)
	}
	err = r.roleChangeAudit(ctx, tx, rc, "role_change_request.create", "role_change_request", row.ID, cmd.ReasonCode, map[string]any{"target_membership_id": cmd.TargetMembershipID.String(), "role_id": roleID.String(), "role_code": role.Code, "operation": cmd.Operation, "scope_type": "TENANT", "configuration_hash": cmd.ConfigurationHash, "target_version": target.RowVersion, "revoke_grant_id": nullableAuditUUID(revokeID)})
	return row, err
}

func (r *PrivilegedRoleChangeRepository) decide(ctx context.Context, tx pgx.Tx, q *sqlcgen.Queries, rc identity.RequestContext, cmd application.RoleChangeCommand, prior sqlcgen.IamRoleChangeRequest) (sqlcgen.IamRoleChangeRequest, error) {
	var zero sqlcgen.IamRoleChangeRequest
	if err := roleChangeLockActors(ctx, q, rc.Principal.ActorID, prior.MakerActorID, prior.TargetActorID); err != nil {
		return zero, err
	}
	pre, err := q.RoleAssignmentNow(ctx)
	if err != nil {
		return zero, err
	}
	members, err := roleChangeLockMembers(ctx, q, rc.TenantID, pre, rc.MembershipID, prior.MakerMembershipID, prior.TargetMembershipID)
	if err != nil {
		return zero, err
	}
	row, err := q.RoleChangeGetForUpdate(ctx, sqlcgen.RoleChangeGetForUpdateParams{TenantID: rc.TenantID, ID: cmd.RequestID})
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, application.ErrRoleAssignmentNotFound
	}
	if err != nil {
		return zero, err
	}
	if err := roleChangeSeparation(rc, row, cmd.Code); err != nil {
		return zero, err
	}
	if row.RowVersion != cmd.Version {
		return zero, application.ErrRoleAssignmentVersion
	}
	if row.Status != "PENDING" {
		return zero, application.ErrRoleChangeNotPending
	}
	if cmd.Code == "APPROVE" {
		if row.TargetActorID != members[row.TargetMembershipID].ActorID || row.MakerActorID != members[row.MakerMembershipID].ActorID {
			return zero, application.ErrRoleChangeTargetChanged
		}
	}
	var role sqlcgen.LockRoleAssignmentCandidateRow
	var config roleChangeConfiguration
	var grant sqlcgen.RoleChangeGetGrantForUpdateRow
	if cmd.Code == "APPROVE" {
		role, err = q.LockRoleAssignmentCandidate(ctx, sqlcgen.LockRoleAssignmentCandidateParams{TenantID: rc.TenantID, ID: row.RoleID})
		if errors.Is(err, pgx.ErrNoRows) {
			return zero, application.ErrRoleChangeConfigurationChanged
		}
		if err != nil {
			return zero, err
		}
		var valid bool
		config, valid, err = loadRoleChangeConfiguration(ctx, q, rc.TenantID, role)
		if err != nil {
			return zero, err
		}
		if !valid || role.Code != row.RoleCode || !bytes.Equal(config.bytes, row.PermissionSnapshot) || !bytes.Equal(config.hash, row.ConfigurationHash) {
			return zero, application.ErrRoleChangeConfigurationChanged
		}
		if row.Operation == "REVOKE" {
			grant, err = q.RoleChangeGetGrantForUpdate(ctx, sqlcgen.RoleChangeGetGrantForUpdateParams{TenantID: rc.TenantID, TenantMembershipID: row.TargetMembershipID, ID: row.RevokeGrantID.UUID, Column4: pre})
			if errors.Is(err, pgx.ErrNoRows) {
				return zero, application.ErrRoleChangeTargetChanged
			}
			if err != nil {
				return zero, err
			}
		}
	}
	// This wall-clock value is chosen after all potentially contended locks, and reused
	// for every final temporal predicate, mutation bound, decision and audit detail.
	at, err := q.RoleAssignmentNow(ctx)
	if err != nil {
		return zero, err
	}
	if err := r.authorizeAt(ctx, q, rc, at, true); err != nil {
		return zero, err
	}
	params := sqlcgen.RoleChangeDecideParams{TenantID: rc.TenantID, ID: row.ID, DecidedByMembershipID: uuid.NullUUID{UUID: rc.MembershipID, Valid: true}, DecidedByActorID: uuid.NullUUID{UUID: rc.Principal.ActorID, Valid: true}, DecidedAt: &at, RowVersion: row.RowVersion}
	if cmd.Code == "REJECT" || cmd.Code == "CANCEL" {
		if cmd.Code == "REJECT" {
			params.Status = "REJECTED"
		} else {
			params.Status = "CANCELLED"
		}
		params.DecisionReasonCode = &cmd.ReasonCode
	} else {
		params.Status = "APPROVED"
		makerOK, err := q.RoleChangeCurrentAuthorityAt(ctx, sqlcgen.RoleChangeCurrentAuthorityAtParams{TenantID: rc.TenantID, ID: row.MakerMembershipID, ActorID: row.MakerActorID, Column4: at})
		if err != nil {
			return zero, err
		}
		if !makerOK {
			return zero, application.ErrRoleChangeMakerUnauthorized
		}
		target, err := q.RoleChangeLockMembership(ctx, sqlcgen.RoleChangeLockMembershipParams{TenantID: rc.TenantID, ID: row.TargetMembershipID, Column3: at})
		if err != nil {
			return zero, err
		}
		if err := roleChangeMemberState(target, row.TargetActorID); err != nil {
			return zero, err
		}
		if target.RowVersion != row.TargetMembershipVersion {
			return zero, application.ErrRoleChangeTargetChanged
		}
		person, err := q.RoleChangeHasPersonHistory(ctx, sqlcgen.RoleChangeHasPersonHistoryParams{TenantID: rc.TenantID, TenantMembershipID: row.TargetMembershipID})
		if err != nil {
			return zero, err
		}
		count, err := q.RoleChangeCountCurrentFutureGrantsAt(ctx, sqlcgen.RoleChangeCountCurrentFutureGrantsAtParams{TenantID: rc.TenantID, TenantMembershipID: row.TargetMembershipID, Column3: at})
		if err != nil {
			return zero, err
		}
		if person || (row.Operation == "ASSIGN" && count != 0) || (row.Operation == "REVOKE" && count != 1) {
			return zero, application.ErrRoleChangeTargetChanged
		}
		var appliedGrantID uuid.UUID
		var appliedPeriod pgtype.Range[pgtype.Timestamptz]
		beforeUser, beforeRole := int64(0), int64(0)
		if row.Operation == "REVOKE" {
			grant, err = q.RoleChangeGetGrantForUpdate(ctx, sqlcgen.RoleChangeGetGrantForUpdateParams{TenantID: rc.TenantID, TenantMembershipID: row.TargetMembershipID, ID: row.RevokeGrantID.UUID, Column4: at})
			if err != nil {
				return zero, application.ErrRoleChangeTargetChanged
			}
			if grant.RoleID != row.RoleID || grant.ScopeType != "TENANT" || grant.ScopeID.Valid || !boolValue(grant.CurrentGrant) || !roleChangeRangeEqual(grant.ValidPeriod, row.RevokeValidPeriod) {
				return zero, application.ErrRoleChangeTargetChanged
			}
			for _, code := range []string{"identity.user.manage", "identity.role.manage"} {
				n, err := q.RoleChangeCountManagersAt(ctx, sqlcgen.RoleChangeCountManagersAtParams{TenantID: rc.TenantID, PermissionCode: code, Column3: at})
				if err != nil {
					return zero, err
				}
				if code == "identity.user.manage" {
					beforeUser = n
				} else {
					beforeRole = n
				}
			}
			appliedPeriod, err = q.RoleChangeEndGrant(ctx, sqlcgen.RoleChangeEndGrantParams{TenantID: rc.TenantID, TenantMembershipID: row.TargetMembershipID, ID: row.RevokeGrantID.UUID, Column4: at, ValidPeriod: row.RevokeValidPeriod})
			if errors.Is(err, pgx.ErrNoRows) {
				return zero, application.ErrRoleChangeTargetChanged
			}
			if err != nil {
				return zero, err
			}
			appliedGrantID = row.RevokeGrantID.UUID
			for _, code := range []string{"identity.user.manage", "identity.role.manage"} {
				n, err := q.RoleChangeCountManagersAt(ctx, sqlcgen.RoleChangeCountManagersAtParams{TenantID: rc.TenantID, PermissionCode: code, Column3: at})
				if err != nil {
					return zero, err
				}
				if code == "identity.user.manage" && beforeUser > 0 && n < 1 {
					return zero, application.ErrDirectoryLastManager
				}
				if code == "identity.role.manage" && beforeRole > 0 && n < 1 {
					return zero, application.ErrLastTenantRoleManager
				}
			}
		} else {
			created, err := q.RoleChangeInsertGrant(ctx, sqlcgen.RoleChangeInsertGrantParams{TenantID: rc.TenantID, TenantMembershipID: row.TargetMembershipID, RoleID: row.RoleID, Column4: at, GrantedBy: uuid.NullUUID{UUID: rc.Principal.ActorID, Valid: true}})
			if err != nil {
				return zero, err
			}
			appliedGrantID = created.ID
			appliedPeriod = created.ValidPeriod
		}
		version, err := q.TouchRoleAssignmentMembership(ctx, sqlcgen.TouchRoleAssignmentMembershipParams{TenantID: rc.TenantID, ID: row.TargetMembershipID, RowVersion: row.TargetMembershipVersion})
		if errors.Is(err, pgx.ErrNoRows) {
			return zero, application.ErrRoleChangeTargetChanged
		}
		if err != nil {
			return zero, err
		}
		params.AppliedGrantID = uuid.NullUUID{UUID: appliedGrantID, Valid: true}
		params.AppliedMembershipVersion = &version
		params.AppliedValidPeriod = appliedPeriod
		grantAction := "access_grant.assign"
		if row.Operation == "REVOKE" {
			grantAction = "access_grant.revoke"
		}
		grantDetail := map[string]any{"request_id": row.ID.String(), "target_membership_id": row.TargetMembershipID.String(), "role_id": row.RoleID.String(), "role_code": row.RoleCode, "operation": row.Operation, "scope_type": "TENANT", "configuration_hash": hex.EncodeToString(row.ConfigurationHash), "application_at": at.UTC().Format(time.RFC3339Nano), "before_version": row.TargetMembershipVersion, "after_version": version}
		roleChangeAuditRange(grantDetail, "prior_validity", row.RevokeValidPeriod)
		roleChangeAuditRange(grantDetail, "applied_validity", appliedPeriod)
		if err := r.roleChangeAudit(ctx, tx, rc, grantAction, "access_grant", appliedGrantID, row.ReasonCode, grantDetail); err != nil {
			return zero, err
		}
	}
	decided, err := q.RoleChangeDecide(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, application.ErrRoleChangeNotPending
	}
	if err != nil {
		return zero, fmt.Errorf("identity: decide role change: %w", err)
	}
	decisionDetail := map[string]any{"target_membership_id": row.TargetMembershipID.String(), "role_id": row.RoleID.String(), "role_code": row.RoleCode, "operation": row.Operation, "scope_type": "TENANT", "configuration_hash": hex.EncodeToString(row.ConfigurationHash), "application_at": at.UTC().Format(time.RFC3339Nano), "before_request_version": row.RowVersion, "after_request_version": decided.RowVersion, "before_target_version": row.TargetMembershipVersion, "proposal_reason_code": row.ReasonCode}
	if decided.AppliedMembershipVersion != nil {
		decisionDetail["after_target_version"] = *decided.AppliedMembershipVersion
	}
	if decided.AppliedGrantID.Valid {
		decisionDetail["applied_grant_id"] = decided.AppliedGrantID.UUID.String()
	}
	roleChangeAuditRange(decisionDetail, "prior_validity", row.RevokeValidPeriod)
	roleChangeAuditRange(decisionDetail, "applied_validity", decided.AppliedValidPeriod)
	reason := cmd.ReasonCode
	if cmd.Code == "APPROVE" {
		reason = row.ReasonCode
	}
	return decided, r.roleChangeAudit(ctx, tx, rc, "role_change_request."+map[string]string{"APPROVE": "approve", "REJECT": "reject", "CANCEL": "cancel"}[cmd.Code], "role_change_request", row.ID, reason, decisionDetail)
}
