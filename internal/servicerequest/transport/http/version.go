package servicerequesthttp

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/servicerequest/application"
	"github.com/celikbros/kapsora/internal/servicerequest/domain"
)

// ListServiceRequestVersions implements listServiceRequestVersions.
func (h *Handler) ListServiceRequestVersions(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, application.ErrRequestNotFound)
	if !ok {
		return
	}
	versions, err := h.svc.ListVersions(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.ServiceRequestVersionList{
		Items: make([]kapsorav1.ServiceRequestVersionSummary, 0, len(versions)),
	}
	for _, version := range versions {
		out.Items = append(out.Items, versionSummary(version))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetServiceRequestVersion implements getServiceRequestVersion. A submitted version answers
// from the snapshot it was frozen with rather than from the live rows, so what a reviewer
// decided against is what a reader sees however the request moved on afterwards.
func (h *Handler) GetServiceRequestVersion(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, application.ErrRequestNotFound)
	if !ok {
		return
	}
	number, err := strconv.Atoi(chi.URLParam(r, "versionNo"))
	if err != nil || number < 1 {
		h.writeError(w, r, application.ErrVersionNotFound)
		return
	}
	view, err := h.svc.GetVersion(r.Context(), rc, id, number)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, versionView(view))
}

func versionSummary(v application.VersionRecord) kapsorav1.ServiceRequestVersionSummary {
	return kapsorav1.ServiceRequestVersionSummary{
		Id: v.ID, VersionNo: v.VersionNo,
		Status:      kapsorav1.ServiceRequestVersionStatus(v.Status),
		SubmittedAt: utcPtr(v.SubmittedAt), SubmittedBy: v.SubmittedBy,
		ReturnedAt: utcPtr(v.ReturnedAt), ReturnedBy: v.ReturnedBy,
		ReturnReasonCode: v.ReturnReasonCode, ReturnReasonText: v.ReturnReasonText,
		CreatedAt: v.CreatedAt.UTC(),
	}
}

func versionView(view application.VersionView) kapsorav1.ServiceRequestVersion {
	v := view.Version
	return kapsorav1.ServiceRequestVersion{
		Id: v.ID, ServiceRequestId: v.ServiceRequestID, VersionNo: v.VersionNo,
		Status:      kapsorav1.ServiceRequestVersionStatus(v.Status),
		SubmittedAt: utcPtr(v.SubmittedAt), SubmittedBy: v.SubmittedBy,
		ReturnedAt: utcPtr(v.ReturnedAt), ReturnedBy: v.ReturnedBy,
		ReturnReasonCode: v.ReturnReasonCode, ReturnReasonText: v.ReturnReasonText,
		Items:     snapshotItemViews(v, view.Items),
		CreatedAt: v.CreatedAt.UTC(),
	}
}

// snapshotItemViews answers a submitted version from its frozen document. The live rows are
// still read for the decisions a reviewer recorded afterwards, which the snapshot cannot
// carry because it was written before they existed; the requested values come from the
// snapshot, so they are what was actually submitted whatever happened since.
func snapshotItemViews(v application.VersionRecord, live []application.ItemRecord) []kapsorav1.ServiceRequestItem {
	if v.Status == domain.VersionDraft || len(v.Snapshot) == 0 {
		return itemViews(live)
	}
	var doc struct {
		Items []struct {
			LineNo              int     `json:"lineNo"`
			ServiceDefinitionID string  `json:"serviceDefinitionId"`
			RequestedQuantity   string  `json:"requestedQuantity"`
			UnitType            string  `json:"unitType"`
			RequestedAmount     *string `json:"requestedAmount"`
			CurrencyCode        *string `json:"currencyCode"`
		} `json:"items"`
	}
	if err := json.Unmarshal(v.Snapshot, &doc); err != nil || len(doc.Items) == 0 {
		// A snapshot nobody can read is a bug, not a reason to show nothing; the live rows
		// of a frozen version are byte-identical in their requested values anyway, because
		// the trigger of migration 000006 will not let them change.
		return itemViews(live)
	}
	byLine := make(map[int]application.ItemRecord, len(live))
	for _, item := range live {
		byLine[item.LineNo] = item
	}
	out := make([]kapsorav1.ServiceRequestItem, 0, len(doc.Items))
	for _, item := range doc.Items {
		row := kapsorav1.ServiceRequestItem{
			LineNo:            item.LineNo,
			RequestedQuantity: item.RequestedQuantity,
			UnitType:          kapsorav1.ServiceUnitType(item.UnitType),
			RequestedAmount:   item.RequestedAmount, CurrencyCode: item.CurrencyCode,
			Status: kapsorav1.ServiceRequestItemStatus(domain.ItemRequested),
		}
		if current, ok := byLine[item.LineNo]; ok {
			row.Id = current.ID
			row.ServiceDefinitionId = current.ServiceDefinitionID
			row.Status = kapsorav1.ServiceRequestItemStatus(current.Status)
			row.ApprovedQuantity, row.ApprovedAmount = current.ApprovedQuantity, current.ApprovedAmount
			row.DecisionReasonCode = current.DecisionReasonCode
		}
		out = append(out, row)
	}
	return out
}
