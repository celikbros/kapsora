package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/document/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/antivirus"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	"github.com/celikbros/kapsora/internal/platform/objectstore"
)

// Storage names the two buckets and how long the links into them live. Both lifetimes are
// short on purpose: a presigned URL is a bearer credential, and the only thing limiting
// the damage of one that leaks is the minute it stops working.
type Storage struct {
	QuarantineBucket string
	SecureBucket     string
	// UploadTTL is how long a client has to start its upload; default 15 minutes
	// (WP-I4-04 section 2.2).
	UploadTTL time.Duration
	// DownloadTTL is how long a download link lives; default 5 minutes.
	DownloadTTL time.Duration
	// EncryptionKeyRef names the key the store protects the bytes with. It is recorded on
	// every version and is a reference, never key material.
	EncryptionKeyRef string
}

func (s Storage) withDefaults() Storage {
	if s.QuarantineBucket == "" {
		s.QuarantineBucket = domain.BucketQuarantine
	}
	if s.SecureBucket == "" {
		s.SecureBucket = domain.BucketSecure
	}
	if s.UploadTTL <= 0 {
		s.UploadTTL = 15 * time.Minute
	}
	if s.DownloadTTL <= 0 {
		s.DownloadTTL = 5 * time.Minute
	}
	return s
}

// Service implements the document use cases.
type Service struct {
	pool    *pgxpool.Pool
	repo    Repository
	store   objectstore.Store
	scanner antivirus.Scanner
	audit   audit.Recorder
	cursors *httpx.CursorCodec
	storage Storage
	logger  *slog.Logger
	now     func() time.Time
}

// Deps are the collaborators of the service.
type Deps struct {
	Pool  *pgxpool.Pool
	Repo  Repository
	Store objectstore.Store
	// Scanner may be nil in the API process: nothing there scans anything. The worker
	// requires one, and ScanObject refuses to run without it rather than treating an
	// absent scanner as a clean verdict.
	Scanner antivirus.Scanner
	Audit   audit.Recorder
	// Cursors may be nil in a process that answers no list.
	Cursors *httpx.CursorCodec
	Storage Storage
	Logger  *slog.Logger
	// Now defaults to time.Now; tests pin it so a clock is deterministic.
	Now func() time.Time
}

// New validates the dependencies.
func New(d Deps) (*Service, error) {
	if d.Pool == nil || d.Repo == nil || d.Store == nil {
		return nil, errors.New("document: pool, repository and object store are required")
	}
	if d.Audit == nil {
		d.Audit = audit.NopRecorder{}
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		pool: d.Pool, repo: d.Repo, store: d.Store, scanner: d.Scanner, audit: d.Audit,
		cursors: d.Cursors, storage: d.Storage.withDefaults(), logger: d.Logger, now: d.Now,
	}, nil
}

// Document is one object with the links that point at it. The links travel with it because
// what a document is — the invoice, the referral, the report — is what a link says, and a
// document without them is a filename.
type Document struct {
	Object ObjectRecord
	Links  []LinkRecord
}

// Downloadable reports whether this document may be handed out at all.
func (d Document) Downloadable() bool {
	return domain.Downloadable(d.Object.ScanStatus, d.Object.Bucket, d.Object.PurgedAt != nil)
}

// Page is one keyset page of documents.
type Page struct {
	Items      []Document
	NextCursor string
}

// Filter is the API-level document list request.
type Filter struct {
	Cursor         string
	Limit          int
	ScanStatus     string
	Classification string
	AggregateType  string
	AggregateID    *uuid.UUID
}

// withTx runs fn inside a tenant-bound transaction.
func (s *Service) withTx(ctx context.Context, rc identity.RequestContext,
	fn func(ctx context.Context, tx pgx.Tx) error,
) error {
	return db.WithTenantTx(ctx, s.pool,
		db.TenantContext{TenantID: rc.TenantID, ActorID: rc.Principal.ActorID}, fn)
}

// withSystemTx runs fn for a tenant with no actor. The worker and the retention sweep act
// for the system: there is nobody whose permissions the write should be attributed to.
func (s *Service) withSystemTx(ctx context.Context, tenantID uuid.UUID,
	fn func(ctx context.Context, tx pgx.Tx) error,
) error {
	return db.WithTenantTx(ctx, s.pool, db.TenantContext{TenantID: tenantID}, fn)
}

// errPagingUnavailable guards the paged reads in a process built without a cursor codec.
var errPagingUnavailable = errors.New("document: paged reads need a cursor codec")

// paging decodes the cursor and clamps the limit; the repository is asked for one row more
// than the page size so the caller learns whether a next page exists.
func (s *Service) paging(cursor string, limit int) (after *httpx.Cursor, pageSize int, err error) {
	if s.cursors == nil {
		return nil, 0, errPagingUnavailable
	}
	decoded, hasCursor, err := s.cursors.Decode(cursor)
	if err != nil {
		return nil, 0, err
	}
	pageSize = httpx.ClampLimit(limit)
	if hasCursor {
		after = &decoded
	}
	return after, pageSize, nil
}

// record writes one business audit row. The detail is ids, codes, sizes and statuses only.
// audit.SanitizeDetail drops any key holding a name, an identifier or a secret, so nothing
// here is named around that rule: original_filename is a legitimate column and a
// deliberately absent audit detail, because the key that would carry it contains "name"
// and would be dropped silently — which looks like an audit and is not.
func (s *Service) record(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	action string, resourceID uuid.UUID, detail map[string]any,
) error {
	return s.audit.Record(ctx, tx, audit.Event{
		TenantID: nullUUID(rc.TenantID), ActorID: nullUUID(rc.Principal.ActorID),
		MembershipID: nullUUID(rc.MembershipID), Category: audit.CategoryBusiness,
		ActionCode: action, ResourceType: domain.AggregateType, ResourceID: nullUUID(resourceID),
		Outcome: audit.OutcomeSuccess, Detail: detail,
	})
}

// recordSecurity writes the SECURITY-category event an infected upload produces. It is a
// separate category rather than a business event because it is what a security review
// reads, and it is written by the worker, which acts for no actor.
func (s *Service) recordSecurity(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	action string, resourceID uuid.UUID, detail map[string]any,
) error {
	return s.audit.Record(ctx, tx, audit.Event{
		TenantID: nullUUID(tenantID), Category: audit.CategorySecurity,
		ActionCode: action, ResourceType: domain.AggregateType, ResourceID: nullUUID(resourceID),
		Outcome: audit.OutcomeFailure, Detail: detail,
	})
}

// objectCursor is the keyset position of a row on the (created_at DESC, id DESC) order the
// list endpoint pages by.
func objectCursor(r ObjectRecord) httpx.Cursor {
	return httpx.Cursor{CreatedAt: r.CreatedAt, ID: r.ID}
}

// objectKeyFor builds the key an object is stored under. The date prefix keeps a bucket's
// listing navigable; the id is what makes the key unguessable and unique. Nothing the
// client sent appears in it — a key built from a filename is a key one upload can use to
// name another one's object.
func objectKeyFor(tenantID, objectID uuid.UUID, at time.Time) string {
	return fmt.Sprintf("%s/%04d/%02d/%s", tenantID, at.Year(), int(at.Month()), objectID)
}

func actorPtr(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func nullUUID(id uuid.UUID) uuid.NullUUID {
	return uuid.NullUUID{UUID: id, Valid: id != uuid.Nil}
}

func optionalPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// storeError wraps an object store failure so the transport can answer 503 rather than
// 500. A store that is down is a state the caller can retry out of.
func storeError(op string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrStoreUnavailable, op, err)
}

// fieldError builds a one-field validation error.
func fieldError(field, code, message string) error {
	ve := &domain.ValidationError{}
	ve.Add(field, code, message)
	return ve
}
