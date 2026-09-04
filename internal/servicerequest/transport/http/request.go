package servicerequesthttp

import (
	"encoding/json"
	"net/http"
	"strings"

	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/servicerequest/application"
	"github.com/celikbros/kapsora/internal/servicerequest/domain"
)

// ListServiceRequests implements listServiceRequests.
func (h *Handler) ListServiceRequests(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	var fields []domain.FieldError
	filter := application.ListFilter{
		Cursor:                 r.URL.Query().Get("cursor"),
		Limit:                  queryLimit(r),
		Status:                 strings.TrimSpace(r.URL.Query().Get("status")),
		Channel:                strings.TrimSpace(r.URL.Query().Get("channel")),
		PersonID:               queryUUID(r, "personId", &fields),
		ProgramID:              queryUUID(r, "programId", &fields),
		ProviderOrganizationID: queryUUID(r, "providerOrganizationId", &fields),
		ServiceDateFrom:        queryDate(r, "serviceDateFrom", &fields),
		ServiceDateTo:          queryDate(r, "serviceDateTo", &fields),
		CreatedFrom:            queryTimestamp(r, "createdFrom", &fields),
		CreatedTo:              queryTimestamp(r, "createdTo", &fields),
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	page, err := h.svc.List(r.Context(), rc, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.ServiceRequestPage{Items: make([]kapsorav1.ServiceRequest, 0, len(page.Items))}
	for _, view := range page.Items {
		out.Items = append(out.Items, requestView(view))
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateServiceRequest implements createServiceRequest.
func (h *Handler) CreateServiceRequest(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCreate)
	if !ok {
		return
	}
	var body kapsorav1.CreateServiceRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.NewRequestInput{
		RequestType: string(body.RequestType), PersonID: body.PersonId,
		ProgramID: body.ProgramId, EnrollmentID: body.EnrollmentId,
		ProviderOrganizationID: body.ProviderOrganizationId,
		ServiceDate:            body.ServiceDate.Time,
		RequestedStartAt:       body.RequestedStartAt, RequestedEndAt: body.RequestedEndAt,
		Channel: string(body.Channel), SupersedesRequestID: body.SupersedesRequestId,
		Items: itemInputs(body.Items),
	}
	view, err := h.svc.Create(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Request.RowVersion))
	w.Header().Set("Location", "/api/v1/service-requests/"+view.Request.ID.String())
	writeJSON(w, http.StatusCreated, requestView(view))
}

// GetServiceRequest implements getServiceRequest.
func (h *Handler) GetServiceRequest(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, application.ErrRequestNotFound)
	if !ok {
		return
	}
	view, err := h.svc.Get(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Request.RowVersion))
	writeJSON(w, http.StatusOK, requestView(view))
}

// PatchServiceRequestDraft implements patchServiceRequestDraft (merge-patch with If-Match).
//
// The body is decoded field by field rather than into the generated struct, because the
// answer to `{"status":"APPROVED"}` has to be a 422 naming the field, and a struct with no
// status field would either reject the whole body as unknown or accept it silently.
func (h *Handler) PatchServiceRequestDraft(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCreate)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, application.ErrRequestNotFound)
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

	patch := application.PatchInput{ExpectedVersion: expected}
	var fields []domain.FieldError
	for key, value := range raw {
		switch key {
		case "providerOrganizationId":
			if isJSONNull(value) {
				patch.ClearProviderOrganization = true
			} else {
				patch.ProviderOrganizationID = decodeUUID(value, key, &fields)
			}
		case "serviceDate":
			if isJSONNull(value) {
				fields = append(fields, domain.FieldError{
					Field: key, Code: "REQUIRED", Message: "hizmet tarihi kaldırılamaz",
				})
			} else {
				patch.ServiceDate = decodeDate(value, key, &fields)
			}
		case "requestedStartAt":
			if isJSONNull(value) {
				patch.ClearRequestedStartAt = true
			} else {
				patch.RequestedStartAt = decodeTimestamp(value, key, &fields)
			}
		case "requestedEndAt":
			if isJSONNull(value) {
				patch.ClearRequestedEndAt = true
			} else {
				patch.RequestedEndAt = decodeTimestamp(value, key, &fields)
			}
		case "status", "reference", "requestType", "personId", "programId", "enrollmentId",
			"channel", "currentVersionNo", "supersedesRequestId", "items", "rowVersion",
			"eligibilityEvaluationId", "ruleEvaluationId", "requiredDocumentTypes",
			"returnReasonCode", "rejectReasonCode", "reviewComment", "submittedAt", "closedAt":
			immutable(key, &fields)
		default:
			unknownField(key, &fields)
		}
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}

	view, err := h.svc.Patch(r.Context(), rc, id, patch)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Request.RowVersion))
	writeJSON(w, http.StatusOK, requestView(view))
}

// PutServiceRequestItems implements putServiceRequestItems.
func (h *Handler) PutServiceRequestItems(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCreate)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, application.ErrRequestNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ServiceRequestItems
	if !decodeJSON(w, r, &body) {
		return
	}
	view, err := h.svc.ReplaceItems(r.Context(), rc, id, itemInputs(body.Items), expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Request.RowVersion))
	writeJSON(w, http.StatusOK, requestView(view))
}

// itemInputs narrows the generated line type onto the domain's own.
func itemInputs(in []kapsorav1.ServiceRequestItemInput) []domain.ItemInput {
	out := make([]domain.ItemInput, 0, len(in))
	for _, item := range in {
		row := domain.ItemInput{
			ServiceDefinitionID: item.ServiceDefinitionId.String(),
			RequestedQuantity:   item.RequestedQuantity,
			UnitType:            string(item.UnitType),
		}
		if item.RequestedAmount != nil {
			row.RequestedAmount = *item.RequestedAmount
		}
		if item.CurrencyCode != nil {
			row.CurrencyCode = *item.CurrencyCode
		}
		out = append(out, row)
	}
	return out
}

// requestView renders a request and the lines of its current version.
func requestView(view application.RequestView) kapsorav1.ServiceRequest {
	r := view.Request
	out := kapsorav1.ServiceRequest{
		Id: r.ID, Reference: r.Reference,
		RequestType: kapsorav1.ServiceRequestType(r.RequestType),
		PersonId:    r.PersonID, ProgramId: r.ProgramID, EnrollmentId: r.EnrollmentID,
		ProviderOrganizationId: r.ProviderOrganizationID,
		ServiceDate:            openapi_types.Date{Time: r.ServiceDate},
		RequestedStartAt:       r.RequestedStartAt, RequestedEndAt: r.RequestedEndAt,
		Channel:                 kapsorav1.ServiceRequestChannel(r.Channel),
		Status:                  kapsorav1.ServiceRequestStatus(r.Status),
		CurrentVersionNo:        r.CurrentVersionNo,
		SupersedesRequestId:     r.SupersedesRequestID,
		EligibilityEvaluationId: r.EligibilityEvaluationID,
		RuleEvaluationId:        r.RuleEvaluationID,
		ReturnReasonCode:        r.ReturnReasonCode,
		RejectReasonCode:        r.RejectReasonCode,
		ReviewComment:           r.ReviewComment,
		Items:                   itemViews(view.Items),
		RowVersion:              int(r.RowVersion),
		CreatedAt:               r.CreatedAt.UTC(),
		SubmittedAt:             utcPtr(r.SubmittedAt), ClosedAt: utcPtr(r.ClosedAt),
	}
	// A null list means the rules have not been asked yet; an empty one means they were
	// asked and required nothing. The two are different answers and are kept apart.
	if r.RequiredDocumentTypes != nil {
		types := r.RequiredDocumentTypes
		out.RequiredDocumentTypes = &types
	}
	return out
}

func itemViews(items []application.ItemRecord) []kapsorav1.ServiceRequestItem {
	out := make([]kapsorav1.ServiceRequestItem, 0, len(items))
	for _, item := range items {
		out = append(out, kapsorav1.ServiceRequestItem{
			Id: item.ID, LineNo: item.LineNo, ServiceDefinitionId: item.ServiceDefinitionID,
			RequestedQuantity: item.RequestedQuantity,
			UnitType:          kapsorav1.ServiceUnitType(item.UnitType),
			RequestedAmount:   item.RequestedAmount, CurrencyCode: item.CurrencyCode,
			Status:           kapsorav1.ServiceRequestItemStatus(item.Status),
			ApprovedQuantity: item.ApprovedQuantity, ApprovedAmount: item.ApprovedAmount,
			DecisionReasonCode: item.DecisionReasonCode,
		})
	}
	return out
}
