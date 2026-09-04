package selection_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/contract/selection"
)

var (
	serviceA  = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	serviceB  = uuid.MustParse("11111111-1111-4111-8111-111111111112")
	catLeaf   = uuid.MustParse("22222222-2222-4222-8222-222222222221")
	catMid    = uuid.MustParse("22222222-2222-4222-8222-222222222222")
	catRoot   = uuid.MustParse("22222222-2222-4222-8222-222222222223")
	packageA  = uuid.MustParse("33333333-3333-4333-8333-333333333331")
	locIzmir  = uuid.MustParse("44444444-4444-4444-8444-444444444441")
	locAnkara = uuid.MustParse("44444444-4444-4444-8444-444444444442")
)

func date(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func item(id string, target selection.Target, subject uuid.UUID) selection.Candidate {
	c := selection.Candidate{
		PriceItemID:  uuid.MustParse(id),
		ValidFrom:    date("2026-01-01"),
		ItemPriority: 100,
		ListPriority: 100,
		Target:       target,
	}
	switch target {
	case selection.TargetDefinition:
		c.DefinitionID = subject
	case selection.TargetPackage:
		c.PackageID = subject
	case selection.TargetCategory:
		c.CategoryID = subject
	case selection.TargetNone:
	}
	return c
}

func request() selection.Request {
	return selection.Request{
		ServiceDate:        date("2026-06-15"), // a Monday
		DefinitionID:       serviceA,
		CategoryPath:       []uuid.UUID{catLeaf, catMid, catRoot},
		PackagesContaining: []uuid.UUID{packageA},
	}
}

func id(n int) string {
	return uuid.NewSHA1(uuid.Nil, []byte{byte(n)}).String()
}

func TestDefinitionBeatsPackageAndCategory(t *testing.T) {
	res := selection.Select(request(), []selection.Candidate{
		item(id(1), selection.TargetCategory, catLeaf),
		item(id(2), selection.TargetPackage, packageA),
		item(id(3), selection.TargetDefinition, serviceA),
	})
	if res.Winner == nil {
		t.Fatalf("no winner: %s", res.Reason)
	}
	if res.Winner.Target != selection.TargetDefinition {
		t.Fatalf("winner target = %v, want definition", res.Winner.Target)
	}
}

func TestNearerCategoryBeatsFurtherOne(t *testing.T) {
	res := selection.Select(request(), []selection.Candidate{
		item(id(1), selection.TargetCategory, catRoot),
		item(id(2), selection.TargetCategory, catLeaf),
	})
	if res.Winner == nil {
		t.Fatalf("no winner: %s", res.Reason)
	}
	if res.Winner.CategoryID != catLeaf {
		t.Fatal("the nearest category must win")
	}
}

func TestLocationPriceBeatsProviderWidePriceOfTheSameTier(t *testing.T) {
	wide := item(id(1), selection.TargetDefinition, serviceA)
	local := item(id(2), selection.TargetDefinition, serviceA)
	local.LocationID = locIzmir

	req := request()
	req.LocationID = locIzmir
	res := selection.Select(req, []selection.Candidate{wide, local})
	if res.Winner == nil {
		t.Fatalf("no winner: %s", res.Reason)
	}
	if res.Winner.LocationID != locIzmir {
		t.Fatal("the location-specific price must win")
	}
}

// The location bonus must never lift a category price over a definition price: naming the
// exact service is the stronger statement of intent.
func TestDefinitionBeatsLocationSpecificCategory(t *testing.T) {
	definition := item(id(1), selection.TargetDefinition, serviceA)
	category := item(id(2), selection.TargetCategory, catLeaf)
	category.LocationID = locIzmir

	req := request()
	req.LocationID = locIzmir
	res := selection.Select(req, []selection.Candidate{category, definition})
	if res.Winner == nil {
		t.Fatalf("no winner: %s", res.Reason)
	}
	if res.Winner.Target != selection.TargetDefinition {
		t.Fatal("the definition price must win over a location-specific category price")
	}
}

func TestPriceForAnotherLocationDoesNotApply(t *testing.T) {
	elsewhere := item(id(1), selection.TargetDefinition, serviceA)
	elsewhere.LocationID = locAnkara

	req := request()
	req.LocationID = locIzmir
	res := selection.Select(req, []selection.Candidate{elsewhere})
	if res.Winner != nil {
		t.Fatal("a price tied to another location must not apply")
	}
	if res.Reason != selection.ReasonNotFound {
		t.Fatalf("reason = %s, want %s", res.Reason, selection.ReasonNotFound)
	}
	if res.Considered[0].Excluded != "LOCATION" {
		t.Fatalf("excluded = %q, want LOCATION", res.Considered[0].Excluded)
	}
}

func TestExpiredAndFuturePricesAreIgnored(t *testing.T) {
	expired := item(id(1), selection.TargetDefinition, serviceA)
	expired.ValidTo = date("2026-06-15") // half-open: the service date is already outside
	future := item(id(2), selection.TargetDefinition, serviceA)
	future.ValidFrom = date("2026-06-16")

	res := selection.Select(request(), []selection.Candidate{expired, future})
	if res.Reason != selection.ReasonNotFound {
		t.Fatalf("reason = %s, want %s", res.Reason, selection.ReasonNotFound)
	}
	for _, c := range res.Considered {
		if c.Excluded != "PERIOD" {
			t.Fatalf("excluded = %q, want PERIOD", c.Excluded)
		}
	}
}

func TestPeriodBoundaryIsHalfOpen(t *testing.T) {
	startsToday := item(id(1), selection.TargetDefinition, serviceA)
	startsToday.ValidFrom = date("2026-06-15")
	res := selection.Select(request(), []selection.Candidate{startsToday})
	if res.Winner == nil {
		t.Fatal("a price starting on the service date applies")
	}
}

func TestSeasonAndWeekdayNarrowTheList(t *testing.T) {
	winter := item(id(1), selection.TargetDefinition, serviceA)
	winter.SeasonFrom, winter.SeasonTo = date("2026-12-01"), date("2027-03-01")
	weekend := item(id(2), selection.TargetDefinition, serviceA)
	weekend.WeekdayMask = 1<<5 | 1<<6 // Saturday and Sunday
	weekday := item(id(3), selection.TargetDefinition, serviceA)
	weekday.WeekdayMask = 1 << 0 // Monday, and the service date is a Monday

	res := selection.Select(request(), []selection.Candidate{winter, weekend, weekday})
	if res.Winner == nil {
		t.Fatalf("no winner: %s", res.Reason)
	}
	if res.Winner.PriceItemID != weekday.PriceItemID {
		t.Fatal("only the Monday price applies on a Monday")
	}
}

func TestPriorityBreaksATieBeforeAmbiguity(t *testing.T) {
	low := item(id(1), selection.TargetDefinition, serviceA)
	high := item(id(2), selection.TargetDefinition, serviceA)
	high.ItemPriority = 200

	res := selection.Select(request(), []selection.Candidate{low, high})
	if res.Winner == nil {
		t.Fatalf("no winner: %s", res.Reason)
	}
	if res.Winner.PriceItemID != high.PriceItemID {
		t.Fatal("the higher item priority must win")
	}
}

func TestListPriorityBreaksATieWhenItemPrioritiesMatch(t *testing.T) {
	a := item(id(1), selection.TargetDefinition, serviceA)
	b := item(id(2), selection.TargetDefinition, serviceA)
	b.ListPriority = 500

	res := selection.Select(request(), []selection.Candidate{a, b})
	if res.Winner == nil {
		t.Fatalf("no winner: %s", res.Reason)
	}
	if res.Winner.PriceItemID != b.PriceItemID {
		t.Fatal("the higher list priority must win")
	}
}

// The rule this package exists for: equal specificity and equal priority is a
// configuration error, and the system says so instead of choosing.
func TestEqualSpecificityIsAmbiguousAndNamesBothCandidates(t *testing.T) {
	a := item(id(1), selection.TargetDefinition, serviceA)
	b := item(id(2), selection.TargetDefinition, serviceA)

	res := selection.Select(request(), []selection.Candidate{a, b})
	if res.Winner != nil {
		t.Fatal("two equally specific prices must not produce a winner")
	}
	if res.Reason != selection.ReasonAmbiguous {
		t.Fatalf("reason = %s, want %s", res.Reason, selection.ReasonAmbiguous)
	}
	if len(res.Tied) != 2 {
		t.Fatalf("tied = %d candidates, want 2", len(res.Tied))
	}
	seen := map[uuid.UUID]bool{}
	for _, c := range res.Tied {
		seen[c.PriceItemID] = true
	}
	if !seen[a.PriceItemID] || !seen[b.PriceItemID] {
		t.Fatal("both tied candidates must be named so an operator can fix one")
	}
}

func TestAmbiguityIsNotDeclaredWhenAThirdCandidateScoresLower(t *testing.T) {
	a := item(id(1), selection.TargetDefinition, serviceA)
	b := item(id(2), selection.TargetCategory, catLeaf)
	c := item(id(3), selection.TargetCategory, catMid)

	res := selection.Select(request(), []selection.Candidate{a, b, c})
	if res.Winner == nil || res.Winner.PriceItemID != a.PriceItemID {
		t.Fatal("the definition price wins outright")
	}
}

func TestNoCandidateForAnotherService(t *testing.T) {
	other := item(id(1), selection.TargetDefinition, serviceB)
	res := selection.Select(request(), []selection.Candidate{other})
	if res.Reason != selection.ReasonNotFound {
		t.Fatalf("reason = %s, want %s", res.Reason, selection.ReasonNotFound)
	}
}

func TestEmptyCandidateListIsNotFound(t *testing.T) {
	res := selection.Select(request(), nil)
	if res.Reason != selection.ReasonNotFound {
		t.Fatalf("reason = %s, want %s", res.Reason, selection.ReasonNotFound)
	}
	if res.Winner != nil {
		t.Fatal("no candidates, no winner")
	}
}

// The explanation a screen shows must not shuffle between two identical calls.
func TestConsideredOrderIsStable(t *testing.T) {
	candidates := []selection.Candidate{
		item(id(1), selection.TargetCategory, catRoot),
		item(id(2), selection.TargetDefinition, serviceA),
		item(id(3), selection.TargetCategory, catLeaf),
	}
	first := selection.Select(request(), candidates)
	second := selection.Select(request(), candidates)
	if len(first.Considered) != len(second.Considered) {
		t.Fatal("length changed between calls")
	}
	for i := range first.Considered {
		if first.Considered[i].Candidate.PriceItemID != second.Considered[i].Candidate.PriceItemID {
			t.Fatalf("order changed at %d", i)
		}
		if first.Considered[i].Score != second.Considered[i].Score {
			t.Fatalf("score changed at %d", i)
		}
	}
}

func TestUnlocatedRequestStillMatchesAProviderWidePrice(t *testing.T) {
	wide := item(id(1), selection.TargetDefinition, serviceA)
	res := selection.Select(request(), []selection.Candidate{wide})
	if res.Winner == nil {
		t.Fatalf("no winner: %s", res.Reason)
	}
}
