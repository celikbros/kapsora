package application

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/pricing"
)

// LinePricingInput is a set of lines to be priced without a quote being stored.
type LinePricingInput struct {
	PersonID          uuid.UUID
	ProgramID         *uuid.UUID
	ProviderProfileID uuid.UUID
	LocationID        *uuid.UUID
	ServiceDate       time.Time
	Items             []QuoteItemInput
	// Context is the same free-form object a quote takes; only the recognised hints are
	// read, and they are the same three, so a claim that names an entitlement code names it
	// the way an eligibility check does.
	Context map[string]any
}

// LinePricing is what the ladder answered, with the versions that decided it.
type LinePricing struct {
	CurrencyCode string
	// Result carries one LineResult per input item, in the order they were given.
	Result            pricing.Result
	PlanVersionID     *uuid.UUID
	ContractVersionID *uuid.UUID
	RuleSetVersionIDs []uuid.UUID
	// PriceItemIDs is the winning contract price item of each line, nil where selection
	// found none — which is what makes a line REVIEW_REQUIRED rather than free.
	PriceItemIDs []*uuid.UUID
}

// PriceLines runs the whole pricing ladder over a set of lines inside the caller's
// transaction and stores nothing.
//
// It exists for the claim (WP-I5-04), which has to price in its submit transaction: a claim
// priced against balances that then moved would be a claim nobody could reconcile. It is
// this package's own `price` under an exported name rather than a second implementation,
// because the whole point of WP-I3-05 is that there is one arithmetic — the number a counter
// was quoted and the number a claim is settled at have to come from the same six steps.
//
// It reserves nothing and moves no balance, exactly as `CreateQuote` does not: the
// eligibility it resolves is the pure resolver over accounts that are already open, and an
// account nobody has opened simply has no balance here.
func (s *Service) PriceLines(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in LinePricingInput,
) (LinePricing, error) {
	hints := parseContext(in.Context)
	computed, err := s.price(ctx, tx, tenantID, QuoteInput{
		PersonID: in.PersonID, ProgramID: in.ProgramID,
		ProviderProfileID: in.ProviderProfileID, LocationID: in.LocationID,
		ServiceDate: in.ServiceDate, Items: in.Items,
	}, hints)
	if err != nil {
		return LinePricing{}, err
	}
	out := LinePricing{
		CurrencyCode: computed.currency, Result: computed.result,
		PlanVersionID: computed.planVersionID, ContractVersionID: computed.contractVersionID,
		RuleSetVersionIDs: computed.ruleSetVersionIDs,
		PriceItemIDs:      make([]*uuid.UUID, 0, len(computed.lines)),
	}
	for _, line := range computed.lines {
		out.PriceItemIDs = append(out.PriceItemIDs, line.PriceItemID)
	}
	return out, nil
}
