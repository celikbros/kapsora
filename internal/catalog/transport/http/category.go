package cataloghttp

import (
	"encoding/json"
	"net/http"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/catalog/application"
	"github.com/celikbros/kapsora/internal/catalog/domain"
)

// ListServiceCategories implements listServiceCategories.
func (h *Handler) ListServiceCategories(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	var fields []domain.FieldError
	filter := application.ListFilter{
		Query:    r.URL.Query().Get("q"),
		Cursor:   r.URL.Query().Get("cursor"),
		Limit:    queryLimit(r),
		Domain:   r.URL.Query().Get("domain"),
		Active:   queryBool(r, "active", &fields),
		ParentID: queryUUID(r, "parentId", &fields),
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	page, err := h.svc.ListCategories(r.Context(), rc, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.ServiceCategoryPage{Items: make([]kapsorav1.ServiceCategory, 0, len(page.Items))}
	for _, c := range page.Items {
		out.Items = append(out.Items, categoryView(c))
	}
	if page.NextCursor != "" {
		next := page.NextCursor
		out.NextCursor = &next
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateServiceCategory implements createServiceCategory.
func (h *Handler) CreateServiceCategory(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	var body kapsorav1.CreateServiceCategoryRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in := domain.NewCategory{
		Code: body.Code, Name: body.Name, Domain: string(body.Domain), Active: true,
	}
	if body.Active != nil {
		in.Active = *body.Active
	}
	if body.ParentId != nil {
		in.ParentID = body.ParentId.String()
	}

	category, err := h.svc.CreateCategory(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(category.RowVersion))
	w.Header().Set("Location", "/api/v1/service-categories/"+category.ID.String())
	writeJSON(w, http.StatusCreated, categoryView(category))
}

// GetServiceCategory implements getServiceCategory.
func (h *Handler) GetServiceCategory(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "categoryId", application.ErrCategoryNotFound)
	if !ok {
		return
	}
	category, err := h.svc.GetCategory(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(category.RowVersion))
	writeJSON(w, http.StatusOK, categoryView(category))
}

// PatchServiceCategory implements patchServiceCategory (merge-patch with If-Match).
func (h *Handler) PatchServiceCategory(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "categoryId", application.ErrCategoryNotFound)
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

	patch := domain.CategoryPatch{ExpectedVersion: expected}
	var fields []domain.FieldError
	for key, value := range raw {
		switch key {
		case "name":
			patch.Name = decodeString(value, key, &fields)
		case "active":
			patch.Active = decodeBool(value, key, &fields)
		case "parentId":
			if isJSONNull(value) {
				patch.ClearParent = true
			} else {
				patch.ParentID = decodeString(value, key, &fields)
			}
		case "code":
			// A category code is read by every definition, contract and claim under it.
			immutable(key, &fields)
		default:
			fields = append(fields, domain.FieldError{Field: key, Code: "UNKNOWN_FIELD", Message: "bilinmeyen alan"})
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	category, err := h.svc.UpdateCategory(r.Context(), rc, id, patch)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(category.RowVersion))
	writeJSON(w, http.StatusOK, categoryView(category))
}

func categoryView(c application.CategoryRecord) kapsorav1.ServiceCategory {
	out := kapsorav1.ServiceCategory{
		Id: c.ID, Code: c.Code, Name: c.Name, Domain: kapsorav1.ServiceDomain(c.Domain),
		Active: c.Active, RowVersion: int(c.RowVersion),
	}
	if c.ParentID != nil {
		parent := *c.ParentID
		out.ParentId = &parent
	}
	return out
}
