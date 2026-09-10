// The reporting use cases, driven against a real database with real rows: real invoices, real
// icmals, real settlements with real payment records, and the real document store behind every
// export file.
//
// Nothing below stubs a figure. Every property this work package asks for is a property of what
// PostgreSQL computes over those rows — a fake repository would prove that a fake added up, and
// the one thing this package must never get wrong is exactly that its totals are the database's
// and not Go's.
//
// The one thing that is a double is the object store, and it is the in-memory implementation of
// the same port MinIO implements. That is deliberate and it is what lets a test read the bytes of
// a rendered export and check that the watermark is on every row of it.
package application_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	documentapp "github.com/celikbros/kapsora/internal/document/application"
	documentpg "github.com/celikbros/kapsora/internal/document/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/objectstore"
	"github.com/celikbros/kapsora/internal/report/application"
	reportgw "github.com/celikbros/kapsora/internal/report/infrastructure/gateway"
	reportpg "github.com/celikbros/kapsora/internal/report/infrastructure/postgres"
)

// fixtureNow is where the fixture's clock is pinned. Every period, due date and TTL below is
// expressed against it, so nothing in this file depends on when the tests run.
var fixtureNow = time.Date(2026, 3, 20, 9, 0, 0, 0, time.UTC)

// The period every statement and every run in this file covers. It is a single day for the runs
// — which is what the daily job reconciles — and a month for the statement.
var (
	runDay        = time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	statementFrom = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	statementTo   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
)

const secureBucket = "secure"

type fixture struct {
	h *dbtest.Harness

	reports   *application.Service
	documents *documentapp.Service
	store     *objectstore.Memory

	tenant   uuid.UUID
	actor    uuid.UUID
	other    uuid.UUID
	sponsor  uuid.UUID
	payer    uuid.UUID
	provider uuid.UUID
	rival    uuid.UUID

	person     uuid.UUID
	program    uuid.UUID
	enrollment uuid.UUID
	definition uuid.UUID
	queue      uuid.UUID
}

func newFixture(t *testing.T) *fixture { //nolint:funlen // one linear fixture reads better whole
	t.Helper()
	h := dbtest.New(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cursors, err := httpx.NewCursorCodec([]byte("report-fixture-cursor-signing-key-32b"))
	if err != nil {
		t.Fatalf("cursor codec: %v", err)
	}
	store := objectstore.NewMemory()
	documents, err := documentapp.New(documentapp.Deps{
		Pool: h.App, Repo: documentpg.New(), Store: store, Audit: auditpg.New(),
		Cursors: cursors, Logger: logger,
		Storage: documentapp.Storage{SecureBucket: secureBucket},
		Now:     func() time.Time { return fixtureNow },
	})
	if err != nil {
		t.Fatalf("document service: %v", err)
	}
	reports, err := application.New(application.Deps{
		Pool: h.App, Repo: reportpg.New(),
		Documents: reportgw.NewDocuments(documents),
		WorkItems: reportpg.NewWorkItems(logger),
		Audit:     auditpg.New(), Cursors: cursors, Logger: logger,
		Now: func() time.Time { return fixtureNow },
	})
	if err != nil {
		t.Fatalf("report service: %v", err)
	}

	f := &fixture{h: h, reports: reports, documents: documents, store: store}
	ctx, cancel := h.Ctx()
	defer cancel()
	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		t.Helper()
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}

	f.tenant = h.CreateTenant("RPT" + uuid.NewString()[:6])
	f.actor = h.CreateActor("report-"+uuid.NewString()[:8], "Mali Değerlendirici")
	h.CreateMembership(f.tenant, f.actor)
	f.other = h.CreateActor("report-other-"+uuid.NewString()[:8], "Başka Kullanıcı")
	h.CreateMembership(f.tenant, f.other)
	f.sponsor = h.CreateTenantOrganization(f.tenant, "Sponsor", "SPONSOR")
	f.payer = h.CreateTenantOrganization(f.tenant, "Payer", "PAYER")
	f.provider = h.CreateTenantOrganization(f.tenant, "Provider", "PROVIDER")
	f.rival = h.CreateTenantOrganization(f.tenant, "Rival Provider", "PROVIDER")

	// The provider's tax identity, which every invoice copies as a blind index.
	h.AdminExec(`
		UPDATE directory.organization o
		   SET tax_number_cipher = decode('00', 'hex'),
		       tax_number_hash   = sha256('4620081341'::bytea)
		  FROM directory.tenant_organization t
		 WHERE t.tenant_id = $1 AND t.id = $2 AND o.id = t.organization_id`,
		f.tenant, f.provider)
	h.AdminExec(`
		UPDATE directory.organization o
		   SET tax_number_cipher = decode('00', 'hex'),
		       tax_number_hash   = sha256('4620081342'::bytea)
		  FROM directory.tenant_organization t
		 WHERE t.tenant_id = $1 AND t.id = $2 AND o.id = t.organization_id`,
		f.tenant, f.rival)

	h.AdminExec(`INSERT INTO party.membership_type (tenant_id, code, display_name)
	             VALUES ($1, 'MEMBER', 'Üye')`, f.tenant)
	var membership, plan uuid.UUID
	scan(&f.person, "person", `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Ada', 'Kaya', 'ada kaya') RETURNING id`, f.tenant)
	scan(&membership, "membership", `
		INSERT INTO party.sponsor_membership (tenant_id, person_id, sponsor_tenant_organization_id,
		                                      membership_type, status, valid_period)
		VALUES ($1, $2, $3, 'MEMBER', 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, f.person, f.sponsor)
	h.AdminExec(`INSERT INTO benefit.program_type (tenant_id, code, display_name)
	             VALUES ($1, 'BENEFIT', 'Fayda')`, f.tenant)
	scan(&f.program, "program", `
		INSERT INTO benefit.program (tenant_id, sponsor_tenant_organization_id,
		                             payer_tenant_organization_id, code, name, program_type,
		                             status, valid_period)
		VALUES ($1, $2, $3, 'PRG', 'Program', 'BENEFIT', 'ACTIVE',
		        daterange('2026-01-01', NULL, '[)')) RETURNING id`, f.tenant, f.sponsor, f.payer)
	scan(&plan, "plan", `
		INSERT INTO benefit.plan (tenant_id, program_id, code, name, status)
		VALUES ($1, $2, 'PLAN', 'Plan', 'ACTIVE') RETURNING id`, f.tenant, f.program)
	scan(&f.enrollment, "enrollment", `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status,
		                                valid_period)
		VALUES ($1, $2, $3, 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		f.tenant, membership, plan)

	var category uuid.UUID
	scan(&category, "category", `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'HEALTH_ROOT', 'Sağlık', 'HEALTH') RETURNING id`, f.tenant)
	scan(&f.definition, "definition", `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name,
		                                        fulfillment_mode, default_unit_type)
		VALUES ($1, $2, 'CONSULT', 'Muayene', 'DIRECT', 'COUNT') RETURNING id`,
		f.tenant, category)

	// The finance queue a differing run raises its work item into. A tenant with no queue gets
	// no item and the run is still written; a test asserts that separately.
	scan(&f.queue, "work queue", `
		INSERT INTO workflow.work_queue (tenant_id, code, name, domain_code, sla_minutes)
		VALUES ($1, 'RECONCILIATION_DIFFERENCE', 'Mutabakat farkı', 'GENERIC', 1440)
		RETURNING id`, f.tenant)
	return f
}

// readerRC is the payer's financial reviewer as FINANCIAL_REVIEWER is issued in roles.go: it
// reads every figure and may take the ordinary kinds out of the building, including the sensitive
// one.
func (f *fixture) readerRC() identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal: identity.Principal{ActorID: f.actor},
		Permissions: map[string]struct{}{
			application.PermissionRead:            {},
			application.PermissionExport:          {},
			application.PermissionExportSensitive: {},
		},
	}
}

// plainExporterRC holds `report.export` and not the sensitive grant. It is the caller the CLAIMS
// refusal is about.
func (f *fixture) plainExporterRC() identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal: identity.Principal{ActorID: f.actor},
		Permissions: map[string]struct{}{
			application.PermissionRead:   {},
			application.PermissionExport: {},
		},
	}
}

// providerRC is a provider-scoped caller: the same permissions, bounded to one organization by an
// ORGANIZATION grant, exactly as PROVIDER_BILLING is issued.
func (f *fixture) providerRC(org uuid.UUID) identity.RequestContext {
	rc := f.readerRC()
	rc.Principal = identity.Principal{ActorID: f.other}
	rc.Scopes = []identity.Scope{{
		Type: application.ScopeOrganization, ID: uuid.NullUUID{UUID: org, Valid: true},
	}}
	return rc
}

// seedBatch writes one decided icmal of the given provider.
func (f *fixture) seedBatch(t *testing.T, provider uuid.UUID, decidedOn time.Time,
	submitted, approved, cut string,
) uuid.UUID {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var id uuid.UUID
	if err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO billing.batch (tenant_id, reference, provider_organization_id,
		                           payer_organization_id, currency_code, period_from, period_to,
		                           status, submitted_at, submitted_by, decided_at, decided_by,
		                           invoice_count, submitted_total, approved_total, cut_total,
		                           returned_total, rejected_total)
		VALUES ($1, $2, $3, $4, 'TRY', $5::date, $5::date, 'DECIDED',
		        $5::timestamptz, $6, $5::timestamptz, $6, 1,
		        $7::text::numeric, $8::text::numeric, $9::text::numeric, 0, 0)
		RETURNING id`,
		f.tenant, batchReference(), provider, f.payer, decidedOn.Format(time.DateOnly),
		f.actor, submitted, approved, cut).Scan(&id); err != nil {
		t.Fatalf("seed batch: %v", err)
	}
	return id
}

// seedSettlement writes one settlement on a batch, with the paid amount and the status given.
//
// The settlement and its payment record go in as one statement on purpose. WP-I7-04's deferred
// constraint trigger checks at commit that a settlement's stored `paid_amount` is exactly the sum
// of its live records, so a fixture that wrote the two separately would be refused by the database
// -- which is the trigger doing its job, and the reason `PAID_SUM_MISMATCH` is a difference kind
// this fixture cannot produce at all.
//
// The status is written as it is asked for, so a test can seed a state the service never produces
// on its own: a PAID settlement waiting to be reconciled, for instance.
func (f *fixture) seedSettlement(t *testing.T, provider, batch uuid.UUID, due time.Time,
	payable, paid, status string,
) uuid.UUID {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var id uuid.UUID
	if err := f.h.Admin.QueryRow(ctx, `
		WITH created AS (
			INSERT INTO billing.settlement (tenant_id, reference, batch_id,
			                                provider_organization_id, payer_organization_id,
			                                currency_code, approved_amount, withheld_amount,
			                                payable_amount, paid_amount, due_date,
			                                settlement_method, status, approved_by, approved_at)
			VALUES ($1, $2, $3, $4, $5, 'TRY', $6::text::numeric, 0, $6::text::numeric,
			        $7::text::numeric, $8::date, 'BANK_TRANSFER', $9, $10, clock_timestamp())
			RETURNING id, tenant_id, provider_organization_id
		), recorded AS (
			INSERT INTO billing.payment_record (tenant_id, settlement_id,
			                                    provider_organization_id, external_reference,
			                                    amount, currency_code, paid_at, source, status)
			SELECT c.tenant_id, c.id, c.provider_organization_id, $11,
			       $7::text::numeric, 'TRY', clock_timestamp(), 'MANUAL', 'RECORDED'
			  FROM created c WHERE $7::text::numeric > 0
			RETURNING settlement_id
		)
		SELECT id FROM created`,
		f.tenant, settlementReference(), batch, provider, f.payer, payable, paid,
		due.Format(time.DateOnly), status, f.actor, "EFT"+upperTail(10)).Scan(&id); err != nil {
		t.Fatalf("seed settlement: %v", err)
	}
	return id
}

// seedInvoice writes one submitted invoice of a provider, dated inside the statement's period.
func (f *fixture) seedInvoice(t *testing.T, provider uuid.UUID, day time.Time, payable string,
	batch *uuid.UUID,
) uuid.UUID {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var id uuid.UUID
	if err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO billing.invoice (tenant_id, provider_organization_id, payer_organization_id,
		                             invoice_number, invoice_date, fiscal_year,
		                             provider_tax_id_hash, currency_code, line_extension_amount,
		                             tax_amount, payable_amount, status, batch_id, submitted_at)
		VALUES ($1, $2, $3, $4, $5::date, 2026, sha256('4620081341'::bytea), 'TRY',
		        $6::text::numeric, 0, $6::text::numeric, 'IN_BATCH', $7, clock_timestamp())
		RETURNING id`,
		f.tenant, provider, f.payer, "FT"+upperTail(10), day.Format(time.DateOnly), payable,
		batch).Scan(&id); err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	return id
}

// seedClaim writes one claim with one line and one decision, which is what the dashboard counts
// and what the CLAIMS export renders.
func (f *fixture) seedClaim(t *testing.T, provider uuid.UUID, status, approved, description string,
	created time.Time,
) uuid.UUID {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var claim, version, line uuid.UUID
	if err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO claim.claim (tenant_id, reference, person_id, program_id, enrollment_id,
		                         provider_organization_id, domain_code, current_version_no,
		                         status, service_date_from, service_date_to, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'HEALTH', 1, $7, $8::date, $8::date, $9)
		RETURNING id`,
		f.tenant, "CLM"+upperTail(8), f.person, f.program, f.enrollment, provider, status,
		created.Format(time.DateOnly), created).Scan(&claim); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	if err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO claim.claim_version (tenant_id, claim_id, version_no, status, submitted_at,
		                                 submitted_by, created_by)
		VALUES ($1, $2, 1, 'SUBMITTED', clock_timestamp(), $3, $3) RETURNING id`,
		f.tenant, claim, f.actor).Scan(&version); err != nil {
		t.Fatalf("seed claim version: %v", err)
	}
	if err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO claim.claim_line (tenant_id, version_id, line_no, service_definition_id,
		                              unit_type, quantity, unit_amount, line_amount,
		                              currency_code, description)
		VALUES ($1, $2, 1, $3, 'COUNT', 1, $4::text::numeric, $4::text::numeric, 'TRY', $5)
		RETURNING id`,
		f.tenant, version, f.definition, approved, description).Scan(&line); err != nil {
		t.Fatalf("seed claim line: %v", err)
	}
	f.h.AdminExec(`
		INSERT INTO claim.line_decision (tenant_id, line_id, decided_in_version_no, decision,
		                                 approved_quantity, approved_amount, payer_amount,
		                                 member_amount, reason_code, decided_by, stage)
		VALUES ($1, $2, 1, 'APPROVED', 1, $3::text::numeric, $3::text::numeric, 0,
		        'CONTRACT_PRICE', $4, 'FINANCIAL')`,
		f.tenant, line, approved, f.actor)
	return claim
}

// count runs a scalar count through the admin pool, with the application bypassed.
func (f *fixture) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var n int
	if err := f.h.Admin.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// scalar reads one text value through the admin pool.
func (f *fixture) scalar(t *testing.T, sql string, args ...any) string {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var value string
	if err := f.h.Admin.QueryRow(ctx, sql, args...).Scan(&value); err != nil {
		t.Fatalf("scalar: %v", err)
	}
	return value
}

// ctx is a bounded context for a service call.
func (f *fixture) ctx() (context.Context, context.CancelFunc) { return f.h.Ctx() }

// The reference generators. Both mirror the CHECK the schema carries, so a seeded row is a row the
// service could have written.
const referenceAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

func upperTail(n int) string {
	raw := uuid.NewString()
	out := make([]byte, 0, n)
	for i := 0; len(out) < n && i < len(raw); i++ {
		c := raw[i]
		switch {
		case c >= 'a' && c <= 'z':
			out = append(out, c-('a'-'A'))
		case c >= '2' && c <= '7':
			out = append(out, c)
		}
	}
	for len(out) < n {
		out = append(out, referenceAlphabet[len(out)%len(referenceAlphabet)])
	}
	return string(out)
}

func batchReference() string      { return "IC-202603-" + upperTail(8) }
func settlementReference() string { return "ST-202603-" + upperTail(8) }
