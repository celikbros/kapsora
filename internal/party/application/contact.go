package application

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/party/domain"
	"github.com/celikbros/kapsora/internal/platform/crypto"
	"github.com/celikbros/kapsora/internal/platform/db"
)

// Permissions guarding contact details (migration 000035). They are separate from
// member.read for the same reason the identifier grants are: an address is how a person
// is reached and how a person is correlated across systems, and a desk that may look at a
// membership is not thereby entitled to the tenant's address book.
const (
	PermissionContactRead   = "member.contact.read"
	PermissionContactManage = "member.contact.manage"
)

// Contact is one way of reaching a person, as it leaves the service: the mask and never
// the value. There is no method anywhere in this package that returns the plaintext, and
// the only caller that needs it — the notification pipeline — decrypts it in its own
// repository and hands it straight to an adapter.
type Contact struct {
	ID          uuid.UUID
	PersonID    uuid.UUID
	Channel     string
	MaskedValue string
	VerifiedAt  *time.Time
	Primary     bool
	CreatedAt   time.Time
	RowVersion  int64
}

// ListContacts returns the person's contact details, masked.
func (s *Service) ListContacts(ctx context.Context, rc identity.RequestContext,
	personID uuid.UUID,
) ([]Contact, error) {
	var out []Contact
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetPerson(ctx, tx, rc.TenantID, personID); err != nil {
			return err
		}
		rows, err := s.repo.ListContacts(ctx, tx, rc.TenantID, personID)
		if err != nil {
			return err
		}
		out = contactViews(rows)
		// Reading how to reach somebody is a read of their personal data, and it is
		// audited as one. The detail carries the channels and the count, never a mask:
		// a mask in an audit row would be a second, permanent copy of the shape of
		// somebody's address in a table nobody can correct.
		return s.recordContactAccess(ctx, tx, rc, personID, len(rows))
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReplaceContacts rewrites the whole contact set of a person.
//
// A replace rather than a merge: a merge leaves behind the number the caller believed
// they had removed, and a notification to a number somebody asked to have deleted is the
// exact failure this table has to avoid.
//
// The value is encrypted with the person-identifier purpose's sibling, so a telephone
// number and a TCKN of the same digits never share a key, and the plaintext exists in
// this function and in no column, log, audit row or response.
func (s *Service) ReplaceContacts(ctx context.Context, rc identity.RequestContext,
	personID uuid.UUID, submitted []domain.SubmittedContact, expected int64,
) ([]Contact, error) {
	normalized, err := normalizeContacts(submitted)
	if err != nil {
		return nil, err
	}

	var out []Contact
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetPerson(ctx, tx, rc.TenantID, personID); err != nil {
			return err
		}
		rows := make([]NewContact, 0, len(normalized))
		for _, c := range normalized {
			cipher, err := s.cipher.Encrypt(ctx, rc.TenantID, crypto.PurposePersonContact, []byte(c.Value))
			if err != nil {
				return fmt.Errorf("party: encrypt contact: %w", err)
			}
			row := NewContact{
				TenantID: rc.TenantID, PersonID: personID, Channel: c.Channel,
				Cipher: cipher, MaskedValue: domain.MaskContact(c.Channel, c.Value),
				Primary: c.Primary, ActorID: rc.Principal.ActorID,
			}
			if c.Verified {
				at := time.Now().UTC()
				row.VerifiedAt = &at
			}
			rows = append(rows, row)
		}
		if _, err := s.repo.DeleteContacts(ctx, tx, rc.TenantID, personID); err != nil {
			return err
		}
		for _, row := range rows {
			if err := s.repo.AddContact(ctx, tx, row); err != nil {
				return err
			}
		}
		// The person's own row_version is the concurrency token of its child rows, and
		// this is where the If-Match is actually enforced: a stale one matches no row.
		if err := s.repo.TouchPerson(ctx, tx, rc.TenantID, personID, rc.Principal.ActorID, expected); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "person.contacts.replace", personID, map[string]any{
			"contact_count": len(rows), "channels": channelsOf(rows),
		}); err != nil {
			return err
		}
		stored, err := s.repo.ListContacts(ctx, tx, rc.TenantID, personID)
		if err != nil {
			return err
		}
		out = contactViews(stored)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// normalizeContacts puts every value into its canonical form and refuses the ones nobody
// could send to. It runs before the transaction opens, so a bad address never becomes a
// row and never becomes a ciphertext either.
func normalizeContacts(submitted []domain.SubmittedContact) ([]domain.SubmittedContact, error) {
	ve := &domain.ValidationError{}
	if len(submitted) > domain.MaxContacts {
		ve.Add("items", "RANGE", fmt.Sprintf("en fazla %d iletişim bilgisi gönderilebilir", domain.MaxContacts))
		return nil, ve.OrNil()
	}
	out := make([]domain.SubmittedContact, 0, len(submitted))
	primaries := map[string]int{}
	for i, c := range submitted {
		field := fmt.Sprintf("items[%d]", i)
		if !domain.ValidContactChannel(c.Channel) {
			ve.Add(field+".channel", domain.CodeContactChannelUnknown, "geçerli bir kanal olmalı")
			continue
		}
		value := domain.NormalizeContact(c.Channel, c.Value)
		if err := domain.ValidateContact(c.Channel, value); err != nil {
			// The message names the shape, never the value: a 422 echoing an address back
			// would put it in a log the moment anybody screenshots the response.
			ve.Add(field+".value", domain.CodeContactInvalid, "geçerli bir iletişim bilgisi olmalı")
			continue
		}
		if c.Primary {
			if first, dup := primaries[c.Channel]; dup {
				ve.Add(field+".primary", domain.CodeContactPrimaryTwice,
					fmt.Sprintf("bu kanalın birincil kaydı %d. satırda zaten var", first))
				continue
			}
			primaries[c.Channel] = i
		}
		out = append(out, domain.SubmittedContact{
			Channel: c.Channel, Value: value, Primary: c.Primary, Verified: c.Verified,
		})
	}
	return out, ve.OrNil()
}

// recordContactAccess writes the access audit row of a contact read.
func (s *Service) recordContactAccess(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	personID uuid.UUID, count int,
) error {
	return s.audit.RecordAccess(ctx, tx, audit.AccessEvent{
		TenantID: rc.TenantID, ActorID: rc.Principal.ActorID, MembershipID: nullUUID(rc.MembershipID),
		PersonID: nullUUID(personID), ResourceType: "person_contact", ResourceID: nullUUID(personID),
		AccessType: audit.AccessView, Classification: audit.ClassPersonal,
		PurposeCode: "MEMBER_CONTACT", ReasonText: fmt.Sprintf("contact_count=%d", count),
		Outcome: audit.OutcomeSuccess,
	})
}

// contactViews renders the stored rows as the view. The two shapes are deliberately the
// same fields in the same order: the stored one holds no envelope and no plaintext, so
// there is nothing for a projection to have to leave behind.
func contactViews(rows []StoredContact) []Contact {
	out := make([]Contact, 0, len(rows))
	for _, r := range rows {
		out = append(out, Contact(r))
	}
	return out
}

// channelsOf is the audit detail: which channels the person now has, in a stable order,
// with no value and no mask.
func channelsOf(rows []NewContact) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, channel := range []string{domain.ChannelEmail, domain.ChannelSMS} {
		for _, r := range rows {
			if r.Channel == channel && !seen[channel] {
				seen[channel] = true
				out = append(out, channel)
			}
		}
	}
	return out
}
