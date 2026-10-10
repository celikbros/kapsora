package dbtests

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/platform/dbmigrate"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

const privilegedSnapshot = `[{"code":"security.break_glass","sensitivity":"PRIVILEGED"}]`

type roleChangeSeed struct {
	tenant  uuid.UUID
	actors  [3]uuid.UUID // target, maker, checker
	members [3]uuid.UUID
	role    uuid.UUID
}

func seedRoleChange(t *testing.T, h *dbtest.Harness, suffix string) roleChangeSeed {
	t.Helper()
	s := roleChangeSeed{tenant: h.CreateTenant("ROLE_CHANGE_" + suffix)}
	for i := range s.actors {
		s.actors[i] = h.CreateActor(fmt.Sprintf("role-change-%s-%d", suffix, i), "Synthetic")
		s.members[i] = h.CreateMembership(s.tenant, s.actors[i])
	}
	ctx, cancel := h.Ctx()
	defer cancel()
	if err := h.Admin.QueryRow(ctx, `INSERT INTO iam.role(tenant_id,code,name,is_system_role)
	 VALUES($1,'TENANT_ADMIN','Synthetic administrator',true) RETURNING id`, s.tenant).Scan(&s.role); err != nil {
		t.Fatalf("seed role: %v", err)
	}
	h.AdminExec(`INSERT INTO iam.role_permission(tenant_id,role_id,permission_code)
	 VALUES($1,$2,'security.break_glass')`, s.tenant, s.role)
	return s
}

type roleChangeRequestInput struct {
	operation                 string
	targetMember, targetActor uuid.UUID
	makerMember, makerActor   uuid.UUID
	role                      uuid.UUID
	scope, snapshot, reason   string
	hash                      []byte
	version                   int64
	revokeGrant               uuid.UUID
	revokeRange               string
}

func defaultRoleChangeRequest(s roleChangeSeed) roleChangeRequestInput {
	return roleChangeRequestInput{
		operation: "ASSIGN", targetMember: s.members[0], targetActor: s.actors[0],
		makerMember: s.members[1], makerActor: s.actors[1], role: s.role,
		scope: "TENANT", snapshot: privilegedSnapshot, reason: "ONBOARDING",
		hash: make([]byte, 32), version: 1,
	}
}

func insertRoleChange(h *dbtest.Harness, s roleChangeSeed, in roleChangeRequestInput) (uuid.UUID, error) {
	ctx, cancel := h.Ctx()
	defer cancel()
	tx, err := h.Admin.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.actor_id',$1,true)`, in.makerActor.String()); err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	var grant any
	var validPeriod any
	if in.revokeGrant != uuid.Nil {
		grant = in.revokeGrant
	}
	if in.revokeRange != "" {
		validPeriod = in.revokeRange
	}
	err = tx.QueryRow(ctx, `INSERT INTO iam.role_change_request(
	 tenant_id,operation,target_membership_id,target_actor_id,maker_membership_id,maker_actor_id,
	 role_id,role_code,scope_type,permission_snapshot,configuration_hash,
	 target_membership_version,revoke_grant_id,revoke_valid_period,reason_code)
	 VALUES($1,$2,$3,$4,$5,$6,$7,'TENANT_ADMIN',$8,$9::jsonb,$10,$11,$12,$13::tstzrange,$14)
	 RETURNING id`, s.tenant, in.operation, in.targetMember, in.targetActor,
		in.makerMember, in.makerActor, in.role, in.scope, in.snapshot, in.hash,
		in.version, grant, validPeriod, in.reason).Scan(&id)
	if err != nil {
		return uuid.Nil, err
	}
	return id, tx.Commit(ctx)
}

func mustRoleChange(t *testing.T, h *dbtest.Harness, s roleChangeSeed, in roleChangeRequestInput) uuid.UUID {
	t.Helper()
	id, err := insertRoleChange(h, s, in)
	if err != nil {
		t.Fatalf("insert role change request: %v", err)
	}
	return id
}

func roleChangeDecision(h *dbtest.Harness, tenant, actor uuid.UUID, sql string, args ...any) error {
	return h.AppTx(tenant, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.actor_id',$1,true)`, actor.String()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, sql, args...)
		return err
	})
}

func roleChangeAdminExecAs(h *dbtest.Harness, actor uuid.UUID, sql string, args ...any) error {
	ctx, cancel := h.Ctx()
	defer cancel()
	tx, err := h.Admin.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.actor_id',$1,true)`, actor.String()); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func TestPrivilegedRoleMigration57To58PreservesIAMState(t *testing.T) {
	h := dbtest.NewAtVersion(t, 57)
	s := seedRoleChange(t, h, "UPGRADE")
	ctx, cancel := h.Ctx()
	defer cancel()
	var grantID, invitationID uuid.UUID
	if err := h.Admin.QueryRow(ctx, `INSERT INTO iam.access_grant(
	 tenant_id,tenant_membership_id,role_id,scope_type,valid_period,granted_by,grant_reason)
	 VALUES($1,$2,$3,'TENANT',tstzrange('2025-01-01','2027-01-01','[)'),$4,'legacy fixture')
	 RETURNING id`, s.tenant, s.members[0], s.role, s.actors[1]).Scan(&grantID); err != nil {
		t.Fatalf("seed legacy grant: %v", err)
	}
	if err := h.Admin.QueryRow(ctx, `INSERT INTO iam.tenant_invitation(
	 tenant_id,masked_recipient,proof_digest,expires_at,status,accepted_actor_id,
	 accepted_membership_id,accept_key,terminal_at,accepted_mode)
	 VALUES($1,'s***@***',sha256('synthetic-proof'::bytea),clock_timestamp()+interval '1 day',
	 'ACCEPTED',$2,$3,'upgrade-key-00001',clock_timestamp(),'EXISTING') RETURNING id`,
		s.tenant, s.actors[0], s.members[0]).Scan(&invitationID); err != nil {
		t.Fatalf("seed accepted invitation: %v", err)
	}
	const stateSQL = `SELECT jsonb_build_object(
	 'actors',(SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM iam.actor a WHERE a.id=ANY($2::uuid[])),
	 'members',(SELECT jsonb_agg(to_jsonb(m) ORDER BY m.id) FROM iam.tenant_membership m WHERE m.tenant_id=$1),
	 'roles',(SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM iam.role r WHERE r.tenant_id=$1),
	 'permissions',(SELECT jsonb_agg(to_jsonb(rp) ORDER BY rp.permission_code) FROM iam.role_permission rp WHERE rp.tenant_id=$1),
	 'grant',(SELECT to_jsonb(g) FROM iam.access_grant g WHERE g.id=$3),
	 'invitation',(SELECT to_jsonb(i) FROM iam.tenant_invitation i WHERE i.id=$4))::text`
	ids := []uuid.UUID{s.actors[0], s.actors[1], s.actors[2]}
	var before, after string
	if err := h.Admin.QueryRow(ctx, stateSQL, s.tenant, ids, grantID, invitationID).Scan(&before); err != nil {
		t.Fatalf("capture version 57 IAM state: %v", err)
	}
	status, err := dbmigrate.UpTo(h.AdminURL, 58)
	if err != nil || status.Version != 58 || status.Dirty {
		t.Fatalf("upgrade 57 to 58: %+v %v", status, err)
	}
	if err := h.Admin.QueryRow(ctx, stateSQL, s.tenant, ids, grantID, invitationID).Scan(&after); err != nil {
		t.Fatalf("capture version 58 IAM state: %v", err)
	}
	if before != after {
		t.Fatal("migration 58 changed existing actor, membership, role, permission, grant or invitation rows")
	}
	// A previous membership of the same actor remains legal and can be identified
	// independently by the new triple evidence key.
	var historical uuid.UUID
	if err := h.Admin.QueryRow(ctx, `INSERT INTO iam.tenant_membership(tenant_id,actor_id,valid_period)
	 VALUES($1,$2,daterange('2020-01-01','2021-01-01','[)')) RETURNING id`,
		s.tenant, s.actors[0]).Scan(&historical); err != nil {
		t.Fatalf("historical nonoverlapping membership: %v", err)
	}
	if historical == s.members[0] {
		t.Fatal("historical membership reused current membership id")
	}
}

func TestPrivilegedRoleRequestBindingAndSnapshotGuards(t *testing.T) {
	h := dbtest.New(t)
	s := seedRoleChange(t, h, "GUARDS")
	other := seedRoleChange(t, h, "GUARDS_OTHER")
	base := defaultRoleChangeRequest(s)
	for _, tc := range []struct {
		name   string
		change func(*roleChangeRequestInput)
		state  string
	}{
		{"maker is target", func(v *roleChangeRequestInput) { v.makerMember, v.makerActor = v.targetMember, v.targetActor }, dbtest.SQLStateCheckViolation},
		{"target actor mismatches membership", func(v *roleChangeRequestInput) { v.targetActor = s.actors[2] }, dbtest.SQLStateForeignKeyViolation},
		{"maker actor mismatches membership", func(v *roleChangeRequestInput) { v.makerActor = s.actors[2] }, dbtest.SQLStateForeignKeyViolation},
		{"cross tenant membership", func(v *roleChangeRequestInput) { v.targetMember, v.targetActor = other.members[0], other.actors[0] }, dbtest.SQLStateForeignKeyViolation},
		{"cross tenant role", func(v *roleChangeRequestInput) { v.role = other.role }, dbtest.SQLStateForeignKeyViolation},
		{"non tenant scope", func(v *roleChangeRequestInput) { v.scope = "ORGANIZATION" }, dbtest.SQLStateCheckViolation},
		{"empty snapshot", func(v *roleChangeRequestInput) { v.snapshot = `[]` }, dbtest.SQLStateCheckViolation},
		{"normal only snapshot", func(v *roleChangeRequestInput) { v.snapshot = `[{"code":"identity.user.read","sensitivity":"NORMAL"}]` }, dbtest.SQLStateCheckViolation},
		{"duplicate codes", func(v *roleChangeRequestInput) {
			v.snapshot = `[{"code":"security.break_glass","sensitivity":"PRIVILEGED"},{"code":"security.break_glass","sensitivity":"PRIVILEGED"}]`
		}, dbtest.SQLStateCheckViolation},
		{"unsorted codes", func(v *roleChangeRequestInput) {
			v.snapshot = `[{"code":"security.break_glass","sensitivity":"PRIVILEGED"},{"code":"identity.user.read","sensitivity":"NORMAL"}]`
		}, dbtest.SQLStateCheckViolation},
		{"extra snapshot field", func(v *roleChangeRequestInput) {
			v.snapshot = `[{"code":"security.break_glass","sensitivity":"PRIVILEGED","name":"leak"}]`
		}, dbtest.SQLStateCheckViolation},
		{"invalid sensitivity", func(v *roleChangeRequestInput) {
			v.snapshot = `[{"code":"security.break_glass","sensitivity":"SECRET"}]`
		}, dbtest.SQLStateCheckViolation},
		{"bad hash length", func(v *roleChangeRequestInput) { v.hash = []byte{1} }, dbtest.SQLStateCheckViolation},
		{"zero target version", func(v *roleChangeRequestInput) { v.version = 0 }, dbtest.SQLStateCheckViolation},
		{"assign carries revoke range", func(v *roleChangeRequestInput) { v.revokeRange = `[2020-01-01,2021-01-01)` }, dbtest.SQLStateCheckViolation},
		{"revoke omits grant", func(v *roleChangeRequestInput) { v.operation, v.reason = "REVOKE", "ACCESS_REVIEW" }, dbtest.SQLStateCheckViolation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.change(&in)
			_, err := insertRoleChange(h, s, in)
			dbtest.ExpectSQLState(t, err, tc.state, tc.name)
		})
	}
	if id := mustRoleChange(t, h, s, base); id == uuid.Nil {
		t.Fatal("valid canonical privileged proposal has no id")
	}
	_, err := insertRoleChange(h, s, base)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "second pending request for same target")
}

func TestPrivilegedRoleInsertActorEvidenceMatchesAppContext(t *testing.T) {
	h := dbtest.New(t)
	s := seedRoleChange(t, h, "ACTOR_CONTEXT")
	requestID := mustRoleChange(t, h, s, defaultRoleChangeRequest(s))
	const request = `INSERT INTO iam.role_change_request(
	 tenant_id,operation,target_membership_id,target_actor_id,maker_membership_id,maker_actor_id,
	 role_id,role_code,scope_type,permission_snapshot,configuration_hash,target_membership_version,reason_code)
	 VALUES($1,'ASSIGN',$2,$3,$4,$5,$6,'TENANT_ADMIN','TENANT',$7::jsonb,$8,1,'ONBOARDING')`
	requestArgs := []any{s.tenant, s.members[2], s.actors[2], s.members[1], s.actors[1],
		s.role, privilegedSnapshot, make([]byte, 32)}
	err := roleChangeDecision(h, s.tenant, s.actors[0], request, requestArgs...)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "another actor cannot insert a maker's proposal")
	if err := roleChangeDecision(h, s.tenant, s.actors[1], request, requestArgs...); err != nil {
		t.Fatalf("real maker can insert own proposal: %v", err)
	}
	const receipt = `INSERT INTO iam.role_change_command_receipt(
	 tenant_id,actor_id,command_code,key_hash,request_hash,request_id,
	 response_status,response_etag,response_body)
	 VALUES($1,$2,'CREATE',sha256('actor-context-key'::bytea),
	 sha256('actor-context-body'::bytea),$3,201,'"1"',$4)`
	receiptArgs := []any{s.tenant, s.actors[1], requestID, []byte(`{"id":"synthetic"}`)}
	err = roleChangeDecision(h, s.tenant, s.actors[2], receipt, receiptArgs...)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "another actor cannot insert a maker's receipt")
	if err := roleChangeDecision(h, s.tenant, s.actors[1], receipt, receiptArgs...); err != nil {
		t.Fatalf("real actor can insert own receipt: %v", err)
	}
}

func TestPrivilegedRoleRequestTransitionsAreGuarded(t *testing.T) {
	h := dbtest.New(t)
	s := seedRoleChange(t, h, "TRANSITIONS")
	id := mustRoleChange(t, h, s, defaultRoleChangeRequest(s))
	err := h.AdminExecErr(`UPDATE iam.role_change_request SET reason_code='DUTY_ASSIGNMENT' WHERE id=$1`, id)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "proposal edit")
	err = h.AdminExecErr(`DELETE FROM iam.role_change_request WHERE id=$1`, id)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "proposal deletion")
	err = roleChangeDecision(h, s.tenant, s.actors[1], `UPDATE iam.role_change_request
	 SET status='REJECTED',decided_by_membership_id=$2,decided_by_actor_id=$3,
	 decided_at=clock_timestamp(),decision_reason_code='STALE_REQUEST' WHERE id=$1`,
		id, s.members[1], s.actors[1])
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "maker cannot reject own proposal")
	err = roleChangeDecision(h, s.tenant, s.actors[0], `UPDATE iam.role_change_request
	 SET status='REJECTED',decided_by_membership_id=$2,decided_by_actor_id=$3,
	 decided_at=clock_timestamp(),decision_reason_code='STALE_REQUEST' WHERE id=$1`,
		id, s.members[0], s.actors[0])
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "target cannot reject own proposal")
	err = roleChangeDecision(h, s.tenant, s.actors[1], `UPDATE iam.role_change_request
	 SET status='REJECTED',decided_by_membership_id=$2,decided_by_actor_id=$3,
	 decided_at=clock_timestamp(),decision_reason_code='STALE_REQUEST' WHERE id=$1`,
		id, s.members[2], s.actors[2])
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "decision actor context must match evidence")
	err = roleChangeDecision(h, s.tenant, s.actors[2], `UPDATE iam.role_change_request
	 SET status='REJECTED',decided_by_membership_id=$2,decided_by_actor_id=$3,
	 decided_at=clock_timestamp(),decision_reason_code='STALE_REQUEST' WHERE id=$1`,
		id, s.members[1], s.actors[2])
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateForeignKeyViolation, "decider membership and actor must bind")
	err = roleChangeDecision(h, s.tenant, s.actors[2], `UPDATE iam.role_change_request
	 SET status='REJECTED',decided_by_membership_id=$2,decided_by_actor_id=$3,
	 decided_at=clock_timestamp() WHERE id=$1`, id, s.members[2], s.actors[2])
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "rejection without reason")
	err = roleChangeDecision(h, s.tenant, s.actors[2], `UPDATE iam.role_change_request
	 SET status='REJECTED',decided_by_membership_id=$2,decided_by_actor_id=$3,
	 decided_at=clock_timestamp(),decision_reason_code='STALE_REQUEST' WHERE id=$1`,
		id, s.members[2], s.actors[2])
	if err != nil {
		t.Fatalf("valid checker rejection: %v", err)
	}
	var version int64
	ctx, cancel := h.Ctx()
	defer cancel()
	if err := h.Admin.QueryRow(ctx, `SELECT row_version FROM iam.role_change_request WHERE id=$1`, id).Scan(&version); err != nil || version != 2 {
		t.Fatalf("terminal request version = %d, err %v; want 2", version, err)
	}
	err = roleChangeDecision(h, s.tenant, s.actors[2], `UPDATE iam.role_change_request SET status='REJECTED' WHERE id=$1`, id)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "no-op terminal update")
	err = h.AdminExecErr(`DELETE FROM iam.role_change_request WHERE id=$1`, id)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "terminal deletion")
	// A completed request no longer blocks a new proposal for the target.
	if _, err := insertRoleChange(h, s, defaultRoleChangeRequest(s)); err != nil {
		t.Fatalf("new request after rejection: %v", err)
	}
}

func TestPrivilegedRoleRevokeEvidenceAndExclusiveLowerBound(t *testing.T) {
	h := dbtest.New(t)
	s := seedRoleChange(t, h, "REVOKE")
	ctx, cancel := h.Ctx()
	defer cancel()
	var targetGrant, otherGrant uuid.UUID
	for i, member := range []uuid.UUID{s.members[0], s.members[2]} {
		var dest *uuid.UUID
		if i == 0 {
			dest = &targetGrant
		} else {
			dest = &otherGrant
		}
		if err := h.Admin.QueryRow(ctx, `INSERT INTO iam.access_grant(
		 tenant_id,tenant_membership_id,role_id,scope_type,valid_period)
		 VALUES($1,$2,$3,'TENANT',tstzrange('2025-01-01','2027-01-01','(]')) RETURNING id`,
			s.tenant, member, s.role).Scan(dest); err != nil {
			t.Fatalf("seed historical grant %d: %v", i, err)
		}
	}
	var original string
	if err := h.Admin.QueryRow(ctx, `SELECT valid_period::text FROM iam.access_grant WHERE id=$1`, targetGrant).Scan(&original); err != nil {
		t.Fatalf("read original range: %v", err)
	}
	in := defaultRoleChangeRequest(s)
	in.operation, in.reason, in.revokeRange = "REVOKE", "ACCESS_REVIEW", original
	in.revokeGrant = otherGrant
	_, err := insertRoleChange(h, s, in)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "revoke grant belongs to different member")
	in.revokeGrant = targetGrant
	in.revokeRange = `[2025-01-01,2027-01-01]`
	_, err = insertRoleChange(h, s, in)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "revoke evidence changes exclusive lower bound")
	in.revokeRange = original
	id := mustRoleChange(t, h, s, in)
	if err := h.AdminExecErr(`UPDATE iam.access_grant
	 SET valid_period=tstzrange(lower(valid_period),'2026-11-01','()') WHERE id=$1`, targetGrant); err != nil {
		t.Fatalf("close original grant preserving exclusive lower: %v", err)
	}
	var closed string
	if err := h.Admin.QueryRow(ctx, `SELECT valid_period::text FROM iam.access_grant WHERE id=$1`, targetGrant).Scan(&closed); err != nil {
		t.Fatalf("read closed range: %v", err)
	}
	const approve = `UPDATE iam.role_change_request SET status='APPROVED',
	 decided_by_membership_id=$2,decided_by_actor_id=$3,decided_at=clock_timestamp(),
	 applied_grant_id=$4,applied_membership_version=2,applied_valid_period=$5::tstzrange WHERE id=$1`
	err = roleChangeDecision(h, s.tenant, s.actors[2], approve,
		id, s.members[2], s.actors[2], targetGrant, `[2025-01-01,2026-11-01)`)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "approved revoke cannot turn exclusive lower inclusive")
	err = roleChangeDecision(h, s.tenant, s.actors[2], approve,
		id, s.members[2], s.actors[2], targetGrant, closed)
	if err != nil {
		t.Fatalf("approved revoke preserves legacy lower bound: %v", err)
	}
}

func TestPrivilegedRoleApprovedGrantMustMatchProposal(t *testing.T) {
	h := dbtest.New(t)
	s := seedRoleChange(t, h, "APPLIED")
	id := mustRoleChange(t, h, s, defaultRoleChangeRequest(s))
	ctx, cancel := h.Ctx()
	defer cancel()
	var wrongGrant, targetGrant uuid.UUID
	for i, member := range []uuid.UUID{s.members[2], s.members[0]} {
		var dest *uuid.UUID
		if i == 0 {
			dest = &wrongGrant
		} else {
			dest = &targetGrant
		}
		if err := h.Admin.QueryRow(ctx, `INSERT INTO iam.access_grant(
		 tenant_id,tenant_membership_id,role_id,scope_type,valid_period)
		 VALUES($1,$2,$3,'TENANT',tstzrange('2026-01-01',NULL,'[)')) RETURNING id`,
			s.tenant, member, s.role).Scan(dest); err != nil {
			t.Fatalf("seed applied grant %d: %v", i, err)
		}
	}
	const approve = `UPDATE iam.role_change_request SET status='APPROVED',
	 decided_by_membership_id=$2,decided_by_actor_id=$3,decided_at=clock_timestamp(),
	 applied_grant_id=$4,applied_membership_version=$5,applied_valid_period=$6::tstzrange WHERE id=$1`
	args := []any{id, s.members[2], s.actors[2], wrongGrant, int64(2), `[2026-01-01,)`}
	err := roleChangeDecision(h, s.tenant, s.actors[2], approve, args...)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "another member's grant")
	args[3] = targetGrant
	args[5] = `[2025-01-01,)`
	err = roleChangeDecision(h, s.tenant, s.actors[2], approve, args...)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "applied range differs from grant")
	args[5] = `[2026-01-01,)`
	args[4] = int64(3)
	err = roleChangeDecision(h, s.tenant, s.actors[2], approve, args...)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "applied version skips one")
	args[4] = int64(2)
	err = roleChangeDecision(h, s.tenant, s.actors[2], approve, args...)
	if err != nil {
		t.Fatalf("matching approved grant: %v", err)
	}
	second := mustRoleChange(t, h, s, defaultRoleChangeRequest(s))
	args[0] = second
	err = roleChangeDecision(h, s.tenant, s.actors[2], approve, args...)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "grant cannot be approved as a second assignment")
	err = roleChangeDecision(h, s.tenant, s.actors[1], `UPDATE iam.role_change_request
	 SET status='CANCELLED',decided_by_membership_id=$2,decided_by_actor_id=$3,
	 decided_at=clock_timestamp(),decision_reason_code='WITHDRAWN' WHERE id=$1`,
		second, s.members[1], s.actors[1])
	if err != nil {
		t.Fatalf("maker cancels second request: %v", err)
	}
	var original string
	if err := h.Admin.QueryRow(ctx, `SELECT valid_period::text FROM iam.access_grant WHERE id=$1`, targetGrant).Scan(&original); err != nil {
		t.Fatalf("read grant before revoke: %v", err)
	}
	revoke := defaultRoleChangeRequest(s)
	revoke.operation, revoke.reason = "REVOKE", "ACCESS_REVIEW"
	revoke.revokeGrant, revoke.revokeRange = targetGrant, original
	revokeID := mustRoleChange(t, h, s, revoke)
	if err := h.AdminExecErr(`UPDATE iam.access_grant
	 SET valid_period=tstzrange(lower(valid_period),'2026-11-01','[)') WHERE id=$1`, targetGrant); err != nil {
		t.Fatalf("close grant for approved revoke: %v", err)
	}
	var closed string
	if err := h.Admin.QueryRow(ctx, `SELECT valid_period::text FROM iam.access_grant WHERE id=$1`, targetGrant).Scan(&closed); err != nil {
		t.Fatalf("read closed grant: %v", err)
	}
	args[0], args[5] = revokeID, closed
	err = roleChangeDecision(h, s.tenant, s.actors[2], approve, args...)
	if err != nil {
		t.Fatalf("same grant may be approved once for assignment and once for revocation: %v", err)
	}
}

func TestPrivilegedRoleReceiptBoundsUniquenessAndAppendOnly(t *testing.T) {
	h := dbtest.New(t)
	s := seedRoleChange(t, h, "RECEIPT")
	id := mustRoleChange(t, h, s, defaultRoleChangeRequest(s))
	const receipt = `INSERT INTO iam.role_change_command_receipt(
	 tenant_id,actor_id,command_code,key_hash,request_hash,request_id,
	 response_status,response_etag,response_body)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`
	key, fingerprint := make([]byte, 32), make([]byte, 32)
	key[0], fingerprint[0] = 1, 2
	args := []any{s.tenant, s.actors[1], "CREATE", key, fingerprint, id, 201, `"1"`, []byte(`{"request":{"id":"synthetic"}}`)}
	if err := roleChangeAdminExecAs(h, s.actors[1], receipt, args...); err != nil {
		t.Fatalf("valid command receipt: %v", err)
	}
	err := roleChangeAdminExecAs(h, s.actors[1], receipt, args...)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "duplicate actor command key")
	for _, tc := range []struct {
		name  string
		index int
		value any
	}{
		{"short key digest", 3, []byte{1}},
		{"short request digest", 4, []byte{2}},
		{"empty response", 8, []byte{}},
		{"oversize response", 8, make([]byte, 262145)},
		{"bad create status", 6, 200},
		{"unquoted ETag", 7, `1`},
		{"foreign request", 5, uuid.New()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := append([]any(nil), args...)
			changed[tc.index] = tc.value
			// Avoid the unique key collision hiding the constraint under test.
			newKey := append([]byte(nil), key...)
			newKey[1] = byte(tc.index + len(tc.name))
			if tc.index != 3 {
				changed[3] = newKey
			}
			err := roleChangeAdminExecAs(h, s.actors[1], receipt, changed...)
			want := dbtest.SQLStateCheckViolation
			if tc.name == "foreign request" {
				want = dbtest.SQLStateForeignKeyViolation
			}
			dbtest.ExpectSQLState(t, err, want, tc.name)
		})
	}
	err = h.AdminExecErr(`UPDATE iam.role_change_command_receipt SET response_status=200 WHERE request_id=$1`, id)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "receipt update")
	err = h.AdminExecErr(`DELETE FROM iam.role_change_command_receipt WHERE request_id=$1`, id)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "receipt delete")
}

func TestPrivilegedRoleTablesEnforceTenantRLS(t *testing.T) {
	h := dbtest.New(t)
	a := seedRoleChange(t, h, "RLS_A")
	b := seedRoleChange(t, h, "RLS_B")
	var bRequestID uuid.UUID
	for _, s := range []roleChangeSeed{a, b} {
		id := mustRoleChange(t, h, s, defaultRoleChangeRequest(s))
		if s.tenant == b.tenant {
			bRequestID = id
		}
		if err := roleChangeAdminExecAs(h, s.actors[1], `INSERT INTO iam.role_change_command_receipt(
		 tenant_id,actor_id,command_code,key_hash,request_hash,request_id,
		 response_status,response_etag,response_body)
		 VALUES($1,$2,'CREATE',sha256($3::bytea),sha256($4::bytea),$5,201,'"1"',$6)`,
			s.tenant, s.actors[1], []byte("key"), []byte("fingerprint"), id, []byte(`{"id":"synthetic"}`)); err != nil {
			t.Fatalf("seed command receipt: %v", err)
		}
	}
	for _, table := range []string{"iam.role_change_request", "iam.role_change_command_receipt"} {
		t.Run(table, func(t *testing.T) {
			for _, tenant := range []uuid.UUID{a.tenant, b.tenant} {
				var n int
				err := h.AppTx(tenant, func(ctx context.Context, tx pgx.Tx) error {
					return tx.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n)
				})
				if err != nil || n != 1 {
					t.Fatalf("tenant %s sees %d rows in %s, err %v", tenant, n, table, err)
				}
			}
			var unbound int
			ctx, cancel := h.Ctx()
			defer cancel()
			if err := h.App.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&unbound); err != nil || unbound != 0 {
				t.Fatalf("unbound app sees %d rows in %s, err %v", unbound, table, err)
			}
		})
	}
	err := roleChangeDecision(h, a.tenant, b.actors[1], `INSERT INTO iam.role_change_request(
	 tenant_id,operation,target_membership_id,target_actor_id,maker_membership_id,maker_actor_id,
	 role_id,role_code,scope_type,permission_snapshot,configuration_hash,target_membership_version,reason_code)
	 VALUES($1,'ASSIGN',$2,$3,$4,$5,$6,'TENANT_ADMIN','TENANT',$7::jsonb,$8,1,'ONBOARDING')`,
		b.tenant, b.members[2], b.actors[2], b.members[1], b.actors[1], b.role,
		privilegedSnapshot, make([]byte, 32))
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateInsufficientPrivilege, "cross-tenant request insert")
	err = roleChangeDecision(h, a.tenant, b.actors[1], `INSERT INTO iam.role_change_command_receipt(
		 tenant_id,actor_id,command_code,key_hash,request_hash,request_id,
		 response_status,response_etag,response_body)
		 VALUES($1,$2,'CREATE',sha256('other-key'::bytea),sha256('other-body'::bytea),
		 $3,201,'"1"','x'::bytea)`,
		b.tenant, b.actors[1], bRequestID)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateInsufficientPrivilege, "cross-tenant receipt insert")
}
