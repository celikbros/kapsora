package application_test

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/celikbros/kapsora/internal/authorization/application"
	notificationapp "github.com/celikbros/kapsora/internal/notification/application"
	"github.com/celikbros/kapsora/internal/notification/domain"
)

// notifyRow is one row of system.outbox_event as this test reads it back.
type notifyRow struct {
	EventCode     string            `json:"eventCode"`
	RecipientType string            `json:"recipientType"`
	RecipientID   string            `json:"recipientId"`
	Channel       string            `json:"channel"`
	DedupeKey     string            `json:"dedupeKey"`
	Variables     map[string]string `json:"variables"`
}

// notifications reads what the tenant has published, newest last. It reads the outbox and
// not the message table because the outbox row is what a business command writes, and it
// is what a rolled-back command must not have left behind.
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
			t.Fatalf("decode payload: %v", err)
		}
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

func fingerprints(rows []notifyRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.EventCode+"|"+r.RecipientType+"|"+r.RecipientID+"|"+r.Channel)
	}
	sort.Strings(out)
	return out
}

// TestAuthorizationTellsTheMemberOnceAndNotAtAllWhenItRollsBack.
//
// The rollback half is the one worth having. An authorization that failed on its second
// line leaves no promise and no hold; it must also leave no message saying a promise was
// made, and the only thing that makes that true is that the publish happens inside the
// same transaction as the reservation.
func TestAuthorizationTellsTheMemberOnceAndNotAtAllWhenItRollsBack(t *testing.T) {
	f := newFixture(t)
	requestID := f.seedRequest(t, f.providerOr, line{f.physioDefinition, "1"})

	view := f.authorize(t, requestID, "auth-notify-1")
	rows := f.notifications(t)
	want := []string{
		notificationapp.EventAuthorizationApproved + "|PERSON|" + f.person.String() + "|EMAIL",
		notificationapp.EventAuthorizationApproved + "|PERSON|" + f.person.String() + "|INAPP",
	}
	if got := fingerprints(rows); !equalStrings(got, want) {
		t.Fatalf("notifications = %v, want %v", got, want)
	}
	for _, row := range rows {
		if row.Variables[domain.VarReferenceNo] != view.Authorization.Reference {
			t.Fatalf("reference_no = %q, want %s", row.Variables[domain.VarReferenceNo], view.Authorization.Reference)
		}
		if row.Variables[domain.VarExpiresAt] != view.Authorization.ValidTo.UTC().Format(time.DateOnly) {
			t.Fatalf("expires_at = %q", row.Variables[domain.VarExpiresAt])
		}
		if err := domain.ScreenVariables(row.Variables); err != nil {
			t.Fatalf("the published variables are not safe: %v", err)
		}
	}

	// The same key again replays the stored authorization rather than creating one, so it
	// publishes nothing new either.
	f.authorize(t, requestID, "auth-notify-1")
	if got := len(f.notifications(t)); got != len(want) {
		t.Fatalf("a replayed authorization published %d notifications in total, want %d", got, len(want))
	}

	// And an authorization that cannot reserve everything it needs tells nobody it did.
	before := len(f.notifications(t))
	half := f.seedRequest(t, f.providerOr,
		line{f.physioDefinition, "1"},
		line{f.dentalDefinition, "5"}, // the DENTAL account only has 2
	)
	if _, err := f.svc.Create(context.Background(), f.rc(), application.NewAuthorizationInput{
		RequestID: half, ValidTo: testNow.Add(24 * time.Hour), IdempotencyKey: "auth-notify-half",
	}); err == nil {
		t.Fatal("an authorization with an unreservable line was accepted")
	}
	if got := len(f.notifications(t)); got != before {
		t.Fatalf("a rolled-back authorization published %d new notifications", got-before)
	}
}

// TestExpiringSweepTellsEachMemberOnceForOneExpiryDay: the reminder is published once per
// authorization per expiry day, so an hourly job writes one message and twenty-three
// no-ops rather than twenty-four messages.
func TestExpiringSweepTellsEachMemberOnceForOneExpiryDay(t *testing.T) {
	f := newFixture(t)
	requestID := f.seedRequest(t, f.providerOr, line{f.physioDefinition, "1"})
	view := f.authorize(t, requestID, "auth-expiring")

	// Move the promise's end to three days out, which is when the reminder is due.
	target := testNow.AddDate(0, 0, application.ExpiringNoticeDays).
		Truncate(24 * time.Hour).Add(9 * time.Hour)
	f.h.AdminExec(`UPDATE service.authorization SET valid_to = $3 WHERE tenant_id = $1 AND id = $2`,
		f.tenant, view.Authorization.ID, target)

	before := len(f.notifications(t))
	published, err := f.svc.NotifyExpiringAuthorizations(context.Background(), testNow)
	if err != nil {
		t.Fatalf("expiring sweep: %v", err)
	}
	if published != 1 {
		t.Fatalf("the sweep published %d reminders, want 1", published)
	}
	rows := f.notifications(t)[before:]
	want := []string{
		notificationapp.EventAuthorizationExpiring + "|PERSON|" + f.person.String() + "|EMAIL",
		notificationapp.EventAuthorizationExpiring + "|PERSON|" + f.person.String() + "|INAPP",
	}
	if got := fingerprints(rows); !equalStrings(got, want) {
		t.Fatalf("reminders = %v, want %v", got, want)
	}

	// A second pass the same day publishes the same events, and the outbox refuses them:
	// the deduplication key carries the authorization and the expiry date, not the clock.
	if _, err := f.svc.NotifyExpiringAuthorizations(context.Background(), testNow.Add(time.Hour)); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if got := len(f.notifications(t)) - before; got != len(want) {
		t.Fatalf("two sweeps of the same day left %d reminders, want %d", got, len(want))
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
