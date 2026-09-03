package domain_test

import (
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	orgdomaintest "github.com/celikbros/kapsora/internal/organization/domain/domaintest"
	"github.com/celikbros/kapsora/internal/party/domain"
)

func TestFoldUsesTurkishCaseRulesAndKeepsDiacritics(t *testing.T) {
	cases := map[string]string{
		"İSMAİL":      "ismail",
		"IŞIL":        "ışıl",
		"Çağla  Öz":   "çağla öz",
		"  Ali  Veli": "ali veli",
		"ÜMİT":        "ümit",
		"Iğdır":       "ığdır",
	}
	for in, want := range cases {
		if got := domain.Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
	// Diacritics are kept, so folded Turkish names stay distinguishable.
	if domain.Fold("Çağla") == domain.Fold("Cagla") {
		t.Fatal("diacritics must survive folding")
	}
}

func TestNormalizedAndDisplayName(t *testing.T) {
	if got := domain.NormalizedName("Ayşe", "Nur", "YILMAZ"); got != "yılmaz, ayşe nur" {
		t.Fatalf("NormalizedName = %q", got)
	}
	if got := domain.NormalizedName("İbrahim", "", "Işık"); got != "ışık, ibrahim" {
		t.Fatalf("NormalizedName = %q", got)
	}
	if got := domain.DisplayName("  Ayşe ", "Nur", "Yılmaz"); got != "Ayşe Nur Yılmaz" {
		t.Fatalf("DisplayName = %q", got)
	}
	if got := domain.DisplayName("Ali", "", "Veli"); got != "Ali Veli" {
		t.Fatalf("DisplayName = %q", got)
	}
	// The search pattern is folded and its LIKE wildcards neutralised.
	if got := domain.SearchPattern("%İSM_AİL%"); got != `\%ism\_ail\%` {
		t.Fatalf("SearchPattern = %q", got)
	}
}

func TestIdentifierNormalisationValidationAndMasking(t *testing.T) {
	if got := domain.NormalizeIdentifier("  123-456 789 \t"); got != "123456789" {
		t.Fatalf("NormalizeIdentifier = %q", got)
	}
	if got := domain.NormalizeIdentifier("ab-12 cd"); got != "AB12CD" {
		t.Fatalf("NormalizeIdentifier = %q", got)
	}

	r := rand.New(rand.NewPCG(11, 12))
	tckn := orgdomaintest.GenerateTCKN(r)
	if err := domain.ValidateIdentifier(domain.TypeTCKN, tckn); err != nil {
		t.Fatalf("generated TCKN rejected: %v", err)
	}
	// A single flipped digit breaks the checksum.
	broken := []byte(tckn)
	broken[4] = '0' + (broken[4]-'0'+1)%10
	if err := domain.ValidateIdentifier(domain.TypeTCKN, string(broken)); !errors.Is(err, domain.ErrIdentifierInvalid) {
		t.Fatalf("broken TCKN accepted: %v", err)
	}
	for _, bad := range []string{"", "123", "12345678901", "ABCDEFGHIJK"} {
		if err := domain.ValidateIdentifier(domain.TypeTCKN, bad); err == nil {
			t.Fatalf("TCKN %q accepted", bad)
		}
	}
	memberNo := domain.NormalizeIdentifier("emp/2026-11")
	if err := domain.ValidateIdentifier(domain.TypeMemberNo, memberNo); err != nil {
		t.Fatalf("member number %q rejected: %v", memberNo, err)
	}
	if err := domain.ValidateIdentifier(domain.TypeMemberNo, domain.NormalizeIdentifier("ÜYE 1")); err == nil {
		t.Fatal("non-ASCII member number accepted")
	}

	masked := domain.MaskIdentifier(domain.TypeTCKN, tckn)
	if masked != tckn[:3]+"******"+tckn[9:] || strings.Contains(masked, tckn) {
		t.Fatalf("TCKN mask = %q", masked)
	}
	if got := domain.MaskIdentifier(domain.TypeMemberNo, "MEM12345"); got != "*****345" {
		t.Fatalf("member number mask = %q", got)
	}
	if got := domain.MaskIdentifier(domain.TypePassport, "U12345678"); got != "U1*******" {
		t.Fatalf("passport mask = %q", got)
	}
	if got := domain.MaskIdentifier("CUSTOMER_NO", "C-77"); got != "**" {
		t.Fatalf("generic mask = %q", got)
	}
	if got := domain.BlindIndexInput(domain.TypeTCKN, tckn); got != "TCKN:"+tckn {
		t.Fatalf("BlindIndexInput = %q", got)
	}
}

func TestScopeKeyImplementsD5(t *testing.T) {
	if key, ok := domain.ScopeKey(domain.ScopeTenant, "sponsor", "row"); !ok || key != "" {
		t.Fatalf("TENANT scope = %q %t", key, ok)
	}
	if key, ok := domain.ScopeKey(domain.ScopeSponsor, "sponsor", "row"); !ok || key != "sponsor" {
		t.Fatalf("SPONSOR scope = %q %t", key, ok)
	}
	if _, ok := domain.ScopeKey(domain.ScopeSponsor, "", "row"); ok {
		t.Fatal("SPONSOR scope without a sponsor must fail")
	}
	if key, ok := domain.ScopeKey(domain.ScopeNone, "", "row"); !ok || key != "row" {
		t.Fatalf("NONE scope = %q %t", key, ok)
	}
}

func TestValidateNewCollectsEveryProblem(t *testing.T) {
	today := time.Date(2026, time.September, 3, 0, 0, 0, 0, time.UTC)
	future := today.AddDate(1, 0, 0)
	in := domain.NewPerson{
		FirstName: "  ", LastName: "Yılmaz", BirthDate: &future, SexAtBirth: "OTHER",
		Identifiers: []domain.SubmittedIdentifier{
			{Type: "tckn", Value: "1"},
			{Type: domain.TypeTCKN, Value: "12345678901"},
			{Type: domain.TypeTCKN, Value: "12345678901"},
		},
	}
	err := domain.ValidateNew(&in, today)
	var ve *domain.ValidationError
	if !errors.As(err, &ve) || !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("expected a validation error, got %v", err)
	}
	got := map[string]string{}
	for _, f := range ve.Fields {
		got[f.Field] = f.Code
	}
	want := map[string]string{
		"firstName":            "REQUIRED",
		"birthDate":            "RANGE",
		"sexAtBirth":           "ENUM",
		"identifiers[0].type":  domain.CodeIdentifierTypeUnknown,
		"identifiers[1].value": domain.CodeIdentifierInvalid,
		"identifiers[2].type":  domain.CodeIdentifierDuplicated,
	}
	for field, code := range want {
		if got[field] != code {
			t.Errorf("field %s = %q, want %q (all: %v)", field, got[field], code, got)
		}
	}

	old := time.Date(1899, time.December, 31, 0, 0, 0, 0, time.UTC)
	tooOld := domain.NewPerson{FirstName: "Ali", LastName: "Veli", BirthDate: &old}
	if err := domain.ValidateNew(&tooOld, today); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("1899 birth date accepted: %v", err)
	}
	ok := domain.NewPerson{FirstName: " Ayşe ", LastName: " Yılmaz ", SexAtBirth: "FEMALE"}
	if err := domain.ValidateNew(&ok, today); err != nil {
		t.Fatalf("valid person rejected: %v", err)
	}
	if ok.FirstName != "Ayşe" || ok.LastName != "Yılmaz" {
		t.Fatalf("names not cleaned: %+v", ok)
	}
}

func TestValidatePatchRejectsMergedStatusAndRemoveOnCreate(t *testing.T) {
	today := time.Now().UTC()
	merged := domain.PersonMerged
	patch := domain.PersonPatch{Status: &merged}
	if err := domain.ValidatePatch(&patch, today); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("MERGED accepted through patch: %v", err)
	}
	create := domain.NewPerson{
		FirstName: "Ali", LastName: "Veli",
		Identifiers: []domain.SubmittedIdentifier{{Type: domain.TypeTCKN, Remove: true}},
	}
	if err := domain.ValidateNew(&create, today); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("remove accepted on create: %v", err)
	}
}

func TestValidatePeriodRequiresAnIncreasingRange(t *testing.T) {
	from := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	if err := domain.ValidatePeriod("valid", from, nil); err != nil {
		t.Fatalf("open period rejected: %v", err)
	}
	same := from
	if err := domain.ValidatePeriod("valid", from, &same); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("empty period accepted")
	}
	later := from.AddDate(0, 1, 0)
	if err := domain.ValidatePeriod("valid", from, &later); err != nil {
		t.Fatalf("closed period rejected: %v", err)
	}
}
