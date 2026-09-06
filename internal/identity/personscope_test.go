package identity

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// The member binding's whole value is what it refuses, so these tests are mostly refusals.
// Each of them would pass silently if RequirePerson simply returned the person the body
// asked for, which is exactly the bug the helper exists to make impossible.

func memberContext(person uuid.UUID) context.Context {
	rc := RequestContext{
		TenantID:    uuid.New(),
		PersonID:    uuid.NullUUID{UUID: person, Valid: true},
		Permissions: map[string]struct{}{"accommodation.booking.create": {}},
	}
	return WithRequestContext(context.Background(), rc)
}

func TestRequirePersonAnswersTheBoundPerson(t *testing.T) {
	me := uuid.New()
	ctx := memberContext(me)

	// A body that names nobody: the server supplies the person.
	rc, got, err := RequirePerson(ctx, uuid.Nil)
	if err != nil {
		t.Fatalf("an empty body was refused: %v", err)
	}
	if got != me {
		t.Fatalf("resolved person = %s, want %s", got, me)
	}
	if p, ok := rc.PersonScope(); !ok || p != me {
		t.Fatalf("PersonScope() = %s,%t", p, ok)
	}

	// A body that names the caller's own person: accepted, because a client echoing back
	// what it read from getMyPerson is doing nothing wrong.
	if _, got, err = RequirePerson(ctx, me); err != nil || got != me {
		t.Fatalf("own person refused: %s, %v", got, err)
	}
}

func TestRequirePersonRefusesAnotherPerson(t *testing.T) {
	me := uuid.New()
	neighbour := uuid.New()

	_, got, err := RequirePerson(memberContext(me), neighbour)
	if !errors.Is(err, ErrPersonScope) {
		t.Fatalf("a body naming another person: err = %v, want ErrPersonScope", err)
	}
	if got != uuid.Nil {
		t.Fatalf("a refused call still resolved %s", got)
	}
}

func TestRequirePersonRefusesAnUnboundAccount(t *testing.T) {
	// A reviewer: permissions, no PERSON scope. This is the case a member sees before
	// onboarding has finished, and it must not be reported as a permission problem.
	rc := RequestContext{
		TenantID:    uuid.New(),
		Permissions: map[string]struct{}{"service_request.read": {}},
		Scopes:      []Scope{{Type: "ORGANIZATION", ID: uuid.NullUUID{UUID: uuid.New(), Valid: true}}},
	}
	ctx := WithRequestContext(context.Background(), rc)

	if _, _, err := RequirePerson(ctx, uuid.Nil); !errors.Is(err, ErrPersonBindingMissing) {
		t.Fatalf("unbound account: err = %v, want ErrPersonBindingMissing", err)
	}
	// And the two refusals stay distinguishable: a member told the wrong one goes looking
	// for the wrong help.
	if errors.Is(ErrPersonBindingMissing, ErrPersonScope) || errors.Is(ErrPersonScope, ErrPersonBindingMissing) {
		t.Fatal("the two person refusals are the same error")
	}
}

func TestRequirePersonWithChecksThePermissionFirst(t *testing.T) {
	me := uuid.New()
	ctx := memberContext(me)

	if _, _, err := RequirePersonWith(ctx, "accommodation.booking.create", uuid.Nil); err != nil {
		t.Fatalf("a bound member holding the permission was refused: %v", err)
	}
	// Missing the permission is a permission problem even for a bound member.
	if _, _, err := RequirePersonWith(ctx, "claim.create", uuid.Nil); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("missing permission: err = %v, want ErrPermissionDenied", err)
	}
	// And the scope is still enforced when the permission is held.
	if _, _, err := RequirePersonWith(ctx, "accommodation.booking.create", uuid.New()); !errors.Is(err, ErrPersonScope) {
		t.Fatalf("another person with the permission held: err = %v, want ErrPersonScope", err)
	}
	// No context at all is unauthenticated, not unbound.
	if _, _, err := RequirePersonWith(context.Background(), "accommodation.booking.create", uuid.Nil); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("no context: err = %v", err)
	}
}

func TestPersonFromScopesIsOneAnswerOrNone(t *testing.T) {
	me := uuid.New()
	org := uuid.NullUUID{UUID: uuid.New(), Valid: true}

	cases := []struct {
		name   string
		scopes []Scope
		want   uuid.NullUUID
	}{
		{"no scopes at all", nil, uuid.NullUUID{}},
		{"only an organization", []Scope{{Type: "ORGANIZATION", ID: org}}, uuid.NullUUID{}},
		{
			"a person beside an organization",
			[]Scope{{Type: "ORGANIZATION", ID: org}, {Type: ScopePerson, ID: uuid.NullUUID{UUID: me, Valid: true}}},
			uuid.NullUUID{UUID: me, Valid: true},
		},
		{
			"the same person twice is still one answer",
			[]Scope{
				{Type: ScopePerson, ID: uuid.NullUUID{UUID: me, Valid: true}},
				{Type: ScopePerson, ID: uuid.NullUUID{UUID: me, Valid: true}},
			},
			uuid.NullUUID{UUID: me, Valid: true},
		},
		{
			// The database refuses this pair outright; if one ever reached here the right
			// answer is none, not whichever came back first.
			"two different people is no answer",
			[]Scope{
				{Type: ScopePerson, ID: uuid.NullUUID{UUID: me, Valid: true}},
				{Type: ScopePerson, ID: uuid.NullUUID{UUID: uuid.New(), Valid: true}},
			},
			uuid.NullUUID{},
		},
		{
			"a PERSON scope with no id is not a binding",
			[]Scope{{Type: ScopePerson}},
			uuid.NullUUID{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PersonFromScopes(tc.scopes); got != tc.want {
				t.Fatalf("PersonFromScopes = %+v, want %+v", got, tc.want)
			}
		})
	}
}
