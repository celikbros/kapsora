package application

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/benefit/eligibility"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// The documents in this file are what a submitted version, an eligibility evaluation and a
// rule evaluation are read back from years later. Nothing personal goes into any of them:
// every field is an id, a date, a code, a quantity or an outcome. A name or an identifier
// in an immutable snapshot is a name nobody can ever remove.

// snapshotItem is one line as it was submitted, with the decimals as exact text.
type snapshotItem struct {
	LineNo              int     `json:"lineNo"`
	ServiceDefinitionID string  `json:"serviceDefinitionId"`
	ServiceCode         string  `json:"serviceCode,omitempty"`
	RequestedQuantity   string  `json:"requestedQuantity"`
	UnitType            string  `json:"unitType"`
	RequestedAmount     *string `json:"requestedAmount,omitempty"`
	CurrencyCode        *string `json:"currencyCode,omitempty"`
}

// snapshotExplanation is one reason behind the eligibility outcome.
type snapshotExplanation struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

// snapshotItemResult is the eligibility verdict on one line.
type snapshotItemResult struct {
	Index             int                   `json:"index"`
	EntitlementCode   string                `json:"entitlementCode,omitempty"`
	Outcome           string                `json:"outcome"`
	RequestedQuantity string                `json:"requestedQuantity"`
	AvailableQuantity *string               `json:"availableQuantity,omitempty"`
	Explanations      []snapshotExplanation `json:"explanations"`
}

// snapshotGate is what the gate decided and what decided it. It lives on the version rather
// than only on the request, so a version that was later superseded still says why it landed
// where it did.
type snapshotGate struct {
	Status                  string                `json:"status"`
	ReasonCode              string                `json:"reasonCode"`
	EligibilityOutcome      string                `json:"eligibilityOutcome"`
	EligibilityEvaluationID *uuid.UUID            `json:"eligibilityEvaluationId,omitempty"`
	RuleEvaluationID        *uuid.UUID            `json:"ruleEvaluationId,omitempty"`
	RuleSetVersionIDs       []uuid.UUID           `json:"ruleSetVersionIds"`
	RequiredDocumentTypes   []string              `json:"requiredDocumentTypes"`
	Explanations            []snapshotExplanation `json:"explanations"`
	Items                   []snapshotItemResult  `json:"items"`
}

// snapshotDocument is the whole frozen version: the header as it was, the lines as they
// were, and the decision they produced.
type snapshotDocument struct {
	SnapshotVersion        int            `json:"snapshotVersion"`
	Reference              string         `json:"reference"`
	RequestType            string         `json:"requestType"`
	PersonID               uuid.UUID      `json:"personId"`
	ProgramID              uuid.UUID      `json:"programId"`
	EnrollmentID           uuid.UUID      `json:"enrollmentId"`
	ProviderOrganizationID *uuid.UUID     `json:"providerOrganizationId,omitempty"`
	ServiceDate            string         `json:"serviceDate"`
	RequestedStartAt       *time.Time     `json:"requestedStartAt,omitempty"`
	RequestedEndAt         *time.Time     `json:"requestedEndAt,omitempty"`
	Channel                string         `json:"channel"`
	VersionNo              int            `json:"versionNo"`
	SubmittedAt            time.Time      `json:"submittedAt"`
	Items                  []snapshotItem `json:"items"`
	Gate                   snapshotGate   `json:"gate"`
}

// buildSnapshot freezes a version: the header, the lines and the gate's answer together.
func buildSnapshot(request RequestRecord, items []ItemRecord, decision gateDecision,
	submittedAt time.Time,
) ([]byte, error) {
	doc := snapshotDocument{
		SnapshotVersion: snapshotVersion, Reference: request.Reference,
		RequestType: request.RequestType, PersonID: request.PersonID,
		ProgramID: request.ProgramID, EnrollmentID: request.EnrollmentID,
		ProviderOrganizationID: request.ProviderOrganizationID,
		ServiceDate:            request.ServiceDate.Format(time.DateOnly),
		RequestedStartAt:       request.RequestedStartAt, RequestedEndAt: request.RequestedEndAt,
		Channel: request.Channel, VersionNo: request.CurrentVersionNo, SubmittedAt: submittedAt,
		Items: snapshotItems(items, nil),
		Gate: snapshotGate{
			Status: decision.Status, ReasonCode: decision.ReasonCode,
			EligibilityOutcome:      decision.EligibilityOutcome,
			EligibilityEvaluationID: decision.EligibilityEvaluationID,
			RuleEvaluationID:        decision.RuleEvaluationID,
			RuleSetVersionIDs:       decision.RuleVersionIDs,
			RequiredDocumentTypes:   nonNilStrings(decision.RequiredDocumentTypes),
			Explanations:            snapshotExplanations(decision.Eligibility.Explanations),
			Items:                   snapshotItemResults(decision.Eligibility.Items),
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("servicerequest: encode version snapshot: %w", err)
	}
	return raw, nil
}

func snapshotItems(items []ItemRecord, definitions map[uuid.UUID]ServiceDefinitionRecord) []snapshotItem {
	out := make([]snapshotItem, 0, len(items))
	for _, item := range items {
		out = append(out, snapshotItem{
			LineNo: item.LineNo, ServiceDefinitionID: item.ServiceDefinitionID.String(),
			ServiceCode:       definitionCode(item.ServiceDefinitionID, definitions),
			RequestedQuantity: item.RequestedQuantity, UnitType: item.UnitType,
			RequestedAmount: item.RequestedAmount, CurrencyCode: item.CurrencyCode,
		})
	}
	return out
}

func snapshotExplanations(in []eligibility.Explanation) []snapshotExplanation {
	out := make([]snapshotExplanation, 0, len(in))
	for _, e := range in {
		out = append(out, snapshotExplanation{Code: e.Code, Message: e.Message, Severity: e.Severity})
	}
	return out
}

func snapshotItemResults(in []eligibility.ItemResult) []snapshotItemResult {
	out := make([]snapshotItemResult, 0, len(in))
	for _, item := range in {
		view := snapshotItemResult{
			Index: item.Index, EntitlementCode: item.EntitlementCode, Outcome: item.Outcome,
			RequestedQuantity: item.RequestedQuantity.String(),
			Explanations:      snapshotExplanations(item.Explanations),
		}
		if item.AvailableQuantity != nil {
			available := item.AvailableQuantity.String()
			view.AvailableQuantity = &available
		}
		out = append(out, view)
	}
	return out
}

// eligibilityRequestSnapshot is the question as the resolver was asked it.
func eligibilityRequestSnapshot(request RequestRecord, items []ItemRecord,
	definitions map[uuid.UUID]ServiceDefinitionRecord, day time.Time,
) ([]byte, error) {
	doc := map[string]any{
		"personId":          request.PersonID,
		"programId":         request.ProgramID,
		"serviceRequestId":  request.ID,
		"serviceRequestRef": request.Reference,
		"serviceDate":       day.Format(time.DateOnly),
		"serviceItems":      snapshotItems(items, definitions),
	}
	if request.ProviderOrganizationID != nil {
		doc["providerOrganizationId"] = *request.ProviderOrganizationID
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("servicerequest: encode eligibility request snapshot: %w", err)
	}
	return raw, nil
}

// eligibilityResultSnapshot is the answer, in the shape the eligibility module stores.
func eligibilityResultSnapshot(id uuid.UUID, evaluatedAt time.Time,
	result eligibility.Result,
) ([]byte, error) {
	doc := map[string]any{
		"evaluationId": id,
		"eligible":     result.Eligible,
		"outcome":      result.Outcome,
		"evaluatedAt":  evaluatedAt,
		"explanations": snapshotExplanations(result.Explanations),
		"items":        snapshotItemResults(result.Items),
		"balances":     snapshotBalances(result.Balances),
	}
	if result.EnrollmentID != uuid.Nil {
		doc["enrollmentId"] = result.EnrollmentID
	}
	if result.PlanVersionID != uuid.Nil {
		doc["planVersionId"] = result.PlanVersionID
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("servicerequest: encode eligibility result snapshot: %w", err)
	}
	return raw, nil
}

func snapshotBalances(in []eligibility.Balance) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, b := range in {
		out = append(out, map[string]any{
			"entitlementCode": b.EntitlementCode, "available": b.Available.String(), "unit": b.Unit,
		})
	}
	return out
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// cursorOf is the keyset position of a request on a page.
func cursorOf(record RequestRecord) httpx.Cursor {
	return httpx.Cursor{CreatedAt: record.CreatedAt, ID: record.ID}
}
