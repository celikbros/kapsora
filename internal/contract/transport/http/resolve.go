package contracthttp

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/contract/application"
	"github.com/celikbros/kapsora/internal/contract/selection"
)

// ResolvePrice implements resolvePrice. It changes no state: it answers which contracted
// price applies, or refuses to guess. Two equally specific prices produce REVIEW_REQUIRED
// with reason PRICE_AMBIGUOUS and every tied item named, because a coin flip here is a
// silent financial error that nothing anywhere reports.
func (h *Handler) ResolvePrice(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	var body kapsorav1.ResolvePriceRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.ResolveRequest{
		ServiceDate:         body.ServiceDate.Time,
		ProviderProfileID:   body.ProviderProfileId,
		ServiceDefinitionID: body.ServiceDefinitionId,
	}
	if body.LocationId != nil {
		location := *body.LocationId
		in.LocationID = &location
	}

	result, err := h.svc.ResolvePrice(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, resolveView(result))
}

func resolveView(result application.ResolveResult) kapsorav1.ResolvePriceResult {
	out := kapsorav1.ResolvePriceResult{
		Outcome:     kapsorav1.ResolvePriceResultOutcome(result.Outcome()),
		ServiceDate: openapi_types.Date{Time: result.ServiceDate},
		Tied:        make([]kapsorav1.ResolvedPrice, 0, len(result.Tied)),
		Considered:  make([]kapsorav1.ScoredPriceCandidate, 0, len(result.Considered)),
	}
	if result.Reason != "" {
		reason := string(result.Reason)
		out.Reason = &reason
	}
	if result.Winner != nil {
		winner := resolvedPriceView(*result.Winner)
		out.Winner = &winner
	}
	for _, tied := range result.Tied {
		out.Tied = append(out.Tied, resolvedPriceView(tied))
	}
	for _, scored := range result.Considered {
		out.Considered = append(out.Considered, scoredView(scored))
	}
	return out
}

func resolvedPriceView(c application.ResolvedCandidate) kapsorav1.ResolvedPrice {
	return kapsorav1.ResolvedPrice{
		PriceItemId: c.Scored.Candidate.PriceItemID, PriceListId: c.Scored.Candidate.PriceListID,
		PriceListCode:     optionalString(c.Detail.PriceListCode),
		ContractVersionId: c.Scored.Candidate.ContractVersionID,
		ContractId:        c.Detail.ContractID, ContractCode: c.Detail.ContractCode,
		VersionNo: c.Detail.VersionNo, CurrencyCode: c.Detail.CurrencyCode,
		LocationId:    c.Detail.LocationID,
		UnitType:      kapsorav1.ServiceUnitType(c.Detail.UnitType),
		PricingMethod: kapsorav1.PricingMethod(c.Detail.PricingMethod),
		Amount:        optionalString(c.Detail.Amount), Percent: optionalString(c.Detail.Percent),
		FormulaKey: c.Detail.FormulaKey,
		MinAmount:  optionalString(c.Detail.MinAmount), MaxAmount: optionalString(c.Detail.MaxAmount),
		MemberShareMethod:  kapsorav1.MemberShareMethod(c.Detail.MemberShareMethod),
		MemberShareAmount:  optionalString(c.Detail.MemberShareAmount),
		MemberSharePercent: optionalString(c.Detail.MemberSharePercent),
		MatchedVia:         kapsorav1.PriceMatchTarget(matchedVia(c.Scored.Candidate.Target)),
	}
}

func scoredView(c application.ResolvedCandidate) kapsorav1.ScoredPriceCandidate {
	itemPriority := c.Scored.Candidate.ItemPriority
	listPriority := c.Scored.Candidate.ListPriority
	out := kapsorav1.ScoredPriceCandidate{
		PriceItemId: c.Scored.Candidate.PriceItemID, PriceListId: c.Scored.Candidate.PriceListID,
		ContractVersionId: c.Scored.Candidate.ContractVersionID,
		Score:             c.Scored.Score, Matched: c.Scored.Matched,
		MatchedVia:   kapsorav1.PriceMatchTarget(matchedVia(c.Scored.Candidate.Target)),
		ItemPriority: &itemPriority, ListPriority: &listPriority,
	}
	if c.Scored.Excluded != "" {
		excluded := c.Scored.Excluded
		out.Excluded = &excluded
	}
	return out
}

// matchedVia names what the price item pointed at; the database CHECK guarantees exactly
// one of the three is set, so TargetNone cannot reach here from a stored row.
func matchedVia(target selection.Target) string {
	switch target {
	case selection.TargetDefinition:
		return "DEFINITION"
	case selection.TargetPackage:
		return "PACKAGE"
	case selection.TargetCategory:
		return "CATEGORY"
	case selection.TargetNone:
		return "CATEGORY"
	default:
		return "CATEGORY"
	}
}
