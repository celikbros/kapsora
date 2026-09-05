package healthhttp

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	kapsorav1 "github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/health/application"
	"github.com/celikbros/kapsora/internal/health/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// Permissions guarding the treatment report. Writing one and reviewing one are two grants on
// purpose: the provider that writes the report is not the payer that decides about it.
const (
	PermissionReportManage = application.PermissionReportManage
	PermissionReportReview = application.PermissionReportReview
)

// ReportRoutes mounts everything below /medical-reports.
func (h *Handler) ReportRoutes(r chi.Router, mw ReportMiddlewares) {
	r.Get("/", h.ListMedicalReports)
	r.With(wrap(mw.CreateReport)).Post("/", h.CreateMedicalReport)
	r.Get("/{reportId}", h.GetMedicalReport)
	r.With(wrap(mw.PatchReport)).Patch("/{reportId}", h.PatchMedicalReportDraft)
	r.With(wrap(mw.PutServices)).Put("/{reportId}/services", h.PutMedicalReportServices)
	r.With(wrap(mw.SubmitReport)).Post("/{reportId}/submit", h.SubmitMedicalReport)
	r.With(wrap(mw.StartReview)).Post("/{reportId}/start-review", h.StartMedicalReview)
	r.With(wrap(mw.Decide)).Post("/{reportId}/approve", h.ApproveMedicalReport)
	r.With(wrap(mw.Decide)).Post("/{reportId}/reject", h.RejectMedicalReport)
	r.With(wrap(mw.CancelReport)).Post("/{reportId}/cancel", h.CancelMedicalReport)
	r.Get("/{reportId}/usages", h.ListMedicalReportUsages)
}

// ReportMiddlewares are the wrappers the integrator applies in cmd/api: the Idempotency-Key
// middleware on every command that changes state. A nil field means "no wrapper".
type ReportMiddlewares struct {
	CreateReport func(http.Handler) http.Handler
	PatchReport  func(http.Handler) http.Handler
	PutServices  func(http.Handler) http.Handler
	SubmitReport func(http.Handler) http.Handler
	StartReview  func(http.Handler) http.Handler
	Decide       func(http.Handler) http.Handler
	CancelReport func(http.Handler) http.Handler
}

// ListMedicalReports implements listMedicalReports.
func (h *Handler) ListMedicalReports(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCaseRead)
	if !ok {
		return
	}
	var fields []domain.FieldError
	filter := application.ReportFilter{
		Cursor:                 r.URL.Query().Get("cursor"),
		Limit:                  queryLimit(r),
		Status:                 strings.TrimSpace(r.URL.Query().Get("status")),
		ReportType:             strings.TrimSpace(r.URL.Query().Get("reportType")),
		PersonID:               queryUUID(r, "personId", &fields),
		CaseID:                 queryUUID(r, "caseId", &fields),
		RootReportID:           queryUUID(r, "rootReportId", &fields),
		ProviderOrganizationID: queryUUID(r, "providerOrganizationId", &fields),
		ValidOn:                queryDate(r, "validOn", &fields),
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	page, err := h.svc.ListReports(r.Context(), rc, filter, accessRequest(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.MedicalReportPage{Items: make([]kapsorav1.MedicalReport, 0, len(page.Items))}
	for _, view := range page.Items {
		out.Items = append(out.Items, reportBody(view))
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, out)
}

// GetMedicalReport implements getMedicalReport.
func (h *Handler) GetMedicalReport(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCaseRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "reportId", application.ErrReportNotFound)
	if !ok {
		return
	}
	view, err := h.svc.GetReport(r.Context(), rc, id, accessRequest(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeReport(w, http.StatusOK, view)
}

// CreateMedicalReport implements createMedicalReport.
func (h *Handler) CreateMedicalReport(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionReportManage)
	if !ok {
		return
	}
	var body kapsorav1.CreateMedicalReport
	if !decodeJSON(w, r, &body) {
		return
	}
	view, err := h.svc.CreateReport(r.Context(), rc, application.NewReportInput{
		PersonID: body.PersonId, CaseID: body.CaseId, SupersedesReportID: body.SupersedesReportId,
		ReportType: derefString(body.ReportType), ReportSubtype: body.ReportSubtype,
		IssuingPractitionerID:         body.IssuingPractitionerId,
		IssuingProviderOrganizationID: body.IssuingProviderOrganizationId,
		IssuedAt:                      timePtr(body.IssuedAt), ValidFrom: timePtr(body.ValidFrom),
		ValidTo: timePtr(body.ValidTo), ClinicalSummary: body.ClinicalSummary,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/medical-reports/"+view.Report.ID.String())
	h.writeReport(w, http.StatusCreated, view)
}

// PatchMedicalReportDraft implements patchMedicalReportDraft.
func (h *Handler) PatchMedicalReportDraft(w http.ResponseWriter, r *http.Request) {
	rc, id, expected, ok := h.reportCommand(w, r, PermissionReportManage)
	if !ok {
		return
	}
	var body kapsorav1.PatchMedicalReportDraft
	if !decodeJSON(w, r, &body) {
		return
	}
	issued, from, to := body.IssuedAt.Time, body.ValidFrom.Time, body.ValidTo.Time
	view, err := h.svc.PatchReportDraft(r.Context(), rc, id, application.NewReportInput{
		CaseID: body.CaseId, ReportType: body.ReportType, ReportSubtype: body.ReportSubtype,
		IssuingPractitionerID:         body.IssuingPractitionerId,
		IssuingProviderOrganizationID: body.IssuingProviderOrganizationId,
		IssuedAt:                      &issued, ValidFrom: &from, ValidTo: &to,
		ClinicalSummary: body.ClinicalSummary,
	}, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeReport(w, http.StatusOK, view)
}

// PutMedicalReportServices implements putMedicalReportServices.
func (h *Handler) PutMedicalReportServices(w http.ResponseWriter, r *http.Request) {
	rc, id, expected, ok := h.reportCommand(w, r, PermissionReportManage)
	if !ok {
		return
	}
	var body kapsorav1.PutMedicalReportServices
	if !decodeJSON(w, r, &body) {
		return
	}
	items := make([]domain.ReportServiceInput, 0, len(body.Items))
	for _, item := range body.Items {
		items = append(items, domain.ReportServiceInput{
			ServiceDefinitionID: item.ServiceDefinitionId.String(),
			CoveredQuantity:     item.CoveredQuantity, CoveredAmount: item.CoveredAmount,
			CurrencyCode: item.CurrencyCode, Notes: item.Notes,
		})
	}
	view, err := h.svc.PutReportServices(r.Context(), rc, id, items, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeReport(w, http.StatusOK, view)
}

// SubmitMedicalReport implements submitMedicalReport.
func (h *Handler) SubmitMedicalReport(w http.ResponseWriter, r *http.Request) {
	rc, id, expected, ok := h.reportCommand(w, r, PermissionReportManage)
	if !ok {
		return
	}
	view, err := h.svc.SubmitReport(r.Context(), rc, id, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeReport(w, http.StatusOK, view)
}

// CancelMedicalReport implements cancelMedicalReport.
func (h *Handler) CancelMedicalReport(w http.ResponseWriter, r *http.Request) {
	rc, id, expected, ok := h.reportCommand(w, r, PermissionReportManage)
	if !ok {
		return
	}
	view, err := h.svc.CancelReport(r.Context(), rc, id, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeReport(w, http.StatusOK, view)
}

// StartMedicalReview implements startMedicalReview.
func (h *Handler) StartMedicalReview(w http.ResponseWriter, r *http.Request) {
	rc, id, expected, ok := h.reportCommand(w, r, PermissionReportReview)
	if !ok {
		return
	}
	view, err := h.svc.StartReview(r.Context(), rc, id, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeReport(w, http.StatusOK, view)
}

// ApproveMedicalReport implements approveMedicalReport.
func (h *Handler) ApproveMedicalReport(w http.ResponseWriter, r *http.Request) {
	rc, id, expected, ok := h.reportCommand(w, r, PermissionReportReview)
	if !ok {
		return
	}
	var body kapsorav1.DecideMedicalReport
	if !decodeOptionalJSON(w, r, &body) {
		return
	}
	view, err := h.svc.ApproveReport(r.Context(), rc, id, body.ReviewComment, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeReport(w, http.StatusOK, view)
}

// RejectMedicalReport implements rejectMedicalReport.
func (h *Handler) RejectMedicalReport(w http.ResponseWriter, r *http.Request) {
	rc, id, expected, ok := h.reportCommand(w, r, PermissionReportReview)
	if !ok {
		return
	}
	var body kapsorav1.RejectMedicalReport
	if !decodeJSON(w, r, &body) {
		return
	}
	view, err := h.svc.RejectReport(r.Context(), rc, id, body.ReviewComment,
		strings.TrimSpace(body.RejectReasonCode), expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeReport(w, http.StatusOK, view)
}

// ListMedicalReportUsages implements listMedicalReportUsages.
func (h *Handler) ListMedicalReportUsages(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCaseRead)
	if !ok {
		return
	}
	id, ok := h.pathUUID(w, r, "reportId", application.ErrReportNotFound)
	if !ok {
		return
	}
	page, err := h.svc.ListUsages(r.Context(), rc, id, r.URL.Query().Get("cursor"), queryLimit(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := kapsorav1.MedicalReportUsagePage{
		Items: make([]kapsorav1.MedicalReportUsage, 0, len(page.Items)),
	}
	for _, row := range page.Items {
		out.Items = append(out.Items, kapsorav1.MedicalReportUsage{
			Id: row.ID, ReportId: row.ReportID,
			UsedByType: kapsorav1.MedicalReportUsedByType(row.UsedByType),
			UsedById:   row.UsedByID, UsedAt: row.UsedAt,
		})
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		out.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, out)
}

// reportCommand is the preamble every state-changing report command shares: the permission,
// the id and the If-Match the ground rules demand of anything that moves a record.
func (h *Handler) reportCommand(w http.ResponseWriter, r *http.Request, permission string) (
	rc identity.RequestContext, id uuid.UUID, expected int64, ok bool,
) {
	rc, ok = h.require(w, r, permission)
	if !ok {
		return rc, uuid.Nil, 0, false
	}
	id, ok = h.pathUUID(w, r, "reportId", application.ErrReportNotFound)
	if !ok {
		return rc, uuid.Nil, 0, false
	}
	expected, ok = requireIfMatch(w, r)
	return rc, id, expected, ok
}

// writeReport answers with the ETag the next command has to send back.
func (h *Handler) writeReport(w http.ResponseWriter, status int, view application.ReportView) {
	w.Header().Set("ETag", etag(view.Report.RowVersion))
	writeJSON(w, status, reportBody(view))
}

// reportBody maps a projected report onto the wire. It carries no rule of its own: whatever
// the service cleared is absent here because it is absent on the record, which is what makes
// "a screen cannot leak what the API never sent" true one layer further down as well.
func reportBody(view application.ReportView) kapsorav1.MedicalReport {
	record := view.Report
	out := kapsorav1.MedicalReport{
		Id: record.ID, PersonId: record.PersonID, Reference: record.Reference,
		VersionNo: record.VersionNo, RootReportId: record.RootReportID,
		IssuedAt:  openapi_types.Date{Time: record.IssuedAt},
		ValidFrom: openapi_types.Date{Time: record.ValidFrom},
		ValidTo:   openapi_types.Date{Time: record.ValidTo},
		Status:    kapsorav1.MedicalReportStatus(record.Status),
		// The projection the service applied, reported so a screen can say "you may not see
		// clinical detail" rather than showing a record with holes in it.
		Projection:       kapsorav1.HealthProjection(view.Projection),
		RejectReasonCode: record.RejectReasonCode,
		ReviewedAt:       record.ReviewedAt,
		SubmittedAt:      record.SubmittedAt,
		Services:         make([]kapsorav1.MedicalReportService, 0, len(view.Services)),
		Documents:        make([]kapsorav1.MedicalReportDocument, 0, len(view.Documents)),
		CreatedAt:        record.CreatedAt,
		RowVersion:       record.RowVersion,
	}
	out.CaseId = idPtr(record.CaseID)
	out.SupersedesReportId = idPtr(record.SupersedesReportID)
	out.IssuingPractitionerId = idPtr(record.IssuingPractitionerID)
	out.IssuingProviderOrganizationId = idPtr(record.IssuingProviderOrganizationID)
	out.ReviewedBy = idPtr(record.ReviewedBy)
	out.SubmittedBy = idPtr(record.SubmittedBy)
	// Absent rather than null in the financial projection, because the record no longer
	// carries them at all.
	if record.ReportType != "" {
		reportType := record.ReportType
		out.ReportType = &reportType
	}
	out.ReportSubtype = record.ReportSubtype
	out.ClinicalSummary = record.ClinicalSummary
	out.ReviewComment = record.ReviewComment
	for _, line := range view.Services {
		out.Services = append(out.Services, kapsorav1.MedicalReportService{
			Id: line.ID, ServiceDefinitionId: line.ServiceDefinitionID,
			ServiceCode: line.ServiceCode, ServiceName: line.ServiceName,
			CoveredQuantity: line.CoveredQuantity, CoveredAmount: line.CoveredAmount,
			CurrencyCode: line.CurrencyCode, Notes: line.Notes,
		})
	}
	for _, doc := range view.Documents {
		out.Documents = append(out.Documents, kapsorav1.MedicalReportDocument{
			Id: doc.ID, ObjectId: doc.ObjectID, DocumentTypeCode: doc.DocumentTypeCode,
			Purpose: doc.Purpose, RequiredPermission: doc.RequiredPermission,
			OriginalFilename: doc.OriginalFilename, ContentType: doc.ContentType,
			ScanStatus:     kapsorav1.MedicalReportDocumentScanStatus(doc.ScanStatus),
			Classification: kapsorav1.MedicalReportDocumentClassification(doc.Classification),
			CreatedAt:      doc.CreatedAt,
		})
	}
	return out
}

func idPtr(id *uuid.UUID) *uuid.UUID {
	if id == nil {
		return nil
	}
	out := *id
	return &out
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func timePtr(d *openapi_types.Date) *time.Time {
	if d == nil {
		return nil
	}
	out := d.Time
	return &out
}

// queryDate reads an optional date filter; a malformed one is a field error rather than a
// silently empty page.
func queryDate(r *http.Request, name string, fields *[]domain.FieldError) *time.Time {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil
	}
	t, err := time.Parse(time.DateOnly, raw)
	if err != nil {
		*fields = append(*fields, domain.FieldError{
			Field: name, Code: "FORMAT", Message: "YYYY-AA-GG biçiminde olmalı",
		})
		return nil
	}
	return &t
}
