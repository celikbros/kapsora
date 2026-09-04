// Package application implements the document use cases: reserving an upload, recording
// that it happened, scanning what arrived, promoting what is clean, handing out a
// short-lived link to it, saying which record it belongs to, and holding it against
// deletion. Transactions are opened here with db.WithTenantTx, so a write and its audit
// row commit together and RLS is bound for every statement.
//
// Three things this package never does.
//
// It never touches a file body on an API request. createUpload hands out a presigned PUT
// and downloadDocument hands out a presigned GET; the only process that reads bytes is the
// worker, and it reads them to feed the scanner.
//
// It never writes a scan status a caller chose. PENDING is what createUpload reserves,
// SCANNING is what completeUpload sets, and CLEAN, INFECTED and FAILED are only ever
// written by the worker from a verdict. There is no statement in the repository that would
// accept a status as a parameter.
//
// And it never promotes a file it has not seen a CLEAN verdict for. The copy into the
// secure bucket happens in exactly one function, after exactly one check.
package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Permissions guarding this package. document.upload, document.read and
// document.download.sensitive are in the catalogue from migration 000008; document.link
// and document.legal_hold.manage are added by migration 000028. They live here rather than
// in the transport because who may put a document beyond the reach of retention is a
// business rule, not a routing detail.
const (
	PermissionUpload    = "document.upload"
	PermissionRead      = "document.read"
	PermissionLink      = "document.link"
	PermissionLegalHold = "document.legal_hold.manage"
)

// ScopeOrganization is the iam.access_grant.scope_type of provider-side roles (v1.2 6.3).
// It is this package's provider boundary: an actor granted an organization sees that
// organization's documents and the tenant's own, and no other provider's.
const ScopeOrganization = "ORGANIZATION"

// ScanRequestedEvent asks the worker to scan an object that has finished uploading. The
// payload carries nothing but ids: the worker loads the object itself, and an event that
// carried a key or a digest would be an event that could disagree with the row.
const ScanRequestedEvent = "document.object.scan_requested"

// Errors mapped by the transport layer to problem codes.
var (
	ErrObjectNotFound = errors.New("document: document not found")
	ErrLinkNotFound   = errors.New("document: document link not found")
	ErrHoldNotFound   = errors.New("document: legal hold not found")

	// ErrNotScanned refuses a download of anything that has not been cleared. It covers
	// PENDING, SCANNING and FAILED: none of them is "safe", and telling them apart in the
	// answer would only say how far along an attack got.
	ErrNotScanned = errors.New("document: the document has not been scanned clean")
	// ErrInfected refuses a download of a file the scanner named something in. Its bytes
	// are already gone; the row answers so the caller stops asking.
	ErrInfected = errors.New("document: the document is infected")
	// ErrPurged refuses a download of a document whose bytes retention has removed.
	ErrPurged = errors.New("document: the document has been purged by retention")
	// ErrAlreadyCompleted refuses a second completeUpload. The first one moved the object
	// out of PENDING and queued its scan.
	ErrAlreadyCompleted = errors.New("document: the upload has already been completed")
	// ErrLinkExists is the unique (tenant, object, aggregate, type) refusing the same
	// document attached twice to the same record under the same type.
	ErrLinkExists = errors.New("document: the document is already linked to that record")
	// ErrLinkPermission is a caller who may read documents in general but not this one:
	// the link says which permission reaching it needs, and the caller does not hold it.
	ErrLinkPermission = errors.New("document: the link requires a permission the caller does not hold")
	// ErrHoldExists is the partial unique index refusing a second active hold on one
	// target.
	ErrHoldExists = errors.New("document: an active legal hold already covers that target")
	// ErrHoldReleased refuses releasing a hold that is already released.
	ErrHoldReleased = errors.New("document: the legal hold has already been released")
	// ErrLegalHold refuses deleting bytes something is holding. It is what makes "under
	// legal hold" mean anything: the retention sweep asks and skips.
	ErrLegalHold = errors.New("document: the document is under legal hold")

	// ErrStoreUnavailable is the object store not answering. It is separated from an
	// internal error because it is the one failure a caller can usefully retry.
	ErrStoreUnavailable = errors.New("document: the object store is unavailable")

	ErrVersionMismatch = errors.New("document: row version does not match If-Match")
)

// Scope is the caller's provider boundary. A nil slice means "no restriction"; an empty
// non-nil slice restricts the caller to nothing, which is the safe reading of a grant that
// names no organization.
type Scope struct {
	OrganizationIDs []uuid.UUID
}

// Restricted reports whether the caller is bound to a set of organizations.
func (s Scope) Restricted() bool { return s.OrganizationIDs != nil }

// scopeOf reads the caller's organization grants. A tenant-wide actor has none.
func scopeOf(rc identity.RequestContext) Scope {
	var ids []uuid.UUID
	for _, s := range rc.Scopes {
		if s.Type != ScopeOrganization {
			continue
		}
		if ids == nil {
			ids = []uuid.UUID{}
		}
		if s.ID.Valid {
			ids = append(ids, s.ID.UUID)
		}
	}
	return Scope{OrganizationIDs: ids}
}

// ObjectRecord is one document.object row.
type ObjectRecord struct {
	ID                  uuid.UUID
	ObjectKey           string
	Bucket              string
	Classification      string
	OriginalFilename    string
	ContentType         string
	ByteSize            *int64
	SHA256              []byte
	ScanStatus          string
	OwnerOrganizationID *uuid.UUID
	DuplicateOfObjectID *uuid.UUID
	UploadedBy          *uuid.UUID
	UploadedAt          time.Time
	PurgedAt            *time.Time
	CreatedAt           time.Time
	RowVersion          int64
}

// NewObjectRow is the insert payload of a reserved upload. The id and the key are chosen
// by the caller together, because the key contains the id: a key derived from anything the
// client sent would let one upload name another one's object.
type NewObjectRow struct {
	ID                  uuid.UUID
	ObjectKey           string
	Classification      string
	OriginalFilename    string
	ContentType         string
	OwnerOrganizationID *uuid.UUID
	ActorID             *uuid.UUID
}

// ObjectQuery is the repository-level document filter.
type ObjectQuery struct {
	Scope          Scope
	ScanStatus     string
	Classification string
	AggregateType  string
	AggregateID    *uuid.UUID
	After          *httpx.Cursor
	PageSize       int
}

// VersionRecord is one append-only document.version row.
type VersionRecord struct {
	ID               uuid.UUID
	ObjectID         uuid.UUID
	VersionNo        int
	ByteSize         int64
	ContentType      string
	EncryptionKeyRef *string
	CreatedAt        time.Time
}

// NewVersionRow is the insert payload of a version.
type NewVersionRow struct {
	ObjectID         uuid.UUID
	VersionNo        int
	ByteSize         int64
	ContentType      string
	EncryptionKeyRef *string
	ActorID          *uuid.UUID
}

// LinkRecord is one document.link row.
type LinkRecord struct {
	ID                 uuid.UUID
	ObjectID           uuid.UUID
	AggregateType      string
	AggregateID        uuid.UUID
	DocumentTypeCode   string
	Purpose            *string
	RequiredPermission *string
	CreatedBy          *uuid.UUID
	CreatedAt          time.Time
}

// NewLinkRow is the insert payload of a link.
type NewLinkRow struct {
	ObjectID           uuid.UUID
	AggregateType      string
	AggregateID        uuid.UUID
	DocumentTypeCode   string
	Purpose            *string
	RequiredPermission *string
	ActorID            *uuid.UUID
}

// ScanResultRecord is one append-only document.scan_result row.
type ScanResultRecord struct {
	ID               uuid.UUID
	ObjectID         uuid.UUID
	VersionID        uuid.UUID
	Engine           string
	SignatureVersion *string
	Outcome          string
	Finding          *string
	ScannedAt        time.Time
}

// NewScanResultRow is the insert payload of one verdict.
type NewScanResultRow struct {
	ObjectID         uuid.UUID
	VersionID        uuid.UUID
	Engine           string
	SignatureVersion *string
	Outcome          string
	Finding          *string
}

// LegalHoldRecord is one document.legal_hold row.
type LegalHoldRecord struct {
	ID            uuid.UUID
	ObjectID      *uuid.UUID
	PersonID      *uuid.UUID
	AggregateType *string
	AggregateID   *uuid.UUID
	Reason        string
	PlacedBy      *uuid.UUID
	PlacedAt      time.Time
	ReleasedAt    *time.Time
	ReleasedBy    *uuid.UUID
	RowVersion    int64
}

// NewLegalHoldRow is the insert payload of a hold.
type NewLegalHoldRow struct {
	ObjectID      *uuid.UUID
	PersonID      *uuid.UUID
	AggregateType *string
	AggregateID   *uuid.UUID
	Reason        string
	ActorID       *uuid.UUID
}

// PromotionRow is everything the worker learned by reading the bytes: the digest it
// computed itself and the size it counted. Neither comes from the client.
type PromotionRow struct {
	ObjectID  uuid.UUID
	SHA256    []byte
	ByteSize  int64
	SecureKey string
}

// Repository is the persistence port; every method runs inside the caller's transaction,
// which db.WithTenantTx has already bound to the tenant so RLS is active. The provider
// boundary is a repository concern too: the scope is passed down rather than checked
// above, so a row outside it is genuinely not there rather than fetched and then hidden.
type Repository interface {
	CreateObject(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewObjectRow) (ObjectRecord, error)
	GetObject(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, scope Scope) (ObjectRecord, error)
	// LockObject reads an object FOR UPDATE with no scope. Only the worker calls it: it
	// acts for the system rather than for a person, and a scan that skipped a row because
	// nobody happened to be granted its organization would leave a file unscanned.
	LockObject(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (ObjectRecord, error)
	ListObjects(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q ObjectQuery) ([]ObjectRecord, error)
	// FindCleanByDigest looks for the canonical stored copy of a digest inside the
	// caller's scope. It is what makes the same file uploaded twice one object.
	FindCleanByDigest(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, digest []byte, scope Scope) (ObjectRecord, error)

	// MarkScanning records what the client claims about the bytes and moves the object out
	// of PENDING. The predicate carries the status, so a second completeUpload changes
	// nothing and is told so.
	MarkScanning(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, byteSize int64, digest []byte) (bool, error)
	// Promote writes the CLEAN verdict, the digest the worker computed and the secure
	// bucket in one statement. It reports false when a canonical object with the same
	// digest already exists, which is the race MarkDuplicate then resolves.
	Promote(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in PromotionRow) (bool, error)
	// MarkDuplicate points an object at the canonical copy of the same bytes. The loser
	// keeps no bytes of its own, so there is one stored file however many rows name it.
	MarkDuplicate(ctx context.Context, tx pgx.Tx, tenantID, id, canonicalID uuid.UUID, canonicalKey string, digest []byte, byteSize int64) error
	// MarkInfected records the verdict on a file whose bytes have already been deleted.
	MarkInfected(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, digest []byte, byteSize int64) error
	// MarkFailed records that no verdict could be reached. The file stays in quarantine
	// and stays unreadable.
	MarkFailed(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	// MarkPurged records that retention removed the bytes; the row outlives them.
	MarkPurged(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, at time.Time) error

	CreateVersion(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewVersionRow) (VersionRecord, error)
	LatestVersion(ctx context.Context, tx pgx.Tx, tenantID, objectID uuid.UUID) (VersionRecord, error)
	CreateScanResult(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewScanResultRow) (ScanResultRecord, error)
	ListScanResults(ctx context.Context, tx pgx.Tx, tenantID, objectID uuid.UUID) ([]ScanResultRecord, error)

	CreateLink(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewLinkRow) (LinkRecord, error)
	DeleteLink(ctx context.Context, tx pgx.Tx, tenantID, objectID, linkID uuid.UUID) (bool, error)
	ListLinks(ctx context.Context, tx pgx.Tx, tenantID, objectID uuid.UUID) ([]LinkRecord, error)

	CreateLegalHold(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewLegalHoldRow) (LegalHoldRecord, error)
	GetLegalHold(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (LegalHoldRecord, error)
	ReleaseLegalHold(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, actorID *uuid.UUID, at time.Time, expected int64) (bool, error)
	// HeldByLegalHold reports whether anything holds this object: a hold on the object
	// itself, or one on a record the object is linked to. Both count, because a hold over
	// a case is a hold over the case's documents.
	HeldByLegalHold(ctx context.Context, tx pgx.Tx, tenantID, objectID uuid.UUID) (bool, error)

	// ListPurgeable lists stored documents older than a cutoff whose bytes are still
	// there. The legal hold check is deliberately not folded into it: skipping is a
	// decision the sweep makes and records, not one a WHERE clause makes silently.
	ListPurgeable(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, before time.Time, limit int) ([]ObjectRecord, error)
	// ActiveTenants lists the tenants the retention job walks. platform.tenant carries no
	// RLS, so it is read outside a tenant transaction like the other cross-tenant jobs.
	ActiveTenants(ctx context.Context, tx pgx.Tx) ([]uuid.UUID, error)
}
