package memberimport

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	benefitapp "github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/party/domain"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// batchRef is a loaded batch together with the tenant it belongs to; the batch read does
// not select tenant_id because RLS already restricts it to one.
type batchRef struct {
	TenantID uuid.UUID
	sqlcgen.GetImportBatchRow
}

func (b batchRef) actor() uuid.UUID {
	if b.CreatedBy.Valid {
		return b.CreatedBy.UUID
	}
	return uuid.Nil
}

func (b batchRef) tenantCtx() db.TenantContext {
	return db.TenantContext{TenantID: b.TenantID, ActorID: b.actor()}
}

// RunValidation validates and matches every staged row of a batch and leaves the batch in
// REVIEW (something needs a human) or READY. It is idempotent: a second run recomputes
// the same verdicts, so a redelivered job or a resumed crash changes nothing.
func (s *Service) RunValidation(ctx context.Context, tenantID, batchID uuid.UUID) error {
	batch, started, err := s.beginValidation(ctx, tenantID, batchID)
	if err != nil || !started {
		return err
	}
	tc := batch.tenantCtx()

	principals, err := s.batchPrincipalIndex(ctx, tc, batchID)
	if err != nil {
		return err
	}
	plans := map[string]planLookup{}

	after := int32(0)
	for {
		next := int32(-1)
		err := db.WithTenantTx(ctx, s.pool, tc, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := sqlcgen.New(tx).ListImportRowsForProcessing(ctx, sqlcgen.ListImportRowsForProcessingParams{
				TenantID: tenantID, BatchID: batchID, AfterRowNo: after,
				PageSize: int32(s.chunkSize), //nolint:gosec // configured constant
			})
			if err != nil {
				return fmt.Errorf("memberimport: read staged rows: %w", err)
			}
			for _, r := range rows {
				if r.Status == RowInvalid || r.Status == RowApplied || r.Status == RowSkipped {
					continue
				}
				if err := s.validateRow(ctx, tx, batch, stagedRow(sqlcgen.ListImportRowsRow(r)), principals, plans); err != nil {
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
			break
		}
		after = next
	}

	return db.WithTenantTx(ctx, s.pool, tc, func(ctx context.Context, tx pgx.Tx) error {
		counters, err := readCounters(ctx, tx, tenantID, batchID)
		if err != nil {
			return err
		}
		if err := writeCounters(ctx, tx, tenantID, batchID, counters); err != nil {
			return err
		}
		next := StatusReady
		if counters.Review > 0 {
			next = StatusReview
		}
		summary := reconciliationSummary(int(batch.RowCount), counters)
		if _, err := sqlcgen.New(tx).SetImportBatchStatus(ctx, sqlcgen.SetImportBatchStatusParams{
			Status: next, ErrorSummary: &summary, TenantID: tenantID, ID: batchID,
			FromStatuses: []string{StatusValidating},
		}); err != nil {
			return fmt.Errorf("memberimport: finish validation: %w", err)
		}
		return nil
	})
}

// beginValidation moves a freshly staged batch to VALIDATING. started is false when the
// batch is already past validation, which makes a redelivered job a no-op.
func (s *Service) beginValidation(ctx context.Context, tenantID, batchID uuid.UUID) (batchRef, bool, error) {
	var out batchRef
	var started bool
	err := db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenantID}, func(ctx context.Context, tx pgx.Tx) error {
		row, err := loadBatch(ctx, tx, tenantID, batchID)
		if err != nil {
			return err
		}
		out = batchRef{TenantID: tenantID, GetImportBatchRow: row}
		if row.Status != StatusReceived && row.Status != StatusValidating {
			return nil
		}
		started = true
		if _, err := sqlcgen.New(tx).SetImportBatchStatus(ctx, sqlcgen.SetImportBatchStatusParams{
			Status: StatusValidating, ErrorSummary: row.ErrorSummary, TenantID: tenantID, ID: batchID,
			FromStatuses: []string{StatusReceived, StatusValidating},
		}); err != nil {
			return fmt.Errorf("memberimport: start validation: %w", err)
		}
		return nil
	})
	return out, started && err == nil, err
}

// batchPrincipalIndex maps the blind index of every principal row's member number to its
// source_record_id, so a dependant referencing a principal of the same file resolves
// without any plaintext ever leaving the staging row.
func (s *Service) batchPrincipalIndex(ctx context.Context, tc db.TenantContext, batchID uuid.UUID) (map[string]string, error) {
	index := map[string]string{}
	after := int32(0)
	for {
		next := int32(-1)
		err := db.WithTenantTx(ctx, s.pool, tc, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := sqlcgen.New(tx).ListImportRowsForProcessing(ctx, sqlcgen.ListImportRowsForProcessingParams{
				TenantID: tc.TenantID, BatchID: batchID, AfterRowNo: after,
				PageSize: int32(s.chunkSize), //nolint:gosec // configured constant
			})
			if err != nil {
				return fmt.Errorf("memberimport: index principals: %w", err)
			}
			for _, r := range rows {
				payload, err := decodePayload(r.Payload)
				if err != nil {
					return fmt.Errorf("memberimport: decode payload: %w", err)
				}
				if payload.Role != RolePrincipal {
					continue
				}
				ids, err := decodeIdentifiers(r.Identifiers)
				if err != nil {
					return fmt.Errorf("memberimport: decode identifiers: %w", err)
				}
				for _, id := range ids {
					if id.Type == domain.TypeMemberNo {
						index[id.Hash] = r.SourceRecordID
					}
				}
			}
			if len(rows) == s.chunkSize {
				next = rows[len(rows)-1].RowNo
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if next < 0 {
			return index, nil
		}
		after = next
	}
}

// planLookup caches one plan_code resolution of the batch.
type planLookup struct {
	id    uuid.UUID
	field RowError
	ok    bool
}

// validateRow resolves the row's plan and principal reference and matches it against the
// live registry (v1.2 10.1 steps 2 and 3).
func (s *Service) validateRow(ctx context.Context, tx pgx.Tx, batch batchRef,
	row stagedRow, principals map[string]string, plans map[string]planLookup,
) error {
	payload, err := decodePayload(row.Payload)
	if err != nil {
		return fmt.Errorf("memberimport: decode payload: %w", err)
	}
	ids, err := decodeIdentifiers(row.Identifiers)
	if err != nil {
		return fmt.Errorf("memberimport: decode identifiers: %w", err)
	}
	rowErrors, err := decodeErrors(row.Errors)
	if err != nil {
		return fmt.Errorf("memberimport: decode errors: %w", err)
	}
	q := sqlcgen.New(tx)

	// Plan: the row's plan_code wins over the batch default; both must resolve to a plan
	// with a version published on the row's start date.
	planID := uuid.Nil
	if batch.PlanID.Valid {
		planID = batch.PlanID.UUID
	}
	if payload.PlanCode != "" {
		lookup, err := s.resolvePlan(ctx, tx, batch.TenantID, payload.PlanCode, payload.ValidFrom, plans)
		if err != nil {
			return err
		}
		if lookup.ok {
			planID = lookup.id
		} else {
			rowErrors = append(rowErrors, lookup.field)
		}
	}
	payload.PlanID = ""
	if planID != uuid.Nil {
		payload.PlanID = planID.String()
	}

	// Principal reference: inside the file first, then the live memberships.
	payload.PrincipalRecordID = ""
	if payload.Role == RoleDependant && payload.PrincipalHash != "" {
		record, inBatch := principals[payload.PrincipalHash]
		switch {
		case inBatch && record != row.SourceRecordID:
			payload.PrincipalRecordID = record
		default:
			_, found, err := s.findByHash(ctx, tx, batch, domain.TypeMemberNo, payload.PrincipalHash)
			if err != nil {
				return err
			}
			if !found {
				rowErrors = append(rowErrors, RowError{
					Field: ColPrincipalMemberNo, Code: CodePrincipalUnknown,
					Message: "asıl üye ne dosyada ne de kayıtlarda bulunabildi",
				})
			}
		}
	}

	var status, decision string
	matched := uuid.NullUUID{}
	payload.CandidatePersonIDs = nil
	switch {
	case len(rowErrors) > 0:
		status, decision = RowInvalid, ""
	default:
		candidates, err := s.matchCandidates(ctx, tx, batch, row, ids)
		if err != nil {
			return err
		}
		switch len(candidates) {
		case 0:
			status, decision = RowValid, DecisionCreate
		case 1:
			person, err := q.GetPersonForImport(ctx, sqlcgen.GetPersonForImportParams{
				TenantID: batch.TenantID, ID: candidates[0],
			})
			if err != nil {
				return fmt.Errorf("memberimport: load matched person: %w", err)
			}
			if person.Status == domain.PersonMerged {
				// A merged person is a redirect, not a match; a human decides.
				status, decision = RowConflict, ""
				payload.CandidatePersonIDs = []string{candidates[0].String()}
				break
			}
			status = RowMatched
			matched = uuid.NullUUID{UUID: candidates[0], Valid: true}
			changes, err := s.wouldChange(ctx, tx, batch, row, payload, person, planID)
			if err != nil {
				return err
			}
			decision = DecisionSkip
			if changes {
				decision = DecisionUpdate
			}
		default:
			status, decision = RowConflict, ""
			for _, c := range candidates {
				payload.CandidatePersonIDs = append(payload.CandidatePersonIDs, c.String())
			}
		}
	}

	payloadJSON, err := encodeJSON(payload)
	if err != nil {
		return fmt.Errorf("memberimport: encode payload: %w", err)
	}
	errorsJSON, err := encodeJSON(rowErrors)
	if err != nil {
		return fmt.Errorf("memberimport: encode errors: %w", err)
	}
	if _, err := q.UpdateImportRowOutcome(ctx, sqlcgen.UpdateImportRowOutcomeParams{
		Status: status, Payload: payloadJSON, Errors: errorsJSON,
		MatchedPersonID: matched, Decision: strPtr(decision),
		TenantID: batch.TenantID, ID: row.ID,
	}); err != nil {
		return fmt.Errorf("memberimport: write row outcome: %w", err)
	}
	return nil
}

// matchCandidates collects the distinct persons the row could be: the person carrying the
// same source_system/source_record_id, plus the owner of each blind-indexed identifier.
func (s *Service) matchCandidates(ctx context.Context, tx pgx.Tx, batch batchRef,
	row stagedRow, ids []stagedIdentifier,
) ([]uuid.UUID, error) {
	q := sqlcgen.New(tx)
	seen := map[uuid.UUID]bool{}
	source, record := batch.SourceSystem, row.SourceRecordID
	bySource, err := q.FindPersonBySourceRecord(ctx, sqlcgen.FindPersonBySourceRecordParams{
		TenantID: batch.TenantID, SourceSystem: &source, SourceRecordID: &record,
	})
	switch {
	case err == nil:
		seen[bySource.ID] = true
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return nil, fmt.Errorf("memberimport: match by source record: %w", err)
	}

	for _, id := range ids {
		if id.Hash == "" {
			continue
		}
		personID, found, err := s.findByHash(ctx, tx, batch, id.Type, id.Hash)
		if err != nil {
			return nil, err
		}
		if found {
			seen[personID] = true
		}
	}
	out := make([]uuid.UUID, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

// findByHash looks one blind index up in the scope its type uses: TCKN across the tenant,
// MEMBER_NO within the sponsor of the batch.
func (s *Service) findByHash(ctx context.Context, tx pgx.Tx, batch batchRef, typeCode, hexHash string) (uuid.UUID, bool, error) {
	raw, err := hex.DecodeString(hexHash)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("memberimport: decode blind index: %w", err)
	}
	found, err := sqlcgen.New(tx).FindPersonIdentifierByHash(ctx, sqlcgen.FindPersonIdentifierByHashParams{
		TenantID: batch.TenantID, IdentifierType: typeCode,
		ScopeKey: scopeKeyFor(typeCode, batch.SponsorTenantOrganizationID), IdentifierHash: raw,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("memberimport: match by identifier: %w", err)
	}
	return found.PersonID, true, nil
}

// scopeKeyFor mirrors party.identifier_type.uniqueness_scope for the two types the file
// carries: MEMBER_NO is unique per sponsor, everything else across the tenant.
func scopeKeyFor(typeCode string, sponsorID uuid.UUID) string {
	if typeCode == domain.TypeMemberNo {
		return sponsorID.String()
	}
	return ""
}

// wouldChange reports whether applying the row would write anything at all. A matched row
// that changes nothing becomes SKIP, which is what makes re-importing a file free.
func (s *Service) wouldChange(ctx context.Context, tx pgx.Tx, batch batchRef,
	row stagedRow, payload rowPayload, person sqlcgen.GetPersonForImportRow, planID uuid.UUID,
) (bool, error) {
	if person.FirstName != payload.FirstName ||
		deref(person.MiddleName) != payload.MiddleName ||
		person.LastName != payload.LastName ||
		dateString(person.BirthDate) != payload.BirthDate ||
		deref(person.SexAtBirth) != payload.SexAtBirth {
		return true, nil
	}
	q := sqlcgen.New(tx)
	source, record := batch.SourceSystem, row.SourceRecordID
	membership, err := q.FindImportMembershipBySource(ctx, sqlcgen.FindImportMembershipBySourceParams{
		TenantID: batch.TenantID, SponsorTenantOrganizationID: batch.SponsorTenantOrganizationID,
		SourceSystem: &source, SourceRecordID: &record,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("memberimport: membership lookup: %w", err)
	}
	if planID == uuid.Nil {
		return false, nil
	}
	if _, err := q.FindImportEnrollment(ctx, sqlcgen.FindImportEnrollmentParams{
		TenantID: batch.TenantID, SponsorMembershipID: membership.ID, PlanID: planID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return true, nil
		}
		return false, fmt.Errorf("memberimport: enrollment lookup: %w", err)
	}
	return false, nil
}

// resolvePlan turns a file plan_code into a plan whose version is published on the row's
// start date. Results are cached per (code, date) because a file repeats a handful of
// codes across thousands of rows.
func (s *Service) resolvePlan(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	code, validFrom string, cache map[string]planLookup,
) (planLookup, error) {
	key := code + "|" + validFrom
	if hit, ok := cache[key]; ok {
		return hit, nil
	}
	plans, err := sqlcgen.New(tx).FindPlansByCode(ctx, sqlcgen.FindPlansByCodeParams{TenantID: tenantID, Code: code})
	if err != nil {
		return planLookup{}, fmt.Errorf("memberimport: plan by code: %w", err)
	}
	active := make([]sqlcgen.FindPlansByCodeRow, 0, len(plans))
	for _, p := range plans {
		if p.Status == planActive {
			active = append(active, p)
		}
	}
	var out planLookup
	switch {
	case len(active) == 0:
		out.field = RowError{Field: ColPlanCode, Code: CodePlanUnknown, Message: "plan kodu bulunamadı veya etkin değil"}
	case len(active) > 1:
		out.field = RowError{Field: ColPlanCode, Code: CodePlanAmbiguous, Message: "plan kodu birden fazla programda tanımlı"}
	default:
		asOf, ok := parseDate(validFrom)
		if !ok {
			asOf = s.now()
		}
		if _, err := benefitapp.ResolvePlanVersion(ctx, tx, tenantID, active[0].ID, asOf); err != nil {
			if !errors.Is(err, benefitapp.ErrNoPublishedVersion) {
				return planLookup{}, err
			}
			out.field = RowError{Field: ColPlanCode, Code: CodePlanNotPublished,
				Message: "bu tarihte yayınlanmış plan sürümü yok"}
		} else {
			out.id, out.ok = active[0].ID, true
		}
	}
	cache[key] = out
	return out, nil
}

// planActive mirrors benefit.plan.status; a file may only enroll into an active plan.
const planActive = "ACTIVE"

// reconciliationSummary is the one-line report GET /imports/members/{id} returns beside
// the counters: rows in the file against what happened to them.
func reconciliationSummary(rowCount int, c Counters) string {
	return fmt.Sprintf("rows=%d valid=%d invalid=%d matched=%d conflict=%d created=%d updated=%d skipped=%d",
		rowCount, c.Valid, c.Invalid, c.Matched, c.Conflict, c.Created, c.Updated, c.Skipped)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
