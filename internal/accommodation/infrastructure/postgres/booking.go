// Package postgres also implements the booking half of the accommodation repository.
//
// Two adapter-level rules live in this file and nowhere else.
//
// The inventory lock is read in `stay_date` order and the query says so; nothing here
// reorders, re-sorts or re-reads it. That ordering is the whole of what keeps concurrent
// holds from deadlocking, and an adapter that "helpfully" sorted the result differently
// would leave the application layer looking correct and the database queueing wrong.
//
// A constraint violation is translated into the application's own named error exactly where
// the constraint name is still visible. `uq_booking_live_per_person_arrival` becomes
// ErrBookingAlreadyLive here, so the service can say what happened rather than a SQLSTATE
// reaching a member.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/celikbros/kapsora/internal/accommodation/application"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// Constraint names this adapter turns into named errors.
const (
	constraintBookingReference = "uq_booking_reference"
	constraintBookingLive      = "uq_booking_live_per_person_arrival"
)

// Bookings is the booking half of the repository. It is stateless; the transaction carries
// everything.
type Bookings struct{}

// NewBookings returns the booking repository.
func NewBookings() Bookings { return Bookings{} }

var _ application.BookingRepository = Bookings{}

// ---------------------------------------------------------------------------
// Inventory under the lock
// ---------------------------------------------------------------------------

// LockInventoryNights implements application.BookingRepository. The ordering is the query's
// and is part of the contract; see the package comment.
func (Bookings) LockInventoryNights(ctx context.Context, tx pgx.Tx, tenantID, roomTypeID uuid.UUID,
	from, to time.Time,
) ([]application.InventoryNight, error) {
	rows, err := sqlcgen.New(tx).LockBookingInventoryRange(ctx, sqlcgen.LockBookingInventoryRangeParams{
		TenantID: tenantID, RoomTypeID: roomTypeID, FromDate: dateOf(from), ToDate: dateOf(to),
	})
	if err != nil {
		return nil, fmt.Errorf("accommodation: lock inventory range: %w", err)
	}
	out := make([]application.InventoryNight, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.InventoryNight{
			StayDate: dateTime(row.StayDate), Capacity: int(row.Capacity),
			Held: int(row.Held), Confirmed: int(row.Confirmed), Available: int(row.Available),
		})
	}
	return out, nil
}

// AddHeld implements application.BookingRepository.
func (Bookings) AddHeld(ctx context.Context, tx pgx.Tx, tenantID, roomTypeID uuid.UUID,
	from, to time.Time, delta int,
) error {
	_, err := sqlcgen.New(tx).AddInventoryHeld(ctx, sqlcgen.AddInventoryHeldParams{
		TenantID: tenantID, RoomTypeID: roomTypeID, FromDate: dateOf(from), ToDate: dateOf(to),
		Delta: int32(delta), //nolint:gosec // a hold moves one room
	})
	if err != nil {
		return fmt.Errorf("accommodation: move held: %w", err)
	}
	return nil
}

// ConfirmNights implements application.BookingRepository.
func (Bookings) ConfirmNights(ctx context.Context, tx pgx.Tx, tenantID, roomTypeID uuid.UUID,
	from, to time.Time,
) error {
	_, err := sqlcgen.New(tx).MoveHeldToConfirmed(ctx, sqlcgen.MoveHeldToConfirmedParams{
		TenantID: tenantID, RoomTypeID: roomTypeID, FromDate: dateOf(from), ToDate: dateOf(to),
	})
	if err != nil {
		return fmt.Errorf("accommodation: confirm nights: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Booking
// ---------------------------------------------------------------------------

// CreateBooking implements application.BookingRepository.
func (Bookings) CreateBooking(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.NewBookingRow,
) (application.BookingRecord, error) {
	row, err := sqlcgen.New(tx).CreateBooking(ctx, sqlcgen.CreateBookingParams{
		TenantID: tenantID, Reference: in.Reference, PersonID: in.PersonID,
		EnrollmentID: in.EnrollmentID, ProgramID: in.ProgramID, PropertyID: in.PropertyID,
		RoomTypeID: in.RoomTypeID, CheckIn: dateOf(in.CheckIn), CheckOut: dateOf(in.CheckOut),
		Nights: int32(in.Nights), Adults: int32(in.Adults), //nolint:gosec // bounded by validation
		Children: int32(in.Children), Status: in.Status, //nolint:gosec // bounded by validation
		HoldExpiresAt:            in.HoldExpiresAt,
		EntitlementReservationID: nullUUID(in.EntitlementReservationID),
		QuoteSnapshot:            in.QuoteSnapshot, Channel: in.Channel,
		ActorID: actorUUID(in.ActorID),
	})
	if err != nil {
		return application.BookingRecord{}, createBookingError(err)
	}
	return bookingOf(bookingColumns{
		ID: row.ID, Reference: row.Reference, PersonID: row.PersonID,
		EnrollmentID: row.EnrollmentID, ProgramID: row.ProgramID, PropertyID: row.PropertyID,
		RoomTypeID: row.RoomTypeID, CheckIn: row.CheckIn, CheckOut: row.CheckOut,
		Nights: row.Nights, Adults: row.Adults, Children: row.Children, Status: row.Status,
		HoldExpiresAt: row.HoldExpiresAt, EntitlementReservationID: row.EntitlementReservationID,
		ServiceRequestID: row.ServiceRequestID, AuthorizationID: row.AuthorizationID,
		VoucherID: row.VoucherID, QuoteSnapshot: row.QuoteSnapshot,
		PolicySnapshot: row.PolicySnapshot, Channel: row.Channel, ConfirmedAt: row.ConfirmedAt,
		CheckedInAt: row.CheckedInAt, CheckedOutAt: row.CheckedOutAt,
		CancelledAt: row.CancelledAt, CancelReasonCode: row.CancelReasonCode,
		ActualNights: row.ActualNights, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		RowVersion: row.RowVersion,
	}), nil
}

// createBookingError names the two constraints a caller can act on. A reference collision
// is a redraw the service performs; a second live booking is the rule of v1.2 10.6 and is a
// 409 the member is told about.
func createBookingError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		switch pgErr.ConstraintName {
		case constraintBookingReference:
			return application.ErrBookingReferenceCollision
		case constraintBookingLive:
			return application.ErrBookingAlreadyLive
		}
	}
	return fmt.Errorf("accommodation: create booking: %w", err)
}

// GetBooking implements application.BookingRepository.
func (Bookings) GetBooking(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	personID *uuid.UUID, scopeIDs []uuid.UUID,
) (application.BookingRecord, error) {
	row, err := sqlcgen.New(tx).GetBooking(ctx, sqlcgen.GetBookingParams{
		TenantID: tenantID, ID: id, PersonID: nullUUID(personID), ScopeIds: scopeIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.BookingRecord{}, application.ErrBookingNotFound
	}
	if err != nil {
		return application.BookingRecord{}, fmt.Errorf("accommodation: read booking: %w", err)
	}
	return bookingOf(bookingColumns(row)), nil
}

// LockBooking implements application.BookingRepository.
func (Bookings) LockBooking(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (
	application.BookingRecord, error,
) {
	row, err := sqlcgen.New(tx).LockBooking(ctx, sqlcgen.LockBookingParams{TenantID: tenantID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.BookingRecord{}, application.ErrBookingNotFound
	}
	if err != nil {
		return application.BookingRecord{}, fmt.Errorf("accommodation: lock booking: %w", err)
	}
	return bookingOf(bookingColumns(row)), nil
}

// GetBookingByRequest implements application.BookingRepository.
func (Bookings) GetBookingByRequest(ctx context.Context, tx pgx.Tx, tenantID, requestID uuid.UUID) (
	application.BookingRecord, error,
) {
	row, err := sqlcgen.New(tx).GetBookingByRequest(ctx, sqlcgen.GetBookingByRequestParams{
		TenantID: tenantID, ServiceRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.BookingRecord{}, application.ErrBookingNotFound
	}
	if err != nil {
		return application.BookingRecord{}, fmt.Errorf("accommodation: read booking by request: %w", err)
	}
	return bookingOf(bookingColumns(row)), nil
}

// ListBookings implements application.BookingRepository.
func (Bookings) ListBookings(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	q application.BookingQuery,
) ([]application.BookingRecord, error) {
	params := sqlcgen.ListBookingsParams{
		TenantID: tenantID, ScopeIds: q.ScopeIDs, PersonID: nullUUID(q.PersonID),
		PropertyID: nullUUID(q.PropertyID), Status: optionalString(q.Status),
		CheckInFrom: dateOrNull(q.CheckInFrom), CheckInTo: dateOrNull(q.CheckInTo),
		PageSize: int32(q.PageSize), //nolint:gosec // clamped by httpx.ClampLimit
	}
	if q.After != nil {
		at := q.After.CreatedAt
		params.AfterAt = &at
		params.AfterID = uuid.NullUUID{UUID: q.After.ID, Valid: true}
	}
	rows, err := sqlcgen.New(tx).ListBookings(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("accommodation: list bookings: %w", err)
	}
	out := make([]application.BookingRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, bookingOf(bookingColumns(row)))
	}
	return out, nil
}

// SetBookingRequest implements application.BookingRepository.
func (Bookings) SetBookingRequest(ctx context.Context, tx pgx.Tx, tenantID, id, requestID,
	actorID uuid.UUID,
) (bool, error) {
	affected, err := sqlcgen.New(tx).SetBookingRequest(ctx, sqlcgen.SetBookingRequestParams{
		TenantID: tenantID, ID: id,
		ServiceRequestID: uuid.NullUUID{UUID: requestID, Valid: true},
		ActorID:          actorUUID(actorID),
	})
	if err != nil {
		return false, fmt.Errorf("accommodation: attach request to booking: %w", err)
	}
	return affected == 1, nil
}

// SetBookingReservation implements application.BookingRepository.
func (Bookings) SetBookingReservation(ctx context.Context, tx pgx.Tx, tenantID, id,
	reservationID uuid.UUID,
) error {
	affected, err := sqlcgen.New(tx).SetBookingReservation(ctx, sqlcgen.SetBookingReservationParams{
		TenantID: tenantID, ID: id,
		EntitlementReservationID: uuid.NullUUID{UUID: reservationID, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("accommodation: attach reservation to booking: %w", err)
	}
	if affected != 1 {
		return application.ErrBookingNotFound
	}
	return nil
}

// ConfirmBookingRow implements application.BookingRepository.
func (Bookings) ConfirmBookingRow(ctx context.Context, tx pgx.Tx, tenantID, id,
	authorizationID uuid.UUID, confirmedAt time.Time, policy json.RawMessage, actorID uuid.UUID,
) (bool, error) {
	affected, err := sqlcgen.New(tx).ConfirmBooking(ctx, sqlcgen.ConfirmBookingParams{
		TenantID: tenantID, ID: id, ConfirmedAt: &confirmedAt,
		AuthorizationID: uuid.NullUUID{UUID: authorizationID, Valid: true},
		PolicySnapshot:  policy, ActorID: actorUUID(actorID),
	})
	if err != nil {
		return false, fmt.Errorf("accommodation: confirm booking: %w", err)
	}
	return affected == 1, nil
}

// MarkPendingApproval implements application.BookingRepository.
func (Bookings) MarkPendingApproval(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	holdExpiresAt time.Time, actorID uuid.UUID,
) (bool, error) {
	affected, err := sqlcgen.New(tx).MarkBookingPendingApproval(ctx,
		sqlcgen.MarkBookingPendingApprovalParams{
			TenantID: tenantID, ID: id, HoldExpiresAt: &holdExpiresAt, ActorID: actorUUID(actorID),
		})
	if err != nil {
		return false, fmt.Errorf("accommodation: mark booking pending: %w", err)
	}
	return affected == 1, nil
}

// CancelBookingRow implements application.BookingRepository.
func (Bookings) CancelBookingRow(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	cancelledAt time.Time, reasonCode string, actorID uuid.UUID,
) (bool, error) {
	affected, err := sqlcgen.New(tx).CancelBooking(ctx, sqlcgen.CancelBookingParams{
		TenantID: tenantID, ID: id, CancelledAt: &cancelledAt,
		CancelReasonCode: &reasonCode, ActorID: actorUUID(actorID),
	})
	if err != nil {
		return false, fmt.Errorf("accommodation: cancel booking: %w", err)
	}
	return affected == 1, nil
}

// ExpireBookingRow implements application.BookingRepository.
func (Bookings) ExpireBookingRow(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (bool, error) {
	affected, err := sqlcgen.New(tx).ExpireBooking(ctx, sqlcgen.ExpireBookingParams{
		TenantID: tenantID, ID: id,
	})
	if err != nil {
		return false, fmt.Errorf("accommodation: expire booking: %w", err)
	}
	return affected == 1, nil
}

// SetBookingVoucher implements application.BookingRepository.
func (Bookings) SetBookingVoucher(ctx context.Context, tx pgx.Tx, tenantID, id, voucherID,
	actorID uuid.UUID,
) error {
	affected, err := sqlcgen.New(tx).SetBookingVoucher(ctx, sqlcgen.SetBookingVoucherParams{
		TenantID: tenantID, ID: id, VoucherID: uuid.NullUUID{UUID: voucherID, Valid: true},
		ActorID: actorUUID(actorID),
	})
	if err != nil {
		return fmt.Errorf("accommodation: attach voucher to booking: %w", err)
	}
	if affected != 1 {
		return application.ErrBookingNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// Nights and guests
// ---------------------------------------------------------------------------

// CreateBookingNight implements application.BookingRepository.
func (Bookings) CreateBookingNight(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.BookingNightRow,
) error {
	if err := (sqlcgen.New(tx)).CreateBookingNight(ctx, sqlcgen.CreateBookingNightParams{
		TenantID: tenantID, BookingID: in.BookingID, StayDate: dateOf(in.StayDate),
		RoomTypeID: in.RoomTypeID, UnitAmount: in.UnitAmount, PayerAmount: in.PayerAmount,
		MemberAmount: in.MemberAmount, CurrencyCode: in.CurrencyCode,
	}); err != nil {
		return fmt.Errorf("accommodation: create booking night: %w", err)
	}
	return nil
}

// ListBookingNights implements application.BookingRepository.
func (Bookings) ListBookingNights(ctx context.Context, tx pgx.Tx, tenantID, bookingID uuid.UUID) (
	[]application.BookingNightRecord, error,
) {
	rows, err := sqlcgen.New(tx).ListBookingNights(ctx, sqlcgen.ListBookingNightsParams{
		TenantID: tenantID, BookingID: bookingID,
	})
	if err != nil {
		return nil, fmt.Errorf("accommodation: list booking nights: %w", err)
	}
	out := make([]application.BookingNightRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.BookingNightRecord{
			StayDate: dateTime(row.StayDate), RoomTypeID: row.RoomTypeID,
			UnitAmount: row.UnitAmount, PayerAmount: row.PayerAmount,
			MemberAmount: row.MemberAmount, CurrencyCode: row.CurrencyCode,
		})
	}
	return out, nil
}

// CreateBookingGuest implements application.BookingRepository.
func (Bookings) CreateBookingGuest(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	in application.BookingGuestRow,
) error {
	if err := (sqlcgen.New(tx)).CreateBookingGuest(ctx, sqlcgen.CreateBookingGuestParams{
		TenantID: tenantID, BookingID: in.BookingID, PersonID: nullUUID(in.PersonID),
		DisplayName: in.DisplayName, GuestType: in.GuestType, IsMinor: in.IsMinor,
	}); err != nil {
		return fmt.Errorf("accommodation: create booking guest: %w", err)
	}
	return nil
}

// ListBookingGuests implements application.BookingRepository.
func (Bookings) ListBookingGuests(ctx context.Context, tx pgx.Tx, tenantID, bookingID uuid.UUID) (
	[]application.BookingGuestRecord, error,
) {
	rows, err := sqlcgen.New(tx).ListBookingGuests(ctx, sqlcgen.ListBookingGuestsParams{
		TenantID: tenantID, BookingID: bookingID,
	})
	if err != nil {
		return nil, fmt.Errorf("accommodation: list booking guests: %w", err)
	}
	out := make([]application.BookingGuestRecord, 0, len(rows))
	for _, row := range rows {
		record := application.BookingGuestRecord{
			ID: row.ID, DisplayName: row.DisplayName, GuestType: row.GuestType,
			IsMinor: row.IsMinor,
		}
		if row.PersonID.Valid {
			id := row.PersonID.UUID
			record.PersonID = &id
		}
		out = append(out, record)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// What a hold needs to know
// ---------------------------------------------------------------------------

// RoomTypeBookingContext implements application.BookingRepository.
func (Bookings) RoomTypeBookingContext(ctx context.Context, tx pgx.Tx, tenantID, roomTypeID uuid.UUID,
	scopeIDs []uuid.UUID,
) (application.RoomTypeBookingContext, error) {
	row, err := sqlcgen.New(tx).GetBookingRoomTypeContext(ctx, sqlcgen.GetBookingRoomTypeContextParams{
		TenantID: tenantID, RoomTypeID: roomTypeID, ScopeIds: scopeIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.RoomTypeBookingContext{}, application.ErrRoomTypeNotFound
	}
	if err != nil {
		return application.RoomTypeBookingContext{}, fmt.Errorf("accommodation: read room type context: %w", err)
	}
	return application.RoomTypeBookingContext{
		RoomTypeID: row.RoomTypeID, RoomTypeCode: row.RoomTypeCode, RoomTypeName: row.RoomTypeName,
		MaxAdults: int(row.MaxAdults), MaxChildren: int(row.MaxChildren),
		MaxOccupancy: int(row.MaxOccupancy), ServiceDefinitionID: row.ServiceDefinitionID,
		ServiceDefinitionCode: row.ServiceDefinitionCode, ServiceUnitType: row.DefaultUnitType,
		ServiceActive: row.ServiceActive, RoomTypeStatus: row.RoomTypeStatus,
		PropertyID: row.PropertyID, PropertyName: row.PropertyName,
		PropertyTimezone: row.Timezone, PropertyStatus: row.PropertyStatus,
		ProviderOrganizationID: row.ProviderOrganizationID,
	}, nil
}

// PersonEnrollmentForStay implements application.BookingRepository.
func (Bookings) PersonEnrollmentForStay(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID,
	day time.Time, programID *uuid.UUID,
) (application.PersonEnrollment, error) {
	row, err := sqlcgen.New(tx).GetPersonEnrollmentForStay(ctx, sqlcgen.GetPersonEnrollmentForStayParams{
		TenantID: tenantID, PersonID: personID, ServiceDate: dateOf(day),
		ProgramID: nullUUID(programID),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return application.PersonEnrollment{}, application.ErrEnrollmentNotFound
	}
	if err != nil {
		return application.PersonEnrollment{}, fmt.Errorf("accommodation: read enrollment: %w", err)
	}
	return application.PersonEnrollment{EnrollmentID: row.EnrollmentID, ProgramID: row.ProgramID}, nil
}

// EntitlementCodeForService implements application.BookingRepository.
func (Bookings) EntitlementCodeForService(ctx context.Context, tx pgx.Tx, tenantID, enrollmentID,
	serviceDefinitionID uuid.UUID, day time.Time,
) (string, error) {
	code, err := sqlcgen.New(tx).GetBookingEntitlementCode(ctx, sqlcgen.GetBookingEntitlementCodeParams{
		TenantID: tenantID, EnrollmentID: enrollmentID,
		ServiceDefinitionID: serviceDefinitionID, ServiceDate: dateOf(day),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The plan maps this service onto no entitlement at all. It is not an error and it
		// is not an empty answer: the member's plan does not cover this kind of room, which
		// is exactly what the refusal above says.
		return "", application.ErrEntitlementAccountNotFound
	}
	if err != nil {
		return "", fmt.Errorf("accommodation: resolve entitlement code: %w", err)
	}
	return code, nil
}

// ContractVersionForProperty implements application.BookingRepository.
func (Bookings) ContractVersionForProperty(ctx context.Context, tx pgx.Tx, tenantID, propertyID uuid.UUID,
	day time.Time,
) (uuid.UUID, error) {
	q := sqlcgen.New(tx)
	profileID, err := q.GetProviderProfileForProperty(ctx, sqlcgen.GetProviderProfileForPropertyParams{
		TenantID: tenantID, PropertyID: propertyID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, application.ErrLodgingTermsMissing
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("accommodation: read provider profile: %w", err)
	}
	versionID, err := q.GetContractVersionForRoomType(ctx, sqlcgen.GetContractVersionForRoomTypeParams{
		TenantID: tenantID, ProviderProfileID: profileID, ServiceDate: dateOf(day),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// No published contract version covers the stay, so there are no terms to freeze.
		// It is the same refusal as terms that were never written: the member is told the
		// stay cannot be agreed, rather than agreeing to a policy nobody wrote.
		return uuid.Nil, application.ErrLodgingTermsMissing
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("accommodation: read contract version: %w", err)
	}
	return versionID, nil
}

// ---------------------------------------------------------------------------
// The sweeps
// ---------------------------------------------------------------------------

// ListExpiredHolds implements application.BookingRepository.
func (Bookings) ListExpiredHolds(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	before time.Time, limit int,
) ([]uuid.UUID, error) {
	rows, err := sqlcgen.New(tx).ListExpiredHolds(ctx, sqlcgen.ListExpiredHoldsParams{
		TenantID: tenantID, Before: &before,
		PageSize: int32(limit), //nolint:gosec // a constant batch size
	})
	if err != nil {
		return nil, fmt.Errorf("accommodation: list expired holds: %w", err)
	}
	return rows, nil
}

// ListBookingsArrivingOn implements application.BookingRepository.
func (Bookings) ListBookingsArrivingOn(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	day time.Time, limit int,
) ([]application.ReminderRow, error) {
	rows, err := sqlcgen.New(tx).ListBookingsForReminder(ctx, sqlcgen.ListBookingsForReminderParams{
		TenantID: tenantID, CheckIn: dateOf(day),
		PageSize: int32(limit), //nolint:gosec // a constant batch size
	})
	if err != nil {
		return nil, fmt.Errorf("accommodation: list arrivals: %w", err)
	}
	out := make([]application.ReminderRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.ReminderRow{
			BookingID: row.ID, Reference: row.Reference, PersonID: row.PersonID,
			CheckIn: dateTime(row.CheckIn), PropertyName: row.PropertyName, Timezone: row.Timezone,
		})
	}
	return out, nil
}

// ActiveTenants implements application.BookingRepository. platform.tenant carries no RLS,
// so it is read with a plain statement like every other cross-tenant job.
func (Bookings) ActiveTenants(ctx context.Context, tx pgx.Tx) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM platform.tenant WHERE status IN ('ACTIVE','SUSPENDED')`)
	if err != nil {
		return nil, fmt.Errorf("accommodation: list tenants: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("accommodation: scan tenant: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("accommodation: list tenants: %w", err)
	}
	return out, nil
}
