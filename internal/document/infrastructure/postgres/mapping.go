package documentpg

import (
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/document/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// The five reads of document.object select the same columns and sqlc gives each of them
// its own row type; the versions, the links, the verdicts and the holds do the same.
// Rather than several copies of the same mapping — which is exactly how a column ends up
// carried in one read and dropped in another — every row is narrowed to one shape here and
// mapped once.

type object struct {
	ID                        uuid.UUID
	ObjectKey                 string
	Bucket                    string
	Classification            string
	OriginalFilename          string
	ContentType               string
	ByteSize                  *int64
	Sha256                    []byte
	ScanStatus                string
	OwnerTenantOrganizationID uuid.NullUUID
	DuplicateOfObjectID       uuid.NullUUID
	UploadedBy                uuid.NullUUID
	UploadedAt                time.Time
	PurgedAt                  *time.Time
	CreatedAt                 time.Time
	RowVersion                int64
}

func createdObjectRow(r sqlcgen.CreateDocumentObjectRow) object     { return object(r) }
func fetchedObjectRow(r sqlcgen.GetDocumentObjectRow) object        { return object(r) }
func lockedObjectRow(r sqlcgen.LockDocumentObjectRow) object        { return object(r) }
func listedObjectRow(r sqlcgen.ListDocumentObjectsRow) object       { return object(r) }
func digestObjectRow(r sqlcgen.FindCleanDocumentByDigestRow) object { return object(r) }
func purgeableObjectRow(r sqlcgen.ListPurgeableDocumentsRow) object { return object(r) }

func objectOf(r object) application.ObjectRecord {
	return application.ObjectRecord{
		ID: r.ID, ObjectKey: r.ObjectKey, Bucket: r.Bucket,
		Classification: r.Classification, OriginalFilename: r.OriginalFilename,
		ContentType: r.ContentType, ByteSize: r.ByteSize, SHA256: r.Sha256,
		ScanStatus:          r.ScanStatus,
		OwnerOrganizationID: uuidPtr(r.OwnerTenantOrganizationID),
		DuplicateOfObjectID: uuidPtr(r.DuplicateOfObjectID),
		UploadedBy:          uuidPtr(r.UploadedBy),
		UploadedAt:          r.UploadedAt, PurgedAt: r.PurgedAt,
		CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
}

type version struct {
	ID               uuid.UUID
	ObjectID         uuid.UUID
	VersionNo        int32
	ByteSize         int64
	ContentType      string
	EncryptionKeyRef *string
	CreatedAt        time.Time
}

func createdVersionRow(r sqlcgen.CreateDocumentVersionRow) version   { return version(r) }
func latestVersionRow(r sqlcgen.GetLatestDocumentVersionRow) version { return version(r) }

func versionOf(r version) application.VersionRecord {
	return application.VersionRecord{
		ID: r.ID, ObjectID: r.ObjectID, VersionNo: int(r.VersionNo), ByteSize: r.ByteSize,
		ContentType: r.ContentType, EncryptionKeyRef: r.EncryptionKeyRef, CreatedAt: r.CreatedAt,
	}
}

type link struct {
	ID                 uuid.UUID
	ObjectID           uuid.UUID
	AggregateType      string
	AggregateID        uuid.UUID
	DocumentTypeCode   string
	Purpose            *string
	RequiredPermission *string
	CreatedBy          uuid.NullUUID
	CreatedAt          time.Time
}

func createdLinkRow(r sqlcgen.CreateDocumentLinkRow) link { return link(r) }
func listedLinkRow(r sqlcgen.ListDocumentLinksRow) link   { return link(r) }

func linkOf(r link) application.LinkRecord {
	return application.LinkRecord{
		ID: r.ID, ObjectID: r.ObjectID, AggregateType: r.AggregateType,
		AggregateID: r.AggregateID, DocumentTypeCode: r.DocumentTypeCode,
		Purpose: r.Purpose, RequiredPermission: r.RequiredPermission,
		CreatedBy: uuidPtr(r.CreatedBy), CreatedAt: r.CreatedAt,
	}
}

type scanResult struct {
	ID               uuid.UUID
	ObjectID         uuid.UUID
	VersionID        uuid.UUID
	Engine           string
	SignatureVersion *string
	Outcome          string
	Finding          *string
	ScannedAt        time.Time
}

func createdScanResultRow(r sqlcgen.CreateDocumentScanResultRow) scanResult { return scanResult(r) }
func listedScanResultRow(r sqlcgen.ListDocumentScanResultsRow) scanResult   { return scanResult(r) }

func scanResultOf(r scanResult) application.ScanResultRecord {
	return application.ScanResultRecord{
		ID: r.ID, ObjectID: r.ObjectID, VersionID: r.VersionID, Engine: r.Engine,
		SignatureVersion: r.SignatureVersion, Outcome: r.Outcome, Finding: r.Finding,
		ScannedAt: r.ScannedAt,
	}
}

type legalHold struct {
	ID            uuid.UUID
	ObjectID      uuid.NullUUID
	PersonID      uuid.NullUUID
	AggregateType *string
	AggregateID   uuid.NullUUID
	Reason        string
	PlacedBy      uuid.NullUUID
	PlacedAt      time.Time
	ReleasedAt    *time.Time
	ReleasedBy    uuid.NullUUID
	RowVersion    int64
}

func createdHoldRow(r sqlcgen.CreateDocumentLegalHoldRow) legalHold { return legalHold(r) }
func fetchedHoldRow(r sqlcgen.GetDocumentLegalHoldRow) legalHold    { return legalHold(r) }

func holdOf(r legalHold) application.LegalHoldRecord {
	return application.LegalHoldRecord{
		ID: r.ID, ObjectID: uuidPtr(r.ObjectID), PersonID: uuidPtr(r.PersonID),
		AggregateType: r.AggregateType, AggregateID: uuidPtr(r.AggregateID),
		Reason: r.Reason, PlacedBy: uuidPtr(r.PlacedBy), PlacedAt: r.PlacedAt,
		ReleasedAt: r.ReleasedAt, ReleasedBy: uuidPtr(r.ReleasedBy), RowVersion: r.RowVersion,
	}
}

func uuidPtr(v uuid.NullUUID) *uuid.UUID {
	if !v.Valid {
		return nil
	}
	id := v.UUID
	return &id
}

func optUUID(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func int64Ptr(v int64) *int64 { return &v }

// versionNo narrows a version number to the column's width. A document with more than two
// billion versions is not a case worth carrying a wider column for, and silently wrapping
// round would make the newest version look like the first.
func versionNo(n int) int32 {
	if n < 1 {
		return 1
	}
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(n)
}

// pageSize clamps a repository page size; the service has already clamped the caller's.
func pageSize(n int) int32 {
	if n <= 0 {
		return 50
	}
	if n > 500 {
		return 500
	}
	return int32(n)
}
