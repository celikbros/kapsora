package tenantsettingspg

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/celikbros/kapsora/internal/accommodation/settings"
	"github.com/celikbros/kapsora/internal/identity"
	identitydomain "github.com/celikbros/kapsora/internal/identity/domain"
	identitypg "github.com/celikbros/kapsora/internal/identity/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/tenantsettings/application"
	"github.com/google/uuid"
)

func fixture(t *testing.T) (*dbtest.Harness, *Repository, identity.RequestContext) {
	t.Helper()
	h := dbtest.New(t)
	tenant := h.CreateTenant("settings_a")
	actor := h.CreateActor("settings-actor", "Settings Actor")
	member := h.CreateMembership(tenant, actor)
	var role uuid.UUID
	ctx := context.Background()
	if err := h.Admin.QueryRow(ctx, `INSERT INTO iam.role(tenant_id,code,name,is_system_role) VALUES($1,'TEST_SETTINGS','Test Settings',false) RETURNING id`, tenant).Scan(&role); err != nil {
		t.Fatal(err)
	}
	h.AdminExec(`INSERT INTO iam.role_permission(tenant_id,role_id,permission_code) VALUES($1,$2,'platform.tenant_settings.read'),($1,$2,'platform.tenant_settings.manage')`, tenant, role)
	h.AdminExec(`INSERT INTO iam.access_grant(tenant_id,tenant_membership_id,role_id,scope_type) VALUES($1,$2,$3,'TENANT')`, tenant, member, role)
	const sid = "settings-session-fixture"
	h.AdminExec(`INSERT INTO iam.session(id_hash,actor_id,active_tenant_id,expires_at,step_up_until) VALUES($1,$2,$3,clock_timestamp()+interval '1 hour',clock_timestamp()+interval '10 minutes')`, identitydomain.TokenHash(sid), actor, tenant)
	rc := identity.RequestContext{Principal: identity.Principal{ActorID: actor, ActorType: identity.ActorHuman}, TenantID: tenant, MembershipID: member, SessionID: sid, ClientType: identity.ClientBrowser, App: identity.AppBackoffice, Permissions: map[string]struct{}{"platform.tenant_settings.read": {}, "platform.tenant_settings.manage": {}}, StepUpValid: true}
	return h, NewRepository(h.App, identitypg.NewTenantSettingsAuthority(30*time.Minute)), rc
}
func command(hold, quote int, match, key string) application.Command {
	body := []byte(`{"holdMinutes":` + itoa(hold) + `,"quoteTtlMinutes":` + itoa(quote) + `,"reasonCode":"OPERATING_REQUIREMENT"}`)
	return application.Command{HoldMinutes: hold, QuoteTTLMinutes: quote, ReasonCode: "OPERATING_REQUIREMENT", IfMatch: match, Key: key, RawBody: body}
}
func itoa(n int) string { return strconv.Itoa(n) }

func TestSettingsAtomicVersionReceiptAndIsolation(t *testing.T) {
	h, r, rc := fixture(t)
	ctx := context.Background()
	initial, err := r.Read(ctx, rc)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err = h.Admin.QueryRow(ctx, `SELECT count(*) FROM platform.tenant_setting WHERE tenant_id=$1`, rc.TenantID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("read wrote rows: %d %v", n, err)
	}
	c := command(20, 45, initial.ETag, "settings-save-000001")
	saved, err := r.Save(ctx, rc, c)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ETag == initial.ETag {
		t.Fatal("aggregate version unchanged")
	}
	current, err := r.Read(ctx, rc)
	if err != nil {
		t.Fatal(err)
	}
	var view application.Accommodation
	if err = json.Unmarshal(current.Body, &view); err != nil {
		t.Fatal(err)
	}
	if view.HoldMinutes.EffectiveValue != 20 || view.QuoteTTLMinutes.EffectiveValue != 45 || view.HoldMinutes.Source != "CONFIGURED" {
		t.Fatal(view)
	}
	if settings.FromMap(map[string]string{settings.KeyHoldMinutes: "20", settings.KeyQuoteTTLMinutes: "45"}).HoldMinutes != 20 {
		t.Fatal("consumer drift")
	}
	if _, err = r.Save(ctx, rc, command(21, 46, initial.ETag, "settings-stale-0001")); !errors.Is(err, application.ErrStale) {
		t.Fatalf("stale=%v", err)
	}
	// An unrelated setting never changes the admitted aggregate version.
	h.AdminExec(`INSERT INTO platform.tenant_setting(tenant_id,setting_key,value_json) VALUES($1,'accommodation.max_nights','30'::jsonb)`, rc.TenantID)
	current, err = r.Read(ctx, rc)
	if err != nil || current.ETag != saved.ETag {
		t.Fatalf("unrelated ETag %q %v", current.ETag, err)
	}
	later, err := r.Save(ctx, rc, command(25, 35, saved.ETag, "settings-later-0001"))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := r.Save(ctx, rc, c)
	if err != nil || !replay.Replayed || replay.ETag != saved.ETag || string(replay.Body) != string(saved.Body) {
		t.Fatalf("receipt replay %#v %v", replay, err)
	}
	if _, err = r.Save(ctx, rc, command(26, 35, saved.ETag, c.Key)); !errors.Is(err, application.ErrKeyReused) {
		t.Fatalf("key reuse=%v", err)
	}
	if later.ETag == saved.ETag {
		t.Fatal("second version unchanged")
	}
	noop, err := r.Save(ctx, rc, command(25, 35, later.ETag, "settings-noop-000001"))
	if err != nil || noop.ETag != later.ETag {
		t.Fatalf("no-op changed version %q %v", noop.ETag, err)
	}
	var count int
	if err = h.Admin.QueryRow(ctx, `SELECT count(*) FROM platform.tenant_setting_command_receipt WHERE tenant_id=$1`, rc.TenantID).Scan(&count); err != nil || count != 3 {
		t.Fatalf("receipts=%d %v", count, err)
	}
	if err = h.Admin.QueryRow(ctx, `SELECT count(*) FROM audit.event WHERE tenant_id=$1 AND action_code='tenant_settings.accommodation.update'`, rc.TenantID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("audits after no-op=%d %v", count, err)
	}
	// A suspended membership may not retrieve the earlier durable receipt.
	h.AdminExec(`UPDATE iam.tenant_membership SET membership_status='SUSPENDED' WHERE tenant_id=$1 AND id=$2`, rc.TenantID, rc.MembershipID)
	if _, err = r.Preflight(ctx, rc, c); !errors.Is(err, identity.ErrPermissionDenied) {
		t.Fatalf("suspended receipt=%v", err)
	}
	other := rc
	other.Principal.ActorID = h.CreateActor("settings-other", "Other Actor")
	if _, err = r.Preflight(ctx, other, c); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("cross-actor receipt=%v", err)
	}
	foreign := rc
	foreign.TenantID = h.CreateTenant("settings_b")
	if _, err = r.Preflight(ctx, foreign, c); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("cross-tenant receipt=%v", err)
	}
}

func TestSettingsSensitiveRowAndAuditRollback(t *testing.T) {
	h, r, rc := fixture(t)
	ctx := context.Background()
	h.AdminExec(`INSERT INTO platform.tenant_setting(tenant_id,setting_key,value_json,is_sensitive) VALUES($1,$2,'999'::jsonb,true)`, rc.TenantID, settings.KeyHoldMinutes)
	if _, err := r.Read(ctx, rc); !errors.Is(err, application.ErrSensitive) {
		t.Fatalf("sensitive read=%v", err)
	}
	h.AdminExec(`DELETE FROM platform.tenant_setting WHERE tenant_id=$1 AND setting_key=$2`, rc.TenantID, settings.KeyHoldMinutes)
	initial, err := r.Read(ctx, rc)
	if err != nil {
		t.Fatal(err)
	}
	h.AdminExec(`ALTER TABLE audit.event ADD CONSTRAINT ck_settings_test_audit_failure CHECK (action_code <> 'tenant_settings.accommodation.update')`)
	if _, err = r.Save(ctx, rc, command(30, 40, initial.ETag, "settings-rollback-0001")); err == nil {
		t.Fatal("audit failure allowed save")
	}
	var count int
	if err = h.Admin.QueryRow(ctx, `SELECT count(*) FROM platform.tenant_setting WHERE tenant_id=$1 AND setting_key IN ($2,$3)`, rc.TenantID, settings.KeyHoldMinutes, settings.KeyQuoteTTLMinutes).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rolled back settings=%d %v", count, err)
	}
}

func TestSettingsStepUpExpiresDuringSettingLockWait(t *testing.T) {
	h, r, rc := fixture(t)
	ctx := context.Background()
	h.AdminExec(`INSERT INTO platform.tenant_setting(tenant_id,setting_key,value_json) VALUES($1,$2,'15'::jsonb)`, rc.TenantID, settings.KeyHoldMinutes)
	initial, err := r.Read(ctx, rc)
	if err != nil {
		t.Fatal(err)
	}
	h.AdminExec(`UPDATE iam.session SET step_up_until=clock_timestamp()+interval '2 seconds' WHERE id_hash=$1`, identitydomain.TokenHash(rc.SessionID))
	blocker, err := h.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	var locked string
	if err = blocker.QueryRow(ctx, `SELECT setting_key FROM platform.tenant_setting WHERE tenant_id=$1 AND setting_key=$2 FOR UPDATE`, rc.TenantID, settings.KeyHoldMinutes).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := r.Save(ctx, rc, command(20, 45, initial.ETag, "settings-wait-stepup-01"))
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	select {
	case err = <-done:
		t.Fatalf("writer finished before row unlock: %v", err)
	default:
	}
	time.Sleep(2100 * time.Millisecond)
	if err = blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if !errors.Is(err, identity.ErrStepUpRequired) {
			t.Fatalf("expired step-up write=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("setting writer did not finish")
	}
	var n int
	if err = h.Admin.QueryRow(ctx, `SELECT count(*) FROM platform.tenant_setting WHERE tenant_id=$1 AND setting_key=$2 AND value_json='20'::jsonb`, rc.TenantID, settings.KeyHoldMinutes).Scan(&n); err != nil || n != 0 {
		t.Fatalf("expired step-up changed setting=%d %v", n, err)
	}
	// The grant can expire while an unrelated offline writer holds a setting row.
	h.AdminExec(`UPDATE iam.session SET step_up_until=clock_timestamp()+interval '10 minutes' WHERE id_hash=$1`, identitydomain.TokenHash(rc.SessionID))
	h.AdminExec(`UPDATE iam.access_grant SET valid_period=tstzrange(clock_timestamp()-interval '1 minute',clock_timestamp()+interval '2 seconds','[)') WHERE tenant_id=$1 AND tenant_membership_id=$2`, rc.TenantID, rc.MembershipID)
	blocker, err = h.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = blocker.QueryRow(ctx, `SELECT setting_key FROM platform.tenant_setting WHERE tenant_id=$1 AND setting_key=$2 FOR UPDATE`, rc.TenantID, settings.KeyHoldMinutes).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	done = make(chan error, 1)
	go func() {
		_, err := r.Save(ctx, rc, command(20, 45, initial.ETag, "settings-wait-grant-001"))
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	select {
	case err = <-done:
		t.Fatalf("writer finished before grant-expiry wait: %v", err)
	default:
	}
	time.Sleep(2100 * time.Millisecond)
	if err = blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if !errors.Is(err, identity.ErrPermissionDenied) {
			t.Fatalf("expired grant write=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("grant-expiry writer did not finish")
	}
}
