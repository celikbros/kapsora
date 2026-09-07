package application

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/benefit/ledger"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Permissions guarding the booking half of the vertical (migration 000008). They are two
// grants and not one on purpose: `accommodation.booking.create` is what a member holds --
// it lets them hold and confirm a room for themselves -- and
// `accommodation.booking.manage` is what a reservation desk holds, which is the same
// commands for somebody else.
const (
	PermissionBookingCreate = "accommodation.booking.create"
	PermissionBookingManage = "accommodation.booking.manage"
)

// Errors of the booking half, mapped by the transport to problem codes.
var (
	// ErrBookingNotFound is a booking this caller cannot see. An unknown id, another
	// member's booking and another provider's are deliberately indistinguishable.
	ErrBookingNotFound = errors.New("accommodation: booking not found")
	// ErrRoomUnavailable is the refusal the whole package exists for: on at least one
	// night of the stay there is no room left. It carries the first such night, because a
	// member looking at a two-week stay needs to know which night to move.
	ErrRoomUnavailable = errors.New("accommodation: no room is available on every night of the stay")
	// ErrQuoteStale refuses a confirmation of a quote older than the tenant's
	// accommodation.quote_ttl_minutes. The member is sent back to search rather than shown
	// a price that may no longer be the one the contract states.
	ErrQuoteStale = errors.New("accommodation: the quote this booking was held on is too old")
	// ErrLodgingTermsMissing refuses a confirmation against a contract version with no
	// lodging terms. A stay confirmed with no cancellation policy is a stay nobody can
	// cancel fairly, and inventing a default would be inventing a fee.
	ErrLodgingTermsMissing = errors.New("accommodation: the contract version has no lodging terms")
	// ErrBookingTransitionInvalid refuses a command the booking's status does not have --
	// confirming an expired hold, releasing a confirmed stay.
	ErrBookingTransitionInvalid = errors.New("accommodation: the booking is not in a state for this command")
	// ErrBookingAlreadyLive is uq_booking_live_per_person_arrival answering: this person
	// already has a live booking of this room type arriving on this day.
	ErrBookingAlreadyLive = errors.New("accommodation: this person already has a live booking of this room type on this date")
	// ErrEnrollmentNotFound is a person with no active enrollment covering the first
	// night. There is no plan to book against and the hold refuses rather than guessing.
	ErrEnrollmentNotFound = errors.New("accommodation: the person has no active enrollment for these dates")
	// ErrEntitlementAccountNotFound is a room type whose service maps onto no open
	// entitlement account for this person. The nights cannot be reserved, so the room is
	// not held: a hold with nothing behind it is a promise the plan cannot keep.
	ErrEntitlementAccountNotFound = errors.New("accommodation: no entitlement account covers this room type")
	// ErrEntitlementInsufficient is a plan that carries no night of this stay: the person
	// is not eligible for this room's service, or the balance is exhausted.
	//
	// Fewer nights than the stay is long is deliberately *not* this refusal. The search
	// already shows a member with two nights left and a three-night stay that the plan
	// carries two and the third is theirs; refusing the hold would refuse exactly the
	// booking they were quoted. Only a stay the plan carries nothing of is refused, and it
	// stays separate from the account being missing because the answers differ: one plan
	// does not cover this kind of room at all, and the other has run out this year.
	ErrEntitlementInsufficient = errors.New("accommodation: the plan carries no night of this stay")
	// ErrOccupancyExceeded is a party larger than the room type sleeps.
	ErrOccupancyExceeded = errors.New("accommodation: the party does not fit this room type")
	// ErrBookingReferenceCollision reports that a reference was already taken. It never
	// reaches a caller: the create draws another one.
	ErrBookingReferenceCollision = errors.New("accommodation: booking reference collision")
	// ErrVoucherNotAvailable is a voucher asked for on a booking that has no authorization
	// behind it yet -- a hold, or a confirmation still waiting on a reviewer.
	ErrVoucherNotAvailable = errors.New("accommodation: this booking has no authorization to issue a voucher on")
)

// RoomUnavailable is the detail behind ErrRoomUnavailable: the first night of the stay
// that has no room, and what stood on it when the lock was taken.
//
// It carries the numbers as well as the date because a member and a provider clerk read
// the same refusal differently: the member needs the night, and the clerk needs to see
// that the allotment is full rather than absent.
type RoomUnavailable struct {
	StayDate time.Time
	// Allotted is false for a night the provider has opened nothing on at all, which is a
	// different fact from a night that is full and has to stay one.
	Allotted  bool
	Capacity  int
	Held      int
	Confirmed int
}

// Error implements error so the refusal can be returned directly and still be matched with
// errors.Is(err, ErrRoomUnavailable).
func (e *RoomUnavailable) Error() string { return ErrRoomUnavailable.Error() }

// Is lets errors.Is reach the sentinel.
func (e *RoomUnavailable) Is(target error) bool { return target == ErrRoomUnavailable }

// BookingRecord is one accommodation.booking row as this layer reads it. Every money value
// lives in the snapshots rather than here: a booking's amounts are the quote's, frozen, and
// a second copy on the header would be a second answer.
type BookingRecord struct {
	ID                       uuid.UUID
	Reference                string
	PersonID                 uuid.UUID
	EnrollmentID             uuid.UUID
	ProgramID                uuid.UUID
	PropertyID               uuid.UUID
	RoomTypeID               uuid.UUID
	CheckIn                  time.Time
	CheckOut                 time.Time
	Nights                   int
	Adults                   int
	Children                 int
	Status                   string
	HoldExpiresAt            *time.Time
	EntitlementReservationID *uuid.UUID
	ServiceRequestID         *uuid.UUID
	AuthorizationID          *uuid.UUID
	VoucherID                *uuid.UUID
	QuoteSnapshot            json.RawMessage
	PolicySnapshot           json.RawMessage
	Channel                  string
	ConfirmedAt              *time.Time
	CheckedInAt              *time.Time
	CheckedOutAt             *time.Time
	CancelledAt              *time.Time
	CancelReasonCode         *string
	ActualNights             *int
	CreatedAt                time.Time
	UpdatedAt                time.Time
	RowVersion               int64
}

// LastNight is the final night of the stay, which is the day before check-out. Every
// inventory range this package writes is bounded by it: an allotment on the check-out day
// belongs to the next guest.
func (b BookingRecord) LastNight() time.Time { return b.CheckOut.AddDate(0, 0, -1) }

// NewBookingRow is a booking as it is written at the hold.
type NewBookingRow struct {
	Reference                string
	PersonID                 uuid.UUID
	EnrollmentID             uuid.UUID
	ProgramID                uuid.UUID
	PropertyID               uuid.UUID
	RoomTypeID               uuid.UUID
	CheckIn                  time.Time
	CheckOut                 time.Time
	Nights                   int
	Adults                   int
	Children                 int
	Status                   string
	HoldExpiresAt            *time.Time
	EntitlementReservationID *uuid.UUID
	QuoteSnapshot            json.RawMessage
	Channel                  string
	ActorID                  uuid.UUID
}

// BookingNightRow is one night of the stay as it is written.
type BookingNightRow struct {
	BookingID    uuid.UUID
	StayDate     time.Time
	RoomTypeID   uuid.UUID
	UnitAmount   string
	PayerAmount  string
	MemberAmount string
	CurrencyCode string
}

// BookingNightRecord is one night as it is read; every amount is the exact decimal text the
// numeric column holds.
type BookingNightRecord struct {
	StayDate     time.Time
	RoomTypeID   uuid.UUID
	UnitAmount   string
	PayerAmount  string
	MemberAmount string
	CurrencyCode string
}

// BookingGuestRow is one guest as it is written.
type BookingGuestRow struct {
	BookingID   uuid.UUID
	PersonID    *uuid.UUID
	DisplayName string
	GuestType   string
	IsMinor     bool
}

// BookingGuestRecord is one guest as it is read.
type BookingGuestRecord struct {
	ID          uuid.UUID
	PersonID    *uuid.UUID
	DisplayName string
	GuestType   string
	IsMinor     bool
}

// BookingQuery is the repository-level booking filter. `PersonID` is the member boundary
// and `ScopeIDs` the provider one; a nil ScopeIDs means the whole tenant and a nil PersonID
// means every member, so the caller has to decide both rather than inherit either.
type BookingQuery struct {
	ScopeIDs    []uuid.UUID
	PersonID    *uuid.UUID
	PropertyID  *uuid.UUID
	Status      string
	CheckInFrom *time.Time
	CheckInTo   *time.Time
	After       *httpx.Cursor
	PageSize    int
}

// InventoryNight is one locked night with the availability the database computed.
type InventoryNight struct {
	StayDate  time.Time
	Capacity  int
	Held      int
	Confirmed int
	Available int
}

// RoomTypeBookingContext is everything a hold decides on, in one read: the room, the
// building, the provider behind it, the clock its nights are counted against, and the
// catalogue service both the request line and the entitlement hang off.
type RoomTypeBookingContext struct {
	RoomTypeID             uuid.UUID
	RoomTypeCode           string
	RoomTypeName           string
	MaxAdults              int
	MaxChildren            int
	MaxOccupancy           int
	ServiceDefinitionID    uuid.UUID
	ServiceDefinitionCode  string
	ServiceUnitType        string
	ServiceActive          bool
	RoomTypeStatus         string
	PropertyID             uuid.UUID
	PropertyName           string
	PropertyTimezone       string
	PropertyStatus         string
	ProviderOrganizationID uuid.UUID
}

// PersonEnrollment is the plan a stay is booked under.
type PersonEnrollment struct {
	EnrollmentID uuid.UUID
	ProgramID    uuid.UUID
}

// ReminderRow is one booking the day-before reminder covers, with the property's own name
// and zone.
type ReminderRow struct {
	BookingID    uuid.UUID
	Reference    string
	PersonID     uuid.UUID
	CheckIn      time.Time
	PropertyName string
	Timezone     string
}

// BookingRepository is the persistence port of the booking half. Every method runs inside
// the caller's transaction, which the service has already bound to the tenant, so RLS is
// active for every statement.
//
// LockInventoryNights is the one method whose ordering is part of its contract: it reads
// the range in stay_date order with FOR UPDATE, and every command that moves a counter
// calls it first. That single ordering is what turns concurrent holds into a queue rather
// than a deadlock, and it is stated on the port so no adapter can quietly reorder it.
type BookingRepository interface {
	// LockInventoryNights takes the nights of [from, to] FOR UPDATE **in stay_date
	// order**. A night the provider has opened nothing on is simply absent from the
	// result, which is how the caller tells "no allotment" from "full".
	LockInventoryNights(ctx context.Context, tx pgx.Tx, tenantID, roomTypeID uuid.UUID,
		from, to time.Time) ([]InventoryNight, error)
	// AddHeld moves `held` by a signed delta over the whole range, in one statement, with
	// the rows already locked.
	AddHeld(ctx context.Context, tx pgx.Tx, tenantID, roomTypeID uuid.UUID,
		from, to time.Time, delta int) error
	// ConfirmNights moves one room per night from `held` to `confirmed`.
	ConfirmNights(ctx context.Context, tx pgx.Tx, tenantID, roomTypeID uuid.UUID,
		from, to time.Time) error

	CreateBooking(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in NewBookingRow) (BookingRecord, error)
	GetBooking(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
		personID *uuid.UUID, scopeIDs []uuid.UUID) (BookingRecord, error)
	// LockBooking reads the row FOR UPDATE with no boundary applied: the sweep and the
	// outbox subscriber act for the tenant and for no provider, and a scope here would
	// leave their bookings unreachable.
	LockBooking(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (BookingRecord, error)
	GetBookingByRequest(ctx context.Context, tx pgx.Tx, tenantID, requestID uuid.UUID) (BookingRecord, error)
	ListBookings(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, q BookingQuery) ([]BookingRecord, error)

	// SetBookingRequest writes the RESERVATION request onto a booking still in HOLD with
	// none. It reports false when nothing moved, which is how a replayed confirmation
	// learns it is a replay.
	SetBookingRequest(ctx context.Context, tx pgx.Tx, tenantID, id, requestID uuid.UUID,
		actorID uuid.UUID) (bool, error)
	// ConfirmBookingRow moves HOLD or PENDING_APPROVAL to CONFIRMED. Every status write in
	// this port is guarded by its own predicate rather than by a flag, because the outbox
	// delivers at least once and the second delivery has to find nothing to do.
	ConfirmBookingRow(ctx context.Context, tx pgx.Tx, tenantID, id, authorizationID uuid.UUID,
		confirmedAt time.Time, policy json.RawMessage, actorID uuid.UUID) (bool, error)
	MarkPendingApproval(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
		holdExpiresAt time.Time, actorID uuid.UUID) (bool, error)
	CancelBookingRow(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
		cancelledAt time.Time, reasonCode string, actorID uuid.UUID) (bool, error)
	ExpireBookingRow(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (bool, error)
	SetBookingVoucher(ctx context.Context, tx pgx.Tx, tenantID, id, voucherID uuid.UUID,
		actorID uuid.UUID) error
	// SetBookingReservation writes the plan's hold onto the booking. It is separate from
	// the insert because the reservation references the booking, so the row has to exist
	// before anything can point at it.
	SetBookingReservation(ctx context.Context, tx pgx.Tx, tenantID, id, reservationID uuid.UUID) error

	CreateBookingNight(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in BookingNightRow) error
	ListBookingNights(ctx context.Context, tx pgx.Tx, tenantID, bookingID uuid.UUID) ([]BookingNightRecord, error)
	CreateBookingGuest(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in BookingGuestRow) error
	ListBookingGuests(ctx context.Context, tx pgx.Tx, tenantID, bookingID uuid.UUID) ([]BookingGuestRecord, error)

	RoomTypeBookingContext(ctx context.Context, tx pgx.Tx, tenantID, roomTypeID uuid.UUID,
		scopeIDs []uuid.UUID) (RoomTypeBookingContext, error)
	PersonEnrollmentForStay(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID,
		day time.Time, programID *uuid.UUID) (PersonEnrollment, error)
	// EntitlementCodeForService is the entitlement a room type's service draws on, under
	// the plan version in force for this enrollment on the first night. It is WP-I5-05's
	// mapping rather than the service's own code: which balance a room night spends is the
	// tenant's configuration, and a match on the code would reserve the wrong one for every
	// tenant that did not happen to name the two the same.
	EntitlementCodeForService(ctx context.Context, tx pgx.Tx, tenantID, enrollmentID,
		serviceDefinitionID uuid.UUID, day time.Time) (string, error)
	// ContractVersionForProperty is the published version of the property's own contract
	// covering the first night. It is read rather than passed in: the terms a booking
	// freezes have to be the ones behind the price the member was quoted, and a caller
	// that could name a version could freeze somebody else's policy onto this stay.
	ContractVersionForProperty(ctx context.Context, tx pgx.Tx, tenantID, propertyID uuid.UUID,
		day time.Time) (uuid.UUID, error)

	// ListExpiredHolds takes the holds past their deadline FOR UPDATE SKIP LOCKED, so two
	// schedulers that both believe they lead cannot expire one booking twice.
	ListExpiredHolds(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, before time.Time,
		limit int) ([]uuid.UUID, error)
	ListBookingsArrivingOn(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, day time.Time,
		limit int) ([]ReminderRow, error)
	// ActiveTenants lists the tenants the sweeps walk. platform.tenant carries no RLS, so
	// it is read outside a tenant transaction like the other cross-tenant jobs.
	ActiveTenants(ctx context.Context, tx pgx.Tx) ([]uuid.UUID, error)
}

// LedgerPort is benefit/ledger as this package uses it: take a hold on the nights, give it
// back, and find the account to take it on. This package never writes a balance, and the
// compiler agrees it cannot post anything else.
type LedgerPort interface {
	Reserve(ctx context.Context, tx pgx.Tx, in ledger.ReserveInput) (ledger.Reservation, error)
	Release(ctx context.Context, tx pgx.Tx, in ledger.MovementInput) (ledger.Reservation, error)
	// ResolveAccounts lists the entitlement accounts a person may spend from on a day,
	// including the family-shared ones of their principal. It is the ledger's own read
	// side: this package neither opens an account nor computes a balance.
	ResolveAccounts(ctx context.Context, tx pgx.Tx, tenantID, personID uuid.UUID,
		asOf time.Time) ([]ledger.Account, error)
}

// BookingRequestInput is the RESERVATION request a confirmation raises.
type BookingRequestInput struct {
	PersonID               uuid.UUID
	EnrollmentID           uuid.UUID
	ProviderOrganizationID uuid.UUID
	ServiceDefinitionID    uuid.UUID
	ServiceDate            time.Time
	RequestedStartAt       time.Time
	RequestedEndAt         time.Time
	// Nights is the quantity of the single line. One line for the whole stay, not one per
	// night: a reviewer decides "four of the five nights" by reducing a quantity, which is
	// the decision WP-I4-01 already knows how to record, and five lines would put a
	// fifteen-line request in front of anybody booking a fortnight.
	Nights       int
	UnitType     string
	Amount       string
	CurrencyCode string
	Channel      string
	// HeldNights is the entitlement this booking has already reserved, as an exact decimal
	// string. The gate adds it back before it judges the line, so a member whose plan covers
	// exactly the stay they are holding is not refused for spending what they hold. It is
	// always the same number as Nights here: both are the covered nights of the frozen quote.
	HeldNights string
}

// BookingRequestRef is the request, as this package needs it.
type BookingRequestRef struct {
	ID        uuid.UUID
	Reference string
	Status    string
}

// RequestPort creates and submits the RESERVATION request a confirmation is asked for
// with. It is a port rather than a direct call so this package cannot grow its own copy of
// the submit gate: the document requirement, the eligibility evaluation and the rule trace
// are WP-I4-01's, and a booking that decided any of them itself would be a second gate that
// could disagree with the one the reviewer reads.
//
// It takes the caller's transaction, so the booking, its request and the request's
// evaluations are one atomic fact.
type RequestPort interface {
	CreateReservation(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
		in BookingRequestInput) (BookingRequestRef, error)
}

// BookingAuthorizationInput is the hold this package asks WP-I4-02 for when a reservation
// request is approved.
type BookingAuthorizationInput struct {
	RequestID uuid.UUID
	ValidFrom time.Time
	ValidTo   time.Time
	// IdempotencyKey is derived from the booking rather than from a clock, so a
	// redelivered outbox event finds the authorization the first delivery created.
	IdempotencyKey string
	// AdoptReservationID is the hold the booking already placed on the plan. The
	// authorization takes it over instead of reserving the nights a second time; this
	// field is the whole of the no-double-reserve rule as this package states it.
	AdoptReservationID *uuid.UUID
	// AdoptReservationExpiresAt moves the adopted hold's deadline out to the end of the
	// stay, so the fifteen-minute countdown does not expire entitlement somebody has
	// already been promised.
	AdoptReservationExpiresAt *time.Time
	MemberAmount              string
}

// BookingAuthorizationRef is the hold, as this package needs it.
type BookingAuthorizationRef struct {
	ID uuid.UUID
	// ApprovedNights is what the authorization actually promised, which is not always what
	// the booking asked for: a reviewer may approve four of the five nights.
	ApprovedNights string
	ValidTo        time.Time
}

// BookingVoucherInput is the voucher command as this package issues it.
type BookingVoucherInput struct {
	AuthorizationID uuid.UUID
	ValidFrom       time.Time
	ValidTo         time.Time
	// Replace revokes whatever live voucher the authorization already has. It is what
	// makes reissuing a rotation rather than a second usable token.
	Replace          bool
	RevokeReasonCode string
}

// IssuedBookingVoucher is a voucher and its plaintext token. The token exists in this
// struct, in the response body it is written into, and nowhere else in this system: not in
// a column, not in a log line, not in an audit row, not in a notification variable, not in
// a URL.
type IssuedBookingVoucher struct {
	ID          uuid.UUID
	MaskedToken string
	ValidFrom   time.Time
	ValidTo     time.Time
	Token       string
}

// AuthorizationPort is WP-I4-02 narrowed to the two things a booking does with a promise:
// take one when the reservation request is approved, and mint the voucher the member shows
// at the desk.
//
// CreateForRequest opens a transaction of its own, because it is the authorization module's
// own command with its own audit rows and ledger movements. IssueVoucher takes the caller's
// transaction: the voucher and the `voucher_id` written on the booking have to commit
// together, or a member would hold a token for a booking that does not know it exists.
type AuthorizationPort interface {
	CreateForRequest(ctx context.Context, rc identity.RequestContext,
		in BookingAuthorizationInput) (BookingAuthorizationRef, error)
	IssueVoucher(ctx context.Context, tx pgx.Tx, rc identity.RequestContext,
		in BookingVoucherInput) (IssuedBookingVoucher, error)
}

// LodgingPolicyPort is WP-I6-04's SnapshotLodgingPolicy, seen from here: the terms of the
// contract version behind this stay, stamped with the moment and the property's zone, as
// the JSON the booking freezes.
//
// It answers ErrLodgingTermsMissing for a version whose terms nobody wrote, which is a
// refusal and not a default: a stay confirmed with no cancellation policy is a stay nobody
// can cancel fairly, and a policy invented here would be a fee invented here.
type LodgingPolicyPort interface {
	SnapshotPolicy(ctx context.Context, rc identity.RequestContext, contractVersionID uuid.UUID,
		timeZone string) (json.RawMessage, error)
}

// NoRequests, NoAuthorizations and NoPolicies are the refusing defaults of the three ports
// above. A process wired with none of them can read bookings, expire holds and send
// reminders, and can confirm nothing -- which is the honest behaviour of the scheduler, and
// says so rather than half-working.
type NoRequests struct{}

// CreateReservation implements RequestPort.
func (NoRequests) CreateReservation(context.Context, pgx.Tx, identity.RequestContext,
	BookingRequestInput,
) (BookingRequestRef, error) {
	return BookingRequestRef{}, errors.New("accommodation: this process cannot raise a reservation request")
}

// NoAuthorizations is the refusing AuthorizationPort.
type NoAuthorizations struct{}

// CreateForRequest implements AuthorizationPort.
func (NoAuthorizations) CreateForRequest(context.Context, identity.RequestContext,
	BookingAuthorizationInput,
) (BookingAuthorizationRef, error) {
	return BookingAuthorizationRef{}, errors.New("accommodation: this process cannot create an authorization")
}

// IssueVoucher implements AuthorizationPort.
func (NoAuthorizations) IssueVoucher(context.Context, pgx.Tx, identity.RequestContext,
	BookingVoucherInput,
) (IssuedBookingVoucher, error) {
	return IssuedBookingVoucher{}, errors.New("accommodation: this process cannot issue a voucher")
}

// NoPolicies is the refusing LodgingPolicyPort.
type NoPolicies struct{}

// SnapshotPolicy implements LodgingPolicyPort.
func (NoPolicies) SnapshotPolicy(context.Context, identity.RequestContext, uuid.UUID, string) (
	json.RawMessage, error,
) {
	return nil, errors.New("accommodation: this process cannot read lodging terms")
}
