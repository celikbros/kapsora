package identity

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestRefuseOwnFileRefusesOnlyTheCallersOwnPerson(t *testing.T) {
	me, other := uuid.New(), uuid.New()
	staffWhoIsAMember := RequestContext{SelfPersonID: uuid.NullUUID{UUID: me, Valid: true}}
	staffOnly := RequestContext{}

	if err := RefuseOwnFile(staffWhoIsAMember, me); !errors.Is(err, ErrOwnFile) {
		t.Fatalf("own file: err = %v, want ErrOwnFile", err)
	}
	if err := RefuseOwnFile(staffWhoIsAMember, other); err != nil {
		t.Fatalf("somebody else's file: err = %v", err)
	}
	if err := RefuseOwnFile(staffOnly, me); err != nil {
		t.Fatalf("an account bound to nobody: err = %v", err)
	}
	// A file with no person is nobody's own file.
	if err := RefuseOwnFile(RequestContext{SelfPersonID: uuid.NullUUID{Valid: true}}, uuid.Nil); err != nil {
		t.Fatalf("a file with no person: err = %v", err)
	}
}
