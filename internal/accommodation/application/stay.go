package application

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/accommodation/domain"
	"github.com/celikbros/kapsora/internal/accommodation/settings"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// The stay itself: the guest arrives, and the guest leaves.
//
// Two rules run through this file and neither is repeated at each step.
//
// **The calendar is the building's.** A night belongs to the clock on the wall of the hotel,
// so the check-in window and the count of nights actually stayed are both computed in the
// property's own zone. A guest checking out at one in the morning in Antalya has slept a
// night the server in another zone might still call yesterday.
//
// **Nothing is consumed beyond what was authorized.** A guest who stayed longer than the
// plan promised has already had the extra nights: the check-out cannot refuse -- refusing
// would leave the stay open forever -- so it consumes what was promised, flags the booking,
// and leaves the difference to M7's claim as an exception a person looks at.

// CheckInInput is the desk taking a voucher.
type CheckInInput struct {
	// Token is the plaintext the guest presented, typed or scanned. It arrives in the
	// request body and nowhere else: not in the URL, not in a query string, and it reaches
	// no column, log line, audit row or notification from here.
	Token string
	// At overrides the moment for a desk recording an arrival it did not enter at the time.
	// Zero means now.
	At time.Time
}

// CheckOutInput is the desk closing a stay.
type CheckOutInput struct {
	// At is the moment the guest left. Zero means now. It is turned into a calendar day on
	// the property's own clock, because that is what a night is counted in.
	At time.Time
}

// CheckInBooking records the guest's arrival against the voucher they presented.
//
// The voucher is redeemed through WP-I4-02 in this command's own transaction, so a check-in
// that rolls back leaves the code still usable and one that commits leaves it spent. No
// fulfilment is written here: nobody has slept anywhere yet, and the nights are consumed at
// check-out for what was actually used.
//
// **A token that is not this booking's is 404 and never "wrong booking".** A desk that could
// tell a real code for another stay apart from a code that does not exist could use this
// endpoint to find out whether a code exists at all, which is exactly what a stolen list of
// candidate tokens would be tested against.
func (s *Service) CheckInBooking(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	in CheckInInput,
) (BookingView, error) {
	if s.bookings == nil {
		return BookingView{}, ErrBookingNotFound
	}
	if strings.TrimSpace(in.Token) == "" {
		return BookingView{}, ErrVoucherRequired
	}
	var out BookingView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		view, err := s.checkIn(ctx, tx, rc, id, in)
		out = view
		return err
	})
	if err != nil {
		return BookingView{}, err
	}
	return out, nil
}

func (s *Service) checkIn(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	id uuid.UUID, in CheckInInput,
) (BookingView, error) {
	if _, err := s.bookings.GetBooking(ctx, tx, rc.TenantID, id, personBoundary(rc), scopeOf(rc)); err != nil {
		return BookingView{}, err
	}
	record, err := s.bookings.LockBooking(ctx, tx, rc.TenantID, id)
	if err != nil {
		return BookingView{}, err
	}
	if record.Status != domain.BookingConfirmed {
		return BookingView{}, ErrBookingTransitionInvalid
	}
	if record.AuthorizationID == nil {
		return BookingView{}, ErrVoucherNotAvailable
	}
	room, err := s.bookings.RoomTypeBookingContext(ctx, tx, rc.TenantID, record.RoomTypeID, nil)
	if err != nil {
		return BookingView{}, err
	}
	tenantSettings, err := settings.Load(ctx, tx, rc.TenantID)
	if err != nil {
		return BookingView{}, err
	}
	at := in.At
	if at.IsZero() {
		at = s.now()
	}
	at = at.UTC()
	if err := insideCheckInWindow(record.CheckIn, room.PropertyTimezone, tenantSettings, at); err != nil {
		return BookingView{}, err
	}

	// The token is spent here, in this transaction. The window checked above is the
	// tenant's and the property's; the voucher's own validity window, its status and the
	// digest match are WP-I4-02's, and this package has no opinion about any of them.
	if err := s.auths.RedeemVoucherToken(ctx, tx, rc, BookingRedeemInput{
		Token: in.Token, AuthorizationID: *record.AuthorizationID, RedeemedAt: at,
	}); err != nil {
		return BookingView{}, err
	}

	moved, err := s.bookings.CheckInBookingRow(ctx, tx, rc.TenantID, record.ID, at,
		rc.Principal.ActorID)
	if err != nil {
		return BookingView{}, err
	}
	if !moved {
		// The row was CONFIRMED when it was locked, so nothing should have moved it. It is
		// worth refusing loudly rather than reporting a check-in that did not happen.
		return BookingView{}, ErrBookingTransitionInvalid
	}
	// The audit detail names the booking and the moment, and nothing that resembles the
	// token. audit.SanitizeDetail would drop a key containing "token" anyway; not writing
	// one is the half that does not depend on it.
	if err := s.record(ctx, tx, rc, ActionBookingCheckIn, ResourceBooking, record.ID, map[string]any{
		"reference": record.Reference, "checked_in_at": at.Format(time.RFC3339),
	}); err != nil {
		return BookingView{}, err
	}
	record.Status = domain.BookingCheckedIn
	record.CheckedInAt = &at
	return s.loadBooking(ctx, tx, rc.TenantID, record)
}

// insideCheckInWindow refuses an arrival outside `[check_in - early, check_in + late]`,
// counted from the start of the arrival day on the property's own clock.
//
// The refusal carries both ends of the window, because a clerk told "too early" and not
// "from when" has been told nothing, and a clerk who can see that the window closed
// yesterday knows to raise a no-show instead of trying again.
func insideCheckInWindow(checkIn time.Time, timeZone string, tenantSettings settings.Values,
	now time.Time,
) error {
	loc, err := domain.LoadLocation(timeZone)
	if err != nil {
		loc = time.UTC
	}
	day := domain.Day(checkIn)
	arrival := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	opens := arrival.Add(-time.Duration(tenantSettings.CheckInEarlyHours) * time.Hour)
	closes := arrival.Add(time.Duration(tenantSettings.CheckInLateHours) * time.Hour)
	if now.Before(opens) || now.After(closes) {
		return &CheckInWindowClosed{
			OpensAt: opens.UTC(), ClosesAt: closes.UTC(), TimeZone: loc.String(),
		}
	}
	return nil
}

// checkInWindowClosesAt is the other half of the same computation, for the no-show report:
// a guest who is late is not a guest who did not come, and the line between the two is the
// moment the window closes.
func checkInWindowClosesAt(checkIn time.Time, timeZone string, tenantSettings settings.Values) time.Time {
	loc, err := domain.LoadLocation(timeZone)
	if err != nil {
		loc = time.UTC
	}
	day := domain.Day(checkIn)
	arrival := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	return arrival.Add(time.Duration(tenantSettings.CheckInLateHours) * time.Hour).UTC()
}

// CheckOutBooking ends a stay and settles what it used.
//
// Four things happen in one transaction and they are one fact rather than four: the nights
// actually stayed are counted on the property's calendar, the fulfilment for those nights is
// written and consumed, everything the authorization still held is released, and the rooms
// of the nights nobody slept in go back to the allotment. A check-out that committed without
// its release would leave a member unable to use a benefit they have already paid for; a
// release that committed without its check-out would give nights back for a stay still
// running.
//
// **Running it twice releases nothing twice.** The status write names CHECKED_IN, so the
// second attempt moves no row and stops; the fulfilment's consumption is keyed by the
// booking, and the release's key is the line and the reason rather than the moment.
func (s *Service) CheckOutBooking(ctx context.Context, rc identity.RequestContext, id uuid.UUID,
	in CheckOutInput,
) (BookingView, error) {
	if s.bookings == nil {
		return BookingView{}, ErrBookingNotFound
	}
	var out BookingView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		view, err := s.checkOut(ctx, tx, rc, id, in)
		out = view
		return err
	})
	if err != nil {
		return BookingView{}, err
	}
	return out, nil
}

func (s *Service) checkOut(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	id uuid.UUID, in CheckOutInput,
) (BookingView, error) {
	if _, err := s.bookings.GetBooking(ctx, tx, rc.TenantID, id, personBoundary(rc), scopeOf(rc)); err != nil {
		return BookingView{}, err
	}
	record, err := s.bookings.LockBooking(ctx, tx, rc.TenantID, id)
	if err != nil {
		return BookingView{}, err
	}
	if record.Status != domain.BookingCheckedIn {
		return BookingView{}, ErrBookingTransitionInvalid
	}
	room, err := s.bookings.RoomTypeBookingContext(ctx, tx, rc.TenantID, record.RoomTypeID, nil)
	if err != nil {
		return BookingView{}, err
	}
	at := in.At
	if at.IsZero() {
		at = s.now()
	}
	at = at.UTC()
	actual := ActualNights(record.CheckIn, at, room.PropertyTimezone)
	snapshot, err := decodeQuoteSnapshot(record.QuoteSnapshot)
	if err != nil {
		return BookingView{}, err
	}

	authorized, used, err := s.settleStay(ctx, tx, rc, record, snapshot, room, actual, at)
	if err != nil {
		return BookingView{}, err
	}
	// A stay that ran over what was promised releases nothing and is flagged instead, for
	// M7's claim to raise as an exception. Flagging rather than refusing is deliberate: the
	// guest has already slept the extra nights, and a check-out that could not be recorded
	// would leave the stay open forever.
	over := authorized > 0 && actual > authorized

	// The rooms of the nights nobody slept in. The nights that were used stay `confirmed` --
	// the room really was occupied on them -- and only the tail goes back to the allotment,
	// where the availability search can sell it again.
	if actual < record.Nights {
		firstFree := domain.Day(record.CheckIn).AddDate(0, 0, actual)
		if _, err := s.bookings.LockInventoryNights(ctx, tx, rc.TenantID, record.RoomTypeID,
			firstFree, record.LastNight()); err != nil {
			return BookingView{}, err
		}
		if err := s.bookings.AddConfirmed(ctx, tx, rc.TenantID, record.RoomTypeID,
			firstFree, record.LastNight(), -1); err != nil {
			return BookingView{}, err
		}
	}

	moved, err := s.bookings.CheckOutBookingRow(ctx, tx, rc.TenantID, record.ID, at, actual, over,
		rc.Principal.ActorID)
	if err != nil {
		return BookingView{}, err
	}
	if !moved {
		return BookingView{}, ErrBookingTransitionInvalid
	}
	if err := s.record(ctx, tx, rc, ActionBookingCheckOut, ResourceBooking, record.ID, map[string]any{
		"reference": record.Reference, "actual_nights": actual, "booked_nights": record.Nights,
		"authorized_nights": authorized, "consumed_nights": used, "over_booking": over,
	}); err != nil {
		return BookingView{}, err
	}
	record.Status = domain.BookingCompleted
	record.CheckedOutAt = &at
	record.ActualNights = &actual
	record.OverBooking = over
	return s.loadBooking(ctx, tx, rc.TenantID, record)
}

// settleStay consumes what the stay used and releases what it did not, across every hold the
// booking stands on, exactly as a discharge does (WP-I5-03 section 2.4).
//
// It reports what the authorization actually promised and what was consumed, because the two
// are what the over-stay flag is decided from. The promise is read rather than assumed: the
// booking asked for the nights its frozen quote said the plan carries, and a reviewer may
// have approved fewer -- releasing `booked - stayed` rather than `approved - stayed` would
// hand back entitlement that was never held.
func (s *Service) settleStay(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record BookingRecord, snapshot QuoteSnapshot, room RoomTypeBookingContext,
	actual int, at time.Time,
) (authorized, used int, err error) {
	if record.AuthorizationID == nil {
		return 0, 0, nil
	}
	lines, err := s.auths.Lines(ctx, tx, rc.TenantID, *record.AuthorizationID)
	if err != nil {
		return 0, 0, err
	}
	promised := benefitdomain.ZeroQuantity()
	for _, line := range lines {
		if line.ServiceDefinitionID != snapshot.ServiceDefinitionID {
			continue
		}
		quantity, parseErr := benefitdomain.ParseQuantity(line.Approved)
		if parseErr != nil {
			return 0, 0, fmt.Errorf("accommodation: approved nights %q: %w", line.Approved, parseErr)
		}
		promised = promised.Add(quantity)
	}
	authorized = wholeNights(promised)
	used = actual
	if used > authorized {
		// Never beyond what was promised. The extra nights are the over-stay, and they are
		// a claim exception rather than an entitlement this command may spend.
		used = authorized
	}

	if used > 0 {
		if err := s.auths.RecordStayFulfilment(ctx, tx, rc, BookingFulfilmentInput{
			AuthorizationID:     *record.AuthorizationID,
			ServiceDefinitionID: snapshot.ServiceDefinitionID,
			Nights:              fmt.Sprintf("%d", used),
			PerformedAt:         at,
			ProviderProfileID:   nil,
		}); err != nil {
			return 0, 0, err
		}
	}
	if authorized > used {
		if _, err := s.auths.ReleaseUnused(ctx, tx, BookingReleaseInput{
			TenantID: rc.TenantID, ActorID: rc.Principal.ActorID,
			AuthorizationID: *record.AuthorizationID,
			Nights:          fmt.Sprintf("%d", authorized-used),
			ReasonCode:      ReasonCheckOut,
		}); err != nil {
			return 0, 0, err
		}
	}
	_ = room
	return authorized, used, nil
}

// ActualNights is how many nights a guest actually stayed: the number of calendar days
// between the booked arrival and the day they left, on the property's own clock, and never
// fewer than one.
//
// It is the calendar and not a duration divided by twenty-four hours. A stay that spans a
// clock change is twenty-three or twenty-five hours long and is still one night, and a guest
// who checks out at eleven in the morning of the day they arrived has still used the room
// for a night the hotel cannot sell to anybody else.
func ActualNights(checkIn time.Time, checkedOutAt time.Time, timeZone string) int {
	loc, err := domain.LoadLocation(timeZone)
	if err != nil {
		loc = time.UTC
	}
	arrival := domain.Day(checkIn)
	departure := domain.DayIn(checkedOutAt, loc)
	nights := int(departure.Sub(arrival).Hours() / 24)
	if nights < 1 {
		return 1
	}
	return nights
}
