package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
	rulesapp "github.com/celikbros/kapsora/internal/rules/application"
	rulesdomain "github.com/celikbros/kapsora/internal/rules/domain"
	rulespg "github.com/celikbros/kapsora/internal/rules/infrastructure/postgres"
)

type documentRuleResult struct {
	PersonID  uuid.UUID `json:"personId"`
	VersionID uuid.UUID `json:"versionId"`
	Status    string    `json:"status"`
}

// documentRule is opt-in local fixture setup, not a grant to a demo login. Like the
// existing demo plan/contract seed, it uses application services and distinct maker
// and checker identities. Only a dedicated synthetic person may be targeted.
func (s *seeder) documentRule(ctx context.Context, personID uuid.UUID, retire bool) (documentRuleResult, error) {
	tenantID, err := sqlcgen.New(s.pool).GetTenantIDByCode(ctx, "DEMO_A")
	if err != nil {
		return documentRuleResult{}, fmt.Errorf("document fixture needs existing DEMO_A: %w", err)
	}
	err = db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenantID}, func(ctx context.Context, tx pgx.Tx) error {
		var dedicated bool
		if err := tx.QueryRow(ctx, `SELECT first_name = 'Deneme' AND last_name = $3
            FROM party.person WHERE tenant_id=$1 AND id=$2`, tenantID, personID,
			"Belgekural"+strings.ReplaceAll(personID.String(), "-", "")).Scan(&dedicated); err != nil {
			return err
		}
		if !dedicated {
			return fmt.Errorf("document fixture refuses a non-dedicated person")
		}
		return nil
	})
	if err != nil {
		return documentRuleResult{}, err
	}
	maker, err := s.credentials.FindByUsername(ctx, "admin.a")
	if err != nil {
		return documentRuleResult{}, err
	}
	checker, err := s.credentials.FindByUsername(ctx, "reviewer.a")
	if err != nil {
		return documentRuleResult{}, err
	}
	cursors, err := httpx.NewCursorCodec(cursorKey)
	if err != nil {
		return documentRuleResult{}, err
	}
	svc, err := rulesapp.New(rulesapp.Deps{Pool: s.pool, Repo: rulespg.New(), Audit: auditpg.New(), Cursors: cursors})
	if err != nil {
		return documentRuleResult{}, err
	}
	return ensureDocumentRule(ctx, svc, tenantID, maker.ActorID, checker.ActorID, personID, retire)
}

// The predicate and its two publish-gate cases prevent this opt-in scenario from
// introducing a tenant-wide document requirement. Versions expire as a second
// boundary if an interrupted test cannot retire its rule.
func documentRuleContent(personID uuid.UUID) (rulesdomain.RuleInput, []rulesdomain.TestCaseInput) {
	return rulesdomain.RuleInput{
		Code: "PC02_INVOICE", Name: "Synthetic request evidence", Priority: 10,
		Condition: fmt.Sprintf("personId == '%s'", personID), Active: true,
		ExplanationCode: "PC02_DOCUMENT_REQUIRED",
		Actions:         []rulesdomain.ActionInput{{Type: "REQUIRE_DOCUMENT", Payload: map[string]any{"documentTypeCode": "INVOICE"}}},
	}, []rulesdomain.TestCaseInput{
		{Code: "TARGET_PERSON", Input: map[string]any{"personId": personID.String()},
			ExpectedOutcome: "REVIEW_REQUIRED", ExpectedExplanations: []string{"PC02_DOCUMENT_REQUIRED"}},
		{Code: "OTHER_PERSON_UNCHANGED", Input: map[string]any{"personId": uuid.Nil.String()},
			ExpectedOutcome: "APPROVED", ExpectedExplanations: []string{}},
	}
}

func ensureDocumentRule(ctx context.Context, svc *rulesapp.Service, tenantID, maker, checker, personID uuid.UUID, retire bool) (documentRuleResult, error) {
	out := documentRuleResult{PersonID: personID, Status: "ABSENT"}
	if personID == uuid.Nil || maker == uuid.Nil || checker == uuid.Nil || maker == checker {
		return out, fmt.Errorf("document fixture needs a person and distinct maker/checker identities")
	}
	rc := rcTenant(tenantID, maker)
	code := "PC02_D_" + strings.ReplaceAll(personID.String(), "-", "")
	sets, err := svc.ListRuleSets(ctx, rc, rulesapp.ListFilter{Query: code, Limit: 100})
	if err != nil {
		return out, err
	}
	var set rulesapp.RuleSetRecord
	for _, candidate := range sets.Items {
		if candidate.Code == strings.ToUpper(code) {
			set = candidate
		}
	}
	if set.ID == uuid.Nil {
		if retire {
			return out, nil
		}
		set, err = svc.CreateRuleSet(ctx, rc, rulesdomain.NewRuleSet{Code: code, Name: "PC02 synthetic document gate", DomainCode: "HEALTH", Purpose: "DOCUMENT"})
		if err != nil {
			return out, err
		}
	}
	if set.Purpose != "DOCUMENT" || set.DomainCode != "HEALTH" {
		return out, fmt.Errorf("unexpected fixture rule set")
	}
	versions, err := svc.ListVersions(ctx, rc, set.ID)
	if err != nil {
		return out, err
	}
	if len(versions) > 1 {
		return out, fmt.Errorf("fixture must have exactly one rule version")
	}
	var view rulesapp.VersionView
	if len(versions) == 0 {
		if retire {
			return out, nil
		}
		today := time.Now().UTC().Truncate(24 * time.Hour)
		from, to := today.AddDate(0, 0, -1), today.AddDate(0, 0, 2)
		view, err = svc.CreateVersion(ctx, rc, set.ID, rulesapp.NewVersionInput{
			ValidFrom: &from, ValidTo: &to, InputSchema: map[string]string{"personId": "string"},
		})
	} else {
		view, err = svc.GetVersion(ctx, rc, versions[0].ID)
	}
	if err != nil {
		return out, err
	}
	out.VersionID, out.Status = view.Version.ID, view.Version.Status
	rule, cases := documentRuleContent(personID)
	if view.Version.Status != "DRAFT" && (len(view.Rules) != 1 || view.Rules[0].Condition != rule.Condition) {
		return out, fmt.Errorf("fixture rule predicate does not match its dedicated person")
	}
	if retire {
		if view.Version.Status == "PUBLISHED" {
			view, err = svc.RetireVersion(ctx, rcTenant(tenantID, checker), view.Version.ID, "PC02_ACCEPTANCE_FINISHED", nil, view.Version.RowVersion)
			if err != nil {
				return out, err
			}
			out.Status = view.Version.Status
		}
		return out, nil
	}
	if view.Version.Status == "RETIRED" {
		return out, fmt.Errorf("retired fixture requires a new synthetic person")
	}
	if view.Version.Status == "DRAFT" {
		rules, err := svc.ReplaceRules(ctx, rc, view.Version.ID, []rulesdomain.RuleInput{rule}, view.Version.RowVersion)
		if err != nil {
			return out, err
		}
		tests, err := svc.ReplaceTestCases(ctx, rc, view.Version.ID, cases, rules.RowVersion)
		if err != nil {
			return out, err
		}
		view, err = svc.SubmitVersion(ctx, rc, view.Version.ID, nil, tests.RowVersion)
		if err != nil {
			return out, err
		}
	}
	if view.Version.Status == "UNDER_REVIEW" {
		view, err = svc.PublishVersion(ctx, rcTenant(tenantID, checker), view.Version.ID, nil, view.Version.RowVersion)
		if err != nil {
			return out, err
		}
	}
	if view.Version.Status != "PUBLISHED" {
		return out, fmt.Errorf("unexpected fixture version status %s", view.Version.Status)
	}
	out.Status = view.Version.Status
	return out, nil
}
