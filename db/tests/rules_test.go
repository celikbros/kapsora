package dbtests

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// rulesSeed is one tenant with everything migration 000022 hangs together: a rule set, a
// draft version, one rule and one test case.
type rulesSeed struct {
	tenant  uuid.UUID
	actor   uuid.UUID
	ruleSet uuid.UUID
	version uuid.UUID
	rule    uuid.UUID
}

func seedRules(h *dbtest.Harness, code string) rulesSeed {
	h.T.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()

	s := rulesSeed{tenant: h.CreateTenant(code)}
	s.actor = h.CreateActor("rules-"+code, "Rule Author "+code)
	must := func(err error, what string) {
		h.T.Helper()
		if err != nil {
			h.T.Fatalf("seed %s: %v", what, err)
		}
	}
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO rules.rule_set (tenant_id, code, name, domain_code, purpose)
		VALUES ($1, 'DOCUMENT_HEALTH', 'Sağlık belge kuralları', 'HEALTH', 'DOCUMENT')
		RETURNING id`, s.tenant).Scan(&s.ruleSet), "rule set")
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO rules.rule_set_version (tenant_id, rule_set_id, version_no, valid_from, input_schema)
		VALUES ($1, $2, 1, '2026-01-01', '{"claim":"map"}'::jsonb) RETURNING id`,
		s.tenant, s.ruleSet).Scan(&s.version), "version")
	must(h.Admin.QueryRow(ctx, `
		INSERT INTO rules.rule (tenant_id, rule_set_version_id, code, name, priority, condition,
		                        actions, explanation_code)
		VALUES ($1, $2, 'RECEIPT_REQUIRED', 'Fatura zorunlu', 10, 'claim.amount > 0',
		        '[{"type":"REQUIRE_DOCUMENT","payload":{"documentTypeCode":"INVOICE"}}]'::jsonb,
		        'DOCUMENT_REQUIRED')
		RETURNING id`, s.tenant, s.version).Scan(&s.rule), "rule")
	must(h.AdminExecErr(`
		INSERT INTO rules.rule_test_case (tenant_id, rule_set_version_id, code, input,
		                                  expected_outcome, expected_explanations)
		VALUES ($1, $2, 'HAS_AMOUNT', '{"claim":{"amount":100}}'::jsonb,
		        'REVIEW_REQUIRED', ARRAY['DOCUMENT_REQUIRED'])`, s.tenant, s.version), "test case")
	return s
}

// publishRuleVersion moves a version to PUBLISHED as the schema owner, which is how the
// tests reach the states the exclusion constraint is about without going through the
// service.
func publishRuleVersion(h *dbtest.Harness, s rulesSeed, versionID uuid.UUID, from string, to any) error {
	h.T.Helper()
	return h.AdminExecErr(`
		UPDATE rules.rule_set_version
		   SET status = 'PUBLISHED', valid_from = $3::date, valid_to = $4::date,
		       content_hash = 'deadbeef', published_by = $5, published_at = clock_timestamp()
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, versionID, from, to, s.actor)
}

// TestRulePriorityIsUnique is the constraint the evaluation order rests on: two rules of
// one version may not share a priority, because "whatever the index returned" is not an
// order and a decision has to be reproducible years later.
func TestRulePriorityIsUnique(t *testing.T) {
	h := dbtest.New(t)
	s := seedRules(h, "RULES_PRIORITY")

	err := h.AdminExecErr(`
		INSERT INTO rules.rule (tenant_id, rule_set_version_id, code, name, priority, condition,
		                        explanation_code)
		VALUES ($1, $2, 'SECOND_RULE', 'İkinci kural', 10, 'true', 'OTHER')`,
		s.tenant, s.version)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "duplicate rule priority")

	// A different priority in the same version is fine, and so is the same priority in a
	// different version: the order only has to be total inside one version.
	if err := h.AdminExecErr(`
		INSERT INTO rules.rule (tenant_id, rule_set_version_id, code, name, priority, condition,
		                        explanation_code)
		VALUES ($1, $2, 'SECOND_RULE', 'İkinci kural', 20, 'true', 'OTHER')`,
		s.tenant, s.version); err != nil {
		t.Fatalf("distinct priority refused: %v", err)
	}

	var second uuid.UUID
	ctx, cancel := h.Ctx()
	defer cancel()
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO rules.rule_set_version (tenant_id, rule_set_id, version_no)
		VALUES ($1, $2, 2) RETURNING id`, s.tenant, s.ruleSet).Scan(&second); err != nil {
		t.Fatalf("second version: %v", err)
	}
	if err := h.AdminExecErr(`
		INSERT INTO rules.rule (tenant_id, rule_set_version_id, code, name, priority, condition,
		                        explanation_code)
		VALUES ($1, $2, 'RECEIPT_REQUIRED', 'Fatura zorunlu', 10, 'true', 'DOCUMENT_REQUIRED')`,
		s.tenant, second); err != nil {
		t.Fatalf("same priority in another version refused: %v", err)
	}
}

// TestPublishedRuleSetVersionsMayNotOverlap: "which rules applied on this date" must have
// exactly one answer, so two published versions of one set may not cover the same day.
func TestPublishedRuleSetVersionsMayNotOverlap(t *testing.T) {
	h := dbtest.New(t)
	s := seedRules(h, "RULES_OVERLAP")

	if err := publishRuleVersion(h, s, s.version, "2026-01-01", "2027-01-01"); err != nil {
		t.Fatalf("publish first version: %v", err)
	}

	ctx, cancel := h.Ctx()
	defer cancel()
	var second uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO rules.rule_set_version (tenant_id, rule_set_id, version_no, valid_from)
		VALUES ($1, $2, 2, '2026-06-01') RETURNING id`, s.tenant, s.ruleSet).Scan(&second); err != nil {
		t.Fatalf("second version: %v", err)
	}
	err := publishRuleVersion(h, s, second, "2026-06-01", nil)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateExclusionViolation, "overlapping published version")

	// A version starting where the first ends is the next agreement, not a competing one.
	if err := publishRuleVersion(h, s, second, "2027-01-01", nil); err != nil {
		t.Fatalf("adjacent published version: %v", err)
	}

	// A draft covering the same days is not a competing answer: only published versions
	// are ever evaluated, so only published versions are excluded.
	var draft uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO rules.rule_set_version (tenant_id, rule_set_id, version_no, valid_from, valid_to)
		VALUES ($1, $2, 3, '2026-03-01', '2026-09-01') RETURNING id`,
		s.tenant, s.ruleSet).Scan(&draft); err != nil {
		t.Fatalf("overlapping draft: %v", err)
	}
}

// TestPublishedRuleSetVersionMustBeComplete covers
// ck_rule_set_version_published_complete and ck_rule_set_version_retire_reason: no code
// path can publish a half-filled row or retire one without saying why.
func TestPublishedRuleSetVersionMustBeComplete(t *testing.T) {
	h := dbtest.New(t)
	s := seedRules(h, "RULES_COMPLETE")

	err := h.AdminExecErr(`
		UPDATE rules.rule_set_version SET status = 'PUBLISHED'
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.version)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "publish without a hash or a publisher")

	if err := publishRuleVersion(h, s, s.version, "2026-01-01", nil); err != nil {
		t.Fatalf("complete publish: %v", err)
	}

	err = h.AdminExecErr(`
		UPDATE rules.rule_set_version SET status = 'RETIRED'
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.version)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "retire without a reason")
}

// TestRuleEvaluationIsAppendOnly: a recorded decision is what a dispute two years later
// reads, so neither the header nor a line of its trace may be updated or deleted.
func TestRuleEvaluationIsAppendOnly(t *testing.T) {
	h := dbtest.New(t)
	s := seedRules(h, "RULES_APPEND")
	if err := publishRuleVersion(h, s, s.version, "2026-01-01", nil); err != nil {
		t.Fatalf("publish: %v", err)
	}

	ctx, cancel := h.Ctx()
	defer cancel()
	var evaluation uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO rules.evaluation (tenant_id, subject_type, rule_set_version_id, input_hash,
		                              input_snapshot, outcome)
		VALUES ($1, 'CLAIM', $2, sha256('x'::bytea), '{"claimId":"a"}'::jsonb, 'REVIEW_REQUIRED')
		RETURNING id`, s.tenant, s.version).Scan(&evaluation); err != nil {
		t.Fatalf("insert evaluation: %v", err)
	}
	if err := h.AdminExecErr(`
		INSERT INTO rules.evaluation_result (tenant_id, evaluation_id, sequence, rule_id, rule_code,
		                                     matched, action_type, explanation_code, severity)
		VALUES ($1, $2, 1, $3, 'RECEIPT_REQUIRED', true, 'REQUIRE_DOCUMENT', 'DOCUMENT_REQUIRED', 'WARNING')`,
		s.tenant, evaluation, s.rule); err != nil {
		t.Fatalf("insert evaluation result: %v", err)
	}

	err := h.AdminExecErr(`
		UPDATE rules.evaluation SET outcome = 'APPROVED' WHERE tenant_id = $1 AND id = $2`,
		s.tenant, evaluation)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "update an evaluation")

	err = h.AdminExecErr(`DELETE FROM rules.evaluation WHERE tenant_id = $1 AND id = $2`,
		s.tenant, evaluation)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "delete an evaluation")

	err = h.AdminExecErr(`
		UPDATE rules.evaluation_result SET matched = false
		 WHERE tenant_id = $1 AND evaluation_id = $2`, s.tenant, evaluation)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateIntegrityConstraint, "update an evaluation result")

	// A second line at the same position would make the trace ambiguous about what ran
	// when, so the sequence is unique inside one evaluation.
	err = h.AdminExecErr(`
		INSERT INTO rules.evaluation_result (tenant_id, evaluation_id, sequence, rule_code,
		                                     matched, explanation_code, severity)
		VALUES ($1, $2, 1, 'OTHER', false, 'OTHER', 'INFO')`, s.tenant, evaluation)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation, "duplicate trace sequence")
}

// TestRuleActionTypeIsClosed: the action list is what the callers of the engine switch on,
// so a type outside it must not reach a stored trace at all.
func TestRuleActionTypeIsClosed(t *testing.T) {
	h := dbtest.New(t)
	s := seedRules(h, "RULES_ACTION")
	if err := publishRuleVersion(h, s, s.version, "2026-01-01", nil); err != nil {
		t.Fatalf("publish: %v", err)
	}

	ctx, cancel := h.Ctx()
	defer cancel()
	var evaluation uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		INSERT INTO rules.evaluation (tenant_id, subject_type, rule_set_version_id, input_hash,
		                              input_snapshot, outcome)
		VALUES ($1, 'CLAIM', $2, sha256('y'::bytea), '{}'::jsonb, 'APPROVED')
		RETURNING id`, s.tenant, s.version).Scan(&evaluation); err != nil {
		t.Fatalf("insert evaluation: %v", err)
	}
	err := h.AdminExecErr(`
		INSERT INTO rules.evaluation_result (tenant_id, evaluation_id, sequence, rule_code,
		                                     matched, action_type, explanation_code, severity)
		VALUES ($1, $2, 1, 'X', true, 'DELETE_EVERYTHING', 'X', 'INFO')`, s.tenant, evaluation)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "unknown action type")
}

// TestRuleTenantIsolation: every table of the schema is invisible across tenants through
// the application role, and a write tagged with another tenant fails the WITH CHECK.
func TestRuleTenantIsolation(t *testing.T) {
	h := dbtest.New(t)
	a := seedRules(h, "RULES_RLS_A")
	b := seedRules(h, "RULES_RLS_B")

	countFor := func(tenant uuid.UUID, table string) int {
		t.Helper()
		var n int
		err := h.AppTx(tenant, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n)
		})
		if err != nil {
			t.Fatalf("count %s as tenant %s: %v", table, tenant, err)
		}
		return n
	}
	for _, table := range []string{
		"rules.rule_set", "rules.rule_set_version", "rules.rule", "rules.rule_test_case",
	} {
		if got := countFor(a.tenant, table); got != 1 {
			t.Fatalf("tenant A sees %d rows of %s, want 1", got, table)
		}
	}

	// A rule tagged with tenant B written inside a tenant A transaction is refused; the
	// composite foreign key means it cannot name tenant B's version either.
	err := h.AppTx(a.tenant, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO rules.rule (tenant_id, rule_set_version_id, code, name, priority,
			                        condition, explanation_code)
			VALUES ($1, $2, 'CROSS_TENANT', 'Sızıntı', 99, 'true', 'X')`, b.tenant, b.version)
		return err
	})
	if got := dbtest.SQLState(err); got != dbtest.SQLStateInsufficientPrivilege {
		t.Fatalf("cross-tenant rule insert returned %q (%v), want %s",
			got, err, dbtest.SQLStateInsufficientPrivilege)
	}
}
