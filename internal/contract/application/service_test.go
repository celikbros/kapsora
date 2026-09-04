package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/contract/application"
	"github.com/celikbros/kapsora/internal/contract/domain"
	contractpg "github.com/celikbros/kapsora/internal/contract/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/contract/selection"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// serviceDate is pinned so every selection assertion is deterministic. 2026-06-15 is a
// Monday, which the weekday-mask cases rely on.
var serviceDate = time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)

type fixture struct {
	h        *dbtest.Harness
	svc      *application.Service
	tenantA  uuid.UUID
	tenantB  uuid.UUID
	maker    uuid.UUID
	checker  uuid.UUID
	payerA   uuid.UUID
	provider uuid.UUID
	location uuid.UUID
	other    uuid.UUID
	root     uuid.UUID
	leaf     uuid.UUID
	physio   uuid.UUID
	massage  uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h := dbtest.New(t)
	cursors, err := httpx.NewCursorCodec([]byte("fedcba9876543210fedcba9876543210"))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := application.New(application.Deps{
		Pool: h.App, Repo: contractpg.New(), Audit: auditpg.New(), Cursors: cursors,
		Now: func() time.Time { return serviceDate },
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		h: h, svc: svc,
		tenantA: h.CreateTenant("CONTRACT_A"), tenantB: h.CreateTenant("CONTRACT_B"),
		maker:   h.CreateActor("contract-maker", "Contract Maker"),
		checker: h.CreateActor("contract-checker", "Contract Checker"),
	}
	f.payerA = h.CreateTenantOrganization(f.tenantA, "Sponsor A", "SPONSOR")
	providerOrg := h.CreateTenantOrganization(f.tenantA, "Hastane A", "PROVIDER")

	ctx, cancel := h.Ctx()
	defer cancel()
	must := func(err error, what string) {
		t.Helper()
		if err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO provider.provider_profile (tenant_id, tenant_organization_id, provider_type, status)
		VALUES ($1, $2, 'HOSPITAL', 'ACTIVE') RETURNING id`, f.tenantA, providerOrg).Scan(&f.provider), "provider")
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO provider.location (tenant_id, provider_profile_id, code, name)
		VALUES ($1, $2, 'MERKEZ', 'Merkez') RETURNING id`, f.tenantA, f.provider).Scan(&f.location), "location")
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO provider.location (tenant_id, provider_profile_id, code, name)
		VALUES ($1, $2, 'SUBE', 'Şube') RETURNING id`, f.tenantA, f.provider).Scan(&f.other), "second location")
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'HEALTH_ROOT', 'Sağlık', 'HEALTH') RETURNING id`, f.tenantA).Scan(&f.root), "root category")
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.service_category (tenant_id, parent_id, code, name, domain_code)
		VALUES ($1, $2, 'PHYSIO', 'Fizyoterapi', 'HEALTH') RETURNING id`, f.tenantA, f.root).Scan(&f.leaf), "leaf category")
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name, fulfillment_mode, default_unit_type)
		VALUES ($1, $2, 'PHYSIO_SESSION', 'Fizyoterapi seansı', 'SESSION', 'SESSION') RETURNING id`,
		f.tenantA, f.leaf).Scan(&f.physio), "physio definition")
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name, fulfillment_mode, default_unit_type)
		VALUES ($1, $2, 'MASSAGE', 'Masaj', 'SESSION', 'SESSION') RETURNING id`,
		f.tenantA, f.leaf).Scan(&f.massage), "massage definition")
	return f
}

// rc is a back office actor holding every contract permission with a fresh step-up.
func (f *fixture) rc(tenant, actor uuid.UUID) identity.RequestContext {
	return identity.RequestContext{
		TenantID: tenant, MembershipID: uuid.New(),
		Principal: identity.Principal{ActorID: actor}, StepUpValid: true,
		Permissions: map[string]struct{}{
			"contract.read": {}, "contract.manage": {}, "contract.publish": {},
		},
	}
}

func (f *fixture) makerRC() identity.RequestContext   { return f.rc(f.tenantA, f.maker) }
func (f *fixture) checkerRC() identity.RequestContext { return f.rc(f.tenantA, f.checker) }

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

// contract creates one contract and activates it, which is what the price selection
// requires of the agreement above a published version.
func (f *fixture) contract(t *testing.T, code string) application.ContractRecord {
	t.Helper()
	record, err := f.svc.CreateContract(context.Background(), f.makerRC(), domain.NewContract{
		Code: code, Name: "Sözleşme " + code,
		PayerOrganizationID: f.payerA.String(), ProviderProfileID: f.provider.String(),
		DomainCode: "HEALTH",
	})
	if err != nil {
		t.Fatalf("create contract %s: %v", code, err)
	}
	active := "ACTIVE"
	record, err = f.svc.UpdateContract(context.Background(), f.makerRC(), record.ID, domain.ContractPatch{
		Status: &active, ExpectedVersion: record.RowVersion,
	})
	if err != nil {
		t.Fatalf("activate contract %s: %v", code, err)
	}
	return record
}

// version opens a draft version with a period and returns it.
func (f *fixture) version(t *testing.T, contractID uuid.UUID, from string, to *time.Time) application.VersionView {
	t.Helper()
	view, err := f.svc.CreateVersion(context.Background(), f.makerRC(), contractID, application.NewVersionInput{
		ValidFrom: dayPtr(t, from), ValidTo: to,
	})
	if err != nil {
		t.Fatalf("create version: %v", err)
	}
	return view
}

// priceList writes a single price list under a draft version and returns it.
func (f *fixture) priceList(t *testing.T, view application.VersionView, in domain.PriceListInput) application.PriceListRecord {
	t.Helper()
	result, err := f.svc.ReplacePriceLists(context.Background(), f.makerRC(), view.Version.ID,
		[]domain.PriceListInput{in}, view.Version.RowVersion)
	if err != nil {
		t.Fatalf("replace price lists: %v", err)
	}
	for _, l := range result.Items {
		if l.Code == in.Code {
			return l
		}
	}
	t.Fatalf("price list %s not found after the write", in.Code)
	return application.PriceListRecord{}
}

// items writes a price item set into one list.
func (f *fixture) items(t *testing.T, list application.PriceListRecord, rows ...domain.PriceItemInput) application.PriceItemPage {
	t.Helper()
	page, err := f.svc.ReplacePriceItems(context.Background(), f.makerRC(), list.ID, rows, list.RowVersion)
	if err != nil {
		t.Fatalf("replace price items: %v", err)
	}
	return page
}

// publish runs the whole maker-checker flow: the maker submits, the checker publishes.
func (f *fixture) publish(t *testing.T, versionID uuid.UUID, rowVersion int64) application.VersionView {
	t.Helper()
	submitted, err := f.svc.SubmitVersion(context.Background(), f.makerRC(), versionID, nil, rowVersion)
	if err != nil {
		t.Fatalf("submit version: %v", err)
	}
	published, err := f.svc.PublishVersion(context.Background(), f.checkerRC(), versionID, nil,
		submitted.Version.RowVersion)
	if err != nil {
		t.Fatalf("publish version: %v", err)
	}
	return published
}

// fixedItem is a FIXED price for one target on the open period from 2026-01-01.
func fixedItem(t *testing.T, amount string) domain.PriceItemInput {
	t.Helper()
	return domain.PriceItemInput{
		UnitType: "SESSION", PricingMethod: "FIXED", Amount: amount,
		ValidFrom: day(t, "2026-01-01"), Priority: 100,
	}
}

func TestPublishedVersionRefusesEveryWrite(t *testing.T) {
	f := newFixture(t)
	contract := f.contract(t, "IMMUTABLE")
	view := f.version(t, contract.ID, "2026-01-01", nil)
	list := f.priceList(t, view, domain.PriceListInput{Code: "STANDART", Name: "Standart", Priority: 100})
	item := fixedItem(t, "250.500000")
	item.ServiceDefinitionID = f.physio.String()
	f.items(t, list, item)

	current, err := f.svc.GetVersion(context.Background(), f.makerRC(), view.Version.ID)
	if err != nil {
		t.Fatalf("reload version: %v", err)
	}
	published := f.publish(t, view.Version.ID, current.Version.RowVersion)
	if published.Version.Status != domain.VersionPublished {
		t.Fatalf("status is %s, want PUBLISHED", published.Version.Status)
	}

	rowVersion := published.Version.RowVersion
	notes := "sonradan"
	writes := map[string]func() error{
		"patch": func() error {
			_, err := f.svc.UpdateVersion(context.Background(), f.makerRC(), view.Version.ID,
				application.VersionPatch{Notes: &notes, ExpectedVersion: rowVersion})
			return err
		},
		"price lists": func() error {
			_, err := f.svc.ReplacePriceLists(context.Background(), f.makerRC(), view.Version.ID,
				[]domain.PriceListInput{{Code: "YENI", Name: "Yeni liste", Priority: 100}}, rowVersion)
			return err
		},
		"packages": func() error {
			_, err := f.svc.ReplacePackages(context.Background(), f.makerRC(), view.Version.ID,
				[]domain.PackageInput{}, rowVersion)
			return err
		},
		"quotas": func() error {
			_, err := f.svc.ReplaceQuotas(context.Background(), f.makerRC(), view.Version.ID,
				[]domain.QuotaInput{}, rowVersion)
			return err
		},
		"payment term": func() error {
			_, err := f.svc.PutPaymentTerm(context.Background(), f.makerRC(), view.Version.ID,
				domain.PaymentTermInput{DueDays: 30, SettlementMethod: "BANK_TRANSFER", TaxBehaviour: "EXEMPT"},
				rowVersion)
			return err
		},
		"submit again": func() error {
			_, err := f.svc.SubmitVersion(context.Background(), f.makerRC(), view.Version.ID, nil, rowVersion)
			return err
		},
	}
	for name, write := range writes {
		if err := write(); !errors.Is(err, application.ErrVersionImmutable) {
			t.Fatalf("%s on a published version: got %v, want ErrVersionImmutable", name, err)
		}
	}

	// The price items of a published list are frozen too, under the list's own ETag.
	reloaded, err := f.svc.ListPriceItems(context.Background(), f.makerRC(), list.ID, "", 0)
	if err != nil {
		t.Fatalf("list price items: %v", err)
	}
	_, err = f.svc.ReplacePriceItems(context.Background(), f.makerRC(), list.ID,
		[]domain.PriceItemInput{}, reloaded.RowVersion)
	if !errors.Is(err, application.ErrVersionImmutable) {
		t.Fatalf("price items on a published version: got %v, want ErrVersionImmutable", err)
	}
}

func TestSubmitRefusesAPriceSheetWithNoItem(t *testing.T) {
	f := newFixture(t)
	contract := f.contract(t, "EMPTY")
	view := f.version(t, contract.ID, "2026-01-01", nil)
	// A list with no items in it is still an empty sheet.
	f.priceList(t, view, domain.PriceListInput{Code: "BOS", Name: "Boş liste", Priority: 100})

	current, err := f.svc.GetVersion(context.Background(), f.makerRC(), view.Version.ID)
	if err != nil {
		t.Fatalf("reload version: %v", err)
	}
	_, err = f.svc.SubmitVersion(context.Background(), f.makerRC(), view.Version.ID, nil, current.Version.RowVersion)
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("submit with no price item: got %v, want a validation error", err)
	}
	if ve.Fields[0].Field != "priceLists" {
		t.Fatalf("submit refused on field %q, want priceLists", ve.Fields[0].Field)
	}
}

func TestPublishNeedsADifferentActor(t *testing.T) {
	f := newFixture(t)
	contract := f.contract(t, "MAKERCHECKER")
	view := f.version(t, contract.ID, "2026-01-01", nil)
	list := f.priceList(t, view, domain.PriceListInput{Code: "STANDART", Name: "Standart", Priority: 100})
	item := fixedItem(t, "100")
	item.ServiceDefinitionID = f.physio.String()
	f.items(t, list, item)

	current, err := f.svc.GetVersion(context.Background(), f.makerRC(), view.Version.ID)
	if err != nil {
		t.Fatalf("reload version: %v", err)
	}
	submitted, err := f.svc.SubmitVersion(context.Background(), f.makerRC(), view.Version.ID, nil,
		current.Version.RowVersion)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	_, err = f.svc.PublishVersion(context.Background(), f.makerRC(), view.Version.ID, nil,
		submitted.Version.RowVersion)
	if !errors.Is(err, application.ErrMakerCheckerSame) {
		t.Fatalf("publish by the submitter: got %v, want ErrMakerCheckerSame", err)
	}

	// The refusal is audited in its own transaction, so it survives the rollback.
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var denied int
	if err := f.h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM audit.event
		 WHERE tenant_id = $1 AND action_code = 'contract_version.publish' AND outcome = 'DENIED'`,
		f.tenantA).Scan(&denied); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if denied != 1 {
		t.Fatalf("denied publish audit rows: %d, want 1", denied)
	}

	if _, err := f.svc.PublishVersion(context.Background(), f.checkerRC(), view.Version.ID, nil,
		submitted.Version.RowVersion); err != nil {
		t.Fatalf("publish by a second actor: %v", err)
	}
}

func TestConfigurationHashIsStableForTheSameContent(t *testing.T) {
	f := newFixture(t)
	first := f.contract(t, "HASH_ONE")
	second := f.contract(t, "HASH_TWO")

	build := func(contractID uuid.UUID, amount string) application.VersionView {
		t.Helper()
		view := f.version(t, contractID, "2026-01-01", nil)
		list := f.priceList(t, view, domain.PriceListInput{Code: "STANDART", Name: "Standart", Priority: 100})
		item := fixedItem(t, amount)
		item.ServiceDefinitionID = f.physio.String()
		f.items(t, list, item)
		current, err := f.svc.GetVersion(context.Background(), f.makerRC(), view.Version.ID)
		if err != nil {
			t.Fatalf("reload version: %v", err)
		}
		return f.publish(t, view.Version.ID, current.Version.RowVersion)
	}

	a := build(first.ID, "250.500000")
	b := build(second.ID, "250.5")
	if a.Version.ConfigurationHash == nil || b.Version.ConfigurationHash == nil {
		t.Fatal("published version has no configuration hash")
	}
	// "250.500000" and "250.5" are the same agreed price, so they must hash the same.
	if *a.Version.ConfigurationHash != *b.Version.ConfigurationHash {
		t.Fatalf("same content hashed differently:\n%s\n%s",
			*a.Version.ConfigurationHash, *b.Version.ConfigurationHash)
	}

	third := f.contract(t, "HASH_THREE")
	c := build(third.ID, "250.5001")
	if *a.Version.ConfigurationHash == *c.Version.ConfigurationHash {
		t.Fatal("a changed price produced the same configuration hash")
	}
}

func TestPublishRefusesAnOverlappingPeriod(t *testing.T) {
	f := newFixture(t)
	contract := f.contract(t, "OVERLAP")

	sheet := func(from string, to *time.Time) uuid.UUID {
		t.Helper()
		view := f.version(t, contract.ID, from, to)
		list := f.priceList(t, view, domain.PriceListInput{Code: "STANDART", Name: "Standart", Priority: 100})
		item := fixedItem(t, "100")
		item.ServiceDefinitionID = f.physio.String()
		f.items(t, list, item)
		return view.Version.ID
	}

	firstID := sheet("2026-01-01", dayPtr(t, "2027-01-01"))
	current, err := f.svc.GetVersion(context.Background(), f.makerRC(), firstID)
	if err != nil {
		t.Fatalf("reload version: %v", err)
	}
	f.publish(t, firstID, current.Version.RowVersion)

	secondID := sheet("2026-06-01", nil)
	current, err = f.svc.GetVersion(context.Background(), f.makerRC(), secondID)
	if err != nil {
		t.Fatalf("reload version: %v", err)
	}
	submitted, err := f.svc.SubmitVersion(context.Background(), f.makerRC(), secondID, nil, current.Version.RowVersion)
	if err != nil {
		t.Fatalf("submit second version: %v", err)
	}
	_, err = f.svc.PublishVersion(context.Background(), f.checkerRC(), secondID, nil, submitted.Version.RowVersion)
	if !errors.Is(err, application.ErrVersionOverlap) {
		t.Fatalf("publishing an overlapping version: got %v, want ErrVersionOverlap", err)
	}
}

// publishedSheet builds one published version holding the given items and returns the
// price list they were written into.
func (f *fixture) publishedSheet(t *testing.T, code string, list domain.PriceListInput,
	rows ...domain.PriceItemInput,
) application.VersionView {
	t.Helper()
	contract := f.contract(t, code)
	view := f.version(t, contract.ID, "2026-01-01", nil)
	created := f.priceList(t, view, list)
	f.items(t, created, rows...)
	current, err := f.svc.GetVersion(context.Background(), f.makerRC(), view.Version.ID)
	if err != nil {
		t.Fatalf("reload version: %v", err)
	}
	return f.publish(t, view.Version.ID, current.Version.RowVersion)
}

func (f *fixture) resolve(t *testing.T, definition uuid.UUID, location *uuid.UUID) application.ResolveResult {
	t.Helper()
	result, err := f.svc.ResolvePrice(context.Background(), f.makerRC(), application.ResolveRequest{
		ServiceDate: serviceDate, ProviderProfileID: f.provider,
		ServiceDefinitionID: definition, LocationID: location,
	})
	if err != nil {
		t.Fatalf("resolve price: %v", err)
	}
	return result
}

func TestResolvePricePrefersTheMostSpecificMatch(t *testing.T) {
	f := newFixture(t)

	definitionPrice := fixedItem(t, "300")
	definitionPrice.ServiceDefinitionID = f.physio.String()
	categoryPrice := fixedItem(t, "200")
	categoryPrice.ServiceCategoryID = f.leaf.String()
	rootPrice := fixedItem(t, "100")
	rootPrice.ServiceCategoryID = f.root.String()

	f.publishedSheet(t, "LADDER", domain.PriceListInput{Code: "STANDART", Name: "Standart", Priority: 100},
		definitionPrice, categoryPrice, rootPrice)

	result := f.resolve(t, f.physio, nil)
	if result.Winner == nil {
		t.Fatalf("no winner: %s", result.Reason)
	}
	if got := result.Winner.Detail.Amount; got != "300" {
		t.Fatalf("winner amount %q, want the definition price 300", got)
	}
	if result.Winner.Scored.Candidate.Target != selection.TargetDefinition {
		t.Fatalf("winner matched via %v, want the definition", result.Winner.Scored.Candidate.Target)
	}
	if len(result.Considered) != 3 {
		t.Fatalf("considered %d candidates, want all 3 explained", len(result.Considered))
	}

	// A service the sheet only covers through the root category falls back to it, and the
	// nearer leaf category still beats the root.
	massage := f.resolve(t, f.massage, nil)
	if massage.Winner == nil {
		t.Fatalf("no winner for the second service: %s", massage.Reason)
	}
	if got := massage.Winner.Detail.Amount; got != "200" {
		t.Fatalf("second service amount %q, want the nearer category price 200", got)
	}
}

func TestResolvePricePrefersALocationPrice(t *testing.T) {
	f := newFixture(t)

	tenantWide := fixedItem(t, "300")
	tenantWide.ServiceDefinitionID = f.physio.String()
	atLocation := fixedItem(t, "450")
	atLocation.ServiceDefinitionID = f.physio.String()
	atLocation.LocationID = f.location.String()

	f.publishedSheet(t, "LOCATION", domain.PriceListInput{Code: "STANDART", Name: "Standart", Priority: 100},
		tenantWide, atLocation)

	atMerkez := f.resolve(t, f.physio, &f.location)
	if atMerkez.Winner == nil || atMerkez.Winner.Detail.Amount != "450" {
		t.Fatalf("at the priced location: %+v, want 450", atMerkez.Winner)
	}
	// Another location of the same provider falls back to the tenant-wide price.
	atSube := f.resolve(t, f.physio, &f.other)
	if atSube.Winner == nil || atSube.Winner.Detail.Amount != "300" {
		t.Fatalf("at another location: %+v, want the tenant-wide 300", atSube.Winner)
	}
}

func TestResolvePriceIgnoresExpiredAndOffSeasonRows(t *testing.T) {
	f := newFixture(t)

	expired := fixedItem(t, "999")
	expired.ServiceDefinitionID = f.physio.String()
	expired.ValidTo = dayPtr(t, "2026-02-01")
	current := fixedItem(t, "300")
	current.ServiceDefinitionID = f.physio.String()
	current.ValidFrom = day(t, "2026-02-01")

	f.publishedSheet(t, "PERIOD", domain.PriceListInput{Code: "STANDART", Name: "Standart", Priority: 100},
		expired, current)

	result := f.resolve(t, f.physio, nil)
	if result.Winner == nil || result.Winner.Detail.Amount != "300" {
		t.Fatalf("winner %+v, want the current 300", result.Winner)
	}
	var excluded int
	for _, c := range result.Considered {
		if !c.Scored.Matched && c.Scored.Excluded == "PERIOD" {
			excluded++
		}
	}
	if excluded != 1 {
		t.Fatalf("%d candidates excluded on PERIOD, want 1", excluded)
	}
}

func TestResolvePriceHonoursTheWeekdayMask(t *testing.T) {
	f := newFixture(t)

	// Bit 5 is Saturday; the service date is a Monday, so the list never applies.
	weekend := 1 << 5
	weekendItem := fixedItem(t, "500")
	weekendItem.ServiceDefinitionID = f.physio.String()

	view := f.publishedSheet(t, "WEEKDAY",
		domain.PriceListInput{Code: "HAFTASONU", Name: "Hafta sonu", Priority: 200, WeekdayMask: &weekend},
		weekendItem)
	if len(view.PriceLists) != 1 {
		t.Fatalf("published %d price lists, want 1", len(view.PriceLists))
	}

	result := f.resolve(t, f.physio, nil)
	if result.Winner != nil {
		t.Fatalf("a Saturday-only price won on a Monday: %+v", result.Winner)
	}
	if result.Reason != selection.ReasonNotFound {
		t.Fatalf("reason %q, want PRICE_NOT_FOUND", result.Reason)
	}
	if len(result.Considered) != 1 || result.Considered[0].Scored.Excluded != "WEEKDAY" {
		t.Fatalf("candidate explanation %+v, want one excluded on WEEKDAY", result.Considered)
	}
}

func TestResolvePriceRefusesToChooseBetweenEqualCandidates(t *testing.T) {
	f := newFixture(t)

	first := fixedItem(t, "300")
	first.ServiceDefinitionID = f.physio.String()
	second := fixedItem(t, "310")
	second.ServiceDefinitionID = f.physio.String()

	f.publishedSheet(t, "AMBIGUOUS", domain.PriceListInput{Code: "STANDART", Name: "Standart", Priority: 100},
		first, second)

	result := f.resolve(t, f.physio, nil)
	if result.Winner != nil {
		t.Fatalf("a tie produced a winner: %+v", result.Winner)
	}
	if result.Reason != selection.ReasonAmbiguous {
		t.Fatalf("reason %q, want PRICE_AMBIGUOUS", result.Reason)
	}
	if len(result.Tied) != 2 {
		t.Fatalf("%d tied candidates named, want both", len(result.Tied))
	}
	ids := map[uuid.UUID]bool{}
	for _, tied := range result.Tied {
		ids[tied.Scored.Candidate.PriceItemID] = true
	}
	if len(ids) != 2 {
		t.Fatalf("tied candidates name %d distinct price items, want 2", len(ids))
	}

	// A higher item priority is a real tie-break, so the ambiguity is fixable.
	if result.Tied[0].Scored.Score != result.Tied[1].Scored.Score {
		t.Fatalf("tied candidates scored %d and %d", result.Tied[0].Scored.Score, result.Tied[1].Scored.Score)
	}
}

func TestResolvePriceAnswersNotFoundWithoutACandidate(t *testing.T) {
	f := newFixture(t)
	f.contract(t, "NOTHING")

	result := f.resolve(t, f.physio, nil)
	if result.Winner != nil || result.Reason != selection.ReasonNotFound {
		t.Fatalf("resolve without any published price: %+v / %s", result.Winner, result.Reason)
	}
	if result.Outcome() != "NOT_FOUND" {
		t.Fatalf("outcome %q, want NOT_FOUND", result.Outcome())
	}
}

func TestResolvePriceScoresAPackagePrice(t *testing.T) {
	f := newFixture(t)
	contract := f.contract(t, "PACKAGE")
	view := f.version(t, contract.ID, "2026-01-01", nil)

	packages, err := f.svc.ReplacePackages(context.Background(), f.makerRC(), view.Version.ID,
		[]domain.PackageInput{{
			Code: "SEANS10", Name: "10 seans", InclusionRule: "ALL",
			Lines: []domain.PackageLineInput{{ServiceDefinitionID: f.physio.String(), IncludedQuantity: "10"}},
		}}, view.Version.RowVersion)
	if err != nil {
		t.Fatalf("replace packages: %v", err)
	}
	if len(packages.Items) != 1 || len(packages.Items[0].Lines) != 1 {
		t.Fatalf("stored packages %+v, want one with one line", packages.Items)
	}

	reloaded, err := f.svc.GetVersion(context.Background(), f.makerRC(), view.Version.ID)
	if err != nil {
		t.Fatalf("reload version: %v", err)
	}
	list := f.priceList(t, reloaded, domain.PriceListInput{Code: "STANDART", Name: "Standart", Priority: 100})

	packagePrice := fixedItem(t, "2500")
	packagePrice.PackageDefinitionID = packages.Items[0].ID.String()
	categoryPrice := fixedItem(t, "200")
	categoryPrice.ServiceCategoryID = f.leaf.String()
	f.items(t, list, packagePrice, categoryPrice)

	current, err := f.svc.GetVersion(context.Background(), f.makerRC(), view.Version.ID)
	if err != nil {
		t.Fatalf("reload version: %v", err)
	}
	f.publish(t, view.Version.ID, current.Version.RowVersion)

	result := f.resolve(t, f.physio, nil)
	if result.Winner == nil {
		t.Fatalf("no winner: %s", result.Reason)
	}
	if result.Winner.Scored.Candidate.Target != selection.TargetPackage {
		t.Fatalf("winner matched via %v, want the package", result.Winner.Scored.Candidate.Target)
	}
	if result.Winner.Detail.Amount != "2500" {
		t.Fatalf("winner amount %q, want 2500", result.Winner.Detail.Amount)
	}
}

func TestPriceAmountsRoundTripAsExactDecimals(t *testing.T) {
	f := newFixture(t)
	contract := f.contract(t, "MONEY")
	view := f.version(t, contract.ID, "2026-01-01", nil)
	list := f.priceList(t, view, domain.PriceListInput{Code: "STANDART", Name: "Standart", Priority: 100})

	item := domain.PriceItemInput{
		ServiceDefinitionID: f.physio.String(), UnitType: "SESSION", PricingMethod: "FIXED",
		// A value no float64 holds exactly.
		Amount: "12345678901.123457", MinAmount: "0.000001", MaxAmount: "99999999999.999999",
		MemberShareMethod: "PERCENT", MemberSharePercent: "12.345678",
		ValidFrom: day(t, "2026-01-01"), Priority: 100,
	}
	page := f.items(t, list, item)
	if len(page.Items) != 1 {
		t.Fatalf("stored %d price items, want 1", len(page.Items))
	}
	stored := page.Items[0]
	for _, tc := range []struct{ name, got, want string }{
		{"amount", stored.Amount, "12345678901.123457"},
		{"minAmount", stored.MinAmount, "0.000001"},
		{"maxAmount", stored.MaxAmount, "99999999999.999999"},
		{"memberSharePercent", stored.MemberSharePercent, "12.345678"},
	} {
		if tc.got != tc.want {
			t.Fatalf("%s round-tripped as %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestCreateVersionCopiesThePriceSheet(t *testing.T) {
	f := newFixture(t)
	contract := f.contract(t, "COPY")
	view := f.version(t, contract.ID, "2026-01-01", dayPtr(t, "2027-01-01"))

	packages, err := f.svc.ReplacePackages(context.Background(), f.makerRC(), view.Version.ID,
		[]domain.PackageInput{{
			Code: "SEANS10", Name: "10 seans",
			Lines: []domain.PackageLineInput{{ServiceDefinitionID: f.physio.String(), IncludedQuantity: "10"}},
		}}, view.Version.RowVersion)
	if err != nil {
		t.Fatalf("replace packages: %v", err)
	}
	reloaded, err := f.svc.GetVersion(context.Background(), f.makerRC(), view.Version.ID)
	if err != nil {
		t.Fatalf("reload version: %v", err)
	}
	list := f.priceList(t, reloaded, domain.PriceListInput{Code: "STANDART", Name: "Standart", Priority: 100})
	definitionPrice := fixedItem(t, "300")
	definitionPrice.ServiceDefinitionID = f.physio.String()
	packagePrice := fixedItem(t, "2500")
	packagePrice.PackageDefinitionID = packages.Items[0].ID.String()
	f.items(t, list, definitionPrice, packagePrice)

	source, err := f.svc.GetVersion(context.Background(), f.makerRC(), view.Version.ID)
	if err != nil {
		t.Fatalf("reload version: %v", err)
	}
	if _, err := f.svc.PutPaymentTerm(context.Background(), f.makerRC(), view.Version.ID,
		domain.PaymentTermInput{DueDays: 45, SettlementMethod: "BANK_TRANSFER", TaxBehaviour: "EXCLUSIVE", VatRate: "20"},
		source.Version.RowVersion); err != nil {
		t.Fatalf("write payment term: %v", err)
	}

	copied, err := f.svc.CreateVersion(context.Background(), f.makerRC(), contract.ID, application.NewVersionInput{
		CopyFromVersionID: &view.Version.ID, ValidFrom: dayPtr(t, "2027-01-01"),
	})
	if err != nil {
		t.Fatalf("copy version: %v", err)
	}
	if len(copied.PriceLists) != 1 || copied.PriceLists[0].ItemCount != 2 {
		t.Fatalf("copied price lists %+v, want one with two items", copied.PriceLists)
	}

	copiedPackages, err := f.svc.ListPackages(context.Background(), f.makerRC(), copied.Version.ID)
	if err != nil {
		t.Fatalf("list copied packages: %v", err)
	}
	if len(copiedPackages.Items) != 1 {
		t.Fatalf("copied %d packages, want 1", len(copiedPackages.Items))
	}
	if copiedPackages.Items[0].ID == packages.Items[0].ID {
		t.Fatal("the copy reused the source version's package row")
	}

	items, err := f.svc.ListPriceItems(context.Background(), f.makerRC(), copied.PriceLists[0].ID, "", 0)
	if err != nil {
		t.Fatalf("list copied price items: %v", err)
	}
	var packagePriced int
	for _, it := range items.Items {
		if it.PackageDefinitionID != nil {
			packagePriced++
			if *it.PackageDefinitionID != copiedPackages.Items[0].ID {
				t.Fatal("a copied price item still points at the source version's package")
			}
		}
	}
	if packagePriced != 1 {
		t.Fatalf("%d copied items price a package, want 1", packagePriced)
	}

	term, err := f.svc.GetPaymentTerm(context.Background(), f.makerRC(), copied.Version.ID)
	if err != nil {
		t.Fatalf("read copied payment term: %v", err)
	}
	if term.Term.DueDays != 45 || term.Term.VatRate != "20" {
		t.Fatalf("copied payment term %+v, want 45 days at 20%%", term.Term)
	}
}

func TestContractsAreTenantIsolated(t *testing.T) {
	f := newFixture(t)
	contract := f.contract(t, "ISOLATION")

	if _, err := f.svc.GetContract(context.Background(), f.rc(f.tenantB, f.maker), contract.ID); !errors.Is(err, application.ErrContractNotFound) {
		t.Fatalf("reading another tenant's contract: got %v, want ErrContractNotFound", err)
	}
	page, err := f.svc.ListContracts(context.Background(), f.rc(f.tenantB, f.maker), application.ListFilter{})
	if err != nil {
		t.Fatalf("list contracts in the other tenant: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("the other tenant sees %d contracts, want 0", len(page.Items))
	}
}

func TestQuotaSetKeepsTheConsumedCounter(t *testing.T) {
	f := newFixture(t)
	contract := f.contract(t, "QUOTA")
	view := f.version(t, contract.ID, "2026-01-01", nil)

	quotas, err := f.svc.ReplaceQuotas(context.Background(), f.makerRC(), view.Version.ID,
		[]domain.QuotaInput{{
			PeriodType: "YEAR", PeriodFrom: day(t, "2026-01-01"), PeriodTo: day(t, "2027-01-01"),
			Capacity: "100",
		}}, view.Version.RowVersion)
	if err != nil {
		t.Fatalf("replace quotas: %v", err)
	}
	if len(quotas.Items) != 1 || quotas.Items[0].Capacity != "100" {
		t.Fatalf("stored quotas %+v, want one of 100", quotas.Items)
	}

	// Authorization owns the consumed counter; a price sheet write must never reset it.
	f.h.AdminExec(`UPDATE contract.provider_quota SET consumed = 40 WHERE tenant_id = $1`, f.tenantA)

	current, err := f.svc.GetVersion(context.Background(), f.makerRC(), view.Version.ID)
	if err != nil {
		t.Fatalf("reload version: %v", err)
	}
	updated, err := f.svc.ReplaceQuotas(context.Background(), f.makerRC(), view.Version.ID,
		[]domain.QuotaInput{{
			PeriodType: "YEAR", PeriodFrom: day(t, "2026-01-01"), PeriodTo: day(t, "2027-01-01"),
			Capacity: "150",
		}}, current.Version.RowVersion)
	if err != nil {
		t.Fatalf("rewrite quotas: %v", err)
	}
	if len(updated.Items) != 1 {
		t.Fatalf("rewritten quotas %+v, want exactly one", updated.Items)
	}
	if updated.Items[0].ID != quotas.Items[0].ID {
		t.Fatal("rewriting a known scope replaced the quota row instead of updating it")
	}
	if updated.Items[0].Consumed != "40" || updated.Items[0].Capacity != "150" {
		t.Fatalf("rewritten quota %+v, want 40 consumed of 150", updated.Items[0])
	}
}
