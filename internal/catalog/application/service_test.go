package application_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/catalog/application"
	"github.com/celikbros/kapsora/internal/catalog/domain"
	catalogpg "github.com/celikbros/kapsora/internal/catalog/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

type fixture struct {
	h       *dbtest.Harness
	svc     *application.Service
	tenantA uuid.UUID
	tenantB uuid.UUID
	actor   uuid.UUID
}

// today is pinned so an as-of read without a date is deterministic.
var today = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h := dbtest.New(t)
	cursors, err := httpx.NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := application.New(application.Deps{
		Pool: h.App, Repo: catalogpg.New(), Audit: auditpg.New(), Cursors: cursors,
		Now: func() time.Time { return today },
	})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{
		h: h, svc: svc,
		tenantA: h.CreateTenant("CATALOG_A"), tenantB: h.CreateTenant("CATALOG_B"),
		actor: h.CreateActor("catalog-operator", "Catalog Operator"),
	}
}

func (f *fixture) rc(tenant uuid.UUID) identity.RequestContext {
	perms := map[string]struct{}{"catalog.read": {}, "catalog.manage": {}}
	return identity.RequestContext{
		TenantID: tenant, MembershipID: uuid.New(),
		Principal: identity.Principal{ActorID: f.actor}, Permissions: perms,
	}
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

func (f *fixture) category(t *testing.T, code string, parent *uuid.UUID) application.CategoryRecord {
	t.Helper()
	in := domain.NewCategory{Code: code, Name: code + " kategorisi", Domain: "HEALTH", Active: true}
	if parent != nil {
		in.ParentID = parent.String()
	}
	rec, err := f.svc.CreateCategory(context.Background(), f.rc(f.tenantA), in)
	if err != nil {
		t.Fatalf("create category %s: %v", code, err)
	}
	return rec
}

func (f *fixture) definition(t *testing.T, categoryID uuid.UUID, code string) application.DefinitionRecord {
	t.Helper()
	rec, err := f.svc.CreateDefinition(context.Background(), f.rc(f.tenantA), domain.NewDefinition{
		CategoryID: categoryID.String(), Code: code, Name: code + " hizmeti",
		FulfillmentMode: "SESSION", DefaultUnitType: "SESSION", RequiresProvider: true, Active: true,
	})
	if err != nil {
		t.Fatalf("create definition %s: %v", code, err)
	}
	return rec
}

func (f *fixture) codeSystem(t *testing.T, code, version string) application.CodeSystemRecord {
	t.Helper()
	rec, err := f.svc.CreateCodeSystem(context.Background(), f.rc(f.tenantA), domain.NewCodeSystem{
		Code: code, Name: code + " sistemi", Version: version, Authority: "SGK",
		ValidFrom: day(t, "2026-01-01"),
	})
	if err != nil {
		t.Fatalf("create code system %s %s: %v", code, version, err)
	}
	return rec
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

func TestCategoryTreeRefusesCyclesAndDepth(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA)

	root := f.category(t, "L1", nil)
	l2 := f.category(t, "L2", &root.ID)
	l3 := f.category(t, "L3", &l2.ID)
	l4 := f.category(t, "L4", &l3.ID)
	l5 := f.category(t, "L5", &l4.ID)
	l6 := f.category(t, "L6", &l5.ID)

	// Level seven is one past the cap.
	_, err := f.svc.CreateCategory(ctx, rc, domain.NewCategory{
		Code: "L7", Name: "Yedinci seviye", Domain: "HEALTH", Active: true, ParentID: l6.ID.String(),
	})
	if got := fieldCodes(t, err)["parentId"]; got != "DEPTH_EXCEEDED" {
		t.Fatalf("parentId code = %q, want DEPTH_EXCEEDED", got)
	}

	// Re-parenting the root three levels below itself closes a loop.
	parent := l4.ID.String()
	_, err = f.svc.UpdateCategory(ctx, rc, root.ID, domain.CategoryPatch{
		ParentID: &parent, ExpectedVersion: root.RowVersion,
	})
	if !errors.Is(err, domain.ErrCategoryCycle) {
		t.Fatalf("expected ErrCategoryCycle, got %v", err)
	}

	// A category may not become its own parent either.
	self := root.ID.String()
	_, err = f.svc.UpdateCategory(ctx, rc, root.ID, domain.CategoryPatch{
		ParentID: &self, ExpectedVersion: root.RowVersion,
	})
	if !errors.Is(err, domain.ErrCategoryCycle) {
		t.Fatalf("expected ErrCategoryCycle for a self parent, got %v", err)
	}

	// Moving a two-level subtree under level five would reach level seven.
	sub := f.category(t, "SUB", nil)
	f.category(t, "SUB_CHILD", &sub.ID)
	target := l5.ID.String()
	_, err = f.svc.UpdateCategory(ctx, rc, sub.ID, domain.CategoryPatch{
		ParentID: &target, ExpectedVersion: sub.RowVersion,
	})
	if got := fieldCodes(t, err)["parentId"]; got != "DEPTH_EXCEEDED" {
		t.Fatalf("parentId code = %q, want DEPTH_EXCEEDED", got)
	}
}

func TestCategoryUpdateUsesOptimisticConcurrency(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA)

	root := f.category(t, "OPT", nil)
	name := "Yeni ad"
	updated, err := f.svc.UpdateCategory(ctx, rc, root.ID, domain.CategoryPatch{
		Name: &name, ExpectedVersion: root.RowVersion,
	})
	if err != nil {
		t.Fatalf("update category: %v", err)
	}
	if updated.Name != name {
		t.Fatalf("name = %q, want %q", updated.Name, name)
	}
	// The row_version moved, so the caller's old ETag is stale.
	if updated.RowVersion == root.RowVersion {
		t.Fatal("the category token did not move")
	}
	if _, err := f.svc.UpdateCategory(ctx, rc, root.ID, domain.CategoryPatch{
		Name: &name, ExpectedVersion: root.RowVersion,
	}); !errors.Is(err, application.ErrVersionMismatch) {
		t.Fatalf("expected ErrVersionMismatch, got %v", err)
	}
}

func TestServiceDefinitionLifecycle(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA)

	category := f.category(t, "PHYSIO", nil)
	def := f.definition(t, category.ID, "PHYSIO_SESSION")
	if def.CategoryCode != "PHYSIO" || def.Domain != "HEALTH" {
		t.Fatalf("definition joined its category wrong: %+v", def)
	}

	// The code is unique within the tenant.
	if _, err := f.svc.CreateDefinition(ctx, rc, domain.NewDefinition{
		CategoryID: category.ID.String(), Code: "PHYSIO_SESSION", Name: "Kopya",
		FulfillmentMode: "SESSION", DefaultUnitType: "SESSION",
	}); !errors.Is(err, application.ErrDefinitionCodeTaken) {
		t.Fatalf("expected ErrDefinitionCodeTaken, got %v", err)
	}

	// Deactivating is allowed at any time and does not cascade.
	inactive := false
	retired, err := f.svc.UpdateDefinition(ctx, rc, def.ID, domain.DefinitionPatch{
		Active: &inactive, ExpectedVersion: def.RowVersion,
	})
	if err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if retired.Active {
		t.Fatal("the definition is still active")
	}
	if retired.RowVersion != def.RowVersion+1 {
		t.Fatalf("row_version %d -> %d, want +1", def.RowVersion, retired.RowVersion)
	}
	if _, err := f.svc.GetCategory(ctx, rc, category.ID); err != nil {
		t.Fatalf("the category must survive its definition being retired: %v", err)
	}

	// Search finds it by code and by name; the filters narrow the same list.
	page, err := f.svc.ListDefinitions(ctx, rc, application.ListFilter{Query: "PHYSIO_SES"})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("search by code: %d items, err %v", len(page.Items), err)
	}
	page, err = f.svc.ListDefinitions(ctx, rc, application.ListFilter{Query: "hizmeti", Active: &inactive})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("search by name: %d items, err %v", len(page.Items), err)
	}
	active := true
	page, err = f.svc.ListDefinitions(ctx, rc, application.ListFilter{Active: &active})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("active filter: %d items, err %v", len(page.Items), err)
	}
}

func TestCodeSystemEditionsLiveSideBySide(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA)

	f.codeSystem(t, "SUT", "2024")
	f.codeSystem(t, "SUT", "2026")
	if _, err := f.svc.CreateCodeSystem(ctx, rc, domain.NewCodeSystem{
		Code: "SUT", Name: "Kopya", Version: "2026", Authority: "SGK", ValidFrom: day(t, "2026-01-01"),
	}); !errors.Is(err, application.ErrCodeSystemTaken) {
		t.Fatalf("expected ErrCodeSystemTaken, got %v", err)
	}

	page, err := f.svc.ListCodeSystems(ctx, rc, application.ListFilter{Query: "SUT"})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("list code systems: %d items, err %v", len(page.Items), err)
	}
}

func TestImportCodeValuesIsAllOrNothingAndCounts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA)
	system := f.codeSystem(t, "ICD10", "2026")

	row := func(code, display, from string) domain.CodeValueInput {
		return domain.CodeValueInput{
			Code: code, Display: display, ValidFrom: day(t, from), Active: true, Attributes: []byte(`{}`),
		}
	}

	first, err := f.svc.ImportCodeValues(ctx, rc, system.ID, []domain.CodeValueInput{
		row("M54.5", "Bel ağrısı", "2026-01-01"),
		row("M54.6", "Sırt ağrısı", "2026-01-01"),
	})
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if first.Created != 2 || first.Updated != 0 || first.Skipped != 0 {
		t.Fatalf("first import summary = %+v, want 2 created", first)
	}

	// Re-importing the identical payload writes nothing.
	again, err := f.svc.ImportCodeValues(ctx, rc, system.ID, []domain.CodeValueInput{
		row("M54.5", "Bel ağrısı", "2026-01-01"),
		row("M54.6", "Sırt ağrısı", "2026-01-01"),
	})
	if err != nil {
		t.Fatalf("replay import: %v", err)
	}
	if again.Skipped != 2 || again.Created != 0 || again.Updated != 0 {
		t.Fatalf("replay summary = %+v, want 2 skipped", again)
	}

	// One changed display is an update, a new key is a create.
	mixed, err := f.svc.ImportCodeValues(ctx, rc, system.ID, []domain.CodeValueInput{
		row("M54.5", "Bel ağrısı (düzeltildi)", "2026-01-01"),
		row("M54.7", "Kuyruk sokumu ağrısı", "2026-01-01"),
	})
	if err != nil {
		t.Fatalf("mixed import: %v", err)
	}
	if mixed.Updated != 1 || mixed.Created != 1 || mixed.Skipped != 0 {
		t.Fatalf("mixed summary = %+v, want 1 updated and 1 created", mixed)
	}

	// A single bad row rejects the whole batch, so nothing from it reaches the table.
	bad := row("M54.9", "", "2026-01-01")
	err = func() error {
		_, err := f.svc.ImportCodeValues(ctx, rc, system.ID, []domain.CodeValueInput{
			row("M54.8", "Yeni satır", "2026-01-01"), bad,
		})
		return err
	}()
	if got := fieldCodes(t, err)["items[1].display"]; got != "LENGTH" {
		t.Fatalf("items[1].display = %q, want LENGTH", got)
	}
	page, err := f.svc.ListCodeValues(ctx, rc, system.ID, application.CodeValueFilter{Limit: 100})
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("the rejected batch left %d rows, want the 3 written before it", len(page.Items))
	}

	// An oversized batch is refused before any work happens.
	oversized := make([]domain.CodeValueInput, 0, domain.MaxImportRows+1)
	for i := range domain.MaxImportRows + 1 {
		oversized = append(oversized, row("X"+strconv.Itoa(i), "Satır", "2026-01-01"))
	}
	if _, err := f.svc.ImportCodeValues(ctx, rc, system.ID, oversized); fieldCodes(t, err)["items"] != "MAX_ITEMS" {
		t.Fatalf("expected MAX_ITEMS on items, got %v", err)
	}
}

func TestImportCodeValuesAcceptsAFullBatch(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA)
	system := f.codeSystem(t, "BIGSYS", "2026")

	items := make([]domain.CodeValueInput, 0, domain.MaxImportRows)
	for i := range domain.MaxImportRows {
		items = append(items, domain.CodeValueInput{
			Code: "C" + strconv.Itoa(i), Display: "Kod " + strconv.Itoa(i),
			ValidFrom: day(t, "2026-01-01"), Active: true, Attributes: []byte(`{}`),
		})
	}
	summary, err := f.svc.ImportCodeValues(ctx, rc, system.ID, items)
	if err != nil {
		t.Fatalf("import %d rows: %v", domain.MaxImportRows, err)
	}
	if summary.Created != domain.MaxImportRows {
		t.Fatalf("created %d rows, want %d", summary.Created, domain.MaxImportRows)
	}

	page, err := f.svc.ListCodeValues(ctx, rc, system.ID, application.CodeValueFilter{Code: "C4999"})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("exact code read: %d items, err %v", len(page.Items), err)
	}
}

func TestCodeValuesAreReadAsOfADate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA)
	system := f.codeSystem(t, "SUT", "2026")

	if _, err := f.svc.ImportCodeValues(ctx, rc, system.ID, []domain.CodeValueInput{
		{Code: "P701010", Display: "Eski karşılık", ValidFrom: day(t, "2025-01-01"),
			ValidTo: dayPtr(t, "2026-01-01"), Active: true, Attributes: []byte(`{}`)},
		{Code: "P701010", Display: "Yeni karşılık", ValidFrom: day(t, "2026-01-01"),
			Active: true, Attributes: []byte(`{"unit":"SESSION"}`)},
	}); err != nil {
		t.Fatalf("import: %v", err)
	}

	read := func(asOf string) []string {
		t.Helper()
		filter := application.CodeValueFilter{Code: "P701010"}
		if asOf != "" {
			filter.AsOf = day(t, asOf)
		}
		page, err := f.svc.ListCodeValues(ctx, rc, system.ID, filter)
		if err != nil {
			t.Fatalf("as-of %q: %v", asOf, err)
		}
		out := make([]string, 0, len(page.Items))
		for _, v := range page.Items {
			out = append(out, v.Display)
		}
		return out
	}

	if got := read("2025-06-01"); len(got) != 1 || got[0] != "Eski karşılık" {
		t.Fatalf("as of 2025-06-01 = %v", got)
	}
	if got := read(""); len(got) != 1 || got[0] != "Yeni karşılık" {
		t.Fatalf("the default as-of is today, got %v", got)
	}
	if got := read("2024-01-01"); len(got) != 0 {
		t.Fatalf("as of 2024-01-01 = %v, want nothing", got)
	}

	// Attributes survive the round trip unchanged.
	page, err := f.svc.ListCodeValues(ctx, rc, system.ID, application.CodeValueFilter{Code: "P701010"})
	if err != nil {
		t.Fatalf("read attributes: %v", err)
	}
	if string(page.Items[0].Attributes) != `{"unit": "SESSION"}` {
		t.Fatalf("attributes = %s", page.Items[0].Attributes)
	}
}

func TestReplaceMappingsRefusesOverlapAndMovesTheETag(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA)

	category := f.category(t, "PHYSIO", nil)
	def := f.definition(t, category.ID, "PHYSIO_SESSION")
	sut := f.codeSystem(t, "SUT", "2026")
	icd := f.codeSystem(t, "ICD10", "2026")

	// A definition may map to several systems at once.
	result, err := f.svc.ReplaceMappings(ctx, rc, def.ID, []domain.MappingInput{
		{CodeSystemID: sut.ID.String(), Code: "P701010", ValidFrom: day(t, "2026-01-01"), Primary: true},
		{CodeSystemID: icd.ID.String(), Code: "M54.5", ValidFrom: day(t, "2026-01-01"), Primary: true},
	}, def.RowVersion)
	if err != nil {
		t.Fatalf("replace mappings: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("stored %d mappings, want 2", len(result.Items))
	}
	if result.RowVersion == def.RowVersion {
		t.Fatal("replacing the mapping set must move the definition ETag")
	}
	if result.Items[0].CodeSystemCode != "ICD10" {
		t.Fatalf("mappings are ordered by code system, got %q first", result.Items[0].CodeSystemCode)
	}

	// A self-contradicting payload is refused before anything is written.
	_, err = f.svc.ReplaceMappings(ctx, rc, def.ID, []domain.MappingInput{
		{CodeSystemID: sut.ID.String(), Code: "P701010", ValidFrom: day(t, "2026-01-01")},
		{CodeSystemID: sut.ID.String(), Code: "P701010", ValidFrom: day(t, "2026-06-01")},
	}, result.RowVersion)
	if !errors.Is(err, domain.ErrMappingOverlap) {
		t.Fatalf("expected ErrMappingOverlap, got %v", err)
	}
	after, err := f.svc.ListMappings(ctx, rc, def.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(after.Items) != 2 {
		t.Fatalf("the refused replacement changed the set: %d rows", len(after.Items))
	}

	// A stale If-Match is refused.
	if _, err := f.svc.ReplaceMappings(ctx, rc, def.ID, nil, def.RowVersion); !errors.Is(err, application.ErrVersionMismatch) {
		t.Fatalf("expected ErrVersionMismatch, got %v", err)
	}

	// Successive periods for the same code are fine, and so is an empty set.
	_, err = f.svc.ReplaceMappings(ctx, rc, def.ID, []domain.MappingInput{
		{CodeSystemID: sut.ID.String(), Code: "P701010", ValidFrom: day(t, "2025-01-01"),
			ValidTo: dayPtr(t, "2026-01-01"), Primary: true},
		{CodeSystemID: sut.ID.String(), Code: "P701010", ValidFrom: day(t, "2026-01-01"), Primary: true},
	}, after.RowVersion)
	if err != nil {
		t.Fatalf("successive periods: %v", err)
	}
}

func TestCatalogIsInvisibleToAnotherTenant(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	category := f.category(t, "PHYSIO", nil)
	def := f.definition(t, category.ID, "PHYSIO_SESSION")
	system := f.codeSystem(t, "SUT", "2026")

	other := f.rc(f.tenantB)
	if _, err := f.svc.GetCategory(ctx, other, category.ID); !errors.Is(err, application.ErrCategoryNotFound) {
		t.Fatalf("category leaked across tenants: %v", err)
	}
	if _, err := f.svc.GetDefinition(ctx, other, def.ID); !errors.Is(err, application.ErrDefinitionNotFound) {
		t.Fatalf("definition leaked across tenants: %v", err)
	}
	if _, err := f.svc.GetCodeSystem(ctx, other, system.ID); !errors.Is(err, application.ErrCodeSystemNotFound) {
		t.Fatalf("code system leaked across tenants: %v", err)
	}
	page, err := f.svc.ListDefinitions(ctx, other, application.ListFilter{})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("the other tenant lists %d definitions, err %v", len(page.Items), err)
	}
}

func TestCategoryPagingIsStableWhileRowsAreInserted(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.rc(f.tenantA)

	for i := range 5 {
		f.category(t, "C"+strconv.Itoa(i), nil)
	}
	first, err := f.svc.ListCategories(ctx, rc, application.ListFilter{Limit: 2})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %d items, cursor %q", len(first.Items), first.NextCursor)
	}

	// New rows sort ahead of the cursor, so they cannot shift the page the caller is
	// walking through.
	f.category(t, "C_NEW", nil)

	second, err := f.svc.ListCategories(ctx, rc, application.ListFilter{Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Items) != 2 {
		t.Fatalf("second page = %d items", len(second.Items))
	}
	seen := map[uuid.UUID]bool{}
	for _, c := range append(append([]application.CategoryRecord{}, first.Items...), second.Items...) {
		if seen[c.ID] {
			t.Fatalf("category %s appeared on two pages", c.Code)
		}
		seen[c.ID] = true
	}
	if _, err := f.svc.ListCategories(ctx, rc, application.ListFilter{Cursor: "not-a-cursor"}); !errors.Is(err, httpx.ErrInvalidCursor) {
		t.Fatalf("expected ErrInvalidCursor, got %v", err)
	}
}
