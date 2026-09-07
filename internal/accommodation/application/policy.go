package application

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/accommodation/domain"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
)

// The cancellation arithmetic, in one file, judged by one document.
//
// **Nothing here reads a contract.** Every figure comes out of the policy the booking froze
// at confirmation and the amounts the booking's own nights carry. That is the whole point of
// the package: a hotel that rewrote its cancellation terms this morning changes what the
// next booking costs to cancel and does not change what this one costs, and a function that
// could reach a contract table would be a function through which that could stop being true.
//
// **Nothing here goes through a float.** The nights are integers, the amounts are exact
// decimals, and `payerFee + memberFee = feeAmount` holds by construction because the two
// halves are summed from the same rows rather than one being derived from the other by a
// percentage of a percentage.

// The penalty kinds of contract.lodging_terms, as this package reads them out of the frozen
// snapshot. They are spelled here as well as in internal/contract/domain because this is a
// reader of a stored document rather than a caller of that package: a snapshot written a
// year ago has to be readable by whatever the terms vocabulary has become since.
const (
	penaltyKindNights  = "NIGHTS"
	penaltyKindPercent = "PERCENT"
)

// LodgingPolicy is the frozen cancellation policy as this package reads it back. The field
// names are the wire shape the contract module wrote (kapsorav1.LodgingPolicySnapshot), so
// the JSON in the column is, by construction, the JSON this reads.
//
// It is a reader's struct and not a copy of the contract's: it names only the four fields a
// cancellation and a no-show are decided by, and a snapshot carrying fields this does not
// know is read successfully rather than refused. A booking made under a policy that has
// since grown a clause must still be cancellable.
type LodgingPolicy struct {
	ContractVersionID uuid.UUID `json:"contractVersionId"`
	SnapshotAt        time.Time `json:"snapshotAt"`
	// TimeZone is the property's own IANA zone, and it is what the free-cancellation hours
	// are counted against. A window measured on the reader's clock would be a fee that
	// depended on who was looking at it.
	TimeZone string `json:"timezone"`
	// FreeCancellationHoursBefore is how many hours before arrival a cancellation is still
	// free. Zero is a policy too: the free window closes at check-in.
	FreeCancellationHoursBefore int    `json:"freeCancellationHoursBefore"`
	PenaltyKind                 string `json:"penaltyKind"`
	// PenaltyNights is present exactly when PenaltyKind is NIGHTS.
	PenaltyNights *int `json:"penaltyNights,omitempty"`
	// PenaltyPercent is present exactly when PenaltyKind is PERCENT, as an exact decimal
	// string of the member's own share.
	PenaltyPercent *string `json:"penaltyPercent,omitempty"`
	// NoShowPercent is what a confirmed no-show costs, as a percentage of the member's
	// share, as an exact decimal string.
	NoShowPercent string `json:"noShowPercent"`
}

// DecodeLodgingPolicy reads a booking's frozen policy back. A booking with none is refused
// rather than defaulted: judging a cancellation by today's contract is exactly what the
// snapshot exists to prevent, and a policy invented here would be a fee invented here.
func DecodeLodgingPolicy(raw json.RawMessage) (LodgingPolicy, error) {
	if len(raw) == 0 {
		return LodgingPolicy{}, ErrPolicySnapshotMissing
	}
	var out LodgingPolicy
	if err := json.Unmarshal(raw, &out); err != nil {
		return LodgingPolicy{}, fmt.Errorf("accommodation: read frozen policy: %w", err)
	}
	if out.PenaltyKind == "" {
		return LodgingPolicy{}, ErrPolicySnapshotMissing
	}
	return out, nil
}

// ArrivalMoment is the instant the free-cancellation window is measured back from: the start
// of the arrival day on the property's own calendar.
//
// It is the building's calendar and not the server's, and that is the single reason the zone
// is in the snapshot at all. A member cancelling a Berlin hotel at 23:30 Istanbul time on
// the second day before arrival is inside a 48-hour window there and outside it here, and
// only one of those two answers is the one they agreed to.
func (p LodgingPolicy) ArrivalMoment(checkIn time.Time) time.Time {
	loc, err := domain.LoadLocation(p.TimeZone)
	if err != nil {
		// A zone this server cannot resolve. UTC is the honest fallback -- it is what the
		// dates in this vertical are stored as -- and it is a configuration fault rather
		// than a reason a member cannot cancel their booking at all.
		loc = time.UTC
	}
	day := domain.Day(checkIn)
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
}

// FreeUntil is the last instant a cancellation of this stay costs nothing.
func (p LodgingPolicy) FreeUntil(checkIn time.Time) time.Time {
	return p.ArrivalMoment(checkIn).Add(-time.Duration(p.FreeCancellationHoursBefore) * time.Hour)
}

// CancellationQuote is what a cancellation now would cost and give back. The preview answers
// with it and the command writes exactly the same figures, because both call the same
// function on the same frozen document.
type CancellationQuote struct {
	Free           bool
	PenaltyNights  int
	FeeAmount      string
	PayerFee       string
	MemberFee      string
	ReleasedNights int
	CurrencyCode   string
	// FreeUntil is the moment the free window closed or closes, so a member looking at a
	// fee can see what they missed rather than being told a number.
	FreeUntil time.Time
}

// QuoteCancellation is the whole of the cancellation arithmetic.
//
// The nights and the money are two different questions and are answered separately. The
// **money** is what the member and the payer owe the hotel: for a NIGHTS policy it is the
// first `penaltyNights` nights of the stay at the amounts the booking's own rows carry, so
// the two shares sum to the fee exactly because they are summed from the same rows; for a
// PERCENT policy it is a percentage of the member's own share, and it is entirely the
// member's, because a percentage of the member's share is by definition not the payer's.
//
// The **nights** are what the plan spends. They are capped at the covered nights -- what the
// plan actually reserved -- because a policy charging three nights against a plan that
// carried two cannot take a third from a balance it never held. The remainder is released.
func QuoteCancellation(policy LodgingPolicy, snapshot QuoteSnapshot, nights []BookingNightRecord,
	stayNights int, now time.Time, checkIn time.Time,
) (CancellationQuote, error) {
	out := CancellationQuote{
		CurrencyCode: snapshot.CurrencyCode, FeeAmount: "0", PayerFee: "0", MemberFee: "0",
		FreeUntil: policy.FreeUntil(checkIn).UTC(),
	}
	if out.CurrencyCode == "" {
		out.CurrencyCode = defaultCurrency
	}
	covered := snapshot.CoveredNights
	if covered < 0 {
		covered = 0
	}
	if !now.After(policy.FreeUntil(checkIn)) {
		// Inside the window the member agreed to. Everything the plan holds goes back and
		// nothing is charged.
		out.Free = true
		out.ReleasedNights = covered
		return out, nil
	}

	switch policy.PenaltyKind {
	case penaltyKindNights:
		charged := 0
		if policy.PenaltyNights != nil {
			charged = *policy.PenaltyNights
		}
		if charged > stayNights {
			// A policy charging more nights than the stay is long charges the stay. The
			// member cannot be made to pay for nights their booking never contained.
			charged = stayNights
		}
		if charged < 0 {
			charged = 0
		}
		fee, payer, member, err := sumFirstNights(nights, charged)
		if err != nil {
			return CancellationQuote{}, err
		}
		out.PenaltyNights = charged
		out.FeeAmount, out.PayerFee, out.MemberFee = fee, payer, member
	case penaltyKindPercent:
		percent := "0"
		if policy.PenaltyPercent != nil && *policy.PenaltyPercent != "" {
			percent = *policy.PenaltyPercent
		}
		fee, err := percentOf(snapshot.MemberAmount, percent)
		if err != nil {
			return CancellationQuote{}, err
		}
		// A percentage of the member's own share is the member's, whole. Splitting it would
		// be inventing a payer contribution the contract does not mention.
		out.FeeAmount, out.MemberFee, out.PayerFee = fee, fee, "0"
	default:
		return CancellationQuote{}, fmt.Errorf(
			"accommodation: frozen policy names an unknown penalty kind %q", policy.PenaltyKind)
	}

	// What the plan spends, and what it gets back. The penalty is capped at what the plan
	// actually reserved: a policy charging three nights against a plan that carried two
	// cannot take a third from a balance it never held.
	spent := out.PenaltyNights
	if spent > covered {
		spent = covered
	}
	out.ReleasedNights = covered - spent
	return out, nil
}

// EntitlementPenalty is how many nights of the plan a cancellation actually spends: the
// policy's penalty, capped at what the plan reserved.
func (q CancellationQuote) EntitlementPenalty(coveredNights int) int {
	if q.Free {
		return 0
	}
	if q.PenaltyNights > coveredNights {
		return coveredNights
	}
	return q.PenaltyNights
}

// NoShowQuote is what a no-show costs and how much of the plan a confirmation would spend.
type NoShowQuote struct {
	FeeAmount    string
	PayerAmount  string
	MemberAmount string
	CurrencyCode string
	// Nights is what a confirmation consumes off the plan. The policy is a percentage and
	// the entitlement is whole nights, so the percentage is applied to the covered nights
	// and rounded **up**: a rate of fifty per cent on a one-night stay that rounded down
	// would be a policy that cost the payer nothing, which is not what "fifty per cent"
	// means to the hotel that kept the room empty.
	Nights int
}

// QuoteNoShow prices a report from the frozen policy's no-show rate.
func QuoteNoShow(policy LodgingPolicy, snapshot QuoteSnapshot) (NoShowQuote, error) {
	out := NoShowQuote{
		FeeAmount: "0", PayerAmount: "0", MemberAmount: "0", CurrencyCode: snapshot.CurrencyCode,
	}
	if out.CurrencyCode == "" {
		out.CurrencyCode = defaultCurrency
	}
	fee, err := percentOf(snapshot.MemberAmount, policy.NoShowPercent)
	if err != nil {
		return NoShowQuote{}, err
	}
	// The rate is stated against the member's own share, so the fee is the member's whole.
	out.FeeAmount, out.MemberAmount = fee, fee
	out.Nights = ceilPercentOfNights(snapshot.CoveredNights, policy.NoShowPercent)
	return out, nil
}

// defaultCurrency is what a snapshot with no currency is read as. It never happens for a
// booking this system wrote -- the quote always carries one -- and a zero fee in an unnamed
// currency would still violate the row's own CHECK, so there is one answer rather than a
// failure a member cannot act on.
const defaultCurrency = "TRY"

// sumFirstNights adds the first n nights of the stay, in stay-date order, keeping the payer
// and member halves apart. The three totals are summed from the same rows, which is why
// `payer + member = fee` exactly and not to within a kuruş.
func sumFirstNights(nights []BookingNightRecord, n int) (fee, payer, member string, err error) {
	total, payerTotal, memberTotal := benefitdomain.ZeroQuantity(),
		benefitdomain.ZeroQuantity(), benefitdomain.ZeroQuantity()
	if n > len(nights) {
		n = len(nights)
	}
	for i := 0; i < n; i++ {
		unit, err := benefitdomain.ParseQuantity(nights[i].UnitAmount)
		if err != nil {
			return "", "", "", fmt.Errorf("accommodation: night amount %q: %w", nights[i].UnitAmount, err)
		}
		payerPart, err := benefitdomain.ParseQuantity(nights[i].PayerAmount)
		if err != nil {
			return "", "", "", fmt.Errorf("accommodation: night payer amount %q: %w",
				nights[i].PayerAmount, err)
		}
		memberPart, err := benefitdomain.ParseQuantity(nights[i].MemberAmount)
		if err != nil {
			return "", "", "", fmt.Errorf("accommodation: night member amount %q: %w",
				nights[i].MemberAmount, err)
		}
		total = total.Add(unit)
		payerTotal = payerTotal.Add(payerPart)
		memberTotal = memberTotal.Add(memberPart)
	}
	// The row's own CHECK says payer + member = fee. Asserting it here as well means a
	// booking whose night rows somehow disagree is refused with a sentence rather than with
	// a constraint violation nobody can read.
	if payerTotal.Add(memberTotal).Cmp(total) != 0 {
		return "", "", "", errors.New(
			"accommodation: the booking's night amounts do not add up to their own total")
	}
	return total.String(), payerTotal.String(), memberTotal.String(), nil
}

// percentOf is p per cent of amount, exactly. Both are exact decimals and the division
// happens once, in base ten: twenty per cent of five hundred is a hundred and not
// 99.999999.
func percentOf(amount, percent string) (string, error) {
	if amount == "" {
		amount = "0"
	}
	if percent == "" {
		percent = "0"
	}
	base, err := benefitdomain.ParseQuantity(amount)
	if err != nil {
		return "", fmt.Errorf("accommodation: amount %q: %w", amount, err)
	}
	rate, err := benefitdomain.ParseQuantity(percent)
	if err != nil {
		return "", fmt.Errorf("accommodation: percentage %q: %w", percent, err)
	}
	return base.Percent(rate).String(), nil
}

// ceilPercentOfNights applies a percentage to a whole number of nights and rounds up.
//
// It counts rather than divides, because the answer has to be a whole night and the exact
// comparison is the only way to round one without a float: the smallest n whose value is at
// least the exact percentage is the ceiling, and `covered` is bounded by the tenant's
// maximum stay, so the loop is a few dozen iterations at worst.
func ceilPercentOfNights(covered int, percent string) int {
	if covered <= 0 {
		return 0
	}
	rate, err := benefitdomain.ParseQuantity(percent)
	if err != nil || !rate.IsPositive() {
		return 0
	}
	exact := benefitdomain.MustQuantity(fmt.Sprintf("%d", covered)).Percent(rate)
	if !exact.IsPositive() {
		return 0
	}
	for n := 1; n < covered; n++ {
		if benefitdomain.MustQuantity(fmt.Sprintf("%d", n)).Cmp(exact) >= 0 {
			return n
		}
	}
	return covered
}
