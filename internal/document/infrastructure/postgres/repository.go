// Package documentpg implements the document repository with sqlc. It is stateless: every
// method takes the caller's tenant-bound transaction, so RLS is active for every statement
// and nothing here can read another tenant's documents.
//
// The provider boundary lives here rather than above: every read takes the caller's scope
// and hands it to SQL, so a row outside it is genuinely not returned. That is what lets the
// application layer answer 404 without ever having held the row.
//
// One method deliberately reports a boolean rather than an error. Promote runs the one
// statement in the schema that writes bucket = 'secure', and "another object already stores
// these bytes" is an outcome the caller has to act on — by pointing this object at that one
// — rather than a failure this layer can name.
package documentpg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/celikbros/kapsora/internal/document/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// PostgreSQL error codes and the constraint names this package maps to named errors.
const (
	uniqueViolation = "23505"

	constraintLinkTarget = "uq_document_link_target"
	constraintDigest     = "uq_document_object_sha256_clean"
	// The three partial unique indexes over the active holds; any of them means the same
	// thing to a caller: something already holds that target.
	constraintHoldObject    = "uq_document_legal_hold_active_object"
	constraintHoldPerson    = "uq_document_legal_hold_active_person"
	constraintHoldAggregate = "uq_document_legal_hold_active_aggregate"
)

// Repository implements application.Repository.
type Repository struct{}

// New returns the repository.
func New() *Repository { return &Repository{} }

var _ application.Repository = (*Repository)(nil)

// CreateObject implements application.Repository.
func (Repository) CreateObject(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewObjectRow,
) (application.ObjectRecord, error) {
	row, err := sqlcgen.New(tx).CreateDocumentObject(ctx, sqlcgen.CreateDocumentObjectParams{
		ID: in.ID, TenantID: tenantID, ObjectKey: in.ObjectKey,
		Classification: in.Classification, OriginalFilename: in.OriginalFilename,
		ContentType:               in.ContentType,
		OwnerTenantOrganizationID: optUUID(in.OwnerOrganizationID),
		ActorID:                   optUUID(in.ActorID),
	})
	if err != nil {
		return application.ObjectRecord{}, fmt.Errorf("document: create object: %w", err)
	}
	return objectOf(createdObjectRow(row)), nil
}

// GetObject implements application.Repository.
func (Repository) GetObject(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	scope application.Scope,
) (application.ObjectRecord, error) {
	row, err := sqlcgen.New(tx).GetDocumentObject(ctx, sqlcgen.GetDocumentObjectParams{
		TenantID: tenantID, ID: id, ScopeIds: scope.OrganizationIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ObjectRecord{}, application.ErrObjectNotFound
	}
	if err != nil {
		return application.ObjectRecord{}, fmt.Errorf("document: get object: %w", err)
	}
	return objectOf(fetchedObjectRow(row)), nil
}

// LockObject implements application.Repository.
func (Repository) LockObject(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (application.ObjectRecord, error) {
	row, err := sqlcgen.New(tx).LockDocumentObject(ctx, sqlcgen.LockDocumentObjectParams{
		TenantID: tenantID, ID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ObjectRecord{}, application.ErrObjectNotFound
	}
	if err != nil {
		return application.ObjectRecord{}, fmt.Errorf("document: lock object: %w", err)
	}
	return objectOf(lockedObjectRow(row)), nil
}

// ListObjects implements application.Repository.
func (Repository) ListObjects(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.ObjectQuery,
) ([]application.ObjectRecord, error) {
	params := sqlcgen.ListDocumentObjectsParams{
		TenantID: tenantID, ScopeIds: q.Scope.OrganizationIDs,
		ScanStatus: optionalString(q.ScanStatus), Classification: optionalString(q.Classification),
		AggregateType: optionalString(q.AggregateType), AggregateID: optUUID(q.AggregateID),
		PageSize: pageSize(q.PageSize),
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListDocumentObjects(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("document: list objects: %w", err)
	}
	out := make([]application.ObjectRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, objectOf(listedObjectRow(row)))
	}
	return out, nil
}

// FindCleanByDigest implements application.Repository.
func (Repository) FindCleanByDigest(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	digest []byte, scope application.Scope,
) (application.ObjectRecord, error) {
	row, err := sqlcgen.New(tx).FindCleanDocumentByDigest(ctx, sqlcgen.FindCleanDocumentByDigestParams{
		TenantID: tenantID, Sha256: digest, ScopeIds: scope.OrganizationIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ObjectRecord{}, application.ErrObjectNotFound
	}
	if err != nil {
		return application.ObjectRecord{}, fmt.Errorf("document: find clean object by digest: %w", err)
	}
	return objectOf(digestObjectRow(row)), nil
}

// MarkScanning implements application.Repository.
func (Repository) MarkScanning(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	byteSize int64, digest []byte,
) (bool, error) {
	affected, err := sqlcgen.New(tx).MarkDocumentObjectScanning(ctx, sqlcgen.MarkDocumentObjectScanningParams{
		TenantID: tenantID, ID: id, ByteSize: int64Ptr(byteSize), Sha256: digest,
	})
	if err != nil {
		return false, fmt.Errorf("document: mark scanning: %w", err)
	}
	return affected == 1, nil
}

// Promote implements application.Repository. A unique violation on the digest index is not
// an error to report upwards: it is the answer that another object already stores these
// bytes, which the caller resolves by pointing this one at it.
func (Repository) Promote(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.PromotionRow,
) (bool, error) {
	affected, err := sqlcgen.New(tx).PromoteDocumentObject(ctx, sqlcgen.PromoteDocumentObjectParams{
		TenantID: tenantID, ID: in.ObjectID, ObjectKey: in.SecureKey,
		Sha256: in.SHA256, ByteSize: int64Ptr(in.ByteSize),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == constraintDigest {
			return false, nil
		}
		return false, fmt.Errorf("document: promote object: %w", err)
	}
	return affected == 1, nil
}

// MarkDuplicate implements application.Repository.
func (Repository) MarkDuplicate(ctx context.Context, tx pgx.Tx, tenantID, id, canonicalID uuid.UUID,
	canonicalKey string, digest []byte, byteSize int64,
) error {
	if _, err := sqlcgen.New(tx).MarkDocumentObjectDuplicate(ctx, sqlcgen.MarkDocumentObjectDuplicateParams{
		TenantID: tenantID, ID: id, ObjectKey: canonicalKey,
		DuplicateOfObjectID: uuid.NullUUID{UUID: canonicalID, Valid: true},
		Sha256:              digest, ByteSize: int64Ptr(byteSize),
	}); err != nil {
		return fmt.Errorf("document: mark duplicate: %w", err)
	}
	return nil
}

// MarkInfected implements application.Repository.
func (Repository) MarkInfected(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	digest []byte, byteSize int64,
) error {
	if _, err := sqlcgen.New(tx).MarkDocumentObjectInfected(ctx, sqlcgen.MarkDocumentObjectInfectedParams{
		TenantID: tenantID, ID: id, Sha256: digest, ByteSize: int64Ptr(byteSize),
	}); err != nil {
		return fmt.Errorf("document: mark infected: %w", err)
	}
	return nil
}

// MarkFailed implements application.Repository.
func (Repository) MarkFailed(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	if _, err := sqlcgen.New(tx).MarkDocumentObjectFailed(ctx, sqlcgen.MarkDocumentObjectFailedParams{
		TenantID: tenantID, ID: id,
	}); err != nil {
		return fmt.Errorf("document: mark failed: %w", err)
	}
	return nil
}

// MarkPurged implements application.Repository.
func (Repository) MarkPurged(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, at time.Time) error {
	moment := at
	if _, err := sqlcgen.New(tx).MarkDocumentObjectPurged(ctx, sqlcgen.MarkDocumentObjectPurgedParams{
		TenantID: tenantID, ID: id, PurgedAt: &moment,
	}); err != nil {
		return fmt.Errorf("document: mark purged: %w", err)
	}
	return nil
}

// CreateVersion implements application.Repository.
func (Repository) CreateVersion(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewVersionRow,
) (application.VersionRecord, error) {
	row, err := sqlcgen.New(tx).CreateDocumentVersion(ctx, sqlcgen.CreateDocumentVersionParams{
		TenantID: tenantID, ObjectID: in.ObjectID, VersionNo: versionNo(in.VersionNo),
		ByteSize: in.ByteSize, ContentType: in.ContentType,
		EncryptionKeyRef: in.EncryptionKeyRef, ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		return application.VersionRecord{}, fmt.Errorf("document: create version: %w", err)
	}
	return versionOf(createdVersionRow(row)), nil
}

// LatestVersion implements application.Repository.
func (Repository) LatestVersion(ctx context.Context, tx pgx.Tx, tenantID, objectID uuid.UUID) (application.VersionRecord, error) {
	row, err := sqlcgen.New(tx).GetLatestDocumentVersion(ctx, sqlcgen.GetLatestDocumentVersionParams{
		TenantID: tenantID, ObjectID: objectID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.VersionRecord{}, application.ErrObjectNotFound
	}
	if err != nil {
		return application.VersionRecord{}, fmt.Errorf("document: latest version: %w", err)
	}
	return versionOf(latestVersionRow(row)), nil
}

// CreateScanResult implements application.Repository.
func (Repository) CreateScanResult(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewScanResultRow,
) (application.ScanResultRecord, error) {
	row, err := sqlcgen.New(tx).CreateDocumentScanResult(ctx, sqlcgen.CreateDocumentScanResultParams{
		TenantID: tenantID, ObjectID: in.ObjectID, VersionID: in.VersionID,
		Engine: in.Engine, SignatureVersion: in.SignatureVersion,
		Outcome: in.Outcome, Finding: in.Finding,
	})
	if err != nil {
		return application.ScanResultRecord{}, fmt.Errorf("document: create scan result: %w", err)
	}
	return scanResultOf(createdScanResultRow(row)), nil
}

// ListScanResults implements application.Repository.
func (Repository) ListScanResults(ctx context.Context, tx pgx.Tx, tenantID, objectID uuid.UUID) ([]application.ScanResultRecord, error) {
	rows, err := sqlcgen.New(tx).ListDocumentScanResults(ctx, sqlcgen.ListDocumentScanResultsParams{
		TenantID: tenantID, ObjectID: objectID,
	})
	if err != nil {
		return nil, fmt.Errorf("document: list scan results: %w", err)
	}
	out := make([]application.ScanResultRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, scanResultOf(listedScanResultRow(row)))
	}
	return out, nil
}

// CreateLink implements application.Repository.
func (Repository) CreateLink(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewLinkRow,
) (application.LinkRecord, error) {
	row, err := sqlcgen.New(tx).CreateDocumentLink(ctx, sqlcgen.CreateDocumentLinkParams{
		TenantID: tenantID, ObjectID: in.ObjectID, AggregateType: in.AggregateType,
		AggregateID: in.AggregateID, DocumentTypeCode: in.DocumentTypeCode,
		Purpose: in.Purpose, RequiredPermission: in.RequiredPermission,
		ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == constraintLinkTarget {
			return application.LinkRecord{}, application.ErrLinkExists
		}
		return application.LinkRecord{}, fmt.Errorf("document: create link: %w", err)
	}
	return linkOf(createdLinkRow(row)), nil
}

// DeleteLink implements application.Repository.
func (Repository) DeleteLink(ctx context.Context, tx pgx.Tx, tenantID, objectID, linkID uuid.UUID) (bool, error) {
	affected, err := sqlcgen.New(tx).DeleteDocumentLink(ctx, sqlcgen.DeleteDocumentLinkParams{
		TenantID: tenantID, ObjectID: objectID, ID: linkID,
	})
	if err != nil {
		return false, fmt.Errorf("document: delete link: %w", err)
	}
	return affected == 1, nil
}

// ListLinks implements application.Repository.
func (Repository) ListLinks(ctx context.Context, tx pgx.Tx, tenantID, objectID uuid.UUID) ([]application.LinkRecord, error) {
	rows, err := sqlcgen.New(tx).ListDocumentLinks(ctx, sqlcgen.ListDocumentLinksParams{
		TenantID: tenantID, ObjectID: objectID,
	})
	if err != nil {
		return nil, fmt.Errorf("document: list links: %w", err)
	}
	out := make([]application.LinkRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, linkOf(listedLinkRow(row)))
	}
	return out, nil
}

// CreateLegalHold implements application.Repository.
func (Repository) CreateLegalHold(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewLegalHoldRow,
) (application.LegalHoldRecord, error) {
	row, err := sqlcgen.New(tx).CreateDocumentLegalHold(ctx, sqlcgen.CreateDocumentLegalHoldParams{
		TenantID: tenantID, ObjectID: optUUID(in.ObjectID), PersonID: optUUID(in.PersonID),
		AggregateType: in.AggregateType, AggregateID: optUUID(in.AggregateID),
		Reason: in.Reason, ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			switch pgErr.ConstraintName {
			case constraintHoldObject, constraintHoldPerson, constraintHoldAggregate:
				return application.LegalHoldRecord{}, application.ErrHoldExists
			}
		}
		return application.LegalHoldRecord{}, fmt.Errorf("document: create legal hold: %w", err)
	}
	return holdOf(createdHoldRow(row)), nil
}

// GetLegalHold implements application.Repository.
func (Repository) GetLegalHold(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (application.LegalHoldRecord, error) {
	row, err := sqlcgen.New(tx).GetDocumentLegalHold(ctx, sqlcgen.GetDocumentLegalHoldParams{
		TenantID: tenantID, ID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.LegalHoldRecord{}, application.ErrHoldNotFound
	}
	if err != nil {
		return application.LegalHoldRecord{}, fmt.Errorf("document: get legal hold: %w", err)
	}
	return holdOf(fetchedHoldRow(row)), nil
}

// ReleaseLegalHold implements application.Repository.
func (Repository) ReleaseLegalHold(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	actorID *uuid.UUID, at time.Time, expected int64,
) (bool, error) {
	moment := at
	affected, err := sqlcgen.New(tx).ReleaseDocumentLegalHold(ctx, sqlcgen.ReleaseDocumentLegalHoldParams{
		TenantID: tenantID, ID: id, ReleasedAt: &moment,
		ActorID: optUUID(actorID), RowVersion: expected,
	})
	if err != nil {
		return false, fmt.Errorf("document: release legal hold: %w", err)
	}
	return affected == 1, nil
}

// HeldByLegalHold implements application.Repository.
func (Repository) HeldByLegalHold(ctx context.Context, tx pgx.Tx, tenantID, objectID uuid.UUID) (bool, error) {
	held, err := sqlcgen.New(tx).DocumentHasActiveLegalHold(ctx, sqlcgen.DocumentHasActiveLegalHoldParams{
		TenantID: tenantID, ObjectID: uuid.NullUUID{UUID: objectID, Valid: true},
	})
	if err != nil {
		return false, fmt.Errorf("document: read legal holds: %w", err)
	}
	return held, nil
}

// ListPurgeable implements application.Repository.
func (Repository) ListPurgeable(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	before time.Time, limit int,
) ([]application.ObjectRecord, error) {
	rows, err := sqlcgen.New(tx).ListPurgeableDocuments(ctx, sqlcgen.ListPurgeableDocumentsParams{
		TenantID: tenantID, Before: before, PageSize: pageSize(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("document: list purgeable documents: %w", err)
	}
	out := make([]application.ObjectRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, objectOf(purgeableObjectRow(row)))
	}
	return out, nil
}

// ActiveTenants implements application.Repository. platform.tenant carries no RLS, so it
// is read outside a tenant transaction like the other cross-tenant jobs.
func (Repository) ActiveTenants(ctx context.Context, tx pgx.Tx) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM platform.tenant WHERE status IN ('ACTIVE','SUSPENDED')`)
	if err != nil {
		return nil, fmt.Errorf("document: list tenants: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("document: scan tenant: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
