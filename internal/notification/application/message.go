package application

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/notification/domain"
	"github.com/celikbros/kapsora/internal/platform/outbox"
)

// MessageDetail is one message with every attempt made on it. The attempts travel with it
// because "was the member told" is not answered by a status alone: a message that says
// SENT after two failures and a message that went first time are the same status and
// different stories.
type MessageDetail struct {
	Message    MessageRecord
	Deliveries []DeliveryRecord
}

// GetMessage reads one message and its delivery attempts.
func (s *Service) GetMessage(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID,
) (MessageDetail, error) {
	var out MessageDetail
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		record, err := s.repo.GetMessage(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		attempts, err := s.repo.ListDeliveries(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		out = MessageDetail{Message: record, Deliveries: attempts}
		return nil
	})
	if err != nil {
		return MessageDetail{}, err
	}
	return out, nil
}

// ListMessages answers one keyset page of the message log. Suppressed messages are on it
// like any other, which is the whole point: an operator looking for "why was this member
// not told" finds the row that says so rather than an absence they have to interpret.
func (s *Service) ListMessages(ctx context.Context, rc identity.RequestContext,
	filter MessageFilter,
) (MessagePage, error) {
	after, pageSize, err := s.paging(filter.Cursor, filter.Limit)
	if err != nil {
		return MessagePage{}, err
	}
	query := MessageQuery{
		EventCode: filter.EventCode, Channel: filter.Channel, Status: filter.Status,
		RecipientType: filter.RecipientType, RecipientID: filter.RecipientID,
		After: after, PageSize: pageSize + 1,
	}
	var rows []MessageRecord
	if err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		rows, err = s.repo.ListMessages(ctx, tx, rc.TenantID, query)
		return err
	}); err != nil {
		return MessagePage{}, err
	}
	page := MessagePage{Items: rows}
	if len(rows) > pageSize {
		page.Items = rows[:pageSize]
		page.NextCursor = s.cursors.Encode(messageCursor(page.Items[len(page.Items)-1]))
	}
	return page, nil
}

// ResendMessage sends what one message said, again.
//
// It writes a new message rather than re-queuing the old one. The original is evidence of
// what happened — it went out at this time, it failed twice, it bounced — and re-queuing
// it would overwrite that story with the new one. The copy carries the same rendered text
// and names the message it came from, so the log shows both halves.
//
// The copy carries no deduplication key. A resend is somebody deciding to send this
// again, which is exactly the thing the key exists to stop happening by accident, and
// keeping the key would make the second send silently do nothing.
//
// Two rules, and the reason for each. A message with no rendered body cannot be resent:
// there is nothing to send, because it was suppressed before anything was written. And a
// recipient who has turned this notification off is not resent to: an operator clicking a
// button must not override somebody's answer to "do you want to hear about this". Quiet
// hours are deliberately not re-checked — an operator resending has already decided about
// the timing, and the member's own answer about *whether* still holds.
func (s *Service) ResendMessage(ctx context.Context, rc identity.RequestContext,
	id uuid.UUID,
) (MessageRecord, error) {
	var copied MessageRecord
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		original, err := s.repo.GetMessage(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		if original.BodyRendered == nil || original.TemplateID == nil {
			return ErrMessageNotResendable
		}
		switch original.Status {
		case domain.MessageSent, domain.MessageFailed:
		default:
			// QUEUED and SENDING are already on their way; SUPPRESSED has no body and
			// has been caught above.
			return ErrMessageNotResendable
		}

		pref, found, err := s.repo.ResolvePreference(ctx, tx, rc.TenantID,
			Recipient{Type: original.RecipientType, ID: original.RecipientID},
			original.EventCode, original.Channel)
		if err != nil {
			return err
		}
		if found && !pref.Enabled {
			return ErrRecipientOptedOut
		}

		source := original.ID
		record, _, err := s.repo.CreateMessage(ctx, tx, rc.TenantID, NewMessageRow{
			EventCode: original.EventCode, RecipientType: original.RecipientType,
			RecipientID: original.RecipientID, Channel: original.Channel,
			Locale: original.Locale, TemplateID: original.TemplateID,
			TemplateVersionNo: original.TemplateVersionNo,
			SubjectRendered:   original.SubjectRendered, BodyRendered: original.BodyRendered,
			SafeVariables: original.SafeVariables, Status: domain.MessageQueued,
			ResentFromMessageID: &source, ActorID: actorPtr(rc.Principal.ActorID),
		})
		if err != nil {
			return err
		}
		copied = record

		// The send is published rather than performed: the API process holds no channel
		// adapter, and a resend must not be able to hold a request open while a mail
		// relay thinks about it.
		if _, _, err := outbox.Publish(ctx, tx, outbox.Event{
			TenantID:      uuid.NullUUID{UUID: rc.TenantID, Valid: true},
			AggregateType: domain.AggregateType,
			AggregateID:   record.ID,
			Type:          SendRequestedEvent,
			Payload:       sendPayload{MessageID: record.ID},
			// One event per copy. The copy's id is new every time, so an operator who
			// clicks twice gets two messages — which is what they asked for — while a
			// redelivery of one event still sends one.
			DeduplicationKey: SendRequestedEvent + ":" + record.ID.String(),
		}); err != nil {
			return err
		}
		return s.record(ctx, tx, rc, "notification.message.resend", record.ID, map[string]any{
			"event_code": record.EventCode, "channel": record.Channel,
			"recipient_type": record.RecipientType,
			"source_status":  original.Status, "source_message_id": original.ID,
		})
	})
	if err != nil {
		return MessageRecord{}, err
	}
	return copied, nil
}
