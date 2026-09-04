// Package application implements the notification use cases: writing a template,
// publishing it, turning a domain event into a message, handing that message to a channel
// adapter and recording every attempt. Transactions are opened here with db.WithTenantTx,
// so a write and its audit row commit together and RLS is bound for every statement.
//
// Four things this package never does.
//
// It never renders anything a template did not declare. Render refuses an undeclared
// variable rather than dropping it, and refuses a value that does not have the shape its
// slot promises. Both refusals write nothing at all.
//
// It never notifies inside somebody else's transaction. A module asks for a notification
// by calling Publish, which inserts an outbox event; the message is written and sent by
// the worker, later, in transactions of its own. A request that rolls back has published
// nothing, and a notification can therefore neither be caused by nor cause the failure of
// a business command.
//
// It never notifies twice for one event. Every request carries a deduplication key, the
// message table is unique on it, and the insert is ON CONFLICT DO NOTHING: a redelivered
// event finds the message it already wrote.
//
// And it never silently declines to tell somebody. Quiet hours, a preference that is off,
// an event with no published template, a recipient with no address — each of them writes a
// SUPPRESSED message naming the reason, because "the member was not told" is an answer an
// operator has to be able to give.
package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/notification/domain"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Permissions guarding this package. notification.manage is in the catalogue from
// migration 000008; notification.read is added by migration 000029. They are separate
// because writing the message everybody gets and reading what one person was actually
// sent are different questions: the first is configuration, the second is a record of
// people's dealings with the payer.
const (
	PermissionManage = "notification.manage"
	PermissionRead   = "notification.read"
)

// Event types this module handles. A domain event reaches notifications through the
// outbox and nowhere else.
const (
	// NotifyRequestedEvent asks for one person to be told about one thing. The payload
	// carries ids, codes and the safe variables — never an address, which the worker
	// resolves itself, and never anything the safe variable catalogue forbids.
	NotifyRequestedEvent = "notification.message.requested"
	// SendRequestedEvent asks for an existing message to be handed to its channel again.
	// It is what a resend publishes; the payload is one message id.
	SendRequestedEvent = "notification.message.send_requested"
)

// MaxSendAttempts bounds how many times one message is offered to its provider. The
// outbox has its own ceiling; this one is about the message rather than the event, so a
// resend of a message that has already been tried ten times does not start again.
const MaxSendAttempts = 10

// Errors mapped by the transport layer to problem codes.
var (
	ErrTemplateNotFound = errors.New("notification: template not found")
	ErrMessageNotFound  = errors.New("notification: message not found")

	// ErrTemplateNotDraft refuses publishing anything but a draft. A published template
	// is immutable and a retired one stays retired.
	ErrTemplateNotDraft = errors.New("notification: only a draft template can be published")
	// ErrTemplateImmutable is the schema refusing an edit to a published template.
	ErrTemplateImmutable = errors.New("notification: a published template cannot be changed")
	// ErrTemplateVersionExists is the unique (tenant, event, channel, locale, version)
	// refusing a second template with the same version number.
	ErrTemplateVersionExists = errors.New("notification: that template version already exists")
	// ErrNoPublishedTemplate is an event nobody has written a message for. It is an error
	// for a resend, which names a message that must already have one, and a suppression
	// reason for a send, which is discovering it for the first time.
	ErrNoPublishedTemplate = errors.New("notification: no published template for that event, channel and locale")

	// ErrMessageNotResendable refuses resending something that was never rendered or is
	// already on its way. A suppressed message has no body to send at all.
	ErrMessageNotResendable = errors.New("notification: the message cannot be sent again")
	// ErrRecipientOptedOut refuses a resend to somebody who has turned this notification
	// off. An operator clicking a button must not override somebody's own answer to "do
	// you want to hear about this".
	ErrRecipientOptedOut = errors.New("notification: the recipient has turned this notification off")

	// ErrSenderUnavailable is the mail server not answering. It is separated from an
	// internal error because it is the one failure a send can usefully retry.
	ErrSenderUnavailable = errors.New("notification: the message channel is unavailable")

	ErrVersionMismatch = errors.New("notification: row version does not match If-Match")
)

// TemplateRecord is one notification.template row.
type TemplateRecord struct {
	ID                uuid.UUID
	EventCode         string
	Channel           string
	Locale            string
	VersionNo         int
	Status            string
	Subject           *string
	Body              string
	DeclaredVariables []string
	PublishedAt       *time.Time
	PublishedBy       *uuid.UUID
	CreatedAt         time.Time
	RowVersion        int64
}

// Template returns the record as the renderer sees it.
func (t TemplateRecord) Template() domain.Template {
	out := domain.Template{
		EventCode: t.EventCode, Channel: t.Channel, Locale: t.Locale,
		VersionNo: t.VersionNo, Body: t.Body, DeclaredVariables: t.DeclaredVariables,
	}
	if t.Subject != nil {
		out.Subject = *t.Subject
	}
	return out
}

// NewTemplateRow is the insert payload of a draft template.
type NewTemplateRow struct {
	EventCode         string
	Channel           string
	Locale            string
	VersionNo         int
	Subject           *string
	Body              string
	DeclaredVariables []string
	ActorID           *uuid.UUID
}

// TemplateQuery is the repository-level template filter.
type TemplateQuery struct {
	EventCode string
	Channel   string
	Locale    string
	Status    string
	After     *httpx.Cursor
	PageSize  int
}

// MessageRecord is one notification.message row.
type MessageRecord struct {
	ID                  uuid.UUID
	EventCode           string
	RecipientType       string
	RecipientID         uuid.UUID
	Channel             string
	Locale              string
	TemplateID          *uuid.UUID
	TemplateVersionNo   *int
	SubjectRendered     *string
	BodyRendered        *string
	SafeVariables       map[string]string
	Status              string
	SuppressedReason    *string
	DedupeKey           *string
	ResentFromMessageID *uuid.UUID
	CreatedAt           time.Time
	SentAt              *time.Time
	RowVersion          int64
}

// NewMessageRow is the insert payload of one message. A suppressed message carries no
// template and no body; a queued one carries both, because the schema will not accept a
// message nobody rendered.
type NewMessageRow struct {
	EventCode           string
	RecipientType       string
	RecipientID         uuid.UUID
	Channel             string
	Locale              string
	TemplateID          *uuid.UUID
	TemplateVersionNo   *int
	SubjectRendered     *string
	BodyRendered        *string
	SafeVariables       map[string]string
	Status              string
	SuppressedReason    *string
	DedupeKey           *string
	ResentFromMessageID *uuid.UUID
	ActorID             *uuid.UUID
}

// MessageQuery is the repository-level message filter.
type MessageQuery struct {
	EventCode     string
	Channel       string
	Status        string
	RecipientType string
	RecipientID   *uuid.UUID
	After         *httpx.Cursor
	PageSize      int
}

// DeliveryRecord is one append-only notification.delivery row.
type DeliveryRecord struct {
	ID                uuid.UUID
	MessageID         uuid.UUID
	AttemptNo         int
	ProviderCode      string
	ProviderMessageID *string
	Outcome           string
	Detail            *string
	AttemptedAt       time.Time
}

// NewDeliveryRow is the insert payload of one attempt. The attempt number is absent on
// purpose: it is computed in SQL from what is already there, so two workers racing to
// record an attempt collide on the unique constraint rather than agreeing on a number
// neither of them checked.
type NewDeliveryRow struct {
	MessageID         uuid.UUID
	ProviderCode      string
	ProviderMessageID *string
	Outcome           string
	Detail            *string
}

// PreferenceRecord is one notification.preference row.
type PreferenceRecord struct {
	ID              uuid.UUID
	RecipientType   string
	RecipientID     uuid.UUID
	EventCode       *string
	Channel         string
	Enabled         bool
	QuietHoursStart *domain.ClockTime
	QuietHoursEnd   *domain.ClockTime
	Timezone        string
	CreatedAt       time.Time
	RowVersion      int64
}

// NewPreferenceRow is one row of a preference replace.
type NewPreferenceRow struct {
	EventCode       *string
	Channel         string
	Enabled         bool
	QuietHoursStart *domain.ClockTime
	QuietHoursEnd   *domain.ClockTime
	Timezone        string
}

// Recipient names who is being told. It travels as a pair everywhere because neither half
// means anything alone.
type Recipient struct {
	Type string
	ID   uuid.UUID
}

// Repository is the persistence port; every method runs inside the caller's transaction,
// which db.WithTenantTx has already bound to the tenant so RLS is active.
type Repository interface {
	CreateTemplate(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewTemplateRow) (TemplateRecord, error)
	GetTemplate(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (TemplateRecord, error)
	ListTemplates(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q TemplateQuery) ([]TemplateRecord, error)
	// NextTemplateVersion is the version number a new draft takes. It is computed in SQL
	// rather than handed in, because a version a caller chose is a version two callers can
	// choose at once.
	NextTemplateVersion(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, eventCode, channel, locale string) (int, error)
	// RetirePublished retires whatever is published for one event, channel and locale, and
	// answers which row it retired. Publishing runs it first, so the partial unique index
	// that keeps one published template per slot is never momentarily violated.
	RetirePublished(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
		eventCode, channel, locale string, actorID *uuid.UUID, at time.Time) (uuid.NullUUID, error)
	// PublishTemplate moves one draft to PUBLISHED. The expected row version is part of
	// the predicate; false means the row moved underneath the caller.
	PublishTemplate(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
		actorID *uuid.UUID, at time.Time, expected int64) (bool, error)
	// FindPublishedTemplate is what the send path renders from. There is at most one.
	FindPublishedTemplate(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
		eventCode, channel, locale string) (TemplateRecord, error)

	// CreateMessage writes one message. It reports created=false when the deduplication
	// key already named a message, which is how a redelivered event notifies once.
	CreateMessage(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewMessageRow) (MessageRecord, bool, error)
	GetMessage(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (MessageRecord, error)
	FindMessageByDedupeKey(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, key string) (MessageRecord, error)
	ListMessages(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q MessageQuery) ([]MessageRecord, error)
	// SetMessageStatus moves a message between the statuses a send walks through. It is
	// the only write the schema allows on a message after it exists, and sent_at is set
	// exactly when the status becomes SENT.
	SetMessageStatus(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, status string, sentAt *time.Time, suppressedReason *string) error

	CreateDelivery(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewDeliveryRow) (DeliveryRecord, error)
	ListDeliveries(ctx context.Context, tx pgx.Tx, tenantID, messageID uuid.UUID) ([]DeliveryRecord, error)

	ListPreferences(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, r Recipient) ([]PreferenceRecord, error)
	// ReplacePreferences writes the whole set for one recipient in one transaction: a
	// merge would leave behind a channel the person believed they had turned off.
	ReplacePreferences(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, r Recipient,
		rows []NewPreferenceRow, actorID *uuid.UUID) ([]PreferenceRecord, error)
	// ResolvePreference answers which row governs one event on one channel: the row that
	// names the event wins over the row that names none. found=false means nobody has
	// said anything, which is a different answer from a row that says "enabled" and is
	// reported separately rather than as a zero value somebody has to read carefully.
	ResolvePreference(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, r Recipient,
		eventCode, channel string) (record PreferenceRecord, found bool, err error)

	// RecipientAddress answers where a message would be sent, or the empty string when
	// the platform holds no address for that recipient on that channel. It is read at
	// send time and never stored: an address in the message row would be a copy of
	// somebody's contact details that outlives every correction to it.
	RecipientAddress(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, r Recipient, channel string) (string, error)
}

// SenderResult is what a channel adapter reports about one attempt.
type SenderResult struct {
	ProviderCode      string
	ProviderMessageID string
	Outcome           string
	Detail            string
	// UndeliveredReason is set by an adapter that accepted the message and delivered
	// nothing — the SMS stub with no provider behind it, the push recorder with no device
	// registry. The attempt is still on record, but the message must not read SENT: this
	// package exists so that a member who was not told can be shown to have not been told,
	// and why, and SENT for something nobody sent is exactly the lie that criterion is
	// about. Empty means the adapter really did hand the message on.
	UndeliveredReason string
}

// ChannelSender hands one rendered message to one channel. The pipeline decides what an
// outcome means; an adapter only says what happened.
//
// An adapter returns an error only when it could not reach a verdict at all. A message
// the provider refused is a REJECTED result, not an error: it is a fact about the message
// worth recording, and the pipeline is what turns it into "stop retrying".
type ChannelSender interface {
	Send(ctx context.Context, address string, m MessageRecord) (SenderResult, error)
}

// ErrNoSender is returned when a process is asked to send on a channel it was not built
// with an adapter for. It is deliberately an error rather than a suppression: a worker
// that quietly recorded "sent" for a channel it cannot reach would be the worst possible
// answer.
var ErrNoSender = errors.New("notification: no adapter is configured for that channel")
