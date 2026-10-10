package healthhttp_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/celikbros/kapsora/internal/health/application"

	"github.com/google/uuid"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/identity"
)

func TestEndEncounterUnblocksCaseClosureAndPreservesClinicalContent(t *testing.T) {
	s := newServer(t)
	caseID, _ := s.openCase(t, s.codePlain, "PRIMARY")
	started := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	created := s.do(t, http.MethodPost, "/api/v1/health-cases/"+caseID.String()+"/encounters", clinicianPermissions,
		map[string]any{"encounterType": "OUTPATIENT", "startedAt": started, "notesClinical": clinicalNote})
	if created.Code != http.StatusCreated {
		t.Fatalf("create=%d: %s", created.Code, created.Body.String())
	}
	encounter := decode[kapsorav1.Encounter](t, created)
	path := "/api/v1/encounters/" + encounter.Id.String() + "/end"
	end := map[string]any{"endedAt": started.Add(time.Minute)}
	etag := etagOf(t, created)
	casePath := "/api/v1/health-cases/" + caseID.String()
	current := s.do(t, http.MethodGet, casePath, clinicianPermissions, nil)
	closed := s.do(t, http.MethodPost, casePath+"/close", clinicianPermissions, nil, "If-Match", etagOf(t, current))
	if closed.Code != 409 || decode[problemBody](t, closed).Code != "HEALTH_CASE_ENCOUNTER_OPEN" {
		t.Fatalf("close=%d", closed.Code)
	}
	for _, perms := range []string{"health.case.read", "health.case.manage", "health.clinical.read"} {
		if r := s.do(t, http.MethodPost, path, perms, end, "If-Match", etag); r.Code != 403 {
			t.Fatalf("permission refusal=%d", r.Code)
		}
	}
	s.scopes = []identity.Scope{{Type: "ORGANIZATION", ID: uuid.NullUUID{UUID: s.otherOrg, Valid: true}}}
	if r := s.do(t, http.MethodPost, path, clinicianPermissions, end, "If-Match", etag); r.Code != 404 {
		t.Fatalf("provider scope=%d", r.Code)
	}
	s.scopes = nil
	tenant := s.tenant
	s.tenant = uuid.New()
	if r := s.do(t, http.MethodPost, path, clinicianPermissions, end, "If-Match", etag); r.Code != 404 {
		t.Fatalf("tenant scope=%d", r.Code)
	}
	s.tenant = tenant
	for _, tc := range []struct {
		body   map[string]any
		etag   string
		status int
	}{
		{end, "", 428}, {end, `"999999"`, 412},
		{map[string]any{"endedAt": started.Add(-time.Second)}, etag, 422},
		{map[string]any{}, etag, 422},
	} {
		if r := s.do(t, http.MethodPost, path, clinicianPermissions, tc.body, "If-Match", tc.etag); r.Code != tc.status {
			t.Fatalf("end=%d want=%d: %s", r.Code, tc.status, r.Body.String())
		}
	}
	done := s.do(t, http.MethodPost, path, clinicianPermissions, end, "If-Match", etag)
	if done.Code != 200 {
		t.Fatalf("end=%d: %s", done.Code, done.Body.String())
	}
	view := decode[kapsorav1.Encounter](t, done)
	if view.EndedAt == nil || view.NotesClinical == nil || *view.NotesClinical != clinicalNote || etagOf(t, done) == etag {
		t.Fatal("end lost content or version")
	}
	if r := s.do(t, http.MethodPost, path, clinicianPermissions, end, "If-Match", etagOf(t, done)); r.Code != 409 || decode[problemBody](t, r).Code != "ENCOUNTER_ALREADY_ENDED" {
		t.Fatalf("second end=%d", r.Code)
	}
	current = s.do(t, http.MethodGet, casePath, clinicianPermissions, nil)
	if r := s.do(t, http.MethodPost, casePath+"/close", clinicianPermissions, nil, "If-Match", etagOf(t, current)); r.Code != 200 {
		t.Fatalf("close after end=%d: %s", r.Code, r.Body.String())
	}
	if r := s.do(t, http.MethodPost, path, clinicianPermissions, end, "If-Match", etagOf(t, done)); r.Code != 409 || decode[problemBody](t, r).Code != "HEALTH_CASE_CLOSED" {
		t.Fatalf("closed case end=%d", r.Code)
	}
	ctx, cancel := s.h.Ctx()
	defer cancel()
	var count int
	if err := s.h.Admin.QueryRow(ctx, `SELECT count(*) FROM audit.event WHERE tenant_id=$1 AND action_code='health_encounter.end' AND resource_id=$2`, tenant, encounter.Id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("end events=%d", count)
	}
}

func TestEndEncounterConcurrentCommandsAndSensitiveProjection(t *testing.T) {
	s := newServer(t)
	caseID, _ := s.openCase(t, s.codeStrict, "PRIMARY")
	started := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	created := s.do(t, http.MethodPost, "/api/v1/health-cases/"+caseID.String()+"/encounters", clinicianPermissions, map[string]any{"encounterType": "OUTPATIENT", "startedAt": started, "notesClinical": clinicalNote})
	if created.Code != 201 {
		t.Fatalf("create=%d", created.Code)
	}
	encounter := decode[kapsorav1.Encounter](t, created)
	ctx, cancel := s.h.Ctx()
	defer cancel()
	rc := reviewerContext(s)
	delete(rc.Permissions, "health.sensitive.read")
	results := make(chan error, 2)
	for range 2 {
		go func() {
			view, err := s.svc.EndEncounter(ctx, rc, encounter.Id, started.Add(time.Minute), encounter.RowVersion)
			if err == nil && (view.Projection != application.ProjectionFinancial || view.Encounter.NotesClinical != nil || view.Encounter.BranchCode != nil) {
				results <- errors.New("end leaked sensitive fields")
				return
			}
			results <- err
		}()
	}
	success, refused := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			success++
		case errors.Is(err, application.ErrEncounterEnded):
			refused++
		default:
			t.Fatalf("end: %v", err)
		}
	}
	if success != 1 || refused != 1 {
		t.Fatalf("success=%d refused=%d", success, refused)
	}
}
