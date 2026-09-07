package dbtests

import (
	"testing"

	"github.com/google/uuid"

	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
)

// The half of WP-I6-03 that lives in the database rather than in Go.
//
// Each of these is a rule the service also states, and that duplication is deliberate in
// exactly one direction: the service's version is a *message* a person can read, and this
// one is a *guarantee* that holds when the service is bypassed, refactored, or wrong. A rule
// that only the service knows is a rule the next command to touch the table can forget.

// seedConfirmedBooking is one CONFIRMED booking on top of the booking fixture: everything the
// tables below reference, in the state a cancellation, a no-show or a waitlist offer acts on.
func seedConfirmedBooking(t *testing.T, h *dbtest.Harness, code string) (bookingSeed, uuid.UUID) {
	t.Helper()
	s := seedBooking(t, h, code)
	booking, err := s.insertBooking(t, h, "BK-20260601-AAAAAAAA", "CONFIRMED",
		"2026-06-15", "2026-06-18", 3)
	if err != nil {
		t.Fatalf("seed a confirmed booking: %v", err)
	}
	return s, booking
}

// TestWaitlistManagePermissionIsSeededAndGrantable is the two-halves test WP-I6-03 section
// 2.6 asks for: the catalogue row of migration 000042 and the role templates in
// internal/identity/application/roles.go have to agree. A permission in one and not the
// other is a permission nobody can hold, or one nobody can be given.
func TestWaitlistManagePermissionIsSeededAndGrantable(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()

	const code = "accommodation.waitlist.manage"

	var seeded int
	if err := h.Admin.QueryRow(ctx,
		`SELECT count(*) FROM iam.permission WHERE code = $1`, code).Scan(&seeded); err != nil {
		t.Fatalf("read permission %s: %v", code, err)
	}
	if seeded != 1 {
		t.Fatalf("permission %s is seeded %d times, want once", code, seeded)
	}
	var sensitivity string
	if err := h.Admin.QueryRow(ctx,
		`SELECT sensitivity FROM iam.permission WHERE code = $1`, code).Scan(&sensitivity); err != nil {
		t.Fatalf("read sensitivity of %s: %v", code, err)
	}
	// NORMAL: a waitlist entry is a member asking to be told when a room frees up. Marking
	// it SENSITIVE would pull every reservation desk into the access reviews that exist to
	// list the grants over personal data, and dilute the list that matters.
	if sensitivity != "NORMAL" {
		t.Errorf("permission %s sensitivity = %s, want NORMAL", code, sensitivity)
	}

	byRole := map[string]map[string]bool{}
	for _, tpl := range identityapp.RoleTemplates() {
		byRole[tpl.Code] = map[string]bool{}
		for _, p := range tpl.Permissions {
			byRole[tpl.Code][p] = true
		}
	}

	// The two desks the work package names: the one at the property and the one at the
	// payer. Each runs a queue for somebody else, which is what this grant is.
	for _, role := range []string{"PROVIDER_RESERVATION", "PROGRAM_MANAGER"} {
		if !byRole[role][code] {
			t.Errorf("role %s does not hold %s", role, code)
		}
	}

	// A member needs none of it. They join a queue for themselves under
	// accommodation.booking.create, which is the grant they already hold to book for
	// themselves; handing them the desk's grant would let them set their own priority.
	if byRole["MEMBER"][code] {
		t.Error("MEMBER holds accommodation.waitlist.manage; a member who can run the queue " +
			"can push themselves up it")
	}
	// And the pairing that must hold whatever anybody renames: nobody may run a waiting list
	// for a hotel they cannot read.
	for _, tpl := range identityapp.RoleTemplates() {
		if byRole[tpl.Code][code] && !byRole[tpl.Code]["accommodation.property.read"] {
			t.Errorf("role %s runs a waiting list and cannot read a property", tpl.Code)
		}
	}
}

// TestCancellationIsAppendOnly is the whole claim of accommodation.cancellation stated as a
// property of the table: a cancellation is the record of what a member was told they would be
// charged, and a row somebody could edit afterwards is not a record of anything.
func TestCancellationIsAppendOnly(t *testing.T) {
	h := dbtest.New(t)
	s, booking := seedConfirmedBooking(t, h, "CANCEL_APPEND")

	h.AdminExec(`
		INSERT INTO accommodation.cancellation (tenant_id, booking_id, cancelled_at, reason_code,
		                                        policy_snapshot, free, penalty_nights,
		                                        released_nights, fee_amount, payer_fee,
		                                        member_fee, currency_code)
		VALUES ($1, $2, clock_timestamp(), 'MEMBER_CANCELLED', '{"penaltyKind":"NIGHTS"}'::jsonb,
		        false, 1, 2, 1000, 900, 100, 'TRY')`, s.tenant, booking)

	err := h.AdminExecErr(`
		UPDATE accommodation.cancellation SET fee_amount = 0 WHERE tenant_id = $1`, s.tenant)
	if err == nil {
		t.Error("a cancellation fee was edited after the fact")
	}
	err = h.AdminExecErr(`DELETE FROM accommodation.cancellation WHERE tenant_id = $1`, s.tenant)
	if err == nil {
		t.Error("a cancellation was deleted; nothing in this table is ever removed")
	}
}

// TestCancellationRefusesAFeeThatDoesNotAddUp is the money invariant of every row in this
// system, on the one table WP-I6-03 adds that carries an amount a member is charged.
func TestCancellationRefusesAFeeThatDoesNotAddUp(t *testing.T) {
	h := dbtest.New(t)
	s, booking := seedConfirmedBooking(t, h, "CANCEL_SUM")

	err := h.AdminExecErr(`
		INSERT INTO accommodation.cancellation (tenant_id, booking_id, cancelled_at, reason_code,
		                                        policy_snapshot, free, penalty_nights,
		                                        released_nights, fee_amount, payer_fee,
		                                        member_fee, currency_code)
		VALUES ($1, $2, clock_timestamp(), 'MEMBER_CANCELLED', '{"penaltyKind":"NIGHTS"}'::jsonb,
		        false, 1, 2, 1000, 900, 99, 'TRY')`, s.tenant, booking)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a fee whose halves do not sum")

	// And a free cancellation that charges. Both halves of the rule are asserted, because
	// either one alone would let a row say "free" and take money anyway.
	err = h.AdminExecErr(`
		INSERT INTO accommodation.cancellation (tenant_id, booking_id, cancelled_at, reason_code,
		                                        policy_snapshot, free, penalty_nights,
		                                        released_nights, fee_amount, payer_fee,
		                                        member_fee, currency_code)
		VALUES ($1, $2, clock_timestamp(), 'MEMBER_CANCELLED', '{"penaltyKind":"NIGHTS"}'::jsonb,
		        true, 0, 3, 500, 500, 0, 'TRY')`, s.tenant, booking)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a free cancellation with a fee")
}

// TestNoShowRefusesItsOwnReporterAsReviewer is the maker-checker rule, in the database.
//
// The service refuses it with a sentence a person can read; this is the half that still holds
// when a future command writes the row directly. A provider clerk may say that nobody came
// and may never be the person who decides it costs the member anything.
func TestNoShowRefusesItsOwnReporterAsReviewer(t *testing.T) {
	h := dbtest.New(t)
	s, booking := seedConfirmedBooking(t, h, "NOSHOW_CK")
	reporter := h.CreateActor("no-show-reporter", "Otel Resepsiyon")

	h.AdminExec(`
		INSERT INTO accommodation.no_show (tenant_id, booking_id, reported_by_actor_id,
		                                   assessed_fee_amount, payer_amount, member_amount,
		                                   currency_code)
		VALUES ($1, $2, $3, 300, 0, 300, 'TRY')`, s.tenant, booking, reporter)

	err := h.AdminExecErr(`
		UPDATE accommodation.no_show
		   SET status = 'CONFIRMED', reviewed_by = $2, reviewed_at = clock_timestamp()
		 WHERE tenant_id = $1`, s.tenant, reporter)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "the reporter reviewing itself")

	// A different person may.
	reviewer := h.CreateActor("no-show-reviewer", "Ödeyici Değerlendirici")
	h.AdminExec(`
		UPDATE accommodation.no_show
		   SET status = 'CONFIRMED', reviewed_by = $2, reviewed_at = clock_timestamp()
		 WHERE tenant_id = $1`, s.tenant, reviewer)

	// And a decided report has both a decider and a moment, or neither.
	err = h.AdminExecErr(`
		UPDATE accommodation.no_show SET reviewed_at = NULL WHERE tenant_id = $1`, s.tenant)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a decision with no moment")
}

// TestOneLiveWaitlistEntryPerPersonAndArrival is uq_waitlist_live_entry: the partial unique
// index that makes "one live place in the queue" a guarantee rather than a service check two
// concurrent joins would both pass.
//
// The predicate is the other half of the same rule: a cancelled or expired entry is history,
// and history must not stop a member joining a queue again.
func TestOneLiveWaitlistEntryPerPersonAndArrival(t *testing.T) {
	h := dbtest.New(t)
	s, _ := seedConfirmedBooking(t, h, "WAITLIST_UQ")

	insert := func() error {
		return h.AdminExecErr(`
			INSERT INTO accommodation.waitlist_entry (tenant_id, person_id, enrollment_id,
			                                          property_id, check_in, check_out, adults)
			VALUES ($1, $2, $3, $4, '2026-06-15', '2026-06-18', 2)`,
			s.tenant, s.person, s.enrollment, s.property)
	}
	if err := insert(); err != nil {
		t.Fatalf("first join: %v", err)
	}
	dbtest.ExpectSQLState(t, insert(), dbtest.SQLStateUniqueViolation, "a second live entry")

	h.AdminExec(`UPDATE accommodation.waitlist_entry SET status = 'CANCELLED' WHERE tenant_id = $1`,
		s.tenant)
	if err := insert(); err != nil {
		t.Errorf("rejoining after cancelling was refused by the index: %v", err)
	}
}

// TestWaitlistOfferNeedsABookingAndADeadline is ck_waitlist_offer: an entry that says OFFERED
// with no booking would be a member told to come and collect nothing, and one with no
// deadline would be a room set aside for ever.
func TestWaitlistOfferNeedsABookingAndADeadline(t *testing.T) {
	h := dbtest.New(t)
	s, booking := seedConfirmedBooking(t, h, "WAITLIST_OFFER")

	h.AdminExec(`
		INSERT INTO accommodation.waitlist_entry (tenant_id, person_id, enrollment_id,
		                                          property_id, check_in, check_out, adults)
		VALUES ($1, $2, $3, $4, '2026-06-15', '2026-06-18', 2)`,
		s.tenant, s.person, s.enrollment, s.property)

	err := h.AdminExecErr(`
		UPDATE accommodation.waitlist_entry SET status = 'OFFERED' WHERE tenant_id = $1`, s.tenant)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "an offer of nothing")

	err = h.AdminExecErr(`
		UPDATE accommodation.waitlist_entry
		   SET status = 'OFFERED', offered_booking_id = $2 WHERE tenant_id = $1`,
		s.tenant, booking)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "an offer with no deadline")

	h.AdminExec(`
		UPDATE accommodation.waitlist_entry
		   SET status = 'OFFERED', offered_booking_id = $2,
		       offer_expires_at = clock_timestamp() + interval '15 minutes'
		 WHERE tenant_id = $1`, s.tenant, booking)
}

// TestACancelledBookingKeepsTheMomentItWasConfirmed is the constraint migration 000042
// relaxes, and the reason it had to be.
//
// A stay that was agreed and then called off is CANCELLED *and* was confirmed on a particular
// day, and that day is what the cancellation fee was judged against. Clearing it to satisfy
// the old equivalence would erase the fact the whole cancellation hangs off. What is still
// refused is the other direction: a booking that never reached a confirmation may not claim
// a moment it was confirmed at.
func TestACancelledBookingKeepsTheMomentItWasConfirmed(t *testing.T) {
	h := dbtest.New(t)
	s, booking := seedConfirmedBooking(t, h, "BOOKING_CONFIRMED_AT")

	h.AdminExec(`
		UPDATE accommodation.booking
		   SET status = 'CANCELLED', cancelled_at = clock_timestamp(),
		       cancel_reason_code = 'MEMBER_CANCELLED'
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, booking)

	ctx, cancel := h.Ctx()
	defer cancel()
	var confirmedAt *string
	if err := h.Admin.QueryRow(ctx, `
		SELECT confirmed_at::text FROM accommodation.booking WHERE tenant_id = $1 AND id = $2`,
		s.tenant, booking).Scan(&confirmedAt); err != nil {
		t.Fatalf("read the cancelled booking: %v", err)
	}
	if confirmedAt == nil {
		t.Error("a cancelled booking lost the moment it was confirmed; the fee it was " +
			"charged was judged against a policy frozen at exactly that moment")
	}

	// A hold may not claim one.
	err := h.AdminExecErr(`
		UPDATE accommodation.booking
		   SET status = 'HOLD', cancelled_at = NULL, cancel_reason_code = NULL,
		       hold_expires_at = clock_timestamp() + interval '15 minutes'
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, booking)
	dbtest.ExpectSQLState(t, err, dbtest.SQLStateCheckViolation, "a hold that was confirmed")
}
