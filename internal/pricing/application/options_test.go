package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/pricing/application"
)

func TestPricingOptionsScopePagingAndReadOnly(t *testing.T) {
	f := newFixture(t)
	otherOrg := f.h.CreateTenantOrganization(f.tenant, "Second Provider", "PROVIDER")
	suspendedOrg := f.h.CreateTenantOrganization(f.tenant, "Suspended Provider", "PROVIDER")
	foreignTenant := f.h.CreateTenant("OPTIONS_FOREIGN")
	foreignOrg := f.h.CreateTenantOrganization(foreignTenant, "Foreign Provider", "PROVIDER")
	var secondProvider, suspended, secondService uuid.UUID
	ctx, cancel := f.h.Ctx()
	defer cancel()
	for _, row := range []struct {
		dst  *uuid.UUID
		sql  string
		args []any
	}{
		{&secondProvider, `INSERT INTO provider.provider_profile (tenant_id, tenant_organization_id, provider_type, status) VALUES ($1,$2,'CLINIC','ACTIVE') RETURNING id`, []any{f.tenant, otherOrg}},
		{&suspended, `INSERT INTO provider.provider_profile (tenant_id, tenant_organization_id, provider_type, status) VALUES ($1,$2,'CLINIC','SUSPENDED') RETURNING id`, []any{f.tenant, suspendedOrg}},
		{&secondService, `INSERT INTO catalog.service_definition (tenant_id, category_id, code, name, fulfillment_mode, default_unit_type) VALUES ($1,$2,'OTHER_SERVICE','Other service','SESSION','SESSION') RETURNING id`, []any{f.tenant, f.category}},
	} {
		if err := f.h.Admin.QueryRow(ctx, row.sql, row.args...).Scan(row.dst); err != nil {
			t.Fatal(err)
		}
	}
	f.h.AdminExec(`INSERT INTO provider.provider_profile (tenant_id, tenant_organization_id, provider_type, status) VALUES ($1,$2,'CLINIC','ACTIVE')`, foreignTenant, foreignOrg)
	var foreignCategory uuid.UUID
	if err := f.h.Admin.QueryRow(ctx, `INSERT INTO catalog.service_category (tenant_id, code, name, domain_code) VALUES ($1,'FOREIGN','Foreign','HEALTH') RETURNING id`, foreignTenant).Scan(&foreignCategory); err != nil {
		t.Fatal(err)
	}
	f.h.AdminExec(`INSERT INTO catalog.service_definition (tenant_id, category_id, code, name, fulfillment_mode, default_unit_type) VALUES ($1,$2,'FOREIGN_SERVICE','Foreign service','SESSION','SESSION')`, foreignTenant, foreignCategory)
	f.h.AdminExec(`INSERT INTO catalog.service_definition (tenant_id, category_id, code, name, fulfillment_mode, default_unit_type, active) VALUES ($1,$2,'INACTIVE_SERVICE','Inactive service','SESSION','SESSION',false)`, f.tenant, f.category)
	before := countPricingSideEffects(t, f)
	providers, err := f.svc.ListProviderOptions(ctx, f.rc(), application.OptionFilter{Limit: 1})
	if err != nil || len(providers.Items) != 1 || providers.NextCursor == nil {
		t.Fatalf("first provider page: %+v %v", providers, err)
	}
	next, err := f.svc.ListProviderOptions(ctx, f.rc(), application.OptionFilter{Limit: 1, Cursor: *providers.NextCursor})
	if err != nil || len(next.Items) != 1 || next.NextCursor != nil {
		t.Fatalf("second provider page: %+v %v", next, err)
	}
	ids := map[uuid.UUID]bool{providers.Items[0].ProviderProfileID: true, next.Items[0].ProviderProfileID: true}
	if !ids[f.provider] || !ids[secondProvider] || ids[suspended] {
		t.Fatalf("provider ids: %v", ids)
	}
	for _, item := range append(providers.Items, next.Items...) {
		if item.OrganizationName == "" {
			t.Fatalf("missing organization name: %+v", item)
		}
	}
	services, err := f.svc.ListServiceOptions(ctx, f.rc(), application.OptionFilter{Limit: 1})
	if err != nil || len(services.Items) != 1 || services.NextCursor == nil {
		t.Fatalf("first service page: %+v %v", services, err)
	}
	serviceNext, err := f.svc.ListServiceOptions(ctx, f.rc(), application.OptionFilter{Limit: 1, Cursor: *services.NextCursor})
	if err != nil || len(serviceNext.Items) != 1 || serviceNext.NextCursor != nil {
		t.Fatalf("second service page: %+v %v", serviceNext, err)
	}
	serviceIDs := map[uuid.UUID]bool{services.Items[0].ServiceDefinitionID: true, serviceNext.Items[0].ServiceDefinitionID: true}
	if !serviceIDs[f.definition] || !serviceIDs[secondService] {
		t.Fatalf("service ids: %v", serviceIDs)
	}
	for _, item := range append(services.Items, serviceNext.Items...) {
		if item.Code == "" || item.Name == "" {
			t.Fatalf("missing service label: %+v", item)
		}
	}
	scoped, err := f.svc.ListProviderOptions(ctx, f.providerRC(f.providerOr), application.OptionFilter{})
	if err != nil || len(scoped.Items) != 1 || scoped.Items[0].ProviderProfileID != f.provider {
		t.Fatalf("scoped providers: %+v %v", scoped, err)
	}
	mixed := f.providerRC(f.providerOr)
	mixed.Scopes = append(mixed.Scopes, identity.Scope{Type: application.ScopeOrganization, ID: uuid.NullUUID{}})
	mixedPage, err := f.svc.ListProviderOptions(ctx, mixed, application.OptionFilter{})
	if err != nil || len(mixedPage.Items) != 1 || mixedPage.Items[0].ProviderProfileID != f.provider {
		t.Fatalf("mixed matching scopes: %+v %v", mixedPage, err)
	}
	invalid := f.rc()
	invalid.Scopes = []identity.Scope{{Type: application.ScopeOrganization, ID: uuid.NullUUID{}}}
	empty, err := f.svc.ListProviderOptions(ctx, invalid, application.OptionFilter{})
	if err != nil || len(empty.Items) != 0 {
		t.Fatalf("invalid scope providers: %+v %v", empty, err)
	}
	if after := countPricingSideEffects(t, f); after != before {
		t.Fatalf("lookup changed quote/ledger/outbox counts: before=%v after=%v", before, after)
	}
}

func TestPricingOptionsValidateAndEscape(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, filter := range []application.OptionFilter{{Query: "x"}, {Limit: 101}, {Cursor: "bad"}} {
		if _, err := f.svc.ListProviderOptions(ctx, f.rc(), filter); err == nil {
			t.Fatalf("provider accepted %+v", filter)
		}
		if _, err := f.svc.ListServiceOptions(ctx, f.rc(), filter); err == nil {
			t.Fatalf("service accepted %+v", filter)
		}
	}
	providers, err := f.svc.ListProviderOptions(ctx, f.rc(), application.OptionFilter{Query: "%%"})
	if err != nil || len(providers.Items) != 0 {
		t.Fatalf("provider wildcard escaped: %+v %v", providers, err)
	}
	services, err := f.svc.ListServiceOptions(ctx, f.rc(), application.OptionFilter{Query: "__"})
	if err != nil || len(services.Items) != 0 {
		t.Fatalf("service wildcard escaped: %+v %v", services, err)
	}
}

func TestMalformedPricingOrganizationScopesFailClosedForQuotes(t *testing.T) {
	f := newFixture(t)
	quote := f.quote(t, f.request())
	for _, scopes := range [][]identity.Scope{
		{{Type: application.ScopeOrganization, ID: uuid.NullUUID{}}},
		{{Type: application.ScopeOrganization, ID: uuid.NullUUID{UUID: uuid.Nil, Valid: true}}},
		{{Type: application.ScopeOrganization, ID: uuid.NullUUID{}}, {Type: application.ScopeOrganization, ID: uuid.NullUUID{UUID: uuid.New(), Valid: true}}},
	} {
		rc := f.rc()
		rc.Scopes = scopes
		if _, err := f.svc.CreateQuote(context.Background(), rc, f.request()); !errors.Is(err, application.ErrProviderScope) {
			t.Fatalf("create with scopes %+v: %v", scopes, err)
		}
		if _, err := f.svc.GetQuote(context.Background(), rc, quote.ID); !errors.Is(err, application.ErrQuoteNotFound) {
			t.Fatalf("get with scopes %+v: %v", scopes, err)
		}
	}
	rc := f.rc()
	rc.Scopes = []identity.Scope{{Type: application.ScopeOrganization, ID: uuid.NullUUID{}}, {Type: application.ScopeOrganization, ID: uuid.NullUUID{UUID: f.providerOr, Valid: true}}}
	if _, err := f.svc.GetQuote(context.Background(), rc, quote.ID); err != nil {
		t.Fatalf("valid matching scope with malformed grant: %v", err)
	}
}

type pricingSideEffects struct {
	counts         [5]int
	accountBalance string
	accountVersion int64
}

func countPricingSideEffects(t *testing.T, f *fixture) pricingSideEffects {
	t.Helper()
	var out pricingSideEffects
	ctx, cancel := f.h.Ctx()
	defer cancel()
	for i, table := range []string{"contract.price_quote", "benefit.entitlement_ledger", "system.outbox_event", "benefit.entitlement_account", "benefit.eligibility_evaluation"} {
		if err := f.h.Admin.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE tenant_id=$1", f.tenant).Scan(&out.counts[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.h.Admin.QueryRow(ctx, `SELECT available_quantity::text, row_version FROM benefit.entitlement_account WHERE tenant_id=$1 AND id=$2`, f.tenant, f.account).Scan(&out.accountBalance, &out.accountVersion); err != nil {
		t.Fatal(err)
	}
	return out
}
