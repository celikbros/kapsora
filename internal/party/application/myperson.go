package application

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
)

// EnrollmentReader is the benefit module's answer to "what is this person enrolled in".
//
// It is an interface here rather than a direct call so this package keeps depending on
// nothing it cannot substitute in a test, and so a deployment that wires no benefit
// service still serves a member their own name and contacts. The benefit service satisfies
// it as it stands; cmd/api passes it in.
type EnrollmentReader interface {
	ListPersonEnrollments(ctx context.Context, rc identity.RequestContext, personID uuid.UUID) ([]benefitapp.Enrollment, error)
}

// MyPerson is the record a member reads about themselves: who they are, what they are
// enrolled in, and how the product may reach them.
//
// The identifier and the contacts are masked, exactly as they are for a back-office
// reader. Being the subject of a record is not a reason to hand the plaintext back over
// the wire: it would be one more copy of a TCKN and a telephone number in a place nobody
// can recall it from, and the member already knows their own number.
type MyPerson struct {
	Person      Person
	Enrollments []benefitapp.Enrollment
	Contacts    []Contact
}

// LoadMyPerson answers getMyPerson: the person the caller's PERSON grant names.
//
// The person id is never taken from the request. There is no parameter for it, which is
// the point: a member cannot read their neighbour's record by editing a path, because
// there is no path to edit. A caller with no PERSON scope gets
// identity.ErrPersonBindingMissing, which the transport turns into a 403 that says the
// account has not been bound rather than one that says permission was denied — the fix is
// somebody finishing the onboarding, not somebody granting a role.
func (s *Service) LoadMyPerson(ctx context.Context, rc identity.RequestContext, enrollments EnrollmentReader) (MyPerson, error) {
	personID, bound := rc.PersonScope()
	if !bound {
		return MyPerson{}, identity.ErrPersonBindingMissing
	}

	var out MyPerson
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		person, err := s.load(ctx, tx, rc.TenantID, personID)
		if err != nil {
			return err
		}
		out.Person = person
		rows, err := s.repo.ListContacts(ctx, tx, rc.TenantID, personID)
		if err != nil {
			return err
		}
		out.Contacts = contactViews(rows)
		// Reading one's own record is still a read of personal data, and it is recorded as
		// one. The detail carries the count and no mask, exactly as ListContacts does: a
		// mask in an audit row would be a second permanent copy of the shape of somebody's
		// address in a table nobody can correct.
		return s.recordContactAccess(ctx, tx, rc, personID, len(rows))
	})
	if err != nil {
		return MyPerson{}, err
	}

	if enrollments != nil {
		list, err := enrollments.ListPersonEnrollments(ctx, rc, personID)
		if err != nil {
			return MyPerson{}, err
		}
		out.Enrollments = list
	}
	if out.Enrollments == nil {
		out.Enrollments = []benefitapp.Enrollment{}
	}
	return out, nil
}
