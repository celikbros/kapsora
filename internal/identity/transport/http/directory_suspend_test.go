package identityhttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/identity/application"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

func suspendCall(s *authzServer, cookie *http.Cookie, csrf string, tenant, member uuid.UUID, version, key, reason string) (int, map[string]any, http.Header) {
	headers := map[string]string{identityhttp.TenantHeader: tenant.String(), "Idempotency-Key": key}
	if version != "" {
		headers["If-Match"] = version
	}
	rec := s.do(call{method: http.MethodPost, path: "/api/v1/admin/users/" + member.String() + "/suspend",
		cookie: cookie, csrf: csrf, headers: headers, body: `{"reasonCode":"` + reason + `"}`})
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body, rec.Header()
}

func stepUpDirectory(t *testing.T, s *authzServer, cookie *http.Cookie, csrf string) {
	t.Helper()
	rec := s.do(call{method: http.MethodPost, path: "/api/v1/session/step-up", cookie: cookie, csrf: csrf,
		body: `{"password":"` + testPassword + `"}`})
	if rec.Code != http.StatusOK {
		t.Fatalf("step-up: %d", rec.Code)
	}
}

func TestDirectorySuspendStepUpVersionReplayAuditAndTenantBoundary(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	target, err := s.svc.CreateAccount(ctx, "directory-target", "Directory target", "directory-target", testPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, tenant := range []uuid.UUID{s.tenantA, s.tenantB} {
		if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: tenant, ActorID: target, RoleCode: "AUDITOR"}); err != nil {
			t.Fatal(err)
		}
	}
	var targetA uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2`, s.tenantA, target).Scan(&targetA); err != nil {
		t.Fatal(err)
	}
	adminCookie, adminCSRF := directorySession(t, s)
	login := s.do(call{method: http.MethodPost, path: "/api/v1/session/login",
		body: `{"username":"directory-target","password":"` + testPassword + `"}`})
	if login.Code != http.StatusOK {
		t.Fatalf("target login: %d", login.Code)
	}
	targetCookie := sessionCookie(t, login, s.cookies.Name())
	targetCSRF, _ := decodeBody(t, login)["csrfToken"].(string)
	if rec := s.do(call{method: http.MethodPost, path: "/api/v1/session/switch-tenant", cookie: targetCookie, csrf: targetCSRF,
		body: `{"tenantId":"` + s.tenantA.String() + `"}`}); rec.Code != http.StatusOK {
		t.Fatalf("target tenant switch: %d", rec.Code)
	}
	if rec := s.do(call{method: http.MethodGet, path: "/api/v1/probe", cookie: targetCookie,
		headers: map[string]string{identityhttp.TenantHeader: s.tenantA.String()}}); rec.Code != http.StatusOK {
		t.Fatalf("target before suspension: %d", rec.Code)
	}
	_, detail := directoryCall(s, adminCookie, s.tenantA, "/api/v1/admin/users/"+targetA.String(), "backoffice")
	version := int64(detail["membership"].(map[string]any)["rowVersion"].(float64))
	initialTag := `"` + versionString(version) + `"`
	const key = "suspend-target-request-0001"
	if code, body, _ := suspendCall(s, adminCookie, adminCSRF, s.tenantA, targetA, initialTag, key, "ACCESS_REVIEW"); code != http.StatusForbidden || body["code"] != "STEP_UP_REQUIRED" {
		t.Fatalf("missing step-up: %d %v", code, body["code"])
	}
	stepUpDirectory(t, s, adminCookie, adminCSRF)
	var ownA, targetB uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2`, s.tenantA, s.actor).Scan(&ownA); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2`, s.tenantB, target).Scan(&targetB); err != nil {
		t.Fatal(err)
	}
	if code, body, _ := suspendCall(s, adminCookie, adminCSRF, s.tenantA, ownA, `"1"`, "suspend-self-request-0001", "ACCESS_REVIEW"); code != http.StatusConflict || body["code"] != "SELF_SUSPENSION_FORBIDDEN" {
		t.Fatalf("self suspension: %d %v", code, body["code"])
	}
	if code, body, _ := suspendCall(s, adminCookie, adminCSRF, s.tenantA, targetB, `"1"`, "suspend-foreign-request-0001", "ACCESS_REVIEW"); code != http.StatusNotFound || body["code"] != "MEMBERSHIP_NOT_FOUND" {
		t.Fatalf("foreign suspension: %d %v", code, body["code"])
	}
	if code, body, _ := suspendCall(s, adminCookie, adminCSRF, s.tenantA, targetA, initialTag, "suspend-invalid-reason-0001", "OTHER"); code != http.StatusBadRequest || body["code"] != "SUSPENSION_REASON_INVALID" {
		t.Fatalf("invalid reason: %d %v", code, body["code"])
	}
	if code, body, _ := suspendCall(s, adminCookie, adminCSRF, s.tenantA, targetA, `"999"`, "suspend-stale-version-0001", "ACCESS_REVIEW"); code != http.StatusPreconditionFailed || body["code"] != "ETAG_MISMATCH" {
		t.Fatalf("stale version: %d %v", code, body["code"])
	}
	if code, body, _ := suspendCall(s, adminCookie, adminCSRF, s.tenantA, targetA, "", "suspend-no-etag-request-0001", "ACCESS_REVIEW"); code != http.StatusPreconditionRequired || body["code"] != "IF_MATCH_REQUIRED" {
		t.Fatalf("missing etag: %d %v", code, body["code"])
	}
	code, body, headers := suspendCall(s, adminCookie, adminCSRF, s.tenantA, targetA, initialTag, key, "ACCESS_REVIEW")
	if code != http.StatusOK || body["membership"].(map[string]any)["membershipStatus"] != "SUSPENDED" || headers.Get("ETag") == initialTag {
		t.Fatalf("suspension outcome: %d", code)
	}
	newTag := headers.Get("ETag")
	if code, _, headers := suspendCall(s, adminCookie, adminCSRF, s.tenantA, targetA, initialTag, key, "ACCESS_REVIEW"); code != http.StatusOK || headers.Get("Idempotent-Replayed") != "true" || headers.Get("ETag") != newTag {
		t.Fatalf("replay changed outcome: %d", code)
	}
	if code, body, _ := suspendCall(s, adminCookie, adminCSRF, s.tenantA, targetA, initialTag, key, "SECURITY_CONCERN"); code != http.StatusConflict || body["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("altered reason reused key: %d %v", code, body["code"])
	}
	if code, body, _ := suspendCall(s, adminCookie, adminCSRF, s.tenantA, targetA, newTag, key, "ACCESS_REVIEW"); code != http.StatusConflict || body["code"] != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("altered etag reused key: %d %v", code, body["code"])
	}
	if rec := s.do(call{method: http.MethodGet, path: "/api/v1/probe", cookie: targetCookie,
		headers: map[string]string{identityhttp.TenantHeader: s.tenantA.String()}}); rec.Code == http.StatusOK {
		t.Fatal("suspended target retained tenant A access")
	}
	if rec := s.do(call{method: http.MethodPost, path: "/api/v1/session/switch-tenant", cookie: targetCookie, csrf: targetCSRF,
		body: `{"tenantId":"` + s.tenantB.String() + `"}`}); rec.Code != http.StatusOK {
		t.Fatalf("other tenant lost session: %d", rec.Code)
	}
	if rec := s.do(call{method: http.MethodGet, path: "/api/v1/probe", cookie: targetCookie,
		headers: map[string]string{identityhttp.TenantHeader: s.tenantB.String()}}); rec.Code != http.StatusOK {
		t.Fatalf("other tenant access: %d", rec.Code)
	}
	var status string
	var auditCount int
	var auditDetail []byte
	if err := s.h.Admin.QueryRow(ctx, `SELECT status FROM iam.actor WHERE id=$1`, target).Scan(&status); err != nil || status != "ACTIVE" {
		t.Fatal("global actor status changed")
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM audit.event WHERE tenant_id=$1 AND action_code='tenant_membership.suspend' AND resource_id=$2`, s.tenantA, targetA).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("one transaction audit event expected: count=%d err=%v", auditCount, err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT detail_json FROM audit.event WHERE tenant_id=$1 AND action_code='tenant_membership.suspend' AND resource_id=$2`, s.tenantA, targetA).Scan(&auditDetail); err != nil {
		t.Fatal(err)
	}
	var safe map[string]any
	if err := json.Unmarshal(auditDetail, &safe); err != nil || safe["old_status"] != "ACTIVE" || safe["new_status"] != "SUSPENDED" {
		t.Fatal("audit omitted safe status transition")
	}
}

func versionString(version int64) string { return strconv.FormatInt(version, 10) }

type failingDirectoryAudit struct{}

func (failingDirectoryAudit) Record(context.Context, pgx.Tx, audit.Event) error {
	return errors.New("synthetic audit failure")
}
func (failingDirectoryAudit) RecordAccess(context.Context, pgx.Tx, audit.AccessEvent) error {
	return nil
}

func TestDirectorySuspendAuditFailureRollsBackMembership(t *testing.T) {
	s := newAuthzServer(t, failingDirectoryAudit{})
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	target := s.h.CreateActor("rollback-target", "Rollback target")
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: target, RoleCode: "AUDITOR"}); err != nil {
		t.Fatal(err)
	}
	var memberID uuid.UUID
	var initialVersion int64
	if err := s.h.Admin.QueryRow(ctx, `SELECT id, row_version FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2`, s.tenantA, target).Scan(&memberID, &initialVersion); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := directorySession(t, s)
	stepUpDirectory(t, s, cookie, csrf)
	code, body, _ := suspendCall(s, cookie, csrf, s.tenantA, memberID, `"`+versionString(initialVersion)+`"`, "rollback-audit-request-0001", "ACCESS_REVIEW")
	if code != http.StatusInternalServerError || body["code"] != "INTERNAL_ERROR" {
		t.Fatalf("audit failure response: %d %v", code, body["code"])
	}
	var status string
	var afterVersion int64
	if err := s.h.Admin.QueryRow(ctx, `SELECT membership_status, row_version FROM iam.tenant_membership WHERE tenant_id=$1 AND id=$2`, s.tenantA, memberID).Scan(&status, &afterVersion); err != nil || status != "ACTIVE" || afterVersion != initialVersion {
		t.Fatal("audit failure committed membership transition")
	}
}

func TestDirectorySuspendConcurrentCommandsUpdateOneVersion(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	target := s.h.CreateActor("concurrent-target", "Concurrent target")
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: target, RoleCode: "AUDITOR"}); err != nil {
		t.Fatal(err)
	}
	var memberID uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2`, s.tenantA, target).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := directorySession(t, s)
	stepUpDirectory(t, s, cookie, csrf)
	_, detail := directoryCall(s, cookie, s.tenantA, "/api/v1/admin/users/"+memberID.String(), "backoffice")
	version := int64(detail["membership"].(map[string]any)["rowVersion"].(float64))
	tag := `"` + versionString(version) + `"`
	var wg sync.WaitGroup
	var statuses [2]int
	for i := range statuses {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			statuses[index], _, _ = suspendCall(s, cookie, csrf, s.tenantA, memberID, tag, "parallel-manager-request-000"+versionString(int64(index)), "ACCESS_REVIEW")
		}(i)
	}
	wg.Wait()
	if (statuses[0] != http.StatusOK || statuses[1] != http.StatusConflict) && (statuses[1] != http.StatusOK || statuses[0] != http.StatusConflict) {
		t.Fatalf("same-version concurrent outcomes: %v", statuses)
	}
	var afterVersion int64
	if err := s.h.Admin.QueryRow(ctx, `SELECT row_version FROM iam.tenant_membership WHERE tenant_id=$1 AND id=$2`, s.tenantA, memberID).Scan(&afterVersion); err != nil || afterVersion != version+1 {
		t.Fatalf("concurrent commands changed version more than once: %d %v", afterVersion, err)
	}
}

func TestDirectorySuspendCompetingManagersKeepOneUsableManager(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	type manager struct {
		actor  uuid.UUID
		member uuid.UUID
		cookie *http.Cookie
		csrf   string
		tag    string
	}
	var managers [2]manager
	for i, user := range []string{"directory-manager-one", "directory-manager-two"} {
		actor, err := s.svc.CreateAccount(ctx, user, "Directory manager", user, testPassword, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: actor, RoleCode: "TENANT_ADMIN"}); err != nil {
			t.Fatal(err)
		}
		managers[i].actor = actor
		if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2`, s.tenantA, actor).Scan(&managers[i].member); err != nil {
			t.Fatal(err)
		}
		login := s.do(call{method: http.MethodPost, path: "/api/v1/session/login",
			body: `{"username":"` + user + `","password":"` + testPassword + `"}`})
		if login.Code != http.StatusOK {
			t.Fatalf("manager login: %d", login.Code)
		}
		managers[i].cookie = sessionCookie(t, login, s.cookies.Name())
		managers[i].csrf, _ = decodeBody(t, login)["csrfToken"].(string)
		if rec := s.do(call{method: http.MethodPost, path: "/api/v1/session/switch-tenant", cookie: managers[i].cookie, csrf: managers[i].csrf,
			body: `{"tenantId":"` + s.tenantA.String() + `"}`}); rec.Code != http.StatusOK {
			t.Fatalf("manager tenant switch: %d", rec.Code)
		}
		stepUpDirectory(t, s, managers[i].cookie, managers[i].csrf)
	}
	for i := range managers {
		code, detail := directoryCall(s, managers[i].cookie, s.tenantA, "/api/v1/admin/users/"+managers[1-i].member.String(), "backoffice")
		if code != http.StatusOK {
			t.Fatalf("competing manager detail: %d", code)
		}
		version := int64(detail["membership"].(map[string]any)["rowVersion"].(float64))
		managers[i].tag = `"` + versionString(version) + `"`
	}
	var wg sync.WaitGroup
	var statuses [2]int
	for i := range managers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			caller, target := managers[index], managers[1-index]
			statuses[index], _, _ = suspendCall(s, caller.cookie, caller.csrf, s.tenantA, target.member,
				caller.tag, "cross-manager-suspend-000"+versionString(int64(index)), "ACCESS_REVIEW")
		}(i)
	}
	wg.Wait()
	if (statuses[0] != http.StatusOK || statuses[1] != http.StatusForbidden) && (statuses[1] != http.StatusOK || statuses[0] != http.StatusForbidden) {
		t.Fatalf("competing manager outcomes: %v", statuses)
	}
	var remaining int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id IN ($2,$3) AND membership_status='ACTIVE'`,
		s.tenantA, managers[0].actor, managers[1].actor).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("usable manager count after race: %d %v", remaining, err)
	}
}

func TestDirectoryUsableManagerPredicateCountsOnlyCurrentTenantGrant(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	var callerMembership uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2`, s.tenantA, s.actor).Scan(&callerMembership); err != nil {
		t.Fatal(err)
	}
	type grantCase struct {
		subject string
		period  string
		service bool
		want    bool
	}
	cases := []grantCase{
		{"future-manager", "future", false, false},
		{"expired-manager", "expired", false, false},
		{"empty-manager", "empty", false, false},
		{"service-manager", "", true, true},
	}
	ids := make([]uuid.UUID, 0, len(cases))
	for _, item := range cases {
		actor := s.h.CreateActor(item.subject, "Predicate manager")
		if item.service {
			if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.actor SET actor_type='SERVICE_ACCOUNT' WHERE id=$1`, actor); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: actor, RoleCode: "TENANT_ADMIN"}); err != nil {
			t.Fatal(err)
		}
		var member uuid.UUID
		if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.tenant_membership WHERE tenant_id=$1 AND actor_id=$2`, s.tenantA, actor).Scan(&member); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, member)
		if item.period != "" {
			if _, err := s.h.Admin.Exec(ctx, `UPDATE iam.access_grant SET valid_period = CASE $3::text
              WHEN 'future' THEN tstzrange(clock_timestamp()+interval '1 day',NULL,'[)')
              WHEN 'expired' THEN tstzrange(clock_timestamp()-interval '2 days',clock_timestamp()-interval '1 day','[)')
              ELSE 'empty'::tstzrange END
              WHERE tenant_id=$1 AND tenant_membership_id=$2`, s.tenantA, member, item.period); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := db.WithTenantTx(ctx, s.h.App, db.TenantContext{TenantID: s.tenantA, ActorID: s.actor}, func(ctx context.Context, tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		count, err := q.CountOtherUsableTenantUserManagers(ctx, sqlcgen.CountOtherUsableTenantUserManagersParams{TenantID: s.tenantA, ID: callerMembership})
		if err != nil {
			return err
		}
		if count != 1 {
			t.Fatalf("usable managers besides caller = %d, want only service account", count)
		}
		for i, member := range ids {
			usable, err := q.IsUsableTenantUserManager(ctx, sqlcgen.IsUsableTenantUserManagerParams{TenantID: s.tenantA, ID: member})
			if err != nil {
				return err
			}
			if usable != cases[i].want {
				t.Fatalf("current manager predicate case %d = %v", i, usable)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
