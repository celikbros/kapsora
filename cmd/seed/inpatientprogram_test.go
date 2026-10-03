package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	contractapp "github.com/celikbros/kapsora/internal/contract/application"
	contractdomain "github.com/celikbros/kapsora/internal/contract/domain"
	contractpg "github.com/celikbros/kapsora/internal/contract/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/contract/selection"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
	requestpg "github.com/celikbros/kapsora/internal/servicerequest/infrastructure/postgres"
)

func TestInpatientProgramGuard(t *testing.T) {
	id := uuid.New()
	baseline := benefitapp.Program{ProgramType: "EMPLOYEE_BENEFIT", SponsorOrganizationID: uuid.New(), PayerOrganizationID: uuid.New()}
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	from, to := now.Add(-48*time.Hour), now.Add(10*24*time.Hour)
	good := benefitapp.Program{
		ID: id, Code: "PC04_" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", "")),
		Name: "PC04 inpatient " + id.String(), ProgramType: baseline.ProgramType,
		Status:                benefitdomain.ProgramDraft,
		SponsorOrganizationID: baseline.SponsorOrganizationID, PayerOrganizationID: baseline.PayerOrganizationID,
		ValidFrom: &from, ValidTo: &to,
	}
	if err := validateInpatientProgram(good, id, baseline, now); err != nil {
		t.Fatal(err)
	}
	active := good
	active.Status = benefitdomain.ProgramActive
	if err := validateInpatientProgram(active, id, baseline, now); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*benefitapp.Program){
		"wrong ID":                    func(p *benefitapp.Program) { p.ID = uuid.New() },
		"unmarked code":               func(p *benefitapp.Program) { p.Code = "DEMO_BENEFIT" },
		"lowercase marker":            func(p *benefitapp.Program) { p.Code = strings.ToLower(p.Code) },
		"invalid marker":              func(p *benefitapp.Program) { p.Code = "PC04_" + strings.Repeat("Z", 32) },
		"wrong name":                  func(p *benefitapp.Program) { p.Name = "PC04 inpatient " + uuid.NewString() },
		"wrong sponsor":               func(p *benefitapp.Program) { p.SponsorOrganizationID = uuid.New() },
		"wrong payer":                 func(p *benefitapp.Program) { p.PayerOrganizationID = uuid.New() },
		"draft window starts late":    func(p *benefitapp.Program) { x := now.Add(-24 * time.Hour); p.ValidFrom = &x },
		"extension window ends early": func(p *benefitapp.Program) { x := now.Add(7 * 24 * time.Hour); p.ValidTo = &x },
		"over 31 days":                func(p *benefitapp.Program) { x := now.Add(31 * 24 * time.Hour); p.ValidTo = &x },
		"unavailable status":          func(p *benefitapp.Program) { p.Status = "CLOSED" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := good
			mutate(&p)
			if err := validateInpatientProgram(p, id, baseline, now); err == nil {
				t.Fatal("unexpected fixture was accepted")
			}
		})
	}
}

func TestInpatientExactComparisons(t *testing.T) {
	a := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	b := a.In(time.FixedZone("offset", 3*3600))
	c := a.Add(time.Hour)
	if !sameDates(&a, &b) || sameDates(&a, &c) || sameDates(nil, &a) {
		t.Fatal("date comparison changed")
	}
	for _, value := range []string{"20", "20.000000", "2e1"} {
		if !decimalIs(value, "20") {
			t.Fatalf("%q should equal 20", value)
		}
	}
	for _, value := range []string{"19.9", "bad", ""} {
		if decimalIs(value, "20") {
			t.Fatalf("%q should differ from 20", value)
		}
	}
}

// dbtest creates and drops its own database; this never targets the running application.
func TestInpatientProgramPublishesAndReusesOnlyDedicatedFixture(t *testing.T) {
	h := dbtest.New(t)
	t.Setenv("KAPSORA_SEED_DEMO_PASSWORD", "demo parola 2026 kapsora")
	s := newDemoSeeder(t, h.App)
	ctx := context.Background()
	if err := s.demo(ctx); err != nil {
		t.Fatal(err)
	}
	tenant, err := sqlcgen.New(h.App).GetTenantIDByCode(ctx, "DEMO_A")
	if err != nil {
		t.Fatal(err)
	}
	maker, err := s.credentials.FindByUsername(ctx, "admin.a")
	if err != nil {
		t.Fatal(err)
	}
	rc := rcTenant(tenant, maker.ActorID)
	baselineID, err := s.findProgram(ctx, rc, "DEMO_BENEFIT")
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := s.benefits.GetProgram(ctx, rc, baselineID)
	if err != nil {
		t.Fatal(err)
	}
	from, to := day(time.Now().UTC()).AddDate(0, 0, -2), day(time.Now().UTC()).AddDate(0, 0, 10)
	program, err := s.benefits.CreateProgram(ctx, rc, benefitapp.NewProgramInput{
		Code: "PC04_" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", "")),
		Name: "Preparing inpatient fixture", ProgramType: baseline.ProgramType,
		SponsorOrganizationID: baseline.SponsorOrganizationID, PayerOrganizationID: baseline.PayerOrganizationID,
		ValidFrom: &from, ValidTo: &to,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.inpatientProgram(ctx, program.ID); err == nil {
		t.Fatal("unmarked fixture was accepted")
	}
	name := "PC04 inpatient " + program.ID.String()
	program, err = s.benefits.UpdateProgram(ctx, rc, program.ID, benefitapp.ProgramPatch{Name: &name, ExpectedVersion: program.RowVersion})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.inpatientProgram(ctx, program.ID)
	if err != nil {
		t.Fatal(describeValidation(err))
	}
	second, err := s.inpatientProgram(ctx, program.ID)
	if err != nil || first != second {
		t.Fatalf("rerun: %+v vs %+v: %v", first, second, err)
	}
	if first.PlanID == uuid.Nil || first.PlanVersionID == uuid.Nil || first.ContractID == uuid.Nil || first.ContractVersionID == uuid.Nil || first.ServiceDefinitionID == uuid.Nil {
		t.Fatalf("incomplete fixture result: %+v", first)
	}
	current, err := s.benefits.GetProgram(ctx, rc, baselineID)
	if err != nil || current.Status != baseline.Status {
		t.Fatalf("baseline program changed: %+v %v", current, err)
	}
	err = db.WithTenantTx(ctx, h.App, db.TenantContext{TenantID: tenant}, func(ctx context.Context, tx pgx.Tx) error {
		for programID, want := range map[uuid.UUID]bool{program.ID: true, baselineID: true} {
			got, err := requestpg.New().ReviewRequired(ctx, tx, tenant, programID)
			if err != nil {
				return err
			}
			if got != want {
				t.Errorf("review for %s: got %v, want %v", programID, got, want)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	createMarked := func(start, end time.Time) benefitapp.Program {
		t.Helper()
		p, err := s.benefits.CreateProgram(ctx, rc, benefitapp.NewProgramInput{
			Code: "PC04_" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", "")),
			Name: "Preparing inpatient fixture", ProgramType: baseline.ProgramType,
			SponsorOrganizationID: baseline.SponsorOrganizationID, PayerOrganizationID: baseline.PayerOrganizationID,
			ValidFrom: &start, ValidTo: &end,
		})
		if err != nil {
			t.Fatal(err)
		}
		name := "PC04 inpatient " + p.ID.String()
		p, err = s.benefits.UpdateProgram(ctx, rc, p.ID, benefitapp.ProgramPatch{Name: &name, ExpectedVersion: p.RowVersion})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	narrowFrom, narrowTo := day(time.Now().UTC()).AddDate(0, 0, -2), day(time.Now().UTC()).AddDate(0, 0, 9)
	reusable := createMarked(narrowFrom, narrowTo)
	reused, err := s.inpatientProgram(ctx, reusable.ID)
	if err != nil {
		t.Fatal(describeValidation(err))
	}
	if reused.ContractID != first.ContractID || reused.ContractVersionID != first.ContractVersionID || reused.PlanID == first.PlanID {
		t.Fatalf("second program should reuse one tariff with its own plan: first=%+v second=%+v", first, reused)
	}
	shared, err := s.biz.contracts.GetContract(ctx, rc, first.ContractID)
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithTenantTx(ctx, h.App, db.TenantContext{TenantID: tenant}, func(ctx context.Context, tx pgx.Tx) error {
		for _, serviceDay := range []time.Time{day(time.Now().UTC().Add(-25 * time.Hour)), day(time.Now().UTC().Add(8 * 24 * time.Hour))} {
			records, err := contractpg.New().ListCandidates(ctx, tx, tenant, contractapp.CandidateQuery{
				ProviderProfileID: shared.ProviderProfileID, ServiceDefinitionID: first.ServiceDefinitionID, ServiceDate: serviceDay,
			})
			if err != nil {
				return err
			}
			candidates := make([]selection.Candidate, 0, len(records))
			for _, row := range records {
				candidates = append(candidates, row.Candidate)
			}
			picked := selection.Select(selection.Request{ServiceDate: serviceDay, DefinitionID: first.ServiceDefinitionID}, candidates)
			if picked.Winner == nil || picked.Winner.ContractVersionID != first.ContractVersionID || picked.Reason != "" || len(records) != 1 || !decimalIs(records[0].Detail.Amount, "400") {
				t.Errorf("shared tariff did not select one 400 TRY contract on %s: candidates=%d winner=%+v reason=%s", serviceDay, len(records), picked.Winner, picked.Reason)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	outsideFrom, outsideTo := day(time.Now().UTC()).AddDate(0, 0, -3), day(time.Now().UTC()).AddDate(0, 0, 11)
	outside := createMarked(outsideFrom, outsideTo)
	if _, err := s.inpatientProgram(ctx, outside.ID); err == nil {
		t.Fatal("outside shared contract window was accepted")
	}
	plans, err := s.benefits.ListPlans(ctx, rc, outside.ID)
	if err != nil || len(plans) != 0 {
		t.Fatalf("outside fixture was mutated: %d plans, %v", len(plans), err)
	}

	checker, err := s.credentials.FindByUsername(ctx, "reviewer.a")
	if err != nil {
		t.Fatal(err)
	}
	shared, err = s.biz.contracts.GetContract(ctx, rc, first.ContractID)
	if err != nil {
		t.Fatal(err)
	}
	foreign := &scenario{tenant: tenant, admin: maker.ActorID, publisher: checker.ActorID,
		payerOrg: baseline.PayerOrganizationID, sponsorOrg: baseline.SponsorOrganizationID,
		hospitalProfile: shared.ProviderProfileID}
	_, err = s.ensureContract(ctx, foreign, contractSpec{
		Code: "PC04_FOREIGN_TARIFF", Name: "Competing test tariff", Domain: "HEALTH", ProfileID: shared.ProviderProfileID,
		Items: []contractdomain.PriceItemInput{{ServiceDefinitionID: first.ServiceDefinitionID.String(),
			UnitType: "NIGHT", PricingMethod: "UNIT", Amount: "401", MemberShareMethod: "NONE",
			ValidFrom: contractValidFrom(s.clock.at)}}, DueDays: 30,
	})
	if err != nil {
		t.Fatal(describeValidation(err))
	}
	blocked := createMarked(narrowFrom, narrowTo)
	if _, err := s.inpatientProgram(ctx, blocked.ID); err == nil {
		t.Fatal("competing hospital tariff was accepted")
	}
	plans, err = s.benefits.ListPlans(ctx, rc, blocked.ID)
	if err != nil || len(plans) != 0 {
		t.Fatalf("competing-tariff fixture was mutated: %d plans, %v", len(plans), err)
	}
}
