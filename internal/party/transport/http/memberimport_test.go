package partyhttp_test

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/identity"
	identityapp "github.com/celikbros/kapsora/internal/identity/application"
	identitypg "github.com/celikbros/kapsora/internal/identity/infrastructure/postgres"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
	"github.com/celikbros/kapsora/internal/party/memberimport"
	partyhttp "github.com/celikbros/kapsora/internal/party/transport/http"
	"github.com/celikbros/kapsora/internal/platform/crypto/localkey"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// Real provisioned grants, HTTP parsing, PostgreSQL staging and worker apply share one
// flow here. No test header injects import.execute into the request context.
func TestProgramManagerImportsMembersThroughHTTP(t *testing.T) {
	h := dbtest.New(t)
	ctx, cancel := h.Ctx()
	defer cancel()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	prov := identityapp.NewProvisioner(identitypg.NewProvisioningRepository(h.App), nil)
	tenant, err := prov.ProvisionTenant(ctx, identityapp.ProvisionCommand{
		Code: "HTTP_IMPORT", LegalName: "Import Test", DisplayName: "Import Test",
	})
	must(err)
	actor := h.CreateActor("http-import-manager", "Import Manager")
	_, err = prov.GrantRole(ctx, identityapp.GrantRoleInput{TenantID: tenant, ActorID: actor, RoleCode: "PROGRAM_MANAGER"})
	must(err)
	sponsor := h.CreateTenantOrganization(tenant, "Synthetic Sponsor", "SPONSOR")
	keys, err := localkey.New([]byte("0123456789abcdef0123456789abcdef"))
	must(err)
	cursors, err := httpx.NewCursorCodec([]byte("fedcba9876543210fedcba9876543210"))
	must(err)
	svc, err := memberimport.New(memberimport.Deps{Pool: h.App, Cipher: keys, Index: keys, Cursors: cursors, Audit: auditpg.New()})
	must(err)
	handler := partyhttp.NewImportHandler(svc, &denyRecorder{}, nil)
	authz := identityapp.NewAuthorizer(identitypg.NewAuthorizationRepository(h.App), identitypg.NewSessionStore(h.App), nil, nil)
	stepped := false
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			session := identity.Session{ID: "import-test", ActorID: actor, ActiveTenantID: uuid.NullUUID{UUID: tenant, Valid: true}}
			if stepped {
				session.StepUpUntil = time.Now().Add(time.Minute)
			}
			rc, err := authz.ResolveTenantContext(r.Context(), session, tenant, identity.AppBackoffice, "http-import")
			if err != nil {
				identityhttp.WriteAuthError(w, r, err, nil)
				return
			}
			next.ServeHTTP(w, r.WithContext(identity.WithRequestContext(r.Context(), rc)))
		})
	})
	router.Route("/api/v1/imports/members", func(r chi.Router) { handler.Routes(r, nil) })

	var payload bytes.Buffer
	form := multipart.NewWriter(&payload)
	must(form.WriteField("sponsorOrganizationId", sponsor.String()))
	must(form.WriteField("sourceSystem", "HTTP_TEST"))
	must(form.WriteField("sourceVersion", "v1"))
	part, err := form.CreateFormFile("file", "synthetic-members.csv")
	must(err)
	_, err = io.WriteString(part, strings.Join(memberimport.Columns, ",")+"\n"+
		"R1,Import,,Synthetic,1990-01-01,FEMALE,,MEMBER1,,PRINCIPAL,,,2026-01-01,,\n")
	must(err)
	must(form.Close())
	call := func(method, path, etag string, body io.Reader, contentType string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, body).WithContext(ctx)
		if etag != "" {
			req.Header.Set("If-Match", etag)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	upload := func() *httptest.ResponseRecorder {
		return call(http.MethodPost, "/api/v1/imports/members/", "", bytes.NewReader(payload.Bytes()), form.FormDataContentType())
	}
	denied := upload()
	if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), "STEP_UP_REQUIRED") {
		t.Fatalf("upload without step-up: %d %s", denied.Code, denied.Body.String())
	}
	stepped = true
	created := upload()
	if created.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", created.Code, created.Body.String())
	}
	var batch struct {
		ID       uuid.UUID                             `json:"id"`
		Status   string                                `json:"status"`
		Counters struct{ Valid, Invalid, Created int } `json:"counters"`
	}
	must(json.Unmarshal(created.Body.Bytes(), &batch))
	if batch.Status != "READY" || batch.Counters.Valid != 1 || batch.Counters.Invalid != 0 {
		t.Fatalf("staged batch: %+v", batch)
	}
	path := "/api/v1/imports/members/" + batch.ID.String()
	stepped = false
	denied = call(http.MethodPost, path+"/apply", created.Header().Get("ETag"), nil, "")
	if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), "STEP_UP_REQUIRED") {
		t.Fatalf("apply without step-up: %d %s", denied.Code, denied.Body.String())
	}
	stepped = true
	accepted := call(http.MethodPost, path+"/apply", created.Header().Get("ETag"), nil, "")
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("apply: %d %s", accepted.Code, accepted.Body.String())
	}
	var jobs int
	must(h.Admin.QueryRow(ctx, `SELECT count(*) FROM system.outbox_event
        WHERE tenant_id = $1 AND aggregate_id = $2 AND event_type = $3`, tenant, batch.ID, memberimport.ApplyEvent).Scan(&jobs))
	if jobs != 1 {
		t.Fatalf("queued apply jobs = %d, want 1", jobs)
	}
	// Execute the worker's operation twice to verify redelivery has no duplicate effect.
	must(svc.RunApply(ctx, tenant, batch.ID))
	must(svc.RunApply(ctx, tenant, batch.ID))
	done := call(http.MethodGet, path, "", nil, "")
	if done.Code != http.StatusOK {
		t.Fatalf("get: %d %s", done.Code, done.Body.String())
	}
	must(json.Unmarshal(done.Body.Bytes(), &batch))
	if batch.Status != "APPLIED" || batch.Counters.Created != 1 {
		t.Fatalf("result: %+v", batch)
	}
	var members int
	must(h.Admin.QueryRow(ctx, `SELECT count(*) FROM party.sponsor_membership
        WHERE tenant_id = $1 AND external_member_no = 'MEMBER1'`, tenant).Scan(&members))
	if members != 1 {
		t.Fatalf("imported memberships = %d, want 1", members)
	}
	duplicate := upload()
	if duplicate.Code != http.StatusConflict || !strings.Contains(duplicate.Body.String(), "IMPORT_DUPLICATE") {
		t.Fatalf("duplicate upload: %d %s", duplicate.Code, duplicate.Body.String())
	}
}
