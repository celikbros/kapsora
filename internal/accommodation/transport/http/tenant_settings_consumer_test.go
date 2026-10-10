package accommodationhttp_test

import (
	"testing"
	"time"

	"github.com/celikbros/kapsora/internal/accommodation/application"
)

// These are consumer tests: a setting row is written as the management command would
// write it, then the real booking service reads it on a later transaction.
func setAccommodationMinute(t *testing.T, s *server, key string, value int) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	_, err := s.h.Admin.Exec(ctx, `INSERT INTO platform.tenant_setting(tenant_id,setting_key,value_json)
		VALUES($1,$2,to_jsonb($3::int))
		ON CONFLICT(tenant_id,setting_key) DO UPDATE SET value_json=EXCLUDED.value_json`,
		s.tenant, key, value)
	if err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
}

func TestManagedHoldMinutesAffectsNewHoldsButNotPersistedExpiry(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.grantNights(t, 20)
	s.putLodgingTermsWithHold(t, nil)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	first, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
	if err != nil {
		t.Fatalf("first hold: %v", err)
	}
	before := *first.Booking.HoldExpiresAt
	setAccommodationMinute(t, s, "accommodation.hold_minutes", 40)
	var oldBooking, oldReservation time.Time
	if err := s.h.Admin.QueryRow(ctx, `SELECT b.hold_expires_at,r.expires_at
		FROM accommodation.booking b JOIN benefit.entitlement_reservation r
		ON r.tenant_id=b.tenant_id AND r.id=b.entitlement_reservation_id
		WHERE b.tenant_id=$1 AND b.id=$2`, s.tenant, first.Booking.ID).Scan(&oldBooking, &oldReservation); err != nil {
		t.Fatalf("read original expiries: %v", err)
	}
	if !oldBooking.Equal(before) || !oldReservation.Equal(before) {
		t.Fatalf("existing booking/reservation expiry changed: %v/%v, want %v", oldBooking, oldReservation, before)
	}
	if _, err := s.svc.ReleaseHold(ctx, s.memberContext(), first.Booking.ID); err != nil {
		t.Fatalf("clear first hold to permit another booking for the same member: %v", err)
	}
	second, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
	if err != nil {
		t.Fatalf("second hold: %v", err)
	}
	if second.Booking.HoldExpiresAt == nil || !second.Booking.HoldExpiresAt.Equal(s.clock.Now().Add(40*time.Minute)) {
		t.Fatalf("new hold expiry = %v, want configured 40 minutes", second.Booking.HoldExpiresAt)
	}
}

func TestManagedHoldMinutesKeepsContractOverride(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.grantNights(t, 10)
	s.putLodgingTermsWithHold(t, int32Ptr(30))
	setAccommodationMinute(t, s, "accommodation.hold_minutes", 40)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	view, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if view.Booking.HoldExpiresAt == nil || !view.Booking.HoldExpiresAt.Equal(s.clock.Now().Add(30*time.Minute)) {
		t.Fatalf("contract expiry = %v, want 30-minute override", view.Booking.HoldExpiresAt)
	}
}

func TestManagedQuoteTTLControlsPendingConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ttl   int
		stale bool
	}{{"short TTL refuses", 15, true}, {"long TTL permits", 60, false}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t)
			s.grantNights(t, 10)
			s.putLodgingTerms(t)
			ctx, cancel := s.h.Ctx()
			defer cancel()
			view, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
			if err != nil {
				t.Fatalf("hold: %v", err)
			}
			_, err = s.h.Admin.Exec(ctx, `UPDATE accommodation.booking
				SET quote_snapshot=jsonb_set(quote_snapshot,'{quotedAt}',
				to_jsonb(to_char((now() AT TIME ZONE 'UTC')-interval '30 minutes',
				'YYYY-MM-DD"T"HH24:MI:SS"Z"')))
				WHERE tenant_id=$1 AND id=$2`, s.tenant, view.Booking.ID)
			if err != nil {
				t.Fatalf("age quote: %v", err)
			}
			setAccommodationMinute(t, s, "accommodation.quote_ttl_minutes", tc.ttl)
			_, err = s.svc.ConfirmBooking(ctx, s.memberContext(), view.Booking.ID)
			if tc.stale && !isError(err, application.ErrQuoteStale) {
				t.Fatalf("confirmation with %d-minute TTL = %v, want stale refusal", tc.ttl, err)
			}
			if !tc.stale && err != nil {
				t.Fatalf("confirmation with %d-minute TTL: %v", tc.ttl, err)
			}
		})
	}
}
