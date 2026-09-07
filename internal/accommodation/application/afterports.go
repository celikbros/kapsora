package application

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/identity"
)

// PermissionWaitlistManage is the grant of migration 000042: running the waiting list for
// somebody else. A member needs none of it -- they join under
// `accommodation.booking.create`, which is the grant they already hold to book for
// themselves -- and its other half is in internal/identity/application/roles.go.
const PermissionWaitlistManage = "accommodation.waitlist.manage"

// Errors of the after-the-promise half, mapped by the transport to problem codes.
var (
	// ErrCancellationTooLate refuses a cancellation of a stay the guest has already
	// started. A guest who has arrived does not cancel, they check out, and the two are
	// different facts with different arithmetic behind them.
	ErrCancellationTooLate = errors.New("accommodation: a booking that has been checked in cannot be cancelled")
	// ErrPolicySnapshotMissing is a confirmed booking with no frozen policy on it. It is a
	// refusal and never a default: judging the cancellation by today's contract is exactly
	// what the snapshot exists to prevent, and inventing a policy here would invent a fee.
	ErrPolicySnapshotMissing = errors.New("accommodation: the booking carries no frozen cancellation policy")
	// ErrCheckInWindow refuses a check-in outside the tenant's own window around the
	// booked arrival, counted on the property's clock. It carries the window, because a
	// clerk told "too early" and not "from when" has been told nothing.
	ErrCheckInWindow = errors.New("accommodation: the booking is outside its check-in window")
	// ErrVoucherRequired is a check-in with no token in the body. The token is never in the
	// URL and never in a query string, so there is nowhere else it could have been.
	ErrVoucherRequired = errors.New("accommodation: check-in needs the voucher code")
	// ErrNoShowEvidenceRequired refuses a no-show report with no clean document behind it.
	// A claim that costs a member money and rests on nothing is a claim nobody can review.
	ErrNoShowEvidenceRequired = errors.New("accommodation: a no-show report needs an evidence document")
	// ErrNoShowTooEarly refuses a report before the check-in window has closed. A guest who
	// is late is not a guest who did not come.
	ErrNoShowTooEarly = errors.New("accommodation: the check-in window has not closed yet")
	// ErrNoShowAlreadyReported is uq_no_show_booking answering: this booking already has a
	// report, decided or not. A refused report is REJECTED rather than deleted, so a second
	// one cannot quietly replace it.
	ErrNoShowAlreadyReported = errors.New("accommodation: this booking already has a no-show report")
	// ErrNoShowNotFound is a report this caller cannot see, or one that does not exist.
	ErrNoShowNotFound = errors.New("accommodation: no-show report not found")
	// ErrNoShowDecided refuses a second review of a report somebody has already decided.
	ErrNoShowDecided = errors.New("accommodation: this no-show report has already been reviewed")
	// ErrNoShowSameActor is the maker-checker rule: the person who reported that nobody
	// came may not be the person who decides it costs the member anything. It is a
	// separate refusal from a permission denial because the caller is allowed to review --
	// just not this one.
	ErrNoShowSameActor = errors.New("accommodation: the reviewer of a no-show must not be the reporter")
	// ErrWaitlistEntryNotFound is an entry this caller cannot see. An unknown id, another
	// member's entry and another provider's are deliberately indistinguishable.
	ErrWaitlistEntryNotFound = errors.New("accommodation: waitlist entry not found")
	// ErrWaitlistAlreadyWaiting is uq_waitlist_live_entry answering: this person already
	// holds a live place in this property's queue for this arrival.
	ErrWaitlistAlreadyWaiting = errors.New("accommodation: this person is already on this waiting list")
	// ErrWaitlistNotOffered refuses accepting an entry that has no offer on it.
	ErrWaitlistNotOffered = errors.New("accommodation: this waitlist entry has no live offer")
	// ErrWaitlistTransitionInvalid refuses a command the entry's status does not have.
	ErrWaitlistTransitionInvalid = errors.New("accommodation: the waitlist entry is not in a state for this command")

	// The four refusals a voucher can give at the desk, in this package's own vocabulary.
	//
	// They are restated here rather than passed through from WP-I4-02 because a transport
	// that matched on that module's sentinels would be an import from this vertical into
	// the authorization module's error surface -- and because the desk needs each one
	// separately: "this does not work" tells the person holding the code nothing they can
	// act on, and "come back tomorrow", "you already used it" and "this was cancelled" are
	// three different things to do next.
	//
	// ErrVoucherNotFound covers both an unknown code and a real code for a different
	// booking, deliberately: a refusal that could tell the two apart would be an oracle for
	// testing a stolen list.
	ErrVoucherNotFound        = errors.New("accommodation: no voucher of this booking matches that code")
	ErrVoucherAlreadyRedeemed = errors.New("accommodation: the voucher has already been redeemed")
	ErrVoucherRevoked         = errors.New("accommodation: the voucher has been withdrawn")
	ErrVoucherExpired         = errors.New("accommodation: the voucher is outside its validity window")
)

// CheckInWindowClosed is the detail behind ErrCheckInWindow: when the window opens and when
// it closes, in the property's own zone.
//
// It carries both ends because a clerk refused at ten in the morning needs to know whether
// to wait two hours or to raise a no-show, and "outside the window" answers neither.
type CheckInWindowClosed struct {
	OpensAt  time.Time
	ClosesAt time.Time
	// TimeZone is the property's own IANA zone, because a window printed in UTC to a desk
	// in Antalya is a window nobody at that desk can act on.
	TimeZone string
}

// Error implements error.
func (e *CheckInWindowClosed) Error() string { return ErrCheckInWindow.Error() }

// Is lets errors.Is reach the sentinel.
func (e *CheckInWindowClosed) Is(target error) bool { return target == ErrCheckInWindow }

// The cancellation reason codes this half writes. They are codes rather than sentences
// because a report counts them and a screen translates them.
const (
	// CancelReasonMember is the member calling off a stay that was agreed. It is the one
	// cancellation that may carry a fee.
	CancelReasonMember = "MEMBER_CANCELLED"
	// CancelReasonNoShow is the booking closed because the payer confirmed the provider's
	// report that nobody arrived.
	CancelReasonNoShow = "NO_SHOW"
)

// The ledger reason codes this half writes. Each is part of the movement's idempotency key,
// so a cancellation penalty, a no-show penalty and a check-out release of the same booking
// are three distinct movements, and any of them run twice is still one.
const (
	// ReasonCancellationPenalty is the plan paying for a room the member did not use. The
	// nights are consumed rather than released, because the payer owes the hotel for them:
	// releasing them would say the plan got its nights back, which is not what happened.
	ReasonCancellationPenalty = "CANCELLATION_PENALTY"
	// ReasonCancellationRelease gives back the nights the penalty did not take.
	ReasonCancellationRelease = "BOOKING_CANCELLED"
	// ReasonNoShowPenalty is the same movement for a confirmed no-show.
	ReasonNoShowPenalty = "NO_SHOW_PENALTY"
	// ReasonNoShowRelease gives back whatever the no-show penalty did not take.
	ReasonNoShowRelease = "NO_SHOW_RELEASED"
	// ReasonCheckOut releases the nights a stay reserved and the guest did not sleep.
	ReasonCheckOut = "BOOKING_CHECKED_OUT"
	// ReasonStayFulfilled names the consumption of the nights actually used.
	ReasonStayFulfilled = "BOOKING_STAY"
	// ReasonVoucherCancelled is written on a voucher a cancellation retired.
	ReasonVoucherCancelled = "BOOKING_CANCELLED"
)

// The no-show statuses (migration 000042).
const (
	NoShowReported  = "REPORTED"
	NoShowConfirmed = "CONFIRMED"
	NoShowDisputed  = "DISPUTED"
	NoShowRejected  = "REJECTED"
)

// NoShowDecisions is the closed list a review may write.
var NoShowDecisions = []string{NoShowConfirmed, NoShowDisputed, NoShowRejected}

// The waitlist statuses (migration 000042).
const (
	WaitlistWaiting   = "WAITING"
	WaitlistOffered   = "OFFERED"
	WaitlistAccepted  = "ACCEPTED"
	WaitlistExpired   = "EXPIRED"
	WaitlistCancelled = "CANCELLED"
)

// WaitlistStatuses is the closed list, in the order of the CHECK.
var WaitlistStatuses = []string{
	WaitlistWaiting, WaitlistOffered, WaitlistAccepted, WaitlistExpired, WaitlistCancelled,
}

// NoShowReviewQueueCode is the payer-side queue a disputed no-show is raised into. It is the
// same queue WP-I4-03 configures for reservation review, because a disputed no-show is a
// reservation question and a queue nobody was told about is a queue nobody watches.
const NoShowReviewQueueCode = "RESERVATION_REVIEW"

// CancellationRecord is one accommodation.cancellation row. Every amount is the exact
// decimal text the numeric column holds.
type CancellationRecord struct {
	ID             uuid.UUID
	BookingID      uuid.UUID
	CancelledAt    time.Time
	CancelledBy    *uuid.UUID
	ReasonCode     string
	PolicySnapshot json.RawMessage
	Free           bool
	PenaltyNights  int
	ReleasedNights int
	FeeAmount      string
	PayerFee       string
	MemberFee      string
	CurrencyCode   string
	CreatedAt      time.Time
}

// NewCancellationRow is a cancellation as it is written. The policy is a copy of the
// booking's own frozen one, so the row proves what it was judged by.
type NewCancellationRow struct {
	BookingID      uuid.UUID
	CancelledAt    time.Time
	CancelledBy    uuid.UUID
	ReasonCode     string
	PolicySnapshot json.RawMessage
	Free           bool
	PenaltyNights  int
	ReleasedNights int
	FeeAmount      string
	PayerFee       string
	MemberFee      string
	CurrencyCode   string
}

// NoShowRecord is one accommodation.no_show row.
type NoShowRecord struct {
	ID                 uuid.UUID
	BookingID          uuid.UUID
	ReportedByActorID  *uuid.UUID
	ReportedAt         time.Time
	EvidenceDocumentID *uuid.UUID
	AssessedFeeAmount  string
	PayerAmount        string
	MemberAmount       string
	CurrencyCode       string
	Status             string
	ReviewedBy         *uuid.UUID
	ReviewedAt         *time.Time
	ReviewComment      *string
	ConsumedNights     int
	CreatedAt          time.Time
	UpdatedAt          time.Time
	RowVersion         int64
}

// NewNoShowRow is a report as it is written.
type NewNoShowRow struct {
	BookingID          uuid.UUID
	ReportedByActorID  uuid.UUID
	ReportedAt         time.Time
	EvidenceDocumentID *uuid.UUID
	AssessedFeeAmount  string
	PayerAmount        string
	MemberAmount       string
	CurrencyCode       string
	ActorID            uuid.UUID
}

// NoShowReviewRow is the payer's answer as it is written.
type NoShowReviewRow struct {
	ID             uuid.UUID
	Status         string
	ReviewedBy     uuid.UUID
	ReviewedAt     time.Time
	ReviewComment  *string
	ConsumedNights int
}

// WaitlistRecord is one accommodation.waitlist_entry row.
type WaitlistRecord struct {
	ID               uuid.UUID
	PersonID         uuid.UUID
	EnrollmentID     uuid.UUID
	PropertyID       uuid.UUID
	RoomTypeID       *uuid.UUID
	CheckIn          time.Time
	CheckOut         time.Time
	Adults           int
	Children         int
	Priority         int
	Status           string
	OfferedBookingID *uuid.UUID
	OfferExpiresAt   *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
	RowVersion       int64
}

// Nights is how long the waited-for stay is.
func (w WaitlistRecord) Nights() int {
	return int(w.CheckOut.Sub(w.CheckIn).Hours() / 24)
}

// NewWaitlistRow is an entry as it is written.
type NewWaitlistRow struct {
	PersonID     uuid.UUID
	EnrollmentID uuid.UUID
	PropertyID   uuid.UUID
	RoomTypeID   *uuid.UUID
	CheckIn      time.Time
	CheckOut     time.Time
	Adults       int
	Children     int
	Priority     int
	// CreatedAt is the second key of the queue order, written by the service rather than
	// defaulted by the database, so an entry joining and an entry requeued behind it are
	// stamped by the same clock.
	CreatedAt time.Time
	ActorID   uuid.UUID
}

// WaitlistQuery is the repository-level filter. `PersonID` is the member boundary and
// `ScopeIDs` the provider one; both are decided by the caller rather than inherited.
type WaitlistQuery struct {
	ScopeIDs   []uuid.UUID
	PersonID   *uuid.UUID
	PropertyID *uuid.UUID
	Status     string
	PageSize   int
}

// BookingReleaseInput gives back nights an authorization reserved and nobody used.
type BookingReleaseInput struct {
	TenantID        uuid.UUID
	ActorID         uuid.UUID
	AuthorizationID uuid.UUID
	// Nights is a ceiling and not an amount: the port releases what is still outstanding up
	// to this, so a hold something else has already drawn on is not released twice.
	Nights string
	// ReasonCode names the movement in the ledger and is part of its idempotency key, so a
	// cancellation and a check-out of the same booking are two movements and either of them
	// run twice is still one.
	ReasonCode string
}

// BookingConsumeInput spends nights the plan paid for and the member did not sleep: a
// cancellation penalty, or a confirmed no-show.
type BookingConsumeInput struct {
	TenantID            uuid.UUID
	ActorID             uuid.UUID
	AuthorizationID     uuid.UUID
	ServiceDefinitionID uuid.UUID
	Nights              string
	// Key is derived from the booking rather than from a clock, so a replayed command
	// consumes once.
	Key        string
	ReasonCode string
}

// BookingFulfilmentInput records what a stay actually delivered and consumes it, in one act.
type BookingFulfilmentInput struct {
	AuthorizationID     uuid.UUID
	ServiceDefinitionID uuid.UUID
	Nights              string
	PerformedAt         time.Time
	ProviderProfileID   *uuid.UUID
}

// BookingRedeemInput is the voucher a guest presents at the desk.
type BookingRedeemInput struct {
	// Token is the plaintext, and it lives in this struct, in the request body it came
	// from, and nowhere else: not in a column, not in a log line, not in an audit row and
	// not in a URL.
	Token string
	// AuthorizationID is the promise the booking stands on. A token for a different one is
	// not found, never "wrong booking".
	AuthorizationID uuid.UUID
	RedeemedAt      time.Time
}

// BookingAuthorizationLine is one approved line of the hold a booking stands on: what was
// promised and what is still outstanding.
//
// It is read rather than assumed because the two can differ. The booking asked for the
// nights its frozen quote said the plan carries; a reviewer may have approved fewer; and a
// check-out that released `booked - stayed` rather than `approved - stayed` would hand back
// entitlement that was never held.
type BookingAuthorizationLine struct {
	ID                  uuid.UUID
	ServiceDefinitionID uuid.UUID
	Approved            string
	Remaining           string
}

// RaiseWorkItem is one piece of work for a person to look at.
type RaiseWorkItem struct {
	QueueCode     string
	AggregateType string
	AggregateID   uuid.UUID
	Title         string
	ActorID       *uuid.UUID
}

// WorkItemPort raises the work a disputed no-show is. It is a port rather than a call into
// the worklist service because the item has to be written in the review's own transaction: a
// dispute recorded with no item raised, or an item raised for a review that rolled back, is
// work nobody can explain.
//
// The title it is given carries the booking's reference and nothing else. A queue is a list
// people read across a room, and a member's name on it is a name on a wall.
type WorkItemPort interface {
	Raise(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, in RaiseWorkItem) error
}

// NoWorkItems is the default WorkItemPort: it raises nothing. A deployment that has not
// configured a review queue is a deployment where nobody is watching, and refusing the
// review would not make anybody watch.
type NoWorkItems struct{}

// Raise implements WorkItemPort.
func (NoWorkItems) Raise(context.Context, pgx.Tx, uuid.UUID, RaiseWorkItem) error { return nil }

// systemActor is the caller the offer sweep acts as when it holds a room on somebody's
// behalf: the tenant, the person the entry names, and nobody in particular as an actor.
// There is no actor because there is no person at a keyboard, and attributing the hold to
// the member would say they did something they have not done yet.
func systemActor(tenantID uuid.UUID) identity.RequestContext {
	return identity.RequestContext{TenantID: tenantID}
}
