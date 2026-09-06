package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/party/application"
	"github.com/celikbros/kapsora/internal/party/domain"
)

// memberRC is the caller getMyPerson actually has: a member account, bound to one person,
// holding none of the member.* grants that read other people's records.
func memberRC(tenant, person uuid.UUID) identity.RequestContext {
	return identity.RequestContext{
		TenantID: tenant, MembershipID: uuid.New(),
		PersonID:    uuid.NullUUID{UUID: person, Valid: true},
		Permissions: map[string]struct{}{"eligibility.check": {}},
		Scopes:      []identity.Scope{{Type: identity.ScopePerson, ID: uuid.NullUUID{UUID: person, Valid: true}}},
	}
}

// stubEnrollments answers with whatever it was given, and records who was asked about. The
// point of the recording is the assertion that matters: the person the reader is asked for
// is the person from the scope and never one from a request.
type stubEnrollments struct {
	asked  []uuid.UUID
	answer []benefitapp.Enrollment
	err    error
}

func (s *stubEnrollments) ListPersonEnrollments(_ context.Context, _ identity.RequestContext,
	personID uuid.UUID,
) ([]benefitapp.Enrollment, error) {
	s.asked = append(s.asked, personID)
	return s.answer, s.err
}

// TestMyPersonAnswersTheBoundPersonAndMasksEverything is the acceptance path: a member
// reads their own record, and what comes back is masked exactly as it would be for a
// back-office reader. Being the subject of a record is not a reason to hand the plaintext
// back over the wire.
func TestMyPersonAnswersTheBoundPersonAndMasksEverything(t *testing.T) {
	f := newFixture(t)

	tckn := f.tckn()
	person := f.create(t, f.tenantA, "Melis", "Üye",
		domain.SubmittedIdentifier{Type: "TCKN", Value: tckn, Primary: true})

	const email = "melis.myperson@example.invalid"
	contacts, err := f.svc.ReplaceContacts(context.Background(), f.rc(f.tenantA), person.ID,
		[]domain.SubmittedContact{{Channel: domain.ChannelEmail, Value: email, Primary: true}},
		person.RowVersion)
	if err != nil {
		t.Fatalf("write contacts: %v", err)
	}
	if len(contacts) != 1 {
		t.Fatalf("wrote %d contacts, want one", len(contacts))
	}

	enrollments := &stubEnrollments{answer: []benefitapp.Enrollment{{
		ID: uuid.New(), PersonID: person.ID, PlanCode: "STANDART", Status: "ACTIVE",
	}}}

	got, err := f.svc.LoadMyPerson(context.Background(), memberRC(f.tenantA, person.ID), enrollments)
	if err != nil {
		t.Fatalf("load my person: %v", err)
	}
	if got.Person.ID != person.ID {
		t.Fatalf("answered with person %s, want the bound %s", got.Person.ID, person.ID)
	}
	if len(enrollments.asked) != 1 || enrollments.asked[0] != person.ID {
		t.Fatalf("the enrollment reader was asked about %v, want only %s", enrollments.asked, person.ID)
	}
	if len(got.Enrollments) != 1 || got.Enrollments[0].PlanCode != "STANDART" {
		t.Fatalf("enrollments = %+v", got.Enrollments)
	}

	// Nothing in the answer is a plaintext identifier or a plaintext address. This is the
	// assertion that would go red if the view were ever built from the raw record.
	if len(got.Contacts) != 1 {
		t.Fatalf("contacts = %+v, want one", got.Contacts)
	}
	if strings.Contains(got.Contacts[0].MaskedValue, "melis.myperson") {
		t.Fatalf("the contact came back unmasked: %q", got.Contacts[0].MaskedValue)
	}
	if got.Person.MaskedPrimaryIdentifier == "" || strings.Contains(got.Person.MaskedPrimaryIdentifier, tckn) {
		t.Fatalf("the identifier came back as %q", got.Person.MaskedPrimaryIdentifier)
	}
	// And the read is recorded: reading one's own personal data is still a read of
	// personal data.
	if n := f.countAccessEvents(t, person.ID); n == 0 {
		t.Fatal("reading one's own record left no access event")
	}
}

// TestMyPersonRefusesAnUnboundAccount is the refusal a member sees before onboarding has
// finished. It has to be its own error rather than a permission denial, because the fix is
// somebody finishing the binding and not somebody granting a role.
func TestMyPersonRefusesAnUnboundAccount(t *testing.T) {
	f := newFixture(t)
	person := f.create(t, f.tenantA, "Kerem", "Üye")

	// A back-office caller: every member.* permission and no PERSON scope at all.
	if _, err := f.svc.LoadMyPerson(context.Background(), f.rc(f.tenantA), nil); !errors.Is(err, identity.ErrPersonBindingMissing) {
		t.Fatalf("an unbound caller: err = %v, want ErrPersonBindingMissing", err)
	}

	// A scope that names nothing is not a binding either.
	rc := memberRC(f.tenantA, person.ID)
	rc.PersonID = uuid.NullUUID{}
	if _, err := f.svc.LoadMyPerson(context.Background(), rc, nil); !errors.Is(err, identity.ErrPersonBindingMissing) {
		t.Fatalf("an empty binding: err = %v, want ErrPersonBindingMissing", err)
	}
	rc.PersonID = uuid.NullUUID{UUID: uuid.Nil, Valid: true}
	if _, err := f.svc.LoadMyPerson(context.Background(), rc, nil); !errors.Is(err, identity.ErrPersonBindingMissing) {
		t.Fatalf("a nil person id: err = %v, want ErrPersonBindingMissing", err)
	}
}

// TestMyPersonCannotReachAnotherTenantsPerson is the isolation that makes the binding safe
// to trust: a request context carrying a person of another tenant reads nothing, because
// the read runs inside this tenant's transaction and RLS answers for it.
func TestMyPersonCannotReachAnotherTenantsPerson(t *testing.T) {
	f := newFixture(t)
	foreign := f.create(t, f.tenantB, "Yabancı", "Kişi")

	enrollments := &stubEnrollments{}
	_, err := f.svc.LoadMyPerson(context.Background(), memberRC(f.tenantA, foreign.ID), enrollments)
	if err == nil {
		t.Fatal("a member read a person of another tenant")
	}
	if !errors.Is(err, application.ErrPersonNotFound) {
		t.Fatalf("err = %v, want ErrPersonNotFound", err)
	}
	// And nothing downstream was asked about them either: a refused read must not become a
	// question somebody else answers.
	if len(enrollments.asked) != 0 {
		t.Fatalf("the enrollment reader was asked about %v after a refused read", enrollments.asked)
	}
}

// TestMyPersonWorksWithoutAnEnrollmentReader keeps the optional dependency honest: a
// deployment that wires no benefit service still serves a member their own name and
// contacts rather than failing the whole read, and the answer is an empty list rather than
// a null the client has to guard.
func TestMyPersonWorksWithoutAnEnrollmentReader(t *testing.T) {
	f := newFixture(t)
	person := f.create(t, f.tenantA, "Deniz", "Üye")

	got, err := f.svc.LoadMyPerson(context.Background(), memberRC(f.tenantA, person.ID), nil)
	if err != nil {
		t.Fatalf("load with no enrollment reader: %v", err)
	}
	if got.Enrollments == nil {
		t.Fatal("enrollments came back nil, want an empty list")
	}
	if len(got.Enrollments) != 0 {
		t.Fatalf("enrollments = %+v, want none", got.Enrollments)
	}
	if got.Person.ID != person.ID {
		t.Fatalf("person = %s, want %s", got.Person.ID, person.ID)
	}
}

// countAccessEvents counts the audit rows recorded against one person.
func (f *fixture) countAccessEvents(t *testing.T, personID uuid.UUID) int {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var n int
	if err := f.h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM audit.access_event WHERE person_id = $1`, personID).Scan(&n); err != nil {
		t.Fatalf("count access events: %v", err)
	}
	return n
}
