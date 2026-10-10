package identitypg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/identity/domain"
	"github.com/jackc/pgx/v5"
)

type TenantSettingsAuthority struct{ IdleTimeout time.Duration }

func NewTenantSettingsAuthority(idleTimeout time.Duration) *TenantSettingsAuthority {
	if idleTimeout <= 0 {
		idleTimeout = domain.DefaultPolicy().IdleTimeout
	}
	return &TenantSettingsAuthority{IdleTimeout: idleTimeout}
}

var _ application.TenantSettingsAuthority = (*TenantSettingsAuthority)(nil)

func (a *TenantSettingsAuthority) CheckTx(ctx context.Context, tx pgx.Tx, rc identity.RequestContext, manage bool) (bool, error) {
	if rc.App != identity.AppAny && rc.App != identity.AppBackoffice {
		return false, identity.ErrPermissionDenied
	}
	if rc.ClientType != identity.ClientBrowser || rc.SessionID == "" {
		return false, identity.ErrUnauthenticated
	}
	var actorID, activeTenant string
	var lastSeen, expires time.Time
	var stepUp, revoked *time.Time
	err := tx.QueryRow(ctx, `SELECT actor_id::text,coalesce(active_tenant_id::text,''),last_seen_at,expires_at,step_up_until,revoked_at FROM iam.session WHERE id_hash=$1 FOR SHARE`, domain.TokenHash(rc.SessionID)).Scan(&actorID, &activeTenant, &lastSeen, &expires, &stepUp, &revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, identity.ErrUnauthenticated
	}
	if err != nil {
		return false, fmt.Errorf("identity: lock settings session: %w", err)
	}
	if actorID != rc.Principal.ActorID.String() || activeTenant != rc.TenantID.String() {
		return false, identity.ErrUnauthenticated
	}
	var actorType, actorStatus, memberStatus string
	var valid bool
	err = tx.QueryRow(ctx, `SELECT a.actor_type,a.status,m.membership_status,m.valid_period @> clock_timestamp()::date FROM iam.tenant_membership m JOIN iam.actor a ON a.id=m.actor_id WHERE m.tenant_id=$1 AND m.id=$2 AND m.actor_id=$3 FOR SHARE OF a,m`, rc.TenantID, rc.MembershipID, rc.Principal.ActorID).Scan(&actorType, &actorStatus, &memberStatus, &valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, identity.ErrPermissionDenied
	}
	if err != nil {
		return false, fmt.Errorf("identity: lock settings membership: %w", err)
	}
	if actorType != "HUMAN" || actorStatus != "ACTIVE" || memberStatus != "ACTIVE" || !valid {
		return false, identity.ErrPermissionDenied
	}
	// Lock the current grant and permission evidence through commit. IAM grant writers
	// acquire the parent tenant lock first, so this also orders revocation with changes.
	rows, err := tx.Query(ctx, `SELECT rp.permission_code FROM iam.access_grant g JOIN iam.role_permission rp ON rp.tenant_id=g.tenant_id AND rp.role_id=g.role_id WHERE g.tenant_id=$1 AND g.tenant_membership_id=$2 AND g.scope_type='TENANT' AND g.valid_period @> clock_timestamp() AND rp.permission_code IN ('platform.tenant_settings.read','platform.tenant_settings.manage') ORDER BY g.id,rp.permission_code FOR SHARE OF g,rp`, rc.TenantID, rc.MembershipID)
	if err != nil {
		return false, fmt.Errorf("identity: lock settings grants: %w", err)
	}
	read, write := false, false
	for rows.Next() {
		var code string
		if err = rows.Scan(&code); err != nil {
			rows.Close()
			return false, err
		}
		if code == "platform.tenant_settings.read" {
			read = true
		}
		if code == "platform.tenant_settings.manage" {
			write = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	// Database wall clock is read after every lock wait, including the grant rows.
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return false, err
	}
	if revoked != nil || !now.Before(expires) || now.Sub(lastSeen) > a.IdleTimeout {
		return false, identity.ErrUnauthenticated
	}
	if manage && (stepUp == nil || !now.Before(*stepUp)) {
		return false, identity.ErrStepUpRequired
	}
	var membershipCurrent bool
	err = tx.QueryRow(ctx, `SELECT valid_period @> $3::timestamptz::date FROM iam.tenant_membership WHERE tenant_id=$1 AND id=$2`, rc.TenantID, rc.MembershipID, now).Scan(&membershipCurrent)
	if err != nil {
		return false, err
	}
	if !membershipCurrent {
		return false, identity.ErrPermissionDenied
	}
	// The grant query above retained locks; this second read evaluates validity at the
	// post-wait wall clock, including grants whose validity elapsed while we waited.
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM iam.access_grant g JOIN iam.role_permission rp ON rp.tenant_id=g.tenant_id AND rp.role_id=g.role_id WHERE g.tenant_id=$1 AND g.tenant_membership_id=$2 AND g.scope_type='TENANT' AND g.valid_period @> $3::timestamptz AND rp.permission_code='platform.tenant_settings.read'), EXISTS(SELECT 1 FROM iam.access_grant g JOIN iam.role_permission rp ON rp.tenant_id=g.tenant_id AND rp.role_id=g.role_id WHERE g.tenant_id=$1 AND g.tenant_membership_id=$2 AND g.scope_type='TENANT' AND g.valid_period @> $3::timestamptz AND rp.permission_code='platform.tenant_settings.manage')`, rc.TenantID, rc.MembershipID, now).Scan(&read, &write)
	if err != nil {
		return false, err
	}
	if !read || (manage && !write) {
		return false, identity.ErrPermissionDenied
	}
	return write, nil
}
