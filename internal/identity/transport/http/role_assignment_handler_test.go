package identityhttp_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity/application"
	identitypg "github.com/celikbros/kapsora/internal/identity/infrastructure/postgres"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
)

func TestDirectoryRoleRouteCoexistence(t *testing.T) {
	// Exercise dispatch: chi.Match can match a parent mount even when its child returns 404.
	marker := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(name)) }
	}
	r := chi.NewRouter()
	r.Route("/api/v1", func(api chi.Router) {
		api.Route("/admin/users", func(users chi.Router) {
			users.Get("/{membershipId}", marker("directory"))
			users.Get("/{membershipId}/role-grants", marker("grants"))
			users.Post("/{membershipId}/role-grants", marker("assign"))
			users.Post("/{membershipId}/role-grants/{grantId}/revoke", marker("revoke"))
			users.Get("/{membershipId}/role-change-eligibility", marker("role-change-eligibility"))
			users.Post("/{membershipId}/role-change-requests", marker("role-change-create"))
		})
		api.Route("/admin", func(admin chi.Router) {
			admin.Get("/role-assignment-options", marker("options"))
			admin.Get("/role-assignment-organizations", marker("organizations"))
			admin.Get("/privileged-role-assignment-options", marker("privileged-options"))
			admin.Get("/role-change-requests", marker("role-change-list"))
			admin.Get("/role-change-requests/{requestId}", marker("role-change-detail"))
			admin.Post("/role-change-requests/{requestId}/approve", marker("role-change-approve"))
			admin.Post("/role-change-requests/{requestId}/reject", marker("role-change-reject"))
			admin.Post("/role-change-requests/{requestId}/cancel", marker("role-change-cancel"))
		})
		api.Route("/admin/invitations", func(invitations chi.Router) {
			invitations.Get("/", marker("invitations"))
		})
	})
	member, grant := uuid.NewString(), uuid.NewString()
	for _, tc := range []struct{ method, path, want string }{
		{http.MethodGet, "/api/v1/admin/users/" + member, "directory"},
		{http.MethodGet, "/api/v1/admin/role-assignment-options", "options"},
		{http.MethodGet, "/api/v1/admin/role-assignment-organizations", "organizations"},
		{http.MethodGet, "/api/v1/admin/users/" + member + "/role-grants", "grants"},
		{http.MethodPost, "/api/v1/admin/users/" + member + "/role-grants", "assign"},
		{http.MethodPost, "/api/v1/admin/users/" + member + "/role-grants/" + grant + "/revoke", "revoke"},
		{http.MethodGet, "/api/v1/admin/invitations/", "invitations"},
		{http.MethodGet, "/api/v1/admin/users/" + member + "/role-change-eligibility", "role-change-eligibility"},
		{http.MethodPost, "/api/v1/admin/users/" + member + "/role-change-requests", "role-change-create"},
		{http.MethodGet, "/api/v1/admin/privileged-role-assignment-options", "privileged-options"},
		{http.MethodGet, "/api/v1/admin/role-change-requests", "role-change-list"},
		{http.MethodGet, "/api/v1/admin/role-change-requests/" + grant, "role-change-detail"},
		{http.MethodPost, "/api/v1/admin/role-change-requests/" + grant + "/approve", "role-change-approve"},
		{http.MethodPost, "/api/v1/admin/role-change-requests/" + grant + "/reject", "role-change-reject"},
		{http.MethodPost, "/api/v1/admin/role-change-requests/" + grant + "/cancel", "role-change-cancel"},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusOK || rec.Body.String() != tc.want {
			t.Errorf("%s %s: status %d, body %q; want %q", tc.method, tc.path, rec.Code, rec.Body.String(), tc.want)
		}
	}
}

func roleCall(s *authzServer, cookie *http.Cookie, csrf string, tenant uuid.UUID, method, path, etag, key, body string) (int, map[string]any, http.Header) {
	headers := map[string]string{identityhttp.TenantHeader: tenant.String()}
	if etag != "" {
		headers["If-Match"] = etag
	}
	if key != "" {
		headers["Idempotency-Key"] = key
	}
	rec := s.do(call{method: method, path: path, cookie: cookie, csrf: csrf, headers: headers, body: body})
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Header()
}

func roleTarget(t *testing.T, s *authzServer, name string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	actor, err := s.svc.CreateAccount(context.Background(), name, name, name, testPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	var membership uuid.UUID
	if err := s.h.Admin.QueryRow(context.Background(), `INSERT INTO iam.tenant_membership (tenant_id,actor_id,membership_status) VALUES ($1,$2,'ACTIVE') RETURNING id`, s.tenantA, actor).Scan(&membership); err != nil {
		t.Fatal(err)
	}
	return actor, membership
}

func TestDirectoryRoleZeroGrantAssignRevokeReplayAndIsolation(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	_, invitationCode := newInvitationFixture(t, s, "role-lifecycle-0001")
	acceptedStatus, accepted, _ := newInvitationCall(s, "/api/v1/invitations/accept-new",
		map[string]any{"code": invitationCode, "displayName": "Role recipient", "password": invitationNewPassword, "confirmed": true},
		"role-invite-accept-0001", nil)
	if acceptedStatus != 200 || accepted["accessPending"] != true {
		t.Fatalf("new invitation acceptance: %d %v", acceptedStatus, accepted)
	}
	targetMember := uuid.MustParse(accepted["membershipId"].(string))
	handle := accepted["loginHandle"].(string)
	var targetActor uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.actor WHERE identity_subject=$1`, handle).Scan(&targetActor); err != nil {
		t.Fatal(err)
	}
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantB, ActorID: targetActor, RoleCode: "AUDITOR"}); err != nil {
		t.Fatal(err)
	}
	managerCookie, managerCSRF := directorySession(t, s)
	path := "/api/v1/admin/users/" + targetMember.String() + "/role-grants"
	code, before, headers := roleCall(s, managerCookie, managerCSRF, s.tenantA, http.MethodGet, path, "", "", "")
	if code != 200 || before["canAssign"] != true || len(before["items"].([]any)) != 0 || headers.Get("ETag") == "" {
		t.Fatalf("zero-grant page: %d %v", code, before)
	}
	initialTag := headers.Get("ETag")
	code, options, _ := roleCall(s, managerCookie, managerCSRF, s.tenantA, http.MethodGet, "/api/v1/admin/role-assignment-options", "", "", "")
	if code != 200 || len(options["items"].([]any)) == 0 {
		t.Fatalf("options: %d %v", code, options)
	}
	login := s.do(call{method: http.MethodPost, path: "/api/v1/session/login", body: `{"username":"` + handle + `","password":"` + invitationNewPassword + `"}`})
	if login.Code != 200 {
		t.Fatalf("target login: %d", login.Code)
	}
	targetCookie := sessionCookie(t, login, s.cookies.Name())
	targetCSRF, _ := decodeBody(t, login)["csrfToken"].(string)
	if rec := s.do(call{method: http.MethodPost, path: "/api/v1/session/switch-tenant", cookie: targetCookie, csrf: targetCSRF,
		body: `{"tenantId":"` + s.tenantA.String() + `"}`}); rec.Code != 200 {
		t.Fatalf("target switch: %d %s", rec.Code, rec.Body.String())
	}
	appsForA := func() []any {
		t.Helper()
		me := decodeBody(t, s.do(call{method: http.MethodGet, path: "/api/v1/me", cookie: targetCookie}))
		for _, raw := range me["tenants"].([]any) {
			entry := raw.(map[string]any)
			if entry["tenant"].(map[string]any)["id"] == s.tenantA.String() {
				return entry["apps"].([]any)
			}
		}
		t.Fatal("accepted tenant absent from /me")
		return nil
	}
	if len(appsForA()) != 0 {
		t.Fatal("accepted zero-grant membership was not waiting")
	}
	probe := func() int {
		return s.do(call{method: http.MethodGet, path: "/api/v1/probe-rule", cookie: targetCookie,
			headers: map[string]string{identityhttp.TenantHeader: s.tenantA.String()}}).Code
	}
	ruleRead := func() int {
		return s.do(call{method: http.MethodGet, path: "/api/v1/rule-sets/", cookie: targetCookie,
			headers: map[string]string{identityhttp.TenantHeader: s.tenantA.String(), "X-Kapsora-App": "backoffice"}}).Code
	}
	if probe() != http.StatusForbidden {
		t.Fatal("zero-grant target could read rules")
	}
	if ruleRead() != http.StatusForbidden {
		t.Fatal("zero-grant target could read rule sets")
	}
	assignBody := `{"roleCode":"RULE_AUTHOR","scopeType":"TENANT","reasonCode":"ONBOARDING"}`
	const assignKey = "role-assign-request-0001"
	if code, body, _ := roleCall(s, managerCookie, managerCSRF, s.tenantA, http.MethodPost, path, initialTag, assignKey, assignBody); code != 403 || body["code"] != "STEP_UP_REQUIRED" {
		t.Fatalf("assignment without step-up: %d %v", code, body)
	}
	stepUpDirectory(t, s, managerCookie, managerCSRF)
	code, assigned, headers := roleCall(s, managerCookie, managerCSRF, s.tenantA, http.MethodPost, path, initialTag, assignKey, assignBody)
	if code != 200 || headers.Get("ETag") == initialTag || assigned["grant"].(map[string]any)["roleCode"] != "RULE_AUTHOR" {
		t.Fatalf("assignment: %d %v", code, assigned)
	}
	assignedTag := headers.Get("ETag")
	grantID := assigned["grant"].(map[string]any)["id"].(string)
	if probe() != http.StatusNoContent {
		t.Fatal("fresh target request did not gain rule read")
	}
	if ruleRead() != http.StatusOK {
		t.Fatal("assigned target cannot read real rule sets")
	}
	if apps := appsForA(); len(apps) != 1 || apps[0] != "backoffice" {
		t.Fatalf("assigned apps: %v", apps)
	}
	if code, _, replay := roleCall(s, managerCookie, managerCSRF, s.tenantA, http.MethodPost, path, initialTag, assignKey, assignBody); code != 200 || replay.Get("Idempotent-Replayed") != "true" || replay.Get("ETag") != assignedTag {
		t.Fatalf("assignment replay: %d", code)
	}
	if code, body, _ := roleCall(s, managerCookie, managerCSRF, s.tenantA, http.MethodPost, path, initialTag, assignKey,
		`{"roleCode":"RULE_AUTHOR","scopeType":"TENANT","reasonCode":"DUTY_ASSIGNMENT"}`); code != 409 || body["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("changed-body replay: %d %v", code, body)
	}
	if code, body, _ := roleCall(s, managerCookie, managerCSRF, s.tenantA, http.MethodPost, path, assignedTag, assignKey, assignBody); code != 409 || body["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("changed-ETag replay: %d %v", code, body)
	}
	if code, body, _ := roleCall(s, managerCookie, managerCSRF, s.tenantA, http.MethodPost, path, initialTag, "role-stale-assign-0001", assignBody); code != 412 || body["code"] != "ETAG_MISMATCH" {
		t.Fatalf("stale assignment version: %d %v", code, body)
	}
	if code, body, _ := roleCall(s, managerCookie, managerCSRF, s.tenantA, http.MethodPost, path, assignedTag, "role-assign-request-0002", assignBody); code != 409 || body["code"] != "EXISTING_ACCESS_CONFLICT" {
		t.Fatalf("second grant: %d %v", code, body)
	}
	revokePath := path + "/" + grantID + "/revoke"
	const revokeKey = "role-revoke-request-0001"
	code, ended, headers := roleCall(s, managerCookie, managerCSRF, s.tenantA, http.MethodPost, revokePath, assignedTag, revokeKey, `{"reasonCode":"DUTY_ENDED"}`)
	if code != 200 || headers.Get("ETag") == assignedTag || ended["grant"].(map[string]any)["validTo"] == nil {
		t.Fatalf("revocation: %d %v", code, ended)
	}
	if probe() != http.StatusForbidden {
		t.Fatal("revoked target kept rule read")
	}
	if ruleRead() != http.StatusForbidden {
		t.Fatal("revoked target kept real rule read")
	}
	if len(appsForA()) != 0 {
		t.Fatal("revoked zero-grant membership did not return to waiting")
	}
	if rec := s.do(call{method: http.MethodPost, path: "/api/v1/session/switch-tenant", cookie: targetCookie, csrf: targetCSRF,
		body: `{"tenantId":"` + s.tenantB.String() + `"}`}); rec.Code != 200 {
		t.Fatal("other tenant credentials or membership changed")
	}
	if rec := s.do(call{method: http.MethodGet, path: "/api/v1/probe", cookie: targetCookie,
		headers: map[string]string{identityhttp.TenantHeader: s.tenantB.String()}}); rec.Code != 200 {
		t.Fatal("other tenant grant changed")
	}
	var audits, grants int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM audit.event WHERE tenant_id=$1 AND resource_id=$2 AND action_code IN ('access_grant.assign','access_grant.revoke')`, s.tenantA, grantID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.access_grant WHERE tenant_id=$1 AND tenant_membership_id=$2`, s.tenantA, targetMember).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if audits != 2 || grants != 1 {
		t.Fatalf("audit/history mismatch: %d events, %d grants", audits, grants)
	}
}

func TestDirectoryRoleAuthorityScopeAndStrictBodies(t *testing.T) {
	s := newAuthzServer(t)
	_, target := roleTarget(t, s, "role-strict-target")
	cookie, csrf := directorySession(t, s)
	path := "/api/v1/admin/users/" + target.String() + "/role-grants"
	if code, body, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodGet, path, "", "", ""); code != 403 || body["code"] != "PERMISSION_DENIED" {
		t.Fatalf("auditor gained management: %d %v", code, body)
	}
	if _, err := s.prov.GrantRole(context.Background(), application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	stepUpDirectory(t, s, cookie, csrf)
	bad := []string{
		`null`, `{}`, `{"roleCode":"RULE_AUTHOR","scopeType":"TENANT","reasonCode":"ONBOARDING","organizationRelationshipId":null}`,
		`{"roleCode":"RULE_AUTHOR","scopeType":"TENANT","reasonCode":"ONBOARDING","organizationRelationshipId":"` + uuid.NewString() + `"}`,
		`{"roleCode":"RULE_AUTHOR","scopeType":"PERSON","reasonCode":"ONBOARDING"}`,
		`{"roleCode":"RULE_AUTHOR","scopeType":"TENANT","reasonCode":"ONBOARDING","extra":1}`,
	}
	for i, body := range bad {
		code, out, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, `"1"`, "role-invalid-body-"+uuid.NewString(), body)
		if code != 400 {
			t.Fatalf("invalid body %d: %d %v", i, code, out)
		}
	}
	code, out, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, `"1"`, "role-privileged-0001", `{"roleCode":"TENANT_ADMIN","scopeType":"TENANT","reasonCode":"ONBOARDING"}`)
	if code != 409 || out["code"] != "ROLE_ASSIGNMENT_UNSUPPORTED" {
		t.Fatalf("privileged role reached command: %d %v", code, out)
	}
	code, out, _ = roleCall(s, cookie, csrf, s.tenantA, http.MethodGet, path, "", "", "")
	if code != 200 || out["canAssign"] != true {
		t.Fatalf("target changed from rejected bodies: %d %v", code, out)
	}
}

func TestDirectoryRoleSyncRefusesInUsePermissionChange(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	actor, err := s.svc.CreateAccount(ctx, "role-sync-target", "Role sync target", "role-sync-target", testPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: actor, RoleCode: "RULE_AUTHOR"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `DELETE FROM iam.role_permission rp USING iam.role r
		WHERE rp.tenant_id=r.tenant_id AND rp.role_id=r.id AND r.tenant_id=$1
		AND r.code='RULE_AUTHOR' AND rp.permission_code='rule.read'`, s.tenantA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.prov.SyncSystemRoles(ctx, s.tenantA); !errors.Is(err, application.ErrRoleConfigurationInUse) {
		t.Fatalf("in-use role sync: %v", err)
	}
	var count int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.role_permission rp JOIN iam.role r ON r.tenant_id=rp.tenant_id AND r.id=rp.role_id
		WHERE r.tenant_id=$1 AND r.code='RULE_AUTHOR' AND rp.permission_code='rule.read'`, s.tenantA).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("refused sync nevertheless changed live role")
	}
	if _, err := s.h.Admin.Exec(ctx, `DELETE FROM iam.role_permission rp USING iam.role r
		WHERE rp.tenant_id=r.tenant_id AND rp.role_id=r.id AND r.tenant_id=$1
		AND r.code='SPONSOR_HR' AND rp.permission_code='report.read'`, s.tenantA); err != nil {
		t.Fatal(err)
	}
	// The refused transaction did not poison an otherwise safe sync of an unused role:
	// after restoring the in-use role's template, SPONSOR_HR can be synchronized.
	if _, err := s.h.Admin.Exec(ctx, `INSERT INTO iam.role_permission (tenant_id,role_id,permission_code)
		SELECT tenant_id,id,'rule.read' FROM iam.role WHERE tenant_id=$1 AND code='RULE_AUTHOR'`, s.tenantA); err != nil {
		t.Fatal(err)
	}
	if result, err := s.prov.SyncSystemRoles(ctx, s.tenantA); err != nil || result.PermissionsAdded < 1 {
		t.Fatalf("unused role sync: %+v %v", result, err)
	}
}

func TestDirectoryRoleOfflineGrantTouchesExistingMembershipAggregate(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	actor, member := roleTarget(t, s, "role-offline-grant-target")
	cookie, csrf := directorySession(t, s)
	stepUpDirectory(t, s, cookie, csrf)
	path := "/api/v1/admin/users/" + member.String() + "/role-grants"
	code, before, headers := roleCall(s, cookie, csrf, s.tenantA, http.MethodGet, path, "", "", "")
	if code != 200 || before["canAssign"] != true {
		t.Fatalf("before offline grant: %d %v", code, before)
	}
	oldTag := headers.Get("ETag")
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: actor, RoleCode: "AUDITOR"}); err != nil {
		t.Fatal(err)
	}
	code, after, headers := roleCall(s, cookie, csrf, s.tenantA, http.MethodGet, path, "", "", "")
	if code != 200 || headers.Get("ETag") == oldTag || after["canAssign"] != false || len(after["items"].([]any)) != 1 {
		t.Fatalf("offline grant aggregate: %d %v", code, after)
	}
	if code, body, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, oldTag, "role-offline-stale-0001",
		`{"roleCode":"RULE_AUTHOR","scopeType":"TENANT","reasonCode":"ONBOARDING"}`); code != 412 || body["code"] != "ETAG_MISMATCH" {
		t.Fatalf("offline grant stale assignment: %d %v", code, body)
	}
}

func TestDirectoryRoleProviderRelationshipSelectorAndScope(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	relationship, err := s.prov.EnsureProviderOrganization(ctx, s.tenantA, "ROLE_HOSP", "Role test hospital")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.prov.EnsureProviderOrganization(ctx, s.tenantA, "ROLE_OTHER", "Other role hospital")
	if err != nil {
		t.Fatal(err)
	}
	var globalOrg, profile, otherProfile uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `SELECT organization_id FROM directory.tenant_organization WHERE tenant_id=$1 AND id=$2`, s.tenantA, relationship).Scan(&globalOrg); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []uuid.UUID{relationship, other} {
		var id uuid.UUID
		if err := s.h.Admin.QueryRow(ctx, `INSERT INTO provider.provider_profile (tenant_id,tenant_organization_id,provider_type,status)
			VALUES ($1,$2,'HOSPITAL','ACTIVE') RETURNING id`, s.tenantA, rel).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if rel == relationship {
			profile = id
		} else {
			otherProfile = id
		}
	}
	_, member := roleTarget(t, s, "role-provider-target")
	cookie, csrf := directorySession(t, s)
	stepUpDirectory(t, s, cookie, csrf)
	code, picker, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodGet, "/api/v1/admin/role-assignment-organizations", "", "", "")
	if code != 200 || len(picker["items"].([]any)) != 2 {
		t.Fatalf("provider picker: %d %v", code, picker)
	}
	path := "/api/v1/admin/users/" + member.String() + "/role-grants"
	for _, wrong := range []uuid.UUID{globalOrg, profile, uuid.New()} {
		body := `{"roleCode":"PROVIDER_ADMIN","scopeType":"ORGANIZATION","organizationRelationshipId":"` + wrong.String() + `","reasonCode":"DUTY_ASSIGNMENT"}`
		if code, out, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, `"1"`, "role-invalid-org-"+uuid.NewString(), body); code != 404 || out["code"] != "RESOURCE_NOT_FOUND" {
			t.Fatalf("unqualified provider selector %s: %d %v", wrong, code, out)
		}
	}
	body := `{"roleCode":"PROVIDER_ADMIN","scopeType":"ORGANIZATION","organizationRelationshipId":"` + relationship.String() + `","reasonCode":"DUTY_ASSIGNMENT"}`
	code, assigned, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, `"1"`, "role-provider-assign-0001", body)
	if code != 200 || assigned["grant"].(map[string]any)["organizationRelationshipId"] != relationship.String() {
		t.Fatalf("provider assignment: %d %v", code, assigned)
	}
	code, history, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodGet, path, "", "", "")
	if code != 200 || history["items"].([]any)[0].(map[string]any)["organizationRelationshipId"] != relationship.String() ||
		history["items"].([]any)[0].(map[string]any)["organizationDisplayName"] != "Role test hospital" {
		t.Fatalf("provider history relationship projection: %d %v", code, history)
	}
	login := s.do(call{method: http.MethodPost, path: "/api/v1/session/login", body: `{"username":"role-provider-target","password":"` + testPassword + `"}`})
	if login.Code != 200 {
		t.Fatalf("provider login: %d", login.Code)
	}
	targetCookie := sessionCookie(t, login, s.cookies.Name())
	targetCSRF, _ := decodeBody(t, login)["csrfToken"].(string)
	if rec := s.do(call{method: http.MethodPost, path: "/api/v1/session/switch-tenant", cookie: targetCookie, csrf: targetCSRF,
		body: `{"tenantId":"` + s.tenantA.String() + `"}`}); rec.Code != 200 {
		t.Fatalf("provider switch: %d", rec.Code)
	}
	probe := func(rel uuid.UUID) int {
		return s.do(call{method: http.MethodGet, path: "/api/v1/probe-provider/" + rel.String(), cookie: targetCookie,
			headers: map[string]string{identityhttp.TenantHeader: s.tenantA.String(), "X-Kapsora-App": "provider"}}).Code
	}
	if probe(relationship) != http.StatusNoContent || probe(other) != http.StatusForbidden {
		t.Fatal("provider scope crossed relationships")
	}
	providerGet := func(id uuid.UUID) (int, http.Header) {
		rec := s.do(call{method: http.MethodGet, path: "/api/v1/providers/" + id.String(), cookie: targetCookie,
			headers: map[string]string{identityhttp.TenantHeader: s.tenantA.String(), "X-Kapsora-App": "provider"}})
		return rec.Code, rec.Header()
	}
	status, ownHeaders := providerGet(profile)
	if status != http.StatusOK {
		t.Fatalf("assigned provider detail: %d", status)
	}
	if status, _ := providerGet(otherProfile); status != http.StatusNotFound {
		t.Fatalf("other provider detail: %d", status)
	}
	patch := func(id uuid.UUID, tag string) int {
		return s.do(call{method: http.MethodPatch, path: "/api/v1/providers/" + id.String(), cookie: targetCookie, csrf: targetCSRF,
			headers: map[string]string{identityhttp.TenantHeader: s.tenantA.String(), "X-Kapsora-App": "provider",
				"Content-Type": "application/merge-patch+json", "If-Match": tag}, body: `{"networkTier":"ROLE_TEST"}`}).Code
	}
	if status := patch(profile, ownHeaders.Get("ETag")); status != http.StatusOK {
		t.Fatalf("assigned provider work: %d", status)
	}
	if status := patch(otherProfile, `"1"`); status != http.StatusNotFound {
		t.Fatalf("other provider update: %d", status)
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE provider.provider_profile SET status='SUSPENDED' WHERE tenant_id=$1 AND id=$2`, s.tenantA, otherProfile); err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE provider.provider_profile SET contracted_to=clock_timestamp()::date WHERE tenant_id=$1 AND id=$2`, s.tenantA, profile); err != nil {
		t.Fatal(err)
	}
	nonProvider, err := s.prov.EnsureProviderOrganization(ctx, s.tenantA, "ROLE_PAYER_REL", "Role payer relationship")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `INSERT INTO provider.provider_profile (tenant_id,tenant_organization_id,provider_type,status)
		VALUES ($1,$2,'HOSPITAL','ACTIVE')`, s.tenantA, nonProvider); err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE directory.tenant_organization SET relationship_role='PAYER' WHERE tenant_id=$1 AND id=$2`, s.tenantA, nonProvider); err != nil {
		t.Fatal(err)
	}
	code, picker, _ = roleCall(s, cookie, csrf, s.tenantA, http.MethodGet, "/api/v1/admin/role-assignment-organizations", "", "", "")
	if code != 200 || len(picker["items"].([]any)) != 0 {
		t.Fatalf("inactive/ended providers remained eligible: %d %v", code, picker)
	}
	_, rejectedMember := roleTarget(t, s, "role-provider-ineligible")
	for _, rel := range []uuid.UUID{relationship, other, nonProvider} {
		body := `{"roleCode":"PROVIDER_ADMIN","scopeType":"ORGANIZATION","organizationRelationshipId":"` + rel.String() + `","reasonCode":"DUTY_ASSIGNMENT"}`
		if status, out, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, "/api/v1/admin/users/"+rejectedMember.String()+"/role-grants", `"1"`, "role-provider-ineligible-"+uuid.NewString(), body); status != 404 || out["code"] != "RESOURCE_NOT_FOUND" {
			t.Fatalf("inactive/ended provider assignment %s: %d %v", rel, status, out)
		}
	}
}

func TestDirectoryRoleExistingInvitationOtherTenantSurvives(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	manager, managerCSRF := directorySession(t, s)
	stepUpDirectory(t, s, manager, managerCSRF)
	status, invite, _ := invitationCreateCall(s, manager, managerCSRF, s.tenantA, "role-existing-invite-0001", "role-existing@example.test")
	if status != 200 {
		t.Fatalf("create invitation: %d %v", status, invite)
	}
	proof := invitationProof(t, s, s.tenantA, uuid.MustParse(invite["invitationId"].(string)))
	target, err := s.svc.CreateAccount(ctx, "role-existing", "Existing role recipient", "role-existing@example.test", testPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantB, ActorID: target, RoleCode: "AUDITOR"}); err != nil {
		t.Fatal(err)
	}
	login := s.do(call{method: http.MethodPost, path: "/api/v1/session/login", body: `{"username":"role-existing","password":"` + testPassword + `"}`})
	if login.Code != 200 {
		t.Fatalf("existing target login: %d", login.Code)
	}
	cookie := sessionCookie(t, login, s.cookies.Name())
	csrf, _ := decodeBody(t, login)["csrfToken"].(string)
	status, accepted := invitationPost(s, cookie, csrf, "/api/v1/invitations/accept-existing", `{"code":"`+proof+`","confirmed":true}`, "role-existing-accept-0001")
	if status != 200 || accepted["accessPending"] != true {
		t.Fatalf("existing acceptance: %d %v", status, accepted)
	}
	member := uuid.MustParse(accepted["membershipId"].(string))
	path := "/api/v1/admin/users/" + member.String() + "/role-grants"
	code, assigned, headers := roleCall(s, manager, managerCSRF, s.tenantA, http.MethodPost, path, `"1"`, "role-existing-assign-0001",
		`{"roleCode":"RULE_AUTHOR","scopeType":"TENANT","reasonCode":"ONBOARDING"}`)
	if code != 200 {
		t.Fatalf("existing assignment: %d %v", code, assigned)
	}
	grantID := assigned["grant"].(map[string]any)["id"].(string)
	code, ended, _ := roleCall(s, manager, managerCSRF, s.tenantA, http.MethodPost, path+"/"+grantID+"/revoke", headers.Get("ETag"), "role-existing-revoke-0001", `{"reasonCode":"ACCESS_REVIEW"}`)
	if code != 200 {
		t.Fatalf("existing revoke: %d %v", code, ended)
	}
	if rec := s.do(call{method: http.MethodPost, path: "/api/v1/session/switch-tenant", cookie: cookie, csrf: csrf,
		body: `{"tenantId":"` + s.tenantB.String() + `"}`}); rec.Code != 200 {
		t.Fatal("existing account lost tenant B")
	}
	if rec := s.do(call{method: http.MethodGet, path: "/api/v1/probe", cookie: cookie,
		headers: map[string]string{identityhttp.TenantHeader: s.tenantB.String()}}); rec.Code != 200 {
		t.Fatal("existing account lost other-tenant permission")
	}
}

func TestDirectoryRoleZeroPermissionFutureAndPrivateScopeBlockAssignment(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	_, currentMember := roleTarget(t, s, "role-empty-target")
	_, futureMember := roleTarget(t, s, "role-future-target")
	var emptyRole uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO iam.role (tenant_id,code,name,is_system_role) VALUES ($1,'ROLE_EMPTY_TEST','Empty synthetic role',false) RETURNING id`, s.tenantA).Scan(&emptyRole); err != nil {
		t.Fatal(err)
	}
	privateScope := uuid.New()
	if _, err := s.h.Admin.Exec(ctx, `INSERT INTO iam.access_grant (tenant_id,tenant_membership_id,role_id,scope_type,scope_id,valid_period)
		VALUES ($1,$2,$3,'PROGRAM',$4,tstzrange(clock_timestamp()-interval '1 hour',NULL,'[)'))`, s.tenantA, currentMember, emptyRole, privateScope); err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `INSERT INTO iam.access_grant (tenant_id,tenant_membership_id,role_id,scope_type,scope_id,valid_period)
		VALUES ($1,$2,$3,'PROGRAM',$4,tstzrange(clock_timestamp()+interval '1 day',NULL,'[)'))`, s.tenantA, futureMember, emptyRole, uuid.New()); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := directorySession(t, s)
	stepUpDirectory(t, s, cookie, csrf)
	for _, member := range []uuid.UUID{currentMember, futureMember} {
		path := "/api/v1/admin/users/" + member.String() + "/role-grants"
		code, list, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodGet, path, "", "", "")
		if code != 200 || list["canAssign"] != false || list["assignmentRefusalCode"] != "EXISTING_ACCESS_CONFLICT" {
			t.Fatalf("empty/future access eligibility: %d %v", code, list)
		}
		grant := list["items"].([]any)[0].(map[string]any)
		if len(grant) != 12 || grant["scopeType"] != "PROGRAM" || grant["organizationRelationshipId"] != nil || grant["organizationDisplayName"] != nil {
			t.Fatalf("private scope projection: %v", grant)
		}
		if _, leaked := grant["scopeId"]; leaked {
			t.Fatal("private scope id leaked")
		}
		code, result, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, `"1"`, "role-empty-block-"+uuid.NewString(),
			`{"roleCode":"RULE_AUTHOR","scopeType":"TENANT","reasonCode":"ONBOARDING"}`)
		if code != 409 || result["code"] != "EXISTING_ACCESS_CONFLICT" {
			t.Fatalf("empty/future grant bypass: %d %v", code, result)
		}
	}
}

func TestDirectoryRoleAuditFailureRollsBackGrantAndAggregateVersion(t *testing.T) {
	s := newAuthzServer(t, failingDirectoryAudit{})
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	_, member := roleTarget(t, s, "role-audit-target")
	cookie, csrf := directorySession(t, s)
	stepUpDirectory(t, s, cookie, csrf)
	path := "/api/v1/admin/users/" + member.String() + "/role-grants"
	code, _, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, `"1"`, "role-audit-fail-0001",
		`{"roleCode":"RULE_AUTHOR","scopeType":"TENANT","reasonCode":"ONBOARDING"}`)
	if code != 500 {
		t.Fatalf("audit failure response: %d", code)
	}
	var grants int
	var version int64
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.access_grant WHERE tenant_id=$1 AND tenant_membership_id=$2`, s.tenantA, member).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT row_version FROM iam.tenant_membership WHERE tenant_id=$1 AND id=$2`, s.tenantA, member).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if grants != 0 || version != 1 {
		t.Fatalf("audit failure committed grant/version: grants=%d version=%d", grants, version)
	}
}

func TestDirectoryRoleReplayRequiresCurrentStepUpAndSession(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	_, member := roleTarget(t, s, "role-replay-target")
	cookie, csrf := directorySession(t, s)
	stepUpDirectory(t, s, cookie, csrf)
	path := "/api/v1/admin/users/" + member.String() + "/role-grants"
	body := `{"roleCode":"RULE_AUTHOR","scopeType":"TENANT","reasonCode":"ONBOARDING"}`
	const key = "role-session-replay-0001"
	if code, _, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, `"1"`, key, body); code != 200 {
		t.Fatalf("initial assignment: %d", code)
	}
	hash := sha256.Sum256([]byte(cookie.Value))
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.session SET step_up_until=clock_timestamp()-interval '1 second' WHERE id_hash=$1`, hash[:]); err != nil {
		t.Fatal(err)
	}
	if code, result, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, `"1"`, key, body); code != 403 || result["code"] != "STEP_UP_REQUIRED" {
		t.Fatalf("expired step-up replay: %d %v", code, result)
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.session SET revoked_at=clock_timestamp() WHERE id_hash=$1`, hash[:]); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, `"1"`, key, body); code != 401 {
		t.Fatalf("revoked-session replay: %d", code)
	}
}

func TestDirectoryRoleReplayAfterTenantSwitchIsDenied(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantB, ActorID: s.actor, RoleCode: "AUDITOR"}); err != nil {
		t.Fatal(err)
	}
	_, member := roleTarget(t, s, "role-switch-replay-target")
	cookie, csrf := directorySession(t, s)
	stepUpDirectory(t, s, cookie, csrf)
	path := "/api/v1/admin/users/" + member.String() + "/role-grants"
	body := `{"roleCode":"RULE_AUTHOR","scopeType":"TENANT","reasonCode":"ONBOARDING"}`
	const key = "role-switch-replay-0001"
	if code, out, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, `"1"`, key, body); code != 200 {
		t.Fatalf("initial assignment: %d %v", code, out)
	}
	if rec := s.do(call{method: http.MethodPost, path: "/api/v1/session/switch-tenant", cookie: cookie, csrf: csrf,
		body: `{"tenantId":"` + s.tenantB.String() + `"}`}); rec.Code != 200 {
		t.Fatalf("switch tenant: %d %s", rec.Code, rec.Body.String())
	}
	if code, out, replay := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, `"1"`, key, body); code != 403 || replay.Get("Idempotent-Replayed") == "true" {
		t.Fatalf("old-tenant replay after switch: %d %v", code, out)
	}
}

func TestDirectoryRoleConcurrentAssignAndPermissionSyncNeverBothCommit(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	_, member := roleTarget(t, s, "role-sync-race-target")
	cookie, csrf := directorySession(t, s)
	stepUpDirectory(t, s, cookie, csrf)
	tpl, _ := application.RoleTemplateByCode("RULE_AUTHOR")
	tpl.Permissions = append(append([]string{}, tpl.Permissions...), "audit.read")
	path := "/api/v1/admin/users/" + member.String() + "/role-grants"
	var assignStatus int
	var syncErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		assignStatus, _, _ = roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, `"1"`, "role-sync-race-assign-0001",
			`{"roleCode":"RULE_AUTHOR","scopeType":"TENANT","reasonCode":"ONBOARDING"}`)
	}()
	go func() {
		defer wg.Done()
		_, syncErr = identitypg.NewProvisioningRepository(s.h.App).SyncSystemRoles(ctx, s.tenantA, []application.RoleTemplate{tpl})
	}()
	wg.Wait()
	var grants, extra int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.access_grant WHERE tenant_id=$1 AND tenant_membership_id=$2`, s.tenantA, member).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.role_permission rp JOIN iam.role r ON r.tenant_id=rp.tenant_id AND r.id=rp.role_id
		WHERE r.tenant_id=$1 AND r.code='RULE_AUTHOR' AND rp.permission_code='audit.read'`, s.tenantA).Scan(&extra); err != nil {
		t.Fatal(err)
	}
	if grants+extra != 1 {
		t.Fatalf("race produced grant=%d extra-permission=%d, assign=%d sync=%v", grants, extra, assignStatus, syncErr)
	}
	if grants == 1 && (assignStatus != 200 || !errors.Is(syncErr, application.ErrRoleConfigurationInUse)) {
		t.Fatalf("grant won but sync did not refuse: %d %v", assignStatus, syncErr)
	}
	if extra == 1 && (syncErr != nil || assignStatus != 409) {
		t.Fatalf("sync won but assignment did not refuse: %d %v", assignStatus, syncErr)
	}
}

func TestDirectoryRoleConcurrentAssignAndRevokeVersionSerialization(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	_, member := roleTarget(t, s, "role-command-race-target")
	cookie, csrf := directorySession(t, s)
	stepUpDirectory(t, s, cookie, csrf)
	path := "/api/v1/admin/users/" + member.String() + "/role-grants"
	body := `{"roleCode":"RULE_AUTHOR","scopeType":"TENANT","reasonCode":"ONBOARDING"}`
	var statuses [2]int
	var wg sync.WaitGroup
	wg.Add(2)
	for i := range statuses {
		go func(i int) {
			defer wg.Done()
			statuses[i], _, _ = roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, `"1"`, "role-race-assign-"+uuid.NewString(), body)
		}(i)
	}
	wg.Wait()
	if (statuses[0] != 200 || statuses[1] != 412) && (statuses[1] != 200 || statuses[0] != 412) {
		t.Fatalf("assign race: %v", statuses)
	}
	code, page, headers := roleCall(s, cookie, csrf, s.tenantA, http.MethodGet, path, "", "", "")
	if code != 200 || len(page["items"].([]any)) != 1 {
		t.Fatalf("one grant after race: %d %v", code, page)
	}
	grantID := page["items"].([]any)[0].(map[string]any)["id"].(string)
	revokePath := path + "/" + grantID + "/revoke"
	wg.Add(2)
	for i := range statuses {
		go func(i int) {
			defer wg.Done()
			statuses[i], _, _ = roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, revokePath, headers.Get("ETag"), "role-race-revoke-"+uuid.NewString(), `{"reasonCode":"DUTY_ENDED"}`)
		}(i)
	}
	wg.Wait()
	if (statuses[0] != 200 || statuses[1] != 412) && (statuses[1] != 200 || statuses[0] != 412) {
		t.Fatalf("revoke race: %v", statuses)
	}
	var events int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM audit.event WHERE tenant_id=$1 AND resource_id=$2 AND action_code IN ('access_grant.assign','access_grant.revoke')`, s.tenantA, grantID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Fatalf("concurrent commands wrote %d audit events", events)
	}
}

func TestDirectoryRoleAuthorityRequiresBothCurrentTenantPermissions(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	_, member := roleTarget(t, s, "role-authority-target")
	rel, err := s.prov.EnsureProviderOrganization(ctx, s.tenantA, "AUTH_PROVIDER", "Authority provider")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "PROVIDER_ADMIN",
		ScopeType: application.ScopeOrganization, ScopeID: uuid.NullUUID{UUID: rel, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `INSERT INTO iam.role (tenant_id,code,name,is_system_role) VALUES ($1,'SPLIT_ROLE_TEST','Split role',false)`, s.tenantA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `INSERT INTO iam.role_permission (tenant_id,role_id,permission_code)
		SELECT tenant_id,id,'identity.role.manage' FROM iam.role WHERE tenant_id=$1 AND code='SPLIT_ROLE_TEST'`, s.tenantA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `INSERT INTO iam.access_grant (tenant_id,tenant_membership_id,role_id,scope_type)
		SELECT m.tenant_id,m.id,r.id,'TENANT' FROM iam.tenant_membership m
		JOIN iam.role r ON r.tenant_id=m.tenant_id AND r.code='SPLIT_ROLE_TEST'
		WHERE m.tenant_id=$1 AND m.actor_id=$2 AND m.valid_period @> clock_timestamp()::date`, s.tenantA, s.actor); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := directorySession(t, s)
	stepUpDirectory(t, s, cookie, csrf)
	path := "/api/v1/admin/users/" + member.String() + "/role-grants"
	for _, app := range []string{"", "backoffice", "provider"} {
		headers := map[string]string{identityhttp.TenantHeader: s.tenantA.String()}
		if app != "" {
			headers["X-Kapsora-App"] = app
		}
		rec := s.do(call{method: http.MethodGet, path: path, cookie: cookie, headers: headers})
		if rec.Code != 403 {
			t.Fatalf("split authority app %q read: %d", app, rec.Code)
		}
	}
	me := decodeBody(t, s.do(call{method: http.MethodGet, path: "/api/v1/me", cookie: cookie}))
	if me["tenants"].([]any)[0].(map[string]any)["canManageTenantRoles"] != false {
		t.Fatal("split grant created tenant role capability")
	}
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodGet, path, "", "", ""); code != 200 {
		t.Fatalf("valid tenant authority: %d", code)
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.access_grant SET valid_period=tstzrange(clock_timestamp()-interval '2 days',clock_timestamp()-interval '1 day','[)')
		WHERE tenant_id=$1 AND tenant_membership_id=(SELECT id FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2)
		AND role_id=(SELECT id FROM iam.role WHERE tenant_id=$1 AND code='TENANT_ADMIN')`, s.tenantA, s.actor); err != nil {
		t.Fatal(err)
	}
	if code, body, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodGet, path, "", "", ""); code != 403 || body["code"] != "PERMISSION_DENIED" {
		t.Fatalf("expired TENANT authority: %d %v", code, body)
	}
}

func TestDirectoryRoleCandidateDriftSensitivityAndSelfProtection(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	_, member := roleTarget(t, s, "role-drift-target")
	cookie, csrf := directorySession(t, s)
	stepUpDirectory(t, s, cookie, csrf)
	path := "/api/v1/admin/users/" + member.String() + "/role-grants"
	code, opts, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodGet, "/api/v1/admin/role-assignment-options", "", "", "")
	if code != 200 || len(opts["items"].([]any)) != 11 {
		t.Fatalf("supported catalog: %d %v", code, opts)
	}
	var ownMember uuid.UUID
	var ownVersion int64
	if err := s.h.Admin.QueryRow(ctx, `SELECT id,row_version FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2`, s.tenantA, s.actor).Scan(&ownMember, &ownVersion); err != nil {
		t.Fatal(err)
	}
	if code, body, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, "/api/v1/admin/users/"+ownMember.String()+"/role-grants", `"`+versionString(ownVersion)+`"`, "role-self-assign-0001",
		`{"roleCode":"RULE_AUTHOR","scopeType":"TENANT","reasonCode":"ONBOARDING"}`); code != 409 || body["code"] != "SELF_ROLE_CHANGE_FORBIDDEN" {
		t.Fatalf("self assignment: %d %v", code, body)
	}
	for _, role := range []string{"TENANT_ADMIN", "MEMBER", "PLAN_PUBLISHER", "RULE_APPROVER", "UNKNOWN_ROLE"} {
		code, body, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, `"1"`, "role-unsupported-"+uuid.NewString(),
			`{"roleCode":"`+role+`","scopeType":"TENANT","reasonCode":"ONBOARDING"}`)
		if code != 409 || body["code"] != "ROLE_ASSIGNMENT_UNSUPPORTED" {
			t.Fatalf("unsupported %s: %d %v", role, code, body)
		}
	}
	if _, err := s.h.Admin.Exec(ctx, `DELETE FROM iam.role_permission rp USING iam.role r WHERE rp.tenant_id=r.tenant_id AND rp.role_id=r.id
		AND r.tenant_id=$1 AND r.code='RULE_AUTHOR' AND rp.permission_code='rule.read'`, s.tenantA); err != nil {
		t.Fatal(err)
	}
	assign := func() (int, map[string]any) {
		status, result, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, `"1"`, "role-drift-"+uuid.NewString(),
			`{"roleCode":"RULE_AUTHOR","scopeType":"TENANT","reasonCode":"ONBOARDING"}`)
		return status, result
	}
	if code, body := assign(); code != 409 || body["code"] != "ROLE_CONFIGURATION_UNSUPPORTED" {
		t.Fatalf("missing permission drift: %d %v", code, body)
	}
	if _, err := s.h.Admin.Exec(ctx, `INSERT INTO iam.role_permission (tenant_id,role_id,permission_code)
		SELECT tenant_id,id,'rule.read' FROM iam.role WHERE tenant_id=$1 AND code='RULE_AUTHOR'`, s.tenantA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `INSERT INTO iam.role_permission (tenant_id,role_id,permission_code)
		SELECT tenant_id,id,'audit.read' FROM iam.role WHERE tenant_id=$1 AND code='RULE_AUTHOR'`, s.tenantA); err != nil {
		t.Fatal(err)
	}
	if code, body := assign(); code != 409 || body["code"] != "ROLE_CONFIGURATION_UNSUPPORTED" {
		t.Fatalf("extra permission drift: %d %v", code, body)
	}
	if _, err := s.h.Admin.Exec(ctx, `DELETE FROM iam.role_permission rp USING iam.role r WHERE rp.tenant_id=r.tenant_id AND rp.role_id=r.id
		AND r.tenant_id=$1 AND r.code='RULE_AUTHOR' AND rp.permission_code='audit.read'`, s.tenantA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.permission SET sensitivity='SENSITIVE' WHERE code='rule.read'`); err != nil {
		t.Fatal(err)
	}
	code, opts, _ = roleCall(s, cookie, csrf, s.tenantA, http.MethodGet, "/api/v1/admin/role-assignment-options", "", "", "")
	foundSensitive := false
	if code == 200 {
		for _, raw := range opts["items"].([]any) {
			item := raw.(map[string]any)
			if item["code"] == "RULE_AUTHOR" && item["hasSensitivePermissions"] == true {
				foundSensitive = true
			}
		}
	}
	if !foundSensitive {
		t.Fatalf("sensitive catalog role missing: %d %v", code, opts)
	}
	_, sensitiveMember := roleTarget(t, s, "role-sensitive-target")
	if code, out, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost,
		"/api/v1/admin/users/"+sensitiveMember.String()+"/role-grants", `"1"`, "role-sensitive-assign-0001",
		`{"roleCode":"RULE_AUTHOR","scopeType":"TENANT","reasonCode":"ONBOARDING"}`); code != 200 {
		t.Fatalf("sensitive permission assignment: %d %v", code, out)
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.permission SET sensitivity='PRIVILEGED' WHERE code='rule.read'`); err != nil {
		t.Fatal(err)
	}
	if code, body := assign(); code != 409 || body["code"] != "ROLE_CONFIGURATION_UNSUPPORTED" {
		t.Fatalf("privileged sensitivity: %d %v", code, body)
	}
}

func TestDirectoryRoleEachSupportedTemplateAssignsToZeroGrantTarget(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	rel, err := s.prov.EnsureProviderOrganization(ctx, s.tenantA, "ROLE_ALL_PROVIDER", "Role all provider")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `INSERT INTO provider.provider_profile (tenant_id,tenant_organization_id,provider_type,status)
		VALUES ($1,$2,'HOSPITAL','ACTIVE')`, s.tenantA, rel); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := directorySession(t, s)
	stepUpDirectory(t, s, cookie, csrf)
	for _, code := range []string{"PROGRAM_MANAGER", "CONTRACT_MANAGER", "RULE_AUTHOR", "MEDICAL_REVIEWER", "FINANCIAL_REVIEWER", "AUDITOR", "SPONSOR_HR",
		"PROVIDER_ADMIN", "PROVIDER_STAFF", "PROVIDER_BILLING", "PROVIDER_RESERVATION"} {
		t.Run(code, func(t *testing.T) {
			_, member := roleTarget(t, s, "role-all-"+strings.ToLower(code))
			scope, _ := application.SupportedRoleScope(code)
			body := `{"roleCode":"` + code + `","scopeType":"` + scope + `","reasonCode":"ONBOARDING"`
			if scope == application.ScopeOrganization {
				body += `,"organizationRelationshipId":"` + rel.String() + `"`
			}
			body += `}`
			path := "/api/v1/admin/users/" + member.String() + "/role-grants"
			status, assigned, headers := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, `"1"`, "role-all-"+uuid.NewString(), body)
			if status != 200 || headers.Get("ETag") == `"1"` || assigned["grant"].(map[string]any)["roleCode"] != code {
				t.Fatalf("supported role assignment: %d %v", status, assigned)
			}
		})
	}
}

func TestDirectoryRoleHistoricalSameActorMembershipSelfProtection(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	var currentMember, historicalMember uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.tenant_membership
		WHERE tenant_id=$1 AND actor_id=$2 AND valid_period @> clock_timestamp()::date`, s.tenantA, s.actor).Scan(&currentMember); err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.tenant_membership
		SET valid_period=daterange(clock_timestamp()::date-1,NULL,'[)') WHERE tenant_id=$1 AND id=$2`, s.tenantA, currentMember); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO iam.tenant_membership (tenant_id,actor_id,membership_status,valid_period)
		VALUES ($1,$2,'ACTIVE',daterange(clock_timestamp()::date-3,clock_timestamp()::date-1,'[)')) RETURNING id`, s.tenantA, s.actor).Scan(&historicalMember); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := directorySession(t, s)
	stepUpDirectory(t, s, cookie, csrf)
	path := "/api/v1/admin/users/" + historicalMember.String() + "/role-grants"
	status, page, headers := roleCall(s, cookie, csrf, s.tenantA, http.MethodGet, path, "", "", "")
	if status != 200 || page["assignmentRefusalCode"] != "SELF_ROLE_CHANGE_FORBIDDEN" || headers.Get("ETag") != `"1"` {
		t.Fatalf("historical self page: %d %v", status, page)
	}
	var auditBefore int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM audit.event WHERE tenant_id=$1
		AND action_code IN ('access_grant.assign','access_grant.revoke')`, s.tenantA).Scan(&auditBefore); err != nil {
		t.Fatal(err)
	}
	if status, out, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, `"1"`, "role-history-self-assign-0001",
		`{"roleCode":"RULE_AUTHOR","scopeType":"TENANT","reasonCode":"ONBOARDING"}`); status != 409 || out["code"] != "SELF_ROLE_CHANGE_FORBIDDEN" {
		t.Fatalf("historical self assign: %d %v", status, out)
	}
	var grantID uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO iam.access_grant (tenant_id,tenant_membership_id,role_id,scope_type)
		SELECT $1,$2,id,'TENANT' FROM iam.role WHERE tenant_id=$1 AND code='RULE_AUTHOR' RETURNING id`, s.tenantA, historicalMember).Scan(&grantID); err != nil {
		t.Fatal(err)
	}
	status, page, _ = roleCall(s, cookie, csrf, s.tenantA, http.MethodGet, path, "", "", "")
	if status != 200 || page["items"].([]any)[0].(map[string]any)["revocationRefusalCode"] != "SELF_ROLE_CHANGE_FORBIDDEN" {
		t.Fatalf("historical self revoke aid: %d %v", status, page)
	}
	if status, out, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path+"/"+grantID.String()+"/revoke", `"1"`, "role-history-self-revoke-0001",
		`{"reasonCode":"ACCESS_REVIEW"}`); status != 409 || out["code"] != "SELF_ROLE_CHANGE_FORBIDDEN" {
		t.Fatalf("historical self revoke: %d %v", status, out)
	}
	var version int64
	var grants, audits int
	if err := s.h.Admin.QueryRow(ctx, `SELECT row_version FROM iam.tenant_membership WHERE tenant_id=$1 AND id=$2`, s.tenantA, historicalMember).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.access_grant WHERE tenant_id=$1 AND tenant_membership_id=$2
		AND valid_period @> clock_timestamp()`, s.tenantA, historicalMember).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM audit.event WHERE tenant_id=$1
		AND action_code IN ('access_grant.assign','access_grant.revoke')`, s.tenantA).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if version != 1 || grants != 1 || audits != auditBefore {
		t.Fatalf("historical self commands mutated aggregate: version=%d grants=%d audits=%d before=%d", version, grants, audits, auditBefore)
	}
}

func TestDirectoryRoleHistoricalPersonStaysPrivateAndBlocksAssignment(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	_, member := roleTarget(t, s, "role-person-history-target")
	var person, memberRole uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO party.person (tenant_id,first_name,last_name,normalized_name)
		VALUES ($1,'Synthetic','Person','synthetic person') RETURNING id`, s.tenantA).Scan(&person); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.role WHERE tenant_id=$1 AND code='MEMBER'`, s.tenantA).Scan(&memberRole); err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `INSERT INTO iam.access_grant (tenant_id,tenant_membership_id,role_id,scope_type,scope_id,valid_period)
		VALUES ($1,$2,$3,'PERSON',$4,tstzrange(clock_timestamp()-interval '2 days',clock_timestamp()-interval '1 day','[)'))`, s.tenantA, member, memberRole, person); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := directorySession(t, s)
	stepUpDirectory(t, s, cookie, csrf)
	path := "/api/v1/admin/users/" + member.String() + "/role-grants"
	code, page, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodGet, path, "", "", "")
	if code != 200 || page["canAssign"] != false || page["assignmentRefusalCode"] != "EXISTING_ACCESS_CONFLICT" {
		t.Fatalf("historical PERSON eligibility: %d %v", code, page)
	}
	grant := page["items"].([]any)[0].(map[string]any)
	if grant["scopeType"] != "PERSON" || grant["organizationRelationshipId"] != nil || grant["organizationDisplayName"] != nil {
		t.Fatalf("PERSON history projection: %v", grant)
	}
	encoded, _ := json.Marshal(page)
	if contains := string(encoded); len(contains) == 0 || strings.Contains(contains, person.String()) {
		t.Fatal("PERSON scope identifier leaked")
	}
	if code, body, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, `"1"`, "role-historical-person-0001",
		`{"roleCode":"RULE_AUTHOR","scopeType":"TENANT","reasonCode":"ONBOARDING"}`); code != 409 || body["code"] != "EXISTING_ACCESS_CONFLICT" {
		t.Fatalf("historical PERSON assignment: %d %v", code, body)
	}
}

func TestDirectoryRoleAssignSuspendRaceUsesMembershipAggregate(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	_, member := roleTarget(t, s, "role-suspend-race-target")
	cookie, csrf := directorySession(t, s)
	stepUpDirectory(t, s, cookie, csrf)
	path := "/api/v1/admin/users/" + member.String() + "/role-grants"
	var assignStatus, suspendStatus int
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		assignStatus, _, _ = roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, `"1"`, "role-suspend-assign-0001",
			`{"roleCode":"RULE_AUTHOR","scopeType":"TENANT","reasonCode":"ONBOARDING"}`)
	}()
	go func() {
		defer wg.Done()
		suspendStatus, _, _ = suspendCall(s, cookie, csrf, s.tenantA, member, `"1"`, "role-suspend-command-0001", "ACCESS_REVIEW")
	}()
	wg.Wait()
	if assignStatus == 200 {
		if suspendStatus != 412 {
			t.Fatalf("suspend after assignment: %d", suspendStatus)
		}
	} else if suspendStatus == 200 {
		if assignStatus != 409 && assignStatus != 412 {
			t.Fatalf("assignment after suspension: %d", assignStatus)
		}
	} else {
		t.Fatalf("neither command succeeded: assign=%d suspend=%d", assignStatus, suspendStatus)
	}
	var grants int
	var status string
	var version int64
	if err := s.h.Admin.QueryRow(ctx, `SELECT membership_status,row_version FROM iam.tenant_membership WHERE tenant_id=$1 AND id=$2`, s.tenantA, member).Scan(&status, &version); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.access_grant WHERE tenant_id=$1 AND tenant_membership_id=$2`, s.tenantA, member).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if version != 2 || (assignStatus == 200 && (grants != 1 || status != "ACTIVE")) || (suspendStatus == 200 && (grants != 0 || status != "SUSPENDED")) {
		t.Fatalf("aggregate race state: status=%s version=%d grants=%d", status, version, grants)
	}
}
