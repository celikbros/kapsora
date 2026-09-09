package dbtests

import (
	"testing"

	"github.com/google/uuid"

	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// The invoice schema of migration 000044 (WP-I7-02), with the application layer bypassed
// entirely: every statement below runs as the schema owner through the admin pool, so no Go
// code of ours is between them and the constraint.
//
// These are the rules the service is allowed to lean on. A rule the service can forget is not
// a rule, and the four this package stands on are all here: a number that is unique in the
// provider's fiscal year, a header whose two halves are its total, an allocation that cannot
// exceed what the payer approved, and a submitted invoice whose links and figures cannot move.

// billingSeed is everything an invoice row references, plus two approved claims to allocate.
type billingSeed struct {
	tenant   uuid.UUID
	actor    uuid.UUID
	provider uuid.UUID
	// other is a second provider of the same tenant, for the "the same number across two
	// providers is fine" half of the uniqueness rule.
	other      uuid.UUID
	sponsor    uuid.UUID
	person     uuid.UUID
	program    uuid.UUID
	enrollment uuid.UUID
	definition uuid.UUID
}

func seedBilling(t *testing.T, h *dbtest.Harness) billingSeed { //nolint:funlen // one linear fixture reads better whole
	t.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	var s billingSeed
	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		t.Helper()
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}

	s.tenant = h.CreateTenant("BIL" + uuid.NewString()[:6])
	s.actor = h.CreateActor("billing-db-"+uuid.NewString()[:8], "Billing DB")
	s.sponsor = h.CreateTenantOrganization(s.tenant, "Sponsor", "SPONSOR")
	payer := h.CreateTenantOrganization(s.tenant, "Payer", "PAYER")
	s.provider = h.CreateTenantOrganization(s.tenant, "Provider", "PROVIDER")
	s.other = h.CreateTenantOrganization(s.tenant, "Other Provider", "PROVIDER")

	// The provider's tax identity. It is a blind index in the shared directory, and it is what
	// an invoice copies: the plaintext VKN lives in `tax_number_cipher` and nothing in the
	// billing schema ever sees it.
	h.AdminExec(`
		UPDATE directory.organization o
		   SET tax_number_cipher = decode('00', 'hex'),
		       tax_number_hash   = sha256('4620081341'::bytea)
		  FROM directory.tenant_organization t
		 WHERE t.tenant_id = $1 AND t.id = $2 AND o.id = t.organization_id`,
		s.tenant, s.provider)

	h.AdminExec(`INSERT INTO party.membership_type (tenant_id, code, display_name)
	             VALUES ($1, 'MEMBER', 'Üye')`, s.tenant)
	var membership, plan uuid.UUID
	scan(&s.person, "person", `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Ada', 'Kaya', 'ada kaya') RETURNING id`, s.tenant)
	scan(&membership, "membership", `
		INSERT INTO party.sponsor_membership (tenant_id, person_id, sponsor_tenant_organization_id,
		                                      membership_type, status, valid_period)
		VALUES ($1, $2, $3, 'MEMBER', 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		s.tenant, s.person, s.sponsor)
	h.AdminExec(`INSERT INTO benefit.program_type (tenant_id, code, display_name)
	             VALUES ($1, 'BENEFIT', 'Fayda')`, s.tenant)
	scan(&s.program, "program", `
		INSERT INTO benefit.program (tenant_id, sponsor_tenant_organization_id,
		                             payer_tenant_organization_id, code, name, program_type,
		                             status, valid_period)
		VALUES ($1, $2, $3, 'PRG', 'Program', 'BENEFIT', 'ACTIVE',
		        daterange('2026-01-01', NULL, '[)')) RETURNING id`, s.tenant, s.sponsor, payer)
	scan(&plan, "plan", `
		INSERT INTO benefit.plan (tenant_id, program_id, code, name, status)
		VALUES ($1, $2, 'PLAN', 'Plan', 'ACTIVE') RETURNING id`, s.tenant, s.program)
	scan(&s.enrollment, "enrollment", `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status, valid_period)
		VALUES ($1, $2, $3, 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		s.tenant, membership, plan)

	var category uuid.UUID
	scan(&category, "category", `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'HEALTH_ROOT', 'Sağlık', 'HEALTH') RETURNING id`, s.tenant)
	scan(&s.definition, "definition", `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name,
		                                        fulfillment_mode, default_unit_type)
		VALUES ($1, $2, 'CONSULT', 'Muayene', 'DIRECT', 'COUNT') RETURNING id`,
		s.tenant, category)
	return s
}

// claim writes one claim of the seed's provider, decided at `approved`, and returns its id. The
// decision is what makes the claim worth anything: `billing.claim_approved_total` sums the
// latest decision of every line of the current version and subtracts the adjustments.
func (s billingSeed) claim(t *testing.T, h *dbtest.Harness, reference, status, approved string,
) uuid.UUID {
	t.Helper()
	return s.claimFor(t, h, s.provider, reference, status, approved, nil)
}

// claimFor is `claim` with the provider and the line description spelled out.
func (s billingSeed) claimFor(t *testing.T, h *dbtest.Harness, provider uuid.UUID,
	reference, status, approved string, description *string,
) uuid.UUID {
	t.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	var claimID, version, line uuid.UUID
	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		t.Helper()
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	scan(&claimID, "claim", `
		INSERT INTO claim.claim (tenant_id, reference, person_id, program_id, enrollment_id,
		                         provider_organization_id, service_date_from, service_date_to,
		                         status)
		VALUES ($1, $2, $3, $4, $5, $6, '2026-06-15', '2026-06-15', $7) RETURNING id`,
		s.tenant, reference, s.person, s.program, s.enrollment, provider, status)
	scan(&version, "claim version", `
		INSERT INTO claim.claim_version (tenant_id, claim_id, version_no, status, submitted_at)
		VALUES ($1, $2, 1, 'SUBMITTED', clock_timestamp()) RETURNING id`, s.tenant, claimID)
	scan(&line, "claim line", `
		INSERT INTO claim.claim_line (tenant_id, version_id, line_no, service_definition_id,
		                              unit_type, quantity, line_amount, description)
		VALUES ($1, $2, 1, $3, 'COUNT', 1, $4::text::numeric, $5) RETURNING id`,
		s.tenant, version, s.definition, approved, description)
	h.AdminExec(`
		INSERT INTO claim.line_decision (tenant_id, line_id, decided_in_version_no, decision,
		                                 approved_quantity, approved_amount, payer_amount,
		                                 member_amount, reason_code, stage)
		VALUES ($1, $2, 1, 'APPROVED', 1, $3::text::numeric, $3::text::numeric, 0,
		        'CONTRACT_PRICE', 'AUTO')`, s.tenant, line, approved)
	return claimID
}

// insertInvoice writes one invoice header directly, so a test can put the table into any state
// the constraints allow and prove the ones they do not.
func (s billingSeed) insertInvoice(t *testing.T, h *dbtest.Harness, provider uuid.UUID,
	number, date, lines, tax, payable, status string,
) (uuid.UUID, error) {
	t.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	var id uuid.UUID
	err := h.Admin.QueryRow(ctx, `
		INSERT INTO billing.invoice (
		    tenant_id, provider_organization_id, invoice_number, invoice_date, fiscal_year,
		    provider_tax_id_hash, line_extension_amount, tax_amount, payable_amount, status,
		    submitted_at)
		VALUES ($1, $2, $3, $4::date, EXTRACT(YEAR FROM $4::date)::int,
		        sha256('4620081341'::bytea), $5::text::numeric, $6::text::numeric,
		        $7::text::numeric, $8,
		        CASE WHEN $8 IN ('DRAFT','CANCELLED') THEN NULL ELSE clock_timestamp() END)
		RETURNING id`,
		s.tenant, provider, number, date, lines, tax, payable, status).Scan(&id)
	return id, err
}

// mustInvoice is insertInvoice for the rows a test is not asserting about.
func (s billingSeed) mustInvoice(t *testing.T, h *dbtest.Harness, number, date, payable string,
) uuid.UUID {
	t.Helper()
	id, err := s.insertInvoice(t, h, s.provider, number, date, payable, "0", payable, "DRAFT")
	if err != nil {
		t.Fatalf("insert invoice %s: %v", number, err)
	}
	return id
}

// allocate links a claim to an invoice directly, returning the error so a test can assert the
// deferred trigger rather than only that the happy path works.
func (s billingSeed) allocate(t *testing.T, h *dbtest.Harness, invoice, claim uuid.UUID,
	amount, currency string,
) error {
	t.Helper()
	return h.AdminExecErr(`
		INSERT INTO billing.invoice_claim (tenant_id, invoice_id, claim_id, claim_version_no,
		                                   allocated_amount, currency_code, claim_status_before)
		VALUES ($1, $2, $3, 1, $4::text::numeric, $5, 'APPROVED')`,
		s.tenant, invoice, claim, amount, currency)
}

// TestInvoiceNumberIsUniquePerProviderAndFiscalYear is section 3's first requirement, with the
// application bypassed. Three facts, and the service is allowed to lean on all three.
func TestInvoiceNumberIsUniquePerProviderAndFiscalYear(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h)

	first := s.mustInvoice(t, h, "KPS2026000041", "2026-03-17", "1000")

	// The same number, the same provider, the same fiscal year: refused.
	if _, err := s.insertInvoice(t, h, s.provider, "KPS2026000041", "2026-09-01",
		"500", "0", "500", "DRAFT"); err == nil {
		t.Fatal("a second live invoice reused a number in the provider's fiscal year")
	} else {
		dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "duplicate invoice number")
	}

	// The same number in the *next* fiscal year is a different document. A provider's
	// numbering restarts every year, and refusing that would refuse most of January.
	if _, err := s.insertInvoice(t, h, s.provider, "KPS2026000041", "2027-01-04",
		"500", "0", "500", "DRAFT"); err != nil {
		t.Errorf("the same number in the next fiscal year was refused: %v", err)
	}

	// The same number for another provider is ordinary: two hospitals both issuing
	// "KPS2026000041" is not this index's business.
	if _, err := s.insertInvoice(t, h, s.other, "KPS2026000041", "2026-03-17",
		"500", "0", "500", "DRAFT"); err != nil {
		t.Errorf("two providers could not use the same number: %v", err)
	}

	// A cancelled invoice frees its number, which is how a provider whose own books already
	// carry it can put it on the correction.
	h.AdminExec(`UPDATE billing.invoice SET status = 'CANCELLED' WHERE id = $1`, first)
	if _, err := s.insertInvoice(t, h, s.provider, "KPS2026000041", "2026-03-18",
		"1000", "0", "1000", "DRAFT"); err != nil {
		t.Errorf("a cancelled invoice did not free its number: %v", err)
	}
}

// TestReturnedInvoiceFreesItsNumberForTheCorrection is the other half of 11.12, and the half a
// schema alone cannot finish.
//
// A returned invoice is outside the index, because the correction may carry the number the
// provider's own books already have. What the index still holds is that two *live* documents
// never share one — which is why resubmitting the returned invoice while the correction stands
// is refused, at the transition rather than at the write that would have caused it.
func TestReturnedInvoiceFreesItsNumberForTheCorrection(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h)

	original, err := s.insertInvoice(t, h, s.provider, "KPS2026000077", "2026-03-17",
		"1000", "0", "1000", "SUBMITTED")
	if err != nil {
		t.Fatalf("insert the original: %v", err)
	}
	// While it is live, the number is taken.
	if _, err := s.insertInvoice(t, h, s.provider, "KPS2026000077", "2026-03-18",
		"900", "0", "900", "DRAFT"); err == nil {
		t.Fatal("a live invoice's number was reused")
	}

	h.AdminExec(`UPDATE billing.invoice SET status = 'RETURNED' WHERE id = $1`, original)
	correction, err := s.insertInvoice(t, h, s.provider, "KPS2026000077", "2026-03-18",
		"900", "0", "900", "DRAFT")
	if err != nil {
		t.Fatalf("a returned invoice did not free its number for the correction: %v", err)
	}
	h.AdminExec(`UPDATE billing.invoice SET supersedes_invoice_id = $2 WHERE id = $1`,
		correction, original)

	// Putting the returned one back into a live status while the correction holds the number is
	// refused. Without that, WP-I7-03's reviewer could produce two live documents billing the
	// same number.
	err = h.AdminExecErr(`UPDATE billing.invoice SET status = 'SUBMITTED' WHERE id = $1`, original)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "two live invoices, one number")
}

// TestInvoiceHeaderArithmeticIsTheDatabases is section 3's second requirement, first half.
func TestInvoiceHeaderArithmeticIsTheDatabases(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h)

	// A kuruş out, with no service anywhere near it.
	if _, err := s.insertInvoice(t, h, s.provider, "SUM1", "2026-03-17",
		"1000.10", "200.02", "1200.13", "DRAFT"); err == nil {
		t.Fatal("a header whose halves do not make its total was accepted")
	} else {
		dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "line + tax = payable")
	}
	if _, err := s.insertInvoice(t, h, s.provider, "SUM2", "2026-03-17",
		"1000.10", "200.02", "1200.12", "DRAFT"); err != nil {
		t.Errorf("an exact header was refused: %v", err)
	}

	// The stored fiscal year is the invoice date's year. Without this CHECK the uniqueness
	// rule could be evaded by typing a different year beside the date.
	err := h.AdminExecErr(`
		INSERT INTO billing.invoice (
		    tenant_id, provider_organization_id, invoice_number, invoice_date, fiscal_year,
		    provider_tax_id_hash, line_extension_amount, tax_amount, payable_amount)
		VALUES ($1, $2, 'YEAR1', '2026-03-17', 2025, sha256('x'::bytea), 100, 0, 100)`,
		s.tenant, s.provider)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "fiscal year matches the date")

	// The tax identity is a 32-byte blind index or nothing at all.
	err = h.AdminExecErr(`
		INSERT INTO billing.invoice (
		    tenant_id, provider_organization_id, invoice_number, invoice_date, fiscal_year,
		    provider_tax_id_hash, line_extension_amount, tax_amount, payable_amount)
		VALUES ($1, $2, 'HASH1', '2026-03-17', 2026, decode('4620081341', 'escape'), 100, 0, 100)`,
		s.tenant, s.provider)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "the tax id is a 32-byte hash")
}

// TestAllocationCeilingIsTheDeferredTriggers is section 3's second requirement, second half.
//
// Every refusal below is the database's, with the service bypassed. They are the rules the
// service reports in a form a provider can act on — and the reason it is allowed to report
// them rather than enforce them.
func TestAllocationCeilingIsTheDeferredTriggers(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h)
	invoice := s.mustInvoice(t, h, "ALLOC1", "2026-03-17", "1000")
	approved := s.claim(t, h, "CLM-20260615-APPROVE1", "APPROVED", "600")

	// Less than was approved is ordinary: a provider collecting part of a claim.
	if err := s.allocate(t, h, invoice, approved, "500", "TRY"); err != nil {
		t.Fatalf("an allocation below the approved total was refused: %v", err)
	}
	h.AdminExec(`DELETE FROM billing.invoice_claim WHERE tenant_id = $1`, s.tenant)

	// A kuruş more than was approved is the payer paying for something it never agreed to.
	err := s.allocate(t, h, invoice, approved, "600.01", "TRY")
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "allocation above the approved total")

	// Exactly the approved total is fine, which is what makes the line above a ceiling rather
	// than an off-by-one.
	if err := s.allocate(t, h, invoice, approved, "600", "TRY"); err != nil {
		t.Fatalf("an allocation of exactly the approved total was refused: %v", err)
	}
	h.AdminExec(`DELETE FROM billing.invoice_claim WHERE tenant_id = $1`, s.tenant)

	// An adjustment moves the ceiling, because the approved total is lines *minus*
	// adjustments — the same arithmetic the readiness endpoint performs.
	h.AdminExec(`
		INSERT INTO claim.adjustment (tenant_id, claim_id, version_no, adjustment_type, amount,
		                              payer_amount, member_amount, reason_code, source_type,
		                              created_by)
		VALUES ($1, $2, 1, 'CUT', 100, 100, 0, 'CONTRACT_LIMIT', 'REVIEW', $3)`,
		s.tenant, approved, s.actor)
	err = s.allocate(t, h, invoice, approved, "600", "TRY")
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "the cut lowered the ceiling")
	if err := s.allocate(t, h, invoice, approved, "500", "TRY"); err != nil {
		t.Fatalf("an allocation of the adjusted total was refused: %v", err)
	}
	h.AdminExec(`DELETE FROM billing.invoice_claim WHERE tenant_id = $1`, s.tenant)

	// A claim nobody approved is not billable at any amount.
	draft := s.claim(t, h, "CLM-20260615-DRAFT001", "SUBMITTED", "400")
	err = s.allocate(t, h, invoice, draft, "1", "TRY")
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "allocation on an undecided claim")

	// And an allocation in another currency is not a figure that can be added to this invoice.
	err = s.allocate(t, h, invoice, approved, "100", "EUR")
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "allocation in another currency")
}

// TestOneClaimSitsOnOneLiveInvoice is the index that stops a provider being paid twice, and the
// release that makes a correction possible.
func TestOneClaimSitsOnOneLiveInvoice(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h)
	first := s.mustInvoice(t, h, "LIVE1", "2026-03-17", "600")
	second := s.mustInvoice(t, h, "LIVE2", "2026-03-18", "600")
	claim := s.claim(t, h, "CLM-20260615-LIVE0001", "APPROVED", "600")

	if err := s.allocate(t, h, first, claim, "600", "TRY"); err != nil {
		t.Fatalf("first allocation: %v", err)
	}
	err := s.allocate(t, h, second, claim, "600", "TRY")
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "one claim, one live invoice")

	// Cancelling the first invoice releases its claim. The row stays -- "which claims did this
	// cancelled invoice cover" is a question a dispute asks -- and goes inactive.
	h.AdminExec(`UPDATE billing.invoice SET status = 'CANCELLED' WHERE id = $1`, first)
	ctx, cancel := h.Ctx()
	defer cancel()
	var active bool
	if err := h.Admin.QueryRow(ctx,
		`SELECT active FROM billing.invoice_claim WHERE tenant_id = $1 AND invoice_id = $2`,
		s.tenant, first).Scan(&active); err != nil {
		t.Fatalf("read back the released link: %v", err)
	}
	if active {
		t.Error("a cancelled invoice is still holding its claim")
	}
	if err := s.allocate(t, h, second, claim, "600", "TRY"); err != nil {
		t.Fatalf("the released claim could not be put on the correction: %v", err)
	}
}

// TestSubmittedInvoiceIsFrozen is section 3's fourth requirement, with the service bypassed.
//
// It is the rule the whole package exists for: a submitted invoice's links and figures never
// change. Everything below is refused by a trigger, so a backfill, a psql session and a future
// command are all held to it.
func TestSubmittedInvoiceIsFrozen(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h)
	invoice := s.mustInvoice(t, h, "FREEZE1", "2026-03-17", "600")
	claim := s.claim(t, h, "CLM-20260615-FREEZE01", "APPROVED", "600")
	if err := s.allocate(t, h, invoice, claim, "600", "TRY"); err != nil {
		t.Fatalf("allocate: %v", err)
	}
	h.AdminExec(`UPDATE billing.invoice SET status = 'SUBMITTED', submitted_at = clock_timestamp()
	              WHERE id = $1`, invoice)

	// The amounts.
	err := h.AdminExecErr(`UPDATE billing.invoice SET line_extension_amount = 700,
	                              payable_amount = 700 WHERE id = $1`, invoice)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a submitted invoice's amounts")

	// The number, which is what a payer reconciles against the provider's own books.
	err = h.AdminExecErr(`UPDATE billing.invoice SET invoice_number = 'FREEZE2' WHERE id = $1`, invoice)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a submitted invoice's number")

	// The allocation amount.
	err = h.AdminExecErr(`UPDATE billing.invoice_claim SET allocated_amount = 100
	                       WHERE tenant_id = $1 AND invoice_id = $2`, s.tenant, invoice)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a submitted invoice's allocation")

	// Removing a link, which would be the quietest way of all to change what an invoice says.
	err = h.AdminExecErr(`DELETE FROM billing.invoice_claim
	                       WHERE tenant_id = $1 AND invoice_id = $2`, s.tenant, invoice)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "removing a submitted link")

	// Adding one.
	second := s.claim(t, h, "CLM-20260615-FREEZE02", "APPROVED", "100")
	err = s.allocate(t, h, invoice, second, "100", "TRY")
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "adding a link to a submitted invoice")

	// What may still move: the lifecycle, and the batch WP-I7-03 will put it in.
	h.AdminExec(`UPDATE billing.invoice SET status = 'APPROVED' WHERE id = $1`, invoice)
	h.AdminExec(`UPDATE billing.invoice SET batch_id = $2 WHERE id = $1`, invoice, uuid.New())
}

// TestClaimApprovedTotalIsLinesMinusAdjustments pins the SQL function the ceiling trigger uses
// against the arithmetic WP-I7-01 performs in Go. Two implementations of one figure is how two
// systems come to disagree about what a provider is owed.
func TestClaimApprovedTotalIsLinesMinusAdjustments(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h)
	claim := s.claim(t, h, "CLM-20260615-TOTAL001", "APPROVED", "449.99")
	ctx, cancel := h.Ctx()
	defer cancel()

	total := func() string {
		t.Helper()
		var out string
		if err := h.Admin.QueryRow(ctx,
			`SELECT trim_scale(billing.claim_approved_total($1, $2))::text`,
			s.tenant, claim).Scan(&out); err != nil {
			t.Fatalf("approved total: %v", err)
		}
		return out
	}
	if got := total(); got != "449.99" {
		t.Errorf("approved total = %s, want the line decision 449.99", got)
	}

	// A cut takes money off; its reversal puts exactly that money back. Not to within a
	// kuruş -- exactly, because every figure is an exact decimal that never became a float.
	var cut uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO claim.adjustment (tenant_id, claim_id, version_no, adjustment_type, amount,
		                              payer_amount, member_amount, reason_code, source_type,
		                              created_by)
		VALUES ($1, $2, 1, 'CUT', 49.99, 49.99, 0, 'CONTRACT_LIMIT', 'REVIEW', $3)
		RETURNING id`, s.tenant, claim, s.actor).Scan(&cut); err != nil {
		t.Fatalf("insert cut: %v", err)
	}
	if got := total(); got != "400" {
		t.Errorf("approved total after the cut = %s, want 400", got)
	}
	h.AdminExec(`
		INSERT INTO claim.adjustment (tenant_id, claim_id, version_no, adjustment_type, amount,
		                              payer_amount, member_amount, reason_code, source_type,
		                              reverses_adjustment_id, created_by)
		VALUES ($1, $2, 1, 'REVERSAL', -49.99, -49.99, 0, 'CONTRACT_LIMIT', 'REVIEW', $3, $4)`,
		s.tenant, claim, cut, s.actor)
	if got := total(); got != "449.99" {
		t.Errorf("approved total after the reversal = %s, want exactly 449.99 again", got)
	}
}

// TestInvoicePermissionsAreSeededAndGrantable checks both halves of the same fact: the
// catalogue rows of migration 000008 and the role templates in
// internal/identity/application/roles.go have to agree, because a permission that exists in one
// and not the other is a permission nobody can hold or one nobody can be given.
//
// The sponsor's HR user is the row that matters. WP-I7-02 §2.3 gives it `invoice.read` -- it
// reads the headers and the totals of the invoices raised against its members' claims -- and
// deliberately not `health.clinical.read`, which is what makes "and never a claim line
// description" a property of the grant rather than of a mapper.
func TestInvoicePermissionsAreSeededAndGrantable(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	granted := map[string][]string{}
	byRole := map[string]map[string]bool{}
	for _, tpl := range identityapp.RoleTemplates() {
		byRole[tpl.Code] = map[string]bool{}
		for _, code := range tpl.Permissions {
			granted[code] = append(granted[code], tpl.Code)
			byRole[tpl.Code][code] = true
		}
	}

	for code, wantSensitivity := range map[string]string{
		"invoice.read":   "NORMAL",
		"invoice.manage": "NORMAL",
	} {
		var n int
		if err := h.Admin.QueryRow(ctx,
			`SELECT count(*) FROM iam.permission WHERE code = $1`, code).Scan(&n); err != nil {
			t.Fatalf("read permission %s: %v", code, err)
		}
		if n != 1 {
			t.Fatalf("permission %s is seeded %d times, want once", code, n)
		}
		var sensitivity string
		if err := h.Admin.QueryRow(ctx,
			`SELECT sensitivity FROM iam.permission WHERE code = $1`, code).Scan(&sensitivity); err != nil {
			t.Fatalf("read sensitivity of %s: %v", code, err)
		}
		if sensitivity != wantSensitivity {
			t.Errorf("permission %s sensitivity = %s, want %s", code, sensitivity, wantSensitivity)
		}
		if len(granted[code]) == 0 {
			t.Errorf("permission %s is in the catalogue but in no role template", code)
		}
	}

	for _, want := range []struct{ role, permission string }{
		{"PROVIDER_BILLING", "invoice.read"},
		{"PROVIDER_BILLING", "invoice.manage"},
		{"FINANCIAL_REVIEWER", "invoice.read"},
		{"FINANCIAL_REVIEWER", "invoice.manage"},
		{"SPONSOR_HR", "invoice.read"},
	} {
		if !byRole[want.role][want.permission] {
			t.Errorf("role %s does not hold %s", want.role, want.permission)
		}
	}

	// The pairing the projection rests on. A grant quietly added here would defeat WP-I7-02
	// §2.3 without a single line of the billing package changing.
	if byRole["SPONSOR_HR"]["health.clinical.read"] {
		t.Error("SPONSOR_HR must never hold health.clinical.read")
	}
	// Whoever may raise an invoice may read one: a role that could submit and not read would
	// have no way to check what it sent.
	for _, tpl := range identityapp.RoleTemplates() {
		if byRole[tpl.Code]["invoice.manage"] && !byRole[tpl.Code]["invoice.read"] {
			t.Errorf("role %s may manage invoices but not read one", tpl.Code)
		}
	}
}
