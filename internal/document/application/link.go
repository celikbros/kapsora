package application

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/document/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// NewLinkInput says which record a document belongs to and what it is there.
type NewLinkInput struct {
	AggregateType    string
	AggregateID      uuid.UUID
	DocumentTypeCode string
	Purpose          string
	// RequiredPermission narrows who may download through this link. Empty means
	// document.read is enough; naming health.clinical.read is how a clinical attachment
	// stops being reachable by everybody who may read documents.
	RequiredPermission string
}

// LinkDocument attaches a document to a record. A link is how a request says "this is the
// invoice"; the same object may be linked to several records, which is what stops the same
// file being uploaded once per place it is needed.
func (s *Service) LinkDocument(ctx context.Context, rc identity.RequestContext,
	documentID uuid.UUID, in NewLinkInput,
) (LinkRecord, error) {
	if err := domain.ValidateLink(in.AggregateType, in.DocumentTypeCode, in.Purpose,
		in.RequiredPermission); err != nil {
		return LinkRecord{}, err
	}
	if in.AggregateID == uuid.Nil {
		return LinkRecord{}, fieldError("aggregateId", "REQUIRED", "kayıt kimliği verilmeli")
	}

	var out LinkRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		object, err := s.repo.GetObject(ctx, tx, rc.TenantID, documentID, scopeOf(rc))
		if err != nil {
			return err
		}
		out, err = s.repo.CreateLink(ctx, tx, rc.TenantID, NewLinkRow{
			ObjectID: object.ID, AggregateType: in.AggregateType, AggregateID: in.AggregateID,
			DocumentTypeCode: in.DocumentTypeCode, Purpose: optionalPtr(in.Purpose),
			RequiredPermission: optionalPtr(in.RequiredPermission),
			ActorID:            actorPtr(rc.Principal.ActorID),
		})
		if err != nil {
			return err
		}
		return s.record(ctx, tx, rc, "document.link.create", object.ID, map[string]any{
			"link_id": out.ID, "aggregate_type": in.AggregateType,
			"aggregate_id": in.AggregateID, "document_type_code": in.DocumentTypeCode,
			"required_permission": in.RequiredPermission,
		})
	})
	if err != nil {
		return LinkRecord{}, err
	}
	return out, nil
}

// UnlinkDocument removes one link. The document itself is untouched: a file that is no
// longer this request's invoice is still a file somebody uploaded, and deleting it here
// would delete it from every other record it belongs to as well.
func (s *Service) UnlinkDocument(ctx context.Context, rc identity.RequestContext,
	documentID, linkID uuid.UUID,
) error {
	return s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		object, err := s.repo.GetObject(ctx, tx, rc.TenantID, documentID, scopeOf(rc))
		if err != nil {
			return err
		}
		removed, err := s.repo.DeleteLink(ctx, tx, rc.TenantID, object.ID, linkID)
		if err != nil {
			return err
		}
		if !removed {
			return ErrLinkNotFound
		}
		return s.record(ctx, tx, rc, "document.link.delete", object.ID, map[string]any{
			"link_id": linkID,
		})
	})
}
