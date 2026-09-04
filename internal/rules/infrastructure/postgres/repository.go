// Package rulespg implements the rule engine repository with sqlc. It is stateless: every
// method takes the caller's tenant-bound transaction, so RLS is active for every
// statement. The jsonb columns are marshalled here and nowhere else, so exactly one piece
// of code decides what the stored shape of an action, a parameter set or an input is.
package rulespg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
	"github.com/celikbros/kapsora/internal/rules/application"
	"github.com/celikbros/kapsora/internal/rules/domain"
)

// Repository is stateless; every method takes the caller's transaction.
type Repository struct{}

// New returns the repository.
func New() *Repository { return &Repository{} }

var _ application.Repository = (*Repository)(nil)

// PostgreSQL error codes mapped to named application errors.
const (
	uniqueViolation     = "23505"
	exclusionViolation  = "23P01"
	foreignKeyViolation = "23503"
)

// CreateRuleSet implements application.Repository.
func (Repository) CreateRuleSet(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewRuleSetRow,
) (uuid.UUID, error) {
	id, err := sqlcgen.New(tx).CreateRuleSet(ctx, sqlcgen.CreateRuleSetParams{
		TenantID: tenantID, Code: in.Code, Name: in.Name,
		DomainCode: in.DomainCode, Purpose: in.Purpose,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return uuid.Nil, application.ErrRuleSetCodeTaken
		}
		return uuid.Nil, fmt.Errorf("rules: create rule set: %w", err)
	}
	return id, nil
}

// GetRuleSet implements application.Repository.
func (Repository) GetRuleSet(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (application.RuleSetRecord, error) {
	row, err := sqlcgen.New(tx).GetRuleSet(ctx, sqlcgen.GetRuleSetParams{TenantID: tenantID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.RuleSetRecord{}, application.ErrRuleSetNotFound
	}
	if err != nil {
		return application.RuleSetRecord{}, fmt.Errorf("rules: get rule set: %w", err)
	}
	return application.RuleSetRecord{
		ID: row.ID, Code: row.Code, Name: row.Name, DomainCode: row.DomainCode,
		Purpose: row.Purpose, Status: row.Status, VersionCount: int(row.VersionCount),
		CreatedAt: row.CreatedAt, RowVersion: row.RowVersion,
	}, nil
}

// ListRuleSets implements application.Repository.
func (Repository) ListRuleSets(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.RuleSetQuery,
) ([]application.RuleSetRecord, error) {
	params := sqlcgen.ListRuleSetsParams{
		TenantID: tenantID, DomainCode: optionalString(q.DomainCode),
		Purpose: optionalString(q.Purpose), Status: optionalString(q.Status),
		Q: optionalString(q.Query), PageSize: pageSize(q.PageSize),
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListRuleSets(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("rules: list rule sets: %w", err)
	}
	out := make([]application.RuleSetRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.RuleSetRecord{
			ID: r.ID, Code: r.Code, Name: r.Name, DomainCode: r.DomainCode,
			Purpose: r.Purpose, Status: r.Status, VersionCount: int(r.VersionCount),
			CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
		})
	}
	return out, nil
}

// UpdateRuleSet implements application.Repository.
func (Repository) UpdateRuleSet(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	in application.RuleSetUpdateRow, expected int64,
) error {
	_, err := sqlcgen.New(tx).UpdateRuleSet(ctx, sqlcgen.UpdateRuleSetParams{
		TenantID: tenantID, ID: id, RowVersion: expected, Name: in.Name, Status: in.Status,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrVersionMismatch
	}
	if err != nil {
		return fmt.Errorf("rules: update rule set: %w", err)
	}
	return nil
}

// NextVersionNo implements application.Repository.
func (Repository) NextVersionNo(ctx context.Context, tx pgx.Tx, tenantID, ruleSetID uuid.UUID) (int, error) {
	n, err := sqlcgen.New(tx).NextRuleSetVersionNo(ctx, sqlcgen.NextRuleSetVersionNoParams{
		TenantID: tenantID, RuleSetID: ruleSetID,
	})
	if err != nil {
		return 0, fmt.Errorf("rules: next version number: %w", err)
	}
	return int(n), nil
}

// CreateVersion implements application.Repository.
func (Repository) CreateVersion(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewVersionRow,
) (uuid.UUID, error) {
	schema, err := marshalSchema(in.InputSchema)
	if err != nil {
		return uuid.Nil, err
	}
	id, err := sqlcgen.New(tx).CreateRuleSetVersion(ctx, sqlcgen.CreateRuleSetVersionParams{
		TenantID: tenantID, RuleSetID: in.RuleSetID, VersionNo: int32(in.VersionNo), //nolint:gosec // version numbers are small positive integers
		ValidFrom: dateValue(in.ValidFrom), ValidTo: dateValue(in.ValidTo),
		InputSchema: schema, Notes: in.Notes,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
			return uuid.Nil, application.ErrRuleSetNotFound
		}
		return uuid.Nil, fmt.Errorf("rules: create version: %w", err)
	}
	return id, nil
}

// GetVersion implements application.Repository.
func (Repository) GetVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (application.VersionRecord, error) {
	row, err := sqlcgen.New(tx).GetRuleSetVersion(ctx, sqlcgen.GetRuleSetVersionParams{TenantID: tenantID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.VersionRecord{}, application.ErrVersionNotFound
	}
	if err != nil {
		return application.VersionRecord{}, fmt.Errorf("rules: get version: %w", err)
	}
	schema, err := unmarshalSchema(row.InputSchema)
	if err != nil {
		return application.VersionRecord{}, err
	}
	return application.VersionRecord{
		ID: row.ID, RuleSetID: row.RuleSetID, RuleSetCode: row.RuleSetCode,
		VersionNo: int(row.VersionNo), Status: row.Status,
		ValidFrom: datePtr(row.ValidFrom), ValidTo: datePtr(row.ValidTo),
		InputSchema: schema, ContentHash: row.ContentHash, Notes: row.Notes,
		SubmittedAt: row.SubmittedAt, SubmittedBy: uuidPtr(row.SubmittedBy),
		PublishedAt: row.PublishedAt, PublishedBy: uuidPtr(row.PublishedBy),
		RetireReasonCode: row.RetireReasonCode, ReviewComment: row.ReviewComment,
		RuleCount: int(row.RuleCount), TestCaseCount: int(row.TestCaseCount),
		CreatedAt: row.CreatedAt, RowVersion: row.RowVersion,
	}, nil
}

// ListVersions implements application.Repository.
func (Repository) ListVersions(ctx context.Context, tx pgx.Tx, tenantID, ruleSetID uuid.UUID) ([]application.VersionRecord, error) {
	rows, err := sqlcgen.New(tx).ListRuleSetVersions(ctx, sqlcgen.ListRuleSetVersionsParams{
		TenantID: tenantID, RuleSetID: ruleSetID,
	})
	if err != nil {
		return nil, fmt.Errorf("rules: list versions: %w", err)
	}
	out := make([]application.VersionRecord, 0, len(rows))
	for _, r := range rows {
		schema, err := unmarshalSchema(r.InputSchema)
		if err != nil {
			return nil, err
		}
		out = append(out, application.VersionRecord{
			ID: r.ID, RuleSetID: r.RuleSetID, RuleSetCode: r.RuleSetCode,
			VersionNo: int(r.VersionNo), Status: r.Status,
			ValidFrom: datePtr(r.ValidFrom), ValidTo: datePtr(r.ValidTo),
			InputSchema: schema, ContentHash: r.ContentHash, Notes: r.Notes,
			SubmittedAt: r.SubmittedAt, SubmittedBy: uuidPtr(r.SubmittedBy),
			PublishedAt: r.PublishedAt, PublishedBy: uuidPtr(r.PublishedBy),
			RetireReasonCode: r.RetireReasonCode, ReviewComment: r.ReviewComment,
			RuleCount: int(r.RuleCount), TestCaseCount: int(r.TestCaseCount),
			CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
		})
	}
	return out, nil
}

// LockVersion implements application.Repository. The counts are absent from the locking
// read: a command decides on the status and the row version, and the two sub-selects would
// be paid for on every write.
func (Repository) LockVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (application.VersionRecord, error) {
	row, err := sqlcgen.New(tx).GetRuleSetVersionForUpdate(ctx, sqlcgen.GetRuleSetVersionForUpdateParams{
		TenantID: tenantID, ID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.VersionRecord{}, application.ErrVersionNotFound
	}
	if err != nil {
		return application.VersionRecord{}, fmt.Errorf("rules: lock version: %w", err)
	}
	schema, err := unmarshalSchema(row.InputSchema)
	if err != nil {
		return application.VersionRecord{}, err
	}
	return application.VersionRecord{
		ID: row.ID, RuleSetID: row.RuleSetID, VersionNo: int(row.VersionNo), Status: row.Status,
		ValidFrom: datePtr(row.ValidFrom), ValidTo: datePtr(row.ValidTo),
		InputSchema: schema, ContentHash: row.ContentHash, Notes: row.Notes,
		SubmittedAt: row.SubmittedAt, SubmittedBy: uuidPtr(row.SubmittedBy),
		PublishedAt: row.PublishedAt, PublishedBy: uuidPtr(row.PublishedBy),
		RetireReasonCode: row.RetireReasonCode, ReviewComment: row.ReviewComment,
		CreatedAt: row.CreatedAt, RowVersion: row.RowVersion,
	}, nil
}

// UpdateVersionDraft implements application.Repository.
func (Repository) UpdateVersionDraft(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	in application.VersionDraftRow,
) error {
	schema, err := marshalSchema(in.InputSchema)
	if err != nil {
		return err
	}
	n, err := sqlcgen.New(tx).UpdateRuleSetVersionDraft(ctx, sqlcgen.UpdateRuleSetVersionDraftParams{
		TenantID: tenantID, ID: id, ValidFrom: dateValue(in.ValidFrom), ValidTo: dateValue(in.ValidTo),
		InputSchema: schema, Notes: in.Notes,
	})
	if err != nil {
		return fmt.Errorf("rules: update draft version: %w", err)
	}
	if n == 0 {
		// The caller already checked the status under a row lock, so nothing matching
		// means the row moved between the lock and the write.
		return application.ErrVersionImmutable
	}
	return nil
}

// TouchVersion implements application.Repository.
func (Repository) TouchVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	if _, err := sqlcgen.New(tx).TouchRuleSetVersion(ctx, sqlcgen.TouchRuleSetVersionParams{
		TenantID: tenantID, ID: id,
	}); err != nil {
		return fmt.Errorf("rules: touch version: %w", err)
	}
	return nil
}

// SubmitVersion implements application.Repository.
func (Repository) SubmitVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in application.SubmitRow) error {
	n, err := sqlcgen.New(tx).SubmitRuleSetVersion(ctx, sqlcgen.SubmitRuleSetVersionParams{
		TenantID: tenantID, ID: id,
		ActorID:       uuid.NullUUID{UUID: in.ActorID, Valid: in.ActorID != uuid.Nil},
		ReviewComment: in.Comment,
	})
	if err != nil {
		return fmt.Errorf("rules: submit version: %w", err)
	}
	if n == 0 {
		return application.ErrVersionTransition
	}
	return nil
}

// PublishVersion implements application.Repository. The exclusion constraint on published
// overlap is the authority on "two versions deciding the same day", so the conflict is
// mapped from the PostgreSQL error rather than pre-checked.
func (Repository) PublishVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in application.PublishRow) error {
	hash := in.ContentHash
	n, err := sqlcgen.New(tx).PublishRuleSetVersion(ctx, sqlcgen.PublishRuleSetVersionParams{
		TenantID: tenantID, ID: id,
		ActorID:     uuid.NullUUID{UUID: in.ActorID, Valid: in.ActorID != uuid.Nil},
		ContentHash: &hash, ReviewComment: in.Comment,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == exclusionViolation {
			return application.ErrVersionOverlap
		}
		return fmt.Errorf("rules: publish version: %w", err)
	}
	if n == 0 {
		return application.ErrVersionTransition
	}
	return nil
}

// RetireVersion implements application.Repository.
func (Repository) RetireVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in application.RetireRow) error {
	n, err := sqlcgen.New(tx).RetireRuleSetVersion(ctx, sqlcgen.RetireRuleSetVersionParams{
		TenantID: tenantID, ID: id, ReasonCode: &in.ReasonCode, ReasonText: in.ReasonText,
	})
	if err != nil {
		return fmt.Errorf("rules: retire version: %w", err)
	}
	if n == 0 {
		return application.ErrVersionTransition
	}
	return nil
}

// ListRules implements application.Repository.
func (Repository) ListRules(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) ([]application.RuleRecord, error) {
	rows, err := sqlcgen.New(tx).ListRules(ctx, sqlcgen.ListRulesParams{
		TenantID: tenantID, RuleSetVersionID: versionID,
	})
	if err != nil {
		return nil, fmt.Errorf("rules: list rules: %w", err)
	}
	out := make([]application.RuleRecord, 0, len(rows))
	for _, r := range rows {
		actions, err := unmarshalActions(r.Actions)
		if err != nil {
			return nil, err
		}
		params, err := unmarshalObject(r.ExplanationParams)
		if err != nil {
			return nil, err
		}
		out = append(out, application.RuleRecord{
			ID: r.ID, RuleSetVersionID: r.RuleSetVersionID, Code: r.Code, Name: r.Name,
			Priority: int(r.Priority), Condition: r.Condition, Actions: actions,
			ExplanationCode: r.ExplanationCode, ExplanationParams: params,
			StopOnMatch: r.StopOnMatch, Active: r.Active, CreatedAt: r.CreatedAt,
		})
	}
	return out, nil
}

// ReplaceRules implements application.Repository. The whole set is deleted and rewritten:
// a rule is its code, its condition and its priority together, and nothing hangs off a
// rule row that would make preserving its id worth the merge.
func (Repository) ReplaceRules(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID,
	rows []domain.RuleInput,
) error {
	q := sqlcgen.New(tx)
	if _, err := q.DeleteRules(ctx, sqlcgen.DeleteRulesParams{
		TenantID: tenantID, RuleSetVersionID: versionID,
	}); err != nil {
		return fmt.Errorf("rules: clear rules: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	params := make([]sqlcgen.CreateRuleParams, 0, len(rows))
	for _, r := range rows {
		actions, err := marshalActions(r.Actions)
		if err != nil {
			return err
		}
		explanation, err := marshalObject(r.ExplanationParams)
		if err != nil {
			return err
		}
		params = append(params, sqlcgen.CreateRuleParams{
			TenantID: tenantID, RuleSetVersionID: versionID, Code: r.Code, Name: r.Name,
			Priority:  int32(r.Priority), //nolint:gosec // validated as 1..100000 by the domain
			Condition: r.Condition, Actions: actions, ExplanationCode: r.ExplanationCode,
			ExplanationParams: explanation, StopOnMatch: r.StopOnMatch, Active: r.Active,
		})
	}
	if err := execBatch(q.CreateRule(ctx, params)); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			if pgErr.ConstraintName == "uq_rule_priority" {
				return application.ErrRulePriorityTaken
			}
			return application.ErrRuleCodeTaken
		}
		return fmt.Errorf("rules: replace rules: %w", err)
	}
	return nil
}

// ListTestCases implements application.Repository.
func (Repository) ListTestCases(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) ([]application.TestCaseRecord, error) {
	rows, err := sqlcgen.New(tx).ListRuleTestCases(ctx, sqlcgen.ListRuleTestCasesParams{
		TenantID: tenantID, RuleSetVersionID: versionID,
	})
	if err != nil {
		return nil, fmt.Errorf("rules: list test cases: %w", err)
	}
	out := make([]application.TestCaseRecord, 0, len(rows))
	for _, r := range rows {
		input, err := unmarshalObject(r.Input)
		if err != nil {
			return nil, err
		}
		record := application.TestCaseRecord{
			ID: r.ID, RuleSetVersionID: r.RuleSetVersionID, Code: r.Code,
			Description: r.Description, Input: input, ExpectedOutcome: r.ExpectedOutcome,
			ExpectedExplanations: nonNilStrings(r.ExpectedExplanations), CreatedAt: r.CreatedAt,
		}
		// A NULL expected_actions means "this case does not assert on actions", which is a
		// different statement from an empty array's "it must produce none".
		if len(r.ExpectedActions) > 0 {
			actions, err := unmarshalActions(r.ExpectedActions)
			if err != nil {
				return nil, err
			}
			record.ExpectedActions, record.AssertsActions = actions, true
		}
		out = append(out, record)
	}
	return out, nil
}

// ReplaceTestCases implements application.Repository.
func (Repository) ReplaceTestCases(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID,
	rows []domain.TestCaseInput,
) error {
	q := sqlcgen.New(tx)
	if _, err := q.DeleteRuleTestCases(ctx, sqlcgen.DeleteRuleTestCasesParams{
		TenantID: tenantID, RuleSetVersionID: versionID,
	}); err != nil {
		return fmt.Errorf("rules: clear test cases: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	params := make([]sqlcgen.CreateRuleTestCaseParams, 0, len(rows))
	for _, c := range rows {
		input, err := marshalObject(c.Input)
		if err != nil {
			return err
		}
		row := sqlcgen.CreateRuleTestCaseParams{
			TenantID: tenantID, RuleSetVersionID: versionID, Code: c.Code,
			Description: c.Description, Input: input, ExpectedOutcome: c.ExpectedOutcome,
			ExpectedExplanations: nonNilStrings(c.ExpectedExplanations),
		}
		if c.AssertsActions {
			actions, err := marshalActions(c.ExpectedActions)
			if err != nil {
				return err
			}
			row.ExpectedActions = actions
		}
		params = append(params, row)
	}
	if err := execBatch(q.CreateRuleTestCase(ctx, params)); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return application.ErrTestCaseCodeTaken
		}
		return fmt.Errorf("rules: replace test cases: %w", err)
	}
	return nil
}

// CreateEvaluation implements application.Repository: the header and its trace commit
// together, so a recorded outcome never exists without the lines that produced it.
func (Repository) CreateEvaluation(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewEvaluationRow, results []application.EvaluationResultRow,
) (uuid.UUID, time.Time, error) {
	q := sqlcgen.New(tx)
	snapshot, err := marshalObject(in.InputSnapshot)
	if err != nil {
		return uuid.Nil, time.Time{}, err
	}
	header, err := q.CreateRuleEvaluation(ctx, sqlcgen.CreateRuleEvaluationParams{
		TenantID: tenantID, SubjectType: in.SubjectType, SubjectID: nullUUID(in.SubjectID),
		RuleSetVersionID: in.RuleSetVersionID, InputHash: in.InputHash, InputSnapshot: snapshot,
		Outcome: in.Outcome, DurationMs: int32Ptr(in.DurationMs), EvaluatedBy: nullUUID(in.EvaluatedBy),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
			return uuid.Nil, time.Time{}, application.ErrVersionNotFound
		}
		return uuid.Nil, time.Time{}, fmt.Errorf("rules: create evaluation: %w", err)
	}
	if len(results) == 0 {
		return header.ID, header.EvaluatedAt, nil
	}
	params := make([]sqlcgen.CreateRuleEvaluationResultParams, 0, len(results))
	for _, r := range results {
		payload, err := optionalObject(r.ActionPayload)
		if err != nil {
			return uuid.Nil, time.Time{}, err
		}
		params = append(params, sqlcgen.CreateRuleEvaluationResultParams{
			TenantID: tenantID, EvaluationID: header.ID,
			Sequence: int32(r.Sequence), //nolint:gosec // one line per rule, bounded by the rule set size
			RuleID:   nullUUID(r.RuleID), RuleCode: r.RuleCode, Matched: r.Matched,
			ActionType: r.ActionType, ActionPayload: payload,
			ExplanationCode: r.ExplanationCode, Severity: r.Severity,
		})
	}
	if err := execBatch(q.CreateRuleEvaluationResult(ctx, params)); err != nil {
		return uuid.Nil, time.Time{}, fmt.Errorf("rules: create evaluation results: %w", err)
	}
	return header.ID, header.EvaluatedAt, nil
}

// GetEvaluation implements application.Repository.
func (Repository) GetEvaluation(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (application.EvaluationRecord, error) {
	q := sqlcgen.New(tx)
	row, err := q.GetRuleEvaluation(ctx, sqlcgen.GetRuleEvaluationParams{TenantID: tenantID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.EvaluationRecord{}, application.ErrEvaluationNotFound
	}
	if err != nil {
		return application.EvaluationRecord{}, fmt.Errorf("rules: get evaluation: %w", err)
	}
	snapshot, err := unmarshalObject(row.InputSnapshot)
	if err != nil {
		return application.EvaluationRecord{}, err
	}
	lines, err := q.ListRuleEvaluationResults(ctx, sqlcgen.ListRuleEvaluationResultsParams{
		TenantID: tenantID, EvaluationID: id,
	})
	if err != nil {
		return application.EvaluationRecord{}, fmt.Errorf("rules: list evaluation results: %w", err)
	}
	out := application.EvaluationRecord{
		ID: row.ID, SubjectType: row.SubjectType, SubjectID: uuidPtr(row.SubjectID),
		RuleSetVersionID: row.RuleSetVersionID, RuleSetID: row.RuleSetID,
		RuleSetCode: row.RuleSetCode, VersionNo: int(row.VersionNo),
		InputHash: row.InputHash, InputSnapshot: snapshot, Outcome: row.Outcome,
		DurationMs: intPtr(row.DurationMs), EvaluatedAt: row.EvaluatedAt,
		EvaluatedBy: uuidPtr(row.EvaluatedBy),
		Results:     make([]application.EvaluationResultRow, 0, len(lines)),
	}
	for _, l := range lines {
		payload, err := unmarshalOptionalObject(l.ActionPayload)
		if err != nil {
			return application.EvaluationRecord{}, err
		}
		out.Results = append(out.Results, application.EvaluationResultRow{
			Sequence: int(l.Sequence), RuleID: uuidPtr(l.RuleID), RuleCode: l.RuleCode,
			Matched: l.Matched, ActionType: l.ActionType, ActionPayload: payload,
			ExplanationCode: l.ExplanationCode, Severity: l.Severity,
		})
	}
	return out, nil
}

// storedAction is the jsonb shape of one action; the closed type list and the typed
// payload are validated by the domain before anything reaches here.
type storedAction struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload,omitempty"`
}

func marshalActions(actions []domain.ActionInput) ([]byte, error) {
	rows := make([]storedAction, 0, len(actions))
	for _, a := range actions {
		rows = append(rows, storedAction{Type: a.Type, Payload: a.Payload})
	}
	out, err := json.Marshal(rows)
	if err != nil {
		return nil, fmt.Errorf("rules: encode actions: %w", err)
	}
	return out, nil
}

func unmarshalActions(raw []byte) ([]domain.ActionInput, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var rows []storedAction
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("rules: decode actions: %w", err)
	}
	out := make([]domain.ActionInput, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain.ActionInput{Type: r.Type, Payload: r.Payload})
	}
	return out, nil
}

func marshalSchema(schema map[string]string) ([]byte, error) {
	if schema == nil {
		schema = map[string]string{}
	}
	out, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("rules: encode input schema: %w", err)
	}
	return out, nil
}

func unmarshalSchema(raw []byte) (map[string]string, error) {
	out := map[string]string{}
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("rules: decode input schema: %w", err)
	}
	return out, nil
}

func marshalObject(m map[string]any) ([]byte, error) {
	if m == nil {
		m = map[string]any{}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("rules: encode object: %w", err)
	}
	return out, nil
}

// optionalObject encodes a payload that may be absent; NULL and an empty object are
// different answers to "what did this action ask for".
func optionalObject(m map[string]any) ([]byte, error) {
	if m == nil {
		return nil, nil
	}
	return marshalObject(m)
}

func unmarshalObject(raw []byte) (map[string]any, error) {
	out := map[string]any{}
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("rules: decode object: %w", err)
	}
	return out, nil
}

func unmarshalOptionalObject(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	return unmarshalObject(raw)
}

// batchExecutor is the shape the pipelined inserts share.
type batchExecutor interface {
	Exec(func(int, error))
	Close() error
}

// execBatch runs a pipelined insert inside the caller's transaction and returns the first
// error; any failure aborts the whole transaction, so a replacement is all-or-nothing.
func execBatch(batch batchExecutor) error {
	var firstErr error
	batch.Exec(func(_ int, err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	})
	if err := batch.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func nullUUID(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}

func uuidPtr(n uuid.NullUUID) *uuid.UUID {
	if !n.Valid {
		return nil
	}
	id := n.UUID
	return &id
}

func intPtr(n *int32) *int {
	if n == nil {
		return nil
	}
	v := int(*n)
	return &v
}

func int32Ptr(n *int) *int32 {
	if n == nil {
		return nil
	}
	v := int32(*n) //nolint:gosec // a millisecond duration of one pass
	return &v
}

func dateValue(t *time.Time) pgtype.Date {
	if t == nil || t.IsZero() {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: domain.DateOnly(*t), Valid: true}
}

func datePtr(d pgtype.Date) *time.Time {
	if !d.Valid || d.InfinityModifier != pgtype.Finite {
		return nil
	}
	t := d.Time
	return &t
}

func pageSize(n int) int32 {
	if n <= 0 {
		n = httpx.DefaultPageSize
	}
	// The limit is clamped by httpx.ClampLimit long before it reaches here.
	return int32(n)
}
