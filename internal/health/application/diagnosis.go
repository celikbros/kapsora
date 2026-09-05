package application

import (
	"context"
	"errors"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/health/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// ListDiagnoses returns the encounter's diagnoses, primary first.
//
// There is no financial projection of a diagnosis, and that is the point of section 2.2: a
// caller that may read cases but not clinical detail gets ErrClinicalReadRequired rather
// than an empty list, because an empty list is itself an answer about the patient — it says
// the encounter has no diagnosis. The same refusal covers a sensitive case the caller may
// not read, for the same reason in the other direction: a refusal that named sensitivity
// would say the case carries a protected category.
func (s *Service) ListDiagnoses(ctx context.Context, rc identity.RequestContext,
	encounterID uuid.UUID, req AccessRequest,
) ([]DiagnosisRecord, error) {
	var (
		out    []DiagnosisRecord
		person uuid.UUID
	)
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.validateAccess(ctx, tx, req); err != nil {
			return err
		}
		encounter, err := s.repo.GetEncounter(ctx, tx, rc.TenantID, encounterID, scopeOf(rc))
		if err != nil {
			return err
		}
		record, err := s.repo.GetCase(ctx, tx, rc.TenantID, encounter.CaseID, scopeOf(rc))
		if err != nil {
			return err
		}
		person = record.PersonID
		d, decideErr := decide(rc, record.Sensitivity, req)
		if decideErr != nil {
			return decideErr
		}
		if d.projection != ProjectionClinical {
			return ErrClinicalReadRequired
		}
		rows, err := s.repo.ListDiagnoses(ctx, tx, rc.TenantID, encounterID)
		if err != nil {
			return err
		}
		out = rows
		return s.recordAccess(ctx, tx, rc, record.PersonID, domain.AggregateEncounter,
			encounterID, audit.AccessView, req, audit.OutcomeSuccess)
	})
	switch {
	case errors.Is(err, ErrAccessPurposeRequired), errors.Is(err, ErrClinicalReadRequired):
		s.recordDenial(ctx, rc, person, domain.AggregateEncounter, encounterID, audit.AccessView, req)
		return nil, err
	case err != nil:
		return nil, err
	}
	return out, nil
}

// PutDiagnoses replaces the encounter's diagnosis set as a whole and maintains the case's
// sensitivity from what was actually stored.
//
// Nothing here takes a `sensitive` from the caller. Every code is resolved against the
// catalogue, its own category decides, and the case is then asked — over every encounter it
// holds, not just this one — whether any sensitive diagnosis remains. Removing the last one
// returns the case to STANDARD: sensitivity is a statement about the diagnoses the case
// carries now, and a correction that removed a wrong code should not leave a member's file
// flagged for ever. The look that happened while it was flagged is on the access log either
// way, which is where that history belongs.
func (s *Service) PutDiagnoses(ctx context.Context, rc identity.RequestContext,
	encounterID uuid.UUID, items []domain.DiagnosisInput,
) ([]DiagnosisRecord, error) {
	if err := domain.ValidateDiagnoses(items); err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(items))
	for i, item := range items {
		id, err := uuid.Parse(item.CodeValueID)
		if err != nil {
			return nil, fieldError(pathOf(i)+".codeValueId", "FORMAT", "geçerli bir kimlik olmalı")
		}
		ids = append(ids, id)
	}
	recordedAt := s.now().UTC()

	var out []DiagnosisRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		encounter, err := s.repo.GetEncounter(ctx, tx, rc.TenantID, encounterID, scopeOf(rc))
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
		codes, err := s.repo.ResolveCodeValues(ctx, tx, rc.TenantID, ids)
		if err != nil {
			return err
		}
		byID := make(map[uuid.UUID]CodeValueRecord, len(codes))
		for _, code := range codes {
			byID[code.ID] = code
		}
		rows := make([]NewDiagnosisRow, 0, len(items))
		ve := &domain.ValidationError{}
		for i, id := range ids {
			code, ok := byID[id]
			switch {
			case !ok:
				ve.Add(pathOf(i)+".codeValueId", "NOT_FOUND", "bu tanı kodu katalogda yok")
				continue
			case !code.Active:
				ve.Add(pathOf(i)+".codeValueId", "INACTIVE", "bu tanı kodu artık kullanılmıyor")
				continue
			}
			rows = append(rows, NewDiagnosisRow{
				EncounterID: encounterID, CodeSystemID: code.CodeSystemID, CodeValueID: id,
				DiagnosisType: items[i].DiagnosisType, Sensitive: code.Sensitive,
				RecordedAt: recordedAt, ActorID: actorPtr(rc.Principal.ActorID),
			})
		}
		if err := ve.OrNil(); err != nil {
			return err
		}
		if err := s.repo.ReplaceDiagnoses(ctx, tx, rc.TenantID, encounterID, rows); err != nil {
			return err
		}
		sensitive, err := s.repo.CaseHasSensitiveDiagnosis(ctx, tx, rc.TenantID, record.ID)
		if err != nil {
			return err
		}
		sensitivity := domain.SensitivityStandard
		if sensitive {
			sensitivity = domain.SensitivitySensitive
		}
		if err := s.repo.SetCaseSensitivity(ctx, tx, rc.TenantID, record.ID, sensitivity,
			actorPtr(rc.Principal.ActorID)); err != nil {
			return err
		}
		// The detail carries a count and the sensitivity the case ended up with, never a
		// diagnosis code: an audit row a support engineer can read is not a place to put
		// the one fact this package exists to keep.
		if err := s.record(ctx, tx, rc, "health_diagnosis.put", domain.AggregateEncounter,
			encounterID, map[string]any{
				"case":            record.ID,
				"person":          record.PersonID,
				"diagnosis_count": len(rows),
				"sensitivity":     sensitivity,
			}); err != nil {
			return err
		}
		stored, err := s.repo.ListDiagnoses(ctx, tx, rc.TenantID, encounterID)
		if err != nil {
			return err
		}
		out = stored
		// No access event: this is a write, and the access log answers who *looked* at a
		// member's clinical data. The write is on the business audit row above, with the
		// count and the sensitivity it produced. A row here would put the author of a
		// diagnosis on the member's list of people who read it.
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListAccessLog answers who looked at a person's clinical data and why. It reads
// audit.access_event rather than anything this module keeps for itself: a second copy of
// the access history would be a second history to disagree with.
func (s *Service) ListAccessLog(ctx context.Context, rc identity.RequestContext,
	personID *uuid.UUID, cursor string, limit int,
) (AccessLogPage, error) {
	after, pageSize, err := s.paging(cursor, limit)
	if err != nil {
		return AccessLogPage{}, err
	}
	var out AccessLogPage
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := s.repo.ListAccessEvents(ctx, tx, rc.TenantID, AccessEventQuery{
			PersonID: personID, After: after, PageSize: pageSize + 1,
		})
		if err != nil {
			return err
		}
		if len(rows) > pageSize {
			out.NextCursor = s.cursors.Encode(accessEventCursor(rows[pageSize-1]))
			rows = rows[:pageSize]
		}
		out.Items = rows
		return nil
	})
	if err != nil {
		return AccessLogPage{}, err
	}
	return out, nil
}

// pathOf is the JSON path of one diagnosis line, so a field error names the line the caller
// actually sent.
func pathOf(i int) string { return "items[" + strconv.Itoa(i) + "]" }
