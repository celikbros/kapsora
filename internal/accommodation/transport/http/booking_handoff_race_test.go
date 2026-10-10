package accommodationhttp_test

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/accommodation/application"
	accommodationpg "github.com/celikbros/kapsora/internal/accommodation/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/db"
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
	bookingStatus, authorizationStatus, reservationStatus    string
	authorizationID, voucherID                               uuid.NullUUID
	available, reserved, consumed                            string
	reservationQuantity, reservationReleased                 string
	inventoryDays, inventoryHeld, inventoryConfirmed         int
	reservations, reserves, releases, vouchers, cancelAudits int
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
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM audit.event
		WHERE tenant_id=$1 AND action_code='authorization.cancel' AND resource_type='authorization'
		AND resource_id IN (SELECT id FROM service.authorization
		WHERE tenant_id=$1 AND idempotency_key=$2)`, s.tenant, "booking:"+bookingID.String()).
		Scan(&st.cancelAudits); err != nil {
		t.Fatal(err)
	}
	return st
}

// A runner-only old-source control restores the terminal cleanup no-op and requires
// HANDOFF_ORPHAN_ACTIVE_AUTHORIZATION from this same deterministic interleaving.
func TestBookingHandoffReleaseAfterCommittedAuthorization(t *testing.T) {
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
		before.reservations != 1 || before.reserves != 1 || before.vouchers != 0 || before.cancelAudits != 0 {
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
	if after.authorizationStatus != "CANCELLED" || after.cancelAudits != 1 {
		t.Fatalf("terminal handoff authorization status/audit = %s/%d, want CANCELLED/1",
			after.authorizationStatus, after.cancelAudits)
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

type interruptAfterAuthorizationCommit struct{ application.AuthorizationPort }

func (a interruptAfterAuthorizationCommit) CreateForRequest(ctx context.Context,
	rc identity.RequestContext, in application.BookingAuthorizationInput,
) (application.BookingAuthorizationRef, error) {
	created, err := a.AuthorizationPort.CreateForRequest(ctx, rc, in)
	if err != nil {
		return created, err
	}
	return created, errors.New("injected worker interruption after authorization commit")
}

func releasedInterruptedHandoff(t *testing.T) (*server, application.BookingView, outbox.Delivery, handoffInventory) {
	t.Helper()
	s, hold, delivery, baseline := handoffBooking(t)
	if hold.Booking.EntitlementReservationID == nil {
		t.Fatal("hold has no entitlement reservation")
	}
	deps := s.deps
	deps.Authorizations = interruptAfterAuthorizationCommit{AuthorizationPort: deps.Authorizations}
	var err error
	s.svc, err = application.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if err := s.svc.HandleServiceRequestDecided(ctx, delivery); err == nil {
		t.Fatal("injected post-commit interruption did not stop the delivery")
	}
	if _, err := s.svc.ReleaseHold(ctx, s.memberContext(), hold.Booking.ID); err != nil {
		t.Fatalf("release after interrupted delivery: %v", err)
	}
	return s, hold, delivery, baseline
}

func TestBookingHandoffInterruptedDeliveryRetiresOnReplay(t *testing.T) {
	s, hold, delivery, baseline := releasedInterruptedHandoff(t)
	before := readBookingHandoffState(t, s, hold.Booking.ID, *hold.Booking.EntitlementReservationID)
	if before.bookingStatus != "CANCELLED" || before.authorizationStatus != "ACTIVE" ||
		before.authorizationID.Valid || before.voucherID.Valid || before.cancelAudits != 0 ||
		before.reservationStatus != "RELEASED" || before.available != "4.000000" ||
		before.reserved != "0.000000" || before.consumed != "0.000000" ||
		before.inventoryHeld != baseline.held || before.inventoryConfirmed != baseline.confirmed ||
		before.reserves != 1 || before.releases != 1 || before.vouchers != 0 {
		t.Fatalf("unexpected interrupted handoff state: %+v", before)
	}
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if err := s.svc.HandleServiceRequestDecided(ctx, delivery); err != nil {
		t.Fatalf("fresh delivery after interruption: %v", err)
	}
	if err := s.svc.HandleServiceRequestDecided(ctx, delivery); err != nil {
		t.Fatalf("identical delivery after retirement: %v", err)
	}
	after := readBookingHandoffState(t, s, hold.Booking.ID, *hold.Booking.EntitlementReservationID)
	if after.bookingStatus != "CANCELLED" || after.authorizationStatus != "CANCELLED" ||
		after.authorizationID.Valid || after.voucherID.Valid || after.cancelAudits != 1 ||
		after.reservationStatus != "RELEASED" || after.available != "4.000000" ||
		after.reserved != "0.000000" || after.consumed != "0.000000" ||
		after.inventoryHeld != baseline.held || after.inventoryConfirmed != baseline.confirmed ||
		after.reserves != 1 || after.releases != 1 || after.vouchers != 0 {
		t.Fatalf("interrupted handoff replay did not retire exactly once: %+v", after)
	}
}

type failRetirementAfterWriteOnce struct {
	application.AuthorizationPort
	failed bool
}

func (a *failRetirementAfterWriteOnce) RetireBookingOrphan(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, in application.BookingOrphanInput,
) error {
	if err := a.AuthorizationPort.RetireBookingOrphan(ctx, tx, rc, in); err != nil {
		return err
	}
	if !a.failed {
		a.failed = true
		return errors.New("injected post-retirement transaction failure")
	}
	return nil
}

func TestBookingHandoffRetirementFailureRollsBackAndRetries(t *testing.T) {
	s, hold, delivery, baseline := releasedInterruptedHandoff(t)
	deps := s.deps
	stop := &failRetirementAfterWriteOnce{AuthorizationPort: deps.Authorizations}
	deps.Authorizations = stop
	var err error
	s.svc, err = application.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if err := s.svc.HandleServiceRequestDecided(ctx, delivery); err == nil || !stop.failed {
		t.Fatalf("injected retirement transaction failure = %v", err)
	}
	rolledBack := readBookingHandoffState(t, s, hold.Booking.ID, *hold.Booking.EntitlementReservationID)
	if rolledBack.authorizationStatus != "ACTIVE" || rolledBack.cancelAudits != 0 ||
		rolledBack.bookingStatus != "CANCELLED" || rolledBack.reservationStatus != "RELEASED" ||
		rolledBack.inventoryHeld != baseline.held || rolledBack.inventoryConfirmed != baseline.confirmed ||
		rolledBack.reserves != 1 || rolledBack.releases != 1 || rolledBack.vouchers != 0 {
		t.Fatalf("retirement failure did not roll back its transaction: %+v", rolledBack)
	}
	if err := s.svc.HandleServiceRequestDecided(ctx, delivery); err != nil {
		t.Fatalf("retirement retry: %v", err)
	}
	after := readBookingHandoffState(t, s, hold.Booking.ID, *hold.Booking.EntitlementReservationID)
	if after.authorizationStatus != "CANCELLED" || after.cancelAudits != 1 ||
		after.bookingStatus != "CANCELLED" || after.reservationStatus != "RELEASED" ||
		after.available != "4.000000" || after.reserved != "0.000000" || after.consumed != "0.000000" ||
		after.inventoryHeld != baseline.held || after.inventoryConfirmed != baseline.confirmed ||
		after.reserves != 1 || after.releases != 1 || after.vouchers != 0 {
		t.Fatalf("retirement retry changed ledger or failed to converge: %+v", after)
	}
}

func TestBookingHandoffProvenanceRefusesWrongEvidence(t *testing.T) {
	s, hold, delivery, _ := releasedInterruptedHandoff(t)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	terminal, err := s.svc.GetBooking(ctx, s.deskContext(), hold.Booking.ID)
	if err != nil || terminal.Booking.ServiceRequestID == nil ||
		terminal.Booking.EntitlementReservationID == nil {
		t.Fatalf("read terminal booking provenance: %+v %v", terminal.Booking, err)
	}
	snapshot, err := application.DecodeQuoteSnapshot(terminal.Booking.QuoteSnapshot)
	if err != nil || snapshot.NightConversion == nil {
		t.Fatalf("read frozen conversion: %+v %v", snapshot, err)
	}
	correct := application.BookingOrphanInput{
		BookingID: hold.Booking.ID, RequestID: *terminal.Booking.ServiceRequestID,
		PersonID: terminal.Booking.PersonID, ReservationID: *terminal.Booking.EntitlementReservationID,
		ServiceDefinitionID: snapshot.ServiceDefinitionID,
		AccountID:           snapshot.NightConversion.AccountID,
		UnitFactor:          snapshot.NightConversion.UnitFactor,
		ReservedUnits:       snapshot.NightConversion.ReservedUnits,
		IdempotencyKey:      "booking:" + hold.Booking.ID.String(),
	}
	for _, tc := range []struct {
		name   string
		mutate func(*application.BookingOrphanInput)
	}{
		{"request", func(in *application.BookingOrphanInput) { in.RequestID = uuid.New() }},
		{"person", func(in *application.BookingOrphanInput) { in.PersonID = uuid.New() }},
		{"reservation", func(in *application.BookingOrphanInput) { in.ReservationID = uuid.New() }},
		{"service", func(in *application.BookingOrphanInput) { in.ServiceDefinitionID = uuid.New() }},
		{"key", func(in *application.BookingOrphanInput) { in.IdempotencyKey = "booking:" + uuid.NewString() }},
		{"account", func(in *application.BookingOrphanInput) { in.AccountID = uuid.New() }},
		{"factor", func(in *application.BookingOrphanInput) { in.UnitFactor = "1" }},
		{"authorization", func(in *application.BookingOrphanInput) { in.ExpectedAuthorizationID = uuid.New() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forged := correct
			tc.mutate(&forged)
			err := db.WithTenantTx(ctx, s.h.App, db.TenantContext{TenantID: s.tenant},
				func(ctx context.Context, tx pgx.Tx) error {
					if _, err := accommodationpg.NewBookings().LockBooking(ctx, tx, s.tenant, hold.Booking.ID); err != nil {
						return err
					}
					return s.deps.Authorizations.RetireBookingOrphan(ctx, tx,
						identity.RequestContext{TenantID: s.tenant}, forged)
				})
			if !errors.Is(err, application.ErrBookingOrphanProvenance) {
				t.Fatalf("wrong %s evidence = %v, want provenance refusal", tc.name, err)
			}
			state := readBookingHandoffState(t, s, hold.Booking.ID, *hold.Booking.EntitlementReservationID)
			if state.authorizationStatus != "ACTIVE" || state.cancelAudits != 0 ||
				state.reserves != 1 || state.releases != 1 || state.vouchers != 0 {
				t.Fatalf("wrong %s evidence mutated orphan: %+v", tc.name, state)
			}
		})
	}
	if err := s.svc.HandleServiceRequestDecided(ctx, delivery); err != nil {
		t.Fatalf("genuine redelivery after forged attempts: %v", err)
	}
	state := readBookingHandoffState(t, s, hold.Booking.ID, *hold.Booking.EntitlementReservationID)
	if state.authorizationStatus != "CANCELLED" || state.cancelAudits != 1 {
		t.Fatalf("genuine redelivery did not retire orphan: %+v", state)
	}
}

func TestBookingHandoffTerminalWithoutAuthorizationIsBenign(t *testing.T) {
	s, hold, delivery, baseline := handoffBooking(t)
	if hold.Booking.EntitlementReservationID == nil {
		t.Fatal("hold has no entitlement reservation")
	}
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if _, err := s.svc.ReleaseHold(ctx, s.memberContext(), hold.Booking.ID); err != nil {
		t.Fatalf("release before event delivery: %v", err)
	}
	if err := s.svc.HandleServiceRequestDecided(ctx, delivery); err != nil {
		t.Fatalf("terminal delivery with no authorization: %v", err)
	}
	var authorizations int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM service.authorization
		WHERE tenant_id=$1 AND idempotency_key=$2`, s.tenant, "booking:"+hold.Booking.ID.String()).
		Scan(&authorizations); err != nil {
		t.Fatal(err)
	}
	if authorizations != 0 {
		t.Fatalf("terminal redelivery created %d authorizations", authorizations)
	}
	inventory := readHandoffInventory(t, s)
	if inventory.held != baseline.held || inventory.confirmed != baseline.confirmed {
		t.Fatalf("terminal redelivery changed inventory: before %+v after %+v", baseline, inventory)
	}
}

type wrongOrphanPerson struct{ application.AuthorizationPort }

func (a wrongOrphanPerson) RetireBookingOrphan(ctx context.Context, tx pgx.Tx,
	rc identity.RequestContext, in application.BookingOrphanInput,
) error {
	in.PersonID = uuid.New()
	return a.AuthorizationPort.RetireBookingOrphan(ctx, tx, rc, in)
}

func TestBookingHandoffBadProvenanceIsPermanent(t *testing.T) {
	s, hold, delivery, _ := releasedInterruptedHandoff(t)
	deps := s.deps
	deps.Authorizations = wrongOrphanPerson{AuthorizationPort: deps.Authorizations}
	var err error
	s.svc, err = application.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := s.h.Ctx()
	defer cancel()
	err = s.svc.HandleServiceRequestDecided(ctx, delivery)
	if outbox.KindOf(err) != outbox.KindPermanent {
		t.Fatalf("wrong persisted orphan provenance = %v, want permanent refusal", err)
	}
	state := readBookingHandoffState(t, s, hold.Booking.ID, *hold.Booking.EntitlementReservationID)
	if state.authorizationStatus != "ACTIVE" || state.cancelAudits != 0 ||
		state.reserves != 1 || state.releases != 1 {
		t.Fatalf("permanent provenance refusal mutated orphan: %+v", state)
	}
}

func TestBookingHandoffUsedPromiseIsNeverRetired(t *testing.T) {
	for _, evidence := range []string{"consumed", "redeemed voucher"} {
		t.Run(evidence, func(t *testing.T) {
			s, hold, delivery, _ := releasedInterruptedHandoff(t)
			ctx, cancel := s.h.Ctx()
			defer cancel()
			var authorizationID uuid.UUID
			if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM service.authorization
				WHERE tenant_id=$1 AND idempotency_key=$2`, s.tenant,
				"booking:"+hold.Booking.ID.String()).Scan(&authorizationID); err != nil {
				t.Fatal(err)
			}
			// The terminal race itself cannot create use evidence. Inject only the
			// conflicting persisted fact, then require the subscriber to refuse it.
			switch evidence {
			case "consumed":
				if _, err := s.h.Admin.Exec(ctx, `UPDATE service.authorization
					SET consumed_total=1 WHERE tenant_id=$1 AND id=$2`, s.tenant, authorizationID); err != nil {
					t.Fatal(err)
				}
			case "redeemed voucher":
				if _, err := s.h.Admin.Exec(ctx, `INSERT INTO service.voucher
					(tenant_id,authorization_id,token_hash,token_masked,valid_from,valid_to,status,redeemed_at)
					VALUES ($1,$2,$3,'***TEST','2026-06-15','2026-06-18','REDEEMED',
					'2026-06-15T12:00:00Z')`, s.tenant, authorizationID, bytes.Repeat([]byte{7}, 32)); err != nil {
					t.Fatal(err)
				}
			}
			err := s.svc.HandleServiceRequestDecided(ctx, delivery)
			if outbox.KindOf(err) != outbox.KindPermanent {
				t.Fatalf("%s evidence cleanup = %v, want permanent refusal", evidence, err)
			}
			state := readBookingHandoffState(t, s, hold.Booking.ID, *hold.Booking.EntitlementReservationID)
			if state.authorizationStatus != "ACTIVE" || state.cancelAudits != 0 ||
				state.bookingStatus != "CANCELLED" || state.reservationStatus != "RELEASED" ||
				state.reserves != 1 || state.releases != 1 {
				t.Fatalf("%s evidence was retired or changed ledger: %+v", evidence, state)
			}
		})
	}
}

func TestBookingHandoffConfirmedThenCancelledDecisionReplay(t *testing.T) {
	s, hold, delivery, _ := handoffBooking(t)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if err := s.svc.HandleServiceRequestDecided(ctx, delivery); err != nil {
		t.Fatalf("first decision delivery: %v", err)
	}
	confirmed := readBookingHandoffState(t, s, hold.Booking.ID, *hold.Booking.EntitlementReservationID)
	if confirmed.bookingStatus != "CONFIRMED" || !confirmed.authorizationID.Valid ||
		!confirmed.voucherID.Valid || confirmed.authorizationStatus != "ACTIVE" {
		t.Fatalf("decision did not link confirmed booking: %+v", confirmed)
	}
	s.clock.At(t, insideFreeWindow)
	if _, err := s.svc.CancelBooking(ctx, s.memberContext(), hold.Booking.ID, ""); err != nil {
		t.Fatalf("cancel confirmed booking: %v", err)
	}
	before := readBookingHandoffState(t, s, hold.Booking.ID, *hold.Booking.EntitlementReservationID)
	if before.bookingStatus != "CANCELLED" || !before.authorizationID.Valid {
		t.Fatalf("cancelled confirmed booking lost historical link: %+v", before)
	}
	if err := s.svc.HandleServiceRequestDecided(ctx, delivery); err != nil {
		t.Fatalf("decision replay for previously confirmed booking: %v", err)
	}
	after := readBookingHandoffState(t, s, hold.Booking.ID, *hold.Booking.EntitlementReservationID)
	if after != before {
		t.Fatalf("decision replay changed previously confirmed/cancelled booking: before %+v after %+v", before, after)
	}
}
