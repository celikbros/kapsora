// Package application implements the rule engine use cases: the rule set, the
// maker-checker lifecycle of its versions, the CEL rules and test cases that hang under a
// version, the publish gate that refuses an untested version, and the evaluation the rest
// of the system asks "what does this decide". Transactions are opened here with
// db.WithTenantTx, so a write and its audit row commit together and RLS is bound for every
// statement.
//
// The evaluator itself is internal/rules/engine and is used, never reimplemented: this
// package maps rows to engine.Rule, hands them to engine.Compile, and maps
// engine.Evaluation back to rows.
package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/rules/domain"
)

// Permissions guarding rule work (migration 000008, RULE_AUTHOR and RULE_APPROVER role
// templates). They live here rather than in the transport because whether a caller may see
// an unpublished draft is a business rule, not a routing detail.
const (
	PermissionRead    = "rule.read"
	PermissionDraft   = "rule.draft"
	PermissionPublish = "rule.publish"
)

// Errors mapped by the transport layer to problem codes.
var (
	ErrRuleSetNotFound    = errors.New("rules: rule set not found")
	ErrVersionNotFound    = errors.New("rules: rule set version not found")
	ErrEvaluationNotFound = errors.New("rules: rule evaluation not found")
	ErrRuleSetCodeTaken   = errors.New("rules: rule set code already used in this tenant")
	ErrRuleCodeTaken      = errors.New("rules: rule code already used in this version")
	ErrRulePriorityTaken  = errors.New("rules: two rules of one version may not share a priority")
	ErrTestCaseCodeTaken  = errors.New("rules: test case code already used in this version")
	ErrVersionMismatch    = errors.New("rules: row version does not match If-Match")
	ErrVersionImmutable   = errors.New("rules: a version that is not a draft cannot be changed")
	ErrVersionTransition  = errors.New("rules: this version status transition is not allowed")
	ErrVersionOverlap     = errors.New("rules: two published versions may not cover the same date")
	ErrMakerCheckerSame   = errors.New("rules: the publisher must differ from the submitter")
	ErrVersionNotLive     = errors.New("rules: only a published version may record an evaluation")
)

// RuleSetRecord is one rules.rule_set row with the number of versions under it.
type RuleSetRecord struct {
	ID           uuid.UUID
	Code         string
	Name         string
	DomainCode   string
	Purpose      string
	Status       string
	VersionCount int
	CreatedAt    time.Time
	RowVersion   int64
}

// NewRuleSetRow is the insert payload of a rule set.
type NewRuleSetRow struct {
	Code       string
	Name       string
	DomainCode string
	Purpose    string
}

// RuleSetUpdateRow is the update payload of a rule set; the code, the domain and the
// purpose are absent because every published version was written against them.
type RuleSetUpdateRow struct {
	Name   string
	Status string
}

// RuleSetQuery is the repository-level rule set filter.
type RuleSetQuery struct {
	DomainCode string
	Purpose    string
	Status     string
	Query      string
	After      *httpx.Cursor
	PageSize   int
}

// VersionRecord is one rules.rule_set_version row with the two counts the submit gate
// cares about.
type VersionRecord struct {
	ID               uuid.UUID
	RuleSetID        uuid.UUID
	RuleSetCode      string
	VersionNo        int
	Status           string
	ValidFrom        *time.Time
	ValidTo          *time.Time
	InputSchema      map[string]string
	ContentHash      *string
	Notes            *string
	SubmittedAt      *time.Time
	SubmittedBy      *uuid.UUID
	PublishedAt      *time.Time
	PublishedBy      *uuid.UUID
	RetireReasonCode *string
	ReviewComment    *string
	RuleCount        int
	TestCaseCount    int
	CreatedAt        time.Time
	RowVersion       int64
}

// NewVersionRow is the insert payload of a rule set version.
type NewVersionRow struct {
	RuleSetID   uuid.UUID
	VersionNo   int
	ValidFrom   *time.Time
	ValidTo     *time.Time
	InputSchema map[string]string
	Notes       *string
}

// VersionDraftRow is the merge-patch result written back to a draft version.
type VersionDraftRow struct {
	ValidFrom   *time.Time
	ValidTo     *time.Time
	InputSchema map[string]string
	Notes       *string
}

// SubmitRow, PublishRow and RetireRow are the three maker-checker writes.
type SubmitRow struct {
	ActorID uuid.UUID
	Comment *string
}

// PublishRow carries the checker and the hash the publish freezes.
type PublishRow struct {
	ActorID     uuid.UUID
	ContentHash string
	Comment     *string
}

// RetireRow carries the reason a published version was withdrawn.
type RetireRow struct {
	ReasonCode string
	ReasonText *string
}

// RuleRecord is one rules.rule row.
type RuleRecord struct {
	ID                uuid.UUID
	RuleSetVersionID  uuid.UUID
	Code              string
	Name              string
	Priority          int
	Condition         string
	Actions           []domain.ActionInput
	ExplanationCode   string
	ExplanationParams map[string]any
	StopOnMatch       bool
	Active            bool
	CreatedAt         time.Time
}

// TestCaseRecord is one rules.rule_test_case row. AssertsActions is false when the stored
// expected_actions is NULL, which means "this case does not assert on actions at all" and
// is a different statement from "it produces none".
type TestCaseRecord struct {
	ID                   uuid.UUID
	RuleSetVersionID     uuid.UUID
	Code                 string
	Description          *string
	Input                map[string]any
	ExpectedOutcome      string
	ExpectedExplanations []string
	ExpectedActions      []domain.ActionInput
	AssertsActions       bool
	CreatedAt            time.Time
}

// NewEvaluationRow is the append-only evaluation header.
type NewEvaluationRow struct {
	SubjectType      string
	SubjectID        *uuid.UUID
	RuleSetVersionID uuid.UUID
	InputHash        []byte
	InputSnapshot    map[string]any
	Outcome          string
	DurationMs       *int
	EvaluatedBy      *uuid.UUID
}

// EvaluationResultRow is one line of the stored trace.
type EvaluationResultRow struct {
	Sequence        int
	RuleID          *uuid.UUID
	RuleCode        string
	Matched         bool
	ActionType      *string
	ActionPayload   map[string]any
	ExplanationCode string
	Severity        string
}

// EvaluationRecord is a stored evaluation with the version that produced it.
type EvaluationRecord struct {
	ID               uuid.UUID
	SubjectType      string
	SubjectID        *uuid.UUID
	RuleSetVersionID uuid.UUID
	RuleSetID        uuid.UUID
	RuleSetCode      string
	VersionNo        int
	InputHash        []byte
	InputSnapshot    map[string]any
	Outcome          string
	DurationMs       *int
	EvaluatedAt      time.Time
	EvaluatedBy      *uuid.UUID
	Results          []EvaluationResultRow
}

// Repository is the persistence port; every method runs inside the caller's transaction,
// which db.WithTenantTx has already bound to the tenant so RLS is active.
type Repository interface {
	CreateRuleSet(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewRuleSetRow) (uuid.UUID, error)
	GetRuleSet(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (RuleSetRecord, error)
	ListRuleSets(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q RuleSetQuery) ([]RuleSetRecord, error)
	UpdateRuleSet(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in RuleSetUpdateRow, expected int64) error

	NextVersionNo(ctx context.Context, tx pgx.Tx, tenantID, ruleSetID uuid.UUID) (int, error)
	CreateVersion(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewVersionRow) (uuid.UUID, error)
	GetVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (VersionRecord, error)
	ListVersions(ctx context.Context, tx pgx.Tx, tenantID, ruleSetID uuid.UUID) ([]VersionRecord, error)
	// LockVersion reads the row FOR UPDATE, so two commands on one version serialise.
	LockVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (VersionRecord, error)
	UpdateVersionDraft(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in VersionDraftRow) error
	// TouchVersion bumps row_version without changing a business field, so writing a rule
	// or a test case invalidates the ETag the caller holds for the version.
	TouchVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	SubmitVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in SubmitRow) error
	// PublishVersion surfaces the published-overlap exclusion constraint as
	// ErrVersionOverlap.
	PublishVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in PublishRow) error
	RetireVersion(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, in RetireRow) error

	ListRules(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) ([]RuleRecord, error)
	// ReplaceRules deletes the whole set and writes the new one, so a rule that survives
	// gets a new id: a rule is its code, its condition and its priority together, and
	// nothing hangs off a rule row that would be worth preserving an id for.
	ReplaceRules(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, rows []domain.RuleInput) error

	ListTestCases(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID) ([]TestCaseRecord, error)
	ReplaceTestCases(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID, rows []domain.TestCaseInput) error

	CreateEvaluation(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewEvaluationRow,
		results []EvaluationResultRow) (uuid.UUID, time.Time, error)
	GetEvaluation(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (EvaluationRecord, error)
}
