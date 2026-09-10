package application_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/celikbros/kapsora/internal/notification/application"
	"github.com/celikbros/kapsora/internal/notification/domain"
)

// This file is the extension the work package asks for: domain's own
// TestRenderRefusesSensitiveContent proves the renderer refuses a diagnosis in one
// hand-written template, and this proves it for every template the product actually
// ships, through every variable each of them declares.
//
// It has to live here rather than beside the domain test because the templates are
// declared in the application package. That is the point of declaring them there at all:
// a template catalogue in cmd/seed would be a catalogue no test could walk.

const (
	seedDiagnosis = "C50.9 malign meme neoplazmı, sağ üst dış kadran, evre 2A"
	seedComment   = "Hasta ile görüştüm, raporun eksik olduğunu söyledi; ek belge istendi."
	seedTCKN      = "10000000146"
)

// safeValues is one legitimate value per catalogue name, so the refusals below are about
// the value that was swapped in and nothing else.
func safeValues() map[string]string {
	return map[string]string{
		domain.VarGivenName:    "Ayşe",
		domain.VarReferenceNo:  "SR-20260615-ABCDEFGH",
		domain.VarStatusCode:   "APPROVED",
		domain.VarEventDate:    "2026-06-15",
		domain.VarExpiresAt:    "2026-07-15",
		domain.VarAmount:       "1250.00",
		domain.VarCurrency:     "TRY",
		domain.VarProviderName: "Demo Hastane",
		domain.VarProgramName:  "Kurumsal Sağlık",
		domain.VarPropertyName: "Demo Sahil Otel",
		// Four characters, which is the whole of what `masked_account` may ever carry
		// (WP-I7-04). A longer sample here would be a sample the catalogue itself refuses.
		domain.VarMaskedAccount: "1326",
		domain.VarDeepLink:      "/requests/0199bd4e-6a1e-7a9c-8f31-2b7c0d5e4a11",
	}
}

// variablesFor is the value set one template declares, and nothing more: an undeclared
// variable is refused, so a superset would fail the happy path for the wrong reason.
func variablesFor(t application.SeedTemplate) map[string]string {
	all := safeValues()
	out := make(map[string]string, len(t.Variables))
	for _, name := range t.Variables {
		out[name] = all[name]
	}
	return out
}

// TestSeedTemplatesAreValidAndRender is the happy path the refusals below are measured
// against: every shipped template passes the same validation an operator's would, and
// every one of them renders from the values its own event actually supplies.
func TestSeedTemplatesAreValidAndRender(t *testing.T) {
	templates := application.SeedTemplates()
	if len(templates) != 2*len(application.WiredEvents) {
		t.Fatalf("%d templates for %d events; every event needs an EMAIL and an INAPP",
			len(templates), len(application.WiredEvents))
	}
	seen := map[string]map[string]bool{}
	for _, tpl := range templates {
		if err := domain.ValidateTemplate(domain.Template{
			EventCode: tpl.EventCode, Channel: tpl.Channel,
			Locale: application.DefaultLocale, VersionNo: 1,
			Subject: tpl.Subject, Body: tpl.Body, DeclaredVariables: tpl.Variables,
		}); err != nil {
			t.Fatalf("%s/%s does not validate: %v", tpl.EventCode, tpl.Channel, err)
		}
		vars := variablesFor(tpl)
		out, err := domain.Render(domain.Template{
			EventCode: tpl.EventCode, Channel: tpl.Channel, Locale: application.DefaultLocale,
			Subject: tpl.Subject, Body: tpl.Body, DeclaredVariables: tpl.Variables,
		}, vars, "https://kapsora.example")
		if err != nil {
			t.Fatalf("%s/%s does not render: %v", tpl.EventCode, tpl.Channel, err)
		}
		if strings.Contains(out.Body, "{{") || strings.TrimSpace(out.Body) == "" {
			t.Fatalf("%s/%s rendered %q", tpl.EventCode, tpl.Channel, out.Body)
		}
		if seen[tpl.EventCode] == nil {
			seen[tpl.EventCode] = map[string]bool{}
		}
		if seen[tpl.EventCode][tpl.Channel] {
			t.Fatalf("%s/%s is declared twice", tpl.EventCode, tpl.Channel)
		}
		seen[tpl.EventCode][tpl.Channel] = true
	}
	for _, event := range application.WiredEvents {
		if !seen[event][domain.ChannelEmail] || !seen[event][domain.ChannelInApp] {
			t.Fatalf("%s is missing a template: %+v", event, seen[event])
		}
	}
}

// TestSeedTemplatesRefuseSmuggledContent walks every shipped template and every variable
// it declares, and puts a diagnosis, an operator's comment and an identity number into
// each of them in turn. Every one is refused and nothing is produced.
//
// The undeclared case is walked too, because it is the one that makes the catalogue
// closed: there is no slot for a diagnosis, so a caller that adds one under a name of its
// own is refused rather than having it quietly dropped.
func TestSeedTemplatesRefuseSmuggledContent(t *testing.T) {
	payloads := map[string]string{
		"a diagnosis":          seedDiagnosis,
		"an operator comment":  seedComment,
		"an identity number":   seedTCKN,
		"an absolute link out": "https://phish.example/requests",
	}
	for _, tpl := range application.SeedTemplates() {
		template := domain.Template{
			EventCode: tpl.EventCode, Channel: tpl.Channel, Locale: application.DefaultLocale,
			Subject: tpl.Subject, Body: tpl.Body, DeclaredVariables: tpl.Variables,
		}
		for label, payload := range payloads {
			for _, name := range tpl.Variables {
				t.Run(tpl.EventCode+"/"+tpl.Channel+"/"+name+"/"+label, func(t *testing.T) {
					vars := variablesFor(tpl)
					vars[name] = payload
					assertRefused(t, template, vars, domain.CodeUnsafeValue)
				})
			}
			t.Run(tpl.EventCode+"/"+tpl.Channel+"/undeclared/"+label, func(t *testing.T) {
				vars := variablesFor(tpl)
				vars["diagnosis"] = payload
				assertRefused(t, template, vars, domain.CodeUnknownVariable)
			})
		}
	}
}

func assertRefused(t *testing.T, template domain.Template, vars map[string]string, code string) {
	t.Helper()
	out, err := domain.Render(template, vars, "https://kapsora.example")
	if err == nil {
		t.Fatalf("the render was accepted; it produced %q / %q", out.Subject, out.Body)
	}
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("the refusal carries no fields: %v", err)
	}
	if !ve.Has(code) {
		t.Fatalf("refused with %+v, want a %s", ve.Fields, code)
	}
	if out.Subject != "" || out.Body != "" {
		t.Fatalf("a refused render produced text: %q / %q", out.Subject, out.Body)
	}
}
