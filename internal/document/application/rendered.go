package application

import (
	"context"
	"crypto/sha256"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/document/domain"
)

// RenderEngine is what the scan history says produced the verdict on a file this platform wrote
// itself. It is a name in the same column ClamAV's name goes in, so "what looked at these bytes"
// has one answer for every document rather than one answer for uploads and a blank for the rest.
const RenderEngine = "kapsora.render"

// RenderedFile is a file the platform produced: a report, an export, a generated document. Nobody
// uploaded it, and there is no browser waiting for a presigned URL.
type RenderedFile struct {
	Filename       string
	ContentType    string
	Classification string
	Body           []byte
	// OwnerOrganizationID is the provider the file belongs to, when it belongs to one.
	OwnerOrganizationID *uuid.UUID
	// Detail is the audit detail of the write. Keys must be snake_case, and the caller owns
	// them because what is worth recording about a rendered file is what produced it.
	Detail map[string]any
	// Action is the audit action code of the write, so a rendered export and a rendered
	// statement are two different rows in the log rather than one.
	Action string
}

// ErrEmptyRender refuses to store a file with no bytes. A zero-byte document is a mistake every
// time, and one the pipeline would otherwise store as a perfectly clean, perfectly empty file.
var ErrEmptyRender = errors.New("document: a rendered file has no bytes")

// StoreRendered puts a file this platform produced into the secure bucket and returns the document
// that holds it.
//
// It is the one way into the document store that does not begin with a presigned PUT, and it
// exists because WP-I7-05's exports have no uploader: the worker rendered the bytes, so there is
// nobody to hand a URL to and nothing untrusted to quarantine. The rule the pipeline is built on
// — a file is readable only after something looked at every byte of it — is kept rather than
// waived: the digest is computed here over the whole body, the CLEAN verdict is written into
// `document.scan_result` with this platform named as the engine, and `Promote` is the same
// statement the scanner's own verdict goes through. What a reader gets is a document whose scan
// history says who cleared it.
//
// It is the worker's and nobody else's. An API process that called it would be an API process
// holding a file body, which is the one thing WP-I4-04 is built to prevent — and the API's own
// service is constructed without the port that reaches this.
func (s *Service) StoreRendered(ctx context.Context, tenantID uuid.UUID, in RenderedFile,
) (Document, error) {
	if len(in.Body) == 0 {
		return Document{}, ErrEmptyRender
	}
	byteSize := int64(len(in.Body))
	if err := domain.ValidateUpload(in.Filename, in.ContentType, in.Classification, "",
		byteSize); err != nil {
		return Document{}, err
	}
	digest := sha256.Sum256(in.Body)

	objectID, err := uuid.NewV7()
	if err != nil {
		return Document{}, err
	}
	key := objectKeyFor(tenantID, objectID, s.now())
	// The bytes go in before the row says they are there. A key written and never recorded is an
	// orphan the retention sweep will never find; a row that claims a key with no bytes behind it
	// is a download that answers 500. The first is a wasted object, the second is a broken
	// promise, and this order chooses the first.
	if err := s.store.Write(ctx, s.storage.SecureBucket, key, in.Body, in.ContentType); err != nil {
		return Document{}, storeError("write rendered file", err)
	}

	var out Document
	err = s.withSystemTx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		created, err := s.repo.CreateObject(ctx, tx, tenantID, NewObjectRow{
			ID: objectID, ObjectKey: key, Classification: in.Classification,
			OriginalFilename: domain.NormalizeFilename(in.Filename),
			ContentType:      in.ContentType, OwnerOrganizationID: in.OwnerOrganizationID,
		})
		if err != nil {
			return err
		}
		if _, err := s.repo.MarkScanning(ctx, tx, tenantID, created.ID, byteSize,
			digest[:]); err != nil {
			return err
		}
		version, err := s.repo.CreateVersion(ctx, tx, tenantID, NewVersionRow{
			ObjectID: created.ID, VersionNo: 1, ByteSize: byteSize,
			ContentType:      in.ContentType,
			EncryptionKeyRef: optionalPtr(s.storage.EncryptionKeyRef),
		})
		if err != nil {
			return err
		}
		if _, err := s.repo.CreateScanResult(ctx, tx, tenantID, NewScanResultRow{
			ObjectID: created.ID, VersionID: version.ID, Engine: RenderEngine,
			Outcome: domain.ScanClean,
		}); err != nil {
			return err
		}
		promoted, err := s.repo.Promote(ctx, tx, tenantID, PromotionRow{
			ObjectID: created.ID, SHA256: digest[:], ByteSize: byteSize, SecureKey: key,
		})
		if err != nil {
			return err
		}
		if !promoted {
			// The digest is already stored under another object. It cannot happen for an export
			// — every one of them carries a watermark naming its own id and the moment it was
			// asked for, so no two exports are ever the same bytes — and if it ever does, saying
			// so is better than quietly handing back somebody else's file.
			return ErrObjectNotFound
		}
		created.ScanStatus = domain.ScanClean
		created.Bucket = s.storage.SecureBucket
		created.ByteSize = &byteSize
		created.SHA256 = digest[:]
		out = Document{Object: created}

		action := in.Action
		if action == "" {
			action = "document.render.store"
		}
		detail := map[string]any{
			"classification": created.Classification,
			"byte_size":      byteSize,
			"content_type":   in.ContentType,
		}
		for key, value := range in.Detail {
			detail[key] = value
		}
		return s.audit.Record(ctx, tx, auditEvent(tenantID, created.ID, action, detail))
	})
	if err != nil {
		// The bytes were written and the row was not. Removing them is the tidy-up the caller
		// cannot do, because it never learns the key.
		if removeErr := s.store.Remove(ctx, s.storage.SecureBucket, key); removeErr != nil {
			s.logger.Warn("document: could not remove the bytes of a rendered file whose row failed",
				"tenant_id", tenantID, "object_id", objectID, "error", removeErr)
		}
		return Document{}, err
	}
	return out, nil
}

// PurgeObject removes the bytes of one document, unless a legal hold covers it. It reports whether
// the bytes went: false means something is holding them, which is exactly what a legal hold means
// and exactly what a caller has to be able to count.
//
// It is the retention sweep's own decision made available to a caller who has a different reason
// for it — an export whose TTL has run out, for instance. Both go through the same function, so a
// hold placed over a case protects the export of that case as surely as it protects the case's
// documents.
func (s *Service) PurgeObject(ctx context.Context, tenantID, objectID uuid.UUID) (bool, error) {
	purged, err := s.purgeOne(ctx, tenantID, ObjectRecord{ID: objectID})
	if errors.Is(err, ErrLegalHold) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return purged, nil
}
