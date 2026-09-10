// The invoice, driven end to end against a real database: real claims with real line
// decisions, the real claim module behind the INVOICED transition, the real tenant settings
// behind the tolerance, and the real deferred trigger underneath every allocation.
//
// Nothing below stubs the claim module. Every property the work package asks for is a property
// of the join between the two — a fake claims port would prove that a fake moved, and the one
// thing this package must never get wrong is exactly that a claim which is being invoiced stops
// being invoiceable.
package application_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	"github.com/celikbros/kapsora/internal/billing/application"
	"github.com/celikbros/kapsora/internal/billing/domain"
	billinggw "github.com/celikbros/kapsora/internal/billing/infrastructure/gateway"
	billingpg "github.com/celikbros/kapsora/internal/billing/infrastructure/postgres"
	claimapp "github.com/celikbros/kapsora/internal/claim/application"
	claimpg "github.com/celikbros/kapsora/internal/claim/infrastructure/postgres"
	healthapp "github.com/celikbros/kapsora/internal/health/application"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/crypto/localkey"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// fixtureNow is where the fixture's clock is pinned, so a submitted_at is deterministic.
var fixtureNow = time.Date(2026, 3, 20, 9, 0, 0, 0, time.UTC)

// invoiceDay is the date every invoice in this file carries. It is a fixed day inside a fixed
// fiscal year, so the uniqueness rule is exercised without depending on when the tests run.
const invoiceDay = "2026-03-17"

// fixtureMasterKey is the local cipher's key in these tests. It is a constant so that a test
// that encrypts an IBAN and then sweeps the schema for it is comparing against a ciphertext
// this process actually produced.
var fixtureMasterKey = []byte{
	0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
	0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f,
	0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17,
	0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f,
}

type fixture struct {
	h *dbtest.Harness

	invoices *application.Service
	claims   *claimapp.Service

	tenant     uuid.UUID
	actor      uuid.UUID
	reviewer   uuid.UUID
	approver   uuid.UUID
	provider   uuid.UUID
	other      uuid.UUID
	sponsor    uuid.UUID
	person     uuid.UUID
	program    uuid.UUID
	enrollment uuid.UUID
	definition uuid.UUID
	document   uuid.UUID
}

func newFixture(t *testing.T) *fixture { //nolint:funlen // one linear fixture reads better whole
	t.Helper()
	h := dbtest.New(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cursors, err := httpx.NewCursorCodec([]byte("billing-fixture-cursor-signing-key-32b"))
	if err != nil {
		t.Fatalf("cursor codec: %v", err)
	}

	claims, err := claimapp.New(claimapp.Deps{
		Pool: h.App, Repo: claimpg.New(), Audit: auditpg.New(), Cursors: cursors,
		Logger: logger, Now: func() time.Time { return fixtureNow },
	})
	if err != nil {
		t.Fatalf("claim service: %v", err)
	}
	// The cipher the member's IBAN goes through (WP-I7-04). It is the real localkey provider
	// under a fixed test key rather than a stub, because the one thing this package must never
	// get wrong is that the number reaches the database as an envelope — and a stub cipher
	// would prove that a stub encrypted something.
	keys, err := localkey.New(fixtureMasterKey)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	invoices, err := application.New(application.Deps{
		Pool: h.App, Repo: billingpg.New(), Batches: billingpg.NewBatchRepository(),
		Settlements: billingpg.NewSettlementRepository(),
		Claims:      billinggw.NewClaims(claims), WorkItems: billingpg.NewWorkItems(logger),
		// The real ledger, for the same reason: a reimbursement that consumed a fake wallet
		// would prove nothing about conservation.
		Entitlements: billinggw.NewEntitlements(
			ledger.NewLedger(func() time.Time { return fixtureNow })),
		Payments: application.RecordingPaymentOrders{},
		Cipher:   keys,
		Audit:    auditpg.New(), Cursors: cursors, Logger: logger,
		Now: func() time.Time { return fixtureNow },
	})
	if err != nil {
		t.Fatalf("billing service: %v", err)
	}

	f := &fixture{h: h, invoices: invoices, claims: claims}
	ctx, cancel := h.Ctx()
	defer cancel()
	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		t.Helper()
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}

	f.tenant = h.CreateTenant("INV" + uuid.NewString()[:6])
	f.actor = h.CreateActor("invoice-"+uuid.NewString()[:8], "Fatura Kullanıcısı")
	h.CreateMembership(f.tenant, f.actor)
	// Two more people, because the icmal's maker-checker rule is about *people*: the provider
	// clerk who sends a batch, the payer's reviewer who works through it, and the second pair
	// of eyes above the tenant's threshold are three actors and never one with three hats.
	f.reviewer = h.CreateActor("reviewer-"+uuid.NewString()[:8], "İnceleyici")
	h.CreateMembership(f.tenant, f.reviewer)
	f.approver = h.CreateActor("approver-"+uuid.NewString()[:8], "Onaylayıcı")
	h.CreateMembership(f.tenant, f.approver)
	f.sponsor = h.CreateTenantOrganization(f.tenant, "Sponsor", "SPONSOR")
	payer := h.CreateTenantOrganization(f.tenant, "Payer", "PAYER")
	f.provider = h.CreateTenantOrganization(f.tenant, "Provider", "PROVIDER")
	f.other = h.CreateTenantOrganization(f.tenant, "Other Provider", "PROVIDER")

	// The provider's tax identity, as the directory holds it: an envelope and a blind index.
	// The invoice copies the index; the number itself never leaves the encrypted column, and
	// there is no path in the billing package that could read it.
	h.AdminExec(`
		UPDATE directory.organization o
		   SET tax_number_cipher = decode('00', 'hex'),
		       tax_number_hash   = sha256('4620081341'::bytea)
		  FROM directory.tenant_organization t
		 WHERE t.tenant_id = $1 AND t.id = $2 AND o.id = t.organization_id`,
		f.tenant, f.provider)

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
		        daterange('2026-01-01', NULL, '[)')) RETURNING id`, f.tenant, f.sponsor, payer)
	scan(&plan, "plan", `
		INSERT INTO benefit.plan (tenant_id, program_id, code, name, status)
		VALUES ($1, $2, 'PLAN', 'Plan', 'ACTIVE') RETURNING id`, f.tenant, f.program)
	scan(&f.enrollment, "enrollment", `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status, valid_period)
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

	// The scanned invoice image: a WP-I4-04 document object the scanner has cleared.
	scan(&f.document, "document", `
		INSERT INTO document.object (tenant_id, owner_tenant_organization_id, object_key,
		                             original_filename, content_type, byte_size, sha256,
		                             bucket, scan_status, uploaded_at)
		VALUES ($1, $2, $3::text, 'fatura.pdf', 'application/pdf', 1024,
		        sha256($3::text::bytea), 'secure', 'CLEAN', clock_timestamp())
		RETURNING id`, f.tenant, f.provider, "invoices/"+uuid.NewString())
	return f
}

// approvedClaim writes one decided claim of this provider and returns its id. The claim is
// written directly rather than driven through WP-I5-04's pipeline because what this package
// cares about is only what a decided claim *is worth* — and that is the line decision.
func (f *fixture) approvedClaim(t *testing.T, reference, approved string) uuid.UUID {
	t.Helper()
	return f.claimFor(t, f.provider, reference, "APPROVED", approved, nil)
}

// claimFor is approvedClaim with the provider, the status and the line description spelled out.
func (f *fixture) claimFor(t *testing.T, provider uuid.UUID, reference, status, approved string,
	description *string,
) uuid.UUID {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var claimID, version, line uuid.UUID
	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		t.Helper()
		if err := f.h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	scan(&claimID, "claim", `
		INSERT INTO claim.claim (tenant_id, reference, person_id, program_id, enrollment_id,
		                         provider_organization_id, service_date_from, service_date_to,
		                         status)
		VALUES ($1, $2, $3, $4, $5, $6, '2026-03-01', '2026-03-01', $7) RETURNING id`,
		f.tenant, reference, f.person, f.program, f.enrollment, provider, status)
	scan(&version, "claim version", `
		INSERT INTO claim.claim_version (tenant_id, claim_id, version_no, status, submitted_at)
		VALUES ($1, $2, 1, 'SUBMITTED', clock_timestamp()) RETURNING id`, f.tenant, claimID)
	scan(&line, "claim line", `
		INSERT INTO claim.claim_line (tenant_id, version_id, line_no, service_definition_id,
		                              unit_type, quantity, line_amount, description)
		VALUES ($1, $2, 1, $3, 'COUNT', 1, $4::text::numeric, $5) RETURNING id`,
		f.tenant, version, f.definition, approved, description)
	f.h.AdminExec(`
		INSERT INTO claim.line_decision (tenant_id, line_id, decided_in_version_no, decision,
		                                 approved_quantity, approved_amount, payer_amount,
		                                 member_amount, reason_code, stage)
		VALUES ($1, $2, 1, 'APPROVED', 1, $3::text::numeric, $3::text::numeric, 0,
		        'CONTRACT_PRICE', 'AUTO')`, f.tenant, line, approved)
	return claimID
}

// claimStatus reads a claim's status straight out of the table, so a test asserts what is
// stored rather than what a projection said.
func (f *fixture) claimStatus(t *testing.T, claimID uuid.UUID) string {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var status string
	if err := f.h.Admin.QueryRow(ctx,
		`SELECT status FROM claim.claim WHERE tenant_id = $1 AND id = $2`,
		f.tenant, claimID).Scan(&status); err != nil {
		t.Fatalf("read claim status: %v", err)
	}
	return status
}

// setSetting writes one tenant setting, the way an operator would.
func (f *fixture) setSetting(key, jsonValue string) {
	f.h.AdminExec(`
		INSERT INTO platform.tenant_setting (tenant_id, setting_key, value_json)
		VALUES ($1, $2, $3::jsonb)
		ON CONFLICT (tenant_id, setting_key) DO UPDATE SET value_json = excluded.value_json`,
		f.tenant, key, jsonValue)
}

// providerRC is the provider's billing clerk: invoice.read and invoice.manage, scoped to its
// own organization, exactly as PROVIDER_BILLING is issued in roles.go.
func (f *fixture) providerRC() identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal: identity.Principal{ActorID: f.actor},
		Permissions: map[string]struct{}{
			application.PermissionRead: {}, application.PermissionManage: {},
		},
		Scopes: []identity.Scope{{
			Type: application.ScopeOrganization,
			ID:   uuid.NullUUID{UUID: f.provider, Valid: true},
		}},
	}
}

// financialRC is the payer's financial reviewer: tenant-wide, no clinical grant of any kind,
// which is what FINANCIAL_REVIEWER holds in roles.go.
func (f *fixture) financialRC() identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal: identity.Principal{ActorID: f.actor},
		Permissions: map[string]struct{}{
			application.PermissionRead: {}, application.PermissionManage: {},
		},
	}
}

// sponsorHRRC is the sponsor's HR user: invoice.read and nothing clinical. It is the subject of
// WP-I7-02 §2.3, and the absence of health.clinical.read is the whole of it.
func (f *fixture) sponsorHRRC() identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal:   identity.Principal{ActorID: f.actor},
		Permissions: map[string]struct{}{application.PermissionRead: {}},
	}
}

// clinicalRC is a caller that has earned the clinical projection — a medical reviewer with
// invoice.read. It exists so the projection test can prove that the field is *there* for
// somebody, rather than merely absent for everyone.
func (f *fixture) clinicalRC() identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal: identity.Principal{ActorID: f.actor},
		Permissions: map[string]struct{}{
			application.PermissionRead:        {},
			healthapp.PermissionClinicalRead:  {},
			healthapp.PermissionSensitiveRead: {},
			application.PermissionManage:      {},
		},
	}
}

// createInvoice opens a draft with the header a test needs and fails on anything unexpected.
func (f *fixture) createInvoice(t *testing.T, rc identity.RequestContext, number, payable string,
) application.InvoiceView {
	t.Helper()
	view, err := f.invoices.CreateInvoice(context.Background(), rc, f.header(number, payable))
	if err != nil {
		t.Fatalf("create invoice %s: %v", number, err)
	}
	return view
}

// header is the smallest header the domain accepts, with no tax so the arithmetic of the
// *header* stays out of the way of the arithmetic these tests are about.
func (f *fixture) header(number, payable string) application.CreateInvoiceInput {
	day, _ := time.Parse(time.DateOnly, invoiceDay)
	document := f.document
	return application.CreateInvoiceInput{
		ProviderOrganizationID: f.provider,
		InvoiceNumber:          number,
		InvoiceDate:            day,
		LineExtensionAmount:    payable,
		TaxAmount:              "0",
		PayableAmount:          payable,
		DocumentID:             &document,
	}
}

// returnedInvoice raises an invoice covering one claim, submits it, and has WP-I7-03's
// reviewer send it back. It is the state a correction is opened from, and it takes four
// commands, so the tests that need two of them say so in one line.
func (f *fixture) returnedInvoice(t *testing.T, rc identity.RequestContext,
	number, claimReference, amount string,
) application.InvoiceView {
	t.Helper()
	claim := f.approvedClaim(t, claimReference, amount)
	view := f.createInvoice(t, rc, number, amount)
	view = f.allocate(t, rc, view, claim, amount)
	submitted, err := f.invoices.SubmitInvoice(context.Background(), rc, view.Invoice.ID,
		view.Invoice.RowVersion)
	if err != nil {
		t.Fatalf("submit %s: %v", number, err)
	}
	f.returnInvoice(submitted.Invoice.ID)
	returned, err := f.invoices.GetInvoice(context.Background(), rc, submitted.Invoice.ID)
	if err != nil {
		t.Fatalf("read the returned invoice %s: %v", number, err)
	}
	if returned.Invoice.Status != domain.StatusReturned {
		t.Fatalf("invoice %s is %s, want RETURNED", number, returned.Invoice.Status)
	}
	return returned
}

// invoiceCount reads how many invoices this tenant has, straight out of the table. A refused
// command must leave it where it was: a create that answered an error and still wrote a row
// would be a draft nobody asked for holding a number nobody could then use.
func (f *fixture) invoiceCount(t *testing.T) int {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var n int
	if err := f.h.Admin.QueryRow(ctx,
		`SELECT count(*) FROM billing.invoice WHERE tenant_id = $1`, f.tenant).Scan(&n); err != nil {
		t.Fatalf("count invoices: %v", err)
	}
	return n
}

// allocate replaces the invoice's allocation set with one claim at one amount.
func (f *fixture) allocate(t *testing.T, rc identity.RequestContext, view application.InvoiceView,
	claimID uuid.UUID, amount string,
) application.InvoiceView {
	t.Helper()
	out, err := f.invoices.PutAllocations(context.Background(), rc, view.Invoice.ID,
		[]application.AllocationInput{{ClaimID: claimID, AllocatedAmount: amount}},
		view.Invoice.RowVersion)
	if err != nil {
		t.Fatalf("put allocations: %v", err)
	}
	return out
}

// returnInvoice does to an invoice exactly what WP-I7-03's reviewer will: it moves the status
// to RETURNED — which releases the claim links through the database's own trigger — and puts
// the claims back to the status they were allocated at, which is `ReleaseFromInvoice`.
//
// WP-I7-03 now performs both halves through `ReviewBatchInvoice`, and its own tests drive that
// command; here the two are written directly, because these tests are about the *invoice* and
// building an icmal around every one of them would be four commands of somebody else's package.
// They are written *together* because that is the contract `ClaimsPort` states: the link flag
// and the claim status move as one, and a reviewer that moved only the first would leave a claim
// nobody could ever bill again.
func (f *fixture) returnInvoice(invoiceID uuid.UUID) {
	f.returnInvoiceWithoutReleasingClaims(invoiceID)
	f.h.AdminExec(`
		UPDATE claim.claim c
		   SET status = ic.claim_status_before
		  FROM billing.invoice_claim ic
		 WHERE ic.tenant_id = c.tenant_id AND ic.claim_id = c.id
		   AND ic.invoice_id = $2 AND c.tenant_id = $1 AND c.status = 'INVOICED'`,
		f.tenant, invoiceID)
}

// returnInvoiceWithoutReleasingClaims is the first half alone. It exists so a test can prove
// that a cancellation after a return still puts the claims back — the case where the link flag
// has already moved and the claim status has not.
func (f *fixture) returnInvoiceWithoutReleasingClaims(invoiceID uuid.UUID) {
	f.h.AdminExec(`UPDATE billing.invoice SET status = 'RETURNED' WHERE tenant_id = $1 AND id = $2`,
		f.tenant, invoiceID)
}
