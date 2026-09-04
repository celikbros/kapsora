package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/rules/application"
	"github.com/celikbros/kapsora/internal/rules/domain"
	rulespg "github.com/celikbros/kapsora/internal/rules/infrastructure/postgres"
)

// inputSchema is the environment every fixture version declares: one map of claim facts
// and the service date. A condition naming anything else does not compile, which is what
// several of these tests are about.
var inputSchema = map[string]string{"claim": "map", "serviceDate": "timestamp"}

type fixture struct {
	h       *dbtest.Harness
	svc     *application.Service
	cache   *application.ProgramCache
	tenant  uuid.UUID
	other   uuid.UUID
	maker   uuid.UUID
	checker uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h := dbtest.New(t)
	cursors, err := httpx.NewCursorCodec([]byte("fedcba9876543210fedcba9876543210"))
	if err != nil {
		t.Fatal(err)
	}
	cache := application.NewProgramCache(8)
	svc, err := application.New(application.Deps{
		Pool: h.App, Repo: rulespg.New(), Audit: auditpg.New(), Cursors: cursors, Programs: cache,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{
		h: h, svc: svc, cache: cache,
		tenant: h.CreateTenant("RULES_A"), other: h.CreateTenant("RULES_B"),
		maker:   h.CreateActor("rule-maker", "Rule Maker"),
		checker: h.CreateActor("rule-checker", "Rule Checker"),
	}
}

// rc is a back office actor holding every rule permission with a fresh step-up.
func (f *fixture) rc(actor uuid.UUID) identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal: identity.Principal{ActorID: actor}, StepUpValid: true,
		Permissions: map[string]struct{}{
			"rule.read": {}, "rule.draft": {}, "rule.publish": {},
		},
	}
}

func (f *fixture) makerRC() identity.RequestContext   { return f.rc(f.maker) }
func (f *fixture) checkerRC() identity.RequestContext { return f.rc(f.checker) }

// readerRC holds rule.read only, which is what a draft must stay invisible to.
func (f *fixture) readerRC() identity.RequestContext {
	return identity.RequestContext{
		TenantID: f.tenant, MembershipID: uuid.New(),
		Principal:   identity.Principal{ActorID: f.maker},
		Permissions: map[string]struct{}{"rule.read": {}},
	}
}

func day(t *testing.T, s string) *time.Time {
	t.Helper()
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return &d
}

func (f *fixture) ruleSet(t *testing.T, code string) application.RuleSetRecord {
	t.Helper()
	record, err := f.svc.CreateRuleSet(context.Background(), f.makerRC(), domain.NewRuleSet{
		Code: code, Name: "Kural seti " + code, DomainCode: "HEALTH", Purpose: "DOCUMENT",
	})
	if err != nil {
		t.Fatalf("create rule set %s: %v", code, err)
	}
	return record
}

// version opens a draft over [from, to); an empty to leaves the period open-ended.
func (f *fixture) version(t *testing.T, setID uuid.UUID, from string, to ...string) application.VersionView {
	t.Helper()
	in := application.NewVersionInput{ValidFrom: day(t, from), InputSchema: inputSchema}
	if len(to) == 1 {
		in.ValidTo = day(t, to[0])
	}
	view, err := f.svc.CreateVersion(context.Background(), f.makerRC(), setID, in)
	if err != nil {
		t.Fatalf("create version: %v", err)
	}
	return view
}

// requireDocument is the rule the fixtures use: a claim above the threshold needs an
// invoice, which is the smallest rule that produces a real action and a real outcome.
func requireDocument() domain.RuleInput {
	return domain.RuleInput{
		Code: "INVOICE_REQUIRED", Name: "Fatura zorunlu", Priority: 10,
		Condition: "claim.amount > 100.0", ExplanationCode: "DOCUMENT_REQUIRED",
		Actions: []domain.ActionInput{{
			Type: "REQUIRE_DOCUMENT", Payload: map[string]any{"documentTypeCode": "INVOICE"},
		}},
		Active: true,
	}
}

func passingCase() domain.TestCaseInput {
	return domain.TestCaseInput{
		Code: "OVER_THRESHOLD", Input: map[string]any{"claim": map[string]any{"amount": 150.0}},
		ExpectedOutcome:      "REVIEW_REQUIRED",
		ExpectedExplanations: []string{"DOCUMENT_REQUIRED"},
	}
}

func (f *fixture) putRules(t *testing.T, versionID uuid.UUID, rowVersion int64,
	rules ...domain.RuleInput,
) application.RuleResult {
	t.Helper()
	result, err := f.svc.ReplaceRules(context.Background(), f.makerRC(), versionID, rules, rowVersion)
	if err != nil {
		t.Fatalf("replace rules: %v", err)
	}
	return result
}

func (f *fixture) putCases(t *testing.T, versionID uuid.UUID, rowVersion int64,
	cases ...domain.TestCaseInput,
) application.TestCaseResult {
	t.Helper()
	result, err := f.svc.ReplaceTestCases(context.Background(), f.makerRC(), versionID, cases, rowVersion)
	if err != nil {
		t.Fatalf("replace test cases: %v", err)
	}
	return result
}

// ready is a draft with one rule and one passing case, at the ETag the next command needs.
func (f *fixture) ready(t *testing.T, code, from string, to ...string) (application.VersionView, int64) {
	t.Helper()
	set := f.ruleSet(t, code)
	view := f.version(t, set.ID, from, to...)
	rules := f.putRules(t, view.Version.ID, view.Version.RowVersion, requireDocument())
	cases := f.putCases(t, view.Version.ID, rules.RowVersion, passingCase())
	return view, cases.RowVersion
}

// publish runs the whole maker-checker flow: the maker submits, the checker publishes.
func (f *fixture) publish(t *testing.T, versionID uuid.UUID, rowVersion int64) application.VersionView {
	t.Helper()
	submitted, err := f.svc.SubmitVersion(context.Background(), f.makerRC(), versionID, nil, rowVersion)
	if err != nil {
		t.Fatalf("submit version: %v", err)
	}
	published, err := f.svc.PublishVersion(context.Background(), f.checkerRC(), versionID, nil,
		submitted.Version.RowVersion)
	if err != nil {
		t.Fatalf("publish version: %v", err)
	}
	return published
}

func (f *fixture) countRows(t *testing.T, table string) int {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var n int
	if err := f.h.Admin.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func fieldCodes(t *testing.T, err error) map[string]string {
	t.Helper()
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected a validation error, got %v", err)
	}
	out := make(map[string]string, len(ve.Fields))
	for _, f := range ve.Fields {
		out[f.Field] = f.Code
	}
	return out
}

func fieldMessage(t *testing.T, err error, field string) string {
	t.Helper()
	var ve *domain.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected a validation error, got %v", err)
	}
	for _, f := range ve.Fields {
		if f.Field == field {
			return f.Message
		}
	}
	t.Fatalf("no error on field %s: %v", field, ve.Fields)
	return ""
}

// TestSubmitRefusesAVersionWithNoTestCase is the first line of the publish gate: an
// untested rule set cannot even be offered for review.
func TestSubmitRefusesAVersionWithNoTestCase(t *testing.T) {
	f := newFixture(t)
	set := f.ruleSet(t, "NO_TESTS")
	view := f.version(t, set.ID, "2026-01-01")
	rules := f.putRules(t, view.Version.ID, view.Version.RowVersion, requireDocument())

	_, err := f.svc.SubmitVersion(context.Background(), f.makerRC(), view.Version.ID, nil, rules.RowVersion)
	if got := fieldCodes(t, err)["testCases"]; got != "TESTS_REQUIRED" {
		t.Fatalf("submit without tests answered %q, want TESTS_REQUIRED", got)
	}
}

// TestSubmitRefusesAFailingTestCase and names it, so an author does not have to guess
// which of forty cases broke.
func TestSubmitRefusesAFailingTestCase(t *testing.T) {
	f := newFixture(t)
	set := f.ruleSet(t, "FAILING_TEST")
	view := f.version(t, set.ID, "2026-01-01")
	rules := f.putRules(t, view.Version.ID, view.Version.RowVersion, requireDocument())
	failing := passingCase()
	failing.Code = "SHOULD_APPROVE"
	failing.ExpectedOutcome = "APPROVED"
	cases := f.putCases(t, view.Version.ID, rules.RowVersion, passingCase(), failing)

	_, err := f.svc.SubmitVersion(context.Background(), f.makerRC(), view.Version.ID, nil, cases.RowVersion)
	if got := fieldCodes(t, err)["testCases"]; got != "TESTS_FAILING" {
		t.Fatalf("submit with a failing test answered %q, want TESTS_FAILING", got)
	}
	message := fieldMessage(t, err, "testCases")
	if !strings.Contains(message, "SHOULD_APPROVE") {
		t.Fatalf("refusal %q does not name the failing case", message)
	}
	if strings.Contains(message, "OVER_THRESHOLD") {
		t.Fatalf("refusal %q names a case that passed", message)
	}
}

// TestPublishNeedsASecondActor: the person who asked for the review may not grant it.
func TestPublishNeedsASecondActor(t *testing.T) {
	f := newFixture(t)
	view, rowVersion := f.ready(t, "MAKER_CHECKER", "2026-01-01")

	submitted, err := f.svc.SubmitVersion(context.Background(), f.makerRC(), view.Version.ID, nil, rowVersion)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	_, err = f.svc.PublishVersion(context.Background(), f.makerRC(), view.Version.ID, nil,
		submitted.Version.RowVersion)
	if !errors.Is(err, application.ErrMakerCheckerSame) {
		t.Fatalf("self publish returned %v, want ErrMakerCheckerSame", err)
	}

	// The refusal is audited in its own transaction, because the command's rolled back.
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var denied int
	if err := f.h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM audit.event
		 WHERE action_code = 'rule_set_version.publish' AND outcome = 'DENIED'
		   AND reason_code = 'MAKER_CHECKER_SAME_ACTOR'`).Scan(&denied); err != nil {
		t.Fatalf("count denials: %v", err)
	}
	if denied != 1 {
		t.Fatalf("%d denial rows recorded, want 1", denied)
	}

	published, err := f.svc.PublishVersion(context.Background(), f.checkerRC(), view.Version.ID, nil,
		submitted.Version.RowVersion)
	if err != nil {
		t.Fatalf("publish by the checker: %v", err)
	}
	if published.Version.Status != domain.VersionPublished {
		t.Fatalf("status is %s, want PUBLISHED", published.Version.Status)
	}
	if published.Version.ContentHash == nil || len(*published.Version.ContentHash) != 64 {
		t.Fatalf("content hash is %v, want 64 hex characters", published.Version.ContentHash)
	}
}

// TestPublishedVersionRefusesEveryWrite: what decided a claim cannot move afterwards, in
// the version row and in both of its child sets.
func TestPublishedVersionRefusesEveryWrite(t *testing.T) {
	f := newFixture(t)
	view, rowVersion := f.ready(t, "IMMUTABLE", "2026-01-01")
	published := f.publish(t, view.Version.ID, rowVersion)

	at := published.Version.RowVersion
	notes := "sonradan"
	writes := map[string]func() error{
		"patch": func() error {
			_, err := f.svc.UpdateVersion(context.Background(), f.makerRC(), view.Version.ID,
				application.VersionPatch{Notes: &notes, ExpectedVersion: at})
			return err
		},
		"rules": func() error {
			_, err := f.svc.ReplaceRules(context.Background(), f.makerRC(), view.Version.ID,
				[]domain.RuleInput{requireDocument()}, at)
			return err
		},
		"test cases": func() error {
			_, err := f.svc.ReplaceTestCases(context.Background(), f.makerRC(), view.Version.ID,
				[]domain.TestCaseInput{passingCase()}, at)
			return err
		},
	}
	for what, write := range writes {
		if err := write(); !errors.Is(err, application.ErrVersionImmutable) {
			t.Fatalf("%s on a published version returned %v, want ErrVersionImmutable", what, err)
		}
	}
}

// TestPublishedVersionsMayNotOverlap maps the exclusion constraint onto its problem code.
func TestPublishedVersionsMayNotOverlap(t *testing.T) {
	f := newFixture(t)
	set := f.ruleSet(t, "OVERLAP")

	first := f.version(t, set.ID, "2026-01-01")
	rules := f.putRules(t, first.Version.ID, first.Version.RowVersion, requireDocument())
	cases := f.putCases(t, first.Version.ID, rules.RowVersion, passingCase())
	f.publish(t, first.Version.ID, cases.RowVersion)

	second := f.version(t, set.ID, "2026-06-01")
	rules = f.putRules(t, second.Version.ID, second.Version.RowVersion, requireDocument())
	cases = f.putCases(t, second.Version.ID, rules.RowVersion, passingCase())
	submitted, err := f.svc.SubmitVersion(context.Background(), f.makerRC(), second.Version.ID, nil, cases.RowVersion)
	if err != nil {
		t.Fatalf("submit second version: %v", err)
	}
	_, err = f.svc.PublishVersion(context.Background(), f.checkerRC(), second.Version.ID, nil,
		submitted.Version.RowVersion)
	if !errors.Is(err, application.ErrVersionOverlap) {
		t.Fatalf("overlapping publish returned %v, want ErrVersionOverlap", err)
	}
}

// TestPutRulesRefusesAConditionThatDoesNotCompile, naming the rule and quoting the
// compiler, because an author writing CEL is owed the real error.
func TestPutRulesRefusesAConditionThatDoesNotCompile(t *testing.T) {
	f := newFixture(t)
	set := f.ruleSet(t, "COMPILE")
	view := f.version(t, set.ID, "2026-01-01")

	broken := requireDocument()
	broken.Code = "UNDECLARED"
	broken.Priority = 20
	broken.Condition = "member.age > 18"
	_, err := f.svc.ReplaceRules(context.Background(), f.makerRC(), view.Version.ID,
		[]domain.RuleInput{requireDocument(), broken}, view.Version.RowVersion)
	if got := fieldCodes(t, err)["items[1].condition"]; got != "RULE_COMPILE_FAILED" {
		t.Fatalf("compile failure answered %q on items[1].condition, want RULE_COMPILE_FAILED: %v", got, err)
	}
	message := fieldMessage(t, err, "items[1].condition")
	if !strings.Contains(message, "UNDECLARED") || !strings.Contains(message, "member") {
		t.Fatalf("message %q names neither the rule nor the undeclared variable", message)
	}

	// Nothing was written: a set that cannot compile must not reach the table at all.
	if n := f.countRows(t, "rules.rule"); n != 0 {
		t.Fatalf("%d rules stored after a refused write, want 0", n)
	}

	// A condition that is not a boolean is refused for the same reason and in the
	// same shape.
	notBoolean := requireDocument()
	notBoolean.Code = "NOT_BOOLEAN"
	notBoolean.Condition = `"yes"`
	_, err = f.svc.ReplaceRules(context.Background(), f.makerRC(), view.Version.ID,
		[]domain.RuleInput{notBoolean}, view.Version.RowVersion)
	if got := fieldCodes(t, err)["items[0].condition"]; got != "RULE_COMPILE_FAILED" {
		t.Fatalf("non-boolean condition answered %q, want RULE_COMPILE_FAILED: %v", got, err)
	}
}

// TestPutRulesRefusesADuplicatePriority before the database has to: evaluation order must
// be total, and the message names the other rule.
func TestPutRulesRefusesADuplicatePriority(t *testing.T) {
	f := newFixture(t)
	set := f.ruleSet(t, "PRIORITY")
	view := f.version(t, set.ID, "2026-01-01")

	second := requireDocument()
	second.Code = "SECOND"
	_, err := f.svc.ReplaceRules(context.Background(), f.makerRC(), view.Version.ID,
		[]domain.RuleInput{requireDocument(), second}, view.Version.RowVersion)
	if got := fieldCodes(t, err)["items[1].priority"]; got != "DUPLICATE" {
		t.Fatalf("duplicate priority answered %q, want DUPLICATE: %v", got, err)
	}
}

// TestPutRulesRefusesAMalformedActionPayload: a malformed action is rejected at authoring
// time and not at three in the morning by the caller that has to perform it.
func TestPutRulesRefusesAMalformedActionPayload(t *testing.T) {
	f := newFixture(t)
	set := f.ruleSet(t, "PAYLOAD")
	view := f.version(t, set.ID, "2026-01-01")

	cases := map[string]struct {
		action domain.ActionInput
		field  string
		code   string
	}{
		"missing required field": {
			action: domain.ActionInput{Type: "REQUIRE_DOCUMENT"},
			field:  "items[0].actions[0].payload.documentTypeCode", code: "REQUIRED",
		},
		"unknown field": {
			action: domain.ActionInput{Type: "REQUIRE_DOCUMENT", Payload: map[string]any{
				"documentTypeCode": "INVOICE", "documentTypeCod": "INVOICE",
			}},
			field: "items[0].actions[0].payload.documentTypeCod", code: "UNKNOWN_FIELD",
		},
		"a number where an exact decimal belongs": {
			action: domain.ActionInput{Type: "SET_LIMIT", Payload: map[string]any{"amount": 100.0}},
			field:  "items[0].actions[0].payload.amount", code: "TYPE",
		},
		"unknown action type": {
			action: domain.ActionInput{Type: "DELETE_EVERYTHING"},
			field:  "items[0].actions[0].type", code: "ENUM",
		},
		"both sides of an exclusive choice": {
			action: domain.ActionInput{Type: "PARTIAL_APPROVE", Payload: map[string]any{
				"percent": "50", "amount": "100",
			}},
			field: "items[0].actions[0].payload", code: "REQUIRED",
		},
	}
	for what, tc := range cases {
		rule := requireDocument()
		rule.Actions = []domain.ActionInput{tc.action}
		_, err := f.svc.ReplaceRules(context.Background(), f.makerRC(), view.Version.ID,
			[]domain.RuleInput{rule}, view.Version.RowVersion)
		if got := fieldCodes(t, err)[tc.field]; got != tc.code {
			t.Fatalf("%s answered %q on %s, want %s: %v", what, got, tc.field, tc.code, err)
		}
	}
}

// TestSimulationWritesNothing is the acceptance criterion in its own right: simulating
// production data leaves no row anywhere.
func TestSimulationWritesNothing(t *testing.T) {
	f := newFixture(t)
	view, rowVersion := f.ready(t, "SIMULATE", "2026-01-01")
	published := f.publish(t, view.Version.ID, rowVersion)

	before := f.countRows(t, "rules.evaluation")
	beforeResults := f.countRows(t, "rules.evaluation_result")
	beforeAudit := f.countRows(t, "audit.event")

	trace, err := f.svc.Simulate(context.Background(), f.makerRC(), published.Version.ID,
		map[string]any{"claim": map[string]any{"amount": 150.0}})
	if err != nil {
		t.Fatalf("simulate: %v", err)
	}
	if trace.Outcome != "REVIEW_REQUIRED" {
		t.Fatalf("simulated outcome is %s, want REVIEW_REQUIRED", trace.Outcome)
	}
	if len(trace.Results) == 0 {
		t.Fatal("simulation returned no trace")
	}

	if after := f.countRows(t, "rules.evaluation"); after != before {
		t.Fatalf("simulation wrote %d evaluation rows", after-before)
	}
	if after := f.countRows(t, "rules.evaluation_result"); after != beforeResults {
		t.Fatalf("simulation wrote %d evaluation result rows", after-beforeResults)
	}
	if after := f.countRows(t, "audit.event"); after != beforeAudit {
		t.Fatalf("simulation wrote %d audit rows", after-beforeAudit)
	}

	// The stored test run is the same promise.
	if _, err := f.svc.RunTests(context.Background(), f.makerRC(), published.Version.ID); err != nil {
		t.Fatalf("run tests: %v", err)
	}
	if after := f.countRows(t, "rules.evaluation"); after != before {
		t.Fatalf("the test run wrote %d evaluation rows", after-before)
	}
}

// TestSimulationRunsAgainstADraft, which is the point of it: an author wants to see what
// the version they are writing would decide, before anybody approves it.
func TestSimulationRunsAgainstADraft(t *testing.T) {
	f := newFixture(t)
	view, _ := f.ready(t, "DRAFT_SIM", "2026-01-01")

	trace, err := f.svc.Simulate(context.Background(), f.makerRC(), view.Version.ID,
		map[string]any{"claim": map[string]any{"amount": 10.0}})
	if err != nil {
		t.Fatalf("simulate a draft: %v", err)
	}
	if trace.Outcome != "APPROVED" {
		t.Fatalf("outcome is %s, want APPROVED", trace.Outcome)
	}
	if trace.Status != domain.VersionDraft {
		t.Fatalf("trace reports status %s, want DRAFT", trace.Status)
	}
	// A draft is never cached: the author is changing it as they read the trace.
	if n := f.cache.Len(); n != 0 {
		t.Fatalf("the cache holds %d programs after simulating a draft, want 0", n)
	}

	// A caller with rule.read alone cannot see the draft at all, so it cannot simulate it
	// either; the version is hidden rather than refused.
	if _, err := f.svc.Simulate(context.Background(), f.readerRC(), view.Version.ID,
		map[string]any{"claim": map[string]any{"amount": 10.0}}); !errors.Is(err, application.ErrVersionNotFound) {
		t.Fatalf("read-only simulation of a draft returned %v, want ErrVersionNotFound", err)
	}
}

// TestEvaluateRecordsTheDecisionAndCachesThePublishedProgram.
func TestEvaluateRecordsTheDecisionAndCachesThePublishedProgram(t *testing.T) {
	f := newFixture(t)
	view, rowVersion := f.ready(t, "EVALUATE", "2026-01-01")
	published := f.publish(t, view.Version.ID, rowVersion)

	subject := uuid.New()
	record, err := f.svc.Evaluate(context.Background(), f.makerRC(), application.EvaluateInput{
		VersionID: published.Version.ID, SubjectType: "CLAIM", SubjectID: &subject,
		Input: map[string]any{"claim": map[string]any{"amount": 150.0}},
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if record.Outcome != "REVIEW_REQUIRED" {
		t.Fatalf("outcome is %s, want REVIEW_REQUIRED", record.Outcome)
	}
	if len(record.Results) != 1 || record.Results[0].RuleCode != "INVOICE_REQUIRED" {
		t.Fatalf("trace is %+v, want one INVOICE_REQUIRED line", record.Results)
	}
	if record.Results[0].ActionType == nil || *record.Results[0].ActionType != "REQUIRE_DOCUMENT" {
		t.Fatalf("action type is %v, want REQUIRE_DOCUMENT", record.Results[0].ActionType)
	}
	if len(record.InputHash) != 32 {
		t.Fatalf("input hash is %d bytes, want 32", len(record.InputHash))
	}
	if record.RuleSetCode == "" || record.VersionNo != published.Version.VersionNo {
		t.Fatalf("evaluation does not name its version: %+v", record)
	}
	if n := f.cache.Len(); n != 1 {
		t.Fatalf("the cache holds %d programs after evaluating a published version, want 1", n)
	}

	// The same input twice produces the same hash, which is what makes two decisions
	// comparable without keeping the input itself.
	again, err := f.svc.Evaluate(context.Background(), f.makerRC(), application.EvaluateInput{
		VersionID: published.Version.ID, SubjectType: "CLAIM", SubjectID: &subject,
		Input: map[string]any{"claim": map[string]any{"amount": 150.0}},
	})
	if err != nil {
		t.Fatalf("evaluate again: %v", err)
	}
	if string(again.InputHash) != string(record.InputHash) {
		t.Fatal("the same input produced two different hashes")
	}

	fetched, err := f.svc.GetEvaluation(context.Background(), f.readerRC(), record.ID)
	if err != nil {
		t.Fatalf("get evaluation: %v", err)
	}
	if fetched.ID != record.ID || len(fetched.Results) != len(record.Results) {
		t.Fatalf("re-read evaluation differs: %+v", fetched)
	}
}

// TestEvaluateRefusesADraft: an unapproved rule must never be the reason a member was told
// no. A draft is simulated, not recorded.
func TestEvaluateRefusesADraft(t *testing.T) {
	f := newFixture(t)
	view, _ := f.ready(t, "DRAFT_EVAL", "2026-01-01")

	_, err := f.svc.Evaluate(context.Background(), f.makerRC(), application.EvaluateInput{
		VersionID: view.Version.ID, SubjectType: "CLAIM",
		Input: map[string]any{"claim": map[string]any{"amount": 150.0}},
	})
	if !errors.Is(err, application.ErrVersionNotLive) {
		t.Fatalf("evaluating a draft returned %v, want ErrVersionNotLive", err)
	}
	if n := f.countRows(t, "rules.evaluation"); n != 0 {
		t.Fatalf("%d evaluation rows written for a refused draft, want 0", n)
	}
}

// TestEvaluationSnapshotHoldsNoIdentityNumberAndNoName reads the stored JSON back and
// scans it. This is the promise the column's comment makes, and the only way to keep it is
// to check the bytes that were actually written.
func TestEvaluationSnapshotHoldsNoIdentityNumberAndNoName(t *testing.T) {
	f := newFixture(t)
	view, rowVersion := f.ready(t, "SNAPSHOT", "2026-01-01")
	published := f.publish(t, view.Version.ID, rowVersion)

	claimID := uuid.New().String()
	record, err := f.svc.Evaluate(context.Background(), f.makerRC(), application.EvaluateInput{
		VersionID: published.Version.ID, SubjectType: "CLAIM",
		Input: map[string]any{
			"claim": map[string]any{
				"amount":       150.0,
				"claimId":      claimID,
				"serviceDate":  "2026-03-15",
				"serviceCode":  "PHYSIO_SESSION",
				"sessionCount": 4.0,
				// Everything below must not survive the filter.
				"memberName":     "Ayşe Yılmaz",
				"holderFullName": "Ayşe Yılmaz",
				"tckn":           "10000000146",
				"nationalNumber": "10000000146",
				"bareIdentity":   10000000146.0,
				"note":           "Hasta 3. kattaki odada yatıyor",
				"phoneNumber":    "05321234567",
			},
		},
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}

	ctx, cancel := f.h.Ctx()
	defer cancel()
	var stored string
	if err := f.h.Admin.QueryRow(ctx,
		`SELECT input_snapshot::text FROM rules.evaluation WHERE id = $1`, record.ID).Scan(&stored); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	for _, forbidden := range []string{
		"Ayşe", "Yılmaz", "10000000146", "05321234567", "Hasta", "memberName",
		"holderFullName", "tckn", "nationalNumber", "phoneNumber",
	} {
		if strings.Contains(stored, forbidden) {
			t.Fatalf("stored snapshot %s contains %q", stored, forbidden)
		}
	}
	// What an auditor actually needs is still there.
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(stored), &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	claim, ok := snapshot["claim"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot lost the claim entirely: %s", stored)
	}
	for _, kept := range []string{"amount", "claimId", "serviceDate", "serviceCode", "sessionCount"} {
		if _, present := claim[kept]; !present {
			t.Fatalf("snapshot dropped %s, which an auditor needs: %s", kept, stored)
		}
	}
	if claim["claimId"] != claimID {
		t.Fatalf("snapshot changed the claim id: %v", claim["claimId"])
	}
	// A number long enough to be an identity is dropped even when its key says nothing.
	if _, present := claim["bareIdentity"]; present {
		t.Fatalf("snapshot kept an eleven digit number: %s", stored)
	}
}

// TestRunTestsReportsExpectedVersusActual, which is what an author reads while writing.
func TestRunTestsReportsExpectedVersusActual(t *testing.T) {
	f := newFixture(t)
	set := f.ruleSet(t, "TEST_RUN")
	view := f.version(t, set.ID, "2026-01-01")
	rules := f.putRules(t, view.Version.ID, view.Version.RowVersion, requireDocument())

	wrong := passingCase()
	wrong.Code = "WRONG_OUTCOME"
	wrong.ExpectedOutcome = "REJECTED"
	wrong.ExpectedExplanations = []string{"SOMETHING_ELSE"}
	assertsActions := domain.TestCaseInput{
		Code: "ASSERTS_ACTIONS", Input: map[string]any{"claim": map[string]any{"amount": 150.0}},
		ExpectedOutcome: "REVIEW_REQUIRED", ExpectedExplanations: []string{"DOCUMENT_REQUIRED"},
		ExpectedActions: []domain.ActionInput{{Type: "REQUIRE_DOCUMENT"}}, AssertsActions: true,
	}
	f.putCases(t, view.Version.ID, rules.RowVersion, passingCase(), wrong, assertsActions)

	run, err := f.svc.RunTests(context.Background(), f.makerRC(), view.Version.ID)
	if err != nil {
		t.Fatalf("run tests: %v", err)
	}
	if run.Total != 3 || run.Failed != 1 || run.Passed {
		t.Fatalf("run is total=%d failed=%d passed=%v, want 3/1/false", run.Total, run.Failed, run.Passed)
	}
	byCode := make(map[string]application.TestCaseOutcome, len(run.Cases))
	for _, c := range run.Cases {
		byCode[c.Code] = c
	}
	failed := byCode["WRONG_OUTCOME"]
	if failed.ActualOutcome != "REVIEW_REQUIRED" {
		t.Fatalf("actual outcome is %s, want REVIEW_REQUIRED", failed.ActualOutcome)
	}
	if len(failed.Mismatches) != 2 {
		t.Fatalf("mismatches are %v, want both OUTCOME and EXPLANATIONS", failed.Mismatches)
	}
	if !byCode["ASSERTS_ACTIONS"].Passed {
		t.Fatalf("the action assertion failed: %+v", byCode["ASSERTS_ACTIONS"])
	}
	if !byCode["OVER_THRESHOLD"].Passed {
		t.Fatalf("the passing case failed: %+v", byCode["OVER_THRESHOLD"])
	}
}

// TestStopOnMatchAndPriorityOrderSurviveTheRoundTrip: the order and the early exit are
// properties of the stored rows, not only of the evaluator's unit tests.
func TestStopOnMatchAndPriorityOrderSurviveTheRoundTrip(t *testing.T) {
	f := newFixture(t)
	set := f.ruleSet(t, "ORDER")
	view := f.version(t, set.ID, "2026-01-01")

	first := domain.RuleInput{
		Code: "REJECT_DENTAL", Name: "Diş reddi", Priority: 5,
		Condition: `claim.type == "DENTAL"`, ExplanationCode: "NOT_COVERED",
		Actions:     []domain.ActionInput{{Type: "REJECT", Payload: map[string]any{"reasonCode": "NOT_COVERED"}}},
		StopOnMatch: true, Active: true,
	}
	inactive := domain.RuleInput{
		Code: "NEVER_RUNS", Name: "Kapalı kural", Priority: 7,
		Condition: "claim.amount > 0.0", ExplanationCode: "DISABLED", Active: false,
	}
	f.putRules(t, view.Version.ID, view.Version.RowVersion, requireDocument(), first, inactive)

	trace, err := f.svc.Simulate(context.Background(), f.makerRC(), view.Version.ID,
		map[string]any{"claim": map[string]any{"type": "DENTAL", "amount": 150.0}})
	if err != nil {
		t.Fatalf("simulate: %v", err)
	}
	if trace.Outcome != "REJECTED" {
		t.Fatalf("outcome is %s, want REJECTED", trace.Outcome)
	}
	// Priority 5 runs first and stops the pass, so the document rule at 10 is never
	// reached even though it would have matched.
	if len(trace.Results) != 1 || trace.Results[0].RuleCode != "REJECT_DENTAL" {
		t.Fatalf("trace is %+v, want the one stopping rule", trace.Results)
	}
	// An inactive rule sharing the priority is allowed, and never evaluated.
	for _, r := range trace.Results {
		if r.RuleCode == "NEVER_RUNS" {
			t.Fatal("an inactive rule was evaluated")
		}
	}
}

// TestARuleThatThrowsBecomesAnErrorLine rather than aborting the pass: one broken rule
// must not silently swallow the ones that would have fired after it.
func TestARuleThatThrowsBecomesAnErrorLine(t *testing.T) {
	f := newFixture(t)
	set := f.ruleSet(t, "THROWS")
	view := f.version(t, set.ID, "2026-01-01")

	throws := domain.RuleInput{
		Code: "MISSING_KEY", Name: "Olmayan alan", Priority: 1,
		Condition: "claim.missing.deeper == true", ExplanationCode: "BROKEN", Active: true,
	}
	f.putRules(t, view.Version.ID, view.Version.RowVersion, throws, requireDocument())

	trace, err := f.svc.Simulate(context.Background(), f.makerRC(), view.Version.ID,
		map[string]any{"claim": map[string]any{"amount": 150.0}})
	if err != nil {
		t.Fatalf("simulate: %v", err)
	}
	if len(trace.Results) != 2 {
		t.Fatalf("trace is %+v, want both rules considered", trace.Results)
	}
	if trace.Results[0].Severity != "ERROR" || trace.Results[0].ExplanationCode != "RULE_ERROR" {
		t.Fatalf("first line is %+v, want a RULE_ERROR at ERROR severity", trace.Results[0])
	}
	if trace.Outcome != "REVIEW_REQUIRED" {
		t.Fatalf("outcome is %s: the rule after the broken one did not run", trace.Outcome)
	}
}

// TestCopyFromVersionCopiesRulesAndTestCases, which is how a revision starts from what is
// live rather than from an empty page.
func TestCopyFromVersionCopiesRulesAndTestCases(t *testing.T) {
	f := newFixture(t)
	view, rowVersion := f.ready(t, "COPY", "2026-01-01", "2027-01-01")
	published := f.publish(t, view.Version.ID, rowVersion)

	next, err := f.svc.CreateVersion(context.Background(), f.makerRC(), published.Version.RuleSetID,
		application.NewVersionInput{CopyFromVersionID: &published.Version.ID, ValidFrom: day(t, "2027-01-01")})
	if err != nil {
		t.Fatalf("create a copy: %v", err)
	}
	if len(next.Rules) != 1 || next.Rules[0].Code != "INVOICE_REQUIRED" {
		t.Fatalf("rules were not copied: %+v", next.Rules)
	}
	if len(next.TestCases) != 1 || next.TestCases[0].Code != "OVER_THRESHOLD" {
		t.Fatalf("test cases were not copied: %+v", next.TestCases)
	}
	if next.Version.InputSchema["claim"] != "map" {
		t.Fatalf("the input schema was not copied: %v", next.Version.InputSchema)
	}
	if next.Rules[0].ID == view.Version.ID {
		t.Fatal("the copy reused the source rule row")
	}
	// The copy carries the same rules, so it hashes to the same content as its source
	// once it is published: what was agreed is what is being re-agreed.
	copied := f.publish(t, next.Version.ID, next.Version.RowVersion)
	if copied.Version.ContentHash == nil || published.Version.ContentHash == nil {
		t.Fatal("a published version carries no content hash")
	}
	if *copied.Version.ContentHash == *published.Version.ContentHash {
		t.Fatal("two versions covering different periods hashed identically")
	}
}

// TestPatchRefusesToWithdrawAVariableARuleStillNames: narrowing the environment under
// written rules would leave the version unable to compile.
func TestPatchRefusesToWithdrawAVariableARuleStillNames(t *testing.T) {
	f := newFixture(t)
	view, rowVersion := f.ready(t, "SCHEMA", "2026-01-01")

	_, err := f.svc.UpdateVersion(context.Background(), f.makerRC(), view.Version.ID,
		application.VersionPatch{InputSchema: map[string]string{}, ExpectedVersion: rowVersion})
	if got := fieldCodes(t, err)["inputSchema"]; got != "RULE_COMPILE_FAILED" {
		t.Fatalf("withdrawing a used variable answered %q, want RULE_COMPILE_FAILED: %v", got, err)
	}

	// Adding a variable is fine: nothing that compiled stops compiling.
	widened := map[string]string{"claim": "map", "serviceDate": "timestamp", "provider": "map"}
	if _, err := f.svc.UpdateVersion(context.Background(), f.makerRC(), view.Version.ID,
		application.VersionPatch{InputSchema: widened, ExpectedVersion: rowVersion}); err != nil {
		t.Fatalf("widening the schema: %v", err)
	}
}

// TestDraftsAreHiddenFromAReadOnlyCaller: an unapproved rule set is a proposal, and
// hiding it is different from refusing it, which would confirm that it exists.
func TestDraftsAreHiddenFromAReadOnlyCaller(t *testing.T) {
	f := newFixture(t)
	view, rowVersion := f.ready(t, "VISIBILITY", "2026-01-01")

	if _, err := f.svc.GetVersion(context.Background(), f.readerRC(), view.Version.ID); !errors.Is(err, application.ErrVersionNotFound) {
		t.Fatalf("reading a draft as a read-only caller returned %v, want ErrVersionNotFound", err)
	}
	versions, err := f.svc.ListVersions(context.Background(), f.readerRC(), view.Version.RuleSetID)
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 0 {
		t.Fatalf("a read-only caller sees %d unpublished versions, want 0", len(versions))
	}

	f.publish(t, view.Version.ID, rowVersion)
	if _, err := f.svc.GetVersion(context.Background(), f.readerRC(), view.Version.ID); err != nil {
		t.Fatalf("reading a published version as a read-only caller: %v", err)
	}
}

// TestRuleSetsAreTenantScoped: the list of one tenant never leaks into another's.
func TestRuleSetsAreTenantScoped(t *testing.T) {
	f := newFixture(t)
	f.ruleSet(t, "TENANT_SCOPED")

	otherRC := identity.RequestContext{
		TenantID: f.other, MembershipID: uuid.New(),
		Principal:   identity.Principal{ActorID: f.maker},
		Permissions: map[string]struct{}{"rule.read": {}, "rule.draft": {}},
	}
	page, err := f.svc.ListRuleSets(context.Background(), otherRC, application.ListFilter{})
	if err != nil {
		t.Fatalf("list in the other tenant: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("the other tenant sees %d rule sets, want 0", len(page.Items))
	}

	// The same code is free in a second tenant, and taken in the first.
	if _, err := f.svc.CreateRuleSet(context.Background(), otherRC, domain.NewRuleSet{
		Code: "TENANT_SCOPED", Name: "Aynı kod", DomainCode: "HEALTH", Purpose: "DOCUMENT",
	}); err != nil {
		t.Fatalf("same code in another tenant: %v", err)
	}
	if _, err := f.svc.CreateRuleSet(context.Background(), f.makerRC(), domain.NewRuleSet{
		Code: "TENANT_SCOPED", Name: "Aynı kod", DomainCode: "HEALTH", Purpose: "DOCUMENT",
	}); !errors.Is(err, application.ErrRuleSetCodeTaken) {
		t.Fatalf("duplicate code returned %v, want ErrRuleSetCodeTaken", err)
	}
}

// TestRetireKeepsAPublishedVersionReadable: every evaluation names the version that
// produced it, so retiring must not take it away.
func TestRetireKeepsAPublishedVersionReadable(t *testing.T) {
	f := newFixture(t)
	view, rowVersion := f.ready(t, "RETIRE", "2026-01-01")
	published := f.publish(t, view.Version.ID, rowVersion)

	retired, err := f.svc.RetireVersion(context.Background(), f.checkerRC(), published.Version.ID,
		"SUPERSEDED", nil, published.Version.RowVersion)
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	if retired.Version.Status != domain.VersionRetired {
		t.Fatalf("status is %s, want RETIRED", retired.Version.Status)
	}
	if _, err := f.svc.GetVersion(context.Background(), f.readerRC(), published.Version.ID); err != nil {
		t.Fatalf("a retired version is unreadable: %v", err)
	}
	// A retired version decides nothing new.
	if _, err := f.svc.Evaluate(context.Background(), f.makerRC(), application.EvaluateInput{
		VersionID: published.Version.ID, SubjectType: "CLAIM",
		Input: map[string]any{"claim": map[string]any{"amount": 150.0}},
	}); !errors.Is(err, application.ErrVersionNotLive) {
		t.Fatalf("evaluating a retired version returned %v, want ErrVersionNotLive", err)
	}
}

// TestEveryWriteNeedsTheCurrentETag.
func TestEveryWriteNeedsTheCurrentETag(t *testing.T) {
	f := newFixture(t)
	view, rowVersion := f.ready(t, "ETAG", "2026-01-01")

	stale := rowVersion - 1
	if _, err := f.svc.ReplaceRules(context.Background(), f.makerRC(), view.Version.ID,
		[]domain.RuleInput{requireDocument()}, stale); !errors.Is(err, application.ErrVersionMismatch) {
		t.Fatalf("a stale If-Match on the rules returned %v, want ErrVersionMismatch", err)
	}
	if _, err := f.svc.SubmitVersion(context.Background(), f.makerRC(), view.Version.ID, nil,
		stale); !errors.Is(err, application.ErrVersionMismatch) {
		t.Fatalf("a stale If-Match on submit returned %v, want ErrVersionMismatch", err)
	}
}
