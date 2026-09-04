package ruleshttp

import (
	"encoding/hex"
	"net/http"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/rules/application"
)

// RunRuleTests implements runRuleTests. It writes nothing, so it carries no If-Match and
// no Idempotency-Key: running the suite twice is the same question asked twice.
func (h *Handler) RunRuleTests(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionDraft)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "ruleSetVersionId", application.ErrVersionNotFound)
	if !ok {
		return
	}
	run, err := h.svc.RunTests(r.Context(), rc, versionID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.RuleTestRunResult{
		RuleSetVersionId: run.VersionID, Passed: run.Passed, Total: run.Total, Failed: run.Failed,
		Cases: make([]kapsorav1.RuleTestCaseResult, 0, len(run.Cases)),
	}
	for _, c := range run.Cases {
		out.Cases = append(out.Cases, testCaseResultView(c))
	}
	writeJSON(w, http.StatusOK, out)
}

// SimulateRuleSetVersion implements simulateRuleSetVersion: one input against a draft or a
// published version, returning the whole trace and writing nothing at all.
func (h *Handler) SimulateRuleSetVersion(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "ruleSetVersionId", application.ErrVersionNotFound)
	if !ok {
		return
	}
	var body kapsorav1.SimulateRuleSetVersionRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	trace, err := h.svc.Simulate(r.Context(), rc, versionID, body.Input)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, traceView(trace))
}

// GetRuleEvaluation implements getRuleEvaluation.
func (h *Handler) GetRuleEvaluation(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "ruleEvaluationId", application.ErrEvaluationNotFound)
	if !ok {
		return
	}
	record, err := h.svc.GetEvaluation(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, evaluationView(record))
}

func testCaseResultView(c application.TestCaseOutcome) kapsorav1.RuleTestCaseResult {
	out := kapsorav1.RuleTestCaseResult{
		Code: c.Code, Passed: c.Passed,
		ExpectedOutcome:      kapsorav1.RuleOutcome(c.ExpectedOutcome),
		ActualOutcome:        kapsorav1.RuleOutcome(c.ActualOutcome),
		ExpectedExplanations: c.ExpectedExplanations,
		ActualExplanations:   c.ActualExplanations,
		ActualActions:        actionViews(c.ActualActions),
		Mismatches:           make([]kapsorav1.RuleTestCaseResultMismatches, 0, len(c.Mismatches)),
	}
	if out.ExpectedExplanations == nil {
		out.ExpectedExplanations = []string{}
	}
	if c.AssertsActions {
		expected := actionViews(c.ExpectedActions)
		out.ExpectedActions = &expected
	}
	for _, m := range c.Mismatches {
		out.Mismatches = append(out.Mismatches, kapsorav1.RuleTestCaseResultMismatches(m))
	}
	return out
}

func traceView(t application.Trace) kapsorav1.RuleEvaluationTrace {
	versionNo, duration := t.VersionNo, t.DurationMs
	status := kapsorav1.RuleSetVersionStatus(t.Status)
	return kapsorav1.RuleEvaluationTrace{
		RuleSetVersionId: t.VersionID, VersionNo: &versionNo, Status: &status,
		Outcome: kapsorav1.RuleOutcome(t.Outcome), DurationMs: &duration,
		Results: resultViews(t.Results),
	}
}

func evaluationView(e application.EvaluationRecord) kapsorav1.RuleEvaluation {
	versionNo, code := e.VersionNo, e.RuleSetCode
	out := kapsorav1.RuleEvaluation{
		Id: e.ID, RuleSetVersionId: e.RuleSetVersionID, RuleSetId: e.RuleSetID,
		RuleSetCode: &code, VersionNo: &versionNo,
		SubjectType: e.SubjectType, SubjectId: e.SubjectID,
		Outcome: kapsorav1.RuleOutcome(e.Outcome), InputHash: hex.EncodeToString(e.InputHash),
		InputSnapshot: e.InputSnapshot, DurationMs: e.DurationMs,
		EvaluatedAt: e.EvaluatedAt, EvaluatedBy: e.EvaluatedBy,
		Results: resultViews(e.Results),
	}
	if out.InputSnapshot == nil {
		out.InputSnapshot = map[string]any{}
	}
	return out
}

func resultViews(rows []application.EvaluationResultRow) []kapsorav1.RuleEvaluationResultLine {
	out := make([]kapsorav1.RuleEvaluationResultLine, 0, len(rows))
	for _, r := range rows {
		line := kapsorav1.RuleEvaluationResultLine{
			Sequence: r.Sequence, RuleId: r.RuleID, RuleCode: r.RuleCode, Matched: r.Matched,
			ActionType: r.ActionType, ExplanationCode: r.ExplanationCode,
			Severity: kapsorav1.RuleSeverity(r.Severity),
		}
		if r.ActionPayload != nil {
			payload := r.ActionPayload
			line.ActionPayload = &payload
		}
		out = append(out, line)
	}
	return out
}
