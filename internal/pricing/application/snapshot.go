package application

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	rulesdomain "github.com/celikbros/kapsora/internal/rules/domain"
)

// The documents below are both the wire shape of the contract (PriceQuote) and the jsonb
// stored on contract.price_quote. They are hand-written rather than taken from the
// generated contract types for the same reason the eligibility snapshots are: every
// amount is an exact numeric(20,6) decimal that must never pass through a float, and a
// string keeps the exact text in both directions.
//
// Nothing personal is stored. Every field is an id, a date, a quantity or a stable code,
// and the whole document is passed through rules/domain.Snapshot before it is written, so
// even a future field added carelessly here cannot put a name or an identity number in an
// immutable column.

// ExplanationView is one line of the reasoning behind a figure.
type ExplanationView struct {
	Code     string  `json:"code"`
	Severity string  `json:"severity"`
	Source   *string `json:"source,omitempty"`
}

// ItemView is one priced line of a quote.
type ItemView struct {
	LineNo              int               `json:"lineNo"`
	ServiceDefinitionID *uuid.UUID        `json:"serviceDefinitionId,omitempty"`
	PackageDefinitionID *uuid.UUID        `json:"packageDefinitionId,omitempty"`
	PriceItemID         *uuid.UUID        `json:"priceItemId,omitempty"`
	Quantity            string            `json:"quantity"`
	RequestedAmount     string            `json:"requestedAmount"`
	ContractAmount      string            `json:"contractAmount"`
	CoveredAmount       string            `json:"coveredAmount"`
	PayerAmount         string            `json:"payerAmount"`
	MemberAmount        string            `json:"memberAmount"`
	Outcome             string            `json:"outcome"`
	Explanations        []ExplanationView `json:"explanations"`
}

// QuoteView is the contract's PriceQuote: the whole answer with its reasoning.
type QuoteView struct {
	ID                      uuid.UUID   `json:"id"`
	PersonID                uuid.UUID   `json:"personId"`
	ProgramID               *uuid.UUID  `json:"programId,omitempty"`
	ProviderProfileID       uuid.UUID   `json:"providerProfileId"`
	LocationID              *uuid.UUID  `json:"locationId,omitempty"`
	ServiceDate             time.Time   `json:"-"`
	CurrencyCode            string      `json:"currencyCode"`
	Outcome                 string      `json:"outcome"`
	RequestedAmount         string      `json:"requestedAmount"`
	ContractAmount          string      `json:"contractAmount"`
	CoveredAmount           string      `json:"coveredAmount"`
	PayerAmount             string      `json:"payerAmount"`
	MemberAmount            string      `json:"memberAmount"`
	Items                   []ItemView  `json:"items"`
	ContractVersionID       *uuid.UUID  `json:"contractVersionId,omitempty"`
	PlanVersionID           *uuid.UUID  `json:"planVersionId,omitempty"`
	RuleSetVersionIDs       []uuid.UUID `json:"ruleSetVersionIds"`
	EligibilityEvaluationID *uuid.UUID  `json:"eligibilityEvaluationId,omitempty"`
	ExpiresAt               time.Time   `json:"-"`
	Expired                 bool        `json:"-"`
	QuotedAt                time.Time   `json:"-"`
	QuotedBy                *uuid.UUID  `json:"quotedBy,omitempty"`
	// Disclaimer is filled in at render time from the constant, never read back out of a
	// stored row: the sentence people are shown must be the one this build says, not the
	// one some earlier build happened to write down.
	Disclaimer string `json:"-"`
}

// resultSnapshot is the jsonb written to contract.price_quote.result_snapshot: the whole
// answer reduced to ids, dates, codes and quantities by rules/domain.Snapshot.
func resultSnapshot(view QuoteView) ([]byte, error) {
	doc, err := toDocument(view)
	if err != nil {
		return nil, fmt.Errorf("pricing: encode result snapshot: %w", err)
	}
	doc["serviceDate"] = view.ServiceDate.Format(time.DateOnly)
	doc["expiresAt"] = view.ExpiresAt.UTC().Format(time.RFC3339)
	doc["quotedAt"] = view.QuotedAt.UTC().Format(time.RFC3339)
	return marshalSnapshot(doc)
}

// RequestItemView is one requested line as the quote understood it.
type RequestItemView struct {
	LineNo              int        `json:"lineNo"`
	ServiceDefinitionID *uuid.UUID `json:"serviceDefinitionId,omitempty"`
	PackageDefinitionID *uuid.UUID `json:"packageDefinitionId,omitempty"`
	Quantity            string     `json:"quantity"`
	RequestedAmount     *string    `json:"requestedAmount,omitempty"`
}

// requestSnapshot is the jsonb written to contract.price_quote.request_snapshot.
func requestSnapshot(in QuoteInput, hints contextHints) ([]byte, error) {
	doc := map[string]any{
		"personId":          in.PersonID.String(),
		"providerProfileId": in.ProviderProfileID.String(),
		"serviceDate":       benefitdomain.DateOnly(in.ServiceDate).Format(time.DateOnly),
		"items":             requestItemDocuments(in),
	}
	if in.ProgramID != nil {
		doc["programId"] = in.ProgramID.String()
	}
	if in.LocationID != nil {
		doc["locationId"] = in.LocationID.String()
	}
	if in.EligibilityEvaluationID != nil {
		doc["eligibilityEvaluationId"] = in.EligibilityEvaluationID.String()
	}
	if ctx := hints.sanitized(); ctx != nil {
		doc["context"] = ctx
	}
	return marshalSnapshot(doc)
}

func requestItemDocuments(in QuoteInput) []any {
	out := make([]any, 0, len(in.Items))
	for i, item := range in.Items {
		entry := map[string]any{
			"lineNo":   i + 1,
			"quantity": item.Quantity.String(),
		}
		if item.ServiceDefinitionID != nil {
			entry["serviceDefinitionId"] = item.ServiceDefinitionID.String()
		}
		if item.PackageDefinitionID != nil {
			entry["packageDefinitionId"] = item.PackageDefinitionID.String()
		}
		if item.RequestedAmount != nil {
			entry["requestedAmount"] = item.RequestedAmount.String()
		}
		out = append(out, entry)
	}
	return out
}

// marshalSnapshot filters a document down to what an immutable column may hold and
// encodes it. The filter is rules/domain.Snapshot, used rather than reimplemented: there
// is one definition in this system of "ids, dates, codes and quantities and nothing
// else", and a second one would drift from it the first time either was changed.
func marshalSnapshot(doc map[string]any) ([]byte, error) {
	out, err := json.Marshal(rulesdomain.Snapshot(doc))
	if err != nil {
		return nil, fmt.Errorf("pricing: encode snapshot: %w", err)
	}
	return out, nil
}

// toDocument round-trips a view through JSON so the snapshot filter walks exactly the
// shape the API returns, rather than a second hand-built copy of it that could disagree.
func toDocument(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// storedResult is the part of a stored result snapshot that is not also a column. The
// money, the ids and the outcome are read back from the columns, which are the authority;
// only the rule set versions live in the document alone.
type storedResult struct {
	RuleSetVersionIDs []uuid.UUID `json:"ruleSetVersionIds"`
}

// itemExplanations encodes the per-line reasoning for contract.price_quote_item. The
// column's CHECK requires an array, so an empty line stores [] rather than null.
func itemExplanations(items []ExplanationView) ([]byte, error) {
	if items == nil {
		items = []ExplanationView{}
	}
	out, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("pricing: encode explanations: %w", err)
	}
	return out, nil
}

// canonicalRequestVersion tags the hashed form below. It is part of the hashed document,
// so a future change to the serialisation cannot produce the same hash for a different
// reading of the same request.
const canonicalRequestVersion = 1

// canonicalRequest is the byte string the request hash is taken over: object keys sorted
// (encoding/json sorts map keys), amounts as exact decimal strings, dates as ISO days and
// absent values as null. The same question twice therefore hashes identically, which is
// what lets an Idempotency-Key tell "the same question again" from "a different question
// under a reused key".
func canonicalRequest(in QuoteInput, hints contextHints) ([]byte, error) {
	items := make([]any, 0, len(in.Items))
	for _, item := range in.Items {
		entry := map[string]any{
			"serviceDefinitionId": nullableUUID(item.ServiceDefinitionID),
			"packageDefinitionId": nullableUUID(item.PackageDefinitionID),
			"quantity":            item.Quantity.String(),
			"requestedAmount":     nil,
		}
		if item.RequestedAmount != nil {
			entry["requestedAmount"] = item.RequestedAmount.String()
		}
		items = append(items, entry)
	}
	doc := map[string]any{
		"schema":                  canonicalRequestVersion,
		"personId":                in.PersonID.String(),
		"programId":               nullableUUID(in.ProgramID),
		"providerProfileId":       in.ProviderProfileID.String(),
		"locationId":              nullableUUID(in.LocationID),
		"serviceDate":             benefitdomain.DateOnly(in.ServiceDate).Format(time.DateOnly),
		"eligibilityEvaluationId": nullableUUID(in.EligibilityEvaluationID),
		"items":                   items,
		"context":                 hints.canonical(),
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("pricing: canonical quote request: %w", err)
	}
	return out, nil
}

// requestHash is the SHA-256 of canonicalRequest, stored as the bytea(32) the table's
// CHECK insists on.
func requestHash(in QuoteInput, hints contextHints) ([]byte, error) {
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
