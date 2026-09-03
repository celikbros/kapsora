package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/organization/application"
	"github.com/celikbros/kapsora/internal/organization/domain"
	"github.com/celikbros/kapsora/internal/organization/domain/domaintest"
	organizationpg "github.com/celikbros/kapsora/internal/organization/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/platform/crypto/localkey"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

type fixture struct {
	h       *dbtest.Harness
	svc     *application.Service
	rand    *rand.Rand
	tenantA uuid.UUID
	tenantB uuid.UUID
	actor   uuid.UUID
	used    map[string]bool
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
		Pool: h.App, Repo: organizationpg.New(), Cipher: keys, Index: keys, Audit: auditpg.New(), Cursors: cursors,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{
		h: h, svc: svc, rand: rand.New(rand.NewPCG(7, 8)),
		tenantA: h.CreateTenant("ORG_A"), tenantB: h.CreateTenant("ORG_B"),
		actor: h.CreateActor("org-actor", "Org Actor"), used: map[string]bool{},
	}
}

func (f *fixture) rc(tenant uuid.UUID) identity.RequestContext {
	return identity.RequestContext{
		TenantID: tenant, MembershipID: uuid.New(), Principal: identity.Principal{ActorID: f.actor},
		Permissions: map[string]struct{}{"organization.read": {}, "organization.manage": {}},
	}
}

// vkn returns a fresh valid tax number not used before in this fixture.
func (f *fixture) vkn() string {
	for {
		v := domaintest.GenerateVKN(f.rand)
		if !f.used[v] {
			f.used[v] = true
			return v
		}
	}
}

func (f *fixture) create(t *testing.T, tenant uuid.UUID, name, role, vkn string, extra ...domain.Identifier) application.Organization {
	t.Helper()
	in := domain.NewOrganization{
		LegalName: name + " A.Ş.", DisplayName: name, OrganizationKind: "PROVIDER", RelationshipRole: role,
		Identifiers: append([]domain.Identifier{{Type: domain.IdentifierVKN, Value: vkn, Primary: true}}, extra...),
	}
	org, err := f.svc.Create(context.Background(), f.rc(tenant), in)
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return org
}

func TestCreateDedupesTheGlobalOrganizationAcrossTenants(t *testing.T) {
	f := newFixture(t)
	vkn := f.vkn()

	a := f.create(t, f.tenantA, "Ortak Hastane", "PROVIDER", vkn)
	b := f.create(t, f.tenantB, "Ortak Hastane", "PROVIDER", vkn)

	if a.OrganizationID != b.OrganizationID {
		t.Fatalf("two global rows for one VKN: %s vs %s", a.OrganizationID, b.OrganizationID)
	}
	if a.ID == b.ID {
		t.Fatal("relationships must differ per tenant")
	}
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var globals int
	if err := f.h.Admin.QueryRow(ctx, `SELECT count(*) FROM directory.organization WHERE tax_number_hash IS NOT NULL`).Scan(&globals); err != nil {
		t.Fatal(err)
	}
	if globals != 1 {
		t.Fatalf("global organizations = %d, want 1", globals)
	}

	// Identifiers come back masked and never in full.
	if len(a.Identifiers) != 1 || a.Identifiers[0].Type != domain.IdentifierVKN || !a.Identifiers[0].Primary {
		t.Fatalf("identifiers = %+v", a.Identifiers)
	}
	if masked := a.Identifiers[0].MaskedValue; masked == vkn || !strings.Contains(masked, "******") || !strings.HasPrefix(masked, vkn[:2]) {
		t.Fatalf("masked value = %q", masked)
	}

	// Same tenant, same role again: the exclusion constraint answers.
	_, err := f.svc.Create(context.Background(), f.rc(f.tenantA), domain.NewOrganization{
		LegalName: "Ortak Hastane A.Ş.", DisplayName: "Ortak Hastane", OrganizationKind: "PROVIDER", RelationshipRole: "PROVIDER",
		Identifiers: []domain.Identifier{{Type: domain.IdentifierVKN, Value: vkn}},
	})
	if !errors.Is(err, application.ErrRelationshipExists) {
		t.Fatalf("duplicate relationship: %v", err)
	}
	// A second role with the same organization is a different relationship.
	if _, err := f.svc.Create(context.Background(), f.rc(f.tenantA), domain.NewOrganization{
		LegalName: "Ortak Hastane A.Ş.", DisplayName: "Ortak Hastane", OrganizationKind: "PROVIDER", RelationshipRole: "VENDOR",
		Identifiers: []domain.Identifier{{Type: domain.IdentifierVKN, Value: vkn}},
	}); err != nil {
		t.Fatalf("second role: %v", err)
	}
}

func TestTaxNumberNeverLeaksIntoStorageAuditOrResponses(t *testing.T) {
	f := newFixture(t)
	vkn := f.vkn()
	org := f.create(t, f.tenantA, "Gizli Klinik", "PROVIDER", vkn)

	body, err := json.Marshal(org)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), vkn) {
		t.Fatal("response model contains the full tax number")
	}

	ctx, cancel := f.h.Ctx()
	defer cancel()
	var cipherText string
	if err := f.h.Admin.QueryRow(ctx, `SELECT encode(tax_number_cipher, 'escape') FROM directory.organization WHERE id = $1`, org.OrganizationID).Scan(&cipherText); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cipherText, vkn) {
		t.Fatal("tax number stored in clear")
	}
	var auditDetail string
	if err := f.h.Admin.QueryRow(ctx, `SELECT string_agg(detail_json::text, ' ') FROM audit.event WHERE tenant_id = $1`, f.tenantA).Scan(&auditDetail); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(auditDetail, vkn) {
		t.Fatal("tax number in audit detail")
	}
	if !strings.Contains(auditDetail, org.OrganizationID.String()) {
		t.Fatalf("audit detail should reference the organization id: %s", auditDetail)
	}
}

func TestOtherTenantCannotSeeOrPatchARelationship(t *testing.T) {
	f := newFixture(t)
	a := f.create(t, f.tenantA, "Yalnız A", "SPONSOR", f.vkn())

	if _, err := f.svc.Get(context.Background(), f.rc(f.tenantB), a.ID); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("other tenant get: %v", err)
	}
	name := "Başka Ad"
	if _, err := f.svc.Update(context.Background(), f.rc(f.tenantB), a.ID, application.UpdateInput{DisplayName: &name, ExpectedVersion: a.RowVersion}); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("other tenant update: %v", err)
	}
	if _, err := f.svc.Get(context.Background(), f.rc(f.tenantA), uuid.New()); !errors.Is(err, application.ErrNotFound) {
		t.Fatal("unknown id must be not found")
	}
	page, err := f.svc.List(context.Background(), f.rc(f.tenantB), application.ListFilter{})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("other tenant list = %+v err=%v", page, err)
	}
}

func TestUpdateHonoursIfMatchAndSharedNameProtection(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	vkn := f.vkn()
	a := f.create(t, f.tenantA, "Tek Sahipli", "PROVIDER", vkn)

	// Stale version.
	code := "PRV-1"
	if _, err := f.svc.Update(ctx, f.rc(f.tenantA), a.ID, application.UpdateInput{TenantCode: &code, ExpectedVersion: a.RowVersion + 5}); !errors.Is(err, application.ErrVersionMismatch) {
		t.Fatalf("stale version: %v", err)
	}
	// Current version: tenant code and status change, version bumps.
	status := "SUSPENDED"
	updated, err := f.svc.Update(ctx, f.rc(f.tenantA), a.ID, application.UpdateInput{TenantCode: &code, RelationshipStatus: &status, ExpectedVersion: a.RowVersion})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.RowVersion != a.RowVersion+1 || updated.TenantCode == nil || *updated.TenantCode != code || updated.RelationshipStatus != "SUSPENDED" {
		t.Fatalf("updated = %+v", updated)
	}
	// Clearing the code with an explicit null.
	cleared, err := f.svc.Update(ctx, f.rc(f.tenantA), a.ID, application.UpdateInput{ClearTenantCode: true, ExpectedVersion: updated.RowVersion})
	if err != nil || cleared.TenantCode != nil {
		t.Fatalf("clear tenant code: %+v err=%v", cleared, err)
	}
	// Renaming while the organization is ours alone works.
	name := "Tek Sahipli Yeni"
	renamed, err := f.svc.Update(ctx, f.rc(f.tenantA), a.ID, application.UpdateInput{DisplayName: &name, ExpectedVersion: cleared.RowVersion})
	if err != nil || renamed.DisplayName != name {
		t.Fatalf("rename: %+v err=%v", renamed, err)
	}
	// Once tenant B also relates to it, the name is shared and read-only.
	f.create(t, f.tenantB, "Tek Sahipli Yeni", "PROVIDER", vkn)
	other := "Yeniden"
	if _, err := f.svc.Update(ctx, f.rc(f.tenantA), a.ID, application.UpdateInput{DisplayName: &other, ExpectedVersion: renamed.RowVersion}); !errors.Is(err, application.ErrSharedReadOnly) {
		t.Fatalf("shared rename: %v", err)
	}
	// Bad status value is a validation error, not a 500.
	bad := "PENDING"
	if _, err := f.svc.Update(ctx, f.rc(f.tenantA), a.ID, application.UpdateInput{RelationshipStatus: &bad, ExpectedVersion: renamed.RowVersion}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("bad status: %v", err)
	}
}

func TestListPaginatesWithoutGapsOrDuplicatesAndFilters(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const total = 120
	created := map[uuid.UUID]bool{}
	for i := 0; i < total; i++ {
		role := "PROVIDER"
		if i%10 == 0 {
			role = "VENDOR"
		}
		org := f.create(t, f.tenantA, "Kurum "+strings.Repeat("x", i%3)+" "+uuid.NewString()[:8], role, f.vkn())
		created[org.ID] = true
	}

	seen := map[uuid.UUID]bool{}
	cursor := ""
	pages := 0
	var lastCreated string
	for {
		page, err := f.svc.List(ctx, f.rc(f.tenantA), application.ListFilter{Cursor: cursor, Limit: 50})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		pages++
		for _, it := range page.Items {
			if seen[it.ID] {
				t.Fatalf("duplicate %s on page %d", it.ID, pages)
			}
			seen[it.ID] = true
			if lastCreated != "" && it.CreatedAt.Format("2006-01-02T15:04:05.000000000") > lastCreated {
				t.Fatalf("order broken on page %d", pages)
			}
			lastCreated = it.CreatedAt.Format("2006-01-02T15:04:05.000000000")
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if pages != 3 || len(seen) != total {
		t.Fatalf("pages=%d seen=%d, want 3/%d", pages, len(seen), total)
	}
	for id := range created {
		if !seen[id] {
			t.Fatalf("relationship %s never listed", id)
		}
	}

	vendors, err := f.svc.List(ctx, f.rc(f.tenantA), application.ListFilter{Role: "VENDOR", Limit: 200})
	if err != nil || len(vendors.Items) != 12 {
		t.Fatalf("vendor filter = %d err=%v", len(vendors.Items), err)
	}
	search, err := f.svc.List(ctx, f.rc(f.tenantA), application.ListFilter{Query: "kurum xx", Limit: 200})
	if err != nil || len(search.Items) != 40 {
		t.Fatalf("search = %d err=%v", len(search.Items), err)
	}
	if _, err := f.svc.List(ctx, f.rc(f.tenantA), application.ListFilter{Cursor: "tampered"}); !errors.Is(err, httpx.ErrInvalidCursor) {
		t.Fatalf("bad cursor: %v", err)
	}
	if _, err := f.svc.List(ctx, f.rc(f.tenantA), application.ListFilter{Query: "x"}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("short query: %v", err)
	}
	if _, err := f.svc.List(ctx, f.rc(f.tenantA), application.ListFilter{Role: "BOSS"}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("bad role: %v", err)
	}
}

func TestCreateValidationAndIdentifierOwnership(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	var ve *domain.ValidationError
	_, err := f.svc.Create(ctx, f.rc(f.tenantA), domain.NewOrganization{
		LegalName: "X", DisplayName: "Hatalı", OrganizationKind: "PROVIDER", RelationshipRole: "PROVIDER",
		Identifiers: []domain.Identifier{{Type: domain.IdentifierVKN, Value: "1234567891"}},
	})
	if !errors.As(err, &ve) {
		t.Fatalf("expected a validation error, got %v", err)
	}
	fields := map[string]string{}
	for _, fe := range ve.Fields {
		fields[fe.Field] = fe.Code
	}
	if fields["legalName"] != "LENGTH" || fields["identifiers[0].value"] != "IDENTIFIER_INVALID" {
		t.Fatalf("fields = %v", fields)
	}

	_, err = f.svc.Create(ctx, f.rc(f.tenantA), domain.NewOrganization{
		LegalName: "Vergisiz A.Ş.", DisplayName: "Vergisiz", OrganizationKind: "VENDOR", RelationshipRole: "VENDOR",
		Identifiers: []domain.Identifier{{Type: domain.IdentifierMERSIS, Value: "0123456789012345"}},
	})
	if !errors.As(err, &ve) || ve.Fields[0].Code != "IDENTIFIER_REQUIRED" {
		t.Fatalf("TR without tax number: %v", err)
	}

	// A public identifier can belong to one organization only.
	mersis := domain.Identifier{Type: domain.IdentifierMERSIS, Value: "1111222233334444"}
	f.create(t, f.tenantA, "Mersisli", "PROVIDER", f.vkn(), mersis)
	_, err = f.svc.Create(ctx, f.rc(f.tenantA), domain.NewOrganization{
		LegalName: "Başka A.Ş.", DisplayName: "Başka", OrganizationKind: "PROVIDER", RelationshipRole: "PROVIDER",
		Identifiers: []domain.Identifier{{Type: domain.IdentifierVKN, Value: f.vkn()}, mersis},
	})
	if !errors.Is(err, application.ErrIdentifierTaken) {
		t.Fatalf("shared MERSIS: %v", err)
	}
}
