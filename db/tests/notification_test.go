package dbtests

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/notification/domain"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// notificationSeed is one tenant with one published template and one sent message, which
// is the smallest thing migration 000029 has anything to say about.
type notificationSeed struct {
	tenant    uuid.UUID
	actor     uuid.UUID
	recipient uuid.UUID
	template  uuid.UUID
	message   uuid.UUID
}

const notificationBody = "Sayın {{given_name}}, {{reference_no}} numaralı başvurunuz onaylandı."

func seedNotification(h *dbtest.Harness, code string) notificationSeed {
	h.T.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()

	s := notificationSeed{tenant: h.CreateTenant(code)}
	s.actor = h.CreateActor("notification-db-"+code, "Notification "+code)
	s.recipient = h.CreateActor("notification-member-"+code, "Uye "+code)

	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		h.T.Helper()
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			h.T.Fatalf("seed %s: %v", what, err)
		}
	}
	scan(&s.template, "notification template", `
		INSERT INTO notification.template (tenant_id, event_code, channel, locale, version_no,
		                                   status, subject, body, declared_variables,
		                                   published_at, published_by)
		VALUES ($1, 'authorization.approved', 'EMAIL', 'tr-TR', 1, 'PUBLISHED',
		        'Başvurunuz onaylandı', $2, ARRAY['given_name','reference_no'],
		        clock_timestamp(), $3)
		RETURNING id`, s.tenant, notificationBody, s.actor)
	scan(&s.message, "notification message", `
		INSERT INTO notification.message (tenant_id, event_code, recipient_type, recipient_id,
		                                  channel, locale, template_id, template_version_no,
		                                  subject_rendered, body_rendered, safe_variables,
		                                  status, dedupe_key)
		VALUES ($1, 'authorization.approved', 'ACTOR', $2, 'EMAIL', 'tr-TR', $3, 1,
		        'Başvurunuz onaylandı',
		        'Sayın Ayşe, AUT-2026-0042 numaralı başvurunuz onaylandı.',
		        '{"given_name":"Ayşe","reference_no":"AUT-2026-0042"}'::jsonb,
		        'QUEUED', 'auth:' || $4)
		RETURNING id`, s.tenant, s.recipient, s.template, code)
	return s
}

// TestNotificationPermissionsAreSeededAndGrantable checks both halves of the same fact. The
// catalogue row and the role template have to agree, because a permission that exists in
// one and not the other is a permission nobody can hold or one nobody can be given — which
// already happened once in this project, with pricing.quote.
func TestNotificationPermissionsAreSeededAndGrantable(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	granted := map[string][]string{}
	for _, tpl := range identityapp.RoleTemplates() {
		for _, code := range tpl.Permissions {
			granted[code] = append(granted[code], tpl.Code)
		}
	}
	for _, code := range []string{"notification.manage", "notification.read"} {
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
	// WP-I4-05 section 2.5 names these three for notification.read.
	for _, want := range []struct{ role, permission string }{
		{"TENANT_ADMIN", "notification.read"},
		{"PROGRAM_MANAGER", "notification.read"},
		{"AUDITOR", "notification.read"},
		{"TENANT_ADMIN", "notification.manage"},
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
	// The other half of "who may hold this": writing the message everybody gets is an
	// administrative act, and reading what one member was actually told is a record of
	// their dealings with the payer. Neither belongs to a provider or to a member.
	for _, code := range []string{"notification.manage", "notification.read"} {
		for _, role := range granted[code] {
			switch role {
			case "PROVIDER_ADMIN", "PROVIDER_STAFF", "PROVIDER_BILLING", "PROVIDER_RESERVATION", "MEMBER":
				t.Fatalf("role %s holds %s; the message log would be readable from outside the tenant", role, code)
			}
		}
	}
}

// TestNotificationTemplateKeepsOnePublishedVersionPerSlot is what makes "which template
// rendered this" a question with one answer.
func TestNotificationTemplateKeepsOnePublishedVersionPerSlot(t *testing.T) {
	h := dbtest.New(t)
	s := seedNotification(h, "NOTIF_SLOT")

	err := h.AdminExecErr(`
		INSERT INTO notification.template (tenant_id, event_code, channel, locale, version_no,
		                                   status, subject, body, declared_variables,
		                                   published_at, published_by)
		VALUES ($1, 'authorization.approved', 'EMAIL', 'tr-TR', 2, 'PUBLISHED',
		        'İkinci', 'İkinci gövde', '{}', clock_timestamp(), $2)`, s.tenant, s.actor)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation,
		"two published templates for one event, channel and locale")

	// A draft alongside a published one is exactly how a new version is written.
	h.AdminExec(`
		INSERT INTO notification.template (tenant_id, event_code, channel, locale, version_no,
		                                   status, subject, body, declared_variables)
		VALUES ($1, 'authorization.approved', 'EMAIL', 'tr-TR', 2, 'DRAFT',
		        'İkinci', 'İkinci gövde', '{}')`, s.tenant)

	// And a version number is used once per slot.
	err = h.AdminExecErr(`
		INSERT INTO notification.template (tenant_id, event_code, channel, locale, version_no,
		                                   status, subject, body, declared_variables)
		VALUES ($1, 'authorization.approved', 'EMAIL', 'tr-TR', 2, 'DRAFT',
		        'Üçüncü', 'Üçüncü gövde', '{}')`, s.tenant)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "two templates with one version number")
}

// TestNotificationTemplateIsImmutableOncePublished: editing the body of a template that has
// already sent messages would rewrite what those messages were rendered from.
func TestNotificationTemplateIsImmutableOncePublished(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()
	s := seedNotification(h, "NOTIF_IMMUT")

	for _, c := range []struct{ what, sql string }{
		{"the body", `UPDATE notification.template SET body = 'başka' WHERE id = $1`},
		{"the subject", `UPDATE notification.template SET subject = 'başka' WHERE id = $1`},
		{"the declared variables", `UPDATE notification.template SET declared_variables = '{}' WHERE id = $1`},
		{"the event it is for", `UPDATE notification.template SET event_code = 'authorization.expired' WHERE id = $1`},
		{"the channel", `UPDATE notification.template SET channel = 'SMS' WHERE id = $1`},
		{"the version number", `UPDATE notification.template SET version_no = 9 WHERE id = $1`},
		{"when it was published", `UPDATE notification.template SET published_at = clock_timestamp() WHERE id = $1`},
	} {
		dbtest.ExpectSQLState(t, h.AdminExecErr(c.sql, s.template),
			dbtest.SQLStateIntegrityConstraint, "changing "+c.what+" of a published template")
	}

	// Retiring it is the one move a published template has.
	h.AdminExec(`UPDATE notification.template SET status = 'RETIRED' WHERE id = $1`, s.template)
	// And a retired template never comes back: the version that replaced it is what
	// renders now, and reviving this one would give the slot two answers.
	dbtest.ExpectSQLState(t,
		h.AdminExecErr(`UPDATE notification.template SET status = 'PUBLISHED' WHERE id = $1`, s.template),
		dbtest.SQLStateIntegrityConstraint, "publishing a retired template again")

	// A draft, on the other hand, is still being written.
	var draft uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO notification.template (tenant_id, event_code, channel, locale, version_no,
		                                   status, subject, body, declared_variables)
		VALUES ($1, 'authorization.expired', 'EMAIL', 'tr-TR', 1, 'DRAFT', 'Konu', 'Gövde', '{}')
		RETURNING id`, s.tenant).Scan(&draft); err != nil {
		t.Fatalf("insert a draft: %v", err)
	}
	h.AdminExec(`UPDATE notification.template SET body = 'yeni gövde' WHERE id = $1`, draft)
}

// TestNotificationTemplateRefusesAnUnsafeDeclaredVariable is the catalogue as a constraint.
// internal/notification/domain says the same thing in Go; deleting one still leaves the
// other, which is the point of writing it twice.
func TestNotificationTemplateRefusesAnUnsafeDeclaredVariable(t *testing.T) {
	h := dbtest.New(t)
	s := seedNotification(h, "NOTIF_VARS")

	for _, declared := range []string{
		`ARRAY['diagnosis']`,
		`ARRAY['tckn']`,
		`ARRAY['comment']`,
		`ARRAY['given_name','diagnosis']`,
		`ARRAY['identifier_value']`,
	} {
		err := h.AdminExecErr(`
			INSERT INTO notification.template (tenant_id, event_code, channel, locale, version_no,
			                                   status, subject, body, declared_variables)
			VALUES ($1, 'authorization.expired', 'EMAIL', 'tr-TR', 1, 'DRAFT', 'Konu', 'Gövde', `+
			declared+`)`, s.tenant)
		dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation,
			"a template declaring "+declared)
	}

	// The catalogue itself is accepted.
	h.AdminExec(`
		INSERT INTO notification.template (tenant_id, event_code, channel, locale, version_no,
		                                   status, subject, body, declared_variables)
		VALUES ($1, 'authorization.expired', 'EMAIL', 'tr-TR', 1, 'DRAFT', 'Konu', 'Gövde',
		        ARRAY['given_name','reference_no','status_code','event_date','expires_at',
		              'amount','currency','provider_name','program_name','deep_link'])`, s.tenant)

	// And a template body cannot carry a bare identity number an operator pasted: every
	// recipient of the event would get it.
	err := h.AdminExecErr(`
		INSERT INTO notification.template (tenant_id, event_code, channel, locale, version_no,
		                                   status, subject, body, declared_variables)
		VALUES ($1, 'authorization.cancelled', 'EMAIL', 'tr-TR', 1, 'DRAFT', 'Konu',
		        'Kayıt numaranız 10000000146', '{}')`, s.tenant)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a template body carrying a TCKN")
}

// TestNotificationMessageRefusesAnIdentityNumber is section 2.4 written as a constraint.
// The Go renderer refuses these values before they get here; the CHECK refuses them even if
// that code is deleted.
func TestNotificationMessageRefusesAnIdentityNumber(t *testing.T) {
	h := dbtest.New(t)
	s := seedNotification(h, "NOTIF_IDNO")

	insert := func(subject, body, variables string) error {
		return h.AdminExecErr(`
			INSERT INTO notification.message (tenant_id, event_code, recipient_type, recipient_id,
			                                  channel, locale, template_id, template_version_no,
			                                  subject_rendered, body_rendered, safe_variables, status)
			VALUES ($1, 'authorization.approved', 'ACTOR', $2, 'EMAIL', 'tr-TR', $3, 1, $4, $5,
			        $6::jsonb, 'QUEUED')`,
			s.tenant, s.recipient, s.template, subject, body, variables)
	}

	safe := `{"given_name":"Ayşe"}`
	dbtest.ExpectSQLState(t,
		insert("Konu", "Sayın Ayşe, TC kimlik numaranız 10000000146.", safe),
		dbtest.SQLStateCheckViolation, "a rendered body carrying a TCKN")
	dbtest.ExpectSQLState(t,
		insert("Vergi numaranız 1234567802", "Gövde", safe),
		dbtest.SQLStateCheckViolation, "a rendered subject carrying a VKN")
	dbtest.ExpectSQLState(t,
		insert("Konu", "Gövde", `{"reference_no":"10000000146"}`),
		dbtest.SQLStateCheckViolation, "safe_variables carrying a TCKN")

	// A deep link carrying a record id is not an identity number, however many digits the
	// id happens to have. This is the case the naive check gets wrong, and it is why the
	// constraint strips uuids first.
	link := "/authorizations/01234567-8901-4234-8901-234567890123"
	if err := insert("Konu", "Ayrıntı: https://kapsora.example"+link,
		`{"deep_link":"`+link+`"}`); err != nil {
		t.Fatalf("a message carrying an all-digit record id was refused: %v", err)
	}
}

// TestNotificationMessageIsAppendOnlyExceptStatusAndSentAt: everything a message says about
// itself is fixed the moment it is written, because it is evidence of what left the system.
func TestNotificationMessageIsAppendOnlyExceptStatusAndSentAt(t *testing.T) {
	h := dbtest.New(t)
	s := seedNotification(h, "NOTIF_APPEND")

	// The two columns a send moves.
	h.AdminExec(`UPDATE notification.message SET status = 'SENDING' WHERE id = $1`, s.message)
	h.AdminExec(`UPDATE notification.message SET status = 'SENT', sent_at = clock_timestamp()
	              WHERE id = $1`, s.message)

	for _, c := range []struct{ what, sql string }{
		{"the rendered body", `UPDATE notification.message SET body_rendered = 'başka' WHERE id = $1`},
		{"the rendered subject", `UPDATE notification.message SET subject_rendered = 'başka' WHERE id = $1`},
		{"the variables", `UPDATE notification.message SET safe_variables = '{}'::jsonb WHERE id = $1`},
		{"the recipient", `UPDATE notification.message SET recipient_id = gen_random_uuid() WHERE id = $1`},
		{"the channel", `UPDATE notification.message SET channel = 'SMS' WHERE id = $1`},
		{"the template snapshot", `UPDATE notification.message SET template_version_no = 9 WHERE id = $1`},
		{"the deduplication key", `UPDATE notification.message SET dedupe_key = 'başka' WHERE id = $1`},
		{"when it was written", `UPDATE notification.message SET created_at = now() WHERE id = $1`},
		{"the message itself", `DELETE FROM notification.message WHERE id = $1`},
	} {
		dbtest.ExpectSQLState(t, h.AdminExecErr(c.sql, s.message),
			dbtest.SQLStateIntegrityConstraint, "changing "+c.what)
	}

	// A SENT message says when, and nothing else does. Half of that pair is a row nobody
	// can answer "when did this go" from.
	dbtest.ExpectSQLState(t,
		h.AdminExecErr(`UPDATE notification.message SET sent_at = NULL WHERE id = $1`, s.message),
		dbtest.SQLStateCheckViolation, "a SENT message with no sent_at")
}

// TestNotificationSuppressionAlwaysNamesAReason is the difference between "not told" and
// "nothing happened".
func TestNotificationSuppressionAlwaysNamesAReason(t *testing.T) {
	h := dbtest.New(t)
	s := seedNotification(h, "NOTIF_SUPPRESS")

	insert := func(status, reason, body string) error {
		return h.AdminExecErr(`
			INSERT INTO notification.message (tenant_id, event_code, recipient_type, recipient_id,
			                                  channel, locale, template_id, template_version_no,
			                                  body_rendered, safe_variables, status, suppressed_reason)
			VALUES ($1, 'authorization.expired', 'ACTOR', $2, 'INAPP', 'tr-TR',
			        CASE WHEN $5::text IS NULL THEN NULL ELSE $3::uuid END,
			        CASE WHEN $5::text IS NULL THEN NULL ELSE 1 END,
			        $5, '{}'::jsonb, $4, NULLIF($6, ''))`,
			s.tenant, s.recipient, s.template, status, nullIfEmpty(body), reason)
	}

	// A suppressed message with no reason is exactly the silence this table exists to
	// prevent.
	dbtest.ExpectSQLState(t, insert("SUPPRESSED", "", ""),
		dbtest.SQLStateCheckViolation, "a suppressed message naming no reason")
	// And a reason on a message that was not suppressed is a contradiction.
	dbtest.ExpectSQLState(t, insert("QUEUED", "QUIET_HOURS", "Gövde"),
		dbtest.SQLStateCheckViolation, "a queued message naming a suppression reason")
	// A queued message with nothing rendered could never be sent.
	dbtest.ExpectSQLState(t, insert("QUEUED", "", ""),
		dbtest.SQLStateCheckViolation, "a queued message with no body")
	// The reason has to be one of the four an operator can act on.
	dbtest.ExpectSQLState(t, insert("SUPPRESSED", "BECAUSE", ""),
		dbtest.SQLStateCheckViolation, "an invented suppression reason")

	if err := insert("SUPPRESSED", "QUIET_HOURS", ""); err != nil {
		t.Fatalf("a suppressed message naming its reason was refused: %v", err)
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// TestNotificationDedupeKeyNotifiesOnce: the same event must not notify twice because a
// retry ran, and the database is the place that is actually true.
func TestNotificationDedupeKeyNotifiesOnce(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()
	s := seedNotification(h, "NOTIF_DEDUPE")

	var key string
	if err := h.Admin.QueryRow(ctx,
		`SELECT dedupe_key FROM notification.message WHERE id = $1`, s.message).Scan(&key); err != nil {
		t.Fatalf("read the deduplication key: %v", err)
	}
	err := h.AdminExecErr(`
		INSERT INTO notification.message (tenant_id, event_code, recipient_type, recipient_id,
		                                  channel, locale, template_id, template_version_no,
		                                  body_rendered, safe_variables, status, dedupe_key)
		VALUES ($1, 'authorization.approved', 'ACTOR', $2, 'EMAIL', 'tr-TR', $3, 1,
		        'İkinci gövde', '{}'::jsonb, 'QUEUED', $4)`,
		s.tenant, s.recipient, s.template, key)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "two messages with one deduplication key")

	// A message with no key — an operator's deliberate resend — is not constrained by it,
	// and two of them are two messages because that is what was asked for.
	for range 2 {
		h.AdminExec(`
			INSERT INTO notification.message (tenant_id, event_code, recipient_type, recipient_id,
			                                  channel, locale, template_id, template_version_no,
			                                  body_rendered, safe_variables, status,
			                                  resent_from_message_id)
			VALUES ($1, 'authorization.approved', 'ACTOR', $2, 'EMAIL', 'tr-TR', $3, 1,
			        'Tekrar gönderim', '{}'::jsonb, 'QUEUED', $4)`,
			s.tenant, s.recipient, s.template, s.message)
	}
}

// TestNotificationDeliveryIsAppendOnly: an attempt that could be rewritten afterwards is
// not evidence of anything.
func TestNotificationDeliveryIsAppendOnly(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()
	s := seedNotification(h, "NOTIF_DELIVERY")

	var attempt uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO notification.delivery (tenant_id, message_id, attempt_no, provider_code, outcome, detail)
		VALUES ($1, $2, 1, 'SMTP', 'ERROR', 'connection refused') RETURNING id`,
		s.tenant, s.message).Scan(&attempt); err != nil {
		t.Fatalf("insert a delivery attempt: %v", err)
	}
	// Attempts accumulate rather than overwrite.
	h.AdminExec(`
		INSERT INTO notification.delivery (tenant_id, message_id, attempt_no, provider_code, outcome)
		VALUES ($1, $2, 2, 'SMTP', 'ACCEPTED')`, s.tenant, s.message)

	dbtest.ExpectSQLState(t,
		h.AdminExecErr(`
			INSERT INTO notification.delivery (tenant_id, message_id, attempt_no, provider_code, outcome)
			VALUES ($1, $2, 2, 'SMTP', 'REJECTED')`, s.tenant, s.message),
		dbtest.SQLStateUniqueViolation, "two attempts numbered the same")

	for _, c := range []struct{ what, sql string }{
		{"an attempt", `UPDATE notification.delivery SET outcome = 'ACCEPTED' WHERE id = $1`},
		{"an attempt away", `DELETE FROM notification.delivery WHERE id = $1`},
	} {
		dbtest.ExpectSQLState(t, h.AdminExecErr(c.sql, attempt),
			dbtest.SQLStateIntegrityConstraint, "rewriting "+c.what)
	}
}

// TestNotificationPreferenceIsOnePerChannelAndEvent: the unique constraint is NULLS NOT
// DISTINCT, so "every event on this channel" is one row rather than one per attempt to
// write it.
func TestNotificationPreferenceIsOnePerChannelAndEvent(t *testing.T) {
	h := dbtest.New(t)
	s := seedNotification(h, "NOTIF_PREF")

	h.AdminExec(`
		INSERT INTO notification.preference (tenant_id, recipient_type, recipient_id, channel, enabled)
		VALUES ($1, 'ACTOR', $2, 'EMAIL', false)`, s.tenant, s.recipient)
	dbtest.ExpectSQLState(t, h.AdminExecErr(`
		INSERT INTO notification.preference (tenant_id, recipient_type, recipient_id, channel, enabled)
		VALUES ($1, 'ACTOR', $2, 'EMAIL', true)`, s.tenant, s.recipient),
		dbtest.SQLStateUniqueViolation, "two channel-wide preferences for one channel")

	// A row naming an event sits beside the channel-wide one; that is how "no e-mail
	// except this one" is expressed.
	h.AdminExec(`
		INSERT INTO notification.preference (tenant_id, recipient_type, recipient_id,
		                                     event_code, channel, enabled)
		VALUES ($1, 'ACTOR', $2, 'authorization.approved', 'EMAIL', true)`, s.tenant, s.recipient)

	// Quiet hours are a pair, and a pair that says nothing is refused.
	dbtest.ExpectSQLState(t, h.AdminExecErr(`
		INSERT INTO notification.preference (tenant_id, recipient_type, recipient_id, channel,
		                                     enabled, quiet_hours_start)
		VALUES ($1, 'ACTOR', $2, 'SMS', true, '22:00')`, s.tenant, s.recipient),
		dbtest.SQLStateCheckViolation, "quiet hours with a start and no end")
	dbtest.ExpectSQLState(t, h.AdminExecErr(`
		INSERT INTO notification.preference (tenant_id, recipient_type, recipient_id, channel,
		                                     enabled, quiet_hours_start, quiet_hours_end)
		VALUES ($1, 'ACTOR', $2, 'SMS', true, '22:00', '22:00')`, s.tenant, s.recipient),
		dbtest.SQLStateCheckViolation, "a quiet hours window of zero length")

	h.AdminExec(`
		INSERT INTO notification.preference (tenant_id, recipient_type, recipient_id, channel,
		                                     enabled, quiet_hours_start, quiet_hours_end, timezone)
		VALUES ($1, 'ACTOR', $2, 'SMS', true, '22:00', '08:00', 'Europe/Istanbul')`,
		s.tenant, s.recipient)
}

// TestNotificationRowsAreTenantIsolated is the RLS check for this schema: the application
// role sees its own tenant's notifications and nothing else, and cannot write a row into
// another tenant either.
func TestNotificationRowsAreTenantIsolated(t *testing.T) {
	h := dbtest.New(t)
	a := seedNotification(h, "NOTIF_RLS_A")
	b := seedNotification(h, "NOTIF_RLS_B")

	if err := h.AppTx(a.tenant, func(ctx context.Context, tx pgx.Tx) error {
		var messages, templates int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM notification.message`).Scan(&messages); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM notification.template`).Scan(&templates); err != nil {
			return err
		}
		if messages != 1 || templates != 1 {
			t.Errorf("tenant A sees %d messages and %d templates, want one of each", messages, templates)
		}
		var visible int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM notification.message WHERE id = $1`, b.message).Scan(&visible); err != nil {
			return err
		}
		if visible != 0 {
			t.Errorf("tenant A can see tenant B's message")
		}
		return nil
	}); err != nil {
		t.Fatalf("read as tenant A: %v", err)
	}

	// And it cannot write into somebody else's tenant either.
	err := h.AppTx(a.tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO notification.preference (tenant_id, recipient_type, recipient_id, channel, enabled)
			VALUES ($1, 'ACTOR', $2, 'EMAIL', false)`, b.tenant, b.recipient)
		return err
	})
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateInsufficientPrivilege,
		"writing a preference into another tenant")
}

// TestSuppressionReasonsAgreeAcrossTheThreePlacesTheyAreWritten catches the drift that
// already happened once: CHANNEL_NOT_DELIVERABLE was added to the column's CHECK and to
// the Go domain but not to the OpenAPI enum, so the server could answer with a value its
// own contract did not declare — invisible to every existing test, because nothing
// compared the lists. The database is the source of truth here; the other two must match
// it exactly, in both directions.
func TestSuppressionReasonsAgreeAcrossTheThreePlacesTheyAreWritten(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	// What the column actually accepts, read out of the CHECK constraint itself.
	var clause string
	if err := h.Admin.QueryRow(ctx, `
		SELECT pg_get_constraintdef(con.oid)
		  FROM pg_constraint con
		  JOIN pg_class rel ON rel.oid = con.conrelid
		  JOIN pg_namespace ns ON ns.oid = rel.relnamespace
		 WHERE ns.nspname = 'notification' AND rel.relname = 'message'
		   AND con.contype = 'c'
		   AND pg_get_constraintdef(con.oid) LIKE '%suppressed_reason%ANY%'`).Scan(&clause); err != nil {
		t.Fatalf("read the suppressed_reason CHECK: %v", err)
	}
	inDatabase := map[string]bool{}
	for _, m := range regexp.MustCompile(`'([A-Z_]+)'`).FindAllStringSubmatch(clause, -1) {
		inDatabase[m[1]] = true
	}
	if len(inDatabase) == 0 {
		t.Fatal("no reasons parsed out of the CHECK; the comparison would prove nothing")
	}

	inDomain := map[string]bool{}
	for _, r := range domain.SuppressionReasons {
		inDomain[r] = true
	}

	spec, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi", "kapsora-v1.yaml"))
	if err != nil {
		t.Fatalf("read the contract: %v", err)
	}
	enum := regexp.MustCompile(`NotificationSuppressionReason:\s*\n\s*type: string\s*\n\s*enum: \[([^\]]+)\]`).
		FindSubmatch(spec)
	if enum == nil {
		t.Fatal("NotificationSuppressionReason enum not found in the contract")
	}
	inContract := map[string]bool{}
	for _, v := range strings.Split(string(enum[1]), ",") {
		inContract[strings.TrimSpace(v)] = true
	}

	for reason := range inDatabase {
		if !inDomain[reason] {
			t.Errorf("%s is accepted by the database but missing from domain.SuppressionReasons", reason)
		}
		if !inContract[reason] {
			t.Errorf("%s is accepted by the database but missing from the OpenAPI enum; "+
				"the server could answer with a value its own contract does not declare", reason)
		}
	}
	for reason := range inContract {
		if !inDatabase[reason] {
			t.Errorf("%s is declared in the OpenAPI enum but the database would refuse it", reason)
		}
	}
	for reason := range inDomain {
		if !inDatabase[reason] {
			t.Errorf("%s is in domain.SuppressionReasons but the database would refuse it", reason)
		}
	}
}
