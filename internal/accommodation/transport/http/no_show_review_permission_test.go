package accommodationhttp_test

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/accommodation/application"
	accommodationhttp "github.com/celikbros/kapsora/internal/accommodation/transport/http"
	"github.com/celikbros/kapsora/internal/identity"
)

// The narrow payer grant must work without booking management. Neither a second
// hotel clerk nor a member can turn the provider's report into a charge.
func TestNoShowReviewPermissionBoundary(t *testing.T) {
	for _, permission := range []string{application.PermissionNoShowReview, application.PermissionBookingManage} {
		t.Run(permission, func(t *testing.T) {
			s := newServer(t)
			s.grantNights(t, 10)
			s.putLodgingTerms(t)
			s.clock.At(t, insideFreeWindow)
			booking := s.confirmBooking(t, s.person, s.roomType)
			clerk := s.h.CreateActor("reporter", "Reporter")
			reviewer := s.h.CreateActor("reviewer", "Reviewer")
			ctx, cancel := s.h.Ctx()
			defer cancel()
			s.clock.At(t, afterCheckInCloses)
			evidence := s.linkCleanBookingDocument(t, booking.Booking.ID)
			if _, err := s.svc.ReportNoShow(ctx, s.providerContext(clerk), booking.Booking.ID,
				application.ReportNoShowInput{EvidenceDocumentID: &evidence}); err != nil {
				t.Fatal(err)
			}
			handler := accommodationhttp.NewHandler(s.svc, denyRecorder{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			router := chi.NewRouter()
			router.Route("/api/v1/accommodation/bookings", func(r chi.Router) { handler.AfterBookingRoutes(r, accommodationhttp.AfterMiddlewares{}) })
			payer := func() identity.RequestContext {
				rc := s.payerContext(reviewer)
				rc.App = identity.AppBackoffice
				rc.Scopes = []identity.Scope{{Type: "TENANT"}}
				rc.Permissions = map[string]struct{}{permission: {}}
				return rc
			}
			call := func(rc identity.RequestContext, status int) {
				t.Helper()
				req := httptest.NewRequest(http.MethodPost, "/api/v1/accommodation/bookings/"+booking.Booking.ID.String()+"/no-show/review", bytes.NewBufferString(`{"status":"CONFIRMED"}`))
				req = req.WithContext(identity.WithRequestContext(req.Context(), rc))
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if rec.Code != status {
					t.Fatalf("status=%d, want %d: %s", rec.Code, status, rec.Body.String())
				}
			}
			cases := []struct {
				name   string
				status int
				change func(*identity.RequestContext)
			}{
				{"missing permission", 403, func(rc *identity.RequestContext) { rc.Permissions = map[string]struct{}{} }},
				{"other hotel clerk", 403, func(rc *identity.RequestContext) { rc.Scopes = s.providerContext(reviewer).Scopes }},
				{"empty organization", 403, func(rc *identity.RequestContext) { rc.Scopes = []identity.Scope{{Type: "ORGANIZATION"}} }},
				{"mixed scope", 403, func(rc *identity.RequestContext) { rc.Scopes = append(rc.Scopes, identity.Scope{Type: "ORGANIZATION"}) }},
				{"member binding", 403, func(rc *identity.RequestContext) { rc.PersonID = uuid.NullUUID{UUID: s.person, Valid: true} }},
				{"empty person scope", 403, func(rc *identity.RequestContext) { rc.Scopes = []identity.Scope{{Type: "PERSON"}} }},
				{"provider app", 403, func(rc *identity.RequestContext) { rc.App = identity.AppProvider }},
				{"member app", 403, func(rc *identity.RequestContext) { rc.App = identity.AppMember }},
				{"own file", 403, func(rc *identity.RequestContext) { rc.SelfPersonID = uuid.NullUUID{UUID: s.person, Valid: true} }},
				{"reporter", 403, func(rc *identity.RequestContext) { rc.Principal.ActorID = clerk }},
				{"foreign tenant", 404, func(rc *identity.RequestContext) { rc.TenantID = s.h.CreateTenant("NOSHOW_FOREIGN") }},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					rc := payer()
					tc.change(&rc)
					call(rc, tc.status)
					available, reserved, consumed := s.balances(t)
					if available != "9.000000" || reserved != "3.000000" || consumed != "0.000000" {
						t.Fatalf("denied review moved balances: %s/%s/%s", available, reserved, consumed)
					}
				})
			}
			call(payer(), 200)
			call(payer(), 409)
			available, reserved, consumed := s.balances(t)
			if available != "9.000000" || reserved != "0.000000" || consumed != "3.000000" {
				t.Fatalf("review must consume exactly once: %s/%s/%s", available, reserved, consumed)
			}
			for _, day := range []string{checkIn, "2026-06-16", lastNight} {
				_, _, confirmed := s.inventoryOf(t, s.roomType, day)
				if confirmed != 0 {
					t.Errorf("inventory for %s remains confirmed: %d", day, confirmed)
				}
			}
			s.assertConservation(t, "payer review permission boundary")
		})
	}
}
