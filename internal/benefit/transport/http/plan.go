package benefithttp

import (
	"encoding/json"
	"net/http"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/benefit/domain"
)

// ListPlans implements listPlans.
func (h *Handler) ListPlans(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionProgramRead)
	if !ok {
		return
	}
	programID, ok := h.pathUUID(w, r, "programId", application.ErrProgramNotFound)
	if !ok {
		return
	}
	plans, err := h.svc.ListPlans(r.Context(), rc, programID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.ListPlans200JSONResponse{Items: make([]kapsorav1.Plan, 0, len(plans))}
	for _, p := range plans {
		out.Items = append(out.Items, planView(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// CreatePlan implements createPlan.
func (h *Handler) CreatePlan(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionPlanManage)
	if !ok {
		return
	}
	programID, ok := h.pathUUID(w, r, "programId", application.ErrProgramNotFound)
	if !ok {
		return
	}
	var body kapsorav1.CreatePlanRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	plan, err := h.svc.CreatePlan(r.Context(), rc, programID, application.NewPlanInput{Code: body.Code, Name: body.Name})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(plan.RowVersion))
	writeJSON(w, http.StatusCreated, planView(plan))
}

// GetPlan implements getPlan.
func (h *Handler) GetPlan(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionProgramRead)
	if !ok {
		return
	}
	planID, ok := h.pathUUID(w, r, "planId", application.ErrPlanNotFound)
	if !ok {
		return
	}
	plan, err := h.svc.GetPlan(r.Context(), rc, planID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(plan.RowVersion))
	writeJSON(w, http.StatusOK, planView(plan))
}

// UpdatePlan implements updatePlan (merge-patch with If-Match).
func (h *Handler) UpdatePlan(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionPlanManage)
	if !ok {
		return
	}
	planID, ok := h.pathUUID(w, r, "planId", application.ErrPlanNotFound)
	if !ok {
		return
	}
	if !requireMergePatch(w, r) {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var raw map[string]json.RawMessage
	if !decodeJSON(w, r, &raw) {
		return
	}

	patch := application.PlanPatch{ExpectedVersion: expected}
	var fields []domain.FieldError
	for key, value := range raw {
		switch key {
		case "name":
			patch.Name = decodeString(value, key, &fields)
		case "status":
			patch.Status = decodeString(value, key, &fields)
		default:
			fields = append(fields, domain.FieldError{Field: key, Code: "UNKNOWN_FIELD", Message: "bilinmeyen alan"})
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	plan, err := h.svc.UpdatePlan(r.Context(), rc, planID, patch)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(plan.RowVersion))
	writeJSON(w, http.StatusOK, planView(plan))
}

func planView(p application.Plan) kapsorav1.Plan {
	out := kapsorav1.Plan{
		Id: p.ID, ProgramId: p.ProgramID, Code: p.Code, Name: p.Name,
		Status: kapsorav1.PlanStatus(p.Status), RowVersion: int(p.RowVersion),
		Versions: make([]kapsorav1.PlanVersionSummary, 0, len(p.Versions)),
	}
	for _, v := range p.Versions {
		out.Versions = append(out.Versions, versionSummaryView(v))
	}
	return out
}
