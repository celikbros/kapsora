// Package healthhttp_test drives the whole module over HTTP against a real database,
// because the thing under test is what leaves the process. The projection is applied in the
// application service, and the only way to prove it holds is to read the bytes the handler
// wrote — which is what the sponsor-HR test below does: it serialises the wire body and
// asserts a diagnosis code and a clinical note appear nowhere in it.
package healthhttp_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	auditpg "github.com/celikbros/kapsora/internal/audit/postgres"
	"github.com/celikbros/kapsora/internal/health/application"
	healthpg "github.com/celikbros/kapsora/internal/health/infrastructure/postgres"
	healthhttp "github.com/celikbros/kapsora/internal/health/transport/http"
	"github.com/celikbros/kapsora/internal/identity"
	identityhttp "github.com/celikbros/kapsora/internal/identity/transport/http"
	"github.com/celikbros/kapsora/internal/platform/dbtest"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// permsHeader lets each request choose what the caller holds, which is the whole subject of
// this package: the same URL answers differently for two callers and must.
const permsHeader = "X-Test-Permissions"

// The permission sets of the roles that matter here, spelled out rather than referenced, so
// this test fails if internal/identity/application/roles.go quietly gains a grant.
const (
	// sponsorHRPermissions is SPONSOR_HR exactly: it may see that a case exists and may
	// never see what it is about.
	sponsorHRPermissions = "member.read,service_request.read,health.case.read,entitlement.read,report.read"
	// clinicianPermissions is PROVIDER_STAFF's health half: it manages cases and reads
	// clinical detail, and holds no sensitive grant.
	clinicianPermissions = "health.case.read,health.case.manage,health.clinical.read"
	// reviewerPermissions is MEDICAL_REVIEWER's: clinical plus the sensitive grant.
	reviewerPermissions = "health.case.read,health.clinical.read,health.sensitive.read"
	auditorPermissions  = "audit.read"
)

// The two clinical facts every test below hunts for in a wire body. They are distinctive
// strings on purpose: a substring scan for "F32.1" or for the note text is only meaningful
// if nothing else in the document could produce it by accident.
const (
	sensitiveCode = "F32.1"
	plainCode     = "J06.9"
	clinicalNote  = "Hasta uyku düzeninden şikayetçi olduğunu belirtti."
)

type denyRecorder struct{ permissions []string }

func (d *denyRecorder) Deny(w http.ResponseWriter, r *http.Request, err error, permission string) {
	d.permissions = append(d.permissions, permission)
	identityhttp.WriteAuthError(w, r, err, nil)
}

type server struct {
	h       *dbtest.Harness
	handler http.Handler

	tenant     uuid.UUID
	actor      uuid.UUID
	membership uuid.UUID
	provider   uuid.UUID
	otherOrg   uuid.UUID
	person     uuid.UUID
	program    uuid.UUID
	enrollment uuid.UUID
	codeSystem uuid.UUID
	codePlain  uuid.UUID
	codeStrict uuid.UUID
	request    uuid.UUID
	// scopes is what the fake context middleware reports as the caller's access grants;
	// a test sets it to bind the caller to one provider organization.
	scopes []identity.Scope
}

func newServer(t *testing.T) *server { //nolint:funlen // one linear fixture reads better whole
	t.Helper()
	h := dbtest.New(t)
	cursors, err := httpx.NewCursorCodec([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := application.New(application.Deps{
		Pool: h.App, Repo: healthpg.New(), Audit: auditpg.New(), Cursors: cursors,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	s := &server{h: h}
	s.tenant = h.CreateTenant("HTTP_HEALTH")
	s.actor = h.CreateActor("health-http-clerk", "Health Clerk")
	s.membership = h.CreateMembership(s.tenant, s.actor)
	s.seedWorld(t)

	handler := healthhttp.NewHandler(svc, &denyRecorder{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Stand-in for RequireTenantContext: the permissions and the grants come from the test.
	fakeContext := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rc := identity.RequestContext{
				TenantID: s.tenant, MembershipID: s.membership,
				Principal:   identity.Principal{ActorID: s.actor},
				StepUpValid: true, Permissions: map[string]struct{}{}, Scopes: s.scopes,
			}
			for _, p := range strings.Split(r.Header.Get(permsHeader), ",") {
				if p != "" {
					rc.Permissions[p] = struct{}{}
				}
			}
			next.ServeHTTP(w, r.WithContext(identity.WithRequestContext(r.Context(), rc)))
		})
	}

	router := chi.NewRouter()
	router.Use(fakeContext)
	router.Route("/api/v1/health-cases", func(r chi.Router) {
		handler.CaseRoutes(r, healthhttp.Middlewares{})
	})
	router.Route("/api/v1/encounters", func(r chi.Router) {
		handler.EncounterRoutes(r, healthhttp.Middlewares{})
	})
	router.Route("/api/v1/health-access-log", handler.AccessLogRoutes)
	s.handler = router
	return s
}

// seedWorld writes everything the module hangs off but does not own: a member enrolled in a
// plan, a provider, a request on a HEALTH-domain service, and a small ICD-10 code system
// with one ordinary code and one the publisher marked sensitive.
func (s *server) seedWorld(t *testing.T) { //nolint:funlen // one linear fixture reads better whole
	t.Helper()
	h := s.h
	ctx, cancel := h.Ctx()
	defer cancel()
	scan := func(dst *uuid.UUID, what, sql string, args ...any) {
		t.Helper()
		if err := h.Admin.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	sponsor := h.CreateTenantOrganization(s.tenant, "Sponsor", "SPONSOR")
	payer := h.CreateTenantOrganization(s.tenant, "Payer", "PAYER")
	s.provider = h.CreateTenantOrganization(s.tenant, "Sağlayıcı A", "PROVIDER")
	s.otherOrg = h.CreateTenantOrganization(s.tenant, "Sağlayıcı B", "PROVIDER")

	h.AdminExec(`INSERT INTO party.membership_type (tenant_id, code, display_name) VALUES ($1, 'MEMBER', 'Üye')`, s.tenant)
	scan(&s.person, "person", `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Deniz', 'Aksoy', 'deniz aksoy') RETURNING id`, s.tenant)
	var membership uuid.UUID
	scan(&membership, "sponsor membership", `
		INSERT INTO party.sponsor_membership (tenant_id, person_id, sponsor_tenant_organization_id,
		                                      membership_type, status, valid_period)
		VALUES ($1, $2, $3, 'MEMBER', 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		s.tenant, s.person, sponsor)

	h.AdminExec(`INSERT INTO benefit.program_type (tenant_id, code, display_name) VALUES ($1, 'BENEFIT', 'Fayda')`, s.tenant)
	scan(&s.program, "program", `
		INSERT INTO benefit.program (tenant_id, sponsor_tenant_organization_id, payer_tenant_organization_id,
		                             code, name, program_type, status, valid_period)
		VALUES ($1, $2, $3, 'PRG', 'Program', 'BENEFIT', 'ACTIVE', daterange('2026-01-01', NULL, '[)'))
		RETURNING id`, s.tenant, sponsor, payer)
	var planID uuid.UUID
	scan(&planID, "plan", `
		INSERT INTO benefit.plan (tenant_id, program_id, code, name, status)
		VALUES ($1, $2, 'PLAN', 'Plan', 'ACTIVE') RETURNING id`, s.tenant, s.program)
	scan(&s.enrollment, "enrollment", `
		INSERT INTO benefit.enrollment (tenant_id, sponsor_membership_id, plan_id, status, valid_period)
		VALUES ($1, $2, $3, 'ACTIVE', daterange('2026-01-01', NULL, '[)')) RETURNING id`,
		s.tenant, membership, planID)

	var category, definition uuid.UUID
	scan(&category, "service category", `
		INSERT INTO catalog.service_category (tenant_id, code, name, domain_code)
		VALUES ($1, 'HEALTH_ROOT', 'Sağlık', 'HEALTH') RETURNING id`, s.tenant)
	scan(&definition, "service definition", `
		INSERT INTO catalog.service_definition (tenant_id, category_id, code, name,
		                                        fulfillment_mode, default_unit_type)
		VALUES ($1, $2, 'PHYSIO', 'Fizyoterapi', 'SESSION', 'SESSION') RETURNING id`,
		s.tenant, category)
	scan(&s.request, "service request", `
		INSERT INTO service.service_request (tenant_id, request_reference, request_type, person_id,
		                                     program_id, enrollment_id, provider_tenant_organization_id,
		                                     service_date, channel)
		VALUES ($1, 'SR-20260615-AAAAAAAA', 'DIRECT_SERVICE', $2, $3, $4, $5, '2026-06-15', 'BACKOFFICE')
		RETURNING id`, s.tenant, s.person, s.program, s.enrollment, s.provider)
	var version uuid.UUID
	scan(&version, "service request version", `
		INSERT INTO service.service_request_version (tenant_id, service_request_id, version_no)
		VALUES ($1, $2, 1) RETURNING id`, s.tenant, s.request)
	h.AdminExec(`
		INSERT INTO service.service_request_item (tenant_id, service_request_version_id, line_no,
		                                          service_definition_id, requested_quantity, unit_type)
		VALUES ($1, $2, 1, $3, 2, 'SESSION')`, s.tenant, version, definition)

	// WP-I5-05 seeds the real ICD-10. What this package needs is only that a code value can
	// say, in its own attributes, that its category is one v1.2 11.10 protects.
	scan(&s.codeSystem, "code system", `
		INSERT INTO catalog.code_system (tenant_id, code, name, version, authority, valid_from)
		VALUES ($1, 'ICD10', 'ICD-10', '2026', 'WHO', '2026-01-01') RETURNING id`, s.tenant)
	scan(&s.codePlain, "plain code value", `
		INSERT INTO catalog.code_value (tenant_id, code_system_id, code, display, valid_from)
		VALUES ($1, $2, $3, 'Üst solunum yolu enfeksiyonu', '2026-01-01') RETURNING id`,
		s.tenant, s.codeSystem, plainCode)
	scan(&s.codeStrict, "sensitive code value", `
		INSERT INTO catalog.code_value (tenant_id, code_system_id, code, display, valid_from, attributes)
		VALUES ($1, $2, $3, 'Orta düzeyde depresif atak', '2026-01-01',
		        '{"chapter":"V","sensitive":true}'::jsonb) RETURNING id`,
		s.tenant, s.codeSystem, sensitiveCode)
}

func (s *server) do(t *testing.T, method, path, permissions string, body any, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set(permsHeader, permissions)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return out
}

type caseBody struct {
	Id         uuid.UUID `json:"id"`
	PersonId   uuid.UUID `json:"personId"`
	Status     string    `json:"status"`
	Projection string    `json:"projection"`
	// Sensitivity is a pointer so "absent" and "STANDARD" are different answers here, which
	// is exactly the difference the financial projection turns on.
	Sensitivity *string `json:"sensitivity"`
	RowVersion  int64   `json:"rowVersion"`
	Encounters  []struct {
		Id            uuid.UUID `json:"id"`
		Projection    string    `json:"projection"`
		BranchCode    *string   `json:"branchCode"`
		NotesClinical *string   `json:"notesClinical"`
		EndedAt       *string   `json:"endedAt"`
	} `json:"encounters"`
}

type casePageBody struct {
	Items []caseBody `json:"items"`
}

type diagnosisListBody struct {
	Items []struct {
		Id            uuid.UUID `json:"id"`
		Code          string    `json:"code"`
		Display       string    `json:"display"`
		DiagnosisType string    `json:"diagnosisType"`
		Sensitive     bool      `json:"sensitive"`
	} `json:"items"`
}

type accessLogBody struct {
	Items []struct {
		PersonId     *uuid.UUID `json:"personId"`
		ResourceType string     `json:"resourceType"`
		ResourceId   *uuid.UUID `json:"resourceId"`
		AccessType   string     `json:"accessType"`
		PurposeCode  *string    `json:"purposeCode"`
		ReasonText   *string    `json:"reasonText"`
		Outcome      string     `json:"outcome"`
	} `json:"items"`
}

type problemBody struct {
	Code   string `json:"code"`
	Status int    `json:"status"`
	Errors []struct {
		Field string `json:"field"`
		Code  string `json:"code"`
	} `json:"errors"`
}

// openCase creates a case, an ended encounter carrying a branch code and clinical notes, and
// the diagnosis set given. It goes through HTTP the whole way: a fixture written straight
// into the database could not prove the write path applies the same rules.
func (s *server) openCase(t *testing.T, codeValueID uuid.UUID, diagnosisType string) (caseID, encounterID uuid.UUID) {
	t.Helper()
	rec := s.do(t, http.MethodPost, "/api/v1/health-cases", clinicianPermissions, map[string]any{
		"personId": s.person, "enrollmentId": s.enrollment, "caseType": "OUTPATIENT",
		"providerOrganizationId": s.provider, "serviceRequestId": s.request,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create case = %d: %s", rec.Code, rec.Body.String())
	}
	created := decode[caseBody](t, rec)

	rec = s.do(t, http.MethodPost, "/api/v1/health-cases/"+created.Id.String()+"/encounters",
		clinicianPermissions, map[string]any{
			"encounterType": "OUTPATIENT",
			"startedAt":     "2026-06-15T09:00:00Z",
			"endedAt":       "2026-06-15T09:40:00Z",
			"branchCode":    "PSK",
			"notesClinical": clinicalNote,
		})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create encounter = %d: %s", rec.Code, rec.Body.String())
	}
	var encounter struct {
		Id uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &encounter); err != nil {
		t.Fatalf("decode encounter: %v", err)
	}

	rec = s.do(t, http.MethodPut, "/api/v1/encounters/"+encounter.Id.String()+"/diagnoses",
		clinicianPermissions, map[string]any{
			"items": []map[string]any{{"codeValueId": codeValueID, "diagnosisType": diagnosisType}},
		})
	if rec.Code != http.StatusOK {
		t.Fatalf("put diagnoses = %d: %s", rec.Code, rec.Body.String())
	}
	return created.Id, encounter.Id
}

// accessEvents reads the raw access rows for one resource, which is where the DENIED and
// SUCCESS assertions below get their evidence from.
func (s *server) accessEvents(t *testing.T, resourceID uuid.UUID) []struct {
	AccessType     string
	Classification string
	Outcome        string
	Purpose        *string
	Reason         *string
	PersonID       *uuid.UUID
} {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()
	rows, err := s.h.Admin.Query(ctx, `
		SELECT access_type, data_classification, outcome, purpose_code, reason_text, person_id
		  FROM audit.access_event
		 WHERE tenant_id = $1 AND resource_id = $2
		 ORDER BY occurred_at, id`, s.tenant, resourceID)
	if err != nil {
		t.Fatalf("read access events: %v", err)
	}
	defer rows.Close()
	var out []struct {
		AccessType     string
		Classification string
		Outcome        string
		Purpose        *string
		Reason         *string
		PersonID       *uuid.UUID
	}
	for rows.Next() {
		var row struct {
			AccessType     string
			Classification string
			Outcome        string
			Purpose        *string
			Reason         *string
			PersonID       *uuid.UUID
		}
		if err := rows.Scan(&row.AccessType, &row.Classification, &row.Outcome,
			&row.Purpose, &row.Reason, &row.PersonID); err != nil {
			t.Fatalf("scan access event: %v", err)
		}
		out = append(out, row)
	}
	return out
}

// TestSponsorHRCannotSeeADiagnosis is the Phase 6 acceptance criterion, at the API.
//
// It is deliberately written against the bytes rather than against the decoded struct. A
// test that asserted `body.Encounters[0].NotesClinical == nil` would pass if the notes came
// back under some other key, or nested inside something the struct ignores; scanning the
// serialised document for the two strings that must never appear cannot. Remove the
// projection from the application service and this test fails on the scan, naming the string
// it found.
func TestSponsorHRCannotSeeADiagnosis(t *testing.T) {
	s := newServer(t)
	caseID, encounterID := s.openCase(t, s.codePlain, "PRIMARY")

	// 1. The list. Every case on the page is the financial projection.
	rec := s.do(t, http.MethodGet, "/api/v1/health-cases?personId="+s.person.String(),
		sponsorHRPermissions, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list cases = %d: %s", rec.Code, rec.Body.String())
	}
	listBytes := rec.Body.String()
	page := decode[casePageBody](t, rec)
	if len(page.Items) != 1 {
		t.Fatalf("list returned %d cases, want 1", len(page.Items))
	}

	// 2. The single read.
	rec = s.do(t, http.MethodGet, "/api/v1/health-cases/"+caseID.String(), sponsorHRPermissions, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get case = %d: %s", rec.Code, rec.Body.String())
	}
	caseBytes := rec.Body.String()
	one := decode[caseBody](t, rec)

	// 3. The encounter on its own.
	rec = s.do(t, http.MethodGet, "/api/v1/encounters/"+encounterID.String(), sponsorHRPermissions, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get encounter = %d: %s", rec.Code, rec.Body.String())
	}
	encounterBytes := rec.Body.String()

	// 4. The diagnoses are a refusal, not an empty list. An empty list would tell the caller
	//    the encounter has no diagnosis, which is a clinical fact it does not hold.
	rec = s.do(t, http.MethodGet, "/api/v1/encounters/"+encounterID.String()+"/diagnoses",
		sponsorHRPermissions, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("list diagnoses as SPONSOR_HR = %d: %s", rec.Code, rec.Body.String())
	}
	refusal := decode[problemBody](t, rec)
	if refusal.Code != "CLINICAL_READ_REQUIRED" {
		t.Fatalf("refusal code = %s, want CLINICAL_READ_REQUIRED", refusal.Code)
	}
	refusalBytes := rec.Body.String()

	// 5. The scan. This is the assertion the criterion is actually about, and it runs
	//    first: nothing a sponsor
	//    HR user was sent contains the diagnosis code or the clinical note, anywhere, under
	//    any key, at any depth.
	for _, body := range []struct{ what, payload string }{
		{"listHealthCases", listBytes},
		{"getHealthCase", caseBytes},
		{"getEncounter", encounterBytes},
		{"listEncounterDiagnoses refusal", refusalBytes},
	} {
		for _, secret := range []struct{ what, value string }{
			{"the diagnosis code", plainCode},
			{"the diagnosis display", "Üst solunum yolu enfeksiyonu"},
			{"the clinical notes", clinicalNote},
			{"the branch code", `"PSK"`},
			{"the sensitivity", "SENSITIVE"},
			{"the sensitivity", "STANDARD"},
		} {
			if strings.Contains(body.payload, secret.value) {
				t.Fatalf("%s answered a sponsor HR user with %s (%q) in the body:\n%s",
					body.what, secret.what, secret.value, body.payload)
			}
		}
	}

	// 6. The structural half, which says the same thing in a way a reader can act on: the
	//    fields are absent and the body names the projection it is.
	for _, view := range []caseBody{page.Items[0], one} {
		if view.Projection != "FINANCIAL" {
			t.Fatalf("projection = %s, want FINANCIAL", view.Projection)
		}
		if view.Sensitivity != nil {
			t.Fatalf("the financial projection carries sensitivity = %q; a sponsor's HR user "+
				"can then tell which members have a protected diagnosis", *view.Sensitivity)
		}
		if len(view.Encounters) != 1 {
			t.Fatalf("case carries %d encounters, want 1", len(view.Encounters))
		}
		enc := view.Encounters[0]
		if enc.BranchCode != nil {
			t.Fatalf("the financial projection carries branchCode = %q", *enc.BranchCode)
		}
		if enc.NotesClinical != nil {
			t.Fatalf("the financial projection carries notesClinical")
		}
		// The encounter's own dates are the half a sponsor legitimately needs.
		if enc.EndedAt == nil {
			t.Fatal("the financial projection dropped the encounter's dates, which are not clinical")
		}
	}

	// 7. And nothing clinical was recorded as read, because nothing clinical was served.
	for _, event := range s.accessEvents(t, caseID) {
		if event.Outcome == "SUCCESS" {
			t.Fatal("a financial-projection read wrote a successful HEALTH access event")
		}
	}
}

// TestClinicalProjectionCarriesEverythingAndIsRecorded is the other side of the same coin:
// the projection withholds clinical detail from a caller who may not have it, and withholds
// nothing from one who may — a guard that refuses everybody is not a guard, it is an outage.
func TestClinicalProjectionCarriesEverythingAndIsRecorded(t *testing.T) {
	s := newServer(t)
	caseID, encounterID := s.openCase(t, s.codePlain, "PRIMARY")

	rec := s.do(t, http.MethodGet, "/api/v1/health-cases/"+caseID.String(), clinicianPermissions, nil,
		"X-Access-Purpose", "TREATMENT", "X-Access-Reason", "kontrol muayenesi")
	if rec.Code != http.StatusOK {
		t.Fatalf("get case = %d: %s", rec.Code, rec.Body.String())
	}
	view := decode[caseBody](t, rec)
	if view.Projection != "CLINICAL" {
		t.Fatalf("projection = %s, want CLINICAL", view.Projection)
	}
	if view.Sensitivity == nil || *view.Sensitivity != "STANDARD" {
		t.Fatalf("clinical projection sensitivity = %v, want STANDARD", view.Sensitivity)
	}
	enc := view.Encounters[0]
	if enc.BranchCode == nil || *enc.BranchCode != "PSK" {
		t.Fatalf("clinical projection branchCode = %v, want PSK", enc.BranchCode)
	}
	if enc.NotesClinical == nil || *enc.NotesClinical != clinicalNote {
		t.Fatalf("clinical projection lost the notes: %v", enc.NotesClinical)
	}

	rec = s.do(t, http.MethodGet, "/api/v1/encounters/"+encounterID.String()+"/diagnoses",
		clinicianPermissions, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list diagnoses = %d: %s", rec.Code, rec.Body.String())
	}
	list := decode[diagnosisListBody](t, rec)
	if len(list.Items) != 1 || list.Items[0].Code != plainCode {
		t.Fatalf("diagnoses = %+v, want one %s", list.Items, plainCode)
	}
	if list.Items[0].Sensitive {
		t.Fatal("an ordinary code came back marked sensitive")
	}

	// The look is on the record, with the reason it was given.
	var found bool
	for _, event := range s.accessEvents(t, caseID) {
		if event.Outcome != "SUCCESS" || event.Classification != "HEALTH" {
			continue
		}
		found = true
		if event.PersonID == nil || *event.PersonID != s.person {
			t.Fatalf("access event person = %v, want %s", event.PersonID, s.person)
		}
		if event.Purpose == nil || *event.Purpose != "TREATMENT" {
			t.Fatalf("access event purpose = %v, want TREATMENT", event.Purpose)
		}
		if event.Reason == nil || *event.Reason != "kontrol muayenesi" {
			t.Fatalf("access event reason = %v", event.Reason)
		}
	}
	if !found {
		t.Fatal("a clinical read wrote no HEALTH access event")
	}
}

// TestSensitiveCaseNeedsThePermissionAndAPurpose walks section 2.3 end to end.
func TestSensitiveCaseNeedsThePermissionAndAPurpose(t *testing.T) {
	s := newServer(t)
	caseID, encounterID := s.openCase(t, s.codeStrict, "PRIMARY")

	// Writing the sensitive diagnosis made the case sensitive; nothing sent it.
	var sensitivity string
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if err := s.h.Admin.QueryRow(ctx,
		`SELECT sensitivity FROM health.health_case WHERE id = $1`, caseID).Scan(&sensitivity); err != nil {
		t.Fatalf("read sensitivity: %v", err)
	}
	if sensitivity != "SENSITIVE" {
		t.Fatalf("case sensitivity = %s, want SENSITIVE", sensitivity)
	}

	// 1. health.clinical.read alone: the financial projection, and no hint of why.
	rec := s.do(t, http.MethodGet, "/api/v1/health-cases/"+caseID.String(), clinicianPermissions, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get sensitive case as clinician = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	view := decode[caseBody](t, rec)
	if view.Projection != "FINANCIAL" {
		t.Fatalf("projection = %s, want FINANCIAL: a clinical reader without the sensitive "+
			"grant sees the same half as anybody else", view.Projection)
	}
	if strings.Contains(body, "SENSITIVE") || strings.Contains(body, clinicalNote) {
		t.Fatalf("the refusal leaked the case's sensitivity or its notes:\n%s", body)
	}
	// The refusal is a row: "who tried" is as much of the record as "who looked". Exactly
	// one per refusal, so a handler that logs twice or not at all both fail here.
	if got := countOutcome(s.accessEvents(t, caseID), "DENIED"); got != 1 {
		t.Fatalf("DENIED access events after one refused sensitive read = %d, want 1", got)
	}

	// Its diagnoses are the same 403 a caller with no clinical grant at all gets, so the
	// refusal itself never says which of the two it was.
	rec = s.do(t, http.MethodGet, "/api/v1/encounters/"+encounterID.String()+"/diagnoses",
		clinicianPermissions, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("diagnoses of a sensitive case without the grant = %d: %s", rec.Code, rec.Body.String())
	}
	if code := decode[problemBody](t, rec).Code; code != "CLINICAL_READ_REQUIRED" {
		t.Fatalf("refusal code = %s, want the same CLINICAL_READ_REQUIRED a non-clinical caller gets", code)
	}

	// 2. With health.sensitive.read but no purpose: 428, and the attempt is recorded.
	rec = s.do(t, http.MethodGet, "/api/v1/health-cases/"+caseID.String(), reviewerPermissions, nil)
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("get sensitive case with no purpose = %d: %s", rec.Code, rec.Body.String())
	}
	if code := decode[problemBody](t, rec).Code; code != "ACCESS_PURPOSE_REQUIRED" {
		t.Fatalf("refusal code = %s, want ACCESS_PURPOSE_REQUIRED", code)
	}
	if got := countOutcome(s.accessEvents(t, caseID), "DENIED"); got != 2 {
		t.Fatalf("DENIED access events after the 428 = %d, want 2 (the earlier refusal and this one)", got)
	}

	// 3. With both and a purpose: the clinical projection, on the record.
	rec = s.do(t, http.MethodGet, "/api/v1/health-cases/"+caseID.String(), reviewerPermissions, nil,
		"X-Access-Purpose", "MEDICAL_REVIEW", "X-Access-Reason", "ön onay değerlendirmesi")
	if rec.Code != http.StatusOK {
		t.Fatalf("get sensitive case with a purpose = %d: %s", rec.Code, rec.Body.String())
	}
	view = decode[caseBody](t, rec)
	if view.Projection != "CLINICAL" {
		t.Fatalf("projection = %s, want CLINICAL", view.Projection)
	}
	if view.Sensitivity == nil || *view.Sensitivity != "SENSITIVE" {
		t.Fatalf("clinical projection sensitivity = %v, want SENSITIVE", view.Sensitivity)
	}
	var success bool
	for _, event := range s.accessEvents(t, caseID) {
		if event.Outcome != "SUCCESS" {
			continue
		}
		success = true
		if event.Purpose == nil || *event.Purpose != "MEDICAL_REVIEW" {
			t.Fatalf("access event purpose = %v, want MEDICAL_REVIEW", event.Purpose)
		}
		if event.PersonID == nil || *event.PersonID != s.person {
			t.Fatalf("access event person = %v, want %s", event.PersonID, s.person)
		}
	}
	if !success {
		t.Fatal("the sensitive read wrote no SUCCESS access event carrying the purpose")
	}

	// 4. An unknown purpose is refused rather than silently recorded as stated.
	rec = s.do(t, http.MethodGet, "/api/v1/health-cases/"+caseID.String(), reviewerPermissions, nil,
		"X-Access-Purpose", "CURIOSITY")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an unknown purpose = %d: %s", rec.Code, rec.Body.String())
	}
}

// TestSensitivityFollowsTheDiagnosisSet: it is derived from what is stored, in both
// directions, and a caller never sends it.
func TestSensitivityFollowsTheDiagnosisSet(t *testing.T) {
	s := newServer(t)
	caseID, encounterID := s.openCase(t, s.codeStrict, "PRIMARY")

	read := func() string {
		t.Helper()
		ctx, cancel := s.h.Ctx()
		defer cancel()
		var sensitivity string
		if err := s.h.Admin.QueryRow(ctx,
			`SELECT sensitivity FROM health.health_case WHERE id = $1`, caseID).Scan(&sensitivity); err != nil {
			t.Fatalf("read sensitivity: %v", err)
		}
		return sensitivity
	}
	if read() != "SENSITIVE" {
		t.Fatal("a sensitive diagnosis did not make the case sensitive")
	}
	// Correcting the code away returns the case to STANDARD: sensitivity is a statement
	// about the diagnoses the case carries now, not a flag that can only ever be set.
	rec := s.do(t, http.MethodPut, "/api/v1/encounters/"+encounterID.String()+"/diagnoses",
		clinicianPermissions, map[string]any{
			"items": []map[string]any{{"codeValueId": s.codePlain, "diagnosisType": "PRIMARY"}},
		})
	if rec.Code != http.StatusOK {
		t.Fatalf("replace diagnoses = %d: %s", rec.Code, rec.Body.String())
	}
	if read() != "STANDARD" {
		t.Fatal("removing the sensitive diagnosis left the case flagged")
	}
	list := decode[diagnosisListBody](t, rec)
	if len(list.Items) != 1 || list.Items[0].Code != plainCode {
		t.Fatalf("the set was not replaced: %+v", list.Items)
	}
}

// TestOnePrimaryDiagnosisIsA422: the database refuses a second primary whatever writes it,
// and the API says which line was the second one rather than letting the constraint surface
// as a 500.
func TestOnePrimaryDiagnosisIsA422(t *testing.T) {
	s := newServer(t)
	_, encounterID := s.openCase(t, s.codePlain, "PRIMARY")

	rec := s.do(t, http.MethodPut, "/api/v1/encounters/"+encounterID.String()+"/diagnoses",
		clinicianPermissions, map[string]any{
			"items": []map[string]any{
				{"codeValueId": s.codePlain, "diagnosisType": "PRIMARY"},
				{"codeValueId": s.codeStrict, "diagnosisType": "PRIMARY"},
			},
		})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("two primary diagnoses = %d: %s", rec.Code, rec.Body.String())
	}
	problem := decode[problemBody](t, rec)
	var named bool
	for _, f := range problem.Errors {
		if f.Field == "items[1].diagnosisType" && f.Code == "DUPLICATE_PRIMARY" {
			named = true
		}
	}
	if !named {
		t.Fatalf("the 422 does not name the second primary: %s", rec.Body.String())
	}
}

// TestCaseClosureNeedsEveryEncounterEnded.
func TestCaseClosureNeedsEveryEncounterEnded(t *testing.T) {
	s := newServer(t)
	rec := s.do(t, http.MethodPost, "/api/v1/health-cases", clinicianPermissions, map[string]any{
		"personId": s.person, "enrollmentId": s.enrollment, "caseType": "OUTPATIENT",
		"providerOrganizationId": s.provider,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create case = %d: %s", rec.Code, rec.Body.String())
	}
	created := decode[caseBody](t, rec)
	etag := rec.Header().Get("ETag")

	// An encounter that has not ended.
	rec = s.do(t, http.MethodPost, "/api/v1/health-cases/"+created.Id.String()+"/encounters",
		clinicianPermissions, map[string]any{
			"encounterType": "INPATIENT", "startedAt": "2026-06-15T09:00:00Z",
		})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create encounter = %d: %s", rec.Code, rec.Body.String())
	}
	var encounter struct {
		Id uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &encounter); err != nil {
		t.Fatalf("decode encounter: %v", err)
	}

	rec = s.do(t, http.MethodPost, "/api/v1/health-cases/"+created.Id.String()+"/close",
		clinicianPermissions, map[string]any{}, "If-Match", etag)
	if rec.Code != http.StatusConflict {
		t.Fatalf("close over an open encounter = %d: %s", rec.Code, rec.Body.String())
	}
	if code := decode[problemBody](t, rec).Code; code != "HEALTH_CASE_ENCOUNTER_OPEN" {
		t.Fatalf("refusal code = %s, want HEALTH_CASE_ENCOUNTER_OPEN", code)
	}

	// End it and the close goes through.
	s.h.AdminExec(`UPDATE health.encounter SET ended_at = started_at + interval '1 hour' WHERE id = $1`,
		encounter.Id)
	rec = s.do(t, http.MethodPost, "/api/v1/health-cases/"+created.Id.String()+"/close",
		clinicianPermissions, map[string]any{"reasonText": "tedavi tamamlandı"}, "If-Match", etag)
	if rec.Code != http.StatusOK {
		t.Fatalf("close = %d: %s", rec.Code, rec.Body.String())
	}
	if closed := decode[caseBody](t, rec); closed.Status != "CLOSED" {
		t.Fatalf("status after close = %s", closed.Status)
	}
	// And a second close is refused rather than silently repeated.
	rec = s.do(t, http.MethodPost, "/api/v1/health-cases/"+created.Id.String()+"/close",
		clinicianPermissions, map[string]any{}, "If-Match", etag)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second close = %d: %s", rec.Code, rec.Body.String())
	}
}

// TestProviderScopeIsA404: a case belonging to another provider is not there, rather than
// there and refused. Which cases exist at all is somebody else's business.
func TestProviderScopeIsA404(t *testing.T) {
	s := newServer(t)
	caseID, encounterID := s.openCase(t, s.codePlain, "PRIMARY")

	// Now bind the caller to the other organization.
	s.scopes = []identity.Scope{{Type: "ORGANIZATION", ID: uuid.NullUUID{UUID: s.otherOrg, Valid: true}}}

	rec := s.do(t, http.MethodGet, "/api/v1/health-cases/"+caseID.String(), clinicianPermissions, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("out-of-scope case = %d: %s", rec.Code, rec.Body.String())
	}
	rec = s.do(t, http.MethodGet, "/api/v1/encounters/"+encounterID.String(), clinicianPermissions, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("out-of-scope encounter = %d: %s", rec.Code, rec.Body.String())
	}
	rec = s.do(t, http.MethodGet, "/api/v1/health-cases", clinicianPermissions, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	if items := decode[casePageBody](t, rec).Items; len(items) != 0 {
		t.Fatalf("an out-of-scope case is on the page: %+v", items)
	}

	// An ORGANIZATION grant that names nothing restricts to nothing, which is the safe
	// reading of it — not "everything", which is how a boundary silently disappears.
	s.scopes = []identity.Scope{{Type: "ORGANIZATION"}}
	rec = s.do(t, http.MethodGet, "/api/v1/health-cases", clinicianPermissions, nil)
	if items := decode[casePageBody](t, rec).Items; len(items) != 0 {
		t.Fatalf("an empty ORGANIZATION grant saw %d cases, want none", len(items))
	}
}

// TestHealthAccessLogListsReadsByPerson: the log answers the member's own question, and the
// financial-projection reads are deliberately absent from it — a read that saw nothing
// clinical is not a clinical access.
func TestHealthAccessLogListsReadsByPerson(t *testing.T) {
	s := newServer(t)
	caseID, _ := s.openCase(t, s.codePlain, "PRIMARY")

	// One clinical read, with its reason.
	if rec := s.do(t, http.MethodGet, "/api/v1/health-cases/"+caseID.String(), clinicianPermissions, nil,
		"X-Access-Purpose", "CLAIM_REVIEW", "X-Access-Reason", "hasar dosyası"); rec.Code != http.StatusOK {
		t.Fatalf("clinical read = %d: %s", rec.Code, rec.Body.String())
	}
	// Several financial ones, which must leave no trace on this log.
	for range 3 {
		if rec := s.do(t, http.MethodGet, "/api/v1/health-cases/"+caseID.String(),
			sponsorHRPermissions, nil); rec.Code != http.StatusOK {
			t.Fatalf("financial read = %d: %s", rec.Code, rec.Body.String())
		}
	}

	rec := s.do(t, http.MethodGet, "/api/v1/health-access-log?personId="+s.person.String(),
		auditorPermissions, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("access log = %d: %s", rec.Code, rec.Body.String())
	}
	log := decode[accessLogBody](t, rec)
	if len(log.Items) == 0 {
		t.Fatal("the access log is empty after a clinical read")
	}
	var withPurpose int
	for _, item := range log.Items {
		if item.PersonId == nil || *item.PersonId != s.person {
			t.Fatalf("the log carries a row for another person: %+v", item)
		}
		if item.Outcome != "SUCCESS" && item.Outcome != "DENIED" {
			t.Fatalf("unexpected outcome %s", item.Outcome)
		}
		if item.PurposeCode != nil && *item.PurposeCode == "CLAIM_REVIEW" {
			withPurpose++
			if item.ReasonText == nil || *item.ReasonText != "hasar dosyası" {
				t.Fatalf("the log lost the reason: %+v", item)
			}
		}
	}
	if withPurpose != 1 {
		t.Fatalf("the log carries %d CLAIM_REVIEW reads, want exactly the one that happened", withPurpose)
	}
	// The three financial reads wrote nothing: a log with one clinical read and one write
	// has exactly those, whatever else was requested in between.
	for _, item := range log.Items {
		if item.AccessType == "VIEW" && item.Outcome == "SUCCESS" && item.PurposeCode == nil &&
			item.ResourceType == "health_case" {
			t.Fatalf("a financial-projection read reached the clinical access log: %+v", item)
		}
	}

	// A caller without audit.read may not read it at all.
	if rec := s.do(t, http.MethodGet, "/api/v1/health-access-log?personId="+s.person.String(),
		sponsorHRPermissions, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("access log without audit.read = %d", rec.Code)
	}
}

// TestCaseCannotBeOpenedFromAnIneligibleRequest: a case is an episode of care, and the
// request it came from has to be one.
func TestCaseCannotBeOpenedFromAnIneligibleRequest(t *testing.T) {
	s := newServer(t)
	// A reimbursement on the same lines is not an episode of care.
	s.h.AdminExec(`UPDATE service.service_request SET request_type = 'REIMBURSEMENT' WHERE id = $1`, s.request)
	rec := s.do(t, http.MethodPost, "/api/v1/health-cases", clinicianPermissions, map[string]any{
		"personId": s.person, "enrollmentId": s.enrollment, "caseType": "OUTPATIENT",
		"providerOrganizationId": s.provider, "serviceRequestId": s.request,
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("case from a reimbursement = %d: %s", rec.Code, rec.Body.String())
	}
	if code := decode[problemBody](t, rec).Code; code != "HEALTH_CASE_REQUEST_NOT_ELIGIBLE" {
		t.Fatalf("refusal code = %s", code)
	}

	// And an enrollment belonging to somebody else is refused too.
	s.h.AdminExec(`UPDATE service.service_request SET request_type = 'DIRECT_SERVICE' WHERE id = $1`, s.request)
	var other uuid.UUID
	ctx, cancel := s.h.Ctx()
	defer cancel()
	if err := s.h.Admin.QueryRow(ctx, `
		INSERT INTO party.person (tenant_id, first_name, last_name, normalized_name)
		VALUES ($1, 'Ece', 'Yıldız', 'ece yıldız') RETURNING id`, s.tenant).Scan(&other); err != nil {
		t.Fatalf("seed second person: %v", err)
	}
	rec = s.do(t, http.MethodPost, "/api/v1/health-cases", clinicianPermissions, map[string]any{
		"personId": other, "enrollmentId": s.enrollment, "caseType": "OUTPATIENT",
		"providerOrganizationId": s.provider,
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("case on somebody else's enrollment = %d: %s", rec.Code, rec.Body.String())
	}
	if code := decode[problemBody](t, rec).Code; code != "HEALTH_CASE_ENROLLMENT_MISMATCH" {
		t.Fatalf("refusal code = %s", code)
	}
}

// TestWritingADiagnosisNeedsTheClinicalGrant: a caller that may not read clinical detail has
// no business writing it, and could not read back what it wrote.
func TestWritingADiagnosisNeedsTheClinicalGrant(t *testing.T) {
	s := newServer(t)
	_, encounterID := s.openCase(t, s.codePlain, "PRIMARY")

	const manageOnly = "health.case.read,health.case.manage"
	rec := s.do(t, http.MethodPut, "/api/v1/encounters/"+encounterID.String()+"/diagnoses",
		manageOnly, map[string]any{
			"items": []map[string]any{{"codeValueId": s.codeStrict, "diagnosisType": "PRIMARY"}},
		})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("put diagnoses without the clinical grant = %d: %s", rec.Code, rec.Body.String())
	}
	rec = s.do(t, http.MethodPost, "/api/v1/health-cases/"+uuid.Nil.String()+"/encounters",
		manageOnly, map[string]any{"encounterType": "OUTPATIENT", "startedAt": "2026-06-15T09:00:00Z"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("create encounter without the clinical grant = %d: %s", rec.Code, rec.Body.String())
	}
}

func countOutcome(events []struct {
	AccessType     string
	Classification string
	Outcome        string
	Purpose        *string
	Reason         *string
	PersonID       *uuid.UUID
}, outcome string,
) int {
	n := 0
	for _, event := range events {
		if event.Outcome == outcome && event.Classification == "HEALTH" {
			n++
		}
	}
	return n
}
