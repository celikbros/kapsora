package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/celikbros/kapsora/internal/notification/domain"
)

// A realistic clinical sentence, an operator's free-text comment and the two Turkish
// identity number shapes. Every test below feeds one of them at the renderer and asserts
// it comes back refused, because a notification leaves the system and cannot be recalled.
const (
	diagnosis = "C50.9 malign meme neoplazmı, sağ üst dış kadran, evre 2A"
	comment   = "Hasta ile görüştüm, raporun eksik olduğunu söyledi; ek belge istendi."
	tckn      = "10000000146"
	vkn       = "1234567802"
)

// emailTemplate is the smallest published template that uses every kind of variable.
func emailTemplate() domain.Template {
	return domain.Template{
		EventCode: "authorization.approved",
		Channel:   domain.ChannelEmail,
		Locale:    "tr-TR",
		VersionNo: 1,
		Subject:   "{{reference_no}} numaralı başvurunuz",
		Body: "Sayın {{given_name}}, {{program_name}} kapsamındaki {{reference_no}} numaralı " +
			"başvurunuz {{status_code}} durumuna geçti. {{event_date}} tarihine kadar " +
			"{{amount}} {{currency}} tutarında hakkınız var. Ayrıntı: {{deep_link}}",
		DeclaredVariables: []string{
			domain.VarGivenName, domain.VarProgramName, domain.VarReferenceNo,
			domain.VarStatusCode, domain.VarEventDate, domain.VarAmount,
			domain.VarCurrency, domain.VarDeepLink,
		},
	}
}

func goodVariables() map[string]string {
	return map[string]string{
		domain.VarGivenName:   "Ayşe",
		domain.VarProgramName: "Kurumsal Sağlık 2026",
		domain.VarReferenceNo: "AUT-2026-0042",
		domain.VarStatusCode:  "ONAYLANDI",
		domain.VarEventDate:   "2026-12-31",
		domain.VarAmount:      "1250.00",
		domain.VarCurrency:    "TRY",
		domain.VarDeepLink:    "/authorizations/0199bd4e-6a1e-7a9c-8f31-2b7c0d5e4a11",
	}
}

// TestRenderProducesTheWholeMessage is the happy path, and it is here so the refusals
// below mean something: the same template and the same variable names do render when the
// values have the shape their slot promises.
func TestRenderProducesTheWholeMessage(t *testing.T) {
	out, err := domain.Render(emailTemplate(), goodVariables(), "https://kapsora.example")
	if err != nil {
		t.Fatalf("render the happy path: %v", err)
	}
	if !strings.Contains(out.Body, "Sayın Ayşe,") {
		t.Fatalf("the given name was not substituted: %q", out.Body)
	}
	if !strings.Contains(out.Subject, "AUT-2026-0042") {
		t.Fatalf("the reference was not substituted into the subject: %q", out.Subject)
	}
	if strings.Contains(out.Body, "{{") {
		t.Fatalf("a placeholder survived the render: %q", out.Body)
	}
	// The link is the configured base plus the path, and nothing else. A query string is
	// where a token would ride along, and there is none.
	if !strings.Contains(out.Body, "https://kapsora.example/authorizations/") {
		t.Fatalf("the deep link was not made absolute: %q", out.Body)
	}
	if strings.ContainsAny(out.Body, "?#") {
		t.Fatalf("the rendered body carries a query string or a fragment: %q", out.Body)
	}
}

// TestRenderRefusesSensitiveContent is the rule the whole package exists for. A diagnosis,
// an identity number and an operator's comment each reach the renderer by the three routes
// that exist — an undeclared name, a declared name whose value is wrong, and a name that
// is not in the catalogue at all — and every one of them is refused with nothing produced.
func TestRenderRefusesSensitiveContent(t *testing.T) {
	cases := []struct {
		name string
		vars func(map[string]string) map[string]string
		code string
	}{
		{
			// There is no slot for a diagnosis, so a diagnosis cannot be supplied under
			// any name. This is the refusal that makes the catalogue closed.
			name: "a diagnosis under a name of its own",
			vars: func(v map[string]string) map[string]string { v["diagnosis"] = diagnosis; return v },
			code: domain.CodeUnknownVariable,
		},
		{
			name: "a diagnosis smuggled into the given name",
			vars: func(v map[string]string) map[string]string {
				v[domain.VarGivenName] = diagnosis
				return v
			},
			code: domain.CodeUnsafeValue,
		},
		{
			name: "a diagnosis smuggled into the program name",
			vars: func(v map[string]string) map[string]string {
				v[domain.VarProgramName] = diagnosis
				return v
			},
			code: domain.CodeUnsafeValue,
		},
		{
			name: "a TCKN under a name of its own",
			vars: func(v map[string]string) map[string]string { v["tckn"] = tckn; return v },
			code: domain.CodeUnknownVariable,
		},
		{
			// The reference number slot accepts digits, which is exactly why the digit run
			// rule has to be there: an eleven digit reference is a TCKN.
			name: "a TCKN in the reference number",
			vars: func(v map[string]string) map[string]string {
				v[domain.VarReferenceNo] = tckn
				return v
			},
			code: domain.CodeUnsafeValue,
		},
		{
			// And the amount slot accepts digits too. A TCKN rendered into "Tutar: ... TRY"
			// is still a TCKN in an e-mail.
			name: "a TCKN in the amount",
			vars: func(v map[string]string) map[string]string {
				v[domain.VarAmount] = tckn
				return v
			},
			code: domain.CodeUnsafeValue,
		},
		{
			name: "a VKN in the reference number",
			vars: func(v map[string]string) map[string]string {
				v[domain.VarReferenceNo] = vkn
				return v
			},
			code: domain.CodeUnsafeValue,
		},
		{
			name: "a TCKN in the given name",
			vars: func(v map[string]string) map[string]string {
				v[domain.VarGivenName] = tckn
				return v
			},
			code: domain.CodeUnsafeValue,
		},
		{
			name: "an operator comment under a name of its own",
			vars: func(v map[string]string) map[string]string { v["comment"] = comment; return v },
			code: domain.CodeUnknownVariable,
		},
		{
			name: "an operator comment in the status word",
			vars: func(v map[string]string) map[string]string {
				v[domain.VarStatusCode] = comment
				return v
			},
			code: domain.CodeUnsafeValue,
		},
		{
			name: "an operator comment in the given name",
			vars: func(v map[string]string) map[string]string {
				v[domain.VarGivenName] = comment
				return v
			},
			code: domain.CodeUnsafeValue,
		},
		{
			// A link that carries a token would be a secret in a mail nobody can recall.
			name: "a token on the end of a deep link",
			vars: func(v map[string]string) map[string]string {
				v[domain.VarDeepLink] = "/vouchers/redeem?token=9f2ab41c7d004e2f"
				return v
			},
			code: domain.CodeUnsafeValue,
		},
		{
			name: "an absolute link to somewhere else entirely",
			vars: func(v map[string]string) map[string]string {
				v[domain.VarDeepLink] = "https://phish.example/authorizations"
				return v
			},
			code: domain.CodeUnsafeValue,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := domain.Render(emailTemplate(), c.vars(goodVariables()), "https://kapsora.example")
			if err == nil {
				t.Fatalf("the render was accepted; it produced %q / %q", out.Subject, out.Body)
			}
			if !errors.Is(err, domain.ErrValidation) {
				t.Fatalf("the refusal is not a validation error: %v", err)
			}
			var ve *domain.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("the refusal carries no fields: %v", err)
			}
			if !ve.Has(c.code) {
				t.Fatalf("refused with %+v, want a %s", ve.Fields, c.code)
			}
			// Nothing was produced. This is the other half of "writes nothing": there is
			// no partially rendered body for a caller to store by mistake.
			if out.Subject != "" || out.Body != "" {
				t.Fatalf("a refused render produced text: %q / %q", out.Subject, out.Body)
			}
		})
	}
}

// TestScreenVariablesRefusesTheSameThingsWithoutATemplate: the screen runs before any
// template is read, so a value that may never leave the system is refused before it can be
// written even into a message that was going to be suppressed anyway.
func TestScreenVariablesRefusesTheSameThingsWithoutATemplate(t *testing.T) {
	for _, vars := range []map[string]string{
		{"diagnosis": diagnosis},
		{"tckn": tckn},
		{"comment": comment},
		{domain.VarGivenName: tckn},
		{domain.VarReferenceNo: vkn},
		{domain.VarAmount: "12345678901.00"},
		{domain.VarDeepLink: "/a/12345678"},
	} {
		if err := domain.ScreenVariables(vars); err == nil {
			t.Fatalf("ScreenVariables accepted %v", vars)
		}
	}
	if err := domain.ScreenVariables(goodVariables()); err != nil {
		t.Fatalf("ScreenVariables refused a safe set: %v", err)
	}
	// The digit run rule applies to amounts too, but its threshold is the length of the
	// shortest identity number rather than a view about how much money belongs in a
	// message. At eight it also refused SR-YYYYMMDD-XXXXXXXX, the format a service request
	// reference is actually minted in, so the rule meant to keep identity numbers out was
	// keeping the one value a notification exists to carry out as well (migration 000030).
	for _, amount := range []string{"9999999.99", "10000000.00", "999999999.99"} {
		if err := domain.ScreenVariables(map[string]string{domain.VarAmount: amount}); err != nil {
			t.Errorf("a legitimate amount %s was refused: %v", amount, err)
		}
	}
	// Ten digits in a row is a VKN and eleven is a TCKN; both are still refused.
	for _, amount := range []string{"1234567890.00", "12345678901.00"} {
		if err := domain.ScreenVariables(map[string]string{domain.VarAmount: amount}); err == nil {
			t.Errorf("%s ran to ten digits and was accepted; that is the shape of an identity number", amount)
		}
	}
}

// TestRenderRefusesAVariableTheTemplateDidNotDeclare: an undeclared variable is refused,
// not dropped. Dropping it would render a body that quietly says something other than what
// the caller meant, and the caller would never know.
func TestRenderRefusesAVariableTheTemplateDidNotDeclare(t *testing.T) {
	vars := goodVariables()
	vars[domain.VarExpiresAt] = "2026-11-01" // in the catalogue, not in this template
	_, err := domain.Render(emailTemplate(), vars, "https://kapsora.example")
	var ve *domain.ValidationError
	if !errors.As(err, &ve) || !ve.Has(domain.CodeNotDeclared) {
		t.Fatalf("an undeclared catalogue variable was not refused: %v", err)
	}

	// And a declared variable nobody supplied is refused rather than rendered empty: half
	// a sentence is worse than no message.
	missing := goodVariables()
	delete(missing, domain.VarStatusCode)
	_, err = domain.Render(emailTemplate(), missing, "https://kapsora.example")
	if !errors.As(err, &ve) || !ve.Has(domain.CodeMissingVariable) {
		t.Fatalf("a missing declared variable was not refused: %v", err)
	}
}

// TestRenderRefusesTextThatOnlyBecomesAnIdentityNumberWhenAssembled is the last guard: two
// values that are each fine on their own can meet in the body and make eleven digits.
func TestRenderRefusesTextThatOnlyBecomesAnIdentityNumberWhenAssembled(t *testing.T) {
	template := domain.Template{
		EventCode: "authorization.approved", Channel: domain.ChannelSMS, Locale: "tr",
		VersionNo: 1,
		// Two safe values with nothing between them. Neither has a run of eight digits;
		// together they have eleven.
		Body:              "{{reference_no}}{{amount}}",
		DeclaredVariables: []string{domain.VarReferenceNo, domain.VarAmount},
	}
	_, err := domain.Render(template, map[string]string{
		domain.VarReferenceNo: "1000000",
		domain.VarAmount:      "0146",
	}, "https://kapsora.example")
	var ve *domain.ValidationError
	if !errors.As(err, &ve) || !ve.Has(domain.CodeUnsafeValue) {
		t.Fatalf("an identity number assembled from two safe values was not refused: %v", err)
	}
}

// TestValidateTemplateRefusesAnUnsafeDeclaration: a template cannot declare a variable the
// catalogue does not have, so there is no way to get a slot for a diagnosis by writing one
// into a template either.
func TestValidateTemplateRefusesAnUnsafeDeclaration(t *testing.T) {
	cases := []struct {
		name  string
		build func(domain.Template) domain.Template
	}{
		{"a declared variable outside the catalogue", func(tpl domain.Template) domain.Template {
			tpl.Body = "Tanı: {{diagnosis}}"
			tpl.DeclaredVariables = []string{"diagnosis"}
			return tpl
		}},
		{"an identity number typed into the body", func(tpl domain.Template) domain.Template {
			tpl.Body = "Kayıt numaranız " + tckn
			tpl.DeclaredVariables = nil
			tpl.Subject = "Kayıt"
			return tpl
		}},
		{"a placeholder nobody declared", func(tpl domain.Template) domain.Template {
			tpl.Body = "Sayın {{given_name}}, hoş geldiniz."
			tpl.DeclaredVariables = nil
			tpl.Subject = "Hoş geldiniz"
			return tpl
		}},
		{"a declared variable the body never uses", func(tpl domain.Template) domain.Template {
			tpl.Body = "Merhaba."
			tpl.Subject = "Merhaba"
			tpl.DeclaredVariables = []string{domain.VarGivenName}
			return tpl
		}},
		{"a mistyped placeholder", func(tpl domain.Template) domain.Template {
			tpl.Body = "Sayın {{ given_name }, hoş geldiniz."
			tpl.Subject = "Hoş geldiniz"
			tpl.DeclaredVariables = nil
			return tpl
		}},
		{"a subject on a channel that has none", func(tpl domain.Template) domain.Template {
			tpl.Channel = domain.ChannelSMS
			tpl.Body = "Merhaba."
			tpl.Subject = "Merhaba"
			tpl.DeclaredVariables = nil
			return tpl
		}},
		{"an e-mail with no subject", func(tpl domain.Template) domain.Template {
			tpl.Body = "Merhaba."
			tpl.Subject = ""
			tpl.DeclaredVariables = nil
			return tpl
		}},
	}
	base := domain.Template{
		EventCode: "authorization.approved", Channel: domain.ChannelEmail, Locale: "tr-TR",
		VersionNo: 1, Subject: "Konu",
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := domain.ValidateTemplate(c.build(base)); err == nil {
				t.Fatal("the template was accepted")
			}
		})
	}

	// The one that has to pass, so the refusals above are not simply "everything fails".
	ok := base
	ok.Body = "Sayın {{given_name}}, hoş geldiniz."
	ok.Subject = "Hoş geldiniz"
	ok.DeclaredVariables = []string{domain.VarGivenName}
	if err := domain.ValidateTemplate(ok); err != nil {
		t.Fatalf("a well formed template was refused: %v", err)
	}
}

// TestQuietHoursWrapAroundMidnight: the window people actually set is 22:00 to 08:00, and
// it is read in the recipient's own zone. A member in Berlin and one in Istanbul do not
// share a night.
func TestQuietHoursWrapAroundMidnight(t *testing.T) {
	start := domain.NewClockTime(22, 0)
	end := domain.NewClockTime(8, 0)

	// 02:00 in Istanbul is 23:00 UTC the day before.
	night := time.Date(2026, 9, 4, 23, 0, 0, 0, time.UTC)
	quiet, err := domain.InQuietHours(night, "Europe/Istanbul", start, end)
	if err != nil {
		t.Fatalf("quiet hours in Istanbul: %v", err)
	}
	if !quiet {
		t.Fatal("02:00 in Istanbul is not inside 22:00-08:00")
	}
	// The same instant is 01:00 in Berlin, which is also inside the window; an hour later
	// in UTC it is 08:00 in Istanbul and out of it, and 07:00 in Berlin and still in.
	morning := time.Date(2026, 9, 5, 5, 0, 0, 0, time.UTC)
	istanbul, err := domain.InQuietHours(morning, "Europe/Istanbul", start, end)
	if err != nil {
		t.Fatalf("quiet hours in Istanbul: %v", err)
	}
	berlin, err := domain.InQuietHours(morning, "Europe/Berlin", start, end)
	if err != nil {
		t.Fatalf("quiet hours in Berlin: %v", err)
	}
	if istanbul {
		t.Fatal("08:00 in Istanbul is inside 22:00-08:00; the window is half open")
	}
	if !berlin {
		t.Fatal("07:00 in Berlin is outside 22:00-08:00; the zone was not applied")
	}

	// A window that does not wrap works the ordinary way.
	lunch, err := domain.InQuietHours(
		time.Date(2026, 9, 5, 9, 30, 0, 0, time.UTC), "Europe/Istanbul",
		domain.NewClockTime(12, 0), domain.NewClockTime(13, 0))
	if err != nil {
		t.Fatalf("quiet hours at lunch: %v", err)
	}
	if !lunch {
		t.Fatal("12:30 is not inside 12:00-13:00")
	}

	// An unresolvable zone is an error rather than a false. Answering "not quiet" would
	// write to somebody in the middle of their night because a zone name was misspelt.
	if _, err := domain.InQuietHours(night, "Europe/Istanbull", start, end); err == nil {
		t.Fatal("an unknown time zone was treated as no quiet hours")
	}
}

// TestClockTimeRoundTrip covers what the wire and the column each carry.
func TestClockTimeRoundTrip(t *testing.T) {
	for _, raw := range []string{"00:00", "07:30", "22:00", "23:59"} {
		parsed, err := domain.ParseClockTime(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		if parsed.String() != raw {
			t.Fatalf("%q round-tripped as %q", raw, parsed.String())
		}
	}
	// The column answers seconds; they are dropped rather than rounding the window.
	if got, err := domain.ParseClockTime("08:00:00"); err != nil || got.String() != "08:00" {
		t.Fatalf("parse 08:00:00 = %v, %v", got, err)
	}
	for _, raw := range []string{"", "24:00", "8:0", "07:60", "seven", "07-30"} {
		if _, err := domain.ParseClockTime(raw); err == nil {
			t.Fatalf("%q was accepted as a time of day", raw)
		}
	}
}

// TestValidTimezoneUsesTheEmbeddedDatabase: the zone database is embedded, so the answer
// does not depend on what the host has installed. Windows ships none at all.
func TestValidTimezoneUsesTheEmbeddedDatabase(t *testing.T) {
	for _, tz := range []string{"Europe/Istanbul", "Europe/Berlin", "UTC", "America/New_York"} {
		if !domain.ValidTimezone(tz) {
			t.Fatalf("%s is not resolvable; the embedded zone database is missing", tz)
		}
	}
	for _, tz := range []string{"", "Mars/Olympus", "Europe/Istanbull", "../../etc/passwd"} {
		if domain.ValidTimezone(tz) {
			t.Fatalf("%q was accepted as a time zone", tz)
		}
	}
}

// TestStripRecordIDsLeavesADeepLinkAlone is the reason the digit run rule does not refuse
// every message whose link happens to contain eight digits. Migration 000029 strips the
// same shape in its CHECK, and the two have to agree.
func TestStripRecordIDsLeavesADeepLinkAlone(t *testing.T) {
	// A uuid whose groups are all digits: the shape that would otherwise look exactly like
	// an identity number to a regular expression.
	link := "/authorizations/01234567-8901-4234-8901-234567890123"
	if strings.ContainsAny(domain.StripRecordIDs(link), "0123456789") {
		t.Fatalf("StripRecordIDs left digits behind: %q", domain.StripRecordIDs(link))
	}
	if err := domain.ScreenVariables(map[string]string{domain.VarDeepLink: link}); err != nil {
		t.Fatalf("a deep link carrying an all-digit record id was refused: %v", err)
	}
}

// TestARealServiceRequestReferenceIsCarryable is the bug the digit-run rule had at eight:
// a reference is minted as SR-YYYYMMDD-XXXXXXXX, the date in the middle is eight digits,
// and the rule meant to stop identity numbers refused the one value a notification exists
// to carry. Ten is the shortest identity number (a VKN; a TCKN has eleven), so both are
// still caught and a date is not.
func TestARealServiceRequestReferenceIsCarryable(t *testing.T) {
	for _, reference := range []string{
		"SR-20260904-K3XQ7ZM2", // the real format, minted by internal/servicerequest
		"AUT-20260904-P7RTVA41",
		"KPS-2026-0042",
	} {
		if err := domain.ScreenVariables(map[string]string{
			domain.VarReferenceNo: reference,
		}); err != nil {
			t.Errorf("a member could not be told their own reference %q: %v", reference, err)
		}
	}

	// And the numbers the rule is actually for are still refused, in every slot.
	for name, value := range map[string]string{
		"tckn": "12345678901",
		"vkn":  "1234567890",
	} {
		if err := domain.ScreenVariables(map[string]string{
			domain.VarReferenceNo: value,
		}); err == nil {
			t.Errorf("a %s passed the screen as a reference number", name)
		}
	}
}
