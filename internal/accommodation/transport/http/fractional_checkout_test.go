package accommodationhttp_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/accommodation/application"
	accommodationdomain "github.com/celikbros/kapsora/internal/accommodation/domain"
	servicerequestapp "github.com/celikbros/kapsora/internal/servicerequest/application"
	servicerequestdomain "github.com/celikbros/kapsora/internal/servicerequest/domain"
)

// fractionalCheckoutBooking uses the real reviewer decision and outbox adoption path.
// The balance is small enough that every returned fractional unit is visible on the
// selected account and original reservation.
func (s *server) fractionalCheckoutBooking(t *testing.T, balance, factor, approved string) application.BookingView {
	t.Helper()
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.putLodgingTerms(t)
	s.replaceNightPlan(t, balance, factor)
	hold, snapshot := s.conversionHold(t, "2026-06-17")
	if snapshot.CoveredNights != 2 || hold.Booking.EntitlementReservationID == nil {
		t.Fatalf("fractional prerequisite hold: %+v %+v", hold.Booking, snapshot)
	}
	_, units := s.conversionReservation(t, hold.Booking.ID)
	var same bool
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if err := s.h.Admin.QueryRow(ctx, `SELECT $1::text::numeric=2*$2::text::numeric`, units, factor).Scan(&same); err != nil || !same {
		t.Fatalf("fractional prerequisite reservation quantity %s, want 2×%s: %v", units, factor, err)
	}
	confirmed, err := s.svc.ConfirmBooking(ctx, s.memberContext(), hold.Booking.ID)
	if err != nil || confirmed.Booking.ServiceRequestID == nil {
		t.Fatalf("fractional prerequisite confirmation: %+v %v", confirmed.Booking, err)
	}
	requestID := *confirmed.Booking.ServiceRequestID
	request, err := s.requests.Get(ctx, s.deskContext(), requestID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.requests.PartiallyApprove(ctx, s.deskContext(), requestID, servicerequestapp.DecisionInput{
		ReasonCode: "FRACTIONAL_LIMIT", ExpectedVersion: request.Request.RowVersion,
		Items: []servicerequestdomain.DecisionItem{{LineNo: 1, Status: servicerequestdomain.ItemPartiallyApproved,
			ApprovedQuantity: approved}},
	}); err != nil {
		t.Fatalf("fractional review: %v", err)
	}
	s.deliverDecision(t, requestID)
	final, err := s.svc.GetBooking(ctx, s.deskContext(), hold.Booking.ID)
	if err != nil || final.Booking.Status != accommodationdomain.BookingConfirmed ||
		final.Booking.AuthorizationID == nil || final.Booking.EntitlementReservationID == nil ||
		*final.Booking.EntitlementReservationID != *hold.Booking.EntitlementReservationID {
		t.Fatalf("fractional prerequisite adoption: %+v %v", final.Booking, err)
	}
	var storedApproved, storedFactor string
	var storedReservation uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `SELECT approved_quantity::text,
		entitlement_unit_factor::text,entitlement_reservation_id
		FROM service.authorization_item WHERE tenant_id=$1 AND authorization_id=$2`,
		s.tenant, *final.Booking.AuthorizationID).
		Scan(&storedApproved, &storedFactor, &storedReservation); err != nil {
		t.Fatal(err)
	}
	var matches bool
	if err := s.h.Admin.QueryRow(ctx, `SELECT $1::text::numeric=$2::text::numeric
		AND $3::text::numeric=$4::text::numeric`, storedApproved, approved, storedFactor, factor).Scan(&matches); err != nil {
		t.Fatal(err)
	}
	if !matches || storedReservation != *hold.Booking.EntitlementReservationID {
		t.Fatalf("fractional adoption approved/factor/reservation=%s/%s/%s, want %s/%s/%s",
			storedApproved, storedFactor, storedReservation, approved, factor, *hold.Booking.EntitlementReservationID)
	}
	return final
}

func (s *server) fractionalAccountState(t *testing.T) (available, reserved, consumed string) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var conserved bool
	if err := s.h.Admin.QueryRow(ctx, `SELECT available_quantity::text,reserved_quantity::text,
		consumed_quantity::text,total_granted=available_quantity+reserved_quantity+
		consumed_quantity+expired_quantity FROM benefit.entitlement_account
		WHERE tenant_id=$1 AND id=$2`, s.tenant, s.account).
		Scan(&available, &reserved, &consumed, &conserved); err != nil {
		t.Fatal(err)
	}
	if !conserved {
		t.Fatal("FRACTIONAL_CHECKOUT_CONSERVATION: account counters do not conserve")
	}
	return
}

func (s *server) fractionalMovements(t *testing.T, reservationID uuid.UUID, reason string) (count int, units string) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*),coalesce(sum(delta_available),0)::text
		FROM benefit.entitlement_ledger WHERE tenant_id=$1 AND reservation_id=$2
		AND movement_type='RELEASE' AND reason_code=$3`, s.tenant, reservationID, reason).
		Scan(&count, &units); err != nil {
		t.Fatal(err)
	}
	return
}

func (s *server) fractionalTerminalEvidence(t *testing.T, booking application.BookingView,
	wantConsumed string, wantFulfilments int,
) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var remaining, itemConsumed string
	var fulfilments, consumeMovements int
	if err := s.h.Admin.QueryRow(ctx, `SELECT (quantity-consumed_quantity-released_quantity)::text
		FROM benefit.entitlement_reservation WHERE tenant_id=$1 AND id=$2`,
		s.tenant, *booking.Booking.EntitlementReservationID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT consumed_quantity::text FROM service.authorization_item
		WHERE tenant_id=$1 AND authorization_id=$2`, s.tenant, *booking.Booking.AuthorizationID).
		Scan(&itemConsumed); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM service.fulfilment
		WHERE tenant_id=$1 AND authorization_id=$2`, s.tenant, *booking.Booking.AuthorizationID).
		Scan(&fulfilments); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM benefit.entitlement_ledger
		WHERE tenant_id=$1 AND reservation_id=$2 AND movement_type='CONSUME'`,
		s.tenant, *booking.Booking.EntitlementReservationID).Scan(&consumeMovements); err != nil {
		t.Fatal(err)
	}
	if remaining != "0.000000" || itemConsumed != wantConsumed ||
		fulfilments != wantFulfilments || consumeMovements != wantFulfilments {
		t.Fatalf("FRACTIONAL_CHECKOUT_TERMINAL_EVIDENCE: reservation remaining=%s item consumed=%s fulfilments=%d CONSUME=%d; want 0/%s/%d/%d",
			remaining, itemConsumed, fulfilments, consumeMovements, wantConsumed,
			wantFulfilments, wantFulfilments)
	}
}

func TestFractionalCheckoutReleasesExactUnusedAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name, balance, factor, approved                                string
		beforeAvailable, beforeReserved, afterAvailable, afterConsumed string
		checkoutRelease, surplusRelease                                string
		used                                                           int
	}{
		{"half-service-double-factor", "4", "2", "0.5", "3.000000", "1.000000", "4.000000", "0.000000", "1.000000", "3.000000", 0},
		{"one-and-half-double-factor", "4", "2", "1.5", "1.000000", "3.000000", "2.000000", "2.000000", "1.000000", "1.000000", 1},
		{"one-and-half-half-factor", "1.5", "0.5", "1.5", "0.750000", "0.750000", "1.000000", "0.500000", "0.250000", "0.250000", 1},
		{"stored-factor-one", "2", "1", "1.5", "0.500000", "1.500000", "1.000000", "1.000000", "0.500000", "0.500000", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t)
			booking := s.fractionalCheckoutBooking(t, tc.balance, tc.factor, tc.approved)
			reservationID := *booking.Booking.EntitlementReservationID
			available, reserved, consumed := s.fractionalAccountState(t)
			if available != tc.beforeAvailable || reserved != tc.beforeReserved || consumed != "0.000000" {
				t.Fatalf("fractional adoption account=%s/%s/%s, want %s/%s/0", available, reserved,
					consumed, tc.beforeAvailable, tc.beforeReserved)
			}
			if count, units := s.fractionalMovements(t, reservationID,
				"BOOKING_UNAPPROVED"); count != 1 || units != tc.surplusRelease {
				t.Fatalf("fractional adoption surplus release=%d/%s, want 1/%s", count, units, tc.surplusRelease)
			}
			ctx, cancel := s.h.Ctx()
			defer cancel()
			issued, err := s.svc.IssueBookingVoucher(ctx, s.memberContext(), booking.Booking.ID)
			if err != nil {
				t.Fatal(err)
			}
			s.clock.At(t, atCheckIn)
			if _, err := s.svc.CheckInBooking(ctx, s.deskContext(), booking.Booking.ID,
				application.CheckInInput{Token: issued.Token}); err != nil {
				t.Fatal(err)
			}
			s.clock.At(t, afterOneNight)
			out, err := s.svc.CheckOutBooking(ctx, s.deskContext(), booking.Booking.ID, application.CheckOutInput{})
			if err != nil {
				t.Fatalf("fractional checkout: %v", err)
			}
			if out.Booking.Status != accommodationdomain.BookingCompleted || out.Booking.ActualNights == nil ||
				*out.Booking.ActualNights != 1 || out.Booking.OverBooking {
				t.Fatalf("fractional checkout whole-night/overstay result: %+v", out.Booking)
			}
			available, reserved, consumed = s.fractionalAccountState(t)
			if available != tc.afterAvailable || consumed != tc.afterConsumed || reserved != "0.000000" {
				t.Fatalf("FRACTIONAL_CHECKOUT_RETAINED_RESERVED: terminal account=%s/%s/%s, want %s/0/%s",
					available, reserved, consumed, tc.afterAvailable, tc.afterConsumed)
			}
			if count, units := s.fractionalMovements(t, reservationID,
				application.ReasonCheckOut); count != 1 || units != tc.checkoutRelease {
				t.Fatalf("FRACTIONAL_CHECKOUT_RELEASE: checkout release=%d/%s, want 1/%s",
					count, units, tc.checkoutRelease)
			}
			s.fractionalTerminalEvidence(t, booking, fmt.Sprintf("%d.000000", tc.used), tc.used)
			if _, err := s.svc.CheckOutBooking(ctx, s.deskContext(), booking.Booking.ID,
				application.CheckOutInput{}); err == nil {
				t.Fatal("completed checkout unexpectedly repeated")
			}
			if count, units := s.fractionalMovements(t, reservationID,
				application.ReasonCheckOut); count != 1 || units != tc.checkoutRelease {
				t.Fatalf("fractional checkout replay release=%d/%s, want 1/%s", count, units, tc.checkoutRelease)
			}
		})
	}
}

func TestFractionalCheckoutLegacyV2FactorOneUsesStoredAuthorization(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.putLodgingTerms(t)
	s.replaceNightPlan(t, "2", "1")
	hold, _ := s.conversionHold(t, "2026-06-17")
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if _, err := s.h.Admin.Exec(ctx, `UPDATE accommodation.booking SET quote_snapshot=
		(quote_snapshot - 'nightConversion') || '{"version":2}'::jsonb
		WHERE tenant_id=$1 AND id=$2`, s.tenant, hold.Booking.ID); err != nil {
		t.Fatal(err)
	}
	// The original evaluation still proves the mapping after its immutable version
	// retires. Checkout must use the factor on the authorization, not today's plan.
	if _, err := s.h.Admin.Exec(ctx, `UPDATE benefit.plan_version
		SET status='RETIRED',retire_reason_code='TEST'
		WHERE tenant_id=$1 AND id=$2`, s.tenant, s.planVersion); err != nil {
		t.Fatal(err)
	}
	confirmed, err := s.svc.ConfirmBooking(ctx, s.memberContext(), hold.Booking.ID)
	if err != nil || confirmed.Booking.ServiceRequestID == nil {
		t.Fatalf("legacy fractional confirmation: %+v %v", confirmed.Booking, err)
	}
	requestID := *confirmed.Booking.ServiceRequestID
	request, err := s.requests.Get(ctx, s.deskContext(), requestID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.requests.PartiallyApprove(ctx, s.deskContext(), requestID, servicerequestapp.DecisionInput{
		ReasonCode: "FRACTIONAL_LIMIT", ExpectedVersion: request.Request.RowVersion,
		Items: []servicerequestdomain.DecisionItem{{LineNo: 1,
			Status: servicerequestdomain.ItemPartiallyApproved, ApprovedQuantity: "1.5"}},
	}); err != nil {
		t.Fatalf("legacy fractional review: %v", err)
	}
	s.deliverDecision(t, requestID)
	booking, err := s.svc.GetBooking(ctx, s.deskContext(), hold.Booking.ID)
	if err != nil || booking.Booking.Status != accommodationdomain.BookingConfirmed ||
		booking.Booking.AuthorizationID == nil || booking.Booking.EntitlementReservationID == nil {
		t.Fatalf("legacy fractional adoption: %+v %v", booking.Booking, err)
	}
	var version int
	var factor, approved string
	if err := s.h.Admin.QueryRow(ctx, `SELECT (b.quote_snapshot->>'version')::int,
		ai.entitlement_unit_factor::text,ai.approved_quantity::text
		FROM accommodation.booking b JOIN service.authorization_item ai
		ON ai.tenant_id=b.tenant_id AND ai.authorization_id=b.authorization_id
		WHERE b.tenant_id=$1 AND b.id=$2`, s.tenant, hold.Booking.ID).
		Scan(&version, &factor, &approved); err != nil {
		t.Fatal(err)
	}
	if version != 2 || factor != "1.000000" || approved != "1.500000" {
		t.Fatalf("legacy fractional persisted quote/factor/approval=%d/%s/%s, want 2/1/1.5",
			version, factor, approved)
	}
	issued, err := s.svc.IssueBookingVoucher(ctx, s.memberContext(), hold.Booking.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.clock.At(t, atCheckIn)
	if _, err := s.svc.CheckInBooking(ctx, s.deskContext(), hold.Booking.ID,
		application.CheckInInput{Token: issued.Token}); err != nil {
		t.Fatal(err)
	}
	s.clock.At(t, afterOneNight)
	if _, err := s.svc.CheckOutBooking(ctx, s.deskContext(), hold.Booking.ID,
		application.CheckOutInput{}); err != nil {
		t.Fatalf("legacy fractional checkout: %v", err)
	}
	available, reserved, consumed := s.fractionalAccountState(t)
	if available != "1.000000" || reserved != "0.000000" || consumed != "1.000000" {
		t.Fatalf("FRACTIONAL_CHECKOUT_LEGACY_RETAINED_RESERVED: account=%s/%s/%s, want 1/0/1",
			available, reserved, consumed)
	}
	if count, units := s.fractionalMovements(t, *booking.Booking.EntitlementReservationID,
		application.ReasonCheckOut); count != 1 || units != "0.500000" {
		t.Fatalf("legacy fractional checkout RELEASE=%d/%s, want 1/0.5", count, units)
	}
	s.fractionalTerminalEvidence(t, booking, "1.000000", 1)
}

type failCheckoutRowOnce struct {
	application.BookingRepository
	failed bool
}

func (b *failCheckoutRowOnce) CheckOutBookingRow(ctx context.Context, tx pgx.Tx,
	tenantID, id uuid.UUID, at time.Time, actual int, over bool, actorID uuid.UUID,
) (bool, error) {
	if !b.failed {
		b.failed = true
		return false, errors.New("injected checkout status failure")
	}
	return b.BookingRepository.CheckOutBookingRow(ctx, tx, tenantID, id, at, actual, over, actorID)
}

func TestFractionalCheckoutStatusFailureRollsBackAndRetrySettles(t *testing.T) {
	s := newServer(t)
	booking := s.fractionalCheckoutBooking(t, "4", "2", "1.5")
	ctx, cancel := s.h.Ctx()
	defer cancel()
	issued, err := s.svc.IssueBookingVoucher(ctx, s.memberContext(), booking.Booking.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.clock.At(t, atCheckIn)
	if _, err := s.svc.CheckInBooking(ctx, s.deskContext(), booking.Booking.ID,
		application.CheckInInput{Token: issued.Token}); err != nil {
		t.Fatal(err)
	}
	_, _, confirmedBefore := s.inventoryOf(t, s.roomType, "2026-06-16")
	if confirmedBefore != 1 {
		t.Fatalf("rollback prerequisite second-night confirmed=%d, want 1", confirmedBefore)
	}
	fault := &failCheckoutRowOnce{BookingRepository: s.deps.Bookings}
	deps := s.deps
	deps.Bookings = fault
	s.svc, err = application.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	s.clock.At(t, afterOneNight)
	if _, err := s.svc.CheckOutBooking(ctx, s.deskContext(), booking.Booking.ID,
		application.CheckOutInput{}); err == nil || !fault.failed {
		t.Fatalf("injected checkout status failure = %v, invoked=%t", err, fault.failed)
	}
	current, err := s.svc.GetBooking(ctx, s.deskContext(), booking.Booking.ID)
	if err != nil || current.Booking.Status != accommodationdomain.BookingCheckedIn {
		t.Fatalf("rollback booking=%+v %v", current.Booking, err)
	}
	available, reserved, consumed := s.fractionalAccountState(t)
	if available != "1.000000" || reserved != "3.000000" || consumed != "0.000000" {
		t.Fatalf("rollback account=%s/%s/%s, want 1/3/0", available, reserved, consumed)
	}
	if _, _, confirmedAfterFailure := s.inventoryOf(t, s.roomType, "2026-06-16"); confirmedAfterFailure != confirmedBefore {
		t.Fatalf("rollback second-night confirmed=%d, want %d", confirmedAfterFailure, confirmedBefore)
	}
	if count, _ := s.fractionalMovements(t, *booking.Booking.EntitlementReservationID,
		application.ReasonCheckOut); count != 0 {
		t.Fatalf("rollback checkout RELEASE=%d, want 0", count)
	}
	var fulfilled, checkedOutEvents int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM service.fulfilment
		WHERE tenant_id=$1 AND authorization_id=$2`, s.tenant, *booking.Booking.AuthorizationID).Scan(&fulfilled); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM system.outbox_event
		WHERE tenant_id=$1 AND aggregate_id=$2 AND event_type='booking.checked_out'`,
		s.tenant, booking.Booking.ID).Scan(&checkedOutEvents); err != nil {
		t.Fatal(err)
	}
	if fulfilled != 0 || checkedOutEvents != 0 {
		t.Fatalf("rollback fulfilment/outbox=%d/%d, want 0/0", fulfilled, checkedOutEvents)
	}
	out, err := s.svc.CheckOutBooking(ctx, s.deskContext(), booking.Booking.ID,
		application.CheckOutInput{})
	if err != nil || out.Booking.Status != accommodationdomain.BookingCompleted {
		t.Fatalf("retry checkout: %+v %v", out.Booking, err)
	}
	available, reserved, consumed = s.fractionalAccountState(t)
	if available != "2.000000" || reserved != "0.000000" || consumed != "2.000000" {
		t.Fatalf("retry account=%s/%s/%s, want 2/0/2", available, reserved, consumed)
	}
	if _, _, confirmedAfterRetry := s.inventoryOf(t, s.roomType, "2026-06-16"); confirmedAfterRetry != 0 {
		t.Fatalf("retry second-night confirmed=%d, want 0", confirmedAfterRetry)
	}
	if count, units := s.fractionalMovements(t, *booking.Booking.EntitlementReservationID,
		application.ReasonCheckOut); count != 1 || units != "1.000000" {
		t.Fatalf("retry checkout RELEASE=%d/%s, want 1/1", count, units)
	}
	s.fractionalTerminalEvidence(t, booking, "1.000000", 1)
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM system.outbox_event
		WHERE tenant_id=$1 AND aggregate_id=$2 AND event_type='booking.checked_out'`,
		s.tenant, booking.Booking.ID).Scan(&checkedOutEvents); err != nil {
		t.Fatal(err)
	}
	if checkedOutEvents != 1 {
		t.Fatalf("retry checkout outbox events=%d, want 1", checkedOutEvents)
	}
}
