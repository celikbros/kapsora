package application

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	notificationapp "github.com/celikbros/kapsora/internal/notification/application"
	notificationdomain "github.com/celikbros/kapsora/internal/notification/domain"
)

// The messages a booking sends, all five of them, in one file.
//
// Two things are true of every one and are not repeated at each call. **Nothing here
// carries a voucher token, a room number or an amount the recipient should not see**: the
// catalogue of safe variables has no slot any of them could be supplied under, the renderer
// refuses a variable a template did not declare, and the proof of entitlement is fetched
// from behind a sign-in through the deep link. And **who is told is not decided here**: it
// is decided once, in WP-I6-04's BookingRecipients table, because "does the hotel hear
// about this" is a privacy decision rather than a detail of whichever command raises it.
//
// The deduplication key of each message is derived from the booking and the fact -- never
// from a clock -- so a command replayed by a flaky network, or an outbox event delivered
// twice, tells the member once.

// recipientsFor turns WP-I6-04's table into the actual rows to notify. The property is a
// recipient only where that table says so, and only when the booking has one to name.
func recipientsFor(eventCode string, personID, providerOrganizationID uuid.UUID) []notificationapp.Recipient {
	kinds := notificationapp.BookingRecipients[eventCode]
	out := make([]notificationapp.Recipient, 0, len(kinds))
	for _, kind := range kinds {
		switch kind {
		case notificationapp.RecipientMember:
			out = append(out, notificationapp.PersonRecipient(personID))
		case notificationapp.RecipientProperty:
			if providerOrganizationID != uuid.Nil {
				out = append(out, notificationapp.OrganizationRecipient(providerOrganizationID))
			}
		}
	}
	return out
}

// notifyBookingHeld tells the member their room is set aside and when the countdown ends.
// The template names a day rather than a minute, which is WP-I6-04's decision and the right
// one: a countdown printed into an e-mail is already wrong by the time it is read, and the
// exact minute is on the screen the link leads to.
func (s *Service) notifyBookingHeld(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	record BookingRecord, propertyName string,
) error {
	expires := ""
	if record.HoldExpiresAt != nil {
		expires = record.HoldExpiresAt.UTC().Format(time.DateOnly)
	}
	return notificationapp.PublishNotification(ctx, tx, tenantID, notificationapp.Notification{
		EventCode:  notificationapp.EventBookingHeld,
		Key:        record.ID.String(),
		Recipients: recipientsFor(notificationapp.EventBookingHeld, record.PersonID, uuid.Nil),
		Variables: map[string]string{
			notificationdomain.VarReferenceNo:  record.Reference,
			notificationdomain.VarPropertyName: propertyName,
			notificationdomain.VarExpiresAt:    expires,
			notificationdomain.VarDeepLink:     bookingLink(record.ID),
		},
	})
}

// notifyBookingPendingApproval tells the member a person is deciding. Only the member: the
// property has not been promised anything yet, and telling a hotel to expect somebody whose
// request may still be refused would be telling it something that is not true.
func (s *Service) notifyBookingPendingApproval(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	record BookingRecord, propertyName, requestStatus string,
) error {
	return notificationapp.PublishNotification(ctx, tx, tenantID, notificationapp.Notification{
		EventCode:  notificationapp.EventBookingPendingApproval,
		Key:        record.ID.String(),
		Recipients: recipientsFor(notificationapp.EventBookingPendingApproval, record.PersonID, uuid.Nil),
		Variables: map[string]string{
			notificationdomain.VarReferenceNo:  record.Reference,
			notificationdomain.VarPropertyName: propertyName,
			notificationdomain.VarEventDate:    record.CheckIn.Format(time.DateOnly),
			notificationdomain.VarStatusCode:   requestStatus,
			notificationdomain.VarDeepLink:     bookingLink(record.ID),
		},
	})
}

// notifyBookingConfirmed tells both sides the stay is agreed: the member, and the property
// that has to expect them.
//
// The amount is the member's own share and nothing else. The payer's share is not the
// hotel's business and not the member's headline, and the voucher token has no slot here at
// all -- it is fetched from behind a sign-in through the link.
func (s *Service) notifyBookingConfirmed(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	record BookingRecord, room RoomTypeBookingContext, snapshot QuoteSnapshot,
) error {
	return notificationapp.PublishNotification(ctx, tx, tenantID, notificationapp.Notification{
		EventCode: notificationapp.EventBookingConfirmed,
		Key:       record.ID.String(),
		Recipients: recipientsFor(notificationapp.EventBookingConfirmed, record.PersonID,
			room.ProviderOrganizationID),
		Variables: map[string]string{
			notificationdomain.VarReferenceNo:  record.Reference,
			notificationdomain.VarPropertyName: room.PropertyName,
			notificationdomain.VarEventDate:    record.CheckIn.Format(time.DateOnly),
			notificationdomain.VarExpiresAt:    record.CheckOut.Format(time.DateOnly),
			notificationdomain.VarAmount:       snapshot.MemberAmount,
			notificationdomain.VarCurrency:     snapshot.CurrencyCode,
			notificationdomain.VarDeepLink:     bookingLink(record.ID),
		},
	})
}

// notifyBookingCancelled tells both sides a stay is off, with what it cost.
//
// The fee is passed in rather than derived, because only the caller knows it. A hold given
// back and a request refused are stays nobody ever agreed to and carry "0"; WP-I6-03's own
// cancellation carries what the booking's frozen policy actually charged. Deriving it here
// would mean this function re-running the cancellation arithmetic, which is exactly one
// place too many for a fee to be computed.
func (s *Service) notifyBookingCancelled(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	record BookingRecord, reasonCode, feeAmount string,
) error {
	if feeAmount == "" {
		feeAmount = "0"
	}
	currency := ""
	if snapshot, err := decodeQuoteSnapshot(record.QuoteSnapshot); err == nil {
		currency = snapshot.CurrencyCode
	}
	propertyName, providerOrganizationID, err := s.propertyOf(ctx, tx, tenantID, record)
	if err != nil {
		return err
	}
	return notificationapp.PublishNotification(ctx, tx, tenantID, notificationapp.Notification{
		EventCode: notificationapp.EventBookingCancelled,
		// The reason is part of the key, so a hold that expired and a booking somebody
		// cancelled are two messages rather than one that the second delivery swallows.
		Key: record.ID.String() + ":" + reasonCode,
		Recipients: recipientsFor(notificationapp.EventBookingCancelled, record.PersonID,
			providerOrganizationID),
		Variables: map[string]string{
			notificationdomain.VarReferenceNo:  record.Reference,
			notificationdomain.VarPropertyName: propertyName,
			notificationdomain.VarEventDate:    record.CheckIn.Format(time.DateOnly),
			notificationdomain.VarStatusCode:   reasonCode,
			notificationdomain.VarAmount:       feeAmount,
			notificationdomain.VarCurrency:     currency,
			notificationdomain.VarDeepLink:     bookingLink(record.ID),
		},
	})
}

// notifyBookingReminder is the day-before message. Only the member is told: the property
// already knows, from the confirmation, that somebody is coming.
func (s *Service) notifyBookingReminder(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	row ReminderRow,
) error {
	arrival := row.CheckIn.Format(time.DateOnly)
	return notificationapp.PublishNotification(ctx, tx, tenantID, notificationapp.Notification{
		EventCode: notificationapp.EventBookingReminder,
		// The booking and the day it arrives, and not the moment the sweep ran: an hourly
		// job writes one message on its first pass of the day and nothing on the rest.
		Key:        row.BookingID.String() + ":" + arrival,
		Recipients: recipientsFor(notificationapp.EventBookingReminder, row.PersonID, uuid.Nil),
		Variables: map[string]string{
			notificationdomain.VarReferenceNo:  row.Reference,
			notificationdomain.VarPropertyName: row.PropertyName,
			notificationdomain.VarEventDate:    arrival,
			notificationdomain.VarDeepLink:     bookingLink(row.BookingID),
		},
	})
}

// propertyOf reads the building's name and the organization behind it, for a message that
// names both. It is a read rather than a field on the booking because a hotel that changed
// its name changed it, and the message a member receives today should say what the hotel is
// called today.
func (s *Service) propertyOf(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	record BookingRecord,
) (string, uuid.UUID, error) {
	room, err := s.bookings.RoomTypeBookingContext(ctx, tx, tenantID, record.RoomTypeID, nil)
	if err != nil {
		return "", uuid.Nil, err
	}
	return room.PropertyName, room.ProviderOrganizationID, nil
}

// bookingLink is the one link shape these messages carry: a path into the product with no
// query string, so there is nowhere in it for a token to ride along.
func bookingLink(id uuid.UUID) string {
	return notificationapp.DeepLink("bookings", id)
}
