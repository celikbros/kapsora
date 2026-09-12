package application_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/workflow/application"
	"github.com/celikbros/kapsora/internal/workflow/domain"
	workflowpg "github.com/celikbros/kapsora/internal/workflow/infrastructure/postgres"
)

// testNow pins the service clock so a window is deterministic. The SLA arithmetic itself
// is done by the database with its own now(), which is the point: the snapshot is taken
// where the row is written, not where the caller happens to be.
var testNow = time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)

const (
	// reviewSLA is short enough that the escalation sweep can be asked about a moment
	// past it without waiting.
	reviewSLA = 60
	// patientSLA is a week: an item raised under it is not due at any moment these tests
	// ask about.
	patientSLA = 10080
	// claimers is how many callers race for one item in the concurrency test.
	claimers = 20
)

type fixture struct {
	h    *dbtest.Harness
	pool *pgxpool.Pool
	svc  *application.Service
	logs *bytes.Buffer

	tenant uuid.UUID
	actor  uuid.UUID
	// rivals are the callers of the concurrency test, one actor each: the loser has to be
	// told who won, and that is only checkable when the winner is somebody in particular.
	rivals []uuid.UUID

	reviewQueue   uuid.UUID
	escalateQueue uuid.UUID
	patientQueue  uuid.UUID
	// lonelyQueue keeps a clock but names nowhere to escalate to.
	lonelyQueue uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h := dbtest.New(t)

	// The concurrency test needs one connection per goroutine in flight; the harness pool
	// is deliberately small, so this one is opened against the same app role.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.NewPool(ctx, h.AppURL, db.PoolOptions{ApplicationName: "workflow-test", MaxConns: 24})
	if err != nil {
		t.Fatalf("app pool: %v", err)
	}
	t.Cleanup(pool.Close)

	cursors, err := httpx.NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	svc, err := application.New(application.Deps{
		Pool: pool, Repo: workflowpg.New(), Audit: auditpg.New(), Cursors: cursors,
		Logger: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:    func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}

	f := &fixture{h: h, pool: pool, svc: svc, logs: logs}
	f.seed(t)
	return f
}

func (f *fixture) seed(t *testing.T) {
	t.Helper()
	h := f.h
	f.tenant = h.CreateTenant("WORKFLOW")
	f.actor = h.CreateActor("workflow-reviewer", "Workflow Reviewer")
	for i := range claimers {
		f.rivals = append(f.rivals, h.CreateActor(
			"workflow-rival-"+string(rune('a'+i)), "Workflow Rival"))
	}

	f.escalateQueue = f.mustQueue(t, application.NewQueueInput{
		Code: "SUPERVISOR", Name: "Süpervizör", DomainCode: "GENERIC",
	})
	f.reviewQueue = f.mustQueue(t, application.NewQueueInput{
		Code: "MEDICAL_REVIEW", Name: "Tıbbi inceleme", DomainCode: "HEALTH",
		SLAMinutes: intp(reviewSLA), EscalationQueueID: &f.escalateQueue,
	})
	f.patientQueue = f.mustQueue(t, application.NewQueueInput{
		Code: "SLOW_REVIEW", Name: "Yavaş inceleme", DomainCode: "GENERIC",
		SLAMinutes: intp(patientSLA), EscalationQueueID: &f.escalateQueue,
	})
	f.lonelyQueue = f.mustQueue(t, application.NewQueueInput{
		Code: "NO_TARGET", Name: "Yönlendirmesiz", DomainCode: "GENERIC",
		SLAMinutes: intp(reviewSLA),
	})
}

// rc is a back office actor holding every permission of this package.
func (f *fixture) rc() identity.RequestContext { return f.rcFor(f.actor) }

func (f *fixture) rcFor(actorID uuid.UUID) identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal: identity.Principal{ActorID: actorID},
		Permissions: map[string]struct{}{
			application.PermissionRead: {}, application.PermissionClaim: {},
			application.PermissionReassign: {}, application.PermissionQueueManage: {},
			application.PermissionPolicy: {},
		},
	}
}

func (f *fixture) mustQueue(t *testing.T, in application.NewQueueInput) uuid.UUID {
	t.Helper()
	record, err := f.svc.CreateQueue(context.Background(), f.rc(), in)
	if err != nil {
		t.Fatalf("create queue %s: %v", in.Code, err)
	}
	return record.ID
}

func (f *fixture) raise(t *testing.T, queueID uuid.UUID, title string) application.ItemRecord {
	t.Helper()
	record, err := f.svc.Raise(context.Background(), f.rc(), application.RaiseInput{
		QueueID: queueID, AggregateType: "SERVICE_REQUEST", AggregateID: uuid.New(),
		Title: title,
	})
	if err != nil {
		t.Fatalf("raise %s: %v", title, err)
	}
	return record
}

func (f *fixture) get(t *testing.T, id uuid.UUID) application.ItemRecord {
	t.Helper()
	record, err := f.svc.GetItem(context.Background(), f.rc(), id)
	if err != nil {
		t.Fatalf("get work item: %v", err)
	}
	return record
}

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

func intp(n int) *int { return &n }

// showInt renders an optional minute count for a failure message; the pointer itself says
// nothing to whoever reads the output.
func showInt(n *int) string {
	if n == nil {
		return "none"
	}
	return strconv.Itoa(*n)
}

// --- claiming ------------------------------------------------------------------------

// TestClaimWorkItemIsNeverARace is the property the whole worklist rests on: twenty people
// reach for one piece of work, exactly one gets it, and the other nineteen are told who
// did. No arrangement of them may leave the item with two owners or with none.
func TestClaimWorkItemIsNeverARace(t *testing.T) {
	f := newFixture(t)
	item := f.raise(t, f.reviewQueue, "Yirmi kişinin uzandığı iş")

	results := make([]error, claimers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range claimers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, results[i] = f.svc.ClaimItem(context.Background(), f.rcFor(f.rivals[i]),
				item.ID, item.RowVersion)
		}(i)
	}
	close(start)
	wg.Wait()

	var (
		winners  []uuid.UUID
		refusals []*application.AlreadyClaimedError
	)
	for i, err := range results {
		var claimed *application.AlreadyClaimedError
		switch {
		case err == nil:
			winners = append(winners, f.rivals[i])
		case errors.As(err, &claimed):
			refusals = append(refusals, claimed)
		default:
			t.Fatalf("caller %d: unexpected error: %v", i, err)
		}
	}
	if len(winners) != 1 {
		t.Fatalf("winners = %d, want exactly one", len(winners))
	}
	if len(refusals) != claimers-1 {
		t.Fatalf("refusals = %d, want %d", len(refusals), claimers-1)
	}
	// Every loser is told who won, by id. A refusal that named nobody would leave two
	// people clicking the same button.
	for i, refusal := range refusals {
		if !errors.Is(refusal, application.ErrWorkItemAlreadyClaimed) {
			t.Fatalf("refusal %d does not match ErrWorkItemAlreadyClaimed", i)
		}
		if refusal.AssigneeActorID == nil {
			t.Fatalf("refusal %d names no assignee", i)
		}
		if *refusal.AssigneeActorID != winners[0] {
			t.Fatalf("refusal %d names %s, want the winner %s", i, refusal.AssigneeActorID, winners[0])
		}
		if refusal.WorkItemID != item.ID {
			t.Fatalf("refusal %d names work item %s, want %s", i, refusal.WorkItemID, item.ID)
		}
	}

	// The row itself: one owner, and that owner is the winner.
	final := f.get(t, item.ID)
	if final.Status != "CLAIMED" {
		t.Fatalf("status = %s, want CLAIMED", final.Status)
	}
	if final.AssigneeActorID == nil || *final.AssigneeActorID != winners[0] {
		t.Fatalf("assignee = %v, want the winner %s", final.AssigneeActorID, winners[0])
	}
	if n := f.count(t, `
		SELECT count(*) FROM workflow.work_item
		 WHERE tenant_id = $1 AND id = $2 AND assignee_actor_id = $3`,
		f.tenant, item.ID, winners[0]); n != 1 {
		t.Fatalf("rows owned by the winner = %d, want 1", n)
	}
	// One claim, one event. Nineteen refusals that had written a transition would make the
	// history say the item changed hands twenty times.
	if n := f.count(t, `
		SELECT count(*) FROM workflow.status_event
		 WHERE tenant_id = $1 AND aggregate_type = 'WORK_ITEM' AND aggregate_id = $2
		   AND transition_code = 'CLAIM'`, f.tenant, item.ID); n != 1 {
		t.Fatalf("CLAIM events = %d, want 1", n)
	}
}

// TestClaimWithAStaleVersionOnAnUnclaimedItemIsNotAClaimByAnybody: an If-Match that has
// simply gone stale is a 412, not a 409 naming nobody.
func TestClaimWithAStaleVersionOnAnUnclaimedItemIsNotAClaimByAnybody(t *testing.T) {
	f := newFixture(t)
	item := f.raise(t, f.reviewQueue, "Sürümü eskimiş iş")

	// A comment leaves the item alone, so the version is moved by a reassignment away and
	// a release back: the item ends OPEN at a version the caller no longer holds.
	if _, err := f.svc.ReassignItem(context.Background(), f.rc(), item.ID, f.actor,
		"ROTA", "", item.RowVersion); err != nil {
		t.Fatalf("reassign: %v", err)
	}
	moved := f.get(t, item.ID)
	if _, err := f.svc.ReleaseItem(context.Background(), f.rc(), item.ID, "", "", moved.RowVersion); err != nil {
		t.Fatalf("release: %v", err)
	}

	_, err := f.svc.ClaimItem(context.Background(), f.rcFor(f.rivals[0]), item.ID, item.RowVersion)
	if !errors.Is(err, application.ErrVersionMismatch) {
		t.Fatalf("err = %v, want ErrVersionMismatch", err)
	}
	if errors.Is(err, application.ErrWorkItemAlreadyClaimed) {
		t.Fatal("a stale version on an unclaimed item must not claim to name a winner")
	}
}

// TestReleaseAndCompleteBelongToTheHolder: putting work down and saying what it decided
// are the holder's to do. Somebody else's is a different command with its own permission.
func TestReleaseAndCompleteBelongToTheHolder(t *testing.T) {
	f := newFixture(t)
	item := f.raise(t, f.reviewQueue, "Sahibi olan iş")

	claimed, err := f.svc.ClaimItem(context.Background(), f.rcFor(f.rivals[0]), item.ID, item.RowVersion)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := f.svc.ReleaseItem(context.Background(), f.rcFor(f.rivals[1]), item.ID,
		"", "", claimed.RowVersion); !errors.Is(err, application.ErrNotAssignee) {
		t.Fatalf("release by a stranger: err = %v, want ErrNotAssignee", err)
	}
	if _, err := f.svc.CompleteItem(context.Background(), f.rcFor(f.rivals[1]), item.ID,
		"APPROVED", "", claimed.RowVersion); !errors.Is(err, application.ErrNotAssignee) {
		t.Fatalf("complete by a stranger: err = %v, want ErrNotAssignee", err)
	}

	completed, err := f.svc.CompleteItem(context.Background(), f.rcFor(f.rivals[0]), item.ID,
		"APPROVED", "Onaylandı", claimed.RowVersion)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if completed.Status != "COMPLETED" || completed.OutcomeCode == nil || *completed.OutcomeCode != "APPROVED" {
		t.Fatalf("completed = %+v, want COMPLETED/APPROVED", completed)
	}
	if completed.CompletedBy == nil || *completed.CompletedBy != f.rivals[0] {
		t.Fatalf("completedBy = %v, want the holder %s", completed.CompletedBy, f.rivals[0])
	}
	// The optional note is stored as an internal comment on the same item.
	comments, err := f.svc.ListComments(context.Background(), f.rcFor(f.rivals[0]), item.ID, nil, 0)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if len(comments) != 1 || comments[0].Visibility != "INTERNAL" || comments[0].Body != "Onaylandı" {
		t.Fatalf("comments = %+v, want one internal note", comments)
	}
}

// TestReassignRecordsWhoHadItBefore: the item no longer knows, so the history has to.
func TestReassignRecordsWhoHadItBefore(t *testing.T) {
	f := newFixture(t)
	item := f.raise(t, f.reviewQueue, "El değiştiren iş")

	claimed, err := f.svc.ClaimItem(context.Background(), f.rcFor(f.rivals[0]), item.ID, item.RowVersion)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	reassigned, err := f.svc.ReassignItem(context.Background(), f.rc(), item.ID, f.rivals[1],
		"ABSENCE", "izinli", claimed.RowVersion)
	if err != nil {
		t.Fatalf("reassign: %v", err)
	}
	if reassigned.AssigneeActorID == nil || *reassigned.AssigneeActorID != f.rivals[1] {
		t.Fatalf("assignee = %v, want %s", reassigned.AssigneeActorID, f.rivals[1])
	}
	if n := f.count(t, `
		SELECT count(*) FROM workflow.status_event
		 WHERE tenant_id = $1 AND aggregate_id = $2 AND transition_code = 'REASSIGN'
		   AND reason_code = 'ABSENCE'
		   AND metadata_json ->> 'previous_assignee_actor_id' = $3::text`,
		f.tenant, item.ID, f.rivals[0].String()); n != 1 {
		t.Fatalf("reassignment events naming the previous holder = %d, want 1", n)
	}
}

// TestAuditDetailSurvivesSanitisation is the trap this project has already fallen into
// once: audit.SanitizeDetail silently drops any key holding a name, an identifier or a
// secret, so a detail key chosen carelessly looks like it audits and does not. These are
// the keys the worklist commands actually write, read back out of the audit row.
func TestAuditDetailSurvivesSanitisation(t *testing.T) {
	f := newFixture(t)
	item := f.raise(t, f.reviewQueue, "Denetlenecek iş")

	claimed, err := f.svc.ClaimItem(context.Background(), f.rcFor(f.rivals[0]), item.ID, item.RowVersion)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := f.svc.ReassignItem(context.Background(), f.rc(), item.ID, f.rivals[1],
		"ROTA", "", claimed.RowVersion); err != nil {
		t.Fatalf("reassign: %v", err)
	}

	for _, tc := range []struct {
		action string
		keys   []string
	}{
		{"work_item.raise", []string{"queue_id", "aggregate_type", "aggregate_id", "priority", "sla_minutes"}},
		{"work_item.claim", []string{"queue_id", "assignee_actor_id"}},
		{"work_item.reassign", []string{"queue_id", "assignee_actor_id", "previous_assignee_actor_id", "reason_code"}},
	} {
		for _, key := range tc.keys {
			if n := f.count(t, `
				SELECT count(*) FROM audit.event
				 WHERE tenant_id = $1 AND resource_id = $2 AND action_code = $3
				   AND detail_json ? $4`, f.tenant, item.ID, tc.action, key); n != 1 {
				t.Fatalf("%s audit detail is missing %q (rows carrying it: %d); "+
					"SanitizeDetail drops a key it does not like without saying so",
					tc.action, key, n)
			}
		}
	}
}

// --- the SLA snapshot -----------------------------------------------------------------

// TestWorkItemKeepsTheClockItWasGiven is v1.2 11.8: retuning a queue must not make
// yesterday's items late, or make late ones on time.
func TestWorkItemKeepsTheClockItWasGiven(t *testing.T) {
	f := newFixture(t)
	item := f.raise(t, f.reviewQueue, "Dünkü iş")
	if item.SLAMinutesSnapshot == nil || *item.SLAMinutesSnapshot != reviewSLA {
		t.Fatalf("snapshot = %v, want %d", item.SLAMinutesSnapshot, reviewSLA)
	}
	if item.DueAt == nil {
		t.Fatal("an item raised into a queue with an SLA must have a due date")
	}
	before := *item.DueAt

	queues, err := f.svc.ListQueues(context.Background(), f.rc(), application.QueueFilter{Limit: 50})
	if err != nil {
		t.Fatalf("list queues: %v", err)
	}
	var version int64
	for _, q := range queues.Items {
		if q.ID == f.reviewQueue {
			version = q.RowVersion
		}
	}
	slower := 6000
	if _, err := f.svc.PatchQueue(context.Background(), f.rc(), f.reviewQueue,
		queuePatchSLA(&slower), version); err != nil {
		t.Fatalf("patch queue: %v", err)
	}

	after := f.get(t, item.ID)
	if after.SLAMinutesSnapshot == nil || *after.SLAMinutesSnapshot != reviewSLA {
		t.Fatalf("snapshot moved to %s; the clock an item was given is the clock it is judged by",
			showInt(after.SLAMinutesSnapshot))
	}
	if after.DueAt == nil || !after.DueAt.Equal(before) {
		t.Fatalf("dueAt moved from %s to %v when the queue was retuned", before, after.DueAt)
	}

	// And the new SLA does apply to work raised from now on.
	fresh := f.raise(t, f.reviewQueue, "Bugünkü iş")
	if fresh.SLAMinutesSnapshot == nil || *fresh.SLAMinutesSnapshot != slower {
		t.Fatalf("new item snapshot = %v, want %d", fresh.SLAMinutesSnapshot, slower)
	}
}

// --- escalation -----------------------------------------------------------------------

// TestEscalationMovesOverdueWorkExactlyOnce covers the three things the job promises:
// an overdue item moves to its queue's target, running again does nothing, and the due
// date does not move — a late item stays late.
func TestEscalationMovesOverdueWorkExactlyOnce(t *testing.T) {
	f := newFixture(t)
	item := f.raise(t, f.reviewQueue, "Geciken iş")
	dueAt := *item.DueAt
	past := time.Now().UTC().Add(2 * time.Hour)

	moved, err := f.svc.EscalateOverdue(context.Background(), past)
	if err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if moved != 1 {
		t.Fatalf("escalated = %d, want 1", moved)
	}
	after := f.get(t, item.ID)
	if after.QueueID != f.escalateQueue {
		t.Fatalf("queue = %s, want the escalation target %s", after.QueueID, f.escalateQueue)
	}
	if after.EscalatedFromQueueID == nil || *after.EscalatedFromQueueID != f.reviewQueue {
		t.Fatalf("escalatedFrom = %v, want %s", after.EscalatedFromQueueID, f.reviewQueue)
	}
	if after.Status != "OPEN" {
		t.Fatalf("status = %s; moving work does not decide it", after.Status)
	}
	if after.DueAt == nil || !after.DueAt.Equal(dueAt) {
		t.Fatalf("dueAt moved from %s to %v; a late item stays late", dueAt, after.DueAt)
	}

	// Twice is once. The second pass sees an item that is still overdue in its new queue
	// and leaves it alone, because escalated_at is already set.
	again, err := f.svc.EscalateOverdue(context.Background(), past.Add(time.Hour))
	if err != nil {
		t.Fatalf("second escalate: %v", err)
	}
	if again != 0 {
		t.Fatalf("second run escalated %d items, want 0", again)
	}
	if n := f.count(t, `
		SELECT count(*) FROM workflow.status_event
		 WHERE tenant_id = $1 AND aggregate_id = $2 AND transition_code = 'ESCALATE'`,
		f.tenant, item.ID); n != 1 {
		t.Fatalf("ESCALATE events = %d, want exactly 1", n)
	}
	settled := f.get(t, item.ID)
	if settled.QueueID != f.escalateQueue {
		t.Fatalf("queue moved again to %s", settled.QueueID)
	}
}

// TestEscalationMarksWorkWhoseQueueHasNowhereToSendIt: it is marked where it stands, which
// is what puts it on a report rather than leaving it late where nobody looks.
func TestEscalationMarksWorkWhoseQueueHasNowhereToSendIt(t *testing.T) {
	f := newFixture(t)
	item := f.raise(t, f.lonelyQueue, "Yönlendirilecek yeri olmayan iş")
	past := time.Now().UTC().Add(2 * time.Hour)

	if _, err := f.svc.EscalateOverdue(context.Background(), past); err != nil {
		t.Fatalf("escalate: %v", err)
	}
	after := f.get(t, item.ID)
	if after.Status != "ESCALATED" {
		t.Fatalf("status = %s, want ESCALATED", after.Status)
	}
	if after.QueueID != f.lonelyQueue {
		t.Fatalf("queue = %s; an item with no target is marked, not moved", after.QueueID)
	}
	if _, err := f.svc.EscalateOverdue(context.Background(), past.Add(time.Hour)); err != nil {
		t.Fatalf("second escalate: %v", err)
	}
	if n := f.count(t, `
		SELECT count(*) FROM workflow.status_event
		 WHERE tenant_id = $1 AND aggregate_id = $2 AND transition_code = 'ESCALATE'`,
		f.tenant, item.ID); n != 1 {
		t.Fatalf("ESCALATE events = %d, want exactly 1", n)
	}
}

// TestEscalationLeavesWorkThatIsNotYetDue, and work with no clock at all.
func TestEscalationLeavesWorkThatIsNotYetDue(t *testing.T) {
	f := newFixture(t)
	patient := f.raise(t, f.patientQueue, "Henüz vakti gelmemiş iş")
	timeless := f.raise(t, f.escalateQueue, "Saati olmayan iş")
	if timeless.DueAt != nil {
		t.Fatal("a queue with no SLA gives its items no due date")
	}
	past := time.Now().UTC().Add(2 * time.Hour)

	escalated, err := f.svc.EscalateOverdue(context.Background(), past)
	if err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if escalated != 0 {
		t.Fatalf("escalated = %d, want 0", escalated)
	}
	for _, item := range []application.ItemRecord{patient, timeless} {
		after := f.get(t, item.ID)
		if after.QueueID != item.QueueID || after.Status != "OPEN" || after.EscalatedAt != nil {
			t.Fatalf("item %s was touched: %+v", item.ID, after)
		}
	}
}

// --- approval policy --------------------------------------------------------------------

// TestApprovalPolicyLookupPicksTheBandAndThePeriod is what the commands that need a policy
// actually ask, and the amounts are exact decimal strings the whole way.
func TestApprovalPolicyLookupPicksTheBandAndThePeriod(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	mid := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	written, err := f.svc.PutPolicies(ctx, f.rc(), policySet("service_request.approve",
		policy("STANDARD", "0", "1000", 1, from, &mid),
		policy("STANDARD", "0", "1000", 2, mid, nil),
		policy("HIGH_VALUE", "1000.000001", "", 3, from, nil),
	))
	if err != nil {
		t.Fatalf("put policies: %v", err)
	}
	if len(written) != 3 {
		t.Fatalf("written = %d, want 3", len(written))
	}
	for _, record := range written {
		if record.VersionNo != 1 {
			t.Fatalf("first write got version %d, want 1", record.VersionNo)
		}
	}

	for _, tc := range []struct {
		name     string
		amount   string
		asOf     time.Time
		scope    string
		approver int
	}{
		{"inside the band, first period", "500", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), "STANDARD", 1},
		{"inside the band, second period", "500", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), "STANDARD", 2},
		{"one hundredth of a lira above the band", "1000.000001", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), "HIGH_VALUE", 3},
		{"far above the band", "9999999.999999", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), "HIGH_VALUE", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := f.svc.ResolvePolicy(ctx, f.rc(), application.PolicyLookup{
				ActionCode: "service_request.approve", Amount: tc.amount, AsOf: tc.asOf,
			})
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got.ScopeCode != tc.scope {
				t.Fatalf("scope = %s, want %s", got.ScopeCode, tc.scope)
			}
			if got.RequiredApproverCount != tc.approver {
				t.Fatalf("approvers = %d, want %d", got.RequiredApproverCount, tc.approver)
			}
		})
	}

	// A day before anything was in force answers nothing rather than the nearest thing.
	if _, err := f.svc.ResolvePolicy(ctx, f.rc(), application.PolicyLookup{
		ActionCode: "service_request.approve", Amount: "500",
		AsOf: time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC),
	}); !errors.Is(err, application.ErrPolicyNotFound) {
		t.Fatalf("err = %v, want ErrPolicyNotFound", err)
	}

	// Writing the set again replaces it and carries the version on, so the numbers say how
	// many times the answer has been changed.
	replaced, err := f.svc.PutPolicies(ctx, f.rc(), policySet("service_request.approve",
		policy("STANDARD", "", "", 4, from, nil),
	))
	if err != nil {
		t.Fatalf("replace policies: %v", err)
	}
	if len(replaced) != 1 || replaced[0].VersionNo != 2 {
		t.Fatalf("replaced = %+v, want one policy at version 2", replaced)
	}
	all, err := f.svc.ListPolicies(ctx, f.rc(), "service_request.approve", "")
	if err != nil {
		t.Fatalf("list policies: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("policies after replace = %d, want 1; a replace is not a merge", len(all))
	}
	if all[0].MinAmount != nil || all[0].MaxAmount != nil {
		t.Fatalf("an open band came back as %v..%v, want null..null", all[0].MinAmount, all[0].MaxAmount)
	}
}

// TestApprovalPolicyKeepsAmountsExact: a band edge survives the round trip digit for digit.
func TestApprovalPolicyKeepsAmountsExact(t *testing.T) {
	f := newFixture(t)
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	written, err := f.svc.PutPolicies(context.Background(), f.rc(), policySet("claim.approve",
		policy("EXACT", "0.000001", "12345678901234.999999", 2, from, nil),
	))
	if err != nil {
		t.Fatalf("put policies: %v", err)
	}
	if written[0].MinAmount == nil || *written[0].MinAmount != "0.000001" {
		t.Fatalf("minAmount = %v, want 0.000001", written[0].MinAmount)
	}
	if written[0].MaxAmount == nil || *written[0].MaxAmount != "12345678901234.999999" {
		t.Fatalf("maxAmount = %v, want 12345678901234.999999", written[0].MaxAmount)
	}
}

// TestApprovalPolicySetRefusesItsOwnOverlap: the caller is told which two entries collide,
// rather than being told only that a constraint fired.
func TestApprovalPolicySetRefusesItsOwnOverlap(t *testing.T) {
	f := newFixture(t)
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := f.svc.PutPolicies(context.Background(), f.rc(), policySet("invoice.approve",
		policy("STANDARD", "0", "100", 1, from, nil),
		policy("STANDARD", "100", "200", 2, from, nil),
	))
	if err == nil {
		t.Fatal("two policies covering the same scope and day must be refused")
	}
	if !containsField(err, "policies[1].validFrom", "OVERLAP") {
		t.Fatalf("err = %v, want a field error on policies[1].validFrom", err)
	}
	if n := f.count(t, `
		SELECT count(*) FROM workflow.approval_policy WHERE tenant_id = $1`, f.tenant); n != 0 {
		t.Fatalf("policies written = %d, want none; a refused set writes nothing", n)
	}
}

// --- helpers ----------------------------------------------------------------------------

// queuePatchSLA is the merge patch that only mentions the SLA. The double pointer is what
// distinguishes "not mentioned" from "cleared", and a test that wants to clear a clock
// passes a pointer to nil.
func queuePatchSLA(minutes *int) domain.QueuePatch {
	return domain.QueuePatch{SLAMinutes: &minutes}
}

func policySet(action string, policies ...domain.Policy) domain.PolicySet {
	return domain.PolicySet{ActionCode: action, Policies: policies}
}

func policy(scope, minAmount, maxAmount string, approvers int, from time.Time, to *time.Time) domain.Policy {
	return domain.Policy{
		ScopeCode: scope, MinAmount: minAmount, MaxAmount: maxAmount,
		RequiredRoleCodes: []string{"MEDICAL_REVIEWER"}, RequiredApproverCount: approvers,
		ValidFrom: from, ValidTo: to,
	}
}

// containsField reports whether the validation error names one field with one code.
func containsField(err error, field, code string) bool {
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		return false
	}
	for _, f := range ve.Fields {
		if f.Field == field && f.Code == code {
			return true
		}
	}
	return false
}

// TestQueuePermissionDecidesWhoSeesAndClaimsTheWork is migration 000048's rule in one test.
// A queue names the permission its work takes, and the worklist applies it on both sides: a
// caller who cannot do the work is not shown it, and cannot take it off the people who can.
// A queue that names nothing keeps the old meaning — every worklist reader.
func TestQueuePermissionDecidesWhoSeesAndClaimsTheWork(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	clinical := f.mustQueue(t, application.NewQueueInput{
		Code: "CLINICAL_REVIEW", Name: "Klinik inceleme", DomainCode: "HEALTH",
		RequiredPermission: "health.medical_report.review",
	})
	clinicalItem := f.raise(t, clinical, "Tıbbi rapor MR-1")
	openItem := f.raise(t, f.reviewQueue, "İncelenecek talep")

	reviewer := f.rcFor(f.actor)
	reviewer.Permissions["health.medical_report.review"] = struct{}{}

	titles := func(rc identity.RequestContext) map[string]bool {
		t.Helper()
		page, err := f.svc.ListItems(ctx, rc, application.ItemFilter{Limit: 50})
		if err != nil {
			t.Fatalf("list items: %v", err)
		}
		out := map[string]bool{}
		for _, item := range page.Items {
			out[item.Title] = true
		}
		return out
	}

	if seen := titles(reviewer); !seen[clinicalItem.Title] || !seen[openItem.Title] {
		t.Fatalf("the reviewer's worklist = %v, want both items", seen)
	}
	clerk := titles(f.rc())
	if clerk[clinicalItem.Title] {
		t.Fatalf("a caller without the permission saw the clinical queue's work: %v", clerk)
	}
	if !clerk[openItem.Title] {
		t.Fatalf("a queue that names no permission stopped being every reader's: %v", clerk)
	}

	// Not shown is not found: neither opening the item nor claiming it is a way in.
	if _, err := f.svc.GetItem(ctx, f.rc(), clinicalItem.ID); !errors.Is(err, application.ErrWorkItemNotFound) {
		t.Fatalf("get without the permission = %v, want not found", err)
	}
	if _, err := f.svc.ClaimItem(ctx, f.rc(), clinicalItem.ID, clinicalItem.RowVersion); !errors.Is(err, application.ErrWorkItemNotFound) {
		t.Fatalf("claim without the permission = %v, want not found", err)
	}
	if f.get(t, openItem.ID).Status != domain.StatusOpen {
		t.Fatal("the refused claim moved another queue's item")
	}

	// And the person who can do the work still takes it.
	claimed, err := f.svc.ClaimItem(ctx, reviewer, clinicalItem.ID, clinicalItem.RowVersion)
	if err != nil {
		t.Fatalf("claim by the reviewer: %v", err)
	}
	if claimed.Status != domain.StatusClaimed {
		t.Fatalf("claimed status = %s", claimed.Status)
	}
}
