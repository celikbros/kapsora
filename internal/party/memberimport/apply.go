package memberimport

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/party/domain"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/outbox"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// CodeApplyFailed marks a row the apply step could not write because the live tables
// refused it (a competing identifier, an overlapping period). The row goes back to the
// review queue instead of failing the whole batch.
const CodeApplyFailed = "APPLY_FAILED"

// membershipActive is the status the import gives a new membership or enrollment.
const membershipActive = "ACTIVE"

// RunApply writes an accepted batch to the live tables. Rows are applied in transactional
// chunks of ChunkSize in row_no order, principal rows before dependants, so a dependant
// always finds the membership it hangs from. Every write is idempotent, so a crashed run
// is resumed by simply running again and an already APPLIED batch is a no-op.
func (s *Service) RunApply(ctx context.Context, tenantID, batchID uuid.UUID) error {
	batch, run, err := s.beginApply(ctx, tenantID, batchID)
	if err != nil || !run {
		return err
	}
	tc := batch.tenantCtx()

	var cat catalogs
	if err := db.WithTenantTx(ctx, s.pool, tc, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		cat, err = loadCatalogs(ctx, tx, tenantID)
		return err
	}); err != nil {
		return err
	}

	for _, role := range []string{RolePrincipal, RoleDependant} {
		if err := s.applyPass(ctx, batch, cat, role); err != nil {
			return err
		}
	}

	return db.WithTenantTx(ctx, s.pool, tc, func(ctx context.Context, tx pgx.Tx) error {
		counters, err := readCounters(ctx, tx, tenantID, batchID)
		if err != nil {
			return err
		}
		if err := writeCounters(ctx, tx, tenantID, batchID, counters); err != nil {
			return err
		}
		summary := reconciliationSummary(int(batch.RowCount), counters)
		if _, err := sqlcgen.New(tx).SetImportBatchStatus(ctx, sqlcgen.SetImportBatchStatusParams{
			Status: StatusApplied, ErrorSummary: &summary, TenantID: tenantID, ID: batchID,
			FromStatuses: []string{StatusApplying},
		}); err != nil {
			return fmt.Errorf("memberimport: finish apply: %w", err)
		}
		return nil
	})
}

// beginApply loads the batch and reports whether the apply job has work to do.
func (s *Service) beginApply(ctx context.Context, tenantID, batchID uuid.UUID) (batchRef, bool, error) {
	var out batchRef
	var run bool
	err := db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenantID}, func(ctx context.Context, tx pgx.Tx) error {
		row, err := loadBatch(ctx, tx, tenantID, batchID)
		if err != nil {
			return err
		}
		out = batchRef{TenantID: tenantID, GetImportBatchRow: row}
		run = row.Status == StatusApplying
		if !run && row.Status != StatusApplied {
			return ErrStateInvalid
		}
		return nil
	})
	return out, run && err == nil, err
}

// applyPass walks the batch once for one membership role.
func (s *Service) applyPass(ctx context.Context, batch batchRef, cat catalogs, role string) error {
	tc := batch.tenantCtx()
	after := int32(0)
	for {
		next := int32(-1)
		err := db.WithTenantTx(ctx, s.pool, tc, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := sqlcgen.New(tx).ListImportRowsForProcessing(ctx, sqlcgen.ListImportRowsForProcessingParams{
				TenantID: batch.TenantID, BatchID: batch.ID, AfterRowNo: after,
				Statuses: []string{RowValid, RowMatched},
				PageSize: int32(s.chunkSize), //nolint:gosec // configured constant
			})
			if err != nil {
				return fmt.Errorf("memberimport: read rows to apply: %w", err)
			}
			for _, r := range rows {
				row := stagedRow(sqlcgen.ListImportRowsRow(r))
				payload, err := decodePayload(row.Payload)
				if err != nil {
					return fmt.Errorf("memberimport: decode payload: %w", err)
				}
				if payload.Role != role {
					continue
				}
				if err := s.applyRowIsolated(ctx, tx, batch, cat, row, payload); err != nil {
					return err
				}
			}
			if len(rows) == s.chunkSize {
				next = rows[len(rows)-1].RowNo
			}
			return nil
		})
		if err != nil {
			return err
		}
		if next < 0 {
			return nil
		}
		after = next
	}
}

// applyRowIsolated runs one row inside a savepoint: a row the live tables reject is
// parked as INVALID with the constraint that refused it, and the rest of the chunk still
// commits.
func (s *Service) applyRowIsolated(ctx context.Context, tx pgx.Tx, batch batchRef, cat catalogs,
	row stagedRow, payload rowPayload,
) error {
	sub, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("memberimport: row savepoint: %w", err)
	}
	applyErr := s.applyRow(ctx, sub, batch, cat, row, payload)
	if applyErr == nil {
		if err := sub.Commit(ctx); err != nil {
			return fmt.Errorf("memberimport: commit row: %w", err)
		}
		return nil
	}
	if err := sub.Rollback(ctx); err != nil {
		return fmt.Errorf("memberimport: rollback row: %w", err)
	}
	constraint, ok := integrityViolation(applyErr)
	if !ok {
		return applyErr
	}
	s.logger.Warn("member import row rejected by the database",
		"import_batch_id", batch.ID, "row_no", row.RowNo, "constraint", constraint)
	return s.parkRow(ctx, tx, batch, row, payload, constraint)
}

// parkRow marks a rejected row INVALID with a stable code; the message names the
// constraint, never the data that violated it.
func (s *Service) parkRow(ctx context.Context, tx pgx.Tx, batch batchRef, row stagedRow,
	payload rowPayload, constraint string,
) error {
	rowErrors, err := decodeErrors(row.Errors)
	if err != nil {
		return fmt.Errorf("memberimport: decode errors: %w", err)
	}
	rowErrors = append(rowErrors, RowError{
		Field: ColSourceRecordID, Code: CodeApplyFailed,
		Message: "kayıt yazılamadı: " + constraint,
	})
	payloadJSON, err := encodeJSON(payload)
	if err != nil {
		return fmt.Errorf("memberimport: encode payload: %w", err)
	}
	errorsJSON, err := encodeJSON(rowErrors)
	if err != nil {
		return fmt.Errorf("memberimport: encode errors: %w", err)
	}
	if _, err := sqlcgen.New(tx).UpdateImportRowOutcome(ctx, sqlcgen.UpdateImportRowOutcomeParams{
		Status: RowInvalid, Payload: payloadJSON, Errors: errorsJSON,
		MatchedPersonID: row.MatchedPersonID, Decision: row.Decision,
		TenantID: batch.TenantID, ID: row.ID,
	}); err != nil {
		return fmt.Errorf("memberimport: park row: %w", err)
	}
	return nil
}

// applyRow writes one staged row: person, identifiers, membership, relationship and
// enrollment. Every step looks for what already exists first, which is what makes a
// second apply of the same file free of writes.
func (s *Service) applyRow(ctx context.Context, tx pgx.Tx, batch batchRef, cat catalogs,
	row stagedRow, payload rowPayload,
) error {
	q := sqlcgen.New(tx)
	decision := deref(row.Decision)
	if decision == DecisionSkip {
		return s.finishRow(ctx, tx, batch, row, payload, RowSkipped, uuid.Nil)
	}
	plaintext, err := s.decryptIdentifiers(ctx, batch.TenantID, row.IdentifierCipher)
	if err != nil {
		return err
	}
	source, record := batch.SourceSystem, row.SourceRecordID
	actor := batch.actor()

	var personID uuid.UUID
	if row.MatchedPersonID.Valid {
		personID = row.MatchedPersonID.UUID
		if _, err := q.UpdatePersonFromImport(ctx, sqlcgen.UpdatePersonFromImportParams{
			FirstName: payload.FirstName, MiddleName: strPtr(payload.MiddleName), LastName: payload.LastName,
			NormalizedName: domain.NormalizedName(payload.FirstName, payload.MiddleName, payload.LastName),
			BirthDate:      dateValue(payload.BirthDate), SexAtBirth: strPtr(payload.SexAtBirth),
			SourceSystem: &source, SourceRecordID: &record, ActorID: nullUUID(actor),
			TenantID: batch.TenantID, ID: personID,
		}); err != nil {
			return fmt.Errorf("memberimport: update person: %w", err)
		}
	} else {
		created, err := q.CreatePersonFromImport(ctx, sqlcgen.CreatePersonFromImportParams{
			TenantID: batch.TenantID, FirstName: payload.FirstName, MiddleName: strPtr(payload.MiddleName),
			LastName:       payload.LastName,
			NormalizedName: domain.NormalizedName(payload.FirstName, payload.MiddleName, payload.LastName),
			BirthDate:      dateValue(payload.BirthDate), SexAtBirth: strPtr(payload.SexAtBirth),
			SourceSystem: &source, SourceRecordID: &record, ActorID: nullUUID(actor),
		})
		if err != nil {
			return fmt.Errorf("memberimport: create person: %w", err)
		}
		personID = created.ID
	}

	if err := s.applyIdentifiers(ctx, tx, batch, cat, row, personID, plaintext); err != nil {
		return err
	}

	principalPersonID, principalMembershipID, err := s.resolvePrincipal(ctx, tx, batch, payload)
	if err != nil {
		return err
	}
	membershipID, err := s.applyMembership(ctx, tx, batch, row, payload, personID, principalMembershipID, plaintext)
	if err != nil {
		return err
	}
	if err := s.applyRelationship(ctx, tx, batch, cat, payload, personID, principalPersonID); err != nil {
		return err
	}
	if err := s.applyEnrollment(ctx, tx, batch, row, payload, personID, membershipID); err != nil {
		return err
	}
	return s.finishRow(ctx, tx, batch, row, payload, RowApplied, personID)
}

// decryptIdentifiers opens the staging envelope. The plaintext exists only for the length
// of one row's apply and is never written back anywhere but party.person_identifier.
func (s *Service) decryptIdentifiers(ctx context.Context, tenantID uuid.UUID, cipher []byte) (map[string]string, error) {
	if len(cipher) == 0 {
		return map[string]string{}, nil
	}
	plain, err := s.cipher.Decrypt(ctx, tenantID, identifierPurpose, cipher)
	if err != nil {
		return nil, fmt.Errorf("memberimport: decrypt identifiers: %w", err)
	}
	out := map[string]string{}
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, fmt.Errorf("memberimport: decode identifier envelope: %w", err)
	}
	return out, nil
}

// applyIdentifiers adds the identifiers the person does not have yet. A type the person
// already carries is left alone: an import fills gaps, it never overwrites a TCKN.
func (s *Service) applyIdentifiers(ctx context.Context, tx pgx.Tx, batch batchRef, cat catalogs,
	row stagedRow, personID uuid.UUID, plaintext map[string]string,
) error {
	ids, err := decodeIdentifiers(row.Identifiers)
	if err != nil {
		return fmt.Errorf("memberimport: decode identifiers: %w", err)
	}
	q := sqlcgen.New(tx)
	for _, id := range ids {
		value, ok := plaintext[id.Type]
		if !ok || value == "" {
			continue
		}
		existing, err := q.CountPersonIdentifiersOfType(ctx, sqlcgen.CountPersonIdentifiersOfTypeParams{
			TenantID: batch.TenantID, PersonID: personID, IdentifierType: id.Type,
		})
		if err != nil {
			return fmt.Errorf("memberimport: count identifiers: %w", err)
		}
		if existing > 0 {
			continue
		}
		hash, err := hex.DecodeString(id.Hash)
		if err != nil {
			return fmt.Errorf("memberimport: decode blind index: %w", err)
		}
		identifierID, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("memberimport: identifier id: %w", err)
		}
		scopeKey, scoped := domain.ScopeKey(cat.identifierScopes[id.Type],
			batch.SponsorTenantOrganizationID.String(), identifierID.String())
		if !scoped {
			continue
		}
		cipher, err := s.cipher.Encrypt(ctx, batch.TenantID, identifierPurpose, []byte(value))
		if err != nil {
			return fmt.Errorf("memberimport: encrypt identifier: %w", err)
		}
		if err := q.AddPersonIdentifier(ctx, sqlcgen.AddPersonIdentifierParams{
			ID: identifierID, TenantID: batch.TenantID, PersonID: personID, IdentifierType: id.Type,
			IdentifierCipher: cipher, IdentifierHash: hash,
			MaskedValue: domain.MaskIdentifier(id.Type, value), ScopeKey: scopeKey, IsPrimary: id.Primary,
		}); err != nil {
			return fmt.Errorf("memberimport: add identifier: %w", err)
		}
	}
	return nil
}

// resolvePrincipal finds the principal a dependant row hangs from: the row of the same
// file first (it was applied in the previous pass), then the live blind index.
func (s *Service) resolvePrincipal(ctx context.Context, tx pgx.Tx, batch batchRef, payload rowPayload) (person, membership uuid.UUID, err error) {
	if payload.Role != RoleDependant {
		return uuid.Nil, uuid.Nil, nil
	}
	q := sqlcgen.New(tx)
	if payload.PrincipalRecordID != "" {
		principalRow, err := q.FindImportRowBySourceRecord(ctx, sqlcgen.FindImportRowBySourceRecordParams{
			TenantID: batch.TenantID, BatchID: batch.ID, SourceRecordID: payload.PrincipalRecordID,
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return uuid.Nil, uuid.Nil, fmt.Errorf("memberimport: principal row: %w", err)
		case principalRow.AppliedPersonID.Valid:
			person = principalRow.AppliedPersonID.UUID
		case principalRow.MatchedPersonID.Valid:
			person = principalRow.MatchedPersonID.UUID
		}
	}
	if person == uuid.Nil && payload.PrincipalHash != "" {
		found, ok, err := s.findByHash(ctx, tx, batch, domain.TypeMemberNo, payload.PrincipalHash)
		if err != nil {
			return uuid.Nil, uuid.Nil, err
		}
		if ok {
			person = found
		}
	}
	if person == uuid.Nil {
		return uuid.Nil, uuid.Nil, nil
	}
	found, err := q.FindImportMembershipByPerson(ctx, sqlcgen.FindImportMembershipByPersonParams{
		TenantID: batch.TenantID, SponsorTenantOrganizationID: batch.SponsorTenantOrganizationID, PersonID: person,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return person, uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("memberimport: principal membership: %w", err)
	}
	return person, found.ID, nil
}

// applyMembership returns the sponsor membership of the row, creating it only when the
// person has none under this sponsor yet.
func (s *Service) applyMembership(ctx context.Context, tx pgx.Tx, batch batchRef, row stagedRow,
	payload rowPayload, personID, principalMembershipID uuid.UUID, plaintext map[string]string,
) (uuid.UUID, error) {
	q := sqlcgen.New(tx)
	source, record := batch.SourceSystem, row.SourceRecordID
	existing, err := q.FindImportMembershipBySource(ctx, sqlcgen.FindImportMembershipBySourceParams{
		TenantID: batch.TenantID, SponsorTenantOrganizationID: batch.SponsorTenantOrganizationID,
		SourceSystem: &source, SourceRecordID: &record,
	})
	switch {
	case err == nil:
		return existing.ID, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("memberimport: membership by source: %w", err)
	}
	byPerson, err := q.FindImportMembershipByPerson(ctx, sqlcgen.FindImportMembershipByPersonParams{
		TenantID: batch.TenantID, SponsorTenantOrganizationID: batch.SponsorTenantOrganizationID, PersonID: personID,
	})
	switch {
	case err == nil:
		return byPerson.ID, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("memberimport: membership by person: %w", err)
	}

	created, err := q.CreateImportMembership(ctx, sqlcgen.CreateImportMembershipParams{
		TenantID: batch.TenantID, PersonID: personID,
		SponsorTenantOrganizationID: batch.SponsorTenantOrganizationID,
		PrincipalMembershipID:       nullUUID(principalMembershipID),
		MembershipType:              payload.MembershipType,
		ExternalMemberNo:            strPtr(plaintext[domain.TypeMemberNo]),
		Status:                      membershipActive,
		ValidFrom:                   dateValue(payload.ValidFrom), ValidTo: dateValue(payload.ValidTo),
		SourceSystem: &source, SourceRecordID: &record,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("memberimport: create membership: %w", err)
	}
	return created, nil
}

// applyRelationship links a dependant to its principal once. Non-directional types are
// stored with the lower id as source, exactly as the manual command does, so a mirrored
// duplicate hits the same exclusion constraint.
func (s *Service) applyRelationship(ctx context.Context, tx pgx.Tx, batch batchRef, cat catalogs,
	payload rowPayload, personID, principalPersonID uuid.UUID,
) error {
	if payload.Relationship == "" || principalPersonID == uuid.Nil || principalPersonID == personID {
		return nil
	}
	directional, known := cat.relationshipDirectional[payload.Relationship]
	if !known {
		return nil
	}
	sourcePerson, targetPerson := principalPersonID, personID
	if !directional && bytes.Compare(targetPerson[:], sourcePerson[:]) < 0 {
		sourcePerson, targetPerson = targetPerson, sourcePerson
	}
	q := sqlcgen.New(tx)
	_, err := q.FindImportRelationship(ctx, sqlcgen.FindImportRelationshipParams{
		TenantID: batch.TenantID, SourcePersonID: sourcePerson, TargetPersonID: targetPerson,
		RelationshipType: payload.Relationship,
	})
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("memberimport: relationship lookup: %w", err)
	}
	if _, err := q.CreateImportRelationship(ctx, sqlcgen.CreateImportRelationshipParams{
		TenantID: batch.TenantID, SourcePersonID: sourcePerson, TargetPersonID: targetPerson,
		RelationshipType: payload.Relationship,
		ValidFrom:        dateValue(payload.ValidFrom), ValidTo: dateValue(payload.ValidTo),
	}); err != nil {
		return fmt.Errorf("memberimport: create relationship: %w", err)
	}
	return nil
}

// applyEnrollment enrolls the membership into the resolved plan and announces it on the
// outbox, so the entitlement accounts open through the same handler a manual enrollment
// uses (WP-I2-03).
func (s *Service) applyEnrollment(ctx context.Context, tx pgx.Tx, batch batchRef, row stagedRow,
	payload rowPayload, personID, membershipID uuid.UUID,
) error {
	if payload.PlanID == "" || membershipID == uuid.Nil {
		return nil
	}
	planID, err := uuid.Parse(payload.PlanID)
	if err != nil {
		return nil //nolint:nilerr // an unparsable cached plan id simply means "no plan"
	}
	q := sqlcgen.New(tx)
	if _, err := q.FindImportEnrollment(ctx, sqlcgen.FindImportEnrollmentParams{
		TenantID: batch.TenantID, SponsorMembershipID: membershipID, PlanID: planID,
	}); err == nil {
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("memberimport: enrollment lookup: %w", err)
	}
	source, record := batch.SourceSystem, row.SourceRecordID
	enrollmentID, err := q.CreateImportEnrollment(ctx, sqlcgen.CreateImportEnrollmentParams{
		TenantID: batch.TenantID, SponsorMembershipID: membershipID, PlanID: planID,
		Status:    membershipActive,
		ValidFrom: dateValue(payload.ValidFrom), ValidTo: dateValue(payload.ValidTo),
		SourceSystem: &source, SourceRecordID: &record,
	})
	if err != nil {
		return fmt.Errorf("memberimport: create enrollment: %w", err)
	}
	validFrom := payload.ValidFrom
	if validFrom == "" {
		validFrom = s.now().Format(time.DateOnly)
	}
	if _, _, err := outbox.Publish(ctx, tx, outbox.Event{
		TenantID: nullUUID(batch.TenantID), AggregateType: "benefit.enrollment", AggregateID: enrollmentID,
		Type: benefitapp.EnrollmentCreatedEvent,
		Payload: map[string]any{
			"enrollmentId": enrollmentID, "personId": personID,
			"sponsorMembershipId": membershipID, "planId": planID, "validFrom": validFrom,
		},
		DeduplicationKey: enrollmentID.String(),
	}); err != nil {
		return err
	}
	return nil
}

// finishRow records the outcome of one row, keeping the decision and the matched person
// so the counters stay derivable from the staging table alone.
func (s *Service) finishRow(ctx context.Context, tx pgx.Tx, batch batchRef, row stagedRow,
	payload rowPayload, status string, personID uuid.UUID,
) error {
	payloadJSON, err := encodeJSON(payload)
	if err != nil {
		return fmt.Errorf("memberimport: encode payload: %w", err)
	}
	errorsJSON, err := encodeJSON(mustErrors(row.Errors))
	if err != nil {
		return fmt.Errorf("memberimport: encode errors: %w", err)
	}
	if _, err := sqlcgen.New(tx).UpdateImportRowOutcome(ctx, sqlcgen.UpdateImportRowOutcomeParams{
		Status: status, Payload: payloadJSON, Errors: errorsJSON,
		MatchedPersonID: row.MatchedPersonID, Decision: row.Decision,
		AppliedPersonID: nullUUID(personID),
		TenantID:        batch.TenantID, ID: row.ID,
	}); err != nil {
		return fmt.Errorf("memberimport: finish row: %w", err)
	}
	return nil
}

func mustErrors(raw []byte) []RowError {
	out, err := decodeErrors(raw)
	if err != nil {
		return []RowError{}
	}
	if out == nil {
		return []RowError{}
	}
	return out
}

// integrityViolation reports the constraint of a class-23 error (unique, foreign key,
// exclusion, check). Anything else is an infrastructure failure and must abort the chunk.
func integrityViolation(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || !strings.HasPrefix(pgErr.Code, "23") {
		return "", false
	}
	name := pgErr.ConstraintName
	if name == "" {
		name = pgErr.Code
	}
	return name, true
}
