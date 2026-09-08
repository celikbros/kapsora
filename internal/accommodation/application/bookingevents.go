package application

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/platform/outbox"
)

// The three domain events a stay publishes when it ends, and the reason they are events
// rather than calls.
//
// A completed stay, a confirmed no-show and a penalised cancellation are all the same thing
// to the money side of the platform: something happened that the provider may invoice. What
// turns each of them into a claim is WP-I7-01's own subscriber, and it must not be able to
// make a check-out fail. A desk clerk closing a stay at eleven at night cannot be told "the
// claim service is down" — the guest has left, the room is free, and the fact has to be
// recorded whatever the billing side is doing.
//
// So the claim is **never** created inside the command's transaction. The command writes an
// outbox row beside the status change, the two commit together, and the subscriber picks it
// up afterwards. That is also what makes the claim idempotent by construction: the outbox
// delivers at least once, and a handler that finds the claim the first delivery made writes
// nothing.
//
// Every payload is identifiers and a word. There is no guest name, no room number, no amount
// and no voucher in any of them: an outbox payload is read by every consumer and by anybody
// who can see the table, and the consumer that needs an amount reads it from the frozen row
// that carries it.
const (
	// CheckedOutEvent is a stay that ended. The claim it produces has one line per night
	// slept, at the amounts the booking froze.
	CheckedOutEvent = "booking.checked_out"
	// NoShowConfirmedEvent is a reviewer agreeing that nobody came. It is published on the
	// confirmation and never on the report: a report costs nobody anything until a second
	// person agrees with it, and an event on the report would be a fee raised by one side.
	NoShowConfirmedEvent = "booking.no_show_confirmed"
	// CancelledEvent is a stay called off. It is published whether or not a fee was charged,
	// because "this cancellation was free" is a fact the billing side has to be able to
	// observe rather than infer from an event that never arrived.
	CancelledEvent = "booking.cancelled"
)

// bookingAggregate is the aggregate type all three events name.
const bookingAggregate = "booking"

// publishCheckedOut writes the outbox row a completed stay owes, inside the check-out's own
// transaction. The deduplication key is the booking and nothing else: a stay is checked out
// once, and a command replayed by a flaky network publishes one event.
func (s *Service) publishCheckedOut(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	record BookingRecord, actualNights int,
) error {
	_, _, err := outbox.Publish(ctx, tx, outbox.Event{
		TenantID:      uuid.NullUUID{UUID: tenantID, Valid: tenantID != uuid.Nil},
		AggregateType: bookingAggregate, AggregateID: record.ID, Type: CheckedOutEvent,
		Payload: map[string]any{
			"bookingId":    record.ID,
			"personId":     record.PersonID,
			"actualNights": actualNights,
		},
		DeduplicationKey: record.ID.String(),
	})
	return err
}

// publishNoShowConfirmed writes the outbox row a confirmed no-show owes.
func (s *Service) publishNoShowConfirmed(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	record BookingRecord, noShowID uuid.UUID,
) error {
	_, _, err := outbox.Publish(ctx, tx, outbox.Event{
		TenantID:      uuid.NullUUID{UUID: tenantID, Valid: tenantID != uuid.Nil},
		AggregateType: bookingAggregate, AggregateID: record.ID, Type: NoShowConfirmedEvent,
		Payload: map[string]any{
			"bookingId": record.ID,
			"noShowId":  noShowID,
			"personId":  record.PersonID,
		},
		DeduplicationKey: record.ID.String(),
	})
	return err
}

// publishCancelled writes the outbox row a cancelled stay owes.
//
// The reason is part of the key, so a hold that expired and a booking somebody called off are
// two events rather than one that the second delivery swallows — the same rule the
// cancellation notification's key follows, for the same reason.
func (s *Service) publishCancelled(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	record BookingRecord, reasonCode string, free bool,
) error {
	_, _, err := outbox.Publish(ctx, tx, outbox.Event{
		TenantID:      uuid.NullUUID{UUID: tenantID, Valid: tenantID != uuid.Nil},
		AggregateType: bookingAggregate, AggregateID: record.ID, Type: CancelledEvent,
		Payload: map[string]any{
			"bookingId":  record.ID,
			"personId":   record.PersonID,
			"reasonCode": reasonCode,
			"free":       free,
		},
		DeduplicationKey: record.ID.String() + ":" + reasonCode,
	})
	return err
}
