package accommodationhttp_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/accommodation/application"
	"github.com/celikbros/kapsora/internal/platform/db"
	servicerequestapp "github.com/celikbros/kapsora/internal/servicerequest/application"
	servicerequestdomain "github.com/celikbros/kapsora/internal/servicerequest/domain"
)

func TestCancellationEffectPenalizedPartialApprovalKeepsFrozenFee(t *testing.T) {
	s := newServer(t)
	booking := s.fractionalPenaltyBooking(t, beforeCheckInOpen, "1.5")
	s.clock.At(t, afterFreeWindow)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	preview, err := s.svc.PreviewCancellation(ctx, s.memberContext(), booking.Booking.ID)
	if err != nil || preview.Quote.EntitlementEffect == nil {
		t.Fatalf("penalized exact preview: %+v %v", preview.Quote, err)
	}
	want := &application.EntitlementEffect{
		ConsumedServiceNights: "1", ReleasedServiceNights: "0.5",
		ConsumedEntitlementUnits: "2", ReleasedEntitlementUnits: "1",
	}
	if *preview.Quote.EntitlementEffect != *want || preview.Quote.PenaltyNights != 1 ||
		preview.Quote.ReleasedNights != 0 || preview.Quote.FeeAmount != "1000" ||
		preview.Quote.PayerFee != "900" || preview.Quote.MemberFee != "100" {
		t.Fatalf("CANCELLATION_EFFECT_PENALIZED_PREVIEW: %+v, want %+v and unchanged 1000/900/100 fee",
			preview.Quote, *want)
	}
	result, err := s.svc.CancelBooking(ctx, s.memberContext(), booking.Booking.ID, "")
	if err != nil || result.Quote.EntitlementEffect == nil || result.Record == nil ||
		result.Record.EntitlementEffect == nil ||
		*result.Quote.EntitlementEffect != *want || *result.Record.EntitlementEffect != *want ||
		result.Quote.FeeAmount != preview.Quote.FeeAmount ||
		result.Quote.PayerFee != preview.Quote.PayerFee ||
		result.Quote.MemberFee != preview.Quote.MemberFee {
		t.Fatalf("CANCELLATION_EFFECT_PENALIZED_RESULT: quote=%+v record=%+v error=%v",
			result.Quote, result.Record, err)
	}
	available, reserved, consumed := s.fractionalAccountState(t)
	if available != "2.000000" || reserved != "0.000000" || consumed != "2.000000" {
		t.Fatalf("penalized exact account=%s/%s/%s, want 2/0/2",
			available, reserved, consumed)
	}
	var fee, payer, member, consumedService, releasedService string
	if err := s.h.Admin.QueryRow(ctx, `SELECT fee_amount::text,payer_fee::text,member_fee::text,
		consumed_service_nights::text,released_service_nights::text
		FROM accommodation.cancellation WHERE tenant_id=$1 AND booking_id=$2`,
		s.tenant, booking.Booking.ID).
		Scan(&fee, &payer, &member, &consumedService, &releasedService); err != nil {
		t.Fatal(err)
	}
	if fee != "1000.000000" || payer != "900.000000" || member != "100.000000" ||
		consumedService != "1.000000" || releasedService != "0.500000" {
		t.Fatalf("penalized exact persisted fee/effect=%s/%s/%s %s/%s",
			fee, payer, member, consumedService, releasedService)
	}
}

// The public preview and result must report the one unit actually returned by an
// authorization approved for half a service night, not the two nights in the old
// frozen-quote projection. The record is checked independently through SQL.
func TestCancellationEffectHTTPPreviewResultAndStoredRecord(t *testing.T) {
	s := newServer(t)
	booking := s.fractionalPenaltyBooking(t, insideFreeWindow, "0.5")
	path := "/api/v1/accommodation/bookings/" + booking.Booking.ID.String()
	previewResponse := s.do(t, http.MethodPost, path+"/cancellation-preview",
		bookerPermissions, nil, s.memberHeaders()...)
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("cancellation effect HTTP preview=%d %s",
			previewResponse.Code, previewResponse.Body.String())
	}
	preview := decode[kapsorav1.CancellationPreview](t, previewResponse)
	if preview.Quote.EntitlementEffect == nil || !preview.Quote.Free ||
		preview.Quote.ReleasedNights != 0 ||
		preview.Quote.EntitlementEffect.ConsumedServiceNights != "0" ||
		preview.Quote.EntitlementEffect.ReleasedServiceNights != "0.5" ||
		preview.Quote.EntitlementEffect.ConsumedEntitlementUnits != "0" ||
		preview.Quote.EntitlementEffect.ReleasedEntitlementUnits != "1" {
		t.Fatalf("CANCELLATION_EFFECT_HTTP_PREVIEW: %+v", preview.Quote)
	}
	resultResponse := s.do(t, http.MethodPost, path+"/cancel", bookerPermissions,
		map[string]any{}, s.memberHeaders()...)
	if resultResponse.Code != http.StatusOK {
		t.Fatalf("cancellation effect HTTP command=%d %s",
			resultResponse.Code, resultResponse.Body.String())
	}
	result := decode[kapsorav1.CancellationResult](t, resultResponse)
	if result.Quote.EntitlementEffect == nil || result.Cancellation.EntitlementEffect == nil ||
		*result.Quote.EntitlementEffect != *preview.Quote.EntitlementEffect ||
		*result.Cancellation.EntitlementEffect != *preview.Quote.EntitlementEffect ||
		result.Quote.ReleasedNights != 0 || result.Cancellation.ReleasedNights != 0 ||
		result.Quote.FeeAmount != preview.Quote.FeeAmount ||
		result.Quote.PayerFee != preview.Quote.PayerFee ||
		result.Quote.MemberFee != preview.Quote.MemberFee {
		t.Fatalf("CANCELLATION_EFFECT_HTTP_RESULT: quote=%+v record=%+v",
			result.Quote, result.Cancellation)
	}
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var releasedNights int
	var consumedService, releasedService, consumedUnits, releasedUnits string
	if err := s.h.Admin.QueryRow(ctx, `SELECT released_nights,consumed_service_nights::text,
		released_service_nights::text,consumed_entitlement_units::text,
		released_entitlement_units::text FROM accommodation.cancellation
		WHERE tenant_id=$1 AND booking_id=$2`, s.tenant, booking.Booking.ID).
		Scan(&releasedNights, &consumedService, &releasedService, &consumedUnits, &releasedUnits); err != nil {
		t.Fatal(err)
	}
	if releasedNights != 0 || consumedService != "0.000000" ||
		releasedService != "0.500000" || consumedUnits != "0.000000" ||
		releasedUnits != "1.000000" {
		t.Fatalf("CANCELLATION_EFFECT_STORED: whole/service/units=%d %s/%s %s/%s",
			releasedNights, consumedService, releasedService, consumedUnits, releasedUnits)
	}
	available, reserved, consumed := s.fractionalAccountState(t)
	if available != "4.000000" || reserved != "0.000000" || consumed != "0.000000" {
		t.Fatalf("cancellation effect terminal ledger=%s/%s/%s, want 4/0/0",
			available, reserved, consumed)
	}
}

// An existing row with only the original fields must remain readable and must not
// acquire a fabricated zero effect from the new nullable columns.
func TestCancellationEffectHistoricalRowHasNoInventedEvidence(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	s.putLodgingTerms(t)
	s.clock.At(t, insideFreeWindow)
	booking := s.confirmBooking(t, s.person, s.roomType)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if _, err := s.h.Admin.Exec(ctx, `INSERT INTO accommodation.cancellation
		(tenant_id,booking_id,cancelled_at,cancelled_by,reason_code,policy_snapshot,
		 free,penalty_nights,released_nights,fee_amount,payer_fee,member_fee,currency_code)
		SELECT tenant_id,id,clock_timestamp(),$3,'TEST',policy_snapshot,
		 true,0,3,0,0,0,'TRY' FROM accommodation.booking
		WHERE tenant_id=$1 AND id=$2`, s.tenant, booking.Booking.ID, s.actor); err != nil {
		t.Fatal(err)
	}
	var record application.CancellationRecord
	if err := db.WithTenantTx(ctx, s.h.Admin, db.TenantContext{TenantID: s.tenant, ActorID: s.actor},
		func(ctx context.Context, tx pgx.Tx) error {
			var readErr error
			record, readErr = s.deps.Bookings.GetCancellation(ctx, tx, s.tenant, booking.Booking.ID)
			return readErr
		}); err != nil {
		t.Fatal(err)
	}
	if record.EntitlementEffect != nil || record.ReleasedNights != 3 || !record.Free {
		t.Fatalf("CANCELLATION_EFFECT_HISTORICAL_NULL: %+v, want nil effect and legacy released3",
			record)
	}
}

func TestCancellationEffectFractionalFactorAndCumulativeRounding(t *testing.T) {
	for _, tc := range []struct {
		name, balance, factor                             string
		consumedUnits, releasedUnits, available, consumed string
	}{
		{"half-factor", "1.5", "0.5", "0.5", "0.25", "1.000000", "0.500000"},
		{"six-decimal-cumulative", "1", "0.333333", "0.333333", "0.166667", "0.666667", "0.333333"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t)
			booking := s.fractionalCheckoutBooking(t, tc.balance, tc.factor, "1.5")
			s.clock.At(t, afterFreeWindow)
			ctx, cancel := s.h.Ctx()
			defer cancel()
			preview, err := s.svc.PreviewCancellation(ctx, s.memberContext(), booking.Booking.ID)
			want := application.EntitlementEffect{
				ConsumedServiceNights: "1", ReleasedServiceNights: "0.5",
				ConsumedEntitlementUnits: tc.consumedUnits, ReleasedEntitlementUnits: tc.releasedUnits,
			}
			if err != nil || preview.Quote.EntitlementEffect == nil ||
				*preview.Quote.EntitlementEffect != want || preview.Quote.ReleasedNights != 0 {
				t.Fatalf("CANCELLATION_EFFECT_FACTOR_PREVIEW: factor=%s quote=%+v error=%v, want %+v",
					tc.factor, preview.Quote, err, want)
			}
			result, err := s.svc.CancelBooking(ctx, s.memberContext(), booking.Booking.ID, "")
			if err != nil || result.Quote.EntitlementEffect == nil || result.Record == nil ||
				result.Record.EntitlementEffect == nil ||
				*result.Quote.EntitlementEffect != want || *result.Record.EntitlementEffect != want ||
				result.Quote.FeeAmount != preview.Quote.FeeAmount ||
				result.Quote.PayerFee != preview.Quote.PayerFee ||
				result.Quote.MemberFee != preview.Quote.MemberFee {
				t.Fatalf("CANCELLATION_EFFECT_FACTOR_RESULT: factor=%s quote=%+v record=%+v error=%v",
					tc.factor, result.Quote, result.Record, err)
			}
			available, reserved, consumed := s.fractionalAccountState(t)
			if available != tc.available || reserved != "0.000000" || consumed != tc.consumed {
				t.Fatalf("CANCELLATION_EFFECT_FACTOR_LEDGER: factor=%s account=%s/%s/%s, want %s/0/%s",
					tc.factor, available, reserved, consumed, tc.available, tc.consumed)
			}
			var consumedUnits, releasedUnits string
			if err := s.h.Admin.QueryRow(ctx, `SELECT consumed_entitlement_units::text,
				released_entitlement_units::text FROM accommodation.cancellation
				WHERE tenant_id=$1 AND booking_id=$2`, s.tenant, booking.Booking.ID).
				Scan(&consumedUnits, &releasedUnits); err != nil {
				t.Fatal(err)
			}
			var matches bool
			if err := s.h.Admin.QueryRow(ctx, `SELECT $1::text::numeric=$2::text::numeric
				AND $3::text::numeric=$4::text::numeric`, consumedUnits, tc.consumedUnits,
				releasedUnits, tc.releasedUnits).Scan(&matches); err != nil || !matches {
				t.Fatalf("CANCELLATION_EFFECT_FACTOR_STORED: factor=%s units=%s/%s want %s/%s: %v",
					tc.factor, consumedUnits, releasedUnits, tc.consumedUnits, tc.releasedUnits, err)
			}
		})
	}
}

// Deliberately misbind only the evidence request. Settlement still uses the real
// authorization; a reporting uncertainty must neither invent an exact effect nor
// create a new refusal for an otherwise valid free cancellation.
type misboundCancellationEvidence struct {
	application.AuthorizationPort
	wrongPerson, wrongReservation bool
}

func (a misboundCancellationEvidence) CancellationEvidence(ctx context.Context, tx pgx.Tx,
	tenantID, authorizationID, requestID, personID, bookingID, reservationID,
	serviceDefinitionID uuid.UUID,
) (*application.BookingCancellationEvidence, error) {
	if a.wrongPerson {
		personID = uuid.New()
	}
	if a.wrongReservation {
		reservationID = uuid.New()
	}
	return a.AuthorizationPort.CancellationEvidence(ctx, tx, tenantID, authorizationID,
		requestID, personID, bookingID, reservationID, serviceDefinitionID)
}

func TestCancellationEffectUnprovenProvenanceOmitsExactClaim(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		wrongPerson, wrongReservation bool
	}{
		{"wrong-person", true, false},
		{"wrong-reservation", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t)
			booking := s.fractionalPenaltyBooking(t, insideFreeWindow, "0.5")
			deps := s.deps
			deps.Authorizations = misboundCancellationEvidence{AuthorizationPort: s.deps.Authorizations,
				wrongPerson: tc.wrongPerson, wrongReservation: tc.wrongReservation}
			var err error
			s.svc, err = application.New(deps)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := s.h.Ctx()
			defer cancel()
			preview, err := s.svc.PreviewCancellation(ctx, s.memberContext(), booking.Booking.ID)
			if err != nil || preview.Quote.EntitlementEffect != nil ||
				!preview.Quote.Free || preview.Quote.ReleasedNights != 2 {
				t.Fatalf("CANCELLATION_EFFECT_UNPROVEN_PREVIEW: %s quote=%+v error=%v",
					tc.name, preview.Quote, err)
			}
			result, err := s.svc.CancelBooking(ctx, s.memberContext(), booking.Booking.ID, "")
			if err != nil || result.Quote.EntitlementEffect != nil || result.Record == nil ||
				result.Record.EntitlementEffect != nil || result.Record.ReleasedNights != 2 {
				t.Fatalf("CANCELLATION_EFFECT_UNPROVEN_RESULT: %s quote=%+v record=%+v error=%v",
					tc.name, result.Quote, result.Record, err)
			}
			available, reserved, consumed := s.fractionalAccountState(t)
			if available != "4.000000" || reserved != "0.000000" || consumed != "0.000000" {
				t.Fatalf("CANCELLATION_EFFECT_UNPROVEN_LEDGER: %s account=%s/%s/%s",
					tc.name, available, reserved, consumed)
			}
		})
	}
}

func TestCancellationEffectPreviewRecomputesAfterAuthorizationConsumption(t *testing.T) {
	s := newServer(t)
	booking := s.fractionalPenaltyBooking(t, insideFreeWindow, "1.5")
	ctx, cancel := s.h.Ctx()
	defer cancel()
	before, err := s.svc.PreviewCancellation(ctx, s.memberContext(), booking.Booking.ID)
	if err != nil || before.Quote.EntitlementEffect == nil ||
		before.Quote.EntitlementEffect.ReleasedServiceNights != "1.5" ||
		before.Quote.EntitlementEffect.ReleasedEntitlementUnits != "3" {
		t.Fatalf("CANCELLATION_EFFECT_RECOMPUTE_BEFORE: %+v %v", before.Quote, err)
	}
	if err := db.WithTenantTx(ctx, s.h.App, db.TenantContext{TenantID: s.tenant, ActorID: s.actor},
		func(ctx context.Context, tx pgx.Tx) error {
			_, consumeErr := s.deps.Authorizations.Consume(ctx, tx, application.BookingConsumeInput{
				TenantID: s.tenant, ActorID: s.actor,
				AuthorizationID:     *booking.Booking.AuthorizationID,
				ServiceDefinitionID: s.definition, Nights: "0.5",
				Key:        "test:cancellation-effect-prior-consume:" + booking.Booking.ID.String(),
				ReasonCode: "TEST_PRIOR_USE",
			})
			return consumeErr
		}); err != nil {
		t.Fatal(err)
	}
	after, err := s.svc.PreviewCancellation(ctx, s.memberContext(), booking.Booking.ID)
	want := application.EntitlementEffect{
		ConsumedServiceNights: "0", ReleasedServiceNights: "1",
		ConsumedEntitlementUnits: "0", ReleasedEntitlementUnits: "2",
	}
	if err != nil || after.Quote.EntitlementEffect == nil ||
		*after.Quote.EntitlementEffect != want || after.Quote.ReleasedNights != 1 ||
		after.Quote.FeeAmount != before.Quote.FeeAmount {
		t.Fatalf("CANCELLATION_EFFECT_RECOMPUTE_AFTER: %+v error=%v, want %+v",
			after.Quote, err, want)
	}
	result, err := s.svc.CancelBooking(ctx, s.memberContext(), booking.Booking.ID, "")
	if err != nil || result.Quote.EntitlementEffect == nil ||
		*result.Quote.EntitlementEffect != want || result.Record == nil ||
		result.Record.EntitlementEffect == nil || *result.Record.EntitlementEffect != want {
		t.Fatalf("CANCELLATION_EFFECT_RECOMPUTE_RESULT: quote=%+v record=%+v error=%v",
			result.Quote, result.Record, err)
	}
	available, reserved, consumed := s.fractionalAccountState(t)
	if available != "3.000000" || reserved != "0.000000" || consumed != "1.000000" {
		t.Fatalf("CANCELLATION_EFFECT_RECOMPUTE_LEDGER: account=%s/%s/%s, want 3/0/1",
			available, reserved, consumed)
	}
}

func TestCancellationEffectPreviewFollowsReviewerApproval(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, insideFreeWindow)
	s.putLodgingTerms(t)
	s.replaceNightPlan(t, "4", "2")
	hold, _ := s.conversionHold(t, "2026-06-17")
	ctx, cancel := s.h.Ctx()
	defer cancel()
	confirmed, err := s.svc.ConfirmBooking(ctx, s.memberContext(), hold.Booking.ID)
	if err != nil || confirmed.Booking.ServiceRequestID == nil ||
		confirmed.Booking.AuthorizationID != nil {
		t.Fatalf("review-preview pending prerequisite: %+v %v", confirmed.Booking, err)
	}
	before, err := s.svc.PreviewCancellation(ctx, s.memberContext(), hold.Booking.ID)
	if err != nil || before.Quote.EntitlementEffect != nil {
		t.Fatalf("CANCELLATION_EFFECT_REVIEW_PENDING: %+v %v, want no invented effect",
			before.Quote, err)
	}
	requestID := *confirmed.Booking.ServiceRequestID
	request, err := s.requests.Get(ctx, s.deskContext(), requestID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.requests.PartiallyApprove(ctx, s.deskContext(), requestID,
		servicerequestapp.DecisionInput{
			ReasonCode: "FRACTIONAL_LIMIT", ExpectedVersion: request.Request.RowVersion,
			Items: []servicerequestdomain.DecisionItem{{LineNo: 1,
				Status: servicerequestdomain.ItemPartiallyApproved, ApprovedQuantity: "0.5"}},
		}); err != nil {
		t.Fatal(err)
	}
	s.deliverDecision(t, requestID)
	after, err := s.svc.PreviewCancellation(ctx, s.memberContext(), hold.Booking.ID)
	want := application.EntitlementEffect{
		ConsumedServiceNights: "0", ReleasedServiceNights: "0.5",
		ConsumedEntitlementUnits: "0", ReleasedEntitlementUnits: "1",
	}
	if err != nil || after.Quote.EntitlementEffect == nil ||
		*after.Quote.EntitlementEffect != want || after.Quote.ReleasedNights != 0 ||
		!after.Quote.Free {
		t.Fatalf("CANCELLATION_EFFECT_REVIEW_APPROVED: %+v error=%v, want %+v",
			after.Quote, err, want)
	}
	available, reserved, consumed := s.fractionalAccountState(t)
	if available != "3.000000" || reserved != "1.000000" || consumed != "0.000000" {
		t.Fatalf("review preview moved entitlement account=%s/%s/%s, want 3/1/0",
			available, reserved, consumed)
	}
}
