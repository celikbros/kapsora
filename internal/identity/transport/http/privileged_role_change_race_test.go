package identityhttp_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/identity/application"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
)

type roleChangeRaceFixture struct {
	s                          *authzServer
	makerCookie, checkerCookie *http.Cookie
	makerCSRF, checkerCSRF     string
	targetActor, targetMember  uuid.UUID
	requestID, requestETag     string
}

func roleChangeRunTwoAtTenantBarrier(t *testing.T, s *authzServer, first, second func(), allowGenericClaimWait bool) {
	t.Helper()
	ctx := context.Background()
	block, err := s.h.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = block.Rollback(ctx) }()
	if _, err := block.Exec(ctx, `SELECT id FROM platform.tenant WHERE id=$1 FOR UPDATE`, s.tenantA); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); first() }()
	go func() { defer wg.Done(); second() }()
	deadline := time.Now().Add(8 * time.Second)
	for {
		var tenantWaiting, claimWaiting int
		if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FILTER (WHERE query LIKE '%platform.tenant%'),count(*) FILTER (WHERE query LIKE '%system.idempotency_record%') FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock'`).Scan(&tenantWaiting, &claimWaiting); err != nil {
			t.Fatal(err)
		}
		// Directory suspension claims its generic idempotency key before entering
		// Suspend. The claim's FK can wait on this tenant row, while B approval
		// already waits in its preflight tenant lock. Both are actual DB barriers.
		if tenantWaiting >= 2 || (allowGenericClaimWait && tenantWaiting >= 1 && tenantWaiting+claimWaiting >= 2) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d tenant-lock and %d generic-claim waiters reached the DB barrier", tenantWaiting, claimWaiting)
		}
		time.Sleep(15 * time.Millisecond)
	}
	if err := block.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("role-change writers did not finish after tenant lock release")
	}
}

func newRoleChangeRaceFixture(t *testing.T) roleChangeRaceFixture {
	t.Helper()
	s := newAuthzServer(t)
	makerCookie, makerCSRF, checkerCookie, checkerCSRF := roleChangeManagers(t, s)
	actor, member := roleTarget(t, s, "role-change-race-"+uuid.NewString())
	hash := roleChangeOption(t, s, makerCookie, makerCSRF, "PLAN_PUBLISHER")
	path := "/api/v1/admin/users/" + member.String() + "/role-change-requests"
	body := `{"operation":"ASSIGN","roleCode":"PLAN_PUBLISHER","configurationHash":"` + hash + `","reasonCode":"ONBOARDING"}`
	code, created, headers := roleCall(s, makerCookie, makerCSRF, s.tenantA, http.MethodPost, path, `"1"`, "role-change-race-create-"+uuid.NewString(), body)
	if code != 201 {
		t.Fatalf("race fixture create: %d %v", code, created)
	}
	return roleChangeRaceFixture{s: s, makerCookie: makerCookie, checkerCookie: checkerCookie, makerCSRF: makerCSRF, checkerCSRF: checkerCSRF, targetActor: actor, targetMember: member, requestID: created["request"].(map[string]any)["id"].(string), requestETag: headers.Get("ETag")}
}

func TestDirectoryRoleChangeConcurrentDecisionsHaveOneTerminalEffect(t *testing.T) {
	for _, other := range []string{"approve", "reject", "cancel"} {
		t.Run("approve-versus-"+other, func(t *testing.T) {
			f := newRoleChangeRaceFixture(t)
			path := "/api/v1/admin/role-change-requests/" + f.requestID
			type result struct {
				code int
				body map[string]any
			}
			results := make([]result, 2)
			roleChangeRunTwoAtTenantBarrier(t, f.s, func() {
				results[0].code, results[0].body, _ = roleCall(f.s, f.checkerCookie, f.checkerCSRF, f.s.tenantA, http.MethodPost, path+"/approve", f.requestETag, "role-change-race-approve-"+uuid.NewString(), `{}`)
			}, func() {
				cookie, csrf := f.checkerCookie, f.checkerCSRF
				body := `{}`
				if other == "reject" {
					body = `{"reasonCode":"STALE_REQUEST"}`
				}
				if other == "cancel" {
					cookie, csrf = f.makerCookie, f.makerCSRF
					body = `{"reasonCode":"WITHDRAWN"}`
				}
				results[1].code, results[1].body, _ = roleCall(f.s, cookie, csrf, f.s.tenantA, http.MethodPost, path+"/"+other, f.requestETag, "role-change-race-"+other+"-"+uuid.NewString(), body)
			}, false)
			success := 0
			for _, r := range results {
				if r.code == 200 {
					success++
				} else if r.code != 412 || r.body["code"] != "ETAG_MISMATCH" {
					t.Fatalf("unexpected competing decision: %d %v", r.code, r.body)
				}
			}
			if success != 1 {
				t.Fatalf("competing decisions succeeded %d times: %+v", success, results)
			}
			var status string
			var grants, decisions int
			ctx := context.Background()
			if err := f.s.h.Admin.QueryRow(ctx, `SELECT status FROM iam.role_change_request WHERE tenant_id=$1 AND id=$2`, f.s.tenantA, uuid.MustParse(f.requestID)).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if err := f.s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.access_grant WHERE tenant_id=$1 AND tenant_membership_id=$2`, f.s.tenantA, f.targetMember).Scan(&grants); err != nil {
				t.Fatal(err)
			}
			if err := f.s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM audit.event WHERE tenant_id=$1 AND resource_id=$2 AND action_code IN ('role_change_request.approve','role_change_request.reject','role_change_request.cancel') AND outcome='SUCCESS'`, f.s.tenantA, uuid.MustParse(f.requestID)).Scan(&decisions); err != nil {
				t.Fatal(err)
			}
			if decisions != 1 || (status == "APPROVED" && grants != 1) || (status != "APPROVED" && grants != 0) {
				t.Fatalf("race outcome status=%s grants=%d decisions=%d", status, grants, decisions)
			}
		})
	}
}

func TestDirectoryRoleChangeApprovalRacesSuspensionAndOfflineGrant(t *testing.T) {
	for _, other := range []string{"suspend", "offline-grant"} {
		t.Run(other, func(t *testing.T) {
			f := newRoleChangeRaceFixture(t)
			type result struct {
				code int
				body map[string]any
				err  error
			}
			var approval, competing result
			roleChangeRunTwoAtTenantBarrier(t, f.s, func() {
				approval.code, approval.body, _ = roleCall(f.s, f.checkerCookie, f.checkerCSRF, f.s.tenantA, http.MethodPost, "/api/v1/admin/role-change-requests/"+f.requestID+"/approve", f.requestETag, "role-change-race-apply-"+uuid.NewString(), `{}`)
			}, func() {
				if other == "suspend" {
					competing.code, competing.body, _ = suspendCall(f.s, f.makerCookie, f.makerCSRF, f.s.tenantA, f.targetMember, `"1"`, "role-change-race-suspend-"+uuid.NewString(), "ACCESS_REVIEW")
				} else {
					_, competing.err = f.s.prov.GrantRole(context.Background(), application.GrantRoleInput{TenantID: f.s.tenantA, ActorID: f.targetActor, RoleCode: "AUDITOR"})
					if competing.err == nil {
						competing.code = 200
					}
				}
			}, other == "suspend")
			var status string
			var version int64
			var grants int
			ctx := context.Background()
			if err := f.s.h.Admin.QueryRow(ctx, `SELECT status FROM iam.role_change_request WHERE tenant_id=$1 AND id=$2`, f.s.tenantA, uuid.MustParse(f.requestID)).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if err := f.s.h.Admin.QueryRow(ctx, `SELECT row_version FROM iam.tenant_membership WHERE tenant_id=$1 AND id=$2`, f.s.tenantA, f.targetMember).Scan(&version); err != nil {
				t.Fatal(err)
			}
			if err := f.s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.access_grant WHERE tenant_id=$1 AND tenant_membership_id=$2`, f.s.tenantA, f.targetMember).Scan(&grants); err != nil {
				t.Fatal(err)
			}
			if other == "suspend" {
				approvalFirst := approval.code == 200 && competing.code == 412 && status == "APPROVED" && version == 2 && grants == 1
				suspendFirst := competing.code == 200 && approval.code == 409 && status == "PENDING" && version == 2 && grants == 0
				if !approvalFirst && !suspendFirst {
					t.Fatalf("approve/suspend race: approval=%d %v suspend=%d %v status=%s version=%d grants=%d", approval.code, approval.body, competing.code, competing.body, status, version, grants)
				}
			} else {
				if competing.err != nil {
					t.Fatal(competing.err)
				}
				approvalFirst := approval.code == 200 && status == "APPROVED" && version == 3 && grants == 2
				offlineFirst := approval.code == 409 && status == "PENDING" && version == 2 && grants == 1
				if !approvalFirst && !offlineFirst {
					t.Fatalf("approve/offline race: approval=%d %v status=%s version=%d grants=%d", approval.code, approval.body, status, version, grants)
				}
			}
		})
	}
}

func TestDirectoryRoleChangeApprovalAndSafeSyncSerializeConfiguration(t *testing.T) {
	f := newRoleChangeRaceFixture(t)
	ctx := context.Background()
	if _, err := f.s.h.Admin.Exec(ctx, `DELETE FROM iam.role_permission rp USING iam.role r WHERE rp.tenant_id=r.tenant_id AND rp.role_id=r.id AND r.tenant_id=$1 AND r.code='PLAN_PUBLISHER' AND rp.permission_code='plan.publish'`, f.s.tenantA); err != nil {
		t.Fatal(err)
	}
	var approvalCode int
	var approvalBody map[string]any
	var syncErr error
	roleChangeRunTwoAtTenantBarrier(t, f.s, func() {
		approvalCode, approvalBody, _ = roleCall(f.s, f.checkerCookie, f.checkerCSRF, f.s.tenantA, http.MethodPost, "/api/v1/admin/role-change-requests/"+f.requestID+"/approve", f.requestETag, "role-change-race-sync-approve-0001", `{}`)
	}, func() { _, syncErr = f.s.prov.SyncSystemRoles(ctx, f.s.tenantA) }, false)
	if syncErr != nil {
		t.Fatal(syncErr)
	}
	var status string
	var grants, permissions int
	var version int64
	if err := f.s.h.Admin.QueryRow(ctx, `SELECT status FROM iam.role_change_request WHERE tenant_id=$1 AND id=$2`, f.s.tenantA, uuid.MustParse(f.requestID)).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := f.s.h.Admin.QueryRow(ctx, `SELECT row_version FROM iam.tenant_membership WHERE tenant_id=$1 AND id=$2`, f.s.tenantA, f.targetMember).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := f.s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.access_grant WHERE tenant_id=$1 AND tenant_membership_id=$2`, f.s.tenantA, f.targetMember).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if err := f.s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.role_permission rp JOIN iam.role r ON r.tenant_id=rp.tenant_id AND r.id=rp.role_id WHERE r.tenant_id=$1 AND r.code='PLAN_PUBLISHER' AND rp.permission_code='plan.publish'`, f.s.tenantA).Scan(&permissions); err != nil {
		t.Fatal(err)
	}
	if permissions != 1 {
		t.Fatalf("safe sync did not restore template permission: %d", permissions)
	}
	approved := approvalCode == 200 && status == "APPROVED" && version == 2 && grants == 1
	drifted := approvalCode == 409 && approvalBody["code"] == "ROLE_CHANGE_CONFIGURATION_CHANGED" && status == "PENDING" && version == 1 && grants == 0
	if !approved && !drifted {
		t.Fatalf("approval/sync race: approve=%d %v status=%s version=%d grants=%d", approvalCode, approvalBody, status, version, grants)
	}
}

func TestDirectoryRoleChangeReplayRequiresCurrentTenantAndStepUp(t *testing.T) {
	f := newRoleChangeRaceFixture(t)
	ctx := context.Background()
	path := "/api/v1/admin/role-change-requests/" + f.requestID + "/approve"
	const key = "role-change-replay-current-authority-0001"
	code, approved, _ := roleCall(f.s, f.checkerCookie, f.checkerCSRF, f.s.tenantA, http.MethodPost, path, f.requestETag, key, `{}`)
	if code != 200 {
		t.Fatalf("replay fixture approval: %d %v", code, approved)
	}
	var checker uuid.UUID
	if err := f.s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.actor WHERE identity_subject='role-change-checker'`).Scan(&checker); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.h.Admin.Exec(ctx, `UPDATE iam.session SET step_up_until=clock_timestamp()-interval '1 second' WHERE actor_id=$1 AND revoked_at IS NULL`, checker); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := roleCall(f.s, f.checkerCookie, f.checkerCSRF, f.s.tenantA, http.MethodPost, path, f.requestETag, key, `{}`); code != 403 || out["code"] != "STEP_UP_REQUIRED" {
		t.Fatalf("expired step-up replay: %d %v", code, out)
	}
	stepUpDirectory(t, f.s, f.checkerCookie, f.checkerCSRF)
	if _, err := f.s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: f.s.tenantB, ActorID: checker, RoleCode: "AUDITOR"}); err != nil {
		t.Fatal(err)
	}
	switchTenant := func(tenant uuid.UUID) {
		t.Helper()
		rec := f.s.do(call{method: http.MethodPost, path: "/api/v1/session/switch-tenant", cookie: f.checkerCookie, csrf: f.checkerCSRF, body: `{"tenantId":"` + tenant.String() + `"}`})
		if rec.Code != 200 {
			t.Fatalf("checker tenant switch: %d %s", rec.Code, rec.Body.String())
		}
	}
	switchTenant(f.s.tenantB)
	if code, _, _ := roleCall(f.s, f.checkerCookie, f.checkerCSRF, f.s.tenantA, http.MethodPost, path, f.requestETag, key, `{}`); code == 200 {
		t.Fatal("switched session retrieved role change receipt")
	}
	switchTenant(f.s.tenantA)
	stepUpDirectory(t, f.s, f.checkerCookie, f.checkerCSRF)
	if code, out, headers := roleCall(f.s, f.checkerCookie, f.checkerCSRF, f.s.tenantA, http.MethodPost, path, f.requestETag, key, `{}`); code != 200 || headers.Get("Idempotent-Replayed") != "true" || out["request"].(map[string]any)["id"] != f.requestID {
		t.Fatalf("re-authorized replay: %d %v", code, out)
	}
}

func TestDirectoryRoleChangeRawZeroFutureAndPersonAccessRefusal(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	if _, err := s.prov.GrantRole(ctx, application.GrantRoleInput{TenantID: s.tenantA, ActorID: s.actor, RoleCode: "TENANT_ADMIN"}); err != nil {
		t.Fatal(err)
	}
	cookie, csrf := directorySession(t, s)
	stepUpDirectory(t, s, cookie, csrf)
	hash := roleChangeOption(t, s, cookie, csrf, "PLAN_PUBLISHER")
	var zeroRole, auditorRole, memberRole uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO iam.role(tenant_id,code,name,is_system_role) VALUES ($1,'ZERO_PERMISSION_ROLE','Zero permission role',false) RETURNING id`, s.tenantA).Scan(&zeroRole); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.role WHERE tenant_id=$1 AND code='AUDITOR'`, s.tenantA).Scan(&auditorRole); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.role WHERE tenant_id=$1 AND code='MEMBER'`, s.tenantA).Scan(&memberRole); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"zero", "future", "person-history"} {
		t.Run(kind, func(t *testing.T) {
			_, member := roleTarget(t, s, "role-change-raw-"+kind)
			switch kind {
			case "zero":
				_, err := s.h.Admin.Exec(ctx, `INSERT INTO iam.access_grant(tenant_id,tenant_membership_id,role_id,scope_type) VALUES ($1,$2,$3,'TENANT')`, s.tenantA, member, zeroRole)
				if err != nil {
					t.Fatal(err)
				}
			case "future":
				_, err := s.h.Admin.Exec(ctx, `INSERT INTO iam.access_grant(tenant_id,tenant_membership_id,role_id,scope_type,valid_period) VALUES ($1,$2,$3,'TENANT',tstzrange(clock_timestamp()+interval '1 day',NULL,'[)'))`, s.tenantA, member, auditorRole)
				if err != nil {
					t.Fatal(err)
				}
			case "person-history":
				var person uuid.UUID
				if err := s.h.Admin.QueryRow(ctx, `INSERT INTO party.person(tenant_id,first_name,last_name,normalized_name) VALUES ($1,'Synthetic','Person','synthetic person') RETURNING id`, s.tenantA).Scan(&person); err != nil {
					t.Fatal(err)
				}
				if _, err := s.h.Admin.Exec(ctx, `INSERT INTO iam.access_grant(tenant_id,tenant_membership_id,role_id,scope_type,scope_id,valid_period) VALUES ($1,$2,$3,'PERSON',$4,tstzrange(clock_timestamp()-interval '2 days',clock_timestamp()-interval '1 day','[)'))`, s.tenantA, member, memberRole, person); err != nil {
					t.Fatal(err)
				}
			}
			path := "/api/v1/admin/users/" + member.String() + "/role-change-eligibility"
			code, eligibility, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodGet, path, "", "", "")
			if code != 200 || eligibility["canRequestAssignment"] != false || eligibility["assignmentRefusalCode"] != "EXISTING_ACCESS_CONFLICT" {
				t.Fatalf("%s eligibility: %d %v", kind, code, eligibility)
			}
			body := `{"operation":"ASSIGN","roleCode":"PLAN_PUBLISHER","configurationHash":"` + hash + `","reasonCode":"ONBOARDING"}`
			code, out, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost, "/api/v1/admin/users/"+member.String()+"/role-change-requests", `"1"`, "role-change-raw-"+kind+"-0001", body)
			if code != 409 || out["code"] != "EXISTING_ACCESS_CONFLICT" {
				t.Fatalf("%s submission: %d %v", kind, code, out)
			}
			var count int
			if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.role_change_request WHERE tenant_id=$1 AND target_membership_id=$2`, s.tenantA, member).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("refused raw access left proposal")
			}
		})
	}
}

func TestDirectoryRoleChangeDurableReceiptReplaysExactResponseBytes(t *testing.T) {
	f := newRoleChangeRaceFixture(t)
	path := "/api/v1/admin/role-change-requests/" + f.requestID + "/approve"
	const key = "role-change-exact-receipt-0001"
	callApproval := func(body string) *httptest.ResponseRecorder {
		return f.s.do(call{method: http.MethodPost, path: path, cookie: f.checkerCookie, csrf: f.checkerCSRF,
			headers: map[string]string{identityhttp.TenantHeader: f.s.tenantA.String(), "If-Match": f.requestETag, "Idempotency-Key": key}, body: body})
	}
	first := callApproval(`{}`)
	if first.Code != 200 {
		t.Fatalf("first approval: %d %s", first.Code, first.Body.String())
	}
	firstBytes := append([]byte(nil), first.Body.Bytes()...)
	firstETag := first.Header().Get("ETag")
	var receiptStatus int32
	var receiptETag string
	var receiptBody []byte
	ctx := context.Background()
	if err := f.s.h.Admin.QueryRow(ctx, `SELECT response_status,response_etag,response_body FROM iam.role_change_command_receipt WHERE tenant_id=$1 AND request_id=$2 AND command_code='APPROVE'`, f.s.tenantA, uuid.MustParse(f.requestID)).Scan(&receiptStatus, &receiptETag, &receiptBody); err != nil {
		t.Fatal(err)
	}
	if int(receiptStatus) != first.Code || receiptETag != firstETag || !bytes.Equal(receiptBody, firstBytes) {
		t.Fatalf("business receipt differs from first response: status=%d/%d etag=%q/%q", receiptStatus, first.Code, receiptETag, firstETag)
	}
	var checker uuid.UUID
	if err := f.s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.actor WHERE identity_subject='role-change-checker'`).Scan(&checker); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.h.Admin.Exec(ctx, `DELETE FROM system.idempotency_record WHERE tenant_id=$1 AND actor_id=$2 AND command_code='role_change.approve' AND idempotency_key=$3`, f.s.tenantA, checker, key); err != nil {
		t.Fatal(err)
	}
	replay := callApproval(`{}`)
	if replay.Code != first.Code || replay.Header().Get("ETag") != firstETag || replay.Header().Get("Idempotent-Replayed") != "true" || !bytes.Equal(replay.Body.Bytes(), firstBytes) {
		t.Fatalf("durable replay differs: status=%d/%d etag=%q/%q body_equal=%t", replay.Code, first.Code, replay.Header().Get("ETag"), firstETag, bytes.Equal(replay.Body.Bytes(), firstBytes))
	}
	malformed := callApproval(`{} {}`)
	if malformed.Code != 400 || bytes.Equal(malformed.Body.Bytes(), firstBytes) {
		t.Fatalf("malformed changed body replayed receipt: %d %s", malformed.Code, malformed.Body.String())
	}
}

func TestDirectoryRoleChangeReceiptRejectsRevokedAuthorityAndSession(t *testing.T) {
	for _, cause := range []string{"authority", "session"} {
		t.Run(cause, func(t *testing.T) {
			f := newRoleChangeRaceFixture(t)
			ctx := context.Background()
			path := "/api/v1/admin/role-change-requests/" + f.requestID + "/approve"
			key := "role-change-revoked-replay-" + cause + "-0001"
			code, approved, _ := roleCall(f.s, f.checkerCookie, f.checkerCSRF, f.s.tenantA, http.MethodPost, path, f.requestETag, key, `{}`)
			if code != 200 {
				t.Fatalf("approval fixture: %d %v", code, approved)
			}
			var checker uuid.UUID
			if err := f.s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.actor WHERE identity_subject='role-change-checker'`).Scan(&checker); err != nil {
				t.Fatal(err)
			}
			if cause == "authority" {
				if _, err := f.s.h.Admin.Exec(ctx, `UPDATE iam.access_grant g SET valid_period=tstzrange(lower(g.valid_period),clock_timestamp(),'[)') FROM iam.tenant_membership m WHERE g.tenant_id=$1 AND g.tenant_membership_id=m.id AND m.tenant_id=$1 AND m.actor_id=$2 AND g.scope_type='TENANT' AND g.valid_period @> clock_timestamp()`, f.s.tenantA, checker); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.s.h.Admin.Exec(ctx, `UPDATE iam.session SET revoked_at=clock_timestamp() WHERE actor_id=$1 AND revoked_at IS NULL`, checker); err != nil {
					t.Fatal(err)
				}
			}
			status, out, headers := roleCall(f.s, f.checkerCookie, f.checkerCSRF, f.s.tenantA, http.MethodPost, path, f.requestETag, key, `{}`)
			if status == 200 || headers.Get("Idempotent-Replayed") == "true" || out["request"] != nil {
				t.Fatalf("revoked %s received receipt: %d %v", cause, status, out)
			}
		})
	}
}

type failRoleChangeApprovalAudit struct{}

func (failRoleChangeApprovalAudit) Record(_ context.Context, _ pgx.Tx, ev audit.Event) error {
	if ev.ActionCode == "role_change_request.approve" {
		return errors.New("injected role approval audit failure")
	}
	return nil
}
func (failRoleChangeApprovalAudit) RecordAccess(context.Context, pgx.Tx, audit.AccessEvent) error {
	return nil
}

func TestDirectoryRoleChangeApprovalAuditAndReceiptFailuresRollBackApplication(t *testing.T) {
	for _, failure := range []string{"audit", "receipt"} {
		t.Run(failure, func(t *testing.T) {
			var s *authzServer
			if failure == "audit" {
				s = newAuthzServer(t, failRoleChangeApprovalAudit{})
			} else {
				s = newAuthzServer(t)
			}
			makerCookie, makerCSRF, checkerCookie, checkerCSRF := roleChangeManagers(t, s)
			_, target := roleTarget(t, s, "role-change-approval-rollback-"+failure)
			hash := roleChangeOption(t, s, makerCookie, makerCSRF, "PLAN_PUBLISHER")
			path := "/api/v1/admin/users/" + target.String() + "/role-change-requests"
			body := `{"operation":"ASSIGN","roleCode":"PLAN_PUBLISHER","configurationHash":"` + hash + `","reasonCode":"ONBOARDING"}`
			code, created, headers := roleCall(s, makerCookie, makerCSRF, s.tenantA, http.MethodPost, path, `"1"`, "role-change-approval-rollback-create-"+failure, body)
			if code != 201 {
				t.Fatalf("rollback fixture create: %d %v", code, created)
			}
			requestID := uuid.MustParse(created["request"].(map[string]any)["id"].(string))
			ctx := context.Background()
			if failure == "receipt" {
				if _, err := s.h.Admin.Exec(ctx, `ALTER TABLE iam.role_change_command_receipt ADD CONSTRAINT ck_test_fail_approval_receipt CHECK (command_code <> 'APPROVE')`); err != nil {
					t.Fatal(err)
				}
			}
			code, out, _ := roleCall(s, checkerCookie, checkerCSRF, s.tenantA, http.MethodPost, "/api/v1/admin/role-change-requests/"+requestID.String()+"/approve", headers.Get("ETag"), "role-change-approval-rollback-"+failure, `{}`)
			if code != 500 || out["code"] != "INTERNAL_ERROR" {
				t.Fatalf("injected %s failure: %d %v", failure, code, out)
			}
			var status string
			var requestVersion, memberVersion int64
			var grants, approvals, receipts int
			if err := s.h.Admin.QueryRow(ctx, `SELECT status,row_version FROM iam.role_change_request WHERE tenant_id=$1 AND id=$2`, s.tenantA, requestID).Scan(&status, &requestVersion); err != nil {
				t.Fatal(err)
			}
			if err := s.h.Admin.QueryRow(ctx, `SELECT row_version FROM iam.tenant_membership WHERE tenant_id=$1 AND id=$2`, s.tenantA, target).Scan(&memberVersion); err != nil {
				t.Fatal(err)
			}
			if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.access_grant WHERE tenant_id=$1 AND tenant_membership_id=$2`, s.tenantA, target).Scan(&grants); err != nil {
				t.Fatal(err)
			}
			if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM audit.event WHERE tenant_id=$1 AND action_code IN ('role_change_request.approve','access_grant.assign') AND outcome='SUCCESS' AND (resource_id=$2 OR detail_json->>'request_id'=$3)`, s.tenantA, requestID, requestID.String()).Scan(&approvals); err != nil {
				t.Fatal(err)
			}
			if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.role_change_command_receipt WHERE tenant_id=$1 AND request_id=$2 AND command_code='APPROVE'`, s.tenantA, requestID).Scan(&receipts); err != nil {
				t.Fatal(err)
			}
			if status != "PENDING" || requestVersion != 1 || memberVersion != 1 || grants != 0 || approvals != 0 || receipts != 0 {
				t.Fatalf("%s rollback leaked effect: status=%s request_version=%d member_version=%d grants=%d audits=%d receipts=%d", failure, status, requestVersion, memberVersion, grants, approvals, receipts)
			}
		})
	}
}
