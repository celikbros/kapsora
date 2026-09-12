package main

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	accommodationapp "github.com/celikbros/kapsora/internal/accommodation/application"
	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	claimapp "github.com/celikbros/kapsora/internal/claim/application"
	documentapp "github.com/celikbros/kapsora/internal/document/application"
	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	identitydomain "github.com/celikbros/kapsora/internal/identity/domain"
	identitypg "github.com/celikbros/kapsora/internal/identity/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/platform/crypto/localkey"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/objectstore"
	reportapp "github.com/celikbros/kapsora/internal/report/application"
)

// The whole of `seed demo`, run twice against a throw-away database.
//
// It is one test rather than ten because what it is checking is one thing: that the six
// scenarios of scripts/demo/KAPSORA-Demo-Rehberi.html can be walked against the real API,
// starting from exactly the states the guide says are already true when each one begins. A test
// per state would pay the cost of building the world ten times and would still not answer the
// question, which is whether the world holds together.
//
// The object store is the in-memory implementation of the same port MinIO implements, which is
// what lets the document steps — the scan of an invoice and the member's receipt — run here at
// all.

// testMasterKey is the local cipher's key in this test. It is a constant so a run is
// deterministic; the database it protects is created and dropped by the harness.
var testMasterKey = []byte{
	0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
	0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f,
	0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17,
	0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f,
}

// newDemoSeeder builds the seeder `seed demo` builds, against the harness's application pool.
func newDemoSeeder(t *testing.T, pool *pgxpool.Pool) *seeder {
	t.Helper()
	keys, err := localkey.New(testMasterKey)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(discardWriter{}, nil))
	sink := identitypg.NewAuditSink(pool, auditpg.New(), logger)
	credentials := identitypg.NewCredentialRepository(pool)
	svc, err := identityapp.New(identityapp.Deps{
		Credentials: credentials,
		Sessions:    identitypg.NewSessionStore(pool),
		Audit:       sink,
		Policy:      identitydomain.DefaultPolicy(),
		Lockout:     identitydomain.DefaultLockout(),
	})
	if err != nil {
		t.Fatalf("identity service: %v", err)
	}
	clock := &seedClock{at: time.Now().UTC()}
	deps := seedDeps{
		Pool: pool, Keys: keys, Store: objectstore.NewMemory(), Logger: logger, Clock: clock,
		Storage: documentapp.Storage{
			QuarantineBucket: "quarantine", SecureBucket: "secure",
			UploadTTL: 15 * time.Minute, DownloadTTL: 5 * time.Minute,
			EncryptionKeyRef: "objectstore:test",
		},
	}
	catalogSvc, notificationSvc, benefitSvc, partySvc, err := newReferenceServices(deps)
	if err != nil {
		t.Fatalf("reference services: %v", err)
	}
	biz, err := newVerticals(deps)
	if err != nil {
		t.Fatalf("business verticals: %v", err)
	}
	return &seeder{
		pool: pool, svc: svc, credentials: credentials,
		provisioner:   identityapp.NewProvisioner(identitypg.NewProvisioningRepository(pool), sink),
		catalog:       catalogSvc,
		notifications: notificationSvc,
		benefits:      benefitSvc,
		party:         partySvc,
		keys:          keys,
		biz:           biz,
		clock:         clock,
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// TestSeedDemoBuildsTheGuideScenariosAndIsIdempotent runs the whole of `seed demo` twice and
// checks, after each pass, that every state the demo guide opens on is there — and that the
// second pass added nothing.
func TestSeedDemoBuildsTheGuideScenariosAndIsIdempotent(t *testing.T) {
	h := dbtest.New(t)
	t.Setenv("KAPSORA_SEED_DEMO_PASSWORD", "demo parola 2026 kapsora")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var first map[string]int
	for pass := 1; pass <= 2; pass++ {
		// A fresh seeder per pass, exactly as two runs of the binary would be: a pass that
		// reused an in-process cache would prove nothing about the second run somebody
		// actually makes.
		s := newDemoSeeder(t, h.App)
		if err := s.demo(ctx); err != nil {
			t.Fatalf("pass %d: seed demo: %v", pass, err)
		}
		assertDemoAccounts(t, h)
		assertStaffMember(t, h)
		assertDemoScenarioStates(t, h, s)
		counts := demoRowCounts(t, h)
		if pass == 1 {
			first = counts
			continue
		}
		for what, n := range counts {
			if first[what] != n {
				t.Errorf("%s: %d rows after one run, %d after two — the seed is not idempotent",
					what, first[what], n)
			}
		}
	}

	// And a run on another day. The icmal periods no longer land where the first run put them
	// and the invoices are already in the icmals it opened, which is exactly the failure a demo
	// database met the morning after it was seeded: the step tried to open a second icmal for an
	// invoice that already had one. Nothing new may be opened, and nothing may fail.
	later := newDemoSeeder(t, h.App)
	later.nowFn = func() time.Time { return time.Now().AddDate(0, 1, 5) }
	if err := later.demo(ctx); err != nil {
		t.Fatalf("seed demo on a later day: %v", err)
	}
	after := demoRowCounts(t, h)
	for _, what := range []string{"billing.batch", "billing.batch_invoice", "billing.invoice", "billing.settlement"} {
		if after[what] != first[what] {
			t.Errorf("%s: %d rows after one run, %d after a run on another day", what, first[what], after[what])
		}
	}
}

// assertDemoAccounts checks that every account the guide names exists in DEMO_A with the role
// and the scope the mock world grants the same username.
func assertDemoAccounts(t *testing.T, h *dbtest.Harness) {
	t.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()

	var tenantA, hospital, hotel uuid.UUID
	if err := h.Admin.QueryRow(ctx,
		`SELECT id FROM platform.tenant WHERE code = 'DEMO_A'`).Scan(&tenantA); err != nil {
		t.Fatalf("find DEMO_A: %v", err)
	}
	scopeOf := func(code string) uuid.UUID {
		var id uuid.UUID
		if err := h.Admin.QueryRow(ctx,
			`SELECT id FROM directory.tenant_organization WHERE tenant_id = $1 AND tenant_code = $2`,
			tenantA, code).Scan(&id); err != nil {
			t.Fatalf("find organization %s: %v", code, err)
		}
		return id
	}
	hospital = scopeOf(hospitalCode)
	hotel = scopeOf(hotelCode)

	cases := []struct {
		username, role, scopeType string
		scopeID                   uuid.UUID
	}{
		{"financial.reviewer", "FINANCIAL_REVIEWER", "TENANT", uuid.Nil},
		{"payer.approver", "PAYER_APPROVER", "TENANT", uuid.Nil},
		{"doctor.a", "MEDICAL_REVIEWER", "TENANT", uuid.Nil},
		{"sponsor.hr", "SPONSOR_HR", "TENANT", uuid.Nil},
		{"billing.a", "PROVIDER_BILLING", "ORGANIZATION", hospital},
		{"reservation.a", "PROVIDER_RESERVATION", "ORGANIZATION", hotel},
		// The four that were already there, checked so a refactor cannot quietly drop one.
		{"admin.a", "TENANT_ADMIN", "TENANT", uuid.Nil},
		{"reviewer.a", "FINANCIAL_REVIEWER", "TENANT", uuid.Nil},
		{"provider.a", "PROVIDER_STAFF", "ORGANIZATION", hospital},
	}
	for _, c := range cases {
		var n int
		var scope any
		if c.scopeID != uuid.Nil {
			scope = c.scopeID
		}
		err := h.Admin.QueryRow(ctx, `
			SELECT count(*)
			  FROM iam.access_grant g
			  JOIN iam.role r ON r.tenant_id = g.tenant_id AND r.id = g.role_id
			  JOIN iam.tenant_membership m ON m.tenant_id = g.tenant_id AND m.id = g.tenant_membership_id
			  JOIN iam.actor a ON a.id = m.actor_id
			 WHERE g.tenant_id = $1 AND r.code = $2 AND a.identity_subject = $3
			   AND g.scope_type = $4 AND g.scope_id IS NOT DISTINCT FROM $5::uuid`,
			tenantA, c.role, c.username, c.scopeType, scope).Scan(&n)
		if err != nil {
			t.Fatalf("read the grant of %s: %v", c.username, err)
		}
		if n != 1 {
			t.Errorf("%s holds %s %s %d times, want once", c.username, c.role, c.scopeType, n)
		}
	}

	// The member's binding is a PERSON grant and nothing else, which is what every member
	// command resolves the person from.
	var personGrants int
	err := h.Admin.QueryRow(ctx, `
		SELECT count(*)
		  FROM iam.access_grant g
		  JOIN iam.role r ON r.tenant_id = g.tenant_id AND r.id = g.role_id
		  JOIN iam.tenant_membership m ON m.tenant_id = g.tenant_id AND m.id = g.tenant_membership_id
		  JOIN iam.actor a ON a.id = m.actor_id
		 WHERE g.tenant_id = $1 AND r.code = 'MEMBER' AND a.identity_subject = 'member.a'
		   AND g.scope_type = 'PERSON' AND g.scope_id IS NOT NULL`, tenantA).Scan(&personGrants)
	if err != nil {
		t.Fatalf("read the member binding: %v", err)
	}
	if personGrants != 1 {
		t.Errorf("member.a has %d PERSON grants, want one", personGrants)
	}

	// And every demo organization carries a tax identity, because an invoice may not be raised
	// against a provider that has none.
	for _, code := range []string{sponsorCode, payerCode, hospitalCode, hotelCode} {
		var withTax int
		if err := h.Admin.QueryRow(ctx, `
			SELECT count(*)
			  FROM directory.tenant_organization t
			  JOIN directory.organization o ON o.id = t.organization_id
			 WHERE t.tenant_id = $1 AND t.tenant_code = $2
			   AND o.tax_number_hash IS NOT NULL AND o.tax_number_cipher IS NOT NULL`,
			tenantA, code).Scan(&withTax); err != nil {
			t.Fatalf("read the tax identity of %s: %v", code, err)
		}
		if withTax != 1 {
			t.Errorf("%s has no tax identity", code)
		}
	}
}

// assertDemoScenarioStates checks the starting state of each of the guide's six scenarios.
func assertDemoScenarioStates(t *testing.T, h *dbtest.Harness, s *seeder) { //nolint:funlen // one list of states
	t.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	var tenantA uuid.UUID
	if err := h.Admin.QueryRow(ctx,
		`SELECT id FROM platform.tenant WHERE code = 'DEMO_A'`).Scan(&tenantA); err != nil {
		t.Fatalf("find DEMO_A: %v", err)
	}
	scalar := func(what, sql string, args ...any) string {
		t.Helper()
		var out string
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		return out
	}
	count := func(what, sql string, args ...any) int {
		t.Helper()
		var out int
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		return out
	}

	// Scenario 1. A DRAFT invoice whose allocations do not add up to its header, so the
	// provider's "Gönder" is refused and says why; and a DRAFT icmal carrying one SUBMITTED
	// invoice, so "İcmali gönder" has something to send.
	status := scalar("the mismatched draft", `
		SELECT status FROM billing.invoice WHERE tenant_id = $1 AND invoice_number = $2`,
		tenantA, invoiceMismatched)
	if status != "DRAFT" {
		t.Errorf("scenario 1: invoice %s is %s, want DRAFT", invoiceMismatched, status)
	}
	gap := scalar("the allocation gap", `
		SELECT trim_scale(i.payable_amount
		       - COALESCE((SELECT sum(ic.allocated_amount) FROM billing.invoice_claim ic
		                    WHERE ic.tenant_id = i.tenant_id AND ic.invoice_id = i.id
		                      AND ic.active), 0))::text
		  FROM billing.invoice i WHERE i.tenant_id = $1 AND i.invoice_number = $2`,
		tenantA, invoiceMismatched)
	if gap == "0" {
		t.Errorf("scenario 1: invoice %s adds up; the refusal step has nothing to refuse",
			invoiceMismatched)
	}
	draftBatches := count("the draft icmal", `
		SELECT count(*) FROM billing.batch b
		 WHERE b.tenant_id = $1 AND b.status = 'DRAFT'
		   AND EXISTS (SELECT 1 FROM billing.batch_invoice bi
		                WHERE bi.tenant_id = b.tenant_id AND bi.batch_id = b.id AND bi.active)`,
		tenantA)
	if draftBatches != 1 {
		t.Errorf("scenario 1: %d draft icmals carrying an invoice, want one", draftBatches)
	}

	// Scenario 2. An icmal UNDER_REVIEW with exactly one invoice still undecided.
	var reviewBatch uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		SELECT id FROM billing.batch WHERE tenant_id = $1 AND status = 'UNDER_REVIEW'`,
		tenantA).Scan(&reviewBatch); err != nil {
		t.Fatalf("scenario 2: find the icmal under review: %v", err)
	}
	pending := count("the undecided invoices", `
		SELECT count(*) FROM billing.batch_invoice
		 WHERE tenant_id = $1 AND batch_id = $2 AND decision IS NULL`, tenantA, reviewBatch)
	if pending != 1 {
		t.Errorf("scenario 2: the icmal under review has %d undecided invoices, want exactly one",
			pending)
	}
	decided := count("the decided invoices", `
		SELECT count(*) FROM billing.batch_invoice
		 WHERE tenant_id = $1 AND batch_id = $2 AND decision IS NOT NULL`, tenantA, reviewBatch)
	if decided < 1 {
		t.Errorf("scenario 2: the icmal under review has no decided invoice beside the pending one")
	}
	// The one still waiting has to be worth more than the 500 the guide cuts off it, or the
	// operator's second step is refused.
	pendingNumber := scalar("the invoice still to decide", `
		SELECT i.invoice_number FROM billing.batch_invoice bi
		  JOIN billing.invoice i ON i.tenant_id = bi.tenant_id AND i.id = bi.invoice_id
		 WHERE bi.tenant_id = $1 AND bi.batch_id = $2 AND bi.decision IS NULL`,
		tenantA, reviewBatch)
	if pendingNumber != invoiceUndecided {
		t.Errorf("scenario 2: %s is the invoice still to decide, want %s", pendingNumber,
			invoiceUndecided)
	}
	pendingAmount := scalar("what the pending invoice bills", `
		SELECT trim_scale(payable_amount)::text FROM billing.invoice
		 WHERE tenant_id = $1 AND invoice_number = $2`, tenantA, invoiceUndecided)
	if pendingAmount != amountUndecided {
		t.Errorf("scenario 2: the pending invoice bills %s, want %s", pendingAmount, amountUndecided)
	}

	// Scenario 3. A settlement waiting for the approver, with a payable amount to release.
	waiting := count("the settlement waiting for approval", `
		SELECT count(*) FROM billing.settlement
		 WHERE tenant_id = $1 AND status = 'PENDING_APPROVAL' AND payable_amount > 0`, tenantA)
	if waiting != 1 {
		t.Errorf("scenario 3: %d settlements are PENDING_APPROVAL, want one", waiting)
	}

	// Scenario 4. A SUBMITTED reimbursement of the bound member, with a clean receipt, a masked
	// account and nothing in the clear.
	var refundStatus, masked string
	var receiptScan string
	err := h.Admin.QueryRow(ctx, `
		SELECT r.status, r.bank_account_masked, o.scan_status
		  FROM billing.reimbursement r
		  JOIN document.object o ON o.tenant_id = r.tenant_id AND o.id = r.receipt_document_id
		 WHERE r.tenant_id = $1`, tenantA).Scan(&refundStatus, &masked, &receiptScan)
	if err != nil {
		t.Fatalf("scenario 4: read the reimbursement: %v", err)
	}
	if refundStatus != "SUBMITTED" {
		t.Errorf("scenario 4: the reimbursement is %s, want SUBMITTED", refundStatus)
	}
	if receiptScan != "CLEAN" {
		t.Errorf("scenario 4: the receipt is %s, want CLEAN", receiptScan)
	}
	if len(masked) != 4 {
		t.Errorf("scenario 4: the account mask is %q; only the last four digits may be stored", masked)
	}
	plain := count("the IBAN sweep", `
		SELECT count(*) FROM billing.reimbursement
		 WHERE tenant_id = $1 AND bank_account_ref_enc IS NULL`, tenantA)
	if plain != 0 {
		t.Errorf("scenario 4: a reimbursement carries no enciphered account reference")
	}

	// Scenario 5. Open allotment reaching well past the month out the guide asks for.
	horizon := count("the open allotment", `
		SELECT count(*) FROM accommodation.inventory_day i
		 WHERE i.tenant_id = $1 AND i.stay_date >= (current_date + 90)
		   AND i.capacity - i.held - i.confirmed > 0`, tenantA)
	if horizon == 0 {
		t.Errorf("scenario 5: no room is on sale ninety days out; the member's search finds nothing")
	}
	assertMemberFindsARoom(t, h, s, tenantA)
	lodging := count("the lodging terms", `
		SELECT count(*) FROM contract.lodging_terms lt
		  JOIN contract.contract_version v ON v.tenant_id = lt.tenant_id AND v.id = lt.contract_version_id
		 WHERE lt.tenant_id = $1 AND v.status = 'PUBLISHED'`, tenantA)
	if lodging != 1 {
		t.Errorf("scenario 5: %d published contract versions carry lodging terms, want one", lodging)
	}

	// Scenario 6. At least one reconciliation run that balanced and one that found a real
	// difference, so the dashboard and the mutabakat screen have non-zero figures.
	balanced := count("the balanced runs", `
		SELECT count(*) FROM billing.reconciliation_run
		 WHERE tenant_id = $1 AND status = 'BALANCED'`, tenantA)
	differing := count("the differing runs", `
		SELECT count(*) FROM billing.reconciliation_run
		 WHERE tenant_id = $1 AND status = 'DIFFERENCES' AND difference_count > 0`, tenantA)
	if balanced == 0 {
		t.Errorf("scenario 6: no reconciliation run balanced")
	}
	if differing == 0 {
		t.Errorf("scenario 6: no reconciliation run found a difference")
	}
	assertOperatorScreensAreNotEmpty(t, h, s, tenantA)
}

// assertOperatorScreensAreNotEmpty walks the two reads the guide opens its first and its last
// scenario on: the provider's earnings figure and the payer's morning dashboard. Both are
// aggregates over everything above, so an empty one means the world was built and does not add
// up — which a table-by-table assertion would never catch.
func assertOperatorScreensAreNotEmpty(t *testing.T, h *dbtest.Harness, s *seeder,
	tenantA uuid.UUID,
) {
	t.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	var hospital uuid.UUID
	if err := h.Admin.QueryRow(ctx,
		`SELECT id FROM directory.tenant_organization WHERE tenant_id = $1 AND tenant_code = $2`,
		tenantA, hospitalCode).Scan(&hospital); err != nil {
		t.Fatalf("find the demo hospital: %v", err)
	}
	var billingActor uuid.UUID
	if err := h.Admin.QueryRow(ctx,
		`SELECT id FROM iam.actor WHERE identity_subject = 'billing.a'`).Scan(&billingActor); err != nil {
		t.Fatalf("find billing.a: %v", err)
	}

	// Scenario 1 step 1: "Dönemin onaylı ve faturalanabilir tutarını görürsünüz."
	earnings, err := s.biz.claims.ProviderEarnings(ctx,
		rcOrganization(tenantA, billingActor, hospital, claimapp.PermissionRead), hospital,
		claimapp.EarningsFilter{})
	if err != nil {
		t.Fatalf("scenario 1: read the provider's earnings: %v", err)
	}
	invoiceable := ""
	for _, bucket := range earnings.Currencies {
		if bucket.CurrencyCode == "TRY" {
			invoiceable = bucket.InvoiceableTotal
		}
	}
	if invoiceable == "" || invoiceable == "0" {
		t.Errorf("scenario 1: the provider's earnings screen shows %q invoiceable; "+
			"there is nothing for the operator to raise an invoice against", invoiceable)
	}

	// Scenario 6 step 1: the operations dashboard. Three of its figures are what the other
	// scenarios left behind, and all three have to be non-zero for the screen to be readable.
	var reviewerActor uuid.UUID
	if err := h.Admin.QueryRow(ctx,
		`SELECT id FROM iam.actor WHERE identity_subject = 'financial.reviewer'`).
		Scan(&reviewerActor); err != nil {
		t.Fatalf("find financial.reviewer: %v", err)
	}
	dashboard, err := s.biz.reports.Dashboard(ctx,
		rcTenant(tenantA, reviewerActor, reportapp.PermissionRead))
	if err != nil {
		t.Fatalf("scenario 6: read the dashboard: %v", err)
	}
	if dashboard.Batches.BatchCount == 0 {
		t.Errorf("scenario 6: the dashboard shows no icmal waiting for a decision")
	}
	if dashboard.Settlements.DueSoonCount+dashboard.Settlements.OverdueCount == 0 {
		t.Errorf("scenario 6: the dashboard shows no settlement due")
	}
	if dashboard.Reimbursements.ReimbursementCount == 0 {
		t.Errorf("scenario 6: the dashboard shows no reimbursement waiting for a decision")
	}
}

// demoRowCounts is what a second run must not change. Every table here is one the seed writes
// to, so a step that created instead of finding shows up as a number that moved.
func demoRowCounts(t *testing.T, h *dbtest.Harness) map[string]int {
	t.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	tables := []string{
		"iam.actor", "iam.access_grant", "directory.tenant_organization",
		"provider.provider_profile", "contract.contract", "contract.contract_version",
		"contract.price_item", "benefit.program", "benefit.plan", "benefit.plan_version",
		"benefit.entitlement_definition", "benefit.entitlement_account", "benefit.enrollment",
		"party.person", "party.sponsor_membership", "catalog.service_definition",
		"claim.claim", "billing.invoice", "billing.invoice_claim", "billing.batch",
		"billing.batch_invoice", "billing.settlement", "billing.payment_record",
		"billing.reimbursement", "billing.reconciliation_run", "document.object",
		"accommodation.property", "accommodation.room_type", "accommodation.inventory_day",
		"iam.credential", "notification.template", "catalog.code_value",
		"workflow.work_queue",
		"health.health_case", "service.service_request",
		"health.medical_report", "health.medical_report_service", "document.link",
		"workflow.work_item",
	}
	out := make(map[string]int, len(tables))
	for _, table := range tables {
		var n int
		if err := h.Admin.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		out[table] = n
	}
	return out
}

// assertMemberFindsARoom walks the search of the guide's fifth scenario: a stay one month out,
// three nights, through the same command the member app calls.
//
// It is the only assertion in this file that is not a read of a table, and it is worth the
// difference: a row in `accommodation.inventory_day` proves an allotment was written, and this
// proves the member is shown a room they may have — priced from the hotel's own published
// contract and drawn against their own entitlement.
func assertMemberFindsARoom(t *testing.T, h *dbtest.Harness, s *seeder, tenantA uuid.UUID) {
	t.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	var personID, memberActor uuid.UUID
	err := h.Admin.QueryRow(ctx, `
		SELECT g.scope_id, a.id
		  FROM iam.access_grant g
		  JOIN iam.role r ON r.tenant_id = g.tenant_id AND r.id = g.role_id
		  JOIN iam.tenant_membership m ON m.tenant_id = g.tenant_id AND m.id = g.tenant_membership_id
		  JOIN iam.actor a ON a.id = m.actor_id
		 WHERE g.tenant_id = $1 AND r.code = 'MEMBER' AND g.scope_type = 'PERSON'
		   AND a.identity_subject = $2`,
		tenantA, demoMemberUsername).Scan(&personID, &memberActor)
	if err != nil {
		t.Fatalf("scenario 5: find the bound member: %v", err)
	}
	rc := rcPerson(tenantA, memberActor, personID,
		accommodationapp.PermissionRead, "eligibility.check")
	checkIn := day(time.Now()).AddDate(0, 1, 0)
	// The search takes a place: either one property or one region, never both and never
	// neither. The guide's own step is "bir yer seçin", so the region the demo hotel sits in is
	// what this asks for.
	result, err := s.biz.accommodation.SearchAvailability(ctx, rc, accommodationapp.SearchInput{
		PersonID: personID, CheckIn: checkIn, CheckOut: checkIn.AddDate(0, 0, 3), Adults: 2,
		RegionCode: "TR-07",
	})
	if err != nil {
		t.Fatalf("scenario 5: search a month out for three nights: %s", describeValidation(err))
	}
	if len(result.Items) == 0 {
		t.Fatalf("scenario 5: the member's search a month out finds no room at all")
	}
	if !result.Eligible {
		t.Errorf("scenario 5: the member is not eligible for the stay the guide asks them to book")
	}
	if result.Nights != 3 {
		t.Errorf("scenario 5: the search covered %d nights, want 3", result.Nights)
	}
}
