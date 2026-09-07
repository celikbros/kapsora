package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/accommodation/domain"
	"github.com/celikbros/kapsora/internal/accommodation/settings"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/benefit/ledger"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// QuoteSnapshotVersion is stamped into every frozen quote. It is here so that a snapshot
// written today can still be read a year from now by code that has learned a new field:
// the shape is versioned, and a reader that meets a version it does not know says so
// instead of silently reading a zero.
const QuoteSnapshotVersion = 1

// PendingApprovalHold is how long a booking's room is held once a reviewer has the
// reservation request and no work item gave the request a deadline of its own.
//
// Three days rather than fifteen minutes, because the countdown a hold runs on is a
// countdown for the member -- "decide, or the room goes back" -- and it stops being that
// the moment the decision belongs to somebody else. A member must not lose a room because
// a reviewer went on leave, and a reviewer's queue must not hold a room for a month.
const PendingApprovalHold = 72 * time.Hour

// The ledger reason codes this package writes. They are part of the movement's idempotency
// key, so a release at expiry and a release at cancellation are two distinct movements and
// either of them run twice is still one.
const (
	reasonBookingHold    = "BOOKING_HOLD"
	reasonHoldReleased   = "HOLD_RELEASED"
	reasonHoldExpired    = "HOLD_EXPIRED"
	reasonRequestRefused = "REQUEST_REJECTED"
)

// voucherReissueReason is written on the voucher a reissue withdrew.
const voucherReissueReason = "REISSUED"

// GuestInput is one person the room is booked for.
type GuestInput struct {
	// PersonID is set for a member or a dependant the tenant already knows, and nil for
	// anybody else.
	PersonID *uuid.UUID
	// DisplayName is a name and only ever a name. There is no identifier field here and
	// there will not be one: a passport number typed into a booking would be plaintext
	// personal data in a table nobody thinks of as holding any.
	DisplayName string
	GuestType   string
	IsMinor     bool
}

// HoldInput is one hold command.
type HoldInput struct {
	// PersonID is whose stay this is. It is resolved by the transport -- from the caller's
	// own PERSON binding for a member, from the body for a desk -- and is never taken from
	// the body for a caller that is bound to a person.
	PersonID   uuid.UUID
	RoomTypeID uuid.UUID
	CheckIn    time.Time
	CheckOut   time.Time
	Adults     int
	Children   int
	ProgramID  *uuid.UUID
	Guests     []GuestInput
	Channel    string
}

// BookingView is a booking with its nights and its guests.
type BookingView struct {
	Booking BookingRecord
	Nights  []BookingNightRecord
	Guests  []BookingGuestRecord
	// SecondsToExpiry is the countdown a screen shows on a held booking, and zero on
	// anything else. It is computed on the server rather than left to a browser to derive
	// from two timestamps whose clocks disagree.
	SecondsToExpiry int
}

// BookingPage is one page of bookings.
type BookingPage struct {
	Items      []BookingView
	NextCursor string
}

// BookingFilter is the API-level list request.
type BookingFilter struct {
	Cursor      string
	Limit       int
	PersonID    *uuid.UUID
	PropertyID  *uuid.UUID
	Status      string
	CheckInFrom *time.Time
	CheckInTo   *time.Time
}

// QuoteSnapshot is what the member saw, frozen at the hold. Every amount is an exact
// decimal string, and none of it is ever recomputed: pricing that changed between the
// search and the confirmation changes the next booking, not this one.
type QuoteSnapshot struct {
	Version int `json:"version"`
	// QuotedAt is the moment the prices in this snapshot were true. It is what
	// accommodation.quote_ttl_minutes is measured against, and it is a fact rather than a
	// clock reading at confirmation time -- which is why a stale quote can be refused at
	// all.
	QuotedAt            time.Time     `json:"quotedAt"`
	EvaluationID        *uuid.UUID    `json:"evaluationId,omitempty"`
	PropertyID          uuid.UUID     `json:"propertyId"`
	RoomTypeID          uuid.UUID     `json:"roomTypeId"`
	ServiceDefinitionID uuid.UUID     `json:"serviceDefinitionId"`
	CurrencyCode        string        `json:"currencyCode"`
	TotalAmount         string        `json:"totalAmount"`
	PayerAmount         string        `json:"payerAmount"`
	MemberAmount        string        `json:"memberAmount"`
	Nights              []QuoteNight  `json:"nights"`
	Entitlement         *QuoteBalance `json:"entitlement,omitempty"`
	Eligible            bool          `json:"eligible"`
	// CoveredNights is how many of the stay's nights the plan carries, frozen with the rest
	// of the quote. It is the one number three later steps read rather than compute: the
	// entitlement the hold reserves, the quantity the reservation request asks for, and the
	// quantity the authorization adopts. A stay of three nights on a plan with two left is
	// `nights: 3, coveredNights: 2` -- the member sleeps three nights and pays for one,
	// which is exactly what the search showed them before they held anything.
	CoveredNights int `json:"coveredNights"`
}

// QuoteNight is one night of the frozen quote.
type QuoteNight struct {
	StayDate     string `json:"stayDate"`
	Amount       string `json:"amount"`
	PayerAmount  string `json:"payerAmount"`
	MemberAmount string `json:"memberAmount"`
}

// QuoteBalance is what the plan had left when the quote was taken.
type QuoteBalance struct {
	EntitlementCode string `json:"entitlementCode"`
	Unit            string `json:"unit"`
	Remaining       string `json:"remaining"`
}

// CreateHold sets a room aside for the length of the tenant's accommodation.hold_minutes,
// takes the entitlement the stay would spend, and freezes the quote the member was shown.
//
// The command has two halves and the split is the point. Everything that reads the world
// and prices it -- the property, the contract, the eligibility evaluation, the pricing
// ladder -- happens first, outside any row lock. Only then is one transaction opened that
// locks the nights in stay_date order, checks them, moves the counters, reserves the
// nights and writes the booking.
//
// Doing it the other way round is the mistake this shape exists to prevent. The eligibility
// check opens a transaction of its own on a second pooled connection; running it with the
// inventory rows already locked would mean every concurrent hold held one connection while
// waiting for another, and five hundred of them would exhaust the pool and deadlock on
// nothing at all. The critical section here is a handful of statements long and touches
// only rows it has already locked.
func (s *Service) CreateHold(ctx context.Context, rc identity.RequestContext, in HoldInput) (BookingView, error) {
	if s.bookings == nil || s.ledger == nil {
		return BookingView{}, errors.New("accommodation: this process cannot hold a room")
	}
	if in.PersonID == uuid.Nil {
		return BookingView{}, ErrPersonRequired
	}
	checkIn, checkOut := domain.Day(in.CheckIn), domain.Day(in.CheckOut)

	prepared, err := s.prepareHold(ctx, rc, in, checkIn, checkOut)
	if err != nil {
		return BookingView{}, err
	}

	var out BookingView
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		view, err := s.placeHold(ctx, tx, rc, in, prepared)
		out = view
		return err
	})
	if err != nil {
		return BookingView{}, err
	}
	return out, nil
}

// preparedHold is everything the locked section needs, computed before any row is locked.
type preparedHold struct {
	checkIn   time.Time
	checkOut  time.Time
	lastNight time.Time
	nights    int
	stayDates []time.Time
	settings  settings.Values
	room      RoomTypeBookingContext
	plan      PersonEnrollment
	quote     QuoteView
	snapshot  QuoteSnapshot
	expiresAt time.Time
}

// prepareHold reads the world, checks eligibility and prices the stay. Nothing here writes
// anything except the eligibility evaluation, which is a record of what the member was
// shown and is exactly what WP-I2-04 exists to keep.
func (s *Service) prepareHold(ctx context.Context, rc identity.RequestContext, in HoldInput,
	checkIn, checkOut time.Time,
) (preparedHold, error) {
	out := preparedHold{checkIn: checkIn, checkOut: checkOut}

	var world searchWorld
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		tenantSettings, err := settings.Load(ctx, tx, rc.TenantID)
		if err != nil {
			return err
		}
		out.settings = tenantSettings
		nights, err := validateHold(in, checkIn, checkOut, tenantSettings.MaxNights)
		if err != nil {
			return err
		}
		out.nights = nights
		out.lastNight = checkIn.AddDate(0, 0, nights-1)
		out.stayDates = domain.StayDates(checkIn, nights)

		room, err := s.bookings.RoomTypeBookingContext(ctx, tx, rc.TenantID, in.RoomTypeID, scopeOf(rc))
		if err != nil {
			return err
		}
		if room.RoomTypeStatus != domain.StatusActive || room.PropertyStatus != domain.StatusActive ||
			!room.ServiceActive {
			// A room type or a building the provider has closed is not on sale, and a
			// service the tenant has retired cannot be priced or entitled. All three are
			// the same answer to the member: this room is not bookable, and that it exists
			// at all is not something a 409 needs to confirm.
			return ErrRoomTypeNotFound
		}
		if !domain.OccupancyFits(in.Adults, in.Children, room.MaxAdults, room.MaxChildren, room.MaxOccupancy) {
			return ErrOccupancyExceeded
		}
		out.room = room

		plan, err := s.bookings.PersonEnrollmentForStay(ctx, tx, rc.TenantID, in.PersonID,
			checkIn, in.ProgramID)
		if err != nil {
			return err
		}
		out.plan = plan

		world = searchWorld{nights: nights, stayDates: out.stayDates}
		return s.loadWorld(ctx, tx, rc, SearchInput{
			PersonID: in.PersonID, PropertyID: &room.PropertyID, ProgramID: in.ProgramID,
			Adults: in.Adults, Children: in.Children,
		}, checkIn, &world)
	})
	if err != nil {
		return preparedHold{}, err
	}

	property, found := world.propertyByID[out.room.PropertyID]
	if !found {
		// The property is not one this person's programs reach: no active contract with a
		// payer behind their plan covers the stay. It is the same answer the search gives
		// them, and for the same reason.
		return preparedHold{}, ErrPropertyNotFound
	}
	room, found := findRoomType(world.roomTypes, in.RoomTypeID)
	if !found {
		return preparedHold{}, ErrRoomTypeNotFound
	}

	// The eligibility check runs outside the read transaction and outside every lock: it
	// opens a transaction of its own, and calling it with rows locked is what would turn
	// concurrent holds into a pool starvation deadlock.
	verdict, err := s.checkEligibility(ctx, rc, SearchInput{
		PersonID: in.PersonID, ProgramID: in.ProgramID,
	}, checkIn, world)
	if err != nil {
		return preparedHold{}, err
	}
	quote, reason := s.quoteRoomType(world, property, room, verdict)
	if quote == nil {
		return preparedHold{}, &QuoteUnavailable{Reason: reason}
	}
	out.quote = *quote
	out.snapshot = snapshotOf(*quote, verdict, out, s.now().UTC())
	// A plan that carries no night of this stay is the one refusal here. Fewer nights than
	// the stay is long is not a refusal and must not become one: the search has already
	// shown the member which nights are theirs to pay, and a hold that insisted on the whole
	// stay would refuse exactly the booking they were quoted.
	if out.snapshot.CoveredNights == 0 {
		return preparedHold{}, ErrEntitlementInsufficient
	}
	out.expiresAt = s.now().UTC().Add(time.Duration(out.settings.HoldMinutes) * time.Minute)
	return out, nil
}

// placeHold is the locked half: the nights, the counters, the reservation and the booking.
//
// The order inside it is not interchangeable. The rows are locked in stay_date order first,
// because that ordering is what makes concurrent holds queue instead of deadlock. The
// availability is read from the rows this transaction now holds, so nobody can take the
// last room between the check and the write. `held` is moved by a statement rather than a
// read-modify-write, and `held + confirmed <= capacity` on the row is the belt underneath:
// if the check above were deleted tomorrow the statement would fail rather than oversell.
func (s *Service) placeHold(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	in HoldInput, prepared preparedHold,
) (BookingView, error) {
	nights, err := s.bookings.LockInventoryNights(ctx, tx, rc.TenantID, in.RoomTypeID,
		prepared.checkIn, prepared.lastNight)
	if err != nil {
		return BookingView{}, err
	}
	if unavailable := firstUnavailable(nights, prepared.stayDates); unavailable != nil {
		return BookingView{}, unavailable
	}
	if err := s.bookings.AddHeld(ctx, tx, rc.TenantID, in.RoomTypeID,
		prepared.checkIn, prepared.lastNight, 1); err != nil {
		return BookingView{}, err
	}

	snapshot, err := json.Marshal(prepared.snapshot)
	if err != nil {
		return BookingView{}, fmt.Errorf("accommodation: freeze quote: %w", err)
	}
	expiresAt := prepared.expiresAt
	record, err := s.createBookingWithReference(ctx, tx, rc, NewBookingRow{
		PersonID: in.PersonID, EnrollmentID: prepared.plan.EnrollmentID,
		ProgramID: prepared.plan.ProgramID, PropertyID: prepared.room.PropertyID,
		RoomTypeID: in.RoomTypeID, CheckIn: prepared.checkIn, CheckOut: prepared.checkOut,
		Nights: prepared.nights, Adults: in.Adults, Children: in.Children,
		Status: domain.BookingHold, HoldExpiresAt: &expiresAt, QuoteSnapshot: snapshot,
		Channel: channelOf(in.Channel, rc), ActorID: rc.Principal.ActorID,
	})
	if err != nil {
		return BookingView{}, err
	}

	// The entitlement, taken once, here. The reservation's deadline is the hold's own, so
	// a member who walks away has their nights given back by WP-I2-03's expiry sweep even
	// if this vertical's sweep never runs.
	reservationID, err := s.reserveNights(ctx, tx, rc, record, prepared, expiresAt)
	if err != nil {
		return BookingView{}, err
	}
	record.EntitlementReservationID = &reservationID

	for i, night := range prepared.quote.NightlyAmounts {
		if err := s.bookings.CreateBookingNight(ctx, tx, rc.TenantID, BookingNightRow{
			BookingID: record.ID, StayDate: prepared.stayDates[i], RoomTypeID: in.RoomTypeID,
			UnitAmount: night.Amount, PayerAmount: night.PayerAmount,
			MemberAmount: night.MemberAmount, CurrencyCode: prepared.quote.CurrencyCode,
		}); err != nil {
			return BookingView{}, err
		}
	}
	for _, guest := range in.Guests {
		if err := s.bookings.CreateBookingGuest(ctx, tx, rc.TenantID, BookingGuestRow{
			BookingID: record.ID, PersonID: guest.PersonID, DisplayName: guest.DisplayName,
			GuestType: guest.GuestType, IsMinor: guest.IsMinor,
		}); err != nil {
			return BookingView{}, err
		}
	}

	if err := s.record(ctx, tx, rc, ActionBookingHold, ResourceBooking, record.ID, map[string]any{
		"reference": record.Reference, "room_type": in.RoomTypeID.String(),
		"nights": prepared.nights, "expires_at": expiresAt.Format(time.RFC3339),
	}); err != nil {
		return BookingView{}, err
	}
	// The member is told inside this transaction: a hold that rolled back set nothing
	// aside and must have told nobody it had.
	if err := s.notifyBookingHeld(ctx, tx, rc.TenantID, record, prepared.room.PropertyName); err != nil {
		return BookingView{}, err
	}
	return s.loadBooking(ctx, tx, rc.TenantID, record)
}

// reserveNights takes the plan's hold for the nights the plan carries.
//
// The account is found through WP-I5-05's service-to-entitlement mapping -- the same
// mapping the eligibility check that produced the quote used -- and never by comparing the
// service's code with an entitlement's. Which balance a room night spends is the tenant's
// configuration: matching on the code would work for whichever tenant named the two the
// same and quietly reserve the wrong balance, or none, for every other one.
func (s *Service) reserveNights(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record BookingRecord, prepared preparedHold, expiresAt time.Time,
) (uuid.UUID, error) {
	code, err := s.bookings.EntitlementCodeForService(ctx, tx, rc.TenantID, record.EnrollmentID,
		prepared.room.ServiceDefinitionID, prepared.checkIn)
	if err != nil {
		return uuid.Nil, err
	}
	accounts, err := s.ledger.ResolveAccounts(ctx, tx, rc.TenantID, record.PersonID, prepared.checkIn)
	if err != nil {
		return uuid.Nil, err
	}
	accountID := uuid.Nil
	for _, account := range accounts {
		if account.Definition.Code == code {
			accountID = account.ID
			break
		}
	}
	if accountID == uuid.Nil {
		return uuid.Nil, ErrEntitlementAccountNotFound
	}
	// The covered nights and never the stay's length. The frozen quote already says how many
	// nights the plan carries; reserving more would hold entitlement the member has been
	// told they will be paying for themselves.
	quantity, err := benefitdomain.ParseQuantity(fmt.Sprintf("%d", prepared.snapshot.CoveredNights))
	if err != nil {
		return uuid.Nil, fmt.Errorf("accommodation: night quantity: %w", err)
	}
	reservation, err := s.ledger.Reserve(ctx, tx, ledger.ReserveInput{
		TenantID: rc.TenantID, AccountID: accountID, Quantity: quantity,
		ReferenceType: ledger.ReferenceBooking, ReferenceID: record.ID,
		Key: bookingReserveKey(record.ID), ExpiresAt: &expiresAt,
		ReasonCode: reasonBookingHold, ActorID: rc.Principal.ActorID,
	})
	if err != nil {
		if errors.Is(err, ledger.ErrInsufficient) {
			// The plan covers this kind of room and has fewer nights left than the stay is
			// long. It is its own refusal rather than "no account", because the member can
			// act on it: a shorter stay would go through.
			return uuid.Nil, ErrEntitlementInsufficient
		}
		return uuid.Nil, err
	}
	if err := s.bookings.SetBookingReservation(ctx, tx, rc.TenantID, record.ID, reservation.ID); err != nil {
		return uuid.Nil, err
	}
	return reservation.ID, nil
}

// createBookingWithReference retries a reference collision rather than reporting it. The
// tail is forty random bits, so a collision means two bookings drew the same number on the
// same day, which is worth one more draw and no more explanation than that.
func (s *Service) createBookingWithReference(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, row NewBookingRow,
) (BookingRecord, error) {
	for attempt := 0; attempt < domain.ReferenceAttempts; attempt++ {
		reference, err := domain.NewBookingReference(s.now())
		if err != nil {
			return BookingRecord{}, err
		}
		row.Reference = reference
		record, err := s.bookings.CreateBooking(ctx, tx, rc.TenantID, row)
		switch {
		case err == nil:
			return record, nil
		case errors.Is(err, ErrBookingReferenceCollision):
			continue
		default:
			return BookingRecord{}, err
		}
	}
	return BookingRecord{}, ErrBookingReferenceCollision
}

// firstUnavailable is the refusal the package exists for: the first night of the stay with
// no room, or nil when every night has one.
//
// It walks the stay's own dates rather than the rows that came back, because the two are
// not the same list. A night the provider has opened nothing on has no row at all, and
// reading "no row" as "not full" is precisely how a guest ends up with nowhere to sleep on
// the third night of a stay somebody sold them.
func firstUnavailable(rows []InventoryNight, stayDates []time.Time) *RoomUnavailable {
	byDate := make(map[string]InventoryNight, len(rows))
	for _, row := range rows {
		byDate[domain.Day(row.StayDate).Format(time.DateOnly)] = row
	}
	for _, day := range stayDates {
		row, found := byDate[day.Format(time.DateOnly)]
		if !found {
			return &RoomUnavailable{StayDate: day}
		}
		if row.Available < 1 {
			return &RoomUnavailable{
				StayDate: day, Allotted: true, Capacity: row.Capacity,
				Held: row.Held, Confirmed: row.Confirmed,
			}
		}
	}
	return nil
}

// ReleaseHold is the member giving the room back before the countdown runs out. It has
// exactly the effect the expiry sweep has -- the counters come back and the nights are
// released -- and it differs from it in one way that matters: it is on the record as a
// command somebody gave, with its own reason code, rather than as a deadline nobody met.
func (s *Service) ReleaseHold(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (BookingView, error) {
	if s.bookings == nil || s.ledger == nil {
		return BookingView{}, errors.New("accommodation: this process cannot release a hold")
	}
	var out BookingView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := s.bookings.GetBooking(ctx, tx, rc.TenantID, id, personBoundary(rc), scopeOf(rc)); err != nil {
			return err
		}
		record, err := s.bookings.LockBooking(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		if !domain.InList(record.Status, domain.HeldBookingStatuses) {
			return ErrBookingTransitionInvalid
		}
		if err := s.giveBackRoom(ctx, tx, rc, record, reasonHoldReleased); err != nil {
			return err
		}
		moved, err := s.bookings.CancelBookingRow(ctx, tx, rc.TenantID, record.ID, s.now().UTC(),
			domain.CancelReasonHoldReleased, rc.Principal.ActorID)
		if err != nil {
			return err
		}
		if !moved {
			return ErrBookingTransitionInvalid
		}
		if err := s.record(ctx, tx, rc, ActionBookingRelease, ResourceBooking, record.ID,
			map[string]any{"reference": record.Reference, "nights": record.Nights}); err != nil {
			return err
		}
		if err := s.notifyBookingCancelled(ctx, tx, rc.TenantID, record,
			domain.CancelReasonHoldReleased); err != nil {
			return err
		}
		record.Status = domain.BookingCancelled
		out, err = s.loadBooking(ctx, tx, rc.TenantID, record)
		return err
	})
	if err != nil {
		return BookingView{}, err
	}
	return out, nil
}

// giveBackRoom returns the inventory and the entitlement a held booking took, under the
// same lock order the hold took them in.
//
// The release of the plan's nights is skipped rather than failed when the ledger has
// already closed the hold. WP-I2-03's own expiry sweep releases a reservation whose
// deadline has passed, and this vertical's sweep releases the same one: whichever gets
// there first, the entitlement is back where it belongs, and refusing here would leave a
// booking nobody can cancel.
func (s *Service) giveBackRoom(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record BookingRecord, reason string,
) error {
	if _, err := s.bookings.LockInventoryNights(ctx, tx, rc.TenantID, record.RoomTypeID,
		record.CheckIn, record.LastNight()); err != nil {
		return err
	}
	if err := s.bookings.AddHeld(ctx, tx, rc.TenantID, record.RoomTypeID,
		record.CheckIn, record.LastNight(), -1); err != nil {
		return err
	}
	if record.EntitlementReservationID == nil {
		return nil
	}
	quantity, err := benefitdomain.ParseQuantity(fmt.Sprintf("%d", record.Nights))
	if err != nil {
		return fmt.Errorf("accommodation: night quantity: %w", err)
	}
	_, err = s.ledger.Release(ctx, tx, ledger.MovementInput{
		TenantID: rc.TenantID, ReservationID: *record.EntitlementReservationID,
		Quantity: quantity, Key: bookingReleaseKey(record.ID, reason),
		ReasonCode: reason, ActorID: rc.Principal.ActorID,
	})
	switch {
	case errors.Is(err, ledger.ErrIdempotentReplay), errors.Is(err, ledger.ErrReservationClosed),
		errors.Is(err, ledger.ErrQuantityRemainder):
		return nil
	default:
		return err
	}
}

// GetBooking returns one booking with its nights and its guests, inside the caller's own
// boundaries: a member sees their own, a provider sees the ones at its properties, and a
// payer sees all of them.
func (s *Service) GetBooking(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (BookingView, error) {
	if s.bookings == nil {
		return BookingView{}, ErrBookingNotFound
	}
	var out BookingView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		record, err := s.bookings.GetBooking(ctx, tx, rc.TenantID, id, personBoundary(rc), scopeOf(rc))
		if err != nil {
			return err
		}
		out, err = s.loadBooking(ctx, tx, rc.TenantID, record)
		return err
	})
	if err != nil {
		return BookingView{}, err
	}
	return out, nil
}

// ListBookings pages through the bookings this caller may see, filtered by person,
// property, status and arrival range.
//
// A member's own binding is applied on top of whatever they asked for rather than instead
// of it: a member who filters by somebody else's person id gets their own bookings and not
// their neighbour's, because the boundary is computed here and the filter is only a
// narrowing of it.
func (s *Service) ListBookings(ctx context.Context, rc identity.RequestContext, f BookingFilter) (BookingPage, error) {
	if s.bookings == nil {
		return BookingPage{}, nil
	}
	if f.Status != "" && !domain.InList(f.Status, domain.BookingStatuses) {
		return BookingPage{}, fieldError("status", "ENUM", "tanınmayan rezervasyon durumu")
	}
	after, pageSize, err := s.paging(f.Cursor, f.Limit)
	if err != nil {
		return BookingPage{}, err
	}

	person := personBoundary(rc)
	if person == nil {
		person = f.PersonID
	}
	var out BookingPage
	err = s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := s.bookings.ListBookings(ctx, tx, rc.TenantID, BookingQuery{
			ScopeIDs: scopeOf(rc), PersonID: person, PropertyID: f.PropertyID,
			Status: f.Status, CheckInFrom: f.CheckInFrom, CheckInTo: f.CheckInTo,
			After: after, PageSize: pageSize + 1,
		})
		if err != nil {
			return err
		}
		if len(rows) > pageSize {
			// A process with no cursor codec answers no next page rather than half of one:
			// the sweeps hold none, and a page whose cursor could not be signed is a page
			// nobody could ask for the rest of.
			if s.cursors != nil {
				out.NextCursor = s.cursors.Encode(httpx.Cursor{
					CreatedAt: rows[pageSize-1].CreatedAt, ID: rows[pageSize-1].ID,
				})
			}
			rows = rows[:pageSize]
		}
		out.Items = make([]BookingView, 0, len(rows))
		for _, row := range rows {
			view, err := s.loadBooking(ctx, tx, rc.TenantID, row)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, view)
		}
		return nil
	})
	if err != nil {
		return BookingPage{}, err
	}
	return out, nil
}

// ConfirmBooking turns a hold into a reservation request and hands it to the gate.
//
// What it does not do is decide. The eligibility gate, the document requirement and the
// rule trace are WP-I4-01's, and the answer -- approved outright, waiting on a document,
// refused -- arrives through the outbox event this package subscribes to. That is why the
// booking is still HOLD or PENDING_APPROVAL when this returns: confirming is asking, and
// the room stays held until somebody answers.
//
// Three refusals happen here rather than later, because all three are things the member can
// act on and none of them should reach a reviewer's queue. A quote older than the tenant's
// accommodation.quote_ttl_minutes is refused and the member is sent back to search: the
// prices in the snapshot are what they will be charged, and a price nobody has looked at
// for an hour is not a price anybody should be committed to. A member share above
// accommodation.stepup_member_amount takes a re-entered password. And a contract version
// with no lodging terms is refused outright -- a stay confirmed with no cancellation policy
// is a stay nobody can cancel fairly, and inventing a default here would be inventing a fee.
func (s *Service) ConfirmBooking(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (BookingView, error) {
	if s.bookings == nil {
		return BookingView{}, ErrBookingNotFound
	}
	var out BookingView
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		view, err := s.confirm(ctx, tx, rc, id)
		out = view
		return err
	})
	if err != nil {
		return BookingView{}, err
	}
	return out, nil
}

func (s *Service) confirm(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	id uuid.UUID,
) (BookingView, error) {
	if _, err := s.bookings.GetBooking(ctx, tx, rc.TenantID, id, personBoundary(rc), scopeOf(rc)); err != nil {
		return BookingView{}, err
	}
	record, err := s.bookings.LockBooking(ctx, tx, rc.TenantID, id)
	if err != nil {
		return BookingView{}, err
	}
	now := s.now().UTC()
	if record.Status != domain.BookingHold || record.HoldExpiresAt == nil ||
		!now.Before(*record.HoldExpiresAt) {
		return BookingView{}, ErrBookingTransitionInvalid
	}

	tenantSettings, err := settings.Load(ctx, tx, rc.TenantID)
	if err != nil {
		return BookingView{}, err
	}
	snapshot, err := decodeQuoteSnapshot(record.QuoteSnapshot)
	if err != nil {
		return BookingView{}, err
	}
	if now.After(snapshot.QuotedAt.Add(time.Duration(tenantSettings.QuoteTTLMinutes) * time.Minute)) {
		return BookingView{}, ErrQuoteStale
	}
	if err := requireStepUp(rc, snapshot.MemberAmount, tenantSettings.StepUpMemberAmount); err != nil {
		return BookingView{}, err
	}

	room, err := s.bookings.RoomTypeBookingContext(ctx, tx, rc.TenantID, record.RoomTypeID, nil)
	if err != nil {
		return BookingView{}, err
	}
	// The policy is read here so a version with no lodging terms is a 409 the member sees
	// now, rather than a reservation request that reaches a reviewer and then cannot be
	// turned into a booking. It is read again, and written, when the approval arrives.
	if _, err := s.policySnapshot(ctx, tx, rc, record, room.PropertyTimezone); err != nil {
		return BookingView{}, err
	}

	request, err := s.requests.CreateReservation(ctx, tx, rc, BookingRequestInput{
		PersonID: record.PersonID, EnrollmentID: record.EnrollmentID,
		ProviderOrganizationID: room.ProviderOrganizationID,
		ServiceDefinitionID:    room.ServiceDefinitionID,
		ServiceDate:            record.CheckIn,
		RequestedStartAt:       record.CheckIn, RequestedEndAt: record.CheckOut,
		// The line is the plan's part of the stay and not the whole of it: the quantity is
		// the nights the plan carries and the amount is what it is being asked to carry on
		// them. A line of three nights against a plan that covers two would ask a reviewer
		// to approve a night the member has already been told is theirs, and the
		// authorization would then promise more than the hold it adopts actually holds.
		Nights: snapshot.CoveredNights, UnitType: room.ServiceUnitType,
		Amount: snapshot.PayerAmount, CurrencyCode: snapshot.CurrencyCode,
		Channel: record.Channel,
		// What the hold already took. Without it the gate would judge this line against a
		// balance this very booking drew down fifteen minutes ago.
		HeldNights: strconv.Itoa(snapshot.CoveredNights),
	})
	if err != nil {
		return BookingView{}, err
	}
	written, err := s.bookings.SetBookingRequest(ctx, tx, rc.TenantID, record.ID, request.ID,
		rc.Principal.ActorID)
	if err != nil {
		return BookingView{}, err
	}
	if !written {
		// Somebody confirmed this booking between the lock and here, which the lock makes
		// impossible -- so it is worth refusing loudly rather than raising a second request.
		return BookingView{}, ErrBookingTransitionInvalid
	}
	record.ServiceRequestID = &request.ID

	if pendingRequest(request.Status) {
		// The reviewer has it and the room must not be lost while they decide. The
		// countdown moves out; the status says why.
		expires := now.Add(PendingApprovalHold)
		moved, err := s.bookings.MarkPendingApproval(ctx, tx, rc.TenantID, record.ID, expires,
			rc.Principal.ActorID)
		if err != nil {
			return BookingView{}, err
		}
		if moved {
			record.Status = domain.BookingPendingApproval
			record.HoldExpiresAt = &expires
			if err := s.notifyBookingPendingApproval(ctx, tx, rc.TenantID, record,
				room.PropertyName, request.Status); err != nil {
				return BookingView{}, err
			}
		}
	}

	if err := s.record(ctx, tx, rc, ActionBookingConfirm, ResourceBooking, record.ID, map[string]any{
		"reference": record.Reference, "service_request": request.ID.String(),
		"request_status": request.Status,
	}); err != nil {
		return BookingView{}, err
	}
	return s.loadBooking(ctx, tx, rc.TenantID, record)
}

// policySnapshot freezes WP-I6-04's lodging terms for the contract version behind this
// stay. The version is read from the contract rather than named by the caller: the terms a
// booking carries have to be the ones behind the price the member was quoted, and a caller
// that could name a version could freeze somebody else's policy onto this stay.
func (s *Service) policySnapshot(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
	record BookingRecord, timeZone string,
) (json.RawMessage, error) {
	versionID, err := s.bookings.ContractVersionForProperty(ctx, tx, rc.TenantID,
		record.PropertyID, record.CheckIn)
	if err != nil {
		return nil, err
	}
	return s.policies.SnapshotPolicy(ctx, rc, versionID, timeZone)
}

// requireStepUp asks for the password again above the tenant's threshold. The comparison is
// on exact decimals and never on floats: a threshold that rounded would be a step-up that
// fired on some amounts and not on others for no reason anybody could explain.
func requireStepUp(rc identity.RequestContext, memberAmount, threshold string) error {
	if rc.StepUpValid {
		return nil
	}
	amount, err := benefitdomain.ParseQuantity(memberAmount)
	if err != nil {
		return fmt.Errorf("accommodation: member amount %q: %w", memberAmount, err)
	}
	limit, err := benefitdomain.ParseQuantity(threshold)
	if err != nil {
		return fmt.Errorf("accommodation: step-up threshold %q: %w", threshold, err)
	}
	if amount.Cmp(limit) > 0 {
		return identity.ErrStepUpRequired
	}
	return nil
}

// pendingRequest reports whether the gate handed the request to a person instead of
// deciding it. Both pending statuses mean the same thing to a booking: nobody has answered
// yet, and the room stays held.
func pendingRequest(status string) bool {
	return status == requestPendingReview || status == requestPendingDocument
}

// loadBooking assembles the view: the row, its nights, its guests and the countdown.
func (s *Service) loadBooking(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID,
	record BookingRecord,
) (BookingView, error) {
	nights, err := s.bookings.ListBookingNights(ctx, tx, tenantID, record.ID)
	if err != nil {
		return BookingView{}, err
	}
	guests, err := s.bookings.ListBookingGuests(ctx, tx, tenantID, record.ID)
	if err != nil {
		return BookingView{}, err
	}
	return BookingView{
		Booking: record, Nights: nights, Guests: guests,
		SecondsToExpiry: secondsToExpiry(record, s.now().UTC()),
	}, nil
}

// secondsToExpiry is the countdown, computed on the server. A booking that is not holding a
// room has none, and one whose deadline has passed has zero rather than a negative number:
// "minus four seconds left" is not a thing a screen can render.
func secondsToExpiry(record BookingRecord, now time.Time) int {
	if !domain.InList(record.Status, domain.HeldBookingStatuses) || record.HoldExpiresAt == nil {
		return 0
	}
	remaining := record.HoldExpiresAt.Sub(now)
	if remaining <= 0 {
		return 0
	}
	return int(remaining.Seconds())
}

// personBoundary is the person a caller is bound to, or nil for a desk. Every booking read
// passes it to the repository, so a member's own binding narrows the query rather than
// being checked afterwards.
func personBoundary(rc identity.RequestContext) *uuid.UUID {
	if personID, bound := rc.PersonScope(); bound && personID != uuid.Nil {
		id := personID
		return &id
	}
	return nil
}

// channelOf is where the booking came from. A caller that named one is believed as far as
// the closed list goes; one that named nothing is placed by its own grants, because the
// channel is what a later report counts and guessing one for everybody would make the
// count meaningless.
func channelOf(named string, rc identity.RequestContext) string {
	if domain.InList(named, domain.Channels) {
		return named
	}
	if _, bound := rc.PersonScope(); bound {
		return domain.ChannelMemberPortal
	}
	if len(scopeOf(rc)) > 0 {
		return domain.ChannelProviderPortal
	}
	return domain.ChannelBackoffice
}

// findRoomType picks the searched room type out of the loaded world.
func findRoomType(rooms []AvailabilityRoomType, id uuid.UUID) (AvailabilityRoomType, bool) {
	for _, room := range rooms {
		if room.ID == id {
			return room, true
		}
	}
	return AvailabilityRoomType{}, false
}

// snapshotOf freezes the quote. Nothing in it is a reference to something that can change:
// the amounts are copies, the entitlement is what the plan had at this moment, and the
// evaluation id is the record of what the member was shown.
func snapshotOf(quote QuoteView, verdict eligibilityVerdict, prepared preparedHold,
	now time.Time,
) QuoteSnapshot {
	evaluationID := verdict.evaluationID
	out := QuoteSnapshot{
		Version: QuoteSnapshotVersion, QuotedAt: now,
		PropertyID: prepared.room.PropertyID, RoomTypeID: prepared.room.RoomTypeID,
		ServiceDefinitionID: prepared.room.ServiceDefinitionID,
		CurrencyCode:        quote.CurrencyCode, TotalAmount: quote.TotalAmount,
		PayerAmount: quote.PayerAmount, MemberAmount: quote.MemberAmount,
		Nights:        make([]QuoteNight, 0, len(quote.NightlyAmounts)),
		CoveredNights: quote.CoveredNights,
		Eligible:      verdict.eligibleForWholeStay(prepared.nights),
	}
	if evaluationID != uuid.Nil {
		out.EvaluationID = &evaluationID
	}
	for _, night := range quote.NightlyAmounts {
		out.Nights = append(out.Nights, QuoteNight{
			StayDate: night.StayDate.Format(time.DateOnly), Amount: night.Amount,
			PayerAmount: night.PayerAmount, MemberAmount: night.MemberAmount,
		})
	}
	if verdict.entitlement != nil {
		out.Entitlement = &QuoteBalance{
			EntitlementCode: verdict.entitlement.EntitlementCode,
			Unit:            verdict.entitlement.Unit,
			Remaining:       verdict.entitlement.Remaining,
		}
	}
	return out
}

// DecodeQuoteSnapshot reads a frozen quote back. It is exported so the transport can render
// what the member agreed to without a second definition of the shape.
func DecodeQuoteSnapshot(raw json.RawMessage) (QuoteSnapshot, error) {
	return decodeQuoteSnapshot(raw)
}

func decodeQuoteSnapshot(raw json.RawMessage) (QuoteSnapshot, error) {
	var out QuoteSnapshot
	if len(raw) == 0 {
		return out, errors.New("accommodation: the booking carries no frozen quote")
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return QuoteSnapshot{}, fmt.Errorf("accommodation: read frozen quote: %w", err)
	}
	return out, nil
}

// bookingReserveKey and bookingReleaseKey are the ledger idempotency keys of a booking's
// hold and of giving it back. Both are derived from the booking rather than from a clock,
// so a replayed command reserves once and releases once; the reason is part of the release
// key so an expiry and a cancellation of the same booking are two movements and either of
// them run twice is still one.
func bookingReserveKey(bookingID uuid.UUID) string { return "booking:" + bookingID.String() }

func bookingReleaseKey(bookingID uuid.UUID, reason string) string {
	return "booking-release:" + reason + ":" + bookingID.String()
}

// QuoteUnavailable is a room type that could not be priced for this stay, with the reason
// code the search would have shown. It is a refusal rather than a hold at a guessed price:
// a room set aside with no agreed amount is a room nobody can be charged for.
type QuoteUnavailable struct {
	Reason string
}

// Error implements error.
func (e *QuoteUnavailable) Error() string { return ErrQuoteUnavailable.Error() }

// Is lets errors.Is reach the sentinel.
func (e *QuoteUnavailable) Is(target error) bool { return target == ErrQuoteUnavailable }

// ErrQuoteUnavailable is the sentinel behind QuoteUnavailable.
var ErrQuoteUnavailable = errors.New("accommodation: this room type cannot be priced for these dates")

// validateHold answers 422 before anything is read or locked.
func validateHold(in HoldInput, checkIn, checkOut time.Time, maxNights int) (int, error) {
	ve := &domain.ValidationError{}
	if in.RoomTypeID == uuid.Nil {
		ve.Add("roomTypeId", "REQUIRED", "oda tipi zorunlu")
	}
	if in.Adults < 1 || in.Adults > 20 {
		ve.Add("adults", "RANGE", "1-20 arasında olmalı")
	}
	if in.Children < 0 || in.Children > 20 {
		ve.Add("children", "RANGE", "0-20 arasında olmalı")
	}
	if checkIn.IsZero() {
		ve.Add("checkIn", "REQUIRED", "giriş tarihi zorunlu")
	}
	if checkOut.IsZero() {
		ve.Add("checkOut", "REQUIRED", "çıkış tarihi zorunlu")
	}
	if in.Channel != "" && !domain.InList(in.Channel, domain.Channels) {
		ve.Add("channel", "ENUM", "tanınmayan kanal")
	}
	for i, guest := range in.Guests {
		if !domain.InList(guest.GuestType, domain.GuestTypes) {
			ve.Add(fmt.Sprintf("guests[%d].guestType", i), "ENUM", "tanınmayan misafir tipi")
		}
		if len(guest.DisplayName) == 0 || len(guest.DisplayName) > 200 {
			ve.Add(fmt.Sprintf("guests[%d].displayName", i), "RANGE", "1-200 karakter olmalı")
		}
		if guest.GuestType == domain.GuestOther && guest.PersonID != nil {
			ve.Add(fmt.Sprintf("guests[%d].personId", i), "CONFLICT",
				"kayıtlı olmayan misafir bir kişiye bağlanamaz")
		}
		if guest.GuestType != domain.GuestOther && guest.PersonID == nil {
			ve.Add(fmt.Sprintf("guests[%d].personId", i), "REQUIRED",
				"üye ve bakmakla yükümlü olunan kişi için kimlik zorunlu")
		}
	}
	if len(in.Guests) > 0 && len(in.Guests) != in.Adults+in.Children {
		ve.Add("guests", "RANGE", "misafir sayısı yetişkin ve çocuk toplamına eşit olmalı")
	}
	if ve.Len() > 0 {
		return 0, ve
	}

	nights, err := domain.Nights(checkIn, checkOut)
	if err != nil {
		ve.Add("checkOut", "RANGE", "giriş tarihinden sonra olmalı")
		return 0, ve
	}
	if nights > maxNights {
		ve.Add("checkOut", "RANGE", fmt.Sprintf("en fazla %d gece rezerve edilebilir", maxNights))
		return 0, ve
	}
	return nights, nil
}
