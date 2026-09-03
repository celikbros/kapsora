package domain_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/celikbros/kapsora/internal/benefit/domain"
)

func date(t *testing.T, s string) *time.Time {
	t.Helper()
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return &d
}

func TestNormalizeDecimalTrimsAndValidates(t *testing.T) {
	cases := []struct{ in, want string }{
		{"0", "0"},
		{"0.000000", "0"},
		{"1000.500000", "1000.5"},
		{"00120.000", "120"},
		{"7", "7"},
		{"12345678901234.123456", "12345678901234.123456"},
		{"-5.50", "-5.5"},
	}
	for _, c := range cases {
		got, err := domain.NormalizeDecimal(c.in)
		if err != nil {
			t.Fatalf("NormalizeDecimal(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("NormalizeDecimal(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"", "abc", "1.2.3", "1e5", "1.1234567", "123456789012345678901"} {
		if _, err := domain.NormalizeDecimal(bad); err == nil {
			t.Fatalf("NormalizeDecimal(%q) accepted an invalid value", bad)
		}
	}
}

func TestTransitions(t *testing.T) {
	if !domain.ProgramTransitionAllowed(domain.ProgramDraft, domain.ProgramActive) ||
		!domain.ProgramTransitionAllowed(domain.ProgramActive, domain.ProgramSuspended) ||
		!domain.ProgramTransitionAllowed(domain.ProgramSuspended, domain.ProgramActive) ||
		!domain.ProgramTransitionAllowed(domain.ProgramSuspended, domain.ProgramClosed) {
		t.Fatal("a documented program transition was refused")
	}
	if domain.ProgramTransitionAllowed(domain.ProgramClosed, domain.ProgramActive) ||
		domain.ProgramTransitionAllowed(domain.ProgramDraft, domain.ProgramClosed) {
		t.Fatal("an undocumented program transition was allowed")
	}
	if !domain.PlanTransitionAllowed(domain.PlanDraft, domain.PlanActive) ||
		domain.PlanTransitionAllowed(domain.PlanRetired, domain.PlanActive) ||
		domain.PlanTransitionAllowed(domain.PlanDraft, domain.PlanRetired) {
		t.Fatal("plan transitions are wrong")
	}
	if !domain.EnrollmentTransitionAllowed(domain.EnrollmentActive, domain.EnrollmentSuspended) ||
		!domain.EnrollmentTransitionAllowed(domain.EnrollmentSuspended, domain.EnrollmentActive) ||
		domain.EnrollmentTransitionAllowed(domain.EnrollmentEnded, domain.EnrollmentActive) {
		t.Fatal("enrollment transitions are wrong")
	}
}

func TestCoversDateIsHalfOpen(t *testing.T) {
	from, to := date(t, "2026-01-01"), date(t, "2027-01-01")
	if !domain.CoversDate(from, to, *date(t, "2026-01-01")) {
		t.Fatal("the lower bound must be inclusive")
	}
	if !domain.CoversDate(from, to, *date(t, "2026-12-31")) {
		t.Fatal("the last day inside the period must be covered")
	}
	if domain.CoversDate(from, to, *date(t, "2027-01-01")) {
		t.Fatal("the upper bound must be exclusive")
	}
	if domain.CoversDate(from, to, *date(t, "2025-12-31")) {
		t.Fatal("a day before the period must not be covered")
	}
	if !domain.CoversDate(nil, nil, *date(t, "2050-06-01")) {
		t.Fatal("an unbounded period must cover every day")
	}
}

func validDefinitions() []domain.EntitlementDefinition {
	return []domain.EntitlementDefinition{
		{
			Code: "DENTAL", Name: "Diş", UnitType: "MONEY", CurrencyCode: "TRY",
			PeriodType: "CALENDAR_YEAR", InitialQuantity: "1500.500000", RolloverPolicy: "NONE",
		},
		{
			Code: "CHECKUP", Name: "Kontrol", UnitType: "COUNT", PeriodType: "ROLLING_DAYS",
			PeriodLength: intPtr(365), InitialQuantity: "2", RolloverPolicy: "CAPPED", RolloverCap: "1.000000",
		},
	}
}

func intPtr(v int) *int { return &v }

func TestValidateDefinitionsMirrorsDatabaseChecks(t *testing.T) {
	defs := validDefinitions()
	if err := domain.ValidateDefinitions(defs); err != nil {
		t.Fatalf("valid definitions rejected: %v", err)
	}
	if defs[0].InitialQuantity != "1500.5" || defs[1].RolloverCap != "1" {
		t.Fatalf("quantities were not normalised: %+v", defs)
	}

	bad := []struct {
		name string
		mut  func(d *domain.EntitlementDefinition)
		want string
	}{
		{"currency required for money", func(d *domain.EntitlementDefinition) { d.CurrencyCode = "" }, "items[0].currencyCode"},
		{"negative quantity", func(d *domain.EntitlementDefinition) { d.InitialQuantity = "-1" }, "items[0].initialQuantity"},
		{"unknown unit", func(d *domain.EntitlementDefinition) { d.UnitType = "BANANA" }, "items[0].unitType"},
	}
	for _, c := range bad {
		list := validDefinitions()[:1]
		c.mut(&list[0])
		err := domain.ValidateDefinitions(list)
		var ve *domain.ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("%s: expected a validation error, got %v", c.name, err)
		}
		found := false
		for _, f := range ve.Fields {
			if f.Field == c.want {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: %s not reported, got %+v", c.name, c.want, ve.Fields)
		}
	}

	// uq_entitlement_definition_code: the same code twice in one version.
	dup := validDefinitions()
	dup[1].Code = dup[0].Code
	err := domain.ValidateDefinitions(dup)
	var dupErr *domain.ValidationError
	if !errors.As(err, &dupErr) || dupErr.Fields[0].Field != "items[1].code" {
		t.Fatalf("a duplicate code must be reported on items[1].code, got %v", err)
	}

	// The rollover cap and the period length are mutually exclusive with their policies.
	list := validDefinitions()
	list[1].RolloverPolicy = "NONE"
	if err := domain.ValidateDefinitions(list); err == nil {
		t.Fatal("a cap without CAPPED must be refused (ck_entitlement_rollover_cap)")
	}
	list = validDefinitions()
	list[0].PeriodLength = intPtr(30)
	if err := domain.ValidateDefinitions(list); err == nil {
		t.Fatal("a period length outside ROLLING_DAYS must be refused (ck_entitlement_period_length)")
	}
}

func TestConfigurationHashIsStableAcrossReserialisation(t *testing.T) {
	from, to := date(t, "2026-01-01"), date(t, "2027-01-01")
	defs := validDefinitions()
	if err := domain.ValidateDefinitions(defs); err != nil {
		t.Fatal(err)
	}

	first, err := domain.ConfigurationHash(from, to, defs)
	if err != nil {
		t.Fatal(err)
	}
	// Same configuration, rows in the opposite order and quantities as the database
	// renders numeric(20,6): the hash must not move.
	reordered := []domain.EntitlementDefinition{defs[1], defs[0]}
	reordered[0].RolloverCap = "1.000000"
	reordered[1].InitialQuantity = "1500.500000"
	second, err := domain.ConfigurationHash(from, to, reordered)
	if err != nil {
		t.Fatal(err)
	}
	if domain.HexHash(first) != domain.HexHash(second) {
		t.Fatalf("hash changed across re-serialisation: %s != %s", domain.HexHash(first), domain.HexHash(second))
	}
	if len(first) != 32 {
		t.Fatalf("hash length = %d, want 32", len(first))
	}

	// A different validity period is a different configuration.
	other, err := domain.ConfigurationHash(from, date(t, "2028-01-01"), defs)
	if err != nil {
		t.Fatal(err)
	}
	if domain.HexHash(first) == domain.HexHash(other) {
		t.Fatal("the validity period is not part of the hash")
	}
}

func TestCanonicalConfigurationHasSortedKeysAndStringNumbers(t *testing.T) {
	defs := validDefinitions()
	if err := domain.ValidateDefinitions(defs); err != nil {
		t.Fatal(err)
	}
	raw, err := domain.CanonicalConfiguration(date(t, "2026-01-01"), nil, defs)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		ValidFrom   string  `json:"validFrom"`
		ValidTo     *string `json:"validTo"`
		Definitions []struct {
			Code            string  `json:"code"`
			InitialQuantity string  `json:"initialQuantity"`
			RolloverCap     *string `json:"rolloverCap"`
			PeriodLength    *string `json:"periodLength"`
		} `json:"definitions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("canonical form is not JSON: %v (%s)", err, raw)
	}
	if doc.ValidFrom != "2026-01-01" || doc.ValidTo != nil {
		t.Fatalf("period bounds = %q / %v", doc.ValidFrom, doc.ValidTo)
	}
	if len(doc.Definitions) != 2 || doc.Definitions[0].Code != "CHECKUP" {
		t.Fatalf("definitions are not sorted by code: %+v", doc.Definitions)
	}
	if doc.Definitions[0].InitialQuantity != "2" || doc.Definitions[1].InitialQuantity != "1500.5" {
		t.Fatalf("numbers are not trimmed strings: %+v", doc.Definitions)
	}
	if doc.Definitions[0].PeriodLength == nil || *doc.Definitions[0].PeriodLength != "365" {
		t.Fatalf("period length is not a string: %+v", doc.Definitions[0])
	}
	if doc.Definitions[1].RolloverCap != nil {
		t.Fatalf("an absent cap must be null: %+v", doc.Definitions[1])
	}
}
