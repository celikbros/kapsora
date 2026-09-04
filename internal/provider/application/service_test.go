package application_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/crypto/localkey"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/provider/application"
	"github.com/celikbros/kapsora/internal/provider/domain"
	providerpg "github.com/celikbros/kapsora/internal/provider/infrastructure/postgres"
)

// providerTables are scanned by the "no plaintext anywhere" assertion.
var providerTables = []string{
	"provider.provider_profile", "provider.location", "provider.capability",
	"provider.practitioner", "provider.practitioner_location",
}

// today is pinned so a search without an asOf is deterministic.
var today = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

type fixture struct {
	h       *dbtest.Harness
	svc     *application.Service
	logs    *bytes.Buffer
	tenantA uuid.UUID
	tenantB uuid.UUID
	actor   uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h := dbtest.New(t)
	keys, err := localkey.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	cursors, err := httpx.NewCursorCodec([]byte("fedcba9876543210fedcba9876543210"))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := application.New(application.Deps{
		Pool: h.App, Repo: providerpg.New(), Cipher: keys, Index: keys,
		Audit: auditpg.New(), Cursors: cursors, Now: func() time.Time { return today },
	})
	if err != nil {
		t.Fatal(err)
	}

	// Every log line written while the test runs is checked for a leaked registration number.
	logs := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	return &fixture{
		h: h, svc: svc, logs: logs,
		tenantA: h.CreateTenant("PROV_A"), tenantB: h.CreateTenant("PROV_B"),
		actor: h.CreateActor("provider-operator", "Provider Operator"),
	}
}

// rc is a tenant-wide back office actor: no ORGANIZATION scope, so it sees every provider.
func (f *fixture) rc(tenant uuid.UUID) identity.RequestContext {
	return identity.RequestContext{
		TenantID: tenant, MembershipID: uuid.New(),
		Principal: identity.Principal{ActorID: f.actor}, StepUpValid: true,
		Permissions: map[string]struct{}{
			"provider.read": {}, "provider.manage": {}, "provider.practitioner.manage": {},
		},
	}
}

// scopedRC is a provider-side actor whose role grant is bound to one organization.
func (f *fixture) scopedRC(tenant, organizationID uuid.UUID) identity.RequestContext {
	rc := f.rc(tenant)
	rc.Scopes = []identity.Scope{{
		Type: application.ScopeOrganization, ID: uuid.NullUUID{UUID: organizationID, Valid: true},
	}}
	return rc
}

func day(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return d
}

func dayPtr(t *testing.T, s string) *time.Time {
	d := day(t, s)
	return &d
}

// provider creates the organization relationship and its profile in one step and returns
// both ids, because the organization id is what a scoped grant names.
func (f *fixture) provider(t *testing.T, tenant uuid.UUID, name string) (application.ProviderRecord, uuid.UUID) {
	t.Helper()
	organizationID := f.h.CreateTenantOrganization(tenant, name, "PROVIDER")
	record, err := f.svc.CreateProvider(context.Background(), f.rc(tenant), domain.NewProvider{
		TenantOrganizationID: organizationID.String(), ProviderType: "HOSPITAL", NetworkTier: "A",
		ContractedFrom: dayPtr(t, "2026-01-01"),
	})
	if err != nil {
		t.Fatalf("create provider %s: %v", name, err)
	}
	return record, organizationID
}

// activeProvider is provider plus the activation the search requires.
func (f *fixture) activeProvider(t *testing.T, tenant uuid.UUID, name string) (application.ProviderRecord, uuid.UUID) {
	t.Helper()
	record, organizationID := f.provider(t, tenant, name)
	activated, err := f.svc.MoveProvider(context.Background(), f.rc(tenant), record.ID,
		application.CommandActivate, record.RowVersion, "", "")
	if err != nil {
		t.Fatalf("activate %s: %v", name, err)
	}
	return activated, organizationID
}

func (f *fixture) location(t *testing.T, tenant uuid.UUID, providerID uuid.UUID, code, city string) application.LocationRecord {
	t.Helper()
	record, err := f.svc.CreateLocation(context.Background(), f.rc(tenant), providerID, domain.NewLocation{
		Code: code, Name: code + " lokasyonu", City: city,
	})
	if err != nil {
		t.Fatalf("create location %s: %v", code, err)
	}
	return record
}

// catalogTree seeds a two-level category tree with one definition under the leaf, which is
// all the search needs to prove the tree walk.
type catalogTree struct {
	root       uuid.UUID
	leaf       uuid.UUID
	definition uuid.UUID
}

func (f *fixture) catalog(t *testing.T, tenant uuid.UUID, prefix string) catalogTree {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var tree catalogTree
	must := func(err error, what string) {
		t.Helper()
		if err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	must(f.h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, $2, 'Sağlık', 'HEALTH') RETURNING id`, tenant, prefix+"_ROOT").Scan(&tree.root), "root category")
	must(f.h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.service_category (tenant_id, parent_id, code, name, domain_code)
		VALUES ($1, $2, $3, 'Fizyoterapi', 'HEALTH') RETURNING id`, tenant, tree.root, prefix+"_LEAF").Scan(&tree.leaf), "leaf category")
	tree.definition = f.definition(t, tenant, tree.leaf, prefix+"_SESSION")
	return tree
}

func (f *fixture) definition(t *testing.T, tenant, categoryID uuid.UUID, code string) uuid.UUID {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var id uuid.UUID
	err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name, fulfillment_mode, default_unit_type)
		VALUES ($1, $2, $3, $3, 'SESSION', 'SESSION') RETURNING id`, tenant, categoryID, code).Scan(&id)
	if err != nil {
		t.Fatalf("seed definition %s: %v", code, err)
	}
	return id
}

func (f *fixture) dump(t *testing.T, table string) string {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var out string
	query := fmt.Sprintf(`SELECT coalesce(string_agg(t::text, ' '), '') FROM %s t`, table)
	if err := f.h.Admin.QueryRow(ctx, query).Scan(&out); err != nil {
		t.Fatalf("dump %s: %v", table, err)
	}
	return out
}

func fieldCodes(t *testing.T, err error) map[string]string {
	t.Helper()
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected a validation error, got %v", err)
	}
	out := make(map[string]string, len(ve.Fields))
	for _, fe := range ve.Fields {
		out[fe.Field] = fe.Code
	}
	return out
}

func TestProviderStatusCommandsWalkTheWholeMachine(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA)

	record, _ := f.provider(t, f.tenantA, "Merkez Hastane")
	if record.Status != domain.StatusPending || record.OrganizationName != "Merkez Hastane" {
		t.Fatalf("new provider = %+v", record)
	}

	// A pending provider has never been active, so it cannot be suspended.
	if _, err := f.svc.MoveProvider(ctx, rc, record.ID, application.CommandSuspend, record.RowVersion, "AUDIT", ""); !errors.Is(err, domain.ErrTransitionInvalid) {
		t.Fatalf("suspend while pending = %v", err)
	}

	active, err := f.svc.MoveProvider(ctx, rc, record.ID, application.CommandActivate, record.RowVersion, "", "")
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if active.Status != domain.StatusActive || active.RowVersion == record.RowVersion {
		t.Fatalf("activated = %+v", active)
	}
	// A stale If-Match loses even for a legal move.
	if _, err := f.svc.MoveProvider(ctx, rc, record.ID, application.CommandSuspend, record.RowVersion, "AUDIT", ""); !errors.Is(err, application.ErrVersionMismatch) {
		t.Fatalf("stale suspend = %v", err)
	}
	// Suspending needs a reason: the status change has to be explainable afterwards.
	if fieldCodes(t, errOf(f.svc.MoveProvider(ctx, rc, record.ID, application.CommandSuspend, active.RowVersion, "", "")))["reasonCode"] != "REQUIRED" {
		t.Fatal("suspend without a reason was accepted")
	}

	suspended, err := f.svc.MoveProvider(ctx, rc, record.ID, application.CommandSuspend, active.RowVersion, "CONTRACT_BREACH", "Fatura uyuşmazlığı")
	if err != nil {
		t.Fatalf("suspend: %v", err)
	}
	reactivated, err := f.svc.MoveProvider(ctx, rc, record.ID, application.CommandActivate, suspended.RowVersion, "", "")
	if err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	terminated, err := f.svc.MoveProvider(ctx, rc, record.ID, application.CommandTerminate, reactivated.RowVersion, "CONTRACT_END", "Sözleşme bitti")
	if err != nil {
		t.Fatalf("terminate: %v", err)
	}
	if terminated.Status != domain.StatusTerminated {
		t.Fatalf("terminated = %+v", terminated)
	}

	// Termination is final for every command.
	for _, cmd := range []application.StatusCommand{
		application.CommandActivate, application.CommandSuspend, application.CommandTerminate,
	} {
		_, err := f.svc.MoveProvider(ctx, rc, record.ID, cmd, terminated.RowVersion, "REOPEN", "")
		if !errors.Is(err, domain.ErrTransitionInvalid) {
			t.Fatalf("%s after termination = %v", cmd, err)
		}
	}

	// The status never moves through the patch: the field is refused by the transport, and
	// the update statement does not carry the column at all.
	tier := "B"
	patched, err := f.svc.UpdateProvider(ctx, rc, record.ID, domain.ProviderPatch{
		NetworkTier: &tier, ExpectedVersion: terminated.RowVersion,
	})
	if err != nil {
		t.Fatalf("patch a terminated provider: %v", err)
	}
	if patched.Status != domain.StatusTerminated || patched.NetworkTier == nil || *patched.NetworkTier != "B" {
		t.Fatalf("patched = %+v", patched)
	}
}

func TestProviderProfileIsUniquePerOrganizationAndNeedsTheProviderRole(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA)

	record, organizationID := f.provider(t, f.tenantA, "Tek Profil")
	if _, err := f.svc.CreateProvider(ctx, rc, domain.NewProvider{
		TenantOrganizationID: organizationID.String(), ProviderType: "CLINIC",
	}); !errors.Is(err, application.ErrProviderProfileExists) {
		t.Fatalf("second profile = %v", err)
	}

	sponsor := f.h.CreateTenantOrganization(f.tenantA, "Sponsor Kurum", "SPONSOR")
	if fieldCodes(t, errOf(f.svc.CreateProvider(ctx, rc, domain.NewProvider{
		TenantOrganizationID: sponsor.String(), ProviderType: "CLINIC",
	})))["tenantOrganizationId"] != "ROLE" {
		t.Fatal("a sponsor relationship was given a provider profile")
	}

	// Another tenant cannot see the profile at all.
	if _, err := f.svc.GetProvider(ctx, f.rc(f.tenantB), record.ID); !errors.Is(err, application.ErrProviderNotFound) {
		t.Fatalf("cross-tenant read = %v", err)
	}
}

func TestCapabilityOverlapIsRefusedAndTheLocationETagMoves(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA)
	provider, _ := f.activeProvider(t, f.tenantA, "Yetkinlik Hastanesi")
	location := f.location(t, f.tenantA, provider.ID, "MERKEZ", "İstanbul")
	tree := f.catalog(t, f.tenantA, "CAP")

	stored, err := f.svc.ReplaceCapabilities(ctx, rc, location.ID, []domain.CapabilityInput{
		{ServiceCategoryID: tree.leaf.String(), ValidFrom: day(t, "2026-01-01")},
		{ServiceDefinitionID: tree.definition.String(), ValidFrom: day(t, "2026-01-01")},
	}, location.RowVersion)
	if err != nil {
		t.Fatalf("replace capabilities: %v", err)
	}
	if len(stored.Items) != 2 {
		t.Fatalf("stored %d capabilities, want 2", len(stored.Items))
	}
	if stored.RowVersion == location.RowVersion {
		t.Fatal("replacing the capability set must move the location ETag")
	}

	// A self-contradicting payload is refused before anything is written.
	_, err = f.svc.ReplaceCapabilities(ctx, rc, location.ID, []domain.CapabilityInput{
		{ServiceDefinitionID: tree.definition.String(), ValidFrom: day(t, "2026-01-01")},
		{ServiceDefinitionID: tree.definition.String(), ValidFrom: day(t, "2026-06-01")},
	}, stored.RowVersion)
	if !errors.Is(err, domain.ErrCapabilityOverlap) {
		t.Fatalf("overlapping payload = %v", err)
	}
	after, err := f.svc.ListCapabilities(ctx, rc, location.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(after.Items) != 2 {
		t.Fatalf("the refused replacement changed the set: %d rows", len(after.Items))
	}

	// Successive periods for the same target are fine, and so is an empty set.
	replaced, err := f.svc.ReplaceCapabilities(ctx, rc, location.ID, []domain.CapabilityInput{
		{ServiceDefinitionID: tree.definition.String(), ValidFrom: day(t, "2025-01-01"), ValidTo: dayPtr(t, "2026-01-01")},
		{ServiceDefinitionID: tree.definition.String(), ValidFrom: day(t, "2026-01-01")},
	}, after.RowVersion)
	if err != nil {
		t.Fatalf("successive periods: %v", err)
	}
	if _, err := f.svc.ReplaceCapabilities(ctx, rc, location.ID, nil, replaced.RowVersion); err != nil {
		t.Fatalf("empty set: %v", err)
	}

	// A stale If-Match is refused, and a catalog row of another tenant is a field error.
	if _, err := f.svc.ReplaceCapabilities(ctx, rc, location.ID, nil, location.RowVersion); !errors.Is(err, application.ErrVersionMismatch) {
		t.Fatalf("stale replacement = %v", err)
	}
	current, err := f.svc.ListCapabilities(ctx, rc, location.ID)
	if err != nil {
		t.Fatal(err)
	}
	foreign := f.catalog(t, f.tenantB, "OTHER")
	if fieldCodes(t, errOf(f.svc.ReplaceCapabilities(ctx, rc, location.ID, []domain.CapabilityInput{
		{ServiceDefinitionID: foreign.definition.String(), ValidFrom: day(t, "2026-01-01")},
	}, current.RowVersion)))["items"] != "NOT_FOUND" {
		t.Fatal("a catalog row of another tenant was accepted")
	}
}

// TestSearchResolvesTheCategoryTree is the guarantee eligibility depends on: a category
// capability covers a definition added under it afterwards, and only ACTIVE providers and
// locations answer.
func TestSearchResolvesTheCategoryTree(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA)
	tree := f.catalog(t, f.tenantA, "SEARCH")

	byCategory, _ := f.activeProvider(t, f.tenantA, "Kategori Hastanesi")
	categoryLocation := f.location(t, f.tenantA, byCategory.ID, "KAT", "İstanbul")
	if _, err := f.svc.ReplaceCapabilities(ctx, rc, categoryLocation.ID, []domain.CapabilityInput{
		// The capability names the root, two levels above the definition's own category.
		{ServiceCategoryID: tree.root.String(), ValidFrom: day(t, "2026-01-01")},
	}, categoryLocation.RowVersion); err != nil {
		t.Fatalf("category capability: %v", err)
	}

	byDefinition, _ := f.activeProvider(t, f.tenantA, "Tanım Hastanesi")
	definitionLocation := f.location(t, f.tenantA, byDefinition.ID, "TAN", "İstanbul")
	if _, err := f.svc.ReplaceCapabilities(ctx, rc, definitionLocation.ID, []domain.CapabilityInput{
		{ServiceDefinitionID: tree.definition.String(), ValidFrom: day(t, "2026-01-01")},
	}, definitionLocation.RowVersion); err != nil {
		t.Fatalf("definition capability: %v", err)
	}

	expiredProvider, _ := f.activeProvider(t, f.tenantA, "Süresi Dolmuş Hastane")
	expiredLocation := f.location(t, f.tenantA, expiredProvider.ID, "ESK", "İstanbul")
	if _, err := f.svc.ReplaceCapabilities(ctx, rc, expiredLocation.ID, []domain.CapabilityInput{
		{ServiceDefinitionID: tree.definition.String(), ValidFrom: day(t, "2025-01-01"), ValidTo: dayPtr(t, "2026-01-01")},
	}, expiredLocation.RowVersion); err != nil {
		t.Fatalf("expired capability: %v", err)
	}

	suspendedProvider, _ := f.activeProvider(t, f.tenantA, "Askıdaki Hastane")
	suspendedLocation := f.location(t, f.tenantA, suspendedProvider.ID, "ASK", "İstanbul")
	if _, err := f.svc.ReplaceCapabilities(ctx, rc, suspendedLocation.ID, []domain.CapabilityInput{
		{ServiceDefinitionID: tree.definition.String(), ValidFrom: day(t, "2026-01-01")},
	}, suspendedLocation.RowVersion); err != nil {
		t.Fatalf("suspended capability: %v", err)
	}
	current, err := f.svc.GetProvider(ctx, rc, suspendedProvider.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.MoveProvider(ctx, rc, suspendedProvider.ID, application.CommandSuspend, current.RowVersion, "AUDIT", ""); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	found := func(t *testing.T, filter application.SearchFilter) map[string]string {
		t.Helper()
		page, err := f.svc.Search(ctx, rc, filter)
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		out := make(map[string]string, len(page.Items))
		for _, hit := range page.Items {
			out[hit.LocationCode] = hit.MatchedVia
		}
		return out
	}

	hits := found(t, application.SearchFilter{ServiceDefinitionID: tree.definition})
	if len(hits) != 2 {
		t.Fatalf("search found %v, want the category and the definition location", hits)
	}
	if hits["KAT"] != "CATEGORY" || hits["TAN"] != "DEFINITION" {
		t.Fatalf("matchedVia = %v", hits)
	}

	// A definition created under the covered category today is covered by the category
	// capability written before it existed, with no provider row touched.
	later := f.definition(t, f.tenantA, tree.leaf, "SEARCH_NEW")
	hits = found(t, application.SearchFilter{ServiceDefinitionID: later})
	if len(hits) != 1 || hits["KAT"] != "CATEGORY" {
		t.Fatalf("a later definition = %v, want only the category location", hits)
	}

	// As of a day the expired capability was still valid, that location answers too.
	hits = found(t, application.SearchFilter{ServiceDefinitionID: tree.definition, AsOf: day(t, "2025-06-01")})
	if len(hits) != 1 || hits["ESK"] != "DEFINITION" {
		t.Fatalf("as of 2025-06-01 = %v", hits)
	}

	// The city filter narrows the same list, and an unknown city empties it.
	hits = found(t, application.SearchFilter{ServiceDefinitionID: tree.definition, City: "Ankara"})
	if len(hits) != 0 {
		t.Fatalf("another city = %v", hits)
	}

	// A definition that does not exist is a field error, never an empty page a caller
	// would read as "nobody can do this".
	if fieldCodes(t, errOf(f.svc.Search(ctx, rc, application.SearchFilter{
		ServiceDefinitionID: uuid.New(),
	})))["serviceDefinitionId"] != "NOT_FOUND" {
		t.Fatal("an unknown service definition was accepted")
	}

	// Paging is stable: no location appears on two pages, and a tampered cursor is refused.
	first, err := f.svc.Search(ctx, rc, application.SearchFilter{ServiceDefinitionID: tree.definition, Limit: 1})
	if err != nil || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatalf("first page = %d items, cursor %q, err %v", len(first.Items), first.NextCursor, err)
	}
	second, err := f.svc.Search(ctx, rc, application.SearchFilter{
		ServiceDefinitionID: tree.definition, Limit: 1, Cursor: first.NextCursor,
	})
	if err != nil || len(second.Items) != 1 {
		t.Fatalf("second page = %d items, err %v", len(second.Items), err)
	}
	if first.Items[0].LocationID == second.Items[0].LocationID {
		t.Fatal("a location appeared on two pages")
	}
	if _, err := f.svc.Search(ctx, rc, application.SearchFilter{
		ServiceDefinitionID: tree.definition, Cursor: "not-a-cursor",
	}); !errors.Is(err, httpx.ErrInvalidCursor) {
		t.Fatalf("tampered cursor = %v", err)
	}
	if !first.AsOf.Equal(domain.DateOnly(today)) {
		t.Fatalf("asOf = %s, want today", first.AsOf)
	}
}

func TestPractitionerRegistrationRoundTripAndPlaintextNeverEscapes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA)
	provider, _ := f.activeProvider(t, f.tenantA, "Uygulayıcı Hastanesi")
	number := "TTB-889911"

	practitioner, err := f.svc.CreatePractitioner(ctx, rc, provider.ID, domain.NewPractitioner{
		FullName: "Dr. Ayşe Yılmaz", Title: "Uzm. Dr.", BranchCode: "FTR",
		RegistrationAuthority: "TTB", RegistrationNumber: number, ValidFrom: dayPtr(t, "2026-01-01"),
	})
	if err != nil {
		t.Fatalf("create practitioner: %v", err)
	}
	normalized := domain.NormalizeRegistrationNumber(number)
	if practitioner.MaskedRegistration != domain.MaskRegistrationNumber(normalized) {
		t.Fatalf("masked = %q", practitioner.MaskedRegistration)
	}

	// The same number cannot be registered twice within its issuing body.
	if _, err := f.svc.CreatePractitioner(ctx, rc, provider.ID, domain.NewPractitioner{
		FullName: "Dr. Kopya", RegistrationAuthority: "TTB", RegistrationNumber: " ttb-889911 ",
	}); !errors.Is(err, application.ErrRegistrationTaken) {
		t.Fatalf("duplicate registration = %v", err)
	}
	// The same digits under another authority are a different registration.
	if _, err := f.svc.CreatePractitioner(ctx, rc, provider.ID, domain.NewPractitioner{
		FullName: "Dr. Diğer Otorite", RegistrationAuthority: "SB", RegistrationNumber: number,
	}); err != nil {
		t.Fatalf("same number under another authority: %v", err)
	}

	// The search finds it through the blind index, even from an unnormalized value.
	found, err := f.svc.SearchByRegistration(ctx, rc, application.RegistrationSearch{
		Authority: "TTB", Number: " ttb -889911 ",
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if found.ID != practitioner.ID {
		t.Fatalf("found %s, want %s", found.ID, practitioner.ID)
	}

	// The index is tenant salted, so the same number in another tenant is a miss.
	otherProvider, _ := f.activeProvider(t, f.tenantB, "Öteki Hastane")
	if _, err := f.svc.CreatePractitioner(ctx, f.rc(f.tenantB), otherProvider.ID, domain.NewPractitioner{
		FullName: "Dr. Öteki", RegistrationAuthority: "TTB", RegistrationNumber: number,
	}); err != nil {
		t.Fatalf("the same number must be free in another tenant: %v", err)
	}
	if _, err := f.svc.SearchByRegistration(ctx, f.rc(f.tenantB), application.RegistrationSearch{
		Authority: "TTB", Number: "TTB-000000",
	}); !errors.Is(err, application.ErrPractitionerNotFound) {
		t.Fatalf("unknown number = %v", err)
	}

	// The access event names the issuing body and never the number.
	dbCtx, cancel := f.h.Ctx()
	defer cancel()
	var accessType, classification, reason string
	err = f.h.Admin.QueryRow(dbCtx, `SELECT access_type, data_classification, coalesce(reason_text,'')
	                                   FROM audit.access_event WHERE tenant_id = $1 ORDER BY occurred_at DESC LIMIT 1`,
		f.tenantA).Scan(&accessType, &classification, &reason)
	if err != nil {
		t.Fatal(err)
	}
	if accessType != "SEARCH" || classification != "PERSONAL" || reason != "registration_authority=TTB" {
		t.Fatalf("access event = %s/%s/%s", accessType, classification, reason)
	}

	body, err := json.Marshal(found)
	if err != nil {
		t.Fatal(err)
	}
	haystacks := map[string]string{"response": string(body), "logs": f.logs.String()}
	for _, table := range providerTables {
		haystacks[table] = f.dump(t, table)
	}
	haystacks["audit.event"] = f.dump(t, "audit.event")
	haystacks["audit.access_event"] = f.dump(t, "audit.access_event")
	for where, hay := range haystacks {
		if strings.Contains(hay, normalized) || strings.Contains(hay, number) {
			t.Errorf("plaintext registration number found in %s", where)
		}
	}
	// The trail still names the action and the issuing body: only the value is secret.
	if !strings.Contains(haystacks["audit.event"], "provider.practitioner.create") {
		t.Fatal("the create was not audited")
	}
	if !strings.Contains(haystacks["provider.practitioner"], "TTB") {
		t.Fatal("the issuing authority should be visible, only the number is secret")
	}
}

func TestPractitionerAssignmentsAreReplacedAsASet(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA)
	provider, _ := f.activeProvider(t, f.tenantA, "Görevlendirme Hastanesi")
	first := f.location(t, f.tenantA, provider.ID, "MERKEZ", "İstanbul")
	second := f.location(t, f.tenantA, provider.ID, "SUBE", "İstanbul")

	practitioner, err := f.svc.CreatePractitioner(ctx, rc, provider.ID, domain.NewPractitioner{
		FullName: "Dr. Görevli", RegistrationAuthority: "TTB", RegistrationNumber: "TTB-114477",
	})
	if err != nil {
		t.Fatalf("create practitioner: %v", err)
	}

	result, err := f.svc.ReplaceAssignments(ctx, rc, practitioner.ID, []domain.AssignmentInput{
		{LocationID: first.ID.String(), Role: "ATTENDING", ValidFrom: day(t, "2026-01-01")},
		{LocationID: second.ID.String(), Role: "CONSULTANT", ValidFrom: day(t, "2026-01-01")},
	}, practitioner.RowVersion)
	if err != nil {
		t.Fatalf("replace assignments: %v", err)
	}
	if len(result.Items) != 2 || result.RowVersion == practitioner.RowVersion {
		t.Fatalf("assignments = %d rows, version %d -> %d", len(result.Items), practitioner.RowVersion, result.RowVersion)
	}
	if result.Items[0].LocationCode != "MERKEZ" {
		t.Fatalf("assignments are ordered by location code, got %q first", result.Items[0].LocationCode)
	}

	// Two overlapping spells in the same role at the same location are refused.
	_, err = f.svc.ReplaceAssignments(ctx, rc, practitioner.ID, []domain.AssignmentInput{
		{LocationID: first.ID.String(), Role: "ATTENDING", ValidFrom: day(t, "2026-01-01"), ValidTo: dayPtr(t, "2026-07-01")},
		{LocationID: first.ID.String(), Role: "ATTENDING", ValidFrom: day(t, "2026-06-01")},
	}, result.RowVersion)
	if !errors.Is(err, domain.ErrAssignmentOverlap) {
		t.Fatalf("overlapping assignments = %v", err)
	}

	// A location of another provider is a field error on the array, not a 404 naming it.
	otherProvider, _ := f.activeProvider(t, f.tenantA, "Başka Hastane")
	otherLocation := f.location(t, f.tenantA, otherProvider.ID, "BASKA", "Ankara")
	if fieldCodes(t, errOf(f.svc.ReplaceAssignments(ctx, rc, practitioner.ID, []domain.AssignmentInput{
		{LocationID: otherLocation.ID.String(), Role: "ATTENDING", ValidFrom: day(t, "2026-01-01")},
	}, result.RowVersion)))["items"] != "NOT_FOUND" {
		t.Fatal("a location of another provider was accepted")
	}

	// Reading the practitioner carries the assignments the replacement left behind.
	loaded, err := f.svc.GetPractitioner(ctx, rc, practitioner.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(loaded.Locations) != 2 {
		t.Fatalf("loaded %d assignments, want 2", len(loaded.Locations))
	}
}

// TestProviderScopedActorSeesOnlyItsOwnProvider is the boundary of section 2.2: the filter
// lives in the repository, and a row outside the scope answers "not found" rather than
// "forbidden", so the existence of another provider does not leak.
func TestProviderScopedActorSeesOnlyItsOwnProvider(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	mine, myOrganization := f.activeProvider(t, f.tenantA, "Benim Hastanem")
	theirs, _ := f.activeProvider(t, f.tenantA, "Başkasının Hastanesi")
	myLocation := f.location(t, f.tenantA, mine.ID, "BENIM", "İzmir")
	theirLocation := f.location(t, f.tenantA, theirs.ID, "ONLARIN", "İzmir")

	practitioner, err := f.svc.CreatePractitioner(ctx, f.rc(f.tenantA), theirs.ID, domain.NewPractitioner{
		FullName: "Dr. Başkası", RegistrationAuthority: "TTB", RegistrationNumber: "TTB-777333",
	})
	if err != nil {
		t.Fatal(err)
	}

	scoped := f.scopedRC(f.tenantA, myOrganization)
	page, err := f.svc.ListProviders(ctx, scoped, application.ListFilter{})
	if err != nil {
		t.Fatalf("scoped list: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != mine.ID {
		t.Fatalf("a scoped actor sees %d providers", len(page.Items))
	}
	// The tenant-wide actor still sees both, so the filter is the grant and not the data.
	all, err := f.svc.ListProviders(ctx, f.rc(f.tenantA), application.ListFilter{})
	if err != nil || len(all.Items) != 2 {
		t.Fatalf("tenant-wide list = %d items, err %v", len(all.Items), err)
	}

	if _, err := f.svc.GetProvider(ctx, scoped, mine.ID); err != nil {
		t.Fatalf("a scoped actor must read its own provider: %v", err)
	}
	if _, err := f.svc.GetProvider(ctx, scoped, theirs.ID); !errors.Is(err, application.ErrProviderNotFound) {
		t.Fatalf("another provider = %v, want not found", err)
	}
	if _, err := f.svc.GetLocation(ctx, scoped, myLocation.ID); err != nil {
		t.Fatalf("own location: %v", err)
	}
	if _, err := f.svc.GetLocation(ctx, scoped, theirLocation.ID); !errors.Is(err, application.ErrLocationNotFound) {
		t.Fatalf("another provider's location = %v, want not found", err)
	}
	if _, err := f.svc.GetPractitioner(ctx, scoped, practitioner.ID); !errors.Is(err, application.ErrPractitionerNotFound) {
		t.Fatalf("another provider's practitioner = %v, want not found", err)
	}
	if _, err := f.svc.SearchByRegistration(ctx, scoped, application.RegistrationSearch{
		Authority: "TTB", Number: "TTB-777333",
	}); !errors.Is(err, application.ErrPractitionerNotFound) {
		t.Fatalf("a scoped registration search must not reach another provider: %v", err)
	}
	// A write is bound by the same predicate, so no route can widen it.
	if _, err := f.svc.CreateLocation(ctx, scoped, theirs.ID, domain.NewLocation{
		Code: "KACAK", Name: "Kaçak lokasyon",
	}); !errors.Is(err, application.ErrProviderNotFound) {
		t.Fatalf("a scoped create on another provider = %v", err)
	}
}

func TestLocationPagingIsStableWhileRowsAreInserted(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA)
	provider, _ := f.activeProvider(t, f.tenantA, "Sayfalama Hastanesi")
	for i := range 5 {
		f.location(t, f.tenantA, provider.ID, "L"+strconv.Itoa(i), "Bursa")
	}

	first, err := f.svc.ListLocations(ctx, rc, provider.ID, application.ListFilter{Limit: 2})
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %d items, cursor %q, err %v", len(first.Items), first.NextCursor, err)
	}
	// New rows sort ahead of the cursor, so they cannot shift the page being walked.
	f.location(t, f.tenantA, provider.ID, "L_NEW", "Bursa")

	second, err := f.svc.ListLocations(ctx, rc, provider.ID, application.ListFilter{Limit: 2, Cursor: first.NextCursor})
	if err != nil || len(second.Items) != 2 {
		t.Fatalf("second page = %d items, err %v", len(second.Items), err)
	}
	seen := map[uuid.UUID]bool{}
	for _, l := range append(append([]application.LocationRecord{}, first.Items...), second.Items...) {
		if seen[l.ID] {
			t.Fatalf("location %s appeared on two pages", l.Code)
		}
		seen[l.ID] = true
	}
	if _, err := f.svc.ListLocations(ctx, rc, provider.ID, application.ListFilter{Cursor: "not-a-cursor"}); !errors.Is(err, httpx.ErrInvalidCursor) {
		t.Fatalf("tampered cursor = %v", err)
	}
}

func TestLocationCodeIsUniquePerProviderAndCoordinatesTravelWhole(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA)
	provider, _ := f.activeProvider(t, f.tenantA, "Kod Hastanesi")

	lat, lng := 41.015137, 28.979530
	location, err := f.svc.CreateLocation(ctx, rc, provider.ID, domain.NewLocation{
		Code: "MERKEZ", Name: "Merkez", City: "İstanbul", Latitude: &lat, Longitude: &lng,
	})
	if err != nil {
		t.Fatalf("create location: %v", err)
	}
	if location.Latitude == nil || location.Longitude == nil {
		t.Fatalf("coordinates = %+v", location)
	}
	// numeric(9,6) keeps six decimal places, which is roughly a tenth of a metre.
	if diff := *location.Latitude - lat; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("latitude round trip = %v, want %v", *location.Latitude, lat)
	}
	if location.CountryCode != domain.DefaultCountry || location.Timezone != domain.DefaultTimezone {
		t.Fatalf("defaults = %q/%q", location.CountryCode, location.Timezone)
	}

	if _, err := f.svc.CreateLocation(ctx, rc, provider.ID, domain.NewLocation{
		Code: "MERKEZ", Name: "Kopya",
	}); !errors.Is(err, application.ErrLocationCodeTaken) {
		t.Fatalf("duplicate code = %v", err)
	}
	// The same code is free at another provider.
	other, _ := f.activeProvider(t, f.tenantA, "Diğer Kod Hastanesi")
	if _, err := f.svc.CreateLocation(ctx, rc, other.ID, domain.NewLocation{Code: "MERKEZ", Name: "Merkez"}); err != nil {
		t.Fatalf("the code must be free at another provider: %v", err)
	}

	// Clearing one half of the pin is refused; clearing both is not.
	if _, err := f.svc.UpdateLocation(ctx, rc, location.ID, domain.LocationPatch{
		ClearLatitude: true, ExpectedVersion: location.RowVersion,
	}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("half a coordinate pair = %v", err)
	}
	cleared, err := f.svc.UpdateLocation(ctx, rc, location.ID, domain.LocationPatch{
		ClearLatitude: true, ClearLongitude: true, ExpectedVersion: location.RowVersion,
	})
	if err != nil {
		t.Fatalf("clear both coordinates: %v", err)
	}
	if cleared.Latitude != nil || cleared.Longitude != nil {
		t.Fatalf("cleared = %+v", cleared)
	}
}

// errOf keeps only the error of a two-value call, so a failure can be handed straight to
// fieldCodes; a call that unexpectedly succeeded fails there as "no validation error".
func errOf[T any](_ T, err error) error { return err }
