package accommodationhttp_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/accommodation/application"
	accommodationpg "github.com/celikbros/kapsora/internal/accommodation/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/sqlcgen"
)

// addCompetingPayerPrice puts a second payer's published price on the SAME provider and
// service. Property visibility alone cannot exclude it: both contracts open this hotel.
func (s *server) addCompetingPayerPrice(t *testing.T, priority int) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	payer := s.h.CreateTenantOrganization(s.tenant, "Competing payer", "PAYER")
	var contractID, versionID, listID uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `
		INSERT INTO contract.contract (tenant_id, code, name, payer_organization_id,
		                               provider_profile_id, domain_code, status)
		VALUES ($1, 'COMPETING_2026', 'Competing payer', $2, $3, 'ACCOMMODATION', 'ACTIVE')
		RETURNING id`, s.tenant, payer, s.provider).Scan(&contractID); err != nil {
		t.Fatalf("create competing contract: %v", err)
	}
	if err := s.h.Admin.QueryRow(ctx, `
		INSERT INTO contract.contract_version (tenant_id, contract_id, version_no, status,
		                                      valid_from, valid_to, currency_code, configuration_hash)
		VALUES ($1, $2, 1, 'DRAFT', '2026-01-01', '2027-01-01', 'TRY', 'competing')
		RETURNING id`, s.tenant, contractID).Scan(&versionID); err != nil {
		t.Fatalf("create competing version: %v", err)
	}
	if err := s.h.Admin.QueryRow(ctx, `
		INSERT INTO contract.price_list (tenant_id, contract_version_id, code, name)
		VALUES ($1, $2, 'COMPETING', 'Competing list') RETURNING id`,
		s.tenant, versionID).Scan(&listID); err != nil {
		t.Fatalf("create competing list: %v", err)
	}
	if _, err := s.h.Admin.Exec(ctx, `
		INSERT INTO contract.price_item (tenant_id, price_list_id, service_definition_id,
		                                 unit_type, pricing_method, amount, member_share_method,
		                                 member_share_percent, valid_from, priority)
		VALUES ($1, $2, $3, 'NIGHT', 'FIXED', 4000, 'PERCENT', 10, '2026-01-01', $4)`,
		s.tenant, listID, s.definition, priority); err != nil {
		t.Fatalf("create competing price: %v", err)
	}
	if _, err := s.h.Admin.Exec(ctx, `
		INSERT INTO contract.lodging_terms (tenant_id, contract_version_id,
		                                    free_cancellation_hours_before, penalty_kind,
		                                    penalty_nights, no_show_percent, min_nights, hold_minutes)
		VALUES ($1, $2, 48, 'NIGHTS', 1, 100, 1, 5)`, s.tenant, versionID); err != nil {
		t.Fatalf("create competing terms: %v", err)
	}
	if _, err := s.h.Admin.Exec(ctx, `
		UPDATE contract.contract_version
		   SET status = 'PUBLISHED', published_at = clock_timestamp(), published_by = $3
		 WHERE tenant_id = $1 AND id = $2`, s.tenant, versionID, s.actor); err != nil {
		t.Fatalf("publish competing version: %v", err)
	}
	return payer, listID
}

func (s *server) payerBoundaryQuote(t *testing.T, program *uuid.UUID) *kapsorav1.AvailabilityQuote {
	t.Helper()
	body := s.searchBody()
	if program != nil {
		body["programId"] = program.String()
	}
	rec := s.do(t, http.MethodPost, "/api/v1/accommodation/availability/search",
		readerPermissions, body, s.memberHeaders()...)
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d: %s", rec.Code, rec.Body.String())
	}
	result := decode[kapsorav1.AvailabilitySearchResult](t, rec)
	for _, item := range result.Results {
		if item.RoomType.Id == s.roomType {
			return item.Quote
		}
	}
	t.Fatal("seeded room type absent")
	return nil
}

func TestPriceCandidatesRespectProgramPayerBeforePriority(t *testing.T) {
	s := newServer(t, true)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.grantNights(t, 10)
	s.putLodgingTermsWithHold(t, int32Ptr(30))
	s.addCompetingPayerPrice(t, 200)

	for _, program := range []*uuid.UUID{nil, &s.program} {
		quote := s.payerBoundaryQuote(t, program)
		if quote == nil || quote.TotalAmount != "3000" {
			t.Fatalf("program %v quote = %+v, want payer A's 3000 despite B's priority", program, quote)
		}
	}
	ctx, cancel := s.h.Ctx()
	defer cancel()
	body := map[string]any{
		"roomTypeId": s.roomType.String(), "checkIn": checkIn, "checkOut": checkOut,
		"adults": 2, "personId": s.person.String(),
	}
	headers := append(s.memberHeaders(), "Idempotency-Key", "payer-boundary-hold-0001")
	rec := s.do(t, http.MethodPost, "/api/v1/accommodation/holds", bookerPermissions,
		body, headers...)
	if rec.Code != http.StatusCreated {
		t.Fatalf("hold with competing payer price = %d: %s", rec.Code, rec.Body.String())
	}
	booked := decode[kapsorav1.Booking](t, rec)
	if booked.QuoteSnapshot.TotalAmount != "3000" {
		t.Errorf("frozen total = %s, want payer A's 3000", booked.QuoteSnapshot.TotalAmount)
	}
	wantExpiry := s.clock.Now().Add(30 * time.Minute)
	if booked.HoldExpiresAt == nil || !booked.HoldExpiresAt.Equal(wantExpiry) ||
		booked.SecondsToExpiry != 1800 {
		t.Errorf("hold expiry/countdown = %v/%d, want payer A's %v/1800",
			booked.HoldExpiresAt, booked.SecondsToExpiry, wantExpiry)
	}
	var bookingExpiry, reservationExpiry time.Time
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT b.hold_expires_at, r.expires_at
		  FROM accommodation.booking b
		  JOIN benefit.entitlement_reservation r
		    ON r.tenant_id = b.tenant_id AND r.id = b.entitlement_reservation_id
		 WHERE b.tenant_id = $1 AND b.id = $2`, s.tenant, booked.Id).
		Scan(&bookingExpiry, &reservationExpiry); err != nil {
		t.Fatalf("read persisted expiries: %v", err)
	}
	if !bookingExpiry.Equal(wantExpiry) || !reservationExpiry.Equal(wantExpiry) {
		t.Errorf("persisted booking/reservation expiry = %v/%v, want %v",
			bookingExpiry, reservationExpiry, wantExpiry)
	}
	var reservations int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM benefit.entitlement_reservation
		WHERE tenant_id = $1 AND reference_id = $2`, s.tenant, booked.Id).
		Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if reservations != 1 {
		t.Errorf("successful hold wrote %d reservations, want one", reservations)
	}
	var movements int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM benefit.entitlement_ledger
		WHERE tenant_id = $1 AND reference_id = $2 AND movement_type = 'RESERVE'`,
		s.tenant, booked.Id).Scan(&movements); err != nil {
		t.Fatal(err)
	}
	if movements != 1 {
		t.Errorf("successful hold wrote %d reserve movements, want one", movements)
	}
}

func TestWrongPayerEqualRankDoesNotCauseAmbiguity(t *testing.T) {
	s := newServer(t)
	s.addCompetingPayerPrice(t, 100)
	quote := s.payerBoundaryQuote(t, &s.program)
	if quote == nil || quote.TotalAmount != "3000" {
		t.Fatalf("wrong-payer tie changed A quote: %+v", quote)
	}
}

func TestWrongPayerOnlyPriceCannotCreateHold(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	s.addCompetingPayerPrice(t, 200)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if _, err := s.h.Admin.Exec(ctx, `UPDATE contract.contract_version SET status = 'DRAFT'
		WHERE tenant_id = $1 AND id = $2`, s.tenant, s.contractVersion); err != nil {
		t.Fatalf("return A version to draft: %v", err)
	}
	if _, err := s.h.Admin.Exec(ctx, `DELETE FROM contract.price_item
		WHERE tenant_id = $1 AND price_list_id IN
		(SELECT l.id FROM contract.price_list l WHERE l.tenant_id = $1
		 AND l.contract_version_id = $2)`, s.tenant, s.contractVersion); err != nil {
		t.Fatalf("remove A price: %v", err)
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE contract.contract_version SET status = 'PUBLISHED'
		WHERE tenant_id = $1 AND id = $2`, s.tenant, s.contractVersion); err != nil {
		t.Fatalf("republish A version: %v", err)
	}
	if quote := s.payerBoundaryQuote(t, nil); quote != nil {
		t.Errorf("wrong-payer only quote = %+v, want no quote", quote)
	}
	if _, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType)); err == nil {
		t.Fatal("wrong-payer only price created a hold")
	}
	var bookings int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM accommodation.booking
		WHERE tenant_id = $1`, s.tenant).Scan(&bookings); err != nil {
		t.Fatalf("count bookings: %v", err)
	}
	if bookings != 0 {
		t.Errorf("wrong-payer only price wrote %d bookings", bookings)
	}
	_, held, _ := s.inventoryOf(t, s.roomType, checkIn)
	if held != 1 {
		t.Errorf("wrong-payer refusal changed held inventory to %d", held)
	}
	var reservations int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM benefit.entitlement_reservation
		WHERE tenant_id = $1`, s.tenant).Scan(&reservations); err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if reservations != 0 {
		t.Errorf("wrong-payer refusal wrote %d reservations", reservations)
	}
}

func TestSamePayerEqualRankRemainsAmbiguous(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if _, err := s.h.Admin.Exec(ctx, `UPDATE contract.contract_version SET status = 'DRAFT'
		WHERE tenant_id = $1 AND id = $2`, s.tenant, s.contractVersion); err != nil {
		t.Fatalf("return A version to draft: %v", err)
	}
	if _, err := s.h.Admin.Exec(ctx, `
		INSERT INTO contract.price_item (tenant_id, price_list_id, service_definition_id,
		                                 unit_type, pricing_method, amount, member_share_method,
		                                 member_share_percent, valid_from, priority)
		SELECT tenant_id, price_list_id, service_definition_id, unit_type, pricing_method,
		       900, member_share_method, member_share_percent, valid_from, priority
		  FROM contract.price_item
		 WHERE tenant_id = $1 AND price_list_id IN
		       (SELECT id FROM contract.price_list WHERE tenant_id = $1
		        AND contract_version_id = $2)`, s.tenant, s.contractVersion); err != nil {
		t.Fatalf("add same-payer tie: %v", err)
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE contract.contract_version SET status = 'PUBLISHED'
		WHERE tenant_id = $1 AND id = $2`, s.tenant, s.contractVersion); err != nil {
		t.Fatalf("republish A version: %v", err)
	}
	body := s.searchBody()
	rec := s.do(t, http.MethodPost, "/api/v1/accommodation/availability/search",
		readerPermissions, body, s.memberHeaders()...)
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d: %s", rec.Code, rec.Body.String())
	}
	result := decode[kapsorav1.AvailabilitySearchResult](t, rec)
	found := false
	for _, item := range result.Results {
		if item.RoomType.Id != s.roomType {
			continue
		}
		found = true
		if item.Quote != nil || item.QuoteUnavailableReason == nil ||
			string(*item.QuoteUnavailableReason) != application.ReasonPriceAmbiguous {
			t.Errorf("same-payer tie result = %+v, want PRICE_AMBIGUOUS", item)
		}
	}
	if !found {
		t.Fatal("seeded room type absent")
	}
	_, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
	var unavailable *application.QuoteUnavailable
	if !asError(err, &unavailable) || unavailable.Reason != application.ReasonPriceAmbiguous {
		t.Fatalf("same-payer tie hold = %v, want PRICE_AMBIGUOUS", err)
	}
	var bookings, reservations int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM accommodation.booking
		WHERE tenant_id = $1`, s.tenant).Scan(&bookings); err != nil {
		t.Fatal(err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM benefit.entitlement_reservation
		WHERE tenant_id = $1`, s.tenant).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if bookings != 0 || reservations != 0 {
		t.Errorf("ambiguous hold wrote %d bookings and %d reservations", bookings, reservations)
	}
	_, held, _ := s.inventoryOf(t, s.roomType, checkIn)
	if held != 1 {
		t.Errorf("ambiguous hold changed held inventory to %d", held)
	}
}

func TestPriceCandidateSQLPayerArrays(t *testing.T) {
	s := newServer(t)
	payerB, _ := s.addCompetingPayerPrice(t, 200)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	base := application.PriceCandidateQuery{
		ProviderProfileIDs:   []uuid.UUID{s.provider},
		ServiceDefinitionIDs: []uuid.UUID{s.definition},
		CheckIn:              mustDay(checkIn), LastNight: mustDay(lastNight),
	}
	for _, tc := range []struct {
		name   string
		payers []uuid.UUID
		want   int
	}{
		{"nil", nil, 0},
		{"empty", []uuid.UUID{}, 0},
		{"A only", []uuid.UUID{s.payer}, 1},
		{"B only", []uuid.UUID{payerB}, 1},
		{"A and B", []uuid.UUID{s.payer, payerB}, 2},
		{"unrelated", []uuid.UUID{uuid.New()}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := base
			args.PayerOrganizationIDs = tc.payers
			var rows []application.PriceCandidate
			err := db.WithTenantTx(ctx, s.h.App,
				db.TenantContext{TenantID: s.tenant, ActorID: s.actor},
				func(ctx context.Context, tx pgx.Tx) error {
					var queryErr error
					rows, queryErr = accommodationpg.New().ListPriceCandidates(ctx, tx, s.tenant, args)
					return queryErr
				})
			if err != nil {
				t.Fatalf("tenant-bound repository query: %v", err)
			}
			if len(rows) != tc.want {
				t.Errorf("rows = %d, want %d", len(rows), tc.want)
			}
		})
	}
}

func TestProgramPayerLookupNarrowsExplicitProgram(t *testing.T) {
	s := newServer(t)
	payerB, _ := s.addCompetingPayerPrice(t, 200)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var programB, planB uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `
		INSERT INTO benefit.program (tenant_id, sponsor_tenant_organization_id,
		 payer_tenant_organization_id, code, name, program_type, status, valid_period)
		SELECT tenant_id, sponsor_tenant_organization_id, $3, 'SECOND', 'Second program',
		       program_type, status, valid_period
		  FROM benefit.program WHERE tenant_id = $1 AND id = $2 RETURNING id`,
		s.tenant, s.program, payerB).Scan(&programB); err != nil {
		t.Fatalf("create second program: %v", err)
	}
	if err := s.h.Admin.QueryRow(ctx, `
		INSERT INTO benefit.plan (tenant_id, program_id, code, name, status)
		VALUES ($1, $2, 'SECOND', 'Second plan', 'ACTIVE') RETURNING id`,
		s.tenant, programB).Scan(&planB); err != nil {
		t.Fatalf("create second plan: %v", err)
	}
	if _, err := s.h.Admin.Exec(ctx, `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id,
		                                status, valid_period)
		SELECT tenant_id, sponsor_membership_id, $3, 'ACTIVE', valid_period
		  FROM benefit.enrollment WHERE tenant_id = $1 AND id = $2`,
		s.tenant, s.enrollment, planB); err != nil {
		t.Fatalf("enroll second program: %v", err)
	}
	query := sqlcgen.ListPersonProgramPayersParams{
		TenantID: s.tenant, PersonID: s.person,
		ServiceDate: pgtype.Date{Time: mustDay(checkIn), Valid: true},
	}
	for _, tc := range []struct {
		name    string
		program uuid.NullUUID
		want    map[uuid.UUID]bool
	}{
		{"omitted program union", uuid.NullUUID{}, map[uuid.UUID]bool{s.payer: true, payerB: true}},
		{"explicit A", uuid.NullUUID{UUID: s.program, Valid: true}, map[uuid.UUID]bool{s.payer: true}},
		{"explicit B", uuid.NullUUID{UUID: programB, Valid: true}, map[uuid.UUID]bool{payerB: true}},
		{"unenrolled program", uuid.NullUUID{UUID: uuid.New(), Valid: true}, map[uuid.UUID]bool{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := query
			args.ProgramID = tc.program
			payers, err := sqlcgen.New(s.h.Admin).ListPersonProgramPayers(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			if len(payers) != len(tc.want) {
				t.Fatalf("payers = %v, want %v", payers, tc.want)
			}
			for _, payer := range payers {
				if !tc.want[payer] {
					t.Errorf("unexpected payer %s", payer)
				}
			}
		})
	}
	// Omitted program preserves the current union of enrolled payers, so B's higher
	// ranked price may win. An explicit A search narrows that same world back to A.
	if quote := s.payerBoundaryQuote(t, nil); quote == nil || quote.TotalAmount != "12000" {
		t.Errorf("omitted-program union quote = %+v, want B's 12000", quote)
	}
	if quote := s.payerBoundaryQuote(t, &s.program); quote == nil || quote.TotalAmount != "3000" {
		t.Errorf("explicit A quote = %+v, want A's 3000", quote)
	}
	body := s.searchBody()
	body["programId"] = uuid.NewString()
	rec := s.do(t, http.MethodPost, "/api/v1/accommodation/availability/search",
		readerPermissions, body, s.memberHeaders()...)
	if rec.Code != http.StatusOK {
		t.Fatalf("unenrolled program search = %d: %s", rec.Code, rec.Body.String())
	}
	result := decode[kapsorav1.AvailabilitySearchResult](t, rec)
	if len(result.Results) != 0 {
		t.Errorf("unenrolled program returned %d rooms", len(result.Results))
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE benefit.enrollment SET status = 'SUSPENDED'
		WHERE tenant_id = $1 AND plan_id = $2`, s.tenant, planB); err != nil {
		t.Fatalf("deactivate B enrollment: %v", err)
	}
	args := query
	args.ProgramID = uuid.NullUUID{UUID: programB, Valid: true}
	payers, err := sqlcgen.New(s.h.Admin).ListPersonProgramPayers(ctx, args)
	if err != nil || len(payers) != 0 {
		t.Errorf("inactive B enrollment payers = %v, err = %v", payers, err)
	}
	if _, err := s.h.Admin.Exec(ctx, `UPDATE benefit.enrollment
		SET status = 'ACTIVE', valid_period = daterange('2026-07-01', NULL, '[)')
		WHERE tenant_id = $1 AND plan_id = $2`, s.tenant, planB); err != nil {
		t.Fatalf("move B enrollment after check-in: %v", err)
	}
	payers, err = sqlcgen.New(s.h.Admin).ListPersonProgramPayers(ctx, args)
	if err != nil || len(payers) != 0 {
		t.Errorf("out-of-period B enrollment payers = %v, err = %v", payers, err)
	}
}
