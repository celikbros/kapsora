package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/celikbros/kapsora/internal/provider/domain"
)

func day(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return d
}

func dayPtr(t *testing.T, s string) *time.Time {
	d := day(t, s)
	return &d
}

// TestProviderStatusMachine is the whole table of section 2.1: a provider is activated,
// suspended and reactivated while its contract runs, terminated once, and never moves
// again afterwards.
func TestProviderStatusMachine(t *testing.T) {
	legal := []struct{ from, to string }{
		{domain.StatusPending, domain.StatusActive},
		{domain.StatusPending, domain.StatusTerminated},
		{domain.StatusActive, domain.StatusSuspended},
		{domain.StatusActive, domain.StatusTerminated},
		{domain.StatusSuspended, domain.StatusActive},
		{domain.StatusSuspended, domain.StatusTerminated},
	}
	for _, tc := range legal {
		if !domain.TransitionAllowed(tc.from, tc.to) {
			t.Errorf("%s -> %s must be allowed", tc.from, tc.to)
		}
		if err := domain.ValidateTransition(tc.from, tc.to); err != nil {
			t.Errorf("ValidateTransition(%s, %s) = %v", tc.from, tc.to, err)
		}
	}

	illegal := []struct{ from, to string }{
		// Terminated is final: nothing leaves it, not even back to itself.
		{domain.StatusTerminated, domain.StatusActive},
		{domain.StatusTerminated, domain.StatusSuspended},
		{domain.StatusTerminated, domain.StatusTerminated},
		// A pending provider has never been active, so it cannot be suspended.
		{domain.StatusPending, domain.StatusSuspended},
		{domain.StatusPending, domain.StatusPending},
		// A command that changes nothing is a mistake, not a no-op.
		{domain.StatusActive, domain.StatusActive},
		{domain.StatusSuspended, domain.StatusSuspended},
		// Nothing ever returns to PENDING.
		{domain.StatusActive, domain.StatusPending},
		{domain.StatusSuspended, domain.StatusPending},
	}
	for _, tc := range illegal {
		if domain.TransitionAllowed(tc.from, tc.to) {
			t.Errorf("%s -> %s must be refused", tc.from, tc.to)
		}
		err := domain.ValidateTransition(tc.from, tc.to)
		if !errors.Is(err, domain.ErrTransitionInvalid) {
			t.Errorf("ValidateTransition(%s, %s) = %v, want ErrTransitionInvalid", tc.from, tc.to, err)
		}
		// The error names the move but carries no data beyond the two statuses.
		if !strings.Contains(err.Error(), tc.from) || !strings.Contains(err.Error(), tc.to) {
			t.Errorf("error %q does not name the refused move", err)
		}
	}
}

// TestResolveCapabilityTargets is the read-time tree walk: a capability covers a definition
// when it names the definition itself or any category above it, so a definition added under
// a covered category later needs no provider row to change.
func TestResolveCapabilityTargets(t *testing.T) {
	definition := "11111111-1111-1111-1111-111111111111"
	leaf := "22222222-2222-2222-2222-222222222222"
	mid := "33333333-3333-3333-3333-333333333333"
	root := "44444444-4444-4444-4444-444444444444"

	scope := domain.ResolveCapabilityTargets(definition, []string{leaf, mid, root})
	if scope.DefinitionID != definition {
		t.Fatalf("definition = %q", scope.DefinitionID)
	}
	if got := scope.CategoryIDs; len(got) != 3 || got[0] != leaf || got[1] != mid || got[2] != root {
		t.Fatalf("chain = %v, want leaf, mid, root in order", got)
	}
	if scope.Empty() {
		t.Fatal("a resolved scope is not empty")
	}

	// The chain arrives from a recursive query, so duplicates and empty ids are dropped
	// before it becomes an = ANY(...) filter.
	scope = domain.ResolveCapabilityTargets(definition, []string{leaf, "", mid, leaf, root, mid})
	if got := scope.CategoryIDs; len(got) != 3 || got[0] != leaf || got[1] != mid || got[2] != root {
		t.Fatalf("deduplicated chain = %v", got)
	}

	// A definition that does not exist has no chain at all, and an empty scope must never
	// be read as "every provider can do this".
	if !domain.ResolveCapabilityTargets(definition, nil).Empty() {
		t.Fatal("a definition with no category chain must resolve to an empty scope")
	}
	if !domain.ResolveCapabilityTargets("", []string{leaf}).Empty() {
		t.Fatal("a scope without a definition must be empty")
	}
}

func TestValidateCapabilitySetRefusesSelfContradiction(t *testing.T) {
	definition := "11111111-1111-1111-1111-111111111111"
	other := "55555555-5555-5555-5555-555555555555"
	category := "22222222-2222-2222-2222-222222222222"

	// Successive periods for the same target are fine: the range is half-open, so the day
	// one ends is the day the next begins.
	if err := domain.ValidateCapabilitySet([]domain.CapabilityInput{
		{ServiceDefinitionID: definition, ValidFrom: day(t, "2025-01-01"), ValidTo: dayPtr(t, "2026-01-01")},
		{ServiceDefinitionID: definition, ValidFrom: day(t, "2026-01-01")},
		{ServiceCategoryID: category, ValidFrom: day(t, "2026-01-01")},
		{ServiceDefinitionID: other, ValidFrom: day(t, "2026-01-01")},
	}); err != nil {
		t.Fatalf("successive periods refused: %v", err)
	}

	for name, items := range map[string][]domain.CapabilityInput{
		"same definition": {
			{ServiceDefinitionID: definition, ValidFrom: day(t, "2026-01-01")},
			{ServiceDefinitionID: definition, ValidFrom: day(t, "2026-06-01")},
		},
		"same category": {
			{ServiceCategoryID: category, ValidFrom: day(t, "2026-01-01"), ValidTo: dayPtr(t, "2027-01-01")},
			{ServiceCategoryID: category, ValidFrom: day(t, "2026-06-01")},
		},
	} {
		if err := domain.ValidateCapabilitySet(items); !errors.Is(err, domain.ErrCapabilityOverlap) {
			t.Errorf("%s: %v, want ErrCapabilityOverlap", name, err)
		}
	}

	// Exactly one target, and a period that runs forwards.
	fields := fieldCodes(t, domain.ValidateCapabilitySet([]domain.CapabilityInput{
		{ValidFrom: day(t, "2026-01-01")},
		{ServiceDefinitionID: definition, ServiceCategoryID: category, ValidFrom: day(t, "2026-01-01")},
		{ServiceDefinitionID: other, ValidFrom: day(t, "2026-06-01"), ValidTo: dayPtr(t, "2026-01-01")},
	}))
	if fields["items[0].serviceDefinitionId"] != "REQUIRED" {
		t.Errorf("a row with no target = %v", fields)
	}
	if fields["items[1].serviceCategoryId"] != "EXCLUSIVE" {
		t.Errorf("a row with both targets = %v", fields)
	}
	if fields["items[2].validTo"] != "PERIOD" {
		t.Errorf("an inverted period = %v", fields)
	}
}

func TestValidateAssignmentSetRefusesSelfContradiction(t *testing.T) {
	location := "11111111-1111-1111-1111-111111111111"
	other := "22222222-2222-2222-2222-222222222222"

	// The same practitioner may hold two roles at one location at once, and the same role
	// at two locations at once.
	if err := domain.ValidateAssignmentSet([]domain.AssignmentInput{
		{LocationID: location, Role: "ATTENDING", ValidFrom: day(t, "2026-01-01")},
		{LocationID: location, Role: "CONSULTANT", ValidFrom: day(t, "2026-01-01")},
		{LocationID: other, Role: "ATTENDING", ValidFrom: day(t, "2026-01-01")},
	}); err != nil {
		t.Fatalf("parallel roles refused: %v", err)
	}

	if err := domain.ValidateAssignmentSet([]domain.AssignmentInput{
		{LocationID: location, Role: "ATTENDING", ValidFrom: day(t, "2026-01-01"), ValidTo: dayPtr(t, "2026-07-01")},
		{LocationID: location, Role: "ATTENDING", ValidFrom: day(t, "2026-06-01")},
	}); !errors.Is(err, domain.ErrAssignmentOverlap) {
		t.Fatalf("overlapping spells = %v, want ErrAssignmentOverlap", err)
	}

	fields := fieldCodes(t, domain.ValidateAssignmentSet([]domain.AssignmentInput{
		{LocationID: location, Role: "SURGEON", ValidFrom: day(t, "2026-01-01")},
	}))
	if fields["items[0].role"] != "ENUM" {
		t.Fatalf("an unknown role = %v", fields)
	}
}

// TestRegistrationNumberHandling covers the three things that keep a professional identity
// number out of every readable surface: one normalized form for the index, a mask that is
// the only thing returned, and an index input qualified by the issuing body.
func TestRegistrationNumberHandling(t *testing.T) {
	for raw, want := range map[string]string{
		" 12-345 ":  "12345",
		"tt/9911":   "TT/9911",
		"12 34 56":  "123456",
		"\t99-11\t": "9911",
	} {
		if got := domain.NormalizeRegistrationNumber(raw); got != want {
			t.Errorf("NormalizeRegistrationNumber(%q) = %q, want %q", raw, got, want)
		}
	}

	number := domain.NormalizeRegistrationNumber("12-3456")
	if err := domain.ValidateRegistrationNumber(number); err != nil {
		t.Fatalf("a well formed number was refused: %v", err)
	}
	if err := domain.ValidateRegistrationNumber(""); err == nil {
		t.Fatal("an empty number was accepted")
	}
	if err := domain.ValidateRegistrationNumber("12 34"); err == nil {
		t.Fatal("an unnormalized number was accepted")
	}

	masked := domain.MaskRegistrationNumber(number)
	if masked == number {
		t.Fatal("the mask must not be the number")
	}
	if !strings.HasPrefix(masked, "12") || strings.Contains(masked, "3456") {
		t.Fatalf("mask = %q, want the first two characters and stars", masked)
	}

	// The issuing body qualifies the value, so the same digits in two registers can never
	// produce the same blind index.
	if domain.RegistrationIndexInput("TTB", number) == domain.RegistrationIndexInput("SB", number) {
		t.Fatal("two authorities must not share an index input")
	}
}

func TestPeriodsOverlapMirrorsTheHalfOpenRange(t *testing.T) {
	cases := []struct {
		name         string
		aFrom, bFrom string
		aTo, bTo     *time.Time
		want         bool
	}{
		{name: "both open", aFrom: "2026-01-01", bFrom: "2026-06-01", want: true},
		{name: "touching ends", aFrom: "2025-01-01", aTo: dayPtr(t, "2026-01-01"), bFrom: "2026-01-01", want: false},
		{name: "one day apart", aFrom: "2025-01-01", aTo: dayPtr(t, "2026-01-01"), bFrom: "2025-12-31", want: true},
		{
			name: "disjoint closed", aFrom: "2025-01-01", aTo: dayPtr(t, "2025-06-01"),
			bFrom: "2025-06-01", bTo: dayPtr(t, "2025-12-01"), want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.PeriodsOverlap(day(t, tc.aFrom), tc.aTo, day(t, tc.bFrom), tc.bTo)
			if got != tc.want {
				t.Fatalf("PeriodsOverlap = %v, want %v", got, tc.want)
			}
			// The relation is symmetric, like the && operator it mirrors.
			if back := domain.PeriodsOverlap(day(t, tc.bFrom), tc.bTo, day(t, tc.aFrom), tc.aTo); back != got {
				t.Fatalf("PeriodsOverlap is not symmetric: %v vs %v", got, back)
			}
		})
	}
}

func TestValidateNewLocationFillsColumnDefaults(t *testing.T) {
	in := domain.NewLocation{Code: "MERKEZ", Name: "Merkez şube"}
	if err := domain.ValidateNewLocation(&in); err != nil {
		t.Fatalf("valid location refused: %v", err)
	}
	if in.CountryCode != domain.DefaultCountry || in.Timezone != domain.DefaultTimezone {
		t.Fatalf("defaults = %q/%q", in.CountryCode, in.Timezone)
	}

	bad := domain.NewLocation{Code: "merkez", Name: "x"}
	fields := fieldCodes(t, domain.ValidateNewLocation(&bad))
	if fields["code"] != "FORMAT" || fields["name"] != "LENGTH" {
		t.Fatalf("field errors = %v", fields)
	}

	// A pin needs both halves, which is what ck_location_geo_pair enforces in the database.
	lat := 41.0
	if err := domain.ValidateCoordinatePair(&lat, nil); err == nil {
		t.Fatal("half a coordinate pair was accepted")
	}
	if err := domain.ValidateCoordinatePair(nil, nil); err != nil {
		t.Fatalf("no coordinates at all is fine: %v", err)
	}
}

func TestValidateNewProviderChecksTheContractPeriod(t *testing.T) {
	in := domain.NewProvider{
		TenantOrganizationID: "11111111-1111-1111-1111-111111111111", ProviderType: "HOSPITAL",
		ContractedFrom: dayPtr(t, "2026-06-01"), ContractedTo: dayPtr(t, "2026-01-01"),
	}
	if fieldCodes(t, domain.ValidateNewProvider(in))["contractedTo"] != "PERIOD" {
		t.Fatal("an inverted contract period was accepted")
	}
	in.ProviderType = "SPACESHIP"
	if fieldCodes(t, domain.ValidateNewProvider(in))["providerType"] != "ENUM" {
		t.Fatal("an unknown provider type was accepted")
	}
}

func fieldCodes(t *testing.T, err error) map[string]string {
	t.Helper()
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected a validation error, got %v", err)
	}
	out := make(map[string]string, len(ve.Fields))
	for _, fe := range ve.Fields {
		out[fe.Field] = fe.Code
	}
	return out
}
