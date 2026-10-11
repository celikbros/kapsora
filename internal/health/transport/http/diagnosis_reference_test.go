package healthhttp_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// A stay holds the diagnosis ID as clinical history. Replacing the set must fail
// atomically, while an encounter without such a reference remains editable.
func TestReferencedAdmissionDiagnosisReplacementConflictsWithoutChangingHistory(t *testing.T) {
	s := newServer(t)
	caseID, encounterID := s.openCase(t, s.codeStrict, "PRIMARY")
	stayID := s.seedStay(t, caseID, encounterID)

	ctx, cancel := s.h.Ctx()
	defer cancel()
	var originalID uuid.UUID
	if err := s.h.Admin.QueryRow(ctx, `SELECT id FROM health.diagnosis WHERE tenant_id = $1 AND encounter_id = $2`, s.tenant, encounterID).Scan(&originalID); err != nil {
		t.Fatalf("read original diagnosis: %v", err)
	}

	path := "/api/v1/encounters/" + encounterID.String() + "/diagnoses"
	rec := s.do(t, http.MethodPut, path, clinicianPermissions, map[string]any{
		"items": []map[string]any{{"codeValueId": s.codePlain, "diagnosisType": "PRIMARY"}},
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("replace referenced diagnosis = %d: %s", rec.Code, rec.Body.String())
	}
	var conflict struct {
		Code   string `json:"code"`
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &conflict); err != nil {
		t.Fatalf("decode conflict: %v", err)
	}
	if conflict.Code != "DIAGNOSIS_IN_USE" || !strings.Contains(conflict.Detail, "klinik kaydı düzeltin") {
		t.Fatalf("conflict lacks stable code or correction guidance: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "fk_inpatient") || strings.Contains(rec.Body.String(), "health.diagnosis") {
		t.Fatalf("conflict exposes database details: %s", rec.Body.String())
	}

	var diagnosisID, stayDiagnosisID uuid.UUID
	var sensitivity, code string
	if err := s.h.Admin.QueryRow(ctx, `
        SELECT d.id, c.sensitivity, v.code
        FROM health.diagnosis d
        JOIN health.health_case c ON c.tenant_id = d.tenant_id AND c.id = $2
        JOIN catalog.code_value v ON v.tenant_id = d.tenant_id AND v.id = d.code_value_id
        WHERE d.tenant_id = $1 AND d.encounter_id = $3`, s.tenant, caseID, encounterID).Scan(&diagnosisID, &sensitivity, &code); err != nil {
		t.Fatalf("read diagnosis after conflict: %v", err)
	}
	if diagnosisID != originalID || sensitivity != "SENSITIVE" || code != sensitiveCode {
		t.Fatalf("conflict changed diagnosis or sensitivity: id=%s sensitivity=%s code=%s", diagnosisID, sensitivity, code)
	}
	if err := s.h.Admin.QueryRow(ctx, `SELECT admission_diagnosis_id FROM health.inpatient_stay WHERE tenant_id = $1 AND id = $2`, s.tenant, stayID).Scan(&stayDiagnosisID); err != nil {
		t.Fatalf("read stay after conflict: %v", err)
	}
	if stayDiagnosisID != originalID {
		t.Fatalf("stay diagnosis = %s, want %s", stayDiagnosisID, originalID)
	}

	rec = s.do(t, http.MethodPost, "/api/v1/health-cases/"+caseID.String()+"/encounters", clinicianPermissions, map[string]any{
		"encounterType": "OUTPATIENT", "startedAt": "2026-06-16T09:00:00Z",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create unrelated encounter = %d: %s", rec.Code, rec.Body.String())
	}
	var editable struct {
		Id uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &editable); err != nil {
		t.Fatalf("decode editable encounter: %v", err)
	}
	editablePath := "/api/v1/encounters/" + editable.Id.String() + "/diagnoses"
	for _, codeID := range []uuid.UUID{s.codePlain, s.codeStrict} {
		rec = s.do(t, http.MethodPut, editablePath, clinicianPermissions, map[string]any{
			"items": []map[string]any{{"codeValueId": codeID, "diagnosisType": "PRIMARY"}},
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("replace unreferenced diagnosis with %s = %d: %s", codeID, rec.Code, rec.Body.String())
		}
	}
}
