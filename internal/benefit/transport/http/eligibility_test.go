package benefithttp_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// The eligibility response shapes. Like the entitlement ones they are hand-written: the
// quantities are exact decimals on the wire and asserting on their text is the point.
type eligibilityExplanationBody struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

type eligibilityItemBody struct {
	Index             int                          `json:"index"`
	EntitlementCode   *string                      `json:"entitlementCode"`
	Outcome           string                       `json:"outcome"`
	RequestedQuantity json.Number                  `json:"requestedQuantity"`
	AvailableQuantity *json.Number                 `json:"availableQuantity"`
	Explanations      []eligibilityExplanationBody `json:"explanations"`
}

type eligibilityBalanceBody struct {
	EntitlementCode string      `json:"entitlementCode"`
	Available       json.Number `json:"available"`
	Unit            string      `json:"unit"`
}

type eligibilityResultBody struct {
	EvaluationId  uuid.UUID                    `json:"evaluationId"`
	EnrollmentId  *uuid.UUID                   `json:"enrollmentId"`
	Eligible      bool                         `json:"eligible"`
	Outcome       string                       `json:"outcome"`
	PlanVersionId *uuid.UUID                   `json:"planVersionId"`
	Explanations  []eligibilityExplanationBody `json:"explanations"`
	Items         []eligibilityItemBody        `json:"items"`
	Balances      []eligibilityBalanceBody     `json:"balances"`
}

type eligibilityEvaluationBody struct {
	Id          uuid.UUID `json:"id"`
	PersonId    uuid.UUID `json:"personId"`
	ServiceDate string    `json:"serviceDate"`
	Outcome     string    `json:"outcome"`
	Request     struct {
		PersonId     uuid.UUID `json:"personId"`
		ServiceItems []struct {
			Quantity json.Number `json:"quantity"`
		} `json:"serviceItems"`
	} `json:"request"`
	Result eligibilityResultBody `json:"result"`
}

type problemBody struct {
	Code   string `json:"code"`
	Errors []struct {
		Field   string `json:"field"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

// checkBody builds one eligibility question with a request-level entitlement hint.
func checkBody(person uuid.UUID, date, code, quantity string) string {
	return fmt.Sprintf(
		`{"personId":%q,"serviceDate":%q,"serviceItems":[{"serviceDefinitionId":%q,"quantity":%s}],"context":{"entitlementCode":%q}}`,
		person, date, uuid.New(), quantity, code)
}

func TestEligibilityCheckThroughHTTP(t *testing.T) {
	s := newServer(t)
	s.openAccounts(t, "HTTPELIG")

	rec := s.do(call{method: http.MethodPost, path: "/api/v1/eligibility/checks",
		body: checkBody(s.person, "2026-06-01", "DENTAL", "100")})
	if rec.Code != http.StatusOK {
		t.Fatalf("check: %d %s", rec.Code, rec.Body.String())
	}
	var result eligibilityResultBody
	decode(t, rec, &result)

	if result.Outcome != "ELIGIBLE" || !result.Eligible {
		t.Fatalf("outcome = %s (eligible=%v): %+v", result.Outcome, result.Eligible, result.Explanations)
	}
	if result.EvaluationId == uuid.Nil || result.PlanVersionId == nil || result.EnrollmentId == nil {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Items) != 1 || result.Items[0].Outcome != "ELIGIBLE" {
		t.Fatalf("items = %+v", result.Items)
	}
	// The money definition keeps its exact decimal text end to end.
	if result.Items[0].AvailableQuantity == nil || result.Items[0].AvailableQuantity.String() != "1500.5" {
		t.Fatalf("available = %v", result.Items[0].AvailableQuantity)
	}
	var dental bool
	for _, b := range result.Balances {
		if b.EntitlementCode == "DENTAL" {
			dental = b.Available.String() == "1500.5" && b.Unit == "MONEY"
		}
	}
	if !dental {
		t.Fatalf("balances = %+v", result.Balances)
	}

	rec = s.do(call{method: http.MethodGet,
		path: "/api/v1/eligibility/evaluations/" + result.EvaluationId.String()})
	if rec.Code != http.StatusOK {
		t.Fatalf("get evaluation: %d %s", rec.Code, rec.Body.String())
	}
	var evaluation eligibilityEvaluationBody
	decode(t, rec, &evaluation)
	if evaluation.Id != result.EvaluationId || evaluation.PersonId != s.person {
		t.Fatalf("evaluation = %+v", evaluation)
	}
	if evaluation.ServiceDate != "2026-06-01" || evaluation.Outcome != "ELIGIBLE" {
		t.Fatalf("evaluation = %+v", evaluation)
	}
	if evaluation.Request.ServiceItems[0].Quantity.String() != "100" {
		t.Fatalf("stored request = %+v", evaluation.Request)
	}

	rec = s.do(call{method: http.MethodGet, path: "/api/v1/eligibility/evaluations/" + uuid.New().String()})
	if rec.Code != http.StatusNotFound || problemOf(t, rec).Code != "ELIGIBILITY_EVALUATION_NOT_FOUND" {
		t.Fatalf("unknown evaluation: %d %s", rec.Code, rec.Body.String())
	}
}

func TestEligibilityCheckRejectsBadInputThroughHTTP(t *testing.T) {
	s := newServer(t)

	cases := []struct {
		name  string
		body  string
		field string
	}{
		{
			name:  "service date is not a day",
			body:  checkBody(s.person, "01.06.2026", "DENTAL", "1"),
			field: "serviceDate",
		},
		{
			name:  "no service items",
			body:  fmt.Sprintf(`{"personId":%q,"serviceDate":"2026-06-01","serviceItems":[]}`, s.person),
			field: "serviceItems",
		},
		{
			name:  "quantity is zero",
			body:  checkBody(s.person, "2026-06-01", "DENTAL", "0"),
			field: "serviceItems[0].quantity",
		},
		{
			name:  "quantity has more than six decimals",
			body:  checkBody(s.person, "2026-06-01", "DENTAL", "1.1234567"),
			field: "serviceItems[0].quantity",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := s.do(call{method: http.MethodPost, path: "/api/v1/eligibility/checks", body: tc.body})
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
			}
			var p problemBody
			decode(t, rec, &p)
			if p.Code != "VALIDATION_FAILED" {
				t.Fatalf("problem = %+v", p)
			}
			found := false
			for _, e := range p.Errors {
				if e.Field == tc.field {
					found = true
				}
			}
			if !found {
				t.Fatalf("field %s missing from %+v", tc.field, p.Errors)
			}
		})
	}
}

func TestEligibilityProviderScopeThroughHTTP(t *testing.T) {
	s := newServer(t)
	body := func(provider string) string {
		return fmt.Sprintf(
			`{"personId":%q,"providerOrganizationId":%q,"serviceDate":"2026-06-01","serviceItems":[{"serviceDefinitionId":%q,"quantity":1}]}`,
			s.person, provider, uuid.New())
	}

	// Inside its own organization the provider clerk is served.
	rec := s.do(call{method: http.MethodPost, path: "/api/v1/eligibility/checks",
		scope: s.sponsorOrg.String(), body: body(s.sponsorOrg.String())})
	if rec.Code != http.StatusOK {
		t.Fatalf("in-scope check: %d %s", rec.Code, rec.Body.String())
	}

	// Another organization is refused, and so is a request that names none.
	rec = s.do(call{method: http.MethodPost, path: "/api/v1/eligibility/checks",
		scope: s.sponsorOrg.String(), body: body(s.payerOrg.String())})
	if rec.Code != http.StatusForbidden || problemOf(t, rec).Code != "PERMISSION_DENIED" {
		t.Fatalf("out-of-scope check: %d %s", rec.Code, rec.Body.String())
	}
	rec = s.do(call{method: http.MethodPost, path: "/api/v1/eligibility/checks",
		scope: s.sponsorOrg.String(), body: checkBody(s.person, "2026-06-01", "DENTAL", "1")})
	if rec.Code != http.StatusForbidden || problemOf(t, rec).Code != "PERMISSION_DENIED" {
		t.Fatalf("scopeless check: %d %s", rec.Code, rec.Body.String())
	}

	// Without the permission the auditing denier answers, whatever the scope says.
	rec = s.do(call{method: http.MethodPost, path: "/api/v1/eligibility/checks",
		perms: "program.read", body: checkBody(s.person, "2026-06-01", "DENTAL", "1")})
	if rec.Code != http.StatusForbidden || problemOf(t, rec).Code != "PERMISSION_DENIED" {
		t.Fatalf("unauthorised check: %d %s", rec.Code, rec.Body.String())
	}
	if len(s.denied.permissions) == 0 || s.denied.permissions[len(s.denied.permissions)-1] != "eligibility.check" {
		t.Fatalf("denied permissions = %v", s.denied.permissions)
	}
}

func TestEligibilityIdempotencyThroughHTTP(t *testing.T) {
	s := newServer(t)
	body := checkBody(s.person, "2026-06-01", "DENTAL", "1")

	first := s.do(call{method: http.MethodPost, path: "/api/v1/eligibility/checks",
		body: body, idempotencyKey: "counter-7"})
	if first.Code != http.StatusOK {
		t.Fatalf("first check: %d %s", first.Code, first.Body.String())
	}
	second := s.do(call{method: http.MethodPost, path: "/api/v1/eligibility/checks",
		body: body, idempotencyKey: "counter-7"})
	if second.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", second.Code, second.Body.String())
	}
	var one, two eligibilityResultBody
	decode(t, first, &one)
	decode(t, second, &two)
	if one.EvaluationId != two.EvaluationId {
		t.Fatalf("replay produced a new evaluation: %s vs %s", one.EvaluationId, two.EvaluationId)
	}

	other := s.do(call{method: http.MethodPost, path: "/api/v1/eligibility/checks",
		body: checkBody(s.person, "2026-06-02", "DENTAL", "1"), idempotencyKey: "counter-7"})
	if other.Code != http.StatusConflict || problemOf(t, other).Code != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("reused key: %d %s", other.Code, other.Body.String())
	}
}
