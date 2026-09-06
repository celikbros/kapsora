package partyhttp_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/celikbros/kapsora/api/generated/kapsorav1"
	orgdomaintest "github.com/celikbros/kapsora/internal/organization/domain/domaintest"
)

// problemBody is the RFC 9457 members these tests assert on. Every problem this product
// emits carries a Turkish title, and a member reading a refusal is exactly the reader that
// rule exists for.
type problemBody struct {
	Title  string `json:"title"`
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

func problemOf(t *testing.T, body string) problemBody {
	t.Helper()
	var p problemBody
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("not a problem document: %v (%s)", err, body)
	}
	return p
}

// TestGetMyPersonAnswersTheBoundPersonOnly is the whole of getMyPerson at the HTTP
// boundary: there is no path segment and no body naming a person, so the only record this
// route can return is the one the caller's own binding names.
func TestGetMyPersonAnswersTheBoundPersonOnly(t *testing.T) {
	s := newServer(t)
	tckn := orgdomaintest.GenerateTCKN(s.rand)
	me := s.createPerson(t, "Melis", "Üye", tckn)
	neighbour := s.createPerson(t, "Kerem", "Komşu", orgdomaintest.GenerateTCKN(s.rand))

	// A member holding none of the member.* grants still reads their own record: reading
	// oneself is not the act member.read exists to gate.
	rec := s.do(call{
		method: http.MethodGet, path: "/api/v1/me/person",
		perms: "eligibility.check", person: me.Id.String(),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("get my person: %d %s", rec.Code, rec.Body.String())
	}
	var got kapsorav1.MyPerson
	decode(t, rec, &got)
	if got.Person.Id != me.Id {
		t.Fatalf("answered with %s, want the bound person %s", got.Person.Id, me.Id)
	}
	if got.Person.Id == neighbour.Id {
		t.Fatal("the route answered with somebody else's record")
	}
	// The identifier is masked and the full number appears nowhere in the response.
	if got.Person.MaskedPrimaryIdentifier == nil || *got.Person.MaskedPrimaryIdentifier == "" {
		t.Fatal("the answer carries no masked identifier")
	}
	if strings.Contains(rec.Body.String(), tckn) {
		t.Fatal("the response body carries the plaintext identity number")
	}
	// The lists are present and empty rather than absent, so a client has nothing to guard.
	if got.Enrollments == nil || got.Contacts == nil {
		t.Fatalf("enrollments = %v contacts = %v, want empty lists", got.Enrollments, got.Contacts)
	}

	// A member bound to their neighbour reads the neighbour, and never both: the answer
	// follows the binding and nothing else. This is the assertion that would go red if the
	// handler ever took a person from anywhere but the request context.
	rec = s.do(call{
		method: http.MethodGet, path: "/api/v1/me/person",
		perms: "eligibility.check", person: neighbour.Id.String(),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("get my person as the neighbour: %d %s", rec.Code, rec.Body.String())
	}
	decode(t, rec, &got)
	if got.Person.Id != neighbour.Id {
		t.Fatalf("answered with %s, want %s", got.Person.Id, neighbour.Id)
	}
}

// TestGetMyPersonRefusesAnUnboundAccount is the refusal a member sees before onboarding has
// bound their account, and the reason it is its own problem code: PERMISSION_DENIED would
// send them to an administrator to ask for a grant nobody has to give.
func TestGetMyPersonRefusesAnUnboundAccount(t *testing.T) {
	s := newServer(t)
	person := s.createPerson(t, "Deniz", "Üye", orgdomaintest.GenerateTCKN(s.rand))

	// A back-office caller with every member permission and no binding: still refused.
	rec := s.do(call{method: http.MethodGet, path: "/api/v1/me/person"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unbound caller: %d %s", rec.Code, rec.Body.String())
	}
	p := problemOf(t, rec.Body.String())
	if p.Code != "PERSON_BINDING_MISSING" {
		t.Fatalf("problem code = %q, want PERSON_BINDING_MISSING", p.Code)
	}
	if p.Status != http.StatusForbidden {
		t.Fatalf("problem status = %d", p.Status)
	}
	// Turkish, and a sentence that tells the reader what to do about it.
	if p.Title == "" || !strings.ContainsAny(p.Title, "şğüıöçİŞĞÜÖÇ") {
		t.Fatalf("problem title %q is not Turkish", p.Title)
	}
	if p.Detail == "" {
		t.Fatalf("problem carries no detail: %+v", p)
	}
	// And the refusal says nothing about whether any person exists.
	if strings.Contains(rec.Body.String(), person.Id.String()) {
		t.Fatal("the refusal names a person")
	}

	// The content type is problem+json, as every refusal in this product is.
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type = %q", ct)
	}
}

// TestGetMyPersonRefusesABindingToAnotherTenantsPerson is the isolation half. A context
// carrying a person id from elsewhere reads nothing: the read runs inside this tenant's
// transaction, and the answer is the same 404 any unknown person gets.
func TestGetMyPersonRefusesABindingToAnotherTenantsPerson(t *testing.T) {
	s := newServer(t)
	var foreign string
	ctx, cancel := s.h.Ctx()
	defer cancel()
	other := s.h.CreateTenant("HTTP_PARTY_OTHER")
	if err := s.h.Admin.QueryRow(ctx, `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Yabancı', 'Kişi', 'yabanci kisi') RETURNING id::text`, other).Scan(&foreign); err != nil {
		t.Fatalf("seed the foreign person: %v", err)
	}

	rec := s.do(call{
		method: http.MethodGet, path: "/api/v1/me/person",
		perms: "eligibility.check", person: foreign,
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("a binding into another tenant: %d %s", rec.Code, rec.Body.String())
	}
	if code := problemOf(t, rec.Body.String()).Code; code != "PERSON_NOT_FOUND" {
		t.Fatalf("problem code = %q, want PERSON_NOT_FOUND", code)
	}
}

// TestGetMyPersonDoesNotShadowTheTenantPicker is the regression this route very nearly
// introduced. Mounting a subrouter at /me makes it the handler for the whole /me subtree,
// and the pre-tenant GET /api/v1/me that builds the tenant picker starts answering 404 --
// silently, with nothing in this package's own tests noticing. The route is registered as
// a flat pattern instead, and this test is what keeps it that way.
func TestGetMyPersonDoesNotShadowTheTenantPicker(t *testing.T) {
	s := newServer(t)
	me := s.createPerson(t, "Selin", "Üye", orgdomaintest.GenerateTCKN(s.rand))

	rec := s.do(call{method: http.MethodGet, path: "/api/v1/me"})
	if rec.Code != http.StatusTeapot {
		t.Fatalf("GET /api/v1/me = %d, want the stand-in's %d: the member route shadowed it",
			rec.Code, http.StatusTeapot)
	}
	rec = s.do(call{
		method: http.MethodGet, path: "/api/v1/me/person",
		perms: "eligibility.check", person: me.Id.String(),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/me/person = %d %s", rec.Code, rec.Body.String())
	}
}
