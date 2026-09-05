package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	notificationapp "github.com/celikbros/kapsora/internal/notification/application"
	"github.com/celikbros/kapsora/internal/notification/domain"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/servicerequest/application"
	servicerequestdomain "github.com/celikbros/kapsora/internal/servicerequest/domain"
)

// notifyRow is one row of system.outbox_event as this test reads it back.
type notifyRow struct {
	EventCode     string `json:"eventCode"`
	RecipientType string `json:"recipientType"`
	RecipientID   string `json:"recipientId"`
	Channel       string `json:"channel"`
	Locale        string `json:"locale"`
	DedupeKey     string `json:"dedupeKey"`
	Variables     map[string]string
}

// notifications reads every notification the tenant has published so far, in a stable
// order. It reads the outbox rather than the message table on purpose: the outbox row is
// what a business command actually writes, and it is the thing that a rolled-back command
// must not have left behind.
func (f *fixture) notifications(t *testing.T) []notifyRow {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	rows, err := f.h.Admin.Query(ctx, `
		SELECT payload_json, deduplication_key
		  FROM system.outbox_event
		 WHERE tenant_id = $1 AND event_type = $2
		 ORDER BY occurred_at, id`, f.tenant, notificationapp.NotifyRequestedEvent)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	defer rows.Close()
	var out []notifyRow
	for rows.Next() {
		var payload []byte
		var key *string
		if err := rows.Scan(&payload, &key); err != nil {
			t.Fatalf("scan outbox row: %v", err)
		}
		var row notifyRow
		if err := json.Unmarshal(payload, &row); err != nil {
			t.Fatalf("decode outbox payload: %v", err)
		}
		var vars struct {
			Variables map[string]string `json:"variables"`
		}
		if err := json.Unmarshal(payload, &vars); err != nil {
			t.Fatalf("decode variables: %v", err)
		}
		row.Variables = vars.Variables
		if key == nil || *key == "" {
			t.Fatalf("a notification event was published with no deduplication key: %+v", row)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	return out
}

// fingerprints is the set of (recipient, channel) pairs a batch of notifications reaches,
// which is what "exactly one message per recipient" is counted over.
func fingerprints(rows []notifyRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.EventCode+"|"+r.RecipientType+"|"+r.RecipientID+"|"+r.Channel)
	}
	sort.Strings(out)
	return out
}

// TestDecidingARequestTellsTheMemberAndTheProviderOnce is the acceptance criterion: a
// decided request tells the member and the provider, once, with nothing clinical on it.
func TestDecidingARequestTellsTheMemberAndTheProviderOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	submitted := f.submit(t, f.create(t))
	if got := len(f.notifications(t)); got != 0 {
		t.Fatalf("submitting a request that needs review published %d notifications, want 0", got)
	}

	approved, err := f.svc.Approve(ctx, f.rc(), submitted.Request.ID, application.DecisionInput{
		ReasonCode: "MEDICALLY_NECESSARY", ExpectedVersion: submitted.Request.RowVersion,
	})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}

	rows := f.notifications(t)
	// Two recipients — the member and the provider organization — on two channels each.
	want := []string{
		notificationapp.EventServiceRequestDecided + "|ORGANIZATION|" + f.providerOr.String() + "|EMAIL",
		notificationapp.EventServiceRequestDecided + "|ORGANIZATION|" + f.providerOr.String() + "|INAPP",
		notificationapp.EventServiceRequestDecided + "|PERSON|" + f.person.String() + "|EMAIL",
		notificationapp.EventServiceRequestDecided + "|PERSON|" + f.person.String() + "|INAPP",
	}
	if got := fingerprints(rows); !equalStrings(got, want) {
		t.Fatalf("notifications = %v, want %v", got, want)
	}

	keys := map[string]bool{}
	for _, row := range rows {
		if keys[row.DedupeKey] {
			t.Fatalf("two notifications share the deduplication key %q", row.DedupeKey)
		}
		keys[row.DedupeKey] = true
		if row.Locale != notificationapp.DefaultLocale {
			t.Fatalf("locale = %q, want %s", row.Locale, notificationapp.DefaultLocale)
		}
		// What the message may carry, and the whole of it. A reason code, a review
		// comment and a line of the request are all absent, and the catalogue would
		// refuse them anyway — this asserts the publisher never tries.
		if len(row.Variables) != 4 {
			t.Fatalf("variables = %v, want exactly the four safe ones", row.Variables)
		}
		if row.Variables[domain.VarReferenceNo] != approved.Request.Reference {
			t.Fatalf("reference_no = %q, want %s", row.Variables[domain.VarReferenceNo], approved.Request.Reference)
		}
		if row.Variables[domain.VarStatusCode] != servicerequestdomain.StatusApproved {
			t.Fatalf("status_code = %q, want APPROVED", row.Variables[domain.VarStatusCode])
		}
		if row.Variables[domain.VarDeepLink] != "/requests/"+approved.Request.ID.String() {
			t.Fatalf("deep_link = %q", row.Variables[domain.VarDeepLink])
		}
		if err := domain.ScreenVariables(row.Variables); err != nil {
			t.Fatalf("the published variables are not safe: %v", err)
		}
	}
}

// TestARolledBackDecisionNotifiesNobody is the other half of publishing inside the
// command's own transaction: a decision that did not happen has told nobody it did.
func TestARolledBackDecisionNotifiesNobody(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	submitted := f.submit(t, f.create(t))

	// A stale If-Match. The command locks, finds the version does not match and returns,
	// which rolls the whole transaction back — the outbox row included.
	_, err := f.svc.Approve(ctx, f.rc(), submitted.Request.ID, application.DecisionInput{
		ReasonCode: "MEDICALLY_NECESSARY", ExpectedVersion: submitted.Request.RowVersion + 99,
	})
	if err == nil {
		t.Fatal("an approval against a stale version was accepted")
	}
	if got := f.notifications(t); len(got) != 0 {
		t.Fatalf("a rolled-back approval published %d notifications: %+v", len(got), got)
	}

	// A refused line decision rolls back the same way, after the status write rather than
	// before it — the notification is published last, and it still leaves nothing.
	_, err = f.svc.Approve(ctx, f.rc(), submitted.Request.ID, application.DecisionInput{
		ReasonCode: "MEDICALLY_NECESSARY", ExpectedVersion: submitted.Request.RowVersion,
		Items: []servicerequestdomain.DecisionItem{{LineNo: 99, Status: servicerequestdomain.ItemApproved}},
	})
	if err == nil {
		t.Fatal("an approval naming a line the request does not have was accepted")
	}
	if got := f.notifications(t); len(got) != 0 {
		t.Fatalf("a refused approval published %d notifications: %+v", len(got), got)
	}
}

// TestAPublishInsideARolledBackTransactionLeavesNothing is the property the commands rely
// on, tested where it can actually be observed.
//
// The command-level test above proves that a refused command notifies nobody, but it can
// only refuse commands that fail before the publish — every failure this package can
// produce happens at the lock or the validation, which are both earlier. So the
// transactional half is asserted directly against the same helper the commands call: the
// rows are absent when the transaction rolls back and present when it commits, and nothing
// about which of those happened is decided by the notification.
func TestAPublishInsideARolledBackTransactionLeavesNothing(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	person := notificationapp.PersonRecipient(f.person)
	publish := func(key string) func(context.Context, pgx.Tx) error {
		return func(ctx context.Context, tx pgx.Tx) error {
			return notificationapp.PublishNotification(ctx, tx, f.tenant, notificationapp.Notification{
				EventCode: notificationapp.EventServiceRequestDecided, Key: key,
				Recipients: []notificationapp.Recipient{person},
				Variables: map[string]string{
					domain.VarReferenceNo: "SR-20260615-ROLLBACK",
					domain.VarStatusCode:  "APPROVED",
					domain.VarEventDate:   "2026-06-15",
					domain.VarDeepLink:    "/requests/0199bd4e-6a1e-7a9c-8f31-2b7c0d5e4a11",
				},
			})
		}
	}

	boom := errors.New("the command failed after publishing")
	err := db.WithTenantTx(ctx, f.h.App, db.TenantContext{TenantID: f.tenant},
		func(ctx context.Context, tx pgx.Tx) error {
			if err := publish("rolled-back")(ctx, tx); err != nil {
				return err
			}
			return boom
		})
	if !errors.Is(err, boom) {
		t.Fatalf("the transaction did not fail: %v", err)
	}
	if got := f.notifications(t); len(got) != 0 {
		t.Fatalf("a rolled-back transaction left %d notifications: %+v", len(got), got)
	}

	// The same publish, committed. Without this half the assertion above would also pass
	// for a helper that publishes nothing at all.
	if err := db.WithTenantTx(ctx, f.h.App, db.TenantContext{TenantID: f.tenant},
		publish("committed")); err != nil {
		t.Fatalf("commit: %v", err)
	}
	rows := f.notifications(t)
	want := []string{
		notificationapp.EventServiceRequestDecided + "|PERSON|" + f.person.String() + "|EMAIL",
		notificationapp.EventServiceRequestDecided + "|PERSON|" + f.person.String() + "|INAPP",
	}
	if got := fingerprints(rows); !equalStrings(got, want) {
		t.Fatalf("notifications = %v, want %v", got, want)
	}
}

// TestReturnAndRejectAndTheGateEachPublishTheirOwnEvent: the four decisions a request can
// receive are one event with different status words, and a request the gate parks on
// PENDING_DOCUMENT is the provider being asked for something rather than a decision.
func TestReturnAndRejectAndTheGateEachPublishTheirOwnEvent(t *testing.T) {
	t.Run("a return tells both sides that somebody answered", func(t *testing.T) {
		f := newFixture(t)
		submitted := f.submit(t, f.create(t))
		if _, err := f.svc.Return(context.Background(), f.rc(), submitted.Request.ID,
			application.ReasonInput{
				ReasonCode: "MISSING_DOCUMENT", ExpectedVersion: submitted.Request.RowVersion,
			}); err != nil {
			t.Fatalf("return: %v", err)
		}
		rows := f.notifications(t)
		if len(rows) != 4 {
			t.Fatalf("a return published %d notifications, want 4", len(rows))
		}
		for _, row := range rows {
			if row.EventCode != notificationapp.EventServiceRequestDecided {
				t.Fatalf("event = %s", row.EventCode)
			}
			// Not "DRAFT", which is where the request actually lands: a member told their
			// request was DRAFT would think it had been forgotten.
			if row.Variables[domain.VarStatusCode] != "RETURNED" {
				t.Fatalf("status_code = %q, want RETURNED", row.Variables[domain.VarStatusCode])
			}
			if _, carried := row.Variables["reason_code"]; carried {
				t.Fatal("the return reason reached the notification")
			}
		}
	})

	t.Run("a rejection carries its status and not its reason", func(t *testing.T) {
		f := newFixture(t)
		submitted := f.submit(t, f.create(t))
		if _, err := f.svc.Reject(context.Background(), f.rc(), submitted.Request.ID,
			application.ReasonInput{
				ReasonCode: "NOT_COVERED", ExpectedVersion: submitted.Request.RowVersion,
				ReasonText: strPtrTest("Plan bu hizmeti kapsamıyor."),
			}); err != nil {
			t.Fatalf("reject: %v", err)
		}
		rows := f.notifications(t)
		if len(rows) != 4 {
			t.Fatalf("a rejection published %d notifications, want 4", len(rows))
		}
		for _, row := range rows {
			if row.Variables[domain.VarStatusCode] != servicerequestdomain.StatusRejected {
				t.Fatalf("status_code = %q, want REJECTED", row.Variables[domain.VarStatusCode])
			}
			for _, value := range row.Variables {
				if value == "Plan bu hizmeti kapsamıyor." {
					t.Fatal("the reviewer's own words reached the notification")
				}
			}
		}
	})

	t.Run("a gate that asks for a document tells only the provider", func(t *testing.T) {
		f := newFixture(t)
		f.publishRule(t, "DOCUMENT", "NEED_REPORT", "true",
			`[{"type":"REQUIRE_DOCUMENT","payload":{"documentTypeCode":"MEDICAL_REPORT"}}]`)
		submitted := f.submit(t, f.create(t))
		if submitted.Request.Status != servicerequestdomain.StatusPendingDocument {
			t.Fatalf("status = %s, want PENDING_DOCUMENT", submitted.Request.Status)
		}
		rows := f.notifications(t)
		want := []string{
			notificationapp.EventServiceRequestPendingDocument + "|ORGANIZATION|" + f.providerOr.String() + "|EMAIL",
			notificationapp.EventServiceRequestPendingDocument + "|ORGANIZATION|" + f.providerOr.String() + "|INAPP",
		}
		if got := fingerprints(rows); !equalStrings(got, want) {
			t.Fatalf("notifications = %v, want %v — the member is not asked for what only the provider can upload", got, want)
		}
	})

	t.Run("an auto-approving gate is a decision like any other", func(t *testing.T) {
		f := newFixture(t)
		f.autoApprove(t)
		submitted := f.submit(t, f.create(t))
		if submitted.Request.Status != servicerequestdomain.StatusApproved {
			t.Fatalf("status = %s, want APPROVED", submitted.Request.Status)
		}
		if got := len(f.notifications(t)); got != 4 {
			t.Fatalf("an auto-approval published %d notifications, want 4", got)
		}
	})
}

// TestOneRequestWithNoProviderTellsOnlyTheMember: a request nobody provides is an ordinary
// request, and having nobody on the provider side to tell is not a reason to refuse it.
func TestOneRequestWithNoProviderTellsOnlyTheMember(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	in := f.input()
	in.RequestType = "REIMBURSEMENT"
	in.ProviderOrganizationID = nil
	view, err := f.svc.Create(ctx, f.rc(), in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	submitted := f.submit(t, view)
	if _, err := f.svc.Reject(ctx, f.rc(), submitted.Request.ID, application.ReasonInput{
		ReasonCode: "NOT_COVERED", ExpectedVersion: submitted.Request.RowVersion,
	}); err != nil {
		t.Fatalf("reject: %v", err)
	}
	want := []string{
		notificationapp.EventServiceRequestDecided + "|PERSON|" + f.person.String() + "|EMAIL",
		notificationapp.EventServiceRequestDecided + "|PERSON|" + f.person.String() + "|INAPP",
	}
	if got := fingerprints(f.notifications(t)); !equalStrings(got, want) {
		t.Fatalf("notifications = %v, want %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func strPtrTest(s string) *string { return &s }

var _ = uuid.Nil
