package healthhttp

import (
	"net/http"
	"strings"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/health/application"
	"github.com/celikbros/kapsora/internal/health/domain"
)

// ListHealthCases implements listHealthCases.
func (h *Handler) ListHealthCases(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCaseRead)
	if !ok {
		return
	}
	var fields []domain.FieldError
	filter := application.ListFilter{
		Cursor:                 r.URL.Query().Get("cursor"),
		Limit:                  queryLimit(r),
		Status:                 strings.TrimSpace(r.URL.Query().Get("status")),
		CaseType:               strings.TrimSpace(r.URL.Query().Get("caseType")),
		PersonID:               queryUUID(r, "personId", &fields),
		ProgramID:              queryUUID(r, "programId", &fields),
		ProviderOrganizationID: queryUUID(r, "providerOrganizationId", &fields),
		OpenedFrom:             queryTimestamp(r, "openedFrom", &fields),
		OpenedTo:               queryTimestamp(r, "openedTo", &fields),
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	page, err := h.svc.ListCases(r.Context(), rc, filter, accessRequest(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.HealthCasePage{Items: make([]kapsorav1.HealthCase, 0, len(page.Items))}
	for _, view := range page.Items {
		out.Items = append(out.Items, caseBody(view))
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateHealthCase implements createHealthCase.
func (h *Handler) CreateHealthCase(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCaseManage)
	if !ok {
		return
	}
	var body kapsorav1.CreateHealthCase
	if !decodeJSON(w, r, &body) {
		return
	}
	view, err := h.svc.CreateCase(r.Context(), rc, application.NewCaseInput{
		PersonID: body.PersonId, ProgramID: body.ProgramId, EnrollmentID: body.EnrollmentId,
		CaseType: string(body.CaseType), ProviderOrganizationID: body.ProviderOrganizationId,
		OpenedAt: body.OpenedAt, ServiceRequestID: body.ServiceRequestId,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Case.RowVersion))
	w.Header().Set("Location", "/api/v1/health-cases/"+view.Case.ID.String())
	writeJSON(w, http.StatusCreated, caseBody(view))
}

// GetHealthCase implements getHealthCase.
func (h *Handler) GetHealthCase(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCaseRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "caseId", application.ErrCaseNotFound)
	if !ok {
		return
	}
	view, err := h.svc.GetCase(r.Context(), rc, id, accessRequest(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Case.RowVersion))
	writeJSON(w, http.StatusOK, caseBody(view))
}

// CloseHealthCase implements closeHealthCase.
func (h *Handler) CloseHealthCase(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCaseManage)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "caseId", application.ErrCaseNotFound)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.CloseHealthCase
	if !decodeOptionalJSON(w, r, &body) {
		return
	}
	view, err := h.svc.CloseCase(r.Context(), rc, id, body.ReasonText, expected, accessRequest(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Case.RowVersion))
	writeJSON(w, http.StatusOK, caseBody(view))
}

// caseBody maps a projected case onto the wire. It carries no rule of its own: whatever the
// service cleared is nil here because it is nil on the record, which is what makes "the
// screen cannot leak what the API never sent" true one layer further down as well.
func caseBody(view application.CaseView) kapsorav1.HealthCase {
	record := view.Case
	out := kapsorav1.HealthCase{
		Id:           record.ID,
		PersonId:     record.PersonID,
		ProgramId:    record.ProgramID,
		EnrollmentId: record.EnrollmentID,
		CaseType:     kapsorav1.HealthCaseType(record.CaseType),
		OpenedAt:     record.OpenedAt,
		ClosedAt:     record.ClosedAt,
		Status:       kapsorav1.HealthCaseStatus(record.Status),
		Projection:   kapsorav1.HealthProjection(view.Projection),
		Encounters:   make([]kapsorav1.Encounter, 0, len(view.Encounters)),
		CreatedAt:    record.CreatedAt,
		RowVersion:   record.RowVersion,
	}
	if record.ProviderOrganizationID != nil {
		id := *record.ProviderOrganizationID
		out.ProviderOrganizationId = &id
	}
	if record.ServiceRequestID != nil {
		id := *record.ServiceRequestID
		out.ServiceRequestId = &id
	}
	if record.Sensitivity != "" {
		sensitivity := kapsorav1.HealthCaseSensitivity(record.Sensitivity)
		out.Sensitivity = &sensitivity
	}
	for _, encounter := range view.Encounters {
		out.Encounters = append(out.Encounters, encounterBody(encounter, view.Projection))
	}
	return out
}
