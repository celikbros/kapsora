package application

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/workflow/domain"
)

// AddComment records what somebody said about a piece of work, and who it was said to.
//
// The visibility is stored, not interpreted. A comment a provider or a member can read may
// not carry clinical detail, and that is a rule about content which the health package
// (M5) enforces; this package's job is to make sure the audience a comment was written for
// is on the row, so there is something to judge it against later.
func (s *Service) AddComment(ctx context.Context, rc identity.RequestContext,
	workItemID uuid.UUID, in domain.NewComment,
) (CommentRecord, error) {
	if err := in.Validate(); err != nil {
		return CommentRecord{}, err
	}
	scope := scopeOf(rc)
	var created CommentRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		item, err := s.repo.GetItem(ctx, tx, rc.TenantID, workItemID, scope)
		if err != nil {
			return err
		}
		created, err = s.addComment(ctx, tx, rc, item, in)
		return err
	})
	if err != nil {
		return CommentRecord{}, err
	}
	return created, nil
}

// addComment writes one comment against an item already read through the caller's
// boundary. It is shared with completeWorkItem, whose optional note is the same row.
//
// The body is never audited. It is free text a person typed and may name anybody;
// audit.SanitizeDetail would drop a key called "body" only if it looked like a name, so
// the safe thing is not to pass it at all.
func (s *Service) addComment(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	item ItemRecord, in domain.NewComment,
) (CommentRecord, error) {
	itemID := item.ID
	record, err := s.repo.CreateComment(ctx, tx, rc.TenantID, NewCommentRow{
		AggregateType: item.AggregateType, AggregateID: item.AggregateID,
		WorkItemID: &itemID, Visibility: in.Visibility, Body: in.Body,
		AuthorActorID: actorPtr(rc.Principal.ActorID),
	})
	if err != nil {
		return CommentRecord{}, err
	}
	if err := s.record(ctx, tx, rc, "work_item.comment", "WORK_ITEM", itemID, map[string]any{
		"comment_id": record.ID, "visibility": record.Visibility,
		"aggregate_type": item.AggregateType, "aggregate_id": item.AggregateID,
	}); err != nil {
		return CommentRecord{}, err
	}
	return record, nil
}

// ListComments reads the comments of one work item, oldest first. A caller may be narrowed
// to the audiences it belongs to: a provider-facing screen asks for PROVIDER comments and
// is not handed the internal ones by accident.
func (s *Service) ListComments(ctx context.Context, rc identity.RequestContext,
	workItemID uuid.UUID, visibilities []string, limit int,
) ([]CommentRecord, error) {
	for _, visibility := range visibilities {
		if err := (domain.NewComment{Visibility: visibility, Body: "-"}).Validate(); err != nil {
			return nil, fieldError("visibility", "ENUM", "geçersiz görünürlük")
		}
	}
	scope := scopeOf(rc)
	pageSize := clampComments(limit)
	var rows []CommentRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.repo.GetItem(ctx, tx, rc.TenantID, workItemID, scope); err != nil {
			return err
		}
		var err error
		rows, err = s.repo.ListItemComments(ctx, tx, rc.TenantID, workItemID, scope, visibilities, pageSize)
		return err
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// MaxComments is how many comments one read returns. A work item's conversation is short
// by nature, so the list is capped rather than paged: a cursor here would be a page nobody
// ever turns.
const MaxComments = 500

func clampComments(limit int) int {
	switch {
	case limit <= 0:
		return MaxComments
	case limit > MaxComments:
		return MaxComments
	default:
		return limit
	}
}
