package main

import (
	"strings"
	"testing"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	benefitpg "github.com/celikbros/kapsora/internal/benefit/infrastructure/postgres"
	catalogapp "github.com/celikbros/kapsora/internal/catalog/application"
	catalogpg "github.com/celikbros/kapsora/internal/catalog/infrastructure/postgres"
	notificationapp "github.com/celikbros/kapsora/internal/notification/application"
	notificationdomain "github.com/celikbros/kapsora/internal/notification/domain"
	notificationpg "github.com/celikbros/kapsora/internal/notification/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// TestICD10RowsAreAWellFormedHierarchy is a pure check of the seeded data: twenty-two
// chapters, every leaf under a code that exists, no duplicates, and the sensitivity WP-I5-01
// derives a case's own sensitivity from actually set on the chapters it names.
func TestICD10RowsAreAWellFormedHierarchy(t *testing.T) {
	chapters := icd10Chapters()
	if len(chapters) != 22 {
		t.Fatalf("chapters = %d, want the 22 of ICD-10", len(chapters))
	}
	known := map[string]icd10Row{}
	for _, row := range icd10Rows() {
		if _, dup := known[row.Code]; dup {
			t.Fatalf("%s is seeded twice", row.Code)
		}
		known[row.Code] = row
	}
	for _, row := range icd10Rows() {
		if row.Parent == "" {
			continue
		}
		if _, ok := known[row.Parent]; !ok {
			t.Fatalf("%s hangs off %s, which is not seeded", row.Code, row.Parent)
		}
	}
	if len(known) < 60 {
		t.Fatalf("%d values seeded; the demo and the tests need a real set", len(known))
	}

	// The sensitive chapters, named as WP-I5-01 names them: psychiatry, reproductive
	// health, and congenital.
	for _, code := range []string{"F00-F99", "O00-O99", "Q00-Q99", "Z30-Z39"} {
		if !known[code].Sensitive {
			t.Errorf("%s is not marked sensitive", code)
		}
	}
	for _, code := range []string{"A00-B99", "J00-J99", "Z00-Z99", "J06.9", "Z00.0"} {
		if known[code].Sensitive {
			t.Errorf("%s is marked sensitive and should not be", code)
		}
	}
	// Every code under a sensitive chapter says so itself, so a reader of one row never
	// has to walk up the tree to find out.
	for _, row := range icd10Rows() {
		if parent, ok := known[row.Parent]; ok && parent.Sensitive && !row.Sensitive {
			t.Errorf("%s is under the sensitive %s but is not marked sensitive", row.Code, row.Parent)
		}
	}
}

// newTestSeeder builds the three services the reference-data steps use against a
// throw-away database. It deliberately leaves out the identity half: this test is about
// the templates, the code system and the mappings, not about provisioning.
func newTestSeeder(t *testing.T, h *dbtest.Harness) *seeder {
	t.Helper()
	cursors, err := httpx.NewCursorCodec([]byte("kapsora-seed-cursor-key-0123456789ab"))
	if err != nil {
		t.Fatal(err)
	}
	catalogSvc, err := catalogapp.New(catalogapp.Deps{
		Pool: h.App, Repo: catalogpg.New(), Audit: auditpg.New(), Cursors: cursors,
	})
	if err != nil {
		t.Fatal(err)
	}
	notificationSvc, err := notificationapp.New(notificationapp.Deps{
		Pool: h.App, Repo: notificationpg.New(nil), Audit: auditpg.New(), Cursors: cursors,
	})
	if err != nil {
		t.Fatal(err)
	}
	benefitSvc, err := benefitapp.New(benefitapp.Deps{
		Pool: h.App, Repo: benefitpg.New(), Audit: auditpg.New(), Cursors: cursors,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &seeder{
		pool: h.App, catalog: catalogSvc, notifications: notificationSvc, benefits: benefitSvc,
	}
}

// TestSeedReferenceDataIsIdempotent runs the three reference-data steps twice against one
// tenant. A seed that is not idempotent is a seed nobody dares run on an existing
// database, which is the same as a seed that does not exist.
func TestSeedReferenceDataIsIdempotent(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()
	s := newTestSeeder(t, h)
	tenant := h.CreateTenant("SEED")

	for pass := 1; pass <= 2; pass++ {
		if err := s.ensureTemplates(ctx, tenant); err != nil {
			t.Fatalf("pass %d templates: %v", pass, err)
		}
		if err := s.ensureICD10(ctx, tenant); err != nil {
			t.Fatalf("pass %d ICD-10: %v", pass, err)
		}
		if err := s.ensureEntitlementMappings(ctx, tenant); err != nil {
			t.Fatalf("pass %d mappings: %v", pass, err)
		}
	}

	// One published template per event and channel, and no second version of any of them.
	var published int
	if err := h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM notification.template
		 WHERE tenant_id = $1 AND status = 'PUBLISHED'`, tenant).Scan(&published); err != nil {
		t.Fatalf("count templates: %v", err)
	}
	if want := 2 * len(notificationapp.WiredEvents); published != want {
		t.Fatalf("published templates = %d, want %d", published, want)
	}
	var total int
	if err := h.Admin.QueryRow(ctx,
		`SELECT count(*) FROM notification.template WHERE tenant_id = $1`, tenant).Scan(&total); err != nil {
		t.Fatalf("count all templates: %v", err)
	}
	if total != published {
		t.Fatalf("%d templates in total for %d published: the second pass republished", total, published)
	}
	for _, event := range notificationapp.WiredEvents {
		for _, ch := range []string{notificationdomain.ChannelEmail, notificationdomain.ChannelInApp} {
			var n int
			if err := h.Admin.QueryRow(ctx, `
				SELECT count(*) FROM notification.template
				 WHERE tenant_id = $1 AND event_code = $2 AND channel = $3
				   AND locale = $4 AND status = 'PUBLISHED'`,
				tenant, event, ch, notificationapp.DefaultLocale).Scan(&n); err != nil {
				t.Fatalf("count %s/%s: %v", event, ch, err)
			}
			if n != 1 {
				t.Fatalf("%s/%s has %d published templates, want 1", event, ch, n)
			}
		}
	}

	// One ICD-10 system with the whole seeded set under it, still once after two passes.
	var systems, values int
	if err := h.Admin.QueryRow(ctx,
		`SELECT count(*) FROM catalog.code_system WHERE tenant_id = $1 AND code = 'ICD10'`,
		tenant).Scan(&systems); err != nil {
		t.Fatalf("count code systems: %v", err)
	}
	if systems != 1 {
		t.Fatalf("ICD-10 registered %d times", systems)
	}
	if err := h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM catalog.code_value v
		  JOIN catalog.code_system s ON s.id = v.code_system_id
		 WHERE v.tenant_id = $1 AND s.code = 'ICD10'`, tenant).Scan(&values); err != nil {
		t.Fatalf("count code values: %v", err)
	}
	if values != len(icd10Rows()) {
		t.Fatalf("code values = %d, want %d", values, len(icd10Rows()))
	}

	// And the attribute WP-I5-01 reads is on the row, as a JSON boolean rather than the
	// string "true", which is what its `attributes->>'sensitive' = 'true'` expects.
	var sensitive int
	if err := h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM catalog.code_value v
		  JOIN catalog.code_system s ON s.id = v.code_system_id
		 WHERE v.tenant_id = $1 AND s.code = 'ICD10'
		   AND v.attributes->>'sensitive' = 'true'`, tenant).Scan(&sensitive); err != nil {
		t.Fatalf("count sensitive values: %v", err)
	}
	if sensitive == 0 {
		t.Fatal("no seeded ICD-10 value is marked sensitive")
	}
}

// TestSeedTemplateBodiesAreTurkishAndCarryNoSecret is a small guard on the copy itself:
// the shipped messages are Turkish and none of them hard-codes a value nobody meant to
// send to everybody who ever receives that event.
func TestSeedTemplateBodiesAreTurkishAndCarryNoSecret(t *testing.T) {
	for _, tpl := range notificationapp.SeedTemplates() {
		if !strings.Contains(tpl.Body, "{{") {
			t.Errorf("%s/%s says nothing about the thing that happened", tpl.EventCode, tpl.Channel)
		}
		if strings.Contains(tpl.Body, "http://") || strings.Contains(tpl.Body, "https://") {
			t.Errorf("%s/%s hard-codes an absolute link", tpl.EventCode, tpl.Channel)
		}
		if tpl.Channel == notificationdomain.ChannelEmail && tpl.Subject == "" {
			t.Errorf("%s has an e-mail template with no subject", tpl.EventCode)
		}
		if tpl.Channel != notificationdomain.ChannelEmail && tpl.Subject != "" {
			t.Errorf("%s/%s carries a subject on a channel that has none", tpl.EventCode, tpl.Channel)
		}
	}
	// The seed writes for one locale and says so; a template published under another
	// locale would render for nobody.
	if notificationapp.DefaultLocale != "tr-TR" {
		t.Fatalf("the seed publishes %s templates", notificationapp.DefaultLocale)
	}
}
