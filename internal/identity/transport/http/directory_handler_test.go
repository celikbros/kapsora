package identityhttp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/identity/application"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
)

func directorySession(t *testing.T, s *authzServer) (*http.Cookie, string) {
	t.Helper()
	cookie, csrf := s.login(t)
	rec := s.do(call{method: http.MethodPost, path: "/api/v1/session/switch-tenant", cookie: cookie, csrf: csrf,
		body: `{"tenantId":"` + s.tenantA.String() + `"}`})
	if rec.Code != http.StatusOK {
		t.Fatalf("switch tenant: %d", rec.Code)
	}
	return cookie, csrf
}

func directoryCall(s *authzServer, cookie *http.Cookie, tenant uuid.UUID, path, app string) (int, map[string]any) {
	headers := map[string]string{identityhttp.TenantHeader: tenant.String()}
	if app != "" {
		headers[identity.AppHeader] = app
	}
	rec := s.do(call{method: http.MethodGet, path: path, cookie: cookie, headers: headers})
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func TestDirectoryPermissionCorrelatesActiveTenantGrantWithReadPermission(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	hospital, err := s.prov.EnsureProviderOrganization(ctx, s.tenantA, "HOSP", "Test provider")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{
		TenantID: s.tenantA, ActorID: s.actor, RoleCode: "PROVIDER_ADMIN",
		ScopeType: application.ScopeOrganization, ScopeID: uuid.NullUUID{UUID: hospital, Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	cookie, _ := directorySession(t, s)
	for _, app := range []string{"", "backoffice", "provider"} {
		code, body := directoryCall(s, cookie, s.tenantA, "/api/v1/admin/users", app)
		if code != http.StatusForbidden || body["code"] != "PERMISSION_DENIED" {
			t.Fatalf("organization-only directory access with app %q: %d %v", app, code, body["code"])
		}
	}
	me := s.do(call{method: http.MethodGet, path: "/api/v1/me", cookie: cookie})
	var contextView struct {
		Tenants []struct {
			CanReadTenantUsers bool `json:"canReadTenantUsers"`
		} `json:"tenants"`
	}
	if err := json.Unmarshal(me.Body.Bytes(), &contextView); err != nil {
		t.Fatal(err)
	}
	if len(contextView.Tenants) != 1 || contextView.Tenants[0].CanReadTenantUsers {
		t.Fatal("organization grant became tenant directory capability")
	}

	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	if code, _ := directoryCall(s, cookie, s.tenantA, "/api/v1/admin/users", "backoffice"); code != http.StatusOK {
		t.Fatalf("tenant admin list: %d", code)
	}
	if code, body := directoryCall(s, cookie, s.tenantA, "/api/v1/admin/users", "provider"); code != http.StatusForbidden || body["code"] != "PERMISSION_DENIED" {
		t.Fatal("provider app opened tenant directory")
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.access_grant SET valid_period = tstzrange(clock_timestamp()-interval '2 days', clock_timestamp()-interval '1 day', '[)')
 WHERE tenant_id = $1 AND tenant_membership_id = (SELECT id FROM iam.tenant_membership WHERE tenant_id = $1 AND actor_id = $2)
   AND scope_type = 'TENANT' AND role_id = (SELECT id FROM iam.role WHERE tenant_id = $1 AND code = 'TENANT_ADMIN')`, s.tenantA, s.actor); err != nil {
		t.Fatal(err)
	}
	if code, body := directoryCall(s, cookie, s.tenantA, "/api/v1/admin/users", ""); code != http.StatusForbidden || body["code"] != "PERMISSION_DENIED" {
		t.Fatal("expired tenant grant authorized directory")
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.access_grant SET valid_period = tstzrange(clock_timestamp()+interval '1 day', NULL, '[)')
 WHERE tenant_id = $1 AND tenant_membership_id = (SELECT id FROM iam.tenant_membership WHERE tenant_id = $1 AND actor_id = $2)
   AND scope_type = 'TENANT' AND role_id = (SELECT id FROM iam.role WHERE tenant_id = $1 AND code = 'TENANT_ADMIN')`, s.tenantA, s.actor); err != nil {
		t.Fatal(err)
	}
	if code, body := directoryCall(s, cookie, s.tenantA, "/api/v1/admin/users", ""); code != http.StatusForbidden || body["code"] != "PERMISSION_DENIED" {
		t.Fatal("future tenant grant authorized directory")
	}
}

func TestDirectoryTenantIsolationPagingAndResponseAllowlist(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	for _, tenant := range []uuid.UUID{s.tenantA, s.tenantB} {
		if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: tenant, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantB, ActorID: s.actor, RoleCode: "FINANCIAL_REVIEWER"}); err != nil {
		t.Fatal(err)
	}
	other := s.h.CreateActor("other", "Other member")
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantB, ActorID: other, RoleCode: "AUDITOR"}); err != nil {
		t.Fatal(err)
	}
	otherA := s.h.CreateActor("other-a", "Other A member")
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: otherA, RoleCode: "AUDITOR"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.tenant_membership SET created_at = '2026-01-01T00:00:00Z' WHERE tenant_id = $1`, s.tenantA); err != nil {
		t.Fatal(err)
	}
	var foreignID uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2`, s.tenantB, other).Scan(&foreignID); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := directorySession(t, s)
	path := "/api/v1/admin/users/" + foreignID.String()
	if code, body := directoryCall(s, cookie, s.tenantA, path, "backoffice"); code != http.StatusNotFound || body["code"] != "MEMBERSHIP_NOT_FOUND" {
		t.Fatal("foreign membership exposed")
	}
	if code, body := directoryCall(s, cookie, s.tenantB, "/api/v1/admin/users", "backoffice"); code != http.StatusForbidden || body["code"] != "TENANT_MISMATCH" {
		t.Fatal("tenant header switched context")
	}

	code, first := directoryCall(s, cookie, s.tenantA, "/api/v1/admin/users?limit=1", "backoffice")
	if code != http.StatusOK {
		t.Fatalf("list: %d", code)
	}
	items, ok := first["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatal("first membership page missing")
	}
	firstCursor, ok := first["nextCursor"].(string)
	if !ok || firstCursor == "" {
		t.Fatal("keyset next cursor missing")
	}
	code, second := directoryCall(s, cookie, s.tenantA, "/api/v1/admin/users?limit=1&cursor="+firstCursor, "backoffice")
	if code != http.StatusOK {
		t.Fatalf("second page: %d", code)
	}
	secondItems := second["items"].([]any)
	if len(secondItems) != 1 || secondItems[0].(map[string]any)["id"] == items[0].(map[string]any)["id"] {
		t.Fatal("keyset page repeated a membership")
	}
	assertKeys(t, items[0], []string{"actorStatus", "actorType", "displayName", "id", "membershipStatus", "validFrom", "validTo", "validityEmpty"})
	member := items[0].(map[string]any)
	code, detail := directoryCall(s, cookie, s.tenantA, "/api/v1/admin/users/"+member["id"].(string), "backoffice")
	if code != http.StatusOK {
		t.Fatalf("detail: %d", code)
	}
	assertKeys(t, detail, []string{"assignedRoles", "membership"})
	assertKeys(t, detail["membership"], []string{"actorStatus", "actorType", "displayName", "id", "membershipStatus", "validFrom", "validTo", "validityEmpty"})
	roles := detail["assignedRoles"].([]any)
	if len(roles) == 0 {
		t.Fatal("assigned roles missing")
	}
	assertKeys(t, roles[0], []string{"code", "isSystemRole", "name", "scopeType", "validFrom", "validTo", "validityEmpty"})
	for _, role := range roles {
		if role.(map[string]any)["code"] == "FINANCIAL_REVIEWER" {
			t.Fatal("tenant B role leaked into tenant A")
		}
	}
	var otherAID uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2`, s.tenantA, otherA).Scan(&otherAID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.tenant_membership SET valid_period = daterange(NULL,NULL,'()') WHERE tenant_id=$1 AND id=$2`, s.tenantA, otherAID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.access_grant SET valid_period = tstzrange(NULL,NULL,'()') WHERE tenant_id=$1 AND tenant_membership_id=$2`, s.tenantA, otherAID); err != nil {
		t.Fatal(err)
	}
	if code, body := directoryCall(s, cookie, s.tenantA, "/api/v1/admin/users/"+otherAID.String(), "backoffice"); code != http.StatusOK || body["membership"].(map[string]any)["validFrom"] != nil || body["membership"].(map[string]any)["validityEmpty"] != false || body["assignedRoles"].([]any)[0].(map[string]any)["validFrom"] != nil {
		t.Fatal("unbounded period projected as finite or empty")
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.tenant_membership SET valid_period = 'empty'::daterange WHERE tenant_id=$1 AND id=$2`, s.tenantA, otherAID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.access_grant SET valid_period = 'empty'::tstzrange WHERE tenant_id=$1 AND tenant_membership_id=$2`, s.tenantA, otherAID); err != nil {
		t.Fatal(err)
	}
	if code, body := directoryCall(s, cookie, s.tenantA, "/api/v1/admin/users/"+otherAID.String(), "backoffice"); code != http.StatusOK || body["membership"].(map[string]any)["validityEmpty"] != true || body["assignedRoles"].([]any)[0].(map[string]any)["validityEmpty"] != true {
		t.Fatal("empty period not distinguished from unbounded")
	}
	if code, body := directoryCall(s, cookie, s.tenantA, "/api/v1/admin/users?status=BOGUS", "backoffice"); code != http.StatusBadRequest || body["code"] != "DIRECTORY_QUERY_INVALID" {
		t.Fatal("invalid status accepted")
	}
	if code, body := directoryCall(s, cookie, s.tenantA, "/api/v1/admin/users?cursor=forged", "backoffice"); code != http.StatusBadRequest || body["code"] != "DIRECTORY_QUERY_INVALID" {
		t.Fatal("forged cursor accepted")
	}
	if code, body := directoryCall(s, cookie, s.tenantA, "/api/v1/admin/users?status=REVOKED", "backoffice"); code != http.StatusOK || len(body["items"].([]any)) != 0 {
		t.Fatal("membership status filter failed")
	}
	if rec := s.do(call{method: http.MethodPost, path: "/api/v1/session/switch-tenant", cookie: cookie, csrf: csrf,
		body: `{"tenantId":"` + s.tenantB.String() + `"}`}); rec.Code != http.StatusOK {
		t.Fatal("second tenant switch failed")
	}
	if code, body := directoryCall(s, cookie, s.tenantB, "/api/v1/admin/users", "backoffice"); code != http.StatusOK || len(body["items"].([]any)) != 2 {
		t.Fatal("second tenant membership page wrong")
	}
	var ownB uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2`, s.tenantB, s.actor).Scan(&ownB); err != nil {
		t.Fatal(err)
	}
	if code, body := directoryCall(s, cookie, s.tenantB, "/api/v1/admin/users/"+ownB.String(), "backoffice"); code != http.StatusOK {
		t.Fatal("tenant B own detail failed")
	} else {
		found := false
		for _, role := range body["assignedRoles"].([]any) {
			if role.(map[string]any)["code"] == "FINANCIAL_REVIEWER" {
				found = true
			}
		}
		if !found {
			t.Fatal("tenant B assigned role missing")
		}
	}
	if code, body := directoryCall(s, cookie, s.tenantB, "/api/v1/admin/users/"+member["id"].(string), "backoffice"); code != http.StatusNotFound || body["code"] != "MEMBERSHIP_NOT_FOUND" {
		t.Fatal("prior tenant membership leaked after switch")
	}
}

func assertKeys(t *testing.T, raw any, want []string) {
	t.Helper()
	value, ok := raw.(map[string]any)
	if !ok {
		t.Fatal("expected JSON object")
	}
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("response fields = %v", keys)
	}
}
