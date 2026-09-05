package application_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/notification/application"
	"github.com/celikbros/kapsora/internal/notification/domain"
	"github.com/celikbros/kapsora/internal/notification/infrastructure/channel"
	partyapp "github.com/celikbros/kapsora/internal/party/application"
	partydomain "github.com/celikbros/kapsora/internal/party/domain"
	partypg "github.com/celikbros/kapsora/internal/party/infrastructure/postgres"
)

// The contact details the fixture writes. They are invented and belong to nobody; the
// point of the test is that neither of them ever appears in a table, a log or a message.
const (
	memberEmail = "melis.contact@example.invalid"
	memberPhone = "+905321234567"
)

// seedMemberWithContacts registers a person through the party service and gives them an
// e-mail address and a telephone number, encrypted the way the product encrypts them. It
// goes through the real service rather than an INSERT because the round trip is the point:
// party writes the envelope, notification reads it back.
func (f *fixture) seedMemberWithContacts(t *testing.T) uuid.UUID {
	t.Helper()
	party, err := partyapp.New(partyapp.Deps{
		Pool: f.pool, Repo: partypg.New(), Cipher: f.keys, Index: f.keys,
		Audit: auditpg.New(), Cursors: f.cursors,
	})
	if err != nil {
		t.Fatalf("build the party service: %v", err)
	}
	rc := identity.RequestContext{
		TenantID: f.tenant, MembershipID: f.membership,
		Principal: identity.Principal{ActorID: f.actor},
		Permissions: map[string]struct{}{
			partyapp.PermissionContactRead: {}, partyapp.PermissionContactManage: {},
		},
	}
	var personID uuid.UUID
	ctx, cancel := f.h.Ctx()
	defer cancel()
	if err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Melis', 'Aksu', 'melis aksu') RETURNING id`, f.tenant).Scan(&personID); err != nil {
		t.Fatalf("seed person: %v", err)
	}
	var rowVersion int64
	if err := f.h.Admin.QueryRow(ctx,
		`SELECT row_version FROM party.person WHERE id = $1`, personID).Scan(&rowVersion); err != nil {
		t.Fatalf("read person version: %v", err)
	}
	contacts, err := party.ReplaceContacts(t.Context(), rc, personID, []partydomain.SubmittedContact{
		{Channel: partydomain.ChannelEmail, Value: memberEmail, Primary: true},
		{Channel: partydomain.ChannelSMS, Value: memberPhone, Primary: true},
	}, rowVersion)
	if err != nil {
		t.Fatalf("write contacts: %v", err)
	}
	if len(contacts) != 2 {
		t.Fatalf("contacts = %d, want 2", len(contacts))
	}
	for _, c := range contacts {
		if strings.Contains(c.MaskedValue, "melis.contact") || strings.Contains(c.MaskedValue, "5321234567") {
			t.Fatalf("the mask %q is not a mask", c.MaskedValue)
		}
	}
	return personID
}

// TestAMemberWithATelephoneNumberIsNoLongerAddressless is the change WP-I5-05 makes to a
// fact this package had recorded honestly and could not fix on its own: an SMS used to be
// suppressed as NO_ADDRESS because the schema held no telephone number for anybody. It now
// reaches its adapter, which hands it to nobody because there is no SMS provider, so the
// message is suppressed as CHANNEL_NOT_DELIVERABLE with the attempt on record.
//
// The difference between the two reasons is the whole point. NO_ADDRESS says "we do not
// know where to write to this person"; CHANNEL_NOT_DELIVERABLE says "we know, and we have
// nobody to carry it". They are different problems with different owners.
func TestAMemberWithATelephoneNumberIsNoLongerAddressless(t *testing.T) {
	f := newFixture(t)
	f.useSenders(t, map[string]application.ChannelSender{
		domain.ChannelSMS:   channel.NewSMS(nil),
		domain.ChannelEmail: f.sender,
	})
	personID := f.seedMemberWithContacts(t)

	f.publishTemplateFor(t, domain.ChannelSMS, "", "{{reference_no}} numaralı başvurunuz {{status_code}}.",
		[]string{domain.VarReferenceNo, domain.VarStatusCode})
	f.publishRequest(t, application.Request{
		EventCode: approvedEvent,
		Recipient: application.PersonRecipient(personID),
		Channel:   domain.ChannelSMS, Locale: "tr-TR",
		Variables: map[string]string{
			domain.VarReferenceNo: "AUT-2026-0091", domain.VarStatusCode: "APPROVED",
		},
		DedupeKey: "contact:sms:" + personID.String(),
	})
	f.drain(t)

	message := f.messageOn(t, domain.ChannelSMS)
	if message.Status != domain.MessageSuppressed {
		t.Fatalf("status = %s, want SUPPRESSED", message.Status)
	}
	if message.SuppressedReason == nil || *message.SuppressedReason != domain.SuppressedNotDeliverable {
		t.Fatalf("reason = %s, want CHANNEL_NOT_DELIVERABLE — the person has a number now",
			deref(message.SuppressedReason))
	}
	// The attempt is on record: it was tried, and the adapter said it delivered nothing.
	if attempts := f.deliveries(t, message.ID); len(attempts) != 1 {
		t.Fatalf("delivery attempts = %d, want 1", len(attempts))
	}

	// An unverified contact is still an address. Nothing above verified anything, and the
	// message reached its adapter all the same.
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var verified *string
	if err := f.h.Admin.QueryRow(ctx, `
		SELECT verified_at::text FROM party.person_contact
		 WHERE tenant_id = $1 AND person_id = $2 AND channel = 'SMS'`,
		f.tenant, personID).Scan(&verified); err != nil {
		t.Fatalf("read contact: %v", err)
	}
	if verified != nil {
		t.Fatal("the fixture verified a contact; the point is that an unverified one is used")
	}
}

// TestAMemberWithNoContactRowIsStillNoAddress keeps the other half honest: the reason a
// message is suppressed still distinguishes "nowhere to send it" from "nobody to carry
// it", and a person nobody has recorded a number for is still the first one.
func TestAMemberWithNoContactRowIsStillNoAddress(t *testing.T) {
	f := newFixture(t)
	f.useSenders(t, map[string]application.ChannelSender{domain.ChannelSMS: channel.NewSMS(nil)})

	var personID uuid.UUID
	ctx, cancel := f.h.Ctx()
	defer cancel()
	if err := f.h.Admin.QueryRow(ctx, `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Sessiz', 'Kayit', 'sessiz kayit') RETURNING id`, f.tenant).Scan(&personID); err != nil {
		t.Fatalf("seed person: %v", err)
	}

	f.publishTemplateFor(t, domain.ChannelSMS, "", "{{reference_no}} numaralı başvurunuz {{status_code}}.",
		[]string{domain.VarReferenceNo, domain.VarStatusCode})
	f.publishRequest(t, application.Request{
		EventCode: approvedEvent,
		Recipient: application.PersonRecipient(personID),
		Channel:   domain.ChannelSMS, Locale: "tr-TR",
		Variables: map[string]string{
			domain.VarReferenceNo: "AUT-2026-0092", domain.VarStatusCode: "APPROVED",
		},
		DedupeKey: "contact:none:" + personID.String(),
	})
	f.drain(t)

	message := f.messageOn(t, domain.ChannelSMS)
	if message.SuppressedReason == nil || *message.SuppressedReason != domain.SuppressedNoAddress {
		t.Fatalf("reason = %s, want NO_ADDRESS", deref(message.SuppressedReason))
	}
}

// TestAContactValueNeverLeavesTheEnvelope is WP-I2-01's plaintext scan applied to contact
// details: the address exists in `value_enc` and in the adapter it was handed to, and in
// no other column, no audit row and no log line.
func TestAContactValueNeverLeavesTheEnvelope(t *testing.T) {
	f := newFixture(t)
	personID := f.seedMemberWithContacts(t)

	f.publishTemplateFor(t, domain.ChannelEmail, "{{reference_no}}",
		"{{reference_no}} numaralı başvurunuz {{status_code}}.",
		[]string{domain.VarReferenceNo, domain.VarStatusCode})
	f.publishRequest(t, application.Request{
		EventCode: approvedEvent,
		Recipient: application.PersonRecipient(personID),
		Channel:   domain.ChannelEmail, Locale: "tr-TR",
		Variables: map[string]string{
			domain.VarReferenceNo: "AUT-2026-0093", domain.VarStatusCode: "APPROVED",
		},
		DedupeKey: "contact:email:" + personID.String(),
	})
	f.drain(t)

	// The message did go out, so the address was resolved and used: the scan below is not
	// passing because nothing happened.
	message := f.messageOn(t, domain.ChannelEmail)
	if message.Status != domain.MessageSent {
		t.Fatalf("status = %s, want SENT — the address was not resolved", message.Status)
	}
	sent := f.sender.sent()
	if len(sent) != 1 || sent[0].address != memberEmail {
		t.Fatalf("the adapter was handed %+v, want the member's own address", sent)
	}

	ctx, cancel := f.h.Ctx()
	defer cancel()
	for _, table := range []string{
		"party.person_contact", "party.person", "notification.message",
		"notification.delivery", "notification.template", "system.outbox_event",
		"audit.event", "audit.access_event",
	} {
		var dump string
		if err := f.h.Admin.QueryRow(ctx,
			`SELECT coalesce(string_agg(t::text, ' '), '') FROM `+table+` t`).Scan(&dump); err != nil {
			t.Fatalf("dump %s: %v", table, err)
		}
		for _, secret := range []string{memberEmail, memberPhone, "5321234567"} {
			if strings.Contains(dump, secret) {
				t.Errorf("a contact value appears in plaintext in %s", table)
			}
		}
	}
}

// messageOn is the one message written on a channel; more than one would mean the pipeline
// wrote a second copy of an event that is supposed to notify once.
func (f *fixture) messageOn(t *testing.T, channelCode string) application.MessageRecord {
	t.Helper()
	var found []application.MessageRecord
	for _, m := range f.messages(t) {
		if m.Channel == channelCode {
			found = append(found, m)
		}
	}
	if len(found) != 1 {
		t.Fatalf("messages on %s = %d, want exactly 1", channelCode, len(found))
	}
	return found[0]
}
