package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
	requestpg "github.com/celikbros/kapsora/internal/servicerequest/infrastructure/postgres"
)

func TestAutomaticProgramPreservesOtherSettings(t *testing.T) {
	id, other := uuid.New(), uuid.New()
	for _, raw := range []string{"null", "[]", `{"default":"no"}`, `{"programs":null}`, `{"programs":[]}`} {
		if _, err := programReviewJSON([]byte(raw), id, false); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	raw := []byte(`{"default":true,"custom":{"keep":1},"programs":{"` + other.String() + `":true}}`)
	changed, err := programReviewJSON(raw, id, false)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Default  bool
		Custom   map[string]int
		Programs map[string]bool
	}
	if err := json.Unmarshal(changed, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Default || got.Custom["keep"] != 1 || !got.Programs[other.String()] || got.Programs[id.String()] {
		t.Fatal("unrelated setting changed")
	}
}

func TestAutomaticProgramFixtureIsScopedAndRepeatable(t *testing.T) {
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
	programs, err := s.benefits.ListPrograms(ctx, rc, benefitapp.ProgramFilter{Query: "DEMO_BENEFIT", Limit: 100})
	if err != nil || len(programs.Items) != 1 {
		t.Fatalf("demo program: %v", err)
	}
	baseline := programs.Items[0]
	if _, err := s.automaticProgram(ctx, baseline.ID, false); err == nil {
		t.Fatal("ordinary program accepted")
	}
	from := time.Now().UTC().Truncate(24 * time.Hour)
	to := from.AddDate(0, 0, 2)
	program, err := s.benefits.CreateProgram(ctx, rc, benefitapp.NewProgramInput{
		Code: "PC02_A_" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", "")), Name: "Preparing fixture", ProgramType: baseline.ProgramType,
		SponsorOrganizationID: baseline.SponsorOrganizationID, PayerOrganizationID: baseline.PayerOrganizationID, ValidFrom: &from, ValidTo: &to,
	})
	if err != nil {
		t.Fatalf("%#v", err)
	}
	if _, err := s.automaticProgram(ctx, program.ID, false); err == nil {
		t.Fatal("missing UUID marker accepted")
	}
	name := "PC02 automatic " + program.ID.String()
	if _, err := s.benefits.UpdateProgram(ctx, rc, program.ID, benefitapp.ProgramPatch{Name: &name, ExpectedVersion: program.RowVersion}); err != nil {
		t.Fatal(err)
	}
	first, err := s.automaticProgram(ctx, program.ID, false)
	if err != nil {
		t.Fatalf("%#v", err)
	}
	second, err := s.automaticProgram(ctx, program.ID, false)
	if err != nil || second != first || first.ReviewRequired || first.PlanID == uuid.Nil {
		t.Fatalf("repeat: %+v %v", second, err)
	}
	check := func(want bool) {
		t.Helper()
		err := db.WithTenantTx(ctx, h.App, db.TenantContext{TenantID: tenant}, func(ctx context.Context, tx pgx.Tx) error {
			for id, expected := range map[uuid.UUID]bool{program.ID: want, baseline.ID: true} {
				actual, err := requestpg.New().ReviewRequired(ctx, tx, tenant, id)
				if err != nil {
					return err
				}
				if actual != expected {
					t.Errorf("review for %s = %v, want %v", id, actual, expected)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	check(false)
	for range 2 {
		if out, err := s.automaticProgram(ctx, program.ID, true); err != nil || !out.ReviewRequired {
			t.Fatalf("disable: %+v %v", out, err)
		}
	}
	check(true)
}
