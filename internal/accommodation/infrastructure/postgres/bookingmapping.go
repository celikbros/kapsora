package postgres

import (
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/internal/accommodation/application"
)

// bookingColumns is the one shape every booking read comes back as.
//
// It exists so the five generated row types -- create, get, lock, by-request and list --
// convert into it directly, and one mapper turns it into the application record. The
// alternative is five near-identical mappers, which is five places for a column to be
// dropped and four of them to keep working.
type bookingColumns struct {
	ID                       uuid.UUID
	Reference                string
	PersonID                 uuid.UUID
	EnrollmentID             uuid.UUID
	ProgramID                uuid.UUID
	PropertyID               uuid.UUID
	RoomTypeID               uuid.UUID
	CheckIn                  pgtype.Date
	CheckOut                 pgtype.Date
	Nights                   int32
	Adults                   int32
	Children                 int32
	Status                   string
	HoldExpiresAt            *time.Time
	EntitlementReservationID uuid.NullUUID
	ServiceRequestID         uuid.NullUUID
	AuthorizationID          uuid.NullUUID
	VoucherID                uuid.NullUUID
	QuoteSnapshot            []byte
	PolicySnapshot           []byte
	Channel                  string
	ConfirmedAt              *time.Time
	CheckedInAt              *time.Time
	CheckedOutAt             *time.Time
	CancelledAt              *time.Time
	CancelReasonCode         *string
	ActualNights             *int32
	CreatedAt                time.Time
	UpdatedAt                time.Time
	RowVersion               int64
}

// bookingOf maps the columns onto the application record. The dates come back as civil
// dates at midnight UTC, which is what every date in this vertical is: a position on a
// calendar rather than an instant, so a stay does not move by a day when the clocks change.
func bookingOf(c bookingColumns) application.BookingRecord {
	out := application.BookingRecord{
		ID: c.ID, Reference: c.Reference, PersonID: c.PersonID,
		EnrollmentID: c.EnrollmentID, ProgramID: c.ProgramID, PropertyID: c.PropertyID,
		RoomTypeID: c.RoomTypeID, CheckIn: dateTime(c.CheckIn), CheckOut: dateTime(c.CheckOut),
		Nights: int(c.Nights), Adults: int(c.Adults), Children: int(c.Children),
		Status: c.Status, HoldExpiresAt: c.HoldExpiresAt,
		EntitlementReservationID: uuidPtr(c.EntitlementReservationID),
		ServiceRequestID:         uuidPtr(c.ServiceRequestID),
		AuthorizationID:          uuidPtr(c.AuthorizationID),
		VoucherID:                uuidPtr(c.VoucherID),
		QuoteSnapshot:            c.QuoteSnapshot, PolicySnapshot: c.PolicySnapshot,
		Channel: c.Channel, ConfirmedAt: c.ConfirmedAt, CheckedInAt: c.CheckedInAt,
		CheckedOutAt: c.CheckedOutAt, CancelledAt: c.CancelledAt,
		CancelReasonCode: c.CancelReasonCode,
		CreatedAt:        c.CreatedAt, UpdatedAt: c.UpdatedAt, RowVersion: c.RowVersion,
	}
	if c.ActualNights != nil {
		nights := int(*c.ActualNights)
		out.ActualNights = &nights
	}
	return out
}

// dateOrNull renders an optional civil date filter.
func dateOrNull(t *time.Time) pgtype.Date {
	if t == nil {
		return pgtype.Date{}
	}
	return dateOf(*t)
}
