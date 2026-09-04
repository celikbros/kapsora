package ruleshttp

import (
	"net/http"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/rules/application"
	"github.com/celikbros/kapsora/internal/rules/domain"
)

// PutRules implements putRules: the whole rule set of a DRAFT version is replaced under
// the version's ETag, and every condition is compiled before anything is stored.
func (h *Handler) PutRules(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionDraft)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "ruleSetVersionId", application.ErrVersionNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ReplaceRulesRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	items := make([]domain.RuleInput, 0, len(body.Items))
	for _, in := range body.Items {
		row := domain.RuleInput{
			Code: in.Code, Name: in.Name, Priority: in.Priority, Condition: in.Condition,
			ExplanationCode: in.ExplanationCode, Actions: actionsOf(in.Actions),
			// The two flags default the way the schema says: a rule runs unless it is
			// switched off, and it does not end the pass unless it says so.
			StopOnMatch: in.StopOnMatch != nil && *in.StopOnMatch,
			Active:      in.Active == nil || *in.Active,
		}
		if in.ExplanationParams != nil {
			row.ExplanationParams = *in.ExplanationParams
		}
		items = append(items, row)
	}

	result, err := h.svc.ReplaceRules(r.Context(), rc, versionID, items, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.RuleList{Items: make([]kapsorav1.Rule, 0, len(result.Items))}
	for _, rule := range result.Items {
		out.Items = append(out.Items, ruleView(rule))
	}
	w.Header().Set("ETag", etag(result.RowVersion))
	writeJSON(w, http.StatusOK, out)
}

// PutRuleTestCases implements putRuleTestCases.
func (h *Handler) PutRuleTestCases(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionDraft)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "ruleSetVersionId", application.ErrVersionNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ReplaceRuleTestCasesRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	items := make([]domain.TestCaseInput, 0, len(body.Items))
	for _, in := range body.Items {
		row := domain.TestCaseInput{
			Code: in.Code, Description: in.Description, Input: in.Input,
			ExpectedOutcome: string(in.ExpectedOutcome),
		}
		if in.ExpectedExplanations != nil {
			row.ExpectedExplanations = *in.ExpectedExplanations
		}
		if in.ExpectedActions != nil {
			// An absent expectation is not the same statement as an empty one: absent
			// means "this case says nothing about actions", empty means "it must produce
			// none".
			row.ExpectedActions = actionsOf(in.ExpectedActions)
			row.AssertsActions = true
		}
		items = append(items, row)
	}

	result, err := h.svc.ReplaceTestCases(r.Context(), rc, versionID, items, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.RuleTestCaseList{Items: make([]kapsorav1.RuleTestCase, 0, len(result.Items))}
	for _, c := range result.Items {
		out.Items = append(out.Items, testCaseView(c))
	}
	w.Header().Set("ETag", etag(result.RowVersion))
	writeJSON(w, http.StatusOK, out)
}

// actionsOf narrows the generated action list to the domain's own.
func actionsOf(in *[]kapsorav1.RuleAction) []domain.ActionInput {
	if in == nil {
		return nil
	}
	out := make([]domain.ActionInput, 0, len(*in))
	for _, a := range *in {
		row := domain.ActionInput{Type: string(a.Type)}
		if a.Payload != nil {
			row.Payload = *a.Payload
		}
		out = append(out, row)
	}
	return out
}

func actionViews(in []domain.ActionInput) []kapsorav1.RuleAction {
	out := make([]kapsorav1.RuleAction, 0, len(in))
	for _, a := range in {
		view := kapsorav1.RuleAction{Type: kapsorav1.RuleActionType(a.Type)}
		if len(a.Payload) > 0 {
			payload := a.Payload
			view.Payload = &payload
		}
		out = append(out, view)
	}
	return out
}

func ruleView(r application.RuleRecord) kapsorav1.Rule {
	out := kapsorav1.Rule{
		Id: r.ID, RuleSetVersionId: r.RuleSetVersionID, Code: r.Code, Name: r.Name,
		Priority: r.Priority, Condition: r.Condition, Actions: actionViews(r.Actions),
		ExplanationCode: r.ExplanationCode, StopOnMatch: r.StopOnMatch, Active: r.Active,
	}
	if len(r.ExplanationParams) > 0 {
		params := r.ExplanationParams
		out.ExplanationParams = &params
	}
	return out
}

func testCaseView(c application.TestCaseRecord) kapsorav1.RuleTestCase {
	out := kapsorav1.RuleTestCase{
		Id: c.ID, RuleSetVersionId: c.RuleSetVersionID, Code: c.Code,
		Description: c.Description, Input: c.Input,
		ExpectedOutcome:      kapsorav1.RuleOutcome(c.ExpectedOutcome),
		ExpectedExplanations: c.ExpectedExplanations,
	}
	if out.ExpectedExplanations == nil {
		out.ExpectedExplanations = []string{}
	}
	if c.AssertsActions {
		actions := actionViews(c.ExpectedActions)
		out.ExpectedActions = &actions
	}
	return out
}
