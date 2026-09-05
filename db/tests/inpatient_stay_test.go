package dbtests

import (
	"strings"
	"testing"

	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// TestInpatientStayPermissionsAreSeededAndGrantable is the two-halves test this package owes:
// the catalogue rows of migration 000008 and the role templates in
// internal/identity/application/roles.go have to agree, because a permission that exists in
// one and not the other is a permission nobody can hold or one nobody can be given.
//
// WP-I5-03 adds no permission, and the assertion is that this is actually true rather than
// merely intended. `health.case.manage` covers asking for an admission, extending it,
// recording its segments and discharging it; the one decision that is not the provider's —
// whether the plan pays for the admission — is the request's own `service_request.review`,
// given on the request page. The pairing is what makes the package's central claim work: a
// role that held both would be a hospital approving its own admissions.
func TestInpatientStayPermissionsAreSeededAndGrantable(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	granted := map[string][]string{}
	byRole := map[string]map[string]bool{}
	for _, tpl := range identityapp.RoleTemplates() {
		byRole[tpl.Code] = map[string]bool{}
		for _, code := range tpl.Permissions {
			granted[code] = append(granted[code], tpl.Code)
			byRole[tpl.Code][code] = true
		}
	}

	for _, code := range []string{"health.case.manage", "service_request.review"} {
		var n int
		if err := h.Admin.QueryRow(ctx,
			`SELECT count(*) FROM iam.permission WHERE code = $1`, code).Scan(&n); err != nil {
			t.Fatalf("read permission %s: %v", code, err)
		}
		if n != 1 {
			t.Fatalf("permission %s is seeded %d times, want once", code, n)
		}
		if len(granted[code]) == 0 {
			t.Errorf("permission %s is in the catalogue but in no role template: nobody can hold it", code)
		}
	}

	// No permission of this package's own. A grant named after the stay would mean the
	// catalogue and the templates had drifted apart from the work package's section 2.5,
	// which says plainly that there is none.
	for _, code := range []string{
		"inpatient_stay.manage", "inpatient.stay.manage", "health.stay.manage",
		"health.inpatient.manage",
	} {
		var n int
		if err := h.Admin.QueryRow(ctx,
			`SELECT count(*) FROM iam.permission WHERE code = $1`, code).Scan(&n); err != nil {
			t.Fatalf("read permission %s: %v", code, err)
		}
		if n != 0 {
			t.Errorf("permission %s exists; WP-I5-03 adds no permission of its own", code)
		}
	}

	for _, want := range []struct{ role, permission string }{
		// The hospital clerk asks for the admission and records what happened.
		{"PROVIDER_STAFF", "health.case.manage"},
		{"PROVIDER_STAFF", "service_request.create"},
		{"PROVIDER_STAFF", "service_request.submit"},
		// The medical reviewer decides it, on the request page.
		{"MEDICAL_REVIEWER", "service_request.review"},
		{"MEDICAL_REVIEWER", "authorization.manage"},
	} {
		if !byRole[want.role][want.permission] {
			t.Errorf("role %s does not hold %s", want.role, want.permission)
		}
	}

	// Asking for an admission and deciding one are two jobs, so no role does both.
	for _, tpl := range identityapp.RoleTemplates() {
		if byRole[tpl.Code]["health.case.manage"] && byRole[tpl.Code]["service_request.review"] {
			t.Errorf("role %s both asks for admissions and decides them", tpl.Code)
		}
	}
	// And the sponsor's HR user manages nothing clinical, in either direction.
	for _, permission := range []string{"health.case.manage", "service_request.review"} {
		if byRole["SPONSOR_HR"][permission] {
			t.Errorf("SPONSOR_HR holds %s", permission)
		}
	}
}

// TestInpatientStayIndexesAndConstraintsExist checks that the three rules the work package
// exists for are in the schema rather than only in the service. Each of them is proved
// against real rows elsewhere; this is the cheap assertion that the object is still there
// under the name the repository maps its violation from, because a constraint renamed in a
// later migration would turn a 409 into a 500 without any test noticing.
func TestInpatientStayIndexesAndConstraintsExist(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	// The two partial unique indexes: the rule is in the WHERE clause, so an index without
	// one would be a different rule wearing the same name.
	for _, name := range []string{"uq_inpatient_stay_open", "uq_stay_extension_pending"} {
		var definition string
		if err := h.Admin.QueryRow(ctx, `
			SELECT indexdef FROM pg_indexes WHERE schemaname = 'health' AND indexname = $1`,
			name).Scan(&definition); err != nil {
			t.Fatalf("read index %s: %v", name, err)
		}
		if !strings.Contains(definition, "WHERE") || !strings.Contains(definition, "UNIQUE") {
			t.Errorf("%s = %q, want a partial unique index", name, definition)
		}
	}
	if !strings.Contains(indexDef(t, h, "uq_inpatient_stay_open"), "provider_organization_id") {
		t.Error("uq_inpatient_stay_open does not name the provider: the rule is one open stay " +
			"per case AND provider, not one per case")
	}

	var definition string
	if err := h.Admin.QueryRow(ctx, `
		SELECT pg_get_constraintdef(c.oid)
		  FROM pg_constraint c
		  JOIN pg_class t ON t.oid = c.conrelid
		  JOIN pg_namespace n ON n.oid = t.relnamespace
		 WHERE n.nspname = 'health' AND t.relname = 'stay_segment'
		   AND c.conname = 'ex_stay_segment_overlap'`).Scan(&definition); err != nil {
		t.Fatalf("read the segment exclusion constraint: %v", err)
	}
	if !containsAll(definition, "EXCLUDE USING gist", "tstzrange", "COMPANION") {
		t.Errorf("ex_stay_segment_overlap = %q, want a gist exclusion over tstzrange that "+
			"leaves COMPANION out", definition)
	}
}

// indexDef reads one index definition back out of the catalogue.
func indexDef(t *testing.T, h *dbtest.Harness, name string) string {
	t.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	var definition string
	if err := h.Admin.QueryRow(ctx, `
		SELECT indexdef FROM pg_indexes WHERE schemaname = 'health' AND indexname = $1`,
		name).Scan(&definition); err != nil {
		t.Fatalf("read index %s: %v", name, err)
	}
	return definition
}

func containsAll(s string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}
