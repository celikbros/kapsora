package accommodationhttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/celikbros/kapsora/internal/accommodation/application"
	accommodationdomain "github.com/celikbros/kapsora/internal/accommodation/domain"
	accommodationgw "github.com/celikbros/kapsora/internal/accommodation/infrastructure/gateway"
	accommodationpg "github.com/celikbros/kapsora/internal/accommodation/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/platform/db"
	"github.com/celikbros/kapsora/internal/platform/outbox"
	servicerequestapp "github.com/celikbros/kapsora/internal/servicerequest/application"
	servicerequestdomain "github.com/celikbros/kapsora/internal/servicerequest/domain"
)

// seedSameProviderDistractor creates a newer published contract under the same provider
// and payer. Its price loses the item-priority ladder, but the removed provider-wide
// selector would choose this version by valid_from and freeze its different terms.
func (s *server) seedSameProviderDistractor(t *testing.T) uuid.UUID {
	return s.seedSameProviderContract(t, "2026-06-01", 1)
}

func (s *server) seedSameProviderContract(t *testing.T, validFrom string, itemPriority int) uuid.UUID {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var contractID, versionID, listID uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `
		INSERT INTO contract.contract (tenant_id, code, name, payer_organization_id,
		                               provider_profile_id, domain_code, status)
		VALUES ($1, 'CONFIRM_DISTRACTOR', 'Confirmation distractor', $2, $3,
		        'ACCOMMODATION', 'ACTIVE') RETURNING id`, s.tenant, s.payer, s.provider).
		Scan(&contractID); err != nil {
		t.Fatalf("seed distractor contract: %v", err)
	}
	if err := s.h.Admin.QueryRow(ctx, `
		INSERT INTO contract.contract_version (tenant_id, contract_id, version_no, status,
		                                       valid_from, valid_to, currency_code, configuration_hash)
		VALUES ($1, $2, 1, 'DRAFT', $3::date, '2027-01-01', 'TRY', 'distractor')
		RETURNING id`, s.tenant, contractID, validFrom).Scan(&versionID); err != nil {
		t.Fatalf("seed distractor version: %v", err)
	}
	if err := s.h.Admin.QueryRow(ctx, `
		INSERT INTO contract.price_list (tenant_id, contract_version_id, code, name)
		VALUES ($1, $2, 'DISTRACTOR', 'Lower ranked list') RETURNING id`,
		s.tenant, versionID).Scan(&listID); err != nil {
		t.Fatalf("seed distractor price list: %v", err)
	}
	if _, err := s.h.Admin.Exec(ctx, `
		INSERT INTO contract.price_item (tenant_id, price_list_id, service_definition_id, unit_type,
		                                 pricing_method, amount, member_share_method,
		                                 member_share_percent, valid_from, priority)
		VALUES ($1, $2, $3, 'NIGHT', 'FIXED', 777, 'PERCENT', 10, $4::date, $5)`,
		s.tenant, listID, s.definition, validFrom, itemPriority); err != nil {
		t.Fatalf("seed losing price: %v", err)
	}
	if _, err := s.h.Admin.Exec(ctx, `
		INSERT INTO contract.lodging_terms (tenant_id, contract_version_id,
		                                    free_cancellation_hours_before, penalty_kind,
		                                    penalty_nights, no_show_percent, min_nights, hold_minutes)
		VALUES ($1, $2, 7, 'NIGHTS', 2, 50, 1, 90)`, s.tenant, versionID); err != nil {
		t.Fatalf("seed distractor terms: %v", err)
	}
	if _, err := s.h.Admin.Exec(ctx, `
		UPDATE contract.contract_version SET status='PUBLISHED',
		       published_at=clock_timestamp(), published_by=$3
		 WHERE tenant_id=$1 AND id=$2`, s.tenant, versionID, s.actor); err != nil {
		t.Fatalf("publish distractor: %v", err)
	}
	return versionID
}

func TestBookingContractBindingFirstNightPolicyWinsPreflightAndApproval(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.grantNights(t, 10)
	s.putLodgingTermsWithHold(t, int32Ptr(30))
	ctx, cancel := s.h.Ctx()
	defer cancel()
	hold, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	// Publishing a newer provider contract after the hold must not rewrite its policy.
	distractor := s.seedSameProviderDistractor(t)

	// This is the exact answer the deleted broad lookup would have supplied.
	var broadWinner uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT cv.id FROM contract.contract_version cv
		JOIN contract.contract c ON c.tenant_id=cv.tenant_id AND c.id=cv.contract_id
		WHERE cv.tenant_id=$1 AND c.provider_profile_id=$2 AND c.status='ACTIVE'
		  AND cv.status='PUBLISHED' AND cv.valid_from <= $3::date
		  AND (cv.valid_to IS NULL OR cv.valid_to > $3::date)
		ORDER BY (c.domain_code='ACCOMMODATION') DESC, cv.valid_from DESC, cv.id
		LIMIT 1`, s.tenant, s.provider, checkIn).Scan(&broadWinner); err != nil {
		t.Fatalf("check negative-control selector: %v", err)
	}
	if broadWinner != distractor || broadWinner == s.contractVersion {
		t.Fatalf("broad selector = %s; fixture must pick distractor %s instead of A %s",
			broadWinner, distractor, s.contractVersion)
	}

	quote, err := application.DecodeQuoteSnapshot(hold.Booking.QuoteSnapshot)
	if err != nil || quote.Version != 2 || quote.FirstNightContractVersionID == nil ||
		*quote.FirstNightContractVersionID != s.contractVersion || quote.TotalAmount != "3000" {
		t.Fatalf("frozen quote = %+v, %v; want v2, A and unchanged 3000", quote, err)
	}
	public := s.do(t, http.MethodGet, "/api/v1/accommodation/bookings/"+hold.Booking.ID.String(),
		bookerPermissions, nil, s.memberHeaders()...)
	if public.Code != http.StatusOK || !bytes.Contains(public.Body.Bytes(), []byte(`"version":1`)) ||
		bytes.Contains(public.Body.Bytes(), []byte("firstNightContractVersionId")) {
		t.Fatalf("public booking = %d %s; internal contract identity must stay private",
			public.Code, public.Body.String())
	}

	confirmed, err := s.svc.ConfirmBooking(ctx, s.memberContext(), hold.Booking.ID)
	if err != nil || confirmed.Booking.ServiceRequestID == nil {
		t.Fatalf("confirm under selected A: %v; request=%v", err, confirmed.Booking.ServiceRequestID)
	}
	s.decideRequest(t, *confirmed.Booking.ServiceRequestID, servicerequestdomain.StatusApproved)
	s.deliverDecision(t, *confirmed.Booking.ServiceRequestID)
	final, err := s.svc.GetBooking(ctx, s.deskContext(), hold.Booking.ID)
	if err != nil || final.Booking.PolicySnapshot == nil {
		t.Fatalf("read confirmed policy: %v; snapshot=%v", err, final.Booking.PolicySnapshot)
	}
	var policy struct {
		ContractVersionID           uuid.UUID `json:"contractVersionId"`
		FreeCancellationHoursBefore int       `json:"freeCancellationHoursBefore"`
		NoShowPercent               string    `json:"noShowPercent"`
	}
	if err := json.Unmarshal(final.Booking.PolicySnapshot, &policy); err != nil {
		t.Fatalf("decode confirmed policy: %v", err)
	}
	if policy.ContractVersionID != s.contractVersion || policy.FreeCancellationHoursBefore != 48 ||
		policy.NoShowPercent != "100" {
		t.Errorf("policy = %+v; want selected A's version, 48 hours and 100 percent", policy)
	}
}

func TestBookingContractBindingLaterNightVersionKeepsFirstNightPolicy(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.grantNights(t, 10)
	s.putLodgingTerms(t)
	later := s.seedSameProviderContract(t, "2026-06-16", 200)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	hold, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
	if err != nil {
		t.Fatalf("cross-version hold: %v", err)
	}
	quote, err := application.DecodeQuoteSnapshot(hold.Booking.QuoteSnapshot)
	if err != nil || quote.FirstNightContractVersionID == nil ||
		*quote.FirstNightContractVersionID != s.contractVersion || quote.TotalAmount != "2554" {
		t.Fatalf("cross-version quote = %+v, %v; want first A and 1000+777+777", quote, err)
	}
	if later == s.contractVersion {
		t.Fatal("fixture did not create a distinct later version")
	}
	requested, err := s.svc.ConfirmBooking(ctx, s.memberContext(), hold.Booking.ID)
	if err != nil || requested.Booking.ServiceRequestID == nil {
		t.Fatalf("confirm cross-version stay: %v; request=%v", err, requested.Booking.ServiceRequestID)
	}
	s.decideRequest(t, *requested.Booking.ServiceRequestID, servicerequestdomain.StatusApproved)
	s.deliverDecision(t, *requested.Booking.ServiceRequestID)
	final, err := s.svc.GetBooking(ctx, s.deskContext(), hold.Booking.ID)
	if err != nil {
		t.Fatalf("read confirmed stay: %v", err)
	}
	var policy struct {
		ContractVersionID uuid.UUID `json:"contractVersionId"`
	}
	if err := json.Unmarshal(final.Booking.PolicySnapshot, &policy); err != nil ||
		policy.ContractVersionID != s.contractVersion {
		t.Fatalf("cross-version policy = %+v, %v; want first-night A", policy, err)
	}
}

func TestBookingContractBindingRetiredSelectedVersionKeepsItsTerms(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.grantNights(t, 10)
	s.putLodgingTerms(t)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	hold, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	s.seedSameProviderDistractor(t)
	if _, err := s.h.Admin.Exec(ctx, `
		UPDATE contract.contract_version
		SET status='RETIRED', retire_reason_code='TEST_REPLACED'
		WHERE tenant_id=$1 AND id=$2`, s.tenant, s.contractVersion); err != nil {
		t.Fatalf("retire selected version after hold: %v", err)
	}
	requested, err := s.svc.ConfirmBooking(ctx, s.memberContext(), hold.Booking.ID)
	if err != nil || requested.Booking.ServiceRequestID == nil {
		t.Fatalf("confirm retired selected version: %v; request=%v", err, requested.Booking.ServiceRequestID)
	}
	s.decideRequest(t, *requested.Booking.ServiceRequestID, servicerequestdomain.StatusApproved)
	s.deliverDecision(t, *requested.Booking.ServiceRequestID)
	final, err := s.svc.GetBooking(ctx, s.deskContext(), hold.Booking.ID)
	if err != nil {
		t.Fatalf("read confirmed stay: %v", err)
	}
	var policy struct {
		ContractVersionID uuid.UUID `json:"contractVersionId"`
	}
	if err := json.Unmarshal(final.Booking.PolicySnapshot, &policy); err != nil ||
		policy.ContractVersionID != s.contractVersion {
		t.Fatalf("retired selected policy = %+v, %v; want original A", policy, err)
	}
}

func TestBookingContractBindingMissingSelectedTermsCannotBorrowDistractor(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.grantNights(t, 10)
	s.seedSameProviderDistractor(t)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	hold, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if _, err := s.svc.ConfirmBooking(ctx, s.memberContext(), hold.Booking.ID); !errors.Is(err, application.ErrLodgingTermsMissing) {
		t.Fatalf("confirm with only losing contract's terms = %v, want selected terms missing", err)
	}
	var requests, authorizations, vouchers int
	for _, item := range []struct {
		name  string
		query string
		out   *int
	}{
		{"requests", `SELECT count(*) FROM service.service_request WHERE tenant_id=$1`, &requests},
		{"authorizations", `SELECT count(*) FROM service.authorization WHERE tenant_id=$1`, &authorizations},
		{"vouchers", `SELECT count(*) FROM service.voucher WHERE tenant_id=$1`, &vouchers},
	} {
		if err := s.h.Admin.QueryRow(ctx, item.query, s.tenant).Scan(item.out); err != nil {
			t.Fatalf("count %s: %v", item.name, err)
		}
	}
	if requests != 0 || authorizations != 0 || vouchers != 0 {
		t.Errorf("refused confirmation wrote request/auth/voucher = %d/%d/%d",
			requests, authorizations, vouchers)
	}
}

func TestBookingContractBindingAsyncMissingSelectedTermsCannotAdopt(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.grantNights(t, 10)
	s.seedSameProviderDistractor(t) // B has terms; selected A deliberately does not.
	ctx, cancel := s.h.Ctx()
	defer cancel()
	hold, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	quote, err := application.DecodeQuoteSnapshot(hold.Booking.QuoteSnapshot)
	if err != nil {
		t.Fatalf("decode quote: %v", err)
	}
	// A normal ConfirmBooking rightly refuses before raising a request. Build only the
	// synthetic requested state through the real gateway to exercise the worker seam.
	var requestID uuid.UUID
	err = db.WithTenantTx(ctx, s.h.App, db.TenantContext{TenantID: s.tenant, ActorID: s.actor},
		func(ctx context.Context, tx pgx.Tx) error {
			request, err := accommodationgw.NewRequests(s.requests).CreateReservation(ctx, tx,
				s.memberContext(), application.BookingRequestInput{
					PersonID: hold.Booking.PersonID, EnrollmentID: hold.Booking.EnrollmentID,
					ProviderOrganizationID: s.providerOr, ServiceDefinitionID: s.definition,
					ServiceDate: hold.Booking.CheckIn, RequestedStartAt: hold.Booking.CheckIn,
					RequestedEndAt: hold.Booking.CheckOut, Nights: quote.CoveredNights,
					UnitType: "NIGHT", Amount: quote.PayerAmount, CurrencyCode: quote.CurrencyCode,
					Channel:    accommodationdomain.ChannelMemberPortal,
					HeldNights: strconv.Itoa(quote.CoveredNights),
				})
			if err != nil {
				return err
			}
			requestID = request.ID
			written, err := accommodationpg.NewBookings().SetBookingRequest(ctx, tx, s.tenant,
				hold.Booking.ID, requestID, s.actor)
			if err != nil {
				return err
			}
			if !written {
				return errors.New("synthetic request was not attached to its hold")
			}
			return nil
		})
	if err != nil {
		t.Fatalf("construct requested hold: %v", err)
	}
	// The worker sees an approved decision. It must read A's missing policy before it
	// calls CreateForRequest, even though B's populated terms remain available.
	payload, err := json.Marshal(map[string]any{"serviceRequestId": requestID, "status": "APPROVED"})
	if err != nil {
		t.Fatalf("encode synthetic decision: %v", err)
	}
	if err := s.svc.HandleServiceRequestDecided(ctx, outbox.Delivery{
		TenantID: uuid.NullUUID{UUID: s.tenant, Valid: true}, Payload: payload,
	}); !errors.Is(err, application.ErrLodgingTermsMissing) {
		t.Fatalf("async approval with selected A terms missing = %v", err)
	}
	var authorizations, vouchers int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM service.authorization WHERE tenant_id=$1`,
		s.tenant).Scan(&authorizations); err != nil {
		t.Fatalf("count authorizations: %v", err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM service.voucher WHERE tenant_id=$1`,
		s.tenant).Scan(&vouchers); err != nil {
		t.Fatalf("count vouchers: %v", err)
	}
	if authorizations != 0 || vouchers != 0 {
		t.Errorf("missing selected terms wrote authorizations/vouchers = %d/%d", authorizations, vouchers)
	}
	still, err := s.svc.GetBooking(ctx, s.deskContext(), hold.Booking.ID)
	if err != nil || still.Booking.AuthorizationID != nil || still.Booking.VoucherID != nil ||
		still.Booking.Status != accommodationdomain.BookingHold {
		t.Errorf("missing terms changed booking: %+v, %v", still.Booking, err)
	}
}

func TestBookingContractBindingLegacyHoldRefusesAndRemainsReadable(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.grantNights(t, 10)
	s.putLodgingTerms(t)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	hold, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	if _, err := s.h.Admin.Exec(ctx, `
		UPDATE accommodation.booking
		SET quote_snapshot=(quote_snapshot - 'firstNightContractVersionId') || '{"version":1}'::jsonb
		WHERE tenant_id=$1 AND id=$2`, s.tenant, hold.Booking.ID); err != nil {
		t.Fatalf("make historical v1 hold: %v", err)
	}
	path := "/api/v1/accommodation/bookings/" + hold.Booking.ID.String()
	read := s.do(t, http.MethodGet, path, bookerPermissions, nil, s.memberHeaders()...)
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"version":1`) {
		t.Fatalf("read historical v1 = %d: %s", read.Code, read.Body.String())
	}
	refused := s.do(t, http.MethodPost, path+"/confirm", bookerPermissions, nil, s.memberHeaders()...)
	if refused.Code != http.StatusConflict || !strings.Contains(refused.Body.String(), "QUOTE_STALE") {
		t.Fatalf("confirm v1 = %d: %s", refused.Code, refused.Body.String())
	}
	var requests int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM service.service_request WHERE tenant_id=$1`,
		s.tenant).Scan(&requests); err != nil {
		t.Fatalf("count requests: %v", err)
	}
	if requests != 0 {
		t.Errorf("legacy refusal wrote %d requests", requests)
	}
	if _, err := s.svc.ReleaseHold(ctx, s.memberContext(), hold.Booking.ID); err != nil {
		t.Fatalf("release historical v1 hold: %v", err)
	}
}

func TestBookingContractBindingCorruptV2NeverRaisesRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit string
	}{
		{"missing identity", `quote_snapshot - 'firstNightContractVersionId'`},
		{"zero identity", `jsonb_set(quote_snapshot, '{firstNightContractVersionId}', '"00000000-0000-0000-0000-000000000000"'::jsonb)`},
		{"malformed identity", `jsonb_set(quote_snapshot, '{firstNightContractVersionId}', '"bad-uuid"'::jsonb)`},
		{"unknown version", `jsonb_set(quote_snapshot, '{version}', '3'::jsonb)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(t)
			s.clock.At(t, "2026-06-13T09:00:00Z")
			s.grantNights(t, 10)
			s.putLodgingTerms(t)
			ctx, cancel := s.h.Ctx()
			defer cancel()
			hold, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
			if err != nil {
				t.Fatalf("hold: %v", err)
			}
			// The expressions are fixed test cases, never request input.
			query := `UPDATE accommodation.booking SET quote_snapshot=` + tc.edit +
				` WHERE tenant_id=$1 AND id=$2`
			if _, err := s.h.Admin.Exec(ctx, query, s.tenant, hold.Booking.ID); err != nil {
				t.Fatalf("corrupt synthetic quote: %v", err)
			}
			path := "/api/v1/accommodation/bookings/" + hold.Booking.ID.String() + "/confirm"
			refused := s.do(t, http.MethodPost, path, bookerPermissions, nil, s.memberHeaders()...)
			if refused.Code != http.StatusInternalServerError ||
				!strings.Contains(refused.Body.String(), "INTERNAL_ERROR") {
				t.Fatalf("corrupt v2 confirm = %d: %s", refused.Code, refused.Body.String())
			}
			var requests int
			if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM service.service_request WHERE tenant_id=$1`,
				s.tenant).Scan(&requests); err != nil {
				t.Fatalf("count requests: %v", err)
			}
			if requests != 0 {
				t.Errorf("corrupt v2 raised %d requests", requests)
			}
		})
	}
}

func TestBookingContractBindingLegacyRequestedDecisionIsPermanentBeforeAdoption(t *testing.T) {
	for _, status := range []string{accommodationdomain.BookingHold, accommodationdomain.BookingPendingApproval} {
		t.Run(status, func(t *testing.T) {
			testLegacyRequestedDecision(t, status)
		})
	}
}

func testLegacyRequestedDecision(t *testing.T, status string) {
	s := newServer(t)
	s.clock.At(t, "2026-06-13T09:00:00Z")
	s.grantNights(t, 10)
	s.putLodgingTerms(t)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	hold, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	confirmed, err := s.svc.ConfirmBooking(ctx, s.memberContext(), hold.Booking.ID)
	if err != nil || confirmed.Booking.ServiceRequestID == nil {
		t.Fatalf("raise request: %v; request=%v", err, confirmed.Booking.ServiceRequestID)
	}
	if _, err := s.h.Admin.Exec(ctx, `
		UPDATE accommodation.booking
		SET quote_snapshot=(quote_snapshot - 'firstNightContractVersionId') || '{"version":1}'::jsonb,
		    status=$3
		WHERE tenant_id=$1 AND id=$2`, s.tenant, hold.Booking.ID, status); err != nil {
		t.Fatalf("make requested historical v1 hold: %v", err)
	}
	s.decideRequest(t, *confirmed.Booking.ServiceRequestID, servicerequestdomain.StatusApproved)
	var payload []byte
	if err := s.h.Admin.QueryRow(ctx, `
		SELECT payload_json FROM system.outbox_event
		WHERE tenant_id=$1 AND event_type=$2 AND aggregate_id=$3
		ORDER BY occurred_at DESC LIMIT 1`, s.tenant, servicerequestapp.DecidedEvent,
		*confirmed.Booking.ServiceRequestID).Scan(&payload); err != nil {
		t.Fatalf("read decided event: %v", err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		err := s.svc.HandleServiceRequestDecided(ctx, outbox.Delivery{
			TenantID: uuid.NullUUID{UUID: s.tenant, Valid: true}, Payload: payload,
		})
		if outbox.KindOf(err) != outbox.KindPermanent || err == nil {
			t.Fatalf("legacy decision attempt %d = %v, want permanent incompatibility", attempt, err)
		}
	}
	var authorizations, vouchers int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM service.authorization WHERE tenant_id=$1`,
		s.tenant).Scan(&authorizations); err != nil {
		t.Fatalf("count authorizations: %v", err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM service.voucher WHERE tenant_id=$1`,
		s.tenant).Scan(&vouchers); err != nil {
		t.Fatalf("count vouchers: %v", err)
	}
	if authorizations != 0 || vouchers != 0 {
		t.Errorf("legacy decision wrote authorizations/vouchers = %d/%d", authorizations, vouchers)
	}
	still, err := s.svc.GetBooking(ctx, s.deskContext(), hold.Booking.ID)
	if err != nil {
		t.Fatalf("read unchanged booking: %v", err)
	}
	if still.Booking.AuthorizationID != nil || still.Booking.VoucherID != nil ||
		still.Booking.Status != status {
		t.Errorf("legacy decision changed booking: status=%s auth=%v voucher=%v",
			still.Booking.Status, still.Booking.AuthorizationID, still.Booking.VoucherID)
	}
	if still.Booking.EntitlementReservationID == nil ||
		*still.Booking.EntitlementReservationID != *hold.Booking.EntitlementReservationID {
		t.Errorf("legacy decision replaced the existing hold reservation")
	}
}
