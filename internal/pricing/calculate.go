// Package pricing turns a selected contract price, a plan's rules and a member's balance
// into the five figures somebody actually needs before a service is delivered: what was
// asked for, what the contract says it costs, how much of that the plan will carry, what
// the payer ends up paying and what the member pays out of pocket.
//
// It is pure. It reserves nothing, moves no balance and reads no clock; a quote is an
// answer, not a promise, and the ledger is untouched by it.
package pricing

import (
	"github.com/celikbros/kapsora/internal/benefit/domain"
)

// Money is the exact decimal every amount here is carried in. It never becomes a float.
type Money = domain.Quantity

// Method is how a price item computes its amount.
type Method string

// The pricing methods of contract.price_item.
const (
	MethodFixed         Method = "FIXED"
	MethodUnit          Method = "UNIT"
	MethodPercentOfList Method = "PERCENT_OF_LIST"
	MethodFormula       Method = "FORMULA"
)

// ShareMethod is how the member's own share of a price is expressed.
type ShareMethod string

// The member share methods of contract.price_item.
const (
	ShareNone    ShareMethod = "NONE"
	ShareFixed   ShareMethod = "FIXED"
	SharePercent ShareMethod = "PERCENT"
)

// Outcome of one line or of the whole quote.
type Outcome string

// Outcomes, matching the CHECK on contract.price_quote.
const (
	OutcomeQuoted         Outcome = "QUOTED"
	OutcomePartial        Outcome = "PARTIAL"
	OutcomeReviewRequired Outcome = "REVIEW_REQUIRED"
	OutcomeNotEligible    Outcome = "NOT_ELIGIBLE"
)

// Explanation codes. Every figure that is not simply the contract amount says why.
const (
	ExplanationNotEligible     = "NOT_ELIGIBLE"
	ExplanationPriceNotFound   = "PRICE_NOT_FOUND"
	ExplanationPriceAmbiguous  = "PRICE_AMBIGUOUS"
	ExplanationMemberShare     = "MEMBER_SHARE_APPLIED"
	ExplanationLimitApplied    = "LIMIT_APPLIED"
	ExplanationPriceAdjusted   = "PRICE_ADJUSTED"
	ExplanationClampedToMin    = "PRICE_CLAMPED_TO_MINIMUM"
	ExplanationClampedToMax    = "PRICE_CLAMPED_TO_MAXIMUM"
	ExplanationBalanceShort    = "BALANCE_INSUFFICIENT"
	ExplanationRounded         = "ROUNDED_TO_CURRENCY"
	ExplanationFormulaUnknown  = "PRICE_FORMULA_UNKNOWN"
	ExplanationReviewRequested = "REVIEW_REQUESTED_BY_RULE"
)

// Severity grades an explanation for display.
type Severity string

// Severities.
const (
	SeverityInfo    Severity = "INFO"
	SeverityWarning Severity = "WARNING"
	SeverityError   Severity = "ERROR"
)

// Explanation is one line of the reasoning, with the thing that caused it.
type Explanation struct {
	Code     string
	Severity Severity
	// Source names the rule or the price item behind this line, when there is one.
	Source string
}

// Price is the winning contract.price_item, already selected.
type Price struct {
	Method             Method
	Amount             Money
	Percent            Money
	MinAmount          *Money
	MaxAmount          *Money
	ShareMethod        ShareMethod
	ShareAmount        Money
	SharePercent       Money
	FormulaKnownAmount *Money // resolved FORMULA result; nil means the key was unknown
}

// AdjustmentKind is what a rule action does to the covered amount.
type AdjustmentKind string

// The kinds this package understands. Everything else a rule returns is the caller's.
const (
	// AdjustmentLimit caps the covered amount at Value.
	AdjustmentLimit AdjustmentKind = "SET_LIMIT"
	// AdjustmentAmount replaces the covered amount with Value.
	AdjustmentAmount AdjustmentKind = "ADJUST_PRICE_AMOUNT"
	// AdjustmentPercent scales the covered amount to Value per cent of itself.
	AdjustmentPercent AdjustmentKind = "ADJUST_PRICE_PERCENT"
	// AdjustmentReview marks the line as needing a person to look at it.
	AdjustmentReview AdjustmentKind = "REVIEW"
)

// Adjustment is one rule action bearing on one line.
type Adjustment struct {
	RuleCode string
	Kind     AdjustmentKind
	Value    Money
}

// Item is one requested line.
type Item struct {
	LineNo int
	// Quantity requested; UNIT prices multiply by it.
	Quantity Money
	// Requested is what the provider asked for, if anything. PERCENT_OF_LIST prices a
	// percentage of it.
	Requested Money
	// Price is the winning price item, or nil when selection found none or could not
	// choose. NoPriceReason then says which.
	Price *Price
	// NoPriceReason is PRICE_NOT_FOUND or PRICE_AMBIGUOUS.
	NoPriceReason string
	// Available is the entitlement balance this line may draw on.
	Available Money
	// Eligible is the eligibility answer for this line.
	Eligible bool
	// Adjustments are the rule actions that bear on this line.
	Adjustments []Adjustment
}

// LineResult is one priced line.
type LineResult struct {
	LineNo       int
	Outcome      Outcome
	Requested    Money
	Contract     Money
	Covered      Money
	Payer        Money
	Member       Money
	Explanations []Explanation
}

// Result is the whole quote: the lines and their totals.
type Result struct {
	Outcome      Outcome
	Items        []LineResult
	Requested    Money
	Contract     Money
	Covered      Money
	Payer        Money
	Member       Money
	Explanations []Explanation
}

// Calculate prices every line and totals them. minorUnits is the currency's scale, which
// is where the single rounding happens.
func Calculate(items []Item, minorUnits int) Result {
	res := Result{Items: make([]LineResult, 0, len(items))}
	for _, item := range items {
		line := calculateLine(item, minorUnits)
		res.Items = append(res.Items, line)
		res.Requested = res.Requested.Add(line.Requested)
		res.Contract = res.Contract.Add(line.Contract)
		res.Covered = res.Covered.Add(line.Covered)
		res.Payer = res.Payer.Add(line.Payer)
		res.Member = res.Member.Add(line.Member)
	}
	res.Outcome = foldOutcome(res.Items)
	// A quote nobody can act on carries no member figure: an operator who is shown a
	// number reads it as the answer, and here there is not one yet.
	if res.Outcome == OutcomeReviewRequired {
		res.Payer, res.Member = domain.ZeroQuantity(), domain.ZeroQuantity()
	}
	return res
}

func calculateLine(item Item, minorUnits int) LineResult {
	line := LineResult{LineNo: item.LineNo, Requested: item.Requested.RoundTo(minorUnits)}

	if item.Price == nil {
		code := item.NoPriceReason
		if code == "" {
			code = ExplanationPriceNotFound
		}
		line.Outcome = OutcomeReviewRequired
		line.Explanations = append(line.Explanations, Explanation{Code: code, Severity: SeverityError})
		return line
	}

	contract, explanations := contractAmount(*item.Price, item.Quantity, item.Requested)
	line.Explanations = append(line.Explanations, explanations...)
	if contract.IsNegative() {
		contract = domain.ZeroQuantity()
	}

	// The member's own share comes off the top: it is the part of the price the plan was
	// never going to carry, not an extra charge on top of it.
	share := memberShare(*item.Price, contract)
	if share.IsPositive() {
		line.Explanations = append(line.Explanations,
			Explanation{Code: ExplanationMemberShare, Severity: SeverityInfo})
	}
	covered := contract.Sub(share)
	if covered.IsNegative() {
		covered = domain.ZeroQuantity()
	}

	review := false
	for _, adj := range item.Adjustments {
		switch adj.Kind {
		case AdjustmentLimit:
			if covered.Cmp(adj.Value) > 0 {
				covered = adj.Value
				line.Explanations = append(line.Explanations,
					Explanation{Code: ExplanationLimitApplied, Severity: SeverityWarning, Source: adj.RuleCode})
			}
		case AdjustmentAmount:
			covered = adj.Value
			line.Explanations = append(line.Explanations,
				Explanation{Code: ExplanationPriceAdjusted, Severity: SeverityInfo, Source: adj.RuleCode})
		case AdjustmentPercent:
			covered = covered.Percent(adj.Value)
			line.Explanations = append(line.Explanations,
				Explanation{Code: ExplanationPriceAdjusted, Severity: SeverityInfo, Source: adj.RuleCode})
		case AdjustmentReview:
			review = true
			line.Explanations = append(line.Explanations,
				Explanation{Code: ExplanationReviewRequested, Severity: SeverityWarning, Source: adj.RuleCode})
		}
	}
	if covered.IsNegative() {
		covered = domain.ZeroQuantity()
	}
	if covered.Cmp(contract) > 0 {
		// A rule may reduce what the plan carries, never raise it above the price agreed.
		covered = contract
	}

	if !item.Eligible {
		covered = domain.ZeroQuantity()
		line.Explanations = append(line.Explanations,
			Explanation{Code: ExplanationNotEligible, Severity: SeverityWarning})
	}

	payer := covered.Min(item.Available)
	if payer.IsNegative() {
		payer = domain.ZeroQuantity()
	}
	if payer.Cmp(covered) < 0 {
		line.Explanations = append(line.Explanations,
			Explanation{Code: ExplanationBalanceShort, Severity: SeverityWarning})
	}

	// One rounding, here, at the end. Contract and payer are rounded to the currency and
	// the member takes the difference, so payer + member equals the contract amount
	// exactly — an invariant that independent rounding would break by a kuruş.
	roundedContract := contract.RoundTo(minorUnits)
	roundedPayer := payer.RoundTo(minorUnits)
	if roundedPayer.Cmp(roundedContract) > 0 {
		roundedPayer = roundedContract
	}
	if contract.Cmp(roundedContract) != 0 || payer.Cmp(roundedPayer) != 0 {
		line.Explanations = append(line.Explanations,
			Explanation{Code: ExplanationRounded, Severity: SeverityInfo})
	}

	line.Contract = roundedContract
	line.Covered = covered.RoundTo(minorUnits)
	line.Payer = roundedPayer
	line.Member = roundedContract.Sub(roundedPayer)

	switch {
	case review:
		line.Outcome = OutcomeReviewRequired
		line.Payer, line.Member = domain.ZeroQuantity(), domain.ZeroQuantity()
	case !item.Eligible:
		line.Outcome = OutcomeNotEligible
	case line.Payer.IsZero() && line.Contract.IsPositive():
		line.Outcome = OutcomePartial
	case line.Payer.Cmp(line.Covered) < 0:
		line.Outcome = OutcomePartial
	default:
		line.Outcome = OutcomeQuoted
	}
	return line
}

// contractAmount computes the price before any share, rule or balance touches it.
func contractAmount(p Price, quantity, requested Money) (Money, []Explanation) {
	var amount Money
	var explanations []Explanation

	switch p.Method {
	case MethodFixed:
		amount = p.Amount
	case MethodUnit:
		amount = p.Amount.Mul(quantity)
	case MethodPercentOfList:
		amount = requested.Percent(p.Percent)
	case MethodFormula:
		if p.FormulaKnownAmount == nil {
			// An unknown formula key is an error, never a quiet fall back to the raw
			// amount: the number would look like a price and be nobody's decision.
			return domain.ZeroQuantity(), []Explanation{
				{Code: ExplanationFormulaUnknown, Severity: SeverityError},
			}
		}
		amount = *p.FormulaKnownAmount
	default:
		return domain.ZeroQuantity(), []Explanation{
			{Code: ExplanationFormulaUnknown, Severity: SeverityError},
		}
	}

	if p.MinAmount != nil && amount.Cmp(*p.MinAmount) < 0 {
		amount = *p.MinAmount
		explanations = append(explanations,
			Explanation{Code: ExplanationClampedToMin, Severity: SeverityInfo})
	}
	if p.MaxAmount != nil && amount.Cmp(*p.MaxAmount) > 0 {
		amount = *p.MaxAmount
		explanations = append(explanations,
			Explanation{Code: ExplanationClampedToMax, Severity: SeverityInfo})
	}
	return amount, explanations
}

// memberShare is the co-payment the member carries whatever the balance says.
func memberShare(p Price, contract Money) Money {
	switch p.ShareMethod {
	case ShareFixed:
		return p.ShareAmount.Min(contract)
	case SharePercent:
		return contract.Percent(p.SharePercent)
	case ShareNone:
		return domain.ZeroQuantity()
	default:
		return domain.ZeroQuantity()
	}
}

// foldOutcome turns the lines into one answer for the quote.
func foldOutcome(lines []LineResult) Outcome {
	if len(lines) == 0 {
		return OutcomeQuoted
	}
	var partial, notEligible, quoted bool
	for _, l := range lines {
		switch l.Outcome {
		case OutcomeReviewRequired:
			// One line nobody can price makes the whole quote unusable as an answer.
			return OutcomeReviewRequired
		case OutcomePartial:
			partial = true
		case OutcomeNotEligible:
			notEligible = true
		case OutcomeQuoted:
			quoted = true
		}
	}
	switch {
	case partial:
		return OutcomePartial
	case notEligible && !quoted:
		return OutcomeNotEligible
	case notEligible:
		return OutcomePartial
	default:
		return OutcomeQuoted
	}
}
