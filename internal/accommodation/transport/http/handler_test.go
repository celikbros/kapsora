// Package accommodationhttp_test drives the whole vertical over HTTP against a real
// database, because the thing under test is what leaves the process: the number a member is
// shown before they commit to anything, and the refusal a provider gets when it tries to
// take back a room it has already promised.
//
// The fixture is deliberately whole — a contracted provider, a member enrolled in a plan
// with a NIGHT entitlement, a mapping from the room's service to that entitlement, an
// allotment — because every one of those is a link in the chain the search walks, and a test
// that stubbed any of them would prove the stub works.
package accommodationhttp_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/accommodation/application"
	accommodationgw "github.com/celikbros/kapsora/internal/accommodation/infrastructure/gateway"
	accommodationpg "github.com/celikbros/kapsora/internal/accommodation/infrastructure/postgres"
	accommodationhttp "github.com/celikbros/kapsora/internal/accommodation/transport/http"
	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	authorizationapp "github.com/celikbros/kapsora/internal/authorization/application"
	authorizationpg "github.com/celikbros/kapsora/internal/authorization/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/benefit/eligibility"
	benefitledger "github.com/celikbros/kapsora/internal/benefit/ledger"
	contractapp "github.com/celikbros/kapsora/internal/contract/application"
	contractpg "github.com/celikbros/kapsora/internal/contract/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	servicerequestapp "github.com/celikbros/kapsora/internal/servicerequest/application"
	servicerequestpg "github.com/celikbros/kapsora/internal/servicerequest/infrastructure/postgres"
)

// permsHeader and scopeHeader let each request choose what the caller holds and which
// provider organization it is bound to, which is the whole subject of half these tests: the
// same URL answers differently for two callers and must.
const (
	permsHeader = "X-Test-Permissions"
	scopeHeader = "X-Test-Scope"
	// personHeader binds the caller to a person, the way iam.access_grant's PERSON scope
	// does for a member account.
	personHeader = "X-Test-Person"
)

const (
	// readerPermissions is what a member holds for this module: it may look and it may not
	// write.
	readerPermissions = "accommodation.property.read"
	// clerkPermissions is PROVIDER_RESERVATION's half: it reads and it opens allotments.
	clerkPermissions = "accommodation.property.read,accommodation.inventory.manage"
)

// The stay every test asks about: three nights in June 2026, inside the contract version and
// inside the plan version.
const (
	checkIn   = "2026-06-15"
	checkOut  = "2026-06-18"
	lastNight = "2026-06-17"
)

// testClock is the one moment every service in the fixture reads.
//
// It exists because the stay this fixture is built around is in June 2026 and the tests that
// follow it -- a cancellation inside a free window, a check-in inside its own hours, a
// no-show after they close -- are all decided by comparing *now* against that stay. A test
// that used the wall clock would answer differently in June than it does in September, which
// is not a test of anything.
//
// It is shared by the accommodation service, the authorization service and the contract
// service on purpose: a voucher's validity window is the authorization module's to judge, and
// a fixture whose desk believed it was June while its voucher store believed it was September
// would refuse every check-in for a reason no test wrote.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock { return &testClock{now: time.Now().UTC()} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Set moves the fixture's clock. Every service reading it moves together.
func (c *testClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t.UTC()
}

// At parses a moment written the way a test reads best -- "2026-06-13T09:00:00Z" -- and moves
// the clock to it.
func (c *testClock) At(t *testing.T, moment string) {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, moment)
	if err != nil {
		t.Fatalf("parse %q: %v", moment, err)
	}
	c.Set(parsed)
}

type denyRecorder struct{}

func (denyRecorder) Deny(w http.ResponseWriter, r *http.Request, err error, _ string) {
	status := http.StatusForbidden
	if errors.Is(err, identity.ErrUnauthenticated) {
		status = http.StatusUnauthorized
	}
	httpx.WriteProblem(w, r, httpx.Problem{
		Status: status, Code: "PERMISSION_DENIED", Title: "Yetki yok",
	})
}

type server struct {
	h       *dbtest.Harness
	handler http.Handler
	svc     *application.Service
	// requests is WP-I4-01's own service, so the booking tests can decide a reservation
	// request the way a reviewer does rather than writing its rows by hand.
	requests *servicerequestapp.Service
	// crowd is the five hundred separate members the oversell test holds with.
	crowd []uuid.UUID
	// clock is the moment every service in this fixture reads. Tests that are about *when*
	// something happened move it; the rest never touch it and run at the wall clock.
	clock *testClock

	tenant     uuid.UUID
	actor      uuid.UUID
	membership uuid.UUID

	// Two providers, so "another provider's room type is 404" is a question this fixture
	// can actually ask.
	providerOr uuid.UUID
	provider   uuid.UUID
	otherOr    uuid.UUID
	other      uuid.UUID

	person     uuid.UUID
	program    uuid.UUID
	definition uuid.UUID

	// The plan behind the member, promoted out of the fixture so the booking tests can
	// grant a bigger entitlement and add more members without a second seed.
	sponsor               uuid.UUID
	payer                 uuid.UUID
	plan                  uuid.UUID
	planVersion           uuid.UUID
	entitlementDefinition uuid.UUID
	enrollment            uuid.UUID
	account               uuid.UUID
	contractVersion       uuid.UUID

	property      uuid.UUID
	roomType      uuid.UUID
	gapRoomType   uuid.UUID
	otherProperty uuid.UUID
	otherRoomType uuid.UUID
	// inactiveProperty is ACTIVE-contracted and INACTIVE, so a member never sees it.
	inactiveProperty uuid.UUID
	inactiveRoomType uuid.UUID
}

func newServer(t *testing.T) *server {
	t.Helper()
	h := dbtest.New(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cursors, err := httpx.NewCursorCodec([]byte("accommodation-test-cursor-key"))
	if err != nil {
		t.Fatal(err)
	}
	eligibilitySvc, err := eligibility.New(eligibility.Deps{
		Pool: h.App, Audit: auditpg.New(), Ledger: benefitledger.NewLedger(time.Now), Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	clock := newTestClock()
	movements := benefitledger.NewLedger(clock.Now)
	authorizationSvc, err := authorizationapp.New(authorizationapp.Deps{
		Pool: h.App, Repo: authorizationpg.New(), Ledger: movements,
		Audit: auditpg.New(), Cursors: cursors, Logger: logger, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	requestSvc, err := servicerequestapp.New(servicerequestapp.Deps{
		Pool: h.App, Repo: servicerequestpg.New(), Audit: auditpg.New(),
		Cursors: cursors, Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	contractSvc, err := contractapp.New(contractapp.Deps{
		Pool: h.App, Repo: contractpg.New(), Audit: auditpg.New(), Cursors: cursors,
		Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The whole vertical, wired the way cmd/api wires it. The booking half is not stubbed
	// anywhere: the request really goes through WP-I4-01's gate, the authorization really
	// adopts the hold's reservation, and the voucher's digest really lands in
	// service.voucher — because every one of those is a link a stub would hide.
	svc, err := application.New(application.Deps{
		Pool: h.App, Repo: accommodationpg.New(), Bookings: accommodationpg.NewBookings(),
		Ledger: movements, Eligibility: eligibilitySvc,
		Requests:       accommodationgw.NewRequests(requestSvc),
		Authorizations: accommodationgw.NewAuthorizations(authorizationSvc),
		Policies:       accommodationgw.NewPolicies(contractSvc),
		WorkItems:      accommodationpg.NewWorkItems(logger),
		Audit:          auditpg.New(), Cursors: cursors, Logger: logger, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}

	s := &server{h: h, svc: svc, requests: requestSvc, clock: clock}
	s.tenant = h.CreateTenant("HTTP_ACC")
	s.actor = h.CreateActor("acc-http-clerk", "Accommodation Clerk")
	s.membership = h.CreateMembership(s.tenant, s.actor)
	s.seedWorld(t)

	handler := accommodationhttp.NewHandler(svc, denyRecorder{}, logger)

	// Stand-in for RequireTenantContext: the permissions, the provider scope and the person
	// binding all come from the test's own headers.
	fakeContext := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rc := identity.RequestContext{
				TenantID: s.tenant, MembershipID: s.membership,
				Principal:   identity.Principal{ActorID: s.actor},
				StepUpValid: true, Permissions: map[string]struct{}{},
			}
			for _, p := range strings.Split(r.Header.Get(permsHeader), ",") {
				if p != "" {
					rc.Permissions[p] = struct{}{}
				}
			}
			if raw := r.Header.Get(scopeHeader); raw != "" {
				rc.Scopes = append(rc.Scopes, identity.Scope{
					Type: "ORGANIZATION",
					ID:   uuid.NullUUID{UUID: uuid.MustParse(raw), Valid: true},
				})
			}
			if raw := r.Header.Get(personHeader); raw != "" {
				id := uuid.MustParse(raw)
				rc.PersonID = uuid.NullUUID{UUID: id, Valid: true}
				rc.Scopes = append(rc.Scopes, identity.Scope{
					Type: identity.ScopePerson, ID: rc.PersonID,
				})
			}
			next.ServeHTTP(w, r.WithContext(identity.WithRequestContext(r.Context(), rc)))
		})
	}

	router := chi.NewRouter()
	router.Use(fakeContext)
	router.Route("/api/v1/accommodation/properties", func(r chi.Router) {
		handler.PropertyRoutes(r, accommodationhttp.Middlewares{})
	})
	router.Route("/api/v1/accommodation/room-types", func(r chi.Router) {
		handler.RoomTypeRoutes(r, accommodationhttp.Middlewares{})
	})
	router.Route("/api/v1/accommodation/availability", func(r chi.Router) {
		handler.AvailabilityRoutes(r, accommodationhttp.Middlewares{})
	})
	router.Route("/api/v1/accommodation/holds", func(r chi.Router) {
		handler.HoldRoutes(r, accommodationhttp.BookingMiddlewares{})
	})
	router.Route("/api/v1/accommodation/bookings", func(r chi.Router) {
		handler.BookingRoutes(r, accommodationhttp.BookingMiddlewares{})
	})
	s.handler = router
	return s
}

// seedWorld writes everything the vertical hangs off but does not own: a sponsored member
// enrolled in a plan whose NIGHT entitlement holds two nights, a contracted provider pricing
// the room's service at 1000 a night with a 10 % member share, a second provider nobody
// contracted for this member, and three room types with three different allotments.
//
// The entitlement is deliberately two nights against a three-night stay, because that is the
// case the whole NIGHT-unit design exists for: the plan carries two nights and the member
// carries the third, and a quote that capped a lira amount at the number 2 would say
// something else entirely.
func (s *server) seedWorld(t *testing.T) { //nolint:funlen // one linear fixture reads better whole
	t.Helper()
	h := s.h
	ctx, cancel := h.Ctx()
	defer cancel()

	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		t.Helper()
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}

	sponsor := h.CreateTenantOrganization(s.tenant, "Sponsor", "SPONSOR")
	payer := h.CreateTenantOrganization(s.tenant, "Payer", "PAYER")
	s.sponsor, s.payer = sponsor, payer
	s.providerOr = h.CreateTenantOrganization(s.tenant, "Otel A", "PROVIDER")
	s.otherOr = h.CreateTenantOrganization(s.tenant, "Otel B", "PROVIDER")

	scan(&s.provider, "provider profile", `
		INSERT INTO provider.provider_profile (tenant_id, tenant_organization_id, provider_type, status)
		VALUES ($1, $2, 'HOTEL', 'ACTIVE') RETURNING id`, s.tenant, s.providerOr)
	scan(&s.other, "other provider profile", `
		INSERT INTO provider.provider_profile (tenant_id, tenant_organization_id, provider_type, status)
		VALUES ($1, $2, 'HOTEL', 'ACTIVE') RETURNING id`, s.tenant, s.otherOr)

	// The member.
	h.AdminExec(`INSERT INTO party.membership_type (tenant_id, code, display_name) VALUES ($1, 'MEMBER', 'Üye')`, s.tenant)
	scan(&s.person, "person", `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Deniz', 'Kara', 'deniz kara') RETURNING id`, s.tenant)
	var sponsorMembership uuid.UUID
	scan(&sponsorMembership, "sponsor membership", `
		INSERT INTO party.sponsor_membership (tenant_id, person_id, sponsor_tenant_organization_id,
		                                      membership_type, status, valid_period)
		VALUES ($1, $2, $3, 'MEMBER', 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		s.tenant, s.person, sponsor)

	h.AdminExec(`INSERT INTO benefit.program_type (tenant_id, code, display_name) VALUES ($1, 'BENEFIT', 'Fayda')`, s.tenant)
	scan(&s.program, "program", `
		INSERT INTO benefit.program (tenant_id, sponsor_tenant_organization_id, payer_tenant_organization_id,
		                             code, name, program_type, status, valid_period)
		VALUES ($1, $2, $3, 'TATIL', 'Tatil programı', 'BENEFIT', 'ACTIVE',
		        daterange('2026-01-01', NULL, '[)')) RETURNING id`, s.tenant, sponsor, payer)
	var plan, planVersion, entitlementDefinition, enrollment, account uuid.UUID
	defer func() {
		s.plan, s.planVersion = plan, planVersion
		s.entitlementDefinition, s.enrollment, s.account = entitlementDefinition, enrollment, account
	}()
	scan(&plan, "plan", `
		INSERT INTO benefit.plan (tenant_id, program_id, code, name, status)
		VALUES ($1, $2, 'PLAN', 'Plan', 'ACTIVE') RETURNING id`, s.tenant, s.program)
	// The version is written as a DRAFT and published at the end of this fixture, because
	// the entitlement mapping of WP-I5-05 is frozen the moment a version is published —
	// which is the rule that makes a booking's entitlement judgeable months later, and one
	// this test has no business going around.
	scan(&planVersion, "plan version", `
		INSERT INTO benefit.plan_version (tenant_id, plan_id, version_no, status, valid_period)
		VALUES ($1, $2, 1, 'DRAFT', daterange('2026-01-01','2027-01-01','[)'))
		RETURNING id`, s.tenant, plan)
	scan(&entitlementDefinition, "entitlement definition", `
		INSERT INTO benefit.entitlement_definition (tenant_id, plan_version_id, code, name, unit_type,
		                                            period_type, initial_quantity)
		VALUES ($1, $2, 'KONAKLAMA_GECE', 'Konaklama gecesi', 'NIGHT', 'CALENDAR_YEAR', 2)
		RETURNING id`, s.tenant, planVersion)
	scan(&enrollment, "enrollment", `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status, valid_period)
		VALUES ($1, $2, $3, 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		s.tenant, sponsorMembership, plan)
	scan(&account, "entitlement account", `
		INSERT INTO benefit.entitlement_account (tenant_id, enrollment_id, entitlement_definition_id,
		                                         benefit_period, total_granted, available_quantity)
		VALUES ($1, $2, $3, daterange('2026-01-01','2027-01-01','[)'), 2, 2) RETURNING id`,
		s.tenant, enrollment, entitlementDefinition)
	h.AdminExec(`
		INSERT INTO benefit.entitlement_ledger (tenant_id, entitlement_account_id, movement_type,
		                                        effective_at, delta_total, delta_available,
		                                        reference_type, reference_id, idempotency_key)
		VALUES ($1, $2, 'GRANT', clock_timestamp(), 2, 2, 'ENROLLMENT', $3, 'grant:seed')`,
		s.tenant, account, enrollment)

	// The catalogue service a room type is.
	var category uuid.UUID
	scan(&category, "service category", `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'KONAKLAMA', 'Konaklama', 'ACCOMMODATION') RETURNING id`, s.tenant)
	scan(&s.definition, "service definition", `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name,
		                                        fulfillment_mode, default_unit_type)
		VALUES ($1, $2, 'OTEL_GECE', 'Otel gecelemesi', 'RESERVATION', 'NIGHT') RETURNING id`,
		s.tenant, category)
	// The mapping of WP-I5-05: this is what turns the room's service into a NIGHT balance.
	h.AdminExec(`
		INSERT INTO benefit.service_entitlement_mapping (tenant_id, plan_version_id,
		                                                 service_definition_id,
		                                                 entitlement_definition_id, unit_factor)
		VALUES ($1, $2, $3, $4, 1)`, s.tenant, planVersion, s.definition, entitlementDefinition)
	// The draft becomes the published version the eligibility check resolves.
	h.AdminExec(`
		UPDATE benefit.plan_version
		   SET status = 'PUBLISHED', published_at = clock_timestamp(), published_by = $3
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, planVersion, s.actor)

	// The contract, its published version and the price of a night.
	var contract, contractVersion, priceList uuid.UUID
	scan(&contract, "contract", `
		INSERT INTO contract.contract (tenant_id, code, name, payer_organization_id,
		                               provider_profile_id, domain_code, status)
		VALUES ($1, 'KONAKLAMA_2026', 'Konaklama 2026', $2, $3, 'ACCOMMODATION', 'ACTIVE')
		RETURNING id`, s.tenant, payer, s.provider)
	defer func() { s.contractVersion = contractVersion }()
	scan(&contractVersion, "contract version", `
		INSERT INTO contract.contract_version (tenant_id, contract_id, version_no, status,
		                                       valid_from, valid_to, currency_code,
		                                       configuration_hash, published_at, published_by)
		VALUES ($1, $2, 1, 'PUBLISHED', '2026-01-01', '2027-01-01', 'TRY', 'deadbeef',
		        clock_timestamp(), $3) RETURNING id`, s.tenant, contract, s.actor)
	scan(&priceList, "price list", `
		INSERT INTO contract.price_list (tenant_id, contract_version_id, code, name)
		VALUES ($1, $2, 'STANDART', 'Standart liste') RETURNING id`, s.tenant, contractVersion)
	h.AdminExec(`
		INSERT INTO contract.price_item (tenant_id, price_list_id, service_definition_id, unit_type,
		                                 pricing_method, amount, member_share_method,
		                                 member_share_percent, valid_from)
		VALUES ($1, $2, $3, 'NIGHT', 'FIXED', 1000, 'PERCENT', 10, '2026-01-01')`,
		s.tenant, priceList, s.definition)

	// The second provider is fully contracted, published and priced — under a different
	// payer organization, for a programme this member is not enrolled in. That is what makes
	// the visibility test mean something: RAKIP is excluded by the payer behind the member's
	// own programme and by nothing else, so a search that dropped that narrowing would show
	// it and the test would go red.
	var otherPayer, otherContract, otherVersion, otherList uuid.UUID
	otherPayer = h.CreateTenantOrganization(s.tenant, "Diğer Payer", "PAYER")
	scan(&otherContract, "other contract", `
		INSERT INTO contract.contract (tenant_id, code, name, payer_organization_id,
		                               provider_profile_id, domain_code, status)
		VALUES ($1, 'RAKIP_2026', 'Rakip 2026', $2, $3, 'ACCOMMODATION', 'ACTIVE')
		RETURNING id`, s.tenant, otherPayer, s.other)
	scan(&otherVersion, "other contract version", `
		INSERT INTO contract.contract_version (tenant_id, contract_id, version_no, status,
		                                       valid_from, valid_to, currency_code,
		                                       configuration_hash, published_at, published_by)
		VALUES ($1, $2, 1, 'PUBLISHED', '2026-01-01', '2027-01-01', 'TRY', 'cafebabe',
		        clock_timestamp(), $3) RETURNING id`, s.tenant, otherContract, s.actor)
	scan(&otherList, "other price list", `
		INSERT INTO contract.price_list (tenant_id, contract_version_id, code, name)
		VALUES ($1, $2, 'STANDART', 'Standart liste') RETURNING id`, s.tenant, otherVersion)
	h.AdminExec(`
		INSERT INTO contract.price_item (tenant_id, price_list_id, service_definition_id, unit_type,
		                                 pricing_method, amount, member_share_method,
		                                 member_share_percent, valid_from)
		VALUES ($1, $2, $3, 'NIGHT', 'FIXED', 800, 'PERCENT', 10, '2026-01-01')`,
		s.tenant, otherList, s.definition)

	// The buildings.
	newProperty := func(dst *uuid.UUID, org uuid.UUID, code, name, status string) {
		scan(dst, "property "+code, `
			INSERT INTO accommodation.property (tenant_id, provider_organization_id, code, name,
			                                    property_type, timezone, city, region_code,
			                                    amenities, status)
			VALUES ($1, $2, $3, $4, 'HOTEL', 'Europe/Istanbul', 'Antalya', 'ANTALYA',
			        '["WIFI","POOL"]'::jsonb, $5) RETURNING id`, s.tenant, org, code, name, status)
	}
	newRoomType := func(dst *uuid.UUID, property uuid.UUID, code, name string) {
		scan(dst, "room type "+code, `
			INSERT INTO accommodation.room_type (tenant_id, property_id, code, name, max_adults,
			                                     max_children, max_occupancy, service_definition_id)
			VALUES ($1, $2, $3, $4, 2, 2, 4, $5) RETURNING id`,
			s.tenant, property, code, name, s.definition)
	}

	newProperty(&s.property, s.providerOr, "MERKEZ", "Merkez Otel", "ACTIVE")
	newProperty(&s.inactiveProperty, s.providerOr, "KAPALI", "Kapalı Otel", "INACTIVE")
	newProperty(&s.otherProperty, s.otherOr, "RAKIP", "Rakip Otel", "ACTIVE")
	newRoomType(&s.roomType, s.property, "STD", "Standart")
	newRoomType(&s.gapRoomType, s.property, "DELUX", "Deluxe")
	newRoomType(&s.inactiveRoomType, s.inactiveProperty, "STD", "Standart")
	newRoomType(&s.otherRoomType, s.otherProperty, "STD", "Standart")

	// The allotment. STD has four rooms free on the first two nights and two on the third,
	// so its answer for the stay is two. DELUX has ten rooms on two of the three nights and
	// no row at all on the second, so its answer is none.
	h.AdminExec(`
		INSERT INTO accommodation.inventory_day (tenant_id, room_type_id, stay_date, capacity, held, confirmed)
		VALUES ($1, $2, '2026-06-15', 5, 1, 0),
		       ($1, $2, '2026-06-16', 4, 0, 0),
		       ($1, $2, '2026-06-17', 3, 1, 0),
		       ($1, $3, '2026-06-15', 10, 0, 0),
		       ($1, $3, '2026-06-17', 10, 0, 0)`, s.tenant, s.roomType, s.gapRoomType)
	h.AdminExec(`
		INSERT INTO accommodation.inventory_day (tenant_id, room_type_id, stay_date, capacity)
		VALUES ($1, $2, '2026-06-15', 9), ($1, $2, '2026-06-16', 9), ($1, $2, '2026-06-17', 9)`,
		s.tenant, s.otherRoomType)
	h.AdminExec(`
		INSERT INTO accommodation.inventory_day (tenant_id, room_type_id, stay_date, capacity)
		VALUES ($1, $2, '2026-06-15', 9), ($1, $2, '2026-06-16', 9), ($1, $2, '2026-06-17', 9)`,
		s.tenant, s.inactiveRoomType)
}

func (s *server) do(t *testing.T, method, path, permissions string, body any,
	headers ...string,
) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set(permsHeader, permissions)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

// isError and asError are errors.Is and errors.As with the noise removed, because half the
// assertions in the booking tests are about which refusal came back.
func isError(err, target error) bool { return errors.Is(err, target) }

func asError(err error, target any) bool { return errors.As(err, target) }

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %T: %v\nbody: %s", out, err, rec.Body.String())
	}
	return out
}

// searchBody is the standard question: three nights for two adults at the contracted hotel.
func (s *server) searchBody() map[string]any {
	return map[string]any{
		"checkIn": checkIn, "checkOut": checkOut, "adults": 2,
		"propertyId": s.property.String(),
	}
}

// memberHeaders bind the caller to the seeded person, the way a member account's PERSON
// grant does.
func (s *server) memberHeaders() []string {
	return []string{personHeader, s.person.String()}
}

// ---------------------------------------------------------------------------
// The search
// ---------------------------------------------------------------------------

// TestSearchAnswersAvailabilityAndTheContributionQuote is the acceptance criterion of the
// work package: a member sees, before holding anything, what is free and what they would
// pay.
//
// Three nights at 1000 with a 10 % member share, against an entitlement holding two nights.
// The total is 3000; the plan carries 900 on each of the first two nights and nothing on the
// third; the member is left with 100 + 100 + 1000 = 1200. Every figure is asserted as an
// exact decimal string.
func TestSearchAnswersAvailabilityAndTheContributionQuote(t *testing.T) {
	s := newServer(t)

	rec := s.do(t, http.MethodPost, "/api/v1/accommodation/availability/search",
		readerPermissions, s.searchBody(), s.memberHeaders()...)
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	result := decode[kapsorav1.AvailabilitySearchResult](t, rec)

	if result.Nights != 3 {
		t.Errorf("nights = %d, want 3", result.Nights)
	}
	if result.EvaluationId == nil {
		t.Fatal("the search recorded no eligibility evaluation; what the member was shown " +
			"would be unanswerable later")
	}
	// The evaluation is a row, not a field: WP-I2-04's snapshot is what makes "what did the
	// system show them" answerable months afterwards.
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var stored int
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM benefit.eligibility_evaluation WHERE tenant_id = $1 AND id = $2`,
		s.tenant, *result.EvaluationId).Scan(&stored); err != nil {
		t.Fatalf("read the evaluation: %v", err)
	}
	if stored != 1 {
		t.Errorf("the evaluation the answer names is stored %d times, want once", stored)
	}

	if result.Entitlement == nil {
		t.Fatal("the answer carries no entitlement")
	}
	if result.Entitlement.Unit != "NIGHT" {
		t.Errorf("entitlement unit = %s, want NIGHT", result.Entitlement.Unit)
	}
	if result.Entitlement.Remaining != "2" {
		t.Errorf("remaining = %s, want 2", result.Entitlement.Remaining)
	}
	// Two nights left against a three-night stay is not eligible for the stay.
	if result.Eligible {
		t.Error("a three-night stay against two remaining nights reads as eligible")
	}

	byCode := map[string]kapsorav1.AvailabilityRoomTypeResult{}
	for _, item := range result.Results {
		byCode[item.RoomType.Code] = item
	}
	if len(byCode) != 2 {
		t.Fatalf("got %d room types, want the two of the contracted ACTIVE hotel: %v",
			len(byCode), byCode)
	}

	std, ok := byCode["STD"]
	if !ok {
		t.Fatal("STD is missing from the answer")
	}
	// 5-1 = 4, 4, 3-1 = 2: the minimum over the range.
	if std.Available != 2 {
		t.Errorf("STD available = %d, want the minimum over the range, 2", std.Available)
	}
	if std.Quote == nil {
		t.Fatalf("STD carries no quote: %v", std.QuoteUnavailableReason)
	}
	if std.Quote.CurrencyCode != "TRY" {
		t.Errorf("currency = %s, want TRY", std.Quote.CurrencyCode)
	}
	if std.Quote.TotalAmount != "3000" {
		t.Errorf("totalAmount = %s, want 3000", std.Quote.TotalAmount)
	}
	if std.Quote.PayerAmount != "1800" {
		t.Errorf("payerAmount = %s, want 1800 — the plan carries two of the three nights",
			std.Quote.PayerAmount)
	}
	if std.Quote.MemberAmount != "1200" {
		t.Errorf("memberAmount = %s, want 1200", std.Quote.MemberAmount)
	}
	if len(std.Quote.NightlyAmounts) != 3 {
		t.Fatalf("got %d nightly amounts, want 3", len(std.Quote.NightlyAmounts))
	}
	if got := std.Quote.NightlyAmounts[2].StayDate.Format(time.DateOnly); got != lastNight {
		t.Errorf("the last night is %s, want %s — check-out is not a night", got, lastNight)
	}
	if std.Quote.NightlyAmounts[2].PayerAmount != "0" {
		t.Errorf("the third night's payer share = %s, want 0",
			std.Quote.NightlyAmounts[2].PayerAmount)
	}

	// A room type with an allotment on two of the three nights is not available for the
	// stay, and it still carries its quote: what it would cost is a fact about the price,
	// not about the rooms.
	delux, ok := byCode["DELUX"]
	if !ok {
		t.Fatal("DELUX is missing from the answer")
	}
	if delux.Available != 0 {
		t.Errorf("DELUX available = %d, want 0 — it has no allotment on the middle night",
			delux.Available)
	}
	if delux.Quote == nil || delux.Quote.TotalAmount != "3000" {
		t.Errorf("DELUX carries no priced answer: %v", delux.QuoteUnavailableReason)
	}
}

// TestSearchShowsOnlyActiveContractedProperties is the visibility half. The second hotel is
// ACTIVE and has an allotment and a room type — it simply has no contract with this member's
// payer, so it does not exist for them. The third is contracted and INACTIVE.
func TestSearchShowsOnlyActiveContractedProperties(t *testing.T) {
	s := newServer(t)

	body := map[string]any{
		"checkIn": checkIn, "checkOut": checkOut, "adults": 2, "regionCode": "ANTALYA",
	}
	rec := s.do(t, http.MethodPost, "/api/v1/accommodation/availability/search",
		readerPermissions, body, s.memberHeaders()...)
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	result := decode[kapsorav1.AvailabilitySearchResult](t, rec)

	for _, item := range result.Results {
		if item.Property.Id == s.otherProperty {
			t.Error("the member was shown a hotel nobody contracted for their programme")
		}
		if item.Property.Id == s.inactiveProperty {
			t.Error("the member was shown an INACTIVE hotel")
		}
		if item.Property.Status != kapsorav1.PropertyStatus("ACTIVE") {
			t.Errorf("a %s property reached the answer", item.Property.Status)
		}
	}
	if len(result.Results) == 0 {
		t.Fatal("the region search found nothing at all; the fixture proves nothing")
	}
}

// TestSearchRefusesAMemberNamingSomebodyElse is the refusal the PERSON binding exists for.
// Without it a member could search — and then hold — a room for their neighbour.
func TestSearchRefusesAMemberNamingSomebodyElse(t *testing.T) {
	s := newServer(t)

	body := s.searchBody()
	body["personId"] = uuid.New().String()
	rec := s.do(t, http.MethodPost, "/api/v1/accommodation/availability/search",
		readerPermissions, body, s.memberHeaders()...)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("naming another person = %d, want 403\nbody: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "PERSON_SCOPE") {
		t.Errorf("the refusal is not PERSON_SCOPE: %s", rec.Body.String())
	}

	// Echoing back one's own person is fine: a client that read it from getMyPerson is
	// doing nothing wrong.
	body["personId"] = s.person.String()
	rec = s.do(t, http.MethodPost, "/api/v1/accommodation/availability/search",
		readerPermissions, body, s.memberHeaders()...)
	if rec.Code != http.StatusOK {
		t.Errorf("echoing one's own person = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
}

// TestSearchNeedsAPersonFromADeskThatIsBoundToNone. A reservation clerk is not a member and
// has to say whose stay it is; the search answers what one person may have, and there is no
// such answer without a person.
func TestSearchNeedsAPersonFromADeskThatIsBoundToNone(t *testing.T) {
	s := newServer(t)

	rec := s.do(t, http.MethodPost, "/api/v1/accommodation/availability/search",
		clerkPermissions, s.searchBody(), scopeHeader, s.providerOr.String())
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a desk naming nobody = %d, want 422\nbody: %s", rec.Code, rec.Body.String())
	}

	body := s.searchBody()
	body["personId"] = s.person.String()
	rec = s.do(t, http.MethodPost, "/api/v1/accommodation/availability/search",
		clerkPermissions, body, scopeHeader, s.providerOr.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("a desk naming the member = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
}

// TestSearchRefusesTheDateBoundaries over the wire, so the 422 a screen actually receives is
// the one the domain decided.
func TestSearchRefusesTheDateBoundaries(t *testing.T) {
	s := newServer(t)

	cases := []struct {
		name              string
		checkIn, checkOut string
	}{
		{"check-out on the day of check-in", "2026-06-15", "2026-06-15"},
		{"check-out before check-in", "2026-06-16", "2026-06-15"},
		{"thirty one nights", "2026-06-01", "2026-07-02"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := s.searchBody()
			body["checkIn"], body["checkOut"] = tc.checkIn, tc.checkOut
			rec := s.do(t, http.MethodPost, "/api/v1/accommodation/availability/search",
				readerPermissions, body, s.memberHeaders()...)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s = %d, want 422\nbody: %s", tc.name, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "checkOut") {
				t.Errorf("the refusal does not name checkOut: %s", rec.Body.String())
			}
		})
	}

	// And thirty nights, the tenant's default maximum, is allowed.
	body := s.searchBody()
	body["checkIn"], body["checkOut"] = "2026-06-01", "2026-07-01"
	rec := s.do(t, http.MethodPost, "/api/v1/accommodation/availability/search",
		readerPermissions, body, s.memberHeaders()...)
	if rec.Code != http.StatusOK {
		t.Fatalf("thirty nights = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// The allotment
// ---------------------------------------------------------------------------

// TestProviderOpensASeasonInOneCall is the other acceptance criterion: a provider opens a
// season's allotment in one call, and the database refuses any capacity below what is
// already held or confirmed.
func TestProviderOpensASeasonInOneCall(t *testing.T) {
	s := newServer(t)
	path := "/api/v1/accommodation/room-types/" + s.roomType.String() + "/inventory"

	rec := s.do(t, http.MethodPut, path, clerkPermissions,
		map[string]any{"from": "2026-08-01", "to": "2026-08-31", "capacity": 12},
		scopeHeader, s.providerOr.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("opening a season = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	opened := decode[kapsorav1.RoomTypeInventoryRange](t, rec)
	if len(opened.Days) != 31 {
		t.Fatalf("got %d days for August, want 31", len(opened.Days))
	}
	for _, day := range opened.Days {
		if !day.Allotted || day.Capacity != 12 || day.Available != 12 {
			t.Fatalf("%s is allotted=%t capacity=%d available=%d, want 12 free",
				day.StayDate.Format(time.DateOnly), day.Allotted, day.Capacity, day.Available)
		}
	}

	// The read answers every date of the range, gaps included, so "no allotment" is
	// visible rather than absent.
	rec = s.do(t, http.MethodGet, path+"?from=2026-07-30&to=2026-08-02", clerkPermissions, nil,
		scopeHeader, s.providerOr.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("reading the range = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	read := decode[kapsorav1.RoomTypeInventoryRange](t, rec)
	if len(read.Days) != 4 {
		t.Fatalf("got %d days for a four-day range", len(read.Days))
	}
	if read.Days[0].Allotted || read.Days[1].Allotted {
		t.Error("July shows as allotted; nothing was opened there")
	}
	if !read.Days[2].Allotted || !read.Days[3].Allotted {
		t.Error("August shows as unallotted")
	}
}

// TestAllotmentBelowCommitmentIsRefusedWithTheFirstOffendingDate. The June fixture holds one
// room on the 15th and one on the 17th; a season opened at one room clears the 15th and the
// 17th, and a season opened at zero fails on the 15th — which is the date the refusal has to
// name, because a provider opening ninety nights needs to know which night to look at.
func TestAllotmentBelowCommitmentIsRefusedWithTheFirstOffendingDate(t *testing.T) {
	s := newServer(t)
	path := "/api/v1/accommodation/room-types/" + s.roomType.String() + "/inventory"

	rec := s.do(t, http.MethodPut, path, clerkPermissions,
		map[string]any{"from": checkIn, "to": lastNight, "capacity": 0},
		scopeHeader, s.providerOr.String())
	if rec.Code != http.StatusConflict {
		t.Fatalf("lowering below a hold = %d, want 409\nbody: %s", rec.Code, rec.Body.String())
	}
	var problem struct {
		Code     string `json:"code"`
		StayDate string `json:"stayDate"`
		Held     int    `json:"held"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v\nbody: %s", err, rec.Body.String())
	}
	if problem.Code != "INVENTORY_BELOW_COMMITMENT" {
		t.Errorf("code = %s, want INVENTORY_BELOW_COMMITMENT", problem.Code)
	}
	if problem.StayDate != checkIn {
		t.Errorf("stayDate = %s, want the first offending night %s", problem.StayDate, checkIn)
	}
	if problem.Held != 1 {
		t.Errorf("held = %d, want 1", problem.Held)
	}

	// Nothing was written: a season is not half applied.
	rec = s.do(t, http.MethodGet, path+"?from="+checkIn+"&to="+lastNight, clerkPermissions, nil,
		scopeHeader, s.providerOr.String())
	after := decode[kapsorav1.RoomTypeInventoryRange](t, rec)
	if after.Days[1].Capacity != 4 {
		t.Errorf("the middle night's capacity is %d, want the original 4 — the refused "+
			"allotment wrote part of the range", after.Days[1].Capacity)
	}

	// And a capacity that clears every night of the range is accepted.
	rec = s.do(t, http.MethodPut, path, clerkPermissions,
		map[string]any{"from": checkIn, "to": lastNight, "capacity": 1},
		scopeHeader, s.providerOr.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("lowering to exactly the commitment = %d, want 200\nbody: %s",
			rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// The provider boundary
// ---------------------------------------------------------------------------

// TestAnotherProvidersRoomTypeIsNotFound. A provider clerk asking about somebody else's
// inventory is answered 404 and not 403: that another hotel's room type exists at all is not
// this caller's business.
func TestAnotherProvidersRoomTypeIsNotFound(t *testing.T) {
	s := newServer(t)
	own := s.providerOr.String()

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/accommodation/room-types/" + s.otherRoomType.String() +
			"/inventory?from=" + checkIn + "&to=" + lastNight},
		{http.MethodGet, "/api/v1/accommodation/properties/" + s.otherProperty.String()},
		{http.MethodGet, "/api/v1/accommodation/properties/" + s.otherProperty.String() + "/room-types"},
	} {
		rec := s.do(t, tc.method, tc.path, clerkPermissions, nil, scopeHeader, own)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404\nbody: %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}

	// The write is the same answer, so a clerk cannot discover another provider's rooms by
	// trying to change them.
	rec := s.do(t, http.MethodPut,
		"/api/v1/accommodation/room-types/"+s.otherRoomType.String()+"/inventory",
		clerkPermissions, map[string]any{"from": "2026-09-01", "to": "2026-09-02", "capacity": 3},
		scopeHeader, own)
	if rec.Code != http.StatusNotFound {
		t.Errorf("opening another provider's allotment = %d, want 404\nbody: %s",
			rec.Code, rec.Body.String())
	}

	// And nothing was written to it.
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var n int
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM accommodation.inventory_day
		 WHERE tenant_id = $1 AND room_type_id = $2 AND stay_date >= '2026-09-01'`,
		s.tenant, s.otherRoomType).Scan(&n); err != nil {
		t.Fatalf("read the other provider's allotment: %v", err)
	}
	if n != 0 {
		t.Errorf("%d nights were written into another provider's allotment", n)
	}
}

// TestProviderListSeesOnlyItsOwnBuildings. The narrowing is in the query rather than after
// it, so a page limit cannot leak somebody else's hotel into the answer.
func TestProviderListSeesOnlyItsOwnBuildings(t *testing.T) {
	s := newServer(t)

	rec := s.do(t, http.MethodGet, "/api/v1/accommodation/properties", clerkPermissions, nil,
		scopeHeader, s.providerOr.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	page := decode[kapsorav1.PropertyPage](t, rec)
	if len(page.Items) != 2 {
		t.Fatalf("the clerk sees %d properties, want its own two", len(page.Items))
	}
	for _, item := range page.Items {
		if item.ProviderOrganizationId != s.providerOr {
			t.Errorf("another provider's hotel %s reached the list", item.Code)
		}
	}

	// A tenant-wide caller sees all three.
	rec = s.do(t, http.MethodGet, "/api/v1/accommodation/properties", clerkPermissions, nil)
	page = decode[kapsorav1.PropertyPage](t, rec)
	if len(page.Items) != 3 {
		t.Errorf("a tenant-wide caller sees %d properties, want 3", len(page.Items))
	}
}

// TestProviderCannotOpenAHotelForAnotherOrganization is the one write with no existing row
// to hide behind a 404: the organization plainly exists, and the clerk may not write to it.
func TestProviderCannotOpenAHotelForAnotherOrganization(t *testing.T) {
	s := newServer(t)

	rec := s.do(t, http.MethodPost, "/api/v1/accommodation/properties", clerkPermissions,
		map[string]any{
			"providerOrganizationId": s.otherOr.String(), "code": "YENI", "name": "Yeni Otel",
			"propertyType": "HOTEL", "timezone": "Europe/Istanbul",
		}, scopeHeader, s.providerOr.String())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("creating a hotel for another organization = %d, want 403\nbody: %s",
			rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "PROPERTY_SCOPE") {
		t.Errorf("the refusal is not PROPERTY_SCOPE: %s", rec.Body.String())
	}
}

// TestDuplicateCodeIsAConflictAndNotAServerError. The unique constraints are the provider's
// own naming, and a clerk who reuses a code has to choose another — which is a 409 they can
// act on, not the 500 an unmapped constraint violation would be.
func TestDuplicateCodeIsAConflictAndNotAServerError(t *testing.T) {
	s := newServer(t)
	own := s.providerOr.String()

	rec := s.do(t, http.MethodPost, "/api/v1/accommodation/properties", clerkPermissions,
		map[string]any{
			"providerOrganizationId": own, "code": "MERKEZ", "name": "Kopya",
			"propertyType": "HOTEL", "timezone": "Europe/Istanbul",
		}, scopeHeader, own)
	if rec.Code != http.StatusConflict {
		t.Fatalf("a duplicate property code = %d, want 409\nbody: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "PROPERTY_CODE_TAKEN") {
		t.Errorf("the refusal is not PROPERTY_CODE_TAKEN: %s", rec.Body.String())
	}

	rec = s.do(t, http.MethodPost,
		"/api/v1/accommodation/properties/"+s.property.String()+"/room-types",
		clerkPermissions, map[string]any{
			"code": "STD", "name": "İkinci standart", "maxAdults": 2, "maxOccupancy": 2,
			"serviceDefinitionId": s.definition.String(),
		}, scopeHeader, own)
	if rec.Code != http.StatusConflict {
		t.Fatalf("a duplicate room type code = %d, want 409\nbody: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ROOM_TYPE_CODE_TAKEN") {
		t.Errorf("the refusal is not ROOM_TYPE_CODE_TAKEN: %s", rec.Body.String())
	}

	// The same code under another provider is not a conflict at all: two hotel chains both
	// calling their flagship MERKEZ is ordinary.
	rec = s.do(t, http.MethodPost, "/api/v1/accommodation/properties", clerkPermissions,
		map[string]any{
			"providerOrganizationId": s.otherOr.String(), "code": "MERKEZ", "name": "Rakip Merkez",
			"propertyType": "HOTEL", "timezone": "Europe/Istanbul",
		})
	if rec.Code != http.StatusCreated {
		t.Errorf("another provider reusing the code = %d, want 201\nbody: %s",
			rec.Code, rec.Body.String())
	}
}

// TestReadingIsNotWriting. accommodation.property.read gets a member into every list and no
// further; the allotment takes accommodation.inventory.manage.
func TestReadingIsNotWriting(t *testing.T) {
	s := newServer(t)

	rec := s.do(t, http.MethodPut,
		"/api/v1/accommodation/room-types/"+s.roomType.String()+"/inventory",
		readerPermissions, map[string]any{"from": "2026-09-01", "to": "2026-09-02", "capacity": 3},
		s.memberHeaders()...)
	if rec.Code != http.StatusForbidden {
		t.Errorf("a member opened an allotment: %d\nbody: %s", rec.Code, rec.Body.String())
	}

	rec = s.do(t, http.MethodPost, "/api/v1/accommodation/properties", readerPermissions,
		map[string]any{
			"providerOrganizationId": s.providerOr.String(), "code": "UYE", "name": "Üye Oteli",
			"propertyType": "HOTEL", "timezone": "Europe/Istanbul",
		}, s.memberHeaders()...)
	if rec.Code != http.StatusForbidden {
		t.Errorf("a member created a hotel: %d\nbody: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// The room type write
// ---------------------------------------------------------------------------

// TestRoomTypeMustBeANightService. A room type is priced and entitled as its service, so a
// service measured in anything but NIGHT is refused on the field rather than accepted and
// discovered when the first quote comes out in the wrong unit.
func TestRoomTypeMustBeANightService(t *testing.T) {
	s := newServer(t)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	var sessionDefinition uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name,
		                                        fulfillment_mode, default_unit_type)
		SELECT tenant_id, category_id, 'SEANS', 'Seans', 'SESSION', 'SESSION'
		  FROM catalog.service_definition WHERE tenant_id = $1 AND id = $2
		RETURNING id`, s.tenant, s.definition).Scan(&sessionDefinition); err != nil {
		t.Fatalf("seed a SESSION service: %v", err)
	}

	rec := s.do(t, http.MethodPost,
		"/api/v1/accommodation/properties/"+s.property.String()+"/room-types",
		clerkPermissions, map[string]any{
			"code": "SEANS", "name": "Seans odası", "maxAdults": 2, "maxOccupancy": 2,
			"serviceDefinitionId": sessionDefinition.String(),
		}, scopeHeader, s.providerOr.String())
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a SESSION-unit room type = %d, want 422\nbody: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "serviceDefinitionId") {
		t.Errorf("the refusal does not name serviceDefinitionId: %s", rec.Body.String())
	}
}

// TestUnknownAmenityIsAFieldError keeps the closed list closed at the edge of the process.
func TestUnknownAmenityIsAFieldError(t *testing.T) {
	s := newServer(t)

	rec := s.do(t, http.MethodPost, "/api/v1/accommodation/properties", clerkPermissions,
		map[string]any{
			"providerOrganizationId": s.providerOr.String(), "code": "YENI", "name": "Yeni Otel",
			"propertyType": "HOTEL", "timezone": "Europe/Istanbul",
			"amenities": []string{"WIFI", "MASSAGE_THERAPY"},
		}, scopeHeader, s.providerOr.String())
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an unknown amenity = %d, want 422\nbody: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "amenities[1]") {
		t.Errorf("the refusal does not name the offending position: %s", rec.Body.String())
	}
}

// TestUnknownTimezoneIsAFieldError. A property whose zone the server cannot resolve would
// have its nights counted against the wrong calendar, and nothing on any screen would say
// so.
func TestUnknownTimezoneIsAFieldError(t *testing.T) {
	s := newServer(t)

	rec := s.do(t, http.MethodPost, "/api/v1/accommodation/properties", clerkPermissions,
		map[string]any{
			"providerOrganizationId": s.providerOr.String(), "code": "YENI", "name": "Yeni Otel",
			"propertyType": "HOTEL", "timezone": "Mars/Olympus",
		}, scopeHeader, s.providerOr.String())
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an unresolvable zone = %d, want 422\nbody: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "timezone") {
		t.Errorf("the refusal does not name timezone: %s", rec.Body.String())
	}
}

// TestPatchPropertyNeedsTheCurrentVersion pins the optimistic concurrency the ETag exists
// for, and that clearing a nullable field is expressible.
func TestPatchPropertyNeedsTheCurrentVersion(t *testing.T) {
	s := newServer(t)
	path := "/api/v1/accommodation/properties/" + s.property.String()

	rec := s.do(t, http.MethodGet, path, clerkPermissions, nil, scopeHeader, s.providerOr.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("read = %d, want 200", rec.Code)
	}
	tag := rec.Header().Get("ETag")
	if tag == "" {
		t.Fatal("the read carries no ETag")
	}

	body := map[string]any{
		"name": "Merkez Otel", "propertyType": "RESORT", "timezone": "Europe/Istanbul",
		"status": "ACTIVE", "city": nil, "regionCode": "ANTALYA",
		"amenities": []string{"WIFI"},
	}
	// Without If-Match: 428.
	rec = s.do(t, http.MethodPatch, path, clerkPermissions, body, scopeHeader, s.providerOr.String())
	if rec.Code != http.StatusPreconditionRequired {
		t.Errorf("a patch with no If-Match = %d, want 428", rec.Code)
	}
	// With a stale one: 412.
	rec = s.do(t, http.MethodPatch, path, clerkPermissions, body,
		scopeHeader, s.providerOr.String(), "If-Match", `"999"`)
	if rec.Code != http.StatusPreconditionFailed {
		t.Errorf("a stale If-Match = %d, want 412", rec.Code)
	}
	// With the current one: 200, and the cleared field is really cleared.
	rec = s.do(t, http.MethodPatch, path, clerkPermissions, body,
		scopeHeader, s.providerOr.String(), "If-Match", tag)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	updated := decode[kapsorav1.Property](t, rec)
	if updated.City != nil {
		t.Errorf("city = %v, want cleared", *updated.City)
	}
	if updated.PropertyType != kapsorav1.PropertyType("RESORT") {
		t.Errorf("propertyType = %s, want RESORT", updated.PropertyType)
	}
	if len(updated.Amenities) != 1 {
		t.Errorf("amenities = %v, want the one that was sent", updated.Amenities)
	}
}
