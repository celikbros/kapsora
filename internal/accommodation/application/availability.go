package application

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/accommodation/domain"
	"github.com/celikbros/kapsora/internal/accommodation/settings"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/eligibility"
	contractapp "github.com/celikbros/kapsora/internal/contract/application"
	"github.com/celikbros/kapsora/internal/contract/selection"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/pricing"
	pricingapp "github.com/celikbros/kapsora/internal/pricing/application"
)

// maxSearchProperties bounds a region search. A region with more hotels than this is a
// region that needs a filter, not a page of four hundred quotes nobody will read.
const maxSearchProperties = 50

// The reasons a room type carries no quote. They are codes rather than sentences because
// a screen shows them next to a room and a report counts them; a guessed number would be
// worse than any of them.
const (
	// ReasonPriceNotFound is the selection's own PRICE_NOT_FOUND: no contracted price
	// covered one of the nights.
	ReasonPriceNotFound = string(selection.ReasonNotFound)
	// ReasonPriceAmbiguous is the selection's PRICE_AMBIGUOUS: two equally specific
	// prices tied and the system does not choose between them.
	ReasonPriceAmbiguous = string(selection.ReasonAmbiguous)
	// ReasonPriceFormulaUnknown is a FORMULA price whose key nothing resolved.
	ReasonPriceFormulaUnknown = pricing.ExplanationFormulaUnknown
	// ReasonCurrencyMismatch is two nights of one stay priced in two currencies. It is
	// its own code because it is a configuration error somebody has to fix, and adding
	// lira to euros to produce a total would be the one thing worse than saying so.
	ReasonCurrencyMismatch = "PRICE_CURRENCY_MISMATCH"
)

// unboundedBalance is the money cap handed to the calculation, and it never binds.
//
// internal/pricing caps what the payer carries at the line's available balance, which is
// right when the entitlement is measured in money. A room type's entitlement is measured
// in NIGHT: a member with two nights left has two nights left, not two lira, and capping a
// contract amount at the number 2 would quote a payer share of two lira for a room that
// costs a thousand. So the night entitlement is applied where it belongs -- the nights
// beyond it are priced as ineligible, and the plan carries nothing on them -- and the
// money cap is left wide open. A MONEY-unit entitlement takes the other path and passes
// its real balance.
var unboundedBalance = benefitdomain.MustQuantity("999999999999")

// oneNight is the quantity of one line: one room, one night.
var oneNight = benefitdomain.MustQuantity("1")

// SearchInput is one availability question.
type SearchInput struct {
	// PersonID is whose stay this is. It is resolved by the transport -- from the
	// caller's own PERSON grant for a member, from the request for a desk -- and is never
	// taken from the body for a caller that is bound to a person.
	PersonID   uuid.UUID
	CheckIn    time.Time
	CheckOut   time.Time
	Adults     int
	Children   int
	PropertyID *uuid.UUID
	RegionCode string
	// ProgramID narrows a member with two programs to one of them. It is honoured, never
	// trusted: a program the person is not enrolled in on the day selects nothing.
	ProgramID *uuid.UUID
}

// SearchResult is the whole answer.
type SearchResult struct {
	PersonID uuid.UUID
	CheckIn  time.Time
	CheckOut time.Time
	Nights   int
	// EvaluationID is the immutable eligibility evaluation this search was recorded as, so
	// "what did the system show them" is answerable later. It is nil only when the search
	// matched no room type at all and there was therefore no service to evaluate.
	EvaluationID *uuid.UUID
	Eligible     bool
	Entitlement  *EntitlementView
	Items        []SearchItem
}

// EntitlementView is what the plan has left for these rooms.
type EntitlementView struct {
	EntitlementCode string
	Unit            string
	// Remaining is an exact decimal string, like every quantity that leaves this system.
	Remaining string
}

// SearchItem is one room type of one property, with what is free and what it would cost.
type SearchItem struct {
	Property PropertyRecord
	RoomType AvailabilityRoomType
	// Available is the server's minimum daily availability over the range, and zero for a
	// room type that has no allotment on even one night of it.
	Available int
	Quote     *QuoteView
	// QuoteUnavailableReason is set exactly when Quote is nil.
	QuoteUnavailableReason string
}

// QuoteView is the contribution answer: what the stay costs, what the plan carries and
// what the member pays. Every figure is an exact decimal string produced by
// internal/pricing, summed once, and rounded once by the calculation that produced it.
type QuoteView struct {
	CurrencyCode   string
	NightlyAmounts []NightAmount
	TotalAmount    string
	PayerAmount    string
	MemberAmount   string
	// CoveredNights is how many nights of this stay the plan is applied to: the nights the
	// eligibility verdict marked eligible, counted here and nowhere else.
	//
	// It is a field on the quote rather than something a later caller re-derives, because
	// it is the same number twice over. It is what the search means when it shows a member
	// two of their three nights carried and the third as their own, and it is the quantity
	// WP-I6-02's hold reserves, its reservation request asks for and its authorization
	// promises. Two derivations of it would be two answers to "how much of this stay does
	// the plan pay for", and the member would have been shown one of them.
	CoveredNights int
}

// NightAmount is one night of the stay.
type NightAmount struct {
	StayDate     time.Time
	Amount       string
	PayerAmount  string
	MemberAmount string
}

// SearchAvailability answers what is free between two dates and what it would cost.
//
// The order is deliberate. The world is loaded and the rooms are counted first; the
// eligibility check runs second, over the services those rooms actually are, and writes the
// evaluation this search is remembered by; the prices are then computed per night and
// summed once. Nothing here reserves anything, moves a balance or touches an inventory
// row: an availability answer is an answer, and the hold is WP-I6-02's.
func (s *Service) SearchAvailability(ctx context.Context, rc identity.RequestContext,
	in SearchInput,
) (SearchResult, error) {
	if s.eligibility == nil {
		return SearchResult{}, fmt.Errorf("accommodation: the search needs an eligibility service")
	}
	if in.PersonID == uuid.Nil {
		return SearchResult{}, ErrPersonRequired
	}

	checkIn, checkOut := domain.Day(in.CheckIn), domain.Day(in.CheckOut)
	world := searchWorld{}
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		tenantSettings, err := settings.Load(ctx, tx, rc.TenantID)
		if err != nil {
			return err
		}
		nights, err := validateSearch(in, checkIn, checkOut, tenantSettings.MaxNights)
		if err != nil {
			return err
		}
		world.nights = nights
		world.stayDates = domain.StayDates(checkIn, nights)
		return s.loadWorld(ctx, tx, rc, in, checkIn, &world)
	})
	if err != nil {
		return SearchResult{}, err
	}

	out := SearchResult{
		PersonID: in.PersonID, CheckIn: checkIn, CheckOut: checkOut, Nights: world.nights,
		Items: make([]SearchItem, 0, len(world.roomTypes)),
	}
	if len(world.roomTypes) == 0 {
		return out, nil
	}

	verdict, err := s.checkEligibility(ctx, rc, in, checkIn, world)
	if err != nil {
		return SearchResult{}, err
	}
	out.EvaluationID = &verdict.evaluationID
	out.Entitlement = verdict.entitlement
	out.Eligible = verdict.eligibleForWholeStay(world.nights)

	for _, room := range world.roomTypes {
		property, found := world.propertyByID[room.PropertyID]
		if !found {
			continue
		}
		item := SearchItem{
			Property: property.Property, RoomType: room,
			Available: world.availabilityOf(room.ID),
		}
		quote, reason := s.quoteRoomType(world, property, room, verdict)
		item.Quote, item.QuoteUnavailableReason = quote, reason
		out.Items = append(out.Items, item)
	}

	if err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		return s.record(ctx, tx, rc, ActionAvailabilitySearch, ResourceProperty, uuid.Nil,
			map[string]any{
				"personId": in.PersonID.String(), "nights": world.nights,
				"roomTypes": len(out.Items), "evaluationId": verdict.evaluationID.String(),
			})
	}); err != nil {
		return SearchResult{}, err
	}
	return out, nil
}

// searchWorld is everything one search loaded, in one transaction and one point in time.
type searchWorld struct {
	nights       int
	stayDates    []time.Time
	properties   []AvailabilityProperty
	propertyByID map[uuid.UUID]AvailabilityProperty
	roomTypes    []AvailabilityRoomType
	inventory    map[uuid.UUID]InventorySummary
	// candidates are the contracted prices, grouped by the provider profile they belong
	// to, loaded once for every property and every night of the search.
	candidates map[uuid.UUID][]PriceCandidate
	// categoryPath and packages are per service definition, because the specificity
	// ladder scores a category price by how far above the definition the category sits.
	categoryPath map[uuid.UUID][]uuid.UUID
	packages     map[uuid.UUID][]uuid.UUID
}

// availabilityOf is the rule the whole search turns on: the minimum daily availability
// over the range, and zero for a room type that lacks an allotment on even one night.
//
// A room type with capacity on twenty-nine of thirty nights is not "mostly available". The
// guest would have nowhere to sleep on the thirtieth, so the answer for the stay is none.
func (w searchWorld) availabilityOf(roomTypeID uuid.UUID) int {
	summary, found := w.inventory[roomTypeID]
	if !found || summary.NightCount < w.nights {
		return 0
	}
	if summary.MinAvailable < 0 {
		return 0
	}
	return summary.MinAvailable
}

// loadWorld reads the properties, the room types, the allotment and the contracted prices
// of one search. It is one transaction on purpose: a hotel that vanished and a price that
// changed halfway through would produce an answer that was never true at any moment.
func (s *Service) loadWorld(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	in SearchInput, checkIn time.Time, world *searchWorld,
) error {
	lastNight := checkIn.AddDate(0, 0, world.nights-1)

	payers, err := s.repo.ListPersonProgramPayers(ctx, tx, rc.TenantID, in.PersonID,
		checkIn, in.ProgramID)
	if err != nil {
		return err
	}
	// A person with no active enrollment on the first night is narrowed to nothing rather
	// than widened to everything: an empty array is not "no filter", and reading it as one
	// would show a member every hotel in the tenant.
	if payers == nil {
		payers = []uuid.UUID{}
	}

	properties, err := s.repo.ListAvailabilityProperties(ctx, tx, rc.TenantID,
		AvailabilityPropertyQuery{
			PropertyID: in.PropertyID, RegionCode: in.RegionCode, ScopeIDs: scopeOf(rc),
			PayerOrganizationIDs: payers, CheckIn: checkIn, LastNight: lastNight,
			Limit: maxSearchProperties,
		})
	if err != nil {
		return err
	}
	world.properties = properties
	world.propertyByID = make(map[uuid.UUID]AvailabilityProperty, len(properties))
	propertyIDs := make([]uuid.UUID, 0, len(properties))
	profileIDs := make([]uuid.UUID, 0, len(properties))
	for _, p := range properties {
		world.propertyByID[p.Property.ID] = p
		propertyIDs = append(propertyIDs, p.Property.ID)
		profileIDs = append(profileIDs, p.ProviderProfileID)
	}
	if len(propertyIDs) == 0 {
		return nil
	}

	rooms, err := s.repo.ListRoomTypesForAvailability(ctx, tx, rc.TenantID, propertyIDs,
		in.Adults, in.Children, in.Adults+in.Children)
	if err != nil {
		return err
	}
	world.roomTypes = rooms
	if len(rooms) == 0 {
		return nil
	}

	roomIDs := make([]uuid.UUID, 0, len(rooms))
	definitionIDs := make([]uuid.UUID, 0, len(rooms))
	seenDefinition := make(map[uuid.UUID]bool, len(rooms))
	for _, r := range rooms {
		roomIDs = append(roomIDs, r.ID)
		if !seenDefinition[r.ServiceDefinitionID] {
			seenDefinition[r.ServiceDefinitionID] = true
			definitionIDs = append(definitionIDs, r.ServiceDefinitionID)
		}
	}

	world.inventory, err = s.repo.SummariseInventory(ctx, tx, rc.TenantID, roomIDs,
		checkIn, lastNight)
	if err != nil {
		return err
	}

	world.categoryPath = make(map[uuid.UUID][]uuid.UUID, len(definitionIDs))
	world.packages = make(map[uuid.UUID][]uuid.UUID, len(definitionIDs))
	categoryIDs := make([]uuid.UUID, 0, len(definitionIDs))
	packageIDs := make([]uuid.UUID, 0, len(definitionIDs))
	for _, definitionID := range definitionIDs {
		path, err := s.repo.CategoryPath(ctx, tx, rc.TenantID, definitionID)
		if err != nil {
			return err
		}
		containing, err := s.repo.PackagesContaining(ctx, tx, rc.TenantID, definitionID)
		if err != nil {
			return err
		}
		world.categoryPath[definitionID] = path
		world.packages[definitionID] = containing
		categoryIDs = append(categoryIDs, path...)
		packageIDs = append(packageIDs, containing...)
	}

	candidates, err := s.repo.ListPriceCandidates(ctx, tx, rc.TenantID, PriceCandidateQuery{
		ProviderProfileIDs: profileIDs, ServiceDefinitionIDs: definitionIDs,
		CategoryIDs: categoryIDs, PackageIDs: packageIDs,
		CheckIn: checkIn, LastNight: lastNight,
	})
	if err != nil {
		return err
	}
	world.candidates = make(map[uuid.UUID][]PriceCandidate, len(profileIDs))
	for _, candidate := range candidates {
		world.candidates[candidate.ProviderProfileID] =
			append(world.candidates[candidate.ProviderProfileID], candidate)
	}
	return nil
}

// eligibilityVerdict is the eligibility half of the answer.
type eligibilityVerdict struct {
	evaluationID uuid.UUID
	// eligibleFor says, per service definition, whether the person is eligible at all --
	// enrolled, on a published plan version, with the service mapped to an entitlement.
	eligibleFor map[uuid.UUID]bool
	// remainingNights is how many nights of the NIGHT entitlement are left, per service
	// definition. It is a count and not a sum of money.
	remainingNights map[uuid.UUID]int
	// remainingMoney is the balance of a MONEY-unit entitlement, for the tenant that
	// mapped a room night to a budget rather than to a night count.
	remainingMoney map[uuid.UUID]benefitdomain.Quantity
	entitlement    *EntitlementView
}

// eligibleForWholeStay is the one boolean the answer carries: at least one of the searched
// services is one this person may use, with enough of the plan left to cover every night of
// the stay. Two nights left and a three-night stay is not "eligible with a caveat" -- the
// third night is the member's to pay -- and the flag says so rather than letting a screen
// discover it in the figures.
func (v eligibilityVerdict) eligibleForWholeStay(nights int) bool {
	for definitionID, allowed := range v.eligibleFor {
		if !allowed {
			continue
		}
		if _, budget := v.remainingMoney[definitionID]; budget {
			// A money budget covers a night or it does not, and by how much is the
			// quote's answer rather than this flag's.
			return true
		}
		if v.remainingNights[definitionID] >= nights {
			return true
		}
	}
	return false
}

// checkEligibility runs one eligibility check for the whole search and keeps its
// evaluation id.
//
// The quantity asked about is one night rather than the length of the stay, and that is the
// point: the answer then separates "this person may not use this benefit at all" from
// "they may, and they have fewer nights left than they asked for". A check for the whole
// stay would fold the two into one INELIGIBLE and the quote could not tell a member which
// of their nights the plan carries.
func (s *Service) checkEligibility(ctx context.Context, rc identity.RequestContext,
	in SearchInput, checkIn time.Time, world searchWorld,
) (eligibilityVerdict, error) {
	definitionIDs := make([]uuid.UUID, 0, len(world.roomTypes))
	seen := make(map[uuid.UUID]bool, len(world.roomTypes))
	for _, room := range world.roomTypes {
		if seen[room.ServiceDefinitionID] {
			continue
		}
		seen[room.ServiceDefinitionID] = true
		definitionIDs = append(definitionIDs, room.ServiceDefinitionID)
	}

	items := make([]eligibility.RequestItem, 0, len(definitionIDs))
	for _, definitionID := range definitionIDs {
		items = append(items, eligibility.RequestItem{
			ServiceDefinitionID: definitionID, Quantity: oneNight,
		})
	}
	check := eligibility.CheckInput{
		PersonID: in.PersonID, ProgramID: in.ProgramID, ServiceDate: checkIn, Items: items,
		Context: map[string]any{"domain": "ACCOMMODATION"},
	}
	// A provider-scoped caller may only ask about its own organization, and the property
	// query has already bound the answer to that organization; naming it here is what lets
	// the eligibility service apply the same boundary it applies to a counter check.
	if scopes := scopeOf(rc); len(scopes) > 0 {
		organizationID := scopes[0]
		check.ProviderOrganizationID = &organizationID
	}

	result, err := s.eligibility.Check(ctx, rc, check)
	if err != nil {
		return eligibilityVerdict{}, err
	}

	unitByCode := make(map[string]string, len(result.Balances))
	for _, balance := range result.Balances {
		unitByCode[balance.EntitlementCode] = balance.Unit
	}

	verdict := eligibilityVerdict{
		evaluationID:    result.EvaluationID,
		eligibleFor:     make(map[uuid.UUID]bool, len(definitionIDs)),
		remainingNights: make(map[uuid.UUID]int, len(definitionIDs)),
		remainingMoney:  make(map[uuid.UUID]benefitdomain.Quantity, len(definitionIDs)),
	}
	for _, item := range result.Items {
		if item.Index < 0 || item.Index >= len(definitionIDs) {
			continue
		}
		definitionID := definitionIDs[item.Index]
		verdict.eligibleFor[definitionID] = item.Outcome == eligibility.ItemEligible
		if item.AvailableQuantity == nil || item.EntitlementCode == nil {
			continue
		}
		available, err := benefitdomain.ParseQuantity(string(*item.AvailableQuantity))
		if err != nil {
			return eligibilityVerdict{}, fmt.Errorf("accommodation: entitlement balance: %w", err)
		}
		code := *item.EntitlementCode
		unit := unitByCode[code]
		if unit == domain.UnitNight {
			verdict.remainingNights[definitionID] = wholeNights(available)
		} else {
			verdict.remainingMoney[definitionID] = available
		}
		if verdict.entitlement == nil {
			verdict.entitlement = &EntitlementView{
				EntitlementCode: code, Unit: unit, Remaining: available.String(),
			}
		}
	}
	return verdict, nil
}

// wholeNights is how many whole nights a balance carries. A half night is not a night
// somebody can sleep, so it truncates towards zero and never rounds up: a balance of 2.9
// is two nights the plan will carry, and telling a member it was three would be a promise
// the ledger cannot keep.
func wholeNights(q benefitdomain.Quantity) int {
	if q.IsNegative() {
		return 0
	}
	// The canonical decimal string is the exact value; taking its integer part is the
	// truncation, done on the text rather than through a float.
	text := q.String()
	if dot := strings.IndexByte(text, '.'); dot >= 0 {
		text = text[:dot]
	}
	nights, err := strconv.Atoi(text)
	if err != nil || nights < 0 {
		// A balance too large for an int is a configuration accident, not a member with
		// unlimited nights; the stay's own length is the most it could ever need.
		return maxCapacity
	}
	return nights
}

// quoteRoomType prices one room type over the stay and sums it once.
//
// One pricing.Item per night, one call to pricing.Calculate, and the totals it produced
// are the totals that go on the wire. The calculation rounds each line to the currency and
// takes the member's share as the difference, so payer + member equals the total exactly;
// summing the nights a second time here -- or rounding the sum -- would break that by a
// kuruş, and a kuruş is the difference between an invoice that reconciles and one that
// does not.
func (s *Service) quoteRoomType(world searchWorld, property AvailabilityProperty,
	room AvailabilityRoomType, verdict eligibilityVerdict,
) (*QuoteView, string) {
	eligible := verdict.eligibleFor[room.ServiceDefinitionID]
	coveredNights := verdict.remainingNights[room.ServiceDefinitionID]
	money, hasMoney := verdict.remainingMoney[room.ServiceDefinitionID]

	request := selection.Request{
		DefinitionID:       room.ServiceDefinitionID,
		CategoryPath:       world.categoryPath[room.ServiceDefinitionID],
		PackagesContaining: world.packages[room.ServiceDefinitionID],
	}
	if property.Property.LocationID != nil {
		request.LocationID = *property.Property.LocationID
	}

	items := make([]pricing.Item, 0, len(world.stayDates))
	currency := ""
	reason := ""
	// The nights the plan is applied to, counted as they are decided rather than inferred
	// afterwards from the figures. A night the plan covers whose split happens to leave the
	// payer nothing is still a night drawn from the count, and reading the count back off
	// `payerAmount` would silently stop being true for a contract with a 100 % member share.
	carried := 0
	for i, night := range world.stayDates {
		item := pricing.Item{LineNo: i + 1, Quantity: oneNight, Available: unboundedBalance}
		switch {
		case !eligible:
			item.Eligible = false
		case hasMoney:
			// A MONEY-unit entitlement is a budget: every night is eligible and the
			// calculation's own balance cap decides how far it reaches. The lines share
			// one account key so the pool is drawn down across the stay rather than
			// offered whole to each night.
			item.Eligible = true
			item.Available = money
			item.AccountKey = room.ServiceDefinitionID.String()
		default:
			// A NIGHT-unit entitlement is a count: the plan carries the first
			// `coveredNights` nights and the member carries the rest.
			item.Eligible = i < coveredNights
		}
		if item.Eligible {
			carried++
		}

		request.ServiceDate = night
		rows, details := applicableCandidates(world.candidates[property.ProviderProfileID], night)
		result := selection.Select(request, rows)
		if result.Winner == nil {
			item.NoPriceReason = string(result.Reason)
			if reason == "" {
				reason = string(result.Reason)
			}
			items = append(items, item)
			continue
		}
		detail := details[result.Winner.PriceItemID]
		price, err := priceOf(detail)
		if err != nil {
			if reason == "" {
				reason = ReasonPriceFormulaUnknown
			}
			item.NoPriceReason = ReasonPriceFormulaUnknown
			items = append(items, item)
			continue
		}
		switch {
		case currency == "":
			currency = detail.CurrencyCode
		case currency != detail.CurrencyCode:
			if reason == "" {
				reason = ReasonCurrencyMismatch
			}
		}
		item.Price = &price
		items = append(items, item)
	}

	if reason != "" {
		return nil, reason
	}
	if currency == "" {
		return nil, ReasonPriceNotFound
	}

	result := pricing.Calculate(items, pricingapp.MinorUnits(currency))
	if result.Outcome == pricing.OutcomeReviewRequired {
		// A line nobody could price makes the whole stay unquotable, and the calculation
		// has already zeroed the split rather than showing a number that is not an answer.
		return nil, unpriceableReason(result)
	}

	view := &QuoteView{
		CoveredNights:  carried,
		CurrencyCode:   currency,
		NightlyAmounts: make([]NightAmount, 0, len(result.Items)),
		TotalAmount:    result.Contract.String(),
		PayerAmount:    result.Payer.String(),
		MemberAmount:   result.Member.String(),
	}
	for i, line := range result.Items {
		night := world.stayDates[i]
		view.NightlyAmounts = append(view.NightlyAmounts, NightAmount{
			StayDate: night, Amount: line.Contract.String(),
			PayerAmount: line.Payer.String(), MemberAmount: line.Member.String(),
		})
	}
	return view, ""
}

// applicableCandidates narrows the loaded prices to the ones whose contract version was
// published over this night. The single-date query in db/queries/contract.sql does this in
// its WHERE; the range query cannot, because a stay may straddle two versions, so the
// filter is here and the specificity ladder that follows it is untouched.
func applicableCandidates(candidates []PriceCandidate, night time.Time) (
	[]selection.Candidate, map[uuid.UUID]contractapp.PriceDetail,
) {
	rows := make([]selection.Candidate, 0, len(candidates))
	details := make(map[uuid.UUID]contractapp.PriceDetail, len(candidates))
	for _, candidate := range candidates {
		if !candidate.VersionValidFrom.IsZero() && night.Before(candidate.VersionValidFrom) {
			continue
		}
		if !candidate.VersionValidTo.IsZero() && !night.Before(candidate.VersionValidTo) {
			continue
		}
		rows = append(rows, candidate.Candidate)
		details[candidate.Candidate.PriceItemID] = candidate.Detail
	}
	return rows, details
}

// unpriceableReason reads back why the calculation refused, so the room type carries the
// selection's own code rather than a generic one.
func unpriceableReason(result pricing.Result) string {
	for _, line := range result.Items {
		for _, explanation := range line.Explanations {
			switch explanation.Code {
			case pricing.ExplanationPriceNotFound, pricing.ExplanationPriceAmbiguous,
				pricing.ExplanationFormulaUnknown:
				return explanation.Code
			}
		}
	}
	return ReasonPriceNotFound
}

// priceOf maps the money on a winning price item onto the calculation's own Price. It is
// the same mapping internal/pricing/application performs, and it is repeated here rather
// than exported from there because exporting it would freeze a shape that belongs to that
// package's own quote.
func priceOf(d contractapp.PriceDetail) (pricing.Price, error) {
	amount, err := decimalOrZero(d.Amount, "amount")
	if err != nil {
		return pricing.Price{}, err
	}
	percent, err := decimalOrZero(d.Percent, "percent")
	if err != nil {
		return pricing.Price{}, err
	}
	shareAmount, err := decimalOrZero(d.MemberShareAmount, "memberShareAmount")
	if err != nil {
		return pricing.Price{}, err
	}
	sharePercent, err := decimalOrZero(d.MemberSharePercent, "memberSharePercent")
	if err != nil {
		return pricing.Price{}, err
	}
	minAmount, err := decimalPtr(d.MinAmount, "minAmount")
	if err != nil {
		return pricing.Price{}, err
	}
	maxAmount, err := decimalPtr(d.MaxAmount, "maxAmount")
	if err != nil {
		return pricing.Price{}, err
	}
	return pricing.Price{
		Method: pricing.Method(d.PricingMethod), Amount: amount, Percent: percent,
		MinAmount: minAmount, MaxAmount: maxAmount,
		ShareMethod: pricing.ShareMethod(d.MemberShareMethod),
		ShareAmount: shareAmount, SharePercent: sharePercent,
	}, nil
}

func decimalOrZero(raw, field string) (benefitdomain.Quantity, error) {
	if raw == "" {
		return benefitdomain.ZeroQuantity(), nil
	}
	q, err := benefitdomain.ParseQuantity(raw)
	if err != nil {
		return benefitdomain.ZeroQuantity(), fmt.Errorf("accommodation: price item %s: %w", field, err)
	}
	return q, nil
}

func decimalPtr(raw, field string) (*benefitdomain.Quantity, error) {
	if raw == "" {
		return nil, nil //nolint:nilnil // an absent bound is genuinely "no value, no error"
	}
	q, err := decimalOrZero(raw, field)
	if err != nil {
		return nil, err
	}
	return &q, nil
}

// validateSearch answers 422 before anything is read. The night arithmetic is the domain's
// and is not repeated here: `checkOut == checkIn` is a stay of no nights, and a stay longer
// than the tenant's accommodation.max_nights is refused with the number that refused it.
func validateSearch(in SearchInput, checkIn, checkOut time.Time, maxNights int) (int, error) {
	ve := &domain.ValidationError{}
	named := 0
	if in.PropertyID != nil && *in.PropertyID != uuid.Nil {
		named++
	}
	if in.RegionCode != "" {
		named++
	}
	if named != 1 {
		ve.Add("propertyId", "REQUIRED", "tesis veya bölge kodundan yalnız biri verilmeli")
	}
	if in.Adults < 1 || in.Adults > 20 {
		ve.Add("adults", "RANGE", "1-20 arasında olmalı")
	}
	if in.Children < 0 || in.Children > 20 {
		ve.Add("children", "RANGE", "0-20 arasında olmalı")
	}
	if checkIn.IsZero() {
		ve.Add("checkIn", "REQUIRED", "giriş tarihi zorunlu")
	}
	if checkOut.IsZero() {
		ve.Add("checkOut", "REQUIRED", "çıkış tarihi zorunlu")
	}
	if ve.Len() > 0 {
		return 0, ve
	}

	nights, err := domain.Nights(checkIn, checkOut)
	if err != nil {
		ve.Add("checkOut", "RANGE", "giriş tarihinden sonra olmalı")
		return 0, ve
	}
	if nights > maxNights {
		ve.Add("checkOut", "RANGE",
			fmt.Sprintf("en fazla %d gece sorgulanabilir", maxNights))
		return 0, ve
	}
	return nights, nil
}
