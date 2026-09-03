package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Program is the view returned by create, get, list and update.
type Program struct {
	ID                    uuid.UUID
	Code                  string
	Name                  string
	ProgramType           string
	Status                string
	SponsorOrganizationID uuid.UUID
	PayerOrganizationID   uuid.UUID
	SponsorDisplayName    string
	PayerDisplayName      string
	ValidFrom             *time.Time
	ValidTo               *time.Time
	PlanCount             int
	RowVersion            int64
}

// ProgramPage is one keyset page of programs.
type ProgramPage struct {
	Items      []Program
	NextCursor string
}

// NewProgramInput is the create command.
type NewProgramInput struct {
	Code                  string
	Name                  string
	ProgramType           string
	SponsorOrganizationID uuid.UUID
	PayerOrganizationID   uuid.UUID
	ValidFrom             *time.Time
	ValidTo               *time.Time
}

// ProgramPatch is a merge-patch of an existing program.
type ProgramPatch struct {
	Name            *string
	Status          *string
	ValidFrom       *time.Time
	ClearValidFrom  bool
	ValidTo         *time.Time
	ClearValidTo    bool
	ExpectedVersion int64
}

// ProgramFilter is the API-level list request.
type ProgramFilter struct {
	Query  string
	Status string
	Cursor string
	Limit  int
}

// CreateProgram registers a program in DRAFT under a sponsor and a payer organization.
func (s *Service) CreateProgram(ctx context.Context, rc identity.RequestContext, in NewProgramInput) (Program, error) {
	in.Code = strings.TrimSpace(in.Code)
	in.Name = strings.TrimSpace(in.Name)
	in.ProgramType = strings.TrimSpace(in.ProgramType)
	in.ValidFrom, in.ValidTo = datePtr(in.ValidFrom), datePtr(in.ValidTo)

	ve := &domain.ValidationError{}
	domain.ValidateProgramCode(ve, in.Code)
	domain.ValidateName(ve, "name", in.Name)
	domain.ValidateProgramType(ve, in.ProgramType)
	domain.ValidatePeriod(ve, "valid", in.ValidFrom, in.ValidTo)
	if err := ve.OrNil(); err != nil {
		return Program{}, err
	}

	var out Program
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if err := s.checkOrganization(ctx, tx, rc.TenantID, "sponsorOrganizationId", in.SponsorOrganizationID, "SPONSOR", ve); err != nil {
			return err
		}
		if err := s.checkOrganization(ctx, tx, rc.TenantID, "payerOrganizationId", in.PayerOrganizationID, "PAYER", ve); err != nil {
			return err
		}
		catalog, err := s.repo.GetProgramType(ctx, tx, rc.TenantID, in.ProgramType)
		switch {
		case errors.Is(err, ErrCatalogEntryNotFound):
			ve.Add("programType", "PROGRAM_TYPE_UNKNOWN", "program türü tanımlı değil")
		case err != nil:
			return err
		case catalog.Status != domain.ProgramActive:
			ve.Add("programType", "PROGRAM_TYPE_UNKNOWN", "program türü kullanım dışı")
		}
		if ve.Len() > 0 {
			return ve
		}

		programID, err := s.repo.CreateProgram(ctx, tx, NewProgramRow{
			TenantID: rc.TenantID, SponsorOrganizationID: in.SponsorOrganizationID,
			PayerOrganizationID: in.PayerOrganizationID, Code: in.Code, Name: in.Name,
			ProgramType: in.ProgramType, ValidFrom: in.ValidFrom, ValidTo: in.ValidTo,
		})
		if err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "program.create", "program", programID, map[string]any{
			"program_type": in.ProgramType, "sponsor_organization_id": in.SponsorOrganizationID,
			"payer_organization_id": in.PayerOrganizationID,
		}); err != nil {
			return err
		}
		row, err := s.repo.GetProgram(ctx, tx, rc.TenantID, programID)
		if err != nil {
			return err
		}
		out = programView(row)
		return nil
	})
	return out, err
}

// GetProgram returns one program of the caller's tenant.
func (s *Service) GetProgram(ctx context.Context, rc identity.RequestContext, programID uuid.UUID) (Program, error) {
	var out Program
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		row, err := s.repo.GetProgram(ctx, tx, rc.TenantID, programID)
		if err != nil {
			return err
		}
		out = programView(row)
		return nil
	})
	return out, err
}

// ListPrograms returns one page ordered by creation time, newest first.
func (s *Service) ListPrograms(ctx context.Context, rc identity.RequestContext, f ProgramFilter) (ProgramPage, error) {
	ve := &domain.ValidationError{}
	if f.Status != "" && !domain.Contains(domain.ProgramStatuses, f.Status) {
		ve.Add("status", "ENUM", "geçersiz durum")
	}
	if err := ve.OrNil(); err != nil {
		return ProgramPage{}, err
	}
	cursor, hasCursor, err := s.cursors.Decode(f.Cursor)
	if err != nil {
		return ProgramPage{}, err
	}
	pageSize := httpx.ClampLimit(f.Limit)
	q := ProgramListQuery{Status: f.Status, PageSize: pageSize + 1}
	q.Pattern = domain.LikePattern(f.Query)
	if hasCursor {
		q.After = &cursor
	}

	var rows []ProgramRow
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rows, err = s.repo.ListPrograms(ctx, tx, rc.TenantID, q)
		return err
	})
	if err != nil {
		return ProgramPage{}, err
	}
	page := ProgramPage{Items: make([]Program, 0, len(rows))}
	if len(rows) > pageSize {
		last := rows[pageSize-1]
		page.NextCursor = s.cursors.Encode(httpx.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
		rows = rows[:pageSize]
	}
	for _, r := range rows {
		page.Items = append(page.Items, programView(r))
	}
	return page, nil
}

// UpdateProgram applies a merge-patch of name, validity and status under optimistic
// concurrency. Only the transitions of section 2.1 are accepted.
func (s *Service) UpdateProgram(ctx context.Context, rc identity.RequestContext, programID uuid.UUID, patch ProgramPatch) (Program, error) {
	ve := &domain.ValidationError{}
	if patch.Name != nil {
		domain.ValidateName(ve, "name", strings.TrimSpace(*patch.Name))
	}
	if patch.Status != nil && !domain.Contains(domain.ProgramUpdateStatuses, *patch.Status) {
		ve.Add("status", "ENUM", "ACTIVE, SUSPENDED veya CLOSED olmalı")
	}
	if err := ve.OrNil(); err != nil {
		return Program{}, err
	}

	var out Program
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.GetProgram(ctx, tx, rc.TenantID, programID)
		if err != nil {
			return err
		}
		if current.RowVersion != patch.ExpectedVersion {
			return ErrVersionMismatch
		}
		next := ProgramUpdateRow{
			Name: current.Name, Status: current.Status,
			ValidFrom: current.ValidFrom, ValidTo: current.ValidTo, Expected: patch.ExpectedVersion,
		}
		if patch.Name != nil {
			next.Name = strings.TrimSpace(*patch.Name)
		}
		if patch.Status != nil {
			if !domain.ProgramTransitionAllowed(current.Status, *patch.Status) {
				return ErrProgramTransition
			}
			next.Status = *patch.Status
		}
		switch {
		case patch.ClearValidFrom:
			next.ValidFrom = nil
		case patch.ValidFrom != nil:
			next.ValidFrom = datePtr(patch.ValidFrom)
		}
		switch {
		case patch.ClearValidTo:
			next.ValidTo = nil
		case patch.ValidTo != nil:
			next.ValidTo = datePtr(patch.ValidTo)
		}
		domain.ValidatePeriod(ve, "valid", next.ValidFrom, next.ValidTo)
		if ve.Len() > 0 {
			return ve
		}
		if err := s.repo.UpdateProgram(ctx, tx, rc.TenantID, programID, next); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "program.update", "program", programID, map[string]any{
			"program_status": next.Status,
		}); err != nil {
			return err
		}
		row, err := s.repo.GetProgram(ctx, tx, rc.TenantID, programID)
		if err != nil {
			return err
		}
		out = programView(row)
		return nil
	})
	return out, err
}

// checkOrganization collects a field error when the organization is unknown or does not
// carry the role the program needs.
func (s *Service) checkOrganization(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	field string, organizationID uuid.UUID, role string, ve *domain.ValidationError) error {
	org, err := s.repo.GetOrganization(ctx, tx, tenantID, organizationID)
	switch {
	case errors.Is(err, ErrNotFound):
		ve.Add(field, "ORGANIZATION_INVALID", "kurum bulunamadı")
	case err != nil:
		return err
	case org.Role != role:
		ve.Add(field, "ORGANIZATION_INVALID", "kurum "+role+" rolünde olmalı")
	case org.Status != domain.ProgramActive:
		ve.Add(field, "ORGANIZATION_INVALID", "kurum ilişkisi etkin değil")
	}
	return nil
}

func programView(r ProgramRow) Program {
	return Program{
		ID: r.ID, Code: r.Code, Name: r.Name, ProgramType: r.ProgramType, Status: r.Status,
		SponsorOrganizationID: r.SponsorOrganizationID, PayerOrganizationID: r.PayerOrganizationID,
		SponsorDisplayName: r.SponsorDisplayName, PayerDisplayName: r.PayerDisplayName,
		ValidFrom: r.ValidFrom, ValidTo: r.ValidTo, PlanCount: int(r.PlanCount), RowVersion: r.RowVersion,
	}
}
