package dbtests

import (
	"testing"

	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// TestMappingAndContactPermissionsAreSeededAndGrantable checks both halves of the same
// fact, the way WP-I5-01's test does: the catalogue row of migration 000035 and the role
// template in internal/identity/application/roles.go have to agree, because a permission
// that exists in one and not the other is a permission nobody can hold or one nobody can
// be given.
//
// The sensitivity is asserted too. An e-mail address and a telephone number are how a
// person is reached and how a person is correlated across systems; a contact grant that
// quietly became NORMAL would drop out of every review that lists the sensitive ones.
func TestMappingAndContactPermissionsAreSeededAndGrantable(t *testing.T) {
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

	for code, wantSensitivity := range map[string]string{
		"entitlement.mapping.manage": "NORMAL",
		"member.contact.read":        "SENSITIVE",
		"member.contact.manage":      "SENSITIVE",
	} {
		var n int
		if err := h.Admin.QueryRow(ctx,
			`SELECT count(*) FROM iam.permission WHERE code = $1`, code).Scan(&n); err != nil {
			t.Fatalf("read permission %s: %v", code, err)
		}
		if n != 1 {
			t.Fatalf("permission %s is seeded %d times, want once", code, n)
		}
		var sensitivity string
		if err := h.Admin.QueryRow(ctx,
			`SELECT sensitivity FROM iam.permission WHERE code = $1`, code).Scan(&sensitivity); err != nil {
			t.Fatalf("read sensitivity of %s: %v", code, err)
		}
		if sensitivity != wantSensitivity {
			t.Errorf("permission %s sensitivity = %s, want %s", code, sensitivity, wantSensitivity)
		}
		if len(granted[code]) == 0 {
			t.Errorf("permission %s is in the catalogue but in no role template: nobody can hold it", code)
		}
	}

	// The mapping is part of a plan's configuration rather than a thing of its own, so
	// whoever may write the entitlements may say which service draws from them. Asserting
	// the pairing rather than the role keeps the rule true if the templates are renamed.
	for _, tpl := range identityapp.RoleTemplates() {
		if byRole[tpl.Code]["plan.manage"] && !byRole[tpl.Code]["entitlement.mapping.manage"] {
			t.Errorf("role %s may write a plan version but not its service mapping", tpl.Code)
		}
	}
	for _, want := range []struct{ role, permission string }{
		{"PROGRAM_MANAGER", "entitlement.mapping.manage"},
		{"PROGRAM_MANAGER", "member.contact.read"},
		{"PROGRAM_MANAGER", "member.contact.manage"},
	} {
		if !byRole[want.role][want.permission] {
			t.Errorf("role %s does not hold %s", want.role, want.permission)
		}
	}
	// A provider sees a member's masked identifier and may not see how to reach them: the
	// address book is the tenant's, not the provider's.
	for _, role := range []string{"PROVIDER_STAFF", "PROVIDER_ADMIN", "SPONSOR_HR"} {
		for _, permission := range []string{"member.contact.read", "member.contact.manage"} {
			if byRole[role][permission] {
				t.Errorf("role %s holds %s", role, permission)
			}
		}
	}
}

// TestServiceEntitlementMappingAndContactShape pins the two things about the new tables
// that the application layer relies on and could not detect the loss of: a service is
// mapped at most once inside one plan version, and a person has at most one primary
// contact per channel.
func TestServiceEntitlementMappingAndContactShape(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	for _, want := range []struct{ table, index string }{
		{"benefit.service_entitlement_mapping", "uq_service_entitlement_mapping"},
		{"party.person_contact", "uq_person_contact_primary"},
	} {
		var n int
		if err := h.Admin.QueryRow(ctx, `
			SELECT count(*) FROM pg_indexes
			 WHERE schemaname || '.' || tablename = $1 AND indexname = $2`,
			want.table, want.index).Scan(&n); err != nil {
			t.Fatalf("read indexes of %s: %v", want.table, err)
		}
		if n != 1 {
			t.Errorf("%s has no %s", want.table, want.index)
		}
	}

	// The factor is a numeric, not a float: a balance that depended on binary rounding
	// would be a balance nobody could reconcile.
	var dataType string
	if err := h.Admin.QueryRow(ctx, `
		SELECT data_type FROM information_schema.columns
		 WHERE table_schema = 'benefit' AND table_name = 'service_entitlement_mapping'
		   AND column_name = 'unit_factor'`).Scan(&dataType); err != nil {
		t.Fatalf("read unit_factor: %v", err)
	}
	if dataType != "numeric" {
		t.Fatalf("unit_factor is %s, want numeric", dataType)
	}

	// party.person_contact holds exactly one binary column, and it is the envelope. A
	// second one would be a second place a plaintext address could live.
	rows, err := h.Admin.Query(ctx, `
		SELECT column_name FROM information_schema.columns
		 WHERE table_schema = 'party' AND table_name = 'person_contact' AND data_type = 'bytea'`)
	if err != nil {
		t.Fatalf("read columns: %v", err)
	}
	defer rows.Close()
	var binary []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		binary = append(binary, name)
	}
	if len(binary) != 1 || binary[0] != "value_enc" {
		t.Fatalf("binary columns of party.person_contact = %v, want [value_enc]", binary)
	}
}
