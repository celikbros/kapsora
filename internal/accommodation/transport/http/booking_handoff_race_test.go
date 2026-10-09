package accommodationhttp_test

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/accommodation/application"
	accommodationpg "github.com/celikbros/kapsora/internal/accommodation/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/outbox"
	servicerequestapp "github.com/celikbros/kapsora/internal/servicerequest/application"
	servicerequestdomain "github.com/celikbros/kapsora/internal/servicerequest/domain"
)

// committedAuthorizationBarrier pauses the subscriber only after the real authorization
// command has committed. The release command can then finish before the subscriber locks
// the booking. Channels, rather than elapsed time, define the interleaving.
type committedAuthorizationBarrier struct {
	application.AuthorizationPort
	created chan application.BookingAuthorizationRef
	resume  chan struct{}
}

func (b *committedAuthorizationBarrier) CreateForRequest(ctx context.Context,
	rc identity.RequestContext, in application.BookingAuthorizationInput,
) (application.BookingAuthorizationRef, error) {
	hold, err := b.AuthorizationPort.CreateForRequest(ctx, rc, in)
	if err != nil {
		return hold, err
	}
	select {
	case b.created <- hold:
	case <-ctx.Done():
		return hold, ctx.Err()
	}
	select {
	case <-b.resume:
		return hold, nil
	case <-ctx.Done():
		return hold, ctx.Err()
	}
}

func bookingDecisionDelivery(t *testing.T, s *server, requestID uuid.UUID) outbox.Delivery {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var payload []byte
	if err := s.h.Admin.QueryRow(ctx, `SELECT payload_json FROM system.outbox_event
		WHERE tenant_id=$1 AND event_type=$2 AND aggregate_id=$3
		ORDER BY occurred_at DESC LIMIT 1`, s.tenant, servicerequestapp.DecidedEvent, requestID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	return outbox.Delivery{TenantID: uuid.NullUUID{UUID: s.tenant, Valid: true}, Payload: payload}
}

type handoffInventory struct{ days, held, confirmed int }

func readHandoffInventory(t *testing.T, s *server) handoffInventory {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var inventory handoffInventory
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*),coalesce(sum(held),0),
		coalesce(sum(confirmed),0) FROM accommodation.inventory_day
		WHERE tenant_id=$1 AND room_type_id=$2 AND stay_date >= $3 AND stay_date < $4`,
		s.tenant, s.roomType, checkIn, "2026-06-17").Scan(
		&inventory.days, &inventory.held, &inventory.confirmed); err != nil {
		t.Fatal(err)
	}
	return inventory
}

func handoffBooking(t *testing.T) (*server, application.BookingView, outbox.Delivery, handoffInventory) {
	t.Helper()
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.putLodgingTerms(t)
	s.replaceNightPlan(t, "4", "2")
	baselineInventory := readHandoffInventory(t, s)
	if baselineInventory.days != 2 {
		t.Fatalf("handoff fixture has %d inventory days, want 2", baselineInventory.days)
	}
	hold, _ := s.conversionHold(t, "2026-06-17")
	ctx, cancel := s.h.Ctx()
	defer cancel()
	confirmed, err := s.svc.ConfirmBooking(ctx, s.memberContext(), hold.Booking.ID)
	if err != nil || confirmed.Booking.ServiceRequestID == nil {
		t.Fatalf("confirm before handoff: %+v %v", confirmed.Booking, err)
	}
	requestID := *confirmed.Booking.ServiceRequestID
	s.decideRequest(t, requestID, servicerequestdomain.StatusApproved)
	return s, hold, bookingDecisionDelivery(t, s, requestID), baselineInventory
}

type bookingHandoffState struct {
	bookingStatus, authorizationStatus, reservationStatus string
	authorizationID, voucherID                            uuid.NullUUID
	available, reserved, consumed                         string
	reservationQuantity, reservationReleased              string
	inventoryDays, inventoryHeld, inventoryConfirmed      int
	reservations, reserves, releases, vouchers            int
}

func readBookingHandoffState(t *testing.T, s *server, bookingID, reservationID uuid.UUID) bookingHandoffState {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var st bookingHandoffState
	if err := s.h.Admin.QueryRow(ctx, `SELECT status,authorization_id,voucher_id
		FROM accommodation.booking WHERE tenant_id=$1 AND id=$2`, s.tenant, bookingID).
		Scan(&st.bookingStatus, &st.authorizationID, &st.voucherID); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT status,quantity::text,released_quantity::text
		FROM benefit.entitlement_reservation WHERE tenant_id=$1 AND id=$2`, s.tenant, reservationID).
		Scan(&st.reservationStatus, &st.reservationQuantity, &st.reservationReleased); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT available_quantity::text,reserved_quantity::text,
		consumed_quantity::text FROM benefit.entitlement_account WHERE tenant_id=$1 AND id=$2`,
		s.tenant, s.account).Scan(&st.available, &st.reserved, &st.consumed); err != nil {
		t.Fatal(err)
	}
	inventory := readHandoffInventory(t, s)
	st.inventoryDays, st.inventoryHeld, st.inventoryConfirmed = inventory.days, inventory.held, inventory.confirmed
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM benefit.entitlement_reservation
		WHERE tenant_id=$1 AND reference_type='BOOKING' AND reference_id=$2`,
		s.tenant, bookingID).Scan(&st.reservations); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FILTER (WHERE movement_type='RESERVE'),
		count(*) FILTER (WHERE movement_type='RELEASE') FROM benefit.entitlement_ledger
		WHERE tenant_id=$1 AND reservation_id=$2`, s.tenant, reservationID).
		Scan(&st.reserves, &st.releases); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM service.voucher
		WHERE tenant_id=$1 AND authorization_id IN
		(SELECT id FROM service.authorization WHERE tenant_id=$1 AND idempotency_key=$2)`,
		s.tenant, "booking:"+bookingID.String()).Scan(&st.vouchers); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT status FROM service.authorization
		WHERE tenant_id=$1 AND idempotency_key=$2`, s.tenant, "booking:"+bookingID.String()).
		Scan(&st.authorizationStatus); err != nil {
		t.Fatal(err)
	}
	return st
}

// This source-negative-control test is opt-in until the handoff correction lands. It
// deliberately expects the terminal booking to have no ACTIVE authorization. On the
// current source, its failure identifies an orphan promise after a released reservation.
func TestBookingHandoffReleaseAfterCommittedAuthorization(t *testing.T) {
	if os.Getenv("KAPSORA_TEST_BOOKING_HANDOFF_RACE") != "1" {
		t.Skip("set KAPSORA_TEST_BOOKING_HANDOFF_RACE=1 in isolated CI")
	}
	s, hold, delivery, baselineInventory := handoffBooking(t)
	if hold.Booking.EntitlementReservationID == nil {
		t.Fatal("hold has no entitlement reservation")
	}
	barrier := &committedAuthorizationBarrier{
		AuthorizationPort: s.deps.Authorizations,
		created:           make(chan application.BookingAuthorizationRef, 1), resume: make(chan struct{}),
	}
	deps := s.deps
	deps.Authorizations = barrier
	var err error
	s.svc, err = application.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := s.h.Ctx()
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- s.svc.HandleServiceRequestDecided(ctx, delivery) }()
	var resumeOnce sync.Once
	resume := func() { resumeOnce.Do(func() { close(barrier.resume) }) }
	resultRead := false
	defer func() {
		resume()
		if !resultRead {
			// The harness drops its database in t.Cleanup. Join the subscriber before
			// that teardown, even when an assertion above calls Fatal.
			if err := <-result; err != nil {
				t.Logf("subscriber during test cleanup: %v", err)
			}
		}
	}()
	var created application.BookingAuthorizationRef
	select {
	case created = <-barrier.created:
	case err := <-result:
		resultRead = true
		t.Fatalf("subscriber exited before committed authorization barrier: %v", err)
	case <-ctx.Done():
		t.Fatalf("subscriber did not reach committed authorization barrier: %v", ctx.Err())
	}
	before := readBookingHandoffState(t, s, hold.Booking.ID, *hold.Booking.EntitlementReservationID)
	if before.bookingStatus != "PENDING_APPROVAL" || before.authorizationStatus != "ACTIVE" ||
		before.authorizationID.Valid || before.voucherID.Valid || before.reservationStatus != "HELD" ||
		before.inventoryDays != baselineInventory.days ||
		before.inventoryHeld != baselineInventory.held+2 ||
		before.inventoryConfirmed != baselineInventory.confirmed ||
		before.reservations != 1 || before.reserves != 1 || before.vouchers != 0 {
		t.Fatalf("unexpected committed-authorization barrier state: %+v", before)
	}
	if _, err := s.svc.ReleaseHold(ctx, s.memberContext(), hold.Booking.ID); err != nil {
		t.Fatalf("release while subscriber is paused: %v", err)
	}
	resume()
	select {
	case err = <-result:
		resultRead = true
	case <-ctx.Done():
		t.Fatalf("subscriber did not return after release: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("subscriber after release: %v", err)
	}
	if err := s.svc.HandleServiceRequestDecided(ctx, delivery); err != nil {
		t.Fatalf("identical decision redelivery: %v", err)
	}
	after := readBookingHandoffState(t, s, hold.Booking.ID, *hold.Booking.EntitlementReservationID)
	if after.bookingStatus != "CANCELLED" || after.authorizationID.Valid || after.voucherID.Valid ||
		after.inventoryDays != baselineInventory.days ||
		after.inventoryHeld != baselineInventory.held ||
		after.inventoryConfirmed != baselineInventory.confirmed ||
		after.reservationStatus != "RELEASED" || after.reservationQuantity != "4.000000" ||
		after.reservationReleased != "4.000000" || after.available != "4.000000" ||
		after.reserved != "0.000000" || after.consumed != "0.000000" ||
		after.reservations != 1 || after.reserves != 1 || after.releases != 1 || after.vouchers != 0 {
		t.Fatalf("released booking/ledger/inventory changed on decision replay: %+v", after)
	}
	if after.authorizationStatus == "ACTIVE" {
		t.Fatalf("HANDOFF_ORPHAN_ACTIVE_AUTHORIZATION: booking %s cancelled after authorization %s committed; reservation released and replay did not revoke promise", hold.Booking.ID, created.ID)
	}
}

// The status write fails inside the booking transaction after authorization adoption.
// Retrying the same event must attach that authorization and issue one voucher.
func TestBookingHandoffTransientStatusWriteRetries(t *testing.T) {
	s, hold, delivery, baselineInventory := handoffBooking(t)
	if hold.Booking.EntitlementReservationID == nil {
		t.Fatal("hold has no entitlement reservation")
	}
	deps := s.deps
	stop := &failConfirmationOnce{BookingRepository: accommodationpg.NewBookings()}
	deps.Bookings = stop
	var err error
	s.svc, err = application.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if err := s.svc.HandleServiceRequestDecided(ctx, delivery); err == nil || !stop.failed {
		t.Fatalf("injected status transaction failure = %v, want retry", err)
	}
	intermediate := readBookingHandoffState(t, s, hold.Booking.ID, *hold.Booking.EntitlementReservationID)
	if intermediate.bookingStatus != "PENDING_APPROVAL" || intermediate.authorizationStatus != "ACTIVE" ||
		intermediate.authorizationID.Valid || intermediate.voucherID.Valid || intermediate.vouchers != 0 ||
		intermediate.reservationStatus != "HELD" || intermediate.available != "0.000000" ||
		intermediate.reserved != "4.000000" || intermediate.consumed != "0.000000" ||
		intermediate.inventoryDays != baselineInventory.days ||
		intermediate.inventoryHeld != baselineInventory.held+2 ||
		intermediate.inventoryConfirmed != baselineInventory.confirmed || intermediate.reservations != 1 ||
		intermediate.reserves != 1 || intermediate.releases != 0 {
		t.Fatalf("unexpected state after rolled-back status transaction: %+v", intermediate)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := s.svc.HandleServiceRequestDecided(ctx, delivery); err != nil {
			t.Fatalf("decision replay %d: %v", attempt+1, err)
		}
	}
	final := readBookingHandoffState(t, s, hold.Booking.ID, *hold.Booking.EntitlementReservationID)
	if final.bookingStatus != "CONFIRMED" || final.authorizationStatus != "ACTIVE" ||
		!final.authorizationID.Valid || !final.voucherID.Valid || final.vouchers != 1 ||
		final.reservationStatus != "HELD" || final.available != "0.000000" ||
		final.reserved != "4.000000" || final.consumed != "0.000000" ||
		final.inventoryDays != baselineInventory.days ||
		final.inventoryHeld != baselineInventory.held ||
		final.inventoryConfirmed != baselineInventory.confirmed+2 ||
		final.reservations != 1 || final.reserves != 1 || final.releases != 0 {
		t.Fatalf("status retry did not converge exactly once: %+v", final)
	}
}
