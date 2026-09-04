package application

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/pricing"
	"github.com/celikbros/kapsora/internal/rules/engine"
)

// The variables a PRICE rule set version may declare and this package supplies. A version
// declaring anything else compiles, and the missing variable then fails that rule at run
// time with a RULE_ERROR line naming it, which is more use to the author than a refusal
// from here.
//
// Every amount arrives as an exact decimal string, never as a JSON number: a price that
// passed through a float on its way into a condition would be a price the rule and the
// arithmetic disagreed about, by a fraction nobody could see.
const (
	varServiceDate         = "serviceDate"
	varPersonID            = "personId"
	varProgramID           = "programId"
	varProviderProfileID   = "providerProfileId"
	varLocationID          = "locationId"
	varServiceDefinitionID = "serviceDefinitionId"
	varPackageDefinitionID = "packageDefinitionId"
	varEntitlementCode     = "entitlementCode"
	varLineNo              = "lineNo"
	varQuantity            = "quantity"
	varRequestedAmount     = "requestedAmount"
	varContractAmount      = "contractAmount"
	varFormulaKey          = "formulaKey"
	varEligible            = "eligible"
)

// Action payload fields this package reads, all validated as exact decimal strings or
// closed enums by internal/rules/domain when the rule was authored.
const (
	payloadAmount = "amount"
	payloadMethod = "method"
	payloadValue  = "value"

	methodFixed   = "FIXED"
	methodPercent = "PERCENT"
	methodDelta   = "DELTA"
)

// priceProgram is one published PRICE rule set version, compiled.
type priceProgram struct {
	version PriceRuleVersion
	program *engine.Program
}

// loadPricePrograms compiles every published PRICE rule set version covering the service
// date. A published version passed the publish gate, which compiles it, so a failure here
// means the stored rules and the engine have genuinely diverged: that is an error rather
// than a line to be quietly skipped, because the alternative is quoting a price the
// tenant's own rules were supposed to have moved.
func (s *Service) loadPricePrograms(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	serviceDate time.Time,
) ([]priceProgram, error) {
	versions, err := s.repo.ListPriceRuleVersions(ctx, tx, tenantID, serviceDate)
	if err != nil {
		return nil, err
	}
	out := make([]priceProgram, 0, len(versions))
	for _, v := range versions {
		program, err := s.programOf(v)
		if err != nil {
			return nil, fmt.Errorf("pricing: compile price rule set %s v%d: %w",
				v.RuleSetCode, v.VersionNo, err)
		}
		out = append(out, priceProgram{version: v, program: program})
	}
	return out, nil
}

// programOf compiles a version, reusing the shared cache. Only published versions reach
// here and a published version never changes, so a cached program can never be stale.
func (s *Service) programOf(v PriceRuleVersion) (*engine.Program, error) {
	if p, ok := s.programs.Get(v.ID); ok {
		return p, nil
	}
	p, err := engine.Compile(v.ID, v.InputSchema, v.Rules)
	if err != nil {
		return nil, err
	}
	s.programs.Put(v.ID, p)
	return p, nil
}

// lineFacts is what a rule may know about one requested line.
type lineFacts struct {
	LineNo              int
	ServiceDefinitionID *uuid.UUID
	PackageDefinitionID *uuid.UUID
	EntitlementCode     string
	Quantity            benefitdomain.Quantity
	Requested           benefitdomain.Quantity
	// Contract is the amount the price item produces before any rule touches it, which is
	// why the calculation is run once without adjustments before the rules are evaluated.
	// It is already rounded to the currency, because that is the figure an author means
	// when they write a threshold.
	Contract   benefitdomain.Quantity
	FormulaKey string
	Eligible   bool
}

// ruleInput builds the one input document every PRICE rule set version of this quote sees.
func ruleInput(in QuoteInput, f lineFacts) map[string]any {
	doc := map[string]any{
		varServiceDate:         benefitdomain.DateOnly(in.ServiceDate),
		varPersonID:            in.PersonID.String(),
		varProviderProfileID:   in.ProviderProfileID.String(),
		varProgramID:           "",
		varLocationID:          "",
		varServiceDefinitionID: "",
		varPackageDefinitionID: "",
		varEntitlementCode:     f.EntitlementCode,
		varLineNo:              int64(f.LineNo),
		varQuantity:            f.Quantity.String(),
		varRequestedAmount:     f.Requested.String(),
		varContractAmount:      f.Contract.String(),
		varFormulaKey:          f.FormulaKey,
		varEligible:            f.Eligible,
	}
	if in.ProgramID != nil {
		doc[varProgramID] = in.ProgramID.String()
	}
	if in.LocationID != nil {
		doc[varLocationID] = in.LocationID.String()
	}
	if f.ServiceDefinitionID != nil {
		doc[varServiceDefinitionID] = f.ServiceDefinitionID.String()
	}
	if f.PackageDefinitionID != nil {
		doc[varPackageDefinitionID] = f.PackageDefinitionID.String()
	}
	return doc
}

// lineAdjustments evaluates every published PRICE rule set version against one line and
// maps what fired onto the four adjustment kinds the calculation understands.
//
// formulaWanted asks for the resolution of a FORMULA price: the first ADJUST_PRICE action
// with method FIXED is then consumed as the formula's answer rather than passed on as an
// adjustment, which is how WP-I3-05 2.1 step 3 resolves a formula_key through the rule
// engine. An unresolved key stays nil, and the calculation answers PRICE_FORMULA_UNKNOWN
// rather than falling back to a number nobody decided.
func (s *Service) lineAdjustments(ctx context.Context, programs []priceProgram, in QuoteInput,
	f lineFacts, formulaWanted bool,
) (adjustments []pricing.Adjustment, formula *pricing.Money) {
	input := ruleInput(in, f)
	for _, p := range programs {
		for _, result := range p.program.Evaluate(ctx, input).Results {
			if !result.Matched || result.ActionType == "" {
				continue
			}
			if formulaWanted && formula == nil && result.ActionType == engine.ActionAdjustPrice &&
				payloadString(result.ActionPayload, payloadMethod) == methodFixed {
				if amount, ok := payloadDecimal(result.ActionPayload, payloadValue); ok {
					formula = &amount
					continue
				}
			}
			if adj, ok := adjustmentOf(result); ok {
				adjustments = append(adjustments, adj)
			}
		}
	}
	return adjustments, formula
}

// adjustmentOf maps one fired action onto an adjustment. The three action types that bear
// on a price are mapped; the rest of the closed list (APPROVE, REJECT, WARN,
// PARTIAL_APPROVE, RESERVE_ENTITLEMENT) is another caller's business — authorization and
// adjudication decide what to do with those, and a quote that acted on them would be
// making a decision it has no standing to make.
func adjustmentOf(result engine.Result) (pricing.Adjustment, bool) {
	adj := pricing.Adjustment{RuleCode: result.RuleCode}
	switch result.ActionType {
	case engine.ActionSetLimit:
		amount, ok := payloadDecimal(result.ActionPayload, payloadAmount)
		if !ok {
			return review(result), true
		}
		adj.Kind, adj.Value = pricing.AdjustmentLimit, amount
		return adj, true
	case engine.ActionAdjustPrice:
		value, ok := payloadDecimal(result.ActionPayload, payloadValue)
		if !ok {
			return review(result), true
		}
		switch payloadString(result.ActionPayload, payloadMethod) {
		case methodFixed:
			adj.Kind, adj.Value = pricing.AdjustmentAmount, value
			return adj, true
		case methodPercent:
			adj.Kind, adj.Value = pricing.AdjustmentPercent, value
			return adj, true
		case methodDelta:
			// DELTA adds to a price rather than replacing or scaling it, and the
			// calculation carries no additive kind. Rather than reinterpret the author's
			// intent, the line goes to a person with the rule named.
			return review(result), true
		default:
			return review(result), true
		}
	case engine.ActionRequireDocument, engine.ActionRequirePreauth,
		engine.ActionRequireMedicalReview, engine.ActionRequireFinancialReview:
		return review(result), true
	default:
		return pricing.Adjustment{}, false
	}
}

// review is the adjustment that stops a line and names the rule that stopped it.
func review(result engine.Result) pricing.Adjustment {
	return pricing.Adjustment{RuleCode: result.RuleCode, Kind: pricing.AdjustmentReview}
}

// payloadDecimal reads an exact decimal out of an action payload. A JSON number is
// refused rather than converted: internal/rules/domain validates these fields as decimal
// text on write for exactly this reason, and accepting a float here would quietly undo it.
func payloadDecimal(payload map[string]any, key string) (benefitdomain.Quantity, bool) {
	s, ok := payload[key].(string)
	if !ok {
		return benefitdomain.ZeroQuantity(), false
	}
	q, err := benefitdomain.ParseQuantity(s)
	if err != nil {
		return benefitdomain.ZeroQuantity(), false
	}
	return q, true
}

func payloadString(payload map[string]any, key string) string {
	s, _ := payload[key].(string)
	return s
}
