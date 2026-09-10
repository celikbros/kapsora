package dbtests

import (
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// The icmal schema of migration 000045 (WP-I7-03), with the application layer bypassed
// entirely: every statement below runs as the schema owner through the admin pool, so no Go
// code of ours is between them and the constraint.
//
// These are the rules the service is allowed to lean on, and a rule the service can forget is
// not a rule. Four of them carry the package: a submitted batch's membership and amounts never
// move, one invoice sits in one live batch, a decision is tied to the amount it approves, and
// the decided totals reconcile to the submitted total.

// batchSeed is a billingSeed with an icmal and two submitted invoices in it.
type batchSeed struct {
	billingSeed
	batch    uuid.UUID
	firstID  uuid.UUID
	secondID uuid.UUID
}

// seedBatch writes one DRAFT icmal over two submitted invoices, directly.
func (s billingSeed) seedBatch(t *testing.T, h *dbtest.Harness) batchSeed {
	t.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	out := batchSeed{billingSeed: s}

	first, err := s.insertInvoice(t, h, s.provider, "ICM000001", "2026-03-17",
		"1000", "0", "1000", "SUBMITTED")
	if err != nil {
		t.Fatalf("insert the first invoice: %v", err)
	}
	second, err := s.insertInvoice(t, h, s.provider, "ICM000002", "2026-03-18",
		"500", "0", "500", "SUBMITTED")
	if err != nil {
		t.Fatalf("insert the second invoice: %v", err)
	}
	out.firstID, out.secondID = first, second

	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO billing.batch (tenant_id, reference, provider_organization_id, domain_code,
		                           currency_code, period_from, period_to)
		VALUES ($1, $2, $3, 'GENERIC', 'TRY', '2026-03-01', '2026-03-31') RETURNING id`,
		s.tenant, "IC-202603-"+batchTail(), s.provider).Scan(&out.batch); err != nil {
		t.Fatalf("insert the batch: %v", err)
	}
	out.member(t, h, out.batch, first, "1000")
	out.member(t, h, out.batch, second, "500")
	return out
}

// member links one invoice to one batch, returning the error so a test can assert the freeze
// rather than only that the happy path works.
func (s batchSeed) member(t *testing.T, h *dbtest.Harness, batch, invoice uuid.UUID,
	amount string,
) {
	t.Helper()
	if err := s.memberErr(h, batch, invoice, amount); err != nil {
		t.Fatalf("link invoice %s to batch %s: %v", invoice, batch, err)
	}
}

func (s batchSeed) memberErr(h *dbtest.Harness, batch, invoice uuid.UUID, amount string) error {
	return h.AdminExecErr(`
		INSERT INTO billing.batch_invoice (tenant_id, batch_id, invoice_id, submitted_amount)
		VALUES ($1, $2, $3, $4::text::numeric)`, s.tenant, batch, invoice, amount)
}

// submit moves the batch out of DRAFT the way the service does, so the freeze is in force.
func (s batchSeed) submit(t *testing.T, h *dbtest.Harness) {
	t.Helper()
	h.AdminExec(`
		UPDATE billing.batch
		   SET status = 'SUBMITTED', submitted_at = clock_timestamp(), submitted_by = $2,
		       invoice_count = 2, submitted_total = 1500
		 WHERE tenant_id = $1 AND id = $3`, s.tenant, s.actor, s.batch)
}

// underReview is the state decisions may be recorded in.
func (s batchSeed) underReview(t *testing.T, h *dbtest.Harness) {
	t.Helper()
	s.submit(t, h)
	h.AdminExec(`UPDATE billing.batch SET status = 'UNDER_REVIEW' WHERE tenant_id = $1 AND id = $2`,
		s.tenant, s.batch)
}

// decide records one decision directly, returning the error so a CHECK can be asserted.
func (s batchSeed) decide(h *dbtest.Harness, invoice uuid.UUID, decision, approved,
	reason string,
) error {
	var reasonArg any
	if reason != "" {
		reasonArg = reason
	}
	return h.AdminExecErr(`
		UPDATE billing.batch_invoice
		   SET decision = $3, approved_amount = $4::text::numeric, reason_code = $5,
		       decided_by = $6, decided_at = clock_timestamp()
		 WHERE tenant_id = $1 AND batch_id = $2 AND invoice_id = $7`,
		s.tenant, s.batch, decision, approved, reasonArg, s.actor, invoice)
}

// batchTail is eight characters of the reference's random half, in the alphabet the column
// CHECK allows. It is derived from a fresh uuid rather than from a counter so two seeds in one
// test never collide.
func batchTail() string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	raw := uuid.New()
	out := make([]byte, 8)
	for i := range out {
		out[i] = alphabet[raw[i]%32]
	}
	return string(out)
}

// TestSubmittedBatchMembershipIsFrozen is section 3's first requirement, with the application
// bypassed: an insert, a delete and an amount change are all refused once the batch has left
// DRAFT.
func TestSubmittedBatchMembershipIsFrozen(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h).seedBatch(t, h)

	third, err := s.insertInvoice(t, h, s.provider, "ICM000003", "2026-03-19",
		"250", "0", "250", "SUBMITTED")
	if err != nil {
		t.Fatalf("insert the third invoice: %v", err)
	}
	// While it is a draft, all three are ordinary.
	s.member(t, h, s.batch, third, "250")
	h.AdminExec(`UPDATE billing.batch_invoice SET submitted_amount = 260
	              WHERE tenant_id = $1 AND batch_id = $2 AND invoice_id = $3`,
		s.tenant, s.batch, third)
	h.AdminExec(`DELETE FROM billing.batch_invoice
	              WHERE tenant_id = $1 AND batch_id = $2 AND invoice_id = $3`,
		s.tenant, s.batch, third)

	s.submit(t, h)

	// An insert.
	err = s.memberErr(h, s.batch, third, "250")
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "an insert into a submitted batch")

	// An amount change.
	err = h.AdminExecErr(`UPDATE billing.batch_invoice SET submitted_amount = 1
	                       WHERE tenant_id = $1 AND batch_id = $2 AND invoice_id = $3`,
		s.tenant, s.batch, s.firstID)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "an amount changed after submit")

	// And a delete.
	err = h.AdminExecErr(`DELETE FROM billing.batch_invoice
	                       WHERE tenant_id = $1 AND batch_id = $2 AND invoice_id = $3`,
		s.tenant, s.batch, s.firstID)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a delete after submit")

	// The membership is exactly what it was.
	ctx, cancel := h.Ctx()
	defer cancel()
	var count int
	var total string
	if err := h.Admin.QueryRow(ctx, `
		SELECT count(*), trim_scale(sum(submitted_amount))::text
		  FROM billing.batch_invoice WHERE tenant_id = $1 AND batch_id = $2`,
		s.tenant, s.batch).Scan(&count, &total); err != nil {
		t.Fatalf("read the membership back: %v", err)
	}
	if count != 2 || total != "1500" {
		t.Fatalf("the frozen batch holds %d invoices worth %s, want 2 worth 1500", count, total)
	}
}

// TestDecisionsMoveOnlyWhileUnderReview is the other half of the freeze: the decision columns
// are the only ones that move, and only in the one status where a reviewer is looking.
func TestDecisionsMoveOnlyWhileUnderReview(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h).seedBatch(t, h)
	s.submit(t, h)

	// SUBMITTED: nobody has opened it yet.
	err := s.decide(h, s.firstID, "APPROVE", "1000", "")
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a decision on a SUBMITTED batch")

	h.AdminExec(`UPDATE billing.batch SET status = 'UNDER_REVIEW' WHERE tenant_id = $1 AND id = $2`,
		s.tenant, s.batch)
	if err := s.decide(h, s.firstID, "APPROVE", "1000", ""); err != nil {
		t.Fatalf("an approval under review was refused: %v", err)
	}
	// A changed decision is the same row with the last answer on it, while the batch is still
	// under review.
	if err := s.decide(h, s.firstID, "CUT", "900", "CONTRACT_TERMS"); err != nil {
		t.Fatalf("a changed decision under review was refused: %v", err)
	}

	// Once the batch is decided, nothing moves again.
	h.AdminExec(`
		UPDATE billing.batch
		   SET status = 'DECIDED', decided_at = clock_timestamp(), decided_by = $2,
		       approved_total = 1400, cut_total = 100, returned_total = 0, rejected_total = 0
		 WHERE tenant_id = $1 AND id = $3`, s.tenant, s.actor, s.batch)
	err = s.decide(h, s.firstID, "APPROVE", "1000", "")
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a decision on a DECIDED batch")
}

// TestOneInvoiceSitsInOneLiveBatch is section 3's third requirement, proved by two concurrent
// transactions doing exactly what two `putBatchInvoices` calls would.
//
// Both transactions insert the same invoice into two different draft batches. The second one
// blocks on the unique index until the first commits, and then loses. There is no ordering here
// a service could have arranged: whichever commits first wins, and the other is refused.
func TestOneInvoiceSitsInOneLiveBatch(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h).seedBatch(t, h)
	ctx, cancel := h.Ctx()
	defer cancel()

	var second uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO billing.batch (tenant_id, reference, provider_organization_id, domain_code,
		                           currency_code, period_from, period_to)
		VALUES ($1, $2, $3, 'GENERIC', 'TRY', '2026-03-01', '2026-03-31') RETURNING id`,
		s.tenant, "IC-202603-"+batchTail(), s.provider).Scan(&second); err != nil {
		t.Fatalf("insert the second batch: %v", err)
	}
	third, err := s.insertInvoice(t, h, s.provider, "ICM000004", "2026-03-20",
		"700", "0", "700", "SUBMITTED")
	if err != nil {
		t.Fatalf("insert the third invoice: %v", err)
	}

	left, err := h.Admin.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the first transaction: %v", err)
	}
	defer func() { _ = left.Rollback(ctx) }()
	right, err := h.Admin.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the second transaction: %v", err)
	}
	defer func() { _ = right.Rollback(ctx) }()

	insert := `INSERT INTO billing.batch_invoice (tenant_id, batch_id, invoice_id, submitted_amount)
	           VALUES ($1, $2, $3, 700)`
	if _, err := left.Exec(ctx, insert, s.tenant, s.batch, third); err != nil {
		t.Fatalf("the first transaction could not claim the invoice: %v", err)
	}

	// The second transaction runs while the first is still open. It blocks on the index rather
	// than failing, so the failure is asserted after the first commits.
	done := make(chan error, 1)
	go func() {
		_, err := right.Exec(ctx, insert, s.tenant, second, third)
		done <- err
	}()

	if err := left.Commit(ctx); err != nil {
		t.Fatalf("commit the first transaction: %v", err)
	}
	err = <-done
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation,
		"one invoice in two live batches")
}

// TestCancelledBatchReleasesItsInvoices is what makes the rule above liveable: an invoice whose
// icmal was withdrawn goes into another one, and the row stays on the record.
func TestCancelledBatchReleasesItsInvoices(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h).seedBatch(t, h)
	ctx, cancel := h.Ctx()
	defer cancel()

	var second uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO billing.batch (tenant_id, reference, provider_organization_id, domain_code,
		                           currency_code, period_from, period_to)
		VALUES ($1, $2, $3, 'GENERIC', 'TRY', '2026-03-01', '2026-03-31') RETURNING id`,
		s.tenant, "IC-202603-"+batchTail(), s.provider).Scan(&second); err != nil {
		t.Fatalf("insert the second batch: %v", err)
	}
	// While the first batch is live the invoice cannot go anywhere else.
	err := s.memberErr(h, second, s.firstID, "1000")
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "a second live batch")

	h.AdminExec(`UPDATE billing.batch SET status = 'CANCELLED' WHERE tenant_id = $1 AND id = $2`,
		s.tenant, s.batch)
	if err := s.memberErr(h, second, s.firstID, "1000"); err != nil {
		t.Fatalf("a cancelled batch did not release its invoice: %v", err)
	}

	// Nothing was deleted: "which invoices did this cancelled icmal cover" is a question a
	// dispute asks.
	var kept int
	if err := h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM billing.batch_invoice
		 WHERE tenant_id = $1 AND batch_id = $2 AND NOT active`,
		s.tenant, s.batch).Scan(&kept); err != nil {
		t.Fatalf("read the cancelled batch's rows: %v", err)
	}
	if kept != 2 {
		t.Fatalf("the cancelled batch kept %d inactive rows, want 2", kept)
	}
}

// TestBatchDecisionIsTiedToItsAmount is the CHECK that stops "approved" sitting beside a figure
// nobody billed.
func TestBatchDecisionIsTiedToItsAmount(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h).seedBatch(t, h)
	s.underReview(t, h)

	cases := []struct {
		name     string
		decision string
		approved string
		reason   string
	}{
		{"an approval for less than was billed", "APPROVE", "900", ""},
		{"a cut of nothing", "CUT", "0", "CONTRACT_TERMS"},
		{"a cut of everything", "CUT", "1000", "CONTRACT_TERMS"},
		{"a cut of more than was billed", "CUT", "1200", "CONTRACT_TERMS"},
		{"a cut with no reason", "CUT", "900", ""},
		{"a return that approves something", "RETURN", "1", "DOCUMENT_MISSING"},
		{"a return with no reason", "RETURN", "0", ""},
		{"a rejection that approves something", "REJECT", "1000", "NOT_COVERED"},
		{"a rejection with no reason", "REJECT", "0", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.decide(h, s.firstID, tc.decision, tc.approved, tc.reason)
			dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, tc.name)
		})
	}

	// The four that are right.
	for _, tc := range []struct{ decision, approved, reason string }{
		{"APPROVE", "1000", ""},
		{"CUT", "999.999999", "CONTRACT_TERMS"},
		{"RETURN", "0", "DOCUMENT_MISSING"},
		{"REJECT", "0", "NOT_COVERED"},
	} {
		if err := s.decide(h, s.firstID, tc.decision, tc.approved, tc.reason); err != nil {
			t.Errorf("a valid %s was refused: %v", tc.decision, err)
		}
	}

	// And a decided row cannot lose half its facts: the person and the moment travel with the
	// decision.
	err := h.AdminExecErr(`
		UPDATE billing.batch_invoice SET decided_by = NULL
		 WHERE tenant_id = $1 AND batch_id = $2 AND invoice_id = $3`,
		s.tenant, s.batch, s.firstID)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a decision with no decider")
}

// TestDecidedBatchTotalsReconcile is the acceptance criterion the settlement rests on, as a
// CHECK rather than as a habit in a service.
func TestDecidedBatchTotalsReconcile(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h).seedBatch(t, h)
	s.underReview(t, h)

	decide := func(approved, cut, returned, rejected string) error {
		return h.AdminExecErr(`
			UPDATE billing.batch
			   SET status = 'DECIDED', decided_at = clock_timestamp(), decided_by = $2,
			       approved_total = $3::text::numeric, cut_total = $4::text::numeric,
			       returned_total = $5::text::numeric, rejected_total = $6::text::numeric
			 WHERE tenant_id = $1 AND id = $7`,
			s.tenant, s.actor, approved, cut, returned, rejected, s.batch)
	}

	// A kuruş out, with no service anywhere near it.
	err := decide("1000", "100", "300", "99.99")
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "totals a kuruş short of 1500")

	// And a kuruş over.
	err = decide("1000", "100", "300", "100.01")
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "totals a kuruş above 1500")

	if err := decide("1000", "100", "300", "100"); err != nil {
		t.Fatalf("reconciling totals were refused: %v", err)
	}

	// A decided batch also says when and by whom.
	err = h.AdminExecErr(`UPDATE billing.batch SET decided_by = NULL WHERE tenant_id = $1 AND id = $2`,
		s.tenant, s.batch)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a decided batch with no decider")
}

// TestBatchReferenceIsUniquePerTenant keeps the reference a provider quotes on the telephone
// unambiguous.
func TestBatchReferenceIsUniquePerTenant(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h)
	reference := "IC-202603-" + batchTail()

	insert := func(ref string) error {
		return h.AdminExecErr(`
			INSERT INTO billing.batch (tenant_id, reference, provider_organization_id,
			                           domain_code, currency_code, period_from, period_to)
			VALUES ($1, $2, $3, 'GENERIC', 'TRY', '2026-03-01', '2026-03-31')`,
			s.tenant, ref, s.provider)
	}
	if err := insert(reference); err != nil {
		t.Fatalf("insert the first batch: %v", err)
	}
	dbtest.ExpectSQLState(t, insert(reference), dbtest.SQLStateUniqueViolation,
		"two icmals under one reference")

	// The shape is the column's, not a convention: a reference of another shape is refused.
	for _, bad := range []string{"IC-2026-ABCDEFGH", "IC-202603-abcdefgh", "IC-202603-ABCDEFG",
		"XX-202603-ABCDEFGH", "IC-202603-ABCDEF01"} {
		dbtest.ExpectSQLState(t, insert(bad), dbtest.SQLStateCheckViolation,
			"a reference shaped "+bad)
	}
}

// TestClaimCanBeClosedUnpaid is the status WP-I7-03's rejection needs, as the word list rather
// than as a string a service happens to write.
func TestClaimCanBeClosedUnpaid(t *testing.T) {
	h := dbtest.New(t)
	s := seedBilling(t, h)
	claim := s.claim(t, h, "CLM-UNPAID-1", "INVOICED", "500")

	if err := h.AdminExecErr(
		`UPDATE claim.claim SET status = 'CLOSED_UNPAID' WHERE tenant_id = $1 AND id = $2`,
		s.tenant, claim); err != nil {
		t.Fatalf("a claim could not be closed unpaid: %v", err)
	}
	// And the word list is still a list: a status nobody defined is still refused.
	err := h.AdminExecErr(
		`UPDATE claim.claim SET status = 'CLOSED_MAYBE' WHERE tenant_id = $1 AND id = $2`,
		s.tenant, claim)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "an undefined claim status")
}
