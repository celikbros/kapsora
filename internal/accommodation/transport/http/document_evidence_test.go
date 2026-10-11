package accommodationhttp_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	documentapp "github.com/celikbros/kapsora/internal/document/application"
	documentpg "github.com/celikbros/kapsora/internal/document/infrastructure/postgres"
	documenthttp "github.com/celikbros/kapsora/internal/document/transport/http"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/objectstore"
)

// Exercise the desk's narrow grant through the HTTP boundary with real booking and
// document repositories. Neither a generic link grant nor a tenant-wide scope is implied.
func TestDeskBookingEvidenceLinkBoundary(t *testing.T) {
	s := newServer(t)
	s.grantNights(t, 10)
	s.putLodgingTerms(t)
	s.clock.At(t, insideFreeWindow)
	booking := s.confirmBooking(t, s.person, s.roomType)
	svc, err := documentapp.New(documentapp.Deps{Pool: s.h.App, Repo: documentpg.New(), Store: objectstore.NewMemory()})
	if err != nil {
		t.Fatal(err)
	}
	handler := documenthttp.NewHandler(svc, denyRecorder{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	router := chi.NewRouter()
	router.Route("/api/v1/documents", func(r chi.Router) { handler.DocumentRoutes(r, documenthttp.Middlewares{}) })
	ctx, cancel := s.h.Ctx()
	defer cancel()
	cases := []struct {
		name   string
		status int
	}{
		{"own clean evidence", 201}, {"scanning own evidence", 201}, {"own duplicate", 201},
		{"pending booking", 404}, {"other provider target", 404}, {"unknown target", 404}, {"foreign tenant", 404},
		{"other provider document", 404}, {"tenant-owned document", 404},
		{"health document", 404}, {"restricted existing link", 404}, {"purged document", 404},
		{"foreign canonical", 404}, {"purged canonical", 404}, {"health canonical", 404},
		{"restricted canonical", 404}, {"absent scope", 404}, {"empty scope", 404},
		{"wrong type", 403}, {"wrong aggregate", 403}, {"sets permission", 403}, {"missing grant", 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evidence := s.linkCleanBookingDocument(t, uuid.New())
			rc := s.providerContext(s.actor)
			rc.Permissions = map[string]struct{}{documentapp.PermissionBookingEvidenceLink: {}}
			in := documentapp.NewLinkInput{AggregateType: "BOOKING", AggregateID: booking.Booking.ID, DocumentTypeCode: "NO_SHOW_EVIDENCE"}
			switch tc.name {
			case "scanning own evidence":
				s.h.AdminExec(`UPDATE document.object SET scan_status='SCANNING',bucket='quarantine' WHERE tenant_id=$1 AND id=$2`, s.tenant, evidence)
			case "pending booking":
				s.h.AdminExec(`UPDATE accommodation.booking SET status='PENDING_APPROVAL',confirmed_at=NULL WHERE tenant_id=$1 AND id=$2`, s.tenant, booking.Booking.ID)
				defer s.h.AdminExec(`UPDATE accommodation.booking SET status='CONFIRMED',confirmed_at=$3 WHERE tenant_id=$1 AND id=$2`, s.tenant, booking.Booking.ID, booking.Booking.ConfirmedAt)
			case "other provider target":
				s.h.AdminExec(`UPDATE accommodation.property SET provider_organization_id=$3 WHERE tenant_id=$1 AND id=$2`, s.tenant, s.property, s.otherOr)
				defer s.h.AdminExec(`UPDATE accommodation.property SET provider_organization_id=$3 WHERE tenant_id=$1 AND id=$2`, s.tenant, s.property, s.providerOr)
			case "unknown target":
				in.AggregateID = uuid.New()
			case "foreign tenant":
				rc.TenantID = s.h.CreateTenant("EVIDENCE_OTHER")
			case "other provider document":
				s.h.AdminExec(`UPDATE document.object SET owner_tenant_organization_id=$3 WHERE tenant_id=$1 AND id=$2`, s.tenant, evidence, s.otherOr)
			case "tenant-owned document":
				s.h.AdminExec(`UPDATE document.object SET owner_tenant_organization_id=NULL WHERE tenant_id=$1 AND id=$2`, s.tenant, evidence)
			case "health document":
				s.h.AdminExec(`UPDATE document.object SET classification='HEALTH' WHERE tenant_id=$1 AND id=$2`, s.tenant, evidence)
			case "restricted existing link":
				s.h.AdminExec(`UPDATE document.link SET required_permission='health.clinical.read' WHERE tenant_id=$1 AND object_id=$2`, s.tenant, evidence)
			case "purged document":
				s.h.AdminExec(`UPDATE document.object SET purged_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, s.tenant, evidence)
			case "own duplicate", "foreign canonical", "purged canonical", "health canonical", "restricted canonical":
				canonical := s.linkCleanBookingDocument(t, uuid.New())
				s.h.AdminExec(`UPDATE document.object o SET duplicate_of_object_id=c.id,object_key=c.object_key,sha256=c.sha256 FROM document.object c WHERE o.tenant_id=$1 AND o.id=$2 AND c.tenant_id=o.tenant_id AND c.id=$3`, s.tenant, evidence, canonical)
				switch tc.name {
				case "foreign canonical":
					s.h.AdminExec(`UPDATE document.object SET owner_tenant_organization_id=$3 WHERE tenant_id=$1 AND id=$2`, s.tenant, canonical, s.otherOr)
				case "purged canonical":
					s.h.AdminExec(`UPDATE document.object SET purged_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2`, s.tenant, canonical)
				case "health canonical":
					s.h.AdminExec(`UPDATE document.object SET classification='HEALTH' WHERE tenant_id=$1 AND id=$2`, s.tenant, canonical)
				case "restricted canonical":
					s.h.AdminExec(`UPDATE document.link SET required_permission='health.clinical.read' WHERE tenant_id=$1 AND object_id=$2`, s.tenant, canonical)
				}
			case "absent scope":
				rc.Scopes = nil
			case "empty scope":
				rc.Scopes = []identity.Scope{{Type: "ORGANIZATION"}}
			case "wrong type":
				in.DocumentTypeCode = "INVOICE"
			case "wrong aggregate":
				in.AggregateType = "MEDICAL_REPORT"
			case "sets permission":
				in.RequiredPermission = "health.clinical.read"
			case "missing grant":
				rc.Permissions = map[string]struct{}{"document.upload": {}}
			}
			body, err := json.Marshal(map[string]any{"aggregateType": in.AggregateType, "aggregateId": in.AggregateID, "documentTypeCode": in.DocumentTypeCode, "requiredPermission": in.RequiredPermission})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/documents/"+evidence.String()+"/links", bytes.NewReader(body))
			req = req.WithContext(identity.WithRequestContext(req.Context(), rc))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			var links int
			if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM document.link WHERE tenant_id=$1 AND object_id=$2 AND aggregate_id=$3`, s.tenant, evidence, booking.Booking.ID).Scan(&links); err != nil {
				t.Fatal(err)
			}
			want := 0
			if tc.status == 201 {
				want = 1
			}
			if links != want {
				t.Fatalf("created links=%d, want %d", links, want)
			}
			if tc.status == 201 {
				var result struct {
					ID uuid.UUID `json:"id"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				removal := httptest.NewRequest(http.MethodDelete, "/api/v1/documents/"+evidence.String()+"/links/"+result.ID.String(), nil)
				removal = removal.WithContext(identity.WithRequestContext(removal.Context(), rc))
				denied := httptest.NewRecorder()
				router.ServeHTTP(denied, removal)
				if denied.Code != 403 {
					t.Fatalf("narrow grant can unlink: %d", denied.Code)
				}
			}
		})
	}
	s.assertConservation(t, "evidence attachment")
}
