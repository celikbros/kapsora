package claimhttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/celikbros/kapsora/internal/claim/application"
)

func TestInpatientClaimUsesCompatibleHealthCaseProvenance(t *testing.T) {
	caseID, stayID := uuid.New(), uuid.New()
	kind := "INPATIENT_STAY"
	original := application.ClaimView{Claim: application.ClaimRecord{
		ID: uuid.New(), CaseID: &caseID, SourceType: &kind, SourceID: &stayID,
	}}
	body := claimView(original)
	if body.SourceType == nil || string(*body.SourceType) != "HEALTH_CASE" ||
		body.SourceId == nil || *body.SourceId != caseID {
		t.Fatalf("API v1 source is not the health case: %+v", body)
	}
	if *original.Claim.SourceType != "INPATIENT_STAY" || *original.Claim.SourceID != stayID {
		t.Fatal("wire projection changed internal stay provenance")
	}
}

func TestInpatientMissingAllocationReturnsSpecificConflict(t *testing.T) {
	h := &Handler{}
	response := httptest.NewRecorder()
	h.writeError(response, httptest.NewRequest(http.MethodGet, "/", nil), application.ErrInpatientAllocationMissing)
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusConflict || body.Code != "CLAIM_INPATIENT_ALLOCATION_MISSING" {
		t.Fatalf("status=%d code=%s", response.Code, body.Code)
	}
}
