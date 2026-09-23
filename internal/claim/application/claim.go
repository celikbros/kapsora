package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/claim/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// NewLineInput is one line as the caller sent it.
type NewLineInput struct {
	LineNo              int
	ServiceDefinitionID uuid.UUID
	UnitType            string
	Quantity            string
	UnitAmount          *string
	LineAmount          string
	CurrencyCode        *string
	DiagnosisID         *uuid.UUID
	MedicalReportID     *uuid.UUID
	PractitionerID      *uuid.UUID
	Description         *string
}

// NewClaimInput is the create command.
type NewClaimInput struct {
	PersonID               uuid.UUID
	ProgramID              uuid.UUID
	EnrollmentID           uuid.UUID
	ProviderOrganizationID uuid.UUID
	CaseID                 *uuid.UUID
	FulfilmentID           *uuid.UUID
	AuthorizationID        *uuid.UUID
	ServiceDateFrom        time.Time
	ServiceDateTo          time.Time
	Channel                string
	Lines                  []NewLineInput
}

// DraftInput is the header patch of a claim whose current version is still a draft.
type DraftInput struct {
	ServiceDateFrom time.Time
	ServiceDateTo   time.Time
	Channel         string
	CaseID          *uuid.UUID
	FulfilmentID    *uuid.UUID
	AuthorizationID *uuid.UUID
	ExpectedVersion int64
}

// ClaimFilter is the API-level list request.
type ClaimFilter struct {
	Cursor                 string
	Limit                  int
	PersonID               *uuid.UUID
	CaseID                 *uuid.UUID
	ProviderOrganizationID *uuid.UUID
	Status                 string
	ServiceDateFrom        *time.Time
	ServiceDateTo          *time.Time
}

// ReasonInput is a claim-level command's reason, with the If-Match it was given.
type ReasonInput struct {
	ReasonCode      string
	ReasonText      *string
	ReviewComment   *string
	ExpectedVersion int64
}

// ListClaims returns a page of claims with the lines of each one's current version, in the
// projection the caller has earned.
//
// A list never answers 428, for the reason WP-I5-01 wrote down: refusing the whole page over
// one sensitive row would make the list useless, and refusing that one row would say which
// row is sensitive.
func (s *Service) ListClaims(ctx context.Context, rc identity.RequestContext, f ClaimFilter,
	req AccessRequest,
) (ClaimPage, error) {
	if err := domain.ValidateStatusFilter(f.Status); err != nil {
		return ClaimPage{}, err
	}
	after, pageSize, err := s.paging(f.Cursor, f.Limit)
	if err != nil {
		return ClaimPage{}, err
	}

	var out ClaimPage
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.validateAccess(ctx, tx, req); err != nil {
			return err
		}
		rows, err := s.repo.ListClaims(ctx, tx, rc.TenantID, ClaimQuery{
			Scope: scopeOf(rc), PersonID: f.PersonID, CaseID: f.CaseID,
			ProviderOrganizationID: f.ProviderOrganizationID, Status: f.Status,
			ServiceDateFrom: f.ServiceDateFrom, ServiceDateTo: f.ServiceDateTo,
			After: after, PageSize: pageSize + 1,
		})
		if err != nil {
			return err
		}
		if len(rows) > pageSize {
			out.NextCursor = s.cursors.Encode(claimCursor(rows[pageSize-1]))
			rows = rows[:pageSize]
		}
		out.Items = make([]ClaimView, 0, len(rows))
		for _, row := range rows {
			decision, err := s.projectionFor(ctx, tx, rc, row, req)
			if err != nil && !errors.Is(err, ErrAccessPurposeRequired) {
				return err
			}
			view, err := s.viewClaim(ctx, tx, rc.TenantID, row, decision.Projection)
			if err != nil {
				return err
			}
			if decision.Projection == ProjectionClinical {
				if err := s.recordAccess(ctx, tx, rc, row.PersonID, row.ID,
					audit.AccessSearch, req, audit.OutcomeSuccess); err != nil {
					return err
				}
			}
			out.Items = append(out.Items, view)
		}
		return nil
	})
	if err != nil {
		return ClaimPage{}, err
	}
	return out, nil
}

// GetClaim returns one claim with the lines of its current version. This is the read that
// demands a purpose when the case behind it is sensitive.
func (s *Service) GetClaim(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	req AccessRequest,
) (ClaimView, error) {
	var (
		out    ClaimView
		person uuid.UUID
	)
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.validateAccess(ctx, tx, req); err != nil {
			return err
		}
		record, err := s.repo.GetClaim(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		person = record.PersonID
		decision, err := s.projectionFor(ctx, tx, rc, record, req)
		if err != nil {
			return err
		}
		out, err = s.viewClaim(ctx, tx, rc.TenantID, record, decision.Projection)
		if err != nil {
			return err
		}
		switch {
		case decision.Projection == ProjectionClinical:
			return s.recordAccess(ctx, tx, rc, record.PersonID, record.ID,
				audit.AccessView, req, audit.OutcomeSuccess)
		case decision.RefusedSensitive:
			return s.recordAccess(ctx, tx, rc, record.PersonID, record.ID,
				audit.AccessView, req, audit.OutcomeDenied)
		}
		return nil
	})
	if errors.Is(err, ErrAccessPurposeRequired) {
		s.recordDenial(ctx, rc, person, id, req)
		return ClaimView{}, err
	}
	if err != nil {
		return ClaimView{}, err
	}
	return out, nil
}

// viewClaim loads the lines of a claim's current version and their decisions, and applies the
// projection to all of it. It records nothing: the callers above decide what a read owes.
func (s *Service) viewClaim(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	record ClaimRecord, p Projection,
) (ClaimView, error) {
	version, err := s.repo.GetVersion(ctx, tx, tenantID, record.ID, record.CurrentVersionNo)
	if err != nil {
		return ClaimView{}, err
	}
	lines, decisions, err := s.linesOf(ctx, tx, tenantID, version.ID)
	if err != nil {
		return ClaimView{}, err
	}
	_, exceptions := readRouting(version.Snapshot)
	return ClaimView{
		Projection: p, Claim: projectClaim(record, p),
		Lines:      buildLineViews(lines, decisions, p),
		Exceptions: projectExceptions(exceptions, p),
	}, nil
}

// linesOf reads a version's lines and the head of each line's decision history.
func (s *Service) linesOf(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID,
) ([]LineRecord, []DecisionRecord, error) {
	lines, err := s.repo.ListLines(ctx, tx, tenantID, versionID)
	if err != nil {
		return nil, nil, err
	}
	decisions, err := s.repo.ListLatestDecisions(ctx, tx, tenantID, versionID)
	if err != nil {
		return nil, nil, err
	}
	return lines, decisions, nil
}

// CreateClaim opens a draft with version 1 and the lines it was given.
//
// Everything happens in one transaction: the header, the version and the lines are one fact.
// A claim with no version, or a version with no lines, is a state no reader ever observes —
// and the version is what the lines hang off, so there is no order in which they could exist
// separately anyway.
func (s *Service) CreateClaim(ctx context.Context, rc identity.RequestContext, in NewClaimInput,
	req AccessRequest,
) (ClaimView, error) {
	if err := domain.ValidatePeriod(domain.Period{From: in.ServiceDateFrom, To: in.ServiceDateTo}); err != nil {
		return ClaimView{}, err
	}
	if err := domain.ValidateChannel(in.Channel); err != nil {
		return ClaimView{}, err
	}
	if err := validateLineInputs(in.Lines); err != nil {
		return ClaimView{}, err
	}
	if in.ProviderOrganizationID == uuid.Nil {
		return ClaimView{}, fieldError("providerOrganizationId", "REQUIRED", "sağlayıcı kurumu zorunlu")
	}
	if err := checkProviderScope(rc, in.ProviderOrganizationID); err != nil {
		return ClaimView{}, err
	}
	channel := in.Channel
	if channel == "" {
		channel = domain.DefaultChannel
	}

	var out ClaimView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.validateAccess(ctx, tx, req); err != nil {
			return err
		}
		if err := s.checkProvider(ctx, tx, rc.TenantID, in.ProviderOrganizationID); err != nil {
			return err
		}
		if err := s.checkEnrollment(ctx, tx, rc.TenantID, in); err != nil {
			return err
		}
		if err := s.checkServices(ctx, tx, rc.TenantID, in.Lines); err != nil {
			return err
		}
		record, err := s.createDraft(ctx, tx, rc, in, channel)
		if err != nil {
			return err
		}
		decision, err := s.projectionFor(ctx, tx, rc, record, AccessRequest{})
		if err != nil {
			return err
		}
		out, err = s.viewClaim(ctx, tx, rc.TenantID, record, decision.Projection)
		return err
	})
	if err != nil {
		return ClaimView{}, err
	}
	return out, nil
}

// createDraft writes the shared draft/version/line/audit unit inside the caller's transaction.
func (s *Service) createDraft(ctx context.Context, tx pgx.Tx, rc identity.RequestContext, in NewClaimInput, channel string) (ClaimRecord, error) {
	record, err := s.createWithReference(ctx, tx, rc, in, channel)
	if err != nil {
		return ClaimRecord{}, err
	}
	version, err := s.repo.CreateVersion(ctx, tx, rc.TenantID, record.ID, 1,
		actorPtr(rc.Principal.ActorID))
	if err != nil {
		return ClaimRecord{}, err
	}
	if err := s.repo.ReplaceLines(ctx, tx, rc.TenantID, version.ID,
		lineRows(version.ID, in.Lines, actorPtr(rc.Principal.ActorID))); err != nil {
		return ClaimRecord{}, err
	}
	if err := s.record(ctx, tx, rc, "claim.create", record.ID, map[string]any{
		"reference": record.Reference, "provider": record.ProviderOrganizationID.String(),
		"version_no": 1, "line_count": len(in.Lines),
	}); err != nil {
		return ClaimRecord{}, err
	}
	return record, nil
}

// createWithReference retries a reference collision rather than making somebody read one.
func (s *Service) createWithReference(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	in NewClaimInput, channel string,
) (ClaimRecord, error) {
	for attempt := 0; attempt < referenceAttempts; attempt++ {
		reference, err := newReference(s.now())
		if err != nil {
			return ClaimRecord{}, err
		}
		record, err := s.repo.CreateClaim(ctx, tx, rc.TenantID, NewClaimRow{
			Reference: reference, PersonID: in.PersonID, ProgramID: in.ProgramID,
			EnrollmentID: in.EnrollmentID, ProviderOrganizationID: in.ProviderOrganizationID,
			DomainCode: domain.DomainHealth,
			// The pair follows the case. A claim raised by hand against an episode of care
			// names it once and the two columns say the same thing, which is what
			// `ck_claim_case_matches_source` asserts.
			SourceType: caseSource(in.CaseID), SourceID: in.CaseID,
			CaseID: in.CaseID, FulfilmentID: in.FulfilmentID,
			AuthorizationID: in.AuthorizationID,
			ServiceDateFrom: domain.DateOnly(in.ServiceDateFrom),
			ServiceDateTo:   domain.DateOnly(in.ServiceDateTo), Channel: channel,
			ActorID: actorPtr(rc.Principal.ActorID),
		})
		switch {
		case err == nil:
			return record, nil
		case errors.Is(err, ErrReferenceCollision):
			continue
		default:
			return ClaimRecord{}, err
		}
	}
	return ClaimRecord{}, ErrReferenceCollision
}

// PatchDraft edits the header of a claim whose current version is still a draft.
//
// The freeze is the repository's predicate, not a check made here: a claim whose current
// version has been submitted is not DRAFT or RETURNED, so the UPDATE matches no row. The
// service then reads the row back to tell the caller which of the two refusals it earned —
// the version is frozen, or somebody moved it since the ETag was issued.
func (s *Service) PatchDraft(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	in DraftInput,
) (ClaimView, error) {
	if err := domain.ValidatePeriod(domain.Period{From: in.ServiceDateFrom, To: in.ServiceDateTo}); err != nil {
		return ClaimView{}, err
	}
	if err := domain.ValidateChannel(in.Channel); err != nil {
		return ClaimView{}, err
	}
	channel := in.Channel
	if channel == "" {
		channel = domain.DefaultChannel
	}

	var out ClaimView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.LockClaim(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		if !domain.Allowed(domain.CommandEdit, current.Status) {
			return ErrVersionFrozen
		}
		if current.RowVersion != in.ExpectedVersion {
			return ErrVersionMismatch
		}
		updated, err := s.repo.UpdateDraft(ctx, tx, rc.TenantID, id, DraftRow{
			ServiceDateFrom: domain.DateOnly(in.ServiceDateFrom),
			ServiceDateTo:   domain.DateOnly(in.ServiceDateTo), Channel: channel,
			CaseID: in.CaseID, FulfilmentID: in.FulfilmentID,
			AuthorizationID: in.AuthorizationID, ActorID: actorPtr(rc.Principal.ActorID),
		}, in.ExpectedVersion)
		if err != nil {
			return err
		}
		if !updated {
			return ErrVersionMismatch
		}
		if err := s.record(ctx, tx, rc, "claim.patch", id, map[string]any{
			"reference": current.Reference, "version_no": current.CurrentVersionNo,
		}); err != nil {
			return err
		}
		out, err = s.reload(ctx, tx, rc, id)
		return err
	})
	if err != nil {
		return ClaimView{}, err
	}
	return out, nil
}

// PutLines replaces the whole line set of the claim's draft version.
//
// The set is the unit. A claim is what its lines say together — the total, the currency, the
// duplicate check — and a per-line PATCH would let two clerks leave a claim nobody meant to
// send.
func (s *Service) PutLines(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	lines []NewLineInput, expected int64,
) (ClaimView, error) {
	if err := validateLineInputs(lines); err != nil {
		return ClaimView{}, err
	}

	var out ClaimView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.LockClaim(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		if !domain.Allowed(domain.CommandEdit, current.Status) {
			return ErrVersionFrozen
		}
		if current.RowVersion != expected {
			return ErrVersionMismatch
		}
		if err := s.checkServices(ctx, tx, rc.TenantID, lines); err != nil {
			return err
		}
		version, err := s.repo.GetDraftVersion(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		// A financial projection cannot round-trip clinical references. Preserve them
		// for the same numbered service; refuse changing a clinically linked service.
		if !rc.Has(PermissionClinicalRead) {
			previous, err := s.repo.ListLines(ctx, tx, rc.TenantID, version.ID)
			if err != nil {
				return err
			}
			for i := range lines {
				for _, old := range previous {
					if old.LineNo != lines[i].LineNo {
						continue
					}
					if old.DiagnosisID != nil || old.MedicalReportID != nil {
						if old.ServiceDefinitionID != lines[i].ServiceDefinitionID {
							return fieldError("lines", "CLINICAL_LINK", "Klinik kayda bağlı hizmet değiştirilemez.")
						}
					}
					lines[i].DiagnosisID = old.DiagnosisID
					lines[i].MedicalReportID = old.MedicalReportID
					lines[i].Description = old.Description
				}
			}
		}

		if err := s.repo.ReplaceLines(ctx, tx, rc.TenantID, version.ID,
			lineRows(version.ID, lines, actorPtr(rc.Principal.ActorID))); err != nil {
			return err
		}
		// The lines belong to the version and the ETag belongs to the claim, so the claim's
		// row_version has to move or the caller's next If-Match would be stale.
		moved, err := s.repo.TouchClaim(ctx, tx, rc.TenantID, id, actorPtr(rc.Principal.ActorID), expected)
		if err != nil {
			return err
		}
		if !moved {
			return ErrVersionMismatch
		}
		if err := s.record(ctx, tx, rc, "claim.lines.put", id, map[string]any{
			"reference": current.Reference, "version_no": version.VersionNo,
			"line_count": len(lines),
		}); err != nil {
			return err
		}
		out, err = s.reload(ctx, tx, rc, id)
		return err
	})
	if err != nil {
		return ClaimView{}, err
	}
	return out, nil
}

// Cancel withdraws a claim the provider should not have raised. It is refused on anything
// already decided: a cancelled claim is a fact, and cancelling a settled one would be an
// accounting entry rather than a withdrawal.
func (s *Service) Cancel(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	in ReasonInput,
) (ClaimView, error) {
	if err := domain.ValidateReason("reasonCode", in.ReasonCode, in.ReasonText); err != nil {
		return ClaimView{}, err
	}

	var out ClaimView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		current, err := s.repo.LockClaim(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		if !domain.Allowed(domain.CommandCancel, current.Status) {
			return ErrTransitionInvalid
		}
		if current.RowVersion != in.ExpectedVersion {
			return ErrVersionMismatch
		}
		now := s.now().UTC()
		moved, err := s.repo.SetStatus(ctx, tx, rc.TenantID, id, StatusRow{
			Status: domain.StatusCancelled, CurrentVersionNo: current.CurrentVersionNo,
			ClosedAt: &now, FromStatuses: domain.From(domain.CommandCancel),
			ActorID: actorPtr(rc.Principal.ActorID),
		}, in.ExpectedVersion)
		if err != nil {
			return err
		}
		if !moved {
			return ErrVersionMismatch
		}
		// Whatever the claim was holding goes back. A withdrawn claim that kept a member's
		// entitlement reserved would be a benefit the member paid for and cannot use.
		if err := s.releaseHold(ctx, tx, rc, current, "CLAIM_CANCELLED"); err != nil {
			return err
		}
		if err := s.record(ctx, tx, rc, "claim.cancel", id, map[string]any{
			"reference": current.Reference, "reason_code": in.ReasonCode,
			"version_no": current.CurrentVersionNo,
		}); err != nil {
			return err
		}
		out, err = s.reload(ctx, tx, rc, id)
		return err
	})
	if err != nil {
		return ClaimView{}, err
	}
	return out, nil
}

// ListVersions returns every version of the claim, newest first.
func (s *Service) ListVersions(ctx context.Context, rc identity.RequestContext, id uuid.UUID) ([]VersionRecord, error) {
	var out []VersionRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetClaim(ctx, tx, rc.TenantID, id, scopeOf(rc)); err != nil {
			return err
		}
		rows, err := s.repo.ListVersions(ctx, tx, rc.TenantID, id)
		out = rows
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetVersion returns one version with the lines it carried and the decision each line was
// given, in the projection the caller has earned.
//
// A superseded version answers exactly what it answered the day it was decided: the lines
// belong to the version and the decisions point at those lines, so nothing a later version
// did can reach back and change it.
func (s *Service) GetVersion(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	versionNo int, req AccessRequest,
) (VersionView, error) {
	var (
		out    VersionView
		person uuid.UUID
	)
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if err := s.validateAccess(ctx, tx, req); err != nil {
			return err
		}
		record, err := s.repo.GetClaim(ctx, tx, rc.TenantID, id, scopeOf(rc))
		if err != nil {
			return err
		}
		person = record.PersonID
		version, err := s.repo.GetVersion(ctx, tx, rc.TenantID, id, versionNo)
		if err != nil {
			return err
		}
		decision, err := s.projectionFor(ctx, tx, rc, record, req)
		if err != nil {
			return err
		}
		lines, decisions, err := s.linesOf(ctx, tx, rc.TenantID, version.ID)
		if err != nil {
			return err
		}
		_, exceptions := readRouting(version.Snapshot)
		out = VersionView{
			Projection: decision.Projection, Version: version,
			Lines:      buildLineViews(lines, decisions, decision.Projection),
			Exceptions: projectExceptions(exceptions, decision.Projection),
		}
		switch {
		case decision.Projection == ProjectionClinical:
			return s.recordAccess(ctx, tx, rc, record.PersonID, record.ID,
				audit.AccessView, req, audit.OutcomeSuccess)
		case decision.RefusedSensitive:
			return s.recordAccess(ctx, tx, rc, record.PersonID, record.ID,
				audit.AccessView, req, audit.OutcomeDenied)
		}
		return nil
	})
	if errors.Is(err, ErrAccessPurposeRequired) {
		s.recordDenial(ctx, rc, person, id, req)
		return VersionView{}, err
	}
	if err != nil {
		return VersionView{}, err
	}
	return out, nil
}

// reload is the answer a write hands back. It records no access event: the access log answers
// "who looked at this person's clinical data", and the person who just wrote it is on the
// business audit row instead, where a write belongs.
func (s *Service) reload(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	id uuid.UUID,
) (ClaimView, error) {
	record, err := s.repo.GetClaim(ctx, tx, rc.TenantID, id, scopeOf(rc))
	if err != nil {
		return ClaimView{}, err
	}
	decision, err := s.projectionFor(ctx, tx, rc, record, AccessRequest{})
	if err != nil && !errors.Is(err, ErrAccessPurposeRequired) {
		return ClaimView{}, err
	}
	return s.viewClaim(ctx, tx, rc.TenantID, record, decision.Projection)
}

// checkProvider refuses a claim raised against an organization that is not a provider of this
// tenant. A claim against a sponsor or a payer is a claim nobody can pay.
func (s *Service) checkProvider(ctx context.Context, tx pgx.Tx, tenantID, orgID uuid.UUID) error {
	ok, err := s.repo.ProviderOrganizationExists(ctx, tx, tenantID, orgID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrProviderUnknown
	}
	return nil
}

// checkEnrollment refuses a claim naming somebody else's plan record.
func (s *Service) checkEnrollment(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in NewClaimInput,
) error {
	enrollment, err := s.repo.GetEnrollment(ctx, tx, tenantID, in.EnrollmentID)
	if err != nil {
		return err
	}
	if enrollment.PersonID != in.PersonID || enrollment.ProgramID != in.ProgramID {
		return ErrEnrollmentMismatch
	}
	return nil
}

// checkServices refuses a line naming a service the catalogue does not have. One read for the
// whole set rather than one per line: a claim with twenty lines of the same service should not
// cost twenty round trips to find that out.
func (s *Service) checkServices(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	lines []NewLineInput,
) error {
	wanted := make([]uuid.UUID, 0, len(lines))
	seen := make(map[uuid.UUID]bool, len(lines))
	for _, line := range lines {
		if seen[line.ServiceDefinitionID] {
			continue
		}
		seen[line.ServiceDefinitionID] = true
		wanted = append(wanted, line.ServiceDefinitionID)
	}
	found, err := s.repo.ResolveServiceDefinitions(ctx, tx, tenantID, wanted)
	if err != nil {
		return err
	}
	if len(found) != len(wanted) {
		return ErrServiceUnknown
	}
	return nil
}

// validateLineInputs runs the domain's line rules over the wire shape.
func validateLineInputs(lines []NewLineInput) error {
	rows := make([]domain.NewLine, 0, len(lines))
	for _, line := range lines {
		rows = append(rows, domain.NewLine{
			LineNo: line.LineNo, ServiceDefinitionID: line.ServiceDefinitionID.String(),
			UnitType: line.UnitType, Quantity: line.Quantity, UnitAmount: line.UnitAmount,
			LineAmount: line.LineAmount, CurrencyCode: line.CurrencyCode,
			Description: line.Description,
		})
	}
	return domain.ValidateLines(rows)
}

// lineRows maps the wire shape onto the insert payload.
func lineRows(versionID uuid.UUID, lines []NewLineInput, actorID *uuid.UUID) []NewLineRow {
	out := make([]NewLineRow, 0, len(lines))
	for _, line := range lines {
		currency := "TRY"
		if line.CurrencyCode != nil && *line.CurrencyCode != "" {
			currency = *line.CurrencyCode
		}
		out = append(out, NewLineRow{
			VersionID: versionID, LineNo: line.LineNo,
			ServiceDefinitionID: line.ServiceDefinitionID, UnitType: line.UnitType,
			Quantity: line.Quantity, UnitAmount: line.UnitAmount, LineAmount: line.LineAmount,
			CurrencyCode: currency, DiagnosisID: line.DiagnosisID,
			MedicalReportID: line.MedicalReportID, PractitionerID: line.PractitionerID,
			Description: trimmedPtr(line.Description), ActorID: actorID,
		})
	}
	return out
}

// copyLines turns a decided version's lines back into the insert payload of the draft that
// corrects it. Everything is copied including the clinical fields: the provider is correcting
// its own statement, and a correction that silently dropped the diagnosis a line named would
// be a correction that changed more than the provider meant.
func copyLines(versionID uuid.UUID, lines []LineRecord, actorID *uuid.UUID) []NewLineRow {
	out := make([]NewLineRow, 0, len(lines))
	for _, line := range lines {
		out = append(out, NewLineRow{
			VersionID: versionID, LineNo: line.LineNo,
			ServiceDefinitionID: line.ServiceDefinitionID, UnitType: line.UnitType,
			Quantity: line.Quantity, UnitAmount: line.UnitAmount, LineAmount: line.LineAmount,
			CurrencyCode: line.CurrencyCode, DiagnosisID: line.DiagnosisID,
			MedicalReportID: line.MedicalReportID, PractitionerID: line.PractitionerID,
			Description: line.Description, ActorID: actorID,
		})
	}
	return out
}

// caseSource is the source type of a claim raised against an episode of care, and nothing at
// all for one raised against nothing.
func caseSource(caseID *uuid.UUID) *string {
	if caseID == nil {
		return nil
	}
	source := domain.SourceHealthCase
	return &source
}
