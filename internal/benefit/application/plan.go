package application

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
)

// Plan is the view returned by the plan endpoints; Versions carries the summaries of the
// plan's versions, newest number first.
type Plan struct {
	ID         uuid.UUID
	ProgramID  uuid.UUID
	Code       string
	Name       string
	Status     string
	Versions   []PlanVersion
	RowVersion int64
}

// NewPlanInput is the create command.
type NewPlanInput struct {
	Code string
	Name string
}

// PlanPatch is a merge-patch of an existing plan.
type PlanPatch struct {
	Name            *string
	Status          *string
	ExpectedVersion int64
}

// CreatePlan adds a DRAFT plan under a program of the caller's tenant.
func (s *Service) CreatePlan(ctx context.Context, rc identity.RequestContext, programID uuid.UUID, in NewPlanInput) (Plan, error) {
	in.Code = strings.TrimSpace(in.Code)
	in.Name = strings.TrimSpace(in.Name)

	ve := &domain.ValidationError{}
	domain.ValidatePlanCode(ve, in.Code)
	domain.ValidateName(ve, "name", in.Name)
	if err := ve.OrNil(); err != nil {
		return Plan{}, err
	}

	var out Plan
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetProgram(ctx, tx, rc.TenantID, programID); err != nil {
			return err
		}
		planID, err := s.repo.CreatePlan(ctx, tx, NewPlanRow{
			TenantID: rc.TenantID, ProgramID: programID, Code: in.Code, Name: in.Name,
		})
		if err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "plan.create", "plan", planID, map[string]any{
			"program_id": programID,
		}); err != nil {
			return err
		}
		out, err = s.loadPlan(ctx, tx, rc.TenantID, planID)
		return err
	})
	return out, err
}

// GetPlan returns one plan with its version summaries.
func (s *Service) GetPlan(ctx context.Context, rc identity.RequestContext, planID uuid.UUID) (Plan, error) {
	var out Plan
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = s.loadPlan(ctx, tx, rc.TenantID, planID)
		return err
	})
	return out, err
}

// ListPlans returns the plans of a program ordered by code.
func (s *Service) ListPlans(ctx context.Context, rc identity.RequestContext, programID uuid.UUID) ([]Plan, error) {
	var out []Plan
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetProgram(ctx, tx, rc.TenantID, programID); err != nil {
			return err
		}
		rows, err := s.repo.ListPlans(ctx, tx, rc.TenantID, programID)
		if err != nil {
			return err
		}
		out = make([]Plan, 0, len(rows))
		for _, r := range rows {
			versions, err := s.repo.ListPlanVersions(ctx, tx, rc.TenantID, r.ID)
			if err != nil {
				return err
			}
			out = append(out, planView(r, versions))
		}
		return nil
	})
	return out, err
}

// UpdatePlan applies a merge-patch of name and status under optimistic concurrency.
func (s *Service) UpdatePlan(ctx context.Context, rc identity.RequestContext, planID uuid.UUID, patch PlanPatch) (Plan, error) {
	ve := &domain.ValidationError{}
	if patch.Name != nil {
		domain.ValidateName(ve, "name", strings.TrimSpace(*patch.Name))
	}
	if patch.Status != nil && !domain.Contains(domain.PlanUpdateStatuses, *patch.Status) {
		ve.Add("status", "ENUM", "ACTIVE veya RETIRED olmalı")
	}
	if err := ve.OrNil(); err != nil {
		return Plan{}, err
	}

	var out Plan
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.GetPlan(ctx, tx, rc.TenantID, planID)
		if err != nil {
			return err
		}
		if current.RowVersion != patch.ExpectedVersion {
			return ErrVersionMismatch
		}
		next := PlanUpdateRow{Name: current.Name, Status: current.Status, Expected: patch.ExpectedVersion}
		if patch.Name != nil {
			next.Name = strings.TrimSpace(*patch.Name)
		}
		if patch.Status != nil {
			if !domain.PlanTransitionAllowed(current.Status, *patch.Status) {
				return ErrPlanTransition
			}
			next.Status = *patch.Status
		}
		if err := s.repo.UpdatePlan(ctx, tx, rc.TenantID, planID, next); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "plan.update", "plan", planID, map[string]any{
			"plan_status": next.Status,
		}); err != nil {
			return err
		}
		out, err = s.loadPlan(ctx, tx, rc.TenantID, planID)
		return err
	})
	return out, err
}

func (s *Service) loadPlan(ctx context.Context, tx pgx.Tx, tenantID, planID uuid.UUID) (Plan, error) {
	row, err := s.repo.GetPlan(ctx, tx, tenantID, planID)
	if err != nil {
		return Plan{}, err
	}
	versions, err := s.repo.ListPlanVersions(ctx, tx, tenantID, planID)
	if err != nil {
		return Plan{}, err
	}
	return planView(row, versions), nil
}

func planView(r PlanRow, versions []PlanVersionRow) Plan {
	out := Plan{
		ID: r.ID, ProgramID: r.ProgramID, Code: r.Code, Name: r.Name, Status: r.Status,
		RowVersion: r.RowVersion, Versions: make([]PlanVersion, 0, len(versions)),
	}
	for _, v := range versions {
		out.Versions = append(out.Versions, versionView(v, nil))
	}
	return out
}
