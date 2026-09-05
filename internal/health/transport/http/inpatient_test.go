package healthhttp_test

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/identity"
)

// The clinical strings this file hunts for in a wire body. They are distinctive on purpose: a
// substring scan is only meaningful if nothing else in the document could produce it by
// accident.
const (
	stayReasonText  = "Solunum sıkıntısı devam ettiği için iki gün daha gerekiyor."
	stayReasonCode  = "COMPLICATION"
	stayRoomCode    = "A-214"
	stayAdmissionAt = "2026-06-15T09:00:00Z"
)

type stayBody struct {
	Id                     uuid.UUID  `json:"id"`
	CaseId                 uuid.UUID  `json:"caseId"`
	PersonId               uuid.UUID  `json:"personId"`
	ProviderOrganizationId uuid.UUID  `json:"providerOrganizationId"`
	Status                 string     `json:"status"`
	Projection             string     `json:"projection"`
	ServiceRequestId       uuid.UUID  `json:"serviceRequestId"`
	AuthorizationId        *uuid.UUID `json:"authorizationId"`
	// The one clinical field of the header. It is a pointer so "absent" and "null" are
	// different answers, which is exactly the difference the financial projection turns on.
	AdmissionDiagnosisId *uuid.UUID `json:"admissionDiagnosisId"`
	AuthorizedDays       *string    `json:"authorizedDays"`
	ActualDays           *string    `json:"actualDays"`
	ReleasedDays         *string    `json:"releasedDays"`
	OverAuthorization    bool       `json:"overAuthorization"`
	EstimatedDays        int        `json:"estimatedDays"`
	RowVersion           int64      `json:"rowVersion"`
	Extensions           []struct {
		Id             uuid.UUID `json:"id"`
		SequenceNo     int       `json:"sequenceNo"`
		AdditionalDays int       `json:"additionalDays"`
		ReasonCode     string    `json:"reasonCode"`
		ReasonText     *string   `json:"reasonText"`
		Status         string    `json:"status"`
	} `json:"extensions"`
	Segments []struct {
		Id          uuid.UUID `json:"id"`
		SegmentType string    `json:"segmentType"`
		StartsAt    string    `json:"startsAt"`
		EndsAt      *string   `json:"endsAt"`
		RoomCode    *string   `json:"roomCode"`
	} `json:"segments"`
}

type stayPageBody struct {
	Items []stayBody `json:"items"`
}

// seedStay writes an admitted stay with one approved extension and two segments straight into
// the tables. The admission itself belongs to the application tests, which drive it through
// the real preauthorization gate; what this file is about is what leaves the process.
// seedStay writes an admitted stay with an extension and two segments. It takes the
// encounter so the stay can name a real admission diagnosis: a projection test whose stay
// has no diagnosis asserts that nil is nil and would pass with the clearing removed.
func (s *server) seedStay(t *testing.T, caseID, encounterID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx, cancel := s.h.Ctx()
	defer cancel()

	var stayID uuid.UUID
	err := s.h.Admin.QueryRow(ctx, `
		INSERT INTO health.inpatient_stay (tenant_id, case_id, provider_organization_id,
		                                   admission_at, estimated_days, expected_discharge_at,
		                                   status, service_request_id, authorized_days,
		                                   admission_diagnosis_id)
		VALUES ($1, $2, $3, $4::timestamptz, 4, $4::timestamptz + interval '5 days',
		        'ADMITTED', $5, 5,
		        (SELECT id FROM health.diagnosis
		          WHERE tenant_id = $1 AND encounter_id = $6 LIMIT 1)) RETURNING id`,
		s.tenant, caseID, s.provider, stayAdmissionAt, s.request, encounterID).Scan(&stayID)
	if err != nil {
		t.Fatalf("seed stay: %v", err)
	}
	s.h.AdminExec(`
		INSERT INTO health.stay_extension (tenant_id, stay_id, sequence_no, additional_days,
		                                   reason_code, reason_text, service_request_id, status)
		VALUES ($1, $2, 1, 2, $3, $4, $5, 'APPROVED')`,
		s.tenant, stayID, stayReasonCode, stayReasonText, s.request)
	s.h.AdminExec(`
		INSERT INTO health.stay_segment (tenant_id, stay_id, segment_type, starts_at, ends_at, room_code)
		VALUES ($1, $2, 'WARD', $3::timestamptz, $3::timestamptz + interval '2 days', $4)`,
		s.tenant, stayID, stayAdmissionAt, stayRoomCode)
	s.h.AdminExec(`
		INSERT INTO health.stay_segment (tenant_id, stay_id, segment_type, starts_at)
		VALUES ($1, $2, 'ICU', $3::timestamptz + interval '2 days')`,
		s.tenant, stayID, stayAdmissionAt)
	return stayID
}

// TestStayFinancialProjectionCarriesEveryFigureAndNoClinicalWord is the acceptance criterion of
// WP-I5-01 applied to an admission: the sponsor's HR user may see that somebody is in hospital
// and how many days the plan promised, and may never see why.
//
// The assertion is made on the serialised body rather than field by field, because a clinical
// field added later would slip past a property check and would not slip past this.
func TestStayFinancialProjectionCarriesEveryFigureAndNoClinicalWord(t *testing.T) {
	s := newServer(t)
	caseID, encounterID := s.openCase(t, s.codePlain, "PRIMARY")
	stayID := s.seedStay(t, caseID, encounterID)

	rec := s.do(t, http.MethodGet, "/api/v1/inpatient-stays/"+stayID.String(), sponsorHRPermissions, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get stay as sponsor HR = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, stayReasonText) {
		t.Fatal("the financial projection carried the extension's clinical reason text")
	}
	out := decode[stayBody](t, rec)
	if out.Projection != "FINANCIAL" {
		t.Fatalf("projection = %s, want FINANCIAL", out.Projection)
	}
	if out.AdmissionDiagnosisId != nil {
		t.Fatal("the financial projection carried the admission diagnosis")
	}
	if len(out.Extensions) != 1 || out.Extensions[0].ReasonText != nil {
		t.Fatalf("extensions = %+v, want one with no reason text", out.Extensions)
	}
	// Everything a claims reviewer needs to reconcile a bill survives: the day counts, the
	// reason code, and every segment.
	if out.AuthorizedDays == nil || *out.AuthorizedDays != "5" {
		t.Fatalf("authorizedDays = %v, want 5", out.AuthorizedDays)
	}
	if out.Extensions[0].ReasonCode != stayReasonCode || out.Extensions[0].AdditionalDays != 2 {
		t.Fatalf("extension = %+v, want the code and the day count", out.Extensions[0])
	}
	if len(out.Segments) != 2 {
		t.Fatalf("segments = %d, want 2: a claim is priced from them", len(out.Segments))
	}
	if !strings.Contains(body, stayRoomCode) {
		t.Fatal("the financial projection dropped the room a night is billed for")
	}
	// The ETag is the row version, quoted, because that is what the next command sends back.
	if tag := rec.Header().Get("ETag"); tag != fmt.Sprintf("%q", strconv.FormatInt(out.RowVersion, 10)) {
		t.Fatalf("ETag = %q, want the quoted row version %d", tag, out.RowVersion)
	}
}

// TestStayClinicalProjectionCarriesTheReasonText is the other half: a clinician sees why.
func TestStayClinicalProjectionCarriesTheReasonText(t *testing.T) {
	s := newServer(t)
	caseID, encounterID := s.openCase(t, s.codePlain, "PRIMARY")
	stayID := s.seedStay(t, caseID, encounterID)

	rec := s.do(t, http.MethodGet, "/api/v1/inpatient-stays/"+stayID.String(), clinicianPermissions, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get stay as clinician = %d: %s", rec.Code, rec.Body.String())
	}
	out := decode[stayBody](t, rec)
	if out.Projection != "CLINICAL" {
		t.Fatalf("projection = %s, want CLINICAL", out.Projection)
	}
	if out.Extensions[0].ReasonText == nil || *out.Extensions[0].ReasonText != stayReasonText {
		t.Fatalf("reasonText = %v, want the doctor's own words", out.Extensions[0].ReasonText)
	}
}

// TestStayListIsBoundedByTheProviderScope proves the boundary is applied to the list as well as
// to the single read: a stay another hospital admitted is not there, rather than there and
// hidden.
func TestStayListIsBoundedByTheProviderScope(t *testing.T) {
	s := newServer(t)
	caseID, encounterID := s.openCase(t, s.codePlain, "PRIMARY")
	stayID := s.seedStay(t, caseID, encounterID)

	rec := s.do(t, http.MethodGet, "/api/v1/inpatient-stays", clinicianPermissions, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list stays = %d: %s", rec.Code, rec.Body.String())
	}
	if page := decode[stayPageBody](t, rec); len(page.Items) != 1 || page.Items[0].Id != stayID {
		t.Fatalf("unscoped list = %+v, want the one stay", page.Items)
	}

	s.scopes = []identity.Scope{{Type: "ORGANIZATION", ID: uuid.NullUUID{UUID: s.otherOrg, Valid: true}}}
	rec = s.do(t, http.MethodGet, "/api/v1/inpatient-stays", clinicianPermissions, nil)
	if page := decode[stayPageBody](t, rec); len(page.Items) != 0 {
		t.Fatalf("another provider's list = %+v, want it empty", page.Items)
	}
	rec = s.do(t, http.MethodGet, "/api/v1/inpatient-stays/"+stayID.String(), clinicianPermissions, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("another provider's read = %d, want 404", rec.Code)
	}
	if code := decode[problemBody](t, rec).Code; code != "INPATIENT_STAY_NOT_FOUND" {
		t.Fatalf("code = %s, want INPATIENT_STAY_NOT_FOUND", code)
	}
}

// TestStayCommandsRequireIfMatch is the concurrency contract every state-changing command here
// carries. A command with no If-Match is 428 rather than a write nobody meant.
func TestStayCommandsRequireIfMatch(t *testing.T) {
	s := newServer(t)
	caseID, encounterID := s.openCase(t, s.codePlain, "PRIMARY")
	stayID := s.seedStay(t, caseID, encounterID)
	base := "/api/v1/inpatient-stays/" + stayID.String()

	for _, command := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, base + "/extensions", map[string]any{"additionalDays": 1, "reasonCode": stayReasonCode}},
		{http.MethodPut, base + "/segments", map[string]any{"items": []any{}}},
		{http.MethodPost, base + "/discharge", map[string]any{}},
		{http.MethodPost, base + "/cancel", map[string]any{"reasonCode": "ADMISSION_NOT_NEEDED"}},
	} {
		rec := s.do(t, command.method, command.path, clinicianPermissions, command.body)
		if rec.Code != http.StatusPreconditionRequired {
			t.Errorf("%s %s without If-Match = %d, want 428", command.method, command.path, rec.Code)
		}
		if code := decode[problemBody](t, rec).Code; code != "IF_MATCH_REQUIRED" {
			t.Errorf("%s %s code = %s, want IF_MATCH_REQUIRED", command.method, command.path, code)
		}
	}
}

// TestStayReconciliationRefusesALiveStay is section 2.4's read: a reconciliation is what the
// settlement was, and a stay still running has not settled anything.
func TestStayReconciliationRefusesALiveStay(t *testing.T) {
	s := newServer(t)
	caseID, encounterID := s.openCase(t, s.codePlain, "PRIMARY")
	stayID := s.seedStay(t, caseID, encounterID)

	rec := s.do(t, http.MethodGet,
		"/api/v1/inpatient-stays/"+stayID.String()+"/reconciliation", clinicianPermissions, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("reconciliation of a live stay = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if code := decode[problemBody](t, rec).Code; code != "INPATIENT_STAY_NOT_DISCHARGED" {
		t.Fatalf("code = %s, want INPATIENT_STAY_NOT_DISCHARGED", code)
	}
}

// TestStayCommandsNeedTheManageGrant is section 2.5: reading a stay and moving one are two
// grants, and the read grant alone moves nothing.
func TestStayCommandsNeedTheManageGrant(t *testing.T) {
	s := newServer(t)
	caseID, encounterID := s.openCase(t, s.codePlain, "PRIMARY")
	stayID := s.seedStay(t, caseID, encounterID)

	rec := s.do(t, http.MethodPost, "/api/v1/inpatient-stays/"+stayID.String()+"/cancel",
		sponsorHRPermissions, map[string]any{"reasonCode": "ADMISSION_NOT_NEEDED"},
		"If-Match", `"1"`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cancel without health.case.manage = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// TestPutStaySegmentsRefusesAnOverlapAndAcceptsACompanion is the exclusion constraint as the
// wire meets it, and the one hole in it that has a name.
func TestPutStaySegmentsRefusesAnOverlapAndAcceptsACompanion(t *testing.T) {
	s := newServer(t)
	caseID, encounterID := s.openCase(t, s.codePlain, "PRIMARY")
	stayID := s.seedStay(t, caseID, encounterID)
	path := "/api/v1/inpatient-stays/" + stayID.String() + "/segments"
	etag := s.stayETag(t, stayID)

	rec := s.do(t, http.MethodPut, path, clinicianPermissions, map[string]any{
		"items": []any{
			map[string]any{"segmentType": "WARD", "startsAt": stayAdmissionAt, "endsAt": "2026-06-17T09:00:00Z"},
			map[string]any{"segmentType": "ICU", "startsAt": "2026-06-16T09:00:00Z"},
		},
	}, "If-Match", etag)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("overlapping segments = %d, want 422: %s", rec.Code, rec.Body.String())
	}

	rec = s.do(t, http.MethodPut, path, clinicianPermissions, map[string]any{
		"items": []any{
			map[string]any{"segmentType": "WARD", "startsAt": stayAdmissionAt, "endsAt": "2026-06-17T09:00:00Z"},
			map[string]any{"segmentType": "COMPANION", "startsAt": stayAdmissionAt, "endsAt": "2026-06-17T09:00:00Z"},
		},
	}, "If-Match", etag)
	if rec.Code != http.StatusOK {
		t.Fatalf("a companion over the patient's own segment = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if out := decode[stayBody](t, rec); len(out.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(out.Segments))
	}
}

// stayETag reads the ETag the next command has to send back, from the header rather than from
// the body: If-Match is what a real client would use, and a test that took the row version out
// of the JSON would not notice the header going missing.
func (s *server) stayETag(t *testing.T, stayID uuid.UUID) string {
	t.Helper()
	rec := s.do(t, http.MethodGet, "/api/v1/inpatient-stays/"+stayID.String(), clinicianPermissions, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read stay for its ETag = %d: %s", rec.Code, rec.Body.String())
	}
	return etagOf(t, rec)
}
