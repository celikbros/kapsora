package accommodationhttp_test

import (
	"os"
	"strings"
	"testing"

	"github.com/celikbros/kapsora/internal/accommodation/application"
	accommodationdomain "github.com/celikbros/kapsora/internal/accommodation/domain"
	benefitdomain "github.com/celikbros/kapsora/internal/benefit/domain"
	servicerequestapp "github.com/celikbros/kapsora/internal/servicerequest/application"
	servicerequestdomain "github.com/celikbros/kapsora/internal/servicerequest/domain"
)

// fractionalPenaltyBooking follows the published factor-two plan, real reviewer
// decision and booking outbox adoption. The policy is frozen at confirmation; only
// the reviewer's approved service quantity differs among the cases below.
func (s *server) fractionalPenaltyBooking(t *testing.T, now, approved string) application.BookingView {
	t.Helper()
	s.clock.At(t, now)
	s.putLodgingTerms(t)
	s.replaceNightPlan(t, "4", "2")
	hold, snapshot := s.conversionHold(t, "2026-06-17")
	if snapshot.CoveredNights != 2 || hold.Booking.EntitlementReservationID == nil {
		t.Fatalf("fractional penalty prerequisite hold: %+v %+v", hold.Booking, snapshot)
	}
	if _, units := s.conversionReservation(t, hold.Booking.ID); units != "4.000000" {
		t.Fatalf("fractional penalty prerequisite reserved units=%s, want 4", units)
	}
	ctx, cancel := s.h.Ctx()
	defer cancel()
	confirmed, err := s.svc.ConfirmBooking(ctx, s.memberContext(), hold.Booking.ID)
	if err != nil || confirmed.Booking.ServiceRequestID == nil {
		t.Fatalf("fractional penalty prerequisite confirmation: %+v %v", confirmed.Booking, err)
	}
	requestID := *confirmed.Booking.ServiceRequestID
	request, err := s.requests.Get(ctx, s.deskContext(), requestID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.requests.PartiallyApprove(ctx, s.deskContext(), requestID, servicerequestapp.DecisionInput{
		ReasonCode: "FRACTIONAL_LIMIT", ExpectedVersion: request.Request.RowVersion,
		Items: []servicerequestdomain.DecisionItem{{LineNo: 1,
			Status: servicerequestdomain.ItemPartiallyApproved, ApprovedQuantity: approved}},
	}); err != nil {
		t.Fatalf("fractional penalty prerequisite review: %v", err)
	}
	s.deliverDecision(t, requestID)
	final, err := s.svc.GetBooking(ctx, s.deskContext(), hold.Booking.ID)
	if err != nil || final.Booking.Status != accommodationdomain.BookingConfirmed ||
		final.Booking.AuthorizationID == nil || final.Booking.EntitlementReservationID == nil ||
		*final.Booking.EntitlementReservationID != *hold.Booking.EntitlementReservationID {
		t.Fatalf("fractional penalty prerequisite adoption: %+v %v", final.Booking, err)
	}
	var factor, quantity string
	if err := s.h.Admin.QueryRow(ctx, `SELECT entitlement_unit_factor::text,approved_quantity::text
		FROM service.authorization_item WHERE tenant_id=$1 AND authorization_id=$2`,
		s.tenant, *final.Booking.AuthorizationID).Scan(&factor, &quantity); err != nil {
		t.Fatal(err)
	}
	var approvedMatches bool
	if err := s.h.Admin.QueryRow(ctx, `SELECT $1::text::numeric=$2::text::numeric`,
		quantity, approved).Scan(&approvedMatches); err != nil {
		t.Fatal(err)
	}
	if factor != "2.000000" || !approvedMatches {
		t.Fatalf("fractional penalty prerequisite factor/approved=%s/%s, want 2/%s",
			factor, quantity, approved)
	}
	policy, err := application.DecodeLodgingPolicy(final.Booking.PolicySnapshot)
	if err != nil {
		t.Fatal(err)
	}
	noShowRate, err := benefitdomain.ParseQuantity(policy.NoShowPercent)
	if err != nil || policy.PenaltyKind != "NIGHTS" || policy.PenaltyNights == nil ||
		*policy.PenaltyNights != 1 || noShowRate.Cmp(benefitdomain.MustQuantity("100")) != 0 {
		t.Fatalf("fractional penalty frozen policy=%+v rate=%s error=%v, want one cancellation night and 100 percent no-show",
			policy, noShowRate.String(), err)
	}
	return final
}

func TestFractionalPenaltyFreeCancellationCapsToApproved(t *testing.T) {
	s := newServer(t)
	booking := s.fractionalPenaltyBooking(t, insideFreeWindow, "0.5")
	available, reserved, consumed := s.fractionalAccountState(t)
	if available != "3.000000" || reserved != "1.000000" || consumed != "0.000000" {
		t.Fatalf("free cancellation prerequisite account=%s/%s/%s, want 3/1/0",
			available, reserved, consumed)
	}
	ctx, cancel := s.h.Ctx()
	defer cancel()
	preview, err := s.svc.PreviewCancellation(ctx, s.memberContext(), booking.Booking.ID)
	if err != nil || preview.Quote.EntitlementEffect == nil ||
		preview.Quote.EntitlementEffect.ConsumedServiceNights != "0" ||
		preview.Quote.EntitlementEffect.ReleasedServiceNights != "0.5" ||
		preview.Quote.EntitlementEffect.ConsumedEntitlementUnits != "0" ||
		preview.Quote.EntitlementEffect.ReleasedEntitlementUnits != "1" ||
		preview.Quote.ReleasedNights != 0 {
		t.Fatalf("CANCELLATION_EFFECT_PREVIEW: quote=%+v error=%v, want actual 0/.5 service and 0/1 units",
			preview.Quote, err)
	}
	result, err := s.svc.CancelBooking(ctx, s.memberContext(), booking.Booking.ID, "")
	if err != nil || !result.Quote.Free || result.Quote.ReleasedNights != 0 ||
		result.Quote.EntitlementEffect == nil || result.Record == nil ||
		result.Record.EntitlementEffect == nil ||
		*result.Quote.EntitlementEffect != *result.Record.EntitlementEffect ||
		*result.Quote.EntitlementEffect != *preview.Quote.EntitlementEffect {
		t.Fatalf("fractional free cancellation: quote=%+v error=%v", result.Quote, err)
	}
	available, reserved, consumed = s.fractionalAccountState(t)
	if available != "4.000000" || reserved != "0.000000" || consumed != "0.000000" {
		t.Fatalf("fractional free cancellation account=%s/%s/%s, want 4/0/0",
			available, reserved, consumed)
	}
	if count, units := s.fractionalMovements(t, *booking.Booking.EntitlementReservationID,
		application.ReasonCancellationRelease); count != 1 || units != "1.000000" {
		t.Fatalf("fractional free cancellation release=%d/%s, want one actual remaining unit", count, units)
	}
	var releasedNights int
	var releasedService, releasedUnits string
	if err := s.h.Admin.QueryRow(ctx, `SELECT released_nights,
		released_service_nights::text,released_entitlement_units::text
		FROM accommodation.cancellation WHERE tenant_id=$1 AND booking_id=$2`,
		s.tenant, booking.Booking.ID).Scan(&releasedNights, &releasedService, &releasedUnits); err != nil {
		t.Fatal(err)
	}
	if releasedNights != 0 || releasedService != "0.500000" || releasedUnits != "1.000000" {
		t.Fatalf("CANCELLATION_EFFECT_RECORD: released whole/service/units=%d/%s/%s, want 0/.5/1",
			releasedNights, releasedService, releasedUnits)
	}
}

func TestFractionalPenaltyReproductionPenalizedCancellation(t *testing.T) {
	if os.Getenv("KAPSORA_TEST_FRACTIONAL_PENALTY_REPRODUCTION") != "1" {
		t.Skip("opt-in diagnostic: fractional cancellation penalty policy remains undecided")
	}
	s := newServer(t)
	booking := s.fractionalPenaltyBooking(t, beforeCheckInOpen, "0.5")
	s.clock.At(t, afterFreeWindow)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	preview, previewErr := s.svc.PreviewCancellation(ctx, s.memberContext(), booking.Booking.ID)
	if previewErr != nil || preview.Quote.EntitlementEffect != nil ||
		preview.Quote.PenaltyNights != 1 || preview.Quote.FeeAmount != "1000" {
		t.Fatalf("fractional penalized preview policy/effect=%+v error=%v, want frozen fee and no provable effect",
			preview.Quote, previewErr)
	}
	result, err := s.svc.CancelBooking(ctx, s.memberContext(), booking.Booking.ID, "")
	if err == nil {
		t.Fatalf("fractional penalized cancellation unexpectedly succeeded: %+v", result)
	}
	if !strings.Contains(err.Error(), "penalty of 1 nights is more than") {
		t.Fatalf("fractional penalized cancellation unexpected error: %v", err)
	}
	penaltyErr := err
	current, err := s.svc.GetBooking(ctx, s.deskContext(), booking.Booking.ID)
	if err != nil || current.Booking.Status != accommodationdomain.BookingConfirmed {
		t.Fatalf("fractional penalized cancellation rollback booking=%+v %v", current.Booking, err)
	}
	available, reserved, consumed := s.fractionalAccountState(t)
	if available != "3.000000" || reserved != "1.000000" || consumed != "0.000000" {
		t.Fatalf("fractional penalized cancellation rollback account=%s/%s/%s, want 3/1/0",
			available, reserved, consumed)
	}
	var cancellationRows int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM accommodation.cancellation
		WHERE tenant_id=$1 AND booking_id=$2`, s.tenant, booking.Booking.ID).
		Scan(&cancellationRows); err != nil {
		t.Fatal(err)
	}
	if cancellationRows != 0 {
		t.Fatalf("fractional penalized cancellation rollback rows=%d, want 0", cancellationRows)
	}
	if _, _, confirmed := s.inventoryOf(t, s.roomType, checkIn); confirmed != 1 {
		t.Fatalf("fractional penalized cancellation rollback inventory=%d, want one confirmed room", confirmed)
	}
	t.Fatalf("FRACTIONAL_PENALTY_CANCEL_REFUSAL: approved 0.5 NIGHT, frozen one-night penalty, reservation still holds 1 unit after %v", penaltyErr)
}

func TestFractionalPenaltyReproductionNoShow(t *testing.T) {
	if os.Getenv("KAPSORA_TEST_FRACTIONAL_PENALTY_REPRODUCTION") != "1" {
		t.Skip("opt-in diagnostic: fractional no-show penalty policy remains undecided")
	}
	s := newServer(t)
	booking := s.fractionalPenaltyBooking(t, beforeCheckInOpen, "1.5")
	clerk := s.h.CreateActor("fractional-no-show-desk", "Fractional Desk")
	reviewer := s.h.CreateActor("fractional-no-show-reviewer", "Fractional Reviewer")
	evidence := s.linkCleanBookingDocument(t, booking.Booking.ID)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	s.clock.At(t, afterCheckInCloses)
	reported, err := s.svc.ReportNoShow(ctx, s.providerContext(clerk), booking.Booking.ID,
		application.ReportNoShowInput{EvidenceDocumentID: &evidence})
	if err != nil || reported.Report.Status != application.NoShowReported {
		t.Fatalf("fractional no-show report: %+v %v", reported.Report, err)
	}
	result, err := s.svc.ReviewNoShow(ctx, s.payerContext(reviewer), booking.Booking.ID,
		application.ReviewNoShowInput{Status: application.NoShowConfirmed})
	if err == nil {
		t.Fatalf("fractional no-show confirmation unexpectedly succeeded: %+v", result.Report)
	}
	if !strings.Contains(err.Error(), "penalty of 2 nights is more than") {
		t.Fatalf("fractional no-show unexpected error: %v", err)
	}
	penaltyErr := err
	current, err := s.svc.GetBooking(ctx, s.deskContext(), booking.Booking.ID)
	if err != nil || current.Booking.Status != accommodationdomain.BookingConfirmed {
		t.Fatalf("fractional no-show rollback booking=%+v %v", current.Booking, err)
	}
	available, reserved, consumed := s.fractionalAccountState(t)
	if available != "1.000000" || reserved != "3.000000" || consumed != "0.000000" {
		t.Fatalf("fractional no-show rollback account=%s/%s/%s, want 1/3/0",
			available, reserved, consumed)
	}
	var reportStatus string
	if err := s.h.Admin.QueryRow(ctx, `SELECT status FROM accommodation.no_show
		WHERE tenant_id=$1 AND booking_id=$2`, s.tenant, booking.Booking.ID).Scan(&reportStatus); err != nil {
		t.Fatal(err)
	}
	if reportStatus != application.NoShowReported {
		t.Fatalf("fractional no-show rollback report=%s, want REPORTED", reportStatus)
	}
	if _, _, confirmed := s.inventoryOf(t, s.roomType, checkIn); confirmed != 1 {
		t.Fatalf("fractional no-show rollback inventory=%d, want one confirmed room", confirmed)
	}
	t.Fatalf("FRACTIONAL_PENALTY_NOSHOW_REFUSAL: approved 1.5 NIGHT, frozen two-night no-show penalty, reservation still holds 3 units after %v", penaltyErr)
}

// An integer partial approval has the same ceiling problem. Keeping this case
// separate from the fractional proof distinguishes the penalty-policy question
// from decimal arithmetic at checkout.
func TestFractionalPenaltyReproductionIntegerPartialNoShow(t *testing.T) {
	if os.Getenv("KAPSORA_TEST_FRACTIONAL_PENALTY_REPRODUCTION") != "1" {
		t.Skip("opt-in diagnostic: partial-approval no-show penalty policy remains undecided")
	}
	s := newServer(t)
	booking := s.fractionalPenaltyBooking(t, beforeCheckInOpen, "1")
	available, reserved, consumed := s.fractionalAccountState(t)
	if available != "2.000000" || reserved != "2.000000" || consumed != "0.000000" {
		t.Fatalf("integer partial no-show prerequisite account=%s/%s/%s, want 2/2/0",
			available, reserved, consumed)
	}
	clerk := s.h.CreateActor("integer-partial-no-show-desk", "Partial Desk")
	reviewer := s.h.CreateActor("integer-partial-no-show-reviewer", "Partial Reviewer")
	evidence := s.linkCleanBookingDocument(t, booking.Booking.ID)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	s.clock.At(t, afterCheckInCloses)
	reported, err := s.svc.ReportNoShow(ctx, s.providerContext(clerk), booking.Booking.ID,
		application.ReportNoShowInput{EvidenceDocumentID: &evidence})
	if err != nil || reported.Report.Status != application.NoShowReported {
		t.Fatalf("integer partial no-show report: %+v %v", reported.Report, err)
	}
	result, err := s.svc.ReviewNoShow(ctx, s.payerContext(reviewer), booking.Booking.ID,
		application.ReviewNoShowInput{Status: application.NoShowConfirmed})
	if err == nil {
		t.Fatalf("integer partial no-show confirmation unexpectedly succeeded: %+v", result.Report)
	}
	if !strings.Contains(err.Error(), "penalty of 2 nights is more than") {
		t.Fatalf("integer partial no-show unexpected error: %v", err)
	}
	penaltyErr := err
	current, err := s.svc.GetBooking(ctx, s.deskContext(), booking.Booking.ID)
	if err != nil || current.Booking.Status != accommodationdomain.BookingConfirmed {
		t.Fatalf("integer partial no-show rollback booking=%+v %v", current.Booking, err)
	}
	available, reserved, consumed = s.fractionalAccountState(t)
	if available != "2.000000" || reserved != "2.000000" || consumed != "0.000000" {
		t.Fatalf("integer partial no-show rollback account=%s/%s/%s, want 2/2/0",
			available, reserved, consumed)
	}
	var reportStatus, reservationRemaining string
	if err := s.h.Admin.QueryRow(ctx, `SELECT status FROM accommodation.no_show
		WHERE tenant_id=$1 AND booking_id=$2`, s.tenant, booking.Booking.ID).Scan(&reportStatus); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT (quantity-consumed_quantity-released_quantity)::text
		FROM benefit.entitlement_reservation WHERE tenant_id=$1 AND id=$2`,
		s.tenant, *booking.Booking.EntitlementReservationID).Scan(&reservationRemaining); err != nil {
		t.Fatal(err)
	}
	if reportStatus != application.NoShowReported || reservationRemaining != "2.000000" {
		t.Fatalf("integer partial no-show rollback report/reservation=%s/%s, want REPORTED/2",
			reportStatus, reservationRemaining)
	}
	if _, _, confirmed := s.inventoryOf(t, s.roomType, checkIn); confirmed != 1 {
		t.Fatalf("integer partial no-show rollback inventory=%d, want one confirmed room", confirmed)
	}
	t.Fatalf("INTEGER_PARTIAL_PENALTY_NOSHOW_REFUSAL: approved 1 NIGHT, frozen two-night no-show penalty, reservation still holds 2 units after %v", penaltyErr)
}
