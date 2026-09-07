// What happens after the promise (WP-I6-03), driven against a real PostgreSQL with the real
// modules behind it: the real ledger, the real authorization module, the real voucher digest
// and the real document pipeline's tables. Nothing below is stubbed, because every one of
// those is a link in the chain a stub would hide.
//
// Every test here is written to go red the moment the thing it guards is removed. The ones
// that matter most, and what breaks them:
//
//   - TestCancellationIsJudgedByTheFrozenPolicy fails if the cancellation reads today's
//     contract instead of the booking's own snapshot: the fee becomes zero and the penalty
//     becomes three.
//   - TestPenalisedCancellationConsumesAndReleasesOnce fails if the penalty is released
//     instead of consumed, if the order of the two movements is swapped, or if running the
//     command twice moves the ledger twice.
//   - TestCheckInNeedsThisBookingsTokenInsideTheWindow fails if the window check is skipped
//     or if a token for another booking is accepted.
//   - TestEarlyCheckOutReleasesTheNightsNobodySlept fails if the release is dropped, and
//     TestOverStayFlagsAndConsumesNothingExtra fails if the check-out consumes past what was
//     authorized.
//   - TestNoShowCostsNothingUntilASecondPersonConfirmsIt fails if the reporter can confirm
//     their own report, if evidence stops being required, or if a rejection closes the
//     booking.
//   - TestWaitlistOffersInQueueOrder fails if the sweep ignores priority, and
//     TestUnacceptedOfferGoesToTheBackOfTheQueue fails if an expired offer does not return
//     to WAITING behind the people who were already waiting.
package accommodationhttp_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/accommodation/application"
	accommodationdomain "github.com/celikbros/kapsora/internal/accommodation/domain"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	"github.com/celikbros/kapsora/internal/identity"
	servicerequestdomain "github.com/celikbros/kapsora/internal/servicerequest/domain"
)

// The moments this file is written around, all on the fixture's own stay (15-18 June 2026 at
// a hotel in Europe/Istanbul, so the arrival day starts at 2026-06-14T21:00Z).
//
//   - the free window of the seeded terms is 48 hours, so it closes at 2026-06-12T21:00Z;
//   - the check-in window is [arrival - 6h, arrival + 24h] = [06-14T15:00Z, 06-15T21:00Z];
//   - the voucher itself is valid from 2026-06-15T00:00Z to 2026-06-19T00:00Z.
const (
	insideFreeWindow  = "2026-06-10T09:00:00Z"
	afterFreeWindow   = "2026-06-14T09:00:00Z"
	beforeCheckInOpen = "2026-06-13T09:00:00Z"
	atCheckIn         = "2026-06-15T10:00:00Z"
	afterOneNight     = "2026-06-16T09:00:00Z"
	afterTheWholeStay = "2026-06-18T09:00:00Z"
	afterAnOverStay   = "2026-06-20T09:00:00Z"
	// afterCheckInCloses is past the tenant's 24 late hours, which is the earliest a
	// provider may say that nobody came.
	afterCheckInCloses = "2026-06-16T08:00:00Z"
)

// ---------------------------------------------------------------------------
// Fixture helpers
// ---------------------------------------------------------------------------

// providerContext is a clerk at the hotel: the booking grant on an ORGANIZATION scope, which
// is what tells the provider side from the payer side everywhere in this vertical.
func (s *server) providerContext(actor uuid.UUID) identity.RequestContext {
	rc := s.deskContext()
	rc.Principal.ActorID = actor
	rc.Scopes = append(rc.Scopes, identity.Scope{
		Type: "ORGANIZATION",
		ID:   uuid.NullUUID{UUID: s.providerOr, Valid: true},
	})
	return rc
}

// payerContext is a reviewer on the payer's side: the same grant, tenant-wide and bound to no
// provider. The two sides are told apart by scope and by nothing else, which is exactly what
// the maker-checker test leans on.
func (s *server) payerContext(actor uuid.UUID) identity.RequestContext {
	rc := s.deskContext()
	rc.Principal.ActorID = actor
	return rc
}

// confirmBooking runs the whole WP-I6-02 path -- hold, confirm, reviewer approves, outbox
// delivers -- and returns the CONFIRMED booking. Everything in this file starts from one.
func (s *server) confirmBooking(t *testing.T, person, roomType uuid.UUID) application.BookingView {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	held, err := s.svc.CreateHold(ctx, s.contextFor(person), s.holdInput(person, roomType))
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	confirmed, err := s.svc.ConfirmBooking(ctx, s.contextFor(person), held.Booking.ID)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if confirmed.Booking.ServiceRequestID == nil {
		t.Fatal("confirming raised no reservation request")
	}
	requestID := *confirmed.Booking.ServiceRequestID
	s.decideRequest(t, requestID, servicerequestdomain.StatusApproved)
	s.deliverDecision(t, requestID)

	final, err := s.svc.GetBooking(ctx, s.deskContext(), held.Booking.ID)
	if err != nil {
		t.Fatalf("read the confirmed booking: %v", err)
	}
	if final.Booking.Status != accommodationdomain.BookingConfirmed {
		t.Fatalf("status = %q, want CONFIRMED", final.Booking.Status)
	}
	return final
}

// rewriteLodgingTerms changes what the contract says today. It is the mutation the snapshot
// test exists for: after this, the booking's frozen policy and the live contract disagree,
// and only one of them may decide a cancellation.
func (s *server) rewriteLodgingTerms(t *testing.T, freeHours, penaltyNights int) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if _, err := s.h.Admin.Exec(ctx, `
		UPDATE contract.contract_version SET status = 'DRAFT'
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.contractVersion); err != nil {
		t.Fatalf("unpublish the version: %v", err)
	}
	if _, err := s.h.Admin.Exec(ctx, `
		UPDATE contract.lodging_terms
		   SET free_cancellation_hours_before = $3, penalty_nights = $4
		 WHERE tenant_id = $1 AND contract_version_id = $2`,
		s.tenant, s.contractVersion, freeHours, penaltyNights); err != nil {
		t.Fatalf("rewrite the lodging terms: %v", err)
	}
	if _, err := s.h.Admin.Exec(ctx, `
		UPDATE contract.contract_version SET status = 'PUBLISHED'
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, s.contractVersion); err != nil {
		t.Fatalf("republish the version: %v", err)
	}
}

// linkCleanBookingDocument seeds the evidence a no-show report needs: an object the scanner
// cleared, linked to the booking under WP-I4-04's BOOKING aggregate. It is written straight
// into the database because what is under test is the gate, not the upload pipeline.
func (s *server) linkCleanBookingDocument(t *testing.T, bookingID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	digest := make([]byte, 32)
	copy(digest, bookingID[:])
	var objectID uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `
		INSERT INTO document.object (tenant_id, object_key, bucket, classification,
		                             original_filename, content_type, byte_size, sha256,
		                             scan_status, owner_tenant_organization_id)
		VALUES ($1, 'secure/' || $2::text, 'secure', 'INTERNAL', 'gelmedi.pdf',
		        'application/pdf', 2048, $3, 'CLEAN', $4)
		RETURNING id`, s.tenant, bookingID.String(), digest, s.providerOr).Scan(&objectID); err != nil {
		t.Fatalf("seed the evidence object: %v", err)
	}
	s.h.AdminExec(`
		INSERT INTO document.link (tenant_id, object_id, aggregate_type, aggregate_id,
		                           document_type_code)
		VALUES ($1, $2, 'BOOKING', $3, 'NO_SHOW_EVIDENCE')`, s.tenant, objectID, bookingID)
	return objectID
}

// assertConservation is WP-I4-02's invariant, asserted after every flow in this file: what
// was granted is always somewhere -- available, reserved, consumed or expired -- and the
// ledger sums to the same vector the account carries. It is the assertion that would catch a
// penalty this package consumed twice, or a release it took twice.
func (s *server) assertConservation(t *testing.T, where string) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var total, available, reserved, consumed, expired string
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT total_granted::text, available_quantity::text, reserved_quantity::text,
		       consumed_quantity::text, expired_quantity::text
		  FROM benefit.entitlement_account WHERE tenant_id = $1 AND id = $2`,
		s.tenant, s.account).Scan(&total, &available, &reserved, &consumed, &expired); err != nil {
		t.Fatalf("%s: read balances: %v", where, err)
	}
	sum := benefitdomain.MustQuantity(available).
		Add(benefitdomain.MustQuantity(reserved)).
		Add(benefitdomain.MustQuantity(consumed)).
		Add(benefitdomain.MustQuantity(expired))
	if sum.Cmp(benefitdomain.MustQuantity(total)) != 0 {
		t.Fatalf("%s: available+reserved+consumed+expired = %s, want total_granted %s",
			where, sum.String(), total)
	}

	var ledgerTotal, ledgerAvailable, ledgerReserved, ledgerConsumed, ledgerExpired string
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT coalesce(sum(delta_total), 0)::text, coalesce(sum(delta_available), 0)::text,
		       coalesce(sum(delta_reserved), 0)::text, coalesce(sum(delta_consumed), 0)::text,
		       coalesce(sum(delta_expired), 0)::text
		  FROM benefit.entitlement_ledger WHERE entitlement_account_id = $1`, s.account).
		Scan(&ledgerTotal, &ledgerAvailable, &ledgerReserved, &ledgerConsumed, &ledgerExpired); err != nil {
		t.Fatalf("%s: read ledger sum: %v", where, err)
	}
	for _, pair := range [][3]string{
		{"total", total, ledgerTotal}, {"available", available, ledgerAvailable},
		{"reserved", reserved, ledgerReserved}, {"consumed", consumed, ledgerConsumed},
		{"expired", expired, ledgerExpired},
	} {
		if benefitdomain.MustQuantity(pair[1]).Cmp(benefitdomain.MustQuantity(pair[2])) != 0 {
			t.Fatalf("%s: the account's %s is %s and the ledger sums to %s",
				where, pair[0], pair[1], pair[2])
		}
	}
}

// balances is the two figures most assertions here are about.
func (s *server) balances(t *testing.T) (available, reserved, consumed string) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT available_quantity::text, reserved_quantity::text, consumed_quantity::text
		  FROM benefit.entitlement_account WHERE tenant_id = $1 AND id = $2`,
		s.tenant, s.account).Scan(&available, &reserved, &consumed); err != nil {
		t.Fatalf("read balances: %v", err)
	}
	return available, reserved, consumed
}

// cancellationRow reads the written cancellation straight from the table, because the whole
// claim of this package is about what that row says it was judged by.
func (s *server) cancellationRow(t *testing.T, bookingID uuid.UUID) (
	free bool, penaltyNights, releasedNights int, fee, payer, member string, policy []byte,
) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT free, penalty_nights, released_nights, fee_amount::text, payer_fee::text,
		       member_fee::text, policy_snapshot
		  FROM accommodation.cancellation WHERE tenant_id = $1 AND booking_id = $2`,
		s.tenant, bookingID).Scan(&free, &penaltyNights, &releasedNights, &fee, &payer,
		&member, &policy); err != nil {
		t.Fatalf("read the cancellation row: %v", err)
	}
	return free, penaltyNights, releasedNights, fee, payer, member, policy
}

// ---------------------------------------------------------------------------
// Cancellation
// ---------------------------------------------------------------------------

// TestCancellationIsJudgedByTheFrozenPolicy is the first acceptance criterion of the work
// package, and the reason `policy_snapshot` exists at all.
//
// The booking is confirmed under terms of 48 free hours and a one-night penalty. The contract
// is then rewritten to nothing free and a three-night penalty -- a change a hotel might make
// any morning -- and the member cancels at a moment that is *outside* the old free window and
// *inside* the new one. Under the snapshot they owe one night; under today's contract they
// would owe nothing. The test asserts the first, so a cancellation that read the live contract
// answers "free, zero" and goes red on both figures.
//
// The written row is checked too, because the row is the evidence: it carries the policy it
// was judged by, so the fee can be explained two years later without the contract being
// re-resolved.
func TestCancellationIsJudgedByTheFrozenPolicy(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	s.putLodgingTerms(t)
	s.clock.At(t, insideFreeWindow)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	booking := s.confirmBooking(t, s.person, s.roomType)

	// The contract changes. Nothing about this booking may change with it.
	s.rewriteLodgingTerms(t, 0, 3)
	s.clock.At(t, afterFreeWindow)

	preview, err := s.svc.PreviewCancellation(ctx, s.memberContext(), booking.Booking.ID)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Quote.Free {
		t.Fatal("the preview says the cancellation is free; it is judging today's contract " +
			"(0 free hours) instead of the 48 the booking was confirmed under")
	}
	if preview.Quote.PenaltyNights != 1 {
		t.Errorf("preview penaltyNights = %d, want the snapshot's 1 and not today's 3",
			preview.Quote.PenaltyNights)
	}
	if preview.Quote.FeeAmount != "1000" {
		t.Errorf("preview feeAmount = %q, want 1000 (one night of the stay)", preview.Quote.FeeAmount)
	}

	result, err := s.svc.CancelBooking(ctx, s.memberContext(), booking.Booking.ID, "")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	// The command answers exactly what the preview did, because both run the same
	// computation on the same document.
	if result.Quote != preview.Quote {
		t.Errorf("the command answered %+v and the preview answered %+v; a member shown one "+
			"figure and charged another has been misled", result.Quote, preview.Quote)
	}

	free, penaltyNights, releasedNights, fee, payer, member, policy := s.cancellationRow(t, booking.Booking.ID)
	if free || penaltyNights != 1 || releasedNights != 2 || fee != "1000.000000" {
		t.Errorf("cancellation row: free=%t penalty=%d released=%d fee=%q; "+
			"want false / 1 / 2 / 1000", free, penaltyNights, releasedNights, fee)
	}
	if payer != "900.000000" || member != "100.000000" {
		t.Errorf("cancellation row: payer=%q member=%q, want 900 and 100", payer, member)
	}
	// payer + member = fee, exactly. The row has a CHECK saying so; this says it in the
	// arithmetic the service produced rather than in the one the database refused.
	if benefitdomain.MustQuantity(payer).Add(benefitdomain.MustQuantity(member)).
		Cmp(benefitdomain.MustQuantity(fee)) != 0 {
		t.Errorf("payer %s + member %s does not equal fee %s", payer, member, fee)
	}

	// The row proves what it was judged by.
	var judged struct {
		FreeHours     int  `json:"freeCancellationHoursBefore"`
		PenaltyNights *int `json:"penaltyNights"`
	}
	if err := json.Unmarshal(policy, &judged); err != nil {
		t.Fatalf("decode the policy on the cancellation row: %v", err)
	}
	if judged.FreeHours != 48 || judged.PenaltyNights == nil || *judged.PenaltyNights != 1 {
		t.Errorf("the cancellation row carries freeHours=%d penaltyNights=%v; "+
			"want the 48 and 1 the booking was confirmed under", judged.FreeHours, judged.PenaltyNights)
	}
	s.assertConservation(t, "after a penalised cancellation")
}

// TestFreeCancellationReleasesEverythingAndChargesNothing is the other half of the same rule:
// inside the window the member agreed to, everything the plan holds goes back and the fee is
// zero. The row's own CHECK refuses a free cancellation that charges; this asserts the
// service never asks it to.
func TestFreeCancellationReleasesEverythingAndChargesNothing(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	s.putLodgingTerms(t)
	s.clock.At(t, insideFreeWindow)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	booking := s.confirmBooking(t, s.person, s.roomType)
	_, reservedAfterConfirm, _ := s.balances(t)
	if reservedAfterConfirm != "3.000000" {
		t.Fatalf("after the confirmation: reserved = %q, want 3", reservedAfterConfirm)
	}

	result, err := s.svc.CancelBooking(ctx, s.memberContext(), booking.Booking.ID, "")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if !result.Quote.Free || result.Quote.FeeAmount != "0" || result.Quote.PenaltyNights != 0 {
		t.Errorf("quote = %+v, want a free cancellation costing nothing", result.Quote)
	}
	if result.Quote.ReleasedNights != 3 {
		t.Errorf("releasedNights = %d, want all three", result.Quote.ReleasedNights)
	}
	available, reserved, consumed := s.balances(t)
	if available != "12.000000" || reserved != "0.000000" || consumed != "0.000000" {
		t.Errorf("after a free cancellation: available=%q reserved=%q consumed=%q; want 12/0/0",
			available, reserved, consumed)
	}
	// The room goes back to the allotment on every night.
	for _, day := range []string{checkIn, "2026-06-16", lastNight} {
		_, _, confirmedRooms := s.inventoryOf(t, s.roomType, day)
		if confirmedRooms != 0 {
			t.Errorf("%s: confirmed = %d after the cancellation, want 0", day, confirmedRooms)
		}
	}
	s.assertConservation(t, "after a free cancellation")
}

// TestPenalisedCancellationConsumesAndReleasesOnce is the ledger half of the package.
//
// Outside the free window the plan pays for the night the member did not use: that night is
// **consumed**, the other two are **released**, and running the command a second time changes
// nothing at all. A release that ran before the consume would hand everything back and leave
// the ledger disagreeing with the fee on the row; a second run that moved anything would mean
// a member could be charged twice by a retried request.
func TestPenalisedCancellationConsumesAndReleasesOnce(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	s.putLodgingTerms(t)
	s.clock.At(t, insideFreeWindow)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	booking := s.confirmBooking(t, s.person, s.roomType)
	s.clock.At(t, afterFreeWindow)

	if _, err := s.svc.CancelBooking(ctx, s.memberContext(), booking.Booking.ID, ""); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	available, reserved, consumed := s.balances(t)
	if available != "11.000000" || reserved != "0.000000" || consumed != "1.000000" {
		t.Errorf("after the penalised cancellation: available=%q reserved=%q consumed=%q; "+
			"want 11/0/1 — the penalty is consumed, not released", available, reserved, consumed)
	}
	s.assertConservation(t, "after the penalised cancellation")

	// The voucher is retired with the stay: a cancelled booking whose code still redeems is
	// a room a guest can walk into.
	var liveVouchers int
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM service.voucher
		 WHERE tenant_id = $1 AND authorization_id = $2 AND status = 'ISSUED'`,
		s.tenant, *booking.Booking.AuthorizationID).Scan(&liveVouchers); err != nil {
		t.Fatalf("count live vouchers: %v", err)
	}
	if liveVouchers != 0 {
		t.Errorf("%d vouchers still work on a cancelled booking, want none", liveVouchers)
	}

	// Twice is once.
	if _, err := s.svc.CancelBooking(ctx, s.memberContext(), booking.Booking.ID, ""); err == nil {
		t.Error("cancelling a cancelled booking succeeded; it would charge the member twice")
	}
	availableTwice, reservedTwice, consumedTwice := s.balances(t)
	if availableTwice != available || reservedTwice != reserved || consumedTwice != consumed {
		t.Errorf("the second cancellation moved the ledger: available %q->%q, reserved %q->%q, "+
			"consumed %q->%q", available, availableTwice, reserved, reservedTwice,
			consumed, consumedTwice)
	}
	var rows int
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM accommodation.cancellation WHERE tenant_id = $1 AND booking_id = $2`,
		s.tenant, booking.Booking.ID).Scan(&rows); err != nil {
		t.Fatalf("count cancellations: %v", err)
	}
	if rows != 1 {
		t.Errorf("cancellation rows = %d, want exactly 1", rows)
	}
	s.assertConservation(t, "after cancelling twice")
}

// TestCancellationIsRefusedAfterCheckIn is the boundary of the command: a guest who has
// arrived does not cancel, they check out, and the two have entirely different arithmetic
// behind them.
func TestCancellationIsRefusedAfterCheckIn(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	s.putLodgingTerms(t)
	s.clock.At(t, insideFreeWindow)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	booking := s.confirmBooking(t, s.person, s.roomType)
	issued, err := s.svc.IssueBookingVoucher(ctx, s.memberContext(), booking.Booking.ID)
	if err != nil {
		t.Fatalf("issue the voucher: %v", err)
	}
	s.clock.At(t, atCheckIn)
	if _, err := s.svc.CheckInBooking(ctx, s.deskContext(), booking.Booking.ID,
		application.CheckInInput{Token: issued.Token}); err != nil {
		t.Fatalf("check in: %v", err)
	}

	_, err = s.svc.CancelBooking(ctx, s.memberContext(), booking.Booking.ID, "")
	if !isError(err, application.ErrCancellationTooLate) {
		t.Errorf("cancelling a checked-in stay answered %v, want BOOKING_CANCELLATION_TOO_LATE", err)
	}
}

// ---------------------------------------------------------------------------
// Check-in
// ---------------------------------------------------------------------------

// TestCheckInNeedsThisBookingsTokenInsideTheWindow is three refusals and one success, and
// each of them is a different thing going wrong at a hotel desk.
//
// The right token inside the window checks the guest in and spends the code. A token that is
// perfectly valid but belongs to somebody else's stay is **not found**, deliberately: a
// refusal that could tell a real code for another booking apart from one that does not exist
// would be an oracle a stolen list could be tested against. A code the member rotated is
// refused because it was withdrawn. And a check-in outside the property's own hours is
// refused with both ends of the window, because a clerk told only "no" has been told nothing.
func TestCheckInNeedsThisBookingsTokenInsideTheWindow(t *testing.T) {
	s := newServer(t)
	roomType := s.seedConcurrencyWorld(t, 2, 4)
	s.putLodgingTerms(t)
	s.clock.At(t, insideFreeWindow)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	mine := s.confirmBooking(t, s.crowd[0], roomType)
	theirs := s.confirmBooking(t, s.crowd[1], roomType)

	myVoucher, err := s.svc.IssueBookingVoucher(ctx, s.contextFor(s.crowd[0]), mine.Booking.ID)
	if err != nil {
		t.Fatalf("issue my voucher: %v", err)
	}
	theirVoucher, err := s.svc.IssueBookingVoucher(ctx, s.contextFor(s.crowd[1]), theirs.Booking.ID)
	if err != nil {
		t.Fatalf("issue their voucher: %v", err)
	}

	// Too early: the window has not opened. The refusal names when it does.
	s.clock.At(t, beforeCheckInOpen)
	_, err = s.svc.CheckInBooking(ctx, s.deskContext(), mine.Booking.ID,
		application.CheckInInput{Token: myVoucher.Token})
	var window *application.CheckInWindowClosed
	if !asError(err, &window) {
		t.Fatalf("checking in a day early answered %v, want the window refusal", err)
	}
	if !window.OpensAt.Equal(mustMoment(t, "2026-06-14T15:00:00Z")) ||
		!window.ClosesAt.Equal(mustMoment(t, "2026-06-15T21:00:00Z")) {
		t.Errorf("the window refusal names [%s, %s]; want [2026-06-14T15:00Z, 2026-06-15T21:00Z] "+
			"— six hours before and twenty-four hours after the arrival day in %s",
			window.OpensAt.Format(time.RFC3339), window.ClosesAt.Format(time.RFC3339),
			window.TimeZone)
	}

	// Inside the window, with somebody else's perfectly valid code.
	s.clock.At(t, atCheckIn)
	_, err = s.svc.CheckInBooking(ctx, s.deskContext(), mine.Booking.ID,
		application.CheckInInput{Token: theirVoucher.Token})
	if !isError(err, application.ErrVoucherNotFound) {
		t.Errorf("checking in with another booking's token answered %v, want not-found — "+
			"anything else tells a desk that the code exists", err)
	}

	// A rotated code. The member asked for a new one, so the old digest stopped working in
	// the same transaction the new one started in.
	rotated, err := s.svc.IssueBookingVoucher(ctx, s.contextFor(s.crowd[0]), mine.Booking.ID)
	if err != nil {
		t.Fatalf("rotate my voucher: %v", err)
	}
	_, err = s.svc.CheckInBooking(ctx, s.deskContext(), mine.Booking.ID,
		application.CheckInInput{Token: myVoucher.Token})
	if !isError(err, application.ErrVoucherRevoked) {
		t.Errorf("checking in with a rotated token answered %v, want the withdrawn refusal", err)
	}

	// And the code the member is actually holding.
	checkedIn, err := s.svc.CheckInBooking(ctx, s.deskContext(), mine.Booking.ID,
		application.CheckInInput{Token: rotated.Token})
	if err != nil {
		t.Fatalf("check in with the live token: %v", err)
	}
	if checkedIn.Booking.Status != accommodationdomain.BookingCheckedIn {
		t.Errorf("status = %q, want CHECKED_IN", checkedIn.Booking.Status)
	}
	if checkedIn.Booking.CheckedInAt == nil {
		t.Error("a checked-in booking with no arrival moment")
	}
	// The code is spent: a second guest cannot walk in on it.
	if _, err := s.svc.CheckInBooking(ctx, s.deskContext(), mine.Booking.ID,
		application.CheckInInput{Token: rotated.Token}); err == nil {
		t.Error("the same token checked in twice")
	}
	s.assertTokenAppearsNowhere(t, rotated.Token)
}

// ---------------------------------------------------------------------------
// Check-out
// ---------------------------------------------------------------------------

// TestEarlyCheckOutReleasesTheNightsNobodySlept is the discharge of this vertical.
//
// A guest booked for three nights leaves after one. One night is consumed, two are released
// across the hold the booking stands on, and the rooms of the two nights nobody slept in go
// back to the allotment where the search can sell them again. The night that *was* used stays
// taken, because the room really was occupied on it.
func TestEarlyCheckOutReleasesTheNightsNobodySlept(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	s.putLodgingTerms(t)
	s.clock.At(t, insideFreeWindow)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	booking := s.confirmBooking(t, s.person, s.roomType)
	issued, err := s.svc.IssueBookingVoucher(ctx, s.memberContext(), booking.Booking.ID)
	if err != nil {
		t.Fatalf("issue the voucher: %v", err)
	}
	s.clock.At(t, atCheckIn)
	if _, err := s.svc.CheckInBooking(ctx, s.deskContext(), booking.Booking.ID,
		application.CheckInInput{Token: issued.Token}); err != nil {
		t.Fatalf("check in: %v", err)
	}

	s.clock.At(t, afterOneNight)
	out, err := s.svc.CheckOutBooking(ctx, s.deskContext(), booking.Booking.ID,
		application.CheckOutInput{})
	if err != nil {
		t.Fatalf("check out: %v", err)
	}
	if out.Booking.Status != accommodationdomain.BookingCompleted {
		t.Errorf("status = %q, want COMPLETED", out.Booking.Status)
	}
	if out.Booking.ActualNights == nil || *out.Booking.ActualNights != 1 {
		t.Fatalf("actualNights = %v, want the single night the guest slept", out.Booking.ActualNights)
	}
	if out.Booking.OverBooking {
		t.Error("a stay shorter than the booking is flagged as an over-stay")
	}

	available, reserved, consumed := s.balances(t)
	if available != "11.000000" || reserved != "0.000000" || consumed != "1.000000" {
		t.Errorf("after the early check-out: available=%q reserved=%q consumed=%q; want 11/0/1 "+
			"— one night used and two given back", available, reserved, consumed)
	}
	s.assertConservation(t, "after an early check-out")

	// The room comes back on the nights nobody slept, and not on the one somebody did.
	_, _, firstNight := s.inventoryOf(t, s.roomType, checkIn)
	if firstNight != 1 {
		t.Errorf("%s: confirmed = %d, want the room the guest actually used", checkIn, firstNight)
	}
	for _, day := range []string{"2026-06-16", lastNight} {
		_, _, confirmedRooms := s.inventoryOf(t, s.roomType, day)
		if confirmedRooms != 0 {
			t.Errorf("%s: confirmed = %d after an early check-out, want the room back", day, confirmedRooms)
		}
	}

	// A second check-out finds nothing to do and gives nothing back twice.
	if _, err := s.svc.CheckOutBooking(ctx, s.deskContext(), booking.Booking.ID,
		application.CheckOutInput{}); err == nil {
		t.Error("checking out a completed stay succeeded")
	}
	availableTwice, _, consumedTwice := s.balances(t)
	if availableTwice != available || consumedTwice != consumed {
		t.Errorf("a second check-out moved the ledger: available %q->%q, consumed %q->%q",
			available, availableTwice, consumed, consumedTwice)
	}
}

// TestOverStayFlagsAndConsumesNothingExtra is the other end of the same command.
//
// A guest who stayed five nights on a three-night promise has already had the extra two: the
// check-out cannot refuse, so it records what happened, consumes exactly what was authorized
// and sets the flag M7's claim raises as an exception. A check-out that consumed five would
// take entitlement nobody ever held, and the account would go somewhere the ledger cannot
// explain.
func TestOverStayFlagsAndConsumesNothingExtra(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	s.putLodgingTerms(t)
	s.clock.At(t, insideFreeWindow)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	booking := s.confirmBooking(t, s.person, s.roomType)
	issued, err := s.svc.IssueBookingVoucher(ctx, s.memberContext(), booking.Booking.ID)
	if err != nil {
		t.Fatalf("issue the voucher: %v", err)
	}
	s.clock.At(t, atCheckIn)
	if _, err := s.svc.CheckInBooking(ctx, s.deskContext(), booking.Booking.ID,
		application.CheckInInput{Token: issued.Token}); err != nil {
		t.Fatalf("check in: %v", err)
	}

	s.clock.At(t, afterAnOverStay)
	out, err := s.svc.CheckOutBooking(ctx, s.deskContext(), booking.Booking.ID,
		application.CheckOutInput{})
	if err != nil {
		t.Fatalf("check out: %v", err)
	}
	if out.Booking.ActualNights == nil || *out.Booking.ActualNights != 5 {
		t.Fatalf("actualNights = %v, want the five nights the guest actually stayed",
			out.Booking.ActualNights)
	}
	if !out.Booking.OverBooking {
		t.Error("a stay that ran two nights past what was authorized is not flagged")
	}
	available, reserved, consumed := s.balances(t)
	if consumed != "3.000000" {
		t.Errorf("consumed = %q after a five-night stay on a three-night promise; want 3 — "+
			"nothing beyond what was authorized may ever be taken", consumed)
	}
	if available != "9.000000" || reserved != "0.000000" {
		t.Errorf("after the over-stay: available=%q reserved=%q; want 9/0", available, reserved)
	}
	s.assertConservation(t, "after an over-stay")
}

// TestCheckOutOnTheDayOfArrivalIsStillOneNight is the floor under the night count: a guest
// who arrives and leaves the same afternoon has still used a room the hotel cannot sell to
// anybody else that night.
func TestCheckOutOnTheDayOfArrivalIsStillOneNight(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	s.putLodgingTerms(t)
	s.clock.At(t, insideFreeWindow)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	booking := s.confirmBooking(t, s.person, s.roomType)
	issued, err := s.svc.IssueBookingVoucher(ctx, s.memberContext(), booking.Booking.ID)
	if err != nil {
		t.Fatalf("issue the voucher: %v", err)
	}
	s.clock.At(t, atCheckIn)
	if _, err := s.svc.CheckInBooking(ctx, s.deskContext(), booking.Booking.ID,
		application.CheckInInput{Token: issued.Token}); err != nil {
		t.Fatalf("check in: %v", err)
	}
	// Half past four in the afternoon of the arrival day, Istanbul time.
	s.clock.At(t, "2026-06-15T13:30:00Z")
	out, err := s.svc.CheckOutBooking(ctx, s.deskContext(), booking.Booking.ID,
		application.CheckOutInput{})
	if err != nil {
		t.Fatalf("check out: %v", err)
	}
	if out.Booking.ActualNights == nil || *out.Booking.ActualNights != 1 {
		t.Errorf("actualNights = %v, want 1 — a room used for an afternoon is a night the "+
			"hotel cannot sell", out.Booking.ActualNights)
	}
	s.assertConservation(t, "after a same-day check-out")
}

// ---------------------------------------------------------------------------
// No-show
// ---------------------------------------------------------------------------

// TestNoShowCostsNothingUntilASecondPersonConfirmsIt is the acceptance criterion of the
// no-show half, and it is four assertions rather than one because there are four ways this
// can go wrong at a member's expense.
//
// A report with no evidence is refused. A report changes nothing: the booking is still
// CONFIRMED and not one night has moved. The provider who filed it cannot confirm it. And
// only when somebody else does is the room freed and the plan drawn down.
func TestNoShowCostsNothingUntilASecondPersonConfirmsIt(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	s.putLodgingTerms(t)
	s.clock.At(t, insideFreeWindow)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	booking := s.confirmBooking(t, s.person, s.roomType)
	clerk := s.h.CreateActor("hotel-desk", "Otel Resepsiyon")
	reviewer := s.h.CreateActor("payer-reviewer", "Ödeyici Değerlendirici")

	// Too early: the guest may still be on their way.
	s.clock.At(t, atCheckIn)
	if _, err := s.svc.ReportNoShow(ctx, s.providerContext(clerk), booking.Booking.ID,
		application.ReportNoShowInput{}); !isError(err, application.ErrNoShowTooEarly) {
		t.Errorf("reporting during the check-in window answered %v, want NO_SHOW_TOO_EARLY", err)
	}

	// After the window closes, but with nothing behind the claim.
	s.clock.At(t, afterCheckInCloses)
	if _, err := s.svc.ReportNoShow(ctx, s.providerContext(clerk), booking.Booking.ID,
		application.ReportNoShowInput{}); !isError(err, application.ErrNoShowEvidenceRequired) {
		t.Errorf("reporting with no document answered %v, want NO_SHOW_EVIDENCE_REQUIRED", err)
	}

	evidence := s.linkCleanBookingDocument(t, booking.Booking.ID)
	reported, err := s.svc.ReportNoShow(ctx, s.providerContext(clerk), booking.Booking.ID,
		application.ReportNoShowInput{EvidenceDocumentID: &evidence})
	if err != nil {
		t.Fatalf("report the no-show: %v", err)
	}
	if reported.Report.Status != application.NoShowReported {
		t.Errorf("report status = %q, want REPORTED", reported.Report.Status)
	}
	// 100 % of the member's own share of the stay.
	if reported.Report.AssessedFeeAmount != "300.000000" ||
		reported.Report.MemberAmount != "300.000000" || reported.Report.PayerAmount != "0.000000" {
		t.Errorf("assessed = %q (payer %q, member %q); want 300 / 0 / 300",
			reported.Report.AssessedFeeAmount, reported.Report.PayerAmount,
			reported.Report.MemberAmount)
	}

	// **Nothing has happened to the member.** This is the whole rule.
	if reported.Booking.Booking.Status != accommodationdomain.BookingConfirmed {
		t.Errorf("the booking is %q after a report; want CONFIRMED until somebody reviews it",
			reported.Booking.Booking.Status)
	}
	available, reserved, consumed := s.balances(t)
	if available != "9.000000" || reserved != "3.000000" || consumed != "0.000000" {
		t.Errorf("a report moved the plan: available=%q reserved=%q consumed=%q; want 9/3/0",
			available, reserved, consumed)
	}
	s.assertConservation(t, "after a no-show report")

	// The reporter cannot confirm their own claim.
	if _, err := s.svc.ReviewNoShow(ctx, s.providerContext(clerk), booking.Booking.ID,
		application.ReviewNoShowInput{Status: application.NoShowConfirmed}); !isError(
		err, application.ErrNoShowSameActor) {
		t.Fatalf("the reporter confirmed their own no-show (%v); a fee may only follow a "+
			"second pair of eyes", err)
	}

	// Somebody else does.
	reviewed, err := s.svc.ReviewNoShow(ctx, s.payerContext(reviewer), booking.Booking.ID,
		application.ReviewNoShowInput{Status: application.NoShowConfirmed})
	if err != nil {
		t.Fatalf("review the no-show: %v", err)
	}
	if reviewed.Report.Status != application.NoShowConfirmed || reviewed.Report.ConsumedNights != 3 {
		t.Errorf("review = %q consuming %d nights; want CONFIRMED and the three the policy's "+
			"100 %% rate says", reviewed.Report.Status, reviewed.Report.ConsumedNights)
	}
	if reviewed.Booking.Booking.Status != accommodationdomain.BookingNoShow {
		t.Errorf("the booking is %q after a confirmed no-show, want NO_SHOW",
			reviewed.Booking.Booking.Status)
	}
	availableAfter, reservedAfter, consumedAfter := s.balances(t)
	if availableAfter != "9.000000" || reservedAfter != "0.000000" || consumedAfter != "3.000000" {
		t.Errorf("after the confirmation: available=%q reserved=%q consumed=%q; want 9/0/3",
			availableAfter, reservedAfter, consumedAfter)
	}
	s.assertConservation(t, "after a confirmed no-show")

	// The room is free again: the guest did not come, so the hotel may sell those nights.
	for _, day := range []string{checkIn, "2026-06-16", lastNight} {
		_, _, confirmedRooms := s.inventoryOf(t, s.roomType, day)
		if confirmedRooms != 0 {
			t.Errorf("%s: confirmed = %d after a no-show, want the room back", day, confirmedRooms)
		}
	}

	// And a second review finds nothing to decide.
	if _, err := s.svc.ReviewNoShow(ctx, s.payerContext(reviewer), booking.Booking.ID,
		application.ReviewNoShowInput{Status: application.NoShowRejected}); !isError(
		err, application.ErrNoShowDecided) {
		t.Errorf("reviewing a decided report answered %v, want NO_SHOW_DECIDED", err)
	}
}

// TestRejectedNoShowLeavesTheBookingBookable is the other answer. A provider who was wrong
// must not have cost the member their stay: the booking is still CONFIRMED, the room is still
// theirs, the plan is untouched, and they can still check in or cancel.
func TestRejectedNoShowLeavesTheBookingBookable(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	s.putLodgingTerms(t)
	s.clock.At(t, insideFreeWindow)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	booking := s.confirmBooking(t, s.person, s.roomType)
	clerk := s.h.CreateActor("hotel-desk-2", "Otel Resepsiyon")
	reviewer := s.h.CreateActor("payer-reviewer-2", "Ödeyici Değerlendirici")
	evidence := s.linkCleanBookingDocument(t, booking.Booking.ID)

	s.clock.At(t, afterCheckInCloses)
	if _, err := s.svc.ReportNoShow(ctx, s.providerContext(clerk), booking.Booking.ID,
		application.ReportNoShowInput{EvidenceDocumentID: &evidence}); err != nil {
		t.Fatalf("report: %v", err)
	}
	comment := "Misafir geldi, kayıt hatası."
	reviewed, err := s.svc.ReviewNoShow(ctx, s.payerContext(reviewer), booking.Booking.ID,
		application.ReviewNoShowInput{Status: application.NoShowRejected, Comment: &comment})
	if err != nil {
		t.Fatalf("reject the no-show: %v", err)
	}
	if reviewed.Booking.Booking.Status != accommodationdomain.BookingConfirmed {
		t.Errorf("the booking is %q after a rejected report, want CONFIRMED", reviewed.Booking.Booking.Status)
	}
	if reviewed.Report.ConsumedNights != 0 {
		t.Errorf("a rejected report consumed %d nights, want none", reviewed.Report.ConsumedNights)
	}
	available, reserved, consumed := s.balances(t)
	if available != "9.000000" || reserved != "3.000000" || consumed != "0.000000" {
		t.Errorf("a rejected report moved the plan: available=%q reserved=%q consumed=%q; want 9/3/0",
			available, reserved, consumed)
	}
	// The room is still theirs, and they can still call it off.
	for _, day := range []string{checkIn, "2026-06-16", lastNight} {
		_, _, confirmedRooms := s.inventoryOf(t, s.roomType, day)
		if confirmedRooms != 1 {
			t.Errorf("%s: confirmed = %d after a rejected report, want the room still taken",
				day, confirmedRooms)
		}
	}
	if _, err := s.svc.CancelBooking(ctx, s.memberContext(), booking.Booking.ID, ""); err != nil {
		t.Errorf("a booking whose no-show was rejected cannot be cancelled: %v", err)
	}
	s.assertConservation(t, "after a rejected no-show")
}

// TestDisputedNoShowRaisesWorkForAPerson: a dispute decides nothing and puts the question in
// front of somebody, which is the only honest thing to do with two parties who disagree.
func TestDisputedNoShowRaisesWorkForAPerson(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	s.putLodgingTerms(t)
	s.clock.At(t, insideFreeWindow)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	booking := s.confirmBooking(t, s.person, s.roomType)
	clerk := s.h.CreateActor("hotel-desk-3", "Otel Resepsiyon")
	reviewer := s.h.CreateActor("payer-reviewer-3", "Ödeyici Değerlendirici")
	evidence := s.linkCleanBookingDocument(t, booking.Booking.ID)
	s.h.AdminExec(`
		INSERT INTO workflow.work_queue (tenant_id, code, name, domain_code, active)
		VALUES ($1, $2, 'Rezervasyon incelemesi', 'ACCOMMODATION', true)`,
		s.tenant, application.NoShowReviewQueueCode)

	s.clock.At(t, afterCheckInCloses)
	if _, err := s.svc.ReportNoShow(ctx, s.providerContext(clerk), booking.Booking.ID,
		application.ReportNoShowInput{EvidenceDocumentID: &evidence}); err != nil {
		t.Fatalf("report: %v", err)
	}
	if _, err := s.svc.ReviewNoShow(ctx, s.payerContext(reviewer), booking.Booking.ID,
		application.ReviewNoShowInput{Status: application.NoShowDisputed}); err != nil {
		t.Fatalf("dispute: %v", err)
	}

	var items int
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM workflow.work_item
		 WHERE tenant_id = $1 AND aggregate_type = 'BOOKING' AND aggregate_id = $2`,
		s.tenant, booking.Booking.ID).Scan(&items); err != nil {
		t.Fatalf("count work items: %v", err)
	}
	if items != 1 {
		t.Errorf("work items raised by the dispute = %d, want 1 — a dispute nobody is watching "+
			"is a member left with a fee and no answer", items)
	}
	// A dispute decides nothing.
	after, err := s.svc.GetBooking(ctx, s.deskContext(), booking.Booking.ID)
	if err != nil {
		t.Fatalf("read the booking: %v", err)
	}
	if after.Booking.Status != accommodationdomain.BookingConfirmed {
		t.Errorf("a disputed no-show moved the booking to %q", after.Booking.Status)
	}
	s.assertConservation(t, "after a disputed no-show")
}

// ---------------------------------------------------------------------------
// Waitlist
// ---------------------------------------------------------------------------

// TestWaitlistOffersInQueueOrder is the acceptance criterion of the queue: a freed room
// reaches the right person without anybody watching.
//
// Three members wait for a room type with one room, which is already taken. The one with the
// raised priority is behind the other two in time, and gets the offer anyway; the room is
// freed once, so exactly one offer is made. A sweep that ordered by `created_at` alone would
// offer it to the first joiner and this test would go red on the person.
func TestWaitlistOffersInQueueOrder(t *testing.T) {
	s := newServer(t)
	roomType := s.seedConcurrencyWorld(t, 4, 1)
	s.putLodgingTerms(t)
	s.clock.At(t, insideFreeWindow)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	// The room is taken, which is why anybody is waiting at all.
	occupant := s.confirmBooking(t, s.crowd[0], roomType)

	// Two ordinary joiners, and a third who joins last with a raised priority.
	first := s.joinWaitlist(t, s.crowd[1], roomType, 0)
	second := s.joinWaitlist(t, s.crowd[2], roomType, 0)
	privileged := s.joinWaitlist(t, s.crowd[3], roomType, 10)

	// Nothing is free, so the sweep offers nothing.
	if offered, err := s.svc.OfferWaitlistRooms(ctx, s.clock.Now()); err != nil || offered != 0 {
		t.Fatalf("sweep with no free room offered %d (err %v), want 0", offered, err)
	}

	// The occupant cancels; one room comes free.
	if _, err := s.svc.CancelBooking(ctx, s.contextFor(s.crowd[0]), occupant.Booking.ID, ""); err != nil {
		t.Fatalf("cancel the occupant: %v", err)
	}
	offered, err := s.svc.OfferWaitlistRooms(ctx, s.clock.Now())
	if err != nil {
		t.Fatalf("offer sweep: %v", err)
	}
	if offered != 1 {
		t.Fatalf("the sweep made %d offers for one freed room, want exactly 1", offered)
	}

	if got := s.waitlistStatus(t, privileged); got != application.WaitlistOffered {
		t.Errorf("the highest-priority entry is %q, want OFFERED — the queue ignored priority", got)
	}
	for _, id := range []uuid.UUID{first, second} {
		if got := s.waitlistStatus(t, id); got != application.WaitlistWaiting {
			t.Errorf("an entry behind the priority one is %q, want WAITING", got)
		}
	}

	// The offer is a real hold, with the hold's own deadline on the entry.
	entry := s.waitlistEntry(t, privileged)
	if entry.OfferedBookingID == nil || entry.OfferExpiresAt == nil {
		t.Fatal("an OFFERED entry with no booking or no deadline")
	}
	held, err := s.svc.GetBooking(ctx, s.deskContext(), *entry.OfferedBookingID)
	if err != nil {
		t.Fatalf("read the offered hold: %v", err)
	}
	if held.Booking.Status != accommodationdomain.BookingHold {
		t.Errorf("the offer is a %q, want a HOLD the member can accept", held.Booking.Status)
	}
	if held.Booking.PersonID != s.crowd[3] {
		t.Error("the offered hold is for somebody other than the entry's own member")
	}
	if !entry.OfferExpiresAt.Equal(*held.Booking.HoldExpiresAt) {
		t.Errorf("the offer runs out at %s and its hold at %s; an offer that outlived its "+
			"hold would be a room somebody else had already been sold",
			entry.OfferExpiresAt, held.Booking.HoldExpiresAt)
	}

	// The member takes it.
	accepted, err := s.svc.AcceptWaitlistOffer(ctx, s.contextFor(s.crowd[3]), privileged)
	if err != nil {
		t.Fatalf("accept the offer: %v", err)
	}
	if accepted.Entry.Status != application.WaitlistAccepted {
		t.Errorf("entry status = %q after accepting, want ACCEPTED", accepted.Entry.Status)
	}
	if accepted.Offer == nil || accepted.Offer.Booking.ServiceRequestID == nil {
		t.Error("accepting an offer raised no reservation request; it must confirm the same " +
			"way any other booking does")
	}
}

// TestUnacceptedOfferGoesToTheBackOfTheQueue is what makes the queue fair over time.
//
// An offer nobody takes expires with its hold, the room goes back, and the entry returns to
// WAITING *behind* the people who were already queued when it was made -- which is what
// moving its `created_at` to now does, because the queue is ordered by it. The next sweep
// then offers the room to the person who was second.
func TestUnacceptedOfferGoesToTheBackOfTheQueue(t *testing.T) {
	s := newServer(t)
	roomType := s.seedConcurrencyWorld(t, 3, 1)
	s.putLodgingTerms(t)
	s.clock.At(t, insideFreeWindow)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	occupant := s.confirmBooking(t, s.crowd[0], roomType)
	firstInLine := s.joinWaitlist(t, s.crowd[1], roomType, 0)
	secondInLine := s.joinWaitlist(t, s.crowd[2], roomType, 0)

	if _, err := s.svc.CancelBooking(ctx, s.contextFor(s.crowd[0]), occupant.Booking.ID, ""); err != nil {
		t.Fatalf("cancel the occupant: %v", err)
	}
	if _, err := s.svc.OfferWaitlistRooms(ctx, s.clock.Now()); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if got := s.waitlistStatus(t, firstInLine); got != application.WaitlistOffered {
		t.Fatalf("the first joiner is %q, want OFFERED", got)
	}

	// Nobody accepts. The hold's countdown runs out, and so does the offer's.
	entry := s.waitlistEntry(t, firstInLine)
	if _, err := s.h.Admin.Exec(ctx, `
		UPDATE accommodation.booking SET hold_expires_at = $3
		 WHERE tenant_id = $1 AND id = $2`,
		s.tenant, *entry.OfferedBookingID, s.clock.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("age the offered hold: %v", err)
	}
	if _, err := s.h.Admin.Exec(ctx, `
		UPDATE accommodation.waitlist_entry SET offer_expires_at = $3
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, firstInLine,
		s.clock.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("age the offer: %v", err)
	}
	if _, err := s.svc.ExpireHolds(ctx, s.clock.Now()); err != nil {
		t.Fatalf("expire the hold: %v", err)
	}

	offered, err := s.svc.OfferWaitlistRooms(ctx, s.clock.Now())
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if offered != 1 {
		t.Fatalf("the second sweep made %d offers, want 1 — the freed room has to pass on", offered)
	}
	if got := s.waitlistStatus(t, secondInLine); got != application.WaitlistOffered {
		t.Errorf("the second joiner is %q after the first let the room go, want OFFERED", got)
	}
	if got := s.waitlistStatus(t, firstInLine); got != application.WaitlistWaiting {
		t.Errorf("the first joiner is %q; an unaccepted offer must return them to WAITING", got)
	}
	// And they are behind, not in front: the queue is ordered by created_at, and theirs moved.
	requeued := s.waitlistEntry(t, firstInLine)
	stillWaiting := s.waitlistEntry(t, secondInLine)
	if !requeued.CreatedAt.After(stillWaiting.CreatedAt) {
		t.Errorf("the requeued entry was created at %s and the one that waited at %s; "+
			"an entry that let a room go must go to the back",
			requeued.CreatedAt, stillWaiting.CreatedAt)
	}
}

// TestOneLivePlaceInTheQueuePerPersonAndArrival is the partial unique index, proved the way
// the booking's own is: with two concurrent joins that both read "nothing yet".
//
// A service check would let both through -- that is the whole reason the rule is an index --
// and a cancelled entry must not stop the member joining again, which is what the predicate
// on the index says and what the second half of this test asserts.
func TestOneLivePlaceInTheQueuePerPersonAndArrival(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	ctx, cancel := s.h.Ctx()
	defer cancel()

	in := application.JoinWaitlistInput{
		PersonID: s.person, PropertyID: s.property, RoomTypeID: &s.roomType,
		CheckIn: mustDay(checkIn), CheckOut: mustDay(checkOut), Adults: 2,
	}
	results := make([]error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			joinCtx, joinCancel := context.WithTimeout(context.Background(), time.Minute)
			defer joinCancel()
			<-start
			_, err := s.svc.JoinWaitlist(joinCtx, s.memberContext(), in)
			results[i] = err
		}(i)
	}
	close(start)
	wg.Wait()

	joined, refused := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			joined++
		case isError(err, application.ErrWaitlistAlreadyWaiting):
			refused++
		default:
			t.Errorf("unexpected refusal from a concurrent join: %v", err)
		}
	}
	if joined != 1 || refused != 1 {
		t.Errorf("two concurrent joins produced %d entries and %d refusals, want 1 and 1",
			joined, refused)
	}

	// Giving the place up frees the member to ask again: the index is partial, and history
	// must not stop somebody rejoining a queue.
	entries, err := s.svc.ListWaitlist(ctx, s.memberContext(), application.WaitlistFilter{})
	if err != nil || len(entries) != 1 {
		t.Fatalf("list waitlist: %d entries, err %v", len(entries), err)
	}
	if _, err := s.svc.CancelWaitlistEntry(ctx, s.memberContext(), entries[0].Entry.ID); err != nil {
		t.Fatalf("cancel the entry: %v", err)
	}
	if _, err := s.svc.JoinWaitlist(ctx, s.memberContext(), in); err != nil {
		t.Errorf("rejoining after cancelling was refused: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Waitlist helpers
// ---------------------------------------------------------------------------

func (s *server) joinWaitlist(t *testing.T, person, roomType uuid.UUID, priority int) uuid.UUID {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	view, err := s.svc.JoinWaitlist(ctx, s.deskContext(), application.JoinWaitlistInput{
		PersonID: person, PropertyID: s.property, RoomTypeID: &roomType,
		CheckIn: mustDay(checkIn), CheckOut: mustDay(checkOut), Adults: 2, Priority: priority,
	})
	if err != nil {
		t.Fatalf("join the waitlist: %v", err)
	}
	// The seeded ids are time-ordered, and so is `created_at`; nudging the clock between
	// joins is what makes "who asked first" a fact rather than a coin toss.
	s.clock.Set(s.clock.Now().Add(time.Second))
	return view.Entry.ID
}

func (s *server) waitlistEntry(t *testing.T, id uuid.UUID) application.WaitlistRecord {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	entries, err := s.svc.ListWaitlist(ctx, s.deskContext(), application.WaitlistFilter{Limit: 200})
	if err != nil {
		t.Fatalf("list the waitlist: %v", err)
	}
	for _, entry := range entries {
		if entry.Entry.ID == id {
			return entry.Entry
		}
	}
	t.Fatalf("waitlist entry %s is not in the list", id)
	return application.WaitlistRecord{}
}

func (s *server) waitlistStatus(t *testing.T, id uuid.UUID) string {
	t.Helper()
	return s.waitlistEntry(t, id).Status
}

func mustMoment(t *testing.T, text string) time.Time {
	t.Helper()
	moment, err := time.Parse(time.RFC3339, text)
	if err != nil {
		t.Fatalf("parse %q: %v", text, err)
	}
	return moment.UTC()
}
