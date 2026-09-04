package notificationpg

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/notification/application"
	"github.com/celikbros/kapsora/internal/notification/domain"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// The single-row and the paged reads select the same columns, and sqlc gives each of them
// its own row type; the messages, deliveries and preferences do the same. Rather than
// several copies of the same mapping — which is exactly how a column ends up carried in
// one read and dropped in another — every row is narrowed to one shape here and mapped
// once.

type template struct {
	ID                uuid.UUID
	EventCode         string
	Channel           string
	Locale            string
	VersionNo         int32
	Status            string
	Subject           *string
	Body              string
	DeclaredVariables []string
	PublishedAt       *time.Time
	PublishedBy       uuid.NullUUID
	CreatedAt         time.Time
	RowVersion        int64
}

func createdTemplateRow(r sqlcgen.CreateNotificationTemplateRow) template { return template(r) }
func templateRow(r sqlcgen.GetNotificationTemplateRow) template           { return template(r) }
func listedTemplateRow(r sqlcgen.ListNotificationTemplatesRow) template   { return template(r) }
func publishedTemplateRow(r sqlcgen.FindPublishedNotificationTemplateRow) template {
	return template(r)
}

func templateOf(r template) application.TemplateRecord {
	return application.TemplateRecord{
		ID: r.ID, EventCode: r.EventCode, Channel: r.Channel, Locale: r.Locale,
		VersionNo: int(r.VersionNo), Status: r.Status, Subject: r.Subject, Body: r.Body,
		DeclaredVariables: variableList(r.DeclaredVariables), PublishedAt: r.PublishedAt,
		PublishedBy: uuidPtr(r.PublishedBy), CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
}

type message struct {
	ID                  uuid.UUID
	EventCode           string
	RecipientType       string
	RecipientID         uuid.UUID
	Channel             string
	Locale              string
	TemplateID          uuid.NullUUID
	TemplateVersionNo   *int32
	SubjectRendered     *string
	BodyRendered        *string
	SafeVariables       []byte
	Status              string
	SuppressedReason    *string
	DedupeKey           *string
	ResentFromMessageID uuid.NullUUID
	CreatedAt           time.Time
	SentAt              *time.Time
	RowVersion          int64
}

func createdMessageRow(r sqlcgen.CreateNotificationMessageRow) message { return message(r) }
func messageRow(r sqlcgen.GetNotificationMessageRow) message           { return message(r) }
func dedupedMessageRow(r sqlcgen.FindNotificationMessageByDedupeKeyRow) message {
	return message(r)
}
func listedMessageRow(r sqlcgen.ListNotificationMessagesRow) message { return message(r) }

func messageOf(r message) application.MessageRecord {
	return application.MessageRecord{
		ID: r.ID, EventCode: r.EventCode, RecipientType: r.RecipientType,
		RecipientID: r.RecipientID, Channel: r.Channel, Locale: r.Locale,
		TemplateID: uuidPtr(r.TemplateID), TemplateVersionNo: intPtr(r.TemplateVersionNo),
		SubjectRendered: r.SubjectRendered, BodyRendered: r.BodyRendered,
		SafeVariables: decodeVariables(r.SafeVariables), Status: r.Status,
		SuppressedReason: r.SuppressedReason, DedupeKey: r.DedupeKey,
		ResentFromMessageID: uuidPtr(r.ResentFromMessageID),
		CreatedAt:           r.CreatedAt, SentAt: r.SentAt, RowVersion: r.RowVersion,
	}
}

type delivery struct {
	ID                uuid.UUID
	MessageID         uuid.UUID
	AttemptNo         int32
	ProviderCode      string
	ProviderMessageID *string
	Outcome           string
	Detail            *string
	AttemptedAt       time.Time
}

func createdDeliveryRow(r sqlcgen.CreateNotificationDeliveryRow) delivery { return delivery(r) }
func listedDeliveryRow(r sqlcgen.ListNotificationDeliveriesRow) delivery  { return delivery(r) }

func deliveryOf(r delivery) application.DeliveryRecord {
	return application.DeliveryRecord{
		ID: r.ID, MessageID: r.MessageID, AttemptNo: int(r.AttemptNo),
		ProviderCode: r.ProviderCode, ProviderMessageID: r.ProviderMessageID,
		Outcome: r.Outcome, Detail: r.Detail, AttemptedAt: r.AttemptedAt,
	}
}

type preference struct {
	ID              uuid.UUID
	RecipientType   string
	RecipientID     uuid.UUID
	EventCode       *string
	Channel         string
	Enabled         bool
	QuietHoursStart pgtype.Time
	QuietHoursEnd   pgtype.Time
	Timezone        string
	CreatedAt       time.Time
	RowVersion      int64
}

func createdPreferenceRow(r sqlcgen.CreateNotificationPreferenceRow) preference {
	return preference(r)
}
func listedPreferenceRow(r sqlcgen.ListNotificationPreferencesRow) preference { return preference(r) }
func resolvedPreferenceRow(r sqlcgen.ResolveNotificationPreferenceRow) preference {
	return preference(r)
}

func preferenceOf(r preference) application.PreferenceRecord {
	return application.PreferenceRecord{
		ID: r.ID, RecipientType: r.RecipientType, RecipientID: r.RecipientID,
		EventCode: r.EventCode, Channel: r.Channel, Enabled: r.Enabled,
		QuietHoursStart: clockPtr(r.QuietHoursStart), QuietHoursEnd: clockPtr(r.QuietHoursEnd),
		Timezone: r.Timezone, CreatedAt: r.CreatedAt, RowVersion: r.RowVersion,
	}
}

// decodeVariables reads safe_variables back. A column that will not parse is answered as
// an empty map rather than an error: the variables are what the message was rendered from
// and the rendered text is already stored, so a log entry with no variables is a smaller
// loss than a log an operator cannot open at all.
func decodeVariables(raw []byte) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

// encodeVariables writes safe_variables. A nil map is stored as an empty object, because
// the column is NOT NULL and "no variables" is a real answer.
func encodeVariables(vars map[string]string) ([]byte, error) {
	if vars == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(vars)
}

// variableList keeps an empty declared list empty rather than nil, so "this template needs
// nothing" reads the same way in Go as the empty array does in the column.
func variableList(names []string) []string {
	if names == nil {
		return []string{}
	}
	return names
}

// clockPtr turns the microseconds a `time` column answers into a time of day. The column
// carries seconds the product has no use for; a window that ends at 07:59:59 is a window
// somebody meant to end at 08:00, and rounding it down here would move it.
func clockPtr(t pgtype.Time) *domain.ClockTime {
	if !t.Valid {
		return nil
	}
	value := domain.ClockTime(t.Microseconds / int64(time.Minute/time.Microsecond))
	return &value
}

// clockValue is clockPtr in the other direction.
func clockValue(c *domain.ClockTime) pgtype.Time {
	if c == nil {
		return pgtype.Time{}
	}
	return pgtype.Time{
		Microseconds: int64(*c) * int64(time.Minute/time.Microsecond),
		Valid:        true,
	}
}

func intPtr(n *int32) *int {
	if n == nil {
		return nil
	}
	value := int(*n)
	return &value
}

func int32Ptr(n *int) *int32 {
	if n == nil {
		return nil
	}
	value := int32(*n) //nolint:gosec // template version numbers come from the database counter
	return &value
}

func uuidPtr(n uuid.NullUUID) *uuid.UUID {
	if !n.Valid {
		return nil
	}
	id := n.UUID
	return &id
}

func optUUID(id *uuid.UUID) uuid.NullUUID {
	if id == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}

// optionalString turns an empty filter into the NULL the query reads as "do not filter".
func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// pageSize clamps what the caller asked for to something the database will answer.
func pageSize(n int) int32 {
	switch {
	case n < 1:
		return 1
	case n > 201:
		return 201
	default:
		return int32(n)
	}
}
