package domain

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"time"
)

// Booking statuses (v1.2 12.3, migration 000041). The order is the order of the CHECK.
const (
	// BookingHold is a room set aside with a countdown running. Nothing has been agreed
	// and the room comes back on its own if nobody confirms.
	BookingHold = "HOLD"
	// BookingPendingApproval is a confirmation a reviewer has not decided yet. The room is
	// still held; only the deadline has moved out to the request's own SLA.
	BookingPendingApproval = "PENDING_APPROVAL"
	BookingConfirmed       = "CONFIRMED"
	BookingCheckedIn       = "CHECKED_IN"
	BookingCompleted       = "COMPLETED"
	BookingCancelled       = "CANCELLED"
	BookingNoShow          = "NO_SHOW"
	// BookingExpired is a hold nobody confirmed in time. It is distinct from CANCELLED
	// because nobody decided it: a member who was still thinking and a member who changed
	// their mind are two different facts, and a cancellation fee may only ever follow the
	// second.
	BookingExpired = "EXPIRED"
)

// BookingStatuses is the closed list, in the order of the CHECK.
var BookingStatuses = []string{
	BookingHold, BookingPendingApproval, BookingConfirmed, BookingCheckedIn,
	BookingCompleted, BookingCancelled, BookingNoShow, BookingExpired,
}

// LiveBookingStatuses is the predicate of uq_booking_live_per_person_arrival: the statuses
// in which a booking still claims a room. It is spelled here as well as in the index
// because the service tells a member *why* their second attempt was refused, and the index
// tells them *that* it was; the two must not drift apart.
var LiveBookingStatuses = []string{
	BookingHold, BookingPendingApproval, BookingConfirmed, BookingCheckedIn,
}

// HeldBookingStatuses are the statuses whose rooms are counted in `inventory_day.held`
// rather than in `confirmed`. Releasing a booking decrements `held` for exactly these.
var HeldBookingStatuses = []string{BookingHold, BookingPendingApproval}

// Guest types (migration 000041).
const (
	GuestMember    = "MEMBER"
	GuestDependant = "DEPENDANT"
	GuestOther     = "GUEST"
)

// GuestTypes is the closed list.
var GuestTypes = []string{GuestMember, GuestDependant, GuestOther}

// Channels a booking may be taken through; the same closed list service.service_request
// uses, because a booking raises one.
const (
	ChannelBackoffice     = "BACKOFFICE"
	ChannelProviderPortal = "PROVIDER_PORTAL"
	ChannelMemberPortal   = "MEMBER_PORTAL"
	ChannelAPI            = "API"
	ChannelBatchImport    = "BATCH_IMPORT"
	ChannelCallCenter     = "CALL_CENTER"
)

// Channels is the closed list.
var Channels = []string{
	ChannelBackoffice, ChannelProviderPortal, ChannelMemberPortal,
	ChannelAPI, ChannelBatchImport, ChannelCallCenter,
}

// Cancellation reason codes this package writes itself. They are codes rather than
// sentences because a report counts them and a screen translates them, and a sentence is
// neither countable nor translatable.
const (
	// CancelReasonHoldReleased is the member giving the room back before the countdown ran
	// out. It is not a cancellation of an agreed stay and carries no fee.
	CancelReasonHoldReleased = "HOLD_RELEASED"
	// CancelReasonRequestRejected is the reservation request refused by the gate or by a
	// reviewer. Nothing was ever agreed, so nothing is charged.
	CancelReasonRequestRejected = "REQUEST_REJECTED"
)

// referenceEncoding is base32 over uppercase letters and digits only, so a reference can be
// read down a telephone without anybody having to say "lowercase l" or "the digit one".
var referenceEncoding = base32.NewEncoding("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567").WithPadding(base32.NoPadding)

// NewBookingReference mints `BK-YYYYMMDD-XXXXXXXX`, the shape ck_booking_reference
// enforces. The date is the day the booking was taken and the tail is forty random bits;
// the pair is unique per tenant and the caller retries the vanishingly rare collision
// rather than reporting it.
func NewBookingReference(now time.Time) (string, error) {
	buf := make([]byte, 5)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("accommodation: generate booking reference: %w", err)
	}
	return fmt.Sprintf("BK-%s-%s", now.UTC().Format("20060102"),
		referenceEncoding.EncodeToString(buf)), nil
}

// ReferenceAttempts is how many times a create retries a reference collision. The tail is
// forty random bits, so two attempts is already generous; the loop exists so a collision is
// a retry rather than an error somebody has to read.
const ReferenceAttempts = 5

// OccupancyFits reports whether a party fits the room type. It is three separate
// comparisons rather than one on the total, because a room that sleeps two adults and one
// child does not sleep three adults, and a check on the total alone would sell it to them.
func OccupancyFits(adults, children, maxAdults, maxChildren, maxOccupancy int) bool {
	return adults <= maxAdults && children <= maxChildren && adults+children <= maxOccupancy
}

// ReminderDay is the civil date whose bookings the day-before reminder covers, given the
// moment the sweep runs and the property's own zone.
//
// It is "tomorrow in the property's zone" and not "now plus twenty-four hours", for the
// reason every date in this package is civil: a member checking into a hotel in Berlin is
// reminded the day before the Berlin calendar says they arrive, whatever the server's
// clock is set to and whichever side of a clock change the night falls on.
func ReminderDay(now time.Time, loc *time.Location) time.Time {
	return DayIn(now, loc).AddDate(0, 0, 1)
}
