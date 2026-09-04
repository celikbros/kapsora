package cataloghttp

import (
	"encoding/json"
	"net/http"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/catalog/application"
	"github.com/celikbros/kapsora/internal/catalog/domain"
)

// ListServiceDefinitions implements listServiceDefinitions.
func (h *Handler) ListServiceDefinitions(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	var fields []domain.FieldError
	filter := application.ListFilter{
		Query:      r.URL.Query().Get("q"),
		Cursor:     r.URL.Query().Get("cursor"),
		Limit:      queryLimit(r),
		Domain:     r.URL.Query().Get("domain"),
		Active:     queryBool(r, "active", &fields),
		CategoryID: queryUUID(r, "categoryId", &fields),
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	page, err := h.svc.ListDefinitions(r.Context(), rc, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.ServiceDefinitionPage{Items: make([]kapsorav1.ServiceDefinition, 0, len(page.Items))}
	for _, d := range page.Items {
		out.Items = append(out.Items, definitionView(d))
	}
	if page.NextCursor != "" {
		next := page.NextCursor
		out.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateServiceDefinition implements createServiceDefinition.
func (h *Handler) CreateServiceDefinition(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	var body kapsorav1.CreateServiceDefinitionRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in := domain.NewDefinition{
		CategoryID: body.CategoryId.String(), Code: body.Code, Name: body.Name,
		FulfillmentMode: string(body.FulfillmentMode), DefaultUnitType: string(body.DefaultUnitType),
		RequiresProvider: true, Active: true,
	}
	if body.Description != nil {
		in.Description = *body.Description
	}
	if body.RequiresProvider != nil {
		in.RequiresProvider = *body.RequiresProvider
	}
	if body.Active != nil {
		in.Active = *body.Active
	}

	definition, err := h.svc.CreateDefinition(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(definition.RowVersion))
	w.Header().Set("Location", "/api/v1/service-definitions/"+definition.ID.String())
	writeJSON(w, http.StatusCreated, definitionView(definition))
}

// GetServiceDefinition implements getServiceDefinition.
func (h *Handler) GetServiceDefinition(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "definitionId", application.ErrDefinitionNotFound)
	if !ok {
		return
	}
	definition, err := h.svc.GetDefinition(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(definition.RowVersion))
	writeJSON(w, http.StatusOK, definitionView(definition))
}

// PatchServiceDefinition implements patchServiceDefinition (merge-patch with If-Match).
func (h *Handler) PatchServiceDefinition(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "definitionId", application.ErrDefinitionNotFound)
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

	patch := domain.DefinitionPatch{ExpectedVersion: expected}
	var fields []domain.FieldError
	for key, value := range raw {
		switch key {
		case "categoryId":
			patch.CategoryID = decodeString(value, key, &fields)
		case "name":
			patch.Name = decodeString(value, key, &fields)
		case "description":
			if isJSONNull(value) {
				patch.ClearDescription = true
			} else {
				patch.Description = decodeString(value, key, &fields)
			}
		case "fulfillmentMode":
			patch.FulfillmentMode = decodeString(value, key, &fields)
		case "defaultUnitType":
			patch.DefaultUnitType = decodeString(value, key, &fields)
		case "requiresProvider":
			patch.RequiresProvider = decodeBool(value, key, &fields)
		case "active":
			patch.Active = decodeBool(value, key, &fields)
		case "code":
			// A wrong code is deactivated and replaced, never renamed: contracts and
			// claims already point at it.
			immutable(key, &fields)
		default:
			fields = append(fields, domain.FieldError{Field: key, Code: "UNKNOWN_FIELD", Message: "bilinmeyen alan"})
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	definition, err := h.svc.UpdateDefinition(r.Context(), rc, id, patch)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(definition.RowVersion))
	writeJSON(w, http.StatusOK, definitionView(definition))
}

func definitionView(d application.DefinitionRecord) kapsorav1.ServiceDefinition {
	return kapsorav1.ServiceDefinition{
		Id: d.ID, CategoryId: d.CategoryID, CategoryCode: d.CategoryCode,
		Domain: kapsorav1.ServiceDomain(d.Domain), Code: d.Code, Name: d.Name, Description: d.Description,
		FulfillmentMode:  kapsorav1.FulfillmentMode(d.FulfillmentMode),
		DefaultUnitType:  kapsorav1.ServiceUnitType(d.DefaultUnitType),
		RequiresProvider: d.RequiresProvider, Active: d.Active, RowVersion: int(d.RowVersion),
	}
}
