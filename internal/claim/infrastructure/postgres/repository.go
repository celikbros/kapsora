// Package claimpg implements the claim repository with sqlc. It is stateless: every method
// takes the caller's tenant-bound transaction, so RLS is active for every statement and
// nothing here can read another tenant's claims.
//
// The provider boundary lives here rather than above: every read takes the caller's scope and
// hands it to SQL, so a claim outside it is genuinely not returned. That is what lets the
// application layer answer 404 without ever having held the row.
//
// What this package deliberately does not do is decide what a caller may see. Every read
// returns the whole row — the line description, the diagnosis reference, the medical
// reviewer's comment — and the projection is applied one layer up. A repository that filtered
// as well would be a second rule about clinical visibility, and two rules is how one of them
// ends up wrong.
package claimpg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/celikbros/kapsora/internal/claim/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// The constraint names the repository turns into named errors, so a caller reads a refusal
// rather than a PostgreSQL string.
const (
	constraintReference = "uq_claim_reference"
	sqlStateUnique      = "23505"
)

// Repository implements application.Repository.
type Repository struct{}

// New returns the repository.
func New() *Repository { return &Repository{} }

var _ application.Repository = (*Repository)(nil)

// CreateClaim implements application.Repository.
func (Repository) CreateClaim(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewClaimRow,
) (application.ClaimRecord, error) {
	row, err := sqlcgen.New(tx).CreateClaim(ctx, sqlcgen.CreateClaimParams{
		TenantID: tenantID, Reference: in.Reference, PersonID: in.PersonID,
		ProgramID: in.ProgramID, EnrollmentID: in.EnrollmentID,
		ProviderOrganizationID: in.ProviderOrganizationID, DomainCode: in.DomainCode,
		CaseID: optUUID(in.CaseID), FulfilmentID: optUUID(in.FulfilmentID),
		AuthorizationID: optUUID(in.AuthorizationID),
		ServiceDateFrom: dateParam(in.ServiceDateFrom), ServiceDateTo: dateParam(in.ServiceDateTo),
		Channel: in.Channel, ActorID: optUUID(in.ActorID),
	})
	if isUniqueViolation(err, constraintReference) {
		return application.ClaimRecord{}, application.ErrReferenceCollision
	}
	if err != nil {
		return application.ClaimRecord{}, fmt.Errorf("claim: create claim: %w", err)
	}
	return claimOf(createdClaimRow(row)), nil
}

// GetClaim implements application.Repository.
func (Repository) GetClaim(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.ClaimRecord, error) {
	row, err := sqlcgen.New(tx).GetClaim(ctx, sqlcgen.GetClaimParams{
		TenantID: tenantID, ID: id, ScopeIds: scope.OrganizationIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ClaimRecord{}, application.ErrClaimNotFound
	}
	if err != nil {
		return application.ClaimRecord{}, fmt.Errorf("claim: get claim: %w", err)
	}
	return claimOf(getClaimRow(row)), nil
}

// LockClaim implements application.Repository.
func (Repository) LockClaim(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.ClaimRecord, error) {
	row, err := sqlcgen.New(tx).LockClaim(ctx, sqlcgen.LockClaimParams{
		TenantID: tenantID, ID: id, ScopeIds: scope.OrganizationIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ClaimRecord{}, application.ErrClaimNotFound
	}
	if err != nil {
		return application.ClaimRecord{}, fmt.Errorf("claim: lock claim: %w", err)
	}
	return claimOf(lockClaimRow(row)), nil
}

// ListClaims implements application.Repository.
func (Repository) ListClaims(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.ClaimQuery,
) ([]application.ClaimRecord, error) {
	params := sqlcgen.ListClaimsParams{
		TenantID: tenantID, ScopeIds: q.Scope.OrganizationIDs,
		PersonID: optUUID(q.PersonID), CaseID: optUUID(q.CaseID),
		ProviderOrganizationID: optUUID(q.ProviderOrganizationID),
		Status:                 optionalString(q.Status),
		ServiceDateFrom:        optDate(q.ServiceDateFrom), ServiceDateTo: optDate(q.ServiceDateTo),
		PageSize: pageSize(q.PageSize),
	}
	if q.After != nil {
		params.AfterAt = &q.After.CreatedAt
		params.AfterID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListClaims(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("claim: list claims: %w", err)
	}
	out := make([]application.ClaimRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, claimOf(listClaimRow(row)))
	}
	return out, nil
}

// UpdateDraft implements application.Repository.
func (Repository) UpdateDraft(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	in application.DraftRow, expected int64,
) (bool, error) {
	affected, err := sqlcgen.New(tx).UpdateClaimDraft(ctx, sqlcgen.UpdateClaimDraftParams{
		TenantID: tenantID, ID: id,
		ServiceDateFrom: dateParam(in.ServiceDateFrom), ServiceDateTo: dateParam(in.ServiceDateTo),
		Channel: in.Channel, CaseID: optUUID(in.CaseID),
		FulfilmentID: optUUID(in.FulfilmentID), AuthorizationID: optUUID(in.AuthorizationID),
		ActorID: optUUID(in.ActorID), ExpectedRowVersion: expected,
	})
	if err != nil {
		return false, fmt.Errorf("claim: update draft: %w", err)
	}
	return affected == 1, nil
}

// TouchClaim implements application.Repository.
func (Repository) TouchClaim(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	actorID *uuid.UUID, expected int64,
) (bool, error) {
	affected, err := sqlcgen.New(tx).TouchClaim(ctx, sqlcgen.TouchClaimParams{
		TenantID: tenantID, ID: id, ActorID: optUUID(actorID), ExpectedRowVersion: expected,
	})
	if err != nil {
		return false, fmt.Errorf("claim: touch claim: %w", err)
	}
	return affected == 1, nil
}

// SetStatus implements application.Repository.
func (Repository) SetStatus(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	in application.StatusRow, expected int64,
) (bool, error) {
	affected, err := sqlcgen.New(tx).SetClaimStatus(ctx, sqlcgen.SetClaimStatusParams{
		TenantID: tenantID, ID: id, Status: in.Status,
		CurrentVersionNo: int32(in.CurrentVersionNo), //nolint:gosec // a version number is bounded by the versions somebody made
		RejectReasonCode: in.RejectReasonCode, ReturnReasonCode: in.ReturnReasonCode,
		ClosedAt: in.ClosedAt, FromStatuses: in.FromStatuses, ActorID: optUUID(in.ActorID),
		ExpectedRowVersion: expected,
	})
	if err != nil {
		return false, fmt.Errorf("claim: set claim status: %w", err)
	}
	return affected == 1, nil
}

// SetReviewComment implements application.Repository.
func (Repository) SetReviewComment(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	stage string, comment *string, actorID *uuid.UUID,
) error {
	_, err := sqlcgen.New(tx).SetClaimReviewComment(ctx, sqlcgen.SetClaimReviewCommentParams{
		TenantID: tenantID, ID: id, Stage: stage, Comment: comment, ActorID: optUUID(actorID),
	})
	if err != nil {
		return fmt.Errorf("claim: set review comment: %w", err)
	}
	return nil
}

// CreateVersion implements application.Repository.
func (Repository) CreateVersion(ctx context.Context, tx pgx.Tx, tenantID, claimID uuid.UUID,
	versionNo int, actorID *uuid.UUID,
) (application.VersionRecord, error) {
	row, err := sqlcgen.New(tx).CreateClaimVersion(ctx, sqlcgen.CreateClaimVersionParams{
		TenantID: tenantID, ClaimID: claimID,
		VersionNo: int32(versionNo), //nolint:gosec // a version number is bounded by the versions somebody made
		ActorID:   optUUID(actorID),
	})
	if err != nil {
		return application.VersionRecord{}, fmt.Errorf("claim: create version: %w", err)
	}
	return versionOf(createdVersionRow(row)), nil
}

// GetDraftVersion implements application.Repository.
func (Repository) GetDraftVersion(ctx context.Context, tx pgx.Tx, tenantID, claimID uuid.UUID,
) (application.VersionRecord, error) {
	row, err := sqlcgen.New(tx).GetClaimDraftVersion(ctx, sqlcgen.GetClaimDraftVersionParams{
		TenantID: tenantID, ClaimID: claimID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.VersionRecord{}, application.ErrVersionFrozen
	}
	if err != nil {
		return application.VersionRecord{}, fmt.Errorf("claim: get draft version: %w", err)
	}
	return versionOf(draftVersionRow(row)), nil
}

// GetVersion implements application.Repository.
func (Repository) GetVersion(ctx context.Context, tx pgx.Tx, tenantID, claimID uuid.UUID,
	versionNo int,
) (application.VersionRecord, error) {
	row, err := sqlcgen.New(tx).GetClaimVersionByNo(ctx, sqlcgen.GetClaimVersionByNoParams{
		TenantID: tenantID, ClaimID: claimID,
		VersionNo: int32(versionNo), //nolint:gosec // a version number is bounded by the versions somebody made
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.VersionRecord{}, application.ErrVersionNotFound
	}
	if err != nil {
		return application.VersionRecord{}, fmt.Errorf("claim: get version: %w", err)
	}
	return versionOf(versionByNoRow(row)), nil
}

// ListVersions implements application.Repository.
func (Repository) ListVersions(ctx context.Context, tx pgx.Tx, tenantID, claimID uuid.UUID,
) ([]application.VersionRecord, error) {
	rows, err := sqlcgen.New(tx).ListClaimVersions(ctx, sqlcgen.ListClaimVersionsParams{
		TenantID: tenantID, ClaimID: claimID,
	})
	if err != nil {
		return nil, fmt.Errorf("claim: list versions: %w", err)
	}
	out := make([]application.VersionRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, versionOf(listVersionRow(row)))
	}
	return out, nil
}

// FreezeVersion implements application.Repository.
func (Repository) FreezeVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID,
	in application.FreezeRow,
) (bool, error) {
	affected, err := sqlcgen.New(tx).FreezeClaimVersion(ctx, sqlcgen.FreezeClaimVersionParams{
		TenantID: tenantID, ID: versionID, Snapshot: in.Snapshot,
		SubmittedAt: &in.SubmittedAt, ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		return false, fmt.Errorf("claim: freeze version: %w", err)
	}
	return affected == 1, nil
}

// SupersedeVersion implements application.Repository.
func (Repository) SupersedeVersion(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID,
	in application.ReturnRow,
) (bool, error) {
	affected, err := sqlcgen.New(tx).SupersedeClaimVersion(ctx, sqlcgen.SupersedeClaimVersionParams{
		TenantID: tenantID, ID: versionID, ReturnedAt: &in.ReturnedAt,
		ReturnReasonCode: &in.ReasonCode, ReturnReasonText: in.ReasonText,
		ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		return false, fmt.Errorf("claim: supersede version: %w", err)
	}
	return affected == 1, nil
}

// ReplaceLines implements application.Repository. The set is the unit: the old lines go and
// the new ones are written, in one statement pair inside the caller's transaction.
func (Repository) ReplaceLines(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID,
	rows []application.NewLineRow,
) error {
	q := sqlcgen.New(tx)
	if err := q.DeleteClaimLines(ctx, sqlcgen.DeleteClaimLinesParams{
		TenantID: tenantID, VersionID: versionID,
	}); err != nil {
		return fmt.Errorf("claim: delete lines: %w", err)
	}
	for _, row := range rows {
		if _, err := q.CreateClaimLine(ctx, sqlcgen.CreateClaimLineParams{
			TenantID: tenantID, VersionID: versionID,
			LineNo:              int32(row.LineNo), //nolint:gosec // bounded by domain.MaxLines
			ServiceDefinitionID: row.ServiceDefinitionID, UnitType: row.UnitType,
			Quantity: row.Quantity, UnitAmount: row.UnitAmount, LineAmount: row.LineAmount,
			CurrencyCode: row.CurrencyCode, DiagnosisID: optUUID(row.DiagnosisID),
			MedicalReportID: optUUID(row.MedicalReportID),
			PractitionerID:  optUUID(row.PractitionerID), Description: row.Description,
			ActorID: optUUID(row.ActorID),
		}); err != nil {
			return fmt.Errorf("claim: create line: %w", err)
		}
	}
	return nil
}

// ListLines implements application.Repository.
func (Repository) ListLines(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID,
) ([]application.LineRecord, error) {
	rows, err := sqlcgen.New(tx).ListClaimLines(ctx, sqlcgen.ListClaimLinesParams{
		TenantID: tenantID, VersionID: versionID,
	})
	if err != nil {
		return nil, fmt.Errorf("claim: list lines: %w", err)
	}
	out := make([]application.LineRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.LineRecord{
			ID: row.ID, VersionID: row.VersionID, LineNo: int(row.LineNo),
			ServiceDefinitionID: row.ServiceDefinitionID, ServiceCode: row.ServiceCode,
			UnitType: row.UnitType, Quantity: row.Quantity,
			UnitAmount: emptyToNil(row.UnitAmount), LineAmount: row.LineAmount,
			CurrencyCode: row.CurrencyCode, DiagnosisID: uuidPtr(row.DiagnosisID),
			MedicalReportID: uuidPtr(row.MedicalReportID),
			PractitionerID:  uuidPtr(row.PractitionerID), Description: row.Description,
			CreatedAt: row.CreatedAt, RowVersion: row.RowVersion,
		})
	}
	return out, nil
}

// CreateDecision implements application.Repository. The table is append-only, so this is the
// only way a decision is ever written and there is no update anywhere below.
func (Repository) CreateDecision(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewDecisionRow,
) (application.DecisionRecord, error) {
	row, err := sqlcgen.New(tx).CreateClaimLineDecision(ctx, sqlcgen.CreateClaimLineDecisionParams{
		TenantID: tenantID, LineID: in.LineID,
		DecidedInVersionNo: int32(in.DecidedInVersionNo), //nolint:gosec // a version number is bounded by the versions somebody made
		Decision:           in.Decision, ApprovedQuantity: in.ApprovedQuantity,
		ApprovedAmount: in.ApprovedAmount, ContractAmount: in.ContractAmount,
		PayerAmount: in.PayerAmount, MemberAmount: in.MemberAmount,
		ReasonCode: in.ReasonCode, ReasonText: in.ReasonText,
		DecidedBy: optUUID(in.DecidedBy), DecidedAt: in.DecidedAt, Stage: in.Stage,
	})
	if err != nil {
		return application.DecisionRecord{}, fmt.Errorf("claim: create line decision: %w", err)
	}
	return application.DecisionRecord{
		ID: row.ID, LineID: row.LineID, DecidedInVersionNo: int(row.DecidedInVersionNo),
		Decision: row.Decision, ApprovedQuantity: row.ApprovedQuantity,
		ApprovedAmount: row.ApprovedAmount, ContractAmount: emptyToNil(row.ContractAmount),
		PayerAmount: row.PayerAmount, MemberAmount: row.MemberAmount,
		ReasonCode: row.ReasonCode, ReasonText: row.ReasonText,
		DecidedBy: uuidPtr(row.DecidedBy), DecidedAt: row.DecidedAt, Stage: row.Stage,
	}, nil
}

// ListLatestDecisions implements application.Repository.
func (Repository) ListLatestDecisions(ctx context.Context, tx pgx.Tx, tenantID, versionID uuid.UUID,
) ([]application.DecisionRecord, error) {
	rows, err := sqlcgen.New(tx).ListLatestClaimLineDecisions(ctx,
		sqlcgen.ListLatestClaimLineDecisionsParams{TenantID: tenantID, VersionID: versionID})
	if err != nil {
		return nil, fmt.Errorf("claim: list line decisions: %w", err)
	}
	out := make([]application.DecisionRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.DecisionRecord{
			ID: row.ID, LineID: row.LineID, DecidedInVersionNo: int(row.DecidedInVersionNo),
			Decision: row.Decision, ApprovedQuantity: row.ApprovedQuantity,
			ApprovedAmount: row.ApprovedAmount, ContractAmount: emptyToNil(row.ContractAmount),
			PayerAmount: row.PayerAmount, MemberAmount: row.MemberAmount,
			ReasonCode: row.ReasonCode, ReasonText: row.ReasonText,
			DecidedBy: uuidPtr(row.DecidedBy), DecidedAt: row.DecidedAt, Stage: row.Stage,
		})
	}
	return out, nil
}

// CountDecisions implements application.Repository.
func (Repository) CountDecisions(ctx context.Context, tx pgx.Tx, tenantID, lineID uuid.UUID) (int, error) {
	n, err := sqlcgen.New(tx).CountClaimLineDecisionHistory(ctx,
		sqlcgen.CountClaimLineDecisionHistoryParams{TenantID: tenantID, LineID: lineID})
	if err != nil {
		return 0, fmt.Errorf("claim: count line decisions: %w", err)
	}
	return int(n), nil
}

// CreateAdjustment implements application.Repository.
func (Repository) CreateAdjustment(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewAdjustmentRow,
) (application.AdjustmentRecord, error) {
	row, err := sqlcgen.New(tx).CreateClaimAdjustment(ctx, sqlcgen.CreateClaimAdjustmentParams{
		TenantID: tenantID, ClaimID: in.ClaimID,
		VersionNo:      int32(in.VersionNo), //nolint:gosec // a version number is bounded by the versions somebody made
		AdjustmentType: in.AdjustmentType, Amount: in.Amount, CurrencyCode: in.CurrencyCode,
		ReasonCode: in.ReasonCode, ReasonText: in.ReasonText, ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		return application.AdjustmentRecord{}, fmt.Errorf("claim: create adjustment: %w", err)
	}
	return application.AdjustmentRecord{
		ID: row.ID, ClaimID: row.ClaimID, VersionNo: int(row.VersionNo),
		AdjustmentType: row.AdjustmentType, Amount: row.Amount, CurrencyCode: row.CurrencyCode,
		ReasonCode: row.ReasonCode, ReasonText: row.ReasonText,
		CreatedBy: uuidPtr(row.CreatedBy), CreatedAt: row.CreatedAt,
	}, nil
}

// ListAdjustments implements application.Repository.
func (Repository) ListAdjustments(ctx context.Context, tx pgx.Tx, tenantID, claimID uuid.UUID,
) ([]application.AdjustmentRecord, error) {
	rows, err := sqlcgen.New(tx).ListClaimAdjustments(ctx, sqlcgen.ListClaimAdjustmentsParams{
		TenantID: tenantID, ClaimID: claimID,
	})
	if err != nil {
		return nil, fmt.Errorf("claim: list adjustments: %w", err)
	}
	out := make([]application.AdjustmentRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.AdjustmentRecord{
			ID: row.ID, ClaimID: row.ClaimID, VersionNo: int(row.VersionNo),
			AdjustmentType: row.AdjustmentType, Amount: row.Amount,
			CurrencyCode: row.CurrencyCode, ReasonCode: row.ReasonCode,
			ReasonText: row.ReasonText, CreatedBy: uuidPtr(row.CreatedBy),
			CreatedAt: row.CreatedAt,
		})
	}
	return out, nil
}

// FindDuplicate implements application.Repository.
func (Repository) FindDuplicate(ctx context.Context, tx pgx.Tx, tenantID, claimID, personID,
	serviceDefinitionID uuid.UUID, from, to time.Time,
) (application.DuplicateRecord, bool, error) {
	row, err := sqlcgen.New(tx).FindDuplicateClaim(ctx, sqlcgen.FindDuplicateClaimParams{
		TenantID: tenantID, ClaimID: claimID, PersonID: personID,
		ServiceDefinitionID: serviceDefinitionID,
		ServiceDateFrom:     dateParam(from), ServiceDateTo: dateParam(to),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.DuplicateRecord{}, false, nil
	}
	if err != nil {
		return application.DuplicateRecord{}, false, fmt.Errorf("claim: find duplicate: %w", err)
	}
	return application.DuplicateRecord{ClaimID: row.ID, Reference: row.Reference}, true, nil
}

// ProviderHasTaxIdentity implements application.Repository.
func (Repository) ProviderHasTaxIdentity(ctx context.Context, tx pgx.Tx, tenantID, organizationID uuid.UUID) (bool, error) {
	ok, err := sqlcgen.New(tx).ProviderHasTaxIdentity(ctx, sqlcgen.ProviderHasTaxIdentityParams{
		TenantID: tenantID, TenantOrganizationID: organizationID,
	})
	if err != nil {
		return false, fmt.Errorf("claim: provider tax identity: %w", err)
	}
	return ok, nil
}

// CaseSensitivity implements application.Repository. A case that is not there answers the
// empty string, which the service reads as STANDARD.
func (Repository) CaseSensitivity(ctx context.Context, tx pgx.Tx, tenantID, caseID uuid.UUID) (string, error) {
	value, err := sqlcgen.New(tx).GetClaimCaseSensitivity(ctx, sqlcgen.GetClaimCaseSensitivityParams{
		TenantID: tenantID, CaseID: caseID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("claim: case sensitivity: %w", err)
	}
	return value, nil
}

// CaseOverAuthorization implements application.Repository.
func (Repository) CaseOverAuthorization(ctx context.Context, tx pgx.Tx, tenantID, caseID uuid.UUID) (bool, error) {
	over, err := sqlcgen.New(tx).ClaimCaseOverAuthorization(ctx,
		sqlcgen.ClaimCaseOverAuthorizationParams{TenantID: tenantID, CaseID: caseID})
	if err != nil {
		return false, fmt.Errorf("claim: case over authorization: %w", err)
	}
	return over, nil
}

// ProviderOrganizationExists implements application.Repository.
func (Repository) ProviderOrganizationExists(ctx context.Context, tx pgx.Tx, tenantID, orgID uuid.UUID) (bool, error) {
	ok, err := sqlcgen.New(tx).ClaimProviderOrganizationExists(ctx,
		sqlcgen.ClaimProviderOrganizationExistsParams{TenantID: tenantID, ID: orgID})
	if err != nil {
		return false, fmt.Errorf("claim: provider organization: %w", err)
	}
	return ok, nil
}

// ProviderProfile implements application.Repository.
func (Repository) ProviderProfile(ctx context.Context, tx pgx.Tx, tenantID, orgID uuid.UUID,
) (application.ProviderProfileRecord, error) {
	row, err := sqlcgen.New(tx).GetClaimProviderProfile(ctx, sqlcgen.GetClaimProviderProfileParams{
		TenantID: tenantID, TenantOrganizationID: orgID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ProviderProfileRecord{}, application.ErrProviderNoProfile
	}
	if err != nil {
		return application.ProviderProfileRecord{}, fmt.Errorf("claim: provider profile: %w", err)
	}
	return application.ProviderProfileRecord{
		ID: row.ID, TenantOrganizationID: row.TenantOrganizationID, Status: row.Status,
	}, nil
}

// GetEnrollment implements application.Repository.
func (Repository) GetEnrollment(ctx context.Context, tx pgx.Tx, tenantID, enrollmentID uuid.UUID,
) (application.EnrollmentRecord, error) {
	row, err := sqlcgen.New(tx).GetClaimEnrollment(ctx, sqlcgen.GetClaimEnrollmentParams{
		TenantID: tenantID, ID: enrollmentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.EnrollmentRecord{}, application.ErrEnrollmentMismatch
	}
	if err != nil {
		return application.EnrollmentRecord{}, fmt.Errorf("claim: get enrollment: %w", err)
	}
	return application.EnrollmentRecord{
		ID: row.ID, PlanID: row.PlanID, ProgramID: row.ProgramID, Status: row.Status,
		PersonID: row.PersonID,
	}, nil
}

// ResolveServiceDefinitions implements application.Repository.
func (Repository) ResolveServiceDefinitions(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	ids []uuid.UUID,
) ([]application.ServiceDefinitionRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := sqlcgen.New(tx).ResolveClaimServiceDefinitions(ctx,
		sqlcgen.ResolveClaimServiceDefinitionsParams{TenantID: tenantID, Ids: ids})
	if err != nil {
		return nil, fmt.Errorf("claim: resolve service definitions: %w", err)
	}
	out := make([]application.ServiceDefinitionRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.ServiceDefinitionRecord{
			ID: row.ID, Code: row.Code, Name: row.Name,
			DefaultUnitType: row.DefaultUnitType, Active: row.Active,
		})
	}
	return out, nil
}

// AccessPurposeExists implements application.Repository. It reads WP-I5-01's own reference
// table, because there is one authority for what an access purpose may be.
func (Repository) AccessPurposeExists(ctx context.Context, tx pgx.Tx, purpose string) (bool, error) {
	ok, err := sqlcgen.New(tx).ClinicalAccessPurposeExists(ctx, purpose)
	if err != nil {
		return false, fmt.Errorf("claim: access purpose: %w", err)
	}
	return ok, nil
}

// isUniqueViolation reports whether err is a unique violation of the named constraint.
func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == sqlStateUnique && pgErr.ConstraintName == constraint
}

// EntitlementCodes implements application.Repository.
func (Repository) EntitlementCodes(ctx context.Context, tx pgx.Tx, tenantID, enrollmentID uuid.UUID,
	serviceDate time.Time, ids []uuid.UUID,
) (map[uuid.UUID]string, error) {
	out := map[uuid.UUID]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := sqlcgen.New(tx).ResolveClaimEntitlementCodes(ctx,
		sqlcgen.ResolveClaimEntitlementCodesParams{
			TenantID: tenantID, EnrollmentID: enrollmentID,
			ServiceDate: dateParam(serviceDate), ServiceDefinitionIds: ids,
		})
	if err != nil {
		return nil, fmt.Errorf("claim: resolve entitlement codes: %w", err)
	}
	for _, row := range rows {
		out[row.ServiceDefinitionID] = row.EntitlementCode
	}
	return out, nil
}
