package engine_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/rules/engine"
)

var version = uuid.MustParse("55555555-5555-4555-8555-555555555555")

func schema() map[string]string {
	return map[string]string{
		"age":       "int",
		"amount":    "double",
		"person":    "map",
		"serviceOn": "timestamp",
		"codes":     "list",
	}
}

func rule(code string, priority int, condition string, actions ...engine.Action) engine.Rule {
	return engine.Rule{
		ID: uuid.New(), Code: code, Priority: priority, Condition: condition,
		Actions: actions, ExplanationCode: "EXPLAIN_" + code, Active: true,
	}
}

func act(t string) engine.Action { return engine.Action{Type: t} }

func input() map[string]any {
	return map[string]any{
		"age":       int64(30),
		"amount":    1500.0,
		"person":    map[string]any{"status": "ACTIVE"},
		"serviceOn": time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
		"codes":     []any{"A", "B"},
	}
}

func compile(t *testing.T, rules ...engine.Rule) *engine.Program {
	t.Helper()
	p, err := engine.Compile(version, schema(), rules)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return p
}

func TestRulesRunInAscendingPriorityOrder(t *testing.T) {
	p := compile(t,
		rule("THIRD", 30, "true"),
		rule("FIRST", 10, "true"),
		rule("SECOND", 20, "true"),
	)
	ev := p.Evaluate(context.Background(), input())
	got := make([]string, 0, len(ev.Results))
	for _, r := range ev.Results {
		got = append(got, r.RuleCode)
	}
	want := []string{"FIRST", "SECOND", "THIRD"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i, r := range ev.Results {
		if r.Sequence != i+1 {
			t.Fatalf("sequence %d at position %d", r.Sequence, i)
		}
	}
}

func TestStopOnMatchEndsThePass(t *testing.T) {
	stopper := rule("STOP", 10, "true", act(engine.ActionReject))
	stopper.StopOnMatch = true
	p := compile(t, stopper, rule("NEVER", 20, "true", act(engine.ActionApprove)))

	ev := p.Evaluate(context.Background(), input())
	if len(ev.Results) != 1 || ev.Results[0].RuleCode != "STOP" {
		t.Fatalf("results = %+v", ev.Results)
	}
	if ev.Outcome != engine.OutcomeRejected {
		t.Fatalf("outcome = %s", ev.Outcome)
	}
}

// One broken rule must not swallow the rules that would have fired after it.
func TestAFailingConditionBecomesAnErrorLineAndThePassContinues(t *testing.T) {
	p := compile(t,
		// person has no "missing" key, so the lookup fails at run time, not compile time.
		rule("BROKEN", 10, `person.missing == "x"`),
		rule("AFTER", 20, "true", act(engine.ActionWarn)),
	)
	ev := p.Evaluate(context.Background(), input())
	if len(ev.Results) != 2 {
		t.Fatalf("results = %+v", ev.Results)
	}
	if ev.Results[0].ExplanationCode != engine.ExplanationError || ev.Results[0].Severity != engine.SeverityError {
		t.Fatalf("broken rule = %+v", ev.Results[0])
	}
	if ev.Results[0].Matched {
		t.Fatal("a rule that threw did not match")
	}
	if ev.Results[1].RuleCode != "AFTER" || !ev.Results[1].Matched {
		t.Fatalf("the following rule must still run: %+v", ev.Results[1])
	}
}

func TestAnExpiredBudgetReportsATimeout(t *testing.T) {
	p := compile(t, rule("ANY", 10, "true"))
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	ev := p.Evaluate(ctx, input())
	if len(ev.Results) != 1 {
		t.Fatalf("results = %+v", ev.Results)
	}
	if ev.Results[0].ExplanationCode != engine.ExplanationTimeout {
		t.Fatalf("explanation = %s, want %s", ev.Results[0].ExplanationCode, engine.ExplanationTimeout)
	}
	if ev.Results[0].Severity != engine.SeverityError {
		t.Fatalf("severity = %s", ev.Results[0].Severity)
	}
}

func TestOutcomeFolding(t *testing.T) {
	cases := []struct {
		name    string
		actions []string
		want    engine.Outcome
	}{
		{"nothing objected", []string{engine.ActionApprove}, engine.OutcomeApproved},
		{"a warning is not an objection", []string{engine.ActionWarn}, engine.OutcomeApproved},
		{"a document request needs review", []string{engine.ActionRequireDocument}, engine.OutcomeReviewRequired},
		{"a partial approval", []string{engine.ActionPartialApprove}, engine.OutcomePartiallyApproved},
		{"review beats partial", []string{engine.ActionPartialApprove, engine.ActionRequirePreauth}, engine.OutcomeReviewRequired},
		{"rejection beats everything", []string{engine.ActionPartialApprove, engine.ActionRequirePreauth, engine.ActionReject}, engine.OutcomeRejected},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			actions := make([]engine.Action, 0, len(c.actions))
			for _, a := range c.actions {
				actions = append(actions, act(a))
			}
			p := compile(t, rule("R", 10, "true", actions...))
			if got := p.Evaluate(context.Background(), input()).Outcome; got != c.want {
				t.Fatalf("outcome = %s, want %s", got, c.want)
			}
		})
	}
}

func TestSeveralActionsOnOneRuleGetDistinctSequences(t *testing.T) {
	p := compile(t, rule("MULTI", 10, "true",
		act(engine.ActionRequireDocument), act(engine.ActionRequirePreauth)),
		rule("NEXT", 20, "true", act(engine.ActionWarn)))

	ev := p.Evaluate(context.Background(), input())
	seen := map[int]bool{}
	for _, r := range ev.Results {
		if seen[r.Sequence] {
			t.Fatalf("sequence %d used twice", r.Sequence)
		}
		seen[r.Sequence] = true
	}
	if len(ev.Results) != 3 {
		t.Fatalf("results = %d, want 3", len(ev.Results))
	}
}

func TestInactiveRulesAreNotCompiled(t *testing.T) {
	off := rule("OFF", 10, "true", act(engine.ActionReject))
	off.Active = false
	p := compile(t, off, rule("ON", 20, "true"))
	if p.RuleCount() != 1 {
		t.Fatalf("rule count = %d, want 1", p.RuleCount())
	}
	if p.Evaluate(context.Background(), input()).Outcome != engine.OutcomeApproved {
		t.Fatal("an inactive rejection must not fire")
	}
}

func TestTheSameInputTwiceProducesTheSameTrace(t *testing.T) {
	p := compile(t,
		rule("A", 10, "age >= 18", act(engine.ActionApprove)),
		rule("B", 20, `person.status == "ACTIVE"`, act(engine.ActionWarn)),
		rule("C", 30, "amount > 10000.0", act(engine.ActionRequireFinancialReview)),
	)
	first := p.Evaluate(context.Background(), input())
	second := p.Evaluate(context.Background(), input())
	if first.Outcome != second.Outcome || len(first.Results) != len(second.Results) {
		t.Fatal("the same input produced a different answer")
	}
	for i := range first.Results {
		a, b := first.Results[i], second.Results[i]
		if a.RuleCode != b.RuleCode || a.Matched != b.Matched || a.ActionType != b.ActionType || a.Sequence != b.Sequence {
			t.Fatalf("result %d differs: %+v vs %+v", i, a, b)
		}
	}
}

// --- compile-time guards -------------------------------------------------------------

func TestAnUndeclaredVariableFailsAtCompileTime(t *testing.T) {
	_, err := engine.Compile(version, schema(), []engine.Rule{rule("R", 10, "salary > 100")})
	if err == nil {
		t.Fatal("a condition reading an undeclared variable must not compile")
	}
	var ce *engine.CompileError
	if !errors.As(err, &ce) || ce.RuleCode != "R" {
		t.Fatalf("error must name the rule: %v", err)
	}
}

func TestANonBooleanConditionFailsAtCompileTime(t *testing.T) {
	_, err := engine.Compile(version, schema(), []engine.Rule{rule("R", 10, "age + 1")})
	if err == nil || !strings.Contains(err.Error(), "boolean") {
		t.Fatalf("a non-boolean condition must be refused: %v", err)
	}
}

func TestTwoRulesMayNotShareAPriority(t *testing.T) {
	_, err := engine.Compile(version, schema(), []engine.Rule{
		rule("A", 10, "true"), rule("B", 10, "true"),
	})
	if !errors.Is(err, engine.ErrDuplicatePriority) {
		t.Fatalf("err = %v, want a duplicate priority", err)
	}
}

func TestAnUnknownActionFailsAtCompileTime(t *testing.T) {
	_, err := engine.Compile(version, schema(), []engine.Rule{
		rule("R", 10, "true", engine.Action{Type: "DELETE_EVERYTHING"}),
	})
	if !errors.Is(err, engine.ErrUnknownAction) {
		t.Fatalf("err = %v, want an unknown action", err)
	}
}

func TestAnUnknownInputTypeIsRefused(t *testing.T) {
	_, err := engine.Compile(version, map[string]string{"x": "regexp"}, nil)
	if !errors.Is(err, engine.ErrUnknownType) {
		t.Fatalf("err = %v, want an unknown type", err)
	}
}

// v1.2 11.7 forbids arbitrary SQL, network calls and file access. They are absent from the
// environment rather than filtered out of it, so a condition reaching for one does not
// compile. This test is the standing proof that the environment stays that small.
func TestTheEnvironmentOffersNoWayOut(t *testing.T) {
	for _, condition := range []string{
		`http("https://example.invalid") == "x"`,
		`readFile("/etc/passwd") == "x"`,
		`exec("rm -rf /") == 0`,
		`sql("select 1") == 1`,
		`now() > serviceOn`,
	} {
		if _, err := engine.Compile(version, schema(), []engine.Rule{rule("R", 10, condition)}); err == nil {
			t.Fatalf("condition %q compiled; the environment is too large", condition)
		}
	}
}

// --- helpers -------------------------------------------------------------------------

func TestAgeHelperCountsCompletedYears(t *testing.T) {
	p := compile(t, rule("ADULT", 10,
		`age(timestamp("2008-06-16T00:00:00Z"), serviceOn) >= 18`, act(engine.ActionApprove)))
	// Born 2008-06-16, service on 2026-06-15: the birthday has not arrived, so 17.
	if got := p.Evaluate(context.Background(), input()); got.Results[0].Matched {
		t.Fatal("the day before an eighteenth birthday is not eighteen")
	}

	p = compile(t, rule("ADULT", 10,
		`age(timestamp("2008-06-15T00:00:00Z"), serviceOn) >= 18`, act(engine.ActionApprove)))
	if got := p.Evaluate(context.Background(), input()); !got.Results[0].Matched {
		t.Fatal("the eighteenth birthday itself counts")
	}
}

func TestOverlapsHelperUsesHalfOpenPeriods(t *testing.T) {
	// [01-01, 06-01) and [06-01, 12-01) touch but do not overlap, the same convention
	// every daterange in the schema uses.
	touching := `overlaps(timestamp("2026-01-01T00:00:00Z"), timestamp("2026-06-01T00:00:00Z"),
	                      timestamp("2026-06-01T00:00:00Z"), timestamp("2026-12-01T00:00:00Z"))`
	p := compile(t, rule("TOUCH", 10, touching))
	if p.Evaluate(context.Background(), input()).Results[0].Matched {
		t.Fatal("half-open periods that touch do not overlap")
	}

	crossing := `overlaps(timestamp("2026-01-01T00:00:00Z"), timestamp("2026-07-01T00:00:00Z"),
	                      timestamp("2026-06-01T00:00:00Z"), timestamp("2026-12-01T00:00:00Z"))`
	p = compile(t, rule("CROSS", 10, crossing))
	if !p.Evaluate(context.Background(), input()).Results[0].Matched {
		t.Fatal("periods that share a month overlap")
	}
}

// The 50 ms budget must actually stop a runaway expression, or it is decoration. A
// nested comprehension over a few thousand elements is millions of iterations, and CEL
// checks the deadline as it goes.
func TestASlowConditionIsCutOffByItsBudget(t *testing.T) {
	big := make([]any, 3000)
	for i := range big {
		big[i] = "code"
	}
	in := input()
	in["codes"] = big

	p := compile(t, rule("SLOW", 10, `codes.all(a, codes.all(b, a != "" && b != ""))`))
	started := time.Now()
	ev := p.Evaluate(context.Background(), in)
	elapsed := time.Since(started)

	if ev.Results[0].ExplanationCode != engine.ExplanationTimeout {
		t.Fatalf("explanation = %s, want %s after %s", ev.Results[0].ExplanationCode, engine.ExplanationTimeout, elapsed)
	}
	// Generous, because a loaded machine is slow to notice; the point is that it stops at
	// all rather than running the full nine million iterations.
	if elapsed > 2*time.Second {
		t.Fatalf("the budget took %s to bite", elapsed)
	}
}

func TestALeapDayBirthdayFallsOnTheFirstOfMarch(t *testing.T) {
	in := input()
	in["serviceOn"] = time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
	p := compile(t, rule("EIGHTEEN", 10,
		`age(timestamp("2008-02-29T00:00:00Z"), serviceOn) >= 18`))
	if p.Evaluate(context.Background(), in).Results[0].Matched {
		t.Fatal("28 February is still seventeen for a leap-day birthday")
	}

	in["serviceOn"] = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if !p.Evaluate(context.Background(), in).Results[0].Matched {
		t.Fatal("1 March is the eighteenth birthday in a common year")
	}
}

func TestProgramReportsItsVersion(t *testing.T) {
	if got := compile(t, rule("R", 10, "true")).VersionID(); got != version {
		t.Fatalf("version = %s, want %s", got, version)
	}
}
