package memberimport_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	orgdomaintest "github.com/celikbros/kapsora/internal/organization/domain/domaintest"
	"github.com/celikbros/kapsora/internal/party/memberimport"
	"github.com/celikbros/kapsora/internal/platform/crypto/localkey"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// stagingTables are scanned by the "no plaintext anywhere" assertion.
var stagingTables = []string{"party.import_batch", "party.import_row", "party.person", "party.person_identifier"}

type fixture struct {
	h        *dbtest.Harness
	svc      *memberimport.Service
	logs     *bytes.Buffer
	rand     *rand.Rand
	tenant   uuid.UUID
	other    uuid.UUID
	sponsor  uuid.UUID
	actor    uuid.UUID
	reviewer uuid.UUID
	used     map[string]bool
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
	// Small limits so the tests exercise the queued path and the chunk boundary without
	// building files with thousands of rows.
	svc, err := memberimport.New(memberimport.Deps{
		Pool: h.App, Cipher: keys, Index: keys, Audit: auditpg.New(), Cursors: cursors,
		InlineRowLimit: 200, ChunkSize: 25,
	})
	if err != nil {
		t.Fatal(err)
	}

	logs := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	f := &fixture{
		h: h, svc: svc, logs: logs, rand: rand.New(rand.NewPCG(11, 17)),
		tenant: h.CreateTenant("IMP_A"), other: h.CreateTenant("IMP_B"),
		actor:    h.CreateActor("import-actor", "Import Actor"),
		reviewer: h.CreateActor("import-reviewer", "Import Reviewer"),
		used:     map[string]bool{},
	}
	f.seedCatalogs(f.tenant)
	f.seedCatalogs(f.other)
	f.sponsor = h.CreateTenantOrganization(f.tenant, "IMP Sponsor", "SPONSOR")
	return f
}

// seedCatalogs writes the baseline rows WP-I1-02 provisioning gives a new tenant.
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

func (f *fixture) rc(actor uuid.UUID) identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(), Principal: identity.Principal{ActorID: actor},
		StepUpValid: true,
		Permissions: map[string]struct{}{"import.execute": {}, "member.read": {}, "member.manage": {}},
	}
}

// tckn returns a fresh checksum-valid identity number.
func (f *fixture) tckn() string {
	for {
		v := orgdomaintest.GenerateTCKN(f.rand)
		if !f.used[v] {
			f.used[v] = true
			return v
		}
	}
}

// row is one line of a CSV_V1 file in canonical column order.
type row struct {
	recordID, first, middle, last, birth, sex string
	tckn, memberNo, employeeNo                string
	membership, principalNo, relationship     string
	validFrom, validTo, planCode              string
}

func (r row) line() string {
	return strings.Join([]string{
		r.recordID, r.first, r.middle, r.last, r.birth, r.sex, r.tckn, r.memberNo, r.employeeNo,
		r.membership, r.principalNo, r.relationship, r.validFrom, r.validTo, r.planCode,
	}, ";")
}

func file(rows ...row) []byte {
	var b strings.Builder
	b.WriteString(strings.Join(memberimport.Columns, ";"))
	b.WriteString("\n")
	for _, r := range rows {
		b.WriteString(r.line())
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// upload stages a file and returns the batch.
func (f *fixture) upload(t *testing.T, version string, content []byte) memberimport.Batch {
	t.Helper()
	batch, queued, err := f.svc.Upload(context.Background(), f.rc(f.actor), memberimport.UploadInput{
		SponsorOrganizationID: f.sponsor, SourceSystem: "HR", SourceVersion: version,
		FileName: "members.csv", Content: content,
	})
	if err != nil {
		t.Fatalf("upload %s: %v", version, err)
	}
	if queued {
		t.Fatalf("upload %s was queued; the fixture limit should keep it inline", version)
	}
	return batch
}

// apply runs the apply command and the worker job it enqueues, then returns the batch.
func (f *fixture) apply(t *testing.T, batch memberimport.Batch) memberimport.Batch {
	t.Helper()
	ctx := context.Background()
	queued, err := f.svc.Apply(ctx, f.rc(f.actor), batch.ID, batch.RowVersion)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := f.svc.RunApply(ctx, f.tenant, queued.ID); err != nil {
		t.Fatalf("run apply: %v", err)
	}
	out, err := f.svc.Get(ctx, f.rc(f.actor), batch.ID)
	if err != nil {
		t.Fatalf("get after apply: %v", err)
	}
	return out
}

// count returns a single-row count from the application pool.
func (f *fixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var n int
	if err := f.h.Admin.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// dump returns the text form of every row of a table, which is how the leak assertion
// looks inside bytea (hex), jsonb and text columns at once.
func (f *fixture) dump(t *testing.T, table string) string {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var out string
	err := f.h.Admin.QueryRow(ctx,
		fmt.Sprintf(`SELECT coalesce(string_agg(t::text, ' '), '') FROM %s t`, table)).Scan(&out)
	if err != nil {
		t.Fatalf("dump %s: %v", table, err)
	}
	return out
}

func TestUploadStagesRowsWithoutPlaintextIdentifiers(t *testing.T) {
	f := newFixture(t)
	principal := row{
		recordID: "R1", first: "Ayşe", last: "Yılmaz", birth: "1980-05-04", sex: "FEMALE",
		tckn: f.tckn(), memberNo: "M-1", membership: memberimport.RolePrincipal, validFrom: "2026-01-01",
	}
	dependant := row{
		recordID: "R2", first: "Deniz", last: "Yılmaz", birth: "2010-03-02", sex: "MALE",
		tckn: f.tckn(), memberNo: "M-2", membership: memberimport.RoleDependant, principalNo: "M-1",
		relationship: "CHILD", validFrom: "2026-01-01",
	}
	batch := f.upload(t, "v1", file(principal, dependant))

	if batch.RowCount != 2 || batch.Counters.Valid != 2 {
		t.Fatalf("batch = %+v", batch)
	}
	if batch.Status != memberimport.StatusReady {
		t.Fatalf("status = %s, want %s", batch.Status, memberimport.StatusReady)
	}

	rows, err := f.svc.ListRows(context.Background(), f.rc(f.actor), batch.ID, memberimport.RowFilter{})
	if err != nil {
		t.Fatalf("list rows: %v", err)
	}
	if len(rows.Items) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows.Items))
	}
	for _, r := range rows.Items {
		if r.Status != memberimport.RowValid || r.Decision == nil || *r.Decision != memberimport.DecisionCreate {
			t.Fatalf("row %d = %+v", r.RowNo, r)
		}
		if len(r.Identifiers) == 0 {
			t.Fatalf("row %d has no masked identifier", r.RowNo)
		}
		for _, id := range r.Identifiers {
			if !strings.Contains(id.MaskedValue, "*") {
				t.Fatalf("identifier %s of row %d is not masked: %s", id.Type, r.RowNo, id.MaskedValue)
			}
		}
	}

	// The plaintext numbers must not be readable anywhere: not in the staging payload,
	// not in the person tables, not in the audit trail and not in the logs.
	haystack := f.logs.String() + f.dump(t, "audit.event")
	for _, table := range stagingTables {
		haystack += f.dump(t, table)
	}
	for _, secret := range []string{principal.tckn, dependant.tckn} {
		if strings.Contains(haystack, secret) {
			t.Fatalf("plaintext identifier leaked into storage, audit or logs")
		}
	}
}

func TestApplyIsIdempotentAndLinksTheDependant(t *testing.T) {
	f := newFixture(t)
	principalTCKN, dependantTCKN := f.tckn(), f.tckn()
	content := file(
		row{recordID: "R1", first: "Ayşe", last: "Yılmaz", birth: "1980-05-04", sex: "FEMALE",
			tckn: principalTCKN, memberNo: "M-1", membership: memberimport.RolePrincipal, validFrom: "2026-01-01"},
		row{recordID: "R2", first: "Deniz", last: "Yılmaz", birth: "2010-03-02", sex: "MALE",
			tckn: dependantTCKN, memberNo: "M-2", membership: memberimport.RoleDependant, principalNo: "M-1",
			relationship: "CHILD", validFrom: "2026-01-01"},
	)
	batch := f.apply(t, f.upload(t, "v1", content))

	if batch.Status != memberimport.StatusApplied {
		t.Fatalf("status = %s, want %s", batch.Status, memberimport.StatusApplied)
	}
	if batch.Counters.Created != 2 {
		t.Fatalf("created = %d, want 2", batch.Counters.Created)
	}
	if n := f.count(t, `SELECT count(*) FROM party.person WHERE tenant_id = $1`, f.tenant); n != 2 {
		t.Fatalf("persons = %d, want 2", n)
	}
	if n := f.count(t, `SELECT count(*) FROM party.sponsor_membership WHERE tenant_id = $1`, f.tenant); n != 2 {
		t.Fatalf("memberships = %d, want 2", n)
	}
	// The dependant's membership points at the principal's, and the family link exists.
	// Member numbers are stored normalised, so the file's "M-1" is "M1" in the registry.
	if n := f.count(t, `
		SELECT count(*) FROM party.sponsor_membership d
		  JOIN party.sponsor_membership p ON p.tenant_id = d.tenant_id AND p.id = d.principal_membership_id
		 WHERE d.tenant_id = $1 AND d.external_member_no = 'M2' AND p.external_member_no = 'M1'`,
		f.tenant); n != 1 {
		t.Fatalf("dependant not linked to the principal (%d)", n)
	}
	if n := f.count(t, `SELECT count(*) FROM party.person_relationship WHERE tenant_id = $1`, f.tenant); n != 1 {
		t.Fatalf("relationships = %d, want 1", n)
	}

	// Re-running the apply of an APPLIED batch changes nothing.
	if err := f.svc.RunApply(context.Background(), f.tenant, batch.ID); err != nil {
		t.Fatalf("second run apply: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM party.person WHERE tenant_id = $1`, f.tenant); n != 2 {
		t.Fatalf("persons after replay = %d, want 2", n)
	}

	// Re-uploading the same bytes under the same source version is refused.
	_, _, err := f.svc.Upload(context.Background(), f.rc(f.actor), memberimport.UploadInput{
		SponsorOrganizationID: f.sponsor, SourceSystem: "HR", SourceVersion: "v1",
		FileName: "members.csv", Content: content,
	})
	if err == nil || !strings.Contains(err.Error(), "already uploaded") {
		t.Fatalf("duplicate upload error = %v", err)
	}
}

func TestSecondVersionUpdatesInsteadOfDuplicating(t *testing.T) {
	f := newFixture(t)
	first := f.tckn()
	v1 := file(row{recordID: "R1", first: "Ayşe", last: "Yılmaz", birth: "1980-05-04", sex: "FEMALE",
		tckn: first, memberNo: "M-1", membership: memberimport.RolePrincipal, validFrom: "2026-01-01"})
	f.apply(t, f.upload(t, "v1", v1))

	// Same person (same TCKN and member number), new surname.
	v2 := file(row{recordID: "R1", first: "Ayşe", last: "Kaya", birth: "1980-05-04", sex: "FEMALE",
		tckn: first, memberNo: "M-1", membership: memberimport.RolePrincipal, validFrom: "2026-01-01"})
	staged := f.upload(t, "v2", v2)
	rows, err := f.svc.ListRows(context.Background(), f.rc(f.actor), staged.ID, memberimport.RowFilter{})
	if err != nil {
		t.Fatalf("list rows: %v", err)
	}
	if len(rows.Items) != 1 || rows.Items[0].Status != memberimport.RowMatched {
		t.Fatalf("row = %+v", rows.Items)
	}
	if d := rows.Items[0].Decision; d == nil || *d != memberimport.DecisionUpdate {
		t.Fatalf("decision = %v, want UPDATE", rows.Items[0].Decision)
	}

	applied := f.apply(t, staged)
	if applied.Counters.Updated != 1 || applied.Counters.Created != 0 {
		t.Fatalf("counters = %+v", applied.Counters)
	}
	if n := f.count(t, `SELECT count(*) FROM party.person WHERE tenant_id = $1`, f.tenant); n != 1 {
		t.Fatalf("persons = %d, want 1", n)
	}
	var last string
	ctx, cancel := f.h.Ctx()
	defer cancel()
	if err := f.h.Admin.QueryRow(ctx,
		`SELECT last_name FROM party.person WHERE tenant_id = $1`, f.tenant).Scan(&last); err != nil {
		t.Fatalf("read person: %v", err)
	}
	if last != "Kaya" {
		t.Fatalf("last name = %s, want Kaya", last)
	}
}

func TestInvalidRowGoesToReviewAndCanBeSkipped(t *testing.T) {
	f := newFixture(t)
	good := f.tckn()
	content := file(
		row{recordID: "R1", first: "Ayşe", last: "Yılmaz", birth: "1980-05-04", sex: "FEMALE",
			tckn: good, memberNo: "M-1", membership: memberimport.RolePrincipal, validFrom: "2026-01-01"},
		// A TCKN with a broken checksum cannot be staged as a member.
		row{recordID: "R2", first: "Bozuk", last: "Kayıt", birth: "1990-01-01", sex: "MALE",
			tckn: "12345678902", memberNo: "M-2", membership: memberimport.RolePrincipal, validFrom: "2026-01-01"},
	)
	batch := f.upload(t, "v1", content)
	if batch.Status != memberimport.StatusReview {
		t.Fatalf("status = %s, want %s", batch.Status, memberimport.StatusReview)
	}
	if batch.Counters.Invalid != 1 || batch.Counters.Valid != 1 {
		t.Fatalf("counters = %+v", batch.Counters)
	}

	rows, err := f.svc.ListRows(context.Background(), f.rc(f.actor), batch.ID,
		memberimport.RowFilter{Status: memberimport.RowInvalid})
	if err != nil {
		t.Fatalf("list invalid rows: %v", err)
	}
	if len(rows.Items) != 1 || len(rows.Items[0].Errors) == 0 {
		t.Fatalf("invalid rows = %+v", rows.Items)
	}

	bad := rows.Items[0]
	reviewed, err := f.svc.Review(context.Background(), f.rc(f.reviewer), batch.ID, bad.ID, memberimport.ReviewInput{
		Decision: memberimport.DecisionSkip, ExpectedVersion: bad.RowVersion,
	})
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if reviewed.Decision == nil || *reviewed.Decision != memberimport.DecisionSkip {
		t.Fatalf("decision = %v", reviewed.Decision)
	}

	// A stale If-Match is refused.
	if _, err := f.svc.Review(context.Background(), f.rc(f.reviewer), batch.ID, bad.ID, memberimport.ReviewInput{
		Decision: memberimport.DecisionSkip, ExpectedVersion: bad.RowVersion,
	}); err == nil {
		t.Fatal("stale review was accepted")
	}

	current, err := f.svc.Get(context.Background(), f.rc(f.actor), batch.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	applied := f.apply(t, current)
	if applied.Counters.Created != 1 || applied.Counters.Skipped != 1 {
		t.Fatalf("counters = %+v", applied.Counters)
	}
	if n := f.count(t, `SELECT count(*) FROM party.person WHERE tenant_id = $1`, f.tenant); n != 1 {
		t.Fatalf("persons = %d, want 1", n)
	}
}

func TestBatchesAndRowsAreInvisibleToOtherTenants(t *testing.T) {
	f := newFixture(t)
	batch := f.upload(t, "v1", file(row{
		recordID: "R1", first: "Ayşe", last: "Yılmaz", birth: "1980-05-04", sex: "FEMALE",
		tckn: f.tckn(), memberNo: "M-1", membership: memberimport.RolePrincipal, validFrom: "2026-01-01",
	}))

	otherRC := identity.RequestContext{
		TenantID: f.other, MembershipID: uuid.New(), Principal: identity.Principal{ActorID: f.actor},
		StepUpValid: true, Permissions: map[string]struct{}{"import.execute": {}},
	}
	if _, err := f.svc.Get(context.Background(), otherRC, batch.ID); err == nil {
		t.Fatal("another tenant could read the batch")
	}
	page, err := f.svc.List(context.Background(), otherRC, memberimport.ListFilter{})
	if err != nil {
		t.Fatalf("list for the other tenant: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("other tenant sees %d batches", len(page.Items))
	}
}

func TestLargeFileIsQueuedForTheWorker(t *testing.T) {
	f := newFixture(t)
	rows := make([]row, 0, 250)
	for i := 0; i < 250; i++ {
		rows = append(rows, row{
			recordID: fmt.Sprintf("R%d", i), first: "Ad", last: fmt.Sprintf("Soyad%d", i),
			birth: "1990-01-01", sex: "FEMALE", tckn: f.tckn(),
			memberNo: fmt.Sprintf("M-%d", i), membership: memberimport.RolePrincipal, validFrom: "2026-01-01",
		})
	}
	batch, queued, err := f.svc.Upload(context.Background(), f.rc(f.actor), memberimport.UploadInput{
		SponsorOrganizationID: f.sponsor, SourceSystem: "HR", SourceVersion: "big",
		FileName: "big.csv", Content: file(rows...),
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if !queued {
		t.Fatal("a file above the inline limit should be queued")
	}
	if batch.RowCount != 250 {
		t.Fatalf("row count = %d, want 250", batch.RowCount)
	}

	// The worker validates it; then the apply writes every row in chunks.
	if err := f.svc.RunValidation(context.Background(), f.tenant, batch.ID); err != nil {
		t.Fatalf("run validation: %v", err)
	}
	current, err := f.svc.Get(context.Background(), f.rc(f.actor), batch.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if current.Counters.Valid != 250 {
		t.Fatalf("valid = %d, want 250", current.Counters.Valid)
	}
	applied := f.apply(t, current)
	if applied.Counters.Created != 250 {
		t.Fatalf("created = %d, want 250", applied.Counters.Created)
	}
	if n := f.count(t, `SELECT count(*) FROM party.person WHERE tenant_id = $1`, f.tenant); n != 250 {
		t.Fatalf("persons = %d, want 250", n)
	}
}
