// Package selection answers one question: for this service, at this provider, on this
// date, which contracted price applies? It is a pure function over rows the repository
// has already loaded, so it can be tested exhaustively without a database.
//
// The rule it exists to enforce is v1.2 11.5: when two prices are equally specific the
// system does not choose. A random winner is a silent financial error — somebody is
// charged the wrong amount and nothing anywhere says so — and the only honest answer is
// to stop and say the configuration is ambiguous.
package selection

import (
	"sort"
	"time"

	"github.com/google/uuid"
)

// Reason explains an outcome that carries no price.
type Reason string

const (
	// ReasonNotFound: no candidate matched the request at all.
	ReasonNotFound Reason = "PRICE_NOT_FOUND"
	// ReasonAmbiguous: two or more candidates tied at the top. Configuration error.
	ReasonAmbiguous Reason = "PRICE_AMBIGUOUS"
)

// Target says what a price item is priced against. Exactly one is set on any row; the
// database CHECK on contract.price_item guarantees it.
type Target int

const (
	// TargetNone means the row named nothing, which the database forbids.
	TargetNone Target = iota
	// TargetDefinition prices one service definition.
	TargetDefinition
	// TargetPackage prices a bundle of services as a whole.
	TargetPackage
	// TargetCategory prices everything under a service category.
	TargetCategory
)

// Candidate is one contract.price_item with the context needed to score it. The
// repository fills these from a query that has already applied the cheap filters
// (published version, active contract, matching provider); everything left here is
// judgement rather than filtering.
type Candidate struct {
	PriceItemID       uuid.UUID
	PriceListID       uuid.UUID
	ContractVersionID uuid.UUID

	Target       Target
	DefinitionID uuid.UUID // set when Target is TargetDefinition
	PackageID    uuid.UUID // set when Target is TargetPackage
	CategoryID   uuid.UUID // set when Target is TargetCategory

	// LocationID restricts the price to one location; the zero value means every
	// location of the provider.
	LocationID uuid.UUID

	// The half-open period [ValidFrom, ValidTo) the item applies to. A zero ValidTo
	// means open-ended.
	ValidFrom time.Time
	ValidTo   time.Time

	// Season of the owning price list, half-open, zero values meaning all year.
	SeasonFrom time.Time
	SeasonTo   time.Time
	// WeekdayMask of the owning price list: bit 0 is Monday. Zero means every day.
	WeekdayMask uint8

	ItemPriority int
	ListPriority int
}

// Request is what is being priced.
type Request struct {
	ServiceDate time.Time
	// DefinitionID is the service being priced. Required.
	DefinitionID uuid.UUID
	// LocationID is where it will be delivered, if known.
	LocationID uuid.UUID
	// CategoryPath is the definition's own category first, then each ancestor up to the
	// root. A nearer ancestor is more specific than a further one.
	CategoryPath []uuid.UUID
	// PackagesContaining lists the packages that include the requested definition.
	PackagesContaining []uuid.UUID
}

// Scored is a candidate with the score it earned, so a screen can explain the choice
// rather than assert it.
type Scored struct {
	Candidate Candidate
	Score     int
	// Matched is false for a candidate that was considered and ruled out; the reason is
	// in Excluded.
	Matched  bool
	Excluded string
}

// Result is the outcome of one selection.
type Result struct {
	// Winner is set only when exactly one candidate scored highest.
	Winner *Candidate
	// Reason is set when Winner is nil.
	Reason Reason
	// Tied lists the candidates that tied at the top when Reason is ReasonAmbiguous.
	Tied []Candidate
	// Considered is every candidate with its score, winners and losers alike, in
	// descending score order. Ordering is stable by price item id so the explanation a
	// screen shows does not shuffle between calls.
	Considered []Scored
}

// Scores of the specificity ladder. The gaps between tiers are wider than the location
// bonus on purpose: a contract that names the exact service means that price for it, and
// a location-specific category price must not outrank it.
const (
	scoreDefinition   = 400
	scorePackage      = 300
	scoreCategoryBase = 200
	scoreLocation     = 50
)

// Select applies the ladder of WP-I3-03 2.3 and returns the single winning price, or a
// reason why there is none.
func Select(req Request, candidates []Candidate) Result {
	res := Result{Considered: make([]Scored, 0, len(candidates))}

	depth := make(map[uuid.UUID]int, len(req.CategoryPath))
	for i, id := range req.CategoryPath {
		// A category may appear once; the first (nearest) position wins if it somehow
		// appears twice, which would mean a cycle upstream.
		if _, seen := depth[id]; !seen {
			depth[id] = i
		}
	}
	packages := make(map[uuid.UUID]bool, len(req.PackagesContaining))
	for _, id := range req.PackagesContaining {
		packages[id] = true
	}

	for _, c := range candidates {
		score, excluded := score(req, c, depth, packages)
		res.Considered = append(res.Considered, Scored{
			Candidate: c, Score: score, Matched: excluded == "", Excluded: excluded,
		})
	}

	sort.SliceStable(res.Considered, func(i, j int) bool {
		a, b := res.Considered[i], res.Considered[j]
		if a.Matched != b.Matched {
			return a.Matched
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Candidate.ItemPriority != b.Candidate.ItemPriority {
			return a.Candidate.ItemPriority > b.Candidate.ItemPriority
		}
		if a.Candidate.ListPriority != b.Candidate.ListPriority {
			return a.Candidate.ListPriority > b.Candidate.ListPriority
		}
		// Not a tie-break: only a stable presentation order for two genuinely tied rows.
		return a.Candidate.PriceItemID.String() < b.Candidate.PriceItemID.String()
	})

	top := make([]Candidate, 0, 2)
	for _, s := range res.Considered {
		if !s.Matched {
			break
		}
		if len(top) == 0 {
			top = append(top, s.Candidate)
			continue
		}
		first := res.Considered[0]
		if s.Score == first.Score &&
			s.Candidate.ItemPriority == first.Candidate.ItemPriority &&
			s.Candidate.ListPriority == first.Candidate.ListPriority {
			top = append(top, s.Candidate)
			continue
		}
		break
	}

	switch len(top) {
	case 0:
		res.Reason = ReasonNotFound
	case 1:
		winner := top[0]
		res.Winner = &winner
	default:
		res.Reason = ReasonAmbiguous
		res.Tied = top
	}
	return res
}

// score returns the candidate's specificity, or the reason it does not apply at all.
func score(req Request, c Candidate, depth map[uuid.UUID]int, packages map[uuid.UUID]bool) (int, string) {
	if !withinPeriod(req.ServiceDate, c.ValidFrom, c.ValidTo) {
		return 0, "PERIOD"
	}
	if !withinSeason(req.ServiceDate, c.SeasonFrom, c.SeasonTo) {
		return 0, "SEASON"
	}
	if !matchesWeekday(req.ServiceDate, c.WeekdayMask) {
		return 0, "WEEKDAY"
	}
	// A price tied to one location applies only there. A price with no location applies
	// wherever the provider works, including a request that names no location.
	if c.LocationID != uuid.Nil && c.LocationID != req.LocationID {
		return 0, "LOCATION"
	}

	base := 0
	switch c.Target {
	case TargetDefinition:
		if c.DefinitionID != req.DefinitionID {
			return 0, "SERVICE"
		}
		base = scoreDefinition
	case TargetPackage:
		if !packages[c.PackageID] {
			return 0, "SERVICE"
		}
		base = scorePackage
	case TargetCategory:
		d, ok := depth[c.CategoryID]
		if !ok {
			return 0, "SERVICE"
		}
		base = scoreCategoryBase - d
	case TargetNone:
		return 0, "SERVICE"
	default:
		return 0, "SERVICE"
	}

	if c.LocationID != uuid.Nil {
		base += scoreLocation
	}
	return base, ""
}

// withinPeriod reports whether d falls in the half-open range [from, to). A zero `to` is
// open-ended; a zero `from` means the row has always applied.
func withinPeriod(d, from, to time.Time) bool {
	day := dateOf(d)
	if !from.IsZero() && day.Before(dateOf(from)) {
		return false
	}
	if !to.IsZero() && !day.Before(dateOf(to)) {
		return false
	}
	return true
}

// withinSeason is withinPeriod for the owning price list; both bounds are set together or
// not at all, which the database CHECK on contract.price_list guarantees.
func withinSeason(d, from, to time.Time) bool {
	if from.IsZero() && to.IsZero() {
		return true
	}
	return withinPeriod(d, from, to)
}

// matchesWeekday reports whether the mask covers d's weekday. Bit 0 is Monday, matching
// the mask stored on contract.price_list. A zero mask means every day.
func matchesWeekday(d time.Time, mask uint8) bool {
	if mask == 0 {
		return true
	}
	// time.Weekday counts Sunday as 0; the mask counts Monday as bit 0.
	bit := (int(d.Weekday()) + 6) % 7
	return mask&(1<<uint(bit)) != 0
}

func dateOf(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
