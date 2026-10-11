package healthhttp_test

import (
	"net/http"
	"testing"
	"time"
)

func TestCaseAndEncounterTenantBoundaryHidesReadsAndWrites(t *testing.T) {
	s := newServer(t)
	caseID, encounterID := s.openCase(t, s.codePlain, "PRIMARY")
	casePath := "/api/v1/health-cases/" + caseID.String()
	encounterPath := "/api/v1/encounters/" + encounterID.String()
	original := s.do(t, http.MethodGet, casePath, clinicianPermissions, nil)
	diagnoses := s.do(t, http.MethodGet, encounterPath+"/diagnoses", clinicianPermissions, nil)
	tenant, membership := s.tenant, s.membership
	s.tenant = s.h.CreateTenant("OTHER_HEALTH_SCOPE")
	s.membership = s.h.CreateMembership(s.tenant, s.actor)
	for _, path := range []string{casePath, encounterPath, encounterPath + "/diagnoses"} {
		if r := s.do(t, http.MethodGet, path, clinicianPermissions, nil); r.Code != 404 {
			t.Fatalf("foreign read = %d", r.Code)
		}
	}
	list := s.do(t, http.MethodGet, "/api/v1/health-cases", clinicianPermissions, nil)
	if list.Code != 200 || len(decode[casePageBody](t, list).Items) != 0 {
		t.Fatal("foreign case visible in list")
	}
	for _, cmd := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, casePath + "/close", map[string]any{}},
		{http.MethodPost, casePath + "/encounters", map[string]any{"encounterType": "OUTPATIENT", "startedAt": time.Now().UTC()}},
		{http.MethodPut, encounterPath + "/diagnoses", map[string]any{"items": []any{}}},
		{http.MethodPost, encounterPath + "/end", map[string]any{"endedAt": time.Now().UTC()}},
	} {
		if r := s.do(t, cmd.method, cmd.path, clinicianPermissions, cmd.body, "If-Match", `"1"`); r.Code != 404 {
			t.Fatalf("foreign %s %s = %d", cmd.method, cmd.path, r.Code)
		}
	}
	s.tenant, s.membership = tenant, membership
	current := s.do(t, http.MethodGet, casePath, clinicianPermissions, nil)
	if current.Body.String() != original.Body.String() || current.Header().Get("ETag") != original.Header().Get("ETag") {
		t.Fatal("foreign commands changed source case")
	}
	current = s.do(t, http.MethodGet, encounterPath+"/diagnoses", clinicianPermissions, nil)
	if current.Body.String() != diagnoses.Body.String() {
		t.Fatal("foreign commands changed diagnoses")
	}
}
