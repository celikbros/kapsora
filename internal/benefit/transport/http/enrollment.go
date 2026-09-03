package benefithttp

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/benefit/domain"
)

// ListPersonEnrollments implements listPersonEnrollments.
func (h *Handler) ListPersonEnrollments(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionProgramRead)
	if !ok {
		return
	}
	personID, ok := h.pathUUID(w, r, "personId", application.ErrNotFound)
	if !ok {
		return
	}
	items, err := h.svc.ListPersonEnrollments(r.Context(), rc, personID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.ListPersonEnrollments200JSONResponse{Items: make([]kapsorav1.Enrollment, 0, len(items))}
	for _, e := range items {
		out.Items = append(out.Items, enrollmentView(e))
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateEnrollment implements createEnrollment.
func (h *Handler) CreateEnrollment(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionEnrollManage)
	if !ok {
		return
	}
	personID, ok := h.pathUUID(w, r, "personId", application.ErrNotFound)
	if !ok {
		return
	}
	var body kapsorav1.CreateEnrollmentRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.NewEnrollmentInput{
		SponsorMembershipID: body.SponsorMembershipId, PlanID: body.PlanId,
		ValidFrom: dateOnly(body.ValidFrom.Time), EnrollmentReason: body.EnrollmentReason,
	}
	if body.Status != nil {
		in.Status = string(*body.Status)
	}
	if body.ValidTo != nil {
		to := dateOnly(body.ValidTo.Time)
		in.ValidTo = &to
	}

	enrollment, err := h.svc.CreateEnrollment(r.Context(), rc, personID, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(enrollment.RowVersion))
	writeJSON(w, http.StatusCreated, enrollmentView(enrollment))
}

// ListEnrollments implements listEnrollments.
func (h *Handler) ListEnrollments(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionProgramRead)
	if !ok {
		return
	}
	q := r.URL.Query()
	filter := application.EnrollmentFilter{Status: q.Get("status"), Cursor: q.Get("cursor"), Limit: queryLimit(r)}
	if raw := q.Get("planId"); raw != "" {
		planID, err := uuid.Parse(raw)
		if err != nil {
			writeValidation(w, r, []domain.FieldError{{Field: "planId", Code: "FORMAT", Message: "geçersiz kimlik"}})
			return
		}
		filter.PlanID = planID
	}
	page, err := h.svc.ListEnrollments(r.Context(), rc, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.EnrollmentPage{Items: make([]kapsorav1.Enrollment, 0, len(page.Items))}
	for _, e := range page.Items {
		out.Items = append(out.Items, enrollmentView(e))
	}
	if page.NextCursor != "" {
		next := page.NextCursor
		out.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, out)
}

// GetEnrollment implements getEnrollment.
func (h *Handler) GetEnrollment(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionProgramRead)
	if !ok {
		return
	}
	enrollmentID, ok := h.pathUUID(w, r, "enrollmentId", application.ErrEnrollmentNotFound)
	if !ok {
		return
	}
	enrollment, err := h.svc.GetEnrollment(r.Context(), rc, enrollmentID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(enrollment.RowVersion))
	writeJSON(w, http.StatusOK, enrollmentView(enrollment))
}

// UpdateEnrollment implements updateEnrollment (merge-patch with If-Match).
func (h *Handler) UpdateEnrollment(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionEnrollManage)
	if !ok {
		return
	}
	enrollmentID, ok := h.pathUUID(w, r, "enrollmentId", application.ErrEnrollmentNotFound)
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

	patch := application.EnrollmentPatch{ExpectedVersion: expected}
	var fields []domain.FieldError
	for key, value := range raw {
		switch key {
		case "status":
			patch.Status = decodeString(value, key, &fields)
		case "validTo":
			if isJSONNull(value) {
				patch.ClearValidTo = true
			} else {
				patch.ValidTo = decodeDate(value, key, &fields)
			}
		default:
			fields = append(fields, domain.FieldError{Field: key, Code: "UNKNOWN_FIELD", Message: "bilinmeyen alan"})
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	enrollment, err := h.svc.UpdateEnrollment(r.Context(), rc, enrollmentID, patch)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(enrollment.RowVersion))
	writeJSON(w, http.StatusOK, enrollmentView(enrollment))
}

func enrollmentView(e application.Enrollment) kapsorav1.Enrollment {
	programID := e.ProgramID
	out := kapsorav1.Enrollment{
		Id: e.ID, PersonId: e.PersonID, SponsorMembershipId: e.SponsorMembershipID,
		PlanId: e.PlanID, PlanCode: e.PlanCode, ProgramId: &programID,
		Status:       kapsorav1.EnrollmentStatus(e.Status),
		ValidFrom:    openapi_types.Date{Time: e.ValidFrom},
		SourceSystem: e.SourceSystem, EnrollmentReason: e.EnrollmentReason,
		RowVersion: int(e.RowVersion),
	}
	if e.ValidTo != nil {
		out.ValidTo = &openapi_types.Date{Time: *e.ValidTo}
	}
	return out
}
