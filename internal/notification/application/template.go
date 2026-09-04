package application

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/notification/domain"
)

// NewTemplateInput is the createNotificationTemplate command. The version number is
// deliberately absent: it is computed in SQL from what already exists, because a number
// the caller chose is a number two callers can choose at once.
type NewTemplateInput struct {
	EventCode         string
	Channel           string
	Locale            string
	Subject           string
	Body              string
	DeclaredVariables []string
}

// CreateTemplate writes a draft. Nothing renders from a draft: the send path reads the
// published template for a slot and there is at most one, so a half-written message
// cannot reach anybody while it is being written.
func (s *Service) CreateTemplate(ctx context.Context, rc identity.RequestContext,
	in NewTemplateInput,
) (TemplateRecord, error) {
	template := domain.Template{
		EventCode: in.EventCode, Channel: in.Channel, Locale: in.Locale,
		Subject: in.Subject, Body: in.Body, DeclaredVariables: in.DeclaredVariables,
	}
	if err := domain.ValidateTemplate(template); err != nil {
		return TemplateRecord{}, err
	}

	var created TemplateRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		version, err := s.repo.NextTemplateVersion(ctx, tx, rc.TenantID,
			template.EventCode, template.Channel, template.Locale)
		if err != nil {
			return err
		}
		record, err := s.repo.CreateTemplate(ctx, tx, rc.TenantID, NewTemplateRow{
			EventCode: template.EventCode, Channel: template.Channel, Locale: template.Locale,
			VersionNo: version, Subject: optionalPtr(template.Subject), Body: template.Body,
			DeclaredVariables: in.DeclaredVariables, ActorID: actorPtr(rc.Principal.ActorID),
		})
		if err != nil {
			return err
		}
		created = record
		return s.record(ctx, tx, rc, "notification.template.create", record.ID, map[string]any{
			"event_code": record.EventCode, "channel": record.Channel,
			"locale": record.Locale, "version_no": record.VersionNo,
			"variable_count": len(record.DeclaredVariables),
		})
	})
	if err != nil {
		return TemplateRecord{}, err
	}
	return created, nil
}

// GetTemplate reads one template.
func (s *Service) GetTemplate(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID,
) (TemplateRecord, error) {
	var record TemplateRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		record, err = s.repo.GetTemplate(ctx, tx, rc.TenantID, id)
		return err
	})
	if err != nil {
		return TemplateRecord{}, err
	}
	return record, nil
}

// ListTemplates answers one keyset page of templates.
func (s *Service) ListTemplates(ctx context.Context, rc identity.RequestContext,
	filter TemplateFilter,
) (TemplatePage, error) {
	after, pageSize, err := s.paging(filter.Cursor, filter.Limit)
	if err != nil {
		return TemplatePage{}, err
	}
	query := TemplateQuery{
		EventCode: filter.EventCode, Channel: filter.Channel, Locale: filter.Locale,
		Status: filter.Status, After: after, PageSize: pageSize + 1,
	}
	var rows []TemplateRecord
	if err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		rows, err = s.repo.ListTemplates(ctx, tx, rc.TenantID, query)
		return err
	}); err != nil {
		return TemplatePage{}, err
	}
	page := TemplatePage{Items: rows}
	if len(rows) > pageSize {
		page.Items = rows[:pageSize]
		page.NextCursor = s.cursors.Encode(templateCursor(page.Items[len(page.Items)-1]))
	}
	return page, nil
}

// PublishTemplate makes one draft the template that renders this event from now on, and
// retires the one it replaces in the same transaction.
//
// Publishing retires rather than refuses. The alternative — refusing a second publish
// until somebody retires the first — would mean an event with no published template for
// however long the two commands are apart, and a message that arrives in that gap is
// suppressed with NO_TEMPLATE. Replacing in one transaction has no such gap: a message
// rendered a microsecond either side of it finds exactly one published template, and it
// is the older one before and the newer one after.
//
// The template that was retired keeps everything it said. Messages already sent name it
// by id and version and carry their own rendered text, so retiring it changes nothing
// about what anybody was told.
func (s *Service) PublishTemplate(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID, expected int64,
) (TemplateRecord, error) {
	var published TemplateRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		draft, err := s.repo.GetTemplate(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		if draft.Status != domain.TemplateDraft {
			return ErrTemplateNotDraft
		}
		// The row is re-validated on the way out rather than trusted because it passed on
		// the way in: a template seeded by a migration or written before a rule tightened
		// has never been through CreateTemplate.
		if err := domain.ValidateTemplate(draft.Template()); err != nil {
			return err
		}

		now := s.now()
		retired, err := s.repo.RetirePublished(ctx, tx, rc.TenantID,
			draft.EventCode, draft.Channel, draft.Locale, actorPtr(rc.Principal.ActorID), now)
		if err != nil {
			return err
		}
		ok, err := s.repo.PublishTemplate(ctx, tx, rc.TenantID, id,
			actorPtr(rc.Principal.ActorID), now, expected)
		if err != nil {
			return err
		}
		if !ok {
			return ErrVersionMismatch
		}
		if published, err = s.repo.GetTemplate(ctx, tx, rc.TenantID, id); err != nil {
			return err
		}
		detail := map[string]any{
			"event_code": published.EventCode, "channel": published.Channel,
			"locale": published.Locale, "version_no": published.VersionNo,
			"replaced": retired.Valid,
		}
		if retired.Valid {
			detail["retired_template_id"] = retired.UUID
		}
		return s.record(ctx, tx, rc, "notification.template.publish", id, detail)
	})
	if err != nil {
		return TemplateRecord{}, err
	}
	return published, nil
}

// publishedTemplateFor reads the template a send renders from, turning "there is none"
// into the sentinel the send path treats as a suppression rather than a failure.
func (s *Service) publishedTemplateFor(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	eventCode, channel, locale string,
) (TemplateRecord, error) {
	record, err := s.repo.FindPublishedTemplate(ctx, tx, tenantID, eventCode, channel, locale)
	if errors.Is(err, ErrTemplateNotFound) {
		return TemplateRecord{}, ErrNoPublishedTemplate
	}
	return record, err
}
