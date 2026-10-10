package accommodationhttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/accommodation/application"
	accommodationdomain "github.com/celikbros/kapsora/internal/accommodation/domain"
	accommodationgw "github.com/celikbros/kapsora/internal/accommodation/infrastructure/gateway"
	accommodationpg "github.com/celikbros/kapsora/internal/accommodation/infrastructure/postgres"
	contractapp "github.com/celikbros/kapsora/internal/contract/application"
	contractpg "github.com/celikbros/kapsora/internal/contract/infrastructure/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
	servicerequestdomain "github.com/celikbros/kapsora/internal/servicerequest/domain"
)

// controlledLodgingPolicy delegates to the real contract gateway. Its callbacks place a
// change exactly between the completed external policy read and the booking write phase.
type controlledLodgingPolicy struct {
	base   application.LodgingPolicyPort
	before func() error
	after  func() error
	calls  int
	ids    []uuid.UUID
}

func (p *controlledLodgingPolicy) SnapshotPolicy(ctx context.Context, rc identity.RequestContext,
	versionID uuid.UUID, zone string,
) (json.RawMessage, error) {
	p.calls++
	p.ids = append(p.ids, versionID)
	if p.before != nil {
		if err := p.before(); err != nil {
			return nil, err
		}
	}
	policy, err := p.base.SnapshotPolicy(ctx, rc, versionID, zone)
	if err != nil {
		return nil, err
	}
	if p.after != nil {
		if err := p.after(); err != nil {
			return nil, err
		}
	}
	return policy, nil
}

func controlledConfirmationService(t *testing.T, s *server) (*application.Service, *controlledLodgingPolicy) {
	t.Helper()
	cursors, err := httpx.NewCursorCodec([]byte("binding-preparation-test-cursor"))
	if err != nil {
		t.Fatal(err)
	}
	contractSvc, err := contractapp.New(contractapp.Deps{
		Pool: s.h.App, Repo: contractpg.New(), Cursors: cursors, Now: s.clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := &controlledLodgingPolicy{base: accommodationgw.NewPolicies(contractSvc)}
	svc, err := application.New(application.Deps{
		Pool: s.h.App, Repo: accommodationpg.New(), Bookings: accommodationpg.NewBookings(),
		Requests: accommodationgw.NewRequests(s.requests), Policies: policy, Now: s.clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc, policy
}

func TestBookingContractBindingTransientPolicyReadRetriesBeforeRequest(t *testing.T) {
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
	svc, policy := controlledConfirmationService(t, s)
	transient := errors.New("synthetic contract policy read unavailable")
	policy.before = func() error {
		if policy.calls == 1 {
			return transient
		}
		return nil
	}
	if _, err := svc.ConfirmBooking(ctx, s.memberContext(), hold.Booking.ID); !errors.Is(err, transient) {
		t.Fatalf("first policy read = %v, want transient error", err)
	}
	var requests, authorizations int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM service.service_request WHERE tenant_id=$1`,
		s.tenant).Scan(&requests); err != nil {
		t.Fatalf("count requests: %v", err)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM service.authorization WHERE tenant_id=$1`,
		s.tenant).Scan(&authorizations); err != nil {
		t.Fatalf("count authorizations: %v", err)
	}
	if requests != 0 || authorizations != 0 {
		t.Fatalf("transient policy failure wrote request/auth = %d/%d", requests, authorizations)
	}
	retried, err := svc.ConfirmBooking(ctx, s.memberContext(), hold.Booking.ID)
	if err != nil || retried.Booking.ServiceRequestID == nil {
		t.Fatalf("retry after policy recovery: %v; request=%v", err, retried.Booking.ServiceRequestID)
	}
	if policy.calls != 2 || len(policy.ids) != 2 ||
		policy.ids[0] != s.contractVersion || policy.ids[1] != s.contractVersion {
		t.Errorf("policy reads = %d %v, want two reads of selected A", policy.calls, policy.ids)
	}
}

func TestBookingContractBindingPreparedPolicyRechecksLockedBooking(t *testing.T) {
	for _, tc := range []struct {
		name       string
		change     func(t *testing.T, s *server, bookingID uuid.UUID) error
		transition bool
	}{
		{"quote identity changed", func(t *testing.T, s *server, bookingID uuid.UUID) error {
			ctx, cancel := s.h.Ctx()
			defer cancel()
			_, err := s.h.Admin.Exec(ctx, `
				UPDATE accommodation.booking
				SET quote_snapshot=jsonb_set(quote_snapshot, '{firstNightContractVersionId}', to_jsonb($3::text))
				WHERE tenant_id=$1 AND id=$2`, s.tenant, bookingID, uuid.NewString())
			return err
		}, false},
		{"property timezone changed", func(t *testing.T, s *server, _ uuid.UUID) error {
			ctx, cancel := s.h.Ctx()
			defer cancel()
			_, err := s.h.Admin.Exec(ctx, `UPDATE accommodation.property SET timezone='UTC'
				WHERE tenant_id=$1 AND id=$2`, s.tenant, s.property)
			return err
		}, false},
		{"booking released", func(t *testing.T, s *server, bookingID uuid.UUID) error {
			ctx, cancel := s.h.Ctx()
			defer cancel()
			_, err := s.svc.ReleaseHold(ctx, s.memberContext(), bookingID)
			return err
		}, true},
		{"hold deadline passed", func(_ *testing.T, s *server, _ uuid.UUID) error {
			s.clock.Set(s.clock.Now().Add(16 * time.Minute))
			return nil
		}, true},
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
			svc, policy := controlledConfirmationService(t, s)
			policy.after = func() error { return tc.change(t, s, hold.Booking.ID) }
			_, err = svc.ConfirmBooking(ctx, s.memberContext(), hold.Booking.ID)
			if err == nil || (tc.transition && !errors.Is(err, application.ErrBookingTransitionInvalid)) {
				t.Fatalf("confirm after %s = %v, want locked refusal", tc.name, err)
			}
			if policy.calls != 1 || len(policy.ids) != 1 || policy.ids[0] != s.contractVersion {
				t.Errorf("prepared policy read = %d %v, want selected A once", policy.calls, policy.ids)
			}
			var requests int
			if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM service.service_request WHERE tenant_id=$1`,
				s.tenant).Scan(&requests); err != nil {
				t.Fatalf("count requests: %v", err)
			}
			if requests != 0 {
				t.Errorf("locked refusal raised %d requests", requests)
			}
		})
	}
}

func TestBookingContractBindingHistoricalConfirmedV1KeepsPolicyAndRedelivery(t *testing.T) {
	s := newServer(t)
	s.clock.At(t, "2026-06-12T09:00:00Z")
	s.grantNights(t, 10)
	s.putLodgingTerms(t)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	hold, err := s.svc.CreateHold(ctx, s.memberContext(), s.holdInput(s.person, s.roomType))
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	requested, err := s.svc.ConfirmBooking(ctx, s.memberContext(), hold.Booking.ID)
	if err != nil || requested.Booking.ServiceRequestID == nil {
		t.Fatalf("confirm: %v; request=%v", err, requested.Booking.ServiceRequestID)
	}
	s.decideRequest(t, *requested.Booking.ServiceRequestID, servicerequestdomain.StatusApproved)
	s.deliverDecision(t, *requested.Booking.ServiceRequestID)
	if _, err := s.h.Admin.Exec(ctx, `
		UPDATE accommodation.booking
		SET quote_snapshot=(quote_snapshot - 'firstNightContractVersionId') || '{"version":1}'::jsonb
		WHERE tenant_id=$1 AND id=$2`, s.tenant, hold.Booking.ID); err != nil {
		t.Fatalf("mark confirmed booking as historical v1: %v", err)
	}
	path := "/api/v1/accommodation/bookings/" + hold.Booking.ID.String()
	read := s.do(t, http.MethodGet, path, bookerPermissions, nil, s.memberHeaders()...)
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"version":1`) {
		t.Fatalf("read confirmed v1 = %d: %s", read.Code, read.Body.String())
	}
	// A terminal redelivery must return before asking the legacy quote for provenance.
	s.deliverDecision(t, *requested.Booking.ServiceRequestID)
	before, err := s.svc.GetBooking(ctx, s.memberContext(), hold.Booking.ID)
	if err != nil || before.Booking.PolicySnapshot == nil ||
		before.Booking.Status != accommodationdomain.BookingConfirmed {
		t.Fatalf("historical confirmation = %+v, %v", before.Booking, err)
	}
	if _, err := s.svc.CancelBooking(ctx, s.memberContext(), hold.Booking.ID, ""); err != nil {
		t.Fatalf("cancel historical confirmed v1 under frozen policy: %v", err)
	}
	after, err := s.svc.GetBooking(ctx, s.memberContext(), hold.Booking.ID)
	if err != nil || after.Booking.Status != accommodationdomain.BookingCancelled {
		t.Fatalf("historical cancellation = %+v, %v", after.Booking, err)
	}
	if !bytes.Equal(before.Booking.PolicySnapshot, after.Booking.PolicySnapshot) {
		t.Errorf("historical cancellation changed frozen policy")
	}
}
