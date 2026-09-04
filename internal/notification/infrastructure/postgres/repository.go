// Package notificationpg implements the notification repository with sqlc. It is
// stateless: every method takes the caller's tenant-bound transaction, so RLS is active
// for every statement and nothing here can read another tenant's messages.
//
// Two methods deliberately report something other than an error.
//
// CreateMessage answers created=false when the deduplication key already named a message.
// That is not a failure: it is the answer "this event has already notified somebody",
// which is what makes the same event delivered twice one message rather than two.
//
// ResolvePreference answers found=false when nobody has said anything about this event
// and channel. A row that says "enabled" and no row at all lead to the same send, and
// telling them apart is what lets the caller say so in a comment rather than in a zero
// value somebody has to read carefully.
package notificationpg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/celikbros/kapsora/internal/notification/application"
	"github.com/celikbros/kapsora/internal/notification/domain"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// PostgreSQL error codes and the constraint names this package maps to named errors.
const (
	uniqueViolation             = "23505"
	integrityConstraintTrigger  = "23000"
	constraintTemplateVersion   = "uq_notification_template_version"
	constraintTemplatePublished = "uq_notification_template_published"
)

// Repository implements application.Repository.
type Repository struct{}

// New returns the repository.
func New() *Repository { return &Repository{} }

var _ application.Repository = (*Repository)(nil)

// CreateTemplate implements application.Repository.
func (Repository) CreateTemplate(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewTemplateRow,
) (application.TemplateRecord, error) {
	row, err := sqlcgen.New(tx).CreateNotificationTemplate(ctx, sqlcgen.CreateNotificationTemplateParams{
		TenantID: tenantID, EventCode: in.EventCode, Channel: in.Channel, Locale: in.Locale,
		VersionNo: int32(in.VersionNo), //nolint:gosec // version numbers come from the database counter
		Subject:   in.Subject, Body: in.Body,
		DeclaredVariables: in.DeclaredVariables, ActorID: optUUID(in.ActorID),
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation &&
			pgErr.ConstraintName == constraintTemplateVersion {
			return application.TemplateRecord{}, application.ErrTemplateVersionExists
		}
		return application.TemplateRecord{}, fmt.Errorf("notification: create template: %w", err)
	}
	return templateOf(createdTemplateRow(row)), nil
}

// GetTemplate implements application.Repository.
func (Repository) GetTemplate(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (
	application.TemplateRecord, error,
) {
	row, err := sqlcgen.New(tx).GetNotificationTemplate(ctx, sqlcgen.GetNotificationTemplateParams{
		TenantID: tenantID, ID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.TemplateRecord{}, application.ErrTemplateNotFound
	}
	if err != nil {
		return application.TemplateRecord{}, fmt.Errorf("notification: get template: %w", err)
	}
	return templateOf(templateRow(row)), nil
}

// ListTemplates implements application.Repository.
func (Repository) ListTemplates(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.TemplateQuery,
) ([]application.TemplateRecord, error) {
	params := sqlcgen.ListNotificationTemplatesParams{
		TenantID: tenantID, EventCode: optionalString(q.EventCode),
		Channel: optionalString(q.Channel), Locale: optionalString(q.Locale),
		Status: optionalString(q.Status), PageSize: pageSize(q.PageSize),
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListNotificationTemplates(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("notification: list templates: %w", err)
	}
	out := make([]application.TemplateRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, templateOf(listedTemplateRow(row)))
	}
	return out, nil
}

// NextTemplateVersion implements application.Repository.
func (Repository) NextTemplateVersion(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	eventCode, channel, locale string,
) (int, error) {
	next, err := sqlcgen.New(tx).NextNotificationTemplateVersion(ctx,
		sqlcgen.NextNotificationTemplateVersionParams{
			TenantID: tenantID, EventCode: eventCode, Channel: channel, Locale: locale,
		})
	if err != nil {
		return 0, fmt.Errorf("notification: next template version: %w", err)
	}
	return int(next), nil
}

// RetirePublished implements application.Repository. An invalid uuid answer is the normal
// case the first time an event gets a template: there was nothing published to replace.
func (Repository) RetirePublished(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	eventCode, channel, locale string, actorID *uuid.UUID, _ time.Time,
) (uuid.NullUUID, error) {
	id, err := sqlcgen.New(tx).RetirePublishedNotificationTemplate(ctx,
		sqlcgen.RetirePublishedNotificationTemplateParams{
			TenantID: tenantID, EventCode: eventCode, Channel: channel, Locale: locale,
			ActorID: optUUID(actorID),
		})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.NullUUID{}, nil
	}
	if err != nil {
		return uuid.NullUUID{}, fmt.Errorf("notification: retire published template: %w", err)
	}
	return uuid.NullUUID{UUID: id, Valid: true}, nil
}

// PublishTemplate implements application.Repository.
func (Repository) PublishTemplate(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	actorID *uuid.UUID, at time.Time, expected int64,
) (bool, error) {
	affected, err := sqlcgen.New(tx).PublishNotificationTemplate(ctx,
		sqlcgen.PublishNotificationTemplateParams{
			TenantID: tenantID, ID: id, PublishedAt: &at,
			ActorID: optUUID(actorID), RowVersion: expected,
		})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation &&
			pgErr.ConstraintName == constraintTemplatePublished {
			// Two publishes for the same slot raced and both retired nothing. The loser is
			// told the row moved rather than being handed a constraint name.
			return false, application.ErrVersionMismatch
		}
		if errors.As(err, &pgErr) && pgErr.Code == integrityConstraintTrigger {
			return false, application.ErrTemplateImmutable
		}
		return false, fmt.Errorf("notification: publish template: %w", err)
	}
	return affected == 1, nil
}

// FindPublishedTemplate implements application.Repository.
func (Repository) FindPublishedTemplate(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	eventCode, channel, locale string,
) (application.TemplateRecord, error) {
	row, err := sqlcgen.New(tx).FindPublishedNotificationTemplate(ctx,
		sqlcgen.FindPublishedNotificationTemplateParams{
			TenantID: tenantID, EventCode: eventCode, Channel: channel, Locale: locale,
		})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.TemplateRecord{}, application.ErrTemplateNotFound
	}
	if err != nil {
		return application.TemplateRecord{}, fmt.Errorf("notification: find published template: %w", err)
	}
	return templateOf(publishedTemplateRow(row)), nil
}

// CreateMessage implements application.Repository. No row returned means the deduplication
// key already named a message, which the caller reads as "already told".
func (Repository) CreateMessage(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewMessageRow,
) (application.MessageRecord, bool, error) {
	variables, err := encodeVariables(in.SafeVariables)
	if err != nil {
		return application.MessageRecord{}, false, fmt.Errorf("notification: encode variables: %w", err)
	}
	row, err := sqlcgen.New(tx).CreateNotificationMessage(ctx, sqlcgen.CreateNotificationMessageParams{
		TenantID: tenantID, EventCode: in.EventCode, RecipientType: in.RecipientType,
		RecipientID: in.RecipientID, Channel: in.Channel, Locale: in.Locale,
		TemplateID: optUUID(in.TemplateID), TemplateVersionNo: int32Ptr(in.TemplateVersionNo),
		SubjectRendered: in.SubjectRendered, BodyRendered: in.BodyRendered,
		SafeVariables: variables, Status: in.Status, SuppressedReason: in.SuppressedReason,
		DedupeKey: in.DedupeKey, ResentFromMessageID: optUUID(in.ResentFromMessageID),
		ActorID: optUUID(in.ActorID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.MessageRecord{}, false, nil
	}
	if err != nil {
		return application.MessageRecord{}, false, fmt.Errorf("notification: create message: %w", err)
	}
	return messageOf(createdMessageRow(row)), true, nil
}

// GetMessage implements application.Repository.
func (Repository) GetMessage(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (
	application.MessageRecord, error,
) {
	row, err := sqlcgen.New(tx).GetNotificationMessage(ctx, sqlcgen.GetNotificationMessageParams{
		TenantID: tenantID, ID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.MessageRecord{}, application.ErrMessageNotFound
	}
	if err != nil {
		return application.MessageRecord{}, fmt.Errorf("notification: get message: %w", err)
	}
	return messageOf(messageRow(row)), nil
}

// FindMessageByDedupeKey implements application.Repository.
func (Repository) FindMessageByDedupeKey(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	key string,
) (application.MessageRecord, error) {
	row, err := sqlcgen.New(tx).FindNotificationMessageByDedupeKey(ctx,
		sqlcgen.FindNotificationMessageByDedupeKeyParams{TenantID: tenantID, DedupeKey: &key})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.MessageRecord{}, application.ErrMessageNotFound
	}
	if err != nil {
		return application.MessageRecord{}, fmt.Errorf("notification: find message by dedupe key: %w", err)
	}
	return messageOf(dedupedMessageRow(row)), nil
}

// ListMessages implements application.Repository.
func (Repository) ListMessages(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.MessageQuery,
) ([]application.MessageRecord, error) {
	params := sqlcgen.ListNotificationMessagesParams{
		TenantID: tenantID, EventCode: optionalString(q.EventCode),
		Channel: optionalString(q.Channel), Status: optionalString(q.Status),
		RecipientType: optionalString(q.RecipientType), PageSize: pageSize(q.PageSize),
	}
	if q.RecipientID != nil {
		params.RecipientID = uuid.NullUUID{UUID: *q.RecipientID, Valid: true}
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.CursorCreatedAt = &at
		params.CursorID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListNotificationMessages(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("notification: list messages: %w", err)
	}
	out := make([]application.MessageRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, messageOf(listedMessageRow(row)))
	}
	return out, nil
}

// SetMessageStatus implements application.Repository.
func (Repository) SetMessageStatus(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	status string, sentAt *time.Time, suppressedReason *string,
) error {
	err := sqlcgen.New(tx).SetNotificationMessageStatus(ctx, sqlcgen.SetNotificationMessageStatusParams{
		TenantID: tenantID, ID: id, Status: status, SentAt: sentAt,
		SuppressedReason: suppressedReason,
	})
	if err != nil {
		return fmt.Errorf("notification: set message status: %w", err)
	}
	return nil
}

// CreateDelivery implements application.Repository.
func (Repository) CreateDelivery(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewDeliveryRow,
) (application.DeliveryRecord, error) {
	row, err := sqlcgen.New(tx).CreateNotificationDelivery(ctx, sqlcgen.CreateNotificationDeliveryParams{
		TenantID: tenantID, MessageID: in.MessageID, ProviderCode: in.ProviderCode,
		ProviderMessageID: in.ProviderMessageID, Outcome: in.Outcome, Detail: in.Detail,
	})
	if err != nil {
		return application.DeliveryRecord{}, fmt.Errorf("notification: create delivery: %w", err)
	}
	return deliveryOf(createdDeliveryRow(row)), nil
}

// ListDeliveries implements application.Repository.
func (Repository) ListDeliveries(ctx context.Context, tx pgx.Tx, tenantID, messageID uuid.UUID) (
	[]application.DeliveryRecord, error,
) {
	rows, err := sqlcgen.New(tx).ListNotificationDeliveries(ctx,
		sqlcgen.ListNotificationDeliveriesParams{TenantID: tenantID, MessageID: messageID})
	if err != nil {
		return nil, fmt.Errorf("notification: list deliveries: %w", err)
	}
	out := make([]application.DeliveryRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, deliveryOf(listedDeliveryRow(row)))
	}
	return out, nil
}

// ListPreferences implements application.Repository.
func (Repository) ListPreferences(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	r application.Recipient,
) ([]application.PreferenceRecord, error) {
	rows, err := sqlcgen.New(tx).ListNotificationPreferences(ctx,
		sqlcgen.ListNotificationPreferencesParams{
			TenantID: tenantID, RecipientType: r.Type, RecipientID: r.ID,
		})
	if err != nil {
		return nil, fmt.Errorf("notification: list preferences: %w", err)
	}
	out := make([]application.PreferenceRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, preferenceOf(listedPreferenceRow(row)))
	}
	return out, nil
}

// ReplacePreferences implements application.Repository. The delete and the inserts are one
// transaction because half a replace is a set that says something nobody asked for.
func (Repository) ReplacePreferences(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	r application.Recipient, rows []application.NewPreferenceRow, actorID *uuid.UUID,
) ([]application.PreferenceRecord, error) {
	q := sqlcgen.New(tx)
	if err := q.DeleteNotificationPreferences(ctx, sqlcgen.DeleteNotificationPreferencesParams{
		TenantID: tenantID, RecipientType: r.Type, RecipientID: r.ID,
	}); err != nil {
		return nil, fmt.Errorf("notification: clear preferences: %w", err)
	}
	out := make([]application.PreferenceRecord, 0, len(rows))
	for _, in := range rows {
		row, err := q.CreateNotificationPreference(ctx, sqlcgen.CreateNotificationPreferenceParams{
			TenantID: tenantID, RecipientType: r.Type, RecipientID: r.ID,
			EventCode: in.EventCode, Channel: in.Channel, Enabled: in.Enabled,
			QuietHoursStart: clockValue(in.QuietHoursStart),
			QuietHoursEnd:   clockValue(in.QuietHoursEnd),
			Timezone:        in.Timezone, ActorID: optUUID(actorID),
		})
		if err != nil {
			return nil, fmt.Errorf("notification: write preference: %w", err)
		}
		out = append(out, preferenceOf(createdPreferenceRow(row)))
	}
	return out, nil
}

// ResolvePreference implements application.Repository.
func (Repository) ResolvePreference(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	r application.Recipient, eventCode, channel string,
) (application.PreferenceRecord, bool, error) {
	row, err := sqlcgen.New(tx).ResolveNotificationPreference(ctx,
		sqlcgen.ResolveNotificationPreferenceParams{
			TenantID: tenantID, RecipientType: r.Type, RecipientID: r.ID,
			Channel: channel, EventCode: &eventCode,
		})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.PreferenceRecord{}, false, nil
	}
	if err != nil {
		return application.PreferenceRecord{}, false, fmt.Errorf("notification: resolve preference: %w", err)
	}
	return preferenceOf(resolvedPreferenceRow(row)), true, nil
}

// RecipientAddress implements application.Repository.
//
// Only an ACTOR on the EMAIL channel has an address today: iam.actor.email is the one
// contact column in the schema. A PERSON has no contact row of their own and an
// ORGANIZATION has none either, so both answer the empty string and the send writes a
// SUPPRESSED message saying NO_ADDRESS. That is the honest answer — a member who cannot be
// e-mailed is shown as not told, with the reason, rather than silently skipped — and the
// day a contact table lands this method grows a branch rather than the pipeline changing.
func (Repository) RecipientAddress(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	r application.Recipient, channel string,
) (string, error) {
	if r.Type != domain.RecipientActor || channel != domain.ChannelEmail {
		return "", nil
	}
	address, err := sqlcgen.New(tx).NotificationActorEmail(ctx, sqlcgen.NotificationActorEmailParams{
		ActorID: r.ID, TenantID: tenantID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("notification: read recipient address: %w", err)
	}
	return address, nil
}
