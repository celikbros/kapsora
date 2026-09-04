package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/audit"
	"github.com/celikbros/kapsora/internal/notification/domain"
	"github.com/celikbros/kapsora/internal/platform/outbox"
)

// Request is what a module asks for when it wants somebody told about something. It
// carries ids, codes and safe variables and nothing else: no address, which the worker
// resolves for itself at send time, and nothing the safe variable catalogue forbids,
// which Validate refuses here rather than at the far end of a queue.
type Request struct {
	EventCode string
	Recipient Recipient
	Channel   string
	Locale    string
	Variables map[string]string
	// DedupeKey is what makes this event notify once. Two requests with the same key are
	// one message however many times either of them is delivered, so it has to name the
	// thing that happened — the authorization, the request, the day — rather than the
	// moment the code ran.
	DedupeKey string
}

// notifyPayload is the wire form of a Request. It is a type of its own so the JSON names
// are a contract between the publisher and the worker rather than a property of the
// struct the rest of the package uses.
type notifyPayload struct {
	EventCode     string            `json:"eventCode"`
	RecipientType string            `json:"recipientType"`
	RecipientID   uuid.UUID         `json:"recipientId"`
	Channel       string            `json:"channel"`
	Locale        string            `json:"locale"`
	Variables     map[string]string `json:"variables"`
	DedupeKey     string            `json:"dedupeKey"`
}

// sendPayload is the wire form of "send this message again".
type sendPayload struct {
	MessageID uuid.UUID `json:"messageId"`
}

// Validate checks everything about a request that can be checked without a database,
// including the variables. Screening them here means a caller that tries to notify
// somebody of their diagnosis is refused inside its own transaction, where the refusal
// still means something, rather than dead-lettering an event an hour later.
func (r Request) Validate() error {
	ve := &domain.ValidationError{}
	if !domain.ValidEventCode(r.EventCode) {
		ve.Add("eventCode", "FORMAT", "nokta ile ayrılmış küçük harf olay kodu olmalı")
	}
	if !domain.ValidRecipientType(r.Recipient.Type) {
		ve.Add("recipientType", "ENUM", "geçerli bir alıcı türü olmalı")
	}
	if r.Recipient.ID == uuid.Nil {
		ve.Add("recipientId", "REQUIRED", "alıcı kimliği gerekli")
	}
	if !domain.ValidChannel(r.Channel) {
		ve.Add("channel", "ENUM", "geçerli bir kanal olmalı")
	}
	if !domain.ValidLocale(r.Locale) {
		ve.Add("locale", "FORMAT", "tr veya tr-TR biçiminde olmalı")
	}
	if err := domain.ValidateDedupeKey(r.DedupeKey); err != nil {
		var inner *domain.ValidationError
		if errors.As(err, &inner) {
			ve.Fields = append(ve.Fields, inner.Fields...)
		}
	}
	if err := domain.ScreenVariables(r.Variables); err != nil {
		var inner *domain.ValidationError
		if errors.As(err, &inner) {
			ve.Fields = append(ve.Fields, inner.Fields...)
		}
	}
	return ve.OrNil()
}

// Publish asks for a notification from inside somebody else's transaction. It writes one
// outbox row and nothing else: no template is read, no message is written and no adapter
// is called, so a notification can neither slow a request down nor make one fail.
//
// The corollary matters just as much. A transaction that rolls back has published
// nothing, so a request that was never committed has notified nobody — there is no path
// from a business command to an e-mail that does not go through a committed outbox row.
func Publish(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, r Request) error {
	if tenantID == uuid.Nil {
		return errors.New("notification: a notification needs a tenant")
	}
	if err := r.Validate(); err != nil {
		return err
	}
	_, _, err := outbox.Publish(ctx, tx, outbox.Event{
		TenantID: uuid.NullUUID{UUID: tenantID, Valid: true},
		// The message does not exist yet, so the aggregate is the person being told.
		AggregateType: domain.AggregateType,
		AggregateID:   r.Recipient.ID,
		Type:          NotifyRequestedEvent,
		Payload: notifyPayload{
			EventCode: r.EventCode, RecipientType: r.Recipient.Type,
			RecipientID: r.Recipient.ID, Channel: r.Channel, Locale: r.Locale,
			Variables: r.Variables, DedupeKey: r.DedupeKey,
		},
		// The outbox refuses the second copy of one request; the message table refuses the
		// second copy of one delivery. Either alone would be enough for the common case,
		// and both together are what makes "the same event notifies once" true whichever
		// half is retried.
		DeduplicationKey: NotifyRequestedEvent + ":" + r.DedupeKey,
	})
	return err
}

// HandleNotifyRequested is the worker's handler for NotifyRequestedEvent: it writes the
// message and hands it to its channel. It is idempotent — a redelivery finds the message
// the first delivery wrote and continues from wherever that one stopped.
func (s *Service) HandleNotifyRequested(ctx context.Context, d outbox.Delivery) error {
	if !d.TenantID.Valid {
		return outbox.Permanent(errors.New("notification: the event carries no tenant"))
	}
	var payload notifyPayload
	if err := json.Unmarshal(d.Payload, &payload); err != nil {
		return outbox.Permanent(fmt.Errorf("notification: decode payload: %w", err))
	}
	request := Request{
		EventCode: payload.EventCode,
		Recipient: Recipient{Type: payload.RecipientType, ID: payload.RecipientID},
		Channel:   payload.Channel, Locale: payload.Locale,
		Variables: payload.Variables, DedupeKey: payload.DedupeKey,
	}
	if err := request.Validate(); err != nil {
		return outbox.Permanent(err)
	}

	message, err := s.Materialize(ctx, d.TenantID.UUID, request)
	if err != nil {
		return classifyForOutbox(err)
	}
	return s.DeliverMessage(ctx, d.TenantID.UUID, message.ID)
}

// HandleSendRequested is the worker's handler for SendRequestedEvent, which a resend
// publishes. The message already exists; all that is left is to offer it to its channel.
func (s *Service) HandleSendRequested(ctx context.Context, d outbox.Delivery) error {
	if !d.TenantID.Valid {
		return outbox.Permanent(errors.New("notification: the event carries no tenant"))
	}
	var payload sendPayload
	if err := json.Unmarshal(d.Payload, &payload); err != nil {
		return outbox.Permanent(fmt.Errorf("notification: decode payload: %w", err))
	}
	if payload.MessageID == uuid.Nil {
		return outbox.Permanent(errors.New("notification: the event names no message"))
	}
	return s.DeliverMessage(ctx, d.TenantID.UUID, payload.MessageID)
}

// Materialize writes the message one request asks for, or finds the one an earlier
// delivery of the same request already wrote. It is exported because the worker is not
// the only caller worth having: a test drives it directly, and so does the resend path
// when it needs the message a request produced.
//
// The order of the checks is the point of the function. The variables are screened before
// anything is read, so a value that may never leave the system is refused before it can be
// written even into a message that was going to be suppressed. Then the template, then
// the recipient's preferences, then whether there is anywhere to send it — and only then
// is anything rendered.
func (s *Service) Materialize(ctx context.Context, tenantID uuid.UUID, r Request) (MessageRecord, error) {
	if err := r.Validate(); err != nil {
		return MessageRecord{}, err
	}
	var out MessageRecord
	err := s.withSystemTx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		existing, err := s.repo.FindMessageByDedupeKey(ctx, tx, tenantID, r.DedupeKey)
		switch {
		case err == nil:
			out = existing
			return nil
		case !errors.Is(err, ErrMessageNotFound):
			return err
		}

		row, err := s.prepare(ctx, tx, tenantID, r)
		if err != nil {
			return err
		}
		record, created, err := s.repo.CreateMessage(ctx, tx, tenantID, row)
		if err != nil {
			return err
		}
		out = record
		if !created {
			// Another worker wrote the same message between the read above and this
			// insert. That is the race the unique deduplication key exists for, and the
			// row that is already there is the answer.
			return nil
		}
		return s.recordSystem(ctx, tx, tenantID, "notification.message.create", record.ID,
			audit.OutcomeSuccess, map[string]any{
				"event_code": record.EventCode, "channel": record.Channel,
				"recipient_type": record.RecipientType, "locale": record.Locale,
				"status": record.Status, "suppressed_reason": deref(record.SuppressedReason),
				"variable_count": len(record.SafeVariables),
			})
	})
	if err != nil {
		return MessageRecord{}, err
	}
	return out, nil
}

// prepare decides what the message row will say. Every path out of it either produces a
// row or an error; there is no path that produces nothing, because "nothing happened" is
// exactly the answer this module exists to stop giving.
func (s *Service) prepare(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, r Request) (NewMessageRow, error) {
	dedupe := r.DedupeKey
	row := NewMessageRow{
		EventCode: r.EventCode, RecipientType: r.Recipient.Type, RecipientID: r.Recipient.ID,
		Channel: r.Channel, Locale: r.Locale, SafeVariables: r.Variables,
		DedupeKey: &dedupe,
	}

	template, err := s.publishedTemplateFor(ctx, tx, tenantID, r.EventCode, r.Channel, r.Locale)
	if errors.Is(err, ErrNoPublishedTemplate) {
		return suppress(row, domain.SuppressedNoTemplate), nil
	}
	if err != nil {
		return NewMessageRow{}, err
	}

	reason, err := s.suppressionFor(ctx, tx, tenantID, r.Recipient, r.EventCode, r.Channel, s.now())
	if err != nil {
		return NewMessageRow{}, err
	}
	if reason != "" {
		return suppress(row, reason), nil
	}

	if channelNeedsAddress(r.Channel) {
		address, err := s.repo.RecipientAddress(ctx, tx, tenantID, r.Recipient, r.Channel)
		if err != nil {
			return NewMessageRow{}, err
		}
		if address == "" {
			return suppress(row, domain.SuppressedNoAddress), nil
		}
	}

	rendered, err := domain.Render(template.Template(), r.Variables, s.linkBase)
	if err != nil {
		// Nothing is written. The transaction this runs in is rolled back by the caller,
		// so a refused render leaves no message, no audit row and no delivery attempt.
		return NewMessageRow{}, err
	}
	versionNo := template.VersionNo
	templateID := template.ID
	row.Status = domain.MessageQueued
	row.TemplateID = &templateID
	row.TemplateVersionNo = &versionNo
	row.BodyRendered = &rendered.Body
	if template.Channel == domain.ChannelEmail {
		row.SubjectRendered = &rendered.Subject
	}
	return row, nil
}

// suppress turns a prepared row into the record of somebody not being told, and why. The
// safe variables stay on it: they are what an operator needs to see to know what the
// member would have been sent, and they have already been screened.
func suppress(row NewMessageRow, reason string) NewMessageRow {
	row.Status = domain.MessageSuppressed
	row.SuppressedReason = &reason
	return row
}

// channelNeedsAddress reports whether a channel has somewhere to send to. INAPP is
// delivered by being stored — the message log is the inbox — and PUSH has no device
// registry in this milestone, so neither of them is suppressed for want of an address.
func channelNeedsAddress(channel string) bool {
	return channel == domain.ChannelEmail || channel == domain.ChannelSMS
}

// DeliverMessage offers one message to its channel and records what happened. It writes a
// delivery row for every attempt, including the ones where the provider could not be
// reached: "it failed twice and then went" is a different fact from "it went", and only a
// row per attempt can tell them apart.
//
// It is idempotent in the way that matters: a message that has been sent, suppressed,
// rejected for good or tried too many times is left exactly as it is.
func (s *Service) DeliverMessage(ctx context.Context, tenantID, messageID uuid.UUID) error {
	message, address, attempt, err := s.beginAttempt(ctx, tenantID, messageID)
	if err != nil {
		return classifyForOutbox(err)
	}
	if attempt == 0 {
		return nil
	}

	sender, ok := s.senders[message.Channel]
	if !ok {
		// A worker that quietly recorded "sent" for a channel it has no adapter for would
		// be the worst possible answer, so the message stays where it is and the event is
		// retried until somebody wires one in.
		return fmt.Errorf("%w: %s", ErrNoSender, message.Channel)
	}
	result, sendErr := sender.Send(ctx, address, message)
	if sendErr != nil {
		result = SenderResult{
			ProviderCode: providerCodeFor(message.Channel),
			Outcome:      domain.OutcomeError,
			Detail:       sendErr.Error(),
		}
	}
	if !domain.ValidOutcome(result.Outcome) {
		result.Outcome = domain.OutcomeError
	}

	if err := s.finishAttempt(ctx, tenantID, message, result); err != nil {
		return err
	}
	switch result.Outcome {
	case domain.OutcomeAccepted:
		return nil
	case domain.OutcomeRejected, domain.OutcomeBounced:
		// The provider's final word. Retrying would only collect the same answer again,
		// so the event stops here and the message stays FAILED with the attempt on record.
		return outbox.Permanent(fmt.Errorf("notification: %s refused the message: %s",
			result.ProviderCode, result.Detail))
	default:
		if sendErr != nil {
			return senderError(message.Channel, sendErr)
		}
		return senderError(message.Channel, errors.New(result.Detail))
	}
}

// beginAttempt reads the message, decides whether it is worth offering again, resolves the
// address and moves the message to SENDING. It answers attempt=0 when there is nothing
// left to do, which is not an error: it is what a redelivery of a finished message looks
// like.
func (s *Service) beginAttempt(ctx context.Context, tenantID, messageID uuid.UUID) (
	message MessageRecord, address string, attempt int, err error,
) {
	err = s.withSystemTx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		record, err := s.repo.GetMessage(ctx, tx, tenantID, messageID)
		if err != nil {
			return err
		}
		attempts, err := s.repo.ListDeliveries(ctx, tx, tenantID, messageID)
		if err != nil {
			return err
		}
		message = record
		if !attemptable(record, attempts) {
			attempt = 0
			return nil
		}
		if channelNeedsAddress(record.Channel) {
			if address, err = s.repo.RecipientAddress(ctx, tx, tenantID,
				Recipient{Type: record.RecipientType, ID: record.RecipientID},
				record.Channel); err != nil {
				return err
			}
			if address == "" {
				// The address disappeared between the message being written and it being
				// sent. There is nowhere to send it, and there never will be until
				// somebody fixes the record, so the attempt is abandoned rather than
				// retried forever.
				attempt = 0
				return s.repo.SetMessageStatus(ctx, tx, tenantID, messageID, domain.MessageFailed, nil, nil)
			}
		}
		attempt = len(attempts) + 1
		return s.repo.SetMessageStatus(ctx, tx, tenantID, messageID, domain.MessageSending, nil, nil)
	})
	return message, address, attempt, err
}

// finishAttempt writes the delivery row and the status the outcome implies, together.
func (s *Service) finishAttempt(ctx context.Context, tenantID uuid.UUID,
	message MessageRecord, result SenderResult,
) error {
	status := domain.MessageFailed
	var sentAt *time.Time
	var suppressedReason *string
	switch {
	case result.Outcome != domain.OutcomeAccepted:
		// FAILED, and the retry logic below decides whether to try again.
	case result.UndeliveredReason != "":
		// The adapter accepted the message and delivered nothing. The attempt is still
		// written below, so the operator can see it was tried; the message itself is a
		// suppression naming why, because SENT would say the member was told.
		reason := domain.SuppressedNotDeliverable
		status, suppressedReason = domain.MessageSuppressed, &reason
	default:
		at := s.now()
		status, sentAt = domain.MessageSent, &at
	}
	return s.withSystemTx(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		delivery, err := s.repo.CreateDelivery(ctx, tx, tenantID, NewDeliveryRow{
			MessageID:         message.ID,
			ProviderCode:      result.ProviderCode,
			ProviderMessageID: optionalPtr(result.ProviderMessageID),
			Outcome:           result.Outcome,
			Detail:            optionalPtr(truncateDetail(result.Detail)),
		})
		if err != nil {
			return err
		}
		if err := s.repo.SetMessageStatus(ctx, tx, tenantID, message.ID, status, sentAt, suppressedReason); err != nil {
			return err
		}
		outcome := audit.OutcomeSuccess
		if result.Outcome != domain.OutcomeAccepted {
			outcome = audit.OutcomeFailure
		}
		return s.recordSystem(ctx, tx, tenantID, "notification.message.deliver", message.ID,
			outcome, map[string]any{
				"event_code": message.EventCode, "channel": message.Channel,
				"provider_code": result.ProviderCode, "outcome": result.Outcome,
				"attempt_no": delivery.AttemptNo, "status": status,
			})
	})
}

// attemptable is the whole retry policy of a message in one function. A permanent
// rejection stops the retries here rather than by exhausting the outbox's ceiling, which
// is the difference between "we stopped trying" and "we gave up after an hour".
func attemptable(message MessageRecord, attempts []DeliveryRecord) bool {
	switch message.Status {
	case domain.MessageSent, domain.MessageSuppressed:
		return false
	}
	if len(attempts) >= MaxSendAttempts {
		return false
	}
	if len(attempts) == 0 {
		return true
	}
	switch attempts[len(attempts)-1].Outcome {
	case domain.OutcomeRejected, domain.OutcomeBounced:
		return false
	}
	return true
}

// providerCodeFor names the adapter an attempt went through when the adapter itself did
// not get far enough to say.
func providerCodeFor(channel string) string {
	switch channel {
	case domain.ChannelEmail:
		return "SMTP"
	case domain.ChannelSMS:
		return "SMS_STUB"
	case domain.ChannelPush:
		return "PUSH_RECORDER"
	default:
		return "INAPP_RECORDER"
	}
}

// truncateDetail bounds a provider's answer to what notification.delivery.detail accepts.
func truncateDetail(detail string) string {
	if len(detail) <= domain.MaxProviderDetail {
		return detail
	}
	out := detail[:domain.MaxProviderDetail]
	for len(out) > 0 && out[len(out)-1]&0xC0 == 0x80 {
		out = out[:len(out)-1]
	}
	return out
}

// classifyForOutbox turns an error into the retry decision the dispatcher reads. A refused
// render and a malformed request are permanent: delivering the same event again would
// produce the same refusal, and a message that must not be sent must not be retried into
// existence.
func classifyForOutbox(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrValidation), errors.Is(err, ErrMessageNotFound):
		return outbox.Permanent(err)
	default:
		return err
	}
}
