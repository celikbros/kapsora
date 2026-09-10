package billinghttp

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/celikbros/kapsora/api/generated/kapsorav1"
	"github.com/celikbros/kapsora/internal/billing/application"
	"github.com/celikbros/kapsora/internal/billing/domain"
	"github.com/celikbros/kapsora/internal/identity"
	"github.com/celikbros/kapsora/internal/platform/httpx"
)

// /api/v1/settlements and /api/v1/reimbursements: what the payer owes the provider, what it
// actually paid, and what it pays the member back (WP-I7-04).
//
// Two things shape every route here, beyond what the package comment already says.
//
// **No response body in this file carries a bank account number.** `bankAccountMasked` is four
// characters and the mapper below is the only place a reimbursement becomes a wire shape, so
// there is no second mapper that could forget. The generated `Reimbursement` type has no field
// the number could go in, which is the version of this promise a compiler can check.
//
// **The member's own routes resolve "me" on the server.** `getMyReimbursements` takes no
// `personId` and `createReimbursement` accepts one only when it is the caller's own;
// `identity.RequirePerson` is what answers both, exactly as it does for a booking.

// Permissions guarding the settlement and reimbursement routes.
const (
	PermissionSettlementRead          = application.PermissionSettlementRead
	PermissionSettlementApprove       = application.PermissionSettlementApprove
	PermissionSettlementRecordPayment = application.PermissionSettlementRecordPayment
	PermissionClaimFinancialReview    = application.PermissionClaimFinancialReview
	PermissionServiceRequestRead      = application.PermissionServiceRequestRead
	PermissionServiceRequestCreate    = application.PermissionServiceRequestCreate
)

// SettlementMiddlewares are the Idempotency-Key wrappers the integrator applies in cmd/api.
//
// Every one of them is required rather than optional, and the reason is the same for all of
// them: a settlement is money. An approval replayed must release one settlement and publish one
// event, a payment record replayed must record one transfer, and a decision replayed must
// consume one member's wallet once.
type SettlementMiddlewares struct {
	ApproveSettlement          func(http.Handler) http.Handler
	CancelSettlement           func(http.Handler) http.Handler
	CreatePaymentRecord        func(http.Handler) http.Handler
	CreateReimbursement        func(http.Handler) http.Handler
	SubmitReimbursement        func(http.Handler) http.Handler
	DecideReimbursement        func(http.Handler) http.Handler
	RecordReimbursementPayment func(http.Handler) http.Handler
}

// SettlementRoutes mounts everything below /settlements.
func (h *Handler) SettlementRoutes(r chi.Router, mw SettlementMiddlewares) {
	r.Get("/", h.ListSettlements)
	r.Get("/{settlementId}", h.GetSettlement)
	r.With(wrap(mw.ApproveSettlement)).Post("/{settlementId}/approve", h.ApproveSettlement)
	r.With(wrap(mw.CancelSettlement)).Post("/{settlementId}/cancel", h.CancelSettlement)
	r.Get("/{settlementId}/payment-records", h.ListPaymentRecords)
	r.With(wrap(mw.CreatePaymentRecord)).
		Post("/{settlementId}/payment-records", h.CreatePaymentRecord)
}

// ReimbursementRoutes mounts everything below /reimbursements.
func (h *Handler) ReimbursementRoutes(r chi.Router, mw SettlementMiddlewares) {
	r.Get("/", h.ListReimbursements)
	r.With(wrap(mw.CreateReimbursement)).Post("/", h.CreateReimbursement)
	r.Get("/{reimbursementId}", h.GetReimbursement)
	r.With(wrap(mw.SubmitReimbursement)).Post("/{reimbursementId}/submit", h.SubmitReimbursement)
	r.With(wrap(mw.DecideReimbursement)).Post("/{reimbursementId}/decide", h.DecideReimbursement)
	r.With(wrap(mw.RecordReimbursementPayment)).
		Post("/{reimbursementId}/payment", h.RecordReimbursementPayment)
}

// MyReimbursementRoutes mounts /me/reimbursements: the member's own list, resolved through the
// PERSON scope and nowhere else.
func (h *Handler) MyReimbursementRoutes(r chi.Router) {
	r.Get("/", h.GetMyReimbursements)
}

// ListSettlements serves GET /settlements.
func (h *Handler) ListSettlements(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionSettlementRead)
	if !ok {
		return
	}
	var fields []domain.FieldError
	filter := application.SettlementFilter{
		Cursor: r.URL.Query().Get("cursor"), Limit: queryLimit(r),
		ProviderOrganizationID: queryUUID(r, "providerOrganizationId", &fields),
		PayerOrganizationID:    queryUUID(r, "payerOrganizationId", &fields),
		BatchID:                queryUUID(r, "batchId", &fields),
		Status:                 r.URL.Query().Get("status"),
		CurrencyCode:           r.URL.Query().Get("currencyCode"),
		DueFrom:                queryDate(r, "dueFrom", &fields),
		DueTo:                  queryDate(r, "dueTo", &fields),
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	page, err := h.svc.ListSettlements(r.Context(), rc, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	body := kapsorav1.SettlementPage{
		Items: make([]kapsorav1.Settlement, 0, len(page.Items)),
	}
	for _, item := range page.Items {
		// A list row carries neither the recoveries nor the payments: a page of fifty
		// settlements is not a place to read five hundred bank references, and the detail is
		// one request away.
		body.Items = append(body.Items, settlementBody(application.SettlementView{
			Settlement: item,
		}))
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		body.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, body)
}

// GetSettlement serves GET /settlements/{settlementId}.
func (h *Handler) GetSettlement(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionSettlementRead)
	if !ok {
		return
	}
	id, ok := h.settlementID(w, r)
	if !ok {
		return
	}
	view, err := h.svc.GetSettlement(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Settlement.RowVersion))
	writeJSON(w, http.StatusOK, settlementBody(view))
}

// ApproveSettlement serves POST /settlements/{settlementId}/approve.
func (h *Handler) ApproveSettlement(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionSettlementApprove)
	if !ok {
		return
	}
	id, ok := h.settlementID(w, r)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	view, err := h.svc.ApproveSettlement(r.Context(), rc, id, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Settlement.RowVersion))
	writeJSON(w, http.StatusOK, settlementBody(view))
}

// CancelSettlement serves POST /settlements/{settlementId}/cancel.
func (h *Handler) CancelSettlement(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionSettlementApprove)
	if !ok {
		return
	}
	id, ok := h.settlementID(w, r)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.CancelSettlement
	if !decodeJSON(w, r, &body) {
		return
	}
	view, err := h.svc.CancelSettlement(r.Context(), rc, id, body.ReasonCode, body.ReasonText,
		expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Settlement.RowVersion))
	writeJSON(w, http.StatusOK, settlementBody(view))
}

// ListPaymentRecords serves GET /settlements/{settlementId}/payment-records.
func (h *Handler) ListPaymentRecords(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionSettlementRead)
	if !ok {
		return
	}
	id, ok := h.settlementID(w, r)
	if !ok {
		return
	}
	rows, err := h.svc.ListPaymentRecords(r.Context(), rc, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	body := kapsorav1.PaymentRecordList{
		Items: make([]kapsorav1.PaymentRecord, 0, len(rows)),
	}
	for _, row := range rows {
		body.Items = append(body.Items, paymentRecordBody(row))
	}
	writeJSON(w, http.StatusOK, body)
}

// CreatePaymentRecord serves POST /settlements/{settlementId}/payment-records.
//
// The permission is either the finance clerk's new `settlement.record_payment` or the
// approver's `settlement.approve`. Two grants for one route rather than one, because a tenant
// that wants the approver never to touch the payment file has to be able to arrange that, and
// a tenant where one person does both has to be able to arrange that too.
func (h *Handler) CreatePaymentRecord(w http.ResponseWriter, r *http.Request) {
	rc, err := identity.Require(r.Context(), PermissionSettlementRecordPayment)
	if err != nil {
		rc, err = identity.Require(r.Context(), PermissionSettlementApprove)
	}
	if err != nil {
		h.deny.Deny(w, r, err, PermissionSettlementRecordPayment)
		return
	}
	id, ok := h.settlementID(w, r)
	if !ok {
		return
	}
	var body kapsorav1.CreatePaymentRecord
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.CreatePaymentRecordInput{
		ExternalReference: body.ExternalReference,
		Amount:            body.Amount,
		PaidAt:            body.PaidAt,
		Notes:             body.Notes,
	}
	if body.CurrencyCode != nil {
		in.CurrencyCode = *body.CurrencyCode
	}
	if body.Source != nil {
		in.Source = string(*body.Source)
	}
	view, err := h.svc.CreatePaymentRecord(r.Context(), rc, id, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(view.Settlement.RowVersion))
	writeJSON(w, http.StatusCreated, settlementBody(view))
}

// ListReimbursements serves GET /reimbursements: the payer's finance side.
func (h *Handler) ListReimbursements(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionClaimFinancialReview)
	if !ok {
		return
	}
	var fields []domain.FieldError
	filter := application.ReimbursementFilter{
		Cursor: r.URL.Query().Get("cursor"), Limit: queryLimit(r),
		PersonID: queryUUID(r, "personId", &fields),
		Status:   r.URL.Query().Get("status"),
		DateFrom: queryDate(r, "from", &fields),
		DateTo:   queryDate(r, "to", &fields),
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	h.writeReimbursementPage(w, r, rc, filter)
}

// GetMyReimbursements serves GET /me/reimbursements.
//
// There is no `personId` parameter and nowhere in the request to name another person: the
// answer comes from the caller's PERSON grant, exactly as `getMyPerson` does.
func (h *Handler) GetMyReimbursements(w http.ResponseWriter, r *http.Request) {
	rc, personID, err := identity.RequirePersonWith(r.Context(), PermissionServiceRequestRead,
		uuid.Nil)
	if err != nil {
		h.writePersonError(w, r, err, PermissionServiceRequestRead)
		return
	}
	var fields []domain.FieldError
	filter := application.ReimbursementFilter{
		Cursor: r.URL.Query().Get("cursor"), Limit: queryLimit(r),
		PersonID: &personID, Status: r.URL.Query().Get("status"),
	}
	if len(fields) > 0 {
		writeValidation(w, r, fields)
		return
	}
	h.writeReimbursementPage(w, r, rc, filter)
}

// writeReimbursementPage is the shared tail of the two list endpoints, so that the member's own
// list and the back-office one answer the same shape.
func (h *Handler) writeReimbursementPage(w http.ResponseWriter, r *http.Request,
	rc identity.RequestContext, filter application.ReimbursementFilter,
) {
	page, err := h.svc.ListReimbursements(r.Context(), rc, filter)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	body := kapsorav1.ReimbursementPage{
		Items: make([]kapsorav1.Reimbursement, 0, len(page.Items)),
	}
	for _, item := range page.Items {
		body.Items = append(body.Items, reimbursementBody(item))
	}
	if page.NextCursor != "" {
		cursor := page.NextCursor
		body.NextCursor = &cursor
	}
	writeJSON(w, http.StatusOK, body)
}

// CreateReimbursement serves POST /reimbursements.
func (h *Handler) CreateReimbursement(w http.ResponseWriter, r *http.Request) {
	var body kapsorav1.CreateReimbursement
	if !decodeJSON(w, r, &body) {
		return
	}
	requested := uuid.Nil
	if body.PersonId != nil {
		requested = *body.PersonId
	}
	// Whose reimbursement this is, is the server's answer. A body naming the caller's own
	// person is accepted; one naming anybody else is refused here, before a single row is read.
	rc, personID, err := identity.RequirePersonWith(r.Context(), PermissionServiceRequestCreate,
		requested)
	if err != nil {
		h.writePersonError(w, r, err, PermissionServiceRequestCreate)
		return
	}
	in := application.CreateReimbursementInput{
		PersonID: personID, ServiceRequestID: body.ServiceRequestId,
		ReceiptDocumentID: body.ReceiptDocumentId,
		RequestedAmount:   body.RequestedAmount,
		BankAccount:       body.BankAccount,
	}
	if body.CurrencyCode != nil {
		in.CurrencyCode = *body.CurrencyCode
	}
	record, err := h.svc.CreateReimbursement(r.Context(), rc, in)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(record.RowVersion))
	w.Header().Set("Location", "/api/v1/reimbursements/"+record.ID.String())
	writeJSON(w, http.StatusCreated, reimbursementBody(record))
}

// GetReimbursement serves GET /reimbursements/{reimbursementId}.
//
// A member reading their own goes through the same route: the PERSON scope narrows the read in
// SQL, so another member's row is not found rather than found and refused — and 404 is the same
// answer an id that does not exist gets, on purpose.
func (h *Handler) GetReimbursement(w http.ResponseWriter, r *http.Request) {
	rc, person, ok := h.reimbursementCaller(w, r, PermissionClaimFinancialReview,
		PermissionServiceRequestRead)
	if !ok {
		return
	}
	id, ok := h.reimbursementID(w, r)
	if !ok {
		return
	}
	record, err := h.svc.GetReimbursement(r.Context(), rc, person, id)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(record.RowVersion))
	writeJSON(w, http.StatusOK, reimbursementBody(record))
}

// SubmitReimbursement serves POST /reimbursements/{reimbursementId}/submit.
func (h *Handler) SubmitReimbursement(w http.ResponseWriter, r *http.Request) {
	rc, personID, err := identity.RequirePersonWith(r.Context(), PermissionServiceRequestCreate,
		uuid.Nil)
	if err != nil {
		h.writePersonError(w, r, err, PermissionServiceRequestCreate)
		return
	}
	id, ok := h.reimbursementID(w, r)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	record, err := h.svc.SubmitReimbursement(r.Context(), rc, personID, id, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(record.RowVersion))
	writeJSON(w, http.StatusOK, reimbursementBody(record))
}

// DecideReimbursement serves POST /reimbursements/{reimbursementId}/decide.
func (h *Handler) DecideReimbursement(w http.ResponseWriter, r *http.Request) {
	rc, ok := h.require(w, r, PermissionClaimFinancialReview)
	if !ok {
		return
	}
	id, ok := h.reimbursementID(w, r)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.DecideReimbursement
	if !decodeJSON(w, r, &body) {
		return
	}
	in := application.DecideReimbursementInput{
		Decision: string(body.Decision), ReasonText: body.ReasonText,
	}
	if body.ApprovedAmount != nil {
		in.ApprovedAmount = *body.ApprovedAmount
	}
	if body.ReasonCode != nil {
		in.ReasonCode = *body.ReasonCode
	}
	record, err := h.svc.DecideReimbursement(r.Context(), rc, id, in, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(record.RowVersion))
	writeJSON(w, http.StatusOK, reimbursementBody(record))
}

// RecordReimbursementPayment serves POST /reimbursements/{reimbursementId}/payment.
func (h *Handler) RecordReimbursementPayment(w http.ResponseWriter, r *http.Request) {
	rc, err := identity.Require(r.Context(), PermissionSettlementRecordPayment)
	if err != nil {
		rc, err = identity.Require(r.Context(), PermissionClaimFinancialReview)
	}
	if err != nil {
		h.deny.Deny(w, r, err, PermissionSettlementRecordPayment)
		return
	}
	id, ok := h.reimbursementID(w, r)
	if !ok {
		return
	}
	expected, ok := requireIfMatch(w, r)
	if !ok {
		return
	}
	var body kapsorav1.RecordReimbursementPayment
	if !decodeJSON(w, r, &body) {
		return
	}
	paidAt := time.Time{}
	if body.PaidAt != nil {
		paidAt = *body.PaidAt
	}
	record, err := h.svc.RecordReimbursementPayment(r.Context(), rc, id, body.PaymentReference,
		paidAt, expected)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(record.RowVersion))
	writeJSON(w, http.StatusOK, reimbursementBody(record))
}

// reimbursementCaller resolves a caller that may be either the payer's reviewer or the member
// themselves.
//
// The reviewer's permission is tried first and answers with no person filter; a caller who does
// not hold it falls back to the member's own route, which answers with the PERSON scope. A
// caller who holds neither is denied under the reviewer's permission, which is the more
// specific of the two and therefore the more useful thing to be told.
func (h *Handler) reimbursementCaller(w http.ResponseWriter, r *http.Request,
	reviewPermission, memberPermission string,
) (identity.RequestContext, *uuid.UUID, bool) {
	if rc, err := identity.Require(r.Context(), reviewPermission); err == nil {
		return rc, nil, true
	}
	rc, personID, err := identity.RequirePersonWith(r.Context(), memberPermission, uuid.Nil)
	if err != nil {
		h.deny.Deny(w, r, identity.ErrPermissionDenied, reviewPermission)
		return identity.RequestContext{}, nil, false
	}
	return rc, &personID, true
}

// writePersonError answers the two refusals `identity.RequirePerson` produces, and hands
// anything else to the auditing denier.
func (h *Handler) writePersonError(w http.ResponseWriter, r *http.Request, err error,
	permission string,
) {
	switch {
	case errors.Is(err, identity.ErrPersonBindingMissing):
		httpx.WritePersonBindingMissingProblem(w, r)
	case errors.Is(err, identity.ErrPersonScope):
		httpx.WritePersonScopeProblem(w, r)
	default:
		h.deny.Deny(w, r, err, permission)
	}
}

// settlementID reads the settlement id from the path. A malformed id is indistinguishable from
// an unknown one, so both are answered 404: that a settlement exists at all is somebody else's
// business.
func (h *Handler) settlementID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "settlementId"))
	if err != nil {
		h.writeError(w, r, application.ErrSettlementNotFound)
		return uuid.Nil, false
	}
	return id, true
}

// reimbursementID reads the reimbursement id from the path, on the same rule.
func (h *Handler) reimbursementID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "reimbursementId"))
	if err != nil {
		h.writeError(w, r, application.ErrReimbursementNotFound)
		return uuid.Nil, false
	}
	return id, true
}

// settlementBody maps the application view onto the wire shape.
func settlementBody(v application.SettlementView) kapsorav1.Settlement {
	rec := v.Settlement
	out := kapsorav1.Settlement{
		Id:                     rec.ID,
		Reference:              rec.Reference,
		BatchId:                rec.BatchID,
		VersionNo:              rec.VersionNo,
		ProviderOrganizationId: rec.ProviderOrganizationID,
		PayerOrganizationId:    rec.PayerOrganizationID,
		CurrencyCode:           rec.CurrencyCode,
		ApprovedAmount:         rec.ApprovedAmount,
		WithheldAmount:         rec.WithheldAmount,
		PayableAmount:          rec.PayableAmount,
		PaidAmount:             rec.PaidAmount,
		DueDate:                openapi_types.Date{Time: rec.DueDate},
		SettlementMethod:       kapsorav1.SettlementSettlementMethod(rec.SettlementMethod),
		Status:                 kapsorav1.SettlementStatus(rec.Status),
		ApprovedBy:             rec.ApprovedBy,
		ApprovedAt:             rec.ApprovedAt,
		CheckedBy:              rec.CheckedBy,
		PostingId:              rec.PostingID,
		Recoveries:             make([]kapsorav1.SettlementRecovery, 0, len(v.Recoveries)),
		Payments:               make([]kapsorav1.PaymentRecord, 0, len(v.Payments)),
		CreatedAt:              rec.CreatedAt,
		RowVersion:             rec.RowVersion,
	}
	if rec.ProviderName != "" {
		name := rec.ProviderName
		out.ProviderName = &name
	}
	if rec.BatchReference != "" {
		reference := rec.BatchReference
		out.BatchReference = &reference
	}
	if rec.CancelReasonCode != "" {
		code := rec.CancelReasonCode
		out.CancelReasonCode = &code
	}
	for _, recovery := range v.Recoveries {
		out.Recoveries = append(out.Recoveries, kapsorav1.SettlementRecovery{
			Id: recovery.ID, ClaimId: recovery.ClaimID,
			AdjustmentId: recovery.AdjustmentID, Amount: recovery.Amount,
			CreatedAt: recovery.CreatedAt,
		})
	}
	for _, payment := range v.Payments {
		out.Payments = append(out.Payments, paymentRecordBody(payment))
	}
	return out
}

// paymentRecordBody maps one payment record.
func paymentRecordBody(rec application.PaymentRecordRecord) kapsorav1.PaymentRecord {
	return kapsorav1.PaymentRecord{
		Id:                     rec.ID,
		SettlementId:           rec.SettlementID,
		ProviderOrganizationId: rec.ProviderOrganizationID,
		ExternalReference:      rec.ExternalReference,
		Amount:                 rec.Amount,
		CurrencyCode:           rec.CurrencyCode,
		PaidAt:                 rec.PaidAt,
		Source:                 kapsorav1.PaymentRecordSource(rec.Source),
		Status:                 kapsorav1.PaymentRecordStatus(rec.Status),
		RecordedBy:             rec.RecordedBy,
		Notes:                  rec.Notes,
		CreatedAt:              rec.CreatedAt,
		RowVersion:             rec.RowVersion,
	}
}

// reimbursementBody maps one reimbursement.
//
// The account is four characters and there is no field on the generated type that could hold
// more. `approvedAmount` is absent rather than an empty string until somebody decides, because
// an empty string beside "approved" would read as a decision somebody made.
func reimbursementBody(rec application.ReimbursementRecord) kapsorav1.Reimbursement {
	out := kapsorav1.Reimbursement{
		Id:                     rec.ID,
		Reference:              rec.Reference,
		PersonId:               rec.PersonID,
		EnrollmentId:           rec.EnrollmentID,
		ServiceRequestId:       rec.ServiceRequestID,
		ClaimId:                rec.ClaimID,
		ReceiptDocumentId:      rec.ReceiptDocumentID,
		ServiceDefinitionId:    rec.ServiceDefinitionID,
		ServiceDate:            openapi_types.Date{Time: rec.ServiceDate},
		ProviderOrganizationId: rec.ProviderOrganizationID,
		RequestedAmount:        rec.RequestedAmount,
		CurrencyCode:           rec.CurrencyCode,
		BankAccountMasked:      rec.BankAccountMasked,
		Status:                 kapsorav1.ReimbursementStatus(rec.Status),
		DuplicateOfId:          rec.DuplicateOfID,
		DecidedBy:              rec.DecidedBy,
		DecidedAt:              rec.DecidedAt,
		SubmittedAt:            rec.SubmittedAt,
		PaidAt:                 rec.PaidAt,
		CreatedAt:              rec.CreatedAt,
		RowVersion:             rec.RowVersion,
	}
	if rec.ApprovedAmount != "" {
		amount := rec.ApprovedAmount
		out.ApprovedAmount = &amount
	}
	if rec.DuplicateReference != "" {
		reference := rec.DuplicateReference
		out.DuplicateOfReference = &reference
	}
	if rec.DecisionReasonCode != "" {
		code := rec.DecisionReasonCode
		out.DecisionReasonCode = &code
	}
	if rec.PaymentReference != "" {
		reference := rec.PaymentReference
		out.PaymentReference = &reference
	}
	return out
}

// writeSettlementError maps WP-I7-04's own errors to problem codes. Every title is Turkish and
// every one of them says what the caller can do next; the two that carry extension members do
// so because the fact is what makes them actionable — a refused payment names the remainder a
// clerk may still enter, and a duplicate names the earlier request rather than only saying
// "duplicate".
//
// It is a separate function from writeError for the reason writeBatchError is: so that one
// switch does not grow to fifty arms.
func writeSettlementError(w http.ResponseWriter, r *http.Request, err error) bool {
	var exceeds *application.PaymentExceedsError
	var duplicate *application.DuplicateReimbursementError
	var ceiling *application.CeilingExceededError
	switch {
	case errors.As(err, &exceeds):
		httpx.WriteProblem(w, r, httpx.Problem{
			Type:   httpx.ProblemTypeBase + "settlements/payment-exceeds",
			Title:  "Ödeme kaydı mutabakat tutarını aşıyor",
			Status: http.StatusConflict, Code: "PAYMENT_EXCEEDS_SETTLEMENT",
			Detail: "Bir mutabakata kaydedilen ödemelerin toplamı ödenecek tutarı geçemez; " +
				"kalan tutar kadar kayıt girebilirsiniz.",
			Extensions: map[string]any{
				"settlementId":  exceeds.SettlementID.String(),
				"amount":        exceeds.Amount,
				"paidAmount":    exceeds.PaidAmount,
				"payableAmount": exceeds.PayableAmount,
				"remainder":     exceeds.Remainder,
				"currencyCode":  exceeds.CurrencyCode,
			},
		})
		return true
	case errors.As(err, &duplicate):
		extensions := map[string]any{
			"existingReimbursementId": duplicate.ExistingID.String(),
			"matchedBy":               duplicateMatchKind(duplicate.ByReceipt),
		}
		if duplicate.ExistingReference != "" {
			extensions["existingReference"] = duplicate.ExistingReference
		}
		if !duplicate.ExistingDate.IsZero() {
			extensions["existingServiceDate"] = duplicate.ExistingDate.Format(time.DateOnly)
		}
		httpx.WriteProblem(w, r, httpx.Problem{
			Type:   httpx.ProblemTypeBase + "reimbursements/duplicate",
			Title:  "Bu belge için daha önce geri ödeme talebi yapılmış",
			Status: http.StatusConflict, Code: "REIMBURSEMENT_DUPLICATE",
			Detail: "Aynı fiş ya da aynı sağlayıcı, tarih ve tutar için açık bir başvurunuz " +
				"zaten var; önceki başvurunun numarası yanıtta yer alıyor.",
			Extensions: extensions,
		})
		return true
	case errors.As(err, &ceiling):
		httpx.WriteProblem(w, r, httpx.Problem{
			Type:   httpx.ProblemTypeBase + "reimbursements/ceiling-exceeded",
			Title:  "Talep edilen tutar sözleşmedeki üst sınırın üzerinde",
			Status: http.StatusUnprocessableEntity, Code: "REIMBURSEMENT_CEILING_EXCEEDED",
			Detail: "Bu hizmet için sözleşme bir üst sınır tanımlıyor; talebinizi bu tutara " +
				"kadar girebilirsiniz.",
			Extensions: map[string]any{
				"requestedAmount": ceiling.Requested,
				"ceilingAmount":   ceiling.Ceiling,
				"currencyCode":    ceiling.CurrencyCode,
			},
		})
		return true
	case errors.Is(err, application.ErrSettlementNotFound):
		problem(w, r, http.StatusNotFound, "settlements/not-found", "SETTLEMENT_NOT_FOUND",
			"Mutabakat bulunamadı", "")
		return true
	case errors.Is(err, application.ErrSettlementTransitionInvalid):
		problem(w, r, http.StatusConflict, "settlements/transition-invalid",
			"SETTLEMENT_TRANSITION_INVALID", "Mutabakat bu durumda bu işleme uygun değil", "")
		return true
	case errors.Is(err, application.ErrSettlementDeciderCannotApprove):
		problem(w, r, http.StatusForbidden, "settlements/decider-cannot-approve",
			"SETTLEMENT_DECIDER_CANNOT_APPROVE",
			"İcmali karara bağlayan kişi bu tutarda mutabakatı onaylayamaz",
			"Eşiğin üzerindeki mutabakatlarda icmali karara bağlayan kişi ödemeyi serbest "+
				"bırakamaz; ikinci bir onaylayıcı gerekir.")
		return true
	case errors.Is(err, application.ErrPaymentTermMissing):
		problem(w, r, http.StatusUnprocessableEntity, "settlements/payment-term-missing",
			"PAYMENT_TERM_MISSING", "Sözleşmede ödeme vadesi tanımlı değil",
			"Mutabakatın vade tarihi sözleşmenin ödeme koşulundan gelir; sözleşme sürümüne "+
				"ödeme vadesi ekleyin.")
		return true
	case errors.Is(err, application.ErrSettlementHasPayments):
		problem(w, r, http.StatusConflict, "settlements/has-payments",
			"SETTLEMENT_HAS_PAYMENTS", "Ödeme kaydı bulunan mutabakat iptal edilemez",
			"Yanlış girilmiş bir ödeme kaydı itirazlı (DISPUTED) işaretlenir; mutabakat "+
				"silinmez.")
		return true
	case errors.Is(err, application.ErrPaymentReferenceTaken):
		problem(w, r, http.StatusConflict, "settlements/payment-reference-taken",
			"PAYMENT_REFERENCE_TAKEN", "Bu banka referansı bu sağlayıcı için zaten kayıtlı",
			"Aynı havalenin iki kez girilmesi mutabakatı ödenmiş gösterir; referansı kontrol "+
				"edin.")
		return true
	case errors.Is(err, application.ErrPaymentNotAllowed):
		problem(w, r, http.StatusConflict, "settlements/payment-not-allowed",
			"PAYMENT_NOT_ALLOWED", "Bu mutabakata şu anda ödeme kaydedilemez",
			"Ödeme kaydı yalnızca onaylanmış, muhasebeleştirilmiş ya da kısmen ödenmiş bir "+
				"mutabakata girilir.")
		return true
	case errors.Is(err, application.ErrReimbursementNotFound):
		problem(w, r, http.StatusNotFound, "reimbursements/not-found",
			"REIMBURSEMENT_NOT_FOUND", "Geri ödeme başvurusu bulunamadı", "")
		return true
	case errors.Is(err, application.ErrReimbursementTransitionInvalid):
		problem(w, r, http.StatusConflict, "reimbursements/transition-invalid",
			"REIMBURSEMENT_TRANSITION_INVALID",
			"Geri ödeme başvurusu bu durumda bu işleme uygun değil", "")
		return true
	case errors.Is(err, application.ErrRequestUnusable):
		problem(w, r, http.StatusUnprocessableEntity, "reimbursements/request-unusable",
			"REIMBURSEMENT_REQUEST_UNUSABLE", "Bu talep geri ödemeye uygun değil",
			"Geri ödeme, kendi adınıza açılmış ve bir sağlayıcı adı taşıyan bir REIMBURSEMENT "+
				"talebine bağlanır.")
		return true
	case errors.Is(err, application.ErrReceiptUnusable):
		problem(w, r, http.StatusUnprocessableEntity, "reimbursements/receipt-unusable",
			"REIMBURSEMENT_RECEIPT_UNUSABLE", "Fiş ya da fatura belgesi kullanılabilir değil",
			"Belge bu kuruma ait, taranmış ve temiz çıkmış olmalı.")
		return true
	case errors.Is(err, application.ErrNotEligibleOnDate):
		problem(w, r, http.StatusUnprocessableEntity, "reimbursements/not-eligible",
			"REIMBURSEMENT_NOT_ELIGIBLE", "Hizmet tarihinde kapsam dışındasınız",
			"Geri ödeme, paranın harcandığı gün geçerli olan bir üyeliğe bağlanır.")
		return true
	case errors.Is(err, application.ErrEntitlementAccountNotFound):
		problem(w, r, http.StatusUnprocessableEntity, "reimbursements/entitlement-not-found",
			"ENTITLEMENT_ACCOUNT_NOT_FOUND", "Bu hizmet için parasal hak hesabı bulunamadı",
			"Onaylanan tutar üyenin parasal hakkından düşülür; planda bu para biriminde bir "+
				"parasal hak tanımlı olmalı.")
		return true
	case errors.Is(err, application.ErrEntitlementInsufficient):
		problem(w, r, http.StatusConflict, "reimbursements/entitlement-insufficient",
			"ENTITLEMENT_INSUFFICIENT", "Üyenin kalan hakkı onaylanan tutar için yeterli değil",
			"Kalan hak kadar kısmi onay verebilirsiniz.")
		return true
	}
	return false
}

// duplicateMatchKind names which half of the duplicate rule matched, so a client can word the
// message differently: the same receipt is one conversation and the same provider, day and
// amount is another.
func duplicateMatchKind(byReceipt bool) string {
	if byReceipt {
		return "RECEIPT"
	}
	return "PROVIDER_DATE_AMOUNT"
}
