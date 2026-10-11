package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

type automaticProgramResult struct {
	ProgramID      uuid.UUID `json:"programId"`
	PlanID         uuid.UUID `json:"planId"`
	ReviewRequired bool      `json:"reviewRequired"`
}

// automaticProgram provisions only a dedicated, short-lived program created by
// the public-API acceptance harness. Existing programs, defaults and grants stay intact.
func (s *seeder) automaticProgram(ctx context.Context, id uuid.UUID, disable bool) (automaticProgramResult, error) {
	out := automaticProgramResult{ProgramID: id, ReviewRequired: true}
	tenant, err := sqlcgen.New(s.pool).GetTenantIDByCode(ctx, "DEMO_A")
	if err != nil {
		return out, err
	}
	maker, err := s.credentials.FindByUsername(ctx, "admin.a")
	if err != nil {
		return out, err
	}
	checker, err := s.credentials.FindByUsername(ctx, "reviewer.a")
	if err != nil {
		return out, err
	}
	if maker.ActorID == checker.ActorID {
		return out, fmt.Errorf("fixture needs distinct maker and checker")
	}
	rc := rcTenant(tenant, maker.ActorID)
	program, err := s.benefits.GetProgram(ctx, rc, id)
	if err != nil {
		return out, err
	}
	if !strings.HasPrefix(program.Code, "PC02_A_") || program.Name != "PC02 automatic "+id.String() ||
		program.ValidFrom == nil || program.ValidTo == nil || program.ValidTo.Sub(*program.ValidFrom).Hours() > 96 {
		return out, fmt.Errorf("automatic fixture refuses a non-dedicated or unbounded program")
	}
	if disable {
		return out, db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenant}, func(ctx context.Context, tx pgx.Tx) error {
			return writeProgramReview(ctx, tx, tenant, id, true)
		})
	}
	if program.Status != benefitdomain.ProgramActive {
		active := benefitdomain.ProgramActive
		if _, err := s.benefits.UpdateProgram(ctx, rc, id, benefitapp.ProgramPatch{Status: &active, ExpectedVersion: program.RowVersion}); err != nil {
			return out, err
		}
	}
	plans, err := s.benefits.ListPlans(ctx, rc, id)
	if err != nil {
		return out, err
	}
	if len(plans) > 1 {
		return out, fmt.Errorf("fixture must have only one plan")
	}
	var plan benefitapp.Plan
	if len(plans) == 0 {
		plan, err = s.benefits.CreatePlan(ctx, rc, id, benefitapp.NewPlanInput{Code: "AUTO_PHYSIO", Name: "Otomatik karar test planı"})
	} else {
		plan = plans[0]
	}
	if err != nil {
		return out, err
	}
	if plan.Code != "AUTO_PHYSIO" {
		return out, fmt.Errorf("unexpected fixture plan")
	}
	out.PlanID = plan.ID
	versions, err := s.benefits.ListPlanVersions(ctx, rc, plan.ID)
	if err != nil {
		return out, err
	}
	if len(versions) > 1 {
		return out, fmt.Errorf("fixture must have only one plan version")
	}
	var version benefitapp.PlanVersion
	if len(versions) == 0 {
		version, err = s.benefits.CreatePlanVersion(ctx, rc, plan.ID, benefitapp.NewPlanVersionInput{ValidFrom: program.ValidFrom, ValidTo: program.ValidTo})
	} else {
		version, err = s.benefits.GetPlanVersion(ctx, rc, versions[0].ID)
	}
	if err != nil {
		return out, err
	}
	if version.Status == benefitdomain.VersionDraft {
		version, err = s.benefits.ReplaceDefinitions(ctx, rc, version.ID, []benefitdomain.EntitlementDefinition{{
			Code: "PHYSIO_SESSION", Name: "Fizyoterapi seansı", UnitType: "SESSION", PeriodType: "CALENDAR_YEAR", InitialQuantity: "20",
		}}, version.RowVersion)
		if err != nil {
			return out, err
		}
		var serviceID uuid.UUID
		err = db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenant}, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT id FROM catalog.service_definition WHERE tenant_id=$1 AND code='PHYSIO_SESSION'`, tenant).Scan(&serviceID)
		})
		if err != nil {
			return out, err
		}
		if _, err := s.benefits.ReplaceMappings(ctx, rc, version.ID, []benefitapp.MappingInput{{ServiceDefinitionID: serviceID, EntitlementCode: "PHYSIO_SESSION", UnitFactor: "1"}}, version.RowVersion); err != nil {
			return out, err
		}
		version, err = s.benefits.GetPlanVersion(ctx, rc, version.ID)
		if err != nil {
			return out, err
		}
		version, err = s.benefits.SubmitPlanVersion(ctx, rc, version.ID, nil, version.RowVersion)
		if err != nil {
			return out, err
		}
	}
	if version.Status == benefitdomain.VersionUnderReview {
		version, err = s.benefits.PublishPlanVersion(ctx, rcTenant(tenant, checker.ActorID), version.ID, nil, version.RowVersion)
		if err != nil {
			return out, err
		}
	}
	if version.Status != benefitdomain.VersionPublished {
		return out, fmt.Errorf("fixture version is not published")
	}
	if plan.Status != benefitdomain.PlanActive {
		active := benefitdomain.PlanActive
		if _, err := s.benefits.UpdatePlan(ctx, rc, plan.ID, benefitapp.PlanPatch{Status: &active, ExpectedVersion: plan.RowVersion}); err != nil {
			return out, err
		}
	}
	err = db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenant}, func(ctx context.Context, tx pgx.Tx) error {
		return writeProgramReview(ctx, tx, tenant, id, false)
	})
	if err == nil {
		out.ReviewRequired = false
	}
	return out, err
}

// Settings have no public write API. Serialize this offline fixture's JSON merge;
// refuse malformed settings instead of repairing them into automatic approval.
func writeProgramReview(ctx context.Context, tx pgx.Tx, tenant, program uuid.UUID, required bool) error {
	if program == uuid.Nil {
		return fmt.Errorf("program is required")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.tenant_setting (tenant_id, setting_key, value_json)
		VALUES ($1, 'service_request.review_required', '{}'::jsonb) ON CONFLICT DO NOTHING`, tenant); err != nil {
		return err
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT value_json FROM platform.tenant_setting WHERE tenant_id=$1
		AND setting_key='service_request.review_required' FOR UPDATE`, tenant).Scan(&raw); err != nil {
		return err
	}
	updated, err := programReviewJSON(raw, program, required)
	if err != nil {
		return err
	}
	return sqlcgen.New(tx).UpsertTenantSetting(ctx, sqlcgen.UpsertTenantSettingParams{TenantID: tenant, SettingKey: "service_request.review_required", ValueJson: updated})
}

func programReviewJSON(raw []byte, program uuid.UUID, required bool) ([]byte, error) {
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(raw, &settings); err != nil || settings == nil {
		return nil, errors.New("invalid review setting")
	}
	if value, ok := settings["default"]; ok && string(value) != "true" && string(value) != "false" {
		return nil, errors.New("invalid default review setting")
	}
	programs := map[string]bool{}
	if value, ok := settings["programs"]; ok {
		if err := json.Unmarshal(value, &programs); err != nil || programs == nil {
			return nil, errors.New("invalid program review setting")
		}
	}
	programs[program.String()] = required
	encoded, err := json.Marshal(programs)
	if err != nil {
		return nil, err
	}
	settings["programs"] = encoded
	return json.Marshal(settings)
}
