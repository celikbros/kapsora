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
	"github.com/celikbros/kapsora/internal/platform/outbox"
)

// EnrollmentCreatedEvent is the outbox event type WP-I2-03 consumes to open the
// entitlement accounts of a new enrollment. The payload carries identifiers only.
const EnrollmentCreatedEvent = "benefit.enrollment.created"

// Enrollment is the view returned by the enrollment endpoints.
type Enrollment struct {
	ID                  uuid.UUID
	PersonID            uuid.UUID
	SponsorMembershipID uuid.UUID
	PlanID              uuid.UUID
	ProgramID           uuid.UUID
	PlanCode            string
	Status              string
	ValidFrom           time.Time
	ValidTo             *time.Time
	EnrollmentReason    *string
	SourceSystem        *string
	RowVersion          int64
}

// EnrollmentPage is one keyset page of enrollments.
type EnrollmentPage struct {
	Items      []Enrollment
	NextCursor string
}

// NewEnrollmentInput is the create command.
type NewEnrollmentInput struct {
	SponsorMembershipID uuid.UUID
	PlanID              uuid.UUID
	Status              string
	ValidFrom           time.Time
	ValidTo             *time.Time
	EnrollmentReason    *string
}

// EnrollmentPatch is a merge-patch of an existing enrollment.
type EnrollmentPatch struct {
	Status          *string
	ValidTo         *time.Time
	ClearValidTo    bool
	ExpectedVersion int64
}

// EnrollmentFilter is the API-level list request.
type EnrollmentFilter struct {
	PlanID uuid.UUID
	Status string
	Cursor string
	Limit  int
}

// CreateEnrollment binds a sponsor membership of the person to a plan for a period. The
// membership must be the person's and active on the start date, and the plan must have a
// PUBLISHED version covering it. The outbox event is written in the same transaction.
func (s *Service) CreateEnrollment(ctx context.Context, rc identity.RequestContext, personID uuid.UUID, in NewEnrollmentInput) (Enrollment, error) {
	if in.Status == "" {
		in.Status = domain.EnrollmentActive
	}
	in.ValidFrom = domain.DateOnly(in.ValidFrom)
	in.ValidTo = datePtr(in.ValidTo)

	ve := &domain.ValidationError{}
	if !domain.Contains(domain.EnrollmentCreate, in.Status) {
		ve.Add("status", "ENUM", "PENDING veya ACTIVE olmalı")
	}
	if in.ValidFrom.IsZero() {
		ve.Add("validFrom", "REQUIRED", "başlangıç tarihi zorunlu")
	}
	if in.EnrollmentReason != nil {
		domain.ValidateText(ve, "enrollmentReason", *in.EnrollmentReason, domain.MaxReasonLength)
	}
	if in.ValidTo != nil && !in.ValidTo.After(in.ValidFrom) {
		ve.Add("validTo", "RANGE", "bitiş tarihi başlangıçtan sonra olmalı")
	}
	if err := ve.OrNil(); err != nil {
		return Enrollment{}, err
	}

	var out Enrollment
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if err := s.requirePerson(ctx, tx, rc.TenantID, personID); err != nil {
			return err
		}
		membership, err := s.repo.GetMembership(ctx, tx, rc.TenantID, in.SponsorMembershipID)
		switch {
		case errors.Is(err, ErrNotFound):
			ve.Add("sponsorMembershipId", "UNKNOWN", "üyelik bulunamadı")
		case err != nil:
			return err
		case membership.PersonID != personID:
			ve.Add("sponsorMembershipId", "UNKNOWN", "üyelik bu kişiye ait değil")
		case membership.Status != domain.EnrollmentActive:
			ve.Add("sponsorMembershipId", "MEMBERSHIP_NOT_ACTIVE", "üyelik etkin değil")
		case !domain.CoversDate(membership.ValidFrom, membership.ValidTo, in.ValidFrom):
			ve.Add("sponsorMembershipId", "MEMBERSHIP_NOT_ACTIVE", "üyelik bu tarihte geçerli değil")
		}

		plan, err := s.repo.GetPlan(ctx, tx, rc.TenantID, in.PlanID)
		switch {
		case errors.Is(err, ErrPlanNotFound), errors.Is(err, ErrNotFound):
			ve.Add("planId", "UNKNOWN", "plan bulunamadı")
		case err != nil:
			return err
		case plan.Status != domain.PlanActive:
			ve.Add("planId", "PLAN_NOT_PUBLISHED", "plan etkin değil")
		default:
			if _, err := ResolvePlanVersion(ctx, tx, rc.TenantID, in.PlanID, in.ValidFrom); err != nil {
				if !errors.Is(err, ErrNoPublishedVersion) {
					return err
				}
				ve.Add("planId", "PLAN_NOT_PUBLISHED", "bu tarihte yayınlanmış plan sürümü yok")
			}
		}
		if ve.Len() > 0 {
			return ve
		}

		enrollmentID, err := s.repo.CreateEnrollment(ctx, tx, NewEnrollmentRow{
			TenantID: rc.TenantID, SponsorMembershipID: in.SponsorMembershipID, PlanID: in.PlanID,
			Status: in.Status, ValidFrom: in.ValidFrom, ValidTo: in.ValidTo,
			EnrollmentReason: optString(strings.TrimSpace(deref(in.EnrollmentReason))),
		})
		if err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "enrollment.create", "enrollment", enrollmentID, map[string]any{
			"person_id": personID, "plan_id": in.PlanID, "sponsor_membership_id": in.SponsorMembershipID,
			"enrollment_status": in.Status,
		}); err != nil {
			return err
		}
		if _, _, err := outbox.Publish(ctx, tx, outbox.Event{
			TenantID:      nullUUID(rc.TenantID),
			AggregateType: "benefit.enrollment",
			AggregateID:   enrollmentID,
			Type:          EnrollmentCreatedEvent,
			Payload: map[string]any{
				"enrollmentId":        enrollmentID,
				"personId":            personID,
				"sponsorMembershipId": in.SponsorMembershipID,
				"planId":              in.PlanID,
				"validFrom":           in.ValidFrom.Format(time.DateOnly),
			},
			DeduplicationKey: enrollmentID.String(),
		}); err != nil {
			return err
		}
		row, err := s.repo.GetEnrollment(ctx, tx, rc.TenantID, enrollmentID)
		if err != nil {
			return err
		}
		out = enrollmentView(row)
		return nil
	})
	return out, err
}

// GetEnrollment returns one enrollment of the caller's tenant.
func (s *Service) GetEnrollment(ctx context.Context, rc identity.RequestContext, enrollmentID uuid.UUID) (Enrollment, error) {
	var out Enrollment
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		row, err := s.repo.GetEnrollment(ctx, tx, rc.TenantID, enrollmentID)
		if err != nil {
			return err
		}
		out = enrollmentView(row)
		return nil
	})
	return out, err
}

// ListPersonEnrollments returns the enrollments of one person, newest first.
func (s *Service) ListPersonEnrollments(ctx context.Context, rc identity.RequestContext, personID uuid.UUID) ([]Enrollment, error) {
	var out []Enrollment
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		if err := s.requirePerson(ctx, tx, rc.TenantID, personID); err != nil {
			return err
		}
		rows, err := s.repo.ListPersonEnrollments(ctx, tx, rc.TenantID, personID)
		if err != nil {
			return err
		}
		out = make([]Enrollment, 0, len(rows))
		for _, r := range rows {
			out = append(out, enrollmentView(r))
		}
		return nil
	})
	return out, err
}

// ListEnrollments returns one keyset page filtered by plan and status.
func (s *Service) ListEnrollments(ctx context.Context, rc identity.RequestContext, f EnrollmentFilter) (EnrollmentPage, error) {
	ve := &domain.ValidationError{}
	if f.Status != "" && !domain.Contains(domain.EnrollmentStatuses, f.Status) {
		ve.Add("status", "ENUM", "geçersiz durum")
	}
	if err := ve.OrNil(); err != nil {
		return EnrollmentPage{}, err
	}
	cursor, hasCursor, err := s.cursors.Decode(f.Cursor)
	if err != nil {
		return EnrollmentPage{}, err
	}
	pageSize := httpx.ClampLimit(f.Limit)
	q := EnrollmentListQuery{PlanID: f.PlanID, Status: f.Status, PageSize: pageSize + 1}
	if hasCursor {
		q.After = &cursor
	}

	var rows []EnrollmentRow
	err = db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rows, err = s.repo.ListEnrollments(ctx, tx, rc.TenantID, q)
		return err
	})
	if err != nil {
		return EnrollmentPage{}, err
	}
	page := EnrollmentPage{Items: make([]Enrollment, 0, len(rows))}
	if len(rows) > pageSize {
		last := rows[pageSize-1]
		page.NextCursor = s.cursors.Encode(httpx.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
		rows = rows[:pageSize]
	}
	for _, r := range rows {
		page.Items = append(page.Items, enrollmentView(r))
	}
	return page, nil
}

// UpdateEnrollment applies a merge-patch of status and validity end.
func (s *Service) UpdateEnrollment(ctx context.Context, rc identity.RequestContext, enrollmentID uuid.UUID, patch EnrollmentPatch) (Enrollment, error) {
	ve := &domain.ValidationError{}
	if patch.Status != nil && !domain.Contains(domain.EnrollmentUpdates, *patch.Status) {
		ve.Add("status", "ENUM", "ACTIVE, SUSPENDED veya ENDED olmalı")
	}
	if err := ve.OrNil(); err != nil {
		return Enrollment{}, err
	}

	var out Enrollment
	err := db.WithTenantTx(ctx, s.pool, tenantCtx(rc), func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.GetEnrollment(ctx, tx, rc.TenantID, enrollmentID)
		if err != nil {
			return err
		}
		if current.RowVersion != patch.ExpectedVersion {
			return ErrVersionMismatch
		}
		next := EnrollmentUpdateRow{Status: current.Status, ValidTo: current.ValidTo, Expected: patch.ExpectedVersion}
		if patch.Status != nil {
			if !domain.EnrollmentTransitionAllowed(current.Status, *patch.Status) {
				return ErrEnrollmentTransition
			}
			next.Status = *patch.Status
		}
		switch {
		case patch.ClearValidTo:
			next.ValidTo = nil
		case patch.ValidTo != nil:
			next.ValidTo = datePtr(patch.ValidTo)
		}
		if next.ValidTo != nil && !next.ValidTo.After(current.ValidFrom) {
			ve.Add("validTo", "RANGE", "bitiş tarihi başlangıçtan sonra olmalı")
			return ve
		}
		if err := s.repo.UpdateEnrollment(ctx, tx, rc.TenantID, enrollmentID, next); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "enrollment.update", "enrollment", enrollmentID, map[string]any{
			"plan_id": current.PlanID, "enrollment_status": next.Status,
		}); err != nil {
			return err
		}
		row, err := s.repo.GetEnrollment(ctx, tx, rc.TenantID, enrollmentID)
		if err != nil {
			return err
		}
		out = enrollmentView(row)
		return nil
	})
	return out, err
}

func enrollmentView(r EnrollmentRow) Enrollment {
	return Enrollment{
		ID: r.ID, PersonID: r.PersonID, SponsorMembershipID: r.SponsorMembershipID,
		PlanID: r.PlanID, ProgramID: r.ProgramID, PlanCode: r.PlanCode, Status: r.Status,
		ValidFrom: r.ValidFrom, ValidTo: r.ValidTo, EnrollmentReason: r.EnrollmentReason,
		SourceSystem: r.SourceSystem, RowVersion: r.RowVersion,
	}
}
