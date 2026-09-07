package dbtests

import (
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// The booking schema of migration 000041 (WP-I6-02), with the application layer bypassed
// entirely: every statement below runs as the schema owner through the admin pool, so no Go
// code of ours is between them and the constraint.
//
// These are the rules the service is allowed to lean on. A rule the service can forget is
// not a rule, and the three the booking half stands on are all here: one live booking per
// person and arrival, a night count that cannot disagree with the dates, and a nightly split
// that cannot be a kuruş out.

// seedBooking adds the plan and the person a booking needs on top of the accommodation
// fixture, and returns everything a booking row references.
type bookingSeed struct {
	accommodationSeed
	person     uuid.UUID
	program    uuid.UUID
	enrollment uuid.UUID
}

func seedBooking(t *testing.T, h *dbtest.Harness, code string) bookingSeed { //nolint:funlen // one linear fixture reads better whole
	t.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()

	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		t.Helper()
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}

	s := bookingSeed{accommodationSeed: seedAccommodation(t, h, code)}
	sponsor := h.CreateTenantOrganization(s.tenant, "Sponsor", "SPONSOR")
	payer := h.CreateTenantOrganization(s.tenant, "Payer", "PAYER")

	h.AdminExec(`INSERT INTO party.membership_type (tenant_id, code, display_name)
	             VALUES ($1, 'MEMBER', 'Üye')`, s.tenant)
	scan(&s.person, "person", `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Deniz', 'Kara', 'deniz kara') RETURNING id`, s.tenant)
	var sponsorMembership uuid.UUID
	scan(&sponsorMembership, "sponsor membership", `
		INSERT INTO party.sponsor_membership (tenant_id, person_id, sponsor_tenant_organization_id,
		                                      membership_type, status, valid_period)
		VALUES ($1, $2, $3, 'MEMBER', 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		s.tenant, s.person, sponsor)

	h.AdminExec(`INSERT INTO benefit.program_type (tenant_id, code, display_name)
	             VALUES ($1, 'BENEFIT', 'Fayda')`, s.tenant)
	scan(&s.program, "program", `
		INSERT INTO benefit.program (tenant_id, sponsor_tenant_organization_id,
		                             payer_tenant_organization_id, code, name, program_type,
		                             status, valid_period)
		VALUES ($1, $2, $3, 'TATIL', 'Tatil', 'BENEFIT', 'ACTIVE',
		        daterange('2026-01-01', NULL, '[)')) RETURNING id`, s.tenant, sponsor, payer)
	var plan uuid.UUID
	scan(&plan, "plan", `
		INSERT INTO benefit.plan (tenant_id, program_id, code, name, status)
		VALUES ($1, $2, 'PLAN', 'Plan', 'ACTIVE') RETURNING id`, s.tenant, s.program)
	scan(&s.enrollment, "enrollment", `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status, valid_period)
		VALUES ($1, $2, $3, 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		s.tenant, sponsorMembership, plan)
	return s
}

// insertBooking writes one booking row directly, so a test can put the table into any state
// the constraints allow and prove the ones they do not.
func (s bookingSeed) insertBooking(t *testing.T, h *dbtest.Harness, reference, status,
	checkIn, checkOut string, nights int,
) (uuid.UUID, error) {
	t.Helper()
	ctx, cancel := h.Ctx()
	defer cancel()
	var id uuid.UUID
	err := h.Admin.QueryRow(ctx, `
		INSERT INTO accommodation.booking (
		    tenant_id, reference, person_id, enrollment_id, program_id, property_id,
		    room_type_id, check_in, check_out, nights, adults, children, status,
		    hold_expires_at, quote_snapshot, channel, confirmed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::date, $9::date, $10, 2, 0, $11,
		        CASE WHEN $11 = 'HOLD' THEN clock_timestamp() + interval '15 minutes' END,
		        '{}'::jsonb, 'MEMBER_PORTAL',
		        CASE WHEN $11 IN ('CONFIRMED','CHECKED_IN','COMPLETED','NO_SHOW')
		             THEN clock_timestamp() END)
		RETURNING id`,
		s.tenant, reference, s.person, s.enrollment, s.program, s.property, s.roomType,
		checkIn, checkOut, nights, status).Scan(&id)
	return id, err
}

// TestOneLiveBookingPerPersonRoomTypeAndArrival is the rule of v1.2 10.6 as the index states
// it, with no service anywhere near it.
//
// It is a partial unique index rather than a service check because the two callers it
// separates arrive at the same instant: a double-clicked confirm, or two browser tabs. Both
// read "nothing yet", both pass any check a service could make, and this index is what
// refuses the second insert. The predicate lists the four live statuses, so a cancelled or
// expired booking must not stop the member booking the same room again — which is the other
// half of the rule and the half a naive unique constraint would get wrong.
func TestOneLiveBookingPerPersonRoomTypeAndArrival(t *testing.T) {
	h := dbtest.New(t)
	s := seedBooking(t, h, "BK_LIVE")

	if _, err := s.insertBooking(t, h, "BK-20260601-AAAAAAAA", "HOLD",
		"2026-06-15", "2026-06-18", 3); err != nil {
		t.Fatalf("the first hold was refused: %v", err)
	}
	// A second live booking of the same room type arriving on the same day.
	_, err := s.insertBooking(t, h, "BK-20260601-BBBBBBBB", "HOLD",
		"2026-06-15", "2026-06-17", 2)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation,
		"a second live booking of one room type for one person on one arrival")

	// A CONFIRMED one is just as live, and so is PENDING_APPROVAL. CHECKED_IN is left out
	// of this loop only because it needs a check-in moment of its own; it is in the index's
	// predicate and WP-I6-03 will be the package that puts a booking into it.
	for _, status := range []string{"PENDING_APPROVAL", "CONFIRMED"} {
		_, err := s.insertBooking(t, h, "BK-20260601-CCCCCCCC", status,
			"2026-06-15", "2026-06-18", 3)
		dbtest.ExpectSQLState(t, err, dbtest.SQLStateUniqueViolation,
			"a "+status+" booking beside a live hold")
	}

	// History does not block a new booking. Once the hold is expired the member may book
	// the same room and the same arrival again, which is the whole reason the index is
	// partial.
	h.AdminExec(`
		UPDATE accommodation.booking SET status = 'EXPIRED', hold_expires_at = NULL
		 WHERE tenant_id = $1`, s.tenant)
	if _, err := s.insertBooking(t, h, "BK-20260601-DDDDDDDD", "HOLD",
		"2026-06-15", "2026-06-18", 3); err != nil {
		t.Fatalf("booking again after an expired hold was refused: %v", err)
	}
	// And a different arrival was never blocked at all.
	if _, err := s.insertBooking(t, h, "BK-20260601-EEEEEEEE", "HOLD",
		"2026-07-01", "2026-07-03", 2); err != nil {
		t.Fatalf("a booking with a different arrival was refused: %v", err)
	}
}

// TestBookingNightsCannotDisagreeWithTheDates is the CHECK that makes the night count
// trustworthy. The service writes it and the database compares it with the dates, so a
// booking whose column says three and whose stay is two cannot be committed at all.
func TestBookingNightsCannotDisagreeWithTheDates(t *testing.T) {
	h := dbtest.New(t)
	s := seedBooking(t, h, "BK_NIGHTS")

	if _, err := s.insertBooking(t, h, "BK-20260601-AAAAAAAA", "HOLD",
		"2026-06-15", "2026-06-18", 3); err != nil {
		t.Fatalf("a three-night stay counted as three was refused: %v", err)
	}
	_, err := s.insertBooking(t, h, "BK-20260601-BBBBBBBB", "HOLD",
		"2026-07-15", "2026-07-18", 2)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation,
		"a three-night stay whose night count says two")

	// A stay of no nights is not a stay.
	_, err = s.insertBooking(t, h, "BK-20260601-CCCCCCCC", "HOLD",
		"2026-08-15", "2026-08-15", 0)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation,
		"a check-out on the day of check-in")
}

// TestBookingNightRowsCarryAnExactSplit is the invariant that reaches an invoice: the
// payer's share plus the member's share is exactly what the night cost. It is a CHECK
// because these three numbers are copied from a quote and then summed by other things, and
// an invoice that is a kuruş out is an invoice nobody can reconcile and nobody can explain.
func TestBookingNightRowsCarryAnExactSplit(t *testing.T) {
	h := dbtest.New(t)
	s := seedBooking(t, h, "BK_SPLIT")
	booking, err := s.insertBooking(t, h, "BK-20260601-AAAAAAAA", "HOLD",
		"2026-06-15", "2026-06-18", 3)
	if err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	insertNight := func(day, unit, payer, member string) error {
		return h.AdminExecErr(`
			INSERT INTO accommodation.booking_night (tenant_id, booking_id, stay_date,
			                                         room_type_id, unit_amount, payer_amount,
			                                         member_amount, currency_code)
			VALUES ($1, $2, $3::date, $4, $5::numeric, $6::numeric, $7::numeric, 'TRY')`,
			s.tenant, booking, day, s.roomType, unit, payer, member)
	}
	if err := insertNight("2026-06-15", "1000.000000", "900.000000", "100.000000"); err != nil {
		t.Fatalf("an exact split was refused: %v", err)
	}
	dbtest.ExpectSQLState(t, insertNight("2026-06-16", "1000.000000", "900.000000", "100.000001"),
		dbtest.SQLStateCheckViolation, "a split that is out by a millionth")

	// One row per night, whatever writes it twice.
	dbtest.ExpectSQLState(t, insertNight("2026-06-15", "1000.000000", "900.000000", "100.000000"),
		dbtest.SQLStateUniqueViolation, "a second row for one night of one booking")
}

// TestBookingGuestKeepsPeopleAndStrangersApart is the shape of the guest list: somebody the
// tenant knows is a person, somebody it does not has only a name, and neither can be filed
// as the other. The column set is the other half of the rule and is asserted by its absence:
// there is no identifier column here, so there is nowhere for a passport number to be typed.
func TestBookingGuestKeepsPeopleAndStrangersApart(t *testing.T) {
	h := dbtest.New(t)
	s := seedBooking(t, h, "BK_GUEST")
	booking, err := s.insertBooking(t, h, "BK-20260601-AAAAAAAA", "HOLD",
		"2026-06-15", "2026-06-18", 3)
	if err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	insertGuest := func(person *uuid.UUID, name, guestType string) error {
		return h.AdminExecErr(`
			INSERT INTO accommodation.booking_guest (tenant_id, booking_id, person_id,
			                                         display_name, guest_type)
			VALUES ($1, $2, $3, $4, $5)`, s.tenant, booking, person, name, guestType)
	}
	if err := insertGuest(&s.person, "Deniz Kara", "MEMBER"); err != nil {
		t.Fatalf("a member guest was refused: %v", err)
	}
	if err := insertGuest(nil, "Bir Misafir", "GUEST"); err != nil {
		t.Fatalf("a guest with no person row was refused: %v", err)
	}
	dbtest.ExpectSQLState(t, insertGuest(nil, "Kim?", "MEMBER"),
		dbtest.SQLStateCheckViolation, "a MEMBER guest with no person")
	dbtest.ExpectSQLState(t, insertGuest(&s.person, "Deniz Kara", "GUEST"),
		dbtest.SQLStateCheckViolation, "a GUEST filed under a person")
	dbtest.ExpectSQLState(t, insertGuest(&s.person, "Deniz Kara", "DEPENDANT"),
		dbtest.SQLStateUniqueViolation, "the same person twice in one booking")

	// The absence that matters: nothing on this table could hold an identifier.
	ctx, cancel := h.Ctx()
	defer cancel()
	var suspicious int
	if err := h.Admin.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		 WHERE table_schema = 'accommodation' AND table_name = 'booking_guest'
		   AND column_name ~ '(identifier|national|passport|phone|email|birth)'`).
		Scan(&suspicious); err != nil {
		t.Fatalf("inspect columns: %v", err)
	}
	if suspicious != 0 {
		t.Errorf("booking_guest has %d column(s) an identifier could be typed into; they "+
			"belong in party.person behind the field cipher", suspicious)
	}
}

// TestBookingStatusAndItsTimestampsAgree is the pair of CHECKs the outbox subscriber leans
// on: a confirmed booking has the moment it was confirmed and a cancelled one has the moment
// it was cancelled. A subscriber that half-wrote its answer -- the status moved, the
// timestamp did not -- is exactly what a redelivery would otherwise leave behind.
func TestBookingStatusAndItsTimestampsAgree(t *testing.T) {
	h := dbtest.New(t)
	s := seedBooking(t, h, "BK_TIMES")
	booking, err := s.insertBooking(t, h, "BK-20260601-AAAAAAAA", "HOLD",
		"2026-06-15", "2026-06-18", 3)
	if err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	dbtest.ExpectSQLState(t, h.AdminExecErr(`
		UPDATE accommodation.booking SET status = 'CONFIRMED', hold_expires_at = NULL
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, booking),
		dbtest.SQLStateCheckViolation, "a CONFIRMED booking with no confirmed_at")
	dbtest.ExpectSQLState(t, h.AdminExecErr(`
		UPDATE accommodation.booking SET status = 'CANCELLED', hold_expires_at = NULL
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, booking),
		dbtest.SQLStateCheckViolation, "a CANCELLED booking with no cancelled_at")
	// And a hold with no deadline is a room set aside forever that no sweep would find.
	dbtest.ExpectSQLState(t, h.AdminExecErr(`
		UPDATE accommodation.booking SET hold_expires_at = NULL
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, booking),
		dbtest.SQLStateCheckViolation, "a HOLD with no deadline")
}
