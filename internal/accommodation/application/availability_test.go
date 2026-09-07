package application

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/accommodation/domain"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	contractapp "github.com/celikbros/kapsora/internal/contract/application"
	"github.com/celikbros/kapsora/internal/contract/selection"
)

// These tests exercise the two halves of a search that decide what a member is told: how
// many rooms are free over a range, and what the stay costs them. Neither half touches a
// database, so both can be walked exhaustively — which matters because the mistakes they
// exist to prevent are quiet ones. A room type sold for a range it has no allotment for is
// a guest with nowhere to sleep; a total summed twice is an invoice that is a kuruş out and
// nobody can say why.

func day(text string) time.Time {
	t, err := time.Parse(time.DateOnly, text)
	if err != nil {
		panic(err)
	}
	return t
}

// ---------------------------------------------------------------------------
// Availability
// ---------------------------------------------------------------------------

// TestAvailabilityIsTheMinimumAndZeroWithoutAnAllotment is the rule the search turns on.
//
// The two facts are deliberately separate: a room type with an allotment on every night but
// one has a healthy minimum and is not available for the stay. A reading that took only the
// minimum would sell it, and the guest would arrive to find one night unbooked.
func TestAvailabilityIsTheMinimumAndZeroWithoutAnAllotment(t *testing.T) {
	roomType := uuid.New()
	cases := []struct {
		name    string
		nights  int
		summary InventorySummary
		present bool
		want    int
	}{
		{name: "the minimum over the range", nights: 3, present: true,
			summary: InventorySummary{MinAvailable: 2, NightCount: 3}, want: 2},
		{name: "full on every night", nights: 3, present: true,
			summary: InventorySummary{MinAvailable: 0, NightCount: 3}, want: 0},
		{name: "an allotment on every night but one", nights: 30, present: true,
			summary: InventorySummary{MinAvailable: 9, NightCount: 29}, want: 0},
		{name: "no allotment at all", nights: 3, present: false, want: 0},
		{name: "one night of thirty allotted", nights: 30, present: true,
			summary: InventorySummary{MinAvailable: 40, NightCount: 1}, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			world := searchWorld{nights: tc.nights, inventory: map[uuid.UUID]InventorySummary{}}
			if tc.present {
				world.inventory[roomType] = tc.summary
			}
			if got := world.availabilityOf(roomType); got != tc.want {
				t.Errorf("availabilityOf = %d, want %d", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The quote
// ---------------------------------------------------------------------------

// quoteFixture is one room type of one property with a set of contracted prices behind it.
type quoteFixture struct {
	definition uuid.UUID
	property   AvailabilityProperty
	room       AvailabilityRoomType
	world      searchWorld
}

// newQuoteFixture builds a three-night stay against one FIXED price of 500 with a 20 %
// member share. The arithmetic that follows is the one from the pricing package's own
// example: the contract says 500 a night, the member's own share is 100 of it, and the plan
// carries the other 400 on every night it covers.
func newQuoteFixture(t *testing.T, nights int, prices ...PriceCandidate) *quoteFixture {
	t.Helper()
	definition := uuid.New()
	profile := uuid.New()
	propertyID := uuid.New()

	f := &quoteFixture{
		definition: definition,
		property: AvailabilityProperty{
			Property:          PropertyRecord{ID: propertyID, Code: "OTEL", Name: "Otel", Status: domain.StatusActive},
			ProviderProfileID: profile,
		},
		room: AvailabilityRoomType{
			ID: uuid.New(), PropertyID: propertyID, Code: "STD", Name: "Standart",
			MaxAdults: 2, MaxOccupancy: 3, ServiceDefinitionID: definition,
		},
	}
	if len(prices) == 0 {
		prices = []PriceCandidate{fixedPrice(definition, "500", "20", "TRY")}
	}
	f.world = searchWorld{
		nights:       nights,
		stayDates:    domain.StayDates(day("2026-06-15"), nights),
		candidates:   map[uuid.UUID][]PriceCandidate{profile: prices},
		categoryPath: map[uuid.UUID][]uuid.UUID{},
		packages:     map[uuid.UUID][]uuid.UUID{},
	}
	return f
}

// fixedPrice is one contracted price item naming the definition, valid for ever, with a
// percentage member share.
func fixedPrice(definition uuid.UUID, amount, sharePercent, currency string) PriceCandidate {
	return PriceCandidate{
		Candidate: selection.Candidate{
			PriceItemID: uuid.New(), PriceListID: uuid.New(), ContractVersionID: uuid.New(),
			Target: selection.TargetDefinition, DefinitionID: definition,
			ItemPriority: 100, ListPriority: 100,
		},
		Detail: contractapp.PriceDetail{
			CurrencyCode: currency, PricingMethod: "FIXED", Amount: amount,
			MemberShareMethod: "PERCENT", MemberSharePercent: sharePercent,
		},
	}
}

// verdictFor is an eligibility answer with a NIGHT entitlement of the given size.
func verdictFor(definition uuid.UUID, eligible bool, remainingNights int) eligibilityVerdict {
	return eligibilityVerdict{
		evaluationID:    uuid.New(),
		eligibleFor:     map[uuid.UUID]bool{definition: eligible},
		remainingNights: map[uuid.UUID]int{definition: remainingNights},
		remainingMoney:  map[uuid.UUID]benefitdomain.Quantity{},
	}
}

// TestQuoteSumsTheNightsOnceAndSplitsExactly is the money invariant of the whole package.
//
// Three nights at 500 with a 20 % member share, fully covered: the total is 1500, the member
// carries 300 and the plan 1200. Every figure is checked as an exact decimal string, the
// per-night figures are checked against the total, and the split is checked to add up — a
// sum taken twice, or a total rounded after the fact, breaks at least one of the three.
func TestQuoteSumsTheNightsOnceAndSplitsExactly(t *testing.T) {
	f := newQuoteFixture(t, 3)
	svc := &Service{}

	quote, reason := svc.quoteRoomType(f.world, f.property, f.room, verdictFor(f.definition, true, 3))
	if quote == nil {
		t.Fatalf("no quote: %s", reason)
	}
	if quote.CurrencyCode != "TRY" {
		t.Errorf("currency = %s, want TRY", quote.CurrencyCode)
	}
	if quote.TotalAmount != "1500" {
		t.Errorf("totalAmount = %s, want 1500", quote.TotalAmount)
	}
	if quote.PayerAmount != "1200" {
		t.Errorf("payerAmount = %s, want 1200", quote.PayerAmount)
	}
	if quote.MemberAmount != "300" {
		t.Errorf("memberAmount = %s, want 300", quote.MemberAmount)
	}
	assertSplitAddsUp(t, quote)

	if len(quote.NightlyAmounts) != 3 {
		t.Fatalf("got %d nightly amounts, want 3", len(quote.NightlyAmounts))
	}
	for i, night := range quote.NightlyAmounts {
		if night.Amount != "500" || night.PayerAmount != "400" || night.MemberAmount != "100" {
			t.Errorf("night %d = %s/%s/%s, want 500/400/100",
				i, night.Amount, night.PayerAmount, night.MemberAmount)
		}
	}
	// The nights are the stay's own nights, in order, and the last is the night before
	// check-out.
	if got := quote.NightlyAmounts[0].StayDate.Format(time.DateOnly); got != "2026-06-15" {
		t.Errorf("first night = %s, want 2026-06-15", got)
	}
	if got := quote.NightlyAmounts[2].StayDate.Format(time.DateOnly); got != "2026-06-17" {
		t.Errorf("last night = %s, want 2026-06-17", got)
	}
}

// TestQuoteRoundsEachNightOnceAndNeverTheTotal is the same invariant on an amount whose
// member share does not divide cleanly.
//
// A third of 333.33 is 111.11 to the kuruş with 0.001 left over. Rounding each night once
// and summing gives a total that its own parts add up to; rounding the sum a second time
// would leave payer + member a kuruş away from the total, on a screen a member is looking at
// before they commit to anything.
func TestQuoteRoundsEachNightOnceAndNeverTheTotal(t *testing.T) {
	definition := uuid.New()
	f := newQuoteFixture(t, 3, fixedPrice(definition, "333.335", "33.333", "TRY"))
	f.definition = definition
	f.room.ServiceDefinitionID = definition

	svc := &Service{}
	quote, reason := svc.quoteRoomType(f.world, f.property, f.room, verdictFor(definition, true, 3))
	if quote == nil {
		t.Fatalf("no quote: %s", reason)
	}
	assertSplitAddsUp(t, quote)

	// And the total is the nights added up, not the nights added up and then rounded.
	total := benefitdomain.ZeroQuantity()
	for _, night := range quote.NightlyAmounts {
		total = total.Add(benefitdomain.MustQuantity(night.Amount))
	}
	if total.String() != quote.TotalAmount {
		t.Errorf("totalAmount = %s but the nights sum to %s", quote.TotalAmount, total)
	}
}

// TestQuoteChargesTheMemberForNightsBeyondTheEntitlement is the NIGHT unit doing its work.
//
// The plan has two nights left and the member asked for three. The first two are carried by
// the plan at 400 each; the third is entirely theirs. The failure this guards against is the
// obvious one: treating a balance of "2" as two lira and capping the payer's share at it.
func TestQuoteChargesTheMemberForNightsBeyondTheEntitlement(t *testing.T) {
	f := newQuoteFixture(t, 3)
	svc := &Service{}

	quote, reason := svc.quoteRoomType(f.world, f.property, f.room, verdictFor(f.definition, true, 2))
	if quote == nil {
		t.Fatalf("no quote: %s", reason)
	}
	if quote.TotalAmount != "1500" {
		t.Errorf("totalAmount = %s, want 1500", quote.TotalAmount)
	}
	if quote.PayerAmount != "800" {
		t.Errorf("payerAmount = %s, want 800 — the plan carries two of three nights", quote.PayerAmount)
	}
	if quote.MemberAmount != "700" {
		t.Errorf("memberAmount = %s, want 700", quote.MemberAmount)
	}
	assertSplitAddsUp(t, quote)

	if quote.NightlyAmounts[2].PayerAmount != "0" {
		t.Errorf("the third night's payer share = %s, want 0",
			quote.NightlyAmounts[2].PayerAmount)
	}
	if quote.NightlyAmounts[2].MemberAmount != "500" {
		t.Errorf("the third night's member share = %s, want the whole 500",
			quote.NightlyAmounts[2].MemberAmount)
	}
}

// TestQuoteCarriesNothingForAnIneligiblePerson: a person the plan does not cover pays the
// whole stay, and the answer says so with figures rather than with an empty quote.
func TestQuoteCarriesNothingForAnIneligiblePerson(t *testing.T) {
	f := newQuoteFixture(t, 2)
	svc := &Service{}

	quote, reason := svc.quoteRoomType(f.world, f.property, f.room, verdictFor(f.definition, false, 10))
	if quote == nil {
		t.Fatalf("no quote: %s", reason)
	}
	if quote.PayerAmount != "0" {
		t.Errorf("payerAmount = %s, want 0", quote.PayerAmount)
	}
	if quote.MemberAmount != "1000" {
		t.Errorf("memberAmount = %s, want the whole 1000", quote.MemberAmount)
	}
	assertSplitAddsUp(t, quote)
}

// TestQuoteIsNullWhenANightIsAmbiguous is the refusal WP-I3-03 exists for, reaching the
// member's screen.
//
// Two equally specific contracted prices tie, the selection does not choose between them,
// and the room type carries no quote and the reason PRICE_AMBIGUOUS. A random winner would
// be a silent financial error: somebody is charged the wrong amount and nothing anywhere
// says so.
func TestQuoteIsNullWhenANightIsAmbiguous(t *testing.T) {
	definition := uuid.New()
	f := newQuoteFixture(t, 2,
		fixedPrice(definition, "500", "20", "TRY"),
		fixedPrice(definition, "700", "20", "TRY"))
	f.definition = definition
	f.room.ServiceDefinitionID = definition

	svc := &Service{}
	quote, reason := svc.quoteRoomType(f.world, f.property, f.room, verdictFor(definition, true, 5))
	if quote != nil {
		t.Fatalf("a tie produced a quote of %s; the system must not choose between two "+
			"equally specific prices", quote.TotalAmount)
	}
	if reason != ReasonPriceAmbiguous {
		t.Errorf("reason = %s, want %s", reason, ReasonPriceAmbiguous)
	}
}

// TestQuoteIsNullWhenOneNightHasNoPrice: a price that stops before the last night leaves the
// stay unquotable as a whole. Quoting the nights that were priced and staying silent about
// the rest would be a number a member would read as the answer.
func TestQuoteIsNullWhenOneNightHasNoPrice(t *testing.T) {
	definition := uuid.New()
	price := fixedPrice(definition, "500", "20", "TRY")
	// Valid up to but not including the third night of the stay.
	price.Candidate.ValidTo = day("2026-06-17")
	f := newQuoteFixture(t, 3, price)
	f.definition = definition
	f.room.ServiceDefinitionID = definition

	svc := &Service{}
	quote, reason := svc.quoteRoomType(f.world, f.property, f.room, verdictFor(definition, true, 5))
	if quote != nil {
		t.Fatalf("a stay with an unpriced night produced a quote of %s", quote.TotalAmount)
	}
	if reason != ReasonPriceNotFound {
		t.Errorf("reason = %s, want %s", reason, ReasonPriceNotFound)
	}
}

// TestQuoteIsNullWhenTheContractVersionDoesNotCoverTheNight proves the per-night version
// filter is doing something. The candidate is loaded for the range, and the night outside
// its published period must not be priced by it.
func TestQuoteIsNullWhenTheContractVersionDoesNotCoverTheNight(t *testing.T) {
	definition := uuid.New()
	price := fixedPrice(definition, "500", "20", "TRY")
	price.VersionValidFrom = day("2026-06-15")
	price.VersionValidTo = day("2026-06-17")
	f := newQuoteFixture(t, 3, price)
	f.definition = definition
	f.room.ServiceDefinitionID = definition

	svc := &Service{}
	quote, reason := svc.quoteRoomType(f.world, f.property, f.room, verdictFor(definition, true, 5))
	if quote != nil {
		t.Fatalf("the third night was priced by a version that had already ended: %s",
			quote.TotalAmount)
	}
	if reason != ReasonPriceNotFound {
		t.Errorf("reason = %s, want %s", reason, ReasonPriceNotFound)
	}
}

// TestQuoteIsNullWhenTwoNightsAreInDifferentCurrencies: adding lira to euros to produce a
// total is the one thing worse than saying the configuration is wrong.
func TestQuoteIsNullWhenTwoNightsAreInDifferentCurrencies(t *testing.T) {
	definition := uuid.New()
	lira := fixedPrice(definition, "500", "0", "TRY")
	lira.Candidate.ValidTo = day("2026-06-16")
	euro := fixedPrice(definition, "50", "0", "EUR")
	euro.Candidate.ValidFrom = day("2026-06-16")

	f := newQuoteFixture(t, 2, lira, euro)
	f.definition = definition
	f.room.ServiceDefinitionID = definition

	svc := &Service{}
	quote, reason := svc.quoteRoomType(f.world, f.property, f.room, verdictFor(definition, true, 5))
	if quote != nil {
		t.Fatalf("two currencies produced one total of %s %s", quote.TotalAmount, quote.CurrencyCode)
	}
	if reason != ReasonCurrencyMismatch {
		t.Errorf("reason = %s, want %s", reason, ReasonCurrencyMismatch)
	}
}

// assertSplitAddsUp is the invariant every quote carries: the payer's share plus the
// member's share is exactly the total, and the same holds night by night.
func assertSplitAddsUp(t *testing.T, quote *QuoteView) {
	t.Helper()
	total := benefitdomain.MustQuantity(quote.TotalAmount)
	payer := benefitdomain.MustQuantity(quote.PayerAmount)
	member := benefitdomain.MustQuantity(quote.MemberAmount)
	if payer.Add(member).Cmp(total) != 0 {
		t.Errorf("payer %s + member %s != total %s", payer, member, total)
	}
	nightTotal := benefitdomain.ZeroQuantity()
	nightPayer := benefitdomain.ZeroQuantity()
	nightMember := benefitdomain.ZeroQuantity()
	for i, night := range quote.NightlyAmounts {
		amount := benefitdomain.MustQuantity(night.Amount)
		p := benefitdomain.MustQuantity(night.PayerAmount)
		m := benefitdomain.MustQuantity(night.MemberAmount)
		if p.Add(m).Cmp(amount) != 0 {
			t.Errorf("night %d: payer %s + member %s != %s", i, p, m, amount)
		}
		nightTotal = nightTotal.Add(amount)
		nightPayer = nightPayer.Add(p)
		nightMember = nightMember.Add(m)
	}
	if nightTotal.Cmp(total) != 0 {
		t.Errorf("the nights sum to %s, the quote says %s", nightTotal, total)
	}
	if nightPayer.Cmp(payer) != 0 {
		t.Errorf("the nights' payer shares sum to %s, the quote says %s", nightPayer, payer)
	}
	if nightMember.Cmp(member) != 0 {
		t.Errorf("the nights' member shares sum to %s, the quote says %s", nightMember, member)
	}
}

// ---------------------------------------------------------------------------
// The eligibility verdict
// ---------------------------------------------------------------------------

func TestEligibleForWholeStayNeedsEnoughNights(t *testing.T) {
	definition := uuid.New()
	if !verdictFor(definition, true, 3).eligibleForWholeStay(3) {
		t.Error("three nights left over a three-night stay is not eligible")
	}
	if verdictFor(definition, true, 2).eligibleForWholeStay(3) {
		t.Error("two nights left over a three-night stay reads as eligible; the third " +
			"night is the member's to pay and the flag has to say so")
	}
	if verdictFor(definition, false, 30).eligibleForWholeStay(3) {
		t.Error("a person the plan does not cover reads as eligible")
	}
}

func TestWholeNightsTruncatesAndNeverRoundsUp(t *testing.T) {
	cases := []struct {
		balance string
		want    int
	}{
		{"0", 0}, {"0.999999", 0}, {"1", 1}, {"2.9", 2}, {"30", 30}, {"-5", 0},
	}
	for _, tc := range cases {
		if got := wholeNights(benefitdomain.MustQuantity(tc.balance)); got != tc.want {
			t.Errorf("wholeNights(%s) = %d, want %d — a fraction of a night is not a "+
				"night anybody can sleep", tc.balance, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// TestValidateSearchRefusesTheBoundaries walks the refusals of the work package: a stay of
// no nights, a stay longer than the tenant allows, and a search that names neither or both
// of the two ways of saying where.
func TestValidateSearchRefusesTheBoundaries(t *testing.T) {
	propertyID := uuid.New()
	base := SearchInput{Adults: 2, PropertyID: &propertyID}

	t.Run("checkOut equal to checkIn", func(t *testing.T) {
		in := base
		_, err := validateSearch(in, day("2026-06-15"), day("2026-06-15"), 30)
		assertFieldError(t, err, "checkOut")
	})
	t.Run("checkOut before checkIn", func(t *testing.T) {
		in := base
		_, err := validateSearch(in, day("2026-06-16"), day("2026-06-15"), 30)
		assertFieldError(t, err, "checkOut")
	})
	t.Run("thirty nights is allowed", func(t *testing.T) {
		in := base
		nights, err := validateSearch(in, day("2026-06-01"), day("2026-07-01"), 30)
		if err != nil {
			t.Fatalf("thirty nights refused: %v", err)
		}
		if nights != 30 {
			t.Errorf("nights = %d, want 30", nights)
		}
	})
	t.Run("thirty one nights is refused", func(t *testing.T) {
		in := base
		_, err := validateSearch(in, day("2026-06-01"), day("2026-07-02"), 30)
		assertFieldError(t, err, "checkOut")
	})
	t.Run("the tenant's own maximum is what refuses", func(t *testing.T) {
		in := base
		if _, err := validateSearch(in, day("2026-06-01"), day("2026-06-08"), 14); err != nil {
			t.Fatalf("seven nights refused under a maximum of fourteen: %v", err)
		}
		_, err := validateSearch(in, day("2026-06-01"), day("2026-06-08"), 5)
		assertFieldError(t, err, "checkOut")
	})
	t.Run("neither property nor region", func(t *testing.T) {
		in := SearchInput{Adults: 2}
		_, err := validateSearch(in, day("2026-06-15"), day("2026-06-16"), 30)
		assertFieldError(t, err, "propertyId")
	})
	t.Run("both property and region", func(t *testing.T) {
		in := SearchInput{Adults: 2, PropertyID: &propertyID, RegionCode: "ANTALYA"}
		_, err := validateSearch(in, day("2026-06-15"), day("2026-06-16"), 30)
		assertFieldError(t, err, "propertyId")
	})
	t.Run("a party of nobody", func(t *testing.T) {
		in := SearchInput{Adults: 0, PropertyID: &propertyID}
		_, err := validateSearch(in, day("2026-06-15"), day("2026-06-16"), 30)
		assertFieldError(t, err, "adults")
	})
}

func assertFieldError(t *testing.T, err error, field string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a validation error naming %s, got none", field)
	}
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected a *domain.ValidationError, got %T: %v", err, err)
	}
	for _, f := range ve.Fields {
		if f.Field == field {
			return
		}
	}
	t.Fatalf("no field error on %s; got %v", field, ve.Fields)
}

// ---------------------------------------------------------------------------
// The allotment
// ---------------------------------------------------------------------------

// TestFirstBelowCommitmentNamesTheEarliestOffendingNight. A provider opening ninety nights
// needs to know which night to look at, and "which" means the first one — not whichever the
// database happened to reach first.
func TestFirstBelowCommitmentNamesTheEarliestOffendingNight(t *testing.T) {
	// Deliberately not in date order, because the answer must not depend on the order the
	// rows arrived in: the night a provider is told to look at is the earliest wrong one.
	rows := []InventoryCommitment{
		{StayDate: day("2026-06-17"), Capacity: 10, Held: 6, Confirmed: 3},
		{StayDate: day("2026-06-15"), Capacity: 10, Held: 1, Confirmed: 1},
		{StayDate: day("2026-06-16"), Capacity: 10, Held: 2, Confirmed: 3},
	}
	// A capacity of 5 is fine on the first night (2 committed), fine on the second (5), and
	// short on the third (9).
	offender := firstBelowCommitment(rows, 5)
	if offender == nil {
		t.Fatal("a capacity of 5 under a commitment of 9 was accepted")
	}
	if got := offender.StayDate.Format(time.DateOnly); got != "2026-06-17" {
		t.Errorf("offending date = %s, want 2026-06-17", got)
	}
	if offender.Held != 6 || offender.Confirmed != 3 {
		t.Errorf("the refusal carries held=%d confirmed=%d, want 6 and 3",
			offender.Held, offender.Confirmed)
	}
	// And a capacity that clears every night is accepted.
	if firstBelowCommitment(rows, 9) != nil {
		t.Error("a capacity of 9 was refused; the largest commitment is exactly 9")
	}
	// The earliest offender wins, not the largest: 4 is short on the second night too.
	if got := firstBelowCommitment(rows, 4).StayDate.Format(time.DateOnly); got != "2026-06-16" {
		t.Errorf("offending date = %s, want the earliest, 2026-06-16", got)
	}
}

// TestInventoryRangeFillsTheGaps. Every date of the range gets an entry, and a night with no
// row is `allotted: false` rather than absent — the difference between "this provider has
// opened nothing here" and "there is nothing free", which mean different things to the
// person deciding what to open next.
func TestInventoryRangeFillsTheGaps(t *testing.T) {
	roomType := uuid.New()
	rows := []InventoryDayRecord{
		{StayDate: day("2026-06-15"), Capacity: 10, Held: 1, Confirmed: 2, Available: 7,
			UpdatedAt: time.Now().UTC(), RowVersion: 3},
		{StayDate: day("2026-06-17"), Capacity: 5, Held: 0, Confirmed: 0, Available: 5,
			UpdatedAt: time.Now().UTC(), RowVersion: 1},
	}
	out := inventoryRange(roomType, day("2026-06-15"), day("2026-06-18"), rows)
	if len(out.Days) != 4 {
		t.Fatalf("got %d days for a four-day range", len(out.Days))
	}
	want := []struct {
		date     string
		allotted bool
		capacity int
	}{
		{"2026-06-15", true, 10},
		{"2026-06-16", false, 0},
		{"2026-06-17", true, 5},
		{"2026-06-18", false, 0},
	}
	for i, w := range want {
		got := out.Days[i]
		if got.StayDate.Format(time.DateOnly) != w.date {
			t.Fatalf("day %d = %s, want %s", i, got.StayDate.Format(time.DateOnly), w.date)
		}
		if got.Allotted != w.allotted {
			t.Errorf("%s allotted = %t, want %t", w.date, got.Allotted, w.allotted)
		}
		if got.Capacity != w.capacity {
			t.Errorf("%s capacity = %d, want %d", w.date, got.Capacity, w.capacity)
		}
		if !got.Allotted && got.RowVersion != 0 {
			t.Errorf("%s has no row and yet carries a row version", w.date)
		}
	}
}

func TestValidatePutInventoryRefusesTheImpossible(t *testing.T) {
	if err := validatePutInventory(day("2026-06-16"), day("2026-06-15"), 5); err == nil {
		t.Error("a range that ends before it starts was accepted")
	}
	if err := validatePutInventory(day("2026-06-15"), day("2026-06-15"), 5); err != nil {
		t.Errorf("a single night was refused: %v", err)
	}
	if err := validatePutInventory(day("2026-01-01"), day("2029-01-01"), 5); err == nil {
		t.Error("a range of three years was accepted; a mistyped year should be a field error")
	}
	if err := validatePutInventory(day("2026-06-15"), day("2026-06-20"), -1); err == nil {
		t.Error("a negative capacity was accepted")
	}
	if err := validatePutInventory(day("2026-06-15"), day("2026-06-20"), 0); err != nil {
		t.Errorf("a capacity of zero was refused: %v — closing a season is a decision too", err)
	}
}
