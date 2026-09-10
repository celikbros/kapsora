package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/billing/application"
	"github.com/celikbros/kapsora/internal/billing/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// The icmal's half of the fixture (WP-I7-03), on the same real database and the same real claim
// module as the invoice's.
//
// Nothing here is stubbed, and that is the point: what this package must never get wrong is that
// a cut on an icmal moves money on real claims, and a fake claims port would prove that a fake
// moved.

// batchPeriod is the window every icmal in these tests collects over.
var (
	batchFrom = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	batchTo   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
)

// providerBatchRC is the provider's billing clerk as PROVIDER_BILLING is issued in roles.go:
// the invoice grants plus `batch.create` and `batch.submit`, scoped to its own organization.
func (f *fixture) providerBatchRC() identity.RequestContext {
	rc := f.providerRC()
	rc.Permissions[application.PermissionBatchCreate] = struct{}{}
	rc.Permissions[application.PermissionBatchSubmit] = struct{}{}
	return rc
}

// reviewerRC is the payer's financial reviewer as FINANCIAL_REVIEWER is issued: tenant-wide,
// `batch.review`, no clinical grant of any kind. The actor is spelled out because every rule
// this package has about who may decide is a rule about *which person*.
func (f *fixture) reviewerRC(actorID uuid.UUID) identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal: identity.Principal{ActorID: actorID},
		Permissions: map[string]struct{}{
			application.PermissionRead:        {},
			application.PermissionBatchReview: {},
		},
	}
}

// submittedInvoice raises one invoice covering the claims it is given, and submits it. The
// amounts are the allocations; the invoice bills exactly their sum, because a mismatch is
// WP-I7-02's business and not this package's.
func (f *fixture) submittedInvoice(t *testing.T, number string,
	allocations map[uuid.UUID]string,
) application.InvoiceView {
	t.Helper()
	total := zeroText()
	rows := make([]application.AllocationInput, 0, len(allocations))
	for claimID, amount := range allocations {
		rows = append(rows, application.AllocationInput{
			ClaimID: claimID, AllocatedAmount: amount,
		})
		total = addText(t, total, amount)
	}
	rc := f.providerBatchRC()
	view := f.createInvoice(t, rc, number, total)
	replaced, err := f.invoices.PutAllocations(context.Background(), rc, view.Invoice.ID, rows,
		view.Invoice.RowVersion)
	if err != nil {
		t.Fatalf("put allocations on %s: %v", number, err)
	}
	submitted, err := f.invoices.SubmitInvoice(context.Background(), rc, replaced.Invoice.ID,
		replaced.Invoice.RowVersion)
	if err != nil {
		t.Fatalf("submit %s: %v", number, err)
	}
	return submitted
}

// draftBatch opens an icmal for this provider over the fixture's period.
func (f *fixture) draftBatch(t *testing.T) application.BatchView {
	t.Helper()
	view, err := f.invoices.CreateBatch(context.Background(), f.providerBatchRC(),
		application.CreateBatchInput{
			// GENERIC, which is what the fixture's invoices carry: the icmal's domain has to
			// be the invoices' domain, and a batch that named a different one would be a
			// batch nothing could go into.
			ProviderOrganizationID: f.provider,
			PeriodFrom:             batchFrom, PeriodTo: batchTo,
		})
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	return view
}

// submittedBatch opens an icmal over the given invoices and sends it.
func (f *fixture) submittedBatch(t *testing.T, invoiceIDs ...uuid.UUID) application.BatchView {
	t.Helper()
	rc := f.providerBatchRC()
	// The clerk has re-entered their password, so the step-up above the tenant's threshold is
	// satisfied whatever the batch is worth. The step-up itself is proved by its own test; a
	// fixture that could not get a large icmal sent would make every other test about small
	// numbers.
	rc.StepUpValid = true
	batch := f.draftBatch(t)
	filled, err := f.invoices.PutBatchInvoices(context.Background(), rc, batch.Batch.ID,
		invoiceIDs, batch.Batch.RowVersion)
	if err != nil {
		t.Fatalf("put batch invoices: %v", err)
	}
	submitted, err := f.invoices.SubmitBatch(context.Background(), rc, filled.Batch.ID,
		filled.Batch.RowVersion)
	if err != nil {
		t.Fatalf("submit batch: %v", err)
	}
	return submitted
}

// review records one decision and returns the batch as it stands afterwards.
func (f *fixture) review(t *testing.T, rc identity.RequestContext, batch application.BatchView,
	invoiceID uuid.UUID, in application.ReviewBatchInvoiceInput,
) application.BatchView {
	t.Helper()
	out, err := f.invoices.ReviewBatchInvoice(context.Background(), rc, batch.Batch.ID, invoiceID,
		in, batch.Batch.RowVersion)
	if err != nil {
		t.Fatalf("review %s as %s: %v", invoiceID, in.Decision, err)
	}
	return out
}

// approveAll answers every invoice in the batch with APPROVE, which is the shortest way to a
// batch that can be decided.
func (f *fixture) approveAll(t *testing.T, rc identity.RequestContext,
	batch application.BatchView,
) application.BatchView {
	t.Helper()
	out := batch
	for _, member := range batch.Invoices {
		out = f.review(t, rc, out, member.InvoiceID, application.ReviewBatchInvoiceInput{
			Decision: domain.DecisionApprove,
		})
	}
	return out
}

// invoiceStatus reads an invoice's status straight out of the table, so a test asserts what is
// stored rather than what a projection said.
func (f *fixture) invoiceStatus(t *testing.T, invoiceID uuid.UUID) string {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var status string
	if err := f.h.Admin.QueryRow(ctx,
		`SELECT status FROM billing.invoice WHERE tenant_id = $1 AND id = $2`,
		f.tenant, invoiceID).Scan(&status); err != nil {
		t.Fatalf("read invoice status: %v", err)
	}
	return status
}

// adjustmentRow is one row of the claim ledger, as a test reads it back.
type adjustmentRow struct {
	ClaimID    uuid.UUID
	Type       string
	Amount     string
	ReasonCode string
	Reverses   *uuid.UUID
}

// adjustments reads the ledger of the given claims, oldest first — which is the order a
// reversal reads in, because a reversal always comes after the row it takes back.
func (f *fixture) adjustments(t *testing.T, claims ...uuid.UUID) []adjustmentRow {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	rows, err := f.h.Admin.Query(ctx, `
		SELECT claim_id, adjustment_type, trim_scale(amount)::text, reason_code,
		       reverses_adjustment_id
		  FROM claim.adjustment
		 WHERE tenant_id = $1 AND claim_id = ANY($2::uuid[])
		 ORDER BY created_at, id`, f.tenant, claims)
	if err != nil {
		t.Fatalf("read adjustments: %v", err)
	}
	defer rows.Close()
	var out []adjustmentRow
	for rows.Next() {
		var row adjustmentRow
		var reverses uuid.NullUUID
		if err := rows.Scan(&row.ClaimID, &row.Type, &row.Amount, &row.ReasonCode,
			&reverses); err != nil {
			t.Fatalf("scan adjustment: %v", err)
		}
		if reverses.Valid {
			id := reverses.UUID
			row.Reverses = &id
		}
		out = append(out, row)
	}
	return out
}

// workItem is the state of the icmal's review in WP-I4-03's queue.
type workItem struct {
	Status   string
	Outcome  *string
	Assignee uuid.NullUUID
}

// createReviewQueue configures the tenant's BATCH_REVIEW queue, the way an operator would. A
// tenant without one raises no work, which is a case a test asserts separately.
func (f *fixture) createReviewQueue(t *testing.T) uuid.UUID {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var id uuid.UUID
	if err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO workflow.work_queue (tenant_id, code, name, domain_code, sla_minutes)
		VALUES ($1, 'BATCH_REVIEW', 'İcmal incelemesi', 'GENERIC', 2880) RETURNING id`,
		f.tenant).Scan(&id); err != nil {
		t.Fatalf("create the review queue: %v", err)
	}
	return id
}

// batchWorkItem reads the one work item raised for a batch, or reports that there is none.
func (f *fixture) batchWorkItem(t *testing.T, batchID uuid.UUID) (workItem, bool) {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var out workItem
	err := f.h.Admin.QueryRow(ctx, `
		SELECT status, outcome_code, assignee_actor_id
		  FROM workflow.work_item
		 WHERE tenant_id = $1 AND aggregate_type = 'BATCH' AND aggregate_id = $2`,
		f.tenant, batchID).Scan(&out.Status, &out.Outcome, &out.Assignee)
	if err != nil {
		return workItem{}, false
	}
	return out, true
}

// auditDetails reads the detail maps of the audit rows one action wrote about one batch, in the
// order they were written.
func (f *fixture) auditDetails(t *testing.T, batchID uuid.UUID, action string) []map[string]any {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	rows, err := f.h.Admin.Query(ctx, `
		SELECT detail_json
		  FROM audit.event
		 WHERE tenant_id = $1 AND resource_type = 'BATCH' AND resource_id = $2
		   AND action_code = $3
		 ORDER BY occurred_at, id`, f.tenant, batchID, action)
	if err != nil {
		t.Fatalf("read audit rows: %v", err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var detail map[string]any
		if err := rows.Scan(&detail); err != nil {
			t.Fatalf("scan audit row: %v", err)
		}
		out = append(out, detail)
	}
	return out
}

// outboxTypes reads which event types were published for one aggregate.
func (f *fixture) outboxTypes(t *testing.T, aggregateID uuid.UUID) []string {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	rows, err := f.h.Admin.Query(ctx, `
		SELECT event_type FROM system.outbox_event
		 WHERE tenant_id = $1 AND aggregate_id = $2
		 ORDER BY occurred_at, id`, f.tenant, aggregateID)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scan outbox row: %v", err)
		}
		out = append(out, value)
	}
	return out
}

// zeroText and addText are exact decimal arithmetic in the fixture, spelled the way the service
// spells it: nothing in a test that is about money is allowed to pass through a float either.
func zeroText() string { return benefitdomain.ZeroQuantity().String() }

func addText(t *testing.T, a, b string) string {
	t.Helper()
	left, err := benefitdomain.ParseQuantity(a)
	if err != nil {
		t.Fatalf("parse %q: %v", a, err)
	}
	right, err := benefitdomain.ParseQuantity(b)
	if err != nil {
		t.Fatalf("parse %q: %v", b, err)
	}
	return left.Add(right).String()
}

// memberOf finds one invoice's row in a batch view.
func memberOf(t *testing.T, view application.BatchView, invoiceID uuid.UUID,
) application.BatchInvoiceRecord {
	t.Helper()
	for _, member := range view.Invoices {
		if member.InvoiceID == invoiceID {
			return member
		}
	}
	t.Fatalf("invoice %s is not in the batch", invoiceID)
	return application.BatchInvoiceRecord{}
}

// submittedInvoiceInCurrency and submittedInvoiceInDomain raise one invoice that deliberately
// does not belong in the fixture's icmal, so the mixture rule has something to refuse. The claim
// underneath has to be denominated the same way, because WP-I7-02 already refuses an allocation
// in another currency and this test is about the *batch*.
func (f *fixture) submittedInvoiceInCurrency(t *testing.T, number string, claimID uuid.UUID,
	amount, currency string,
) application.InvoiceView {
	t.Helper()
	header := f.header(number, amount)
	header.CurrencyCode = currency
	return f.submitHeader(t, header, claimID, amount)
}

func (f *fixture) submittedInvoiceInDomain(t *testing.T, number string, claimID uuid.UUID,
	amount, domainCode string,
) application.InvoiceView {
	t.Helper()
	header := f.header(number, amount)
	header.DomainCode = domainCode
	return f.submitHeader(t, header, claimID, amount)
}

// submitHeader raises, allocates and submits one invoice from a header a test built.
func (f *fixture) submitHeader(t *testing.T, header application.CreateInvoiceInput,
	claimID uuid.UUID, amount string,
) application.InvoiceView {
	t.Helper()
	rc := f.providerBatchRC()
	view, err := f.invoices.CreateInvoice(context.Background(), rc, header)
	if err != nil {
		t.Fatalf("create invoice %s: %v", header.InvoiceNumber, err)
	}
	view = f.allocate(t, rc, view, claimID, amount)
	submitted, err := f.invoices.SubmitInvoice(context.Background(), rc, view.Invoice.ID,
		view.Invoice.RowVersion)
	if err != nil {
		t.Fatalf("submit %s: %v", header.InvoiceNumber, err)
	}
	return submitted
}

// correctionHeader is the header of an invoice raised to supersede a returned one.
func (f *fixture) correctionHeader(number, payable string, supersedes uuid.UUID,
) application.CreateInvoiceInput {
	header := f.header(number, payable)
	header.SupersedesInvoiceID = &supersedes
	return header
}

// claimApprovedTotal is what the payer has approved for a claim after its adjustments, read
// through the database's own function — the same arithmetic the allocation ceiling applies. It
// is read here rather than through `InvoiceReadiness` because a claim already on a document is
// no longer in a status that endpoint answers about, and the figure is the same either way.
func (f *fixture) claimApprovedTotal(t *testing.T, claimID uuid.UUID) string {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var total string
	if err := f.h.Admin.QueryRow(ctx,
		`SELECT trim_scale(billing.claim_approved_total($1, $2))::text`,
		f.tenant, claimID).Scan(&total); err != nil {
		t.Fatalf("read the claim's approved total: %v", err)
	}
	return total
}

// auditActor reads which actor the newest audit row of one action names. "Who did this" is half
// of what an audit row is for, and a row that recorded the amounts and not the person would be
// half a record.
func (f *fixture) auditActor(t *testing.T, batchID uuid.UUID, action string) uuid.UUID {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var actor uuid.UUID
	if err := f.h.Admin.QueryRow(ctx, `
		SELECT actor_id
		  FROM audit.event
		 WHERE tenant_id = $1 AND resource_type = 'BATCH' AND resource_id = $2
		   AND action_code = $3
		 ORDER BY occurred_at DESC, id DESC
		 LIMIT 1`, f.tenant, batchID, action).Scan(&actor); err != nil {
		t.Fatalf("read the audit actor: %v", err)
	}
	return actor
}
