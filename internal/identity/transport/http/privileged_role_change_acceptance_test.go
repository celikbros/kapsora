package identityhttp_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	benefitpg "github.com/celikbros/kapsora/internal/benefit/infrastructure/postgres"
	benefithttp "github.com/celikbros/kapsora/internal/benefit/transport/http"
	billingapp "github.com/celikbros/kapsora/internal/billing/application"
	billingpg "github.com/celikbros/kapsora/internal/billing/infrastructure/postgres"
	billinghttp "github.com/celikbros/kapsora/internal/billing/transport/http"
	contractapp "github.com/celikbros/kapsora/internal/contract/application"
	contractpg "github.com/celikbros/kapsora/internal/contract/infrastructure/postgres"
	contracthttp "github.com/celikbros/kapsora/internal/contract/transport/http"
	healthapp "github.com/celikbros/kapsora/internal/health/application"
	healthpg "github.com/celikbros/kapsora/internal/health/infrastructure/postgres"
	healthhttp "github.com/celikbros/kapsora/internal/health/transport/http"
	"github.com/celikbros/kapsora/internal/identity/application"
	identitypg "github.com/celikbros/kapsora/internal/identity/infrastructure/postgres"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

type roleChangeApprovalResult struct {
	status int
	body   map[string]any
}

// A real tenant-row lock keeps the approval transaction outside its business
// checks until the fixture has changed. Merely launching competing goroutines
// would permit either request to finish before the other starts.
func roleChangeApprovalBehindTenantLock(t *testing.T, s *authzServer, cookie *http.Cookie, csrf, requestID, requestETag string, onWait func(context.Context)) roleChangeApprovalResult {
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
	finished := make(chan roleChangeApprovalResult, 1)
	go func() {
		status, body, _ := roleCall(s, cookie, csrf, s.tenantA, http.MethodPost,
			"/api/v1/admin/role-change-requests/"+requestID+"/approve", requestETag,
			"role-change-acceptance-approve-"+uuid.NewString(), `{}`)
		finished <- roleChangeApprovalResult{status, body}
	}()
	deadline := time.Now().Add(8 * time.Second)
	for {
		var waiting bool
		if err := s.h.Admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%platform.tenant%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case got := <-finished:
			t.Fatalf("approval completed before tenant-lock barrier: %d %v", got.status, got.body)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("approval did not wait on tenant lock")
		}
		time.Sleep(15 * time.Millisecond)
	}
	onWait(ctx)
	if err := block.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-finished:
		return got
	case <-time.After(8 * time.Second):
		t.Fatal("approval did not finish after tenant lock release")
		return roleChangeApprovalResult{}
	}
}

func roleChangeWaitUntilGrantExpires(t *testing.T, s *authzServer, grantID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var expired bool
		if err := s.h.Admin.QueryRow(ctx, `SELECT clock_timestamp()>upper(valid_period) FROM iam.access_grant WHERE id=$1`, grantID).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("grant did not naturally expire before barrier deadline")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestDirectoryRoleChangeApprovalRechecksNaturalCheckerAuthorityExpiryAfterWait(t *testing.T) {
	f := newRoleChangeRaceFixture(t)
	ctx := context.Background()
	var checkerGrant uuid.UUID
	if err := f.s.h.Admin.QueryRow(ctx, `SELECT g.id FROM iam.access_grant g JOIN iam.tenant_membership m ON m.tenant_id=g.tenant_id AND m.id=g.tenant_membership_id JOIN iam.actor a ON a.id=m.actor_id JOIN iam.role r ON r.tenant_id=g.tenant_id AND r.id=g.role_id WHERE g.tenant_id=$1 AND a.identity_subject='role-change-checker' AND r.code='TENANT_ADMIN'`, f.s.tenantA).Scan(&checkerGrant); err != nil {
		t.Fatal(err)
	}
	got := roleChangeApprovalBehindTenantLock(t, f.s, f.checkerCookie, f.checkerCSRF, f.requestID, f.requestETag, func(ctx context.Context) {
		if _, err := f.s.h.Admin.Exec(ctx, `UPDATE iam.access_grant SET valid_period=tstzrange(lower(valid_period),clock_timestamp()+interval '2 seconds','[)') WHERE id=$1`, checkerGrant); err != nil {
			t.Fatal(err)
		}
		roleChangeWaitUntilGrantExpires(t, f.s, checkerGrant)
	})
	if got.status != 403 || got.body["code"] != "PERMISSION_DENIED" {
		t.Fatalf("expired checker authority applied approval: %d %v", got.status, got.body)
	}
	var status string
	var grants int
	if err := f.s.h.Admin.QueryRow(ctx, `SELECT status FROM iam.role_change_request WHERE tenant_id=$1 AND id=$2`, f.s.tenantA, uuid.MustParse(f.requestID)).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := f.s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.access_grant WHERE tenant_id=$1 AND tenant_membership_id=$2`, f.s.tenantA, f.targetMember).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if status != "PENDING" || grants != 0 {
		t.Fatalf("expired checker changed request or grant: status=%s grants=%d", status, grants)
	}
}

func TestDirectoryRoleChangeApprovalRechecksNaturalMakerAuthorityExpiryAfterWait(t *testing.T) {
	f := newRoleChangeRaceFixture(t)
	ctx := context.Background()
	var makerGrant uuid.UUID
	if err := f.s.h.Admin.QueryRow(ctx, `SELECT g.id FROM iam.access_grant g JOIN iam.role r ON r.tenant_id=g.tenant_id AND r.id=g.role_id JOIN iam.tenant_membership m ON m.tenant_id=g.tenant_id AND m.id=g.tenant_membership_id WHERE g.tenant_id=$1 AND m.actor_id=$2 AND r.code='TENANT_ADMIN'`, f.s.tenantA, f.s.actor).Scan(&makerGrant); err != nil {
		t.Fatal(err)
	}
	got := roleChangeApprovalBehindTenantLock(t, f.s, f.checkerCookie, f.checkerCSRF, f.requestID, f.requestETag, func(ctx context.Context) {
		if _, err := f.s.h.Admin.Exec(ctx, `UPDATE iam.access_grant SET valid_period=tstzrange(lower(valid_period),clock_timestamp()+interval '2 seconds','[)') WHERE id=$1`, makerGrant); err != nil {
			t.Fatal(err)
		}
		roleChangeWaitUntilGrantExpires(t, f.s, makerGrant)
	})
	if got.status != 409 || got.body["code"] != "ROLE_CHANGE_MAKER_UNAUTHORIZED" {
		t.Fatalf("naturally expired maker approved request: %d %v", got.status, got.body)
	}
	var status string
	var grants int
	if err := f.s.h.Admin.QueryRow(ctx, `SELECT status FROM iam.role_change_request WHERE tenant_id=$1 AND id=$2`, f.s.tenantA, uuid.MustParse(f.requestID)).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := f.s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.access_grant WHERE tenant_id=$1 AND tenant_membership_id=$2`, f.s.tenantA, f.targetMember).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if status != "PENDING" || grants != 0 {
		t.Fatalf("expired maker changed request or grant: status=%s grants=%d", status, grants)
	}
}

func TestDirectoryRoleChangeApprovalRechecksTargetValidityAndSensitivityAfterWait(t *testing.T) {
	for _, kind := range []string{"target-validity", "permission-sensitivity"} {
		t.Run(kind, func(t *testing.T) {
			f := newRoleChangeRaceFixture(t)
			got := roleChangeApprovalBehindTenantLock(t, f.s, f.checkerCookie, f.checkerCSRF, f.requestID, f.requestETag, func(ctx context.Context) {
				var err error
				if kind == "target-validity" {
					// Membership validity is a daterange. A deterministic CI barrier cannot
					// cross midnight, so move its end while approval is actually waiting.
					_, err = f.s.h.Admin.Exec(ctx, `UPDATE iam.tenant_membership SET valid_period=daterange(CURRENT_DATE-1,CURRENT_DATE,'[)') WHERE tenant_id=$1 AND id=$2`, f.s.tenantA, f.targetMember)
				} else {
					_, err = f.s.h.Admin.Exec(ctx, `UPDATE iam.permission SET sensitivity=CASE WHEN sensitivity='PRIVILEGED' THEN 'SENSITIVE' ELSE 'PRIVILEGED' END WHERE code='plan.publish'`)
				}
				if err != nil {
					t.Fatal(err)
				}
			})
			want := "ROLE_CHANGE_TARGET_CHANGED"
			if kind == "permission-sensitivity" {
				want = "ROLE_CHANGE_CONFIGURATION_CHANGED"
			}
			if got.status != 409 || got.body["code"] != want {
				t.Fatalf("%s post-wait approval: %d %v", kind, got.status, got.body)
			}
			var status string
			var grants int
			if err := f.s.h.Admin.QueryRow(context.Background(), `SELECT status FROM iam.role_change_request WHERE tenant_id=$1 AND id=$2`, f.s.tenantA, uuid.MustParse(f.requestID)).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if err := f.s.h.Admin.QueryRow(context.Background(), `SELECT count(*) FROM iam.access_grant WHERE tenant_id=$1 AND tenant_membership_id=$2`, f.s.tenantA, f.targetMember).Scan(&grants); err != nil {
				t.Fatal(err)
			}
			if status != "PENDING" || grants != 0 {
				t.Fatalf("%s applied after drift: status=%s grants=%d", kind, status, grants)
			}
		})
	}
}

func TestDirectoryRoleChangeReceiptSurvivesGenericInProgressCompletionGap(t *testing.T) {
	f := newRoleChangeRaceFixture(t)
	path := "/api/v1/admin/role-change-requests/" + f.requestID + "/approve"
	const key = "role-change-committed-in-progress-0001"
	command := func() (int, []byte, string, string) {
		rec := f.s.do(call{method: http.MethodPost, path: path, cookie: f.checkerCookie, csrf: f.checkerCSRF,
			headers: map[string]string{identityhttp.TenantHeader: f.s.tenantA.String(), "If-Match": f.requestETag, "Idempotency-Key": key}, body: `{}`})
		return rec.Code, append([]byte(nil), rec.Body.Bytes()...), rec.Header().Get("ETag"), rec.Header().Get("Idempotent-Replayed")
	}
	status, body, etag, _ := command()
	if status != 200 || etag == "" {
		t.Fatalf("initial approval: %d %s ETag=%q", status, body, etag)
	}
	ctx := context.Background()
	var checker uuid.UUID
	if err := f.s.h.Admin.QueryRow(ctx, `SELECT id FROM iam.actor WHERE identity_subject='role-change-checker'`).Scan(&checker); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := f.s.h.Admin.QueryRow(ctx, `UPDATE system.idempotency_record SET status='IN_PROGRESS',completed_at=NULL,response_status=NULL,response_body=NULL,created_at=clock_timestamp() WHERE tenant_id=$1 AND actor_id=$2 AND command_code='role_change.approve' AND idempotency_key=$3 RETURNING 1`, f.s.tenantA, checker, key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	replayStatus, replayBody, replayETag, replayed := command()
	if replayStatus != status || !bytes.Equal(replayBody, body) || replayETag != etag || replayed != "true" {
		t.Fatalf("committed receipt through IN_PROGRESS: status=%d/%d ETag=%q/%q replay=%q bodies equal=%t", replayStatus, status, replayETag, etag, replayed, bytes.Equal(replayBody, body))
	}
	var grants, successAudits, versions int
	if err := f.s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.access_grant WHERE tenant_id=$1 AND tenant_membership_id=$2`, f.s.tenantA, f.targetMember).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if err := f.s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM audit.event WHERE tenant_id=$1 AND resource_id=$2 AND action_code='role_change_request.approve' AND outcome='SUCCESS'`, f.s.tenantA, uuid.MustParse(f.requestID)).Scan(&successAudits); err != nil {
		t.Fatal(err)
	}
	if err := f.s.h.Admin.QueryRow(ctx, `SELECT row_version FROM iam.tenant_membership WHERE tenant_id=$1 AND id=$2`, f.s.tenantA, f.targetMember).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if grants != 1 || successAudits != 1 || versions != 2 {
		t.Fatalf("receipt replay duplicated effect: grants=%d audits=%d version=%d", grants, successAudits, versions)
	}
}

func roleChangeCreateRevokeFixture(t *testing.T, s *authzServer, makerCookie *http.Cookie, makerCSRF string, member, grantID uuid.UUID, role string) (string, string) {
	t.Helper()
	hash := roleChangeOption(t, s, makerCookie, makerCSRF, role)
	path := "/api/v1/admin/users/" + member.String()
	code, _, current := roleCall(s, makerCookie, makerCSRF, s.tenantA, http.MethodGet, path+"/role-change-eligibility", "", "", "")
	if code != 200 {
		t.Fatalf("revoke eligibility: %d", code)
	}
	body := fmt.Sprintf(`{"operation":"REVOKE","grantId":"%s","configurationHash":"%s","reasonCode":"DUTY_ENDED"}`, grantID, hash)
	code, created, headers := roleCall(s, makerCookie, makerCSRF, s.tenantA, http.MethodPost, path+"/role-change-requests", current.Get("ETag"), "role-change-revoke-create-"+uuid.NewString(), body)
	if code != 201 {
		t.Fatalf("revoke request: %d %v", code, created)
	}
	return created["request"].(map[string]any)["id"].(string), headers.Get("ETag")
}

// Direct fixture writes shape a finite historical grant; the revoke and its
// checker decision travel only through the public role-change commands.
func roleChangeFiniteGrant(t *testing.T, s *authzServer, member uuid.UUID, role, bounds string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := s.h.Admin.QueryRow(context.Background(), `INSERT INTO iam.access_grant(tenant_id,tenant_membership_id,role_id,scope_type,valid_period) SELECT $1,$2,r.id,'TENANT',tstzrange(clock_timestamp()-interval '2 days',clock_timestamp()+interval '1 day',$4) FROM iam.role r WHERE r.tenant_id=$1 AND r.code=$3 RETURNING id`, s.tenantA, member, role, bounds).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDirectoryRoleChangeRevokeRechecksNaturalGrantExpiryAfterWait(t *testing.T) {
	s := newAuthzServer(t)
	makerCookie, makerCSRF, checkerCookie, checkerCSRF := roleChangeManagers(t, s)
	_, member := roleTarget(t, s, "role-change-revoke-expiry-"+uuid.NewString())
	grantID := roleChangeFiniteGrant(t, s, member, "PLAN_PUBLISHER", "[)")
	if _, err := s.h.Admin.Exec(context.Background(), `UPDATE iam.access_grant SET valid_period=tstzrange(lower(valid_period),clock_timestamp()+interval '6 seconds','[)') WHERE id=$1`, grantID); err != nil {
		t.Fatal(err)
	}
	requestID, etag := roleChangeCreateRevokeFixture(t, s, makerCookie, makerCSRF, member, grantID, "PLAN_PUBLISHER")
	got := roleChangeApprovalBehindTenantLock(t, s, checkerCookie, checkerCSRF, requestID, etag, func(ctx context.Context) {
		roleChangeWaitUntilGrantExpires(t, s, grantID)
	})
	if got.status != 409 || got.body["code"] != "ROLE_CHANGE_TARGET_CHANGED" {
		t.Fatalf("expired revoke grant approved: %d %v", got.status, got.body)
	}
	var status string
	if err := s.h.Admin.QueryRow(context.Background(), `SELECT status FROM iam.role_change_request WHERE tenant_id=$1 AND id=$2`, s.tenantA, uuid.MustParse(requestID)).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "PENDING" {
		t.Fatalf("expired revoke grant transitioned request to %s", status)
	}
}

func TestDirectoryRoleChangePublicRevokePreservesFiniteExclusiveLowerBound(t *testing.T) {
	s := newAuthzServer(t)
	makerCookie, makerCSRF, checkerCookie, checkerCSRF := roleChangeManagers(t, s)
	name := "role-change-revoke-lower-" + uuid.NewString()
	_, member := roleTarget(t, s, name)
	grantID := roleChangeFiniteGrant(t, s, member, "RULE_APPROVER", "()")
	login := s.do(call{method: http.MethodPost, path: "/api/v1/session/login", body: fmt.Sprintf(`{"username":"%s","password":"%s"}`, name, testPassword)})
	if login.Code != 200 {
		t.Fatalf("target login: %d %s", login.Code, login.Body.String())
	}
	targetCookie := sessionCookie(t, login, s.cookies.Name())
	targetCSRF := decodeBody(t, login)["csrfToken"].(string)
	switchRec := s.do(call{method: http.MethodPost, path: "/api/v1/session/switch-tenant", cookie: targetCookie, csrf: targetCSRF,
		body: `{"tenantId":"` + s.tenantA.String() + `"}`})
	if switchRec.Code != 200 {
		t.Fatalf("target tenant switch: %d %s", switchRec.Code, switchRec.Body.String())
	}
	ruleRead := func() int {
		return s.do(call{method: http.MethodGet, path: "/api/v1/rule-sets/", cookie: targetCookie,
			headers: map[string]string{identityhttp.TenantHeader: s.tenantA.String(), "X-Kapsora-App": "backoffice"}}).Code
	}
	if got := ruleRead(); got != 200 {
		t.Fatalf("effective finite RULE_APPROVER cannot read real rule list: %d", got)
	}
	ctx := context.Background()
	var lowerBefore, upperBefore time.Time
	var lowerInclusiveBefore, upperInclusiveBefore bool
	if err := s.h.Admin.QueryRow(ctx, `SELECT lower(valid_period),upper(valid_period),lower_inc(valid_period),upper_inc(valid_period) FROM iam.access_grant WHERE id=$1`, grantID).Scan(&lowerBefore, &upperBefore, &lowerInclusiveBefore, &upperInclusiveBefore); err != nil {
		t.Fatal(err)
	}
	if lowerInclusiveBefore || upperInclusiveBefore {
		t.Fatal("fixture grant did not have exclusive finite bounds")
	}
	requestID, etag := roleChangeCreateRevokeFixture(t, s, makerCookie, makerCSRF, member, grantID, "RULE_APPROVER")
	status, approved, _ := roleCall(s, checkerCookie, checkerCSRF, s.tenantA, http.MethodPost,
		"/api/v1/admin/role-change-requests/"+requestID+"/approve", etag,
		"role-change-finite-revoke-"+uuid.NewString(), `{}`)
	if status != 200 || approved["request"].(map[string]any)["status"] != "APPROVED" || approved["appliedGrant"].(map[string]any)["id"] != grantID.String() {
		t.Fatalf("finite public revoke: %d %v", status, approved)
	}
	var lowerAfter, upperAfter time.Time
	var lowerInclusiveAfter, upperInclusiveAfter, empty bool
	if err := s.h.Admin.QueryRow(ctx, `SELECT lower(valid_period),upper(valid_period),lower_inc(valid_period),upper_inc(valid_period),isempty(valid_period) FROM iam.access_grant WHERE id=$1`, grantID).Scan(&lowerAfter, &upperAfter, &lowerInclusiveAfter, &upperInclusiveAfter, &empty); err != nil {
		t.Fatal(err)
	}
	if empty || !lowerAfter.Equal(lowerBefore) || lowerInclusiveAfter || upperInclusiveAfter || !upperAfter.Before(upperBefore) || !upperAfter.After(lowerBefore) {
		t.Fatalf("revoke changed finite exclusive lower bound: before=(%s,%s) after=(%s,%s) inclusive=%t/%t empty=%t", lowerBefore, upperBefore, lowerAfter, upperAfter, lowerInclusiveAfter, upperInclusiveAfter, empty)
	}
	var current bool
	if err := s.h.Admin.QueryRow(ctx, `SELECT valid_period @> clock_timestamp() FROM iam.access_grant WHERE id=$1`, grantID).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if current {
		t.Fatal("approved revoke left grant currently effective")
	}
	if got := ruleRead(); got != 403 {
		t.Fatalf("revoked target retained real rule read on next request: %d", got)
	}
	me := decodeBody(t, s.do(call{method: http.MethodGet, path: "/api/v1/me", cookie: targetCookie}))
	for _, raw := range me["tenants"].([]any) {
		entry := raw.(map[string]any)
		if entry["tenant"].(map[string]any)["id"] == s.tenantA.String() && len(entry["apps"].([]any)) != 0 {
			t.Fatalf("revoked target retained app context: %v", entry["apps"])
		}
	}
}

// This second router mounts the production benefit, contract, billing and
// health readers with the same session/tenant middleware as newAuthzServer.
// The base fixture already mounts the production rules reader.
func roleChangeDownstreamReader(t *testing.T, s *authzServer) http.Handler {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cursors, err := httpx.NewCursorCodec(signingKey)
	if err != nil {
		t.Fatal(err)
	}
	sessions := identitypg.NewSessionStore(s.h.App)
	sink := identitypg.NewAuditSink(s.h.App, auditpg.New(), logger)
	authz := application.NewAuthorizer(identitypg.NewAuthorizationRepository(s.h.App), sessions, sink, nil)
	mw := identityhttp.NewMiddleware(s.svc, s.cookies, signingKey, logger).WithAuthorizer(authz)
	benefitService, err := benefitapp.New(benefitapp.Deps{Pool: s.h.App, Repo: benefitpg.New(), Cursors: cursors})
	if err != nil {
		t.Fatal(err)
	}
	contractService, err := contractapp.New(contractapp.Deps{Pool: s.h.App, Repo: contractpg.New(), Cursors: cursors})
	if err != nil {
		t.Fatal(err)
	}
	billingService, err := billingapp.New(billingapp.Deps{Pool: s.h.App, Repo: billingpg.New(), Settlements: billingpg.NewSettlementRepository(), Cursors: cursors})
	if err != nil {
		t.Fatal(err)
	}
	healthService, err := healthapp.New(healthapp.Deps{Pool: s.h.App, Repo: healthpg.New(), Reports: healthpg.NewReports(), Cursors: cursors})
	if err != nil {
		t.Fatal(err)
	}
	benefitHandler := benefithttp.NewHandler(benefitService, mw, logger)
	contractHandler := contracthttp.NewHandler(contractService, mw, logger)
	billingHandler := billinghttp.NewHandler(billingService, mw, logger)
	healthHandler := healthhttp.NewHandler(healthService, mw, logger)
	r := chi.NewRouter()
	r.Route("/api/v1", func(api chi.Router) {
		api.Use(mw.LoadSession)
		api.Group(func(tenant chi.Router) {
			tenant.Use(mw.RequireCSRF)
			tenant.Use(mw.RequireTenantContext)
			tenant.Route("/programs", func(rr chi.Router) { benefitHandler.ProgramRoutes(rr, benefithttp.Middlewares{}) })
			tenant.Route("/contracts", func(rr chi.Router) { contractHandler.ContractRoutes(rr, contracthttp.Middlewares{}) })
			tenant.Route("/settlements", func(rr chi.Router) { billingHandler.SettlementRoutes(rr, billinghttp.SettlementMiddlewares{}) })
			tenant.Route("/health-cases", func(rr chi.Router) { healthHandler.CaseRoutes(rr, healthhttp.Middlewares{}) })
		})
	})
	return r
}

func roleChangeDownstreamRead(s *authzServer, handler http.Handler, cookie *http.Cookie, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(cookie)
	req.Header.Set(identityhttp.TenantHeader, s.tenantA.String())
	req.Header.Set("X-Kapsora-App", "backoffice")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestDirectoryRoleChangeEveryProtectedRoleOpensOrdinaryContextAndRealRead(t *testing.T) {
	s := newAuthzServer(t)
	makerCookie, makerCSRF, checkerCookie, checkerCSRF := roleChangeManagers(t, s)
	downstream := roleChangeDownstreamReader(t, s)
	for _, tc := range []struct{ role, path string }{
		{"TENANT_ADMIN", "/api/v1/rule-sets/"},
		{"PLAN_PUBLISHER", "/api/v1/programs/"},
		{"CONTRACT_PUBLISHER", "/api/v1/contracts/"},
		{"RULE_APPROVER", "/api/v1/rule-sets/"},
		{"PAYER_APPROVER", "/api/v1/settlements/"},
	} {
		t.Run(tc.role, func(t *testing.T) {
			name := "role-change-read-" + uuid.NewString()
			_, member := roleTarget(t, s, name)
			login := s.do(call{method: http.MethodPost, path: "/api/v1/session/login", body: fmt.Sprintf(`{"username":"%s","password":"%s"}`, name, testPassword)})
			if login.Code != 200 {
				t.Fatalf("target login: %d %s", login.Code, login.Body.String())
			}
			targetCookie := sessionCookie(t, login, s.cookies.Name())
			targetCSRF := decodeBody(t, login)["csrfToken"].(string)
			switchRec := s.do(call{method: http.MethodPost, path: "/api/v1/session/switch-tenant", cookie: targetCookie, csrf: targetCSRF,
				body: `{"tenantId":"` + s.tenantA.String() + `"}`})
			if switchRec.Code != 200 {
				t.Fatalf("target tenant switch: %d %s", switchRec.Code, switchRec.Body.String())
			}
			read := func(path string) *httptest.ResponseRecorder {
				if path == "/api/v1/rule-sets/" {
					return s.do(call{method: http.MethodGet, path: path, cookie: targetCookie,
						headers: map[string]string{identityhttp.TenantHeader: s.tenantA.String(), "X-Kapsora-App": "backoffice"}})
				}
				return roleChangeDownstreamRead(s, downstream, targetCookie, path)
			}
			if rec := read(tc.path); rec.Code != 403 {
				t.Fatalf("zero-grant target read %s: %d %s", tc.path, rec.Code, rec.Body.String())
			}
			hash := roleChangeOption(t, s, makerCookie, makerCSRF, tc.role)
			body := fmt.Sprintf(`{"operation":"ASSIGN","roleCode":"%s","configurationHash":"%s","reasonCode":"DUTY_ASSIGNMENT"}`, tc.role, hash)
			path := "/api/v1/admin/users/" + member.String() + "/role-change-requests"
			code, created, headers := roleCall(s, makerCookie, makerCSRF, s.tenantA, http.MethodPost, path, `"1"`, "role-change-read-create-"+uuid.NewString(), body)
			if code != 201 {
				t.Fatalf("%s create: %d %v", tc.role, code, created)
			}
			requestID := created["request"].(map[string]any)["id"].(string)
			code, approved, _ := roleCall(s, checkerCookie, checkerCSRF, s.tenantA, http.MethodPost,
				"/api/v1/admin/role-change-requests/"+requestID+"/approve", headers.Get("ETag"), "role-change-read-approve-"+uuid.NewString(), `{}`)
			if code != 200 || approved["request"].(map[string]any)["status"] != "APPROVED" {
				t.Fatalf("%s approve: %d %v", tc.role, code, approved)
			}
			me := decodeBody(t, s.do(call{method: http.MethodGet, path: "/api/v1/me", cookie: targetCookie}))
			found := false
			for _, raw := range me["tenants"].([]any) {
				entry := raw.(map[string]any)
				if entry["tenant"].(map[string]any)["id"] == s.tenantA.String() {
					found = true
					if len(entry["apps"].([]any)) == 0 {
						t.Fatalf("%s approved but ordinary context still waits", tc.role)
					}
				}
			}
			if !found {
				t.Fatalf("%s target tenant absent from /me", tc.role)
			}
			if rec := read(tc.path); rec.Code != 200 {
				t.Fatalf("%s real read %s: %d %s", tc.role, tc.path, rec.Code, rec.Body.String())
			}
			if tc.role == "TENANT_ADMIN" {
				if rec := read("/api/v1/health-cases/"); rec.Code != 403 {
					t.Fatalf("TENANT_ADMIN reached clinical case route: %d %s", rec.Code, rec.Body.String())
				}
			}
		})
	}
}
