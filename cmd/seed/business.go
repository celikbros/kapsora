package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	catalogapp "github.com/celikbros/kapsora/internal/catalog/application"
	catalogdomain "github.com/celikbros/kapsora/internal/catalog/domain"
	"github.com/celikbros/kapsora/internal/identity"
	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	orgapp "github.com/celikbros/kapsora/internal/organization/application"
	orgdomain "github.com/celikbros/kapsora/internal/organization/domain"
	"github.com/celikbros/kapsora/internal/platform/crypto"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/outbox"
	providerapp "github.com/celikbros/kapsora/internal/provider/application"
	providerdomain "github.com/celikbros/kapsora/internal/provider/domain"
	workflowapp "github.com/celikbros/kapsora/internal/workflow/application"
	workflowdomain "github.com/celikbros/kapsora/internal/workflow/domain"
)

// The demo business world (WP-I7-01..06), built entirely through the application services the
// HTTP handlers call.
//
// It exists so that the six scenarios of scripts/demo/KAPSORA-Demo-Rehberi.html can be walked
// against the real API and not only against the browser mock. Each scenario opens on a state
// somebody else has already left behind — an icmal already under review with one invoice still
// undecided, a settlement already waiting for approval, a reconciliation that already found a
// difference — and this file is the list of commands that produce exactly those states.
//
// Three rules hold everywhere below.
//
// **Nothing is written with SQL that a service can write.** Every organization, contract, price
// list, plan, enrollment, claim, invoice, icmal, settlement, reimbursement, property and
// inventory day here went through the same validation, the same gate and the same maker-checker
// rule a real one does. A row this seed can produce that the publishing gate would refuse would
// be a bug in the seed, not a fixture worth keeping.
//
// **Every step is idempotent by natural key.** A step looks for what it would create — a code, a
// number, a period — and creates only what is missing. Running `seed demo` twice leaves exactly
// what one run leaves, which is what makes it safe to run on a database that already has work in
// it.
//
// **Nothing here invents an identifier.** The VKNs are synthetic numbers that pass the checksum
// and belong to nobody, and they reach the database through the platform cipher and blind
// indexer exactly as a real one would.

// The synthetic tax numbers of the demo organizations. Each passes the Turkish VKN checksum and
// each is visibly not a real taxpayer's: a demo data set seeded with a real number would be a
// real number in every developer's database.
const (
	sponsorVKN  = "1111111114"
	payerVKN    = "2222222224"
	hospitalVKN = "3333333339"
	hotelVKN    = "4444444444"
)

// The tenant codes the demo organizations are found by. `DEMO_HOSPITAL` is the one the existing
// seed already creates and `provider.a` is already scoped to; the other three are this file's.
const (
	sponsorCode  = "DEMO_SPONSOR"
	payerCode    = "DEMO_PAYER"
	hospitalCode = "DEMO_HOSPITAL"
	hotelCode    = "DEMO_HOTEL"
)

// The service codes the demo catalogue carries. The first three are the ones cmd/seed/mapping.go
// already maps onto entitlements, so the mapping step finds something to map the moment a plan
// version exists; the fourth is the room type's, and it is measured in NIGHT because that is
// what a room is sold in.
const (
	serviceGPVisit      = "GP_VISIT"
	servicePhysio       = "PHYSIO_SESSION"
	serviceMRI          = "MRI_SCAN"
	serviceLodgingNight = "LODGING_NIGHT"
)

// The entitlement codes the plan grants. They are the codes cmd/seed/mapping.go names, plus the
// accommodation one, so a member's remaining nights and remaining lira are two balances rather
// than one.
const (
	entitlementMoney  = "HEALTH_MONEY"
	entitlementPhysio = "PHYSIO_SESSION"
	entitlementNights = "LODGING_NIGHT"
)

// scenario carries the ids one step hands the next. It is filled in order and every field is
// read back from the service that owns it, so a second run finds the same world rather than
// building a parallel one.
type scenario struct {
	tenant uuid.UUID

	// The people. Every maker-checker rule in this world is a rule about *which person*, so
	// the actors are kept apart rather than collapsed into one seeding identity.
	admin       uuid.UUID // admin.a: writes the drafts
	publisher   uuid.UUID // reviewer.a: publishes what admin.a submitted
	reviewer    uuid.UUID // financial.reviewer: decides invoices and icmals
	approver    uuid.UUID // payer.approver: releases the money
	billing     uuid.UUID // billing.a: raises invoices and sends icmals
	reservation uuid.UUID // reservation.a: keeps the hotel's allotment
	member      uuid.UUID // member.a: asks for the reimbursement
	provider    uuid.UUID // provider.a: the clinic desk that raises requests and reports

	sponsorOrg  uuid.UUID
	payerOrg    uuid.UUID
	hospitalOrg uuid.UUID
	hotelOrg    uuid.UUID

	hospitalProfile uuid.UUID
	hotelProfile    uuid.UUID

	hospitalVersion uuid.UUID // the PUBLISHED contract version the claims are priced against
	hotelVersion    uuid.UUID // the PUBLISHED contract version the lodging terms live on

	services map[string]uuid.UUID // service definition id by code

	person      uuid.UUID
	membership  uuid.UUID
	program     uuid.UUID
	plan        uuid.UUID
	planVersion uuid.UUID
	enrollment  uuid.UUID

	healthCase uuid.UUID
}

// seedClock is the seed's own clock. Every service that stamps a date is given it, so the demo
// world can be dated the way a few weeks of business would be rather than all at one instant:
// an icmal decided a month ago has a settlement that fell due today, and a settlement paid a
// week ago has a reconciliation run that balanced.
//
// It is a field rather than a parameter because the services are built once and the steps move
// the clock between them.
type seedClock struct{ at time.Time }

func (c *seedClock) now() time.Time { return c.at }

// day truncates to midnight UTC, which is the granularity every period, due date and inventory
// day in this file is expressed at.
func day(t time.Time) time.Time { return t.UTC().Truncate(24 * time.Hour) }

// rcTenant is a tenant-wide caller acting as one actor. It carries no organization scope, so the
// services read it as unrestricted — which is what a payer-side role is.
//
// `StepUpValid` is on because every command the seed gives is one the operator would have
// re-entered their password for. A demo world that could not contain a large icmal would make
// every screen in it about small numbers.
func rcTenant(tenantID, actorID uuid.UUID, permissions ...string) identity.RequestContext {
	perms := make(map[string]struct{}, len(permissions))
	for _, p := range permissions {
		perms[p] = struct{}{}
	}
	return identity.RequestContext{
		TenantID:    tenantID,
		Principal:   identity.Principal{ActorID: actorID},
		Permissions: perms,
		StepUpValid: true,
	}
}

// rcOrganization is a provider-side caller: the same context bounded to one organization by an
// ORGANIZATION scope, exactly as PROVIDER_BILLING and PROVIDER_RESERVATION are issued.
func rcOrganization(tenantID, actorID, organizationID uuid.UUID,
	permissions ...string,
) identity.RequestContext {
	rc := rcTenant(tenantID, actorID, permissions...)
	rc.Scopes = []identity.Scope{{
		Type: identityapp.ScopeOrganization,
		ID:   uuid.NullUUID{UUID: organizationID, Valid: true},
	}}
	return rc
}

// rcPerson is the member, bound to their own person by a PERSON scope. It is what
// `identity.RequirePerson` reads, so a seed command that named somebody else would be refused by
// the same code the transport uses.
func rcPerson(tenantID, actorID, personID uuid.UUID,
	permissions ...string,
) identity.RequestContext {
	rc := rcTenant(tenantID, actorID, permissions...)
	rc.PersonID = uuid.NullUUID{UUID: personID, Valid: true}
	rc.Scopes = []identity.Scope{{
		Type: identityapp.ScopePerson,
		ID:   uuid.NullUUID{UUID: personID, Valid: true},
	}}
	return rc
}

// step prints one line of the seed's report in the shape every other step uses.
func step(kind, name, state string) {
	fmt.Printf("%-7s %-22s %s\n", kind, name, state)
}

// created renders the two words every idempotent step ends on.
func created(isNew bool, newWord, oldWord string) string {
	if isNew {
		return newWord
	}
	return oldWord
}

// ensureBusinessScenario builds the whole demo world of the six scenarios, in the order one
// thing needs another: the catalogue, the organizations, the contract, the plan, the member's
// enrollment, the claims, the invoices and icmals, the settlements, the reimbursement, the
// hotel's allotment, and finally the reconciliation that reads all of it.
func (s *seeder) ensureBusinessScenario(ctx context.Context, tenantID uuid.UUID,
	actors map[string]uuid.UUID,
) error {
	if s.biz == nil {
		return errors.New("seed: the business verticals are not wired")
	}
	sc := &scenario{
		tenant:      tenantID,
		admin:       actors["admin.a"],
		publisher:   actors["reviewer.a"],
		reviewer:    actors["financial.reviewer"],
		approver:    actors["payer.approver"],
		billing:     actors["billing.a"],
		reservation: actors["reservation.a"],
		member:      actors["member.a"],
		provider:    actors["provider.a"],
		services:    map[string]uuid.UUID{},
	}
	steps := []struct {
		name string
		run  func(context.Context, *scenario) error
	}{
		{"work queues", s.ensureWorkQueues},
		{"catalogue", s.ensureServiceCatalogue},
		{"organizations", s.ensureDemoOrganizations},
		{"provider profiles", s.ensureProviderProfiles},
		{"contracts", s.ensureDemoContracts},
		{"program and plan", s.ensureDemoProgram},
		{"enrollment", s.ensureDemoEnrollment},
		{"claims and billing", s.ensureBillingStory},
		{"reimbursement", s.ensureDemoReimbursement},
		{"accommodation", s.ensureDemoAccommodation},
		{"reconciliation", s.ensureDemoReconciliation},
		// Last, and on its own: the staff member's files belong to a second person, and none of
		// the steps above reads anything of hers.
		{"staff member", s.ensureStaffMemberFiles},
	}
	for _, st := range steps {
		if err := st.run(ctx, sc); err != nil {
			return fmt.Errorf("%s: %s", st.name, describeValidation(err))
		}
	}
	return nil
}

// ensureWorkQueues opens the five queues the demo world raises work into. A tenant with no queue
// is not an error anywhere in this codebase — a claim still routes and a reconciliation run is
// still written — but the item is then raised into nothing, and a demo whose review queues are
// empty is a demo that cannot show anybody where their work waits.
func (s *seeder) ensureWorkQueues(ctx context.Context, sc *scenario) error {
	rc := rcTenant(sc.tenant, sc.admin, workflowapp.PermissionQueueManage,
		workflowapp.PermissionRead)
	// Each queue names the permission its work takes, so the worklist shows it to the people
	// who can do that work and to nobody else (migration 000048).
	queues := []struct{ code, name, domain, permission string }{
		{"MEDICAL_REVIEW", "Tıbbi değerlendirme", "HEALTH", "health.medical_report.review"},
		{"FINANCIAL_REVIEW", "Mali değerlendirme", "HEALTH", "claim.financial.review"},
		{"BATCH_REVIEW", "İcmal incelemesi", "GENERIC", "batch.review"},
		{"RECONCILIATION_DIFFERENCE", "Mutabakat farkı", "GENERIC", "settlement.read"},
		{"RESERVATION_REVIEW", "Rezervasyon incelemesi", "ACCOMMODATION", "accommodation.booking.manage"},
	}
	page, err := s.biz.workflows.ListQueues(ctx, rc, workflowapp.QueueFilter{Limit: 200})
	if err != nil {
		return fmt.Errorf("list queues: %w", err)
	}
	opened, repermissioned := 0, 0
	for _, q := range queues {
		if existing, ok := queueByCode(page.Items, q.code); ok {
			// A queue opened before migration 000048 carries the default permission, which
			// shows its work to every worklist reader. Give it the one its work takes.
			if existing.RequiredPermission == q.permission {
				continue
			}
			permission := q.permission
			if _, err := s.biz.workflows.PatchQueue(ctx, rc, existing.ID,
				workflowdomain.QueuePatch{RequiredPermission: &permission}, existing.RowVersion); err != nil {
				return fmt.Errorf("set the permission of queue %s: %w", q.code, err)
			}
			repermissioned++
			continue
		}
		sla := 1440
		if _, err := s.biz.workflows.CreateQueue(ctx, rc, workflowapp.NewQueueInput{
			Code: q.code, Name: q.name, DomainCode: q.domain,
			AssignmentPolicy: "MANUAL", SLAMinutes: &sla,
			RequiredPermission: q.permission,
		}); err != nil {
			return fmt.Errorf("create queue %s: %w", q.code, err)
		}
		opened++
	}
	step("queue", "work queues", fmt.Sprintf("%d opened, %d already there, %d re-permissioned",
		opened, len(queues)-opened, repermissioned))
	return nil
}

func queueByCode(items []workflowapp.QueueRecord, code string) (workflowapp.QueueRecord, bool) {
	for _, item := range items {
		if item.Code == code {
			return item, true
		}
	}
	return workflowapp.QueueRecord{}, false
}

// ensureServiceCatalogue writes the four services the demo world sells: three health services
// and the room night. They carry the codes cmd/seed/mapping.go already maps onto entitlements,
// which is what makes a mapped service answer "Uygun" on an eligibility screen.
func (s *seeder) ensureServiceCatalogue(ctx context.Context, sc *scenario) error {
	rc := rcTenant(sc.tenant, sc.admin)
	health, err := s.ensureCategory(ctx, rc, "HEALTH_ROOT", "Sağlık hizmetleri", "HEALTH")
	if err != nil {
		return err
	}
	lodging, err := s.ensureCategory(ctx, rc, "ACCOMMODATION_ROOT", "Konaklama", "ACCOMMODATION")
	if err != nil {
		return err
	}
	definitions := []struct {
		category         uuid.UUID
		code, name, unit string
	}{
		{health, serviceGPVisit, "Poliklinik muayenesi", "COUNT"},
		{health, servicePhysio, "Fizyoterapi seansı", "SESSION"},
		{health, serviceMRI, "Manyetik rezonans görüntüleme", "COUNT"},
		{lodging, serviceLodgingNight, "Konaklama gecesi", "NIGHT"},
	}
	fresh := 0
	for _, d := range definitions {
		id, isNew, err := s.ensureServiceDefinition(ctx, rc, d.category, d.code, d.name, d.unit)
		if err != nil {
			return err
		}
		sc.services[d.code] = id
		if isNew {
			fresh++
		}
	}
	step("catalog", "demo services", fmt.Sprintf("%d created, %d already there",
		fresh, len(definitions)-fresh))
	return nil
}

func (s *seeder) ensureCategory(ctx context.Context, rc identity.RequestContext,
	code, name, domainCode string,
) (uuid.UUID, error) {
	page, err := s.catalog.ListCategories(ctx, rc, catalogapp.ListFilter{Query: code, Limit: 50})
	if err != nil {
		return uuid.Nil, fmt.Errorf("list categories: %w", err)
	}
	for _, item := range page.Items {
		if item.Code == code {
			return item.ID, nil
		}
	}
	record, err := s.catalog.CreateCategory(ctx, rc, catalogdomain.NewCategory{
		Code: code, Name: name, Domain: domainCode, Active: true,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("create category %s: %w", code, err)
	}
	return record.ID, nil
}

func (s *seeder) ensureServiceDefinition(ctx context.Context, rc identity.RequestContext,
	categoryID uuid.UUID, code, name, unit string,
) (uuid.UUID, bool, error) {
	page, err := s.catalog.ListDefinitions(ctx, rc, catalogapp.ListFilter{Query: code, Limit: 50})
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("list service definitions: %w", err)
	}
	for _, item := range page.Items {
		if item.Code == code {
			return item.ID, false, nil
		}
	}
	record, err := s.catalog.CreateDefinition(ctx, rc, catalogdomain.NewDefinition{
		CategoryID: categoryID.String(), Code: code, Name: name,
		FulfillmentMode: "DIRECT", DefaultUnitType: unit, RequiresProvider: true, Active: true,
	})
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("create service definition %s: %w", code, err)
	}
	return record.ID, true, nil
}

// ensureDemoOrganizations finds or creates the four organizations the world needs and makes sure
// every one of them carries a tax identity.
//
// The hospital is the one the existing seed already created through the identity provisioner, so
// it is found rather than created and only its missing VKN is filled in; the other three go
// through WP-I1-03's own create, which deduplicates on the tax number's blind index.
func (s *seeder) ensureDemoOrganizations(ctx context.Context, sc *scenario) error {
	rc := rcTenant(sc.tenant, sc.admin)
	var err error
	if sc.sponsorOrg, err = s.ensureOrganization(ctx, rc, sponsorCode, "Demo Sponsor Holding A.Ş.",
		"SPONSOR", "SPONSOR", sponsorVKN); err != nil {
		return err
	}
	if sc.payerOrg, err = s.ensureOrganization(ctx, rc, payerCode, "Demo Ödeyici Sigorta A.Ş.",
		"PAYER", "INSURER", payerVKN); err != nil {
		return err
	}
	// The hospital already exists: `demo()` creates it with the identity provisioner so that
	// `provider.a` and `billing.a` are scoped to one and the same relationship row.
	if sc.hospitalOrg, err = s.provisioner.EnsureProviderOrganization(ctx, sc.tenant,
		hospitalCode, "Demo Hastane"); err != nil {
		return err
	}
	if err := s.ensureTaxIdentity(ctx, sc.tenant, sc.hospitalOrg, hospitalVKN); err != nil {
		return err
	}
	if sc.hotelOrg, err = s.ensureOrganization(ctx, rc, hotelCode, "Demo Otel İşletmeleri A.Ş.",
		"PROVIDER", "PROVIDER", hotelVKN); err != nil {
		return err
	}
	// `reservation.a` is granted here rather than with the other roles in `demo()` because the
	// organization it is scoped to is created two lines above: a PROVIDER_RESERVATION grant
	// naming no organization would be a desk clerk who may keep nobody's allotment.
	grantedReservation, err := s.provisioner.GrantRole(ctx, identityapp.GrantRoleInput{
		TenantID: sc.tenant, ActorID: sc.reservation, RoleCode: "PROVIDER_RESERVATION",
		ScopeType: identityapp.ScopeOrganization,
		ScopeID:   uuid.NullUUID{UUID: sc.hotelOrg, Valid: true},
		Reason:    "seed demo hotel desk",
	})
	if err != nil {
		return fmt.Errorf("grant PROVIDER_RESERVATION on the demo hotel: %w", err)
	}
	step("grant", "PROVIDER_RESERVATION", created(grantedReservation, "granted", "exists")+" on "+hotelCode)
	return nil
}

// ensureOrganization finds a relationship by the tenant code it was given, or creates it.
//
// The lookup is by display name because that is what the list read offers; the tenant code is
// checked on the row itself, so two organizations with similar names never collapse into one.
func (s *seeder) ensureOrganization(ctx context.Context, rc identity.RequestContext,
	tenantCode, legalName, role, kind, vkn string,
) (uuid.UUID, error) {
	page, err := s.biz.organizations.List(ctx, rc, orgapp.ListFilter{Role: role, Limit: 100})
	if err != nil {
		return uuid.Nil, fmt.Errorf("list organizations: %w", err)
	}
	for _, item := range page.Items {
		full, err := s.biz.organizations.Get(ctx, rc, item.ID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("read organization %s: %w", item.ID, err)
		}
		if full.TenantCode != nil && *full.TenantCode == tenantCode {
			step("org", tenantCode, "exists")
			return full.ID, nil
		}
	}
	record, err := s.biz.organizations.Create(ctx, rc, orgdomain.NewOrganization{
		LegalName: legalName, DisplayName: legalName, OrganizationKind: kind,
		RelationshipRole: role, CountryCode: "TR", TenantCode: tenantCode,
		Identifiers: []orgdomain.Identifier{
			{Type: orgdomain.IdentifierVKN, Value: vkn, Primary: true},
		},
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("create organization %s: %w", tenantCode, err)
	}
	step("org", tenantCode, "created")
	return record.ID, nil
}

// ensureTaxIdentity gives an organization created without one the VKN it needs to be invoiced
// against. The number goes through the platform cipher and the global blind index exactly as
// WP-I1-03's own create does; nothing here ever writes a tax number in the clear.
func (s *seeder) ensureTaxIdentity(ctx context.Context, tenantID, relationshipID uuid.UUID,
	vkn string,
) error {
	if err := orgdomain.Validate(orgdomain.IdentifierVKN, vkn); err != nil {
		return fmt.Errorf("the demo VKN is not a VKN: %w", err)
	}
	hash, err := s.keys.GlobalIndex(ctx, crypto.PurposeOrganizationTax, vkn)
	if err != nil {
		return fmt.Errorf("index the demo VKN: %w", err)
	}
	cipher, err := s.keys.Encrypt(ctx, uuid.Nil, crypto.PurposeOrganizationTax, []byte(vkn))
	if err != nil {
		return fmt.Errorf("encrypt the demo VKN: %w", err)
	}
	written, err := s.provisioner.EnsureOrganizationTaxIdentity(ctx, tenantID, relationshipID,
		cipher, hash)
	if err != nil {
		return err
	}
	step("org", hospitalCode, created(written, "tax identity written", "tax identity exists"))
	return nil
}

// ensureProviderProfiles gives the hospital and the hotel the provider profile a contract hangs
// off. Without one there is no contract, and without a contract there is no price and no payment
// term — which is to say no claim anybody can price and no settlement anybody can date.
func (s *seeder) ensureProviderProfiles(ctx context.Context, sc *scenario) error {
	rc := rcTenant(sc.tenant, sc.admin)
	var err error
	if sc.hospitalProfile, err = s.ensureProviderProfile(ctx, rc, sc.hospitalOrg, "HOSPITAL",
		"Demo Hastane"); err != nil {
		return err
	}
	if sc.hotelProfile, err = s.ensureProviderProfile(ctx, rc, sc.hotelOrg, "HOTEL",
		"Demo Otel"); err != nil {
		return err
	}
	return nil
}

func (s *seeder) ensureProviderProfile(ctx context.Context, rc identity.RequestContext,
	organizationID uuid.UUID, providerType, label string,
) (uuid.UUID, error) {
	page, err := s.biz.providers.ListProviders(ctx, rc, providerapp.ListFilter{Limit: 100})
	if err != nil {
		return uuid.Nil, fmt.Errorf("list providers: %w", err)
	}
	for _, item := range page.Items {
		if item.TenantOrganizationID == organizationID {
			step("provider", label, "exists")
			return item.ID, s.activateProvider(ctx, rc, item.ID, item.Status, item.RowVersion, label)
		}
	}
	record, err := s.biz.providers.CreateProvider(ctx, rc, providerdomain.NewProvider{
		TenantOrganizationID: organizationID.String(), ProviderType: providerType,
		NetworkTier: "STANDARD",
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("create provider %s: %w", label, err)
	}
	step("provider", label, "created")
	return record.ID, s.activateProvider(ctx, rc, record.ID, record.Status, record.RowVersion, label)
}

// activateProvider moves a new provider profile from PENDING to ACTIVE.
//
// A profile is created PENDING because somebody has to check the paperwork, and two reads in
// this platform narrow to ACTIVE and nothing else: the availability search, which will not offer
// a room of a provider nobody has admitted yet, and the price selection behind every claim. A
// demo world whose providers were left PENDING would answer every search with nothing at all.
func (s *seeder) activateProvider(ctx context.Context, rc identity.RequestContext,
	providerID uuid.UUID, status string, rowVersion int64, label string,
) error {
	if status != providerdomain.StatusPending {
		return nil
	}
	if _, err := s.biz.providers.MoveProvider(ctx, rc, providerID, providerapp.CommandActivate,
		rowVersion, "", ""); err != nil {
		return fmt.Errorf("activate provider %s: %w", label, err)
	}
	step("provider", label, "active")
	return nil
}

// readOutboxEvent reads back the event a command published, so a handler the seed runs is given
// the payload the command actually wrote rather than one this file made up.
//
// It is the only read in this file that is not a service call, and it is a read: system.outbox
// is the platform's own table and no application service exposes it. The transaction is
// tenant-bound because the table has RLS forced, and a query outside one would quietly return
// nothing rather than fail.
func (s *seeder) readOutboxEvent(ctx context.Context, tenantID, aggregateID uuid.UUID,
	eventType string,
) (outbox.Delivery, error) {
	var out outbox.Delivery
	err := db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenantID},
		func(ctx context.Context, tx pgx.Tx) error {
			var payload []byte
			var aggregateType string
			var occurredAt time.Time
			err := tx.QueryRow(ctx, `
				SELECT aggregate_type, payload_json, occurred_at
				  FROM system.outbox_event
				 WHERE tenant_id = $1 AND aggregate_id = $2 AND event_type = $3
				 ORDER BY occurred_at DESC, id DESC
				 LIMIT 1`, tenantID, aggregateID, eventType).
				Scan(&aggregateType, &payload, &occurredAt)
			if err != nil {
				return fmt.Errorf("read the %s event of %s: %w", eventType, aggregateID, err)
			}
			out = outbox.Delivery{
				ID:            uuid.New(),
				TenantID:      uuid.NullUUID{UUID: tenantID, Valid: true},
				AggregateType: aggregateType,
				AggregateID:   aggregateID,
				Type:          eventType,
				Payload:       payload,
				OccurredAt:    occurredAt,
			}
			return nil
		})
	return out, err
}

// describeValidation turns a module's validation error into something an operator can act on.
//
// Every module in this codebase carries its own `ValidationError` with its own `Fields` slice —
// which is right, because a package should not import another's error type to raise its own —
// and the consequence is that `err.Error()` says "3 validation error(s)" and nothing else. The
// HTTP layer unpacks each of them into a problem document; the seed has no HTTP layer, so it
// unpacks them here, structurally, and a module that grows a fourth field name loses nothing but
// the detail.
func describeValidation(err error) string {
	if err == nil {
		return ""
	}
	// The chain is walked rather than the outermost error inspected: every step above wraps
	// what it was doing around the refusal, and the fields are on the innermost one.
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		if detail, ok := validationFields(cause); ok {
			return err.Error() + ": " + detail
		}
	}
	return err.Error()
}

// validationFields reports the field errors of one error value, if it carries any.
func validationFields(err error) (string, bool) {
	value := reflect.ValueOf(err)
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return "", false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return "", false
	}
	fields := value.FieldByName("Fields")
	if !fields.IsValid() || fields.Kind() != reflect.Slice || fields.Len() == 0 {
		return "", false
	}
	parts := make([]string, 0, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		item := fields.Index(i)
		read := func(name string) string {
			f := item.FieldByName(name)
			if f.IsValid() && f.Kind() == reflect.String {
				return f.String()
			}
			return ""
		}
		parts = append(parts, fmt.Sprintf("%s=%s (%s)", read("Field"), read("Code"), read("Message")))
	}
	return strings.Join(parts, "; "), true
}
