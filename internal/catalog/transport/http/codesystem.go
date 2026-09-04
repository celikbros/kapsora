package cataloghttp

import (
	"encoding/json"
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/catalog/application"
	"github.com/celikbros/kapsora/internal/catalog/domain"
)

// emptyAttributes is what a code value carries when the caller sent no attributes.
var emptyAttributes = []byte(`{}`)

// ListCodeSystems implements listCodeSystems.
func (h *Handler) ListCodeSystems(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	q := r.URL.Query()
	page, err := h.svc.ListCodeSystems(r.Context(), rc, application.ListFilter{
		Query: q.Get("q"), Cursor: q.Get("cursor"), Limit: queryLimit(r),
		Authority: q.Get("authority"), Status: q.Get("status"),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.CodeSystemPage{Items: make([]kapsorav1.CodeSystem, 0, len(page.Items))}
	for _, s := range page.Items {
		out.Items = append(out.Items, codeSystemView(s))
	}
	if page.NextCursor != "" {
		next := page.NextCursor
		out.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, out)
}

// GetCodeSystem implements getCodeSystem. A caller needs this to learn the ETag before
// it can patch the system.
func (h *Handler) GetCodeSystem(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "codeSystemId", application.ErrCodeSystemNotFound)
	if !ok {
		return
	}
	system, err := h.svc.GetCodeSystem(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(system.RowVersion))
	writeJSON(w, http.StatusOK, codeSystemView(system))
}

// CreateCodeSystem implements createCodeSystem.
func (h *Handler) CreateCodeSystem(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	var body kapsorav1.CreateCodeSystemRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in := domain.NewCodeSystem{
		Code: body.Code, Name: body.Name, Version: body.Version,
		Authority: string(body.Authority), ValidFrom: domain.DateOnly(body.ValidFrom.Time),
	}
	if body.Licensed != nil {
		in.Licensed = *body.Licensed
	}
	if body.ValidTo != nil {
		to := domain.DateOnly(body.ValidTo.Time)
		in.ValidTo = &to
	}

	system, err := h.svc.CreateCodeSystem(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(system.RowVersion))
	w.Header().Set("Location", "/api/v1/code-systems/"+system.ID.String())
	writeJSON(w, http.StatusCreated, codeSystemView(system))
}

// PatchCodeSystem implements patchCodeSystem (merge-patch with If-Match).
func (h *Handler) PatchCodeSystem(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "codeSystemId", application.ErrCodeSystemNotFound)
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

	patch := domain.CodeSystemPatch{ExpectedVersion: expected}
	var fields []domain.FieldError
	for key, value := range raw {
		switch key {
		case "name":
			patch.Name = decodeString(value, key, &fields)
		case "authority":
			patch.Authority = decodeString(value, key, &fields)
		case "licensed":
			patch.Licensed = decodeBool(value, key, &fields)
		case "status":
			patch.Status = decodeString(value, key, &fields)
		case "validTo":
			if isJSONNull(value) {
				patch.ClearValidTo = true
			} else {
				patch.ValidTo = decodeDate(value, key, &fields)
			}
		case "code", "version":
			// Code and version identify the edition every value and mapping hangs off.
			immutable(key, &fields)
		default:
			fields = append(fields, domain.FieldError{Field: key, Code: "UNKNOWN_FIELD", Message: "bilinmeyen alan"})
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	system, err := h.svc.UpdateCodeSystem(r.Context(), rc, id, patch)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(system.RowVersion))
	writeJSON(w, http.StatusOK, codeSystemView(system))
}

// ListCodeValues implements listCodeValues; the values are always resolved as of a date.
func (h *Handler) ListCodeValues(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "codeSystemId", application.ErrCodeSystemNotFound)
	if !ok {
		return
	}
	var fields []domain.FieldError
	filter := application.CodeValueFilter{
		AsOf:   queryDate(r, "asOf", &fields),
		Code:   r.URL.Query().Get("code"),
		Query:  r.URL.Query().Get("q"),
		Cursor: r.URL.Query().Get("cursor"),
		Limit:  queryLimit(r),
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	page, err := h.svc.ListCodeValues(r.Context(), rc, id, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.CodeValuePage{
		AsOf:  openapi_types.Date{Time: page.AsOf},
		Items: make([]kapsorav1.CodeValue, 0, len(page.Items)),
	}
	for _, v := range page.Items {
		out.Items = append(out.Items, codeValueView(v))
	}
	if page.NextCursor != "" {
		next := page.NextCursor
		out.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, out)
}

// ImportCodeValues implements importCodeValues. The batch is decoded whole because the
// call is all-or-nothing: nothing is written until every row has passed validation.
func (h *Handler) ImportCodeValues(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "codeSystemId", application.ErrCodeSystemNotFound)
	if !ok {
		return
	}
	var body kapsorav1.ImportCodeValuesRequest
	if !decodeJSONLimit(w, r, &body, maxImportBodyBytes) {
		return
	}

	items := make([]domain.CodeValueInput, 0, len(body.Items))
	var fields []domain.FieldError
	for i, row := range body.Items {
		in := domain.CodeValueInput{
			Code: row.Code, Display: row.Display, ValidFrom: domain.DateOnly(row.ValidFrom.Time),
			Active: true, Attributes: emptyAttributes,
		}
		if row.ParentCode != nil {
			in.ParentCode = *row.ParentCode
		}
		if row.ValidTo != nil {
			to := domain.DateOnly(row.ValidTo.Time)
			in.ValidTo = &to
		}
		if row.Active != nil {
			in.Active = *row.Active
		}
		if row.Attributes != nil {
			encoded, err := json.Marshal(*row.Attributes)
			if err != nil {
				fields = append(fields, domain.FieldError{
					Field: fieldPath(i, "attributes"), Code: "TYPE", Message: "JSON nesnesi olmalı",
				})
				continue
			}
			in.Attributes = encoded
		}
		items = append(items, in)
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	summary, err := h.svc.ImportCodeValues(r.Context(), rc, id, items)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, kapsorav1.CodeValueImportResult{
		Created: summary.Created, Updated: summary.Updated, Skipped: summary.Skipped,
		Errors: []kapsorav1.CodeValueImportError{},
	})
}

func codeSystemView(s application.CodeSystemRecord) kapsorav1.CodeSystem {
	out := kapsorav1.CodeSystem{
		Id: s.ID, Code: s.Code, Name: s.Name, Version: s.Version,
		Authority: kapsorav1.CodeSystemAuthority(s.Authority), Licensed: s.Licensed,
		Status: kapsorav1.CodeSystemStatus(s.Status), ValidFrom: openapi_types.Date{Time: s.ValidFrom},
		RowVersion: int(s.RowVersion),
	}
	if s.ValidTo != nil {
		out.ValidTo = &openapi_types.Date{Time: *s.ValidTo}
	}
	return out
}

func codeValueView(v application.CodeValueRecord) kapsorav1.CodeValue {
	out := kapsorav1.CodeValue{
		Id: v.ID, CodeSystemId: v.CodeSystemID, Code: v.Code, Display: v.Display,
		ParentCode: v.ParentCode, ValidFrom: openapi_types.Date{Time: v.ValidFrom}, Active: v.Active,
		Attributes: map[string]interface{}{},
	}
	if v.ValidTo != nil {
		out.ValidTo = &openapi_types.Date{Time: *v.ValidTo}
	}
	if len(v.Attributes) > 0 {
		_ = json.Unmarshal(v.Attributes, &out.Attributes)
	}
	return out
}
