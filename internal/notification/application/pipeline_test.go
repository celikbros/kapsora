package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/notification/application"
	"github.com/celikbros/kapsora/internal/notification/domain"
	"github.com/celikbros/kapsora/internal/notification/infrastructure/channel"
	notificationpg "github.com/celikbros/kapsora/internal/notification/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/platform/crypto/localkey"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/mail"
	"github.com/celikbros/kapsora/internal/platform/outbox"
)

// The three things a notification may never carry, written out once so every assertion
// below looks for the same strings.
const (
	diagnosis = "C50.9 malign meme neoplazmı, sağ üst dış kadran, evre 2A"
	comment   = "Hasta ile görüştüm, raporun eksik olduğunu söyledi; ek belge istendi."
	tckn      = "10000000146"
)

const (
	approvedEvent = "authorization.approved"
	testLinkBase  = "https://kapsora.example"
)

// fakeSender is the channel adapter the pipeline tests drive. The point of it is that the
// guarantees under test — an attempt is recorded whatever the provider said, a permanent
// rejection stops the retries, a suppressed message is never handed to anybody — have to be
// provable on a machine with no mail server running. Answers are queued; an empty queue
// accepts.
type fakeSender struct {
	mu       sync.Mutex
	answers  []answer
	attempts []attempt
}

type answer struct {
	result application.SenderResult
	err    error
}

// attempt is what the adapter was actually asked to send, so a test can assert what would
// have left the system rather than only what the database says.
type attempt struct {
	address string
	subject string
	body    string
}

func newFakeSender() *fakeSender { return &fakeSender{} }

func (f *fakeSender) queue(a answer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, a)
}

func (f *fakeSender) queueError(err error) { f.queue(answer{err: err}) }

func (f *fakeSender) queueRejection(detail string) {
	f.queue(answer{result: application.SenderResult{
		ProviderCode: "SMTP", Outcome: domain.OutcomeRejected, Detail: detail,
	}})
}

func (f *fakeSender) sent() []attempt {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]attempt(nil), f.attempts...)
}

func (f *fakeSender) Send(_ context.Context, address string, m application.MessageRecord) (
	application.SenderResult, error,
) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, subject := "", ""
	if m.BodyRendered != nil {
		body = *m.BodyRendered
	}
	if m.SubjectRendered != nil {
		subject = *m.SubjectRendered
	}
	f.attempts = append(f.attempts, attempt{address: address, subject: subject, body: body})
	if len(f.answers) > 0 {
		next := f.answers[0]
		f.answers = f.answers[1:]
		return next.result, next.err
	}
	return application.SenderResult{
		ProviderCode: "SMTP", ProviderMessageID: "<" + uuid.NewString() + "@kapsora.local>",
		Outcome: domain.OutcomeAccepted, Detail: "accepted at end of data",
	}, nil
}

// fixture is one throw-away database, one service, one fake channel and one dispatcher.
type fixture struct {
	h          *dbtest.Harness
	pool       *pgxpool.Pool
	svc        *application.Service
	cursors    *httpx.CursorCodec
	keys       *localkey.Provider
	sender     *fakeSender
	dispatcher *outbox.Dispatcher

	tenant     uuid.UUID
	actor      uuid.UUID
	membership uuid.UUID
	// recipient is an actor with an e-mail address and an active membership: the one
	// recipient shape the platform can currently resolve an address for.
	recipient      uuid.UUID
	recipientEmail string
	// unreachable is an actor with no address at all.
	unreachable uuid.UUID

	now time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h := dbtest.New(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.NewPool(ctx, h.AppURL, db.PoolOptions{ApplicationName: "notification-test", MaxConns: 12})
	if err != nil {
		t.Fatalf("app pool: %v", err)
	}
	t.Cleanup(pool.Close)

	cursors, err := httpx.NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	// The repository decrypts a member's contact envelope at send time, so the fixture
	// holds the same key provider the party service writes contacts with.
	keys, err := localkey.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		h: h, pool: pool, sender: newFakeSender(),
		// A Tuesday lunchtime in Istanbul: outside every quiet hours window a test sets,
		// so a suppression is never an accident of when the suite happened to run.
		now: time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC),
	}
	f.cursors = cursors
	f.keys = keys
	f.useSenders(t, map[string]application.ChannelSender{
		domain.ChannelEmail: f.sender,
		domain.ChannelSMS:   f.sender,
		domain.ChannelInApp: f.sender,
	})

	f.tenant = h.CreateTenant("NOTIF")
	f.actor = h.CreateActor("notification-admin-"+uuid.NewString()[:8], "Notification Admin")
	f.membership = h.CreateMembership(f.tenant, f.actor)
	f.recipient = h.CreateActor("member-"+uuid.NewString()[:8], "Uye")
	h.CreateMembership(f.tenant, f.recipient)
	f.recipientEmail = "uye." + uuid.NewString()[:8] + "@example.com"
	h.AdminExec(`UPDATE iam.actor SET email = $2 WHERE id = $1`, f.recipient, f.recipientEmail)
	f.unreachable = h.CreateActor("noaddress-"+uuid.NewString()[:8], "Adressiz")
	h.CreateMembership(f.tenant, f.unreachable)

	return f
}

// useSenders rebuilds the service and its dispatcher around one set of channel adapters.
// Every test but one uses the fake channel; the live Mailpit test swaps in the real SMTP
// adapter and drives exactly the same pipeline.
func (f *fixture) useSenders(t *testing.T, senders map[string]application.ChannelSender) {
	t.Helper()
	svc, err := application.New(application.Deps{
		Pool: f.pool, Repo: notificationpg.New(f.keys), Audit: auditpg.New(), Cursors: f.cursors,
		LinkBase: testLinkBase, Senders: senders,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    func() time.Time { return f.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	f.svc = svc
	f.dispatcher = outbox.New(f.pool, outbox.Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	f.dispatcher.Handle(application.NotifyRequestedEvent, svc.HandleNotifyRequested)
	f.dispatcher.Handle(application.SendRequestedEvent, svc.HandleSendRequested)
}

// rc is a tenant-wide caller holding both notification permissions.
func (f *fixture) rc() identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: f.membership,
		Principal: identity.Principal{ActorID: f.actor},
		Permissions: map[string]struct{}{
			application.PermissionManage: {}, application.PermissionRead: {},
		},
	}
}

func (f *fixture) ctx(t *testing.T) context.Context {
	t.Helper()
	return identity.WithRequestContext(t.Context(), f.rc())
}

// publishTemplate writes and publishes the e-mail template every test renders from.
func (f *fixture) publishTemplate(t *testing.T) application.TemplateRecord {
	t.Helper()
	return f.publishTemplateFor(t, domain.ChannelEmail,
		"{{reference_no}} numaralı başvurunuz",
		"Sayın {{given_name}}, {{reference_no}} numaralı başvurunuz {{status_code}} durumuna "+
			"geçti. Ayrıntı: {{deep_link}}",
		[]string{domain.VarGivenName, domain.VarReferenceNo, domain.VarStatusCode, domain.VarDeepLink})
}

func (f *fixture) publishTemplateFor(t *testing.T, channel, subject, body string,
	declared []string,
) application.TemplateRecord {
	t.Helper()
	ctx := f.ctx(t)
	in := application.NewTemplateInput{
		EventCode: approvedEvent, Channel: channel, Locale: "tr-TR",
		Body: body, DeclaredVariables: declared,
	}
	if channel == domain.ChannelEmail {
		in.Subject = subject
	}
	draft, err := f.svc.CreateTemplate(ctx, f.rc(), in)
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	published, err := f.svc.PublishTemplate(ctx, f.rc(), draft.ID, draft.RowVersion)
	if err != nil {
		t.Fatalf("publish template: %v", err)
	}
	if published.Status != domain.TemplatePublished {
		t.Fatalf("template status = %s after publish", published.Status)
	}
	return published
}

// request is the notification every test asks for, with safe values.
func (f *fixture) request(dedupe string) application.Request {
	return application.Request{
		EventCode: approvedEvent,
		Recipient: application.Recipient{Type: domain.RecipientActor, ID: f.recipient},
		Channel:   domain.ChannelEmail, Locale: "tr-TR",
		Variables: map[string]string{
			domain.VarGivenName:   "Ayşe",
			domain.VarReferenceNo: "AUT-2026-0042",
			domain.VarStatusCode:  "ONAYLANDI",
			domain.VarDeepLink:    "/authorizations/" + uuid.NewString(),
		},
		DedupeKey: dedupe,
	}
}

// publishRequest inserts the outbox event the way a business module would: inside its own
// transaction, which commits.
func (f *fixture) publishRequest(t *testing.T, r application.Request) {
	t.Helper()
	if err := f.tenantTx(t, func(ctx context.Context, tx pgx.Tx) error {
		return application.Publish(ctx, tx, f.tenant, r)
	}); err != nil {
		t.Fatalf("publish the notification request: %v", err)
	}
}

func (f *fixture) tenantTx(t *testing.T, fn func(ctx context.Context, tx pgx.Tx) error) error {
	t.Helper()
	return db.WithTenantTx(t.Context(), f.pool,
		db.TenantContext{TenantID: f.tenant, ActorID: f.actor}, fn)
}

// drain runs the dispatcher until it claims nothing, so a test asserts about the state the
// whole outbox path left rather than about one handler call.
func (f *fixture) drain(t *testing.T) {
	t.Helper()
	for range 5 {
		n, err := f.dispatcher.RunOnce(t.Context())
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		if n == 0 {
			return
		}
	}
}

// messages reads the whole log, newest first.
func (f *fixture) messages(t *testing.T) []application.MessageRecord {
	t.Helper()
	page, err := f.svc.ListMessages(f.ctx(t), f.rc(), application.MessageFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	return page.Items
}

func (f *fixture) only(t *testing.T) application.MessageRecord {
	t.Helper()
	rows := f.messages(t)
	if len(rows) != 1 {
		t.Fatalf("the log holds %d messages, want exactly one: %+v", len(rows), rows)
	}
	return rows[0]
}

func (f *fixture) deliveries(t *testing.T, messageID uuid.UUID) []application.DeliveryRecord {
	t.Helper()
	detail, err := f.svc.GetMessage(f.ctx(t), f.rc(), messageID)
	if err != nil {
		t.Fatalf("get message: %v", err)
	}
	return detail.Deliveries
}

func (f *fixture) countRows(t *testing.T, table string) int {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var n int
	if err := f.h.Admin.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// scanStoredMessagesFor is the column scan section 2.4 asks for: every text and json column
// of notification.message, read as the schema owner so RLS cannot hide anything, searched
// for the things a notification may never carry.
func (f *fixture) scanStoredMessagesFor(t *testing.T, needles ...string) {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	rows, err := f.h.Admin.Query(ctx, `
		SELECT id,
		       coalesce(subject_rendered, '') || ' ' || coalesce(body_rendered, '') || ' ' ||
		       safe_variables::text || ' ' || coalesce(suppressed_reason, '') || ' ' ||
		       coalesce(dedupe_key, '')
		  FROM notification.message`)
	if err != nil {
		t.Fatalf("scan stored messages: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var stored string
		if err := rows.Scan(&id, &stored); err != nil {
			t.Fatalf("scan a stored message: %v", err)
		}
		for _, needle := range needles {
			if strings.Contains(stored, needle) {
				t.Fatalf("notification.message %s carries %q", id, needle)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("scan stored messages: %v", err)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// TestNotificationTravelsFromAnOutboxEventToASentMessage is the whole path in one test: a
// module publishes inside its own transaction, the transaction commits, the worker picks
// the event up, renders it and hands it to a channel.
func TestNotificationTravelsFromAnOutboxEventToASentMessage(t *testing.T) {
	f := newFixture(t)
	template := f.publishTemplate(t)

	request := f.request("auth:" + uuid.NewString())
	f.publishRequest(t, request)
	f.drain(t)

	message := f.only(t)
	if message.Status != domain.MessageSent {
		t.Fatalf("message status = %s, want SENT", message.Status)
	}
	if message.SentAt == nil {
		t.Fatal("a SENT message has no sent_at")
	}
	// The snapshot of what was used. A template retired tomorrow must not change what this
	// message says it was rendered from.
	if message.TemplateID == nil || *message.TemplateID != template.ID {
		t.Fatalf("message template = %v, want %s", message.TemplateID, template.ID)
	}
	if message.TemplateVersionNo == nil || *message.TemplateVersionNo != template.VersionNo {
		t.Fatalf("message template version = %v, want %d", message.TemplateVersionNo, template.VersionNo)
	}
	if message.BodyRendered == nil || !strings.Contains(*message.BodyRendered, "Sayın Ayşe") {
		t.Fatalf("body = %v", message.BodyRendered)
	}

	attempts := f.deliveries(t, message.ID)
	if len(attempts) != 1 {
		t.Fatalf("%d delivery attempts, want one", len(attempts))
	}
	if attempts[0].AttemptNo != 1 || attempts[0].Outcome != domain.OutcomeAccepted {
		t.Fatalf("attempt = %+v", attempts[0])
	}

	sent := f.sender.sent()
	if len(sent) != 1 {
		t.Fatalf("the channel was asked to send %d times, want once", len(sent))
	}
	// The address is resolved at send time and is nowhere in the database.
	if sent[0].address != f.recipientEmail {
		t.Fatalf("sent to %q, want %q", sent[0].address, f.recipientEmail)
	}
	// The link is the configured base plus a path. There is no query string, so there is
	// nowhere a token could have ridden along.
	if !strings.Contains(sent[0].body, testLinkBase+"/authorizations/") {
		t.Fatalf("the deep link was not made absolute: %q", sent[0].body)
	}
	if strings.ContainsAny(sent[0].body, "?#") {
		t.Fatalf("the message body carries a query string: %q", sent[0].body)
	}

	f.scanStoredMessagesFor(t, f.recipientEmail, diagnosis, comment, tckn)
}

// TestSensitiveContentIsRefusedAndWritesNothing is section 3, first bullet. A diagnosis, a
// TCKN and an operator's comment are each offered to the pipeline by both routes that
// exist — the publish a business module makes, and the worker's own render — and neither
// leaves a row behind.
func TestSensitiveContentIsRefusedAndWritesNothing(t *testing.T) {
	f := newFixture(t)
	f.publishTemplate(t)

	unsafe := []struct {
		name string
		vars map[string]string
	}{
		{"a diagnosis", map[string]string{"diagnosis": diagnosis}},
		{"a diagnosis in the given name", map[string]string{domain.VarGivenName: diagnosis}},
		{"a TCKN", map[string]string{"tckn": tckn}},
		{"a TCKN in the reference number", map[string]string{domain.VarReferenceNo: tckn}},
		{"an operator comment", map[string]string{"comment": comment}},
		{"an operator comment in the status word", map[string]string{domain.VarStatusCode: comment}},
	}

	for _, c := range unsafe {
		t.Run(c.name, func(t *testing.T) {
			request := f.request("unsafe:" + uuid.NewString())
			for name, value := range c.vars {
				request.Variables[name] = value
			}

			// Route one: a business module publishing inside its own transaction. It is
			// refused there, where the refusal still means something.
			err := f.tenantTx(t, func(ctx context.Context, tx pgx.Tx) error {
				return application.Publish(ctx, tx, f.tenant, request)
			})
			if !errors.Is(err, domain.ErrValidation) {
				t.Fatalf("Publish accepted %s: %v", c.name, err)
			}

			// Route two: the worker rendering an event that got past the publisher —
			// because it was written by an older build, or by hand.
			_, err = f.svc.Materialize(t.Context(), f.tenant, request)
			if !errors.Is(err, domain.ErrValidation) {
				t.Fatalf("Materialize accepted %s: %v", c.name, err)
			}
		})
	}

	// Nothing at all was written: no outbox event, no message, no delivery attempt, and
	// the channel was never asked to send anything.
	for _, table := range []string{
		"system.outbox_event", "notification.message", "notification.delivery",
	} {
		if n := f.countRows(t, table); n != 0 {
			t.Fatalf("%s holds %d rows after only refused notifications", table, n)
		}
	}
	if got := len(f.sender.sent()); got != 0 {
		t.Fatalf("the channel was asked to send %d times after only refused notifications", got)
	}
	f.scanStoredMessagesFor(t, diagnosis, comment, tckn)
}

// TestSensitiveContentThatGetsPastTheHandlerIsStillRefused drives the worker's handler with
// a hand-built event, which is how an event written by an older build would arrive. The
// handler refuses it permanently — a redelivery would produce the same refusal — and
// writes nothing.
func TestSensitiveContentThatGetsPastTheHandlerIsStillRefused(t *testing.T) {
	f := newFixture(t)
	f.publishTemplate(t)

	payload, err := json.Marshal(map[string]any{
		"eventCode":     approvedEvent,
		"recipientType": domain.RecipientActor,
		"recipientId":   f.recipient,
		"channel":       domain.ChannelEmail,
		"locale":        "tr-TR",
		"variables":     map[string]string{"diagnosis": diagnosis, "tckn": tckn},
		"dedupeKey":     "handcrafted:" + uuid.NewString(),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = f.svc.HandleNotifyRequested(t.Context(), outbox.Delivery{
		ID:       uuid.New(),
		TenantID: uuid.NullUUID{UUID: f.tenant, Valid: true},
		Type:     application.NotifyRequestedEvent,
		Payload:  payload,
		Attempt:  1,
	})
	if err == nil {
		t.Fatal("the handler accepted a payload carrying a diagnosis and a TCKN")
	}
	if outbox.KindOf(err) != outbox.KindPermanent {
		t.Fatalf("the refusal is %s, want PERMANENT: a retry would only refuse it again", outbox.KindOf(err))
	}
	if n := f.countRows(t, "notification.message"); n != 0 {
		t.Fatalf("%d messages were written by a refused render", n)
	}
	f.scanStoredMessagesFor(t, diagnosis, tckn)
}

// TestTheSameEventNotifiesOnce is section 3, third bullet. The outbox delivers at least
// once, so the same event arriving twice has to produce one message.
func TestTheSameEventNotifiesOnce(t *testing.T) {
	f := newFixture(t)
	f.publishTemplate(t)

	request := f.request("auth:one-and-only")

	// The publisher itself may run twice — a retried command, a replayed job — and the
	// outbox refuses the second copy.
	f.publishRequest(t, request)
	f.publishRequest(t, request)
	if n := f.countRows(t, "system.outbox_event"); n != 1 {
		t.Fatalf("%d outbox events for one request, want one", n)
	}

	// And the event itself is delivered more than once, which is what the message table's
	// unique deduplication key is for.
	f.drain(t)
	first := f.only(t)
	for range 3 {
		if _, err := f.svc.Materialize(t.Context(), f.tenant, request); err != nil {
			t.Fatalf("redeliver: %v", err)
		}
	}
	second := f.only(t)
	if first.ID != second.ID {
		t.Fatalf("a redelivery wrote a second message: %s then %s", first.ID, second.ID)
	}
	if got := len(f.sender.sent()); got != 1 {
		t.Fatalf("the channel was asked to send %d times for one event, want once", got)
	}
	if got := len(f.deliveries(t, first.ID)); got != 1 {
		t.Fatalf("%d delivery attempts for one event, want one", got)
	}
}

// TestARolledBackTransactionNotifiesNobody is section 3, fourth bullet, and it is the
// reason notifications go through the outbox at all. There is no path from a business
// command to an e-mail that does not go through a committed row.
func TestARolledBackTransactionNotifiesNobody(t *testing.T) {
	f := newFixture(t)
	f.publishTemplate(t)
	request := f.request("auth:rolled-back")

	// A business transaction that publishes a notification and then fails.
	wanted := errors.New("the business command failed after publishing")
	err := f.tenantTx(t, func(ctx context.Context, tx pgx.Tx) error {
		if err := application.Publish(ctx, tx, f.tenant, request); err != nil {
			return err
		}
		// Proof the publish reached the database before the rollback: the row is visible
		// inside the transaction that wrote it.
		var n int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM system.outbox_event WHERE event_type = $1`,
			application.NotifyRequestedEvent).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("the publish wrote %d rows inside its own transaction, want one", n)
		}
		return wanted
	})
	if !errors.Is(err, wanted) {
		t.Fatalf("the transaction ended with %v, want the business failure", err)
	}

	// Nothing survived it.
	if n := f.countRows(t, "system.outbox_event"); n != 0 {
		t.Fatalf("%d outbox events survived a rolled back transaction", n)
	}
	f.drain(t)
	if n := f.countRows(t, "notification.message"); n != 0 {
		t.Fatalf("%d messages exist after a rolled back transaction", n)
	}
	if got := len(f.sender.sent()); got != 0 {
		t.Fatalf("the channel was asked to send %d times after a rolled back transaction", got)
	}
}

// TestSuppressionIsARowWithAReason is section 3, fifth bullet. Quiet hours, a preference
// that is off, an event with no template and a recipient with no address each write a
// message saying so, and an operator sees all four in the log.
func TestSuppressionIsARowWithAReason(t *testing.T) {
	f := newFixture(t)
	f.publishTemplate(t)
	ctx := f.ctx(t)

	// Somebody who turned e-mail off, and somebody whose night it is. The clock is pinned
	// to 12:00 in Istanbul, so the window below is genuinely the middle of the night.
	quietStart := domain.NewClockTime(22, 0)
	quietEnd := domain.NewClockTime(8, 0)
	if _, err := f.svc.PutPreferences(ctx, f.rc(),
		application.Recipient{Type: domain.RecipientActor, ID: f.recipient},
		[]application.PreferenceInput{
			{EventCode: approvedEvent, Channel: domain.ChannelEmail, Enabled: false},
			{Channel: domain.ChannelSMS, Enabled: true,
				QuietHoursStart: &quietStart, QuietHoursEnd: &quietEnd, Timezone: "Europe/Istanbul"},
		}); err != nil {
		t.Fatalf("put preferences: %v", err)
	}

	// Each of the four is published and then worked off before the next, because the
	// suppression is decided when the worker runs rather than when the event is written:
	// leaving them all to one drain would ask every question at the same instant, and
	// quiet hours are a question about the instant.

	// 1. The preference that is off.
	f.publishRequest(t, f.request("suppress:disabled"))
	f.drain(t)

	// 2. Quiet hours. The SMS template is published so "no template" cannot be the reason,
	// and the clock is moved into the middle of the recipient's night.
	f.publishTemplateFor(t, domain.ChannelSMS, "",
		"{{reference_no}} başvurunuz {{status_code}}.",
		[]string{domain.VarReferenceNo, domain.VarStatusCode})
	quiet := f.request("suppress:quiet")
	quiet.Channel = domain.ChannelSMS
	delete(quiet.Variables, domain.VarGivenName)
	delete(quiet.Variables, domain.VarDeepLink)
	f.publishRequest(t, quiet)
	f.now = time.Date(2026, 9, 8, 23, 30, 0, 0, time.UTC) // 02:30 in Istanbul
	f.drain(t)
	f.now = time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC)

	// 3. An event nobody has written a template for.
	noTemplate := f.request("suppress:no-template")
	noTemplate.EventCode = "authorization.expired"
	f.publishRequest(t, noTemplate)
	f.drain(t)

	// 4. A recipient the platform holds no address for.
	noAddress := f.request("suppress:no-address")
	noAddress.Recipient = application.Recipient{Type: domain.RecipientActor, ID: f.unreachable}
	f.publishRequest(t, noAddress)
	f.drain(t)

	want := map[string]string{
		"suppress:disabled":    domain.SuppressedPreferenceDisabled,
		"suppress:quiet":       domain.SuppressedQuietHours,
		"suppress:no-template": domain.SuppressedNoTemplate,
		"suppress:no-address":  domain.SuppressedNoAddress,
	}
	rows := f.messages(t)
	if len(rows) != len(want) {
		t.Fatalf("the log holds %d messages, want %d", len(rows), len(want))
	}
	for _, message := range rows {
		key := ""
		if message.DedupeKey != nil {
			key = *message.DedupeKey
		}
		reason, expected := want[key]
		if !expected {
			t.Fatalf("an unexpected message %s with key %q", message.ID, key)
		}
		if message.Status != domain.MessageSuppressed {
			t.Fatalf("%s status = %s, want SUPPRESSED", key, message.Status)
		}
		if message.SuppressedReason == nil || *message.SuppressedReason != reason {
			t.Fatalf("%s suppressed reason = %q, want %s", key, deref(message.SuppressedReason), reason)
		}
		// Nothing was handed to a channel, and nothing was rendered: there is no body for
		// a later mistake to send.
		if message.BodyRendered != nil {
			t.Fatalf("%s carries a rendered body although it was suppressed", key)
		}
		if got := len(f.deliveries(t, message.ID)); got != 0 {
			t.Fatalf("%s has %d delivery attempts although it was suppressed", key, got)
		}
		// The variables are on the row, so an operator can see what the member would have
		// been told. They have already been screened, so this is safe.
		if len(message.SafeVariables) == 0 {
			t.Fatalf("%s carries no variables; an operator cannot see what was not sent", key)
		}
	}
	if got := len(f.sender.sent()); got != 0 {
		t.Fatalf("the channel was asked to send %d times for four suppressed messages", got)
	}

	// The operator's own filter: "show me who was not told".
	page, err := f.svc.ListMessages(ctx, f.rc(), application.MessageFilter{
		Status: domain.MessageSuppressed, Limit: 100,
	})
	if err != nil {
		t.Fatalf("list suppressed messages: %v", err)
	}
	if len(page.Items) != len(want) {
		t.Fatalf("the suppressed filter answers %d rows, want %d", len(page.Items), len(want))
	}
}

// TestDeliveryAttemptsAccumulateAndAPermanentRejectionStops is section 3, last bullet. Two
// failures and a refusal produce three rows, and nothing after the refusal produces a
// fourth.
func TestDeliveryAttemptsAccumulateAndAPermanentRejectionStops(t *testing.T) {
	f := newFixture(t)
	f.publishTemplate(t)

	f.sender.queueError(errors.New("dial tcp 127.0.0.1:1025: connection refused"))
	f.sender.queueError(errors.New("dial tcp 127.0.0.1:1025: connection refused"))
	f.sender.queueRejection("550 5.1.1 recipient rejected")

	request := f.request("auth:rejected")
	message, err := f.svc.Materialize(t.Context(), f.tenant, request)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}

	// Two attempts that could not reach the provider. Each is retryable, and each leaves a
	// row: "it failed twice and then went" is a different fact from "it went".
	for i := range 2 {
		err := f.svc.DeliverMessage(t.Context(), f.tenant, message.ID)
		if err == nil {
			t.Fatalf("attempt %d was reported as a success", i+1)
		}
		if outbox.KindOf(err) == outbox.KindPermanent {
			t.Fatalf("attempt %d was classified PERMANENT; the message would never be retried", i+1)
		}
	}

	// The third is the provider's final word.
	err = f.svc.DeliverMessage(t.Context(), f.tenant, message.ID)
	if err == nil {
		t.Fatal("a rejection was reported as a success")
	}
	if outbox.KindOf(err) != outbox.KindPermanent {
		t.Fatalf("a rejection was classified %s, want PERMANENT", outbox.KindOf(err))
	}

	after := f.deliveries(t, message.ID)
	if len(after) != 3 {
		t.Fatalf("%d delivery attempts, want three", len(after))
	}
	for i, attempt := range after {
		if attempt.AttemptNo != i+1 {
			t.Fatalf("attempt %d is numbered %d", i+1, attempt.AttemptNo)
		}
	}
	if after[0].Outcome != domain.OutcomeError || after[2].Outcome != domain.OutcomeRejected {
		t.Fatalf("outcomes = %s, %s, %s", after[0].Outcome, after[1].Outcome, after[2].Outcome)
	}

	// And nothing after it tries again, however many times the event is redelivered.
	for range 3 {
		if err := f.svc.DeliverMessage(t.Context(), f.tenant, message.ID); err != nil {
			t.Fatalf("a delivery after a rejection returned %v, want nothing left to do", err)
		}
	}
	if got := len(f.deliveries(t, message.ID)); got != 3 {
		t.Fatalf("%d delivery attempts after a rejection, want the three that already existed", got)
	}
	if got := len(f.sender.sent()); got != 3 {
		t.Fatalf("the channel was asked to send %d times, want three", got)
	}
	final := f.only(t)
	if final.Status != domain.MessageFailed {
		t.Fatalf("message status = %s after a rejection, want FAILED", final.Status)
	}
	if final.SentAt != nil {
		t.Fatal("a FAILED message carries a sent_at")
	}
}

// TestAPublishedTemplateIsImmutableAndPublishingRetiresTheOneItReplaces is section 3,
// second bullet. This package picks "retires the first": publishing replaces in one
// transaction, so there is never an instant with no published template for the event.
func TestAPublishedTemplateIsImmutableAndPublishingRetiresTheOneItReplaces(t *testing.T) {
	f := newFixture(t)
	ctx := f.ctx(t)
	first := f.publishTemplate(t)

	// A published template cannot be edited, by anybody, including the schema owner.
	err := f.h.AdminExecErr(
		`UPDATE notification.template SET body = 'değişti' WHERE id = $1`, first.ID)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "editing a published template")
	err = f.h.AdminExecErr(
		`UPDATE notification.template SET declared_variables = '{}' WHERE id = $1`, first.ID)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint,
		"changing what a published template declares")

	// A second version for the same event, channel and language.
	second, err2 := f.svc.CreateTemplate(ctx, f.rc(), application.NewTemplateInput{
		EventCode: approvedEvent, Channel: domain.ChannelEmail, Locale: "tr-TR",
		Subject:           "{{reference_no}} başvurunuz",
		Body:              "Sayın {{given_name}}, {{reference_no}} numaralı başvurunuz güncellendi.",
		DeclaredVariables: []string{domain.VarGivenName, domain.VarReferenceNo},
	})
	if err2 != nil {
		t.Fatalf("create the second version: %v", err2)
	}
	if second.VersionNo != first.VersionNo+1 {
		t.Fatalf("the second version is numbered %d, want %d", second.VersionNo, first.VersionNo+1)
	}

	published, err2 := f.svc.PublishTemplate(ctx, f.rc(), second.ID, second.RowVersion)
	if err2 != nil {
		t.Fatalf("publish the second version: %v", err2)
	}
	if published.Status != domain.TemplatePublished {
		t.Fatalf("the second version is %s after publishing", published.Status)
	}

	// The first is retired, not deleted: everything it said is still readable next to the
	// messages it produced.
	retired, err2 := f.svc.GetTemplate(ctx, f.rc(), first.ID)
	if err2 != nil {
		t.Fatalf("read the first version: %v", err2)
	}
	if retired.Status != domain.TemplateRetired {
		t.Fatalf("the first version is %s after a second was published, want RETIRED", retired.Status)
	}
	if retired.Body != first.Body {
		t.Fatal("the retired version's body changed")
	}

	// Exactly one published template per slot, which is what makes "which template
	// rendered this" a question with one answer.
	page, err2 := f.svc.ListTemplates(ctx, f.rc(), application.TemplateFilter{
		EventCode: approvedEvent, Channel: domain.ChannelEmail, Locale: "tr-TR",
		Status: domain.TemplatePublished, Limit: 50,
	})
	if err2 != nil {
		t.Fatalf("list published templates: %v", err2)
	}
	if len(page.Items) != 1 || page.Items[0].ID != second.ID {
		t.Fatalf("published templates = %+v, want only the second version", page.Items)
	}

	// A retired template can never be published again.
	if _, err := f.svc.PublishTemplate(ctx, f.rc(), first.ID, retired.RowVersion); !errors.Is(
		err, application.ErrTemplateNotDraft) {
		t.Fatalf("publishing a retired template answered %v, want ErrTemplateNotDraft", err)
	}

	// And the message rendered now uses the new version.
	f.publishRequest(t, application.Request{
		EventCode: approvedEvent,
		Recipient: application.Recipient{Type: domain.RecipientActor, ID: f.recipient},
		Channel:   domain.ChannelEmail, Locale: "tr-TR",
		Variables: map[string]string{
			domain.VarGivenName: "Ayşe", domain.VarReferenceNo: "AUT-2026-0043",
		},
		DedupeKey: "auth:after-republish",
	})
	f.drain(t)
	message := f.only(t)
	if message.TemplateID == nil || *message.TemplateID != second.ID {
		t.Fatalf("the message was rendered from %v, want the newly published version", message.TemplateID)
	}
	if message.BodyRendered == nil || !strings.Contains(*message.BodyRendered, "güncellendi") {
		t.Fatalf("the message body is %v, want the second version's text", message.BodyRendered)
	}
}

// TestResendWritesANewMessageAndRespectsAnOptOut: a resend is an operator deciding to send
// again, which is a new message rather than a rewrite of the old one — and it is not a way
// around somebody's answer to "do you want to hear about this".
func TestResendWritesANewMessageAndRespectsAnOptOut(t *testing.T) {
	f := newFixture(t)
	f.publishTemplate(t)
	ctx := f.ctx(t)

	f.publishRequest(t, f.request("auth:resend"))
	f.drain(t)
	original := f.only(t)

	copied, err := f.svc.ResendMessage(ctx, f.rc(), original.ID)
	if err != nil {
		t.Fatalf("resend: %v", err)
	}
	if copied.ID == original.ID {
		t.Fatal("the resend re-queued the original instead of writing a copy")
	}
	if copied.ResentFromMessageID == nil || *copied.ResentFromMessageID != original.ID {
		t.Fatalf("the copy does not name what it came from: %v", copied.ResentFromMessageID)
	}
	if copied.DedupeKey != nil {
		t.Fatal("the copy carries a deduplication key; a deliberate resend would do nothing")
	}
	if copied.BodyRendered == nil || original.BodyRendered == nil ||
		*copied.BodyRendered != *original.BodyRendered {
		t.Fatal("the copy does not carry the same text as the original")
	}

	f.drain(t)
	after, err := f.svc.GetMessage(ctx, f.rc(), copied.ID)
	if err != nil {
		t.Fatalf("read the copy: %v", err)
	}
	if after.Message.Status != domain.MessageSent {
		t.Fatalf("the copy is %s after the worker ran, want SENT", after.Message.Status)
	}
	// The original is untouched evidence of what happened the first time.
	unchanged, err := f.svc.GetMessage(ctx, f.rc(), original.ID)
	if err != nil {
		t.Fatalf("read the original: %v", err)
	}
	if unchanged.Message.SentAt == nil || !unchanged.Message.SentAt.Equal(*original.SentAt) {
		t.Fatal("the original's sent_at moved when it was resent")
	}
	if got := len(f.deliveries(t, original.ID)); got != 1 {
		t.Fatalf("the original has %d attempts after a resend, want the one it always had", got)
	}

	// Somebody who has turned this off is not resent to.
	if _, err := f.svc.PutPreferences(ctx, f.rc(),
		application.Recipient{Type: domain.RecipientActor, ID: f.recipient},
		[]application.PreferenceInput{
			{EventCode: approvedEvent, Channel: domain.ChannelEmail, Enabled: false},
		}); err != nil {
		t.Fatalf("put preferences: %v", err)
	}
	if _, err := f.svc.ResendMessage(ctx, f.rc(), original.ID); !errors.Is(
		err, application.ErrRecipientOptedOut) {
		t.Fatalf("resending to somebody who opted out answered %v", err)
	}

	// And a suppressed message has no body to resend at all.
	f.publishRequest(t, f.request("auth:suppressed-resend"))
	f.drain(t)
	var suppressed application.MessageRecord
	for _, m := range f.messages(t) {
		if m.Status == domain.MessageSuppressed {
			suppressed = m
		}
	}
	if suppressed.ID == uuid.Nil {
		t.Fatal("the disabled preference did not produce a suppressed message")
	}
	if _, err := f.svc.ResendMessage(ctx, f.rc(), suppressed.ID); !errors.Is(
		err, application.ErrMessageNotResendable) {
		t.Fatalf("resending a suppressed message answered %v", err)
	}
}

// TestANotificationActuallyArrivesInMailpit is the one test in this package that sends
// through a real SMTP server. Everything above uses a fake channel, because the guarantees
// the pipeline has to prove — a refusal stops the retries, a suppression writes a row — are
// exactly the ones a real server will not produce on demand. This one proves the other
// half: that the whole path, from a business transaction through the outbox and the SMTP
// client to a mail sink, delivers the text the template produced.
//
// It skips when Mailpit is not running (scripts/native/up.*), so a machine without a mail
// sink still runs the rest of the suite.
func TestANotificationActuallyArrivesInMailpit(t *testing.T) {
	smtpAddr := mailpitEnvOr("KAPSORA_SMTP_ADDR", "127.0.0.1:1025")
	ui := mailpitEnvOr("KAPSORA_MAILPIT_UI_URL", "http://127.0.0.1:8025")
	if !mailpitLive(t, ui) {
		t.Skipf("Mailpit is not answering on %s (scripts/native/up.*); skipping the live send", ui)
	}
	smtp, err := mail.NewSMTP(mail.SMTPOptions{
		Address: smtpAddr, From: "kapsora@kapsora.local", Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("new smtp client: %v", err)
	}
	if err := smtp.Ping(t.Context()); err != nil {
		t.Skipf("Mailpit SMTP on %s is not answering: %v", smtpAddr, err)
	}
	email, err := channel.NewEmail(smtp)
	if err != nil {
		t.Fatal(err)
	}

	f := newFixture(t)
	// Swap the fake channel for the real one. Everything else — the template, the outbox,
	// the worker, the message log — is exactly what the tests above exercise.
	f.useSenders(t, map[string]application.ChannelSender{domain.ChannelEmail: email})
	f.publishTemplate(t)

	marker := fmt.Sprintf("KAPSORA-E2E-%d", time.Now().UnixNano())
	request := f.request("auth:live:" + marker)
	request.Variables[domain.VarReferenceNo] = "AUT-" + marker[len(marker)-6:]
	f.publishRequest(t, request)
	f.drain(t)

	message := f.only(t)
	if message.Status != domain.MessageSent {
		t.Fatalf("message status = %s after a live send, want SENT", message.Status)
	}
	attempts := f.deliveries(t, message.ID)
	if len(attempts) != 1 || attempts[0].Outcome != domain.OutcomeAccepted {
		t.Fatalf("delivery attempts = %+v", attempts)
	}
	if attempts[0].ProviderMessageID == nil {
		t.Fatal("the attempt records no provider message id")
	}

	arrived := mailpitFind(t, ui, *attempts[0].ProviderMessageID)
	if arrived.Subject != deref(message.SubjectRendered) {
		t.Fatalf("Mailpit received subject %q, want %q", arrived.Subject, deref(message.SubjectRendered))
	}
	if !strings.Contains(arrived.Text, deref(message.BodyRendered)) {
		t.Fatalf("Mailpit received body %q, want it to contain the rendered text %q",
			arrived.Text, deref(message.BodyRendered))
	}
	// What actually left the system carries nothing it may not.
	for _, needle := range []string{diagnosis, comment, tckn} {
		if strings.Contains(arrived.Text, needle) || strings.Contains(arrived.Subject, needle) {
			t.Fatalf("the delivered mail carries %q", needle)
		}
	}
	if strings.TrimSpace(arrived.HTML) != "" {
		t.Fatalf("the delivered mail carries an HTML part: %q", arrived.HTML)
	}
}

func mailpitEnvOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

func mailpitLive(t *testing.T, base string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/livez", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

type mailpitMessage struct {
	ID        string `json:"ID"`
	MessageID string `json:"MessageID"`
	Subject   string `json:"Subject"`
	Text      string `json:"Text"`
	HTML      string `json:"HTML"`
}

// mailpitFind polls the sink for the message the delivery row names. Mailpit accepts a
// message at the end of DATA and indexes it a moment later, so one read can miss it.
func mailpitFind(t *testing.T, base, providerMessageID string) mailpitMessage {
	t.Helper()
	wanted := strings.Trim(providerMessageID, "<>")
	deadline := time.Now().Add(10 * time.Second)
	for {
		var listing struct {
			Messages []mailpitMessage `json:"messages"`
		}
		mailpitGet(t, base+"/api/v1/messages?limit=50", &listing)
		for _, summary := range listing.Messages {
			if summary.MessageID != wanted {
				continue
			}
			var full mailpitMessage
			mailpitGet(t, base+"/api/v1/message/"+summary.ID, &full)
			return full
		}
		if time.Now().After(deadline) {
			t.Fatalf("the message %s never appeared in Mailpit", wanted)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func mailpitGet(t *testing.T, url string, dst any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request %s: %v", url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get %s: status %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
}

// TestAChannelWithNoProviderBehindItDoesNotClaimToHaveSent is this package's own
// acceptance criterion applied to its stub adapters: a member who was not told can be
// shown to have not been told, and why. The SMS stub and the push recorder hand the
// message to nobody, so a message on those channels must not read SENT — a message log
// saying SENT for something no provider ever carried is precisely the record that
// criterion exists to prevent. The attempt is still written, because the operator has to
// see that it was tried.
//
// In-app is the deliberate exception: it is delivered by being recorded, since the
// message log is what the screen reads.
func TestAChannelWithNoProviderBehindItDoesNotClaimToHaveSent(t *testing.T) {
	f := newFixture(t)
	f.useSenders(t, map[string]application.ChannelSender{
		domain.ChannelSMS:   channel.NewSMS(nil),
		domain.ChannelPush:  channel.NewPush(nil),
		domain.ChannelInApp: channel.NewInApp(nil),
	})

	for _, tc := range []struct {
		channel      string
		wantStatus   string
		wantReason   string
		wantAttempts int
	}{
		// SMS never reaches its stub: the platform holds no telephone number for anybody,
		// so the message is suppressed as NO_ADDRESS before an attempt is made. That is the
		// honest answer today and it changes to CHANNEL_NOT_DELIVERABLE the day a number
		// exists and no provider does.
		{domain.ChannelSMS, domain.MessageSuppressed, domain.SuppressedNoAddress, 0},
		// Push needs no address, so it does reach its recorder — which hands the message to
		// nobody, because there is no device registry.
		{domain.ChannelPush, domain.MessageSuppressed, domain.SuppressedNotDeliverable, 1},
		{domain.ChannelInApp, domain.MessageSent, "", 1},
	} {
		t.Run(tc.channel, func(t *testing.T) {
			f.publishTemplateFor(t, tc.channel, "", "Sayın {{given_name}}, {{reference_no}} sonuçlandı.",
				[]string{domain.VarGivenName, domain.VarReferenceNo})
			f.publishRequest(t, application.Request{
				EventCode: approvedEvent,
				Recipient: application.Recipient{Type: domain.RecipientActor, ID: f.recipient},
				Channel:   tc.channel, Locale: "tr-TR",
				Variables: map[string]string{
					domain.VarGivenName: "Ayşe", domain.VarReferenceNo: "AUT-2026-0091",
				},
				DedupeKey: "undeliverable:" + tc.channel,
			})
			f.drain(t)

			var found *application.MessageRecord
			for _, m := range f.messages(t) {
				if m.Channel == tc.channel {
					found = &m
					break
				}
			}
			if found == nil {
				t.Fatalf("no message was written for %s", tc.channel)
			}
			if found.Status != tc.wantStatus {
				t.Fatalf("%s status = %s, want %s", tc.channel, found.Status, tc.wantStatus)
			}
			if tc.wantReason == "" {
				if found.SuppressedReason != nil {
					t.Fatalf("%s carries reason %q but was delivered", tc.channel, *found.SuppressedReason)
				}
			} else {
				if found.SuppressedReason == nil || *found.SuppressedReason != tc.wantReason {
					t.Fatalf("%s reason = %s, want %s; a suppression nobody can explain is a silent nothing",
						tc.channel, deref(found.SuppressedReason), tc.wantReason)
				}
			}
			// Where an attempt was made it is on record, so the operator sees it was tried.
			if attempts := f.deliveries(t, found.ID); len(attempts) != tc.wantAttempts {
				t.Fatalf("%s delivery attempts = %d, want %d",
					tc.channel, len(attempts), tc.wantAttempts)
			}
		})
	}
}
