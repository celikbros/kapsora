package eligibility

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/benefit/domain"
)

// The documents below are both the wire shape of the contract (EligibilityCheckResult,
// EligibilityEvaluation) and the jsonb stored in benefit.eligibility_evaluation. They are
// hand-written rather than taken from the generated contract types for the same reason
// the entitlement views are: a quantity is a numeric(20,6) that must never pass through
// a float, and json.Number keeps the exact decimal text in both directions.
//
// Nothing personal is stored. Every field is an id, a date, a quantity, a unit or a
// stable code; names, identifiers, diagnoses and free-text notes never enter a snapshot.

// ExplanationView is one reason of the result document.
type ExplanationView struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

// ItemView is the verdict on one requested line, in request order.
type ItemView struct {
	Index             int               `json:"index"`
	EntitlementCode   *string           `json:"entitlementCode,omitempty"`
	Outcome           string            `json:"outcome"`
	RequestedQuantity json.Number       `json:"requestedQuantity"`
	AvailableQuantity *json.Number      `json:"availableQuantity,omitempty"`
	Explanations      []ExplanationView `json:"explanations"`
}

// BalanceView is one entitlement balance reported with the outcome.
type BalanceView struct {
	EntitlementCode string      `json:"entitlementCode"`
	Available       json.Number `json:"available"`
	Unit            string      `json:"unit"`
}

// ResultView is the contract's EligibilityCheckResult.
type ResultView struct {
	EvaluationID      uuid.UUID         `json:"evaluationId"`
	EnrollmentID      *uuid.UUID        `json:"enrollmentId,omitempty"`
	Items             []ItemView        `json:"items"`
	Eligible          bool              `json:"eligible"`
	Outcome           string            `json:"outcome"`
	EvaluatedAt       time.Time         `json:"evaluatedAt"`
	PlanVersionID     *uuid.UUID        `json:"planVersionId,omitempty"`
	RuleSetVersionIDs []uuid.UUID       `json:"ruleSetVersionIds"`
	Explanations      []ExplanationView `json:"explanations"`
	Balances          []BalanceView     `json:"balances"`
}

// RequestItemView is one requested line as it was understood.
type RequestItemView struct {
	ServiceDefinitionID uuid.UUID    `json:"serviceDefinitionId"`
	Quantity            json.Number  `json:"quantity"`
	RequestedAmount     *json.Number `json:"requestedAmount,omitempty"`
	CurrencyCode        *string      `json:"currencyCode,omitempty"`
}

// RequestView is the contract's EligibilityCheckRequest as it was understood. The free
// form context is reduced to the keys the resolution actually reads, so an operator
// cannot smuggle a name or a diagnosis into an immutable snapshot.
type RequestView struct {
	PersonID               uuid.UUID         `json:"personId"`
	ProgramID              *uuid.UUID        `json:"programId,omitempty"`
	ProviderOrganizationID *uuid.UUID        `json:"providerOrganizationId,omitempty"`
	ServiceDate            string            `json:"serviceDate"`
	ServiceItems           []RequestItemView `json:"serviceItems"`
	Context                map[string]any    `json:"context,omitempty"`
}

// EvaluationView is the contract's EligibilityEvaluation: the stored snapshot.
type EvaluationView struct {
	ID            uuid.UUID   `json:"id"`
	PersonID      uuid.UUID   `json:"personId"`
	ProgramID     *uuid.UUID  `json:"programId,omitempty"`
	EnrollmentID  *uuid.UUID  `json:"enrollmentId,omitempty"`
	PlanVersionID *uuid.UUID  `json:"planVersionId,omitempty"`
	ServiceDate   string      `json:"serviceDate"`
	Outcome       string      `json:"outcome"`
	EvaluatedAt   time.Time   `json:"evaluatedAt"`
	EvaluatedBy   *uuid.UUID  `json:"evaluatedBy,omitempty"`
	Request       RequestView `json:"request"`
	Result        ResultView  `json:"result"`
}

// newResultView renders a resolution as the contract's result document.
func newResultView(id uuid.UUID, evaluatedAt time.Time, r Result) ResultView {
	out := ResultView{
		EvaluationID: id, Eligible: r.Eligible, Outcome: r.Outcome,
		EvaluatedAt: evaluatedAt.UTC(), RuleSetVersionIDs: []uuid.UUID{},
		Explanations: explanationViews(r.Explanations),
		Items:        make([]ItemView, 0, len(r.Items)),
		Balances:     make([]BalanceView, 0, len(r.Balances)),
	}
	if r.EnrollmentID != uuid.Nil {
		id := r.EnrollmentID
		out.EnrollmentID = &id
	}
	if r.PlanVersionID != uuid.Nil {
		id := r.PlanVersionID
		out.PlanVersionID = &id
	}
	for _, item := range r.Items {
		view := ItemView{
			Index: item.Index, Outcome: item.Outcome,
			RequestedQuantity: json.Number(item.RequestedQuantity.String()),
			Explanations:      explanationViews(item.Explanations),
		}
		if item.EntitlementCode != "" {
			code := item.EntitlementCode
			view.EntitlementCode = &code
		}
		if item.AvailableQuantity != nil {
			available := json.Number(item.AvailableQuantity.String())
			view.AvailableQuantity = &available
		}
		out.Items = append(out.Items, view)
	}
	for _, b := range r.Balances {
		out.Balances = append(out.Balances, BalanceView{
			EntitlementCode: b.EntitlementCode, Available: json.Number(b.Available.String()), Unit: b.Unit,
		})
	}
	return out
}

func explanationViews(in []Explanation) []ExplanationView {
	out := make([]ExplanationView, 0, len(in))
	for _, e := range in {
		out = append(out, ExplanationView{Code: e.Code, Message: e.Message, Severity: e.Severity})
	}
	return out
}

// newRequestView renders the request as it was understood, with the context reduced to
// the recognised hints.
func newRequestView(in CheckInput, hints contextHints) RequestView {
	out := RequestView{
		PersonID: in.PersonID, ProgramID: in.ProgramID,
		ProviderOrganizationID: in.ProviderOrganizationID,
		ServiceDate:            domain.DateOnly(in.ServiceDate).Format(time.DateOnly),
		ServiceItems:           make([]RequestItemView, 0, len(in.Items)),
		Context:                hints.sanitized(),
	}
	for _, item := range in.Items {
		view := RequestItemView{
			ServiceDefinitionID: item.ServiceDefinitionID,
			Quantity:            json.Number(item.Quantity.String()),
		}
		if item.RequestedAmount != nil {
			amount := json.Number(item.RequestedAmount.String())
			view.RequestedAmount = &amount
		}
		if item.CurrencyCode != "" {
			code := item.CurrencyCode
			view.CurrencyCode = &code
		}
		out.ServiceItems = append(out.ServiceItems, view)
	}
	return out
}

// canonicalRequest is the byte string the request hash is taken over. It follows the
// same rules as domain.CanonicalConfiguration: object keys are sorted (encoding/json
// sorts map keys), quantities are exact decimal strings, dates are ISO days and absent
// values are null. Two readings of the same request therefore hash identically, which is
// what lets an Idempotency-Key replay tell "the same question again" from "a different
// question under a reused key".
const canonicalRequestVersion = 1

func canonicalRequest(in CheckInput, hints contextHints) ([]byte, error) {
	items := make([]any, 0, len(in.Items))
	for _, item := range in.Items {
		entry := map[string]any{
			"serviceDefinitionId": item.ServiceDefinitionID.String(),
			"quantity":            item.Quantity.String(),
			"requestedAmount":     nil,
			"currencyCode":        nil,
		}
		if item.RequestedAmount != nil {
			entry["requestedAmount"] = item.RequestedAmount.String()
		}
		if item.CurrencyCode != "" {
			entry["currencyCode"] = item.CurrencyCode
		}
		items = append(items, entry)
	}
	doc := map[string]any{
		"schema":                 canonicalRequestVersion,
		"personId":               in.PersonID.String(),
		"programId":              nullableUUID(in.ProgramID),
		"providerOrganizationId": nullableUUID(in.ProviderOrganizationID),
		"serviceDate":            domain.DateOnly(in.ServiceDate).Format(time.DateOnly),
		"serviceItems":           items,
		"context":                hints.canonical(),
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("benefit: canonical eligibility request: %w", err)
	}
	return out, nil
}

// requestHash is the SHA-256 of canonicalRequest, stored as bytea(32).
func requestHash(in CheckInput, hints contextHints) ([]byte, error) {
	canonical, err := canonicalRequest(in, hints)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	return sum[:], nil
}

func nullableUUID(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return id.String()
}
