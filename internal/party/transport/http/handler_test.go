package partyhttp_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
	orgdomaintest "github.com/celikbros/kapsora/internal/organization/domain/domaintest"
	"github.com/celikbros/kapsora/internal/party/application"
	partypg "github.com/celikbros/kapsora/internal/party/infrastructure/postgres"
	partyhttp "github.com/celikbros/kapsora/internal/party/transport/http"
	"github.com/celikbros/kapsora/internal/platform/crypto/localkey"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Test headers letting each request choose its permissions and step-up state.
const (
	permsHeader  = "X-Test-Permissions"
	stepUpHeader = "X-Test-StepUp"

	allPermissions = "member.read,member.manage,member.relationship.manage,membership.manage"
	patchType      = "application/merge-patch+json"
)

type denyRecorder struct{ permissions []string }

func (d *denyRecorder) Deny(w http.ResponseWriter, r *http.Request, err error, permission string) {
	d.permissions = append(d.permissions, permission)
	identityhttp.WriteAuthError(w, r, err, nil)
}

type server struct {
	h       *dbtest.Harness
	handler http.Handler
	denied  *denyRecorder
	logs    *bytes.Buffer
	tenant  uuid.UUID
	rand    *rand.Rand
}

func newServer(t *testing.T) *server {
	t.Helper()
	h := dbtest.New(t)
	keys, err := localkey.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	cursors, err := httpx.NewCursorCodec([]byte("fedcba9876543210fedcba9876543210"))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := application.New(application.Deps{
		Pool: h.App, Repo: partypg.New(), Cipher: keys, Index: keys, Audit: auditpg.New(), Cursors: cursors,
	})
	if err != nil {
		t.Fatal(err)
	}
	tenant := h.CreateTenant("HTTP_PARTY")
	actor := h.CreateActor("party-http", "Party HTTP")
	seedCatalogs(h, tenant)

	logs := &bytes.Buffer{}
	denied := &denyRecorder{}
	handler := partyhttp.NewHandler(svc, denied, slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))

	// Stand-in for RequireTenantContext: permissions and step-up come from test headers.
	fakeContext := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rc := identity.RequestContext{
				TenantID: tenant, MembershipID: uuid.New(),
				Principal:   identity.Principal{ActorID: actor},
				Permissions: map[string]struct{}{},
				StepUpValid: r.Header.Get(stepUpHeader) == "1",
			}
			for _, p := range strings.Split(r.Header.Get(permsHeader), ",") {
				if p != "" {
					rc.Permissions[p] = struct{}{}
				}
			}
			next.ServeHTTP(w, r.WithContext(identity.WithRequestContext(r.Context(), rc)))
		})
	}

	r := chi.NewRouter()
	r.Route("/api/v1/people", func(rr chi.Router) {
		rr.Use(fakeContext)
		handler.Routes(rr, partyhttp.Middlewares{})
	})
	r.Route("/api/v1/party", func(rr chi.Router) {
		rr.Use(fakeContext)
		handler.CatalogRoutes(rr)
	})
	return &server{h: h, handler: r, denied: denied, logs: logs, tenant: tenant, rand: rand.New(rand.NewPCG(21, 22))}
}

func seedCatalogs(h *dbtest.Harness, tenant uuid.UUID) {
	c := identityapp.DefaultBaselineCatalogs()
	for _, e := range c.IdentifierTypes {
		h.AdminExec(`INSERT INTO party.identifier_type (tenant_id, code, display_name, is_sensitive, uniqueness_scope)
		             VALUES ($1, $2, $3, $4, $5)`, tenant, e.Code, e.DisplayName, e.Flag, e.Scope)
	}
	for _, e := range c.RelationshipTypes {
		h.AdminExec(`INSERT INTO party.relationship_type (tenant_id, code, display_name, is_directional)
		             VALUES ($1, $2, $3, $4)`, tenant, e.Code, e.DisplayName, e.Flag)
	}
	for _, e := range c.MembershipTypes {
		h.AdminExec(`INSERT INTO party.membership_type (tenant_id, code, display_name, requires_principal)
		             VALUES ($1, $2, $3, $4)`, tenant, e.Code, e.DisplayName, e.Flag)
	}
}

type call struct {
	method, path, body, contentType, ifMatch, perms string
	stepUp                                          bool
}

func (s *server) do(c call) *httptest.ResponseRecorder {
	var body io.Reader
	if c.body != "" {
		body = strings.NewReader(c.body)
	}
	req := httptest.NewRequest(c.method, c.path, body)
	if c.contentType == "" {
		c.contentType = "application/json"
	}
	req.Header.Set("Content-Type", c.contentType)
	if c.ifMatch != "" {
		req.Header.Set("If-Match", c.ifMatch)
	}
	if c.perms == "" {
		c.perms = allPermissions
	}
	req.Header.Set(permsHeader, c.perms)
	if c.stepUp {
		req.Header.Set(stepUpHeader, "1")
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func (s *server) createPerson(t *testing.T, first, last, tckn string) kapsorav1.Person {
	t.Helper()
	body := map[string]any{"firstName": first, "lastName": last}
	if tckn != "" {
		body["identifiers"] = []map[string]any{{"type": "TCKN", "value": tckn, "primary": true}}
	}
	raw, _ := json.Marshal(body)
	rec := s.do(call{method: http.MethodPost, path: "/api/v1/people", body: string(raw)})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create person: %d %s", rec.Code, rec.Body.String())
	}
	var person kapsorav1.Person
	decode(t, rec, &person)
	return person
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

func TestCreateGetAndMaskingThroughHTTP(t *testing.T) {
	s := newServer(t)
	tckn := orgdomaintest.GenerateTCKN(s.rand)

	rec := s.do(call{method: http.MethodPost, path: "/api/v1/people", body: `{"firstName":"Ayşe","lastName":"Yılmaz","birthDate":"1990-05-04","sexAtBirth":"FEMALE","identifiers":[{"type":"TCKN","value":"` + tckn + `","primary":true}]}`})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("ETag") != `"1"` || !strings.HasPrefix(rec.Header().Get("Location"), "/api/v1/people/") {
		t.Fatalf("headers = %v", rec.Header())
	}
	if strings.Contains(rec.Body.String(), tckn) {
		t.Fatal("the full identifier appears in the response body")
	}
	var person kapsorav1.Person
	decode(t, rec, &person)
	if person.DisplayName != "Ayşe Yılmaz" || person.RowVersion != 1 || person.Identifiers == nil {
		t.Fatalf("person = %+v", person)
	}
	if got := (*person.Identifiers)[0].MaskedValue; got != tckn[:3]+"******"+tckn[9:] {
		t.Fatalf("masked value = %q", got)
	}
	if person.MaskedPrimaryIdentifier == nil || person.BirthDate == nil || person.BirthDate.Format("2006-01-02") != "1990-05-04" {
		t.Fatalf("person = %+v", person)
	}

	rec = s.do(call{method: http.MethodGet, path: "/api/v1/people/" + person.Id.String()})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("get: %d etag=%q", rec.Code, rec.Header().Get("ETag"))
	}
	rec = s.do(call{method: http.MethodGet, path: "/api/v1/people/" + uuid.NewString()})
	var p map[string]any
	decode(t, rec, &p)
	if rec.Code != http.StatusNotFound || p["code"] != "PERSON_NOT_FOUND" {
		t.Fatalf("unknown id: %d %v", rec.Code, p)
	}
	if rec := s.do(call{method: http.MethodGet, path: "/api/v1/people/not-a-uuid"}); rec.Code != http.StatusNotFound {
		t.Fatalf("malformed id must look like unknown: %d", rec.Code)
	}
	if strings.Contains(s.logs.String(), tckn) {
		t.Fatal("the identifier leaked into the logs")
	}
}

func TestPermissionDenialsAndStepUpOnSearch(t *testing.T) {
	s := newServer(t)
	tckn := orgdomaintest.GenerateTCKN(s.rand)
	person := s.createPerson(t, "Aranan", "Kişi", tckn)
	searchBody := `{"type":"TCKN","value":"` + tckn + `"}`

	rec := s.do(call{method: http.MethodPost, path: "/api/v1/people", body: `{"firstName":"A","lastName":"B"}`, perms: "member.read"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("create without member.manage: %d", rec.Code)
	}
	var p map[string]any
	decode(t, rec, &p)
	if p["code"] != "PERMISSION_DENIED" || s.denied.permissions[0] != "member.manage" {
		t.Fatalf("denial = %v recorded=%v", p, s.denied.permissions)
	}

	// Without the search permission the endpoint denies before any lookup.
	rec = s.do(call{method: http.MethodPost, path: "/api/v1/people/search-by-identifier", body: searchBody, stepUp: true})
	decode(t, rec, &p)
	if rec.Code != http.StatusForbidden || p["code"] != "PERMISSION_DENIED" {
		t.Fatalf("search without permission: %d %v", rec.Code, p)
	}
	// With the permission but no step-up it is 403 STEP_UP_REQUIRED.
	rec = s.do(call{method: http.MethodPost, path: "/api/v1/people/search-by-identifier", body: searchBody, perms: "member.identifier.search"})
	decode(t, rec, &p)
	if rec.Code != http.StatusForbidden || p["code"] != "STEP_UP_REQUIRED" {
		t.Fatalf("search without step-up: %d %v", rec.Code, p)
	}
	// With both it resolves the person and returns only the masked identifier.
	rec = s.do(call{method: http.MethodPost, path: "/api/v1/people/search-by-identifier", body: searchBody, perms: "member.identifier.search", stepUp: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("search: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), tckn) {
		t.Fatal("the search response echoes the identifier")
	}
	var summary kapsorav1.PersonSummary
	decode(t, rec, &summary)
	if summary.Id != person.Id {
		t.Fatalf("summary = %+v", summary)
	}

	// Every denial went through the auditing denier.
	if len(s.denied.permissions) != 3 {
		t.Fatalf("denials = %v", s.denied.permissions)
	}
	for _, perm := range []string{"member.read", "membership.manage", "member.relationship.manage"} {
		if rec := s.do(call{method: http.MethodGet, path: "/api/v1/people", perms: perm}); perm != "member.read" && rec.Code != http.StatusForbidden {
			t.Fatalf("list with %s: %d", perm, rec.Code)
		}
	}
	if rec := s.do(call{method: http.MethodGet, path: "/api/v1/party/catalogs", perms: "audit.read"}); rec.Code != http.StatusForbidden {
		t.Fatalf("catalogs without member.read: %d", rec.Code)
	}
	if strings.Contains(s.logs.String(), tckn) {
		t.Fatal("the identifier leaked into the logs")
	}
}

func TestValidationPayloads(t *testing.T) {
	s := newServer(t)

	rec := s.do(call{method: http.MethodPost, path: "/api/v1/people", body: `{"firstName":"Ali","lastName":"Veli","identifiers":[{"type":"TCKN","value":"11111111111"}]}`})
	var problem struct {
		Code   string `json:"code"`
		Errors []struct {
			Field string `json:"field"`
			Code  string `json:"code"`
		} `json:"errors"`
	}
	decode(t, rec, &problem)
	if rec.Code != http.StatusUnprocessableEntity || problem.Code != "VALIDATION_FAILED" {
		t.Fatalf("bad TCKN: %d %+v", rec.Code, problem)
	}
	if problem.Errors[0].Field != "identifiers[0].value" || problem.Errors[0].Code != "IDENTIFIER_INVALID" {
		t.Fatalf("errors = %+v", problem.Errors)
	}
	if rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Fatalf("content type = %q", rec.Header().Get("Content-Type"))
	}

	// An identifier type the tenant does not define.
	rec = s.do(call{method: http.MethodPost, path: "/api/v1/people", body: `{"firstName":"Ali","lastName":"Veli","identifiers":[{"type":"NATO_ID","value":"X1"}]}`})
	decode(t, rec, &problem)
	if rec.Code != http.StatusUnprocessableEntity || problem.Errors[0].Code != "IDENTIFIER_TYPE_UNKNOWN" {
		t.Fatalf("unknown identifier type: %d %+v", rec.Code, problem)
	}
	// A SPONSOR-scoped identifier on a person without a membership.
	rec = s.do(call{method: http.MethodPost, path: "/api/v1/people", body: `{"firstName":"Ali","lastName":"Veli","identifiers":[{"type":"MEMBER_NO","value":"M-1"}]}`})
	decode(t, rec, &problem)
	if rec.Code != http.StatusUnprocessableEntity || problem.Errors[0].Code != "IDENTIFIER_SCOPE_REQUIRED" {
		t.Fatalf("sponsor-scoped identifier: %d %+v", rec.Code, problem)
	}

	if rec := s.do(call{method: http.MethodPost, path: "/api/v1/people", body: `{"firstName":"Ali","bogus":1}`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}
	rec = s.do(call{method: http.MethodGet, path: "/api/v1/people?cursor=bogus"})
	var generic map[string]any
	decode(t, rec, &generic)
	if rec.Code != http.StatusBadRequest || generic["code"] != "CURSOR_INVALID" {
		t.Fatalf("bad cursor: %d %v", rec.Code, generic)
	}
	if rec := s.do(call{method: http.MethodGet, path: "/api/v1/people?limit=abc"}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad limit: %d", rec.Code)
	}
	if rec := s.do(call{method: http.MethodGet, path: "/api/v1/people?status=GONE"}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad status: %d", rec.Code)
	}
	if rec := s.do(call{method: http.MethodGet, path: "/api/v1/people?sponsorOrganizationId=nope"}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad sponsor id: %d", rec.Code)
	}
}

func TestPatchPreconditionsAndConflicts(t *testing.T) {
	s := newServer(t)
	first := orgdomaintest.GenerateTCKN(s.rand)
	second := orgdomaintest.GenerateTCKN(s.rand)
	person := s.createPerson(t, "Güncellenecek", "Kişi", first)
	other := s.createPerson(t, "Başka", "Kişi", second)
	path := "/api/v1/people/" + person.Id.String()

	if rec := s.do(call{method: http.MethodPatch, path: path, contentType: "application/json", ifMatch: `"1"`, body: `{"firstName":"X"}`}); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong content type: %d", rec.Code)
	}
	if rec := s.do(call{method: http.MethodPatch, path: path, contentType: patchType, body: `{"firstName":"X"}`}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match: %d", rec.Code)
	}
	if rec := s.do(call{method: http.MethodPatch, path: path, contentType: patchType, ifMatch: `"9"`, body: `{"firstName":"X"}`}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: %d", rec.Code)
	}
	if rec := s.do(call{method: http.MethodPatch, path: path, contentType: patchType, ifMatch: `"1"`, body: `{"nickname":"X"}`}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown patch field: %d", rec.Code)
	}

	// Taking another person's identifier is a 409; the owner is named only for callers
	// that may search by identifier anyway.
	body := `{"identifiers":[{"type":"TCKN","value":"` + second + `"}]}`
	rec := s.do(call{method: http.MethodPatch, path: path, contentType: patchType, ifMatch: `W/"1"`, body: body})
	var problem map[string]any
	decode(t, rec, &problem)
	if rec.Code != http.StatusConflict || problem["code"] != "PERSON_IDENTIFIER_TAKEN" {
		t.Fatalf("identifier taken: %d %v", rec.Code, problem)
	}
	if detail, ok := problem["detail"].(string); ok && strings.Contains(detail, other.Id.String()) {
		t.Fatal("the owner must stay hidden without member.identifier.search")
	}
	rec = s.do(call{method: http.MethodPatch, path: path, contentType: patchType, ifMatch: `"1"`, body: body,
		perms: allPermissions + ",member.identifier.search"})
	decode(t, rec, &problem)
	if rec.Code != http.StatusConflict || !strings.Contains(problem["detail"].(string), other.Id.String()) {
		t.Fatalf("owner should be named for a searcher: %v", problem)
	}

	// A well-formed patch bumps the version and clears a nulled field.
	rec = s.do(call{method: http.MethodPatch, path: path, contentType: patchType, ifMatch: `"1"`,
		body: `{"firstName":"Güncel","middleName":"Nur","status":"INACTIVE"}`})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("patch: %d etag=%q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	var patched kapsorav1.Person
	decode(t, rec, &patched)
	if patched.FirstName != "Güncel" || patched.MiddleName == nil || *patched.MiddleName != "Nur" || string(patched.Status) != "INACTIVE" {
		t.Fatalf("patched = %+v", patched)
	}
	rec = s.do(call{method: http.MethodPatch, path: path, contentType: patchType, ifMatch: `"2"`, body: `{"middleName":null}`})
	var cleared kapsorav1.Person
	decode(t, rec, &cleared)
	if rec.Code != http.StatusOK || cleared.MiddleName != nil {
		t.Fatalf("null clears middle name: %d %+v", rec.Code, cleared)
	}
	// MERGED is not settable through the patch.
	if rec := s.do(call{method: http.MethodPatch, path: path, contentType: patchType, ifMatch: `"3"`, body: `{"status":"MERGED"}`}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("MERGED through patch: %d", rec.Code)
	}
}

func TestRelationshipsAndMembershipsThroughHTTP(t *testing.T) {
	s := newServer(t)
	sponsor := s.h.CreateTenantOrganization(s.tenant, "Sponsor A.Ş.", "SPONSOR")
	parent := s.createPerson(t, "Ebeveyn", "Kişi", "")
	child := s.createPerson(t, "Çocuk", "Kişi", "")
	base := "/api/v1/people/" + parent.Id.String()

	rec := s.do(call{method: http.MethodPost, path: base + "/relationships",
		body: `{"targetPersonId":"` + child.Id.String() + `","relationshipType":"CHILD","validFrom":"2020-01-01"}`})
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create relationship: %d %s", rec.Code, rec.Body.String())
	}
	var rel kapsorav1.PersonRelationship
	decode(t, rec, &rel)
	if string(rel.Direction) != "OUTGOING" || rel.OtherPerson.Id != child.Id {
		t.Fatalf("relationship = %+v", rel)
	}
	// The overlap guard answers 409.
	rec = s.do(call{method: http.MethodPost, path: base + "/relationships",
		body: `{"targetPersonId":"` + child.Id.String() + `","relationshipType":"CHILD","validFrom":"2021-01-01"}`})
	var problem map[string]any
	decode(t, rec, &problem)
	if rec.Code != http.StatusConflict || problem["code"] != "RELATIONSHIP_OVERLAP" {
		t.Fatalf("overlap: %d %v", rec.Code, problem)
	}
	// Self-relationships are 422.
	rec = s.do(call{method: http.MethodPost, path: base + "/relationships",
		body: `{"targetPersonId":"` + parent.Id.String() + `","relationshipType":"CHILD","validFrom":"2020-01-01"}`})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("self relationship: %d", rec.Code)
	}
	// Ending needs If-Match.
	endPath := base + "/relationships/" + rel.Id.String() + "/end"
	if rec := s.do(call{method: http.MethodPost, path: endPath, body: `{"endsOn":"2026-01-01","reasonCode":"CUSTODY_CHANGE"}`}); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("end without If-Match: %d", rec.Code)
	}
	rec = s.do(call{method: http.MethodPost, path: endPath, ifMatch: `"1"`, body: `{"endsOn":"2026-01-01","reasonCode":"CUSTODY_CHANGE"}`})
	decode(t, rec, &rel)
	if rec.Code != http.StatusOK || string(rel.Status) != "ENDED" || rel.EndReasonCode == nil {
		t.Fatalf("end relationship: %d %+v", rec.Code, rel)
	}
	rec = s.do(call{method: http.MethodGet, path: "/api/v1/people/" + child.Id.String() + "/relationships"})
	var relList struct {
		Items []kapsorav1.PersonRelationship `json:"items"`
	}
	decode(t, rec, &relList)
	if rec.Code != http.StatusOK || len(relList.Items) != 1 || string(relList.Items[0].Direction) != "INCOMING" {
		t.Fatalf("relationship list = %d %+v", rec.Code, relList.Items)
	}

	// Memberships.
	rec = s.do(call{method: http.MethodPost, path: base + "/memberships",
		body: `{"sponsorOrganizationId":"` + sponsor.String() + `","membershipType":"EMPLOYEE","externalMemberNo":"EMP-9","validFrom":"2024-01-01"}`})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create membership: %d %s", rec.Code, rec.Body.String())
	}
	var membership kapsorav1.SponsorMembership
	decode(t, rec, &membership)
	if membership.SponsorDisplayName != "Sponsor A.Ş." || string(membership.Status) != "ACTIVE" {
		t.Fatalf("membership = %+v", membership)
	}
	// FAMILY without a principal is 422 PRINCIPAL_REQUIRED.
	rec = s.do(call{method: http.MethodPost, path: "/api/v1/people/" + child.Id.String() + "/memberships",
		body: `{"sponsorOrganizationId":"` + sponsor.String() + `","membershipType":"FAMILY","validFrom":"2024-01-01"}`})
	var validation struct {
		Errors []struct{ Field, Code string } `json:"errors"`
	}
	decode(t, rec, &validation)
	if rec.Code != http.StatusUnprocessableEntity || validation.Errors[0].Code != "PRINCIPAL_REQUIRED" {
		t.Fatalf("dependant without principal: %d %+v", rec.Code, validation.Errors)
	}
	// The member number is unique per sponsor.
	rec = s.do(call{method: http.MethodPost, path: "/api/v1/people/" + child.Id.String() + "/memberships",
		body: `{"sponsorOrganizationId":"` + sponsor.String() + `","membershipType":"MEMBER","externalMemberNo":"EMP-9","validFrom":"2024-01-01"}`})
	decode(t, rec, &problem)
	if rec.Code != http.StatusConflict || problem["code"] != "MEMBER_NO_TAKEN" {
		t.Fatalf("member number: %d %v", rec.Code, problem)
	}
	// Patch under If-Match.
	membershipPath := base + "/memberships/" + membership.Id.String()
	rec = s.do(call{method: http.MethodPatch, path: membershipPath, contentType: patchType, ifMatch: `"1"`, body: `{"status":"ENDED","validTo":"2026-06-30"}`})
	decode(t, rec, &membership)
	if rec.Code != http.StatusOK || string(membership.Status) != "ENDED" || membership.ValidTo == nil {
		t.Fatalf("patch membership: %d %+v", rec.Code, membership)
	}
	rec = s.do(call{method: http.MethodGet, path: base + "/memberships"})
	var memberships struct {
		Items []kapsorav1.SponsorMembership `json:"items"`
	}
	decode(t, rec, &memberships)
	if rec.Code != http.StatusOK || len(memberships.Items) != 1 {
		t.Fatalf("membership list = %d %+v", rec.Code, memberships.Items)
	}
}

func TestCatalogsAndListPaging(t *testing.T) {
	s := newServer(t)
	for i := 0; i < 3; i++ {
		s.createPerson(t, "Liste", string(rune('A'+i)), orgdomaintest.GenerateTCKN(s.rand))
	}

	rec := s.do(call{method: http.MethodGet, path: "/api/v1/party/catalogs"})
	if rec.Code != http.StatusOK {
		t.Fatalf("catalogs: %d %s", rec.Code, rec.Body.String())
	}
	var catalogs kapsorav1.PartyCatalogs
	decode(t, rec, &catalogs)
	if len(catalogs.IdentifierTypes) != 4 || len(catalogs.RelationshipTypes) != 6 || len(catalogs.MembershipTypes) != 8 {
		t.Fatalf("catalogs = %+v", catalogs)
	}
	if scope := catalogs.IdentifierTypes[len(catalogs.IdentifierTypes)-1].UniquenessScope; scope == nil {
		t.Fatal("identifier types must carry their uniqueness scope")
	}

	seen := map[string]bool{}
	cursor := ""
	for pages := 0; pages < 5; pages++ {
		path := "/api/v1/people?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		rec := s.do(call{method: http.MethodGet, path: path})
		if rec.Code != http.StatusOK {
			t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
		}
		var page kapsorav1.PersonPage
		decode(t, rec, &page)
		for _, it := range page.Items {
			if seen[it.Id.String()] {
				t.Fatalf("duplicate %s", it.Id)
			}
			seen[it.Id.String()] = true
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if len(seen) != 3 {
		t.Fatalf("listed %d persons, want 3", len(seen))
	}
}
