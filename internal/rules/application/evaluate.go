package application

import (
	"context"
	"reflect"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/rules/domain"
	"github.com/celikbros/kapsora/internal/rules/engine"
)

// Trace is one pass over a version without anything being written: what was considered, in
// order, and what it folded to.
type Trace struct {
	VersionID  uuid.UUID
	VersionNo  int
	Status     string
	Outcome    string
	DurationMs int
	Results    []EvaluationResultRow
}

// TestCaseOutcome is one test case with what it expected and what it got.
type TestCaseOutcome struct {
	Code                 string
	Passed               bool
	ExpectedOutcome      string
	ActualOutcome        string
	ExpectedExplanations []string
	ActualExplanations   []string
	ExpectedActions      []domain.ActionInput
	AssertsActions       bool
	ActualActions        []domain.ActionInput
	Mismatches           []string
}

// Mismatch kinds reported per case.
const (
	MismatchOutcome      = "OUTCOME"
	MismatchExplanations = "EXPLANATIONS"
	MismatchActions      = "ACTIONS"
)

// TestRun is the whole stored test suite of one version, run against its rules.
type TestRun struct {
	VersionID uuid.UUID
	Total     int
	Failed    int
	// Passed is true only when there is at least one case and every one of them passed.
	// A suite of nothing proves nothing, and submit refuses it for exactly that reason.
	Passed bool
	Cases  []TestCaseOutcome
}

// FailedCodes names the cases that did not pass, which is what the submit refusal quotes.
func (r TestRun) FailedCodes() []string {
	out := make([]string, 0, r.Failed)
	for _, c := range r.Cases {
		if !c.Passed {
			out = append(out, c.Code)
		}
	}
	return out
}

// EvaluateInput is the library entry point's command: which version decides, what it
// decides about, and the whole input document it decides on.
type EvaluateInput struct {
	VersionID   uuid.UUID
	SubjectType string
	SubjectID   *uuid.UUID
	Input       map[string]any
}

// subjectTypePattern mirrors ck_rule_evaluation_subject.
var subjectTypePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,39}$`)

// RunTests runs the stored cases of a version against its rules and reports, per case,
// expected versus actual. It writes nothing: no evaluation row and no test result row, so
// an author may run it as often as they like.
func (s *Service) RunTests(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID) (TestRun, error) {
	var out TestRun
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		version, err := s.repo.GetVersion(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		if !visibleTo(rc, version.Status) {
			return ErrVersionNotFound
		}
		out, err = s.runTests(ctx, tx, rc.TenantID, version)
		return err
	})
	return out, err
}

// runTests is the shared body of RunTests and the submit gate, so the check an author sees
// and the check that lets a version reach review are the same code and cannot drift.
func (s *Service) runTests(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, version VersionRecord) (TestRun, error) {
	rules, err := s.repo.ListRules(ctx, tx, tenantID, version.ID)
	if err != nil {
		return TestRun{}, err
	}
	cases, err := s.repo.ListTestCases(ctx, tx, tenantID, version.ID)
	if err != nil {
		return TestRun{}, err
	}
	program, err := s.programOf(version, rules)
	if err != nil {
		return TestRun{}, compileFieldError("rules", err)
	}

	run := TestRun{VersionID: version.ID, Total: len(cases), Cases: make([]TestCaseOutcome, 0, len(cases))}
	for _, c := range cases {
		ev := program.Evaluate(ctx, coerceInput(version.InputSchema, c.Input))
		outcome := TestCaseOutcome{
			Code: c.Code, ExpectedOutcome: c.ExpectedOutcome, ActualOutcome: string(ev.Outcome),
			ExpectedExplanations: c.ExpectedExplanations, ActualExplanations: explanationsOf(ev),
			ExpectedActions: c.ExpectedActions, AssertsActions: c.AssertsActions,
			ActualActions: actionsOf(ev), Mismatches: []string{},
		}
		if outcome.ExpectedExplanations == nil {
			outcome.ExpectedExplanations = []string{}
		}
		if c.ExpectedOutcome != string(ev.Outcome) {
			outcome.Mismatches = append(outcome.Mismatches, MismatchOutcome)
		}
		if !equalStrings(outcome.ExpectedExplanations, outcome.ActualExplanations) {
			outcome.Mismatches = append(outcome.Mismatches, MismatchExplanations)
		}
		if c.AssertsActions && !matchActions(c.ExpectedActions, outcome.ActualActions) {
			outcome.Mismatches = append(outcome.Mismatches, MismatchActions)
		}
		outcome.Passed = len(outcome.Mismatches) == 0
		if !outcome.Passed {
			run.Failed++
		}
		run.Cases = append(run.Cases, outcome)
	}
	run.Passed = run.Total > 0 && run.Failed == 0
	return run, nil
}

// Simulate runs one supplied input against a draft or published version and returns the
// full trace. It writes no evaluation row and has no side effect of any kind: the engine
// returns actions and performs none of them, which is what lets production data be
// simulated safely (v1.2 11.7, "simulation creates no transaction").
func (s *Service) Simulate(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID,
	input map[string]any,
) (Trace, error) {
	if input == nil {
		return Trace{}, fieldError("input", "REQUIRED", "girdi belgesi gerekli")
	}

	var out Trace
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		version, rules, err := s.loadForEvaluation(ctx, tx, rc, versionID)
		if err != nil {
			return err
		}
		program, err := s.programOf(version, rules)
		if err != nil {
			return compileFieldError("rules", err)
		}
		out = traceOf(version, program.Evaluate(ctx, coerceInput(version.InputSchema, input)))
		return nil
	})
	return out, err
}

// Evaluate is the entry point the rest of the system calls: it runs a published version
// over one input and records what was decided, so a dispute years later can read the
// version, the rules that fired and the order they fired in. A draft is refused — an
// unapproved rule must never be the reason a member was told no.
func (s *Service) Evaluate(ctx context.Context, rc identity.RequestContext, in EvaluateInput) (EvaluationRecord, error) {
	if !subjectTypePattern.MatchString(in.SubjectType) {
		return EvaluationRecord{}, fieldError("subjectType", "FORMAT", "büyük harf, rakam ve alt çizgi; 2-40 karakter")
	}
	if in.Input == nil {
		return EvaluationRecord{}, fieldError("input", "REQUIRED", "girdi belgesi gerekli")
	}
	hash, err := domain.InputHash(in.Input)
	if err != nil {
		return EvaluationRecord{}, err
	}

	var out EvaluationRecord
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		version, err := s.repo.GetVersion(ctx, tx, rc.TenantID, in.VersionID)
		if err != nil {
			return err
		}
		if version.Status != domain.VersionPublished {
			return ErrVersionNotLive
		}
		rules, err := s.repo.ListRules(ctx, tx, rc.TenantID, in.VersionID)
		if err != nil {
			return err
		}
		program, err := s.programOf(version, rules)
		if err != nil {
			return compileFieldError("rules", err)
		}
		ev := program.Evaluate(ctx, coerceInput(version.InputSchema, in.Input))
		duration := int(ev.Duration.Milliseconds())
		id, _, err := s.repo.CreateEvaluation(ctx, tx, rc.TenantID, NewEvaluationRow{
			SubjectType: in.SubjectType, SubjectID: in.SubjectID, RuleSetVersionID: in.VersionID,
			InputHash: hash,
			// Ids, dates and quantities only. Whatever else the caller passed in stays in
			// the caller's own memory and never reaches this column.
			InputSnapshot: domain.Snapshot(in.Input),
			Outcome:       string(ev.Outcome), DurationMs: &duration,
			EvaluatedBy: actorPtr(rc.Principal.ActorID),
		}, resultRows(ev))
		if err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "rule_evaluation.create", "rule_evaluation", id, map[string]any{
			"rule_set_version_id": in.VersionID, "subject_type": in.SubjectType,
			"outcome": string(ev.Outcome), "rule_count": len(rules),
		}); err != nil {
			return err
		}
		out, err = s.repo.GetEvaluation(ctx, tx, rc.TenantID, id)
		return err
	})
	return out, err
}

// GetEvaluation reads one recorded decision with its whole trace.
func (s *Service) GetEvaluation(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (EvaluationRecord, error) {
	var out EvaluationRecord
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = s.repo.GetEvaluation(ctx, tx, rc.TenantID, id)
		return err
	})
	return out, err
}

// loadForEvaluation reads a version the caller may see, with its rules.
func (s *Service) loadForEvaluation(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	versionID uuid.UUID,
) (VersionRecord, []RuleRecord, error) {
	version, err := s.repo.GetVersion(ctx, tx, rc.TenantID, versionID)
	if err != nil {
		return VersionRecord{}, nil, err
	}
	if !visibleTo(rc, version.Status) {
		return VersionRecord{}, nil, ErrVersionNotFound
	}
	rules, err := s.repo.ListRules(ctx, tx, rc.TenantID, versionID)
	if err != nil {
		return VersionRecord{}, nil, err
	}
	return version, rules, nil
}

func traceOf(version VersionRecord, ev engine.Evaluation) Trace {
	return Trace{
		VersionID: version.ID, VersionNo: version.VersionNo, Status: version.Status,
		Outcome: string(ev.Outcome), DurationMs: int(ev.Duration.Milliseconds()),
		Results: resultRows(ev),
	}
}

func resultRows(ev engine.Evaluation) []EvaluationResultRow {
	out := make([]EvaluationResultRow, 0, len(ev.Results))
	for _, r := range ev.Results {
		row := EvaluationResultRow{
			Sequence: r.Sequence, RuleCode: r.RuleCode, Matched: r.Matched,
			ActionPayload: r.ActionPayload, ExplanationCode: r.ExplanationCode,
			Severity: string(r.Severity),
		}
		if r.RuleID != uuid.Nil {
			id := r.RuleID
			row.RuleID = &id
		}
		if r.ActionType != "" {
			t := r.ActionType
			row.ActionType = &t
		}
		out = append(out, row)
	}
	return out
}

// explanationsOf is what a test case asserts against: the codes of the rules that actually
// said something — the ones that matched, plus any that failed loudly. The INFO line a
// rule leaves behind when it simply did not match is trace, not a statement, and asserting
// on it would make every test case break whenever an unrelated rule was added.
func explanationsOf(ev engine.Evaluation) []string {
	out := make([]string, 0, len(ev.Results))
	seen := make(map[string]bool, len(ev.Results))
	for _, r := range ev.Results {
		if !r.Matched && r.Severity != engine.SeverityError {
			continue
		}
		if seen[r.ExplanationCode] {
			continue
		}
		seen[r.ExplanationCode] = true
		out = append(out, r.ExplanationCode)
	}
	return out
}

// actionsOf is the ordered list of actions the pass produced.
func actionsOf(ev engine.Evaluation) []domain.ActionInput {
	out := make([]domain.ActionInput, 0, len(ev.Results))
	for _, r := range ev.Results {
		if !r.Matched || r.ActionType == "" {
			continue
		}
		out = append(out, domain.ActionInput{Type: r.ActionType, Payload: r.ActionPayload})
	}
	return out
}

// matchActions compares an expectation with what happened. The types must match in order;
// a payload is compared only where the expectation supplied one, so a case may assert
// "this asks for a document" without repeating every field of the request.
func matchActions(expected, actual []domain.ActionInput) bool {
	if len(expected) != len(actual) {
		return false
	}
	for i, e := range expected {
		if e.Type != actual[i].Type {
			return false
		}
		if len(e.Payload) == 0 {
			continue
		}
		if !reflect.DeepEqual(e.Payload, actual[i].Payload) {
			return false
		}
	}
	return true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func actorPtr(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// coerceInput turns a JSON document into the Go values the declared CEL types expect. JSON
// has no timestamp and one number type, so without this a version declaring `serviceDate`
// as a timestamp would see a string and every condition touching it would become an ERROR
// line. A value that cannot be coerced is passed through untouched: the type error then
// comes from CEL itself, naming the rule, which is more use than a message from here.
func coerceInput(schema map[string]string, input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = coerceValue(schema[key], value)
	}
	return out
}

func coerceValue(kind string, value any) any {
	switch kind {
	case "timestamp":
		return coerceTimestamp(value)
	case "duration":
		if s, ok := value.(string); ok {
			if d, err := time.ParseDuration(s); err == nil {
				return d
			}
		}
	case "int":
		switch v := value.(type) {
		case float64:
			if v == float64(int64(v)) {
				return int64(v)
			}
		case int:
			return int64(v)
		}
	case "double":
		switch v := value.(type) {
		case int:
			return float64(v)
		case int64:
			return float64(v)
		}
	}
	return value
}

// coerceTimestamp accepts an ISO day as well as a full RFC 3339 instant, because a service
// date is written as a day everywhere else in the system.
func coerceTimestamp(value any) any {
	s, ok := value.(string)
	if !ok {
		return value
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t.UTC()
	}
	return value
}
