// The inpatient stay, driven end to end against a real database: the admission is asked for
// through the real preauthorization gate, decided through the real service request commands,
// and the stay follows through the real outbox event. Nothing below stubs the modules this
// package leans on, because every property the work package asks for is a property of the
// join between them — a fake authorization would prove the release happened in a fake ledger.
package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	authorizationapp "github.com/celikbros/kapsora/internal/authorization/application"
	authorizationpg "github.com/celikbros/kapsora/internal/authorization/infrastructure/postgres"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	"github.com/celikbros/kapsora/internal/health/application"
	"github.com/celikbros/kapsora/internal/health/domain"
	healthgw "github.com/celikbros/kapsora/internal/health/infrastructure/gateway"
	healthpg "github.com/celikbros/kapsora/internal/health/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/outbox"
	servicerequestapp "github.com/celikbros/kapsora/internal/servicerequest/application"
	servicerequestdomain "github.com/celikbros/kapsora/internal/servicerequest/domain"
	servicerequestpg "github.com/celikbros/kapsora/internal/servicerequest/infrastructure/postgres"
)

// dayGrant is what the INPATIENT_DAY account is opened with. It is generous on purpose: this
// file is about day counts and releases, not about running out of entitlement.
const dayGrant = "60"

type stayFixture struct {
	h    *dbtest.Harness
	pool *pgxpool.Pool

	health   *application.Service
	requests *servicerequestapp.Service

	tenant     uuid.UUID
	actor      uuid.UUID
	person     uuid.UUID
	program    uuid.UUID
	enrollment uuid.UUID
	provider   uuid.UUID
	otherOrg   uuid.UUID
	definition uuid.UUID
	account    uuid.UUID

	// now is the clock every service in the fixture reads. Tests move it forward rather
	// than sleeping, so a three-day admission takes no wall time and the arithmetic under
	// test is the arithmetic that ran.
	mu  sync.Mutex
	now time.Time
}

// stayNow is where the fixture's clock starts. It is a Monday morning, which matters only
// in that every window in these tests is then deterministic.
var stayNow = time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)

func newStayFixture(t *testing.T) *stayFixture {
	t.Helper()
	h := dbtest.New(t)

	// The concurrency test needs one connection per goroutine in flight; the harness pool
	// is deliberately small, so this one is opened against the same app role.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := db.NewPool(ctx, h.AppURL, db.PoolOptions{ApplicationName: "stay-test", MaxConns: 24})
	if err != nil {
		t.Fatalf("app pool: %v", err)
	}
	t.Cleanup(pool.Close)

	cursors, err := httpx.NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	f := &stayFixture{h: h, pool: pool, now: stayNow}
	clock := func() time.Time {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.now
	}

	entitlements, err := ledger.New(ledger.Deps{Pool: pool, Audit: auditpg.New(), Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	authorizations, err := authorizationapp.New(authorizationapp.Deps{
		Pool: pool, Repo: authorizationpg.New(), Ledger: entitlements.Ledger(),
		Audit: auditpg.New(), Cursors: cursors, Logger: logger, Now: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	requests, err := servicerequestapp.New(servicerequestapp.Deps{
		Pool: pool, Repo: servicerequestpg.New(), Audit: auditpg.New(),
		Cursors: cursors, Logger: logger, Now: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	health, err := application.New(application.Deps{
		Pool: pool, Repo: healthpg.New(), Reports: healthpg.NewReports(),
		StayRepo: healthpg.NewStays(), Requests: healthgw.NewRequests(requests),
		Authorizations: healthgw.NewAuthorizations(authorizations),
		Audit:          auditpg.New(), Cursors: cursors, Logger: logger, Now: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.health, f.requests = health, requests
	f.seed(t)
	return f
}

// advance moves the fixture's clock forward by whole days.
func (f *stayFixture) advance(days int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.AddDate(0, 0, days)
}

func (f *stayFixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *stayFixture) seed(t *testing.T) { //nolint:funlen // one linear fixture reads better whole
	t.Helper()
	h := f.h
	ctx, cancel := h.Ctx()
	defer cancel()
	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		t.Helper()
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}

	f.tenant = h.CreateTenant("INPATIENT")
	f.actor = h.CreateActor("inpatient-clerk", "Inpatient Clerk")
	sponsor := h.CreateTenantOrganization(f.tenant, "Stay Sponsor", "SPONSOR")
	payer := h.CreateTenantOrganization(f.tenant, "Stay Payer", "PAYER")
	f.provider = h.CreateTenantOrganization(f.tenant, "Stay Hospital", "PROVIDER")
	f.otherOrg = h.CreateTenantOrganization(f.tenant, "Other Hospital", "PROVIDER")

	h.AdminExec(`INSERT INTO party.membership_type (tenant_id, code, display_name) VALUES ($1, 'MEMBER', 'Üye')`, f.tenant)
	scan(&f.person, "person", `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Eylül', 'Barın', 'eylul barin') RETURNING id`, f.tenant)
	var membership uuid.UUID
	scan(&membership, "membership", `
		INSERT INTO party.sponsor_membership (tenant_id, person_id, sponsor_tenant_organization_id,
		                                      membership_type, status, valid_period)
		VALUES ($1, $2, $3, 'MEMBER', 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, f.person, sponsor)

	h.AdminExec(`INSERT INTO benefit.program_type (tenant_id, code, display_name) VALUES ($1, 'BENEFIT', 'Fayda')`, f.tenant)
	scan(&f.program, "program", `
		INSERT INTO benefit.program (tenant_id, sponsor_tenant_organization_id, payer_tenant_organization_id,
		                             code, name, program_type, status, valid_period)
		VALUES ($1, $2, $3, 'PRG', 'Program', 'BENEFIT', 'ACTIVE', daterange('2026-01-01', NULL, '[)'))
		RETURNING id`, f.tenant, sponsor, payer)
	var planID, planVersion uuid.UUID
	scan(&planID, "plan", `
		INSERT INTO benefit.plan (tenant_id, program_id, code, name, status)
		VALUES ($1, $2, 'PLAN', 'Plan', 'ACTIVE') RETURNING id`, f.tenant, f.program)
	scan(&planVersion, "plan version", `
		INSERT INTO benefit.plan_version (tenant_id, plan_id, version_no, status, valid_period,
		                                  published_at, published_by)
		VALUES ($1, $2, 1, 'PUBLISHED', daterange('2026-01-01','2027-01-01','[)'), clock_timestamp(), $3)
		RETURNING id`, f.tenant, planID, f.actor)
	scan(&f.enrollment, "enrollment", `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status, valid_period)
		VALUES ($1, $2, $3, 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, membership, planID)

	var category uuid.UUID
	scan(&category, "service category", `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'HEALTH_ROOT', 'Sağlık', 'HEALTH') RETURNING id`, f.tenant)
	// The admission service, under the code the module looks it up by. A tenant without it
	// cannot admit anybody, which is the refusal the create command answers.
	scan(&f.definition, "admission service", `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name,
		                                        fulfillment_mode, default_unit_type, requires_provider)
		VALUES ($1, $2, 'INPATIENT_DAY', 'Yatak günü', 'DIRECT', 'NIGHT', true) RETURNING id`,
		f.tenant, category)

	// The entitlement the admission draws on. Its code matches the service definition's,
	// which is how WP-I4-02 maps a line onto a balance.
	var definitionID uuid.UUID
	scan(&definitionID, "entitlement definition", `
		INSERT INTO benefit.entitlement_definition (tenant_id, plan_version_id, code, name, unit_type,
		                                            period_type, initial_quantity)
		VALUES ($1, $2, 'INPATIENT_DAY', 'Yatak günü', 'NIGHT', 'CALENDAR_YEAR', $3::text::numeric)
		RETURNING id`, f.tenant, planVersion, dayGrant)
	scan(&f.account, "entitlement account", `
		INSERT INTO benefit.entitlement_account (tenant_id, enrollment_id, entitlement_definition_id,
		                                         benefit_period, total_granted, available_quantity)
		VALUES ($1, $2, $3, daterange('2026-01-01','2027-01-01','[)'), $4::text::numeric, $4::text::numeric)
		RETURNING id`, f.tenant, f.enrollment, definitionID, dayGrant)
	h.AdminExec(`
		INSERT INTO benefit.entitlement_ledger (tenant_id, entitlement_account_id, movement_type,
		                                        effective_at, delta_total, delta_available,
		                                        reference_type, reference_id, idempotency_key)
		VALUES ($1, $2, 'GRANT', clock_timestamp(), $3::text::numeric, $3::text::numeric,
		        'ENROLLMENT', $4, 'grant:seed:inpatient')`,
		f.tenant, f.account, dayGrant, f.enrollment)
}

// newCase opens a health case for the member, directly. The case is WP-I5-01's aggregate and
// driving its own command here would only test that package twice.
func (f *stayFixture) newCase(t *testing.T, provider uuid.UUID) uuid.UUID {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var id uuid.UUID
	err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO health.health_case (tenant_id, person_id, program_id, enrollment_id, case_type,
		                                provider_organization_id, opened_at)
		VALUES ($1, $2, $3, $4, 'INPATIENT', $5, $6) RETURNING id`,
		f.tenant, f.person, f.program, f.enrollment, provider, f.clock()).Scan(&id)
	if err != nil {
		t.Fatalf("seed case: %v", err)
	}
	return id
}

// rc is a provider clerk holding what section 2.5 says an admission needs and nothing more.
func (f *stayFixture) rc() identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal: identity.Principal{ActorID: f.actor},
		Permissions: map[string]struct{}{
			application.PermissionCaseRead: {}, application.PermissionCaseManage: {},
			application.PermissionClinicalRead: {},
		},
	}
}

// createStay asks for an admission of the given length.
func (f *stayFixture) createStay(t *testing.T, caseID uuid.UUID, days int, admissionAt time.Time) (application.StayView, error) {
	t.Helper()
	return f.health.CreateStay(context.Background(), f.rc(), application.NewStayInput{
		CaseID: caseID, ProviderOrganizationID: f.provider,
		AdmissionAt: admissionAt, EstimatedDays: days,
	}, application.AccessRequest{})
}

func (f *stayFixture) mustCreateStay(t *testing.T, caseID uuid.UUID, days int) application.StayView {
	t.Helper()
	view, err := f.createStay(t, caseID, days, f.clock())
	if err != nil {
		t.Fatalf("create stay: %v", err)
	}
	return view
}

// reviewerRC is the medical reviewer: they decide requests and know nothing about stays.
func (f *stayFixture) reviewerRC() identity.RequestContext {
	rc := f.rc()
	rc.Permissions = map[string]struct{}{
		servicerequestapp.PermissionRead: {}, servicerequestapp.PermissionReview: {},
	}
	return rc
}

// decide approves, partially approves or rejects a request exactly as a reviewer would on the
// request page, and then delivers whatever the decision published. The two halves are one
// helper on purpose: a test that called the handler directly would still pass if the publish
// were deleted, and the publish is half of what section 2.2 asks for.
func (f *stayFixture) decide(t *testing.T, requestID uuid.UUID, command, approvedQuantity string) {
	t.Helper()
	ctx := context.Background()
	rc := f.reviewerRC()
	current, err := f.requests.Get(ctx, rc, requestID)
	if err != nil {
		t.Fatalf("read request: %v", err)
	}
	switch command {
	case "APPROVE":
		_, err = f.requests.Approve(ctx, rc, requestID, servicerequestapp.DecisionInput{
			ReasonCode: "MEDICALLY_NECESSARY", ExpectedVersion: current.Request.RowVersion,
		})
	case "PARTIAL":
		_, err = f.requests.PartiallyApprove(ctx, rc, requestID, servicerequestapp.DecisionInput{
			ReasonCode: "SHORTER_STAY_SUFFICIENT",
			Items: []servicerequestdomain.DecisionItem{{
				LineNo: 1, Status: servicerequestdomain.ItemPartiallyApproved,
				ApprovedQuantity: approvedQuantity,
			}},
			ExpectedVersion: current.Request.RowVersion,
		})
	case "REJECT":
		_, err = f.requests.Reject(ctx, rc, requestID, servicerequestapp.ReasonInput{
			ReasonCode: "NOT_MEDICALLY_NECESSARY", ExpectedVersion: current.Request.RowVersion,
		})
	default:
		t.Fatalf("unknown decision %q", command)
	}
	if err != nil {
		t.Fatalf("%s request: %v", command, err)
	}
	f.deliverDecisions(t)
}

// deliverDecisions plays the worker: it reads the decided-request events the commands above
// actually wrote and hands each of them to the subscription, exactly as kapsora-worker's
// dispatcher would. Reading them out of system.outbox_event rather than fabricating one is
// what makes this test fail if the publish is removed.
func (f *stayFixture) deliverDecisions(t *testing.T) {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	rows, err := f.h.Admin.Query(ctx, `
		SELECT id, tenant_id, aggregate_type, aggregate_id, event_type, event_schema_version,
		       payload_json, occurred_at
		  FROM system.outbox_event
		 WHERE event_type = $1 AND status = 'PENDING'
		 ORDER BY occurred_at, id`, servicerequestapp.DecidedEvent)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	var deliveries []outbox.Delivery
	for rows.Next() {
		var d outbox.Delivery
		var payload []byte
		var version int32
		if err := rows.Scan(&d.ID, &d.TenantID, &d.AggregateType, &d.AggregateID, &d.Type,
			&version, &payload, &d.OccurredAt); err != nil {
			rows.Close()
			t.Fatalf("scan outbox row: %v", err)
		}
		d.SchemaVersion, d.Payload, d.Attempt = int(version), json.RawMessage(payload), 1
		deliveries = append(deliveries, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if len(deliveries) == 0 {
		t.Fatal("the decision published no service_request.decided event")
	}
	for _, d := range deliveries {
		if err := f.health.HandleServiceRequestDecided(context.Background(), d); err != nil {
			t.Fatalf("deliver %s: %v", d.ID, err)
		}
		f.h.AdminExec(`UPDATE system.outbox_event SET status = 'SUCCEEDED', processed_at = clock_timestamp()
		                WHERE id = $1`, d.ID)
	}
}

// stay re-reads a stay through the service, so every assertion below is about what a caller
// would actually be answered.
func (f *stayFixture) stay(t *testing.T, id uuid.UUID) application.StayView {
	t.Helper()
	view, err := f.health.GetStay(context.Background(), f.rc(), id, application.AccessRequest{})
	if err != nil {
		t.Fatalf("read stay: %v", err)
	}
	return view
}

// authorizedStay is the whole happy path up to "the person may be admitted": ask, review,
// approve, and confirm the stay followed.
func (f *stayFixture) authorizedStay(t *testing.T, caseID uuid.UUID, days int) application.StayView {
	t.Helper()
	view := f.mustCreateStay(t, caseID, days)
	f.decide(t, view.Stay.ServiceRequestID, "APPROVE", "")
	out := f.stay(t, view.Stay.ID)
	if out.Stay.Status != domain.StayAuthorized {
		t.Fatalf("stay status = %s, want AUTHORIZED", out.Stay.Status)
	}
	return out
}

func (f *stayFixture) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var n int
	if err := f.h.Admin.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// balances reads the materialised account columns as exact decimal text.
// sum reads one aggregate as text, for assertions about exact decimals.
func (f *stayFixture) sum(t *testing.T, sql string, args ...any) string {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var out string
	if err := f.h.Admin.QueryRow(ctx, sql, args...).Scan(&out); err != nil {
		t.Fatalf("sum: %v", err)
	}
	return out
}

func (f *stayFixture) balances(t *testing.T) ledger.Balances {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var total, available, reserved, consumed, expired string
	err := f.h.Admin.QueryRow(ctx, `
		SELECT total_granted::text, available_quantity::text, reserved_quantity::text,
		       consumed_quantity::text, expired_quantity::text
		  FROM benefit.entitlement_account WHERE id = $1`, f.account).
		Scan(&total, &available, &reserved, &consumed, &expired)
	if err != nil {
		t.Fatalf("read balances: %v", err)
	}
	return ledger.Balances{
		Total: benefitdomain.MustQuantity(total), Available: benefitdomain.MustQuantity(available),
		Reserved: benefitdomain.MustQuantity(reserved), Consumed: benefitdomain.MustQuantity(consumed),
		Expired: benefitdomain.MustQuantity(expired),
	}
}

// assertConservation is WP-I4-02's invariant, asserted after every flow in this file: what was
// granted is always somewhere, and the ledger sums to the same vector the account carries. It
// is the assertion that would catch a release this package took twice.
func (f *stayFixture) assertConservation(t *testing.T) {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	b := f.balances(t)
	sum := b.Available.Add(b.Reserved).Add(b.Consumed).Add(b.Expired)
	if sum.Cmp(b.Total) != 0 {
		t.Fatalf("available+reserved+consumed+expired = %s, want total_granted %s",
			sum.String(), b.Total.String())
	}
	var total, available, reserved, consumed, expired string
	err := f.h.Admin.QueryRow(ctx, `
		SELECT coalesce(sum(delta_total), 0)::text, coalesce(sum(delta_available), 0)::text,
		       coalesce(sum(delta_reserved), 0)::text, coalesce(sum(delta_consumed), 0)::text,
		       coalesce(sum(delta_expired), 0)::text
		  FROM benefit.entitlement_ledger WHERE entitlement_account_id = $1`, f.account).
		Scan(&total, &available, &reserved, &consumed, &expired)
	if err != nil {
		t.Fatalf("read ledger sum: %v", err)
	}
	ledgerSum := ledger.Balances{
		Total: benefitdomain.MustQuantity(total), Available: benefitdomain.MustQuantity(available),
		Reserved: benefitdomain.MustQuantity(reserved), Consumed: benefitdomain.MustQuantity(consumed),
		Expired: benefitdomain.MustQuantity(expired),
	}
	if !b.Equal(ledgerSum) {
		t.Fatalf("account %v does not equal ledger sum %v", b, ledgerSum)
	}
}

// setWindow writes one of the tenant's admission window settings.
func (f *stayFixture) setWindow(t *testing.T, key string, days int) {
	t.Helper()
	f.h.AdminExec(`
		INSERT INTO platform.tenant_setting (tenant_id, setting_key, value_json)
		VALUES ($1, $2, to_jsonb($3::int))
		ON CONFLICT (tenant_id, setting_key) DO UPDATE SET value_json = EXCLUDED.value_json`,
		f.tenant, key, days)
}

// ---------------------------------------------------------------------------
// One open stay per case and provider
// ---------------------------------------------------------------------------

// TestSecondOpenStayIsRefused is the rule of v1.2 10.4 step 3 as a caller meets it.
func TestSecondOpenStayIsRefused(t *testing.T) {
	f := newStayFixture(t)
	caseID := f.newCase(t, f.provider)
	f.mustCreateStay(t, caseID, 3)

	_, err := f.createStay(t, caseID, 2, f.clock())
	if !errorIs(err, application.ErrStayAlreadyOpen) {
		t.Fatalf("second create: %v, want ErrStayAlreadyOpen", err)
	}
	if n := f.count(t, `SELECT count(*) FROM health.inpatient_stay WHERE case_id = $1`, caseID); n != 1 {
		t.Fatalf("stays = %d, want 1", n)
	}
	// And the refused stay's preauthorization went with it: one request, not two.
	if n := f.count(t, `SELECT count(*) FROM service.service_request WHERE tenant_id = $1`, f.tenant); n != 1 {
		t.Fatalf("service requests = %d, want 1: the refused stay's request was not rolled back", n)
	}
}

// TestTwentyConcurrentCreatesLeaveExactlyOneStay is the same rule proved against a race rather
// than against a sequence. Twenty callers arrive at once; the partial unique index is what
// decides, and nineteen of them take their preauthorization request down with them.
//
// This is the test that would still pass with the index replaced by a count-then-insert, if it
// were run sequentially. Run concurrently it does not: a count taken by twenty transactions
// that have all not yet committed answers zero twenty times.
func TestTwentyConcurrentCreatesLeaveExactlyOneStay(t *testing.T) {
	f := newStayFixture(t)
	caseID := f.newCase(t, f.provider)

	const callers = 20
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created int
		errs    []error
	)
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.createStay(t, caseID, 3, stayNow)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				created++
				return
			}
			errs = append(errs, err)
		}()
	}
	close(start)
	wg.Wait()

	if created != 1 {
		t.Fatalf("successful creates = %d, want exactly 1 (errors: %v)", created, errs)
	}
	if n := f.count(t, `SELECT count(*) FROM health.inpatient_stay WHERE case_id = $1`, caseID); n != 1 {
		t.Fatalf("stays = %d, want exactly 1", n)
	}
	if n := f.count(t, `SELECT count(*) FROM service.service_request WHERE tenant_id = $1`, f.tenant); n != 1 {
		t.Fatalf("service requests = %d, want 1: the refused creates left requests behind", n)
	}
	for _, err := range errs {
		if !errorIs(err, application.ErrStayAlreadyOpen) {
			t.Fatalf("a refused create answered %v, want ErrStayAlreadyOpen", err)
		}
	}
}

// TestAnotherProviderMayAdmitTheSameCase keeps the rule the size it is: one open stay per case
// *and provider*, not one per case. A person transferred to another hospital is a second
// admission, and a rule that forbade it would make a transfer unrecordable.
func TestAnotherProviderMayAdmitTheSameCase(t *testing.T) {
	f := newStayFixture(t)
	caseID := f.newCase(t, f.provider)
	f.mustCreateStay(t, caseID, 3)

	_, err := f.health.CreateStay(context.Background(), f.rc(), application.NewStayInput{
		CaseID: caseID, ProviderOrganizationID: f.otherOrg,
		AdmissionAt: f.clock(), EstimatedDays: 2,
	}, application.AccessRequest{})
	if err != nil {
		t.Fatalf("second provider create: %v, want it to be allowed", err)
	}
	if n := f.count(t, `SELECT count(*) FROM health.inpatient_stay WHERE case_id = $1`, caseID); n != 2 {
		t.Fatalf("stays = %d, want 2", n)
	}
}

// ---------------------------------------------------------------------------
// The admission window
// ---------------------------------------------------------------------------

// TestAdmissionWindow is section 2.2's refusal, on the boundary. Three days back is the last
// day inside a window of three; four days back is the first day outside it.
func TestAdmissionWindow(t *testing.T) {
	f := newStayFixture(t)
	f.setWindow(t, application.SettingBackdateDays, 3)

	caseID := f.newCase(t, f.provider)
	_, err := f.createStay(t, caseID, 2, f.clock().AddDate(0, 0, -4))
	if !errorIs(err, application.ErrAdmissionOutOfWindow) {
		t.Fatalf("admission four days back: %v, want ErrAdmissionOutOfWindow", err)
	}
	if n := f.count(t, `SELECT count(*) FROM health.inpatient_stay WHERE case_id = $1`, caseID); n != 0 {
		t.Fatalf("stays = %d, want 0: a refused admission wrote a row", n)
	}

	if _, err := f.createStay(t, caseID, 2, f.clock().AddDate(0, 0, -3)); err != nil {
		t.Fatalf("admission three days back: %v, want it accepted", err)
	}
}

// TestAdmissionWindowDefaults proves the fallback of section 2.2: a tenant that has configured
// nothing gets three days back and thirty forward.
func TestAdmissionWindowDefaults(t *testing.T) {
	f := newStayFixture(t)
	caseID := f.newCase(t, f.provider)

	if _, err := f.createStay(t, caseID, 2, f.clock().AddDate(0, 0, 30)); err != nil {
		t.Fatalf("admission thirty days forward: %v, want it accepted", err)
	}
	other := f.newCase(t, f.otherOrg)
	_, err := f.health.CreateStay(context.Background(), f.rc(), application.NewStayInput{
		CaseID: other, ProviderOrganizationID: f.otherOrg,
		AdmissionAt: f.clock().AddDate(0, 0, 31), EstimatedDays: 2,
	}, application.AccessRequest{})
	if !errorIs(err, application.ErrAdmissionOutOfWindow) {
		t.Fatalf("admission thirty-one days forward: %v, want ErrAdmissionOutOfWindow", err)
	}
}

// ---------------------------------------------------------------------------
// The decision, through the outbox
// ---------------------------------------------------------------------------

// TestApprovingTheRequestAuthorizesTheStay is acceptance criterion 3: a reviewer decides the
// admission where they decide every request, and the stay follows with a hold attached.
func TestApprovingTheRequestAuthorizesTheStay(t *testing.T) {
	f := newStayFixture(t)
	caseID := f.newCase(t, f.provider)
	view := f.mustCreateStay(t, caseID, 5)
	if view.Stay.Status != domain.StayRequested {
		t.Fatalf("new stay status = %s, want REQUESTED", view.Stay.Status)
	}
	if view.Stay.AuthorizationID != nil {
		t.Fatal("a stay nobody has decided carries an authorization")
	}

	f.decide(t, view.Stay.ServiceRequestID, "APPROVE", "")

	out := f.stay(t, view.Stay.ID)
	if out.Stay.Status != domain.StayAuthorized {
		t.Fatalf("stay status = %s, want AUTHORIZED", out.Stay.Status)
	}
	if out.Stay.AuthorizationID == nil {
		t.Fatal("an authorized stay carries no authorization")
	}
	if out.Stay.AuthorizedDays != "5" {
		t.Fatalf("authorized days = %q, want 5", out.Stay.AuthorizedDays)
	}
	if b := f.balances(t); b.Reserved.String() != "5" {
		t.Fatalf("reserved = %s, want 5", b.Reserved.String())
	}
	f.assertConservation(t)
}

// TestRejectingTheRequestRejectsTheStay is the other half: no hold at all.
func TestRejectingTheRequestRejectsTheStay(t *testing.T) {
	f := newStayFixture(t)
	caseID := f.newCase(t, f.provider)
	view := f.mustCreateStay(t, caseID, 5)

	f.decide(t, view.Stay.ServiceRequestID, "REJECT", "")

	out := f.stay(t, view.Stay.ID)
	if out.Stay.Status != domain.StayRejected {
		t.Fatalf("stay status = %s, want REJECTED", out.Stay.Status)
	}
	if out.Stay.AuthorizationID != nil {
		t.Fatal("a refused stay carries an authorization")
	}
	if n := f.count(t, `SELECT count(*) FROM service.authorization WHERE tenant_id = $1`, f.tenant); n != 0 {
		t.Fatalf("authorizations = %d, want 0", n)
	}
	if b := f.balances(t); !b.Reserved.IsZero() {
		t.Fatalf("reserved = %s, want 0: a refused admission held entitlement", b.Reserved.String())
	}
	// And a refused stay does not block the corrected one, which is why REJECTED is outside
	// the partial unique index.
	if _, err := f.createStay(t, caseID, 3, f.clock()); err != nil {
		t.Fatalf("create after a rejection: %v, want it allowed", err)
	}
	f.assertConservation(t)
}

// TestPartialApprovalAuthorizesWhatTheReviewerApproved is why the stay reads the day count off
// the authorization rather than off its own estimate: a reviewer who granted four of five days
// has authorized four, and a reconciliation against five would release a day too few.
func TestPartialApprovalAuthorizesWhatTheReviewerApproved(t *testing.T) {
	f := newStayFixture(t)
	caseID := f.newCase(t, f.provider)
	view := f.mustCreateStay(t, caseID, 5)

	f.decide(t, view.Stay.ServiceRequestID, "PARTIAL", "4")

	out := f.stay(t, view.Stay.ID)
	if out.Stay.Status != domain.StayAuthorized {
		t.Fatalf("stay status = %s, want AUTHORIZED", out.Stay.Status)
	}
	if out.Stay.AuthorizedDays != "4" {
		t.Fatalf("authorized days = %q, want 4 — the estimate was 5", out.Stay.AuthorizedDays)
	}
	f.assertConservation(t)
}

// TestRedeliveredDecisionAuthorizesOnce is the idempotency the outbox demands. The same event
// twice must not take a second hold.
func TestRedeliveredDecisionAuthorizesOnce(t *testing.T) {
	f := newStayFixture(t)
	caseID := f.newCase(t, f.provider)
	view := f.mustCreateStay(t, caseID, 5)
	f.decide(t, view.Stay.ServiceRequestID, "APPROVE", "")

	// Replay every decided event, exactly as a redelivery would.
	f.h.AdminExec(`UPDATE system.outbox_event SET status = 'PENDING' WHERE event_type = $1`,
		servicerequestapp.DecidedEvent)
	f.deliverDecisions(t)

	if n := f.count(t, `SELECT count(*) FROM service.authorization WHERE tenant_id = $1`, f.tenant); n != 1 {
		t.Fatalf("authorizations = %d, want 1 after a redelivery", n)
	}
	if b := f.balances(t); b.Reserved.String() != "5" {
		t.Fatalf("reserved = %s, want 5 after a redelivery", b.Reserved.String())
	}
	f.assertConservation(t)
}

// ---------------------------------------------------------------------------
// The extension gate
// ---------------------------------------------------------------------------

// TestSecondExtensionWhileOneIsUndecidedIsRefused is v1.2 10.4 step 6, and then the release of
// the gate once the first one is decided.
func TestSecondExtensionWhileOneIsUndecidedIsRefused(t *testing.T) {
	f := newStayFixture(t)
	caseID := f.newCase(t, f.provider)
	stay := f.authorizedStay(t, caseID, 5)

	extended, err := f.health.ExtendStay(context.Background(), f.rc(), stay.Stay.ID,
		application.StayExtensionInput{AdditionalDays: 2, ReasonCode: "COMPLICATION"},
		stay.Stay.RowVersion, application.AccessRequest{})
	if err != nil {
		t.Fatalf("first extension: %v", err)
	}
	if len(extended.Extensions) != 1 {
		t.Fatalf("extensions = %d, want 1", len(extended.Extensions))
	}

	current := f.stay(t, stay.Stay.ID)
	_, err = f.health.ExtendStay(context.Background(), f.rc(), stay.Stay.ID,
		application.StayExtensionInput{AdditionalDays: 1, ReasonCode: "COMPLICATION"},
		current.Stay.RowVersion, application.AccessRequest{})
	if !errorIs(err, application.ErrStayExtensionPending) {
		t.Fatalf("second extension while one is undecided: %v, want ErrStayExtensionPending", err)
	}
	if n := f.count(t, `SELECT count(*) FROM health.stay_extension WHERE stay_id = $1`, stay.Stay.ID); n != 1 {
		t.Fatalf("extensions = %d, want 1", n)
	}

	// Decide the first one; the gate opens.
	f.decide(t, extended.Extensions[0].ServiceRequestID, "APPROVE", "")
	after := f.stay(t, stay.Stay.ID)
	if after.Extensions[0].Status != domain.ExtensionApproved {
		t.Fatalf("extension status = %s, want APPROVED", after.Extensions[0].Status)
	}
	if after.Stay.AuthorizedDays != "7" {
		t.Fatalf("authorized days = %q, want 7 after a two-day extension", after.Stay.AuthorizedDays)
	}
	if _, err := f.health.ExtendStay(context.Background(), f.rc(), stay.Stay.ID,
		application.StayExtensionInput{AdditionalDays: 1, ReasonCode: "COMPLICATION"},
		after.Stay.RowVersion, application.AccessRequest{}); err != nil {
		t.Fatalf("second extension after the first was decided: %v, want it accepted", err)
	}
	f.assertConservation(t)
}

// TestExtensionOfAnUndecidedStayIsRefused is the other half of section 2.3's precondition:
// there is nothing to extend until somebody has approved the admission.
func TestExtensionOfAnUndecidedStayIsRefused(t *testing.T) {
	f := newStayFixture(t)
	caseID := f.newCase(t, f.provider)
	stay := f.mustCreateStay(t, caseID, 5)

	_, err := f.health.ExtendStay(context.Background(), f.rc(), stay.Stay.ID,
		application.StayExtensionInput{AdditionalDays: 2, ReasonCode: "COMPLICATION"},
		stay.Stay.RowVersion, application.AccessRequest{})
	if !errorIs(err, application.ErrStayTransitionInvalid) {
		t.Fatalf("extending a REQUESTED stay: %v, want ErrStayTransitionInvalid", err)
	}
}

// TestRejectedExtensionLeavesTheStayUnchanged is section 2.3's last sentence.
func TestRejectedExtensionLeavesTheStayUnchanged(t *testing.T) {
	f := newStayFixture(t)
	caseID := f.newCase(t, f.provider)
	stay := f.authorizedStay(t, caseID, 5)

	extended, err := f.health.ExtendStay(context.Background(), f.rc(), stay.Stay.ID,
		application.StayExtensionInput{AdditionalDays: 2, ReasonCode: "COMPLICATION"},
		stay.Stay.RowVersion, application.AccessRequest{})
	if err != nil {
		t.Fatalf("extension: %v", err)
	}
	f.decide(t, extended.Extensions[0].ServiceRequestID, "REJECT", "")

	after := f.stay(t, stay.Stay.ID)
	if after.Extensions[0].Status != domain.ExtensionRejected {
		t.Fatalf("extension status = %s, want REJECTED", after.Extensions[0].Status)
	}
	if after.Extensions[0].AuthorizationID != nil {
		t.Fatal("a refused extension carries an authorization")
	}
	if after.Stay.AuthorizedDays != "5" {
		t.Fatalf("authorized days = %q, want 5: a refused extension moved the stay",
			after.Stay.AuthorizedDays)
	}
	if !after.Stay.ExpectedDischargeAt.Equal(stay.Stay.ExpectedDischargeAt) {
		t.Fatal("a refused extension moved the expected discharge")
	}
	f.assertConservation(t)
}

// ---------------------------------------------------------------------------
// Discharge, reconciliation and release
// ---------------------------------------------------------------------------

// TestDischargeReleasesWhatWasNotUsed is the heart of section 2.4: authorize five days,
// discharge after three, two days go back to the member.
func TestDischargeReleasesWhatWasNotUsed(t *testing.T) {
	f := newStayFixture(t)
	caseID := f.newCase(t, f.provider)
	stay := f.authorizedStay(t, caseID, 5)
	if b := f.balances(t); b.Reserved.String() != "5" || b.Available.String() != "55" {
		t.Fatalf("after authorization: reserved=%s available=%s, want 5 and 55",
			b.Reserved.String(), b.Available.String())
	}

	f.advance(3)
	discharged, err := f.health.DischargeStay(context.Background(), f.rc(), stay.Stay.ID,
		f.clock(), stay.Stay.RowVersion, application.AccessRequest{})
	if err != nil {
		t.Fatalf("discharge: %v", err)
	}
	if discharged.Stay.Status != domain.StayDischarged {
		t.Fatalf("status = %s, want DISCHARGED", discharged.Stay.Status)
	}
	if discharged.Stay.ActualDays != "3" || discharged.Stay.ReleasedDays != "2" {
		t.Fatalf("actual=%q released=%q, want 3 and 2",
			discharged.Stay.ActualDays, discharged.Stay.ReleasedDays)
	}
	if discharged.Stay.OverAuthorization {
		t.Fatal("a stay inside its authorization is flagged over-authorization")
	}
	b := f.balances(t)
	if b.Reserved.String() != "3" || b.Available.String() != "57" {
		t.Fatalf("after discharge: reserved=%s available=%s, want 3 and 57",
			b.Reserved.String(), b.Available.String())
	}
	f.assertConservation(t)

	// The reconciliation endpoint answers the same two figures and the release.
	recon, err := f.health.GetStayReconciliation(context.Background(), f.rc(), stay.Stay.ID,
		application.AccessRequest{})
	if err != nil {
		t.Fatalf("reconciliation: %v", err)
	}
	if recon.AuthorizedDays != "5" || recon.ActualDays != "3" || recon.ReleasedDays != "2" {
		t.Fatalf("reconciliation = %+v, want 5/3/2", recon)
	}

	// And running discharge twice releases nothing twice.
	_, err = f.health.DischargeStay(context.Background(), f.rc(), stay.Stay.ID, f.clock(),
		discharged.Stay.RowVersion, application.AccessRequest{})
	if !errorIs(err, application.ErrStayTransitionInvalid) {
		t.Fatalf("second discharge: %v, want ErrStayTransitionInvalid", err)
	}
	after := f.balances(t)
	if !after.Equal(b) {
		t.Fatalf("balances moved on the second discharge: %v then %v", b, after)
	}
	f.assertConservation(t)
}

// TestDischargeReleasesTheExtensionsHoldToo is the acceptance criterion read strictly: what
// was reserved and not used is released, never quietly kept. An extension reserves its added
// days on an authorization of its own — the stay then stands on two holds — and a discharge
// that only knew about the first one would give back what the first one holds and leave the
// extension's days reserved against a bed nobody is in. The member would find a benefit they
// have already paid for unusable, and no aggregate assertion would notice: the account's
// totals are right whichever hold the days are taken from.
func TestDischargeReleasesTheExtensionsHoldToo(t *testing.T) {
	f := newStayFixture(t)
	caseID := f.newCase(t, f.provider)
	stay := f.authorizedStay(t, caseID, 5)

	extended, err := f.health.ExtendStay(context.Background(), f.rc(), stay.Stay.ID,
		application.StayExtensionInput{AdditionalDays: 3, ReasonCode: "COMPLICATION"},
		stay.Stay.RowVersion, application.AccessRequest{})
	if err != nil {
		t.Fatalf("extension: %v", err)
	}
	f.decide(t, extended.Extensions[0].ServiceRequestID, "APPROVE", "")
	authorized := f.stay(t, stay.Stay.ID)
	if authorized.Stay.AuthorizedDays != "8" {
		t.Fatalf("authorized days = %q, want 8", authorized.Stay.AuthorizedDays)
	}
	if b := f.balances(t); b.Reserved.String() != "8" {
		t.Fatalf("reserved after the extension = %s, want 8", b.Reserved.String())
	}

	// Discharged after two days, with six of the eight promised days unspent — more than
	// the original authorization alone is holding.
	f.advance(2)
	discharged, err := f.health.DischargeStay(context.Background(), f.rc(), stay.Stay.ID,
		f.clock(), authorized.Stay.RowVersion, application.AccessRequest{})
	if err != nil {
		t.Fatalf("discharge: %v", err)
	}
	if discharged.Stay.ActualDays != "2" || discharged.Stay.ReleasedDays != "6" {
		t.Fatalf("actual=%q released=%q, want 2 and 6",
			discharged.Stay.ActualDays, discharged.Stay.ReleasedDays)
	}
	// The account is the check that would pass either way; these two are the ones that
	// would not. Nothing is reserved beyond the days the person actually spent, and the
	// extension's own hold is one of the places that was true of.
	if b := f.balances(t); b.Reserved.String() != "2" {
		t.Fatalf("reserved after discharge = %s, want 2: the unused days of every hold "+
			"this stay stands on are released, not only the first one", b.Reserved.String())
	}
	// And the same figure hold by hold, so that a release which took the right total from
	// the wrong authorization would not pass either. Two days outstanding, on one hold.
	if outstanding := f.sum(t, `
		SELECT coalesce(sum(r.quantity - r.consumed_quantity - r.released_quantity), 0)::text
		  FROM benefit.entitlement_reservation r
		  JOIN service.authorization_item i ON i.entitlement_reservation_id = r.id
		 WHERE i.authorization_id IN (
		           SELECT authorization_id FROM health.inpatient_stay
		            WHERE id = $1 AND authorization_id IS NOT NULL
		            UNION ALL
		           SELECT authorization_id FROM health.stay_extension
		            WHERE stay_id = $1 AND authorization_id IS NOT NULL)`,
		stay.Stay.ID); outstanding != "2.000000" && outstanding != "2" {
		t.Fatalf("this stay's holds still reserve %s days for the two that were spent",
			outstanding)
	}
	f.assertConservation(t)
}

// TestDischargeOverAuthorizationReleasesNothing is the other side of the reconciliation: the
// admission ran over, so there is nothing to give back and the claim is told.
func TestDischargeOverAuthorizationReleasesNothing(t *testing.T) {
	f := newStayFixture(t)
	caseID := f.newCase(t, f.provider)
	stay := f.authorizedStay(t, caseID, 5)

	f.advance(7)
	discharged, err := f.health.DischargeStay(context.Background(), f.rc(), stay.Stay.ID,
		f.clock(), stay.Stay.RowVersion, application.AccessRequest{})
	if err != nil {
		t.Fatalf("discharge: %v", err)
	}
	if discharged.Stay.ActualDays != "7" {
		t.Fatalf("actual days = %q, want 7", discharged.Stay.ActualDays)
	}
	if !discharged.Stay.OverAuthorization {
		t.Fatal("a stay that ran over its authorization is not flagged")
	}
	if discharged.Stay.ReleasedDays != "0" {
		t.Fatalf("released = %q, want 0", discharged.Stay.ReleasedDays)
	}
	if b := f.balances(t); b.Reserved.String() != "5" {
		t.Fatalf("reserved = %s, want 5: an over-run admission released something",
			b.Reserved.String())
	}
	f.assertConservation(t)
}

// TestDischargeSameDayCountsOneDay is the "never less than 1" of section 2.4. Somebody admitted
// in the morning and sent home in the afternoon stayed a day, not a third of one.
func TestDischargeSameDayCountsOneDay(t *testing.T) {
	f := newStayFixture(t)
	caseID := f.newCase(t, f.provider)
	stay := f.authorizedStay(t, caseID, 2)

	sixHoursLater := stay.Stay.AdmissionAt.Add(6 * time.Hour)
	f.advance(1)
	discharged, err := f.health.DischargeStay(context.Background(), f.rc(), stay.Stay.ID,
		sixHoursLater, stay.Stay.RowVersion, application.AccessRequest{})
	if err != nil {
		t.Fatalf("discharge: %v", err)
	}
	if discharged.Stay.ActualDays != "1" || discharged.Stay.ReleasedDays != "1" {
		t.Fatalf("actual=%q released=%q, want 1 and 1",
			discharged.Stay.ActualDays, discharged.Stay.ReleasedDays)
	}
	f.assertConservation(t)
}

// TestDischargeEndsOpenSegments is the second half of section 2.4's first sentence: a segment
// left open past the discharge would price a night the patient was not there for.
func TestDischargeEndsOpenSegments(t *testing.T) {
	f := newStayFixture(t)
	caseID := f.newCase(t, f.provider)
	stay := f.authorizedStay(t, caseID, 5)

	admitted := f.putSegments(t, stay, []domain.SegmentInput{
		{SegmentType: domain.SegmentWard, StartsAt: stay.Stay.AdmissionAt},
	})
	f.advance(3)
	discharged, err := f.health.DischargeStay(context.Background(), f.rc(), stay.Stay.ID,
		f.clock(), admitted.Stay.RowVersion, application.AccessRequest{})
	if err != nil {
		t.Fatalf("discharge: %v", err)
	}
	if len(discharged.Segments) != 1 || discharged.Segments[0].EndsAt == nil {
		t.Fatalf("segments after discharge = %+v, want one ended segment", discharged.Segments)
	}
	if !discharged.Segments[0].EndsAt.Equal(*discharged.Stay.DischargeAt) {
		t.Fatal("the open segment was not ended at the discharge moment")
	}
	f.assertConservation(t)
}

// TestCancelReleasesEverything is section 2.4 read the other way: an admission that did not
// happen has held nothing.
func TestCancelReleasesEverything(t *testing.T) {
	f := newStayFixture(t)
	caseID := f.newCase(t, f.provider)
	stay := f.authorizedStay(t, caseID, 5)

	if _, err := f.health.CancelStay(context.Background(), f.rc(), stay.Stay.ID,
		"ADMISSION_NOT_NEEDED", nil, stay.Stay.RowVersion, application.AccessRequest{}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	out := f.stay(t, stay.Stay.ID)
	if out.Stay.Status != domain.StayCancelled {
		t.Fatalf("status = %s, want CANCELLED", out.Stay.Status)
	}
	if b := f.balances(t); !b.Reserved.IsZero() || b.Available.String() != dayGrant {
		t.Fatalf("after cancel: reserved=%s available=%s, want 0 and %s",
			b.Reserved.String(), b.Available.String(), dayGrant)
	}
	f.assertConservation(t)
}

// ---------------------------------------------------------------------------
// Segments
// ---------------------------------------------------------------------------

func (f *stayFixture) putSegments(t *testing.T, stay application.StayView,
	items []domain.SegmentInput,
) application.StayView {
	t.Helper()
	current := f.stay(t, stay.Stay.ID)
	view, err := f.health.PutStaySegments(context.Background(), f.rc(), stay.Stay.ID, items,
		current.Stay.RowVersion, application.AccessRequest{})
	if err != nil {
		t.Fatalf("put segments: %v", err)
	}
	return view
}

// TestOverlappingSegmentsAreRefused is the exclusion constraint's rule, and the transfer that
// is not an overlap because the range is half-open.
func TestOverlappingSegmentsAreRefused(t *testing.T) {
	f := newStayFixture(t)
	caseID := f.newCase(t, f.provider)
	stay := f.authorizedStay(t, caseID, 5)
	admission := stay.Stay.AdmissionAt

	end := admission.Add(48 * time.Hour)
	_, err := f.health.PutStaySegments(context.Background(), f.rc(), stay.Stay.ID,
		[]domain.SegmentInput{
			{SegmentType: domain.SegmentWard, StartsAt: admission, EndsAt: &end},
			{SegmentType: domain.SegmentICU, StartsAt: admission.Add(24 * time.Hour)},
		}, stay.Stay.RowVersion, application.AccessRequest{})
	if err == nil {
		t.Fatal("overlapping WARD and ICU segments were accepted")
	}
	if n := f.count(t, `SELECT count(*) FROM health.stay_segment WHERE stay_id = $1`, stay.Stay.ID); n != 0 {
		t.Fatalf("segments = %d, want 0: a refused set was half written", n)
	}

	// The same two, meeting rather than overlapping: a transfer.
	transfer := admission.Add(24 * time.Hour)
	view := f.putSegments(t, stay, []domain.SegmentInput{
		{SegmentType: domain.SegmentWard, StartsAt: admission, EndsAt: &transfer},
		{SegmentType: domain.SegmentICU, StartsAt: transfer},
	})
	if len(view.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(view.Segments))
	}
	// And recording where the patient is is the admission.
	if view.Stay.Status != domain.StayAdmitted {
		t.Fatalf("status = %s, want ADMITTED after the first segment set", view.Stay.Status)
	}
}

// TestCompanionMayOverlap is the hole in the constraint, with its name on it: a relative
// sleeping in the room is in the room while the patient is.
func TestCompanionMayOverlap(t *testing.T) {
	f := newStayFixture(t)
	caseID := f.newCase(t, f.provider)
	stay := f.authorizedStay(t, caseID, 5)
	admission := stay.Stay.AdmissionAt
	end := admission.Add(48 * time.Hour)

	view := f.putSegments(t, stay, []domain.SegmentInput{
		{SegmentType: domain.SegmentWard, StartsAt: admission, EndsAt: &end},
		{SegmentType: domain.SegmentCompanion, StartsAt: admission, EndsAt: &end},
	})
	if len(view.Segments) != 2 {
		t.Fatalf("segments = %d, want 2: a companion could not overlap the patient", len(view.Segments))
	}
}

// TestExclusionConstraintRefusesWhatTheServiceLetPast is the proof that the constraint is the
// authority rather than the validation. The rows are written straight into the table, past
// every line of Go in this package, and the database still refuses them.
func TestExclusionConstraintRefusesWhatTheServiceLetPast(t *testing.T) {
	f := newStayFixture(t)
	caseID := f.newCase(t, f.provider)
	stay := f.authorizedStay(t, caseID, 5)
	admission := stay.Stay.AdmissionAt

	f.h.AdminExec(`
		INSERT INTO health.stay_segment (tenant_id, stay_id, segment_type, starts_at, ends_at)
		VALUES ($1, $2, 'WARD', $3, $4)`,
		f.tenant, stay.Stay.ID, admission, admission.Add(48*time.Hour))
	err := f.h.AdminExecErr(`
		INSERT INTO health.stay_segment (tenant_id, stay_id, segment_type, starts_at, ends_at)
		VALUES ($1, $2, 'ICU', $3, $4)`,
		f.tenant, stay.Stay.ID, admission.Add(24*time.Hour), admission.Add(72*time.Hour))
	if err == nil {
		t.Fatal("the database accepted two overlapping non-companion segments")
	}
	if !strings.Contains(err.Error(), "ex_stay_segment_overlap") {
		t.Fatalf("refusal = %v, want the exclusion constraint", err)
	}
	// A companion over the same hours is accepted, by the same constraint's WHERE clause.
	f.h.AdminExec(`
		INSERT INTO health.stay_segment (tenant_id, stay_id, segment_type, starts_at, ends_at)
		VALUES ($1, $2, 'COMPANION', $3, $4)`,
		f.tenant, stay.Stay.ID, admission.Add(24*time.Hour), admission.Add(72*time.Hour))
}

// TestPendingExtensionIndexRefusesWhatTheServiceLetPast is the same proof for the extension
// gate: the partial unique index refuses a second undecided extension whatever writes it.
func TestPendingExtensionIndexRefusesWhatTheServiceLetPast(t *testing.T) {
	f := newStayFixture(t)
	caseID := f.newCase(t, f.provider)
	stay := f.authorizedStay(t, caseID, 5)

	extended, err := f.health.ExtendStay(context.Background(), f.rc(), stay.Stay.ID,
		application.StayExtensionInput{AdditionalDays: 2, ReasonCode: "COMPLICATION"},
		stay.Stay.RowVersion, application.AccessRequest{})
	if err != nil {
		t.Fatalf("first extension: %v", err)
	}
	// A second request of its own, so the refusal below can only be the pending index: the
	// extension's unique key on its request would refuse a row that reused the first one,
	// and a test that could not tell the two apart would keep passing with the index gone.
	other, err := f.health.CreateStay(context.Background(), f.rc(), application.NewStayInput{
		CaseID: f.newCase(t, f.otherOrg), ProviderOrganizationID: f.otherOrg,
		AdmissionAt: f.clock(), EstimatedDays: 1,
	}, application.AccessRequest{})
	if err != nil {
		t.Fatalf("second stay: %v", err)
	}
	insertErr := f.h.AdminExecErr(`
		INSERT INTO health.stay_extension (tenant_id, stay_id, sequence_no, additional_days,
		                                   reason_code, service_request_id)
		VALUES ($1, $2, 99, 1, 'COMPLICATION', $3)`,
		f.tenant, stay.Stay.ID, other.Stay.ServiceRequestID)
	if insertErr == nil {
		t.Fatal("the database accepted a second undecided extension")
	}
	if dbtest.SQLState(insertErr) != dbtest.SQLStateUniqueViolation {
		t.Fatalf("refusal = %v, want a unique violation from uq_stay_extension_pending", insertErr)
	}
	_ = extended
}

// errorIs is errors.Is, with the nil case spelled out so a test that meant to fail and did
// not says so rather than passing on a nil comparison.
func errorIs(err, target error) bool {
	return err != nil && errors.Is(err, target)
}
