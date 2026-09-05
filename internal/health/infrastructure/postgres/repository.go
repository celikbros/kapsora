// Package healthpg implements the health repository with sqlc. It is stateless: every
// method takes the caller's tenant-bound transaction, so RLS is active for every statement
// and nothing here can read another tenant's cases.
//
// The provider boundary lives here rather than above: every read takes the caller's scope
// and hands it to SQL, so a row outside it is genuinely not returned. That is what lets the
// application layer answer 404 without ever having held the row.
//
// What this package deliberately does not do is decide what a caller may see. Every read
// returns the whole row — sensitivity, branch code, clinical notes — and the projection is
// applied one layer up. A repository that filtered as well would be a second rule about
// clinical visibility, and two rules is how one of them ends up wrong.
package healthpg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/health/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// Repository implements application.Repository.
type Repository struct{}

// New returns the repository.
func New() *Repository { return &Repository{} }

var _ application.Repository = (*Repository)(nil)

// CreateCase implements application.Repository.
func (Repository) CreateCase(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewCaseRow,
) (application.CaseRecord, error) {
	row, err := sqlcgen.New(tx).CreateHealthCase(ctx, sqlcgen.CreateHealthCaseParams{
		TenantID: tenantID, PersonID: in.PersonID, ProgramID: in.ProgramID,
		EnrollmentID: in.EnrollmentID, CaseType: in.CaseType,
		ProviderOrganizationID: optUUID(in.ProviderOrganizationID), OpenedAt: in.OpenedAt,
		ServiceRequestID: optUUID(in.ServiceRequestID), ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		return application.CaseRecord{}, fmt.Errorf("health: create case: %w", err)
	}
	return caseOf(createdCaseRow(row)), nil
}

// GetCase implements application.Repository.
func (Repository) GetCase(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.CaseRecord, error) {
	row, err := sqlcgen.New(tx).GetHealthCase(ctx, sqlcgen.GetHealthCaseParams{
		TenantID: tenantID, ID: id, ScopeIds: scope.OrganizationIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.CaseRecord{}, application.ErrCaseNotFound
	}
	if err != nil {
		return application.CaseRecord{}, fmt.Errorf("health: get case: %w", err)
	}
	return caseOf(fetchedCaseRow(row)), nil
}

// LockCase implements application.Repository.
func (Repository) LockCase(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.CaseRecord, error) {
	row, err := sqlcgen.New(tx).LockHealthCase(ctx, sqlcgen.LockHealthCaseParams{
		TenantID: tenantID, ID: id, ScopeIds: scope.OrganizationIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.CaseRecord{}, application.ErrCaseNotFound
	}
	if err != nil {
		return application.CaseRecord{}, fmt.Errorf("health: lock case: %w", err)
	}
	return caseOf(lockedCaseRow(row)), nil
}

// ListCases implements application.Repository.
func (Repository) ListCases(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.CaseQuery,
) ([]application.CaseRecord, error) {
	params := sqlcgen.ListHealthCasesParams{
		TenantID: tenantID, ScopeIds: q.Scope.OrganizationIDs,
		PersonID: optUUID(q.PersonID), ProgramID: optUUID(q.ProgramID),
		ProviderOrganizationID: optUUID(q.ProviderOrganizationID),
		Status:                 optionalString(q.Status), CaseType: optionalString(q.CaseType),
		OpenedFrom: q.OpenedFrom, OpenedTo: q.OpenedTo, PageSize: pageSize(q.PageSize),
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.AfterAt = &at
		params.AfterID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListHealthCases(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("health: list cases: %w", err)
	}
	out := make([]application.CaseRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, caseOf(listedCaseRow(row)))
	}
	return out, nil
}

// CloseCase implements application.Repository. The statement's predicate is the whole
// precondition, so "no row matched" is the answer to both "somebody else moved it" and
// "it was already closed"; the caller has read the row under FOR UPDATE and has already
// separated those two, so what is left here is the version.
func (Repository) CloseCase(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	closedAt time.Time, actorID *uuid.UUID, expected int64,
) error {
	affected, err := sqlcgen.New(tx).CloseHealthCase(ctx, sqlcgen.CloseHealthCaseParams{
		TenantID: tenantID, ID: id, ClosedAt: &closedAt, ActorID: optUUID(actorID),
		ExpectedRowVersion: expected,
	})
	if err != nil {
		return fmt.Errorf("health: close case: %w", err)
	}
	if affected == 0 {
		return application.ErrVersionMismatch
	}
	return nil
}

// SetCaseSensitivity implements application.Repository.
func (Repository) SetCaseSensitivity(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	sensitivity string, actorID *uuid.UUID,
) error {
	if err := (sqlcgen.New(tx)).SetHealthCaseSensitivity(ctx, sqlcgen.SetHealthCaseSensitivityParams{
		TenantID: tenantID, ID: id, Sensitivity: sensitivity, ActorID: optUUID(actorID),
	}); err != nil {
		return fmt.Errorf("health: set case sensitivity: %w", err)
	}
	return nil
}

// CountOpenEncounters implements application.Repository.
func (Repository) CountOpenEncounters(ctx context.Context, tx pgx.Tx, tenantID, caseID uuid.UUID) (int, error) {
	n, err := sqlcgen.New(tx).CountOpenEncounters(ctx, sqlcgen.CountOpenEncountersParams{
		TenantID: tenantID, CaseID: caseID,
	})
	if err != nil {
		return 0, fmt.Errorf("health: count open encounters: %w", err)
	}
	return int(n), nil
}

// CreateEncounter implements application.Repository.
func (Repository) CreateEncounter(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewEncounterRow,
) (application.EncounterRecord, error) {
	row, err := sqlcgen.New(tx).CreateEncounter(ctx, sqlcgen.CreateEncounterParams{
		TenantID: tenantID, CaseID: in.CaseID, EncounterType: in.EncounterType,
		StartedAt: in.StartedAt, EndedAt: in.EndedAt, LocationID: optUUID(in.LocationID),
		PractitionerID: optUUID(in.PractitionerID), BranchCode: in.BranchCode,
		NotesClinical: in.NotesClinical, ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		return application.EncounterRecord{}, fmt.Errorf("health: create encounter: %w", err)
	}
	return encounterOf(createdEncounterRow(row)), nil
}

// GetEncounter implements application.Repository.
func (Repository) GetEncounter(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.EncounterRecord, error) {
	row, err := sqlcgen.New(tx).GetEncounter(ctx, sqlcgen.GetEncounterParams{
		TenantID: tenantID, ID: id, ScopeIds: scope.OrganizationIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.EncounterRecord{}, application.ErrEncounterNotFound
	}
	if err != nil {
		return application.EncounterRecord{}, fmt.Errorf("health: get encounter: %w", err)
	}
	return encounterOf(fetchedEncounterRow(row)), nil
}

// ListCaseEncounters implements application.Repository.
func (Repository) ListCaseEncounters(ctx context.Context, tx pgx.Tx, tenantID, caseID uuid.UUID) (
	[]application.EncounterRecord, error,
) {
	rows, err := sqlcgen.New(tx).ListCaseEncounters(ctx, sqlcgen.ListCaseEncountersParams{
		TenantID: tenantID, CaseID: caseID,
	})
	if err != nil {
		return nil, fmt.Errorf("health: list case encounters: %w", err)
	}
	out := make([]application.EncounterRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, encounterOf(listedEncounterRow(row)))
	}
	return out, nil
}

// ListDiagnoses implements application.Repository.
func (Repository) ListDiagnoses(ctx context.Context, tx pgx.Tx, tenantID, encounterID uuid.UUID) (
	[]application.DiagnosisRecord, error,
) {
	rows, err := sqlcgen.New(tx).ListEncounterDiagnoses(ctx, sqlcgen.ListEncounterDiagnosesParams{
		TenantID: tenantID, EncounterID: encounterID,
	})
	if err != nil {
		return nil, fmt.Errorf("health: list diagnoses: %w", err)
	}
	out := make([]application.DiagnosisRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, diagnosisOf(row))
	}
	return out, nil
}

// ReplaceDiagnoses implements application.Repository. The delete and the inserts run in the
// caller's transaction, so an encounter is never momentarily without its diagnoses as far
// as any other reader is concerned.
func (Repository) ReplaceDiagnoses(ctx context.Context, tx pgx.Tx, tenantID, encounterID uuid.UUID,
	rows []application.NewDiagnosisRow,
) error {
	q := sqlcgen.New(tx)
	if err := q.DeleteEncounterDiagnoses(ctx, sqlcgen.DeleteEncounterDiagnosesParams{
		TenantID: tenantID, EncounterID: encounterID,
	}); err != nil {
		return fmt.Errorf("health: delete diagnoses: %w", err)
	}
	for _, row := range rows {
		if _, err := q.CreateDiagnosis(ctx, sqlcgen.CreateDiagnosisParams{
			TenantID: tenantID, EncounterID: row.EncounterID, CodeSystemID: row.CodeSystemID,
			CodeValueID: row.CodeValueID, DiagnosisType: row.DiagnosisType,
			Sensitive: row.Sensitive, RecordedAt: row.RecordedAt, ActorID: optUUID(row.ActorID),
		}); err != nil {
			return fmt.Errorf("health: create diagnosis: %w", err)
		}
	}
	return nil
}

// CaseHasSensitiveDiagnosis implements application.Repository.
func (Repository) CaseHasSensitiveDiagnosis(ctx context.Context, tx pgx.Tx, tenantID, caseID uuid.UUID) (bool, error) {
	ok, err := sqlcgen.New(tx).CaseHasSensitiveDiagnosis(ctx, sqlcgen.CaseHasSensitiveDiagnosisParams{
		TenantID: tenantID, CaseID: caseID,
	})
	if err != nil {
		return false, fmt.Errorf("health: case has sensitive diagnosis: %w", err)
	}
	return ok, nil
}

// ResolveCodeValues implements application.Repository.
func (Repository) ResolveCodeValues(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	ids []uuid.UUID,
) ([]application.CodeValueRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := sqlcgen.New(tx).ResolveDiagnosisCodeValues(ctx, sqlcgen.ResolveDiagnosisCodeValuesParams{
		TenantID: tenantID, Ids: ids,
	})
	if err != nil {
		return nil, fmt.Errorf("health: resolve code values: %w", err)
	}
	out := make([]application.CodeValueRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.CodeValueRecord{
			ID: row.ID, CodeSystemID: row.CodeSystemID, CodeSystemCode: row.CodeSystemCode,
			Code: row.Code, Display: row.Display, Active: row.Active, Sensitive: row.Sensitive,
		})
	}
	return out, nil
}

// GetEnrollment implements application.Repository.
func (Repository) GetEnrollment(ctx context.Context, tx pgx.Tx, tenantID, enrollmentID uuid.UUID) (
	application.EnrollmentRecord, error,
) {
	row, err := sqlcgen.New(tx).GetHealthCaseEnrollment(ctx, sqlcgen.GetHealthCaseEnrollmentParams{
		TenantID: tenantID, ID: enrollmentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.EnrollmentRecord{}, application.ErrEnrollmentMismatch
	}
	if err != nil {
		return application.EnrollmentRecord{}, fmt.Errorf("health: get enrollment: %w", err)
	}
	return application.EnrollmentRecord{
		ID: row.ID, PlanID: row.PlanID, ProgramID: row.ProgramID,
		Status: row.Status, PersonID: row.PersonID,
	}, nil
}

// GetServiceRequest implements application.Repository.
func (Repository) GetServiceRequest(ctx context.Context, tx pgx.Tx, tenantID, requestID uuid.UUID) (
	application.ServiceRequestRecord, error,
) {
	row, err := sqlcgen.New(tx).GetHealthCaseServiceRequest(ctx, sqlcgen.GetHealthCaseServiceRequestParams{
		TenantID: tenantID, ID: requestID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ServiceRequestRecord{}, application.ErrRequestNotFound
	}
	if err != nil {
		return application.ServiceRequestRecord{}, fmt.Errorf("health: get service request: %w", err)
	}
	return application.ServiceRequestRecord{
		ID: row.ID, PersonID: row.PersonID, ProgramID: row.ProgramID,
		EnrollmentID: row.EnrollmentID, RequestType: row.RequestType,
		HasHealthService: row.HasHealthService,
	}, nil
}

// ProviderOrganizationExists implements application.Repository.
func (Repository) ProviderOrganizationExists(ctx context.Context, tx pgx.Tx, tenantID, orgID uuid.UUID) (bool, error) {
	ok, err := sqlcgen.New(tx).HealthProviderOrganizationExists(ctx,
		sqlcgen.HealthProviderOrganizationExistsParams{TenantID: tenantID, ID: orgID})
	if err != nil {
		return false, fmt.Errorf("health: provider organization exists: %w", err)
	}
	return ok, nil
}

// AccessPurposeExists implements application.Repository.
func (Repository) AccessPurposeExists(ctx context.Context, tx pgx.Tx, purpose string) (bool, error) {
	ok, err := sqlcgen.New(tx).ClinicalAccessPurposeExists(ctx, purpose)
	if err != nil {
		return false, fmt.Errorf("health: clinical access purpose exists: %w", err)
	}
	return ok, nil
}

// ListAccessEvents implements application.Repository.
func (Repository) ListAccessEvents(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.AccessEventQuery,
) ([]application.AccessEventRecord, error) {
	params := sqlcgen.ListHealthAccessEventsParams{
		TenantID: tenantID, PersonID: optUUID(q.PersonID), PageSize: pageSize(q.PageSize),
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.AfterAt = &at
		params.AfterID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListHealthAccessEvents(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("health: list access events: %w", err)
	}
	out := make([]application.AccessEventRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, accessEventOf(row))
	}
	return out, nil
}
