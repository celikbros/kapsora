package application

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/accommodation/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// ExpiryBatchSize bounds one pass of the hold sweep per tenant. A minute's worth of expired
// holds is a handful; the bound is here so a tenant that has been unswept for a day cannot
// hold one transaction open over ten thousand rows.
const ExpiryBatchSize = 200

// ReminderBatchSize bounds one pass of the check-in reminder per tenant.
const ReminderBatchSize = 500

// ExpireHolds gives back every room whose countdown has run out, tenant by tenant.
//
// This job is the release, not a request that may never come. A member who closed the tab
// has not cancelled anything and never will; without this sweep their room would stay held
// and their nights stay reserved until somebody noticed. That is why the deadline is a
// column and this runs every minute.
//
// **Running it twice changes nothing, and neither does running it halfway.** Each booking is
// taken FOR UPDATE SKIP LOCKED, so two schedulers that both believe they lead take different
// rows and neither waits. The status write names HOLD, so a second pass over an already
// expired booking updates no row and gives nothing back a second time. The inventory is
// decremented only in the same transaction as that write, and the ledger release carries a
// key derived from the booking, so the entitlement comes back exactly once however many
// times this runs.
func (s *Service) ExpireHolds(ctx context.Context, now time.Time) (int, error) {
	tenants, err := s.activeTenants(ctx)
	if err != nil {
		return 0, err
	}
	expired := 0
	for _, tenantID := range tenants {
		count, err := s.expireTenantHolds(ctx, tenantID, now.UTC())
		if err != nil {
			return expired, err
		}
		expired += count
	}
	return expired, nil
}

func (s *Service) expireTenantHolds(ctx context.Context, tenantID uuid.UUID, now time.Time) (int, error) {
	rc := systemContext(tenantID)
	var ids []uuid.UUID
	if err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		found, err := s.bookings.ListExpiredHolds(ctx, tx, tenantID, now, ExpiryBatchSize)
		ids = found
		return err
	}); err != nil {
		return 0, fmt.Errorf("accommodation: list expired holds for tenant %s: %w", tenantID, err)
	}

	expired := 0
	for _, id := range ids {
		moved, err := s.expireOneHold(ctx, rc, id)
		if err != nil {
			return expired, fmt.Errorf("accommodation: expire hold %s: %w", id, err)
		}
		if moved {
			expired++
		}
	}
	return expired, nil
}

// expireOneHold is one booking, in one transaction: the room back, the nights back, the
// status EXPIRED. It is EXPIRED rather than CANCELLED because nobody decided it -- a member
// who was still thinking and a member who changed their mind are two different facts, and a
// cancellation fee may only ever follow the second.
func (s *Service) expireOneHold(ctx context.Context, rc identity.RequestContext, id uuid.UUID) (bool, error) {
	moved := false
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		record, err := s.bookings.LockBooking(ctx, tx, rc.TenantID, id)
		if err != nil {
			return err
		}
		if record.Status != domain.BookingHold {
			// Somebody confirmed or released it between the listing and this lock. Both
			// are ordinary; neither is this job's to undo.
			return nil
		}
		if err := s.giveBackRoom(ctx, tx, rc, record, reasonHoldExpired); err != nil {
			return err
		}
		expired, err := s.bookings.ExpireBookingRow(ctx, tx, rc.TenantID, record.ID)
		if err != nil || !expired {
			return err
		}
		moved = true
		return s.record(ctx, tx, rc, ActionBookingExpire, ResourceBooking, record.ID,
			map[string]any{"reference": record.Reference, "nights": record.Nights})
	})
	return moved, err
}

// NotifyUpcomingCheckIns tells a member the day before they arrive, in the property's own
// zone (WP-I6-04's booking.reminder, which that package left for this one to publish).
//
// The day is computed per property and not per server. A night belongs to the calendar
// hanging on the wall of the building, so a member arriving at a hotel in Berlin is reminded
// the day before the Berlin calendar says they arrive; a sweep that used one tenant-wide
// zone would be a day out for every property outside it, twice a year for the rest.
//
// It runs hourly and tells nobody twice. The deduplication key carries the booking and the
// arrival date -- facts, not the moment the sweep ran -- so the first pass of the day writes
// one message and the other twenty-three write none.
func (s *Service) NotifyUpcomingCheckIns(ctx context.Context, now time.Time) (int, error) {
	tenants, err := s.activeTenants(ctx)
	if err != nil {
		return 0, err
	}
	published := 0
	for _, tenantID := range tenants {
		count, err := s.notifyTenantCheckIns(ctx, tenantID, now.UTC())
		if err != nil {
			return published, err
		}
		published += count
	}
	return published, nil
}

func (s *Service) notifyTenantCheckIns(ctx context.Context, tenantID uuid.UUID, now time.Time) (int, error) {
	rc := systemContext(tenantID)
	count := 0
	err := s.withTx(ctx, rc, func(ctx context.Context, tx pgx.Tx) error {
		// The candidate set is every booking arriving today, tomorrow or the day after in
		// UTC. Which of those three is "tomorrow" depends on the property's own zone, and
		// the filter below is what decides it; reading only one UTC day would miss every
		// property far enough east or west that its tomorrow is not the server's.
		for offset := 0; offset <= 2; offset++ {
			day := domain.Day(now).AddDate(0, 0, offset)
			rows, err := s.bookings.ListBookingsArrivingOn(ctx, tx, tenantID, day, ReminderBatchSize)
			if err != nil {
				return err
			}
			for _, row := range rows {
				loc, err := domain.LoadLocation(row.Timezone)
				if err != nil {
					// A property whose zone this server cannot resolve is a configuration
					// fault, not a reason to stop reminding everybody else.
					s.logger.Warn("accommodation: unresolvable property timezone",
						"booking", row.BookingID, "timezone", row.Timezone)
					continue
				}
				if !domain.Day(row.CheckIn).Equal(domain.ReminderDay(now, loc)) {
					continue
				}
				if err := s.notifyBookingReminder(ctx, tx, tenantID, row); err != nil {
					return err
				}
				count++
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("accommodation: check-in reminders for tenant %s: %w", tenantID, err)
	}
	return count, nil
}

// activeTenants lists the tenants the sweeps walk. platform.tenant carries no RLS, so it is
// read outside a tenant transaction like every other cross-tenant job.
func (s *Service) activeTenants(ctx context.Context) ([]uuid.UUID, error) {
	if s.bookings == nil {
		return nil, nil
	}
	// A plain transaction and not a tenant-bound one: this read is the thing that decides
	// which tenants there are, so there is no tenant to bind it to yet. platform.tenant
	// carries no RLS, which is what makes that safe.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("accommodation: begin tenant listing: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tenants, err := s.bookings.ActiveTenants(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("accommodation: list tenants: %w", err)
	}
	return tenants, nil
}
