package application

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	notificationapp "github.com/celikbros/kapsora/internal/notification/application"
	notificationdomain "github.com/celikbros/kapsora/internal/notification/domain"
)

// The two messages WP-I6-03 raises, on the same terms as the five WP-I6-02 raises: no
// token, no room number, no amount the recipient should not see, and who is told decided
// once in WP-I6-04's BookingRecipients table rather than here.

// notifyNoShowReported tells the member what has been claimed and what the contract says it
// costs, so the first they hear of a no-show is not an invoice.
//
// Only the member. The property is the one who reported it, and telling a hotel what it has
// just said would be noise on a channel that has to be worth reading. The amount is what the
// frozen policy assessed and it is a claim rather than a charge: nothing has been taken from
// anybody until a reviewer on the payer's side confirms it, and the link is where the member
// goes to say so.
func (s *Service) notifyNoShowReported(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	record BookingRecord, propertyName string, quote NoShowQuote,
) error {
	return notificationapp.PublishNotification(ctx, tx, tenantID, notificationapp.Notification{
		EventCode: notificationapp.EventBookingNoShowReported,
		// The booking and nothing else: a report happens once per booking, and the unique
		// index says so, so a redelivered command tells the member once.
		Key:        record.ID.String(),
		Recipients: recipientsFor(notificationapp.EventBookingNoShowReported, record.PersonID, uuid.Nil),
		Variables: map[string]string{
			notificationdomain.VarReferenceNo:  record.Reference,
			notificationdomain.VarPropertyName: propertyName,
			notificationdomain.VarEventDate:    record.CheckIn.Format(time.DateOnly),
			notificationdomain.VarAmount:       quote.FeeAmount,
			notificationdomain.VarCurrency:     quote.CurrencyCode,
			notificationdomain.VarDeepLink:     bookingLink(record.ID),
		},
	})
}

// notifyWaitlistOffered tells a waiting member that a room has come free and when the offer
// runs out.
//
// Only the member: an offer made to somebody on a waiting list is not the hotel's business
// until it is accepted, and a property told about every offer would learn who is waiting for
// what. The deadline is a day rather than a minute, which is WP-I6-04's decision and the
// right one -- a countdown printed into an e-mail is already wrong by the time it is read,
// and the exact minute is on the screen the link leads to.
func (s *Service) notifyWaitlistOffered(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	entry WaitlistRecord, booking BookingRecord, propertyName string, expiresAt time.Time,
) error {
	return notificationapp.PublishNotification(ctx, tx, tenantID, notificationapp.Notification{
		EventCode: notificationapp.EventBookingOffered,
		// The entry and the booking it was offered. An entry that was offered a room, let
		// it go and reached the front of the queue again is a second offer and a second
		// message, which is what the member needs; a key on the entry alone would tell them
		// once and leave the second room to expire unwatched.
		Key:        entry.ID.String() + ":" + booking.ID.String(),
		Recipients: recipientsFor(notificationapp.EventBookingOffered, entry.PersonID, uuid.Nil),
		Variables: map[string]string{
			notificationdomain.VarReferenceNo:  booking.Reference,
			notificationdomain.VarPropertyName: propertyName,
			notificationdomain.VarExpiresAt:    expiresAt.UTC().Format(time.DateOnly),
			notificationdomain.VarDeepLink:     bookingLink(booking.ID),
		},
	})
}
