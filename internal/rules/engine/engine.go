// Package engine evaluates a published rule set version against one input and reports
// what fired, in order, with an explanation for each. It performs none of the actions it
// returns: the caller — eligibility, authorization, adjudication — decides what to do
// with them. That separation is what lets a simulation run without writing a row.
//
// Conditions are CEL (ADR-023). The constraints v1.2 11.7 sets — no arbitrary SQL, no
// network, no file access, no unbounded loop, deterministic over its input — are
// properties of the language and of the environment built here, not rules anyone has to
// remember.
package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"github.com/google/uuid"
)

// Severity of one explanation line.
type Severity string

// Severities, matching the CHECK on rules.evaluation_result.
const (
	SeverityInfo    Severity = "INFO"
	SeverityWarning Severity = "WARNING"
	SeverityError   Severity = "ERROR"
)

// Outcome folds every action a pass produced into one answer.
type Outcome string

// Outcomes, matching the CHECK on rules.evaluation.
const (
	OutcomeApproved          Outcome = "APPROVED"
	OutcomeRejected          Outcome = "REJECTED"
	OutcomeReviewRequired    Outcome = "REVIEW_REQUIRED"
	OutcomePartiallyApproved Outcome = "PARTIALLY_APPROVED"
)

// Action types a rule may return. The closed list is v1.2 11.7's: a rule result is not
// only approve or reject.
const (
	ActionApprove                = "APPROVE"
	ActionReject                 = "REJECT"
	ActionWarn                   = "WARN"
	ActionRequireDocument        = "REQUIRE_DOCUMENT"
	ActionRequirePreauth         = "REQUIRE_PREAUTH"
	ActionRequireMedicalReview   = "REQUIRE_MEDICAL_REVIEW"
	ActionRequireFinancialReview = "REQUIRE_FINANCIAL_REVIEW"
	ActionPartialApprove         = "PARTIAL_APPROVE"
	ActionReserveEntitlement     = "RESERVE_ENTITLEMENT"
	ActionAdjustPrice            = "ADJUST_PRICE"
	ActionSetLimit               = "SET_LIMIT"
)

// KnownActions is every action type the engine will accept on a rule.
var KnownActions = map[string]bool{
	ActionApprove: true, ActionReject: true, ActionWarn: true,
	ActionRequireDocument: true, ActionRequirePreauth: true,
	ActionRequireMedicalReview: true, ActionRequireFinancialReview: true,
	ActionPartialApprove: true, ActionReserveEntitlement: true,
	ActionAdjustPrice: true, ActionSetLimit: true,
}

// Explanation codes the engine itself produces, as opposed to the ones rules carry.
const (
	// ExplanationTimeout: the rule exceeded its evaluation budget.
	ExplanationTimeout = "RULE_TIMEOUT"
	// ExplanationError: the condition failed at run time (a missing key, a bad cast).
	ExplanationError = "RULE_ERROR"
)

// RuleBudget is how long one condition may take. A rule is a small boolean expression
// over data already in memory; anything slower is a bug, and a slow rule must fail
// loudly rather than quietly stretch every request behind it.
const RuleBudget = 50 * time.Millisecond

// Action is what a matched rule asks the caller to do.
type Action struct {
	Type    string
	Payload map[string]any
}

// Rule is one authored row of a rule set version.
type Rule struct {
	ID                uuid.UUID
	Code              string
	Priority          int
	Condition         string
	Actions           []Action
	ExplanationCode   string
	ExplanationParams map[string]any
	StopOnMatch       bool
	Active            bool
}

// Result is one line of the trace: what was considered and what came of it.
type Result struct {
	Sequence        int
	RuleID          uuid.UUID
	RuleCode        string
	Matched         bool
	ActionType      string
	ActionPayload   map[string]any
	ExplanationCode string
	Severity        Severity
}

// Evaluation is the whole pass.
type Evaluation struct {
	VersionID uuid.UUID
	Outcome   Outcome
	Results   []Result
	Duration  time.Duration
}

// Program is a compiled rule set version, safe for concurrent use. A published version
// never changes, so a Program never needs invalidating.
type Program struct {
	versionID uuid.UUID
	rules     []compiled
}

type compiled struct {
	rule    Rule
	program cel.Program
}

// VersionID is the rule set version this program was compiled from.
func (p *Program) VersionID() uuid.UUID { return p.versionID }

// RuleCount is how many active rules the program holds.
func (p *Program) RuleCount() int { return len(p.rules) }

// CompileError names the rule that failed to compile, so an author sees which one.
type CompileError struct {
	RuleCode string
	Err      error
}

func (e *CompileError) Error() string {
	if e.RuleCode == "" {
		return fmt.Sprintf("rules: %v", e.Err)
	}
	return fmt.Sprintf("rules: rule %s: %v", e.RuleCode, e.Err)
}

func (e *CompileError) Unwrap() error { return e.Err }

// ErrDuplicatePriority reports two rules claiming the same position. The database
// enforces this too; compiling checks it as well because a Program built from anywhere
// else must not evaluate in an order nobody chose.
var ErrDuplicatePriority = errors.New("rules: two rules share a priority")

// ErrUnknownAction reports an action type outside the closed list.
var ErrUnknownAction = errors.New("rules: unknown action type")

// Compile builds the CEL environment from the version's declared input variables and
// compiles every active rule against it. A condition referring to a variable nobody
// declared fails here, at authoring time, rather than at three in the morning.
func Compile(versionID uuid.UUID, inputSchema map[string]string, rules []Rule) (*Program, error) {
	env, err := newEnv(inputSchema)
	if err != nil {
		return nil, &CompileError{Err: err}
	}

	active := make([]Rule, 0, len(rules))
	seen := make(map[int]string, len(rules))
	for _, r := range rules {
		if !r.Active {
			continue
		}
		if other, dup := seen[r.Priority]; dup {
			return nil, &CompileError{
				RuleCode: r.Code,
				Err:      fmt.Errorf("%w: %s and %s both at %d", ErrDuplicatePriority, other, r.Code, r.Priority),
			}
		}
		seen[r.Priority] = r.Code
		for _, a := range r.Actions {
			if !KnownActions[a.Type] {
				return nil, &CompileError{RuleCode: r.Code, Err: fmt.Errorf("%w: %q", ErrUnknownAction, a.Type)}
			}
		}
		active = append(active, r)
	}

	// Ascending priority: 1 runs first. The database keeps priorities unique, so this
	// order is total and the same on every machine.
	sort.Slice(active, func(i, j int) bool { return active[i].Priority < active[j].Priority })

	out := &Program{versionID: versionID, rules: make([]compiled, 0, len(active))}
	for _, r := range active {
		ast, issues := env.Compile(r.Condition)
		if issues != nil && issues.Err() != nil {
			return nil, &CompileError{RuleCode: r.Code, Err: issues.Err()}
		}
		if ast.OutputType() != cel.BoolType {
			return nil, &CompileError{
				RuleCode: r.Code,
				Err:      fmt.Errorf("condition must be a boolean, got %s", ast.OutputType()),
			}
		}
		// InterruptCheckFrequency lets the context deadline actually stop an evaluation
		// rather than being noticed only after it returns.
		prg, err := env.Program(ast,
			cel.EvalOptions(cel.OptOptimize),
			cel.InterruptCheckFrequency(100),
		)
		if err != nil {
			return nil, &CompileError{RuleCode: r.Code, Err: err}
		}
		out.rules = append(out.rules, compiled{rule: r, program: prg})
	}
	return out, nil
}

// Evaluate runs every rule in priority order and folds the actions into one outcome.
// It never returns an error for a rule that misbehaves: a rule that times out or throws
// becomes an ERROR line in the trace and the pass continues, because one broken rule
// must not silently swallow the twelve that would have fired after it.
func (p *Program) Evaluate(ctx context.Context, input map[string]any) Evaluation {
	started := time.Now()
	ev := Evaluation{VersionID: p.versionID, Results: make([]Result, 0, len(p.rules))}
	seq := 0

	for _, c := range p.rules {
		seq++
		matched, evalErr := p.runOne(ctx, c, input)
		switch {
		case evalErr != nil:
			code := ExplanationError
			if errors.Is(evalErr, context.DeadlineExceeded) {
				code = ExplanationTimeout
			}
			ev.Results = append(ev.Results, Result{
				Sequence: seq, RuleID: c.rule.ID, RuleCode: c.rule.Code,
				Matched: false, ExplanationCode: code, Severity: SeverityError,
			})
			continue
		case !matched:
			ev.Results = append(ev.Results, Result{
				Sequence: seq, RuleID: c.rule.ID, RuleCode: c.rule.Code,
				Matched: false, ExplanationCode: c.rule.ExplanationCode, Severity: SeverityInfo,
			})
			continue
		}

		if len(c.rule.Actions) == 0 {
			ev.Results = append(ev.Results, Result{
				Sequence: seq, RuleID: c.rule.ID, RuleCode: c.rule.Code,
				Matched: true, ExplanationCode: c.rule.ExplanationCode, Severity: SeverityInfo,
			})
		}
		for i, a := range c.rule.Actions {
			if i > 0 {
				seq++
			}
			ev.Results = append(ev.Results, Result{
				Sequence: seq, RuleID: c.rule.ID, RuleCode: c.rule.Code,
				Matched: true, ActionType: a.Type, ActionPayload: a.Payload,
				ExplanationCode: c.rule.ExplanationCode, Severity: severityOf(a.Type),
			})
		}
		if c.rule.StopOnMatch {
			break
		}
	}

	ev.Outcome = fold(ev.Results)
	ev.Duration = time.Since(started)
	return ev
}

// runOne evaluates one condition under its own budget.
func (p *Program) runOne(ctx context.Context, c compiled, input map[string]any) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, RuleBudget)
	defer cancel()

	// Do not start work under a context that is already finished. CEL only notices a
	// deadline between interrupt checks, so a short expression would otherwise run to
	// completion after the caller had already given up waiting for it.
	if err := ctx.Err(); err != nil {
		return false, err
	}

	val, _, err := c.program.ContextEval(ctx, input)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		return false, err
	}
	return asBool(val)
}

func asBool(val ref.Val) (bool, error) {
	b, ok := val.Value().(bool)
	if !ok {
		return false, fmt.Errorf("rules: condition returned %v, want a boolean", val.Type())
	}
	return b, nil
}

// severityOf grades an action for the trace. It is presentation, not logic: the outcome
// is folded from the action types themselves.
func severityOf(action string) Severity {
	switch action {
	case ActionReject:
		return SeverityError
	case ActionWarn, ActionRequireDocument, ActionRequirePreauth,
		ActionRequireMedicalReview, ActionRequireFinancialReview:
		return SeverityWarning
	default:
		return SeverityInfo
	}
}

// fold turns the actions that fired into one answer. A rejection beats everything; a
// request for review beats a partial approval; approval is what is left when nothing
// objected.
func fold(results []Result) Outcome {
	var review, partial bool
	for _, r := range results {
		if !r.Matched {
			continue
		}
		switch r.ActionType {
		case ActionReject:
			return OutcomeRejected
		case ActionRequireDocument, ActionRequirePreauth,
			ActionRequireMedicalReview, ActionRequireFinancialReview:
			review = true
		case ActionPartialApprove:
			partial = true
		}
	}
	switch {
	case review:
		return OutcomeReviewRequired
	case partial:
		return OutcomePartiallyApproved
	default:
		return OutcomeApproved
	}
}

// newEnv builds the CEL environment: the declared variables and a small helper set, and
// nothing else. There is no function here that reaches a network, a file or a database,
// which is how v1.2 11.7's prohibitions are kept — by absence, not by review.
func newEnv(inputSchema map[string]string) (*cel.Env, error) {
	opts := []cel.EnvOption{
		cel.HomogeneousAggregateLiterals(),
		ageFunction(),
		overlapsFunction(),
	}
	// Declaration order must not matter, but iteration over a map is random, so sort the
	// names to keep any error message stable.
	names := make([]string, 0, len(inputSchema))
	for name := range inputSchema {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t, err := celType(inputSchema[name])
		if err != nil {
			return nil, fmt.Errorf("variable %q: %w", name, err)
		}
		opts = append(opts, cel.Variable(name, t))
	}
	return cel.NewEnv(opts...)
}

// ErrUnknownType reports an input schema naming a type the environment does not offer.
var ErrUnknownType = errors.New("rules: unknown input type")

func celType(name string) (*cel.Type, error) {
	switch name {
	case "string":
		return cel.StringType, nil
	case "int":
		return cel.IntType, nil
	case "double":
		return cel.DoubleType, nil
	case "bool":
		return cel.BoolType, nil
	case "timestamp":
		return cel.TimestampType, nil
	case "duration":
		return cel.DurationType, nil
	case "map":
		return cel.MapType(cel.StringType, cel.DynType), nil
	case "list":
		return cel.ListType(cel.DynType), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownType, name)
	}
}

// ageFunction adds age(birth, on) -> int: completed years between two timestamps. Age
// limits are everywhere in benefit rules and every author would otherwise write the
// leap-year arithmetic again, differently.
func ageFunction() cel.EnvOption {
	return cel.Function("age",
		cel.Overload("age_timestamp_timestamp",
			[]*cel.Type{cel.TimestampType, cel.TimestampType}, cel.IntType,
			cel.BinaryBinding(func(lhs, rhs ref.Val) ref.Val {
				birth, ok1 := lhs.Value().(time.Time)
				on, ok2 := rhs.Value().(time.Time)
				if !ok1 || !ok2 {
					return types.NewErr("age expects two timestamps")
				}
				// Compare month and day rather than day-of-year: across a leap year
				// boundary the same calendar date has different ordinals, and an
				// eighteenth birthday would arrive a day late.
				years := on.Year() - birth.Year()
				bm, bd := birth.Month(), birth.Day()
				om, od := on.Month(), on.Day()
				if om < bm || (om == bm && od < bd) {
					// A 29 February birthday therefore falls on 1 March in a common year.
					years--
				}
				if years < 0 {
					years = 0
				}
				return types.Int(years)
			}),
		),
	)
}

// overlapsFunction adds overlaps(aFrom, aTo, bFrom, bTo) -> bool over half-open periods,
// the same convention the database uses for every daterange in the schema.
func overlapsFunction() cel.EnvOption {
	return cel.Function("overlaps",
		cel.Overload("overlaps_four_timestamps",
			[]*cel.Type{cel.TimestampType, cel.TimestampType, cel.TimestampType, cel.TimestampType},
			cel.BoolType,
			cel.FunctionBinding(func(args ...ref.Val) ref.Val {
				if len(args) != 4 {
					return types.NewErr("overlaps expects four timestamps")
				}
				vals := make([]time.Time, 4)
				for i, a := range args {
					t, ok := a.Value().(time.Time)
					if !ok {
						return types.NewErr("overlaps expects four timestamps")
					}
					vals[i] = t
				}
				aFrom, aTo, bFrom, bTo := vals[0], vals[1], vals[2], vals[3]
				return types.Bool(aFrom.Before(bTo) && bFrom.Before(aTo))
			}),
		),
	)
}
