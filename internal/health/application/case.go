package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/health/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// NewCaseInput is the create command.
type NewCaseInput struct {
	PersonID uuid.UUID
	// ProgramID is optional: an enrollment belongs to exactly one program, so the service
	// derives it and refuses a caller that named a different one. A provider-scoped actor
	// may read neither programs nor enrollments and could not repeat an id it never saw —
	// the lesson WP-I4-01's first real consumer paid for.
	ProgramID              *uuid.UUID
	EnrollmentID           uuid.UUID
	CaseType               string
	ProviderOrganizationID *uuid.UUID
	OpenedAt               *time.Time
	ServiceRequestID       *uuid.UUID
}

// ListFilter is the API-level list request.
type ListFilter struct {
	Cursor                 string
	Limit                  int
	PersonID               *uuid.UUID
	ProgramID              *uuid.UUID
	ProviderOrganizationID *uuid.UUID
	Status                 string
	CaseType               string
	OpenedFrom             *time.Time
	OpenedTo               *time.Time
}

// ListCases returns a page of cases with their encounters, each row in the projection the
// caller has earned.
//
// A list never answers 428. A sensitive case the caller may not fully read is served in the
// financial projection like any other, because refusing the whole page over one row would
// make the list useless and refusing that one row would say which row is sensitive. The
// single read is where the purpose is demanded.
func (s *Service) ListCases(ctx context.Context, rc identity.RequestContext, f ListFilter,
	req AccessRequest,
) (CasePage, error) {
	if err := domain.ValidateCaseStatusFilter(f.Status); err != nil {
		return CasePage{}, err
	}
	if err := domain.ValidateCaseTypeFilter(f.CaseType); err != nil {
		return CasePage{}, err
	}
	after, pageSize, err := s.paging(f.Cursor, f.Limit)
	if err != nil {
		return CasePage{}, err
	}

	var out CasePage
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.validateAccess(ctx, tx, req); err != nil {
			return err
		}
		rows, err := s.repo.ListCases(ctx, tx, rc.TenantID, CaseQuery{
			Scope: scopeOf(rc), PersonID: f.PersonID, ProgramID: f.ProgramID,
			ProviderOrganizationID: f.ProviderOrganizationID, Status: f.Status,
			CaseType: f.CaseType, OpenedFrom: f.OpenedFrom, OpenedTo: f.OpenedTo,
			After: after, PageSize: pageSize + 1,
		})
		if err != nil {
			return err
		}
		if len(rows) > pageSize {
			out.NextCursor = s.cursors.Encode(caseCursor(rows[pageSize-1]))
			rows = rows[:pageSize]
		}
		out.Items = make([]CaseView, 0, len(rows))
		for _, row := range rows {
			view, err := s.viewCase(ctx, tx, rc, row, req, audit.AccessSearch, listRead)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, view)
		}
		return nil
	})
	if err != nil {
		return CasePage{}, err
	}
	return out, nil
}

// GetCase returns one case with its encounters. This is the read that demands a purpose: a
// sensitive case the caller holds the sensitive grant for and stated no reason to open is
// refused, and the refusal is recorded.
func (s *Service) GetCase(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	req AccessRequest,
) (CaseView, error) {
	var (
		out    CaseView
		person uuid.UUID
	)
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.validateAccess(ctx, tx, req); err != nil {
			return err
		}
		record, err := s.repo.GetCase(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		person = record.PersonID
		out, err = s.viewCase(ctx, tx, rc, record, req, audit.AccessView, singleRead)
		return err
	})
	if errors.Is(err, ErrAccessPurposeRequired) {
		s.recordDenial(ctx, rc, person, domain.AggregateCase, id, req)
		return CaseView{}, err
	}
	if err != nil {
		return CaseView{}, err
	}
	return out, nil
}

// viewCase loads a case's encounters, decides what the caller may see, applies the
// projection and writes the access event a clinical read owes. It is the only path from a
// row to a view: everything that answers with a case goes through here, so there is one
// place the projection can be removed from and one place a test can prove it is applied.
//
// The mode says how this particular read treats a sensitive case it may not fully see; see
// readMode for the three of them.
func (s *Service) viewCase(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record CaseRecord, req AccessRequest, accessType audit.AccessType, mode readMode,
) (CaseView, error) {
	d, err := decide(rc, record.Sensitivity, req)
	if err != nil && mode.demandPurpose {
		return CaseView{}, err
	}
	encounters, err := s.repo.ListCaseEncounters(ctx, tx, rc.TenantID, record.ID)
	if err != nil {
		return CaseView{}, err
	}
	view := CaseView{
		Projection: d.projection,
		Case:       projectCase(record, d.projection),
		Encounters: projectEncounters(encounters, d.projection),
	}
	switch {
	case d.projection == ProjectionClinical && mode.recordSuccess:
		if err := s.recordAccess(ctx, tx, rc, record.PersonID, domain.AggregateCase,
			record.ID, accessType, req, audit.OutcomeSuccess); err != nil {
			return CaseView{}, err
		}
	case d.refusedSensitive && mode.recordRefusal:
		// The caller asked for a record it may not have and was given the financial
		// projection instead, which is exactly the row a security review looks for.
		if err := s.recordAccess(ctx, tx, rc, record.PersonID, domain.AggregateCase,
			record.ID, accessType, req, audit.OutcomeDenied); err != nil {
			return CaseView{}, err
		}
	}
	return view, nil
}

// CreateCase opens a case for a member. A case is always opened STANDARD: sensitivity is
// what its diagnoses make it, and nothing here accepts one.
func (s *Service) CreateCase(ctx context.Context, rc identity.RequestContext, in NewCaseInput) (CaseView, error) {
	now := s.now().UTC()
	if err := domain.ValidateNewCase(domain.NewCase{
		CaseType: in.CaseType, OpenedAt: in.OpenedAt, Now: now,
	}); err != nil {
		return CaseView{}, err
	}
	if in.PersonID == uuid.Nil || in.EnrollmentID == uuid.Nil {
		return CaseView{}, fieldError("enrollmentId", "REQUIRED", "hak sahibi ve plan kaydı zorunlu")
	}
	openedAt := now
	if in.OpenedAt != nil {
		openedAt = in.OpenedAt.UTC()
	}

	var out CaseView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		programID, err := s.resolveEnrollment(ctx, tx, rc, in)
		if err != nil {
			return err
		}
		if err := s.checkProvider(ctx, tx, rc, in.ProviderOrganizationID); err != nil {
			return err
		}
		if err := s.checkServiceRequest(ctx, tx, rc, in); err != nil {
			return err
		}
		record, err := s.repo.CreateCase(ctx, tx, rc.TenantID, NewCaseRow{
			PersonID: in.PersonID, ProgramID: programID, EnrollmentID: in.EnrollmentID,
			CaseType: in.CaseType, ProviderOrganizationID: in.ProviderOrganizationID,
			OpenedAt: openedAt, ServiceRequestID: in.ServiceRequestID,
			ActorID: actorPtr(rc.Principal.ActorID),
		})
		if err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "health_case.create", domain.AggregateCase, record.ID,
			// Ids, codes and booleans only. audit.SanitizeDetail would drop anything else
			// silently, so nothing here is named in a way that would make it disappear.
			map[string]any{
				"case_type":    record.CaseType,
				"status":       record.Status,
				"from_request": record.ServiceRequestID != nil,
				"has_provider": record.ProviderOrganizationID != nil,
				"person":       record.PersonID,
				"enrollment":   record.EnrollmentID,
				"program":      record.ProgramID,
			}); err != nil {
			return err
		}
		// A case that has just been created carries no encounter and no diagnosis, so this
		// view is the same in both projections; it goes through viewCase all the same, so
		// creating one is not a way round the access event a clinical read owes.
		out, err = s.viewCase(ctx, tx, rc, record, AccessRequest{}, audit.AccessView, commandRead)
		return err
	})
	if err != nil {
		return CaseView{}, err
	}
	return out, nil
}

// resolveEnrollment checks the enrollment belongs to the person and returns the program it
// implies, refusing a caller that named a different one.
func (s *Service) resolveEnrollment(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	in NewCaseInput,
) (uuid.UUID, error) {
	enrollment, err := s.repo.GetEnrollment(ctx, tx, rc.TenantID, in.EnrollmentID)
	if err != nil {
		return uuid.Nil, err
	}
	if enrollment.PersonID != in.PersonID {
		return uuid.Nil, ErrEnrollmentMismatch
	}
	if in.ProgramID != nil && *in.ProgramID != enrollment.ProgramID {
		return uuid.Nil, ErrEnrollmentMismatch
	}
	return enrollment.ProgramID, nil
}

// checkProvider keeps a case inside the caller's provider boundary. A provider-scoped actor
// naming somebody else's organization is refused rather than quietly writing a row it would
// then not be able to read.
func (s *Service) checkProvider(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	orgID *uuid.UUID,
) error {
	scope := scopeOf(rc)
	if orgID == nil {
		if scope.Restricted() {
			return ErrProviderScope
		}
		return nil
	}
	ok, err := s.repo.ProviderOrganizationExists(ctx, tx, rc.TenantID, *orgID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrProviderUnknown
	}
	if !scope.Restricted() {
		return nil
	}
	for _, id := range scope.OrganizationIDs {
		if id == *orgID {
			return nil
		}
	}
	return ErrProviderScope
}

// checkServiceRequest is section 2.5: a case may be opened from a DIRECT_SERVICE or a
// PREAUTHORIZATION on a HEALTH-domain service, and from nothing else. A case opened from an
// accommodation booking is not a health case, and a case opened against another person's
// request is not this person's case.
func (s *Service) checkServiceRequest(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	in NewCaseInput,
) error {
	if in.ServiceRequestID == nil {
		return nil
	}
	request, err := s.repo.GetServiceRequest(ctx, tx, rc.TenantID, *in.ServiceRequestID)
	if err != nil {
		return err
	}
	if request.PersonID != in.PersonID ||
		!domain.Contains(domain.CaseRequestTypes, request.RequestType) ||
		!request.HasHealthService {
		return ErrRequestNotEligible
	}
	return nil
}

// CloseCase closes a case. Two things stop it: an encounter nobody ended, and an inpatient
// stay still running. Both are refusals rather than something the close fixes on the way
// past — a case closed over an open encounter is a case nobody can bill.
func (s *Service) CloseCase(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	reasonText *string, expected int64, req AccessRequest,
) (CaseView, error) {
	if err := domain.ValidateCloseReason(reasonText); err != nil {
		return CaseView{}, err
	}
	reason := trimmedPtr(reasonText)
	closedAt := s.now().UTC()

	var out CaseView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.validateAccess(ctx, tx, req); err != nil {
			return err
		}
		record, err := s.repo.LockCase(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		if record.Status == domain.StatusClosed {
			return ErrCaseClosed
		}
		open, err := s.repo.CountOpenEncounters(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		if open > 0 {
			return ErrEncounterOpen
		}
		stays, err := s.stays.OpenStays(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		if stays > 0 {
			return ErrStayOpen
		}
		if err := s.repo.CloseCase(ctx, tx, rc.TenantID, id, closedAt,
			actorPtr(rc.Principal.ActorID), expected); err != nil {
			return err
		}
		detail := map[string]any{"person": record.PersonID, "case_type": record.CaseType}
		if reason != nil {
			detail["reason"] = *reason
		}
		if err := s.record(ctx, tx, rc, "health_case.close", domain.AggregateCase, id, detail); err != nil {
			return err
		}
		reloaded, err := s.repo.GetCase(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		out, err = s.viewCase(ctx, tx, rc, reloaded, req, audit.AccessView, commandRead)
		return err
	})
	if err != nil {
		return CaseView{}, err
	}
	return out, nil
}
