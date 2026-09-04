package application

import (
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/rules/domain"
	"github.com/celikbros/kapsora/internal/rules/engine"
)

// compile turns stored rows into the evaluator's own rules and compiles them against the
// version's declared environment. It is the only place the two representations meet.
func compile(versionID uuid.UUID, schema map[string]string, rules []RuleRecord) (*engine.Program, error) {
	return engine.Compile(versionID, schema, engineRules(rules))
}

func engineRules(rules []RuleRecord) []engine.Rule {
	out := make([]engine.Rule, 0, len(rules))
	for _, r := range rules {
		out = append(out, engine.Rule{
			ID: r.ID, Code: r.Code, Priority: r.Priority, Condition: r.Condition,
			Actions: domain.EngineActions(r.Actions), ExplanationCode: r.ExplanationCode,
			ExplanationParams: r.ExplanationParams, StopOnMatch: r.StopOnMatch, Active: r.Active,
		})
	}
	return out
}

// compileFieldError turns a compiler refusal into a 422 field error naming the rule that
// failed and quoting the compiler's own message. An author is a trained user writing CEL
// in a monospace field (ADR-023); they are owed the real error, not "invalid rule".
func compileFieldError(field string, err error) error {
	var ce *engine.CompileError
	if !errors.As(err, &ce) {
		return err
	}
	message := ce.Err.Error()
	if ce.RuleCode != "" {
		message = fmt.Sprintf("%s: %s", ce.RuleCode, message)
	}
	ve := &domain.ValidationError{}
	ve.Add(field, "RULE_COMPILE_FAILED", message)
	return ve
}

// compileRuleError is compileFieldError for a set write: the failing rule's own position
// in the submitted array is what the author's screen highlights, so the code is looked up
// among the rows that were sent.
func compileRuleError(rows []domain.RuleInput, err error) error {
	var ce *engine.CompileError
	if !errors.As(err, &ce) {
		return err
	}
	field := "items"
	if ce.RuleCode != "" {
		for i, r := range rows {
			if r.Code == ce.RuleCode {
				field = fmt.Sprintf("items[%d].condition", i)
				break
			}
		}
	}
	ve := &domain.ValidationError{}
	ve.Add(field, "RULE_COMPILE_FAILED", fmt.Sprintf("%s: %s", ce.RuleCode, ce.Err.Error()))
	return ve
}

// programOf compiles a version's rules, reusing a cached program when the version is
// published. A published version never changes, so a cached program can never be stale; a
// draft is never cached, because the author is changing it as they read the trace.
func (s *Service) programOf(version VersionRecord, rules []RuleRecord) (*engine.Program, error) {
	published := version.Status == domain.VersionPublished || version.Status == domain.VersionRetired
	if published {
		if p, ok := s.programs.Get(version.ID); ok {
			return p, nil
		}
	}
	p, err := compile(version.ID, version.InputSchema, rules)
	if err != nil {
		return nil, err
	}
	if published {
		s.programs.Put(version.ID, p)
	}
	return p, nil
}
