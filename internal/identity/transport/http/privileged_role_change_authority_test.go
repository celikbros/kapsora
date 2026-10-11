package identityhttp_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
)

func roleChangeAuthorityLogin(t *testing.T, s *authzServer, handle string) (*http.Cookie, string) {
	t.Helper()
	login := s.do(call{method: http.MethodPost, path: "/api/v1/session/login", body: `{"username":"` + handle + `","password":"` + testPassword + `"}`})
	if login.Code != http.StatusOK {
		t.Fatalf("%s login: %d %s", handle, login.Code, login.Body.String())
	}
	cookie := sessionCookie(t, login, s.cookies.Name())
	csrf, _ := decodeBody(t, login)["csrfToken"].(string)
	switchTenant := s.do(call{method: http.MethodPost, path: "/api/v1/session/switch-tenant", cookie: cookie, csrf: csrf,
		body: `{"tenantId":"` + s.tenantA.String() + `"}`})
	if switchTenant.Code != http.StatusOK {
		t.Fatalf("%s tenant switch: %d %s", handle, switchTenant.Code, switchTenant.Body.String())
	}
	stepUpDirectory(t, s, cookie, csrf)
	return cookie, csrf
}

func roleChangeAssertPendingWithoutGrant(t *testing.T, f roleChangeRaceFixture) {
	t.Helper()
	roleChangeAssertPending(t, f)
	var grants int
	if err := f.s.h.Admin.QueryRow(context.Background(), `SELECT count(*) FROM iam.access_grant WHERE tenant_id=$1 AND tenant_membership_id=$2`,
		f.s.tenantA, f.targetMember).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if grants != 0 {
		t.Fatalf("denial changed access: grants=%d", grants)
	}
}

func roleChangeAssertPending(t *testing.T, f roleChangeRaceFixture) {
	t.Helper()
	var status string
	if err := f.s.h.Admin.QueryRow(context.Background(), `SELECT status FROM iam.role_change_request WHERE tenant_id=$1 AND id=$2`,
		f.s.tenantA, uuid.MustParse(f.requestID)).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "PENDING" {
		t.Fatalf("denial changed request status: %s", status)
	}
}

func TestDirectoryRoleChangeMakerLogoutDoesNotInvalidateProposal(t *testing.T) {
	f := newRoleChangeRaceFixture(t)
	if _, err := f.s.h.Admin.Exec(context.Background(), `UPDATE iam.session SET revoked_at=clock_timestamp() WHERE actor_id=$1 AND revoked_at IS NULL`, f.s.actor); err != nil {
		t.Fatal(err)
	}
	code, out, _ := roleCall(f.s, f.checkerCookie, f.checkerCSRF, f.s.tenantA, http.MethodPost,
		"/api/v1/admin/role-change-requests/"+f.requestID+"/approve", f.requestETag,
		"role-change-maker-logout-approve-0001", `{}`)
	if code != http.StatusOK || out["request"].(map[string]any)["status"] != "APPROVED" {
		t.Fatalf("maker logout incorrectly blocked distinct checker: %d %v", code, out)
	}
}

func TestDirectoryRoleChangeMakerAuthorityLossBlocksApprovalButAllowsRejection(t *testing.T) {
	f := newRoleChangeRaceFixture(t)
	ctx := context.Background()
	if _, err := f.s.h.Admin.Exec(ctx, `UPDATE iam.access_grant g SET valid_period=tstzrange(lower(g.valid_period),clock_timestamp(),'[)')
		FROM iam.tenant_membership m, iam.role r
		WHERE g.tenant_id=$1 AND g.tenant_membership_id=m.id AND m.tenant_id=$1 AND m.actor_id=$2
		AND r.tenant_id=g.tenant_id AND r.id=g.role_id AND r.code='TENANT_ADMIN'
		AND g.scope_type='TENANT' AND g.valid_period @> clock_timestamp()`, f.s.tenantA, f.s.actor); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/admin/role-change-requests/" + f.requestID
	code, out, _ := roleCall(f.s, f.checkerCookie, f.checkerCSRF, f.s.tenantA, http.MethodPost,
		path+"/approve", f.requestETag, "role-change-maker-lost-approve-0001", `{}`)
	if code != http.StatusConflict || out["code"] != "ROLE_CHANGE_MAKER_UNAUTHORIZED" {
		t.Fatalf("lost maker authority was approved: %d %v", code, out)
	}
	roleChangeAssertPendingWithoutGrant(t, f)
	code, out, _ = roleCall(f.s, f.checkerCookie, f.checkerCSRF, f.s.tenantA, http.MethodPost,
		path+"/reject", f.requestETag, "role-change-maker-lost-reject-0001", `{"reasonCode":"STALE_REQUEST"}`)
	if code != http.StatusOK || out["request"].(map[string]any)["status"] != "REJECTED" {
		t.Fatalf("checker could not close stale maker proposal: %d %v", code, out)
	}
}

func TestDirectoryRoleChangeSplitTenantAuthorityAndForgedAppHeaders(t *testing.T) {
	for _, tenantPermission := range []string{"identity.user.read", "identity.role.manage"} {
		t.Run(tenantPermission, func(t *testing.T) {
			s := newAuthzServer(t)
			ctx := context.Background()
			var member uuid.UUID
			if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2`, s.tenantA, s.actor).Scan(&member); err != nil {
				t.Fatal(err)
			}
			for _, permission := range []string{"identity.user.read", "identity.role.manage"} {
				var role uuid.UUID
				if err := s.h.Admin.QueryRow(ctx, `INSERT INTO iam.role(tenant_id,code,name,is_system_role) VALUES ($1,$2,'Synthetic split authority',false) RETURNING id`,
					s.tenantA, "SPLIT_"+strings.ToUpper(strings.ReplaceAll(permission, ".", "_"))).Scan(&role); err != nil {
					t.Fatal(err)
				}
				if _, err := s.h.Admin.Exec(ctx, `INSERT INTO iam.role_permission(tenant_id,role_id,permission_code) VALUES ($1,$2,$3)`, s.tenantA, role, permission); err != nil {
					t.Fatal(err)
				}
				scope, scopeID := "TENANT", any(nil)
				if permission != tenantPermission {
					scope, scopeID = "ORGANIZATION", uuid.New()
				}
				if _, err := s.h.Admin.Exec(ctx, `INSERT INTO iam.access_grant(tenant_id,tenant_membership_id,role_id,scope_type,scope_id) VALUES ($1,$2,$3,$4,$5)`,
					s.tenantA, member, role, scope, scopeID); err != nil {
					t.Fatal(err)
				}
			}
			cookie, csrf := directorySession(t, s)
			stepUpDirectory(t, s, cookie, csrf)
			if code, _ := directoryCall(s, cookie, s.tenantA, "/api/v1/admin/privileged-role-assignment-options", ""); code != http.StatusForbidden {
				t.Fatalf("split %s authority read options: %d", tenantPermission, code)
			}
			_, target := roleTarget(t, s, "role-change-split-"+strings.ReplaceAll(tenantPermission, ".", "-"))
			body := `{"operation":"ASSIGN","roleCode":"PLAN_PUBLISHER","configurationHash":"` + strings.Repeat("0", 64) + `","reasonCode":"ONBOARDING"}`
			code, out, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost,
				"/api/v1/admin/users/"+target.String()+"/role-change-requests", `"1"`, "role-change-split-authority-0001", body)
			if code != http.StatusForbidden {
				t.Fatalf("split %s authority proposed role: %d %v", tenantPermission, code, out)
			}
		})
	}

	s := newAuthzServer(t)
	makerCookie, makerCSRF, _, _ := roleChangeManagers(t, s)
	_, target := roleTarget(t, s, "role-change-forged-app-target")
	hash := roleChangeOption(t, s, makerCookie, makerCSRF, "PLAN_PUBLISHER")
	body := `{"operation":"ASSIGN","roleCode":"PLAN_PUBLISHER","configurationHash":"` + hash + `","reasonCode":"ONBOARDING"}`
	for _, app := range []string{"provider", "member"} {
		t.Run("forged-"+app, func(t *testing.T) {
			if code, _ := directoryCall(s, makerCookie, s.tenantA, "/api/v1/admin/privileged-role-assignment-options", app); code != http.StatusForbidden {
				t.Fatalf("%s app read B options: %d", app, code)
			}
			rec := s.do(call{method: http.MethodPost, path: "/api/v1/admin/users/" + target.String() + "/role-change-requests",
				cookie: makerCookie, csrf: makerCSRF, headers: map[string]string{identityhttp.TenantHeader: s.tenantA.String(),
					identity.AppHeader: app, "If-Match": `"1"`, "Idempotency-Key": "role-change-forged-" + app + "-0001"}, body: body})
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s app proposed B role: %d %s", app, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestDirectoryRoleChangeGlobalActorSeparationAcrossSessionsAndHistory(t *testing.T) {
	t.Run("maker second session", func(t *testing.T) {
		f := newRoleChangeRaceFixture(t)
		cookie, csrf := directorySession(t, f.s)
		stepUpDirectory(t, f.s, cookie, csrf)
		code, out, _ := roleCall(f.s, cookie, csrf, f.s.tenantA, http.MethodPost,
			"/api/v1/admin/role-change-requests/"+f.requestID+"/approve", f.requestETag,
			"role-change-maker-second-session-0001", `{}`)
		if code != http.StatusForbidden || out["code"] != "MAKER_CHECKER_SAME_ACTOR" {
			t.Fatalf("maker second session approved: %d %v", code, out)
		}
		roleChangeAssertPendingWithoutGrant(t, f)
	})
	t.Run("maker historical membership", func(t *testing.T) {
		f := newRoleChangeRaceFixture(t)
		ctx := context.Background()
		if _, err := f.s.h.Admin.Exec(ctx, `UPDATE iam.tenant_membership SET valid_period=daterange(CURRENT_DATE-2,CURRENT_DATE,'[)') WHERE tenant_id=$1 AND actor_id=$2`, f.s.tenantA, f.s.actor); err != nil {
			t.Fatal(err)
		}
		var current uuid.UUID
		if err := f.s.h.Admin.QueryRow(ctx, `INSERT INTO iam.tenant_membership(tenant_id,actor_id,membership_status,valid_period)
			VALUES ($1,$2,'ACTIVE',daterange(CURRENT_DATE,NULL,'[)')) RETURNING id`, f.s.tenantA, f.s.actor).Scan(&current); err != nil {
			t.Fatal(err)
		}
		var adminRole uuid.UUID
		if err := f.s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.role WHERE tenant_id=$1 AND code='TENANT_ADMIN'`, f.s.tenantA).Scan(&adminRole); err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.h.Admin.Exec(ctx, `INSERT INTO iam.access_grant(tenant_id,tenant_membership_id,role_id,scope_type) VALUES ($1,$2,$3,'TENANT')`,
			f.s.tenantA, current, adminRole); err != nil {
			t.Fatal(err)
		}
		cookie, csrf := directorySession(t, f.s)
		stepUpDirectory(t, f.s, cookie, csrf)
		if code, _ := directoryCall(f.s, cookie, f.s.tenantA, "/api/v1/admin/privileged-role-assignment-options", ""); code != http.StatusOK {
			t.Fatalf("new membership did not have current authority: %d", code)
		}
		code, out, _ := roleCall(f.s, cookie, csrf, f.s.tenantA, http.MethodPost,
			"/api/v1/admin/role-change-requests/"+f.requestID+"/approve", f.requestETag,
			"role-change-maker-historical-0001", `{}`)
		if code != http.StatusForbidden || out["code"] != "MAKER_CHECKER_SAME_ACTOR" {
			t.Fatalf("maker new membership approved historical proposal: %d %v", code, out)
		}
		roleChangeAssertPendingWithoutGrant(t, f)
	})
	t.Run("target second session", func(t *testing.T) {
		f := newRoleChangeRaceFixture(t)
		if _, err := f.s.prov.GrantRole(context.Background(), application.GrantRoleInput{
			TenantID: f.s.tenantA, ActorID: f.targetActor, RoleCode: "TENANT_ADMIN"}); err != nil {
			t.Fatal(err)
		}
		// roleTarget records the account under this exact synthetic handle.
		var handle string
		if err := f.s.h.Admin.QueryRow(context.Background(), `SELECT identity_subject FROM iam.actor WHERE id=$1`, f.targetActor).Scan(&handle); err != nil {
			t.Fatal(err)
		}
		cookie, csrf := roleChangeAuthorityLogin(t, f.s, handle)
		code, out, _ := roleCall(f.s, cookie, csrf, f.s.tenantA, http.MethodPost,
			"/api/v1/admin/role-change-requests/"+f.requestID+"/approve", f.requestETag,
			"role-change-target-second-session-0001", `{}`)
		if code != http.StatusForbidden || out["code"] != "MAKER_CHECKER_SAME_ACTOR" {
			t.Fatalf("target as checker approved: %d %v", code, out)
		}
		roleChangeAssertPending(t, f)
	})
}

func TestDirectoryRoleChangeRevokePreservesLastTenantUserManager(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	var narrowRole, makerMember, checkerMember uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO iam.role(tenant_id,code,name,is_system_role)
		VALUES ($1,'ROLE_CHANGE_NARROW_MANAGER','Synthetic role change manager',false) RETURNING id`, s.tenantA).Scan(&narrowRole); err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `INSERT INTO iam.role_permission(tenant_id,role_id,permission_code)
		VALUES ($1,$2,'identity.user.read'),($1,$2,'identity.role.manage')`, s.tenantA, narrowRole); err != nil {
		t.Fatal(err)
	}
	checker, err := s.svc.CreateAccount(ctx, "role-change-narrow-checker", "Narrow checker", "role-change-narrow-checker", testPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2`, s.tenantA, s.actor).Scan(&makerMember); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO iam.tenant_membership(tenant_id,actor_id,membership_status) VALUES ($1,$2,'ACTIVE') RETURNING id`,
		s.tenantA, checker).Scan(&checkerMember); err != nil {
		t.Fatal(err)
	}
	for _, member := range []uuid.UUID{makerMember, checkerMember} {
		if _, err := s.h.Admin.Exec(ctx, `INSERT INTO iam.access_grant(tenant_id,tenant_membership_id,role_id,scope_type) VALUES ($1,$2,$3,'TENANT')`,
			s.tenantA, member, narrowRole); err != nil {
			t.Fatal(err)
		}
	}
	targetActor, targetMember := roleTarget(t, s, "role-change-last-user-manager-target")
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: targetActor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	makerCookie, makerCSRF := directorySession(t, s)
	stepUpDirectory(t, s, makerCookie, makerCSRF)
	checkerCookie, checkerCSRF := roleChangeAuthorityLogin(t, s, "role-change-narrow-checker")
	if code, _ := directoryCall(s, makerCookie, s.tenantA, "/api/v1/admin/privileged-role-assignment-options", ""); code != http.StatusOK {
		t.Fatalf("narrow maker missing B authority: %d", code)
	}
	hash := roleChangeOption(t, s, makerCookie, makerCSRF, "TENANT_ADMIN")
	eligibilityPath := "/api/v1/admin/users/" + targetMember.String() + "/role-change-eligibility"
	code, eligibility, headers := roleCall(s, makerCookie, makerCSRF, s.tenantA, http.MethodGet, eligibilityPath, "", "", "")
	if code != http.StatusOK || len(eligibility["revokeGrantIds"].([]any)) != 1 {
		t.Fatalf("sole privileged grant not eligible: %d %v", code, eligibility)
	}
	grantID := eligibility["revokeGrantIds"].([]any)[0].(string)
	version := headers.Get("ETag")
	requestBody := `{"operation":"REVOKE","grantId":"` + grantID + `","configurationHash":"` + hash + `","reasonCode":"ACCESS_REVIEW"}`
	code, created, requestHeaders := roleCall(s, makerCookie, makerCSRF, s.tenantA, http.MethodPost,
		"/api/v1/admin/users/"+targetMember.String()+"/role-change-requests", version,
		"role-change-last-user-create-0001", requestBody)
	if code != http.StatusCreated {
		t.Fatalf("last user manager revoke proposal: %d %v", code, created)
	}
	requestID := created["request"].(map[string]any)["id"].(string)
	code, out, _ := roleCall(s, checkerCookie, checkerCSRF, s.tenantA, http.MethodPost,
		"/api/v1/admin/role-change-requests/"+requestID+"/approve", requestHeaders.Get("ETag"),
		"role-change-last-user-approve-0001", `{}`)
	if code != http.StatusConflict || out["code"] != "LAST_TENANT_MANAGER" {
		t.Fatalf("last user manager revoked: %d %v", code, out)
	}
	var status string
	var targetVersion, requestVersion int64
	var currentGrant bool
	var effects, receipts int
	if err := s.h.Admin.QueryRow(ctx, `SELECT status,row_version FROM iam.role_change_request WHERE tenant_id=$1 AND id=$2`, s.tenantA, uuid.MustParse(requestID)).Scan(&status, &requestVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT row_version FROM iam.tenant_membership WHERE tenant_id=$1 AND id=$2`, s.tenantA, targetMember).Scan(&targetVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT valid_period @> clock_timestamp() FROM iam.access_grant WHERE tenant_id=$1 AND id=$2`,
		s.tenantA, uuid.MustParse(grantID)).Scan(&currentGrant); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM audit.event WHERE tenant_id=$1 AND action_code IN ('role_change_request.approve','access_grant.revoke') AND outcome='SUCCESS' AND (resource_id=$2 OR detail_json->>'request_id'=$3)`,
		s.tenantA, uuid.MustParse(requestID), requestID).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.role_change_command_receipt WHERE tenant_id=$1 AND request_id=$2 AND command_code='APPROVE'`,
		s.tenantA, uuid.MustParse(requestID)).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if status != "PENDING" || requestVersion != 1 || targetVersion != int64(eligibility["membershipRowVersion"].(float64)) || !currentGrant || effects != 0 || receipts != 0 {
		t.Fatalf("last manager denial changed effect: status=%s request_version=%d target_version=%d current=%t audits=%d receipts=%d",
			status, requestVersion, targetVersion, currentGrant, effects, receipts)
	}
}
