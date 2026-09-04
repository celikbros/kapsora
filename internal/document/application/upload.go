package application

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/document/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/objectstore"
	"github.com/celikbros/kapsora/internal/platform/outbox"
)

// ErrUploadMissing is completeUpload on an object whose bytes never arrived. The presigned
// URL was handed out and not used, or it expired first; either way there is nothing to
// scan, and moving the row to SCANNING would queue a scan of nothing.
var ErrUploadMissing = errors.New("document: no bytes were uploaded for this document")

// NewUploadInput is what a caller says about a file before any byte of it exists.
type NewUploadInput struct {
	OriginalFilename string
	ContentType      string
	ByteSize         int64
	Classification   string
	// OwnerOrganizationID is the provider the document belongs to. A tenant-wide caller
	// may leave it empty; a provider-scoped one may only name an organization it holds.
	OwnerOrganizationID *uuid.UUID
	// SHA256 is optional and is the digest the client already computed over the file it
	// is about to send. When it is given and those exact bytes are already stored and
	// clean, no second upload happens at all: the same file uploaded twice is one object,
	// which is also what stops a member re-uploading a document being counted as new.
	//
	// It is a claim, not a fact, so it is only ever used to find a document the caller
	// could already see: the lookup carries the caller's own provider scope. It can
	// therefore save an upload and can never reach a document across that boundary.
	SHA256 string
}

// UploadReservation is the answer to createUpload: the document row and, unless those
// exact bytes are already stored, a short-lived URL to put the file at. The API never
// takes the body itself (v1.2 39.14), so this URL is the whole of the upload contract.
type UploadReservation struct {
	Document Document
	// Upload is nil when the file is already stored, which is the deduplicated case.
	Upload *objectstore.PresignedURL
}

// CreateUpload reserves an object and hands back a presigned PUT into the quarantine
// bucket. Nothing is scanned yet and nothing is readable: the row is PENDING, the bucket
// is quarantine, and the only way out of either is a verdict.
func (s *Service) CreateUpload(ctx context.Context, rc identity.RequestContext,
	in NewUploadInput,
) (UploadReservation, error) {
	if err := domain.ValidateUpload(in.OriginalFilename, in.ContentType, in.Classification,
		in.SHA256, in.ByteSize); err != nil {
		return UploadReservation{}, err
	}
	scope := scopeOf(rc)
	owner, err := resolveOwner(scope, in.OwnerOrganizationID)
	if err != nil {
		return UploadReservation{}, err
	}

	var claimed []byte
	if in.SHA256 != "" {
		if claimed, err = hex.DecodeString(in.SHA256); err != nil {
			return UploadReservation{}, fieldError("sha256", "FORMAT", "onaltılık özet çözülemedi")
		}
	}

	out := UploadReservation{}
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if claimed != nil {
			existing, err := s.repo.FindCleanByDigest(ctx, tx, rc.TenantID, claimed, scope)
			switch {
			case err == nil:
				links, err := s.repo.ListLinks(ctx, tx, rc.TenantID, existing.ID)
				if err != nil {
					return err
				}
				out.Document = Document{Object: existing, Links: links}
				return s.record(ctx, tx, rc, "document.upload.deduplicated", existing.ID,
					map[string]any{"classification": existing.Classification, "deduplicated": true})
			case errors.Is(err, ErrObjectNotFound):
				// Nothing stored under those bytes yet; carry on and reserve an upload.
			default:
				return err
			}
		}

		objectID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		key := objectKeyFor(rc.TenantID, objectID, s.now())
		created, err := s.repo.CreateObject(ctx, tx, rc.TenantID, NewObjectRow{
			ID: objectID, ObjectKey: key, Classification: in.Classification,
			OriginalFilename: domain.NormalizeFilename(in.OriginalFilename),
			ContentType:      in.ContentType, OwnerOrganizationID: owner,
			ActorID: actorPtr(rc.Principal.ActorID),
		})
		if err != nil {
			return err
		}

		// Signed inside the transaction: a store that cannot issue the URL leaves no
		// reserved row behind, so a PENDING document always had a way to be filled.
		url, err := s.store.PresignPut(ctx, s.storage.QuarantineBucket, key, objectstore.PutConstraint{
			ContentType: in.ContentType, ByteSize: in.ByteSize, TTL: s.storage.UploadTTL,
		})
		if err != nil {
			return storeError("presign upload", err)
		}
		out.Document = Document{Object: created}
		out.Upload = &url
		return s.record(ctx, tx, rc, "document.upload.create", created.ID, map[string]any{
			"classification": created.Classification, "byte_size": in.ByteSize,
			"content_type": in.ContentType,
		})
	})
	if err != nil {
		return UploadReservation{}, err
	}
	return out, nil
}

// CompleteUpload records what the client says it uploaded and queues the scan. Neither the
// size nor the digest is trusted: the worker counts and hashes the bytes itself, and it is
// that digest the object is finally stored under. They are recorded because a claim that
// later disagrees with the file is itself worth knowing about.
func (s *Service) CompleteUpload(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID, sha256Hex string, byteSize int64,
) (Document, error) {
	if err := domain.ValidateComplete(sha256Hex, byteSize); err != nil {
		return Document{}, err
	}
	claimed, err := hex.DecodeString(sha256Hex)
	if err != nil {
		return Document{}, fieldError("sha256", "FORMAT", "onaltılık özet çözülemedi")
	}
	scope := scopeOf(rc)

	var out Document
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		object, err := s.repo.GetObject(ctx, tx, rc.TenantID, id, scope)
		if err != nil {
			return err
		}
		if object.ScanStatus != domain.ScanPending {
			return ErrAlreadyCompleted
		}
		// The bytes have to be there before a scan is queued: a worker sent after a file
		// that was never uploaded would fail, retry and eventually dead-letter, and the
		// caller would never learn that its upload did not happen.
		present, err := s.store.Exists(ctx, s.storage.QuarantineBucket, object.ObjectKey)
		if err != nil {
			return storeError("stat upload", err)
		}
		if !present {
			return ErrUploadMissing
		}

		moved, err := s.repo.MarkScanning(ctx, tx, rc.TenantID, object.ID, byteSize, claimed)
		if err != nil {
			return err
		}
		if !moved {
			return ErrAlreadyCompleted
		}
		if _, err := s.repo.CreateVersion(ctx, tx, rc.TenantID, NewVersionRow{
			ObjectID: object.ID, VersionNo: 1, ByteSize: byteSize,
			ContentType:      object.ContentType,
			EncryptionKeyRef: optionalPtr(s.storage.EncryptionKeyRef),
			ActorID:          actorPtr(rc.Principal.ActorID),
		}); err != nil {
			return err
		}
		if _, _, err := outbox.Publish(ctx, tx, outbox.Event{
			TenantID: nullUUID(rc.TenantID), AggregateType: domain.AggregateType,
			AggregateID: object.ID, Type: ScanRequestedEvent,
			Payload:          map[string]any{"documentId": object.ID},
			DeduplicationKey: ScanRequestedEvent + ":" + object.ID.String(),
		}); err != nil {
			return err
		}

		object.ScanStatus = domain.ScanScanning
		object.ByteSize = &byteSize
		object.SHA256 = claimed
		out = Document{Object: object}
		return s.record(ctx, tx, rc, "document.upload.complete", object.ID, map[string]any{
			"byte_size": byteSize, "classification": object.Classification,
		})
	})
	if err != nil {
		return Document{}, err
	}
	return out, nil
}

// resolveOwner decides which provider a document belongs to. A tenant-wide caller may name
// one or none; a provider-scoped caller may only name one it holds, and one that holds
// exactly one organization does not have to say which.
func resolveOwner(scope Scope, requested *uuid.UUID) (*uuid.UUID, error) {
	if !scope.Restricted() {
		return requested, nil
	}
	if requested == nil {
		if len(scope.OrganizationIDs) == 1 {
			only := scope.OrganizationIDs[0]
			return &only, nil
		}
		return nil, fieldError("ownerOrganizationId", "REQUIRED", "belgenin ait olduğu kurum verilmeli")
	}
	for _, id := range scope.OrganizationIDs {
		if id == *requested {
			return requested, nil
		}
	}
	return nil, fieldError("ownerOrganizationId", "SCOPE", "bu kurum için yetkiniz yok")
}
