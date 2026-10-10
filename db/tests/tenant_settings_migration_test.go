package dbtests

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/platform/dbmigrate"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

func TestTenantSettingsMigration60To61PreservesExistingState(t *testing.T) {
	h := dbtest.NewAtVersion(t, 60)
	s := seedRoleChange(t, h, "SETTINGS_UPGRADE")
	ctx, cancel := h.Ctx()
	defer cancel()
	var customRole uuid.UUID
	if err := h.Admin.QueryRow(ctx, `INSERT INTO iam.role(tenant_id,code,name,is_system_role)
		VALUES($1,'CUSTOM_READER','Custom reader',false) RETURNING id`, s.tenant).Scan(&customRole); err != nil {
		t.Fatalf("custom role: %v", err)
	}
	h.AdminExec(`INSERT INTO iam.role_permission(tenant_id,role_id,permission_code)
		VALUES($1,$2,'security.break_glass')`, s.tenant, customRole)
	h.AdminExec(`INSERT INTO platform.tenant_setting(tenant_id,setting_key,value_json)
		VALUES($1,'accommodation.hold_minutes','27'::jsonb),
		($1,'accommodation.quote_ttl_minutes','43'::jsonb)`, s.tenant)
	requestID := mustRoleChange(t, h, s, defaultRoleChangeRequest(s))
	const snapshot = `SELECT jsonb_build_object(
		'settings',(SELECT jsonb_agg(to_jsonb(x) ORDER BY x.setting_key) FROM
		 (SELECT tenant_id,setting_key,value_json,created_at,updated_at
		 FROM platform.tenant_setting WHERE tenant_id=$1) x),
		'roles',(SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM iam.role r WHERE r.tenant_id=$1),
		'grants',(SELECT jsonb_agg(to_jsonb(g) ORDER BY g.id) FROM iam.access_grant g WHERE g.tenant_id=$1),
		'request',(SELECT to_jsonb(q) FROM iam.role_change_request q WHERE q.id=$2))::text`
	var before, after string
	if err := h.Admin.QueryRow(ctx, snapshot, s.tenant, requestID).Scan(&before); err != nil {
		t.Fatalf("snapshot before upgrade: %v", err)
	}
	status, err := dbmigrate.UpTo(h.AdminURL, 61)
	if err != nil || status.Version != 61 || status.Dirty {
		t.Fatalf("upgrade to 61: %+v %v", status, err)
	}
	if err := h.Admin.QueryRow(ctx, snapshot, s.tenant, requestID).Scan(&after); err != nil {
		t.Fatalf("snapshot after upgrade: %v", err)
	}
	if before != after {
		t.Fatal("migration changed existing setting values, IAM roles/grants, or pending request snapshot")
	}
	var systemNew, customNew, oldGrant int
	if err := h.Admin.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM iam.role_permission WHERE tenant_id=$1 AND role_id=$2
		 AND permission_code IN ('platform.tenant_settings.read','platform.tenant_settings.manage')),
		(SELECT count(*) FROM iam.role_permission WHERE tenant_id=$1 AND role_id=$3
		 AND permission_code IN ('platform.tenant_settings.read','platform.tenant_settings.manage')),
		(SELECT count(*) FROM iam.role_permission WHERE tenant_id=$1 AND role_id=$2
		 AND permission_code='security.break_glass')`, s.tenant, s.role, customRole).
		Scan(&systemNew, &customNew, &oldGrant); err != nil {
		t.Fatalf("read migrated grants: %v", err)
	}
	if systemNew != 2 || customNew != 0 || oldGrant != 1 {
		t.Fatalf("new permissions system/custom/old = %d/%d/%d, want 2/0/1", systemNew, customNew, oldGrant)
	}
	var rowVersion int64
	if err := h.Admin.QueryRow(ctx, `SELECT row_version FROM platform.tenant_setting
		WHERE tenant_id=$1 AND setting_key='accommodation.hold_minutes'`, s.tenant).Scan(&rowVersion); err != nil || rowVersion != 1 {
		t.Fatalf("existing setting version = %d, err %v, want 1", rowVersion, err)
	}
}

func TestTenantSettingReceiptHasForcedRLSAndAppendOnlyEvidence(t *testing.T) {
	h := dbtest.New(t)
	a := h.CreateTenant("SETTING_RECEIPT_A")
	b := h.CreateTenant("SETTING_RECEIPT_B")
	actor := h.CreateActor("setting-receipt-actor", "Synthetic")
	am := h.CreateMembership(a, actor)
	bm := h.CreateMembership(b, actor)
	const insert = `INSERT INTO platform.tenant_setting_command_receipt(
		tenant_id,actor_id,membership_id,operation_code,key_hash,request_hash,
		response_status,response_etag,response_body)
		VALUES($1,$2,$3,'accommodation.update',sha256($4::bytea),
		sha256('request'::bytea),200,'"synthetic"',$5)`
	for _, pair := range []struct{ tenant, member uuid.UUID }{{a, am}, {b, bm}} {
		if err := roleChangeAdminExecAs(h, actor, insert, pair.tenant, actor, pair.member,
			[]byte(pair.tenant.String()), []byte(`{"holdMinutes":20}`)); err != nil {
			t.Fatalf("insert receipt: %v", err)
		}
	}
	var forced bool
	ctx, cancel := h.Ctx()
	defer cancel()
	if err := h.Admin.QueryRow(ctx, `SELECT relrowsecurity AND relforcerowsecurity
		FROM pg_class WHERE oid='platform.tenant_setting_command_receipt'::regclass`).Scan(&forced); err != nil || !forced {
		t.Fatalf("receipt RLS forced = %v, err %v", forced, err)
	}
	for _, tenant := range []uuid.UUID{a, b} {
		var count int
		if err := h.AppTx(tenant, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM platform.tenant_setting_command_receipt`).Scan(&count)
		}); err != nil || count != 1 {
			t.Fatalf("tenant %s sees %d receipts: %v", tenant, count, err)
		}
	}
	var unbound int
	if err := h.App.QueryRow(ctx, `SELECT count(*) FROM platform.tenant_setting_command_receipt`).Scan(&unbound); err != nil || unbound != 0 {
		t.Fatalf("unbound app sees %d receipts: %v", unbound, err)
	}
	err := roleChangeDecision(h, a, actor, insert, b, actor, bm, []byte("cross-tenant"), []byte(`{"holdMinutes":20}`))
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateInsufficientPrivilege, "cross-tenant receipt")
	err = roleChangeDecision(h, a, h.CreateActor("receipt-impostor", "Synthetic"), insert,
		a, actor, am, []byte("impostor"), []byte(`{"holdMinutes":20}`))
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateInsufficientPrivilege, "receipt actor mismatch")
	err = h.AdminExecErr(`UPDATE platform.tenant_setting_command_receipt SET response_status=200 WHERE tenant_id=$1`, a)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "receipt update")
	err = h.AdminExecErr(`DELETE FROM platform.tenant_setting_command_receipt WHERE tenant_id=$1`, a)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "receipt delete")
}
