package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/party/application"
	"github.com/celikbros/kapsora/internal/party/domain"
)

// The contact details these tests write. They are invented and belong to nobody; what
// matters about them is that neither ever appears anywhere but the envelope column.
const (
	contactEmail = "gizli.kisi@example.invalid"
	contactPhone = "+905321234567"
)

// contactRC is a caller holding the two contact grants, which are separate from
// member.read for the same reason the identifier grants are.
func (f *fixture) contactRC(tenant uuid.UUID) identity.RequestContext {
	rc := f.rc(tenant)
	rc.Permissions[application.PermissionContactRead] = struct{}{}
	rc.Permissions[application.PermissionContactManage] = struct{}{}
	return rc
}

// TestContactsAreStoredEncryptedAndReadBackMasked is the whole shape of section 2.5: the
// value goes in once, comes back only as a mask, and the column holds an envelope.
func TestContactsAreStoredEncryptedAndReadBackMasked(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.contactRC(f.tenantA)
	person := f.create(t, f.tenantA, "Gizli", "Kisi")

	contacts, err := f.svc.ReplaceContacts(ctx, rc, person.ID, []domain.SubmittedContact{
		{Channel: domain.ChannelEmail, Value: "  Gizli.Kisi@Example.INVALID ", Primary: true},
		{Channel: domain.ChannelSMS, Value: "+90 532 123 45 67", Primary: true},
	}, person.RowVersion)
	if err != nil {
		t.Fatalf("replace contacts: %v", err)
	}
	if len(contacts) != 2 {
		t.Fatalf("contacts = %d, want 2", len(contacts))
	}

	byChannel := map[string]application.Contact{}
	for _, c := range contacts {
		byChannel[c.Channel] = c
	}
	// The mask keeps enough to recognise a value somebody already knows and never enough
	// to learn one they do not.
	if got := byChannel[domain.ChannelEmail].MaskedValue; got != "g*********@example.invalid" {
		t.Fatalf("e-mail mask = %q", got)
	}
	if got := byChannel[domain.ChannelSMS].MaskedValue; got != "+**********67" {
		t.Fatalf("telephone mask = %q", got)
	}
	if byChannel[domain.ChannelSMS].VerifiedAt != nil {
		t.Fatal("a contact nobody proved came back verified")
	}

	// The read is a read of somebody's personal data and is audited as one.
	read, err := f.svc.ListContacts(ctx, rc, person.ID)
	if err != nil {
		t.Fatalf("list contacts: %v", err)
	}
	if len(read) != 2 {
		t.Fatalf("read back %d contacts, want 2", len(read))
	}
	if !strings.Contains(f.dump(t, "audit.access_event"), "person_contact") {
		t.Fatal("reading a person's contact details left no access audit row")
	}

	// The column holds an envelope, which is to say: not the address.
	ctxDB, cancel := f.h.Ctx()
	defer cancel()
	var enc []byte
	if err := f.h.Admin.QueryRow(ctxDB, `
		SELECT value_enc FROM party.person_contact
		 WHERE tenant_id = $1 AND person_id = $2 AND channel = 'EMAIL'`,
		f.tenantA, person.ID).Scan(&enc); err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	if len(enc) == 0 || strings.Contains(string(enc), "example.invalid") {
		t.Fatal("value_enc is not an envelope")
	}
}

// TestReplaceContactsIsAReplaceAndHonoursTheETag: a merge would leave behind the number
// somebody asked to have removed, and a notification to it is exactly what this table has
// to avoid.
func TestReplaceContactsIsAReplaceAndHonoursTheETag(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.contactRC(f.tenantA)
	person := f.create(t, f.tenantA, "Silinen", "Numara")

	if _, err := f.svc.ReplaceContacts(ctx, rc, person.ID, []domain.SubmittedContact{
		{Channel: domain.ChannelSMS, Value: contactPhone, Primary: true},
		{Channel: domain.ChannelEmail, Value: contactEmail},
	}, person.RowVersion); err != nil {
		t.Fatalf("first write: %v", err)
	}

	// A stale If-Match writes nothing at all, and the set is left where it was.
	if _, err := f.svc.ReplaceContacts(ctx, rc, person.ID, nil, person.RowVersion); !errors.Is(err, application.ErrVersionMismatch) {
		t.Fatalf("a stale If-Match = %v, want ErrVersionMismatch", err)
	}
	if got, err := f.svc.ListContacts(ctx, rc, person.ID); err != nil || len(got) != 2 {
		t.Fatalf("after the refused write: %d contacts, err %v", len(got), err)
	}

	current, err := f.svc.Get(ctx, rc, person.ID)
	if err != nil {
		t.Fatalf("read person: %v", err)
	}
	if _, err := f.svc.ReplaceContacts(ctx, rc, person.ID, []domain.SubmittedContact{
		{Channel: domain.ChannelEmail, Value: contactEmail, Primary: true},
	}, current.RowVersion); err != nil {
		t.Fatalf("second write: %v", err)
	}
	after, err := f.svc.ListContacts(ctx, rc, person.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(after) != 1 || after[0].Channel != domain.ChannelEmail {
		t.Fatalf("the replaced set = %+v; the removed number survived", after)
	}
}

// TestContactsRefuseWhatNobodyCouldSendTo: an address nobody could ever write to is a 422
// rather than a row that quietly fails at send time, and the refusal never echoes the
// value back.
func TestContactsRefuseWhatNobodyCouldSendTo(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.contactRC(f.tenantA)
	person := f.create(t, f.tenantA, "Hatali", "Kayit")

	for name, items := range map[string][]domain.SubmittedContact{
		"an e-mail address with no host": {{Channel: domain.ChannelEmail, Value: "kimse"}},
		"an e-mail address with no dot in the host": {
			{Channel: domain.ChannelEmail, Value: "kimse@localhost"},
		},
		"a telephone number too short":  {{Channel: domain.ChannelSMS, Value: "+9053"}},
		"a channel that does not exist": {{Channel: "FAX", Value: "+905321234567"}},
		"two primaries on one channel": {
			{Channel: domain.ChannelSMS, Value: contactPhone, Primary: true},
			{Channel: domain.ChannelSMS, Value: "+905329876543", Primary: true},
		},
	} {
		err := func() error {
			_, err := f.svc.ReplaceContacts(ctx, rc, person.ID, items, person.RowVersion)
			return err
		}()
		if err == nil {
			t.Fatalf("%s was accepted", name)
		}
		var ve *domain.ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("%s was refused with %v, want a validation error", name, err)
		}
		body, marshalErr := json.Marshal(ve.Fields)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		for _, value := range []string{contactPhone, "905329876543", "kimse@localhost"} {
			if strings.Contains(string(body), value) {
				t.Fatalf("%s: the refusal echoed the value back: %s", name, body)
			}
		}
	}
	if got, err := f.svc.ListContacts(ctx, rc, person.ID); err != nil || len(got) != 0 {
		t.Fatalf("a refused write left %d contacts behind (err %v)", len(got), err)
	}
}

// TestContactPlaintextNeverReachesStorageAuditOrLogs is WP-I2-01's scan, extended to
// contact details. It is the same assertion, over the same tables plus the new one, and
// against the same three places a value could escape to: a column, an audit row, a log.
func TestContactPlaintextNeverReachesStorageAuditOrLogs(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	rc := f.contactRC(f.tenantA)
	person := f.create(t, f.tenantA, "Gizli", "Iletisim")

	if _, err := f.svc.ReplaceContacts(ctx, rc, person.ID, []domain.SubmittedContact{
		{Channel: domain.ChannelEmail, Value: contactEmail, Primary: true},
		{Channel: domain.ChannelSMS, Value: contactPhone, Primary: true},
	}, person.RowVersion); err != nil {
		t.Fatalf("write contacts: %v", err)
	}
	if _, err := f.svc.ListContacts(ctx, rc, person.ID); err != nil {
		t.Fatalf("read contacts: %v", err)
	}

	haystacks := map[string]string{}
	for _, table := range partyTables {
		haystacks[table] = f.dump(t, table)
	}
	haystacks["audit.event"] = f.dump(t, "audit.event")
	haystacks["audit.access_event"] = f.dump(t, "audit.access_event")
	haystacks["logs"] = f.logs.String()
	// The local part and the bare digits separately, because a mask that kept either of
	// them would pass a search for the whole value.
	for _, secret := range []string{contactEmail, contactPhone, "gizli.kisi", "5321234567"} {
		for where, hay := range haystacks {
			if strings.Contains(hay, secret) {
				t.Errorf("the contact value %q appears in plaintext in %s", secret, where)
			}
		}
	}
	// And the audit trail still says what happened, which is the other half of the rule:
	// the value is secret, the fact that somebody wrote one is not.
	if !strings.Contains(haystacks["audit.event"], "person.contacts.replace") {
		t.Fatal("writing contact details was not audited")
	}
	if !strings.Contains(haystacks["party.person_contact"], "EMAIL") {
		t.Fatal("the channel should be visible; only the value is secret")
	}
}
