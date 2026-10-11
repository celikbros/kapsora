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

// NewEncounterInput is the create command of an encounter.
type NewEncounterInput struct {
	CaseID         uuid.UUID
	EncounterType  string
	StartedAt      time.Time
	EndedAt        *time.Time
	LocationID     *uuid.UUID
	PractitionerID *uuid.UUID
	BranchCode     *string
	NotesClinical  *string
}

// CreateEncounter records one contact inside a case.
//
// It needs health.clinical.read as well as health.case.manage, and the transport enforces
// the second half. The reason is in the payload: branchCode and notesClinical are clinical
// fields, and a caller who may not read clinical detail has no business writing it — it
// could not read back what it wrote, and the answer this command returns would have to be
// the financial projection of something the caller had just typed.
func (s *Service) CreateEncounter(ctx context.Context, rc identity.RequestContext,
	in NewEncounterInput,
) (EncounterView, error) {
	if err := domain.ValidateNewEncounter(domain.NewEncounter{
		EncounterType: in.EncounterType, StartedAt: in.StartedAt, EndedAt: in.EndedAt,
		BranchCode: in.BranchCode, NotesClinical: in.NotesClinical,
	}); err != nil {
		return EncounterView{}, err
	}

	var out EncounterView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		// The case is read through the caller's scope first, so an encounter can never be
		// hung on a case the caller could not have seen.
		record, err := s.repo.LockCase(ctx, tx, rc.TenantID, in.CaseID, scopeOf(rc))
		if err != nil {
			return err
		}
		if record.Status == domain.StatusClosed {
			return ErrCaseClosed
		}
		encounter, err := s.repo.CreateEncounter(ctx, tx, rc.TenantID, NewEncounterRow{
			CaseID: in.CaseID, EncounterType: in.EncounterType, StartedAt: in.StartedAt.UTC(),
			EndedAt: utcPtr(in.EndedAt), LocationID: in.LocationID,
			PractitionerID: in.PractitionerID, BranchCode: trimmedPtr(in.BranchCode),
			NotesClinical: trimmedPtr(in.NotesClinical),
			ActorID:       actorPtr(rc.Principal.ActorID),
		})
		if err != nil {
			return err
		}
		// The detail says that clinical notes exist, never what they say: "has notes" is a
		// fact about the record, and the text is the thing this package exists to keep.
		if err := s.record(ctx, tx, rc, "health_encounter.create", domain.AggregateEncounter,
			encounter.ID, map[string]any{
				"case":             record.ID,
				"encounter_type":   encounter.EncounterType,
				"has_notes":        encounter.NotesClinical != nil,
				"has_branch":       encounter.BranchCode != nil,
				"has_ended":        encounter.EndedAt != nil,
				"has_location":     encounter.LocationID != nil,
				"has_practitioner": encounter.PractitionerID != nil,
			}); err != nil {
			return err
		}
		out, err = s.viewEncounter(ctx, tx, rc, record, encounter, AccessRequest{}, audit.AccessView, commandRead)
		return err
	})
	if err != nil {
		return EncounterView{}, err
	}
	return out, nil
}

// GetEncounter returns one encounter in the projection the caller has earned.
func (s *Service) GetEncounter(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	req AccessRequest,
) (EncounterView, error) {
	var (
		out    EncounterView
		person uuid.UUID
	)
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.validateAccess(ctx, tx, req); err != nil {
			return err
		}
		encounter, err := s.repo.GetEncounter(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		record, err := s.repo.GetCase(ctx, tx, rc.TenantID, encounter.CaseID, scopeOf(rc))
		if err != nil {
			return err
		}
		person = record.PersonID
		out, err = s.viewEncounter(ctx, tx, rc, record, encounter, req, audit.AccessView, singleRead)
		return err
	})
	if errors.Is(err, ErrAccessPurposeRequired) {
		s.recordDenial(ctx, rc, person, domain.AggregateEncounter, id, req)
		return EncounterView{}, err
	}
	if err != nil {
		return EncounterView{}, err
	}
	return out, nil
}

// viewEncounter is viewCase for one encounter: the same decision, the same projection and
// the same access event, taken from the case the encounter belongs to. An encounter's
// sensitivity is its case's — a psychiatric diagnosis on one contact protects the episode,
// not that one contact.
func (s *Service) viewEncounter(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record CaseRecord, encounter EncounterRecord, req AccessRequest,
	accessType audit.AccessType, mode readMode,
) (EncounterView, error) {
	d, err := decide(rc, record.Sensitivity, req)
	if err != nil && mode.demandPurpose {
		return EncounterView{}, err
	}
	view := EncounterView{
		Projection: d.projection,
		Encounter:  projectEncounter(encounter, d.projection),
	}
	switch {
	case d.projection == ProjectionClinical && mode.recordSuccess:
		if err := s.recordAccess(ctx, tx, rc, record.PersonID, domain.AggregateEncounter,
			encounter.ID, accessType, req, audit.OutcomeSuccess); err != nil {
			return EncounterView{}, err
		}
	case d.refusedSensitive && mode.recordRefusal:
		if err := s.recordAccess(ctx, tx, rc, record.PersonID, domain.AggregateEncounter,
			encounter.ID, accessType, req, audit.OutcomeDenied); err != nil {
			return EncounterView{}, err
		}
	}
	return view, nil
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	utc := t.UTC()
	return &utc
}

// EndEncounter shares the parent lock with case closure and diagnosis writes. The
// second scoped read observes the version after acquiring that lock, so concurrent
// endings cannot overwrite each other or race case closure.
func (s *Service) EndEncounter(ctx context.Context, rc identity.RequestContext, id uuid.UUID, endedAt time.Time, expected int64) (EncounterView, error) {
	var out EncounterView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		encounter, err := s.repo.GetEncounter(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		record, err := s.repo.LockCase(ctx, tx, rc.TenantID, encounter.CaseID, scopeOf(rc))
		if err != nil {
			return err
		}
		if record.Status == domain.StatusClosed {
			return ErrCaseClosed
		}
		encounter, err = s.repo.GetEncounter(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		if encounter.EndedAt != nil {
			return ErrEncounterEnded
		}
		if encounter.RowVersion != expected {
			return ErrVersionMismatch
		}
		if endedAt.IsZero() {
			return fieldError("endedAt", "REQUIRED", "bitiş zamanı zorunlu")
		}
		if err := domain.ValidateNewEncounter(domain.NewEncounter{EncounterType: encounter.EncounterType, StartedAt: encounter.StartedAt, EndedAt: &endedAt}); err != nil {
			return err
		}
		if err := s.repo.EndEncounter(ctx, tx, rc.TenantID, id, endedAt.UTC(), actorPtr(rc.Principal.ActorID), expected); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "health_encounter.end", domain.AggregateEncounter, id, map[string]any{"case": record.ID, "person": record.PersonID}); err != nil {
			return err
		}
		encounter, err = s.repo.GetEncounter(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		out, err = s.viewEncounter(ctx, tx, rc, record, encounter, AccessRequest{}, audit.AccessView, commandRead)
		return err
	})
	return out, err
}
