package claimpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/claim/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// ListCaseSources implements the scoped financial candidate list.
func (Repository) ListCaseSources(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, q application.CaseSourceQuery) ([]application.CaseSourceSummary, error) {
	params := sqlcgen.ListClaimSourcesParams{TenantID: tenant, ScopeIds: q.Scope.OrganizationIDs, Now: q.Now, PageSize: pageSize(q.PageSize)}
	if q.After != nil {
		params.AfterID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
		params.AfterTime = &q.After.CreatedAt
	}
	rows, err := sqlcgen.New(tx).ListClaimSources(ctx, params)
	if err != nil {
		return nil, err
	}
	out := make([]application.CaseSourceSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, application.CaseSourceSummary{ID: r.ID, OpenedAt: r.OpenedAt, RowVersion: r.RowVersion, ServiceDate: r.ServiceDate.Time, RequestReference: r.RequestReference, PersonDisplayName: r.PersonDisplayName})
	}
	return out, nil
}

func sourceIDs(value any) ([]uuid.UUID, error) {
	// sqlc infers JSON aggregate expressions as interface{}; pgx decodes JSON values.
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if bytes, ok := value.([]byte); ok {
		raw = bytes
	}
	var ids []uuid.UUID
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil, fmt.Errorf("claim: decode source references: %w", err)
	}
	return ids, nil
}

// LockCaseSource locks the source before duplicate detection and creation.
func (Repository) LockCaseSource(ctx context.Context, tx pgx.Tx, tenant, id uuid.UUID, scope application.Scope, now time.Time) (application.CaseSource, error) {
	r, err := sqlcgen.New(tx).LockClaimSource(ctx, sqlcgen.LockClaimSourceParams{TenantID: tenant, ID: id, ScopeIds: scope.OrganizationIDs, Now: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.CaseSource{}, application.ErrSourceNotFound
	}
	if err != nil {
		return application.CaseSource{}, err
	}
	r.AlreadyRaised, err = sqlcgen.New(tx).ClaimSourceAlreadyRaised(ctx, sqlcgen.ClaimSourceAlreadyRaisedParams{TenantID: tenant, CaseID: uuid.NullUUID{UUID: id, Valid: true}})
	if err != nil {
		return application.CaseSource{}, err
	}
	auth, err := sourceIDs(r.AuthorizationIds)
	if err != nil {
		return application.CaseSource{}, err
	}
	diagnoses, err := sourceIDs(r.DiagnosisIds)
	if err != nil {
		return application.CaseSource{}, err
	}
	return application.CaseSource{CaseSourceSummary: application.CaseSourceSummary{ID: r.ID, OpenedAt: r.OpenedAt, RowVersion: r.RowVersion, ServiceDate: r.ServiceDate.Time, RequestReference: r.RequestReference, PersonDisplayName: r.PersonDisplayName},
		PersonID: r.PersonID, ProgramID: r.ProgramID, EnrollmentID: r.EnrollmentID, ProviderID: r.ProviderOrganizationID.UUID,
		AuthorizationIDs: auth, DiagnosisIDs: diagnoses, Ongoing: r.Ongoing, AlreadyRaised: r.AlreadyRaised}, nil
}

// CaseSourceLines keeps clinical associations inside the repository/application boundary.
func (Repository) CaseSourceLines(ctx context.Context, tx pgx.Tx, tenant uuid.UUID, source application.CaseSource) ([]application.CaseSourceLine, error) {
	rows, err := sqlcgen.New(tx).ClaimSourceLines(ctx, sqlcgen.ClaimSourceLinesParams{TenantID: tenant, CaseID: uuid.NullUUID{UUID: source.ID, Valid: true}, PersonID: source.PersonID, ProviderID: uuid.NullUUID{UUID: source.ProviderID, Valid: true}, ServiceDate: dateParam(source.ServiceDate), AuthorizationID: source.AuthorizationIDs[0]})
	if err != nil {
		return nil, err
	}
	out := make([]application.CaseSourceLine, 0, len(rows))
	for _, r := range rows {
		reports, err := sourceIDs(r.ReportIds)
		if err != nil {
			return nil, err
		}
		out = append(out, application.CaseSourceLine{ServiceID: r.ServiceDefinitionID, Code: r.ServiceCode, Name: r.ServiceName, UnitType: r.UnitType, Quantity: r.Quantity, ReportIDs: reports})
	}
	return out, nil
}
