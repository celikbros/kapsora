// Package healthpg's treatment report half. It is the same shape as the case repository
// above it and for the same reasons: stateless, every method inside the caller's
// tenant-bound transaction, the provider boundary applied in SQL so a report outside it is
// genuinely absent rather than fetched and then hidden — and no decision anywhere about what
// a caller may see. Every read returns the whole row, clinical summary and review comment
// included, and the projection is applied one layer up.
package healthpg

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/health/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// ReportRepository implements application.ReportRepository.
type ReportRepository struct{}

// NewReports returns the treatment report repository.
func NewReports() *ReportRepository { return &ReportRepository{} }

var _ application.ReportRepository = (*ReportRepository)(nil)

// uniqueViolation is the SQLSTATE the partial unique index raises when a chain would end up
// with two approved versions. It is unreachable through the ordinary path — the approval
// supersedes its predecessor in the same transaction — so it exists to make a race an answer
// rather than a constraint violation in a log.
const uniqueViolation = "23505"

// CreateReport implements application.ReportRepository.
func (ReportRepository) CreateReport(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewReportRow,
) (application.ReportRecord, error) {
	row, err := sqlcgen.New(tx).CreateMedicalReport(ctx, sqlcgen.CreateMedicalReportParams{
		ID: in.ID, TenantID: tenantID, PersonID: in.PersonID, CaseID: optUUID(in.CaseID),
		Reference: in.Reference, VersionNo: versionNo(in.VersionNo), RootReportID: in.RootReportID,
		SupersedesReportID: optUUID(in.SupersedesReportID), ReportType: in.ReportType,
		ReportSubtype:                 in.ReportSubtype,
		IssuingPractitionerID:         optUUID(in.IssuingPractitionerID),
		IssuingProviderOrganizationID: optUUID(in.IssuingProviderOrganizationID),
		IssuedAt:                      date(in.IssuedAt), ValidFrom: date(in.ValidFrom), ValidTo: date(in.ValidTo),
		ClinicalSummary: in.ClinicalSummary, ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		return application.ReportRecord{}, fmt.Errorf("health: create medical report: %w", err)
	}
	return application.ReportRecord{
		ID: row.ID, PersonID: row.PersonID, CaseID: uuidPtr(row.CaseID), Reference: row.Reference,
		VersionNo: int(row.VersionNo), RootReportID: row.RootReportID,
		SupersedesReportID: uuidPtr(row.SupersedesReportID), ReportType: row.ReportType,
		ReportSubtype:                 row.ReportSubtype,
		IssuingPractitionerID:         uuidPtr(row.IssuingPractitionerID),
		IssuingProviderOrganizationID: uuidPtr(row.IssuingProviderOrganizationID),
		IssuedAt:                      dateValue(row.IssuedAt), ValidFrom: dateValue(row.ValidFrom),
		ValidTo: dateValue(row.ValidTo), Status: row.Status, ClinicalSummary: row.ClinicalSummary,
		ReviewComment: row.ReviewComment, RejectReasonCode: row.RejectReasonCode,
		ReviewedBy: uuidPtr(row.ReviewedBy), ReviewedAt: row.ReviewedAt,
		SubmittedAt: row.SubmittedAt, SubmittedBy: uuidPtr(row.SubmittedBy),
		CreatedAt: row.CreatedAt, RowVersion: row.RowVersion,
		// The insert does not join the case, so the sensitivity is not known here. Nothing
		// builds a view from this record: the create command reads the report back through
		// GetReport, which does join it. A record that carried the wrong sensitivity would
		// be a record the projection decided wrongly about.
		CaseSensitivity: "",
	}, nil
}

// GetReport implements application.ReportRepository.
func (ReportRepository) GetReport(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.ReportRecord, error) {
	row, err := sqlcgen.New(tx).GetMedicalReport(ctx, sqlcgen.GetMedicalReportParams{
		TenantID: tenantID, ID: id, ScopeIds: scope.OrganizationIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ReportRecord{}, application.ErrReportNotFound
	}
	if err != nil {
		return application.ReportRecord{}, fmt.Errorf("health: get medical report: %w", err)
	}
	return reportOf(fetchedReportRow(row)), nil
}

// LockReport implements application.ReportRepository.
func (ReportRepository) LockReport(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.ReportRecord, error) {
	row, err := sqlcgen.New(tx).LockMedicalReport(ctx, sqlcgen.LockMedicalReportParams{
		TenantID: tenantID, ID: id, ScopeIds: scope.OrganizationIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ReportRecord{}, application.ErrReportNotFound
	}
	if err != nil {
		return application.ReportRecord{}, fmt.Errorf("health: lock medical report: %w", err)
	}
	return reportOf(lockedReportRow(row)), nil
}

// ListReports implements application.ReportRepository.
func (ReportRepository) ListReports(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.ReportQuery,
) ([]application.ReportRecord, error) {
	params := sqlcgen.ListMedicalReportsParams{
		TenantID: tenantID, ScopeIds: q.Scope.OrganizationIDs,
		PersonID: optUUID(q.PersonID), CaseID: optUUID(q.CaseID),
		RootReportID: optUUID(q.RootReportID), ProviderOrganizationID: optUUID(q.ProviderOrganizationID),
		Status: optionalString(q.Status), ReportType: optionalString(q.ReportType),
		PageSize: pageSize(q.PageSize),
	}
	if q.ValidOn != nil {
		params.ValidOn = date(*q.ValidOn)
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.AfterAt = &at
		params.AfterID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListMedicalReports(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("health: list medical reports: %w", err)
	}
	out := make([]application.ReportRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, reportOf(listedReportRow(row)))
	}
	return out, nil
}

// ListReportChain implements application.ReportRepository.
func (ReportRepository) ListReportChain(ctx context.Context, tx pgx.Tx, tenantID, rootID uuid.UUID) (
	[]application.ReportRecord, error,
) {
	rows, err := sqlcgen.New(tx).ListMedicalReportChain(ctx, sqlcgen.ListMedicalReportChainParams{
		TenantID: tenantID, RootReportID: rootID,
	})
	if err != nil {
		return nil, fmt.Errorf("health: list medical report chain: %w", err)
	}
	out := make([]application.ReportRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, reportOf(chainReportRow(row)))
	}
	return out, nil
}

// PatchReportDraft implements application.ReportRepository.
func (ReportRepository) PatchReportDraft(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	in application.PatchReportRow, expected int64,
) error {
	affected, err := sqlcgen.New(tx).UpdateMedicalReportDraft(ctx, sqlcgen.UpdateMedicalReportDraftParams{
		CaseID: optUUID(in.CaseID), ReportType: in.ReportType, ReportSubtype: in.ReportSubtype,
		IssuingPractitionerID:         optUUID(in.IssuingPractitionerID),
		IssuingProviderOrganizationID: optUUID(in.IssuingProviderOrganizationID),
		IssuedAt:                      date(in.IssuedAt), ValidFrom: date(in.ValidFrom), ValidTo: date(in.ValidTo),
		ClinicalSummary: in.ClinicalSummary, ActorID: optUUID(in.ActorID),
		TenantID: tenantID, ID: id, ExpectedRowVersion: expected,
	})
	if err != nil {
		return fmt.Errorf("health: patch medical report draft: %w", err)
	}
	return moved(affected)
}

// TouchReportDraft implements application.ReportRepository.
func (ReportRepository) TouchReportDraft(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	actorID *uuid.UUID, expected int64,
) error {
	affected, err := sqlcgen.New(tx).TouchMedicalReport(ctx, sqlcgen.TouchMedicalReportParams{
		TenantID: tenantID, ID: id, ActorID: optUUID(actorID), ExpectedRowVersion: expected,
	})
	if err != nil {
		return fmt.Errorf("health: touch medical report: %w", err)
	}
	return moved(affected)
}

// MarkReportSubmitted implements application.ReportRepository.
func (ReportRepository) MarkReportSubmitted(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	submittedAt time.Time, actorID *uuid.UUID, expected int64,
) error {
	affected, err := sqlcgen.New(tx).MarkMedicalReportSubmitted(ctx, sqlcgen.MarkMedicalReportSubmittedParams{
		SubmittedAt: &submittedAt, ActorID: optUUID(actorID), TenantID: tenantID, ID: id,
		ExpectedRowVersion: expected,
	})
	if err != nil {
		return fmt.Errorf("health: submit medical report: %w", err)
	}
	return moved(affected)
}

// MarkReportUnderReview implements application.ReportRepository.
func (ReportRepository) MarkReportUnderReview(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	actorID *uuid.UUID,
) (bool, error) {
	affected, err := sqlcgen.New(tx).MarkMedicalReportUnderReview(ctx, sqlcgen.MarkMedicalReportUnderReviewParams{
		ActorID: optUUID(actorID), TenantID: tenantID, ID: id,
	})
	if err != nil {
		return false, fmt.Errorf("health: start medical report review: %w", err)
	}
	return affected > 0, nil
}

// MarkReportApproved implements application.ReportRepository.
func (ReportRepository) MarkReportApproved(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	in application.ReportDecisionRow,
) error {
	reviewedAt := in.ReviewedAt
	affected, err := sqlcgen.New(tx).MarkMedicalReportApproved(ctx, sqlcgen.MarkMedicalReportApprovedParams{
		ReviewComment: in.ReviewComment, ReviewedAt: &reviewedAt,
		ReviewedBy: uuid.NullUUID{UUID: in.ReviewedBy, Valid: true},
		TenantID:   tenantID, ID: id, ExpectedRowVersion: in.Expected,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return application.ErrReportChainApproved
		}
		return fmt.Errorf("health: approve medical report: %w", err)
	}
	return moved(affected)
}

// MarkReportRejected implements application.ReportRepository.
func (ReportRepository) MarkReportRejected(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	in application.ReportDecisionRow,
) error {
	reviewedAt := in.ReviewedAt
	reason := in.RejectReasonCode
	affected, err := sqlcgen.New(tx).MarkMedicalReportRejected(ctx, sqlcgen.MarkMedicalReportRejectedParams{
		ReviewComment: in.ReviewComment, RejectReasonCode: &reason, ReviewedAt: &reviewedAt,
		ReviewedBy: uuid.NullUUID{UUID: in.ReviewedBy, Valid: true},
		TenantID:   tenantID, ID: id, ExpectedRowVersion: in.Expected,
	})
	if err != nil {
		return fmt.Errorf("health: reject medical report: %w", err)
	}
	return moved(affected)
}

// MarkReportCancelled implements application.ReportRepository.
func (ReportRepository) MarkReportCancelled(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	actorID *uuid.UUID, expected int64,
) error {
	affected, err := sqlcgen.New(tx).MarkMedicalReportCancelled(ctx, sqlcgen.MarkMedicalReportCancelledParams{
		ActorID: optUUID(actorID), TenantID: tenantID, ID: id, ExpectedRowVersion: expected,
	})
	if err != nil {
		return fmt.Errorf("health: cancel medical report: %w", err)
	}
	return moved(affected)
}

// SupersedeReport implements application.ReportRepository.
func (ReportRepository) SupersedeReport(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (bool, error) {
	affected, err := sqlcgen.New(tx).SupersedeMedicalReport(ctx, sqlcgen.SupersedeMedicalReportParams{
		TenantID: tenantID, ID: id,
	})
	if err != nil {
		return false, fmt.Errorf("health: supersede medical report: %w", err)
	}
	return affected > 0, nil
}

// ListExpirableReports implements application.ReportRepository.
func (ReportRepository) ListExpirableReports(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	asOf time.Time, limit int,
) ([]uuid.UUID, error) {
	ids, err := sqlcgen.New(tx).ListExpirableMedicalReports(ctx, sqlcgen.ListExpirableMedicalReportsParams{
		TenantID: tenantID, AsOf: date(asOf), PageSize: pageSize(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("health: list expirable medical reports: %w", err)
	}
	return ids, nil
}

// MarkReportExpired implements application.ReportRepository.
func (ReportRepository) MarkReportExpired(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (bool, error) {
	affected, err := sqlcgen.New(tx).MarkMedicalReportExpired(ctx, sqlcgen.MarkMedicalReportExpiredParams{
		TenantID: tenantID, ID: id,
	})
	if err != nil {
		return false, fmt.Errorf("health: expire medical report: %w", err)
	}
	return affected > 0, nil
}

// ListReportServices implements application.ReportRepository.
func (ReportRepository) ListReportServices(ctx context.Context, tx pgx.Tx, tenantID, reportID uuid.UUID) (
	[]application.ReportServiceRecord, error,
) {
	rows, err := sqlcgen.New(tx).ListMedicalReportServices(ctx, sqlcgen.ListMedicalReportServicesParams{
		TenantID: tenantID, ReportID: reportID,
	})
	if err != nil {
		return nil, fmt.Errorf("health: list medical report services: %w", err)
	}
	out := make([]application.ReportServiceRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.ReportServiceRecord{
			ID: row.ID, ReportID: row.ReportID, ServiceDefinitionID: row.ServiceDefinitionID,
			ServiceCode: row.ServiceCode, ServiceName: row.ServiceName,
			CoveredQuantity: optionalString(row.CoveredQuantity),
			CoveredAmount:   optionalString(row.CoveredAmount),
			CurrencyCode:    row.CurrencyCode, Notes: row.Notes, RowVersion: row.RowVersion,
		})
	}
	return out, nil
}

// ReplaceReportServices implements application.ReportRepository. The set is the unit: the
// whole set is deleted and the new one written in the same statement pair, so a replacement
// that failed halfway leaves the report with the lines it had.
func (ReportRepository) ReplaceReportServices(ctx context.Context, tx pgx.Tx, tenantID, reportID uuid.UUID,
	rows []application.NewReportServiceRow,
) error {
	q := sqlcgen.New(tx)
	if _, err := q.DeleteMedicalReportServices(ctx, sqlcgen.DeleteMedicalReportServicesParams{
		TenantID: tenantID, ReportID: reportID,
	}); err != nil {
		return fmt.Errorf("health: delete medical report services: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	params := make([]sqlcgen.CreateMedicalReportServiceParams, 0, len(rows))
	for _, row := range rows {
		params = append(params, sqlcgen.CreateMedicalReportServiceParams{
			TenantID: tenantID, ReportID: reportID, ServiceDefinitionID: row.ServiceDefinitionID,
			CoveredQuantity: row.CoveredQuantity, CoveredAmount: row.CoveredAmount,
			CurrencyCode: row.CurrencyCode, Notes: row.Notes, ActorID: optUUID(row.ActorID),
		})
	}
	if err := execBatch(q.CreateMedicalReportService(ctx, params)); err != nil {
		return fmt.Errorf("health: create medical report services: %w", err)
	}
	return nil
}

// CountReportServices implements application.ReportRepository.
func (ReportRepository) CountReportServices(ctx context.Context, tx pgx.Tx, tenantID, reportID uuid.UUID) (int, error) {
	n, err := sqlcgen.New(tx).CountMedicalReportServices(ctx, sqlcgen.CountMedicalReportServicesParams{
		TenantID: tenantID, ReportID: reportID,
	})
	if err != nil {
		return 0, fmt.Errorf("health: count medical report services: %w", err)
	}
	return int(n), nil
}

// ListReportDocuments implements application.ReportRepository.
func (ReportRepository) ListReportDocuments(ctx context.Context, tx pgx.Tx, tenantID, reportID uuid.UUID) (
	[]application.ReportDocumentRecord, error,
) {
	rows, err := sqlcgen.New(tx).ListMedicalReportDocuments(ctx, sqlcgen.ListMedicalReportDocumentsParams{
		TenantID: tenantID, ReportID: reportID,
	})
	if err != nil {
		return nil, fmt.Errorf("health: list medical report documents: %w", err)
	}
	out := make([]application.ReportDocumentRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.ReportDocumentRecord{
			ID: row.ID, ObjectID: row.ObjectID, DocumentTypeCode: row.DocumentTypeCode,
			Purpose: row.Purpose, RequiredPermission: row.RequiredPermission,
			OriginalFilename: row.OriginalFilename, ContentType: row.ContentType,
			ScanStatus: row.ScanStatus, Classification: row.Classification, CreatedAt: row.CreatedAt,
		})
	}
	return out, nil
}

// CountCleanReportDocuments implements application.ReportRepository.
func (ReportRepository) CountCleanReportDocuments(ctx context.Context, tx pgx.Tx, tenantID, reportID uuid.UUID,
	reportType string,
) (int, error) {
	n, err := sqlcgen.New(tx).CountCleanMedicalReportDocuments(ctx, sqlcgen.CountCleanMedicalReportDocumentsParams{
		TenantID: tenantID, ReportID: reportID, ReportType: reportType,
	})
	if err != nil {
		return 0, fmt.Errorf("health: count clean medical report documents: %w", err)
	}
	return int(n), nil
}

// CreateReportUsage implements application.ReportRepository.
func (ReportRepository) CreateReportUsage(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewReportUsageRow,
) (application.ReportUsageRecord, error) {
	row, err := sqlcgen.New(tx).CreateMedicalReportUsage(ctx, sqlcgen.CreateMedicalReportUsageParams{
		TenantID: tenantID, ReportID: in.ReportID, UsedByType: in.UsedByType,
		UsedByID: in.UsedByID, UsedAt: in.UsedAt, ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		return application.ReportUsageRecord{}, fmt.Errorf("health: create medical report usage: %w", err)
	}
	return application.ReportUsageRecord{
		ID: row.ID, ReportID: row.ReportID, UsedByType: row.UsedByType,
		UsedByID: row.UsedByID, UsedAt: row.UsedAt,
	}, nil
}

// ListReportUsages implements application.ReportRepository.
func (ReportRepository) ListReportUsages(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.UsageQuery,
) ([]application.ReportUsageRecord, error) {
	params := sqlcgen.ListMedicalReportUsagesParams{
		TenantID: tenantID, ReportID: q.ReportID, PageSize: pageSize(q.PageSize),
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.AfterAt = &at
		params.AfterID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListMedicalReportUsages(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("health: list medical report usages: %w", err)
	}
	out := make([]application.ReportUsageRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.ReportUsageRecord{
			ID: row.ID, ReportID: row.ReportID, UsedByType: row.UsedByType,
			UsedByID: row.UsedByID, UsedAt: row.UsedAt,
		})
	}
	return out, nil
}

// ReportCoverageRow implements application.ReportRepository.
func (ReportRepository) ReportCoverageRow(ctx context.Context, tx pgx.Tx, tenantID, reportID,
	serviceDefinitionID uuid.UUID,
) (application.CoverageRow, error) {
	row, err := sqlcgen.New(tx).GetMedicalReportCoverage(ctx, sqlcgen.GetMedicalReportCoverageParams{
		TenantID: tenantID, ReportID: reportID, ServiceDefinitionID: serviceDefinitionID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.CoverageRow{}, application.ErrReportNotFound
	}
	if err != nil {
		return application.CoverageRow{}, fmt.Errorf("health: medical report coverage: %w", err)
	}
	return application.CoverageRow{
		ReportID: row.ID, Reference: row.Reference, VersionNo: int(row.VersionNo),
		RootReportID: row.RootReportID, PersonID: row.PersonID, Status: row.Status,
		ValidFrom: dateValue(row.ValidFrom), ValidTo: dateValue(row.ValidTo),
		HasServiceLine:  row.ServiceLineID.Valid,
		CoveredQuantity: optionalString(row.CoveredQuantity),
		CoveredAmount:   optionalString(row.CoveredAmount),
		CurrencyCode:    row.CurrencyCode,
	}, nil
}

// GetReportServiceDefinition implements application.ReportRepository.
func (ReportRepository) GetReportServiceDefinition(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (
	application.ServiceDefinitionRecord, error,
) {
	row, err := sqlcgen.New(tx).GetMedicalReportServiceDefinition(ctx,
		sqlcgen.GetMedicalReportServiceDefinitionParams{TenantID: tenantID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ServiceDefinitionRecord{}, application.ErrReportServiceUnknown
	}
	if err != nil {
		return application.ServiceDefinitionRecord{}, fmt.Errorf("health: get service definition: %w", err)
	}
	return application.ServiceDefinitionRecord{
		ID: row.ID, Code: row.Code, Name: row.Name, Active: row.Active,
	}, nil
}

// GetReportCase implements application.ReportRepository.
func (ReportRepository) GetReportCase(ctx context.Context, tx pgx.Tx, tenantID, caseID uuid.UUID) (
	application.CaseSummary, error,
) {
	row, err := sqlcgen.New(tx).GetMedicalReportPerson(ctx, sqlcgen.GetMedicalReportPersonParams{
		TenantID: tenantID, ID: caseID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.CaseSummary{}, application.ErrReportCaseNotFound
	}
	if err != nil {
		return application.CaseSummary{}, fmt.Errorf("health: get report case: %w", err)
	}
	return application.CaseSummary{
		ID: row.ID, PersonID: row.PersonID, Status: row.Status, Sensitivity: row.Sensitivity,
	}, nil
}

// ActiveTenants implements application.ReportRepository. It runs outside a tenant context,
// which is why the expiry job opens a transaction of its own for it.
func (ReportRepository) ActiveTenants(ctx context.Context, tx pgx.Tx) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx,
		`SELECT id FROM platform.tenant WHERE status IN ('ACTIVE','SUSPENDED') ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("health: list active tenants: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("health: scan tenant: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// moved turns "no row matched" into the version error the transport answers 412 with. Every
// command has already read its row under FOR UPDATE and separated "not found" and "wrong
// status" from it, so what is left here is genuinely the version.
func moved(affected int64) error {
	if affected == 0 {
		return application.ErrVersionMismatch
	}
	return nil
}

// versionNo narrows a version number to the int32 the column is. A chain that had reached
// two billion versions would be a chain nobody could read, so the clamp is a formality; it
// exists so the conversion is not a silent wrap.
func versionNo(n int) int32 {
	if n < 1 {
		return 1
	}
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(n)
}

func date(t time.Time) pgtype.Date {
	if t.IsZero() {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: t, Valid: true}
}

func dateValue(d pgtype.Date) time.Time {
	if !d.Valid {
		return time.Time{}
	}
	return d.Time
}

type batchExecutor interface {
	Exec(func(int, error))
	Close() error
}

func execBatch(batch batchExecutor) error {
	var firstErr error
	batch.Exec(func(_ int, err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	})
	if err := batch.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}
