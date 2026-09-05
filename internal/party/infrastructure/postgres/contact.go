package partypg

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/party/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// ListContacts implements application.Repository. It selects the mask and never the
// envelope: the only query in the schema that reads `value_enc` is the notification
// pipeline's address lookup, which has to send to the address rather than show it.
func (Repository) ListContacts(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID) ([]application.StoredContact, error) {
	rows, err := sqlcgen.New(tx).ListPersonContacts(ctx, sqlcgen.ListPersonContactsParams{
		TenantID: tenantID, PersonID: personID,
	})
	if err != nil {
		return nil, fmt.Errorf("party: list contacts: %w", err)
	}
	out := make([]application.StoredContact, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.StoredContact{
			ID: r.ID, PersonID: r.PersonID, Channel: r.Channel, MaskedValue: r.ValueMasked,
			VerifiedAt: r.VerifiedAt, Primary: r.IsPrimary,
			CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
		})
	}
	return out, nil
}

// DeleteContacts implements application.Repository.
func (Repository) DeleteContacts(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID) (int64, error) {
	n, err := sqlcgen.New(tx).DeletePersonContacts(ctx, sqlcgen.DeletePersonContactsParams{
		TenantID: tenantID, PersonID: personID,
	})
	if err != nil {
		return 0, fmt.Errorf("party: delete contacts: %w", err)
	}
	return n, nil
}

// AddContact implements application.Repository.
func (Repository) AddContact(ctx context.Context, tx pgx.Tx, in application.NewContact) error {
	_, err := sqlcgen.New(tx).CreatePersonContact(ctx, sqlcgen.CreatePersonContactParams{
		TenantID: in.TenantID, PersonID: in.PersonID, Channel: in.Channel,
		ValueEnc: in.Cipher, ValueMasked: in.MaskedValue, VerifiedAt: in.VerifiedAt,
		IsPrimary: in.Primary, ActorID: nullUUID(in.ActorID),
	})
	if err != nil {
		return fmt.Errorf("party: add contact: %w", err)
	}
	return nil
}

// TouchPerson implements application.Repository.
func (Repository) TouchPerson(ctx context.Context, tx pgx.Tx, tenantID, personID, actorID uuid.UUID, expected int64) error {
	_, err := sqlcgen.New(tx).TouchPersonForContacts(ctx, sqlcgen.TouchPersonForContactsParams{
		TenantID: tenantID, ID: personID, RowVersion: expected, ActorID: nullUUID(actorID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrVersionMismatch
	}
	if err != nil {
		return fmt.Errorf("party: touch person: %w", err)
	}
	return nil
}
