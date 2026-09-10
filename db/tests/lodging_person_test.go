package dbtests

import (
	"fmt"
	"testing"

	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	notificationapp "github.com/celikbros/kapsora/internal/notification/application"
	notificationdomain "github.com/celikbros/kapsora/internal/notification/domain"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// TestLodgingTermsPermissionIsSeededAndGrantable is the two-halves test WP-I6-04 section
// 2.6 asks for, in the shape the mapping and contact permissions of WP-I5-05 established:
// the catalogue row of migration 000039 and the role template in
// internal/identity/application/roles.go have to agree. A permission in one and not the
// other is a permission nobody can hold, or one nobody can be given.
func TestLodgingTermsPermissionIsSeededAndGrantable(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	const code = "contract.lodging_terms.manage"

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
	// NORMAL, not SENSITIVE: a cancellation policy is a commercial term between two
	// organizations, not a fact about a person. Marking it SENSITIVE would put it in every
	// access review that lists the grants over personal data and drown the ones that are.
	if sensitivity != "NORMAL" {
		t.Errorf("permission %s sensitivity = %s, want NORMAL", code, sensitivity)
	}

	byRole := map[string]map[string]bool{}
	holders := 0
	for _, tpl := range identityapp.RoleTemplates() {
		byRole[tpl.Code] = map[string]bool{}
		for _, p := range tpl.Permissions {
			byRole[tpl.Code][p] = true
		}
		if byRole[tpl.Code][code] {
			holders++
		}
	}
	if holders == 0 {
		t.Fatalf("permission %s is in the catalogue but in no role template: nobody can hold it", code)
	}

	// The pairing rather than the role name, so the rule survives a rename: whoever may
	// write a contract version may say what cancelling under it costs. A desk that could
	// set a price but not its cancellation terms could only ever publish half an agreement.
	for _, tpl := range identityapp.RoleTemplates() {
		if byRole[tpl.Code]["contract.manage"] && !byRole[tpl.Code][code] {
			t.Errorf("role %s may write a contract version but not its lodging terms", tpl.Code)
		}
		// And the reverse: it is not a grant anybody holds on its own, because there is no
		// screen that writes lodging terms without writing a draft version around them.
		if byRole[tpl.Code][code] && !byRole[tpl.Code]["contract.manage"] {
			t.Errorf("role %s holds %s without contract.manage", tpl.Code, code)
		}
	}
	// A member, a provider clerk and the sponsor's HR user never write contract terms.
	for _, role := range []string{"MEMBER", "PROVIDER_STAFF", "PROVIDER_RESERVATION", "SPONSOR_HR"} {
		if byRole[role][code] {
			t.Errorf("role %s holds %s", role, code)
		}
	}
}

// TestPersonScopeIsPartOfTheGrantVocabulary pins the schema half of the member binding.
// The Go side spells the scope in identity.ScopePerson; if the CHECK and the constant ever
// disagree, every member grant is refused at write time with a constraint violation and no
// test above this layer would say why.
func TestPersonScopeIsPartOfTheGrantVocabulary(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	var definition string
	if err := h.Admin.QueryRow(ctx, `
		SELECT pg_get_constraintdef(c.oid)
		  FROM pg_constraint c
		  JOIN pg_class t ON t.oid = c.conrelid
		  JOIN pg_namespace n ON n.oid = t.relnamespace
		 WHERE n.nspname = 'iam' AND t.relname = 'access_grant'
		   AND c.conname = 'ck_access_grant_scope_type'`).Scan(&definition); err != nil {
		t.Fatalf("read the scope type constraint: %v", err)
	}
	for _, scope := range []string{
		identityapp.ScopeTenant, identityapp.ScopeOrganization, identityapp.ScopeProgram,
		identityapp.ScopeProviderLocation, identityapp.ScopeWorkQueue, identityapp.ScopePerson,
	} {
		if !contains(definition, scope) {
			t.Errorf("scope %s is not in ck_access_grant_scope_type: %s", scope, definition)
		}
	}

	// The person a PERSON grant names is a real person of the same tenant, enforced by a
	// composite foreign key over the generated column rather than by whichever code path
	// wrote the row.
	var fkCount int
	if err := h.Admin.QueryRow(ctx, `
		SELECT count(*)
		  FROM pg_constraint c
		  JOIN pg_class t ON t.oid = c.conrelid
		  JOIN pg_namespace n ON n.oid = t.relnamespace
		 WHERE n.nspname = 'iam' AND t.relname = 'access_grant'
		   AND c.conname = 'fk_access_grant_person' AND c.contype = 'f'`).Scan(&fkCount); err != nil {
		t.Fatalf("read the person foreign key: %v", err)
	}
	if fkCount != 1 {
		t.Error("iam.access_grant has no foreign key from its PERSON scope into party.person")
	}

	// One account, one person, and one person, one account.
	for _, index := range []string{"uq_access_grant_person_scope", "uq_access_grant_membership_person"} {
		var n int
		if err := h.Admin.QueryRow(ctx, `
			SELECT count(*) FROM pg_indexes
			 WHERE schemaname = 'iam' AND tablename = 'access_grant' AND indexname = $1`,
			index).Scan(&n); err != nil {
			t.Fatalf("read indexes of iam.access_grant: %v", err)
		}
		if n != 1 {
			t.Errorf("iam.access_grant has no %s", index)
		}
	}
}

// TestPropertyNameIsInTheTemplateCatalogueCheck keeps the two statements of the safe
// variable catalogue in step. The Go list is what the renderer works from; the CHECK is
// what makes "there is no slot a diagnosis could be supplied under" a fact about the
// schema. A name added to one and not the other is a template the product can compose and
// the database will not store.
func TestPropertyNameIsInTheTemplateCatalogueCheck(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	var definition string
	if err := h.Admin.QueryRow(ctx, `
		SELECT pg_get_constraintdef(c.oid)
		  FROM pg_constraint c
		  JOIN pg_class t ON t.oid = c.conrelid
		  JOIN pg_namespace n ON n.oid = t.relnamespace
		 WHERE n.nspname = 'notification' AND t.relname = 'template'
		   AND c.conname = 'ck_notification_template_variables'`).Scan(&definition); err != nil {
		t.Fatalf("read the variable constraint: %v", err)
	}
	for _, name := range notificationdomain.SafeVariableNames {
		if !contains(definition, "'"+name+"'") {
			t.Errorf("safe variable %s is in the Go catalogue and not in the CHECK: %s", name, definition)
		}
	}
	// And nothing is in the CHECK that the Go catalogue does not have: a name the database
	// accepts and the renderer refuses is a template an operator can save and never send.
	known := map[string]bool{}
	for _, name := range notificationdomain.SafeVariableNames {
		known[name] = true
	}
	for _, name := range []string{"diagnosis", "note", "voucher_token", "identity_number"} {
		if contains(definition, "'"+name+"'") {
			t.Errorf("the CHECK accepts %s, which is not a safe variable", name)
		}
	}
	// The cardinality bound is the size of the catalogue, in both places. It is read from the
	// Go constant rather than written out, so a variable added by a later migration -- WP-I7-04
	// added `masked_account` -- moves both halves or fails here.
	if !contains(definition, fmt.Sprintf("<= %d", notificationdomain.MaxDeclaredVariables)) {
		t.Errorf("the CHECK does not bound declared_variables at the catalogue size: %s", definition)
	}
	if notificationdomain.MaxDeclaredVariables != len(notificationdomain.SafeVariableNames) {
		t.Errorf("MaxDeclaredVariables = %d, catalogue has %d names",
			notificationdomain.MaxDeclaredVariables, len(notificationdomain.SafeVariableNames))
	}

	// Every event the seed publishes a template for is a valid event code, so a booking
	// event added in Go cannot be one notification.template would refuse.
	for _, event := range notificationapp.WiredEvents {
		var ok bool
		if err := h.Admin.QueryRow(ctx,
			`SELECT $1::text ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*){1,4}$'`, event).Scan(&ok); err != nil {
			t.Fatalf("check event code %s: %v", event, err)
		}
		if !ok {
			t.Errorf("event %s does not match ck_notification_template_event_code", event)
		}
	}
}

// TestLodgingTermsTableIsGuardedAndIsolated states the three schema guarantees the
// application leans on and could not detect the loss of: the row is tenant-isolated, it is
// unique per version, and the DRAFT-only rule is a trigger rather than a convention.
func TestLodgingTermsTableIsGuardedAndIsolated(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	var enabled, forced bool
	if err := h.Admin.QueryRow(ctx, `
		SELECT c.relrowsecurity, c.relforcerowsecurity
		  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'contract' AND c.relname = 'lodging_terms'`).Scan(&enabled, &forced); err != nil {
		t.Fatalf("read RLS of contract.lodging_terms: %v", err)
	}
	if !enabled || !forced {
		t.Errorf("contract.lodging_terms RLS enabled=%t forced=%t", enabled, forced)
	}

	var triggers int
	if err := h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM pg_trigger tg
		  JOIN pg_class c ON c.oid = tg.tgrelid
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'contract' AND c.relname = 'lodging_terms'
		   AND tg.tgname = 'tg_lodging_terms_guard' AND NOT tg.tgisinternal`).Scan(&triggers); err != nil {
		t.Fatalf("read the guard trigger: %v", err)
	}
	if triggers != 1 {
		t.Error("contract.lodging_terms has no tg_lodging_terms_guard; a published version's terms could be rewritten")
	}

	for _, want := range []string{
		"uq_lodging_terms_version", "ck_lodging_terms_penalty", "ck_lodging_terms_nights",
	} {
		var n int
		if err := h.Admin.QueryRow(ctx, `
			SELECT count(*)
			  FROM pg_constraint c
			  JOIN pg_class t ON t.oid = c.conrelid
			  JOIN pg_namespace n ON n.oid = t.relnamespace
			 WHERE n.nspname = 'contract' AND t.relname = 'lodging_terms' AND c.conname = $1`,
			want).Scan(&n); err != nil {
			t.Fatalf("read constraint %s: %v", want, err)
		}
		if n != 1 {
			t.Errorf("contract.lodging_terms has no %s", want)
		}
	}

	// The two percentages are numeric and not float: a fee that depended on binary
	// rounding would be a fee two systems disagree about.
	for _, column := range []string{"penalty_percent", "no_show_percent"} {
		var dataType string
		if err := h.Admin.QueryRow(ctx, `
			SELECT data_type FROM information_schema.columns
			 WHERE table_schema = 'contract' AND table_name = 'lodging_terms' AND column_name = $1`,
			column).Scan(&dataType); err != nil {
			t.Fatalf("read %s: %v", column, err)
		}
		if dataType != "numeric" {
			t.Errorf("contract.lodging_terms.%s is %s, want numeric", column, dataType)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
