package cataloghttp

import (
	"net/http"

	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/catalog/application"
	"github.com/celikbros/kapsora/internal/catalog/domain"
)

// ListServiceCodeMappings implements listServiceCodeMappings.
func (h *Handler) ListServiceCodeMappings(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "definitionId", application.ErrDefinitionNotFound)
	if !ok {
		return
	}
	result, err := h.svc.ListMappings(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mappingListView(result))
}

// PutServiceCodeMappings implements putServiceCodeMappings; If-Match carries the ETag of
// the service definition, which the replacement moves on.
func (h *Handler) PutServiceCodeMappings(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionManage)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "definitionId", application.ErrDefinitionNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ReplaceServiceCodeMappingsRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	items := make([]domain.MappingInput, 0, len(body.Items))
	for _, row := range body.Items {
		in := domain.MappingInput{
			CodeSystemID: row.CodeSystemId.String(), Code: row.Code,
			ValidFrom: domain.DateOnly(row.ValidFrom.Time),
		}
		if row.ValidTo != nil {
			to := domain.DateOnly(row.ValidTo.Time)
			in.ValidTo = &to
		}
		if row.Primary != nil {
			in.Primary = *row.Primary
		}
		items = append(items, in)
	}

	result, err := h.svc.ReplaceMappings(r.Context(), rc, id, items, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(result.RowVersion))
	writeJSON(w, http.StatusOK, mappingListView(result))
}

func mappingListView(result application.MappingResult) kapsorav1.ServiceCodeMappingList {
	out := kapsorav1.ServiceCodeMappingList{
		Items: make([]kapsorav1.ServiceCodeMapping, 0, len(result.Items)),
	}
	for _, m := range result.Items {
		version := m.CodeSystemVersion
		item := kapsorav1.ServiceCodeMapping{
			Id: m.ID, ServiceDefinitionId: m.ServiceDefinitionID, CodeSystemId: m.CodeSystemID,
			CodeSystemCode: m.CodeSystemCode, CodeSystemVersion: &version, Code: m.Code,
			ValidFrom: openapi_types.Date{Time: m.ValidFrom}, Primary: m.Primary,
		}
		if m.ValidTo != nil {
			item.ValidTo = &openapi_types.Date{Time: *m.ValidTo}
		}
		out.Items = append(out.Items, item)
	}
	return out
}
