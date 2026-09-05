package benefithttp

import (
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/benefit/application"
)

// ListEntitlementMappings implements listEntitlementMappings.
func (h *Handler) ListEntitlementMappings(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionProgramRead)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "planVersionId", application.ErrPlanVersionNotFound)
	if !ok {
		return
	}
	mappings, err := h.svc.ListMappings(r.Context(), rc, versionID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mappingList(mappings))
}

// PutEntitlementMappings implements putEntitlementMappings.
func (h *Handler) PutEntitlementMappings(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, application.PermissionMappingManage)
	if !ok {
		return
	}
	versionID, ok := h.pathUUID(w, r, "planVersionId", application.ErrPlanVersionNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body struct {
		Items []kapsorav1.EntitlementMappingInput `json:"items"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	items := make([]application.MappingInput, 0, len(body.Items))
	for _, item := range body.Items {
		next := application.MappingInput{
			ServiceDefinitionID: item.ServiceDefinitionId,
			EntitlementCode:     item.EntitlementCode,
		}
		if item.UnitFactor != nil {
			next.UnitFactor = *item.UnitFactor
		}
		if item.ValidFrom != nil {
			from := dateOnly(item.ValidFrom.Time)
			next.ValidFrom = &from
		}
		if item.ValidTo != nil {
			to := dateOnly(item.ValidTo.Time)
			next.ValidTo = &to
		}
		items = append(items, next)
	}

	mappings, err := h.svc.ReplaceMappings(r.Context(), rc, versionID, items, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mappingList(mappings))
}

// mappingList renders the contract's mapping collection.
func mappingList(mappings []application.Mapping) kapsorav1.ListEntitlementMappings200JSONResponse {
	out := kapsorav1.ListEntitlementMappings200JSONResponse{
		Items: make([]kapsorav1.EntitlementMapping, 0, len(mappings)),
	}
	for _, m := range mappings {
		out.Items = append(out.Items, kapsorav1.EntitlementMapping{
			Id: m.ID, PlanVersionId: m.PlanVersionID,
			ServiceDefinitionId: m.ServiceDefinitionID, ServiceCode: m.ServiceCode,
			ServiceName:             m.ServiceName,
			EntitlementDefinitionId: m.EntitlementDefinitionID,
			EntitlementCode:         m.EntitlementCode,
			UnitType:                kapsorav1.EntitlementMappingUnitType(m.UnitType),
			UnitFactor:              m.UnitFactor,
			ValidFrom:               datePtr(m.ValidFrom), ValidTo: datePtr(m.ValidTo),
			RowVersion: m.RowVersion,
		})
	}
	return out
}

// datePtr renders an optional day as the contract's date.
func datePtr(t *time.Time) *openapi_types.Date {
	if t == nil {
		return nil
	}
	return &openapi_types.Date{Time: *t}
}
