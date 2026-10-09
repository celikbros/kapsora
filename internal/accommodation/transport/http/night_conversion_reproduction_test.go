package accommodationhttp_test

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/accommodation/application"
)

// These tests deliberately fail against the accepted factor-1 implementation. They
// run only in the isolated diagnostic CI step until conversion support is implemented.
func requireNightConversionReproduction(t *testing.T) {
	t.Helper()
	if os.Getenv("KAPSORA_TEST_NIGHT_CONVERSION_REPRODUCTION") != "1" {
		t.Skip("NIGHT conversion reproduction is isolated from the required green suite")
	}
}

// replaceNightPlan preserves the existing member, room service, provider contract and
// price. A new version is mapped while DRAFT, then published, and the old enrollment is
// suspended so the command has exactly one active funding choice. Both accounts remain
// in the database, making accidental use of the original account observable.
func (s *server) replaceNightPlan(t *testing.T, balance, factor string) uuid.UUID {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var plan, version, definition, enrollment, account uuid.UUID
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
		(tenant_id,plan_version_id,code,name,unit_type,period_type,initial_quantity)
		VALUES ($1,$2,'KONAKLAMA_GECE','Mapped nights','NIGHT','CALENDAR_YEAR',$3::text::numeric)
		RETURNING id`, s.tenant, version, balance).Scan(&definition); err != nil {
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
		 AND a.enrollment_id=e.enrollment_id
		WHERE e.tenant_id=$1 AND e.id=$2`,
		s.tenant, *result.EvaluationId, s.definition, balance, factor).
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
	requireNightConversionReproduction(t)
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
	requireNightConversionReproduction(t)
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
	requireNightConversionReproduction(t)
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
