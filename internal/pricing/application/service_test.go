package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/pricing/application"
	pricingpg "github.com/celikbros/kapsora/internal/pricing/infrastructure/postgres"
)

// The fixture seeds the whole chain a quote reads: a sponsored member with an entitlement
// account holding 300 TRY, a provider with one location, a catalog definition, and a
// published contract version pricing that definition at 500 TRY with a 20 % member share.
//
// The arithmetic that follows from it is the example of WP-I3-05 section 3: the contract
// says 500, the member's own share is 20 % of it (100), the plan would carry the other
// 400, the balance stops the payer at 300, and the member is left with 500 − 300 = 200.
const (
	entitlementCode = "PHYSIO"
	// serviceDate is a Monday inside both the plan version and the contract version.
	serviceDateText = "2026-06-15"
)

var serviceDate = time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)

type fixture struct {
	h   *dbtest.Harness
	svc *application.Service

	tenant     uuid.UUID
	actor      uuid.UUID
	providerOr uuid.UUID
	provider   uuid.UUID
	location   uuid.UUID
	person     uuid.UUID
	program    uuid.UUID
	definition uuid.UUID
	category   uuid.UUID
	priceList  uuid.UUID
	priceItem  uuid.UUID
	version    uuid.UUID
	enrollment uuid.UUID
	account    uuid.UUID
	ruleSet    uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	return newFixtureWithEntitlement(t, "MONEY", "300")
}

func newQuantityFixture(t *testing.T) *fixture {
	return newFixtureWithEntitlement(t, "SESSION", "20")
}

func newFixtureWithEntitlement(t *testing.T, unit, initial string) *fixture {
	t.Helper()
	h := dbtest.New(t)
	svc, err := application.New(application.Deps{
		Pool: h.App, Repo: pricingpg.New(), Audit: auditpg.New(),
		Now: func() time.Time { return time.Date(2026, 6, 15, 9, 30, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{h: h, svc: svc}
	f.seed(t, unit, initial)
	return f
}

func (f *fixture) seed(t *testing.T, unit, initial string) { //nolint:funlen // one linear fixture reads better whole
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

	f.tenant = h.CreateTenant("PRICING")
	f.actor = h.CreateActor("pricing-clerk", "Pricing Clerk")
	sponsor := h.CreateTenantOrganization(f.tenant, "Pricing Sponsor", "SPONSOR")
	payer := h.CreateTenantOrganization(f.tenant, "Pricing Payer", "PAYER")
	f.providerOr = h.CreateTenantOrganization(f.tenant, "Pricing Provider", "PROVIDER")

	scan(&f.provider, "provider profile", `
		INSERT INTO provider.provider_profile (tenant_id, tenant_organization_id, provider_type, status)
		VALUES ($1, $2, 'CLINIC', 'ACTIVE') RETURNING id`, f.tenant, f.providerOr)
	scan(&f.location, "location", `
		INSERT INTO provider.location (tenant_id, provider_profile_id, code, name)
		VALUES ($1, $2, 'MERKEZ', 'Merkez') RETURNING id`, f.tenant, f.provider)

	h.AdminExec(`INSERT INTO party.membership_type (tenant_id, code, display_name) VALUES ($1, 'MEMBER', 'Üye')`, f.tenant)
	scan(&f.person, "person", `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Asli', 'Yildizhan', 'asli yildizhan') RETURNING id`, f.tenant)
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
	var planID uuid.UUID
	scan(&planID, "plan", `
		INSERT INTO benefit.plan (tenant_id, program_id, code, name, status)
		VALUES ($1, $2, 'PLAN', 'Plan', 'ACTIVE') RETURNING id`, f.tenant, f.program)
	var planVersion uuid.UUID
	scan(&planVersion, "plan version", `
		INSERT INTO benefit.plan_version (tenant_id, plan_id, version_no, status, valid_period,
		                                  published_at, published_by)
		VALUES ($1, $2, 1, 'DRAFT', daterange('2026-01-01','2027-01-01','[)'), NULL, NULL)
		RETURNING id`, f.tenant, planID)
	var definitionID uuid.UUID
	scan(&definitionID, "entitlement definition", `
		INSERT INTO benefit.entitlement_definition (tenant_id, plan_version_id, code, name, unit_type,
		                                            currency_code, period_type, initial_quantity)
		VALUES ($1, $2, $3, $3, $4, CASE WHEN $4='MONEY' THEN 'TRY' ELSE NULL END, 'CALENDAR_YEAR', $5) RETURNING id`,
		f.tenant, planVersion, entitlementCode, unit, initial)
	scan(&f.enrollment, "enrollment", `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status, valid_period)
		VALUES ($1, $2, $3, 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, membership, planID)

	// The account is opened here rather than by the quote, which is the point: a quote
	// must not open one, because opening posts a GRANT movement onto the ledger.
	scan(&f.account, "entitlement account", `
		INSERT INTO benefit.entitlement_account (tenant_id, enrollment_id, entitlement_definition_id,
		                                         benefit_period, total_granted, available_quantity)
		VALUES ($1, $2, $3, daterange('2026-01-01','2027-01-01','[)'), $4, $4) RETURNING id`,
		f.tenant, f.enrollment, definitionID, initial)
	h.AdminExec(`
		INSERT INTO benefit.entitlement_ledger (tenant_id, entitlement_account_id, movement_type,
		                                        effective_at, delta_total, delta_available,
		                                        reference_type, reference_id, idempotency_key)
		VALUES ($1, $2, 'GRANT', clock_timestamp(), $4, $4, 'ENROLLMENT', $3, 'grant:seed')`,
		f.tenant, f.account, f.enrollment, initial)

	scan(&f.category, "service category", `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'HEALTH_ROOT', 'Sağlık', 'HEALTH') RETURNING id`, f.tenant)
	scan(&f.definition, "service definition", `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name,
		                                        fulfillment_mode, default_unit_type)
		VALUES ($1, $2, 'PHYSIO_SESSION', 'Fizyoterapi seansı', 'SESSION', 'SESSION') RETURNING id`,
		f.tenant, f.category)

	if unit != "MONEY" {
		h.AdminExec(`INSERT INTO benefit.service_entitlement_mapping
		    (tenant_id, plan_version_id, service_definition_id, entitlement_definition_id, unit_factor)
		    VALUES ($1, $2, $3, $4, 2)`, f.tenant, planVersion, f.definition, definitionID)
	}
	h.AdminExec(`UPDATE benefit.plan_version SET status='PUBLISHED', published_at=clock_timestamp(), published_by=$3 WHERE tenant_id=$1 AND id=$2`, f.tenant, planVersion, f.actor)

	var contractID uuid.UUID
	scan(&contractID, "contract", `
		INSERT INTO contract.contract (tenant_id, code, name, payer_organization_id,
		                               provider_profile_id, domain_code, status)
		VALUES ($1, 'HEALTH_2026', 'Sağlık 2026', $2, $3, 'HEALTH', 'ACTIVE') RETURNING id`,
		f.tenant, payer, f.provider)
	scan(&f.version, "contract version", `
		INSERT INTO contract.contract_version (tenant_id, contract_id, version_no, status,
		                                       valid_from, valid_to, currency_code,
		                                       configuration_hash, published_at, published_by)
		VALUES ($1, $2, 1, 'PUBLISHED', '2026-01-01', '2027-01-01', 'TRY', 'deadbeef',
		        clock_timestamp(), $3) RETURNING id`, f.tenant, contractID, f.actor)
	scan(&f.priceList, "price list", `
		INSERT INTO contract.price_list (tenant_id, contract_version_id, code, name)
		VALUES ($1, $2, 'STANDART', 'Standart liste') RETURNING id`, f.tenant, f.version)
	scan(&f.priceItem, "price item", `
		INSERT INTO contract.price_item (tenant_id, price_list_id, service_definition_id, unit_type,
		                                 pricing_method, amount, member_share_method,
		                                 member_share_percent, valid_from)
		VALUES ($1, $2, $3, 'SESSION', 'FIXED', 500, 'PERCENT', 20, '2026-01-01') RETURNING id`,
		f.tenant, f.priceList, f.definition)
}

// rc is a tenant-wide clerk holding pricing.quote.
func (f *fixture) rc() identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal:   identity.Principal{ActorID: f.actor},
		Permissions: map[string]struct{}{application.PermissionQuote: {}},
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

// request is the fixture's standard question: one session of the priced definition.
func (f *fixture) request() application.QuoteInput {
	return application.QuoteInput{
		PersonID: f.person, ProviderProfileID: f.provider, ServiceDate: serviceDate,
		Items: []application.QuoteItemInput{{
			ServiceDefinitionID: &f.definition, Quantity: benefitdomain.MustQuantity("1"),
		}},
		Context: map[string]any{"entitlementCode": entitlementCode},
	}
}

func (f *fixture) quote(t *testing.T, in application.QuoteInput) application.QuoteView {
	t.Helper()
	out, err := f.svc.CreateQuote(context.Background(), f.rc(), in)
	if err != nil {
		t.Fatalf("create quote: %v", err)
	}
	return out
}

// publishPriceRules puts one PRICE rule set version live over the service date. The
// condition is written against the variables the pricing module supplies.
func (f *fixture) publishPriceRules(t *testing.T, code, condition, actions string) uuid.UUID {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	if f.ruleSet == uuid.Nil {
		if err := f.h.Admin.QueryRow(ctx, `
			INSERT INTO rules.rule_set (tenant_id, code, name, domain_code, purpose, status)
			VALUES ($1, 'PRICE_RULES', 'Fiyat kuralları', 'HEALTH', 'PRICE', 'ACTIVE')
			RETURNING id`, f.tenant).Scan(&f.ruleSet); err != nil {
			t.Fatalf("seed price rule set: %v", err)
		}
	}
	var versionID uuid.UUID
	if err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO rules.rule_set_version (tenant_id, rule_set_id, version_no, status, valid_from,
		                                    input_schema, content_hash, published_at, published_by)
		VALUES ($1, $2, 1, 'PUBLISHED', '2026-01-01',
		        '{"serviceDate":"timestamp","contractAmount":"string","entitlementCode":"string","eligible":"bool","formulaKey":"string"}'::jsonb,
		        'deadbeef', clock_timestamp(), $3)
		RETURNING id`, f.tenant, f.ruleSet, f.actor).Scan(&versionID); err != nil {
		t.Fatalf("seed price rule version: %v", err)
	}
	f.h.AdminExec(`
		INSERT INTO rules.rule (tenant_id, rule_set_version_id, code, name, priority, condition,
		                        actions, explanation_code)
		VALUES ($1, $2, $3, $3, 10, $4, $5::jsonb, 'PRICE_RULE')`,
		f.tenant, versionID, code, condition, actions)
	return versionID
}

// ledgerState is every fact about the entitlement ledger a quote must leave alone.
type ledgerState struct {
	entries      int
	accounts     int
	reservations int
	balances     []string
}

func (f *fixture) ledgerState(t *testing.T) ledgerState {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	out := ledgerState{}
	row := f.h.Admin.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM benefit.entitlement_ledger WHERE tenant_id = $1),
		       (SELECT count(*) FROM benefit.entitlement_account WHERE tenant_id = $1),
		       (SELECT count(*) FROM benefit.entitlement_reservation WHERE tenant_id = $1)`, f.tenant)
	if err := row.Scan(&out.entries, &out.accounts, &out.reservations); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	rows, err := f.h.Admin.Query(ctx, `
		SELECT id::text, total_granted::text, available_quantity::text, reserved_quantity::text,
		       consumed_quantity::text, expired_quantity::text, row_version::text
		  FROM benefit.entitlement_account WHERE tenant_id = $1 ORDER BY id`, f.tenant)
	if err != nil {
		t.Fatalf("read balances: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, total, available, reserved, consumed, expired, version string
		if err := rows.Scan(&id, &total, &available, &reserved, &consumed, &expired, &version); err != nil {
			t.Fatalf("scan balance: %v", err)
		}
		out.balances = append(out.balances,
			strings.Join([]string{id, total, available, reserved, consumed, expired, version}, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read balances: %v", err)
	}
	return out
}

// TestQuoteSplitsTheContractAmount is the worked example of the work package: 500 TRY at
// 20 % member share against a 300 TRY balance is 300 for the payer and 200 for the member,
// exactly, as decimal strings.
func TestQuoteSplitsTheContractAmount(t *testing.T) {
	f := newFixture(t)
	quote := f.quote(t, f.request())

	if quote.CurrencyCode != "TRY" {
		t.Fatalf("currency = %q, want TRY", quote.CurrencyCode)
	}
	if quote.ContractAmount != "500" || quote.CoveredAmount != "400" {
		t.Fatalf("contract/covered = %s/%s, want 500/400", quote.ContractAmount, quote.CoveredAmount)
	}
	if quote.PayerAmount != "300" || quote.MemberAmount != "200" {
		t.Fatalf("payer/member = %s/%s, want 300/200", quote.PayerAmount, quote.MemberAmount)
	}
	// The plan would have carried 400 and the balance stopped it at 300, so the line is
	// partial rather than quoted, and the reason is on the line.
	if quote.Outcome != "PARTIAL" {
		t.Fatalf("outcome = %s, want PARTIAL", quote.Outcome)
	}
	if len(quote.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(quote.Items))
	}
	item := quote.Items[0]
	if item.PriceItemID == nil || *item.PriceItemID != f.priceItem {
		t.Fatalf("line does not name the winning price item: %+v", item.PriceItemID)
	}
	if !hasExplanation(item.Explanations, "MEMBER_SHARE_APPLIED") ||
		!hasExplanation(item.Explanations, "BALANCE_INSUFFICIENT") {
		t.Fatalf("explanations do not say why: %+v", item.Explanations)
	}
	if quote.ContractVersionID == nil || *quote.ContractVersionID != f.version {
		t.Fatal("the quote does not name the contract version behind it")
	}
	if quote.PlanVersionID == nil {
		t.Fatal("the quote does not name the plan version behind it")
	}
	if quote.Disclaimer == "" || !strings.Contains(quote.Disclaimer, "teklif") {
		t.Fatalf("disclaimer missing: %q", quote.Disclaimer)
	}
}

// TestQuoteTouchesNothingInTheLedger is the single most important property of the package:
// a quote is an answer, not a promise. Nothing is reserved, no balance moves and no
// account is opened, whether the person had one already or not.
func TestQuoteTouchesNothingInTheLedger(t *testing.T) {
	f := newFixture(t)
	before := f.ledgerState(t)

	f.quote(t, f.request())
	// A second person with an enrollment and no account at all is the case that would
	// betray a lazy EnsureAccounts hiding inside the quote path.
	f.quote(t, f.request())

	after := f.ledgerState(t)
	if before.entries != after.entries {
		t.Fatalf("ledger rows %d -> %d: a quote moved the ledger", before.entries, after.entries)
	}
	if before.accounts != after.accounts {
		t.Fatalf("accounts %d -> %d: a quote opened an entitlement account", before.accounts, after.accounts)
	}
	if before.reservations != after.reservations {
		t.Fatalf("reservations %d -> %d: a quote reserved a balance", before.reservations, after.reservations)
	}
	if strings.Join(before.balances, ";") != strings.Join(after.balances, ";") {
		t.Fatalf("account balances changed:\nbefore %v\nafter  %v", before.balances, after.balances)
	}
}

// TestQuoteWithNoPriceIsReviewRequired: an ambiguous or missing price must not produce a
// number. Two equally specific prices are a configuration error, and guessing between them
// is a silent financial error nothing anywhere reports.
func TestQuoteWithNoPriceIsReviewRequired(t *testing.T) {
	f := newFixture(t)

	// A second, equally specific price on the same definition ties with the first.
	f.h.AdminExec(`
		INSERT INTO contract.price_item (tenant_id, price_list_id, service_definition_id, unit_type,
		                                 pricing_method, amount, valid_from)
		VALUES ($1, $2, $3, 'SESSION', 'FIXED', 700, '2026-01-01')`,
		f.tenant, f.priceList, f.definition)

	quote := f.quote(t, f.request())
	if quote.Outcome != "REVIEW_REQUIRED" {
		t.Fatalf("outcome = %s, want REVIEW_REQUIRED", quote.Outcome)
	}
	if quote.PayerAmount != "0" || quote.MemberAmount != "0" {
		t.Fatalf("a quote nobody can act on carries figures: payer %s member %s",
			quote.PayerAmount, quote.MemberAmount)
	}
	if !hasExplanation(quote.Items[0].Explanations, "PRICE_AMBIGUOUS") {
		t.Fatalf("the line does not say the price was ambiguous: %+v", quote.Items[0].Explanations)
	}
}

// TestQuoteWithNoContractedPriceIsReviewRequired: no candidate at all is the other half of
// the same rule.
func TestQuoteWithNoContractedPriceIsReviewRequired(t *testing.T) {
	f := newFixture(t)
	f.h.AdminExec(`DELETE FROM contract.price_item WHERE tenant_id = $1`, f.tenant)

	quote := f.quote(t, f.request())
	if quote.Outcome != "REVIEW_REQUIRED" {
		t.Fatalf("outcome = %s, want REVIEW_REQUIRED", quote.Outcome)
	}
	if !hasExplanation(quote.Items[0].Explanations, "PRICE_NOT_FOUND") {
		t.Fatalf("the line does not say the price was missing: %+v", quote.Items[0].Explanations)
	}
}

// TestPriceRuleLimitCapsWhatThePlanCarries: SET_LIMIT is the rule action a plan's own
// ceiling is written as, and the covered amount has to obey it with the rule code named.
func TestPriceRuleLimitCapsWhatThePlanCarries(t *testing.T) {
	f := newFixture(t)
	versionID := f.publishPriceRules(t, "PHYSIO_CAP", `entitlementCode == "PHYSIO"`,
		`[{"type":"SET_LIMIT","payload":{"amount":"250"}}]`)

	quote := f.quote(t, f.request())
	if quote.CoveredAmount != "250" {
		t.Fatalf("covered = %s, want 250 after the limit", quote.CoveredAmount)
	}
	// The plan carries 250 and the balance is 300, so the payer pays all of it and the
	// member is left with the rest of the contract amount.
	if quote.PayerAmount != "250" || quote.MemberAmount != "250" {
		t.Fatalf("payer/member = %s/%s, want 250/250", quote.PayerAmount, quote.MemberAmount)
	}
	if !hasExplanationFrom(quote.Items[0].Explanations, "LIMIT_APPLIED", "PHYSIO_CAP") {
		t.Fatalf("the limit does not name the rule that applied it: %+v", quote.Items[0].Explanations)
	}
	if len(quote.RuleSetVersionIDs) != 1 || quote.RuleSetVersionIDs[0] != versionID {
		t.Fatalf("ruleSetVersionIds = %v, want [%s]", quote.RuleSetVersionIDs, versionID)
	}
}

// TestPriceRuleRequiringReviewStopsTheQuote: a REQUIRE_* action means a person has to look
// at the line, and a quote with such a line carries no member figure.
func TestPriceRuleRequiringReviewStopsTheQuote(t *testing.T) {
	f := newFixture(t)
	f.publishPriceRules(t, "PHYSIO_PREAUTH", `entitlementCode == "PHYSIO"`,
		`[{"type":"REQUIRE_PREAUTH","payload":{"authorizationTypeCode":"PHYSIO"}}]`)

	quote := f.quote(t, f.request())
	if quote.Outcome != "REVIEW_REQUIRED" {
		t.Fatalf("outcome = %s, want REVIEW_REQUIRED", quote.Outcome)
	}
	if quote.PayerAmount != "0" || quote.MemberAmount != "0" {
		t.Fatalf("payer/member = %s/%s, want 0/0", quote.PayerAmount, quote.MemberAmount)
	}
	if !hasExplanationFrom(quote.Items[0].Explanations, "REVIEW_REQUESTED_BY_RULE", "PHYSIO_PREAUTH") {
		t.Fatalf("the line does not name the rule that stopped it: %+v", quote.Items[0].Explanations)
	}
}

// TestQuoteForAnIneligiblePersonStillPrices: a member the plan does not cover may still
// choose to pay privately, so the contract amount is quoted and the coverage is zero.
func TestQuoteForAnIneligiblePersonStillPrices(t *testing.T) {
	f := newFixture(t)
	f.h.AdminExec(`UPDATE benefit.enrollment SET status = 'SUSPENDED' WHERE tenant_id = $1`, f.tenant)

	quote := f.quote(t, f.request())
	if quote.Outcome != "NOT_ELIGIBLE" {
		t.Fatalf("outcome = %s, want NOT_ELIGIBLE", quote.Outcome)
	}
	if quote.ContractAmount != "500" {
		t.Fatalf("contract = %s, want 500: an ineligible member is still told the price",
			quote.ContractAmount)
	}
	if quote.CoveredAmount != "0" || quote.PayerAmount != "0" || quote.MemberAmount != "500" {
		t.Fatalf("covered/payer/member = %s/%s/%s, want 0/0/500",
			quote.CoveredAmount, quote.PayerAmount, quote.MemberAmount)
	}
}

// TestQuoteReplaysUnderOneIdempotencyKey: a retried submit returns the quote that was
// already given, and the same key with a different question is refused rather than
// answering somebody else's number.
func TestQuoteReplaysUnderOneIdempotencyKey(t *testing.T) {
	f := newFixture(t)
	in := f.request()
	in.IdempotencyKey = "pricing-quote-key-0001"

	first := f.quote(t, in)
	second := f.quote(t, in)
	if first.ID != second.ID {
		t.Fatalf("replay produced a second quote: %s then %s", first.ID, second.ID)
	}

	var stored int
	ctx, cancel := f.h.Ctx()
	defer cancel()
	if err := f.h.Admin.QueryRow(ctx,
		`SELECT count(*) FROM contract.price_quote WHERE tenant_id = $1`, f.tenant).Scan(&stored); err != nil {
		t.Fatalf("count quotes: %v", err)
	}
	if stored != 1 {
		t.Fatalf("stored quotes = %d, want 1", stored)
	}

	different := in
	different.Items = []application.QuoteItemInput{{
		ServiceDefinitionID: &f.definition, Quantity: benefitdomain.MustQuantity("2"),
	}}
	if _, err := f.svc.CreateQuote(context.Background(), f.rc(), different); !errors.Is(err, application.ErrIdempotencyKeyReuse) {
		t.Fatalf("reused key with a different question: %v", err)
	}
}

// TestExpiredQuoteIsReportedNotHidden: a member who was given a number is owed the reason
// it no longer holds, so an expired quote reads back and says it expired.
func TestExpiredQuoteIsReportedNotHidden(t *testing.T) {
	f := newFixture(t)
	quote := f.quote(t, f.request())
	if quote.Expired {
		t.Fatal("a fresh quote reports itself expired")
	}
	// The default lifetime is 72 hours, so nothing shorter than that has passed yet.
	if want := quote.QuotedAt.Add(application.DefaultQuoteTTL); !quote.ExpiresAt.Equal(want) {
		t.Fatalf("expiresAt = %s, want %s", quote.ExpiresAt, want)
	}

	late, err := application.New(application.Deps{
		Pool: f.h.App, Repo: pricingpg.New(), Audit: auditpg.New(),
		Now: func() time.Time { return quote.ExpiresAt.Add(time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	read, err := late.GetQuote(context.Background(), f.rc(), quote.ID)
	if err != nil {
		t.Fatalf("read an expired quote: %v", err)
	}
	if !read.Expired {
		t.Fatal("an expired quote did not say so")
	}
	if read.PayerAmount != quote.PayerAmount {
		t.Fatal("an expired quote lost its figures")
	}
}

// TestQuoteTTLComesFromTheTenantSetting.
func TestQuoteTTLComesFromTheTenantSetting(t *testing.T) {
	f := newFixture(t)
	f.h.AdminExec(`
		INSERT INTO platform.tenant_setting (tenant_id, setting_key, value_json)
		VALUES ($1, 'pricing.quote_ttl_hours', '6'::jsonb)`, f.tenant)

	quote := f.quote(t, f.request())
	if want := quote.QuotedAt.Add(6 * time.Hour); !quote.ExpiresAt.Equal(want) {
		t.Fatalf("expiresAt = %s, want %s", quote.ExpiresAt, want)
	}
}

// TestQuoteReadsBackFromTheStoredColumns: the stored row is the answer, not a cache of it,
// so a read reproduces every figure and every explanation of the original.
func TestQuoteReadsBackFromTheStoredColumns(t *testing.T) {
	f := newFixture(t)
	created := f.quote(t, f.request())

	read, err := f.svc.GetQuote(context.Background(), f.rc(), created.ID)
	if err != nil {
		t.Fatalf("get quote: %v", err)
	}
	if read.PayerAmount != created.PayerAmount || read.MemberAmount != created.MemberAmount ||
		read.ContractAmount != created.ContractAmount || read.Outcome != created.Outcome {
		t.Fatalf("read differs from the quote given:\n%+v\n%+v", created, read)
	}
	if len(read.Items) != 1 || len(read.Items[0].Explanations) != len(created.Items[0].Explanations) {
		t.Fatalf("read lost the line explanations: %+v", read.Items)
	}

	if _, err := f.svc.GetQuote(context.Background(), f.rc(), uuid.New()); !errors.Is(err, application.ErrQuoteNotFound) {
		t.Fatalf("unknown quote: %v", err)
	}
}

// TestSnapshotsCarryNoIdentifierOrName reads the stored JSON back out of the columns and
// scans it. The whole reason those columns are filtered is that an audit trail which
// itself needs protecting is an audit trail nobody will be allowed to read.
func TestSnapshotsCarryNoIdentifierOrName(t *testing.T) {
	f := newFixture(t)
	in := f.request()
	// Everything a caller might smuggle through the free-form context object.
	in.Context = map[string]any{
		"entitlementCode": entitlementCode,
		"domain":          "HEALTH",
		"personName":      "Asli Yildizhan",
		"tckn":            "10000000146",
		"note":            "hastanın kendi ifadesi",
	}
	quote := f.quote(t, in)

	ctx, cancel := f.h.Ctx()
	defer cancel()
	var request, result []byte
	if err := f.h.Admin.QueryRow(ctx, `
		SELECT request_snapshot, result_snapshot FROM contract.price_quote
		 WHERE tenant_id = $1 AND id = $2`, f.tenant, quote.ID).Scan(&request, &result); err != nil {
		t.Fatalf("read snapshots: %v", err)
	}

	for name, raw := range map[string][]byte{"request": request, "result": result} {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s snapshot is not an object: %v", name, err)
		}
		for _, banned := range []string{"Asli", "Yildizhan", "10000000146", "hastanın", "personName", "tckn", "note"} {
			if strings.Contains(string(raw), banned) {
				t.Fatalf("%s snapshot contains %q: %s", name, banned, raw)
			}
		}
	}
	// What must survive: the ids, the day, the codes and the amounts.
	for _, wanted := range []string{f.person.String(), serviceDateText, entitlementCode, "500"} {
		if !strings.Contains(string(request)+string(result), wanted) {
			t.Fatalf("snapshots lost %q:\nrequest %s\nresult %s", wanted, request, result)
		}
	}
}

// TestProviderScopedActorSeesOnlyItsOwnQuotes: a counter clerk at one provider must not be
// able to find out what another provider was quoted, and must not be able to ask for one.
func TestProviderScopedActorSeesOnlyItsOwnQuotes(t *testing.T) {
	f := newFixture(t)
	quote := f.quote(t, f.request())

	own := f.providerRC(f.providerOr)
	if _, err := f.svc.GetQuote(context.Background(), own, quote.ID); err != nil {
		t.Fatalf("the provider that was quoted cannot read it: %v", err)
	}

	stranger := f.providerRC(uuid.New())
	if _, err := f.svc.GetQuote(context.Background(), stranger, quote.ID); !errors.Is(err, application.ErrQuoteNotFound) {
		t.Fatalf("another provider read the quote: %v", err)
	}
	if _, err := f.svc.CreateQuote(context.Background(), stranger, f.request()); !errors.Is(err, application.ErrProviderScope) {
		t.Fatalf("another provider quoted for this one: %v", err)
	}
}

// TestQuoteWritesAnAccessAuditEvent: reading what a named person's care would cost is an
// access to their data, and it leaves a row saying who looked.
func TestQuoteWritesAnAccessAuditEvent(t *testing.T) {
	f := newFixture(t)
	in := f.request()
	in.Context = map[string]any{"entitlementCode": entitlementCode, "domain": "HEALTH"}
	quote := f.quote(t, in)

	ctx, cancel := f.h.Ctx()
	defer cancel()
	var classification, purpose, resource string
	if err := f.h.Admin.QueryRow(ctx, `
		SELECT data_classification, purpose_code, resource_type
		  FROM audit.access_event
		 WHERE tenant_id = $1 AND resource_id = $2`, f.tenant, quote.ID).Scan(
		&classification, &purpose, &resource); err != nil {
		t.Fatalf("read access event: %v", err)
	}
	if classification != "HEALTH" {
		t.Fatalf("classification = %s, want HEALTH for a clinical context", classification)
	}
	if purpose != application.PurposeQuote || resource != application.ResourceQuote {
		t.Fatalf("purpose/resource = %s/%s", purpose, resource)
	}
}

// TestQuoteRefusesATargetThisProviderDoesNotHave.
func TestQuoteRefusesATargetThisProviderDoesNotHave(t *testing.T) {
	f := newFixture(t)

	unknownProvider := f.request()
	unknownProvider.ProviderProfileID = uuid.New()
	if _, err := f.svc.CreateQuote(context.Background(), f.rc(), unknownProvider); !errors.Is(err, application.ErrProviderNotFound) {
		t.Fatalf("unknown provider: %v", err)
	}

	foreignLocation := f.request()
	other := uuid.New()
	foreignLocation.LocationID = &other
	if _, err := f.svc.CreateQuote(context.Background(), f.rc(), foreignLocation); !errors.Is(err, application.ErrLocationNotFound) {
		t.Fatalf("foreign location: %v", err)
	}

	foreignEvaluation := f.request()
	foreignEvaluation.EligibilityEvaluationID = &other
	if _, err := f.svc.CreateQuote(context.Background(), f.rc(), foreignEvaluation); !errors.Is(err, application.ErrEvaluationNotFound) {
		t.Fatalf("foreign eligibility evaluation: %v", err)
	}
}

// TestQuoteIsTenantIsolated: another tenant's quote is simply not there.
func TestQuoteIsTenantIsolated(t *testing.T) {
	f := newFixture(t)
	quote := f.quote(t, f.request())

	other := f.rc()
	other.TenantID = f.h.CreateTenant("PRICING_OTHER")
	if _, err := f.svc.GetQuote(context.Background(), other, quote.ID); !errors.Is(err, application.ErrQuoteNotFound) {
		t.Fatalf("another tenant read the quote: %v", err)
	}
}

// TestQuoteValidatesTheRequest.
func TestQuoteValidatesTheRequest(t *testing.T) {
	f := newFixture(t)
	empty := f.request()
	empty.Items = nil
	if err := errOf(f.svc.CreateQuote(context.Background(), f.rc(), empty)); err == nil {
		t.Fatal("a quote with no lines was accepted")
	}

	both := f.request()
	pkg := uuid.New()
	both.Items[0].PackageDefinitionID = &pkg
	if err := errOf(f.svc.CreateQuote(context.Background(), f.rc(), both)); err == nil {
		t.Fatal("a line naming both a definition and a package was accepted")
	}

	zero := f.request()
	zero.Items[0].Quantity = benefitdomain.ZeroQuantity()
	if err := errOf(f.svc.CreateQuote(context.Background(), f.rc(), zero)); err == nil {
		t.Fatal("a line with no quantity was accepted")
	}
}

// TestQuoteRunsInOneTransactionAtRepeatableRead proves the isolation level the whole
// explanation depends on: everything the quote names was read from one point in time.
func TestQuoteRunsInOneTransactionAtRepeatableRead(t *testing.T) {
	f := newFixture(t)
	// A REPEATABLE READ transaction sees a consistent snapshot, so a price written by
	// another connection after the quote started must not reach it. Writing the second,
	// tying price after the quote has been given leaves the stored answer intact.
	quote := f.quote(t, f.request())
	f.h.AdminExec(`
		INSERT INTO contract.price_item (tenant_id, price_list_id, service_definition_id, unit_type,
		                                 pricing_method, amount, valid_from)
		VALUES ($1, $2, $3, 'SESSION', 'FIXED', 900, '2026-01-01')`,
		f.tenant, f.priceList, f.definition)

	read, err := f.svc.GetQuote(context.Background(), f.rc(), quote.ID)
	if err != nil {
		t.Fatalf("get quote: %v", err)
	}
	if read.ContractAmount != "500" {
		t.Fatalf("a stored quote changed when the price list did: %s", read.ContractAmount)
	}
}

func hasExplanation(items []application.ExplanationView, code string) bool {
	for _, e := range items {
		if e.Code == code {
			return true
		}
	}
	return false
}

func hasExplanationFrom(items []application.ExplanationView, code, source string) bool {
	for _, e := range items {
		if e.Code == code && e.Source != nil && *e.Source == source {
			return true
		}
	}
	return false
}

func errOf(_ application.QuoteView, err error) error { return err }

// Two lines drawing on the same entitlement account share its balance. Quoting each of
// them the whole balance would tell a member they owe nothing for twice the services the
// account can actually carry.
func TestTwoLinesOnOneAccountShareTheBalance(t *testing.T) {
	f := newFixture(t)
	before := f.ledgerState(t)

	in := f.request()
	in.Items = append(in.Items, application.QuoteItemInput{
		ServiceDefinitionID: &f.definition, Quantity: benefitdomain.MustQuantity("1"),
	})
	quote := f.quote(t, in)

	if len(quote.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(quote.Items))
	}
	first := benefitdomain.MustQuantity(quote.Items[0].PayerAmount)
	second := benefitdomain.MustQuantity(quote.Items[1].PayerAmount)
	if first.Add(second).Cmp(benefitdomain.MustQuantity("300")) > 0 {
		t.Fatalf("the two lines were quoted %s and %s of cover against a 300 balance", first, second)
	}
	if second.Cmp(first) > 0 {
		t.Fatalf("the second line drew more than the first: %s then %s", first, second)
	}
	// And the ledger is still untouched, as it must be for every quote.
	after := f.ledgerState(t)
	if after.entries != before.entries || after.accounts != before.accounts ||
		after.reservations != before.reservations ||
		strings.Join(after.balances, ";") != strings.Join(before.balances, ";") {
		t.Fatalf("a quote moved the ledger:\nbefore %+v\nafter  %+v", before, after)
	}
}
