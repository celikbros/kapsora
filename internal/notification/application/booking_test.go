package application_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/notification/application"
	"github.com/celikbros/kapsora/internal/notification/domain"
)

// The accommodation events of WP-I6-04 section 2.5, end to end: a shipped template renders
// for each of them, exactly one message reaches each recipient on each channel, a replayed
// command adds nothing, a rolled back one leaves nothing, and nothing a booking could carry
// that a member must not receive gets through.

// publishSeedTemplates publishes both shipped templates of one event, the way cmd/seed
// does. It goes through the ordinary CreateTemplate/PublishTemplate path on purpose: a
// template the publishing gate would refuse must be refused here too, so a shipped template
// that no longer satisfies the catalogue fails this test rather than the seed run.
func (f *fixture) publishSeedTemplates(t *testing.T, eventCode string) int {
	t.Helper()
	ctx := f.ctx(t)
	published := 0
	for _, tpl := range application.SeedTemplates() {
		if tpl.EventCode != eventCode {
			continue
		}
		draft, err := f.svc.CreateTemplate(ctx, f.rc(), application.NewTemplateInput{
			EventCode: tpl.EventCode, Channel: tpl.Channel, Locale: application.DefaultLocale,
			Subject: tpl.Subject, Body: tpl.Body, DeclaredVariables: tpl.Variables,
		})
		if err != nil {
			t.Fatalf("create %s/%s: %v", tpl.EventCode, tpl.Channel, err)
		}
		if _, err := f.svc.PublishTemplate(ctx, f.rc(), draft.ID, draft.RowVersion); err != nil {
			t.Fatalf("publish %s/%s: %v", tpl.EventCode, tpl.Channel, err)
		}
		published++
	}
	if published != 2 {
		t.Fatalf("%s ships %d templates, want an EMAIL and an INAPP", eventCode, published)
	}
	return published
}

// bookingVariables is one legitimate value per name the event's templates declare, taken
// from the same catalogue the refusal test uses.
func bookingVariables(t *testing.T, eventCode string) map[string]string {
	t.Helper()
	for _, tpl := range application.SeedTemplates() {
		if tpl.EventCode == eventCode {
			return variablesFor(tpl)
		}
	}
	t.Fatalf("no shipped template for %s", eventCode)
	return nil
}

func (f *fixture) messagesFor(t *testing.T, eventCode string) []application.MessageRecord {
	t.Helper()
	var out []application.MessageRecord
	for _, m := range f.messages(t) {
		if m.EventCode == eventCode {
			out = append(out, m)
		}
	}
	return out
}

// TestEveryBookingEventShipsTemplatesAndRecipients keeps the three tables that describe an
// event in step. It is a cheap test that catches the expensive mistake: an event added to
// the code with no template, which the seed would publish nothing for and which would then
// suppress every message it ever raised with NO_TEMPLATE, silently.
func TestEveryBookingEventShipsTemplatesAndRecipients(t *testing.T) {
	wired := map[string]bool{}
	for _, e := range application.WiredEvents {
		wired[e] = true
	}
	templates := map[string]map[string]bool{}
	for _, tpl := range application.SeedTemplates() {
		if templates[tpl.EventCode] == nil {
			templates[tpl.EventCode] = map[string]bool{}
		}
		templates[tpl.EventCode][tpl.Channel] = true
	}

	if len(application.BookingEvents) != 7 {
		t.Fatalf("%d booking events, want the seven of WP-I6-04", len(application.BookingEvents))
	}
	for _, event := range application.BookingEvents {
		if !wired[event] {
			t.Errorf("%s is a booking event but is not in WiredEvents; the seed would skip it", event)
		}
		if !domain.ValidEventCode(event) {
			t.Errorf("%s is not a valid event code; notification.template would refuse it", event)
		}
		if !templates[event][domain.ChannelEmail] || !templates[event][domain.ChannelInApp] {
			t.Errorf("%s ships %+v, want an EMAIL and an INAPP", event, templates[event])
		}
		recipients := application.BookingRecipients[event]
		if len(recipients) == 0 {
			t.Errorf("%s tells nobody", event)
		}
		for _, kind := range recipients {
			if kind != application.RecipientMember && kind != application.RecipientProperty {
				t.Errorf("%s has recipient kind %q", event, kind)
			}
		}
	}

	// The property hears about the two things that affect it and nothing else. This is the
	// privacy half of the table: a hotel does not learn that a member is waiting on an
	// approval, was reminded, or was offered a room somebody else gave up.
	for _, event := range []string{application.EventBookingConfirmed, application.EventBookingCancelled} {
		if len(application.BookingRecipients[event]) != 2 {
			t.Errorf("%s does not reach the property", event)
		}
	}
	for _, event := range []string{
		application.EventBookingHeld,
		application.EventBookingPendingApproval,
		application.EventBookingReminder,
		application.EventBookingNoShowReported,
		application.EventBookingOffered,
	} {
		for _, kind := range application.BookingRecipients[event] {
			if kind == application.RecipientProperty {
				t.Errorf("%s reaches the property; only the member should hear about it", event)
			}
		}
	}
}

// TestABookingEventNotifiesEachRecipientOnceOnEachChannel is section 3, third bullet: one
// message per recipient per channel through the outbox, and a redelivered command adds
// nothing.
func TestABookingEventNotifiesEachRecipientOnceOnEachChannel(t *testing.T) {
	f := newFixture(t)
	event := application.EventBookingConfirmed
	f.publishSeedTemplates(t, event)

	member := f.seedMemberWithContacts(t)
	bookingID := uuid.New()
	notification := application.Notification{
		EventCode:  event,
		Key:        bookingID.String() + ":CONFIRMED",
		Recipients: []application.Recipient{application.PersonRecipient(member)},
		Variables:  bookingVariables(t, event),
	}

	publish := func() {
		t.Helper()
		if err := f.tenantTx(t, func(ctx context.Context, tx pgx.Tx) error {
			return application.PublishNotification(ctx, tx, f.tenant, notification)
		}); err != nil {
			t.Fatalf("publish %s: %v", event, err)
		}
	}
	publish()
	// The same command run twice: a retried request, a replayed job. The dedupe key is
	// derived from the booking and the status rather than from a clock, so the second copy
	// is refused.
	publish()

	f.drain(t)

	got := f.messagesFor(t, event)
	if len(got) != 2 {
		t.Fatalf("%d messages for one confirmation, want one EMAIL and one INAPP: %+v", len(got), got)
	}
	channels := map[string]int{}
	for _, m := range got {
		channels[m.Channel]++
		if m.RecipientID != member || m.RecipientType != domain.RecipientPerson {
			t.Errorf("a message went to %s/%s, not the member", m.RecipientType, m.RecipientID)
		}
	}
	if channels[domain.ChannelEmail] != 1 || channels[domain.ChannelInApp] != 1 {
		t.Fatalf("channels = %+v, want one of each", channels)
	}

	// Publishing a third time after the messages exist still writes nothing new: the
	// message table's unique dedupe key is the second lock, after the outbox's.
	publish()
	f.drain(t)
	if again := f.messagesFor(t, event); len(again) != 2 {
		t.Fatalf("%d messages after a third publish, want two", len(again))
	}

	// The rendered text carries the property and the amount, and nothing that could be
	// misused: no voucher token, no identity number, no clinical word.
	f.scanStoredMessagesFor(t, tckn, diagnosis, comment)
}

// TestABookingEventTellsBothSidesWhenItShould walks the recipient table for real: a
// confirmation reaches the member and the property, and each of them once per channel.
func TestABookingEventTellsBothSidesWhenItShould(t *testing.T) {
	f := newFixture(t)
	event := application.EventBookingCancelled
	f.publishSeedTemplates(t, event)

	member := f.seedMemberWithContacts(t)
	property := f.h.CreateTenantOrganization(f.tenant, "Demo Sahil Otel", "PROVIDER")
	key := uuid.New().String() + ":CANCELLED"
	if err := f.tenantTx(t, func(ctx context.Context, tx pgx.Tx) error {
		return application.PublishNotification(ctx, tx, f.tenant, application.Notification{
			EventCode: event, Key: key,
			Recipients: []application.Recipient{
				application.PersonRecipient(member),
				application.OrganizationRecipient(property),
			},
			Variables: bookingVariables(t, event),
		})
	}); err != nil {
		t.Fatalf("publish %s: %v", event, err)
	}
	f.drain(t)

	perRecipient := map[uuid.UUID]map[string]int{}
	for _, m := range f.messagesFor(t, event) {
		if perRecipient[m.RecipientID] == nil {
			perRecipient[m.RecipientID] = map[string]int{}
		}
		perRecipient[m.RecipientID][m.Channel]++
	}
	if len(perRecipient) != 2 {
		t.Fatalf("a cancellation reached %d recipients, want the member and the property", len(perRecipient))
	}
	for id, channels := range perRecipient {
		if channels[domain.ChannelEmail] != 1 || channels[domain.ChannelInApp] != 1 {
			t.Errorf("recipient %s got %+v, want one of each channel", id, channels)
		}
	}
	if perRecipient[member] == nil || perRecipient[property] == nil {
		t.Fatalf("recipients = %+v, want the member %s and the property %s",
			perRecipient, member, property)
	}
}

// TestARolledBackBookingCommandNotifiesNobody is the same guarantee the older events have,
// asserted for the path WP-I6-02 will actually use: PublishNotification writes inside the
// caller's transaction and nowhere else.
func TestARolledBackBookingCommandNotifiesNobody(t *testing.T) {
	f := newFixture(t)
	event := application.EventBookingHeld
	f.publishSeedTemplates(t, event)

	member := f.seedMemberWithContacts(t)
	wanted := errors.New("the hold could not be taken")
	err := f.tenantTx(t, func(ctx context.Context, tx pgx.Tx) error {
		if err := application.PublishNotification(ctx, tx, f.tenant, application.Notification{
			EventCode: event, Key: uuid.NewString() + ":HELD",
			Recipients: []application.Recipient{application.PersonRecipient(member)},
			Variables:  bookingVariables(t, event),
		}); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM system.outbox_event WHERE event_type = $1`,
			application.NotifyRequestedEvent).Scan(&n); err != nil {
			return err
		}
		if n != 2 {
			return fmt.Errorf("the publish wrote %d rows inside its own transaction, want two", n)
		}
		return wanted
	})
	if !errors.Is(err, wanted) {
		t.Fatalf("the transaction ended with %v, want the business failure", err)
	}

	if n := f.countRows(t, "system.outbox_event"); n != 0 {
		t.Fatalf("%d outbox events survived a rolled back hold", n)
	}
	f.drain(t)
	if n := f.countRows(t, "notification.message"); n != 0 {
		t.Fatalf("%d messages exist after a rolled back hold", n)
	}
	if got := len(f.sender.sent()); got != 0 {
		t.Fatalf("the channel was asked to send %d times after a rolled back hold", got)
	}
}

// TestABookingMessageRefusesAVoucherTokenOrAnIdentifier is the refusal that matters most
// for this vertical. A booking has a voucher whose plaintext opens a door; a member has a
// TCKN. Neither has a slot in the catalogue, so the attempt to smuggle one in through the
// nearest-looking name is refused and nothing at all is written.
func TestABookingMessageRefusesAVoucherTokenOrAnIdentifier(t *testing.T) {
	f := newFixture(t)
	event := application.EventBookingConfirmed
	f.publishSeedTemplates(t, event)

	member := f.seedMemberWithContacts(t)
	const voucherToken = "9f2ab41c7d004e2f8a1b6c3d5e7f0912"

	payloads := map[string]struct {
		name, value string
	}{
		"a voucher token as the reference":    {domain.VarReferenceNo, voucherToken},
		"a voucher token in the link":         {domain.VarDeepLink, "/vouchers/redeem?token=" + voucherToken},
		"a voucher token as the link itself":  {domain.VarDeepLink, "https://kapsora.example/v/" + voucherToken},
		"an identity number as the amount":    {domain.VarAmount, tckn},
		"an identity number as the reference": {domain.VarReferenceNo, tckn},
		"a diagnosis as the property name":    {domain.VarPropertyName, diagnosis},
		"an operator comment as a property":   {domain.VarPropertyName, comment},
	}
	for label, payload := range payloads {
		t.Run(label, func(t *testing.T) {
			vars := bookingVariables(t, event)
			vars[payload.name] = payload.value
			if err := f.tenantTx(t, func(ctx context.Context, tx pgx.Tx) error {
				return application.PublishNotification(ctx, tx, f.tenant, application.Notification{
					EventCode: event, Key: uuid.NewString() + ":CONFIRMED",
					Recipients: []application.Recipient{application.PersonRecipient(member)},
					Variables:  vars,
				})
			}); err == nil {
				// The publisher may accept it; the renderer must not. Either way nothing
				// may reach a recipient, which the assertions below check.
				f.drain(t)
			}
			f.scanStoredMessagesFor(t, payload.value)
			for _, sent := range f.sender.sent() {
				if strings.Contains(sent.body, payload.value) || strings.Contains(sent.subject, payload.value) {
					t.Fatalf("%s was handed to a channel adapter", label)
				}
			}
		})
	}

	// What this test does not claim, and the product does not guarantee, is that a caller
	// which deliberately mislabels a token as a *name* is caught by the value rules. A
	// 32-character hex string is a plausible one-word name to any rule that looks only at
	// the value, and `provider_name` has carried the same limitation since WP-I4-05 with
	// the same reasoning written beside it: what actually keeps a token out of a message
	// is that the catalogue has no slot for one and no shipped template declares a name
	// for a booking's voucher. The two structural guarantees are asserted instead - the
	// link cannot carry a query string, and an undeclared variable is refused outright.

	// A name the catalogue does not have is refused outright rather than dropped, so a
	// caller cannot invent a slot.
	vars := bookingVariables(t, event)
	vars["voucher_token"] = voucherToken
	if err := f.tenantTx(t, func(ctx context.Context, tx pgx.Tx) error {
		return application.PublishNotification(ctx, tx, f.tenant, application.Notification{
			EventCode: event, Key: uuid.NewString() + ":CONFIRMED",
			Recipients: []application.Recipient{application.PersonRecipient(member)},
			Variables:  vars,
		})
	}); err == nil {
		f.drain(t)
	}
	f.scanStoredMessagesFor(t, voucherToken)

	// And no shipped booking template declares a slot a token could be supplied under: the
	// only names any of them use are the reference, the property, the dates, the amount,
	// the status and the link.
	allowed := map[string]bool{
		domain.VarReferenceNo: true, domain.VarPropertyName: true, domain.VarEventDate: true,
		domain.VarExpiresAt: true, domain.VarAmount: true, domain.VarCurrency: true,
		domain.VarStatusCode: true, domain.VarDeepLink: true,
	}
	for _, tpl := range application.SeedTemplates() {
		if application.BookingRecipients[tpl.EventCode] == nil {
			continue
		}
		for _, name := range tpl.Variables {
			if !allowed[name] {
				t.Errorf("%s/%s declares %s, which no booking message should carry",
					tpl.EventCode, tpl.Channel, name)
			}
		}
	}
}

// TestTheReminderSendsOnceAcrossRepeatedRuns is the scheduler guarantee of section 3: the
// reminder job may run any number of times on the day before check-in, and the member is
// told once. The key is the booking and the day, which is what makes a rerun idempotent
// without the job having to remember anything.
func TestTheReminderSendsOnceAcrossRepeatedRuns(t *testing.T) {
	f := newFixture(t)
	event := application.EventBookingReminder
	f.publishSeedTemplates(t, event)

	member := f.seedMemberWithContacts(t)
	bookingID := uuid.New()
	vars := bookingVariables(t, event)
	// The key a scheduler would derive: this booking, this check-in day. It contains no
	// clock, so the fourth run of the job produces the same key as the first.
	key := bookingID.String() + ":" + vars[domain.VarEventDate]

	for range 4 {
		if err := f.tenantTx(t, func(ctx context.Context, tx pgx.Tx) error {
			return application.PublishNotification(ctx, tx, f.tenant, application.Notification{
				EventCode: event, Key: key,
				Recipients: []application.Recipient{application.PersonRecipient(member)},
				Variables:  vars,
			})
		}); err != nil {
			t.Fatalf("reminder run: %v", err)
		}
		f.drain(t)
	}

	got := f.messagesFor(t, event)
	if len(got) != 2 {
		t.Fatalf("%d reminder messages after four runs, want one EMAIL and one INAPP", len(got))
	}
	// Only the reminder was ever published in this test, so every attempt the adapter saw
	// belongs to it: two, one per channel, no matter how many times the job ran.
	if attempts := len(f.sender.sent()); attempts != 2 {
		t.Fatalf("the adapter was asked to send %d times across four runs, want twice (one per channel)", attempts)
	}
}
