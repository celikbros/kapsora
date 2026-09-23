package main

import (
	"context"
	"testing"

	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	rulesapp "github.com/celikbros/kapsora/internal/rules/application"
	rulespg "github.com/celikbros/kapsora/internal/rules/infrastructure/postgres"
)

func TestClaimReviewRuleFixturePublishesOnlyItsPersonAndRetiresIdempotently(t *testing.T) {
	h := dbtest.New(t)
	tenant := h.CreateTenant("CLAIM_REVIEW_FIXTURE")
	maker := h.CreateActor("fixture-maker", "Fixture maker")
	checker := h.CreateActor("fixture-checker", "Fixture checker")
	cursors, err := httpx.NewCursorCodec(cursorKey)
	if err != nil {
		t.Fatalf("%#v", err)
	}
	svc, err := rulesapp.New(rulesapp.Deps{Pool: h.App, Repo: rulespg.New(), Audit: auditpg.New(), Cursors: cursors})
	if err != nil {
		t.Fatalf("%#v", err)
	}
	ctx := context.Background()
	person := uuid.New()
	if _, err := ensureClaimReviewRule(ctx, svc, tenant, maker, maker, person, false); err == nil {
		t.Fatal("same maker/checker accepted")
	}
	if _, err := ensureClaimReviewRule(ctx, svc, tenant, maker, checker, uuid.Nil, false); err == nil {
		t.Fatal("empty person accepted")
	}
	first, err := ensureClaimReviewRule(ctx, svc, tenant, maker, checker, person, false)
	if err != nil {
		t.Fatalf("%#v", err)
	}
	if first.Status != "PUBLISHED" {
		t.Fatalf("status = %s", first.Status)
	}
	repeated, err := ensureClaimReviewRule(ctx, svc, tenant, maker, checker, person, false)
	if err != nil || repeated != first {
		t.Fatalf("repeat = %+v, %v", repeated, err)
	}
	view, err := svc.GetVersion(ctx, rcTenant(tenant, maker), first.VersionID)
	if err != nil {
		t.Fatalf("%#v", err)
	}
	if len(view.Rules) != 1 || len(view.TestCases) != 2 || view.Version.ValidTo == nil {
		t.Fatal("fixture lost scoped rule, positive/negative publish-gate tests or expiry")
	}
	// SubmitVersion ran both assertions: the target requires both review stages; another person
	// remains approved. A failing case would have prevented publication entirely.
	rule, _ := claimReviewRuleContent(person)
	if view.Rules[0].Condition != rule.Condition || len(view.Rules[0].Actions) != 2 {
		t.Fatal("published predicate changed")
	}
	retired, err := ensureClaimReviewRule(ctx, svc, tenant, maker, checker, person, true)
	if err != nil || retired.Status != "RETIRED" {
		t.Fatalf("retire = %+v, %v", retired, err)
	}
	again, err := ensureClaimReviewRule(ctx, svc, tenant, maker, checker, person, true)
	if err != nil || again != retired {
		t.Fatalf("repeat retire = %+v, %v", again, err)
	}
	if _, err := ensureClaimReviewRule(ctx, svc, tenant, maker, checker, person, false); err == nil {
		t.Fatal("retired fixture silently reactivated")
	}
	var grants int
	if err := h.Admin.QueryRow(ctx, `SELECT count(*) FROM iam.access_grant WHERE tenant_id=$1`, tenant).Scan(&grants); err != nil {
		t.Fatalf("%#v", err)
	}
	if grants != 0 {
		t.Fatal("fixture changed human access grants")
	}
}
