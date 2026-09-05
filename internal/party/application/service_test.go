package application_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	orgdomaintest "github.com/celikbros/kapsora/internal/organization/domain/domaintest"
	"github.com/celikbros/kapsora/internal/party/application"
	"github.com/celikbros/kapsora/internal/party/domain"
	partypg "github.com/celikbros/kapsora/internal/party/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/platform/crypto"
	"github.com/celikbros/kapsora/internal/platform/crypto/localkey"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// partyTables are scanned by the "no plaintext anywhere" assertion.
var partyTables = []string{
	"party.person", "party.person_identifier", "party.identifier_type",
	"party.relationship_type", "party.person_relationship",
	"party.membership_type", "party.sponsor_membership",
	// Contact details are held exactly as identifiers are (WP-I5-05), so they are scanned
	// exactly as identifiers are.
	"party.person_contact",
}

type fixture struct {
	h       *dbtest.Harness
	svc     *application.Service
	keys    *localkey.Provider
	logs    *bytes.Buffer
	rand    *rand.Rand
	tenantA uuid.UUID
	tenantB uuid.UUID
	actor   uuid.UUID
	used    map[string]bool
}

func newFixture(t *testing.T) *fixture {
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

	// Every log line written while the test runs is checked for leaked plaintext.
	logs := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	f := &fixture{
		h: h, svc: svc, keys: keys, logs: logs, rand: rand.New(rand.NewPCG(3, 5)),
		tenantA: h.CreateTenant("PARTY_A"), tenantB: h.CreateTenant("PARTY_B"),
		actor: h.CreateActor("party-actor", "Party Actor"), used: map[string]bool{},
	}
	f.seedCatalogs(f.tenantA)
	f.seedCatalogs(f.tenantB)
	return f
}

// seedCatalogs writes the same baseline rows WP-I1-02 provisioning gives a new tenant.
func (f *fixture) seedCatalogs(tenant uuid.UUID) {
	f.h.T.Helper()
	c := identityapp.DefaultBaselineCatalogs()
	for _, e := range c.IdentifierTypes {
		f.h.AdminExec(`INSERT INTO party.identifier_type (tenant_id, code, display_name, is_sensitive, uniqueness_scope)
		               VALUES ($1, $2, $3, $4, $5)`, tenant, e.Code, e.DisplayName, e.Flag, e.Scope)
	}
	for _, e := range c.RelationshipTypes {
		f.h.AdminExec(`INSERT INTO party.relationship_type (tenant_id, code, display_name, is_directional)
		               VALUES ($1, $2, $3, $4)`, tenant, e.Code, e.DisplayName, e.Flag)
	}
	for _, e := range c.MembershipTypes {
		f.h.AdminExec(`INSERT INTO party.membership_type (tenant_id, code, display_name, requires_principal)
		               VALUES ($1, $2, $3, $4)`, tenant, e.Code, e.DisplayName, e.Flag)
	}
}

func (f *fixture) rc(tenant uuid.UUID) identity.RequestContext {
	return identity.RequestContext{
		TenantID: tenant, MembershipID: uuid.New(), Principal: identity.Principal{ActorID: f.actor},
		StepUpValid: true,
		Permissions: map[string]struct{}{
			"member.read": {}, "member.manage": {}, "member.identifier.search": {},
			"member.relationship.manage": {}, "membership.manage": {},
		},
	}
}

// tckn returns a fresh checksum-valid identity number not used before in this fixture.
func (f *fixture) tckn() string {
	for {
		v := orgdomaintest.GenerateTCKN(f.rand)
		if !f.used[v] {
			f.used[v] = true
			return v
		}
	}
}

func (f *fixture) create(t *testing.T, tenant uuid.UUID, first, last string, ids ...domain.SubmittedIdentifier) application.Person {
	t.Helper()
	p, err := f.svc.Create(context.Background(), f.rc(tenant), domain.NewPerson{
		FirstName: first, LastName: last, Identifiers: ids,
	})
	if err != nil {
		t.Fatalf("create %s %s: %v", first, last, err)
	}
	return p
}

// dump returns the text form of every row of a table, which is how the leak assertion
// looks inside bytea (hex), jsonb and text columns at once.
func (f *fixture) dump(t *testing.T, table string) string {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var out string
	query := fmt.Sprintf(`SELECT coalesce(string_agg(t::text, ' '), '') FROM %s t`, table)
	if err := f.h.Admin.QueryRow(ctx, query).Scan(&out); err != nil {
		t.Fatalf("dump %s: %v", table, err)
	}
	return out
}

func TestCreateStoresEncryptedIdentifierAndBlindIndex(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tckn := f.tckn()

	person := f.create(t, f.tenantA, "Ayşe", "Yılmaz", domain.SubmittedIdentifier{Type: domain.TypeTCKN, Value: tckn, Primary: true})
	if person.RowVersion != 1 || person.DisplayName != "Ayşe Yılmaz" || person.Status != "ACTIVE" {
		t.Fatalf("person = %+v", person)
	}
	if len(person.Identifiers) != 1 || person.Identifiers[0].MaskedValue != tckn[:3]+"******"+tckn[9:] {
		t.Fatalf("identifiers = %+v", person.Identifiers)
	}
	if person.MaskedPrimaryIdentifier != person.Identifiers[0].MaskedValue {
		t.Fatalf("primary mask = %q", person.MaskedPrimaryIdentifier)
	}

	dbCtx, cancel := f.h.Ctx()
	defer cancel()
	var cipher, hash []byte
	var scopeKey string
	err := f.h.Admin.QueryRow(dbCtx, `SELECT identifier_cipher, identifier_hash, scope_key
	                                    FROM party.person_identifier WHERE person_id = $1`, person.ID).
		Scan(&cipher, &hash, &scopeKey)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := f.keys.Decrypt(ctx, f.tenantA, crypto.PurposePersonIdentifier, cipher)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(plain) != tckn {
		t.Fatal("ciphertext does not round-trip to the submitted value")
	}
	want, err := f.keys.TenantIndex(ctx, f.tenantA, crypto.PurposePersonIdentifier, domain.BlindIndexInput(domain.TypeTCKN, tckn))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(hash, want) {
		t.Fatal("stored blind index differs from the recomputed one")
	}
	if scopeKey != "" {
		t.Fatalf("TENANT-scoped identifier scope_key = %q, want empty", scopeKey)
	}

	// The same value in another tenant is a different index and a different row.
	other := f.create(t, f.tenantB, "Ayşe", "Yılmaz", domain.SubmittedIdentifier{Type: domain.TypeTCKN, Value: tckn, Primary: true})
	otherHash, err := f.keys.TenantIndex(ctx, f.tenantB, crypto.PurposePersonIdentifier, domain.BlindIndexInput(domain.TypeTCKN, tckn))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(hash, otherHash) {
		t.Fatal("the blind index is not tenant-salted")
	}
	if other.ID == person.ID {
		t.Fatal("tenants must not share person rows")
	}

	// A second person of the same tenant cannot take the identifier.
	_, err = f.svc.Create(ctx, f.rc(f.tenantA), domain.NewPerson{
		FirstName: "Başka", LastName: "Kişi",
		Identifiers: []domain.SubmittedIdentifier{{Type: domain.TypeTCKN, Value: tckn}},
	})
	var taken *application.IdentifierTakenError
	if !errors.As(err, &taken) || !errors.Is(err, application.ErrIdentifierTaken) {
		t.Fatalf("duplicate TCKN: %v", err)
	}
	if taken.ExistingPersonID != person.ID {
		t.Fatalf("owner = %s, want %s", taken.ExistingPersonID, person.ID)
	}
}

func TestIdentifierPlaintextNeverReachesStorageAuditOrLogs(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tckn := f.tckn()

	person := f.create(t, f.tenantA, "Gizli", "Kişi", domain.SubmittedIdentifier{Type: domain.TypeTCKN, Value: tckn, Primary: true})
	if _, err := f.svc.SearchByIdentifier(ctx, f.rc(f.tenantA), application.SearchInput{Type: domain.TypeTCKN, Value: tckn}); err != nil {
		t.Fatalf("search: %v", err)
	}
	body, err := json.Marshal(person)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), tckn) {
		t.Fatal("response model contains the plaintext identifier")
	}

	haystacks := map[string]string{}
	for _, table := range partyTables {
		haystacks[table] = f.dump(t, table)
	}
	haystacks["audit.event"] = f.dump(t, "audit.event")
	haystacks["audit.access_event"] = f.dump(t, "audit.access_event")
	haystacks["logs"] = f.logs.String()
	for where, hay := range haystacks {
		if strings.Contains(hay, tckn) {
			t.Errorf("plaintext identifier found in %s", where)
		}
	}
	// The audit trail still names the person and the action.
	if !strings.Contains(haystacks["audit.event"], "person.create") {
		t.Fatal("person.create was not audited")
	}
	if !strings.Contains(haystacks["party.person_identifier"], "TCKN") {
		t.Fatal("the identifier type should be visible, only the value is secret")
	}
}

func TestSearchByIdentifierFindsThePersonAndWritesAnAccessEvent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tckn := f.tckn()
	person := f.create(t, f.tenantA, "Aranan", "Kişi", domain.SubmittedIdentifier{Type: domain.TypeTCKN, Value: tckn, Primary: true})

	found, err := f.svc.SearchByIdentifier(ctx, f.rc(f.tenantA), application.SearchInput{Type: domain.TypeTCKN, Value: " " + tckn + " "})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if found.ID != person.ID || found.DisplayName != "Aranan Kişi" {
		t.Fatalf("found = %+v", found)
	}

	dbCtx, cancel := f.h.Ctx()
	defer cancel()
	var accessType, classification, reason string
	var personID uuid.NullUUID
	err = f.h.Admin.QueryRow(dbCtx, `SELECT access_type, data_classification, coalesce(reason_text,''), person_id
	                                   FROM audit.access_event WHERE tenant_id = $1 ORDER BY occurred_at DESC LIMIT 1`, f.tenantA).
		Scan(&accessType, &classification, &reason, &personID)
	if err != nil {
		t.Fatal(err)
	}
	if accessType != "SEARCH" || classification != "PERSONAL" || reason != "identifier_type=TCKN" {
		t.Fatalf("access event = %s/%s/%s", accessType, classification, reason)
	}
	if !personID.Valid || personID.UUID != person.ID {
		t.Fatalf("access event person = %v", personID)
	}

	// A miss is a 404 and is audited as well.
	if _, err := f.svc.SearchByIdentifier(ctx, f.rc(f.tenantA), application.SearchInput{Type: domain.TypeTCKN, Value: f.tckn()}); !errors.Is(err, application.ErrPersonNotFound) {
		t.Fatalf("unknown identifier: %v", err)
	}
	var events int
	if err := f.h.Admin.QueryRow(dbCtx, `SELECT count(*) FROM audit.access_event WHERE tenant_id = $1`, f.tenantA).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Fatalf("access events = %d, want 2", events)
	}

	// The same value in another tenant does not resolve.
	if _, err := f.svc.SearchByIdentifier(ctx, f.rc(f.tenantB), application.SearchInput{Type: domain.TypeTCKN, Value: tckn}); !errors.Is(err, application.ErrPersonNotFound) {
		t.Fatalf("cross-tenant search: %v", err)
	}
	// A checksum failure is a validation error, not a lookup.
	if _, err := f.svc.SearchByIdentifier(ctx, f.rc(f.tenantA), application.SearchInput{Type: domain.TypeTCKN, Value: "11111111111"}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("invalid TCKN: %v", err)
	}
	// An unknown catalog code is a validation error too.
	if _, err := f.svc.SearchByIdentifier(ctx, f.rc(f.tenantA), application.SearchInput{Type: "NOT_A_TYPE", Value: tckn}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("unknown type: %v", err)
	}
}

func TestUpdateHonoursIfMatchAndReplacesIdentifiers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first := f.tckn()
	person := f.create(t, f.tenantA, "Eski", "Ad", domain.SubmittedIdentifier{Type: domain.TypeTCKN, Value: first, Primary: true})

	name := "Yeni"
	if _, err := f.svc.Update(ctx, f.rc(f.tenantA), person.ID, domain.PersonPatch{FirstName: &name, ExpectedVersion: 99}); !errors.Is(err, application.ErrVersionMismatch) {
		t.Fatalf("stale If-Match: %v", err)
	}

	second := f.tckn()
	updated, err := f.svc.Update(ctx, f.rc(f.tenantA), person.ID, domain.PersonPatch{
		FirstName:       &name,
		Identifiers:     []domain.SubmittedIdentifier{{Type: domain.TypeTCKN, Value: second, Primary: true}},
		ExpectedVersion: person.RowVersion,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.RowVersion != person.RowVersion+1 || updated.FirstName != "Yeni" {
		t.Fatalf("updated = %+v", updated)
	}
	if len(updated.Identifiers) != 1 || updated.Identifiers[0].MaskedValue != second[:3]+"******"+second[9:] {
		t.Fatalf("identifiers = %+v", updated.Identifiers)
	}
	// The replaced value is free again, the new one is taken.
	reuse := f.create(t, f.tenantA, "Devir", "Alan", domain.SubmittedIdentifier{Type: domain.TypeTCKN, Value: first})
	if len(reuse.Identifiers) != 1 {
		t.Fatalf("the released identifier could not be reused: %+v", reuse.Identifiers)
	}

	// Removing by {type, remove} leaves no identifier behind.
	cleared, err := f.svc.Update(ctx, f.rc(f.tenantA), updated.ID, domain.PersonPatch{
		Identifiers:     []domain.SubmittedIdentifier{{Type: domain.TypeTCKN, Remove: true}},
		ExpectedVersion: updated.RowVersion,
	})
	if err != nil || len(cleared.Identifiers) != 0 {
		t.Fatalf("remove identifier: %+v err=%v", cleared.Identifiers, err)
	}

	// DECEASED is allowed, MERGED is not.
	deceased := "DECEASED"
	if _, err := f.svc.Update(ctx, f.rc(f.tenantA), cleared.ID, domain.PersonPatch{Status: &deceased, ExpectedVersion: cleared.RowVersion}); err != nil {
		t.Fatalf("status patch: %v", err)
	}
	merged := "MERGED"
	if _, err := f.svc.Update(ctx, f.rc(f.tenantA), cleared.ID, domain.PersonPatch{Status: &merged, ExpectedVersion: cleared.RowVersion + 1}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("MERGED through patch: %v", err)
	}
}

func TestOtherTenantCannotSeeOrPatchAPerson(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	person := f.create(t, f.tenantA, "Yalnız", "A")

	if _, err := f.svc.Get(ctx, f.rc(f.tenantB), person.ID); !errors.Is(err, application.ErrPersonNotFound) {
		t.Fatalf("other tenant get: %v", err)
	}
	name := "Sızıntı"
	if _, err := f.svc.Update(ctx, f.rc(f.tenantB), person.ID, domain.PersonPatch{FirstName: &name, ExpectedVersion: 1}); !errors.Is(err, application.ErrPersonNotFound) {
		t.Fatalf("other tenant update: %v", err)
	}
	if _, err := f.svc.Get(ctx, f.rc(f.tenantA), uuid.New()); !errors.Is(err, application.ErrPersonNotFound) {
		t.Fatal("unknown id must be not found")
	}
	page, err := f.svc.List(ctx, f.rc(f.tenantB), application.ListFilter{})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("other tenant list = %+v err=%v", page, err)
	}
}

func TestListPaginatesAndFilters(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const total = 120
	created := map[uuid.UUID]bool{}
	for i := 0; i < total; i++ {
		last := "Yılmaz"
		if i%10 == 0 {
			last = "Işık"
		}
		created[f.create(t, f.tenantA, fmt.Sprintf("Kişi%03d", i), last).ID] = true
	}

	seen := map[uuid.UUID]bool{}
	cursor, pages := "", 0
	for {
		page, err := f.svc.List(ctx, f.rc(f.tenantA), application.ListFilter{Cursor: cursor, Limit: 50})
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		pages++
		for _, it := range page.Items {
			if seen[it.ID] {
				t.Fatalf("duplicate %s on page %d", it.ID, pages)
			}
			seen[it.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if pages != 3 || len(seen) != total {
		t.Fatalf("pages=%d seen=%d, want 3/%d", pages, len(seen), total)
	}
	for id := range created {
		if !seen[id] {
			t.Fatalf("person %s never listed", id)
		}
	}

	// The name filter is folded the same way as normalized_name, so "IŞIK" finds "Işık".
	hits, err := f.svc.List(ctx, f.rc(f.tenantA), application.ListFilter{Query: "IŞIK", Limit: 200})
	if err != nil || len(hits.Items) != 12 {
		t.Fatalf("Turkish name filter = %d err=%v", len(hits.Items), err)
	}
	// Tokens may arrive in any order.
	if hits, err = f.svc.List(ctx, f.rc(f.tenantA), application.ListFilter{Query: "kişi001 yılmaz", Limit: 200}); err != nil || len(hits.Items) != 1 {
		t.Fatalf("two-token filter = %d err=%v", len(hits.Items), err)
	}
	if _, err := f.svc.List(ctx, f.rc(f.tenantA), application.ListFilter{Query: "x"}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("short query: %v", err)
	}
	if _, err := f.svc.List(ctx, f.rc(f.tenantA), application.ListFilter{Status: "GONE"}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("bad status: %v", err)
	}
	if _, err := f.svc.List(ctx, f.rc(f.tenantA), application.ListFilter{Cursor: "tampered"}); !errors.Is(err, httpx.ErrInvalidCursor) {
		t.Fatalf("bad cursor: %v", err)
	}
}

func TestCatalogsReturnTheProvisionedBaseline(t *testing.T) {
	f := newFixture(t)
	catalogs, err := f.svc.Catalogs(context.Background(), f.rc(f.tenantA))
	if err != nil {
		t.Fatalf("catalogs: %v", err)
	}
	if len(catalogs.IdentifierTypes) != 4 || len(catalogs.RelationshipTypes) != 6 || len(catalogs.MembershipTypes) != 8 {
		t.Fatalf("catalog sizes = %d/%d/%d", len(catalogs.IdentifierTypes), len(catalogs.RelationshipTypes), len(catalogs.MembershipTypes))
	}
	byCode := map[string]application.CatalogType{}
	for _, c := range catalogs.IdentifierTypes {
		byCode[c.Code] = c
	}
	if tckn := byCode[domain.TypeTCKN]; !tckn.IsSensitive || tckn.UniquenessScope != domain.ScopeTenant {
		t.Fatalf("TCKN = %+v", tckn)
	}
	if member := byCode[domain.TypeMemberNo]; member.UniquenessScope != domain.ScopeSponsor {
		t.Fatalf("MEMBER_NO = %+v", member)
	}
	family := application.CatalogType{}
	for _, c := range catalogs.MembershipTypes {
		if c.Code == "FAMILY" {
			family = c
		}
	}
	if !family.RequiresPrincipal {
		t.Fatalf("FAMILY = %+v", family)
	}
}

func date(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
