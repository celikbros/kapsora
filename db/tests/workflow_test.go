package dbtests

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// workflowSeed is one tenant with the two queues migration 000027 needs to say anything
// interesting: a review queue with a clock and a supervisor queue to escalate into.
type workflowSeed struct {
	tenant   uuid.UUID
	actor    uuid.UUID
	queue    uuid.UUID
	escalate uuid.UUID
	item     uuid.UUID
}

func seedWorkflow(h *dbtest.Harness, code string) workflowSeed {
	h.T.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()

	s := workflowSeed{tenant: h.CreateTenant(code)}
	s.actor = h.CreateActor("workflow-db-"+code, "Workflow "+code)
	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		h.T.Helper()
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			h.T.Fatalf("seed %s: %v", what, err)
		}
	}

	scan(&s.escalate, "escalation queue", `
		INSERT INTO workflow.work_queue (tenant_id, code, name, domain_code)
		VALUES ($1, 'SUPERVISOR', 'Süpervizör', 'GENERIC') RETURNING id`, s.tenant)
	scan(&s.queue, "work queue", `
		INSERT INTO workflow.work_queue (tenant_id, code, name, domain_code, sla_minutes,
		                                 escalation_queue_id)
		VALUES ($1, 'REVIEW', 'İnceleme', 'HEALTH', 60, $2) RETURNING id`, s.tenant, s.escalate)
	scan(&s.item, "work item", `
		INSERT INTO workflow.work_item (tenant_id, queue_id, aggregate_type, aggregate_id, title,
		                                sla_minutes_snapshot, due_at)
		VALUES ($1, $2, 'SERVICE_REQUEST', gen_random_uuid(), 'İncelenecek talep', 60,
		        clock_timestamp() + interval '60 minutes')
		RETURNING id`, s.tenant, s.queue)
	return s
}

// TestWorkItemCannotBeOwnedAndOpenAtOnce is the pair of CHECKs that make "nobody owns
// this" readable from the row alone, which is what a claim's predicate relies on.
func TestWorkItemCannotBeOwnedAndOpenAtOnce(t *testing.T) {
	h := dbtest.New(t)
	s := seedWorkflow(h, "WF_OWNER")

	err := h.AdminExecErr(`
		UPDATE workflow.work_item SET assignee_actor_id = $3, assigned_at = clock_timestamp()
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.item, s.actor)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "an OPEN item with an owner")

	err = h.AdminExecErr(`
		UPDATE workflow.work_item SET status = 'CLAIMED' WHERE tenant_id = $1 AND id = $2`,
		s.tenant, s.item)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a CLAIMED item with no owner")

	// An owner and the moment of taking ownership arrive together.
	err = h.AdminExecErr(`
		UPDATE workflow.work_item SET status = 'CLAIMED', assignee_actor_id = $3
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.item, s.actor)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "an owner with no assignment time")

	if err := h.AdminExecErr(`
		UPDATE workflow.work_item
		   SET status = 'CLAIMED', assignee_actor_id = $3, assigned_at = clock_timestamp()
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.item, s.actor); err != nil {
		t.Fatalf("claiming with an owner and a time must be allowed: %v", err)
	}
}

// TestWorkItemCompletionSaysWhenAndWhat: an item closed with neither is a row nobody can
// report on.
func TestWorkItemCompletionSaysWhenAndWhat(t *testing.T) {
	h := dbtest.New(t)
	s := seedWorkflow(h, "WF_COMPLETE")

	err := h.AdminExecErr(`
		UPDATE workflow.work_item SET status = 'COMPLETED', assignee_actor_id = $3,
		       assigned_at = clock_timestamp(), completed_at = clock_timestamp()
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.item, s.actor)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "completed with no outcome")

	err = h.AdminExecErr(`
		UPDATE workflow.work_item SET status = 'COMPLETED', assignee_actor_id = $3,
		       assigned_at = clock_timestamp(), outcome_code = 'APPROVED'
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.item, s.actor)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "completed with no completion time")

	if err := h.AdminExecErr(`
		UPDATE workflow.work_item SET status = 'COMPLETED', assignee_actor_id = $3,
		       assigned_at = clock_timestamp(), completed_at = clock_timestamp(),
		       outcome_code = 'APPROVED', completed_by = $3
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.item, s.actor); err != nil {
		t.Fatalf("completing with a time and an outcome must be allowed: %v", err)
	}

	err = h.AdminExecErr(`
		UPDATE workflow.work_item SET status = 'ARCHIVED' WHERE tenant_id = $1 AND id = $2`,
		s.tenant, s.item)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a status outside the closed list")
}

// TestWorkItemDueDateExistsExactlyWithItsClock: a due date without a snapshot, or a
// snapshot without a due date, would leave "is this late" unanswerable from the row.
func TestWorkItemDueDateExistsExactlyWithItsClock(t *testing.T) {
	h := dbtest.New(t)
	s := seedWorkflow(h, "WF_DUE")

	err := h.AdminExecErr(`
		UPDATE workflow.work_item SET sla_minutes_snapshot = NULL
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.item)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a due date with no clock behind it")

	err = h.AdminExecErr(`
		UPDATE workflow.work_item SET due_at = NULL WHERE tenant_id = $1 AND id = $2`,
		s.tenant, s.item)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a clock with no due date")

	err = h.AdminExecErr(`
		UPDATE workflow.work_item SET sla_minutes_snapshot = 0,
		       due_at = created_at WHERE tenant_id = $1 AND id = $2`, s.tenant, s.item)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "an SLA of no minutes at all")
}

// TestWorkQueueCannotEscalateIntoItself, and its code is unique inside the tenant.
func TestWorkQueueCannotEscalateIntoItself(t *testing.T) {
	h := dbtest.New(t)
	s := seedWorkflow(h, "WF_QUEUE")

	err := h.AdminExecErr(`
		UPDATE workflow.work_queue SET escalation_queue_id = id
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.queue)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a queue escalating into itself")

	err = h.AdminExecErr(`
		INSERT INTO workflow.work_queue (tenant_id, code, name, domain_code)
		VALUES ($1, 'REVIEW', 'İkinci inceleme', 'GENERIC')`, s.tenant)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "the same queue code twice")

	err = h.AdminExecErr(`
		INSERT INTO workflow.work_queue (tenant_id, code, name, domain_code)
		VALUES ($1, 'review lower', 'Küçük harf', 'GENERIC')`, s.tenant)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a queue code outside the format")
}

// TestApprovalPolicyOverlapIsRefused is the constraint the lookup depends on: without it,
// "how many approvals does this need" would have two answers on the same day.
func TestApprovalPolicyOverlapIsRefused(t *testing.T) {
	h := dbtest.New(t)
	s := seedWorkflow(h, "WF_POLICY")

	insert := func(scope, from, to string) error {
		return h.AdminExecErr(`
			INSERT INTO workflow.approval_policy (tenant_id, action_code, scope_code, min_amount,
			                                      max_amount, required_approver_count,
			                                      valid_from, valid_to)
			VALUES ($1, 'service_request.approve', $2, 0, 1000, 2, $3::date, $4::date)`,
			s.tenant, scope, from, nullableDate(to))
	}
	if err := insert("STANDARD", "2026-01-01", "2026-07-01"); err != nil {
		t.Fatalf("first policy: %v", err)
	}
	dbtest.ExpectSQLState(t, insert("STANDARD", "2026-06-01", "2026-12-01"),
		dbtest.SQLStateExclusionViolation, "two policies covering the same day")

	// A different scope may cover the same day: that is how one action carries more than
	// one amount band at a time.
	if err := insert("HIGH_VALUE", "2026-01-01", ""); err != nil {
		t.Fatalf("a second scope on the same day must be allowed: %v", err)
	}
	// And the same scope may follow on once the first period has ended.
	if err := insert("STANDARD", "2026-07-01", ""); err != nil {
		t.Fatalf("a following period must be allowed: %v", err)
	}

	err := h.AdminExecErr(`
		INSERT INTO workflow.approval_policy (tenant_id, action_code, scope_code, min_amount,
		                                      max_amount, required_approver_count, valid_from)
		VALUES ($1, 'service_request.approve', 'BACKWARDS', 1000, 10, 1, '2026-01-01')`, s.tenant)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a band whose top is below its floor")

	err = h.AdminExecErr(`
		INSERT INTO workflow.approval_policy (tenant_id, action_code, scope_code,
		                                      required_approver_count, valid_from)
		VALUES ($1, 'service_request.approve', 'NOBODY', 0, '2026-01-01')`, s.tenant)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a policy needing no approver at all")
}

// nullableDate turns the empty string into a NULL date argument.
func nullableDate(raw string) *string {
	if raw == "" {
		return nil
	}
	return &raw
}

// TestWorkflowCommentsAreAppendOnlyAndCarryTheirVisibility: a comment that could be edited
// after the decision it influenced is not a record of why the decision was made.
func TestWorkflowCommentsAreAppendOnlyAndCarryTheirVisibility(t *testing.T) {
	h := dbtest.New(t)
	s := seedWorkflow(h, "WF_COMMENT")
	ctx, cancel := h.Ctx()
	defer cancel()

	var commentID uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO workflow.comment (tenant_id, aggregate_type, aggregate_id, work_item_id,
		                              visibility, body, author_actor_id)
		VALUES ($1, 'SERVICE_REQUEST', gen_random_uuid(), $2, 'PROVIDER', 'Rapor eksik', $3)
		RETURNING id`, s.tenant, s.item, s.actor).Scan(&commentID); err != nil {
		t.Fatalf("insert comment: %v", err)
	}

	err := h.AdminExecErr(`UPDATE workflow.comment SET body = 'Başka bir şey'
	                        WHERE tenant_id = $1 AND id = $2`, s.tenant, commentID)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "editing a comment")

	err = h.AdminExecErr(`DELETE FROM workflow.comment WHERE tenant_id = $1 AND id = $2`,
		s.tenant, commentID)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "deleting a comment")

	err = h.AdminExecErr(`
		INSERT INTO workflow.comment (tenant_id, aggregate_type, aggregate_id, visibility, body)
		VALUES ($1, 'SERVICE_REQUEST', gen_random_uuid(), 'EVERYBODY', 'Herkese açık')`, s.tenant)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a visibility outside the closed list")

	err = h.AdminExecErr(`
		INSERT INTO workflow.comment (tenant_id, aggregate_type, aggregate_id, visibility, body)
		VALUES ($1, 'SERVICE_REQUEST', gen_random_uuid(), 'INTERNAL', '   ')`, s.tenant)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a comment with nothing in it")
}

// TestWorkflowCompositeKeysStayInsideTheTenant: every foreign key of migration 000027
// carries the tenant, so a row can never point at another tenant's queue or item.
func TestWorkflowCompositeKeysStayInsideTheTenant(t *testing.T) {
	h := dbtest.New(t)
	a := seedWorkflow(h, "WF_FK_A")
	b := seedWorkflow(h, "WF_FK_B")

	err := h.AdminExecErr(`
		INSERT INTO workflow.work_item (tenant_id, queue_id, aggregate_type, aggregate_id, title)
		VALUES ($1, $2, 'SERVICE_REQUEST', gen_random_uuid(), 'Yanlış kuyruk')`,
		a.tenant, b.queue)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateForeignKeyViolation, "an item in another tenant's queue")

	err = h.AdminExecErr(`
		UPDATE workflow.work_queue SET escalation_queue_id = $3 WHERE tenant_id = $1 AND id = $2`,
		a.tenant, a.queue, b.escalate)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateForeignKeyViolation, "escalating into another tenant's queue")

	err = h.AdminExecErr(`
		INSERT INTO workflow.comment (tenant_id, aggregate_type, aggregate_id, work_item_id,
		                              visibility, body)
		VALUES ($1, 'SERVICE_REQUEST', gen_random_uuid(), $2, 'INTERNAL', 'Yanlış kalem')`,
		a.tenant, b.item)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateForeignKeyViolation, "a comment on another tenant's item")
}

// TestWorkflowTenantIsolation: every table of this package is invisible across tenants
// through the application role.
func TestWorkflowTenantIsolation(t *testing.T) {
	h := dbtest.New(t)
	a := seedWorkflow(h, "WF_RLS_A")
	b := seedWorkflow(h, "WF_RLS_B")
	for _, s := range []workflowSeed{a, b} {
		h.AdminExec(`
			INSERT INTO workflow.comment (tenant_id, aggregate_type, aggregate_id, work_item_id,
			                              visibility, body)
			VALUES ($1, 'SERVICE_REQUEST', gen_random_uuid(), $2, 'INTERNAL', 'Not')`,
			s.tenant, s.item)
		h.AdminExec(`
			INSERT INTO workflow.approval_policy (tenant_id, action_code, scope_code,
			                                      required_approver_count, valid_from)
			VALUES ($1, 'service_request.approve', 'STANDARD', 1, '2026-01-01')`, s.tenant)
	}

	visible := func(tenant uuid.UUID, table string) int {
		t.Helper()
		var n int
		err := h.AppTx(tenant, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n)
		})
		if err != nil {
			t.Fatalf("count %s for %s: %v", table, tenant, err)
		}
		return n
	}
	for _, tc := range []struct {
		table string
		want  int
	}{
		{"workflow.work_queue", 2}, // the review queue and the one it escalates into
		{"workflow.work_item", 1},
		{"workflow.comment", 1},
		{"workflow.approval_policy", 1},
	} {
		if got := visible(a.tenant, tc.table); got != tc.want {
			t.Fatalf("%s: tenant A sees %d rows, want %d", tc.table, got, tc.want)
		}
		if got := visible(b.tenant, tc.table); got != tc.want {
			t.Fatalf("%s: tenant B sees %d rows, want %d", tc.table, got, tc.want)
		}
	}

	// A write tagged with another tenant fails the WITH CHECK of the policy.
	err := h.AppTx(a.tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `
			INSERT INTO workflow.work_queue (tenant_id, code, name, domain_code)
			VALUES ($1, 'SMUGGLED', 'Kaçak', 'GENERIC')`, b.tenant)
		return execErr
	})
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateInsufficientPrivilege, "writing another tenant's queue")
}

// TestWorkflowPermissionsAreSeededAndGrantable checks both halves of the same fact. The
// permission catalogue and the role templates are two separate places, and a permission
// seeded into the first but missing from the second is a permission nobody can ever hold —
// which has already happened once, with pricing.quote.
func TestWorkflowPermissionsAreSeededAndGrantable(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	granted := map[string][]string{}
	for _, tpl := range identityapp.RoleTemplates() {
		for _, code := range tpl.Permissions {
			granted[code] = append(granted[code], tpl.Code)
		}
	}
	for _, code := range []string{
		"worklist.read", "worklist.claim", "worklist.reassign",
		"workflow.queue.manage", "workflow.policy.manage",
	} {
		var n int
		if err := h.Admin.QueryRow(ctx,
			`SELECT count(*) FROM iam.permission WHERE code = $1`, code).Scan(&n); err != nil {
			t.Fatalf("read permission %s: %v", code, err)
		}
		if n != 1 {
			t.Fatalf("permission %s is seeded %d times, want once", code, n)
		}
		if len(granted[code]) == 0 {
			t.Fatalf("permission %s is in the catalogue but in no role template: nobody can hold it", code)
		}
	}
	for _, want := range []struct{ role, permission string }{
		{"PROGRAM_MANAGER", "worklist.read"},
		{"PROGRAM_MANAGER", "worklist.claim"},
		{"MEDICAL_REVIEWER", "worklist.read"},
		{"MEDICAL_REVIEWER", "worklist.claim"},
		{"FINANCIAL_REVIEWER", "worklist.read"},
		{"FINANCIAL_REVIEWER", "worklist.claim"},
		{"PAYER_APPROVER", "worklist.read"},
		{"PAYER_APPROVER", "worklist.claim"},
		{"TENANT_ADMIN", "worklist.reassign"},
		{"TENANT_ADMIN", "workflow.queue.manage"},
		{"TENANT_ADMIN", "workflow.policy.manage"},
	} {
		found := false
		for _, role := range granted[want.permission] {
			if role == want.role {
				found = true
			}
		}
		if !found {
			t.Fatalf("role %s does not hold %s", want.role, want.permission)
		}
	}
}
