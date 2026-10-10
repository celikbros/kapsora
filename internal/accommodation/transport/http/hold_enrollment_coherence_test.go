package accommodationhttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/accommodation/application"
	accommodationpg "github.com/celikbros/kapsora/internal/accommodation/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/platform/db"
)

// addEnrollment creates a second published plan with the same service mapping and
// entitlement code as the first. Its later validity start opposes the chosen UUID order.
func (s *server) addEnrollment(t *testing.T, program uuid.UUID, id uuid.UUID, funded int) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var plan, version, definition, account uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO benefit.plan (tenant_id,program_id,code,name,status)
		VALUES ($1,$2,'SECOND','Second plan','ACTIVE') RETURNING id`, s.tenant, program).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO benefit.plan_version
		(tenant_id,plan_id,version_no,status,valid_period)
		VALUES ($1,$2,1,'DRAFT',daterange('2026-04-01','2027-01-01','[)')) RETURNING id`,
		s.tenant, plan).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO benefit.entitlement_definition
		(tenant_id,plan_version_id,code,name,unit_type,period_type,initial_quantity)
		VALUES ($1,$2,'KONAKLAMA_GECE','Second night balance','NIGHT','CALENDAR_YEAR',$3)
		RETURNING id`, s.tenant, version, funded).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	s.h.AdminExec(`INSERT INTO benefit.service_entitlement_mapping
		(tenant_id,plan_version_id,service_definition_id,entitlement_definition_id,unit_factor)
		VALUES ($1,$2,$3,$4,1)`, s.tenant, version, s.definition, definition)
	s.h.AdminExec(`UPDATE benefit.plan_version SET status='PUBLISHED',published_at=clock_timestamp(),
		published_by=$3 WHERE tenant_id=$1 AND id=$2`, s.tenant, version, s.actor)
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO benefit.enrollment
		(id,tenant_id,sponsor_membership_id,plan_id,status,valid_period)
		SELECT $3,tenant_id,sponsor_membership_id,$4,'ACTIVE',
			daterange('2026-04-01',NULL,'[)') FROM benefit.enrollment
		WHERE tenant_id=$1 AND id=$2 RETURNING id`, s.tenant, s.enrollment, id, plan).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO benefit.entitlement_account
		(tenant_id,enrollment_id,entitlement_definition_id,benefit_period,total_granted,available_quantity)
		VALUES ($1,$2,$3,daterange('2026-01-01','2027-01-01','[)'),$4,$4) RETURNING id`,
		s.tenant, id, definition, funded).Scan(&account); err != nil {
		t.Fatal(err)
	}
	s.h.AdminExec(`INSERT INTO benefit.entitlement_ledger
		(tenant_id,entitlement_account_id,movement_type,effective_at,delta_total,delta_available,
		 reference_type,reference_id,idempotency_key)
		VALUES ($1,$2,'GRANT',clock_timestamp(),$3,$3,'ENROLLMENT',$4,'grant:second')`,
		s.tenant, account, funded, id)
	return id, account
}

func (s *server) assertNoHoldEffects(t *testing.T) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var bookings, queue, reservations, reserves int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM accommodation.booking WHERE tenant_id=$1`, s.tenant).Scan(&bookings); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM accommodation.waitlist_entry WHERE tenant_id=$1`, s.tenant).Scan(&queue); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM benefit.entitlement_reservation WHERE tenant_id=$1`, s.tenant).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM benefit.entitlement_ledger
		WHERE tenant_id=$1 AND movement_type='RESERVE'`, s.tenant).Scan(&reserves); err != nil {
		t.Fatal(err)
	}
	_, held, _ := s.inventoryOf(t, s.roomType, checkIn)
	if bookings != 0 || queue != 0 || reservations != 0 || reserves != 0 || held != 1 {
		t.Fatalf("refusal effects: bookings=%d queue=%d reservations=%d reserves=%d held=%d",
			bookings, queue, reservations, reserves, held)
	}
}

func TestHoldAmbiguousProgramsRefuseBeforeEffects(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.grantNights(t, 10)
	payer, _ := s.addCompetingPayerPrice(t, 200)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var program uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO benefit.program
		(tenant_id,sponsor_tenant_organization_id,payer_tenant_organization_id,
		 code,name,program_type,status,valid_period)
		SELECT tenant_id,sponsor_tenant_organization_id,$3,'SECOND','Second program',
		program_type,'ACTIVE',valid_period FROM benefit.program WHERE tenant_id=$1 AND id=$2
		RETURNING id`, s.tenant, s.program, payer).Scan(&program); err != nil {
		t.Fatal(err)
	}
	// The new enrollment has a higher UUID but a later validity start: old hold SQL
	// chooses A, while old eligibility chooses B and its larger balance.
	s.addEnrollment(t, program, uuid.MustParse("ffffffff-ffff-ffff-ffff-fffffffffff0"), 20)
	if quote := s.payerBoundaryQuote(t, nil); quote == nil || quote.TotalAmount != "12000" {
		t.Fatalf("omitted-program search lost A+B payer union: %+v", quote)
	}
	body := map[string]any{"roomTypeId": s.roomType.String(), "checkIn": checkIn,
		"checkOut": checkOut, "adults": 2, "personId": s.person.String()}
	rec := s.do(t, http.MethodPost, "/api/v1/accommodation/holds", bookerPermissions,
		body, s.memberHeaders()...)
	if rec.Code != http.StatusUnprocessableEntity || !containsProblemCode(rec.Body.Bytes(), "ENROLLMENT_MULTIPLE") {
		t.Fatalf("ambiguous hold = %d: %s", rec.Code, rec.Body.String())
	}
	join := map[string]any{"propertyId": s.property.String(), "roomTypeId": s.roomType.String(),
		"checkIn": checkIn, "checkOut": checkOut, "adults": 2, "personId": s.person.String()}
	rec = s.do(t, http.MethodPost, "/api/v1/accommodation/waitlist", bookerPermissions,
		join, s.memberHeaders()...)
	if rec.Code != http.StatusUnprocessableEntity || !containsProblemCode(rec.Body.Bytes(), "ENROLLMENT_MULTIPLE") {
		t.Fatalf("ambiguous join = %d: %s", rec.Code, rec.Body.String())
	}
	s.assertNoHoldEffects(t)
}

func TestHoldExplicitProgramPinsEvaluationPriceAndAccount(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.putLodgingTermsWithHold(t, int32Ptr(30))
	payer, _ := s.addCompetingPayerPrice(t, 200)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var program uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO benefit.program
		(tenant_id,sponsor_tenant_organization_id,payer_tenant_organization_id,
		 code,name,program_type,status,valid_period)
		SELECT tenant_id,sponsor_tenant_organization_id,$3,'SECOND','Second program',
		program_type,'ACTIVE',valid_period FROM benefit.program WHERE tenant_id=$1 AND id=$2
		RETURNING id`, s.tenant, s.program, payer).Scan(&program); err != nil {
		t.Fatal(err)
	}
	_, otherAccount := s.addEnrollment(t, program, uuid.MustParse("ffffffff-ffff-ffff-ffff-fffffffffff0"), 20)
	if quote := s.payerBoundaryQuote(t, &s.program); quote == nil || quote.TotalAmount != "3000" {
		t.Fatalf("explicit A search price = %+v", quote)
	}
	in := s.holdInput(s.person, s.roomType)
	in.ProgramID = &s.program
	held, err := s.svc.CreateHold(ctx, s.memberContext(), in)
	if err != nil {
		t.Fatal(err)
	}
	if held.Booking.EnrollmentID != s.enrollment || held.Booking.ProgramID != s.program {
		t.Fatalf("booking enrollment/program = %s/%s", held.Booking.EnrollmentID, held.Booking.ProgramID)
	}
	var snapshot application.QuoteSnapshot
	if err := json.Unmarshal(held.Booking.QuoteSnapshot, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.TotalAmount != "3000" || snapshot.PayerAmount != "1800" ||
		snapshot.MemberAmount != "1200" || snapshot.CoveredNights != 2 ||
		snapshot.FirstNightContractVersionID == nil || *snapshot.FirstNightContractVersionID != s.contractVersion ||
		snapshot.EvaluationID == nil {
		t.Fatalf("incoherent frozen quote: %+v", snapshot)
	}
	if held.Booking.HoldExpiresAt == nil || !held.Booking.HoldExpiresAt.Equal(s.clock.Now().Add(30*time.Minute)) {
		t.Fatalf("wrong contract hold deadline: %v", held.Booking.HoldExpiresAt)
	}
	var evaluated, version, account, reservedEnrollment uuid.UUID
	var quantity string
	if err := s.h.Admin.QueryRow(ctx, `SELECT e.enrollment_id,e.plan_version_id,
		 r.entitlement_account_id,a.enrollment_id,r.quantity::text
		 FROM benefit.eligibility_evaluation e
		 JOIN accommodation.booking b ON b.tenant_id=e.tenant_id
		 JOIN benefit.entitlement_reservation r ON r.tenant_id=b.tenant_id AND r.id=b.entitlement_reservation_id
		 JOIN benefit.entitlement_account a ON a.tenant_id=r.tenant_id AND a.id=r.entitlement_account_id
		 WHERE e.tenant_id=$1 AND e.id=$2 AND b.id=$3`,
		s.tenant, *snapshot.EvaluationID, held.Booking.ID).
		Scan(&evaluated, &version, &account, &reservedEnrollment, &quantity); err != nil {
		t.Fatal(err)
	}
	if evaluated != s.enrollment || version != s.planVersion || account != s.account ||
		reservedEnrollment != s.enrollment || quantity != "2.000000" {
		t.Fatalf("evaluation/reservation = %s/%s/%s/%s/%s", evaluated, version, account, reservedEnrollment, quantity)
	}
	var otherReserved string
	if err := s.h.Admin.QueryRow(ctx, `SELECT reserved_quantity::text FROM benefit.entitlement_account
		WHERE tenant_id=$1 AND id=$2`, s.tenant, otherAccount).Scan(&otherReserved); err != nil {
		t.Fatal(err)
	}
	if otherReserved != "0.000000" {
		t.Fatalf("other program funded hold: %s", otherReserved)
	}
	var reserveMovements int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM benefit.entitlement_ledger
		WHERE tenant_id=$1 AND reference_id=$2 AND movement_type='RESERVE'`,
		s.tenant, held.Booking.ID).Scan(&reserveMovements); err != nil {
		t.Fatal(err)
	}
	if reserveMovements != 1 {
		t.Fatalf("hold wrote %d reserve movements, want one", reserveMovements)
	}
	s.assertConservation(t, "explicit enrollment hold")
	if _, err := s.svc.ReleaseHold(ctx, s.memberContext(), held.Booking.ID); err != nil {
		t.Fatalf("release selected enrollment hold: %v", err)
	}
	available, reserved, consumed := s.balances(t)
	if available != "2.000000" || reserved != "0.000000" || consumed != "0.000000" {
		t.Fatalf("release changed A balance to %s/%s/%s", available, reserved, consumed)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT reserved_quantity::text FROM benefit.entitlement_account
		WHERE tenant_id=$1 AND id=$2`, s.tenant, otherAccount).Scan(&otherReserved); err != nil {
		t.Fatal(err)
	}
	if otherReserved != "0.000000" {
		t.Fatalf("release changed B balance: %s", otherReserved)
	}
	s.assertConservation(t, "explicit enrollment release")
}

func TestHoldSameProgramAmbiguityAndPinnedWaitlistContinuity(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.grantNights(t, 10)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	join, err := s.svc.JoinWaitlist(ctx, s.memberContext(), application.JoinWaitlistInput{
		PersonID: s.person, PropertyID: s.property, RoomTypeID: &s.roomType, ProgramID: &s.program,
		CheckIn: mustDay(checkIn), CheckOut: mustDay(checkOut), Adults: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if join.Entry.EnrollmentID != s.enrollment {
		t.Fatalf("saved waitlist enrollment = %s", join.Entry.EnrollmentID)
	}
	// A lower ID would win the old SQL LIMIT 1 even though the queue saved A.
	s.addEnrollment(t, s.program, uuid.MustParse("00000000-0000-0000-0000-000000000001"), 20)
	in := s.holdInput(s.person, s.roomType)
	in.ProgramID = &s.program
	if _, err := s.svc.CreateHold(ctx, s.memberContext(), in); !errors.Is(err, application.ErrEnrollmentMultiple) {
		t.Fatalf("ordinary same-program hold = %v, want ambiguity", err)
	}
	if _, err := s.svc.JoinWaitlist(ctx, s.memberContext(), application.JoinWaitlistInput{
		PersonID: s.person, PropertyID: s.property, RoomTypeID: &s.roomType, ProgramID: &s.program,
		CheckIn: mustDay(checkIn), CheckOut: mustDay(checkOut), Adults: 2,
	}); !errors.Is(err, application.ErrEnrollmentMultiple) {
		t.Fatalf("ordinary same-program join = %v, want ambiguity", err)
	}
	offered, err := s.svc.OfferWaitlistRooms(ctx, s.clock.Now())
	if err != nil || offered != 1 {
		t.Fatalf("pinned same-program offer = %d, %v", offered, err)
	}
	entry := s.waitlistEntry(t, join.Entry.ID)
	if entry.OfferedBookingID == nil {
		t.Fatal("valid pinned enrollment did not receive a booking")
	}
	held, err := s.svc.GetBooking(ctx, s.memberContext(), *entry.OfferedBookingID)
	if err != nil || held.Booking.EnrollmentID != s.enrollment {
		t.Fatalf("pinned offer changed enrollment: %+v, %v", held.Booking, err)
	}
	var reservedAccount uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `SELECT r.entitlement_account_id
		FROM accommodation.booking b JOIN benefit.entitlement_reservation r
		ON r.tenant_id=b.tenant_id AND r.id=b.entitlement_reservation_id
		WHERE b.tenant_id=$1 AND b.id=$2`, s.tenant, held.Booking.ID).Scan(&reservedAccount); err != nil {
		t.Fatal(err)
	}
	if reservedAccount != s.account {
		t.Fatalf("pinned offer reserved account %s, want %s", reservedAccount, s.account)
	}
	s.assertConservation(t, "pinned same-program offer")
}

func TestPinnedEnrollmentUnavailableLeavesQueueWaiting(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change string
	}{
		{"suspended", `UPDATE benefit.enrollment SET status='SUSPENDED' WHERE tenant_id=$1 AND id=$2`},
		{"expired", `UPDATE benefit.enrollment SET valid_period=daterange('2026-01-01','2026-06-01','[)') WHERE tenant_id=$1 AND id=$2`},
		{"future", `UPDATE benefit.enrollment SET valid_period=daterange('2026-07-01',NULL,'[)') WHERE tenant_id=$1 AND id=$2`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t)
			s.clock.At(t, "2026-06-13T09:00:00Z")
			s.grantNights(t, 10)
			ctx, cancel := s.h.Ctx()
			defer cancel()
			join, err := s.svc.JoinWaitlist(ctx, s.memberContext(), application.JoinWaitlistInput{
				PersonID: s.person, PropertyID: s.property, RoomTypeID: &s.roomType,
				CheckIn: mustDay(checkIn), CheckOut: mustDay(checkOut), Adults: 2,
			})
			if err != nil {
				t.Fatal(err)
			}
			// The second funded plan is available to an unpinned command; the
			// scheduler must still leave this entry on its saved A enrollment.
			s.addEnrollment(t, s.program,
				uuid.MustParse("00000000-0000-0000-0000-000000000001"), 20)
			if _, err := s.h.Admin.Exec(ctx, tc.change, s.tenant, s.enrollment); err != nil {
				t.Fatal(err)
			}
			offered, err := s.svc.OfferWaitlistRooms(ctx, s.clock.Now())
			if err != nil || offered != 0 {
				t.Fatalf("unavailable pinned offer = %d, %v", offered, err)
			}
			entry := s.waitlistEntry(t, join.Entry.ID)
			if entry.Status != application.WaitlistWaiting || entry.OfferedBookingID != nil {
				t.Fatalf("unavailable pinned enrollment changed queue: %+v", entry)
			}
			var bookings, reservations int
			if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM accommodation.booking WHERE tenant_id=$1`, s.tenant).Scan(&bookings); err != nil {
				t.Fatal(err)
			}
			if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM benefit.entitlement_reservation WHERE tenant_id=$1`, s.tenant).Scan(&reservations); err != nil {
				t.Fatal(err)
			}
			if bookings != 0 || reservations != 0 {
				t.Fatalf("unavailable pinned enrollment wrote bookings=%d reservations=%d", bookings, reservations)
			}
		})
	}
}

func TestPinnedUnfundedEnrollmentDoesNotBorrowSecondPlan(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	ctx, cancel := s.h.Ctx()
	defer cancel()
	join, err := s.svc.JoinWaitlist(ctx, s.memberContext(), application.JoinWaitlistInput{
		PersonID: s.person, PropertyID: s.property, RoomTypeID: &s.roomType,
		ProgramID: &s.program, CheckIn: mustDay(checkIn), CheckOut: mustDay(checkOut), Adults: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, otherAccount := s.addEnrollment(t, s.program,
		uuid.MustParse("00000000-0000-0000-0000-000000000001"), 20)
	if _, err := s.h.Admin.Exec(ctx, `UPDATE benefit.entitlement_account SET status='FROZEN'
		WHERE tenant_id=$1 AND id=$2`, s.tenant, s.account); err != nil {
		t.Fatal(err)
	}
	offered, err := s.svc.OfferWaitlistRooms(ctx, s.clock.Now())
	if err != nil || offered != 0 {
		t.Fatalf("unfunded pinned offer = %d, %v", offered, err)
	}
	entry := s.waitlistEntry(t, join.Entry.ID)
	if entry.Status != application.WaitlistWaiting || entry.OfferedBookingID != nil {
		t.Fatalf("unfunded pinned enrollment changed queue: %+v", entry)
	}
	var bookings, reservations int
	var otherReserved string
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM accommodation.booking WHERE tenant_id=$1`, s.tenant).Scan(&bookings); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM benefit.entitlement_reservation WHERE tenant_id=$1`, s.tenant).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT reserved_quantity::text FROM benefit.entitlement_account
		WHERE tenant_id=$1 AND id=$2`, s.tenant, otherAccount).Scan(&otherReserved); err != nil {
		t.Fatal(err)
	}
	if bookings != 0 || reservations != 0 || otherReserved != "0.000000" {
		t.Fatalf("borrowed second plan: bookings=%d reservations=%d other reserved=%s",
			bookings, reservations, otherReserved)
	}
}

func TestPinnedHoldRejectsWrongPersonProgramAndUnknownEnrollment(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var otherPerson, membership, wrongPersonEnrollment, otherProgram uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO party.person
		(tenant_id,first_name,last_name,normalized_name)
		VALUES ($1,'Other','Member','other member') RETURNING id`, s.tenant).Scan(&otherPerson); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO party.sponsor_membership
		(tenant_id,person_id,sponsor_tenant_organization_id,membership_type,status,valid_period)
		VALUES ($1,$2,$3,'MEMBER','ACTIVE',daterange('2026-01-01',NULL,'[)')) RETURNING id`,
		s.tenant, otherPerson, s.sponsor).Scan(&membership); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO benefit.enrollment
		(tenant_id,sponsor_membership_id,plan_id,status,valid_period)
		VALUES ($1,$2,$3,'ACTIVE',daterange('2026-01-01',NULL,'[)')) RETURNING id`,
		s.tenant, membership, s.plan).Scan(&wrongPersonEnrollment); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `INSERT INTO benefit.program
		(tenant_id,sponsor_tenant_organization_id,payer_tenant_organization_id,
		 code,name,program_type,status,valid_period)
		SELECT tenant_id,sponsor_tenant_organization_id,payer_tenant_organization_id,
		 'OTHER','Other program',program_type,'ACTIVE',valid_period
		 FROM benefit.program WHERE tenant_id=$1 AND id=$2 RETURNING id`,
		s.tenant, s.program).Scan(&otherProgram); err != nil {
		t.Fatal(err)
	}
	wrongProgramEnrollment, _ := s.addEnrollment(t, otherProgram,
		uuid.MustParse("ffffffff-ffff-ffff-ffff-fffffffffff0"), 20)
	for _, tc := range []struct {
		name string
		id   uuid.UUID
	}{
		{"wrong person", wrongPersonEnrollment},
		{"wrong program", wrongProgramEnrollment},
		{"unknown", uuid.New()},
		{"zero", uuid.Nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := s.holdInput(s.person, s.roomType)
			in.ProgramID = &s.program
			in.ExpectedEnrollmentID = &tc.id
			if _, err := s.svc.CreateHold(ctx, s.memberContext(), in); !errors.Is(err, application.ErrEnrollmentNotFound) {
				t.Fatalf("pinned %s = %v, want no enrollment", tc.name, err)
			}
		})
	}
	zeroProgram := uuid.Nil
	in := s.holdInput(s.person, s.roomType)
	in.ProgramID = &zeroProgram
	if _, err := s.svc.CreateHold(ctx, s.memberContext(), in); !errors.Is(err, application.ErrEnrollmentNotFound) {
		t.Fatalf("explicit zero program hold = %v, want no enrollment", err)
	}
	if _, err := s.svc.JoinWaitlist(ctx, s.memberContext(), application.JoinWaitlistInput{
		PersonID: s.person, PropertyID: s.property, RoomTypeID: &s.roomType,
		ProgramID: &zeroProgram, CheckIn: mustDay(checkIn), CheckOut: mustDay(checkOut), Adults: 2,
	}); !errors.Is(err, application.ErrEnrollmentNotFound) {
		t.Fatalf("explicit zero program join = %v, want no enrollment", err)
	}
	foreignTenant := s.h.CreateTenant("FOREIGN_PIN")
	if err := db.WithTenantTx(ctx, s.h.App, db.TenantContext{
		TenantID: foreignTenant, ActorID: s.actor,
	}, func(ctx context.Context, tx pgx.Tx) error {
		_, err := accommodationpg.NewBookings().PersonEnrollmentForStay(ctx, tx,
			foreignTenant, s.person, mustDay(checkIn), &s.program, &s.enrollment)
		if !errors.Is(err, application.ErrEnrollmentNotFound) {
			t.Fatalf("foreign tenant selected enrollment: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.assertNoHoldEffects(t)
}

func containsProblemCode(body []byte, code string) bool {
	var problem struct {
		Code string `json:"code"`
	}
	return json.Unmarshal(body, &problem) == nil && problem.Code == code
}
