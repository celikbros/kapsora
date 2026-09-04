package pricinghttp

import (
	"net/http"
	"strconv"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/pricing/application"
)

// CreatePriceQuote implements createPriceQuote.
func (h *Handler) CreatePriceQuote(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionQuote)
	if !ok {
		return
	}
	var body kapsorav1.CreatePriceQuoteRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in, ok := quoteInput(w, r, body)
	if !ok {
		return
	}
	in.IdempotencyKey = strings.TrimSpace(r.Header.Get("Idempotency-Key"))

	quote, err := h.svc.CreateQuote(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, quoteView(quote))
}

// GetPriceQuote implements getPriceQuote.
func (h *Handler) GetPriceQuote(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionQuote)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "priceQuoteId", application.ErrQuoteNotFound)
	if !ok {
		return
	}
	quote, err := h.svc.GetQuote(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, quoteView(quote))
}

// quoteInput turns the decoded body into the application input. Amounts and quantities
// arrive as exact decimal strings and are parsed into numeric(20,6) here, so a value that
// could not be represented exactly is a 422 naming the line rather than a rounded price
// nobody asked for.
func quoteInput(w http.ResponseWriter, r *http.Request, body kapsorav1.CreatePriceQuoteRequest) (application.QuoteInput, bool) {
	ve := &benefitdomain.ValidationError{}
	in := application.QuoteInput{
		PersonID: body.PersonId, ProgramID: body.ProgramId,
		ProviderProfileID: body.ProviderProfileId, LocationID: body.LocationId,
		ServiceDate:             body.ServiceDate.Time,
		EligibilityEvaluationID: body.EligibilityEvaluationId,
		Items:                   make([]application.QuoteItemInput, 0, len(body.Items)),
	}
	if body.Context != nil {
		in.Context = *body.Context
	}
	for i, item := range body.Items {
		field := "items[" + strconv.Itoa(i) + "]"
		out := application.QuoteItemInput{
			ServiceDefinitionID: item.ServiceDefinitionId,
			PackageDefinitionID: item.PackageDefinitionId,
		}
		quantity, err := benefitdomain.ParseQuantity(item.Quantity)
		if err != nil {
			ve.Add(field+".quantity", "FORMAT", "en fazla 6 ondalık basamaklı sayı olmalı")
		} else {
			out.Quantity = quantity
		}
		if item.RequestedAmount != nil {
			amount, err := benefitdomain.ParseQuantity(*item.RequestedAmount)
			if err != nil {
				ve.Add(field+".requestedAmount", "FORMAT", "en fazla 6 ondalık basamaklı sayı olmalı")
			} else {
				out.RequestedAmount = &amount
			}
		}
		in.Items = append(in.Items, out)
	}
	if err := ve.OrNil(); err != nil {
		writeValidation(w, r, ve.Fields)
		return application.QuoteInput{}, false
	}
	return in, true
}

// quoteView renders a stored quote as the contract's PriceQuote.
func quoteView(q application.QuoteView) kapsorav1.PriceQuote {
	out := kapsorav1.PriceQuote{
		Id: q.ID, PersonId: q.PersonID, ProgramId: q.ProgramID,
		ProviderProfileId: q.ProviderProfileID, LocationId: q.LocationID,
		ServiceDate:  openapi_types.Date{Time: q.ServiceDate},
		CurrencyCode: q.CurrencyCode, Outcome: kapsorav1.PriceQuoteOutcome(q.Outcome),
		RequestedAmount: q.RequestedAmount, ContractAmount: q.ContractAmount,
		CoveredAmount: q.CoveredAmount, PayerAmount: q.PayerAmount, MemberAmount: q.MemberAmount,
		Items:             make([]kapsorav1.PriceQuoteItem, 0, len(q.Items)),
		ContractVersionId: q.ContractVersionID, PlanVersionId: q.PlanVersionID,
		RuleSetVersionIds:       ruleSetVersionIDs(q),
		EligibilityEvaluationId: q.EligibilityEvaluationID,
		ExpiresAt:               q.ExpiresAt, Expired: q.Expired,
		QuotedAt: q.QuotedAt, QuotedBy: q.QuotedBy, Disclaimer: q.Disclaimer,
	}
	for _, item := range q.Items {
		out.Items = append(out.Items, kapsorav1.PriceQuoteItem{
			LineNo:              item.LineNo,
			ServiceDefinitionId: item.ServiceDefinitionID,
			PackageDefinitionId: item.PackageDefinitionID,
			PriceItemId:         item.PriceItemID,
			Quantity:            item.Quantity,
			RequestedAmount:     item.RequestedAmount, ContractAmount: item.ContractAmount,
			CoveredAmount: item.CoveredAmount, PayerAmount: item.PayerAmount,
			MemberAmount: item.MemberAmount,
			Outcome:      kapsorav1.PriceQuoteOutcome(item.Outcome),
			Explanations: explanationViews(item.Explanations),
		})
	}
	return out
}

// ruleSetVersionIDs is never null on the wire: an empty list says "no PRICE rules applied
// on this date", which is a different and more useful statement than an absent field.
func ruleSetVersionIDs(q application.QuoteView) []openapi_types.UUID {
	out := make([]openapi_types.UUID, 0, len(q.RuleSetVersionIDs))
	out = append(out, q.RuleSetVersionIDs...)
	return out
}

func explanationViews(in []application.ExplanationView) []kapsorav1.PriceQuoteExplanation {
	out := make([]kapsorav1.PriceQuoteExplanation, 0, len(in))
	for _, e := range in {
		out = append(out, kapsorav1.PriceQuoteExplanation{
			Code:     e.Code,
			Severity: kapsorav1.PriceQuoteExplanationSeverity(e.Severity),
			Source:   e.Source,
		})
	}
	return out
}
