package accommodationhttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/accommodation/application"
	accommodationpg "github.com/celikbros/kapsora/internal/accommodation/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/platform/outbox"
	servicerequestapp "github.com/celikbros/kapsora/internal/servicerequest/application"
	servicerequestdomain "github.com/celikbros/kapsora/internal/servicerequest/domain"
)

// replaceNightPlan preserves the existing member, room service, provider contract and
// price. A new version is mapped while DRAFT, then published, and the old enrollment is
// suspended so the command has exactly one active funding choice. Both accounts remain
// in the database, making accidental use of the original account observable.
func (s *server) replaceNightPlan(t *testing.T, balance, factor string, familyShared ...bool) uuid.UUID {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var plan, version, definition, enrollment, account uuid.UUID
	shared := len(familyShared) > 0 && familyShared[0]
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO benefit.plan
		(tenant_id,program_id,code,name,status)
		VALUES ($1,$2,'CONVERSION','Mapped night conversion','ACTIVE') RETURNING id`,
		s.tenant, s.program).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO benefit.plan_version
		(tenant_id,plan_id,version_no,status,valid_period)
		VALUES ($1,$2,1,'DRAFT',daterange('2026-01-01','2027-01-01','[)')) RETURNING id`,
		s.tenant, plan).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO benefit.entitlement_definition
		(tenant_id,plan_version_id,code,name,unit_type,period_type,initial_quantity,family_shared)
		VALUES ($1,$2,'KONAKLAMA_GECE','Mapped nights','NIGHT','CALENDAR_YEAR',$3::text::numeric,$4)
		RETURNING id`, s.tenant, version, balance, shared).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	s.h.AdminExec(`INSERT INTO benefit.service_entitlement_mapping
		(tenant_id,plan_version_id,service_definition_id,entitlement_definition_id,unit_factor)
		VALUES ($1,$2,$3,$4,$5::text::numeric)`,
		s.tenant, version, s.definition, definition, factor)
	s.h.AdminExec(`UPDATE benefit.plan_version SET status='PUBLISHED',
		published_at=clock_timestamp(),published_by=$3 WHERE tenant_id=$1 AND id=$2`,
		s.tenant, version, s.actor)
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO benefit.enrollment
		(tenant_id,sponsor_membership_id,plan_id,status,valid_period)
		SELECT tenant_id,sponsor_membership_id,$3,'ACTIVE',valid_period
		FROM benefit.enrollment WHERE tenant_id=$1 AND id=$2 RETURNING id`,
		s.tenant, s.enrollment, plan).Scan(&enrollment); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO benefit.entitlement_account
		(tenant_id,enrollment_id,entitlement_definition_id,benefit_period,
		 total_granted,available_quantity)
		VALUES ($1,$2,$3,daterange('2026-01-01','2027-01-01','[)'),
		 $4::text::numeric,$4::text::numeric) RETURNING id`,
		s.tenant, enrollment, definition, balance).Scan(&account); err != nil {
		t.Fatal(err)
	}
	s.h.AdminExec(`INSERT INTO benefit.entitlement_ledger
		(tenant_id,entitlement_account_id,movement_type,effective_at,
		 delta_total,delta_available,reference_type,reference_id,idempotency_key)
		VALUES ($1,$2,'GRANT',clock_timestamp(),$3::text::numeric,$3::text::numeric,
		 'ENROLLMENT',$4,'grant:conversion')`, s.tenant, account, balance, enrollment)
	s.h.AdminExec(`UPDATE benefit.enrollment SET status='SUSPENDED'
		WHERE tenant_id=$1 AND id=$2`, s.tenant, s.enrollment)
	// Search's general eligibility read is intentionally unpinned. Freeze the
	// superseded balance so a duplicate entitlement code cannot supply its answer.
	s.h.AdminExec(`UPDATE benefit.entitlement_account SET status='FROZEN'
		WHERE tenant_id=$1 AND id=$2`, s.tenant, s.account)
	s.enrollment, s.plan, s.planVersion = enrollment, plan, version
	s.entitlementDefinition, s.account = definition, account
	return account
}

func (s *server) conversionQuote(t *testing.T, checkout, balance, factor string) *kapsorav1.AvailabilityQuote {
	t.Helper()
	body := s.searchBody()
	body["checkOut"] = checkout
	rec := s.do(t, http.MethodPost, "/api/v1/accommodation/availability/search",
		readerPermissions, body, s.memberHeaders()...)
	if rec.Code != http.StatusOK {
		t.Fatalf("conversion search = %d: %s", rec.Code, rec.Body.String())
	}
	result := decode[kapsorav1.AvailabilitySearchResult](t, rec)
	if result.EvaluationId == nil || result.Entitlement == nil || result.Entitlement.Unit != "NIGHT" {
		t.Fatalf("conversion search missed NIGHT evaluation: %+v", result)
	}
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var selectedEnrollment, selectedVersion, selectedAccount uuid.UUID
	var balanceMatches, factorMatches bool
	if err := s.h.Admin.QueryRow(ctx, `SELECT e.enrollment_id,e.plan_version_id,a.id,
		(e.result_snapshot->'items'->0->>'availableQuantity')::numeric=$4::text::numeric,
		m.unit_factor=$5::text::numeric
		FROM benefit.eligibility_evaluation e
		JOIN benefit.service_entitlement_mapping m
		 ON m.tenant_id=e.tenant_id AND m.plan_version_id=e.plan_version_id
		 AND m.service_definition_id=$3
		JOIN benefit.entitlement_account a
		 ON a.tenant_id=m.tenant_id AND a.entitlement_definition_id=m.entitlement_definition_id
		 AND a.id=$6
		WHERE e.tenant_id=$1 AND e.id=$2`,
		s.tenant, *result.EvaluationId, s.definition, balance, factor, s.account).
		Scan(&selectedEnrollment, &selectedVersion, &selectedAccount, &balanceMatches, &factorMatches); err != nil {
		t.Fatalf("conversion evaluation fixture: %v", err)
	}
	if selectedEnrollment != s.enrollment || selectedVersion != s.planVersion ||
		selectedAccount != s.account || !balanceMatches || !factorMatches {
		t.Fatalf("conversion evaluation selected %s/%s/%s balance=%t factor=%t",
			selectedEnrollment, selectedVersion, selectedAccount, balanceMatches, factorMatches)
	}
	for _, item := range result.Results {
		if item.RoomType.Id == s.roomType {
			return item.Quote
		}
	}
	t.Fatal("priced room type absent from conversion search")
	return nil
}

func conversionPayerNights(quote *kapsorav1.AvailabilityQuote) string {
	if quote == nil {
		return ""
	}
	amounts := make([]string, 0, len(quote.NightlyAmounts))
	for _, night := range quote.NightlyAmounts {
		amounts = append(amounts, night.PayerAmount)
	}
	return strings.Join(amounts, ",")
}

func (s *server) conversionHold(t *testing.T, checkout string) (application.BookingView, application.QuoteSnapshot) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	in := s.holdInput(s.person, s.roomType)
	in.CheckOut = mustDay(checkout)
	view, err := s.svc.CreateHold(ctx, s.memberContext(), in)
	if err != nil {
		t.Fatalf("conversion hold: %v", err)
	}
	var snapshot application.QuoteSnapshot
	if err := json.Unmarshal(view.Booking.QuoteSnapshot, &snapshot); err != nil {
		t.Fatal(err)
	}
	if view.Booking.EnrollmentID != s.enrollment || view.Booking.ProgramID != s.program ||
		snapshot.EvaluationID == nil {
		t.Fatalf("conversion booking/evaluation identity: %+v %+v", view.Booking, snapshot)
	}
	if snapshot.Version != application.QuoteSnapshotVersion || snapshot.NightConversion == nil ||
		snapshot.NightConversion.PlanVersionID != s.planVersion ||
		snapshot.NightConversion.DefinitionID != s.entitlementDefinition ||
		snapshot.NightConversion.AccountID != s.account ||
		snapshot.NightConversion.UnitType != "NIGHT" {
		t.Fatalf("conversion frozen mapping identity: %+v", snapshot)
	}
	return view, snapshot
}

func (s *server) conversionReservation(t *testing.T, bookingID uuid.UUID) (account uuid.UUID, units string) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if err := s.h.Admin.QueryRow(ctx, `SELECT r.entitlement_account_id,r.quantity::text
		FROM accommodation.booking b JOIN benefit.entitlement_reservation r
		 ON r.tenant_id=b.tenant_id AND r.id=b.entitlement_reservation_id
		WHERE b.tenant_id=$1 AND b.id=$2`, s.tenant, bookingID).Scan(&account, &units); err != nil {
		t.Fatal(err)
	}
	if account != s.account {
		t.Fatalf("conversion reserved account %s, want selected %s", account, s.account)
	}
	var reservations, movements int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM benefit.entitlement_reservation
		WHERE tenant_id=$1 AND reference_id=$2`, s.tenant, bookingID).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM benefit.entitlement_ledger
		WHERE tenant_id=$1 AND reference_id=$2 AND movement_type='RESERVE'`,
		s.tenant, bookingID).Scan(&movements); err != nil {
		t.Fatal(err)
	}
	if reservations != 1 || movements != 1 {
		t.Fatalf("conversion hold effects reservations=%d RESERVE=%d, want one each", reservations, movements)
	}
	return account, units
}

func TestNightConversionBalance3Factor2Stay2(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.replaceNightPlan(t, "3", "2")
	quote := s.conversionQuote(t, "2026-06-17", "3", "2")
	if quote == nil || conversionPayerNights(quote) != "900,0" ||
		quote.TotalAmount != "2000" || quote.PayerAmount != "900" || quote.MemberAmount != "1100" {
		t.Fatalf("NIGHT_CONVERSION_BALANCE3_COVERAGE: search quote = %+v, want 1 covered night and 2000/900/1100", quote)
	}
	view, snapshot := s.conversionHold(t, "2026-06-17")
	if snapshot.CoveredNights != 1 || snapshot.TotalAmount != "2000" ||
		snapshot.PayerAmount != "900" || snapshot.MemberAmount != "1100" {
		t.Fatalf("NIGHT_CONVERSION_BALANCE3_COVERAGE: hold quote = %+v", snapshot)
	}
	_, units := s.conversionReservation(t, view.Booking.ID)
	if units != "2.000000" {
		t.Fatalf("NIGHT_CONVERSION_BALANCE3_UNITS: reserved %s, want 2.000000", units)
	}
}

func TestNightConversionBalance4Factor2Stay2(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.replaceNightPlan(t, "4", "2")
	quote := s.conversionQuote(t, "2026-06-17", "4", "2")
	if quote == nil || conversionPayerNights(quote) != "900,900" ||
		quote.TotalAmount != "2000" || quote.PayerAmount != "1800" || quote.MemberAmount != "200" {
		t.Fatalf("NIGHT_CONVERSION_BALANCE4_COVERAGE: search quote = %+v, want 2 covered nights and 2000/1800/200", quote)
	}
	view, snapshot := s.conversionHold(t, "2026-06-17")
	if snapshot.CoveredNights != 2 || snapshot.TotalAmount != "2000" ||
		snapshot.PayerAmount != "1800" || snapshot.MemberAmount != "200" {
		t.Fatalf("NIGHT_CONVERSION_BALANCE4_COVERAGE: hold quote = %+v", snapshot)
	}
	_, units := s.conversionReservation(t, view.Booking.ID)
	if units != "4.000000" {
		t.Fatalf("NIGHT_CONVERSION_BALANCE4_UNITS: reserved %s, want 4.000000", units)
	}
}

func TestNightConversionBalanceOnePointFiveFactorHalfStay3(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.replaceNightPlan(t, "1.5", "0.5")
	quote := s.conversionQuote(t, checkOut, "1.5", "0.5")
	if quote == nil || conversionPayerNights(quote) != "900,900,900" ||
		quote.TotalAmount != "3000" || quote.PayerAmount != "2700" || quote.MemberAmount != "300" {
		t.Fatalf("NIGHT_CONVERSION_BALANCE1_5_COVERAGE: search quote = %+v, want 3 covered nights and 3000/2700/300", quote)
	}
	view, snapshot := s.conversionHold(t, checkOut)
	if snapshot.CoveredNights != 3 || snapshot.TotalAmount != "3000" ||
		snapshot.PayerAmount != "2700" || snapshot.MemberAmount != "300" {
		t.Fatalf("NIGHT_CONVERSION_BALANCE1_5_COVERAGE: hold quote = %+v", snapshot)
	}
	_, units := s.conversionReservation(t, view.Booking.ID)
	if units != "1.500000" {
		t.Fatalf("NIGHT_CONVERSION_BALANCE1_5_UNITS: reserved %s, want 1.500000", units)
	}
}

func TestNightConversionAdoptionRetainsFactor(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.putLodgingTerms(t)
	s.replaceNightPlan(t, "4", "2")
	quote := s.conversionQuote(t, "2026-06-17", "4", "2")
	if quote == nil || conversionPayerNights(quote) != "900,900" ||
		quote.TotalAmount != "2000" || quote.PayerAmount != "1800" || quote.MemberAmount != "200" {
		t.Fatalf("adoption prerequisite search quote = %+v", quote)
	}
	view, snapshot := s.conversionHold(t, "2026-06-17")
	if snapshot.CoveredNights != 2 || view.Booking.EntitlementReservationID == nil {
		t.Fatalf("adoption prerequisite hold = %+v %+v", view.Booking, snapshot)
	}
	reservationID := *view.Booking.EntitlementReservationID
	// Old code reserves two units rather than four. This test intentionally carries
	// that old hold through submission to isolate the separate adoption-factor bug.
	s.conversionReservation(t, view.Booking.ID)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	confirmed, err := s.svc.ConfirmBooking(ctx, s.memberContext(), view.Booking.ID)
	if err != nil || confirmed.Booking.ServiceRequestID == nil {
		t.Fatalf("adoption prerequisite confirmation = %+v, %v", confirmed.Booking, err)
	}
	requestID := *confirmed.Booking.ServiceRequestID
	s.decideRequest(t, requestID, servicerequestdomain.StatusApproved)
	s.deliverDecision(t, requestID)
	final, err := s.svc.GetBooking(ctx, s.deskContext(), view.Booking.ID)
	if err != nil || final.Booking.Status != "CONFIRMED" || final.Booking.AuthorizationID == nil {
		t.Fatalf("adoption prerequisite booking = %+v, %v", final.Booking, err)
	}
	var itemReservation uuid.UUID
	var approved, factor string
	if err := s.h.Admin.QueryRow(ctx, `SELECT approved_quantity::text,
		entitlement_unit_factor::text,entitlement_reservation_id
		FROM service.authorization_item WHERE tenant_id=$1 AND authorization_id=$2`,
		s.tenant, *final.Booking.AuthorizationID).Scan(&approved, &factor, &itemReservation); err != nil {
		t.Fatalf("adoption prerequisite authorization item: %v", err)
	}
	if approved != "2.000000" || itemReservation != reservationID {
		t.Fatalf("adoption prerequisite service quantity/reservation = %s/%s, want 2/%s",
			approved, itemReservation, reservationID)
	}
	var reserveMovements int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM benefit.entitlement_ledger
		WHERE tenant_id=$1 AND reference_id=$2 AND movement_type='RESERVE'`,
		s.tenant, view.Booking.ID).Scan(&reserveMovements); err != nil {
		t.Fatal(err)
	}
	if reserveMovements != 1 {
		t.Fatalf("adoption prerequisite RESERVE movements = %d, want one", reserveMovements)
	}
	if factor != "2.000000" {
		t.Fatalf("NIGHT_CONVERSION_ADOPTION_FACTOR: authorization factor = %s, want 2.000000", factor)
	}
}

func TestNightConversionLegacyV2UsesOnlyOriginalFactorOne(t *testing.T) {
	for _, tc := range []struct {
		name, factor      string
		missingEvaluation bool
		retireOriginal    bool
		confirm           bool
	}{
		{"proven factor one after retirement", "1", false, true, true},
		{"nonunit mapping", "2", false, false, false},
		{"unprovable evaluation", "1", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t)
			s.clock.At(t, "2026-06-13T09:00:00Z")
			s.putLodgingTerms(t)
			s.replaceNightPlan(t, "4", tc.factor)
			view, _ := s.conversionHold(t, "2026-06-17")
			ctx, cancel := s.h.Ctx()
			defer cancel()
			edit := `(quote_snapshot - 'nightConversion') || '{"version":2}'::jsonb`
			if tc.missingEvaluation {
				edit = `(quote_snapshot - 'nightConversion' - 'evaluationId') || '{"version":2}'::jsonb`
			}
			// A synthetic v2 hold keeps the original immutable evaluation and the
			// reservation actually made at the hold; only private conversion data is absent.
			if _, err := s.h.Admin.Exec(ctx, `UPDATE accommodation.booking SET quote_snapshot=`+edit+
				` WHERE tenant_id=$1 AND id=$2`, s.tenant, view.Booking.ID); err != nil {
				t.Fatal(err)
			}
			if tc.retireOriginal {
				if _, err := s.h.Admin.Exec(ctx, `UPDATE benefit.plan_version
					SET status='RETIRED',retire_reason_code='TEST'
					WHERE tenant_id=$1 AND id=$2`, s.tenant, s.planVersion); err != nil {
					t.Fatal(err)
				}
			}
			read := s.do(t, http.MethodGet, "/api/v1/accommodation/bookings/"+view.Booking.ID.String(),
				bookerPermissions, nil, s.memberHeaders()...)
			if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"version":1`) ||
				strings.Contains(read.Body.String(), "nightConversion") {
				t.Fatalf("legacy public quote = %d %s", read.Code, read.Body.String())
			}
			confirmed, err := s.svc.ConfirmBooking(ctx, s.memberContext(), view.Booking.ID)
			if tc.confirm {
				if err != nil || confirmed.Booking.ServiceRequestID == nil {
					t.Fatalf("proven v2 factor-one confirmation: %v", err)
				}
				s.decideRequest(t, *confirmed.Booking.ServiceRequestID, servicerequestdomain.StatusApproved)
				s.deliverDecision(t, *confirmed.Booking.ServiceRequestID)
				return
			}
			if !errors.Is(err, application.ErrQuoteStale) {
				t.Fatalf("legacy nonunit/unprovable confirm = %v, want stale", err)
			}
			var requests, authorizations int
			if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM service.service_request WHERE tenant_id=$1`, s.tenant).Scan(&requests); err != nil {
				t.Fatal(err)
			}
			if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM service.authorization WHERE tenant_id=$1`, s.tenant).Scan(&authorizations); err != nil {
				t.Fatal(err)
			}
			if requests != 0 || authorizations != 0 {
				t.Fatalf("legacy refusal effects requests=%d authorizations=%d", requests, authorizations)
			}
			if _, err := s.svc.ReleaseHold(ctx, s.memberContext(), view.Booking.ID); err != nil {
				t.Fatalf("legacy refused hold release: %v", err)
			}
			var available string
			if err := s.h.Admin.QueryRow(ctx, `SELECT available_quantity::text FROM benefit.entitlement_account WHERE tenant_id=$1 AND id=$2`, s.tenant, s.account).Scan(&available); err != nil {
				t.Fatal(err)
			}
			if available != "4.000000" {
				t.Fatalf("legacy release available %s, want 4", available)
			}
		})
	}
}

func (s *server) competingNightEnrollment(t *testing.T) uuid.UUID {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var plan, version, definition, enrollment, account uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO benefit.plan
		(tenant_id,program_id,code,name,status)
		VALUES ($1,$2,'CONVERSION_B','Competing night plan','ACTIVE') RETURNING id`,
		s.tenant, s.program).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO benefit.plan_version
		(tenant_id,plan_id,version_no,status,valid_period)
		VALUES ($1,$2,1,'DRAFT',daterange('2026-01-01','2027-01-01','[)')) RETURNING id`,
		s.tenant, plan).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO benefit.entitlement_definition
		(tenant_id,plan_version_id,code,name,unit_type,period_type,initial_quantity)
		VALUES ($1,$2,'KONAKLAMA_GECE','Competing nights','NIGHT','CALENDAR_YEAR',20)
		RETURNING id`, s.tenant, version).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	s.h.AdminExec(`INSERT INTO benefit.service_entitlement_mapping
		(tenant_id,plan_version_id,service_definition_id,entitlement_definition_id,unit_factor)
		VALUES ($1,$2,$3,$4,1)`, s.tenant, version, s.definition, definition)
	s.h.AdminExec(`UPDATE benefit.plan_version SET status='PUBLISHED',
		published_at=clock_timestamp(),published_by=$3 WHERE tenant_id=$1 AND id=$2`,
		s.tenant, version, s.actor)
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO benefit.enrollment
		(tenant_id,sponsor_membership_id,plan_id,status,valid_period)
		SELECT tenant_id,sponsor_membership_id,$3,'ACTIVE',daterange('2026-06-01','2027-01-01','[)')
		FROM benefit.enrollment WHERE tenant_id=$1 AND id=$2 RETURNING id`,
		s.tenant, s.enrollment, plan).Scan(&enrollment); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO benefit.entitlement_account
		(tenant_id,enrollment_id,entitlement_definition_id,benefit_period,total_granted,available_quantity)
		VALUES ($1,$2,$3,daterange('2026-01-01','2027-01-01','[)'),20,20) RETURNING id`,
		s.tenant, enrollment, definition).Scan(&account); err != nil {
		t.Fatal(err)
	}
	s.h.AdminExec(`INSERT INTO benefit.entitlement_ledger
		(tenant_id,entitlement_account_id,movement_type,effective_at,
		 delta_total,delta_available,reference_type,reference_id,idempotency_key)
		VALUES ($1,$2,'GRANT',clock_timestamp(),20,20,'ENROLLMENT',$3,'grant:competing')`,
		s.tenant, account, enrollment)
	return account
}

func TestNightConversionConfirmationPinsHeldEnrollmentAgainstLaterSameProgramPlan(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.putLodgingTerms(t)
	s.replaceNightPlan(t, "4", "2")
	hold, snapshot := s.conversionHold(t, "2026-06-17")
	if snapshot.CoveredNights != 2 {
		t.Fatalf("A coverage = %d, want 2", snapshot.CoveredNights)
	}
	accountB := s.competingNightEnrollment(t)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	confirmed, err := s.svc.ConfirmBooking(ctx, s.memberContext(), hold.Booking.ID)
	if err != nil || confirmed.Booking.ServiceRequestID == nil {
		t.Fatalf("A hold, B later same program confirmation: %v", err)
	}
	var selectedEnrollment, selectedVersion uuid.UUID
	var status, itemOutcome, available string
	if err := s.h.Admin.QueryRow(ctx, `SELECT ee.enrollment_id,ee.plan_version_id,sr.status,
		ee.result_snapshot->'items'->0->>'outcome',
		ee.result_snapshot->'items'->0->>'availableQuantity'
		FROM service.service_request sr JOIN benefit.eligibility_evaluation ee
		 ON ee.tenant_id=sr.tenant_id AND ee.id=sr.eligibility_evaluation_id
		WHERE sr.tenant_id=$1 AND sr.id=$2`, s.tenant, *confirmed.Booking.ServiceRequestID).
		Scan(&selectedEnrollment, &selectedVersion, &status, &itemOutcome, &available); err != nil {
		t.Fatal(err)
	}
	if selectedEnrollment != s.enrollment || selectedVersion != s.planVersion ||
		itemOutcome != "ELIGIBLE" || available != "0" ||
		status == "ELIGIBILITY_FAILED" {
		t.Fatalf("pinned confirmation selected %s/%s status %s outcome %s available %s, want A %s/%s eligible on own held units",
			selectedEnrollment, selectedVersion, status, itemOutcome, available, s.enrollment, s.planVersion)
	}
	s.decideRequest(t, *confirmed.Booking.ServiceRequestID, servicerequestdomain.StatusApproved)
	s.deliverDecision(t, *confirmed.Booking.ServiceRequestID)
	var adoptedAccount, adoptedReservation uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `SELECT r.entitlement_account_id,ai.entitlement_reservation_id
		FROM accommodation.booking b JOIN service.authorization_item ai
		 ON ai.tenant_id=b.tenant_id AND ai.authorization_id=b.authorization_id
		JOIN benefit.entitlement_reservation r ON r.tenant_id=ai.tenant_id
		 AND r.id=ai.entitlement_reservation_id
		WHERE b.tenant_id=$1 AND b.id=$2`, s.tenant, hold.Booking.ID).
		Scan(&adoptedAccount, &adoptedReservation); err != nil {
		t.Fatal(err)
	}
	if adoptedAccount != s.account || adoptedReservation != *hold.Booking.EntitlementReservationID {
		t.Fatalf("A adoption account/reservation %s/%s, want %s/%s", adoptedAccount, adoptedReservation,
			s.account, *hold.Booking.EntitlementReservationID)
	}
	var bAvailable string
	if err := s.h.Admin.QueryRow(ctx, `SELECT available_quantity::text FROM benefit.entitlement_account
		WHERE tenant_id=$1 AND id=$2`, s.tenant, accountB).Scan(&bAvailable); err != nil {
		t.Fatal(err)
	}
	if bAvailable != "20.000000" {
		t.Fatalf("B balance changed to %s while adopting A", bAvailable)
	}
}

type failConfirmationOnce struct {
	application.BookingRepository
	failed bool
}

type transientEvidenceOnce struct {
	application.BookingRepository
	failed bool
}

func (r *transientEvidenceOnce) BookingConversionEvidence(ctx context.Context, tx pgx.Tx,
	tenantID, evaluationID, reservationID, serviceDefinitionID uuid.UUID,
) (application.BookingConversionEvidence, error) {
	if !r.failed {
		r.failed = true
		return application.BookingConversionEvidence{}, errors.New("injected transient evidence read")
	}
	return r.BookingRepository.BookingConversionEvidence(ctx, tx, tenantID, evaluationID, reservationID, serviceDefinitionID)
}

func (r *failConfirmationOnce) ConfirmBookingRow(ctx context.Context, tx pgx.Tx,
	tenantID, id, authorizationID uuid.UUID, at time.Time, policy json.RawMessage, actorID uuid.UUID,
) (bool, error) {
	if !r.failed {
		r.failed = true
		return false, errors.New("injected booking status failure")
	}
	return r.BookingRepository.ConfirmBookingRow(ctx, tx, tenantID, id, authorizationID, at, policy, actorID)
}

func TestNightConversionPartialApprovalReleasesSurplusAndRetryAdoptsOnce(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.putLodgingTerms(t)
	s.replaceNightPlan(t, "4", "2")
	hold, _ := s.conversionHold(t, "2026-06-17")
	ctx, cancel := s.h.Ctx()
	defer cancel()
	confirmed, err := s.svc.ConfirmBooking(ctx, s.memberContext(), hold.Booking.ID)
	if err != nil || confirmed.Booking.ServiceRequestID == nil {
		t.Fatalf("confirm: %v", err)
	}
	requestID := *confirmed.Booking.ServiceRequestID
	request, err := s.requests.Get(ctx, s.deskContext(), requestID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.requests.PartiallyApprove(ctx, s.deskContext(), requestID, servicerequestapp.DecisionInput{
		ReasonCode: "LIMIT_APPLIED", ExpectedVersion: request.Request.RowVersion,
		Items: []servicerequestdomain.DecisionItem{{LineNo: 1, Status: servicerequestdomain.ItemPartiallyApproved, ApprovedQuantity: "1"}},
	}); err != nil {
		t.Fatalf("partial decision: %v", err)
	}
	var payload []byte
	if err := s.h.Admin.QueryRow(ctx, `SELECT payload_json FROM system.outbox_event
		WHERE tenant_id=$1 AND event_type=$2 AND aggregate_id=$3
		ORDER BY occurred_at DESC LIMIT 1`, s.tenant, servicerequestapp.DecidedEvent, requestID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	deps := s.deps
	stop := &failConfirmationOnce{BookingRepository: accommodationpg.NewBookings()}
	deps.Bookings = stop
	s.svc, err = application.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	delivery := outbox.Delivery{TenantID: uuid.NullUUID{UUID: s.tenant, Valid: true}, Payload: payload}
	if err := s.svc.HandleServiceRequestDecided(ctx, delivery); err == nil || !stop.failed {
		t.Fatalf("injected post-adoption booking write = %v, want failure", err)
	}
	assertState := func(wantAvailable, wantReserved, wantConsumed string, wantAuth int) {
		t.Helper()
		var available, reserved, consumed string
		var authorizations, reserves, surplusReleases int
		if err := s.h.Admin.QueryRow(ctx, `SELECT available_quantity::text,reserved_quantity::text,consumed_quantity::text
			FROM benefit.entitlement_account WHERE tenant_id=$1 AND id=$2`, s.tenant, s.account).
			Scan(&available, &reserved, &consumed); err != nil {
			t.Fatal(err)
		}
		if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM service.authorization WHERE tenant_id=$1`, s.tenant).Scan(&authorizations); err != nil {
			t.Fatal(err)
		}
		if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM benefit.entitlement_ledger
			WHERE tenant_id=$1 AND reference_id=$2 AND movement_type='RESERVE'`, s.tenant, hold.Booking.ID).Scan(&reserves); err != nil {
			t.Fatal(err)
		}
		if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM benefit.entitlement_ledger
			WHERE tenant_id=$1 AND reservation_id=$2 AND movement_type='RELEASE'
			AND idempotency_key=$3`, s.tenant, *hold.Booking.EntitlementReservationID,
			"booking-unapproved:"+hold.Booking.ID.String()).Scan(&surplusReleases); err != nil {
			t.Fatal(err)
		}
		if available != wantAvailable || reserved != wantReserved || consumed != wantConsumed ||
			authorizations != wantAuth || reserves != 1 || surplusReleases != 1 {
			t.Fatalf("partial approval balances=%s/%s/%s auth=%d reserve=%d surplus=%d",
				available, reserved, consumed, authorizations, reserves, surplusReleases)
		}
	}
	assertState("2.000000", "2.000000", "0.000000", 1)
	if err := s.svc.HandleServiceRequestDecided(ctx, delivery); err != nil {
		t.Fatalf("retry after committed adoption: %v", err)
	}
	assertState("2.000000", "2.000000", "0.000000", 1)
	final, err := s.svc.GetBooking(ctx, s.deskContext(), hold.Booking.ID)
	if err != nil || final.Booking.Status != "CONFIRMED" || final.Booking.AuthorizationID == nil {
		t.Fatalf("retry booking: %+v %v", final.Booking, err)
	}
	issued, err := s.svc.IssueBookingVoucher(ctx, s.memberContext(), hold.Booking.ID)
	if err != nil {
		t.Fatalf("voucher: %v", err)
	}
	s.clock.At(t, atCheckIn)
	if _, err := s.svc.CheckInBooking(ctx, s.deskContext(), hold.Booking.ID,
		application.CheckInInput{Token: issued.Token}); err != nil {
		t.Fatalf("check in: %v", err)
	}
	s.clock.At(t, afterOneNight)
	if _, err := s.svc.CheckOutBooking(ctx, s.deskContext(), hold.Booking.ID,
		application.CheckOutInput{}); err != nil {
		t.Fatalf("one-night checkout: %v", err)
	}
	assertState("2.000000", "0.000000", "2.000000", 1)
}

func TestNightConversionTransientApprovalEvidenceReadRetries(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.putLodgingTerms(t)
	s.replaceNightPlan(t, "4", "2")
	hold, _ := s.conversionHold(t, "2026-06-17")
	ctx, cancel := s.h.Ctx()
	defer cancel()
	confirmed, err := s.svc.ConfirmBooking(ctx, s.memberContext(), hold.Booking.ID)
	if err != nil || confirmed.Booking.ServiceRequestID == nil {
		t.Fatalf("confirm: %v", err)
	}
	requestID := *confirmed.Booking.ServiceRequestID
	s.decideRequest(t, requestID, servicerequestdomain.StatusApproved)
	var payload []byte
	if err := s.h.Admin.QueryRow(ctx, `SELECT payload_json FROM system.outbox_event
		WHERE tenant_id=$1 AND event_type=$2 AND aggregate_id=$3
		ORDER BY occurred_at DESC LIMIT 1`, s.tenant, servicerequestapp.DecidedEvent, requestID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	deps := s.deps
	read := &transientEvidenceOnce{BookingRepository: accommodationpg.NewBookings()}
	deps.Bookings = read
	s.svc, err = application.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	delivery := outbox.Delivery{TenantID: uuid.NullUUID{UUID: s.tenant, Valid: true}, Payload: payload}
	if err := s.svc.HandleServiceRequestDecided(ctx, delivery); err == nil ||
		outbox.KindOf(err) != outbox.KindTransient || !read.failed {
		t.Fatalf("evidence read failure = %v, want transient retry", err)
	}
	var authorizations int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM service.authorization WHERE tenant_id=$1`, s.tenant).Scan(&authorizations); err != nil {
		t.Fatal(err)
	}
	if authorizations != 0 {
		t.Fatalf("transient evidence failure created %d authorizations", authorizations)
	}
	if err := s.svc.HandleServiceRequestDecided(ctx, delivery); err != nil {
		t.Fatalf("evidence retry: %v", err)
	}
	final, err := s.svc.GetBooking(ctx, s.deskContext(), hold.Booking.ID)
	if err != nil || final.Booking.Status != "CONFIRMED" {
		t.Fatalf("evidence retry booking = %+v %v", final.Booking, err)
	}
}

func TestNightConversionLegacyNonunitApprovalRefusesBeforeAuthorization(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.putLodgingTerms(t)
	s.replaceNightPlan(t, "4", "2")
	hold, _ := s.conversionHold(t, "2026-06-17")
	ctx, cancel := s.h.Ctx()
	defer cancel()
	confirmed, err := s.svc.ConfirmBooking(ctx, s.memberContext(), hold.Booking.ID)
	if err != nil || confirmed.Booking.ServiceRequestID == nil {
		t.Fatalf("request before legacy simulation: %v", err)
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE accommodation.booking
		SET quote_snapshot=(quote_snapshot - 'nightConversion') || '{"version":2}'::jsonb
		WHERE tenant_id=$1 AND id=$2`, s.tenant, hold.Booking.ID); err != nil {
		t.Fatal(err)
	}
	requestID := *confirmed.Booking.ServiceRequestID
	s.decideRequest(t, requestID, servicerequestdomain.StatusApproved)
	var payload []byte
	if err := s.h.Admin.QueryRow(ctx, `SELECT payload_json FROM system.outbox_event
		WHERE tenant_id=$1 AND event_type=$2 AND aggregate_id=$3
		ORDER BY occurred_at DESC LIMIT 1`, s.tenant, servicerequestapp.DecidedEvent, requestID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	delivery := outbox.Delivery{TenantID: uuid.NullUUID{UUID: s.tenant, Valid: true}, Payload: payload}
	for attempt := 0; attempt < 2; attempt++ {
		if err := s.svc.HandleServiceRequestDecided(ctx, delivery); err == nil ||
			outbox.KindOf(err) != outbox.KindPermanent {
			t.Fatalf("legacy nonunit approval attempt %d = %v, want permanent stale", attempt+1, err)
		}
	}
	var authorizations int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM service.authorization WHERE tenant_id=$1`, s.tenant).Scan(&authorizations); err != nil {
		t.Fatal(err)
	}
	if authorizations != 0 {
		t.Fatalf("legacy nonunit approval wrote %d authorizations", authorizations)
	}
	current, err := s.svc.GetBooking(ctx, s.deskContext(), hold.Booking.ID)
	if err != nil || current.Booking.AuthorizationID != nil {
		t.Fatalf("legacy nonunit booking effects = %+v %v", current.Booking, err)
	}
}

func TestNightConversionFamilySharedPrincipalAccountStaysBound(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.putLodgingTerms(t)
	s.replaceNightPlan(t, "4", "2", true)
	principalEnrollment := s.enrollment
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var principalMembership, dependant, dependantMembership, dependantEnrollment uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `SELECT sponsor_membership_id FROM benefit.enrollment
		WHERE tenant_id=$1 AND id=$2`, s.tenant, principalEnrollment).Scan(&principalMembership); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO party.person
		(tenant_id,first_name,last_name,normalized_name)
		VALUES ($1,'Shared','Dependant','shared dependant') RETURNING id`, s.tenant).Scan(&dependant); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO party.sponsor_membership
		(tenant_id,person_id,sponsor_tenant_organization_id,principal_membership_id,
		 membership_type,status,valid_period)
		VALUES ($1,$2,$3,$4,'MEMBER','ACTIVE',daterange('2026-01-01',NULL,'[)')) RETURNING id`,
		s.tenant, dependant, s.sponsor, principalMembership).Scan(&dependantMembership); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO benefit.enrollment
		(tenant_id,sponsor_membership_id,plan_id,status,valid_period)
		VALUES ($1,$2,$3,'ACTIVE',daterange('2026-01-01',NULL,'[)')) RETURNING id`,
		s.tenant, dependantMembership, s.plan).Scan(&dependantEnrollment); err != nil {
		t.Fatal(err)
	}
	s.person, s.enrollment = dependant, dependantEnrollment
	quote := s.conversionQuote(t, "2026-06-17", "4", "2")
	if quote == nil || conversionPayerNights(quote) != "900,900" {
		t.Fatalf("shared principal quote = %+v", quote)
	}
	hold, snapshot := s.conversionHold(t, "2026-06-17")
	if snapshot.NightConversion.AccountID != s.account || hold.Booking.EnrollmentID != dependantEnrollment {
		t.Fatalf("shared hold selected account/enrollment = %+v %+v", snapshot.NightConversion, hold.Booking)
	}
	var holder uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `SELECT enrollment_id FROM benefit.entitlement_account
		WHERE tenant_id=$1 AND id=$2`, s.tenant, s.account).Scan(&holder); err != nil {
		t.Fatal(err)
	}
	if holder != principalEnrollment || holder == dependantEnrollment {
		t.Fatalf("shared account holder %s, want principal %s", holder, principalEnrollment)
	}
	confirmed, err := s.svc.ConfirmBooking(ctx, s.memberContext(), hold.Booking.ID)
	if err != nil || confirmed.Booking.ServiceRequestID == nil {
		t.Fatalf("shared confirmation: %v", err)
	}
	s.decideRequest(t, *confirmed.Booking.ServiceRequestID, servicerequestdomain.StatusApproved)
	s.deliverDecision(t, *confirmed.Booking.ServiceRequestID)
	final, err := s.svc.GetBooking(ctx, s.deskContext(), hold.Booking.ID)
	if err != nil || final.Booking.AuthorizationID == nil {
		t.Fatalf("shared approval: %+v %v", final.Booking, err)
	}
	issued, err := s.svc.IssueBookingVoucher(ctx, s.memberContext(), hold.Booking.ID)
	if err != nil {
		t.Fatalf("shared voucher: %v", err)
	}
	s.clock.At(t, atCheckIn)
	if _, err := s.svc.CheckInBooking(ctx, s.deskContext(), hold.Booking.ID,
		application.CheckInInput{Token: issued.Token}); err != nil {
		t.Fatalf("shared check in: %v", err)
	}
	s.clock.At(t, "2026-06-17T09:00:00Z")
	if _, err := s.svc.CheckOutBooking(ctx, s.deskContext(), hold.Booking.ID,
		application.CheckOutInput{}); err != nil {
		t.Fatalf("shared checkout: %v", err)
	}
	var available, reserved, consumed string
	if err := s.h.Admin.QueryRow(ctx, `SELECT available_quantity::text,reserved_quantity::text,consumed_quantity::text
		FROM benefit.entitlement_account WHERE tenant_id=$1 AND id=$2`, s.tenant, s.account).
		Scan(&available, &reserved, &consumed); err != nil {
		t.Fatal(err)
	}
	if available != "0.000000" || reserved != "0.000000" || consumed != "4.000000" {
		t.Fatalf("shared principal ledger = %s/%s/%s, want 0/0/4", available, reserved, consumed)
	}
}

func (s *server) approvedConversionBooking(t *testing.T, checkout, balance, factor string) application.BookingView {
	t.Helper()
	s.putLodgingTerms(t)
	s.replaceNightPlan(t, balance, factor)
	hold, _ := s.conversionHold(t, checkout)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	confirmed, err := s.svc.ConfirmBooking(ctx, s.memberContext(), hold.Booking.ID)
	if err != nil || confirmed.Booking.ServiceRequestID == nil {
		t.Fatalf("conversion confirm: %v", err)
	}
	s.decideRequest(t, *confirmed.Booking.ServiceRequestID, servicerequestdomain.StatusApproved)
	s.deliverDecision(t, *confirmed.Booking.ServiceRequestID)
	final, err := s.svc.GetBooking(ctx, s.deskContext(), hold.Booking.ID)
	if err != nil || final.Booking.Status != "CONFIRMED" || final.Booking.AuthorizationID == nil {
		t.Fatalf("conversion approve: %+v %v", final.Booking, err)
	}
	return final
}

func (s *server) assertConversionLedger(t *testing.T, available, reserved, consumed string) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var gotAvailable, gotReserved, gotConsumed, granted string
	if err := s.h.Admin.QueryRow(ctx, `SELECT available_quantity::text,reserved_quantity::text,
		consumed_quantity::text,total_granted::text FROM benefit.entitlement_account
		WHERE tenant_id=$1 AND id=$2`, s.tenant, s.account).
		Scan(&gotAvailable, &gotReserved, &gotConsumed, &granted); err != nil {
		t.Fatal(err)
	}
	if gotAvailable != available || gotReserved != reserved || gotConsumed != consumed ||
		granted != "4.000000" {
		t.Fatalf("mapped ledger = %s/%s/%s total=%s, want %s/%s/%s total4",
			gotAvailable, gotReserved, gotConsumed, granted, available, reserved, consumed)
	}
	// The database's conservation CHECK is accompanied by an independent assertion
	// over all three exact counters, including fractional or partial movements.
	var conserved bool
	if err := s.h.Admin.QueryRow(ctx, `SELECT total_granted = available_quantity +
		reserved_quantity + consumed_quantity + expired_quantity
		FROM benefit.entitlement_account WHERE tenant_id=$1 AND id=$2`, s.tenant, s.account).Scan(&conserved); err != nil {
		t.Fatal(err)
	}
	if !conserved {
		t.Fatal("mapped account failed exact conservation")
	}
}

func TestNightConversionFreeAndPenalizedCancellationConserveUnits(t *testing.T) {
	for _, tc := range []struct {
		name, at, available, consumed string
		free                          bool
	}{
		{"free", insideFreeWindow, "4.000000", "0.000000", true},
		{"penalized", afterFreeWindow, "2.000000", "2.000000", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t)
			s.clock.At(t, tc.at)
			booking := s.approvedConversionBooking(t, "2026-06-17", "4", "2")
			s.assertConversionLedger(t, "0.000000", "4.000000", "0.000000")
			ctx, cancel := s.h.Ctx()
			defer cancel()
			result, err := s.svc.CancelBooking(ctx, s.memberContext(), booking.Booking.ID, "")
			if err != nil {
				t.Fatalf("cancel: %v", err)
			}
			if result.Quote.Free != tc.free {
				t.Fatalf("free = %t, want %t", result.Quote.Free, tc.free)
			}
			s.assertConversionLedger(t, tc.available, "0.000000", tc.consumed)
			if _, err := s.svc.CancelBooking(ctx, s.memberContext(), booking.Booking.ID, ""); err == nil {
				t.Fatal("repeat cancellation unexpectedly succeeded")
			}
			s.assertConversionLedger(t, tc.available, "0.000000", tc.consumed)
		})
	}
}

func TestNightConversionNoShowConsumesMappedUnitsOnce(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, insideFreeWindow)
	booking := s.approvedConversionBooking(t, "2026-06-17", "4", "2")
	clerk := s.h.CreateActor("mapped-hotel-desk", "Mapped Desk")
	reviewer := s.h.CreateActor("mapped-payer-reviewer", "Mapped Reviewer")
	evidence := s.linkCleanBookingDocument(t, booking.Booking.ID)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	s.clock.At(t, afterCheckInCloses)
	if _, err := s.svc.ReportNoShow(ctx, s.providerContext(clerk), booking.Booking.ID,
		application.ReportNoShowInput{EvidenceDocumentID: &evidence}); err != nil {
		t.Fatalf("report no-show: %v", err)
	}
	s.assertConversionLedger(t, "0.000000", "4.000000", "0.000000")
	reviewed, err := s.svc.ReviewNoShow(ctx, s.payerContext(reviewer), booking.Booking.ID,
		application.ReviewNoShowInput{Status: application.NoShowConfirmed})
	if err != nil || reviewed.Report.ConsumedNights != 2 {
		t.Fatalf("mapped no-show: %+v %v", reviewed.Report, err)
	}
	s.assertConversionLedger(t, "0.000000", "0.000000", "4.000000")
	if _, err := s.svc.ReviewNoShow(ctx, s.payerContext(reviewer), booking.Booking.ID,
		application.ReviewNoShowInput{Status: application.NoShowConfirmed}); err == nil {
		t.Fatal("repeat no-show decision succeeded")
	}
	s.assertConversionLedger(t, "0.000000", "0.000000", "4.000000")
}

func TestNightConversionHalfFactorSubmissionAndFullCheckout(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	booking := s.approvedConversionBooking(t, checkOut, "1.5", "0.5")
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var factor, approved, reserved string
	if err := s.h.Admin.QueryRow(ctx, `SELECT ai.entitlement_unit_factor::text,
		ai.approved_quantity::text,r.quantity::text
		FROM service.authorization_item ai JOIN benefit.entitlement_reservation r
		 ON r.tenant_id=ai.tenant_id AND r.id=ai.entitlement_reservation_id
		WHERE ai.tenant_id=$1 AND ai.authorization_id=$2`, s.tenant, *booking.Booking.AuthorizationID).
		Scan(&factor, &approved, &reserved); err != nil {
		t.Fatal(err)
	}
	if factor != "0.500000" || approved != "3.000000" || reserved != "1.500000" {
		t.Fatalf("half-factor adoption factor/service/reserved = %s/%s/%s, want .5/3/1.5", factor, approved, reserved)
	}
	issued, err := s.svc.IssueBookingVoucher(ctx, s.memberContext(), booking.Booking.ID)
	if err != nil {
		t.Fatalf("half-factor voucher: %v", err)
	}
	s.clock.At(t, atCheckIn)
	if _, err := s.svc.CheckInBooking(ctx, s.deskContext(), booking.Booking.ID,
		application.CheckInInput{Token: issued.Token}); err != nil {
		t.Fatalf("half-factor check in: %v", err)
	}
	s.clock.At(t, afterTheWholeStay)
	out, err := s.svc.CheckOutBooking(ctx, s.deskContext(), booking.Booking.ID,
		application.CheckOutInput{})
	if err != nil || out.Booking.ActualNights == nil || *out.Booking.ActualNights != 3 {
		t.Fatalf("half-factor full checkout: %+v %v", out.Booking, err)
	}
	var available, held, consumed string
	if err := s.h.Admin.QueryRow(ctx, `SELECT available_quantity::text,reserved_quantity::text,
		consumed_quantity::text FROM benefit.entitlement_account WHERE tenant_id=$1 AND id=$2`,
		s.tenant, s.account).Scan(&available, &held, &consumed); err != nil {
		t.Fatal(err)
	}
	if available != "0.000000" || held != "0.000000" || consumed != "1.500000" {
		t.Fatalf("half-factor checkout ledger = %s/%s/%s, want 0/0/1.5", available, held, consumed)
	}
}

func TestNightConversionMalformedPrivateSnapshotRefusesBeforeRequest(t *testing.T) {
	for _, tc := range []struct{ name, edit string }{
		{"zero factor", `jsonb_set(quote_snapshot,'{nightConversion,unitFactor}','"0"'::jsonb)`},
		{"wrong account", `jsonb_set(quote_snapshot,'{nightConversion,accountId}',to_jsonb($3::text))`},
		{"missing conversion", `quote_snapshot - 'nightConversion'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t)
			s.clock.At(t, "2026-06-13T09:00:00Z")
			s.replaceNightPlan(t, "4", "2")
			hold, _ := s.conversionHold(t, "2026-06-17")
			read := s.do(t, http.MethodGet, "/api/v1/accommodation/bookings/"+hold.Booking.ID.String(),
				bookerPermissions, nil, s.memberHeaders()...)
			if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"version":1`) ||
				strings.Contains(read.Body.String(), "nightConversion") {
				t.Fatalf("v3 public projection = %d %s", read.Code, read.Body.String())
			}
			ctx, cancel := s.h.Ctx()
			defer cancel()
			query := `UPDATE accommodation.booking SET quote_snapshot=` + tc.edit + ` WHERE tenant_id=$1 AND id=$2`
			if tc.name == "wrong account" {
				_, err := s.h.Admin.Exec(ctx, query, s.tenant, hold.Booking.ID, uuid.NewString())
				if err != nil {
					t.Fatal(err)
				}
			} else if _, err := s.h.Admin.Exec(ctx, query, s.tenant, hold.Booking.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.svc.ConfirmBooking(ctx, s.memberContext(), hold.Booking.ID); !errors.Is(err, application.ErrQuoteStale) {
				t.Fatalf("malformed %s confirmation = %v, want stale quote", tc.name, err)
			}
			var requests int
			if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM service.service_request WHERE tenant_id=$1`, s.tenant).Scan(&requests); err != nil {
				t.Fatal(err)
			}
			if requests != 0 {
				t.Fatalf("malformed %s wrote %d requests", tc.name, requests)
			}
			if _, err := s.svc.ReleaseHold(ctx, s.memberContext(), hold.Booking.ID); err != nil {
				t.Fatalf("malformed hold release: %v", err)
			}
		})
	}
}
