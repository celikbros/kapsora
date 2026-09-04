package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/rules/domain"
)

// NewVersionInput is the create command; CopyFromVersionID copies the input schema, the
// rules and the test cases of another version of the same set, which is how a revision
// starts from what is live rather than from an empty page.
type NewVersionInput struct {
	CopyFromVersionID *uuid.UUID
	ValidFrom         *time.Time
	ValidTo           *time.Time
	InputSchema       map[string]string
	Notes             *string
}

// VersionPatch is a merge-patch of a draft version.
type VersionPatch struct {
	ValidFrom       *time.Time
	ClearValidFrom  bool
	ValidTo         *time.Time
	ClearValidTo    bool
	InputSchema     map[string]string
	Notes           *string
	ClearNotes      bool
	ExpectedVersion int64
}

// ListVersions returns the version summaries of a rule set, highest number first.
func (s *Service) ListVersions(ctx context.Context, rc identity.RequestContext, ruleSetID uuid.UUID) ([]VersionRecord, error) {
	var out []VersionRecord
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetRuleSet(ctx, tx, rc.TenantID, ruleSetID); err != nil {
			return err
		}
		rows, err := s.repo.ListVersions(ctx, tx, rc.TenantID, ruleSetID)
		if err != nil {
			return err
		}
		out = make([]VersionRecord, 0, len(rows))
		for _, v := range rows {
			if visibleTo(rc, v.Status) {
				out = append(out, v)
			}
		}
		return nil
	})
	return out, err
}

// visibleTo decides whether a caller may see a version at all. A published or retired
// version is what decided somebody's claim, so anyone who may read rules may read it. A
// draft or a version under review is an unapproved proposal, and it is hidden entirely
// rather than refused, which would confirm that a revision is under way.
func visibleTo(rc identity.RequestContext, status string) bool {
	if status == domain.VersionPublished || status == domain.VersionRetired {
		return true
	}
	return rc.Has(PermissionDraft)
}

// GetVersion returns one version with its rules and test cases.
func (s *Service) GetVersion(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID) (VersionView, error) {
	var out VersionView
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		loaded, err := s.loadVersion(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		if !visibleTo(rc, loaded.Version.Status) {
			return ErrVersionNotFound
		}
		out = loaded
		return nil
	})
	return out, err
}

// CreateVersion opens the next DRAFT version of a rule set.
func (s *Service) CreateVersion(ctx context.Context, rc identity.RequestContext, ruleSetID uuid.UUID,
	in NewVersionInput,
) (VersionView, error) {
	in.ValidFrom, in.ValidTo = domain.DayPtr(in.ValidFrom), domain.DayPtr(in.ValidTo)

	var out VersionView
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetRuleSet(ctx, tx, rc.TenantID, ruleSetID); err != nil {
			return err
		}
		var source *VersionView
		if in.CopyFromVersionID != nil {
			origin, err := s.loadVersion(ctx, tx, rc.TenantID, *in.CopyFromVersionID)
			if err != nil {
				return err
			}
			if origin.Version.RuleSetID != ruleSetID {
				return fieldError("copyFromVersionId", "UNKNOWN", "kaynak sürüm bu kural setine ait değil")
			}
			source = &origin
		}
		schema := in.InputSchema
		if schema == nil && source != nil {
			// A copy keeps the environment it was written against unless the caller says
			// otherwise; the conditions coming with it were compiled against exactly that.
			schema = source.Version.InputSchema
		}
		if schema == nil {
			schema = map[string]string{}
		}
		if err := domain.ValidateVersion(domain.VersionInput{
			ValidFrom: in.ValidFrom, ValidTo: in.ValidTo, InputSchema: schema, Notes: in.Notes,
		}); err != nil {
			return err
		}
		versionNo, err := s.repo.NextVersionNo(ctx, tx, rc.TenantID, ruleSetID)
		if err != nil {
			return err
		}
		versionID, err := s.repo.CreateVersion(ctx, tx, rc.TenantID, NewVersionRow{
			RuleSetID: ruleSetID, VersionNo: versionNo, ValidFrom: in.ValidFrom,
			ValidTo: in.ValidTo, InputSchema: schema, Notes: in.Notes,
		})
		if err != nil {
			return err
		}
		copiedRules, copiedCases := 0, 0
		if source != nil {
			if copiedRules, copiedCases, err = s.copyContent(ctx, tx, rc.TenantID, *source, versionID); err != nil {
				return err
			}
		}
		if err := s.record(ctx, tx, rc, "rule_set_version.create", "rule_set_version", versionID, map[string]any{
			"rule_set_id": ruleSetID, "version_no": versionNo,
			"copied_rules": copiedRules, "copied_test_cases": copiedCases,
		}); err != nil {
			return err
		}
		out, err = s.loadVersion(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// copyContent duplicates the rules and test cases of one version into a fresh draft.
func (s *Service) copyContent(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	source VersionView, targetID uuid.UUID,
) (rules, cases int, err error) {
	ruleRows := make([]domain.RuleInput, 0, len(source.Rules))
	for _, r := range source.Rules {
		ruleRows = append(ruleRows, domain.RuleInput{
			Code: r.Code, Name: r.Name, Priority: r.Priority, Condition: r.Condition,
			Actions: r.Actions, ExplanationCode: r.ExplanationCode,
			ExplanationParams: r.ExplanationParams, StopOnMatch: r.StopOnMatch, Active: r.Active,
		})
	}
	if err := s.repo.ReplaceRules(ctx, tx, tenantID, targetID, ruleRows); err != nil {
		return 0, 0, err
	}
	caseRows := make([]domain.TestCaseInput, 0, len(source.TestCases))
	for _, c := range source.TestCases {
		caseRows = append(caseRows, domain.TestCaseInput{
			Code: c.Code, Description: c.Description, Input: c.Input,
			ExpectedOutcome: c.ExpectedOutcome, ExpectedExplanations: c.ExpectedExplanations,
			ExpectedActions: c.ExpectedActions, AssertsActions: c.AssertsActions,
		})
	}
	if err := s.repo.ReplaceTestCases(ctx, tx, tenantID, targetID, caseRows); err != nil {
		return 0, 0, err
	}
	return len(ruleRows), len(caseRows), nil
}

// UpdateVersion applies a merge-patch of the period, the input schema and the notes of a
// DRAFT version.
func (s *Service) UpdateVersion(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID,
	p VersionPatch,
) (VersionView, error) {
	var out VersionView
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockDraft(ctx, tx, rc.TenantID, versionID, p.ExpectedVersion)
		if err != nil {
			return err
		}
		next := VersionDraftRow{
			ValidFrom: current.ValidFrom, ValidTo: current.ValidTo,
			InputSchema: current.InputSchema, Notes: current.Notes,
		}
		switch {
		case p.ClearValidFrom:
			next.ValidFrom = nil
		case p.ValidFrom != nil:
			next.ValidFrom = domain.DayPtr(p.ValidFrom)
		}
		switch {
		case p.ClearValidTo:
			next.ValidTo = nil
		case p.ValidTo != nil:
			next.ValidTo = domain.DayPtr(p.ValidTo)
		}
		if p.InputSchema != nil {
			next.InputSchema = p.InputSchema
		}
		switch {
		case p.ClearNotes:
			next.Notes = nil
		case p.Notes != nil:
			next.Notes = optString(strings.TrimSpace(*p.Notes))
		}
		if err := domain.ValidateVersion(domain.VersionInput{
			ValidFrom: next.ValidFrom, ValidTo: next.ValidTo,
			InputSchema: next.InputSchema, Notes: next.Notes,
		}); err != nil {
			return err
		}
		// Narrowing the environment under rules that were already written would leave the
		// version unable to compile, so the rules are recompiled against the new schema
		// here rather than discovered to be broken at submit.
		rules, err := s.repo.ListRules(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		if _, err := compile(versionID, next.InputSchema, rules); err != nil {
			return compileFieldError("inputSchema", err)
		}
		if err := s.repo.UpdateVersionDraft(ctx, tx, rc.TenantID, versionID, next); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "rule_set_version.update", "rule_set_version", versionID, map[string]any{
			"rule_set_id": current.RuleSetID, "version_no": current.VersionNo,
		}); err != nil {
			return err
		}
		out, err = s.loadVersion(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// SubmitVersion moves a draft to UNDER_REVIEW and records the maker. This is the gate the
// package exists for: a version reaches review only with at least one test case and only
// when every one of them passes. An untested rule that decides what a member is owed is
// not a rule anybody should have to trust, and the review is the last moment at which
// saying so is cheap.
func (s *Service) SubmitVersion(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID,
	comment *string, expected int64,
) (VersionView, error) {
	if err := domain.ValidateComment(comment); err != nil {
		return VersionView{}, err
	}

	var out VersionView
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.lockDraft(ctx, tx, rc.TenantID, versionID, expected)
		if err != nil {
			return err
		}
		ve := &domain.ValidationError{}
		if current.ValidFrom == nil {
			ve.Add("validFrom", "REQUIRED", "geçerlilik başlangıcı gerekli")
		}
		run, err := s.runTests(ctx, tx, rc.TenantID, current)
		if err != nil {
			return err
		}
		switch {
		case run.Total == 0:
			ve.Add("testCases", "TESTS_REQUIRED", "en az bir test senaryosu gerekli")
		case !run.Passed:
			ve.Add("testCases", "TESTS_FAILING", "başarısız senaryolar: "+strings.Join(run.FailedCodes(), ", "))
		}
		if err := ve.OrNil(); err != nil {
			return err
		}
		if err := s.repo.SubmitVersion(ctx, tx, rc.TenantID, versionID, SubmitRow{
			ActorID: rc.Principal.ActorID, Comment: optString(strings.TrimSpace(deref(comment))),
		}); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "rule_set_version.submit", "rule_set_version", versionID, map[string]any{
			"rule_set_id": current.RuleSetID, "version_no": current.VersionNo,
			"rule_count": current.RuleCount, "test_case_count": run.Total,
		}); err != nil {
			return err
		}
		out, err = s.loadVersion(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// PublishVersion freezes a version under review and makes it the one the engine evaluates.
// The checker must differ from the maker; the refusal is audited before it is reported.
// The hash is taken over the input schema and every rule, so "which rules decided this"
// can be proved after the fact.
func (s *Service) PublishVersion(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID,
	comment *string, expected int64,
) (VersionView, error) {
	if err := domain.ValidateComment(comment); err != nil {
		return VersionView{}, err
	}

	var out VersionView
	var denied *VersionRecord
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.LockVersion(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		if current.RowVersion != expected {
			return ErrVersionMismatch
		}
		if current.Status != domain.VersionUnderReview {
			return statusError(current.Status)
		}
		if current.SubmittedBy != nil && *current.SubmittedBy == rc.Principal.ActorID {
			// The audit row cannot be written here: this transaction rolls back. It is
			// written by auditMakerCheckerDenial once the refusal is final.
			row := current
			denied = &row
			return ErrMakerCheckerSame
		}
		rules, err := s.repo.ListRules(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		hash, err := domain.ContentHash(contentOf(current, rules))
		if err != nil {
			return err
		}
		if err := s.repo.PublishVersion(ctx, tx, rc.TenantID, versionID, PublishRow{
			ActorID: rc.Principal.ActorID, ContentHash: hash,
			Comment: optString(strings.TrimSpace(deref(comment))),
		}); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "rule_set_version.publish", "rule_set_version", versionID, map[string]any{
			"rule_set_id": current.RuleSetID, "version_no": current.VersionNo,
			"content_hash": hash, "rule_count": len(rules),
		}); err != nil {
			return err
		}
		out, err = s.loadVersion(ctx, tx, rc.TenantID, versionID)
		return err
	})
	if denied != nil {
		return VersionView{}, s.auditMakerCheckerDenial(ctx, rc, *denied, err)
	}
	return out, err
}

// auditMakerCheckerDenial records the refused publish in its own transaction, because the
// command's transaction rolled back with it. A failure to audit is reported alongside the
// refusal: a denial that leaves no trace is worse than a noisy error.
func (s *Service) auditMakerCheckerDenial(ctx context.Context, rc identity.RequestContext,
	row VersionRecord, cause error,
) error {
	auditErr := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		return s.recordDenied(ctx, tx, rc, "rule_set_version.publish", "rule_set_version", row.ID,
			"MAKER_CHECKER_SAME_ACTOR", map[string]any{
				"rule_set_id": row.RuleSetID, "version_no": row.VersionNo,
			})
	})
	if auditErr != nil {
		return errors.Join(cause, auditErr)
	}
	return cause
}

// RetireVersion closes a published version with a reason; it stays readable, because every
// evaluation ever recorded names the version that produced it.
func (s *Service) RetireVersion(ctx context.Context, rc identity.RequestContext, versionID uuid.UUID,
	reasonCode string, reasonText *string, expected int64,
) (VersionView, error) {
	reasonCode = strings.ToUpper(strings.TrimSpace(reasonCode))
	if err := domain.ValidateReasonCode(reasonCode, reasonText); err != nil {
		return VersionView{}, err
	}

	var out VersionView
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.LockVersion(ctx, tx, rc.TenantID, versionID)
		if err != nil {
			return err
		}
		if current.RowVersion != expected {
			return ErrVersionMismatch
		}
		if current.Status != domain.VersionPublished {
			return statusError(current.Status)
		}
		if err := s.repo.RetireVersion(ctx, tx, rc.TenantID, versionID, RetireRow{
			ReasonCode: reasonCode, ReasonText: optString(strings.TrimSpace(deref(reasonText))),
		}); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "rule_set_version.retire", "rule_set_version", versionID, map[string]any{
			"rule_set_id": current.RuleSetID, "version_no": current.VersionNo, "reason_code": reasonCode,
		}); err != nil {
			return err
		}
		out, err = s.loadVersion(ctx, tx, rc.TenantID, versionID)
		return err
	})
	return out, err
}

// lockDraft loads a version FOR UPDATE and asserts that it is an editable draft matching
// the caller's If-Match. Every write to a version or to any of its children goes through
// here, which is what makes RULE_VERSION_IMMUTABLE one rule rather than four.
func (s *Service) lockDraft(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID,
	expected int64,
) (VersionRecord, error) {
	current, err := s.repo.LockVersion(ctx, tx, tenantID, versionID)
	if err != nil {
		return VersionRecord{}, err
	}
	if current.RowVersion != expected {
		return VersionRecord{}, ErrVersionMismatch
	}
	if current.Status != domain.VersionDraft {
		return VersionRecord{}, statusError(current.Status)
	}
	return current, nil
}

// loadVersion reads a version with the rules and test cases under it.
func (s *Service) loadVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) (VersionView, error) {
	version, err := s.repo.GetVersion(ctx, tx, tenantID, versionID)
	if err != nil {
		return VersionView{}, err
	}
	rules, err := s.repo.ListRules(ctx, tx, tenantID, versionID)
	if err != nil {
		return VersionView{}, err
	}
	cases, err := s.repo.ListTestCases(ctx, tx, tenantID, versionID)
	if err != nil {
		return VersionView{}, err
	}
	return VersionView{Version: version, Rules: rules, TestCases: cases}, nil
}

// contentOf assembles what the content hash is taken over.
func contentOf(v VersionRecord, rules []RuleRecord) domain.Content {
	out := domain.Content{
		ValidFrom: v.ValidFrom, ValidTo: v.ValidTo, InputSchema: v.InputSchema,
		Rules: make([]domain.RuleContent, 0, len(rules)),
	}
	for _, r := range rules {
		out.Rules = append(out.Rules, domain.RuleContent{
			Code: r.Code, Name: r.Name, Priority: r.Priority, Condition: r.Condition,
			Actions: r.Actions, ExplanationCode: r.ExplanationCode,
			ExplanationParams: r.ExplanationParams, StopOnMatch: r.StopOnMatch, Active: r.Active,
		})
	}
	return out
}
