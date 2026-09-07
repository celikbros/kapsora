package scheduler

import (
	"context"
	"time"

	accommodationapp "github.com/celikbros/kapsora/internal/accommodation/application"
)

// AccommodationHoldExpire gives back every room whose fifteen minutes have run out
// (WP-I6-02 section 2.2).
//
// This job is the release, not a request that may never come. A member who closed the tab
// has not cancelled anything and never will; without this sweep their room would stay held
// and their nights stay reserved until somebody noticed. That is why the hold's deadline is
// a column and this runs every minute.
//
// It is idempotent under any number of runs, and under two schedulers that both believe
// they lead: each booking is taken FOR UPDATE SKIP LOCKED, the status write names HOLD, and
// the entitlement release carries a key derived from the booking. A second pass finds
// nothing to do rather than giving a room back twice.
func AccommodationHoldExpire(svc *accommodationapp.Service) Job {
	return Job{
		Code:  "accommodation.hold_expire",
		Every: time.Minute,
		Run: func(ctx context.Context) (Metrics, error) {
			expired, err := svc.ExpireHolds(ctx, time.Now().UTC())
			return Metrics{"expired": expired}, err
		},
	}
}

// AccommodationBookingReminder tells a member the day before they arrive (WP-I6-04's
// booking.reminder, which that package left for WP-I6-02 to publish).
//
// It runs hourly and tells nobody twice. The day is computed in each property's own zone,
// because a night belongs to the calendar hanging on the wall of that building; the
// deduplication key carries the booking and its arrival date rather than the moment the
// sweep ran, so the first pass of the day writes one message and the other twenty-three
// write none.
func AccommodationBookingReminder(svc *accommodationapp.Service) Job {
	return Job{
		Code:  "accommodation.booking_reminder",
		Every: time.Hour,
		Run: func(ctx context.Context) (Metrics, error) {
			published, err := svc.NotifyUpcomingCheckIns(ctx, time.Now().UTC())
			return Metrics{"published": published}, err
		},
	}
}
