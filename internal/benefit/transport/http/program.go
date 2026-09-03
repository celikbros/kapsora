package benefithttp

import (
	"encoding/json"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/benefit/application"
	"github.com/celikbros/kapsora/internal/benefit/domain"
)

// ListPrograms implements listPrograms.
func (h *Handler) ListPrograms(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionProgramRead)
	if !ok {
		return
	}
	q := r.URL.Query()
	page, err := h.svc.ListPrograms(r.Context(), rc, application.ProgramFilter{
		Query: q.Get("q"), Status: q.Get("status"), Cursor: q.Get("cursor"), Limit: queryLimit(r),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.ProgramPage{Items: make([]kapsorav1.Program, 0, len(page.Items))}
	for _, p := range page.Items {
		out.Items = append(out.Items, programView(p))
	}
	if page.NextCursor != "" {
		next := page.NextCursor
		out.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateProgram implements createProgram.
func (h *Handler) CreateProgram(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionProgramManage)
	if !ok {
		return
	}
	var body kapsorav1.CreateProgramRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.NewProgramInput{
		Code: body.Code, Name: body.Name, ProgramType: body.ProgramType,
		SponsorOrganizationID: body.SponsorOrganizationId, PayerOrganizationID: body.PayerOrganizationId,
	}
	if body.ValidFrom != nil {
		from := dateOnly(body.ValidFrom.Time)
		in.ValidFrom = &from
	}
	if body.ValidTo != nil {
		to := dateOnly(body.ValidTo.Time)
		in.ValidTo = &to
	}

	program, err := h.svc.CreateProgram(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(program.RowVersion))
	writeJSON(w, http.StatusCreated, programView(program))
}

// GetProgram implements getProgram.
func (h *Handler) GetProgram(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionProgramRead)
	if !ok {
		return
	}
	programID, ok := h.pathUUID(w, r, "programId", application.ErrProgramNotFound)
	if !ok {
		return
	}
	program, err := h.svc.GetProgram(r.Context(), rc, programID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(program.RowVersion))
	writeJSON(w, http.StatusOK, programView(program))
}

// UpdateProgram implements updateProgram (merge-patch with If-Match).
func (h *Handler) UpdateProgram(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionProgramManage)
	if !ok {
		return
	}
	programID, ok := h.pathUUID(w, r, "programId", application.ErrProgramNotFound)
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

	patch := application.ProgramPatch{ExpectedVersion: expected}
	var fields []domain.FieldError
	for key, value := range raw {
		switch key {
		case "name":
			patch.Name = decodeString(value, key, &fields)
		case "status":
			patch.Status = decodeString(value, key, &fields)
		case "validFrom":
			if isJSONNull(value) {
				patch.ClearValidFrom = true
			} else {
				patch.ValidFrom = decodeDate(value, key, &fields)
			}
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

	program, err := h.svc.UpdateProgram(r.Context(), rc, programID, patch)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(program.RowVersion))
	writeJSON(w, http.StatusOK, programView(program))
}

func programView(p application.Program) kapsorav1.Program {
	sponsor, payer := p.SponsorDisplayName, p.PayerDisplayName
	count := p.PlanCount
	out := kapsorav1.Program{
		Id: p.ID, Code: p.Code, Name: p.Name, ProgramType: p.ProgramType,
		Status:                kapsorav1.ProgramStatus(p.Status),
		SponsorOrganizationId: p.SponsorOrganizationID, PayerOrganizationId: p.PayerOrganizationID,
		SponsorDisplayName: &sponsor, PayerDisplayName: &payer,
		PlanCount: &count, RowVersion: int(p.RowVersion),
	}
	if p.ValidFrom != nil {
		out.ValidFrom = &openapi_types.Date{Time: *p.ValidFrom}
	}
	if p.ValidTo != nil {
		out.ValidTo = &openapi_types.Date{Time: *p.ValidTo}
	}
	return out
}
