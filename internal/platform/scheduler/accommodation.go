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

// AccommodationWaitlistOffer hands a freed room to whoever is at the front of the queue
// (WP-I6-03 section 2.5).
//
// Every five minutes, and on nothing else. No command triggers it and no release calls it,
// because a sweep that some paths ran and others did not would be a queue that worked for
// cancellations and quietly did not for expiries -- and a member on a waiting list would
// never learn which kind of freed room theirs was.
//
// Two properties make it safe on more than one scheduler. The queue is read FOR UPDATE SKIP
// LOCKED, so two sweeps that both believe they lead take different entries and neither
// waits. And the offer is placed by WP-I6-02's own hold, with the same stay-date lock order
// and the same counters, so a room offered to a waiting member and a room taken at the
// search screen cannot oversell each other.
func AccommodationWaitlistOffer(svc *accommodationapp.Service) Job {
	return Job{
		Code:  "accommodation.waitlist_offer",
		Every: 5 * time.Minute,
		Run: func(ctx context.Context) (Metrics, error) {
			offered, err := svc.OfferWaitlistRooms(ctx, time.Now().UTC())
			return Metrics{"offered": offered}, err
		},
	}
}
