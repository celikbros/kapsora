package healthhttp

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/health/application"
	"github.com/celikbros/kapsora/internal/health/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// StayRoutes mounts everything below /inpatient-stays.
//
// Every command names health.case.manage and nothing else, because that is what section 2.5
// says: admitting somebody, extending it, recording the segments and discharging are all one
// job, done by provider staff, and the one decision that is not theirs — whether the plan
// pays for the admission — is not an endpoint here at all. It is the request's review, on the
// request page, and this module hears about it through the outbox.
func (h *Handler) StayRoutes(r chi.Router, mw StayMiddlewares) {
	r.Get("/", h.ListInpatientStays)
	r.With(wrap(mw.CreateStay)).Post("/", h.CreateInpatientStay)
	r.Get("/{stayId}", h.GetInpatientStay)
	r.Get("/{stayId}/reconciliation", h.GetInpatientStayReconciliation)
	r.With(wrap(mw.ExtendStay)).Post("/{stayId}/extensions", h.ExtendInpatientStay)
	r.With(wrap(mw.PutSegments)).Put("/{stayId}/segments", h.PutStaySegments)
	r.With(wrap(mw.Discharge)).Post("/{stayId}/discharge", h.DischargeInpatientStay)
	r.With(wrap(mw.CancelStay)).Post("/{stayId}/cancel", h.CancelInpatientStay)
}

// StayMiddlewares are the wrappers the integrator applies in cmd/api: the Idempotency-Key
// middleware on every command that changes state. A nil field means "no wrapper".
type StayMiddlewares struct {
	CreateStay  func(http.Handler) http.Handler
	ExtendStay  func(http.Handler) http.Handler
	PutSegments func(http.Handler) http.Handler
	Discharge   func(http.Handler) http.Handler
	CancelStay  func(http.Handler) http.Handler
}

// ListInpatientStays implements listInpatientStays.
func (h *Handler) ListInpatientStays(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCaseRead)
	if !ok {
		return
	}
	access, ok := h.accessRequest(w, r)
	if !ok {
		return
	}
	var fields []domain.FieldError
	filter := application.StayFilter{
		Cursor:                 r.URL.Query().Get("cursor"),
		Limit:                  queryLimit(r),
		Status:                 strings.TrimSpace(r.URL.Query().Get("status")),
		CaseID:                 queryUUID(r, "caseId", &fields),
		PersonID:               queryUUID(r, "personId", &fields),
		ProviderOrganizationID: queryUUID(r, "providerOrganizationId", &fields),
		AdmittedFrom:           queryTimestamp(r, "admittedFrom", &fields),
		AdmittedTo:             queryTimestamp(r, "admittedTo", &fields),
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	page, err := h.svc.ListStays(r.Context(), rc, filter, access)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.InpatientStayPage{Items: make([]kapsorav1.InpatientStay, 0, len(page.Items))}
	for _, view := range page.Items {
		out.Items = append(out.Items, stayBody(view))
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, out)
}

// GetInpatientStay implements getInpatientStay.
func (h *Handler) GetInpatientStay(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCaseRead)
	if !ok {
		return
	}
	access, ok := h.accessRequest(w, r)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "stayId", application.ErrStayNotFound)
	if !ok {
		return
	}
	view, err := h.svc.GetStay(r.Context(), rc, id, access)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeStay(w, http.StatusOK, view)
}

// CreateInpatientStay implements createInpatientStay.
func (h *Handler) CreateInpatientStay(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCaseManage)
	if !ok {
		return
	}
	access, ok := h.accessRequest(w, r)
	if !ok {
		return
	}
	var body kapsorav1.CreateInpatientStay
	if !decodeJSON(w, r, &body) {
		return
	}
	view, err := h.svc.CreateStay(r.Context(), rc, application.NewStayInput{
		CaseID: body.CaseId, ProviderOrganizationID: body.ProviderOrganizationId,
		LocationID: body.LocationId, AttendingPractitionerID: body.AttendingPractitionerId,
		AdmissionAt: body.AdmissionAt, EstimatedDays: body.EstimatedDays,
		AdmissionDiagnosisID: body.AdmissionDiagnosisId,
	}, access)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/inpatient-stays/"+view.Stay.ID.String())
	h.writeStay(w, http.StatusCreated, view)
}

// ExtendInpatientStay implements extendInpatientStay.
func (h *Handler) ExtendInpatientStay(w http.ResponseWriter, r *http.Request) {
	rc, id, expected, ok := h.stayCommand(w, r)
	if !ok {
		return
	}
	access, ok := h.accessRequest(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ExtendInpatientStay
	if !decodeJSON(w, r, &body) {
		return
	}
	view, err := h.svc.ExtendStay(r.Context(), rc, id, application.StayExtensionInput{
		AdditionalDays: body.AdditionalDays, ReasonCode: body.ReasonCode,
		ReasonText: body.ReasonText,
	}, expected, access)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeStay(w, http.StatusOK, view)
}

// PutStaySegments implements putStaySegments.
func (h *Handler) PutStaySegments(w http.ResponseWriter, r *http.Request) {
	rc, id, expected, ok := h.stayCommand(w, r)
	if !ok {
		return
	}
	access, ok := h.accessRequest(w, r)
	if !ok {
		return
	}
	var body kapsorav1.PutStaySegments
	if !decodeJSON(w, r, &body) {
		return
	}
	items := make([]domain.SegmentInput, 0, len(body.Items))
	for _, item := range body.Items {
		items = append(items, domain.SegmentInput{
			SegmentType: string(item.SegmentType), StartsAt: item.StartsAt,
			EndsAt: item.EndsAt, RoomCode: item.RoomCode, BedCode: item.BedCode,
		})
	}
	view, err := h.svc.PutStaySegments(r.Context(), rc, id, items, expected, access)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeStay(w, http.StatusOK, view)
}

// DischargeInpatientStay implements dischargeInpatientStay.
func (h *Handler) DischargeInpatientStay(w http.ResponseWriter, r *http.Request) {
	rc, id, expected, ok := h.stayCommand(w, r)
	if !ok {
		return
	}
	access, ok := h.accessRequest(w, r)
	if !ok {
		return
	}
	var body kapsorav1.DischargeInpatientStay
	if !decodeOptionalJSON(w, r, &body) {
		return
	}
	var dischargeAt time.Time
	if body.DischargeAt != nil {
		dischargeAt = *body.DischargeAt
	}
	view, err := h.svc.DischargeStay(r.Context(), rc, id, dischargeAt, expected, access)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeStay(w, http.StatusOK, view)
}

// CancelInpatientStay implements cancelInpatientStay.
func (h *Handler) CancelInpatientStay(w http.ResponseWriter, r *http.Request) {
	rc, id, expected, ok := h.stayCommand(w, r)
	if !ok {
		return
	}
	access, ok := h.accessRequest(w, r)
	if !ok {
		return
	}
	var body kapsorav1.CancelInpatientStay
	if !decodeJSON(w, r, &body) {
		return
	}
	view, err := h.svc.CancelStay(r.Context(), rc, id, body.ReasonCode, body.ReasonText,
		expected, access)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeStay(w, http.StatusOK, view)
}

// GetInpatientStayReconciliation implements getInpatientStayReconciliation.
func (h *Handler) GetInpatientStayReconciliation(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCaseRead)
	if !ok {
		return
	}
	access, ok := h.accessRequest(w, r)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "stayId", application.ErrStayNotFound)
	if !ok {
		return
	}
	out, err := h.svc.GetStayReconciliation(r.Context(), rc, id, access)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, kapsorav1.StayReconciliation{
		StayId: out.StayID, AuthorizationId: idPtr(out.AuthorizationID),
		AdmissionAt: out.AdmissionAt, DischargeAt: out.DischargeAt,
		AuthorizedDays: out.AuthorizedDays, ActualDays: out.ActualDays,
		ReleasedDays: out.ReleasedDays, OverAuthorization: out.OverAuthorization,
	})
}

// stayCommand is the preamble every state-changing stay command shares: the permission, the
// id and the If-Match the caller has to be holding.
func (h *Handler) stayCommand(w http.ResponseWriter, r *http.Request) (
	rc identity.RequestContext, id uuid.UUID, expected int64, ok bool,
) {
	rc, ok = h.require(w, r, PermissionCaseManage)
	if !ok {
		return rc, uuid.Nil, 0, false
	}
	id, ok = h.pathUUID(w, r, "stayId", application.ErrStayNotFound)
	if !ok {
		return rc, uuid.Nil, 0, false
	}
	expected, ok = requireIfMatch(w, r)
	return rc, id, expected, ok
}

// writeStay answers with the ETag the next command has to send back.
func (h *Handler) writeStay(w http.ResponseWriter, status int, view application.StayView) {
	w.Header().Set("ETag", etag(view.Stay.RowVersion))
	writeJSON(w, status, stayBody(view))
}

// stayBody maps a projected stay onto the wire. It carries no rule of its own: whatever the
// service cleared is absent here because it is absent on the record, which is what makes "a
// screen cannot leak what the API never sent" true one layer further down as well.
func stayBody(view application.StayView) kapsorav1.InpatientStay {
	record := view.Stay
	out := kapsorav1.InpatientStay{
		Id: record.ID, CaseId: record.CaseID, PersonId: record.PersonID,
		ProviderOrganizationId: record.ProviderOrganizationID,
		AdmissionAt:            record.AdmissionAt, EstimatedDays: record.EstimatedDays,
		ExpectedDischargeAt: record.ExpectedDischargeAt, DischargeAt: record.DischargeAt,
		Status: kapsorav1.InpatientStayStatus(record.Status),
		// The projection the service applied, reported so a screen can say "you may not see
		// clinical detail" rather than showing a record with holes in it.
		Projection:        kapsorav1.HealthProjection(view.Projection),
		ServiceRequestId:  record.ServiceRequestID,
		OverAuthorization: record.OverAuthorization,
		CancelReasonCode:  record.CancelReasonCode,
		Extensions:        make([]kapsorav1.StayExtension, 0, len(view.Extensions)),
		Segments:          make([]kapsorav1.StaySegment, 0, len(view.Segments)),
		CreatedAt:         record.CreatedAt,
		RowVersion:        record.RowVersion,
	}
	out.LocationId = idPtr(record.LocationID)
	out.AttendingPractitionerId = idPtr(record.AttendingPractitionerID)
	out.AuthorizationId = idPtr(record.AuthorizationID)
	// Absent rather than null in the financial projection, because the record no longer
	// carries it at all.
	out.AdmissionDiagnosisId = idPtr(record.AdmissionDiagnosisID)
	// An empty string is a NULL numeric column, and it is absent rather than "0": "not
	// authorized yet" and "authorized for nothing" are different answers.
	out.AuthorizedDays = decimalPtr(record.AuthorizedDays)
	out.ActualDays = decimalPtr(record.ActualDays)
	out.ReleasedDays = decimalPtr(record.ReleasedDays)
	for _, extension := range view.Extensions {
		out.Extensions = append(out.Extensions, kapsorav1.StayExtension{
			Id: extension.ID, StayId: extension.StayID, SequenceNo: extension.SequenceNo,
			AdditionalDays: extension.AdditionalDays, ReasonCode: extension.ReasonCode,
			ReasonText: extension.ReasonText, ServiceRequestId: extension.ServiceRequestID,
			AuthorizationId: idPtr(extension.AuthorizationID),
			Status:          kapsorav1.StayExtensionStatus(extension.Status),
			CreatedAt:       extension.CreatedAt, RowVersion: extension.RowVersion,
		})
	}
	for _, segment := range view.Segments {
		out.Segments = append(out.Segments, kapsorav1.StaySegment{
			Id: segment.ID, StayId: segment.StayID,
			SegmentType: kapsorav1.StaySegmentType(segment.SegmentType),
			StartsAt:    segment.StartsAt, EndsAt: segment.EndsAt,
			RoomCode: segment.RoomCode, BedCode: segment.BedCode,
			CreatedAt: segment.CreatedAt, RowVersion: segment.RowVersion,
		})
	}
	return out
}

// decimalPtr renders one of the exact-decimal day counts, or nothing at all when the column
// is NULL.
func decimalPtr(raw string) *string {
	if raw == "" {
		return nil
	}
	out := raw
	return &out
}
