package application_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/authorization/application"
	"github.com/celikbros/kapsora/internal/authorization/domain"
	authorizationpg "github.com/celikbros/kapsora/internal/authorization/infrastructure/postgres"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// The fixture seeds the whole chain an authorization reads: a sponsored member enrolled
// in a published plan with two entitlement accounts, a provider organization and two
// catalogue definitions whose codes match the entitlement codes — which is how a line is
// mapped onto a balance until a catalogue-to-entitlement table exists.
const (
	physioCode = "PHYSIO"
	dentalCode = "DENTAL"
	// physioGrant is what the PHYSIO account is opened with; the concurrency test spends
	// exactly this many units and expects the rest of its callers to be refused.
	physioGrant = "20"
	dentalGrant = "2"
)

// testNow pins the clock so every window in these tests is deterministic.
var (
	testNow     = time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	serviceDate = time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
)

type fixture struct {
	h    *dbtest.Harness
	pool *pgxpool.Pool
	svc  *application.Service
	logs *bytes.Buffer

	tenant     uuid.UUID
	actor      uuid.UUID
	providerOr uuid.UUID
	otherOr    uuid.UUID
	person     uuid.UUID
	program    uuid.UUID
	enrollment uuid.UUID

	physioDefinition uuid.UUID
	dentalDefinition uuid.UUID
	physioAccount    uuid.UUID
	dentalAccount    uuid.UUID
}

func newFixture(t *testing.T) *fixture { return newFixtureWithMapping(t, false) }

func newFixtureWithMapping(t *testing.T, mapped bool) *fixture {
	t.Helper()
	h := dbtest.New(t)

	// The concurrency test needs one connection per goroutine in flight; the harness pool
	// is deliberately small, so this one is opened against the same app role.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.NewPool(ctx, h.AppURL, db.PoolOptions{ApplicationName: "authorization-test", MaxConns: 24})
	if err != nil {
		t.Fatalf("app pool: %v", err)
	}
	t.Cleanup(pool.Close)

	cursors, err := httpx.NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	svc, err := application.New(application.Deps{
		Pool: pool, Repo: authorizationpg.New(), Audit: auditpg.New(), Cursors: cursors,
		Logger: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:    func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}

	f := &fixture{h: h, pool: pool, svc: svc, logs: logs}
	f.seed(t, mapped)
	return f
}

func (f *fixture) seed(t *testing.T, mapped bool) { //nolint:funlen // one linear fixture reads better whole
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

	f.tenant = h.CreateTenant("AUTHORIZATION")
	f.actor = h.CreateActor("authorization-clerk", "Authorization Clerk")
	sponsor := h.CreateTenantOrganization(f.tenant, "Auth Sponsor", "SPONSOR")
	payer := h.CreateTenantOrganization(f.tenant, "Auth Payer", "PAYER")
	f.providerOr = h.CreateTenantOrganization(f.tenant, "Auth Provider", "PROVIDER")
	f.otherOr = h.CreateTenantOrganization(f.tenant, "Another Provider", "PROVIDER")

	h.AdminExec(`INSERT INTO party.membership_type (tenant_id, code, display_name) VALUES ($1, 'MEMBER', 'Üye')`, f.tenant)
	scan(&f.person, "person", `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Deniz', 'Aksoy', 'deniz aksoy') RETURNING id`, f.tenant)
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
		VALUES ($1, $2, 1, 'DRAFT', daterange('2026-01-01','2027-01-01','[)'), NULL, NULL)
		RETURNING id`, f.tenant, planID)
	scan(&f.enrollment, "enrollment", `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status, valid_period)
		VALUES ($1, $2, $3, 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, membership, planID)

	var category uuid.UUID
	scan(&category, "service category", `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'HEALTH_ROOT', 'Sağlık', 'HEALTH') RETURNING id`, f.tenant)

	// The accounts are opened here rather than by this package, which is the point:
	// nothing in the authorization module may open one, because opening posts a GRANT.
	open := func(code, quantity string) (definition, account uuid.UUID) {
		t.Helper()
		var definitionID, accountID uuid.UUID
		scan(&definitionID, "entitlement definition "+code, `
			INSERT INTO benefit.entitlement_definition (tenant_id, plan_version_id, code, name, unit_type,
			                                            period_type, initial_quantity)
			VALUES ($1, $2, $3, $3, 'SESSION', 'CALENDAR_YEAR', $4::text::numeric) RETURNING id`,
			f.tenant, planVersion, code, quantity)
		scan(&accountID, "entitlement account "+code, `
			INSERT INTO benefit.entitlement_account (tenant_id, enrollment_id, entitlement_definition_id,
			                                         benefit_period, total_granted, available_quantity)
			VALUES ($1, $2, $3, daterange('2026-01-01','2027-01-01','[)'), $4::text::numeric, $4::text::numeric)
			RETURNING id`, f.tenant, f.enrollment, definitionID, quantity)
		h.AdminExec(`
			INSERT INTO benefit.entitlement_ledger (tenant_id, entitlement_account_id, movement_type,
			                                        effective_at, delta_total, delta_available,
			                                        reference_type, reference_id, idempotency_key)
			VALUES ($1, $2, 'GRANT', clock_timestamp(), $3::text::numeric, $3::text::numeric,
			        'ENROLLMENT', $4, $5)`,
			f.tenant, accountID, quantity, f.enrollment, "grant:seed:"+code)
		serviceCode := code
		if mapped && code == physioCode {
			serviceCode = "MAPPED_PHYSIO"
		}
		var serviceID uuid.UUID
		scan(&serviceID, "service definition "+code, `
			INSERT INTO catalog.service_definition (tenant_id, category_id, code, name,
			                                        fulfillment_mode, default_unit_type, requires_provider)
			VALUES ($1, $2, $3, $3, 'SESSION', 'SESSION', false) RETURNING id`,
			f.tenant, category, serviceCode)
		if mapped && code == physioCode {
			h.AdminExec(`INSERT INTO benefit.service_entitlement_mapping
			    (tenant_id, plan_version_id, service_definition_id, entitlement_definition_id, unit_factor)
			    VALUES ($1,$2,$3,$4,2)`, f.tenant, planVersion, serviceID, definitionID)
		}
		return serviceID, accountID
	}
	f.physioDefinition, f.physioAccount = open(physioCode, physioGrant)
	f.dentalDefinition, f.dentalAccount = open(dentalCode, dentalGrant)
	h.AdminExec(`UPDATE benefit.plan_version SET status='PUBLISHED', published_at=clock_timestamp(), published_by=$3 WHERE tenant_id=$1 AND id=$2`, f.tenant, planVersion, f.actor)
}

// rc is a back office actor holding every permission of this package.
func (f *fixture) rc() identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal: identity.Principal{ActorID: f.actor},
		Permissions: map[string]struct{}{
			application.PermissionManage: {}, application.PermissionRecord: {},
			application.PermissionRedeem: {}, application.PermissionRequestRead: {},
		},
	}
}

// providerRC is a provider-scoped actor: its grants are bound to one organization.
func (f *fixture) providerRC(scope uuid.UUID) identity.RequestContext {
	rc := f.rc()
	rc.Scopes = []identity.Scope{{
		Type: application.ScopeOrganization, ID: uuid.NullUUID{UUID: scope, Valid: true},
	}}
	return rc
}

// line is one approved line of a seeded request.
type line struct {
	definition uuid.UUID
	quantity   string
}

// seedRequest writes an APPROVED service request with the lines given. The rows are
// written directly because this package must work from what a decision left behind,
// whatever produced it; driving the request module's own commands here would only test
// that module twice.
func (f *fixture) seedRequest(t *testing.T, provider uuid.UUID, lines ...line) uuid.UUID {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()

	var requestID, versionID uuid.UUID
	reference := "SR-TEST-" + uuid.NewString()[:8]
	if err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO service.service_request (tenant_id, request_reference, request_type, person_id,
		                                     program_id, enrollment_id, provider_tenant_organization_id,
		                                     service_date, channel)
		VALUES ($1, $2, 'PREAUTHORIZATION', $3, $4, $5, $6, $7, 'BACKOFFICE') RETURNING id`,
		f.tenant, reference, f.person, f.program, f.enrollment, provider, serviceDate).Scan(&requestID); err != nil {
		t.Fatalf("seed request: %v", err)
	}
	if err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO service.service_request_version (tenant_id, service_request_id, version_no, status)
		VALUES ($1, $2, 1, 'DRAFT') RETURNING id`, f.tenant, requestID).Scan(&versionID); err != nil {
		t.Fatalf("seed request version: %v", err)
	}
	for i, l := range lines {
		f.h.AdminExec(`
			INSERT INTO service.service_request_item (tenant_id, service_request_version_id, line_no,
			                                          service_definition_id, requested_quantity, unit_type)
			VALUES ($1, $2, $3, $4, $5::text::numeric, 'SESSION')`,
			f.tenant, versionID, i+1, l.definition, l.quantity)
	}
	// The version freezes first, exactly as a submit would, and only then does the
	// decision land on the lines: the item guard of migration 000006 refuses an insert
	// into anything but a draft.
	f.h.AdminExec(`
		UPDATE service.service_request_version
		   SET status = 'SUBMITTED', snapshot_json = '{}'::jsonb, submitted_at = clock_timestamp()
		 WHERE tenant_id = $1 AND id = $2`, f.tenant, versionID)
	f.h.AdminExec(`
		UPDATE service.service_request_item
		   SET status = 'APPROVED', approved_quantity = requested_quantity
		 WHERE tenant_id = $1 AND service_request_version_id = $2`, f.tenant, versionID)
	f.h.AdminExec(`
		UPDATE service.service_request
		   SET status = 'APPROVED', submitted_at = clock_timestamp()
		 WHERE tenant_id = $1 AND id = $2`, f.tenant, requestID)
	return requestID
}

// authorize creates an authorization over the whole approved request.
func (f *fixture) authorize(t *testing.T, requestID uuid.UUID, key string) application.AuthorizationView {
	t.Helper()
	view, err := f.svc.Create(context.Background(), f.rc(), application.NewAuthorizationInput{
		RequestID: requestID, ValidTo: testNow.Add(24 * time.Hour), IdempotencyKey: key,
	})
	if err != nil {
		t.Fatalf("create authorization: %v", err)
	}
	return view
}

// balances reads the materialised account columns as exact decimal text.
func (f *fixture) balances(t *testing.T, accountID uuid.UUID) ledger.Balances {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var total, available, reserved, consumed, expired string
	err := f.h.Admin.QueryRow(ctx, `
		SELECT total_granted::text, available_quantity::text, reserved_quantity::text,
		       consumed_quantity::text, expired_quantity::text
		  FROM benefit.entitlement_account WHERE id = $1`, accountID).
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

// assertConservation is the invariant every flow in this file ends with: what was granted
// is always somewhere — available, reserved, consumed or expired — and the ledger sums to
// the same vector the account carries.
func (f *fixture) assertConservation(t *testing.T, accountIDs ...uuid.UUID) {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	for _, accountID := range accountIDs {
		b := f.balances(t, accountID)
		sum := b.Available.Add(b.Reserved).Add(b.Consumed).Add(b.Expired)
		if sum.Cmp(b.Total) != 0 {
			t.Fatalf("account %s: available+reserved+consumed+expired = %s, want total_granted %s",
				accountID, sum.String(), b.Total.String())
		}
		var total, available, reserved, consumed, expired string
		err := f.h.Admin.QueryRow(ctx, `
			SELECT coalesce(sum(delta_total), 0)::text, coalesce(sum(delta_available), 0)::text,
			       coalesce(sum(delta_reserved), 0)::text, coalesce(sum(delta_consumed), 0)::text,
			       coalesce(sum(delta_expired), 0)::text
			  FROM benefit.entitlement_ledger WHERE entitlement_account_id = $1`, accountID).
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
			t.Fatalf("account %s %v does not equal ledger sum %v", accountID, b, ledgerSum)
		}
	}
}

func (f *fixture) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var n int
	if err := f.h.Admin.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v\nsql: %s", err, sql)
	}
	return n
}

func assertQuantity(t *testing.T, got benefitdomain.Quantity, want, what string) {
	t.Helper()
	if got.Cmp(benefitdomain.MustQuantity(want)) != 0 {
		t.Fatalf("%s = %s, want %s", what, got.String(), want)
	}
}

// --- creating an authorization ------------------------------------------------------

func TestCreateAuthorizationReservesEntitlement(t *testing.T) {
	f := newFixture(t)
	requestID := f.seedRequest(t, f.providerOr, line{f.physioDefinition, "3"})

	view := f.authorize(t, requestID, "auth-1")
	if view.Authorization.Status != "ACTIVE" {
		t.Fatalf("status = %s, want ACTIVE", view.Authorization.Status)
	}
	if view.Authorization.ReservedTotal != "3" {
		t.Fatalf("reservedTotal = %s, want 3", view.Authorization.ReservedTotal)
	}
	if len(view.Items) != 1 || view.Items[0].ReservationID == nil {
		t.Fatalf("expected one line carrying its reservation id, got %+v", view.Items)
	}

	b := f.balances(t, f.physioAccount)
	assertQuantity(t, b.Reserved, "3", "reserved")
	assertQuantity(t, b.Available, "17", "available")
	f.assertConservation(t, f.physioAccount, f.dentalAccount)
}

// TestCreateAuthorizationRefusesUnapprovedRequest is the precondition of 2.2 step 1.
func TestCreateAuthorizationRefusesUnapprovedRequest(t *testing.T) {
	f := newFixture(t)
	requestID := f.seedRequest(t, f.providerOr, line{f.physioDefinition, "1"})
	f.h.AdminExec(`UPDATE service.service_request SET status = 'PENDING_REVIEW'
	                WHERE tenant_id = $1 AND id = $2`, f.tenant, requestID)

	_, err := f.svc.Create(context.Background(), f.rc(), application.NewAuthorizationInput{
		RequestID: requestID, ValidTo: testNow.Add(time.Hour), IdempotencyKey: "auth-unapproved",
	})
	if !errors.Is(err, application.ErrRequestNotApproved) {
		t.Fatalf("create against a request under review: %v, want ErrRequestNotApproved", err)
	}
	if n := f.count(t, `SELECT count(*) FROM service.authorization WHERE tenant_id = $1`, f.tenant); n != 0 {
		t.Fatalf("authorizations = %d, want 0", n)
	}
}

// TestCreateAuthorizationHalfReservedCannotExist is acceptance criterion 2: the second
// line has no balance, so the first line's hold must not survive either.
func TestCreateAuthorizationHalfReservedCannotExist(t *testing.T) {
	f := newFixture(t)
	requestID := f.seedRequest(t, f.providerOr,
		line{f.physioDefinition, "1"},
		line{f.dentalDefinition, "5"}, // the DENTAL account only has 2
	)

	_, err := f.svc.Create(context.Background(), f.rc(), application.NewAuthorizationInput{
		RequestID: requestID, ValidTo: testNow.Add(time.Hour), IdempotencyKey: "auth-half",
	})
	if !errors.Is(err, ledger.ErrInsufficient) {
		t.Fatalf("create with an unaffordable second line: %v, want ErrInsufficient", err)
	}

	if n := f.count(t, `SELECT count(*) FROM service.authorization WHERE tenant_id = $1`, f.tenant); n != 0 {
		t.Fatalf("authorizations = %d, want 0", n)
	}
	if n := f.count(t, `SELECT count(*) FROM service.authorization_item WHERE tenant_id = $1`, f.tenant); n != 0 {
		t.Fatalf("authorization items = %d, want 0", n)
	}
	if n := f.count(t, `SELECT count(*) FROM benefit.entitlement_reservation WHERE tenant_id = $1`, f.tenant); n != 0 {
		t.Fatalf("reservations = %d, want 0", n)
	}
	assertQuantity(t, f.balances(t, f.physioAccount).Available, physioGrant, "physio available")
	f.assertConservation(t, f.physioAccount, f.dentalAccount)
}

// TestCreateAuthorizationIdempotentReplay is 2.2: the same key returns the same
// authorization and takes one set of reservations, not two.
func TestCreateAuthorizationIdempotentReplay(t *testing.T) {
	f := newFixture(t)
	requestID := f.seedRequest(t, f.providerOr, line{f.physioDefinition, "4"})

	first := f.authorize(t, requestID, "auth-replay")
	second := f.authorize(t, requestID, "auth-replay")
	if first.Authorization.ID != second.Authorization.ID {
		t.Fatalf("replay returned %s, want the original %s", second.Authorization.ID, first.Authorization.ID)
	}
	if n := f.count(t, `SELECT count(*) FROM benefit.entitlement_reservation WHERE tenant_id = $1`, f.tenant); n != 1 {
		t.Fatalf("reservations after a replay = %d, want 1", n)
	}
	assertQuantity(t, f.balances(t, f.physioAccount).Reserved, "4", "reserved")
	f.assertConservation(t, f.physioAccount)
}

// TestCreateAuthorizationNoDoublePromise is the WP-I2-03 concurrency test one level up:
// fifty callers, a balance that covers twenty of them, and no arrangement of them may
// leave twenty-one promises.
func TestCreateAuthorizationNoDoublePromise(t *testing.T) {
	const callers = 50
	f := newFixture(t)

	requests := make([]uuid.UUID, callers)
	for i := range requests {
		requests[i] = f.seedRequest(t, f.providerOr, line{f.physioDefinition, "1"})
	}

	results := make([]error, callers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, results[i] = f.svc.Create(context.Background(), f.rc(), application.NewAuthorizationInput{
				RequestID: requests[i], ValidTo: testNow.Add(24 * time.Hour),
				IdempotencyKey: fmt.Sprintf("auth-concurrent-%d", i),
			})
		}(i)
	}
	close(start)
	wg.Wait()

	created, refused := 0, 0
	for i, err := range results {
		switch {
		case err == nil:
			created++
		case errors.Is(err, ledger.ErrInsufficient):
			refused++
		default:
			t.Fatalf("caller %d: unexpected error: %v", i, err)
		}
	}
	if created != 20 || refused != callers-20 {
		t.Fatalf("created = %d, refused = %d; want 20 and %d", created, refused, callers-20)
	}
	if n := f.count(t, `SELECT count(*) FROM service.authorization WHERE tenant_id = $1`, f.tenant); n != 20 {
		t.Fatalf("authorizations = %d, want 20", n)
	}
	if n := f.count(t, `SELECT count(*) FROM benefit.entitlement_reservation WHERE tenant_id = $1`, f.tenant); n != 20 {
		t.Fatalf("reservations = %d, want 20", n)
	}

	b := f.balances(t, f.physioAccount)
	assertQuantity(t, b.Reserved, "20", "reserved")
	assertQuantity(t, b.Available, "0", "available")
	// The account's reserved quantity equals the sum of the holds, which is the statement
	// the ledger and this package have to agree on.
	var held string
	ctx, cancel := f.h.Ctx()
	defer cancel()
	if err := f.h.Admin.QueryRow(ctx, `
		SELECT coalesce(sum(quantity), 0)::text FROM benefit.entitlement_reservation
		 WHERE tenant_id = $1 AND entitlement_account_id = $2 AND status = 'HELD'`,
		f.tenant, f.physioAccount).Scan(&held); err != nil {
		t.Fatalf("sum reservations: %v", err)
	}
	assertQuantity(t, benefitdomain.MustQuantity(held), "20", "sum of holds")
	f.assertConservation(t, f.physioAccount)
}

// --- fulfilment ---------------------------------------------------------------------

func TestFulfilmentConsumesPartiallyThenFully(t *testing.T) {
	f := newFixture(t)
	requestID := f.seedRequest(t, f.providerOr, line{f.physioDefinition, "6"})
	view := f.authorize(t, requestID, "auth-consume")
	itemID := view.Items[0].ID

	recorded := f.record(t, view.Authorization.ID, itemID, "4")
	// Recording alone moves nothing: the entitlement is still held, not spent.
	b := f.balances(t, f.physioAccount)
	assertQuantity(t, b.Reserved, "6", "reserved after recording")
	assertQuantity(t, b.Consumed, "0", "consumed after recording")

	completed, err := f.svc.CompleteFulfilment(context.Background(), f.rc(),
		recorded.Fulfilment.ID, recorded.Fulfilment.RowVersion)
	if err != nil {
		t.Fatalf("complete fulfilment: %v", err)
	}
	if completed.Fulfilment.Status != "COMPLETED" {
		t.Fatalf("fulfilment status = %s, want COMPLETED", completed.Fulfilment.Status)
	}
	b = f.balances(t, f.physioAccount)
	assertQuantity(t, b.Consumed, "4", "consumed")
	// The remainder stays reserved: a member promised six and given four still holds two.
	assertQuantity(t, b.Reserved, "2", "reserved after a partial delivery")
	assertQuantity(t, b.Available, "14", "available")

	authorization, err := f.svc.Get(context.Background(), f.rc(), view.Authorization.ID)
	if err != nil {
		t.Fatalf("read authorization: %v", err)
	}
	if authorization.Authorization.Status != "PARTIALLY_USED" {
		t.Fatalf("authorization status = %s, want PARTIALLY_USED", authorization.Authorization.Status)
	}

	// Delivering the rest exhausts the promise.
	rest := f.record(t, view.Authorization.ID, itemID, "2")
	if _, err := f.svc.CompleteFulfilment(context.Background(), f.rc(),
		rest.Fulfilment.ID, rest.Fulfilment.RowVersion); err != nil {
		t.Fatalf("complete second fulfilment: %v", err)
	}
	authorization, err = f.svc.Get(context.Background(), f.rc(), view.Authorization.ID)
	if err != nil {
		t.Fatalf("read authorization: %v", err)
	}
	if authorization.Authorization.Status != "USED" {
		t.Fatalf("authorization status = %s, want USED", authorization.Authorization.Status)
	}
	b = f.balances(t, f.physioAccount)
	assertQuantity(t, b.Consumed, "6", "consumed")
	assertQuantity(t, b.Reserved, "0", "reserved")
	f.assertConservation(t, f.physioAccount)
}

func TestFulfilmentRefusesOverDelivery(t *testing.T) {
	f := newFixture(t)
	requestID := f.seedRequest(t, f.providerOr, line{f.physioDefinition, "2"})
	view := f.authorize(t, requestID, "auth-over")

	_, err := f.svc.RecordFulfilment(context.Background(), f.rc(), application.NewFulfilmentInput{
		AuthorizationID: view.Authorization.ID, PerformedAt: testNow,
		Items: items(view.Items[0].ID, "3"),
	})
	if !errors.Is(err, application.ErrOverFulfilment) {
		t.Fatalf("recording more than was approved: %v, want ErrOverFulfilment", err)
	}
	if n := f.count(t, `SELECT count(*) FROM service.fulfilment WHERE tenant_id = $1`, f.tenant); n != 0 {
		t.Fatalf("fulfilments = %d, want 0", n)
	}
	f.assertConservation(t, f.physioAccount)
}

func TestCancelFulfilmentReleasesNothingAndRefusesCompleted(t *testing.T) {
	f := newFixture(t)
	requestID := f.seedRequest(t, f.providerOr, line{f.physioDefinition, "2"})
	view := f.authorize(t, requestID, "auth-cancel-fulfilment")
	recorded := f.record(t, view.Authorization.ID, view.Items[0].ID, "1")

	cancelled, err := f.svc.CancelFulfilment(context.Background(), f.rc(), recorded.Fulfilment.ID,
		application.ReasonInput{ReasonCode: "NOT_ATTENDED", ExpectedVersion: recorded.Fulfilment.RowVersion})
	if err != nil {
		t.Fatalf("cancel fulfilment: %v", err)
	}
	if cancelled.Fulfilment.Status != "CANCELLED" {
		t.Fatalf("status = %s, want CANCELLED", cancelled.Fulfilment.Status)
	}
	// Nothing was consumed, so nothing changes on the ledger; the hold is still whole.
	assertQuantity(t, f.balances(t, f.physioAccount).Reserved, "2", "reserved")

	// A completed one is a different matter: undoing a consumption is a reversal.
	second := f.record(t, view.Authorization.ID, view.Items[0].ID, "1")
	completed, err := f.svc.CompleteFulfilment(context.Background(), f.rc(),
		second.Fulfilment.ID, second.Fulfilment.RowVersion)
	if err != nil {
		t.Fatalf("complete fulfilment: %v", err)
	}
	_, err = f.svc.CancelFulfilment(context.Background(), f.rc(), completed.Fulfilment.ID,
		application.ReasonInput{ReasonCode: "MISTAKE", ExpectedVersion: completed.Fulfilment.RowVersion})
	if !errors.Is(err, application.ErrFulfilmentCompleted) {
		t.Fatalf("cancelling a completed fulfilment: %v, want ErrFulfilmentCompleted", err)
	}
	f.assertConservation(t, f.physioAccount)
}

// --- cancellation, extension and expiry ---------------------------------------------

func TestCancelAuthorizationReleasesOutstandingHolds(t *testing.T) {
	f := newFixture(t)
	requestID := f.seedRequest(t, f.providerOr, line{f.physioDefinition, "5"})
	view := f.authorize(t, requestID, "auth-cancel")
	recorded := f.record(t, view.Authorization.ID, view.Items[0].ID, "2")
	if _, err := f.svc.CompleteFulfilment(context.Background(), f.rc(),
		recorded.Fulfilment.ID, recorded.Fulfilment.RowVersion); err != nil {
		t.Fatalf("complete fulfilment: %v", err)
	}
	current, err := f.svc.Get(context.Background(), f.rc(), view.Authorization.ID)
	if err != nil {
		t.Fatalf("read authorization: %v", err)
	}

	cancelled, err := f.svc.Cancel(context.Background(), f.rc(), view.Authorization.ID,
		application.ReasonInput{
			ReasonCode: "MEMBER_WITHDREW", ExpectedVersion: current.Authorization.RowVersion,
		})
	if err != nil {
		t.Fatalf("cancel authorization: %v", err)
	}
	if cancelled.Authorization.Status != "CANCELLED" {
		t.Fatalf("status = %s, want CANCELLED", cancelled.Authorization.Status)
	}

	b := f.balances(t, f.physioAccount)
	// What was delivered stays consumed; only the three that were never used come back.
	assertQuantity(t, b.Consumed, "2", "consumed")
	assertQuantity(t, b.Reserved, "0", "reserved")
	assertQuantity(t, b.Available, "18", "available")
	f.assertConservation(t, f.physioAccount)

	// Cancelling again is refused rather than releasing a second time.
	_, err = f.svc.Cancel(context.Background(), f.rc(), view.Authorization.ID,
		application.ReasonInput{ReasonCode: "AGAIN", ExpectedVersion: cancelled.Authorization.RowVersion})
	if !errors.Is(err, application.ErrAuthorizationNotActive) {
		t.Fatalf("second cancel: %v, want ErrAuthorizationNotActive", err)
	}
	assertQuantity(t, f.balances(t, f.physioAccount).Available, "18", "available after a second cancel")
}

func TestExtendAuthorizationMovesForwardOnly(t *testing.T) {
	f := newFixture(t)
	requestID := f.seedRequest(t, f.providerOr, line{f.physioDefinition, "1"})
	view := f.authorize(t, requestID, "auth-extend")

	_, err := f.svc.Extend(context.Background(), f.rc(), view.Authorization.ID, application.ExtendInput{
		ValidTo: testNow.Add(time.Hour), ExpectedVersion: view.Authorization.RowVersion,
	})
	if err == nil {
		t.Fatal("shortening an authorization must be refused")
	}
	extended, err := f.svc.Extend(context.Background(), f.rc(), view.Authorization.ID, application.ExtendInput{
		ValidTo: testNow.Add(72 * time.Hour), ReasonCode: "TREATMENT_DELAYED",
		ExpectedVersion: view.Authorization.RowVersion,
	})
	if err != nil {
		t.Fatalf("extend authorization: %v", err)
	}
	if !extended.Authorization.ValidTo.Equal(testNow.Add(72 * time.Hour)) {
		t.Fatalf("validTo = %s, want %s", extended.Authorization.ValidTo, testNow.Add(72*time.Hour))
	}

	// A cancelled authorization cannot be revived by moving its end.
	cancelled, err := f.svc.Cancel(context.Background(), f.rc(), view.Authorization.ID,
		application.ReasonInput{ReasonCode: "WITHDRAWN", ExpectedVersion: extended.Authorization.RowVersion})
	if err != nil {
		t.Fatalf("cancel authorization: %v", err)
	}
	_, err = f.svc.Extend(context.Background(), f.rc(), view.Authorization.ID, application.ExtendInput{
		ValidTo: testNow.Add(96 * time.Hour), ExpectedVersion: cancelled.Authorization.RowVersion,
	})
	if !errors.Is(err, application.ErrAuthorizationNotActive) {
		t.Fatalf("extending a cancelled authorization: %v, want ErrAuthorizationNotActive", err)
	}
}

// TestExpiryJobRunTwiceReleasesOnce is 2.3: the sweep is safe to run repeatedly.
func TestExpiryJobRunTwiceReleasesOnce(t *testing.T) {
	f := newFixture(t)
	requestID := f.seedRequest(t, f.providerOr, line{f.physioDefinition, "5"})
	if _, err := f.svc.Create(context.Background(), f.rc(), application.NewAuthorizationInput{
		RequestID: requestID, ValidFrom: ptr(testNow.Add(-2 * time.Hour)),
		ValidTo: testNow.Add(-time.Hour), IdempotencyKey: "auth-expire",
	}); err != nil {
		t.Fatalf("create authorization: %v", err)
	}
	assertQuantity(t, f.balances(t, f.physioAccount).Reserved, "5", "reserved before the sweep")

	first, err := f.svc.ExpireAuthorizations(context.Background(), testNow)
	if err != nil {
		t.Fatalf("expire (first run): %v", err)
	}
	if first != 1 {
		t.Fatalf("first run expired %d, want 1", first)
	}
	afterFirst := f.balances(t, f.physioAccount)
	assertQuantity(t, afterFirst.Reserved, "0", "reserved after the sweep")
	assertQuantity(t, afterFirst.Available, physioGrant, "available after the sweep")

	second, err := f.svc.ExpireAuthorizations(context.Background(), testNow)
	if err != nil {
		t.Fatalf("expire (second run): %v", err)
	}
	if second != 0 {
		t.Fatalf("second run expired %d, want 0", second)
	}
	if !f.balances(t, f.physioAccount).Equal(afterFirst) {
		t.Fatal("the second run changed the balances")
	}
	if n := f.count(t, `SELECT count(*) FROM service.authorization
	                     WHERE tenant_id = $1 AND status = 'EXPIRED'`, f.tenant); n != 1 {
		t.Fatalf("expired authorizations = %d, want 1", n)
	}
	// Exactly one RELEASE movement: running the job twice must not post a second.
	if n := f.count(t, `SELECT count(*) FROM benefit.entitlement_ledger
	                     WHERE entitlement_account_id = $1 AND movement_type = 'RELEASE'`,
		f.physioAccount); n != 1 {
		t.Fatalf("release movements = %d, want 1", n)
	}
	f.assertConservation(t, f.physioAccount)
}

// --- vouchers -----------------------------------------------------------------------

func TestVoucherTokenIsStoredNowhereAndRedeemsOnce(t *testing.T) {
	f := newFixture(t)
	requestID := f.seedRequest(t, f.providerOr, line{f.physioDefinition, "2"})
	view := f.authorize(t, requestID, "auth-voucher")

	issued, err := f.svc.IssueVoucher(context.Background(), f.rc(), application.IssueVoucherInput{
		AuthorizationID: view.Authorization.ID,
	})
	if err != nil {
		t.Fatalf("issue voucher: %v", err)
	}
	if issued.Token == "" {
		t.Fatal("the issue response must carry the plaintext token")
	}
	if strings.Contains(issued.Voucher.MaskedToken, issued.Token) {
		t.Fatal("the masked form must not contain the whole token")
	}

	// The plaintext appears in no column of any table.
	f.assertTokenAbsentFromDatabase(t, issued.Token)

	redeemed, err := f.svc.RedeemVoucher(context.Background(), f.rc(), application.RedeemVoucherInput{
		Token: issued.Token, PerformedAt: testNow,
		Items: items(view.Items[0].ID, "1"),
	})
	if err != nil {
		t.Fatalf("redeem voucher: %v", err)
	}
	if redeemed.Fulfilment.Status != "RECORDED" {
		t.Fatalf("fulfilment status = %s, want RECORDED", redeemed.Fulfilment.Status)
	}

	// A second redemption is refused, and no second fulfilment is left behind.
	_, err = f.svc.RedeemVoucher(context.Background(), f.rc(), application.RedeemVoucherInput{
		Token: issued.Token, PerformedAt: testNow,
		Items: items(view.Items[0].ID, "1"),
	})
	if !errors.Is(err, application.ErrVoucherAlreadyRedeemed) {
		t.Fatalf("second redemption: %v, want ErrVoucherAlreadyRedeemed", err)
	}
	if n := f.count(t, `SELECT count(*) FROM service.fulfilment WHERE tenant_id = $1`, f.tenant); n != 1 {
		t.Fatalf("fulfilments = %d, want 1", n)
	}

	// Still nothing anywhere, now that an audit row and a log line have been written too.
	f.assertTokenAbsentFromDatabase(t, issued.Token)
	if strings.Contains(f.logs.String(), issued.Token) {
		t.Fatal("the voucher token reached the log buffer")
	}
	f.assertConservation(t, f.physioAccount)
}

// TestVoucherRedemptionAndFulfilmentAreOneTransaction is 3.5: a fulfilment that fails
// leaves the voucher unredeemed, because both are written by the same transaction.
func TestVoucherRedemptionAndFulfilmentAreOneTransaction(t *testing.T) {
	f := newFixture(t)
	requestID := f.seedRequest(t, f.providerOr, line{f.physioDefinition, "2"})
	view := f.authorize(t, requestID, "auth-voucher-rollback")
	issued, err := f.svc.IssueVoucher(context.Background(), f.rc(), application.IssueVoucherInput{
		AuthorizationID: view.Authorization.ID,
	})
	if err != nil {
		t.Fatalf("issue voucher: %v", err)
	}

	// A line that belongs to no authorization fails the fulfilment, and with it the
	// redemption: the member walks away still holding a usable voucher.
	_, err = f.svc.RedeemVoucher(context.Background(), f.rc(), application.RedeemVoucherInput{
		Token: issued.Token, PerformedAt: testNow,
		Items: items(uuid.New(), "1"),
	})
	if !errors.Is(err, application.ErrItemNotInAuthorization) {
		t.Fatalf("redeeming with an unknown line: %v, want ErrItemNotInAuthorization", err)
	}
	var status string
	ctx, cancel := f.h.Ctx()
	defer cancel()
	if err := f.h.Admin.QueryRow(ctx, `SELECT status FROM service.voucher WHERE id = $1`,
		issued.Voucher.ID).Scan(&status); err != nil {
		t.Fatalf("read voucher: %v", err)
	}
	if status != "ISSUED" {
		t.Fatalf("voucher status = %s, want ISSUED", status)
	}
	if n := f.count(t, `SELECT count(*) FROM service.fulfilment WHERE tenant_id = $1`, f.tenant); n != 0 {
		t.Fatalf("fulfilments = %d, want 0", n)
	}

	// The same voucher still works afterwards.
	if _, err := f.svc.RedeemVoucher(context.Background(), f.rc(), application.RedeemVoucherInput{
		Token: issued.Token, PerformedAt: testNow,
		Items: items(view.Items[0].ID, "1"),
	}); err != nil {
		t.Fatalf("redeem after a failed attempt: %v", err)
	}
	f.assertConservation(t, f.physioAccount)
}

func TestVoucherOutsideItsWindowIsRefused(t *testing.T) {
	f := newFixture(t)
	requestID := f.seedRequest(t, f.providerOr, line{f.physioDefinition, "1"})
	view := f.authorize(t, requestID, "auth-voucher-window")
	issued, err := f.svc.IssueVoucher(context.Background(), f.rc(), application.IssueVoucherInput{
		AuthorizationID: view.Authorization.ID,
		ValidFrom:       ptr(testNow.Add(2 * time.Hour)), ValidTo: ptr(testNow.Add(4 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("issue voucher: %v", err)
	}
	_, err = f.svc.RedeemVoucher(context.Background(), f.rc(), application.RedeemVoucherInput{
		Token: issued.Token, PerformedAt: testNow,
		Items: items(view.Items[0].ID, "1"),
	})
	if !errors.Is(err, application.ErrVoucherExpired) {
		t.Fatalf("redeeming before the window opens: %v, want ErrVoucherExpired", err)
	}

	// A voucher may not outlive the hold behind it either.
	_, err = f.svc.IssueVoucher(context.Background(), f.rc(), application.IssueVoucherInput{
		AuthorizationID: view.Authorization.ID, ValidTo: ptr(testNow.Add(365 * 24 * time.Hour)),
	})
	if err == nil {
		t.Fatal("a voucher outliving its authorization must be refused")
	}
}

// assertTokenAbsentFromDatabase scans every column of every table of the tenant schemas
// for the plaintext. The whole row is cast to text, so a column added later is covered
// without anybody remembering to add it here.
func (f *fixture) assertTokenAbsentFromDatabase(t *testing.T, token string) {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	rows, err := f.h.Admin.Query(ctx, `
		SELECT n.nspname, c.relname
		  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relkind IN ('r','p')
		   AND n.nspname IN ('platform','directory','iam','party','benefit','catalog',
		                     'service','contract','provider','rules','workflow','audit','system')
		 ORDER BY 1, 2`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	type table struct{ schema, name string }
	var tables []table
	for rows.Next() {
		var tb table
		if err := rows.Scan(&tb.schema, &tb.name); err != nil {
			rows.Close()
			t.Fatalf("scan table: %v", err)
		}
		tables = append(tables, tb)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("list tables: %v", err)
	}
	if len(tables) == 0 {
		t.Fatal("no tables found to scan; the scan would prove nothing")
	}

	for _, tb := range tables {
		// position() rather than LIKE: a base64url token may contain an underscore, and
		// LIKE would read it as a wildcard.
		sql := fmt.Sprintf(`SELECT count(*) FROM %s.%s x WHERE position($1 in x::text) > 0`,
			pgIdent(tb.schema), pgIdent(tb.name))
		var n int
		if err := f.h.Admin.QueryRow(ctx, sql, token).Scan(&n); err != nil {
			t.Fatalf("scan %s.%s: %v", tb.schema, tb.name, err)
		}
		if n != 0 {
			t.Fatalf("the voucher plaintext appears in %s.%s (%d row(s))", tb.schema, tb.name, n)
		}
	}
}

// --- the provider boundary ----------------------------------------------------------

// TestProviderBoundaryHidesOtherProvidersAuthorization is the 404-not-403 rule: the row
// is never returned, so the caller cannot learn that it exists.
func TestProviderBoundaryHidesOtherProvidersAuthorization(t *testing.T) {
	f := newFixture(t)
	requestID := f.seedRequest(t, f.providerOr, line{f.physioDefinition, "1"})
	view := f.authorize(t, requestID, "auth-scope")

	if _, err := f.svc.Get(context.Background(), f.providerRC(f.providerOr), view.Authorization.ID); err != nil {
		t.Fatalf("the owning provider must see its own authorization: %v", err)
	}
	_, err := f.svc.Get(context.Background(), f.providerRC(f.otherOr), view.Authorization.ID)
	if !errors.Is(err, application.ErrAuthorizationNotFound) {
		t.Fatalf("another provider: %v, want ErrAuthorizationNotFound", err)
	}

	// A grant of ORGANIZATION scope that names no organization restricts to nothing.
	rc := f.rc()
	rc.Scopes = []identity.Scope{{Type: application.ScopeOrganization}}
	if _, err := f.svc.Get(context.Background(), rc, view.Authorization.ID); !errors.Is(err, application.ErrAuthorizationNotFound) {
		t.Fatalf("an empty organization grant: %v, want ErrAuthorizationNotFound", err)
	}
	page, err := f.svc.List(context.Background(), rc, application.AuthorizationFilter{})
	if err != nil {
		t.Fatalf("list with an empty organization grant: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("an empty organization grant listed %d authorizations, want 0", len(page.Items))
	}
}

// --- helpers ------------------------------------------------------------------------

// record writes one fulfilment line against an authorization.
func (f *fixture) record(t *testing.T, authorizationID, itemID uuid.UUID, quantity string) application.FulfilmentView {
	t.Helper()
	view, err := f.svc.RecordFulfilment(context.Background(), f.rc(), application.NewFulfilmentInput{
		AuthorizationID: authorizationID, PerformedAt: testNow,
		Items: items(itemID, quantity),
	})
	if err != nil {
		t.Fatalf("record fulfilment: %v", err)
	}
	return view
}

func ptr[T any](v T) *T { return &v }

// pgIdent quotes an identifier for the table scan above; the names come from the
// catalogue, so this only has to survive them, not arbitrary input.
func pgIdent(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// items is one delivered line, as the contract layer would have decoded it.
func items(authorizationItemID uuid.UUID, quantity string) []domain.FulfilmentItemInput {
	return []domain.FulfilmentItemInput{{
		AuthorizationItemID: authorizationItemID.String(), ActualQuantity: quantity,
	}}
}

// TestIssuingTwiceLeavesOneLiveVoucher is the hole the missing Idempotency-Key opens.
// Issuing carries no key on purpose — the idempotency middleware persists the response
// body, and the issue response is the only place the plaintext token exists — so nothing
// upstream deduplicates a double-clicked issue. Without a refusal here the member walks
// away holding two usable tokens for one promise.
func TestIssuingTwiceLeavesOneLiveVoucher(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	requestID := f.seedRequest(t, f.providerOr, line{f.physioDefinition, "2"})
	view := f.authorize(t, requestID, "auth-voucher-twice")

	first, err := f.svc.IssueVoucher(ctx, f.rc(), application.IssueVoucherInput{
		AuthorizationID: view.Authorization.ID,
	})
	if err != nil {
		t.Fatalf("issue voucher: %v", err)
	}

	if _, err := f.svc.IssueVoucher(ctx, f.rc(), application.IssueVoucherInput{
		AuthorizationID: view.Authorization.ID,
	}); !errors.Is(err, application.ErrVoucherAlreadyIssued) {
		t.Fatalf("second issue: %v, want ErrVoucherAlreadyIssued", err)
	}
	if n := f.count(t,
		`SELECT count(*) FROM service.voucher WHERE tenant_id = $1 AND status = 'ISSUED'`,
		f.tenant); n != 1 {
		t.Fatalf("live vouchers = %d, want 1", n)
	}

	// Once the first is spent the authorization may be given another, which is the case a
	// counter actually needs: the partial index restricts live vouchers, not all of them.
	if _, err := f.svc.RedeemVoucher(ctx, f.rc(), application.RedeemVoucherInput{
		Token: first.Token, PerformedAt: testNow, Items: items(view.Items[0].ID, "1"),
	}); err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if _, err := f.svc.IssueVoucher(ctx, f.rc(), application.IssueVoucherInput{
		AuthorizationID: view.Authorization.ID,
	}); err != nil {
		t.Fatalf("issuing a replacement for a spent voucher was refused: %v", err)
	}
}
