// The booking half of the vertical, driven against a real PostgreSQL with the real
// modules behind it: the real submit gate, the real ledger, the real authorization module
// and the real voucher digest. Nothing below is stubbed, because every one of those is a
// link in the chain a stub would hide.
//
// Three of these tests are the work package's acceptance criteria and are written to go red
// the moment the thing they guard is removed:
//
//   - TestFiveHundredConcurrentHoldsLeaveExactlyThree fails if the `ORDER BY stay_date` is
//     dropped from the lock (deadlock), if the availability check under the lock is skipped
//     (the CHECK fires and the test reports it), or if the lock itself goes.
//   - TestConfirmationReservesTheNightsExactlyOnce fails if the authorization reserves
//     instead of adopting: the account's reserved quantity would move by twice the nights.
//   - TestExpirySweepReleasesOnceAndOnlyOnce fails if the sweep releases twice or if a
//     second run moves anything.
package accommodationhttp_test

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/accommodation/application"
	accommodationdomain "github.com/celikbros/kapsora/internal/accommodation/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/outbox"
	servicerequestapp "github.com/celikbros/kapsora/internal/servicerequest/application"
	servicerequestdomain "github.com/celikbros/kapsora/internal/servicerequest/domain"
)

// bookerPermissions is what a member holds to hold and confirm a room for themselves.
const bookerPermissions = "accommodation.property.read,accommodation.booking.create"

// concurrentHolds is the number the acceptance criterion names, on a room type with three
// rooms. Five hundred is not decoration: three is the number that has to survive, and the
// four hundred and ninety-seven refusals are what proves nothing was oversold by luck.
const (
	concurrentHolds = 500
	concurrentRooms = 3
)

// ---------------------------------------------------------------------------
// Fixture helpers
// ---------------------------------------------------------------------------

// grantNights tops up the seeded member's NIGHT entitlement, because the base fixture holds
// deliberately fewer nights than the stay is long (that is what its own quote test is
// about) and a booking reserves every night of the stay.
func (s *server) grantNights(t *testing.T, extra int) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if _, err := s.h.Admin.Exec(ctx, `
		UPDATE benefit.entitlement_account
		   SET total_granted = total_granted + $3, available_quantity = available_quantity + $3
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.account, extra); err != nil {
		t.Fatalf("grant nights: %v", err)
	}
	if _, err := s.h.Admin.Exec(ctx, `
		INSERT INTO benefit.entitlement_ledger (tenant_id, entitlement_account_id, movement_type,
		                                        effective_at, delta_total, delta_available,
		                                        reference_type, reference_id, idempotency_key)
		VALUES ($1, $2, 'GRANT', clock_timestamp(), $3, $3, 'ENROLLMENT', $4, $5)`,
		s.tenant, s.account, extra, s.enrollment,
		fmt.Sprintf("grant:top-up:%d:%s", extra, uuid.NewString())); err != nil {
		t.Fatalf("grant ledger row: %v", err)
	}
}

// putLodgingTerms writes the cancellation policy a confirmation freezes. Without it every
// confirmation is LODGING_TERMS_MISSING, which is the point of one of the tests below.
func (s *server) putLodgingTerms(t *testing.T) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	// The terms may only be written while the version is a draft — migration 000039's
	// trigger says so, and that rule is exactly what makes a published sheet immutable. The
	// fixture publishes its version at seed time, so it is walked back to DRAFT for the
	// insert and published again, rather than the trigger being worked around.
	if _, err := s.h.Admin.Exec(ctx, `
		UPDATE contract.contract_version SET status = 'DRAFT'
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.contractVersion); err != nil {
		t.Fatalf("unpublish the version: %v", err)
	}
	if _, err := s.h.Admin.Exec(ctx, `
		INSERT INTO contract.lodging_terms (tenant_id, contract_version_id,
		                                    free_cancellation_hours_before, penalty_kind,
		                                    penalty_nights, no_show_percent, min_nights)
		VALUES ($1, $2, 48, 'NIGHTS', 1, 100, 1)`, s.tenant, s.contractVersion); err != nil {
		t.Fatalf("seed lodging terms: %v", err)
	}
	if _, err := s.h.Admin.Exec(ctx, `
		UPDATE contract.contract_version SET status = 'PUBLISHED'
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.contractVersion); err != nil {
		t.Fatalf("republish the version: %v", err)
	}
}

// openAllotment sets one capacity across the whole stay for a room type.
func (s *server) openAllotment(t *testing.T, roomType uuid.UUID, capacity int) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if _, err := s.h.Admin.Exec(ctx, `
		INSERT INTO accommodation.inventory_day (tenant_id, room_type_id, stay_date, capacity)
		SELECT $1, $2, d::date, $3
		  FROM generate_series($4::date, $5::date, interval '1 day') AS d
		ON CONFLICT (tenant_id, room_type_id, stay_date)
		DO UPDATE SET capacity = EXCLUDED.capacity, held = 0, confirmed = 0`,
		s.tenant, roomType, capacity, checkIn, lastNight); err != nil {
		t.Fatalf("open allotment: %v", err)
	}
}

// memberContext is the request context of the seeded member: bound to their own person, so
// every boundary in the service is the real one rather than a tenant-wide read.
func (s *server) memberContext() identity.RequestContext {
	return s.contextFor(s.person)
}

func (s *server) contextFor(person uuid.UUID) identity.RequestContext {
	rc := identity.RequestContext{
		TenantID: s.tenant, MembershipID: s.membership,
		Principal:   identity.Principal{ActorID: s.actor},
		StepUpValid: true,
		Permissions: map[string]struct{}{
			"accommodation.property.read":   {},
			"accommodation.booking.create":  {},
			"service_request.read":          {},
			"service_request.create":        {},
			"service_request.submit":        {},
			"service_request.approve":       {},
			"authorization.manage":          {},
			"contract.lodging_terms.manage": {},
			"contract.read":                 {},
		},
		PersonID: uuid.NullUUID{UUID: person, Valid: true},
	}
	rc.Scopes = append(rc.Scopes, identity.Scope{Type: identity.ScopePerson, ID: rc.PersonID})
	return rc
}

// deskContext is a back-office caller: no person binding, no provider scope. It is what the
// outbox subscriber and the sweeps act as, and what a reservation desk holds.
func (s *server) deskContext() identity.RequestContext {
	return identity.RequestContext{
		TenantID: s.tenant, MembershipID: s.membership,
		Principal:   identity.Principal{ActorID: s.actor},
		StepUpValid: true,
		Permissions: map[string]struct{}{
			"accommodation.property.read":  {},
			"accommodation.booking.create": {},
			"accommodation.booking.manage": {},
			"service_request.read":         {},
			"service_request.approve":      {},
			"authorization.manage":         {},
		},
	}
}

// holdInput is the standard hold: three nights for two adults in the seeded room type.
func (s *server) holdInput(person, roomType uuid.UUID) application.HoldInput {
	return application.HoldInput{
		PersonID: person, RoomTypeID: roomType,
		CheckIn: mustDay(checkIn), CheckOut: mustDay(checkOut), Adults: 2,
		Channel: accommodationdomain.ChannelMemberPortal,
	}
}

func mustDay(text string) time.Time {
	day, err := time.Parse(time.DateOnly, text)
	if err != nil {
		panic(err)
	}
	return day
}

// inventoryOf reads the three counters of one night straight from the table, so an assertion
// about oversell is an assertion about the row the database holds and not about a view.
func (s *server) inventoryOf(t *testing.T, roomType uuid.UUID, day string) (capacity, held, confirmed int) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT capacity, held, confirmed FROM accommodation.inventory_day
		 WHERE tenant_id = $1 AND room_type_id = $2 AND stay_date = $3::date`,
		s.tenant, roomType, day).Scan(&capacity, &held, &confirmed); err != nil {
		t.Fatalf("read inventory for %s: %v", day, err)
	}
	return capacity, held, confirmed
}

// accountBalances reads the ledger's own materialised balances.
func (s *server) accountBalances(t *testing.T) (available, reserved string) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT available_quantity::text, reserved_quantity::text
		  FROM benefit.entitlement_account WHERE tenant_id = $1 AND id = $2`,
		s.tenant, s.account).Scan(&available, &reserved); err != nil {
		t.Fatalf("read balances: %v", err)
	}
	return available, reserved
}

// assertNightRowsMatch is the invariant the work package asks for after every command: the
// number of booking_night rows equals the booking's own night count.
func (s *server) assertNightRowsMatch(t *testing.T, bookingID uuid.UUID, where string) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var nights, rows int
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT b.nights,
		       (SELECT count(*) FROM accommodation.booking_night n
		         WHERE n.tenant_id = b.tenant_id AND n.booking_id = b.id)
		  FROM accommodation.booking b WHERE b.tenant_id = $1 AND b.id = $2`,
		s.tenant, bookingID).Scan(&nights, &rows); err != nil {
		t.Fatalf("count booking nights (%s): %v", where, err)
	}
	if nights != rows {
		t.Errorf("%s: booking says %d nights and has %d night rows", where, nights, rows)
	}
}

// decideRequest is the reviewer's decision, taken through WP-I4-01's own command so the
// lines are decided the way a real approval decides them.
func (s *server) decideRequest(t *testing.T, requestID uuid.UUID, status string) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	view, err := s.requests.Get(ctx, s.deskContext(), requestID)
	if err != nil {
		t.Fatalf("read request: %v", err)
	}
	switch status {
	case servicerequestdomain.StatusApproved:
		_, err = s.requests.Approve(ctx, s.deskContext(), requestID, servicerequestapp.DecisionInput{
			ReasonCode: "TEST", ExpectedVersion: view.Request.RowVersion,
		})
	default:
		_, err = s.requests.Reject(ctx, s.deskContext(), requestID, servicerequestapp.ReasonInput{
			ReasonCode: "TEST", ExpectedVersion: view.Request.RowVersion,
		})
	}
	if err != nil {
		t.Fatalf("decide request as %s: %v", status, err)
	}
}

// deliverDecision feeds the booking's subscriber the outbox row the decision actually
// wrote. The payload is read from the table rather than built here, so a change to the
// event's shape breaks this test instead of silently breaking the subscriber.
func (s *server) deliverDecision(t *testing.T, requestID uuid.UUID) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var payload []byte
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT payload_json FROM system.outbox_event
		 WHERE tenant_id = $1 AND event_type = $2 AND aggregate_id = $3
		 ORDER BY occurred_at DESC LIMIT 1`,
		s.tenant, servicerequestapp.DecidedEvent, requestID).Scan(&payload); err != nil {
		t.Fatalf("read decided event: %v", err)
	}
	if err := s.svc.HandleServiceRequestDecided(ctx, outbox.Delivery{
		TenantID: uuid.NullUUID{UUID: s.tenant, Valid: true}, Payload: payload,
	}); err != nil {
		t.Fatalf("handle decided event: %v", err)
	}
}

// ---------------------------------------------------------------------------
// No oversell
// ---------------------------------------------------------------------------

// TestFiveHundredConcurrentHoldsLeaveExactlyThree is the acceptance criterion of the work
// package, proved on a real PostgreSQL with real goroutines rather than by argument.
//
// Five hundred different members hold the same three nights of a room type with three
// rooms, all at once, through one pool. Exactly three succeed. `held` is three on every
// night, never four and never negative. Four hundred and ninety-seven are told
// ROOM_UNAVAILABLE and told which night. Nothing deadlocks, and the CHECK on the row never
// fires — because the CHECK is the belt and the `FOR UPDATE ... ORDER BY stay_date` is the
// braces, and this test is what says the braces are doing the work.
//
// Five hundred *different* members, because the partial unique index would refuse a second
// live booking for one person and the test would then prove that index rather than the lock.
func TestFiveHundredConcurrentHoldsLeaveExactlyThree(t *testing.T) {
	s := newServer(t)
	roomType := s.seedConcurrencyWorld(t, concurrentHolds, concurrentRooms)

	type outcome struct {
		err     error
		booking uuid.UUID
	}
	results := make([]outcome, concurrentHolds)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < concurrentHolds; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			<-start
			person := s.crowd[i]
			view, err := s.svc.CreateHold(ctx, s.contextFor(person), s.holdInput(person, roomType))
			results[i] = outcome{err: err, booking: view.Booking.ID}
		}(i)
	}
	close(start)
	wg.Wait()

	held, unavailable, other := 0, 0, 0
	for _, r := range results {
		switch {
		case r.err == nil:
			held++
		case isRoomUnavailable(r.err):
			unavailable++
		default:
			other++
			t.Errorf("unexpected refusal: %v", r.err)
		}
	}
	if held != concurrentRooms {
		t.Errorf("holds that succeeded = %d, want exactly %d — the room was oversold or undersold",
			held, concurrentRooms)
	}
	if unavailable != concurrentHolds-concurrentRooms {
		t.Errorf("ROOM_UNAVAILABLE refusals = %d, want %d", unavailable, concurrentHolds-concurrentRooms)
	}
	if other != 0 {
		t.Errorf("%d holds failed for a reason that is neither success nor a full room", other)
	}

	for _, day := range []string{checkIn, "2026-06-16", lastNight} {
		capacity, heldRooms, confirmed := s.inventoryOf(t, roomType, day)
		if heldRooms != concurrentRooms || confirmed != 0 {
			t.Errorf("%s: held = %d, confirmed = %d, want %d held and 0 confirmed",
				day, heldRooms, confirmed, concurrentRooms)
		}
		if heldRooms+confirmed > capacity {
			t.Errorf("%s: oversold — held + confirmed = %d over a capacity of %d",
				day, heldRooms+confirmed, capacity)
		}
	}

	ctx, cancel := s.h.Ctx()
	defer cancel()
	var rows int
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM accommodation.booking
		 WHERE tenant_id = $1 AND room_type_id = $2 AND status = 'HOLD'`,
		s.tenant, roomType).Scan(&rows); err != nil {
		t.Fatalf("count holds: %v", err)
	}
	if rows != concurrentRooms {
		t.Errorf("HOLD rows = %d, want %d", rows, concurrentRooms)
	}
	for _, r := range results {
		if r.err == nil {
			s.assertNightRowsMatch(t, r.booking, "after a concurrent hold")
		}
	}
}

func isRoomUnavailable(err error) bool {
	var unavailable *application.RoomUnavailable
	return asError(err, &unavailable)
}

// ---------------------------------------------------------------------------
// The hold and giving it back
// ---------------------------------------------------------------------------

// TestHoldFreezesTheQuoteAndTakesTheNights is the ordinary path: the room is set aside, the
// countdown runs, the plan's nights are reserved with the hold's own deadline, and the
// amounts stored are the amounts the search would have shown.
func TestHoldFreezesTheQuoteAndTakesTheNights(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)

	availableBefore, reservedBefore := s.accountBalances(t)
	if availableBefore != "12.000000" || reservedBefore != "0.000000" {
		t.Fatalf("fixture: available = %q, reserved = %q, want 12 and 0", availableBefore, reservedBefore)
	}

	ctx, cancel := s.h.Ctx()
	defer cancel()
	view, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if view.Booking.Status != accommodationdomain.BookingHold {
		t.Errorf("status = %q, want HOLD", view.Booking.Status)
	}
	if view.SecondsToExpiry <= 0 || view.SecondsToExpiry > 15*60 {
		t.Errorf("secondsToExpiry = %d, want a countdown inside the tenant's fifteen minutes",
			view.SecondsToExpiry)
	}
	if view.Booking.EntitlementReservationID == nil {
		t.Fatal("the hold reserved no entitlement; a room set aside against nothing is a " +
			"promise the plan cannot keep")
	}
	if len(view.Nights) != 3 {
		t.Fatalf("night rows = %d, want 3", len(view.Nights))
	}
	s.assertNightRowsMatch(t, view.Booking.ID, "after the hold")

	// Three nights at 1000 with a 10 % member share, all three carried by the plan.
	snapshot, err := application.DecodeQuoteSnapshot(view.Booking.QuoteSnapshot)
	if err != nil {
		t.Fatalf("decode frozen quote: %v", err)
	}
	if snapshot.TotalAmount != "3000" || snapshot.PayerAmount != "2700" || snapshot.MemberAmount != "300" {
		t.Errorf("frozen quote = total %q, payer %q, member %q; want 3000 / 2700 / 300",
			snapshot.TotalAmount, snapshot.PayerAmount, snapshot.MemberAmount)
	}
	if snapshot.EvaluationID == nil {
		t.Error("the frozen quote names no eligibility evaluation")
	}

	for _, day := range []string{checkIn, "2026-06-16", lastNight} {
		_, held, _ := s.inventoryOf(t, s.roomType, day)
		if held < 1 {
			t.Errorf("%s: held = %d, want at least the room this hold took", day, held)
		}
	}
	available, reserved := s.accountBalances(t)
	if available != "9.000000" || reserved != "3.000000" {
		t.Errorf("after the hold: available = %q, reserved = %q; want 9 and 3", available, reserved)
	}
}

// TestReleasingAHoldGivesBackTheRoomAndTheNightsOnce is the member changing their mind
// before anything was agreed. Nothing is charged, the room comes back, and the nights come
// back exactly once.
func TestReleasingAHoldGivesBackTheRoomAndTheNightsOnce(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	_, heldBefore, _ := s.inventoryOf(t, s.roomType, checkIn)
	view, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	released, err := s.svc.ReleaseHold(ctx, s.memberContext(), view.Booking.ID)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if released.Booking.Status != accommodationdomain.BookingCancelled {
		t.Errorf("status = %q, want CANCELLED", released.Booking.Status)
	}
	if released.SecondsToExpiry != 0 {
		t.Errorf("a released booking still shows %d seconds on its countdown", released.SecondsToExpiry)
	}
	_, heldAfter, _ := s.inventoryOf(t, s.roomType, checkIn)
	if heldAfter != heldBefore {
		t.Errorf("held = %d after the release, want the %d it was before", heldAfter, heldBefore)
	}
	available, reserved := s.accountBalances(t)
	if available != "12.000000" || reserved != "0.000000" {
		t.Errorf("after the release: available = %q, reserved = %q; want 12 and 0", available, reserved)
	}
	s.assertNightRowsMatch(t, view.Booking.ID, "after the release")

	// Releasing again is refused rather than giving the room back twice.
	if _, err := s.svc.ReleaseHold(ctx, s.memberContext(), view.Booking.ID); err == nil {
		t.Error("releasing a released booking succeeded; the room would come back twice")
	}
}

// TestExpirySweepReleasesOnceAndOnlyOnce is the acceptance criterion that a hold gives the
// room and the nights back without anybody calling an API.
//
// The second run is the whole point. The sweep has to be safe under any number of passes,
// and the way it is safe is a predicate rather than a flag: the second pass finds an EXPIRED
// booking, updates nothing, and gives nothing back.
func TestExpirySweepReleasesOnceAndOnlyOnce(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	_, heldBefore, _ := s.inventoryOf(t, s.roomType, checkIn)
	view, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	// The countdown is pushed into the past, which is what a member who closed the tab
	// fifteen minutes ago looks like to the sweep.
	if _, err := s.h.Admin.Exec(ctx, `
		UPDATE accommodation.booking SET hold_expires_at = clock_timestamp() - interval '1 minute'
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, view.Booking.ID); err != nil {
		t.Fatalf("age the hold: %v", err)
	}

	expired, err := s.svc.ExpireHolds(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("expiry sweep: %v", err)
	}
	if expired != 1 {
		t.Errorf("first sweep expired %d holds, want 1", expired)
	}
	after, err := s.svc.GetBooking(ctx, s.deskContext(), view.Booking.ID)
	if err != nil {
		t.Fatalf("read the expired booking: %v", err)
	}
	if after.Booking.Status != accommodationdomain.BookingExpired {
		t.Errorf("status = %q, want EXPIRED", after.Booking.Status)
	}
	_, heldAfter, _ := s.inventoryOf(t, s.roomType, checkIn)
	if heldAfter != heldBefore {
		t.Errorf("held = %d after the sweep, want the %d it was before", heldAfter, heldBefore)
	}
	available, reserved := s.accountBalances(t)
	if available != "12.000000" || reserved != "0.000000" {
		t.Errorf("after the sweep: available = %q, reserved = %q; want 12 and 0", available, reserved)
	}
	s.assertNightRowsMatch(t, view.Booking.ID, "after the sweep")

	// The second run. It must change nothing at all.
	again, err := s.svc.ExpireHolds(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if again != 0 {
		t.Errorf("second sweep expired %d holds, want 0", again)
	}
	_, heldTwice, _ := s.inventoryOf(t, s.roomType, checkIn)
	if heldTwice != heldBefore {
		t.Errorf("held = %d after the second sweep, want %d — the room came back twice",
			heldTwice, heldBefore)
	}
	availableTwice, reservedTwice := s.accountBalances(t)
	if availableTwice != available || reservedTwice != reserved {
		t.Errorf("the second sweep moved the ledger: available %q -> %q, reserved %q -> %q",
			available, availableTwice, reserved, reservedTwice)
	}
}

// ---------------------------------------------------------------------------
// Confirmation
// ---------------------------------------------------------------------------

// TestConfirmationReservesTheNightsExactlyOnce is the acceptance criterion the adoption
// exists for.
//
// After the hold, three nights are reserved. After the approval, three nights are *still*
// reserved — not six — and the authorization's own line points at the very reservation the
// hold took. An authorization that reserved instead of adopting would leave two real holds
// on one account for one stay, the ledger's conservation would still be satisfied, and
// nothing anywhere would say the member's plan had been drawn down twice. This test is what
// says it.
func TestConfirmationReservesTheNightsExactlyOnce(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	s.putLodgingTerms(t)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	view, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	holdReservation := *view.Booking.EntitlementReservationID
	_, reservedAfterHold := s.accountBalances(t)
	if reservedAfterHold != "3.000000" {
		t.Fatalf("after the hold: reserved = %q, want 3", reservedAfterHold)
	}

	confirmed, err := s.svc.ConfirmBooking(ctx, s.memberContext(), view.Booking.ID)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if confirmed.Booking.ServiceRequestID == nil {
		t.Fatal("confirming raised no reservation request")
	}
	requestID := *confirmed.Booking.ServiceRequestID
	s.assertNightRowsMatch(t, view.Booking.ID, "after the confirmation was raised")

	s.decideRequest(t, requestID, servicerequestdomain.StatusApproved)
	s.deliverDecision(t, requestID)

	final, err := s.svc.GetBooking(ctx, s.deskContext(), view.Booking.ID)
	if err != nil {
		t.Fatalf("read the confirmed booking: %v", err)
	}
	if final.Booking.Status != accommodationdomain.BookingConfirmed {
		t.Fatalf("status = %q, want CONFIRMED", final.Booking.Status)
	}
	if final.Booking.AuthorizationID == nil {
		t.Fatal("a confirmed booking with no authorization")
	}
	if final.Booking.PolicySnapshot == nil {
		t.Error("the confirmation froze no cancellation policy")
	}
	s.assertNightRowsMatch(t, view.Booking.ID, "after the approval")

	// The ledger moved by exactly the nights, in total.
	available, reserved := s.accountBalances(t)
	if reserved != "3.000000" || available != "9.000000" {
		t.Errorf("after the approval: available = %q, reserved = %q; want 9 and 3 — "+
			"six reserved would mean the authorization took a second hold", available, reserved)
	}

	// The authorization's line points at the hold's own reservation.
	var itemReservation uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT ai.entitlement_reservation_id FROM service.authorization_item ai
		 WHERE ai.tenant_id = $1 AND ai.authorization_id = $2`,
		s.tenant, *final.Booking.AuthorizationID).Scan(&itemReservation); err != nil {
		t.Fatalf("read the authorization line: %v", err)
	}
	if itemReservation != holdReservation {
		t.Errorf("the authorization line holds reservation %s, want the hold's own %s",
			itemReservation, holdReservation)
	}
	var reservationCount int
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM benefit.entitlement_reservation
		 WHERE tenant_id = $1 AND entitlement_account_id = $2`, s.tenant, s.account).
		Scan(&reservationCount); err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if reservationCount != 1 {
		t.Errorf("reservations on the account = %d, want exactly 1", reservationCount)
	}

	// held became confirmed on every night, and neither went negative.
	for _, day := range []string{checkIn, "2026-06-16", lastNight} {
		_, held, confirmedRooms := s.inventoryOf(t, s.roomType, day)
		if confirmedRooms < 1 {
			t.Errorf("%s: confirmed = %d, want the room this booking took", day, confirmedRooms)
		}
		if held < 0 {
			t.Errorf("%s: held went negative (%d)", day, held)
		}
	}

	// The voucher: a digest exists, and the plaintext is nowhere.
	var digestLength int
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT octet_length(token_hash) FROM service.voucher
		 WHERE tenant_id = $1 AND authorization_id = $2 AND status = 'ISSUED'`,
		s.tenant, *final.Booking.AuthorizationID).Scan(&digestLength); err != nil {
		t.Fatalf("read the voucher digest: %v", err)
	}
	if digestLength != 32 {
		t.Errorf("voucher digest is %d bytes, want a 32-byte SHA-256", digestLength)
	}

	// And the reissue hands the member a token, once, which appears in no table.
	issued, err := s.svc.IssueBookingVoucher(ctx, s.memberContext(), view.Booking.ID)
	if err != nil {
		t.Fatalf("issue the member's voucher: %v", err)
	}
	if issued.Token == "" {
		t.Fatal("the voucher command returned no token")
	}
	s.assertTokenAppearsNowhere(t, issued.Token)
}

// assertTokenAppearsNowhere sweeps every text and jsonb column of the tenant's own data for
// the plaintext. It is a blunt instrument on purpose: the rule is that the token exists in
// one response body and nowhere else, and a targeted assertion would only ever check the
// places somebody remembered to think of.
func (s *server) assertTokenAppearsNowhere(t *testing.T, token string) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	rows, err := s.h.Admin.Query(ctx, `
		SELECT c.table_schema, c.table_name, c.column_name
		  FROM information_schema.columns c
		  JOIN information_schema.tables tb
		    ON tb.table_schema = c.table_schema AND tb.table_name = c.table_name
		 WHERE tb.table_type = 'BASE TABLE'
		   AND c.table_schema IN ('accommodation','service','benefit','audit','platform','workflow','notification')
		   AND c.data_type IN ('text','character varying','jsonb','json')`)
	if err != nil {
		t.Fatalf("list columns: %v", err)
	}
	type column struct{ schema, table, name string }
	var columns []column
	for rows.Next() {
		var c column
		if err := rows.Scan(&c.schema, &c.table, &c.name); err != nil {
			rows.Close()
			t.Fatalf("scan column: %v", err)
		}
		columns = append(columns, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("list columns: %v", err)
	}
	for _, c := range columns {
		query := fmt.Sprintf(`SELECT count(*) FROM %s.%s WHERE %s::text LIKE $1`,
			pgx.Identifier{c.schema}.Sanitize(), pgx.Identifier{c.table}.Sanitize(),
			pgx.Identifier{c.name}.Sanitize())
		var found int
		if err := s.h.Admin.QueryRow(ctx, query, "%"+token+"%").Scan(&found); err != nil {
			// A partitioned parent or a column the admin role cannot read is not a finding.
			continue
		}
		if found > 0 {
			t.Errorf("the voucher token appears in %s.%s.%s — it must exist in one response "+
				"body and nowhere else", c.schema, c.table, c.name)
		}
	}
}

// TestRejectionGivesTheRoomAndTheNightsBackOnce is the other half of the decision: a refused
// reservation cancels the booking and gives back everything it was holding, exactly once.
func TestRejectionGivesTheRoomAndTheNightsBackOnce(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	s.putLodgingTerms(t)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	_, heldBefore, _ := s.inventoryOf(t, s.roomType, checkIn)
	view, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	confirmed, err := s.svc.ConfirmBooking(ctx, s.memberContext(), view.Booking.ID)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	requestID := *confirmed.Booking.ServiceRequestID

	s.decideRequest(t, requestID, servicerequestdomain.StatusRejected)
	s.deliverDecision(t, requestID)

	final, err := s.svc.GetBooking(ctx, s.deskContext(), view.Booking.ID)
	if err != nil {
		t.Fatalf("read the refused booking: %v", err)
	}
	if final.Booking.Status != accommodationdomain.BookingCancelled {
		t.Errorf("status = %q, want CANCELLED", final.Booking.Status)
	}
	_, heldAfter, confirmedRooms := s.inventoryOf(t, s.roomType, checkIn)
	if heldAfter != heldBefore || confirmedRooms != 0 {
		t.Errorf("after the refusal: held = %d (want %d), confirmed = %d (want 0)",
			heldAfter, heldBefore, confirmedRooms)
	}
	available, reserved := s.accountBalances(t)
	if available != "12.000000" || reserved != "0.000000" {
		t.Errorf("after the refusal: available = %q, reserved = %q; want 12 and 0", available, reserved)
	}
	s.assertNightRowsMatch(t, view.Booking.ID, "after the refusal")

	// A redelivered refusal must change nothing.
	s.deliverDecision(t, requestID)
	availableAgain, reservedAgain := s.accountBalances(t)
	if availableAgain != available || reservedAgain != reserved {
		t.Errorf("a redelivered refusal moved the ledger: available %q -> %q, reserved %q -> %q",
			available, availableAgain, reserved, reservedAgain)
	}
	_, heldAgain, _ := s.inventoryOf(t, s.roomType, checkIn)
	if heldAgain != heldAfter {
		t.Errorf("a redelivered refusal gave the room back twice: held %d -> %d", heldAfter, heldAgain)
	}
}

// TestConfirmationRefusesAStaleQuote is the rule that a member is charged what they saw.
// The frozen quote is aged past the tenant's accommodation.quote_ttl_minutes and the
// confirmation is refused; the room is still held, so they can search again and start over.
func TestConfirmationRefusesAStaleQuote(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	s.putLodgingTerms(t)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	view, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	// The quote was taken two hours ago; the tenant's default TTL is one.
	if _, err := s.h.Admin.Exec(ctx, `
		UPDATE accommodation.booking
		   SET quote_snapshot = jsonb_set(quote_snapshot, '{quotedAt}',
		         to_jsonb(to_char((now() AT TIME ZONE 'UTC') - interval '2 hours',
		                          'YYYY-MM-DD"T"HH24:MI:SS"Z"')))
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, view.Booking.ID); err != nil {
		t.Fatalf("age the quote: %v", err)
	}

	_, err = s.svc.ConfirmBooking(ctx, s.memberContext(), view.Booking.ID)
	if err == nil {
		t.Fatal("a two-hour-old quote was confirmed; the member would be charged a price " +
			"nobody had looked at")
	}
	if !isError(err, application.ErrQuoteStale) {
		t.Fatalf("confirm = %v, want ErrQuoteStale", err)
	}
	still, err := s.svc.GetBooking(ctx, s.memberContext(), view.Booking.ID)
	if err != nil {
		t.Fatalf("read the booking: %v", err)
	}
	if still.Booking.Status != accommodationdomain.BookingHold {
		t.Errorf("status = %q after a refused confirmation, want the room still HOLD",
			still.Booking.Status)
	}
}

// TestConfirmationRefusesAVersionWithNoLodgingTerms is the other 409 the member sees before
// a reviewer ever does: a stay agreed with no cancellation policy is a stay nobody could
// cancel fairly, and no default is invented here.
func TestConfirmationRefusesAVersionWithNoLodgingTerms(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	view, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if _, err := s.svc.ConfirmBooking(ctx, s.memberContext(), view.Booking.ID); !isError(err, application.ErrLodgingTermsMissing) {
		t.Fatalf("confirm = %v, want ErrLodgingTermsMissing", err)
	}
	// And nothing was raised: a refused confirmation leaves no request behind.
	var requests int
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM service.service_request WHERE tenant_id = $1`, s.tenant).
		Scan(&requests); err != nil {
		t.Fatalf("count requests: %v", err)
	}
	if requests != 0 {
		t.Errorf("a refused confirmation left %d service requests behind", requests)
	}
}

// TestStepUpIsRequiredAboveTheTenantThreshold is WP-I1-02's re-entered password, applied
// where the member's own share is large. The threshold is lowered under the quote's member
// share so the rule fires on a fixture whose amounts are already asserted elsewhere.
func TestStepUpIsRequiredAboveTheTenantThreshold(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	s.putLodgingTerms(t)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	if _, err := s.h.Admin.Exec(ctx, `
		INSERT INTO platform.tenant_setting (tenant_id, setting_key, value_json)
		VALUES ($1, 'accommodation.stepup_member_amount', to_jsonb('100'::text))`, s.tenant); err != nil {
		t.Fatalf("set the step-up threshold: %v", err)
	}
	view, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
	if err != nil {
		t.Fatalf("hold: %v", err)
	}

	weak := s.memberContext()
	weak.StepUpValid = false
	if _, err := s.svc.ConfirmBooking(ctx, weak, view.Booking.ID); !isError(err, identity.ErrStepUpRequired) {
		t.Fatalf("confirm without step-up = %v, want ErrStepUpRequired (the member's share "+
			"of 300 is above the tenant's threshold of 100)", err)
	}
	if _, err := s.svc.ConfirmBooking(ctx, s.memberContext(), view.Booking.ID); err != nil {
		t.Fatalf("confirm with a valid step-up: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Partial coverage
// ---------------------------------------------------------------------------

// TestPartiallyCoveredStayHoldsWhatWasQuoted is the rule WP-I6-01 and WP-I6-02 have to
// state once between them.
//
// The search tells a member with two nights left and a three-night stay that the plan
// carries two of them and the third is theirs. If the hold then insisted on reserving three
// it would refuse exactly the booking they were just quoted, and the quote would have been a
// lie. So the hold reserves two, the booking is still three nights long -- the guest sleeps
// three nights -- and the frozen quote says which is which.
//
// The base fixture is deliberately built for this: two nights of entitlement against a
// three-night stay. `grantNights` is not called here, and that absence is the test.
func TestPartiallyCoveredStayHoldsWhatWasQuoted(t *testing.T) {
	s := newServer(t)
	s.putLodgingTerms(t)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	available, reserved := s.accountBalances(t)
	if available != "2.000000" || reserved != "0.000000" {
		t.Fatalf("fixture: available = %q, reserved = %q, want the two nights of the base world",
			available, reserved)
	}

	view, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
	if err != nil {
		t.Fatalf("a three-night hold on a two-night balance was refused: %v", err)
	}
	if view.Booking.Nights != 3 {
		t.Errorf("nights = %d, want 3 — the guest sleeps three nights whatever the plan pays for",
			view.Booking.Nights)
	}
	if len(view.Nights) != 3 {
		t.Errorf("night rows = %d, want 3", len(view.Nights))
	}
	s.assertNightRowsMatch(t, view.Booking.ID, "after a partially covered hold")

	snapshot, err := application.DecodeQuoteSnapshot(view.Booking.QuoteSnapshot)
	if err != nil {
		t.Fatalf("decode frozen quote: %v", err)
	}
	if snapshot.CoveredNights != 2 {
		t.Fatalf("coveredNights = %d, want 2", snapshot.CoveredNights)
	}
	if snapshot.Eligible {
		t.Error("eligible is true for a stay the plan carries only part of")
	}
	// Three nights at 1000 with a 10 % member share, two of them carried: the plan pays
	// 900 + 900 and the member pays 100 + 100 + 1000.
	if snapshot.TotalAmount != "3000" || snapshot.PayerAmount != "1800" ||
		snapshot.MemberAmount != "1200" {
		t.Errorf("frozen quote = total %q, payer %q, member %q; want 3000 / 1800 / 1200",
			snapshot.TotalAmount, snapshot.PayerAmount, snapshot.MemberAmount)
	}

	// The plan gave up two nights, not three.
	available, reserved = s.accountBalances(t)
	if available != "0.000000" || reserved != "2.000000" {
		t.Fatalf("after the hold: available = %q, reserved = %q; want 0 and 2", available, reserved)
	}

	confirmed, err := s.svc.ConfirmBooking(ctx, s.memberContext(), view.Booking.ID)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	requestID := *confirmed.Booking.ServiceRequestID

	// The request asks for the plan's part of the stay: two nights, and the payer's own
	// share of them.
	var quantity, amount string
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT i.requested_quantity::text, i.requested_amount::text
		  FROM service.service_request_item i
		  JOIN service.service_request_version v
		    ON v.tenant_id = i.tenant_id AND v.id = i.service_request_version_id
		 WHERE v.tenant_id = $1 AND v.service_request_id = $2`,
		s.tenant, requestID).Scan(&quantity, &amount); err != nil {
		t.Fatalf("read the request line: %v", err)
	}
	if quantity != "2.000000" {
		t.Errorf("the request line asks for %s nights, want 2", quantity)
	}
	if amount != "1800.000000" {
		t.Errorf("the request line asks for %s, want the payer share of 1800", amount)
	}

	s.decideRequest(t, requestID, servicerequestdomain.StatusApproved)
	s.deliverDecision(t, requestID)

	final, err := s.svc.GetBooking(ctx, s.deskContext(), view.Booking.ID)
	if err != nil {
		t.Fatalf("read the confirmed booking: %v", err)
	}
	if final.Booking.Status != accommodationdomain.BookingConfirmed {
		t.Fatalf("status = %q, want CONFIRMED", final.Booking.Status)
	}
	if final.Booking.Nights != 3 {
		t.Errorf("nights = %d after the approval, want 3", final.Booking.Nights)
	}
	s.assertNightRowsMatch(t, view.Booking.ID, "after a partially covered approval")

	// The ledger moved by exactly two in total. Three would mean the confirmation reserved
	// a night the member was told they were paying for.
	available, reserved = s.accountBalances(t)
	if available != "0.000000" || reserved != "2.000000" {
		t.Errorf("after the approval: available = %q, reserved = %q; want 0 and 2",
			available, reserved)
	}

	// And the authorization promised two nights against the hold's own two-night row.
	var itemQuantity string
	var itemReservation uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT ai.approved_quantity::text, ai.entitlement_reservation_id
		  FROM service.authorization_item ai
		 WHERE ai.tenant_id = $1 AND ai.authorization_id = $2`,
		s.tenant, *final.Booking.AuthorizationID).Scan(&itemQuantity, &itemReservation); err != nil {
		t.Fatalf("read the authorization line: %v", err)
	}
	if itemQuantity != "2.000000" {
		t.Errorf("the authorization promised %s nights, want 2", itemQuantity)
	}
	if itemReservation != *view.Booking.EntitlementReservationID {
		t.Errorf("the authorization line holds reservation %s, want the hold's own %s",
			itemReservation, *view.Booking.EntitlementReservationID)
	}
	var reservations int
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM benefit.entitlement_reservation
		 WHERE tenant_id = $1 AND entitlement_account_id = $2`, s.tenant, s.account).
		Scan(&reservations); err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if reservations != 1 {
		t.Errorf("reservations on the account = %d, want exactly 1", reservations)
	}
}

// TestStayThePlanCarriesNoNightOfIsRefused is the one refusal left. A member whose plan does
// not reach this stay at all is told so rather than being allowed to hold a room they would
// pay for entirely and could not be entitled to.
func TestStayThePlanCarriesNoNightOfIsRefused(t *testing.T) {
	s := newServer(t)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	// The balance is spent. Everything else about the world is untouched, so the room is
	// free, the price resolves and the only thing missing is the entitlement.
	if _, err := s.h.Admin.Exec(ctx, `
		UPDATE benefit.entitlement_account
		   SET available_quantity = 0, consumed_quantity = total_granted
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.account); err != nil {
		t.Fatalf("spend the balance: %v", err)
	}

	_, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
	if !isError(err, application.ErrEntitlementInsufficient) {
		t.Fatalf("hold on an exhausted plan = %v, want ErrEntitlementInsufficient", err)
	}
	// Nothing was set aside on the way to that refusal.
	var bookings int
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM accommodation.booking WHERE tenant_id = $1`, s.tenant).
		Scan(&bookings); err != nil {
		t.Fatalf("count bookings: %v", err)
	}
	if bookings != 0 {
		t.Errorf("a refused hold left %d bookings behind", bookings)
	}
	_, held, _ := s.inventoryOf(t, s.roomType, checkIn)
	if held != 1 {
		t.Errorf("held = %d after a refused hold, want the fixture's own 1", held)
	}
}

// ---------------------------------------------------------------------------
// One live booking per person, room type and arrival
// ---------------------------------------------------------------------------

// TestOneLiveBookingPerPersonAndArrival proves the partial unique index under a concurrent
// double-submit, which is the case it exists for: two tabs, both reading "nothing yet", both
// passing any check a service could make, and the database refusing the second insert.
func TestOneLiveBookingPerPersonAndArrival(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 20)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	const attempts = 8
	errs := make([]error, attempts)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
			errs[i] = err
		}(i)
	}
	close(start)
	wg.Wait()

	succeeded, refused := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			succeeded++
		case isError(err, application.ErrBookingAlreadyLive):
			refused++
		default:
			t.Errorf("unexpected refusal: %v", err)
		}
	}
	if succeeded != 1 {
		t.Errorf("holds that succeeded = %d, want exactly 1", succeeded)
	}
	if refused != attempts-1 {
		t.Errorf("BOOKING_ALREADY_LIVE refusals = %d, want %d", refused, attempts-1)
	}

	var live int
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM accommodation.booking
		 WHERE tenant_id = $1 AND person_id = $2 AND room_type_id = $3
		   AND status IN ('HOLD','PENDING_APPROVAL','CONFIRMED','CHECKED_IN')`,
		s.tenant, s.person, s.roomType).Scan(&live); err != nil {
		t.Fatalf("count live bookings: %v", err)
	}
	if live != 1 {
		t.Errorf("live bookings = %d, want 1", live)
	}
}

// ---------------------------------------------------------------------------
// Visibility
// ---------------------------------------------------------------------------

// TestBookingVisibility is the rule of section 2.4 over HTTP: a member reads their own
// booking, a provider reads the ones at its own properties, and neither can see the other's.
func TestBookingVisibility(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	view, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	path := "/api/v1/accommodation/bookings/" + view.Booking.ID.String()

	rec := s.do(t, http.MethodGet, path, bookerPermissions, nil, s.memberHeaders()...)
	if rec.Code != http.StatusOK {
		t.Fatalf("the member reading their own booking = %d, want 200\nbody: %s",
			rec.Code, rec.Body.String())
	}
	got := decode[kapsorav1.Booking](t, rec)
	if got.Reference != view.Booking.Reference {
		t.Errorf("reference = %q, want %q", got.Reference, view.Booking.Reference)
	}

	// The provider that runs the hotel sees it.
	rec = s.do(t, http.MethodGet, path, bookerPermissions, nil, scopeHeader, s.providerOr.String())
	if rec.Code != http.StatusOK {
		t.Errorf("the property's own provider reading the booking = %d, want 200", rec.Code)
	}
	// The other provider does not, and is told it does not exist rather than that it may not.
	rec = s.do(t, http.MethodGet, path, bookerPermissions, nil, scopeHeader, s.otherOr.String())
	if rec.Code != http.StatusNotFound {
		t.Errorf("another provider reading the booking = %d, want 404", rec.Code)
	}
	// The list obeys the same boundary.
	rec = s.do(t, http.MethodGet, "/api/v1/accommodation/bookings", bookerPermissions, nil,
		scopeHeader, s.otherOr.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200", rec.Code)
	}
	page := decode[kapsorav1.BookingList](t, rec)
	if len(page.Items) != 0 {
		t.Errorf("another provider's list holds %d bookings, want none", len(page.Items))
	}

	// A second member, bound to a different person. This is the half of the boundary a test
	// that only reads the member's own booking never touches: without the person binding
	// narrowing every query, this account -- an ordinary member of the same tenant, holding
	// exactly the same grants -- would read, list, release and confirm somebody else's stay.
	neighbour := s.secondMember(t)
	other := []string{personHeader, neighbour.String()}

	rec = s.do(t, http.MethodGet, path, bookerPermissions, nil, other...)
	if rec.Code != http.StatusNotFound {
		t.Errorf("another member reading this booking = %d, want 404\nbody: %s",
			rec.Code, rec.Body.String())
	}
	rec = s.do(t, http.MethodGet, "/api/v1/accommodation/bookings", bookerPermissions, nil, other...)
	if rec.Code != http.StatusOK {
		t.Fatalf("another member listing = %d, want 200", rec.Code)
	}
	theirs := decode[kapsorav1.BookingList](t, rec)
	for _, item := range theirs.Items {
		if item.Id == view.Booking.ID {
			t.Error("another member's list holds this booking; the person binding is not " +
				"narrowing the query")
		}
	}
	if len(theirs.Items) != 0 {
		t.Errorf("another member's list holds %d bookings, want none of their own",
			len(theirs.Items))
	}
	// And the commands are 404 too, not 403: that this member is going to Antalya is not
	// their neighbour's business either.
	for _, command := range []string{"release", "confirm"} {
		rec = s.do(t, http.MethodPost, path+"/"+command, bookerPermissions, nil, other...)
		if rec.Code != http.StatusNotFound {
			t.Errorf("another member calling %s = %d, want 404\nbody: %s",
				command, rec.Code, rec.Body.String())
		}
	}
	// The booking is untouched by any of it.
	after, err := s.svc.GetBooking(ctx, s.deskContext(), view.Booking.ID)
	if err != nil {
		t.Fatalf("read the booking: %v", err)
	}
	if after.Booking.Status != accommodationdomain.BookingHold {
		t.Errorf("status = %q after a neighbour tried to move it, want HOLD", after.Booking.Status)
	}
}

// secondMember adds a person nobody has booked anything for, so a member account can be
// bound to somebody other than the one the fixture books as.
func (s *server) secondMember(t *testing.T) uuid.UUID {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var id uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Komsu', 'Yildiz', 'komsu yildiz') RETURNING id`, s.tenant).Scan(&id); err != nil {
		t.Fatalf("seed the second member: %v", err)
	}
	return id
}

// TestHoldOverHTTPRefusesAnotherPersonsStay is the person binding: a member holding a room
// for their neighbour is the thing the binding exists to stop.
func TestHoldOverHTTPRefusesAnotherPersonsStay(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)

	body := map[string]any{
		"roomTypeId": s.roomType.String(), "checkIn": checkIn, "checkOut": checkOut,
		"adults": 2, "personId": uuid.NewString(),
	}
	rec := s.do(t, http.MethodPost, "/api/v1/accommodation/holds", bookerPermissions, body,
		s.memberHeaders()...)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("holding for somebody else = %d, want 403\nbody: %s", rec.Code, rec.Body.String())
	}
}

// TestHoldRefusesANightWithNoAllotment is the "a missing row is not zero and not unlimited"
// rule, reached through a hold: the room type has capacity on two of the three nights, and
// the refusal names the night that has none.
func TestHoldRefusesANightWithNoAllotment(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	_, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.gapRoomType))
	var unavailable *application.RoomUnavailable
	if !asError(err, &unavailable) {
		t.Fatalf("hold on a room type with a gap = %v, want ROOM_UNAVAILABLE", err)
	}
	if got := unavailable.StayDate.Format(time.DateOnly); got != "2026-06-16" {
		t.Errorf("the refusal names %s, want the night with no allotment (2026-06-16)", got)
	}
	if unavailable.Allotted {
		t.Error("the refusal says the night is allotted; it has no row at all")
	}
}

// ---------------------------------------------------------------------------
// The crowd
// ---------------------------------------------------------------------------

// seedConcurrencyWorld creates one room type with `rooms` rooms on every night of the stay
// and `people` separate members, each with their own enrollment and their own three nights
// of entitlement.
//
// They have to be separate people. The partial unique index refuses a second live booking
// for one person, so five hundred attempts by one member would prove that index rather than
// the lock, and the oversell question would go unasked.
func (s *server) seedConcurrencyWorld(t *testing.T, people, rooms int) uuid.UUID {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()

	var roomType uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `
		INSERT INTO accommodation.room_type (tenant_id, property_id, code, name, max_adults,
		                                     max_children, max_occupancy, service_definition_id)
		VALUES ($1, $2, 'CONC', 'Yarış', 2, 2, 4, $3) RETURNING id`,
		s.tenant, s.property, s.definition).Scan(&roomType); err != nil {
		t.Fatalf("seed the contended room type: %v", err)
	}
	s.openAllotment(t, roomType, rooms)

	// One statement per layer rather than one round trip per member: five hundred people,
	// their memberships, their enrollments and their accounts, with the GRANT movement that
	// makes each balance real.
	rows, err := s.h.Admin.Query(ctx, `
		WITH people AS (
		  INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		  SELECT $1, 'Yarış', 'Üye ' || i, 'yaris uye ' || i
		    FROM generate_series(1, $2) AS i
		  RETURNING id
		), memberships AS (
		  INSERT INTO party.sponsor_membership (tenant_id, person_id, sponsor_tenant_organization_id,
		                                        membership_type, status, valid_period)
		  SELECT $1, p.id, $3, 'MEMBER', 'ACTIVE', daterange('2026-01-01', NULL, '[)')
		    FROM people p
		  RETURNING id, person_id
		), enrollments AS (
		  INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status, valid_period)
		  SELECT $1, m.id, $4, 'ACTIVE', daterange('2026-01-01', NULL, '[)')
		    FROM memberships m
		  RETURNING id, sponsor_membership_id
		), accounts AS (
		  INSERT INTO benefit.entitlement_account (tenant_id, enrollment_id, entitlement_definition_id,
		                                           benefit_period, total_granted, available_quantity)
		  SELECT $1, e.id, $5, daterange('2026-01-01','2027-01-01','[)'), 5, 5
		    FROM enrollments e
		  RETURNING id, enrollment_id
		), grants AS (
		  INSERT INTO benefit.entitlement_ledger (tenant_id, entitlement_account_id, movement_type,
		                                          effective_at, delta_total, delta_available,
		                                          reference_type, reference_id, idempotency_key)
		  SELECT $1, a.id, 'GRANT', clock_timestamp(), 5, 5, 'ENROLLMENT', a.enrollment_id,
		         'grant:crowd:' || a.id
		    FROM accounts a
		  RETURNING 1
		)
		SELECT m.person_id FROM memberships m
		 WHERE (SELECT count(*) FROM grants) IS NOT NULL
		 ORDER BY m.person_id`,
		s.tenant, people, s.sponsor, s.plan, s.entitlementDefinition)
	if err != nil {
		t.Fatalf("seed the crowd: %v", err)
	}
	defer rows.Close()
	s.crowd = nil
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan a member of the crowd: %v", err)
		}
		s.crowd = append(s.crowd, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("seed the crowd: %v", err)
	}
	if len(s.crowd) != people {
		t.Fatalf("seeded %d members, want %d", len(s.crowd), people)
	}
	return roomType
}
