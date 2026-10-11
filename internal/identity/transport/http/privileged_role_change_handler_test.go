package identityhttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity/application"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
)

func roleChangeManagers(t *testing.T, s *authzServer) (*http.Cookie, string, *http.Cookie, string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	checker, err := s.svc.CreateAccount(ctx, "role-change-checker", "Role change checker", "role-change-checker", testPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: checker, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	makerCookie, makerCSRF := directorySession(t, s)
	stepUpDirectory(t, s, makerCookie, makerCSRF)
	login := s.do(call{method: http.MethodPost, path: "/api/v1/session/login", body: `{"username":"role-change-checker","password":"` + testPassword + `"}`})
	if login.Code != 200 {
		t.Fatalf("checker login: %d %s", login.Code, login.Body.String())
	}
	checkerCookie := sessionCookie(t, login, s.cookies.Name())
	checkerCSRF, _ := decodeBody(t, login)["csrfToken"].(string)
	switchRec := s.do(call{method: http.MethodPost, path: "/api/v1/session/switch-tenant", cookie: checkerCookie, csrf: checkerCSRF, body: `{"tenantId":"` + s.tenantA.String() + `"}`})
	if switchRec.Code != 200 {
		t.Fatalf("checker tenant switch: %d %s", switchRec.Code, switchRec.Body.String())
	}
	stepUpDirectory(t, s, checkerCookie, checkerCSRF)
	return makerCookie, makerCSRF, checkerCookie, checkerCSRF
}

func roleChangeOption(t *testing.T, s *authzServer, cookie *http.Cookie, csrf, role string) string {
	t.Helper()
	code, out, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodGet, "/api/v1/admin/privileged-role-assignment-options", "", "", "")
	if code != 200 {
		t.Fatalf("B options: %d %v", code, out)
	}
	for _, v := range out["items"].([]any) {
		item := v.(map[string]any)
		if item["code"] == role {
			if item["scopeType"] != "TENANT" || item["requiresApproval"] != true {
				t.Fatalf("B option shape: %v", item)
			}
			return item["configurationHash"].(string)
		}
	}
	t.Fatalf("B option %s absent: %v", role, out)
	return ""
}

func TestDirectoryRoleChangeAssignApproveRevokeReceiptAndSeparation(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	makerCookie, makerCSRF, checkerCookie, checkerCSRF := roleChangeManagers(t, s)
	_, target := roleTarget(t, s, "role-change-target")
	hash := roleChangeOption(t, s, makerCookie, makerCSRF, "TENANT_ADMIN")
	eligibilityPath := "/api/v1/admin/users/" + target.String() + "/role-change-eligibility"
	code, eligible, headers := roleCall(s, makerCookie, makerCSRF, s.tenantA, http.MethodGet, eligibilityPath, "", "", "")
	if code != 200 || eligible["canRequestAssignment"] != true || eligible["checkerAvailability"] != "AVAILABLE" || headers.Get("ETag") == "" {
		t.Fatalf("initial B eligibility: %d %v", code, eligible)
	}
	createPath := "/api/v1/admin/users/" + target.String() + "/role-change-requests"
	assignBody := `{"operation":"ASSIGN","roleCode":"TENANT_ADMIN","configurationHash":"` + hash + `","reasonCode":"ONBOARDING"}`
	initialTag := headers.Get("ETag")
	code, created, createHeaders := roleCall(s, makerCookie, makerCSRF, s.tenantA, http.MethodPost, createPath, initialTag, "role-change-create-assign-0001", assignBody)
	if code != 201 || created["request"].(map[string]any)["status"] != "PENDING" || created["membershipRowVersion"] != nil || created["appliedGrant"] != nil {
		t.Fatalf("create B assignment: %d %v", code, created)
	}
	requestID := created["request"].(map[string]any)["id"].(string)
	if code, _, current := roleCall(s, makerCookie, makerCSRF, s.tenantA, http.MethodGet, eligibilityPath, "", "", ""); code != 200 || current.Get("ETag") != initialTag {
		t.Fatal("submission touched target aggregate")
	}
	decisionPath := "/api/v1/admin/role-change-requests/" + requestID + "/approve"
	if code, out, _ := roleCall(s, makerCookie, makerCSRF, s.tenantA, http.MethodPost, decisionPath, createHeaders.Get("ETag"), "role-change-self-check-0001", `{}`); code != 403 || out["code"] != "MAKER_CHECKER_SAME_ACTOR" {
		t.Fatalf("maker approved own request: %d %v", code, out)
	}
	var denied int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM audit.event WHERE tenant_id=$1 AND resource_id=$2 AND action_code='role_change_request.approve' AND outcome='DENIED' AND reason_code='MAKER_CHECKER_SAME_ACTOR'`, s.tenantA, uuid.MustParse(requestID)).Scan(&denied); err != nil {
		t.Fatal(err)
	}
	if denied != 1 {
		t.Fatalf("same-actor denial audits: %d", denied)
	}
	approveKey := "role-change-approve-0001"
	code, approved, approveHeaders := roleCall(s, checkerCookie, checkerCSRF, s.tenantA, http.MethodPost, decisionPath, createHeaders.Get("ETag"), approveKey, `{}`)
	if code != 200 || approved["request"].(map[string]any)["status"] != "APPROVED" || approved["appliedGrant"].(map[string]any)["roleCode"] != "TENANT_ADMIN" || approved["membershipRowVersion"] == nil {
		t.Fatalf("approve B assignment: %d %v", code, approved)
	}
	grantID := approved["appliedGrant"].(map[string]any)["id"].(string)
	if _, ok := approved["appliedGrant"].(map[string]any)["canRevoke"]; ok {
		t.Fatal("durable applied grant leaked A action flag")
	}
	// Erase only the generic middleware record: the business receipt must still
	// recover the original bytes, ETag and status without a second grant or audit.
	var checkerActor uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.actor WHERE identity_subject='role-change-checker'`).Scan(&checkerActor); err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `DELETE FROM system.idempotency_record WHERE tenant_id=$1 AND actor_id=$2 AND command_code='role_change.approve' AND idempotency_key=$3`, s.tenantA, checkerActor, approveKey); err != nil {
		t.Fatal(err)
	}
	code, replayed, replayHeaders := roleCall(s, checkerCookie, checkerCSRF, s.tenantA, http.MethodPost, decisionPath, createHeaders.Get("ETag"), approveKey, `{}`)
	if code != 200 || replayHeaders.Get("ETag") != approveHeaders.Get("ETag") || replayHeaders.Get("Idempotent-Replayed") != "true" || replayed["request"].(map[string]any)["id"] != requestID {
		t.Fatalf("durable approval replay: %d %v", code, replayed)
	}
	if code, out, _ := roleCall(s, checkerCookie, checkerCSRF, s.tenantA, http.MethodPost, decisionPath, `"999"`, approveKey, `{}`); code != 409 || out["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("changed ETag replay: %d %v", code, out)
	}
	newTargetTag := fmt.Sprintf(`"%.0f"`, approved["membershipRowVersion"].(float64))
	revokeBody := `{"operation":"REVOKE","grantId":"` + grantID + `","configurationHash":"` + hash + `","reasonCode":"DUTY_ENDED"}`
	code, revokeRequest, revokeHeaders := roleCall(s, makerCookie, makerCSRF, s.tenantA, http.MethodPost, createPath, newTargetTag, "role-change-create-revoke-0001", revokeBody)
	if code != 201 || revokeRequest["request"].(map[string]any)["operation"] != "REVOKE" {
		t.Fatalf("create B revoke: %d %v", code, revokeRequest)
	}
	revokeID := revokeRequest["request"].(map[string]any)["id"].(string)
	code, revoked, _ := roleCall(s, checkerCookie, checkerCSRF, s.tenantA, http.MethodPost, "/api/v1/admin/role-change-requests/"+revokeID+"/approve", revokeHeaders.Get("ETag"), "role-change-approve-revoke-0001", `{}`)
	if code != 200 || revoked["request"].(map[string]any)["status"] != "APPROVED" || revoked["appliedGrant"].(map[string]any)["validTo"] == nil {
		t.Fatalf("approve B revoke: %d %v", code, revoked)
	}
	var requests, grants int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.role_change_request WHERE tenant_id=$1 AND target_membership_id=$2`, s.tenantA, target).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.access_grant WHERE tenant_id=$1 AND tenant_membership_id=$2`, s.tenantA, target).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || grants != 1 {
		t.Fatalf("B effects duplicated: requests=%d grants=%d", requests, grants)
	}
	var detailText, reason string
	if err := s.h.Admin.QueryRow(ctx, `SELECT detail_json::text,reason_code FROM audit.event WHERE tenant_id=$1 AND resource_id=$2 AND action_code='role_change_request.approve' ORDER BY occurred_at DESC LIMIT 1`, s.tenantA, uuid.MustParse(revokeID)).Scan(&detailText, &reason); err != nil {
		t.Fatal(err)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(detailText), &detail); err != nil {
		t.Fatal(err)
	}
	if reason != "DUTY_ENDED" || detail["applied_validity_from"] == nil || detail["applied_validity_to"] == nil || detail["after_target_version"] == nil || detail["applied_grant_id"] != grantID {
		t.Fatalf("approved revoke audit lost safe evidence: reason=%q detail=%v", reason, detail)
	}
}

func TestDirectoryRoleChangePendingWithoutCheckerAndStrictPreflight(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	_, target := roleTarget(t, s, "role-change-no-checker-target")
	cookie, csrf := directorySession(t, s)
	stepUpDirectory(t, s, cookie, csrf)
	hash := roleChangeOption(t, s, cookie, csrf, "PLAN_PUBLISHER")
	eligibilityPath := "/api/v1/admin/users/" + target.String() + "/role-change-eligibility"
	code, page, headers := roleCall(s, cookie, csrf, s.tenantA, http.MethodGet, eligibilityPath, "", "", "")
	if code != 200 || page["checkerAvailability"] != "NO_ELIGIBLE_CHECKER" || page["canRequestAssignment"] != true {
		t.Fatalf("no checker eligibility: %d %v", code, page)
	}
	path := "/api/v1/admin/users/" + target.String() + "/role-change-requests"
	valid := `{"operation":"ASSIGN","roleCode":"PLAN_PUBLISHER","configurationHash":"` + hash + `","reasonCode":"ONBOARDING"}`
	for i, body := range []string{`null`, `{}`, valid + `{}`, `{"operation":"ASSIGN","roleCode":"PLAN_PUBLISHER","configurationHash":"` + hash + `","reasonCode":"ONBOARDING","scopeType":"TENANT"}`, `{"operation":"ASSIGN","roleCode":"PLAN_PUBLISHER","configurationHash":null,"reasonCode":"ONBOARDING"}`, `{"operation":"ASSIGN","operation":"REVOKE","roleCode":"PLAN_PUBLISHER","configurationHash":"` + hash + `","reasonCode":"ONBOARDING"}`} {
		code, out, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, headers.Get("ETag"), "role-change-bad-body-"+uuid.NewString(), body)
		if code != 400 || out["code"] != "INVALID_REQUEST_BODY" {
			t.Fatalf("bad body %d: %d %v", i, code, out)
		}
	}
	textRec := s.do(call{method: http.MethodPost, path: path, cookie: cookie, csrf: csrf, headers: map[string]string{identityhttp.TenantHeader: s.tenantA.String(), "If-Match": headers.Get("ETag"), "Idempotency-Key": "role-change-text-media-0001", "Content-Type": "text/plain"}, body: valid})
	if textRec.Code != 415 {
		t.Fatalf("media type replay gate: %d %s", textRec.Code, textRec.Body.String())
	}
	code, created, createdHeaders := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, headers.Get("ETag"), "role-change-no-checker-create-0001", valid)
	if code != 201 || created["request"].(map[string]any)["status"] != "PENDING" {
		t.Fatalf("no-checker submission: %d %v", code, created)
	}
	if code, out, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, headers.Get("ETag"), "role-change-second-pending-0001", valid); code != 409 || out["code"] != "ROLE_CHANGE_PENDING_EXISTS" {
		t.Fatalf("second pending: %d %v", code, out)
	}
	id := created["request"].(map[string]any)["id"].(string)
	cancelPath := "/api/v1/admin/role-change-requests/" + id + "/cancel"
	if code, cancelled, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, cancelPath, createdHeaders.Get("ETag"), "role-change-cancel-0001", `{"reasonCode":"WITHDRAWN"}`); code != 200 || cancelled["request"].(map[string]any)["status"] != "CANCELLED" {
		t.Fatalf("cancel no-checker request: %d %v", code, cancelled)
	}
	if code, _, current := roleCall(s, cookie, csrf, s.tenantA, http.MethodGet, eligibilityPath, "", "", ""); code != 200 || current.Get("ETag") != headers.Get("ETag") {
		t.Fatal("cancel touched target aggregate")
	}
}

func TestDirectoryRoleChangeConfigurationAndTargetDriftLeavePending(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	makerCookie, makerCSRF, checkerCookie, checkerCSRF := roleChangeManagers(t, s)
	for _, tc := range []struct {
		name, role string
		drift      func(uuid.UUID) error
		want       string
	}{
		{"configuration", "PLAN_PUBLISHER", func(_ uuid.UUID) error {
			_, err := s.h.Admin.Exec(ctx, `DELETE FROM iam.role_permission rp USING iam.role r WHERE rp.tenant_id=r.tenant_id AND rp.role_id=r.id AND r.tenant_id=$1 AND r.code='PLAN_PUBLISHER' AND rp.permission_code=(SELECT min(permission_code) FROM iam.role_permission p WHERE p.tenant_id=r.tenant_id AND p.role_id=r.id)`, s.tenantA)
			return err
		}, "ROLE_CHANGE_CONFIGURATION_CHANGED"},
		{"target", "CONTRACT_PUBLISHER", func(member uuid.UUID) error {
			var actor uuid.UUID
			if err := s.h.Admin.QueryRow(ctx, `SELECT actor_id FROM iam.tenant_membership WHERE tenant_id=$1 AND id=$2`, s.tenantA, member).Scan(&actor); err != nil {
				return err
			}
			_, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: actor, RoleCode: "AUDITOR"})
			return err
		}, "ROLE_CHANGE_TARGET_CHANGED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, target := roleTarget(t, s, "role-change-drift-"+tc.name)
			hash := roleChangeOption(t, s, makerCookie, makerCSRF, tc.role)
			path := "/api/v1/admin/users/" + target.String() + "/role-change-requests"
			body := `{"operation":"ASSIGN","roleCode":"` + tc.role + `","configurationHash":"` + hash + `","reasonCode":"ONBOARDING"}`
			code, created, headers := roleCall(s, makerCookie, makerCSRF, s.tenantA, http.MethodPost, path, `"1"`, "role-change-drift-create-"+tc.name, body)
			if code != 201 {
				t.Fatalf("drift fixture create: %d %v", code, created)
			}
			id := created["request"].(map[string]any)["id"].(string)
			if err := tc.drift(target); err != nil {
				t.Fatal(err)
			}
			decisionPath := "/api/v1/admin/role-change-requests/" + id
			code, out, _ := roleCall(s, checkerCookie, checkerCSRF, s.tenantA, http.MethodPost, decisionPath+"/approve", headers.Get("ETag"), "role-change-drift-approve-"+tc.name, `{}`)
			if code != 409 || out["code"] != tc.want {
				t.Fatalf("drift approval: %d %v", code, out)
			}
			code, detail, _ := roleCall(s, checkerCookie, checkerCSRF, s.tenantA, http.MethodGet, decisionPath, "", "", "")
			if code != 200 || detail["request"].(map[string]any)["status"] != "PENDING" || detail["canApprove"] != false || detail["approvalRefusalCode"] != tc.want {
				t.Fatalf("pending drift detail: %d %v", code, detail)
			}
			code, rejected, _ := roleCall(s, checkerCookie, checkerCSRF, s.tenantA, http.MethodPost, decisionPath+"/reject", headers.Get("ETag"), "role-change-drift-reject-"+tc.name, `{"reasonCode":"STALE_REQUEST"}`)
			if code != 200 || rejected["request"].(map[string]any)["status"] != "REJECTED" {
				t.Fatalf("reject stale request: %d %v", code, rejected)
			}
		})
	}
}

func TestDirectoryRoleChangeAuditFailureRollsBackRequestAndReceipt(t *testing.T) {
	s := newAuthzServer(t, failingDirectoryAudit{})
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	_, target := roleTarget(t, s, "role-change-audit-rollback-target")
	cookie, csrf := directorySession(t, s)
	stepUpDirectory(t, s, cookie, csrf)
	hash := roleChangeOption(t, s, cookie, csrf, "PLAN_PUBLISHER")
	path := "/api/v1/admin/users/" + target.String() + "/role-change-requests"
	body := `{"operation":"ASSIGN","roleCode":"PLAN_PUBLISHER","configurationHash":"` + hash + `","reasonCode":"ONBOARDING"}`
	code, out, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, path, `"1"`, "role-change-audit-rollback-0001", body)
	if code != 500 || out["code"] != "INTERNAL_ERROR" {
		t.Fatalf("audit failure response: %d %v", code, out)
	}
	var requests, receipts int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.role_change_request WHERE tenant_id=$1 AND target_membership_id=$2`, s.tenantA, target).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.role_change_command_receipt WHERE tenant_id=$1`, s.tenantA).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if requests != 0 || receipts != 0 {
		t.Fatalf("audit rollback left request=%d receipt=%d", requests, receipts)
	}
}

func TestDirectoryRoleChangeApprovalRechecksStepUpAfterTenantLockWait(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	makerCookie, makerCSRF, checkerCookie, checkerCSRF := roleChangeManagers(t, s)
	_, target := roleTarget(t, s, "role-change-wait-target")
	hash := roleChangeOption(t, s, makerCookie, makerCSRF, "PLAN_PUBLISHER")
	createPath := "/api/v1/admin/users/" + target.String() + "/role-change-requests"
	body := `{"operation":"ASSIGN","roleCode":"PLAN_PUBLISHER","configurationHash":"` + hash + `","reasonCode":"ONBOARDING"}`
	code, created, headers := roleCall(s, makerCookie, makerCSRF, s.tenantA, http.MethodPost, createPath, `"1"`, "role-change-wait-create-0001", body)
	if code != 201 {
		t.Fatalf("wait fixture create: %d %v", code, created)
	}
	id := created["request"].(map[string]any)["id"].(string)
	block, err := s.h.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = block.Rollback(ctx) }()
	if _, err := block.Exec(ctx, `SELECT id FROM platform.tenant WHERE id=$1 FOR UPDATE`, s.tenantA); err != nil {
		t.Fatal(err)
	}
	type result struct {
		code int
		body map[string]any
	}
	finished := make(chan result, 1)
	go func() {
		status, out, _ := roleCall(s, checkerCookie, checkerCSRF, s.tenantA, http.MethodPost, "/api/v1/admin/role-change-requests/"+id+"/approve", headers.Get("ETag"), "role-change-wait-approve-0001", `{}`)
		finished <- result{status, out}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := s.h.Admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%platform.tenant%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("approval did not reach tenant lock")
		}
		time.Sleep(15 * time.Millisecond)
	}
	var checker uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.actor WHERE identity_subject='role-change-checker'`).Scan(&checker); err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.session SET step_up_until=clock_timestamp()-interval '1 second' WHERE actor_id=$1 AND revoked_at IS NULL`, checker); err != nil {
		t.Fatal(err)
	}
	if err := block.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-finished:
		if got.code != 403 || got.body["code"] != "STEP_UP_REQUIRED" {
			t.Fatalf("post-wait expired step-up: %d %v", got.code, got.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("approval did not finish")
	}
	var grants int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.access_grant WHERE tenant_id=$1 AND tenant_membership_id=$2`, s.tenantA, target).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if grants != 0 {
		t.Fatalf("expired checker applied %d grants", grants)
	}
	stepUpDirectory(t, s, checkerCookie, checkerCSRF)
	if status, out, _ := roleCall(s, checkerCookie, checkerCSRF, s.tenantA, http.MethodPost, "/api/v1/admin/role-change-requests/"+id+"/approve", headers.Get("ETag"), "role-change-wait-approve-0001", `{}`); status != 200 || out["request"].(map[string]any)["status"] != "APPROVED" {
		t.Fatalf("fresh retry after wait: %d %v", status, out)
	}
}

func TestDirectoryRoleChangeAllFiveProtectedRolesUseApprovalOnly(t *testing.T) {
	s := newAuthzServer(t)
	makerCookie, makerCSRF, checkerCookie, checkerCSRF := roleChangeManagers(t, s)
	for _, role := range []string{"TENANT_ADMIN", "PLAN_PUBLISHER", "CONTRACT_PUBLISHER", "RULE_APPROVER", "PAYER_APPROVER"} {
		t.Run(role, func(t *testing.T) {
			_, target := roleTarget(t, s, "role-change-five-"+role)
			hash := roleChangeOption(t, s, makerCookie, makerCSRF, role)
			path := "/api/v1/admin/users/" + target.String() + "/role-change-requests"
			body := `{"operation":"ASSIGN","roleCode":"` + role + `","configurationHash":"` + hash + `","reasonCode":"DUTY_ASSIGNMENT"}`
			code, created, headers := roleCall(s, makerCookie, makerCSRF, s.tenantA, http.MethodPost, path, `"1"`, "role-change-five-create-"+role, body)
			if code != 201 || created["request"].(map[string]any)["status"] != "PENDING" {
				t.Fatalf("%s create: %d %v", role, code, created)
			}
			requestID := created["request"].(map[string]any)["id"].(string)
			code, approved, _ := roleCall(s, checkerCookie, checkerCSRF, s.tenantA, http.MethodPost, "/api/v1/admin/role-change-requests/"+requestID+"/approve", headers.Get("ETag"), "role-change-five-approve-"+role, `{}`)
			if code != 200 || approved["request"].(map[string]any)["status"] != "APPROVED" || approved["appliedGrant"].(map[string]any)["roleCode"] != role {
				t.Fatalf("%s approve: %d %v", role, code, approved)
			}
		})
	}
}

func TestDirectoryRoleChangeSuspensionPreservesDistinctRoleManagerIncludingFiniteServiceGrant(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	var userOnlyRole, callerMembership, tenantAdminRole uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO iam.role(tenant_id,code,name,is_system_role) VALUES ($1,'USER_ONLY_MANAGER','User only manager',false) RETURNING id`, s.tenantA).Scan(&userOnlyRole); err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `INSERT INTO iam.role_permission(tenant_id,role_id,permission_code) VALUES ($1,$2,'identity.user.manage')`, s.tenantA, userOnlyRole); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2`, s.tenantA, s.actor).Scan(&callerMembership); err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `INSERT INTO iam.access_grant(tenant_id,tenant_membership_id,role_id,scope_type) VALUES ($1,$2,$3,'TENANT')`, s.tenantA, callerMembership, userOnlyRole); err != nil {
		t.Fatal(err)
	}
	targetActor, err := s.svc.CreateAccount(ctx, "role-change-last-target", "Last role manager", "role-change-last-target", testPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: targetActor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	var target uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2`, s.tenantA, targetActor).Scan(&target); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.role WHERE tenant_id=$1 AND code='TENANT_ADMIN'`, s.tenantA).Scan(&tenantAdminRole); err != nil {
		t.Fatal(err)
	}
	// Duplicate grants from the same membership still represent one manager.
	if _, err := s.h.Admin.Exec(ctx, `INSERT INTO iam.access_grant(tenant_id,tenant_membership_id,role_id,scope_type) VALUES ($1,$2,$3,'TENANT')`, s.tenantA, target, tenantAdminRole); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := directorySession(t, s)
	stepUpDirectory(t, s, cookie, csrf)
	code, out, _ := suspendCall(s, cookie, csrf, s.tenantA, target, `"1"`, "role-change-last-suspend-0001", "ACCESS_REVIEW")
	if code != 409 || out["code"] != "LAST_TENANT_ROLE_MANAGER" {
		t.Fatalf("duplicate grant counted twice: %d %v", code, out)
	}
	var serviceActor, serviceMember uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO iam.actor(identity_issuer,identity_subject,actor_type,display_name,status) VALUES ('kapsora','role-change-service-manager','SERVICE_ACCOUNT','Service manager','ACTIVE') RETURNING id`).Scan(&serviceActor); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO iam.tenant_membership(tenant_id,actor_id,membership_status) VALUES ($1,$2,'ACTIVE') RETURNING id`, s.tenantA, serviceActor).Scan(&serviceMember); err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `INSERT INTO iam.access_grant(tenant_id,tenant_membership_id,role_id,scope_type,valid_period) VALUES ($1,$2,$3,'TENANT',tstzrange(clock_timestamp()-interval '1 minute',clock_timestamp()+interval '1 day','[)'))`, s.tenantA, serviceMember, tenantAdminRole); err != nil {
		t.Fatal(err)
	}
	code, out, _ = suspendCall(s, cookie, csrf, s.tenantA, target, `"1"`, "role-change-last-suspend-0002", "ACCESS_REVIEW")
	if code != 200 {
		t.Fatalf("finite service manager not counted: %d %v", code, out)
	}
}

func TestDirectoryRoleChangeSyncRefusesInUseProtectedPermissionExpansion(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	target, err := s.svc.CreateAccount(ctx, "role-change-sync-target", "Role sync target", "role-change-sync-target", testPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: target, RoleCode: "PLAN_PUBLISHER"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.h.Admin.Exec(ctx, `DELETE FROM iam.role_permission rp USING iam.role r WHERE rp.tenant_id=r.tenant_id AND rp.role_id=r.id AND r.tenant_id=$1 AND r.code='PLAN_PUBLISHER' AND rp.permission_code='plan.publish'`, s.tenantA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.prov.SyncSystemRoles(ctx, s.tenantA); !errors.Is(err, application.ErrRoleConfigurationInUse) {
		t.Fatalf("B in-use sync: %v", err)
	}
	var present int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.role_permission rp JOIN iam.role r ON r.tenant_id=rp.tenant_id AND r.id=rp.role_id WHERE r.tenant_id=$1 AND r.code='PLAN_PUBLISHER' AND rp.permission_code='plan.publish'`, s.tenantA).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if present != 0 {
		t.Fatal("refused B sync nevertheless expanded role permissions")
	}
}
