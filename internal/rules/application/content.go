package application

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/rules/domain"
)

// ReplaceRules swaps the whole rule set of a DRAFT version under the version's own
// optimistic-concurrency token. Every condition is compiled against the version's declared
// input schema before anything is written, so a condition naming a variable nobody
// declared is refused by the author's own screen rather than by the caller that needed the
// decision.
func (s *Service) ReplaceRules(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID,
	items []domain.RuleInput, expected int64,
) (RuleResult, error) {
	rows := normaliseRules(items)
	if err := domain.ValidateRules(rows); err != nil {
		return RuleResult{}, err
	}

	var out RuleResult
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockDraft(ctx, tx, rc.TenantID, versionID, expected)
		if err != nil {
			return err
		}
		// Compile before writing: a set that cannot compile must not reach the table at
		// all, or the next submit would fail on rows nobody can see the error for.
		if _, err := compile(versionID, current.InputSchema, recordsOf(versionID, rows)); err != nil {
			return compileRuleError(rows, err)
		}
		if err := s.repo.ReplaceRules(ctx, tx, rc.TenantID, versionID, rows); err != nil {
			return err
		}
		if err := s.touchAndAudit(ctx, tx, rc, current, "rule_set_version.rules.replace",
			map[string]any{"rule_count": len(rows)}); err != nil {
			return err
		}
		out, err = s.rules(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// normaliseRules applies the defaults the contract documents before validation sees them.
func normaliseRules(items []domain.RuleInput) []domain.RuleInput {
	out := make([]domain.RuleInput, 0, len(items))
	for _, r := range items {
		r.Code = strings.ToUpper(strings.TrimSpace(r.Code))
		r.Name = strings.TrimSpace(r.Name)
		r.ExplanationCode = strings.ToUpper(strings.TrimSpace(r.ExplanationCode))
		r.Condition = strings.TrimSpace(r.Condition)
		if r.ExplanationParams == nil {
			r.ExplanationParams = map[string]any{}
		}
		out = append(out, r)
	}
	return out
}

// recordsOf lifts submitted rows into the record shape the compiler helper takes, so the
// write path and the read path compile through exactly one function.
func recordsOf(versionID uuid.UUID, rows []domain.RuleInput) []RuleRecord {
	out := make([]RuleRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, RuleRecord{
			RuleSetVersionID: versionID, Code: r.Code, Name: r.Name, Priority: r.Priority,
			Condition: r.Condition, Actions: r.Actions, ExplanationCode: r.ExplanationCode,
			ExplanationParams: r.ExplanationParams, StopOnMatch: r.StopOnMatch, Active: r.Active,
		})
	}
	return out
}

// ReplaceTestCases swaps the whole test case set of a DRAFT version. The cases are not run
// here: writing a case that currently fails is how an author records the behaviour they
// are about to build, and the gate that refuses a failing case is submit.
func (s *Service) ReplaceTestCases(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID,
	items []domain.TestCaseInput, expected int64,
) (TestCaseResult, error) {
	rows := normaliseTestCases(items)
	if err := domain.ValidateTestCases(rows); err != nil {
		return TestCaseResult{}, err
	}

	var out TestCaseResult
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockDraft(ctx, tx, rc.TenantID, versionID, expected)
		if err != nil {
			return err
		}
		if err := s.repo.ReplaceTestCases(ctx, tx, rc.TenantID, versionID, rows); err != nil {
			return err
		}
		if err := s.touchAndAudit(ctx, tx, rc, current, "rule_set_version.test_cases.replace",
			map[string]any{"test_case_count": len(rows)}); err != nil {
			return err
		}
		out, err = s.testCases(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

func normaliseTestCases(items []domain.TestCaseInput) []domain.TestCaseInput {
	out := make([]domain.TestCaseInput, 0, len(items))
	for _, c := range items {
		c.Code = strings.ToUpper(strings.TrimSpace(c.Code))
		if c.Input == nil {
			c.Input = map[string]any{}
		}
		if c.ExpectedExplanations == nil {
			c.ExpectedExplanations = []string{}
		}
		for i, code := range c.ExpectedExplanations {
			c.ExpectedExplanations[i] = strings.ToUpper(strings.TrimSpace(code))
		}
		if c.AssertsActions && c.ExpectedActions == nil {
			c.ExpectedActions = []domain.ActionInput{}
		}
		out = append(out, c)
	}
	return out
}

// touchAndAudit moves the version's ETag with its content and records what changed.
func (s *Service) touchAndAudit(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	version VersionRecord, action string, detail map[string]any,
) error {
	if err := s.repo.TouchVersion(ctx, tx, rc.TenantID, version.ID); err != nil {
		return err
	}
	detail["rule_set_id"] = version.RuleSetID
	detail["version_no"] = version.VersionNo
	return s.record(ctx, tx, rc, action, "rule_set_version", version.ID, detail)
}

// rules reads the rule set together with the version ETag it is written under.
func (s *Service) rules(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (RuleResult, error) {
	version, err := s.repo.GetVersion(ctx, tx, tenantID, versionID)
	if err != nil {
		return RuleResult{}, err
	}
	items, err := s.repo.ListRules(ctx, tx, tenantID, versionID)
	if err != nil {
		return RuleResult{}, err
	}
	return RuleResult{Items: items, RowVersion: version.RowVersion}, nil
}

// testCases reads the test case set together with the version ETag it is written under.
func (s *Service) testCases(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (TestCaseResult, error) {
	version, err := s.repo.GetVersion(ctx, tx, tenantID, versionID)
	if err != nil {
		return TestCaseResult{}, err
	}
	items, err := s.repo.ListTestCases(ctx, tx, tenantID, versionID)
	if err != nil {
		return TestCaseResult{}, err
	}
	return TestCaseResult{Items: items, RowVersion: version.RowVersion}, nil
}
