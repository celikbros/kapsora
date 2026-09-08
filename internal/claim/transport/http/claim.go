package claimhttp

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/claim/application"
	"github.com/celikbros/kapsora/internal/claim/domain"
	"github.com/celikbros/kapsora/internal/identity"
)

// ListClaims serves GET /claims.
func (h *Handler) ListClaims(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	access, ok := h.accessRequest(w, r)
	if !ok {
		return
	}
	var fields []domain.FieldError
	filter := application.ClaimFilter{
		Cursor: r.URL.Query().Get("cursor"), Limit: queryLimit(r),
		PersonID: queryUUID(r, "personId", &fields), CaseID: queryUUID(r, "caseId", &fields),
		ProviderOrganizationID: queryUUID(r, "providerOrganizationId", &fields),
		Status:                 r.URL.Query().Get("status"),
		ServiceDateFrom:        queryDate(r, "serviceDateFrom", &fields),
		ServiceDateTo:          queryDate(r, "serviceDateTo", &fields),
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	page, err := h.svc.ListClaims(r.Context(), rc, filter, access)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	body := kapsorav1.ClaimPage{Items: make([]kapsorav1.Claim, 0, len(page.Items))}
	for _, item := range page.Items {
		body.Items = append(body.Items, claimView(item))
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		body.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, body)
}

// CreateClaim serves POST /claims.
func (h *Handler) CreateClaim(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCreate)
	if !ok {
		return
	}
	access, ok := h.accessRequest(w, r)
	if !ok {
		return
	}
	var body kapsorav1.CreateClaim
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.NewClaimInput{
		PersonID: body.PersonId, ProgramID: body.ProgramId,
		EnrollmentID: body.EnrollmentId, ProviderOrganizationID: body.ProviderOrganizationId,
		CaseID: body.CaseId, FulfilmentID: body.FulfilmentId,
		AuthorizationID: body.AuthorizationId,
		ServiceDateFrom: body.ServiceDateFrom.Time, ServiceDateTo: body.ServiceDateTo.Time,
		Lines: newLines(body.Lines),
	}
	if body.Channel != nil {
		in.Channel = string(*body.Channel)
	}
	view, err := h.svc.CreateClaim(r.Context(), rc, in, access)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Claim.RowVersion))
	w.Header().Set("Location", "/api/v1/claims/"+view.Claim.ID.String())
	writeJSON(w, http.StatusCreated, claimView(view))
}

// GetClaim serves GET /claims/{claimId}.
func (h *Handler) GetClaim(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	access, ok := h.accessRequest(w, r)
	if !ok {
		return
	}
	id, ok := h.claimID(w, r)
	if !ok {
		return
	}
	view, err := h.svc.GetClaim(r.Context(), rc, id, access)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Claim.RowVersion))
	writeJSON(w, http.StatusOK, claimView(view))
}

// PatchClaimDraft serves PATCH /claims/{claimId}.
func (h *Handler) PatchClaimDraft(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCreate)
	if !ok {
		return
	}
	id, ok := h.claimID(w, r)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.PatchClaimDraft
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.DraftInput{
		ServiceDateFrom: body.ServiceDateFrom.Time, ServiceDateTo: body.ServiceDateTo.Time,
		CaseID: body.CaseId, FulfilmentID: body.FulfilmentId,
		AuthorizationID: body.AuthorizationId, ExpectedVersion: expected,
	}
	if body.Channel != nil {
		in.Channel = string(*body.Channel)
	}
	view, err := h.svc.PatchDraft(r.Context(), rc, id, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Claim.RowVersion))
	writeJSON(w, http.StatusOK, claimView(view))
}

// PutClaimLines serves PUT /claims/{claimId}/lines.
func (h *Handler) PutClaimLines(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCreate)
	if !ok {
		return
	}
	id, ok := h.claimID(w, r)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.PutClaimLines
	if !decodeJSON(w, r, &body) {
		return
	}
	view, err := h.svc.PutLines(r.Context(), rc, id, newLines(body.Lines), expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Claim.RowVersion))
	writeJSON(w, http.StatusOK, claimView(view))
}

// SubmitClaim serves POST /claims/{claimId}/submit.
func (h *Handler) SubmitClaim(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionSubmit)
	if !ok {
		return
	}
	access, ok := h.accessRequest(w, r)
	if !ok {
		return
	}
	id, ok := h.claimID(w, r)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	view, err := h.svc.Submit(r.Context(), rc, id, expected, access)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Claim.RowVersion))
	writeJSON(w, http.StatusOK, claimView(view))
}

// DecideClaimLines serves POST /claims/{claimId}/line-decisions.
func (h *Handler) DecideClaimLines(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireReview(w, r)
	if !ok {
		return
	}
	access, ok := h.accessRequest(w, r)
	if !ok {
		return
	}
	id, ok := h.claimID(w, r)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.DecideClaimLines
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.DecideInput{
		Decisions:     make([]application.DecisionInput, 0, len(body.Decisions)),
		ReviewComment: body.ReviewComment, ExpectedVersion: expected,
	}
	for _, d := range body.Decisions {
		in.Decisions = append(in.Decisions, application.DecisionInput{
			LineNo: d.LineNo, Decision: string(d.Decision),
			ApprovedQuantity: d.ApprovedQuantity, ApprovedAmount: d.ApprovedAmount,
			PayerAmount: d.PayerAmount, MemberAmount: d.MemberAmount,
			ReasonCode: d.ReasonCode, ReasonText: d.ReasonText,
		})
	}
	view, err := h.svc.DecideLines(r.Context(), rc, id, in, access)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Claim.RowVersion))
	writeJSON(w, http.StatusOK, claimView(view))
}

// ApproveClaim serves POST /claims/{claimId}/approve.
func (h *Handler) ApproveClaim(w http.ResponseWriter, r *http.Request) {
	h.decide(w, r, func(rc identity.RequestContext, id uuid.UUID, in application.ReasonInput) (application.ClaimView, error) {
		return h.svc.Approve(r.Context(), rc, id, in)
	})
}

// RejectClaim serves POST /claims/{claimId}/reject.
func (h *Handler) RejectClaim(w http.ResponseWriter, r *http.Request) {
	h.decide(w, r, func(rc identity.RequestContext, id uuid.UUID, in application.ReasonInput) (application.ClaimView, error) {
		return h.svc.Reject(r.Context(), rc, id, in)
	})
}

// ReturnClaim serves POST /claims/{claimId}/return.
func (h *Handler) ReturnClaim(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.requireReview(w, r)
	if !ok {
		return
	}
	id, ok := h.claimID(w, r)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ClaimReturnReason
	if !decodeJSON(w, r, &body) {
		return
	}
	view, err := h.svc.Return(r.Context(), rc, id, application.ReasonInput{
		ReasonCode: body.ReasonCode, ReasonText: body.ReasonText, ExpectedVersion: expected,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Claim.RowVersion))
	writeJSON(w, http.StatusOK, claimView(view))
}

// CancelClaim serves POST /claims/{claimId}/cancel.
func (h *Handler) CancelClaim(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionCancel)
	if !ok {
		return
	}
	id, ok := h.claimID(w, r)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ClaimReason
	if !decodeJSON(w, r, &body) {
		return
	}
	view, err := h.svc.Cancel(r.Context(), rc, id, application.ReasonInput{
		ReasonCode: body.ReasonCode, ReasonText: body.ReasonText, ExpectedVersion: expected,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Claim.RowVersion))
	writeJSON(w, http.StatusOK, claimView(view))
}

// ListClaimVersions serves GET /claims/{claimId}/versions.
func (h *Handler) ListClaimVersions(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.claimID(w, r)
	if !ok {
		return
	}
	rows, err := h.svc.ListVersions(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	body := kapsorav1.ClaimVersionList{Items: make([]kapsorav1.ClaimVersionSummary, 0, len(rows))}
	for _, row := range rows {
		body.Items = append(body.Items, versionSummary(row))
	}
	writeJSON(w, http.StatusOK, body)
}

// GetClaimVersion serves GET /claims/{claimId}/versions/{versionNo}.
func (h *Handler) GetClaimVersion(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	access, ok := h.accessRequest(w, r)
	if !ok {
		return
	}
	id, ok := h.claimID(w, r)
	if !ok {
		return
	}
	versionNo, err := strconv.Atoi(chi.URLParam(r, "versionNo"))
	if err != nil || versionNo < 1 {
		h.writeError(w, r, application.ErrVersionNotFound)
		return
	}
	view, err := h.svc.GetVersion(r.Context(), rc, id, versionNo, access)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, kapsorav1.ClaimVersion{
		Version:    versionSummary(view.Version),
		Projection: kapsorav1.HealthProjection(view.Projection),
		Lines:      lineViews(view.Lines),
		Exceptions: exceptionViews(view.Exceptions),
	})
}

// GetClaimInvoiceReadiness serves GET /claims/{claimId}/invoice-readiness.
func (h *Handler) GetClaimInvoiceReadiness(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionRead)
	if !ok {
		return
	}
	id, ok := h.claimID(w, r)
	if !ok {
		return
	}
	readiness, err := h.svc.InvoiceReadiness(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, readinessView(readiness))
}

// decide is the shared body of approve and reject: both read the same reason payload, both
// take an If-Match, and both need a reviewer.
func (h *Handler) decide(w http.ResponseWriter, r *http.Request,
	run func(rc identity.RequestContext, id uuid.UUID, in application.ReasonInput) (application.ClaimView, error),
) {
	rc, ok := h.requireReview(w, r)
	if !ok {
		return
	}
	id, ok := h.claimID(w, r)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.ClaimDecisionReason
	if !decodeJSON(w, r, &body) {
		return
	}
	view, err := run(rc, id, application.ReasonInput{
		ReasonCode: body.ReasonCode, ReasonText: body.ReasonText,
		ReviewComment: body.ReviewComment, ExpectedVersion: expected,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Claim.RowVersion))
	writeJSON(w, http.StatusOK, claimView(view))
}

// newLines maps the generated line shape onto the application's. A malformed decimal is caught
// by the domain rather than here, so the caller is told which line and which field.
func newLines(rows []kapsorav1.NewClaimLine) []application.NewLineInput {
	out := make([]application.NewLineInput, 0, len(rows))
	for _, row := range rows {
		out = append(out, application.NewLineInput{
			LineNo: row.LineNo, ServiceDefinitionID: row.ServiceDefinitionId,
			UnitType: row.UnitType, Quantity: row.Quantity, UnitAmount: row.UnitAmount,
			LineAmount: row.LineAmount, CurrencyCode: row.CurrencyCode,
			DiagnosisID: row.DiagnosisId, MedicalReportID: row.MedicalReportId,
			PractitionerID: row.PractitionerId, Description: row.Description,
		})
	}
	return out
}

// claimView maps a projected claim onto the wire. It can only render what the service gave it:
// a field the projection cleared is nil here, and there is no branch below that decides
// whether to include one.
func claimView(view application.ClaimView) kapsorav1.Claim {
	record := view.Claim
	out := kapsorav1.Claim{
		Id: record.ID, Reference: record.Reference, PersonId: record.PersonID,
		ProgramId: record.ProgramID, EnrollmentId: record.EnrollmentID,
		ProviderOrganizationId: record.ProviderOrganizationID, DomainCode: record.DomainCode,
		CaseId: record.CaseID, FulfilmentId: record.FulfilmentID,
		SourceId:         record.SourceID,
		AuthorizationId:  record.AuthorizationID,
		CurrentVersionNo: record.CurrentVersionNo,
		Status:           kapsorav1.ClaimStatus(record.Status),
		ServiceDateFrom:  dateOf(record.ServiceDateFrom),
		ServiceDateTo:    dateOf(record.ServiceDateTo),
		Channel:          kapsorav1.ServiceRequestChannel(record.Channel),
		Projection:       kapsorav1.HealthProjection(view.Projection),
		RejectReasonCode: record.RejectReasonCode, ReturnReasonCode: record.ReturnReasonCode,
		ReviewCommentMedical:   record.ReviewCommentMedical,
		ReviewCommentFinancial: record.ReviewCommentFinancial,
		ClosedAt:               record.ClosedAt,
		Lines:                  lineViews(view.Lines),
		Exceptions:             exceptionViews(view.Exceptions),
		CreatedAt:              record.CreatedAt, RowVersion: record.RowVersion,
	}
	if record.SourceType != nil {
		source := kapsorav1.ClaimSourceType(*record.SourceType)
		out.SourceType = &source
	}
	return out
}

func lineViews(rows []application.LineView) []kapsorav1.ClaimLine {
	out := make([]kapsorav1.ClaimLine, 0, len(rows))
	for _, row := range rows {
		line := kapsorav1.ClaimLine{
			Id: row.Line.ID, LineNo: row.Line.LineNo,
			ServiceDefinitionId: row.Line.ServiceDefinitionID, ServiceCode: row.Line.ServiceCode,
			UnitType: row.Line.UnitType, Quantity: row.Line.Quantity,
			UnitAmount: row.Line.UnitAmount, LineAmount: row.Line.LineAmount,
			CurrencyCode: row.Line.CurrencyCode, DiagnosisId: row.Line.DiagnosisID,
			MedicalReportId: row.Line.MedicalReportID, PractitionerId: row.Line.PractitionerID,
			Description: row.Line.Description,
			CreatedAt:   row.Line.CreatedAt, RowVersion: row.Line.RowVersion,
		}
		if row.Decision != nil {
			decision := decisionView(*row.Decision)
			line.Decision = &decision
		}
		out = append(out, line)
	}
	return out
}

func decisionView(d application.DecisionRecord) kapsorav1.ClaimLineDecision {
	return kapsorav1.ClaimLineDecision{
		Id: d.ID, LineId: d.LineID, DecidedInVersionNo: d.DecidedInVersionNo,
		Decision:         kapsorav1.ClaimDecisionKind(d.Decision),
		ApprovedQuantity: d.ApprovedQuantity, ApprovedAmount: d.ApprovedAmount,
		ContractAmount: d.ContractAmount, PayerAmount: d.PayerAmount,
		MemberAmount: d.MemberAmount, ReasonCode: d.ReasonCode, ReasonText: d.ReasonText,
		DecidedBy: d.DecidedBy, DecidedAt: d.DecidedAt,
		Stage: kapsorav1.ClaimDecisionStage(d.Stage),
	}
}

func exceptionViews(rows []application.ClaimException) []kapsorav1.ClaimException {
	out := make([]kapsorav1.ClaimException, 0, len(rows))
	for _, row := range rows {
		item := kapsorav1.ClaimException{
			Code: row.Code, Stage: kapsorav1.ClaimDecisionStage(row.Stage),
		}
		if row.LineNo > 0 {
			lineNo := row.LineNo
			item.LineNo = &lineNo
		}
		if row.Detail != "" {
			detail := row.Detail
			item.Detail = &detail
		}
		out = append(out, item)
	}
	return out
}

func versionSummary(v application.VersionRecord) kapsorav1.ClaimVersionSummary {
	return kapsorav1.ClaimVersionSummary{
		Id: v.ID, VersionNo: v.VersionNo,
		Status:      kapsorav1.ClaimVersionStatus(v.Status),
		SubmittedAt: v.SubmittedAt, SubmittedBy: v.SubmittedBy,
		ReturnedAt: v.ReturnedAt, ReturnedBy: v.ReturnedBy,
		ReturnReasonCode: v.ReturnReasonCode, ReturnReasonText: v.ReturnReasonText,
		CreatedAt: v.CreatedAt, RowVersion: v.RowVersion,
	}
}

// dateOf renders a service date as the contract's date type.
func dateOf(t time.Time) openapi_types.Date { return openapi_types.Date{Time: t} }
