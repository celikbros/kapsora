package application_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/claim/application"
	"github.com/celikbros/kapsora/internal/claim/domain"
	"github.com/celikbros/kapsora/internal/platform/outbox"
)

// The lodging claim, driven through the outbox handlers the worker registers, against a real
// database.
//
// Nothing here fakes a booking. The stay, its frozen nights and the two fee rows are written
// into the accommodation tables exactly as WP-I6-02 and WP-I6-03 write them, because the whole
// property this package promises is "the claim carries the figures the booking froze" — and a
// fake booking would prove that a fake was copied.

// The nightly figures. They are chosen so a re-price gives a visibly different answer: the
// contracted price of this room type is deliberately not what the booking froze, so a claim
// that asked the contract would produce 900 a night rather than 800 and 600.
const (
	nightOne      = "800"
	nightOnePayer = "600"
	nightOneMemb  = "200"
	nightTwo      = "600"
	nightTwoPayer = "600"
	nightTwoMemb  = "0"
	// The third night the plan did not carry: the whole amount is the member's.
	nightThree      = "700"
	nightThreePayer = "0"
	nightThreeMemb  = "700"
)

// bookingFixture is the stay a lodging claim is raised from.
type bookingFixture struct {
	bookingID  uuid.UUID
	roomType   uuid.UUID
	service    uuid.UUID
	checkIn    time.Time
	nights     int
	currency   string
	nightCount int
}

var bookingCheckIn = time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)

// seedBooking writes a property, a room type and a booking with its frozen nights.
//
// `status` and the moments are passed in because the three events this file drives arrive on
// three different bookings: a COMPLETED stay, a NO_SHOW and a CANCELLED one, each of which the
// booking table's own CHECKs insist looks a particular way.
func (f *fixture) seedBooking(t *testing.T, code, status string, nights []struct {
	unit, payer, member string
}, actualNights *int,
) bookingFixture {
	t.Helper()
	h := f.h
	ctx, cancel := h.Ctx()
	defer cancel()
	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		t.Helper()
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}

	var category, service uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		SELECT id FROM catalog.service_category WHERE tenant_id = $1 AND code = 'LODGING_ROOT'`,
		f.tenant).Scan(&category); err != nil {
		scan(&category, "lodging category", `
			INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
			VALUES ($1, 'LODGING_ROOT', 'Konaklama', 'ACCOMMODATION') RETURNING id`, f.tenant)
	}
	if err := h.Admin.QueryRow(ctx, `
		SELECT id FROM catalog.service_definition WHERE tenant_id = $1 AND code = 'LODGING_NIGHT'`,
		f.tenant).Scan(&service); err != nil {
		scan(&service, "lodging service", `
			INSERT INTO catalog.service_definition (tenant_id, category_id, code, name,
			                                        fulfillment_mode, default_unit_type,
			                                        requires_provider)
			VALUES ($1, $2, 'LODGING_NIGHT', 'Konaklama gecesi', 'DIRECT', 'NIGHT', true)
			RETURNING id`, f.tenant, category)
	}

	var property, roomType uuid.UUID
	if err := h.Admin.QueryRow(ctx, `
		SELECT id FROM accommodation.property WHERE tenant_id = $1 AND code = 'OTEL1'`,
		f.tenant).Scan(&property); err != nil {
		scan(&property, "property", `
			INSERT INTO accommodation.property (tenant_id, provider_organization_id, code, name,
			                                    property_type, timezone)
			VALUES ($1, $2, 'OTEL1', 'Sahil Oteli', 'HOTEL', 'Europe/Istanbul') RETURNING id`,
			f.tenant, f.provider)
	}
	if err := h.Admin.QueryRow(ctx, `
		SELECT id FROM accommodation.room_type WHERE tenant_id = $1 AND property_id = $2
		   AND code = 'STD'`, f.tenant, property).Scan(&roomType); err != nil {
		scan(&roomType, "room type", `
			INSERT INTO accommodation.room_type (tenant_id, property_id, code, name, max_adults,
			                                     max_children, max_occupancy, service_definition_id)
			VALUES ($1, $2, 'STD', 'Standart oda', 2, 2, 4, $3) RETURNING id`,
			f.tenant, property, service)
	}

	checkOut := bookingCheckIn.AddDate(0, 0, len(nights))
	// The frozen quote. `coveredNights` is the one number the claim reads back from it, and
	// it is the count of nights whose payer share is not nothing.
	covered := 0
	for _, night := range nights {
		if night.payer != "0" {
			covered++
		}
	}
	snapshot, err := json.Marshal(map[string]any{
		"version": 1, "currencyCode": "TRY", "coveredNights": covered,
		"serviceDefinitionId": service.String(),
	})
	if err != nil {
		t.Fatal(err)
	}

	var confirmedAt, checkedInAt, checkedOutAt, cancelledAt *time.Time
	moment := bookingCheckIn.Add(12 * time.Hour)
	switch status {
	case "COMPLETED":
		confirmedAt, checkedInAt = &moment, &moment
		out := checkOut.Add(10 * time.Hour)
		checkedOutAt = &out
	case "NO_SHOW":
		confirmedAt = &moment
	case "CANCELLED":
		cancelledAt = &moment
	}

	var bookingID uuid.UUID
	scan(&bookingID, "booking", `
		INSERT INTO accommodation.booking (tenant_id, reference, person_id, enrollment_id,
		                                   program_id, property_id, room_type_id, check_in,
		                                   check_out, nights, adults, status, quote_snapshot,
		                                   channel, confirmed_at, checked_in_at, checked_out_at,
		                                   cancelled_at, cancel_reason_code, actual_nights)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 2, $11, $12, 'MEMBER_PORTAL',
		        $13, $14, $15, $16, $17, $18)
		RETURNING id`,
		f.tenant, code, f.person, f.enrollment, f.program, property, roomType,
		bookingCheckIn, checkOut, len(nights), status, snapshot,
		confirmedAt, checkedInAt, checkedOutAt, cancelledAt,
		cancelReasonFor(status), actualNights)

	for i, night := range nights {
		h.AdminExec(`
			INSERT INTO accommodation.booking_night (tenant_id, booking_id, stay_date, room_type_id,
			                                         unit_amount, payer_amount, member_amount,
			                                         currency_code)
			VALUES ($1, $2, $3, $4, $5::text::numeric, $6::text::numeric, $7::text::numeric, 'TRY')`,
			f.tenant, bookingID, bookingCheckIn.AddDate(0, 0, i), roomType,
			night.unit, night.payer, night.member)
	}

	return bookingFixture{
		bookingID: bookingID, roomType: roomType, service: service, checkIn: bookingCheckIn,
		nights: len(nights), currency: "TRY", nightCount: len(nights),
	}
}

func cancelReasonFor(status string) *string {
	if status != "CANCELLED" && status != "NO_SHOW" {
		return nil
	}
	reason := "MEMBER_CANCELLED"
	if status == "NO_SHOW" {
		reason = "NO_SHOW"
	}
	return &reason
}

// threeNights is the stay every check-out test uses: two nights the plan carries and one it
// does not.
func threeNights() []struct{ unit, payer, member string } {
	return []struct{ unit, payer, member string }{
		{nightOne, nightOnePayer, nightOneMemb},
		{nightTwo, nightTwoPayer, nightTwoMemb},
		{nightThree, nightThreePayer, nightThreeMemb},
	}
}

// delivery builds the outbox delivery the worker would hand a handler.
func (f *fixture) delivery(t *testing.T, bookingID uuid.UUID, extra map[string]any) outbox.Delivery {
	t.Helper()
	body := map[string]any{"bookingId": bookingID}
	for k, v := range extra {
		body[k] = v
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return outbox.Delivery{
		TenantID: uuid.NullUUID{UUID: f.tenant, Valid: true},
		Payload:  payload,
	}
}

// claimForBooking reads back the claim a handler raised, through the service the API uses.
func (f *fixture) claimForBooking(t *testing.T, bookingID uuid.UUID) (application.ClaimView, bool) {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var claimID uuid.UUID
	err := f.h.Admin.QueryRow(ctx, `
		SELECT id FROM claim.claim
		 WHERE tenant_id = $1 AND source_type = 'BOOKING' AND source_id = $2
		   AND status NOT IN ('CANCELLED','REJECTED')`, f.tenant, bookingID).Scan(&claimID)
	if err != nil {
		return application.ClaimView{}, false
	}
	view, err := f.claims.GetClaim(context.Background(), f.financialRC(), claimID,
		application.AccessRequest{})
	if err != nil {
		t.Fatalf("read lodging claim: %v", err)
	}
	return view, true
}

// countClaimsFor is how many claims exist for a booking, live or not.
func (f *fixture) countClaimsFor(t *testing.T, bookingID uuid.UUID) int {
	t.Helper()
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var count int
	if err := f.h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM claim.claim
		 WHERE tenant_id = $1 AND source_type = 'BOOKING' AND source_id = $2`,
		f.tenant, bookingID).Scan(&count); err != nil {
		t.Fatalf("count claims: %v", err)
	}
	return count
}

// TestCheckOutProducesOneLinePerNightAtTheFrozenAmount is section 3's first requirement, and
// it is the one the whole package turns on: the claim's lines are the booking's own rows.
//
// The room type is contracted at a different price on purpose, so an implementation that asked
// the pricing ladder what a night costs would produce a visibly different figure rather than
// the same one by luck.
func TestCheckOutProducesOneLinePerNightAtTheFrozenAmount(t *testing.T) {
	f := newFixture(t)
	slept := 3
	booking := f.seedBooking(t, "BK-20260610-CHECKOUT", "COMPLETED", threeNights(), &slept)

	if err := f.claims.HandleBookingCheckedOut(context.Background(),
		f.delivery(t, booking.bookingID, nil)); err != nil {
		t.Fatalf("handle checked out: %v", err)
	}

	view, ok := f.claimForBooking(t, booking.bookingID)
	if !ok {
		t.Fatal("a completed stay produced no claim")
	}
	if view.Claim.DomainCode != domain.DomainAccommodation {
		t.Errorf("domain = %q, want %q", view.Claim.DomainCode, domain.DomainAccommodation)
	}
	if view.Claim.SourceType == nil || *view.Claim.SourceType != domain.SourceBooking {
		t.Errorf("source type = %v, want BOOKING", view.Claim.SourceType)
	}
	if view.Claim.SourceID == nil || *view.Claim.SourceID != booking.bookingID {
		t.Errorf("source id = %v, want the booking", view.Claim.SourceID)
	}
	if view.Claim.Status != domain.StatusPendingFinancial {
		t.Errorf("status = %q, want PENDING_FINANCIAL", view.Claim.Status)
	}
	if view.Claim.ProviderOrganizationID != f.provider {
		t.Errorf("provider = %v, want the property's organization", view.Claim.ProviderOrganizationID)
	}
	if len(view.Lines) != 3 {
		t.Fatalf("lines = %d, want one per night slept", len(view.Lines))
	}

	// The frozen amounts, copied. This is the assertion a re-pricing breaks.
	wantAmounts := []string{nightOne, nightTwo, nightThree}
	for i, want := range wantAmounts {
		line := lineByNo(t, view, i+1)
		if line.Line.LineAmount != want {
			t.Errorf("line %d amount = %s, want the booking's frozen %s",
				i+1, line.Line.LineAmount, want)
		}
		if line.Line.UnitAmount == nil {
			t.Errorf("line %d carries no unit amount, want the frozen %s", i+1, want)
		} else if *line.Line.UnitAmount != want {
			t.Errorf("line %d unit amount = %s, want the booking's frozen %s",
				i+1, *line.Line.UnitAmount, want)
		}
		if line.Line.Quantity != "1" || line.Line.UnitType != application.UnitNight {
			t.Errorf("line %d = %s %s, want 1 NIGHT", i+1, line.Line.Quantity, line.Line.UnitType)
		}
	}

	// The two nights the plan carried are already decided, at the payer amount, by the
	// system, with the reason the confirmation gave.
	for _, tc := range []struct {
		lineNo int
		payer  string
	}{{1, nightOnePayer}, {2, nightTwoPayer}} {
		line := lineByNo(t, view, tc.lineNo)
		if line.Decision == nil {
			t.Fatalf("line %d carries no decision; a night the plan carried is decided at creation", tc.lineNo)
		}
		if line.Decision.Decision != domain.DecisionApproved {
			t.Errorf("line %d decision = %s, want APPROVED", tc.lineNo, line.Decision.Decision)
		}
		if line.Decision.ApprovedAmount != tc.payer || line.Decision.PayerAmount != tc.payer {
			t.Errorf("line %d approved %s / payer %s, want %s at the line's payer amount",
				tc.lineNo, line.Decision.ApprovedAmount, line.Decision.PayerAmount, tc.payer)
		}
		if line.Decision.MemberAmount != "0" {
			t.Errorf("line %d member amount = %s, want 0", tc.lineNo, line.Decision.MemberAmount)
		}
		if line.Decision.ReasonCode != application.ReasonBookingConfirmed {
			t.Errorf("line %d reason = %s, want %s", tc.lineNo, line.Decision.ReasonCode,
				application.ReasonBookingConfirmed)
		}
		if line.Decision.Stage != domain.StageAuto || line.Decision.DecidedBy != nil {
			t.Errorf("line %d decided at %s by %v; a decision nobody looked at is AUTO with no actor",
				tc.lineNo, line.Decision.Stage, line.Decision.DecidedBy)
		}
	}
	// The night the plan did not carry is a line with payer nothing and no decision: it is
	// the financial reviewer's to answer.
	third := lineByNo(t, view, 3)
	if third.Decision != nil {
		t.Errorf("line 3 was decided at creation; a night the plan did not carry is the reviewer's")
	}
}

// TestASecondCheckOutEventCreatesNothing is the at-least-once delivery requirement. A handler
// that looked and then wrote without the read would raise a second claim for a stay the
// provider would then invoice twice.
func TestASecondCheckOutEventCreatesNothing(t *testing.T) {
	f := newFixture(t)
	slept := 3
	booking := f.seedBooking(t, "BK-20260610-REDELIVR", "COMPLETED", threeNights(), &slept)
	delivery := f.delivery(t, booking.bookingID, nil)

	for i := 0; i < 3; i++ {
		if err := f.claims.HandleBookingCheckedOut(context.Background(), delivery); err != nil {
			t.Fatalf("delivery %d: %v", i+1, err)
		}
	}
	if got := f.countClaimsFor(t, booking.bookingID); got != 1 {
		t.Fatalf("claims for the booking = %d, want exactly 1 after three deliveries", got)
	}

	// And nothing hanging off it was written twice either.
	ctx, cancel := f.h.Ctx()
	defer cancel()
	var versions, lines, decisions int
	if err := f.h.Admin.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM claim.claim_version v
		         WHERE v.tenant_id = c.tenant_id AND v.claim_id = c.id),
		       (SELECT count(*) FROM claim.claim_line l
		          JOIN claim.claim_version v ON v.tenant_id = l.tenant_id AND v.id = l.version_id
		         WHERE v.claim_id = c.id),
		       (SELECT count(*) FROM claim.line_decision d
		          JOIN claim.claim_line l ON l.tenant_id = d.tenant_id AND l.id = d.line_id
		          JOIN claim.claim_version v ON v.tenant_id = l.tenant_id AND v.id = l.version_id
		         WHERE v.claim_id = c.id)
		  FROM claim.claim c
		 WHERE c.tenant_id = $1 AND c.source_id = $2`,
		f.tenant, booking.bookingID).Scan(&versions, &lines, &decisions); err != nil {
		t.Fatalf("count claim rows: %v", err)
	}
	if versions != 1 || lines != 3 || decisions != 2 {
		t.Fatalf("after three deliveries: %d versions, %d lines, %d decisions; want 1, 3, 2",
			versions, lines, decisions)
	}
}

// TestTwoLiveClaimsForOneBookingAreRefusedByTheDatabase bypasses the application entirely.
// The handler's read is the first half of the idempotency; this is the half that holds when
// two deliveries look at the same moment and both find nothing.
func TestTwoLiveClaimsForOneBookingAreRefusedByTheDatabase(t *testing.T) {
	f := newFixture(t)
	slept := 3
	booking := f.seedBooking(t, "BK-20260610-UNIQUE01", "COMPLETED", threeNights(), &slept)
	if err := f.claims.HandleBookingCheckedOut(context.Background(),
		f.delivery(t, booking.bookingID, nil)); err != nil {
		t.Fatalf("handle checked out: %v", err)
	}

	ctx, cancel := f.h.Ctx()
	defer cancel()
	_, err := f.h.Admin.Exec(ctx, `
		INSERT INTO claim.claim (tenant_id, reference, person_id, program_id, enrollment_id,
		                         provider_organization_id, domain_code, source_type, source_id,
		                         status, service_date_from, service_date_to)
		VALUES ($1, 'CLM-DUPLICATE-1', $2, $3, $4, $5, 'ACCOMMODATION', 'BOOKING', $6,
		        'PENDING_FINANCIAL', $7, $7)`,
		f.tenant, f.person, f.program, f.enrollment, f.provider, booking.bookingID,
		bookingCheckIn)
	if err == nil {
		t.Fatal("a second live claim for one booking was accepted; uq_claim_live_booking is not holding")
	}

	// A cancelled claim is not live, so the same booking may be claimed again after one was
	// withdrawn. That is the partial index doing exactly what it says.
	f.h.AdminExec(`UPDATE claim.claim SET status = 'CANCELLED', closed_at = clock_timestamp()
	                WHERE tenant_id = $1 AND source_id = $2`, f.tenant, booking.bookingID)
	if _, err := f.h.Admin.Exec(ctx, `
		INSERT INTO claim.claim (tenant_id, reference, person_id, program_id, enrollment_id,
		                         provider_organization_id, domain_code, source_type, source_id,
		                         status, service_date_from, service_date_to)
		VALUES ($1, 'CLM-AFTERCANCEL-1', $2, $3, $4, $5, 'ACCOMMODATION', 'BOOKING', $6,
		        'PENDING_FINANCIAL', $7, $7)`,
		f.tenant, f.person, f.program, f.enrollment, f.provider, booking.bookingID,
		bookingCheckIn); err != nil {
		t.Fatalf("a cancelled claim still blocked the booking: %v", err)
	}
}

// TestAClaimSourceMustExist is the trigger that stands in for the composite foreign key a
// `source_type` column cannot have.
func TestAClaimSourceMustExist(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := f.h.Ctx()
	defer cancel()
	_, err := f.h.Admin.Exec(ctx, `
		INSERT INTO claim.claim (tenant_id, reference, person_id, program_id, enrollment_id,
		                         provider_organization_id, domain_code, source_type, source_id,
		                         status, service_date_from, service_date_to)
		VALUES ($1, 'CLM-NOSOURCE-1', $2, $3, $4, $5, 'ACCOMMODATION', 'BOOKING', $6,
		        'DRAFT', $7, $7)`,
		f.tenant, f.person, f.program, f.enrollment, f.provider, uuid.New(), bookingCheckIn)
	if err == nil {
		t.Fatal("a claim naming a booking that does not exist was accepted")
	}
}

// TestNoShowConfirmationProducesAClaimMatchingTheFeeRow is section 3's third requirement for
// the no-show half: the line is the fee, and the split is the row's own.
func TestNoShowConfirmationProducesAClaimMatchingTheFeeRow(t *testing.T) {
	f := newFixture(t)
	booking := f.seedBooking(t, "BK-20260610-NOSHOW01", "NO_SHOW", threeNights(), nil)
	f.seedNoShow(t, booking.bookingID, "CONFIRMED", "800", "600", "200")

	if err := f.claims.HandleBookingNoShowConfirmed(context.Background(),
		f.delivery(t, booking.bookingID, nil)); err != nil {
		t.Fatalf("handle no-show confirmed: %v", err)
	}
	view, ok := f.claimForBooking(t, booking.bookingID)
	if !ok {
		t.Fatal("a confirmed no-show produced no claim")
	}
	if len(view.Lines) != 1 {
		t.Fatalf("lines = %d, want exactly one fee line", len(view.Lines))
	}
	line := lineByNo(t, view, 1)
	if line.Line.UnitType != application.UnitNoShowFee || line.Line.Quantity != "1" {
		t.Errorf("line = %s %s, want 1 %s", line.Line.Quantity, line.Line.UnitType,
			application.UnitNoShowFee)
	}
	if line.Line.LineAmount != "800" {
		t.Errorf("fee line = %s, want the assessed 800", line.Line.LineAmount)
	}
	if line.Decision == nil || line.Decision.PayerAmount != "600" {
		t.Errorf("payer share = %v, want the fee row's 600", line.Decision)
	}

	// **The provenance is the claim's source, not a ledger row.** The claim names the booking,
	// the no-show row is unique per booking, and the line's unit type says which of the two
	// fees assessed it — so nothing has to be written into `claim.adjustment` to answer "where
	// did this money come from", and the ledger stays a table that only ever holds money that
	// moved.
	if view.Claim.SourceType == nil || *view.Claim.SourceType != domain.SourceBooking ||
		view.Claim.SourceID == nil || *view.Claim.SourceID != booking.bookingID {
		t.Errorf("source = %v / %v, want BOOKING naming the stay the fee was assessed on",
			view.Claim.SourceType, view.Claim.SourceID)
	}
	adjustments, err := f.claims.ListAdjustments(context.Background(), f.financialRC(), view.Claim.ID)
	if err != nil {
		t.Fatalf("list adjustments: %v", err)
	}
	if len(adjustments) != 0 {
		t.Errorf("a fee claim wrote %d ledger rows; the fee is the line and nothing moved beside it",
			len(adjustments))
	}
	// And it waits where every lodging claim waits: in front of the financial reviewer.
	if view.Claim.Status != domain.StatusPendingFinancial {
		t.Errorf("status = %s, want PENDING_FINANCIAL", view.Claim.Status)
	}
}

// TestAPenalisedCancellationProducesAClaimAndAFreeOneDoesNot is the other half of the same
// requirement, and the negative half is the one that matters: a member who cancelled inside
// the window they were promised must not find a bill.
func TestAPenalisedCancellationProducesAClaimAndAFreeOneDoesNot(t *testing.T) {
	f := newFixture(t)
	penalised := f.seedBooking(t, "BK-20260610-CANCELFE", "CANCELLED", threeNights(), nil)
	f.seedCancellation(t, penalised.bookingID, false, "800", "0", "800")
	free := f.seedBooking(t, "BK-20260610-CANCELFR", "CANCELLED", threeNights(), nil)
	f.seedCancellation(t, free.bookingID, true, "0", "0", "0")

	ctx := context.Background()
	if err := f.claims.HandleBookingCancelled(ctx,
		f.delivery(t, penalised.bookingID, map[string]any{"free": false})); err != nil {
		t.Fatalf("handle penalised cancellation: %v", err)
	}
	if err := f.claims.HandleBookingCancelled(ctx,
		f.delivery(t, free.bookingID, map[string]any{"free": true})); err != nil {
		t.Fatalf("handle free cancellation: %v", err)
	}

	view, ok := f.claimForBooking(t, penalised.bookingID)
	if !ok {
		t.Fatal("a penalised cancellation produced no claim")
	}
	line := lineByNo(t, view, 1)
	if line.Line.UnitType != application.UnitCancellationFee || line.Line.LineAmount != "800" {
		t.Errorf("line = %s %s, want the assessed cancellation fee of 800",
			line.Line.LineAmount, line.Line.UnitType)
	}
	// Same as the no-show: the claim's source is the provenance, and the ledger is untouched.
	if view.Claim.SourceID == nil || *view.Claim.SourceID != penalised.bookingID {
		t.Errorf("source id = %v, want the cancelled stay", view.Claim.SourceID)
	}
	rows, err := f.claims.ListAdjustments(ctx, f.financialRC(), view.Claim.ID)
	if err != nil {
		t.Fatalf("list adjustments: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("a cancellation fee claim wrote %d ledger rows, want none", len(rows))
	}
	// The whole fee is the member's on this policy, so nothing is auto-approved for the payer
	// and the financial reviewer answers who carries it.
	if line.Decision != nil {
		t.Errorf("a fee entirely the member's was auto-approved: %+v", line.Decision)
	}

	if got := f.countClaimsFor(t, free.bookingID); got != 0 {
		t.Fatalf("a free cancellation produced %d claims, want none", got)
	}
}

// seedNoShow writes a decided no-show report with the fee it assessed.
func (f *fixture) seedNoShow(t *testing.T, bookingID uuid.UUID, status, fee, payer, member string) {
	t.Helper()
	f.h.AdminExec(`
		INSERT INTO accommodation.no_show (tenant_id, booking_id, reported_by_actor_id, reported_at,
		                                   assessed_fee_amount, payer_amount, member_amount,
		                                   currency_code, status, reviewed_by, reviewed_at,
		                                   consumed_nights)
		VALUES ($1, $2, $3, clock_timestamp(), $4::text::numeric, $5::text::numeric,
		        $6::text::numeric, 'TRY', $7, $8, clock_timestamp(), 1)`,
		f.tenant, bookingID, f.actor, fee, payer, member, status, f.reviewer)
}

// seedCancellation writes the cancellation row and the fee it charged.
func (f *fixture) seedCancellation(t *testing.T, bookingID uuid.UUID, free bool,
	fee, payer, member string,
) {
	t.Helper()
	f.h.AdminExec(`
		INSERT INTO accommodation.cancellation (tenant_id, booking_id, cancelled_at, cancelled_by,
		                                        reason_code, policy_snapshot, free, penalty_nights,
		                                        released_nights, fee_amount, payer_fee, member_fee,
		                                        currency_code)
		VALUES ($1, $2, clock_timestamp(), $3, 'MEMBER_CANCELLED', '{}'::jsonb, $4, $5, 0,
		        $6::text::numeric, $7::text::numeric, $8::text::numeric, 'TRY')`,
		f.tenant, bookingID, f.actor, free, penaltyNightsFor(free), fee, payer, member)
}

func penaltyNightsFor(free bool) int {
	if free {
		return 0
	}
	return 1
}
